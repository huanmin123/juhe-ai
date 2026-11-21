// Package authzgovernance implements the one-time data migration for the
// recipient-consent authorization model (schema revision 28).
//
// The migration is deliberately conservative: legacy rows that cannot be
// mapped unambiguously block the upgrade instead of being guessed. It never
// invents consent: legacy acceptances are recorded as acceptance_mode='legacy'
// with accepted_by NULL and a 30-day review deadline.
package authzgovernance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
)

// Options controls the migration.
type Options struct {
	Secret              string
	Now                 time.Time
	ReviewDeadlineDays  int
	TeamMemberlessTTL   time.Duration
	PendingOfferTTL     time.Duration
	OwnerApprovalTTL    time.Duration
	TeamApprovalTTL     time.Duration
	MemberOfferTTL      time.Duration
	LegacyReviewEnabled bool
}

func (o Options) withDefaults() Options {
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	} else {
		o.Now = o.Now.UTC()
	}
	if o.ReviewDeadlineDays <= 0 {
		o.ReviewDeadlineDays = 30
	}
	if o.TeamMemberlessTTL <= 0 {
		o.TeamMemberlessTTL = 14 * 24 * time.Hour
	}
	if o.PendingOfferTTL <= 0 {
		o.PendingOfferTTL = 7 * 24 * time.Hour
	}
	if o.OwnerApprovalTTL <= 0 {
		o.OwnerApprovalTTL = 7 * 24 * time.Hour
	}
	if o.TeamApprovalTTL <= 0 {
		o.TeamApprovalTTL = 14 * 24 * time.Hour
	}
	if o.MemberOfferTTL <= 0 {
		o.MemberOfferTTL = 7 * 24 * time.Hour
	}
	return o
}

// Result is the migration report persisted in the upgrade ledger detail.
type Result struct {
	SourcesMapped             int      `json:"sourcesMapped"`
	SourcesStatusMigrated     int      `json:"sourcesStatusMigrated"`
	SourcesRevisionBackfilled int      `json:"sourcesRevisionBackfilled"`
	GrantsLegacyMarked        int      `json:"grantsLegacyMarked"`
	GroupSnapshotsCreated     int      `json:"groupSnapshotsCreated"`
	GroupSettingsBackfilled   int      `json:"groupSettingsBackfilled"`
	PendingTTLBackfilled      int      `json:"pendingTtlBackfilled"`
	LegacyNotifications       int      `json:"legacyNotifications"`
	ConstraintsValidated      []string `json:"constraintsValidated"`
	ForeignKeysTightened      []string `json:"foreignKeysTightened"`
	OrphanSources             []string `json:"orphanSources,omitempty"`
	OrphanGrants              []string `json:"orphanGrants,omitempty"`
	BlockedReasons            []string `json:"blockedReasons,omitempty"`
}

// ErrNeedsReview is returned when ambiguous legacy data must be reviewed
// before the migration can proceed.
var ErrNeedsReview = errors.New("授权迁移存在需要人工复核的存量数据")

// Apply runs the data migration. It is idempotent.
func Apply(ctx context.Context, db *sql.DB, options Options) (Result, error) {
	options = options.withDefaults()
	if strings.TrimSpace(options.Secret) == "" {
		return Result{}, errors.New("授权迁移需要 revision HMAC secret")
	}
	nowText := options.Now.Format(time.RFC3339Nano)
	reviewDeadline := options.Now.Add(time.Duration(options.ReviewDeadlineDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	var result Result

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	// 1. Snapshot every account revision referenced by a grant or source.
	revisions, revisionVersions, missingAccounts, err := loadAccountRevisions(ctx, tx, options.Secret)
	if err != nil {
		return result, err
	}
	if len(missingAccounts) > 0 {
		result.OrphanGrants = missingAccounts
	}

	// 2. Backfill legacy acceptance fields, terminal fields, pending TTL and
	// grant baseline revisions. This must run before source status mapping and
	// grant_id mapping: the expand-phase accepted-revision CHECK is NOT VALID
	// but still enforced on updated rows, and mapping superseded/covered_by_team
	// sources to active/dormant first would leave them without an accepted
	// revision. The backfill therefore treats that future target status as live.
	grantMarked, sourceRevisionCount, pendingCount, err := backfillGrantsAndSources(ctx, tx,
		revisions, revisionVersions, options, nowText, reviewDeadline)
	if err != nil {
		return result, err
	}
	result.GrantsLegacyMarked = grantMarked
	result.SourcesRevisionBackfilled = sourceRevisionCount
	result.PendingTTLBackfilled = pendingCount

	// 3. Map legacy superseded/revoked source statuses that do not depend on
	// the parent grant row. Parent-grant terminal mapping runs later, after
	// grant_id is matched; before revision 28 legacy sources have grant_id NULL
	// and a LEFT JOIN would never see the parent status.
	migrated, blocked, err := migrateSourceStatuses(ctx, tx)
	if err != nil {
		return result, err
	}
	result.SourcesStatusMigrated = migrated
	if len(blocked) > 0 {
		result.BlockedReasons = append(result.BlockedReasons, blocked...)
		return result, fmt.Errorf("%w: %s", ErrNeedsReview, strings.Join(blocked, "; "))
	}

	// 4. Backfill grant_id with lifecycle-aware matching.
	mapped, orphanSources, err := backfillGrantIDs(ctx, tx)
	if err != nil {
		return result, err
	}
	result.SourcesMapped = mapped
	result.OrphanSources = orphanSources
	if len(orphanSources) > 0 {
		return result, fmt.Errorf("%w: source grant_id 无法唯一匹配: %s", ErrNeedsReview, strings.Join(orphanSources, ","))
	}

	// 5. Now that source.grant_id is known, map live sources whose parent grant
	// is already terminal. Expired sources must lose the migration-computed
	// accepted revision so a later extend restores them through needs_review
	// instead of silently reviving stale material.
	parentMapped, err := migrateTerminalParentSources(ctx, tx)
	if err != nil {
		return result, err
	}
	result.SourcesStatusMigrated += parentMapped

	// 6. Revoke orphan resources (grants whose resource no longer exists).
	if err := revokeOrphanResources(ctx, tx, nowText); err != nil {
		return result, err
	}

	// 6.5 Preflight the RESTRICT foreign keys before any VALIDATE so missing
	// grantee accounts/teams are reported as reviewable row ids instead of a
	// raw FK violation deep in the tightening step.
	if reasons, err := preflightForeignKeyOrphans(ctx, tx); err != nil {
		return result, err
	} else if len(reasons) > 0 {
		result.BlockedReasons = append(result.BlockedReasons, reasons...)
		return result, fmt.Errorf("%w: %s", ErrNeedsReview, strings.Join(reasons, "; "))
	}

	// 7. Group snapshots and settings for legacy active/paused runtimes.
	snapshots, settings, err := backfillGroupSnapshots(ctx, tx, revisions, revisionVersions, options, nowText)
	if err != nil {
		return result, err
	}
	result.GroupSnapshotsCreated = snapshots
	result.GroupSettingsBackfilled = settings

	// 8. Legacy review notification (dedupe key makes it one-shot).
	notifications, err := insertLegacyReviewNotifications(ctx, tx, options, nowText, reviewDeadline)
	if err != nil {
		return result, err
	}
	result.LegacyNotifications = notifications

	// 9. Validate the NOT VALID constraints emitted by the expand phase.
	validated, err := validateConstraints(ctx, tx)
	if err != nil {
		return result, err
	}
	result.ConstraintsValidated = validated

	// 10. Tighten foreign keys to RESTRICT.
	tightened, err := tightenForeignKeys(ctx, tx)
	if err != nil {
		return result, err
	}
	result.ForeignKeysTightened = tightened

	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func loadAccountRevisions(ctx context.Context, tx *sql.Tx, secret string) (map[string]string, map[string]int, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT resource_id FROM juhe_business.resource_authorization_grants
		WHERE resource_type = 'account'
		UNION
		SELECT resource_id FROM juhe_business.resource_authorizations WHERE resource_type = 'account'`)
	if err != nil {
		return nil, nil, nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	revisions := make(map[string]string, len(ids))
	versions := make(map[string]int, len(ids))
	missing := []string{}
	for _, id := range ids {
		revision, version, _, err := authzrevision.Compute(ctx, tx, id, secret)
		if errors.Is(err, sql.ErrNoRows) {
			missing = append(missing, id)
			continue
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("计算账户 %s 来源修订失败: %w", id, err)
		}
		revisions[id] = revision
		versions[id] = version
	}
	return revisions, versions, missing, nil
}

type legacySourceRow struct {
	id              string
	authorizationID string
	grantID         string
	grantStatus     string
	sourceType      string
	sourceTeamID    string
	status          string
	endedReason     sql.NullString
	endedAt         sql.NullString
	updatedAt       string
	createdAt       string
}

func migrateSourceStatuses(ctx context.Context, tx *sql.Tx) (int, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.authorization_id, COALESCE(s.grant_id, ''), COALESCE(g.status, ''),
			s.source_type, COALESCE(s.source_team_id, ''), s.status, s.ended_reason, s.ended_at, s.updated_at, s.created_at
		FROM juhe_business.resource_authorization_sources s
		LEFT JOIN juhe_business.resource_authorization_grants g ON g.id = s.grant_id
		ORDER BY s.id`)
	if err != nil {
		return 0, nil, err
	}
	var list []legacySourceRow
	for rows.Next() {
		var item legacySourceRow
		if err := rows.Scan(&item.id, &item.authorizationID, &item.grantID, &item.grantStatus,
			&item.sourceType, &item.sourceTeamID, &item.status, &item.endedReason, &item.endedAt,
			&item.updatedAt, &item.createdAt); err != nil {
			rows.Close()
			return 0, nil, err
		}
		list = append(list, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}

	// Preflight the post-migration live-key uniqueness before any UPDATE. The
	// expand phase creates the open-status unique indexes before this data step;
	// mapping superseded rows to dormant/active can therefore collide with an
	// already live source of the same runtime (old active-only indexes allowed
	// that combination). Detect it here and block with row ids instead of
	// failing halfway through the status mapping with a raw duplicate-key error.
	targets := make([]string, len(list))
	blocked := []string{}
	for index, item := range list {
		nextStatus, _, _, known := legacySourceTargetStatus(item.sourceType, item.status, item.endedReason.String)
		if !known {
			blocked = append(blocked, fmt.Sprintf("source %s superseded/ended_reason=%q 无法判定", item.id, item.endedReason.String))
			continue
		}
		targets[index] = nextStatus
	}
	if len(blocked) == 0 {
		blocked = append(blocked, detectLiveSourceCollisions(list, targets)...)
	}
	if len(blocked) > 0 {
		return 0, blocked, nil
	}

	migrated := 0
	for _, item := range list {
		nextStatus, endedReason, clearEnded, _ := legacySourceTargetStatus(item.sourceType, item.status, item.endedReason.String)
		if nextStatus == item.status && !clearEnded {
			continue
		}
		expiredFrom := ""
		endedAt := item.endedAt
		if nextStatus == "expired" {
			expiredFrom = legacyExpiredFromStatus(item.status)
			if !endedAt.Valid || endedAt.String == "" {
				endedAt = sql.NullString{String: fallbackTimestamp(item.updatedAt, item.createdAt), Valid: true}
			}
		}
		if isTerminalSourceStatus(nextStatus) && (!endedAt.Valid || endedAt.String == "") {
			endedAt = sql.NullString{String: fallbackTimestamp(item.updatedAt, item.createdAt), Valid: true}
		}
		var endedArg any
		var reasonArg any
		var revokedAtArg any
		if clearEnded {
			endedArg = nil
			reasonArg = nil
			revokedAtArg = nil
		} else {
			endedArg = nullString(endedAt)
			reasonArg = nullString(sql.NullString{String: endedReason, Valid: endedReason != ""})
			revokedAtArg = nullString(endedAt)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_sources
			SET status = $1,
				ended_at = $2,
				ended_reason = $3,
				revoked_at = CASE WHEN $1 IN ('active','dormant') THEN NULL
					ELSE CASE WHEN $4::text IS NULL THEN revoked_at ELSE $4 END END,
				revoked_by = CASE WHEN $1 IN ('active','dormant') THEN NULL ELSE revoked_by END,
				expired_from_status = CASE WHEN $1 = 'expired' THEN NULLIF($5, '') ELSE expired_from_status END,
				updated_at = updated_at
			WHERE id = $6 AND status = $7`, nextStatus, endedArg, reasonArg, revokedAtArg, expiredFrom, item.id, item.status); err != nil {
			return migrated, blocked, err
		}
		migrated++
	}
	return migrated, blocked, nil
}

// legacySourceTargetStatus maps one legacy source row to its revision-28
// status. known=false means an unclassifiable superseded row must block the
// migration for review.
func legacySourceTargetStatus(sourceType, status, endedReason string) (nextStatus, nextEndedReason string, clearEnded, known bool) {
	switch {
	case status == "superseded" && endedReason == "covered_by_team" && sourceType == "manual":
		return "dormant", "", true, true
	case status == "superseded" && endedReason == "covered_by_team" && sourceType == "team":
		return "active", "", true, true
	case status == "superseded" && endedReason == "authorization_expired":
		return "expired", "authorization_expired", false, true
	case status == "superseded" && endedReason == "authorization_returned":
		return "returned", "authorization_returned", false, true
	case status == "superseded" && endedReason == "authorization_revoked":
		return "revoked", "authorization_revoked", false, true
	case status == "revoked" && endedReason == "grantee_returned":
		return "returned", "grantee_returned", false, true
	case status == "revoked" && (endedReason == "account_deleted" || endedReason == "team_revoked"):
		return "revoked", endedReason, false, true
	case status == "superseded":
		return "", "", false, false
	default:
		return status, endedReason, false, true
	}
}

// detectLiveSourceCollisions reports source rows that would share the new
// open-status unique key after the status mapping. The callers use the same
// structural key as the expand-phase indexes: authorization_id for manual
// sources and authorization_id + source_team_id for team sources.
func detectLiveSourceCollisions(list []legacySourceRow, targets []string) []string {
	keyIDs := map[string][]string{}
	keyDisplays := map[string]string{}
	for index, item := range list {
		if !isLiveSourceStatus(targets[index]) {
			continue
		}
		key := item.authorizationID
		display := "authorization=" + item.authorizationID + ", source_type=" + item.sourceType
		if item.sourceType == "team" {
			key += "\x00" + item.sourceTeamID
			display += ", source_team_id=" + item.sourceTeamID
		}
		keyIDs[key] = append(keyIDs[key], item.id)
		keyDisplays[key] = display
	}
	blocked := []string{}
	for key, ids := range keyIDs {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		blocked = append(blocked, fmt.Sprintf(
			"source 非终态键冲突（%s）: %s", keyDisplays[key], strings.Join(ids, ",")))
	}
	sort.Strings(blocked)
	return blocked
}

// migrateTerminalParentSources maps live legacy sources whose parent grant is
// already terminal. It must run after backfillGrantIDs: revision 28 adds
// source.grant_id, so before that step the LEFT JOIN cannot see the parent.
func migrateTerminalParentSources(ctx context.Context, tx *sql.Tx) (int, error) {
	type source struct {
		id          string
		status      string
		grantStatus string
		endedAt     sql.NullString
		endedReason sql.NullString
		updatedAt   string
		createdAt   string
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.status, g.status, s.ended_at, s.ended_reason,
			s.updated_at, s.created_at
		FROM juhe_business.resource_authorization_sources s
		JOIN juhe_business.resource_authorization_grants g ON g.id = s.grant_id
		WHERE g.status IN ('expired','returned','revoked')
		  AND s.status IN ('pending','active','dormant','needs_review')
		ORDER BY s.id`)
	if err != nil {
		return 0, err
	}
	var list []source
	for rows.Next() {
		var item source
		if err := rows.Scan(&item.id, &item.status, &item.grantStatus, &item.endedAt,
			&item.endedReason, &item.updatedAt, &item.createdAt); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	migrated := 0
	for _, item := range list {
		nextStatus := item.grantStatus
		endedReason := item.endedReason.String
		if endedReason == "" {
			switch nextStatus {
			case "expired":
				endedReason = "authorization_expired"
			case "returned":
				endedReason = "authorization_returned"
			case "revoked":
				endedReason = "authorization_revoked"
			}
		}
		endedAt := item.endedAt
		if !endedAt.Valid || endedAt.String == "" {
			endedAt = sql.NullString{String: fallbackTimestamp(item.updatedAt, item.createdAt), Valid: true}
		}
		expiredFrom := ""
		if nextStatus == "expired" {
			expiredFrom = legacyExpiredFromStatus(item.status)
		}
		// Expired legacy sources must not keep the migration-computed accepted
		// revision: extend restores only expired-cycle sources and re-checks the
		// current material before reactivating them.
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_sources
			SET status = $1,
				ended_at = COALESCE(ended_at, $2),
				ended_reason = COALESCE(NULLIF(ended_reason, ''), $3),
				accepted_source_revision = CASE WHEN $1 = 'expired' THEN NULL ELSE accepted_source_revision END,
				accepted_source_revision_version = CASE WHEN $1 = 'expired' THEN NULL ELSE accepted_source_revision_version END,
				expired_from_status = CASE WHEN $1 = 'expired' THEN NULLIF($4, '') ELSE expired_from_status END,
				updated_at = updated_at
			WHERE id = $5 AND status = $6`, nextStatus, nullString(endedAt), endedReason,
			expiredFrom, item.id, item.status); err != nil {
			return migrated, err
		}
		migrated++
	}
	return migrated, nil
}

func legacyExpiredFromStatus(status string) string {
	switch status {
	case "active":
		return "active"
	case "dormant":
		return "dormant"
	case "pending":
		return "pending"
	case "needs_review":
		return "needs_review"
	default:
		return "active"
	}
}

func isLiveSourceStatus(status string) bool {
	return status == "active" || status == "superseded" || status == "pending" || status == "dormant" || status == "needs_review"
}

func isTerminalSourceStatus(status string) bool {
	return status == "revoked" || status == "returned" || status == "declined" || status == "expired"
}

func fallbackTimestamp(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func nullString(value sql.NullString) any {
	if !value.Valid || value.String == "" {
		return nil
	}
	return value.String
}

// backfillGrantIDs maps every source to its parent grant. Ambiguous rows are
// reported instead of guessed.
func backfillGrantIDs(ctx context.Context, tx *sql.Tx) (int, []string, error) {
	type source struct {
		id            string
		authorization string
		sourceType    string
		teamID        sql.NullString
		createdAt     string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, authorization_id, source_type, source_team_id, created_at
		FROM juhe_business.resource_authorization_sources
		WHERE grant_id IS NULL ORDER BY id`)
	if err != nil {
		return 0, nil, err
	}
	var sources []source
	for rows.Next() {
		var item source
		if err := rows.Scan(&item.id, &item.authorization, &item.sourceType, &item.teamID, &item.createdAt); err != nil {
			rows.Close()
			return 0, nil, err
		}
		sources = append(sources, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}

	mapped := 0
	orphans := []string{}
	for _, item := range sources {
		candidates, err := sourceGrantCandidates(ctx, tx, item.authorization, item.sourceType, item.teamID.String)
		if err != nil {
			return mapped, orphans, err
		}
		switch len(candidates) {
		case 0:
			orphans = append(orphans, item.id)
			continue
		case 1:
		default:
			// Prefer exactly one candidate whose lifecycle contains the
			// source creation instant. This is the only deterministic
			// tie-break allowed by the design.
			filtered := make([]string, 0, len(candidates))
			for _, id := range candidates {
				ok, err := grantLifecycleContains(ctx, tx, id, item.createdAt)
				if err != nil {
					return mapped, orphans, err
				}
				if ok {
					filtered = append(filtered, id)
				}
			}
			if len(filtered) != 1 {
				orphans = append(orphans, fmt.Sprintf("%s(candidates=%d)", item.id, len(candidates)))
				continue
			}
			candidates = filtered
		}
		result, err := tx.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_sources
			SET grant_id = $1 WHERE id = $2 AND grant_id IS NULL`, candidates[0], item.id)
		if err != nil {
			return mapped, orphans, err
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			mapped++
		}
	}
	return mapped, orphans, nil
}

func sourceGrantCandidates(ctx context.Context, tx *sql.Tx, authorizationID, sourceType, teamID string) ([]string, error) {
	var resourceType, resourceID, granteeID string
	err := tx.QueryRowContext(ctx, `SELECT resource_type, resource_id, grantee_system_account_id
		FROM juhe_business.resource_authorizations WHERE id = $1`, authorizationID).
		Scan(&resourceType, &resourceID, &granteeID)
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if sourceType == "team" {
		rows, err = tx.QueryContext(ctx, `SELECT id FROM juhe_business.resource_authorization_grants
			WHERE resource_type = $1 AND resource_id = $2 AND grantee_type = 'team' AND grantee_team_id = $3
			ORDER BY created_at, id`, resourceType, resourceID, teamID)
	} else {
		rows, err = tx.QueryContext(ctx, `SELECT id FROM juhe_business.resource_authorization_grants
			WHERE resource_type = $1 AND resource_id = $2 AND grantee_type = 'system_account' AND grantee_system_account_id = $3
			ORDER BY created_at, id`, resourceType, resourceID, granteeID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func grantLifecycleContains(ctx context.Context, tx *sql.Tx, grantID, instant string) (bool, error) {
	var createdAt string
	var endedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT created_at, COALESCE(ended_at, revoked_at)
		FROM juhe_business.resource_authorization_grants WHERE id = $1`, grantID).Scan(&createdAt, &endedAt); err != nil {
		return false, err
	}
	if instant < createdAt {
		return false, nil
	}
	if endedAt.Valid && endedAt.String != "" && instant > endedAt.String {
		return false, nil
	}
	return true, nil
}

func backfillGrantsAndSources(ctx context.Context, tx *sql.Tx, revisions map[string]string,
	versions map[string]int, options Options, nowText, reviewDeadline string) (int, int, int, error) {
	grantMarked := 0
	sourceRevisionCount := 0
	pendingCount := 0

	type grant struct {
		id           string
		resourceType string
		resourceID   string
		ownerID      string
		granteeType  string
		status       string
		createdAt    string
		revokedAt    sql.NullString
		updatedAt    string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_type, status, created_at, revoked_at, updated_at
		FROM juhe_business.resource_authorization_grants ORDER BY id`)
	if err != nil {
		return 0, 0, 0, err
	}
	var grants []grant
	for rows.Next() {
		var item grant
		if err := rows.Scan(&item.id, &item.resourceType, &item.resourceID, &item.ownerID,
			&item.granteeType, &item.status, &item.createdAt, &item.revokedAt, &item.updatedAt); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		grants = append(grants, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}

	for _, item := range grants {
		baseline := groupBaseline(item.resourceType, item.resourceID, item.ownerID, options.Secret)
		if item.resourceType == "account" {
			if revision, ok := revisions[item.resourceID]; ok {
				baseline = revision
			}
		}
		terminal := item.status == "expired" || item.status == "revoked" || item.status == "returned" || item.status == "declined"
		endedAt := item.revokedAt
		if terminal && (!endedAt.Valid || endedAt.String == "") {
			endedAt = sql.NullString{String: fallbackTimestamp(item.updatedAt, item.createdAt), Valid: true}
		}
		endedReason := sql.NullString{}
		if terminal {
			switch item.status {
			case "expired":
				endedReason = sql.NullString{String: "authorization_expired", Valid: true}
			case "returned":
				endedReason = sql.NullString{String: "authorization_returned", Valid: true}
			case "revoked":
				endedReason = sql.NullString{String: "authorization_revoked", Valid: true}
			}
		}
		expiredFrom := sql.NullString{}
		if item.status == "expired" {
			expiredFrom = sql.NullString{String: "active", Valid: true}
		}
		pendingExpires := sql.NullString{}
		switch item.status {
		case "pending_owner_approval":
			pendingExpires = sql.NullString{String: options.Now.Add(options.OwnerApprovalTTL).Format(time.RFC3339Nano), Valid: true}
		case "pending":
			pendingExpires = sql.NullString{String: options.Now.Add(options.PendingOfferTTL).Format(time.RFC3339Nano), Valid: true}
		case "pending_team_approval":
			pendingExpires = sql.NullString{String: options.Now.Add(options.TeamApprovalTTL).Format(time.RFC3339Nano), Valid: true}
		case "pending_member_acceptance":
			var sourceCount int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_sources
				WHERE grant_id = $1 AND status IN ('pending','active','dormant','needs_review')`, item.id).Scan(&sourceCount); err != nil {
				return 0, 0, 0, err
			}
			if sourceCount == 0 {
				pendingExpires = sql.NullString{String: options.Now.Add(options.TeamMemberlessTTL).Format(time.RFC3339Nano), Valid: true}
			}
		}
		if pendingExpires.Valid {
			pendingCount++
		}
		teamApprovedAt := sql.NullString{}
		acceptedAt := sql.NullString{}
		if item.granteeType == "team" {
			teamApprovedAt = sql.NullString{String: item.createdAt, Valid: true}
		}
		if item.status == "active" || item.status == "paused" || item.status == "expired" {
			acceptedAt = sql.NullString{String: item.createdAt, Valid: true}
		}
		legacyBasis := "migration"
		if item.status == "expired" {
			legacyBasis = "migration;legacy_expiry_unknown"
		}
		deadline := sql.NullString{}
		if acceptedAt.Valid {
			deadline = sql.NullString{String: reviewDeadline, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_grants SET
				accepted_at = COALESCE(accepted_at, $2),
				team_approved_at = COALESCE(team_approved_at, $3),
				source_revision = COALESCE(source_revision, $4),
				pending_expires_at = COALESCE(pending_expires_at, $5),
				expired_from_status = COALESCE(expired_from_status, $6),
				ended_at = COALESCE(ended_at, $7),
				ended_reason = COALESCE(ended_reason, $8),
				legacy_basis = COALESCE(legacy_basis, $9),
				legacy_review_deadline = COALESCE(legacy_review_deadline, $10)
			WHERE id = $1 AND acceptance_mode = 'legacy'`, item.id, nullString(acceptedAt), nullString(teamApprovedAt), nullString(sql.NullString{String: baseline, Valid: baseline != ""}),
			nullString(pendingExpires), nullString(expiredFrom), nullString(endedAt), nullString(endedReason), legacyBasis, nullString(deadline)); err != nil {
			return 0, 0, 0, err
		}
		grantMarked++
	}

	// Sources: accepted revisions, terminal fields, pending TTL and expiry
	// cycle defaults. Only live sources participate in accepted revision
	// backfill; expired terminal sources stay without accepted revision so
	// extend must restore them as needs_review.
	type sourceRow struct {
		id              string
		grantID         string
		authorizationID string
		sourceType      string
		status          string
		createdAt       string
		updatedAt       string
		endedAt         sql.NullString
		endedReason     sql.NullString
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, COALESCE(grant_id, ''), authorization_id, source_type, status,
			created_at, updated_at, ended_at, ended_reason
		FROM juhe_business.resource_authorization_sources ORDER BY id`)
	if err != nil {
		return grantMarked, 0, pendingCount, err
	}
	var sources []sourceRow
	for rows.Next() {
		var item sourceRow
		if err := rows.Scan(&item.id, &item.grantID, &item.authorizationID, &item.sourceType, &item.status,
			&item.createdAt, &item.updatedAt, &item.endedAt, &item.endedReason); err != nil {
			rows.Close()
			return grantMarked, 0, pendingCount, err
		}
		sources = append(sources, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return grantMarked, 0, pendingCount, err
	}

	for _, item := range sources {
		var resourceType, resourceID, ownerID string
		if err := tx.QueryRowContext(ctx, `SELECT resource_type, resource_id, resource_owner_system_account_id
			FROM juhe_business.resource_authorizations WHERE id = $1`, item.authorizationID).
			Scan(&resourceType, &resourceID, &ownerID); err != nil {
			return grantMarked, sourceRevisionCount, pendingCount, err
		}
		// superseded/covered_by_team is mapped to a live source by
		// migrateSourceStatuses (manual -> dormant, team -> active). The
		// accepted-revision CHECK applies on that UPDATE, so treat the target
		// status as live while backfilling; otherwise a real legacy upgrade
		// fails before the mapping step can run.
		effectiveStatus := item.status
		if item.status == "superseded" && item.endedReason.String == "covered_by_team" {
			switch item.sourceType {
			case "manual":
				effectiveStatus = "dormant"
			case "team":
				effectiveStatus = "active"
			}
		}
		acceptedRevision := sql.NullString{}
		acceptedVersion := sql.NullInt64{}
		observedRevision := sql.NullString{}
		revisionVersion := 1
		if effectiveStatus == "active" || effectiveStatus == "dormant" || effectiveStatus == "needs_review" || effectiveStatus == "pending" {
			if resourceType == "account" {
				if revision, ok := revisions[resourceID]; ok {
					acceptedRevision = sql.NullString{String: revision, Valid: true}
					acceptedVersion = sql.NullInt64{Int64: int64(versions[resourceID]), Valid: true}
					observedRevision = acceptedRevision
					revisionVersion = versions[resourceID]
				}
			} else {
				baseline := groupBaseline(resourceType, resourceID, ownerID, options.Secret)
				acceptedRevision = sql.NullString{String: baseline, Valid: true}
				acceptedVersion = sql.NullInt64{Int64: int64(authzrevision.Version), Valid: true}
				observedRevision = acceptedRevision
				revisionVersion = authzrevision.Version
			}
		}
		if effectiveStatus == "needs_review" {
			// Migration never creates needs_review for legacy rows except the
			// revision-upgrade step; preserve accepted revision if present.
			acceptedRevision = sql.NullString{}
			acceptedVersion = sql.NullInt64{}
		}
		endedAt := item.endedAt
		if isTerminalSourceStatus(effectiveStatus) && (!endedAt.Valid || endedAt.String == "") {
			endedAt = sql.NullString{String: fallbackTimestamp(item.updatedAt, item.createdAt), Valid: true}
		}
		pendingExpires := sql.NullString{}
		if effectiveStatus == "pending" {
			pendingExpires = sql.NullString{String: options.Now.Add(options.MemberOfferTTL).Format(time.RFC3339Nano), Valid: true}
			pendingCount++
		}
		acceptedAt := sql.NullString{}
		if acceptedRevision.Valid {
			acceptedAt = sql.NullString{String: item.createdAt, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_sources SET
				accepted_at = COALESCE(accepted_at, $2),
				accepted_source_revision = CASE
					WHEN $8 = 'needs_review' AND observed_source_revision IS NULL THEN NULL
					WHEN accepted_source_revision IS NULL AND observed_source_revision IS NULL THEN $3
					ELSE accepted_source_revision END,
				accepted_source_revision_version = CASE
					WHEN $8 = 'needs_review' AND observed_source_revision IS NULL THEN NULL
					WHEN accepted_source_revision IS NULL AND observed_source_revision IS NULL THEN $4
					ELSE accepted_source_revision_version END,
				observed_source_revision = COALESCE(observed_source_revision, $5),
				revision_version = CASE WHEN observed_source_revision IS NULL THEN $6 ELSE revision_version END,
				pending_expires_at = COALESCE(pending_expires_at, $7),
				expired_from_status = CASE WHEN $8 = 'expired' THEN COALESCE(expired_from_status, $9) ELSE expired_from_status END,
				ended_at = COALESCE(ended_at, $10)
			WHERE id = $1 AND acceptance_mode = 'legacy'`, item.id, nullString(acceptedAt), nullString(acceptedRevision),
			nullInt(acceptedVersion), nullString(observedRevision), revisionVersion,
			nullString(pendingExpires), effectiveStatus, legacyExpiredFromStatus(effectiveStatus), nullString(endedAt)); err != nil {
			return grantMarked, sourceRevisionCount, pendingCount, err
		}
		if acceptedRevision.Valid {
			sourceRevisionCount++
		}
	}

	return grantMarked, sourceRevisionCount, pendingCount, nil
}

func nullInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func groupBaseline(resourceType, resourceID, ownerID, secret string) string {
	if resourceType != "group" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("group\x00" + resourceID + "\x00" + ownerID))
	return hex.EncodeToString(mac.Sum(nil))
}

// revokeOrphanResources revokes non-terminal grants whose account/group has
// disappeared, preserving the historical rows for audit.
func revokeOrphanResources(ctx context.Context, tx *sql.Tx, nowText string) error {
	rows, err := tx.QueryContext(ctx, `SELECT g.id, g.resource_type, g.resource_id
		FROM juhe_business.resource_authorization_grants g
		WHERE g.status NOT IN ('expired','revoked','returned','declined')
		  AND (
			(g.resource_type = 'account' AND NOT EXISTS (SELECT 1 FROM juhe_business.accounts a WHERE a.id = g.resource_id AND a.deleted_at IS NULL))
			OR (g.resource_type = 'group' AND NOT EXISTS (SELECT 1 FROM juhe_business.groups gr WHERE gr.id = g.resource_id))
		)`)
	if err != nil {
		return err
	}
	type orphan struct{ id, resourceType, resourceID string }
	var items []orphan
	for rows.Next() {
		var item orphan
		if err := rows.Scan(&item.id, &item.resourceType, &item.resourceID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_grants
			SET status = 'revoked', ended_at = COALESCE(ended_at, $2), ended_reason = COALESCE(ended_reason, 'resource_deleted'), updated_at = $2
			WHERE id = $1 AND status NOT IN ('expired','revoked','returned','declined')`, item.id, nowText); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_sources
			SET status = 'revoked', ended_at = COALESCE(ended_at, $2), ended_reason = COALESCE(ended_reason, 'resource_deleted'), updated_at = $2
			WHERE grant_id = $1 AND status IN ('pending','active','dormant','needs_review','superseded')`, item.id, nowText); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.resource_authorizations ra
			SET status = 'revoked', status_reason = 'resource_deleted', status_changed_at = $1,
				effective_source_type = NULL, effective_source_team_id = NULL, updated_at = $1
			WHERE ra.resource_type = $2 AND ra.resource_id = $3
			  AND ra.status NOT IN ('expired','revoked','returned','declined')`,
			nowText, item.resourceType, item.resourceID); err != nil {
			return err
		}
		// Revoking the runtime alone leaves recipient-side references writable:
		// disable recipient group bindings, the group authorization switch and
		// provisioned instances so the gateway and UI cannot keep serving a
		// deleted resource even before the next reconciliation pass.
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.group_accounts
			SET enabled = 0, updated_at = $2
			WHERE account_authorization_id IN (
				SELECT id FROM juhe_business.resource_authorizations
				WHERE resource_type = $1 AND resource_id = $3)
			  AND enabled = 1`, item.resourceType, nowText, item.resourceID); err != nil {
			return err
		}
		if item.resourceType == "group" {
			if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.group_authorization_settings
				SET enabled = 0, updated_at = $2
				WHERE group_id = $1 AND enabled = 1`, item.resourceID, nowText); err != nil {
				return err
			}
		}
		if item.resourceType == "account" {
			if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.accounts
				SET status = 'disabled', schedulable = 0, updated_at = $2
				WHERE authorization_instance_source_account_id = $1 AND deleted_at IS NULL
				  AND status <> 'disabled'`, item.resourceID, nowText); err != nil {
				return err
			}
		}
	}
	return nil
}

// backfillGroupSnapshots builds the initial group account snapshot for every
// active/paused legacy group runtime and creates/fills settings rows.
func backfillGroupSnapshots(ctx context.Context, tx *sql.Tx, revisions map[string]string,
	versions map[string]int, options Options, nowText string) (int, int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT ra.id, ra.resource_id, ra.status, g.system_account_id, g.group_kind
		FROM juhe_business.resource_authorizations ra
		JOIN juhe_business.groups g ON g.id = ra.resource_id
		WHERE ra.resource_type = 'group' ORDER BY ra.id`)
	if err != nil {
		return 0, 0, err
	}
	type runtimeRow struct {
		id      string
		groupID string
		status  string
		ownerID string
		kind    string
	}
	var runtimes []runtimeRow
	for rows.Next() {
		var item runtimeRow
		if err := rows.Scan(&item.id, &item.groupID, &item.status, &item.ownerID, &item.kind); err != nil {
			rows.Close()
			return 0, 0, err
		}
		runtimes = append(runtimes, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	snapshotCount := 0
	settingsCount := 0
	for _, runtime := range runtimes {
		if runtime.kind != "standard" {
			return snapshotCount, settingsCount, fmt.Errorf("%w: group runtime %s 指向超级分组 %s", ErrNeedsReview, runtime.id, runtime.groupID)
		}
		if runtime.status == "active" || runtime.status == "paused" {
			accounts, err := groupSnapshotAccounts(ctx, tx, runtime.groupID, runtime.ownerID)
			if err != nil {
				return snapshotCount, settingsCount, err
			}
			snapshotMode := "normal"
			if len(accounts) > 500 {
				snapshotMode = "legacy_oversized"
			}
			parts := make([]string, 0, len(accounts))
			for _, accountID := range accounts {
				revision, ok := revisions[accountID]
				if !ok {
					revision, _, _, err = authzrevision.Compute(ctx, tx, accountID, options.Secret)
					if err != nil {
						return snapshotCount, settingsCount, fmt.Errorf("分组快照账户 %s 修订计算失败: %w", accountID, err)
					}
				}
				version := versions[accountID]
				if version == 0 {
					version = authzrevision.Version
				}
				parts = append(parts, accountID+"\x00"+revision+"\x00"+strconv.Itoa(version))
				result, err := tx.ExecContext(ctx, `INSERT INTO juhe_business.resource_authorization_group_accounts
					(authorization_id, account_id, source_revision, revision_version, accepted_at, updated_at)
					VALUES ($1, $2, $3, $4, $5, $5)
					ON CONFLICT (authorization_id, account_id) DO NOTHING`,
					runtime.id, accountID, revision, version, nowText)
				if err != nil {
					return snapshotCount, settingsCount, err
				}
				if affected, _ := result.RowsAffected(); affected == 1 {
					snapshotCount++
				}
			}
			sort.Strings(parts)
			snapshotRevision := hashParts(options.Secret, parts)
			if _, execErr := tx.ExecContext(ctx, `INSERT INTO juhe_business.group_authorization_settings
				(authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json,
				 snapshot_revision, snapshot_mode, created_at, updated_at)
				SELECT ra.id, ra.grantee_system_account_id, ra.resource_id, 1, 'personal', NULL,
					$2, $3, $4, $4
				FROM juhe_business.resource_authorizations ra WHERE ra.id = $1
				ON CONFLICT (authorization_id) DO UPDATE SET
					snapshot_revision = CASE WHEN juhe_business.group_authorization_settings.snapshot_revision IS NULL THEN EXCLUDED.snapshot_revision ELSE juhe_business.group_authorization_settings.snapshot_revision END,
					snapshot_mode = CASE WHEN juhe_business.group_authorization_settings.snapshot_revision IS NULL THEN EXCLUDED.snapshot_mode ELSE juhe_business.group_authorization_settings.snapshot_mode END,
					updated_at = CASE WHEN juhe_business.group_authorization_settings.snapshot_revision IS NULL THEN EXCLUDED.updated_at ELSE juhe_business.group_authorization_settings.updated_at END`,
				runtime.id, snapshotRevision, snapshotMode, nowText); execErr != nil {
				return snapshotCount, settingsCount, execErr
			}
			settingsCount++
		} else {
			// Terminal runtime: fail closed. Missing rows are also acceptable
			// (gateway treats absence as 0); explicit rows are forced to 0.
			if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.group_authorization_settings
				SET enabled = 0, updated_at = $2 WHERE authorization_id = $1`, runtime.id, nowText); err != nil {
				return snapshotCount, settingsCount, err
			}
		}
	}
	return snapshotCount, settingsCount, nil
}

func groupSnapshotAccounts(ctx context.Context, tx *sql.Tx, groupID, ownerID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT ga.account_id
		FROM juhe_business.group_accounts ga
		JOIN juhe_business.accounts a ON a.id = ga.account_id
		WHERE ga.group_id = $1 AND ga.enabled = 1
		  AND ga.system_account_id = $2
		  AND a.system_account_id = $2
		  AND a.deleted_at IS NULL
		  AND a.authorization_instance_authorization_id IS NULL
		ORDER BY ga.account_id`, groupID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func hashParts(secret string, parts []string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	for _, part := range parts {
		mac.Write([]byte(part))
		mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func insertLegacyReviewNotifications(ctx context.Context, tx *sql.Tx, options Options, nowText, reviewDeadline string) (int, error) {
	// Direct grants notify the grantee runtime; team grants notify every active
	// member's runtime row (team_cascade provisions one runtime per member).
	rows, err := tx.QueryContext(ctx, `SELECT ra.id, ra.grantee_system_account_id,
			g.id, s.id
		FROM juhe_business.resource_authorization_grants g
		JOIN juhe_business.resource_authorization_sources s ON s.grant_id = g.id
			AND s.status IN ('active','dormant')
		JOIN juhe_business.resource_authorizations ra ON ra.id = s.authorization_id
		WHERE ra.status = 'active' AND g.acceptance_mode = 'legacy'
		  AND (
			(g.grantee_type = 'system_account'
			  AND g.grantee_system_account_id = ra.grantee_system_account_id)
			OR (g.grantee_type IN ('team','team_member')
			  AND s.source_type = 'team' AND s.source_team_id = g.grantee_team_id
			  AND EXISTS (
				SELECT 1 FROM juhe_business.system_team_members m
				WHERE m.team_id = g.grantee_team_id
				  AND m.system_account_id = ra.grantee_system_account_id
				  AND m.status = 'active'))
		  )
		ORDER BY ra.id, s.id`)
	if err != nil {
		return 0, err
	}
	type notification struct {
		runtimeID string
		grantee   string
		grantID   string
		sourceID  string
	}
	var items []notification
	for rows.Next() {
		var item notification
		if err := rows.Scan(&item.runtimeID, &item.grantee, &item.grantID, &item.sourceID); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	inserted := 0
	for _, item := range items {
		payload, _ := json.Marshal(map[string]any{
			"message":        "你收到的授权已进入新治理模型，请在截止前复核并重新确认或归还/屏蔽",
			"reviewDeadline": reviewDeadline,
		})
		result, err := tx.ExecContext(ctx, `INSERT INTO juhe_business.authorization_notifications
			(id, system_account_id, type, grant_id, source_id, payload_json, dedupe_key, read_at, created_at)
			VALUES ($1, $2, 'legacy_review_required', $3, $4, $5, $6, NULL, $7)
			ON CONFLICT (dedupe_key) DO NOTHING`,
			"authnotif_"+hashParts(options.Secret, []string{"legacy", item.sourceID, item.grantee, nowText})[:32],
			item.grantee, item.grantID, item.sourceID, string(payload),
			"legacy-review:"+item.sourceID+":"+item.grantee, nowText)
		if err != nil {
			return inserted, err
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			inserted++
		}
	}
	return inserted, nil
}

var constraintNames = []string{
	"resource_authorization_grants_status_check",
	"resource_authorization_grants_acceptance_mode_check",
	"resource_authorization_grants_pending_expiry_check",
	"resource_authorization_grants_ended_at_check",
	"resource_authorizations_status_check",
	"resource_authorization_sources_status_check",
	"resource_authorization_sources_type_check",
	"resource_authorization_sources_dormant_check",
	"resource_authorization_sources_accepted_revision_check",
	"resource_authorization_sources_pending_expiry_check",
	"resource_authorization_sources_ended_at_check",
	"group_authorization_settings_snapshot_mode_check",
}

func validateConstraints(ctx context.Context, tx *sql.Tx) ([]string, error) {
	validated := []string{}
	for _, name := range constraintNames {
		table := constraintTable(name)
		var convalidated bool
		err := tx.QueryRowContext(ctx, `SELECT c.convalidated
			FROM pg_constraint c
			JOIN pg_class r ON r.oid = c.conrelid
			JOIN pg_namespace n ON n.oid = r.relnamespace
			WHERE n.nspname = 'juhe_business' AND r.relname = $1 AND c.conname = $2`, table, name).Scan(&convalidated)
		if errors.Is(err, sql.ErrNoRows) {
			return validated, fmt.Errorf("缺少约束 %s", name)
		}
		if err != nil {
			return validated, err
		}
		if convalidated {
			validated = append(validated, name)
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE juhe_business.%s VALIDATE CONSTRAINT %s`, table, name)); err != nil {
			return validated, fmt.Errorf("校验约束 %s 失败: %w", name, err)
		}
		validated = append(validated, name)
	}
	return validated, nil
}

func constraintTable(name string) string {
	switch {
	case strings.HasPrefix(name, "resource_authorization_grants_"):
		return "resource_authorization_grants"
	case strings.HasPrefix(name, "resource_authorizations_"):
		return "resource_authorizations"
	case strings.HasPrefix(name, "resource_authorization_sources_"):
		return "resource_authorization_sources"
	case strings.HasPrefix(name, "group_authorization_settings_"):
		return "group_authorization_settings"
	default:
		return ""
	}
}

// preflightForeignKeyOrphans reports rows that would make the RESTRICT foreign
// key validation fail. It is read-only: the caller blocks for review instead of
// guessing whether an orphaned grantee/team should be revoked.
func preflightForeignKeyOrphans(ctx context.Context, tx *sql.Tx) ([]string, error) {
	checks := []struct {
		label string
		query string
	}{
		{
			label: "resource_authorizations.grantee_system_account_id",
			query: `SELECT ra.id || ' (grantee=' || ra.grantee_system_account_id || ')'
				FROM juhe_business.resource_authorizations ra
				WHERE COALESCE(ra.grantee_system_account_id, '') <> ''
				  AND NOT EXISTS (SELECT 1 FROM juhe_business.system_accounts a WHERE a.id = ra.grantee_system_account_id)
				ORDER BY ra.id LIMIT 50`,
		},
		{
			label: "resource_authorization_grants.grantee_system_account_id",
			query: `SELECT g.id || ' (grantee=' || g.grantee_system_account_id || ')'
				FROM juhe_business.resource_authorization_grants g
				WHERE COALESCE(g.grantee_system_account_id, '') <> ''
				  AND NOT EXISTS (SELECT 1 FROM juhe_business.system_accounts a WHERE a.id = g.grantee_system_account_id)
				ORDER BY g.id LIMIT 50`,
		},
		{
			label: "resource_authorization_grants.grantee_team_id",
			query: `SELECT g.id || ' (team=' || g.grantee_team_id || ')'
				FROM juhe_business.resource_authorization_grants g
				WHERE COALESCE(g.grantee_team_id, '') <> ''
				  AND NOT EXISTS (SELECT 1 FROM juhe_business.system_teams t WHERE t.id = g.grantee_team_id)
				ORDER BY g.id LIMIT 50`,
		},
		{
			label: "resource_authorization_sources.source_team_id",
			query: `SELECT s.id || ' (team=' || s.source_team_id || ')'
				FROM juhe_business.resource_authorization_sources s
				WHERE COALESCE(s.source_team_id, '') <> ''
				  AND NOT EXISTS (SELECT 1 FROM juhe_business.system_teams t WHERE t.id = s.source_team_id)
				ORDER BY s.id LIMIT 50`,
		},
		{
			label: "resource_authorization_sources.authorization_id",
			query: `SELECT s.id || ' (authorization=' || s.authorization_id || ')'
				FROM juhe_business.resource_authorization_sources s
				WHERE NOT EXISTS (SELECT 1 FROM juhe_business.resource_authorizations ra WHERE ra.id = s.authorization_id)
				ORDER BY s.id LIMIT 50`,
		},
		{
			label: "resource_authorization_sources.grant_id",
			query: `SELECT s.id || ' (grant=' || s.grant_id || ')'
				FROM juhe_business.resource_authorization_sources s
				WHERE COALESCE(s.grant_id, '') <> ''
				  AND NOT EXISTS (SELECT 1 FROM juhe_business.resource_authorization_grants g WHERE g.id = s.grant_id)
				ORDER BY s.id LIMIT 50`,
		},
	}
	reasons := []string{}
	for _, check := range checks {
		rows, err := tx.QueryContext(ctx, check.query)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			reasons = append(reasons, fmt.Sprintf("%s 存在孤儿行: %s", check.label, strings.Join(ids, ",")))
		}
	}
	return reasons, nil
}

func tightenForeignKeys(ctx context.Context, tx *sql.Tx) ([]string, error) {
	type fk struct {
		table      string
		name       string
		columns    string
		references string
	}
	list := []fk{
		{"resource_authorizations", "resource_authorizations_grantee_system_account_id_fkey", "grantee_system_account_id", "juhe_business.system_accounts(id)"},
		{"resource_authorization_grants", "resource_authorization_grants_grantee_system_account_id_fkey", "grantee_system_account_id", "juhe_business.system_accounts(id)"},
		{"resource_authorization_grants", "resource_authorization_grants_grantee_team_id_fkey", "grantee_team_id", "juhe_business.system_teams(id)"},
		{"resource_authorization_sources", "resource_authorization_sources_source_team_id_fkey", "source_team_id", "juhe_business.system_teams(id)"},
		{"resource_authorization_sources", "resource_authorization_sources_authorization_id_fkey", "authorization_id", "juhe_business.resource_authorizations(id)"},
		{"resource_authorization_sources", "resource_authorization_sources_grant_id_fkey", "grant_id", "juhe_business.resource_authorization_grants(id)"},
	}
	done := []string{}
	for _, item := range list {
		// Drop the old CASCADE FK if it exists, then add a NOT VALID
		// RESTRICT FK and validate it. Drop is idempotent.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE juhe_business.%s DROP CONSTRAINT IF EXISTS %s`, item.table, item.name)); err != nil {
			return done, err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_constraint c
			JOIN pg_class r ON r.oid = c.conrelid
			JOIN pg_namespace n ON n.oid = r.relnamespace
			WHERE n.nspname = 'juhe_business' AND r.relname = $1 AND c.conname = $2)`,
			item.table, item.name).Scan(&exists); err != nil {
			return done, err
		}
		if !exists {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE juhe_business.%s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s ON DELETE RESTRICT NOT VALID`,
				item.table, item.name, item.columns, item.references)); err != nil {
				return done, fmt.Errorf("收紧外键 %s 失败: %w", item.name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE juhe_business.%s VALIDATE CONSTRAINT %s`, item.table, item.name)); err != nil {
			return done, fmt.Errorf("校验外键 %s 失败（可能存在孤儿行）: %w", item.name, err)
		}
		done = append(done, item.name)
	}
	return done, nil
}

// Satisfied is the idempotent completion predicate used by the upgrade
// pipeline. It returns true only when the data migration and constraint/FK
// tightening have converged.
func Satisfied(ctx context.Context, db *sql.DB, secret string) (bool, error) {
	var pending int
	checks := []string{
		`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE grant_id IS NULL`,
		`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE status = 'superseded'`,
		`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE status IN ('active','dormant') AND (accepted_source_revision IS NULL OR accepted_source_revision_version IS NULL)`,
		`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE status IN ('revoked','returned','declined','expired') AND ended_at IS NULL`,
		`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE status = 'pending' AND pending_expires_at IS NULL`,
		`SELECT count(*) FROM juhe_business.resource_authorization_grants WHERE status IN ('expired','revoked','returned','declined') AND ended_at IS NULL`,
		`SELECT count(*) FROM juhe_business.resource_authorization_grants WHERE status IN ('pending_owner_approval','pending','pending_team_approval') AND pending_expires_at IS NULL`,
		`SELECT count(*) FROM juhe_business.resource_authorizations ra
			WHERE ra.resource_type = 'group' AND ra.status IN ('active','paused')
			  AND NOT EXISTS (
				SELECT 1 FROM juhe_business.group_authorization_settings s
				WHERE s.authorization_id = ra.id AND COALESCE(s.snapshot_revision, '') <> ''
			  )`,
		`SELECT count(*) FROM juhe_business.resource_authorization_grants g
			WHERE g.acceptance_mode = 'legacy' AND g.status IN ('active','paused') AND g.legacy_review_deadline IS NULL`,
	}
	for _, query := range checks {
		if err := db.QueryRowContext(ctx, query).Scan(&pending); err != nil {
			if isUndefinedRelation(err) {
				// Fresh/legacy databases reach Satisfied before the upgrade
				// pipeline runs the additive schema phase. Missing revision 28
				// tables mean "not migrated yet", not a failed evaluation.
				return false, nil
			}
			return false, err
		}
		if pending > 0 {
			return false, nil
		}
	}
	// All constraints must be validated.
	for _, name := range constraintNames {
		var validated bool
		err := db.QueryRowContext(ctx, `SELECT c.convalidated
			FROM pg_constraint c
			JOIN pg_class r ON r.oid = c.conrelid
			JOIN pg_namespace n ON n.oid = r.relnamespace
			WHERE n.nspname = 'juhe_business' AND r.relname = $1 AND c.conname = $2`,
			constraintTable(name), name).Scan(&validated)
		if err != nil {
			// A missing constraint row means the expand phase has not created
			// it yet; treat it like a missing relation instead of failing the
			// upgrade plan evaluation.
			if errors.Is(err, sql.ErrNoRows) || isUndefinedRelation(err) {
				return false, nil
			}
			return false, err
		}
		if !validated {
			return false, nil
		}
	}
	// FK definitions must be RESTRICT.
	var restrictive int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint c
		JOIN pg_class r ON r.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = r.relnamespace
		WHERE n.nspname = 'juhe_business'
		  AND c.conname IN (
			'resource_authorizations_grantee_system_account_id_fkey',
			'resource_authorization_grants_grantee_system_account_id_fkey',
			'resource_authorization_grants_grantee_team_id_fkey',
			'resource_authorization_sources_source_team_id_fkey',
			'resource_authorization_sources_authorization_id_fkey',
			'resource_authorization_sources_grant_id_fkey')
		  AND c.confdeltype = 'r'`).Scan(&restrictive); err != nil {
		return false, err
	}
	return restrictive == 6, nil
}

// Verify reports the first unmet condition with an actionable message.
func Verify(ctx context.Context, db *sql.DB, secret string) error {
	ok, err := Satisfied(ctx, db, secret)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	reasons := []struct {
		query string
		label string
	}{
		{`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE grant_id IS NULL`, "source.grant_id 未回填"},
		{`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE status = 'superseded'`, "source.superseded 未迁移"},
		{`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE status IN ('active','dormant') AND (accepted_source_revision IS NULL OR accepted_source_revision_version IS NULL)`, "active/dormant source 缺少 accepted revision"},
		{`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE status IN ('revoked','returned','declined','expired') AND ended_at IS NULL`, "终态 source 缺少 ended_at"},
		{`SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE status = 'pending' AND pending_expires_at IS NULL`, "pending source 缺少 pending_expires_at"},
		{`SELECT count(*) FROM juhe_business.resource_authorization_grants WHERE status IN ('expired','revoked','returned','declined') AND ended_at IS NULL`, "终态 grant 缺少 ended_at"},
		{`SELECT count(*) FROM juhe_business.resource_authorization_grants WHERE status IN ('pending_owner_approval','pending','pending_team_approval') AND pending_expires_at IS NULL`, "pending grant 缺少 pending_expires_at"},
		{`SELECT count(*) FROM juhe_business.resource_authorizations ra
			WHERE ra.resource_type = 'group' AND ra.status IN ('active','paused')
			  AND NOT EXISTS (
				SELECT 1 FROM juhe_business.group_authorization_settings s
				WHERE s.authorization_id = ra.id AND COALESCE(s.snapshot_revision, '') <> ''
			  )`, "active/paused 分组 runtime 缺少快照 settings/snapshot_revision"},
		{`SELECT count(*) FROM juhe_business.resource_authorization_grants g
			WHERE g.acceptance_mode = 'legacy' AND g.status IN ('active','paused') AND g.legacy_review_deadline IS NULL`, "legacy grant 缺少复核截止"},
	}
	for _, reason := range reasons {
		var count int
		if err := db.QueryRowContext(ctx, reason.query).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("revision 28 未完成：%s（%d 行）", reason.label, count)
		}
	}
	return errors.New("revision 28 完成态谓词未满足（约束或外键未收敛）")
}

func isUndefinedRelation(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "does not exist") || strings.Contains(message, "不存在")
}

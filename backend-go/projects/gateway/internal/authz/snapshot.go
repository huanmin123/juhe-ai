// Group authorization scope snapshots, explicit binding and per-account
// reconfirmation (design §4.6/§5.10).
package authz

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

// ScopeAccount is one account in the current group scope or snapshot.
type ScopeAccount struct {
	AccountID       string `json:"accountId"`
	AccountName     string `json:"accountName,omitempty"`
	SourceRevision  string `json:"sourceRevision"`
	RevisionVersion int    `json:"revisionVersion"`
	AcceptedAt      string `json:"acceptedAt,omitempty"`
}

// ScopeDiff is the receiver-visible range review payload.
type ScopeDiff struct {
	SourceID         string         `json:"sourceId"`
	GroupID          string         `json:"groupId"`
	SnapshotRevision string         `json:"snapshotRevision,omitempty"`
	SnapshotMissing  bool           `json:"snapshotMissing"`
	SnapshotMode     string         `json:"snapshotMode,omitempty"`
	Enabled          bool           `json:"enabled"`
	CurrentCount     int            `json:"currentCount"`
	SnapshotCount    int            `json:"snapshotCount"`
	Added            []ScopeAccount `json:"added"`
	Removed          []ScopeAccount `json:"removed"`
	RevisionChanged  []ScopeAccount `json:"revisionChanged"`
}

// ScopeReview is the receiver's explicit review of one added account.
type ScopeReview struct {
	AccountID       string `json:"accountId"`
	SourceRevision  string `json:"sourceRevision"`
	RevisionVersion int    `json:"revisionVersion"`
}

// RefreshScopeInput is the explicit range refresh request.
type RefreshScopeInput struct {
	ExpectedSnapshotRevision string        `json:"expectedSnapshotRevision"`
	ReviewedAdded            []ScopeReview `json:"reviewedAdded"`
	RebuildMissingSnapshot   bool          `json:"rebuildMissingSnapshot"`
}

// GroupSnapshotService functions are methods on Store (defined here for
// discoverability).

func snapshotHash(secret string, accounts []ScopeAccount) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("授权快照密钥未配置")
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].AccountID < accounts[j].AccountID })
	mac := hmac.New(sha256.New, []byte(secret))
	for _, account := range accounts {
		mac.Write([]byte(account.AccountID))
		mac.Write([]byte{0})
		mac.Write([]byte(account.SourceRevision))
		mac.Write([]byte{0})
		mac.Write([]byte(itoa(account.RevisionVersion)))
		mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Store) loadSnapshot(ctx context.Context, q revisionQueryer, runtimeID string) ([]ScopeAccount, error) {
	rows, err := q.QueryContext(ctx, `SELECT account_id, source_revision, revision_version, accepted_at
		FROM juhe_business.resource_authorization_group_accounts
		WHERE authorization_id = $1 ORDER BY account_id`, runtimeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScopeAccount{}
	for rows.Next() {
		var item ScopeAccount
		if err := rows.Scan(&item.AccountID, &item.SourceRevision, &item.RevisionVersion, &item.AcceptedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) loadCurrentScope(ctx context.Context, q revisionQueryer, groupID, ownerID string) ([]ScopeAccount, error) {
	rows, err := q.QueryContext(ctx, `SELECT ga.account_id, a.name
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
	var ids []string
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		names[id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ScopeAccount, 0, len(ids))
	for _, id := range ids {
		revision, version, err := s.accountRevision(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, ScopeAccount{AccountID: id, AccountName: names[id], SourceRevision: revision, RevisionVersion: version})
	}
	return out, nil
}

func (s *Store) settingsRow(ctx context.Context, q revisionQueryer, runtimeID string, forUpdate bool) (enabled bool, exists bool, snapshotRevision string, snapshotMode string, err error) {
	query := `SELECT enabled, COALESCE(snapshot_revision, ''), COALESCE(snapshot_mode, 'normal')
		FROM juhe_business.group_authorization_settings WHERE authorization_id = $1`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var enabledInt int
	err = q.QueryRowContext(ctx, query, runtimeID).Scan(&enabledInt, &snapshotRevision, &snapshotMode)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, "", "", nil
	}
	if err != nil {
		return false, false, "", "", err
	}
	return enabledInt == 1, true, snapshotRevision, snapshotMode, nil
}

// ensureGroupSnapshotForAccept builds the first snapshot when the runtime has
// no live group source. Existing snapshots are preserved untouched.
func (s *Store) ensureGroupSnapshotForAccept(ctx context.Context, tx *sql.Tx, runtime *runtimeRecord, currentRevision string, currentVersion int, now string) error {
	live, err := s.hasLiveGroupSource(ctx, tx, runtime.ID)
	if err != nil {
		return err
	}
	snapshot, err := s.loadSnapshot(ctx, tx, runtime.ID)
	if err != nil {
		return err
	}
	enabled, exists, existingRevision, existingMode, err := s.settingsRow(ctx, tx, runtime.ID, false)
	if err != nil {
		return err
	}
	emptyRevision, err := snapshotHash(s.revisionSecret, nil)
	if err != nil {
		return err
	}
	if len(snapshot) > 0 {
		hash, err := snapshotHash(s.revisionSecret, snapshot)
		if err != nil {
			return err
		}
		mode := existingMode
		if mode == "" {
			mode = "normal"
		}
		if !exists {
			_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_authorization_settings")+`
				(authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json,
				 snapshot_revision, snapshot_mode, created_at, updated_at)
				VALUES (?, ?, ?, 0, 'personal', NULL, ?, ?, ?, ?)`),
				runtime.ID, runtime.GranteeID, runtime.ResourceID, hash, mode, now, now)
			return err
		}
		if existingRevision == "" {
			_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_authorization_settings")+`
				SET snapshot_revision = ?, snapshot_mode = ?, updated_at = ? WHERE authorization_id = ?`),
				hash, mode, now, runtime.ID)
			return err
		}
		_ = enabled
		return nil
	}
	// A canonical empty snapshot is represented by the empty-set HMAC in
	// settings, not by snapshot rows. Without this check a group that was empty
	// when the first source was accepted could never accept a second source:
	// hasLiveGroupSource is true and len(snapshot)==0 would be misread as a
	// missing snapshot.
	if exists && existingRevision != "" {
		if existingRevision == emptyRevision {
			return nil
		}
		return failCode("snapshot_missing_requires_review", "分组授权快照缺失，请先查看范围差异并重建")
	}
	if live {
		return failCode("snapshot_missing_requires_review", "分组授权快照缺失，请先查看范围差异并重建")
	}
	current, err := s.loadCurrentScope(ctx, tx, runtime.ResourceID, runtime.OwnerID)
	if err != nil {
		return err
	}
	if len(current) > MaxGroupSnapshotAccounts {
		return failCode("group_snapshot_too_large", "分组账户数超过授权快照上限")
	}
	mode := "normal"
	hash, err := snapshotHash(s.revisionSecret, current)
	if err != nil {
		return err
	}
	for _, account := range current {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_group_accounts")+`
			(authorization_id, account_id, source_revision, revision_version, accepted_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (authorization_id, account_id) DO NOTHING`),
			runtime.ID, account.AccountID, account.SourceRevision, account.RevisionVersion, now, now); err != nil {
			return err
		}
	}
	if exists {
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_authorization_settings")+`
			SET snapshot_revision = ?, snapshot_mode = ?, updated_at = ? WHERE authorization_id = ?`),
			hash, mode, now, runtime.ID)
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_authorization_settings")+`
		(authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json,
		 snapshot_revision, snapshot_mode, created_at, updated_at)
		VALUES (?, ?, ?, 0, 'personal', NULL, ?, ?, ?, ?)`),
		runtime.ID, runtime.GranteeID, runtime.ResourceID, hash, mode, now, now)
	return err
}

func (s *Store) hasLiveGroupSource(ctx context.Context, q revisionQueryer, runtimeID string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_sources
		WHERE authorization_id = $1 AND source_type IN ('manual','team')
		  AND status IN ('active','dormant','needs_review')`, runtimeID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ScopeDiff returns the current-vs-snapshot range diff for the receiver.
func (s *Store) ScopeDiff(ctx context.Context, systemAccountID, sourceID string) (*ScopeDiff, error) {
	source, runtime, group, err := s.loadGroupSourceContext(ctx, s.db, sourceID)
	if err != nil {
		return nil, err
	}
	if source == nil || runtime == nil || runtime.GranteeID != systemAccountID {
		return nil, failf("授权来源不存在")
	}
	if runtime.OwnerID == "" || group == nil || group.OwnerID != runtime.OwnerID {
		return nil, failCode("resource_changed", "授权资源归属已变化，请重新授权")
	}
	if runtime.Status != StatusActive || (source.Status != StatusActive && source.Status != StatusDormant) {
		return nil, failCode("source_not_bindable", "授权来源当前不可用")
	}
	enabled, exists, snapshotRevision, mode, err := s.settingsRow(ctx, s.db, runtime.ID, false)
	if err != nil {
		return nil, err
	}
	if !exists {
		enabled = false
	}
	snapshot, err := s.loadSnapshot(ctx, s.db, runtime.ID)
	if err != nil {
		return nil, err
	}
	current, err := s.loadCurrentScope(ctx, s.db, group.ID, group.OwnerID)
	if err != nil {
		return nil, err
	}
	emptyRevision, err := snapshotHash(s.revisionSecret, nil)
	if err != nil {
		return nil, err
	}
	snapshotMissing := !exists || snapshotRevision == "" ||
		(len(snapshot) == 0 && snapshotRevision != emptyRevision)
	diff := &ScopeDiff{
		SourceID: source.ID, GroupID: group.ID, Enabled: enabled,
		SnapshotRevision: snapshotRevision, SnapshotMode: mode,
		SnapshotMissing: snapshotMissing,
		CurrentCount:    len(current), SnapshotCount: len(snapshot),
		Added: []ScopeAccount{}, Removed: []ScopeAccount{}, RevisionChanged: []ScopeAccount{},
	}
	snapshotByID := map[string]ScopeAccount{}
	for _, item := range snapshot {
		snapshotByID[item.AccountID] = item
	}
	currentByID := map[string]ScopeAccount{}
	for _, item := range current {
		currentByID[item.AccountID] = item
		if old, ok := snapshotByID[item.AccountID]; !ok {
			diff.Added = append(diff.Added, item)
		} else if old.SourceRevision != item.SourceRevision || old.RevisionVersion != item.RevisionVersion {
			changed := item
			changed.AcceptedAt = old.AcceptedAt
			diff.RevisionChanged = append(diff.RevisionChanged, changed)
		}
	}
	for _, item := range snapshot {
		if _, ok := currentByID[item.AccountID]; !ok {
			diff.Removed = append(diff.Removed, item)
		}
	}
	return diff, nil
}

type groupContext struct {
	ID      string
	OwnerID string
	Kind    string
}

func (s *Store) loadGroupSourceContext(ctx context.Context, q revisionQueryer, sourceID string) (*sourceRecord, *runtimeRecord, *groupContext, error) {
	var source sourceRecord
	var runtime runtimeRecord
	var group groupContext
	err := q.QueryRowContext(ctx, `SELECT s.id, s.authorization_id, s.source_type, COALESCE(s.source_team_id, ''), s.status,
			ra.id, ra.resource_type, ra.resource_id, ra.resource_owner_system_account_id, ra.grantee_system_account_id,
			ra.status, COALESCE(ra.effective_source_type, ''), COALESCE(ra.effective_source_team_id, ''),
			COALESCE(ra.expires_at, ''), COALESCE(ra.limits_json, ''), COALESCE(ra.acceptance_mode, 'legacy'),
			ra.created_at, ra.updated_at,
			g.id, g.system_account_id, g.group_kind
		FROM juhe_business.resource_authorization_sources s
		JOIN juhe_business.resource_authorizations ra ON ra.id = s.authorization_id
		JOIN juhe_business.groups g ON g.id = ra.resource_id
		WHERE s.id = $1 AND ra.resource_type = 'group'`, sourceID).
		Scan(&source.ID, &source.AuthorizationID, &source.SourceType, &source.SourceTeamID, &source.Status,
			&runtime.ID, &runtime.ResourceType, &runtime.ResourceID, &runtime.OwnerID, &runtime.GranteeID,
			&runtime.Status, &runtime.EffectiveType, &runtime.EffectiveTeamID,
			&runtime.ExpiresAt, &runtime.LimitsJSON, &runtime.AcceptanceMode, &runtime.CreatedAt, &runtime.UpdatedAt,
			&group.ID, &group.OwnerID, &group.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return &source, &runtime, &group, nil
}

// RefreshScope applies only added/removed accounts and the explicit missing
// snapshot rebuild. Existing revision changes must use ReconfirmScopeAccount.
func (s *Store) RefreshScope(ctx context.Context, systemAccountID, sourceID string, input RefreshScopeInput, now string) (*ScopeDiff, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Lock order: runtime → settings (source is read-only here).
	source, runtime, group, err := s.loadGroupSourceContext(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	if source == nil || runtime == nil || runtime.GranteeID != systemAccountID {
		return nil, failf("授权来源不存在")
	}
	if runtime.OwnerID == "" || group == nil || group.OwnerID != runtime.OwnerID {
		return nil, failCode("resource_changed", "授权资源归属已变化，请重新授权")
	}
	if runtime.Status != StatusActive || (source.Status != StatusActive && source.Status != StatusDormant) {
		return nil, failCode("source_not_bindable", "授权来源当前不可用")
	}
	if group.Kind != "standard" {
		return nil, failCode("group_kind_conflict", "超级分组不能作为授权资源")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorizations")+` WHERE id = ? FOR UPDATE`), runtime.ID); err != nil {
		return nil, err
	}
	settingsQuery := `SELECT COALESCE(snapshot_revision, ''), COALESCE(snapshot_mode, 'normal'), enabled
		FROM ` + s.table("group_authorization_settings") + ` WHERE authorization_id = ?`
	var snapshotRevision, mode string
	var enabledInt int
	err = tx.QueryRowContext(ctx, s.bind(settingsQuery+" FOR UPDATE"), runtime.ID).Scan(&snapshotRevision, &mode, &enabledInt)
	if errors.Is(err, sql.ErrNoRows) {
		if !input.RebuildMissingSnapshot {
			return nil, failCode("snapshot_missing_requires_review", "分组授权快照缺失，请先重建")
		}
		snapshotRevision, mode, enabledInt = "", "normal", 0
	} else if err != nil {
		return nil, err
	}
	snapshot, err := s.loadSnapshot(ctx, tx, runtime.ID)
	if err != nil {
		return nil, err
	}
	current, err := s.loadCurrentScope(ctx, tx, group.ID, group.OwnerID)
	if err != nil {
		return nil, err
	}
	emptyRevision, err := snapshotHash(s.revisionSecret, nil)
	if err != nil {
		return nil, err
	}
	canonicalEmpty := len(snapshot) == 0 && snapshotRevision != "" && snapshotRevision == emptyRevision
	if len(snapshot) == 0 && !canonicalEmpty {
		if !input.RebuildMissingSnapshot {
			return nil, failCode("snapshot_missing_requires_review", "分组授权快照缺失，请先重建")
		}
		if snapshotRevision != "" && input.ExpectedSnapshotRevision != snapshotRevision {
			return nil, failCode("scope_stale", "分组授权范围已变化，请刷新后重新审阅")
		}
		if len(current) > MaxGroupSnapshotAccounts {
			return nil, failCode("group_snapshot_too_large", "分组账户数超过授权快照上限")
		}
		reviewed := map[string]ScopeReview{}
		for _, item := range input.ReviewedAdded {
			reviewed[item.AccountID] = item
		}
		for _, account := range current {
			review, ok := reviewed[account.AccountID]
			if !ok || review.SourceRevision != account.SourceRevision ||
				review.RevisionVersion != account.RevisionVersion {
				return nil, failCode("scope_stale", "新增账户未按当前修订审阅，请刷新后重新选择")
			}
		}
		hash, err := snapshotHash(s.revisionSecret, current)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("resource_authorization_group_accounts")+`
			WHERE authorization_id = ?`), runtime.ID); err != nil {
			return nil, err
		}
		for _, account := range current {
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_group_accounts")+`
				(authorization_id, account_id, source_revision, revision_version, accepted_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`), runtime.ID, account.AccountID, account.SourceRevision,
				account.RevisionVersion, now, now); err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_authorization_settings")+`
			(authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json,
			 snapshot_revision, snapshot_mode, created_at, updated_at)
			VALUES (?, ?, ?, 0, 'personal', NULL, ?, ?, ?, ?)
			ON CONFLICT (authorization_id) DO UPDATE SET enabled = 0, snapshot_revision = EXCLUDED.snapshot_revision,
				snapshot_mode = EXCLUDED.snapshot_mode, updated_at = EXCLUDED.updated_at`),
			runtime.ID, runtime.GranteeID, runtime.ResourceID, hash, "normal", now, now); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		s.invalidateGovernance(ctx, invalidationReasonUpdated)
		return s.ScopeDiff(ctx, systemAccountID, sourceID)
	}
	if snapshotRevision == "" {
		return nil, failCode("scope_stale", "快照修订缺失，请刷新范围差异")
	}
	if input.ExpectedSnapshotRevision != snapshotRevision {
		return nil, failCode("scope_stale", "分组授权范围已变化，请刷新后重新审阅")
	}
	if mode == "legacy_oversized" && len(current) > len(snapshot) {
		return nil, failCode("group_snapshot_too_large", "存量超限快照只能减少或保持账户数")
	}
	snapshotByID := map[string]ScopeAccount{}
	for _, item := range snapshot {
		snapshotByID[item.AccountID] = item
	}
	currentByID := map[string]ScopeAccount{}
	for _, item := range current {
		currentByID[item.AccountID] = item
	}
	reviewed := map[string]ScopeReview{}
	for _, item := range input.ReviewedAdded {
		reviewed[item.AccountID] = item
	}
	// Removed accounts: delete snapshot rows.
	for _, item := range snapshot {
		if _, ok := currentByID[item.AccountID]; !ok {
			if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("resource_authorization_group_accounts")+`
				WHERE authorization_id = ? AND account_id = ?`), runtime.ID, item.AccountID); err != nil {
				return nil, err
			}
		}
	}
	// Added accounts: require explicit review and exact revision/version.
	for _, item := range current {
		if _, ok := snapshotByID[item.AccountID]; ok {
			continue
		}
		review, ok := reviewed[item.AccountID]
		if !ok || review.SourceRevision != item.SourceRevision || review.RevisionVersion != item.RevisionVersion {
			return nil, failCode("scope_stale", "新增账户未按当前修订审阅，请刷新后重新选择")
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_group_accounts")+`
			(authorization_id, account_id, source_revision, revision_version, accepted_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`), runtime.ID, item.AccountID, item.SourceRevision, item.RevisionVersion, now, now); err != nil {
			return nil, err
		}
	}
	nextSnapshot, err := s.loadSnapshot(ctx, tx, runtime.ID)
	if err != nil {
		return nil, err
	}
	if len(nextSnapshot) > MaxGroupSnapshotAccounts && mode != "legacy_oversized" {
		return nil, failCode("group_snapshot_too_large", "分组账户数超过授权快照上限")
	}
	hash, err := snapshotHash(s.revisionSecret, nextSnapshot)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_authorization_settings")+`
		SET snapshot_revision = ?, updated_at = ? WHERE authorization_id = ?`), hash, now, runtime.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return s.ScopeDiff(ctx, systemAccountID, sourceID)
}

// ReconfirmScopeAccount accepts one account's new revision and recomputes the
// snapshot hash under the settings row lock.
func (s *Store) ReconfirmScopeAccount(ctx context.Context, systemAccountID, sourceID, accountID, expectedRevision string, expectedVersion int, now string) (*ScopeDiff, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, runtime, group, err := s.loadGroupSourceContext(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	if source == nil || runtime == nil || runtime.GranteeID != systemAccountID {
		return nil, failf("授权来源不存在")
	}
	if runtime.OwnerID == "" || group == nil || group.OwnerID != runtime.OwnerID {
		return nil, failCode("resource_changed", "授权资源归属已变化，请重新授权")
	}
	if runtime.Status != StatusActive || (source.Status != StatusActive && source.Status != StatusDormant) {
		return nil, failCode("source_not_bindable", "授权来源当前不可用")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorizations")+` WHERE id = ? FOR UPDATE`), runtime.ID); err != nil {
		return nil, err
	}
	var currentSnapshotRevision string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COALESCE(snapshot_revision, '') FROM `+s.table("group_authorization_settings")+`
		WHERE authorization_id = ? FOR UPDATE`), runtime.ID).Scan(&currentSnapshotRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, failCode("snapshot_missing_requires_review", "分组授权快照缺失，请先重建")
		}
		return nil, err
	}
	currentRevision, currentVersion, err := s.accountRevision(ctx, tx, accountID)
	if err != nil {
		return nil, err
	}
	if expectedRevision != currentRevision || expectedVersion != currentVersion {
		return nil, failCode("scope_stale", "账户来源已变化，请刷新后重新审阅")
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_group_accounts")+`
		SET source_revision = ?, revision_version = ?, accepted_at = ?, updated_at = ?
		WHERE authorization_id = ? AND account_id = ?`), currentRevision, currentVersion, now, now, runtime.ID, accountID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, failCode("scope_stale", "账户不在已接受的分组快照中")
	}
	snapshot, err := s.loadSnapshot(ctx, tx, runtime.ID)
	if err != nil {
		return nil, err
	}
	hash, err := snapshotHash(s.revisionSecret, snapshot)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_authorization_settings")+`
		SET snapshot_revision = ?, updated_at = ? WHERE authorization_id = ?`), hash, now, runtime.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return s.ScopeDiff(ctx, systemAccountID, sourceID)
}

// BindGroupSource enables the runtime-level group authorization switch.
func (s *Store) BindGroupSource(ctx context.Context, systemAccountID, sourceID string, enable bool, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	source, runtime, group, err := s.loadGroupSourceContext(ctx, tx, sourceID)
	if err != nil {
		return err
	}
	if source == nil || runtime == nil || runtime.GranteeID != systemAccountID {
		return failf("授权来源不存在")
	}
	if runtime.OwnerID == "" || group == nil || group.OwnerID != runtime.OwnerID {
		return failCode("resource_changed", "授权资源归属已变化，请重新授权")
	}
	// Unbinding is always allowed for an owned runtime: it only turns the
	// receiver-side switch off, so terminal/paused runtimes need no snapshot
	// precondition. Enabling still requires a live runtime + accepted source.
	if enable {
		if runtime.Status != StatusActive {
			return failCode("source_not_active", "授权运行时尚未生效")
		}
		if source.Status != StatusActive && source.Status != StatusDormant {
			return failCode("source_not_active", "授权来源尚未生效")
		}
	}
	if group.Kind != "standard" {
		return failCode("group_kind_conflict", "超级分组不能作为授权资源")
	}
	// The context above is an unlocked read. Re-validate under the design lock
	// order (runtime → settings → source) so a concurrent return/revoke that
	// committed while we waited cannot leave settings.enabled=1 on a terminal
	// runtime.
	var lockedRuntimeStatus string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("resource_authorizations")+`
		WHERE id = ? FOR UPDATE`), runtime.ID).Scan(&lockedRuntimeStatus); err != nil {
		return err
	}
	if enable && lockedRuntimeStatus != StatusActive {
		return failCode("source_not_active", "授权运行时尚未生效")
	}
	var lockedSourceStatus string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("resource_authorization_sources")+`
		WHERE id = ? AND authorization_id = ? FOR UPDATE`), source.ID, runtime.ID).Scan(&lockedSourceStatus); err != nil {
		return err
	}
	if enable && lockedSourceStatus != StatusActive && lockedSourceStatus != StatusDormant {
		return failCode("source_not_active", "授权来源尚未生效")
	}
	enabled, exists, snapshotRevision, snapshotMode, err := s.settingsRow(ctx, tx, runtime.ID, true)
	if err != nil {
		return err
	}
	if !exists {
		if !enable {
			return nil
		}
		return failCode("snapshot_missing_requires_review", "分组授权设置缺失，请先重建快照")
	}
	if snapshotRevision == "" {
		if !enable {
			return nil
		}
		return failCode("snapshot_missing_requires_review", "分组授权快照修订缺失")
	}
	_ = snapshotMode
	next := 0
	if enable {
		next = 1
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_authorization_settings")+`
		SET enabled = ?, updated_at = ? WHERE authorization_id = ?`), next, now, runtime.ID); err != nil {
		return err
	}
	_ = enabled
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

// RecomputeSnapshotRevisionForMigration is exported for the migration/verify
// tools and tests that need the canonical hash without writing.
func RecomputeSnapshotRevisionForMigration(secret string, accounts []ScopeAccount) (string, error) {
	return snapshotHash(secret, accounts)
}

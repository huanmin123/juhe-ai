package accounts

// M11 traffic migration: POST /{id}/traffic-migration (Node
// account-traffic-migration.routes.ts +
// account-runtime-mutation.repository.ts migrateAccountTrafficAsync /
// migrateAuthorizedAccountBindingTrafficAsync).
//
// The DB state machine (scope checks, the same-owner/provider/group guards,
// the source status CAS) lives here. The gateway runtime session handover
// (Node migrateServerOpenAIAccountTrafficRuntime over the db-service IPC)
// rides the narrow TrafficRuntimeMigrator port injected by the composition
// root; a nil port reports zero migrated sessions exactly like the IPC
// fallback ({ migratedSessionCount: 0 }).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TrafficMigrationSourceStatus mirrors accountTrafficMigrationSchema enum.
type TrafficMigrationSourceStatus string

const (
	trafficSourceTemporaryUnavailable TrafficMigrationSourceStatus = "temporary_unavailable"
	trafficSourceDisabled             TrafficMigrationSourceStatus = "disabled"
	trafficSourceUnchanged            TrafficMigrationSourceStatus = "unchanged"
)

const manualTrafficMigrationReason = "手动迁移流量"

// temporaryUnavailableInitialBackoffSeconds mirrors
// account-runtime-mutation-helpers.ts.
const temporaryUnavailableInitialBackoffSeconds = 3

// TrafficMigrationInput mirrors accountTrafficMigrationSchema.
type TrafficMigrationInput struct {
	TargetAccountID string
	SourceStatus    TrafficMigrationSourceStatus // empty → temporary_unavailable
}

// parseTrafficMigrationBody mirrors accountTrafficMigrationSchema.strict().
func parseTrafficMigrationBody(body map[string]any) (TrafficMigrationInput, string) {
	input := TrafficMigrationInput{}
	for key := range body {
		switch key {
		case "targetAccountId", "sourceStatus":
		default:
			return TrafficMigrationInput{}, "迁移流量参数无效"
		}
	}
	target, _ := body["targetAccountId"].(string)
	if strings.TrimSpace(target) == "" {
		return TrafficMigrationInput{}, "迁移流量参数无效"
	}
	input.TargetAccountID = strings.TrimSpace(target)
	if value, exists := body["sourceStatus"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return TrafficMigrationInput{}, "迁移流量参数无效"
		}
		switch TrafficMigrationSourceStatus(text) {
		case trafficSourceTemporaryUnavailable, trafficSourceDisabled, trafficSourceUnchanged:
			input.SourceStatus = TrafficMigrationSourceStatus(text)
		default:
			return TrafficMigrationInput{}, "迁移流量参数无效"
		}
	}
	return input, ""
}

// TrafficMigrationResult mirrors the sanitized route response
// (sanitizeAccountTrafficMigrationResponse).
type TrafficMigrationResult struct {
	SourceAccount       *ListItem `json:"sourceAccount"`
	TargetAccount       *ListItem `json:"targetAccount"`
	SourceCooldownUntil *string   `json:"sourceCooldownUntil,omitempty"`
	MigratedSessionCount int      `json:"migratedSessionCount"`
	SourceStatus        string    `json:"sourceStatus"`
	GroupID             *string   `json:"-"`
}

// TrafficRuntimeMigrator is the narrow port of the gateway runtime session
// migration (Node migrateServerOpenAIAccountTrafficRuntime).
type TrafficRuntimeMigrator interface {
	MigrateOpenAIAccountTrafficRuntime(ctx context.Context, input TrafficRuntimeMigrationInput) (int, error)
}

// TrafficRuntimeMigrationInput mirrors OpenAIAccountTrafficMigrationRuntimeRequest.
type TrafficRuntimeMigrationInput struct {
	SourceAccountID       string
	TargetAccountID       string
	PreferMigratedSessions bool
	AffinityScope         *TrafficMigrationScope
	PreferenceScope       *TrafficMigrationScope
}

// TrafficMigrationScope mirrors { systemAccountId, groupId }.
type TrafficMigrationScope struct {
	SystemAccountID string
	GroupID         string
}

// SetTrafficRuntimeMigrator wires the port (composition-root handover).
func (s *Store) SetTrafficRuntimeMigrator(migrator TrafficRuntimeMigrator) {
	s.trafficRuntimeMigrator = migrator
}

var errTrafficSameAccount = errors.New("目标账户不能和当前账户相同")

// trafficMigrationFailure marks the deterministic 400 copies thrown by the
// Node repository guards.
type trafficMigrationFailure struct{ message string }

func (e *trafficMigrationFailure) Error() string { return e.message }

// MigrateTraffic mirrors migrateAccountTrafficAsync: the owner branch first,
// the authorized-instance binding branch when the source row reads as an
// instance. Returns (nil, nil) when the source/target row is missing or out
// of scope (route 404 账户不存在或无权迁移).
func (s *Store) MigrateTraffic(ctx context.Context, sourceAccountID string, input TrafficMigrationInput, access AccessScope) (*TrafficMigrationResult, error) {
	ctx = ensureCtx(ctx)
	sourceID := strings.TrimSpace(sourceAccountID)
	if sourceID == "" {
		return nil, nil
	}
	// The instance branch keys off the full summary stamp
	// (findAccountSummaryAsync accessType), not the api-key projection.
	sourceSummary, err := s.findTrafficSummary(ctx, sourceID, access)
	if err != nil {
		return nil, err
	}
	if sourceSummary == nil {
		return nil, nil
	}
	if sourceSummary.AccessType == "authorized" {
		return s.migrateAuthorizedBindingTraffic(ctx, sourceID, input, access)
	}
	return s.migrateOwnerTraffic(ctx, sourceID, input, access)
}

// findTrafficSummary resolves one summary row through the management
// projection for the scope guard and the response payload.
func (s *Store) findTrafficSummary(ctx context.Context, accountID string, access AccessScope) (*ListItem, error) {
	result, err := s.ListPage(ctx, access, ListOptions{IDs: []string{accountID}, Page: 1, PageSize: 1})
	if err != nil {
		return nil, err
	}
	if len(result.Items) == 0 {
		return nil, nil
	}
	return &result.Items[0], nil
}

func (s *Store) migrateOwnerTraffic(ctx context.Context, sourceAccountID string, input TrafficMigrationInput, access AccessScope) (*TrafficMigrationResult, error) {
	if sourceAccountID == input.TargetAccountID {
		return nil, errTrafficSameAccount
	}
	// accountRowForManage for both rows: the managed (scope-checked) raw rows.
	sourceRow, err := s.trafficManagedRow(ctx, sourceAccountID, access)
	if err != nil {
		return nil, err
	}
	if sourceRow == nil {
		return nil, nil
	}
	targetRow, err := s.trafficManagedRow(ctx, input.TargetAccountID, access)
	if err != nil {
		return nil, err
	}
	if targetRow == nil {
		return nil, nil
	}
	if sourceRow.systemAccountID != targetRow.systemAccountID {
		return nil, &trafficMigrationFailure{message: "目标账户必须和当前账户归属同一个系统账户"}
	}
	if sourceRow.providerCode != targetRow.providerCode {
		return nil, &trafficMigrationFailure{message: "目标账户必须和当前账户属于同一个供应商"}
	}
	sourceGroupID, err := s.enabledGroupIDForAccount(ctx, sourceRow.id, sourceRow.systemAccountID)
	if err != nil {
		return nil, err
	}
	targetGroupID, err := s.enabledGroupIDForAccount(ctx, targetRow.id, targetRow.systemAccountID)
	if err != nil {
		return nil, err
	}
	if sourceGroupID == "" || targetGroupID == "" || sourceGroupID != targetGroupID {
		return nil, &trafficMigrationFailure{message: "目标账户必须和当前账户在同一个分组内"}
	}
	now := s.now()
	targetCooldown := strings.TrimSpace(targetRow.cooldownUntil.String)
	if targetRow.status != "active" || targetRow.schedulable != 1 ||
		isAccountExpired(targetRow.accountExpiresAt.String, now) ||
		isLaterInstant(targetCooldown, isoMillis(now)) {
		return nil, &trafficMigrationFailure{message: "目标账户当前不可调度，请选择正常可用的账户"}
	}
	ownerAccess := AccessScope{ViewerID: sourceRow.systemAccountID}
	if input.SourceStatus == trafficSourceUnchanged {
		sourceSummary, err := s.findTrafficSummary(ctx, sourceRow.id, ownerAccess)
		if err != nil {
			return nil, err
		}
		targetSummary, err := s.findTrafficSummary(ctx, targetRow.id, ownerAccess)
		if err != nil {
			return nil, err
		}
		if sourceSummary == nil || targetSummary == nil {
			return nil, nil
		}
		return &TrafficMigrationResult{
			SourceAccount: sourceSummary,
			TargetAccount: targetSummary,
			SourceStatus:  string(trafficSourceUnchanged),
			GroupID:       &sourceGroupID,
		}, nil
	}
	sourceStatus := input.SourceStatus
	if sourceStatus == "" {
		sourceStatus = trafficSourceTemporaryUnavailable
	}
	nowMS := now.UnixMilli()
	nowISO := isoMillis(now)
	sourceCooldownUntil := ""
	sourceObservationStartedAt := ""
	sourceCooldownGeneration := ""
	if sourceStatus == trafficSourceTemporaryUnavailable {
		sourceCooldownUntil = isoMillis(time.UnixMilli(nowMS + temporaryUnavailableInitialBackoffSeconds*1000))
		sourceObservationStartedAt = isoMillis(now)
		sourceCooldownGeneration = newCooldownGeneration()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exec sql.Result
	if sourceStatus == trafficSourceDisabled {
		exec, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
			SET status = 'disabled',
				schedulable = 0,
				cooldown_until = NULL,
				last_error_code = NULL,
				last_error_message = ?,
				last_error_trace_id = NULL,
				cooldown_retest_failure_count = 0,
				cooldown_retest_observation_started_at = NULL,
				cooldown_retest_generation = NULL,
				cooldown_retest_last_at = NULL,
				cooldown_retest_last_status_code = NULL,
				stream_failure_count = 0,
				stream_failure_window_started_at = NULL,
				updated_at = ?
			WHERE id = ?
				AND system_account_id = ?`),
			manualTrafficMigrationReason, nowISO, sourceRow.id, sourceRow.systemAccountID)
	} else {
		exec, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
			SET status = 'temporary_unavailable',
				cooldown_until = ?,
				last_error_code = NULL,
				last_error_message = ?,
				last_error_trace_id = NULL,
				cooldown_retest_failure_count = 0,
				cooldown_retest_observation_started_at = ?,
				cooldown_retest_generation = ?,
				cooldown_retest_last_at = NULL,
				cooldown_retest_last_status_code = NULL,
				stream_failure_count = 0,
				stream_failure_window_started_at = NULL,
				updated_at = ?
			WHERE id = ?
				AND system_account_id = ?`),
		 nullableStringPointer(sourceCooldownUntil), manualTrafficMigrationReason,
		 nullableStringPointer(sourceObservationStartedAt), nullableStringPointer(sourceCooldownGeneration),
		 nowISO, sourceRow.id, sourceRow.systemAccountID)
	}
	if err != nil {
		return nil, err
	}
	if affected, _ := exec.RowsAffected(); affected == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sourceSummary, err := s.findTrafficSummary(ctx, sourceRow.id, ownerAccess)
	if err != nil {
		return nil, err
	}
	targetSummary, err := s.findTrafficSummary(ctx, targetRow.id, ownerAccess)
	if err != nil {
		return nil, err
	}
	if sourceSummary == nil || targetSummary == nil {
		return nil, nil
	}
	result := &TrafficMigrationResult{
		SourceAccount: sourceSummary,
		TargetAccount: targetSummary,
		SourceStatus:  string(sourceStatus),
		GroupID:       &sourceGroupID,
	}
	if sourceCooldownUntil != "" {
		result.SourceCooldownUntil = &sourceCooldownUntil
	}
	return result, nil
}

func (s *Store) migrateAuthorizedBindingTraffic(ctx context.Context, sourceAccountID string, input TrafficMigrationInput, access AccessScope) (*TrafficMigrationResult, error) {
	if sourceAccountID == input.TargetAccountID {
		return nil, errTrafficSameAccount
	}
	grantee := access.viewerID()
	if grantee == "" {
		return nil, nil
	}
	granteeAccess := AccessScope{ViewerID: grantee}
	sourceSummary, err := s.findTrafficSummary(ctx, sourceAccountID, granteeAccess)
	if err != nil {
		return nil, err
	}
	targetSummary, err := s.findTrafficSummary(ctx, input.TargetAccountID, granteeAccess)
	if err != nil {
		return nil, err
	}
	if sourceSummary == nil || sourceSummary.AccessType != "authorized" {
		return nil, nil
	}
	if targetSummary == nil {
		return nil, nil
	}
	if sourceSummary.BoundGroupID == nil || sourceSummary.AccountAuthorizationID == nil ||
		targetSummary.BoundGroupID == nil || *sourceSummary.BoundGroupID != *targetSummary.BoundGroupID {
		return nil, &trafficMigrationFailure{message: "目标账户必须和当前账户在你的同一个分组内"}
	}
	if sourceSummary.ProviderCode != targetSummary.ProviderCode {
		return nil, &trafficMigrationFailure{message: "目标账户必须和当前账户属于同一个供应商"}
	}
	if message := trafficTargetUnavailableMessage(targetSummary); message != "" {
		return nil, &trafficMigrationFailure{message: message}
	}
	groupID := *sourceSummary.BoundGroupID
	if input.SourceStatus == trafficSourceUnchanged {
		return &TrafficMigrationResult{
			SourceAccount: sourceSummary,
			TargetAccount: targetSummary,
			SourceStatus:  string(trafficSourceUnchanged),
			GroupID:       &groupID,
		}, nil
	}
	sourceStatus := input.SourceStatus
	if sourceStatus == "" {
		sourceStatus = trafficSourceTemporaryUnavailable
	}
	now := s.now()
	nowISO := isoMillis(now)
	sourceCooldownUntil := ""
	sourceCooldownGeneration := ""
	observationStartedAt := ""
	if sourceStatus == trafficSourceTemporaryUnavailable {
		sourceCooldownUntil = isoMillis(time.UnixMilli(now.UnixMilli() + temporaryUnavailableInitialBackoffSeconds*1000))
		observationStartedAt = nowISO
		sourceCooldownGeneration = newCooldownGeneration()
	}
	nextStatus := "temporary_unavailable"
	nextSchedulable := 1
	if sourceStatus == trafficSourceDisabled {
		nextStatus = "disabled"
		nextSchedulable = 0
	}
	exec, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET status = ?,
			schedulable = ?,
			cooldown_until = ?,
			last_error_code = NULL,
			last_error_message = ?,
			last_error_trace_id = NULL,
			cooldown_retest_failure_count = 0,
			cooldown_retest_observation_started_at = ?,
			cooldown_retest_generation = ?,
			cooldown_retest_last_at = NULL,
			cooldown_retest_last_status_code = NULL,
			stream_failure_count = 0,
			stream_failure_window_started_at = NULL,
			updated_at = ?
		WHERE id = ?
			AND system_account_id = ?
			AND authorization_instance_authorization_id = ?
			AND deleted_at IS NULL
			AND EXISTS (
				SELECT 1
				FROM `+s.table("group_accounts")+` group_accounts
				WHERE group_accounts.account_id = accounts.id
					AND group_accounts.system_account_id = ?
					AND group_accounts.group_id = ?
					AND group_accounts.enabled = 1
					AND group_accounts.account_authorization_id = ?
			)`),
		nextStatus, nextSchedulable, nullableStringPointer(sourceCooldownUntil),
		manualTrafficMigrationReason, nilToEmpty(observationStartedAt), nilToEmpty(sourceCooldownGeneration),
		nowISO, sourceSummary.ID, grantee, *sourceSummary.AccountAuthorizationID,
		grantee, groupID, *sourceSummary.AccountAuthorizationID)
	if err != nil {
		return nil, err
	}
	if affected, _ := exec.RowsAffected(); affected == 0 {
		return nil, nil
	}
	nextSource, err := s.findTrafficSummary(ctx, sourceSummary.ID, granteeAccess)
	if err != nil {
		return nil, err
	}
	nextTarget, err := s.findTrafficSummary(ctx, targetSummary.ID, granteeAccess)
	if err != nil {
		return nil, err
	}
	if nextSource == nil || nextTarget == nil {
		return nil, nil
	}
	result := &TrafficMigrationResult{
		SourceAccount: nextSource,
		TargetAccount: nextTarget,
		SourceStatus:  string(sourceStatus),
		GroupID:       &groupID,
	}
	if sourceCooldownUntil != "" {
		result.SourceCooldownUntil = &sourceCooldownUntil
	}
	return result, nil
}

// trafficTargetUnavailableMessage mirrors accountDispatchUnavailableMessage
// for the migration target: the effective availability must be schedulable.
func trafficTargetUnavailableMessage(target *ListItem) string {
	if !target.EffectiveAvailability.Available {
		return "目标账户当前不可调度，请选择正常可用的账户"
	}
	return ""
}

// trafficManagedRow is the accountRowForManage projection for the owner
// branch (scope-checked raw row with the scheduling columns).
type trafficManagedRow struct {
	id               string
	systemAccountID  string
	providerCode     string
	status           string
	schedulable      int
	accountExpiresAt sql.NullString
	cooldownUntil    sql.NullString
}

func (s *Store) trafficManagedRow(ctx context.Context, accountID string, access AccessScope) (*trafficManagedRow, error) {
	authorized := s.authorizedReadableIDs(ctx, access)[accountID]
	scopeClause := ""
	args := []any{strings.TrimSpace(accountID)}
	if scoped := access.manageableID(); scoped != "" && !authorized {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row trafficManagedRow
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, provider_code, status,
			schedulable, account_expires_at, cooldown_until
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.systemAccountID, &row.providerCode, &row.status,
		&row.schedulable, &row.accountExpiresAt, &row.cooldownUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	return &row, nil
}

// enabledGroupIDForAccount mirrors accountEnabledGroupIdForClientAsync.
func (s *Store) enabledGroupIDForAccount(ctx context.Context, accountID, systemAccountID string) (string, error) {
	var groupID sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT group_id FROM `+s.table("group_accounts")+`
		WHERE account_id = ? AND system_account_id = ? AND enabled = 1
		ORDER BY updated_at DESC, group_id ASC
		LIMIT 1`), accountID, systemAccountID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return groupID.String, nil
}

// isLaterInstant mirrors isLaterIso: both instants parse as RFC3339 and left
// is strictly later than right.
func isLaterInstant(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	leftMS, leftOK := balanceSnapshotTimestampMs(left)
	rightMS, rightOK := balanceSnapshotTimestampMs(right)
	if !leftOK || !rightOK {
		return false
	}
	return leftMS > rightMS
}

func nilToEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// nullableStringPointer renders a blank text as a SQL NULL parameter.
func nullableStringPointer(value string) any {
	return nilToEmpty(value)
}

func newCooldownGeneration() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("cooldown:%x", buf)
}

// runtimeMigrationInput mirrors the route runtime handover assembly
// (runtimeMigrationInput in account-traffic-migration.routes.ts). The route
// normalizes sourceStatus to temporary_unavailable before the store call, so
// result.SourceStatus is always set.
func buildRuntimeMigrationInput(result *TrafficMigrationResult, input TrafficMigrationInput, access AccessScope) TrafficRuntimeMigrationInput {
	runtime := TrafficRuntimeMigrationInput{
		SourceAccountID: result.SourceAccount.ID,
		TargetAccountID: result.TargetAccount.ID,
	}
	if result.SourceStatus == string(trafficSourceUnchanged) {
		runtime.PreferMigratedSessions = true
	}
	summary := result.SourceAccount
	systemAccountID := access.viewerID()
	if summary.AccessType == "authorized" && summary.BoundGroupID != nil && systemAccountID != "" {
		runtime.AffinityScope = &TrafficMigrationScope{SystemAccountID: systemAccountID, GroupID: *summary.BoundGroupID}
	}
	if result.SourceStatus != string(trafficSourceUnchanged) {
		preferenceSystemID := systemAccountID
		if summary.AccessType != "authorized" {
			preferenceSystemID = summary.OwnerSystemAccountID
		}
		groupID := ""
		if result.GroupID != nil {
			groupID = *result.GroupID
		} else if summary.BoundGroupID != nil {
			groupID = *summary.BoundGroupID
		}
		if preferenceSystemID != "" && groupID != "" {
			runtime.PreferenceScope = &TrafficMigrationScope{SystemAccountID: preferenceSystemID, GroupID: groupID}
		}
	}
	return runtime
}

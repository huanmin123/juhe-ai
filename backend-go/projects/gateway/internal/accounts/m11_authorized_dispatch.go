package accounts

// M11 authorized-instance dispatch: PATCH /{id}/authorized-dispatch (Node
// account-authorized-dispatch.routes.ts +
// account-authorized-dispatch.repository.ts
// updateAuthorizedAccountBindingDispatchAsync).
//
// The state machine runs inside one transaction: the scope-checked instance
// row, the binding row, the expectedConfigRevision CAS, the pending_test
// guard, the dispatch availability guard, the account failure clear + the
// group_accounts local_* update, and the dispatch revision advance when the
// restore re-enables scheduling. The runtime restore handover reuses the
// runtime-reset port (Node clearServerAccountRuntimeAvailability).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// AuthorizedDispatchRevisionConflictError mirrors
// AuthorizedAccountDispatchRevisionConflictError (route 409).
type AuthorizedDispatchRevisionConflictError struct {
	AccountID              string
	ExpectedConfigRevision int64
}

func (e *AuthorizedDispatchRevisionConflictError) Error() string {
	return "授权账户配置已发生并发变更，请重试：" + e.AccountID
}

// AuthorizedDispatchInput mirrors authorizedAccountDispatchSchema.
type AuthorizedDispatchInput struct {
	ExpectedConfigRevision int64
	Status                 *string
	Priority               *int
	SuperPriorityEnabled   *bool
	FallbackEnabled        *bool
	ClearFailureState      bool
}

// parseAuthorizedDispatchBody mirrors authorizedAccountDispatchSchema.strict()
// plus superRefine: at least one change key, and a lone clearFailureState must
// be true.
func parseAuthorizedDispatchBody(body map[string]any) (AuthorizedDispatchInput, string) {
	input := AuthorizedDispatchInput{}
	for key := range body {
		switch key {
		case "expectedConfigRevision", "status", "priority", "superPriorityEnabled", "fallbackEnabled", "clearFailureState":
		default:
			return AuthorizedDispatchInput{}, "授权账户调度参数无效"
		}
	}
	revision, ok := body["expectedConfigRevision"].(float64)
	if !ok || revision != float64(int64(revision)) || revision < 1 {
		return AuthorizedDispatchInput{}, "授权账户调度参数无效"
	}
	input.ExpectedConfigRevision = int64(revision)
	if value, exists := body["status"]; exists && value != nil {
		text, ok := value.(string)
		if !ok || (text != "active" && text != "disabled") {
			return AuthorizedDispatchInput{}, "授权账户调度参数无效"
		}
		input.Status = &text
	}
	if value, exists := body["priority"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) || number < 0 {
			return AuthorizedDispatchInput{}, "授权账户调度参数无效"
		}
		priority := int(number)
		input.Priority = &priority
	}
	if value, exists := body["superPriorityEnabled"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return AuthorizedDispatchInput{}, "授权账户调度参数无效"
		}
		input.SuperPriorityEnabled = &enabled
	}
	if value, exists := body["fallbackEnabled"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return AuthorizedDispatchInput{}, "授权账户调度参数无效"
		}
		input.FallbackEnabled = &enabled
	}
	if value, exists := body["clearFailureState"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return AuthorizedDispatchInput{}, "授权账户调度参数无效"
		}
		input.ClearFailureState = enabled
	}
	changedKeys := 0
	for _, key := range []string{"status", "priority", "superPriorityEnabled", "fallbackEnabled", "clearFailureState"} {
		if _, exists := body[key]; exists {
			changedKeys++
		}
	}
	if changedKeys == 0 || (changedKeys == 1 && input.ClearFailureState != true && hasKey(body, "clearFailureState")) {
		return AuthorizedDispatchInput{}, "请至少提交一项授权账户调度变更"
	}
	return input, ""
}

func hasKey(body map[string]any, key string) bool {
	_, exists := body[key]
	return exists
}

// AuthorizedDispatchChange mirrors AuthorizedAccountDispatchChange.
type AuthorizedDispatchChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// AuthorizedDispatchPatch mirrors AuthorizedAccountDispatchMutationPatch.
type AuthorizedDispatchPatch struct {
	Status               *string `json:"status,omitempty"`
	Schedulable          *bool `json:"schedulable,omitempty"`
	Priority             *int  `json:"priority,omitempty"`
	SuperPriorityEnabled *bool `json:"superPriorityEnabled,omitempty"`
	FallbackEnabled      *bool `json:"fallbackEnabled,omitempty"`
	FailureStateCleared  bool  `json:"failureStateCleared,omitempty"`
}

// AuthorizedDispatchBinding mirrors the authorizedBinding triple.
type AuthorizedDispatchBinding struct {
	SystemAccountID        string
	GroupID                string
	AccountAuthorizationID string
}

// AuthorizedDispatchResult mirrors AuthorizedAccountDispatchMutationResult.
type AuthorizedDispatchResult struct {
	ID                     string                      `json:"id"`
	ConfigRevision         int64                       `json:"configRevision"`
	ChangedFields          []string                    `json:"changedFields"`
	Patch                  AuthorizedDispatchPatch     `json:"patch"`
	Changes                []AuthorizedDispatchChange  `json:"-"`
	Name                   string                      `json:"-"`
	OwnerSystemAccountID   string                      `json:"-"`
	RuntimeRestoreRequired bool                        `json:"-"`
	AuthorizedBinding      AuthorizedDispatchBinding   `json:"-"`
}

// authorizedDispatchRow is the instance row projection the state machine
// reads (loadAuthorizedAccountDispatchRowForUpdate columns).
type authorizedDispatchRow struct {
	id                                 string
	configRevision                     int64
	systemAccountID                    string
	name                               string
	status                             string
	schedulable                        int
	accountExpiresAt                   sql.NullString
	cooldownUntil                      sql.NullString
	lastErrorCode                      sql.NullString
	lastErrorMessage                   sql.NullString
	lastErrorTraceID                   sql.NullString
	cooldownRetestFailureCount         int
	cooldownRetestObservationStartedAt sql.NullString
	cooldownRetestGeneration           sql.NullString
	cooldownRetestLastAt               sql.NullString
	cooldownRetestLastStatusCode       sql.NullInt64
	streamFailureCount                 int
	streamFailureWindowStartedAt       sql.NullString
	authorizationID                    sql.NullString
	sourceAccountID                    sql.NullString
	authorizationStatus                sql.NullString
	authorizationExpiresAt             sql.NullString
	sourceID                           sql.NullString
	sourceStatus                       sql.NullString
	sourceLastErrorCode                sql.NullString
	sourceLastErrorMessage             sql.NullString
	sourceAccountExpiresAt             sql.NullString
	sourceCooldownUntil                sql.NullString
	sourceSchedulable                  sql.NullInt64
}

// authorizedDispatchBinding is the loadAuthorizedAccountBindingForUpdate row.
type authorizedDispatchBinding struct {
	groupID                string
	accountAuthorizationID string
	localPriority          int
	localSuperPriority     bool
	localFallback          bool
}

// UpdateAuthorizedDispatch mirrors updateAuthorizedAccountBindingDispatchAsync.
// Returns (nil, nil) when the row/binding/authorization is gone (route 404
// 授权账户不存在或尚未绑定分组).
func (s *Store) UpdateAuthorizedDispatch(ctx context.Context, accountID string, input AuthorizedDispatchInput, access AccessScope) (*AuthorizedDispatchResult, error) {
	ctx = ensureCtx(ctx)
	grantee := access.viewerID()
	if grantee == "" {
		return nil, nil
	}
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row, err := s.loadAuthorizedDispatchRow(ctx, tx, id, grantee)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	binding, err := s.loadAuthorizedDispatchBinding(ctx, tx, row)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, nil
	}
	if row.configRevision != input.ExpectedConfigRevision {
		return nil, &AuthorizedDispatchRevisionConflictError{AccountID: id, ExpectedConfigRevision: input.ExpectedConfigRevision}
	}
	if row.authorizationStatus.Valid && (row.authorizationStatus.String == "revoked" || row.authorizationStatus.String == "returned") {
		return nil, nil
	}
	outcome, err := s.patchAuthorizedDispatchTx(ctx, tx, row, binding, input)
	if err != nil {
		return nil, err
	}
	if outcome == nil {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Node runtimeRestoreRequired → clearServerAccountRuntimeAvailability via
	// the runtime-reset port (a nil port skips the runtime surfaces).
	if outcome.RuntimeRestoreRequired {
		if effects := s.runtimeResetEffectsOrNil(); effects != nil {
			_, _ = effects.ClearAccountRuntimeAvailability(ctx, RuntimeAvailabilityClearInput{
				AccountID: outcome.ID,
				AuthorizedBinding: &RuntimeAuthorizedBinding{
					SystemAccountID:        outcome.AuthorizedBinding.SystemAccountID,
					GroupID:                outcome.AuthorizedBinding.GroupID,
					AccountAuthorizationID: outcome.AuthorizedBinding.AccountAuthorizationID,
				},
				IncludeBaseAccountKey: false,
			})
		}
	}
	return outcome, nil
}

// loadAuthorizedDispatchRow reads the instance row inside the transaction.
func (s *Store) loadAuthorizedDispatchRow(ctx context.Context, q queryer, accountID, grantee string) (*authorizedDispatchRow, error) {
	var row authorizedDispatchRow
	err := q.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision, accounts.system_account_id,
			accounts.name, accounts.status, accounts.schedulable, accounts.account_expires_at,
			accounts.cooldown_until, accounts.last_error_code, accounts.last_error_message,
			accounts.last_error_trace_id, accounts.cooldown_retest_failure_count,
			accounts.cooldown_retest_observation_started_at, accounts.cooldown_retest_generation,
			accounts.cooldown_retest_last_at, accounts.cooldown_retest_last_status_code,
			accounts.stream_failure_count, accounts.stream_failure_window_started_at,
			accounts.authorization_instance_authorization_id, accounts.authorization_instance_source_account_id,
			authorizations.status AS authorization_status, authorizations.expires_at AS authorization_expires_at,
			source_accounts.id AS source_id, source_accounts.status AS source_status,
			source_accounts.last_error_code AS source_last_error_code,
			source_accounts.last_error_message AS source_last_error_message,
			source_accounts.account_expires_at AS source_account_expires_at,
			source_accounts.cooldown_until AS source_cooldown_until,
			source_accounts.schedulable AS source_schedulable
		FROM `+s.table("accounts")+` accounts
		LEFT JOIN `+s.table("resource_authorizations")+` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
		LEFT JOIN `+s.table("accounts")+` source_accounts
			ON source_accounts.id = accounts.authorization_instance_source_account_id
			AND source_accounts.deleted_at IS NULL
		WHERE accounts.id = ?
			AND accounts.system_account_id = ?
			AND accounts.deleted_at IS NULL`+s.forUpdate()+`
		LIMIT 1`), accountID, grantee).Scan(
		&row.id, &row.configRevision, &row.systemAccountID,
		&row.name, &row.status, &row.schedulable, &row.accountExpiresAt,
		&row.cooldownUntil, &row.lastErrorCode, &row.lastErrorMessage,
		&row.lastErrorTraceID, &row.cooldownRetestFailureCount,
		&row.cooldownRetestObservationStartedAt, &row.cooldownRetestGeneration,
		&row.cooldownRetestLastAt, &row.cooldownRetestLastStatusCode,
		&row.streamFailureCount, &row.streamFailureWindowStartedAt,
		&row.authorizationID, &row.sourceAccountID,
		&row.authorizationStatus, &row.authorizationExpiresAt,
		&row.sourceID, &row.sourceStatus,
		&row.sourceLastErrorCode, &row.sourceLastErrorMessage,
		&row.sourceAccountExpiresAt, &row.sourceCooldownUntil, &row.sourceSchedulable)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// loadAuthorizedDispatchBinding reads the grantee's enabled binding row.
func (s *Store) loadAuthorizedDispatchBinding(ctx context.Context, q queryer, row *authorizedDispatchRow) (*authorizedDispatchBinding, error) {
	var binding authorizedDispatchBinding
	var priority int64
	var super, fallback int
	err := q.QueryRowContext(ctx, s.bind(`SELECT group_id, account_authorization_id, local_priority,
			local_super_priority_enabled, local_fallback_enabled
		FROM `+s.table("group_accounts")+`
		WHERE account_id = ? AND system_account_id = ? AND enabled = 1`+s.forUpdate()+`
		LIMIT 1`), row.id, row.systemAccountID).Scan(
		&binding.groupID, &binding.accountAuthorizationID, &priority, &super, &fallback)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	binding.localPriority = int(priority)
	binding.localSuperPriority = super == 1
	binding.localFallback = fallback == 1
	return &binding, nil
}

// authorizedDispatchUnavailableMessage mirrors
// authorizedDispatchUnavailableMessage (the quota gate degrades to not
// exceeded without the wired quota port, exactly like the zero-cost read).
func (s *Store) authorizedDispatchUnavailableMessage(row *authorizedDispatchRow, allowLocalRecovery bool) string {
	now := s.now()
	nowISO := isoMillis(now)
	if row.authorizationStatus.Valid {
		switch row.authorizationStatus.String {
		case "expired":
			return "授权已到期，当前账户不能调用"
		case "paused":
			return "授权已暂停，当前账户不能调用"
		case "revoked", "returned":
			return "授权关系已失效，当前账户不能调用"
		}
	}
	if row.authorizationExpiresAt.Valid {
		if expired, ok := instantNotAfter(row.authorizationExpiresAt.String, nowISO); ok && expired {
			return "授权已到期，当前账户不能调用"
		}
	}
	if !row.sourceID.Valid || row.sourceID.String == "" || !row.sourceStatus.Valid || row.sourceStatus.String == "" {
		return "授权方原账户不存在或已删除，当前账户不能调用"
	}
	if row.sourceLastErrorCode.Valid && row.sourceLastErrorCode.String == "account_expired" ||
		row.sourceAccountExpiresAt.Valid && isAccountExpired(row.sourceAccountExpiresAt.String, now) {
		return "授权方原账户已到期，当前账户不能调用"
	}
	switch {
	case row.sourceStatus.String == "disabled":
		return "授权方原账户已停用，当前账户不能调用"
	case row.sourceStatus.String == "pending_test":
		return "授权方原账户尚未通过后台健康检查，当前账户不能调用"
	case row.sourceStatus.String == "error":
		return sqlTextOr(row.sourceLastErrorMessage, "授权方原账户处于异常状态，当前账户不能调用")
	case row.sourceStatus.String == "rate_limited":
		return sqlTextOr(row.sourceLastErrorMessage, "授权方原账户限流中，当前账户不能调用")
	case row.sourceStatus.String == "temporary_unavailable":
		return sqlTextOr(row.sourceLastErrorMessage, "授权方原账户临时不可调用，当前账户不能调用")
	case row.sourceStatus.String == "quality_isolated":
		return sqlTextOr(row.sourceLastErrorMessage, "授权方原账户因模型质量不达标已隔离，恢复前不能调用")
	}
	if row.sourceCooldownUntil.Valid {
		if cooling, ok := instantLater(row.sourceCooldownUntil.String, nowISO); ok && cooling {
			return "授权方原账户正在冷却，恢复前当前账户不能调用"
		}
	}
	if !row.sourceSchedulable.Valid || row.sourceSchedulable.Int64 != 1 {
		return "授权方原账户已关闭调度，当前账户不能调用"
	}
	if row.lastErrorCode.Valid && row.lastErrorCode.String == "account_expired" ||
		row.accountExpiresAt.Valid && isAccountExpired(row.accountExpiresAt.String, now) {
		return "授权账户已到期，当前不可用"
	}
	if !allowLocalRecovery {
		switch row.status {
		case "disabled":
			return "授权账户已停用，当前不可用"
		case "pending_test":
			return "授权账户正在等待后台健康检查，检查通过前不会参与调度"
		case "error":
			return sqlTextOr(row.lastErrorMessage, "授权账户处于异常状态，当前不可用")
		case "rate_limited":
			return sqlTextOr(row.lastErrorMessage, "授权账户限流中，恢复前不会参与调度")
		case "temporary_unavailable":
			return sqlTextOr(row.lastErrorMessage, "授权账户临时不可调用，恢复前不会参与调度")
		case "quality_isolated":
			return sqlTextOr(row.lastErrorMessage, "授权账户因模型质量不达标已隔离，质量恢复检查通过前不会参与调度")
		}
		if row.cooldownUntil.Valid {
			if cooling, ok := instantLater(row.cooldownUntil.String, nowISO); ok && cooling {
				return "授权账户正在冷却，恢复前不会参与调度"
			}
		}
		if row.schedulable != 1 {
			return "授权账户暂时不可调用，恢复前不会参与调度"
		}
	}
	return ""
}

func sqlTextOr(value sql.NullString, fallback string) string {
	if value.Valid && strings.TrimSpace(value.String) != "" {
		return value.String
	}
	return fallback
}

// instantLater reports whether left is strictly later than right.
func instantLater(left, right string) (bool, bool) {
	leftMS, leftOK := balanceSnapshotTimestampMs(left)
	rightMS, rightOK := balanceSnapshotTimestampMs(right)
	if !leftOK || !rightOK {
		return false, false
	}
	return leftMS > rightMS, true
}

// instantNotAfter reports whether left is at or before right.
func instantNotAfter(left, right string) (bool, bool) {
	later, ok := instantLater(left, right)
	if !ok {
		return false, false
	}
	return !later, true
}

// patchAuthorizedDispatchTx mirrors patchAuthorizedAccountDispatchInTransaction.
func (s *Store) patchAuthorizedDispatchTx(ctx context.Context, tx *sql.Tx, row *authorizedDispatchRow, binding *authorizedDispatchBinding, input AuthorizedDispatchInput) (*AuthorizedDispatchResult, error) {
	hasStatus := input.Status != nil
	hasPriority := input.Priority != nil
	hasSuper := input.SuperPriorityEnabled != nil
	hasFallback := input.FallbackEnabled != nil
	clearFailure := input.ClearFailureState

	if (hasStatus || clearFailure) && row.status == "pending_test" {
		return nil, errors.New("待检查账户需等待后台健康检查通过后才能参与调度")
	}
	if (hasStatus && *input.Status == "active") || clearFailure ||
		(hasSuper && *input.SuperPriorityEnabled) || (hasFallback && *input.FallbackEnabled) {
		if message := s.authorizedDispatchUnavailableMessage(row, (hasStatus && *input.Status == "active") || clearFailure); message != "" {
			return nil, errors.New(message)
		}
	}
	currentSchedulable := row.schedulable == 1
	nextPriority := binding.localPriority
	if hasPriority {
		nextPriority = *input.Priority
	}
	nextSuper := binding.localSuperPriority
	if hasSuper {
		nextSuper = *input.SuperPriorityEnabled
	}
	nextFallback := binding.localFallback
	if hasFallback {
		nextFallback = *input.FallbackEnabled
	}
	if nextSuper && nextFallback {
		return nil, errors.New("超级优先和降级备用不能同时开启")
	}
	nextStatus := row.status
	if hasStatus {
		if *input.Status == "disabled" {
			nextStatus = "disabled"
		} else {
			nextStatus = "active"
		}
	} else if clearFailure {
		nextStatus = "active"
	}
	nextSchedulable := currentSchedulable
	if hasStatus {
		nextSchedulable = *input.Status != "disabled"
	} else if clearFailure {
		nextSchedulable = true
	}
	accountSets := map[string]any{}
	bindingSets := map[string]any{}
	changes := []AuthorizedDispatchChange{}
	patch := AuthorizedDispatchPatch{}
	addChange := func(field string, before, after any) {
		if deepEqualAny(before, after) {
			return
		}
		changes = append(changes, AuthorizedDispatchChange{Field: field, Before: before, After: after})
	}
	if hasPriority {
		if binding.localPriority != nextPriority {
			bindingSets["local_priority"] = nextPriority
		}
		addChange("priority", binding.localPriority, nextPriority)
		if binding.localPriority != nextPriority {
			value := nextPriority
			patch.Priority = &value
		}
	}
	if hasSuper {
		if binding.localSuperPriority != nextSuper {
			bindingSets["local_super_priority_enabled"] = boolInt(nextSuper)
		}
		addChange("superPriorityEnabled", binding.localSuperPriority, nextSuper)
		if binding.localSuperPriority != nextSuper {
			patch.SuperPriorityEnabled = &nextSuper
		}
	}
	if hasFallback {
		if binding.localFallback != nextFallback {
			bindingSets["local_fallback_enabled"] = boolInt(nextFallback)
		}
		addChange("fallbackEnabled", binding.localFallback, nextFallback)
		if binding.localFallback != nextFallback {
			patch.FallbackEnabled = &nextFallback
		}
	}
	failureStateChanged := false
	if hasStatus || clearFailure {
		if row.status != nextStatus {
			accountSets["status"] = nextStatus
		}
		if currentSchedulable != nextSchedulable {
			accountSets["schedulable"] = boolInt(nextSchedulable)
		}
		addChange("status", row.status, nextStatus)
		addChange("schedulable", currentSchedulable, nextSchedulable)
		if row.status != nextStatus {
			patch.Status = &nextStatus
		}
		if currentSchedulable != nextSchedulable {
			patch.Schedulable = &nextSchedulable
		}
		// clearAuthorizedFailureStateColumns.
		before := len(accountSets)
		if row.cooldownUntil.Valid {
			accountSets["cooldown_until"] = nil
		}
		if row.lastErrorCode.Valid {
			accountSets["last_error_code"] = nil
		}
		if row.lastErrorMessage.Valid {
			accountSets["last_error_message"] = nil
		}
		if row.lastErrorTraceID.Valid {
			accountSets["last_error_trace_id"] = nil
		}
		if row.cooldownRetestFailureCount != 0 {
			accountSets["cooldown_retest_failure_count"] = 0
		}
		if row.cooldownRetestObservationStartedAt.Valid {
			accountSets["cooldown_retest_observation_started_at"] = nil
		}
		if row.cooldownRetestGeneration.Valid {
			accountSets["cooldown_retest_generation"] = nil
		}
		if row.cooldownRetestLastAt.Valid {
			accountSets["cooldown_retest_last_at"] = nil
		}
		if row.cooldownRetestLastStatusCode.Valid {
			accountSets["cooldown_retest_last_status_code"] = nil
		}
		if row.streamFailureCount != 0 {
			accountSets["stream_failure_count"] = 0
		}
		if row.streamFailureWindowStartedAt.Valid {
			accountSets["stream_failure_window_started_at"] = nil
		}
		if len(accountSets) > before {
			failureStateChanged = true
			patch.FailureStateCleared = true
			changes = append(changes, AuthorizedDispatchChange{Field: "failureState", Before: "异常状态", After: "已清除"})
		}
	}
	if len(accountSets) == 0 && len(bindingSets) == 0 {
		return &AuthorizedDispatchResult{
			ID:              row.id,
			ConfigRevision:  row.configRevision,
			ChangedFields:   []string{},
			Patch:           patch,
			Changes:         changes,
			Name:            row.name,
			OwnerSystemAccountID: row.systemAccountID,
			AuthorizedBinding: AuthorizedDispatchBinding{
				SystemAccountID:        row.systemAccountID,
				GroupID:                binding.groupID,
				AccountAuthorizationID: binding.accountAuthorizationID,
			},
		}, nil
	}
	now := isoMillis(s.now())
	if len(accountSets) > 0 {
		sets := []string{}
		args := []any{}
		for column, value := range accountSets {
			sets = append(sets, column+" = ?")
			args = append(args, value)
		}
		sets = append(sets, "config_revision = config_revision + 1", "updated_at = ?")
		args = append(args, now, row.id, row.systemAccountID, row.authorizationID.String, row.configRevision)
		exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
			SET `+strings.Join(sets, ", ")+`
			WHERE id = ?
				AND system_account_id = ?
				AND authorization_instance_authorization_id = ?
				AND config_revision = ?
				AND deleted_at IS NULL`), args...)
		if err != nil {
			return nil, err
		}
		if affected, _ := exec.RowsAffected(); affected != 1 {
			return nil, &AuthorizedDispatchRevisionConflictError{AccountID: row.id, ExpectedConfigRevision: row.configRevision}
		}
	}
	if len(bindingSets) > 0 {
		sets := []string{}
		args := []any{}
		for column, value := range bindingSets {
			sets = append(sets, column+" = ?")
			args = append(args, value)
		}
		sets = append(sets, "updated_at = ?")
		args = append(args, now, row.id, row.systemAccountID, binding.groupID, binding.accountAuthorizationID)
		exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_accounts")+`
			SET `+strings.Join(sets, ", ")+`
			WHERE account_id = ?
				AND system_account_id = ?
				AND group_id = ?
				AND account_authorization_id = ?
				AND enabled = 1`), args...)
		if err != nil {
			return nil, err
		}
		if affected, _ := exec.RowsAffected(); affected != 1 {
			return nil, errors.New("授权账户分组绑定已发生变化，请刷新后重试")
		}
	}
	restoredForDispatch := nextStatus == "active" && (!currentSchedulable || row.status != "active" || failureStateChanged)
	if restoredForDispatch {
		if err := s.advanceBatchDispatchRevision(ctx, tx, row.id, newID("dispatch"), s.now().UnixMilli()); err != nil {
			return nil, err
		}
	}
	changedFields := make([]string, 0, len(changes))
	for _, change := range changes {
		changedFields = append(changedFields, change.Field)
	}
	sort.Strings(changedFields)
	return &AuthorizedDispatchResult{
		ID:              row.id,
		ConfigRevision:  row.configRevision + 1,
		ChangedFields:   changedFields,
		Patch:           patch,
		Changes:         changes,
		Name:            row.name,
		OwnerSystemAccountID: row.systemAccountID,
		// Node runtimeRestoreRequired: clearFailureState || input.status ===
		// 'active'.
		RuntimeRestoreRequired: clearFailure || (hasStatus && *input.Status == "active"),
		AuthorizedBinding: AuthorizedDispatchBinding{
			SystemAccountID:        row.systemAccountID,
			GroupID:                binding.groupID,
			AccountAuthorizationID: binding.accountAuthorizationID,
		},
	}, nil
}


func deepEqualAny(left, right any) bool {
	leftRaw, errLeft := json.Marshal(left)
	rightRaw, errRight := json.Marshal(right)
	if errLeft != nil || errRight != nil {
		return false
	}
	return string(leftRaw) == string(rightRaw)
}

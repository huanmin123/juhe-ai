package accounts

import (
	"context"
	"database/sql"
	"time"
)

// 编辑路径（PATCH）的状态机与运行态归一化（Node→Go 迁移缺口登记项 1/3）。
//
// 归档依据 backend/src/storage/account-management-patch.repository.ts：
//   - :556-637 连接面组装（connectionChanged）、status/schedulable 状态机、
//     连接变化 → pending_test 联动与 nextRuntimeState 调用；
//   - :1736-1845 nextRuntimeState / applyRuntimeStateColumns（冷却/错误/
//     重试观察列族按目标状态归一化，clearRetest 生命周期含代际保留规则）；
//   - :1938-1949 assertStatusMutationAllowed / accountStatusForcesSchedulableOff；
//   - :1492-1508 hasRetainedActiveAccountApiKeyInClient +
//     account-api-key-rotation.ts:141-171 isAccountApiKeyPoolIsolationEnabled
//     （Key 池轮换只有在隔离池无保留活跃 Key 时才算连接变化）；
//   - account-runtime-mutation-helpers.ts:37-56 initialCooldownUntilForStatus /
//     cooldownRetestObservationStartedAtForStatus（temporary_unavailable 初始
//     退避 3 秒，rate_limited 用 defaultTemporaryUnschedulableMinutes）。
//
// 双方言说明：所有语句经 s.bind/s.table，运行态列族经 setRuntimeColumn 的
// 单赋值通道并入 SET，PG/SQLite 共用同一语句形状。

// RuntimeCooldownSettings is the narrow settings port behind the rate_limited
// re-arm cooldown (Node getSettings().defaultTemporaryUnschedulableMinutes).
type RuntimeCooldownSettings interface {
	// DefaultTemporaryUnschedulableMinutes returns the validated setting
	// (Node integerSetting(1, 1440)).
	DefaultTemporaryUnschedulableMinutes() int
}

// SetRuntimeCooldownSettings wires the settings port (compose handover; nil
// keeps the Node schema default below).
func (s *Store) SetRuntimeCooldownSettings(settings RuntimeCooldownSettings) {
	s.runtimeCooldownSettings = settings
}

// defaultTemporaryUnschedulableMinutesFallback mirrors schema-defaults.ts:599
// (['defaultTemporaryUnschedulableMinutes', 2]) — also the Go jobs default —
// used while the settings port is unwired.
const defaultTemporaryUnschedulableMinutesFallback = 2

func (s *Store) defaultTemporaryUnschedulableMinutes() int {
	if s.runtimeCooldownSettings != nil {
		return s.runtimeCooldownSettings.DefaultTemporaryUnschedulableMinutes()
	}
	return defaultTemporaryUnschedulableMinutesFallback
}

// assertStatusMutationAllowed mirrors assertStatusMutationAllowed
// (:1938-1944): the basic-edit surface only accepts active/pending_test/
// disabled, a same-value rewrite, or the manual active → temporary_unavailable
// isolation.
func assertStatusMutationAllowed(current, requested string) error {
	switch requested {
	case "active", "pending_test", "disabled":
		return nil
	}
	if current == requested {
		return nil
	}
	if current == "active" && requested == "temporary_unavailable" {
		return nil
	}
	return &ValidationError{Message: "编辑状态只支持可调度、待检查或停用；正常账户可通过人工隔离进入临时不可调用"}
}

// accountStatusForcesSchedulableOff mirrors accountStatusForcesSchedulableOff
// (:1946-1949): temporary_unavailable plus every hard-unavailable status
// except disabled forces schedulable off.
func accountStatusForcesSchedulableOff(status string) bool {
	if status == "temporary_unavailable" {
		return true
	}
	switch status {
	case "pending_test", "error", "quality_isolated":
		return true
	}
	return false
}

// isCoolingAccountStatus mirrors isCoolingAccountStatus (account-status.ts).
func isCoolingAccountStatus(status string) bool {
	return status == "rate_limited" || status == "temporary_unavailable"
}

// patchRuntimeStateBefore carries the row's persisted runtime-state columns
// into the normalization.
type patchRuntimeStateBefore struct {
	status                       string
	cooldownUntil                sql.NullString
	lastErrorCode                sql.NullString
	lastErrorMessage             sql.NullString
	lastErrorTraceID             sql.NullString
	cooldownRetestFailureCount   int
	cooldownRetestObservation    sql.NullString
	cooldownRetestGeneration     sql.NullString
	cooldownRetestLastAt         sql.NullString
	cooldownRetestLastStatusCode sql.NullInt64
}

// patchRuntimeStateInput mirrors nextRuntimeState's input object; note the
// caller passes hasStatusInput = statusChanged (归档 :621-627).
type patchRuntimeStateInput struct {
	nextStatus        string
	hasStatusInput    bool
	connectionChanged bool
	expiredByPackage  bool
	now               time.Time
}

// patchRuntimeState mirrors nextRuntimeState's return record.
type patchRuntimeState struct {
	cooldownUntil                sql.NullString
	lastErrorCode                sql.NullString
	lastErrorMessage             sql.NullString
	lastErrorTraceID             sql.NullString
	cooldownRetestFailureCount   int
	cooldownRetestObservation    sql.NullString
	cooldownRetestGeneration     sql.NullString
	cooldownRetestLastAt         sql.NullString
	cooldownRetestLastStatusCode sql.NullInt64
}

// nextRuntimeState mirrors nextRuntimeState (:1736-1829): the cooldown/error/
// retest-observation columns normalized against the target status. The
// clearRetest tail keeps a freshly armed generation (cooling arm) and only
// nulls the generation when no new observation started — the lifecycle rule
// the jobs cooldown fence depends on.
func (s *Store) nextRuntimeState(before patchRuntimeStateBefore, in patchRuntimeStateInput) patchRuntimeState {
	state := patchRuntimeState{
		cooldownUntil:                before.cooldownUntil,
		lastErrorCode:                before.lastErrorCode,
		lastErrorMessage:             before.lastErrorMessage,
		lastErrorTraceID:             before.lastErrorTraceID,
		cooldownRetestFailureCount:   before.cooldownRetestFailureCount,
		cooldownRetestObservation:    before.cooldownRetestObservation,
		cooldownRetestGeneration:     before.cooldownRetestGeneration,
		cooldownRetestLastAt:         before.cooldownRetestLastAt,
		cooldownRetestLastStatusCode: before.cooldownRetestLastStatusCode,
	}
	clearRetest := false
	if in.hasStatusInput || in.connectionChanged {
		switch {
		case in.nextStatus == "active":
			state.cooldownUntil = sql.NullString{}
			state.lastErrorCode = sql.NullString{}
			state.lastErrorMessage = sql.NullString{}
			state.lastErrorTraceID = sql.NullString{}
			state.cooldownRetestObservation = sql.NullString{}
			clearRetest = true
		case in.nextStatus == "pending_test":
			state.cooldownUntil = sql.NullString{}
			state.lastErrorCode = sql.NullString{}
			state.lastErrorMessage = sqlNullString("账户配置已保存，等待后台检查")
			state.lastErrorTraceID = sql.NullString{}
			state.cooldownRetestObservation = sql.NullString{}
			clearRetest = true
		case in.nextStatus == "disabled" || in.nextStatus == "error":
			state.cooldownUntil = sql.NullString{}
			state.cooldownRetestObservation = sql.NullString{}
			if in.nextStatus == "disabled" {
				state.lastErrorCode = sql.NullString{}
				state.lastErrorMessage = sql.NullString{}
				state.lastErrorTraceID = sql.NullString{}
				clearRetest = true
			}
		case isCoolingAccountStatus(in.nextStatus) &&
			(in.nextStatus != before.status || !cooldownUntilArmed(before.cooldownUntil)):
			// 冷却态重置：目标状态与当前不同，或冷却窗口已经失效时重新武装。
			state.cooldownUntil = s.runtimeMutationInitialCooldownUntil(in.nextStatus, in.now)
			state.cooldownRetestObservation = cooldownRetestObservationStartedAtForStatus(in.nextStatus, in.now)
			if state.cooldownRetestObservation.Valid {
				state.cooldownRetestGeneration = sqlNullString(newCooldownGeneration())
			} else {
				state.cooldownRetestGeneration = sql.NullString{}
			}
			state.lastErrorCode = sql.NullString{}
			if in.nextStatus == "temporary_unavailable" {
				state.lastErrorMessage = sqlNullString("手动设置为临时不可调用")
			} else {
				state.lastErrorMessage = sqlNullString("手动设置为限流中")
			}
			clearRetest = in.nextStatus == "temporary_unavailable"
		}
	}
	if in.expiredByPackage {
		state.cooldownUntil = sql.NullString{}
		state.lastErrorCode = sqlNullString("account_expired")
		state.lastErrorMessage = sqlNullString("账户套餐已过期，已自动停用")
		state.lastErrorTraceID = sql.NullString{}
		state.cooldownRetestObservation = sql.NullString{}
		clearRetest = true
	}
	if clearRetest {
		state.cooldownRetestFailureCount = 0
		if !state.cooldownRetestObservation.Valid {
			state.cooldownRetestGeneration = sql.NullString{}
		}
		state.cooldownRetestLastAt = sql.NullString{}
		state.cooldownRetestLastStatusCode = sql.NullInt64{}
	}
	return state
}

// applyRuntimeStateColumns mirrors applyRuntimeStateColumns (:1831-1845):
// setMapIfChanged semantics — a column only enters the assignment set when
// the normalized value differs from the persisted one.
func applyRuntimeStateColumns(setColumn func(column string, value any), before patchRuntimeStateBefore, state patchRuntimeState) {
	if before.cooldownUntil != state.cooldownUntil {
		setColumn("cooldown_until", nullStringValue(state.cooldownUntil))
	}
	if before.lastErrorCode != state.lastErrorCode {
		setColumn("last_error_code", nullStringValue(state.lastErrorCode))
	}
	if before.lastErrorMessage != state.lastErrorMessage {
		setColumn("last_error_message", nullStringValue(state.lastErrorMessage))
	}
	if before.lastErrorTraceID != state.lastErrorTraceID {
		setColumn("last_error_trace_id", nullStringValue(state.lastErrorTraceID))
	}
	if before.cooldownRetestFailureCount != state.cooldownRetestFailureCount {
		setColumn("cooldown_retest_failure_count", state.cooldownRetestFailureCount)
	}
	if before.cooldownRetestObservation != state.cooldownRetestObservation {
		setColumn("cooldown_retest_observation_started_at", nullStringValue(state.cooldownRetestObservation))
	}
	if before.cooldownRetestGeneration != state.cooldownRetestGeneration {
		setColumn("cooldown_retest_generation", nullStringValue(state.cooldownRetestGeneration))
	}
	if before.cooldownRetestLastAt != state.cooldownRetestLastAt {
		setColumn("cooldown_retest_last_at", nullStringValue(state.cooldownRetestLastAt))
	}
	if before.cooldownRetestLastStatusCode != state.cooldownRetestLastStatusCode {
		setColumn("cooldown_retest_last_status_code", nullInt64Value(state.cooldownRetestLastStatusCode))
	}
}

// runtimeMutationInitialCooldownUntil mirrors initialCooldownUntilForStatus
// (account-runtime-mutation-helpers.ts:37-48): temporary_unavailable arms the
// 3-second initial backoff, rate_limited the defaultTemporaryUnschedulableMinutes
// window. patch.go 的同名函数（bounded-recovery 两个臂使用）已对齐同一 3 秒
// 初始退避；本函数额外覆盖编辑路径状态机需要的 rate_limited 分支并返回
// sql.NullString 形状。
func (s *Store) runtimeMutationInitialCooldownUntil(status string, now time.Time) sql.NullString {
	switch status {
	case "temporary_unavailable":
		return sqlNullString(isoMillis(now.Add(3 * time.Second)))
	case "rate_limited":
		minutes := s.defaultTemporaryUnschedulableMinutes()
		return sqlNullString(isoMillis(now.Add(time.Duration(minutes) * time.Minute)))
	}
	return sql.NullString{}
}

// cooldownRetestObservationStartedAtForStatus mirrors
// cooldownRetestObservationStartedAtForStatus
// (account-runtime-mutation-helpers.ts:50-52): cooling statuses stamp the
// observation start with now.
func cooldownRetestObservationStartedAtForStatus(status string, now time.Time) sql.NullString {
	if isCoolingAccountStatus(status) {
		return sqlNullString(isoMillis(now))
	}
	return sql.NullString{}
}

// cooldownUntilArmed mirrors the Node truthiness check `!cooldownUntil`: an
// absent or blank cooldown column counts as disarmed.
func cooldownUntilArmed(value sql.NullString) bool {
	return value.Valid && value.String != ""
}

func sqlNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func nullStringValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func nullInt64Value(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

// changesHaveField reports whether the diff already carries the field.
func changesHaveField(changes []PatchChange, field string) bool {
	for index := range changes {
		if changes[index].Field == field {
			return true
		}
	}
	return false
}

// isAccountAPIKeyPoolIsolationEnabled mirrors isAccountApiKeyPoolIsolationEnabled
// (account-api-key-rotation.ts:141-155): api_key accounts on a supported
// provider with more than one effective key run the isolated Key pool.
func isAccountAPIKeyPoolIsolationEnabled(providerCode, protocolCode, protocolVersion, accountType string, credentials Credentials) bool {
	if accountType != "api_key" {
		return false
	}
	if !isAccountAPIKeyPoolProviderSupported(providerCode, protocolCode, protocolVersion) {
		return false
	}
	return EffectiveAccountApiKeyCount(credentials) > 1
}

// isAccountAPIKeyPoolProviderSupported mirrors
// isAccountApiKeyPoolProviderSupported (account-api-key-rotation.ts:157-171).
func isAccountAPIKeyPoolProviderSupported(providerCode, protocolCode, protocolVersion string) bool {
	normalized := normalizeProviderToken(providerCode)
	if normalized == openAICompatibleProviderCodeConstant || normalized == gptVendorCode {
		return true
	}
	if isDeepSeekProviderCodeToken(providerCode) || isGlmProviderCodeToken(providerCode) ||
		isGeminiProviderCodeToken(providerCode) {
		return true
	}
	if isAnthropicProtocolProfileOf(protocolPredicateInput{
		protocolCode: protocolCode, protocolVersion: protocolVersion,
	}) {
		return true
	}
	return normalized == anthropicProviderCode
}

// hasRetainedActiveAccountAPIKeyState mirrors
// hasRetainedActiveAccountApiKeyInClient (:1492-1508): at least one current
// Key surviving into the next pool must still sit in the active runtime state
// (a missing runtime row counts as active).
func (s *Store) hasRetainedActiveAccountAPIKeyState(ctx context.Context, q queryer, accountID string, current, next Credentials) (bool, error) {
	nextFingerprints := map[string]bool{}
	for _, entry := range accountAPIKeyEntries(s.secret, next) {
		nextFingerprints[entry.fingerprint] = true
	}
	if len(nextFingerprints) == 0 {
		return false, nil
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT key_fingerprint, status FROM `+s.table("account_api_key_runtime_states")+`
		WHERE account_id = ?`), accountID)
	if err != nil {
		return false, err
	}
	statusByFingerprint := map[string]string{}
	for rows.Next() {
		var fingerprint, status string
		if err := rows.Scan(&fingerprint, &status); err != nil {
			rows.Close()
			return false, err
		}
		statusByFingerprint[fingerprint] = status
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, entry := range accountAPIKeyEntries(s.secret, current) {
		if !nextFingerprints[entry.fingerprint] {
			continue
		}
		if status, ok := statusByFingerprint[entry.fingerprint]; !ok || status == "active" {
			return true, nil
		}
	}
	return false, nil
}

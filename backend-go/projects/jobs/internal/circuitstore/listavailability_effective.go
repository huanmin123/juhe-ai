package circuitstore

// effective availability 状态机与 presentation：逐分支对照归档 Node
// domain/account-effective-availability.ts、domain/account-status-classification.ts、
// domain/account-status-presentation.ts（payload 只消费 statusBoundary / probe）。

import (
	"fmt"
	"strings"
	"time"
)

// effectiveAvailability 是 Node AccountEffectiveAvailability 的值形状。
type effectiveAvailability struct {
	available    bool
	status       string
	label        string
	color        string
	blockerScope string
	reason       string
	retryAt      string
}

// availabilityInput 是状态机输入集合（AccountEffectiveAvailabilityInput 的
// jobs 侧窄投影；全部为 DB 行 / seeds 派生 / 运行态读的已归一值）。
type availabilityInput struct {
	accessType                  string
	boundGroupID                string
	groupBindStatus             string
	authorizationStatus         string
	authorizationExpiresAt      string
	authorizationQuotaExceeded  bool
	sourceAccountID             string
	sourceStatus                string
	sourceSchedulable           *bool
	sourceExpiresAt             string
	sourceCooldownUntil         string
	sourceLastErrorCode         string
	sourceLastErrorMessage      string
	accountExpiresAt            string
	status                      string
	schedulable                 bool
	cooldownUntil               string
	lastErrorCode               string
	lastErrorMessage            string
	lastHealthCheckAt           string
	lastHealthCheckErrorCode    string
	lastHealthCheckErrorMessage string
	apiKeyAllUnavailable        bool
	apiKeyTotal                 int
	apiKeyNextProbeAt           string
	runtimeStatus               string
	runtimeReason               string
}

// accountEffectiveAvailability 对齐 accountEffectiveAvailability 全分支。
func accountEffectiveAvailability(input availabilityInput, now time.Time) effectiveAvailability {
	if input.accessType == "authorized" {
		if blocker := authorizedBindingAvailability(input); blocker != nil {
			return *blocker
		}
		if blocker := authorizationAvailability(input, now); blocker != nil {
			return *blocker
		}
		if input.authorizationQuotaExceeded {
			return blockedAvailability("authorization_quota_exceeded", "授权额度已用完", "red", "authorization", "授权额度已用完，当前账户不能调用", "")
		}
		if blocker := sourceAccountAvailability(input, now); blocker != nil {
			return *blocker
		}
	}
	if blocker := instanceAccountAvailability(input, now); blocker != nil {
		return *blocker
	}
	if input.apiKeyAllUnavailable {
		return blockedAvailability("api_key_pool_unavailable", "Key 全部不可用", "red", "api_key_pool",
			fmt.Sprintf("账户内 %d 个 API Key 均不可用，后台探测恢复前不会参与调度", input.apiKeyTotal), input.apiKeyNextProbeAt)
	}
	if blocker := runtimeAvailabilityBlocker(input); blocker != nil {
		return *blocker
	}
	return effectiveAvailability{available: true, status: "available", label: "可调度", color: "green"}
}

func authorizedBindingAvailability(input availabilityInput) *effectiveAvailability {
	if input.boundGroupID == "" {
		return blockedPtr("binding_missing", "未绑定分组", "red", "binding", "授权账户需要先绑定到你的分组", "")
	}
	if input.groupBindStatus == "authorization_unavailable" {
		return blockedPtr("authorization_unavailable", "授权已失效", "red", "binding", "当前分组绑定的授权已失效，请重新绑定分组或联系授权人", "")
	}
	return nil
}

func authorizationAvailability(input availabilityInput, now time.Time) *effectiveAvailability {
	if input.authorizationStatus == "expired" || isExpiredInstant(input.authorizationExpiresAt, now) {
		return blockedPtr("authorization_expired", "授权到期", "red", "authorization", "授权已到期，当前账户不能调用", "")
	}
	if input.authorizationStatus == "paused" {
		return blockedPtr("authorization_paused", "授权暂停", "orange", "authorization", "授权已暂停，当前账户不能调用", "")
	}
	if input.authorizationStatus == "revoked" || input.authorizationStatus == "returned" {
		return blockedPtr("authorization_unavailable", "授权已失效", "red", "authorization", "授权关系已失效，当前账户不能调用", "")
	}
	return nil
}

func sourceAccountAvailability(input availabilityInput, now time.Time) *effectiveAvailability {
	if input.sourceAccountID == "" || input.sourceStatus == "" {
		return blockedPtr("source_deleted", "来源缺失", "red", "source_account", "授权方原账户不存在或已删除，当前账户不能调用", "")
	}
	if input.sourceLastErrorCode == "account_expired" || isExpiredInstant(input.sourceExpiresAt, now) {
		return blockedPtr("source_expired", "来源到期", "red", "source_account", "授权方原账户已到期，当前账户不能调用", "")
	}
	switch input.sourceStatus {
	case "disabled":
		return blockedPtr("source_disabled", "来源停用", "red", "source_account", sourceReasonOf(input, "授权方原账户已停用，当前账户不能调用"), "")
	case "pending_test":
		return blockedPtr("source_pending_test", "来源待检查", "blue", "source_account", "授权方原账户尚未通过后台健康检查，当前账户不能调用", "")
	case "error":
		return blockedPtr("source_error", "来源异常", "red", "source_account", sourceReasonOf(input, "授权方原账户处于异常状态，当前账户不能调用"), "")
	case "rate_limited":
		return blockedPtr("source_rate_limited", "来源限流中", "orange", "source_account", sourceReasonOf(input, "授权方原账户限流中，当前账户不能调用"), "")
	case "temporary_unavailable":
		return blockedPtr("source_temporary_unavailable", "来源临时不可调用", "gold", "source_account", sourceReasonOf(input, "授权方原账户临时不可调用，当前账户不能调用"), "")
	case "quality_isolated":
		return blockedPtr("source_quality_isolated", "来源质量隔离", "red", "source_account", sourceReasonOf(input, "授权方原账户因模型质量不达标已隔离，恢复前不能调用"), "")
	}
	if isFutureInstant(input.sourceCooldownUntil, now) {
		return blockedPtr("source_cooldown", "来源冷却", "gold", "source_account", "授权方原账户正在冷却，恢复前当前账户不能调用", input.sourceCooldownUntil)
	}
	if input.sourceSchedulable != nil && !*input.sourceSchedulable {
		return blockedPtr("source_unschedulable", "来源停调", "orange", "source_account", "授权方原账户已关闭调度，当前账户不能调用", "")
	}
	return nil
}

func instanceAccountAvailability(input availabilityInput, now time.Time) *effectiveAvailability {
	instanceLabel := "账户"
	instanceReasonPrefix := "账户"
	blockerScope := "account"
	if input.accessType == "authorized" {
		instanceLabel = "授权实例"
		instanceReasonPrefix = "授权账户"
		blockerScope = "authorized_instance"
	}
	if input.lastErrorCode == "account_expired" || isExpiredInstant(input.accountExpiresAt, now) {
		return blockedPtr("instance_expired", instanceLabel+"到期", "red", blockerScope, instanceReasonPrefix+"已到期，当前不可用", "")
	}
	switch input.status {
	case "disabled":
		return blockedPtr("instance_disabled", instanceLabel+"停用", "default", blockerScope, instanceReasonPrefix+"已停用，当前不可用", "")
	case "pending_test":
		if input.lastHealthCheckAt != "" && (input.lastHealthCheckErrorCode != "" || input.lastHealthCheckErrorMessage != "") {
			return blockedPtr("instance_pending_test", instanceLabel+"检查失败", "red", blockerScope, instanceReasonPrefix+"后台健康检查未通过，系统将自动重试", "")
		}
		return blockedPtr("instance_pending_test", instanceLabel+"待检查", "blue", blockerScope, instanceReasonPrefix+"正在等待后台健康检查，检查通过前不会参与调度", "")
	case "error":
		return blockedPtr("instance_error", instanceLabel+"异常", "red", blockerScope,
			orDefault(input.lastErrorMessage, instanceReasonPrefix+"处于异常状态，当前不可用"), "")
	case "rate_limited":
		return blockedPtr("instance_rate_limited", instanceLabel+"限流中", "orange", blockerScope,
			orDefault(input.lastErrorMessage, instanceReasonPrefix+"限流中，恢复前不会参与调度"), "")
	case "temporary_unavailable":
		return blockedPtr("instance_temporary_unavailable", instanceLabel+"临时不可调用", "gold", blockerScope,
			orDefault(input.lastErrorMessage, instanceReasonPrefix+"临时不可调用，恢复前不会参与调度"), "")
	case "quality_isolated":
		return blockedPtr("instance_quality_isolated", instanceLabel+"质量隔离", "red", blockerScope,
			orDefault(input.lastErrorMessage, instanceReasonPrefix+"因模型质量不达标已隔离，质量恢复检查通过前不会参与调度"), "")
	}
	if isFutureInstant(input.cooldownUntil, now) {
		return blockedPtr("instance_cooldown", instanceLabel+"冷却", "gold", blockerScope,
			instanceReasonPrefix+"正在冷却，恢复前不会参与调度", input.cooldownUntil)
	}
	if !input.schedulable {
		return blockedPtr("instance_unschedulable", instanceLabel+"停调", "orange", blockerScope,
			instanceReasonPrefix+"暂时不可调用，恢复前不会参与调度", "")
	}
	return nil
}

func runtimeAvailabilityBlocker(input availabilityInput) *effectiveAvailability {
	runtime := input.runtimeStatus
	if runtime == "" || runtime == "normal" {
		return nil
	}
	if input.status != "active" {
		return nil
	}
	if runtime == "degraded" {
		return &effectiveAvailability{
			available: true, status: "runtime_degraded", label: "调度降级", color: "gold",
			blockerScope: "runtime",
			reason:       orDefault(input.runtimeReason, "当前账号近期失败，正常候选不足时才会兜底尝试"),
		}
	}
	switch runtime {
	case "precheck_pending":
		return blockedPtr("runtime_precheck_pending", "待探针确认", "blue", "runtime",
			orDefault(input.runtimeReason, "当前网关正在执行事前探针确认"), "")
	case "local_suppressed":
		return blockedPtr("runtime_local_suppressed", "短暂避让", "gold", "runtime",
			orDefault(input.runtimeReason, "当前网关短窗口内临时避让该账户"), "")
	case "half_open":
		return blockedPtr("runtime_half_open", "半开探测", "blue", "runtime",
			orDefault(input.runtimeReason, "当前网关已放行一个请求确认账户是否恢复"), "")
	case "precheck_failed":
		return blockedPtr("runtime_precheck_failed", "探针确认失败", "gold", "runtime",
			orDefault(input.runtimeReason, "最近事前探针确认失败，当前网关暂不调度该账户"), "")
	}
	return nil
}

func sourceReasonOf(input availabilityInput, fallback string) string {
	if input.sourceLastErrorMessage != "" {
		return input.sourceLastErrorMessage
	}
	return fallback
}

func blockedAvailability(status, label, color, scope, reason, retryAt string) effectiveAvailability {
	return effectiveAvailability{
		available: false,
		status:    status, label: label, color: color,
		blockerScope: scope, reason: reason, retryAt: retryAt,
	}
}

func blockedPtr(status, label, color, scope, reason, retryAt string) *effectiveAvailability {
	value := blockedAvailability(status, label, color, scope, reason, retryAt)
	return &value
}

// ---- 状态分类（domain/account-status-classification.ts）----

// filterStatusForEffectiveAvailabilityStatus 对齐
// accountStatusFilterForEffectiveAvailabilityStatus。
func filterStatusForEffectiveAvailabilityStatus(status string) string {
	switch status {
	case "available", "runtime_degraded":
		return "active"
	case "source_pending_test", "instance_pending_test":
		return "pending_test"
	case "source_error", "instance_error":
		return "error"
	case "source_quality_isolated", "instance_quality_isolated":
		return "quality_isolated"
	case "authorization_quota_exceeded", "source_rate_limited", "instance_rate_limited":
		return "rate_limited"
	case "source_temporary_unavailable", "source_cooldown", "instance_temporary_unavailable",
		"instance_cooldown", "api_key_pool_unavailable", "runtime_local_suppressed",
		"runtime_half_open", "runtime_precheck_pending", "runtime_precheck_failed":
		return "temporary_unavailable"
	case "authorization_expired", "authorization_paused", "authorization_unavailable",
		"binding_missing", "permission_denied", "source_deleted", "source_expired",
		"source_disabled", "source_unschedulable", "instance_expired", "instance_disabled",
		"instance_unschedulable":
		return "disabled"
	}
	return ""
}

// accountFilterStatuses 对齐 accountFilterStatuses：返回唯一投影状态；
// 无法唯一归类时返回空集合（调用方抛错，Node 同）。
func accountFilterStatuses(itemStatus string, item *effectiveAvailability, authorizationQuotaExceeded bool) []string {
	if item != nil {
		if derived := filterStatusForEffectiveAvailabilityStatus(item.status); derived != "" {
			return []string{derived}
		}
	}
	if authorizationQuotaExceeded {
		return []string{"rate_limited"}
	}
	if itemStatus == "active" && item != nil && !item.available {
		return []string{}
	}
	return []string{itemStatus}
}

// ---- presentation（payload 只消费 statusBoundary / probe）----

// presentationPayload 对齐 accountAvailabilityPresentation 的 payload 面。
func presentationPayload(availability *effectiveAvailability, row managementRow, runtimeRecoveryAt *string, now time.Time) map[string]any {
	if availability == nil {
		return nil
	}
	status, action := presentationMapping(availability.status)
	presentation := map[string]any{
		"status": status,
		"label":  availability.label,
		"action": action,
	}
	if availability.reason != "" {
		presentation["reason"] = availability.reason
	}
	if availability.status == "instance_error" && row.lastErrorCode.String == "account_activation_check_timeout" {
		presentation["action"] = "retry_check"
	} else if availability.status == "instance_error" && strings.HasPrefix(row.lastErrorCode.String, "cooldown_retest_") {
		presentation["action"] = "restore_account"
	}
	if boundary := statusBoundaryAtOf(availability, row, "", runtimeRecoveryAt); boundary != nil {
		presentation["statusBoundary"] = map[string]any{"at": *boundary, "kind": boundaryKindOf(availability.status)}
		return presentation
	}
	if probe := probeFactsPayload(availability, row, now); probe != nil {
		presentation["probe"] = probe
	}
	return presentation
}

func boundaryKindOf(status string) string {
	switch status {
	case "authorization_expired":
		return "authorization_expired"
	case "source_expired":
		return "source_expired"
	case "instance_expired":
		return "account_expired"
	case "authorization_quota_exceeded":
		return "quota_reset"
	case "source_cooldown", "instance_cooldown":
		return "cooldown_expiry"
	case "runtime_local_suppressed":
		return "policy_ttl_expiry"
	}
	return ""
}

// presentationMapping 对齐 mappings 表。
func presentationMapping(status string) (string, string) {
	switch status {
	case "available":
		return "available", "none"
	case "permission_denied":
		return "permission_denied", "contact_admin"
	case "authorization_expired":
		return "expired", "renew_authorization"
	case "authorization_paused", "authorization_unavailable", "authorization_quota_exceeded":
		return "authorization_blocked", "contact_authorizer"
	case "source_deleted", "source_disabled", "source_unschedulable":
		return "source_blocked", "contact_authorizer"
	case "source_expired":
		return "expired", "contact_authorizer"
	case "source_pending_test":
		return "pending_check", "contact_authorizer"
	case "source_error":
		return "error", "contact_authorizer"
	case "source_rate_limited":
		return "rate_limited", "contact_authorizer"
	case "source_temporary_unavailable", "source_cooldown":
		return "temporarily_unavailable", "contact_authorizer"
	case "source_quality_isolated":
		return "source_blocked", "contact_authorizer"
	case "instance_expired":
		return "expired", "renew_authorization"
	case "instance_disabled", "instance_unschedulable":
		return "disabled", "enable_account"
	case "instance_pending_test":
		return "pending_check", "retry_check"
	case "instance_error":
		return "error", "fix_configuration"
	case "instance_rate_limited":
		return "rate_limited", "restore_account"
	case "instance_temporary_unavailable", "instance_cooldown":
		return "temporarily_unavailable", "restore_account"
	case "instance_quality_isolated":
		return "error", "retry_check"
	case "binding_missing":
		return "binding_missing", "bind_group"
	case "api_key_pool_unavailable":
		return "key_pool_unavailable", "retry_check"
	case "runtime_degraded":
		return "degraded", "restore_account"
	case "runtime_local_suppressed":
		return "avoided", "restore_account"
	case "runtime_half_open":
		return "verifying", "none"
	case "runtime_precheck_pending":
		return "verifying", "restore_account"
	case "runtime_precheck_failed":
		return "verification_failed", "retry_check"
	}
	return status, "none"
}

// probeFactsPayload 对齐 presentation 的 probe 分支（hasProbeFact 门控；
// runtime_* probe 事实由 runtimeAvailability.probePresentation 承载，不再
// 重复入 presentation.probe——Node 在该分支仅复用同一事实对象）。
func probeFactsPayload(availability *effectiveAvailability, row managementRow, now time.Time) map[string]any {
	if availability == nil {
		return nil
	}
	switch {
	case availability.status == "api_key_pool_unavailable":
		return nil
	case strings.HasPrefix(availability.status, "runtime_"):
		return nil
	case strings.HasPrefix(availability.status, "source_"):
		return sourceProbePayload(availability.status, row, now)
	case availability.status == "instance_pending_test":
		lastObservation := healthObservationPayload(row)
		if lastObservation == nil && row.nextHealthCheckAt.String == "" {
			return nil
		}
		return probeSummary("activation_check", lastObservation, schedulePayloadState(row.nextHealthCheckAt.String, now))
	}
	return nil
}

// sourceProbePayload 对齐 sourceAccountProbe 派生。
func sourceProbePayload(status string, row managementRow, now time.Time) map[string]any {
	cooldownDriven := status == "source_rate_limited" || status == "source_temporary_unavailable" ||
		(status == "source_error" && strings.HasPrefix(row.sourceLastErrorCode.String, "cooldown_retest_"))
	attemptedAt := row.sourceLastHealthCheckAt.String
	errorCode := row.sourceLastHealthCheckErrorCode.String
	reason := diagnosticText(row.sourceLastHealthCheckErrorMessage.String)
	traceID := row.sourceLastHealthCheckTraceID.String
	httpStatus := int64(0)
	if row.sourceLastHealthCheckStatusCode.Valid {
		httpStatus = row.sourceLastHealthCheckStatusCode.Int64
	}
	if cooldownDriven {
		attemptedAt = row.sourceCooldownRetestLastAt.String
		errorCode = row.sourceLastErrorCode.String
		reason = diagnosticText(row.sourceLastErrorMessage.String)
		traceID = row.sourceLastErrorTraceID.String
		if row.sourceCooldownRetestLastStatusCode.Valid {
			httpStatus = row.sourceCooldownRetestLastStatusCode.Int64
		}
	}
	var lastObservation map[string]any
	if attemptedAt != "" {
		failed := errorCode != "" || reason != "" || httpStatus >= 400
		result := "success"
		if failed {
			result = "failed"
		}
		lastObservation = map[string]any{
			"observationId": probeObservationID("source_account_probe",
				orDefault(row.authorizationInstanceSourceID.String, row.id), attemptedAt, traceID, errorCode, reason),
			"attemptedAt": attemptedAt,
			"result":      result,
		}
		if httpStatus > 0 {
			lastObservation["httpStatus"] = httpStatus
		}
		if errorCode != "" {
			lastObservation["errorCode"] = errorCode
		}
		if reason != "" {
			lastObservation["reason"] = reason
		}
		if traceID != "" {
			lastObservation["traceId"] = traceID
		}
	}
	next := row.sourceNextHealthCheckAt.String
	if cooldownDriven {
		next = row.sourceCooldownUntil.String
	}
	schedule := schedulePayloadState(next, now)
	if lastObservation == nil && schedule["state"] == "none" {
		return nil
	}
	return probeSummary("source_account_probe", lastObservation, schedule)
}

func healthObservationPayload(row managementRow) map[string]any {
	if row.lastHealthCheckAt.String == "" {
		return nil
	}
	failed := row.lastHealthCheckErrorCode.String != "" || row.lastHealthCheckErrorMessage.String != "" ||
		(row.lastHealthCheckStatusCode.Valid && row.lastHealthCheckStatusCode.Int64 >= 400)
	result := "success"
	if failed {
		result = "failed"
	}
	observation := map[string]any{
		"observationId": probeObservationID("health_check", row.id, row.lastHealthCheckAt.String,
			row.lastHealthCheckTraceID.String, row.lastHealthCheckErrorCode.String, row.lastHealthCheckErrorMessage.String),
		"attemptedAt": row.lastHealthCheckAt.String,
		"result":      result,
	}
	if row.lastHealthCheckStatusCode.Valid && row.lastHealthCheckStatusCode.Int64 > 0 {
		observation["httpStatus"] = row.lastHealthCheckStatusCode.Int64
	}
	if row.lastHealthCheckErrorCode.String != "" {
		observation["errorCode"] = row.lastHealthCheckErrorCode.String
	}
	if row.lastHealthCheckErrorMessage.String != "" {
		observation["reason"] = diagnosticText(row.lastHealthCheckErrorMessage.String)
	}
	if row.lastHealthCheckTraceID.String != "" {
		observation["traceId"] = row.lastHealthCheckTraceID.String
	}
	return observation
}

func probeSummary(kind string, lastObservation map[string]any, schedule map[string]any) map[string]any {
	payload := map[string]any{"kind": kind, "schedule": schedule}
	if lastObservation != nil {
		payload["lastObservation"] = lastObservation
	}
	return payload
}

func schedulePayloadState(nextAttemptAt string, now time.Time) map[string]any {
	if nextAttemptAt == "" {
		return map[string]any{"state": "none"}
	}
	timestamp, ok := rfc3339Millis(nextAttemptAt)
	if !ok {
		return map[string]any{"state": "none"}
	}
	state := "scheduled"
	if timestamp <= now.UnixMilli() {
		state = "due_waiting"
	}
	return map[string]any{"state": state, "nextAttemptAt": nextAttemptAt}
}

// probeObservationID 对齐 accountProbeObservationId（sha256 前 24 位 hex）。
func probeObservationID(kind, identity, attemptedAt, traceID, errorCode, reason string) string {
	fingerprint := strings.Join([]string{kind, identity, attemptedAt, traceID, errorCode, reason}, "|")
	return sha256HexShort(fingerprint, 24)
}

// ---- 小工具 ----

func ptrString(value string) *string { return &value }

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func isExpiredInstant(value string, now time.Time) bool {
	if value == "" {
		return false
	}
	timestamp, ok := rfc3339Millis(value)
	if !ok {
		return false
	}
	return timestamp <= now.UnixMilli()
}

func isFutureInstant(value string, now time.Time) bool {
	if value == "" {
		return false
	}
	timestamp, ok := rfc3339Millis(value)
	if !ok {
		return false
	}
	return timestamp > now.UnixMilli()
}

func rfc3339Millis(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

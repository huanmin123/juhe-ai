package accounts

// list_availability_presentation.go — 账户管理列表的 availabilityPresentation
// 投影（BUG-0278 缺陷修复批次二）：进程内纯合成，无 IO、无失败降级路径。
// 逐函数对照 jobs internal/circuitstore/listavailability_effective.go:283-526
// 的 presentation 面移植（presentationPayload / presentationMapping /
// probeFactsPayload / healthObservationPayload / schedulePayloadState /
// probeObservationID），映射词汇与分支取舍保持一致。
//
// 与 jobs 的范围差异（取证裁决，详见 docs/bug/问题-0278-账户列表运行态overlay缺失.md）：
//   - sourceProbePayload（source_* 探针分支）不移植：gateway 第一批 effective
//     合成（list.go ownerEffectiveAvailability + list_runtime_overlay.go
//     applyRuntimeOverlayAvailability）只产出 instance_* / api_key_pool_unavailable
//     / runtime_* / available 状态，从不产出 source_*（授权实例行的来源臂
//     blocker 不在列表状态机内），source 探针输入列（source_last_health_check_*
//     等）也不在本批扩列清单内；
//   - statusBoundary 只保留 gateway 可达分支：instance_expired（account_expires_at）、
//     instance_cooldown（cooldown_until）。jobs 的 runtime_local_suppressed →
//     policy_ttl_expiry 边界需要 runtime recoveryAt 输入（jobs 从
//     ProbePresentation["recoveryAt"] 取），gateway 批次一 DTO
//     （AccountRuntimeAvailabilityPublic）不含 probePresentation，该分支在
//     gateway 输入面下不触发；authorization_*/source_*/quota 分支状态不产出；
//   - 不发 runtimeAvailability.probePresentation 键（前端零消费，批次一纪律
//     保持）；探针 tooltip 的 lastObservation/traceId 唯一来源是 accounts 表
//     健康列，probe store 批量快照端口移出本批（probe store 无 observation 且
//     gateway-account-recovery 在 go-only 栈零写入方，属独立存量缺口）。

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// AccountAvailabilityPresentation mirrors AccountAvailabilityPresentation
// （frontend/src/types/domain/accounts.ts:213-223）：status/label/action 必填，
// reason/statusBoundary/probe 按分支产出；statusBoundary 与 probe 互斥
// （jobs presentationPayload：boundary 命中即返回，不再看 probe）。
type AccountAvailabilityPresentation struct {
	Status         string                       `json:"status"`
	Label          string                       `json:"label"`
	Action         string                       `json:"action"`
	Reason         string                       `json:"reason,omitempty"`
	StatusBoundary *AccountStatusBoundaryPublic `json:"statusBoundary,omitempty"`
	Probe          *AccountProbeSummaryPublic   `json:"probe,omitempty"`
}

// AccountStatusBoundaryPublic mirrors statusBoundary（at/kind 必填）。
type AccountStatusBoundaryPublic struct {
	At   string `json:"at"`
	Kind string `json:"kind"`
}

// AccountProbeSummaryPublic mirrors AccountProbeSummary（前端
// accounts.ts:204-211）：kind 必填，schedule 必填，lastObservation 可省略。
type AccountProbeSummaryPublic struct {
	Kind            string                         `json:"kind"`
	LastObservation *AccountProbeObservationPublic `json:"lastObservation,omitempty"`
	Schedule        AccountProbeSchedulePublic     `json:"schedule"`
}

// AccountProbeObservationPublic mirrors AccountProbeObservation（前端
// accounts.ts:194-202）：observationId/attemptedAt/result 必填，
// httpStatus 仅 number，其余按值产出。
type AccountProbeObservationPublic struct {
	ObservationID string `json:"observationId"`
	AttemptedAt   string `json:"attemptedAt"`
	Result        string `json:"result"` // success | failed
	HTTPStatus    int64  `json:"httpStatus,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
	Reason        string `json:"reason,omitempty"`
	TraceID       string `json:"traceId,omitempty"`
}

// AccountProbeSchedulePublic mirrors AccountProbeSummary.schedule
// （jobs schedulePayloadState 只产出 scheduled/due_waiting/none）。
type AccountProbeSchedulePublic struct {
	State         string  `json:"state"`
	NextAttemptAt *string `json:"nextAttemptAt,omitempty"`
}

// buildAvailabilityPresentation 对齐 jobs presentationPayload：输入为行健康
// 列与最终 effectiveAvailability 状态（在全部 hydrate——含批次一 runtime
// overlay 的 effective 重算——之后调用）。jobs 对非 nil availability 恒产出，
// gateway 的 EffectiveAvailability 是值类型，故恒返回非 nil。
func buildAvailabilityPresentation(item ListItem, row listRow, now time.Time) AccountAvailabilityPresentation {
	availability := item.EffectiveAvailability
	status, action := presentationMapping(availability.Status)
	presentation := AccountAvailabilityPresentation{
		Status: status,
		Label:  availability.Label,
		Action: action,
	}
	if availability.Reason != nil && *availability.Reason != "" {
		presentation.Reason = *availability.Reason
	}
	// instance_error 的两个动作覆写（jobs presentationPayload 原样：超时检查
	// 引导重试、冷却复测错误码引导恢复账户）。
	if availability.Status == "instance_error" && row.lastErrorCode.String == "account_activation_check_timeout" {
		presentation.Action = "retry_check"
	} else if availability.Status == "instance_error" && strings.HasPrefix(row.lastErrorCode.String, "cooldown_retest_") {
		presentation.Action = "restore_account"
	}
	if boundary := availabilityStatusBoundaryAt(availability.Status, row); boundary != "" {
		presentation.StatusBoundary = &AccountStatusBoundaryPublic{At: boundary, Kind: boundaryKindOf(availability.Status)}
		return presentation
	}
	presentation.Probe = availabilityProbeFacts(availability.Status, row, now)
	return presentation
}

// presentationMapping 对齐 jobs presentationMapping（词汇逐分支一致）。表中的
// permission/authorization/source/binding 词汇在 gateway 列表状态机下当前不可
// 达，保留以锁定完整映射契约（jobs 同表全量）。
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

// boundaryKindOf 对齐 jobs boundaryKindOf。
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

// availabilityStatusBoundaryAt 对齐 jobs statusBoundaryAtOf 的 gateway 可达
// 分支：instance_expired ← account_expires_at、instance_cooldown ←
// cooldown_until。runtime_local_suppressed 的 recoveryAt 输入在 gateway 不存
// 在（批次一 DTO 无 probePresentation），该分支恒空；其余 jobs 分支对应
// gateway 不产出的状态（文件头差异说明）。
func availabilityStatusBoundaryAt(status string, row listRow) string {
	switch status {
	case "instance_expired":
		if row.accountExpiresAt.Valid {
			return row.accountExpiresAt.String
		}
	case "instance_cooldown":
		if row.cooldownUntil.Valid {
			return row.cooldownUntil.String
		}
	}
	return ""
}

// availabilityProbeFacts 对齐 jobs probeFactsPayload 的 hasProbeFact 门控：
// api_key_pool_unavailable 与 runtime_* 不产 probe（runtime 事实由
// runtimeAvailability 承载，Node 在该分支仅复用同一事实对象且前端零消费
// probePresentation）；source_* 分支不移植（文件头差异说明）；唯一产出探针的
// 可达状态是 instance_pending_test（activation_check）。
func availabilityProbeFacts(status string, row listRow, now time.Time) *AccountProbeSummaryPublic {
	switch {
	case status == "api_key_pool_unavailable":
		return nil
	case strings.HasPrefix(status, "runtime_"):
		return nil
	case strings.HasPrefix(status, "source_"):
		return nil
	case status == "instance_pending_test":
		lastObservation := healthObservationPayload(row)
		if lastObservation == nil && row.nextHealthCheckAt.String == "" {
			return nil
		}
		return probeSummary("activation_check", lastObservation, schedulePayloadState(row.nextHealthCheckAt.String, now))
	}
	return nil
}

// healthObservationPayload 对齐 jobs healthObservationPayload：observationId
// 指纹 kind 固定 "health_check"、identity=账户 id（与 jobs 同源语义）。
func healthObservationPayload(row listRow) *AccountProbeObservationPublic {
	if row.lastHealthCheckAt.String == "" {
		return nil
	}
	failed := row.lastHealthCheckErrorCode.String != "" || row.lastHealthCheckErrorMessage.String != "" ||
		(row.lastHealthCheckStatusCode.Valid && row.lastHealthCheckStatusCode.Int64 >= 400)
	result := "success"
	if failed {
		result = "failed"
	}
	observation := &AccountProbeObservationPublic{
		ObservationID: probeObservationID("health_check", row.id, row.lastHealthCheckAt.String,
			row.lastHealthCheckTraceID.String, row.lastHealthCheckErrorCode.String, row.lastHealthCheckErrorMessage.String),
		AttemptedAt: row.lastHealthCheckAt.String,
		Result:      result,
	}
	if row.lastHealthCheckStatusCode.Valid && row.lastHealthCheckStatusCode.Int64 > 0 {
		observation.HTTPStatus = row.lastHealthCheckStatusCode.Int64
	}
	if row.lastHealthCheckErrorCode.String != "" {
		observation.ErrorCode = row.lastHealthCheckErrorCode.String
	}
	if row.lastHealthCheckErrorMessage.String != "" {
		observation.Reason = diagnosticText(row.lastHealthCheckErrorMessage.String)
	}
	if row.lastHealthCheckTraceID.String != "" {
		observation.TraceID = row.lastHealthCheckTraceID.String
	}
	return observation
}

// probeSummary 对齐 jobs probeSummary：lastObservation 缺席时省略键。
func probeSummary(kind string, lastObservation *AccountProbeObservationPublic, schedule AccountProbeSchedulePublic) *AccountProbeSummaryPublic {
	return &AccountProbeSummaryPublic{Kind: kind, LastObservation: lastObservation, Schedule: schedule}
}

// schedulePayloadState 对齐 jobs schedulePayloadState：空/不可解析 → none，
// 已到期 → due_waiting（保留原计划时间），未来 → scheduled。
func schedulePayloadState(nextAttemptAt string, now time.Time) AccountProbeSchedulePublic {
	if nextAttemptAt == "" {
		return AccountProbeSchedulePublic{State: "none"}
	}
	timestamp, ok := rfc3339Millis(nextAttemptAt)
	if !ok {
		return AccountProbeSchedulePublic{State: "none"}
	}
	state := "scheduled"
	if timestamp <= now.UnixMilli() {
		state = "due_waiting"
	}
	return AccountProbeSchedulePublic{State: state, NextAttemptAt: &nextAttemptAt}
}

// probeObservationID 对齐 jobs probeObservationID（Node
// accountProbeObservationId：sha256 前 24 位 hex）。
func probeObservationID(kind, identity, attemptedAt, traceID, errorCode, reason string) string {
	fingerprint := strings.Join([]string{kind, identity, attemptedAt, traceID, errorCode, reason}, "|")
	return sha256HexShort(fingerprint, 24)
}

func sha256HexShort(input string, length int) string {
	digest := sha256.Sum256([]byte(input))
	return hex.EncodeToString(digest[:])[:length]
}

// diagnosticText 对齐 jobs diagnosticText（Node accountListDiagnosticText：
// 96 字符截断，省略空白）。
func diagnosticText(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	if len(text) <= 96 {
		return text
	}
	return text[:95] + "…"
}

// rfc3339Millis 对齐 jobs rfc3339Millis（RFC3339Nano 解析失败返回 ok=false）。
func rfc3339Millis(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// hydrateAvailabilityPresentation 在既有 hydrate（含批次一 runtime overlay 的
// effective 重算）之后统一合成 availabilityPresentation：输入=行内 accounts
// 健康列 + 该行最终 effectiveAvailability 状态，items 与 records 页内一一对齐。
// 纯进程内合成，无端口、无失败降级路径，每行恒产出。仅列表面涉及：jobs 语义
// 的详情 presentation 在 gateway 无对应面（FindEditBasicDetail 是编辑凭据面，
// 不携带可用性）。runtimeAvailability 不发 probePresentation 键（文件头差异
// 说明）。
func (s *Store) hydrateAvailabilityPresentation(items []ListItem, records []listRow) {
	if len(items) == 0 {
		return
	}
	now := s.now()
	count := len(items)
	if len(records) < count {
		count = len(records)
	}
	for index := 0; index < count; index++ {
		presentation := buildAvailabilityPresentation(items[index], records[index], now)
		items[index].AvailabilityPresentation = &presentation
	}
}

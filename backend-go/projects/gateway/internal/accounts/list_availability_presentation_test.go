package accounts

// 账户列表 availabilityPresentation 投影回归（BUG-0278 缺陷修复批次二，修复前
// 必红）：管理面/用户面列表此前不返回 availabilityPresentation，前端状态卡片
// tooltip（最近检查/HTTP 状态/错误码/traceId/下次检查）不可见。本文件锁定：
//   - 合成纯函数分支（healthObservation 成功/失败/HTTP 状态/错误码/traceId、
//     schedule none/scheduled/due_waiting、label/reason 优先级、
//     runtime_*/api_key_pool_unavailable 不产 probe、observationId 派生稳定）；
//   - 列表端到端（扩列后 ListPage 返回 availabilityPresentation、缺列行降级、
//     合成发生在批次一 effective 重算之后）。
//
// 守卫形态：下列用例直接引用 buildAvailabilityPresentation /
// availabilityProbeFacts / healthObservationPayload / schedulePayloadState /
// probeObservationID / presentationMapping / availabilityStatusBoundaryAt 与
// ListItem.AvailabilityPresentation——合成函数不存在时无法编译，字段缺失时端
// 到端断言必红。禁止 go test 的批次约束下由主代理统一执行。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

func presentationRowFor(id string) listRow {
	return listRow{id: id}
}

// nullString / nullInt64 construct the nullable scan cells（包内既有
// nullStringValue/nullInt64Value 是 PATCH 绑定方向，名字不冲突）。
func nullString(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }

func nullInt64(value int64) sql.NullInt64 { return sql.NullInt64{Int64: value, Valid: true} }

// TestPresentationMappingVocabulary locks the status→(status,action) mapping
// vocabulary（jobs presentationMapping 全表；含 gateway 当前不可达词汇）。
func TestPresentationMappingVocabulary(t *testing.T) {
	cases := []struct {
		input  string
		status string
		action string
	}{
		{"available", "available", "none"},
		{"instance_pending_test", "pending_check", "retry_check"},
		{"instance_error", "error", "fix_configuration"},
		{"instance_rate_limited", "rate_limited", "restore_account"},
		{"instance_temporary_unavailable", "temporarily_unavailable", "restore_account"},
		{"instance_cooldown", "temporarily_unavailable", "restore_account"},
		{"instance_quality_isolated", "error", "retry_check"},
		{"instance_disabled", "disabled", "enable_account"},
		{"instance_unschedulable", "disabled", "enable_account"},
		{"instance_expired", "expired", "renew_authorization"},
		{"api_key_pool_unavailable", "key_pool_unavailable", "retry_check"},
		{"runtime_degraded", "degraded", "restore_account"},
		{"runtime_local_suppressed", "avoided", "restore_account"},
		{"runtime_half_open", "verifying", "none"},
		{"runtime_precheck_pending", "verifying", "restore_account"},
		{"runtime_precheck_failed", "verification_failed", "retry_check"},
		{"source_pending_test", "pending_check", "contact_authorizer"},
		{"authorization_expired", "expired", "renew_authorization"},
		{"binding_missing", "binding_missing", "bind_group"},
	}
	for _, testCase := range cases {
		status, action := presentationMapping(testCase.input)
		if status != testCase.status || action != testCase.action {
			t.Fatalf("presentationMapping(%q) = (%q,%q)，应为 (%q,%q)",
				testCase.input, status, action, testCase.status, testCase.action)
		}
	}
	// 未知状态透传 + action none（jobs 同款兜底）。
	if status, action := presentationMapping("unknown_status"); status != "unknown_status" || action != "none" {
		t.Fatalf("未知状态应透传：( %q,%q)", status, action)
	}
}

// TestHealthObservationPayloadBranches locks the observation derivation:
// 成功/失败判定顺序（errorCode/errorMessage/httpStatus>=400 任一即 failed）、
// httpStatus 仅在正数时输出、reason 走 96 字符截断、traceId 透传。
func TestHealthObservationPayloadBranches(t *testing.T) {
	if observation := healthObservationPayload(presentationRowFor("acc-none")); observation != nil {
		t.Fatalf("无 last_health_check_at 应返回 nil：%v", observation)
	}

	successRow := presentationRowFor("acc-ok")
	successRow.lastHealthCheckAt = nullString("2026-10-03T00:00:00.000Z")
	successRow.lastHealthCheckStatusCode = nullInt64(200)
	success := healthObservationPayload(successRow)
	if success == nil || success.Result != "success" || success.HTTPStatus != 200 ||
		success.ErrorCode != "" || success.Reason != "" || success.TraceID != "" {
		t.Fatalf("成功 observation 形状不符：%v", success)
	}

	errorCodeRow := presentationRowFor("acc-err")
	errorCodeRow.lastHealthCheckAt = nullString("2026-10-03T00:01:00.000Z")
	errorCodeRow.lastHealthCheckErrorCode = nullString("upstream_5xx")
	errorCodeRow.lastHealthCheckTraceID = nullString("trace-err")
	failedByCode := healthObservationPayload(errorCodeRow)
	if failedByCode == nil || failedByCode.Result != "failed" || failedByCode.ErrorCode != "upstream_5xx" ||
		failedByCode.TraceID != "trace-err" || failedByCode.HTTPStatus != 0 {
		t.Fatalf("errorCode 失败 observation 形状不符：%v", failedByCode)
	}

	statusCodeRow := presentationRowFor("acc-502")
	statusCodeRow.lastHealthCheckAt = nullString("2026-10-03T00:02:00.000Z")
	statusCodeRow.lastHealthCheckStatusCode = nullInt64(502)
	failedByStatus := healthObservationPayload(statusCodeRow)
	if failedByStatus == nil || failedByStatus.Result != "failed" || failedByStatus.HTTPStatus != 502 {
		t.Fatalf("httpStatus>=400 应判失败：%v", failedByStatus)
	}

	// NULL status_code 不输出 httpStatus（仅 number 且 >0 才带）。
	nullStatusRow := presentationRowFor("acc-null-code")
	nullStatusRow.lastHealthCheckAt = nullString("2026-10-03T00:03:00.000Z")
	nullCodeObservation := healthObservationPayload(nullStatusRow)
	if nullCodeObservation == nil || nullCodeObservation.Result != "success" {
		t.Fatalf("NULL status_code 应 success 且无 httpStatus：%v", nullCodeObservation)
	}

	// reason 走 diagnosticText（96 字节截断，jobs 同款字节语义：len(text)<=96
	// 原样，否则 text[:95]+"…"——按字节而非 rune）。
	longMessage := strings.Repeat("a", 120)
	truncatedRow := presentationRowFor("acc-long")
	truncatedRow.lastHealthCheckAt = nullString("2026-10-03T00:04:00.000Z")
	truncatedRow.lastHealthCheckErrorMessage = nullString(longMessage)
	truncated := healthObservationPayload(truncatedRow)
	if truncated == nil || truncated.Reason != strings.Repeat("a", 95)+"…" {
		t.Fatalf("reason 应 96 字节截断：%q", truncated.Reason)
	}
	// CJK 输入（每字 3 字节）在字节语义下 96 字节=32 字整+第 33 字前 2 字节，
	// 断言与 jobs text[:95] 切片逐字节一致。
	cjkRow := presentationRowFor("acc-long-cjk")
	cjkRow.lastHealthCheckAt = nullString("2026-10-03T00:05:00.000Z")
	cjkRow.lastHealthCheckErrorMessage = nullString(strings.Repeat("长", 120))
	cjkTruncated := healthObservationPayload(cjkRow)
	if cjkTruncated == nil || cjkTruncated.Reason != textSlice95(strings.Repeat("长", 120))+"…" {
		t.Fatalf("CJK reason 应与 jobs 字节切片一致：%q", cjkTruncated.Reason)
	}
}

// textSlice95 复刻 jobs diagnosticText 的 text[:95] 字节切片（测试内不直接
// 依赖被测函数的截断实现）。
func textSlice95(value string) string { return value[:95] }

// TestProbeObservationIDStable locks the observationId derivation（sha256 前
// 24 位 hex，输入同源稳定、任一字段变化即变化）。
func TestProbeObservationIDStable(t *testing.T) {
	first := probeObservationID("health_check", "acc-1", "2026-10-03T00:00:00.000Z", "trace-1", "upstream_5xx", "上游 5xx")
	second := probeObservationID("health_check", "acc-1", "2026-10-03T00:00:00.000Z", "trace-1", "upstream_5xx", "上游 5xx")
	if first != second {
		t.Fatalf("observationId 应稳定：%q != %q", first, second)
	}
	if len(first) != 24 {
		t.Fatalf("observationId 应为 24 位：%q", first)
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("observationId 应为 hex：%q", first)
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"health_check", "acc-1", "2026-10-03T00:00:00.000Z", "trace-1", "upstream_5xx", "上游 5xx",
	}, "|")))
	if expected := hex.EncodeToString(digest[:])[:24]; first != expected {
		t.Fatalf("observationId 指纹不符：%q != %q", first, expected)
	}
	if changed := probeObservationID("health_check", "acc-2", "2026-10-03T00:00:00.000Z", "trace-1", "upstream_5xx", "上游 5xx"); changed == first {
		t.Fatalf("identity 变化应改变 observationId")
	}
}

// TestSchedulePayloadStateBranches locks none/scheduled/due_waiting 与不可解析
// 回退 none（jobs schedulePayloadState 同款）。
func TestSchedulePayloadStateBranches(t *testing.T) {
	now := time.Now()
	if state := schedulePayloadState("", now); state.State != "none" || state.NextAttemptAt != nil {
		t.Fatalf("空 next 应 none：%v", state)
	}
	if state := schedulePayloadState("not-a-time", now); state.State != "none" {
		t.Fatalf("不可解析应 none：%v", state)
	}
	future := now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if state := schedulePayloadState(future, now); state.State != "scheduled" || state.NextAttemptAt == nil || *state.NextAttemptAt != future {
		t.Fatalf("未来应 scheduled：%v", state)
	}
	past := now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	state := schedulePayloadState(past, now)
	if state.State != "due_waiting" || state.NextAttemptAt == nil || *state.NextAttemptAt != past {
		t.Fatalf("已到期应 due_waiting 且保留原计划：%v", state)
	}
}

// TestAvailabilityProbeFactsGating locks the hasProbeFact gate：
// 仅 instance_pending_test 产出 activation_check 探针；runtime_* /
// api_key_pool_unavailable 不产 probe（jobs 同款门控），source_* 恒 nil
// （gateway 不产出该状态族，分支不移植）。
func TestAvailabilityProbeFactsGating(t *testing.T) {
	now := time.Now()
	row := presentationRowFor("acc-gate")
	row.lastHealthCheckAt = nullString("2026-10-03T00:00:00.000Z")
	row.nextHealthCheckAt = nullString(now.Add(time.Hour).UTC().Format(time.RFC3339Nano))

	pending := availabilityProbeFacts("instance_pending_test", row, now)
	if pending == nil || pending.Kind != "activation_check" || pending.LastObservation == nil ||
		pending.Schedule.State != "scheduled" {
		t.Fatalf("instance_pending_test 应产 activation_check 探针：%v", pending)
	}

	observationOnlyRow := presentationRowFor("acc-obs-only")
	observationOnlyRow.lastHealthCheckAt = nullString("2026-10-03T00:00:00.000Z")
	observationOnly := availabilityProbeFacts("instance_pending_test", observationOnlyRow, now)
	if observationOnly == nil || observationOnly.LastObservation == nil || observationOnly.Schedule.State != "none" {
		t.Fatalf("仅有 observation 无 next 应产 schedule=none 探针：%v", observationOnly)
	}

	scheduleOnlyRow := presentationRowFor("acc-sched-only")
	scheduleOnlyRow.nextHealthCheckAt = nullString(now.Add(time.Hour).UTC().Format(time.RFC3339Nano))
	scheduleOnly := availabilityProbeFacts("instance_pending_test", scheduleOnlyRow, now)
	if scheduleOnly == nil || scheduleOnly.LastObservation != nil || scheduleOnly.Schedule.State != "scheduled" {
		t.Fatalf("仅有 next 应产无 lastObservation 探针：%v", scheduleOnly)
	}

	if neither := availabilityProbeFacts("instance_pending_test", presentationRowFor("acc-empty"), now); neither != nil {
		t.Fatalf("observation 与 next 均缺应不产探针（缺列行降级）：%v", neither)
	}

	for _, status := range []string{"api_key_pool_unavailable", "runtime_half_open", "runtime_degraded",
		"runtime_precheck_failed", "instance_error", "available", "source_error", "source_pending_test"} {
		if probe := availabilityProbeFacts(status, row, now); probe != nil {
			t.Fatalf("%s 不应产 probe：%v", status, probe)
		}
	}
}

// TestAvailabilityStatusBoundaryAt locks the boundary sources：instance_expired
// ← account_expires_at、instance_cooldown ← cooldown_until；其余状态无边界。
func TestAvailabilityStatusBoundaryAt(t *testing.T) {
	row := presentationRowFor("acc-boundary")
	row.accountExpiresAt = nullString("2026-10-03T08:00:00.000Z")
	row.cooldownUntil = nullString("2026-10-03T09:00:00.000Z")
	if boundary := availabilityStatusBoundaryAt("instance_expired", row); boundary != "2026-10-03T08:00:00.000Z" {
		t.Fatalf("instance_expired 边界应取 account_expires_at：%q", boundary)
	}
	if boundary := availabilityStatusBoundaryAt("instance_cooldown", row); boundary != "2026-10-03T09:00:00.000Z" {
		t.Fatalf("instance_cooldown 边界应取 cooldown_until：%q", boundary)
	}
	if boundary := availabilityStatusBoundaryAt("instance_cooldown", presentationRowFor("acc-no-cols")); boundary != "" {
		t.Fatalf("缺列行应无边界：%q", boundary)
	}
	if boundary := availabilityStatusBoundaryAt("runtime_local_suppressed", row); boundary != "" {
		t.Fatalf("runtime recoveryAt 输入在 gateway 不存在，应无边界：%q", boundary)
	}
	if kind := boundaryKindOf("instance_expired"); kind != "account_expired" {
		t.Fatalf("instance_expired 边界 kind：%q", kind)
	}
	if kind := boundaryKindOf("instance_cooldown"); kind != "cooldown_expiry" {
		t.Fatalf("instance_cooldown 边界 kind：%q", kind)
	}
}

// TestBuildAvailabilityPresentationReasonActionBoundary locks the payload
// assembly：label/reason 取 effective 原文（reason 空则省略）、instance_error
// 动作覆写、boundary 与 probe 的结构互斥（boundary 命中即不再看 probe）。
func TestBuildAvailabilityPresentationReasonActionBoundary(t *testing.T) {
	now := time.Now()

	// available：action none、reason 省略、无 boundary/probe。
	available := buildAvailabilityPresentation(ListItem{
		ID:     "acc-avail",
		Status: "active",
		EffectiveAvailability: EffectiveAvailability{
			Available: true, Status: "available", Label: "可调度", Color: "green",
		},
	}, presentationRowFor("acc-avail"), now)
	if available.Status != "available" || available.Action != "none" || available.Label != "可调度" ||
		available.Reason != "" || available.StatusBoundary != nil || available.Probe != nil {
		t.Fatalf("available presentation 形状不符：%+v", available)
	}

	// instance_error：reason 透传 + 动作覆写两分支。
	errorRow := presentationRowFor("acc-err")
	timeoutItem := ListItem{EffectiveAvailability: EffectiveAvailability{
		Available: false, Status: "instance_error", Label: "账户异常", Color: "red",
		Reason: strPtr("上游连接超时"),
	}}
	timeout := buildAvailabilityPresentation(timeoutItem, func() listRow {
		row := errorRow
		row.lastErrorCode = nullString("account_activation_check_timeout")
		return row
	}(), now)
	if timeout.Action != "retry_check" || timeout.Reason != "上游连接超时" {
		t.Fatalf("超时覆写应 retry_check：%+v", timeout)
	}
	cooldownError := buildAvailabilityPresentation(timeoutItem, func() listRow {
		row := errorRow
		row.lastErrorCode = nullString("cooldown_retest_w1")
		return row
	}(), now)
	if cooldownError.Action != "restore_account" {
		t.Fatalf("cooldown_retest_* 覆写应 restore_account：%+v", cooldownError)
	}
	plainError := buildAvailabilityPresentation(timeoutItem, func() listRow {
		row := errorRow
		row.lastErrorCode = nullString("other_error")
		return row
	}(), now)
	if plainError.Action != "fix_configuration" {
		t.Fatalf("其余 instance_error 保持 fix_configuration：%+v", plainError)
	}

	// instance_cooldown：boundary 命中且无 probe（互斥）。
	cooldownRow := presentationRowFor("acc-cool")
	cooldownRow.cooldownUntil = nullString("2026-10-03T09:00:00.000Z")
	cooldown := buildAvailabilityPresentation(ListItem{EffectiveAvailability: EffectiveAvailability{
		Available: false, Status: "instance_cooldown", Label: "账户冷却", Color: "gold",
	}}, cooldownRow, now)
	if cooldown.StatusBoundary == nil || cooldown.StatusBoundary.At != "2026-10-03T09:00:00.000Z" ||
		cooldown.StatusBoundary.Kind != "cooldown_expiry" || cooldown.Probe != nil {
		t.Fatalf("cooldown 应产 statusBoundary 且无 probe：%+v", cooldown)
	}
}

// TestListPageHydratesAvailabilityPresentation locks the end-to-end wiring:
// 扩列后 ListPage 返回 availabilityPresentation（管理面），缺列行降级为无
// probe，runtime 分支行合成发生在批次一 effective 重算之后，且
// runtimeAvailability 不携带 probePresentation 键。
//
// 修复前必红：ListItem 无 availabilityPresentation 键（JSON 缺键）。
func TestListPageHydratesAvailabilityPresentation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-ap-1", adminID, "ap-1", "pending_test")
	env.seedAccount(t, "acc-ap-2", adminID, "ap-2", "active")
	env.seedAccount(t, "acc-ap-3", adminID, "ap-3", "pending_test")
	env.seedAccount(t, "acc-ap-4", adminID, "ap-4", "pending_test")
	env.seedAccount(t, "acc-ap-5", adminID, "ap-5", "active")

	now := time.Now().UTC()
	attemptedAt := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	nextFuture := now.Add(time.Hour).Format(time.RFC3339Nano)
	nextPast := now.Add(-time.Minute).Format(time.RFC3339Nano)
	// acc-ap-1：完整健康行（失败 observation + 未来计划）。
	env.exec(t, `UPDATE accounts SET last_health_check_at = ?, next_health_check_at = ?,
		last_health_check_status_code = 502, last_health_check_error_code = 'upstream_5xx',
		last_health_check_error_message = '上游 5xx', last_health_check_trace_id = 'trace-ap-1',
		cooldown_retest_last_at = ?, cooldown_retest_last_status_code = 502
		WHERE id = 'acc-ap-1'`, attemptedAt, nextFuture, attemptedAt)
	// acc-ap-3：全健康列 NULL（缺列行降级）。
	// acc-ap-4：仅 next_health_check_at 已到期（due_waiting）。
	env.exec(t, `UPDATE accounts SET next_health_check_at = ? WHERE id = 'acc-ap-4'`, nextPast)

	env.store.SetRuntimeAvailabilitySource(&fakeRuntimeAvailabilitySource{byKeys: map[string]AccountRuntimeAvailabilityPublic{
		"acc-ap-5": {Status: "half_open", Reason: "租约半开确认中"},
	}})

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)

	// acc-ap-1：pending_check + activation_check 探针（失败 observation 全字段）。
	presentation, ok := items["acc-ap-1"]["availabilityPresentation"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-1 应返回 availabilityPresentation：%v", items["acc-ap-1"])
	}
	if presentation["status"] != "pending_check" || presentation["action"] != "retry_check" ||
		presentation["label"] != "账户待检查" {
		t.Fatalf("acc-ap-1 presentation 词汇不符：%v", presentation)
	}
	probe, ok := presentation["probe"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-1 应携带 probe：%v", presentation)
	}
	if probe["kind"] != "activation_check" {
		t.Fatalf("probe.kind 应 activation_check：%v", probe)
	}
	observation, ok := probe["lastObservation"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-1 应携带 lastObservation：%v", probe)
	}
	expectedObservationID := sha256HexShort(strings.Join([]string{
		"health_check", "acc-ap-1", attemptedAt, "trace-ap-1", "upstream_5xx", "上游 5xx",
	}, "|"), 24)
	if observation["observationId"] != expectedObservationID {
		t.Fatalf("observationId 应为 jobs 同源指纹：%v != %s", observation["observationId"], expectedObservationID)
	}
	if observation["attemptedAt"] != attemptedAt || observation["result"] != "failed" ||
		observation["httpStatus"] != float64(502) || observation["errorCode"] != "upstream_5xx" ||
		observation["reason"] != "上游 5xx" || observation["traceId"] != "trace-ap-1" {
		t.Fatalf("lastObservation 形状不符：%v", observation)
	}
	schedule, ok := probe["schedule"].(map[string]any)
	if !ok || schedule["state"] != "scheduled" || schedule["nextAttemptAt"] != nextFuture {
		t.Fatalf("schedule 应 scheduled：%v", probe["schedule"])
	}
	if _, hasBoundary := presentation["statusBoundary"]; hasBoundary {
		t.Fatalf("pending_check 不应携带 statusBoundary：%v", presentation)
	}

	// acc-ap-2：active 基线行恒产出，但仅 status/label/action 三键。
	base, ok := items["acc-ap-2"]["availabilityPresentation"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-2 应恒产出 availabilityPresentation：%v", items["acc-ap-2"])
	}
	if base["status"] != "available" || base["action"] != "none" {
		t.Fatalf("acc-ap-2 presentation 应 available/none：%v", base)
	}
	for _, key := range []string{"reason", "statusBoundary", "probe"} {
		if _, exists := base[key]; exists {
			t.Fatalf("acc-ap-2 不应携带 %s：%v", key, base)
		}
	}

	// acc-ap-3：缺列行降级——presentation 存在但不产 probe。
	degraded, ok := items["acc-ap-3"]["availabilityPresentation"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-3 缺列行仍应产出 presentation：%v", items["acc-ap-3"])
	}
	if degraded["status"] != "pending_check" {
		t.Fatalf("acc-ap-3 status 应 pending_check：%v", degraded)
	}
	if _, exists := degraded["probe"]; exists {
		t.Fatalf("acc-ap-3 缺列行不应产 probe：%v", degraded)
	}

	// acc-ap-4：到期计划 → due_waiting（保留原计划时间）。
	dueProbe, ok := items["acc-ap-4"]["availabilityPresentation"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-4 应产出 presentation：%v", items["acc-ap-4"])
	}
	dueProbeObj, ok := dueProbe["probe"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-4 应产 probe（有 next 计划）：%v", dueProbe)
	}
	dueSchedule, ok := dueProbeObj["schedule"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-4 probe 应携带 schedule：%v", dueProbeObj)
	}
	if dueSchedule["state"] != "due_waiting" || dueSchedule["nextAttemptAt"] != nextPast {
		t.Fatalf("acc-ap-4 schedule 应 due_waiting 且保留原计划时间：%v", dueSchedule)
	}

	// acc-ap-5：runtime_half_open → presentation.status=verifying（证明合成在
	// 批次一 effective 重算之后），且不产 probe；runtimeAvailability 不发
	// probePresentation 键（批次纪律保持）。
	runtimeItem := items["acc-ap-5"]
	runtimePresentation, ok := runtimeItem["availabilityPresentation"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-5 应产出 presentation：%v", runtimeItem)
	}
	if runtimePresentation["status"] != "verifying" || runtimePresentation["action"] != "none" {
		t.Fatalf("runtime_half_open 应映射 verifying/none：%v", runtimePresentation)
	}
	if _, exists := runtimePresentation["probe"]; exists {
		t.Fatalf("runtime_* 不应产 probe：%v", runtimePresentation)
	}
	if _, exists := runtimePresentation["reason"]; !exists {
		t.Fatalf("runtime 分支 reason 应透传：%v", runtimePresentation)
	}
	runtimeOverlay, ok := runtimeItem["runtimeAvailability"].(map[string]any)
	if !ok {
		t.Fatalf("acc-ap-5 应携带 runtimeAvailability：%v", runtimeItem)
	}
	if _, hasProbePresentation := runtimeOverlay["probePresentation"]; hasProbePresentation {
		t.Fatalf("runtimeAvailability 不得携带 probePresentation 键：%v", runtimeOverlay)
	}
}

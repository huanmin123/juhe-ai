package circuitstore

// accountEffectiveAvailability 状态机与 presentation payload 的全分支矩阵测试：
// 输入集合 availabilityInput 的每个阻塞分支都必须映射到唯一
// effectiveAvailability（status/label/color/blockerScope/reason/retryAt），
// 分类与 presentation 映射保持与归档 Node 同名函数逐分支一致。

import (
	"testing"
	"time"
)

func effNow() time.Time { return time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC) }

const (
	effPast   = "2020-01-01T00:00:00.000Z"
	effFuture = "2030-01-01T00:00:00.000Z"
)

// baseEffInput 是可调度 owner active 账户的基准输入（所有阻塞分支由各用例覆写）。
func baseEffInput() availabilityInput {
	return availabilityInput{
		accessType:  "owner",
		status:      "active",
		schedulable: true,
	}
}

// TestAccountEffectiveAvailabilityOwnerBranches 覆盖 owner 实例账户的
// instance_* / api_key_pool / runtime_* / available 分支。
func TestAccountEffectiveAvailabilityOwnerBranches(t *testing.T) {
	now := effNow()
	cases := []struct {
		name        string
		mutate      func(*availabilityInput)
		wantStatus  string
		wantScope   string
		wantRetryAt string
		available   bool
	}{
		{name: "available基准", mutate: func(i *availabilityInput) {}, wantStatus: "available", available: true},
		{name: "账户到期时间戳", mutate: func(i *availabilityInput) { i.accountExpiresAt = effPast }, wantStatus: "instance_expired", wantScope: "account"},
		{name: "账户停用", mutate: func(i *availabilityInput) { i.status = "disabled" }, wantStatus: "instance_disabled", wantScope: "account"},
		{name: "待检查无失败痕迹", mutate: func(i *availabilityInput) { i.status = "pending_test" }, wantStatus: "instance_pending_test", wantScope: "account"},
		{name: "待检查有失败痕迹", mutate: func(i *availabilityInput) {
			i.status = "pending_test"
			i.lastHealthCheckAt = effPast
			i.lastHealthCheckErrorCode = "http_502"
		}, wantStatus: "instance_pending_test", wantScope: "account"},
		{name: "账户异常", mutate: func(i *availabilityInput) { i.status = "error" }, wantStatus: "instance_error", wantScope: "account"},
		{name: "账户限流", mutate: func(i *availabilityInput) { i.status = "rate_limited" }, wantStatus: "instance_rate_limited", wantScope: "account"},
		{name: "账户临时不可调用", mutate: func(i *availabilityInput) { i.status = "temporary_unavailable" }, wantStatus: "instance_temporary_unavailable", wantScope: "account"},
		{name: "账户质量隔离", mutate: func(i *availabilityInput) { i.status = "quality_isolated" }, wantStatus: "instance_quality_isolated", wantScope: "account"},
		{name: "冷却未来边界", mutate: func(i *availabilityInput) { i.cooldownUntil = effFuture }, wantStatus: "instance_cooldown", wantScope: "account", wantRetryAt: effFuture},
		{name: "冷却过期不阻塞", mutate: func(i *availabilityInput) { i.cooldownUntil = effPast }, wantStatus: "available", available: true},
		{name: "停调", mutate: func(i *availabilityInput) { i.schedulable = false }, wantStatus: "instance_unschedulable", wantScope: "account"},
		{name: "key池全不可用", mutate: func(i *availabilityInput) {
			i.apiKeyAllUnavailable = true
			i.apiKeyTotal = 3
			i.apiKeyNextProbeAt = effFuture
		}, wantStatus: "api_key_pool_unavailable", wantScope: "api_key_pool", wantRetryAt: effFuture},
		{name: "运行态降级仍可用", mutate: func(i *availabilityInput) { i.runtimeStatus = "degraded" }, wantStatus: "runtime_degraded", wantScope: "runtime", available: true},
		{name: "运行态事前确认", mutate: func(i *availabilityInput) { i.runtimeStatus = "precheck_pending" }, wantStatus: "runtime_precheck_pending", wantScope: "runtime"},
		{name: "运行态策略避让", mutate: func(i *availabilityInput) { i.runtimeStatus = "local_suppressed" }, wantStatus: "runtime_local_suppressed", wantScope: "runtime"},
		{name: "运行态半开", mutate: func(i *availabilityInput) { i.runtimeStatus = "half_open" }, wantStatus: "runtime_half_open", wantScope: "runtime"},
		{name: "运行态确认失败", mutate: func(i *availabilityInput) { i.runtimeStatus = "precheck_failed" }, wantStatus: "runtime_precheck_failed", wantScope: "runtime"},
		{name: "非active账户忽略运行态阻塞", mutate: func(i *availabilityInput) {
			i.status = "disabled"
			i.runtimeStatus = "degraded"
		}, wantStatus: "instance_disabled", wantScope: "account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := baseEffInput()
			tc.mutate(&input)
			got := accountEffectiveAvailability(input, now)
			if got.status != tc.wantStatus {
				t.Fatalf("status = %q, 期望 %q", got.status, tc.wantStatus)
			}
			if got.available != tc.available {
				t.Fatalf("available = %v, 期望 %v (status %s)", got.available, tc.available, got.status)
			}
			if tc.wantScope != "" && got.blockerScope != tc.wantScope {
				t.Fatalf("blockerScope = %q, 期望 %q", got.blockerScope, tc.wantScope)
			}
			if tc.wantRetryAt != "" && got.retryAt != tc.wantRetryAt {
				t.Fatalf("retryAt = %q, 期望 %q", got.retryAt, tc.wantRetryAt)
			}
			if got.status == "" || got.label == "" || got.color == "" {
				t.Fatalf("presentation 字段不得为空: %+v", got)
			}
		})
	}
}

// TestAccountEffectiveAvailabilityAuthorizedBranches 覆盖 authorized 实例的
// binding/authorization/source 阻塞链（按 Node 检查顺序取第一个命中）。
func TestAccountEffectiveAvailabilityAuthorizedBranches(t *testing.T) {
	now := effNow()
	newAuthorized := func() availabilityInput {
		input := baseEffInput()
		input.accessType = "authorized"
		input.boundGroupID = "group-1"
		input.authorizationStatus = "active"
		input.sourceAccountID = "src-1"
		input.sourceStatus = "active"
		return input
	}
	cases := []struct {
		name       string
		mutate     func(*availabilityInput)
		wantStatus string
		wantScope  string
	}{
		{name: "authorized可用", mutate: func(i *availabilityInput) {}, wantStatus: "available", wantScope: ""},
		{name: "未绑定分组", mutate: func(i *availabilityInput) { i.boundGroupID = "" }, wantStatus: "binding_missing", wantScope: "binding"},
		{name: "绑定授权失效", mutate: func(i *availabilityInput) { i.groupBindStatus = "authorization_unavailable" }, wantStatus: "authorization_unavailable", wantScope: "binding"},
		{name: "授权状态到期", mutate: func(i *availabilityInput) { i.authorizationStatus = "expired" }, wantStatus: "authorization_expired", wantScope: "authorization"},
		{name: "授权到期时间戳", mutate: func(i *availabilityInput) { i.authorizationExpiresAt = effPast }, wantStatus: "authorization_expired", wantScope: "authorization"},
		{name: "授权暂停", mutate: func(i *availabilityInput) { i.authorizationStatus = "paused" }, wantStatus: "authorization_paused", wantScope: "authorization"},
		{name: "授权已撤销", mutate: func(i *availabilityInput) { i.authorizationStatus = "revoked" }, wantStatus: "authorization_unavailable", wantScope: "authorization"},
		{name: "授权已归还", mutate: func(i *availabilityInput) { i.authorizationStatus = "returned" }, wantStatus: "authorization_unavailable", wantScope: "authorization"},
		{name: "授权额度超限", mutate: func(i *availabilityInput) { i.authorizationQuotaExceeded = true }, wantStatus: "authorization_quota_exceeded", wantScope: "authorization"},
		{name: "来源缺失", mutate: func(i *availabilityInput) { i.sourceAccountID = "" }, wantStatus: "source_deleted", wantScope: "source_account"},
		{name: "来源到期错误码", mutate: func(i *availabilityInput) { i.sourceLastErrorCode = "account_expired" }, wantStatus: "source_expired", wantScope: "source_account"},
		{name: "来源到期时间戳", mutate: func(i *availabilityInput) { i.sourceExpiresAt = effPast }, wantStatus: "source_expired", wantScope: "source_account"},
		{name: "来源停用", mutate: func(i *availabilityInput) { i.sourceStatus = "disabled" }, wantStatus: "source_disabled", wantScope: "source_account"},
		{name: "来源待检查", mutate: func(i *availabilityInput) { i.sourceStatus = "pending_test" }, wantStatus: "source_pending_test", wantScope: "source_account"},
		{name: "来源异常", mutate: func(i *availabilityInput) { i.sourceStatus = "error" }, wantStatus: "source_error", wantScope: "source_account"},
		{name: "来源限流", mutate: func(i *availabilityInput) { i.sourceStatus = "rate_limited" }, wantStatus: "source_rate_limited", wantScope: "source_account"},
		{name: "来源临时不可调用", mutate: func(i *availabilityInput) { i.sourceStatus = "temporary_unavailable" }, wantStatus: "source_temporary_unavailable", wantScope: "source_account"},
		{name: "来源质量隔离", mutate: func(i *availabilityInput) { i.sourceStatus = "quality_isolated" }, wantStatus: "source_quality_isolated", wantScope: "source_account"},
		{name: "来源冷却", mutate: func(i *availabilityInput) { i.sourceCooldownUntil = effFuture }, wantStatus: "source_cooldown", wantScope: "source_account"},
		{name: "来源停调", mutate: func(i *availabilityInput) {
			falseValue := false
			i.sourceSchedulable = &falseValue
		}, wantStatus: "source_unschedulable", wantScope: "source_account"},
		{name: "来源可调度true不阻塞", mutate: func(i *availabilityInput) {
			trueValue := true
			i.sourceSchedulable = &trueValue
		}, wantStatus: "available", wantScope: ""},
		{name: "来源错误信息优先于默认文案", mutate: func(i *availabilityInput) {
			i.sourceStatus = "disabled"
			i.sourceLastErrorMessage = "上游维护中"
		}, wantStatus: "source_disabled", wantScope: "source_account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := newAuthorized()
			tc.mutate(&input)
			got := accountEffectiveAvailability(input, now)
			if got.status != tc.wantStatus {
				t.Fatalf("status = %q, 期望 %q", got.status, tc.wantStatus)
			}
			if got.blockerScope != tc.wantScope {
				t.Fatalf("blockerScope = %q, 期望 %q", got.blockerScope, tc.wantScope)
			}
		})
	}

	// authorized 实例级阻塞的 label/scope 用 authorized 专属文案与 scope。
	input := newAuthorized()
	input.status = "disabled"
	got := accountEffectiveAvailability(input, now)
	if got.status != "instance_disabled" || got.blockerScope != "authorized_instance" {
		t.Fatalf("authorized 实例停用应映射 authorized_instance scope: %+v", got)
	}
	if got.label != "授权实例停用" {
		t.Fatalf("authorized 实例 label 不符: %s", got.label)
	}
}

// TestFilterStatusForEffectiveAvailabilityStatus 锁定状态到筛选状态的映射表。
func TestFilterStatusForEffectiveAvailabilityStatus(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"available", "active"},
		{"runtime_degraded", "active"},
		{"instance_pending_test", "pending_test"},
		{"source_pending_test", "pending_test"},
		{"instance_error", "error"},
		{"source_error", "error"},
		{"instance_quality_isolated", "quality_isolated"},
		{"source_quality_isolated", "quality_isolated"},
		{"authorization_quota_exceeded", "rate_limited"},
		{"source_rate_limited", "rate_limited"},
		{"instance_rate_limited", "rate_limited"},
		{"source_cooldown", "temporary_unavailable"},
		{"instance_cooldown", "temporary_unavailable"},
		{"api_key_pool_unavailable", "temporary_unavailable"},
		{"runtime_local_suppressed", "temporary_unavailable"},
		{"runtime_half_open", "temporary_unavailable"},
		{"runtime_precheck_pending", "temporary_unavailable"},
		{"runtime_precheck_failed", "temporary_unavailable"},
		{"source_temporary_unavailable", "temporary_unavailable"},
		{"instance_temporary_unavailable", "temporary_unavailable"},
		{"authorization_expired", "disabled"},
		{"authorization_paused", "disabled"},
		{"authorization_unavailable", "disabled"},
		{"binding_missing", "disabled"},
		{"permission_denied", "disabled"},
		{"source_deleted", "disabled"},
		{"source_expired", "disabled"},
		{"source_disabled", "disabled"},
		{"source_unschedulable", "disabled"},
		{"instance_expired", "disabled"},
		{"instance_disabled", "disabled"},
		{"instance_unschedulable", "disabled"},
		{"unknown_status", ""},
	}
	for _, tc := range cases {
		if got := filterStatusForEffectiveAvailabilityStatus(tc.status); got != tc.want {
			t.Fatalf("filterStatus(%q) = %q, 期望 %q", tc.status, got, tc.want)
		}
	}
}

// TestAccountFilterStatusesFallbacks 覆盖派生状态缺失时的回退序。
func TestAccountFilterStatusesFallbacks(t *testing.T) {
	blocked := blockedAvailability("instance_disabled", "", "", "", "", "")
	if got := accountFilterStatuses("active", &blocked, false); len(got) != 1 || got[0] != "disabled" {
		t.Fatalf("派生状态优先: %v", got)
	}
	// itemStatus 非 active 且无派生状态 → 原样返回 itemStatus。
	unknown := effectiveAvailability{available: false, status: "mystery"}
	if got := accountFilterStatuses("paused", &unknown, false); len(got) != 1 || got[0] != "paused" {
		t.Fatalf("非 active 无派生应回退 itemStatus: %v", got)
	}
	if got := accountFilterStatuses("disabled", nil, false); len(got) != 1 || got[0] != "disabled" {
		t.Fatalf("无可用性对象应回退 itemStatus: %v", got)
	}
}

// TestPresentationMappingTable 锁定 presentationMapping 的完整映射表与默认值。
func TestPresentationMappingTable(t *testing.T) {
	cases := []struct {
		status  string
		want    string
		wantAct string
	}{
		{"available", "available", "none"},
		{"permission_denied", "permission_denied", "contact_admin"},
		{"authorization_expired", "expired", "renew_authorization"},
		{"authorization_paused", "authorization_blocked", "contact_authorizer"},
		{"authorization_unavailable", "authorization_blocked", "contact_authorizer"},
		{"authorization_quota_exceeded", "authorization_blocked", "contact_authorizer"},
		{"source_deleted", "source_blocked", "contact_authorizer"},
		{"source_disabled", "source_blocked", "contact_authorizer"},
		{"source_unschedulable", "source_blocked", "contact_authorizer"},
		{"source_expired", "expired", "contact_authorizer"},
		{"source_pending_test", "pending_check", "contact_authorizer"},
		{"source_error", "error", "contact_authorizer"},
		{"source_rate_limited", "rate_limited", "contact_authorizer"},
		{"source_temporary_unavailable", "temporarily_unavailable", "contact_authorizer"},
		{"source_cooldown", "temporarily_unavailable", "contact_authorizer"},
		{"source_quality_isolated", "source_blocked", "contact_authorizer"},
		{"instance_expired", "expired", "renew_authorization"},
		{"instance_disabled", "disabled", "enable_account"},
		{"instance_unschedulable", "disabled", "enable_account"},
		{"instance_pending_test", "pending_check", "retry_check"},
		{"instance_error", "error", "fix_configuration"},
		{"instance_rate_limited", "rate_limited", "restore_account"},
		{"instance_temporary_unavailable", "temporarily_unavailable", "restore_account"},
		{"instance_cooldown", "temporarily_unavailable", "restore_account"},
		{"instance_quality_isolated", "error", "retry_check"},
		{"binding_missing", "binding_missing", "bind_group"},
		{"api_key_pool_unavailable", "key_pool_unavailable", "retry_check"},
		{"runtime_degraded", "degraded", "restore_account"},
		{"runtime_local_suppressed", "avoided", "restore_account"},
		{"runtime_half_open", "verifying", "none"},
		{"runtime_precheck_pending", "verifying", "restore_account"},
		{"runtime_precheck_failed", "verification_failed", "retry_check"},
		{"unknown_status", "unknown_status", "none"},
	}
	for _, tc := range cases {
		gotStatus, gotAction := presentationMapping(tc.status)
		if gotStatus != tc.want || gotAction != tc.wantAct {
			t.Fatalf("presentationMapping(%q) = (%q, %q), 期望 (%q, %q)", tc.status, gotStatus, gotAction, tc.want, tc.wantAct)
		}
	}
}

// TestBoundaryKindOf 锁定 statusBoundary kind 映射。
func TestBoundaryKindOf(t *testing.T) {
	cases := map[string]string{
		"authorization_expired":        "authorization_expired",
		"source_expired":               "source_expired",
		"instance_expired":             "account_expired",
		"authorization_quota_exceeded": "quota_reset",
		"source_cooldown":              "cooldown_expiry",
		"instance_cooldown":            "cooldown_expiry",
		"runtime_local_suppressed":     "policy_ttl_expiry",
		"instance_error":               "",
	}
	for status, want := range cases {
		if got := boundaryKindOf(status); got != want {
			t.Fatalf("boundaryKindOf(%q) = %q, 期望 %q", status, got, want)
		}
	}
}

func emptyManagementRow(id string) managementRow {
	return managementRow{id: id}
}

// TestPresentationPayloadBoundaryAndProbeActions 覆盖 presentationPayload 的
// statusBoundary 分支与 instance_error 动作改写。
func TestPresentationPayloadBoundaryAndProbeActions(t *testing.T) {
	now := effNow()
	if got := presentationPayload(nil, emptyManagementRow("a"), nil, now); got != nil {
		t.Fatalf("无可用性对象时 payload 必须为 nil: %v", got)
	}

	// cooldown 阻塞携带 statusBoundary（at + kind）；边界取 row.cooldownUntil。
	cooldownRow := emptyManagementRow("a")
	cooldownRow.cooldownUntil.Valid = true
	cooldownRow.cooldownUntil.String = effFuture
	cooldown := blockedAvailability("instance_cooldown", "冷却", "gold", "account", "冷却中", effFuture)
	payload := presentationPayload(&cooldown, cooldownRow, nil, now)
	boundary, ok := payload["statusBoundary"].(map[string]any)
	if !ok || boundary["at"] != effFuture || boundary["kind"] != "cooldown_expiry" {
		t.Fatalf("cooldown 应携带 statusBoundary: %v", payload)
	}
	if _, exists := payload["probe"]; exists {
		t.Fatalf("有 statusBoundary 时不得再输出 probe: %v", payload)
	}

	// instance_error + account_activation_check_timeout → retry_check。
	row := emptyManagementRow("a")
	row.lastErrorCode.Valid = true
	row.lastErrorCode.String = "account_activation_check_timeout"
	errAvail := blockedAvailability("instance_error", "异常", "red", "account", "异常", "")
	payload = presentationPayload(&errAvail, row, nil, now)
	if payload["action"] != "retry_check" {
		t.Fatalf("激活检查超时应改写为 retry_check: %v", payload["action"])
	}

	// instance_error + cooldown_retest_ 前缀 → restore_account。
	row2 := emptyManagementRow("b")
	row2.lastErrorCode.Valid = true
	row2.lastErrorCode.String = "cooldown_retest_http_502"
	payload = presentationPayload(&errAvail, row2, nil, now)
	if payload["action"] != "restore_account" {
		t.Fatalf("冷却重试错误应改写为 restore_account: %v", payload["action"])
	}

	// available 输出基础三元组。
	okAvail := effectiveAvailability{available: true, status: "available", label: "可调度", color: "green"}
	payload = presentationPayload(&okAvail, emptyManagementRow("c"), nil, now)
	if payload["status"] != "available" || payload["action"] != "none" || payload["label"] != "可调度" {
		t.Fatalf("available presentation 不符: %v", payload)
	}
}

// TestProbeFactsPayloadGating 覆盖 probeFactsPayload 的门控分支。
func TestProbeFactsPayloadGating(t *testing.T) {
	now := effNow()
	row := emptyManagementRow("a")
	if got := probeFactsPayload(nil, row, now); got != nil {
		t.Fatalf("nil 可用性应返回 nil: %v", got)
	}
	keyPool := blockedAvailability("api_key_pool_unavailable", "", "", "", "", "")
	if got := probeFactsPayload(&keyPool, row, now); got != nil {
		t.Fatalf("api_key_pool 阻塞无 probe 事实: %v", got)
	}
	runtimeBlocked := blockedAvailability("runtime_half_open", "", "", "", "", "")
	if got := probeFactsPayload(&runtimeBlocked, row, now); got != nil {
		t.Fatalf("runtime_* 事实由 runtimeAvailability 承载: %v", got)
	}
	instanceError := blockedAvailability("instance_error", "", "", "", "", "")
	if got := probeFactsPayload(&instanceError, row, now); got != nil {
		t.Fatalf("普通实例阻塞无 probe 事实: %v", got)
	}

	// instance_pending_test：无健康观察且无 nextHealthCheckAt → nil。
	pending := blockedAvailability("instance_pending_test", "", "", "", "", "")
	if got := probeFactsPayload(&pending, row, now); got != nil {
		t.Fatalf("无观察无计划的待检查不输出 probe: %v", got)
	}
	// 有 nextHealthCheckAt → schedule-only probe。
	row.nextHealthCheckAt.Valid = true
	row.nextHealthCheckAt.String = effFuture
	got := probeFactsPayload(&pending, row, now)
	if got == nil || got["kind"] != "activation_check" {
		t.Fatalf("待检查应输出 activation_check probe: %v", got)
	}
	schedule, ok := got["schedule"].(map[string]any)
	if !ok || schedule["state"] != "scheduled" {
		t.Fatalf("未来 nextHealthCheckAt 应为 scheduled: %v", got["schedule"])
	}
	if _, exists := got["lastObservation"]; exists {
		t.Fatalf("无健康观察不得输出 lastObservation: %v", got)
	}

	// source_* → sourceProbePayload。
	sourceBlocked := blockedAvailability("source_error", "", "", "", "", "")
	row.sourceNextHealthCheckAt.Valid = true
	row.sourceNextHealthCheckAt.String = effFuture
	got = probeFactsPayload(&sourceBlocked, row, now)
	if got == nil || got["kind"] != "source_account_probe" {
		t.Fatalf("source 阻塞应输出 source_account_probe: %v", got)
	}
}

// TestSourceProbePayloadObservations 覆盖 sourceProbePayload 的健康观察与
// cooldown-retest 双数据源切换。
func TestSourceProbePayloadObservations(t *testing.T) {
	now := effNow()

	// 无观察且无计划 → nil。
	if got := sourceProbePayload("source_error", emptyManagementRow("a"), now); got != nil {
		t.Fatalf("无观察无计划的 source probe 应为 nil: %v", got)
	}

	// 健康检查成功观察（httpStatus < 400）。
	row := emptyManagementRow("a")
	row.sourceLastHealthCheckAt.Valid = true
	row.sourceLastHealthCheckAt.String = effPast
	row.sourceLastHealthCheckStatusCode.Valid = true
	row.sourceLastHealthCheckStatusCode.Int64 = 200
	got := sourceProbePayload("source_error", row, now)
	if got == nil {
		t.Fatal("有观察时必须输出 probe")
	}
	observation, ok := got["lastObservation"].(map[string]any)
	if !ok || observation["result"] != "success" || observation["attemptedAt"] != effPast {
		t.Fatalf("成功观察不符: %v", got["lastObservation"])
	}
	if observation["httpStatus"] != int64(200) {
		t.Fatalf("httpStatus 应保留: %v", observation["httpStatus"])
	}

	// 失败观察带 errorCode/reason/traceId，且 observationId 稳定为 24 位 hex。
	row.sourceLastHealthCheckErrorCode.Valid = true
	row.sourceLastHealthCheckErrorCode.String = "http_502"
	row.sourceLastHealthCheckErrorMessage.Valid = true
	row.sourceLastHealthCheckErrorMessage.String = "上游 502"
	row.sourceLastHealthCheckTraceID.Valid = true
	row.sourceLastHealthCheckTraceID.String = "trace-1"
	got = sourceProbePayload("source_error", row, now)
	observation = got["lastObservation"].(map[string]any)
	if observation["result"] != "failed" || observation["errorCode"] != "http_502" || observation["reason"] != "上游 502" || observation["traceId"] != "trace-1" {
		t.Fatalf("失败观察不符: %v", observation)
	}
	observationID := observation["observationId"].(string)
	if len(observationID) != 24 {
		t.Fatalf("observationId 应为 24 字符: %s", observationID)
	}

	// rate_limited 走 cooldown-retest 数据源（attemptedAt 用 retest 时间）。
	retestRow := emptyManagementRow("b")
	retestRow.sourceCooldownRetestLastAt.Valid = true
	retestRow.sourceCooldownRetestLastAt.String = effPast
	retestRow.sourceLastErrorCode.Valid = true
	retestRow.sourceLastErrorCode.String = "cooldown_retest_http_429"
	retestRow.sourceCooldownRetestLastStatusCode.Valid = true
	retestRow.sourceCooldownRetestLastStatusCode.Int64 = 429
	retestRow.sourceCooldownUntil.Valid = true
	retestRow.sourceCooldownUntil.String = effFuture
	got = sourceProbePayload("source_rate_limited", retestRow, now)
	observation = got["lastObservation"].(map[string]any)
	if observation["attemptedAt"] != effPast || observation["errorCode"] != "cooldown_retest_http_429" || observation["httpStatus"] != int64(429) {
		t.Fatalf("cooldown-driven 观察应取 retest 列: %v", observation)
	}
	schedule := got["schedule"].(map[string]any)
	if schedule["nextAttemptAt"] != effFuture {
		t.Fatalf("cooldown-driven 计划应取 sourceCooldownUntil: %v", schedule)
	}
}

// TestHealthObservationPayload 覆盖 healthObservationPayload 的形状。
func TestHealthObservationPayload(t *testing.T) {
	if got := healthObservationPayload(emptyManagementRow("a")); got != nil {
		t.Fatalf("无 lastHealthCheckAt 时应为 nil: %v", got)
	}
	row := emptyManagementRow("a")
	row.lastHealthCheckAt.Valid = true
	row.lastHealthCheckAt.String = effPast
	got := healthObservationPayload(row)
	if got["result"] != "success" {
		t.Fatalf("无失败痕迹应为 success: %v", got)
	}
	if _, exists := got["httpStatus"]; exists {
		t.Fatalf("无 status code 不得输出 httpStatus: %v", got)
	}
	row.lastHealthCheckStatusCode.Valid = true
	row.lastHealthCheckStatusCode.Int64 = 500
	got = healthObservationPayload(row)
	if got["result"] != "failed" || got["httpStatus"] != int64(500) {
		t.Fatalf("5xx 应判 failed 并保留 httpStatus: %v", got)
	}
}

// TestSchedulePayloadState 覆盖 schedule 状态分类。
func TestSchedulePayloadState(t *testing.T) {
	now := effNow()
	if got := schedulePayloadState("", now); got["state"] != "none" {
		t.Fatalf("空计划应为 none: %v", got)
	}
	if got := schedulePayloadState("not-a-time", now); got["state"] != "none" {
		t.Fatalf("非法时间应为 none: %v", got)
	}
	if got := schedulePayloadState(effFuture, now); got["state"] != "scheduled" {
		t.Fatalf("未来时间应为 scheduled: %v", got)
	}
	if got := schedulePayloadState(effPast, now); got["state"] != "due_waiting" {
		t.Fatalf("过去时间应为 due_waiting: %v", got)
	}
}

// TestProbeObservationIDDeterministic 锁定 observationId 的确定性哈希。
func TestProbeObservationIDDeterministic(t *testing.T) {
	first := probeObservationID("health_check", "acc-1", effPast, "trace", "code", "reason")
	second := probeObservationID("health_check", "acc-1", effPast, "trace", "code", "reason")
	if first != second {
		t.Fatalf("相同输入必须产出相同 observationId: %s vs %s", first, second)
	}
	changed := probeObservationID("health_check", "acc-2", effPast, "trace", "code", "reason")
	if changed == first {
		t.Fatal("不同 identity 不得产出相同 observationId")
	}
}

// TestProbeSummaryShape 锁定 probeSummary 装配。
func TestProbeSummaryShape(t *testing.T) {
	observation := map[string]any{"result": "success"}
	schedule := map[string]any{"state": "none"}
	got := probeSummary("kind-1", observation, schedule)
	if got["kind"] != "kind-1" {
		t.Fatalf("probeSummary 装配不符: %v", got)
	}
	gotSchedule, ok := got["schedule"].(map[string]any)
	if !ok || gotSchedule["state"] != "none" {
		t.Fatalf("schedule 应原样透传: %v", got["schedule"])
	}
	gotObservation, ok := got["lastObservation"].(map[string]any)
	if !ok || gotObservation["result"] != "success" {
		t.Fatalf("lastObservation 应原样透传: %v", got["lastObservation"])
	}
	withoutObservation := probeSummary("kind-1", nil, schedule)
	if _, exists := withoutObservation["lastObservation"]; exists {
		t.Fatalf("无观察不得输出 lastObservation 键: %v", withoutObservation)
	}
}

// TestEffectiveSmallHelpers 覆盖小工具的边界（nil/空/非法时间）。
func TestEffectiveSmallHelpers(t *testing.T) {
	now := effNow()
	if ptrString("x") == nil || *ptrString("x") != "x" {
		t.Fatal("ptrString 应返回指针")
	}
	if optionalString("") != nil {
		t.Fatal("空串应返回 nil")
	}
	if optionalString("x") == nil || *optionalString("x") != "x" {
		t.Fatal("非空串应返回指针")
	}
	if orDefault("", "fallback") != "fallback" || orDefault("value", "fallback") != "value" {
		t.Fatal("orDefault 语义不符")
	}
	if isExpiredInstant("", now) {
		t.Fatal("空时间不算过期")
	}
	if !isExpiredInstant(effPast, now) {
		t.Fatal("过去时间应过期")
	}
	if isExpiredInstant("bad-time", now) {
		t.Fatal("非法时间不得判为过期")
	}
	if isFutureInstant("", now) {
		t.Fatal("空时间不算未来")
	}
	if !isFutureInstant(effFuture, now) {
		t.Fatal("未来时间应判未来")
	}
	if isFutureInstant("bad-time", now) {
		t.Fatal("非法时间不得判为未来")
	}
	if value, ok := rfc3339Millis(effFuture); !ok || value != time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("rfc3339Millis 解析不符: %d %v", value, ok)
	}
	if _, ok := rfc3339Millis("bad"); ok {
		t.Fatal("非法时间不得解析成功")
	}
}

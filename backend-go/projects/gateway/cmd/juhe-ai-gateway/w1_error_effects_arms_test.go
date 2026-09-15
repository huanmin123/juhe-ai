package main

// w1_error_effects_arms_test.go —— chain_error_policy_effects.go 状态写侧的
// 补臂单测。覆盖 chain_failure_dispatch_test.go / w1_dispatch_arms_test.go
// 未触及的分支：归因文案与摘要、授权绑定目标投影、运行态观察围栏 SQL 片段、
// 桥构造错误路径、loadAccountRuntimeRow、过期自动停用成功写、冷却兜底与
// 截断分支、授权绑定冷却/禁用变体（含 EXISTS 守卫）、Key 级失败记录守卫。
// SQLite 夹具复用 newErrorPolicyEffectsFixture；固定时钟保证确定性。

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// w1eBindingAccount 构造 account_authorized 绑定候选（指针字段齐全）。
func w1eBindingAccount(id string) gatewaydispatch.AccountCandidate {
	account := errorPolicyAccount(nil)
	account.ID = id
	account.AccountAccessType = "account_authorized"
	account.BindingSystemAccountID = strPtr("sys-bind")
	account.BoundGroupID = strPtr("grp-bind")
	account.AccountAuthorizationID = strPtr("authz-bind")
	return account
}

// ---------------------------------------------------------------------------
// 纯函数标量臂
// ---------------------------------------------------------------------------

func TestW1EEffectsScalarArms(t *testing.T) {
	// explicitPolicyFailureCode 三分支。
	if got := explicitPolicyFailureCode(accountErrorPolicyDecision{RuleSource: "system", QuotaRecoveryMode: string(quotaRecoveryModeExplicitReset)}); got != systemQuotaExplicitResetCooldownCode {
		t.Fatalf("system explicit_reset 码 = %q", got)
	}
	if got := explicitPolicyFailureCode(accountErrorPolicyDecision{RuleSource: "system", QuotaRecoveryMode: string(quotaRecoveryModeGeneric)}); got != systemQuotaGenericCooldownCode {
		t.Fatalf("system generic 码 = %q", got)
	}
	if got := explicitPolicyFailureCode(accountErrorPolicyDecision{RuleSource: "account"}); got != explicitAccountErrorPolicyCooldownCode {
		t.Fatalf("account 码 = %q", got)
	}

	// stringValueOf。
	if got := stringValueOf(nil); got != "" {
		t.Fatalf("stringValueOf(nil) = %q", got)
	}
	if got := stringValueOf(strPtr("值")); got != "值" {
		t.Fatalf("stringValueOf = %q", got)
	}

	// isHardUnavailableAccountStatus。
	for _, status := range []string{"disabled", "pending_test", "error", "quality_isolated"} {
		if !isHardUnavailableAccountStatus(status) {
			t.Fatalf("isHardUnavailableAccountStatus(%q) 必须为真", status)
		}
	}
	for _, status := range []string{"active", "rate_limited", "temporary_unavailable", ""} {
		if isHardUnavailableAccountStatus(status) {
			t.Fatalf("isHardUnavailableAccountStatus(%q) 必须为假", status)
		}
	}

	// isAccountExpired。
	now := w1ePolicyClock()
	if isAccountExpired(sql.NullString{}, now) {
		t.Fatal("NULL 过期时间必须为假")
	}
	if isAccountExpired(sql.NullString{String: "   ", Valid: true}, now) {
		t.Fatal("空白过期时间必须为假")
	}
	if isAccountExpired(sql.NullString{String: "不是时间", Valid: true}, now) {
		t.Fatal("非法过期时间必须为假")
	}
	if isAccountExpired(sql.NullString{String: "2026-09-01T10:00:01.000Z", Valid: true}, now) {
		t.Fatal("未来过期时间必须为假")
	}
	if !isAccountExpired(sql.NullString{String: "2026-08-01T00:00:00.000Z", Valid: true}, now) {
		t.Fatal("过去过期时间必须为真")
	}
	if !isAccountExpired(sql.NullString{String: "2026-09-01T10:00:00.000Z", Valid: true}, now) {
		t.Fatal("恰好等于当前时刻必须视为过期")
	}

	// rowCountOf(nil) 为 0；非 nil 结果在 SQLite 用例中断言。
	if rowCountOf(nil) != 0 {
		t.Fatal("rowCountOf(nil) 必须为 0")
	}

	// newUUIDv4：UUIDv4 形状且两次调用不同。
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	first := newUUIDv4()
	second := newUUIDv4()
	if !uuidPattern.MatchString(first) || !uuidPattern.MatchString(second) {
		t.Fatalf("UUID 形状错误：%q / %q", first, second)
	}
	if first == second {
		t.Fatalf("两次生成结果相同：%q", first)
	}

	// truncateUTF8：rune 边界。
	if got := truncateUTF8("abc", 5); got != "abc" {
		t.Fatalf("未超限截断 = %q", got)
	}
	if got := truncateUTF8("abcde", 5); got != "abcde" {
		t.Fatalf("恰好等于限长 = %q", got)
	}
	if got := truncateUTF8("abcdef", 3); got != "abc" {
		t.Fatalf("超限截断 = %q", got)
	}
	if got := truncateUTF8("上游系统过载", 3); got != "上游系" {
		t.Fatalf("多字节截断 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 归因文案与失败体摘要
// ---------------------------------------------------------------------------

func TestW1EEffectsReasonArms(t *testing.T) {
	account := errorPolicyAccount(nil)
	decision := accountErrorPolicyDecision{RuleName: "限流规则", RuleSource: "account"}

	// 无状态码：异常文案取 ErrorMessage。
	reason := explicitAccountErrorPolicyReason(account, chainErrorPolicyFailureInput{ErrorMessage: "连接中断"}, decision)
	if reason != "账户错误策略「限流规则」命中；上游请求异常：连接中断" {
		t.Fatalf("无状态码文案 = %q", reason)
	}

	// 无状态码且全空：回落「请求失败」。
	reason = explicitAccountErrorPolicyReason(account, chainErrorPolicyFailureInput{}, decision)
	if reason != "账户错误策略「限流规则」命中；上游请求异常：请求失败" {
		t.Fatalf("空输入文案 = %q", reason)
	}

	// 无状态码 + 敏感串：脱敏后拼入。
	reason = explicitAccountErrorPolicyReason(account, chainErrorPolicyFailureInput{ErrorMessage: "bad key sk-abcdefgh1234 leaked"}, decision)
	if strings.Contains(reason, "sk-abcdefgh1234") || !strings.Contains(reason, "[redacted]") {
		t.Fatalf("异常文案未脱敏 = %q", reason)
	}

	// 有状态码 + resolved 摘要：直接采用。
	reason = explicitAccountErrorPolicyReason(account, chainErrorPolicyFailureInput{
		HasStatusCode: true, StatusCode: 429, UpstreamErrorSummary: "上游过载", UpstreamErrorSummaryResolved: true,
	}, decision)
	if reason != "账户错误策略「限流规则」命中；上游调用失败：HTTP 429；上游过载" {
		t.Fatalf("resolved 摘要文案 = %q", reason)
	}

	// 有状态码 + resolved 但摘要为空：仅基础句。
	reason = explicitAccountErrorPolicyReason(account, chainErrorPolicyFailureInput{
		HasStatusCode: true, StatusCode: 402, UpstreamErrorSummaryResolved: true,
	}, decision)
	if reason != "账户错误策略「限流规则」命中；上游调用失败：HTTP 402" {
		t.Fatalf("空摘要文案 = %q", reason)
	}

	// 未 resolved：从失败体重解析协议载荷摘要。
	reason = explicitAccountErrorPolicyReason(account, chainErrorPolicyFailureInput{
		HasStatusCode: true, StatusCode: 402,
		BodyText: `{"error":{"code":"insufficient_quota","message":"quota out"}}`,
	}, decision)
	if reason != "账户错误策略「限流规则」命中；上游调用失败：HTTP 402；insufficient_quota；quota out" {
		t.Fatalf("重解析摘要文案 = %q", reason)
	}

	// 未 resolved 但已有摘要：不重解析。
	reason = explicitAccountErrorPolicyReason(account, chainErrorPolicyFailureInput{
		HasStatusCode: true, StatusCode: 500, UpstreamErrorSummary: "已有摘要",
		BodyText: `{"error":{"code":"insufficient_quota","message":"quota out"}}`,
	}, decision)
	if !strings.HasSuffix(reason, "上游调用失败：HTTP 500；已有摘要") {
		t.Fatalf("已有摘要文案 = %q", reason)
	}

	// 系统来源标签与未命名规则兜底。
	systemDecision := accountErrorPolicyDecision{RuleSource: "system"}
	reason = explicitAccountErrorPolicyReason(account, chainErrorPolicyFailureInput{HasStatusCode: true, StatusCode: 402}, systemDecision)
	if reason != "系统继承错误策略「未命名规则」命中；上游调用失败：HTTP 402" {
		t.Fatalf("系统标签文案 = %q", reason)
	}

	// 超长文案按 rune 截断到 1000。
	longInput := chainErrorPolicyFailureInput{HasStatusCode: true, StatusCode: 500, UpstreamErrorSummary: strings.Repeat("长", 1200), UpstreamErrorSummaryResolved: true}
	longReason := explicitAccountErrorPolicyReason(account, longInput, decision)
	runes := []rune(longReason)
	if len(runes) != 1000 {
		t.Fatalf("超长文案长度 = %d, want 1000", len(runes))
	}

	// summaryFromFailureBody：空体与非 JSON 体返回空串。
	if got := summaryFromFailureBody(chainErrorPolicyFailureInput{}, account); got != "" {
		t.Fatalf("空体摘要 = %q", got)
	}
	if got := summaryFromFailureBody(chainErrorPolicyFailureInput{BodyText: "upstream down"}, account); got != "" {
		t.Fatalf("非 JSON 体摘要 = %q", got)
	}
	if got := summaryFromFailureBody(chainErrorPolicyFailureInput{BodyText: `{"error":{"code":"boom","message":"坏消息"}}`}, account); got != "boom；坏消息" {
		t.Fatalf("JSON 体摘要 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 授权绑定目标与 Key 级写目标投影、观察围栏输入
// ---------------------------------------------------------------------------

func TestW1EEffectsTargetArms(t *testing.T) {
	if got := authorizedBindingTargetOf(errorPolicyAccount(nil)); got != nil {
		t.Fatalf("非授权账户 = %+v, want nil", got)
	}
	missingBinding := w1eBindingAccount("acc")
	missingBinding.BindingSystemAccountID = nil
	if got := authorizedBindingTargetOf(missingBinding); got != nil {
		t.Fatalf("缺 BindingSystemAccountID = %+v, want nil", got)
	}
	missingGroup := w1eBindingAccount("acc")
	missingGroup.BoundGroupID = nil
	if got := authorizedBindingTargetOf(missingGroup); got != nil {
		t.Fatalf("缺 BoundGroupID = %+v, want nil", got)
	}
	missingAuthz := w1eBindingAccount("acc")
	missingAuthz.AccountAuthorizationID = nil
	if got := authorizedBindingTargetOf(missingAuthz); got != nil {
		t.Fatalf("缺 AccountAuthorizationID = %+v, want nil", got)
	}
	emptyBinding := w1eBindingAccount("acc")
	emptyBinding.BindingSystemAccountID = strPtr("")
	if got := authorizedBindingTargetOf(emptyBinding); got != nil {
		t.Fatalf("空 BindingSystemAccountID = %+v, want nil", got)
	}
	target := authorizedBindingTargetOf(w1eBindingAccount("acc-9"))
	if target == nil || target.AccountID != "acc-9" || target.SystemAccountID != "sys-bind" ||
		target.GroupID != "grp-bind" || target.AccountAuthorizationID != "authz-bind" {
		t.Fatalf("合法绑定目标 = %+v", target)
	}

	// accountKeyTargetOf：指针字段与索引投影。
	fingerprint := "fp-w1e"
	sourceID := "src-1"
	index := 2
	candidate := errorPolicyAccount(map[string]any{"api_keys": []any{"k1", "k2"}})
	candidate.SystemAccountID = "sys-1"
	candidate.AccountOwnerSystemAccountID = "owner-1"
	candidate.CredentialSourceAccountID = &sourceID
	candidate.SelectedAPIKeyFingerprint = &fingerprint
	candidate.SelectedAPIKeyIndex = &index
	candidate.APIKeyRuntimeStateDisabled = true
	targetInput := accountKeyTargetOf(candidate)
	if targetInput.AccountID != "acc_1" || targetInput.SystemAccountID != "sys-1" ||
		targetInput.OwnerSystemAccountID != "owner-1" || targetInput.CredentialSourceAccountID != "src-1" ||
		targetInput.SelectedAPIKeyFingerprint != "fp-w1e" || !targetInput.RuntimeStateDisabled {
		t.Fatalf("Key 级目标 = %+v", targetInput)
	}
	if !targetInput.HasSelectedAPIKeyIndex || targetInput.SelectedAPIKeyIndex != 2 {
		t.Fatalf("索引投影 = %+v", targetInput)
	}
	noIndex := accountKeyTargetOf(errorPolicyAccount(nil))
	if noIndex.HasSelectedAPIKeyIndex || noIndex.CredentialSourceAccountID != "" {
		t.Fatalf("无索引目标 = %+v", noIndex)
	}

	// runtimeFailureObservationGuardOf。
	if got := runtimeFailureObservationGuardOf(errorPolicyAccount(nil), w1ePolicyClock()); got != nil {
		t.Fatalf("无 revision = %+v, want nil", got)
	}
	zero := int64(0)
	zeroCandidate := errorPolicyAccount(nil)
	zeroCandidate.DispatchRevision = &zero
	if got := runtimeFailureObservationGuardOf(zeroCandidate, w1ePolicyClock()); got != nil {
		t.Fatalf("revision 0 = %+v, want nil", got)
	}
	revision := int64(7)
	revisionCandidate := errorPolicyAccount(nil)
	revisionCandidate.DispatchRevision = &revision
	guard := runtimeFailureObservationGuardOf(revisionCandidate, w1ePolicyClock())
	if guard == nil || guard.ExpectedDispatchRevision != 7 || guard.ObservedAt != "2026-09-01T10:00:00.000Z" {
		t.Fatalf("观察围栏 = %+v", guard)
	}
}

// ---------------------------------------------------------------------------
// SQL 片段与参数（直接断言生成结果）
// ---------------------------------------------------------------------------

func TestW1EEffectsSQLFragments(t *testing.T) {
	guard := &runtimeFailureObservationGuard{ExpectedDispatchRevision: 3, ObservedAt: "2026-09-01T10:00:00.000Z"}

	if got := runtimeFailureObservationGuardSQL(nil); got != "" {
		t.Fatalf("nil 围栏 SQL = %q", got)
	}
	guardSQL := runtimeFailureObservationGuardSQL(guard)
	for _, fragment := range []string{
		"dispatch_revision = ?",
		"last_health_success_at IS NULL OR last_health_success_at < ?",
		"updated_at IS NULL OR updated_at <= ?",
	} {
		if !strings.Contains(guardSQL, fragment) {
			t.Fatalf("围栏 SQL 缺片段 %q：%s", fragment, guardSQL)
		}
	}
	if got := runtimeFailureObservationGuardParams(nil); got != nil {
		t.Fatalf("nil 围栏参数 = %v", got)
	}
	params := runtimeFailureObservationGuardParams(guard)
	if len(params) != 3 || params[0] != int64(3) || params[1] != guard.ObservedAt || params[2] != guard.ObservedAt {
		t.Fatalf("围栏参数 = %v", params)
	}
	zeroGuard := &runtimeFailureObservationGuard{ExpectedDispatchRevision: 0, ObservedAt: "ts"}
	if clipped := runtimeFailureObservationGuardParams(zeroGuard); clipped[0] != int64(1) {
		t.Fatalf("revision 下限截断 = %v", clipped[0])
	}

	if got := runtimeFailureUpdatedAtSQL(nil); got != "?" {
		t.Fatalf("nil updated_at SQL = %q", got)
	}
	updatedAtSQL := runtimeFailureUpdatedAtSQL(guard)
	if !strings.Contains(updatedAtSQL, "CASE WHEN updated_at IS NULL OR updated_at < ? THEN ? ELSE updated_at END") {
		t.Fatalf("updated_at SQL = %q", updatedAtSQL)
	}
	if got := runtimeFailureUpdatedAtParams(nil, "fallback"); len(got) != 1 || got[0] != "fallback" {
		t.Fatalf("nil updated_at 参数 = %v", got)
	}
	if got := runtimeFailureUpdatedAtParams(guard, "fallback"); len(got) != 2 || got[0] != guard.ObservedAt || got[1] != guard.ObservedAt {
		t.Fatalf("updated_at 参数 = %v", got)
	}

	// 系统 quota 优先级围栏：非通用码短路；通用码分方言。
	plainBridge := &chainErrorPolicyEffectsBridge{pg: false}
	pgBridge := &chainErrorPolicyEffectsBridge{pg: true}
	if got := plainBridge.systemQuotaCooldownPrioritySQL(explicitAccountErrorPolicyCooldownCode, "ref"); got != "" {
		t.Fatalf("非通用码围栏 = %q", got)
	}
	plainFencing := plainBridge.systemQuotaCooldownPrioritySQL(systemQuotaGenericCooldownCode, "ref")
	for _, fragment := range []string{"last_error_code IN (?, ?, ?)", "LIKE ?", "julianday(cooldown_until) > julianday(?)"} {
		if !strings.Contains(plainFencing, fragment) {
			t.Fatalf("SQLite 围栏缺片段 %q：%s", fragment, plainFencing)
		}
	}
	pgFencing := pgBridge.systemQuotaCooldownPrioritySQL(systemQuotaGenericCooldownCode, "ref")
	if !strings.Contains(pgFencing, "cooldown_until::timestamptz > ?::timestamptz") {
		t.Fatalf("PG 围栏 = %s", pgFencing)
	}
	if got := systemQuotaCooldownPriorityParams(explicitAccountErrorPolicyCooldownCode, "ref"); got != nil {
		t.Fatalf("非通用码参数 = %v", got)
	}
	priorityParams := systemQuotaCooldownPriorityParams(systemQuotaGenericCooldownCode, "ref")
	wantParams := []any{
		systemQuotaExplicitResetCooldownCode, systemQuotaGenericCooldownCode, explicitAccountErrorPolicyCooldownCode,
		legacyExplicitPolicyMessagePrefix + "%", "ref",
	}
	if len(priorityParams) != len(wantParams) {
		t.Fatalf("优先级参数长度 = %d", len(priorityParams))
	}
	for i := range wantParams {
		if priorityParams[i] != wantParams[i] {
			t.Fatalf("优先级参数[%d] = %v, want %v", i, priorityParams[i], wantParams[i])
		}
	}

	// 绑定守卫 SQL 与参数。
	if got := plainBridge.bindingGuardSQL(nil); got != "" {
		t.Fatalf("nil 绑定守卫 SQL = %q", got)
	}
	if got := plainBridge.bindingExistsSQL(nil); got != "" {
		t.Fatalf("nil 绑定 EXISTS SQL = %q", got)
	}
	if got := bindingGuardParams(nil); got != nil {
		t.Fatalf("nil 绑定守卫参数 = %v", got)
	}
	if got := bindingExistsParams(nil); got != nil {
		t.Fatalf("nil 绑定 EXISTS 参数 = %v", got)
	}
	binding := &authorizedBindingTarget{
		AccountID: "acc-1", SystemAccountID: "sys-1", GroupID: "grp-1", AccountAuthorizationID: "authz-1",
	}
	existsSQL := plainBridge.bindingExistsSQL(binding)
	for _, fragment := range []string{
		"FROM group_accounts group_accounts",
		"group_accounts.account_id = accounts.id",
		"group_accounts.system_account_id = ?",
		"group_accounts.group_id = ?",
		"group_accounts.enabled = 1",
		"group_accounts.account_authorization_id = ?",
	} {
		if !strings.Contains(existsSQL, fragment) {
			t.Fatalf("EXISTS SQL 缺片段 %q：%s", fragment, existsSQL)
		}
	}
	guardBindingSQL := plainBridge.bindingGuardSQL(binding)
	for _, fragment := range []string{"system_account_id = ?", "authorization_instance_authorization_id = ?"} {
		if !strings.Contains(guardBindingSQL, fragment) {
			t.Fatalf("绑定守卫 SQL 缺片段 %q：%s", fragment, guardBindingSQL)
		}
	}
	guardParams := bindingGuardParams(binding)
	wantGuardParams := []any{"sys-1", "authz-1", "sys-1", "grp-1", "authz-1"}
	if len(guardParams) != len(wantGuardParams) {
		t.Fatalf("绑定守卫参数长度 = %d", len(guardParams))
	}
	for i := range wantGuardParams {
		if guardParams[i] != wantGuardParams[i] {
			t.Fatalf("绑定守卫参数[%d] = %v, want %v", i, guardParams[i], wantGuardParams[i])
		}
	}
	existsParams := bindingExistsParams(binding)
	if len(existsParams) != 3 || existsParams[0] != "sys-1" || existsParams[1] != "grp-1" || existsParams[2] != "authz-1" {
		t.Fatalf("EXISTS 参数 = %v", existsParams)
	}

	// 表名与占位符方言。
	if got := plainBridge.table("accounts"); got != "accounts" {
		t.Fatalf("SQLite 表名 = %q", got)
	}
	if got := pgBridge.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("PG 表名 = %q", got)
	}
	if got := plainBridge.bind("a = ? AND b = ?"); got != "a = ? AND b = ?" {
		t.Fatalf("SQLite bind = %q", got)
	}
	if got := pgBridge.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("PG bind = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 桥构造、降级实现与运行态行读取
// ---------------------------------------------------------------------------

func TestW1EEffectsBridgeArms(t *testing.T) {
	// 缺业务库句柄。
	if _, _, err := newChainErrorPolicyEffectsBridge(&composition{}, "secret"); err == nil || !strings.Contains(err.Error(), "缺少业务库句柄") {
		t.Fatalf("nil db = %v", err)
	}

	// 缺 JUHE_AI_SECRET。
	fixture := newErrorPolicyEffectsFixture(t)
	if _, _, err := newChainErrorPolicyEffectsBridge(&composition{db: fixture.db}, "  "); err == nil || !strings.Contains(err.Error(), "缺少 JUHE_AI_SECRET") {
		t.Fatalf("空 secret = %v", err)
	}

	// 完整装配：桥 + 决策服务可用，pool 闭包对非池账户返回 false。
	bridgePort, service, err := newChainErrorPolicyEffectsBridge(&composition{db: fixture.db}, "w1e-secret")
	if err != nil {
		t.Fatalf("装配桥: %v", err)
	}
	bridge, ok := bridgePort.(*chainErrorPolicyEffectsBridge)
	if !ok || bridge == nil || service == nil || bridge.keyStates == nil || bridge.pg {
		t.Fatalf("装配结果 = %T / %+v / %+v", bridgePort, bridge, service)
	}
	if delta := time.Since(bridge.now()); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("桥时钟偏差过大：%v", delta)
	}
	// pool 闭包在决策服务上：非池账户必须为 false。
	if service.pool(errorPolicyAccount(nil)) {
		t.Fatal("非池账户 pool 必须为 false")
	}
	// 桥的写入口可用（空库上 markCooldown 行缺失 → 未变更无错误）。
	changed, _, err := bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		errorPolicyAccount(nil), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("空库冷却应用 = %v/%v, want false/nil", changed, err)
	}

	// 降级实现：保留决策事实、跳过状态写、可重复调用。
	degraded := &degradedChainErrorPolicyEffects{}
	account := errorPolicyAccount(nil)
	account.Status = "paused"
	changed, status, err := degraded.ApplyAccountErrorPolicyDecision(context.Background(), account, systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || changed || status != "paused" {
		t.Fatalf("降级应用 = %v/%s/%v", changed, status, err)
	}
	if err := degraded.RecordKeyScopedQuotaFailure(context.Background(), account, systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{}); err != nil {
		t.Fatalf("降级记录 = %v", err)
	}
	degraded.degrade()

	// loadAccountRuntimeRow：命中、过期列读取与未命中。
	fixture.seedAccount(t, "acc_w1e_row", "active", 4)
	row, err := fixture.bridge.loadAccountRuntimeRow(context.Background(), "acc_w1e_row")
	if err != nil || row == nil || row.Status != "active" || row.ConfigRevision != 4 || row.AccountExpiresAt.Valid {
		t.Fatalf("运行态行 = %+v/%v", row, err)
	}
	if _, err := fixture.db.Exec(`UPDATE accounts SET account_expires_at = '2026-08-01T00:00:00.000Z' WHERE id = 'acc_w1e_row'`); err != nil {
		t.Fatalf("写过期列: %v", err)
	}
	row, err = fixture.bridge.loadAccountRuntimeRow(context.Background(), "acc_w1e_row")
	if err != nil || row == nil || !row.AccountExpiresAt.Valid || row.AccountExpiresAt.String != "2026-08-01T00:00:00.000Z" {
		t.Fatalf("过期行 = %+v/%v", row, err)
	}
	row, err = fixture.bridge.loadAccountRuntimeRow(context.Background(), "acc_w1e_missing")
	if err != nil || row != nil {
		t.Fatalf("未命中行 = %+v/%v, want nil/nil", row, err)
	}
}

// ---------------------------------------------------------------------------
// ApplyAccountErrorPolicyDecision 分支
// ---------------------------------------------------------------------------

func TestW1EEffectsApplyArms(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	ctx := context.Background()

	// retry_next 提前返回：不做任何状态写（无需种子行）。
	paused := errorPolicyAccount(nil)
	paused.Status = "paused"
	changed, status, err := fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, paused,
		accountErrorPolicyDecision{Action: decisionActionRetryNext, RuleName: "换号", RuleSource: "account"},
		chainErrorPolicyFailureInput{})
	if err != nil || changed || status != "paused" {
		t.Fatalf("retry_next = %v/%s/%v", changed, status, err)
	}

	// 过期分支成功写：status=disabled + account_expired 码 + 冷却清空 + 计数复位。
	fixture.seedAccount(t, "acc_w1e_expired", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET account_expires_at = '2026-08-01T00:00:00.000Z', updated_at = '2026-08-31T00:00:00.000Z' WHERE id = 'acc_w1e_expired'`); err != nil {
		t.Fatalf("种子过期账户: %v", err)
	}
	expiredCandidate := errorPolicyAccount(nil)
	expiredCandidate.ID = "acc_w1e_expired"
	changed, status, err = fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, expiredCandidate,
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || !changed || status != cooldownStatusRateLimited {
		t.Fatalf("过期分支 = %v/%s/%v", changed, status, err)
	}
	row := fixture.accountRow(t, "acc_w1e_expired")
	if row["status"] != "disabled" || row["schedulable"] != 0 || row["cooldown_until"] != "" || row["last_error_code"] != "account_expired" {
		t.Fatalf("过期行 = %+v", row)
	}
	var updatedAt, errorMessage string
	if err := fixture.db.QueryRow(`SELECT updated_at, last_error_message FROM accounts WHERE id = 'acc_w1e_expired'`).Scan(&updatedAt, &errorMessage); err != nil {
		t.Fatalf("读过期行: %v", err)
	}
	if updatedAt != "2026-09-01T10:00:00.000Z" || !strings.Contains(errorMessage, "账户套餐已过期") {
		t.Fatalf("过期行 updated_at/message = %q/%q", updatedAt, errorMessage)
	}

	// rate_limited 决策缺冷却值：兜底 now+1 分钟；traceID 落库。
	fixture.seedAccount(t, "acc_w1e_nofall", "active", 1)
	noFallback := errorPolicyAccount(nil)
	noFallback.ID = "acc_w1e_nofall"
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, noFallback, accountErrorPolicyDecision{
		Action: decisionActionCooldown, RuleName: "限流", RuleSource: "account",
		CooldownStatus: cooldownStatusRateLimited,
	}, chainErrorPolicyFailureInput{TraceID: "trace-w1e"})
	if err != nil || !changed {
		t.Fatalf("兜底冷却 = %v/%v", changed, err)
	}
	row = fixture.accountRow(t, "acc_w1e_nofall")
	if row["cooldown_until"] != "2026-09-01T10:01:00.000Z" {
		t.Fatalf("兜底冷却值 = %v", row["cooldown_until"])
	}
	var traceID sql.NullString
	if err := fixture.db.QueryRow(`SELECT last_error_trace_id FROM accounts WHERE id = 'acc_w1e_nofall'`).Scan(&traceID); err != nil {
		t.Fatalf("读 trace: %v", err)
	}
	if !traceID.Valid || traceID.String != "trace-w1e" {
		t.Fatalf("trace = %+v", traceID)
	}

	// 遗留策略前缀消息（last_error_code 为 NULL）同样围栏 generic 冷却。
	fixture.seedAccount(t, "acc_w1e_legacy", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET last_error_code = NULL, last_error_message = ?,
		cooldown_until = '2026-09-01T11:30:00.000Z' WHERE id = 'acc_w1e_legacy'`,
		legacyExplicitPolicyMessagePrefix+"旧规则」命中；上游调用失败：HTTP 429"); err != nil {
		t.Fatalf("种子遗留冷却: %v", err)
	}
	legacyCandidate := errorPolicyAccount(nil)
	legacyCandidate.ID = "acc_w1e_legacy"
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, legacyCandidate,
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("遗留围栏 = %v/%v, want false", changed, err)
	}

	// 非冷却类错误码 + 非遗留消息：不触发优先级围栏，generic 冷却照常写入。
	// 【已知 SQL 三值逻辑空洞，仅记录不在测试中固化】当 last_error_code 为
	// NULL 且消息非遗留前缀、cooldown_until 在未来时，NULL IN (...) 产生
	// NULL、NOT(NULL)=NULL，行被排除，generic 冷却被静默跳过；Node 侧
	// includes(null) 为 false 应放行。修复裁决后可补 NULL 行放行断言。
	fixture.seedAccount(t, "acc_w1e_plain", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET last_error_code = 'http_500', last_error_message = '普通失败',
		cooldown_until = '2026-09-01T11:30:00.000Z' WHERE id = 'acc_w1e_plain'`); err != nil {
		t.Fatalf("种子普通冷却: %v", err)
	}
	plainCandidate := errorPolicyAccount(nil)
	plainCandidate.ID = "acc_w1e_plain"
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, plainCandidate,
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || !changed {
		t.Fatalf("普通消息冷却 = %v/%v", changed, err)
	}

	// 直接调用 markCooldown：超长错误码截断 120、空 reason/trace 保持 NULL。
	fixture.seedAccount(t, "acc_w1e_trim", "active", 1)
	trimCandidate := errorPolicyAccount(nil)
	trimCandidate.ID = "acc_w1e_trim"
	changed, err = fixture.bridge.markCooldown(ctx, trimCandidate, systemQuotaDecisionOf("explicit_reset"),
		"", strings.Repeat("x", 130), "   ", nil, nil)
	if err != nil || !changed {
		t.Fatalf("markCooldown = %v/%v", changed, err)
	}
	var errorCode sql.NullString
	if err := fixture.db.QueryRow(`SELECT last_error_code FROM accounts WHERE id = 'acc_w1e_trim'`).Scan(&errorCode); err != nil {
		t.Fatalf("读错误码: %v", err)
	}
	if !errorCode.Valid || len(errorCode.String) != 120 {
		t.Fatalf("错误码截断 = %+v（长度 %d）", errorCode, len(errorCode.String))
	}
	if _, err := fixture.db.Exec(`SELECT last_error_message FROM accounts WHERE id = 'acc_w1e_trim'`); err != nil {
		t.Fatalf("读消息列: %v", err)
	}
	var message, trace sql.NullString
	if err := fixture.db.QueryRow(`SELECT last_error_message, last_error_trace_id FROM accounts WHERE id = 'acc_w1e_trim'`).Scan(&message, &trace); err != nil {
		t.Fatalf("读空列: %v", err)
	}
	if message.Valid || trace.Valid {
		t.Fatalf("空 reason/trace = %+v/%+v, want NULL", message, trace)
	}

	// rowCountOf：非 nil 结果取 RowsAffected。
	result, err := fixture.db.Exec(`UPDATE accounts SET updated_at = updated_at WHERE id = 'acc_w1e_trim'`)
	if err != nil {
		t.Fatalf("执行 UPDATE: %v", err)
	}
	if rowCountOf(result) != 1 {
		t.Fatalf("rowCountOf = %d, want 1", rowCountOf(result))
	}
}

// ---------------------------------------------------------------------------
// 授权绑定变体写入（bindingGuardSQL / bindingExistsSQL 路径）
// ---------------------------------------------------------------------------

func TestW1EEffectsBindingWrites(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	ctx := context.Background()
	bindRow := func(t *testing.T, id, status string) {
		t.Helper()
		fixture.seedAccount(t, id, status, 1)
		if _, err := fixture.db.Exec(`UPDATE accounts SET system_account_id = 'sys-bind',
			authorization_instance_authorization_id = 'authz-bind' WHERE id = ?`, id); err != nil {
			t.Fatalf("绑定行更新: %v", err)
		}
	}
	bindGroup := func(t *testing.T, id string, enabled int) {
		t.Helper()
		if _, err := fixture.db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, account_authorization_id)
			VALUES ('grp-bind', 'sys-bind', ?, ?, 'authz-bind')`, id, enabled); err != nil {
			t.Fatalf("种子 group_accounts: %v", err)
		}
	}

	// 绑定冷却：EXISTS 命中 → 写入。
	bindRow(t, "acc_w1e_bindcool", "active")
	bindGroup(t, "acc_w1e_bindcool", 1)
	changed, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, w1eBindingAccount("acc_w1e_bindcool"),
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || !changed {
		t.Fatalf("绑定冷却 = %v/%v", changed, err)
	}
	if row := fixture.accountRow(t, "acc_w1e_bindcool"); row["status"] != "rate_limited" {
		t.Fatalf("绑定冷却行 = %+v", row)
	}

	// 绑定冷却：EXISTS 未命中（无 group_accounts 行）→ 拒绝。
	bindRow(t, "acc_w1e_bindmiss", "active")
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, w1eBindingAccount("acc_w1e_bindmiss"),
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("绑定缺失冷却 = %v/%v, want false", changed, err)
	}
	if row := fixture.accountRow(t, "acc_w1e_bindmiss"); row["status"] != "active" {
		t.Fatalf("绑定缺失行 = %+v", row)
	}

	// 绑定冷却：group_accounts 停用（enabled=0）→ 拒绝。
	bindRow(t, "acc_w1e_bindoff", "active")
	bindGroup(t, "acc_w1e_bindoff", 0)
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, w1eBindingAccount("acc_w1e_bindoff"),
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("停用绑定冷却 = %v/%v, want false", changed, err)
	}

	// 绑定禁用：命中 → status=error + upstream_failure。
	bindRow(t, "acc_w1e_binddis", "active")
	bindGroup(t, "acc_w1e_binddis", 1)
	disable := accountErrorPolicyDecision{Action: decisionActionDisable, RuleName: "崩溃禁用", RuleSource: "account"}
	changed, status, err := fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, w1eBindingAccount("acc_w1e_binddis"),
		disable, chainErrorPolicyFailureInput{HasStatusCode: true, StatusCode: 500})
	if err != nil || !changed || status != "error" {
		t.Fatalf("绑定禁用 = %v/%s/%v", changed, status, err)
	}
	if row := fixture.accountRow(t, "acc_w1e_binddis"); row["status"] != "error" || row["last_error_code"] != "upstream_failure" {
		t.Fatalf("绑定禁用行 = %+v", row)
	}

	// 绑定禁用：行已是 disabled（status <> 'disabled' 守卫）→ 拒绝。
	bindRow(t, "acc_w1e_binddis2", "disabled")
	bindGroup(t, "acc_w1e_binddis2", 1)
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(ctx, w1eBindingAccount("acc_w1e_binddis2"),
		disable, chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("disabled 绑定禁用 = %v/%v, want false", changed, err)
	}
	if row := fixture.accountRow(t, "acc_w1e_binddis2"); row["status"] != "disabled" {
		t.Fatalf("disabled 绑定行 = %+v", row)
	}
}

// ---------------------------------------------------------------------------
// RecordKeyScopedQuotaFailure 守卫与模式映射
// ---------------------------------------------------------------------------

func TestW1EEffectsKeyScopedArms(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	ctx := context.Background()
	countStates := func(t *testing.T) int {
		t.Helper()
		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM account_api_key_runtime_states`).Scan(&count); err != nil {
			t.Fatalf("计数 Key 状态: %v", err)
		}
		return count
	}

	// 无指纹候选：直接跳过，不产生行。
	if err := fixture.bridge.RecordKeyScopedQuotaFailure(ctx, errorPolicyAccount(map[string]any{"api_keys": []any{"k1"}}),
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{}); err != nil {
		t.Fatalf("无指纹记录 = %v", err)
	}
	if countStates(t) != 0 {
		t.Fatal("无指纹候选不得写行")
	}

	// 运行态已停用：跳过。
	fingerprint := fixture.keyStates.FingerprintAPIKey("key-w1e")
	disabledCandidate := errorPolicyAccount(map[string]any{"api_keys": []any{"key-w1e", "key-b"}})
	disabledCandidate.SystemAccountID = "sys_owner"
	disabledCandidate.SelectedAPIKeyFingerprint = &fingerprint
	disabledCandidate.APIKeyRuntimeStateDisabled = true
	if err := fixture.bridge.RecordKeyScopedQuotaFailure(ctx, disabledCandidate,
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{}); err != nil {
		t.Fatalf("停用态记录 = %v", err)
	}
	if countStates(t) != 0 {
		t.Fatal("停用态候选不得写行")
	}

	// explicit_reset 模式：恢复码映射 + AttemptStartedAtMs 覆盖 observedAt。
	// 注意 store 的 normalizeObservedAt 会把晚于自身时钟的观察时刻钳回 now，
	// 因此起点取固定时钟前 5 分钟（09:55），与缺省 10:00 可区分。
	fixture.seedAccount(t, "acc_w1e_key", "active", 1)
	if _, err := fixture.db.Exec(`INSERT INTO account_api_key_runtime_states
		(id, system_account_id, account_id, key_fingerprint, status, created_at, updated_at)
		VALUES ('w1e_state', 'sys_owner', 'acc_w1e_key', ?, 'active', '2026-09-01T09:00:00.000Z', '2026-09-01T09:00:00.000Z')`, fingerprint); err != nil {
		t.Fatalf("种子 Key 状态: %v", err)
	}
	activeCandidate := disabledCandidate
	activeCandidate.APIKeyRuntimeStateDisabled = false
	activeCandidate.ID = "acc_w1e_key"
	input := chainErrorPolicyFailureInput{
		HasStatusCode:        true,
		StatusCode:           402,
		UpstreamErrorSummary: "insufficient quota",
		AttemptStartedAtMs:   w1ePolicyClock().Add(-5 * time.Minute).UnixMilli(),
	}
	if err := fixture.bridge.RecordKeyScopedQuotaFailure(ctx, activeCandidate,
		systemQuotaDecisionOf("explicit_reset"), input); err != nil {
		t.Fatalf("explicit_reset 记录 = %v", err)
	}
	var status, errorCode, attemptAt string
	if err := fixture.db.QueryRow(`SELECT status, last_error_code, COALESCE(last_attempt_at, '')
		FROM account_api_key_runtime_states WHERE account_id = 'acc_w1e_key' AND key_fingerprint = ?`,
		fingerprint).Scan(&status, &errorCode, &attemptAt); err != nil {
		t.Fatalf("读 Key 状态: %v", err)
	}
	if status != "rate_limited" || errorCode != "api_key_quota_insufficient_reset" {
		t.Fatalf("explicit_reset 行 = %s/%s", status, errorCode)
	}
	if attemptAt != "2026-09-01T09:55:00.000Z" {
		t.Fatalf("observedAt = %q, want 决策起点 09:55", attemptAt)
	}

	// 空模式回落 generic 恢复码。显式重置后的同一行带恢复窗口，generic 失败
	// 会被 store 的窗口围栏拦下（与账户级围栏同语义），因此换一个独立账户行；
	// 指纹必须取自候选 APIKeys 列表（ResolveTarget 校验成员资格）。
	secondFingerprint := fixture.keyStates.FingerprintAPIKey("key-b")
	fixture.seedAccount(t, "acc_w1e_key2", "active", 1)
	if _, err := fixture.db.Exec(`INSERT INTO account_api_key_runtime_states
		(id, system_account_id, account_id, key_fingerprint, status, created_at, updated_at)
		VALUES ('w1e_state2', 'sys_owner', 'acc_w1e_key2', ?, 'active', '2026-09-01T09:00:00.000Z', '2026-09-01T09:00:00.000Z')`, secondFingerprint); err != nil {
		t.Fatalf("种子第二把 Key: %v", err)
	}
	secondCandidate := activeCandidate
	secondCandidate.ID = "acc_w1e_key2"
	secondCandidate.SelectedAPIKeyFingerprint = &secondFingerprint
	if err := fixture.bridge.RecordKeyScopedQuotaFailure(ctx, secondCandidate,
		accountErrorPolicyDecision{Action: decisionActionCooldown, CooldownUntil: "2026-09-01T11:00:00.000Z"}, input); err != nil {
		t.Fatalf("空模式记录 = %v", err)
	}
	if err := fixture.db.QueryRow(`SELECT last_error_code FROM account_api_key_runtime_states
		WHERE account_id = 'acc_w1e_key2' AND key_fingerprint = ?`, secondFingerprint).Scan(&errorCode); err != nil {
		t.Fatalf("读恢复码: %v", err)
	}
	if errorCode != "api_key_quota_insufficient" {
		t.Fatalf("generic 恢复码 = %q", errorCode)
	}
}

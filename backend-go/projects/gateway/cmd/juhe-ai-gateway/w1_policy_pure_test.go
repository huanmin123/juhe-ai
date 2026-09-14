package main

// w1: 错误策略面纯函数收割——系统额度规则匹配（system-rules 注册表）、
// 规则读取归一的逐字段校验臂、错误标识归一。

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

func TestW1NormalizeErrorIdentifier(t *testing.T) {
	// Node: trim().toLowerCase().replace(/[\s-]+/g,'_')。
	if got := normalizeErrorIdentifier("  Insufficient-Quota  "); got != "insufficient_quota" {
		t.Fatalf("normalize = %q", got)
	}
	if got := normalizeErrorIdentifier("A\t\nB"); got != "a_b" {
		t.Fatalf("mixed whitespace = %q", got)
	}
	if got := normalizeErrorIdentifier("  "); got != "" {
		t.Fatalf("blank = %q", got)
	}
	if got := seedKeyPart(nil); got != "account" {
		t.Fatalf("nil fingerprint = %q", got)
	}
	blank := "  "
	if got := seedKeyPart(&blank); got != "account" {
		t.Fatalf("blank fingerprint = %q", got)
	}
	fingerprint := " fp_1 "
	if got := seedKeyPart(&fingerprint); got != "fp_1" {
		t.Fatalf("fingerprint = %q", got)
	}
	if got := formatPriority(12.7); got != "12" {
		t.Fatalf("priority = %q", got)
	}
	if got := responseHeaderMapOf(nil); got != nil {
		t.Fatalf("nil headers = %v", got)
	}
	header := http.Header{"Retry-After": {"30"}, "X-Empty": {}}
	projected := responseHeaderMapOf(header)
	if projected["retry-after"] != "30" {
		t.Fatalf("projected = %v", projected)
	}
	if _, exists := projected["X-Empty"]; exists {
		t.Fatal("空值头必须跳过")
	}
}

func TestW1SystemQuotaRuleMatcherArms(t *testing.T) {
	// 非 402/403 一律不命中。
	if systemInsufficientQuotaRuleMatches(http.StatusTooManyRequests, "insufficient_quota", "", "") {
		t.Fatal("429 不得命中系统额度规则")
	}
	// 402 + 无标识：宽匹配命中（Node 语义）。
	if !systemInsufficientQuotaRuleMatches(http.StatusPaymentRequired, "", "", "") {
		t.Fatal("402 裸失败必须命中")
	}
	// code 含 quota。
	if !systemInsufficientQuotaRuleMatches(http.StatusForbidden, "billing_quota_exceeded", "", "") {
		t.Fatal("quota code 必须命中")
	}
	// 非额度类 403 标识排除（Node nonQuota403ErrorIdentifiers）。
	for _, identifier := range nonQuota403ErrorIdentifiers {
		if systemInsufficientQuotaRuleMatches(http.StatusForbidden, identifier, "", "") {
			t.Fatalf("排除标识 %s 不得命中", identifier)
		}
	}
	// 文本标记命中（insufficientQuotaTextMarkers 精确标记）。
	marker := insufficientQuotaTextMarkers[0]
	if !systemInsufficientQuotaRuleMatches(http.StatusPaymentRequired, "", "", "上游返回 "+marker) {
		t.Fatalf("文本标记 %q 必须命中", marker)
	}
	// 无任何标记的 403：不命中。
	if systemInsufficientQuotaRuleMatches(http.StatusForbidden, "", "", "普通失败") {
		t.Fatal("无标记 403 不得命中")
	}
}

func w1RuleBase() map[string]any {
	return map[string]any{
		"enabled": true, "name": " 规则一 ", "priority": 10.0, "action": "retry_next",
		"status_codes": []any{500.0},
	}
}

func TestW1RuleReadFieldValidation(t *testing.T) {
	// 非法启用状态 / 名称 / 优先级。
	badBool := w1RuleBase()
	badBool["enabled"] = "yes"
	if _, err := accountErrorHandlingRuleRead(badBool, 1); err == nil || !strings.Contains(err.Error(), "布尔值") {
		t.Fatalf("enabled = %v", err)
	}
	badName := w1RuleBase()
	badName["name"] = "   "
	if _, err := accountErrorHandlingRuleRead(badName, 1); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("name = %v", err)
	}
	badPriority := w1RuleBase()
	badPriority["priority"] = 1.5
	if _, err := accountErrorHandlingRuleRead(badPriority, 1); err == nil || !strings.Contains(err.Error(), "大于 0 的整数") {
		t.Fatalf("priority = %v", err)
	}
	// 非法动作。
	badAction := w1RuleBase()
	badAction["action"] = "explode"
	if _, err := accountErrorHandlingRuleRead(badAction, 1); err == nil || !strings.Contains(err.Error(), "动作无效") {
		t.Fatalf("action = %v", err)
	}
	// error_codes 填 2xx 成功码：拒绝。
	badCode := w1RuleBase()
	badCode["error_codes"] = []any{"200"}
	if _, err := accountErrorHandlingRuleRead(badCode, 1); err == nil || !strings.Contains(err.Error(), "2xx") {
		t.Fatalf("2xx error code = %v", err)
	}
	// 名称与关键字段归一：trim 生效。
	rule, err := accountErrorHandlingRuleRead(w1RuleBase(), 1)
	if err != nil || rule.Name != "规则一" || rule.Priority != 10 || rule.Action != "retry_next" {
		t.Fatalf("rule = %+v, %v", rule, err)
	}
}

func TestW1RuleReadResetStrategies(t *testing.T) {
	// 限流规则：duration / daily / weekly 三种恢复策略链接。
	duration := w1RuleBase()
	duration["action"] = "rate_limited"
	duration["reset_strategy"] = "duration"
	duration["duration_hours"] = 2.0
	duration["status_codes"] = []any{429.0}
	durationRule, err := accountErrorHandlingRuleRead(duration, 1)
	if err != nil || durationRule.ResetStrategy != "duration" || durationRule.DurationHours != 2 {
		t.Fatalf("duration rule = %+v, %v", durationRule, err)
	}
	daily := w1RuleBase()
	daily["action"] = "rate_limited"
	daily["reset_strategy"] = "daily"
	daily["daily_reset_hour"] = 6.0
	dailyRule, err := accountErrorHandlingRuleRead(daily, 1)
	if err != nil || dailyRule.DailyResetHour != 6 {
		t.Fatalf("daily rule = %+v, %v", dailyRule, err)
	}
	weekly := w1RuleBase()
	weekly["action"] = "rate_limited"
	weekly["reset_strategy"] = "weekly"
	weekly["weekly_reset_day"] = 3.0
	weekly["weekly_reset_hour"] = 8.0
	weeklyRule, err := accountErrorHandlingRuleRead(weekly, 1)
	if err != nil || weeklyRule.WeeklyResetDay != 3 || weeklyRule.WeeklyResetHour != 8 {
		t.Fatalf("weekly rule = %+v, %v", weeklyRule, err)
	}
	// 缺失恢复字段：报错。
	missingHours := w1RuleBase()
	missingHours["action"] = "rate_limited"
	missingHours["reset_strategy"] = "duration"
	if _, err := accountErrorHandlingRuleRead(missingHours, 1); err == nil {
		t.Fatal("缺 duration_hours 必须报错")
	}
	missingDay := w1RuleBase()
	missingDay["action"] = "rate_limited"
	missingDay["reset_strategy"] = "weekly"
	missingDay["weekly_reset_hour"] = 8.0
	if _, err := accountErrorHandlingRuleRead(missingDay, 1); err == nil {
		t.Fatal("缺 weekly_reset_day 必须报错")
	}
	// 越界恢复字段。
	badHour := w1RuleBase()
	badHour["action"] = "rate_limited"
	badHour["reset_strategy"] = "daily"
	badHour["daily_reset_hour"] = 24.0
	if _, err := accountErrorHandlingRuleRead(badHour, 1); err == nil || !strings.Contains(err.Error(), "0-23") {
		t.Fatalf("hour = %v", err)
	}
	badDay := w1RuleBase()
	badDay["action"] = "rate_limited"
	badDay["reset_strategy"] = "weekly"
	badDay["weekly_reset_day"] = 7.0
	if _, err := accountErrorHandlingRuleRead(badDay, 1); err == nil || !strings.Contains(err.Error(), "0-6") {
		t.Fatalf("weekday = %v", err)
	}
}

func TestW1RuleReadListHelpers(t *testing.T) {
	// 状态码数组：非数字 / 越界 / 2xx / 去重。
	badStatusType := w1RuleBase()
	badStatusType["status_codes"] = "500"
	if _, err := accountErrorHandlingRuleRead(badStatusType, 1); err == nil || !strings.Contains(err.Error(), "数字数组") {
		t.Fatalf("status type = %v", err)
	}
	badStatusRange := w1RuleBase()
	badStatusRange["status_codes"] = []any{99.0}
	if _, err := accountErrorHandlingRuleRead(badStatusRange, 1); err == nil || !strings.Contains(err.Error(), "不合法") {
		t.Fatalf("status range = %v", err)
	}
	badStatus2xx := w1RuleBase()
	badStatus2xx["status_codes"] = []any{200.0}
	if _, err := accountErrorHandlingRuleRead(badStatus2xx, 1); err == nil || !strings.Contains(err.Error(), "2xx") {
		t.Fatalf("status 2xx = %v", err)
	}
	dupStatus := w1RuleBase()
	dupStatus["status_codes"] = []any{500.0, 500.0, 503.0}
	dupRule, err := accountErrorHandlingRuleRead(dupStatus, 1)
	if err != nil || len(dupRule.StatusCodes) != 2 {
		t.Fatalf("dedupe = %+v, %v", dupRule, err)
	}
	// 字符串数组：nil 放行、非数组拒绝、空白拒绝。
	emptyRule := w1RuleBase()
	emptyRule["error_codes"] = nil
	if _, err := accountErrorHandlingRuleRead(emptyRule, 1); err != nil {
		t.Fatalf("nil list = %v", err)
	}
	badList := w1RuleBase()
	badList["keywords"] = "keyword"
	if _, err := accountErrorHandlingRuleRead(badList, 1); err == nil || !strings.Contains(err.Error(), "字符串数组") {
		t.Fatalf("keywords type = %v", err)
	}
	blankItem := w1RuleBase()
	blankItem["keywords"] = []any{"  "}
	if _, err := accountErrorHandlingRuleRead(blankItem, 1); err == nil {
		t.Fatal("空白关键字必须拒绝")
	}
	// 读取助手直测。
	if !readTextEqual("system", "system") || readTextEqual("system", "user") || readTextEqual(1, "1") {
		t.Fatal("readTextEqual 语义错误")
	}
	if !readBoolEqual(true, true) || readBoolEqual(false, true) || readBoolEqual("true", true) {
		t.Fatal("readBoolEqual 语义错误")
	}
	if flag, err := readRequiredBool(false, "x"); err != nil || flag {
		t.Fatalf("readRequiredBool = %v, %v", flag, err)
	}
	if _, err := readRequiredBool(1, "标签"); err == nil || !strings.Contains(err.Error(), "布尔值") {
		t.Fatalf("readRequiredBool err = %v", err)
	}
	if got, err := readRequiredString(" 甲 ", "标签"); err != nil || got != "甲" {
		t.Fatalf("readRequiredString = %q, %v", got, err)
	}
	if got, err := readRequiredPositiveInt(3.0, "标签"); err != nil || got != 3 {
		t.Fatalf("readPositiveInt = %v, %v", got, err)
	}
	if _, err := readRequiredPositiveInt(3.5, "标签"); err == nil {
		t.Fatal("小数必须拒绝")
	}
	if _, err := readHour(-1, "标签"); err == nil {
		t.Fatal("负小时必须拒绝")
	}
}

func TestW1EffectsPureHelpers(t *testing.T) {
	// 失败码选择：系统规则按额度恢复模式分流，账户规则固定码。
	systemReset := accountErrorPolicyDecision{RuleSource: "system", QuotaRecoveryMode: string(quotaRecoveryModeExplicitReset)}
	if got := explicitPolicyFailureCode(systemReset); got != systemQuotaExplicitResetCooldownCode {
		t.Fatalf("system reset code = %q", got)
	}
	systemGeneric := accountErrorPolicyDecision{RuleSource: "system"}
	if got := explicitPolicyFailureCode(systemGeneric); got != systemQuotaGenericCooldownCode {
		t.Fatalf("system generic code = %q", got)
	}
	if got := explicitPolicyFailureCode(accountErrorPolicyDecision{}); got != explicitAccountErrorPolicyCooldownCode {
		t.Fatalf("account code = %q", got)
	}
	// rune 截断。
	if got := truncateUTF8("普通文案", 2); got != "普通" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncateUTF8("短", 10); got != "短" {
		t.Fatalf("no truncate = %q", got)
	}
	// 授权绑定目标：非授权访问类型 / 缺任一指针 / 空串 → nil。
	if authorizedBindingTargetOf(gatewaydispatch.AccountCandidate{}) != nil {
		t.Fatal("非授权类型必须 nil")
	}
	emptyBinding := gatewaydispatch.AccountCandidate{
		AccountAccessType:      "account_authorized",
		BindingSystemAccountID: w1PolicyStrPtr(""),
		BoundGroupID:           w1PolicyStrPtr("grp"),
		AccountAuthorizationID: w1PolicyStrPtr("auth"),
	}
	if authorizedBindingTargetOf(emptyBinding) != nil {
		t.Fatal("空绑定必须 nil")
	}
	authorized := gatewaydispatch.AccountCandidate{
		ID:                     "acc_1",
		AccountAccessType:      "account_authorized",
		BindingSystemAccountID: w1PolicyStrPtr("sys_owner"),
		BoundGroupID:           w1PolicyStrPtr("grp_1"),
		AccountAuthorizationID: w1PolicyStrPtr("auth_1"),
	}
	target := authorizedBindingTargetOf(authorized)
	if target == nil || target.SystemAccountID != "sys_owner" || target.GroupID != "grp_1" || target.AccountAuthorizationID != "auth_1" {
		t.Fatalf("target = %+v", target)
	}
	// 字符串解引用。
	if stringValueOf(nil) != "" || stringValueOf(w1PolicyStrPtr("v")) != "v" {
		t.Fatal("stringValueOf 语义错误")
	}
	// 硬不可用状态判定。
	for _, status := range []string{"disabled", "pending_test", "error", "quality_isolated"} {
		if !isHardUnavailableAccountStatus(status) {
			t.Fatalf("%s 必须硬不可用", status)
		}
	}
	if isHardUnavailableAccountStatus("active") {
		t.Fatal("active 不得硬不可用")
	}
	// 账户过期：NULL / 空串 / 非法 / 已过 / 未过。
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	if isAccountExpired(sql.NullString{}, now) || isAccountExpired(sql.NullString{String: " ", Valid: true}, now) {
		t.Fatal("空过期时间不得判过期")
	}
	if isAccountExpired(sql.NullString{String: "not-a-time", Valid: true}, now) {
		t.Fatal("非法时间不得判过期")
	}
	if !isAccountExpired(sql.NullString{String: "2026-09-13T12:00:00Z", Valid: true}, now) {
		t.Fatal("边界时刻必须判过期")
	}
	if isAccountExpired(sql.NullString{String: "2026-09-13T12:00:01Z", Valid: true}, now) {
		t.Fatal("未到期不得判过期")
	}
	// 观察围栏：revision 缺席 → nil；有 revision → SQL 与参数成对。
	if runtimeFailureObservationGuardOf(gatewaydispatch.AccountCandidate{}, now) != nil {
		t.Fatal("无 revision 必须无围栏")
	}
	revision := int64(4)
	guard := runtimeFailureObservationGuardOf(gatewaydispatch.AccountCandidate{DispatchRevision: &revision}, now)
	if guard == nil || guard.ExpectedDispatchRevision != 4 {
		t.Fatalf("guard = %+v", guard)
	}
	if runtimeFailureObservationGuardSQL(nil) != "" || runtimeFailureObservationGuardParams(nil) != nil {
		t.Fatal("nil 围栏投影必须空")
	}
	params := runtimeFailureObservationGuardParams(guard)
	if len(params) != 3 || params[0] != int64(4) || params[1] != guard.ObservedAt {
		t.Fatalf("params = %v", params)
	}
	if runtimeFailureUpdatedAtSQL(nil) != "?" {
		t.Fatal("无围栏 updated_at 必须直赋")
	}
	updatedParams := runtimeFailureUpdatedAtParams(guard, "fallback")
	if len(updatedParams) != 2 || updatedParams[0] != guard.ObservedAt {
		t.Fatalf("updated params = %v", updatedParams)
	}
	if got := runtimeFailureUpdatedAtParams(nil, "fallback"); len(got) != 1 || got[0] != "fallback" {
		t.Fatalf("fallback params = %v", got)
	}
}

func w1PolicyStrPtr(value string) *string { return &value }

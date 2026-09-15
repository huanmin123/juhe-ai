package main

// w1_error_policy_arms_test.go —— chain_error_policy.go 决策服务的补臂单测。
// 只覆盖 chain_failure_dispatch_test.go 未触及的分支：读取归一辅助函数、
// 规则/覆盖校验矩阵、匹配器、冷却边界、被动确定性抖动、额度恢复策略、
// 上游恢复 hint 解析器与 Decide 的错误上抛 / 非默认账户类型路径。
// 固定时钟注入（w1ePolicyClock）保证确定性可重放；断言只用 stdlib。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w1ePolicyClock 固定决策时钟：2026-09-01T10:00:00Z（周二）。
func w1ePolicyClock() time.Time {
	return time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
}

func w1eParseUntil(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("解析冷却时间 %q: %v", value, err)
	}
	return parsed
}

// w1eAssertWithin 断言冷却时间落在 center±window 内且不等于中心（确定性
// 抖动必须把边界移离精确时刻，offset==0 时实现改写为 1ms）。
func w1eAssertWithin(t *testing.T, value string, center time.Time, window time.Duration) {
	t.Helper()
	parsed := w1eParseUntil(t, value)
	delta := parsed.Sub(center)
	if delta > window || delta < -window {
		t.Fatalf("冷却时间 %s 偏离中心 %s 超出 ±%s（delta=%v）", value, center.Format(rfc3339MillisUTC), window, delta)
	}
	if delta == 0 {
		t.Fatalf("冷却时间 %s 等于中心值，确定性抖动必须移动边界", value)
	}
}

// w1eParsedBody 以 encoding/json 构造 Decide 的 parsedBody 输入（与生产
// 非流式 JSON 体解析同构的 map 投影）。
func w1eParsedBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("解析失败体 %q: %v", body, err)
	}
	return parsed
}

// ---------------------------------------------------------------------------
// 读取归一标量辅助（readTextEqual / readBoolEqual / readRequiredBool /
// readRequiredString / readRequiredPositiveInt / parseFloat64 / isAllDigits）
// ---------------------------------------------------------------------------

func TestW1EPolicyScalarReaders(t *testing.T) {
	if !readTextEqual("system", "system") || readTextEqual("account", "system") || readTextEqual(42, "system") || readTextEqual(nil, "system") {
		t.Fatalf("readTextEqual 分支错误")
	}
	if !readBoolEqual(true, true) || !readBoolEqual(false, false) || readBoolEqual(true, false) || readBoolEqual(false, true) || readBoolEqual("true", true) || readBoolEqual(nil, true) {
		t.Fatalf("readBoolEqual 分支错误")
	}
	if flag, err := readRequiredBool(true, "x"); err != nil || !flag {
		t.Fatalf("readRequiredBool(true) = %v/%v", flag, err)
	}
	if flag, err := readRequiredBool(false, "x"); err != nil || flag {
		t.Fatalf("readRequiredBool(false) = %v/%v", flag, err)
	}
	for _, value := range []any{"true", 1, nil, 1.5} {
		if _, err := readRequiredBool(value, "标签"); err == nil || !strings.Contains(err.Error(), "必须是布尔值") {
			t.Fatalf("readRequiredBool(%v) = %v, want 布尔错误", value, err)
		}
	}
	if text, err := readRequiredString("  值  ", "标签"); err != nil || text != "值" {
		t.Fatalf("readRequiredString 修剪 = %q/%v", text, err)
	}
	for _, value := range []any{"", "   ", nil, 42} {
		if _, err := readRequiredString(value, "标签"); err == nil || !strings.Contains(err.Error(), "不能为空") {
			t.Fatalf("readRequiredString(%v) = %v, want 非空错误", value, err)
		}
	}
	// 只接受 JSON float64：int 传入必须失败。
	if number, err := readRequiredPositiveInt(float64(3), "标签"); err != nil || number != 3 {
		t.Fatalf("readRequiredPositiveInt(3.0) = %v/%v", number, err)
	}
	for _, value := range []any{1, float64(0), float64(-2), float64(2.5), "3", nil} {
		if _, err := readRequiredPositiveInt(value, "标签"); err == nil || !strings.Contains(err.Error(), "大于 0 的整数") {
			t.Fatalf("readRequiredPositiveInt(%v) = %v, want 正整数错误", value, err)
		}
	}
	if number, err := parseFloat64(" 12.5 "); err != nil || number != 12.5 {
		t.Fatalf("parseFloat64 = %v/%v", number, err)
	}
	if _, err := parseFloat64("abc"); err == nil {
		t.Fatalf("parseFloat64(abc) 必须报错")
	}
	if !isAllDigits("0429") || !isAllDigits("9") {
		t.Fatal("isAllDigits 纯数字必须为真")
	}
	for _, text := range []string{"", "12a", "1 2", "-1", "四"} {
		if isAllDigits(text) {
			t.Fatalf("isAllDigits(%q) 必须为假", text)
		}
	}
}

// ---------------------------------------------------------------------------
// 列表读取（readStatusCodes / readStringList）与小时/星期读取
// ---------------------------------------------------------------------------

func TestW1EPolicyListReaders(t *testing.T) {
	codes, err := readStatusCodes(nil, 1)
	if err != nil || codes != nil {
		t.Fatalf("readStatusCodes(nil) = %v/%v", codes, err)
	}
	codes, err = readStatusCodes([]any{float64(429), float64(500), float64(429)}, 1)
	if err != nil || len(codes) != 2 || codes[0] != 429 || codes[1] != 500 {
		t.Fatalf("readStatusCodes 去重 = %v/%v", codes, err)
	}
	for _, value := range []any{float64(429), "429", 429} {
		if _, err := readStatusCodes(value, 1); err == nil || !strings.Contains(err.Error(), "数字数组") {
			t.Fatalf("readStatusCodes(%v) = %v, want 数组错误", value, err)
		}
	}
	for _, item := range []any{
		[]any{"429"}, []any{float64(99)}, []any{float64(600)},
		[]any{float64(429.5)}, []any{float64(200)}, []any{float64(299)},
	} {
		if _, err := readStatusCodes(item, 1); err == nil {
			t.Fatalf("readStatusCodes(%v) 必须报错", item)
		}
	}
	list, err := readStringList(nil, "标签")
	if err != nil || list != nil {
		t.Fatalf("readStringList(nil) = %v/%v", list, err)
	}
	list, err = readStringList([]any{" A ", "A", "B"}, "标签")
	if err != nil || len(list) != 2 || list[0] != "A" || list[1] != "B" {
		t.Fatalf("readStringList 修剪去重 = %v/%v", list, err)
	}
	for _, value := range []any{[]any{" "}, []any{42}, "A", 42} {
		if _, err := readStringList(value, "标签"); err == nil || !strings.Contains(err.Error(), "字符串数组") {
			t.Fatalf("readStringList(%v) = %v, want 字符串数组错误", value, err)
		}
	}
}

func TestW1EPolicyHourWeekdayReaders(t *testing.T) {
	for _, value := range []any{float64(0), float64(23)} {
		hour, err := readHour(value, "标签")
		if err != nil || hour != value {
			t.Fatalf("readHour(%v) = %v/%v", value, hour, err)
		}
	}
	for _, value := range []any{float64(24), float64(-1), float64(1.5), "3", nil, 3} {
		if _, err := readHour(value, "标签"); err == nil || !strings.Contains(err.Error(), "0-23 的整数") {
			t.Fatalf("readHour(%v) = %v, want 小时错误", value, err)
		}
	}
	for _, value := range []any{float64(0), float64(6)} {
		day, err := readWeekday(value, "标签")
		if err != nil || day != value {
			t.Fatalf("readWeekday(%v) = %v/%v", value, day, err)
		}
	}
	for _, value := range []any{float64(7), float64(-1), float64(0.5), "1", nil} {
		if _, err := readWeekday(value, "标签"); err == nil || !strings.Contains(err.Error(), "0-6 的整数") {
			t.Fatalf("readWeekday(%v) = %v, want 星期错误", value, err)
		}
	}
}

// ---------------------------------------------------------------------------
// accountErrorHandlingRulesRead / accountErrorHandlingRuleRead 校验矩阵
// ---------------------------------------------------------------------------

func TestW1EHandlingRulesReadArms(t *testing.T) {
	rules, err := accountErrorHandlingRulesRead(nil)
	if err != nil || rules != nil {
		t.Fatalf("accountErrorHandlingRulesRead(nil) = %v/%v", rules, err)
	}
	if _, err := accountErrorHandlingRulesRead("nope"); err == nil || !strings.Contains(err.Error(), "规则格式无效") {
		t.Fatalf("非数组规则 = %v, want 格式错误", err)
	}
	if _, err := accountErrorHandlingRulesRead([]any{42}); err == nil || !strings.Contains(err.Error(), "第 1 条") {
		t.Fatalf("非对象规则项 = %v, want 第 1 条错误", err)
	}

	cases := []struct {
		name  string
		rule  map[string]any
		extra []any
		want  string
	}{
		{"系统继承来源", map[string]any{"source": "system", "enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "不能写入系统继承规则"},
		{"继承标记", map[string]any{"inherited": true, "enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "不能写入系统继承规则"},
		{"不可编辑标记", map[string]any{"editable": false, "enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "不能写入系统继承规则"},
		{"不支持字段", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}, "proxy": "x"}, nil, "不支持字段"},
		{"缺 enabled", map[string]any{"name": "x", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "启用状态"},
		{"enabled 非布尔", map[string]any{"enabled": "true", "name": "x", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "启用状态"},
		{"缺名称", map[string]any{"enabled": true, "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "规则名称"},
		{"名称空白", map[string]any{"enabled": true, "name": "   ", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "规则名称"},
		{"优先级为零", map[string]any{"enabled": true, "name": "x", "priority": float64(0), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "优先级"},
		{"优先级非整数", map[string]any{"enabled": true, "name": "x", "priority": float64(2.5), "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "优先级"},
		{"优先级字符串", map[string]any{"enabled": true, "name": "x", "priority": "1", "action": "retry_next", "status_codes": []any{float64(500)}}, nil, "优先级"},
		{"动作缺失", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "status_codes": []any{float64(500)}}, nil, "错误处理动作无效"},
		{"动作无效", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "explode", "status_codes": []any{float64(500)}}, nil, "错误处理动作无效"},
		{"状态码非数组", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "status_codes": 429}, nil, "状态码"},
		{"错误码 2xx", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "error_codes": []any{"200"}}, nil, "错误码不能填写 2xx"},
		{"无限流策略", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "status_codes": []any{float64(429)}}, nil, "恢复策略无效"},
		{"限流策略无效", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "reset_strategy": "monthly", "status_codes": []any{float64(429)}}, nil, "恢复策略无效"},
		{"duration 缺小时", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "reset_strategy": "duration", "status_codes": []any{float64(429)}}, nil, "恢复小时数"},
		{"duration 小时为零", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "reset_strategy": "duration", "duration_hours": float64(0), "status_codes": []any{float64(429)}}, nil, "恢复小时数"},
		{"daily 缺小时", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "reset_strategy": "daily", "status_codes": []any{float64(429)}}, nil, "每日恢复小时"},
		{"daily 小时越界", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "reset_strategy": "daily", "daily_reset_hour": float64(24), "status_codes": []any{float64(429)}}, nil, "每日恢复小时"},
		{"weekly 缺天", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "reset_strategy": "weekly", "status_codes": []any{float64(429)}}, nil, "每周恢复日期"},
		{"weekly 天越界", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "reset_strategy": "weekly", "weekly_reset_day": float64(7), "weekly_reset_hour": float64(8), "status_codes": []any{float64(429)}}, nil, "每周恢复日期"},
		{"weekly 小时越界", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "rate_limited", "reset_strategy": "weekly", "weekly_reset_day": float64(5), "weekly_reset_hour": float64(25), "status_codes": []any{float64(429)}}, nil, "每周恢复小时"},
		{"无匹配条件", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next"}, nil, "至少需要一个匹配条件"},
		{"错误类型非字符串", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "error_types": []any{42}}, nil, "错误类型"},
		{"关键字空串", map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "keywords": []any{" "}}, nil, "关键字"},
	}
	for _, testCase := range cases {
		list := append([]any{testCase.rule}, testCase.extra...)
		if _, err := accountErrorHandlingRulesRead(list); err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("%s: err = %v, want 含 %q", testCase.name, err, testCase.want)
		}
	}

	// 停用规则允许零匹配条件。
	disabled, err := accountErrorHandlingRulesRead([]any{map[string]any{
		"enabled": false, "name": "停用", "priority": float64(1), "action": "retry_next",
	}})
	if err != nil || len(disabled) != 1 || disabled[0].Enabled {
		t.Fatalf("停用规则 = %v/%v", disabled, err)
	}

	// 完整 weekly 限流规则归一：修剪、去重、字段投影。
	normalized, err := accountErrorHandlingRulesRead([]any{map[string]any{
		"enabled": true, "name": "  周期限流  ", "priority": float64(3), "action": "rate_limited",
		"status_codes": []any{float64(429), float64(429)}, "error_types": []any{" RateLimit "},
		"keywords": []any{" 过载 "}, "reset_strategy": "weekly",
		"weekly_reset_day": float64(5), "weekly_reset_hour": float64(8), "description": "说明",
	}})
	if err != nil || len(normalized) != 1 {
		t.Fatalf("weekly 规则归一 = %v/%v", normalized, err)
	}
	rule := normalized[0]
	if rule.Name != "周期限流" || rule.Priority != 3 || rule.Action != "rate_limited" {
		t.Fatalf("weekly 基础字段 = %+v", rule)
	}
	if len(rule.StatusCodes) != 1 || rule.StatusCodes[0] != 429 {
		t.Fatalf("weekly 状态码去重 = %v", rule.StatusCodes)
	}
	if len(rule.ErrorTypes) != 1 || rule.ErrorTypes[0] != "RateLimit" || len(rule.Keywords) != 1 || rule.Keywords[0] != "过载" {
		t.Fatalf("weekly 匹配条件 = %v/%v", rule.ErrorTypes, rule.Keywords)
	}
	if rule.ResetStrategy != "weekly" || rule.WeeklyResetDay != 5 || rule.WeeklyResetHour != 8 {
		t.Fatalf("weekly 恢复字段 = %+v", rule)
	}
}

// ---------------------------------------------------------------------------
// accountErrorPolicyOverridesRead 校验矩阵
// ---------------------------------------------------------------------------

func TestW1EOverrideReadArms(t *testing.T) {
	overrides, err := accountErrorPolicyOverridesRead(nil)
	if err != nil || overrides != nil {
		t.Fatalf("accountErrorPolicyOverridesRead(nil) = %v/%v", overrides, err)
	}
	if _, err := accountErrorPolicyOverridesRead("nope"); err == nil || !strings.Contains(err.Error(), "覆盖格式无效") {
		t.Fatalf("非数组覆盖 = %v", err)
	}
	if _, err := accountErrorPolicyOverridesRead([]any{42}); err == nil || !strings.Contains(err.Error(), "第 1 条") {
		t.Fatalf("非对象覆盖项 = %v", err)
	}
	cases := []struct {
		name     string
		override map[string]any
		want     string
	}{
		{"规则 ID 无效", map[string]any{"system_rule_id": "other", "action": "delete"}, "系统规则 ID 无效"},
		{"动作缺失", map[string]any{"system_rule_id": systemInsufficientQuotaRuleID}, "覆盖动作无效"},
		{"动作无效", map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "mute"}, "覆盖动作无效"},
		{"delete 带索引", map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "delete", "rule_index": float64(0)}, "不支持字段"},
		{"replace 缺索引", map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace"}, "规则索引无效"},
		{"replace 索引负数", map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace", "rule_index": float64(-1)}, "规则索引无效"},
		{"replace 索引字符串", map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace", "rule_index": "0"}, "规则索引无效"},
		{"未知字段", map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "delete", "memo": "x"}, "不支持字段"},
	}
	for _, testCase := range cases {
		if _, err := accountErrorPolicyOverridesRead([]any{testCase.override}); err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("%s: err = %v, want 含 %q", testCase.name, err, testCase.want)
		}
	}
	replaced, err := accountErrorPolicyOverridesRead([]any{map[string]any{
		"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace", "rule_index": float64(2),
	}})
	if err != nil || len(replaced) != 1 || !replaced[0].HasRuleIndex || replaced[0].RuleIndex != 2 || replaced[0].Action != "replace" {
		t.Fatalf("replace 覆盖 = %v/%v", replaced, err)
	}
	deleted, err := accountErrorPolicyOverridesRead([]any{map[string]any{
		"system_rule_id": systemInsufficientQuotaRuleID, "action": "delete",
	}})
	if err != nil || len(deleted) != 1 || deleted[0].HasRuleIndex || deleted[0].Action != "delete" {
		t.Fatalf("delete 覆盖 = %v/%v", deleted, err)
	}
}

// ---------------------------------------------------------------------------
// accountErrorRuleMatches 维度矩阵
// ---------------------------------------------------------------------------

func TestW1ERuleMatchesArms(t *testing.T) {
	if !accountErrorRuleMatches(accountErrorHandlingRule{}, 500, "", "", "") {
		t.Fatal("无条件规则必须命中")
	}
	statusOnly := accountErrorHandlingRule{StatusCodes: []float64{429}}
	if !accountErrorRuleMatches(statusOnly, 429, "", "", "") {
		t.Fatal("状态码命中失败")
	}
	if accountErrorRuleMatches(statusOnly, 500, "", "", "") {
		t.Fatal("状态码不匹配必须拒绝")
	}
	codeOnly := accountErrorHandlingRule{ErrorCodes: []string{"RATE_LIMIT_EXCEEDED"}}
	if !accountErrorRuleMatches(codeOnly, 429, "rate_limit_exceeded", "", "") {
		t.Fatal("错误码大小写归一命中失败")
	}
	if accountErrorRuleMatches(codeOnly, 429, "insufficient_quota", "", "") {
		t.Fatal("错误码不匹配必须拒绝")
	}
	typeOnly := accountErrorHandlingRule{ErrorTypes: []string{"Insufficient_Quota"}}
	if !accountErrorRuleMatches(typeOnly, 402, "", "insufficient_quota", "") {
		t.Fatal("错误类型命中失败")
	}
	if accountErrorRuleMatches(typeOnly, 402, "", "rate_limit_error", "") {
		t.Fatal("错误类型不匹配必须拒绝")
	}
	// 匹配器不做关键字修剪（修剪发生在读取侧），按原串包含判断。
	keywordOnly := accountErrorHandlingRule{Keywords: []string{"系统过载"}}
	if !accountErrorRuleMatches(keywordOnly, 500, "", "", "上游 系统过载，请重试") {
		t.Fatal("关键字命中失败")
	}
	if accountErrorRuleMatches(keywordOnly, 500, "", "", "正常失败") {
		t.Fatal("关键字不匹配必须拒绝")
	}
	combined := accountErrorHandlingRule{StatusCodes: []float64{429}, ErrorCodes: []string{"rate_limit_exceeded"}}
	if accountErrorRuleMatches(combined, 429, "insufficient_quota", "", "") {
		t.Fatal("组合条件部分命中必须拒绝")
	}
	if !accountErrorRuleMatches(combined, 429, "rate_limit_exceeded", "", "") {
		t.Fatal("组合条件全命中失败")
	}
}

// ---------------------------------------------------------------------------
// accountErrorRuleCooldownUntil：duration / daily / weekly 与顺延分支
// ---------------------------------------------------------------------------

func TestW1ERuleCooldownUntilArms(t *testing.T) {
	now := w1ePolicyClock()
	if int(now.Weekday()) != 2 {
		t.Fatalf("时钟假设失败：2026-09-01 应为周二，实际 weekday=%d", int(now.Weekday()))
	}

	// duration：2 小时 + 确定性抖动（窗口 ±30 分钟）。
	duration := accountErrorHandlingRule{ResetStrategy: "duration", DurationHours: 2}
	w1eAssertWithin(t, accountErrorRuleCooldownUntil(duration, now, "seed-a"), now.Add(2*time.Hour), 30*time.Minute)

	// daily 目标小时在今天晚些（23 点）：对齐今天 23:00 ± 30 分钟。
	dailyFuture := accountErrorHandlingRule{ResetStrategy: "daily", DailyResetHour: 23}
	w1eAssertWithin(t, accountErrorRuleCooldownUntil(dailyFuture, now, "seed-b"),
		time.Date(2026, 9, 1, 23, 0, 0, 0, time.UTC), 30*time.Minute)

	// daily 目标小时等于当前小时：过去 → 顺延一天（24 小时间隔 → 窗口 ±60 分钟）。
	dailyRollover := accountErrorHandlingRule{ResetStrategy: "daily", DailyResetHour: 10}
	w1eAssertWithin(t, accountErrorRuleCooldownUntil(dailyRollover, now, "seed-c"),
		time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC), time.Hour)

	// weekly 目标（周五 8 点）在未来：只断言确定性、非精确时刻与落在未来。
	// 【疑似生产 bug，待用户裁决，不在测试中固化】chain_error_policy.go 的
	// accountErrorRuleCooldownUntil weekly 分支写 target.AddDate(daysAhead, 0, 0)，
	// 而 Go AddDate(years, months, days) 的首参是年 —— 「+3 天」被当作
	// 「+3 年」推进（2026-09-01 → 2029-09-01），weekly 限流冷却会以年为单位。
	// 正确写法应为 AddDate(0, 0, daysAhead)；修复后应把本段改回
	// w1eAssertWithin(±1h, 2026-09-04T08:00Z) 中心断言。
	weeklyFuture := accountErrorHandlingRule{ResetStrategy: "weekly", WeeklyResetDay: 5, WeeklyResetHour: 8}
	weeklyUntil := accountErrorRuleCooldownUntil(weeklyFuture, now, "seed-d")
	if weeklyUntil != accountErrorRuleCooldownUntil(weeklyFuture, now, "seed-d") {
		t.Fatalf("weekly 同种子结果不稳定：%s vs %s", weeklyUntil, accountErrorRuleCooldownUntil(weeklyFuture, now, "seed-d"))
	}
	if !w1eParseUntil(t, weeklyUntil).After(now) {
		t.Fatalf("weekly 冷却必须在未来：%s", weeklyUntil)
	}
	if w1eParseUntil(t, weeklyUntil).Equal(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly 冷却等于精确边界，确定性抖动缺失：%s", weeklyUntil)
	}

	// weekly 同日早于当前时刻（周二 8 点 < 10 点）：同上只断言诚实性质
	// （期望语义应为顺延到 9 月 8 日 08:00，受上述 AddDate 疑点影响不强断言中心）。
	weeklyRollover := accountErrorHandlingRule{ResetStrategy: "weekly", WeeklyResetDay: 2, WeeklyResetHour: 8}
	weeklyRolloverUntil := accountErrorRuleCooldownUntil(weeklyRollover, now, "seed-e")
	if weeklyRolloverUntil != accountErrorRuleCooldownUntil(weeklyRollover, now, "seed-e") {
		t.Fatalf("weekly 顺延同种子结果不稳定：%s", weeklyRolloverUntil)
	}
	if !w1eParseUntil(t, weeklyRolloverUntil).After(now) {
		t.Fatalf("weekly 顺延冷却必须在未来：%s", weeklyRolloverUntil)
	}

	// 相同输入结果稳定（确定性），不同种子几乎必然不同。
	first := accountErrorRuleCooldownUntil(dailyFuture, now, "seed-x")
	second := accountErrorRuleCooldownUntil(dailyFuture, now, "seed-x")
	if first != second {
		t.Fatalf("同种子结果不稳定：%s vs %s", first, second)
	}
}

// ---------------------------------------------------------------------------
// 被动确定性抖动与数值辅助
// ---------------------------------------------------------------------------

func TestW1EJitterArms(t *testing.T) {
	cases := []struct {
		name       string
		intervalMs int64
		want       int64
	}{
		{"半分钟间隔取一半", 30_000, 15_000},
		{"40 秒间隔被半程截断", 40_000, 20_000},
		{"1 秒间隔半程为零", 1, 0},
		{"2 分钟间隔 30 秒窗", 120_000, 30_000},
		{"2 小时间隔 30 分钟窗", 7_200_000, 1_800_000},
		{"48 小时间隔 1 小时窗", 48 * 3_600_000, 3_600_000},
		{"30 天间隔 8 小时窗", 30 * 24 * 3_600_000, 8 * 60 * 60_000},
	}
	for _, testCase := range cases {
		if got := passiveScheduleJitterWindowMs(testCase.intervalMs); got != testCase.want {
			t.Fatalf("%s: passiveScheduleJitterWindowMs(%d) = %d, want %d", testCase.name, testCase.intervalMs, got, testCase.want)
		}
	}

	if max64(2, 3) != 3 || max64(4, 3) != 4 || max64(5, 5) != 5 {
		t.Fatal("max64 分支错误")
	}
	if min64(2, 3) != 2 || min64(4, 3) != 3 || min64(5, 5) != 5 {
		t.Fatal("min64 分支错误")
	}

	// imul32：32 位有符号乘法回绕。
	if got := imul32(65536, 65536); got != 0 {
		t.Fatalf("imul32(2^16, 2^16) = %d, want 0（回绕）", got)
	}
	if got := imul32(0x40000000, 4); got != 0 {
		t.Fatalf("imul32(0x40000000, 4) = %d, want 0（回绕）", got)
	}
	if got := imul32(-1, 1); got != -1 {
		t.Fatalf("imul32(-1, 1) = %d, want -1", got)
	}
	if got := imul32(3, 5); got != 15 {
		t.Fatalf("imul32(3, 5) = %d, want 15", got)
	}

	// 零窗口直接返回 0；正常窗口内确定性采样且非零。
	if got := passiveScheduleDeterministicOffsetMs(1, "seed"); got != 0 {
		t.Fatalf("零窗口 offset = %d, want 0", got)
	}
	offsetA := passiveScheduleDeterministicOffsetMs(7_200_000, "seed-a")
	if offsetA < -1_800_000 || offsetA > 1_800_000 || offsetA == 0 {
		t.Fatalf("offset = %d, want ±30 分钟窗口内且非零", offsetA)
	}
	if offsetA != passiveScheduleDeterministicOffsetMs(7_200_000, "seed-a") {
		t.Fatal("同种子 offset 必须确定")
	}
}

// ---------------------------------------------------------------------------
// 额度恢复策略读取归一
// ---------------------------------------------------------------------------

func TestW1EQuotaRecoveryPolicyArms(t *testing.T) {
	if _, err := quotaRecoveryPolicyRead("nope"); err == nil || !strings.Contains(err.Error(), "必须是对象") {
		t.Fatalf("非对象策略 = %v", err)
	}
	if _, err := quotaRecoveryPolicyRead(map[string]any{"weird": map[string]any{}}); err == nil || !strings.Contains(err.Error(), "weird 不受支持") {
		t.Fatalf("未知字段 = %v", err)
	}
	if _, err := quotaRecoveryPolicyRead(map[string]any{"api_key": 42}); err == nil || !strings.Contains(err.Error(), "策略项必须是对象") {
		t.Fatalf("非对象策略项 = %v", err)
	}
	if _, err := quotaRecoveryPolicyRead(map[string]any{"oauth": map[string]any{"reset_strategy": "monthly"}}); err == nil || !strings.Contains(err.Error(), "duration、daily 或 weekly") {
		t.Fatalf("无效策略 = %v", err)
	}

	// duration 策略：范围 30-10080 分钟。
	if _, err := quotaRecoveryPolicyRead(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(29)}}); err == nil || !strings.Contains(err.Error(), "duration_minutes") {
		t.Fatalf("duration 下界 = %v", err)
	}
	if _, err := quotaRecoveryPolicyRead(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(7*24*60 + 1)}}); err == nil || !strings.Contains(err.Error(), "duration_minutes") {
		t.Fatalf("duration 上界 = %v", err)
	}
	if _, err := quotaRecoveryPolicyRead(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": 60}}); err == nil {
		t.Fatal("duration_minutes 传 int 必须报错（仅接受 JSON float64）")
	}

	// daily / weekly 边界。
	if _, err := quotaRecoveryPolicyRead(map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(24)}}); err == nil || !strings.Contains(err.Error(), "daily_reset_hour") {
		t.Fatalf("daily 上界 = %v", err)
	}
	if _, err := quotaRecoveryPolicyRead(map[string]any{"oauth": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(-1), "weekly_reset_hour": float64(0)}}); err == nil || !strings.Contains(err.Error(), "weekly_reset_day") {
		t.Fatalf("weekly 下界 = %v", err)
	}
	if _, err := quotaRecoveryPolicyRead(map[string]any{"oauth": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(1), "weekly_reset_hour": float64(99)}}); err == nil || !strings.Contains(err.Error(), "weekly_reset_hour") {
		t.Fatalf("weekly 小时 = %v", err)
	}

	// jitter_minutes：缺失可，15 可，其他值或 int 类型不可。
	valid, err := quotaRecoveryPolicyRead(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(120), "jitter_minutes": float64(15), "timezone": " Asia/Shanghai "}})
	if err != nil {
		t.Fatalf("合法策略 = %v", err)
	}
	schedule := valid["api_key"].(map[string]any)
	if schedule["duration_minutes"] != float64(120) || schedule["timezone"] != "Asia/Shanghai" || schedule["jitter_minutes"] != float64(15) {
		t.Fatalf("duration 归一 = %+v", schedule)
	}
	badJitter := map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(120), "jitter_minutes": float64(30)}}
	if _, err := quotaRecoveryPolicyRead(badJitter); err == nil || !strings.Contains(err.Error(), "jitter_minutes") {
		t.Fatalf("jitter 非法值 = %v", err)
	}
	intJitter := map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(120), "jitter_minutes": 15}}
	if _, err := quotaRecoveryPolicyRead(intJitter); err == nil {
		t.Fatal("jitter 传 int 必须报错")
	}

	// timezone：非字符串、空白、非法值均报错。
	badTz := map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": 42}}
	if _, err := quotaRecoveryPolicyRead(badTz); err == nil || !strings.Contains(err.Error(), "timezone 无效") {
		t.Fatalf("timezone 非字符串 = %v", err)
	}
	blankTz := map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": "   "}}
	if _, err := quotaRecoveryPolicyRead(blankTz); err == nil {
		t.Fatal("timezone 空白必须报错")
	}
	unknownTz := map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": "Mars/Olympus"}}
	if _, err := quotaRecoveryPolicyRead(unknownTz); err == nil || !strings.Contains(err.Error(), "timezone 无效") {
		t.Fatalf("未知 timezone = %v", err)
	}

	// 缺省 jitter/timezone 兜底。
	defaulted, err := quotaRecoveryPolicyRead(map[string]any{"google_oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(6)}})
	if err != nil {
		t.Fatalf("缺省字段策略 = %v", err)
	}
	googleSchedule := defaulted["google_oauth"].(map[string]any)
	if googleSchedule["timezone"] != "UTC" || googleSchedule["jitter_minutes"] != float64(15) || googleSchedule["daily_reset_hour"] != float64(6) {
		t.Fatalf("缺省归一 = %+v", googleSchedule)
	}
}

// ---------------------------------------------------------------------------
// 额度恢复计划投影与冷却边界
// ---------------------------------------------------------------------------

func TestW1EQuotaRecoveryScheduleArms(t *testing.T) {
	// ForAccount 缺省：api_key 60 分钟 duration，oauth/google_oauth 每日 0 点。
	apiDefault := quotaRecoveryScheduleForAccount(nil, "api_key")
	if apiDefault["reset_strategy"] != "duration" || apiDefault["duration_minutes"] != float64(60) {
		t.Fatalf("api_key 缺省 = %+v", apiDefault)
	}
	oauthDefault := quotaRecoveryScheduleForAccount(nil, "oauth")
	if oauthDefault["reset_strategy"] != "daily" || oauthDefault["daily_reset_hour"] != float64(0) {
		t.Fatalf("oauth 缺省 = %+v", oauthDefault)
	}
	unknownType := quotaRecoveryScheduleForAccount(nil, "weird_type")
	if unknownType["reset_strategy"] != "daily" {
		t.Fatalf("未知类型回落 oauth = %+v", unknownType)
	}

	// 配置项覆盖缺省（含 reset_strategy 切换）。
	policy := map[string]any{"api_key": map[string]any{
		"reset_strategy": "daily", "daily_reset_hour": float64(5), "timezone": "Asia/Shanghai",
	}}
	overridden := quotaRecoveryScheduleForAccount(policy, "api_key")
	if overridden["reset_strategy"] != "daily" || overridden["daily_reset_hour"] != float64(5) || overridden["timezone"] != "Asia/Shanghai" {
		t.Fatalf("配置覆盖 = %+v", overridden)
	}
	partial := quotaRecoveryScheduleForAccount(map[string]any{"oauth": map[string]any{"daily_reset_hour": float64(3)}}, "oauth")
	if partial["daily_reset_hour"] != float64(3) || partial["timezone"] != "UTC" || partial["reset_strategy"] != "daily" {
		t.Fatalf("部分覆盖 = %+v", partial)
	}

	now := w1ePolicyClock()

	// Boundary：duration 直接加时长，缺 duration_minutes 兜底 60 分钟。
	durationBoundary, err := quotaRecoveryScheduleBoundary(map[string]any{"reset_strategy": "duration", "duration_minutes": float64(90)}, now)
	if err != nil || !durationBoundary.Equal(now.Add(90*time.Minute)) {
		t.Fatalf("duration 边界 = %v/%v", durationBoundary, err)
	}
	defaultBoundary, err := quotaRecoveryScheduleBoundary(map[string]any{"reset_strategy": "duration"}, now)
	if err != nil || !defaultBoundary.Equal(now.Add(time.Hour)) {
		t.Fatalf("duration 缺省边界 = %v/%v", defaultBoundary, err)
	}

	// daily：未来目标小时今天命中；等于当前时刻顺延一天。
	dayBoundary, err := quotaRecoveryScheduleBoundary(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(23)}, now)
	if err != nil || !dayBoundary.Equal(time.Date(2026, 9, 1, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("daily 未来边界 = %v/%v", dayBoundary, err)
	}
	rolloverBoundary, err := quotaRecoveryScheduleBoundary(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(10)}, now)
	if err != nil || !rolloverBoundary.Equal(time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("daily 顺延边界 = %v/%v", rolloverBoundary, err)
	}

	// weekly：目标日在未来 / 目标时刻已过顺延一周。
	weeklyFuture, err := quotaRecoveryScheduleBoundary(map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(5), "weekly_reset_hour": float64(8)}, now)
	if err != nil || !weeklyFuture.Equal(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly 未来边界 = %v/%v", weeklyFuture, err)
	}
	weeklyPast, err := quotaRecoveryScheduleBoundary(map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(2), "weekly_reset_hour": float64(8)}, now)
	if err != nil || !weeklyPast.Equal(time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly 顺延边界 = %v/%v", weeklyPast, err)
	}

	// 时区：now=10:00Z 对应上海 18:00，目标 20:00 尚未过去 → 12:00Z。
	tzBoundary, err := quotaRecoveryScheduleBoundary(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(20), "timezone": "Asia/Shanghai"}, now)
	if err != nil || !tzBoundary.Equal(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("时区边界 = %v/%v", tzBoundary, err)
	}
	if _, err := quotaRecoveryScheduleBoundary(map[string]any{"reset_strategy": "daily", "timezone": "Mars/Olympus"}, now); err == nil || !strings.Contains(err.Error(), "timezone 无效") {
		t.Fatalf("非法时区 = %v", err)
	}

	// CooldownUntil：默认 oauth 每日 0 点 ± 30 分钟；duration 90 ± 30 分钟。
	untilDefault, err := quotaRecoveryCooldownUntil(nil, "oauth", "seed-a", now)
	if err != nil {
		t.Fatalf("默认冷却 = %v", err)
	}
	w1eAssertWithin(t, untilDefault, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), 30*time.Minute)
	untilDuration, err := quotaRecoveryCooldownUntil(
		map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(90)}},
		"api_key", "seed-b", now)
	if err != nil {
		t.Fatalf("duration 冷却 = %v", err)
	}
	w1eAssertWithin(t, untilDuration, now.Add(90*time.Minute), 30*time.Minute)
	if _, err := quotaRecoveryCooldownUntil(
		map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": "Mars/Olympus"}},
		"oauth", "seed-c", now); err == nil || !strings.Contains(err.Error(), "timezone 无效") {
		t.Fatalf("非法时区冷却 = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 恢复 hint 解析器
// ---------------------------------------------------------------------------

func TestW1EQuotaHintParsers(t *testing.T) {
	if parseJSONValueLoose("") != nil || parseJSONValueLoose("   ") != nil || parseJSONValueLoose("{bad") != nil {
		t.Fatal("空串/空白/坏 JSON 必须返回 nil")
	}
	if number, ok := parseJSONValueLoose("42").(float64); !ok || number != 42 {
		t.Fatalf("标量 JSON = %#v", parseJSONValueLoose("42"))
	}
	if list, ok := parseJSONValueLoose("[1,2]").([]any); !ok || len(list) != 2 {
		t.Fatalf("数组 JSON = %#v", parseJSONValueLoose("[1,2]"))
	}
	if _, ok := parseJSONValueLoose(`{"a":1}`).(map[string]any); !ok {
		t.Fatalf("对象 JSON = %#v", parseJSONValueLoose(`{"a":1}`))
	}

	// findFirstJSONField：名字优先于 map 顺序；递归对象与数组；缺失返回 nil。
	named := map[string]any{"resetAt": float64(1), "reset_at": float64(2)}
	if got := findFirstJSONField(named, []string{"reset_at", "resetAt"}); got != float64(2) {
		t.Fatalf("名字优先 = %#v, want 2", got)
	}
	nested := map[string]any{"data": map[string]any{"quota_reset_at": "inner"}}
	if got := findFirstJSONField(nested, []string{"quota_reset_at"}); got != "inner" {
		t.Fatalf("嵌套递归 = %#v", got)
	}
	arrayed := []any{map[string]any{"other": float64(1)}, map[string]any{"errors": []any{map[string]any{"reset_at": float64(9)}}}}
	if got := findFirstJSONField(arrayed, []string{"reset_at"}); got != float64(9) {
		t.Fatalf("数组递归 = %#v, want 9", got)
	}
	if got := findFirstJSONField(map[string]any{"a": "b"}, []string{"reset_at"}); got != nil {
		t.Fatalf("缺失字段 = %#v, want nil", got)
	}
	if got := findFirstJSONField(float64(3), []string{"reset_at"}); got != nil {
		t.Fatalf("标量输入 = %#v, want nil", got)
	}

	now := w1ePolicyClock()

	// parseAbsoluteRecoveryTime：秒/毫秒数值、字符串数值、RFC3339 与非法输入。
	seconds := parseAbsoluteRecoveryTime(float64(1790000000))
	if seconds == nil || seconds.UTC().Format(rfc3339MillisUTC) != "2026-09-21T14:13:20.000Z" {
		t.Fatalf("秒数值 = %v", seconds)
	}
	millis := parseAbsoluteRecoveryTime(float64(1790000123456))
	if millis == nil || !millis.Equal(time.Unix(1790000123, 456*int64(time.Millisecond))) {
		t.Fatalf("毫秒数值 = %v", millis)
	}
	if boundary := parseAbsoluteRecoveryTime(float64(10_000_000_000)); boundary == nil {
		t.Fatal("1e10 边界按秒放大必须非 nil")
	}
	if parseAbsoluteRecoveryTime(float64(0)) != nil || parseAbsoluteRecoveryTime(float64(-5)) != nil {
		t.Fatal("非正数值必须返回 nil")
	}
	if textSeconds := parseAbsoluteRecoveryTime(" 1790000000 "); textSeconds == nil || !textSeconds.Equal(*seconds) {
		t.Fatalf("字符串秒数值 = %v", textSeconds)
	}
	if parseAbsoluteRecoveryTime("") != nil || parseAbsoluteRecoveryTime("   ") != nil || parseAbsoluteRecoveryTime("abc") != nil || parseAbsoluteRecoveryTime("2026-13-99T00:00:00Z") != nil {
		t.Fatal("空白/非数值/非法 RFC3339 必须返回 nil")
	}
	offsetParsed := parseAbsoluteRecoveryTime("2026-09-21T00:00:00+08:00")
	if offsetParsed == nil || offsetParsed.UTC().Format(rfc3339MillisUTC) != "2026-09-20T16:00:00.000Z" {
		t.Fatalf("RFC3339 带时区 = %v", offsetParsed)
	}
	if parseAbsoluteRecoveryTime(true) != nil || parseAbsoluteRecoveryTime(nil) != nil {
		t.Fatal("非数值/字符串类型必须返回 nil")
	}

	// parsePositiveSeconds：向上取整、字符串与非正数。
	if got := parsePositiveSeconds(float64(2.5)); got == nil || *got != 3 {
		t.Fatalf("2.5 秒 = %v, want 3", got)
	}
	if got := parsePositiveSeconds(float64(90.7)); got == nil || *got != 91 {
		t.Fatalf("90.7 秒 = %v, want 91", got)
	}
	if parsePositiveSeconds(float64(0)) != nil || parsePositiveSeconds(float64(-1)) != nil {
		t.Fatal("非正秒数必须返回 nil")
	}
	if got := parsePositiveSeconds(" 3.2 "); got == nil || *got != 4 {
		t.Fatalf("字符串 3.2 = %v, want 4", got)
	}
	if parsePositiveSeconds("") != nil || parsePositiveSeconds("x") != nil || parsePositiveSeconds(true) != nil {
		t.Fatal("空白/非数值/布尔必须返回 nil")
	}

	// parseRetryAfterHeader：秒数、RFC3339、RFC1123 与非法输入。
	if parseRetryAfterHeader("", now) != nil || parseRetryAfterHeader("   ", now) != nil {
		t.Fatal("空 retry-after 必须返回 nil")
	}
	if got := parseRetryAfterHeader("120", now); got == nil || !got.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("retry-after 120 = %v", got)
	}
	if got := parseRetryAfterHeader("2026-09-21T00:00:00Z", now); got == nil || !got.Equal(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("retry-after RFC3339 = %v", got)
	}
	httpDate := parseRetryAfterHeader("Tue, 01 Sep 2026 10:05:00 GMT", now)
	if httpDate == nil || !httpDate.Equal(time.Date(2026, 9, 1, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("retry-after RFC1123 = %v", httpDate)
	}
	if parseRetryAfterHeader("Mon, 01 Sep 2025 10:00:00 GMT", now) != nil {
		t.Fatal("过去 HTTP 日期必须返回 nil")
	}
	if parseRetryAfterHeader("garbage", now) != nil {
		t.Fatal("无法解析的 retry-after 必须返回 nil")
	}

	// parseProviderResetHeader：绝对时间或 nil。
	provider := parseProviderResetHeader("1790000000", now)
	if provider == nil || !provider.Equal(*seconds) {
		t.Fatalf("provider reset = %v", provider)
	}
	if parseProviderResetHeader("2020-01-01T00:00:00Z", now) != nil || parseProviderResetHeader("nope", now) != nil {
		t.Fatal("过去/非法 provider reset 必须返回 nil")
	}

	// 响应头大小写不敏感与多名字优先。
	headers := map[string]string{"retry-after": "120", "X-Multi": "1"}
	if got := getCaseInsensitiveHeader(headers, "RETRY-AFTER"); got != "120" {
		t.Fatalf("大小写不敏感读取 = %q", got)
	}
	if got := getCaseInsensitiveHeader(headers, "missing"); got != "" {
		t.Fatalf("缺失头 = %q, want 空串", got)
	}
	if got := firstCaseInsensitiveHeader(headers, "x-quota-reset-at", "retry-after"); got != "120" {
		t.Fatalf("多名字优先 = %q", got)
	}
	if got := firstCaseInsensitiveHeader(headers, "x-quota-reset-at", "x-ratelimit-reset"); got != "" {
		t.Fatalf("全缺失 = %q, want 空串", got)
	}
}

// ---------------------------------------------------------------------------
// extractAPIKeyQuotaRecoveryHint 优先级矩阵
// ---------------------------------------------------------------------------

func TestW1EQuotaHintExtractionArms(t *testing.T) {
	now := w1ePolicyClock()

	hint := extractAPIKeyQuotaRecoveryHint(`{"error":{"code":"insufficient_quota","reset_at":1790000000}}`, nil, now)
	if hint == nil || hint.Mode != quotaRecoveryModeExplicitReset || hint.Source != "reset_at" || hint.CooldownUntil != "2026-09-21T14:13:20.000Z" {
		t.Fatalf("reset_at 秒 = %+v", hint)
	}

	msExpected := time.Unix(1790000123, 456*int64(time.Millisecond)).UTC().Format(rfc3339MillisUTC)
	hint = extractAPIKeyQuotaRecoveryHint(`{"resetAt":"1790000123456"}`, nil, now)
	if hint == nil || hint.Source != "reset_at" || hint.CooldownUntil != msExpected {
		t.Fatalf("resetAt 毫秒字符串 = %+v", hint)
	}

	hint = extractAPIKeyQuotaRecoveryHint(`{"quota_reset_at":"2026-09-21T00:00:00+08:00"}`, nil, now)
	if hint == nil || hint.CooldownUntil != "2026-09-20T16:00:00.000Z" {
		t.Fatalf("RFC3339 reset_at = %+v", hint)
	}

	// 过去 reset_at 落到秒数字段；ceil 到整秒。
	hint = extractAPIKeyQuotaRecoveryHint(`{"reset_at":"2020-01-01T00:00:00Z","reset_after_seconds":90.7}`, nil, now)
	if hint == nil || hint.Source != "reset_at" || hint.CooldownUntil != "2026-09-01T10:01:31.000Z" {
		t.Fatalf("reset_after_seconds = %+v", hint)
	}

	hint = extractAPIKeyQuotaRecoveryHint(`{"reset_at":1,"retry_after_seconds":"30.2"}`, nil, now)
	if hint == nil || hint.CooldownUntil != "2026-09-01T10:00:31.000Z" {
		t.Fatalf("retry_after_seconds = %+v", hint)
	}

	// retry-after 头（秒）。
	hint = extractAPIKeyQuotaRecoveryHint(`{"error":{"message":"quota"}}`, map[string]string{"retry-after": "120"}, now)
	if hint == nil || hint.Source != "retry_after" || hint.CooldownUntil != "2026-09-01T10:02:00.000Z" {
		t.Fatalf("retry-after 秒 = %+v", hint)
	}

	// retry-after 头（HTTP 日期）。
	hint = extractAPIKeyQuotaRecoveryHint(`{}`, map[string]string{"Retry-After": "Tue, 01 Sep 2026 10:05:00 GMT"}, now)
	if hint == nil || hint.Source != "retry_after" || hint.CooldownUntil != "2026-09-01T10:05:00.000Z" {
		t.Fatalf("retry-after 日期 = %+v", hint)
	}

	// 供应商 reset 头：秒值与毫秒值。
	hint = extractAPIKeyQuotaRecoveryHint(`{}`, map[string]string{"x-quota-reset-at": "1790000000"}, now)
	if hint == nil || hint.Source != "provider_header" || hint.CooldownUntil != "2026-09-21T14:13:20.000Z" {
		t.Fatalf("x-quota-reset-at = %+v", hint)
	}
	hint = extractAPIKeyQuotaRecoveryHint(`{}`, map[string]string{"X-RateLimit-Reset": "1790000123456"}, now)
	if hint == nil || hint.Source != "provider_header" || hint.CooldownUntil != msExpected {
		t.Fatalf("x-ratelimit-reset 大小写 = %+v", hint)
	}

	// 优先级：retry-after 头压过供应商头。
	hint = extractAPIKeyQuotaRecoveryHint(`{}`, map[string]string{
		"retry-after": "120", "x-quota-reset-at": "1790000000",
	}, now)
	if hint == nil || hint.Source != "retry_after" {
		t.Fatalf("retry-after 优先 = %+v", hint)
	}

	// 数组体内的嵌套 reset_at 同样命中（D-98 数组递归）。
	hint = extractAPIKeyQuotaRecoveryHint(`[{"error":{"reset_at":1790000000}}]`, nil, now)
	if hint == nil || hint.Source != "reset_at" {
		t.Fatalf("数组递归 = %+v", hint)
	}

	// 无任何可解析来源 → nil。
	if extractAPIKeyQuotaRecoveryHint("上游拒绝", nil, now) != nil {
		t.Fatal("非 JSON 体必须返回 nil")
	}
	if extractAPIKeyQuotaRecoveryHint(`{"reset_at":1}`, nil, now) != nil {
		t.Fatal("过去 reset_at 且无其他来源必须返回 nil")
	}
}

// ---------------------------------------------------------------------------
// 系统额度规则剩余分支与错误标识归一
// ---------------------------------------------------------------------------

func TestW1ESystemQuotaRuleTextArms(t *testing.T) {
	if systemInsufficientQuotaRuleMatches(500, "insufficient_quota", "", "") {
		t.Fatal("非 402/403 必须拒绝")
	}
	if !systemInsufficientQuotaRuleMatches(402, "my_quota_gone", "", "") {
		t.Fatal("code 含 quota 子串必须命中")
	}
	if !systemInsufficientQuotaRuleMatches(403, "", "insufficient_balance", "") {
		t.Fatal("type 稳定码必须命中")
	}
	if !systemInsufficientQuotaRuleMatches(403, "Insufficient Quota", "", "") {
		t.Fatal("空格连字符归一后必须命中稳定码")
	}
	if systemInsufficientQuotaRuleMatches(402, "custom_error", "", "request was access denied by policy") {
		t.Fatal("文本中的非额度标识必须排除")
	}
	if systemInsufficientQuotaRuleMatches(403, "custom_error", "", "request forbidden by policy") {
		t.Fatal("forbidden 文本必须排除")
	}
	if !systemInsufficientQuotaRuleMatches(402, "custom_error", "", "your credit balance too low") {
		t.Fatal("credit balance too low 文本必须命中")
	}
	if !systemInsufficientQuotaRuleMatches(403, "custom_error", "", "wallet balance exhausted") {
		t.Fatal("wallet balance exhausted 文本必须命中")
	}
	if !systemInsufficientQuotaRuleMatches(402, "custom_error", "", "subscription quota insufficient for this model") {
		t.Fatal("subscription quota insufficient 文本必须命中")
	}
	if !systemInsufficientQuotaRuleMatches(403, "", "", "please top up, insufficient quota") {
		t.Fatal("403 无标识 + 额度文本必须命中")
	}

	if got := normalizeErrorIdentifier("  Insufficient-Quota  "); got != "insufficient_quota" {
		t.Fatalf("normalizeErrorIdentifier 空白连字符 = %q", got)
	}
	if got := normalizeErrorIdentifier("A\tB\nC"); got != "a_b_c" {
		t.Fatalf("normalizeErrorIdentifier 控制空白 = %q", got)
	}
	if got := normalizeErrorIdentifier("no--seps"); got != "no_seps" {
		t.Fatalf("normalizeErrorIdentifier 重复分隔 = %q", got)
	}
	if got := normalizeErrorIdentifier(""); got != "" {
		t.Fatalf("normalizeErrorIdentifier 空串 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 载荷摘要与诊断脱敏
// ---------------------------------------------------------------------------

func TestW1EPayloadSummaryArms(t *testing.T) {
	summary := accountErrorPayloadSummary(gatewayproto.ErrorPayload{Code: "insufficient_quota", Message: "Quota exceeded"})
	if summary != "insufficient_quota；Quota exceeded" {
		t.Fatalf("双字段摘要 = %q", summary)
	}
	if summary := accountErrorPayloadSummary(gatewayproto.ErrorPayload{Code: "same", Message: "same"}); summary != "same" {
		t.Fatalf("去重摘要 = %q", summary)
	}
	if summary := accountErrorPayloadSummary(gatewayproto.ErrorPayload{}); summary != "" {
		t.Fatalf("空载荷摘要 = %q", summary)
	}
	if summary := accountErrorPayloadSummary(gatewayproto.ErrorPayload{Message: "只有消息"}); summary != "只有消息" {
		t.Fatalf("仅消息摘要 = %q", summary)
	}
	sensitive := accountErrorPayloadSummary(gatewayproto.ErrorPayload{Code: "auth", Message: "bad key sk-abcdefgh1234 leaked"})
	if strings.Contains(sensitive, "sk-abcdefgh1234") || !strings.Contains(sensitive, "[redacted]") {
		t.Fatalf("敏感串必须脱敏 = %q", sensitive)
	}

	if got := sanitizeDiagnosticText(""); got != "" {
		t.Fatalf("空串脱敏 = %q", got)
	}
	if got := sanitizeDiagnosticText("   "); got != "" {
		t.Fatalf("空白脱敏 = %q", got)
	}
	if got := sanitizeDiagnosticText("token sk-abcdefgh1234 here"); strings.Contains(got, "sk-abcdefgh1234") {
		t.Fatalf("脱敏结果 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 决策服务构造与小辅助
// ---------------------------------------------------------------------------

func TestW1EPolicyServiceHelpers(t *testing.T) {
	// 零值 deps：Now 回落 time.Now，pool 回落恒 false。
	service := newChainErrorPolicyService(chainErrorPolicyDeps{})
	if delta := time.Since(service.now()); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("缺省 Now 偏差过大：%v", delta)
	}
	if service.pool(gatewaydispatch.AccountCandidate{ID: "acc"}) {
		t.Fatal("缺省 pool 必须恒 false")
	}

	if seedKeyPart(nil) != "account" {
		t.Fatal("nil 指纹必须回落 account")
	}
	empty := ""
	if seedKeyPart(&empty) != "account" {
		t.Fatal("空指纹必须回落 account")
	}
	blank := "   "
	if seedKeyPart(&blank) != "account" {
		t.Fatal("空白指纹必须回落 account")
	}
	fingerprint := " fp-1 "
	if seedKeyPart(&fingerprint) != "fp-1" {
		t.Fatalf("指纹修剪 = %q", seedKeyPart(&fingerprint))
	}

	if got := formatPriority(2.9); got != "2" {
		t.Fatalf("formatPriority(2.9) = %q", got)
	}
	if got := formatPriority(10); got != "10" {
		t.Fatalf("formatPriority(10) = %q", got)
	}

	if responseHeaderMapOf(nil) != nil {
		t.Fatal("nil 头必须返回 nil")
	}
	headerMap := responseHeaderMapOf(http.Header{
		"Retry-After": []string{"120"},
		"X-Multi":     {"a", "b"},
		"X-Empty":     {},
	})
	if headerMap["retry-after"] != "120" || headerMap["x-multi"] != "a, b" {
		t.Fatalf("头投影 = %+v", headerMap)
	}
	if _, exists := headerMap["x-empty"]; exists {
		t.Fatal("空值切片必须跳过")
	}

	// errorPayloadOf：非 JSON 体（parsedBody nil）返回空载荷，不回读文本。
	emptyService := newChainErrorPolicyService(chainErrorPolicyDeps{Now: w1ePolicyClock})
	if payload := emptyService.errorPayloadOf(`{"error":{"code":"boom"}}`, nil, nil); payload.HasEvidence() {
		t.Fatalf("非 JSON 体载荷 = %+v", payload)
	}
	payload := emptyService.errorPayloadOf(`ignored`, nil, w1eParsedBody(t, `{"error":{"code":"boom","type":"server_error"}}`))
	if payload.Code != "boom" || payload.Type != "server_error" {
		t.Fatalf("JSON 载荷投影 = %+v", payload)
	}

	// quotaRecoveryErrorCode 模式映射。
	if got := quotaRecoveryErrorCode(string(quotaRecoveryModeExplicitReset)); got != "api_key_quota_insufficient_reset" {
		t.Fatalf("explicit_reset 码 = %q", got)
	}
	if got := quotaRecoveryErrorCode(string(quotaRecoveryModeGeneric)); got != "api_key_quota_insufficient" {
		t.Fatalf("generic 码 = %q", got)
	}
	if got := quotaRecoveryErrorCode(""); got != "api_key_quota_insufficient" {
		t.Fatalf("空模式码 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Decide：2xx 边界、错误上抛与非默认账户类型路径
// ---------------------------------------------------------------------------

func TestW1EDecideArms(t *testing.T) {
	service := newFixedErrorPolicyService(nil)
	settings := gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	quotaBody := `{"error":{"code":"insufficient_quota","message":"insufficient quota"}}`

	// 2xx 全闭区间无决策（含上边界 299）。
	parsed2xx := w1eParsedBody(t, quotaBody)
	for _, status := range []int{200, 204, 299} {
		decision, err := service.Decide(errorPolicyAccount(nil), status, nil, quotaBody, parsed2xx, settings)
		if err != nil || decision != nil {
			t.Fatalf("status %d = %+v/%v, want nil", status, decision, err)
		}
	}

	// 非 2xx 命中账户规则的状态码维度（301 迁移规则）。
	rule301 := errorPolicyAccount(map[string]any{"error_handling_rules": []any{map[string]any{
		"enabled": true, "name": "迁移", "priority": float64(1), "action": "retry_next",
		"status_codes": []any{float64(301)},
	}}})
	decision, err := service.Decide(rule301, 301, nil, "{}", map[string]any{}, settings)
	if err != nil || decision == nil || decision.Action != decisionActionRetryNext || decision.RuleName != "迁移" {
		t.Fatalf("301 账户规则 = %+v/%v", decision, err)
	}

	// 覆盖列表格式错误 → 错误上抛。
	badOverrides := errorPolicyAccount(map[string]any{"error_handling_rule_overrides": "nope"})
	if _, err := service.Decide(badOverrides, 500, nil, `{"error":{"message":"boom"}}`, w1eParsedBody(t, `{"error":{"message":"boom"}}`), settings); err == nil || !strings.Contains(err.Error(), "覆盖格式无效") {
		t.Fatalf("覆盖格式错误 = %v", err)
	}
	idOverride := errorPolicyAccount(map[string]any{"error_handling_rule_overrides": []any{
		map[string]any{"system_rule_id": "other", "action": "delete"},
	}})
	if _, err := service.Decide(idOverride, 500, nil, `{"error":{"message":"boom"}}`, w1eParsedBody(t, `{"error":{"message":"boom"}}`), settings); err == nil || !strings.Contains(err.Error(), "系统规则 ID 无效") {
		t.Fatalf("覆盖规则 ID = %v", err)
	}

	// 规则列表格式错误 → 错误上抛（500 不触发系统规则，读取必然发生）。
	badRules := errorPolicyAccount(map[string]any{"error_handling_rules": 42})
	if _, err := service.Decide(badRules, 500, nil, `{"error":{"message":"boom"}}`, w1eParsedBody(t, `{"error":{"message":"boom"}}`), settings); err == nil || !strings.Contains(err.Error(), "规则格式无效") {
		t.Fatalf("规则格式错误 = %v", err)
	}

	// 系统额度分支中 quota_recovery_policy 格式错误 → 错误上抛。
	badPolicy := errorPolicyAccount(map[string]any{"quota_recovery_policy": 42})
	if _, err := service.Decide(badPolicy, http.StatusPaymentRequired, nil, quotaBody, w1eParsedBody(t, quotaBody), settings); err == nil || !strings.Contains(err.Error(), "额度恢复策略必须是对象") {
		t.Fatalf("恢复策略格式错误 = %v", err)
	}

	// api_key + duration 90 分钟策略：冷却落在 11:30 ± 30 分钟。
	policy90 := errorPolicyAccount(map[string]any{"quota_recovery_policy": map[string]any{
		"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(90)},
	}})
	decision, err = service.Decide(policy90, http.StatusPaymentRequired, nil, quotaBody, w1eParsedBody(t, quotaBody), settings)
	if err != nil || decision == nil || decision.QuotaRecoveryMode != "generic" {
		t.Fatalf("duration 策略决策 = %+v/%v", decision, err)
	}
	w1eAssertWithin(t, decision.CooldownUntil, w1ePolicyClock().Add(90*time.Minute), 30*time.Minute)

	// oauth 账户：不提取 hint（模式为空），默认每日 0 点边界。
	oauth := errorPolicyAccount(nil)
	oauth.Type = "oauth"
	hintBody := `{"error":{"code":"insufficient_quota","reset_at":1790000000}}`
	decision, err = service.Decide(oauth, http.StatusPaymentRequired, nil, hintBody, w1eParsedBody(t, hintBody), settings)
	if err != nil || decision == nil {
		t.Fatalf("oauth 系统决策 = %+v/%v", decision, err)
	}
	if decision.QuotaRecoveryMode != "" || decision.QuotaRecoveryHintSource != "" {
		t.Fatalf("oauth 不得提取 hint：%+v", decision)
	}
	if !w1eParseUntil(t, decision.CooldownUntil).After(w1ePolicyClock()) {
		t.Fatalf("oauth 冷却必须在未来：%s", decision.CooldownUntil)
	}

	// google_oauth + google_oauth daily 5 点策略：边界 9 月 2 日 05:00 ± 30 分钟。
	google := errorPolicyAccount(map[string]any{"quota_recovery_policy": map[string]any{
		"google_oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(5)},
	}})
	google.Type = "google_oauth"
	decision, err = service.Decide(google, http.StatusPaymentRequired, nil, quotaBody, w1eParsedBody(t, quotaBody), settings)
	if err != nil || decision == nil {
		t.Fatalf("google_oauth 系统决策 = %+v/%v", decision, err)
	}
	w1eAssertWithin(t, decision.CooldownUntil, time.Date(2026, 9, 2, 5, 0, 0, 0, time.UTC), 30*time.Minute)

	// 403 + 供应商 reset 头：hint 来源 provider_header。
	resetHeader := http.Header{"X-Quota-Reset-At": []string{"1790000000"}}
	decision, err = service.Decide(errorPolicyAccount(nil), http.StatusForbidden, resetHeader, quotaBody, w1eParsedBody(t, quotaBody), settings)
	if err != nil || decision == nil || decision.QuotaRecoveryMode != "explicit_reset" || decision.QuotaRecoveryHintSource != "provider_header" || decision.CooldownUntil != "2026-09-21T14:13:20.000Z" {
		t.Fatalf("provider_header hint = %+v/%v", decision, err)
	}

	// pool 恒 true 注入：系统决策携带 keyScoped 事实。
	poolService := newFixedErrorPolicyService(func(gatewaydispatch.AccountCandidate) bool { return true })
	decision, err = poolService.Decide(errorPolicyAccount(nil), http.StatusPaymentRequired, nil, quotaBody, w1eParsedBody(t, quotaBody), settings)
	if err != nil || decision == nil || !decision.KeyScoped {
		t.Fatalf("pool 注入决策 = %+v/%v", decision, err)
	}
}

package main

// w1: 账户错误处理策略规则读取校验（Node account-error-policy-validation.ts
// 移植）——字段白名单、匹配条件完整性、限流恢复策略联动。

import (
	"strings"
	"testing"
)

func w1ValidRuleRecord() map[string]any {
	return map[string]any{
		"enabled": true, "name": "规则一", "priority": float64(10),
		"action": "retry_next", "status_codes": []any{float64(500)},
	}
}

func TestW1AccountErrorHandlingRuleReadValid(t *testing.T) {
	rule, err := accountErrorHandlingRuleRead(w1ValidRuleRecord(), 0)
	if err != nil {
		t.Fatalf("valid rule: %v", err)
	}
	if rule.Enabled != true || rule.Name != "规则一" || rule.Priority != 10 || rule.Action != "retry_next" {
		t.Fatalf("rule = %+v", rule)
	}
	if len(rule.StatusCodes) != 1 || rule.StatusCodes[0] != 500 {
		t.Fatalf("status codes = %v", rule.StatusCodes)
	}
	// 禁用规则可无匹配条件。
	disabled := w1ValidRuleRecord()
	disabled["enabled"] = false
	delete(disabled, "status_codes")
	if _, err := accountErrorHandlingRuleRead(disabled, 0); err != nil {
		t.Fatalf("禁用无条件: %v", err)
	}
}

func TestW1AccountErrorHandlingRuleReadErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		expect string
	}{
		{"系统继承", func(r map[string]any) { r["source"] = "system" }, "系统继承"},
		{"不支持字段", func(r map[string]any) { r["bogus"] = 1 }, "不支持字段"},
		{"缺名称", func(r map[string]any) { delete(r, "name") }, "名称"},
		{"零优先级", func(r map[string]any) { r["priority"] = float64(0) }, "优先级"},
		{"无效动作", func(r map[string]any) { r["action"] = "bogus" }, "动作无效"},
		{"2xx 错误码", func(r map[string]any) { r["status_codes"] = nil; r["error_codes"] = []any{"200"} }, "成功码"},
		{"无匹配条件", func(r map[string]any) { delete(r, "status_codes") }, "至少需要一个匹配条件"},
		{"非法状态码", func(r map[string]any) { r["status_codes"] = []any{"abc"} }, "状态码"},
		{"非法错误类型", func(r map[string]any) { r["error_types"] = []any{42} }, "错误类型"},
	}
	// 非对象记录：格式无效。
	if _, err := accountErrorHandlingRuleRead("not-a-map", 0); err == nil || !strings.Contains(err.Error(), "格式无效") {
		t.Fatalf("非对象 = %v", err)
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			record := w1ValidRuleRecord()
			testCase.mutate(record)
			_, err := accountErrorHandlingRuleRead(record, 0)
			if err == nil || !strings.Contains(err.Error(), testCase.expect) {
				t.Fatalf("错误 = %v，want 含 %q", err, testCase.expect)
			}
		})
	}
}

func TestW1RateLimitedRuleResetStrategy(t *testing.T) {
	// duration 策略。
	duration := w1ValidRuleRecord()
	duration["action"] = "rate_limited"
	duration["reset_strategy"] = "duration"
	duration["duration_hours"] = float64(2)
	rule, err := accountErrorHandlingRuleRead(duration, 0)
	if err != nil || rule.ResetStrategy != "duration" || rule.DurationHours != 2 {
		t.Fatalf("duration = %+v, %v", rule, err)
	}
	// daily 策略。
	daily := w1ValidRuleRecord()
	daily["action"] = "rate_limited"
	daily["reset_strategy"] = "daily"
	daily["daily_reset_hour"] = float64(6)
	rule, err = accountErrorHandlingRuleRead(daily, 0)
	if err != nil || rule.ResetStrategy != "daily" {
		t.Fatalf("daily = %+v, %v", rule, err)
	}
	// weekly 策略。
	weekly := w1ValidRuleRecord()
	weekly["action"] = "rate_limited"
	weekly["reset_strategy"] = "weekly"
	weekly["weekly_reset_day"] = float64(3)
	weekly["weekly_reset_hour"] = float64(8)
	rule, err = accountErrorHandlingRuleRead(weekly, 0)
	if err != nil || rule.ResetStrategy != "weekly" || rule.WeeklyResetDay != 3 {
		t.Fatalf("weekly = %+v, %v", rule, err)
	}
	// 缺策略。
	missing := w1ValidRuleRecord()
	missing["action"] = "rate_limited"
	if _, err := accountErrorHandlingRuleRead(missing, 0); err == nil || !strings.Contains(err.Error(), "恢复策略无效") {
		t.Fatalf("缺策略 = %v", err)
	}
	// duration 缺小时。
	badDuration := w1ValidRuleRecord()
	badDuration["action"] = "rate_limited"
	badDuration["reset_strategy"] = "duration"
	if _, err := accountErrorHandlingRuleRead(badDuration, 0); err == nil {
		t.Fatal("duration 缺小时必须报错")
	}
}

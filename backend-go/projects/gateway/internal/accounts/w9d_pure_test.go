package accounts

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// w9d：纯校验/投影函数高密度补测（balance_config、error_policy、quota_recovery、
// upstream_base_url、list_usage 投影、crypto）。
// ---------------------------------------------------------------------------

func TestW9DBalanceConfigArms(t *testing.T) {
	// 非对象 / 未知键 / adapter 缺失与非法。
	if _, err := NormalizeAccountBalanceConfig("str"); err == nil {
		t.Fatal("non-object config must fail")
	}
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"unknown": 1}); err == nil {
		t.Fatal("unknown key must fail")
	}
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": 1}); err == nil {
		t.Fatal("non-string adapter must fail")
	}
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "oauth"}); err == nil {
		t.Fatal("invalid adapter must fail")
	}
	// intervalMinutes 边界。
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "builtin", "intervalMinutes": 11.0}); err == nil {
		t.Fatal("interval > 10 must fail")
	}
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "builtin", "intervalMinutes": 0.5}); err == nil {
		t.Fatal("fractional interval must fail")
	}
	canonical, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "builtin", "intervalMinutes": 9.0, "preferredBuiltinAdapter": "sub2api"})
	if err != nil || canonical["intervalMinutes"] != 9.0 || canonical["preferredBuiltinAdapter"] != "sub2api" {
		t.Fatalf("canonical=%v err=%v", canonical, err)
	}
	// preferredBuiltinAdapter 非法。
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "builtin", "preferredBuiltinAdapter": "nope"}); err == nil {
		t.Fatal("invalid builtin adapter must fail")
	}
	// custom 互斥规则。
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "custom"}); err == nil {
		t.Fatal("custom without config must fail")
	}
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "builtin", "custom": map[string]any{"path": "/x"}}); err == nil {
		t.Fatal("builtin with custom must fail")
	}
	customOK := map[string]any{"path": "/balance", "remainingPointer": "/data/remaining", "divisor": "2.5"}
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "custom", "preferredBuiltinAdapter": "sub2api", "custom": customOK}); err == nil {
		t.Fatal("custom with builtin preference must fail")
	}
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "custom", "custom": customOK}); err != nil {
		t.Fatalf("valid custom err=%v", err)
	}
	// custom 校验矩阵。
	badCustoms := []map[string]any{
		{"path": 1},
		{"path": "relative"},
		{"path": "//host"},
		{"path": "/x", "remainingPointer": 1},
		{"path": "/x", "remainingPointer": "bad pointer"},
		{"path": "/x", "totalPointer": "/t"},
		{"path": "/x", "remainingPointer": "/r", "divisor": 2},
		{"path": "/x", "remainingPointer": "/r", "divisor": "0"},
		{"path": "/x", "remainingPointer": "/r", "divisor": "0.0"},
		{"path": "/x", "remainingPointer": "/r", "divisor": "-1"},
		{"path": "/x", "unknownField": 1},
	}
	for i, custom := range badCustoms {
		if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "custom", "custom": custom}); err == nil {
			t.Fatalf("bad custom[%d] must fail: %v", i, custom)
		}
	}
	// total+used 成对路径。
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "/x", "totalPointer": "/t", "usedPointer": "/u"}}); err != nil {
		t.Fatalf("total+used custom err=%v", err)
	}
	// 能力校验矩阵。
	creds := Credentials{"api_keys": []any{" sk-1 ", "sk-1", "sk-2", 3, ""}}
	if got := EffectiveAccountApiKeys(creds); len(got) != 2 || got[0] != "sk-1" || got[1] != "sk-2" {
		t.Fatalf("effective keys=%v", got)
	}
	if got := EffectiveAccountApiKeys(Credentials{"api_key": " legacy "}); len(got) != 1 || got[0] != "legacy" {
		t.Fatalf("legacy key=%v", got)
	}
	if got := EffectiveAccountApiKeys(nil); len(got) != 0 {
		t.Fatalf("nil keys=%v", got)
	}
	if n := EffectiveAccountApiKeyCount(creds); n != 2 {
		t.Fatalf("count=%d", n)
	}
	if _, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{AuthorizedInstance: true}, true); err == nil {
		t.Fatal("authorized instance must reject enabled query")
	}
	if enabled, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{AuthorizedInstance: true}, false); err != nil || enabled {
		t.Fatalf("authorized disabled enabled=%v err=%v", enabled, err)
	}
	if _, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{AccountType: "oauth"}, true); err == nil {
		t.Fatal("oauth must reject enabled query")
	}
	if _, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{AccountType: "api_key"}, true); err == nil {
		t.Fatal("no keys must reject enabled query")
	}
	if enabled, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{AccountType: "api_key", Credentials: creds}, true); err != nil || !enabled {
		t.Fatalf("valid capability enabled=%v err=%v", enabled, err)
	}
	if enabled, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{AccountType: "api_key"}, false); err != nil || enabled {
		t.Fatalf("disabled passthrough enabled=%v err=%v", enabled, err)
	}
}

func TestW9DErrorPolicyArms(t *testing.T) {
	// 规则列表：非数组 / 元素非对象。
	if _, err := normalizeAccountErrorHandlingRules("x"); err == nil {
		t.Fatal("non-array rules must fail")
	}
	if _, err := normalizeAccountErrorHandlingRules([]any{"x"}); err == nil {
		t.Fatal("non-object rule must fail")
	}
	// 单条规则必填字段臂。
	rule := func(fields map[string]any) map[string]any {
		base := map[string]any{"action": "mark_unavailable", "conditions": []any{map[string]any{"type": "status_code", "values": []any{429.0}}}}
		for k, v := range fields {
			base[k] = v
		}
		return base
	}
	badRules := []map[string]any{
		{"conditions": "bad"},
		{"conditions": []any{}},
		{"conditions": []any{map[string]any{"type": "status_code"}}},
		{"conditions": []any{map[string]any{"type": "unknown"}}},
		{"conditions": []any{map[string]any{"type": "status_code", "values": "bad"}}},
		{"conditions": []any{map[string]any{"type": "status_code", "values": []any{"x"}}}},
		{"action": "bogus"},
		{"resetStrategy": "bogus"},
		{"cooldownSeconds": "x"},
		{"cooldownSeconds": -1.0},
		{"windowHours": 25.0},
		{"weekday": 8.0},
		{"hourStart": 24.0},
		{"threshold": 0.0},
		{"errorCode": 1.0},
		{"statusCodes": "bad"},
		{"statusCodes": []any{-1.0}},
		{"message": 1.0},
		{"traceIds": "bad"},
		{"models": 1.0},
	}
	for i, fields := range badRules {
		if _, err := normalizeAccountErrorHandlingRule(rule(fields), i); err == nil {
			t.Fatalf("bad rule[%d] must fail: %v", i, fields)
		}
	}
	// overrides 顶层非数组。
	if _, err := normalizeAccountErrorPolicyOverrides("x"); err == nil {
		t.Fatal("non-array overrides must fail")
	}
	if _, err := normalizeAccountErrorPolicyOverrides([]any{"x"}); err == nil {
		t.Fatal("non-object override must fail")
	}
	if !isAllDigits("123") || isAllDigits("12a") {
		t.Fatal("isAllDigits arms")
	}
}

func TestW9DUpstreamURLArms(t *testing.T) {
	// 合法 URL 保留路径。
	if _, err := validateOpenAICompatibleBaseURL("https://api.example.com/v1"); err != nil {
		t.Fatalf("valid url err=%v", err)
	}
	// 非法矩阵。
	bad := []string{
		"",
		"not a url",
		"https://user:pass@example.com/v", // 凭据
		"https://example.com/../v1",
		"https://example.com/v1/chat/completions",
	}
	for i, value := range bad {
		if _, err := validateOpenAICompatibleBaseURL(value); err == nil {
			t.Fatalf("bad url[%d]=%q must fail", i, value)
		}
	}
	// localhost 与私网地址判定。
	if !isLocalhostHostName("localhost") || isLocalhostHostName("example.com") {
		t.Fatal("localhost arms")
	}
	if !isPrivateOrReservedIP("10.0.0.1") || !isPrivateOrReservedIP("192.168.1.1") || isPrivateOrReservedIP("8.8.8.8") {
		t.Fatal("private ip arms")
	}
	if !isPrivateOrReservedIP("169.254.1.1") || !isPrivateOrReservedIP("172.16.0.1") || !isPrivateOrReservedIP("::1") {
		t.Fatal("reserved arms")
	}
	// 断言路径段合法性。
	if err := assertUpstreamPathSegments("/v1/x"); err != nil {
		t.Fatalf("valid segments err=%v", err)
	}
	if err := assertUpstreamPathSegments("/v1/x%zz"); err == nil {
		t.Fatal("invalid encoding segment must fail")
	}
	if !matchConsecutiveSlashes("a//b") || matchConsecutiveSlashes("a/b") {
		t.Fatal("consecutive slash arms")
	}
	if !matchesOpenAIEndpointPath([]string{"chat", "completions"}) {
		t.Fatal("openai endpoint path must match")
	}
	// validationMessageOrPolicy：非 upstream 校验错误 → 通用文案。
	if validationMessageOrPolicy(nil) != "上游 Base URL 格式无效" {
		t.Fatal("nil policy message")
	}
	if _, err := validateRawUpstreamURLString("not a url"); err == nil {
		t.Fatal("raw invalid url must fail")
	}
	if !isAlphaByte('a') || isAlphaByte('1') || !isDigitByte('1') || isDigitByte('a') {
		t.Fatal("char class arms")
	}
}

func mustIPv4(text string) (v [4]byte) {
	parts := strings.Split(text, ".")
	for i, p := range parts {
		n := 0
		for _, c := range p {
			n = n*10 + int(c-'0')
		}
		v[i] = byte(n)
	}
	return v
}

func TestW9DListUsageAndQuotaArms(t *testing.T) {
	// usage scope 去重。
	scopes := uniqueUsageScopes([]UsageScope{
		{RowKey: "a", SystemAccountID: "s", ScopeType: "account", ScopeID: "a"},
		{RowKey: "a", SystemAccountID: "s", ScopeType: "account", ScopeID: "a"},
		{RowKey: "b", SystemAccountID: "s", ScopeType: "account", ScopeID: "b"},
	})
	if len(scopes) != 2 {
		t.Fatalf("unique scopes=%v", scopes)
	}
	scope := accountUsageScope("row", "sys", "auth")
	if scope.RowKey != "row" || scope.SystemAccountID != "sys" || scope.ScopeID != "auth" || scope.ScopeType == "" {
		t.Fatalf("scope=%+v", scope)
	}
	noAuth := accountUsageScope("row", "sys", "")
	if noAuth.ScopeID != "row" {
		t.Fatalf("no auth scope=%+v", noAuth)
	}
	// quota recovery 校验臂。
	if _, err := normalizeQuotaRecoveryPolicy("x"); err == nil {
		t.Fatal("non-object policy must fail")
	}
	if _, err := normalizeQuotaRecoverySchedule("x"); err == nil {
		t.Fatal("non-object schedule must fail")
	}
	if _, err := normalizeQuotaRecoverySchedule([]any{}); err == nil {
		t.Fatal("empty schedule must fail")
	}
	if !validQuotaRecoveryTimezone("UTC") || validQuotaRecoveryTimezone("Mars/Olympus") {
		t.Fatal("timezone arms")
	}
	if _, err := quotaRecoveryIntegerInRange("x", 1, 2, "v"); err == nil {
		t.Fatal("non-integer range must fail")
	}
	if jsonEncodeLength(map[string]any{}) == 0 {
		t.Fatal("json length must be positive")
	}
}

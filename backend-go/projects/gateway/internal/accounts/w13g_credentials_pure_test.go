package accounts

// w13g 凭据/策略规范化与上游 URL 策略纯函数补测：错误臂矩阵。
// 覆盖 credentials_normalize.go、error_policy.go、response_inspection.go、
// quota_recovery.go、endpoint_modes.go、upstream_base_url.go 的分支。
//
// 不可达登记（w13g，无法从包内入口触发）：
// - credentials_normalize.go:99-101 NormalizeAccountCredentialsForWrite 的
//   accountCredentialsRecord err 分支：该函数体恒返回 nil error。
// - credentials_normalize.go:674-676 / 703-705 / 707-709 /
//   canonicalizeNormalizedCredentials 与 assertAccountCredentialsJSONSize 的
//   json.Marshal/Unmarshal err 分支：输入全部来自已验证的 JSON 形状值，
//   无法注入不可序列化值。
// - upstream_base_url.go:406-408 isPrivateOrReservedIP 的 To4()==nil 臂：
//   含 "." 且不含 ":" 的输入 ParseIP 成功时必为合法 IPv4，To4 恒非 nil。
// - upstream_base_url.go:461-462 mustIPv6Network panic 臂：blockedIPv6Ranges
//   为静态合法字面量。
// - endpoint_modes.go:456-458 providerAccountCredentialDriverForContext 的
//   err 传播臂：该函数恒返回 nil error。
// - quota_recovery.go:53-55 策略整体大小超限臂：字段集合固定（3 类 schedule），
//   strategy/jitter/timezone 均有形状与取值约束，序列化结果恒 < 4096 字节。
// - upstream_base_url.go:37-51 upstreamURLSecurityConfig 的 allow=true 与
//   allowlist 解析臂：进程级 sync.Once，测试无法在不违反全局状态顺序约束的
//   前提下注入环境变量（同文件 sync.go 既有测试以环境探测 + skip 规避）。
// - upstream_base_url.go:174-176 路径不以 / 开头臂：parseRawAbsoluteURLParts
//   把空路径规范为 "/"，到达该检查时路径恒以 / 开头。
// - upstream_base_url.go:199-201 Fragment/ForceQuery 臂：含 "?" 或 "#" 的输入
//   在裸解析层已被拒绝，到达 url.Parse 级检查时 query/fragment 恒为空。

import (
	"math"
	"net/url"
	"os"
	"strings"
	"testing"
)

func w13gPrivateUpstreamAllowed() bool {
	value := os.Getenv("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS")
	return value == "true" || value == "1"
}

// w13gOpenAIContext 构造已注册 gpt/openai-compatible 驱动的默认上下文。
func w13gOpenAIContext(provider string) *EndpointModeDefaultContext {
	return &EndpointModeDefaultContext{
		ProviderCode: provider, ProtocolCode: "openai", ProtocolVersion: "v1",
	}
}

func TestW13GCredentialsNormalizeTypeAndKeyGates(t *testing.T) {
	// 未知账户类型。
	if _, err := NormalizeAccountCredentialsForWrite("bogus", Credentials{"api_key": "sk"}, nil); err == nil ||
		!strings.Contains(err.Error(), "不支持凭据写入") {
		t.Fatalf("未知账户类型：%v", err)
	}
	// 未知字段。
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk", "base_url": "https://api.example.com", "bogus_key": 1,
	}, nil); err == nil || !strings.Contains(err.Error(), "不支持的字段") {
		t.Fatalf("未知字段：%v", err)
	}
	// 废弃键被剥离后不再拒绝。
	normalized, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk", "base_url": "https://api.example.com",
		"codex_responses_safe_repair_enabled": true,
	}, nil)
	if err != nil {
		t.Fatalf("废弃键剥离：%v", err)
	}
	if _, ok := normalized["codex_responses_safe_repair_enabled"]; ok {
		t.Fatal("废弃键应被剥离")
	}
	// api_keys 与 api_key 均缺失 → 列表回落 nil 元素 → "API Key不能为空"。
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"base_url": "https://api.example.com",
	}, nil); err == nil || !strings.Contains(err.Error(), "API Key不能为空") {
		t.Fatalf("缺 key 回落：%v", err)
	}
	// 超长的 base_url 触发字节上限。
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk", "base_url": "https://api.example.com/" + strings.Repeat("p", 2048),
	}, nil); err == nil || !strings.Contains(err.Error(), "不能超过") {
		t.Fatalf("超长 base_url：%v", err)
	}
	// 私网上游拒绝（环境显式放行时跳过）。
	if !w13gPrivateUpstreamAllowed() {
		if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
			"api_key": "sk", "base_url": "http://127.0.0.1:8080/v1",
		}, nil); err == nil || !strings.Contains(err.Error(), "本机、内网") {
			t.Fatalf("私网 base_url：%v", err)
		}
	}
	// 非法 supported_endpoint_modes。
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk", "base_url": "https://api.example.com",
		"supported_endpoint_modes": "chat_json",
	}, nil); err == nil || !strings.Contains(err.Error(), "必须是数组") {
		t.Fatalf("modes 非数组：%v", err)
	}
	// 多 key 加权策略：权重非法。
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_keys": []any{"sk-a", "sk-b"}, "api_key_strategy": "weighted_round_robin",
		"api_key_weights": []any{float64(1), float64(101)},
		"base_url":        "https://api.example.com",
	}, nil); err == nil || !strings.Contains(err.Error(), "权重") {
		t.Fatalf("权重越界：%v", err)
	}
	// 凭据整体超限（10 个 4KB key，内容互异避免去重）。
	oversize := Credentials{"api_keys": []any{}, "base_url": "https://api.example.com"}
	for index := 0; index < 10; index++ {
		oversize["api_keys"] = append(oversize["api_keys"].([]any), "sk-"+strings.Repeat("k", 4096)+"-"+string(rune('a'+index)))
	}
	if _, err := NormalizeAccountCredentialsForWrite("api_key", oversize, nil); err == nil ||
		!strings.Contains(err.Error(), "整体大小") {
		t.Fatalf("凭据整体超限：%v", err)
	}
}

func TestW13GOAuthCredentialArms(t *testing.T) {
	base := func(extra Credentials) Credentials {
		credentials := Credentials{"base_url": "https://api.example.com"}
		for key, value := range extra {
			credentials[key] = value
		}
		return credentials
	}
	cases := []struct {
		name  string
		input Credentials
		want  string
	}{
		{"access_token 非字符串", base(Credentials{"access_token": 1}), "Access Token不能为空"},
		{"refresh_token 非布尔", base(Credentials{"refresh_token": false}), "Refresh Token不能为空"},
		{"expires_at 坏时间", base(Credentials{"access_token": "at", "expires_at": "bad"}), "必须是有效时间字符串"},
		{"expires_at 空白", base(Credentials{"access_token": "at", "expires_at": "  "}), "必须是有效时间字符串"},
		{"client_id 非字符串", base(Credentials{"access_token": "at", "client_id": 1}), "OAuth client_id不能为空"},
		{"id_token 非字符串", base(Credentials{"access_token": "at", "id_token": 1}), "OAuth id_token不能为空"},
		{"token_type 非字符串", base(Credentials{"access_token": "at", "token_type": 1}), "OAuth token_type不能为空"},
		{"scope 非字符串", base(Credentials{"access_token": "at", "scope": 1}), "OAuth scope不能为空"},
		{"email 非字符串", base(Credentials{"access_token": "at", "email": 1}), "OAuth email不能为空"},
		{"account_id 非字符串", base(Credentials{"access_token": "at", "account_id": 1}), "OpenAI account_id不能为空"},
		{"chatgpt_account_id 非字符串", base(Credentials{"access_token": "at", "chatgpt_account_id": 1}), "OpenAI chatgpt_account_id不能为空"},
		{"organization_id 非字符串", base(Credentials{"access_token": "at", "organization_id": 1}), "Anthropic organization_id不能为空"},
	}
	for _, c := range cases {
		if _, err := NormalizeAccountCredentialsForWrite("oauth", c.input, nil); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：%v", c.name, err)
		}
	}
	// base_url 缺失。
	if _, err := NormalizeAccountCredentialsForWrite("oauth", Credentials{"access_token": "at"}, nil); err == nil ||
		!strings.Contains(err.Error(), "Base URL不能为空") {
		t.Fatalf("缺 base_url：%v", err)
	}
	// Anthropic profile：access_token 必填。
	anthropic := &EndpointModeDefaultContext{ProviderCode: "anthropic", ProtocolCode: "anthropic", ProtocolVersion: "v1"}
	if _, err := NormalizeAccountCredentialsForWrite("oauth", base(nil), anthropic); err == nil ||
		!strings.Contains(err.Error(), "Anthropic OAuth Access Token 不能为空") {
		t.Fatalf("anthropic token 必填：%v", err)
	}
	// gpt profile：access_token 必须带 account_id。
	if _, err := NormalizeAccountCredentialsForWrite("oauth", base(Credentials{"access_token": "at"}),
		w13gOpenAIContext("gpt")); err == nil || !strings.Contains(err.Error(), "缺少 account_id") {
		t.Fatalf("gpt account_id 必填：%v", err)
	}
	// 未注册驱动的上下文 → modes 归一化报错。
	if _, err := NormalizeAccountCredentialsForWrite("oauth", base(Credentials{"access_token": "at"}),
		&EndpointModeDefaultContext{ProviderCode: "bogus", ProtocolCode: "bogus", ProtocolVersion: "v1"}); err == nil ||
		!strings.Contains(err.Error(), "未注册接口能力归一化") {
		t.Fatalf("未注册驱动：%v", err)
	}
	// gpt 覆盖字段非法枚举。
	if _, err := NormalizeAccountCredentialsForWrite("oauth", base(Credentials{
		"access_token": "at", "account_id": "acc", "service_tier_override": "bogus",
	}), w13gOpenAIContext("gpt")); err == nil || !strings.Contains(err.Error(), "服务等级覆盖无效") {
		t.Fatalf("gpt 服务等级枚举：%v", err)
	}
	if _, err := NormalizeAccountCredentialsForWrite("oauth", base(Credentials{
		"access_token": "at", "account_id": "acc", "reasoning_effort_override": "bogus",
	}), w13gOpenAIContext("gpt")); err == nil || !strings.Contains(err.Error(), "思考级别覆盖无效") {
		t.Fatalf("gpt 思考级别枚举：%v", err)
	}
	// 覆盖字段非字符串（任意已注册上下文都会在 token 形状检查处失败）。
	if _, err := NormalizeAccountCredentialsForWrite("oauth", base(Credentials{
		"access_token": "at", "service_tier_override": 1,
	}), nil); err == nil || !strings.Contains(err.Error(), "服务等级覆盖无效") {
		t.Fatalf("服务等级类型：%v", err)
	}
	if _, err := NormalizeAccountCredentialsForWrite("oauth", base(Credentials{
		"access_token": "at", "reasoning_effort_override": 1,
	}), nil); err == nil || !strings.Contains(err.Error(), "思考级别覆盖无效") {
		t.Fatalf("思考级别类型：%v", err)
	}
	// 凭据整体超限：三个 16KB 级字段相加。
	huge := base(Credentials{
		"access_token":  strings.Repeat("a", 16*1024),
		"refresh_token": strings.Repeat("r", 16*1024),
		"id_token":      strings.Repeat("i", 16*1024),
	})
	if _, err := NormalizeAccountCredentialsForWrite("oauth", huge, nil); err == nil ||
		!strings.Contains(err.Error(), "整体大小") {
		t.Fatalf("oauth 整体超限：%v", err)
	}
	// 合法 oauth 全字段透传。
	normalized, err := NormalizeAccountCredentialsForWrite("oauth", base(Credentials{
		"access_token": " at ", "refresh_token": " rt ", "expires_at": "2026-01-01T00:00:00Z",
		"client_id": "cid", "id_token": "idt", "token_type": "bearer", "scope": "s",
		"email": "e", "account_id": " acc ", "chatgpt_user_id": "u", "plan_type": "plus",
	}), w13gOpenAIContext("gpt"))
	if err != nil {
		t.Fatalf("合法 oauth：%v", err)
	}
	if normalized["account_id"] != "acc" || normalized["access_token"] != "at" {
		t.Fatalf("oauth 归一化：%v", normalized)
	}
}

func TestW13GGoogleOAuthCredentialArms(t *testing.T) {
	base := func(extra Credentials) Credentials {
		credentials := Credentials{"base_url": "https://api.example.com"}
		for key, value := range extra {
			credentials[key] = value
		}
		return credentials
	}
	cases := []struct {
		name  string
		input Credentials
		want  string
	}{
		{"access_token 非字符串", base(Credentials{"access_token": 1}), "Google Access Token不能为空"},
		{"refresh_token 非字符串", base(Credentials{"refresh_token": 1}), "Google Refresh Token不能为空"},
		{"两者皆空", base(nil), "Google OAuth 凭据不能为空"},
		{"refresh 缺 client", base(Credentials{"refresh_token": "rt"}), "Client ID 和 Client Secret"},
		{"expires_at 坏时间", base(Credentials{"access_token": "at", "expires_at": "x"}), "必须是有效时间字符串"},
		{"quota_project_id 非字符串", base(Credentials{"access_token": "at", "quota_project_id": 1}), "Google Quota Project ID不能为空"},
		{"drive_storage_limit 非法", base(Credentials{"access_token": "at", "drive_storage_limit": "abc"}), "必须是非负整数"},
		{"service_tier 非字符串", base(Credentials{"access_token": "at", "service_tier_override": 1}), "服务等级覆盖无效"},
	}
	for _, c := range cases {
		if _, err := NormalizeAccountCredentialsForWrite("google_oauth", c.input, nil); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：%v", c.name, err)
		}
	}
	// base_url 缺失。
	if _, err := NormalizeAccountCredentialsForWrite("google_oauth", Credentials{"access_token": "at"}, nil); err == nil ||
		!strings.Contains(err.Error(), "Base URL不能为空") {
		t.Fatalf("google 缺 base_url：%v", err)
	}
	// 整体超限（须带 client，避免 refresh 约束先行失败）。
	huge := base(Credentials{
		"access_token":  strings.Repeat("a", 16*1024),
		"refresh_token": strings.Repeat("r", 16*1024),
		"client_id":     "cid",
		"client_secret": strings.Repeat("c", 16*1024),
	})
	if _, err := NormalizeAccountCredentialsForWrite("google_oauth", huge, nil); err == nil ||
		!strings.Contains(err.Error(), "整体大小") {
		t.Fatalf("google 整体超限：%v", err)
	}
	// 合法：drive 数值接受 float 与全数字字符串。
	normalized, err := NormalizeAccountCredentialsForWrite("google_oauth", base(Credentials{
		"access_token": "at", "client_id": "cid", "client_secret": "sec",
		"drive_storage_limit": float64(15), "drive_storage_usage": " 7 ",
		"project_id": "p", "oauth_type": "gemini",
	}), nil)
	if err != nil {
		t.Fatalf("合法 google：%v", err)
	}
	if normalized["drive_storage_limit"] != float64(15) || normalized["drive_storage_usage"] != "7" {
		t.Fatalf("google 归一化：%v", normalized)
	}
}

func TestW13GErrorPolicyRuleArms(t *testing.T) {
	// 非数组顶层。
	if _, err := normalizeAccountErrorHandlingRules("x"); err == nil ||
		!strings.Contains(err.Error(), "格式无效") {
		t.Fatalf("规则非数组：%v", err)
	}
	// 布尔 inherited 系统规则拒绝（覆盖 textEqual bool 臂）。
	if _, err := normalizeAccountErrorHandlingRules([]any{map[string]any{"inherited": true}}); err == nil ||
		!strings.Contains(err.Error(), "系统继承规则") {
		t.Fatalf("继承规则：%v", err)
	}
	rule := func(extra map[string]any) map[string]any {
		record := map[string]any{
			"enabled": true, "name": "r", "priority": float64(1), "action": "retry_next",
			"status_codes": []any{float64(429)},
		}
		for key, value := range extra {
			record[key] = value
		}
		return record
	}
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"规则非对象", "x", "格式无效"},
		{"不支持字段", rule(map[string]any{"bogus": 1}), "不支持字段"},
		{"enabled 非布尔", func() any { record := rule(nil); record["enabled"] = 1; return record }(), "必须是布尔值"},
		{"name 空白", func() any { record := rule(nil); record["name"] = " "; return record }(), "规则名称不能为空"},
		{"priority 非法", func() any { record := rule(nil); record["priority"] = float64(0); return record }(), "大于 0 的整数"},
		{"action 非法", func() any { record := rule(nil); record["action"] = "bogus"; return record }(), "动作无效"},
		{"status_codes 非数组", func() any { record := rule(nil); record["status_codes"] = "x"; return record }(), "状态码必须是数字数组"},
		{"状态码非法", func() any { record := rule(nil); record["status_codes"] = []any{float64(99)}; return record }(), "状态码不合法"},
		{"2xx 状态码", func() any { record := rule(nil); record["status_codes"] = []any{float64(200)}; return record }(), "2xx 成功状态码"},
		{"error_codes 非数组", func() any { record := rule(nil); record["error_codes"] = "x"; return record }(), "错误码必须是字符串数组"},
		{"error_codes 2xx", func() any {
			record := rule(nil)
			record["error_codes"] = []any{"200"}
			return record
		}(), "不能填写 2xx 成功码"},
		{"error_types 非数组", func() any { record := rule(nil); record["error_types"] = "x"; return record }(), "错误类型必须是字符串数组"},
		{"keywords 非法项", func() any { record := rule(nil); record["keywords"] = []any{1}; return record }(), "规则关键字不能为空"},
		{"description 非字符串", func() any { record := rule(nil); record["description"] = 1; return record }(), "必须是字符串"},
		{"无匹配条件", func() any { record := rule(nil); delete(record, "status_codes"); return record }(), "至少需要一个匹配条件"},
		{"限流策略缺省", func() any {
			record := rule(nil)
			record["action"] = "rate_limited"
			return record
		}(), "恢复策略无效"},
		{"限流 duration 缺失", func() any {
			record := rule(nil)
			record["action"] = "rate_limited"
			record["reset_strategy"] = "duration"
			return record
		}(), "恢复小时数"},
		{"限流 daily 小时非法", func() any {
			record := rule(nil)
			record["action"] = "rate_limited"
			record["reset_strategy"] = "daily"
			record["daily_reset_hour"] = float64(24)
			return record
		}(), "0-23 的整数"},
		{"限流 weekly 缺日", func() any {
			record := rule(nil)
			record["action"] = "rate_limited"
			record["reset_strategy"] = "weekly"
			return record
		}(), "每周恢复日期"},
		{"限流 weekly 小时非法", func() any {
			record := rule(nil)
			record["action"] = "rate_limited"
			record["reset_strategy"] = "weekly"
			record["weekly_reset_day"] = float64(2)
			record["weekly_reset_hour"] = float64(99)
			return record
		}(), "0-23 的整数"},
	}
	for _, c := range cases {
		if _, err := normalizeAccountErrorHandlingRules([]any{c.input}); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：%v", c.name, err)
		}
	}
	// 合法 weekly 限流 + 重复状态码去重 + 空数组回落 + 关键字去重。
	normalized, err := normalizeAccountErrorHandlingRules([]any{map[string]any{
		"enabled": true, "name": " r ", "priority": float64(2), "action": "rate_limited",
		"reset_strategy": "weekly", "weekly_reset_day": float64(3), "weekly_reset_hour": float64(8),
		"status_codes": []any{float64(429), float64(429)},
		"error_types":  []any{"quota", "quota"},
		"keywords":     []any{},
		"description":  " d ",
	}})
	if err != nil {
		t.Fatalf("合法限流规则：%v", err)
	}
	written := normalized[0].(map[string]any)
	if written["name"] != "r" || written["weekly_reset_day"] != float64(3) {
		t.Fatalf("限流规则归一化：%v", written)
	}
	if list, ok := written["status_codes"].([]any); !ok || len(list) != 1 {
		t.Fatalf("状态码去重：%v", written["status_codes"])
	}
	if _, exists := written["keywords"]; exists {
		t.Fatal("空 keywords 不应落键")
	}
}

func TestW13GErrorPolicyOverrideArms(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"非数组", "x", "覆盖格式无效"},
		{"条目非对象", []any{"x"}, "覆盖格式无效"},
		{"系统规则 ID 错", []any{map[string]any{"system_rule_id": "other", "action": "delete"}}, "系统规则 ID 无效"},
		{"动作错", []any{map[string]any{"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID, "action": "bogus"}}, "动作无效"},
		{"不支持字段", []any{map[string]any{
			"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID, "action": "delete", "bogus": 1,
		}}, "不支持字段"},
		{"replace 索引错", []any{map[string]any{
			"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID, "action": "replace", "rule_index": "x",
		}}, "规则索引无效"},
	}
	for _, c := range cases {
		if _, err := normalizeAccountErrorPolicyOverrides(c.input); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：%v", c.name, err)
		}
	}
	// 合法 replace + delete。
	normalized, err := normalizeAccountErrorPolicyOverrides([]any{
		map[string]any{"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID, "action": "replace", "rule_index": float64(2)},
		map[string]any{"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID, "action": "delete"},
	})
	if err != nil || len(normalized) != 2 {
		t.Fatalf("合法覆盖：%v %v", normalized, err)
	}
	if normalized[0].(map[string]any)["rule_index"] != float64(2) {
		t.Fatalf("replace 索引：%v", normalized[0])
	}
}

func TestW13GResponseInspectionArms(t *testing.T) {
	// 顶层形状。
	if _, err := normalizeAccountResponseInspectionRules("x"); err == nil ||
		!strings.Contains(err.Error(), "必须是数组") {
		t.Fatalf("顶层非数组：%v", err)
	}
	tooMany := []any{}
	for index := 0; index < 21; index++ {
		tooMany = append(tooMany, map[string]any{"enabled": false, "name": "n", "priority": float64(1),
			"match": map[string]any{}, "action": "observe"})
	}
	if _, err := normalizeAccountResponseInspectionRules(tooMany); err == nil ||
		!strings.Contains(err.Error(), "不能超过 20 条") {
		t.Fatalf("超 20 条：%v", err)
	}
	validRule := func(extra map[string]any) map[string]any {
		record := map[string]any{"enabled": true, "name": "n", "priority": float64(1),
			"match": map[string]any{"errorCodes": []any{"429"}}, "action": "observe"}
		for key, value := range extra {
			record[key] = value
		}
		return record
	}
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"规则非对象", "x", "参数无效"},
		{"不支持键", validRule(map[string]any{"bogus": 1}), "参数无效"},
		{"enabled 非布尔", func() any { record := validRule(nil); record["enabled"] = 1; return record }(), "参数无效"},
		{"name 非字符串", func() any { record := validRule(nil); record["name"] = 1; return record }(), "参数无效"},
		{"name 空白", func() any { record := validRule(nil); record["name"] = " "; return record }(), "参数无效"},
		{"priority 越界", func() any { record := validRule(nil); record["priority"] = float64(0); return record }(), "参数无效"},
		{"match 非对象", func() any { record := validRule(nil); record["match"] = "x"; return record }(), "参数无效"},
		{"action 非法", func() any { record := validRule(nil); record["action"] = "bogus"; return record }(), "参数无效"},
		{"notes 非字符串", func() any { record := validRule(nil); record["notes"] = 1; return record }(), "参数无效"},
		{"notes 超长", func() any { record := validRule(nil); record["notes"] = strings.Repeat("n", 1001); return record }(), "参数无效"},
		{"无匹配条件", func() any { record := validRule(nil); record["match"] = map[string]any{}; return record }(), "至少需要一个匹配条件"},
		{"clientProfiles 非法", func() any {
			record := validRule(nil)
			record["match"] = map[string]any{"clientProfiles": []any{"bogus"}}
			return record
		}(), "参数无效"},
		{"文本字段非数组", func() any {
			record := validRule(nil)
			record["match"] = map[string]any{"outputTextIncludes": "x"}
			return record
		}(), "参数无效"},
		{"文本项空白", func() any {
			record := validRule(nil)
			record["match"] = map[string]any{"outputTextIncludes": []any{" "}}
			return record
		}(), "参数无效"},
		{"未知 match 键", func() any {
			record := validRule(nil)
			record["match"] = map[string]any{"bogus": []any{"x"}}
			return record
		}(), "参数无效"},
	}
	for _, c := range cases {
		if _, err := normalizeAccountResponseInspectionRules([]any{c.input}); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：%v", c.name, err)
		}
	}
	// enabled=false 时允许空 match；notes 生效。
	normalized, err := normalizeAccountResponseInspectionRules([]any{map[string]any{
		"enabled": false, "name": "disabled", "priority": float64(9),
		"match": map[string]any{}, "action": "drop_event", "notes": " n ",
	}})
	if err != nil || len(normalized) != 1 {
		t.Fatalf("disabled 规则：%v %v", normalized, err)
	}
	if normalized[0].(map[string]any)["notes"] != "n" {
		t.Fatalf("notes 归一化：%v", normalized[0])
	}
}

func TestW13GQuotaRecoveryPolicyArms(t *testing.T) {
	if _, err := normalizeQuotaRecoveryPolicy("x"); err == nil ||
		!strings.Contains(err.Error(), "必须是对象") {
		t.Fatalf("非对象：%v", err)
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"bogus": map[string]any{}}); err == nil ||
		!strings.Contains(err.Error(), "不受支持") {
		t.Fatalf("不支持字段：%v", err)
	}
	schedule := func(extra map[string]any) map[string]any {
		record := map[string]any{"reset_strategy": "duration", "duration_minutes": float64(60)}
		for key, value := range extra {
			record[key] = value
		}
		return record
	}
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"schedule 非对象", map[string]any{"api_key": "x"}, "必须是对象"},
		{"strategy 非法", map[string]any{"api_key": map[string]any{"reset_strategy": "bogus"}}, "duration、daily 或 weekly"},
		{"duration 越界", map[string]any{"api_key": schedule(map[string]any{"duration_minutes": float64(10)})}, "30-10080 的整数"},
		{"daily 小时越界", map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(24)}}, "0-23 的整数"},
		{"weekly 日越界", map[string]any{"google_oauth": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(7)}}, "0-6 的整数"},
		{"weekly 小时越界", map[string]any{"api_key": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(1), "weekly_reset_hour": -1}}, "0-23 的整数"},
		{"jitter 非法", map[string]any{"api_key": schedule(map[string]any{"jitter_minutes": float64(10)})}, "jitter_minutes固定15"},
		{"timezone 空白", map[string]any{"api_key": schedule(map[string]any{"timezone": "  "})}, "timezone 无效"},
		{"timezone 未知", map[string]any{"api_key": schedule(map[string]any{"timezone": "Mars/Olympus"})}, "timezone 无效"},
	}
	for _, c := range cases {
		if _, err := normalizeQuotaRecoveryPolicy(c.input); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：%v", c.name, err)
		}
	}
	// 合法：三类 schedule + UTC 偏移时区 + 显式 jitter 15。
	normalized, err := normalizeQuotaRecoveryPolicy(map[string]any{
		"api_key":      schedule(nil),
		"oauth":        map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(6), "timezone": "utc+05:30"},
		"google_oauth": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(1), "weekly_reset_hour": float64(2), "jitter_minutes": float64(15)},
	})
	if err != nil {
		t.Fatalf("合法策略：%v", err)
	}
	if normalized["oauth"].(map[string]any)["timezone"] != "utc+05:30" {
		t.Fatalf("偏移时区：%v", normalized["oauth"])
	}
	// 偏移时区形状矩阵。
	for name, valid := range map[string]bool{
		"UTC": true, "GMT": true, "UTC+08": true, "UTC-08:00": true, "GMT+5": false,
		"UTC+08:00:00": false, "UTC+080": false, "UTCX": false,
	} {
		_, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": schedule(map[string]any{"timezone": name})})
		if valid != (err == nil) {
			t.Fatalf("时区 %s 期望 %v，得到 %v", name, valid, err)
		}
	}
	// 通过凭据写入附带策略：非法策略错误传播。
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk", "base_url": "https://api.example.com",
		"quota_recovery_policy": "x",
	}, nil); err == nil || !strings.Contains(err.Error(), "必须是对象") {
		t.Fatalf("策略经凭据传播：%v", err)
	}
	// response_inspection 与 overrides 经凭据写入的错误传播。
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk", "base_url": "https://api.example.com",
		"response_inspection_rules": "x",
	}, nil); err == nil || !strings.Contains(err.Error(), "必须是数组") {
		t.Fatalf("响应检查经凭据传播：%v", err)
	}
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{
		"api_key": "sk", "base_url": "https://api.example.com",
		"error_handling_rule_overrides": "x",
	}, nil); err == nil || !strings.Contains(err.Error(), "覆盖格式无效") {
		t.Fatalf("覆盖经凭据传播：%v", err)
	}
}

func TestW13GEndpointModeDriverArms(t *testing.T) {
	// xai 驱动：pinned 上下文非法 modes。
	xai := endpointModeDefaultContext{providerCode: "xai", accountType: "api_key",
		protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: xaiOpenAIV1ProfileID}
	if _, err := normalizeEndpointModesForWrite(opt("x"), xai); err == nil ||
		!strings.Contains(err.Error(), "必须是数组") {
		t.Fatalf("xai 驱动：%v", err)
	}
	// deepseek anthropic pin：只允许 messages 对。
	deepSeekAnthropic := endpointModeDefaultContext{providerCode: "deepseek",
		protocolCode: "anthropic", protocolVersion: "v1", providerProtocolProfileID: deepSeekAnthropicV1ProfileID}
	if _, err := normalizeEndpointModesForWrite(opt([]any{"messages_json", "message_token_counting"}), deepSeekAnthropic); err == nil ||
		!strings.Contains(err.Error(), "DeepSeek") {
		t.Fatalf("deepseek anthropic pin：%v", err)
	}
	if _, err := normalizeEndpointModesForWrite(opt("x"), deepSeekAnthropic); err == nil ||
		!strings.Contains(err.Error(), "必须是数组") {
		t.Fatalf("deepseek anthropic 非法值：%v", err)
	}
	// glm anthropic pin。
	glmAnthropic := endpointModeDefaultContext{providerCode: "glm",
		protocolCode: "anthropic", protocolVersion: "v1", providerProtocolProfileID: glmCodingAnthropicV1ProfileID}
	if _, err := normalizeEndpointModesForWrite(opt([]any{"messages_json", "message_token_counting"}), glmAnthropic); err == nil ||
		!strings.Contains(err.Error(), "GLM") {
		t.Fatalf("glm anthropic pin：%v", err)
	}
	// glm openai：非法值传播 + 越出 chat 对拒绝。
	glmOpenAI := endpointModeDefaultContext{providerCode: "glm",
		protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: glmGeneralOpenAIV1ProfileID}
	if _, err := normalizeEndpointModesForWrite(opt("x"), glmOpenAI); err == nil ||
		!strings.Contains(err.Error(), "必须是数组") {
		t.Fatalf("glm openai 非法值：%v", err)
	}
	if _, err := normalizeEndpointModesForWrite(opt([]any{"responses_json"}), glmOpenAI); err == nil ||
		!strings.Contains(err.Error(), "对话补全") {
		t.Fatalf("glm openai 越界：%v", err)
	}
	// anthropic / gemini 驱动非法值。
	anthropicCtx := endpointModeDefaultContext{providerCode: "anthropic",
		protocolCode: "anthropic", protocolVersion: "v1"}
	if _, err := normalizeEndpointModesForWrite(opt([]any{"chat_json"}), anthropicCtx); err == nil {
		t.Fatal("anthropic 驱动应拒绝 openai 模式")
	}
	geminiCtx := endpointModeDefaultContext{providerCode: "gemini",
		protocolCode: "gemini", protocolVersion: "v1beta"}
	if _, err := normalizeEndpointModesForWrite(opt([]any{"chat_json"}), geminiCtx); err == nil {
		t.Fatal("gemini 驱动应拒绝 openai 模式")
	}
	// 未注册上下文。
	unregistered := endpointModeDefaultContext{providerCode: "bogus", protocolCode: "bogus", protocolVersion: "v1"}
	if _, err := normalizeEndpointModesForWrite(opt(nil), unregistered); err == nil ||
		!strings.Contains(err.Error(), "未注册") {
		t.Fatalf("未注册驱动：%v", err)
	}
	// 健康检查模式：anthropic provider 首选 messages_json。
	if mode := preferredHealthCheckEndpointMode("anthropic", "profile-x"); mode != "messages_json" {
		t.Fatalf("anthropic 首选：%s", mode)
	}
	if mode := preferredHealthCheckEndpointMode("gpt", "profile-g"); mode != "responses_sse" {
		t.Fatalf("gpt 首选：%s", mode)
	}
	if mode := preferredHealthCheckEndpointMode("openai", "profile_gemini_native_v1beta"); mode != "generate_content_json" {
		t.Fatalf("gemini native 首选：%s", mode)
	}
	if mode := preferredHealthCheckEndpointMode("openai", "deepseek-anthropic-v1"); mode != "messages_json" {
		t.Fatalf("profile anthropic 首选：%s", mode)
	}
	// resolveDefault：无 _json 时回落第一个可用形态。
	mode, err := resolveDefaultHealthCheckEndpointMode("openai", "p", []string{"chat_sse", "responses_sse"})
	if err != nil || mode != "chat_sse" {
		t.Fatalf("无 json 回落：%s %v", mode, err)
	}
	if _, err := resolveDefaultHealthCheckEndpointMode("openai", "p", nil); err == nil {
		t.Fatal("空形态应报错")
	}
	// resolveHealthCheckEndpointMode：nil 值回落默认、非法值、images 目录未证实。
	if _, err := resolveHealthCheckEndpointMode(nil, "openai", "p", []string{"chat_json"}, nil); err != nil {
		t.Fatalf("nil 值回落：%v", err)
	}
	bogus := "bogus_mode"
	if _, err := resolveHealthCheckEndpointMode(&bogus, "openai", "p", nil, nil); err == nil {
		t.Fatal("非法形态应报错")
	}
	images := "images_json"
	if _, err := resolveHealthCheckEndpointMode(&images, "openai", "p", []string{"images_json"}, nil); err == nil {
		t.Fatal("images 未证实应报错")
	}
	notEnabled := "responses_json"
	if _, err := resolveHealthCheckEndpointMode(&notEnabled, "openai", "p", []string{"chat_json"}, nil); err == nil {
		t.Fatal("未启用形态应报错")
	}
}

func TestW13GUpstreamBaseURLPureArms(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"空白", "   ", "不能为空"},
		{"制表符", "https://api.example.com\tx", "空白字符"},
		{"全角空格", "https://api.example.com\u00a0x", "空白字符"},
		{"控制字符", "https://api.example.com\x01", "控制字符"},
		{"反斜杠", `https://api.example.com\\v1`, "反斜杠"},
		{"userinfo", "https://user:pass@api.example.com/v1", "用户名或密码"},
		{"查询参数", "https://api.example.com/v1?key=1", "查询参数"},
		{"片段", "https://api.example.com/v1#frag", "片段标识"},
		{"三斜杠", "https:///api.example.com", "两个斜杠"},
		{"无主机", "https://", "主机名"},
		{"仅端口无主机", "https://:8080", "主机名"},
		{"连续斜杠", "https://api.example.com//v1", "连续斜杠"},
		{"v1 具体路径", "https://api.example.com/v1/chat/completions", "具体接口路径"},
		{"具体路径", "https://api.example.com/chat/completions", "具体接口路径"},
		{"编码斜杠", "https://api.example.com/%2fv1", "编码后的斜杠"},
		{"路径编码无效", "https://api.example.com/%zz", "编码无效"},
		{"点段", "https://api.example.com/./v1", ". 或 .. 段"},
		{"坏编码段", "https://api.example.com/%2e%2e/v1", ". 或 .. 段"},
		{"非 http 协议", "ftp://api.example.com/v1", "http 或 https"},
	}
	for _, c := range cases {
		if _, err := validateOpenAICompatibleBaseURL(c.input); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s（%s）：%v", c.name, c.input, err)
		}
	}
	// 合法路径矩阵。
	for _, ok := range []string{"https://api.example.com", "https://api.example.com/v1",
		"https://api.example.com/v1/", "https://api.example.com/openai/v1"} {
		if _, err := validateOpenAICompatibleBaseURL(ok); err != nil {
			t.Fatalf("合法 %s：%v", ok, err)
		}
	}
	// 私网判定矩阵（纯字面量，不依赖环境）。
	private := []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.1.1",
		"100.64.0.1", "224.0.0.1", "240.0.0.1", "::1", "fe80::1", "fc00::1", "2001:db8::1", "::ffff:10.0.0.1"}
	for _, host := range private {
		if !isPrivateOrReservedIP(host) {
			t.Fatalf("%s 应判定为私网/保留", host)
		}
	}
	public := []string{"8.8.8.8", "example.com", "", "256.1.1.1", "::zzz"}
	for _, host := range public {
		if isPrivateOrReservedIP(host) {
			t.Fatalf("%s 不应判定为私网", host)
		}
	}
	if !isBlockedIPv6(nil) {
		t.Fatal("nil IP 应按保留处理")
	}
	// 空允许列表恒不命中。
	parsed, err := url.Parse("https://127.0.0.1:8080/v1")
	if err != nil {
		t.Fatal(err)
	}
	if upstreamOriginAllowlisted(parsed, upstreamURLSecurity{}) {
		t.Fatal("空允许列表不应命中")
	}
	// 私网整体拒绝（环境放行时跳过）。
	if !w13gPrivateUpstreamAllowed() {
		if err := assertSafeUpstreamBaseURL("http://192.168.1.1/v1"); err == nil {
			t.Fatal("私网 base_url 应拒绝")
		}
	}
	// canonicalizeJSONValue 对不可序列化值原样返回，对 NaN 走 JSON 失败臂。
	if canonicalizeJSONValue(math.NaN()) == nil {
		t.Fatal("NaN 应回落原值")
	}
	// credentialsDeepEqual：nil 与空记录视为相等。
	if !credentialsDeepEqual(nil, Credentials{}) {
		t.Fatal("nil 与空记录应相等")
	}
	if credentialsDeepEqual(Credentials{"a": 1}, Credentials{"a": 2}) {
		t.Fatal("不同记录不应相等")
	}
}

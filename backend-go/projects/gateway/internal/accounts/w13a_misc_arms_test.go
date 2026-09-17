package accounts

// w13a write.go / credentials_normalize.go / export.go / import_source.go
// 纯函数与存储臂补齐：模型映射体解析、凭据来源、标签/支持模型归一、API Key
// 池解析与权重、OAuth/Google OAuth 归一、导出凭据白名单与状态投影、来源
// 适配器公共臂。

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestW13ANormalizeModelMappingBodyArms(t *testing.T) {
	valid := map[string]any{
		"sourceModel": " m ", "sourceEndpointFamily": "chat_completions",
		"upstreamModel": "u", "upstreamEndpointFamily": "responses", "enabled": true,
	}
	if mapping, ok := normalizeModelMappingBody(valid); !ok || mapping.SourceModel != "m" || *mapping.Enabled != true {
		t.Fatalf("合法映射应通过：%+v", mapping)
	}
	for name, mutate := range map[string]func(map[string]any){
		"空 sourceModel":   func(m map[string]any) { m["sourceModel"] = " " },
		"空 upstream":      func(m map[string]any) { m["upstreamModel"] = "" },
		"空 source 族":      func(m map[string]any) { m["sourceEndpointFamily"] = "" },
		"空 upstream 族":    func(m map[string]any) { m["upstreamEndpointFamily"] = "" },
		"非法 source 族":     func(m map[string]any) { m["sourceEndpointFamily"] = "bogus" },
		"非法 upstream 族":   func(m map[string]any) { m["upstreamEndpointFamily"] = "stream_generate_content" },
		"enabled 非布尔":     func(m map[string]any) { m["enabled"] = 3 },
		"未知键":             func(m map[string]any) { m["bogus"] = 1 },
	} {
		caseBody := map[string]any{}
		for key, value := range valid {
			caseBody[key] = value
		}
		mutate(caseBody)
		if _, ok := normalizeModelMappingBody(caseBody); ok {
			t.Fatalf("%s 应拒绝", name)
		}
	}
}

func TestW13ARequiredCredentialSourceAndTags(t *testing.T) {
	// requiredAccountCredentialSource 全分支。
	if _, err := requiredAccountCredentialSource("oauth", Credentials{}); err == nil {
		t.Fatal("空 OAuth 应拒绝")
	}
	if got, err := requiredAccountCredentialSource("oauth", Credentials{"access_token": " t "}); err != nil || got != "t" {
		t.Fatalf("OAuth token 应去空白：%q %v", got, err)
	}
	if _, err := requiredAccountCredentialSource("api_key", Credentials{}); err == nil {
		t.Fatal("空 API Key 应拒绝")
	}
	if got, err := requiredAccountCredentialSource("api_key", Credentials{"api_keys": []any{"", "sk-b"}}); err != nil || got != "sk-b" {
		t.Fatalf("池应取首个非空 Key：%q %v", got, err)
	}
	if _, err := requiredAccountCredentialSource("google_oauth", Credentials{}); err == nil {
		t.Fatal("空 Google OAuth 应拒绝")
	}
	if got, err := requiredAccountCredentialSource("bogus", Credentials{"refresh_token": "r"}); err != nil || got != "r" {
		t.Fatalf("默认分支应回退多键：%q %v", got, err)
	}
	if _, err := requiredAccountCredentialSource("bogus", Credentials{}); err == nil {
		t.Fatal("默认分支空凭据应拒绝")
	}

	// normalizeAccountTagNamesInput：nil / 非数组 / 非字符串项 / 空白跳过 / 去重 / 超量。
	if got, err := normalizeAccountTagNamesInput(nil); err != nil || got != nil {
		t.Fatalf("nil 应透传：%v %v", got, err)
	}
	if _, err := normalizeAccountTagNamesInput("x"); err == nil {
		t.Fatal("非数组应拒绝")
	}
	if _, err := normalizeAccountTagNamesInput([]any{3}); err == nil {
		t.Fatal("非字符串项应拒绝")
	}
	if got, err := normalizeAccountTagNamesInput([]any{" ", " a b ", "a b"}); err != nil || len(got) != 1 || got[0] != "a b" {
		t.Fatalf("空白应跳过且合并空白去重：%v %v", got, err)
	}
	many := []any{}
	for i := 0; i < maxTagsPerAccount+1; i++ {
		many = append(many, "标签" + string(rune('A'+i)))
	}
	if _, err := normalizeAccountTagNamesInput(many); err == nil {
		t.Fatal("超量标签应拒绝")
	}

	// normalizeSupportedModelsInput：非数组 / 非字符串 / 空白与去重。
	if _, err := normalizeSupportedModelsInput("x"); err == nil {
		t.Fatal("非数组应拒绝")
	}
	if _, err := normalizeSupportedModelsInput([]any{3}); err == nil {
		t.Fatal("非字符串应拒绝")
	}
	if got, err := normalizeSupportedModelsInput([]any{"", " m ", "m"}); err != nil || !reflect.DeepEqual(got, []string{"m"}) {
		t.Fatalf("空白应丢弃且去重：%v %v", got, err)
	}
	if err := assertSupportedModelsRequired([]string{" "}); err == nil {
		t.Fatal("全空白应拒绝")
	}
}

func TestW13ACredentialsNormalizeArms(t *testing.T) {
	defaults := EndpointModeDefaultContext{ProviderCode: "gpt", AccountType: "api_key"}

	// accountCredentialAllowedKeys。
	if _, err := accountCredentialAllowedKeys("bogus"); err == nil {
		t.Fatal("未知类型应拒绝")
	}
	if keys, err := accountCredentialAllowedKeys("oauth"); err != nil || keys["refresh_token"] != true {
		t.Fatalf("OAuth 键集应包含 refresh_token：%v %v", keys, err)
	}
	// stripDeprecatedAccountCredentialKeys。
	input := map[string]any{"api_key": "sk", "deprecated_field": 1}
	if stripped := stripDeprecatedAccountCredentialKeys(input); len(stripped) != len(input) {
		t.Fatalf("无废弃键应原样返回：%v", stripped)
	}
	// normalizeAPIKeyCredentialList：池优先、空池回退 api_key、超量、去重。
	if _, err := normalizeAPIKeyCredentialList(map[string]any{"api_key": ""}); err == nil {
		t.Fatal("空 Key 应拒绝")
	}
	if got, err := normalizeAPIKeyCredentialList(map[string]any{
		"api_key": "sk-a", "api_keys": []any{" sk-b ", "sk-b", "sk-c"},
	}); err != nil || !reflect.DeepEqual(got, []string{"sk-b", "sk-c"}) {
		t.Fatalf("池应优先并去重：%v %v", got, err)
	}
	big := []any{}
	for i := 0; i < accountAPIKeyListMaxItems+1; i++ {
		big = append(big, "sk-"+string(rune('A'+i)))
	}
	if _, err := normalizeAPIKeyCredentialList(map[string]any{"api_keys": big}); err == nil {
		t.Fatal("超量 Key 应拒绝")
	}
	// 策略与权重。
	if normalizeAPIKeyStrategy("bogus") != "failover" || normalizeAPIKeyStrategy("round_robin") != "round_robin" {
		t.Fatal("策略归一不一致")
	}
	if weights, err := normalizeAPIKeyWeights([]any{float64(2), nil}, 3); err != nil ||
		len(weights) != 3 || weights[0] != float64(2) || weights[1] != float64(1) || weights[2] != float64(1) {
		t.Fatalf("缺省权重应为 1：%v %v", weights, err)
	}
	if _, err := normalizeAPIKeyWeights([]any{float64(0)}, 1); err == nil {
		t.Fatal("非法权重应拒绝")
	}

	// OAuth 归一：缺 refresh/access 拒绝。
	if _, err := NormalizeAccountCredentialsForWrite("oauth", Credentials{}, &defaults); err == nil {
		t.Fatal("空 OAuth 凭据应拒绝")
	}
	// google_oauth：缺 refresh 拒绝。
	if _, err := NormalizeAccountCredentialsForWrite("google_oauth", Credentials{}, &defaults); err == nil {
		t.Fatal("空 Google OAuth 凭据应拒绝")
	}
	// 未知类型拒绝。
	if _, err := NormalizeAccountCredentialsForWrite("bogus", Credentials{}, &defaults); err == nil {
		t.Fatal("未知类型应拒绝")
	}
	// api_key 缺 base URL。
	if _, err := NormalizeAccountCredentialsForWrite("api_key", Credentials{"api_key": "sk"}, &defaults); err == nil {
		t.Fatal("缺 Base URL 应拒绝")
	}
}

func TestW13AExportHelpers(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-ex1", adminID, "w13a-ex1", "active")
	env.seedAccount(t, "acc-w13a-ex2", adminID, "w13a-ex2", "pending_test")
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, username, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w13a-ex', ?, 'w13a-ex-proxy', 'socks5', 'h', 1080, 'user', 1, 'unknown', ?, ?)`, adminID, now, now)
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w13a-ex' WHERE id = 'acc-w13a-ex1'`)
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	result, err := env.store.ExportAccounts(context.Background(), ExportOptions{
		AccountIDs: []string{" acc-w13a-ex1 ", "", "acc-w13a-ex1", "acc-w13a-ex2"},
	}, scope)
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	if len(result.Document.Accounts) != 2 {
		t.Fatalf("导出账户数不一致：%d", len(result.Document.Accounts))
	}
	if len(result.Document.Proxies) != 1 || result.Document.Proxies[0].Name != "w13a-ex-proxy" {
		t.Fatalf("代理应导出：%+v", result.Document.Proxies)
	}
	// 状态投影。
	if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = 'acc-w13a-ex1'`); got != "active" {
		t.Fatal("种子状态应 active")
	}
	if normalizeExportAccountIDs([]string{"", " a ", "a"}) == nil {
		t.Fatal("归一后不应为 nil")
	}
	if normalizeExportAccountIDs([]string{"", " "}) != nil {
		t.Fatal("全空白应为 nil")
	}
	// exportCredentials 白名单。
	credentials := exportCredentials("api_key", Credentials{"api_key": "sk", "bogus": 1, "base_url": nil})
	if credentials["api_key"] != "sk" {
		t.Fatalf("api_key 白名单应保留键：%v", credentials)
	}
	if _, exists := credentials["bogus"]; exists {
		t.Fatal("白名单外键不应导出")
	}
	other := exportCredentials("bogus", Credentials{"z": 1, "a": 2})
	if len(other) != 2 {
		t.Fatalf("未知类型应保留全部键：%v", other)
	}
	// strPtrOrNil。
	if strPtrOrNil(" x ") == nil || *strPtrOrNil(" x ") != "x" {
		t.Fatal("非空应去空白返回指针")
	}
	if strPtrOrNil("  ") != nil {
		t.Fatal("空白应返回 nil")
	}
}

func TestW13AImportSourceHelpers(t *testing.T) {
	// sourceLabel。
	if sourceLabel(importSourceNewAPI) != "NewAPI" || sourceLabel(importSourceOneAPI) != "One-API" {
		t.Fatal("来源标签不一致")
	}
	// parseSourceJSON：字符串非法 JSON 拒绝、对象透传。
	if _, err := parseSourceJSON("{bad", "来源"); err == nil {
		t.Fatal("非法 JSON 应拒绝")
	}
	parsed, err := parseSourceJSON(map[string]any{"a": 1}, "来源")
	if err != nil || parsed == nil {
		t.Fatalf("对象应透传：%v %v", parsed, err)
	}
	// adaptImportSource：native 直通；未知模式走 CLIProxyAPI 适配并给出空提示。
	data := map[string]any{"accounts": []any{}}
	out, summary := adaptImportSource(data, importSourceNative)
	outMap, _ := out.(map[string]any)
	if outMap == nil || len(outMap) != len(data) || summary.Mode != importSourceNative {
		t.Fatalf("native 应直通：%v %v", out, summary)
	}
	_, summary = adaptImportSource(map[string]any{}, "bogus-mode")
	if len(summary.Messages) == 0 {
		t.Fatalf("空来源应有提示：%+v", summary)
	}
	if strings.TrimSpace(strings.Join(summary.Messages, "|")) == "" {
		t.Fatal("空来源应有提示")
	}
}

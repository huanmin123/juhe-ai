package accounts

// OAuth 管理式建户连接维度缺省派生（供应商与上游协议面重构设计 §2/§2.2/§8）：
//   - DefaultConnectionsForOAuthVendor 表驱动：四类 OAuth 组合各断言派生的
//     preset / registry 默认 base URL / endpoint modes（§2.2 收窄词表）与
//     健康检查连接维度缺省；未注册组合显式拒绝；
//   - OAuth 入径落库（TestB1CreateWritesConnectionRows 范式按 OAuth 入径）：
//     派生缺省经 Store.Create 全流程落 account_connections /
//     account_endpoint_modes / 健康检查三字段，fresh 契约形态完整。

import (
	"context"
	"strings"
	"testing"
)

// TestDefaultConnectionsForOAuthVendorTable 是四类 OAuth 的派生规则表：
// gpt.oauth / xai.oauth 按 §2.2 收窄为 {responses_json, responses_sse}；
// anthropic.oauth 允许完整 Anthropic 词表；gemini.google_oauth 三种 oauth_type
// 同用 Gemini native 词表（不按 oauth_type 再收窄）。baseUrl 取 §1.1 注册表
// default_base_url；健康检查缺省形态取连接 JSON（非流式）形态。
func TestDefaultConnectionsForOAuthVendorTable(t *testing.T) {
	cases := []struct {
		name          string
		provider      string
		accountType   string
		credentials   Credentials
		wantPreset    string
		wantBaseURL   string
		wantModes     []string
		wantSurface   string
		wantHealthMod string
	}{
		{
			name: "gpt oauth", provider: "gpt", accountType: "oauth",
			credentials: Credentials{"access_token": "at", "base_url": "https://api.openai.com/v1"},
			wantPreset:  "gpt_openai_v1", wantBaseURL: "https://api.openai.com/v1",
			wantModes: []string{"responses_json", "responses_sse"}, wantSurface: "openai_v1",
			wantHealthMod: "responses_json",
		},
		{
			name: "xai oauth", provider: "xai", accountType: "oauth",
			credentials: Credentials{"access_token": "at", "base_url": "https://api.x.ai/v1"},
			wantPreset:  "xai_openai_v1", wantBaseURL: "https://api.x.ai/v1",
			wantModes: []string{"responses_json", "responses_sse"}, wantSurface: "openai_v1",
			wantHealthMod: "responses_json",
		},
		{
			name: "anthropic oauth", provider: "anthropic", accountType: "oauth",
			credentials: Credentials{"access_token": "at", "base_url": "https://api.anthropic.com/v1"},
			wantPreset:  "anthropic_v1", wantBaseURL: "https://api.anthropic.com/v1",
			wantModes: []string{"message_token_counting", "messages_json", "messages_sse"}, wantSurface: "anthropic_v1",
			wantHealthMod: "messages_json",
		},
		{
			name: "gemini google_oauth code_assist", provider: "gemini", accountType: "google_oauth",
			credentials: Credentials{"access_token": "at", "oauth_type": "code_assist", "base_url": "https://generativelanguage.googleapis.com"},
			wantPreset:  "gemini_native_v1beta", wantBaseURL: "https://generativelanguage.googleapis.com",
			wantModes: []string{"count_tokens", "embed_content", "generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"}, wantSurface: "gemini_v1beta",
			wantHealthMod: "generate_content_json",
		},
		{
			name: "gemini google_oauth google_one", provider: "gemini", accountType: "google_oauth",
			credentials: Credentials{"access_token": "at", "oauth_type": "google_one", "base_url": "https://generativelanguage.googleapis.com"},
			wantPreset:  "gemini_native_v1beta", wantBaseURL: "https://generativelanguage.googleapis.com",
			wantModes: []string{"count_tokens", "embed_content", "generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"}, wantSurface: "gemini_v1beta",
			wantHealthMod: "generate_content_json",
		},
		{
			name: "gemini google_oauth ai_studio", provider: "gemini", accountType: "google_oauth",
			credentials: Credentials{"access_token": "at", "oauth_type": "ai_studio", "base_url": "https://generativelanguage.googleapis.com"},
			wantPreset:  "gemini_native_v1beta", wantBaseURL: "https://generativelanguage.googleapis.com",
			wantModes: []string{"count_tokens", "embed_content", "generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"}, wantSurface: "gemini_v1beta",
			wantHealthMod: "generate_content_json",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			defaults, err := DefaultConnectionsForOAuthVendor(testCase.provider, testCase.accountType, testCase.credentials)
			if err != nil {
				t.Fatalf("派生: %v", err)
			}
			if len(defaults.Connections) != 1 {
				t.Fatalf("连接行数: %d", len(defaults.Connections))
			}
			connection := defaults.Connections[0]
			if connection.ConnectionPresetCode != testCase.wantPreset {
				t.Fatalf("preset: %q", connection.ConnectionPresetCode)
			}
			if connection.BaseURL != testCase.wantBaseURL {
				t.Fatalf("base URL: %q", connection.BaseURL)
			}
			if !connection.Enabled {
				t.Fatalf("派生连接必须 enabled")
			}
			if strings.Join(connection.EndpointModes, ",") != strings.Join(testCase.wantModes, ",") {
				t.Fatalf("endpoint modes: %v（want %v）", connection.EndpointModes, testCase.wantModes)
			}
			if defaults.HealthCheckConnectionPresetCode != testCase.wantPreset {
				t.Fatalf("健康检查 preset: %q", defaults.HealthCheckConnectionPresetCode)
			}
			if defaults.HealthCheckEndpointMode != testCase.wantHealthMod {
				t.Fatalf("健康检查缺省形态: %q", defaults.HealthCheckEndpointMode)
			}
			// 派生连接与 §2 组合表的 registry surface 一致（写面回显校验同源）。
			surface, ok := connectorComboSurface(testCase.provider, testCase.accountType,
				accountOAuthTypeForRegistry(testCase.accountType, "", testCase.credentials), testCase.wantPreset, "chat")
			if !ok || surface != testCase.wantSurface {
				t.Fatalf("registry surface: %q ok=%v", surface, ok)
			}
		})
	}
}

// TestDefaultConnectionsForOAuthVendorRejectsUnsupported：非 OAuth 账户类型、
// 未知供应商组合与缺 oauth_type 的 google_oauth 显式拒绝。
func TestDefaultConnectionsForOAuthVendorRejectsUnsupported(t *testing.T) {
	cases := []struct {
		name        string
		provider    string
		accountType string
		credentials Credentials
	}{
		{"api_key 账户不是 OAuth 建户入径", "gpt", "api_key", Credentials{"api_key": "sk-x"}},
		{"generic 供应商无 OAuth 组合", "openai", "oauth", Credentials{"access_token": "at"}},
		{"google_oauth 缺 oauth_type", "gemini", "google_oauth", Credentials{"access_token": "at"}},
		{"google_oauth 未知 oauth_type", "gemini", "google_oauth", Credentials{"access_token": "at", "oauth_type": "bogus"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := DefaultConnectionsForOAuthVendor(testCase.provider, testCase.accountType, testCase.credentials); err == nil {
				t.Fatalf("必须拒绝")
			}
		})
	}
}

// TestOAuthManagedDefaultsCreateWritesConnectionRows 把四类 OAuth 的派生缺省
// 按 CreateOAuthAccount 的组装形态送入 Store.Create（OAuth 入径），断言连接行、
// endpoint mode 行与健康检查三字段落库形态（B1 完成判定范式的 OAuth 版）。
func TestOAuthManagedDefaultsCreateWritesConnectionRows(t *testing.T) {
	cases := []struct {
		name        string
		provider    string
		accountType string
		credentials Credentials
		model       string
		modelModes  string
		wantPreset  string
		wantBaseURL string
		wantSurface string
		wantModes   []string
	}{
		{
			name: "gpt oauth", provider: "gpt", accountType: "oauth",
			credentials: Credentials{"access_token": "at-gpt", "account_id": "acc-openai-1", "base_url": "https://api.openai.com/v1"},
			model:       "gpt-4o-mini", modelModes: `["chat_json","chat_sse","responses_json","responses_sse"]`,
			wantPreset: "gpt_openai_v1", wantBaseURL: "https://api.openai.com/v1", wantSurface: "openai_v1",
			wantModes: []string{"responses_json", "responses_sse"},
		},
		{
			name: "xai oauth", provider: "xai", accountType: "oauth",
			credentials: Credentials{"access_token": "at-xai", "base_url": "https://api.x.ai/v1"},
			model:       "grok-4", modelModes: `["chat_json","chat_sse","responses_json","responses_sse"]`,
			wantPreset: "xai_openai_v1", wantBaseURL: "https://api.x.ai/v1", wantSurface: "openai_v1",
			wantModes: []string{"responses_json", "responses_sse"},
		},
		{
			name: "anthropic oauth", provider: "anthropic", accountType: "oauth",
			credentials: Credentials{"access_token": "at-anthropic", "base_url": "https://api.anthropic.com/v1"},
			model:       "claude-sonnet-4-5", modelModes: `["messages_json","messages_sse","message_token_counting"]`,
			wantPreset: "anthropic_v1", wantBaseURL: "https://api.anthropic.com/v1", wantSurface: "anthropic_v1",
			wantModes: []string{"message_token_counting", "messages_json", "messages_sse"},
		},
		{
			name: "gemini google_oauth", provider: "gemini", accountType: "google_oauth",
			credentials: Credentials{"access_token": "at-gemini", "oauth_type": "code_assist", "base_url": "https://generativelanguage.googleapis.com"},
			model:       "gemini-2.5-pro", modelModes: `["generate_content_json","generate_content_sse"]`,
			wantPreset: "gemini_native_v1beta", wantBaseURL: "https://generativelanguage.googleapis.com", wantSurface: "gemini_v1beta",
			wantModes: []string{"count_tokens", "embed_content", "generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newTestEnv(t)
			ownerID := env.login(t, "root", "root-pass", "super_admin")
			now := "2026-01-01T00:00:00.000Z"
			// gpt vendor 已由 env 夹具预置（INSERT OR IGNORE 幂等）；其余厂商按
			// §2.1 真实厂商规则补行。创建链要求 owner 有启用的默认分组。
			env.exec(t, `INSERT OR IGNORE INTO vendors (id, code, name, enabled, created_at, updated_at)
				VALUES (?, ?, ?, 1, ?, ?)`, "ven-"+testCase.provider, testCase.provider, testCase.provider, now, now)
			env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
				VALUES (?, ?, '默认分组', 1, 1, 'personal', ?, ?)`,
				"grp-default-"+ownerID, ownerID, testCase.provider, now, now)
			env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
				supported_endpoint_modes_json, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'builtin', 'active', ?, ?, ?)`,
				"mod-"+testCase.provider, testCase.provider, testCase.model, testCase.model, testCase.modelModes, now, now)

			defaults, err := DefaultConnectionsForOAuthVendor(testCase.provider, testCase.accountType, testCase.credentials)
			if err != nil {
				t.Fatalf("派生: %v", err)
			}
			healthModel := testCase.model
			healthMode := defaults.HealthCheckEndpointMode
			result, err := env.store.Create(context.Background(), CreateInput{
				ProviderCode:                    testCase.provider,
				ProviderProtocolProfileID:       "prof-" + testCase.provider,
				Name:                            "oauth-defaults-" + testCase.provider,
				AccountType:                     testCase.accountType,
				Credentials:                     testCase.credentials,
				SupportedModels:                 []string{testCase.model},
				Connections:                     defaults.Connections,
				HealthCheckConnectionPresetCode: &defaults.HealthCheckConnectionPresetCode,
				HealthCheckEndpointMode:         &healthMode,
				HealthCheckModel:                &healthModel,
				Status:                          CreationStatus{Status: "active", SkipInitialHealthCheck: true, Schedulable: true},
			}, AccessScope{ViewerID: ownerID, IsAdmin: true})
			if err != nil {
				t.Fatalf("OAuth 入径创建: %v", err)
			}

			// 连接行：单行、preset 自然键、registry surface、注册默认 base URL、enabled。
			connectionID := env.queryCell(t, `SELECT id FROM account_connections WHERE account_id = ?`, result.ID)
			if !strings.HasPrefix(connectionID, "conn-") {
				t.Fatalf("connection id 前缀：%q", connectionID)
			}
			if got := env.queryCell(t, `SELECT connection_preset_code FROM account_connections WHERE id = ?`, connectionID); got != testCase.wantPreset {
				t.Fatalf("连接 preset：%q", got)
			}
			if got := env.queryCell(t, `SELECT protocol_surface_code FROM account_connections WHERE id = ?`, connectionID); got != testCase.wantSurface {
				t.Fatalf("连接 surface：%q", got)
			}
			if got := env.queryCell(t, `SELECT base_url FROM account_connections WHERE id = ?`, connectionID); got != testCase.wantBaseURL {
				t.Fatalf("连接 base_url：%q", got)
			}
			if got := env.count(t, `SELECT COUNT(*) FROM account_connections WHERE account_id = ? AND enabled = 1`, result.ID); got != 1 {
				t.Fatalf("enabled 连接行数：%d", got)
			}
			// endpoint mode 行：派生词表全集逐行落库。
			if got := env.count(t, `SELECT COUNT(*) FROM account_endpoint_modes WHERE connection_id = ?`, connectionID); got != len(testCase.wantModes) {
				t.Fatalf("endpoint mode 行数：%d（want %d）", got, len(testCase.wantModes))
			}
			for _, mode := range testCase.wantModes {
				if got := env.count(t, `SELECT COUNT(*) FROM account_endpoint_modes WHERE connection_id = ? AND endpoint_mode = ?`, connectionID, mode); got != 1 {
					t.Fatalf("endpoint mode %s 行缺失", mode)
				}
			}
			// 健康检查三字段事务闭环：指向派生连接 + 缺省 JSON 检查形态。
			var gotHealthConnection, gotHealthMode, gotHealthModel string
			if err := env.db.QueryRow(`SELECT health_check_connection_id, health_check_endpoint_mode, health_check_model
				FROM accounts WHERE id = ?`, result.ID).Scan(&gotHealthConnection, &gotHealthMode, &gotHealthModel); err != nil {
				t.Fatal(err)
			}
			if gotHealthConnection != connectionID || gotHealthMode != defaults.HealthCheckEndpointMode || gotHealthModel != testCase.model {
				t.Fatalf("健康检查三字段：%q %q %q", gotHealthConnection, gotHealthMode, gotHealthModel)
			}
		})
	}
}

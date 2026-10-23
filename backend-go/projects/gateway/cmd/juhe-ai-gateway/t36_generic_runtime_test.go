package main

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// T36：generic_http 账户 vendor 为空时，视频 adapter 按冻结的协议面回退；
// real vendor 仍优先；未知协议保持能力缺失（nil）。
func TestChainVideoAdapterGenericProtocolFallback(t *testing.T) {
	cases := []struct {
		name     string
		vendor   string
		protocol string
		version  string
		want     string
	}{
		{name: "generic openai", vendor: "", protocol: "openai", version: "v1", want: "openai"},
		{name: "generic gemini", vendor: "", protocol: "gemini", version: "v1beta", want: "gemini"},
		{name: "real vendor wins", vendor: "glm", protocol: "openai", version: "v1", want: "glm"},
		{name: "unknown protocol", vendor: "", protocol: "anthropic", version: "v1", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := chainVideoAdapterForAccount(gatewaydispatch.AccountCandidate{
				VendorCode:      tc.vendor,
				ProtocolCode:    tc.protocol,
				ProtocolVersion: tc.version,
			})
			if tc.want == "" {
				if adapter != nil {
					t.Fatalf("adapter = %v, want nil", adapter)
				}
				return
			}
			if adapter == nil || adapter.Provider() != tc.want {
				t.Fatalf("adapter = %v, want provider %q", adapter, tc.want)
			}
		})
	}
}

// T36：generic_http API-key 账户保持多 Key 池隔离；空 vendor 不得作为通配符。
func TestChainAccountAPIKeyPoolGenericHTTP(t *testing.T) {
	credentials := map[string]any{"api_keys": []any{"a", "b"}}
	if !chainAccountAPIKeyPoolIsolationEnabled("", "generic_http", "openai", "v1", "api_key", credentials) {
		t.Fatal("generic_http 多 Key 应启用池隔离")
	}
	if chainAccountAPIKeyPoolIsolationEnabled("", "", "openai", "v1", "api_key", credentials) {
		t.Fatal("空 driver + 空 vendor 不得启用池隔离")
	}
	if chainAccountAPIKeyPoolIsolationEnabled("", "generic_http", "openai", "v1", "oauth", credentials) {
		t.Fatal("oauth 账户不得启用池隔离")
	}
}

// T36：account_supported_models → models 的真实行保留完整元数据，vendor 为空
// 不伪造 custom，upstream_model 可用于计价身份解析。
func TestChainCatalogListAccountSupportedModels(t *testing.T) {
	dbPath := t.TempDir() + "/t36-account-models.sqlite3"
	db := w2bOpenPlainSQLiteAt(t, dbPath)
	seedChainBusinessSchema(t, db)
	if _, err := db.Exec(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
			supported_endpoint_modes_json, context_window_tokens, created_at, updated_at)
		VALUES ('m-t36', NULL, 'custom-public', 'custom-upstream', 'personal', 'sys-t36', 'active',
			'["chat_sse"]', 128000, '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES ('acc-t36', 'custom-public', 'm-t36', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	source := &chainCatalogSource{db: db, postgres: false}
	items, err := source.ListAccountSupportedModelCatalog(context.Background(), "acc-t36", "sys-t36")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want 1", items)
	}
	item := items[0]
	if item.ID == nil || *item.ID != "m-t36" {
		t.Fatalf("ID = %v, want m-t36", item.ID)
	}
	if item.ProviderCode != "" {
		t.Fatalf("ProviderCode = %q, want empty (no fake custom)", item.ProviderCode)
	}
	if item.UpstreamModel != "custom-upstream" {
		t.Fatalf("UpstreamModel = %q, want custom-upstream", item.UpstreamModel)
	}
	if item.ContextWindowTokens == nil || *item.ContextWindowTokens != 128000 {
		t.Fatalf("ContextWindowTokens = %v, want 128000", item.ContextWindowTokens)
	}
	if len(item.SupportedAPIProtocols) != 1 || item.SupportedAPIProtocols[0] != "chat_completions" {
		t.Fatalf("SupportedAPIProtocols = %v, want [chat_completions]", item.SupportedAPIProtocols)
	}
}

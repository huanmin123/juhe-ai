package main

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// T36 / 2026-11-05 协议面退役：空 vendor 的 generic openai/gemini 账户不再
// 按冻结协议面回退；只有真实 vendor 仍生成 adapter，未知协议保持 nil。
func TestChainVideoAdapterRetiredGenericVendorRejected(t *testing.T) {
	cases := []struct {
		name     string
		vendor   string
		protocol string
		version  string
		want     string
	}{
		{name: "retired generic openai", vendor: "", protocol: "openai", version: "v1", want: ""},
		{name: "retired generic gemini", vendor: "", protocol: "gemini", version: "v1beta", want: ""},
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

// T36 / 2026-11-05 协议面退役：generic_http 与空 vendor 不再是可承载账户，
// 即使多 Key 也不得启用池隔离；空 vendor 不得作为通配符。
func TestChainAccountAPIKeyPoolRetiredGenericVendorRejected(t *testing.T) {
	credentials := map[string]any{"api_keys": []any{"a", "b"}}
	if chainAccountAPIKeyPoolIsolationEnabled("", "generic_http", "openai", "v1", "api_key", credentials) {
		t.Fatal("generic_http 已退役，不得启用池隔离")
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
	source := &chainCatalogSource{db: db}
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

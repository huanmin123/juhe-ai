package accounts

import (
	"context"
	"testing"
)

// T33：generic_http（vendor 为空）人工测试目录来自 account_supported_models
// 的真实 models 行，保留 public_model/modes 元数据；不再返回空候选。
func TestT33GenericManualTestCatalogSource(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-t33-generic", adminID, "t33-generic", "active")
	env.exec(t, `UPDATE accounts SET vendor_code = NULL, connection_driver_code = 'generic_http' WHERE id = 'acc-t33-generic'`)
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
		supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl-t33-generic', NULL, 'generic-manual', 'generic-manual', 'personal', ?, 'active',
			'["chat_sse"]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, adminID)
	env.exec(t, `INSERT INTO account_supported_models (account_id, model_id, created_at)
		VALUES ('acc-t33-generic', 'mdl-t33-generic', '2026-01-01T00:00:00Z')`)

	rows, err := env.store.ListAccountSupportedTestCatalogOptions(context.Background(), "acc-t33-generic", ManualTestOptionsQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Model != "generic-manual" {
		t.Fatalf("rows = %+v, want generic-manual", rows)
	}
	if len(rows[0].SupportedAPIProtocols) != 1 || rows[0].SupportedAPIProtocols[0] != "chat_completions" {
		t.Fatalf("protocols = %v, want [chat_completions]", rows[0].SupportedAPIProtocols)
	}

	// 完整人工测试面：候选列表与测试请求校验都走同一账户目录。
	ctx := &ManualTestContext{
		ID:                     "acc-t33-generic",
		FactAccountID:          "acc-t33-generic",
		OwnerSystemAccountID:   adminID,
		ProviderCode:           "",
		ProtocolCode:           "openai",
		ProtocolVersion:        "v1",
		Type:                   "api_key",
		SupportedEndpointModes: []string{"chat_sse"},
	}
	options, total, err := env.store.AccountManualTestOptionsWithTotal(context.Background(), ctx, ManualTestOptionsQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total < 1 || len(options) < 1 || options[0].ID != "generic-manual" {
		t.Fatalf("options = %+v total=%d, want generic-manual", options, total)
	}
	capabilities, err := env.store.AccountManualTestModelCapabilities(context.Background(), ctx, "generic-manual")
	if err != nil || capabilities.ID != "generic-manual" {
		t.Fatalf("capabilities = %+v err=%v", capabilities, err)
	}
}

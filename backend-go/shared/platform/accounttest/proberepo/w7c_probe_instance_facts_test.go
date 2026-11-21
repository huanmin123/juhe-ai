package proberepo

// 授权实例读面（2026-11-21）：实例行不承载资源事实——凭据列是 "{}" 占位、
// 无自身连接行，身份/健康检查/Key 运行态都归来源账户。LoadAccountForTest
// 必须按来源账户投影，否则 jobs account-connectivity-health 会在解密占位
// 凭据时把实例按候选缺失跳过。非实例行保持原有投影。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

func TestW7CLoadAccountForTestAuthorizedInstanceSourceFacts(t *testing.T) {
	h := openTestDBPostgres(t)
	ctx := context.Background()

	// 实例行：凭据 "{}" 占位、health/身份列 NULL、无自身连接行（生产实例同形）。
	h.exec(t, `INSERT INTO juhe_business.accounts
		(id, system_account_id, name, type, status, credentials_encrypted, connection_driver_code,
		 protocol_code, protocol_version,
		 authorization_instance_authorization_id, authorization_instance_source_account_id,
		 authorization_instance_owner_system_account_id, created_at, updated_at)
		VALUES ('inst-health', 'sys-grantee', '实例', 'api_key', 'active', '{}', 'gpt', 'openai', 'v1',
		 'authz-health', 'src-health', 'sys-owner', $1, $1)`, nowMillisText())
	h.exec(t, `INSERT INTO juhe_business.accounts
		(id, system_account_id, name, type, status, credentials_encrypted, vendor_code, connection_driver_code,
		 protocol_code, protocol_version, health_check_model, health_check_endpoint_mode, created_at, updated_at)
		VALUES ('src-health', 'sys-owner', '来源', 'api_key', 'active', $1, 'gpt', 'gpt', 'openai', 'v1',
		 'gpt-health-model', 'embeddings_json', $2, $2)`,
		h.sealCredentials(t, map[string]any{"api_keys": []any{"sk-src-one", "sk-src-two"}}), nowMillisText())
	h.seedModel(t, "src-health", "gpt-health-model")
	h.seedProbeConnection(t, "src-health", "gpt_openai_v1", "https://source.example.com/v1", "embeddings_json")
	h.exec(t, `INSERT INTO juhe_business.resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('authz-health', 'account', 'src-health', 'sys-owner', 'sys-grantee', 'active', 'sys-owner', $1, $1)`, nowMillisText())
	// 接收方自有分组 + 实例绑定（LoadAccountForTest 的 BoundGroupID 与分组
	// 访问身份都取实例行 system_account_id）。
	h.exec(t, `INSERT INTO juhe_business.groups (id, system_account_id, name, enabled, created_at, updated_at)
		VALUES ('group-health', 'sys-grantee', 'g', 1, $1, $1)`, nowMillisText())
	h.exec(t, `INSERT INTO juhe_business.group_accounts (group_id, system_account_id, account_id, enabled, created_at, updated_at)
		VALUES ('group-health', 'sys-grantee', 'inst-health', 1, $1, $1)`, nowMillisText())

	// Key 运行态只写在来源账户上（网关对授权实例按 CredentialSourceAccountID
	// 落库）；实例读面必须读来源，才能反映池不可用。
	fpOne := h.store.FingerprintAPIKey("sk-src-one")
	fpTwo := h.store.FingerprintAPIKey("sk-src-two")
	h.seedRuntimeState(t, "src-health", fpOne, 0, "temporary_unavailable", plusMillis(-1000))
	h.seedRuntimeState(t, "src-health", fpTwo, 1, "rate_limited", plusMillis(-1000))

	view, err := h.store.LoadAccountForTest(ctx, "inst-health")
	if err != nil || view == nil {
		t.Fatalf("实例视图不得因 \"{}\" 占位凭据返回空: view=%v err=%v", view, err)
	}
	if view.Type != "api_key" || view.ProviderCode != "gpt" {
		t.Fatalf("实例 Type/Provider 必须投影来源: type=%q provider=%q", view.Type, view.ProviderCode)
	}
	if view.HealthCheckModel != "gpt-health-model" || view.HealthCheckEndpointMode != "embeddings_json" {
		t.Fatalf("实例健康检查配置必须投影来源: model=%q mode=%q", view.HealthCheckModel, view.HealthCheckEndpointMode)
	}
	keys, _ := view.Credentials["api_keys"].([]any)
	if len(keys) != 2 || keys[0] != "sk-src-one" || keys[1] != "sk-src-two" {
		t.Fatalf("实例凭据必须取来源封套: %#v", view.Credentials)
	}
	if view.APIKeyRuntime[fpOne] != "temporary_unavailable" || view.APIKeyRuntime[fpTwo] != "rate_limited" {
		t.Fatalf("实例 Key 运行态必须按来源账户统计: %#v", view.APIKeyRuntime)
	}
	if len(view.SupportedModels) != 1 || view.SupportedModels[0] != "gpt-health-model" {
		t.Fatalf("实例支持模型必须取来源: %#v", view.SupportedModels)
	}
	if view.BoundGroupID != "group-health" {
		t.Fatalf("实例绑定分组 = %q", view.BoundGroupID)
	}
	if view.EffectiveAvailable || view.EffectiveAvailabilityStatus != "api_key_pool_unavailable" {
		t.Fatalf("来源 Key 池全部不可用必须阻断: available=%t status=%q",
			view.EffectiveAvailable, view.EffectiveAvailabilityStatus)
	}

	// 探针视图必须使用来源连接/凭据（runner 的后续 LoadAccountForGroup 路径）。
	probe, err := h.store.LoadProbeView(ctx, accountquality.ProbeRequest{
		AccountID: "inst-health", GroupID: "group-health", SystemAccountID: "sys-grantee", Full: true,
	})
	if err != nil || probe == nil {
		t.Fatalf("实例探针视图: view=%v err=%v", probe, err)
	}
	if probe.BaseURL != "https://source.example.com/v1" || probe.ConnectionPresetCode != "gpt_openai_v1" {
		t.Fatalf("实例探针连接必须取来源: base=%q preset=%q", probe.BaseURL, probe.ConnectionPresetCode)
	}
	if probe.SelectedAPIKey != "sk-src-one" || len(probe.APIKeyEntries) != 2 {
		t.Fatalf("实例探针凭据必须取来源: selected=%q entries=%+v", probe.SelectedAPIKey, probe.APIKeyEntries)
	}

	// 实例自身没有连接行。
	var ownConnections int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_connections WHERE account_id = 'inst-health'`).Scan(&ownConnections); err != nil {
		t.Fatal(err)
	}
	if ownConnections != 0 {
		t.Fatalf("实例连接行数 = %d", ownConnections)
	}

	// OAuth 来源：实例行 type/oauth_type 与来源不同，视图必须投影来源。
	h.exec(t, `INSERT INTO juhe_business.accounts
		(id, system_account_id, name, type, status, credentials_encrypted, connection_driver_code,
		 protocol_code, protocol_version, authorization_instance_authorization_id,
		 authorization_instance_source_account_id, created_at, updated_at)
		VALUES ('inst-oauth', 'sys-grantee', '实例OAuth', 'api_key', 'active', '{}', 'gpt', 'openai', 'v1',
		 'authz-oauth', 'src-oauth', $1, $1)`, nowMillisText())
	h.exec(t, `INSERT INTO juhe_business.accounts
		(id, system_account_id, name, type, status, credentials_encrypted, vendor_code, oauth_type, connection_driver_code,
		 protocol_code, protocol_version, created_at, updated_at)
		VALUES ('src-oauth', 'sys-owner', '来源OAuth', 'oauth', 'active', $1, 'gpt', 'openai_oauth', 'gpt',
		 'openai', 'v1', $2, $2)`,
		h.sealCredentials(t, map[string]any{"access_token": "tok-src"}), nowMillisText())
	oauthView, err := h.store.LoadAccountForTest(ctx, "inst-oauth")
	if err != nil || oauthView == nil {
		t.Fatalf("OAuth 实例视图: view=%v err=%v", oauthView, err)
	}
	if oauthView.Type != "oauth" || oauthView.OAuthType != "openai_oauth" || oauthView.ProviderCode != "gpt" {
		t.Fatalf("OAuth 实例身份列必须投影来源: type=%q oauth=%q provider=%q",
			oauthView.Type, oauthView.OAuthType, oauthView.ProviderCode)
	}
	if token, _ := oauthView.Credentials["access_token"].(string); token != "tok-src" {
		t.Fatalf("OAuth 实例凭据必须取来源: %#v", oauthView.Credentials)
	}

	// 非实例对照：自身凭据与（空）运行态读取不变。
	h.seedPoolAccount(t, "acc-plain")
	plain, err := h.store.LoadAccountForTest(ctx, "acc-plain")
	if err != nil || plain == nil {
		t.Fatalf("普通账户视图: view=%v err=%v", plain, err)
	}
	plainKeys, _ := plain.Credentials["api_keys"].([]any)
	if len(plainKeys) != 2 || plainKeys[0] != "sk-key-one" || plainKeys[1] != "sk-key-two" {
		t.Fatalf("普通账户凭据被错误改道: %#v", plain.Credentials)
	}
	if len(plain.APIKeyRuntime) != 0 || !plain.EffectiveAvailable {
		t.Fatalf("普通账户运行态/可用性被错误改道: runtime=%#v available=%t",
			plain.APIKeyRuntime, plain.EffectiveAvailable)
	}
}

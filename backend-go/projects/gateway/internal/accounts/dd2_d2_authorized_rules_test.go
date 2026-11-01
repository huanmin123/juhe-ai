package accounts

// DD2-D2 钉住测试：授权实例高级详情的 EffectiveErrorHandlingRules 必须与
// EffectiveQuotaRecoveryPolicy 同口径取 effectivePolicySource——stamped 实例
// 行 credentials_encrypted 恒 NULL，此前用 ownerCredentials 会让来源账户的
// 错误处理规则在授权实例读面不可见。DB 流程测试，复用 w13g 授权装配。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

func TestDD2D2AuthorizedInstanceRulesFromSource(t *testing.T) {
	env, authzStore, store := w13gTrafficAuthorizedEnv(t)
	ownerID := env.login(t, "dd2-d2-owner", "owner-pass", "user")
	memberID := env.login(t, "dd2-d2-member", "member-pass", "user")
	env.seedProviderAndDefaultGroup(t, ownerID)
	env.seedProviderAndDefaultGroup(t, memberID)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, memberID)

	// 来源账户带错误处理策略（一条规则 + delete 覆盖）。
	sourceID := "acc-dd2-d2-src"
	sealed, err := EncryptJSON(testSecret, Credentials{
		"api_key": "sk-dd2-d2",
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "来源限流", "priority": float64(1),
			"action": "rate_limited", "reset_strategy": "duration",
			"duration_hours": float64(2), "status_codes": []any{float64(429)},
		}},
		"error_handling_rule_overrides": []any{map[string]any{
			"system_rule_id": "system.upstream_insufficient_quota", "action": "delete",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES (?, ?, 'gpt', 'gpt', 'openai', 'v1', 'dd2-d2-src', 'api_key', 'active', ?, 'sk-dd2***', 'gpt-4o-mini', ?, ?)`,
		sourceID, ownerID, sealed, now, now)

	// team 授权 + grantee 侧实例行（w13gSeedTrafficInstance 同款装配，来源凭据
	// 已手工落行故不走 seedAccount）。
	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: sourceID,
		GranteeType: "team", GranteeID: "team-w13g-tm",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = ?`, memberID, sourceID)
	if runtimeID == "" {
		t.Fatal("runtime 授权行缺失")
	}
	instanceID := "acc-dd2-d2-inst"
	env.seedAuthorizationInstance(t, instanceID, memberID, runtimeID, sourceID)
	// active 授权 join 需要 owner stamp（resource_owner_system_account_id）。
	env.exec(t, `UPDATE accounts SET authorization_instance_owner_system_account_id = ?
		WHERE id = ?`, ownerID, instanceID)
	groupID := env.queryCell(t, `SELECT id FROM groups WHERE system_account_id = ? AND is_default = 1`, memberID)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at, account_authorization_id)
		VALUES (?, ?, ?, 1, ?, ?, ?)`, memberID, groupID, instanceID, now, now, runtimeID)

	detail, err := store.FindAdvancedDetail(context.Background(), instanceID, AccessScope{ViewerID: memberID})
	if err != nil || detail == nil {
		t.Fatalf("授权实例高级详情读取：%v %v", detail, err)
	}
	// delete 覆盖生效 → 无系统条目，仅剩来源账户规则（修复前 ownerCredentials
	// 恒空 → 只展示系统注册规则）。
	if len(detail.EffectiveErrorHandlingRules) != 1 {
		t.Fatalf("来源 delete 覆盖应抑制系统规则、仅剩来源账户规则：%+v", detail.EffectiveErrorHandlingRules)
	}
	rule := detail.EffectiveErrorHandlingRules[0]
	if rule.Source != "account" || rule.ID != "account.1" {
		t.Fatalf("展示条目应来自来源账户规则：%+v", rule)
	}
	if body, ok := rule.Rule.(map[string]any); !ok || body["name"] != "来源限流" {
		t.Fatalf("来源规则内容应透传：%+v", rule)
	}
	// 同口径验证：quota 策略臂此前已走 effectivePolicySource，保持一致。
	if detail.EffectiveQuotaRecoveryPolicy == nil {
		t.Fatal("quota 策略投影不得为空")
	}
}

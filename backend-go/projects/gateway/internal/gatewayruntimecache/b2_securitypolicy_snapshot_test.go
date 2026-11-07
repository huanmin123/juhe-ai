package gatewayruntimecache

// API Key 安全策略运行时快照测试（安全策略设计 §4.1/§10，批B2）：
//   - 绑定快照两字段（SecurityPolicyID / SecurityPolicyRulesJSON）随
//     loadGatewayAPIKeyRow LEFT JOIN security_policies 带出，rules_json 原文
//     直进快照（json tag 即序列化形态）；
//   - 引用损坏语义对齐调度偏好 P2：绑定 id 非空但 JOIN 谓词落空（行缺失/跨
//     owner）显式上抛，rules_json 损坏（编译失败）显式上抛，禁止静默回落内置；
//   - 未绑定（NULL）/内置行（{"rules":[]}）落零开销形态；
//   - 编译缓存：同 rulesJSON 命中同一编译产物实例；空规则集返回 Empty。

import (
	"context"
	"testing"
)

func TestB2SecurityPolicySnapshotLoad(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	// 安全策略存储面（security_policies 表 + api_keys.security_policy_id 列）
	// 已随共享夹具 schema 建出（sqlmodels_test.go，与生产 DDL 同构）；这里只
	// 种本用例的读取世界。
	for _, statement := range []string{
		`INSERT INTO juhe_business.security_policies (id, system_account_id, name, rules_json, built_in, created_at, updated_at)
			VALUES ('secpol-builtin-default', NULL, '默认安全策略', '{"rules":[]}', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.security_policies (id, system_account_id, name, rules_json, built_in, created_at, updated_at)
			VALUES ('secpol_custom_1', 'sys_owner', '手机号占位', '{"rules":[{"id":"r1","type":"phone","action":"placeholder"}]}', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.security_policies (id, system_account_id, name, rules_json, built_in, created_at, updated_at)
			VALUES ('secpol_corrupt', 'sys_owner', '损坏规则', '{"rules":[{"id":"r1","type":"unknown_type","action":"mask"}]}', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.security_policies (id, system_account_id, name, rules_json, built_in, created_at, updated_at)
			VALUES ('secpol_other_owner', 'sys_other', '他人策略', '{"rules":[{"id":"r1","type":"phone","action":"mask"}]}', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g1', 'sys_owner', 1, 'personal')`,
		`INSERT INTO juhe_business.route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_secpol', 'sys_owner', 'normal', NULL, 'active')`,
		`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_secpol', 'rs_secpol', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		// purpose='chat'：普通 Key 走偏好合成（与调度偏好 P2 世界一致），安全
		// 策略绑定是正交维度。
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose, scheduling_preference_id, security_policy_id)
			VALUES ('key_secpol_custom', 'sys_owner', 'rs_secpol', '` + HashSecret("sk-secpol-custom") + `', 'active', 'chat', 'schpref_builtin', 'secpol_custom_1')`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose, scheduling_preference_id, security_policy_id)
			VALUES ('key_secpol_builtin', 'sys_owner', 'rs_secpol', '` + HashSecret("sk-secpol-builtin") + `', 'active', 'chat', 'schpref_builtin', 'secpol-builtin-default')`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose, scheduling_preference_id, security_policy_id)
			VALUES ('key_secpol_null', 'sys_owner', 'rs_secpol', '` + HashSecret("sk-secpol-null") + `', 'active', 'chat', 'schpref_builtin', NULL)`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose, scheduling_preference_id, security_policy_id)
			VALUES ('key_secpol_missing', 'sys_owner', 'rs_secpol', '` + HashSecret("sk-secpol-missing") + `', 'active', 'chat', 'schpref_builtin', 'secpol_not_exists')`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose, scheduling_preference_id, security_policy_id)
			VALUES ('key_secpol_cross', 'sys_owner', 'rs_secpol', '` + HashSecret("sk-secpol-cross") + `', 'active', 'chat', 'schpref_builtin', 'secpol_other_owner')`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose, scheduling_preference_id, security_policy_id)
			VALUES ('key_secpol_corrupt', 'sys_owner', 'rs_secpol', '` + HashSecret("sk-secpol-corrupt") + `', 'active', 'chat', 'schpref_builtin', 'secpol_corrupt')`,
		`INSERT INTO juhe_business.scheduling_preferences (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
			VALUES ('schpref_builtin', NULL, '默认调度偏好', 'cost_first', NULL, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	models, err := NewSQLReadModels(db, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{
		"g1": {Accounts: []OpenAIAccountSecret{testAccount("a1", "sys_owner")}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Run("绑定自定义策略带出原文并编译", func(t *testing.T) {
		runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-secpol-custom"))
		if err != nil || runtime.APIKey == nil {
			t.Fatalf("读取 = %+v err=%v", runtime.APIKey, err)
		}
		row := runtime.APIKey
		if row.SecurityPolicyID != "secpol_custom_1" {
			t.Fatalf("SecurityPolicyID = %q", row.SecurityPolicyID)
		}
		wantRules := `{"rules":[{"id":"r1","type":"phone","action":"placeholder"}]}`
		if row.SecurityPolicyRulesJSON != wantRules {
			t.Fatalf("SecurityPolicyRulesJSON 应为原文 %q，得到 %q", wantRules, row.SecurityPolicyRulesJSON)
		}
		policy, err := CompiledPolicyForKeyRow(row)
		if err != nil {
			t.Fatalf("编译: %v", err)
		}
		if policy.Empty() || !policy.HasPlaceholder() {
			t.Fatalf("编译产物应为非空 placeholder 策略: empty=%v placeholder=%v", policy.Empty(), policy.HasPlaceholder())
		}
	})

	t.Run("绑定内置行带出空规则原文", func(t *testing.T) {
		runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-secpol-builtin"))
		if err != nil || runtime.APIKey == nil {
			t.Fatalf("读取 = %+v err=%v", runtime.APIKey, err)
		}
		row := runtime.APIKey
		if row.SecurityPolicyID != "secpol-builtin-default" || row.SecurityPolicyRulesJSON != `{"rules":[]}` {
			t.Fatalf("内置行快照不符: id=%q rules=%q", row.SecurityPolicyID, row.SecurityPolicyRulesJSON)
		}
		policy, err := CompiledPolicyForKeyRow(row)
		if err != nil {
			t.Fatalf("编译: %v", err)
		}
		if !policy.Empty() {
			t.Fatalf("内置行编译产物应为 Empty")
		}
	})

	t.Run("未绑定两字段保持空", func(t *testing.T) {
		runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-secpol-null"))
		if err != nil || runtime.APIKey == nil {
			t.Fatalf("读取 = %+v err=%v", runtime.APIKey, err)
		}
		row := runtime.APIKey
		if row.SecurityPolicyID != "" || row.SecurityPolicyRulesJSON != "" {
			t.Fatalf("未绑定应保持空串: id=%q rules=%q", row.SecurityPolicyID, row.SecurityPolicyRulesJSON)
		}
		policy, err := CompiledPolicyForKeyRow(row)
		if err != nil {
			t.Fatalf("编译: %v", err)
		}
		if !policy.Empty() {
			t.Fatalf("未绑定编译产物应为 Empty")
		}
	})

	t.Run("引用行缺失显式上抛", func(t *testing.T) {
		_, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-secpol-missing"))
		if err == nil {
			t.Fatalf("引用行缺失必须显式上抛")
		}
	})

	t.Run("跨 owner 引用显式上抛", func(t *testing.T) {
		_, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-secpol-cross"))
		if err == nil {
			t.Fatalf("跨 owner 引用必须显式上抛")
		}
	})

	t.Run("规则集损坏显式上抛", func(t *testing.T) {
		_, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-secpol-corrupt"))
		if err == nil {
			t.Fatalf("规则集损坏必须显式上抛")
		}
	})
}

func TestB2SecurityPolicyCompileCache(t *testing.T) {
	rules := `{"rules":[{"id":"r1","type":"phone","action":"placeholder"}]}`
	first, err := compiledSecurityPolicyForRules(rules)
	if err != nil {
		t.Fatalf("首次编译: %v", err)
	}
	second, err := compiledSecurityPolicyForRules(rules)
	if err != nil {
		t.Fatalf("二次编译: %v", err)
	}
	if first != second {
		t.Fatalf("同 rulesJSON 必须命中同一编译产物实例（内容摘要键）")
	}
	// 空/空白输入返回共享 Empty 单例。
	empty, err := compiledSecurityPolicyForRules("   ")
	if err != nil {
		t.Fatalf("空白输入: %v", err)
	}
	if empty != emptySecurityPolicy || !empty.Empty() {
		t.Fatalf("空白输入应返回共享 Empty 单例")
	}
	// nil 行与普通行入口。
	if policy, err := CompiledPolicyForKeyRow(nil); err != nil || !policy.Empty() {
		t.Fatalf("nil 行应返回 Empty: %v %v", policy, err)
	}
	if _, err := compiledSecurityPolicyForRules(`{"rules":[{"id":"r1","type":"phone"}]}`); err == nil {
		t.Fatalf("缺 action 的规则必须编译失败")
	}
}

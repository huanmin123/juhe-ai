package gatewayruntimecache

// w17a 覆盖波次：loadGatewayAPIKeyByHash 的调度配置解码门通用化。
// normalRoutingConfig 键是 normal/weighted/failover/round_robin 四种调度
// 模式共享的组内调度配置（历史命名）：同一 config_json 在四种模式下都解码
// 出 speed_first 配置；hybrid_smart 行即使 config_json 含 normalRoutingConfig
// 子对象也不解码（NormalRoutingConfig 保持 nil，仅解码 hybridRoutingConfig）；
// 无/空 config_json 回落 nil、空对象回落 cost_first 的既有行为不变。

import (
	"context"
	"encoding/json"
	"testing"
)

func TestW17ASQLSchedulingModeDecode(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{
		"g1": {Accounts: []OpenAIAccountSecret{testAccount("a1", "sys_owner")}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	const speedConfig = `{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000}}`
	modes := []struct {
		strategyID string
		mode       string
		keyHash    string
	}{
		{"rs_w17a_normal", RouteStrategyModeNormal, "sk-w17a-normal"},
		{"rs_w17a_weighted", RouteStrategyModeWeighted, "sk-w17a-weighted"},
		{"rs_w17a_failover", RouteStrategyModeFailover, "sk-w17a-failover"},
		{"rs_w17a_roundrobin", RouteStrategyModeRoundRobin, "sk-w17a-roundrobin"},
	}
	seed := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g1', 'sys_owner', 'gpt', 1, 'personal')`,
	}
	for _, m := range modes {
		seed = append(seed,
			`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('`+m.strategyID+`', 'sys_owner', '`+m.mode+`', '`+speedConfig+`', 'active')`,
			`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key_`+m.strategyID+`', 'sys_owner', '`+m.strategyID+`', '`+HashSecret(m.keyHash)+`', 'active')`,
			`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_`+m.strategyID+`', '`+m.strategyID+`', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		)
	}
	seed = append(seed,
		// hybrid_smart：config_json 同样携带 normalRoutingConfig 子对象，但不解码。
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_w17a_hybrid', 'sys_owner', 'hybrid_smart', '`+speedConfig+`', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key_w17a_hybrid', 'sys_owner', 'rs_w17a_hybrid', '`+HashSecret("sk-w17a-hybrid")+`', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_w17a_hybrid', 'rs_w17a_hybrid', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		// 无 config_json / 空 config_json / 空对象 config_json。
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_w17a_noconf', 'sys_owner', 'weighted', NULL, 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key_w17a_noconf', 'sys_owner', 'rs_w17a_noconf', '`+HashSecret("sk-w17a-noconf")+`', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_w17a_noconf', 'rs_w17a_noconf', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_w17a_empty', 'sys_owner', 'weighted', '', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key_w17a_empty', 'sys_owner', 'rs_w17a_empty', '`+HashSecret("sk-w17a-empty")+`', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_w17a_empty', 'rs_w17a_empty', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_w17a_emptyobj', 'sys_owner', 'weighted', '{}', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key_w17a_emptyobj', 'sys_owner', 'rs_w17a_emptyobj', '`+HashSecret("sk-w17a-emptyobj")+`', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_w17a_emptyobj', 'rs_w17a_emptyobj', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
	)
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	ctx := context.Background()

	// 四种调度模式：同一 config_json 都解码出 speed_first + firstByteDeadlineMs。
	for _, m := range modes {
		runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret(m.keyHash))
		if err != nil || runtime.APIKey == nil {
			t.Fatalf("%s 读取 = %+v err=%v", m.mode, runtime.APIKey, err)
		}
		config := runtime.APIKey.NormalRoutingConfig
		if config == nil || config.SchedulingPreference != "speed_first" || config.Raw == nil {
			t.Fatalf("%s 调度配置 = %+v", m.mode, config)
		}
		if runtime.APIKey.RouteStrategyMode != m.mode {
			t.Fatalf("%s 模式 = %q", m.mode, runtime.APIKey.RouteStrategyMode)
		}
		if runtime.APIKey.HybridRoutingConfig != nil {
			t.Fatalf("%s 不应解码混合配置 = %+v", m.mode, runtime.APIKey.HybridRoutingConfig)
		}
		var payload map[string]any
		if err := json.Unmarshal(config.Raw, &payload); err != nil {
			t.Fatalf("%s raw 解码: %v", m.mode, err)
		}
		if deadline, ok := payload["firstByteDeadlineMs"].(float64); !ok || deadline != 30000 {
			t.Fatalf("%s firstByteDeadlineMs = %v", m.mode, payload["firstByteDeadlineMs"])
		}
	}

	// hybrid_smart：即使 config_json 含 normalRoutingConfig 子对象也不解码。
	hybrid, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-w17a-hybrid"))
	if err != nil || hybrid.APIKey == nil {
		t.Fatalf("hybrid 读取 = %+v err=%v", hybrid.APIKey, err)
	}
	if hybrid.APIKey.RouteStrategyMode != RouteStrategyModeHybridSmart {
		t.Fatalf("hybrid 模式 = %q", hybrid.APIKey.RouteStrategyMode)
	}
	if hybrid.APIKey.NormalRoutingConfig != nil {
		t.Fatalf("hybrid_smart 行不得解码 normalRoutingConfig = %+v", hybrid.APIKey.NormalRoutingConfig)
	}

	// 无 config_json / 空 config_json → NormalRoutingConfig 保持 nil。
	for _, missing := range []struct{ name, keyHash string }{
		{"无 config_json", "sk-w17a-noconf"},
		{"空 config_json", "sk-w17a-empty"},
	} {
		runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret(missing.keyHash))
		if err != nil || runtime.APIKey == nil {
			t.Fatalf("%s 读取 = %+v err=%v", missing.name, runtime.APIKey, err)
		}
		if runtime.APIKey.NormalRoutingConfig != nil {
			t.Fatalf("%s 必须保持 nil = %+v", missing.name, runtime.APIKey.NormalRoutingConfig)
		}
	}

	// 空对象 config_json → 既有 cost_first 缺省行为不变。
	emptyObj, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-w17a-emptyobj"))
	if err != nil || emptyObj.APIKey == nil {
		t.Fatalf("空对象读取 = %+v err=%v", emptyObj.APIKey, err)
	}
	config := emptyObj.APIKey.NormalRoutingConfig
	if config == nil || config.SchedulingPreference != "cost_first" {
		t.Fatalf("空对象配置 = %+v", config)
	}
}

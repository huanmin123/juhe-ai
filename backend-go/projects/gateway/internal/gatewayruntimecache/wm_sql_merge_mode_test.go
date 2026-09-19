package gatewayruntimecache

// 合并路由（merge 模式）B8 回归：NormalizeRouteStrategyMode 白名单放行
// merge，merge 策略行经 SQL runtime cache 读取（ReadGatewayRuntimeByKeyHash）
// 后 RouteStrategyMode 保持 "merge"——修复前未知值静默回退 normal；统一解码
// 门覆盖 merge，speed_first 的 normalRoutingConfig 照常解码。

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWMSQLMergeModeRoundTrip(t *testing.T) {
	if NormalizeRouteStrategyMode(RouteStrategyModeMerge) != RouteStrategyModeMerge {
		t.Fatal("NormalizeRouteStrategyMode(merge) 必须保持 merge")
	}
	if NormalizeRouteStrategyMode("bogus") != RouteStrategyModeNormal {
		t.Fatal("未知值必须回落 normal")
	}

	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{
		"g1": {Accounts: []OpenAIAccountSecret{testAccount("a1", "sys_owner")}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	const speedConfig = `{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000}}`
	seed := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g1', 'sys_owner', 'gpt', 1, 'personal')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g2', 'sys_owner', 'gpt', 1, 'personal')`,
		// merge 策略（双绑定 + speed_first 配置）+ 绑定 API Key。
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_wm_merge', 'sys_owner', 'merge', '` + speedConfig + `', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key_wm_merge', 'sys_owner', 'rs_wm_merge', '` + HashSecret("sk-wm-merge") + `', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_wm_merge_1', 'rs_wm_merge', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_wm_merge_2', 'rs_wm_merge', 'sys_owner', 'g2', 2, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		// merge + 无 config_json：mode 保持 merge、调度配置保持 nil。
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_wm_merge_noconf', 'sys_owner', 'merge', NULL, 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key_wm_merge_noconf', 'sys_owner', 'rs_wm_merge_noconf', '` + HashSecret("sk-wm-merge-noconf") + `', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b_wm_merge_noconf', 'rs_wm_merge_noconf', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	ctx := context.Background()

	// B8 底线：merge 行 mode 保持 merge（不被静默回退 normal）。
	runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-wm-merge"))
	if err != nil || runtime.APIKey == nil {
		t.Fatalf("merge 读取 = %+v err=%v", runtime.APIKey, err)
	}
	if runtime.APIKey.RouteStrategyMode != RouteStrategyModeMerge {
		t.Fatalf("merge 行 mode = %q，必须保持 merge", runtime.APIKey.RouteStrategyMode)
	}
	// 解码门覆盖 merge：speed_first 的 normalRoutingConfig 照常解码。
	config := runtime.APIKey.NormalRoutingConfig
	if config == nil || config.SchedulingPreference != "speed_first" || config.Raw == nil {
		t.Fatalf("merge speed_first 调度配置 = %+v", config)
	}
	var payload map[string]any
	if err := json.Unmarshal(config.Raw, &payload); err != nil {
		t.Fatalf("raw 解码: %v", err)
	}
	if deadline, ok := payload["firstByteDeadlineMs"].(float64); !ok || deadline != 30000 {
		t.Fatalf("firstByteDeadlineMs = %v", payload["firstByteDeadlineMs"])
	}
	// 双绑定投影完整。
	if len(runtime.APIKey.GroupBindings) != 2 {
		t.Fatalf("merge 绑定投影 = %+v", runtime.APIKey.GroupBindings)
	}

	// merge + 无 config_json：mode 仍保持 merge、调度配置 nil。
	noconf, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-wm-merge-noconf"))
	if err != nil || noconf.APIKey == nil {
		t.Fatalf("merge noconf 读取 = %+v err=%v", noconf.APIKey, err)
	}
	if noconf.APIKey.RouteStrategyMode != RouteStrategyModeMerge {
		t.Fatalf("merge noconf 行 mode = %q", noconf.APIKey.RouteStrategyMode)
	}
	if noconf.APIKey.NormalRoutingConfig != nil {
		t.Fatalf("无 config_json 必须保持 nil = %+v", noconf.APIKey.NormalRoutingConfig)
	}
}

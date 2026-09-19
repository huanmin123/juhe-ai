package gatewayruntimecache

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
)

// SQL 读模型的 seam 接线：未接线时快速失败，接线后逐层委托，
// 并发源缺省读零（对齐无并发状态的 Node 运行时）。
func TestWhSQLReadModelsSeams(t *testing.T) {
	if _, err := NewSQLReadModels(nil, false, nil, nil, nil, nil); err == nil {
		t.Fatal("nil 数据库必须报错")
	}
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 未接线：账户/目录读快速失败，并发挥拉取读零。
	if _, err := models.ListOpenAIAccountsForGroupResult(ctx, "g1", "sys", OpenAIAccountsForGroupOptions{}); err == nil {
		t.Fatal("未接线账户选择器必须报错")
	}
	if _, err := models.ListProviderModelCatalog(ctx, ModelCatalogListOptions{ProviderCode: "gpt"}); err == nil {
		t.Fatal("未接线目录源必须报错")
	}
	concurrency, err := models.LoadAccountCurrentConcurrencyByID(ctx, []string{"a1"})
	if err != nil || len(concurrency) != 0 {
		t.Fatalf("缺省并发必须为空表 = %v err=%v", concurrency, err)
	}

	// 接线后委托：账户、目录与并发表都命中替身。
	models.SetAccountsSelector(seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{
		"g1": {Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}},
	}})
	models.SetCatalogSource(whCatalogStub{items: []ProviderModelCatalogItem{{ProviderCode: "gpt", Model: "m1"}}})
	models.SetConcurrencySource(whConcurrencyStub{values: map[string]int{"a1": 2}})
	result, err := models.ListOpenAIAccountsForGroupResult(ctx, "g1", "sys", OpenAIAccountsForGroupOptions{})
	if err != nil || len(result.Accounts) != 1 {
		t.Fatalf("接线账户委托 = %+v err=%v", result, err)
	}
	items, err := models.ListProviderModelCatalog(ctx, ModelCatalogListOptions{ProviderCode: "gpt"})
	if err != nil || len(items) != 1 {
		t.Fatalf("接线目录委托 = %v err=%v", items, err)
	}
	concurrency, err = models.LoadAccountCurrentConcurrencyByID(ctx, []string{"a1"})
	if err != nil || concurrency["a1"] != 2 {
		t.Fatalf("接线并发委托 = %v err=%v", concurrency, err)
	}
	// SetSettingsStore(nil) 保持原仓库可用。
	models.SetSettingsStore(nil)
	if _, err := models.ReadGatewaySettings(ctx); err != nil {
		t.Fatalf("设置读取 = %v", err)
	}
	// SetSettingsStore 注入组合共享仓库。
	sharedStore, err := settings.NewStore(db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	models.SetSettingsStore(sharedStore)
	if _, err := models.ReadGatewaySettings(ctx); err != nil {
		t.Fatalf("共享仓库设置读取 = %v", err)
	}
}

// whCatalogStub 是 CatalogSource 的最小替身。
type whCatalogStub struct{ items []ProviderModelCatalogItem }

func (s whCatalogStub) ListProviderModelCatalog(ctx context.Context, input ModelCatalogListOptions) ([]ProviderModelCatalogItem, error) {
	return s.items, nil
}

// whConcurrencyStub 是 ConcurrencySource 的最小替身。
type whConcurrencyStub struct{ values map[string]int }

func (s whConcurrencyStub) LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error) {
	out := map[string]int{}
	for _, id := range accountIDs {
		out[id] = s.values[id]
	}
	return out, nil
}

// 按 key_hash 的运行态读取（启动预热路径）：跳过 sk- 前缀守卫并复用同一
// 校验/组合尾段。
func TestWhSQLReadGatewayRuntimeByKeyHash(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{
		"g1": {Accounts: []OpenAIAccountSecret{testAccount("a1", "sys_owner")}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g1', 'sys_owner', 'gpt', 1, 'personal')`,
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs1', 'sys_owner', 'normal', '{"schedulingPreference":"speed_first","firstByteDeadlineMs":2500}', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key1', 'sys_owner', 'rs1', '` + HashSecret("sk-hash1") + `', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b1', 'rs1', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	ctx := context.Background()

	runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-hash1"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.APIKey == nil || runtime.APIKey.SelectedGroupID != "g1" || runtime.APIKey.NormalRoutingConfig == nil {
		t.Fatalf("按 hash 读取 = %+v", runtime.APIKey)
	}
	if len(runtime.Accounts) != 1 || runtime.Accounts[0].ID != "a1" {
		t.Fatalf("账户集 = %+v", runtime.Accounts)
	}
	// 未命中：仅设置投影 + 空账户。
	missing, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-missing"))
	if err != nil || missing.APIKey != nil || missing.Accounts == nil {
		t.Fatalf("未命中 = %+v err=%v", missing, err)
	}
	// 原始 key 读取（sk- 守卫路径）。
	raw, err := models.ReadGatewayRuntime(ctx, "sk-hash1")
	if err != nil || raw.APIKey == nil {
		t.Fatalf("原始 key 读取 = %+v err=%v", raw.APIKey, err)
	}
	// 非 sk- 前缀：仅设置投影。
	anonymous, err := models.ReadGatewayRuntime(ctx, "not-a-key")
	if err != nil || anonymous.APIKey != nil {
		t.Fatalf("匿名读取 = %+v err=%v", anonymous.APIKey, err)
	}
	// 预热枚举：活跃 key 的 hash 集合。
	hashes, err := models.ListActiveGatewayAPIKeyHashes(ctx)
	if err != nil || len(hashes) != 1 || hashes[0] != HashSecret("sk-hash1") {
		t.Fatalf("预热哈希 = %v err=%v", hashes, err)
	}
}

// 过期/禁用行与动态路由模式在运行态读取中的短路语义。
func TestWhSQLReadGatewayRuntimeGates(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g1', 'sys_owner', 'gpt', 1, 'personal')`,
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_normal', 'sys_owner', 'normal', '{}', 'active')`,
		`INSERT INTO route_strategies (id, system_account_id, mode, config_json, status) VALUES ('rs_weighted', 'sys_owner', 'weighted', '{}', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status, expires_at) VALUES ('key_expired', 'sys_owner', 'rs_normal', '` + HashSecret("sk-expired") + `', 'active', '2000-01-01T00:00:00.000Z')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status, expires_at) VALUES ('key_disabled', 'sys_owner', 'rs_normal', '` + HashSecret("sk-disabled") + `', 'disabled', NULL)`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status, expires_at) VALUES ('key_dynamic', 'sys_owner', 'rs_weighted', '` + HashSecret("sk-dynamic") + `', 'active', NULL)`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('bh', 'rs_weighted', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status, expires_at) VALUES ('key_nobinding', 'sys_owner', 'rs_normal', '` + HashSecret("sk-nobinding") + `', 'active', NULL)`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	ctx := context.Background()

	// 过期 → nil。
	expired, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-expired"))
	if err != nil || expired.APIKey != nil {
		t.Fatalf("过期 key = %+v err=%v", expired.APIKey, err)
	}
	// 禁用 → nil。
	disabled, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-disabled"))
	if err != nil || disabled.APIKey != nil {
		t.Fatalf("禁用 key = %+v err=%v", disabled.APIKey, err)
	}
	// 无绑定 → nil（校验失败语义）。
	noBinding, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-nobinding"))
	if err != nil || noBinding.APIKey != nil {
		t.Fatalf("无绑定 key = %+v err=%v", noBinding.APIKey, err)
	}
	// 动态路由模式：静态形态返回，账户集为空（由 Service 层重选）。
	dynamic, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-dynamic"))
	if err != nil || dynamic.APIKey == nil {
		t.Fatalf("动态 key = %+v err=%v", dynamic.APIKey, err)
	}
	if dynamic.APIKey.RouteStrategyMode != RouteStrategyModeWeighted || len(dynamic.Accounts) != 0 {
		t.Fatalf("动态形态 = mode:%s accounts:%d", dynamic.APIKey.RouteStrategyMode, len(dynamic.Accounts))
	}
	if got := decodeNormalRoutingConfig(`{"schedulingPreference":"cost_first"}`); got == nil {
		t.Fatal("合法普通配置必须解码")
	}
	if got := decodeNormalRoutingConfig(`nope`); got != nil {
		t.Fatal("坏普通配置必须返回 nil")
	}
	// 限额 JSON 解析：合法/空/坏。
	if limits := parseUserRequestLimitsJSON(sql.NullString{String: `{"perMinute":7}`, Valid: true}); limits == nil || limits.PerMinute == nil || *limits.PerMinute != 7 {
		t.Fatalf("限额解析 = %+v", limits)
	}
	if limits := parseUserRequestLimitsJSON(sql.NullString{}); limits != nil {
		t.Fatal("空限额必须返回 nil")
	}
	if limits := parseUserRequestLimitsJSON(sql.NullString{String: "bad", Valid: true}); limits != nil {
		t.Fatal("坏限额必须返回 nil")
	}
	// 绑定归一化与去重。
	bindings := []GatewayAPIKeyGroupBindingRow{
		{GroupID: "g1", Status: "active", GroupEnabled: 1},
		{GroupID: "g1", Status: "active", GroupEnabled: 1},
		{GroupID: "g2", Status: "disabled", GroupEnabled: 1},
		{GroupID: "g3", Status: "active", GroupEnabled: 0},
	}
	// 归一化只过滤非激活/禁用分组，不做去重。
	normalized := normalizeGatewayAPIKeyGroupBindings(bindings)
	if len(normalized) != 2 {
		t.Fatalf("绑定归一化 = %d", len(normalized))
	}
	if ids := uniqueGroupIDs(bindings); len(ids) != 3 {
		t.Fatalf("分组去重（含未激活） = %v", ids)
	}
	// 时间过期判定。
	if expiredBool, err := isoTimeExpired("2000-01-01T00:00:00.000Z", 1_700_000_000_000); err != nil || !expiredBool {
		t.Fatalf("过期判定 = %v err=%v", expiredBool, err)
	}
	if expiredBool, err := isoTimeExpired("", 1_700_000_000_000); err != nil || expiredBool {
		t.Fatalf("空过期时间 = %v err=%v", expiredBool, err)
	}
}

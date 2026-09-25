package schema

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

// TestW9ASeedSQLiteDefaultsStatementFailures 逐条注入执行失败，覆盖
// SeedSQLiteDefaults 全部语句错误包装。
func TestW9ASeedSQLiteDefaultsStatementFailures(t *testing.T) {
	seed := func(rec *wmSchemaRecorder) error {
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		_, err := SeedSQLiteDefaults(context.Background(), db, SeedOptions{Now: func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }})
		return err
	}
	total := w9aSeedStatementCount(t, seed)
	if total < 10 {
		t.Fatalf("成功路径语句数异常: %d", total)
	}
	for failAfter := 1; failAfter <= total-1; failAfter++ {
		rec := &wmSchemaRecorder{failExecAfter: failAfter}
		db := openWMSchemaFakeDB(rec)
		// 与上方计数闭包固定同一时钟：快照中 shutdown_date 早于当前日期的行
		// 会随 real clock 变化，导致语句数与 total 不一致。
		_, err := SeedSQLiteDefaults(context.Background(), db, SeedOptions{Now: func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }})
		db.Close()
		if err == nil || !strings.Contains(err.Error(), "sqlite seed statement") {
			t.Fatalf("failExecAfter=%d 错误异常: %v", failAfter, err)
		}
	}
}

// TestW9ASeedSQLiteHelpersSuccessPaths 通过脚本化查询覆盖 SQLite 变体的
// 路由策略/API Key/对话 Key/外部集成 Token 创建链与存在即跳过分支。
func TestW9ASeedSQLiteHelpersSuccessPaths(t *testing.T) {
	rec := &wmSchemaRecorder{}
	now := "2026-09-01T00:00:00.000Z"

	// 默认分组列表：两个可建默认路由的分组。
	rec.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{
		{"grp_gpt_default", "gpt默认分组"},
		{"grp_deepseek_default", "deepseek默认分组"},
	})
	// route strategy exists：命中 → 继续绑定（第二个分组走 exists=false 分支由
	// 另一个用例覆盖）。
	rec.script(sqSeedRouteStrategyExistsSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default"}})
	rec.script(sqSeedRouteStrategyExistsSelect, nil, [][]driver.Value{{"route_strategy_grp_deepseek_default"}})
	// existing default api key：零行 → 创建。

	// chat key：已存在且非空 → 直接返回；这里走创建链。
	rec.script(sqSeedChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
	rec.script(sqSeedChatKeyRouteSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default", "gpt默认路由"}})

	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	var executed []string
	exec := func(query string, _ ...any) error {
		executed = append(executed, query)
		return nil
	}
	ctx := context.Background()
	if err := seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, db, exec, SeedOptions{}, now); err != nil {
		t.Fatalf("route strategies: %v", err)
	}
	if err := seedSQLiteAdminChatAPIKey(ctx, db, exec, SeedOptions{}, now); err != nil {
		t.Fatalf("chat api key: %v", err)
	}
	if err := seedSQLiteExternalIntegrationTestToken(ctx, db, exec, SeedOptions{}, now); err != nil {
		t.Fatalf("external token: %v", err)
	}

	joined := strings.Join(executed, "\n")
	for _, fragment := range []string{
		sqSeedRouteStrategyInsert,
		sqSeedRouteStrategyGroupBindingInsert,
		sqSeedDefaultAPIKeyInsert,
		sqSeedChatAPIKeyInsert,
		sqSeedExternalIntegrationTokenInsert,
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("缺少语句 %q", fragment)
		}
	}
}

// TestW9ASeedSQLiteRouteStrategyMissing 覆盖 exists=false 的跳过分支。
func TestW9ASeedSQLiteRouteStrategyMissing(t *testing.T) {
	rec := &wmSchemaRecorder{}
	rec.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{"grp_x_default", "x分组"}})
	// exists select 零行 → false → 跳过绑定。
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	var executed []string
	exec := func(query string, _ ...any) error {
		executed = append(executed, query)
		return nil
	}
	if err := seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(context.Background(), db, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("exists=false: %v", err)
	}
	for _, query := range executed {
		if strings.Contains(query, sqSeedRouteStrategyGroupBindingInsert) {
			t.Fatal("exists=false 不应插入绑定")
		}
	}
}

// TestW9ASeedSQLiteChatKeyBranches 覆盖 chat key 的已存在/分组缺失/路由缺失
// 分支。
func TestW9ASeedSQLiteChatKeyBranches(t *testing.T) {
	ctx := context.Background()
	exec := func(string, ...any) error { return nil }

	// 已存在非空 → return nil。
	recExists := &wmSchemaRecorder{}
	recExists.script(sqSeedChatKeyExistsSelect, nil, [][]driver.Value{{"key_chat_sys_admin"}})
	dbExists := openWMSchemaFakeDB(recExists)
	defer dbExists.Close()
	if err := seedSQLiteAdminChatAPIKey(ctx, dbExists, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("existing: %v", err)
	}

	// 不存在且默认分组缺失 → return nil。
	recMissing := &wmSchemaRecorder{}
	dbMissing := openWMSchemaFakeDB(recMissing)
	defer dbMissing.Close()
	if err := seedSQLiteAdminChatAPIKey(ctx, dbMissing, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("missing group: %v", err)
	}

	// 分组存在但路由缺失 → return nil。
	recNoRoute := &wmSchemaRecorder{}
	recNoRoute.script(sqSeedChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
	dbNoRoute := openWMSchemaFakeDB(recNoRoute)
	defer dbNoRoute.Close()
	if err := seedSQLiteAdminChatAPIKey(ctx, dbNoRoute, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("missing route: %v", err)
	}
}

// TestW9ASeedSQLiteExternalTokenUpdate 覆盖 token 已存在的 UPDATE 分支。
func TestW9ASeedSQLiteExternalTokenUpdate(t *testing.T) {
	rec := &wmSchemaRecorder{}
	rec.script(sqSeedExternalIntegrationTokenSelect, nil, [][]driver.Value{{externalIntegrationTestTokenID}})
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	var executed []string
	exec := func(query string, _ ...any) error {
		executed = append(executed, query)
		return nil
	}
	if err := seedSQLiteExternalIntegrationTestToken(context.Background(), db, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("token update: %v", err)
	}
	if len(executed) == 0 || !strings.Contains(executed[len(executed)-1], sqSeedExternalIntegrationTokenUpdate) {
		t.Fatalf("最后一条应是 token UPDATE: %v", executed)
	}
}

// TestW9ASeedSQLiteQueryFailuresOnClosedDB 覆盖查询错误包装：关闭的数据库
// 让所有 Query 失败。
func TestW9ASeedSQLiteQueryFailuresOnClosedDB(t *testing.T) {
	db := openWMSchemaFakeDB(&wmSchemaRecorder{})
	db.Close()
	ctx := context.Background()
	exec := func(string, ...any) error { return nil }

	err := seedSQLiteDisableStaleGeneratedModels(ctx, db, exec, "2026-09-01T00:00:00.000Z", map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "select generated model rows") {
		t.Fatalf("stale select 失败必须被包装: %v", err)
	}
	err = seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, db, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z")
	if err == nil || !strings.Contains(err.Error(), "select default groups") {
		t.Fatalf("default groups select 失败必须被包装: %v", err)
	}
	err = seedSQLiteAdminChatAPIKey(ctx, db, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z")
	if err == nil || !strings.Contains(err.Error(), "check chat api key") {
		t.Fatalf("chat key check 失败必须被包装: %v", err)
	}
	if _, err := seedSQLiteRouteStrategyExists(ctx, db, "route_strategy_x"); err == nil || !strings.Contains(err.Error(), "check route strategy") {
		t.Fatalf("route strategy exists 失败必须被包装: %v", err)
	}
	if _, err := seedSQLiteExistingDefaultAPIKeyID(ctx, db, "route_strategy_x"); err == nil || !strings.Contains(err.Error(), "check existing default api key") {
		t.Fatalf("existing api key 失败必须被包装: %v", err)
	}
	err = seedSQLiteExternalIntegrationTestToken(ctx, db, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z")
	if err == nil || !strings.Contains(err.Error(), "select external integration token") {
		t.Fatalf("external token select 失败必须被包装: %v", err)
	}
	// repair 的 QueryRow 错误走 continue 分支，不返回错误。
	if err := seedSQLiteRepairProfileAccountTypes(ctx, db, exec, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("repair 错误必须按 Node 语义跳过: %v", err)
	}
}

// TestW9ASeedSQLiteStaleGeneratedModelsDisable 用脚本化行覆盖 stale 模型
// 枚举与禁用执行。
func TestW9ASeedSQLiteStaleGeneratedModelsDisable(t *testing.T) {
	rec := &wmSchemaRecorder{}
	rec.script(sqSeedGeneratedModelRowsSelect, nil, [][]driver.Value{
		{"provider_model_keep", "gpt", "kept-model"},
		{"provider_model_stale", "glm", "removed-model"},
	})
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	builtIn := map[string]bool{"gpt\x00kept-model": true}
	var disabled []string
	exec := func(_ string, args ...any) error {
		if len(args) == 2 {
			disabled = append(disabled, args[1].(string))
		}
		return nil
	}
	if err := seedSQLiteDisableStaleGeneratedModels(context.Background(), db, exec, "2026-09-01T00:00:00.000Z", builtIn); err != nil {
		t.Fatalf("stale disable: %v", err)
	}
	if len(disabled) != 1 || disabled[0] != "provider_model_stale" {
		t.Fatalf("只应禁用不在内置清单中的行: %v", disabled)
	}
}

// TestW9ASeedSQLiteScanFailures 用 NULL 列覆盖 Scan 错误包装。
func TestW9ASeedSQLiteScanFailures(t *testing.T) {
	rec := &wmSchemaRecorder{}
	rec.script(sqSeedGeneratedModelRowsSelect, nil, [][]driver.Value{{nil, "gpt", "m"}})
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	err := seedSQLiteDisableStaleGeneratedModels(context.Background(), db, func(string, ...any) error { return nil },
		"2026-09-01T00:00:00.000Z", map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "scan generated model row") {
		t.Fatalf("scan 失败必须被包装: %v", err)
	}

	recGroups := &wmSchemaRecorder{}
	recGroups.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{nil, "name"}})
	dbGroups := openWMSchemaFakeDB(recGroups)
	defer dbGroups.Close()
	err = seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(context.Background(), dbGroups, func(string, ...any) error { return nil },
		SeedOptions{}, "2026-09-01T00:00:00.000Z")
	if err == nil || !strings.Contains(err.Error(), "scan default group") {
		t.Fatalf("group scan 失败必须被包装: %v", err)
	}
}

// TestW9AEnsureAllSQLiteStatementFailures 覆盖 EnsureAllSQLite 的各 schema
// 错误包装与 ensure 执行失败分支。
func TestW9AEnsureAllSQLiteStatementFailures(t *testing.T) {
	ctx := context.Background()

	// 关闭的连接：business（第一个 schema）失败。
	closed := openWMSchemaFakeDB(&wmSchemaRecorder{})
	closed.Close()
	if _, err := EnsureAllSQLite(ctx, closed); err == nil || !strings.Contains(err.Error(), "ensure sqlite business schema") {
		t.Fatalf("business 失败必须被包装: %v", err)
	}
	if _, err := EnsureSQLiteBusiness(ctx, closed); err == nil || !strings.Contains(err.Error(), "sqlite schema statement") {
		t.Fatalf("ensure 执行失败必须带定位: %v", err)
	}

	// 逐 schema 注入失败：前面 schema 的语句全部成功后，目标 schema 首条失败。
	// stats 守卫（success_cost_usd 回填）在 stats DDL 之后追加
	// len(sqliteStatsSuccessCostGuardTables) 条 UPDATE，后续 schema 偏移随之平移。
	guardExecs := len(sqliteStatsSuccessCostGuardTables)
	offsets := map[string]int{
		"stats":   len(sqliteBusinessScript),
		"chat":    len(sqliteBusinessScript) + len(sqliteStatsScript) + guardExecs,
		"codex":   len(sqliteBusinessScript) + len(sqliteStatsScript) + len(sqliteChatScript) + guardExecs,
		"dataset": len(sqliteBusinessScript) + len(sqliteStatsScript) + len(sqliteChatScript) + len(sqliteCodexContextScript) + guardExecs,
		"usage":   len(sqliteBusinessScript) + len(sqliteStatsScript) + len(sqliteChatScript) + len(sqliteCodexContextScript) + len(sqliteDatasetScript) + guardExecs,
	}
	needles := map[string]string{
		"stats": "ensure sqlite stats schema", "chat": "ensure sqlite chat schema",
		"codex": "ensure sqlite codex context schema", "dataset": "ensure sqlite dataset schema",
		"usage": "ensure sqlite usage catalog schema",
	}
	for name, offset := range offsets {
		rec := &wmSchemaRecorder{failExecAfter: offset}
		db := openWMSchemaFakeDB(rec)
		_, err := EnsureAllSQLite(ctx, db)
		db.Close()
		if err == nil || !strings.Contains(err.Error(), needles[name]) {
			t.Fatalf("%s 失败必须被包装: %v", name, err)
		}
	}
}

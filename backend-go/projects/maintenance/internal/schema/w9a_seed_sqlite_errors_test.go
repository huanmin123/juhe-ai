package schema

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

// TestW9ASeedSQLiteDefaultsFullScriptInjectFailures 在脚本化查询链下逐条注入
// 失败，覆盖 SeedSQLiteDefaults 及其 helper 的查询依赖错误分支。
func TestW9ASeedSQLiteDefaultsFullScriptInjectFailures(t *testing.T) {
	buildRec := func() *wmSchemaRecorder {
		rec := &wmSchemaRecorder{}
		// stale generated models：一行 stale 数据（触发 disable UPDATE）。
		rec.script(sqSeedGeneratedModelRowsSelect, nil, [][]driver.Value{
			{"provider_model_stale", "glm", "removed-model"},
		})
		// profile account types repair：全部需要合并 → UPDATE。
		w9aScriptRowsFor(rec, sqSeedProfileAccountTypesSelect, len(pgSeedProfiles), []driver.Value{"[]"})
		// 默认分组列表。
		rec.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{"grp_gpt_default", "gpt默认分组"}})
		rec.script(sqSeedRouteStrategyExistsSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default"}})
		// chat key 创建链。
		rec.script(sqSeedChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
		rec.script(sqSeedChatKeyRouteSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default", "gpt默认路由"}})
		return rec
	}

	for failAfter := 1; failAfter <= 200; failAfter++ {
		rec := buildRec()
		db := openWMSchemaFakeDB(rec)
		_, err := SeedSQLiteDefaults(context.Background(), db, SeedOptions{})
		db.Close()
		if err != nil && !strings.Contains(err.Error(), "sqlite seed statement") && !strings.Contains(err.Error(), errW9AExecFailed.Error()) {
			t.Fatalf("failAfter=%d 错误异常: %v", failAfter, err)
		}
	}
}

// TestW9ASeedSQLiteRouteStrategyErrorBranches 逐分支注入，覆盖 SQLite 默认
// 路由策略创建链的错误包装。
func TestW9ASeedSQLiteRouteStrategyErrorBranches(t *testing.T) {
	ctx := context.Background()
	now := "2026-09-01T00:00:00.000Z"

	// route strategy insert 失败。
	recInsert := &wmSchemaRecorder{}
	recInsert.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{"grp_gpt_default", "gpt默认分组"}})
	dbInsert := openWMSchemaFakeDB(recInsert)
	defer dbInsert.Close()
	err := seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, dbInsert, w9aCountingExec(nil, 0), SeedOptions{}, now)
	if err == nil {
		t.Fatalf("route strategy insert 失败必须上抛: %v", err)
	}

	// exists select Scan 失败。
	recExists := &wmSchemaRecorder{}
	recExists.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{"grp_gpt_default", "gpt默认分组"}})
	recExists.script(sqSeedRouteStrategyExistsSelect, nil, [][]driver.Value{{nil}})
	dbExists := openWMSchemaFakeDB(recExists)
	defer dbExists.Close()
	err = seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, dbExists, w9aCountingExec(nil, 99), SeedOptions{}, now)
	if err == nil {
		t.Fatalf("exists select 失败必须上抛: %v", err)
	}

	// binding insert 失败。
	recBinding := &wmSchemaRecorder{}
	recBinding.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{"grp_gpt_default", "gpt默认分组"}})
	recBinding.script(sqSeedRouteStrategyExistsSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default"}})
	dbBinding := openWMSchemaFakeDB(recBinding)
	defer dbBinding.Close()
	err = seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, dbBinding, w9aCountingExec(nil, 1), SeedOptions{}, now)
	if err == nil {
		t.Fatalf("binding insert 失败必须上抛: %v", err)
	}

	// existing default api key select Scan 失败。
	recKeyScan := &wmSchemaRecorder{}
	recKeyScan.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{"grp_gpt_default", "gpt默认分组"}})
	recKeyScan.script(sqSeedRouteStrategyExistsSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default"}})
	recKeyScan.script(sqSeedExistingDefaultAPIKeySelect, nil, [][]driver.Value{{nil}})
	dbKeyScan := openWMSchemaFakeDB(recKeyScan)
	defer dbKeyScan.Close()
	err = seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, dbKeyScan, w9aCountingExec(nil, 99), SeedOptions{}, now)
	if err == nil {
		t.Fatalf("existing key select 失败必须上抛: %v", err)
	}

	// default api key insert 失败。
	recKeyInsert := &wmSchemaRecorder{}
	recKeyInsert.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{"grp_gpt_default", "gpt默认分组"}})
	recKeyInsert.script(sqSeedRouteStrategyExistsSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default"}})
	dbKeyInsert := openWMSchemaFakeDB(recKeyInsert)
	defer dbKeyInsert.Close()
	err = seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, dbKeyInsert, w9aCountingExec(nil, 2), SeedOptions{}, now)
	if err == nil {
		t.Fatalf("api key insert 失败必须上抛: %v", err)
	}
}

// TestW9ASeedSQLiteChatKeyScanErrorBranches 覆盖 chat key 的两个 Scan 错误
// 分支。
func TestW9ASeedSQLiteChatKeyScanErrorBranches(t *testing.T) {
	ctx := context.Background()
	exec := func(string, ...any) error { return nil }

	recGroup := &wmSchemaRecorder{}
	recGroup.script(sqSeedChatKeyDefaultGroupSelect, nil, [][]driver.Value{{nil}})
	dbGroup := openWMSchemaFakeDB(recGroup)
	defer dbGroup.Close()
	if err := seedSQLiteAdminChatAPIKey(ctx, dbGroup, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err == nil {
		t.Fatal("default group Scan 失败必须上抛")
	}

	recRoute := &wmSchemaRecorder{}
	recRoute.script(sqSeedChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
	recRoute.script(sqSeedChatKeyRouteSelect, nil, [][]driver.Value{{nil, nil}})
	dbRoute := openWMSchemaFakeDB(recRoute)
	defer dbRoute.Close()
	if err := seedSQLiteAdminChatAPIKey(ctx, dbRoute, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err == nil {
		t.Fatal("route Scan 失败必须上抛")
	}
}

// TestW9ASeedSQLiteRepairParseError 覆盖 repair 的非法 JSON 跳过分支与
// UPDATE 失败分支。
func TestW9ASeedSQLiteRepairParseError(t *testing.T) {
	ctx := context.Background()
	recBad := &wmSchemaRecorder{}
	w9aScriptRowsFor(recBad, sqSeedProfileAccountTypesSelect, len(pgSeedProfiles), []driver.Value{"{bad"})
	dbBad := openWMSchemaFakeDB(recBad)
	defer dbBad.Close()
	var executed []string
	if err := seedSQLiteRepairProfileAccountTypes(ctx, dbBad, func(query string, _ ...any) error {
		executed = append(executed, query)
		return nil
	}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("parse 失败必须跳过: %v", err)
	}
	if len(executed) != 0 {
		t.Fatalf("parse 失败路径不应执行 UPDATE: %d", len(executed))
	}

	recMerge := &wmSchemaRecorder{}
	w9aScriptRowsFor(recMerge, sqSeedProfileAccountTypesSelect, len(pgSeedProfiles), []driver.Value{"[]"})
	dbMerge := openWMSchemaFakeDB(recMerge)
	defer dbMerge.Close()
	if err := seedSQLiteRepairProfileAccountTypes(ctx, dbMerge, w9aCountingExec(nil, 0), "2026-09-01T00:00:00.000Z"); err == nil {
		t.Fatal("UPDATE 失败必须上抛")
	}
}

// TestW9ASeedSQLiteStaleDisableExecError 覆盖 stale disable 的 UPDATE 失败
// 分支。
func TestW9ASeedSQLiteStaleDisableExecError(t *testing.T) {
	rec := &wmSchemaRecorder{}
	rec.script(sqSeedGeneratedModelRowsSelect, nil, [][]driver.Value{{"provider_model_stale", "glm", "removed-model"}})
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	err := seedSQLiteDisableStaleGeneratedModels(context.Background(), db, w9aCountingExec(nil, 0), "2026-09-01T00:00:00.000Z", map[string]bool{})
	if err == nil {
		t.Fatal("stale disable 失败必须上抛")
	}
}

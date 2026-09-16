package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

var errW9AExecFailed = errors.New("w9a 第 N 次执行失败")

// w9aCountingExec 返回一个前 successCount 次成功、之后失败的 exec 闭包，并
// 记录已执行的语句。
func w9aCountingExec(record *[]string, successCount int) func(string, ...any) error {
	calls := 0
	return func(query string, _ ...any) error {
		calls++
		if calls > successCount {
			return errW9AExecFailed
		}
		if record != nil {
			*record = append(*record, query)
		}
		return nil
	}
}

// w9aScriptRowsFor 把一条行数据登记 n 次。
func w9aScriptRowsFor(rec *wmSchemaRecorder, match string, n int, row []driver.Value) {
	for i := 0; i < n; i++ {
		rec.script(match, nil, [][]driver.Value{row})
	}
}

func TestW9ASeedFirstStatementFailuresOnClosedDB(t *testing.T) {
	ctx := context.Background()

	pg := openWMSchemaFakeDB(&wmSchemaRecorder{})
	pg.Close()
	if _, err := SeedPostgresDefaults(ctx, pg, SeedOptions{}); err == nil || !strings.Contains(err.Error(), "postgres seed statement 1") {
		t.Fatalf("PG 首条失败必须带定位: %v", err)
	}
	if _, err := EnsurePostgresSeeds(ctx, pg); err == nil || !strings.Contains(err.Error(), "postgres seed statement 1") {
		t.Fatalf("EnsurePostgresSeeds 首条失败必须带定位: %v", err)
	}

	sqlite := openWMSchemaFakeDB(&wmSchemaRecorder{})
	sqlite.Close()
	if _, err := SeedSQLiteDefaults(ctx, sqlite, SeedOptions{}); err == nil || !strings.Contains(err.Error(), "sqlite seed statement 1") {
		t.Fatalf("SQLite 首条失败必须带定位: %v", err)
	}
}

// TestW9ASeedPostgresRepairUpdateError 覆盖 repair 函数的 UPDATE 失败分支。
func TestW9ASeedPostgresRepairUpdateError(t *testing.T) {
	rec := &wmSchemaRecorder{}
	w9aScriptRowsFor(rec, pgSeedProfileAccountTypesSelect, len(pgSeedProfiles), []driver.Value{"[]"})
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	err := seedPostgresRepairProfileAccountTypes(context.Background(), &w9aRowsAffectedErrClient{db: db},
		w9aCountingExec(nil, 0), "2026-09-01T00:00:00.000Z")
	if err == nil {
		t.Fatal("UPDATE 失败必须返回错误")
	}
}

// TestW9ASeedPostgresRouteStrategyErrorBranches 逐分支注入，覆盖默认路由
// 策略创建链的全部错误包装。
func TestW9ASeedPostgresRouteStrategyErrorBranches(t *testing.T) {
	ctx := context.Background()
	now := "2026-09-01T00:00:00.000Z"
	groupRow := []driver.Value{"grp_gpt_default", "gpt默认分组"}
	strategyID := defaultRouteStrategyIDForGroup("grp_gpt_default")

	// group select 非法值 → Scan 错误（非 ErrNoRows）。
	recScan := &wmSchemaRecorder{}
	recScan.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{{nil, nil}})
	dbScan := openWMSchemaFakeDB(recScan)
	defer dbScan.Close()
	err := seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx, &w9aRowsAffectedErrClient{db: dbScan}, w9aCountingExec(nil, 99), SeedOptions{}, now)
	if err == nil || !strings.Contains(err.Error(), "select admin default group") {
		t.Fatalf("group select 失败必须被包装: %v", err)
	}

	// route strategy insert 失败。
	recInsert := &wmSchemaRecorder{}
	recInsert.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{groupRow})
	dbInsert := openWMSchemaFakeDB(recInsert)
	defer dbInsert.Close()
	err = seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx, &w9aRowsAffectedErrClient{db: dbInsert}, w9aCountingExec(nil, 0), SeedOptions{}, now)
	if err == nil || !strings.Contains(err.Error(), errW9AExecFailed.Error()) {
		t.Fatalf("route strategy insert 失败必须上抛: %v", err)
	}

	// strategy select Scan 失败。
	recStrategy := &wmSchemaRecorder{}
	recStrategy.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{groupRow})
	recStrategy.script(pgSeedAdminRouteStrategySelect, nil, [][]driver.Value{{nil}})
	dbStrategy := openWMSchemaFakeDB(recStrategy)
	defer dbStrategy.Close()
	err = seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx, &w9aRowsAffectedErrClient{db: dbStrategy}, w9aCountingExec(nil, 99), SeedOptions{}, now)
	if err == nil || !strings.Contains(err.Error(), "select admin route strategy") {
		t.Fatalf("strategy select 失败必须被包装: %v", err)
	}

	// group binding insert 失败。
	recBinding := &wmSchemaRecorder{}
	recBinding.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{groupRow})
	recBinding.script(pgSeedAdminRouteStrategySelect, nil, [][]driver.Value{{strategyID}})
	dbBinding := openWMSchemaFakeDB(recBinding)
	defer dbBinding.Close()
	err = seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx, &w9aRowsAffectedErrClient{db: dbBinding}, w9aCountingExec(nil, 1), SeedOptions{}, now)
	if err == nil {
		t.Fatalf("binding insert 失败必须上抛: %v", err)
	}

	// existing api key select Scan 失败。
	recKeyScan := &wmSchemaRecorder{}
	recKeyScan.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{groupRow})
	recKeyScan.script(pgSeedAdminRouteStrategySelect, nil, [][]driver.Value{{strategyID}})
	recKeyScan.script(pgSeedAdminExistingDefaultAPIKeySelect, nil, [][]driver.Value{{nil}})
	dbKeyScan := openWMSchemaFakeDB(recKeyScan)
	defer dbKeyScan.Close()
	err = seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx, &w9aRowsAffectedErrClient{db: dbKeyScan}, w9aCountingExec(nil, 99), SeedOptions{}, now)
	if err == nil || !strings.Contains(err.Error(), "select existing default api key") {
		t.Fatalf("existing key select 失败必须被包装: %v", err)
	}

	// default api key insert 失败。
	recKeyInsert := &wmSchemaRecorder{}
	recKeyInsert.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{groupRow})
	recKeyInsert.script(pgSeedAdminRouteStrategySelect, nil, [][]driver.Value{{strategyID}})
	dbKeyInsert := openWMSchemaFakeDB(recKeyInsert)
	defer dbKeyInsert.Close()
	err = seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx, &w9aRowsAffectedErrClient{db: dbKeyInsert}, w9aCountingExec(nil, 2), SeedOptions{}, now)
	if err == nil {
		t.Fatalf("api key insert 失败必须上抛: %v", err)
	}
}

// TestW9ASeedPostgresChatKeyErrorBranches 覆盖 chat key 的检查/选择错误分支
// 与插入失败。
func TestW9ASeedPostgresChatKeyErrorBranches(t *testing.T) {
	ctx := context.Background()
	now := "2026-09-01T00:00:00.000Z"

	// exists select Scan 失败。
	recExists := &wmSchemaRecorder{}
	recExists.script(pgSeedAdminChatKeyExistsSelect, nil, [][]driver.Value{{nil}})
	dbExists := openWMSchemaFakeDB(recExists)
	defer dbExists.Close()
	err := seedPostgresAdminChatAPIKey(ctx, &w9aRowsAffectedErrClient{db: dbExists}, w9aCountingExec(nil, 99), SeedOptions{}, now)
	if err == nil || !strings.Contains(err.Error(), "check chat api key") {
		t.Fatalf("chat key check 失败必须被包装: %v", err)
	}

	// default group select Scan 失败。
	recGroup := &wmSchemaRecorder{}
	recGroup.script(pgSeedAdminChatKeyDefaultGroupSelect, nil, [][]driver.Value{{nil}})
	dbGroup := openWMSchemaFakeDB(recGroup)
	defer dbGroup.Close()
	err = seedPostgresAdminChatAPIKey(ctx, &w9aRowsAffectedErrClient{db: dbGroup}, w9aCountingExec(nil, 99), SeedOptions{}, now)
	if err == nil || !strings.Contains(err.Error(), "select chat key default group") {
		t.Fatalf("default group select 失败必须被包装: %v", err)
	}

	// route select Scan 失败。
	recRoute := &wmSchemaRecorder{}
	recRoute.script(pgSeedAdminChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
	recRoute.script(pgSeedAdminChatKeyRouteSelect, nil, [][]driver.Value{{nil, nil}})
	dbRoute := openWMSchemaFakeDB(recRoute)
	defer dbRoute.Close()
	err = seedPostgresAdminChatAPIKey(ctx, &w9aRowsAffectedErrClient{db: dbRoute}, w9aCountingExec(nil, 99), SeedOptions{}, now)
	if err == nil || !strings.Contains(err.Error(), "select chat key route strategy") {
		t.Fatalf("route select 失败必须被包装: %v", err)
	}

	// chat api key insert 失败。
	recInsert := &wmSchemaRecorder{}
	recInsert.script(pgSeedAdminChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
	recInsert.script(pgSeedAdminChatKeyRouteSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default", "gpt默认路由"}})
	dbInsert := openWMSchemaFakeDB(recInsert)
	defer dbInsert.Close()
	err = seedPostgresAdminChatAPIKey(ctx, &w9aRowsAffectedErrClient{db: dbInsert}, w9aCountingExec(nil, 0), SeedOptions{}, now)
	if err == nil {
		t.Fatalf("chat key insert 失败必须上抛: %v", err)
	}
}

// TestW9ASeedPostgresExternalTokenSelectError 覆盖 token select 的非
// ErrNoRows 失败分支（exec 成功、查询失败）。
func TestW9ASeedPostgresExternalTokenSelectError(t *testing.T) {
	closed := openWMSchemaFakeDB(&wmSchemaRecorder{})
	closed.Close()
	err := seedPostgresExternalIntegrationTestToken(context.Background(), &w9aRowsAffectedErrClient{db: closed},
		w9aCountingExec(nil, 99), SeedOptions{}, "2026-09-01T00:00:00.000Z")
	if err == nil || !strings.Contains(err.Error(), "select external integration token") {
		t.Fatalf("token select 失败必须被包装: %v", err)
	}
}

// TestW9ASeedPostgresDefaultsWithFullScriptInjectFailures 在完整脚本链下逐条
// 注入失败，覆盖 seedPostgresDefaults 中依赖查询结果的错误分支。
func TestW9ASeedPostgresDefaultsWithFullScriptInjectFailures(t *testing.T) {
	buildRec := func() *wmSchemaRecorder {
		rec := &wmSchemaRecorder{}
		w9aScriptRowsFor(rec, pgSeedProfileAccountTypesSelect, len(pgSeedProfiles), []driver.Value{"[]"})
		for _, groupSeed := range pgSeedGroups {
			if groupSeed.SystemAccountID != "sys_admin" || groupSeed.ProviderCode == hybridProviderCode {
				continue
			}
			rec.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{{"grp_" + groupSeed.ProviderCode + "_default", groupSeed.Name + "分组"}})
			rec.script(pgSeedAdminRouteStrategySelect, nil, [][]driver.Value{{defaultRouteStrategyIDForGroup("grp_" + groupSeed.ProviderCode + "_default")}})
		}
		rec.script(pgSeedAdminChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
		rec.script(pgSeedAdminChatKeyRouteSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default", "gpt默认路由"}})
		return rec
	}

	for failAfter := 1; failAfter <= 200; failAfter++ {
		rec := buildRec()
		db := openWMSchemaFakeDB(rec)
		_, err := SeedPostgresDefaults(context.Background(), db, SeedOptions{})
		db.Close()
		if err == nil {
			continue
		}
		if !strings.Contains(err.Error(), "postgres seed statement") && !strings.Contains(err.Error(), errW9AExecFailed.Error()) {
			t.Fatalf("failAfter=%d 错误异常: %v", failAfter, err)
		}
	}
}

// TestW9AEnsurePostgresSeedsRepairUpdateInjectFailures 在 repair 合并路径下
// 逐条注入失败，覆盖 EnsurePostgresSeeds 的 repair UPDATE 错误分支。
func TestW9AEnsurePostgresSeedsRepairUpdateInjectFailures(t *testing.T) {
	for failAfter := 1; failAfter <= 200; failAfter++ {
		rec := &wmSchemaRecorder{failExecAfter: failAfter}
		w9aScriptRowsFor(rec, pgSeedProfileAccountTypesSelect, len(pgSeedProfiles), []driver.Value{"[]"})
		db := openWMSchemaFakeDB(rec)
		_, err := EnsurePostgresSeeds(context.Background(), db)
		db.Close()
		if err != nil && !strings.Contains(err.Error(), "postgres seed statement") {
			t.Fatalf("failAfter=%d 错误异常: %v", failAfter, err)
		}
	}
}

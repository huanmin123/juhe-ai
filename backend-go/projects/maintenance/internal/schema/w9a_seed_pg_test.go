package schema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

var errW9AInjected = errors.New("w9a 注入失败")

// w9aRowsAffectedErrResult 让 RowsAffected 失败，覆盖 stale disable 的计数
// 错误分支。
type w9aRowsAffectedErrResult struct{}

func (w9aRowsAffectedErrResult) LastInsertId() (int64, error) { return 0, errW9AInjected }
func (w9aRowsAffectedErrResult) RowsAffected() (int64, error) { return 0, errW9AInjected }

// w9aRowsAffectedErrClient 只让 Exec 返回失败计数的 Result，Query 透传。
type w9aRowsAffectedErrClient struct {
	db *sql.DB
}

func (c *w9aRowsAffectedErrClient) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return w9aRowsAffectedErrResult{}, nil
}

func (c *w9aRowsAffectedErrClient) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.db.QueryRowContext(ctx, query, args...)
}

// w9aSeedStatementCount 先跑一遍成功路径，取总语句数。
func w9aSeedStatementCount(t *testing.T, seed func(rec *wmSchemaRecorder) error) int {
	t.Helper()
	rec := &wmSchemaRecorder{}
	if err := seed(rec); err != nil {
		t.Fatalf("成功路径失败: %v", err)
	}
	return rec.execCount()
}

// TestW9ASeedPostgresDefaultsStatementFailures 逐条注入执行失败，覆盖
// seedPostgresDefaults 每个语句错误包装。
func TestW9ASeedPostgresDefaultsStatementFailures(t *testing.T) {
	seed := func(rec *wmSchemaRecorder) error {
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		_, err := SeedPostgresDefaults(context.Background(), db, SeedOptions{Now: func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }})
		return err
	}
	total := w9aSeedStatementCount(t, seed)
	if total < 10 {
		t.Fatalf("成功路径语句数异常: %d", total)
	}
	for failAfter := 1; failAfter <= total-1; failAfter++ {
		rec := &wmSchemaRecorder{failExecAfter: failAfter}
		db := openWMSchemaFakeDB(rec)
		_, err := SeedPostgresDefaults(context.Background(), db, SeedOptions{})
		db.Close()
		if err == nil {
			t.Fatalf("failExecAfter=%d 必须失败", failAfter)
		}
		if !strings.Contains(err.Error(), "postgres seed statement") {
			t.Fatalf("failExecAfter=%d 错误必须带语句定位: %v", failAfter, err)
		}
	}
}

// TestW9ASeedPostgresModelCatalogRowsAffectedFailure 覆盖 stale disable 计数
// 错误分支。
func TestW9ASeedPostgresModelCatalogRowsAffectedFailure(t *testing.T) {
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	client := &w9aRowsAffectedErrClient{db: db}
	result := PGSeedResult{}
	err := seedPostgresModelCatalog(context.Background(), client, func(string, ...any) error { return nil },
		SeedOptions{}, seedTimestamp(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)), &result)
	if err == nil || !strings.Contains(err.Error(), "rows affected") {
		t.Fatalf("RowsAffected 失败必须被包装: %v", err)
	}
}

// TestW9ASeedPostgresRepairProfileAccountTypes 覆盖 profile account-types 修复
// 的三条分支：ErrNoRows 跳过、非法 JSON 跳过、需要合并时执行 UPDATE。
func TestW9ASeedPostgresRepairProfileAccountTypes(t *testing.T) {
	// ErrNoRows（默认零行）与非法 JSON 都走跳过分支。
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	if err := seedPostgresRepairProfileAccountTypes(context.Background(), &w9aRowsAffectedErrClient{db: db},
		func(string, ...any) error { return nil }, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if rec.execCount() != 0 {
		t.Fatalf("跳过路径不应执行 UPDATE: %d", rec.execCount())
	}

	// 合并路径：stored 为空数组 → 全部内置 account types 需要合并 → UPDATE。
	recMerge := &wmSchemaRecorder{}
	for range pgSeedProfiles {
		recMerge.script(pgSeedProfileAccountTypesSelect, nil, [][]driver.Value{{"[]"}})
	}
	dbMerge := openWMSchemaFakeDB(recMerge)
	defer dbMerge.Close()
	var mergedUpdates []string
	if err := seedPostgresRepairProfileAccountTypes(context.Background(), &w9aRowsAffectedErrClient{db: dbMerge},
		func(query string, _ ...any) error { mergedUpdates = append(mergedUpdates, query); return nil },
		"2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("repair merge: %v", err)
	}
	if got := len(mergedUpdates); got != len(pgSeedProfiles) {
		t.Fatalf("merge 路径 UPDATE 次数=%d want %d", got, len(pgSeedProfiles))
	}

	// 相同 account types → equal → 跳过。
	recEqual := &wmSchemaRecorder{}
	for _, profile := range pgSeedProfiles {
		recEqual.script(pgSeedProfileAccountTypesSelect, nil, [][]driver.Value{{mustJSON(profile.AccountTypes)}})
	}
	dbEqual := openWMSchemaFakeDB(recEqual)
	defer dbEqual.Close()
	if err := seedPostgresRepairProfileAccountTypes(context.Background(), &w9aRowsAffectedErrClient{db: dbEqual},
		func(string, ...any) error { return nil }, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("repair equal: %v", err)
	}
	if got := recEqual.execCount(); got != 0 {
		t.Fatalf("equal 路径不应执行 UPDATE: %d", got)
	}
}

// TestW9ASeedPostgresAdminRouteStrategiesSuccessPath 通过脚本化查询结果覆盖
// 默认路由策略/API Key/对话 Key/外部集成 Token 的完整创建链。
func TestW9ASeedPostgresAdminRouteStrategiesSuccessPath(t *testing.T) {
	rec := &wmSchemaRecorder{}
	now := "2026-09-01T00:00:00.000Z"
	for _, groupSeed := range pgSeedGroups {
		if groupSeed.SystemAccountID != "sys_admin" || groupSeed.ProviderCode == hybridProviderCode {
			continue
		}
		rec.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{{"grp_" + groupSeed.ProviderCode + "_default", groupSeed.Name + "分组"}})
		rec.script(pgSeedAdminRouteStrategySelect, nil, [][]driver.Value{{defaultRouteStrategyIDForGroup("grp_" + groupSeed.ProviderCode + "_default")}})
		// existing default api key select 不登记 → ErrNoRows → 创建新 Key。
	}
	// chat key：不存在 → 选默认分组 → 选路由 → 创建。
	rec.script(pgSeedAdminChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
	rec.script(pgSeedAdminChatKeyRouteSelect, nil, [][]driver.Value{{"route_strategy_gpt_default", "gpt默认路由"}})
	// external integration token：不存在 → 创建。

	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	var executed []string
	exec := func(query string, _ ...any) error {
		executed = append(executed, query)
		return nil
	}
	if err := seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(context.Background(), &w9aRowsAffectedErrClient{db: db}, exec, SeedOptions{}, now); err != nil {
		t.Fatalf("route strategies: %v", err)
	}
	if err := seedPostgresAdminChatAPIKey(context.Background(), &w9aRowsAffectedErrClient{db: db}, exec, SeedOptions{}, now); err != nil {
		t.Fatalf("chat api key: %v", err)
	}
	if err := seedPostgresExternalIntegrationTestToken(context.Background(), &w9aRowsAffectedErrClient{db: db}, exec, SeedOptions{}, now); err != nil {
		t.Fatalf("external integration token: %v", err)
	}

	joined := strings.Join(executed, "\n")
	if !strings.Contains(joined, pgSeedAdminRouteStrategyInsert) {
		t.Fatal("必须插入默认路由策略")
	}
	if !strings.Contains(joined, pgSeedAdminDefaultAPIKeyInsert) {
		t.Fatal("必须插入默认 API Key")
	}
	if !strings.Contains(joined, pgSeedAdminChatAPIKeyInsert) {
		t.Fatal("必须插入对话 API Key")
	}
	if !strings.Contains(joined, pgSeedExternalIntegrationTokenInsert) {
		t.Fatal("必须插入外部集成测试 Token")
	}
}

// TestW9ASeedPostgresAdminRouteStrategySelectNoRows 覆盖 select ErrNoRows 的
// 跳过分支（group 命中但 strategy select 返回零行）。
func TestW9ASeedPostgresAdminRouteStrategySelectNoRows(t *testing.T) {
	rec := &wmSchemaRecorder{}
	for _, groupSeed := range pgSeedGroups {
		if groupSeed.SystemAccountID != "sys_admin" || groupSeed.ProviderCode == hybridProviderCode {
			continue
		}
		rec.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{{"grp_x", "x分组"}})
	}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	exec := func(string, ...any) error { return nil }
	if err := seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(context.Background(), &w9aRowsAffectedErrClient{db: db}, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("ErrNoRows 跳过必须不报错: %v", err)
	}
}

// TestW9ASeedPostgresChatKeyExistingPaths 覆盖 chat key 已存在（返回值）与
// 默认分组缺失（ErrNoRows → return nil）分支。
func TestW9ASeedPostgresChatKeyExistingPaths(t *testing.T) {
	// 已存在 → 直接返回。
	recExists := &wmSchemaRecorder{}
	recExists.script(pgSeedAdminChatKeyExistsSelect, nil, [][]driver.Value{{"key_chat_sys_admin"}})
	dbExists := openWMSchemaFakeDB(recExists)
	defer dbExists.Close()
	exec := func(string, ...any) error { return nil }
	if err := seedPostgresAdminChatAPIKey(context.Background(), &w9aRowsAffectedErrClient{db: dbExists}, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("existing chat key: %v", err)
	}

	// 不存在且默认分组缺失 → return nil。
	recMissing := &wmSchemaRecorder{}
	dbMissing := openWMSchemaFakeDB(recMissing)
	defer dbMissing.Close()
	if err := seedPostgresAdminChatAPIKey(context.Background(), &w9aRowsAffectedErrClient{db: dbMissing}, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("missing default group: %v", err)
	}

	// 分组存在但路由策略缺失 → return nil。
	recNoRoute := &wmSchemaRecorder{}
	recNoRoute.script(pgSeedAdminChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
	dbNoRoute := openWMSchemaFakeDB(recNoRoute)
	defer dbNoRoute.Close()
	if err := seedPostgresAdminChatAPIKey(context.Background(), &w9aRowsAffectedErrClient{db: dbNoRoute}, exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("missing route strategy: %v", err)
	}
}

// TestW9ASeedPostgresExternalTokenUpdatePath 覆盖 token 已存在的 UPDATE 分支。
func TestW9ASeedPostgresExternalTokenUpdatePath(t *testing.T) {
	rec := &wmSchemaRecorder{}
	rec.script(pgSeedExternalIntegrationTokenSelect, nil, [][]driver.Value{{externalIntegrationTestTokenID}})
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	var executed []string
	exec := func(query string, _ ...any) error {
		executed = append(executed, query)
		return nil
	}
	if err := seedPostgresExternalIntegrationTestToken(context.Background(), &w9aRowsAffectedErrClient{db: db},
		exec, SeedOptions{}, "2026-09-01T00:00:00.000Z"); err != nil {
		t.Fatalf("token update path: %v", err)
	}
	if len(executed) == 0 || !strings.Contains(executed[len(executed)-1], pgSeedExternalIntegrationTokenUpdate) {
		t.Fatalf("最后一条应是 token UPDATE: %v", executed)
	}
}

// TestW9AEnsurePostgresSeedsStatementFailures 逐条注入失败，覆盖
// EnsurePostgresSeeds 全部错误包装与 repair 跳过分支。
func TestW9AEnsurePostgresSeedsStatementFailures(t *testing.T) {
	total := w9aSeedStatementCount(t, func(rec *wmSchemaRecorder) error {
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		_, err := EnsurePostgresSeeds(context.Background(), db)
		return err
	})
	for failAfter := 1; failAfter <= total-1; failAfter++ {
		rec := &wmSchemaRecorder{failExecAfter: failAfter}
		db := openWMSchemaFakeDB(rec)
		_, err := EnsurePostgresSeeds(context.Background(), db)
		db.Close()
		if err == nil || !strings.Contains(err.Error(), "postgres seed statement") {
			t.Fatalf("failExecAfter=%d 错误异常: %v", failAfter, err)
		}
	}
}

// TestW9AEnsurePostgresSeedsRepairJSONBranches 覆盖修复循环的非法 JSON 与
// equal 跳过分支。
func TestW9AEnsurePostgresSeedsRepairJSONBranches(t *testing.T) {
	// 非法 JSON → 解析失败跳过，无 UPDATE。
	recBad := &wmSchemaRecorder{}
	for range pgSeedProfiles {
		recBad.script(pgSeedProfileAccountTypesSelect, nil, [][]driver.Value{{"{bad"}})
	}
	dbBad := openWMSchemaFakeDB(recBad)
	defer dbBad.Close()
	if _, err := EnsurePostgresSeeds(context.Background(), dbBad); err != nil {
		t.Fatalf("repair bad json: %v", err)
	}
	if recBad.execCount() == 0 {
		t.Fatal("基础语句必须执行")
	}

	// equal → 跳过 UPDATE。
	recEqual := &wmSchemaRecorder{}
	for _, profile := range pgSeedProfiles {
		recEqual.script(pgSeedProfileAccountTypesSelect, nil, [][]driver.Value{{mustJSON(profile.AccountTypes)}})
	}
	dbEqual := openWMSchemaFakeDB(recEqual)
	defer dbEqual.Close()
	result, err := EnsurePostgresSeeds(context.Background(), dbEqual)
	if err != nil {
		t.Fatalf("repair equal: %v", err)
	}
	if result.StatementCount <= 0 {
		t.Fatalf("StatementCount 必须为正: %d", result.StatementCount)
	}
}

// TestW9AEnsurePostgresDDLAndSchemaFailures 覆盖 EnsurePostgres 的 schema
// 创建失败与 DDL 执行失败分支。
func TestW9AEnsurePostgresDDLAndSchemaFailures(t *testing.T) {
	rec := &wmSchemaRecorder{failExecAfter: 8}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	_, err := EnsurePostgres(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "postgres schema statement") {
		t.Fatalf("DDL 失败必须带定位: %v", err)
	}

	// 关闭的连接让第一条 CREATE SCHEMA 失败，覆盖 schema 创建错误包装。
	closed := openWMSchemaFakeDB(&wmSchemaRecorder{})
	closed.Close()
	if _, err := EnsurePostgres(context.Background(), closed); err == nil ||
		!strings.Contains(err.Error(), "create postgres schema") {
		t.Fatalf("schema 创建失败必须被包装: %v", err)
	}
}

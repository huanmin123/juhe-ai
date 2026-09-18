package statsverify

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w12b_units2_test.go 补充 statsverify 剩余分支：join 行扫描的类型转换
// 错误臂（无类型 scratch 表绕过列亲和性）、按组脏协议的读取/删除注入、
// 缺失账户跳过臂、job 入口 nil Clock 与窗口刷新注入、LoadUsageStatsTimezone
// 空值臂、nil store 防御与 OpenStore 建表失败。
// 不可达登记见 w12b_failinject_test.go 头注释。

func TestW12bSvJoinRowScanTyped(t *testing.T) {
	ctx := context.Background()
	store, _, _ := w12bSvOpenStore(t)
	// 无类型列（BLOB 亲和性）：保留原生类型以直达 scan 转换错误臂。
	mustExec(t, ctx, store.db, "CREATE TABLE w12b_scratch (group_id, account_id, account_authorization_id, group_system_account_id, account_system_account_id, status, schedulable, cooldown_until, concurrency_limit, authorization_status, authorization_expires_at)")

	scan := func(values ...any) error {
		placeholders := make([]string, 0, len(values))
		anyArgs := make([]any, 0, len(values))
		for range values {
			placeholders = append(placeholders, "?")
		}
		for _, value := range values {
			anyArgs = append(anyArgs, value)
		}
		mustExec(t, ctx, store.db, "DELETE FROM w12b_scratch")
		mustExec(t, ctx, store.db, "INSERT INTO w12b_scratch VALUES ("+strings.Join(placeholders, ", ")+")", anyArgs...)
		rows, err := store.db.QueryContext(ctx, "SELECT group_id, account_id, account_authorization_id, group_system_account_id, account_system_account_id, status, schedulable, cooldown_until, concurrency_limit, authorization_status, authorization_expires_at FROM w12b_scratch")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("应有行")
		}
		_, err = scanGroupAccountStatsJoinRow(rows)
		return err
	}

	if err := scan("g", "a", "auth", "sys", "sys", "active", 1, "2026-09-17T00:00:00.000Z", 5, "active", "2026-10-17T00:00:00.000Z"); err != nil {
		t.Fatalf("合法行应通过: %v", err)
	}
	if err := scan("g", 1, nil, "sys", nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("account_id 数值应报错")
	}
	if err := scan("g", nil, 2, "sys", nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("authorization_id 数值应报错")
	}
	if err := scan("g", nil, nil, "sys", 3, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("account system_account_id 数值应报错")
	}
	if err := scan("g", nil, nil, "sys", nil, 4, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("status 数值应报错")
	}
	if err := scan("g", nil, nil, "sys", nil, nil, "w12b-x", nil, nil, nil, nil); err == nil {
		t.Fatal("schedulable 文本应报错")
	}
	if err := scan("g", nil, nil, "sys", nil, nil, nil, 7, nil, nil, nil); err == nil {
		t.Fatal("cooldown 数值应报错")
	}
	if err := scan("g", nil, nil, "sys", nil, nil, nil, nil, "w12b-y", nil, nil); err == nil {
		t.Fatal("concurrency 文本应报错")
	}
	if err := scan("g", nil, nil, "sys", nil, nil, nil, nil, nil, 8, nil); err == nil {
		t.Fatal("authorization status 数值应报错")
	}
	if err := scan("g", nil, nil, "sys", nil, nil, nil, nil, nil, nil, 9); err == nil {
		t.Fatal("authorization expires 数值应报错")
	}
}

func TestW12bSvGroupStatsExtraArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// limit 上限截断。
	store, _, _ := w12bSvOpenStore(t)
	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Limit: 5000, Now: now}); err != nil {
		t.Fatalf("超限 limit 应被截断并成功: %v", err)
	}

	// 按组脏行读取注入、初始构建后 __all__ 读取注入与删除注入（直调确定性覆盖）。
	store2, _, businessSpec2 := w12bSvOpenStore(t)
	seedGroupFixture(t, ctx, store2)
	businessSpec2.arm("WHERE group_id <>")
	if _, err := store2.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err == nil {
		t.Fatal("按组脏行读取注入应报错")
	}
	businessSpec2.armOnce("WHERE group_id = ")
	if _, err := store2.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err == nil {
		t.Fatal("初始构建后的 __all__ 读取注入应报错")
	}
	businessSpec2.disarm()
	mustExec(t, ctx, store2.business, "INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g1', 'w12b', '2026-09-17T12:00:00.000Z')")
	tx, err := store2.business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store2.loadGroupAccountStatsDirtyRows(ctx, tx, 10)
	if err != nil {
		t.Fatal(err)
	}
	businessSpec2.arm("DELETE FROM group_account_stats_dirty")
	if err := store2.deleteGroupAccountStatsDirtyRows(ctx, tx, rows); err == nil {
		t.Fatal("删除注入应报错")
	}
	businessSpec2.disarm()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// join 行扫描错误臂：schedulable / concurrency_limit 文本落在 INTEGER 列。
	store3, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store3.business, "INSERT INTO groups (id, system_account_id) VALUES ('g9', 'sys')")
	mustExec(t, ctx, store3.business, "INSERT INTO accounts (id, system_account_id, status, schedulable, concurrency_limit) VALUES ('a9', 'sys', 'active', 'w12b-x', 1)")
	mustExec(t, ctx, store3.business, "INSERT INTO group_accounts (group_id, account_id, account_authorization_id, enabled) VALUES ('g9', 'a9', NULL, 1)")
	mustExec(t, ctx, store3.business, "INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g9', 'w12b', '2026-09-17T12:00:00.000Z')")
	if _, err := store3.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err == nil {
		t.Fatal("schedulable 文本应使 join 扫描失败")
	}
	store4, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store4.business, "INSERT INTO groups (id, system_account_id) VALUES ('g9', 'sys')")
	mustExec(t, ctx, store4.business, "INSERT INTO accounts (id, system_account_id, status, schedulable, concurrency_limit) VALUES ('a9', 'sys', 'active', 1, 'w12b-y')")
	mustExec(t, ctx, store4.business, "INSERT INTO group_accounts (group_id, account_id, account_authorization_id, enabled) VALUES ('g9', 'a9', NULL, 1)")
	mustExec(t, ctx, store4.business, "INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g9', 'w12b', '2026-09-17T12:00:00.000Z')")
	if _, err := store4.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err == nil {
		t.Fatal("concurrency_limit 文本应使 join 扫描失败")
	}

	// 缺失账户的 join 行（LEFT JOIN NULL account_id）跳过臂。
	store5, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store5.business, "INSERT INTO groups (id, system_account_id) VALUES ('g8', 'sys')")
	mustExec(t, ctx, store5.business, "INSERT INTO group_accounts (group_id, account_id, account_authorization_id, enabled) VALUES ('g8', 'missing', NULL, 1)")
	mustExec(t, ctx, store5.business, "INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g8', 'w12b', '2026-09-17T12:00:00.000Z')")
	if _, err := store5.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err != nil {
		t.Fatalf("缺失账户应跳过并成功: %v", err)
	}
	if got := queryInt(t, ctx, store5.db, "SELECT total FROM group_account_stats WHERE group_id = 'g8'"); got != 0 {
		t.Fatalf("缺失账户不应计数: %d", got)
	}
}

func TestW12bSvJobsAndStoreExtraArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// nil Clock 走 SystemClock + 窗口刷新注入。
	store, statsSpec, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store.business, "INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '\"UTC\"')")
	statsSpec.arm("FROM usage_records")
	if _, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{IngestGate: w12bStubGate{}}); err == nil {
		t.Fatal("nil Clock 聚合注入应报错")
	}
	statsSpec.disarm()
	statsSpec.arm("client_ip_range_window_dirty_ips")
	if _, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{IngestGate: w12bStubGate{}, Clock: NewFixedClock(now)}); err == nil {
		t.Fatal("窗口刷新注入应报错")
	}
	statsSpec.disarm()

	// 前两步的聚合臂在 LoadUsageStatsLocation 之后才触发，已用真实 SystemClock
	// 把 "UTC" 写进时区缓存；固定时钟的 now 一旦被真实时钟追上，缓存会误判
	// 仍有效。显式清缓存，保证后续空值臂真正落到 DB 读取（可重放）。
	store.tzMu.Lock()
	store.tzValue = ""
	store.tzExpiresAt = time.Time{}
	store.tzMu.Unlock()

	// LoadUsageStatsTimezone 空值臂与空时区臂。
	mustExec(t, ctx, store.business, "UPDATE system_settings SET value_json = '' WHERE key = 'usageStatsTimezone'")
	if _, err := store.LoadUsageStatsTimezone(ctx, now.Add(2*UsageStatsTimezoneCacheTTL)); err == nil {
		t.Fatal("空 value_json 应报错")
	}
	mustExec(t, ctx, store.business, "UPDATE system_settings SET value_json = '\"\"' WHERE key = 'usageStatsTimezone'")
	if _, err := store.LoadUsageStatsTimezone(ctx, now.Add(4*UsageStatsTimezoneCacheTTL)); err == nil {
		t.Fatal("空时区应报错")
	}
	if _, _, err := store.LoadUsageStatsLocation(ctx, now.Add(6*UsageStatsTimezoneCacheTTL)); err == nil {
		t.Fatal("空时区 Location 应报错")
	}
	// 收尾清缓存，避免污染同进程内其他用例对缓存语义的假设。
	store.tzMu.Lock()
	store.tzValue = ""
	store.tzExpiresAt = time.Time{}
	store.tzMu.Unlock()

	// nil store Close / nil EnsureSchema（OpenStore 的 EnsureSchema 失败臂
	// 经真实 "sqlite" 驱动无注入点，登记不可达）。
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := nilStore.EnsureSchema(ctx); err == nil {
		t.Fatal("nil store EnsureSchema 应报错")
	}
	_ = statsSpec
}

func TestW12bSvWindowReadArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	seed := func(t *testing.T) *Store {
		store, _, _ := w12bSvOpenStore(t)
		mustExec(t, ctx, store.business, "INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '\"UTC\"')")
		return store
	}

	// stale 重建：账户窗口 INSERT 注入（stale 循环先账户后 IP）。
	store := seed(t)
	mustExec(t, ctx, store.db, "INSERT INTO stats_job_state (scope_type, scope_id, job_name, updated_at) VALUES ('client_ip_range_window', '2026-09-17:2026-09-17', 'client_ip_range_window_refresh', '2026-09-17T00:00:00.000Z')")
	statsSpec := w12bSvStatsSpec(t)
	statsSpec.arm("INSERT INTO client_ip_account_usage_range_windows")
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("stale 账户窗口注入应报错")
	}
	statsSpec.disarm()

	// per-hash：usage 窗口清理注入。
	store2 := seed(t)
	mustExec(t, ctx, store2.db, "INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h5', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')")
	mustExec(t, ctx, store2.db, "INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h5', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')")
	statsSpec2 := w12bSvStatsSpec(t)
	statsSpec2.arm("DELETE FROM client_ip_range_window_dirty_ips")
	if err := store2.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("per-hash 清理注入应报错")
	}
	statsSpec2.disarm()

	// readDirtyRows 查询注入：IP 表与账户表各自错误臂。
	store3 := seed(t)
	mustExec(t, ctx, store3.db, "INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h6', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')")
	mustExec(t, ctx, store3.db, "INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h6', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')")
	statsSpec3 := w12bSvStatsSpec(t)
	// readDirtyRows 查询文本含换行缩进，用带真实换行的匹配串区分两表。
	statsSpec3.arm("SELECT ip_hash, generation")
	if err := store3.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("IP 脏行读取注入应报错")
	} else {
		t.Logf("IP 脏行注入 err = %v", err)
	}
	statsSpec3.arm("client_ip_account_range_window_dirty_ips\n\t\t\tWHERE ip_hash IN")
	if err := store3.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("账户脏行读取注入应报错")
	}
	statsSpec3.disarm()

	// AggregateClientIPStatsBatch：nil store 与 batchLimit<1 归一。
	var nilStore *Store
	if _, err := nilStore.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("nil store 聚合应报错")
	}
	store4 := seed(t)
	if _, err := store4.AggregateClientIPStatsBatch(ctx, 0, now); err != nil {
		t.Fatalf("limit<1 应归一为 1: %v", err)
	}
}

func TestW12bSvJobStateBadLagWithTimezone(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store.business, "INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '\"UTC\"')")
	mustExec(t, ctx, store.db, "INSERT INTO stats_job_state (scope_type, scope_id, job_name, lag_seconds, updated_at) VALUES ('global', '', 'client_ip_stats_aggregation', 'abc', '2026-09-17T00:00:00.000Z')")
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("lag_seconds 文本应报错")
	}
}

func TestW12bSvWriteAggregatesBuildError(t *testing.T) {
	ctx := context.Background()
	store, _, _ := w12bSvOpenStore(t)
	// 直调写路径：非法 created_at 在 build 阶段报错（WHERE 词法过滤使该臂
	// 无法经聚合入口触达）。
	bad := []UsageStatsRecordRow{{ID: "w12b-bad", ClientIP: strPtr("1.2.3.4"), CreatedAt: "not-a-time"}}
	if err := store.writeClientIPAggregates(ctx, store.db, bad, "2026-09-17T00:00:00.000Z", time.UTC, time.Now()); err == nil {
		t.Fatal("build 阶段坏时间应报错")
	}
}

func TestW12bSvAccumulatorPanicArms(t *testing.T) {
	// firstSeenAt/lastUsedAt 损坏的 fail-fast 臂（调用方预归一化，直调触达）。
	target := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "w12b-bad-first")
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	})
	target2 := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	target2.lastUsedAt = "w12b-bad-last"
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target2, UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	})
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target2, UsageStatsAccumulator{LastErrorAt: "w12b-bad-error"}, "2026-09-17T10:00:00.000Z")
	})
}

func TestW12bSvRefreshLocationAndRegistryArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// 窗口刷新的时区读取注入。
	store, _, businessSpec := w12bSvOpenStore(t)
	businessSpec.arm("system_settings")
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("窗口刷新时区注入应报错")
	}
	businessSpec.disarm()

	// registryAggregatesFromIPAggregates 的 fail-fast 解析臂。
	base := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	badFirst := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "w12b-bad")
	assertPanics(t, func() { registryAggregatesFromIPAggregates([]*clientIPAggregate{base, badFirst}) })
	good := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "2026-09-17T09:00:00.000Z")
	badLast := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "2026-09-17T08:00:00.000Z")
	badLast.lastUsedAt = "w12b-bad"
	assertPanics(t, func() { registryAggregatesFromIPAggregates([]*clientIPAggregate{good, badLast}) })
	// 正常合并臂。
	older := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "2026-09-17T08:00:00.000Z")
	older.lastUsedAt = "2026-09-17T09:00:00.000Z"
	entries := registryAggregatesFromIPAggregates([]*clientIPAggregate{base, older})
	if len(entries) != 1 || entries[0].firstSeenAt != "2026-09-17T08:00:00.000Z" {
		t.Fatalf("registry 合并不符: %+v", entries)
	}

	// Accumulator lastErrorAt 解析 panic 臂（fresh target，避免提前 panic）。
	fresh := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(fresh, UsageStatsAccumulator{LastErrorAt: "w12b-bad-error"}, "2026-09-17T11:00:00.000Z")
	})

	// OpenStore EnsureSchema 失败臂：预置同名视图让 CREATE TABLE IF NOT EXISTS 失败。
	w12bSvRegisterDrivers()
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "stats.sqlite3")
	businessPath := filepath.Join(dir, "business.sqlite3")
	statsDB, err := sqlOpenHelper(w12bSvStatsDriverName, statsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statsDB.ExecContext(ctx, `CREATE VIEW usage_records AS SELECT 1 AS one`); err != nil {
		t.Fatal(err)
	}
	businessDB, err := sqlOpenHelper(w12bSvBusinessDriverName, businessPath)
	if err != nil {
		t.Fatal(err)
	}
	w12bSvDriversMu.Lock()
	w12bSvDrivers[w12bSvStatsDriverName] = &w12bFailSpec{}
	w12bSvDrivers[w12bSvBusinessDriverName] = &w12bFailSpec{}
	w12bSvDriversMu.Unlock()
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite, SQLiteStatsPath: statsPath, SQLiteBusinessPath: businessPath}); err == nil {
		t.Fatal("同名视图应使建表失败")
	}
	_ = statsDB.Close()
	_ = businessDB.Close()
}

func sqlOpenHelper(driverName, path string) (*sql.DB, error) {
	return sql.Open(driverName, "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
}

func TestW12bSvRemainingArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// nil store 窗口刷新。
	var nilStore *Store
	if err := nilStore.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("nil store 窗口刷新应报错")
	}

	// LoadUsageStatsLocation 的时区错误传播臂。
	store, _, _ := w12bSvOpenStore(t)
	if _, _, err := store.LoadUsageStatsLocation(ctx, now); err == nil {
		t.Fatal("缺时区设置应报错")
	}

	// per-hash 清理注入：IP 脏表 DELETE 命中（clear 第一步即失败）。
	store2, statsSpec2, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store2.business, "INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '\"UTC\"')")
	mustExec(t, ctx, store2.db, "INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h9', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')")
	mustExec(t, ctx, store2.db, "INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h9', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')")
	statsSpec2.arm("DELETE FROM client_ip_range_window_dirty_ips WHERE (ip_hash")
	if err := store2.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("IP 脏表清理注入应报错")
	}
	statsSpec2.disarm()

	// hasPending 查询注入（per-hash 清理后的 pending 检查）。
	mustExec(t, ctx, store2.db, "INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h10', '2026-09-17T01:00:00.000Z', 2, '2026-09-17T01:00:00.000Z')")
	mustExec(t, ctx, store2.db, "INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h10', '2026-09-17T01:00:00.000Z', 2, '2026-09-17T01:00:00.000Z')")
	statsSpec2.arm("SELECT 1 FROM client_ip_range_window_dirty_ips")
	if err := store2.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("pending 查询注入应报错")
	}
	statsSpec2.disarm()

	// readDirtyRows 直调注入：IP 表与账户表。
	store3, statsSpec3, _ := w12bSvOpenStore(t)
	tx, err := store3.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statsSpec3.arm("SELECT ip_hash, generation")
	if _, err := store3.readDirtyRows(ctx, tx, store3.statsTable("client_ip_range_window_dirty_ips"), []string{"w12b-x"}, ""); err == nil {
		t.Fatal("IP 脏行读取注入应报错")
	}
	if _, err := store3.readDirtyRows(ctx, tx, store3.statsTable("client_ip_account_range_window_dirty_ips"), []string{"w12b-x"}, ""); err == nil {
		t.Fatal("账户脏行读取注入应报错")
	}
	statsSpec3.disarm()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// 空白 group_id 的脏行：targets 归一为空 → 零工作直接返回。
	store4, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store4.business, "INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('   ', 'w12b', '2026-09-17T12:00:00.000Z')")
	if _, err := store4.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err != nil {
		t.Fatalf("空白 group_id 应归一为空集: %v", err)
	}
}

func TestW12bSvFinalArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// 一致性：仅有 daily 无 hourly → NoRows 归零臂 + 漂移 issue。
	store := openTestStore(t, "UTC")
	seedConsistencyDaily(t, ctx, store, "sys", "system", "", "2026-09-15", 3, 0)
	issues, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) == 0 || issues[0].Metric != "request_count" || issues[0].HourlyValue != 0 {
		t.Fatalf("无 hourly 应产出漂移: %#v", issues)
	}

	// business 侧建表失败（EnsureSchema 第二段）。
	w12bSvRegisterDrivers()
	statsSpec := &w12bFailSpec{}
	businessSpec := &w12bFailSpec{match: "CREATE TABLE"}
	w12bSvDriversMu.Lock()
	w12bSvDrivers[w12bSvStatsDriverName] = statsSpec
	w12bSvDrivers[w12bSvBusinessDriverName] = businessSpec
	w12bSvDriversMu.Unlock()
	dir := t.TempDir()
	statsDB, err := sqlOpenHelper(w12bSvStatsDriverName, filepath.Join(dir, "s3.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer statsDB.Close()
	businessDB, err := sqlOpenHelper(w12bSvBusinessDriverName, filepath.Join(dir, "b3.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer businessDB.Close()
	direct := &Store{mode: StoreSQLite, db: statsDB, business: businessDB}
	if err := direct.EnsureSchema(ctx); err == nil {
		t.Fatal("business 建表注入应报错")
	}

	// 按组路径：删除注入经刷新流程传播（163 臂）。
	store2, _, businessSpec2 := w12bSvOpenStore(t)
	seedGroupFixture(t, ctx, store2)
	mustExec(t, ctx, store2.business, "INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g1', 'w12b', '2026-09-17T12:00:00.000Z')")
	businessSpec2.arm("DELETE FROM group_account_stats_dirty")
	if _, err := store2.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err == nil {
		t.Fatal("按组删除注入应报错")
	}
	businessSpec2.disarm()
}

func TestW12bSvLagNoRowsAndLastErrorMessageArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// 仅排除源行：eligible 空 → ignored 命中 → lag NoRows → lagSeconds 0。
	store, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store.business, "INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '\"UTC\"')")
	mustExec(t, ctx, store.db, "INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, created_at) VALUES ('w12b-ign2', 'sys', 't', 'runtime_recovery_probe', 1, '2026-09-17T10:00:00.000Z')")
	processed, err := store.AggregateClientIPStatsBatch(ctx, 10, now)
	if err != nil || processed != 0 {
		t.Fatalf("仅排除源行应空批成功: %d %v", processed, err)
	}

	// target.lastErrorAt 损坏 + 新错误时间：currentMs 解析 panic 臂。
	target := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	target.lastErrorAt = "w12b-bad-error"
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{LastErrorAt: "2026-09-17T11:00:00.000Z"}, "2026-09-17T10:30:00.000Z")
	})
}

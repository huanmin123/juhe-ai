package mockdata

// 统计域测试：在隔离临时数据根上建 business + stats schema，跑 business 域与
// stats 域，然后按 coverage.go 断言、设计文档要求的样本矩阵（采样、后台任务、
// OAuth 用量快照、IP 样本、脏标记）、jobregistry 真实任务名与幂等性逐条核对。
//
// 任务名权威源与用量域的列清单同一手法：从 jobs 源文本解析（maintenance 禁止
// import jobs），文件不可读时 skip 而不是伪通过。
//
// 全部数据写在 t.TempDir() 下；统计域的「不伪造 owner 租约 / stats_job_state /
// 账户健康运行态」边界也有专门断言。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// statsJobRegistrySource 是 jobs jobregistry 任务名的权威源（相对 mockdata 包目录）。
const statsJobRegistrySource = "../../../../projects/jobs/internal/jobregistry/registry.go"

var statsJobNamePattern = regexp.MustCompile(`JobName: "([a-z0-9-]+)"`)

// statsTestRoot 建一个含 business（schema + seed）与 stats（schema）库的临时数据根。
func statsTestRoot(t *testing.T) string {
	t.Helper()
	dir := businessTestRoot(t)
	paths, err := ResolvePaths(dir, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(paths.Stats))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := schema.EnsureSQLiteStats(context.Background(), db); err != nil {
		t.Fatalf("应用 stats schema: %v", err)
	}
	return dir
}

// statsTestEnv 在 statsTestRoot 上按固定基准时间跑 business 域与 stats 域。
func statsTestEnv(t *testing.T, options Options) (*env, DomainResult) {
	t.Helper()
	dir := statsTestRoot(t)
	paths, err := ResolvePaths(dir, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	options.Paths = paths
	if options.Now.IsZero() {
		options.Now = businessTestNow
	}
	e := newEnv(options, nil)
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Errorf("关闭 mockdata 存储: %v", err)
		}
	})
	if _, err := seedBusiness(context.Background(), e); err != nil {
		t.Fatalf("seedBusiness: %v", err)
	}
	result, err := seedStatsRaw(context.Background(), e)
	if err != nil {
		t.Fatalf("seedStatsRaw: %v", err)
	}
	e.recordDomainResult(result)
	return e, result
}

// statsScalarInt 执行返回单个整数的查询。
func statsScalarInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("查询 %q: %v", query, err)
	}
	return value
}

// statsStringColumn 查询单列多行文本。
func statsStringColumn(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("查询 %q: %v", query, err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("扫描 %q: %v", query, err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 %q: %v", query, err)
	}
	return values
}

// TestSeedStatsRawSatisfiesCoverageAssertions 逐条核对 coverage.go 中归属 stats
// 域的关键状态断言：域已接线时它们是硬门槛。
func TestSeedStatsRawSatisfiesCoverageAssertions(t *testing.T) {
	e, result := statsTestEnv(t, Options{Days: 7, DailyRequests: 20, Now: businessTestNow})
	if len(result.Counts) == 0 {
		t.Fatal("stats 域必须返回非空 Counts，否则覆盖报告会把它当成未接线")
	}
	assertions := 0
	for _, assertion := range coverageAssertions() {
		if assertion.Domain != DomainStats {
			continue
		}
		assertions++
		// 裁决：background_task_runs 断言按 stats 库判定（系统指标页读面），
		// 不是 observability 域的 task-runs 库。
		if assertion.Store != StoreStats {
			t.Fatalf("stats 断言应指向 %s 存储：%s", StoreStats, assertion.Name)
		}
		count, err := grepAssertionCount(context.Background(), e, assertion)
		if err != nil {
			t.Fatal(err)
		}
		if count < assertion.Min {
			t.Errorf("%s: 命中 %d 行，要求至少 %d 行", assertion.Name, count, assertion.Min)
		}
	}
	if assertions == 0 {
		t.Fatal("coverage.go 里必须存在 stats 域断言（口径变更时本测试要同步）")
	}
}

// TestSeedStatsRawSampleMatrix 表驱动核对统计域的样本矩阵。
func TestSeedStatsRawSampleMatrix(t *testing.T) {
	const days = 7
	e, _ := statsTestEnv(t, Options{Days: days, DailyRequests: 20, Now: businessTestNow})
	db, err := e.openExisting(StoreStats)
	if err != nil || db == nil {
		t.Fatalf("stats 库应已存在: %v", err)
	}
	business, err := e.openExisting(StoreBusiness)
	if err != nil || business == nil {
		t.Fatalf("business 库应已存在: %v", err)
	}

	t.Run("system_metrics_samples", func(t *testing.T) {
		rows := statsScalarInt(t, db, "SELECT COUNT(*) FROM system_metrics_samples")
		// 30 分钟间隔 × days 天 ≥ days*48 个采样点；宽容下限取 days*24 防止
		// 未来调整间隔时误报，但仍保证趋势窗口无空洞。
		if rows < days*24 {
			t.Errorf("system_metrics_samples %d 行，至少 %d（days×24）", rows, days*24)
		}
		if got := statsScalarInt(t, db, "SELECT COUNT(*) FROM system_metrics_samples WHERE id NOT LIKE ?", CleanupIDPrefix+"%"); got != 0 {
			t.Errorf("%d 行的 id 不带造数标识前缀", got)
		}
		if got := statsScalarInt(t, db, "SELECT COUNT(*) FROM system_metrics_samples WHERE cpu_percent IS NULL OR memory_used_percent IS NULL OR sampled_at IS NULL"); got != 0 {
			t.Errorf("%d 行缺核心指标列", got)
		}
	})

	t.Run("process_event_loop_samples", func(t *testing.T) {
		for _, role := range statsProcessRoles {
			if rows := statsScalarInt(t, db, "SELECT COUNT(*) FROM process_event_loop_samples WHERE process_role = ?", role); rows == 0 {
				t.Errorf("process_role 缺样本 %q", role)
			}
		}
		// 每个角色的样本数应与系统指标采样点数同栅格（同步采样）。
		metricRows := statsScalarInt(t, db, "SELECT COUNT(*) FROM system_metrics_samples")
		for _, role := range statsProcessRoles {
			rows := statsScalarInt(t, db, "SELECT COUNT(*) FROM process_event_loop_samples WHERE process_role = ?", role)
			if rows < metricRows {
				t.Errorf("process_role %q 样本 %d 行少于系统指标 %d 行（未同步采样）", role, rows, metricRows)
			}
		}
	})

	t.Run("background_task_runs", func(t *testing.T) {
		distinct := statsScalarInt(t, db, "SELECT COUNT(DISTINCT job_name) FROM background_task_runs")
		if distinct < 6 {
			t.Errorf("background_task_runs 只有 %d 个任务名，要求至少 6 个", distinct)
		}
		for _, status := range []string{"completed", "failed", "running", "queued"} {
			if rows := statsScalarInt(t, db, "SELECT COUNT(*) FROM background_task_runs WHERE status = ?", status); rows == 0 {
				t.Errorf("background_task_runs 缺 status=%q 样本", status)
			}
		}
		if got := statsScalarInt(t, db, `SELECT COUNT(*) FROM background_task_runs
			WHERE status IN ('completed','failed') AND (duration_ms IS NULL OR finished_at IS NULL OR started_at IS NULL)`); got != 0 {
			t.Errorf("%d 条 completed/failed 行缺 duration / started_at / finished_at", got)
		}
		if got := statsScalarInt(t, db, `SELECT COUNT(*) FROM background_task_runs
			WHERE status = 'failed' AND (error_message IS NULL OR error_message = '' OR exit_code IS NULL)`); got != 0 {
			t.Errorf("%d 条 failed 行缺 error_message / exit_code", got)
		}
		// running 行必须心跳新鲜：过旧的 running 行会被 jobs 的 reconcile 改判
		// failed，页面样本在造数后立刻变样。
		freshHeartbeat := businessTestNow.Add(-5 * time.Minute).Format(isoMillisLayout)
		if got := statsScalarInt(t, db, `SELECT COUNT(*) FROM background_task_runs
			WHERE status = 'running' AND (heartbeat_at IS NULL OR heartbeat_at < ?)`, freshHeartbeat); got != 0 {
			t.Errorf("%d 条 running 行心跳过期（会被 reconcile 改判 failed）", got)
		}
	})

	t.Run("background_task_names_from_jobregistry", func(t *testing.T) {
		raw, err := os.ReadFile(statsJobRegistrySource)
		if err != nil {
			t.Skipf("jobs jobregistry 源文本不可读（%v），本 checkout 无法核对任务名", err)
		}
		registered := map[string]bool{}
		for _, match := range statsJobNamePattern.FindAllStringSubmatch(string(raw), -1) {
			registered[match[1]] = true
		}
		if len(registered) == 0 {
			t.Fatalf("未能从 %s 解析出任何 JobName", statsJobRegistrySource)
		}
		for _, jobName := range statsStringColumn(t, db, "SELECT DISTINCT job_name FROM background_task_runs") {
			if !registered[jobName] {
				t.Errorf("job_name %q 不在 jobs jobregistry 的注册清单里（不得伪造任务名）", jobName)
			}
		}
	})

	t.Run("account_usage_snapshots", func(t *testing.T) {
		rows := statsScalarInt(t, db, "SELECT COUNT(*) FROM account_usage_snapshots WHERE kind = ?", statsAccountUsageSnapshotKind)
		if rows < 2 {
			t.Errorf("openai_codex 快照只有 %d 行，要求至少 2 个 OAuth 账户", rows)
		}
		// 快照必须挂在 business 域的 OAuth mock 账户上，且 payload 带 5h/7d 窗口键
		// （gateway accountsbalance.OAuthUsageWindowFromSnapshot 的读面键）。
		type snapshotRow struct {
			accountID string
			payload   string
		}
		rowsResult, err := db.QueryContext(context.Background(), `SELECT s.account_id, s.snapshot_json
			FROM account_usage_snapshots s WHERE s.kind = ?`, statsAccountUsageSnapshotKind)
		if err != nil {
			t.Fatal(err)
		}
		var snapshots []snapshotRow
		for rowsResult.Next() {
			var row snapshotRow
			if err := rowsResult.Scan(&row.accountID, &row.payload); err != nil {
				rowsResult.Close()
				t.Fatal(err)
			}
			snapshots = append(snapshots, row)
		}
		rowsResult.Close()
		for _, row := range snapshots {
			if got := statsScalarInt(t, business, "SELECT COUNT(*) FROM accounts WHERE id = ? AND type = 'oauth'", row.accountID); got != 1 {
				t.Errorf("快照账户 %q 不是 business 库的 OAuth mock 账户", row.accountID)
			}
			var document map[string]any
			if err := json.Unmarshal([]byte(row.payload), &document); err != nil {
				t.Fatalf("快照 %s 的 snapshot_json 不是合法 JSON: %v", row.accountID, err)
			}
			for _, key := range []string{"codex_5h_used_percent", "codex_5h_reset_at", "codex_7d_used_percent", "codex_7d_reset_at"} {
				if _, ok := document[key]; !ok {
					t.Errorf("快照 %s 缺 payload 键 %q", row.accountID, key)
				}
			}
		}
	})

	t.Run("client_ip_samples", func(t *testing.T) {
		if rows := statsScalarInt(t, db, "SELECT COUNT(*) FROM client_ip_registry"); rows == 0 {
			t.Error("client_ip_registry 必须有登记行（策略读面 INNER JOIN registry）")
		}
		for _, sample := range []struct{ policyType, status string }{
			{"blacklist", "active"}, {"allowlist", "active"}, {"blacklist", "disabled"},
		} {
			if rows := statsScalarInt(t, db,
				"SELECT COUNT(*) FROM client_ip_policies WHERE policy_type = ? AND status = ?",
				sample.policyType, sample.status); rows == 0 {
				t.Errorf("client_ip_policies 缺 policy_type=%s status=%s 样本", sample.policyType, sample.status)
			}
		}
		// 命中只给启用中的黑名单策略；stat_date 形如 YYYY-MM-DD。
		if rows := statsScalarInt(t, db, `SELECT COUNT(*) FROM client_ip_policy_hits h
			JOIN client_ip_policies p ON p.id = h.policy_id
			WHERE p.status <> 'active' OR p.policy_type <> 'blacklist'`); rows != 0 {
			t.Errorf("%d 条命中行挂在非启用黑名单策略上", rows)
		}
		if got := statsScalarInt(t, db, "SELECT COUNT(*) FROM client_ip_registry WHERE client_ip NOT LIKE '10.10.%' AND client_ip NOT LIKE '10.20.%'"); got != 0 {
			t.Errorf("%d 条登记 IP 不在用量记录的 10.10./10.20. 网段", got)
		}
	})

	t.Run("dirty_markers", func(t *testing.T) {
		wantGroups := statsScalarInt(t, business, "SELECT COUNT(*) FROM groups WHERE id LIKE ?", CleanupIDPrefix+"%")
		if wantGroups == 0 {
			t.Fatal("business 库必须有 mock 分组（前置失败）")
		}
		if got := statsScalarInt(t, business, "SELECT COUNT(*) FROM group_account_stats_dirty"); got != wantGroups {
			t.Errorf("group_account_stats_dirty %d 行，mock 分组 %d 个（必须全部标脏）", got, wantGroups)
		}
		if got := statsScalarInt(t, business, "SELECT COUNT(*) FROM group_account_stats_dirty WHERE reason NOT LIKE ?", CleanupIDPrefix+"%"); got != 0 {
			t.Errorf("%d 条脏标记 reason 不带造数标识", got)
		}

		wantOwners := statsScalarInt(t, business, "SELECT COUNT(DISTINCT system_account_id) FROM accounts WHERE id LIKE ?", CleanupIDPrefix+"%")
		overviewOwners := statsStringColumn(t, db, "SELECT system_account_id FROM usage_overview_dirty_scopes")
		globalSeen := false
		ownerSeen := 0
		for _, owner := range overviewOwners {
			if owner == "global" {
				globalSeen = true
				continue
			}
			ownerSeen++
		}
		if !globalSeen {
			t.Error("usage_overview_dirty_scopes 缺 global 作用域行")
		}
		if ownerSeen != wantOwners {
			t.Errorf("usage_overview_dirty_scopes 覆盖 %d 个系统账户，mock 账户归属 %d 个", ownerSeen, wantOwners)
		}
		if got := statsScalarInt(t, db, "SELECT COUNT(*) FROM usage_overview_dirty_scopes WHERE min_changed_date > ?", businessTestNow.Format("2006-01-02")); got != 0 {
			t.Errorf("%d 条 overview 脏范围的 min_changed_date 晚于基准时间", got)
		}

		if got := statsScalarInt(t, db, "SELECT COUNT(*) FROM ai_performance_summary_dirty_system_accounts"); got != wantOwners {
			t.Errorf("ai_performance_summary_dirty_system_accounts %d 行，mock 账户归属 %d 个", got, wantOwners)
		}
		if got := statsScalarInt(t, db, "SELECT COUNT(*) FROM ai_performance_summary_dirty_system_accounts WHERE min_stat_date > max_stat_date"); got != 0 {
			t.Errorf("%d 条 AI 性能脏范围 min_stat_date > max_stat_date", got)
		}
	})

	t.Run("owner_runtime_tables_not_forged", func(t *testing.T) {
		// 裁决 2/3 与设计文档「不写什么」：owner 租约、统计游标、账户健康运行态
		// 一律不伪造；这些表在 stats-only 运行里必须保持 0 行。
		for _, table := range []string{"background_job_leases", "stats_job_state"} {
			if rows := statsScalarInt(t, db, "SELECT COUNT(*) FROM "+table); rows != 0 {
				t.Errorf("%s 有 %d 行：owner 运行态不得伪造", table, rows)
			}
		}
	})
}

// TestSeedStatsRawIdempotentAcrossRuns 验证统计域「重新执行不叠加」：同一数据根
// 第二次运行（含清理）后，逐键计数与各表行数都与第一次相同。
func TestSeedStatsRawIdempotentAcrossRuns(t *testing.T) {
	dir := statsTestRoot(t)
	options := Options{Days: 3, DailyRequests: 12, Now: businessTestNow}
	first := statsRunOnce(t, dir, options)
	second := statsRunOnce(t, dir, options)
	if len(first) != len(second) {
		t.Fatalf("两次运行的计数键不一致：%v / %v", sortedKeys(first), sortedKeys(second))
	}
	for key, value := range first {
		if second[key] != value {
			t.Errorf("计数 %s 第二次为 %d，第一次为 %d", key, second[key], value)
		}
	}

	// 表行数逐表核对（清理由 cleanupAll 负责；stats 域的 upsert 主键必须让
	// 「清理被跳过」的场景同样行数稳定）。
	paths, err := ResolvePaths(dir, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(paths.Stats))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, table := range []string{"system_metrics_samples", "process_event_loop_samples", "background_task_runs", "account_usage_snapshots", "client_ip_registry", "client_ip_policies", "client_ip_policy_hits"} {
		rows := statsScalarInt(t, db, "SELECT COUNT(*) FROM "+table)
		if rows == 0 {
			t.Errorf("%s 第二次运行后为空", table)
		}
	}
	business, err := sql.Open("sqlite", sqliteDSN(paths.Business))
	if err != nil {
		t.Fatal(err)
	}
	defer business.Close()
	business.SetMaxOpenConns(1)
	if got, want := statsScalarInt(t, business, "SELECT COUNT(*) FROM group_account_stats_dirty"),
		second["groupAccountStatsDirty"]; got != want {
		t.Errorf("第二次运行后 group_account_stats_dirty %d 行，报告 %d", got, want)
	}
}

// statsRunOnce 模拟一次完整的 --mockdata 统计域执行：新建 env（含清理语义）
// → 重建 business → seedStatsRaw，返回本次域计数。
func statsRunOnce(t *testing.T, dir string, options Options) map[string]int {
	t.Helper()
	paths, err := ResolvePaths(dir, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	options.Paths = paths
	e := newEnv(options, nil)
	defer func() {
		if err := e.Close(); err != nil {
			t.Errorf("关闭 mockdata 存储: %v", err)
		}
	}()
	// 顺序与 --mockdata 一致：先清理上一批，再按域顺序重建 business → stats。
	if _, _, err := cleanupAll(context.Background(), e); err != nil {
		t.Fatalf("cleanupAll: %v", err)
	}
	if _, err := seedBusiness(context.Background(), e); err != nil {
		t.Fatalf("seedBusiness: %v", err)
	}
	result, err := seedStatsRaw(context.Background(), e)
	if err != nil {
		t.Fatalf("seedStatsRaw: %v", err)
	}
	return result.Counts
}

// TestSeedStatsRawSkipsWithoutStatsDatabase 核对空数据根上的跳过语义：Counts 为空
// （域未接线）且不创建 stats 库文件；stats 库存在但缺表时按缺表逐项跳过。
func TestSeedStatsRawSkipsWithoutStatsDatabase(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Days: 3, DailyRequests: 5, Now: businessTestNow, Paths: paths}, nil)
	defer func() { _ = e.Close() }()
	result, err := seedStatsRaw(context.Background(), e)
	if err != nil {
		t.Fatalf("空数据根上 seedStatsRaw 必须跳过而不是失败: %v", err)
	}
	if len(result.Counts) != 0 {
		t.Fatalf("空数据根上 counts 必须为空（未接线语义）: %v", result.Counts)
	}
	if _, statErr := os.Stat(paths.Stats); !os.IsNotExist(statErr) {
		t.Fatalf("跳过时不得创建 stats 库：%v", statErr)
	}

	// stats 库存在但没有任何表（例如只被用量域镜像建过库）：按缺表跳过，不得报错。
	dir := businessTestRoot(t)
	paths, err = ResolvePaths(dir, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e2 := newEnv(Options{Days: 3, DailyRequests: 5, Now: businessTestNow, Paths: paths}, nil)
	defer func() { _ = e2.Close() }()
	if _, err := e2.exec(context.Background(), StoreStats, "CREATE TABLE usage_records (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	result, err = seedStatsRaw(context.Background(), e2)
	if err != nil {
		t.Fatalf("stats 库缺表时 seedStatsRaw 必须逐项跳过: %v", err)
	}
	if len(result.Counts) != 0 {
		t.Fatalf("stats 库缺表时 counts 必须为空: %v", result.Counts)
	}
}

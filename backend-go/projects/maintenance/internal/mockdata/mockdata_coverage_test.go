package mockdata

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bootstrappedEnv 构建一个「所有固定存储都存在且各有一张非空表」的最小环境：
// 让覆盖校验的 Ready 判定不被缺库掩盖。
func bootstrappedEnv(t *testing.T) *env {
	t.Helper()
	e := testEnv(t)
	ctx := context.Background()
	// 每个固定存储都建一张自造表并写一行，模拟「已 bootstrap 且有数据」。
	for _, item := range e.options.Paths.fixedStores() {
		createTestTable(t, e, item.Name, `CREATE TABLE verification_probe (id TEXT PRIMARY KEY)`)
		if err := e.insertMap(ctx, item.Name, "verification_probe", map[string]any{"id": "probe"}); err != nil {
			t.Fatal(err)
		}
	}
	// 分片存储：codex 分片与 usage 分片同样要有数据。
	for _, item := range e.options.Paths.codexContextStores() {
		createTestTable(t, e, item.Name, `CREATE TABLE verification_probe (id TEXT PRIMARY KEY)`)
		if err := e.insertMap(ctx, item.Name, "verification_probe", map[string]any{"id": "probe"}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestVerifyCoverageReadyWhenEveryTableHasRows(t *testing.T) {
	e := bootstrappedEnv(t)
	report, err := VerifyCoverage(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready {
		t.Fatalf("report should be ready: empty=%v errors=%v", report.Empty, report.Errors)
	}
	if len(report.Stores) == 0 {
		t.Fatal("report should enumerate stores")
	}
	// 域未接线：全部关键状态断言进 NotCovered，但不阻塞 Ready。
	if len(report.NotCovered) == 0 {
		t.Fatal("unwired domains must be listed as not covered")
	}
	for _, item := range report.NotCovered {
		if !strings.Contains(item, "未接线") {
			t.Fatalf("not-covered entry should explain why: %q", item)
		}
	}
}

func TestVerifyCoverageFlagsEmptyTablesAndMissingStores(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	// 只有 business 存在，且其中一张表为空。
	createTestTable(t, e, StoreBusiness, `CREATE TABLE filled (id TEXT PRIMARY KEY)`)
	if err := e.insertMap(ctx, StoreBusiness, "filled", map[string]any{"id": "x"}); err != nil {
		t.Fatal(err)
	}
	createTestTable(t, e, StoreBusiness, `CREATE TABLE hollow (id TEXT PRIMARY KEY)`)
	report, err := VerifyCoverage(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatal("an empty table must fail the report")
	}
	if len(report.Empty) != 1 || report.Empty[0] != StoreBusiness+".hollow" {
		t.Fatalf("empty = %v", report.Empty)
	}
	// 其余 11 个固定存储 + 分片文件缺失都进 Errors。
	missing := strings.Join(report.Errors, "\n")
	if !strings.Contains(missing, StoreStats+": 存储文件不存在") {
		t.Fatalf("errors should name the missing stats store:\n%s", missing)
	}
	if !strings.Contains(missing, StoreCodexContextShardPrefix+"[0]: 存储文件不存在") {
		t.Fatalf("errors should name a missing codex shard:\n%s", missing)
	}
	// 零表的库是结构性错误（schema 没 ensure）。
	e2 := testEnv(t)
	if _, err := e2.open(StoreBusiness); err != nil {
		t.Fatal(err)
	}
	report2, err := VerifyCoverage(ctx, e2)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(report2.Errors, "\n")
	if !strings.Contains(joined, StoreBusiness+": 库内没有可读表") {
		t.Fatalf("a tableless store must be an error:\n%s", joined)
	}
}

func TestVerifyCoverageAllowlistKeepsReady(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	// 白名单表可以为空：owner 租约 / 游标族与设计文档明确允许为空的瞬时队列，
	// 以及「消费后为空才是正确状态」的运行时队列。
	for _, ddl := range []string{
		`CREATE TABLE background_job_leases (lease_key TEXT PRIMARY KEY)`,
		`CREATE TABLE audit_log_owner_leases (lease_key TEXT PRIMARY KEY)`,
		`CREATE TABLE account_health_key_cursors (cursor_key TEXT PRIMARY KEY)`,
		`CREATE TABLE usage_overview_dirty_scopes_projection_cursors (cursor_key TEXT PRIMARY KEY)`,
		`CREATE TABLE usage_range_window_requests (id TEXT PRIMARY KEY)`,
		`CREATE TABLE codex_context_storage_cleanup_queue (id TEXT PRIMARY KEY)`,
		`CREATE TABLE account_health_current_state (account_id TEXT PRIMARY KEY)`,
		`CREATE TABLE stats_job_state (job_name TEXT PRIMARY KEY)`,
		`CREATE TABLE record_maintenance_jobs (id TEXT PRIMARY KEY)`,
		`CREATE TABLE account_health_probe_request_outbox (id TEXT PRIMARY KEY)`,
	} {
		createTestTable(t, e, StoreBusiness, ddl)
	}
	report, err := VerifyCoverage(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	// 白名单表不在 Empty 里；该库本身有可读表，因此没有「库内没有可读表」错误。
	for _, item := range report.Empty {
		if strings.Contains(item, "owner_leases") || strings.Contains(item, "cursors") || strings.Contains(item, "usage_range_window_requests") {
			t.Fatalf("allowlisted table must not be empty-flagged: %v", report.Empty)
		}
	}
	// 非白名单的空分片库仍然报缺库错误，因此这里只断言白名单项被标注。
	var allowlisted int
	for _, storeCoverage := range report.Stores {
		if storeCoverage.Name != StoreBusiness {
			continue
		}
		for _, table := range storeCoverage.Tables {
			if table.AllowEmpty {
				allowlisted++
				if table.AllowEmptyReason == "" {
					t.Fatalf("allowlisted table %s must carry a reason", table.Name)
				}
			}
		}
	}
	if allowlisted != 10 {
		t.Fatalf("allowlisted tables = %d, want 10", allowlisted)
	}
}

func TestCoverageAllowEmptyRules(t *testing.T) {
	cases := []struct {
		table  string
		allows bool
	}{
		{"usage_range_window_requests", true},
		{"codex_context_storage_cleanup_queue", true},
		{"stats_job_state", true},
		{"background_job_leases", true},
		{"account_health_current_state", true},
		{"account_health_outcomes", true},
		{"audit_payload_blob_gc", true},
		{"account_health_jobs_input_versions", true},
		{"account_health_jobs_input_outbox", true},
		{"account_api_key_pool_probe_cursors", true},
		{"account_health_projection_receipts", true},
		{"account_list_availability_projections", true},
		{"account_list_availability_projection_index", true},
		{"account_list_availability_projection_tags", true},
		{"account_list_availability_projection_search_terms", true},
		{"account_list_availability_runtime_overlays", true},
		{"account_quality_dirty_accounts", true},
		{"usage_record_cleanup_deductions", true},
		{"record_maintenance_jobs", true},
		{"account_health_probe_request_outbox", true},
		{"account_health_direct_input_suppressions", true},
		{"system_metrics_hourly", true},
		{"process_event_loop_hourly", true},
		{"system_metrics_trend_windows", true},
		{"process_event_loop_trend_windows", true},
		{"anything_owner_leases", true},
		{"anything_key_cursors", true},
		{"anything_projection_cursors", true},
		{"accounts", false},
		{"usage_records", false},
		{"oauth_access_tokens", false},
		{"account_lock_states", false},
		{"account_list_availability_projection_dependency_health", false},
	}
	for _, testCase := range cases {
		reason, allowed := coverageAllowEmpty(testCase.table)
		if allowed != testCase.allows {
			t.Fatalf("coverageAllowEmpty(%q) = %v, want %v", testCase.table, allowed, testCase.allows)
		}
		if allowed && reason == "" {
			t.Fatalf("coverageAllowEmpty(%q) must explain the exemption", testCase.table)
		}
	}
}

func TestAssertionTableParsing(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"SELECT COUNT(*) FROM accounts WHERE status = 'active'", "accounts"},
		{"SELECT COUNT(*) FROM usage_records", "usage_records"},
		{"SELECT 1", ""},
	}
	for _, testCase := range cases {
		if got := assertionTable(testCase.query); got != testCase.want {
			t.Fatalf("assertionTable(%q) = %q, want %q", testCase.query, got, testCase.want)
		}
	}
}

// TestAssertionsFailWhenDomainIsWired 验证「域接线后断言就是硬门槛」：手工把域
// 标成已接线，空库上的断言必须让 Ready=false 并写进 Errors。
func TestAssertionsFailWhenDomainIsWired(t *testing.T) {
	e := testEnv(t)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE accounts (id TEXT PRIMARY KEY, status TEXT NOT NULL)`)
	e.recordDomainResult(DomainResult{Name: DomainBusiness, Counts: map[string]int{"accounts": 1}})
	report, err := VerifyCoverage(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatal("wired domain with unsatisfied assertions must not be ready")
	}
	joined := strings.Join(report.Errors, "\n")
	if !strings.Contains(joined, "accounts.status.active") {
		t.Fatalf("errors should name the failed assertion:\n%s", joined)
	}
	for _, item := range report.NotCovered {
		if strings.Contains(item, "accounts.status.active") {
			t.Fatalf("a wired domain assertion must not be not-covered: %q", item)
		}
	}
}

// TestAssertionsPassOnSatisfiedDomain 用一个真实小库让业务域断言通过。
func TestAssertionsPassOnSatisfiedDomain(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	createTestTable(t, e, StoreBusiness, `CREATE TABLE accounts (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL
	)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE route_strategies (
		id TEXT PRIMARY KEY,
		mode TEXT NOT NULL
	)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE route_strategy_groups (
		route_strategy_id TEXT NOT NULL,
		group_id TEXT NOT NULL,
		PRIMARY KEY (route_strategy_id, group_id)
	)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE resource_authorizations (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL
	)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE account_api_key_runtime_states (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL
	)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE custom_provider_models (
		id TEXT PRIMARY KEY,
		scope TEXT NOT NULL,
		status TEXT NOT NULL
	)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE model_check_question_bank (id TEXT PRIMARY KEY)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE api_keys (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL
	)`)
	rows := []struct {
		table string
		row   map[string]any
	}{
		{"accounts", map[string]any{"id": "a1", "status": "active"}},
		{"accounts", map[string]any{"id": "a2", "status": "disabled"}},
		{"route_strategies", map[string]any{"id": "r1", "mode": "normal"}},
		{"route_strategies", map[string]any{"id": "r2", "mode": "failover"}},
		{"route_strategies", map[string]any{"id": "r3", "mode": "weighted_round_robin"}},
		{"route_strategy_groups", map[string]any{"route_strategy_id": "r2", "group_id": "g1"}},
		{"resource_authorizations", map[string]any{"id": "z1", "status": "active"}},
		{"resource_authorizations", map[string]any{"id": "z2", "status": "paused"}},
		{"account_api_key_runtime_states", map[string]any{"id": "k1", "status": "active"}},
		{"account_api_key_runtime_states", map[string]any{"id": "k2", "status": "cooldown"}},
		{"custom_provider_models", map[string]any{"id": "c1", "scope": "personal", "status": "active"}},
		{"custom_provider_models", map[string]any{"id": "c2", "scope": "personal", "status": "draft"}},
		{"model_check_question_bank", map[string]any{"id": "q1"}},
		{"api_keys", map[string]any{"id": CleanupIDPrefix + "key_main", "name": CleanupNamePrefix + "主力Key"}},
	}
	for _, item := range rows {
		if err := e.insertMap(ctx, StoreBusiness, item.table, item.row); err != nil {
			t.Fatal(err)
		}
	}
	e.recordDomainResult(DomainResult{Name: DomainBusiness, Counts: map[string]int{"accounts": 2}})
	notCovered, failures := evaluateAssertions(ctx, e)
	for _, failure := range failures {
		if strings.HasPrefix(failure, "accounts.") || strings.HasPrefix(failure, "route_strategies.") || strings.HasPrefix(failure, "resource_authorizations.") || strings.HasPrefix(failure, "custom_provider_models.") || failure == "model_check_question_bank" || strings.HasPrefix(failure, "api_keys.") || strings.HasPrefix(failure, "account_api_key_runtime_states.") {
			t.Fatalf("business assertion failed unexpectedly: %s", failure)
		}
	}
	// 其他四个域仍未接线。
	for _, item := range notCovered {
		if strings.Contains(item, DomainBusiness) {
			t.Fatalf("business assertions should be evaluated: %q", item)
		}
	}
}

// writeUsageShardFixture 直接在磁盘上造出一个 usage 分片文件。分片存储是
// 「按目录发现的」，必须在 newEnv 之前落地，否则不会被登记进上下文。
func writeUsageShardFixture(t *testing.T, shardPath string, rows int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(shardPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(shardPath)+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE usage_records (
		id TEXT PRIMARY KEY,
		traffic_source TEXT NOT NULL,
		model_mapping_applied INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < rows; index++ {
		if _, err := db.Exec(`INSERT INTO usage_records (id, traffic_source, model_mapping_applied) VALUES (?, 'gateway', 1)`, fmt.Sprintf("%susage_%d", CleanupIDPrefix, index)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAssertionsCoverShardPrefix 验证分片前缀断言把各分片计数相加。
func TestAssertionsCoverShardPrefix(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	writeUsageShardFixture(t, filepath.Join(paths.UsageShardRoot, "20260101", "s00.sqlite3"), 2)
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = e.Close() }()
	ctx := context.Background()
	assertion := coverageAssertion{Name: "shard", Store: StoreUsageShardPrefix + "[", Query: "SELECT COUNT(*) FROM usage_records WHERE traffic_source = 'gateway'", Min: 2}
	count, err := grepAssertionCount(ctx, e, assertion)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("shard count = %d, want 2", count)
	}
	if len(e.stores()) != len(paths.fixedStores())+paths.CodexContextShardCount+1 {
		t.Fatalf("stores = %d", len(e.stores()))
	}
	// 未知存储名必须报错而不是静默算 0。
	if _, err := grepAssertionCount(ctx, e, coverageAssertion{Store: "not-a-store", Query: "SELECT 1"}); err == nil {
		t.Fatal("unknown store should fail")
	}
	// 目标表不存在的分片按 0 处理（域未接线时就是这种状态）。
	if count, err := grepAssertionCount(ctx, e, coverageAssertion{Store: StoreUsageShardPrefix + "[", Query: "SELECT COUNT(*) FROM ghost"}); err != nil || count != 0 {
		t.Fatalf("missing table count = %d/%v", count, err)
	}
}

func TestVerifyPathsCoverageValidatesOptions(t *testing.T) {
	if _, err := VerifyPathsCoverage(context.Background(), Options{}); err == nil {
		t.Fatal("invalid options must fail before touching any store")
	}
}

func TestCoverageAssertionInventoryPinsVerifiedTables(t *testing.T) {
	// 断言清单是硬编码的验证点骨架：这里钉住「每个域至少一条断言」与
	// 「断言名唯一」，避免后续实现误删域覆盖。
	seen := map[string]bool{}
	perDomain := map[string]int{}
	for _, assertion := range coverageAssertions() {
		if assertion.Name == "" || assertion.Query == "" || assertion.Store == "" || assertion.Domain == "" {
			t.Fatalf("incomplete assertion: %+v", assertion)
		}
		if assertion.Min < 1 {
			t.Fatalf("assertion %s must require at least one row", assertion.Name)
		}
		if seen[assertion.Name] {
			t.Fatalf("duplicate assertion name %q", assertion.Name)
		}
		seen[assertion.Name] = true
		perDomain[assertion.Domain]++
	}
	for _, domain := range []string{DomainBusiness, DomainUsage, DomainStats, DomainObservability, DomainChatCodexModelCheck} {
		if perDomain[domain] == 0 {
			t.Fatalf("domain %s has no coverage assertion", domain)
		}
	}
}

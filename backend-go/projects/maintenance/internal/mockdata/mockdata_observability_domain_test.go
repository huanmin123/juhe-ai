package mockdata

// 可观测域（observability）的 PG 集成测试（pgtestkit 每测试独立库）。
//
// 属主表 DDL 夹具采用「从属主 schema 源文件提取 DDL 常量并执行」的手法（参照
// jobs cmd/juhe-ai-jobs w0cross_contract_golden_test.go 的跨项目文件读取）：
// gateway 与 maintenance 是不同 Go module 且 gateway 不可被 maintenance import，
// 因此测试用 go/parser 解析属主源文件里的 postgresSchema 常量原文并执行——不
// 复制 DDL，属主 DDL 漂移时测试库与真实 schema 一起漂移，造数写错列型会立刻
// 在真实形状上失败。gateway 源未检出（非 monorepo 检出）时相关测试 t.Skip。

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// observabilityOwnerFiles 是属主 DDL 常量的源文件与常量名（相对 backend-go 根）。
type observabilityOwnerFile struct {
	RelPath   string
	ConstName string
	// MustReport 标记的常量缺失视为本 module 自身损坏（jobs 是 maintenance 的
	// module 依赖，必然检出）；gateway 属 missable（单独检出时 skip）。
	MustReport bool
	// Segments 标记该常量覆盖哪些属主段（audit / operation / runtime）。
	Segments []string
	// Sanity 是常量里必须出现的一条 CREATE TABLE 证据（夹具自检）。
	Sanity string
}

var observabilityOwnerFiles = []observabilityOwnerFile{
	{RelPath: "projects/gateway/internal/auditlog/schema.go", ConstName: "postgresSchema",
		Segments: []string{"audit"}, Sanity: "CREATE TABLE IF NOT EXISTS juhe_dataset.audit_logs"},
	{RelPath: "projects/gateway/internal/auditlog/store.go", ConstName: "postgresBlobGCSchema",
		Segments: []string{"audit"}, Sanity: "CREATE TABLE IF NOT EXISTS juhe_dataset.audit_payload_blob_gc"},
	{RelPath: "projects/gateway/internal/operationlog/schema.go", ConstName: "postgresSchema",
		Segments: []string{"operation"}, Sanity: "CREATE TABLE IF NOT EXISTS juhe_dataset.operation_logs"},
	{RelPath: "projects/jobs/internal/runtimelog/schema.go", ConstName: "postgresSchema",
		MustReport: true, Segments: []string{"runtime"},
		Sanity: "CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_logs"},
}

// observabilityBackendGoRoot 用 runtime.Caller(0) 定位 backend-go workspace 根
// （本测试文件位于 projects/maintenance/internal/mockdata/ 下）；以 go.work +
// 双 module 标志探测而不是硬编码层数。未命中返回 false（gateway 源未检出）。
func observabilityBackendGoRoot(t *testing.T) (string, bool) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) 无法定位测试文件路径")
	}
	dir := filepath.Dir(thisFile)
	for level := 0; level < 8; level++ {
		if observabilityFileExists(filepath.Join(dir, "go.work")) &&
			observabilityFileExists(filepath.Join(dir, "projects", "gateway", "go.mod")) &&
			observabilityFileExists(filepath.Join(dir, "projects", "jobs", "go.mod")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func observabilityFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// observabilityConstFromSource 从源文件提取包级字符串常量字面量值。
func observabilityConstFromSource(t *testing.T, path, constName string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range valueSpec.Names {
				if name.Name != constName {
					continue
				}
				if len(valueSpec.Values) <= index {
					t.Fatalf("%s: 常量 %s 缺少显式值", path, constName)
				}
				literal, ok := valueSpec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s: 常量 %s 不是字符串字面量", path, constName)
				}
				value, unquoteErr := strconv.Unquote(literal.Value)
				if unquoteErr != nil {
					t.Fatalf("%s: 常量 %s 反引号解码失败: %v", path, constName, unquoteErr)
				}
				return value
			}
		}
	}
	t.Fatalf("%s: 未找到常量 %s", path, constName)
	return ""
}

// observabilityApplyOwnerSchema 从属主源文件提取 DDL 并执行（segments 选 audit /
// operation / runtime 的子集，用于「只建部分 schema」的 skip 路径测试）。
func observabilityApplyOwnerSchema(t *testing.T, ctx context.Context, target *sql.DB, segments ...string) {
	t.Helper()
	root, ok := observabilityBackendGoRoot(t)
	if !ok {
		t.Skipf("未定位到 backend-go workspace 根（monorepo 完整检出时生效）")
	}
	for _, item := range observabilityOwnerFiles {
		matched := false
		for _, segment := range segments {
			for _, itemSegment := range item.Segments {
				if segment == itemSegment {
					matched = true
				}
			}
		}
		if !matched {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(item.RelPath))
		if !observabilityFileExists(path) {
			if item.MustReport {
				t.Fatalf("jobs 契约源缺失（本 module 依赖应必然检出）: %s", path)
			}
			t.Skipf("gateway 契约源未检出（monorepo 完整检出时本测试生效）: %s", path)
		}
		ddl := observabilityConstFromSource(t, path, item.ConstName)
		if !strings.Contains(ddl, item.Sanity) {
			t.Fatalf("%s 的 %s 未包含预期语句证据 %q（属主源文件结构变化，夹具需复核）",
				item.RelPath, item.ConstName, item.Sanity)
		}
		for _, statement := range strings.Split(ddl, ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := target.ExecContext(ctx, statement); err != nil {
				t.Fatalf("执行属主 DDL（%s）失败: %v\n语句: %s", item.RelPath, err, statement)
			}
		}
	}
}

// observabilityPrepare 返回 pgtestkit 的 prepare 函数：EnsurePostgres + 指定属主段。
func observabilityPrepare(t *testing.T, segments ...string) func(ctx context.Context, target *sql.DB) error {
	t.Helper()
	return func(ctx context.Context, target *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, target); err != nil {
			return err
		}
		observabilityApplyOwnerSchema(t, ctx, target, segments...)
		return nil
	}
}

// observabilityRunForTest 在指定库上执行一次完整造数（固定时钟）。Run 会执行
// 注册表里全部已接线域（含 chat 域的资产文件落盘），因此与 runMockdataForTest
// 一样把 chat 资产根固定到测试临时目录。
func observabilityRunForTest(t *testing.T, dsn string, now time.Time, summaryFile string) Report {
	t.Helper()
	t.Setenv("JUHE_AI_CHAT_ASSETS_ROOT", filepath.Join(t.TempDir(), "chat-assets"))
	report, err := Run(context.Background(), Options{
		DSN:           dsn,
		Days:          3,
		DailyRequests: DefaultDailyRequests,
		Now:           now,
		SummaryFile:   summaryFile,
	})
	if err != nil {
		t.Fatalf("mockdata Run 失败: %v", err)
	}
	return report
}

// observabilityCountsOf 取指定域的计数；域缺失时返回 nil。
func observabilityCountsOf(report Report, name string) map[string]int {
	for _, domain := range report.Domains {
		if domain.Name == name {
			return domain.Counts
		}
	}
	return nil
}

// observabilityQueryInt 执行单值查询。
func observabilityQueryInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("查询 %q: %v", query, err)
	}
	return count
}

// TestObservabilityRunSeedsAllSegments 是全属主就位时的核心验收：五个子段全部
// 产出，列型与关联语义在真实属主 schema 上成立。
func TestObservabilityRunSeedsAllSegments(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t,
		observabilityPrepare(t, "audit", "operation", "runtime")).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	summaryPath := coverageSummaryPath(t)
	report := observabilityRunForTest(t, dsn, now, summaryPath)

	counts := observabilityCountsOf(report, DomainObservability)
	if counts == nil {
		t.Fatalf("observability 域未产出计数: %+v", report.Domains)
	}
	t.Logf("observability counts (days=3): %v", counts)
	// 各子段计数非空；确定性子段钉住精确值。
	expectPositive := []string{
		"auditLogs", "auditLogAttempts", "auditPayloadBlobs", "auditPayloadRefs", "auditErrorGroups",
		"operationLogs", "operationLogTargets", "operationLogViewers", "operationLogSummarySearchTerms",
		"runtimeLogs", "runtimeLogEventFacets",
		"publicApiLogs",
	}
	for _, key := range expectPositive {
		if counts[key] == 0 {
			t.Fatalf("observability 计数缺少或为 0: %s in %v", key, counts)
		}
	}
	for key, want := range map[string]int{
		"runtimeLogFacetSummary": 1,
		"runtimeLogLevelFacets":  3, // info / warn / error
		"accountCleanupTargets":  3,
		"apiKeyCleanupTargets":   3,
	} {
		if counts[key] != want {
			t.Fatalf("observability 计数 %s = %d, want %d: %v", key, counts[key], want, counts)
		}
	}
	// 属主就位的三个存储不进 pending；juhe_j3b / juhe_jobs 仍缺失。
	pendingStores := map[string]bool{}
	for _, pending := range report.OwnerStoresPending {
		pendingStores[pending.Store] = true
	}
	for _, store := range []string{"audit_log", "operation_log", "runtime_log"} {
		if pendingStores[store] {
			t.Fatalf("属主 %s 已建表仍进 pending: %+v", store, report.OwnerStoresPending)
		}
	}
	for _, store := range []string{"model_check", "account_health"} {
		if !pendingStores[store] {
			t.Fatalf("属主 %s 未建表应进 pending: %+v", store, report.OwnerStoresPending)
		}
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// 审计面：媒体端点、错误分组回填、重试尝试、payload 引用完整性。
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("audit_logs")+
		" WHERE path IN ('/v1/audio/speech', '/v1/videos')"); n < 2 {
		t.Fatalf("媒体端点审计样本不足: %d", n)
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("audit_logs")+
		" WHERE trace_id LIKE '"+CleanupTracePrefix+"usage-coverage_upstream_response_model_%'"); n != 3 {
		t.Fatalf("上游响应模型对比 trace 应恰 3 条: %d", n)
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("audit_logs")+
		" WHERE success = false AND error_group_id IS NULL"); n != 0 {
		t.Fatalf("失败样本未回填 error_group_id: %d 行", n)
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("audit_logs")+
		" WHERE attempt_count >= 2"); n < 1 {
		t.Fatalf("缺少重试样本: %d", n)
	}
	if n := observabilityQueryInt(t, db, `SELECT COUNT(*) FROM `+obsTable("audit_log_attempts")+` a
		JOIN `+obsTable("audit_logs")+` l ON l.id = a.audit_log_id
		WHERE l.attempt_count >= 2 AND a.attempt_index = 1 AND a.success = true`); n != 0 {
		t.Fatalf("重试样本的首次尝试应为失败: %d 行", n)
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("audit_payload_refs")+
		" WHERE body_blob_id IS NULL OR body_sha256 IS NULL"); n != 0 {
		t.Fatalf("payload 引用缺 blob 指针: %d 行", n)
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("audit_payload_blobs")+
		" WHERE compression = 'gzip' AND storage_key LIKE '%.gz'"); n < 1 {
		t.Fatalf("音频 data URI payload 应产生 gzip / .gz blob: %d", n)
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("audit_payload_blobs")+
		" WHERE compression = 'none' AND storage_key LIKE '%.blob'"); n < 1 {
		t.Fatalf("缺少未压缩 blob 样本: %d", n)
	}
	// ref_count 按属主 PG 语义恒为 0（gateway 不再维护该缓存列，refs 表是
	// 唯一事实源）；每个 blob 都有 refs 行指向。
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("audit_payload_blobs")+
		" WHERE ref_count <> 0"); n != 0 {
		t.Fatalf("PG 形态 ref_count 缓存列应保持 0: %d 行", n)
	}
	if n := observabilityQueryInt(t, db, `SELECT COUNT(*) FROM `+obsTable("audit_payload_blobs")+` b
		WHERE NOT EXISTS (SELECT 1 FROM `+obsTable("audit_payload_refs")+` r
			WHERE r.body_blob_id = b.id)`); n != 0 {
		t.Fatalf("存在没有任何 refs 指向的 blob: %d 行", n)
	}

	// 操作日志面：可见者推导与检索词展开。
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("operation_log_viewers")+
		" WHERE visibility_reason = 'actor_self'"); n == 0 {
		t.Fatal("缺少 actor_self 可见者样本")
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("operation_log_summary_search_terms")+
		" WHERE term = '造数'"); n == 0 {
		t.Fatal("摘要检索词应能按「造数」命中")
	}

	// 运行日志面：分面与索引行一致（分面是表的聚合视图）。
	total := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("runtime_logs"))
	facetTotal := observabilityQueryInt(t, db, "SELECT total_count FROM "+obsTable("runtime_log_facet_summary")+
		" WHERE bucket_key = '"+observabilityRuntimeFacetBucketKey+"'")
	if total == 0 || facetTotal != total {
		t.Fatalf("分面汇总 total_count=%d 与索引行数 %d 不一致", facetTotal, total)
	}
	levelSum := observabilityQueryInt(t, db, "SELECT COALESCE(SUM(count), 0) FROM "+obsTable("runtime_log_level_facets")+
		" WHERE bucket_key = '"+observabilityRuntimeFacetBucketKey+"'")
	if levelSum != total {
		t.Fatalf("level 分面合计 %d 与索引行数 %d 不一致", levelSum, total)
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("runtime_log_event_facets")+
		" WHERE latest_time IS NULL"); n != 0 {
		t.Fatalf("event 分面缺 latest_time: %d 行", n)
	}

	// 公开接口日志面：内置测试 Token、capture 状态与状态码分布。
	if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable("public_api_logs")+
		" WHERE is_test_token = 1"); n == 0 {
		t.Fatal("缺少内置测试 Token 样本")
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(DISTINCT response_capture_status) FROM "+
		obsTable("public_api_logs")); n < 2 {
		t.Fatalf("响应 capture 状态分布不足: %d", n)
	}
	if n := observabilityQueryInt(t, db, "SELECT COUNT(DISTINCT status_code) FROM "+
		obsTable("public_api_logs")); n < 3 {
		t.Fatalf("公开接口状态码分布不足: %d", n)
	}

	// 清理目标面：attempt_count 三态矩阵。
	for _, table := range []string{"account_record_cleanup_targets", "api_key_record_cleanup_targets"} {
		if n := observabilityQueryInt(t, db, "SELECT COUNT(DISTINCT attempt_count) FROM "+obsTable(table)); n != 3 {
			t.Fatalf("%s attempt_count 三态不足: %d", table, n)
		}
	}

	// 覆盖校验：observability 断言已评估且全部通过（不在 NotCovered / Errors）。
	coverage, err := VerifyCoverage(context.Background(), Options{
		DSN: dsn, Days: 3, DailyRequests: DefaultDailyRequests, SummaryFile: summaryPath,
	})
	if err != nil {
		t.Fatalf("VerifyCoverage: %v", err)
	}
	notCoveredNames := map[string]bool{}
	for _, item := range coverage.NotCovered {
		notCoveredNames[item[:strings.Index(item, "（")]] = true
	}
	for _, name := range []string{
		"audit_logs", "audit_logs.media_endpoints", "audit_error_groups", "audit_payload_blobs",
		"audit_payload_refs", "operation_logs", "operation_log_targets", "runtime_logs",
		"runtime_log_facet_summary", "runtime_log_level_facets", "public_api_logs", "public_api_logs.test_token",
	} {
		if notCoveredNames[name] {
			t.Fatalf("断言 %s 应已评估而非 NotCovered: %v", name, coverage.NotCovered)
		}
	}
	for _, item := range coverage.Empty {
		if strings.HasPrefix(item, SchemaDataset+".") {
			t.Fatalf("juhe_dataset 不应留空表: %v", coverage.Empty)
		}
	}
	for _, item := range coverage.Errors {
		if strings.HasPrefix(item, "audit_") || strings.HasPrefix(item, "operation_") ||
			strings.HasPrefix(item, "runtime_") || strings.HasPrefix(item, "public_api_logs") {
			t.Fatalf("observability 断言失败: %v", coverage.Errors)
		}
	}
}

// TestObservabilityRunIsIdempotent 重复执行（同一时钟）不叠加：第二次 Run 前的
// 清理删掉上一批（含属主哨兵表的显式规则命中），重插后全部计数逐键一致。
func TestObservabilityRunIsIdempotent(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t,
		observabilityPrepare(t, "audit", "operation", "runtime")).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first := observabilityRunForTest(t, dsn, now, "")
	second := observabilityRunForTest(t, dsn, now, "")

	firstCounts := observabilityCountsOf(first, DomainObservability)
	secondCounts := observabilityCountsOf(second, DomainObservability)
	if len(firstCounts) == 0 || len(firstCounts) != len(secondCounts) {
		t.Fatalf("两次运行的计数键不一致: %v vs %v", firstCounts, secondCounts)
	}
	for key, count := range firstCounts {
		if secondCounts[key] != count {
			t.Fatalf("重复执行叠加了 %s: first=%d second=%d", key, count, secondCounts[key])
		}
	}
	// 第二次运行的清理必须删掉第一批属主表行（显式规则命中）。
	for _, table := range []string{"audit_logs", "operation_logs", "runtime_logs"} {
		if second.Cleanup[obsTable(table)] == 0 {
			t.Fatalf("第二次清理未命中属主表 %s: %v", table, second.Cleanup)
		}
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, table := range []string{
		"audit_logs", "audit_log_attempts", "audit_payload_refs", "operation_logs",
		"runtime_logs", "public_api_logs",
	} {
		if n := observabilityQueryInt(t, db, "SELECT COUNT(*) FROM "+obsTable(table)); n == 0 {
			t.Fatalf("第二次运行后 %s 不应有残留空表", table)
		}
	}
}

// TestObservabilityCleanupSweepsOwnerTables 钉住清理语义：显式规则 + 通用扫描把
// 属主表造数行清空；内容寻址的 blob 行保留（按唯一键幂等复用，属主保留期由
// gateway 的 audit retention 负责）；分面行保留（聚合视图由下次 seed 重算）。
func TestObservabilityCleanupSweepsOwnerTables(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t,
		observabilityPrepare(t, "audit", "operation", "runtime")).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	observabilityRunForTest(t, dsn, now, "")

	ctx := context.Background()
	e, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	deleted, skipped, err := cleanupAll(ctx, e, nil)
	if err != nil {
		t.Fatalf("cleanupAll: %v", err)
	}
	for _, table := range []string{"audit_logs", "operation_logs", "runtime_logs"} {
		if deleted[obsTable(table)] == 0 {
			t.Fatalf("清理未命中属主表 %s: %v", table, deleted)
		}
	}
	_ = skipped
	count := func(query string) int {
		t.Helper()
		var n int
		if err := e.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, table := range []string{
		"audit_logs", "audit_log_attempts", "audit_payload_refs", "audit_error_groups",
		"operation_logs", "operation_log_targets", "operation_log_viewers",
		"operation_log_summary_search_terms", "runtime_logs", "public_api_logs",
		"account_record_cleanup_targets", "api_key_record_cleanup_targets",
	} {
		if n := count("SELECT COUNT(*) FROM " + obsTable(table)); n != 0 {
			t.Fatalf("清理后 %s 仍有 %d 行", table, n)
		}
	}
	if n := count("SELECT COUNT(*) FROM " + obsTable("audit_payload_blobs")); n == 0 {
		t.Fatal("内容寻址 blob 行应保留（幂等复用，不清扫）")
	}
	if n := count("SELECT COUNT(*) FROM " + obsTable("runtime_log_facet_summary")); n == 0 {
		t.Fatal("分面聚合行应保留（由下次 seed 重算，不属清理面）")
	}
	// 第二次清理：全部 no-op。
	second, _, err := cleanupAll(ctx, e, nil)
	if err != nil {
		t.Fatalf("第二次 cleanupAll: %v", err)
	}
	for table, rows := range second {
		if rows != 0 {
			t.Fatalf("第二次清理仍删除 %s %d 行", table, rows)
		}
	}
	// 再造数一次：分面重算后与索引行重新一致，全部计数可复现。
	report := observabilityRunForTest(t, dsn, now, "")
	counts := observabilityCountsOf(report, DomainObservability)
	if counts["runtimeLogs"] == 0 || counts["runtimeLogFacetSummary"] != 1 {
		t.Fatalf("重建后运行日志计数异常: %v", counts)
	}
	if total := count("SELECT COUNT(*) FROM " + obsTable("runtime_logs")); total != counts["runtimeLogs"] {
		t.Fatalf("runtime_logs 行数 %d 与计数 %d 不一致", total, counts["runtimeLogs"])
	}
}

// TestObservabilitySkipsMissingOwnerStores 钉住属主缺失语义：三个属主段整体
// 跳过（不写半批、不复制 DDL），maintenance 面照常产出；覆盖断言按「属主存储
// 缺失」进 NotCovered 而不阻塞为失败。
func TestObservabilitySkipsMissingOwnerStores(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, func(ctx context.Context, target *sql.DB) error {
		_, err := schema.EnsurePostgres(ctx, target)
		return err
	}).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	summaryPath := coverageSummaryPath(t)
	report := observabilityRunForTest(t, dsn, now, summaryPath)

	pendingStores := map[string]bool{}
	for _, pending := range report.OwnerStoresPending {
		pendingStores[pending.Store] = true
	}
	for _, store := range []string{"audit_log", "operation_log", "runtime_log"} {
		if !pendingStores[store] {
			t.Fatalf("属主 %s 未建表应进 pending: %+v", store, report.OwnerStoresPending)
		}
	}
	counts := observabilityCountsOf(report, DomainObservability)
	if counts == nil {
		t.Fatalf("observability 域应至少产出 maintenance 面: %+v", report.Domains)
	}
	for _, key := range []string{"auditLogs", "operationLogs", "runtimeLogs"} {
		if _, present := counts[key]; present {
			t.Fatalf("属主缺失时不应产出 %s: %v", key, counts)
		}
	}
	if counts["publicApiLogs"] == 0 || counts["accountCleanupTargets"] != 3 {
		t.Fatalf("maintenance 面应照常产出: %v", counts)
	}

	coverage, err := VerifyCoverage(context.Background(), Options{
		DSN: dsn, Days: 3, DailyRequests: DefaultDailyRequests, SummaryFile: summaryPath,
	})
	if err != nil {
		t.Fatalf("VerifyCoverage: %v", err)
	}
	wantPendingReason := map[string]string{
		"audit_logs":                "属主存储 audit_log 缺失",
		"operation_logs":            "属主存储 operation_log 缺失",
		"runtime_logs":              "属主存储 runtime_log 缺失",
		"runtime_log_facet_summary": "属主存储 runtime_log 缺失",
	}
	for name, reason := range wantPendingReason {
		found := false
		for _, item := range coverage.NotCovered {
			if strings.HasPrefix(item, name+"（") && strings.Contains(item, reason) {
				found = true
			}
		}
		if !found {
			t.Fatalf("断言 %s 应按 %q 进 NotCovered: %v", name, reason, coverage.NotCovered)
		}
	}
	// maintenance 面断言已评估：public_api_logs 不在 NotCovered、Errors 无它。
	for _, item := range coverage.NotCovered {
		if strings.HasPrefix(item, "public_api_logs（") {
			t.Fatalf("public_api_logs 断言不应 NotCovered: %v", coverage.NotCovered)
		}
	}
	for _, item := range coverage.Errors {
		if strings.HasPrefix(item, "public_api_logs") {
			t.Fatalf("public_api_logs 断言不应失败: %v", coverage.Errors)
		}
	}
}

// TestObservabilityPartialOwnerStoreSkip 钉住「只建了部分属主 schema」的中间态：
// audit 段产出、operation / runtime 段跳过，pending 只列真正缺失的属主。
func TestObservabilityPartialOwnerStoreSkip(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, observabilityPrepare(t, "audit")).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	report := observabilityRunForTest(t, dsn, now, "")

	pendingStores := map[string]bool{}
	for _, pending := range report.OwnerStoresPending {
		pendingStores[pending.Store] = true
	}
	if pendingStores["audit_log"] {
		t.Fatalf("audit_log 已建表不应 pending: %+v", report.OwnerStoresPending)
	}
	for _, store := range []string{"operation_log", "runtime_log"} {
		if !pendingStores[store] {
			t.Fatalf("属主 %s 应 pending: %+v", store, report.OwnerStoresPending)
		}
	}
	counts := observabilityCountsOf(report, DomainObservability)
	for _, key := range []string{"auditLogs", "auditErrorGroups", "auditPayloadBlobs"} {
		if counts[key] == 0 {
			t.Fatalf("audit 段应产出 %s: %v", key, counts)
		}
	}
	for _, key := range []string{"operationLogs", "runtimeLogs"} {
		if _, present := counts[key]; present {
			t.Fatalf("缺失属主段不应产出 %s: %v", key, counts)
		}
	}
}

// TestObservabilityCleanupOrderPlacesChildrenBeforeSentinels 钉住属主哨兵表加入
// 清理拓扑后的删除顺序：外键子表先于哨兵父表。
func TestObservabilityCleanupOrderPlacesChildrenBeforeSentinels(t *testing.T) {
	db := pgtestkit.NewTestDatabase(t,
		observabilityPrepare(t, "audit", "operation", "runtime")).DB
	order, err := cleanupOrder(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	position := map[string]int{}
	for index, table := range order {
		if _, seen := position[table]; seen {
			t.Fatalf("清理顺序重复出现 %s", table)
		}
		position[table] = index
	}
	pairs := [][2]string{
		{qualified(SchemaDataset, "audit_payload_refs"), qualified(SchemaDataset, "audit_payload_blobs")},
		{qualified(SchemaDataset, "audit_payload_refs"), qualified(SchemaDataset, "audit_logs")},
		{qualified(SchemaDataset, "audit_log_attempts"), qualified(SchemaDataset, "audit_logs")},
		{qualified(SchemaDataset, "operation_log_targets"), qualified(SchemaDataset, "operation_logs")},
		{qualified(SchemaDataset, "operation_log_viewers"), qualified(SchemaDataset, "operation_logs")},
	}
	for _, pair := range pairs {
		childPos, childOK := position[pair[0]]
		parentPos, parentOK := position[pair[1]]
		if !childOK || !parentOK {
			t.Fatalf("清理顺序缺表: child=%v parent=%v", childOK, parentOK)
		}
		if childPos >= parentPos {
			t.Fatalf("清理顺序错误: %s 必须先于 %s", pair[0], pair[1])
		}
	}
	// 属主哨兵表必须在拓扑序里（缺失场景由 cleanupTable 记 skipped，不在这里）。
	for _, table := range []string{"audit_logs", "operation_logs", "runtime_logs"} {
		if _, ok := position[obsTable(table)]; !ok {
			t.Fatalf("清理顺序缺少属主哨兵表 %s", table)
		}
	}
}

// TestObservabilityPublicLogsNeedSources 钉住公开接口日志的来源契约：没有可用
// 来源 + Token 组合时跳过该子段（不伪造来源），跳过可从 Counts 与日志追溯。
func TestObservabilityPublicLogsNeedSources(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, func(ctx context.Context, target *sql.DB) error {
		_, err := schema.EnsurePostgres(ctx, target)
		return err
	}).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	e, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1, Now: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	// business 域未产出（空库无 mock 来源，内置测试来源也无 Token join 行）：
	// 兜底用户可引用，但来源集合只可能为空或内置——这里直接用空引用验证
	// 「来源为空 → 子段跳过」的分支本身。
	refs := observabilityCollectReferences(context.Background(), e)
	counts := map[string]int{}
	if err := observabilitySeedPublicAPILogs(context.Background(), e, refs, now, counts); err != nil {
		t.Fatalf("公开接口日志子段: %v", err)
	}
	if _, present := counts["publicApiLogs"]; present {
		t.Fatalf("无来源时应跳过而不是伪造: %v", counts)
	}
}

// observabilityArtifactShapeCheck 复核审计 blob 的 storage_key 与 gateway 现行
// 语义逐字对齐（sha256/<digest>-<shortHash8><.blob|.gz>）——防漂移的纯函数断言。
func TestObservabilityBlobSemanticsParity(t *testing.T) {
	raw := []byte(strings.Repeat(`{"mockdata":true}`, 400)) // > 4KiB 可压缩 JSON
	blob := observabilityBuildBlob("application/json", raw)
	if blob.Compression != "gzip" || !strings.HasSuffix(blob.StorageKey, ".gz") {
		t.Fatalf("大 JSON 应走 gzip/.gz: %+v", blob)
	}
	if blob.RawSize != len(raw) || blob.CompressedSize >= blob.RawSize {
		t.Fatalf("压缩尺寸自洽性失败: raw=%d compressed=%d", blob.RawSize, blob.CompressedSize)
	}
	if !strings.HasPrefix(blob.ID, "blob:") || len(strings.Split(blob.ID, ":")) != 3 {
		t.Fatalf("blob id 形态错误: %s", blob.ID)
	}
	small := observabilityBuildBlob("application/json", []byte(`{"mockdata":true}`))
	if small.Compression != "none" || !strings.HasSuffix(small.StorageKey, ".blob") {
		t.Fatalf("小 payload 应保持 none/.blob: %+v", small)
	}
	wav := observabilityBuildBlob("audio/wav", chatCodexWAV())
	if wav.Compression != "none" {
		t.Fatalf("audio/wav 不可文本压缩: %+v", wav)
	}
	// 同内容两次构造必须得到同一 id / storage_key（内容寻址幂等的根基）。
	again := observabilityBuildBlob("application/json", raw)
	if again.ID != blob.ID || again.StorageKey != blob.StorageKey {
		t.Fatalf("同内容 blob 身份不稳定: %s vs %s", again.ID, blob.ID)
	}
}

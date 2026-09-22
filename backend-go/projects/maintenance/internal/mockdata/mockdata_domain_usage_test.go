package mockdata

// 用量域测试：在临时数据根上跑 business 域（提供真实外键）+ usage 域，然后按
// coverage.go 的断言、设计文档的样本矩阵、分片文件与 catalog / stats 镜像的一致性、
// 健康检查逐小时覆盖与幂等性逐条核对。
//
// 为什么把 jobs 的列定义读进测试：maintenance 模块不能 import jobs（Go 三项目基线
// 禁止跨项目 import），而「列名 / 列序写错」正是本域最容易犯、又只能靠真实结构
// 暴露的错误。列序以 jobs usagewriter/rows.go 的源文本为准（能读到时），表结构以
// schema.EnsureSQLiteUsageShard 建出的真实表为准，两条路径同时钉住。
//
// 全部数据写在 t.TempDir() 下：造数最危险的失败模式是写错数据根，测试绝不碰
// .local/dev/data。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

const (
	// usageWriterRowsSource 是 jobs usagewriter 列定义的权威源（相对 mockdata 包目录）。
	usageWriterRowsSource = "../../../../projects/jobs/internal/usagewriter/rows.go"

	// usageDailyTracePattern 匹配日常记录 trace（区分逐小时健康记录与定向覆盖样本）。
	usageDailyTracePattern = CleanupTracePrefix + `usage-\d{5}`
)

// usageShardPathPattern 是 jobs usagewriter 的权威分片路径形态：
// <root>/YYYY/MM/DD/usage-YYYYMMDD-sNN.sqlite3。
var usageShardPathPattern = regexp.MustCompile(`[\\/](\d{4})[\\/](\d{2})[\\/](\d{2})[\\/]usage-(\d{8})-s(\d{2})\.sqlite3$`)

var usageDailyTraceRegexp = regexp.MustCompile("^" + usageDailyTracePattern + "$")

// usageTestOptions 返回本域测试使用的 Options（时间基准固定，行数可控）。
func usageTestOptions(days, daily int) Options {
	return Options{Days: days, DailyRequests: daily, Now: businessTestNow}
}

// usageEnvAtRoot 在给定数据根上建 env、跑 business 域与 usage 域，并把域结果
// 记账进 env（覆盖校验据此按「已接线」评估）。不跑清理：清理语义由幂等测试单独覆盖。
func usageEnvAtRoot(t *testing.T, root string, options Options) (*env, DomainResult) {
	t.Helper()
	paths, err := ResolvePaths(root, "", envMap(nil))
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
	result, err := seedUsage(context.Background(), e)
	if err != nil {
		t.Fatalf("seedUsage: %v", err)
	}
	e.recordDomainResult(result)
	return e, result
}

// usageTestEnv 用专门的临时数据根跑一次用量域。
func usageTestEnv(t *testing.T, options Options) (*env, DomainResult) {
	t.Helper()
	return usageEnvAtRoot(t, businessTestRoot(t), options)
}

// usageCoverageEnv 用「新进程视角」重建 env：分片文件已在磁盘上，枚举得到全部分片，
// 断言按 usage 域已接线评估（与 --verify-mockdata-coverage 的独立进程一致）。
func usageCoverageEnv(t *testing.T, root string, options Options, results ...DomainResult) *env {
	t.Helper()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	options.Paths = paths
	options.Now = businessTestNow
	e := newEnv(options, nil)
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Errorf("关闭 mockdata 存储: %v", err)
		}
	})
	for _, result := range results {
		e.recordDomainResult(result)
	}
	return e
}

// usageOpenShard 打开一个分片文件用于只读核对。
func usageOpenShard(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("打开分片 %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// usageScalar 执行返回单个整数的查询。
func usageScalar(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("查询 %q: %v", query, err)
	}
	return value
}

// usageShardRow 是一条分片使用记录的关键列（样本矩阵断言用）。
type usageShardRow struct {
	shardKey              string
	TraceID               string
	TrafficSource         string
	Endpoint              string
	Success               int
	StatusCode            sql.NullInt64
	Stream                int
	BilledServiceTier     string
	RequestedServiceTier  string
	RequestedEffort       sql.NullString
	EffectiveEffort       sql.NullString
	ModelMappingApplied   int
	ModelMappingSource    sql.NullString
	UpstreamModel         sql.NullString
	UpstreamResponseModel sql.NullString
	Model                 sql.NullString
	ClientIP              sql.NullString
	CostBreakdown         sql.NullString
	InputImageTokens      sql.NullInt64
	OutputImageTokens     sql.NullInt64
	ErrorCode             sql.NullString
	AccountID             sql.NullString
	GroupID               sql.NullString
	APIKeyID              sql.NullString
	SystemAccountID       string
	CreatedAt             string
}

// usageLoadShardRows 打开本次造数写出的全部分片，逐片读出关键列。
func usageLoadShardRows(t *testing.T, paths Paths) []usageShardRow {
	t.Helper()
	stores := paths.usageShardStores()
	if len(stores) == 0 {
		t.Fatal("造数后必须能枚举出分片文件")
	}
	var rows []usageShardRow
	for _, item := range stores {
		db := usageOpenShard(t, item.Path)
		result, err := db.QueryContext(context.Background(), `
			SELECT trace_id, traffic_source, endpoint, success, status_code, stream,
				billed_service_tier, requested_service_tier, requested_reasoning_effort,
				effective_reasoning_effort, model_mapping_applied, model_mapping_source,
				upstream_model, upstream_response_model, model, client_ip,
				cost_breakdown_snapshot_json, input_image_tokens, output_image_tokens,
				error_code, account_id, group_id, api_key_id, system_account_id, created_at
			FROM usage_records ORDER BY id`)
		if err != nil {
			t.Fatalf("读取分片 %s: %v", item.Name, err)
		}
		for result.Next() {
			row := usageShardRow{shardKey: item.Name}
			if err := result.Scan(&row.TraceID, &row.TrafficSource, &row.Endpoint, &row.Success,
				&row.StatusCode, &row.Stream, &row.BilledServiceTier, &row.RequestedServiceTier,
				&row.RequestedEffort, &row.EffectiveEffort, &row.ModelMappingApplied, &row.ModelMappingSource,
				&row.UpstreamModel, &row.UpstreamResponseModel, &row.Model, &row.ClientIP,
				&row.CostBreakdown, &row.InputImageTokens, &row.OutputImageTokens,
				&row.ErrorCode, &row.AccountID, &row.GroupID, &row.APIKeyID, &row.SystemAccountID,
				&row.CreatedAt); err != nil {
				result.Close()
				t.Fatalf("扫描分片 %s 行: %v", item.Name, err)
			}
			rows = append(rows, row)
		}
		if err := result.Err(); err != nil {
			result.Close()
			t.Fatalf("遍历分片 %s: %v", item.Name, err)
		}
		result.Close()
	}
	return rows
}

// usageTraceRow 按 trace 找唯一记录（三条上游响应模型样本各只有一条）。
func usageTraceRow(t *testing.T, rows []usageShardRow, traceID string) usageShardRow {
	t.Helper()
	var found []usageShardRow
	for _, row := range rows {
		if row.TraceID == traceID {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("trace %s 命中 %d 行，要求恰好 1 行", traceID, len(found))
	}
	return found[0]
}

// TestSeedUsageSatisfiesCoverageAssertions 逐条核对 coverage.go 中归属 usage 域
// 的关键状态断言：域已接线时它们就是硬门槛。
func TestSeedUsageSatisfiesCoverageAssertions(t *testing.T) {
	root := businessTestRoot(t)
	_, result := usageEnvAtRoot(t, root, usageTestOptions(7, 20))
	if len(result.Counts) == 0 {
		t.Fatal("usage 域必须返回非空 Counts，否则覆盖报告会把它当成未接线")
	}
	// 覆盖断言按「独立进程视角」核对：重建 env 后分片已落盘，枚举得到全部分片
	// （域刚写完时 env 的分片清单还是空的，那是懒加载约定而不是缺陷）。
	verify := usageCoverageEnv(t, root, usageTestOptions(7, 20), result)
	assertions := 0
	for _, assertion := range coverageAssertions() {
		if assertion.Domain != DomainUsage {
			continue
		}
		assertions++
		if !strings.HasPrefix(assertion.Store, StoreUsageShardPrefix+"[") {
			t.Fatalf("usage 断言应指向分片前缀 %s[：%s", StoreUsageShardPrefix, assertion.Name)
		}
		total := 0
		for _, item := range verify.stores() {
			if !strings.HasPrefix(item.Name, StoreUsageShardPrefix+"[") {
				continue
			}
			db, err := verify.openExisting(item.Name)
			if err != nil {
				t.Fatal(err)
			}
			total += usageScalar(t, db, assertion.Query)
		}
		if total < assertion.Min {
			t.Errorf("%s: 命中 %d 行，要求至少 %d 行", assertion.Name, total, assertion.Min)
		}
	}
	if assertions == 0 {
		t.Fatal("coverage.go 里必须存在 usage 域断言（口径变更时本测试要同步）")
	}
	report, err := VerifyCoverage(context.Background(), verify)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range report.NotCovered {
		if strings.Contains(item, "域 "+DomainUsage) {
			t.Errorf("usage 已接线但断言仍被标记未覆盖: %s", item)
		}
	}
	for _, item := range report.Errors {
		if strings.Contains(item, "usage_records") {
			t.Errorf("usage 断言失败: %s", item)
		}
	}
}

// TestSeedUsageColumnMirrorMatchesJobsAuthority 钉住三处列定义：
//   - usageRecordColumns 与 jobs usagewriter/rows.go 的 UsageRecordColumns 逐字一致；
//   - 分片表的真实列序（schema.EnsureSQLiteUsageShard 建出）与 usageRecordColumns 一致；
//   - stats 镜像表的列覆盖 statsUsageRecordColumns。
func TestSeedUsageColumnMirrorMatchesJobsAuthority(t *testing.T) {
	raw, err := os.ReadFile(usageWriterRowsSource)
	if err != nil {
		t.Skipf("jobs usagewriter 源文本不可读（%v），本 checkout 无法核对列序镜像", err)
	}
	jobsColumns := usageExtractStringLiteralList(t, string(raw), "var UsageRecordColumns = []string{")
	if len(jobsColumns) == 0 {
		t.Fatalf("未能从 %s 解析 UsageRecordColumns", usageWriterRowsSource)
	}
	if len(jobsColumns) != len(usageRecordColumns) {
		t.Fatalf("列数不一致：maintenance %d 列、jobs %d 列", len(usageRecordColumns), len(jobsColumns))
	}
	for index := range jobsColumns {
		if jobsColumns[index] != usageRecordColumns[index] {
			t.Fatalf("列序第 %d 项不一致：maintenance %q、jobs %q", index, usageRecordColumns[index], jobsColumns[index])
		}
	}

	// 真实分片表：列名与列序必须与 usageRecordColumns 相同（INSERT 按列序绑定参数）。
	dir := t.TempDir()
	path := filepath.Join(dir, "usage-shard.sqlite3")
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := schema.EnsureSQLiteUsageShard(context.Background(), db); err != nil {
		t.Fatalf("应用分片 schema: %v", err)
	}
	columns, err := queryTableColumns(context.Background(), db, "usage_records")
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != len(usageRecordColumns) {
		t.Fatalf("分片表列数 %d，usageRecordColumns %d", len(columns), len(usageRecordColumns))
	}
	for index, column := range columns {
		if column.Name != usageRecordColumns[index] {
			t.Fatalf("分片表第 %d 列 %q，usageRecordColumns 期望 %q", index, column.Name, usageRecordColumns[index])
		}
	}

	// stats 镜像：列集合必须被真实镜像表覆盖（镜像 INSERT 只按列名绑定）。
	e, _ := usageTestEnv(t, usageTestOptions(1, 2))
	mirror, err := e.openExisting(StoreStats)
	if err != nil || mirror == nil {
		t.Fatalf("stats 库应已创建镜像表: %v", err)
	}
	mirrorColumns, err := queryTableColumns(context.Background(), mirror, "usage_records")
	if err != nil {
		t.Fatal(err)
	}
	available := map[string]bool{}
	for _, column := range mirrorColumns {
		available[column.Name] = true
	}
	for _, column := range statsUsageRecordColumns {
		if !available[column] {
			t.Errorf("stats 镜像表 usage_records 缺列 %q（statsUsageRecordColumns 要求存在）", column)
		}
	}
}

// usageExtractStringLiteralList 从 Go 源文本里取指定 `[]string{` 声明后的全部
// 字符串字面量（列清单）。
func usageExtractStringLiteralList(t *testing.T, source, marker string) []string {
	t.Helper()
	start := strings.Index(source, marker)
	if start < 0 {
		return nil
	}
	rest := source[start+len(marker):]
	end := strings.Index(rest, "}")
	if end < 0 {
		return nil
	}
	literalPattern := regexp.MustCompile(`"([A-Za-z0-9_]+)"`)
	var values []string
	for _, match := range literalPattern.FindAllStringSubmatch(rest[:end], -1) {
		values = append(values, match[1])
	}
	return values
}

// TestSeedUsageShardCatalogAndStatsMirrorConsistency 核对「分片文件 → catalog →
// stats 镜像」三层一致：路径形态、行数、日期跨度都对齐。
func TestSeedUsageShardCatalogAndStatsMirrorConsistency(t *testing.T) {
	const days, daily = 7, 20
	e, result := usageTestEnv(t, usageTestOptions(days, daily))
	paths := e.options.Paths

	catalog, err := e.openExisting(StoreUsageCatalog)
	if err != nil || catalog == nil {
		t.Fatalf("usage-catalog 应已创建: %v", err)
	}
	files := paths.usageShardStores()
	if len(files) == 0 {
		t.Fatal("分片文件必须落地")
	}
	registered := map[string]string{}
	rows, err := catalog.QueryContext(context.Background(),
		`SELECT shard_key, file_path FROM usage_record_shards WHERE status = 'active'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var shardKey, filePath string
		if err := rows.Scan(&shardKey, &filePath); err != nil {
			t.Fatal(err)
		}
		registered[shardKey] = filePath
	}
	rows.Close()
	if len(registered) != len(files) {
		t.Fatalf("catalog 登记 %d 个分片，磁盘上 %d 个", len(registered), len(files))
	}
	shardRecords := 0
	for _, item := range files {
		match := usageShardPathPattern.FindStringSubmatch(item.Path)
		if match == nil {
			t.Fatalf("分片路径不符合 jobs 布局 <root>/YYYY/MM/DD/usage-YYYYMMDD-sNN.sqlite3: %s", item.Path)
		}
		if match[1]+match[2]+match[3] != match[4] {
			t.Fatalf("分片目录日期 %s%s%s 与文件名日期 %s 不一致", match[1], match[2], match[3], match[4])
		}
		shardKey := match[4] + ":s" + match[5]
		filePath, ok := registered[shardKey]
		if !ok {
			t.Fatalf("分片 %s 未在 usage_record_shards 登记", shardKey)
		}
		if filepath.Clean(filePath) != filepath.Clean(item.Path) {
			t.Fatalf("catalog file_path %q 与磁盘路径 %q 不一致", filePath, item.Path)
		}
		if !filepath.IsAbs(filePath) {
			t.Fatalf("catalog file_path 必须是绝对路径: %q", filePath)
		}
		db := usageOpenShard(t, item.Path)
		shardRecords += usageScalar(t, db, "SELECT COUNT(*) FROM usage_records")
	}
	if want := result.Counts["usageRecords"]; shardRecords != want {
		t.Fatalf("分片内记录数 %d，报告 usageRecords %d", shardRecords, want)
	}
	if got, want := result.Counts["usageRecordShards"], len(files); got != want {
		t.Fatalf("报告 usageRecordShards %d，磁盘分片 %d", got, want)
	}

	// catalog 的逐行条目与账户 / Key 作用域目录都必须与记录数对齐。
	if got, want := usageScalar(t, catalog, "SELECT COUNT(*) FROM usage_record_shard_entries"), result.Counts["usageRecords"]; got != want {
		t.Fatalf("usage_record_shard_entries %d 行，usageRecords %d", got, want)
	}
	if usageScalar(t, catalog, "SELECT COUNT(*) FROM usage_record_account_shards") == 0 {
		t.Error("usage_record_account_shards 必须有账户作用域行")
	}
	if usageScalar(t, catalog, "SELECT COUNT(*) FROM usage_record_api_key_shards") == 0 {
		t.Error("usage_record_api_key_shards 必须有 Key 作用域行")
	}

	// stats 镜像行数与分片一致（statsagg 的唯一输入源）。
	stats, err := e.openExisting(StoreStats)
	if err != nil || stats == nil {
		t.Fatalf("stats 库应已创建镜像: %v", err)
	}
	if got, want := usageScalar(t, stats, "SELECT COUNT(*) FROM usage_records"), result.Counts["usageRecords"]; got != want {
		t.Fatalf("stats 镜像 %d 行，usageRecords %d", got, want)
	}

	// 日常记录必须是「近 Days 天 × 每天 DailyRequests 条」，时间落在基准时间之前。
	dailyRows := 0
	daysSeen := map[string]bool{}
	for _, row := range usageLoadShardRows(t, paths) {
		createdAt, err := time.Parse(isoMillisLayout, row.CreatedAt)
		if err != nil {
			t.Fatalf("记录时间 %q 不是毫秒 ISO: %v", row.CreatedAt, err)
		}
		if createdAt.After(businessTestNow) {
			t.Fatalf("记录时间 %s 晚于基准时间 %s", row.CreatedAt, businessTestNow.Format(isoMillisLayout))
		}
		if usageDailyTraceRegexp.MatchString(row.TraceID) {
			dailyRows++
			daysSeen[createdAt.Format("2006-01-02")] = true
		}
	}
	if want := days * daily; dailyRows != want {
		t.Fatalf("日常记录 %d 条，期望 Days×DailyRequests=%d", dailyRows, want)
	}
	if len(daysSeen) != days {
		t.Fatalf("日常记录覆盖 %d 天，Options.Days=%d", len(daysSeen), days)
	}
}

// TestSeedUsageSampleMatrix 核对设计文档要求的样本矩阵。
func TestSeedUsageSampleMatrix(t *testing.T) {
	e, _ := usageTestEnv(t, usageTestOptions(7, 20))
	rows := usageLoadShardRows(t, e.options.Paths)
	if len(rows) == 0 {
		t.Fatal("必须有使用记录")
	}
	trafficSources := map[string]bool{}
	endpoints := map[string]bool{}
	statusCodes := map[int64]bool{}
	streams := map[int]bool{}
	billedTiers := map[string]bool{}
	efforts := map[string]bool{}
	mappingSources := map[string]bool{}
	clientIPFamilies := map[string]bool{}
	imageTokenRows := 0
	mappingRows := 0
	costBreakdownRows := 0
	for _, row := range rows {
		trafficSources[row.TrafficSource] = true
		endpoints[row.Endpoint] = true
		if row.StatusCode.Valid {
			statusCodes[row.StatusCode.Int64] = true
		}
		streams[row.Stream] = true
		billedTiers[row.BilledServiceTier] = true
		if row.RequestedEffort.Valid {
			efforts[row.RequestedEffort.String] = true
		}
		if row.EffectiveEffort.Valid {
			efforts[row.EffectiveEffort.String] = true
		}
		if row.ModelMappingSource.Valid {
			mappingSources[row.ModelMappingSource.String] = true
		}
		if row.ModelMappingApplied == 1 && row.ModelMappingSource.Valid {
			mappingRows++
		}
		if row.InputImageTokens.Valid || row.OutputImageTokens.Valid {
			imageTokenRows++
		}
		if row.CostBreakdown.Valid && row.CostBreakdown.String != "" {
			costBreakdownRows++
			var document map[string]any
			if err := json.Unmarshal([]byte(row.CostBreakdown.String), &document); err != nil {
				t.Fatalf("计价快照不是合法 JSON: %v", err)
			}
		}
		if row.ClientIP.Valid {
			switch {
			case strings.HasPrefix(row.ClientIP.String, "10.10."):
				clientIPFamilies["10.10."] = true
			case strings.HasPrefix(row.ClientIP.String, "10.20."):
				clientIPFamilies["10.20."] = true
			default:
				t.Fatalf("client_ip %q 不在 10.10.x.x / 10.20.x.x 网段", row.ClientIP.String)
			}
		}
	}
	for _, want := range []string{"gateway", "manual_account_test", "account_health_check", "cooldown_retest", "runtime_recovery_probe"} {
		if !trafficSources[want] {
			t.Errorf("traffic_source 缺样本 %q", want)
		}
	}
	for _, want := range []string{"/v1/responses", "/v1/chat/completions", "/v1/images/generations", "/v1/models", "/v1/messages", "/v1/messages/count_tokens"} {
		found := false
		for endpoint := range endpoints {
			if strings.HasSuffix(endpoint, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("endpoint 缺样本 %q（现有 %v）", want, sortedKeys(endpoints))
		}
	}
	for _, want := range []int64{429, 503, 500, 402, 401} {
		if !statusCodes[want] {
			t.Errorf("失败样本缺状态码 %d", want)
		}
	}
	for _, want := range []int{0, 1} {
		if !streams[want] {
			t.Errorf("stream 缺取值 %d", want)
		}
	}
	for _, want := range []string{"priority", "flex", "default"} {
		if !billedTiers[want] {
			t.Errorf("billed_service_tier 缺取值 %q", want)
		}
	}
	for _, want := range []string{"low", "medium", "high"} {
		if !efforts[want] {
			t.Errorf("reasoning effort 缺取值 %q", want)
		}
	}
	if imageTokenRows == 0 {
		t.Error("必须有带 input/output image token 的图像模型记录")
	}
	if mappingRows == 0 {
		t.Error("必须有 model_mapping_applied=1 且 model_mapping_source 非空的样本")
	}
	if len(mappingSources) == 0 {
		t.Error("model_mapping_source 必须有取值")
	}
	if costBreakdownRows == 0 {
		t.Error("必须有 cost_breakdown_snapshot_json 非空的样本（Priority / Flex 档位）")
	}
	if len(clientIPFamilies) != 2 {
		t.Errorf("client_ip 应覆盖 10.10. 与 10.20. 两个网段，现有 %v", sortedKeys(clientIPFamilies))
	}
}

// TestSeedUsageUpstreamResponseModelSamples 核对三条上游响应模型定向样本的字段组合。
func TestSeedUsageUpstreamResponseModelSamples(t *testing.T) {
	e, _ := usageTestEnv(t, usageTestOptions(7, 20))
	rows := usageLoadShardRows(t, e.options.Paths)

	match := usageTraceRow(t, rows, usageTraceUpstreamMatch)
	if !match.UpstreamModel.Valid || !match.UpstreamResponseModel.Valid {
		t.Fatal("match 样本的 upstream_model 与 upstream_response_model 都必须非空")
	}
	if match.UpstreamModel.String != match.UpstreamResponseModel.String {
		t.Fatalf("match 样本要求两个模型字段相等：%q / %q", match.UpstreamModel.String, match.UpstreamResponseModel.String)
	}
	if match.ModelMappingApplied != 1 {
		t.Fatalf("match 样本必须 model_mapping_applied=1，实际 %d", match.ModelMappingApplied)
	}

	mismatch := usageTraceRow(t, rows, usageTraceUpstreamMismatch)
	if !mismatch.UpstreamModel.Valid || !mismatch.UpstreamResponseModel.Valid {
		t.Fatal("mismatch 样本的 upstream_model 与 upstream_response_model 都必须非空")
	}
	if mismatch.UpstreamModel.String == mismatch.UpstreamResponseModel.String {
		t.Fatal("mismatch 样本要求两个模型字段不等（映射后不一致）")
	}
	if mismatch.ModelMappingApplied != 1 {
		t.Fatalf("mismatch 样本必须 model_mapping_applied=1，实际 %d", mismatch.ModelMappingApplied)
	}

	unmapped := usageTraceRow(t, rows, usageTraceUpstreamUnmappedMismatch)
	if !unmapped.UpstreamModel.Valid || !unmapped.UpstreamResponseModel.Valid {
		t.Fatal("unmapped_mismatch 样本的 upstream_model 与 upstream_response_model 都必须非空")
	}
	if unmapped.UpstreamModel.String == unmapped.UpstreamResponseModel.String {
		t.Fatal("unmapped_mismatch 样本要求两个模型字段不等（无映射不一致）")
	}
	if unmapped.ModelMappingApplied != 0 {
		t.Fatalf("unmapped_mismatch 样本必须 model_mapping_applied=0，实际 %d", unmapped.ModelMappingApplied)
	}
	// 未映射语义：请求模型本身就是要发送的上游模型，不能与映射样本共用映射对。
	if unmapped.Model.Valid && unmapped.Model.String != unmapped.UpstreamModel.String {
		t.Fatalf("unmapped_mismatch 的请求模型 %q 必须等于实际发送模型 %q", unmapped.Model.String, unmapped.UpstreamModel.String)
	}
}

// TestSeedUsageHealthRecordsCoverEveryAccountHourly 核对健康检查来源的逐小时覆盖：
// 每个 mock 账户都有记录、存在缺口小时、存在失败小时。
func TestSeedUsageHealthRecordsCoverEveryAccountHourly(t *testing.T) {
	const days = 7
	e, _ := usageTestEnv(t, usageTestOptions(days, 20))
	rows := usageLoadShardRows(t, e.options.Paths)
	rangeHours := days * 24
	hoursByAccount := map[string]map[string]bool{}
	failuresByAccount := map[string]int{}
	for _, row := range rows {
		if !strings.HasPrefix(row.TraceID, CleanupTracePrefix+"usage-health-") {
			continue
		}
		if !row.AccountID.Valid {
			t.Fatalf("健康检查记录 %s 缺 account_id", row.TraceID)
		}
		if row.TrafficSource != "account_health_check" {
			t.Fatalf("健康检查记录 %s 的 traffic_source = %q", row.TraceID, row.TrafficSource)
		}
		if hoursByAccount[row.AccountID.String] == nil {
			hoursByAccount[row.AccountID.String] = map[string]bool{}
		}
		createdAt, err := time.Parse(isoMillisLayout, row.CreatedAt)
		if err != nil {
			t.Fatalf("健康检查记录时间 %q: %v", row.CreatedAt, err)
		}
		hoursByAccount[row.AccountID.String][createdAt.Format("2006-01-02T15")] = true
		if row.Success == 0 {
			failuresByAccount[row.AccountID.String]++
		}
	}
	if len(hoursByAccount) == 0 {
		t.Fatal("必须有 account_health_check 逐小时记录")
	}
	// 任务硬要求：每个 mock 账户都要有逐小时记录（不允许因 Key/账户归属巧合漏账户）。
	business, err := e.openExisting(StoreBusiness)
	if err != nil || business == nil {
		t.Fatalf("business 库应已存在: %v", err)
	}
	wantAccounts := usageScalar(t, business, "SELECT COUNT(*) FROM accounts WHERE id LIKE ?", CleanupIDPrefix+"%")
	if len(hoursByAccount) != wantAccounts {
		t.Errorf("有健康记录的账户 %d 个，business mock 账户 %d 个", len(hoursByAccount), wantAccounts)
	}
	gapped := 0
	for accountID, hours := range hoursByAccount {
		coverage := float64(len(hours)) / float64(rangeHours)
		if coverage < 0.9 {
			t.Errorf("账户 %s 的小时覆盖率 %.3f 低于 0.9（jobs 的覆盖率断言会失败）", accountID, coverage)
		}
		if len(hours) < rangeHours {
			gapped++
		}
		if failuresByAccount[accountID] == 0 {
			t.Errorf("账户 %s 必须有失败小时样本", accountID)
		}
	}
	if gapped == 0 {
		t.Error("必须存在无记录小时（设计文档要求绿 / 红 / 灰三态都能看到）")
	}
}

// TestSeedUsageIdempotentAcrossRuns 验证「重新执行不叠加」：第二次运行（含清理）
// 后，逐键计数与各表行数都与第一次相同。
func TestSeedUsageIdempotentAcrossRuns(t *testing.T) {
	root := businessTestRoot(t)
	first := usageRunOnce(t, root, usageTestOptions(3, 12))
	second := usageRunOnce(t, root, usageTestOptions(3, 12))
	if len(first) != len(second) {
		t.Fatalf("两次运行的计数键不一致：%v / %v", sortedKeys(first), sortedKeys(second))
	}
	for key, value := range first {
		if second[key] != value {
			t.Errorf("计数 %s 第二次为 %d，第一次为 %d", key, second[key], value)
		}
	}
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	stores := paths.usageShardStores()
	if len(stores) == 0 {
		t.Fatal("分片文件必须存在")
	}
	if got, want := len(stores), second["usageShardFiles"]; got != want {
		t.Fatalf("第二次运行后磁盘分片 %d 个，报告 %d", got, want)
	}
	records := 0
	for _, item := range stores {
		db := usageOpenShard(t, item.Path)
		records += usageScalar(t, db, "SELECT COUNT(*) FROM usage_records")
	}
	if want := second["usageRecords"]; records != want {
		t.Fatalf("第二次运行后分片内共 %d 条记录，报告 usageRecords %d", records, want)
	}
	catalog := usageOpenShard(t, paths.UsageCatalog)
	if got, want := usageScalar(t, catalog, "SELECT COUNT(*) FROM usage_record_shards"), second["usageRecordShards"]; got != want {
		t.Fatalf("第二次运行后 catalog 分片行 %d，报告 %d", got, want)
	}
	if got, want := usageScalar(t, catalog, "SELECT COUNT(*) FROM usage_record_shard_entries"), second["usageRecords"]; got != want {
		t.Fatalf("第二次运行后 catalog 条目 %d 行，报告 %d", got, want)
	}
	stats := usageOpenShard(t, paths.Stats)
	if got, want := usageScalar(t, stats, "SELECT COUNT(*) FROM usage_records"), second["usageRecords"]; got != want {
		t.Fatalf("第二次运行后 stats 镜像 %d 行，报告 %d", got, want)
	}
}

// usageRunOnce 模拟一次完整的 --mockdata 用量域执行：新建 env（枚举已存在的分片）
// → 清理上一批 → seed，返回本次域计数。
func usageRunOnce(t *testing.T, root string, options Options) map[string]int {
	t.Helper()
	paths, err := ResolvePaths(root, "", envMap(nil))
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
	// 顺序与 --mockdata 一致：先清理上一批（清理会删掉上一轮的 mock 业务行），
	// 再按域顺序重建 business → usage。
	if _, _, err := cleanupAll(context.Background(), e); err != nil {
		t.Fatalf("cleanupAll: %v", err)
	}
	if _, err := seedBusiness(context.Background(), e); err != nil {
		t.Fatalf("seedBusiness: %v", err)
	}
	result, err := seedUsage(context.Background(), e)
	if err != nil {
		t.Fatalf("seedUsage: %v", err)
	}
	return result.Counts
}

// TestSeedUsageSkipsWithoutBusinessResources 核对空数据根与缺 mock 资源时的跳过
// 语义：Counts 为空（域未接线）且不创建任何分片 / catalog 文件。
func TestSeedUsageSkipsWithoutBusinessResources(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(usageTestOptions(3, 5), nil)
	defer func() { _ = e.Close() }()
	result, err := seedUsage(context.Background(), e)
	if err != nil {
		t.Fatalf("空数据根上 seedUsage 必须跳过而不是失败: %v", err)
	}
	if len(result.Counts) != 0 {
		t.Fatalf("空数据根上 counts 必须为空（未接线语义）: %v", result.Counts)
	}
	if _, statErr := os.Stat(paths.UsageCatalog); !os.IsNotExist(statErr) {
		t.Fatalf("跳过时不得创建 usage-catalog：%v", statErr)
	}
	if stores := paths.usageShardStores(); len(stores) != 0 {
		t.Fatalf("跳过时不得创建分片文件: %v", stores)
	}

	// business 库存在但没有 mock 账户：同样跳过（不写半批数据）。
	businessOnly := businessTestRoot(t)
	paths, err = ResolvePaths(businessOnly, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e2 := newEnv(usageTestOptions(3, 5), nil)
	defer func() { _ = e2.Close() }()
	result, err = seedUsage(context.Background(), e2)
	if err != nil {
		t.Fatalf("缺 mock 账户时 seedUsage 必须跳过: %v", err)
	}
	if len(result.Counts) != 0 {
		t.Fatalf("缺 mock 账户时 counts 必须为空: %v", result.Counts)
	}
}

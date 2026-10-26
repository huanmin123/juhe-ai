package mockdata

// usage / stats 域的 PG 集成测试（pgtestkit 每测试独立库）：域内容断言（行数、
// 日分区、定向样本 trace 关联、shard 登记一致）、重复 Run 幂等（行数不叠加）、
// 清理规则覆盖本域表、覆盖断言在两域接线后转硬门槛。真实 PG 由 pgtestkit 自动
// 解析 .local/project-resources/dev/env/shared.env；连接串内容不进入断言与日志。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// usageStatsTestNow 是本文件全部造数运行的固定时间基准（UTC 正午，保证覆盖
// 样本的 -1h..-6h 偏移不跨日）。
var usageStatsTestNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// runUsageStatsForTest 用小样本参数（Days=2、DailyRequests=5：10 条日常记录恰好
// 轮转完整端点矩阵）执行一次完整造数。chat 资产文件根固定到临时目录，测试不得
// 把资产文件写到仓库工作目录。
func runUsageStatsForTest(t *testing.T, dsn string, now time.Time, summaryFile string) Report {
	t.Helper()
	t.Setenv("JUHE_AI_CHAT_ASSETS_ROOT", filepath.Join(t.TempDir(), "chat-assets"))
	report, err := Run(context.Background(), Options{
		DSN:           dsn,
		Days:          2,
		DailyRequests: 5,
		Now:           now,
		SummaryFile:   summaryFile,
	})
	if err != nil {
		t.Fatalf("mockdata Run 失败: %v", err)
	}
	return report
}

// domainCounts 取指定域的计数。
func domainCounts(t *testing.T, report Report, domain string) map[string]int {
	t.Helper()
	for _, item := range report.Domains {
		if item.Name == domain {
			return item.Counts
		}
	}
	t.Fatalf("报告缺域 %s: %+v", domain, report.Domains)
	return nil
}

// TestMockdataUsageDomainSeedsRecordsAndShards 是 usage 域的核心验收：记录行数
// = Days×DailyRequests + 覆盖样本、批内日期都有日分区、三条上游响应模型定向
// 样本可关联 business 账户与 Key、shard 四表登记与明细一致。
func TestMockdataUsageDomainSeedsRecordsAndShards(t *testing.T) {
	dsn := newMockdataTestDB(t)
	summaryPath := coverageSummaryPath(t)
	report := runUsageStatsForTest(t, dsn, usageStatsTestNow, summaryPath)

	counts := domainCounts(t, report, DomainUsage)
	const wantDaily = 2 * 5
	const wantCoverage = 6 // 三连上游响应模型 + 模型映射 + models/images 端点。
	if counts["usage_records"] != wantDaily+wantCoverage {
		t.Fatalf("usage_records 计数 = %d, want %d: %v", counts["usage_records"], wantDaily+wantCoverage, counts)
	}
	if counts["usage_record_shard_entries"] != counts["usage_records"] {
		t.Fatalf("shard entries 与明细不一致: %v", counts)
	}
	for _, key := range []string{"usage_record_shards", "usage_record_account_shards", "usage_record_api_key_shards"} {
		if counts[key] == 0 {
			t.Fatalf("usage 计数缺少 %s: %v", key, counts)
		}
	}

	ctx := context.Background()
	env, err := newEnv(Options{DSN: dsn, Days: 2, DailyRequests: 5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	count := func(query string) int {
		t.Helper()
		var n int
		if err := env.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 明细行数与日分区：批内 created_at 的每个日期都有对应分区子表。
	if n := count("SELECT COUNT(*) FROM " + ut("usage_records")); n != wantDaily+wantCoverage {
		t.Fatalf("usage_records 实际行数 = %d, want %d", n, wantDaily+wantCoverage)
	}
	if n := count("SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace " +
		"WHERE n.nspname = '" + SchemaUsage + "' AND c.relkind = 'r' AND c.relispartition " +
		"AND c.relname LIKE 'usage_records\\_%'"); n < 2 {
		t.Fatalf("日分区数量 = %d, want >= 2", n)
	}

	// 三条定向样本：trace 可关联 business 域的真实账户与 Key（不猜 ID 的外键
	// 全部来自 business 造数），一致 / 不一致语义由 upstream 列的原始值表达。
	coverageTraces := []string{usageTraceUpstreamMatch, usageTraceUpstreamMismatch, usageTraceUpstreamUnmappedMismatch}
	for _, trace := range coverageTraces {
		var accountID, apiKeyID *string
		err := env.db.QueryRowContext(ctx,
			"SELECT account_id, api_key_id FROM "+ut("usage_records")+" WHERE trace_id = $1", trace).
			Scan(&accountID, &apiKeyID)
		if err != nil {
			t.Fatalf("定向样本 %s 不存在: %v", trace, err)
		}
		if accountID == nil || apiKeyID == nil || !strings.HasPrefix(*accountID, CleanupIDPrefix) ||
			!strings.HasPrefix(*apiKeyID, CleanupIDPrefix) {
			t.Fatalf("定向样本 %s 未关联 business mock 账户/Key: %v %v", trace, accountID, apiKeyID)
		}
	}
	if n := count("SELECT COUNT(*) FROM " + ut("usage_records") + " WHERE upstream_model = upstream_response_model " +
		"AND trace_id = '" + usageTraceUpstreamMatch + "'"); n != 1 {
		t.Fatalf("一致样本缺失: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + ut("usage_records") + " WHERE upstream_response_model = upstream_model || '" +
		usageResponseModelSuffix + "' AND trace_id IN ('" + usageTraceUpstreamMismatch + "','" +
		usageTraceUpstreamUnmappedMismatch + "')"); n != 2 {
		t.Fatalf("不一致样本缺失: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + ut("usage_records") + " WHERE trace_id = '" +
		usageTraceUpstreamUnmappedMismatch + "' AND model_mapping_applied = 0"); n != 1 {
		t.Fatalf("未映射样本的 model_mapping_applied 应为 0: %d", n)
	}

	// shard 登记一致：每条明细都有 entry、登记行 file_path 是 PG 逻辑定位、
	// shard_key 三位分片号、shard_id 落在分片数内。
	if n := count("SELECT COUNT(*) FROM " + ut("usage_records") + " r WHERE NOT EXISTS (SELECT 1 FROM " +
		ut("usage_record_shard_entries") + " e WHERE e.usage_id = r.id)"); n != 0 {
		t.Fatalf("%d 条明细没有 shard entry", n)
	}
	if n := count("SELECT COUNT(*) FROM " + ut("usage_record_shards") +
		" WHERE file_path NOT LIKE 'postgres:juhe_usage.usage_records:%'"); n != 0 {
		t.Fatalf("%d 条 shard 登记的 file_path 不是 PG 逻辑定位", n)
	}
	if n := count("SELECT COUNT(*) FROM " + ut("usage_record_shards") +
		" WHERE shard_key !~ '^\\d{8}:s\\d{3}$'"); n != 0 {
		t.Fatalf("%d 条 shard_key 不是「8 位日期 + 三位分片号」形态", n)
	}
	if n := count("SELECT COUNT(*) FROM " + ut("usage_record_shards") +
		" WHERE shard_id < 0 OR shard_id >= 16"); n != 0 {
		t.Fatalf("%d 条 shard_id 超出分片数", n)
	}
	if n := count("SELECT COUNT(*) FROM " + ut("usage_record_account_shards") +
		" WHERE first_created_at > last_seen_at"); n != 0 {
		t.Fatalf("%d 条账户分片跨度非法", n)
	}
	if n := count("SELECT COUNT(*) FROM " + ut("usage_record_api_key_shards") +
		" WHERE first_created_at > last_seen_at"); n != 0 {
		t.Fatalf("%d 条 Key 分片跨度非法", n)
	}
	// PG 形态没有 sqlite 时代的 stats 镜像 usage_records 表（juhe_stats schema
	// 里不存在该表）。
	if exists, err := tableExists(ctx, env.db, SchemaStats, "usage_records"); err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("juhe_stats 不应存在 sqlite 时代的 usage_records 镜像表")
	}
}

// TestMockdataStatsDomainSeedsRawSamples 是 stats 域的核心验收：后台任务运行、
// 三个 kind 的账户用量快照（timestamptz 列真实落值）、IP 登记/策略/命中与
// 脏标记全部落库。
func TestMockdataStatsDomainSeedsRawSamples(t *testing.T) {
	dsn := newMockdataTestDB(t)
	report := runUsageStatsForTest(t, dsn, usageStatsTestNow, "")
	counts := domainCounts(t, report, DomainStats)
	for _, key := range []string{
		"background_task_runs", "account_usage_snapshots", "client_ip_registry",
		"client_ip_policies", "client_ip_policy_hits", "usage_overview_dirty_scopes",
		"ai_performance_summary_dirty_system_accounts", "group_account_stats_dirty",
	} {
		if counts[key] == 0 {
			t.Fatalf("stats 计数缺少 %s: %v", key, counts)
		}
	}

	ctx := context.Background()
	env, err := newEnv(Options{DSN: dsn, Days: 2, DailyRequests: 5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	count := func(query string) int {
		t.Helper()
		var n int
		if err := env.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 后台任务运行：stats 域独占（chat 域不再写该表），状态词表覆盖
	// completed / failed / running / queued / skipped。
	if n := count("SELECT COUNT(*) FROM " + st("background_task_runs")); n < len(statsBackgroundTaskSamples) {
		t.Fatalf("background_task_runs 行数 = %d, want >= %d", n, len(statsBackgroundTaskSamples))
	}
	if n := count("SELECT COUNT(DISTINCT status) FROM " + st("background_task_runs")); n < 5 {
		t.Fatalf("background_task_runs 状态分布 = %d, want >= 5", n)
	}

	// 账户用量快照：三个 kind 齐全；relay_balance / xai_grok 的读面键对齐
	// gateway / jobs 消费形状；timestamptz 列非空可读。
	if n := count("SELECT COUNT(DISTINCT kind) FROM " + st("account_usage_snapshots")); n != 3 {
		t.Fatalf("account_usage_snapshots kind 分布 = %d, want 3", n)
	}
	if n := count("SELECT COUNT(*) FROM " + st("account_usage_snapshots") + " WHERE kind = '" +
		statsSnapshotKindRelayBalance + "' AND refresh_status = 'fresh' AND source = 'upstream_api' " +
		"AND snapshot_json LIKE '%inputDigest%' AND next_refresh_after IS NOT NULL"); n != 1 {
		t.Fatalf("relay_balance 快照形状不符: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + st("account_usage_snapshots") + " WHERE kind = '" +
		statsSnapshotKindXAIGrok + "' AND source = 'xai_grok_billing' AND refresh_status = 'ok' " +
		"AND snapshot_json LIKE '%grok_credit_used_percent%'"); n != 1 {
		t.Fatalf("xai_grok 快照形状不符: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + st("account_usage_snapshots") + " WHERE kind = '" +
		statsSnapshotKindCodex + "' AND refresh_status = 'fresh' " +
		"AND snapshot_json LIKE '%codex_5h_used_percent%' AND snapshot_json LIKE '%codex_7d_used_percent%'"); n < 1 {
		t.Fatalf("openai_codex 快照形状不符: %d", n)
	}

	// IP 样本与 usage 明细同一 IP 家族：登记行的 client_ip 至少一条能在明细里
	// 找到（statsClientIPIdentity 的哈希口径由策略可关联间接钉住）。
	if n := count("SELECT COUNT(*) FROM " + st("client_ip_registry") + " r WHERE EXISTS (SELECT 1 FROM " +
		ut("usage_records") + " u WHERE u.client_ip = r.client_ip)"); n < 1 {
		t.Fatal("IP 登记样本与使用明细的 client_ip 没有交集")
	}
	if n := count("SELECT COUNT(*) FROM " + st("client_ip_policies") + " p JOIN " +
		st("client_ip_registry") + " r ON r.ip_hash = p.ip_hash"); n < 3 {
		t.Fatalf("策略行与 registry 按 ip_hash 关联失败: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + st("client_ip_policy_hits")); n < 1 {
		t.Fatal("策略命中样本缺失")
	}

	// 脏标记：global 作用域 + 每个 mock 账户归属系统账户。
	if n := count("SELECT COUNT(*) FROM " + st("usage_overview_dirty_scopes") +
		" WHERE system_account_id = 'global'"); n != 1 {
		t.Fatalf("global 脏范围行数 = %d, want 1", n)
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "group_account_stats_dirty")); n < 1 {
		t.Fatal("分组统计脏标记缺失")
	}
}

// TestMockdataUsageStatsDomainsAreIdempotent 重复执行不叠加：第二次 Run 的两域
// 计数与第一次一致，且第二次清理命中了两域的表。
func TestMockdataUsageStatsDomainsAreIdempotent(t *testing.T) {
	dsn := newMockdataTestDB(t)
	first := runUsageStatsForTest(t, dsn, usageStatsTestNow, "")
	second := runUsageStatsForTest(t, dsn, usageStatsTestNow.Add(2*time.Hour), "")
	for _, domain := range []string{DomainUsage, DomainStats} {
		firstCounts, secondCounts := domainCounts(t, first, domain), domainCounts(t, second, domain)
		if len(firstCounts) != len(secondCounts) {
			t.Fatalf("域 %s 重复执行计数键数变化: %v vs %v", domain, firstCounts, secondCounts)
		}
		for key, count := range firstCounts {
			if secondCounts[key] != count {
				t.Fatalf("域 %s 重复执行叠加了 %s: first=%d second=%d", domain, key, count, secondCounts[key])
			}
		}
	}
	// 第二次清理命中两域的表（清理标识收敛）。
	for _, schemaTable := range []string{
		ut("usage_records"), ut("usage_record_shard_entries"), ut("usage_record_shards"),
		st("background_task_runs"), st("account_usage_snapshots"), st("client_ip_registry"),
		st("usage_overview_dirty_scopes"),
	} {
		if second.Cleanup[schemaTable] == 0 {
			t.Fatalf("第二次清理未命中 %s: %v", schemaTable, second.Cleanup)
		}
	}
}

// TestMockdataUsageStatsCleanupCoversDomainTables 清理规则覆盖两域表：清理后
// 明细 / shard 四表 / stats 写入面全部归零，第二次清理 no-op。
func TestMockdataUsageStatsCleanupCoversDomainTables(t *testing.T) {
	dsn := newMockdataTestDB(t)
	runUsageStatsForTest(t, dsn, usageStatsTestNow, "")

	ctx := context.Background()
	env, err := newEnv(Options{DSN: dsn, Days: 2, DailyRequests: 5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	count := func(query string) int {
		t.Helper()
		var n int
		if err := env.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	deleted, _, err := cleanupAll(ctx, env, nil)
	if err != nil {
		t.Fatalf("cleanupAll: %v", err)
	}
	for _, schemaTable := range []string{
		ut("usage_records"), ut("usage_record_shard_entries"), ut("usage_record_shards"),
		ut("usage_record_account_shards"), ut("usage_record_api_key_shards"),
		st("background_task_runs"), st("account_usage_snapshots"),
		st("client_ip_registry"), st("client_ip_policies"), st("client_ip_policy_hits"),
		st("usage_overview_dirty_scopes"), st("ai_performance_summary_dirty_system_accounts"),
		qualified(SchemaBusiness, "group_account_stats_dirty"),
	} {
		if deleted[schemaTable] == 0 {
			t.Fatalf("清理未命中 %s: %v", schemaTable, deleted)
		}
	}
	// 清理后两域写入面全部归零（含不带标识的 shard 登记行——按「分片内已无
	// 条目」回收；client_ip_registry 按派生面重置）。
	for _, schemaTable := range []string{
		ut("usage_records"), ut("usage_record_shards"), ut("usage_record_shard_entries"),
		ut("usage_record_account_shards"), ut("usage_record_api_key_shards"),
		st("client_ip_registry"), st("usage_overview_dirty_scopes"),
		st("ai_performance_summary_dirty_system_accounts"),
	} {
		if n := count("SELECT COUNT(*) FROM " + schemaTable); n != 0 {
			t.Fatalf("清理后 %s 仍有 %d 行", schemaTable, n)
		}
	}
	// 第二次清理：no-op（幂等收敛）。
	second, _, err := cleanupAll(ctx, env, nil)
	if err != nil {
		t.Fatalf("第二次 cleanupAll: %v", err)
	}
	for table, rows := range second {
		if rows != 0 {
			t.Fatalf("第二次清理仍删除 %s %d 行", table, rows)
		}
	}
}

// TestVerifyCoverageUsageStatsHardGates 覆盖断言在两域接线后转硬门槛：Run 产出
// 的摘要让 usage / stats 全部断言被真实评估（不在 NotCovered），且全部通过
// （Errors 无两域相关失败）。
func TestVerifyCoverageUsageStatsHardGates(t *testing.T) {
	dsn := newMockdataTestDB(t)
	summaryPath := coverageSummaryPath(t)
	runUsageStatsForTest(t, dsn, usageStatsTestNow, summaryPath)

	report, err := VerifyCoverage(context.Background(), Options{
		DSN:           dsn,
		Days:          2,
		DailyRequests: 5,
		SummaryFile:   summaryPath,
	})
	if err != nil {
		t.Fatalf("VerifyCoverage: %v", err)
	}
	notCovered := func(name string) bool {
		for _, item := range report.NotCovered {
			if strings.HasPrefix(item, name) {
				return true
			}
		}
		return false
	}
	for _, assertion := range coverageAssertions() {
		if assertion.Domain != DomainUsage && assertion.Domain != DomainStats {
			continue
		}
		if notCovered(assertion.Name) {
			t.Fatalf("断言 %s 应已评估却进 NotCovered: %v", assertion.Name, report.NotCovered)
		}
	}
	for _, item := range report.Errors {
		if strings.Contains(item, "usage") || strings.Contains(item, "stats") ||
			strings.Contains(item, "background_task_runs") || strings.Contains(item, "account_usage_snapshots") {
			t.Fatalf("usage / stats 断言未通过: %v", report.Errors)
		}
	}
}

// TestUsageShardLocationMirror 钉住逻辑 shard 定位算法与 jobs usagewriter 的
// 对齐契约（maintenance 不允许 import jobs，算法在本包对齐）：ID 解析、三位
// 分片号、逻辑 file_path、ID 缺失时按 createdAt 桶 + 逻辑哈希回落。
func TestUsageShardLocationMirror(t *testing.T) {
	entropy := usageShardEntropy("mockdata-usage-00042", 42)
	if len(entropy) > 24 {
		t.Fatalf("熵超长: %q", entropy)
	}
	for _, r := range entropy {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			t.Fatalf("熵含非 alnum 字符: %q", entropy)
		}
	}
	id := usageRecordID(time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC), "mockdata-usage-00042", 42)
	location := usageLogicalShardLocationForRecord(id, "2026-10-25T03:04:05.000Z")
	if location.RecordID != id || location.BucketDateKey != "20261025" || location.BucketDate != "2026-10-25" {
		t.Fatalf("定位解析不符: %+v", location)
	}
	if want := "20261025:s" + fmt.Sprintf("%03d", location.ShardID); location.ShardKey != want {
		t.Fatalf("shard_key = %q, want %q（三位分片号）", location.ShardKey, want)
	}
	if location.FilePath != "postgres:juhe_usage.usage_records:"+location.ShardKey {
		t.Fatalf("file_path = %q", location.FilePath)
	}
	if location.ShardID < 0 || location.ShardID >= usageLogicalShardCount {
		t.Fatalf("shard_id 越界: %d", location.ShardID)
	}
	// 非 usage_* 前缀的 ID：回落 createdAt 桶 + 31 乘子逻辑哈希（与 jobs
	// LogicalShardID 同式）。
	fallback := usageLogicalShardLocationForRecord("legacy-id", "2026-10-24T10:00:00.000Z")
	if fallback.BucketDateKey != "20261024" {
		t.Fatalf("回落桶日期 = %q", fallback.BucketDateKey)
	}
	hash := uint32(0)
	for index := 0; index < len("legacy-id"); index++ {
		hash = hash*31 + uint32("legacy-id"[index])
	}
	if want := int(hash) % usageLogicalShardCount; fallback.ShardID != want {
		t.Fatalf("回落分片号 = %d, want %d", fallback.ShardID, want)
	}
}

// TestUsageRecordColumnsMatchDDL 钉住 usage_records 写入列集与 pg_schema_usage
// DDL 的 68 列一致（列名来自 DDL 抽取，缺列会让 NOT NULL 约束在写入时才爆）。
func TestUsageRecordColumnsMatchDDL(t *testing.T) {
	want := []string{
		"id", "system_account_id", "trace_id", "traffic_source", "client_ip",
		"api_key_id", "group_id", "route_group_id", "compaction_expected",
		"account_id", "endpoint", "provider_code", "provider_protocol_profile_id",
		"usage_semantic", "model", "upstream_model", "upstream_response_model",
		"pricing_model", "requested_service_tier", "effective_service_tier",
		"reported_service_tier", "billed_service_tier", "requested_reasoning_effort",
		"effective_reasoning_effort", "cost_breakdown_snapshot_json",
		"model_mapping_applied", "model_mapping_source", "source_endpoint_family",
		"upstream_endpoint_family", "stream", "status_code", "success",
		"failure_attribution", "first_token_ms", "duration_ms", "input_tokens",
		"output_tokens", "cache_read_tokens", "cache_read_cost_usd",
		"cache_write_tokens", "cache_write_1h_tokens", "cache_write_cost_usd",
		"thinking_tokens", "input_image_tokens", "output_image_tokens",
		"input_audio_tokens", "output_audio_tokens", "output_image_count",
		"tts_input_chars", "audio_input_seconds", "usage_missing",
		"output_video_seconds", "cost_usd", "error_code", "error_message",
		"request_snapshot_json", "response_snapshot_json",
		"account_owner_system_account_id", "group_owner_system_account_id",
		"account_access_type", "group_access_type", "account_authorization_id",
		"account_authorization_source_type", "account_authorization_source_team_id",
		"group_authorization_id", "group_authorization_source_type",
		"group_authorization_source_team_id", "created_at",
	}
	if len(usageRecordColumns) != len(want) {
		t.Fatalf("usageRecordColumns %d 列, want %d", len(usageRecordColumns), len(want))
	}
	for index, column := range want {
		if usageRecordColumns[index] != column {
			t.Fatalf("列序 %d = %q, want %q", index, usageRecordColumns[index], column)
		}
	}
}

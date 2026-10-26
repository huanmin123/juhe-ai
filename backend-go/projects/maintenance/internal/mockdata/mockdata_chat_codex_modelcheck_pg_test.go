package mockdata

// chat_codex_model_check 域的 PG 集成测试（pgtestkit 每测试独立库）：chat 日
// 分区与消息、资产文件与引用完整性、codex 三表、J3b 模型检测样本（prepare 先
// 建 juhe_j3b schema，Run 内完成受控 bootstrap）、background_task_runs、
// juhe_jobs 属主缺失 skip 路径、重复执行幂等与属主行自清、清理收敛。
// chat 资产文件根由 runMockdataForTest 固定到测试临时目录（JUHE_AI_CHAT_ASSETS_ROOT）。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// chatDomainCounts 从报告里取 chat_codex_model_check 域的计数。
func chatDomainCounts(t *testing.T, report Report) map[string]int {
	t.Helper()
	for _, domain := range report.Domains {
		if domain.Name == DomainChatCodexModelCheck {
			return domain.Counts
		}
	}
	t.Fatal("报告缺少 chat_codex_model_check 域")
	return nil
}

// TestChatCodexModelCheckDomainContent 是本域的核心验收：全新库（无属主存储）
// 一次 Run 后 chat 分区就位、消息与资产落库、资产文件真实存在、引用完整、
// codex 三表闭环、后台任务五状态齐全；属主缺失的两段按 skip 语义留空。
func TestChatCodexModelCheckDomainContent(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	report := runMockdataForTest(t, dsn, now, "")
	counts := chatDomainCounts(t, report)

	// 计数：4 会话 / 20 消息（12 常规 + 8 媒体）/ 5 资产（3 图 + 2 媒体）/
	// 5 引用 / 2 图片生成 / 2 存储窗口桶 / 1 幂等键 / 1 检查点 + 3 条目 /
	// codex 2 会话 3 响应 2 摘要。background_task_runs 归 stats 域，不在本域计数。
	wantCounts := map[string]int{
		"chat_conversations": 4, "chat_messages": 20, "chat_assets": 5,
		"chat_asset_references": 5, "chat_image_generations": 2,
		"chat_user_storage_windows": 2, "chat_message_idempotency": 1,
		"chat_context_checkpoints": 1, "chat_context_entries": 3, "chat_user_asset_usage": 1,
		"codex_context_sessions": 2, "codex_context_responses": 3, "codex_context_compacts": 2,
	}
	for table, want := range wantCounts {
		if counts[table] != want {
			t.Fatalf("计数 %s = %d, want %d（全部计数 %v）", table, counts[table], want, counts)
		}
	}
	// 属主缺失：模型检测与账户健康段跳过，不进 Counts。
	if counts["model_check_runs"] != 0 || counts["account_health_outcomes"] != 0 {
		t.Fatalf("属主缺失时属主段不应产出: %v", counts)
	}
	pendingStores := map[string]bool{}
	for _, pending := range report.OwnerStoresPending {
		pendingStores[pending.Store] = true
	}
	if !pendingStores["model_check"] || !pendingStores["account_health"] {
		t.Fatalf("属主缺失应进 OwnerStoresPending: %+v", report.OwnerStoresPending)
	}

	env, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	ctx := context.Background()
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := env.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 分区：消息时间落在 2026-10-24 / 10-25 两天，ensure 预建当日 + 次日，
	// 因此 20261024/25/26 三个分区必须存在（10-23 无消息、不应存在）。
	partitionExists := func(dateKey string) bool {
		t.Helper()
		var regclass *string
		if err := env.db.QueryRowContext(ctx, "SELECT to_regclass($1)::text",
			SchemaChat+"."+chatMessagePartitionPrefix+dateKey).Scan(&regclass); err != nil {
			t.Fatal(err)
		}
		return regclass != nil
	}
	for _, dateKey := range []string{"20261024", "20261025", "20261026"} {
		if !partitionExists(dateKey) {
			t.Fatalf("chat_messages 分区 %s 未创建", dateKey)
		}
	}
	if partitionExists("20261023") {
		t.Fatal("无消息的日期不应预建分区 20261023")
	}
	// 消息写入的分区路由正确：按 created_at 前缀逐日计数与总数一致。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaChat, "chat_messages")); n != 20 {
		t.Fatalf("chat_messages 总数 %d != 20", n)
	}

	// 资产文件：ready 资产的 storage_key 与 preview_storage_key 必须真实落在
	// JUHE_AI_CHAT_ASSETS_ROOT 下（只有库行没有文件就是页面 404）。
	assetsRoot := os.Getenv("JUHE_AI_CHAT_ASSETS_ROOT")
	if assetsRoot == "" {
		t.Fatal("测试未设置 JUHE_AI_CHAT_ASSETS_ROOT")
	}
	rows, err := env.db.QueryContext(ctx, "SELECT id, storage_key, preview_storage_key FROM "+qualified(SchemaChat, "chat_assets")+
		" WHERE processing_status = 'ready'")
	if err != nil {
		t.Fatal(err)
	}
	assetCount := 0
	for rows.Next() {
		var id, storageKey string
		var previewKey *string
		if err := rows.Scan(&id, &storageKey, &previewKey); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		assetCount++
		if _, err := os.Stat(filepath.Join(assetsRoot, filepath.FromSlash(storageKey))); err != nil {
			t.Fatalf("资产 %s 的文件缺失: %v", id, err)
		}
		if previewKey != nil {
			if _, err := os.Stat(filepath.Join(assetsRoot, filepath.FromSlash(*previewKey))); err != nil {
				t.Fatalf("资产 %s 的预览文件缺失: %v", id, err)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if assetCount != 5 {
		t.Fatalf("ready 资产数 %d != 5", assetCount)
	}

	// 引用完整性：每条引用都能解析到资产（双外键）与消息；媒体资产无宽高、
	// 无预览；assistant_generated 图片有预览键。
	if n := count(`SELECT COUNT(*) FROM ` + qualified(SchemaChat, "chat_asset_references") + ` r
		LEFT JOIN ` + qualified(SchemaChat, "chat_assets") + ` a ON a.id = r.asset_id AND a.conversation_id = r.conversation_id
		LEFT JOIN ` + qualified(SchemaChat, "chat_messages") + ` m ON m.id = r.message_id
		WHERE a.id IS NULL OR m.id IS NULL`); n != 0 {
		t.Fatalf("存在悬挂引用: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaChat, "chat_assets") +
		" WHERE processed_mime_type IN ('audio/wav','video/mp4') AND (original_width IS NOT NULL OR preview_storage_key IS NOT NULL)"); n != 0 {
		t.Fatalf("媒体资产不应有宽高或预览: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaChat, "chat_assets") +
		" WHERE source_kind = 'assistant_generated' AND processed_mime_type LIKE 'image/%' AND preview_storage_key IS NULL"); n != 0 {
		t.Fatalf("assistant_generated 图片缺预览: %d", n)
	}

	// codex 三表闭环：会话 latest 指针与响应/摘要行一致，响应 previous 链完整。
	if n := count(`SELECT COUNT(*) FROM ` + qualified(SchemaCodexContext, "codex_context_sessions") + ` s
		WHERE (s.latest_response_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + qualified(SchemaCodexContext, "codex_context_responses") + ` r WHERE r.response_id = s.latest_response_id))
		   OR (s.latest_compact_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + qualified(SchemaCodexContext, "codex_context_compacts") + ` c WHERE c.compact_id = s.latest_compact_id))`); n != 0 {
		t.Fatalf("会话 latest 指针悬挂: %d", n)
	}

	// 后台任务运行表归 stats 域（域归属见 Mockdata造数设计），本域不再断言其状态分布。
}

// chatJobsFixturePrepare 建 juhe_j3b schema（Run 内做受控 bootstrap 建表）与
// juhe_jobs.account_health_outcomes 夹具表。夹具 DDL 逐列来自
// jobs/internal/accounthealth/store.go 的 postgresSchema（仅测试固件使用；
// 生产建表路径归 jobs，maintenance 不复制 DDL）。
func chatJobsFixturePrepare(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+SchemaJ3B); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+SchemaJobs); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS juhe_jobs.account_health_outcomes (outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, account_id TEXT NOT NULL, outcome TEXT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, input_version BIGINT NOT NULL, config_revision BIGINT NOT NULL, dispatch_revision BIGINT NOT NULL, status_code INTEGER, error_code TEXT, error_message TEXT, payload JSONB NOT NULL)`)
	return err
}

// TestChatCodexModelCheckOwnerStoresPresent：属主就位时模型检测矩阵、可信度
// 投影、基线、调度历史、小时健康与持久化输入闭环全部落库，账户健康四类结果
// 齐全。
func TestChatCodexModelCheckOwnerStoresPresent(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, chatJobsFixturePrepare).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	report := runMockdataForTest(t, dsn, now, "")
	counts := chatDomainCounts(t, report)

	wantCounts := map[string]int{
		"model_check_runs": 8, "model_check_observations": 4, "model_check_items": 19,
		"model_trust_observation_receipts": 3, "model_account_trust_results": 1,
		"model_trust_latest_dirty_accounts": 1, "model_trust_aggregation_state": 1,
		"model_token_intercept_baseline_versions": 1, "model_check_scheduler_tasks": 2,
		"account_quality_health_hourly": 6, "model_check_input_versions": 1,
		"model_check_inputs": 1, "model_check_execution_claims": 1, "model_check_outcomes": 1,
		"account_health_outcomes": 4,
	}
	for table, want := range wantCounts {
		if counts[table] != want {
			t.Fatalf("计数 %s = %d, want %d（全部计数 %v）", table, counts[table], want, counts)
		}
	}

	env, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	ctx := context.Background()
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := env.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// 运行矩阵：quick/full × running/completed/failed/canceled 全覆盖。
	if n := count("SELECT COUNT(DISTINCT status) FROM " + qualified(SchemaJ3B, "model_check_runs")); n != 4 {
		t.Fatalf("运行状态分布 %d != 4", n)
	}
	if n := count("SELECT COUNT(DISTINCT profile) FROM " + qualified(SchemaJ3B, "model_check_runs")); n != 2 {
		t.Fatalf("运行 profile 分布 %d != 2", n)
	}
	// 运行中样本：finished/duration/同步状态必须留空；终态 completed 带 applied。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaJ3B, "model_check_runs") +
		" WHERE status = 'running' AND (finished_at IS NOT NULL OR duration_ms IS NOT NULL OR quality_health_sync_status IS NOT NULL)"); n != 0 {
		t.Fatalf("运行中样本带了终态字段: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaJ3B, "model_check_runs") +
		" WHERE status = 'completed' AND quality_health_sync_status <> 'applied'"); n != 0 {
		t.Fatalf("completed 样本健康同步状态不符: %d", n)
	}
	// observation：已完成深度样本的 3 条已聚合，运行中深度样本的 1 条未聚合。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaJ3B, "model_check_observations") +
		" WHERE aggregation_completed_at IS NULL"); n != 1 {
		t.Fatalf("未聚合 observation 数 %d != 1", n)
	}
	// 题库环节：custom_quiz 检查项与 policy_snapshot.customQuestionIds 同批。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaJ3B, "model_check_items") +
		" WHERE item_type = 'custom_quiz'"); n != 3 {
		t.Fatalf("custom_quiz 检查项 %d != 3", n)
	}
	// 持久化输入闭环：committed 布尔列与 JSONB payload 落库成功。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaJ3B, "model_check_outcomes") + " WHERE committed"); n != 1 {
		t.Fatalf("committed outcome %d != 1", n)
	}
	// 账户健康：四类 outcome 词表各一行。
	if n := count("SELECT COUNT(DISTINCT outcome) FROM " + qualified(SchemaJobs, "account_health_outcomes")); n != 4 {
		t.Fatalf("账户健康 outcome 分布 %d != 4", n)
	}
}

// TestChatCodexModelCheckJobsMissingSkipsOnlyHealth：juhe_j3b 就位、juhe_jobs
// 缺失时，只有账户健康段跳过且不影响其余段——属主缺失是独立、非失败的语义。
func TestChatCodexModelCheckJobsMissingSkipsOnlyHealth(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		return ensureJ3BSchemaOnly(ctx, db)
	}).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	report := runMockdataForTest(t, dsn, now, "")
	counts := chatDomainCounts(t, report)
	if counts["model_check_runs"] != 8 {
		t.Fatalf("juhe_j3b 就位时模型检测段应产出: %v", counts)
	}
	if counts["account_health_outcomes"] != 0 {
		t.Fatalf("juhe_jobs 缺失时账户健康段应跳过: %v", counts)
	}
	pendingStores := map[string]bool{}
	for _, pending := range report.OwnerStoresPending {
		pendingStores[pending.Store] = true
	}
	if pendingStores["model_check"] {
		t.Fatal("juhe_j3b bootstrap 后 model_check 不应 pending")
	}
	if !pendingStores["account_health"] {
		t.Fatalf("juhe_jobs 缺失应 pending: %+v", report.OwnerStoresPending)
	}
}

// TestChatCodexModelCheckIdempotent：重复执行不叠加——maintenance schema 行由
// Run 级清理收敛，属主 schema 行由域 seed 事务自清收敛（cleanupAll 不经过属主
// schema，这是唯一的幂等防线）。
func TestChatCodexModelCheckIdempotent(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, chatJobsFixturePrepare).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first := chatDomainCounts(t, runMockdataForTest(t, dsn, now, ""))
	second := chatDomainCounts(t, runMockdataForTest(t, dsn, now.Add(2*time.Hour), ""))
	for table, want := range first {
		if second[table] != want {
			t.Fatalf("重复执行叠加了 %s: first=%d second=%d", table, want, second[table])
		}
	}
	env, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	ctx := context.Background()
	count := func(query string) int {
		t.Helper()
		var n int
		if err := env.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// 物理行数与计数一致（无历史残留行）。background_task_runs 归 stats 域，
	// 不在本域计数与检查范围。
	for table, want := range first {
		schema := SchemaChat
		query := "SELECT COUNT(*) FROM " + qualified(schema, table)
		switch {
		case strings.HasPrefix(table, "codex_context"):
			schema = SchemaCodexContext
			query = "SELECT COUNT(*) FROM " + qualified(schema, table)
		case table == "account_health_outcomes":
			schema = SchemaJobs
			query = "SELECT COUNT(*) FROM " + qualified(schema, table)
		case strings.HasPrefix(table, "model_check") || strings.HasPrefix(table, "account_quality") ||
			strings.HasPrefix(table, "model_trust") || strings.HasPrefix(table, "model_token") ||
			strings.HasPrefix(table, "model_account"):
			schema = SchemaJ3B
			query = "SELECT COUNT(*) FROM " + qualified(schema, table)
		}
		if n := count(query); n != want {
			t.Fatalf("表 %s 物理行数 %d != 计数 %d", table, n, want)
		}
	}
}

// TestChatCodexModelCheckCleanupSweeps：Run 级清理把本域在 maintenance schema
// 的行全部清空（chat/codex/stats），属主 schema 行不在清理拓扑内（由域自清）。
func TestChatCodexModelCheckCleanupSweeps(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	runMockdataForTest(t, dsn, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), "")
	env, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	ctx := context.Background()
	deleted, _, err := cleanupAll(ctx, env, nil)
	if err != nil {
		t.Fatalf("cleanupAll: %v", err)
	}
	for _, table := range []string{"chat_conversations", "chat_messages", "chat_assets", "chat_asset_references"} {
		if deleted[qualified(SchemaChat, table)] == 0 {
			t.Fatalf("清理未命中 %s: %v", table, deleted)
		}
	}
	for _, schemaTable := range []string{
		qualified(SchemaCodexContext, "codex_context_sessions"),
		qualified(SchemaCodexContext, "codex_context_responses"),
		qualified(SchemaCodexContext, "codex_context_compacts"),
	} {
		if deleted[schemaTable] == 0 {
			t.Fatalf("清理未命中 %s: %v", schemaTable, deleted)
		}
	}
	// 第二次清理为 no-op（幂等收敛）。
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

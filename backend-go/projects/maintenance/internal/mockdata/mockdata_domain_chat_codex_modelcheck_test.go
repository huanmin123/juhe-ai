package mockdata

// chat / codex / 模型检测域测试：在临时数据根里用真实 DDL（schema.EnsureSQLiteChat、
// J3b ensure 入口）与真实 business 夹具跑 seedChatCodexModelCheck，断言行数、
// 样本矩阵、外键关联、图片文件、分片落位、可信度自洽、幂等与覆盖断言。
//
// 为什么用 internal/schema 的真 DDL 而不是抄一遍列定义：本域最容易犯且只能靠
// 真实库结构暴露的错误就是「列名 / CHECK 组合写错」，而 schema 包正是 gateway
// 运行时用的同一份 DDL（internal/schema 属本模块，跨项目 import 禁令不涉及）。
//
// 全部数据写在 t.TempDir() 下：造数最危险的失败模式是写错数据根，测试绝不碰
// .local/dev/data。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// chatCodexTestNow 是本域测试固定的时间基准：所有断言都基于它推导。
var chatCodexTestNow = time.Date(2026, 5, 20, 9, 15, 0, 0, time.UTC)

// chatCodexTestBusinessDDL 是 business 库的测试夹具表（只含本域查询的列）。
var chatCodexTestBusinessDDL = []string{
	`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE)`,
	`CREATE TABLE api_keys (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL)`,
	`CREATE TABLE accounts (
	  id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL,
	  provider_code TEXT, provider_protocol_profile_id TEXT, protocol_code TEXT,
	  health_check_endpoint_mode TEXT, health_check_model TEXT
	)`,
	`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL)`,
	`CREATE TABLE account_supported_models (account_id TEXT NOT NULL, model TEXT NOT NULL)`,
	`CREATE TABLE provider_model_catalog (model TEXT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'active', mode TEXT, catalog_order INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE model_check_question_bank (
	  id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending'
	)`,
}

// chatCodexTestEnv 建好 business 夹具与 chat 库真 schema，返回上下文。
// codex 分片与 J3b 专库刻意不预建：它们由域自己确保（这正是要验证的行为）。
func chatCodexTestEnv(t *testing.T, shardCount int) *env {
	t.Helper()
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(map[string]string{
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": shardCountString(shardCount),
	}))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 7, DailyRequests: 20, Now: chatCodexTestNow}, nil)
	t.Cleanup(func() { _ = e.Close() })
	ctx := context.Background()
	for _, ddl := range chatCodexTestBusinessDDL {
		createTestTable(t, e, StoreBusiness, ddl)
	}
	db, err := e.open(StoreChat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.EnsureSQLiteChat(ctx, db); err != nil {
		t.Fatalf("ensure chat schema: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := e.exec(ctx, StoreBusiness, query, args...); err != nil {
			t.Fatalf("business 夹具写入失败: %v", err)
		}
	}
	seed(`INSERT INTO system_accounts (id, username) VALUES (?, ?)`,
		CleanupIDPrefix+"system_account_admin", CleanupIDPrefix+"admin")
	seed(`INSERT INTO api_keys (id, name, system_account_id) VALUES (?, ?, ?)`,
		CleanupIDPrefix+"api_key_admin_main", CleanupNamePrefix+"主力 Key", CleanupIDPrefix+"system_account_admin")
	seed(`INSERT INTO groups (id, system_account_id) VALUES (?, ?)`,
		CleanupIDPrefix+"group_main", CleanupIDPrefix+"system_account_admin")
	seed(`INSERT INTO groups (id, system_account_id) VALUES (?, ?)`,
		CleanupIDPrefix+"group_alt", CleanupIDPrefix+"system_account_admin")
	for _, account := range []string{"account_primary", "account_secondary"} {
		seed(`INSERT INTO accounts (id, name, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, health_check_endpoint_mode, health_check_model) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			CleanupIDPrefix+account, CleanupNamePrefix+account, CleanupIDPrefix+"system_account_admin",
			"openai", "profile_openai_openai_v1", "openai_responses", "responses_sse", "deepseek-v4.1-flash")
		seed(`INSERT INTO account_supported_models (account_id, model) VALUES (?, ?)`,
			CleanupIDPrefix+account, "deepseek-v4.1-flash")
	}
	seed(`INSERT INTO provider_model_catalog (model, status, mode, catalog_order) VALUES (?, 'active', 'text', 1)`, "deepseek-v4.1-flash")
	seed(`INSERT INTO provider_model_catalog (model, status, mode, catalog_order) VALUES (?, 'active', 'text', 2)`, "deepseek-v4.1-pro")
	seed(`INSERT INTO provider_model_catalog (model, status, mode, catalog_order) VALUES (?, 'active', 'image', 3)`, "gpt-image-2")
	seed(`INSERT INTO model_check_question_bank (id, title, status) VALUES (?, ?, 'approved')`,
		CleanupIDPrefix+"question_approved_1", CleanupNamePrefix+"题库题目一")
	seed(`INSERT INTO model_check_question_bank (id, title, status) VALUES (?, ?, 'approved')`,
		CleanupIDPrefix+"question_approved_2", CleanupNamePrefix+"题库题目二")
	seed(`INSERT INTO model_check_question_bank (id, title, status) VALUES (?, ?, 'pending')`,
		CleanupIDPrefix+"question_pending", CleanupNamePrefix+"未审核题目")
	return e
}

func shardCountString(count int) string { return strconv.Itoa(count) }

// openOrFail 取已存在存储的句柄（测试辅助）。
func (e *env) openOrFail(t *testing.T, storeName string) *sql.DB {
	t.Helper()
	db, err := e.openExisting(storeName)
	if err != nil {
		t.Fatalf("打开 %s: %v", storeName, err)
	}
	if db == nil {
		t.Fatalf("存储 %s 不存在", storeName)
	}
	return db
}

// chatCodexTestCount 在指定存储上执行计数查询。
func chatCodexTestCount(t *testing.T, e *env, storeName, query string, args ...any) int {
	t.Helper()
	db, err := e.openExisting(storeName)
	if err != nil || db == nil {
		t.Fatalf("打开 %s: %v", storeName, err)
	}
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("计数 %s: %v", query, err)
	}
	return count
}

// chatCodexTestValues 读取一列文本（NULL 归一成空串）。
func chatCodexTestValues(t *testing.T, e *env, storeName, query string, args ...any) []string {
	t.Helper()
	db, err := e.openExisting(storeName)
	if err != nil || db == nil {
		t.Fatalf("打开 %s: %v", storeName, err)
	}
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("查询 %s: %v", query, err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("扫描 %s: %v", query, err)
		}
		values = append(values, value.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 %s: %v", query, err)
	}
	return values
}

// chatCodexSeed 跑一次本域并返回结果。
func chatCodexSeed(t *testing.T, e *env) DomainResult {
	t.Helper()
	result, err := seedChatCodexModelCheck(context.Background(), e)
	if err != nil {
		t.Fatalf("seedChatCodexModelCheck: %v", err)
	}
	if result.Name != DomainChatCodexModelCheck {
		t.Fatalf("域名 = %q", result.Name)
	}
	return result
}

func TestSeedChatCodexModelCheckWritesEveryOwnedTable(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	result := chatCodexSeed(t, e)
	cases := []struct {
		store string
		table string
		key   string
		min   int
	}{
		{StoreChat, "chat_conversations", "chat_conversations", 2},
		{StoreChat, "chat_messages", "chat_messages", 6},
		{StoreChat, "chat_message_idempotency", "chat_message_idempotency", 1},
		{StoreChat, "chat_user_storage_windows", "chat_user_storage_windows", 1},
		{StoreChat, "chat_user_asset_usage", "chat_user_asset_usage", 1},
		{StoreChat, "chat_context_checkpoints", "chat_context_checkpoints", 1},
		{StoreChat, "chat_context_entries", "chat_context_entries", 1},
		{StoreChat, "chat_assets", "chat_assets", 2},
		{StoreChat, "chat_asset_references", "chat_asset_references", 1},
		{StoreChat, "chat_image_generations", "chat_image_generations", 1},
		{codexContextShardName(0), "codex_context_sessions", "codex_context_sessions", 0},
		{StoreModelCheck, "model_check_runs", "model_check_runs", 4},
		{StoreModelCheck, "model_check_items", "model_check_items", 6},
		{StoreModelCheck, "model_check_observations", "model_check_observations", 2},
		{StoreModelCheck, "model_check_input_versions", "model_check_input_versions", 1},
		{StoreModelCheck, "model_check_inputs", "model_check_inputs", 1},
		{StoreModelCheck, "model_check_execution_claims", "model_check_execution_claims", 1},
		{StoreModelCheck, "model_check_outcomes", "model_check_outcomes", 1},
		{StoreModelCheck, "model_check_scheduler_tasks", "model_check_scheduler_tasks", 1},
		{StoreModelCheck, "model_account_trust_results", "model_account_trust_results", 1},
		{StoreModelCheck, "model_trust_latest_dirty_accounts", "model_trust_latest_dirty_accounts", 1},
		{StoreModelCheck, "model_trust_observation_receipts", "model_trust_observation_receipts", 1},
		{StoreModelCheck, "model_trust_aggregation_state", "model_trust_aggregation_state", 1},
		{StoreModelCheck, "model_token_intercept_baseline_versions", "model_token_intercept_baseline_versions", 1},
		{StoreModelCheck, "account_quality_health_hourly", "account_quality_health_hourly", 1},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.table, func(t *testing.T) {
			// codex 三表跨分片分布，按全部分片求和。
			rows := 0
			for _, item := range e.stores() {
				if !strings.HasPrefix(item.Name, StoreCodexContextShardPrefix+"[") &&
					item.Name != StoreChat && item.Name != StoreModelCheck {
					continue
				}
				exists, err := e.existsTable(context.Background(), item.Name, testCase.table)
				if err != nil {
					t.Fatal(err)
				}
				if !exists {
					continue
				}
				rows += chatCodexTestCount(t, e, item.Name, "SELECT COUNT(*) FROM "+testCase.table)
			}
			if rows < testCase.min {
				t.Fatalf("%s 行数 = %d，要求至少 %d", testCase.table, rows, testCase.min)
			}
			if result.Counts[testCase.key] != rows {
				t.Fatalf("counts[%s] = %d，表行数 = %d", testCase.key, result.Counts[testCase.key], rows)
			}
		})
	}
}

func TestSeedChatCodexModelCheckChatSampleMatrix(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	chatCodexSeed(t, e)
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"置顶会话", "SELECT COUNT(*) FROM chat_conversations WHERE is_pinned = 1", 1},
		{"达轮次上限会话", "SELECT COUNT(*) FROM chat_conversations WHERE user_turn_count = 50 AND next_sequence_no = 101", 1},
		{"用户消息", "SELECT COUNT(*) FROM chat_messages WHERE role = 'user' AND client_message_id IS NOT NULL", 6},
		{"助手消息", "SELECT COUNT(*) FROM chat_messages WHERE role = 'assistant' AND client_message_id IS NULL", 6},
		{"失败消息", "SELECT COUNT(*) FROM chat_messages WHERE status = 'failed' AND error_code IS NOT NULL", 1},
		{"reasoning 块", "SELECT COUNT(*) FROM chat_messages WHERE content_blocks_json LIKE '%\"type\":\"reasoning\"%'", 2},
		{"工具调用块", "SELECT COUNT(*) FROM chat_messages WHERE content_blocks_json LIKE '%\"type\":\"tool_call\"%'", 2},
		{"图片输出块", "SELECT COUNT(*) FROM chat_messages WHERE content_blocks_json LIKE '%\"type\":\"output_image\"%'", 2},
		{"图片输入块", "SELECT COUNT(*) FROM chat_messages WHERE content_blocks_json LIKE '%\"type\":\"input_image\"%'", 1},
		{"上下文检查点", "SELECT COUNT(*) FROM chat_context_checkpoints WHERE status = 'active'", 1},
		{"生成操作", "SELECT COUNT(*) FROM chat_image_generations WHERE operation = 'generate'", 1},
		{"编辑操作", "SELECT COUNT(*) FROM chat_image_generations WHERE operation = 'edit'", 1},
		{"资产引用", "SELECT COUNT(*) FROM chat_asset_references", 3},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			rows := chatCodexTestCount(t, e, StoreChat, testCase.query)
			if rows != testCase.want {
				t.Fatalf("%s: 命中 %d 行，期望 %d", testCase.name, rows, testCase.want)
			}
		})
	}
	// 达轮次上限会话必须真的处在「最后一轮」：seq 99/100 是最后两条消息，
	// 且 user_turn_count 等于 gateway 默认上限。
	if got := chatCodexTestCount(t, e, StoreChat,
		`SELECT COUNT(*) FROM chat_conversations c WHERE c.user_turn_count = 50
		  AND (SELECT MAX(sequence_no) FROM chat_messages m WHERE m.conversation_id = c.id) = c.next_sequence_no - 1`); got != 1 {
		t.Fatalf("达轮次上限样本的最后一条消息序号应为 next_sequence_no-1，命中 %d", got)
	}
	// 内容块必须是合法 JSON 数组（前端 parseContentBlocks 直接解析）。
	for _, blocks := range chatCodexTestValues(t, e, StoreChat, "SELECT content_blocks_json FROM chat_messages") {
		var decoded []map[string]any
		if err := json.Unmarshal([]byte(blocks), &decoded); err != nil {
			t.Fatalf("content_blocks_json 不是 JSON 数组: %v (%s)", err, blocks)
		}
	}
	// 图片生成的行必须指向同会话的资产，且计费字段齐全。
	rows := chatCodexTestValues(t, e, StoreChat,
		`SELECT g.operation || '|' || g.model || '|' || g.size || '|' || g.quality || '|' || g.output_format
		 FROM chat_image_generations g JOIN chat_assets a ON a.id = g.asset_id AND a.conversation_id = g.conversation_id
		 ORDER BY g.operation`)
	if len(rows) != 2 {
		t.Fatalf("图片生成关联行 = %v", rows)
	}
	for _, row := range rows {
		if strings.Contains(row, "||") || strings.HasSuffix(row, "|") {
			t.Fatalf("图片生成计费字段为空: %q", row)
		}
	}
}

func TestSeedChatCodexModelCheckAssetFilesExist(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	chatCodexSeed(t, e)
	root := e.options.Paths.ChatAssetsRoot
	db, err := e.openExisting(StoreChat)
	if err != nil || db == nil {
		t.Fatalf("打开 chat: %v", err)
	}
	type assetRow struct {
		id         string
		mime       string
		bytes      int
		sha        string
		key        string
		previewKey sql.NullString
		previewLen sql.NullInt64
	}
	rows, err := db.QueryContext(context.Background(), `SELECT id, processed_mime_type, processed_bytes, processed_sha256,
		storage_key, preview_storage_key, preview_bytes FROM chat_assets ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var assets []assetRow
	for rows.Next() {
		var item assetRow
		if err := rows.Scan(&item.id, &item.mime, &item.bytes, &item.sha, &item.key, &item.previewKey, &item.previewLen); err != nil {
			t.Fatal(err)
		}
		assets = append(assets, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(assets) < 2 {
		t.Fatalf("资产行数 = %d，要求至少 2", len(assets))
	}
	magic := map[string][]byte{
		"image/png":  {0x89, 'P', 'N', 'G'},
		"image/jpeg": {0xff, 0xd8, 0xff},
		"image/webp": {'R', 'I', 'F', 'F'},
	}
	checked := 0
	for _, asset := range assets {
		path := filepath.Join(root, filepath.FromSlash(asset.key))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("资产文件缺失 %s: %v", path, err)
		}
		if len(data) != asset.bytes {
			t.Fatalf("资产文件字节 %d != processed_bytes %d", len(data), asset.bytes)
		}
		expected, ok := magic[asset.mime]
		if !ok {
			t.Fatalf("未知处理后类型 %q", asset.mime)
		}
		if len(data) < len(expected) || string(data[:len(expected)]) != string(expected) {
			t.Fatalf("资产 %s 的 magic 与 %s 不符: % x", asset.id, asset.mime, data[:len(expected)])
		}
		if chatCodexDigest(data) != asset.sha {
			t.Fatalf("资产 %s 的 sha256 与文件不一致", asset.id)
		}
		checked++
		if asset.previewKey.Valid {
			previewPath := filepath.Join(root, filepath.FromSlash(asset.previewKey.String))
			preview, err := os.ReadFile(previewPath)
			if err != nil {
				t.Fatalf("预览文件缺失 %s: %v", previewPath, err)
			}
			if int64(len(preview)) != asset.previewLen.Int64 {
				t.Fatalf("预览文件字节 %d != preview_bytes %d", len(preview), asset.previewLen.Int64)
			}
			// WebP 容器结构自检（标准库没有 WebP 解码器，这里校验容器自洽：
			// RIFF/WEBP/VP8L 标识 + RIFF size = 文件长度-8 + 块长 = payload+填充）。
			if len(preview) != 34 || string(preview[:4]) != "RIFF" || string(preview[8:12]) != "WEBP" ||
				string(preview[12:16]) != "VP8L" || int(preview[4]) != len(preview)-8 {
				t.Fatalf("预览文件不是合法最小 WebP: % x", preview)
			}
			if int(preview[16]) != len(preview)-21 {
				t.Fatalf("VP8L 块长与文件长度不一致: % x", preview)
			}
		}
	}
	if checked < 2 {
		t.Fatalf("校验过的资产 = %d", checked)
	}
	// 图片生成必须指向 assistant_generated 资产（生成图不是用户上传）。
	if got := chatCodexTestCount(t, e, StoreChat,
		`SELECT COUNT(*) FROM chat_image_generations g JOIN chat_assets a ON a.id = g.asset_id
		  WHERE a.source_kind <> 'assistant_generated'`); got != 0 {
		t.Fatalf("图片生成指向了非生成资产: %d 行", got)
	}
}

func TestSeedChatCodexModelCheckCodexShardsAndReferences(t *testing.T) {
	const shardCount = 3
	e := chatCodexTestEnv(t, shardCount)
	chatCodexSeed(t, e)
	populated := map[string]bool{}
	fullSet := map[string]bool{}
	for _, item := range e.stores() {
		if !strings.HasPrefix(item.Name, StoreCodexContextShardPrefix+"[") {
			continue
		}
		exists, err := e.existsTable(context.Background(), item.Name, "codex_context_sessions")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			continue
		}
		sessions := chatCodexTestCount(t, e, item.Name, "SELECT COUNT(*) FROM codex_context_sessions")
		responses := chatCodexTestCount(t, e, item.Name, "SELECT COUNT(*) FROM codex_context_responses")
		compacts := chatCodexTestCount(t, e, item.Name, "SELECT COUNT(*) FROM codex_context_compacts")
		if sessions+responses+compacts > 0 {
			populated[item.Name] = true
		}
		// 「≥2 个分片各 ≥1 组会话 / 响应 / 摘要」：分片对齐组按目标分片挑 ID，
		// 三种行都落在运行时哈希算出的分片上。
		if sessions > 0 && responses > 0 && compacts > 0 {
			fullSet[item.Name] = true
		}
	}
	wantShards := 2
	if shardCount < wantShards {
		wantShards = shardCount
	}
	if len(populated) < wantShards {
		t.Fatalf("有数据的 codex 分片 = %v，要求至少 %d 个", populated, wantShards)
	}
	if len(fullSet) < wantShards {
		t.Fatalf("含完整会话/响应/摘要三表的分片 = %v，要求至少 %d 个", fullSet, wantShards)
	}
	// 会话 / 响应 / 摘要三表都要有行（跨分片合计），且行必须落在运行时哈希
	// 得到的分片里。
	for _, table := range []string{"codex_context_sessions", "codex_context_responses", "codex_context_compacts"} {
		total := 0
		for _, item := range e.stores() {
			if !strings.HasPrefix(item.Name, StoreCodexContextShardPrefix+"[") {
				continue
			}
			exists, err := e.existsTable(context.Background(), item.Name, table)
			if err != nil {
				t.Fatal(err)
			}
			if !exists {
				continue
			}
			total += chatCodexTestCount(t, e, item.Name, "SELECT COUNT(*) FROM "+table)
		}
		if total == 0 {
			t.Fatalf("%s 没有任何行", table)
		}
	}
	idColumn := map[string]string{
		"codex_context_sessions":  "id",
		"codex_context_responses": "response_id",
		"codex_context_compacts":  "compact_id",
	}
	for table, column := range idColumn {
		for _, item := range e.stores() {
			if !strings.HasPrefix(item.Name, StoreCodexContextShardPrefix+"[") {
				continue
			}
			exists, err := e.existsTable(context.Background(), item.Name, table)
			if err != nil {
				t.Fatal(err)
			}
			if !exists {
				continue
			}
			index := chatCodexShardNameIndex(t, item.Name)
			for _, id := range chatCodexTestValues(t, e, item.Name, "SELECT "+column+" FROM "+table) {
				if got := chatCodexShardIndex(id, shardCount); got != index {
					t.Fatalf("%s.%s = %q 落在分片 %d，运行时哈希为 %d", table, column, id, index, got)
				}
			}
		}
	}
}

// chatCodexShardNameIndex 从 "codex-context-shard[N]" 里取 N。
func chatCodexShardNameIndex(t *testing.T, name string) int {
	t.Helper()
	start := strings.LastIndex(name, "[")
	end := strings.LastIndex(name, "]")
	if start < 0 || end <= start {
		t.Fatalf("非法分片存储名 %q", name)
	}
	value := 0
	for _, char := range name[start+1 : end] {
		value = value*10 + int(char-'0')
	}
	return value
}

func TestSeedChatCodexModelCheckObservationsBindDeepRunsOnly(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	chatCodexSeed(t, e)
	// observation 只绑定 profile='full' 的运行。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_observations o JOIN model_check_runs r ON r.id = o.run_id WHERE r.profile <> 'full'`); got != 0 {
		t.Fatalf("observation 绑定了非深度运行: %d 行", got)
	}
	// 可信度计数字段必须与「聚合器作用域内」的 observation 事实一致：作用域是
	// (system_account_id, account_id, requested_model)，与运行时 trustObservations
	// 的查询条件相同。
	var observationCount, roundCount, identityCount, sourceCount int
	if err := e.openOrFail(t, StoreModelCheck).QueryRowContext(context.Background(),
		`SELECT COUNT(*), COUNT(DISTINCT o.round_index),
		        SUM(CASE WHEN o.probe_family = 'identity_observation' THEN 1 ELSE 0 END),
		        COUNT(DISTINCT o.upstream_bucket_hmac)
		 FROM model_check_observations o JOIN model_account_trust_results t
		   ON t.system_account_id = o.system_account_id AND t.account_id = o.account_id
		  AND t.requested_model = o.requested_model`).Scan(&observationCount, &roundCount, &identityCount, &sourceCount); err != nil {
		t.Fatal(err)
	}
	var trustObservationCount, trustRoundCount, trustIdentityCount, trustSourceCount int
	if err := e.openOrFail(t, StoreModelCheck).QueryRowContext(context.Background(),
		`SELECT observation_count, round_count, identity_observation_count, independent_source_count
		 FROM model_account_trust_results`).Scan(&trustObservationCount, &trustRoundCount, &trustIdentityCount, &trustSourceCount); err != nil {
		t.Fatal(err)
	}
	if trustObservationCount != observationCount || trustRoundCount != roundCount ||
		trustIdentityCount != identityCount || trustSourceCount != sourceCount {
		t.Fatalf("可信度计数与 observation 不一致: trust=(%d,%d,%d,%d) observation=(%d,%d,%d,%d)",
			trustObservationCount, trustRoundCount, trustIdentityCount, trustSourceCount,
			observationCount, roundCount, identityCount, sourceCount)
	}
	// 最新可信结果的 last_observed_id 必须是一条真实 observation；已聚合的
	// observation 必须有回执；回执的创建时刻必须与 observation 行一致。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_account_trust_results t
		  WHERE t.last_observed_id IS NULL
		     OR NOT EXISTS (SELECT 1 FROM model_check_observations o WHERE o.id = t.last_observed_id)`); got != 0 {
		t.Fatalf("最新可信结果没有指向真实 observation: %d 行", got)
	}
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_observations o
		  WHERE o.aggregation_completed_at IS NOT NULL
		    AND NOT EXISTS (SELECT 1 FROM model_trust_observation_receipts r WHERE r.observation_id = o.id)`); got != 0 {
		t.Fatalf("已聚合 observation 缺少回执: %d 行", got)
	}
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_trust_observation_receipts r JOIN model_check_observations o ON o.id = r.observation_id
		  WHERE r.observation_created_at <> o.created_at`); got != 0 {
		t.Fatalf("回执创建时刻与 observation 不一致: %d 行", got)
	}
	// 未聚合的深度运行必须留下脏账户行（聚合成功后该行才会被删除）。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_trust_latest_dirty_accounts d
		  WHERE NOT EXISTS (
		    SELECT 1 FROM model_check_observations o JOIN model_check_runs r ON r.id = o.run_id
		    WHERE o.aggregation_completed_at IS NULL
		      AND o.account_id = d.account_id AND o.requested_model = d.requested_model)`); got != 0 {
		t.Fatalf("脏账户行没有对应的未聚合 observation: %d 行", got)
	}
	// 聚合游标指向最后一条 observation，且作用域是运行时唯一作用域。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_trust_aggregation_state s
		  WHERE s.scope_key = '`+chatCodexTrustScopeKey+`' AND s.cursor_id IS NOT NULL
		    AND EXISTS (SELECT 1 FROM model_check_observations o WHERE o.id = s.cursor_id)`); got != 1 {
		t.Fatalf("聚合游标行不合法: %d 行", got)
	}
	// 基线必须满足 hmac-sha256-v1:<64 hex> 格式（运行时激活校验的形状）。
	baseline := chatCodexTestValues(t, e, StoreModelCheck, "SELECT cohort_key_hmac FROM model_token_intercept_baseline_versions")
	if len(baseline) != 1 || !strings.HasPrefix(baseline[0], "hmac-sha256-v1:") || len(baseline[0]) != len("hmac-sha256-v1:")+64 {
		t.Fatalf("基线 cohort_key_hmac = %v", baseline)
	}
}

func TestSeedChatCodexModelCheckRunMatrixAndTerminalTasks(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	chatCodexSeed(t, e)
	cases := []struct {
		profile string
		status  string
	}{
		{"quick", "running"}, {"quick", "completed"}, {"quick", "failed"}, {"quick", "canceled"},
		{"full", "running"}, {"full", "completed"}, {"full", "failed"}, {"full", "canceled"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.profile+"_"+testCase.status, func(t *testing.T) {
			got := chatCodexTestCount(t, e, StoreModelCheck,
				"SELECT COUNT(*) FROM model_check_runs WHERE profile = ? AND status = ?", testCase.profile, testCase.status)
			if got < 1 {
				t.Fatalf("缺少 %s/%s 样本", testCase.profile, testCase.status)
			}
		})
	}
	// 运行中样本不能带结束时间 / 耗时（页面据此显示执行中）。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_runs WHERE status = 'running' AND (finished_at IS NOT NULL OR duration_ms IS NOT NULL)`); got != 0 {
		t.Fatalf("运行中样本带了结束字段: %d 行", got)
	}
	// 调度任务只写终态：pending/failed 的行会被运行时真的认领并发起上游探测。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_scheduler_tasks WHERE state <> 'completed' OR claim_owner IS NOT NULL OR claim_until IS NOT NULL`); got != 0 {
		t.Fatalf("调度任务不是终态或带认领者: %d 行", got)
	}
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_scheduler_tasks WHERE json_valid(payload) = 0`); got != 0 {
		t.Fatalf("调度任务 payload 不是合法 JSON: %d 行", got)
	}
	// 认领行的租约必须已经释放（claim_until 在过去），否则运行时会把造数行
	// 当成活跃持有者。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_execution_claims WHERE claim_until >= ?`, chatCodexStamp(chatCodexTestNow)); got != 0 {
		t.Fatalf("认领行仍是活跃租约: %d 行", got)
	}
	// 深度完成样本的 resultSummary 必须带 trustReport（详情抽屉的读数来源）。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_runs WHERE profile = 'full' AND status = 'completed'
		  AND result_summary_json LIKE '%"trustReport"%'`); got < 1 {
		t.Fatalf("深度完成样本缺少 trustReport: %d 行", got)
	}
	// 题库环节只引用 status='approved' 的题目。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_items WHERE item_type = 'custom_quiz'`); got != 2 {
		t.Fatalf("题库检查项 = %d，期望 2", got)
	}
}

func TestSeedChatCodexModelCheckInputDigestMatchesSnapshot(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	chatCodexSeed(t, e)
	ctx := context.Background()
	var (
		inputID, identityKey, targetID, digest string
		configRevision, policyRevision         string
		trigger                                string
		issuedAt, expiresAt                    string
		payload                                []byte
	)
	if err := e.openOrFail(t, StoreModelCheck).QueryRowContext(ctx,
		`SELECT input_id, identity_key, target_id, input_digest, config_revision, policy_revision, trigger,
		        issued_at, expires_at, payload FROM model_check_inputs`).Scan(
		&inputID, &identityKey, &targetID, &digest, &configRevision, &policyRevision, &trigger,
		&issuedAt, &expiresAt, &payload); err != nil {
		t.Fatal(err)
	}
	issued, err := time.Parse(time.RFC3339Nano, issuedAt)
	if err != nil {
		t.Fatalf("issued_at 不可解析: %v", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		t.Fatalf("expires_at 不可解析: %v", err)
	}
	recomputed, err := chatCodexInputDigest(inputID, identityKey, targetID, configRevision, policyRevision, trigger, issued, expires, payload)
	if err != nil {
		t.Fatal(err)
	}
	if recomputed != digest {
		t.Fatalf("input_digest 与快照不一致: 库内 %s，重算 %s", digest, recomputed)
	}
	// 认领 / 产出的外键闭环。
	if got := chatCodexTestCount(t, e, StoreModelCheck,
		`SELECT COUNT(*) FROM model_check_outcomes o
		  WHERE o.input_digest <> (SELECT i.input_digest FROM model_check_inputs i WHERE i.input_id = o.input_id)
		     OR o.committed <> 1
		     OR o.payload_digest <> '' AND o.payload_digest IS NULL`); got != 0 {
		t.Fatalf("产出与输入不一致: %d 行", got)
	}
}

func TestSeedChatCodexModelCheckIsIdempotent(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	first := chatCodexSeed(t, e)
	tables := []string{
		"chat_conversations", "chat_messages", "chat_message_idempotency", "chat_user_storage_windows",
		"chat_user_asset_usage", "chat_context_checkpoints", "chat_context_entries", "chat_assets",
		"chat_asset_references", "chat_image_generations",
	}
	before := map[string]int{}
	for _, table := range tables {
		before[table] = chatCodexTestCount(t, e, StoreChat, "SELECT COUNT(*) FROM "+table)
	}
	modelCheckTables := []string{
		"model_check_runs", "model_check_items", "model_check_observations", "model_check_inputs",
		"model_check_input_versions", "model_check_execution_claims", "model_check_outcomes",
		"model_check_scheduler_tasks", "model_account_trust_results", "model_trust_latest_dirty_accounts",
		"model_trust_observation_receipts", "model_trust_aggregation_state",
		"model_token_intercept_baseline_versions", "account_quality_health_hourly",
	}
	beforeModel := map[string]int{}
	for _, table := range modelCheckTables {
		beforeModel[table] = chatCodexTestCount(t, e, StoreModelCheck, "SELECT COUNT(*) FROM "+table)
	}
	second := chatCodexSeed(t, e)
	if len(second.Counts) != len(first.Counts) {
		t.Fatalf("两次运行的 count 键数量不同: %d vs %d", len(first.Counts), len(second.Counts))
	}
	for key, value := range first.Counts {
		if second.Counts[key] != value {
			t.Fatalf("counts[%s] 第二次 = %d，第一次 = %d", key, second.Counts[key], value)
		}
	}
	for _, table := range tables {
		if got := chatCodexTestCount(t, e, StoreChat, "SELECT COUNT(*) FROM "+table); got != before[table] {
			t.Fatalf("%s 行数从 %d 变成 %d", table, before[table], got)
		}
	}
	for _, table := range modelCheckTables {
		if got := chatCodexTestCount(t, e, StoreModelCheck, "SELECT COUNT(*) FROM "+table); got != beforeModel[table] {
			t.Fatalf("%s 行数从 %d 变成 %d", table, beforeModel[table], got)
		}
	}
	// 基线行没有可承载清理标识的标识列，靠域自身的 delete-first 保证不叠加：
	// 第二次运行后仍然恰好 1 行（这里显式钉住，避免将来只依赖清理）。
	if got := chatCodexTestCount(t, e, StoreModelCheck, "SELECT COUNT(*) FROM model_token_intercept_baseline_versions"); got != 1 {
		t.Fatalf("基线行数 = %d，要求恰好 1", got)
	}
}

func TestSeedChatCodexModelCheckSkipsWithoutBusinessResources(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 7, DailyRequests: 20, Now: chatCodexTestNow}, nil)
	t.Cleanup(func() { _ = e.Close() })
	result := chatCodexSeed(t, e)
	if len(result.Counts) != 0 {
		t.Fatalf("空数据根上 counts 应为空: %v", result.Counts)
	}
	// 空数据根上不得凭造数创建 chat / 模型检测库：前者由 --ensure-schema 建，
	// 后者只在该段真正有前置资源时才建。
	for _, item := range []string{StoreChat, StoreModelCheck} {
		if _, err := os.Stat(e.byName[item].Path); !os.IsNotExist(err) {
			t.Fatalf("%s 不应被创建: %v", item, err)
		}
	}
}

func TestSeedChatCodexModelCheckSatisfiesCoverageAssertions(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	result := chatCodexSeed(t, e)
	e.recordDomainResult(result)
	notCovered, failures := evaluateAssertions(context.Background(), e)
	for _, failure := range failures {
		t.Fatalf("chat/模型检测域断言失败: %s", failure)
	}
	for _, item := range notCovered {
		for _, name := range []string{"chat_conversations", "chat_messages", "model_check_runs", "model_check_observations"} {
			if strings.HasPrefix(item, name+"（") {
				t.Fatalf("已接线域断言不得为 not-covered: %s", item)
			}
		}
	}
}

func TestSeedChatCodexModelCheckRowsCarryCleanupMarkers(t *testing.T) {
	e := chatCodexTestEnv(t, 3)
	chatCodexSeed(t, e)
	ctx := context.Background()
	// 显式清理规则覆盖的表：清理后必须一行不剩。
	explicit := []struct {
		store string
		table string
		query string
		args  []any
	}{
		{StoreChat, "chat_messages", "DELETE FROM chat_messages WHERE id LIKE ? OR trace_id LIKE ? OR conversation_id LIKE ?",
			[]any{CleanupIDPrefix + "%", CleanupTracePrefix + "%", CleanupIDPrefix + "%"}},
		{StoreChat, "chat_conversations", "DELETE FROM chat_conversations WHERE id LIKE ?", []any{CleanupIDPrefix + "%"}},
	}
	for _, rule := range explicit {
		if _, err := e.exec(ctx, rule.store, rule.query, rule.args...); err != nil {
			t.Fatalf("清理 %s: %v", rule.table, err)
		}
		if got := chatCodexTestCount(t, e, rule.store, "SELECT COUNT(*) FROM "+rule.table); got != 0 {
			t.Fatalf("显式规则清理后 %s 还剩 %d 行", rule.table, got)
		}
	}
	// 通用标识扫描：全部 chat 表与除基线外的 J3b 表都必须被标识前缀扫空。
	swept := []struct {
		store string
		table string
	}{
		{StoreChat, "chat_message_idempotency"},
		{StoreChat, "chat_user_storage_windows"},
		{StoreChat, "chat_user_asset_usage"},
		{StoreChat, "chat_context_checkpoints"},
		{StoreChat, "chat_context_entries"},
		{StoreChat, "chat_assets"},
		{StoreChat, "chat_asset_references"},
		{StoreChat, "chat_image_generations"},
		{StoreModelCheck, "model_check_runs"},
		{StoreModelCheck, "model_check_items"},
		{StoreModelCheck, "model_check_observations"},
		{StoreModelCheck, "model_check_inputs"},
		{StoreModelCheck, "model_check_input_versions"},
		{StoreModelCheck, "model_check_execution_claims"},
		{StoreModelCheck, "model_check_outcomes"},
		{StoreModelCheck, "model_check_scheduler_tasks"},
		{StoreModelCheck, "model_account_trust_results"},
		{StoreModelCheck, "model_trust_latest_dirty_accounts"},
		{StoreModelCheck, "model_trust_observation_receipts"},
		{StoreModelCheck, "model_trust_aggregation_state"},
		{StoreModelCheck, "account_quality_health_hourly"},
	}
	for _, item := range swept {
		db, err := e.openExisting(item.store)
		if err != nil || db == nil {
			t.Fatalf("打开 %s: %v", item.store, err)
		}
		columns, err := queryTableColumns(ctx, db, item.table)
		if err != nil {
			t.Fatal(err)
		}
		query := sweepDeleteQuery(item.table, columns)
		if query == "" {
			t.Fatalf("%s 没有可扫描的标识列，通用清理覆盖不到", item.table)
		}
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("通用清理 %s: %v", item.table, err)
		}
		if got := chatCodexTestCount(t, e, item.store, "SELECT COUNT(*) FROM "+item.table); got != 0 {
			t.Fatalf("通用清理后 %s 还剩 %d 行", item.table, got)
		}
	}
}

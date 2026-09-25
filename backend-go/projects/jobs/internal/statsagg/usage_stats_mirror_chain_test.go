package statsagg

// SQLite standalone 全链路集成测试（用量统计聚合源断链修复的验收面）：
// gateway 形状 spool 文件 → usagespooldrain（真实解析/归一化）→
// usagewriter.Writer（真实队列/写计划/分片写 + stats 库镜像写）→ 真实
// Aggregator 聚合（含业务库授权链查找）→ 断言 stats 镜像行与投影表数值。
//
// 库形状对齐生产 dev 拓扑：statsverify EnsureSchema 建 usage_records（聚合
// 41 列形状）与 client-ip 子集；聚合投影表由 SQLiteTestSchema 补齐（生产里
// 由 gateway bootstrap 经 maintenance EnsureSQLiteStats 创建，jobs 模块不依
// 赖 maintenance，测试用同形 DDL 替身）。resource_authorizations / accounts
// 只允许出现在业务库——stats 侧影子表会让授权查找的句柄修复失去覆盖。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagespooldrain"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

func chainOpenSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	// 与 worker_assembly openSQLite 同款：单连接 + busy_timeout + WAL。
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("打开 chain sqlite 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		_ = db.Close()
		t.Fatalf("配置 chain sqlite WAL 失败: %v", err)
	}
	return db
}

func chainIntPtr(value int) *int             { return &value }
func chainFloat64Ptr(value float64) *float64 { return &value }
func chainQueryCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("查询 %s 失败: %v", query, err)
	}
	return count
}

// TestUsageStatsMirrorChainSQLite 驱动真实生产函数走完
// spool → drain → writer（分片+镜像）→ 聚合 全链。
func TestUsageStatsMirrorChainSQLite(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "stats.sqlite3")
	businessPath := filepath.Join(dir, "business.sqlite3")
	shardRoot := filepath.Join(dir, "usage-shards")
	spoolRoot := filepath.Join(dir, "usage-record-spool")

	// 业务库 fixture：授权链真实形状（resource_authorizations 带 resource/
	// grantee 列、accounts 带 instance 授权列），先于 statsverify 建库落盘，
	// 其精简业务 DDL 对同名表 IF NOT EXISTS no-op（生产 dev 由 gateway 先建
	// 全量业务表，测试以查找所需的最小列集替身）。
	businessDB := chainOpenSQLite(t, businessPath)
	defer businessDB.Close()
	if _, err := businessDB.Exec(`
		CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY,
			resource_type TEXT NOT NULL,
			resource_id TEXT,
			grantee_system_account_id TEXT
		);
		CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			authorization_instance_authorization_id TEXT
		);
		INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id)
		  VALUES ('auth-1', 'account', 'acct-1', 'sys-caller');
		INSERT INTO accounts (id, system_account_id, authorization_instance_authorization_id)
		  VALUES ('acct-1', 'sys-owner', 'auth-1'), ('acct-instance-caller', 'sys-caller', 'auth-1');
	`); err != nil {
		t.Fatalf("seed 业务库授权链失败: %v", err)
	}

	// stats 库：生产 statsverify schema（含本修复的聚合 41 列 usage_records）。
	store, err := statsverify.OpenStore(statsverify.StoreConfig{
		Mode:               statsverify.StoreSQLite,
		SQLiteStatsPath:    statsPath,
		SQLiteBusinessPath: businessPath,
	})
	if err != nil {
		t.Fatalf("打开 statsverify store 失败: %v", err)
	}
	defer store.Close()
	statsDB := chainOpenSQLite(t, statsPath)
	defer statsDB.Close()
	for _, statement := range SQLiteTestSchema {
		// 授权链表只在业务库；stats 侧影子表会让 B 修复失去覆盖。
		if strings.HasPrefix(statement, "CREATE TABLE IF NOT EXISTS resource_authorizations (") ||
			strings.HasPrefix(statement, "CREATE TABLE IF NOT EXISTS accounts (") {
			continue
		}
		if _, err := statsDB.Exec(statement); err != nil {
			t.Fatalf("建聚合投影表失败: %v", err)
		}
	}
	// 生产 dev 拓扑里 gateway bootstrap 会跑 maintenance EnsureSQLiteStats，
	// 为遗留库补 success_cost_usd 列（配额成功口径）。statsverify 的建库 DDL
	// 尚未同步该列（另一写域），这里在测试内等价复刻该加法迁移，保证聚合
	// INSERT 与生产 schema 一致。
	ensureSuccessCostUsdColumns(t, statsDB)

	// usagewriter：真实分片写 + stats 镜像写。
	catalogDB := chainOpenSQLite(t, filepath.Join(dir, "usage-catalog.sqlite3"))
	defer catalogDB.Close()
	shardStore := usagewriter.NewSqliteShardStore(usagewriter.SqliteShardStoreConfig{
		CatalogDB:  catalogDB,
		ShardRoot:  shardRoot,
		ShardCount: 4,
		StatsDB:    statsDB,
	})
	if err := shardStore.EnsureCatalogSchema(); err != nil {
		t.Fatalf("初始化 catalog schema 失败: %v", err)
	}
	writer := usagewriter.NewWriter(usagewriter.Config{
		ShardCount:      4,
		ShardRoot:       shardRoot,
		BatchSize:       1,
		FlushIntervalMs: 5,
		RetryDelayMs:    10,
	}, shardStore, nil)
	writer.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// writer 先停（排空后台 flush），随后才关 shard store 的分片句柄，
	// Windows 上未关闭的分片文件会阻塞临时目录清理。
	defer func() {
		writer.Close(context.Background())
		_ = shardStore.Close()
	}()

	// gateway 形状 spool 文件（Persist：json.Marshal + '\n'，单文件单记录）。
	spoolDir := filepath.Join(spoolRoot, "instance-1")
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		t.Fatalf("建 spool 目录失败: %v", err)
	}
	input := usagewriter.UsageRecordInput{
		ID:                             "usage_20260920_s0_chain_0001",
		SystemAccountID:                "sys-caller",
		TraceID:                        "trace-chain-1",
		TrafficSource:                  "gateway",
		APIKeyID:                       "key-1",
		GroupID:                        "grp-1",
		GroupOwnerSystemAccountID:      "sys-owner",
		GroupAccessType:                "owner",
		AccountID:                      "acct-1",
		AccountOwnerSystemAccountID:    "sys-owner",
		AccountAccessType:              "account_authorized",
		AccountAuthorizationID:         "auth-1",
		AccountAuthorizationSourceType: "manual",
		Endpoint:                       "/v1/chat/completions",
		ProviderCode:                   "openai",
		Model:                          "gpt-chain",
		StatusCode:                     chainIntPtr(200),
		Success:                        true,
		FirstTokenMs:                   chainIntPtr(30),
		DurationMs:                     chainIntPtr(120),
		InputTokens:                    chainIntPtr(100),
		OutputTokens:                   chainIntPtr(50),
		CostUsd:                        chainFloat64Ptr(0.25),
		CreatedAt:                      "2026-09-20T10:00:00.000Z",
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("序列化 spool 记录失败: %v", err)
	}
	payload = append(payload, '\n')
	spoolFile := filepath.Join(spoolDir, "0000000000000-chain.json")
	if err := os.WriteFile(spoolFile, payload, 0o644); err != nil {
		t.Fatalf("写 spool 文件失败: %v", err)
	}

	// 真实 drain：解析 → Enqueue → 删除。
	drainer := &usagespooldrain.Drainer{Directory: spoolRoot, Enqueuer: writer, BatchSize: 10}
	processed, err := drainer.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain 失败: %v", err)
	}
	if processed != 1 {
		t.Fatalf("drain 处理文件数 = %d, 期望 1", processed)
	}
	if _, err := os.Stat(spoolFile); !os.IsNotExist(err) {
		t.Fatalf("spool 文件未在入队后删除: %v", err)
	}

	// 等待 writer 后台 flush 排空（分片写 + 镜像写成功后队列清空）。
	deadline := time.Now().Add(5 * time.Second)
	for writer.PendingCount() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("writer 队列未在期限内排空，剩余 %d", writer.PendingCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	writer.Drain()
	if runtime := writer.Runtime(); runtime.DroppedCount != 0 || runtime.DeadLetterCount != 0 {
		t.Fatalf("writer 出现丢弃/死信: %+v", runtime)
	}

	// 分片 + catalog 写入侧不回归。
	if count := chainQueryCount(t, catalogDB, `SELECT COUNT(*) FROM usage_record_shard_entries`); count != 1 {
		t.Fatalf("usage_record_shard_entries 行数 = %d, 期望 1", count)
	}

	// 断言 A：stats 镜像行存在且聚合维度列齐全。
	if count := chainQueryCount(t, statsDB, `SELECT COUNT(*) FROM usage_records`); count != 1 {
		t.Fatalf("stats.usage_records 行数 = %d, 期望 1（镜像写断链未修复）", count)
	}
	var mirroredAuthID, mirroredEndpoint string
	if err := statsDB.QueryRow(
		`SELECT account_authorization_id, endpoint FROM usage_records WHERE id = ?`, input.ID,
	).Scan(&mirroredAuthID, &mirroredEndpoint); err != nil {
		t.Fatalf("读取 stats 镜像行失败: %v", err)
	}
	if mirroredAuthID != "auth-1" || mirroredEndpoint != "/v1/chat/completions" {
		t.Fatalf("stats 镜像行列值不符: account_authorization_id=%q endpoint=%q", mirroredAuthID, mirroredEndpoint)
	}

	// 真实聚合（B：授权查找必须走业务库句柄，stats 库无授权表）。
	aggregator := &Aggregator{
		DB:         statsDB,
		Dialect:    Dialect{Postgres: false},
		Clock:      StaticTimezoneSource{Location: time.UTC},
		BusinessDB: businessDB,
	}
	processedRows, err := aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{
		BatchSize:         100,
		SafeCreatedBefore: "2026-09-20T10:01:00.000Z",
	})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if processedRows != 1 {
		t.Fatalf("聚合处理行数 = %d, 期望 1", processedRows)
	}

	// 断言数值：totals / daily 非零且正确。
	totalsQuery := `SELECT request_count, input_tokens, output_tokens, total_cost_usd
		FROM usage_stats_totals WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ?`
	var requestCount, inputTokens, outputTokens int
	var totalCost float64
	if err := statsDB.QueryRow(totalsQuery, "sys-caller", "sys-caller").Scan(&requestCount, &inputTokens, &outputTokens, &totalCost); err != nil {
		t.Fatalf("读取 usage_stats_totals 失败: %v", err)
	}
	if requestCount != 1 || inputTokens != 100 || outputTokens != 50 || totalCost != 0.25 {
		t.Fatalf("usage_stats_totals 数值不符: request=%d input=%d output=%d cost=%v", requestCount, inputTokens, outputTokens, totalCost)
	}
	dailyRequestCount := 0
	if err := statsDB.QueryRow(
		`SELECT request_count FROM usage_stats_daily WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ? AND stat_date = ?`,
		"sys-caller", "sys-caller", "2026-09-20",
	).Scan(&dailyRequestCount); err != nil {
		t.Fatalf("读取 usage_stats_daily 失败: %v", err)
	}
	if dailyRequestCount != 1 {
		t.Fatalf("usage_stats_daily request_count = %d, 期望 1", dailyRequestCount)
	}
	authScopeRequestCount := 0
	if err := statsDB.QueryRow(
		`SELECT request_count FROM usage_stats_totals WHERE system_account_id = ? AND scope_type = 'account_authorization' AND scope_id = ?`,
		"sys-caller", "auth-1",
	).Scan(&authScopeRequestCount); err != nil {
		t.Fatalf("读取 account_authorization 维度失败: %v", err)
	}
	if authScopeRequestCount != 1 {
		t.Fatalf("account_authorization 维度 request_count = %d, 期望 1", authScopeRequestCount)
	}
	// 授权日报（lookup 的 resource_id 查找结果落地）。
	userSummaryRequestCount := 0
	if err := statsDB.QueryRow(
		`SELECT request_count FROM authorization_user_usage_summary_daily
		 WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = '' AND grantee_filter_system_account_id = ?
		   AND resource_filter_type = 'account' AND resource_filter_id = ?`,
		"sys-owner", "2026-09-20", "sys-caller", "acct-1",
	).Scan(&userSummaryRequestCount); err != nil {
		t.Fatalf("读取授权日报失败: %v", err)
	}
	if userSummaryRequestCount != 1 {
		t.Fatalf("authorization_user_usage_summary_daily request_count = %d, 期望 1", userSummaryRequestCount)
	}
}

// ensureSuccessCostUsdColumns 对已存在的 stats 投影表补 success_cost_usd 列，
// 与 maintenance EnsureSQLiteStats 的遗留库守卫同款（PRAGMA table_info 判定 +
// ADD COLUMN + rerun-safe 回填：error_count = 0 的行 total 即成功成本）。
func ensureSuccessCostUsdColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{
		"usage_stats_totals",
		"usage_stats_minute",
		"usage_stats_hourly",
		"usage_stats_daily",
		"usage_stats_weekly",
		"usage_stats_monthly",
	} {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatalf("读取 %s 列失败: %v", table, err)
		}
		columnExists := false
		for rows.Next() {
			var cid, notNull, pk int
			var name, declaredType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				t.Fatalf("扫描 %s 列失败: %v", table, err)
			}
			if name == "success_cost_usd" {
				columnExists = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("遍历 %s 列失败: %v", table, err)
		}
		rows.Close()
		if columnExists {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN success_cost_usd REAL NOT NULL DEFAULT 0"); err != nil {
			t.Fatalf("补 %s.success_cost_usd 失败: %v", table, err)
		}
	}
}

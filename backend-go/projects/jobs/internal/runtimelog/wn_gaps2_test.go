package runtimelog

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wn_gaps2_test.go 补齐最后一批可达分支：文件截断重置、清理配额与候选批、
// 续约成功/panic、SQLite drop/trigger 注入、PG 各语句失败点。

type panickingRenewStore struct{ Store }

func (store *panickingRenewStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	panic("renewal fixture panic")
}

func TestPostgresFakeCursorNormalizeRejectsInvalidTimestamps(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	store := openFakePostgresStore(t, server)
	lease := OwnerLease{OwnerID: "o", FenceToken: 7}
	if err := store.CopyCursor(context.Background(), lease, Cursor{LogFile: "x", CreatedAt: "bad-time"}); err == nil {
		t.Fatal("非法 createdAt 必须拒绝")
	}
	if err := store.CopyCursor(context.Background(), lease, Cursor{LogFile: "x", LastReadAt: "bad-time"}); err == nil {
		t.Fatal("非法 lastReadAt 必须拒绝")
	}
}

// TestPostgresFakeCommitSkipsFacetsWhenAllExpired 全部记录过期时不得写 facet。
func TestPostgresFakeCommitSkipsFacetsWhenAllExpired(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	registerCommitSuccessPath(t, server)
	server.handleFunc(pgFakeCursorUpsertSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "INSERT 0 1", nil
	})
	store := openFakePostgresStore(t, server)
	lease := OwnerLease{OwnerID: "o", FenceToken: 7}
	err := store.Commit(context.Background(), lease, []Record{{ID: "old", Time: "2020-01-01T00:00:00.000Z", Level: "info", CreatedAt: "2020-01-01T00:00:00.000Z"}},
		Cursor{LogFile: "x"}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range server.executedLog() {
		if strings.Contains(normalizePGSQL(entry.sql), "INSERT INTO juhe_dataset.runtime_log_facet_summary") {
			t.Fatal("全部过期时不得写入 facet 汇总")
		}
	}
}

// TestPostgresFakeFacetLevelEventFailures 注入 level/event facet 写入失败。
func TestPostgresFakeFacetLevelEventFailures(t *testing.T) {
	t.Run("level facet", func(t *testing.T) {
		server := newPGFakeServer(t)
		registerPostgresCatalog(t, server)
		registerPostgresLeaseHandlers(t, server)
		registerCommitSuccessPath(t, server)
		server.handleErrorPrefix("INSERT INTO juhe_dataset.runtime_log_level_facets", "42501", "permission denied for level facets")
		store := openFakePostgresStore(t, server)
		err := store.Commit(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7},
			[]Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", Level: "info", CreatedAt: "2026-08-08T00:00:00.000Z"}},
			Cursor{LogFile: "x"}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
		if err == nil {
			t.Fatal("level facet 写入失败必须暴露")
		}
	})
	t.Run("event facet", func(t *testing.T) {
		server := newPGFakeServer(t)
		registerPostgresCatalog(t, server)
		registerPostgresLeaseHandlers(t, server)
		registerCommitSuccessPath(t, server)
		server.handleErrorPrefix("INSERT INTO juhe_dataset.runtime_log_event_facets", "42501", "permission denied for event facets")
		store := openFakePostgresStore(t, server)
		err := store.Commit(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7},
			[]Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", Level: "info", CreatedAt: "2026-08-08T00:00:00.000Z"}},
			Cursor{LogFile: "x"}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
		if err == nil {
			t.Fatal("event facet 写入失败必须暴露")
		}
	})
}

// TestPostgresFakeDecrementFacetFailures 注入 facet 扣减各语句失败。
// wn_pgfake 的 resolve 精确匹配优先于前缀匹配，而 registerCleanupSuccessPath
// 已为下列语句注册精确成功脚本，因此必须用整句精确注入才能覆盖成功脚本。
func TestPostgresFakeDecrementFacetFailures(t *testing.T) {
	cases := []struct {
		name    string
		failSQL string
	}{
		{name: "summary update", failSQL: "UPDATE juhe_dataset.runtime_log_facet_summary SET total_count = GREATEST(0, total_count - $1), earliest_time = $2, latest_time = $3, updated_at = $4 WHERE bucket_key = $5"},
		{name: "summary delete", failSQL: "DELETE FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1 AND total_count <= 0"},
		{name: "level update", failSQL: "UPDATE juhe_dataset.runtime_log_level_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND level = $4"},
		{name: "level delete", failSQL: "DELETE FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = $1 AND count <= 0"},
		{name: "event update", failSQL: "UPDATE juhe_dataset.runtime_log_event_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND event = $4"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := newPGFakeServer(t)
			registerPostgresCatalog(t, server)
			registerPostgresLeaseHandlers(t, server)
			registerCleanupSuccessPath(t, server)
			server.handleError(test.failSQL, "42501", "wn_pgfake 注入失败: "+test.name)
			store := openFakePostgresStore(t, server)
			if _, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7}, time.Now(), 10, 1); err == nil {
				t.Fatalf("%s 失败必须暴露", test.name)
			}
		})
	}
}

// TestPostgresFakeDecrementSkipsRecordsBelowBaseline 覆盖 PG 扣减基线之下的记录分支。
func TestPostgresFakeDecrementSkipsRecordsBelowBaseline(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	registerCleanupSuccessPath(t, server)
	baselineColumns := []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}
	server.handleFunc("SELECT COALESCE((SELECT earliest_time::text FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", baselineColumns, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{"2027-01-01T00:00:00.000Z"}}, "SELECT 1", nil
	})
	store := openFakePostgresStore(t, server)
	result, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7}, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeLogs != 1 {
		t.Fatalf("Cleanup 统计不正确: %#v", result)
	}
	for _, entry := range server.executedLog() {
		if strings.HasPrefix(normalizePGSQL(entry.sql), "UPDATE juhe_dataset.runtime_log_facet_summary") {
			t.Fatal("低于计数基线的记录不得扣减 facet 汇总")
		}
	}
}

// TestPostgresFakeSecondBeginFailure 注入第二次 BEGIN 失败，覆盖 cursor 批开始失败分支。
func TestPostgresFakeSecondBeginFailure(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	registerCleanupSuccessPath(t, server)
	var begins atomic.Int64
	server.handleFunc("begin", nil, func([]string) ([][]any, string, *pgFakeError) {
		if begins.Add(1) >= 2 {
			return nil, "", &pgFakeError{code: "57P01", message: "crash during second begin"}
		}
		return nil, "BEGIN", nil
	})
	store := openFakePostgresStore(t, server)
	if _, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7}, time.Now(), 10, 2); err == nil || !strings.Contains(err.Error(), "second begin") {
		t.Fatalf("cursor 批开始失败必须暴露: %v", err)
	}
}

// TestPostgresFakeEnsureSchemaReportsCheckFailureBeforeAndAfterDDL 覆盖 EnsureSchema
// 前置校验失败与 DDL 后校验失败分支。
func TestPostgresFakeEnsureSchemaReportsCheckFailureBeforeAndAfterDDL(t *testing.T) {
	t.Run("check failure before DDL", func(t *testing.T) {
		server := newPGFakeServer(t)
		server.handleError("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", "42501", "permission denied for tables")
		store := openFakePostgresStore(t, server)
		if err := EnsureSchema(context.Background(), store); err == nil || !strings.Contains(err.Error(), "permission denied for tables") {
			t.Fatalf("前置校验失败必须暴露: %v", err)
		}
	})
	t.Run("check failure after DDL", func(t *testing.T) {
		server := newPGFakeServer(t)
		var tablesQueryCount atomic.Int64
		server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeTableNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
			if tablesQueryCount.Add(1) == 1 {
				return nil, "SELECT 0", nil
			}
			return [][]any{{args[0]}}, "SELECT 1", nil
		})
		// 第二次 CheckSchema 走到列校验时注入失败。
		var columnsQueryCount atomic.Int64
		server.handleFunc("SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeColumnTypeColumns(), func(args []string) ([][]any, string, *pgFakeError) {
			if columnsQueryCount.Add(1) >= 1 {
				return nil, "", &pgFakeError{code: "42501", message: "permission denied for columns after DDL"}
			}
			return nil, "SELECT 0", nil
		})
		store := openFakePostgresStore(t, server)
		if err := EnsureSchema(context.Background(), store); err == nil || !strings.Contains(err.Error(), "初始化 PostgreSQL 运行日志 schema 后校验失败") {
			t.Fatalf("DDL 后校验失败必须暴露: %v", err)
		}
	})
}

// TestPostgresFakeCheckSchemaIndexQueryFailure 覆盖索引元数据非缺行错误分支。
func TestPostgresFakeCheckSchemaIndexQueryFailure(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	server.handleError("SELECT indexname FROM pg_indexes WHERE schemaname = 'juhe_dataset' AND indexname = $1", "42501", "permission denied for pg_indexes")
	store := openFakePostgresStore(t, server)
	err := store.CheckSchema(context.Background())
	if err == nil || strings.Contains(err.Error(), "schema 不完整") && !strings.Contains(err.Error(), "permission denied for pg_indexes") {
		t.Fatalf("索引元数据错误必须保留原始错误: %v", err)
	}
}

// TestLoadConfigAcceptsExplicitValues 覆盖显式合法值解析分支。
func TestLoadConfigAcceptsExplicitValues(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":        "inst-explicit",
		"JUHE_AI_RUNTIME_LOG_STORE":              "postgres",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL":       "postgres://jobs:secret@127.0.0.1:5432/db?sslmode=disable",
		"JUHE_AI_RUNTIME_LOG_OWNER_LEASE":        "45s",
		"JUHE_AI_RUNTIME_LOG_ONCE":               "false",
		"JUHE_AI_RUNTIME_LOG_POLL_INTERVAL":      "2s",
		"JUHE_AI_RUNTIME_LOG_RETENTION_DAYS":     "30",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_MAX_CONNS": "8",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS": "0",
		"JUHE_AI_LOG_DIR":                        "logs",
	}
	config, err := LoadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("显式合法配置必须通过: %v", err)
	}
	if config.OwnerLease != 45*time.Second || config.PollInterval != 2*time.Second || config.RetentionDays != 30 || config.PostgresMaxConns != 8 {
		t.Fatalf("显式配置未生效: %#v", config)
	}
}

// TestNormalizeNodeTimestampRejectsBlank 覆盖空白时间拒绝分支。
func TestNormalizeNodeTimestampRejectsBlank(t *testing.T) {
	if _, err := normalizeNodeTimestamp("   "); err == nil {
		t.Fatal("空白时间必须拒绝")
	}
}

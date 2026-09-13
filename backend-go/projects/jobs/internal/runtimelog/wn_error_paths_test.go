package runtimelog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wn_error_paths_test.go 用脚本化错误注入覆盖 store/migration/config 的
// 失败分支：数据库错误必须原样返回且不留部分写入。

// TestPostgresFakeBeginFailurePropagatesToAllTxMethods 注入 SET LOCAL 失败，
// 让所有依赖 beginPostgresTx 的方法在同一失败点上暴露错误。
func TestPostgresFakeBeginFailurePropagatesToAllTxMethods(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	server.handleError("SET LOCAL statement_timeout = '5s'", "42501", "permission denied for guard")
	store := openFakePostgresStore(t, server)
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "owner-a", FenceToken: 7}
	want := "设置 F1 PostgreSQL 事务时限失败"

	if _, _, err := store.AcquireOwnerLease(ctx, "owner", time.Minute); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("AcquireOwnerLease 必须暴露事务时限失败: %v", err)
	}
	if _, err := store.RenewOwnerLease(ctx, lease, time.Minute); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("RenewOwnerLease 必须暴露事务时限失败: %v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ReleaseOwnerLease 必须暴露事务时限失败: %v", err)
	}
	if err := store.VerifyOwnerLease(ctx, lease); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("VerifyOwnerLease 必须暴露事务时限失败: %v", err)
	}
	if err := store.WithOwnerLeaseFence(ctx, lease, func() error { return nil }); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("WithOwnerLeaseFence 必须暴露事务时限失败: %v", err)
	}
	if err := store.ReplaceCursor(ctx, lease, nil, Cursor{LogFile: "x"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ReplaceCursor 必须暴露事务时限失败: %v", err)
	}
	if err := store.CopyCursor(ctx, lease, Cursor{LogFile: "x"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("CopyCursor 必须暴露事务时限失败: %v", err)
	}
	if err := store.Commit(ctx, lease, nil, Cursor{LogFile: "x"}, time.Now()); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Commit 必须暴露事务时限失败: %v", err)
	}
	if _, err := store.Cleanup(ctx, lease, time.Now(), 1, 1); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Cleanup 必须暴露事务时限失败: %v", err)
	}
}

// TestPostgresFakeVerifyLeaseFailureBlocksWrites 注入 lease 校验语句错误，
// 写路径必须原样失败。
func TestPostgresFakeVerifyLeaseFailureBlocksWrites(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	server.handleFunc(pgFakeLeaseVerifySQL, []pgFakeColumn{{name: "fence_token", oid: pgFakeOIDInt8}}, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "", &pgFakeError{code: "42501", message: "permission denied for lease"}
	})
	store := openFakePostgresStore(t, server)
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "owner-a", FenceToken: 7}

	if err := store.VerifyOwnerLease(ctx, lease); err == nil || strings.Contains(err.Error(), "已失效") {
		t.Fatalf("校验错误必须保留原始错误: %v", err)
	}
	if err := store.ReplaceCursor(ctx, lease, nil, Cursor{LogFile: "x"}); err == nil {
		t.Fatal("ReplaceCursor 必须暴露 lease 校验错误")
	}
	if err := store.CopyCursor(ctx, lease, Cursor{LogFile: "x"}); err == nil {
		t.Fatal("CopyCursor 必须暴露 lease 校验错误")
	}
	if err := store.Commit(ctx, lease, nil, Cursor{LogFile: "x"}, time.Now()); err == nil {
		t.Fatal("Commit 必须暴露 lease 校验错误")
	}
	if err := store.WithOwnerLeaseFence(ctx, lease, func() error { return nil }); err == nil {
		t.Fatal("WithOwnerLeaseFence 必须暴露 lease 校验错误")
	}
}

// TestPostgresFakeStatementLevelFailures 逐条注入 DML/查询失败，确保每个
// 失败点都把数据库错误暴露给调用方。
func TestPostgresFakeStatementLevelFailures(t *testing.T) {
	cases := []struct {
		name          string
		failSQL       string
		failSQLPrefix string
		skipCatalog   bool
		execute       func(t *testing.T, store *postgresStore, server *pgFakeServer)
	}{
		{name: "acquire query", failSQL: pgFakeLeaseAcquireSQL, execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			if _, _, err := store.AcquireOwnerLease(context.Background(), "o", time.Minute); err == nil {
				t.Fatal("获取 lease 查询失败必须暴露")
			}
		}},
		{name: "renew exec", failSQL: pgFakeLeaseRenewSQL, execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			if _, err := store.RenewOwnerLease(context.Background(), lease, time.Minute); err == nil {
				t.Fatal("续约失败必须暴露")
			}
		}},
		{name: "release exec", failSQL: pgFakeLeaseReleaseSQL, execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			if err := store.ReleaseOwnerLease(context.Background(), lease); err == nil {
				t.Fatal("释放失败必须暴露")
			}
		}},
		{name: "cursor upsert", failSQL: pgFakeCursorUpsertSQL, execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			if err := store.CopyCursor(context.Background(), lease, Cursor{LogFile: "x"}); err == nil {
				t.Fatal("cursor upsert 失败必须暴露")
			}
		}},
		{name: "cursor delete", failSQL: "DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1", execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			if err := store.ReplaceCursor(context.Background(), lease, nil, Cursor{LogFile: "x"}); err == nil {
				t.Fatal("cursor 删除失败必须暴露")
			}
		}},
		{name: "facet summary insert", failSQLPrefix: "INSERT INTO juhe_dataset.runtime_log_facet_summary", execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			registerCommitSuccessPath(t, server)
			if err := store.Commit(context.Background(), lease, []Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", Level: "info", Event: "e", CreatedAt: "2026-08-08T00:00:00.000Z"}},
				Cursor{LogFile: "x"}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)); err == nil {
				t.Fatal("facet 汇总写入失败必须暴露")
			}
		}},
		{name: "cleanup select unregistered", failSQL: "", execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			if _, err := store.Cleanup(context.Background(), lease, time.Now(), 1, 1); err == nil {
				t.Fatal("过期记录查询失败必须暴露")
			}
		}},
		{name: "cleanup cursor delete", failSQLPrefix: "DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE ctid IN", execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			registerCleanupSuccessPath(t, server)
			if _, err := store.Cleanup(context.Background(), lease, time.Now(), 10, 1); err == nil {
				t.Fatal("cursor 清理失败必须暴露")
			}
		}},
		{name: "decrement earliest baseline", failSQLPrefix: "SELECT COALESCE((SELECT earliest_time::text FROM juhe_dataset.runtime_log_facet_summary", execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			registerCleanupSuccessPath(t, server)
			if _, err := store.Cleanup(context.Background(), lease, time.Now(), 10, 1); err == nil {
				t.Fatal("facet 基线读取失败必须暴露")
			}
		}},
		{name: "runtime retention days unregistered", failSQL: "", execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			if _, err := store.RuntimeRetentionDays(context.Background(), 7); err == nil {
				t.Fatal("保留期读取失败必须暴露")
			}
		}},
		{name: "metadata columns unregistered", failSQL: "", skipCatalog: true, execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			if err := store.CheckSchema(context.Background()); err == nil {
				t.Fatal("列元数据读取失败必须暴露")
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := newPGFakeServer(t)
			if !test.skipCatalog {
				registerPostgresCatalog(t, server)
			}
			registerPostgresLeaseHandlers(t, server)
			if test.failSQLPrefix != "" {
				server.handleErrorPrefix(test.failSQLPrefix, "42501", "wn_pgfake 注入失败: "+test.name)
			} else if test.failSQL != "" {
				server.handleError(test.failSQL, "42501", "wn_pgfake 注入失败: "+test.name)
			}
			store := openFakePostgresStore(t, server)
			test.execute(t, store, server)
		})
	}
}

// registerCommitSuccessPath 注册 Commit 成功路径所需的其余脚本（在注入用例中
// 单条失败之外的语句都要成功，才能推进到目标失败点）。
func registerCommitSuccessPath(t *testing.T, server *pgFakeServer) {
	t.Helper()
	server.handlePrefixFunc("INSERT INTO juhe_dataset.runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at) VALUES",
		[]pgFakeColumn{{name: "time", oid: pgFakeOIDText}, {name: "level", oid: pgFakeOIDText}, {name: "event", oid: pgFakeOIDText}},
		func(sql string, args []string) ([][]any, string, *pgFakeError) {
			records := len(args) / 12
			rows := make([][]any, 0, records)
			for index := 0; index < records; index++ {
				base := index * 12
				rows = append(rows, []any{args[base+4], args[base+5], args[base+7]})
			}
			return rows, "INSERT 1", nil
		})
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key) DO UPDATE SET total_count = juhe_dataset.runtime_log_facet_summary.total_count + excluded.total_count, earliest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.earliest_time IS NULL OR excluded.earliest_time < juhe_dataset.runtime_log_facet_summary.earliest_time THEN excluded.earliest_time ELSE juhe_dataset.runtime_log_facet_summary.earliest_time END, latest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_facet_summary.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_facet_summary.latest_time END, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES ($1, $2, $3, $4) ON CONFLICT(bucket_key, level) DO UPDATE SET count = juhe_dataset.runtime_log_level_facets.count + excluded.count, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key, event) DO UPDATE SET count = juhe_dataset.runtime_log_event_facets.count + excluded.count, latest_time = CASE WHEN juhe_dataset.runtime_log_event_facets.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_event_facets.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_event_facets.latest_time END, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
}

// registerCleanupSuccessPath 注册 Cleanup 成功路径所需的其余脚本。
func registerCleanupSuccessPath(t *testing.T, server *pgFakeServer) {
	t.Helper()
	server.handleFunc(pgFakeCleanupSelectSQL, []pgFakeColumn{
		{name: "id", oid: pgFakeOIDText},
		{name: "time", oid: pgFakeOIDText},
		{name: "level", oid: pgFakeOIDText},
		{name: "event", oid: pgFakeOIDText},
	}, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{"expired-1", "2026-08-01T00:00:00.000Z", "info", "expired-event"}}, "SELECT 1", nil
	})
	server.handlePrefixFunc("DELETE FROM juhe_dataset.runtime_logs WHERE id IN (", nil, func(string, []string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 1", nil
	})
	server.handleFunc("SELECT COALESCE((SELECT earliest_time::text FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{""}}, "SELECT 1", nil
	})
	coalesceColumns := []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}
	server.handleFunc("SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time ASC, id ASC LIMIT 1), '')", coalesceColumns, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{""}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time DESC, id DESC LIMIT 1), '')", coalesceColumns, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{""}}, "SELECT 1", nil
	})
	server.handleFunc("UPDATE juhe_dataset.runtime_log_facet_summary SET total_count = GREATEST(0, total_count - $1), earliest_time = $2, latest_time = $3, updated_at = $4 WHERE bucket_key = $5", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "UPDATE 1", nil
	})
	server.handleFunc("DELETE FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1 AND total_count <= 0", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 0", nil
	})
	server.handleFunc("UPDATE juhe_dataset.runtime_log_level_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND level = $4", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "UPDATE 1", nil
	})
	server.handleFunc("DELETE FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = $1 AND count <= 0", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 0", nil
	})
	server.handleFunc("UPDATE juhe_dataset.runtime_log_event_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND event = $4", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "UPDATE 1", nil
	})
	server.handleFunc("DELETE FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = $1 AND count <= 0", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 0", nil
	})
	server.handleFunc("DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE ctid IN (SELECT ctid FROM juhe_dataset.runtime_log_file_cursors WHERE updated_at < $1 AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, ctid ASC LIMIT $2)", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 0", nil
	})
}

// TestPostgresFakeInsertRecordsFailure 注入批量 INSERT 失败（动态前缀脚本）。
func TestPostgresFakeInsertRecordsFailure(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	server.handlePrefixFunc("INSERT INTO juhe_dataset.runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at) VALUES",
		nil, func(string, []string) ([][]any, string, *pgFakeError) {
			return nil, "", &pgFakeError{code: "42501", message: "permission denied for runtime_logs"}
		})
	store := openFakePostgresStore(t, server)
	lease := OwnerLease{OwnerID: "o", FenceToken: 7}
	err := store.Commit(context.Background(), lease, []Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", CreatedAt: "2026-08-08T00:00:00.000Z"}}, Cursor{LogFile: "x"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "permission denied for runtime_logs") {
		t.Fatalf("批量写入失败必须暴露: %v", err)
	}
}

// TestPostgresFakeCleanupSecondBatchEmpty 覆盖第二批无数据的提前结束分支。
func TestPostgresFakeCleanupSecondBatchEmpty(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	var selects atomic.Int64
	server.handleFunc(pgFakeCleanupSelectSQL, []pgFakeColumn{
		{name: "id", oid: pgFakeOIDText},
		{name: "time", oid: pgFakeOIDText},
		{name: "level", oid: pgFakeOIDText},
		{name: "event", oid: pgFakeOIDText},
	}, func([]string) ([][]any, string, *pgFakeError) {
		if selects.Add(1) == 1 {
			return [][]any{{"expired-1", "2026-08-01T00:00:00.000Z", "info", "expired-event"}}, "SELECT 1", nil
		}
		return nil, "SELECT 0", nil
	})
	server.handlePrefixFunc("DELETE FROM juhe_dataset.runtime_logs WHERE id IN (", nil, func(string, []string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 1", nil
	})
	registerCleanupSuccessPath(t, server)
	// registerCleanupSuccessPath 重复注册了 cleanup select，这里重新覆盖为计数脚本。
	server.handleFunc(pgFakeCleanupSelectSQL, []pgFakeColumn{
		{name: "id", oid: pgFakeOIDText},
		{name: "time", oid: pgFakeOIDText},
		{name: "level", oid: pgFakeOIDText},
		{name: "event", oid: pgFakeOIDText},
	}, func([]string) ([][]any, string, *pgFakeError) {
		if selects.Add(1) == 1 {
			return [][]any{{"expired-1", "2026-08-01T00:00:00.000Z", "info", "expired-event"}}, "SELECT 1", nil
		}
		return nil, "SELECT 0", nil
	})
	// 让第二批查询直接空返回：游标清理脚本固定 DELETE 1 后因 count<batchLimit 结束。
	server.handleFunc("DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE ctid IN (SELECT ctid FROM juhe_dataset.runtime_log_file_cursors WHERE updated_at < $1 AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, ctid ASC LIMIT $2)", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 0", nil
	})
	store := openFakePostgresStore(t, server)
	result, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7}, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeLogs != 1 || result.RuntimeLogCursors != 0 {
		t.Fatalf("Cleanup 统计不正确: %#v", result)
	}
}

// TestPostgresFakeCursorQueryFailure 注入 cursor 查询失败。
func TestPostgresFakeCursorQueryFailure(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	server.handleFunc(pgFakeCursorSelectWhereFile, pgFakeCursorColumns(), func([]string) ([][]any, string, *pgFakeError) {
		return nil, "", &pgFakeError{code: "42501", message: "permission denied for cursors"}
	})
	store := openFakePostgresStore(t, server)
	if _, err := store.FindCursor(context.Background(), "x"); err == nil {
		t.Fatal("cursor 查询失败必须暴露")
	}
}

// TestOpenStoreSQLiteRejectsMemoryAndDirectory 覆盖 WAL 与文件打开失败分支。
func TestOpenStoreSQLiteRejectsMemoryAndDirectory(t *testing.T) {
	// 目录路径无法作为 SQLite 文件打开。
	if _, err := OpenStore(context.Background(), Config{Mode: ModeSQLite, RuntimeLogDatabasePath: t.TempDir(), BusinessPath: t.TempDir()}); err == nil {
		t.Fatal("目录路径必须拒绝作为运行日志数据库")
	}
	// 内存库不支持 WAL journal_mode，必须 fail-closed。
	if _, err := OpenStore(context.Background(), Config{Mode: ModeSQLite, RuntimeLogDatabasePath: ":memory:", BusinessPath: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "WAL journal_mode") {
		t.Fatalf("不支持 WAL 的存储必须拒绝启动: %v", err)
	}
}

// TestOpenStorePostgresRejectsInvalidMaxConns 覆盖连接池配置非法分支。
func TestOpenStorePostgresRejectsInvalidMaxConns(t *testing.T) {
	server := newPGFakeServer(t)
	_, err := OpenStore(context.Background(), Config{Mode: ModePostgres, PostgresURL: server.URL(), PostgresMaxConns: -1})
	if err == nil {
		t.Fatal("非法连接数必须拒绝启动")
	}
}

// TestSQLiteCursorNormalizeRejectsInvalidTimestamps 覆盖 cursor 归一化失败分支。
func TestSQLiteCursorNormalizeRejectsInvalidTimestamps(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	lease := testOwnerLease(t, ctx)
	if err := store.CopyCursor(ctx, lease, Cursor{LogFile: "x", CreatedAt: "bad-time"}); err == nil {
		t.Fatal("非法 createdAt 必须拒绝")
	}
	if err := store.CopyCursor(ctx, lease, Cursor{LogFile: "x", LastReadAt: "bad-time"}); err == nil {
		t.Fatal("非法 lastReadAt 必须拒绝")
	}
	if err := store.ReplaceCursor(ctx, lease, nil, Cursor{LogFile: "x", CreatedAt: "bad-time"}); err == nil {
		t.Fatal("ReplaceCursor 非法 createdAt 必须拒绝")
	}
}

// TestSQLiteCommitFacetFailure 注入 facet 写入失败，Commit 必须整体失败。
func TestSQLiteCommitFacetFailure(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	lease := testOwnerLease(t, ctx)
	if _, err := store.db.Exec("CREATE TRIGGER reject_facet_summary BEFORE INSERT ON runtime_log_facet_summary BEGIN SELECT RAISE(ABORT, 'forced facet failure'); END"); err != nil {
		t.Fatal(err)
	}
	cursor := Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log"), FileIdentity: "facet:1:1"}
	err := store.Commit(ctx, lease, []Record{{ID: "facet-record", Time: "2026-08-08T00:00:00.000Z", Level: "info", Event: "e", RawJSON: "{}", CreatedAt: "2026-08-08T00:00:00.000Z"}}, cursor, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("facet 写入失败必须让 Commit 整体失败")
	}
	if _, err := store.db.Exec("DROP TRIGGER reject_facet_summary"); err != nil {
		t.Fatal(err)
	}
	// 失败后无部分写入：记录与游标都不存在。
	assertRuntimeLogCount(t, store, 0)
	if got, err := store.FindCursor(context.Background(), cursor.LogFile); err != nil || got != nil {
		t.Fatalf("失败事务不得残留 cursor: cursor=%v err=%v", got, err)
	}
}

// TestSQLiteCheckSchemaRejectsMissingTable 覆盖表缺失分支。
func TestSQLiteCheckSchemaRejectsMissingTable(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if _, err := store.db.Exec("DROP TABLE runtime_log_event_facets"); err != nil {
		t.Fatal(err)
	}
	err := store.CheckSchema(context.Background())
	if err == nil || !strings.Contains(err.Error(), "runtime_log_event_facets") {
		t.Fatalf("缺少表必须阻止启动: %v", err)
	}
}

// TestSQLiteWithOwnerLeaseFencePropagatesCallbackError 覆盖 SQLite fence 回调失败分支。
func TestSQLiteWithOwnerLeaseFencePropagatesCallbackError(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	callbackErr := errors.New("sqlite fence callback fixture failure")
	if err := store.WithOwnerLeaseFence(context.Background(), lease, func() error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("callback 失败必须原样返回: %v", err)
	}
}

// TestMigrateLegacySQLiteRejectsCorruptDataset 覆盖 ATTACH 失败分支。
func TestMigrateLegacySQLiteRejectsCorruptDataset(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	if err := os.WriteFile(config.DatasetPath, []byte("this is not a sqlite database at all —— just garbage bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := MigrateLegacySQLite(testOwnerContext(t, store), config, store)
	if err == nil || !strings.Contains(err.Error(), "附加旧运行日志数据库失败") {
		t.Fatalf("损坏数据源必须拒绝迁移: %v", err)
	}
	detachLegacy(t, store)
}

// TestMigrateLegacySQLiteRejectsStaleLease 覆盖迁移前 lease 校验失败分支。
func TestMigrateLegacySQLiteRejectsStaleLease(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	legacy, err := sql.Open("sqlite", config.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	// 未持有 lease 的上下文已在参数校验后被 ownerLeaseFromContext 拒绝；
	// 这里用低 fence token 触发 verifySQLiteOwnerLease 失败。
	staleCtx := withOwnerLease(context.Background(), OwnerLease{OwnerID: "ghost", FenceToken: 99})
	err = MigrateLegacySQLite(staleCtx, config, store)
	if err == nil || !errors.Is(err, ErrOwnerLeaseFenced) {
		t.Fatalf("旧 token 迁移必须被 fence 拒绝: %v", err)
	}
}

// TestMigrateLegacySQLiteRejectsCopyFailure 覆盖复制阶段失败分支。
func TestMigrateLegacySQLiteRejectsCopyFailure(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	detachLegacy(t, store)
	// openTestSQLiteStore 已预建 dataset，缺列 schema 使用全新文件。
	config.DatasetPath = filepath.Join(t.TempDir(), "partial-legacy.sqlite")
	dataset, err := sql.Open("sqlite", config.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	// 复制语句按列名 SELECT：runtime_logs 缺少非瞬时列 trace_id 时，
	// 瞬时列校验（只检查 time/created_at）先通过，复制阶段才会失败。
	if _, err := dataset.Exec(`CREATE TABLE runtime_logs (id TEXT, log_file TEXT, log_offset INTEGER, line_number INTEGER, time TEXT, level TEXT, raw_json TEXT, created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`CREATE TABLE runtime_log_file_cursors (log_file TEXT, file_identity TEXT, cursor_offset INTEGER, line_number INTEGER, file_size INTEGER, truncation_generation INTEGER, file_mtime_ms INTEGER, last_read_at TEXT, last_error_message TEXT, created_at TEXT, updated_at TEXT); CREATE TABLE runtime_log_facet_summary (bucket_key TEXT, total_count INTEGER, earliest_time TEXT, latest_time TEXT, updated_at TEXT); CREATE TABLE runtime_log_level_facets (bucket_key TEXT, level TEXT, count INTEGER, updated_at TEXT); CREATE TABLE runtime_log_event_facets (bucket_key TEXT, event TEXT, count INTEGER, latest_time TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, raw_json, created_at) VALUES ('x', 'juhe-ai.log', 0, 0, '2026-08-09T00:00:00.000Z', 'info', '{}', '2026-08-09T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := dataset.Close(); err != nil {
		t.Fatal(err)
	}
	err = MigrateLegacySQLite(testOwnerContext(t, store), config, store)
	if err == nil || !strings.Contains(err.Error(), "复制旧运行日志 SQLite 数据失败") {
		t.Fatalf("复制失败必须拒绝迁移: %v", err)
	}
	detachLegacy(t, store)
}

// TestMigrateLegacySQLiteRejectsInvalidInstant 覆盖绝对时间非法分支。
func TestMigrateLegacySQLiteRejectsInvalidInstant(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	legacy, err := sql.Open("sqlite", config.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, raw_json, created_at) VALUES ('bad-time', 'juhe-ai.log', 0, 0, 'not-a-date', 'info', '{}', '2026-08-09T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	err = MigrateLegacySQLite(testOwnerContext(t, store), config, store)
	if err == nil || !strings.Contains(err.Error(), "非法") {
		t.Fatalf("非法绝对时间必须拒绝迁移: %v", err)
	}
	detachLegacy(t, store)
}

// TestSameSQLitePathRejectsDanglingAlias 覆盖别名解析失败分支。
func TestSameSQLitePathRejectsDanglingAlias(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left.sqlite")
	if err := os.WriteFile(left, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(root, "dangling.sqlite")
	target := filepath.Join(root, "missing-target.sqlite")
	if err := os.Symlink(target, dangling); err != nil {
		t.Skipf("当前环境不能创建悬空 symlink fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(target, []byte("cleanup"), 0o600)
		_ = os.Remove(dangling)
	})
	if _, err := sameSQLitePath(left, dangling); err == nil {
		t.Fatal("悬空别名必须报错")
	}
}

// TestSQLitePathWithinRejectsCrossVolume 覆盖跨盘符路径比较失败分支。
func TestSQLitePathWithinRejectsCrossVolume(t *testing.T) {
	if _, err := sqlitePathWithin(`C:\roots\logs`, `D:\other\place.sqlite`); err == nil {
		t.Fatal("跨盘符路径必须报错")
	}
}

// TestDanglingSQLiteSymlinkHandlesReadFailure 覆盖目录枚举失败分支。
func TestDanglingSQLiteSymlinkHandlesReadFailure(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if danglingSQLiteSymlink(filepath.Join(filePath, "child.sqlite")) {
		t.Fatal("枚举失败的路径不得判定为 symlink")
	}
}

// TestLoadConfigRejectsEachInvalidEnv 逐项覆盖配置校验失败分支。
func TestLoadConfigRejectsEachInvalidEnv(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":        "inst-1",
		"JUHE_AI_RUNTIME_LOG_STORE":              "sqlite",
		"JUHE_AI_DATASET_DATABASE_PATH":          "dataset.sqlite",
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":      "runtime-log.sqlite",
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    "table-monitor.sqlite",
		"JUHE_AI_DATABASE_PATH":                  "business.sqlite",
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    "usage-catalog.sqlite",
		"JUHE_AI_STATS_DATABASE_PATH":            "stats.sqlite",
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": "codex-shards",
		"JUHE_AI_LOG_DIR":                        "logs",
	}
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "store mode", key: "JUHE_AI_RUNTIME_LOG_STORE", value: "bogus"},
		{name: "retention interval", key: "JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL", value: "0s"},
		{name: "poll interval", key: "JUHE_AI_RUNTIME_LOG_POLL_INTERVAL", value: "0s"},
		{name: "log retention days", key: "JUHE_AI_LOG_RETENTION_DAYS", value: "0"},
		{name: "log max files", key: "JUHE_AI_LOG_MAX_FILES", value: "501"},
		{name: "batch size", key: "JUHE_AI_RUNTIME_LOG_BATCH_SIZE", value: "0"},
		{name: "postgres max conns", key: "JUHE_AI_RUNTIME_LOG_POSTGRES_MAX_CONNS", value: "-2"},
		{name: "postgres min conns", key: "JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS", value: "-2"},
		{name: "missing business path", key: "JUHE_AI_DATABASE_PATH", value: ""},
		{name: "missing dataset path", key: "JUHE_AI_DATASET_DATABASE_PATH", value: ""},
		{name: "missing usage catalog path", key: "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", value: ""},
		{name: "missing stats path", key: "JUHE_AI_STATS_DATABASE_PATH", value: ""},
		{name: "missing codex root", key: "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT", value: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{}
			for name, value := range base {
				values[name] = value
			}
			values[test.key] = test.value
			if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("非法 %s 配置必须拒绝启动: %v", test.key, err)
			}
		})
	}

	// 运行日志库放入 Codex shard 根目录必须拒绝。
	withinShard := map[string]string{}
	for name, value := range base {
		withinShard[name] = value
	}
	withinShard["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = filepath.Join(base["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "runtime-log.sqlite")
	if _, err := LoadConfig(func(name string) string { return withinShard[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT") {
		t.Fatalf("运行日志库放入 shard 根目录必须拒绝: %v", err)
	}

	// shard 目录中的 .sqlite3 文件与运行日志库同文件必须拒绝（硬链接别名）。
	root := t.TempDir()
	shardRoot := filepath.Join(root, "codex-shards")
	if err := os.MkdirAll(shardRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(root, "runtime-log.sqlite")
	if err := os.WriteFile(runtimePath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	shardAlias := filepath.Join(shardRoot, "state-001.sqlite3")
	if err := os.Link(runtimePath, shardAlias); err != nil {
		t.Fatalf("创建 shard 硬链接 fixture 失败: %v", err)
	}
	shardConflict := map[string]string{}
	for name, value := range base {
		shardConflict[name] = value
	}
	shardConflict["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = shardRoot
	shardConflict["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = runtimePath
	if _, err := LoadConfig(func(name string) string { return shardConflict[name] }); err == nil || !strings.Contains(err.Error(), "shard") {
		t.Fatalf("与 shard 文件共用必须拒绝: %v", err)
	}
}

// TestParseLogFileNameServerInstance 覆盖 server 实例命名分支。
func TestParseLogFileNameServerInstance(t *testing.T) {
	role, kind, ok := ParseLogFileName("juhe-ai.instance-9.log")
	if !ok || role != "server:instance-9" || kind != LogFileCurrent {
		t.Fatalf("server 实例文件名解析不正确: %q %q %t", role, kind, ok)
	}
}

// TestImportFileSkipsDanglingSymlink 覆盖 Stat 失败跳过分支。
func TestImportFileSkipsDanglingSymlink(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	target := filepath.Join(config.LogDirectory, "missing-target.log")
	link := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("当前环境不能创建悬空 symlink fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(target, []byte("cleanup"), 0o600)
		_ = os.Remove(link)
	})
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatalf("悬空日志 symlink 必须被安静跳过: %v", err)
	}
}

// TestCleanupRotatedFilesNegativeQuotaDeletes 覆盖 maxRotatedFiles<0 分支。
func TestCleanupRotatedFilesNegativeQuotaDeletes(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	currentPath := filepath.Join(config.LogDirectory, "juhe-ai.log")
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, currentPath, logLine("current", "2026-08-08T00:00:00.000Z")+"\n")
	writeTestFile(t, rotatedPath, logLine("rotated", "2026-08-08T00:00:01.000Z")+"\n")
	config.LogMaxFiles = 0
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	deleted, err := indexer.cleanupRotatedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("负配额必须按 0 处理并清理轮转文件，实际删除 %d", deleted)
	}
}

// TestRunWithOwnerLeaseStopsWhenRenewalFails 覆盖续约失败取消运行分支。
// 续约固定失败，结果与调度时机无关：callback 在续约取消后必然收到 Done。
func TestRunWithOwnerLeaseStopsWhenRenewalFails(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	renewing := &failingRenewStore{Store: store}
	config := Config{OwnerID: "renew-owner", OwnerLease: 3 * time.Millisecond}
	started := make(chan struct{})
	run := func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- RunWithOwnerLease(context.Background(), config, renewing, run) }()
	<-started
	select {
	case err := <-errCh:
		// 当前实现：run 被取消后，续约错误会作为最终错误返回（取消不吞掉续约失败）。
		if err == nil || !strings.Contains(err.Error(), "renewal fixture failure") {
			t.Fatalf("续约失败必须作为最终错误返回: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("续约失败必须在短时间内取消运行")
	}
}

type failingRenewStore struct{ Store }

func (store *failingRenewStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	return false, errors.New("renewal fixture failure")
}

// TestRunWithOwnerLeaseCoversZeroInterval 覆盖续约间隔兜底分支。
func TestRunWithOwnerLeaseCoversZeroInterval(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	runErr := errors.New("run fixture failure")
	err := RunWithOwnerLease(context.Background(), Config{OwnerID: "zero-owner"}, store, func(context.Context) error { return runErr })
	if !errors.Is(err, runErr) {
		t.Fatalf("run 错误必须原样返回: %v", err)
	}
}

// TestIndexerRunStopsOnContextCancel 覆盖 Run 的轮询循环与取消退出分支。
// 以第 N 次 RuntimeRetentionDays 作为同步点，断言不依赖具体时序。
func TestIndexerRunStopsOnContextCancel(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), t.Name(), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("测试必须获得 owner lease: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() {
		if releaseErr := store.ReleaseOwnerLease(context.Background(), lease); releaseErr != nil && !errors.Is(releaseErr, ErrOwnerLeaseFenced) {
			t.Errorf("清理测试 owner lease 失败: %v", releaseErr)
		}
	})
	config.PollInterval = time.Millisecond
	config.RetentionInterval = time.Hour
	runs := make(chan struct{}, 32)
	probed := &runSignalStore{Store: store, runs: runs}
	indexer := NewIndexer(config, probed)

	runCtx, cancel := context.WithCancel(withOwnerLease(context.Background(), lease))
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- indexer.Run(runCtx) }()
	// 初次 RunOnce 同步完成后，再等两次轮询，保证循环已经运转。
	<-runs
	<-runs
	<-runs
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后 Run 必须返回 ctx.Err，实际为 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在取消后退出")
	}
}

type runSignalStore struct {
	Store
	runs chan struct{}
}

func (store *runSignalStore) RuntimeRetentionDays(ctx context.Context, fallback int) (int, error) {
	select {
	case store.runs <- struct{}{}:
	default:
	}
	return store.Store.RuntimeRetentionDays(ctx, fallback)
}

// TestPostgresFakeCommitFailurePropagatesToAllTxMethods 注入 COMMIT 失败，
// 覆盖各事务方法提交失败分支。
func TestPostgresFakeCommitFailurePropagatesToAllTxMethods(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	registerCommitSuccessPath(t, server)
	registerCleanupSuccessPath(t, server)
	server.handleFunc(pgFakeCursorUpsertSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "INSERT 0 1", nil
	})
	server.handleFunc("DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 0", nil
	})
	// pgx 5.10 发送小写 commit/rollback。
	server.handleError("commit", "57P01", "crash during commit")
	server.handleError("COMMIT", "57P01", "crash during commit")
	store := openFakePostgresStore(t, server)
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "o", FenceToken: 7}
	want := "crash during commit"

	if err := store.VerifyOwnerLease(ctx, lease); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("VerifyOwnerLease 必须暴露提交失败: %v", err)
	}
	if err := store.WithOwnerLeaseFence(ctx, lease, func() error { return nil }); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("WithOwnerLeaseFence 必须暴露提交失败: %v", err)
	}
	if err := store.ReplaceCursor(ctx, lease, nil, Cursor{LogFile: "x"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ReplaceCursor 必须暴露提交失败: %v", err)
	}
	if err := store.CopyCursor(ctx, lease, Cursor{LogFile: "x"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("CopyCursor 必须暴露提交失败: %v", err)
	}
	if err := store.Commit(ctx, lease, []Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", CreatedAt: "2026-08-08T00:00:00.000Z"}}, Cursor{LogFile: "x"}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Commit 必须暴露提交失败: %v", err)
	}
	if _, err := store.Cleanup(ctx, lease, time.Now(), 10, 1); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Cleanup 必须暴露提交失败: %v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ReleaseOwnerLease 必须暴露提交失败: %v", err)
	}
	if _, err := store.RenewOwnerLease(ctx, lease, time.Minute); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("RenewOwnerLease 必须暴露提交失败: %v", err)
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "o", time.Minute); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("AcquireOwnerLease 必须暴露提交失败: %v", err)
	}
}

// TestPostgresFakeAcquireQueryError 覆盖获取 lease 的查询错误分支（懒读取路径
// 之外，Scan 直接暴露错误）。
func TestPostgresFakeAcquireQueryError(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	server.handleError(pgFakeLeaseAcquireSQL, "42501", "permission denied for lease insert")
	store := openFakePostgresStore(t, server)
	if _, _, err := store.AcquireOwnerLease(context.Background(), "o", time.Minute); err == nil {
		t.Fatal("获取 lease 失败必须暴露")
	}
}

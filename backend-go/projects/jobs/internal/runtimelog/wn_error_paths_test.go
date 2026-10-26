package runtimelog

import (
	"context"
	"errors"
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
	server.handleError("SET LOCAL statement_timeout = '30s'", "42501", "permission denied for guard")
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
// 失败点都把数据库错误暴露给调用方。成功路径脚本必须先注册，错误注入最后
// 注册：wn_pgfake 同键后注册覆盖先注册，精确匹配优先于前缀匹配。
func TestPostgresFakeStatementLevelFailures(t *testing.T) {
	cases := []struct {
		name          string
		failSQL       string
		failSQLPrefix string
		skipCatalog   bool
		register      func(t *testing.T, server *pgFakeServer)
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
		{name: "facet summary insert", failSQLPrefix: "INSERT INTO juhe_dataset.runtime_log_facet_summary", register: registerCommitSuccessPath, execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
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
		{name: "cleanup cursor delete", failSQL: "DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE ctid IN (SELECT ctid FROM juhe_dataset.runtime_log_file_cursors WHERE updated_at < $1 AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, ctid ASC LIMIT $2)", register: registerCleanupSuccessPath, execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
			if _, err := store.Cleanup(context.Background(), lease, time.Now(), 10, 1); err == nil {
				t.Fatal("cursor 清理失败必须暴露")
			}
		}},
		{name: "decrement earliest baseline", failSQL: "SELECT COALESCE((SELECT " + postgresNodeInstantText("earliest_time") + " FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", register: registerCleanupSuccessPath, execute: func(t *testing.T, store *postgresStore, server *pgFakeServer) {
			lease := OwnerLease{OwnerID: "o", FenceToken: 7}
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
			if test.register != nil {
				test.register(t, server)
			}
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
	server.handleFunc("SELECT COALESCE((SELECT "+postgresNodeInstantText("earliest_time")+" FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{""}}, "SELECT 1", nil
	})
	coalesceColumns := []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}
	server.handleFunc("SELECT COALESCE((SELECT "+postgresNodeInstantText("time")+" FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time ASC, id ASC LIMIT 1), '')", coalesceColumns, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{""}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT COALESCE((SELECT "+postgresNodeInstantText("time")+" FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time DESC, id DESC LIMIT 1), '')", coalesceColumns, func([]string) ([][]any, string, *pgFakeError) {
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

func TestOpenStorePostgresRejectsInvalidMaxConns(t *testing.T) {
	server := newPGFakeServer(t)
	_, err := OpenStore(context.Background(), Config{Mode: ModePostgres, PostgresURL: server.URL(), PostgresMaxConns: -1})
	if err == nil {
		t.Fatal("非法连接数必须拒绝启动")
	}
}

func TestLoadConfigRejectsEachInvalidEnv(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":  "inst-1",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL": "postgres://unit-test.invalid/f1",
		"JUHE_AI_LOG_DIR":                  "logs",
	}
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "retention interval", key: "JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL", value: "0s"},
		{name: "poll interval", key: "JUHE_AI_RUNTIME_LOG_POLL_INTERVAL", value: "0s"},
		{name: "log retention days", key: "JUHE_AI_LOG_RETENTION_DAYS", value: "0"},
		{name: "log max files", key: "JUHE_AI_LOG_MAX_FILES", value: "501"},
		{name: "batch size", key: "JUHE_AI_RUNTIME_LOG_BATCH_SIZE", value: "0"},
		{name: "postgres max conns", key: "JUHE_AI_RUNTIME_LOG_POSTGRES_MAX_CONNS", value: "-2"},
		{name: "postgres min conns", key: "JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS", value: "-2"},
		// 缺失 business/dataset/usage-catalog/stats/codex 根的失败臂已删除
		// （2026-09-19 零配置决策：路径类 env 缺省按 DATA_DIR 派生，恒非空，
		// 不再是校验失败分支）。
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
}

// TestParseLogFileNameServerInstance 覆盖 server 实例命名分支。
func TestParseLogFileNameServerInstance(t *testing.T) {
	role, kind, ok := ParseLogFileName("juhe-ai.instance-9.log")
	if !ok || role != "server:instance-9" || kind != LogFileCurrent {
		t.Fatalf("server 实例文件名解析不正确: %q %q %t", role, kind, ok)
	}
}

type failingRenewStore struct{ Store }

func (store *failingRenewStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	return false, errors.New("renewal fixture failure")
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

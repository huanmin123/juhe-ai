package runtimelog

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// w14i_pgfake_arms_test.go 用包内 pgFake 线协议假服务脚本化 ErrorResponse，
// 覆盖 postgresStore 写路径的数据库错误臂（无需真实 PostgreSQL，也不触碰
// 共享 w1cover 库）：
//   - Commit 的 facet 累加错误臂（summary/level/event 三条 INSERT）；
//   - Commit 的记录 INSERT 错误臂；
//   - Cleanup 的 lease 校验/SELECT/DELETE 错误臂与第二阶段 lease 校验臂；
//   - decrementPostgresFacets 的计数查询/UPDATE/DELETE 错误臂；
//   - CheckSchema 的列查询错误臂。
//
// w14i 波次不可达清单（均已核对）：rows.Scan/rows.Err 中断迭代臂、
// tx.Commit/pool 关闭级错误臂——pgFake 无法在合法结果集中途注入迭代错误。

func w14iRegisterHappyCommit(t *testing.T, server *pgFakeServer) {
	t.Helper()
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	server.handleFunc(pgFakeCursorUpsertSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "INSERT 0 1", nil
	})
	server.handlePrefixFunc("INSERT INTO juhe_dataset.runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at) VALUES",
		[]pgFakeColumn{{name: "time", oid: pgFakeOIDText}, {name: "level", oid: pgFakeOIDText}, {name: "event", oid: pgFakeOIDText}},
		func(sql string, args []string) ([][]any, string, *pgFakeError) {
			records := len(args) / 12
			rows := make([][]any, 0, records)
			for index := 0; index < records; index++ {
				base := index * 12
				rows = append(rows, []any{args[base+4], args[base+5], args[base+7]})
			}
			return rows, "INSERT " + string(rune('0'+records)), nil
		})
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key) DO UPDATE SET total_count = juhe_dataset.runtime_log_facet_summary.total_count + excluded.total_count, earliest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.earliest_time IS NULL OR excluded.earliest_time < juhe_dataset.runtime_log_facet_summary.earliest_time THEN excluded.earliest_time ELSE juhe_dataset.runtime_log_facet_summary.earliest_time END, latest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_facet_summary.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_facet_summary.latest_time END, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES ($1, $2, $3, $4) ON CONFLICT(bucket_key, level) DO UPDATE SET count = juhe_dataset.runtime_log_level_facets.count + excluded.count, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key, event) DO UPDATE SET count = juhe_dataset.runtime_log_event_facets.count + excluded.count, latest_time = CASE WHEN juhe_dataset.runtime_log_event_facets.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_event_facets.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_event_facets.latest_time END, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
}

func w14iFakeCommit(t *testing.T, store *postgresStore) error {
	t.Helper()
	lease := OwnerLease{OwnerID: "w14i-fake-owner", FenceToken: 7}
	cursor := Cursor{LogFile: "/logs/w14i-fake.log", FileIdentity: "w14i-fake-identity", CreatedAt: "2026-08-08T00:00:00.000Z", LastReadAt: "2026-08-08T00:00:00.000Z"}
	records := []Record{{ID: "w14i-fake-rec", Time: "2026-08-08T00:00:00.000Z", Level: "info", Event: "w14i-fake-event", CreatedAt: "2026-08-08T00:00:00.000Z"}}
	return store.Commit(context.Background(), lease, records, cursor, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
}

func TestW14iFakeCommitFacetSummaryInsertError(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCommit(t, server)
	server.handleError("INSERT INTO juhe_dataset.runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key) DO UPDATE SET total_count = juhe_dataset.runtime_log_facet_summary.total_count + excluded.total_count, earliest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.earliest_time IS NULL OR excluded.earliest_time < juhe_dataset.runtime_log_facet_summary.earliest_time THEN excluded.earliest_time ELSE juhe_dataset.runtime_log_facet_summary.earliest_time END, latest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_facet_summary.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_facet_summary.latest_time END, updated_at = excluded.updated_at",
		"53000", "w14i summary boom")
	store := openFakePostgresStore(t, server)
	if err := w14iFakeCommit(t, store); err == nil || !strings.Contains(err.Error(), "w14i summary boom") {
		t.Fatalf("facet summary 写入失败应上抛: %v", err)
	}
}

func TestW14iFakeCommitFacetLevelInsertError(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCommit(t, server)
	server.handleError("INSERT INTO juhe_dataset.runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES ($1, $2, $3, $4) ON CONFLICT(bucket_key, level) DO UPDATE SET count = juhe_dataset.runtime_log_level_facets.count + excluded.count, updated_at = excluded.updated_at",
		"53000", "w14i level boom")
	store := openFakePostgresStore(t, server)
	if err := w14iFakeCommit(t, store); err == nil || !strings.Contains(err.Error(), "w14i level boom") {
		t.Fatalf("level facet 写入失败应上抛: %v", err)
	}
}

func TestW14iFakeCommitFacetEventInsertError(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCommit(t, server)
	server.handleError("INSERT INTO juhe_dataset.runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key, event) DO UPDATE SET count = juhe_dataset.runtime_log_event_facets.count + excluded.count, latest_time = CASE WHEN juhe_dataset.runtime_log_event_facets.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_event_facets.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_event_facets.latest_time END, updated_at = excluded.updated_at",
		"53000", "w14i event boom")
	store := openFakePostgresStore(t, server)
	if err := w14iFakeCommit(t, store); err == nil || !strings.Contains(err.Error(), "w14i event boom") {
		t.Fatalf("event facet 写入失败应上抛: %v", err)
	}
}

func TestW14iFakeCommitRecordInsertError(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCommit(t, server)
	server.handleErrorPrefix("INSERT INTO juhe_dataset.runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at) VALUES",
		"53000", "w14i record boom")
	store := openFakePostgresStore(t, server)
	if err := w14iFakeCommit(t, store); err == nil || !strings.Contains(err.Error(), "w14i record boom") {
		t.Fatalf("记录写入失败应上抛: %v", err)
	}
}

func TestW14iFakeCheckSchemaColumnQueryError(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	server.handleFunc("SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "", &pgFakeError{code: "53000", message: "w14i columns boom"}
	})
	store := openFakePostgresStore(t, server)
	if err := store.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "w14i columns boom") {
		t.Fatalf("列查询失败应上抛: %v", err)
	}
}

func w14iRegisterHappyCleanup(t *testing.T, server *pgFakeServer) {
	t.Helper()
	w14iRegisterHappyCommit(t, server)
	server.handleFunc(pgFakeCleanupSelectSQL, []pgFakeColumn{
		{name: "id", oid: pgFakeOIDText},
		{name: "time", oid: pgFakeOIDText},
		{name: "level", oid: pgFakeOIDText},
		{name: "event", oid: pgFakeOIDText},
	}, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{"w14i-expired-1", "2026-08-01T00:00:00.000Z", "info", "w14i-event"}}, "SELECT 1", nil
	})
	server.handlePrefixFunc("DELETE FROM juhe_dataset.runtime_logs WHERE id IN (", nil, func(sql string, args []string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE " + fmt.Sprintf("%d", len(args)), nil
	})
	server.handleFunc("SELECT COALESCE((SELECT earliest_time::text FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{"2026-08-01T00:00:00.000Z"}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time ASC, id ASC LIMIT 1), '')", []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{""}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time DESC, id DESC LIMIT 1), '')", []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}, func([]string) ([][]any, string, *pgFakeError) {
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
	server.handleFunc(`DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE ctid IN (SELECT ctid FROM juhe_dataset.runtime_log_file_cursors WHERE updated_at < $1 AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, ctid ASC LIMIT $2)`, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 0", nil
	})
}

func TestW14iFakeCleanupVerifyLeaseErrorPhaseOne(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCleanup(t, server)
	server.handleFunc(pgFakeLeaseVerifySQL, []pgFakeColumn{{name: "fence_token", oid: pgFakeOIDInt8}}, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "", &pgFakeError{code: "53000", message: "w14i verify boom"}
	})
	store := openFakePostgresStore(t, server)
	_, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "w14i-fake-owner", FenceToken: 7}, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 10, 3)
	if err == nil || !strings.Contains(err.Error(), "w14i verify boom") {
		t.Fatalf("第一阶段 lease 校验失败应上抛: %v", err)
	}
}

func TestW14iFakeCleanupVerifyLeaseErrorPhaseTwo(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCleanup(t, server)
	// 第一阶段 lease 校验成功、批删空转退出；第二阶段校验开始失败。
	var calls int64
	server.handleFunc(pgFakeLeaseVerifySQL, []pgFakeColumn{{name: "fence_token", oid: pgFakeOIDInt8}}, func([]string) ([][]any, string, *pgFakeError) {
		if atomic.AddInt64(&calls, 1) > 1 {
			return nil, "", &pgFakeError{code: "53000", message: "w14i phase2 verify boom"}
		}
		return [][]any{{"7"}}, "SELECT 1", nil
	})
	// 第一批次删除后即不足一批，第二阶段游标批在首个事务失败。
	server.handleFunc(pgFakeCleanupSelectSQL, []pgFakeColumn{
		{name: "id", oid: pgFakeOIDText},
		{name: "time", oid: pgFakeOIDText},
		{name: "level", oid: pgFakeOIDText},
		{name: "event", oid: pgFakeOIDText},
	}, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "SELECT 0", nil
	})
	store := openFakePostgresStore(t, server)
	_, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "w14i-fake-owner", FenceToken: 7}, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 10, 3)
	if err == nil || !strings.Contains(err.Error(), "w14i phase2 verify boom") {
		t.Fatalf("第二阶段 lease 校验失败应上抛: %v", err)
	}
}

func TestW14iFakeCleanupSelectError(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCleanup(t, server)
	server.handleFunc(pgFakeCleanupSelectSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "", &pgFakeError{code: "53000", message: "w14i select boom"}
	})
	store := openFakePostgresStore(t, server)
	_, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "w14i-fake-owner", FenceToken: 7}, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 10, 3)
	if err == nil || !strings.Contains(err.Error(), "w14i select boom") {
		t.Fatalf("过期记录查询失败应上抛: %v", err)
	}
}

func TestW14iFakeCleanupDeleteError(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCleanup(t, server)
	server.handleErrorPrefix("DELETE FROM juhe_dataset.runtime_logs WHERE id IN (", "53000", "w14i delete boom")
	store := openFakePostgresStore(t, server)
	_, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "w14i-fake-owner", FenceToken: 7}, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 10, 3)
	if err == nil || !strings.Contains(err.Error(), "w14i delete boom") {
		t.Fatalf("过期记录删除失败应上抛: %v", err)
	}
}

func TestW14iFakeDecrementArms(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		errFn func(server *pgFakeServer)
		boom  string
	}{
		{name: "counted-from", sql: "SELECT COALESCE((SELECT earliest_time::text FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", boom: "w14i counted boom"},
		{name: "earliest-select", sql: "SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time ASC, id ASC LIMIT 1), '')", boom: "w14i earliest boom"},
		{name: "latest-select", sql: "SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time DESC, id DESC LIMIT 1), '')", boom: "w14i latest boom"},
		{name: "summary-update", sql: "UPDATE juhe_dataset.runtime_log_facet_summary SET total_count = GREATEST(0, total_count - $1), earliest_time = $2, latest_time = $3, updated_at = $4 WHERE bucket_key = $5", boom: "w14i summary update boom"},
		{name: "summary-delete", sql: "DELETE FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1 AND total_count <= 0", boom: "w14i summary delete boom"},
		{name: "level-update", sql: "UPDATE juhe_dataset.runtime_log_level_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND level = $4", boom: "w14i level update boom"},
		{name: "level-delete", sql: "DELETE FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = $1 AND count <= 0", boom: "w14i level delete boom"},
		{name: "event-update", sql: "UPDATE juhe_dataset.runtime_log_event_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND event = $4", boom: "w14i event update boom"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := newPGFakeServer(t)
			w14iRegisterHappyCleanup(t, server)
			server.handleError(testCase.sql, "53000", testCase.boom)
			store := openFakePostgresStore(t, server)
			_, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "w14i-fake-owner", FenceToken: 7}, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 10, 3)
			if err == nil || !strings.Contains(err.Error(), testCase.boom) {
				for _, entry := range server.executedLog() {
					t.Logf("executed: %.200q", entry.sql)
				}
				t.Fatalf("%s 失败应上抛: %v", testCase.name, err)
			}
		})
	}
}

func TestW14iFakeCommitInsertParseError(t *testing.T) {
	// 未注册记录 INSERT：tx.Query 在 Parse 阶段即失败并直接上抛。
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	server.handleFunc(pgFakeCursorUpsertSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "INSERT 0 1", nil
	})
	store := openFakePostgresStore(t, server)
	if err := w14iFakeCommit(t, store); err == nil {
		t.Fatalf("未注册的记录 INSERT 应使 Commit 失败")
	}
}

func TestW14iFakeCheckSchemaColumnsQueryParseError(t *testing.T) {
	// 未注册列查询：CheckSchema 必须在 checkPostgresColumns 的 Query 处直接上抛。
	server := newPGFakeServer(t)
	server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeTableNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		return [][]any{{args[0]}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT indexname FROM pg_indexes WHERE schemaname = 'juhe_dataset' AND indexname = $1", pgFakeIndexNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		return [][]any{{args[0]}}, "SELECT 1", nil
	})
	store := openFakePostgresStore(t, server)
	if err := store.CheckSchema(context.Background()); err == nil {
		t.Fatalf("未注册的列查询应使 CheckSchema 失败")
	}
}

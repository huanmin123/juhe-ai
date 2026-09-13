package runtimelog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wn_postgres_ops_test.go 覆盖 postgresStore 的 owner lease 状态机、cursor
// 读写、Commit 聚合与 Cleanup 批处理语义。SQL 响应由 pgfake 脚本化提供，
// 参数与 SQL 全文来自 server 执行日志逐字符断言。

// 与 store.go 中的 SQL 常量保持逐字一致（服务端会做空白归一化）。
const (
	pgFakeLeaseVerifySQL = `SELECT fence_token
    FROM juhe_dataset.runtime_log_index_owner_leases
    WHERE lease_key = $1 AND owner_id = $2 AND fence_token = $3 AND lease_until > $4
    FOR UPDATE`

	pgFakeLeaseAcquireSQL = `INSERT INTO juhe_dataset.runtime_log_index_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
    VALUES ($1, $2, 1, $3, $4)
    ON CONFLICT(lease_key) DO UPDATE SET
      owner_id = excluded.owner_id,
      fence_token = juhe_dataset.runtime_log_index_owner_leases.fence_token + 1,
      lease_until = excluded.lease_until,
      updated_at = excluded.updated_at
    WHERE juhe_dataset.runtime_log_index_owner_leases.lease_until <= $5
    RETURNING fence_token`

	pgFakeLeaseRenewSQL = `UPDATE juhe_dataset.runtime_log_index_owner_leases
    SET lease_until = $1, updated_at = $2
    WHERE lease_key = $3 AND owner_id = $4 AND fence_token = $5 AND lease_until > $6`

	pgFakeLeaseReleaseSQL = `UPDATE juhe_dataset.runtime_log_index_owner_leases SET owner_id = '', lease_until = $1, updated_at = $1 WHERE lease_key = $2 AND owner_id = $3 AND fence_token = $4`

	pgFakeCursorUpsertSQL = `INSERT INTO juhe_dataset.runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT(log_file) DO UPDATE SET file_identity = excluded.file_identity, cursor_offset = excluded.cursor_offset, line_number = excluded.line_number, file_size = excluded.file_size, truncation_generation = excluded.truncation_generation, file_mtime_ms = excluded.file_mtime_ms, last_read_at = excluded.last_read_at, last_error_message = excluded.last_error_message, updated_at = excluded.updated_at`

	pgFakeCursorSelectWhereFile = `SELECT log_file, COALESCE(file_identity, ''), cursor_offset, line_number, file_size, truncation_generation, COALESCE(file_mtime_ms, 0), COALESCE(last_read_at::text, ''), COALESCE(last_error_message, ''), created_at::text, updated_at::text FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1`

	pgFakeCursorSelectWhereIdentity = `SELECT log_file, COALESCE(file_identity, ''), cursor_offset, line_number, file_size, truncation_generation, COALESCE(file_mtime_ms, 0), COALESCE(last_read_at::text, ''), COALESCE(last_error_message, ''), created_at::text, updated_at::text FROM juhe_dataset.runtime_log_file_cursors WHERE file_identity = $1 ORDER BY updated_at DESC LIMIT 1`

	pgFakeCleanupSelectSQL = `SELECT id, time::text, level, COALESCE(event, '') FROM juhe_dataset.runtime_logs WHERE time < $1 ORDER BY time ASC, id ASC LIMIT $2`

	pgFakeRetentionDaysSQL = `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = $1 AND key = $2`
)

// pgFakeCursorColumns 是 cursor 查询的 11 列声明，与 postgresCursorSelect 对齐。
func pgFakeCursorColumns() []pgFakeColumn {
	return []pgFakeColumn{
		{name: "log_file", oid: pgFakeOIDText},
		{name: "file_identity", oid: pgFakeOIDText},
		{name: "cursor_offset", oid: pgFakeOIDInt8},
		{name: "line_number", oid: pgFakeOIDInt8},
		{name: "file_size", oid: pgFakeOIDInt8},
		{name: "truncation_generation", oid: pgFakeOIDInt8},
		{name: "file_mtime_ms", oid: pgFakeOIDInt8},
		{name: "last_read_at", oid: pgFakeOIDText},
		{name: "last_error_message", oid: pgFakeOIDText},
		{name: "created_at", oid: pgFakeOIDText},
		{name: "updated_at", oid: pgFakeOIDText},
	}
}

// pgFakeCursorRow 生成一行与 cursor 值对应的文本结果。
func pgFakeCursorRow(cursor Cursor) []any {
	return []any{
		cursor.LogFile,
		cursor.FileIdentity,
		fmt.Sprintf("%d", cursor.CursorOffset),
		fmt.Sprintf("%d", cursor.LineNumber),
		fmt.Sprintf("%d", cursor.FileSize),
		fmt.Sprintf("%d", cursor.TruncationGeneration),
		fmt.Sprintf("%d", cursor.FileMtimeMs),
		cursor.LastReadAt,
		cursor.LastErrorMessage,
		cursor.CreatedAt,
		cursor.UpdatedAt,
	}
}

// registerPostgresLeaseHandlers 注册 owner lease 状态机的标准脚本：
// 校验/续约/释放默认成功，获取返回 fence_token=7。
func registerPostgresLeaseHandlers(t *testing.T, server *pgFakeServer) {
	t.Helper()
	server.handleFunc(pgFakeLeaseVerifySQL, []pgFakeColumn{{name: "fence_token", oid: pgFakeOIDInt8}}, func(args []string) ([][]any, string, *pgFakeError) {
		return [][]any{{"7"}}, "SELECT 1", nil
	})
	server.handleFunc(pgFakeLeaseAcquireSQL, []pgFakeColumn{{name: "fence_token", oid: pgFakeOIDInt8}}, func(args []string) ([][]any, string, *pgFakeError) {
		return [][]any{{"7"}}, "SELECT 1", nil
	})
	server.handleFunc(pgFakeLeaseRenewSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "UPDATE 1", nil
	})
	server.handleFunc(pgFakeLeaseReleaseSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "UPDATE 1", nil
	})
}

// findExecuted 在执行日志中查找含 needle 的最近一条记录。
func findExecuted(t *testing.T, server *pgFakeServer, needle string) pgFakeExecuted {
	t.Helper()
	found := false
	result := pgFakeExecuted{}
	for _, entry := range server.executedLog() {
		if strings.Contains(normalizePGSQL(entry.sql), normalizePGSQL(needle)) {
			result = entry
			found = true
		}
	}
	if !found {
		t.Fatalf("执行日志中未找到包含 %q 的 SQL", needle)
	}
	return result
}

func TestPostgresFakeOwnerLeaseLifecycle(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	store := openFakePostgresStore(t, server)
	ctx := context.Background()

	lease, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner-a 必须获得 PostgreSQL lease: lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	if lease.OwnerID != "owner-a" || lease.FenceToken != 7 {
		t.Fatalf("lease 必须携带 owner 与 fence token: %#v", lease)
	}
	acquireEntry := findExecuted(t, server, "INSERT INTO juhe_dataset.runtime_log_index_owner_leases")
	if len(acquireEntry.args) < 2 || acquireEntry.args[1] != "owner-a" {
		t.Fatalf("获取 lease 必须绑定 owner 参数: %#v", acquireEntry.args)
	}

	// 另一 owner 在 lease 未过期时必须被拒绝（服务端返回零行）。
	server.handleFunc(pgFakeLeaseAcquireSQL, []pgFakeColumn{{name: "fence_token", oid: pgFakeOIDInt8}}, func(args []string) ([][]any, string, *pgFakeError) {
		return nil, "INSERT 0 0", nil
	})
	if _, acquired, err = store.AcquireOwnerLease(ctx, "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("未过期 lease 必须拒绝第二个 owner: acquired=%t err=%v", acquired, err)
	}

	renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute)
	if err != nil || !renewed {
		t.Fatalf("当前 token 必须可续约: renewed=%t err=%v", renewed, err)
	}
	server.handleFunc(pgFakeLeaseRenewSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "UPDATE 0", nil
	})
	if renewed, err = store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || renewed {
		t.Fatalf("过期 token 不得续约: renewed=%t err=%v", renewed, err)
	}

	if err := store.VerifyOwnerLease(ctx, lease); err != nil {
		t.Fatalf("有效 lease 校验必须通过: %v", err)
	}
	server.handleFunc(pgFakeLeaseVerifySQL, []pgFakeColumn{{name: "fence_token", oid: pgFakeOIDInt8}}, func(args []string) ([][]any, string, *pgFakeError) {
		return nil, "SELECT 0", nil
	})
	if err := store.VerifyOwnerLease(ctx, lease); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("零行校验必须返回 ErrOwnerLeaseLost: %v", err)
	}

	// 恢复释放脚本后正常释放；零行释放必须报 fenced。
	server.handleFunc(pgFakeLeaseReleaseSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "UPDATE 0", nil
	})
	if err := store.ReleaseOwnerLease(ctx, lease); !errors.Is(err, ErrOwnerLeaseFenced) {
		t.Fatalf("零行释放必须返回 ErrOwnerLeaseFenced: %v", err)
	}
	releaseEntry := findExecuted(t, server, "UPDATE juhe_dataset.runtime_log_index_owner_leases SET owner_id = ''")
	if len(releaseEntry.args) < 4 || releaseEntry.args[2] != "owner-a" || releaseEntry.args[3] != "7" {
		t.Fatalf("释放必须精确匹配 owner 与 fence token: %#v", releaseEntry.args)
	}
}

func TestPostgresFakeWithOwnerLeaseFenceCommitsAndRollsBack(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	store := openFakePostgresStore(t, server)
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "owner-a", FenceToken: 7}

	callbackErr := errors.New("fence callback fixture failure")
	if err := store.WithOwnerLeaseFence(ctx, lease, func() error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("callback 失败必须原样返回: %v", err)
	}
	rolledBack := false
	for _, entry := range server.executedLog() {
		if strings.EqualFold(entry.sql, "ROLLBACK") {
			rolledBack = true
		}
		if strings.EqualFold(entry.sql, "COMMIT") {
			t.Fatalf("callback 失败不得提交 fence 事务")
		}
	}
	if !rolledBack {
		t.Fatalf("callback 失败必须回滚 fence 事务")
	}

	executedInCallback := false
	if err := store.WithOwnerLeaseFence(ctx, lease, func() error { executedInCallback = true; return nil }); err != nil {
		t.Fatalf("fence 成功路径不得报错: %v", err)
	}
	if !executedInCallback {
		t.Fatalf("fence 成功路径必须执行 callback")
	}
	committed := false
	for _, entry := range server.executedLog() {
		if strings.EqualFold(entry.sql, "COMMIT") {
			committed = true
		}
	}
	if !committed {
		t.Fatalf("fence 成功路径必须提交事务")
	}
}

func TestPostgresFakeCursorReadsAndReplace(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	replacement := Cursor{
		LogFile:              "/logs/juhe-ai.log",
		FileIdentity:         "win:42:1700000000000",
		CursorOffset:         128,
		LineNumber:           9,
		FileSize:             128,
		TruncationGeneration: 1,
		FileMtimeMs:          1700000000001,
		LastReadAt:           "2026-08-08T00:00:00.000Z",
		LastErrorMessage:     "",
		CreatedAt:            "2026-08-08T00:00:00.000Z",
		UpdatedAt:            "2026-08-08T00:00:00.000Z",
	}
	server.handleFunc(pgFakeCursorSelectWhereFile, pgFakeCursorColumns(), func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{pgFakeCursorRow(replacement)}, "SELECT 1", nil
	})
	server.handleFunc(pgFakeCursorSelectWhereIdentity, pgFakeCursorColumns(), func([]string) ([][]any, string, *pgFakeError) {
		return nil, "SELECT 0", nil
	})
	server.handleFunc(pgFakeCursorUpsertSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "INSERT 0 1", nil
	})
	server.handleFunc("DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1", nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE 1", nil
	})
	store := openFakePostgresStore(t, server)
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "owner-a", FenceToken: 7}

	found, err := store.FindCursor(ctx, replacement.LogFile)
	if err != nil || found == nil {
		t.Fatalf("必须读取到 cursor: cursor=%#v err=%v", found, err)
	}
	if *found != replacement {
		t.Fatalf("cursor 往返不一致: got=%#v want=%#v", *found, replacement)
	}
	missing, err := store.FindCursorByIdentity(ctx, "unknown-identity")
	if err != nil || missing != nil {
		t.Fatalf("无匹配 identity 必须返回 nil cursor: cursor=%#v err=%v", missing, err)
	}

	displaced := Cursor{LogFile: "__runtime_log_identity__:old:1:1", FileIdentity: "old:1:1"}
	if err := store.ReplaceCursor(ctx, lease, &displaced, replacement); err != nil {
		t.Fatalf("ReplaceCursor 必须保留被替换 cursor 并写入新 cursor: %v", err)
	}
	deleteEntry := findExecuted(t, server, "DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1")
	if len(deleteEntry.args) < 1 || deleteEntry.args[0] != replacement.LogFile {
		t.Fatalf("ReplaceCursor 必须先删除同路径旧 cursor: %#v", deleteEntry.args)
	}
	if err := store.CopyCursor(ctx, lease, replacement); err != nil {
		t.Fatalf("CopyCursor 必须在 fence 校验后 upsert: %v", err)
	}
	var upsertLogFiles []string
	for _, entry := range server.executedLog() {
		if strings.HasPrefix(normalizePGSQL(entry.sql), "INSERT INTO juhe_dataset.runtime_log_file_cursors") {
			if len(entry.args) < 11 {
				t.Fatalf("cursor upsert 参数不完整: %#v", entry.args)
			}
			upsertLogFiles = append(upsertLogFiles, entry.args[0])
		}
	}
	// ReplaceCursor 必须先保留被替换 cursor（displaced identity 前缀）再写入新
	// cursor；CopyCursor 随后复制一次。
	wantOrder := []string{displaced.LogFile, replacement.LogFile, replacement.LogFile}
	if len(upsertLogFiles) != len(wantOrder) {
		t.Fatalf("cursor upsert 次数 = %d, want %d: %#v", len(upsertLogFiles), len(wantOrder), upsertLogFiles)
	}
	for index, want := range wantOrder {
		if upsertLogFiles[index] != want {
			t.Fatalf("第 %d 次 cursor upsert log_file = %q, want %q", index+1, upsertLogFiles[index], want)
		}
	}
}

func TestPostgresFakeCommitWritesFacetsOnlyForRetainedRecords(t *testing.T) {
	server := newPGFakeServer(t)
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
			return rows, fmt.Sprintf("INSERT %d", records), nil
		})
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key) DO UPDATE SET total_count = juhe_dataset.runtime_log_facet_summary.total_count + excluded.total_count, earliest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.earliest_time IS NULL OR excluded.earliest_time < juhe_dataset.runtime_log_facet_summary.earliest_time THEN excluded.earliest_time ELSE juhe_dataset.runtime_log_facet_summary.earliest_time END, latest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_facet_summary.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_facet_summary.latest_time END, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES ($1, $2, $3, $4) ON CONFLICT(bucket_key, level) DO UPDATE SET count = juhe_dataset.runtime_log_level_facets.count + excluded.count, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
	server.handleFunc("INSERT INTO juhe_dataset.runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key, event) DO UPDATE SET count = juhe_dataset.runtime_log_event_facets.count + excluded.count, latest_time = CASE WHEN juhe_dataset.runtime_log_event_facets.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_event_facets.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_event_facets.latest_time END, updated_at = excluded.updated_at",
		nil, func([]string) ([][]any, string, *pgFakeError) { return nil, "INSERT 0 1", nil })
	store := openFakePostgresStore(t, server)
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "owner-a", FenceToken: 7}

	cutoff := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	cursor := Cursor{LogFile: "/logs/juhe-ai.log", FileIdentity: "win:42:1", CreatedAt: "2026-08-08T00:00:00.000Z", LastReadAt: "2026-08-08T00:00:00.000Z"}
	records := []Record{
		{ID: "expired", Time: "2026-08-01T00:00:00.000Z", Level: "info", Event: "expired-event", CreatedAt: "2026-08-01T00:00:00.000Z"},
		{ID: "retained", Time: "2026-08-08T00:00:00.000Z", Level: "warn", Event: "retained-event", CreatedAt: "2026-08-08T00:00:00.000Z"},
	}
	if err := store.Commit(ctx, lease, records, cursor, cutoff); err != nil {
		t.Fatalf("Commit 必须写入 records/cursor/facets: %v", err)
	}

	summary := findExecuted(t, server, "INSERT INTO juhe_dataset.runtime_log_facet_summary")
	// args: bucket, total_count, earliest, latest, updated_at。过期记录不得计入。
	if len(summary.args) < 4 || summary.args[0] != "current" || summary.args[1] != "1" || summary.args[2] != "2026-08-08T00:00:00.000Z" || summary.args[3] != "2026-08-08T00:00:00.000Z" {
		t.Fatalf("facet 汇总必须只统计 cutoff 之后的记录: %#v", summary.args)
	}
	level := findExecuted(t, server, "INSERT INTO juhe_dataset.runtime_log_level_facets")
	if len(level.args) < 3 || level.args[1] != "warn" || level.args[2] != "1" {
		t.Fatalf("level facet 必须聚合 retained 记录级别: %#v", level.args)
	}
	event := findExecuted(t, server, "INSERT INTO juhe_dataset.runtime_log_event_facets")
	if len(event.args) < 3 || event.args[1] != "retained-event" {
		t.Fatalf("event facet 必须聚合 retained 记录事件: %#v", event.args)
	}
}

func TestPostgresFakeCleanupBatchesAndFacetAdjustment(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	server.handleFunc(pgFakeCleanupSelectSQL, []pgFakeColumn{
		{name: "id", oid: pgFakeOIDText},
		{name: "time", oid: pgFakeOIDText},
		{name: "level", oid: pgFakeOIDText},
		{name: "event", oid: pgFakeOIDText},
	}, func(args []string) ([][]any, string, *pgFakeError) {
		return [][]any{
			{"expired-1", "2026-08-01T00:00:00.000Z", "info", "expired-event"},
			{"expired-2", "2026-08-01T01:00:00.000Z", "warn", ""},
		}, "SELECT 2", nil
	})
	server.handlePrefixFunc("DELETE FROM juhe_dataset.runtime_logs WHERE id IN (", nil, func(sql string, args []string) ([][]any, string, *pgFakeError) {
		return nil, "DELETE " + fmt.Sprintf("%d", len(args)), nil
	})
	server.handleFunc("SELECT COALESCE((SELECT earliest_time::text FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{""}}, "SELECT 1", nil
	})
	earliestColumns := []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}
	server.handleFunc("SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time ASC, id ASC LIMIT 1), '')", earliestColumns, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{"2026-08-08T00:00:00.000Z"}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time DESC, id DESC LIMIT 1), '')", earliestColumns, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{"2026-08-08T00:00:00.000Z"}}, "SELECT 1", nil
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
		return nil, "DELETE 1", nil
	})
	store := openFakePostgresStore(t, server)
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "owner-a", FenceToken: 7}

	result, err := store.Cleanup(ctx, lease, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), 10, 1)
	if err != nil {
		t.Fatalf("Cleanup 必须按批清理: %v", err)
	}
	if result.RuntimeLogs != 2 || result.RuntimeLogCursors != 1 {
		t.Fatalf("Cleanup 统计不正确: %#v", result)
	}
	deleteEntry := findExecuted(t, server, "DELETE FROM juhe_dataset.runtime_logs WHERE id IN (")
	if len(deleteEntry.args) != 2 || deleteEntry.args[0] != "expired-1" || deleteEntry.args[1] != "expired-2" {
		t.Fatalf("过期记录必须按主键批量删除: %#v", deleteEntry.args)
	}

	// lease 失效时清理必须立即失败且不产生删除。
	lostServer := newPGFakeServer(t)
	registerPostgresCatalog(t, lostServer)
	lostServer.handleFunc(pgFakeLeaseVerifySQL, []pgFakeColumn{{name: "fence_token", oid: pgFakeOIDInt8}}, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "SELECT 0", nil
	})
	lostStore := openFakePostgresStore(t, lostServer)
	if _, err := lostStore.Cleanup(ctx, lease, time.Now(), 10, 1); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("失效 lease 的清理必须返回 ErrOwnerLeaseLost: %v", err)
	}
	for _, entry := range lostServer.executedLog() {
		if strings.HasPrefix(normalizePGSQL(entry.sql), "DELETE FROM juhe_dataset.runtime_logs") {
			t.Fatalf("失效 lease 不得执行删除")
		}
	}
}

func TestPostgresFakeRuntimeRetentionDaysBranches(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	store := openFakePostgresStore(t, server)
	ctx := context.Background()

	setRetention := func(value string) {
		server.handleFunc(pgFakeRetentionDaysSQL, []pgFakeColumn{{name: "value_json", oid: pgFakeOIDText}}, func([]string) ([][]any, string, *pgFakeError) {
			return [][]any{{value}}, "SELECT 1", nil
		})
	}
	setRetention("30")
	days, err := store.RuntimeRetentionDays(ctx, 7)
	if err != nil || days != 30 {
		t.Fatalf("必须读取业务设置的保留天数: days=%d err=%v", days, err)
	}
	setRetention("99")
	if days, err = store.RuntimeRetentionDays(ctx, 7); err == nil || days != 0 {
		t.Fatalf("超出 1..90 的保留天数必须拒绝: days=%d err=%v", days, err)
	}
	setRetention("not-json")
	if days, err = store.RuntimeRetentionDays(ctx, 7); err == nil || days != 0 {
		t.Fatalf("非 JSON 设置必须拒绝: days=%d err=%v", days, err)
	}
	server.handleFunc(pgFakeRetentionDaysSQL, []pgFakeColumn{{name: "value_json", oid: pgFakeOIDText}}, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "SELECT 0", nil
	})
	if days, err = store.RuntimeRetentionDays(ctx, 7); err != nil || days != 7 {
		t.Fatalf("无设置时必须回退默认值: days=%d err=%v", days, err)
	}
}

func TestPostgresFakeOpenStoreRejectsInvalidConfigs(t *testing.T) {
	if _, err := OpenStore(context.Background(), Config{Mode: ModePostgres, PostgresURL: "not-a-postgres-url"}); err == nil {
		t.Fatal("非法 PostgreSQL URL 必须拒绝启动")
	}
	server := newPGFakeServer(t)
	if _, err := OpenStore(context.Background(), Config{Mode: ModePostgres, PostgresURL: server.URL(), PostgresMaxConns: 1, PostgresMinConns: 2}); err == nil || !strings.Contains(err.Error(), "min connections") {
		t.Fatalf("min connections 大于 max connections 必须拒绝启动: %v", err)
	}
	if _, err := OpenStore(context.Background(), Config{Mode: Mode("bogus")}); err == nil || !strings.Contains(err.Error(), "不支持的运行日志 Store 模式") {
		t.Fatalf("未知 Store 模式必须拒绝启动: %v", err)
	}
	// 连接已关闭的端口必须报错（覆盖 Ping 失败路径）。
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("创建已关闭端口 fixture 失败: %v", err)
	}
	closedURL := fmt.Sprintf("postgres://jobs:secret@%s/juhe_ai_fake?sslmode=disable", closedListener.Addr())
	if err := closedListener.Close(); err != nil {
		t.Fatalf("关闭端口 fixture 失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := OpenStore(ctx, Config{Mode: ModePostgres, PostgresURL: closedURL}); err == nil {
		t.Fatal("无法连接的 PostgreSQL 必须拒绝启动")
	}
}

func TestPostgresFakeEnsureSchemaReportsDDLFailure(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	var tablesQueryCount atomic.Int64
	server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeTableNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		if tablesQueryCount.Add(1) == 1 {
			return nil, "SELECT 0", nil
		}
		return [][]any{{args[0]}}, "SELECT 1", nil
	})
	server.handleError("CREATE SCHEMA IF NOT EXISTS juhe_dataset", "42501", "permission denied for schema")
	store := openFakePostgresStore(t, server)
	if err := EnsureSchema(context.Background(), store); err == nil || !strings.Contains(err.Error(), "permission denied for schema") {
		t.Fatalf("bootstrap DDL 失败必须返回原始错误: %v", err)
	}
}

func TestEnsureSchemaRejectsUnknownStore(t *testing.T) {
	err := EnsureSchema(context.Background(), unknownStoreStub{})
	if err == nil || !strings.Contains(err.Error(), "未知运行日志 Store") {
		t.Fatalf("未知 Store 必须拒绝初始化: %v", err)
	}
}

type unknownStoreStub struct{ Store }

func TestSQLiteParseRuntimeRetentionDaysDirect(t *testing.T) {
	if days, err := parseRuntimeRetentionDays("14"); err != nil || days != 14 {
		t.Fatalf("整数 JSON 必须解析: days=%d err=%v", days, err)
	}
	for _, value := range []string{"\"14\"", "0", "91", "13.5", "{}"} {
		if _, err := parseRuntimeRetentionDays(value); err == nil {
			t.Fatalf("保留天数 %q 必须拒绝", value)
		}
	}
}

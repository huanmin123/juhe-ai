package runtimelog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	postgresSmokeURLVariable      = "JUHE_AI_RUNTIME_LOG_POSTGRES_SMOKE_URL"
	postgresSmokeRequiredVariable = "JUHE_AI_RUNTIME_LOG_POSTGRES_SMOKE_REQUIRED"
)

func TestPostgresRuntimeLogAdapterSmoke(t *testing.T) {
	url := strings.TrimSpace(os.Getenv(postgresSmokeURLVariable))
	if url == "" {
		if postgresSmokeRequired(os.Getenv(postgresSmokeRequiredVariable)) {
			t.Fatalf("%s=true/1 时必须设置 %s", postgresSmokeRequiredVariable, postgresSmokeURLVariable)
		}
		t.Skipf("未设置 %s；PostgreSQL adapter smoke 被显式跳过，不能视为 PostgreSQL 验证通过", postgresSmokeURLVariable)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, err := OpenStore(ctx, Config{Mode: ModePostgres, PostgresURL: url})
	if err != nil {
		t.Fatalf("连接 PostgreSQL smoke 数据库失败: %s", redactPostgresSmokeError(err, url))
	}
	store, ok := opened.(*postgresStore)
	if !ok {
		_ = opened.Close()
		t.Fatalf("OpenStore(ModePostgres) 返回 %T，期望 *postgresStore", opened)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭 PostgreSQL smoke store 失败: %s", redactPostgresSmokeError(err, url))
		}
	})
	if err := EnsureSchema(ctx, store); err != nil {
		t.Fatalf("初始化 PostgreSQL F1 schema 失败: %s", redactPostgresSmokeError(err, url))
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("检查 PostgreSQL F1 schema 失败: %s", redactPostgresSmokeError(err, url))
	}
	assertPostgresF1TablesEmpty(t, ctx, store)

	prefix := postgresSmokePrefix(t)
	ownerA := prefix + "-owner-a"
	ownerB := prefix + "-owner-b"
	cleanupOwner := prefix + "-owner-cleanup"
	leaseA, acquired, err := store.AcquireOwnerLease(ctx, ownerA, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner A 必须获得 PostgreSQL lease: lease=%#v acquired=%t err=%s", leaseA, acquired, redactPostgresSmokeError(err, url))
	}
	cleanupLease := leaseA
	t.Cleanup(func() {
		cleanupPostgresSmokeData(t, store, cleanupLease, cleanupOwner, url)
	})

	if _, acquired, err := store.AcquireOwnerLease(ctx, ownerB, time.Minute); err != nil || acquired {
		t.Fatalf("owner B 在 A 未过期时必须不能获得 lease: acquired=%t err=%s", acquired, redactPostgresSmokeError(err, url))
	}

	expiredLeaseTime := "2000-01-01T00:00:00.000Z"
	updated, err := store.pool.Exec(ctx, `
UPDATE juhe_dataset.runtime_log_index_owner_leases
SET lease_until = $1, updated_at = $1
WHERE lease_key = $2 AND owner_id = $3 AND fence_token = $4
`, expiredLeaseTime, runtimeLogOwnerLeaseKey, leaseA.OwnerID, leaseA.FenceToken)
	if err != nil {
		t.Fatalf("使 owner A lease 过期失败: %s", redactPostgresSmokeError(err, url))
	}
	if updated.RowsAffected() != 1 {
		t.Fatalf("使 owner A lease 过期必须只更新一行，实际为 %d", updated.RowsAffected())
	}

	leaseB, acquired, err := store.AcquireOwnerLease(ctx, ownerB, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("过期后 owner B 必须获得 lease: lease=%#v acquired=%t err=%s", leaseB, acquired, redactPostgresSmokeError(err, url))
	}
	cleanupLease = leaseB
	if leaseB.FenceToken <= leaseA.FenceToken {
		t.Fatalf("owner B fence token 必须递增: A=%d B=%d", leaseA.FenceToken, leaseB.FenceToken)
	}

	retentionCutoff := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	staleCursor := postgresSmokeCursor(prefix+"-stale", "2026-08-08T00:00:00.000Z")
	staleRecord := postgresSmokeRecord(prefix+"-stale", "2026-08-08T00:00:00.000Z", "warn")
	if err := store.Commit(ctx, leaseA, []Record{staleRecord}, staleCursor, retentionCutoff); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("旧 owner A Commit 必须返回 ErrOwnerLeaseLost/ErrOwnerLeaseFenced，实际为 %v", err)
	}

	retainedTime := "2026-08-08T00:00:00.000Z"
	expiredTime := "2020-01-01T00:00:00.000Z"
	cursor := postgresSmokeCursor(prefix, retainedTime)
	records := []Record{
		postgresSmokeRecord(prefix+"-expired", expiredTime, "error"),
		postgresSmokeRecord(prefix+"-retained", retainedTime, "warn"),
	}
	if err := store.Commit(ctx, leaseB, records, cursor, retentionCutoff); err != nil {
		t.Fatalf("owner B Commit 写入 records/cursor/facets 失败: %s", redactPostgresSmokeError(err, url))
	}

	storedCursor, err := store.FindCursor(ctx, cursor.LogFile)
	if err != nil || storedCursor == nil {
		t.Fatalf("Commit 后必须可读取 cursor: cursor=%#v err=%s", storedCursor, redactPostgresSmokeError(err, url))
	}
	if storedCursor.FileIdentity != cursor.FileIdentity || storedCursor.CursorOffset != cursor.CursorOffset || storedCursor.LineNumber != cursor.LineNumber {
		t.Fatalf("cursor 不符合写入值: got=%#v want=%#v", storedCursor, cursor)
	}
	assertPostgresRetainedFacets(t, ctx, store, records[1])

	cleanupResult, err := store.Cleanup(ctx, leaseB, retentionCutoff, 10, 10)
	if err != nil {
		t.Fatalf("清理过期 record 失败: %s", redactPostgresSmokeError(err, url))
	}
	if cleanupResult.RuntimeLogs != 1 {
		t.Fatalf("Cleanup.RuntimeLogs = %d，期望仅清理 1 条过期 record", cleanupResult.RuntimeLogs)
	}
	assertPostgresRetainedState(t, ctx, store, cursor, records[1])
}

func postgresSmokeRequired(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true") || strings.TrimSpace(value) == "1"
}

func postgresSmokePrefix(t *testing.T) string {
	t.Helper()
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatalf("生成 PostgreSQL smoke 唯一标识失败: %v", err)
	}
	return "pg-smoke-" + hex.EncodeToString(bytes)
}

func postgresSmokeRecord(id string, timestamp string, level string) Record {
	return Record{
		ID:         id,
		LogFile:    id + ".log",
		LogOffset:  1,
		LineNumber: 1,
		Time:       timestamp,
		Level:      level,
		TraceID:    id + "-trace",
		Event:      id + "-event",
		Message:    id + "-message",
		RawJSON:    `{"source":"postgres-smoke"}`,
		CreatedAt:  timestamp,
	}
}

func postgresSmokeCursor(prefix string, timestamp string) Cursor {
	return Cursor{
		LogFile:              prefix + ".log",
		FileIdentity:         prefix + "-file-identity",
		CursorOffset:         2,
		LineNumber:           2,
		FileSize:             2,
		TruncationGeneration: 0,
		FileMtimeMs:          1,
		LastReadAt:           timestamp,
		CreatedAt:            timestamp,
		UpdatedAt:            timestamp,
	}
}

func assertPostgresF1TablesEmpty(t *testing.T, ctx context.Context, store *postgresStore) {
	t.Helper()
	for _, table := range runtimeLogTables {
		count := postgresSmokeTableCount(t, ctx, store, table)
		if count != 0 {
			t.Fatalf("PostgreSQL smoke 仅允许使用 F1 表全空的专用可销毁数据库；juhe_dataset.%s 当前有 %d 行", table, count)
		}
	}
}

func assertPostgresRetainedFacets(t *testing.T, ctx context.Context, store *postgresStore, retained Record) {
	t.Helper()
	var total, levelCount, eventCount int
	if err := store.pool.QueryRow(ctx, "SELECT total_count FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1", facetBucketKey).Scan(&total); err != nil {
		t.Fatalf("读取 retained facet summary 失败: %v", err)
	}
	if err := store.pool.QueryRow(ctx, "SELECT count FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = $1 AND level = $2", facetBucketKey, retained.Level).Scan(&levelCount); err != nil {
		t.Fatalf("读取 retained level facet 失败: %v", err)
	}
	if err := store.pool.QueryRow(ctx, "SELECT count FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = $1 AND event = $2", facetBucketKey, retained.Event).Scan(&eventCount); err != nil {
		t.Fatalf("读取 retained event facet 失败: %v", err)
	}
	if total != 1 || levelCount != 1 || eventCount != 1 {
		t.Fatalf("retained facets 不正确: summary=%d level=%d event=%d", total, levelCount, eventCount)
	}
}

func assertPostgresRetainedState(t *testing.T, ctx context.Context, store *postgresStore, cursor Cursor, retained Record) {
	t.Helper()
	if count := postgresSmokeTableCount(t, ctx, store, "runtime_logs"); count != 1 {
		t.Fatalf("清理后 runtime_logs = %d，期望保留 1 条", count)
	}
	var storedID string
	if err := store.pool.QueryRow(ctx, "SELECT id FROM juhe_dataset.runtime_logs WHERE id = $1", retained.ID).Scan(&storedID); err != nil || storedID != retained.ID {
		t.Fatalf("清理后保留 record 不可见: id=%q err=%v", storedID, err)
	}
	storedCursor, err := store.FindCursor(ctx, cursor.LogFile)
	if err != nil || storedCursor == nil {
		t.Fatalf("清理后保留 cursor 不可见: cursor=%#v err=%v", storedCursor, err)
	}
	assertPostgresRetainedFacets(t, ctx, store, retained)
}

func cleanupPostgresSmokeData(t *testing.T, store *postgresStore, lease OwnerLease, cleanupOwner string, url string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.VerifyOwnerLease(ctx, lease); err != nil {
		var acquired bool
		lease, acquired, err = store.AcquireOwnerLease(ctx, cleanupOwner, time.Minute)
		if err != nil || !acquired {
			t.Errorf("PostgreSQL smoke 清理前无法取得有效 owner lease: acquired=%t err=%s", acquired, redactPostgresSmokeError(err, url))
			return
		}
	}
	if _, err := store.Cleanup(ctx, lease, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), 100, 100); err != nil {
		t.Errorf("PostgreSQL smoke adapter Cleanup 失败: %s", redactPostgresSmokeError(err, url))
	} else {
		for _, table := range runtimeLogTables {
			if table == "runtime_log_index_owner_leases" {
				continue
			}
			count, err := postgresSmokeTableCountError(ctx, store, table)
			if err != nil {
				t.Errorf("统计 PostgreSQL smoke 表 juhe_dataset.%s 失败: %s", table, redactPostgresSmokeError(err, url))
				continue
			}
			if count != 0 {
				t.Errorf("PostgreSQL smoke Cleanup 后 juhe_dataset.%s = %d，期望 0", table, count)
			}
		}
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Errorf("PostgreSQL smoke 释放当前 owner lease 失败: %s", redactPostgresSmokeError(err, url))
		return
	}
	count, err := postgresSmokeTableCountError(ctx, store, "runtime_log_index_owner_leases")
	if err != nil {
		t.Errorf("统计 PostgreSQL smoke owner lease 表失败: %s", redactPostgresSmokeError(err, url))
		return
	}
	if count != 1 {
		t.Errorf("PostgreSQL smoke release 后 owner lease 表 = %d，期望保留 1 行 fence 状态", count)
	}
}

func postgresSmokeTableCount(t *testing.T, ctx context.Context, store *postgresStore, table string) int {
	t.Helper()
	count, err := postgresSmokeTableCountError(ctx, store, table)
	if err != nil {
		t.Fatalf("统计 PostgreSQL smoke 表 juhe_dataset.%s 失败: %v", table, err)
	}
	return count
}

func postgresSmokeTableCountError(ctx context.Context, store *postgresStore, table string) (int, error) {
	var count int
	err := store.pool.QueryRow(ctx, "SELECT COUNT(*) FROM juhe_dataset."+table).Scan(&count)
	return count, err
}

func redactPostgresSmokeError(err error, url string) string {
	if err == nil {
		return "<nil>"
	}
	return strings.ReplaceAll(err.Error(), url, "[redacted PostgreSQL URL]")
}

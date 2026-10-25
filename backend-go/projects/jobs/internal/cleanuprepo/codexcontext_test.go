package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// codexcontext.go 的语义测试：SQLite 多分片（state-<i>.sqlite3）覆盖过期
// 会话批清理、storage key 入队、残余过期时间刷新、pending key 过滤与结算
// 退避；PG 路径用录制驱动覆盖同构语句链。

func TestStorageCleanupRetryAt(t *testing.T) {
	base := kitNow()
	cases := []struct {
		attempts int
		wantMs   int64
	}{
		{0, 30_000}, {1, 30_000}, {2, 60_000}, {10, 15_360_000}, {11, 21_600_000}, {99, 21_600_000},
	}
	for _, item := range cases {
		got := storageCleanupRetryAt(base, nil, item.attempts)
		want := ISOOf(base.Add(time.Duration(item.wantMs) * time.Millisecond))
		if got != want {
			t.Fatalf("storageCleanupRetryAt(%d) = %q, 期望 %q", item.attempts, got, want)
		}
	}
	// 溢出防护：抖动超过 MaxInt64-delayMs 时封顶。
	overflow := storageCleanupRetryAt(base, func(int64) int64 { return math.MaxInt64 }, 99)
	if overflow != ISOOf(base.Add(21_600_000*time.Millisecond)) {
		t.Fatalf("溢出防护失败：%q", overflow)
	}
}

func TestCodexCleanupExpiredPostgres(t *testing.T) {
	rec := newPGRecorder()
	store := &CodexContextStore{PG: openRecorderPG(rec), Now: kitNow}
	rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"},
		[][]driver.Value{{"s-1", "2026-09-01T00:00:00.000Z"}, {"s-2", "2026-09-02T00:00:00.000Z"}})
	rec.script("juhe_codex_context.codex_context_responses\n      WHERE session_id IN", []string{"storage_key"},
		[][]driver.Value{{"k-1"}})
	rec.script("juhe_codex_context.codex_context_compacts\n      WHERE session_id IN", []string{"storage_key"}, nil)
	rec.script("juhe_codex_context.codex_context_responses\n        WHERE session_id IN", []string{"session_id", "expires_at"},
		[][]driver.Value{{"s-2", "2026-12-01T00:00:00.000Z"}})
	rec.script("juhe_codex_context.codex_context_compacts\n        WHERE session_id IN", []string{"session_id", "expires_at"}, nil)
	rec.script("WHERE next_attempt_at <=", []string{"storage_key"},
		[][]driver.Value{{"k-1"}, {"k-9"}})
	rec.script("juhe_codex_context.codex_context_responses\n        WHERE storage_key IN", []string{"storage_key"},
		[][]driver.Value{{"k-9"}})
	rec.script("juhe_codex_context.codex_context_compacts\n        WHERE storage_key IN", []string{"storage_key"}, nil)

	result, err := store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 2)
	if err != nil {
		t.Fatalf("CleanupExpiredStates: %v", err)
	}
	if result.DeletedSessions != 1 || result.DeletedResponses != 1 || result.DeletedCompacts != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.StorageKeys) != 1 || result.StorageKeys[0] != "k-1" {
		t.Fatalf("StorageKeys = %v", result.StorageKeys)
	}
	// 语句家族断言：responses/compacts 删除、会话刷新、引用 key 队列清理。
	joined := make([]string, 0, 8)
	for _, statement := range rec.all() {
		joined = append(joined, statement.query)
	}
	allText := strings.Join(joined, "\n")
	for _, needle := range []string{
		"DELETE FROM juhe_codex_context.codex_context_responses",
		"UPDATE juhe_codex_context.codex_context_sessions",
		"DELETE FROM juhe_codex_context.codex_context_sessions WHERE id =",
		"INSERT INTO juhe_codex_context.codex_context_storage_cleanup_queue",
		"DELETE FROM codex_context_storage_cleanup_queue WHERE storage_key IN",
	} {
		if !strings.Contains(allText, needle) {
			t.Fatalf("缺少 PG 语句：%s", needle)
		}
	}
}

// TestSettleStorageCleanupPostgres：PG 结算（attempt 读取 + UPDATE 退避）。
func TestSettleStorageCleanupPostgres(t *testing.T) {
	rec := newPGRecorder()
	store := &CodexContextStore{PG: openRecorderPG(rec), Now: kitNow}
	rec.script("SELECT attempt_count", []string{"attempt_count"}, [][]driver.Value{{int64(2)}})

	result, err := store.SettleStorageCleanup(context.Background(), Settlement{
		SucceededStorageKeys: []string{"k-1"},
		Failures:             []SettlementFailure{{StorageKey: "k-2", Error: "boom"}},
		Now:                  kitUpdatedAt,
	})
	if err != nil {
		t.Fatalf("SettleStorageCleanup: %v", err)
	}
	if result.Acknowledged != 1 || result.Deferred != 1 {
		t.Fatalf("result = %+v", result)
	}
	var deferredUpdate *recordedStatement
	for index := range rec.statements {
		if strings.Contains(rec.statements[index].query, "UPDATE juhe_codex_context.codex_context_storage_cleanup_queue") {
			deferredUpdate = &rec.statements[index]
		}
	}
	if deferredUpdate == nil {
		t.Fatalf("缺少退避 UPDATE")
	}
	// attempt 2 → 第 3 次退避 120s。
	if fmtNextAttempt := fmt.Sprintf("%v", deferredUpdate.args[2]); fmtNextAttempt != ISOOf(kitNow().Add(120*time.Second)) {
		t.Fatalf("next_attempt_at = %v", deferredUpdate.args[2])
	}
}

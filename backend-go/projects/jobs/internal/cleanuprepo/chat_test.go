package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

)

// chat.go 的语义测试：SQLite 真库覆盖 cleanupChatRetention 主链（中断轮次
// 恢复 → 过期轮次删除 → 容量窗口收缩 → 空会话清理 → 标题回退）、压缩恢复、
// 检查点清理与资产认领删除；PG 录制驱动覆盖分区裁剪。

func TestDeleteChatAssetObjects(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := filepath.Join(root, "a", "b.bin")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := deleteChatAssetObjects(root, "a/b.bin", "a/missing.bin"); err != nil {
		t.Fatalf("删除应容忍缺失文件：%v", err)
	}
	if _, err := os.Stat(existing); !os.IsNotExist(err) {
		t.Fatalf("存在的文件应被删除")
	}
	if err := deleteChatAssetObjects(root, "a/../escape.bin"); err == nil {
		t.Fatalf(".. 路径应被拒绝")
	}
	if err := deleteChatAssetObjects(root, "/abs.bin"); err == nil {
		t.Fatalf("绝对路径应被拒绝")
	}
	if err := deleteChatAssetObjects(root, "a\x00b"); err == nil {
		t.Fatalf("空字节应被拒绝")
	}
	if err := deleteChatAssetObjects(root, "1.bin", "2.bin", "3.bin"); err == nil {
		t.Fatalf("超过两个对象应被拒绝")
	}
	// 重复键去重后仍可执行。
	if err := deleteChatAssetObjects(root, "a/missing.bin", "a/missing.bin"); err != nil {
		t.Fatalf("重复键应去重：%v", err)
	}
}

// TestDropExpiredChatPartitions：PG 分区裁剪（过期分区收集会话并 bump
// revision 后 DROP）。
func TestDropExpiredChatPartitions(t *testing.T) {
	rec := newPGRecorder()
	store := &ChatStore{DB: openRecorderPG(rec), Now: kitNow}
	rec.script("FROM pg_inherits", []string{"partition_name"}, [][]driver.Value{
		{"chat_messages_20260101"},
		{"chat_messages_20990101"}, // 未过期 → 跳过
		{"chat_messages_legacy"},   // 命名不匹配 → 跳过
	})
	rec.script("chat_messages_20260101", []string{"conversation_id", "system_account_id"},
		[][]driver.Value{{"conv-1", "sys-1"}, {"conv-2", "sys-1"}})

	tx, err := store.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	affected := map[string][2]string{}
	outcome, err := store.dropExpiredChatPartitions(context.Background(), tx, kitUpdatedAt, 30,
		func(conversationID, systemAccountID string) {
			affected[systemAccountID+"\x00"+conversationID] = [2]string{conversationID, systemAccountID}
		})
	if err != nil {
		t.Fatalf("dropExpiredChatPartitions: %v", err)
	}
	if outcome.DroppedPartitions != 1 || len(outcome.AdvancedKeys) != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(affected) != 2 {
		t.Fatalf("addAffected 回调 = %v", affected)
	}
	var dropped bool
	for _, statement := range rec.all() {
		if strings.Contains(statement.query, "DROP TABLE IF EXISTS juhe_chat") &&
			strings.Contains(statement.query, "chat_messages_20260101") {
			dropped = true
		}
	}
	if !dropped {
		t.Fatalf("过期分区应被 DROP")
	}
}

func TestChatPureHelpers(t *testing.T) {
	t.Run("cleanupRetryDelayMs", func(t *testing.T) {
		cases := []struct {
			attempts int
			want     int64
		}{
			{-5, 60_000}, {0, 60_000}, {1, 60_000}, {2, 120_000}, {3, 240_000}, {7, 3_600_000}, {8, 3_600_000}, {99, 3_600_000},
		}
		for _, item := range cases {
			if got := cleanupRetryDelayMs(item.attempts); got != item.want {
				t.Fatalf("cleanupRetryDelayMs(%d) = %d, 期望 %d", item.attempts, got, item.want)
			}
		}
	})
	t.Run("requiredChatTimestamp", func(t *testing.T) {
		if _, err := requiredChatTimestamp("2026-09-10T00:00:00.500Z", "x"); err != nil {
			t.Fatalf("合法时间不应报错：%v", err)
		}
		for _, value := range []any{"", "   ", nil, "not-a-time"} {
			if _, err := requiredChatTimestamp(value, "x"); err == nil {
				t.Fatalf("%v 应报错", value)
			}
		}
	})
	t.Run("storageWindowCutoffDate", func(t *testing.T) {
		cutoff, err := storageWindowCutoffDate(kitUpdatedAt, 30)
		if err != nil || cutoff != "2026-08-11" {
			t.Fatalf("cutoff = %q, %v", cutoff, err)
		}
		if _, err := storageWindowCutoffDate("bad", 1); err == nil {
			t.Fatalf("非法时间应报错")
		}
	})
	t.Run("titleFromContent", func(t *testing.T) {
		if got := titleFromContent("  hello\tworld\nnext "); got != "hello world next" {
			t.Fatalf("titleFromContent = %q", got)
		}
		if got := titleFromContent(strings.Repeat("长", 100)); len([]rune(got)) != 60 {
			t.Fatalf("超长标题应截断到 60：%d", len([]rune(got)))
		}
		if got := titleFromContent("\x01\x02"); got != "新对话" {
			t.Fatalf("空标题回退 = %q", got)
		}
	})
	t.Run("requiredAssistantStorageReservation", func(t *testing.T) {
		reserved, err := requiredAssistantStorageReservation(int64(ChatAssistantStorageReservationBytes))
		if err != nil || reserved != ChatAssistantStorageReservationBytes {
			t.Fatalf("预留 = %d, %v", reserved, err)
		}
		if _, err := requiredAssistantStorageReservation(int64(1)); err == nil {
			t.Fatalf("预留不一致应报错")
		}
	})
	t.Run("textOf/optionalText/numberOf", func(t *testing.T) {
		stamp, _ := time.Parse(time.RFC3339, "2026-09-10T08:00:00+08:00")
		if got := textOf(stamp); got != "2026-09-10T00:00:00.000Z" {
			t.Fatalf("textOf(time) = %q", got)
		}
		if got := textOf([]byte("raw")); got != "raw" {
			t.Fatalf("textOf([]byte) = %q", got)
		}
		if got := textOf(int64(7)); got != "7" {
			t.Fatalf("textOf(int64) = %q", got)
		}
		if got := optionalText("   "); got != "" {
			t.Fatalf("optionalText 空白 = %q", got)
		}
		if got := numberOf(nil); got != 0 {
			t.Fatalf("numberOf(nil) = %v", got)
		}
		if got := numberOf([]byte("3.5")); got != 3.5 {
			t.Fatalf("numberOf([]byte) = %v", got)
		}
		if got := numberOf("abc"); got != 0 {
			t.Fatalf("numberOf 非法 = %v", got)
		}
		if !containsKey(map[string][2]float64{"k": {}}, "k") || containsKey(map[string][2]float64{}, "k") {
			t.Fatalf("containsKey 语义错误")
		}
		if got := ISOOf(kitNow()); got != kitUpdatedAt {
			t.Fatalf("ISOOf = %q", got)
		}
		if got := fmt.Sprintf("%s", filepathFromSlash("a/b")); os.PathSeparator == '\\' && got != `a\b` {
			t.Fatalf("filepathFromSlash = %q", got)
		}
	})
}

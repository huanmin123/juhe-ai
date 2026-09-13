package retention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// woSettleDB 记录结算调用的 DbService 桩。
type woSettleDB struct {
	settlement CodexContextSettlement
	err        error
}

func (d *woSettleDB) CleanupChatRetention(context.Context, ChatRetentionInput) (*ChatRetentionResult, error) {
	return nil, errors.New("not implemented")
}
func (d *woSettleDB) CleanupExpiredSystemSessions(context.Context, string, int) (int64, error) {
	return 0, errors.New("not implemented")
}
func (d *woSettleDB) CleanupExpiredCodexContextStates(context.Context, string, int) (*CodexContextExpiredCleanup, error) {
	return nil, errors.New("not implemented")
}
func (d *woSettleDB) CleanupExpiredDeletedAccounts(context.Context) (*ExpiredDeletedAccountSummary, error) {
	return nil, errors.New("not implemented")
}
func (d *woSettleDB) SettleCodexContextStorageCleanup(_ context.Context, settlement CodexContextSettlement) (CodexContextSettlementResult, error) {
	d.settlement = settlement
	return CodexContextSettlementResult{}, d.err
}

// woTestDbService 的其余方法桩通过内嵌补齐。
func TestCodexContextStorageProcessorLifecycle(t *testing.T) {
	root := t.TempDir()
	// 三个键：已存在文件、缺失文件、重复键；缺失仍算成功。
	key1 := filepath.Join("2026", "09", "state-1.json")
	if err := os.MkdirAll(filepath.Join(root, "2026", "09"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, key1), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := &woSettleDB{}
	processor := NewCodexContextStorageProcessor(root, db, nil)
	deleted, err := processor.ProcessBatch(context.Background(), []string{key1, "2026/09/missing.json", key1})
	if err != nil || deleted != 1 {
		t.Fatalf("实际删除计数应为 1（缺失文件计入成功但不计删除）: %d %v", deleted, err)
	}
	if len(db.settlement.SucceededStorageKeys) != 2 {
		t.Fatalf("结算应收到 2 个成功键（含缺失文件）: %#v", db.settlement)
	}
	if _, err := os.Stat(filepath.Join(root, key1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("文件应被删除: %v", err)
	}
}

func TestCodexContextStorageProcessorGuards(t *testing.T) {
	var nilProcessor *CodexContextStorageProcessor
	if _, err := nilProcessor.ProcessBatch(context.Background(), nil); err == nil {
		t.Fatalf("nil 处理器应报错")
	}
	empty := &CodexContextStorageProcessor{}
	if _, err := empty.ProcessBatch(context.Background(), nil); err == nil {
		t.Fatalf("缺删除器应报错")
	}
	// DB 缺失时回退 missingDbService，结算报错并保留原始计数。
	root := t.TempDir()
	processor := &CodexContextStorageProcessor{Deleter: NewFilesystemKeyDeleter(root)}
	if _, err := processor.ProcessBatch(context.Background(), nil); err == nil {
		t.Fatalf("缺 DB 服务应返回结算错误")
	}
	// 数据库结算失败时错误上抛。
	failing := &woSettleDB{err: errors.New("settle down")}
	withDB := NewCodexContextStorageProcessor(root, failing, nil)
	if _, err := withDB.ProcessBatch(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "settle down") {
		t.Fatalf("结算失败应上抛: %v", err)
	}
	// 取消的 context 应在结算成功后中止。
	okDB := &woSettleDB{}
	okProcessor := NewCodexContextStorageProcessor(root, okDB, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := okProcessor.ProcessBatch(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 context 应上抛: %v", err)
	}
}

func TestMissingCleanerStubsFailClosed(t *testing.T) {
	if _, err := (missingNonBusinessData{}).CleanupBefore(context.Background(), "cutoff", 10); err == nil {
		t.Fatalf("缺 non-business 清理器应报错")
	}
	if _, err := (missingPublicApiLogs{}).CleanupBefore(context.Background(), "cutoff", 10); err == nil {
		t.Fatalf("缺 public API 日志清理器应报错")
	}
	if _, err := (missingUsageRecords{}).CleanupProcessedBefore(context.Background(), "cutoff", 10); err == nil {
		t.Fatalf("缺 usage records 清理器应报错")
	}
}

func TestCodexContextPathValidation(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveCodexContextStorageCleanupPath(root, "../escape.json"); err == nil {
		t.Fatalf("路径穿越应报错")
	}
	path, err := ResolveCodexContextStorageCleanupPath(root, "\\2026\\09\\state.json")
	if err != nil {
		t.Fatalf("反斜杠应归一化: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Clean(root)) {
		t.Fatalf("解析路径应位于根目录下: %s", path)
	}
}

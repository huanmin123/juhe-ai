package main

// codex storage 清理的 segments 根回归（storageKey 根错位修复）：
// storageKey 是相对 codex-context segments 根（JUHE_AI_CODEX_CONTEXT_ROOT）
// 的相对路径（gateway gatewaycodex.SegmentStorageKey 的
// sessions/<safe>/segments/<hour>.json.gz 形态）。修复前
// codexStorageProcessor 误用 store.ShardRoot（state-shards 根）当删除根，
// 而「文件不存在=成功」的结算语义让每次删除都按成功落账——segment 文件
// 永不清理（存储泄漏）。本测试锁定：删除按 segmentRoot 解析、真实删文件、
// 不存在=成功 语义保持。

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/cleanuprepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// codexSegmentRootFakeDb 只结算不触库：本测试隔离文件删除根语义，DB 结算
// 面由 familyDbService / cleanuprepo 自身覆盖。
type codexSegmentRootFakeDb struct {
	settlement retention.CodexContextSettlement
}

func (f *codexSegmentRootFakeDb) CleanupChatRetention(context.Context, retention.ChatRetentionInput) (*retention.ChatRetentionResult, error) {
	return &retention.ChatRetentionResult{}, nil
}

func (f *codexSegmentRootFakeDb) CleanupExpiredSystemSessions(context.Context, string, int) (int64, error) {
	return 0, nil
}

func (f *codexSegmentRootFakeDb) CleanupExpiredCodexContextStates(context.Context, string, int) (*retention.CodexContextExpiredCleanup, error) {
	return &retention.CodexContextExpiredCleanup{}, nil
}

func (f *codexSegmentRootFakeDb) SettleCodexContextStorageCleanup(_ context.Context, settlement retention.CodexContextSettlement) (retention.CodexContextSettlementResult, error) {
	f.settlement = settlement
	return retention.CodexContextSettlementResult{}, nil
}

func (f *codexSegmentRootFakeDb) CleanupExpiredDeletedAccounts(context.Context) (*retention.ExpiredDeletedAccountSummary, error) {
	return &retention.ExpiredDeletedAccountSummary{}, nil
}

func TestCodexStorageProcessorDeletesUnderSegmentsRoot(t *testing.T) {
	root := t.TempDir()
	segmentsRoot := filepath.Join(root, "codex-context")
	// 故意与 segments 根不同源的 state-shards 根：修复前误用它当删除根时，
	// 文件解析 miss → deleted=0 → 本断言红。
	shardRoot := filepath.Join(root, "codex-shards")
	storageKey := "sessions/safe-abc/segments/2026093010.json.gz"
	target := filepath.Join(segmentsRoot, filepath.FromSlash(storageKey))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("建 segments 目录: %v", err)
	}
	if err := os.WriteFile(target, []byte("segment-payload"), 0o644); err != nil {
		t.Fatalf("写 segment 文件: %v", err)
	}
	db := &codexSegmentRootFakeDb{}
	processor := &codexStorageProcessor{
		db:          db,
		store:       &cleanuprepo.CodexContextStore{ShardRoot: shardRoot, ShardCount: 1},
		segmentRoot: segmentsRoot,
		logger:      slog.Default(),
	}
	deleted, err := processor.ProcessBatch(context.Background(), []string{storageKey})
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("segments 根下的 segment 文件必须被删除（根错位会使删除永远 miss），deleted=%d", deleted)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("segment 文件应已删除，Lstat err=%v", statErr)
	}
	if len(db.settlement.SucceededStorageKeys) != 1 || db.settlement.SucceededStorageKeys[0] != storageKey {
		t.Fatalf("结算成功键不符: %+v", db.settlement)
	}

	// 「不存在=成功」语义保持：重放同一 key 不报错、不计删除、仍结算成功键。
	deleted, err = processor.ProcessBatch(context.Background(), []string{storageKey})
	if err != nil {
		t.Fatalf("重放缺失 key 必须按成功结算而非错误: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("缺失文件不得计为已删除: %d", deleted)
	}
	if len(db.settlement.SucceededStorageKeys) != 1 || db.settlement.SucceededStorageKeys[0] != storageKey {
		t.Fatalf("重放结算成功键不符: %+v", db.settlement)
	}
}

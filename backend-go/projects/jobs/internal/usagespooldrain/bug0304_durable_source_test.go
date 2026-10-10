package usagespooldrain

// BUG-0304 回归测试：drain 与真实 usagewriter.Writer 的 durable 交接契约
// ——队列满且溢出落盘失败时保留 durable 源文件（不删除、writer 零终态计
// 数）；障碍解除后重试恢复（记录被接受、源文件删除、落库证实）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// bug0304StubShardStore 是 usagewriter.ShardStore 的内存替身：接收证明走
// writer Runtime 计数（契约许可的计数断言方式），不做真实分片落库。
type bug0304StubShardStore struct {
	mu   sync.Mutex
	rows int
}

func (s *bug0304StubShardStore) WriteBatch(_ usagewriter.Ctx, plan usagewriter.WritePlan) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows += len(plan.ShardEntries)
	return len(plan.ShardEntries), nil
}

func (s *bug0304StubShardStore) totalRows() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows
}

// TestBug0304DrainKeepsSourceOnOverflowPersistFailureThenReplays 端到端
// （真实 Writer + 真实 FileOverflowSpool + 真实 Drainer）：
//
//	阶段 1 失败保源：writer 队列（QueueMaxItems=1）被首条 Enqueue 占满，溢
//	  出 spool 目录指向已存在普通文件（Persist 必败，同 usagewriter
//	  wb_overflow_pgfastfail_test.go 手法）→ DrainOnce 返回错误、
//	  processed == 0、源文件仍存在、writer 丢弃计数为 0；
//	阶段 2 重试恢复：移除障碍文件（溢出目录恢复可用）并腾空队列后再跑
//	  DrainOnce → 返回 nil、源文件被删除，记录经 writer 落库证实已接收。
func TestBug0304DrainKeepsSourceOnOverflowPersistFailureThenReplays(t *testing.T) {
	root := t.TempDir()
	spoolDirectory := filepath.Join(root, "usage-record-spool")
	blocker := filepath.Join(root, "overflow-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &bug0304StubShardStore{}
	writer := usagewriter.NewWriter(usagewriter.Config{
		QueueMaxItems: 1, QueueMaxBytes: 64 * 1024, BatchSize: 8,
		FlushIntervalMs: 60_000, ShardRoot: filepath.Join(root, "usage-shards"),
	}, store, nil,
		usagewriter.WithOverflowSpool(usagewriter.NewFileOverflowSpool(blocker)))
	// 不 Start：无后台 flush，队列满状态由本测试显式驱动（writer.Drain）。

	// 阶段 1：失败保源。首条直接 Enqueue 占满队列。
	if err := writer.Enqueue(context.Background(), baseRecord("usage_20260102_s00_1767225600000_a1")); err != nil {
		t.Fatal(err)
	}
	record := baseRecord("usage_20260102_s00_1767225600000_a2")
	sourcePath := gatewaySpoolFile(t, spoolDirectory,
		"1767225600000-1000-00000000-0000-4000-8000-0000000000a2", record)
	drainer := newTestDrainer(spoolDirectory, writer)

	processed, err := drainer.DrainOnce(context.Background())
	if err == nil {
		t.Fatal("溢出落盘失败时 DrainOnce 必须返回错误")
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("durable 源文件必须保留待重试: %v", err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedDispatchCount != 0 || runtime.DroppedOverflowCount != 0 {
		t.Fatalf("溢出落盘失败不得终态计数（记录将由源文件重试）: %+v", runtime)
	}

	// 阶段 2：重试恢复。移除障碍文件让溢出目录可用，并腾空队列。
	writer.Drain()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	processed, err = drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("障碍解除后重试必须成功: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("重试成功后源文件必须删除: %v", err)
	}
	// 记录已被 writer 接受：两条全部落库（Runtime 计数 + stub store 落库行
	// 数；端到端落库断言的计数版，参照 drain_test.go
	// TestDrainIntoWriterEndToEnd）。
	writer.Drain()
	runtime = writer.Runtime()
	if runtime.HandledRecords != 2 || runtime.WrittenRecords != 2 {
		t.Fatalf("两条记录必须全部落库: %+v", runtime)
	}
	if runtime.DroppedCount != 0 {
		t.Fatalf("全程不得有丢弃: %+v", runtime)
	}
	if rows := store.totalRows(); rows != 2 {
		t.Fatalf("store rows = %d, want 2", rows)
	}
}

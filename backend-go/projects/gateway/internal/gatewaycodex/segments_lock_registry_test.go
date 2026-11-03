package gatewaycodex

// 写锁注册表生命周期测试（2026-11-03 内存审查修复）：SegmentStore 的
// keyWriteLock 注册表按 refs 引用计数，最后一个引用释放时删除条目，避免
// 「会话 × 小时」键空间随运行时长线性增长。同时必须证明删除不会破坏互斥：
// 同一个 key 的并发写入必须始终通过同一把锁串行（否则 append 偏移互相覆盖）。

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func lockRegistrySize(store *SegmentStore) int {
	store.locksMu.Lock()
	defer store.locksMu.Unlock()
	return len(store.locks)
}

// TestSegmentWriteLockRegistryDrained 单一 key 反复写入后注册表必须回到空。
// 旧实现（无删除）在这里会残留 1 条并随不同 key 线性累积。
func TestSegmentWriteLockRegistryDrained(t *testing.T) {
	store, err := NewSegmentStore(SegmentStoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.UnixMilli(1_800_000_000_000)
	for index := 0; index < 20; index++ {
		if _, err := store.WriteSegmentPayload(ctx, "session-drain", map[string]any{"i": index}, now); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
		if remaining := lockRegistrySize(store); remaining != 0 {
			t.Fatalf("after write %d: locks remaining=%d want=0", index, remaining)
		}
	}
}

// TestSegmentWriteLockRegistryManyKeys 多键写入后注册表同样必须清空。
func TestSegmentWriteLockRegistryManyKeys(t *testing.T) {
	store, err := NewSegmentStore(SegmentStoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.UnixMilli(1_800_000_000_000)
	for index := 0; index < 50; index++ {
		session := fmt.Sprintf("session-%02d", index)
		if _, err := store.WriteSegmentPayload(ctx, session, map[string]any{"i": index}, now); err != nil {
			t.Fatalf("write %s: %v", session, err)
		}
	}
	if remaining := lockRegistrySize(store); remaining != 0 {
		t.Fatalf("locks remaining=%d want=0（注册表必须随引用归零清理）", remaining)
	}
}

// TestSegmentWriteLockMutualExclusionUnderChurn 并发压测：同一 key 上并发写入
// 必须全部串行。refs 归零删除若存在竞态（等待者仍持旧对象 + 新调用方拿到新
// 对象），两把锁会同时写同一文件，append 偏移互相覆盖，回读校验必然失败。
// 用 -race 运行以覆盖数据竞争。
func TestSegmentWriteLockMutualExclusionUnderChurn(t *testing.T) {
	store, err := NewSegmentStore(SegmentStoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.UnixMilli(1_800_000_000_000)
	const writers = 16
	const perWriter = 8

	var waitGroup sync.WaitGroup
	type result struct {
		writer int
		round  int
		ref    CodexContextPayloadReference
		err    error
	}
	results := make(chan result, writers*perWriter)
	for writer := 0; writer < writers; writer++ {
		waitGroup.Add(1)
		go func(writer int) {
			defer waitGroup.Done()
			for round := 0; round < perWriter; round++ {
				ref, err := store.WriteSegmentPayload(ctx, "session-shared", map[string]any{
					"writer": writer,
					"round":  round,
				}, now)
				results <- result{writer: writer, round: round, ref: ref, err: err}
			}
		}(writer)
	}
	waitGroup.Wait()
	close(results)

	collected := make([]result, 0, writers*perWriter)
	for item := range results {
		if item.err != nil {
			t.Fatalf("concurrent write: %v", item.err)
		}
		collected = append(collected, item)
	}
	if len(collected) != writers*perWriter {
		t.Fatalf("writes=%d want=%d", len(collected), writers*perWriter)
	}

	// 逐条回读：偏移/SHA256/大小任一被并发覆盖，ReadSegmentPayload 会报错或
	// 内容不符。
	for _, item := range collected {
		raw, err := store.ReadSegmentPayload(item.ref)
		if err != nil {
			t.Fatalf("read back writer=%d round=%d: %v（并发写入偏移被覆盖）", item.writer, item.round, err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode writer=%d round=%d: %v", item.writer, item.round, err)
		}
		if gotWriter, _ := payload["writer"].(float64); int(gotWriter) != item.writer {
			t.Fatalf("payload writer=%v want=%d（读到其他写入者的数据）", payload["writer"], item.writer)
		}
		if gotRound, _ := payload["round"].(float64); int(gotRound) != item.round {
			t.Fatalf("payload round=%v want=%d", payload["round"], item.round)
		}
	}
	if remaining := lockRegistrySize(store); remaining != 0 {
		t.Fatalf("locks remaining=%d want=0", remaining)
	}
}

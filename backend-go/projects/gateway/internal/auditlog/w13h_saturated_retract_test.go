package auditlog

import (
	"context"
	"errors"
	"testing"
	"time"
)

// w13h 波次测试（2026-10-28，W13 残留①②收口）：生产复核确认空壳仍以
// 个位数/小时新增，两条已修复失败分支的撤回命令走的是与采集共用的饱和
// 队列——队列满与存储故障同窗，撤回命令被丢弃即空壳残留（残留②）；panic
// 分支则从不派发撤回（残留①）。收口契约：
// ① persistOne panic 臂必须派发信封撤回（lifecycle 守卫使无行/终态行为
//    无害 no-op）；
// ② 失败分支在队列饱和时必须在 worker 协程上内联执行撤回，不再依赖可能
//    被丢弃的入队路径。

// TestW13HQueueFullFailureBranchRetractsInline：队列塞满后同步调用
// persistOne（复现「终态写入失败 + 撤回命令无法入队」的生产饱和形态），
// 撤回必须内联执行，不得丢弃。
func TestW13HQueueFullFailureBranchRetractsInline(t *testing.T) {
	fake := &fakeStore{renewOK: true, persistErr: errors.New("获取 F3 blob 生命周期锁失败: context deadline exceeded")}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "w13h-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	// NewProducer 不启动 worker（start.Do 在首次 Capture/Retract 才触发），
	// 直接把采集队列塞满即可获得确定性的饱和现场。
	for i := 0; i < producerQueueCapacity; i++ {
		select {
		case producer.queue <- queuedAudit{}:
		default:
			t.Fatalf("队列容量守恒被破坏：第 %d 条即满（capacity=%d）", i, producerQueueCapacity)
		}
	}

	producer.persistOne(producerTestInput("w13h-saturated"))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.retractCalls != 1 {
		t.Fatalf("饱和队列下失败分支必须内联撤回一次，实际 retractCalls=%d ids=%v", fake.retractCalls, fake.retractIDs)
	}
	if len(fake.retractIDs) != 1 || fake.retractIDs[0] != "w13h-saturated" {
		t.Fatalf("内联撤回 id = %v, want [w13h-saturated]", fake.retractIDs)
	}
}

// w13hPanicPersistStore 模拟 Persist 内部 panic（信封可能已写）。
type w13hPanicPersistStore struct {
	fakeStore
}

func (s *w13hPanicPersistStore) Persist(context.Context, OwnerLease, AuditLogInput) (PersistResult, error) {
	panic("w13h persist boom")
}

// TestW13HPersistPanicRetractsEnvelope：panic 分支必须派发信封撤回，且
// panic 隔离不吞撤回、不拖垮 worker。
func TestW13HPersistPanicRetractsEnvelope(t *testing.T) {
	fake := &w13hPanicPersistStore{fakeStore{renewOK: true}}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "w13h-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	producer.persistOne(producerTestInput("w13h-panic"))

	// 撤回已入队；kick 一次正常采集启动 worker 池，等撤回被消费落账。
	producer.Capture(producerTestInput("w13h-kick"))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		retracts := append([]string(nil), fake.retractIDs...)
		fake.mu.Unlock()
		if len(retracts) >= 1 {
			if retracts[0] != "w13h-panic" {
				t.Fatalf("panic 臂撤回 id = %v", retracts)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("panic 分支必须派发信封撤回，实际未执行任何撤回")
}

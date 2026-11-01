package auditlog

// DD5-E1 波次测试（2026-11-01，R5 confirmed B 类即修）：CaptureDeferred 的丢
// 弃臂跑在请求准入协程上（DispatchAuditLog 同步直调），此前队列槽满时经
// retractBestEffort 在调用方协程内联 retractOne（最长 retractWaitBudget 15s
// 的 settle 等待 + DELETE 重试）——F3 存储慢/队列饱和窗内每个新请求的准入
// 面都被拉长。收口契约：
// ① 丢弃臂在队列槽满时返回时间不随撤回预算增长（不内联撤回）；
// ② 挂起的撤回意图由 worker 消费间隙补派，撤回补偿语义仍最终送达。

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestDD5E1QueueFullDropArmReturnsWithoutRetractBudgetBlocking 钉住 ①：信封
// 在途（persist 停在续租门内）+ 队列槽满的丢弃现场，丢弃臂必须瞬时返回——
// 修复前此路径内联 retractOne，会等到 retractWaitBudget 预算耗尽才放弃。
func TestDD5E1QueueFullDropArmReturnsWithoutRetractBudgetBlocking(t *testing.T) {
	const envID = "dd5e1-inflight-env"
	const fillerEstimate = 256
	previousBudget := retractWaitBudget
	retractWaitBudget = 2 * time.Second
	t.Cleanup(func() { retractWaitBudget = previousBudget })

	inner := &fakeStore{renewOK: true}
	gate := make(chan struct{})
	store := &gatedStore{fakeStore: inner, gate: gate}
	t.Cleanup(func() { close(gate) })
	logger := &fakeProducerLogger{warns: &[]string{}}
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd5e1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, logger)

	// 信封 + 垫底把 4 个 worker 全部停进续租门：信封在途未 settle，内联撤回
	// 的 waitForEnvelopeSettle 必然等满预算（修复前形态）。
	producer.Capture(dd1Envelope(envID))
	for index := 0; index < producerWorkers-1; index++ {
		producer.Capture(producerTestInput(fmt.Sprintf("dd5e1-park-%d", index)))
	}
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() != producerWorkers && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != producerWorkers {
		t.Fatalf("仅 %d 个 worker 停进续租门，want %d", store.entered.Load(), producerWorkers)
	}
	if !producer.envelopeInFlight(envID) {
		t.Fatal("信封必须在途（内联撤回等待面成立的前置）")
	}

	// 队列槽塞满：丢弃臂的 tryEnqueueRetract 必然失败（槽满臂）。
	for i := 0; i < producerQueueCapacity; i++ {
		select {
		case producer.queue <- queuedAudit{}:
		default:
			t.Fatalf("队列容量守恒被破坏：第 %d 条即满（capacity=%d）", i, producerQueueCapacity)
		}
	}

	startedAt := time.Now()
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   fillerEstimate,
		EnvelopeID: envID,
		TraceID:    "trace-dd5e1",
		AuditLogID: envID,
		Decode: func() (AuditLogInput, error) {
			return producerTestInput(envID), nil
		},
	})
	elapsed := time.Since(startedAt)
	if elapsed >= retractWaitBudget/2 {
		t.Fatalf("丢弃臂返回耗时 %v ≥ 撤回预算一半 %v：不得在调用方协程内联执行撤回", elapsed, retractWaitBudget/2)
	}
	if dropped := producer.DroppedTotal(); dropped != 1 {
		t.Fatalf("本条采集必须被丢弃一次，实际 %d", dropped)
	}
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("丢弃臂返回时不得已执行删除（内联证据）: %v", got)
	}
	// DD-D1 既有语义下丢弃臂会注销本采集登记的在途条目（与信封采集同 id），
	// 非本次修复对象；「未内联」以时间断言与挂起意图登记为证。
	producer.envelopeMu.Lock()
	_, intentPending := producer.droppedRetractIntents[envID]
	producer.envelopeMu.Unlock()
	if !intentPending {
		t.Fatal("槽满丢弃臂必须登记挂起撤回意图（不得内联执行撤回）")
	}
}

// TestDD5E1QueueFullDropArmIntentEventuallyRedelivered 钉住 ②：槽满丢弃臂
// 登记的挂起撤回意图，由 worker 消费间隙转储为撤回命令并执行——丢弃终态后
// 同请求已落库的壳行仍被撤回送达，补偿语义不丢。
func TestDD5E1QueueFullDropArmIntentEventuallyRedelivered(t *testing.T) {
	const envID = "dd5e1-redelivery-env"
	const fillerEstimate = 256

	inner := &fakeStore{renewOK: true}
	gate := make(chan struct{})
	store := &gatedStore{fakeStore: inner, gate: gate}
	var releaseGate sync.Once
	t.Cleanup(func() { releaseGate.Do(func() { close(gate) }) })
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd5e1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	// 信封 + 垫底停满 worker，队列随后确定性塞满。
	producer.Capture(dd1Envelope(envID))
	for index := 0; index < producerWorkers-1; index++ {
		producer.Capture(producerTestInput(fmt.Sprintf("dd5e1r-park-%d", index)))
	}
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() != producerWorkers && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != producerWorkers {
		t.Fatalf("仅 %d 个 worker 停进续租门，want %d", store.entered.Load(), producerWorkers)
	}
	for i := 0; i < producerQueueCapacity; i++ {
		select {
		case producer.queue <- queuedAudit{}:
		default:
			t.Fatalf("队列容量守恒被破坏：第 %d 条即满（capacity=%d）", i, producerQueueCapacity)
		}
	}

	// 丢弃臂（槽满）：撤回命令无法入队 → 意图挂起。
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   fillerEstimate,
		EnvelopeID: envID,
		TraceID:    "trace-dd5e1r",
		AuditLogID: envID,
		Decode: func() (AuditLogInput, error) {
			return producerTestInput(envID), nil
		},
	})
	producer.envelopeMu.Lock()
	_, intentPending := producer.droppedRetractIntents[envID]
	producer.envelopeMu.Unlock()
	if !intentPending {
		t.Fatal("槽满丢弃臂必须登记挂起撤回意图")
	}
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("意图挂起阶段不得执行删除: %v", got)
	}

	// 放行续租门：worker 恢复消费队列，每条消费后 drain 转储意图 → 撤回命令
	// 入队 → retractOne 执行删除。
	releaseGate.Do(func() { close(gate) })
	deadline = time.Now().Add(10 * time.Second)
	for {
		got := store.retractions()
		if len(got) >= 1 {
			if got[0] != envID {
				t.Fatalf("补派撤回 id = %v, want [%s]", got, envID)
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("挂起的丢弃臂撤回意图必须最终送达，实际 retractions=%v", store.retractions())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

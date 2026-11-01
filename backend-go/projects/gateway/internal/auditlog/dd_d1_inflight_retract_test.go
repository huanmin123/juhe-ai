package auditlog

// DD-D1 波次测试（2026-10-31，W13 第四产生面收口）：in_progress 信封经有界
// 队列异步落库，撤回命令走同一队列、4 worker 并发消费无按 ID 顺序——存储慢
// 窗下信封 persist 卡在续租/持久化时，Cancel 派发的撤回被空闲 worker 立即执
// 行 → DELETE 无行 no-op → 信封随后提交 → 无 finalize 无撤回 → 永久空壳。
// 收口契约：信封 id 在入队前登记在途集合、persistOne settle（成功/失败/
// panic 三臂）注销；撤回命令对在途 id 延迟到 settle 后生效，15s 总预算耗尽
// 仍在途则放弃本次撤回（此时 DELETE 必然 no-op，执行只会更差）。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// dd1GatedPersistStore 把 Persist 挂在 gate 上（信封在途注入）：进入即计数，
// 放行前不返回，复现「信封 persist 卡在存储慢窗」的在途现场。
type dd1GatedPersistStore struct {
	fakeStore
	gate    chan struct{}
	entered atomic.Int32
}

func (s *dd1GatedPersistStore) Persist(ctx context.Context, lease OwnerLease, input AuditLogInput) (PersistResult, error) {
	s.entered.Add(1)
	<-s.gate
	return s.fakeStore.Persist(ctx, lease, input)
}

func dd1Envelope(id string) AuditLogInput {
	envelope := producerTestInput(id)
	envelope.LifecycleStatus = LifecycleInProgress
	return envelope
}

// TestDD1RetractDeferredWhileEnvelopeInFlight 锁定核心时序：信封 persist 在
// 途时撤回不得执行删除；settle 后撤回才真正生效（删除可见的 in_progress 行）。
func TestDD1RetractDeferredWhileEnvelopeInFlight(t *testing.T) {
	store := &dd1GatedPersistStore{fakeStore: fakeStore{renewOK: true}, gate: make(chan struct{})}
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	producer.Capture(dd1Envelope("dd1-envelope"))

	// 等 worker 进入被挂住的 Persist（信封进入在途窗口）。
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != 1 {
		t.Fatalf("信封 persist 未进入在途窗口: entered=%d", store.entered.Load())
	}
	if !producer.envelopeInFlight("dd1-envelope") {
		t.Fatal("已入队未 settle 的信封必须登记在途集合")
	}

	producer.Retract("dd1-envelope")
	// 在途窗口内撤回不得执行删除：此刻 DELETE 必然 no-op，执行即空壳根源。
	time.Sleep(150 * time.Millisecond)
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("信封 settle 前撤回不得执行删除: %v", got)
	}

	// settle：信封 persist 完成（注销在途），撤回随后执行。
	close(store.gate)
	deadline = time.Now().Add(5 * time.Second)
	for {
		if got := store.retractions(); len(got) == 1 {
			if got[0] != "dd1-envelope" {
				t.Fatalf("settle 后撤回 id = %v, want [dd1-envelope]", got)
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("settle 后撤回必须生效，实际 retractions=%v entered=%d", store.retractions(), store.entered.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDD1DeferredEnvelopeRegistersInFlight 锁定 DD-D4 延迟采集入口的同一
// 契约：EnvelopeID 在入队（worker 解码前）即登记，撤回同样延迟到 settle。
func TestDD1DeferredEnvelopeRegistersInFlight(t *testing.T) {
	store := &dd1GatedPersistStore{fakeStore: fakeStore{renewOK: true}, gate: make(chan struct{})}
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	producer.CaptureDeferred(DeferredCapture{
		EnvelopeID: "dd1-deferred-env",
		TraceID:    "trace-dd1-deferred",
		AuditLogID: "dd1-deferred-env",
		Decode: func() (AuditLogInput, error) {
			return dd1Envelope("dd1-deferred-env"), nil
		},
	})

	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != 1 {
		t.Fatalf("延迟信封 persist 未进入在途窗口: entered=%d", store.entered.Load())
	}
	if !producer.envelopeInFlight("dd1-deferred-env") {
		t.Fatal("延迟采集的信封必须在入队时（而非 worker 解码时）登记在途")
	}

	producer.Retract("dd1-deferred-env")
	time.Sleep(150 * time.Millisecond)
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("延迟采集信封 settle 前撤回不得执行删除: %v", got)
	}

	close(store.gate)
	deadline = time.Now().Add(5 * time.Second)
	for {
		if got := store.retractions(); len(got) == 1 {
			if got[0] != "dd1-deferred-env" {
				t.Fatalf("settle 后撤回 id = %v, want [dd1-deferred-env]", got)
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("settle 后撤回必须生效，实际 retractions=%v", store.retractions())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDD1EnvelopeRegistrationReleasedOnDropAndSettle 锁定集合有界性：入队
// 失败立即注销（登记先行但不泄漏）；settle（本测试走成功臂）注销；终态采集
// 从不登记。
func TestDD1EnvelopeRegistrationReleasedOnDropAndSettle(t *testing.T) {
	// (a) 队列饱和 → Capture 准入失败 → 在途登记不残留。全部 worker 先停进
	// gated renewal，队列在无消费窗口内保持满，准入失败即确定。
	inner := &fakeStore{renewOK: true}
	gate := make(chan struct{})
	parked := &gatedStore{fakeStore: inner, gate: gate}
	producer := NewProducer(parked, StaticLease(OwnerLease{OwnerID: "dd1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	for i := 0; i < producerWorkers; i++ {
		producer.Capture(producerTestInput("dd1-park"))
	}
	deadline := time.Now().Add(5 * time.Second)
	for parked.entered.Load() < producerWorkers && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if parked.entered.Load() != producerWorkers {
		t.Fatalf("仅 %d 个 worker 停进续租门，want %d", parked.entered.Load(), producerWorkers)
	}
	for i := 0; i < producerQueueCapacity; i++ {
		select {
		case producer.queue <- queuedAudit{}:
		default:
			t.Fatalf("队列容量守恒被破坏：第 %d 条即满（capacity=%d）", i, producerQueueCapacity)
		}
	}
	producer.Capture(dd1Envelope("dd1-dropped-env"))
	if producer.envelopeInFlight("dd1-dropped-env") {
		t.Fatal("准入失败的信封不得残留在途登记")
	}
	close(gate)

	// (b) settle（成功臂）→ 注销。
	settled := &fakeStore{renewOK: true}
	settleProducer := NewProducer(settled, StaticLease(OwnerLease{OwnerID: "dd1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	settleProducer.Capture(dd1Envelope("dd1-settled-env"))
	settled.waitForCalls(t, 3) // renew + persist + hot search
	settleDeadline := time.Now().Add(5 * time.Second)
	for settleProducer.envelopeInFlight("dd1-settled-env") {
		if !time.Now().Before(settleDeadline) {
			t.Fatal("settle 后在途登记必须注销")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// (c) 终态采集从不参与在途集合。
	settleProducer.Capture(producerTestInput("dd1-finalized"))
	settled.waitForCalls(t, 6)
	if settleProducer.envelopeInFlight("dd1-finalized") {
		t.Fatal("终态采集不得登记在途集合")
	}
}

// TestDD1DeferredDecodeFailureUnregistersEnvelope 锁定 DD-RC-A1：延迟采集的
// Decode 失败臂在 persistOne 之前返回、persistOne 的 settle defer 不可达，
// 必须在返回前注销入队时登记的在途信封——否则该 id 永久留在在途集合，其
// 后所有 Retract 都会在 waitForEnvelopeSettle 空转到 15s 预算耗尽后放弃
// （撤回永不生效）。同时钉住该臂语义：直接丢弃，不派生撤回（Decode 失败
// 即输入损坏、信封行无从定位，撤回 DELETE 必然 no-op）。
func TestDD1DeferredDecodeFailureUnregistersEnvelope(t *testing.T) {
	logger := &fakeProducerLogger{warns: &[]string{}}
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "dd1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, logger)
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   256,
		EnvelopeID: "dd1-decode-boom-env",
		TraceID:    "trace-dd1-decode-boom",
		AuditLogID: "dd1-decode-boom-env",
		Decode: func() (AuditLogInput, error) {
			return AuditLogInput{}, errors.New("dd1 bridge boom")
		},
	})

	// Decode 失败臂的 settle 边界：预算释放归零即本条已结算。
	deadline := time.Now().Add(5 * time.Second)
	for producer.QueueBytes() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := producer.QueueBytes(); got != 0 {
		t.Fatalf("Decode 失败后队列预算必须释放，实际 %d", got)
	}
	if producer.envelopeInFlight("dd1-decode-boom-env") {
		t.Fatal("Decode 失败臂必须在返回前注销在途信封登记")
	}
	if got := fake.retractions(); len(got) != 0 {
		t.Fatalf("Decode 失败臂不得派生撤回（输入损坏无从定位信封行）: %v", got)
	}
	if _, persists, _ := fake.counts(); persists != 0 {
		t.Fatalf("Decode 失败臂不得触达持久化: %d", persists)
	}
	found := false
	for _, warn := range logger.snapshot() {
		if warn == "F3 审计采集 JSON 编码失败，丢弃本条采集" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Decode 失败臂必须保留丢弃诊断日志: %v", logger.snapshot())
	}

	// 注销生效的行为证据：同一 id 的撤回立即执行（不再被 waitForEnvelopeSettle
	// 拖到 15s 预算耗尽）；对无行 id 的 DELETE 是 lifecycle 守卫下的无害 no-op。
	producer.Retract("dd1-decode-boom-env")
	fake.waitForCalls(t, 2) // renew + retract
	if got := fake.retractions(); len(got) != 1 || got[0] != "dd1-decode-boom-env" {
		t.Fatalf("在途登记注销后撤回必须立即执行: %v", got)
	}
}

// TestDD1RetractStillWorksWhenNothingInFlight 锁定回归底线：无在途信封时
// 撤回立即执行（w13f 契约不被在途检查拖延）。
func TestDD1RetractStillWorksWhenNothingInFlight(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "dd1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	producer.Retract("dd1-unknown-env")
	fake.waitForCalls(t, 2) // renew + retract
	if got := fake.retractions(); len(got) != 1 || got[0] != "dd1-unknown-env" {
		t.Fatalf("无在途信封时撤回必须立即执行: %v", got)
	}
}

// dd1PanickingPersistStore 模拟 Persist panic（信封可能已写）。
type dd1PanickingPersistStore struct {
	fakeStore
}

func (s *dd1PanickingPersistStore) Persist(context.Context, OwnerLease, AuditLogInput) (PersistResult, error) {
	panic("dd1 persist boom")
}

// TestDD1PanicArmUnregistersBeforeRetract 锁定 panic 臂顺序：settle 注销先
// 于撤回派生——panic 后派生的撤回不再等待本条（已结束的）persist。
func TestDD1PanicArmUnregistersBeforeRetract(t *testing.T) {
	fake := &dd1PanickingPersistStore{fakeStore{renewOK: true}}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "dd1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	producer.Capture(dd1Envelope("dd1-panic-env"))
	// panic 臂：decode/persist panic → settle defer 注销 → 撤回派生（入队）。
	// kick 一次正常采集启动 worker 池已随 Capture 启动；等撤回落账。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := fake.retractions(); len(got) == 1 {
			if got[0] != "dd1-panic-env" {
				t.Fatalf("panic 臂撤回 id = %v", got)
			}
			if producer.envelopeInFlight("dd1-panic-env") {
				t.Fatal("panic settle 后在途登记必须已注销")
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("panic 臂必须派发撤回，实际 retractions=%v", fake.retractions())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// dd2G1WarnPresent 在诊断日志快照中查找指定告警（worker 协程写入，须走
// 互斥快照）。
func dd2G1WarnPresent(logger *fakeProducerLogger, want string) bool {
	for _, warn := range logger.snapshot() {
		if warn == want {
			return true
		}
	}
	return false
}

// TestDD2G1AbandonedRetractRedispatchedOnSuccessSettle 锁定 DD2-G1 settle
// 边缘组合臂：撤回在途等待 → 预算耗尽放弃（信封仍在途、无 DELETE，撤回
// 意图已登记）→ persist 成功 settle → 成功臂读到意图补派一次幂等撤回 →
// 无残留。修复前：放弃即终止，成功臂不派生撤回（needsRetract 仅失败/panic
// 臂），in_progress 壳行滞留至 retention。预算经 retractWaitBudget 测试缝
// 钉小（生产默认 15s 不变），复现「settle 落在撤回预算耗尽之后」的带内时序。
func TestDD2G1AbandonedRetractRedispatchedOnSuccessSettle(t *testing.T) {
	logger := &fakeProducerLogger{warns: &[]string{}}
	store := &dd1GatedPersistStore{fakeStore: fakeStore{renewOK: true}, gate: make(chan struct{})}
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd2g1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, logger)

	savedBudget := retractWaitBudget.Load()
	retractWaitBudget.Store(int64(100 * time.Millisecond))
	defer func() { retractWaitBudget.Store(savedBudget) }()

	producer.Capture(dd1Envelope("dd2g1-band-env"))
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != 1 {
		t.Fatalf("信封 persist 未进入在途窗口: entered=%d", store.entered.Load())
	}

	producer.Retract("dd2g1-band-env")
	// 等待者预算（100ms）耗尽放弃：以放弃诊断日志为确定性证据——日志在
	// 意图登记之后写出，出现即放弃臂已执行且意图已落位。
	abandonDeadline := time.Now().Add(5 * time.Second)
	for !dd2G1WarnPresent(logger, "F3 审计信封撤回等待 settle 超时，放弃本次撤回（已登记撤回意图，settle 后补派）") {
		if !time.Now().Before(abandonDeadline) {
			t.Fatalf("撤回等待者未在预算耗尽后放弃: %v", logger.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("预算耗尽放弃（settle 前）不得执行删除: %v", got)
	}

	// persist 成功 settle：成功臂读到撤回意图 → 补派撤回执行，壳行不残留。
	close(store.gate)
	deadline = time.Now().Add(5 * time.Second)
	for {
		got := store.retractions()
		if len(got) == 1 {
			if got[0] != "dd2g1-band-env" {
				t.Fatalf("补派撤回 id = %v, want [dd2g1-band-env]", got)
			}
			if producer.envelopeInFlight("dd2g1-band-env") {
				t.Fatal("补派撤回执行后在途登记必须已注销")
			}
			return
		}
		if len(got) > 1 {
			t.Fatalf("补派必须收敛为单次撤回: %v", got)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("成功 settle 后必须补派撤回，实际 retractions=%v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDD2G1SuccessSettleWithoutRetractIntentDoesNotRetract 对照组：无撤回
// 意图的成功 settle 不得派生撤回——补派严格以「settle 前登记的撤回意图」
// 为门，正常成功落库的信封不受影响（成功信封不被误撤）。
func TestDD2G1SuccessSettleWithoutRetractIntentDoesNotRetract(t *testing.T) {
	store := &dd1GatedPersistStore{fakeStore: fakeStore{renewOK: true}, gate: make(chan struct{})}
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd2g1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	producer.Capture(dd1Envelope("dd2g1-ctrl-env"))
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != 1 {
		t.Fatalf("信封 persist 未进入在途窗口: entered=%d", store.entered.Load())
	}

	// 成功 settle，全程无撤回命令。
	close(store.gate)
	deadline = time.Now().Add(5 * time.Second)
	for producer.envelopeInFlight("dd2g1-ctrl-env") {
		if !time.Now().Before(deadline) {
			t.Fatal("成功 settle 后在途登记必须注销")
		}
		time.Sleep(5 * time.Millisecond)
	}
	store.waitForCalls(t, 3)           // renew + persist + hot search（无第 4 个撤回调用）
	time.Sleep(200 * time.Millisecond) // 留出潜在误派发的观察窗
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("无撤回意图的成功 settle 不得派生撤回: %v", got)
	}
}

package auditlog

// DD3-C2/DD3-C4 波次测试（2026-10-31，R3 confirmed A 类即修；超限臂口径由
// DD4-RC1 于同日收口为三臂一致）：
// C2 —— CaptureDeferred 的三个丢弃臂（单条超限/字节预算满/入队失败）对被丢
//       条目的撤回目标 id（EnvelopeID，finalize 类回落 AuditLogID）补派
//       retractBestEffort：同请求信封已 settle 成壳则撤回执行删除（lifecycle
//       守卫 DELETE 命中壳行），未落库则撤回为无害 no-op。
// C4 —— Retract 三次入队全耗尽且信封在途时登记撤回意图，信封 settle（尤其
//       成功臂）读到意图补派一次幂等撤回——命令丢弃不再等价于撤回丢失。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// dd3c2SelectiveGateStore 只对指定 id 的 Persist 挂门（其余放行）：信封先
// 自由 settle 成壳，filler 采集停在门内让字节预算不释放——复现「预算满 +
// 信封已落库」的丢弃现场。
type dd3c2SelectiveGateStore struct {
	fakeStore
	release chan struct{}
	gateIDs map[string]bool
}

func (s *dd3c2SelectiveGateStore) Persist(ctx context.Context, lease OwnerLease, input AuditLogInput) (PersistResult, error) {
	if s.gateIDs[input.ID] {
		<-s.release
	}
	return s.fakeStore.Persist(ctx, lease, input)
}

func dd3c2AdmitFillers(t *testing.T, producer *Producer, prefix string, count, estimate int) {
	t.Helper()
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("%s-%d", prefix, index)
		producer.CaptureDeferred(DeferredCapture{
			Estimate:   estimate,
			TraceID:    "trace-" + id,
			AuditLogID: id,
			Decode: func() (AuditLogInput, error) {
				return producerTestInput(id), nil
			},
		})
	}
}

// TestDD3C2FinalizeDropBudgetFullRetractsSettledShell 锁定 C2 主臂：信封先
// settle 落库（in_progress 壳行已提交）→ finalize Deferred（与调用方
// chain_ports.DispatchAuditLog 同构：EnvelopeID 空、AuditLogID 与信封同 id）
// 因队列字节预算满被丢弃 → 丢弃臂补派撤回并执行 → 壳行不残留。
func TestDD3C2FinalizeDropBudgetFullRetractsSettledShell(t *testing.T) {
	const envID = "dd3c2-shell-env"
	const fillerEstimate = 1024
	previousBudget := producerQueueMaxBytes
	producerQueueMaxBytes = int64(3 * fillerEstimate)
	t.Cleanup(func() { producerQueueMaxBytes = previousBudget })

	store := &dd3c2SelectiveGateStore{
		fakeStore: fakeStore{renewOK: true},
		release:   make(chan struct{}),
		gateIDs:   map[string]bool{"dd3c2-filler-0": true, "dd3c2-filler-1": true, "dd3c2-filler-2": true},
	}
	t.Cleanup(func() { close(store.release) })
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd3c2-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	// 信封 settle 落库：壳行已提交、在途登记注销。
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   256,
		EnvelopeID: envID,
		TraceID:    "trace-dd3c2",
		AuditLogID: envID,
		Decode: func() (AuditLogInput, error) {
			envelope := producerTestInput(envID)
			envelope.LifecycleStatus = LifecycleInProgress
			return envelope, nil
		},
	})
	deadline := time.Now().Add(5 * time.Second)
	for producer.envelopeInFlight(envID) {
		if !time.Now().Before(deadline) {
			t.Fatal("信封 persist 未 settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if persisted := store.persistedInputs(); len(persisted) != 1 || persisted[0].ID != envID || persisted[0].LifecycleStatus != LifecycleInProgress {
		t.Fatalf("信封壳行必须已落库: %+v", persisted)
	}

	// 三条 filler 占满字节预算（persist 停在门内，预算不释放）。
	dd3c2AdmitFillers(t, producer, "dd3c2-filler", 3, fillerEstimate)
	if got := producer.QueueBytes(); got != int64(3*fillerEstimate) {
		t.Fatalf("预算必须被 filler 占满: %d", got)
	}

	// finalize Deferred 超预算 → 预算丢弃臂（无单条超限）。
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   fillerEstimate,
		TraceID:    "trace-dd3c2-finalize",
		AuditLogID: envID,
		Decode: func() (AuditLogInput, error) {
			return producerTestInput(envID), nil
		},
	})
	if dropped := producer.DroppedTotal(); dropped != 1 {
		t.Fatalf("finalize 条目必须被丢弃一次，实际 %d", dropped)
	}

	// 丢弃臂补派撤回并执行：壳行被 lifecycle 守卫 DELETE 命中，无残留。
	deadline = time.Now().Add(5 * time.Second)
	for {
		if got := store.retractions(); len(got) == 1 {
			if got[0] != envID {
				t.Fatalf("丢弃臂撤回 id = %v, want [%s]", got, envID)
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("finalize 丢弃后必须补派撤回，实际 retractions=%v", store.retractions())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDD3C2EnvelopeDropRetractNoOpHarmless 锁定 C2 副臂：信封条目自身在预
// 算臂被丢（同批丢弃、从未入队从未落库）→ 丢弃臂补派的撤回对无行 id 是
// lifecycle 守卫下的无害 no-op——不触达持久化、不悬挂、不残留登记。
func TestDD3C2EnvelopeDropRetractNoOpHarmless(t *testing.T) {
	const envID = "dd3c2-dropped-env"
	const fillerEstimate = 1024
	previousBudget := producerQueueMaxBytes
	producerQueueMaxBytes = int64(3 * fillerEstimate)
	t.Cleanup(func() { producerQueueMaxBytes = previousBudget })

	store := &dd3c2SelectiveGateStore{
		fakeStore: fakeStore{renewOK: true},
		release:   make(chan struct{}),
		gateIDs:   map[string]bool{"dd3c2b-filler-0": true, "dd3c2b-filler-1": true, "dd3c2b-filler-2": true},
	}
	t.Cleanup(func() { close(store.release) })
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd3c2-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	dd3c2AdmitFillers(t, producer, "dd3c2b-filler", 3, fillerEstimate)
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   fillerEstimate,
		EnvelopeID: envID,
		TraceID:    "trace-dd3c2b",
		AuditLogID: envID,
		Decode: func() (AuditLogInput, error) {
			return producerTestInput(envID), nil
		},
	})
	if dropped := producer.DroppedTotal(); dropped != 1 {
		t.Fatalf("信封条目必须被丢弃一次，实际 %d", dropped)
	}
	if producer.envelopeInFlight(envID) {
		t.Fatal("被丢信封条目不得残留在途登记")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := store.retractions(); len(got) == 1 {
			if got[0] != envID {
				t.Fatalf("丢弃臂撤回 id = %v, want [%s]", got, envID)
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("信封条目被丢后必须补派撤回，实际 retractions=%v", store.retractions())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, persists, _ := store.counts(); persists != 0 {
		t.Fatalf("信封未落库时撤回路径不得触达持久化: %d", persists)
	}
}

// TestDD3C2OversizeEnvelopeDropRetractsNoOpHarmless 锁定超限臂的信封类语义
// （DD4-RC1 收口后与队列丢弃臂一致）：单条超限臂发生在在途登记之前，信封
// 从未入队也从未经本采集 settle、在途集合不残留；但丢弃臂仍对信封 id 补派
// 撤回——信封未落库时撤回是 lifecycle 守卫下的无害 no-op，不触达持久化。
func TestDD3C2OversizeEnvelopeDropRetractsNoOpHarmless(t *testing.T) {
	previousBudget := producerQueueMaxBytes
	producerQueueMaxBytes = 1024
	t.Cleanup(func() { producerQueueMaxBytes = previousBudget })

	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "dd3c2-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   4096,
		EnvelopeID: "dd3c2-oversize-env",
		TraceID:    "trace-dd3c2-oversize",
		AuditLogID: "dd3c2-oversize-env",
		Decode: func() (AuditLogInput, error) {
			return producerTestInput("dd3c2-oversize-env"), nil
		},
	})
	if dropped := producer.DroppedTotal(); dropped != 1 {
		t.Fatalf("超限条目必须被丢弃一次，实际 %d", dropped)
	}
	if producer.envelopeInFlight("dd3c2-oversize-env") {
		t.Fatal("超限臂丢弃的信封不得登记在途集合")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := fake.retractions(); len(got) == 1 {
			if got[0] != "dd3c2-oversize-env" {
				t.Fatalf("超限臂撤回 id = %v, want [dd3c2-oversize-env]", got)
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("超限臂丢弃后必须补派撤回（DD4-RC1 三臂一致），实际 %v", fake.retractions())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, persists, _ := fake.counts(); persists != 0 {
		t.Fatalf("信封未落库时撤回路径不得触达持久化: %d", persists)
	}
}

// TestDD3C4RetractEnqueueExhaustedRegistersIntent 锁定 C4：撤回命令三次入队
// 全失败 × 信封在途 → 放弃臂登记撤回意图（诊断日志为确定性证据）→ 信封
// persist 成功 settle → 成功臂读到意图补派一次幂等撤回。
func TestDD3C4RetractEnqueueExhaustedRegistersIntent(t *testing.T) {
	logger := &fakeProducerLogger{warns: &[]string{}}
	gate := make(chan struct{})
	store := &gatedStore{fakeStore: &fakeStore{renewOK: true}, gate: gate}
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd3c4-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, logger)

	const envID = "dd3c4-env"
	// 信封采集入队（入队即登记在途）+ 3 条垫底采集把 4 个 worker 全部停进
	// 续租门，队列随后可确定性塞满。
	producer.Capture(dd1Envelope(envID))
	for index := 0; index < producerWorkers-1; index++ {
		producer.Capture(producerTestInput(fmt.Sprintf("dd3c4-park-%d", index)))
	}
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() != producerWorkers && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != producerWorkers {
		t.Fatalf("仅 %d 个 worker 停进续租门，want %d", store.entered.Load(), producerWorkers)
	}
	if !producer.envelopeInFlight(envID) {
		t.Fatal("信封必须在途（撤回意图登记的前置条件）")
	}

	for i := 0; i < producerQueueCapacity; i++ {
		select {
		case producer.queue <- queuedAudit{}:
		default:
			t.Fatalf("队列容量守恒被破坏：第 %d 条即满（capacity=%d）", i, producerQueueCapacity)
		}
	}
	producer.Retract(envID)

	// 三次入队耗尽放弃：日志在意图置位之后写出，出现即意图已落位。
	abandonDeadline := time.Now().Add(5 * time.Second)
	for !dd2G1WarnPresent(logger, "F3 审计采集队列已满，丢弃信封撤回命令（信封在途，已登记撤回意图，settle 后补派）") {
		if !time.Now().Before(abandonDeadline) {
			t.Fatalf("撤回命令丢弃臂未登记撤回意图: %v", logger.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("settle 前不得执行删除: %v", got)
	}

	// settle：信封 persist 成功 → 成功臂读到意图 → 补派一次幂等撤回。
	close(gate)
	deadline = time.Now().Add(5 * time.Second)
	for {
		got := store.retractions()
		if len(got) == 1 {
			if got[0] != envID {
				t.Fatalf("补派撤回 id = %v, want [%s]", got, envID)
			}
			return
		}
		if len(got) > 1 {
			t.Fatalf("补派必须收敛为单次撤回: %v", got)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("settle 后必须补派撤回，实际 retractions=%v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

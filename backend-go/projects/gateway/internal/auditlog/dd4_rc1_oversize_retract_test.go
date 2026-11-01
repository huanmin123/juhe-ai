package auditlog

// DD4-RC1 回归（2026-10-31，R4 confirmed A 类即修，C2 钉住契约窄缺口）：
// CaptureDeferred 的单条超限臂与队列丢弃臂撤回口径对齐。
//
// 缺陷：超限臂发生在在途登记之前、且不派撤回——finalize 类采集（EnvelopeID
// 空、AuditLogID 与信封同源）估算超限时被该臂丢弃，同请求的 in_progress 信
// 封已 settle 落库成壳（outcome=gateway_succeeded 落 failure retention 窗），
// 壳行无 settle 可等待、无 Cancel 可撤回，滞留至 retention。
//
// 修复：超限臂与队列预算满/入队失败两臂一致，对撤回目标 id（EnvelopeID 回
// 落 AuditLogID）补派 retractBestEffort。

import (
	"testing"
	"time"
)

// TestDD4RC1OversizeFinalizeDropRetractsSettledShell 锁定 RC1 主场景：信封
// 先 settle 落库成壳 → finalize 类 Deferred（EnvelopeID 空、AuditLogID 与壳
// 同 id）估算超预算 → 超限臂丢弃 → 撤回命中壳行 id，壳行不残留。
func TestDD4RC1OversizeFinalizeDropRetractsSettledShell(t *testing.T) {
	const shellID = "dd4rc1-shell"
	previousBudget := producerQueueMaxBytes
	producerQueueMaxBytes = 1024
	t.Cleanup(func() { producerQueueMaxBytes = previousBudget })

	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "dd4rc1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	// 信封 settle 落库：壳行已提交、在途登记注销。
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   256,
		EnvelopeID: shellID,
		TraceID:    "trace-dd4rc1",
		AuditLogID: shellID,
		Decode: func() (AuditLogInput, error) {
			envelope := producerTestInput(shellID)
			envelope.LifecycleStatus = LifecycleInProgress
			return envelope, nil
		},
	})
	deadline := time.Now().Add(5 * time.Second)
	for producer.envelopeInFlight(shellID) {
		if !time.Now().Before(deadline) {
			t.Fatal("信封 persist 未 settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if persisted := fake.persistedInputs(); len(persisted) != 1 || persisted[0].ID != shellID || persisted[0].LifecycleStatus != LifecycleInProgress {
		t.Fatalf("信封壳行必须已落库: %+v", persisted)
	}

	// finalize 类超限条目（EnvelopeID 空、AuditLogID=壳 id）→ 超限臂丢弃。
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   4096,
		TraceID:    "trace-dd4rc1-finalize",
		AuditLogID: shellID,
		Decode: func() (AuditLogInput, error) {
			return producerTestInput(shellID), nil
		},
	})
	if dropped := producer.DroppedTotal(); dropped != 1 {
		t.Fatalf("超限条目必须被丢弃一次，实际 %d", dropped)
	}

	// 超限臂撤回命中壳行 id（与队列丢弃臂同口径）。
	deadline = time.Now().Add(5 * time.Second)
	for {
		if got := fake.retractions(); len(got) == 1 {
			if got[0] != shellID {
				t.Fatalf("超限臂撤回 id = %v, want [%s]", got, shellID)
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("finalize 超限丢弃后必须补派撤回（修复前不派），实际 %v", fake.retractions())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

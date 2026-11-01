package auditlog

// DD-D4 波次测试（2026-10-31）：请求路径全量 Marshal+base64 违背架构总览
// 「大审计的哈希、正文降级、base64 和 JSON 编码由有界后台 goroutine 完成」
// 契约。收口后 cmd 类型桥经 CaptureDeferred 把未解码采集并入有界队列，
// worker 协程内执行 Marshal→Unmarshal 后再持久化；队列字节预算按入队前
// 估算（EstimateDeferredAuditBytes 口径）记账，准入与结算释放语义不变。

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestDD4CaptureDeferredPersistsDecodedInput 锁定 worker 侧解码管道：
// CaptureDeferred 在 worker 协程内执行 Decode 后走完整 persist 序列
// （renew → Persist → hot search），持久化内容即 Decode 产物。
func TestDD4CaptureDeferredPersistsDecodedInput(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "dd4-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	want := producerTestInput("dd4-deferred")
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   1024,
		TraceID:    want.TraceID,
		AuditLogID: want.ID,
		Decode: func() (AuditLogInput, error) {
			return want, nil
		},
	})
	fake.waitForCalls(t, 3)
	stored := fake.persistedInputs()
	if len(stored) != 1 || stored[0].ID != "dd4-deferred" || stored[0].TraceID != want.TraceID {
		t.Fatalf("worker 必须持久化 Decode 产物: %+v", stored)
	}
}

// TestDD4DeferredDecodeFailureDropsWithWarn 锁定解码失败臂：丢弃本条采集、
// 记诊断日志（不静默吞错）、不触达持久化、预算随结算释放归零。
func TestDD4DeferredDecodeFailureDropsWithWarn(t *testing.T) {
	logger := &fakeProducerLogger{warns: &[]string{}}
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "dd4-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, logger)
	producer.CaptureDeferred(DeferredCapture{
		Estimate: 256,
		TraceID:  "trace-dd4-fail",
		Decode: func() (AuditLogInput, error) {
			return AuditLogInput{}, errors.New("dd4 bridge boom")
		},
	})
	deadline := time.Now().Add(5 * time.Second)
	for producer.QueueBytes() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := producer.QueueBytes(); got != 0 {
		t.Fatalf("解码失败后预算必须随结算释放，实际 %d", got)
	}
	if _, persists, _ := fake.counts(); persists != 0 {
		t.Fatalf("解码失败不得触达持久化: %d", persists)
	}
	found := false
	for _, warn := range logger.snapshot() {
		if strings.Contains(warn, "F3 审计采集 JSON 编码失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("解码失败必须记诊断日志: %v", logger.snapshot())
	}
}

// TestDD4DeferredAdmissionUsesEstimateAndReleases 锁定记账口径：准入按调用
// 方估算字节记账（预算满丢弃而非条数上限）、gauge 覆盖排队+在途驻留、
// settle 后释放归零、已准入条目全部落库。
func TestDD4DeferredAdmissionUsesEstimateAndReleases(t *testing.T) {
	previousBudget := producerQueueMaxBytes
	const estimate = 4096
	producerQueueMaxBytes = int64(3 * estimate)
	t.Cleanup(func() { producerQueueMaxBytes = previousBudget })

	store := &blockingStore{fakeStore: fakeStore{renewOK: true}, release: make(chan struct{})}
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd4-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	const total = 10
	for index := 0; index < total; index++ {
		id := fmt.Sprintf("dd4-%d", index)
		producer.CaptureDeferred(DeferredCapture{
			Estimate:   estimate,
			TraceID:    "trace-" + id,
			AuditLogID: id,
			Decode: func() (AuditLogInput, error) {
				return producerTestInput(id), nil
			},
		})
	}

	if dropped := producer.DroppedTotal(); dropped != total-3 {
		t.Fatalf("预算 3 条时应丢弃 %d 条，实际 %d", total-3, dropped)
	}
	if got := producer.QueueBytes(); got != int64(3*estimate) {
		t.Fatalf("驻留 gauge 必须钉在预算上界 %d，实际 %d", 3*estimate, got)
	}
	// DD3-C2（2026-10-31）：预算丢弃条目补派 bytes=0 的撤回命令（撤回不占
	// 字节预算），深度上界相应放宽为 准入 3 条 + 丢弃 7 条的撤回命令；字节
	// 驻留 gauge 上界不变。
	if producer.QueueDepth() > total {
		t.Fatalf("队列深度不得超过准入 3 + 撤回命令 %d = %d，实际 %d", total-3, total, producer.QueueDepth())
	}

	close(store.release)
	store.waitForCalls(t, 3)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && producer.QueueBytes() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := producer.QueueBytes(); got != 0 {
		t.Fatalf("释放后台账必须归零，实际 %d", got)
	}
	if persisted := store.persistedInputs(); len(persisted) != 3 {
		t.Fatalf("已准入条目必须全部落库，实际 %d", len(persisted))
	}
}

// TestDD4DeferredNilDecodeAndNilStoreAreInert 锁定守卫：Decode 缺失或
// store 缺失时静默丢弃，不 panic、不占预算。
func TestDD4DeferredNilDecodeAndNilStoreAreInert(t *testing.T) {
	var nilProducer *Producer
	nilProducer.CaptureDeferred(DeferredCapture{Estimate: 64})
	NewProducer(nil, StaticLease(OwnerLease{}), Config{}, nil).CaptureDeferred(DeferredCapture{
		Estimate: 64,
		Decode: func() (AuditLogInput, error) {
			return producerTestInput("dd4-nil-store"), nil
		},
	})
}

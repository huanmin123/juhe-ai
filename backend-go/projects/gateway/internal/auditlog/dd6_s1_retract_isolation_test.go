package auditlog

// DD6-S1 波次测试（2026-11-01，R6 confirmed 中·审计饱和自延长）：撤回命令与
// 数据条目此前共用同一队列与 worker 池——慢 PG 窗内每个被丢 finalize 派生免
// 预算撤回命令（bytes=0），单条在慢存储上最长占 worker retractWaitBudget
// （15s：DD-D1 settle 等待 + 3 次续租删除），producerWorkers=4 全被撤回占满
// 后数据条目被排挤、queueBytes 不释放、字节预算持续满、丢弃继续派生撤回
// （仅能靠 PG 恢复打破的正反馈；DD5-E1 只移除了调用方内联阻塞）。收口契约：
// 撤回命令走专用有界队列（retractQueue）与专用 worker（retractWorkerLoop），
// 数据 worker 只消费采集条目——撤回占用不再排挤数据面。

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// dd6SelectiveGateStore 只对指定 id 的 Persist 挂门（其余放行）并计数十入门
// 的 Persist 调用：信封停在门内维持在途窗口（撤回 settle 等待挂满预算的前
// 置），数据条目照常流过。
type dd6SelectiveGateStore struct {
	fakeStore
	release chan struct{}
	gateID  string
	entered atomic.Int32
}

func (s *dd6SelectiveGateStore) Persist(ctx context.Context, lease OwnerLease, input AuditLogInput) (PersistResult, error) {
	if input.ID == s.gateID {
		s.entered.Add(1)
		<-s.release
	}
	return s.fakeStore.Persist(ctx, lease, input)
}

// TestDD6S1RetractOccupationCannotStarveDataConsumption 钉住核心隔离契约：
// 全部撤回 worker 被撤回命令占满（DD-D1 settle 等待挂满预算）的同一窗口内，
// 数据条目照常入队、照常被数据 worker 消费落库、字节预算照常释放——撤回占
// 用不排挤数据消费，DD6-S1 正反馈的「数据条目被排挤 → queueBytes 不释放」
// 环节被打断；撤回本身在信封 settle 后仍最终送达（DD3-C2/C4 回归面）。
func TestDD6S1RetractOccupationCannotStarveDataConsumption(t *testing.T) {
	const envID = "dd6s1-env"
	// 不钉 retractWaitBudget 测试缝变量（默认 15s 覆盖整个测试窗，撤回命令
	// 必然全程停在 settle 等待；且泄漏 worker 对该变量的读取不再与测试写入
	// 构成 -race 竞争）。

	store := &dd6SelectiveGateStore{
		fakeStore: fakeStore{renewOK: true},
		release:   make(chan struct{}),
		gateID:    envID,
	}
	var releaseGate sync.Once
	t.Cleanup(func() { releaseGate.Do(func() { close(store.release) }) })
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd6s1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	// 信封 persist 停进存储门：在途窗口成立（撤回 settle 等待的挂起点）。
	producer.Capture(dd1Envelope(envID))
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != 1 {
		t.Fatalf("信封 persist 未进入存储门: entered=%d", store.entered.Load())
	}
	if !producer.envelopeInFlight(envID) {
		t.Fatal("信封必须在途（撤回 settle 等待挂满的前置）")
	}

	// 两条撤回命令占满全部撤回 worker：retractOne 停在 DD-D1 settle 等待。
	producer.Retract(envID)
	producer.Retract(envID)
	time.Sleep(100 * time.Millisecond) // 留出撤回 worker 进入 settle 等待的窗口
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("信封 settle 前撤回不得执行删除（DD-D1）: %v", got)
	}

	// 同一窗口内数据条目照常消费落库：数据 worker 未被撤回排挤。
	const dataEntries = 8
	for index := 0; index < dataEntries; index++ {
		producer.Capture(producerTestInput(fmt.Sprintf("dd6s1-data-%d", index)))
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if persisted := store.persistedInputs(); len(persisted) >= dataEntries {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("撤回占满撤回 worker 的窗口内数据条目必须照常消费（隔离契约），实际 persisted=%d",
				len(store.persistedInputs()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if dropped := producer.DroppedTotal(); dropped != 0 {
		t.Fatalf("数据条目不得因撤回占用被丢弃，实际 %d", dropped)
	}
	// 字节预算残留仅为停在存储门内的信封条目自身——数据条目消费后字节即时
	// 释放，撤回命令（bytes=0 免预算）不占任何预算（DD6-S1 正反馈的
	// 「queueBytes 不释放」环节被打破的直接证据）。
	wantResidual := int64(estimateAuditInputBytes(dd1Envelope(envID)))
	if got := producer.QueueBytes(); got != wantResidual {
		t.Fatalf("字节预算残留 = %d, want 信封条目自身 %d（撤回不得占预算、数据条目必须即时释放）", got, wantResidual)
	}

	// 撤回最终仍送达：信封 settle 后两条撤回命令依次执行（lifecycle 守卫使
	// 重复撤回无害；此处以两条命令都落账钉住撤回面不因隔离而丢命令）。
	releaseGate.Do(func() { close(store.release) })
	deadline = time.Now().Add(5 * time.Second)
	for {
		got := store.retractions()
		if count := dd6CountRetractions(got, envID); count >= 1 {
			if count != 2 {
				t.Fatalf("两条撤回命令都必须最终送达，实际 envID 撤回次数=%d: %v", count, got)
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("信封 settle 后撤回必须送达，实际 retractions=%v", store.retractions())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDD6S1RetractQueueBoundedAndIntentDeferred 钉住撤回面的有界性：撤回队列
// 容量为 retractQueueCapacity（无新增无界结构），饱和时丢弃臂转登记挂起意图
// （DD5-E1 语义在专用队列上保持），撤回仍最终送达。
func TestDD6S1RetractQueueBoundedAndIntentDeferred(t *testing.T) {
	const envID = "dd6s1-bounded-env"
	// 不钉 retractWaitBudget（同上：默认 15s 覆盖测试窗，避免测试缝变量竞争）。

	store := &dd6SelectiveGateStore{
		fakeStore: fakeStore{renewOK: true},
		release:   make(chan struct{}),
		gateID:    envID,
	}
	var releaseGate sync.Once
	t.Cleanup(func() { releaseGate.Do(func() { close(store.release) }) })
	producer := NewProducer(store, StaticLease(OwnerLease{OwnerID: "dd6s1-owner", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)

	// 信封停门维持其在途窗口：两条撤回命令把全部撤回 worker 停进 DD-D1
	// settle 等待（预算覆盖整窗），撤回队列随后可确定性填满。
	producer.Capture(dd1Envelope(envID))
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != 1 {
		t.Fatalf("信封 persist 未进入存储门: entered=%d", store.entered.Load())
	}
	producer.Retract(envID)
	producer.Retract(envID)
	time.Sleep(100 * time.Millisecond)
	if got := store.retractions(); len(got) != 0 {
		t.Fatalf("撤回 worker 必须停在 settle 等待（信封在途）: %v", got)
	}
	dd6FillRetractQueue(t, producer, "dd6s1-fill")

	// 单条超预算的丢弃臂：撤回命令无法入队 → 意图挂起。
	previousMaxBytes := producerQueueMaxBytes
	producerQueueMaxBytes = 1
	t.Cleanup(func() { producerQueueMaxBytes = previousMaxBytes })
	producer.CaptureDeferred(DeferredCapture{
		Estimate:   producerRecordBaseBytes,
		EnvelopeID: envID,
		TraceID:    "trace-dd6s1-bounded",
		AuditLogID: envID,
		Decode: func() (AuditLogInput, error) {
			return producerTestInput(envID), nil
		},
	})
	if dropped := producer.DroppedTotal(); dropped != 1 {
		t.Fatalf("本条采集必须被丢弃一次，实际 %d", dropped)
	}
	producer.envelopeMu.Lock()
	_, intentPending := producer.droppedRetractIntents[envID]
	producer.envelopeMu.Unlock()
	if !intentPending {
		t.Fatal("撤回队列槽满的丢弃臂必须登记挂起撤回意图")
	}

	// 放行：信封 settle 后挂起意图经 worker 消费间隙转储补派，撤回仍最终送达。
	releaseGate.Do(func() { close(store.release) })
	deadline = time.Now().Add(10 * time.Second)
	for {
		if count := dd6CountRetractions(store.retractions(), envID); count >= 1 {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("挂起意图必须最终补派送达，实际 retractions=%v", store.retractions())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

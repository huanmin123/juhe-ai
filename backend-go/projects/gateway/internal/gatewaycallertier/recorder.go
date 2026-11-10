package gatewaycallertier

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// Metrics 是影子观测的指标回调面（M1 量化观测）。组合根把它们接到
// Prometheus 计数器；零值即 no-op，测试无需注册。字段语义：
//   - RecordTotal: name=request|signal|error, kind=信号种类或错误类别
//   - TierGauge: 影子求值后的层级样本（按 Key 记，观测分布）
//   - TransitionTotal: from/to 层级转换计数
type Metrics struct {
	RecordTotal     func(name, kind string)
	TierGauge       func(apiKeyID string, tier int)
	TransitionTotal func(from, to int)
}

func (m Metrics) record(name, kind string) {
	if m.RecordTotal != nil {
		m.RecordTotal(name, kind)
	}
}

func (m Metrics) tier(apiKeyID string, tier int) {
	if m.TierGauge != nil {
		m.TierGauge(apiKeyID, tier)
	}
}

func (m Metrics) transition(from, to int) {
	if m.TransitionTotal != nil && from != to {
		m.TransitionTotal(from, to)
	}
}

// Recorder 是 M1 影子观测的公共门面：信号/请求计数入窗口并完成影子升降级。
// 全部方法不向请求路径传播存储错误（分级是辅助调度不是事实门槛，设计
// §6.2）——错误经 Metrics.RecordTotal("error",…) 暴露。
//
// 2026-11-09：Redis 形态改为单 worker 有界队列异步落库（每请求不再做
// WATCH 读改写）。本记录器只服务影子观测，`TierOf` 不参与调度决策，因此
// 队列满时丢弃事件、停机时尽力排空都是可接受取舍；MemoryStore 形态是进程
// 内互斥写，保持同步语义（standalone 与测试无需等待）。
type Recorder struct {
	store   Store
	metrics Metrics
	now     func() time.Time
	// events/done 仅在 store 声明异步写时创建（RedisStore）；nil 表示同步。
	events chan recorderEvent
	done   chan struct{}
	stop   chan struct{}
	closed atomic.Bool
}

const (
	recorderQueueCapacity = 8192
	recorderDrainBatch    = 4096
	recorderMutateTimeout = 5 * time.Second
)

type recorderEvent struct {
	apiKeyID string
	kind     string
	at       time.Time
	flush    chan struct{}
}

// asynchronousStore 标记允许把 Mutate 移出请求 goroutine 的存储实现。
// 只对 Redis WATCH 读改写生效；MemoryStore 的进程内互斥写保持同步。
type asynchronousStore interface {
	asynchronousMutation()
}

// NewRecorder 构造影子记录器。now 为 nil 时取 time.Now。
func NewRecorder(store Store, metrics Metrics, now func() time.Time) (*Recorder, error) {
	if store == nil {
		return nil, ErrStoreRequired
	}
	if now == nil {
		now = time.Now
	}
	recorder := &Recorder{store: store, metrics: metrics, now: now}
	if _, ok := store.(asynchronousStore); ok {
		recorder.events = make(chan recorderEvent, recorderQueueCapacity)
		recorder.done = make(chan struct{})
		recorder.stop = make(chan struct{})
		go recorder.run()
	}
	return recorder, nil
}

// RecordRequest 计入请求分母（设计 §3：信号比率必须以同窗口请求总数为
// 分母），并完成窗口滚动与影子求值。
func (r *Recorder) RecordRequest(ctx context.Context, apiKeyID string) {
	r.record(ctx, apiKeyID, "")
}

// RecordSignal 计入一次高置信本地信号并完成影子求值。
func (r *Recorder) RecordSignal(ctx context.Context, apiKeyID string, kind SignalKind) {
	r.record(ctx, apiKeyID, string(kind))
}

func (r *Recorder) record(ctx context.Context, apiKeyID, kind string) {
	if apiKeyID == "" {
		r.metrics.record("error", "empty_api_key_id")
		return
	}
	if kind != "" && kind != string(SignalInvalidJSON) && kind != string(SignalModelFilterFailed) {
		r.metrics.record("error", "unknown_signal_kind")
		return
	}
	event := recorderEvent{apiKeyID: apiKeyID, kind: kind, at: r.now()}
	if r.events == nil {
		if err := r.processEvents(ctx, apiKeyID, []recorderEvent{event}); err != nil {
			if errors.Is(err, ErrUnknownSignal) {
				r.metrics.record("error", "unknown_signal_kind")
				return
			}
			r.metrics.record("error", "store_mutate")
		}
		return
	}
	if r.closed.Load() {
		return
	}
	select {
	case r.events <- event:
	default:
		// 影子观测允许丢弃：队列满表示事件产生速率已经超过单一 worker 的
		// Redis 处理能力，阻塞请求协程的代价远高于少记样本。
		r.metrics.record("error", "queue_full")
	}
}

// run 消费异步事件：按 key 分组后每个 key 只做一次 Mutate，保留同 key 事件
// 顺序与各自发生时间（跨分钟的信号不会被批量时间戳折叠）。
func (r *Recorder) run() {
	defer close(r.done)
	for {
		select {
		case <-r.stop:
			r.drainRemaining()
			return
		case first := <-r.events:
			if first.flush != nil {
				close(first.flush)
				continue
			}
			events := []recorderEvent{first}
			var barriers []chan struct{}
			for len(events) < recorderDrainBatch {
				select {
				case next := <-r.events:
					if next.flush != nil {
						barriers = append(barriers, next.flush)
						goto process
					}
					events = append(events, next)
				default:
					goto process
				}
			}
		process:
			r.processBatch(events)
			for _, barrier := range barriers {
				close(barrier)
			}
		}
	}
}

func (r *Recorder) drainRemaining() {
	for {
		select {
		case event := <-r.events:
			if event.flush != nil {
				close(event.flush)
				continue
			}
			batch := []recorderEvent{event}
			for len(batch) < recorderDrainBatch {
				select {
				case next := <-r.events:
					if next.flush != nil {
						// 屏障前的批次必须先落库，再放行 Flush 等待者。
						r.processBatch(batch)
						batch = nil
						close(next.flush)
						continue
					}
					batch = append(batch, next)
				default:
					r.processBatch(batch)
					return
				}
			}
			r.processBatch(batch)
		default:
			return
		}
	}
}

func (r *Recorder) processBatch(events []recorderEvent) {
	if len(events) == 0 {
		return
	}
	grouped := make(map[string][]recorderEvent, len(events))
	order := make([]string, 0, len(events))
	for _, event := range events {
		if _, ok := grouped[event.apiKeyID]; !ok {
			order = append(order, event.apiKeyID)
		}
		grouped[event.apiKeyID] = append(grouped[event.apiKeyID], event)
	}
	for _, apiKeyID := range order {
		if err := r.processEvents(context.Background(), apiKeyID, grouped[apiKeyID]); err != nil {
			if errors.Is(err, ErrUnknownSignal) {
				r.metrics.record("error", "unknown_signal_kind")
				continue
			}
			r.metrics.record("error", "store_mutate")
		}
	}
}

// processEvents 在一次 Mutate 内按事件时间顺序应用同 key 的全部事件。
func (r *Recorder) processEvents(ctx context.Context, apiKeyID string, events []recorderEvent) error {
	ctx, cancel := context.WithTimeout(ctx, recorderMutateTimeout)
	defer cancel()
	type tierSample struct{ before, after int }
	samples := make([]tierSample, 0, len(events))
	err := r.store.Mutate(ctx, apiKeyID, func(current []byte) ([]byte, error) {
		samples = samples[:0]
		state := unmarshalState(current, events[0].at)
		for _, event := range events {
			before := state.Tier
			next, err := applyRecord(state, SignalKind(event.kind), event.at)
			if err != nil {
				return nil, err
			}
			next.Generation++
			next.UpdatedAtMs = event.at.UnixMilli()
			state = next
			samples = append(samples, tierSample{before: before, after: next.Tier})
		}
		return marshalState(state)
	})
	if err != nil {
		return err
	}
	for index, event := range events {
		r.metrics.transition(samples[index].before, samples[index].after)
		r.metrics.tier(apiKeyID, samples[index].after)
		if event.kind == "" {
			r.metrics.record("request_total", "ok")
			continue
		}
		r.metrics.record("signal_total", event.kind)
	}
	return nil
}

// Flush 等待此前入队的事件全部处理完；同步形态直接返回。ctx 必须有超时，
// 避免存储黑洞时调用方无限等待。
func (r *Recorder) Flush(ctx context.Context) error {
	if r.events == nil {
		return nil
	}
	if r.closed.Load() {
		return nil
	}
	barrier := make(chan struct{})
	select {
	case r.events <- recorderEvent{flush: barrier}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 停止异步 worker（尽力排空队列后退出）；同步形态无操作。
func (r *Recorder) Close(ctx context.Context) error {
	if r.events == nil {
		return nil
	}
	if r.closed.CompareAndSwap(false, true) {
		close(r.stop)
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TierOf 只读当前影子层级：任何失败/无状态/状态损坏一律返回保守 DefaultTier
// （T2），错误经 Metrics 暴露。只读不改状态、不触发评估。
func (r *Recorder) TierOf(ctx context.Context, apiKeyID string) int {
	if getter, ok := r.store.(interface {
		GetRaw(ctx context.Context, apiKeyID string) ([]byte, error)
	}); ok {
		blob, err := getter.GetRaw(ctx, apiKeyID)
		if err != nil {
			r.metrics.record("error", "tier_read")
			return DefaultTier
		}
		if blob == nil {
			return DefaultTier
		}
		return unmarshalState(blob, r.now()).Tier
	}
	return DefaultTier
}

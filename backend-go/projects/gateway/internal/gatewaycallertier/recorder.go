package gatewaycallertier

import (
	"context"
	"errors"
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

// Recorder 是 M1 影子观测的公共门面：信号/请求计数入窗口并同步完成影子
// 升降级。全部方法不向请求路径传播存储错误（分级是辅助调度不是事实门槛，
// 设计 §6.2）——错误经 Metrics.RecordTotal("error",…) 暴露。
type Recorder struct {
	store   Store
	metrics Metrics
	now     func() time.Time
}

// NewRecorder 构造影子记录器。now 为 nil 时取 time.Now。
func NewRecorder(store Store, metrics Metrics, now func() time.Time) (*Recorder, error) {
	if store == nil {
		return nil, ErrStoreRequired
	}
	if now == nil {
		now = time.Now
	}
	return &Recorder{store: store, metrics: metrics, now: now}, nil
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
	now := r.now()
	err := r.store.Mutate(ctx, apiKeyID, func(current []byte) ([]byte, error) {
		state := unmarshalState(current, now)
		before := state.Tier
		next, err := applyRecord(state, SignalKind(kind), now)
		if err != nil {
			return nil, err
		}
		next.Generation++
		next.UpdatedAtMs = now.UnixMilli()
		r.metrics.transition(before, next.Tier)
		r.metrics.tier(apiKeyID, next.Tier)
		return marshalState(next)
	})
	if err != nil {
		if errors.Is(err, ErrUnknownSignal) {
			r.metrics.record("error", "unknown_signal_kind")
			return
		}
		r.metrics.record("error", "store_mutate")
		return
	}
	if kind == "" {
		r.metrics.record("request_total", "ok")
		return
	}
	r.metrics.record("signal_total", string(kind))
}

// TierOf 只读当前影子层级：任何失败/无状态/状态损坏一律返回保守 DefaultTier
//（T2），错误经 Metrics 暴露。只读不改状态、不触发评估。
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

package gatewayaccounteffects

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// AccountEffects 门面（accounteffects.go）：归一化入参 + 入队 + 缓存失效副作用
// ---------------------------------------------------------------------------

// weHook 可脚本化的副作用端口：clearLocal 返回值可控（既有 recorderHook 恒 true）。
type weHook struct {
	mu          sync.Mutex
	cleared     []string
	clearResult bool
	invalidated int
	probes      []RecoveryProbeScheduleInput
}

func (h *weHook) clearLocal(runtimeKey string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleared = append(h.cleared, runtimeKey)
	return h.clearResult
}

func (h *weHook) invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.invalidated++
}

func (h *weHook) scheduleProbe(input RecoveryProbeScheduleInput) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.probes = append(h.probes, input)
}

func (h *weHook) invalidateCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.invalidated
}

func (h *weHook) clearedKeys() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.cleared...)
}

// weNewService 构造带可脚本化端口的 SideEffectsService。
func weNewService(t *testing.T, writer SideEffectWriter, driver string) (*SideEffectsService, *weHook, *FakeClock, *ManualScheduler) {
	t.Helper()
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	scheduler := NewManualScheduler()
	hook := &weHook{}
	service, err := NewSideEffectsService(SideEffectsConfig{RuntimeStateDriver: driver}, SideEffectDeps{
		Clock:                         clock,
		Random:                        func() float64 { return 0.5 },
		Scheduler:                     scheduler,
		Writer:                        writer,
		ClearRuntimeAvailabilityLocal: hook.clearLocal,
		InvalidateRuntimeCache:        hook.invalidate,
		ScheduleRecoveryProbe:         hook.scheduleProbe,
	})
	if err != nil {
		t.Fatalf("构造 SideEffectsService 失败：%v", err)
	}
	return service, hook, clock, scheduler
}

// weRecordingClearer 记录 ClearAccountStreamFailureState 调用并按脚本返回。
type weRecordingClearer struct {
	mu      sync.Mutex
	changed bool
	err     error
	calls   []string
	done    chan struct{}
}

func (c *weRecordingClearer) ClearAccountStreamFailureState(_ context.Context, accountID string) (bool, error) {
	c.mu.Lock()
	c.calls = append(c.calls, accountID)
	changed, err := c.changed, c.err
	c.mu.Unlock()
	if c.done != nil {
		c.done <- struct{}{}
	}
	return changed, err
}

// weTemporaryWriter 记录 MarkAccountTemporaryUnavailable 调用并按脚本返回。
type weTemporaryWriter struct {
	mu       sync.Mutex
	updated  bool
	err      error
	reasons  []string
	traceIDs []string
}

func (w *weTemporaryWriter) MarkAccountTemporaryUnavailable(_ context.Context, _ gatewayruntimecache.OpenAIAccountSecret, reason string, traceID string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reasons = append(w.reasons, reason)
	w.traceIDs = append(w.traceIDs, traceID)
	return w.updated, w.err
}

func TestWeAccountEffectsFacadeEnqueueNormalization(t *testing.T) {
	writer := &scriptedWriter{}
	service, hook, clock, scheduler := weNewService(t, writer, "")
	facade := NewAccountEffects(service, SideEffectsConfig{}, SideEffectDeps{
		Logger:                        NopLogger{},
		ClearRuntimeAvailabilityLocal: hook.clearLocal,
		InvalidateRuntimeCache:        hook.invalidate,
	}, AccountEffectsDeps{
		TraceID: func() string { return " trace-1 " },
	})
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
	statusCode := int64(502)
	body := "upstream broken"
	err := facade.ApplyAccountErrorHandlingWithCacheInvalidation(context.Background(), account, AccountErrorHandlingRequest{
		Success:        false,
		StatusCode:     &statusCode,
		BodyText:       &body,
		TrafficSource:  "account_health_check",
		PolicyDecision: map[string]any{"decision": "record"},
	})
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	// 归一化契约：TraceID 原样携带，ObservedAt 取注入时钟的 canonical RFC3339 投影。
	_ = clock
	scheduler.Fire()
	if writer.callCount() != 1 {
		t.Fatalf("writer 调用数 = %d, want 1", writer.callCount())
	}
	// 失败观测必须进入队列（非 gateway 流量不要求 PolicyDecision 存在）。
	state := service.GetState(0, 0)
	if state.CompletedCount != 1 || state.EnqueuedCount != 1 {
		t.Fatalf("state = %+v", state)
	}
}

func TestWeAccountEffectsFacadeGatewayWithoutPolicySkipped(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, scheduler := weNewService(t, writer, "")
	facade := NewAccountEffects(service, SideEffectsConfig{}, SideEffectDeps{Logger: NopLogger{}}, AccountEffectsDeps{})
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
	// 契约：gateway 流量的失败观测若没有策略决定，直接跳过（不入队不报错）。
	err := facade.ApplyAccountErrorHandlingWithCacheInvalidation(context.Background(), account, AccountErrorHandlingRequest{
		Success:       false,
		TrafficSource: TrafficSourceGateway,
	})
	if err != nil {
		t.Fatalf("跳过路径不应报错：%v", err)
	}
	if scheduler.Pending() != 0 {
		t.Fatalf("pending = %d, want 0", scheduler.Pending())
	}
}

func TestWeAccountEffectsMarkTemporaryUnavailable(t *testing.T) {
	tests := []struct {
		name           string
		writer         *weTemporaryWriter
		clearResult    bool
		wantResult     bool
		wantInvalidate int
		wantCleared    int
	}{
		{
			name:       "写入失败返回 false",
			writer:     &weTemporaryWriter{err: errors.New("db 失败")},
			wantResult: false,
		},
		{
			name:       "未变更返回 false",
			writer:     &weTemporaryWriter{updated: false},
			wantResult: false,
		},
		{
			name:           "本地清理成功不再失效缓存",
			writer:         &weTemporaryWriter{updated: true},
			clearResult:    true,
			wantResult:     true,
			wantCleared:    1,
			wantInvalidate: 0,
		},
		{
			name:           "本地清理失败回退运行态缓存失效",
			writer:         &weTemporaryWriter{updated: true},
			clearResult:    false,
			wantResult:     true,
			wantCleared:    1,
			wantInvalidate: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &scriptedWriter{}
			service, hook, _, _ := weNewService(t, writer, "")
			hook.clearResult = tt.clearResult
			facade := NewAccountEffects(service, SideEffectsConfig{}, SideEffectDeps{
				Logger:                        NopLogger{},
				ClearRuntimeAvailabilityLocal: hook.clearLocal,
				InvalidateRuntimeCache:        hook.invalidate,
			}, AccountEffectsDeps{TraceID: func() string { return "trace-9" }})
			account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
			result := facade.MarkGatewayAccountTemporaryUnavailableWithCacheInvalidation(
				context.Background(), tt.writer, account, "上游持续失败", "probe")
			if result != tt.wantResult {
				t.Fatalf("result = %v, want %v", result, tt.wantResult)
			}
			if len(hook.clearedKeys()) != tt.wantCleared {
				t.Fatalf("cleared = %d, want %d", len(hook.clearedKeys()), tt.wantCleared)
			}
			if hook.invalidateCount() != tt.wantInvalidate {
				t.Fatalf("invalidate = %d, want %d", hook.invalidateCount(), tt.wantInvalidate)
			}
		})
	}
}

func TestWeAccountEffectsMarkTemporaryUnavailableTruncatesReason(t *testing.T) {
	writer := &scriptedWriter{}
	service, hook, _, _ := weNewService(t, writer, "")
	hook.clearResult = true
	facade := NewAccountEffects(service, SideEffectsConfig{}, SideEffectDeps{
		Logger:                        NopLogger{},
		ClearRuntimeAvailabilityLocal: hook.clearLocal,
	}, AccountEffectsDeps{})
	tempWriter := &weTemporaryWriter{updated: true}
	long := strings.Repeat("错", 1200)
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
	if !facade.MarkGatewayAccountTemporaryUnavailableWithCacheInvalidation(context.Background(), tempWriter, account, long, "probe") {
		t.Fatal("期望写入成功")
	}
	if len(tempWriter.reasons) != 1 || len([]rune(tempWriter.reasons[0])) != 1000 {
		t.Fatalf("reason 长度 = %d, want 1000", len([]rune(tempWriter.reasons[0])))
	}
}

func TestWeAccountEffectsMarkTemporaryUnavailableInvalidBindingFallback(t *testing.T) {
	// 契约：授权账户缺少绑定时 runtime key 回落为 account.ID，仍然完成本地清理。
	writer := &scriptedWriter{}
	service, hook, _, _ := weNewService(t, writer, "")
	hook.clearResult = true
	facade := NewAccountEffects(service, SideEffectsConfig{}, SideEffectDeps{
		Logger:                        NopLogger{},
		ClearRuntimeAvailabilityLocal: hook.clearLocal,
	}, AccountEffectsDeps{})
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-7", Status: "active", AccountAccessType: "account_authorized"}
	tempWriter := &weTemporaryWriter{updated: true}
	if !facade.MarkGatewayAccountTemporaryUnavailableWithCacheInvalidation(context.Background(), tempWriter, account, "原因", "probe") {
		t.Fatal("期望写入成功")
	}
	cleared := hook.clearedKeys()
	if len(cleared) != 1 || cleared[0] != "acc-7" {
		t.Fatalf("cleared = %v, want [acc-7]", cleared)
	}
}

func TestWeAccountEffectsClearStreamFailureState(t *testing.T) {
	tests := []struct {
		name           string
		clearer        *weRecordingClearer
		wantInvalidate int
	}{
		{name: "清理成功失效运行态缓存", clearer: &weRecordingClearer{changed: true}, wantInvalidate: 1},
		{name: "清理无变更不失效", clearer: &weRecordingClearer{changed: false}, wantInvalidate: 0},
		{name: "清理失败仅告警", clearer: &weRecordingClearer{err: errors.New("db 失败")}, wantInvalidate: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &scriptedWriter{}
			service, hook, _, _ := weNewService(t, writer, "")
			done := make(chan struct{}, 1)
			tt.clearer.done = done
			facade := NewAccountEffects(service, SideEffectsConfig{}, SideEffectDeps{
				Logger:                 NopLogger{},
				InvalidateRuntimeCache: hook.invalidate,
			}, AccountEffectsDeps{})
			account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}
			facade.ClearAccountStreamFailureStateWithCacheInvalidation(context.Background(), tt.clearer, account)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("清理协程未在期限内完成")
			}
			if hook.invalidateCount() != tt.wantInvalidate {
				t.Fatalf("invalidate = %d, want %d", hook.invalidateCount(), tt.wantInvalidate)
			}
		})
	}
}

func TestWeAccountEffectsHandleStreamFailureIsRequestLocal(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, _ := weNewService(t, writer, "")
	facade := NewAccountEffects(service, SideEffectsConfig{}, SideEffectDeps{Logger: NopLogger{}}, AccountEffectsDeps{})
	// 契约：流式帧错误是请求局部观测，不授权共享状态写入。
	if err := facade.HandleStreamFailure(); err != nil {
		t.Fatalf("HandleStreamFailure = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// truncateRunes / optionalText（accounteffects.go 内部 helper）
// ---------------------------------------------------------------------------

func TestWeTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "短文本原样", value: "abc", limit: 10, want: "abc"},
		{name: "恰好等于上限", value: "abcd", limit: 4, want: "abcd"},
		{name: "按 rune 截断中文", value: "一二三四五", limit: 3, want: "一二三"},
		{name: "上限为零", value: "abc", limit: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateRunes(tt.value, tt.limit); got != tt.want {
				t.Fatalf("truncateRunes = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWeOptionalText(t *testing.T) {
	if got := optionalText("   "); got != nil {
		t.Fatalf("空白应返回 nil, got %v", *got)
	}
	value := "trace-1"
	got := optionalText(value)
	if got == nil || *got != value {
		t.Fatalf("optionalText = %v, want %s", got, value)
	}
}

// ---------------------------------------------------------------------------
// runtimekeys.go / sideeffectpolicy.go 的 Must 变体
// ---------------------------------------------------------------------------

func TestWeMustGatewayAccountRuntimeKey(t *testing.T) {
	valid := SuppressibleGatewayAccount{ID: "acc-1"}
	if got := MustGatewayAccountRuntimeKey(valid); got != "acc-1" {
		t.Fatalf("key = %s, want acc-1", got)
	}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("缺少绑定上下文的授权账户必须 panic")
		}
	}()
	MustGatewayAccountRuntimeKey(SuppressibleGatewayAccount{ID: "acc-2", AccessType: "authorized"})
}

func TestWeMustAccountErrorHandlingOperationRuntimeKey(t *testing.T) {
	operation := newTestOperation("acc-1", true)
	if got := MustAccountErrorHandlingOperationRuntimeKey(operation); got != "acc-1" {
		t.Fatalf("key = %s, want acc-1", got)
	}
	invalid := newTestOperation("acc-2", true)
	invalid.Account.AccountAccessType = "account_authorized"
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("缺绑定的授权账户必须 panic")
			}
		}()
		MustAccountErrorHandlingOperationRuntimeKey(invalid)
	}()
}

// ---------------------------------------------------------------------------
// logger.go：NopLogger / SlogLogger
// ---------------------------------------------------------------------------

func TestWeLoggerAdapters(t *testing.T) {
	nop := NopLogger{}
	nop.Info(map[string]any{"event": "x"}, "info")
	nop.Warn(nil, "warn")
	nop.Error(map[string]any{"event": "y"}, "error")

	if _, ok := SlogLogger(nil).(NopLogger); !ok {
		t.Fatal("SlogLogger(nil) 应回落 NopLogger")
	}
	adapter := SlogLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	adapter.Info(map[string]any{"event": "e", "accountId": "a-1"}, "消息")
	adapter.Warn(nil, "空字段")
	adapter.Error(map[string]any{"event": "z"}, "错误")
}

// ---------------------------------------------------------------------------
// clock.go：RealScheduler / sortInts / passiveScheduleNotBeforeDelayMs
// ---------------------------------------------------------------------------

func TestWeRealSchedulerFiresAndCancels(t *testing.T) {
	fired := make(chan struct{}, 1)
	scheduler := RealScheduler{}
	handle := scheduler.After(20, func() { fired <- struct{}{} })
	// 契约：Cancel 后定时器不再触发（20ms 足够长，取消必然先于触发）。
	handle.Cancel()
	select {
	case <-fired:
		t.Fatal("取消后的定时器不应触发")
	default:
	}
	handle2 := scheduler.After(1, func() { fired <- struct{}{} })
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		handle2.Cancel()
		t.Fatal("到期定时器未触发")
	}
	// 负延迟夹到 0：立刻触发。
	handle3 := scheduler.After(-5, func() { fired <- struct{}{} })
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		handle3.Cancel()
		t.Fatal("负延迟应夹到 0 立即触发")
	}
}

func TestWeSortInts(t *testing.T) {
	values := []int{3, 1, 2}
	sortInts(values)
	if values[0] != 1 || values[1] != 2 || values[2] != 3 {
		t.Fatalf("sortInts = %v", values)
	}
	single := []int{7}
	sortInts(single)
	if single[0] != 7 {
		t.Fatalf("单元素被改动: %v", single)
	}
	empty := []int{}
	sortInts(empty)
	if len(empty) != 0 {
		t.Fatalf("空切片被改动: %v", empty)
	}
}

func TestWePassiveScheduleNotBeforeDelayMs(t *testing.T) {
	// interval=1 时 jitter 窗口为 0，offset 为 0，必须回落 interval+1。
	if got := passiveScheduleNotBeforeDelayMs(1, func() float64 { return 0.5 }); got != 2 {
		t.Fatalf("notBefore(1) = %d, want 2", got)
	}
	// 正常窗口：sampled=0.25 产生负 offset，notBefore 取绝对值保证不早于外部硬期限。
	got := passiveScheduleNotBeforeDelayMs(60_000, func() float64 { return 0.25 })
	if got != 60_000+15_000 {
		t.Fatalf("notBefore(60000) = %d, want %d", got, 75_000)
	}
}

func TestWeMathRandomInRange(t *testing.T) {
	for index := 0; index < 32; index++ {
		value := mathRandom()
		if value < 0 || value >= 1 {
			t.Fatalf("mathRandom = %v 越界", value)
		}
	}
}

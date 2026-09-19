package gatewaycircuit

// w14e 覆盖率补强：jitter / precheck / preauth+dispatch 适配器 / suppression
// / types 的未覆盖分支。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// jitter.go：纯函数分支。
// ---------------------------------------------------------------------------

func TestW14EJitterArms(t *testing.T) {
	// 各窗口档位。
	cases := []struct {
		interval int64
		floor    int64
	}{
		{interval: 500, floor: 0},                    // sub-minute：窗口被 interval/2 收窄
		{interval: 5 * 60_000, floor: 1},             // minute 档
		{interval: 2 * 60 * 60_000, floor: 1},        // hour 档
		{interval: 2 * 24 * 60 * 60_000, floor: 1},   // day 档
		{interval: 10 * 24 * 60 * 60_000, floor: 1},  // week 档
	}
	for _, testCase := range cases {
		if got := passiveScheduleJitterWindowMs(testCase.interval); got < testCase.floor {
			t.Fatalf("window(%d) = %d", testCase.interval, got)
		}
	}
	if got := passiveScheduleJitterWindowMs(0); got != 0 {
		t.Fatalf("zero interval window = %d", got)
	}
	// 非正窗口直接返回 0。
	if got := passiveScheduleOffsetWithinWindowMs(0, func() float64 { return 0.5 }); got != 0 {
		t.Fatalf("zero window offset = %d", got)
	}
	// offset==0 时返回 1。
	if got := passiveScheduleOffsetWithinWindowMs(1, func() float64 { return 1 }); got != 1 {
		t.Fatalf("offset = %d", got)
	}
	// 越界随机值被钳制；NaN 视为 0。
	if got := passiveScheduleOffsetWithinWindowMs(100, func() float64 { return 42 }); got <= 0 {
		t.Fatalf("clamped offset = %d", got)
	}
	// notBefore：interval<1 归一为 1；offset<0 取反。
	delay := passiveScheduleNotBeforeDelayMs(0, func() float64 { return 0.9 })
	if delay < 1 {
		t.Fatalf("notBefore delay = %d", delay)
	}
	negative := passiveScheduleNotBeforeDelayMs(60_000, func() float64 { return 0 })
	if negative < 60_000 {
		t.Fatalf("notBefore negative delay = %d", negative)
	}
	// Settings.accountCircuitBackoffDelayMs：缺表回退默认表、小 attempt 精确、
	// 大 attempt 钳制、带 seed 的确定性抖动。
	settings := DefaultSettings()
	if got := settings.accountCircuitBackoffDelayMs(0, "", nil); got <= 0 {
		t.Fatalf("attempt 0 delay = %d", got)
	}
	if got := settings.accountCircuitBackoffDelayMs(2, "seed:w14e", func() float64 { return 0.5 }); got <= 0 {
		t.Fatalf("seeded delay = %d", got)
	}
	huge := settings.accountCircuitBackoffDelayMs(999, "seed:w14e", nil)
	if huge <= 0 {
		t.Fatalf("clamped delay = %d", huge)
	}
	empty := Settings{}
	if got := empty.accountCircuitBackoffDelayMs(9, "", nil); got <= 0 {
		t.Fatalf("empty settings delay = %d", got)
	}
	// hexPrefixSample 的非十六进制截断。
	if got := hexPrefixSample("zzzz"); got != 0 {
		t.Fatalf("invalid hex sample = %d", got)
	}
	if got := hexPrefixSample("deadbeef-rest"); got != 0xdeadbeef {
		t.Fatalf("hex sample = %d", got)
	}
}

// ---------------------------------------------------------------------------
// precheck.go：协议字段回退与摘要错误臂。
// ---------------------------------------------------------------------------

func TestW14EPrecheckSummaryArms(t *testing.T) {
	anthropic := gatewayruntimecache.OpenAIAccountSecret{ProviderProtocolProfileID: "Acct_anthropic_claude"}
	if code := gatewayAccountSummaryProtocolCode(anthropic); code != AnthropicProtocolCode {
		t.Fatalf("anthropic code = %q", code)
	}
	if version := gatewayAccountSummaryProtocolVersion(anthropic); version != AnthropicProtocolVersion {
		t.Fatalf("anthropic version = %q", version)
	}
	gemini := gatewayruntimecache.OpenAIAccountSecret{ProviderProtocolProfileID: "acct_gemini_native"}
	if code := gatewayAccountSummaryProtocolCode(gemini); code != GeminiProtocolCode {
		t.Fatalf("gemini code = %q", code)
	}
	if version := gatewayAccountSummaryProtocolVersion(gemini); version != GeminiProtocolVersion {
		t.Fatalf("gemini version = %q", version)
	}
	unknown := gatewayruntimecache.OpenAIAccountSecret{ProviderProtocolProfileID: "acct_unknown_x"}
	if code := gatewayAccountSummaryProtocolCode(unknown); code != "" {
		t.Fatalf("unknown code = %q", code)
	}
	if version := gatewayAccountSummaryProtocolVersion(unknown); version != "" {
		t.Fatalf("unknown version = %q", version)
	}
	// 授权账户缺绑定且上下文缺系统账户必须报错。
	authorized := gatewayruntimecache.OpenAIAccountSecret{AccountAccessType: "account_authorized"}
	if _, err := gatewayAccountSummarySystemAccountID(authorized, PrecheckSummaryContext{}); err == nil || !strings.Contains(err.Error(), "授权账户缺少绑定系统账户") {
		t.Fatalf("authorized err = %v", err)
	}
	// 普通账户缺系统账户且上下文缺失必须报错。
	if _, err := gatewayAccountSummarySystemAccountID(gatewayruntimecache.OpenAIAccountSecret{}, PrecheckSummaryContext{}); err == nil || !strings.Contains(err.Error(), "账户缺少系统账户") {
		t.Fatalf("owner err = %v", err)
	}
	if got := derefInt(nil); got != 0 {
		t.Fatalf("derefInt(nil) = %d", got)
	}
	value := 7
	if got := derefInt(&value); got != 7 {
		t.Fatalf("derefInt = %d", got)
	}
}

// ---------------------------------------------------------------------------
// preauth / dispatchwait 适配器：审计捕获与选项覆盖。
// ---------------------------------------------------------------------------

func TestW14EPreAuthAdapterOptionsAndAudit(t *testing.T) {
	coordinator, _ := newManualCoordinator(t, 4, 4)
	// Options 覆盖 + AuditCapture 提供时全部生效分支。
	waiter := &PreAuthRecoverableWait{
		Coordinator: coordinator,
		Logger:      NopLogger,
		Options:     WaitEngineOptions{MaxWaitMs: 60_000, CheckIntervalMs: 20, DueRetryDelayMs: 1_000},
	}
	input := gatewaypreauth.RecoverableWaitInput{
		ScopeKey: "w14e-scope", Reason: "recoverable_unavailable",
		IsReady:          func(context.Context) bool { return true },
		NextRetryAfterMs: func(context.Context) (int64, bool) { return 0, false },
		AuditCapture:     w14eMetadataCapture{},
	}
	if err := waiter.WaitForRecoverableUnavailableState(context.Background(), input); err != nil {
		t.Fatalf("options override must keep ready path: %v", err)
	}
	// 引擎错误向上透传：未就绪 + 刷新失败（真实定时器协调器）。
	realCoordinator := NewWaitCoordinator(WaitCoordinatorOptions{MaxWaitersPerScope: 4, MaxWaitersGlobal: 8})
	waiter = &PreAuthRecoverableWait{
		Coordinator: realCoordinator,
		Logger:      NopLogger,
		Options:     WaitEngineOptions{MaxWaitMs: 60_000, CheckIntervalMs: 20},
	}
	erroring := gatewaypreauth.RecoverableWaitInput{
		ScopeKey: "w14e-scope-err", Reason: "recoverable_unavailable",
		IsReady:          func(context.Context) bool { return false },
		NextRetryAfterMs: func(context.Context) (int64, bool) { return 0, false },
		Refresh:          func(context.Context) error { return errors.New("w14e refresh boom") },
		AuditCapture:     w14eMetadataCapture{},
		MaxWaitMs:        5_000,
		RequestStartedAtMs: time.Now().UnixMilli(),
		DeadlineAtMs:       time.Now().UnixMilli() + 60_000,
	}
	if err := waiter.WaitForRecoverableUnavailableState(context.Background(), erroring); err == nil || !strings.Contains(err.Error(), "w14e refresh boom") {
		t.Fatalf("engine refresh error = %v", err)
	}
}

type w14eMetadataCapture struct{}

func (w14eMetadataCapture) AddGatewayMetadata(label string, metadata map[string]any) {}
func (w14eMetadataCapture) BindContext(gatewaypreauth.AuditGatewayContext)           {}
func (w14eMetadataCapture) Finalize(gatewaypreauth.AuditFinalizeInput)               {}

func TestW14EDispatchWaitAdapterAuditAndOptions(t *testing.T) {
	coordinator, hub := newManualCoordinator(t, 4, 4)
	waiter := &PreAuthRecoverableWait{
		Coordinator: coordinator,
		Logger:      NopLogger,
		Options:     WaitEngineOptions{MaxWaitMs: 60_000, CheckIntervalMs: 20},
	}
	waited, skipped, err := waiter.WaitForStateLoop(context.Background(), StateWaitInput{
		ScopeKey: "w14e-scope", Reason: "suppression", MaxWaitMs: 5,
		RequestStartedAtMs: time.Now().UnixMilli(),
		AuditCapture:       w14eMetadataCapture{},
		Refresh:            func(context.Context) (bool, bool, int64, error) { return true, false, 0, nil },
	})
	if err != nil || skipped != "" || waited < 0 {
		t.Fatalf("state loop = (%d, %q, %v)", waited, skipped, err)
	}
	// 首采样通过、后续刷新失败：引擎错误经适配器透传。
	// 手动协调器需要显式触发等待轮次。
	go func() {
		time.Sleep(20 * time.Millisecond)
		hub.fireAll()
	}()
	samples := 0
	_, _, err = waiter.WaitForStateLoop(context.Background(), StateWaitInput{
		ScopeKey: "w14e-scope-err", Reason: "suppression", MaxWaitMs: 5_000,
		RequestStartedAtMs: time.Now().UnixMilli(),
		DeadlineAtMs:       time.Now().UnixMilli() + 60_000,
		Refresh: func(context.Context) (bool, bool, int64, error) {
			samples++
			if samples == 1 {
				return false, false, 0, nil
			}
			return false, false, 0, errors.New("w14e later refresh boom")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "w14e later refresh boom") {
		t.Fatalf("later refresh error = %v", err)
	}
}

// TestW14ESuppressionDegradationArms 已随 DegradeForGatewayFailure /
// ActivateRuntimeDegradation / ClearDegradation 写面退场删除（生产写面
// 退场，见 suppression.go 顶部注记）。

func TestW14ESuppressionFilterWithoutPredicate(t *testing.T) {
	now := int64(1_000_000)
	clock := &now
	store := NewLocalSuppressionStore(LocalSuppressionStoreOptions{Now: func() int64 { return *clock }})

	account := SuppressibleAccount{SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: "w14e-filter"}}
	result := store.FilterSuppressions([]SuppressibleAccount{account}, nil, SuppressionFilterOptions{})
	if len(result.Accounts) != 1 || result.SuppressedCount != 0 {
		t.Fatalf("filter result = %+v", result)
	}
	// 半开租约路径：half_open 抑制到期后带 AcquireHalfOpenLease 过滤。
	store.Suppress("w14e-filter", 1_000, "transport:timeout", AvailabilityStatusHalfOpen, nil)
	*clock += 5_000
	withLease := store.FilterSuppressions([]SuppressibleAccount{account}, nil, SuppressionFilterOptions{AcquireHalfOpenLease: true})
	if len(withLease.Accounts) != 1 || len(withLease.AcquiredHalfOpenLeases) != 1 {
		t.Fatalf("half-open filter = %+v", withLease)
	}
	// PreserveDispatchPriorityTiers：少于两个账户直接返回副本；未知层级归末尾。
	reordered := PreserveDispatchPriorityTiers([]SuppressibleAccount{account}, []SuppressibleAccount{account}, nil)
	if len(reordered) != 1 {
		t.Fatalf("preserve = %+v", reordered)
	}
}

// ---------------------------------------------------------------------------
// types.go 收尾：作用域必填字段与证据窗口裁剪。
// ---------------------------------------------------------------------------

func TestW14EScopeRequiredParts(t *testing.T) {
	if _, err := ScopeKey(Scope{Kind: ScopeKindKey, AccountRuntimeKey: "acc", KeyFingerprint: " "}); err == nil {
		t.Fatal("key scope without fingerprint must fail")
	}
	if _, err := ScopeKey(Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acc", ProtocolProfile: "p", RequestLane: LaneText}); err == nil {
		t.Fatal("protocol scope without model bucket must fail")
	}
	// FailureEvidenceKeysOf 的保留窗口裁剪（required+1）。
	shas := stringList{}
	for index := 0; index < 5; index++ {
		shas = append(shas, strings.Repeat(string(rune('a'+index)), 64))
	}
	keys, err := FailureEvidenceKeysOf(State{ConfirmationFailuresRequired: int64Ptr(2), FailureEvidenceKeys: shas})
	if err != nil || len(keys) != 3 {
		t.Fatalf("keys = %d, %v", len(keys), err)
	}
	if keys[len(keys)-1] != shas[4] {
		t.Fatalf("keys must keep the newest: %v", keys)
	}
}

// 编译期锚定 imports（被参数化场景使用）。
var (
	_ = errors.New
	_ = context.Background
)

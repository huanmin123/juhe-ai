package gatewaydispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 引擎级错误路径测试：handleUpstreamAttemptError 的重抛/跳过/换 Key 分类、
// 准备失败的账户级/请求级分支、容量排队、可恢复等待与账户锁跨账户阻断。
// 全部通过 fake 端口驱动，无真实 PG/Redis。

// fastDispatchSettings 返回无重试延迟的设置（避免同账户重试真实 sleep）。
func fastDispatchSettings() gatewayruntimecache.GatewaySettings {
	settings := gatewaySettingsForTest()
	settings.TemporaryUnschedulableRetryIntervalSeconds = 0
	return settings
}

func fastDispatchArgs(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate) FetchFirstAvailableUpstreamArgs {
	t.Helper()
	args := dispatchArgs(t, req, accounts)
	args.Settings = fastDispatchSettings()
	return args
}

// ---------------------------------------------------------------------------
// handleUpstreamAttemptError 直测
// ---------------------------------------------------------------------------

// attemptErrorHarness 组装 handleUpstreamAttemptError 的最小输入并暴露状态句柄。
type attemptErrorHarness struct {
	engine        *Engine
	args          FetchFirstAvailableUpstreamArgs
	coordination  *RequestCoordinationContext
	input         *dispatchSingleAccountInput
	lastAttempt   *UpstreamAttempt
	agentGuidance *gatewaypreauth.GatewayAgentGuidanceResponse
}

func newAttemptErrorHarness(t *testing.T) *attemptErrorHarness {
	t.Helper()
	engine, _, _ := newTestEngine(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	coordination := args.RequestCoordination
	usage := testUsageContext()
	auditIndex := 0
	sameAccountRetryID := ""
	harness := &attemptErrorHarness{
		engine:        engine,
		args:          args,
		coordination:  coordination,
		lastAttempt:   &UpstreamAttempt{AccountID: "a-1", Message: "上次尝试"},
	}
	harness.input = &dispatchSingleAccountInput{
		args:                                &args,
		coordination:                        coordination,
		usageContext:                        &usage,
		auditCapture:                        AuditCapture{Context: &frozenAudit{sink: &fakeAuditSink{}}, Sink: &fakeAuditSink{}},
		settings:                            args.Settings,
		signal:                              context.Background(),
		requestLane:                         "text",
		requestApiKeyAttemptCount:           ptrInt(0),
		activeSameAccountRetryID:            &sameAccountRetryID,
		activeAccountLockRetryLease:         newAccountLockLeaseRef(nil),
		activeAccountLockObservation:        newAccountLockObservationRef(nil),
		observedEscapedTiers:                map[string]struct{}{},
		failedProxyDispatchKeys:             map[string]string{},
		failedAccountIDs:                    map[string]struct{}{},
		recoverableFailedAccountIDs:         map[string]struct{}{},
		cycleRecoverableAccountIDs:          map[string]struct{}{},
		capacityLimitFailures:               newCapacityFailuresRef(nil),
		pendingApiKeyFailures:               newPendingFailuresRef(nil),
		lastAttempt:                         &harness.lastAttempt,
		agentGuidanceResponse:               &harness.agentGuidance,
		auditAttemptIndex:                   &auditIndex,
		concurrencyRetryWaitBudgetMs:        ptrInt64(1_200),
		keyModelFailureBudget:               gatewayaccounteffects.NewGatewayKeyModelFailureBudget(),
		reserveSameAccountRetry:             func(gatewayrouting.GatewayDispatchAttemptIdentity, string, string) (string, error) { return "", nil },
		createAccountLockLeaseRelease:       func(bool) func(bool) bool { return func(bool) bool { return true } },
		setAccountCircuitAttemptTransferred: func() {},
	}
	return harness
}

func newAccountLockLeaseRef(value *AccountLockRetryLease) **AccountLockRetryLease { return &value }
func newAccountLockObservationRef(value *AccountLockObservation) **AccountLockObservation {
	return &value
}
func newCapacityFailuresRef(value []AccountCapacityLimitFailure) *[]AccountCapacityLimitFailure {
	return &value
}
func newPendingFailuresRef(value []PendingAccountApiKeyFailure) *[]PendingAccountApiKeyFailure {
	return &value
}
func ptrInt(value int) *int { return &value }

func (h *attemptErrorHarness) errorContext(account AccountCandidate, failure error, mutate func(*upstreamAttemptErrorContext)) (upstreamAttemptErrorContext, errorKind, errorStop, error) {
	retryKey := false
	skipAccount := false
	keepSlot := false
	var result *UpstreamDispatchResult
	loop := upstreamAttemptLoopContext{
		in:            h.input,
		account:       account,
		upstreamUrls:  []string{"https://upstream.example/v1/chat/completions"},
		usageContext:  h.input.usageContext,
		auditCapture:  h.input.auditCapture,
		signal:        h.input.signal,
		accountApiKeyAttemptCount: ptrInt(0),
		concurrencySlot: &ConcurrencySlot{Acquired: true},
		excludedApiKeyFingerprints: map[string]struct{}{},
		keepConcurrencySlotRef:     &keepSlot,
		pendingApiKeyFailuresRef:   h.input.pendingApiKeyFailures,
		retryAccountApiKeyRef:      &retryKey,
		skipAccountRef:             &skipAccount,
		resultRef:                  &result,
	}
	errorCtx := upstreamAttemptErrorContext{
		loop:                          loop,
		account:                       account,
		attemptFailure:                failure,
		upstreamURL:                   "https://upstream.example/v1/chat/completions",
		attemptStartedAt:              NowMs(),
		auditAttemptID:                "audit-1",
		hotQualityAttempt:             &hotQualityAttemptHandle{},
		firstByteDeadlineTriggeredRef: ptrBool(false),
		dispatchAttemptIdentity:       gatewayDispatchIdentityForTest(),
	}
	if mutate != nil {
		mutate(&errorCtx)
	}
	kind, stop, err := h.engine.handleUpstreamAttemptError(context.Background(), errorCtx)
	return errorCtx, kind, stop, err
}

func ptrBool(value bool) *bool { return &value }

func TestHandleUpstreamAttemptErrorRethrowsWallBudget(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	wallErr := &GatewayRequestWallBudgetExhaustedError{BudgetKind: WallBudgetKindWall}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], wallErr, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindRethrow || stop.rethrown != wallErr {
		t.Fatalf("kind=%v stop=%#v", kind, stop)
	}
}

func TestHandleUpstreamAttemptErrorAccountScopedGuidanceSkipsAccount(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	accountScopedTrue := true
	guidance := &gatewaypreauth.GatewayAgentGuidanceResponse{Message: "仅本账户支持", AccountScoped: &accountScopedTrue}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], guidance, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindHandled || stop.kind != errorStopSkipAccount {
		t.Fatalf("kind=%v stop=%#v", kind, stop)
	}
	if harness.input.agentGuidanceResponse == nil || *harness.input.agentGuidanceResponse != guidance {
		t.Fatalf("guidance = %#v", harness.input.agentGuidanceResponse)
	}
	if _, ok := harness.input.failedAccountIDs["a-1"]; !ok {
		t.Fatal("账户必须进入失败集合")
	}
	if last := *harness.input.lastAttempt; last == nil || last.UpstreamURL != "gateway:agent_guidance" {
		t.Fatalf("lastAttempt = %#v", last)
	}
}

func TestHandleUpstreamAttemptErrorRequestScopedErrorsRethrow(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	adapter := NewOpenAIOAuthCodexAdapterError("adapter 全局失败")
	accountScopedFalse := false
	cases := []error{
		&gatewaypreauth.GatewayAgentGuidanceResponse{Message: "全局 guidance", AccountScoped: &accountScopedFalse},
		&gatewaypreauth.GatewayLocalProtocolResponse{Message: "本地协议"},
		&gatewaypreauth.GatewayRequestValidationError{Message: "校验失败"},
		adapter,
	}
	for index, failure := range cases {
		_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], failure, nil)
		if err != nil {
			t.Fatalf("case %d err = %v", index, err)
		}
		if kind != errorKindRethrow || stop.rethrown != failure {
			t.Fatalf("case %d kind=%v rethrown=%v", index, kind, stop.rethrown)
		}
	}
}

func TestHandleUpstreamAttemptErrorNeutralConfiguredDeadlineCutover(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	coordinator := &NormalRouteFirstByteAttemptCoordinator{state: "active"}
	released := false
	coordinator.AttachReservation(&SpeedFirstCutoverReservationView{
		TargetAccountIDValue: "a-1",
		ReleaseFunc:          func() { released = true },
	})
	timeoutErr := &GatewayFirstByteTimeoutError{Message: "配置截止超时", Source: FirstByteTimeoutSourceConfiguredDeadline}
	deadline := &gatewayrouting.NormalRouteAttemptFirstByteDeadline{
		EffectiveDeadlineMs: 5_000,
		LimitingFactor:      gatewayrouting.FirstByteLimitingFactorConfigured,
	}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], timeoutErr, func(errorCtx *upstreamAttemptErrorContext) {
		errorCtx.normalRouteFirstByteDeadline = deadline
		errorCtx.firstByteDeadlineCoordinator = coordinator
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindRethrow {
		t.Fatalf("kind = %v", kind)
	}
	cutover, ok := stop.rethrown.(*NormalRouteFirstByteCutoverError)
	if !ok {
		t.Fatalf("rethrown = %#v", stop.rethrown)
	}
	if cutover.AccountID != "a-1" || cutover.Deadline.EffectiveDeadlineMs != 5_000 {
		t.Fatalf("cutover = %#v", cutover)
	}
	if cutover.CutoverReservation == nil {
		t.Fatal("切换预留必须随错误转移")
	}
	if released {
		t.Fatal("转移的预留不被释放")
	}
	if coordinator.state != "transferred" {
		t.Fatalf("coordinator state = %q", coordinator.state)
	}
}

func TestHandleUpstreamAttemptErrorNeutralWallPrecommitDeadline(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	coordinator := &NormalRouteFirstByteAttemptCoordinator{state: "active"}
	released := false
	coordinator.AttachReservation(&SpeedFirstCutoverReservationView{
		ReleaseFunc: func() { released = true },
	})
	timeoutErr := &GatewayFirstByteTimeoutError{Message: "墙钟超时", Source: FirstByteTimeoutSourceConfiguredDeadline}
	deadline := &gatewayrouting.NormalRouteAttemptFirstByteDeadline{
		EffectiveDeadlineMs: 5_000,
		LimitingFactor:      gatewayrouting.FirstByteLimitingFactorWallPrecommit,
	}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], timeoutErr, func(errorCtx *upstreamAttemptErrorContext) {
		errorCtx.normalRouteFirstByteDeadline = deadline
		errorCtx.firstByteDeadlineCoordinator = coordinator
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindRethrow {
		t.Fatalf("kind = %v", kind)
	}
	budgetErr, ok := stop.rethrown.(*GatewayRequestWallBudgetExhaustedError)
	if !ok || budgetErr.BudgetKind != WallBudgetKindWall {
		t.Fatalf("rethrown = %#v", stop.rethrown)
	}
	if !released {
		t.Fatal("wall precommit 必须先释放预留（supersede）")
	}
}

func TestHandleUpstreamAttemptErrorAbortedAfterStartRecords(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	harness.input.signal = canceled
	abortErr := &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], abortErr, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindRethrow || stop.rethrown != abortErr {
		t.Fatalf("kind=%v rethrown=%v", kind, stop.rethrown)
	}
	dispatcher := harness.engine.FailureDispatcher.(*fakeFailureDispatcher)
	if dispatcher.handleErrorCalls.Load() != 1 {
		t.Fatalf("已开始的取消必须记录失败处理, calls = %d", dispatcher.handleErrorCalls.Load())
	}
	// 亲和必须遗忘该账户。
	if len(harness.engine.Affinity.(*fakeAffinity).forgotten) != 1 {
		t.Fatal("取消后必须遗忘会话亲和")
	}
}

func TestHandleUpstreamAttemptErrorUnprovenTransportRethrow(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	unproven := errors.New("dial tcp: connection refused")
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], unproven, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindRethrow || stop.rethrown != unproven {
		t.Fatalf("kind=%v rethrown=%v", kind, stop.rethrown)
	}
}

func TestHandleUpstreamAttemptErrorProvenStartedSameAccountRetry(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	harness.input.reserveSameAccountRetry = func(gatewayrouting.GatewayDispatchAttemptIdentity, string, string) (string, error) {
		return "retry-1", nil
	}
	started := &PrimaryStartedGatewayTransportError{Err: &StartedTransportError{Err: errors.New("连接被重置")}}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], started, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindHandled || stop.kind != errorStopRetrySameAccount {
		t.Fatalf("kind=%v stop=%#v", kind, stop)
	}
	if harness.input.activeSameAccountRetryID == nil || *harness.input.activeSameAccountRetryID != "retry-1" {
		t.Fatalf("activeSameAccountRetryID = %#v", harness.input.activeSameAccountRetryID)
	}
	// lastAttempt 归类传输失败种类。
	last := *harness.input.lastAttempt
	if last == nil || last.TransportFailureKind != TransportFailureKindConnection {
		t.Fatalf("lastAttempt = %#v", last)
	}
}

func TestHandleUpstreamAttemptErrorProvenTimeoutClassified(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	// 空消息使电路失败归类退回错误文本（含「超时」→ timeout 种类）。
	harness.lastAttempt = &UpstreamAttempt{AccountID: "a-1"}
	harness.input.reserveSameAccountRetry = func(gatewayrouting.GatewayDispatchAttemptIdentity, string, string) (string, error) {
		return "retry-timeout", nil
	}
	started := &PrimaryStartedGatewayTransportError{Err: &StartedTransportError{Err: &UpstreamRequestTimeoutError{Message: "上游请求超时"}}}
	_, _, stop, err := harness.errorContext(testAccounts("a-1")[0], started, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if stop.kind != errorStopRetrySameAccount {
		t.Fatalf("stop = %#v", stop)
	}
	last := *harness.input.lastAttempt
	if last == nil || last.TransportFailureKind != TransportFailureKindTimeout {
		t.Fatalf("lastAttempt = %#v", last)
	}
}

func TestHandleUpstreamAttemptErrorRetryAnotherApiKeyWithPendingFailure(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	harness.engine.FailureDispatcher = &fakeFailureDispatcher{
		failedResult:  FailedUpstreamResponseResult{Action: FailedResponseActionSkipAccount},
		opaqueFailover: true,
	}
	effects := &recordingAPIKeyEffects{}
	harness.engine.APIKeyEffects = effects
	fingerprint := "fp-1"
	account := accountWithKeys("a-1", "k1", "k2")
	account.SelectedAPIKeyFingerprint = &fingerprint
	started := &PrimaryStartedGatewayTransportError{Err: &StartedTransportError{Err: errors.New("连接被重置")}}
	_, kind, stop, err := harness.errorContext(account, started, func(errorCtx *upstreamAttemptErrorContext) {
		errorCtx.loop.accountApiKeyAttemptCount = ptrInt(0)
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindHandled || stop.kind != errorStopRetryKey {
		t.Fatalf("kind=%v stop=%#v", kind, stop)
	}
	pending := *harness.input.pendingApiKeyFailures
	if len(pending) != 1 || pending[0].Status != "temporary_unavailable" || pending[0].ObservationEpoch != "obs-a-1" {
		t.Fatalf("pending = %#v", pending)
	}
}

func TestHandleUpstreamAttemptErrorLockBlocksCrossAccount(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	harness.input.accountLockTrafficEnabled = true
	harness.engine.Locks = &blockingLocks{}
	harness.input.reserveSameAccountRetry = func(gatewayrouting.GatewayDispatchAttemptIdentity, string, string) (string, error) {
		return "", nil // 无同账户重试 → 走账户锁检查
	}
	started := &PrimaryStartedGatewayTransportError{Err: &StartedTransportError{Err: errors.New("连接被重置")}}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], started, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindHandled || stop.kind != errorStopSkipAccount {
		t.Fatalf("kind=%v stop=%#v", kind, stop)
	}
}

// blockingLocks 返回阻断跨账户的锁状态。
type blockingLocks struct{}

func (blockingLocks) FindStateAsync(context.Context, string) (*AccountLockStateView, error) {
	return &AccountLockStateView{BlocksCrossAccount: true}, nil
}

func (blockingLocks) AcquireRetryLeaseAsync(context.Context, string, int64) (LockLeaseAcquire, error) {
	return LockLeaseAcquire{Allowed: true}, nil
}

func (blockingLocks) ConsumeRetryLeaseAsync(context.Context, string, string) (bool, error) {
	return true, nil
}

func (blockingLocks) ReleaseRetryLeaseAsync(context.Context, ReleaseRetryLeaseInput) (bool, error) {
	return true, nil
}

func (blockingLocks) AbandonRetryReservationAsync(context.Context, AccountLockRetryLease) error {
	return nil
}

func (blockingLocks) RecordFailureAsync(context.Context, string, string, *AccountLockObservation) error {
	return nil
}

func (blockingLocks) SettleDeadlineAsync(context.Context, string, int64, *AccountLockObservation) error {
	return nil
}

func (blockingLocks) ListStatesAsync(context.Context, []string) (map[string]AccountLockStateView, error) {
	return map[string]AccountLockStateView{}, nil
}

func (blockingLocks) CompleteSuccessAsync(context.Context, string, string, *AccountLockObservation) error {
	return nil
}

// ---------------------------------------------------------------------------
// 端到端错误路径
// ---------------------------------------------------------------------------

// TestDispatchTransportFailureSkipsToNextAccount: 双账户全部连接拒绝（已开始
// 传输失败），第一个账户走同账户重试预算后跳过，第二个账户同样失败。
func TestDispatchTransportFailureSkipsToNextAccount(t *testing.T) {
	engine, driver, dispatcher := newTestEngine(t)
	dead := "https://127.0.0.1:9/v1/chat/completions"
	driver.urlByAccount = map[string][]string{
		"a-1": {dead},
		"a-2": {dead},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1", "a-2")))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if len(attemptErr.FailedAccountIDs) != 2 {
		t.Fatalf("failed = %#v", attemptErr.FailedAccountIDs)
	}
	if dispatcher.handleErrorCalls.Load() < 2 {
		t.Fatalf("错误处理调用 = %d", dispatcher.handleErrorCalls.Load())
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.AccountID != "a-2" {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

// TestDispatchKeyScopedTransportFailureRotatesKeys: Key 级传输失败轮换同账户
// Key 池，池穷尽后账户失败。
func TestDispatchKeyScopedTransportFailureRotatesKeys(t *testing.T) {
	engine, driver, dispatcher := newTestEngine(t)
	dispatcher.keyScopedFailure.Store(true)
	driver.urlByAccount = map[string][]string{
		"a-1": {"https://127.0.0.1:9/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{multiKeyTestAccount("a-1", "key-a", "key-b")}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, accounts))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if dispatcher.handleErrorCalls.Load() < 2 {
		t.Fatalf("Key 轮换必须重试错误处理, calls = %d", dispatcher.handleErrorCalls.Load())
	}
	// 池穷尽后 lastAttempt 保留最后一次传输失败（shouldRetry 的池穷尽分支先于
	// Key 选择失败）。
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.TransportFailureKind != TransportFailureKindConnection {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

// partsErrorDriver 覆盖请求部件构建以注入准备错误（可按账户限定）。
type partsErrorDriver struct {
	fakeDriver
	err            error
	errByAccountID map[string]error
}

func (f *partsErrorDriver) BuildGatewayUpstreamRequestParts(ctx context.Context, req *gatewaypreauth.GatewayRequest, account AccountCandidate, identity UsageIdentity, requestClientCompatibility string) (PreparedRequestParts, error) {
	if f.err != nil {
		return PreparedRequestParts{}, f.err
	}
	if accountErr, ok := f.errByAccountID[account.ID]; ok {
		return PreparedRequestParts{}, accountErr
	}
	return f.fakeDriver.BuildGatewayUpstreamRequestParts(ctx, req, account, identity, requestClientCompatibility)
}

func TestDispatchAccountScopedGuidancePreparationError(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	partsDriver := &partsErrorDriver{errByAccountID: map[string]error{
		"a-1": &gatewaypreauth.GatewayAgentGuidanceResponse{Message: "该账户不支持此请求"},
	}}
	partsDriver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	engine.Driver = partsDriver
	_ = driver
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1", "a-2")))
	if err != nil {
		t.Fatalf("账户级 guidance 后第二个账户必须接管: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

func TestDispatchRequestScopedGuidancePreparationErrorRethrows(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	accountScopedFalse := false
	guidanceErr := &gatewaypreauth.GatewayAgentGuidanceResponse{Message: "全局 guidance", AccountScoped: &accountScopedFalse}
	partsDriver := &partsErrorDriver{errByAccountID: map[string]error{"a-1": guidanceErr}}
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	partsDriver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	engine.Driver = partsDriver
	_ = driver
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	if !errors.Is(err, guidanceErr) {
		t.Fatalf("请求级 guidance 必须重抛, got %v", err)
	}
}

func TestDispatchValidationAndAdapterPreparationErrorsRethrow(t *testing.T) {
	for index, failure := range []error{
		&gatewaypreauth.GatewayRequestValidationError{Message: "校验"},
		NewOpenAIOAuthCodexAdapterError("adapter"),
	} {
		engine, driver, _ := newTestEngine(t)
		partsDriver := &partsErrorDriver{errByAccountID: map[string]error{"a-1": failure}}
		okServer := sequentialServer(t, 0, 500)
		partsDriver.urlByAccount = map[string][]string{
			"a-1": {okServer.URL + "/v1/chat/completions"},
		}
		engine.Driver = partsDriver
		_ = driver
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
		if !errors.Is(err, failure) {
			t.Fatalf("case %d: 必须重抛 %v, got %v", index, failure, err)
		}
		okServer.Close()
	}
}

// keyScopedErrorDriver 的错误由 FailureDispatcher 判定为 Key 级。
func TestDispatchPreparationKeyScopedErrorRotatesKey(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, dispatcher := newTestEngine(t)
	dispatcher.keyScopedFailure.Store(true)
	keyPartsDriver := &partsErrorDriver{errByAccountID: map[string]error{"a-1": errors.New("准备失败")}}
	keyPartsDriver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	engine.Driver = keyPartsDriver
	_ = driver
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{multiKeyTestAccount("a-1", "key-a", "key-b")}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, accounts))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	// 两次 Key 各自经历一次准备错误处理（Key 级失败驱动轮换）。
	if dispatcher.handleErrorCalls.Load() < 2 {
		t.Fatalf("Key 级准备失败必须轮换 Key, calls = %d", dispatcher.handleErrorCalls.Load())
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "account:preparation" {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

// TestDispatchUnavailableProxyThenSharedProxySkip: a-1 代理不可用被记入失败
// 代理键，共享同一代理画像的 a-2 直接以 proxy:skipped 跳过。
func TestDispatchUnavailableProxyThenSharedProxySkip(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	profileID := "proxy-1"
	account1 := testAccounts("a-1")[0]
	account1.ProxyProfileID = &profileID
	unavailable := true
	message := "代理维护中"
	account1.ProxyProfileUnavailable = &unavailable
	account1.ProxyProfileErrorMessage = &message
	account2 := testAccounts("a-2")[0]
	account2.ProxyProfileID = &profileID
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	usage := testUsageContext()
	usage.TrafficSource = "probe" // 非网关流量允许账户状态变更分支
	args := fastDispatchArgs(t, req, []AccountCandidate{account1, account2})
	args.UsageContext = usage
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	// a-2 与 a-1 共享失败代理画像：直接以 proxy:skipped 跳过。
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "proxy:skipped" {
		t.Fatalf("共享代理画像必须被跳过, got %#v", attemptErr.LastAttempt)
	}
	if attemptErr.LastAttempt.AccountID != "a-2" {
		t.Fatalf("account = %s", attemptErr.LastAttempt.AccountID)
	}
	if len(engine.Usage.(*fakeUsage).records) == 0 {
		t.Fatal("代理不可用必须记录失败尝试")
	}
}

// unavailableQueue 恒返回未就绪的排队结果。
type unavailableQueue struct{}

func (unavailableQueue) WaitForCapacity(context.Context, HighConcurrencyWaitInput) (QueueWaitResult, error) {
	return QueueWaitResult{Ready: false, Reason: "timeout"}, nil
}

func TestDispatchCapacityLimitWithPolicyQueueWaits(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	store := &fakeConcurrencyStore{exhausted: true, limit: 4}
	engine.Concurrency = store
	engine.HighConcurrencyQueue = unavailableQueue{}
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	// 服务器重试预算收窄到 120ms：容量失败后短暂排队 → 预算耗尽 → 记录容量失败。
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(120, gatewaypreauth.SystemClock{})
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "concurrency:limit" {
		t.Fatalf("容量失败必须成为最后尝试, got %#v", attemptErr.LastAttempt)
	}
	if !strings.Contains(attemptErr.LastAttempt.Message, "账户并发已达到上限") {
		t.Fatalf("message = %q", attemptErr.LastAttempt.Message)
	}
}

func TestDispatchCapacityLimitWithoutPolicyRetriesThenFails(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	store := &fakeConcurrencyStore{exhausted: true, limit: 4}
	engine.Concurrency = store
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(120, gatewaypreauth.SystemClock{})
	args.GroupSchedulingPolicy = nil
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "concurrency:limit" {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

// switchableSuppression 支持运行中切换每个账户的抑制状态。
type switchableSuppression struct {
	mu        sync.Mutex
	suppress  map[string]bool
	callCount int
}

func (s *switchableSuppression) set(accountID string, suppressed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suppress[accountID] = suppressed
}

func (s *switchableSuppression) FilterAsync(_ context.Context, accounts []AccountCandidate, _ SuppressionFilterOptions) (SuppressionFilterResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callCount++
	result := SuppressionFilterResult{Accounts: []AccountCandidate{}, SuppressedAccountIDs: []string{}, AcquiredHalfOpenLeases: []HalfOpenLease{}}
	for _, account := range accounts {
		if s.suppress[account.ID] {
			result.SuppressedAccountIDs = append(result.SuppressedAccountIDs, account.ID)
			result.SuppressedCount++
		} else {
			result.Accounts = append(result.Accounts, account)
		}
	}
	result.AllSuppressed = len(accounts) > 0 && len(result.Accounts) == 0
	return result, nil
}

func (s *switchableSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

// readyWaiter 第一次等待即报告恢复（不再全部抑制）。
type readyWaiter struct{}

func (readyWaiter) WaitForState(_ context.Context, input SuppressionWaitInput) (SuppressionFilterResult, error) {
	state, err := input.Refresh(context.Background())
	if err != nil {
		return SuppressionFilterResult{}, err
	}
	return state, nil
}

func TestDispatchRecoverableWaitExhausted(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	suppression := &switchableSuppression{suppress: map[string]bool{"a-1": true}}
	engine.Suppression = suppression
	engine.RecoverableWait = exhaustedWaiter{}
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.WaitForRecoverableFailures = true
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(60, gatewaypreauth.SystemClock{})
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.Message != "所有上游账户仍处于本地短期屏蔽" {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

// exhaustedWaiter 恒报告全部抑制（等待超时语义）。
type exhaustedWaiter struct{}

func (exhaustedWaiter) WaitForState(context.Context, SuppressionWaitInput) (SuppressionFilterResult, error) {
	return SuppressionFilterResult{AllSuppressed: true}, nil
}

func TestDispatchRecoverableWaitRecoversAndDispatches(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	suppression := &switchableSuppression{suppress: map[string]bool{"a-1": true}}
	engine.Suppression = suppression
	engine.RecoverableWait = readyWaiter{}
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	// 第一次 FilterAsync 后解除抑制（post-cycle 等待 Refresh 时读到新状态）。
	suppression.set("a-1", false)
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.WaitForRecoverableFailures = true
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("恢复后必须完成调度: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

// returnResponseDispatcher 把失败响应原样交回调用方（Node return_response）。
type returnResponseDispatcher struct {
	fakeFailureDispatcher
}

func (f *returnResponseDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	f.handleFailedCalls.Add(1)
	return FailedUpstreamResponseResult{Action: FailedResponseActionReturnResponse, Response: input.Response}, nil
}

func TestDispatchReturnResponseActionSelectsFailedResponse(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	engine.FailureDispatcher = &returnResponseDispatcher{}
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	}))
	defer failServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	if err != nil {
		t.Fatalf("return_response 动作必须原样返回失败响应: %v", err)
	}
	if result.Response == nil || result.Response.OK() || result.Response.Status() != http.StatusTooManyRequests {
		t.Fatalf("status = %v", result.Response)
	}
}

func TestDispatchLockBlocksAfterFailedResponse(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	engine.Locks = &blockingLocks{}
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.Status != http.StatusBadGateway {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

func TestDispatchAttemptDeduplicated(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	// 预登记同一物理凭据：真实尝试的注册被拒 → 请求去重。
	_, regErr := args.RequestCoordination.RequestAttemptTracker.TryRecordDispatchAttempt(gatewayrouting.GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: gatewayrouting.GatewayDispatchAttemptIdentity{
			AccountRuntimeKey:     "a-1",
			PhysicalCredentialKey: "a-1",
			ProtocolModelKey:      "protocol-model",
		},
	})
	if regErr != nil {
		t.Fatalf("预登记: %v", regErr)
	}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	// 预登记的物理凭据在候选入口即被过滤：无任何尝试产生。
	if attemptErr.LastAttempt != nil {
		t.Fatalf("入口去重不应产生尝试, got %#v", attemptErr.LastAttempt)
	}
}

func TestDispatchWithCircuitServiceWired(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	store, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 1024})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	circuits, err := gatewaycircuit.NewCircuitService(store, gatewaycircuit.ServiceOptions{})
	if err != nil {
		t.Fatalf("circuit service: %v", err)
	}
	engine, driver, _ := newTestEngine(t)
	engine.Circuits = circuits
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	if err != nil {
		t.Fatalf("装配短电路后必须照常调度: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

func TestDispatchAbortBeforeUpstreamStartSkipsRecording(t *testing.T) {
	engine, driver, dispatcher := newTestEngine(t)
	dead := "https://127.0.0.1:9/v1/chat/completions"
	driver.urlByAccount = map[string][]string{"a-1": {dead}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	ctx, cancel := context.WithCancel(context.Background())
	args.Signal = ctx
	args.RequestCoordination.OnUpstreamAttemptStarted = func(AccountCandidate, string) {
		cancel() // 传输发出前取消：未开始的取消不记录失败处理
	}
	defer cancel()
	_, err := engine.FetchFirstAvailableUpstream(ctx, args)
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) {
		t.Fatalf("expected abort, got %v", err)
	}
	if aborted.UpstreamRequestStarted {
		t.Fatal("传输未发出的取消不应标记已开始")
	}
	if dispatcher.handleErrorCalls.Load() != 0 {
		t.Fatalf("未开始的取消不应记录错误处理, calls = %d", dispatcher.handleErrorCalls.Load())
	}
	// 亲和被遗忘。
	if len(engine.Affinity.(*fakeAffinity).forgotten) != 1 {
		t.Fatal("取消后必须遗忘会话亲和")
	}
}

func TestDispatchUnsafeURLUnprovenFailureRethrows(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {"://not-a-valid-url"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	if err == nil {
		t.Fatal("未证实的传输失败必须重抛")
	}
	var attemptErr *UpstreamAttemptError
	if errorsAs(err, &attemptErr) {
		t.Fatalf("未证实的失败直接重抛, got %#v", attemptErr)
	}
}

// TestDispatchObservedEscapedTierMetadata: 次级候选的 tier 逃逸被观测。
func TestDispatchObservedEscapedTierMetadata(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {}, // 无 URL → 跳过
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1", "a-2")))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

// TestDispatchOnUpstreamAttemptStartedHookFires: 尝试开始钩子按次触发。
func TestDispatchOnUpstreamAttemptStartedHookFires(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	var started atomic.Int64
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination.OnUpstreamAttemptStarted = func(AccountCandidate, string) {
		started.Add(1)
	}
	if _, err := engine.FetchFirstAvailableUpstream(context.Background(), args); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if started.Load() != 1 {
		t.Fatalf("started hook calls = %d", started.Load())
	}
}

// TestDispatchResponsePrecommitDeadlinePopulated: 非 image 通道且墙钟有界时
// 成功结果携带响应预提交截止。
func TestDispatchResponsePrecommitDeadlinePopulated(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.ResponsePrecommitDeadlineAtMs == nil {
		t.Fatal("成功结果必须携带响应预提交截止")
	}
	if result.ReleaseConcurrency == nil || result.MarkFirstOutput == nil {
		t.Fatal("并发释放与首字标记必须存在")
	}
	result.MarkFirstOutput()
	// Confirm 闭包可安全调用（无装配端口时中性）。
	if err := result.ConfirmSameAccountApiKeyFailures(); err != nil {
		t.Fatalf("ConfirmSameAccountApiKeyFailures: %v", err)
	}
	if err := result.ConfirmAccountAPIKeySuccess(); err != nil {
		t.Fatalf("ConfirmAccountAPIKeySuccess: %v", err)
	}
	if err := result.ConfirmAccountLockSuccess(); err != nil {
		t.Fatalf("ConfirmAccountLockSuccess: %v", err)
	}
	if result.ConfirmHalfOpenSuccess() {
		t.Fatal("未启用自动状态变更时 half-open 成功确认返回 false")
	}
}

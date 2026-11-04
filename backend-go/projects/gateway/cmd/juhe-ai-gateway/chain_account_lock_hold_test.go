package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// chainLockHoldAudit 记录链面审计标签与载荷，用于断言锁死门的审计事件。
type chainLockHoldAudit struct {
	labels   []string
	metadata []map[string]any
}

func (a *chainLockHoldAudit) BindContext(gatewaypreauth.AuditGatewayContext) {}

func (a *chainLockHoldAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	a.labels = append(a.labels, label)
	a.metadata = append(a.metadata, metadata)
}

func (a *chainLockHoldAudit) Finalize(gatewaypreauth.AuditFinalizeInput) {}

func (a *chainLockHoldAudit) has(label string) bool {
	for _, recorded := range a.labels {
		if recorded == label {
			return true
		}
	}
	return false
}

func (a *chainLockHoldAudit) payloadOf(label string) map[string]any {
	for index, recorded := range a.labels {
		if recorded == label {
			return a.metadata[index]
		}
	}
	return nil
}

// 锁死窗口的链面终局测试（AI 账户锁死调度机制设计与实现前复审 §3.4）：
// 锁死 hold 走既有 client handoff / 503 契约，不切后备分组；响应检查换号
// 被锁死门否决时不写排除集、不计数、不切组。

type chainLockHoldLocks struct {
	states         map[string]gatewaydispatch.AccountLockStateView
	err            error
	lockedOnRecord bool
	recorded       map[string]bool
	recordCalls    int
}

func (l *chainLockHoldLocks) FindStateAsync(_ context.Context, accountID string) (*gatewaydispatch.AccountLockStateView, error) {
	if l.err != nil {
		return nil, l.err
	}
	state, ok := l.states[accountID]
	if !ok {
		return nil, nil
	}
	if l.lockedOnRecord && l.recorded[accountID] {
		state.BlocksCrossAccount = true
	}
	return &state, nil
}

func (l *chainLockHoldLocks) AcquireRetryLeaseAsync(context.Context, string, int64) (gatewaydispatch.LockLeaseAcquire, error) {
	return gatewaydispatch.LockLeaseAcquire{Allowed: true}, nil
}

func (l *chainLockHoldLocks) ConsumeRetryLeaseAsync(context.Context, string, string) (bool, error) {
	return true, nil
}

func (l *chainLockHoldLocks) ReleaseRetryLeaseAsync(context.Context, gatewaydispatch.ReleaseRetryLeaseInput) (bool, error) {
	return true, nil
}

func (l *chainLockHoldLocks) AbandonRetryReservationAsync(context.Context, gatewaydispatch.AccountLockRetryLease) error {
	return nil
}

func (l *chainLockHoldLocks) RecordFailureAsync(_ context.Context, accountID, _ string, _ *gatewaydispatch.AccountLockObservation) error {
	l.recordCalls++
	if l.recorded == nil {
		l.recorded = map[string]bool{}
	}
	l.recorded[accountID] = true
	return nil
}

func (l *chainLockHoldLocks) SettleDeadlineAsync(context.Context, string, int64, *gatewaydispatch.AccountLockObservation) error {
	return nil
}

func (l *chainLockHoldLocks) ListStatesAsync(_ context.Context, accountIDs []string) (map[string]gatewaydispatch.AccountLockStateView, error) {
	if l.err != nil {
		return nil, l.err
	}
	states := make(map[string]gatewaydispatch.AccountLockStateView, len(accountIDs))
	for _, accountID := range accountIDs {
		if state, ok := l.states[accountID]; ok {
			states[accountID] = state
		}
	}
	return states, nil
}

func (l *chainLockHoldLocks) CompleteSuccessAsync(context.Context, string, string, *gatewaydispatch.AccountLockObservation) error {
	return nil
}

// TestChainSettleDispatchErrorAccountLockHoldRenderHandoff：锁死 hold 错误
// 直接渲染 503 / upstream_retryable_error，绝不进入后备分组。
func TestChainSettleDispatchErrorAccountLockHoldRenderHandoff(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	loop.exhaustedAccounts = map[string]struct{}{"a-1": {}}
	lockHold := &gatewaydispatch.AccountLockHoldError{
		AccountID: "a-1",
		Message:   "账户处于锁死窗口，窗口内不切换其他账户",
	}

	if settled := loop.settleDispatchError(context.Background(), lockHold); !settled {
		t.Fatal("锁死 hold 必须终结本次请求")
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("响应渲染次数 = %d，期望 1", len(sink.inputs))
	}
	input := sink.inputs[0]
	if input.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d，期望 503", input.StatusCode)
	}
	if input.ResponsePayload.Error.Code != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatalf("errorCode = %q", input.ResponsePayload.Error.Code)
	}
	if loop.fallbackSwitches != 0 {
		t.Fatalf("fallbackSwitches = %d，锁死窗口内不得切组", loop.fallbackSwitches)
	}
	if !audit.has("account_lock_hold_dispatch_denied") {
		t.Fatalf("缺少 account_lock_hold_dispatch_denied 审计：%v", audit.labels)
	}
	payload := audit.payloadOf("account_lock_hold_dispatch_denied")
	if payload["accountId"] != "a-1" {
		t.Fatalf("audit accountId = %#v", payload["accountId"])
	}
	if payload["errorMessage"] != lockHold.Message {
		t.Fatalf("audit errorMessage = %#v", payload["errorMessage"])
	}
}

// TestChainSwitchToFallbackGroupAccountLockHoldDenied：请求级耗尽集含锁死
// 账号时，切组被拒绝；读取失败同样 fail-closed。
func TestChainSwitchToFallbackGroupAccountLockHoldDenied(t *testing.T) {
	cases := []struct {
		name  string
		locks *chainLockHoldLocks
	}{
		{
			name: "blocked",
			locks: &chainLockHoldLocks{states: map[string]gatewaydispatch.AccountLockStateView{
				"a-1": {BlocksCrossAccount: true},
			}},
		},
		{
			name:  "read_error_fail_closed",
			locks: &chainLockHoldLocks{err: context.DeadlineExceeded},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loop := newV1TestLoop(t, &recordingFailureSink{})
			audit := &chainLockHoldAudit{}
			loop.auditCapture = audit
			loop.c.engine = &gatewaydispatch.Engine{Locks: testCase.locks}
			loop.exhaustedAccounts = map[string]struct{}{"a-1": {}}

			switched, err := loop.switchToFallbackGroup(context.Background(), "upstream_accounts_exhausted")
			if err != nil {
				t.Fatalf("switchToFallbackGroup: %v", err)
			}
			if switched != v1FallbackLockHold {
				t.Fatalf("switched = %q，锁死窗口内必须返回 lock_hold（调用方转客户端交接）", switched)
			}
			if loop.fallbackSwitches != 0 {
				t.Fatalf("fallbackSwitches = %d", loop.fallbackSwitches)
			}
			if !audit.has("account_lock_fallback_denied") {
				t.Fatalf("缺少 account_lock_fallback_denied 审计：%v", audit.labels)
			}
			payload := audit.payloadOf("account_lock_fallback_denied")
			if payload["reason"] != "upstream_accounts_exhausted" {
				t.Fatalf("audit reason = %#v", payload["reason"])
			}
			if ids, ok := payload["exhaustedAccountIds"].([]string); !ok || len(ids) != 1 || ids[0] != "a-1" {
				t.Fatalf("audit exhaustedAccountIds = %#v", payload["exhaustedAccountIds"])
			}
		})
	}
}

// TestChainSettleResponseStreamServerRetryAccountLockHold：响应检查要求换号
// 但当前账号锁死时渲染 503；不写 streamRetryExcludedAccounts、不增加服务端
// 重试计数、不切组。
func TestChainSettleResponseStreamServerRetryAccountLockHold(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	loop.c.engine = &gatewaydispatch.Engine{Locks: &chainLockHoldLocks{states: map[string]gatewaydispatch.AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	}}}
	handling := gatewayresponse.UpstreamResponseHandlingResult{
		RetryReason:           gatewayresponse.StreamServerRetryResponseInspection,
		ErrorCode:             "policy_blocked",
		ExcludeCurrentAccount: true,
		ResponseInspection: &gatewayresponse.ResponseInspectionDecision{
			PolicyID:   "policy-1",
			PolicyName: "测试策略",
		},
	}
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "a-1"},
	}

	if settled := loop.settleResponseStreamServerRetry(context.Background(), dispatched, handling); !settled {
		t.Fatal("锁死窗口内响应检查换号必须终结本次请求")
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("响应渲染次数 = %d，期望 1", len(sink.inputs))
	}
	input := sink.inputs[0]
	if input.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d，期望 503", input.StatusCode)
	}
	if input.ResponsePayload.Error.Code != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatalf("errorCode = %q", input.ResponsePayload.Error.Code)
	}
	if len(loop.streamRetryExcludedAccounts) != 0 {
		t.Fatalf("streamRetryExcludedAccounts = %#v，锁死窗口内不得写排除集", loop.streamRetryExcludedAccounts)
	}
	if loop.streamServerRetryCount != 0 {
		t.Fatalf("streamServerRetryCount = %d，锁死窗口内不得计数", loop.streamServerRetryCount)
	}
	if loop.fallbackSwitches != 0 {
		t.Fatalf("fallbackSwitches = %d", loop.fallbackSwitches)
	}
	if !audit.has("account_lock_response_inspection_retry_denied") {
		t.Fatalf("缺少 account_lock_response_inspection_retry_denied 审计：%v", audit.labels)
	}
	if audit.has("stream_server_retry_dispatch") {
		t.Fatalf("锁死窗口内不得进入换号派发审计：%v", audit.labels)
	}
	payload := audit.payloadOf("account_lock_response_inspection_retry_denied")
	if payload["accountId"] != "a-1" || payload["errorCode"] != "policy_blocked" {
		t.Fatalf("audit payload = %#v", payload)
	}
	if payload["policyId"] != "policy-1" || payload["policyName"] != "测试策略" {
		t.Fatalf("audit policy = %#v", payload)
	}
}

// TestChainSettleResponseStreamServerRetryNilEngineKeepsBehavior：engine / Locks
// 未装配（newV1TestLoop 常见形态）时不得 panic，保持既有换号行为。
func TestChainSettleResponseStreamServerRetryNilEngineKeepsBehavior(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	loop.current.Accounts = []gatewayruntimecache.OpenAIAccountSecret{{ID: "a-1"}, {ID: "a-2"}}
	handling := gatewayresponse.UpstreamResponseHandlingResult{
		RetryReason:           gatewayresponse.StreamServerRetryResponseInspection,
		ErrorCode:             "policy_blocked",
		ExcludeCurrentAccount: true,
		ResponseInspection:    &gatewayresponse.ResponseInspectionDecision{PolicyID: "policy-1"},
	}
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "a-1"},
	}

	if settled := loop.settleResponseStreamServerRetry(context.Background(), dispatched, handling); settled {
		t.Fatal("engine 未装配时必须保持既有换号行为（继续派发）")
	}
	if _, ok := loop.streamRetryExcludedAccounts["a-1"]; !ok {
		t.Fatal("既有行为应写入 streamRetryExcludedAccounts")
	}
	if loop.streamServerRetryCount != 1 {
		t.Fatalf("streamServerRetryCount = %d，期望 1", loop.streamServerRetryCount)
	}
}

// TestChainSettleResponseStreamServerRetryLockedIdleFirstFailure：LOCKED_IDLE
// 账户首次 200 内容失败时，链面必须先推进锁死窗口（RecordFailureAsync）再
// 否决换号；不能因为初始 BlocksCrossAccount=false 就放行。
func TestChainSettleResponseStreamServerRetryLockedIdleFirstFailure(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	locks := &chainLockHoldLocks{
		states: map[string]gatewaydispatch.AccountLockStateView{
			"a-1": {BlocksCrossAccount: false},
		},
		lockedOnRecord: true,
		recorded:       map[string]bool{},
	}
	loop.c.engine = &gatewaydispatch.Engine{Locks: locks}
	handling := gatewayresponse.UpstreamResponseHandlingResult{
		RetryReason:           gatewayresponse.StreamServerRetryResponseInspection,
		ErrorCode:             "policy_blocked",
		ExcludeCurrentAccount: true,
		ResponseInspection:    &gatewayresponse.ResponseInspectionDecision{PolicyID: "policy-1"},
	}
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "a-1"},
	}

	if settled := loop.settleResponseStreamServerRetry(context.Background(), dispatched, handling); !settled {
		t.Fatal("LOCKED_IDLE 首次响应检查失败必须推进窗口并终结请求")
	}
	if locks.recordCalls == 0 {
		t.Fatal("必须调用 RecordFailureAsync 推进锁死窗口")
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("响应渲染 = %#v，期望 503", sink.inputs)
	}
	if len(loop.streamRetryExcludedAccounts) != 0 {
		t.Fatalf("streamRetryExcludedAccounts = %#v，锁死窗口内不得写排除集", loop.streamRetryExcludedAccounts)
	}
	if loop.streamServerRetryCount != 0 {
		t.Fatalf("streamServerRetryCount = %d，锁死窗口内不得计数", loop.streamServerRetryCount)
	}
	if !audit.has("account_lock_response_inspection_retry_denied") {
		t.Fatalf("缺少 account_lock_response_inspection_retry_denied 审计：%v", audit.labels)
	}
}

// TestChainAccountLockHoldInterruptsDownstream：可 Hijack 的下游上，锁死 hold
// 必须真·断开连接（客户端断连后重试），不得再写 503。
func TestChainAccountLockHoldInterruptsDownstream(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	loop.exhaustedAccounts = map[string]struct{}{"a-1": {}}
	lockHold := &gatewaydispatch.AccountLockHoldError{
		AccountID: "a-1",
		Message:   "账户处于锁死窗口，窗口内不切换其他账户",
	}

	if settled := loop.settleDispatchError(context.Background(), lockHold); !settled {
		t.Fatal("锁死 hold 必须终结本次请求")
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d，期望真断开一次", fake.hijackCalls, conn.closeCalls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("真断开时不得再写 503：%#v", sink.inputs)
	}
	payload := audit.payloadOf("account_lock_hold_dispatch_denied")
	if payload["handoff"] != "connection_interrupt" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

// TestChainAccountLockHoldFallsBackTo503WhenHijackUnavailable：底层不支持
// Hijack（能力缺失，典型为 HTTP/2 直连）时必须在提交响应头前回退 503
// client retry 契约。
func TestChainAccountLockHoldFallsBackTo503WhenHijackUnavailable(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	lockHold := &gatewaydispatch.AccountLockHoldError{
		AccountID: "a-1",
		Message:   "账户处于锁死窗口，窗口内不切换其他账户",
	}

	if settled := loop.settleDispatchError(context.Background(), lockHold); !settled {
		t.Fatal("锁死 hold 必须终结本次请求")
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Hijack 能力缺失必须回退 503：%#v", sink.inputs)
	}
	payload := audit.payloadOf("account_lock_hold_dispatch_denied")
	if payload["handoff"] != "http_503_fallback" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

// TestChainAccountLockHoldRuntimeHijackFailureDegrades：能力存在但 Hijack
// 运行期失败时 200 头已提交，无法回退 503——审计必须记录降级为截断正文。
func TestChainAccountLockHoldRuntimeHijackFailureDegrades(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	fake := &interruptFakeWriter{
		conn:      &interruptFakeConn{},
		sink:      &strings.Builder{},
		hijackErr: errors.New("hijack refused"),
	}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	lockHold := &gatewaydispatch.AccountLockHoldError{
		AccountID: "a-1",
		Message:   "账户处于锁死窗口，窗口内不切换其他账户",
	}

	if settled := loop.settleDispatchError(context.Background(), lockHold); !settled {
		t.Fatal("锁死 hold 必须终结本次请求")
	}
	if fake.hijackCalls != 1 {
		t.Fatalf("hijackCalls = %d，期望尝试一次", fake.hijackCalls)
	}
	if !loop.res.HeadersSent() {
		t.Fatal("非流式交接必须先提交 200 传输头，代理才会透传截断")
	}
	payload := audit.payloadOf("account_lock_hold_dispatch_denied")
	if payload["handoff"] != "response_headers_committed_hijack_failed" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

// TestChainAccountLockHoldStreamingHandoffCommitsSSEThenDisconnects：流式请求
// 必须先提交 SSE 头再断开，否则反向代理会把 pre-commit 断开合成 502，客户端
// 不会按网络错误重试。
func TestChainAccountLockHoldStreamingHandoffCommitsSSEThenDisconnects(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.req = switchTargetRequest(t, "/v1/chat/completions",
		`{"model":"gpt-test","stream":true}`, "gpt-test", true)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	lockHold := &gatewaydispatch.AccountLockHoldError{
		AccountID: "a-1",
		Message:   "账户处于锁死窗口，窗口内不切换其他账户",
	}

	if settled := loop.settleDispatchError(context.Background(), lockHold); !settled {
		t.Fatal("锁死 hold 必须终结本次请求")
	}
	if !loop.res.HeadersSent() {
		t.Fatal("流式交接必须先提交 SSE 响应头")
	}
	if loop.res.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", loop.res.Header().Get("Content-Type"))
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d，期望提交后真断开一次", fake.hijackCalls, conn.closeCalls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("流式真断开时不得再写 503：%#v", sink.inputs)
	}
	payload := audit.payloadOf("account_lock_hold_dispatch_denied")
	if payload["handoff"] != "stream_committed_disconnect" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

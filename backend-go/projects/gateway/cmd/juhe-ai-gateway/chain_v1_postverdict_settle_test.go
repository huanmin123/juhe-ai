package main

// BUG-0267 post-verdict 结算块的分类矩阵测试（Node routes.ts:1780-1877 判定与
// 结算表 + :2509-2521 finally 兜底）。判定映射错误 = 写错证据方向（本档病灶），
// 每类终态必须断言 circuit / keyModel / 账户锁三个消费者各自收到的调用。
//
// 引擎侧（attemptoutcomes.go OK 臂）不再前置 ReportFramingComplete、句柄经
// UpstreamDispatchResult 带出的事实由 gatewaydispatch 包的
// TestCircuitAuditOkBranchCarriesAttemptHandlesToResult 传导断言锁定（复审 P0
// 的回归锁：赋值缺失时链面结算块是死代码且行为测试全绿）；本文件直调结算
// 函数（与 TestV1HalfOpenLeaseConfirmOnProtocolSuccess 同形态），以计数闭包
// 句柄隔离被测分类逻辑。

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// ---------------------------------------------------------------------------
// 计数闭包句柄（postVerdict* 接口的测试实现）
// ---------------------------------------------------------------------------

type postVerdictCircuitFake struct {
	mu               sync.Mutex
	transportReports int
	unknownReports   int
	framingReports   int
	lastTransport    gatewaycircuit.TransportFailure
	isConfirmation   bool
	unknownErr       error
	transportErr     error
	framingErr       error
}

func (f *postVerdictCircuitFake) IsConfirmation() bool {
	return f.isConfirmation
}

func (f *postVerdictCircuitFake) ReportTransportFailure(_ context.Context, failure gatewaycircuit.TransportFailure) (gatewaycircuit.FailureDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transportReports++
	f.lastTransport = failure
	return gatewaycircuit.FailureDecision{}, f.transportErr
}

func (f *postVerdictCircuitFake) ReportFramingComplete(_ context.Context) (*gatewaycircuit.MutationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.framingReports++
	return nil, f.framingErr
}

func (f *postVerdictCircuitFake) ReportUnknown(_ context.Context) (*gatewaycircuit.MutationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unknownReports++
	return nil, f.unknownErr
}

type postVerdictKeyModelFake struct {
	mu            sync.Mutex
	successReport int
	notComplete   int
	unknownReport int
}

func (f *postVerdictKeyModelFake) ReportCompleteSuccess(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successReport++
	return nil
}

func (f *postVerdictKeyModelFake) ReportUpstreamNotComplete(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notComplete++
	return nil
}

func (f *postVerdictKeyModelFake) ReportUnknown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unknownReport++
	return nil
}

type postVerdictLockFake struct {
	mu          sync.Mutex
	failures    int
	lastAccount string
	lastReason  string
	lastObs     *gatewaydispatch.AccountLockObservation
}

func (f *postVerdictLockFake) RecordFailureAsync(_ context.Context, accountID, reason string, observation *gatewaydispatch.AccountLockObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures++
	f.lastAccount = accountID
	f.lastReason = reason
	f.lastObs = observation
	return nil
}

// ---------------------------------------------------------------------------
// 分类矩阵（Node routes.ts:1750-1877）
// ---------------------------------------------------------------------------

type postVerdictMatrixCase struct {
	name     string
	handling gatewayresponse.UpstreamResponseHandlingResult
	deadline *gatewayrouting.NormalRouteAttemptFirstByteDeadline
	// 非默认环境（nil = newV1TestLoop 默认 gateway 流量 + 活跃 ctx）。
	trafficSource  string
	abortedContext bool

	expectCircuitTransport int
	expectCircuitUnknown   int
	expectCircuitFraming   int
	expectTransportKind    string
	expectTransportReason  string
	expectKeyModelUnknown  int
	expectKeyModelSuccess  int
	expectKeyModelNotComp  int
	expectLockFailures     int
}

// postVerdictMatrixCases 覆盖 Node 结算表全部分类格（判定映射错误 = 写错证据
// 方向）：body 传输失败 / neutral 终止 / upstream_protocol_failure 重试 /
// explicit_user_policy 重试 / 协议验证成功，外加 hard/neutral 首字截止、中止
// 与下游关闭守卫、wall-budget neutral、非 explicit 的 response_inspection。
func postVerdictMatrixCases() []postVerdictMatrixCase {
	return []postVerdictMatrixCase{
		{
			name: "body_transport_failure_reports_transport_evidence",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				ErrorCode:        "upstream_protocol_failure",
				TransportFailure: &gatewayresponse.StreamTransportFailure{Kind: "read_incomplete", Reason: "上游响应体中断"},
			},
			expectCircuitTransport: 1,
			expectTransportKind:    "read_incomplete",
			expectTransportReason:  "上游响应体中断",
			expectKeyModelNotComp:  1,
			expectLockFailures:     1,
		},
		{
			name: "neutral_gateway_local_failure_settles_unknown",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized:    true,
				GatewayLocalFailure: true,
			},
			expectCircuitUnknown:  1,
			expectKeyModelUnknown: 1,
		},
		{
			name: "request_local_protocol_failure_retry_framing_complete_and_unknown",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream: true,
				RetryReason:   gatewayresponse.StreamServerRetryUpstreamProtocolFailure,
			},
			expectCircuitFraming:  1,
			expectKeyModelUnknown: 1,
		},
		{
			name: "explicit_user_policy_retry_framing_complete_and_unknown",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream:     true,
				RetryReason:       gatewayresponse.StreamServerRetryResponseInspection,
				ResponseInspection: &gatewayresponse.ResponseInspectionDecision{
					ReplayAuthority: "explicit_user_policy",
				},
			},
			expectCircuitFraming:  1,
			expectKeyModelUnknown: 1,
		},
		{
			name: "protocol_validated_success_completes_both",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				ProtocolValidatedSuccess: true,
			},
			expectCircuitFraming:  1,
			expectKeyModelSuccess: 1,
		},
		{
			name: "hard_first_byte_cutover_synthesizes_timeout_transport_failure",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				FirstByteDeadlineCutover: true,
				ErrorCode:                "first_byte_timeout",
				Message:                  "首字超时",
			},
			deadline:               &gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorLaneTimeout},
			expectCircuitTransport: 1,
			expectTransportKind:    "timeout",
			expectTransportReason:  "首字超时",
			expectKeyModelNotComp:  1,
			// Node :1813-1823：hard cutover 不记账户锁失败（调度竞速，非账户
			// 传输证据），但 circuit 侧合成 timeout 传输失败（:1781-1787）。
			expectLockFailures: 0,
		},
		{
			name: "hard_first_byte_cutover_without_message_uses_default_reason",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				FirstByteDeadlineCutover: true,
				ErrorCode:                "first_byte_timeout",
			},
			deadline:               &gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorUncommittedAttempt},
			expectCircuitTransport: 1,
			expectTransportKind:    "timeout",
			expectTransportReason:  "普通路由首字硬截止已到达",
			expectKeyModelNotComp:  1,
		},
		{
			name: "neutral_first_byte_cutover_settles_unknown",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				FirstByteDeadlineCutover: true,
				ErrorCode:                "first_byte_timeout",
				Message:                  "首字超时",
			},
			deadline:               &gatewayrouting.NormalRouteAttemptFirstByteDeadline{LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured},
			expectCircuitUnknown:    1,
			expectKeyModelUnknown:   1,
			expectLockFailures:      0,
		},
		{
			name: "transport_failure_with_downstream_closed_settles_nothing_on_circuit",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				ErrorCode:        "downstream_connection_closed",
				TransportFailure: &gatewayresponse.StreamTransportFailure{Kind: "read_incomplete", Reason: "读中断"},
			},
			expectCircuitTransport: 0,
			expectCircuitUnknown:   0,
			expectCircuitFraming:   0,
			expectKeyModelNotComp:  1,
			expectLockFailures:     0,
		},
		{
			name: "transport_failure_with_aborted_context_settles_nothing_on_circuit",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				TransportFailure: &gatewayresponse.StreamTransportFailure{Kind: "timeout", Reason: "超时"},
			},
			abortedContext:        true,
			expectCircuitTransport: 0,
			expectCircuitUnknown:   0,
			expectCircuitFraming:   0,
			expectKeyModelNotComp:  1,
			expectLockFailures:     0,
		},
		{
			name: "wall_budget_exhausted_is_neutral",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				ErrorCode:        gatewayresponse.GatewayRequestWallBudgetExhaustedCode,
			},
			expectCircuitUnknown:  1,
			expectKeyModelUnknown: 1,
			expectLockFailures:    0,
		},
		{
			// ReplayAuthority 判定（Node :1791-1796）：系统默认重放权威不是
			// explicit_user_policy，keyModel 走 not-complete 臂。
			name: "system_default_replay_authority_not_explicit_policy",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream:      true,
				RetryReason:        gatewayresponse.StreamServerRetryResponseInspection,
				ResponseInspection: &gatewayresponse.ResponseInspectionDecision{ReplayAuthority: "system_default_retry_next_account"},
			},
			expectCircuitFraming: 1,
			expectKeyModelNotComp: 1,
		},
		{
			// 非 gateway 流量（引擎 accountLockTrafficEnabled 同源门）不记
			// 账户锁失败；circuit/keyModel 结算不随流量源变化。
			name: "non_gateway_traffic_skips_lock_record_only",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				TransportFailure: &gatewayresponse.StreamTransportFailure{Kind: "read_incomplete", Reason: "中断"},
			},
			trafficSource:          "account_diagnostic",
			expectCircuitTransport: 1,
			expectTransportKind:    "read_incomplete",
			expectTransportReason:  "中断",
			expectKeyModelNotComp:  1,
			expectLockFailures:     0,
		},
	}
}

// TestV1PostVerdictSettlementMatrix：五类终态（外加守卫与 cutover 变体）×
// circuit / keyModel / 账户锁三消费者的分类矩阵（Node :1780-1877）。
func TestV1PostVerdictSettlementMatrix(t *testing.T) {
	for _, testCase := range postVerdictMatrixCases() {
		t.Run(testCase.name, func(t *testing.T) {
			loop := newV1TestLoop(t, &recordingFailureSink{})
			if testCase.trafficSource != "" {
				loop.current.UsageContext.TrafficSource = testCase.trafficSource
			}
			ctx := context.Background()
			if testCase.abortedContext {
				cancelled, cancel := context.WithCancel(context.Background())
				cancel()
				ctx = cancelled
			}
			circuit := &postVerdictCircuitFake{}
			keyModel := &postVerdictKeyModelFake{}
			lock := &postVerdictLockFake{}
			observation := &gatewaydispatch.AccountLockObservation{Generation: 7, IncidentID: "incident_1"}
			dispatched := gatewaydispatch.UpstreamDispatchResult{
				Account:                  gatewaydispatch.AccountCandidate{ID: "acct_1"},
				NormalRouteFirstByteDeadline: testCase.deadline,
				AccountLockObservation:   observation,
			}
			loop.settlePostVerdictUpstreamAttempts(ctx, dispatched, testCase.handling, circuit, keyModel, lock)

			if circuit.transportReports != testCase.expectCircuitTransport {
				t.Fatalf("circuit ReportTransportFailure 调用数 = %d，期望 %d", circuit.transportReports, testCase.expectCircuitTransport)
			}
			if circuit.unknownReports != testCase.expectCircuitUnknown {
				t.Fatalf("circuit ReportUnknown 调用数 = %d，期望 %d", circuit.unknownReports, testCase.expectCircuitUnknown)
			}
			if circuit.framingReports != testCase.expectCircuitFraming {
				t.Fatalf("circuit ReportFramingComplete 调用数 = %d，期望 %d", circuit.framingReports, testCase.expectCircuitFraming)
			}
			if testCase.expectCircuitTransport > 0 {
				if circuit.lastTransport.Kind != testCase.expectTransportKind {
					t.Fatalf("circuit 传输失败 kind = %q，期望 %q", circuit.lastTransport.Kind, testCase.expectTransportKind)
				}
				if circuit.lastTransport.Reason != testCase.expectTransportReason {
					t.Fatalf("circuit 传输失败 reason = %q，期望 %q", circuit.lastTransport.Reason, testCase.expectTransportReason)
				}
			}
			if keyModel.unknownReport != testCase.expectKeyModelUnknown {
				t.Fatalf("keyModel ReportUnknown 调用数 = %d，期望 %d", keyModel.unknownReport, testCase.expectKeyModelUnknown)
			}
			if keyModel.successReport != testCase.expectKeyModelSuccess {
				t.Fatalf("keyModel ReportCompleteSuccess 调用数 = %d，期望 %d", keyModel.successReport, testCase.expectKeyModelSuccess)
			}
			if keyModel.notComplete != testCase.expectKeyModelNotComp {
				t.Fatalf("keyModel ReportUpstreamNotComplete 调用数 = %d，期望 %d", keyModel.notComplete, testCase.expectKeyModelNotComp)
			}
			if lock.failures != testCase.expectLockFailures {
				t.Fatalf("账户锁 RecordFailureAsync 调用数 = %d，期望 %d", lock.failures, testCase.expectLockFailures)
			}
			if testCase.expectLockFailures > 0 {
				if lock.lastAccount != "acct_1" {
					t.Fatalf("账户锁失败记录 accountID = %q，期望 acct_1", lock.lastAccount)
				}
				if lock.lastReason != "upstream_body_transport_failure" {
					t.Fatalf("账户锁失败记录 reason = %q，期望 upstream_body_transport_failure", lock.lastReason)
				}
				if lock.lastObs != observation {
					t.Fatal("账户锁失败记录必须透传引擎带出的 AccountLockObservation")
				}
			}
		})
	}
}

// TestV1PostVerdictNilHandlesAreNoOp：引擎未装配句柄（nil）时结算块必须整体
// 跳过且不 panic（typed nil 由 postVerdictCircuitHandle 转换防御）。
func TestV1PostVerdictNilHandlesAreNoOp(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	loop.settlePostVerdictUpstreamAttempts(t.Context(),
		gatewaydispatch.UpstreamDispatchResult{},
		gatewayresponse.UpstreamResponseHandlingResult{ProtocolValidatedSuccess: true},
		nil, nil, nil)
	if handle := postVerdictCircuitHandle(nil); handle != nil {
		t.Fatal("nil 熔断句柄装箱后必须保持接口 nil")
	}
	if handle := postVerdictKeyModelHandle(nil); handle != nil {
		t.Fatal("nil key-model 句柄装箱后必须保持接口 nil")
	}
}

// TestV1PostVerdictSettlementErrorsOnlyLog：结算端口出错只记日志，不改写调用
// 流程（与 confirmProtocolSuccessSideEffects 同风格，无 panic、计数照常推进）。
func TestV1PostVerdictSettlementErrorsOnlyLog(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	circuit := &postVerdictCircuitFake{transportErr: errors.New("circuit store down")}
	keyModel := &postVerdictKeyModelFake{}
	loop.settlePostVerdictUpstreamAttempts(t.Context(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acct_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{
			AlreadyFinalized: true,
			TransportFailure: &gatewayresponse.StreamTransportFailure{Kind: "read_incomplete", Reason: "中断"},
		},
		circuit, keyModel, &postVerdictLockFake{})
	if circuit.transportReports != 1 {
		t.Fatalf("传输失败结算出错后调用数 = %d，期望 1（不中断流程）", circuit.transportReports)
	}
}

// ---------------------------------------------------------------------------
// finally 兜底（Node routes.ts:2509-2521 + :2649-2666）
// ---------------------------------------------------------------------------

// TestV1PostVerdictFallbackSettlement：兜底结算的 confirmation 门、双次重试、
// keyModel 无条件 unknown 与幂等前提。
func TestV1PostVerdictFallbackSettlement(t *testing.T) {
	t.Run("nil_handles_are_noop", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		loop.settleTransferredUpstreamAttemptsSafely(t.Context(), "acct_1", nil, nil)
	})

	t.Run("non_confirmation_circuit_settles_keymodel_only", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		circuit := &postVerdictCircuitFake{isConfirmation: false}
		keyModel := &postVerdictKeyModelFake{}
		loop.settleTransferredUpstreamAttemptsSafely(t.Context(), "acct_1", circuit, keyModel)
		if circuit.unknownReports != 0 {
			t.Fatalf("非 confirmation 尝试不得兜底结算 circuit（调用数 %d）", circuit.unknownReports)
		}
		if keyModel.unknownReport != 1 {
			t.Fatalf("keyModel 必须无条件 reportUnknown（调用数 %d）", keyModel.unknownReport)
		}
	})

	t.Run("confirmation_settles_once_on_success", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		circuit := &postVerdictCircuitFake{isConfirmation: true}
		keyModel := &postVerdictKeyModelFake{}
		loop.settleTransferredUpstreamAttemptsSafely(t.Context(), "acct_1", circuit, keyModel)
		if circuit.unknownReports != 1 {
			t.Fatalf("成功后不得继续重试（调用数 %d，期望 1）", circuit.unknownReports)
		}
		if keyModel.unknownReport != 1 {
			t.Fatalf("circuit 成功后 keyModel 仍须 reportUnknown（Node finally 顺序）")
		}
	})

	t.Run("confirmation_retries_twice_then_keeps_intent", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		circuit := &postVerdictCircuitFake{isConfirmation: true, unknownErr: errors.New("store unavailable")}
		keyModel := &postVerdictKeyModelFake{}
		loop.settleTransferredUpstreamAttemptsSafely(t.Context(), "acct_1", circuit, keyModel)
		if circuit.unknownReports != 2 {
			t.Fatalf("持续失败必须恰好重试两次（调用数 %d，期望 2）", circuit.unknownReports)
		}
		if keyModel.unknownReport != 1 {
			t.Fatalf("circuit 结算失败不得阻断 keyModel 兜底（调用数 %d）", keyModel.unknownReport)
		}
	})
}

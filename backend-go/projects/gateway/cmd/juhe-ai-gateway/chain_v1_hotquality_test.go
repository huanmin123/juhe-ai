package main

// BUG-0241 链面热质量终态结算测试：settleHotQualityTerminal 镜像 Node
// routes.ts:1841-1860 的统一结算点（成功终态 completed_response + 终态显式
// 首字样本；诊断透传 upstream_response_failure），以及 W2-C 桥接对
// FirstByteMs 的透传。

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

type recordingChainHotQualityAttempt struct {
	terminals []gatewaydispatch.HotQualityTerminal
}

func (r *recordingChainHotQualityAttempt) MarkFirstByte(*float64) {}

func (r *recordingChainHotQualityAttempt) RecordTerminal(_ context.Context, terminal gatewaydispatch.HotQualityTerminal) {
	r.terminals = append(r.terminals, terminal)
}

// TestV1SettleHotQualityTerminalCompletedResponse：协议验证成功 + 不重试 →
// completed_response / none / request_lifecycle，FirstTokenMs 作为终态显式
// 首字样本透传（Node 成功分支 source 是 request_lifecycle）。
func TestV1SettleHotQualityTerminalCompletedResponse(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	fake := &recordingChainHotQualityAttempt{}
	dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}}
	dispatched.HotQualityAttempt = fake
	firstTokenMs := int64(234)

	loop.settleHotQualityTerminal(context.Background(), dispatched, gatewayresponse.UpstreamResponseHandlingResult{
		FirstTokenMs:             &firstTokenMs,
		ProtocolValidatedSuccess: true,
	})

	if len(fake.terminals) != 1 {
		t.Fatalf("terminals = %d, want 1", len(fake.terminals))
	}
	terminal := fake.terminals[0]
	if terminal.OutcomeClass != gatewaydispatch.HotQualityOutcomeCompletedResponse {
		t.Fatalf("outcomeClass = %s, want completed_response", terminal.OutcomeClass)
	}
	if terminal.FailureScope != "none" || terminal.Source != "request_lifecycle" {
		t.Fatalf("failureScope/source = %s/%s, want none/request_lifecycle", terminal.FailureScope, terminal.Source)
	}
	if terminal.FirstByteMs == nil || *terminal.FirstByteMs != 234 {
		t.Fatalf("firstByteMs = %v, want 234", terminal.FirstByteMs)
	}
}

// TestV1SettleHotQualityTerminalDiagnosticForward：完整转发但未通过协议验证
// （非 2xx 透传 / 校验未过转发，Node diagnosticUpstreamResponse 的
// !ok || !validated 臂）→ upstream_response_failure / none / upstream_response；
// Node 的 firstByteMs 载荷表达式对所有 outcomeClass 臂统一求值，转发响应的
// 首字延迟同样作为显式首字样本携带。
func TestV1SettleHotQualityTerminalDiagnosticForward(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	fake := &recordingChainHotQualityAttempt{}
	dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}}
	dispatched.HotQualityAttempt = fake
	firstTokenMs := int64(321)

	loop.settleHotQualityTerminal(context.Background(), dispatched, gatewayresponse.UpstreamResponseHandlingResult{
		PassthroughUpstreamFailure: true,
		FirstTokenMs:               &firstTokenMs,
	})

	if len(fake.terminals) != 1 {
		t.Fatalf("terminals = %d, want 1", len(fake.terminals))
	}
	terminal := fake.terminals[0]
	if terminal.OutcomeClass != gatewaydispatch.HotQualityOutcomeUpstreamResponseFailure {
		t.Fatalf("outcomeClass = %s, want upstream_response_failure", terminal.OutcomeClass)
	}
	if terminal.FailureScope != "none" || terminal.Source != "upstream_response" {
		t.Fatalf("failureScope/source = %s/%s, want none/upstream_response", terminal.FailureScope, terminal.Source)
	}
	if terminal.FirstByteMs == nil || *terminal.FirstByteMs != 321 {
		t.Fatalf("firstByteMs = %v, want 321", terminal.FirstByteMs)
	}
}

// TestV1SettleHotQualityTerminalRetryAndFinalizedDispatch：留档收口后统一
// 结算点对 RetryUpstream 响应期重试族与 AlreadyFinalized 渲染族的分派
// （BUG-0241，对齐 Node routes.ts:1841-1860 五臂）：
//   - RetryUpstream（非用户策略）→ upstream_response_failure / none /
//     upstream_response（重试轮次是新 attempt，本 attempt 诊断终态收口）；
//   - RetryUpstream（用户配置响应检查策略：决策 ReplayAuthority 为
//     explicit_user_policy）→ explicit_policy_failure / account /
//     explicit_policy；系统默认策略（system_default_retry_next_account）
//     与策略未切号（ReplayAuthority 空）不算显式策略失败，走
//     upstream_response_failure（BUG-0267 判定差对齐）；
//   - AlreadyFinalized + TransportFailure(read_incomplete) →
//     read_interruption / protocol_model / gateway_transport；
//   - AlreadyFinalized + TransportFailure(timeout) → timeout；
//   - AlreadyFinalized 本地渲染族（无 TransportFailure）→ unknown / none /
//     request_lifecycle。
func TestV1SettleHotQualityTerminalRetryAndFinalizedDispatch(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	cases := []struct {
		name             string
		handling         gatewayresponse.UpstreamResponseHandlingResult
		wantOutcomeClass string
		wantFailureScope string
		wantSource       string
		wantFirstByteMs  *float64
	}{
		{
			name: "retry response_inspection",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream: true,
				RetryReason:   gatewayresponse.StreamServerRetryResponseInspection,
			},
			wantOutcomeClass: gatewaydispatch.HotQualityOutcomeUpstreamResponseFailure,
			wantFailureScope: "none",
			wantSource:       "upstream_response",
		},
		{
			name: "retry pre_commit_stream_failure",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream: true,
				RetryReason:   gatewayresponse.StreamServerRetryPreCommitStreamFailure,
			},
			wantOutcomeClass: gatewaydispatch.HotQualityOutcomeUpstreamResponseFailure,
			wantFailureScope: "none",
			wantSource:       "upstream_response",
		},
		{
			name: "retry explicit configured policy",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream: true,
				RetryReason:   gatewayresponse.StreamServerRetryResponseInspection,
				ResponseInspection: &gatewayresponse.ResponseInspectionDecision{
					Reason:          "configured_response_policy",
					ReplayAuthority: "explicit_user_policy",
				},
			},
			wantOutcomeClass: gatewaydispatch.HotQualityOutcomeExplicitPolicyFailure,
			wantFailureScope: "account",
			wantSource:       "explicit_policy",
		},
		{
			// BUG-0267 判定差：系统默认检查策略的重试不计入显式策略失败，
			// 与 Node explicitUserPolicyRetry 对齐（仅用户配置策略授予
			// explicit_user_policy 权威）。
			name: "retry system default inspection policy",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream: true,
				RetryReason:   gatewayresponse.StreamServerRetryResponseInspection,
				ResponseInspection: &gatewayresponse.ResponseInspectionDecision{
					Reason:          "configured_response_policy",
					PolicySource:    gatewayresponse.PolicySourceSystemDefault,
					ReplayAuthority: "system_default_retry_next_account",
				},
			},
			wantOutcomeClass: gatewaydispatch.HotQualityOutcomeUpstreamResponseFailure,
			wantFailureScope: "none",
			wantSource:       "upstream_response",
		},
		{
			// 用户策略但动作未切号（ReplayAuthority 空）：不算显式策略失败。
			name: "retry user policy without account switch",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream: true,
				RetryReason:   gatewayresponse.StreamServerRetryResponseInspection,
				ResponseInspection: &gatewayresponse.ResponseInspectionDecision{
					Reason:       "configured_response_policy",
					PolicySource: gatewayresponse.PolicySourceManagement,
				},
			},
			wantOutcomeClass: gatewaydispatch.HotQualityOutcomeUpstreamResponseFailure,
			wantFailureScope: "none",
			wantSource:       "upstream_response",
		},
		{
			name: "finalized transport read_incomplete",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				TransportFailure: &gatewayresponse.StreamTransportFailure{Kind: "read_incomplete"},
			},
			wantOutcomeClass: gatewaydispatch.HotQualityOutcomeReadInterruption,
			wantFailureScope: "protocol_model",
			wantSource:       "gateway_transport",
		},
		{
			name: "finalized transport timeout",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				TransportFailure: &gatewayresponse.StreamTransportFailure{Kind: "timeout"},
			},
			wantOutcomeClass: gatewaydispatch.HotQualityOutcomeTimeout,
			wantFailureScope: "protocol_model",
			wantSource:       "gateway_transport",
		},
		{
			name: "finalized local render family",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized:    true,
				GatewayLocalFailure: true,
			},
			wantOutcomeClass: gatewaydispatch.HotQualityOutcomeUnknown,
			wantFailureScope: "none",
			wantSource:       "request_lifecycle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &recordingChainHotQualityAttempt{}
			dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}}
			dispatched.HotQualityAttempt = fake
			firstTokenMs := int64(150)
			tc.handling.FirstTokenMs = &firstTokenMs
			wantFirstByte := 150.0
			tc.wantFirstByteMs = &wantFirstByte
			loop.settleHotQualityTerminal(context.Background(), dispatched, tc.handling)
			if len(fake.terminals) != 1 {
				t.Fatalf("terminals = %d, want 1", len(fake.terminals))
			}
			terminal := fake.terminals[0]
			if terminal.OutcomeClass != tc.wantOutcomeClass {
				t.Fatalf("outcomeClass = %s, want %s", terminal.OutcomeClass, tc.wantOutcomeClass)
			}
			if terminal.FailureScope != tc.wantFailureScope || terminal.Source != tc.wantSource {
				t.Fatalf("failureScope/source = %s/%s, want %s/%s", terminal.FailureScope, terminal.Source, tc.wantFailureScope, tc.wantSource)
			}
			if terminal.FirstByteMs == nil || *terminal.FirstByteMs != *tc.wantFirstByteMs {
				t.Fatalf("firstByteMs = %v, want %v", terminal.FirstByteMs, *tc.wantFirstByteMs)
			}
		})
	}
}

// TestEngineMarkFirstOutputSamplesHotQuality：流式即时首字通道接线锁
// （BUG-0241 留档收口）——引擎 markFirstOutput 闭包必须在首个语义输出时
// 调用 hotQualityAttempt.MarkFirstByte（Node routes.ts:1502-1517
// markFirstOutputWithTiming 同构）；若接线被移除，本 needle 失败。
func TestEngineMarkFirstOutputSamplesHotQuality(t *testing.T) {
	source := readSource(t, "../../internal/gatewaydispatch/dispatchsingle.go")
	if !strings.Contains(source, "hotQualityAttempt.MarkFirstByte(&elapsedMs)") {
		t.Fatal("dispatchsingle.go markFirstOutput 闭包必须保留热质量首字即时采样（hotQualityAttempt.MarkFirstByte，BUG-0241）")
	}
}

// TestV1SettleHotQualityTerminalNilHandleNoop：引擎未装配热质量句柄（nil）时
// 结算为中性 no-op。
func TestV1SettleHotQualityTerminalNilHandleNoop(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	loop.settleHotQualityTerminal(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		gatewayresponse.UpstreamResponseHandlingResult{ProtocolValidatedSuccess: true})
}

// ---------------------------------------------------------------------------
// W2-C 桥接透传（chainHotQualityAttemptLifecycle）
// ---------------------------------------------------------------------------

type fakeHotQualityStore struct {
	terminal  *gatewayhotquality.HotQualityRecordTerminalInput
	terminals []gatewayhotquality.HotQualityRecordTerminalInput
}

func (s *fakeHotQualityStore) RecordAttempt(context.Context, gatewayhotquality.HotQualityRecordAttemptInput) (*gatewayhotquality.HotQualityAttemptMutationResult, error) {
	// 返回非 nil result：lifecycle 会读取 result.Status 做 observation 映射，
	// nil 会触发 panic 兜底路径（safego 恢复），不属于干净测试。
	return &gatewayhotquality.HotQualityAttemptMutationResult{Status: "applied"}, nil
}

func (s *fakeHotQualityStore) RecordTerminal(_ context.Context, input gatewayhotquality.HotQualityRecordTerminalInput) (*gatewayhotquality.HotQualityTerminalMutationResult, error) {
	captured := input
	s.terminal = &captured
	s.terminals = append(s.terminals, captured)
	return &gatewayhotquality.HotQualityTerminalMutationResult{Status: "applied"}, nil
}

func (s *fakeHotQualityStore) Get(context.Context, gatewayhotquality.HotQualityScope, *int64) (*gatewayhotquality.HotQualitySnapshot, error) {
	return nil, nil
}

func (s *fakeHotQualityStore) GetTerminal(context.Context, string, *int64) (*gatewayhotquality.HotQualityTerminalRecord, error) {
	return nil, nil
}

func (s *fakeHotQualityStore) Stats(context.Context, *int64) (*gatewayhotquality.HotQualityStoreStats, error) {
	return nil, nil
}

// TestChainHotQualityBridgePassesFirstByteMs：真实 gatewayhotquality lifecycle
// （+fake store）经 W2-C 桥接收到 HotQualityTerminal 时，FirstByteMs 与
// completed_response 原样进入 GatewayHotQualityTerminalInput 并落到存储输入。
func TestChainHotQualityBridgePassesFirstByteMs(t *testing.T) {
	store := &fakeHotQualityStore{}
	runtime := &gatewayhotquality.GatewayHotQualityRuntime{HotQualityStore: store}
	lifecycle, err := gatewayhotquality.NewGatewayHotQualityAttemptLifecycle(gatewayhotquality.GatewayHotQualityAttemptLifecycleInput{
		Runtime:     runtime,
		AttemptID:   "hotq-bridge-1",
		Account:     gatewayhotquality.GatewayHotQualityAccountView{ID: "a-1", ProtocolCode: "openai", ProtocolVersion: "2024"},
		RequestLane: "text",
	})
	if err != nil {
		t.Fatalf("创建 lifecycle 失败: %v", err)
	}
	bridge := &chainHotQualityAttemptLifecycle{lifecycle: lifecycle}
	firstByte := 789.0
	bridge.RecordTerminal(context.Background(), gatewaydispatch.HotQualityTerminal{
		OutcomeClass: gatewaydispatch.HotQualityOutcomeCompletedResponse,
		FailureScope: "none",
		Source:       "request_lifecycle",
		FirstByteMs:  &firstByte,
	})
	if store.terminal == nil {
		t.Fatal("存储层未收到终态")
	}
	if store.terminal.OutcomeClass != "completed_response" {
		t.Fatalf("outcomeClass = %s, want completed_response", store.terminal.OutcomeClass)
	}
	if store.terminal.FirstByteMs == nil || *store.terminal.FirstByteMs != 789 {
		t.Fatalf("firstByteMs = %v, want 789", store.terminal.FirstByteMs)
	}
	// 幂等（Node "只结算一次"）：同一句柄先成功结算后再补记失败终态，
	// 首次结算（completed_response）生效，第二次不再落库。
	bridge.RecordTerminal(context.Background(), gatewaydispatch.HotQualityTerminal{
		OutcomeClass: gatewaydispatch.HotQualityOutcomeTimeout,
		FailureScope: "protocol_model",
		Source:       "gateway_transport",
	})
	if len(store.terminals) != 1 || store.terminals[0].OutcomeClass != "completed_response" {
		t.Fatalf("二次结算必须被幂等拒绝: %+v", store.terminals)
	}
}

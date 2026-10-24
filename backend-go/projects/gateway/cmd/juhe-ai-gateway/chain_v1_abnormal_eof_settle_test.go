package main

// 流式提交边界受限重放与异常终局处置设计 §3.2.2：upstream_stream_abnormal_eof
// 的 post-verdict 失败归因三面（circuit ReportUnknown / 账户锁失败记录 /
// 热质量 upstream_response_failure，keyModel 维持默认臂 ReportUpstreamNot-
// Complete），与 §3.1 窗口资格排除面（preCommitReplayWindowDisabledOf）的
// 判定矩阵。结算消费者句柄复用 postverdict 计数闭包（chain_v1_postverdict_
// settle_test.go 同形态）。

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

// TestV1PostVerdictAbnormalEOFTwoFormsSettleAttribution：异常终局两形态
// （提交后 AlreadyFinalized 断开 / 提交前 RetryUpstream 换号）共用同一归因三面
// ——circuit ReportUnknown（不进 ReportFramingComplete 治愈证据臂）+ 账户锁
// RecordFailureAsync('upstream_stream_abnormal_eof') + keyModel 默认臂
// ReportUpstreamNotComplete。
func TestV1PostVerdictAbnormalEOFTwoFormsSettleAttribution(t *testing.T) {
	handlingForms := []struct {
		name     string
		handling gatewayresponse.UpstreamResponseHandlingResult
	}{
		{
			name: "committed_disconnect_form",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				ErrorCode:        gatewayresponse.UpstreamStreamAbnormalEOFCode,
				Message:          "上游流在协议终止事件前以无法解析的异常事件收尾",
			},
		},
		{
			name: "pre_commit_retry_form",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream:         true,
				RetryReason:           gatewayresponse.StreamServerRetryPreCommitStreamFailure,
				ExcludeCurrentAccount: true,
				ErrorCode:             gatewayresponse.UpstreamStreamAbnormalEOFCode,
				Message:               "上游流在协议终止事件前以无法解析的异常事件收尾",
			},
		},
	}
	for _, form := range handlingForms {
		t.Run(form.name, func(t *testing.T) {
			loop := newV1TestLoop(t, &recordingFailureSink{})
			ctx := context.Background()
			circuit := &postVerdictCircuitFake{}
			keyModel := &postVerdictKeyModelFake{}
			lock := &postVerdictLockFake{}
			observation := &gatewaydispatch.AccountLockObservation{Generation: 3, IncidentID: "incident_w22"}
			dispatched := gatewaydispatch.UpstreamDispatchResult{
				Account:                gatewaydispatch.AccountCandidate{ID: "acct_w22"},
				AccountLockObservation: observation,
			}
			loop.settlePostVerdictUpstreamAttempts(ctx, dispatched, form.handling, circuit, keyModel, lock)

			// circuit：ReportUnknown，不计传输失败、更不得当治愈证据。
			if circuit.unknownReports != 1 || circuit.transportReports != 0 || circuit.framingReports != 0 {
				t.Fatalf("circuit = unknown:%d transport:%d framing:%d, want 1/0/0",
					circuit.unknownReports, circuit.transportReports, circuit.framingReports)
			}
			// keyModel：默认臂 ReportUpstreamNotComplete（现状同款）。
			if keyModel.notComplete != 1 || keyModel.unknownReport != 0 || keyModel.successReport != 0 {
				t.Fatalf("keyModel = notComplete:%d unknown:%d success:%d, want 1/0/0",
					keyModel.notComplete, keyModel.unknownReport, keyModel.successReport)
			}
			// 账户锁：按 upstream_stream_abnormal_eof 归因记失败。
			if lock.failures != 1 || lock.lastReason != gatewayresponse.UpstreamStreamAbnormalEOFCode || lock.lastAccount != "acct_w22" {
				t.Fatalf("lock = failures:%d reason:%q account:%q", lock.failures, lock.lastReason, lock.lastAccount)
			}
			if lock.lastObs != observation {
				t.Fatal("账户锁观察载荷应随调用透传")
			}
		})
	}
}

// TestV1PostVerdictAbnormalEOFGuards：中止与下游关闭形态不记账户锁（对齐
// 传输失败侧守卫）。
func TestV1PostVerdictAbnormalEOFGuards(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	circuit := &postVerdictCircuitFake{}
	keyModel := &postVerdictKeyModelFake{}
	lock := &postVerdictLockFake{}
	loop.settlePostVerdictUpstreamAttempts(cancelled, gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "acct_w22"},
	}, gatewayresponse.UpstreamResponseHandlingResult{
		AlreadyFinalized: true,
		ErrorCode:        gatewayresponse.UpstreamStreamAbnormalEOFCode,
	}, circuit, keyModel, lock)
	if lock.failures != 0 {
		t.Fatalf("中止形态不得记账户锁失败: %d", lock.failures)
	}

	// 非 gateway 流量（引擎 accountLockTrafficEnabled 同源门）跳过账户锁记录。
	loop2 := newV1TestLoop(t, &recordingFailureSink{})
	loop2.current.UsageContext.TrafficSource = "account_diagnostic"
	lock2 := &postVerdictLockFake{}
	loop2.settlePostVerdictUpstreamAttempts(context.Background(), gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "acct_w22"},
	}, gatewayresponse.UpstreamResponseHandlingResult{
		AlreadyFinalized: true,
		ErrorCode:        gatewayresponse.UpstreamStreamAbnormalEOFCode,
	}, &postVerdictCircuitFake{}, &postVerdictKeyModelFake{}, lock2)
	if lock2.failures != 0 {
		t.Fatalf("非 gateway 流量不得记账户锁失败: %d", lock2.failures)
	}
}

// TestV1SettleHotQualityTerminalAbnormalEOF：异常终局热质量终态归上游响应面
// （upstream_response_failure / none / upstream_response）——AlreadyFinalized
// 渲染族的 unknown / request_lifecycle 臂不适用于该失败。
func TestV1SettleHotQualityTerminalAbnormalEOF(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	fake := &recordingChainHotQualityAttempt{}
	dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_w22"}}
	dispatched.HotQualityAttempt = fake
	loop.settleHotQualityTerminal(context.Background(), dispatched, gatewayresponse.UpstreamResponseHandlingResult{
		AlreadyFinalized: true,
		ErrorCode:        gatewayresponse.UpstreamStreamAbnormalEOFCode,
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
}

// TestPreCommitReplayWindowDisabledOf：窗口资格排除面判定矩阵（设计 §3.1）。
func TestPreCommitReplayWindowDisabledOf(t *testing.T) {
	request := func(headers map[string]string) *gatewaypreauth.GatewayRequest {
		raw := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		for key, value := range headers {
			raw.Header.Set(key, value)
		}
		return gatewaypreauth.NewGatewayRequest(raw)
	}
	nativeResponsesAccount := gatewaydispatch.AccountCandidate{
		ID:                     "acc_native",
		SupportedEndpointModes: []string{"chat_json", "chat_sse", "responses_json", "responses_sse"},
	}
	chatOnlyAccount := gatewaydispatch.AccountCandidate{
		ID:                     "acc_chatonly",
		SupportedEndpointModes: []string{"chat_json", "chat_sse"},
	}
	cases := []struct {
		name      string
		req       *gatewaypreauth.GatewayRequest
		lane      gatewayproto.RequestLane
		protocol  string
		account   gatewaydispatch.AccountCandidate
		wantBlank bool
	}{
		{
			name:      "文本主路径缺省启用",
			req:       request(nil),
			protocol:  "chat_completions_sse",
			account:   chatOnlyAccount,
			wantBlank: false,
		},
		{
			name:      "媒体域请求排除",
			req:       request(nil),
			lane:      gatewayproto.LaneImage,
			protocol:  "chat_completions_sse",
			account:   chatOnlyAccount,
			wantBlank: true,
		},
		{
			name:      "anthropic 桥接流排除",
			req:       request(nil),
			protocol:  "messages_sse",
			account:   chatOnlyAccount,
			wantBlank: true,
		},
		{
			name:      "gemini 桥接流排除",
			req:       request(nil),
			protocol:  "gemini_stream_generate_content_sse",
			account:   chatOnlyAccount,
			wantBlank: true,
		},
		{
			name:      "responses 桥回转（chat-only 账户）排除",
			req:       request(nil),
			protocol:  "responses_sse",
			account:   chatOnlyAccount,
			wantBlank: true,
		},
		{
			name:      "responses 原生直通（持有原生端点）不排除",
			req:       request(nil),
			protocol:  "responses_sse",
			account:   nativeResponsesAccount,
			wantBlank: false,
		},
		{
			name:      "chat_web_search 副作用子调用排除",
			req:       request(map[string]string{"X-Juhe-Ai-Purpose": "chat_web_search"}),
			protocol:  "responses_sse",
			account:   nativeResponsesAccount,
			wantBlank: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			context := &gatewaypreauth.DispatchContext{RequestLane: testCase.lane}
			context.ClientStrategy.DownstreamProtocol = testCase.protocol
			got := preCommitReplayWindowDisabledOf(testCase.req, context, testCase.account)
			if got != testCase.wantBlank {
				t.Fatalf("preCommitReplayWindowDisabledOf = %v, want %v", got, testCase.wantBlank)
			}
		})
	}
	// purpose 头大小写不敏感（写入方是内部身份通道，读取面保持宽容）。
	if !preCommitReplayWindowDisabledOf(request(map[string]string{"x-juhe-ai-purpose": "Chat_Web_Search"}),
		&gatewaypreauth.DispatchContext{}, nativeResponsesAccount) {
		t.Fatal("purpose 头应大小写不敏感排除窗口")
	}
}

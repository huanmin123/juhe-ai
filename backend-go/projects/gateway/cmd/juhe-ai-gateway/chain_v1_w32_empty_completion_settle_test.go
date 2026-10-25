package main

// 流式提交边界受限重放与异常终局处置设计 §3.2.3（v2.3，2026-10-26）：
// upstream_stream_empty_completion 的 post-verdict 失败归因三面（circuit
// ReportUnknown / 账户锁按本码失败记录 / 热质量 upstream_response_failure，
// keyModel 维持默认臂 ReportUpstreamNotComplete）与换号耗尽出口 fallback
// reason 映射。结算消费者句柄复用 postverdict 计数闭包
//（chain_v1_postverdict_settle_test.go 同形态）。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

// TestV1PostVerdictEmptyCompletionTwoFormsSettleAttribution：空完成两形态
// （提交后 AlreadyFinalized 断开 / 提交前 RetryUpstream 换号）共用同一归因
// 三面——circuit ReportUnknown（不进 ReportFramingComplete 治愈证据臂）+
// 账户锁 RecordFailureAsync('upstream_stream_empty_completion') + keyModel
// 默认臂 ReportUpstreamNotComplete。
func TestV1PostVerdictEmptyCompletionTwoFormsSettleAttribution(t *testing.T) {
	handlingForms := []struct {
		name     string
		handling gatewayresponse.UpstreamResponseHandlingResult
	}{
		{
			name: "committed_disconnect_form",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				AlreadyFinalized: true,
				ErrorCode:        gatewayresponse.UpstreamStreamEmptyCompletionCode,
				Message:          "上游流式响应全程零语义输出即宣告完成",
			},
		},
		{
			name: "pre_commit_retry_form",
			handling: gatewayresponse.UpstreamResponseHandlingResult{
				RetryUpstream:         true,
				RetryReason:           gatewayresponse.StreamServerRetryEmptyCompletion,
				ExcludeCurrentAccount: true,
				ErrorCode:             gatewayresponse.UpstreamStreamEmptyCompletionCode,
				Message:               "上游流式响应全程零语义输出即宣告完成",
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
			observation := &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "incident_w32"}
			dispatched := gatewaydispatch.UpstreamDispatchResult{
				Account:                gatewaydispatch.AccountCandidate{ID: "acct_w32"},
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
			// 账户锁：按 upstream_stream_empty_completion 归因记失败。
			if lock.failures != 1 || lock.lastReason != gatewayresponse.UpstreamStreamEmptyCompletionCode || lock.lastAccount != "acct_w32" {
				t.Fatalf("lock = failures:%d reason:%q account:%q", lock.failures, lock.lastReason, lock.lastAccount)
			}
			if lock.lastObs != observation {
				t.Fatal("账户锁观察载荷应随调用透传")
			}
		})
	}
}

// TestV1PostVerdictEmptyCompletionGuards：中止与下游关闭形态不记账户锁；
// abnormal_eof 同码并存时各归各因（账户锁 reason 不串码）。
func TestV1PostVerdictEmptyCompletionGuards(t *testing.T) {
	loop := newV1TestLoop(t, &recordingFailureSink{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	lock := &postVerdictLockFake{}
	loop.settlePostVerdictUpstreamAttempts(cancelled, gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "acct_w32"},
	}, gatewayresponse.UpstreamResponseHandlingResult{
		AlreadyFinalized: true,
		ErrorCode:        gatewayresponse.UpstreamStreamEmptyCompletionCode,
	}, &postVerdictCircuitFake{}, &postVerdictKeyModelFake{}, lock)
	if lock.failures != 0 {
		t.Fatalf("中止形态不得记账户锁失败: %d", lock.failures)
	}

	// 非 gateway 流量（引擎 accountLockTrafficEnabled 同源门）跳过账户锁记录。
	loop2 := newV1TestLoop(t, &recordingFailureSink{})
	loop2.current.UsageContext.TrafficSource = "account_diagnostic"
	lock2 := &postVerdictLockFake{}
	loop2.settlePostVerdictUpstreamAttempts(context.Background(), gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "acct_w32"},
	}, gatewayresponse.UpstreamResponseHandlingResult{
		AlreadyFinalized: true,
		ErrorCode:        gatewayresponse.UpstreamStreamEmptyCompletionCode,
	}, &postVerdictCircuitFake{}, &postVerdictKeyModelFake{}, lock2)
	if lock2.failures != 0 {
		t.Fatalf("非 gateway 流量不得记账户锁失败: %d", lock2.failures)
	}
}

// TestV1SettleHotQualityTerminalEmptyCompletion：空完成热质量终态归上游响应面
// （upstream_response_failure / none / upstream_response）——RetryUpstream 换号
// 形态与 AlreadyFinalized 断开形态一致，渲染族 unknown / request_lifecycle 臂
// 不适用于该失败族。
func TestV1SettleHotQualityTerminalEmptyCompletion(t *testing.T) {
	for _, form := range []gatewayresponse.UpstreamResponseHandlingResult{
		{
			RetryUpstream:         true,
			RetryReason:           gatewayresponse.StreamServerRetryEmptyCompletion,
			ExcludeCurrentAccount: true,
			ErrorCode:             gatewayresponse.UpstreamStreamEmptyCompletionCode,
		},
		{
			AlreadyFinalized: true,
			ErrorCode:        gatewayresponse.UpstreamStreamEmptyCompletionCode,
		},
	} {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		fake := &recordingChainHotQualityAttempt{}
		dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_w32"}}
		dispatched.HotQualityAttempt = fake
		loop.settleHotQualityTerminal(context.Background(), dispatched, form)
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
}

// TestStreamServerRetryFallbackReasonEmptyCompletion：空完成换号耗尽出口
// fallback reason 独立可辨，不与通用 pre_commit 耗尽混淆。
func TestStreamServerRetryFallbackReasonEmptyCompletion(t *testing.T) {
	if got := streamServerRetryFallbackReason(gatewayresponse.StreamServerRetryEmptyCompletion); got != "upstream_stream_empty_completion_server_retry_exhausted" {
		t.Fatalf("fallback reason = %q", got)
	}
	// 既有映射回归保持。
	if got := streamServerRetryFallbackReason(gatewayresponse.StreamServerRetryPreCommitStreamFailure); got != "stream_server_retry_exhausted" {
		t.Fatalf("pre_commit fallback reason 回归破坏: %q", got)
	}
}

// TestClassifyPostVerdictOutcomeEmptyCompletionIsolatedFromAbnormalEOF：两码
// 分类位互不串扰（abnormal_eof 不点亮 empty completion 位，反之亦然）。
func TestClassifyPostVerdictOutcomeEmptyCompletionIsolatedFromAbnormalEOF(t *testing.T) {
	empty := classifyPostVerdictOutcome(gatewaydispatch.UpstreamDispatchResult{}, gatewayresponse.UpstreamResponseHandlingResult{
		AlreadyFinalized: true,
		ErrorCode:        gatewayresponse.UpstreamStreamEmptyCompletionCode,
	})
	if !empty.upstreamStreamEmptyCompletion || empty.upstreamStreamAbnormalEOF {
		t.Fatalf("空完成分类 = %+v", empty)
	}
	abnormal := classifyPostVerdictOutcome(gatewaydispatch.UpstreamDispatchResult{}, gatewayresponse.UpstreamResponseHandlingResult{
		AlreadyFinalized: true,
		ErrorCode:        gatewayresponse.UpstreamStreamAbnormalEOFCode,
	})
	if !abnormal.upstreamStreamAbnormalEOF || abnormal.upstreamStreamEmptyCompletion {
		t.Fatalf("异常终局分类 = %+v", abnormal)
	}
}

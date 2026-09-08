package main

// D-123（BUG-0175 波4 W4-A）失败尝试审计完整性回归：gateway 流量的 failed
// response 分支完成审计尝试时必须携带上游响应事实（statusCode /
// responseHeaders / responseBody），对齐归档 failure-dispatch.ts:276-283。

import (
	"context"
	"net/http"
	"testing"
)

func TestChainFailureDispatcherGatewaySkipCompletesAttemptWithResponseFacts(t *testing.T) {
	const body = `{"error":{"message":"upstream exploded","code":"server_error"}}`
	response := failureDispatchUpstreamResponse(t, http.StatusInternalServerError, "application/json", body)
	sink := &failureDispatchAuditSink{}
	affinity := &failureDispatchAffinity{}
	dispatcher := newFailureDispatcherForTest(affinity)

	input := gatewayFailedResponseInput(response, sink, "gateway")
	if _, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input); err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if len(sink.completions) != 1 {
		t.Fatalf("completions = %+v", sink.completions)
	}
	completion := sink.completions[0]
	if completion.Success {
		t.Fatalf("失败尝试 success 必须为 false")
	}
	if completion.ErrorPhase != "upstream_response" {
		t.Fatalf("errorPhase = %q", completion.ErrorPhase)
	}
	if completion.StatusCode == nil || *completion.StatusCode != http.StatusInternalServerError {
		t.Fatalf("statusCode = %+v", completion.StatusCode)
	}
	if completion.ResponseHeaders == nil || completion.ResponseHeaders.Get("Content-Type") != "application/json" {
		t.Fatalf("responseHeaders = %+v", completion.ResponseHeaders)
	}
	if string(completion.ResponseBody) != body {
		t.Fatalf("responseBody = %q", string(completion.ResponseBody))
	}
	if completion.ErrorMessage == "" {
		t.Fatalf("errorMessage 缺失")
	}
}

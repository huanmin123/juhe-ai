package main

// BUG-0343 链面回填面：applyDispatchedEffectiveUsageFacts 把引擎带出的
// 「最终出站体生效档位/思考级别」写回链面 usage 上下文，以及
// handleUpstreamResponse 开头的调用点接线。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// TestApplyDispatchedEffectiveUsageFacts 钉住回填语义：无条件覆盖（空串代表
// 出站体确实没有该字段，不得回落请求值）、requested 保持不变、nil 上下文不
// panic。
func TestApplyDispatchedEffectiveUsageFacts(t *testing.T) {
	context := &gatewaypreauth.DispatchContext{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			RequestedServiceTier:     "flex",
			EffectiveServiceTier:     "flex",
			RequestedReasoningEffort: "medium",
			EffectiveReasoningEffort: "medium",
		},
	}
	applyDispatchedEffectiveUsageFacts(context, gatewaydispatch.UpstreamDispatchResult{
		EffectiveServiceTier:     "priority",
		EffectiveReasoningEffort: "max",
	})
	if context.UsageContext.EffectiveServiceTier != "priority" || context.UsageContext.EffectiveReasoningEffort != "max" {
		t.Fatalf("effective = %s/%s, want priority/max",
			context.UsageContext.EffectiveServiceTier, context.UsageContext.EffectiveReasoningEffort)
	}
	if context.UsageContext.RequestedServiceTier != "flex" || context.UsageContext.RequestedReasoningEffort != "medium" {
		t.Fatalf("requested = %s/%s, want flex/medium（回填不得改写请求值）",
			context.UsageContext.RequestedServiceTier, context.UsageContext.RequestedReasoningEffort)
	}

	// 出站体没有该字段时覆盖为空串，不得回落请求值。
	applyDispatchedEffectiveUsageFacts(context, gatewaydispatch.UpstreamDispatchResult{})
	if context.UsageContext.EffectiveServiceTier != "" || context.UsageContext.EffectiveReasoningEffort != "" {
		t.Fatalf("empty dispatched must overwrite, got %s/%s",
			context.UsageContext.EffectiveServiceTier, context.UsageContext.EffectiveReasoningEffort)
	}

	// nil 上下文不 panic。
	applyDispatchedEffectiveUsageFacts(nil, gatewaydispatch.UpstreamDispatchResult{
		EffectiveServiceTier:     "priority",
		EffectiveReasoningEffort: "max",
	})
}

// TestHandleUpstreamResponseBackfillsEffectiveUsageFacts 锁定 handleUpstreamResponse
// 开头的调用点接线：装配复用 R5 首字截止切号的 200 stream + abort 形态（走
// 早退分支即可，不要求产生用量记录），回填发生在任何早退之前，链面原始副本
// 的 effective 字段必须被覆盖为引擎带出值、requested 保持不变。
func TestHandleUpstreamResponseBackfillsEffectiveUsageFacts(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	// 完整响应管道会调用 ShouldCaptureSuccessPayloads：挂真实 gatewayusage
	// capture（零值安全，false），与 R5 切号测试同款装配。
	loop.auditCapture = preauthAuditCapture{inner: &gatewayusage.AuditCaptureContext{}}
	// 链面原始副本：preflight 冻结 requested=flex/medium，引擎值拷贝更新对
	// 链面不可见（BUG-0343 的现象面），因此先摆出 effective==requested。
	loop.current.UsageContext.RequestedServiceTier = "flex"
	loop.current.UsageContext.EffectiveServiceTier = "flex"
	loop.current.UsageContext.RequestedReasoningEffort = "medium"
	loop.current.UsageContext.EffectiveReasoningEffort = "medium"

	bodyReader, bodyWriter := io.Pipe()
	defer bodyWriter.Close()

	coordinator := &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}

	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "acc_slow", Name: "慢账户"},
		Response: gatewaydispatch.NewGatewayUpstreamResponseForTransform(200,
			http.Header{"Content-Type": []string{"application/json"}}, bodyReader),
		EffectiveServiceTier:     "priority",
		EffectiveReasoningEffort: "max",
		NormalRouteFirstByteDeadline: &gatewayrouting.NormalRouteAttemptFirstByteDeadline{
			ConfiguredDeadlineMs: 60,
			EffectiveDeadlineMs:  60,
			LimitingFactor:       gatewayrouting.FirstByteLimitingFactorConfigured,
		},
		FirstByteDeadlineCoordinator: coordinator,
		OnFirstByteDeadline: func(gatewaydispatch.FirstByteDeadlineDecisionInput) gatewaydispatch.FirstByteDeadlineAction {
			return gatewaydispatch.FirstByteDeadlineActionAbort
		},
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req := gatewaypreauth.NewGatewayRequest(request)
	res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

	handling := loop.c.handleUpstreamResponse(nil, req, res, loop.auditCapture, loop.current, dispatched, loop.startedAt,
		gatewayruntimecache.GatewaySettings{}, requestBudgets{}, nil)

	// 走到与 R5 相同的首字截止切号早退分支，确认装配有效。
	if !handling.FirstByteDeadlineCutover {
		t.Fatalf("必须产生切号 verdict: %+v", handling)
	}
	if loop.current.UsageContext.EffectiveServiceTier != "priority" || loop.current.UsageContext.EffectiveReasoningEffort != "max" {
		t.Fatalf("effective = %s/%s, want priority/max",
			loop.current.UsageContext.EffectiveServiceTier, loop.current.UsageContext.EffectiveReasoningEffort)
	}
	if loop.current.UsageContext.RequestedServiceTier != "flex" || loop.current.UsageContext.RequestedReasoningEffort != "medium" {
		t.Fatalf("requested = %s/%s, want flex/medium（回填不得改写请求值）",
			loop.current.UsageContext.RequestedServiceTier, loop.current.UsageContext.RequestedReasoningEffort)
	}
}

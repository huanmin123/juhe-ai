package gatewaydispatch

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// Circuit facade + degradation ordering helpers shared by the dispatch engine.
// 压缩请求形状判定已随调度内核通用化设计 5.2 三轨合一移出内核：唯一实现是
// gatewaycodex.CodexCompactionExpectedForRequest（preflight 单点消费），引擎
// 超时豁免改由链面传入 TimeoutsDisabled / TotalTimeLane 参数。

// ---------------------------------------------------------------------------
// gatewaycircuit attempt facade
// ---------------------------------------------------------------------------

// gatewaycircuitAttemptFacade is the direct gatewaycircuit.Attempt handle
// (the package already owns the Node account-circuit contract).
type gatewaycircuitAttemptFacade = gatewaycircuit.Attempt

func gatewayproxyhealthViewOf(account AccountCandidate) gatewayproxyhealth.DispatchPriorityAccountView {
	priority := float64(account.Priority)
	super := account.SuperPriorityEnabled
	fallback := account.FallbackEnabled
	return gatewayproxyhealth.DispatchPriorityAccountView{
		ID:                   account.ID,
		Priority:             &priority,
		SuperPriorityEnabled: &super,
		FallbackEnabled:      &fallback,
	}
}

func gatewayproxyhealthTierOf(view gatewayproxyhealth.DispatchPriorityAccountView, _ float64, priority *gatewayrouting.GatewayAccountModelPriority) string {
	return gatewayproxyhealth.GatewayAccountDispatchPriorityTier(view, gatewayproxyhealth.DispatchPriorityOrderOptions{
		ModelRankByAccountID: modelPriorityRankMap(priority),
	})
}

// isTransportQualityOutcome mirrors isTransportQualityOutcome.
func isTransportQualityOutcome(outcomeClass string) bool {
	switch outcomeClass {
	case HotQualityOutcomeTransportFailure, HotQualityOutcomeTimeout,
		HotQualityOutcomeReadInterruption, HotQualityOutcomeIncompleteResponse:
		return true
	}
	return false
}

// circuitTransportFailure mirrors accountCircuitTransportFailure.
func circuitTransportFailure(err error, fallbackMessage string) gatewaycircuitTransportFailure {
	reason := trimString(fallbackMessage)
	if reason == "" && err != nil {
		reason = trimString(err.Error())
	}
	if reason == "" {
		reason = "上游传输失败"
	}
	diagnostic := strings.ToLower(strings.TrimSpace(reason))
	if err != nil {
		diagnostic = strings.ToLower(strings.TrimSpace(errorNameOf(err) + " " + errorCodeOf(err) + " " + reason))
	}
	kind := "transport"
	if timeoutLikeText(diagnostic) {
		kind = "timeout"
	}
	return gatewaycircuitTransportFailure{kind: kind, reason: reason}
}

type gatewaycircuitTransportFailure struct {
	kind   string
	reason string
}

func errorNameOf(err error) string {
	if named, ok := err.(interface{ Name() string }); ok {
		return named.Name()
	}
	return ""
}

func errorCodeOf(err error) string {
	if coder, ok := err.(interface{ Code() string }); ok {
		return coder.Code()
	}
	return ""
}

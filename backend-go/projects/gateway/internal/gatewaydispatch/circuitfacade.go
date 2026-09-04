package gatewaydispatch

import (
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// Codex compaction contract + circuit facade + degradation ordering helpers
// shared by the dispatch engine.

var codexCompactionRequestSearchPattern = regexp.MustCompile(`"type"\s*:\s*"compaction_trigger"`)

// CodexCompactionExpectedForRequest mirrors codexCompactionExpectedForRequest.
func CodexCompactionExpectedForRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || req.MethodUpper() != "POST" {
		return false
	}
	normalizedPath := normalizedOpenAIRequestPath(req)
	if normalizedPath == "/responses/compact" {
		return true
	}
	return normalizedPath == "/responses" && requestBodyHasCompactionTrigger(req)
}

func (e *Engine) codexCompactionExpectedForRequest(req *gatewaypreauth.GatewayRequest) bool {
	return CodexCompactionExpectedForRequest(req)
}

func normalizedOpenAIRequestPath(req *gatewaypreauth.GatewayRequest) string {
	path := splitPath(req.PathAndQuery())
	return stripV1Prefix(path)
}

func requestBodyHasCompactionTrigger(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || req.Body == nil || len(req.Body.RawBody) == 0 {
		return false
	}
	state := req.BodyState()
	if state != nil && state.CodexCompactionTrigger {
		return true
	}
	if len(req.Body.RawBody) <= 64*1024 {
		return codexCompactionRequestSearchPattern.Match(req.Body.RawBody)
	}
	edge := 64 * 1024
	head := req.Body.RawBody[:edge]
	tail := req.Body.RawBody[len(req.Body.RawBody)-edge:]
	return codexCompactionRequestSearchPattern.Match(head) || codexCompactionRequestSearchPattern.Match(tail)
}

var _ = gatewaybody.IsJSONContentType

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



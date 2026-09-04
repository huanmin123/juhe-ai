package gatewaycodex

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of client-profiles/client-source-avoidance.service.ts +
// client-source-availability-probe.service.ts. Both Node files are
// rename-only re-exports of the codex turn implementation ("The persistence
// implementation originated as Codex turn retry. Its state key is now
// supplied by the common source resolver"); the Go port keeps the client
// source names as thin aliases so callers do not couple to the legacy
// implementation detail.

// ClientSourceFailureEvidence mirrors ClientSourceFailureEvidence.
type ClientSourceFailureEvidence = CodexTurnFailureEvidence

// ClientSourceAccountAvoidanceResult mirrors
// ClientSourceAccountAvoidanceResult.
type ClientSourceAccountAvoidanceResult = CodexTurnAccountAvoidanceResult

// ClientSourceFailureRecordResult mirrors ClientSourceFailureRecordResult.
type ClientSourceFailureRecordResult = CodexTurnFailureRecordResult

// OrderOpenAIAccountsByClientSourceAvoidance mirrors
// orderOpenAIAccountsByClientSourceAvoidance.
func (s *TurnRetryService) OrderOpenAIAccountsByClientSourceAvoidance(accounts []gatewayruntimecache.OpenAIAccountSecret, strategy OpenAIGatewayClientStrategyContext, modelPriority *gatewayrouting.GatewayAccountModelPriority) ClientSourceAccountAvoidanceResult {
	return s.OrderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy, modelPriority)
}

// OrderOpenAIAccountsByClientSourceAvoidanceAsync mirrors
// orderOpenAIAccountsByClientSourceAvoidanceAsync.
func (s *TurnRetryService) OrderOpenAIAccountsByClientSourceAvoidanceAsync(ctx context.Context, accounts []gatewayruntimecache.OpenAIAccountSecret, strategy OpenAIGatewayClientStrategyContext, modelPriority *gatewayrouting.GatewayAccountModelPriority) (ClientSourceAccountAvoidanceResult, error) {
	return s.OrderOpenAIAccountsByCodexTurnAvoidanceAsync(ctx, accounts, strategy, modelPriority)
}

// RememberGatewayClientSourceFailure mirrors
// rememberGatewayClientSourceFailure.
func (s *TurnRetryService) RememberGatewayClientSourceFailure(strategy OpenAIGatewayClientStrategyContext, accountID string, input CodexTurnFailureInput) *ClientSourceFailureRecordResult {
	return s.RememberCodexTurnStreamFailure(strategy, accountID, input)
}

// RememberGatewayClientSourceFailureAsync mirrors
// rememberGatewayClientSourceFailureAsync.
func (s *TurnRetryService) RememberGatewayClientSourceFailureAsync(ctx context.Context, strategy OpenAIGatewayClientStrategyContext, accountID string, input CodexTurnFailureInput) (*ClientSourceFailureRecordResult, error) {
	return s.RememberCodexTurnStreamFailureAsync(ctx, strategy, accountID, input)
}

// RunGatewayClientSourceAvoidanceAvailabilityProbe mirrors the
// runGatewayClientSourceAvoidanceAvailabilityProbe re-export.
func (s *TurnAvoidanceProbeService) RunGatewayClientSourceAvoidanceAvailabilityProbe(ctx context.Context, input CodexTurnAvoidanceProbeInput) (CodexTurnAvoidanceProbeResult, error) {
	return s.RunCodexTurnAvoidanceAvailabilityProbe(ctx, input)
}

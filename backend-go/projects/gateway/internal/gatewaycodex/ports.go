package gatewaycodex

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// G05 port adapters. The preflight consumes exactly these seams
// (gatewaypreauth/ports.go):
//
//   - ClientStrategy — client-profiles/strategy.ts (resolve + audit);
//   - CodexBridgePreflight — codex-responses preflight surface (compaction
//     contract + context state preflight + compact preflight).
//
// The compile-time assertions pin the contract match.

var (
	_ gatewaypreauth.ClientStrategy       = (*ClientStrategyPort)(nil)
	_ gatewaypreauth.CodexBridgePreflight = (*BridgePreflightPort)(nil)
)

// ClientStrategyPort adapts ClientStrategyDeps onto the frozen G05 port.
type ClientStrategyPort struct {
	Deps *ClientStrategyDeps
}

// NewClientStrategyPort builds the adapter.
func NewClientStrategyPort(deps *ClientStrategyDeps) *ClientStrategyPort {
	return &ClientStrategyPort{Deps: deps}
}

// Resolve mirrors resolveOpenAIGatewayClientStrategy through the port shape.
// The full context rides in Opaque for the downstream slices.
func (p *ClientStrategyPort) Resolve(req *gatewaypreauth.GatewayRequest, input gatewaypreauth.ClientStrategyInput) gatewaypreauth.ClientStrategyContext {
	strategy := p.Deps.ResolveOpenAIGatewayClientStrategy(req, ClientStrategyIdentity{
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         input.GroupID,
		Endpoint:        input.Endpoint,
		ProviderCode:    input.ProviderCode,
		ClientIP:        input.ClientIP,
	})
	context := gatewaypreauth.ClientStrategyContext{
		ClientProfile:              strategy.ClientProfile,
		DownstreamProtocol:         strategy.DownstreamProtocol,
		RequestClientCompatibility: strategy.RequestClientCompatibility,
		Opaque:                     strategy,
	}
	if strategy.ClientSource != nil {
		context.ClientSource = &gatewaypreauth.ClientSource{}
		if strategy.ClientSource.SessionIdentity != nil {
			context.ClientSource.SessionIdentity = &gatewaypreauth.SessionIdentity{
				SessionID:       strategy.ClientSource.SessionIdentity.SessionID,
				ConversationKey: strategy.ClientSource.SessionIdentity.ConversationKey,
			}
		}
	}
	return context
}

// AuditMetadata mirrors openAIGatewayClientStrategyAuditMetadata through the
// port shape: the port input carries the full G18 context in Opaque.
func (p *ClientStrategyPort) AuditMetadata(strategy gatewaypreauth.ClientStrategyContext) map[string]any {
	full, ok := strategy.Opaque.(OpenAIGatewayClientStrategyContext)
	if !ok {
		full = OpenAIGatewayClientStrategyContext{
			ClientProfile:              strategy.ClientProfile,
			DownstreamProtocol:         strategy.DownstreamProtocol,
			RequestClientCompatibility: strategy.RequestClientCompatibility,
		}
	}
	return OpenAIGatewayClientStrategyAuditMetadata(full)
}

// BridgePreflightPort adapts the codex bridge services onto the frozen G05
// port.
type BridgePreflightPort struct {
	Bridge   *ChatBridgeStateService
	Compact  *CompactPreflightService
	Registry *ContextRequestStateRegistry
}

// NewBridgePreflightPort builds the adapter.
func NewBridgePreflightPort(bridge *ChatBridgeStateService, compact *CompactPreflightService, registry *ContextRequestStateRegistry) *BridgePreflightPort {
	return &BridgePreflightPort{Bridge: bridge, Compact: compact, Registry: registry}
}

// CompactionExpectedForRequest mirrors codexCompactionExpectedForRequest.
func (p *BridgePreflightPort) CompactionExpectedForRequest(req *gatewaypreauth.GatewayRequest) bool {
	return CodexCompactionExpectedForRequest(req)
}

// ApplyContextStatePreflight mirrors applyCodexResponsesContextStatePreflight.
func (p *BridgePreflightPort) ApplyContextStatePreflight(ctx context.Context, input gatewaypreauth.CodexContextStateInput) (bool, error) {
	return p.Bridge.ApplyContextStatePreflight(ctx, p.Registry, ContextStatePreflightInput{
		Req:             input.Req,
		Res:             input.Res,
		AuditCapture:    input.AuditCapture,
		UsageContext:    input.UsageContext,
		StartedAt:       input.StartedAt,
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         input.GroupID,
		GroupAccess:     input.GroupAccess,
		Signal:          input.Signal,
	})
}

// ApplyChatBridgeCompactPreflight mirrors
// applyCodexResponsesChatBridgeCompactPreflight.
func (p *BridgePreflightPort) ApplyChatBridgeCompactPreflight(ctx context.Context, input gatewaypreauth.CodexCompactPreflightInput) (gatewaypreauth.CodexCompactPreflightResult, error) {
	result, err := p.Compact.ApplyChatBridgeCompactPreflight(ctx, CompactPreflightInput{
		Req:                        input.Req,
		Res:                        input.Res,
		AuditCapture:               input.AuditCapture,
		UsageContext:               input.UsageContext,
		StartedAt:                  input.StartedAt,
		SystemAccountID:            input.SystemAccountID,
		APIKeyID:                   input.APIKeyID,
		GroupID:                    input.GroupID,
		GroupAccess:                input.GroupAccess,
		RequestClientCompatibility: input.RequestClientCompatibility,
		DispatchAccounts:           input.DispatchAccounts,
		ActiveGatewaySettings:      input.ActiveGatewaySettings,
		ClientIPAccountAvoidance:   input.ClientIPAccountAvoidance,
		ModelPriority:              input.ModelPriority,
		RequestLane:                input.RequestLane,
		GroupSchedulingPolicy:      input.GroupSchedulingPolicy,
		RequestCoordination:        input.RequestCoordination,
		OnDispatchedAccount:        input.OnDispatchedAccount,
		Signal:                     input.Signal,
	})
	return gatewaypreauth.CodexCompactPreflightResult{
		Completed: result.Completed,
		Accounts:  result.Accounts,
	}, err
}

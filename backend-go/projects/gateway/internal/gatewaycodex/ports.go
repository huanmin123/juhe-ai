package gatewaycodex

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// G05 port adapter. The preflight consumes exactly this seam
// (gatewaypreauth/ports.go):
//
//   - ClientStrategy — client-profiles/strategy.ts (resolve + audit).
//
// The compile-time assertion pins the contract match. 2026-11-07 跨协议
// Responses↔Chat 桥退役后，CodexBridgePreflight 面只剩原生 compaction
// contract 单点判定（compactioncontract.go），bridge 状态 / 桥摘要 compact
// preflight 的端口实现随桥删除。

var (
	_ gatewaypreauth.ClientStrategy = (*ClientStrategyPort)(nil)
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

package gatewaydispatch

import (
	"context"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Port of runtime/key-model-capability.ts (D-133, BUG-0175): the request-side
// key-model capability resolution the dispatch attempt loop feeds into
// prepareGatewayKeyModelAttempt. The engine previously handed the admission
// service an account-id-only carrier, so every attempt hashed the zero
// capability and the main-probe triple alignment never ran.

// ResolveGatewayKeyModelAttemptCapability mirrors resolveGatewayKeyModelCapability
// (key-model-capability.ts:24-36): nil mirrors the Node undefined, which
// prepareGatewayKeyModelAttempt turns into the `disabled` preparation.
func ResolveGatewayKeyModelAttemptCapability(req *gatewaypreauth.GatewayRequest, account AccountCandidate) *gatewayaccounteffects.GatewayKeyModelCapability {
	model := strings.TrimSpace(requestModelOrEmpty(req))
	family := GatewayRequestEndpointFamily(req)
	revision := int64(0)
	if account.DispatchRevision != nil {
		revision = *account.DispatchRevision
	}
	keyFingerprint := ""
	if account.SelectedAPIKeyFingerprint != nil {
		keyFingerprint = strings.TrimSpace(*account.SelectedAPIKeyFingerprint)
	}
	if model == "" || family == "" || keyFingerprint == "" || revision < 1 {
		return nil
	}
	stream := gatewaypreauth.RequestStream(req)
	capability := gatewayKeyModelCapabilityForRoute(account, model, family, stream)
	if capability == nil {
		return nil
	}
	return &gatewayaccounteffects.GatewayKeyModelCapability{
		AccountID:   account.ID,
		Capability:  *capability,
		IsMainProbe: gatewayKeyModelRouteMatchesMainProbe(account, model, family, stream, *capability),
	}
}

// gatewayKeyModelRouteMatchesMainProbe mirrors routeMatchesMainProbe
// (key-model-capability.ts:38-51): the requested route equals the account's
// health-check (main probe) route on model + family + stream and the resolved
// capability reproduces itself.
func gatewayKeyModelRouteMatchesMainProbe(account AccountCandidate, requestedModel string, family string, stream bool, capability gatewayaccounteffects.CapabilityKey) bool {
	if requestedModel != strings.TrimSpace(account.HealthCheckModel) {
		return false
	}
	mainFamily, mainStream, ok := gatewayaccounteffects.SourceEndpointMode(account.HealthCheckEndpointMode)
	if !ok || mainFamily != family || mainStream != stream {
		return false
	}
	expected := gatewayKeyModelCapabilityForRoute(account, requestedModel, family, stream)
	return expected != nil &&
		expected.FinalUpstreamModel == capability.FinalUpstreamModel &&
		expected.UpstreamEndpointMode == capability.UpstreamEndpointMode
}

// gatewayKeyModelCapabilityForRoute mirrors capabilityForRoute
// (key-model-capability.ts:53-75): resolve the account model mapping for the
// client model + endpoint family and derive the upstream endpoint mode.
func gatewayKeyModelCapabilityForRoute(account AccountCandidate, clientModel string, clientEndpointFamily string, stream bool) *gatewayaccounteffects.CapabilityKey {
	revision := int64(0)
	if account.DispatchRevision != nil {
		revision = *account.DispatchRevision
	}
	keyFingerprint := ""
	if account.SelectedAPIKeyFingerprint != nil {
		keyFingerprint = strings.TrimSpace(*account.SelectedAPIKeyFingerprint)
	}
	if keyFingerprint == "" || revision < 1 {
		return nil
	}
	mapping := resolveAccountModelMapping(account, clientModel, clientEndpointFamily)
	upstreamFamily := clientEndpointFamily
	if mapping != nil && mapping.UpstreamEndpointFamily != "" {
		upstreamFamily = mapping.UpstreamEndpointFamily
	}
	upstreamEndpointMode := gatewayaccounteffects.EndpointModeForFamily(upstreamFamily, stream)
	if upstreamEndpointMode == "" {
		return nil
	}
	finalUpstreamModel := clientModel
	if mapping != nil && mapping.UpstreamModel != "" {
		finalUpstreamModel = mapping.UpstreamModel
	}
	credentialSourceAccountID := account.ID
	if account.CredentialSourceAccountID != nil && strings.TrimSpace(*account.CredentialSourceAccountID) != "" {
		credentialSourceAccountID = strings.TrimSpace(*account.CredentialSourceAccountID)
	}
	return &gatewayaccounteffects.CapabilityKey{
		CredentialSourceAccountID: credentialSourceAccountID,
		KeyFingerprint:            keyFingerprint,
		ClientModel:               clientModel,
		ClientEndpointFamily:      clientEndpointFamily,
		FinalUpstreamModel:        finalUpstreamModel,
		UpstreamEndpointMode:      upstreamEndpointMode,
		DispatchRevision:          revision,
	}
}

// mergePermitLostSignal mirrors GatewayKeyModelAttempt.transportSignal
// (key-model-attempt.ts `AbortSignal.any([parent, renewalAbort])`): the
// derived signal cancels when the parent request signal cancels or the
// foreground permit is lost. The watcher exits with the parent context, so
// no goroutine outlives the request.
func MergePermitLostSignal(parent context.Context, lost <-chan struct{}) context.Context {
	if lost == nil {
		return parent
	}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-lost:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

// hotQualityProtocolProfileOf mirrors the protocol profile fallback of
// gatewayAccountProtocolModelScope (the profile id wins; an empty value
// composes protocolCode:protocolVersion).
func hotQualityProtocolProfileOf(account AccountCandidate) string {
	if strings.TrimSpace(account.ProviderProtocolProfileID) != "" {
		return account.ProviderProtocolProfileID
	}
	return account.ProtocolCode + ":" + account.ProtocolVersion
}

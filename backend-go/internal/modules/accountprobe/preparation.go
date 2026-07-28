package accountprobe

import (
	"errors"
	"fmt"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

var (
	ErrModelUnavailable       = errors.New("account probe model is unavailable")
	ErrProtocolBridgeRequired = errors.New("account probe protocol bridge is required")
)

type PreparedRequest struct {
	Request    RequestSpec
	Resolution gatewaycandidatewindow.EffectiveModelResolution
}

// PrepareRequest resolves the candidate's exact model before serializing the
// diagnostic request. Cross-protocol mappings fail closed until the matching
// gateway bridge has been migrated; changing only model/path would corrupt the
// request contract.
func PrepareRequest(candidate gatewaycandidatewindow.Candidate, input RequestInput) (PreparedRequest, error) {
	sourceFamily, ok := EndpointFamilyForMode(input.Mode)
	if !ok {
		return PreparedRequest{}, fmt.Errorf("%w: unsupported endpoint mode %q", ErrInvalidProtocolInput, input.Mode)
	}
	resolution, ok := gatewaycandidatewindow.ResolveEffectiveModel(candidate, input.Model, string(sourceFamily))
	if !ok {
		return PreparedRequest{}, fmt.Errorf("%w: %s for %s", ErrModelUnavailable, input.Model, sourceFamily)
	}
	if resolution.MappingApplied && !probeMappingUsesSourceShape(resolution.SourceEndpointFamily, resolution.UpstreamEndpointFamily) {
		return PreparedRequest{}, fmt.Errorf("%w: %s to %s", ErrProtocolBridgeRequired, resolution.SourceEndpointFamily, resolution.UpstreamEndpointFamily)
	}
	input.Model = resolution.UpstreamModel
	request, err := BuildRequest(input)
	if err != nil {
		return PreparedRequest{}, err
	}
	return PreparedRequest{Request: request, Resolution: resolution}, nil
}

func EndpointFamilyForMode(mode EndpointMode) (gatewayprotocol.EndpointFamily, bool) {
	switch mode {
	case ModeChatJSON, ModeChatSSE:
		return gatewayprotocol.EndpointChatCompletions, true
	case ModeResponsesJSON, ModeResponsesSSE:
		return gatewayprotocol.EndpointResponses, true
	case ModeMessagesJSON, ModeMessagesSSE:
		return gatewayprotocol.EndpointMessages, true
	case ModeGenerateContentJSON:
		return gatewayprotocol.EndpointGenerateContent, true
	case ModeGenerateContentSSE:
		return gatewayprotocol.EndpointStreamGenerateContent, true
	case ModeInteractionsJSON, ModeInteractionsSSE:
		return gatewayprotocol.EndpointInteractions, true
	default:
		return gatewayprotocol.EndpointUnknown, false
	}
}

func probeMappingUsesSourceShape(source, upstream gatewayprotocol.EndpointFamily) bool {
	return source == upstream || source == gatewayprotocol.EndpointStreamGenerateContent && upstream == gatewayprotocol.EndpointGenerateContent
}

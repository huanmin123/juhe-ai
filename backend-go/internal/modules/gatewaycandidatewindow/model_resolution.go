package gatewaycandidatewindow

import (
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewaymodelcapability"
	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

type EffectiveModelResolution = gatewaymodelcapability.ModelResolution

// ResolveEffectiveModel applies provider scoping, authorization-resource
// identity and the gateway's canonical exact model-mapping rules. A caller
// must still run a protocol bridge when the returned endpoint family changes.
func ResolveEffectiveModel(candidate Candidate, requestedModel, sourceEndpointFamily string) (EffectiveModelResolution, bool) {
	providerCode, profileID, protocolCode, protocolVersion := effectiveProtocolIdentity(candidate)
	mappings := make([]gatewaymodelcapability.ModelMapping, 0, len(candidate.ModelMappings))
	for _, mapping := range candidate.ModelMappings {
		mappingProvider := strings.TrimSpace(mapping.ProviderCode)
		if mappingProvider != "" && mappingProvider != providerCode {
			continue
		}
		mappings = append(mappings, gatewaymodelcapability.ModelMapping{
			SourceModel:            strings.TrimSpace(mapping.SourceModel),
			SourceEndpointFamily:   gatewayprotocol.EndpointFamily(strings.TrimSpace(mapping.SourceEndpointFamily)),
			UpstreamModel:          strings.TrimSpace(mapping.UpstreamModel),
			UpstreamEndpointFamily: gatewayprotocol.EndpointFamily(strings.TrimSpace(mapping.UpstreamEndpointFamily)),
			Enabled:                mapping.Enabled,
		})
	}
	return gatewaymodelcapability.ResolveEffectiveModel(gatewaymodelcapability.Candidate{
		ProviderCode: providerCode, ProviderProtocolProfileID: profileID,
		ProtocolCode: protocolCode, ProtocolVersion: protocolVersion,
		SupportedModels: append([]string(nil), candidate.SupportedModels...), ModelMappings: mappings,
	}, requestedModel, gatewayprotocol.EndpointFamily(strings.TrimSpace(sourceEndpointFamily)))
}

func effectiveProtocolIdentity(candidate Candidate) (providerCode, profileID, protocolCode, protocolVersion string) {
	projection := candidate.Projection
	providerCode = strings.TrimSpace(projection.ProviderCode)
	profileID = strings.TrimSpace(projection.ProviderProtocolProfileID)
	protocolCode = strings.TrimSpace(projection.ProtocolCode)
	protocolVersion = strings.TrimSpace(projection.ProtocolVersion)
	if strings.TrimSpace(projection.ResourceAccountID) != "" {
		providerCode = strings.TrimSpace(projection.ResourceProviderCode)
		profileID = strings.TrimSpace(projection.ResourceProviderProtocolProfileID)
		protocolCode = strings.TrimSpace(projection.ResourceProtocolCode)
		protocolVersion = strings.TrimSpace(projection.ResourceProtocolVersion)
	}
	return providerCode, profileID, protocolCode, protocolVersion
}

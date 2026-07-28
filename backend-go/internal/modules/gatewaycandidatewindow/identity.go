package gatewaycandidatewindow

import "strings"

type AccountIdentity struct {
	AccountID                 string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	ConfigRevision            int
	DispatchRevision          int64
}

func EffectiveAccountIdentity(candidate Candidate) AccountIdentity {
	projection := candidate.Projection
	identity := AccountIdentity{
		AccountID: strings.TrimSpace(projection.AccountID), ProviderCode: strings.TrimSpace(projection.ProviderCode),
		ProviderProtocolProfileID: strings.TrimSpace(projection.ProviderProtocolProfileID),
		ProtocolCode:              strings.TrimSpace(projection.ProtocolCode), ProtocolVersion: strings.TrimSpace(projection.ProtocolVersion),
		Type: strings.TrimSpace(projection.Type), ConfigRevision: projection.ConfigRevision, DispatchRevision: projection.DispatchRevision,
	}
	if strings.TrimSpace(projection.ResourceAccountID) != "" {
		identity.AccountID = strings.TrimSpace(projection.ResourceAccountID)
		identity.ProviderCode = strings.TrimSpace(projection.ResourceProviderCode)
		identity.ProviderProtocolProfileID = strings.TrimSpace(projection.ResourceProviderProtocolProfileID)
		identity.ProtocolCode = strings.TrimSpace(projection.ResourceProtocolCode)
		identity.ProtocolVersion = strings.TrimSpace(projection.ResourceProtocolVersion)
		identity.Type = strings.TrimSpace(projection.ResourceType)
		identity.ConfigRevision = projection.ResourceConfigRevision
		identity.DispatchRevision = projection.ResourceDispatchRevision
	}
	return identity
}

func EffectiveClientCompatibility(candidate Candidate) string {
	if strings.TrimSpace(candidate.Projection.ResourceAccountID) != "" {
		return strings.TrimSpace(candidate.Projection.ResourceClientCompatibility)
	}
	return strings.TrimSpace(candidate.Projection.ClientCompatibility)
}

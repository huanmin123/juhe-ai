package port

import (
	"context"
	"time"
)

type GatewayCandidateHydrationInput struct {
	AccountIDs []string
	ProxyIDs   []string
}

type GatewayCandidateModelMapping struct {
	ProviderCode           string `json:"providerCode"`
	SourceModel            string `json:"sourceModel"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily"`
	UpstreamModel          string `json:"upstreamModel"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily"`
	Enabled                bool   `json:"enabled"`
}

type GatewayCandidateAccountFacts struct {
	SupportedModels []string
	ModelMappings   []GatewayCandidateModelMapping
}

type GatewayCandidateQualityFacts struct {
	QualityScore            *int64
	QualityState            string
	QualityEWMAFirstTokenMS *float64
}

type GatewayCandidateProxyFacts struct {
	ID                string
	Type              string
	Host              string
	Port              int
	Username          string
	PasswordEncrypted string
	Enabled           bool
}

type GatewayCandidateHydrationFacts struct {
	Accounts map[string]GatewayCandidateAccountFacts
	Proxies  map[string]GatewayCandidateProxyFacts
}

type GatewayCandidateHydrationReader interface {
	LoadGatewayCandidateHydrationFacts(context.Context, GatewayCandidateHydrationInput) (GatewayCandidateHydrationFacts, error)
}

type GatewayCandidateQualityReader interface {
	LoadGatewayCandidateQualityFacts(context.Context, []string, time.Time) (map[string]GatewayCandidateQualityFacts, error)
}

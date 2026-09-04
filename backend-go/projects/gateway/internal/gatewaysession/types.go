package gatewaysession

// Identity status / resolution / confidence / invalid-reason enums mirror
// session-identity/types.ts string unions.
type (
	IdentityStatus         string
	IdentityResolution     string
	IdentityConfidence     string
	IdentityInvalidReason  string
	IdentitySemanticKind   string
	IdentitySourceLocation string
)

const (
	IdentityStatusResolved IdentityStatus = "resolved"
	IdentityStatusMissing  IdentityStatus = "missing"
	IdentityStatusConflict IdentityStatus = "conflict"
	IdentityStatusInvalid  IdentityStatus = "invalid"

	IdentityResolutionOfficial IdentityResolution = "official"
	IdentityResolutionMissing  IdentityResolution = "missing"
	IdentityResolutionConflict IdentityResolution = "conflict"
	IdentityResolutionInvalid  IdentityResolution = "invalid"

	IdentityConfidenceAuthoritative IdentityConfidence = "authoritative"

	IdentityInvalidReasonEmpty            IdentityInvalidReason = "empty"
	IdentityInvalidReasonControlCharacter IdentityInvalidReason = "control_character"
	IdentityInvalidReasonTooLong          IdentityInvalidReason = "too_long"
	IdentityInvalidReasonInvalidShape     IdentityInvalidReason = "invalid_shape"

	IdentitySemanticKindSession IdentitySemanticKind = "session"

	IdentitySourceLocationHeader IdentitySourceLocation = "header"
)

// IdentityPhysicalSource mirrors GatewaySessionIdentityPhysicalSource.
type IdentityPhysicalSource struct {
	Location IdentitySourceLocation
	Path     string
}

// IdentityCandidate mirrors GatewaySessionIdentityCandidate (the public
// candidate projection).
type IdentityCandidate struct {
	ResolverID        string
	SemanticKind      IdentitySemanticKind
	SemanticNamespace string
	Source            IdentityPhysicalSource
	Confidence        IdentityConfidence
	Priority          int
	Valid             bool
	EvidenceKey       string
	InvalidReason     IdentityInvalidReason
}

// IdentityConflict mirrors GatewaySessionIdentityConflict.
type IdentityConflict struct {
	Kind         IdentitySemanticKind
	Priority     int
	Sources      []IdentityPhysicalSource
	EvidenceKeys []string
}

// GatewaySessionIdentity mirrors GatewaySessionIdentity.
type GatewaySessionIdentity struct {
	Status            IdentityStatus
	Resolution        IdentityResolution
	SessionID         string
	ConversationKey   string
	SemanticNamespace string
	Source            *IdentityPhysicalSource
	Sources           []IdentityPhysicalSource
	Confidence        IdentityConfidence
	Candidates        []IdentityCandidate
	Conflicts         []IdentityConflict
}

// IdentityScope mirrors GatewaySessionIdentityScope; HMACSecret empty falls
// back to the service secret (runtimeConfig.secret).
type IdentityScope struct {
	ClientProfile   string
	SystemAccountID string
	APIKeyID        string
	HMACSecret      string
}

// ResolvedGatewaySessionIdentityScope mirrors
// ResolvedGatewaySessionIdentityScope: the scope with the effective secret.
type ResolvedGatewaySessionIdentityScope struct {
	ClientProfile   string
	SystemAccountID string
	APIKeyID        string
	HMACSecret      string
}

// GatewaySessionAffinityKeyScope mirrors GatewaySessionAffinityKeyScope.
type GatewaySessionAffinityKeyScope struct {
	HMACSecret                string
	SystemAccountID           string
	APIKeyID                  string
	RouteStrategyID           string
	GroupID                   string
	ProviderProtocolProfileID string
}

// RawCandidate mirrors GatewaySessionIdentityRawCandidate. Node keeps
// rawValue: unknown with an invalidShape flag; the Go projection types the raw
// value as string and keeps the flag for future non-string evidence.
type RawCandidate struct {
	ResolverID        string
	SemanticKind      IdentitySemanticKind
	SemanticNamespace string
	Source            IdentityPhysicalSource
	Confidence        IdentityConfidence
	Priority          int
	RawValue          string
	InvalidShape      bool
}

// ValidatedGatewaySessionIdentityCandidate mirrors
// ValidatedGatewaySessionIdentityCandidate.
type ValidatedGatewaySessionIdentityCandidate struct {
	RawCandidate
	EvidenceKey string
}

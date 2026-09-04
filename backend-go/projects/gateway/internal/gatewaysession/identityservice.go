package gatewaysession

// IdentityService mirrors the session-identity/service.ts exports. The Node
// module reads runtimeConfig.secret as the fallback HMAC secret; here the
// secret is injected per service. Node's versionedHmac throws whenever the
// effective secret is empty — that guard moves to construction so the
// per-request paths stay infallible exactly like the Node runtime (where
// runtimeConfig.secret always carries the non-empty default).
type IdentityService struct {
	secret string
}

// NewIdentityService builds the service. secret mirrors runtimeConfig.secret
// (the Node default is 'juhe-ai-dev-secret-change-me'; callers inject the
// platform-resolved value).
func NewIdentityService(secret string) (*IdentityService, error) {
	if err := validateIdentitySecret(secret); err != nil {
		return nil, err
	}
	return &IdentityService{secret: secret}, nil
}

func validateIdentitySecret(secret string) error {
	if jsTrimString(secret) == "" {
		return ErrEmptyHMACSecret
	}
	return nil
}

// Resolve mirrors resolveGatewaySessionIdentity.
//
// Difference vs Node: session-identity/service.ts memoizes the result in a
// WeakMap keyed by the request object (getGatewaySessionIdentity). Go has no
// weak maps; callers keep the returned value for the request lifetime, so the
// memo is not carried over.
func (s *IdentityService) Resolve(request IdentityRequest, scope IdentityScope, resolvers []Resolver) (GatewaySessionIdentity, error) {
	resolvedScope := s.resolveGatewaySessionIdentityScope(scope)
	rawCandidates := CollectGatewaySessionIdentityCandidates(ResolverContext{
		Request:        request,
		ClientProfile:  scope.ClientProfile,
		NormalizedPath: NormalizedGatewaySessionRequestPath(request),
	}, resolvers)

	var validCandidates []*ValidatedGatewaySessionIdentityCandidate
	candidates := make([]IdentityCandidate, 0, len(rawCandidates))
	hasInvalidCandidate := false

	for _, rawCandidate := range rawCandidates {
		validation, invalidReason, err := ValidateGatewaySessionIdentityCandidate(rawCandidate, resolvedScope)
		if err != nil {
			return GatewaySessionIdentity{}, err
		}
		if validation != nil {
			validCandidates = append(validCandidates, validation)
			candidates = append(candidates, toPublicCandidate(*validation))
			continue
		}
		hasInvalidCandidate = true
		candidates = append(candidates, IdentityCandidate{
			ResolverID:        rawCandidate.ResolverID,
			SemanticKind:      rawCandidate.SemanticKind,
			SemanticNamespace: rawCandidate.SemanticNamespace,
			Source:            rawCandidate.Source,
			Confidence:        rawCandidate.Confidence,
			Priority:          rawCandidate.Priority,
			Valid:             false,
			InvalidReason:     invalidReason,
		})
	}

	if hasInvalidCandidate {
		return GatewaySessionIdentity{
			Status:     IdentityStatusInvalid,
			Resolution: IdentityResolutionInvalid,
			Sources:    []IdentityPhysicalSource{},
			Candidates: candidates,
			Conflicts:  []IdentityConflict{},
		}, nil
	}
	if len(validCandidates) == 0 {
		return GatewaySessionIdentity{
			Status:     IdentityStatusMissing,
			Resolution: IdentityResolutionMissing,
			Sources:    []IdentityPhysicalSource{},
			Candidates: candidates,
			Conflicts:  []IdentityConflict{},
		}, nil
	}

	highestPriority := 0
	for _, candidate := range validCandidates {
		if candidate.Priority > highestPriority {
			highestPriority = candidate.Priority
		}
	}
	var highest []*ValidatedGatewaySessionIdentityCandidate
	for _, candidate := range validCandidates {
		if candidate.Priority == highestPriority {
			highest = append(highest, candidate)
		}
	}

	// Group by JSON.stringify([semanticNamespace, rawValue]).
	identityValues := make(map[string][]*ValidatedGatewaySessionIdentityCandidate)
	for _, candidate := range highest {
		key := jsJSONStringArray([]string{candidate.SemanticNamespace, candidate.RawValue})
		identityValues[key] = append(identityValues[key], candidate)
	}
	if len(identityValues) != 1 {
		return GatewaySessionIdentity{
			Status:     IdentityStatusConflict,
			Resolution: IdentityResolutionConflict,
			Sources:    []IdentityPhysicalSource{},
			Candidates: candidates,
			Conflicts: []IdentityConflict{{
				Kind:         IdentitySemanticKindSession,
				Priority:     highestPriority,
				Sources:      uniqueSources(sourcesOf(highest)),
				EvidenceKeys: uniqueStrings(evidenceKeysOf(highest)),
			}},
		}, nil
	}

	selected := highest[0]
	var sameIdentity []*ValidatedGatewaySessionIdentityCandidate
	for _, candidate := range validCandidates {
		if candidate.SemanticNamespace == selected.SemanticNamespace && candidate.RawValue == selected.RawValue {
			sameIdentity = append(sameIdentity, candidate)
		}
	}
	conversationKey, err := CreateGatewayConversationKey(resolvedScope, selected.SemanticNamespace, selected.RawValue)
	if err != nil {
		return GatewaySessionIdentity{}, err
	}
	return GatewaySessionIdentity{
		Status:            IdentityStatusResolved,
		Resolution:        IdentityResolutionOfficial,
		SessionID:         selected.RawValue,
		ConversationKey:   conversationKey,
		SemanticNamespace: selected.SemanticNamespace,
		Source:            &selected.Source,
		Sources:           uniqueSources(sourcesOf(sameIdentity)),
		Confidence:        selected.Confidence,
		Candidates:        candidates,
		Conflicts:         []IdentityConflict{},
	}, nil
}

// DeriveGatewaySessionAffinityKey mirrors deriveGatewaySessionAffinityKey:
// ("", nil) when the identity has no conversation key; the secret falls back
// to the service secret when the scope does not carry one.
func (s *IdentityService) DeriveGatewaySessionAffinityKey(conversationKey string, scope GatewaySessionAffinityKeyScope) (string, error) {
	if conversationKey == "" {
		return "", nil
	}
	if scope.HMACSecret == "" {
		scope.HMACSecret = s.secret
	}
	return DeriveGatewaySessionAffinityKeyFromConversationKey(conversationKey, scope)
}

func (s *IdentityService) resolveGatewaySessionIdentityScope(scope IdentityScope) ResolvedGatewaySessionIdentityScope {
	secret := scope.HMACSecret
	if secret == "" {
		secret = s.secret
	}
	return ResolvedGatewaySessionIdentityScope{
		ClientProfile:   scope.ClientProfile,
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		HMACSecret:      secret,
	}
}

func sourcesOf(candidates []*ValidatedGatewaySessionIdentityCandidate) []IdentityPhysicalSource {
	sources := make([]IdentityPhysicalSource, 0, len(candidates))
	for _, candidate := range candidates {
		sources = append(sources, candidate.Source)
	}
	return sources
}

func evidenceKeysOf(candidates []*ValidatedGatewaySessionIdentityCandidate) []string {
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, candidate.EvidenceKey)
	}
	return keys
}

func uniqueSources(sources []IdentityPhysicalSource) []IdentityPhysicalSource {
	seen := make(map[string]struct{}, len(sources))
	unique := make([]IdentityPhysicalSource, 0, len(sources))
	for _, source := range sources {
		key := string(source.Location) + ":" + source.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, source)
	}
	return unique
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func toPublicCandidate(candidate ValidatedGatewaySessionIdentityCandidate) IdentityCandidate {
	return IdentityCandidate{
		ResolverID:        candidate.ResolverID,
		SemanticKind:      candidate.SemanticKind,
		SemanticNamespace: candidate.SemanticNamespace,
		Source:            candidate.Source,
		Confidence:        candidate.Confidence,
		Priority:          candidate.Priority,
		Valid:             true,
		EvidenceKey:       candidate.EvidenceKey,
	}
}

package gatewaycandidatewindow

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayaccountcandidates"
	"juhe-ai/backend-go/internal/store/port"
)

const FinalLimit = 256

type Projector interface {
	Project(context.Context, gatewayaccountcandidates.ProjectInput) (gatewayaccountcandidates.Projection, bool, error)
}

// Hydrator resolves one bounded batch without per-account queries. It owns
// credential validation/decryption and bulk model, proxy and API-key state reads.
type Hydrator interface {
	Hydrate(context.Context, HydrateInput) ([]HydrationResult, error)
}

type CandidatePreRanker interface {
	PreRank(context.Context, []port.GatewayAccountCandidate) (map[string]CandidateRankFacts, error)
}

type CandidateRankFacts struct {
	QualityScore            *int64
	QualityState            string
	QualityEWMAFirstTokenMS *float64
}

type HydrateInput struct {
	Candidates     []port.GatewayAccountCandidate
	RequestedModel string
	EndpointFamily string
	PreRanks       map[string]CandidateRankFacts
}

type ModelMapping struct {
	ProviderCode           string
	SourceModel            string
	SourceEndpointFamily   string
	UpstreamModel          string
	UpstreamEndpointFamily string
	Enabled                bool
}

type APIKeyRuntime struct {
	KeyFingerprint string
	KeyIndex       int
	Status         string
	CooldownUntil  string
	NextProbeAt    string
}

type ProxyRuntime struct {
	ID                string
	Type              string
	Host              string
	Port              int
	Username          string
	Credentials       CredentialSet `json:"-"`
	Enabled           bool
	Available         bool
	UnavailableReason string
}

// Candidate contains dispatch facts, but deliberately does not expose plaintext
// credentials. A later request assembler receives those through the hydrator's
// private runtime implementation rather than diagnostics or JSON DTOs.
type Candidate struct {
	Projection              port.GatewayAccountCandidate
	Credentials             CredentialSet `json:"-"`
	DefaultBaseURL          string
	SupportedEndpointModes  []string
	EndpointModesComplete   bool
	SupportedModels         []string
	ModelMappings           []ModelMapping
	APIKeyRuntime           []APIKeyRuntime
	Proxy                   *ProxyRuntime
	QualityScore            *int64
	QualityState            string
	QualityEWMAFirstTokenMS *float64
}

type HydrationResult struct {
	AccountID  string
	Candidate  Candidate
	DropReason string
}

type LoadInput struct {
	GroupID            string
	SystemAccountID    string
	RequestedModel     string
	EndpointFamily     string
	IncludeUnavailable bool
}

type Diagnostics struct {
	ScanLimit             int
	FinalLimit            int
	CandidateRowCount     int
	ScannedRowCount       int
	EligibleRowCount      int
	HydrationBatchCount   int
	HydratedAccountCount  int
	HydrationDroppedCount int
	FinalAccountCount     int
	ScanLimitReached      bool
}

type Window struct {
	Access      port.GatewayGroupAccess
	Candidates  []Candidate
	Diagnostics Diagnostics
}

// PolicyWindow returns a detached, credential-free candidate view for
// request-local selection policies. A policy may inspect and reorder its own
// copy but cannot mutate the hydrated window that later reaches the claim
// boundary, nor observe account or proxy credentials.
func PolicyWindow(input Window) Window {
	result := input
	result.Access.GroupAuthorizationExpiresAt = clonePolicyTime(input.Access.GroupAuthorizationExpiresAt)
	result.Candidates = make([]Candidate, 0, len(input.Candidates))
	for _, candidate := range input.Candidates {
		copy := candidate
		copy.Projection = policyProjection(candidate.Projection)
		copy.Credentials = CredentialSet{}
		copy.SupportedEndpointModes = append([]string(nil), candidate.SupportedEndpointModes...)
		copy.SupportedModels = append([]string(nil), candidate.SupportedModels...)
		copy.ModelMappings = append([]ModelMapping(nil), candidate.ModelMappings...)
		copy.APIKeyRuntime = append([]APIKeyRuntime(nil), candidate.APIKeyRuntime...)
		if candidate.Proxy != nil {
			proxy := *candidate.Proxy
			proxy.Credentials = CredentialSet{}
			copy.Proxy = &proxy
		}
		copy.QualityScore = clonePolicyInt64(candidate.QualityScore)
		copy.QualityEWMAFirstTokenMS = clonePolicyFloat64(candidate.QualityEWMAFirstTokenMS)
		result.Candidates = append(result.Candidates, copy)
	}
	return result
}

func policyProjection(input port.GatewayAccountCandidate) port.GatewayAccountCandidate {
	result := input
	// A request policy selects account IDs only; encrypted material is neither
	// capability nor scheduling input and must never cross this boundary.
	result.CredentialsEncrypted = ""
	result.ResourceCredentialsEncrypted = ""
	result.CooldownUntil = clonePolicyTime(input.CooldownUntil)
	result.AccountExpiresAt = clonePolicyTime(input.AccountExpiresAt)
	result.AuthorizationExpiresAt = clonePolicyTime(input.AuthorizationExpiresAt)
	result.ResourceCooldownUntil = clonePolicyTime(input.ResourceCooldownUntil)
	result.ResourceAccountExpiresAt = clonePolicyTime(input.ResourceAccountExpiresAt)
	return result
}

func clonePolicyTime(input *time.Time) *time.Time {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func clonePolicyInt64(input *int64) *int64 {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func clonePolicyFloat64(input *float64) *float64 {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

// PreBodyCapacityInput identifies the already-authenticated route group for
// the Node-compatible speed-first admission snapshot. It intentionally has no
// model or endpoint fields: this snapshot is collected before body parsing.
type PreBodyCapacityInput struct {
	GroupID         string
	SystemAccountID string
}

// PreBodyCapacityCandidate exposes only the identity and limit used for a
// later body-admission capacity calculation. It never contains credentials,
// proxy credentials, model facts, or a dispatch claim.
type PreBodyCapacityCandidate struct {
	AccountID                 string
	CredentialSourceAccountID string
	ConcurrencyLimit          int
}

// PreBodyCapacitySnapshot is the bounded, body-independent counterpart of
// Node's runtime.accounts for speed-first body admission. Its candidates have
// passed the same credential/account hydration boundary as a regular window,
// but no request model is evaluated and no candidate is dispatched.
type PreBodyCapacitySnapshot struct {
	Access      port.GatewayGroupAccess
	Candidates  []PreBodyCapacityCandidate
	Diagnostics Diagnostics
}

type Service struct {
	projector Projector
	hydrator  Hydrator
}

func NewService(projector Projector, hydrator Hydrator) *Service {
	return &Service{projector: projector, hydrator: hydrator}
}

func (s *Service) Load(ctx context.Context, input LoadInput) (Window, bool, error) {
	if s.projector == nil {
		return Window{}, false, fmt.Errorf("candidate projector is required")
	}
	if s.hydrator == nil {
		return Window{}, false, fmt.Errorf("candidate hydrator is required")
	}
	requestedModel := strings.TrimSpace(input.RequestedModel)
	endpointFamily := strings.TrimSpace(input.EndpointFamily)
	projection, found, err := s.projector.Project(ctx, gatewayaccountcandidates.ProjectInput{
		GroupID:            input.GroupID,
		SystemAccountID:    input.SystemAccountID,
		RequestedModel:     requestedModel,
		EndpointFamily:     endpointFamily,
		IncludeUnavailable: input.IncludeUnavailable,
	})
	if err != nil || !found {
		return Window{}, found, err
	}
	if len(projection.Candidates) > port.GatewayAccountCandidateScanLimit {
		return Window{}, false, fmt.Errorf("candidate projector exceeded scan limit: %d", len(projection.Candidates))
	}

	candidates := append([]port.GatewayAccountCandidate(nil), projection.Candidates...)
	preRanks := map[string]CandidateRankFacts{}
	if ranker, ok := s.hydrator.(CandidatePreRanker); ok && len(candidates) > 0 {
		ranks, rankErr := ranker.PreRank(ctx, candidates)
		if rankErr != nil {
			return Window{}, false, fmt.Errorf("pre-rank gateway candidates: %w", rankErr)
		}
		preRanks = ranks
		slices.SortStableFunc(candidates, func(left, right port.GatewayAccountCandidate) int {
			return compareProjections(left, right, ranks)
		})
	}
	diagnostics := Diagnostics{
		ScanLimit:         port.GatewayAccountCandidateScanLimit,
		FinalLimit:        FinalLimit,
		CandidateRowCount: len(candidates),
		ScannedRowCount:   len(candidates),
		EligibleRowCount:  len(candidates),
		ScanLimitReached:  projection.LimitReached,
	}
	final := make([]Candidate, 0, min(FinalLimit, len(candidates)))
	for start := 0; start < len(candidates) && len(final) < FinalLimit; start += FinalLimit {
		end := min(start+FinalLimit, len(candidates))
		batch := candidates[start:end]
		results, hydrateErr := s.hydrator.Hydrate(ctx, HydrateInput{
			Candidates:     batch,
			RequestedModel: requestedModel,
			EndpointFamily: endpointFamily,
			PreRanks:       preRanks,
		})
		if hydrateErr != nil {
			return Window{}, false, fmt.Errorf("hydrate gateway candidate batch: %w", hydrateErr)
		}
		diagnostics.HydrationBatchCount++
		byID, indexErr := indexHydrationResults(batch, results)
		if indexErr != nil {
			return Window{}, false, indexErr
		}
		for _, row := range batch {
			result, ok := byID[row.AccountID]
			if !ok || strings.TrimSpace(result.DropReason) != "" {
				diagnostics.HydrationDroppedCount++
				continue
			}
			result.Candidate.Projection = row
			if result.Candidate.QualityScore == nil {
				result.Candidate.QualityScore = preRanks[row.AccountID].QualityScore
			}
			if modelRank(result.Candidate, requestedModel, endpointFamily) >= 3 {
				diagnostics.HydrationDroppedCount++
				continue
			}
			final = append(final, result.Candidate)
			diagnostics.HydratedAccountCount++
			if len(final) == FinalLimit {
				break
			}
		}
	}

	slices.SortStableFunc(final, func(left, right Candidate) int {
		return compareCandidates(left, right, requestedModel, endpointFamily)
	})
	diagnostics.FinalAccountCount = len(final)
	return Window{Access: projection.Access, Candidates: final, Diagnostics: diagnostics}, true, nil
}

// LoadPreBodyCapacity collects a bounded, credential-validated account view
// before a request body is read. It mirrors Node's runtime account loading:
// active, schedulable candidates are scanned in existing dispatch order; up
// to FinalLimit hydrated accounts are retained; a broken candidate may be
// skipped and a later row used to refill the bounded result. Unlike Load, it
// never evaluates model rank because model facts belong to the body stage.
func (s *Service) LoadPreBodyCapacity(ctx context.Context, input PreBodyCapacityInput) (PreBodyCapacitySnapshot, bool, error) {
	if s == nil || s.projector == nil {
		return PreBodyCapacitySnapshot{}, false, fmt.Errorf("candidate projector is required")
	}
	if s.hydrator == nil {
		return PreBodyCapacitySnapshot{}, false, fmt.Errorf("candidate hydrator is required")
	}
	projection, found, err := s.projector.Project(ctx, gatewayaccountcandidates.ProjectInput{
		GroupID: strings.TrimSpace(input.GroupID), SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil || !found {
		return PreBodyCapacitySnapshot{}, found, err
	}
	diagnostics := Diagnostics{
		ScanLimit:         port.GatewayAccountCandidateScanLimit,
		FinalLimit:        FinalLimit,
		CandidateRowCount: len(projection.Candidates),
		ScannedRowCount:   len(projection.Candidates),
		EligibleRowCount:  len(projection.Candidates),
		ScanLimitReached:  projection.LimitReached,
	}
	result := PreBodyCapacitySnapshot{
		Access: projection.Access, Candidates: make([]PreBodyCapacityCandidate, 0, min(FinalLimit, len(projection.Candidates))), Diagnostics: diagnostics,
	}
	for start := 0; start < len(projection.Candidates) && len(result.Candidates) < FinalLimit; start += FinalLimit {
		end := min(start+FinalLimit, len(projection.Candidates))
		batch := projection.Candidates[start:end]
		hydrated, hydrateErr := s.hydrator.Hydrate(ctx, HydrateInput{Candidates: batch})
		if hydrateErr != nil {
			return PreBodyCapacitySnapshot{}, false, fmt.Errorf("hydrate pre-body capacity candidates: %w", hydrateErr)
		}
		result.Diagnostics.HydrationBatchCount++
		byID, indexErr := indexHydrationResults(batch, hydrated)
		if indexErr != nil {
			return PreBodyCapacitySnapshot{}, false, indexErr
		}
		for _, row := range batch {
			hydration, ok := byID[row.AccountID]
			if !ok || strings.TrimSpace(hydration.DropReason) != "" {
				result.Diagnostics.HydrationDroppedCount++
				continue
			}
			result.Candidates = append(result.Candidates, preBodyCapacityCandidate(row))
			result.Diagnostics.HydratedAccountCount++
			if len(result.Candidates) == FinalLimit {
				break
			}
		}
	}
	result.Diagnostics.FinalAccountCount = len(result.Candidates)
	return result, true, nil
}

func preBodyCapacityCandidate(row port.GatewayAccountCandidate) PreBodyCapacityCandidate {
	accountID := strings.TrimSpace(row.AccountID)
	sourceID := strings.TrimSpace(row.ResourceAccountID)
	limit := row.ConcurrencyLimit
	if sourceID != "" {
		limit = row.ResourceConcurrencyLimit
		if sourceID == accountID {
			sourceID = ""
		}
	}
	return PreBodyCapacityCandidate{AccountID: accountID, CredentialSourceAccountID: sourceID, ConcurrencyLimit: limit}
}

func compareProjections(left, right port.GatewayAccountCandidate, ranks map[string]CandidateRankFacts) int {
	if comparison := compareInt(left.ModelRank, right.ModelRank); comparison != 0 {
		return comparison
	}
	if comparison := compareBool(left.LocalFallbackEnabled, right.LocalFallbackEnabled); comparison != 0 {
		return comparison
	}
	if comparison := compareBool(right.LocalSuperPriorityEnabled, left.LocalSuperPriorityEnabled); comparison != 0 {
		return comparison
	}
	if comparison := compareInt(left.LocalPriority, right.LocalPriority); comparison != 0 {
		return comparison
	}
	if comparison := compareQuality(ranks[left.AccountID].QualityScore, ranks[right.AccountID].QualityScore); comparison != 0 {
		return comparison
	}
	if comparison := strings.Compare(left.Name, right.Name); comparison != 0 {
		return comparison
	}
	return strings.Compare(left.AccountID, right.AccountID)
}

func indexHydrationResults(batch []port.GatewayAccountCandidate, results []HydrationResult) (map[string]HydrationResult, error) {
	allowed := make(map[string]struct{}, len(batch))
	for _, candidate := range batch {
		id := strings.TrimSpace(candidate.AccountID)
		if id == "" {
			return nil, fmt.Errorf("candidate projector returned empty account id")
		}
		if _, exists := allowed[id]; exists {
			return nil, fmt.Errorf("candidate projector returned duplicate account id %q", id)
		}
		allowed[id] = struct{}{}
	}
	indexed := make(map[string]HydrationResult, len(results))
	for _, result := range results {
		id := strings.TrimSpace(result.AccountID)
		if _, ok := allowed[id]; !ok {
			return nil, fmt.Errorf("candidate hydrator returned unknown account id %q", id)
		}
		if _, exists := indexed[id]; exists {
			return nil, fmt.Errorf("candidate hydrator returned duplicate account id %q", id)
		}
		indexed[id] = result
	}
	return indexed, nil
}

func compareCandidates(left, right Candidate, model, endpointFamily string) int {
	if comparison := compareInt(modelRank(left, model, endpointFamily), modelRank(right, model, endpointFamily)); comparison != 0 {
		return comparison
	}
	if comparison := compareBool(left.Projection.LocalFallbackEnabled, right.Projection.LocalFallbackEnabled); comparison != 0 {
		return comparison
	}
	if comparison := compareBool(right.Projection.LocalSuperPriorityEnabled, left.Projection.LocalSuperPriorityEnabled); comparison != 0 {
		return comparison
	}
	if comparison := compareInt(left.Projection.LocalPriority, right.Projection.LocalPriority); comparison != 0 {
		return comparison
	}
	if comparison := compareQuality(effectiveQuality(left), effectiveQuality(right)); comparison != 0 {
		return comparison
	}
	if comparison := strings.Compare(left.Projection.Name, right.Projection.Name); comparison != 0 {
		return comparison
	}
	return strings.Compare(left.Projection.AccountID, right.Projection.AccountID)
}

func effectiveQuality(candidate Candidate) *int64 {
	return candidate.QualityScore
}

func modelRank(candidate Candidate, model, endpointFamily string) int {
	resolution, ok := ResolveEffectiveModel(candidate, model, endpointFamily)
	if !ok {
		return 3
	}
	if resolution.MappingApplied {
		return 1
	}
	return 0
}

// CandidateSupportsRequest revalidates hydrated model facts immediately before
// a caller builds an upstream attempt. It is not an authorization lease.
func CandidateSupportsRequest(candidate Candidate, model, endpointFamily string) bool {
	return modelRank(candidate, strings.TrimSpace(model), strings.TrimSpace(endpointFamily)) < 3
}

func compareQuality(left, right *int64) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	return compareInt64(*left, *right)
}

func compareBool(left, right bool) int {
	if left == right {
		return 0
	}
	if left {
		return 1
	}
	return -1
}

func compareInt(left, right int) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareInt64(left, right int64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

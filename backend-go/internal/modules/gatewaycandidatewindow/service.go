package gatewaycandidatewindow

import (
	"context"
	"fmt"
	"slices"
	"strings"

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

type HydrateInput struct {
	Candidates     []port.GatewayAccountCandidate
	RequestedModel string
	EndpointFamily string
}

type ModelMapping struct {
	SourceModel            string
	SourceEndpointFamily   string
	UpstreamModel          string
	UpstreamEndpointFamily string
}

type APIKeyRuntime struct {
	KeyID         string
	Status        string
	CooldownUntil string
}

type ProxyRuntime struct {
	ID      string
	Type    string
	Enabled bool
}

// Candidate contains dispatch facts, but deliberately does not expose plaintext
// credentials. A later request assembler receives those through the hydrator's
// private runtime implementation rather than diagnostics or JSON DTOs.
type Candidate struct {
	Projection      port.GatewayAccountCandidate
	SupportedModels []string
	ModelMappings   []ModelMapping
	APIKeyRuntime   []APIKeyRuntime
	Proxy           *ProxyRuntime
	QualityScore    *int64
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

	diagnostics := Diagnostics{
		ScanLimit:         port.GatewayAccountCandidateScanLimit,
		FinalLimit:        FinalLimit,
		CandidateRowCount: len(projection.Candidates),
		ScannedRowCount:   len(projection.Candidates),
		ScanLimitReached:  projection.LimitReached,
	}
	final := make([]Candidate, 0, min(FinalLimit, len(projection.Candidates)))
	for start := 0; start < len(projection.Candidates) && len(final) < FinalLimit; start += FinalLimit {
		end := min(start+FinalLimit, len(projection.Candidates))
		batch := projection.Candidates[start:end]
		results, hydrateErr := s.hydrator.Hydrate(ctx, HydrateInput{
			Candidates:     batch,
			RequestedModel: requestedModel,
			EndpointFamily: endpointFamily,
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
			if modelRank(result.Candidate, requestedModel, endpointFamily) >= 3 {
				diagnostics.HydrationDroppedCount++
				continue
			}
			diagnostics.EligibleRowCount++
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
	if model == "" {
		return 0
	}
	for _, supported := range candidate.SupportedModels {
		if strings.EqualFold(strings.TrimSpace(supported), model) {
			return 0
		}
	}
	for _, mapping := range candidate.ModelMappings {
		if !strings.EqualFold(strings.TrimSpace(mapping.SourceModel), model) {
			continue
		}
		family := strings.TrimSpace(mapping.SourceEndpointFamily)
		if endpointFamily != "" && !strings.EqualFold(family, endpointFamily) {
			continue
		}
		upstream := strings.TrimSpace(mapping.UpstreamModel)
		if upstream == "" || strings.EqualFold(upstream, model) || !supportsModel(candidate.SupportedModels, upstream) {
			continue
		}
		return 1
	}
	if len(candidate.SupportedModels) == 0 {
		return 2
	}
	return 3
}

func supportsModel(models []string, wanted string) bool {
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model), wanted) {
			return true
		}
	}
	return false
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

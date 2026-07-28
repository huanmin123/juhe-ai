package gatewaycandidatewindow

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewayaccountcandidates"
	"juhe-ai/backend-go/internal/store/port"
)

func TestLoadHydratesInBoundedBatchesAndRefillsBrokenRows(t *testing.T) {
	rows := make([]port.GatewayAccountCandidate, 0, 264)
	for i := 0; i < 264; i++ {
		rows = append(rows, port.GatewayAccountCandidate{AccountID: "account_" + itoa(i), Name: "candidate"})
	}
	projector := &projectorStub{projection: gatewayaccountcandidates.Projection{Candidates: rows, ScanLimit: 512, LimitReached: false}}
	hydrator := &hydratorStub{dropFirstBatch: true, custom: make(map[string]HydrationResult, len(rows))}
	for index := range rows {
		hydrator.custom[rows[index].AccountID] = HydrationResult{AccountID: rows[index].AccountID, Candidate: Candidate{SupportedModels: []string{"gpt-5"}}}
	}
	window, found, err := NewService(projector, hydrator).Load(context.Background(), LoadInput{GroupID: "group", SystemAccountID: "user", RequestedModel: "gpt-5"})
	if err != nil || !found {
		t.Fatalf("Load() = found %v, err %v", found, err)
	}
	if len(window.Candidates) != 8 {
		t.Fatalf("final candidates = %d, want 8", len(window.Candidates))
	}
	if hydrator.calls != 2 || window.Diagnostics.HydrationBatchCount != 2 || window.Diagnostics.HydrationDroppedCount != 256 {
		t.Fatalf("hydration diagnostics = %+v calls=%d", window.Diagnostics, hydrator.calls)
	}
	if window.Diagnostics.CandidateRowCount != 264 || window.Diagnostics.ScannedRowCount != 264 {
		t.Fatalf("scan diagnostics = %+v", window.Diagnostics)
	}
}

func TestLoadStopsAfterSuccessfulFinalBatchAndSortsModelThenBusinessAndQuality(t *testing.T) {
	rows := make([]port.GatewayAccountCandidate, 0, 300)
	for i := 0; i < 300; i++ {
		rows = append(rows, port.GatewayAccountCandidate{AccountID: "account_" + itoa(i), Name: "candidate"})
	}
	rows[0].AccountID, rows[0].Name = "slow", "slow"
	rows[1].AccountID, rows[1].Name = "fast", "fast"
	projector := &projectorStub{projection: gatewayaccountcandidates.Projection{Candidates: rows, LimitReached: false}}
	hydrator := &hydratorStub{defaultSupportedModels: []string{"gpt-5.5"}, custom: map[string]HydrationResult{
		"slow": {AccountID: "slow", Candidate: Candidate{SupportedModels: []string{"gpt-5.5"}, QualityScore: ptr(int64(500))}},
		"fast": {AccountID: "fast", Candidate: Candidate{SupportedModels: []string{"gpt-5.5"}, QualityScore: ptr(int64(100))}},
	}}
	window, _, err := NewService(projector, hydrator).Load(context.Background(), LoadInput{RequestedModel: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if hydrator.calls != 1 || len(window.Candidates) != 256 {
		t.Fatalf("calls/final = %d/%d", hydrator.calls, len(window.Candidates))
	}
	if window.Candidates[0].Projection.AccountID != "fast" {
		t.Fatalf("model match did not rank first: %s", window.Candidates[0].Projection.AccountID)
	}
	if window.Diagnostics.FinalAccountCount != 256 || window.Diagnostics.HydratedAccountCount != 256 {
		t.Fatalf("diagnostics = %+v", window.Diagnostics)
	}
}

func TestLoadRejectsHydratorProtocolViolations(t *testing.T) {
	projector := &projectorStub{projection: gatewayaccountcandidates.Projection{Candidates: []port.GatewayAccountCandidate{{AccountID: "a"}}}}
	for _, testCase := range []struct {
		name string
		stub hydratorStub
	}{
		{name: "unknown", stub: hydratorStub{results: []HydrationResult{{AccountID: "b"}}}},
		{name: "duplicate", stub: hydratorStub{results: []HydrationResult{{AccountID: "a"}, {AccountID: "a"}}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := NewService(projector, &testCase.stub).Load(context.Background(), LoadInput{})
			if err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestLoadDropsRestrictedModelAndAcceptsVerifiedNonIdentityMapping(t *testing.T) {
	rows := []port.GatewayAccountCandidate{{AccountID: "restricted"}, {AccountID: "mapped"}, {AccountID: "invalid"}}
	projector := &projectorStub{projection: gatewayaccountcandidates.Projection{Candidates: rows}}
	hydrator := &hydratorStub{custom: map[string]HydrationResult{
		"restricted": {AccountID: "restricted", Candidate: Candidate{SupportedModels: []string{"gpt-4"}}},
		"mapped": {
			AccountID: "mapped",
			Candidate: Candidate{
				SupportedModels: []string{"gpt-5-upstream"},
				ModelMappings: []ModelMapping{{
					Enabled:                true,
					SourceModel:            "gpt-5",
					SourceEndpointFamily:   "responses",
					UpstreamModel:          "gpt-5-upstream",
					UpstreamEndpointFamily: "responses",
				}},
			},
		},
	}}
	window, _, err := NewService(projector, hydrator).Load(context.Background(), LoadInput{RequestedModel: "gpt-5", EndpointFamily: "responses"})
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 1 || window.Candidates[0].Projection.AccountID != "mapped" {
		t.Fatalf("candidates = %+v", window.Candidates)
	}
	if window.Diagnostics.EligibleRowCount != 3 || window.Diagnostics.HydrationDroppedCount != 2 {
		t.Fatalf("diagnostics = %+v", window.Diagnostics)
	}
}

func TestLoadDropsCandidatesWithoutRequestedModelOrSupportedModels(t *testing.T) {
	rows := []port.GatewayAccountCandidate{{AccountID: "empty-request"}, {AccountID: "empty-models"}}
	projector := &projectorStub{projection: gatewayaccountcandidates.Projection{Candidates: rows}}
	hydrator := &hydratorStub{custom: map[string]HydrationResult{
		"empty-request": {AccountID: "empty-request", Candidate: Candidate{SupportedModels: []string{"gpt-5"}}},
		"empty-models":  {AccountID: "empty-models", Candidate: Candidate{}},
	}}
	window, found, err := NewService(projector, hydrator).Load(context.Background(), LoadInput{})
	if err != nil || !found || len(window.Candidates) != 0 || window.Diagnostics.HydrationDroppedCount != 2 {
		t.Fatalf("Load() = window=%+v found=%v err=%v", window, found, err)
	}
}

func TestLoadPreRanksAllScannedRowsBeforeFirstHydrationBatch(t *testing.T) {
	rows := make([]port.GatewayAccountCandidate, 300)
	for index := range rows {
		rows[index] = port.GatewayAccountCandidate{AccountID: "account_" + itoa(index), Name: "candidate", ModelRank: 0}
	}
	lateID := rows[299].AccountID
	hydrator := &rankingHydratorStub{
		hydratorStub: hydratorStub{defaultSupportedModels: []string{"gpt-5"}},
		ranks: map[string]CandidateRankFacts{
			rows[0].AccountID: {QualityScore: ptr(int64(100))},
			lateID:            {QualityScore: ptr(int64(1))},
		},
	}
	window, _, err := NewService(&projectorStub{projection: gatewayaccountcandidates.Projection{Candidates: rows}}, hydrator).Load(context.Background(), LoadInput{RequestedModel: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	if hydrator.calls != 1 || len(window.Candidates) != FinalLimit {
		t.Fatalf("hydration calls/final = %d/%d", hydrator.calls, len(window.Candidates))
	}
	if window.Candidates[0].Projection.AccountID != lateID {
		t.Fatalf("late quality candidate was not promoted: %s", window.Candidates[0].Projection.AccountID)
	}
}

func TestLoadPropagatesDependenciesAndMissingGroup(t *testing.T) {
	if _, _, err := NewService(nil, &hydratorStub{}).Load(context.Background(), LoadInput{}); err == nil {
		t.Fatal("missing projector should fail")
	}
	if _, _, err := NewService(&projectorStub{}, nil).Load(context.Background(), LoadInput{}); err == nil {
		t.Fatal("missing hydrator should fail")
	}
	projector := &projectorStub{found: false}
	window, found, err := NewService(projector, &hydratorStub{}).Load(context.Background(), LoadInput{})
	if err != nil || found || window.Candidates != nil {
		t.Fatalf("missing group = %+v/%v/%v", window, found, err)
	}
	projector.err = errors.New("db unavailable")
	if _, _, err := NewService(projector, &hydratorStub{}).Load(context.Background(), LoadInput{}); !errors.Is(err, projector.err) {
		t.Fatal(err)
	}
}

type projectorStub struct {
	projection gatewayaccountcandidates.Projection
	found      bool
	err        error
}

func (s *projectorStub) Project(context.Context, gatewayaccountcandidates.ProjectInput) (gatewayaccountcandidates.Projection, bool, error) {
	found := s.found || s.projection.Candidates != nil
	return s.projection, found, s.err
}

type hydratorStub struct {
	calls                  int
	dropFirstBatch         bool
	defaultSupportedModels []string
	custom                 map[string]HydrationResult
	results                []HydrationResult
}

type rankingHydratorStub struct {
	hydratorStub
	ranks map[string]CandidateRankFacts
	err   error
}

func (s *rankingHydratorStub) PreRank(context.Context, []port.GatewayAccountCandidate) (map[string]CandidateRankFacts, error) {
	return s.ranks, s.err
}

func (s *hydratorStub) Hydrate(_ context.Context, input HydrateInput) ([]HydrationResult, error) {
	s.calls++
	if s.results != nil {
		return s.results, nil
	}
	if s.dropFirstBatch && s.calls == 1 {
		return []HydrationResult{}, nil
	}
	results := make([]HydrationResult, 0, len(input.Candidates))
	for _, row := range input.Candidates {
		if result, ok := s.custom[row.AccountID]; ok {
			results = append(results, result)
			continue
		}
		results = append(results, HydrationResult{AccountID: row.AccountID, Candidate: Candidate{SupportedModels: append([]string(nil), s.defaultSupportedModels...)}})
	}
	return results, nil
}

func ptr(value int64) *int64 { return &value }

func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	result := ""
	for value > 0 {
		result = string(digits[value%10]) + result
		value /= 10
	}
	return result
}

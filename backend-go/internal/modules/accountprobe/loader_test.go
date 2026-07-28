package accountprobe

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

func TestLoaderUsesExactAccountFilterAndHydratesProjection(t *testing.T) {
	now := time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC)
	row := port.GatewayAccountCandidate{AccountID: "account-1", GroupID: "group-1", SystemAccountID: "system-1"}
	reader := &candidateReaderStub{found: true, access: port.GatewayGroupAccess{
		GroupID: "group-1", CallerSystemAccountID: "system-1", GroupOwnerSystemAccountID: "system-1", ProviderCode: "openai", AccessType: port.GatewayGroupAccessOwner,
	}, rows: []port.GatewayAccountCandidate{row}}
	hydrator := &candidateHydratorStub{results: []gatewaycandidatewindow.HydrationResult{{
		AccountID: "account-1", Candidate: gatewaycandidatewindow.Candidate{Projection: row, DefaultBaseURL: "https://api.example.com", SupportedModels: []string{"gpt-5"}},
	}}}
	candidate, found, err := (Loader{Reader: reader, Hydrator: hydrator}).Load(context.Background(), LoadInput{
		AccountID: " account-1 ", GroupID: " group-1 ", SystemAccountID: " system-1 ",
		RequestedModel: " gpt-5 ", EndpointFamily: " responses ", Now: now,
	})
	if err != nil || !found {
		t.Fatalf("Load() found=%v error=%v", found, err)
	}
	want := port.GatewayAccountCandidateListInput{
		Access: reader.access, AccountID: "account-1", Now: now, IncludeUnavailable: true,
		RequestedModel: "gpt-5", EndpointFamily: "responses", Limit: 2,
	}
	if !reflect.DeepEqual(reader.listInput, want) {
		t.Fatalf("list input = %+v, want %+v", reader.listInput, want)
	}
	if candidate.Projection.AccountID != "account-1" || candidate.DefaultBaseURL != "https://api.example.com" {
		t.Fatalf("candidate = %+v", candidate)
	}
}

func TestLoaderFailsClosedForMissingAccessNonUniqueAndHydrationDrop(t *testing.T) {
	valid := LoadInput{AccountID: "a", GroupID: "g", SystemAccountID: "s", RequestedModel: "m", EndpointFamily: "responses"}
	for _, test := range []struct {
		name      string
		reader    *candidateReaderStub
		hydrator  *candidateHydratorStub
		wantFound bool
		wantError string
	}{
		{name: "missing access", reader: &candidateReaderStub{}, hydrator: &candidateHydratorStub{}},
		{name: "duplicate rows", reader: &candidateReaderStub{found: true, rows: []port.GatewayAccountCandidate{{AccountID: "a"}, {AccountID: "a"}}}, hydrator: &candidateHydratorStub{}, wantError: "non-unique"},
		{name: "hydration drop", reader: &candidateReaderStub{found: true, rows: []port.GatewayAccountCandidate{{AccountID: "a"}}}, hydrator: &candidateHydratorStub{results: []gatewaycandidatewindow.HydrationResult{{AccountID: "a", DropReason: gatewaycandidatewindow.DropCredentialDecrypt}}}, wantError: "credential_decrypt_failed"},
		{name: "stale model facts", reader: &candidateReaderStub{found: true, rows: []port.GatewayAccountCandidate{{AccountID: "a"}}}, hydrator: &candidateHydratorStub{results: []gatewaycandidatewindow.HydrationResult{{AccountID: "a", Candidate: gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: "a"}}}}}, wantError: "no longer supports"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, found, err := (Loader{Reader: test.reader, Hydrator: test.hydrator}).Load(context.Background(), valid)
			if found != test.wantFound || (test.wantError == "" && err != nil) || (test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError))) {
				t.Fatalf("Load() found=%v error=%v", found, err)
			}
		})
	}
}

func TestLoaderPropagatesReaderAndHydratorErrors(t *testing.T) {
	valid := LoadInput{AccountID: "a", GroupID: "g", SystemAccountID: "s", RequestedModel: "m", EndpointFamily: "responses"}
	readerErr := errors.New("reader unavailable")
	if _, _, err := (Loader{Reader: &candidateReaderStub{found: true, err: readerErr}, Hydrator: &candidateHydratorStub{}}).Load(context.Background(), valid); !errors.Is(err, readerErr) {
		t.Fatalf("reader error = %v", err)
	}
	hydratorErr := errors.New("hydrator unavailable")
	reader := &candidateReaderStub{found: true, rows: []port.GatewayAccountCandidate{{AccountID: "a"}}}
	if _, _, err := (Loader{Reader: reader, Hydrator: &candidateHydratorStub{err: hydratorErr}}).Load(context.Background(), valid); !errors.Is(err, hydratorErr) {
		t.Fatalf("hydrator error = %v", err)
	}
}

type candidateReaderStub struct {
	access    port.GatewayGroupAccess
	found     bool
	err       error
	rows      []port.GatewayAccountCandidate
	listInput port.GatewayAccountCandidateListInput
}

func (s *candidateReaderStub) ResolveGatewayGroupAccess(context.Context, port.GatewayGroupAccessInput) (port.GatewayGroupAccess, bool, error) {
	return s.access, s.found, s.err
}

func (s *candidateReaderStub) ListGatewayAccountCandidates(_ context.Context, input port.GatewayAccountCandidateListInput) ([]port.GatewayAccountCandidate, error) {
	s.listInput = input
	return s.rows, s.err
}

type candidateHydratorStub struct {
	results []gatewaycandidatewindow.HydrationResult
	err     error
}

func (s *candidateHydratorStub) Hydrate(context.Context, gatewaycandidatewindow.HydrateInput) ([]gatewaycandidatewindow.HydrationResult, error) {
	return s.results, s.err
}

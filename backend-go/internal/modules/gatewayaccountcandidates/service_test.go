package gatewayaccountcandidates

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceProjectResolvesAccessBeforeBoundedCandidateRead(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 11, 12, 0, time.UTC)
	store := &candidateStoreStub{
		access: port.GatewayGroupAccess{
			GroupID:                   "group_1",
			CallerSystemAccountID:     "sys_grantee",
			GroupOwnerSystemAccountID: "sys_owner",
			ProviderCode:              "gpt",
			AccessType:                port.GatewayGroupAccessAuthorized,
			GroupAuthorizationID:      "auth_group_1",
		},
		found:      true,
		candidates: []port.GatewayAccountCandidate{{AccountID: "account_1"}},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	projection, found, err := service.Project(context.Background(), ProjectInput{
		GroupID:            " group_1 ",
		SystemAccountID:    " sys_grantee ",
		IncludeUnavailable: true,
	})
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if !found {
		t.Fatal("Project() found = false")
	}
	if projection.Access.GroupAuthorizationID != "auth_group_1" || len(projection.Candidates) != 1 {
		t.Fatalf("projection = %+v", projection)
	}
	wantResolve := port.GatewayGroupAccessInput{
		GroupID:         "group_1",
		SystemAccountID: "sys_grantee",
		Now:             now,
	}
	if !reflect.DeepEqual(store.resolveInput, wantResolve) {
		t.Fatalf("resolve input = %+v, want %+v", store.resolveInput, wantResolve)
	}
	wantList := port.GatewayAccountCandidateListInput{
		Access:             store.access,
		Now:                now,
		IncludeUnavailable: true,
		Limit:              port.GatewayAccountCandidateScanLimit,
	}
	if !reflect.DeepEqual(store.listInput, wantList) {
		t.Fatalf("list input = %+v, want %+v", store.listInput, wantList)
	}
}

func TestServiceProjectStopsWhenGroupAccessIsNotVisible(t *testing.T) {
	store := &candidateStoreStub{}
	service := NewService(store)

	projection, found, err := service.Project(context.Background(), ProjectInput{
		GroupID:         "group_missing",
		SystemAccountID: "sys_user",
	})
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if found || store.listCalls != 0 {
		t.Fatalf("found/list calls = %v/%d", found, store.listCalls)
	}
	if projection.Candidates != nil {
		t.Fatalf("projection = %+v", projection)
	}
}

func TestServiceProjectValidatesFoundationDependenciesAndScope(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		service   *Service
		input     ProjectInput
		wantError string
	}{
		{name: "store", service: NewService(nil), input: ProjectInput{GroupID: "group_1", SystemAccountID: "sys_1"}, wantError: "store is required"},
		{name: "group", service: NewService(&candidateStoreStub{}), input: ProjectInput{SystemAccountID: "sys_1"}, wantError: "group id is required"},
		{name: "caller", service: NewService(&candidateStoreStub{}), input: ProjectInput{GroupID: "group_1"}, wantError: "system account id is required"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := testCase.service.Project(context.Background(), testCase.input)
			if err == nil || err.Error() != testCase.wantError {
				t.Fatalf("Project() error = %v, want %q", err, testCase.wantError)
			}
		})
	}
}

func TestServiceProjectPropagatesStoreErrors(t *testing.T) {
	wantErr := errors.New("db unavailable")
	store := &candidateStoreStub{resolveErr: wantErr}
	_, _, err := NewService(store).Project(context.Background(), ProjectInput{GroupID: "group_1", SystemAccountID: "sys_1"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Project() error = %v, want %v", err, wantErr)
	}
	store = &candidateStoreStub{found: true, listErr: wantErr}
	_, _, err = NewService(store).Project(context.Background(), ProjectInput{GroupID: "group_1", SystemAccountID: "sys_1"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Project() list error = %v, want %v", err, wantErr)
	}
}

type candidateStoreStub struct {
	access       port.GatewayGroupAccess
	found        bool
	candidates   []port.GatewayAccountCandidate
	resolveErr   error
	listErr      error
	resolveInput port.GatewayGroupAccessInput
	listInput    port.GatewayAccountCandidateListInput
	listCalls    int
}

func (s *candidateStoreStub) ResolveGatewayGroupAccess(_ context.Context, input port.GatewayGroupAccessInput) (port.GatewayGroupAccess, bool, error) {
	s.resolveInput = input
	return s.access, s.found, s.resolveErr
}

func (s *candidateStoreStub) ListGatewayAccountCandidates(_ context.Context, input port.GatewayAccountCandidateListInput) ([]port.GatewayAccountCandidate, error) {
	s.listCalls++
	s.listInput = input
	return s.candidates, s.listErr
}

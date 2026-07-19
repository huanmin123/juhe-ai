package accountpagedata

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestResolveOwnersMergesNormalizesAndSortsKnownOwnersAndGrantees(t *testing.T) {
	reader := &granteeReaderStub{ids: []string{" grantee-b ", "owner-a", "grantee-a"}}

	owners, allScopes, err := ResolveOwners(t.Context(), reader, " account-1 ", []string{" owner-b ", "owner-a", ""})
	if err != nil {
		t.Fatalf("ResolveOwners() error = %v", err)
	}
	if allScopes {
		t.Fatal("ResolveOwners() allScopes = true, want false")
	}
	if got, want := reader.accountID, "account-1"; got != want {
		t.Fatalf("lookup account id = %q, want %q", got, want)
	}
	if got, want := owners, []string{"grantee-a", "grantee-b", "owner-a", "owner-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("owners = %#v, want %#v", got, want)
	}
}

func TestResolveOwnersFallsBackToAllScopesWhenOwnerLimitIsExceeded(t *testing.T) {
	grantees := make([]string, MaxEventOwners+1)
	for index := range grantees {
		grantees[index] = fmt.Sprintf("grantee-%03d", index)
	}

	owners, allScopes, err := ResolveOwners(t.Context(), &granteeReaderStub{ids: grantees}, "account-1", nil)
	if err != nil {
		t.Fatalf("ResolveOwners() error = %v", err)
	}
	if !allScopes || len(owners) != 0 {
		t.Fatalf("ResolveOwners() owners=%#v allScopes=%v, want empty global fallback", owners, allScopes)
	}
}

func TestResolveOwnersFallsBackToKnownOwnersAndAllScopes(t *testing.T) {
	cause := errors.New("lookup failed")
	for _, reader := range []GranteeReader{nil, &granteeReaderStub{err: cause}} {
		owners, allScopes, err := ResolveOwners(context.Background(), reader, "account-1", []string{" owner-b ", "owner-a"})
		if err == nil {
			t.Fatal("ResolveOwners() error = nil")
		}
		if !allScopes {
			t.Fatal("ResolveOwners() allScopes = false, want true")
		}
		if got, want := owners, []string{"owner-a", "owner-b"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("owners = %#v, want %#v", got, want)
		}
	}
}

func TestChangeOperationsAreExplicit(t *testing.T) {
	if OperationUpsert != Operation("upsert") || OperationDelete != Operation("delete") {
		t.Fatalf("operations = %q/%q", OperationUpsert, OperationDelete)
	}
}

type granteeReaderStub struct {
	ids       []string
	err       error
	accountID string
}

func (s *granteeReaderStub) ListAccountAuthorizationGranteeIDs(_ context.Context, accountID string) ([]string, error) {
	s.accountID = accountID
	return append([]string(nil), s.ids...), s.err
}

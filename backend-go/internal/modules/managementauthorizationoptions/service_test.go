package managementauthorizationoptions

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestGranteeAccountsNormalizesInputAndMapsOptions(t *testing.T) {
	store := &authorizationOptionStoreStub{
		granteeAccounts: []port.ManagementAuthorizationGranteeAccountOption{{
			ID:          "sys_user",
			Username:    "user",
			DisplayName: "用户",
			Status:      "active",
		}},
	}
	service := NewService(store)

	got, err := service.GranteeAccounts(context.Background(), PrincipalOptionListInput{
		IDs:     []string{" sys_user ", "sys_user", "", "sys_disabled"},
		Keyword: "  用户  ",
		Limit:   500,
	})
	if err != nil {
		t.Fatalf("GranteeAccounts() error = %v", err)
	}
	if store.input.Keyword != "用户" || store.input.Limit != 50 {
		t.Fatalf("store input = %+v, want trimmed keyword and limit 50", store.input)
	}
	if len(store.input.IDs) != 2 || store.input.IDs[0] != "sys_user" || store.input.IDs[1] != "sys_disabled" {
		t.Fatalf("ids = %#v", store.input.IDs)
	}
	if len(got) != 1 || got[0].ID != "sys_user" || got[0].Username != "user" || got[0].DisplayName != "用户" || got[0].Status != "active" {
		t.Fatalf("GranteeAccounts() = %+v", got)
	}
}

func TestGranteeAccountsDefaultsLimit(t *testing.T) {
	store := &authorizationOptionStoreStub{}
	service := NewService(store)

	if _, err := service.GranteeAccounts(context.Background(), PrincipalOptionListInput{Limit: -10}); err != nil {
		t.Fatalf("GranteeAccounts() error = %v", err)
	}
	if store.input.Limit != 50 {
		t.Fatalf("limit = %d, want 50", store.input.Limit)
	}
}

func TestGranteeAccountsReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&authorizationOptionStoreStub{err: want})

	_, err := service.GranteeAccounts(context.Background(), PrincipalOptionListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("GranteeAccounts() error = %v, want %v", err, want)
	}
}

type authorizationOptionStoreStub struct {
	input           port.ManagementAuthorizationPrincipalOptionListInput
	granteeAccounts []port.ManagementAuthorizationGranteeAccountOption
	err             error
}

func (s *authorizationOptionStoreStub) ListManagementAuthorizationGranteeAccounts(_ context.Context, input port.ManagementAuthorizationPrincipalOptionListInput) ([]port.ManagementAuthorizationGranteeAccountOption, error) {
	s.input = input
	return s.granteeAccounts, s.err
}

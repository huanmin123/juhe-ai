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

func TestGranteeTeamsNormalizesInputAndMapsOptions(t *testing.T) {
	store := &authorizationOptionStoreStub{
		granteeTeams: []port.ManagementAuthorizationGranteeTeamOption{{
			ID:     "team_ops",
			Name:   "运维团队",
			Status: "active",
		}},
	}
	service := NewService(store)

	got, err := service.GranteeTeams(context.Background(), PrincipalOptionListInput{
		IDs:     []string{" team_ops ", "team_ops", "", "team_disabled"},
		Keyword: "  运维  ",
		Limit:   500,
	})
	if err != nil {
		t.Fatalf("GranteeTeams() error = %v", err)
	}
	if store.teamInput.Keyword != "运维" || store.teamInput.Limit != 50 {
		t.Fatalf("store team input = %+v, want trimmed keyword and limit 50", store.teamInput)
	}
	if len(store.teamInput.IDs) != 2 || store.teamInput.IDs[0] != "team_ops" || store.teamInput.IDs[1] != "team_disabled" {
		t.Fatalf("team ids = %#v", store.teamInput.IDs)
	}
	if len(got) != 1 || got[0].ID != "team_ops" || got[0].Name != "运维团队" || got[0].Status != "active" {
		t.Fatalf("GranteeTeams() = %+v", got)
	}
}

func TestGranteeTeamsReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&authorizationOptionStoreStub{err: want})

	_, err := service.GranteeTeams(context.Background(), PrincipalOptionListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("GranteeTeams() error = %v, want %v", err, want)
	}
}

func TestGranteeGroupsNormalizesInputAndMapsOptions(t *testing.T) {
	store := &authorizationOptionStoreStub{
		granteeGroups: []port.ManagementAuthorizationGranteeGroupOption{{
			ID:   "grp_default",
			Name: "默认分组",
		}},
	}
	service := NewService(store)

	got, err := service.GranteeGroups(context.Background(), GranteeGroupOptionListInput{
		GranteeSystemAccountID: " sys_user ",
		IDs:                    []string{" grp_default ", "grp_default", "", "grp_backup"},
		Keyword:                "  默认  ",
		ProviderCode:           " openai ",
		Limit:                  500,
		PreferDefault:          true,
	})
	if err != nil {
		t.Fatalf("GranteeGroups() error = %v", err)
	}
	if store.groupInput.GranteeSystemAccountID != "sys_user" ||
		store.groupInput.Keyword != "默认" ||
		store.groupInput.ProviderCode != "openai" ||
		store.groupInput.Limit != 50 ||
		!store.groupInput.PreferDefault {
		t.Fatalf("store group input = %+v", store.groupInput)
	}
	if len(store.groupInput.IDs) != 2 || store.groupInput.IDs[0] != "grp_default" || store.groupInput.IDs[1] != "grp_backup" {
		t.Fatalf("group ids = %#v", store.groupInput.IDs)
	}
	if len(got) != 1 {
		t.Fatalf("GranteeGroups() = %+v", got)
	}
	item := got[0]
	if item.ID != "grp_default" || item.Name != "默认分组" {
		t.Fatalf("mapped group = %+v", item)
	}
}

func TestGranteeGroupsReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&authorizationOptionStoreStub{err: want})

	_, err := service.GranteeGroups(context.Background(), GranteeGroupOptionListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("GranteeGroups() error = %v, want %v", err, want)
	}
}

type authorizationOptionStoreStub struct {
	input           port.ManagementAuthorizationPrincipalOptionListInput
	teamInput       port.ManagementAuthorizationPrincipalOptionListInput
	groupInput      port.ManagementAuthorizationGranteeGroupOptionListInput
	granteeAccounts []port.ManagementAuthorizationGranteeAccountOption
	granteeTeams    []port.ManagementAuthorizationGranteeTeamOption
	granteeGroups   []port.ManagementAuthorizationGranteeGroupOption
	err             error
}

func (s *authorizationOptionStoreStub) ListManagementAuthorizationGranteeAccounts(_ context.Context, input port.ManagementAuthorizationPrincipalOptionListInput) ([]port.ManagementAuthorizationGranteeAccountOption, error) {
	s.input = input
	return s.granteeAccounts, s.err
}

func (s *authorizationOptionStoreStub) ListManagementAuthorizationGranteeTeams(_ context.Context, input port.ManagementAuthorizationPrincipalOptionListInput) ([]port.ManagementAuthorizationGranteeTeamOption, error) {
	s.teamInput = input
	return s.granteeTeams, s.err
}

func (s *authorizationOptionStoreStub) ListManagementAuthorizationGranteeGroups(_ context.Context, input port.ManagementAuthorizationGranteeGroupOptionListInput) ([]port.ManagementAuthorizationGranteeGroupOption, error) {
	s.groupInput = input
	return s.granteeGroups, s.err
}

package managementgroups

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceOptionsNormalizesInputAndMapsOwnerOptions(t *testing.T) {
	store := &groupOptionStoreStub{
		options: []port.ManagementGroupOption{
			{
				ID:                                 "group_default",
				SystemAccountID:                    "sys_admin",
				SystemAccountName:                  "管理员",
				OwnerSystemAccountID:               "sys_admin",
				OwnerSystemAccountName:             "管理员",
				Name:                               "默认分组",
				ProviderCode:                       "openai",
				Enabled:                            true,
				IsDefault:                          true,
				GroupType:                          "high_concurrency",
				SchedulingPolicy:                   map[string]any{"mode": "balanced_fast"},
				HasActiveManualAuthorizationSource: true,
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{
		SystemAccountID:            " sys_admin ",
		IncludeSystemAccountFields: true,
		IDs:                        []string{" group_default ", "group_default", "", "group_disabled"},
		Keyword:                    " 默认 ",
		ProviderCode:               " openai ",
		Limit:                      500,
		ManageableOnly:             true,
		PreferDefault:              true,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}

	if store.input.SystemAccountID != "sys_admin" ||
		store.input.Keyword != "默认" ||
		store.input.ProviderCode != "openai" ||
		store.input.Limit != 50 ||
		!store.input.ManageableOnly ||
		!store.input.PreferDefault ||
		!store.input.IncludeSystemAccountFields {
		t.Fatalf("store input = %+v", store.input)
	}
	if len(store.input.IDs) != 2 || store.input.IDs[0] != "group_default" || store.input.IDs[1] != "group_disabled" {
		t.Fatalf("store ids = %#v", store.input.IDs)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.SystemAccountID != "sys_admin" ||
		got.SystemAccountName != "管理员" ||
		got.OwnerSystemAccountID != "sys_admin" ||
		got.OwnerSystemAccountName != "管理员" ||
		got.ProviderCode != "openai" ||
		got.GroupType != "high_concurrency" ||
		got.AccessType != "owner" ||
		!got.Enabled ||
		!got.IsDefault {
		t.Fatalf("option = %+v", got)
	}
	if got.Permissions != ownerPermissions() {
		t.Fatalf("permissions = %+v", got.Permissions)
	}
}

func TestServiceOptionsKeepsAuthorizedGroupReturnPermissionFalseWithoutManualSource(t *testing.T) {
	store := &groupOptionStoreStub{
		options: []port.ManagementGroupOption{
			{
				ID:                   "group_team_authorized",
				OwnerSystemAccountID: "sys_owner",
				Name:                 "团队授权分组",
				ProviderCode:         "openai",
				Enabled:              true,
				GroupType:            "personal",
				AccessType:           "authorized",
				GroupAuthorizationID: "auth_group_team",
				AuthorizationStatus:  "active",
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{SystemAccountID: "sys_user"})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %+v", options)
	}
	if options[0].Permissions.CanReturnAuthorization {
		t.Fatalf("authorized permissions = %+v, want canReturnAuthorization=false without active manual source", options[0].Permissions)
	}
}

func TestServiceOptionsMapsAuthorizedOptions(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	store := &groupOptionStoreStub{
		options: []port.ManagementGroupOption{
			{
				ID:                                 "group_authorized",
				OwnerSystemAccountID:               "sys_owner",
				OwnerSystemAccountName:             "所有者",
				Name:                               "授权分组",
				ProviderCode:                       "openai",
				Enabled:                            true,
				GroupType:                          "personal",
				AccessType:                         "authorized",
				GroupAuthorizationID:               "auth_group_1",
				AuthorizationStatus:                "active",
				AuthorizationExpiresAt:             &expiresAt,
				AuthorizationLimits:                map[string]any{"daily": map[string]any{"limit": float64(100)}},
				HasActiveManualAuthorizationSource: true,
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{
		SystemAccountID: "sys_user",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}

	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.AccessType != "authorized" ||
		got.GroupAuthorizationID != "auth_group_1" ||
		got.AuthorizationStatus != "active" ||
		got.AuthorizationExpiresAt == nil ||
		got.AuthorizationLimits["daily"] == nil ||
		got.OwnerSystemAccountName != "所有者" ||
		got.SystemAccountID != "" {
		t.Fatalf("authorized option = %+v", got)
	}
	if got.Permissions.CanAuthorize ||
		got.Permissions.CanDelete ||
		got.Permissions.CanManageAccounts ||
		got.Permissions.CanViewCredentials ||
		!got.Permissions.CanUse ||
		!got.Permissions.CanEdit ||
		!got.Permissions.CanReturnAuthorization ||
		!got.Permissions.CanBindToAPIKey {
		t.Fatalf("authorized permissions = %+v", got.Permissions)
	}
}

func TestAuthorizedGroupCanBindConditions(t *testing.T) {
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	tests := []struct {
		name      string
		enabled   bool
		status    string
		expiresAt *time.Time
		want      bool
	}{
		{name: "active enabled no expiry", enabled: true, status: "active", want: true},
		{name: "active enabled future expiry", enabled: true, status: "active", expiresAt: &future, want: true},
		{name: "disabled", enabled: false, status: "active", expiresAt: &future, want: false},
		{name: "paused", enabled: true, status: "paused", expiresAt: &future, want: false},
		{name: "expired status", enabled: true, status: "expired", expiresAt: &future, want: false},
		{name: "past expiry", enabled: true, status: "active", expiresAt: &past, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canBindAuthorizedGroup(tt.enabled, tt.status, tt.expiresAt); got != tt.want {
				t.Fatalf("canBindAuthorizedGroup() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServiceOptionsDefaults(t *testing.T) {
	store := &groupOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 {
		t.Fatalf("limit = %d, want 50", store.input.Limit)
	}
}

func TestServiceAccountOptionsNormalizesInputAndMapsAccountIDs(t *testing.T) {
	store := &groupOptionStoreStub{
		accountOptions: []port.ManagementGroupAccountOption{
			{
				ID:                     "group_default",
				SystemAccountID:        "sys_admin",
				SystemAccountName:      "管理员",
				OwnerSystemAccountID:   "sys_admin",
				OwnerSystemAccountName: "管理员",
				Name:                   "默认分组",
				ProviderCode:           "openai",
				Enabled:                true,
				IsDefault:              true,
				GroupType:              "personal",
				AccountIDs:             []string{"acct_a", "acct_b"},
			},
		},
	}
	service := NewService(store)

	options, err := service.AccountOptions(context.Background(), OptionListInput{
		SystemAccountID:            " sys_admin ",
		IncludeSystemAccountFields: true,
		IDs:                        []string{" group_default ", "group_default", "group_backup"},
		Keyword:                    " 默认 ",
		ProviderCode:               " openai ",
		Limit:                      500,
		ManageableOnly:             true,
		PreferDefault:              true,
	})
	if err != nil {
		t.Fatalf("AccountOptions() error = %v", err)
	}

	if store.accountInput.SystemAccountID != "sys_admin" ||
		store.accountInput.Keyword != "默认" ||
		store.accountInput.ProviderCode != "openai" ||
		store.accountInput.Limit != 50 ||
		!store.accountInput.ManageableOnly ||
		!store.accountInput.PreferDefault ||
		!store.accountInput.IncludeSystemAccountFields {
		t.Fatalf("store account input = %+v", store.accountInput)
	}
	if len(store.accountInput.IDs) != 2 || store.accountInput.IDs[0] != "group_default" || store.accountInput.IDs[1] != "group_backup" {
		t.Fatalf("store account ids = %#v", store.accountInput.IDs)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.ID != "group_default" ||
		got.SystemAccountID != "sys_admin" ||
		got.AccessType != "owner" ||
		len(got.AccountIDs) != 2 ||
		got.AccountIDs[0] != "acct_a" ||
		got.AccountIDs[1] != "acct_b" {
		t.Fatalf("account option = %+v", got)
	}
	if got.Permissions != ownerPermissions() {
		t.Fatalf("permissions = %+v", got.Permissions)
	}
}

func TestServiceAccountOptionsMapsAuthorizedAccountIDsEmpty(t *testing.T) {
	store := &groupOptionStoreStub{
		accountOptions: []port.ManagementGroupAccountOption{
			{
				ID:                     "group_authorized",
				OwnerSystemAccountID:   "sys_owner",
				OwnerSystemAccountName: "所有者",
				Name:                   "授权分组",
				ProviderCode:           "openai",
				Enabled:                true,
				GroupType:              "personal",
				AccessType:             "authorized",
				GroupAuthorizationID:   "auth_group_1",
				AuthorizationStatus:    "paused",
				AccountIDs:             []string{},
			},
		},
	}
	service := NewService(store)

	options, err := service.AccountOptions(context.Background(), OptionListInput{SystemAccountID: "sys_user"})
	if err != nil {
		t.Fatalf("AccountOptions() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.AccessType != "authorized" ||
		got.GroupAuthorizationID != "auth_group_1" ||
		got.AuthorizationStatus != "paused" ||
		got.OwnerSystemAccountName != "所有者" ||
		len(got.AccountIDs) != 0 ||
		got.Permissions.CanBindToAPIKey ||
		got.Permissions.CanReturnAuthorization {
		t.Fatalf("authorized account option = %+v", got)
	}
}

func TestServiceAccountOptionsMapsAuthorizedReturnPermission(t *testing.T) {
	store := &groupOptionStoreStub{
		accountOptions: []port.ManagementGroupAccountOption{
			{
				ID:                                 "group_authorized",
				OwnerSystemAccountID:               "sys_owner",
				Name:                               "授权分组",
				ProviderCode:                       "openai",
				Enabled:                            true,
				GroupType:                          "personal",
				AccessType:                         "authorized",
				GroupAuthorizationID:               "auth_group_1",
				AuthorizationStatus:                "active",
				HasActiveManualAuthorizationSource: true,
			},
		},
	}
	service := NewService(store)

	options, err := service.AccountOptions(context.Background(), OptionListInput{SystemAccountID: "sys_user"})
	if err != nil {
		t.Fatalf("AccountOptions() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if !got.Permissions.CanReturnAuthorization {
		t.Fatalf("authorized account permissions = %+v", got.Permissions)
	}
}

type groupOptionStoreStub struct {
	input          port.ManagementGroupOptionListInput
	accountInput   port.ManagementGroupOptionListInput
	options        []port.ManagementGroupOption
	accountOptions []port.ManagementGroupAccountOption
	err            error
}

func (s *groupOptionStoreStub) ListManagementGroupOptions(_ context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupOption, error) {
	s.input = input
	return s.options, s.err
}

func (s *groupOptionStoreStub) ListManagementGroupAccountOptions(_ context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupAccountOption, error) {
	s.accountInput = input
	return s.accountOptions, s.err
}

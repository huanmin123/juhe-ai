package managementaccounts

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceOptionsNormalizesInputAndMapsOwnerOptions(t *testing.T) {
	expiresAt := time.Date(2026, 7, 8, 9, 0, 0, 123, time.UTC)
	store := &accountOptionStoreStub{
		options: []port.ManagementAccountOption{
			{
				ID:                        "acct_main",
				SystemAccountID:           "sys_user",
				SystemAccountName:         "用户",
				OwnerSystemAccountID:      "sys_user",
				OwnerSystemAccountName:    "用户",
				ProviderCode:              "openai",
				ProviderProtocolProfileID: "profile_openai_openai_v1",
				ProtocolCode:              "openai",
				ProtocolVersion:           "v1",
				Name:                      "主账号",
				Type:                      "api_key",
				Status:                    "active",
				AccountExpiresAt:          &expiresAt,
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{
		SystemAccountID:            " sys_user ",
		IncludeSystemAccountFields: true,
		IDs:                        []string{" acct_main ", "acct_main", "", "acct_backup"},
		Keyword:                    " 主 ",
		ProviderCode:               " openai ",
		GroupID:                    " group_default ",
		Type:                       " api_key ",
		Status:                     "active, all,disabled,active",
		Schedulable:                "enabled",
		Page:                       3,
		Limit:                      500,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}

	if store.input.SystemAccountID != "sys_user" ||
		!store.input.IncludeSystemAccountFields ||
		store.input.Keyword != "主" ||
		store.input.ProviderCode != "openai" ||
		store.input.GroupID != "group_default" ||
		store.input.Type != "api_key" ||
		store.input.Schedulable != "enabled" ||
		store.input.Limit != 50 ||
		store.input.Offset != 100 {
		t.Fatalf("store input = %+v", store.input)
	}
	if len(store.input.IDs) != 2 || store.input.IDs[0] != "acct_main" || store.input.IDs[1] != "acct_backup" {
		t.Fatalf("store ids = %#v", store.input.IDs)
	}
	if len(store.input.Statuses) != 2 || store.input.Statuses[0] != "active" || store.input.Statuses[1] != "disabled" {
		t.Fatalf("store statuses = %#v", store.input.Statuses)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.SystemAccountID != "sys_user" ||
		got.SystemAccountName != "用户" ||
		got.OwnerSystemAccountID != "sys_user" ||
		got.OwnerSystemAccountName != "用户" ||
		got.ProviderProtocolProfileID != "profile_openai_openai_v1" ||
		got.ProtocolCode != "openai" ||
		got.ProtocolVersion != "v1" ||
		got.AccessType != "owner" ||
		got.AccountExpiresAt != expiresAt.Format(time.RFC3339Nano) {
		t.Fatalf("option = %+v", got)
	}
	if got.Permissions != ownerPermissions() {
		t.Fatalf("permissions = %+v", got.Permissions)
	}
}

func TestServiceOptionsDefaultsAndUnsupportedTags(t *testing.T) {
	store := &accountOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{ProviderCode: "all", Type: "all", Schedulable: "bad"}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 || store.input.Offset != 0 || store.input.ProviderCode != "" || store.input.Type != "" || store.input.Schedulable != "" {
		t.Fatalf("default store input = %+v", store.input)
	}

	store.called = false
	options, err := service.Options(context.Background(), OptionListInput{TagIDs: []string{"tag_a"}})
	if err != nil {
		t.Fatalf("Options(tagIds) error = %v", err)
	}
	if len(options) != 0 {
		t.Fatalf("tag options = %+v, want empty owner-only result", options)
	}
	if store.called {
		t.Fatal("store should not be called while account tag schema is not migrated")
	}
}

func TestOptionPageClampsToWindow(t *testing.T) {
	if got := optionPage(999, 50); got != 20 {
		t.Fatalf("optionPage() = %d, want 20", got)
	}
}

type accountOptionStoreStub struct {
	called  bool
	input   port.ManagementAccountOptionListInput
	options []port.ManagementAccountOption
	err     error
}

func (s *accountOptionStoreStub) ListManagementAccountOptions(_ context.Context, input port.ManagementAccountOptionListInput) ([]port.ManagementAccountOption, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}

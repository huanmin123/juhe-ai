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
		TagIDs:                     []string{" tag_a ", "tag_a", "", "tag_b"},
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
	if len(store.input.TagIDs) != 2 || store.input.TagIDs[0] != "tag_a" || store.input.TagIDs[1] != "tag_b" {
		t.Fatalf("store tag ids = %#v", store.input.TagIDs)
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

func TestServiceOptionsDefaults(t *testing.T) {
	store := &accountOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{ProviderCode: "all", Type: "all", Schedulable: "bad"}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 || store.input.Offset != 0 || store.input.ProviderCode != "" || store.input.Type != "" || store.input.Schedulable != "" {
		t.Fatalf("default store input = %+v", store.input)
	}
}

func TestServiceTagsNormalizesInputAndMapsTags(t *testing.T) {
	createdAt := time.Date(2026, 7, 7, 8, 0, 0, 123, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	store := &accountOptionStoreStub{
		tags: []port.ManagementAccountTag{
			{ID: "tag_main", Name: "主力", AccountCount: 2, CreatedAt: createdAt, UpdatedAt: updatedAt},
			{ID: "tag_negative", Name: "异常", AccountCount: -1, CreatedAt: createdAt, UpdatedAt: updatedAt},
		},
	}
	service := NewService(store)

	tags, err := service.Tags(context.Background(), TagListInput{SystemAccountID: " sys_user "})
	if err != nil {
		t.Fatalf("Tags() error = %v", err)
	}
	if store.tagInput.SystemAccountID != "sys_user" {
		t.Fatalf("tag input = %+v", store.tagInput)
	}
	if len(tags) != 2 {
		t.Fatalf("tags = %+v", tags)
	}
	if tags[0].ID != "tag_main" || tags[0].Name != "主力" || tags[0].AccountCount != 2 ||
		tags[0].CreatedAt != createdAt.Format(time.RFC3339Nano) ||
		tags[0].UpdatedAt != updatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("tag[0] = %+v", tags[0])
	}
	if tags[1].AccountCount != 0 {
		t.Fatalf("negative account count should be clamped: %+v", tags[1])
	}
}

func TestOptionPageClampsToWindow(t *testing.T) {
	if got := optionPage(999, 50); got != 20 {
		t.Fatalf("optionPage() = %d, want 20", got)
	}
}

type accountOptionStoreStub struct {
	called   bool
	input    port.ManagementAccountOptionListInput
	options  []port.ManagementAccountOption
	tagInput port.ManagementAccountTagListInput
	tags     []port.ManagementAccountTag
	err      error
}

func (s *accountOptionStoreStub) ListManagementAccountOptions(_ context.Context, input port.ManagementAccountOptionListInput) ([]port.ManagementAccountOption, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}

func (s *accountOptionStoreStub) ListManagementAccountTags(_ context.Context, input port.ManagementAccountTagListInput) ([]port.ManagementAccountTag, error) {
	s.tagInput = input
	return s.tags, s.err
}

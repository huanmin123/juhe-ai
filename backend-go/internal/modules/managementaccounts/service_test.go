package managementaccounts

import (
	"context"
	"errors"
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

func TestServiceOptionsMapsAuthorizedOptions(t *testing.T) {
	expiresAt := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	store := &accountOptionStoreStub{
		options: []port.ManagementAccountOption{
			{
				ID:                                   "acct_authorized",
				OwnerSystemAccountID:                 "sys_owner",
				OwnerSystemAccountName:               "Owner",
				ProviderCode:                         "openai",
				ProviderProtocolProfileID:            "profile_openai_openai_v1",
				ProtocolCode:                         "openai",
				ProtocolVersion:                      "v1",
				Name:                                 "授权账户",
				Type:                                 "api_key",
				Status:                               "active",
				AccessType:                           "authorized",
				AccountAuthorizationID:               "auth_account",
				AuthorizationStatus:                  "active",
				AuthorizationExpiresAt:               &expiresAt,
				AuthorizationInstanceSourceAccountID: "acct_source",
				AuthorizationInstanceOwnerSystemAccountID: "sys_owner",
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{SystemAccountID: "sys_viewer"})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %+v", options)
	}
	got := options[0]
	if got.SystemAccountID != "" ||
		got.SystemAccountName != "" ||
		got.OwnerSystemAccountID != "sys_owner" ||
		got.OwnerSystemAccountName != "Owner" ||
		got.AccessType != "authorized" ||
		got.AccountAuthorizationID != "auth_account" ||
		got.AuthorizationStatus != "active" ||
		got.AuthorizationExpiresAt != expiresAt.Format(time.RFC3339Nano) ||
		got.AuthorizationInstanceSourceAccountID != "acct_source" ||
		got.AuthorizationInstanceOwnerSystemAccountID != "sys_owner" {
		t.Fatalf("authorized option = %+v", got)
	}
	if got.Permissions.CanEdit ||
		got.Permissions.CanDelete ||
		got.Permissions.CanAuthorize ||
		got.Permissions.CanViewCredentials ||
		got.Permissions.CanManageAccounts ||
		got.Permissions.CanBindToAPIKey ||
		!got.Permissions.CanUse {
		t.Fatalf("authorized permissions = %+v", got.Permissions)
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

func TestServiceDeleteTagNormalizesInput(t *testing.T) {
	store := &accountOptionStoreStub{deleteResult: true}
	service := NewService(store)

	deleted, err := service.DeleteTag(context.Background(), TagDeleteInput{ID: " tag_main ", SystemAccountID: " sys_user "})
	if err != nil {
		t.Fatalf("DeleteTag() error = %v", err)
	}
	if !deleted {
		t.Fatal("DeleteTag() deleted = false, want true")
	}
	if store.deleteInput.TagID != "tag_main" || store.deleteInput.SystemAccountID != "sys_user" {
		t.Fatalf("delete input = %+v", store.deleteInput)
	}
}

func TestServiceDeleteTagMapsInUseError(t *testing.T) {
	store := &accountOptionStoreStub{deleteErr: port.ErrManagementAccountTagInUse}
	service := NewService(store)

	deleted, err := service.DeleteTag(context.Background(), TagDeleteInput{ID: "tag_main", SystemAccountID: "sys_user"})
	if !errors.Is(err, ErrAccountTagInUse) {
		t.Fatalf("DeleteTag() error = %v, want ErrAccountTagInUse", err)
	}
	if deleted {
		t.Fatal("DeleteTag() deleted = true, want false on in-use error")
	}
}

func TestServiceUpdateTagsNormalizesInputAndMapsSummary(t *testing.T) {
	expiresAt := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &accountOptionStoreStub{
		updateSummary: port.ManagementAccountSummary{
			ID:                        "acct_main",
			SystemAccountID:           "sys_user",
			SystemAccountName:         "用户",
			OwnerSystemAccountID:      "sys_user",
			OwnerSystemAccountName:    "用户",
			ProviderCode:              "gpt",
			ProviderProtocolProfileID: "profile_gpt_openai_v1",
			ProtocolCode:              "openai",
			ProtocolVersion:           "v1",
			Name:                      "主账号",
			Notes:                     "备注",
			Type:                      "api_key",
			Status:                    "active",
			ConcurrencyLimit:          20,
			Priority:                  3,
			SuperPriorityEnabled:      true,
			ClientCompatibility:       "openai_standard",
			Schedulable:               true,
			AvailabilityScheduleJSON:  `{"enabled":true}`,
			AccountExpiresAt:          &expiresAt,
			BoundGroupID:              "group_main",
			BoundGroupName:            "默认分组",
			AccessType:                "owner",
			Tags:                      []port.ManagementAccountTag{{ID: "tag_main", Name: "主力", CreatedAt: createdAt, UpdatedAt: createdAt}},
		},
		updateOK: true,
	}
	service := NewService(store)

	summary, err := service.UpdateTags(context.Background(), TagUpdateInput{
		AccountID:       " acct_main ",
		SystemAccountID: " sys_user ",
		Tags:            []string{" 主力 ", "主力", "API\t标签", "", " api "},
	})
	if err != nil {
		t.Fatalf("UpdateTags() error = %v", err)
	}

	if store.updateInput.AccountID != "acct_main" || store.updateInput.SystemAccountID != "sys_user" {
		t.Fatalf("update input = %+v", store.updateInput)
	}
	if len(store.updateInput.Tags) != 3 ||
		store.updateInput.Tags[0].Name != "主力" ||
		store.updateInput.Tags[1].Name != "API 标签" ||
		store.updateInput.Tags[2].Name != "api" ||
		store.updateInput.Tags[0].ID == "" ||
		store.updateInput.Tags[0].ID[:7] != "acctag_" {
		t.Fatalf("normalized tags = %+v", store.updateInput.Tags)
	}
	if summary.ID != "acct_main" ||
		summary.Credentials == nil ||
		summary.SystemAccountID != "sys_user" ||
		summary.BoundGroupID != "group_main" ||
		summary.AccountExpiresAt != expiresAt.Format(time.RFC3339Nano) ||
		len(summary.Tags) != 1 ||
		summary.Tags[0].Name != "主力" ||
		summary.TodayUsage.TotalTokens != 0 ||
		summary.Permissions != ownerPermissions() {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.AvailabilitySchedule == nil {
		t.Fatalf("availability schedule should be decoded: %+v", summary)
	}
}

func TestServiceUpdateTagsRejectsInvalidInput(t *testing.T) {
	t.Run("too many", func(t *testing.T) {
		values := make([]string, 25)
		for index := range values {
			values[index] = "标签" + string(rune('A'+index))
		}
		_, err := NewService(&accountOptionStoreStub{}).UpdateTags(context.Background(), TagUpdateInput{AccountID: "acct", SystemAccountID: "sys", Tags: values})
		message, ok := ValidationMessage(err)
		if !ok || message != "单个账户最多配置 24 个标签" {
			t.Fatalf("validation = %q, %v; err = %v", message, ok, err)
		}
	})
	t.Run("too long", func(t *testing.T) {
		_, err := NewService(&accountOptionStoreStub{}).UpdateTags(context.Background(), TagUpdateInput{
			AccountID:       "acct",
			SystemAccountID: "sys",
			Tags:            []string{"一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一"},
		})
		message, ok := ValidationMessage(err)
		if !ok || message != "账户标签不能超过 40 个字符" {
			t.Fatalf("validation = %q, %v; err = %v", message, ok, err)
		}
	})
}

func TestServiceUpdateTagsMapsNotFound(t *testing.T) {
	_, err := NewService(&accountOptionStoreStub{}).UpdateTags(context.Background(), TagUpdateInput{
		AccountID:       "missing",
		SystemAccountID: "sys_user",
		Tags:            []string{"主力"},
	})
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("UpdateTags() error = %v, want ErrAccountNotFound", err)
	}
}

func TestOptionPageClampsToWindow(t *testing.T) {
	if got := optionPage(999, 50); got != 20 {
		t.Fatalf("optionPage() = %d, want 20", got)
	}
}

type accountOptionStoreStub struct {
	called        bool
	input         port.ManagementAccountOptionListInput
	options       []port.ManagementAccountOption
	tagInput      port.ManagementAccountTagListInput
	tags          []port.ManagementAccountTag
	deleteInput   port.ManagementAccountTagDeleteInput
	deleteResult  bool
	deleteErr     error
	updateInput   port.ManagementAccountTagUpdateInput
	updateSummary port.ManagementAccountSummary
	updateOK      bool
	updateErr     error
	err           error
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

func (s *accountOptionStoreStub) DeleteManagementAccountTag(_ context.Context, input port.ManagementAccountTagDeleteInput) (bool, error) {
	s.deleteInput = input
	return s.deleteResult, s.deleteErr
}

func (s *accountOptionStoreStub) UpdateManagementAccountTags(_ context.Context, input port.ManagementAccountTagUpdateInput) (port.ManagementAccountSummary, bool, error) {
	s.updateInput = input
	return s.updateSummary, s.updateOK, s.updateErr
}

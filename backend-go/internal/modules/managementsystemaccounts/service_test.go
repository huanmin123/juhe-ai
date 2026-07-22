package managementsystemaccounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

func TestListNormalizesInputAndMapsSummaries(t *testing.T) {
	createdAt := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	lastLoginAt := createdAt.Add(time.Minute)
	store := &systemAccountOptionStoreStub{
		listResult: port.ManagementSystemAccountListResult{
			Items: []port.ManagementSystemAccountSummary{{
				ID:                     "sys_admin",
				Username:               "admin",
				DisplayName:            "管理员",
				Description:            "系统管理员",
				Role:                   "admin",
				Status:                 "active",
				MustChangePassword:     true,
				ImageGenerationEnabled: true,
				LastLoginAt:            &lastLoginAt,
				CreatedAt:              createdAt,
				UpdatedAt:              createdAt.Add(time.Hour),
			}},
			HasMore: true,
		},
	}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{
		Keyword:  " 管理 ",
		Page:     2,
		PageSize: 500,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Keyword != "管理" || store.listInput.Limit != 101 || store.listInput.Offset != 100 {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if result.Page != 2 || result.PageSize != 100 || result.Total != 102 || !result.HasMore {
		t.Fatalf("pagination = page %d size %d total %d hasMore %v", result.Page, result.PageSize, result.Total, result.HasMore)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %+v", result.Items)
	}
	got := result.Items[0]
	if got.ID != "sys_admin" ||
		got.Description != "系统管理员" ||
		got.Role != "admin" ||
		got.MustChangePassword ||
		!got.ImageGenerationEnabled ||
		got.LastLoginAt != lastLoginAt.Format(time.RFC3339Nano) ||
		got.CreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("summary = %+v", got)
	}
}

func TestListDefaultsAndClampsPageToWindow(t *testing.T) {
	store := &systemAccountOptionStoreStub{}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{Page: 999, PageSize: -1})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Limit != 21 || store.listInput.Offset != 980 {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if result.Page != 50 || result.PageSize != 20 {
		t.Fatalf("pagination = page %d size %d", result.Page, result.PageSize)
	}
}

func TestListReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&systemAccountOptionStoreStub{listErr: want})

	_, err := service.List(context.Background(), ListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("List() error = %v, want %v", err, want)
	}
}

func TestOptionsNormalizesInputAndMapsOptions(t *testing.T) {
	store := &systemAccountOptionStoreStub{
		options: []port.ManagementSystemAccountOption{{
			ID:          "sys_user",
			Username:    "user",
			DisplayName: "用户",
			Status:      "active",
		}},
	}
	service := NewService(store)

	got, err := service.Options(context.Background(), OptionListInput{
		IDs:     []string{" sys_user ", "sys_user", "", "sys_disabled"},
		Keyword: "  用户  ",
		Limit:   500,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Keyword != "用户" || store.input.Limit != 50 {
		t.Fatalf("store input = %+v, want trimmed keyword and limit 50", store.input)
	}
	if len(store.input.IDs) != 2 || store.input.IDs[0] != "sys_user" || store.input.IDs[1] != "sys_disabled" {
		t.Fatalf("ids = %#v", store.input.IDs)
	}
	if len(got) != 1 || got[0].ID != "sys_user" || got[0].Username != "user" || got[0].DisplayName != "用户" || got[0].Status != "active" {
		t.Fatalf("Options() = %+v", got)
	}
}

func TestOptionsDefaultsLimit(t *testing.T) {
	store := &systemAccountOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{Limit: -10}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 {
		t.Fatalf("limit = %d, want 50", store.input.Limit)
	}
}

func TestOptionsReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&systemAccountOptionStoreStub{err: want})

	_, err := service.Options(context.Background(), OptionListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("Options() error = %v, want %v", err, want)
	}
}

func TestResetPasswordHashesPasswordAndMapsResult(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	updatedAt := now.Add(time.Minute)
	mustChangePassword := true
	store := &systemAccountOptionStoreStub{
		resetFound: true,
		resetResult: port.ManagementSystemAccountPasswordResetResult{
			Before: port.ManagementSystemAccountSummary{
				ID:                 "sys_user",
				Username:           "user",
				DisplayName:        "用户",
				Role:               "user",
				Status:             "active",
				MustChangePassword: false,
				CreatedAt:          now,
				UpdatedAt:          now,
			},
			Account: port.ManagementSystemAccountSummary{
				ID:                 "sys_user",
				Username:           "user",
				DisplayName:        "用户",
				Role:               "user",
				Status:             "active",
				MustChangePassword: true,
				CreatedAt:          now,
				UpdatedAt:          updatedAt,
			},
			RevokedSessionCount: 2,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return updatedAt },
		HashPassword: func(password string) (string, error) {
			if password != "NewPass123" {
				t.Fatalf("hash password input = %q", password)
			}
			return "hashed-password", nil
		},
	})

	result, err := service.ResetPassword(context.Background(), PasswordResetInput{
		SystemAccountID:    " sys_user ",
		Password:           "NewPass123",
		MustChangePassword: &mustChangePassword,
	})

	if err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if store.resetInput.SystemAccountID != "sys_user" ||
		store.resetInput.PasswordHash != "hashed-password" ||
		!store.resetInput.HasMustChangePassword ||
		!store.resetInput.MustChangePassword ||
		!store.resetInput.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("reset input = %+v", store.resetInput)
	}
	if result.Account.ID != "sys_user" ||
		!result.Account.MustChangePassword ||
		result.Account.UpdatedAt != updatedAt.Format(time.RFC3339Nano) ||
		result.RevokedSessionCount != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestResetPasswordNormalizesAdminMustChangePassword(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := &systemAccountOptionStoreStub{
		resetFound: true,
		resetResult: port.ManagementSystemAccountPasswordResetResult{
			Before: port.ManagementSystemAccountSummary{ID: "sys_admin", Role: "admin", Status: "active", MustChangePassword: true, CreatedAt: now, UpdatedAt: now},
			Account: port.ManagementSystemAccountSummary{
				ID:                 "sys_admin",
				Username:           "admin",
				DisplayName:        "管理员",
				Role:               "admin",
				Status:             "active",
				MustChangePassword: true,
				CreatedAt:          now,
				UpdatedAt:          now,
			},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:        store,
		HashPassword: func(string) (string, error) { return "hash", nil },
	})

	result, err := service.ResetPassword(context.Background(), PasswordResetInput{SystemAccountID: "sys_admin", Password: "NewPass123"})

	if err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if result.Account.MustChangePassword || result.Before.MustChangePassword {
		t.Fatalf("admin mustChangePassword should be normalized to false, result = %+v", result)
	}
}

func TestResetPasswordRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		input    PasswordResetInput
		wantErr  error
		wantCall bool
	}{
		{name: "missing id", input: PasswordResetInput{Password: "NewPass123"}, wantErr: ErrPasswordResetInvalid},
		{name: "short password", input: PasswordResetInput{SystemAccountID: "sys_user", Password: "abc"}, wantErr: ErrPasswordResetInvalid},
		{name: "password whitespace", input: PasswordResetInput{SystemAccountID: "sys_user", Password: "New Pass123"}, wantErr: ErrPasswordResetWhitespace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &systemAccountOptionStoreStub{}
			service := NewServiceWithOptions(ServiceOptions{
				Store:        store,
				HashPassword: func(string) (string, error) { return "hash", nil },
			})

			_, err := service.ResetPassword(context.Background(), tt.input)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ResetPassword() error = %v, want %v", err, tt.wantErr)
			}
			if store.resetCalled != tt.wantCall {
				t.Fatalf("reset called = %v, want %v", store.resetCalled, tt.wantCall)
			}
		})
	}
}

func TestResetPasswordMapsStoreNotFoundAndErrors(t *testing.T) {
	want := errors.New("postgres down")
	tests := []struct {
		name    string
		store   *systemAccountOptionStoreStub
		wantErr error
	}{
		{name: "not found", store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountNotFound},
		{name: "store error", store: &systemAccountOptionStoreStub{resetErr: want}, wantErr: want},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher := &systemAccountPageDataPublisherStub{}
			service := NewServiceWithOptions(ServiceOptions{
				Store:             tt.store,
				HashPassword:      func(string) (string, error) { return "hash", nil },
				PageDataPublisher: publisher,
			})

			_, err := service.ResetPassword(context.Background(), PasswordResetInput{SystemAccountID: "sys_user", Password: "NewPass123"})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ResetPassword() error = %v, want %v", err, tt.wantErr)
			}
			if len(publisher.domains) != 0 {
				t.Fatalf("page data domains = %#v, want none", publisher.domains)
			}
		})
	}
}

func TestUpdateStatusNormalizesInputAndMapsResult(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	updatedAt := now.Add(time.Minute)
	store := &systemAccountOptionStoreStub{
		statusFound: true,
		statusResult: port.ManagementSystemAccountStatusUpdateResult{
			Before: port.ManagementSystemAccountSummary{
				ID:        "sys_user",
				Username:  "user",
				Role:      "user",
				Status:    "active",
				CreatedAt: now,
				UpdatedAt: now,
			},
			Account: port.ManagementSystemAccountSummary{
				ID:        "sys_user",
				Username:  "user",
				Role:      "user",
				Status:    "disabled",
				CreatedAt: now,
				UpdatedAt: updatedAt,
			},
			RevokedSessionCount: 2,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return updatedAt },
	})

	result, err := service.UpdateStatus(context.Background(), StatusUpdateInput{SystemAccountID: " sys_user ", Status: "disabled"})

	if err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if !store.statusCalled ||
		store.statusInput.SystemAccountID != "sys_user" ||
		store.statusInput.Status != "disabled" ||
		!store.statusInput.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("status input = %+v", store.statusInput)
	}
	if result.Before.Status != "active" ||
		result.Account.Status != "disabled" ||
		result.Account.UpdatedAt != updatedAt.Format(time.RFC3339Nano) ||
		result.RevokedSessionCount != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestUpdateStatusInvalidatesGatewayCacheWhenStatusChanges(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	invalidator := &systemAccountInvalidatorStub{}
	store := &systemAccountOptionStoreStub{
		statusFound: true,
		statusResult: port.ManagementSystemAccountStatusUpdateResult{
			Before:  port.ManagementSystemAccountSummary{ID: "sys_user", Status: "active", CreatedAt: now, UpdatedAt: now},
			Account: port.ManagementSystemAccountSummary{ID: "sys_user", Status: "disabled", CreatedAt: now, UpdatedAt: now},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		SystemAccountInvalidator: invalidator,
	})

	if _, err := service.UpdateStatus(context.Background(), StatusUpdateInput{SystemAccountID: " sys_user ", Status: "disabled"}); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}

	if invalidator.statusCalls != 1 || invalidator.statusIDs[0] != "sys_user" {
		t.Fatalf("status invalidation = %d / %#v, want sys_user", invalidator.statusCalls, invalidator.statusIDs)
	}
	if invalidator.imageCalls != 0 {
		t.Fatalf("image invalidation calls = %d, want 0", invalidator.imageCalls)
	}
}

func TestUpdateStatusSkipsGatewayCacheInvalidationWhenStatusUnchanged(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	invalidator := &systemAccountInvalidatorStub{}
	store := &systemAccountOptionStoreStub{
		statusFound: true,
		statusResult: port.ManagementSystemAccountStatusUpdateResult{
			Before:  port.ManagementSystemAccountSummary{ID: "sys_user", Status: "active", CreatedAt: now, UpdatedAt: now},
			Account: port.ManagementSystemAccountSummary{ID: "sys_user", Status: "active", CreatedAt: now, UpdatedAt: now},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		SystemAccountInvalidator: invalidator,
	})

	if _, err := service.UpdateStatus(context.Background(), StatusUpdateInput{SystemAccountID: "sys_user", Status: "active"}); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}

	if invalidator.statusCalls != 0 || invalidator.imageCalls != 0 {
		t.Fatalf("invalidation calls = status %d image %d, want 0", invalidator.statusCalls, invalidator.imageCalls)
	}
}

func TestUpdateStatusReturnsGatewayCacheInvalidationError(t *testing.T) {
	want := errors.New("redis down")
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := &systemAccountOptionStoreStub{
		statusFound: true,
		statusResult: port.ManagementSystemAccountStatusUpdateResult{
			Before:  port.ManagementSystemAccountSummary{ID: "sys_user", Status: "active", CreatedAt: now, UpdatedAt: now},
			Account: port.ManagementSystemAccountSummary{ID: "sys_user", Status: "disabled", CreatedAt: now, UpdatedAt: now},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		SystemAccountInvalidator: &systemAccountInvalidatorStub{statusErr: want},
	})

	_, err := service.UpdateStatus(context.Background(), StatusUpdateInput{SystemAccountID: "sys_user", Status: "disabled"})

	if !errors.Is(err, want) {
		t.Fatalf("UpdateStatus() error = %v, want %v", err, want)
	}
}

func TestUpdateStatusRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input StatusUpdateInput
	}{
		{name: "missing id", input: StatusUpdateInput{Status: "disabled"}},
		{name: "invalid status", input: StatusUpdateInput{SystemAccountID: "sys_user", Status: "archived"}},
		{name: "empty status", input: StatusUpdateInput{SystemAccountID: "sys_user"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &systemAccountOptionStoreStub{}
			service := NewService(store)

			_, err := service.UpdateStatus(context.Background(), tt.input)

			if !errors.Is(err, ErrStatusUpdateInvalid) {
				t.Fatalf("UpdateStatus() error = %v, want %v", err, ErrStatusUpdateInvalid)
			}
			if store.statusCalled {
				t.Fatal("store should not be called for invalid status update input")
			}
		})
	}
}

func TestUpdateStatusMapsStoreNotFoundBlockedAndErrors(t *testing.T) {
	want := errors.New("postgres down")
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		store   *systemAccountOptionStoreStub
		wantErr error
	}{
		{name: "not found", store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountNotFound},
		{
			name: "last active super admin",
			store: &systemAccountOptionStoreStub{
				statusFound: true,
				statusResult: port.ManagementSystemAccountStatusUpdateResult{
					Before:                      port.ManagementSystemAccountSummary{ID: "sys_super", Role: "super_admin", Status: "active", CreatedAt: now, UpdatedAt: now},
					Account:                     port.ManagementSystemAccountSummary{ID: "sys_super", Role: "super_admin", Status: "active", CreatedAt: now, UpdatedAt: now},
					BlockedLastActiveSuperAdmin: true,
				},
			},
			wantErr: ErrActiveSuperAdminRequired,
		},
		{name: "store error", store: &systemAccountOptionStoreStub{statusErr: want}, wantErr: want},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewService(tt.store)

			_, err := service.UpdateStatus(context.Background(), StatusUpdateInput{SystemAccountID: "sys_user", Status: "disabled"})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UpdateStatus() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateStatusSkipsGatewayCacheInvalidationForBlockedAndStoreErrors(t *testing.T) {
	want := errors.New("postgres down")
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		store *systemAccountOptionStoreStub
	}{
		{name: "not found", store: &systemAccountOptionStoreStub{}},
		{name: "store error", store: &systemAccountOptionStoreStub{statusErr: want}},
		{
			name: "last active super admin",
			store: &systemAccountOptionStoreStub{
				statusFound: true,
				statusResult: port.ManagementSystemAccountStatusUpdateResult{
					Before:                      port.ManagementSystemAccountSummary{ID: "sys_super", Role: "super_admin", Status: "active", CreatedAt: now, UpdatedAt: now},
					Account:                     port.ManagementSystemAccountSummary{ID: "sys_super", Role: "super_admin", Status: "disabled", CreatedAt: now, UpdatedAt: now},
					BlockedLastActiveSuperAdmin: true,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalidator := &systemAccountInvalidatorStub{}
			service := NewServiceWithOptions(ServiceOptions{
				Store:                    tt.store,
				SystemAccountInvalidator: invalidator,
			})

			_, _ = service.UpdateStatus(context.Background(), StatusUpdateInput{SystemAccountID: "sys_user", Status: "disabled"})

			if invalidator.statusCalls != 0 || invalidator.imageCalls != 0 {
				t.Fatalf("invalidation calls = status %d image %d, want 0", invalidator.statusCalls, invalidator.imageCalls)
			}
		})
	}
}

func TestUpdateImageGenerationNormalizesInputMapsResultAndInvalidatesGatewayCache(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	updatedAt := now.Add(time.Minute)
	invalidator := &systemAccountInvalidatorStub{}
	store := &systemAccountOptionStoreStub{
		imageFound: true,
		imageResult: port.ManagementSystemAccountImageGenerationUpdateResult{
			Before: port.ManagementSystemAccountSummary{
				ID:                     "sys_user",
				Username:               "user",
				DisplayName:            "用户",
				Status:                 "active",
				ImageGenerationEnabled: false,
				CreatedAt:              now,
				UpdatedAt:              now,
			},
			Account: port.ManagementSystemAccountSummary{
				ID:                     "sys_user",
				Username:               "user",
				DisplayName:            "用户",
				Status:                 "active",
				ImageGenerationEnabled: true,
				CreatedAt:              now,
				UpdatedAt:              updatedAt,
			},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return updatedAt },
		SystemAccountInvalidator: invalidator,
	})

	result, err := service.UpdateImageGeneration(context.Background(), ImageGenerationUpdateInput{SystemAccountID: " sys_user ", ImageGenerationEnabled: true})

	if err != nil {
		t.Fatalf("UpdateImageGeneration() error = %v", err)
	}
	if !store.imageCalled ||
		store.imageInput.SystemAccountID != "sys_user" ||
		!store.imageInput.ImageGenerationEnabled ||
		!store.imageInput.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("image input = %+v", store.imageInput)
	}
	if !result.Changed ||
		!result.Account.ImageGenerationEnabled ||
		result.Account.UpdatedAt != updatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.imageCalls != 1 || invalidator.imageIDs[0] != "sys_user" {
		t.Fatalf("image invalidation = %d / %#v, want sys_user", invalidator.imageCalls, invalidator.imageIDs)
	}
	if invalidator.statusCalls != 0 {
		t.Fatalf("status invalidation calls = %d, want 0", invalidator.statusCalls)
	}
}

func TestUpdateImageGenerationSkipsGatewayCacheInvalidationWhenUnchanged(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	invalidator := &systemAccountInvalidatorStub{}
	store := &systemAccountOptionStoreStub{
		imageFound: true,
		imageResult: port.ManagementSystemAccountImageGenerationUpdateResult{
			Before:  port.ManagementSystemAccountSummary{ID: "sys_user", ImageGenerationEnabled: true, CreatedAt: now, UpdatedAt: now},
			Account: port.ManagementSystemAccountSummary{ID: "sys_user", ImageGenerationEnabled: true, CreatedAt: now, UpdatedAt: now},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		SystemAccountInvalidator: invalidator,
	})

	result, err := service.UpdateImageGeneration(context.Background(), ImageGenerationUpdateInput{SystemAccountID: "sys_user", ImageGenerationEnabled: true})

	if err != nil {
		t.Fatalf("UpdateImageGeneration() error = %v", err)
	}
	if result.Changed {
		t.Fatal("Changed = true, want false")
	}
	if invalidator.imageCalls != 0 || invalidator.statusCalls != 0 {
		t.Fatalf("invalidation calls = image %d status %d, want 0", invalidator.imageCalls, invalidator.statusCalls)
	}
}

func TestUpdateImageGenerationRejectsInvalidInputAndMapsErrors(t *testing.T) {
	want := errors.New("postgres down")
	tests := []struct {
		name    string
		input   ImageGenerationUpdateInput
		store   *systemAccountOptionStoreStub
		wantErr error
	}{
		{name: "missing id", input: ImageGenerationUpdateInput{}, store: &systemAccountOptionStoreStub{}, wantErr: ErrImageGenerationUpdateInvalid},
		{name: "not found", input: ImageGenerationUpdateInput{SystemAccountID: "sys_user"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountNotFound},
		{name: "store error", input: ImageGenerationUpdateInput{SystemAccountID: "sys_user"}, store: &systemAccountOptionStoreStub{imageErr: want}, wantErr: want},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalidator := &systemAccountInvalidatorStub{}
			publisher := &systemAccountPageDataPublisherStub{}
			service := NewServiceWithOptions(ServiceOptions{
				Store:                    tt.store,
				SystemAccountInvalidator: invalidator,
				PageDataPublisher:        publisher,
			})

			_, err := service.UpdateImageGeneration(context.Background(), tt.input)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UpdateImageGeneration() error = %v, want %v", err, tt.wantErr)
			}
			if invalidator.imageCalls != 0 || invalidator.statusCalls != 0 {
				t.Fatalf("invalidation calls = image %d status %d, want 0", invalidator.imageCalls, invalidator.statusCalls)
			}
			if len(publisher.domains) != 0 {
				t.Fatalf("page data domains = %#v, want none", publisher.domains)
			}
		})
	}
}

func TestUpdateImageGenerationReturnsGatewayCacheInvalidationError(t *testing.T) {
	want := errors.New("redis down")
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := &systemAccountOptionStoreStub{
		imageFound: true,
		imageResult: port.ManagementSystemAccountImageGenerationUpdateResult{
			Before:  port.ManagementSystemAccountSummary{ID: "sys_user", ImageGenerationEnabled: false, CreatedAt: now, UpdatedAt: now},
			Account: port.ManagementSystemAccountSummary{ID: "sys_user", ImageGenerationEnabled: true, CreatedAt: now, UpdatedAt: now},
		},
	}
	publisher := &systemAccountPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		SystemAccountInvalidator: &systemAccountInvalidatorStub{imageErr: want},
		PageDataPublisher:        publisher,
	})

	_, err := service.UpdateImageGeneration(context.Background(), ImageGenerationUpdateInput{SystemAccountID: "sys_user", ImageGenerationEnabled: true})

	if !errors.Is(err, want) {
		t.Fatalf("UpdateImageGeneration() error = %v, want %v", err, want)
	}
	assertSystemAccountPageDataResets(t, publisher)
}

func TestUpdateProfileNormalizesInputAndMapsResult(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	updatedAt := now.Add(time.Minute)
	displayName := "新名称"
	description := "  新说明  "
	role := "admin"
	mustChangePassword := true
	store := &systemAccountOptionStoreStub{
		profileFound: true,
		profileResult: port.ManagementSystemAccountProfileUpdateResult{
			Before: port.ManagementSystemAccountSummary{
				ID:                 "sys_user",
				Username:           "user",
				DisplayName:        "旧名称",
				Description:        "旧说明",
				Role:               "user",
				Status:             "active",
				MustChangePassword: false,
				CreatedAt:          now,
				UpdatedAt:          now,
			},
			Account: port.ManagementSystemAccountSummary{
				ID:                 "sys_user",
				Username:           "user",
				DisplayName:        "新名称",
				Description:        "新说明",
				Role:               "admin",
				Status:             "active",
				MustChangePassword: true,
				CreatedAt:          now,
				UpdatedAt:          updatedAt,
			},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return updatedAt },
	})

	result, err := service.UpdateProfile(context.Background(), ProfileUpdateInput{
		SystemAccountID:    " sys_user ",
		DisplayName:        &displayName,
		HasDescription:     true,
		Description:        &description,
		Role:               &role,
		MustChangePassword: &mustChangePassword,
	})

	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if !store.profileCalled ||
		store.profileInput.SystemAccountID != "sys_user" ||
		!store.profileInput.HasDisplayName ||
		store.profileInput.DisplayName != "新名称" ||
		!store.profileInput.HasDescription ||
		store.profileInput.Description == nil ||
		*store.profileInput.Description != "新说明" ||
		!store.profileInput.HasRole ||
		store.profileInput.Role != "admin" ||
		!store.profileInput.HasMustChangePassword ||
		!store.profileInput.MustChangePassword ||
		!store.profileInput.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("profile input = %+v", store.profileInput)
	}
	if !result.Changed ||
		result.Before.DisplayName != "旧名称" ||
		result.Account.DisplayName != "新名称" ||
		result.Account.Description != "新说明" ||
		result.Account.Role != "admin" ||
		result.Account.MustChangePassword {
		t.Fatalf("result = %+v", result)
	}
}

func TestUpdateProfileAllowsDescriptionNull(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := &systemAccountOptionStoreStub{
		profileFound: true,
		profileResult: port.ManagementSystemAccountProfileUpdateResult{
			Before:  port.ManagementSystemAccountSummary{ID: "sys_user", Username: "user", DisplayName: "用户", Description: "旧说明", Role: "user", Status: "active", CreatedAt: now, UpdatedAt: now},
			Account: port.ManagementSystemAccountSummary{ID: "sys_user", Username: "user", DisplayName: "用户", Role: "user", Status: "active", CreatedAt: now, UpdatedAt: now},
		},
	}
	service := NewService(store)

	result, err := service.UpdateProfile(context.Background(), ProfileUpdateInput{SystemAccountID: "sys_user", HasDescription: true})

	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if !store.profileInput.HasDescription || store.profileInput.Description != nil {
		t.Fatalf("profile description input = %+v", store.profileInput)
	}
	if !result.Changed || result.Account.Description != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestUpdateProfileRejectsInvalidInput(t *testing.T) {
	spaceName := "bad user"
	longDescription := ""
	for i := 0; i < maxDescriptionRunes+1; i++ {
		longDescription += "a"
	}
	invalidRole := "super_admin"
	tests := []struct {
		name    string
		input   ProfileUpdateInput
		wantErr error
	}{
		{name: "missing id", input: ProfileUpdateInput{DisplayName: stringPtr("用户")}, wantErr: ErrProfileUpdateInvalid},
		{name: "no fields", input: ProfileUpdateInput{SystemAccountID: "sys_user"}, wantErr: ErrProfileUpdateInvalid},
		{name: "display name whitespace", input: ProfileUpdateInput{SystemAccountID: "sys_user", DisplayName: &spaceName}, wantErr: ErrProfileUpdateWhitespace},
		{name: "description too long", input: ProfileUpdateInput{SystemAccountID: "sys_user", HasDescription: true, Description: &longDescription}, wantErr: ErrProfileUpdateInvalid},
		{name: "invalid role", input: ProfileUpdateInput{SystemAccountID: "sys_user", Role: &invalidRole}, wantErr: ErrProfileUpdateInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &systemAccountOptionStoreStub{}
			service := NewService(store)

			_, err := service.UpdateProfile(context.Background(), tt.input)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UpdateProfile() error = %v, want %v", err, tt.wantErr)
			}
			if store.profileCalled {
				t.Fatal("store should not be called for invalid profile update input")
			}
		})
	}
}

func TestUpdateProfileMapsStoreErrors(t *testing.T) {
	want := errors.New("postgres down")
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		store   *systemAccountOptionStoreStub
		wantErr error
	}{
		{name: "not found", store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountNotFound},
		{name: "duplicate display name", store: &systemAccountOptionStoreStub{profileErr: port.ErrManagementSystemAccountDisplayNameExists}, wantErr: ErrProfileUpdateDisplayNameDup},
		{
			name: "last active super admin",
			store: &systemAccountOptionStoreStub{
				profileFound: true,
				profileResult: port.ManagementSystemAccountProfileUpdateResult{
					Before:                      port.ManagementSystemAccountSummary{ID: "sys_super", Role: "super_admin", Status: "active", CreatedAt: now, UpdatedAt: now},
					Account:                     port.ManagementSystemAccountSummary{ID: "sys_super", Role: "super_admin", Status: "active", CreatedAt: now, UpdatedAt: now},
					BlockedLastActiveSuperAdmin: true,
				},
			},
			wantErr: ErrActiveSuperAdminRequired,
		},
		{name: "store error", store: &systemAccountOptionStoreStub{profileErr: want}, wantErr: want},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher := &systemAccountPageDataPublisherStub{}
			service := NewServiceWithOptions(ServiceOptions{Store: tt.store, PageDataPublisher: publisher})

			_, err := service.UpdateProfile(context.Background(), ProfileUpdateInput{SystemAccountID: "sys_user", DisplayName: stringPtr("用户")})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UpdateProfile() error = %v, want %v", err, tt.wantErr)
			}
			if len(publisher.domains) != 0 {
				t.Fatalf("page data domains = %#v, want none", publisher.domains)
			}
		})
	}
}

func TestUpdateNormalizesMixedInputMapsResultAndInvalidatesStatusFirst(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	updatedAt := now.Add(time.Minute)
	displayName := "新名称"
	description := "  新说明  "
	password := "NewPass123"
	role := "admin"
	status := "disabled"
	mustChangePassword := true
	imageGenerationEnabled := true
	invalidator := &systemAccountInvalidatorStub{}
	store := &systemAccountOptionStoreStub{
		updateFound: true,
		updateResult: port.ManagementSystemAccountUpdateResult{
			Before: port.ManagementSystemAccountSummary{
				ID:                     "sys_user",
				Username:               "user",
				DisplayName:            "旧名称",
				Description:            "旧说明",
				Role:                   "user",
				Status:                 "active",
				MustChangePassword:     false,
				ImageGenerationEnabled: false,
				CreatedAt:              now,
				UpdatedAt:              now,
			},
			Account: port.ManagementSystemAccountSummary{
				ID:                     "sys_user",
				Username:               "user",
				DisplayName:            "新名称",
				Description:            "新说明",
				Role:                   "admin",
				Status:                 "disabled",
				MustChangePassword:     false,
				ImageGenerationEnabled: true,
				CreatedAt:              now,
				UpdatedAt:              updatedAt,
			},
			RevokedSessionCount: 2,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return updatedAt },
		SystemAccountInvalidator: invalidator,
		HashPassword: func(value string) (string, error) {
			if value != password {
				t.Fatalf("hash password input = %q", value)
			}
			return "hashed-password", nil
		},
	})

	result, err := service.Update(context.Background(), UpdateInput{
		SystemAccountID:        " sys_user ",
		DisplayName:            &displayName,
		HasDescription:         true,
		Description:            &description,
		Password:               &password,
		Role:                   &role,
		Status:                 &status,
		MustChangePassword:     &mustChangePassword,
		ImageGenerationEnabled: &imageGenerationEnabled,
	})

	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	input := store.updateInput
	if !store.updateCalled ||
		input.SystemAccountID != "sys_user" ||
		!input.HasDisplayName ||
		input.DisplayName != "新名称" ||
		!input.HasDescription ||
		input.Description == nil ||
		*input.Description != "新说明" ||
		!input.HasPassword ||
		input.PasswordHash != "hashed-password" ||
		!input.HasRole ||
		input.Role != "admin" ||
		!input.HasStatus ||
		input.Status != "disabled" ||
		!input.HasMustChangePassword ||
		!input.MustChangePassword ||
		!input.HasImageGenerationEnabled ||
		!input.ImageGenerationEnabled ||
		!input.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("update input = %+v", input)
	}
	if !result.Changed ||
		!result.PasswordChanged ||
		result.RevokedSessionCount != 2 ||
		result.Account.MustChangePassword ||
		result.Account.Status != "disabled" ||
		!result.Account.ImageGenerationEnabled {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.statusCalls != 1 || invalidator.statusIDs[0] != "sys_user" {
		t.Fatalf("status invalidation = %d / %#v", invalidator.statusCalls, invalidator.statusIDs)
	}
	if invalidator.imageCalls != 0 {
		t.Fatalf("image invalidation calls = %d, want 0", invalidator.imageCalls)
	}
}

func TestUpdateInvalidatesImageWhenStatusUnchanged(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	imageGenerationEnabled := true
	invalidator := &systemAccountInvalidatorStub{}
	store := &systemAccountOptionStoreStub{
		updateFound: true,
		updateResult: port.ManagementSystemAccountUpdateResult{
			Before:  port.ManagementSystemAccountSummary{ID: "sys_user", Status: "active", ImageGenerationEnabled: false, CreatedAt: now, UpdatedAt: now},
			Account: port.ManagementSystemAccountSummary{ID: "sys_user", Status: "active", ImageGenerationEnabled: true, CreatedAt: now, UpdatedAt: now},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		SystemAccountInvalidator: invalidator,
	})

	if _, err := service.Update(context.Background(), UpdateInput{SystemAccountID: "sys_user", ImageGenerationEnabled: &imageGenerationEnabled}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if invalidator.imageCalls != 1 || invalidator.imageIDs[0] != "sys_user" {
		t.Fatalf("image invalidation = %d / %#v", invalidator.imageCalls, invalidator.imageIDs)
	}
	if invalidator.statusCalls != 0 {
		t.Fatalf("status invalidation calls = %d, want 0", invalidator.statusCalls)
	}
}

func TestUpdateRejectsInvalidInput(t *testing.T) {
	spaceName := "bad user"
	shortPassword := "abc"
	spacePassword := "New Pass123"
	invalidRole := "super_admin"
	invalidStatus := "archived"
	tests := []struct {
		name    string
		input   UpdateInput
		wantErr error
	}{
		{name: "missing id", input: UpdateInput{DisplayName: stringPtr("用户")}, wantErr: ErrProfileUpdateInvalid},
		{name: "no fields", input: UpdateInput{SystemAccountID: "sys_user"}, wantErr: ErrProfileUpdateInvalid},
		{name: "display whitespace", input: UpdateInput{SystemAccountID: "sys_user", DisplayName: &spaceName}, wantErr: ErrProfileUpdateWhitespace},
		{name: "short password", input: UpdateInput{SystemAccountID: "sys_user", Password: &shortPassword}, wantErr: ErrPasswordResetInvalid},
		{name: "password whitespace", input: UpdateInput{SystemAccountID: "sys_user", Password: &spacePassword}, wantErr: ErrPasswordResetWhitespace},
		{name: "invalid role", input: UpdateInput{SystemAccountID: "sys_user", Role: &invalidRole}, wantErr: ErrProfileUpdateInvalid},
		{name: "invalid status", input: UpdateInput{SystemAccountID: "sys_user", Status: &invalidStatus}, wantErr: ErrProfileUpdateInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &systemAccountOptionStoreStub{}
			service := NewServiceWithOptions(ServiceOptions{
				Store:        store,
				HashPassword: func(string) (string, error) { return "hash", nil },
			})

			_, err := service.Update(context.Background(), tt.input)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, tt.wantErr)
			}
			if store.updateCalled {
				t.Fatal("store should not be called for invalid update input")
			}
		})
	}
}

func TestUpdateMapsStoreErrors(t *testing.T) {
	want := errors.New("postgres down")
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		store   *systemAccountOptionStoreStub
		wantErr error
	}{
		{name: "not found", store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountNotFound},
		{name: "duplicate display name", store: &systemAccountOptionStoreStub{updateErr: port.ErrManagementSystemAccountDisplayNameExists}, wantErr: ErrProfileUpdateDisplayNameDup},
		{
			name: "last active super admin",
			store: &systemAccountOptionStoreStub{
				updateFound: true,
				updateResult: port.ManagementSystemAccountUpdateResult{
					Before:                      port.ManagementSystemAccountSummary{ID: "sys_super", Role: "super_admin", Status: "active", CreatedAt: now, UpdatedAt: now},
					Account:                     port.ManagementSystemAccountSummary{ID: "sys_super", Role: "super_admin", Status: "active", CreatedAt: now, UpdatedAt: now},
					BlockedLastActiveSuperAdmin: true,
				},
			},
			wantErr: ErrActiveSuperAdminRequired,
		},
		{name: "store error", store: &systemAccountOptionStoreStub{updateErr: want}, wantErr: want},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher := &systemAccountPageDataPublisherStub{}
			service := NewServiceWithOptions(ServiceOptions{
				Store:             tt.store,
				HashPassword:      func(string) (string, error) { return "hash", nil },
				PageDataPublisher: publisher,
			})

			_, err := service.Update(context.Background(), UpdateInput{SystemAccountID: "sys_user", DisplayName: stringPtr("用户")})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, tt.wantErr)
			}
			if len(publisher.domains) != 0 {
				t.Fatalf("page data domains = %#v, want none", publisher.domains)
			}
		})
	}
}

type systemAccountOptionStoreStub struct {
	listInput     port.ManagementSystemAccountListInput
	listResult    port.ManagementSystemAccountListResult
	listErr       error
	input         port.ManagementSystemAccountOptionListInput
	options       []port.ManagementSystemAccountOption
	err           error
	resetCalled   bool
	resetInput    port.ManagementSystemAccountPasswordResetInput
	resetResult   port.ManagementSystemAccountPasswordResetResult
	resetFound    bool
	resetErr      error
	statusCalled  bool
	statusInput   port.ManagementSystemAccountStatusUpdateInput
	statusResult  port.ManagementSystemAccountStatusUpdateResult
	statusFound   bool
	statusErr     error
	imageCalled   bool
	imageInput    port.ManagementSystemAccountImageGenerationUpdateInput
	imageResult   port.ManagementSystemAccountImageGenerationUpdateResult
	imageFound    bool
	imageErr      error
	profileCalled bool
	profileInput  port.ManagementSystemAccountProfileUpdateInput
	profileResult port.ManagementSystemAccountProfileUpdateResult
	profileFound  bool
	profileErr    error
	updateCalled  bool
	updateInput   port.ManagementSystemAccountUpdateInput
	updateResult  port.ManagementSystemAccountUpdateResult
	updateFound   bool
	updateErr     error
	createCalled  bool
	createInput   port.ManagementSystemAccountCreateInput
	createResult  port.ManagementSystemAccountCreateResult
	createErr     error
}

type systemAccountInvalidatorStub struct {
	statusCalls int
	statusIDs   []string
	statusErr   error
	imageCalls  int
	imageIDs    []string
	imageErr    error
}

func (s *systemAccountInvalidatorStub) InvalidateSystemAccountStatusChanged(_ context.Context, systemAccountID string) error {
	s.statusCalls++
	s.statusIDs = append(s.statusIDs, systemAccountID)
	return s.statusErr
}

func (s *systemAccountInvalidatorStub) InvalidateSystemAccountImageGenerationChanged(_ context.Context, systemAccountID string) error {
	s.imageCalls++
	s.imageIDs = append(s.imageIDs, systemAccountID)
	return s.imageErr
}

func (s *systemAccountOptionStoreStub) ListManagementSystemAccounts(_ context.Context, input port.ManagementSystemAccountListInput) (port.ManagementSystemAccountListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *systemAccountOptionStoreStub) ListManagementSystemAccountOptions(_ context.Context, input port.ManagementSystemAccountOptionListInput) ([]port.ManagementSystemAccountOption, error) {
	s.input = input
	return s.options, s.err
}

func (s *systemAccountOptionStoreStub) ResetManagementSystemAccountPassword(_ context.Context, input port.ManagementSystemAccountPasswordResetInput) (port.ManagementSystemAccountPasswordResetResult, bool, error) {
	s.resetCalled = true
	s.resetInput = input
	return s.resetResult, s.resetFound, s.resetErr
}

func (s *systemAccountOptionStoreStub) UpdateManagementSystemAccountStatus(_ context.Context, input port.ManagementSystemAccountStatusUpdateInput) (port.ManagementSystemAccountStatusUpdateResult, bool, error) {
	s.statusCalled = true
	s.statusInput = input
	return s.statusResult, s.statusFound, s.statusErr
}

func (s *systemAccountOptionStoreStub) UpdateManagementSystemAccountImageGeneration(_ context.Context, input port.ManagementSystemAccountImageGenerationUpdateInput) (port.ManagementSystemAccountImageGenerationUpdateResult, bool, error) {
	s.imageCalled = true
	s.imageInput = input
	return s.imageResult, s.imageFound, s.imageErr
}

func (s *systemAccountOptionStoreStub) UpdateManagementSystemAccountProfile(_ context.Context, input port.ManagementSystemAccountProfileUpdateInput) (port.ManagementSystemAccountProfileUpdateResult, bool, error) {
	s.profileCalled = true
	s.profileInput = input
	return s.profileResult, s.profileFound, s.profileErr
}

func (s *systemAccountOptionStoreStub) UpdateManagementSystemAccount(_ context.Context, input port.ManagementSystemAccountUpdateInput) (port.ManagementSystemAccountUpdateResult, bool, error) {
	s.updateCalled = true
	s.updateInput = input
	return s.updateResult, s.updateFound, s.updateErr
}

func (s *systemAccountOptionStoreStub) CreateManagementSystemAccount(_ context.Context, input port.ManagementSystemAccountCreateInput) (port.ManagementSystemAccountCreateResult, error) {
	s.createCalled = true
	s.createInput = input
	return s.createResult, s.createErr
}

func stringPtr(value string) *string {
	return &value
}

func TestCreateSystemAccountNormalizesDefaultsAndDefaultAPIKeys(t *testing.T) {
	const credentialSecret = "12345678901234567890123456789012"
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := &systemAccountOptionStoreStub{
		createResult: port.ManagementSystemAccountCreateResult{
			Account: port.ManagementSystemAccountSummary{
				ID:                     "sys_new",
				Username:               "new_user",
				DisplayName:            "新用户",
				Description:            "说明",
				Role:                   "user",
				Status:                 "active",
				MustChangePassword:     true,
				ImageGenerationEnabled: true,
				CreatedAt:              now,
				UpdatedAt:              now,
			},
			DefaultGroupIDs:  []string{"grp_1"},
			DefaultAPIKeyIDs: []string{"key_1"},
		},
	}
	description := " 说明 "
	imageEnabled := true
	service := NewServiceWithOptions(ServiceOptions{
		Store:        store,
		Now:          func() time.Time { return now },
		HashPassword: func(value string) (string, error) { return "hashed:" + value, nil },
		Secret:       credentialSecret,
	})

	result, err := service.Create(context.Background(), CreateInput{
		Username:               "new_user",
		DisplayName:            "新用户",
		Description:            &description,
		Password:               "Pass1234",
		ImageGenerationEnabled: &imageEnabled,
	})

	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !store.createCalled {
		t.Fatal("CreateManagementSystemAccount() was not called")
	}
	input := store.createInput
	if input.Username != "new_user" || input.DisplayName != "新用户" || input.Description == nil || *input.Description != "说明" {
		t.Fatalf("normalized create input = %+v", input)
	}
	if input.Role != "user" || input.Status != "active" || !input.MustChangePassword || !input.ImageGenerationEnabled {
		t.Fatalf("default fields = role %q status %q mustChange %v image %v", input.Role, input.Status, input.MustChangePassword, input.ImageGenerationEnabled)
	}
	if input.PasswordHash != "hashed:Pass1234" || !input.CreatedAt.Equal(now) || !input.UpdatedAt.Equal(now) {
		t.Fatalf("password/time input = %+v", input)
	}
	if len(input.DefaultAPIKeys) != 7 {
		t.Fatalf("default api keys = %d, want 7", len(input.DefaultAPIKeys))
	}
	seen := map[string]bool{}
	codec := secretcrypto.NewJSONCodec(credentialSecret)
	for _, item := range input.DefaultAPIKeys {
		if item.ID == "" || item.KeyHash == "" || item.KeyPrefix == "" || item.KeySuffix == "" || item.KeySecretEncrypted == "" {
			t.Fatalf("default api key item missing fields: %+v", item)
		}
		if !strings.HasPrefix(item.KeyPrefix, "sk-") {
			t.Fatalf("key prefix = %q, want sk- prefix", item.KeyPrefix)
		}
		if seen[item.KeyHash] {
			t.Fatalf("duplicate key hash %q", item.KeyHash)
		}
		payload, err := codec.DecryptJSON(item.KeySecretEncrypted)
		if err != nil {
			t.Fatalf("decrypt default api key: %v", err)
		}
		secret, ok := payload["key"].(string)
		if !ok ||
			item.KeyHash != apikeysecret.Hash(secret) ||
			item.KeyPrefix != apikeysecret.Prefix(secret) ||
			item.KeySuffix != apikeysecret.Suffix(secret) {
			t.Fatalf("default api key material mismatch: item=%+v payload=%#v", item, payload)
		}
		seen[item.KeyHash] = true
	}
	if result.Account.ID != "sys_new" || result.Account.LastLoginAt != "" || len(result.DefaultAPIKeyIDs) != 1 || len(result.DefaultGroupIDs) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCreateSystemAccountNormalizesAdminMustChangePassword(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	requestedMustChangePassword := true
	store := &systemAccountOptionStoreStub{
		createResult: port.ManagementSystemAccountCreateResult{
			Account: port.ManagementSystemAccountSummary{
				ID:                 "sys_admin",
				Username:           "admin_user",
				DisplayName:        "管理员",
				Role:               "admin",
				Status:             "active",
				MustChangePassword: false,
				CreatedAt:          now,
				UpdatedAt:          now,
			},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:        store,
		Now:          func() time.Time { return now },
		HashPassword: func(string) (string, error) { return "hash", nil },
	})

	result, err := service.Create(context.Background(), CreateInput{
		Username:           "admin_user",
		DisplayName:        "管理员",
		Password:           "Pass1234",
		Role:               "admin",
		MustChangePassword: &requestedMustChangePassword,
	})

	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.createInput.Role != "admin" || store.createInput.MustChangePassword {
		t.Fatalf("admin create input = %+v, want mustChangePassword=false", store.createInput)
	}
	if result.Account.MustChangePassword {
		t.Fatalf("result = %+v, want admin mustChangePassword=false", result)
	}
}

func TestCreateSystemAccountRejectsInvalidAndDuplicateInputs(t *testing.T) {
	want := errors.New("postgres down")
	longDescription := ""
	for i := 0; i < maxDescriptionRunes+1; i++ {
		longDescription += "a"
	}
	tests := []struct {
		name    string
		input   CreateInput
		store   *systemAccountOptionStoreStub
		wantErr error
	}{
		{name: "missing username", input: CreateInput{DisplayName: "用户", Password: "Pass1234"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountCreateInvalid},
		{name: "short password", input: CreateInput{Username: "user", DisplayName: "用户", Password: "123"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountCreateInvalid},
		{name: "whitespace username", input: CreateInput{Username: "new user", DisplayName: "用户", Password: "Pass1234"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountWhitespace},
		{name: "trimmed username still invalid", input: CreateInput{Username: " user ", DisplayName: "用户", Password: "Pass1234"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountWhitespace},
		{name: "trimmed display still invalid", input: CreateInput{Username: "user", DisplayName: " 用户 ", Password: "Pass1234"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountWhitespace},
		{name: "unicode whitespace display name", input: CreateInput{Username: "user", DisplayName: "用户　名称", Password: "Pass1234"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountWhitespace},
		{name: "invalid role", input: CreateInput{Username: "user", DisplayName: "用户", Password: "Pass1234", Role: "super_admin"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountCreateInvalid},
		{name: "spaced role", input: CreateInput{Username: "user", DisplayName: "用户", Password: "Pass1234", Role: " admin "}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountCreateInvalid},
		{name: "invalid status", input: CreateInput{Username: "user", DisplayName: "用户", Password: "Pass1234", Status: "archived"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountCreateInvalid},
		{name: "spaced status", input: CreateInput{Username: "user", DisplayName: "用户", Password: "Pass1234", Status: " active "}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountCreateInvalid},
		{name: "description too long", input: CreateInput{Username: "user", DisplayName: "用户", Description: &longDescription, Password: "Pass1234"}, store: &systemAccountOptionStoreStub{}, wantErr: ErrSystemAccountCreateInvalid},
		{name: "duplicate username", input: CreateInput{Username: "user", DisplayName: "用户", Password: "Pass1234"}, store: &systemAccountOptionStoreStub{createErr: port.ErrManagementSystemAccountUsernameExists}, wantErr: ErrSystemAccountUsernameExists},
		{name: "duplicate display", input: CreateInput{Username: "user", DisplayName: "用户", Password: "Pass1234"}, store: &systemAccountOptionStoreStub{createErr: port.ErrManagementSystemAccountDisplayNameExists}, wantErr: ErrSystemAccountDisplayNameExists},
		{name: "store error", input: CreateInput{Username: "user", DisplayName: "用户", Password: "Pass1234"}, store: &systemAccountOptionStoreStub{createErr: want}, wantErr: want},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewServiceWithOptions(ServiceOptions{Store: tt.store, HashPassword: func(string) (string, error) { return "hash", nil }})

			_, err := service.Create(context.Background(), tt.input)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create() error = %v, want %v", err, tt.wantErr)
			}
			if errors.Is(tt.wantErr, ErrSystemAccountCreateInvalid) || errors.Is(tt.wantErr, ErrSystemAccountWhitespace) {
				if tt.store.createCalled {
					t.Fatal("store should not be called for invalid input")
				}
			}
		})
	}
}

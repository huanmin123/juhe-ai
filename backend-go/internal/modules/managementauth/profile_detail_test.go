package managementauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestProfileDetailServiceReturnsSafeCurrentAccountSummary(t *testing.T) {
	createdAt := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	reader := &managementCurrentUserProfileReaderStub{
		account: port.ManagementSystemAccountSummary{
			ID: "sys_user", Username: "user", DisplayName: "测试用户", Description: "普通账户",
			Role: "user", Status: "active", MustChangePassword: true, ImageGenerationEnabled: true,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		found: true,
	}
	service := NewProfileDetailService(reader)

	result, err := service.GetProfile(context.Background(), Context{SystemAccountID: "sys_user"})
	if err != nil {
		t.Fatalf("GetProfile() error = %v", err)
	}
	if reader.systemAccountID != "sys_user" || result.DisplayName != "测试用户" || !result.ImageGenerationEnabled || !result.MustChangePassword {
		t.Fatalf("GetProfile() = %+v, reader id = %q", result, reader.systemAccountID)
	}
}

func TestProfileDetailServiceNormalizesAdminMustChangePassword(t *testing.T) {
	service := NewProfileDetailService(&managementCurrentUserProfileReaderStub{
		account: port.ManagementSystemAccountSummary{ID: "sys_admin", Role: "admin", MustChangePassword: true},
		found:   true,
	})

	result, err := service.GetProfile(context.Background(), Context{SystemAccountID: "sys_admin"})
	if err != nil {
		t.Fatalf("GetProfile() error = %v", err)
	}
	if result.MustChangePassword {
		t.Fatal("admin mustChangePassword should be normalized to false")
	}
}

func TestProfileDetailServiceMapsMissingAccount(t *testing.T) {
	service := NewProfileDetailService(&managementCurrentUserProfileReaderStub{})
	_, err := service.GetProfile(context.Background(), Context{SystemAccountID: "missing"})
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("GetProfile() error = %v, want ErrProfileNotFound", err)
	}
}

type managementCurrentUserProfileReaderStub struct {
	systemAccountID string
	account         port.ManagementSystemAccountSummary
	found           bool
	err             error
}

func (s *managementCurrentUserProfileReaderStub) FindManagementCurrentUserProfile(_ context.Context, systemAccountID string) (port.ManagementSystemAccountSummary, bool, error) {
	s.systemAccountID = systemAccountID
	return s.account, s.found, s.err
}

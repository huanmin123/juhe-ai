package managementauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestNormalizeProfileDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{name: "valid", value: "新名称", want: "新名称"},
		{name: "empty", value: "", wantErr: ErrProfileDisplayNameInvalid},
		{name: "space", value: "bad user", wantErr: ErrProfileDisplayNameWhitespace},
		{name: "leading whitespace", value: " baduser", wantErr: ErrProfileDisplayNameWhitespace},
		{name: "newline", value: "bad\nuser", wantErr: ErrProfileDisplayNameWhitespace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeProfileDisplayName(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NormalizeProfileDisplayName() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeProfileDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProfileServiceUpdateProfile(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := &managementProfileWriterStub{
		result: port.ManagementCurrentUserProfileUpdateResult{
			Before: port.ManagementCurrentUserProfile{
				ID:          "sys_user",
				Username:    "user",
				DisplayName: "旧名称",
				Role:        "user",
			},
			Account: port.ManagementCurrentUserProfile{
				ID:          "sys_user",
				Username:    "user",
				DisplayName: "新名称",
				Role:        "user",
			},
		},
		found: true,
	}
	service := NewProfileServiceWithOptions(ProfileServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	result, err := service.UpdateProfile(context.Background(), ProfileUpdateInput{
		AuthContext: Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			DisplayName:     "旧名称",
			Role:            "user",
		},
		DisplayName: "新名称",
	})
	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if !store.called {
		t.Fatal("store was not called")
	}
	if store.input.SystemAccountID != "sys_user" || store.input.DisplayName != "新名称" || !store.input.UpdatedAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
	if !result.Changed || result.Before.DisplayName != "旧名称" || result.Account.DisplayName != "新名称" {
		t.Fatalf("result = %+v", result)
	}
}

func TestProfileServiceUpdateProfileNoopSkipsStore(t *testing.T) {
	store := &managementProfileWriterStub{}
	service := NewProfileServiceWithOptions(ProfileServiceOptions{Store: store})

	result, err := service.UpdateProfile(context.Background(), ProfileUpdateInput{
		AuthContext: Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			DisplayName:     "当前名称",
			Role:            "user",
		},
		DisplayName: "当前名称",
	})
	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if store.called {
		t.Fatal("store should not be called for no-op display name update")
	}
	if result.Changed || result.Account.DisplayName != "当前名称" {
		t.Fatalf("result = %+v", result)
	}
}

func TestProfileServiceUpdateProfileMapsStoreErrors(t *testing.T) {
	tests := []struct {
		name    string
		found   bool
		err     error
		wantErr error
	}{
		{name: "not found", found: false, wantErr: ErrProfileNotFound},
		{name: "duplicate display name", found: true, err: port.ErrManagementProfileDisplayNameExists, wantErr: ErrProfileDisplayNameExists},
		{name: "store error", found: true, err: errors.New("postgres down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewProfileServiceWithOptions(ProfileServiceOptions{
				Store: &managementProfileWriterStub{found: tt.found, err: tt.err},
			})

			_, err := service.UpdateProfile(context.Background(), ProfileUpdateInput{
				AuthContext: Context{
					SystemAccountID: "sys_user",
					DisplayName:     "旧名称",
				},
				DisplayName: "新名称",
			})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("UpdateProfile() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if !errors.Is(err, tt.err) {
				t.Fatalf("UpdateProfile() error = %v, want store error", err)
			}
		})
	}
}

func TestProfileServiceMapsAdminMustChangePasswordToFalse(t *testing.T) {
	service := NewProfileServiceWithOptions(ProfileServiceOptions{
		Store: &managementProfileWriterStub{
			found: true,
			result: port.ManagementCurrentUserProfileUpdateResult{
				Before:  port.ManagementCurrentUserProfile{ID: "sys_admin", Username: "admin", DisplayName: "旧名称", Role: "admin", MustChangePassword: true},
				Account: port.ManagementCurrentUserProfile{ID: "sys_admin", Username: "admin", DisplayName: "新名称", Role: "admin", MustChangePassword: true},
			},
		},
	})

	result, err := service.UpdateProfile(context.Background(), ProfileUpdateInput{
		AuthContext: Context{SystemAccountID: "sys_admin", DisplayName: "旧名称", Role: "admin"},
		DisplayName: "新名称",
	})
	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if result.Account.MustChangePassword {
		t.Fatalf("admin mustChangePassword = true, want false; result = %+v", result)
	}
}

type managementProfileWriterStub struct {
	called bool
	input  port.ManagementCurrentUserProfileUpdateInput
	result port.ManagementCurrentUserProfileUpdateResult
	found  bool
	err    error
}

func (s *managementProfileWriterStub) UpdateManagementCurrentUserProfile(_ context.Context, input port.ManagementCurrentUserProfileUpdateInput) (port.ManagementCurrentUserProfileUpdateResult, bool, error) {
	s.called = true
	s.input = input
	return s.result, s.found, s.err
}

package managementauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestPasswordHashMatchesNodePBKDF2Format(t *testing.T) {
	salt := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	hash := hashPasswordWithSalt("Pass1234", salt)
	want := "pbkdf2$sha512$120000$AAECAwQFBgcICQoLDA0ODw$Y0L6oesYt2UCwJUxYYcXkm2Y5lOKp2BvcsISx2-kdQk"
	if hash != want {
		t.Fatalf("hashPasswordWithSalt() = %q, want %q", hash, want)
	}
	if !VerifyPassword("Pass1234", hash) {
		t.Fatal("VerifyPassword() rejected the generated hash")
	}
	if VerifyPassword("WrongPass", hash) {
		t.Fatal("VerifyPassword() accepted the wrong password")
	}
	for _, malformed := range []string{
		"",
		"pbkdf2$sha256$120000$salt$hash",
		"pbkdf2$sha512$0$AAECAwQFBgcICQoLDA0ODw$Y0L6oesYt2UCwJUxYYcXkm2Y5lOKp2BvcsISx2-kdQk",
		"pbkdf2$sha512$120000$bad salt$hash",
	} {
		if VerifyPassword("Pass1234", malformed) {
			t.Fatalf("VerifyPassword() accepted malformed hash %q", malformed)
		}
	}
}

func TestPasswordServiceChangePasswordAllowsMustChangeWithoutOldPassword(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := &managementPasswordChangerStub{
		updated: port.ManagementSystemAccountSummary{
			ID:                 "sys_user",
			Username:           "user",
			DisplayName:        "用户",
			Role:               "user",
			Status:             "active",
			MustChangePassword: false,
			CreatedAt:          now.Add(-time.Hour),
			UpdatedAt:          now,
		},
		updateFound: true,
	}
	service := NewPasswordServiceWithOptions(PasswordServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
		HashPassword: func(password string) (string, error) {
			if password != "NewPass123" {
				t.Fatalf("hash password input = %q", password)
			}
			return "new-hash", nil
		},
		VerifyPassword: func(string, string) bool {
			t.Fatal("must-change password flow must not verify the old password")
			return false
		},
	})

	result, err := service.ChangePassword(context.Background(), PasswordChangeInput{
		AuthContext: Context{
			SystemAccountID:    "sys_user",
			Username:           "user",
			DisplayName:        "用户",
			Role:               "user",
			MustChangePassword: true,
			SessionID:          "sess_current",
		},
		NewPassword: "NewPass123",
	})
	if err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if store.credentialCalled {
		t.Fatal("credential lookup should be skipped for must-change users")
	}
	if store.updateInput.SystemAccountID != "sys_user" || store.updateInput.PasswordHash != "new-hash" || !store.updateInput.UpdatedAt.Equal(now) {
		t.Fatalf("update input = %+v", store.updateInput)
	}
	if store.revokeSystemAccountID != "sys_user" || store.revokeKeepSessionID != "sess_current" {
		t.Fatalf("revoke input = %q/%q", store.revokeSystemAccountID, store.revokeKeepSessionID)
	}
	if result.Account.ID != "sys_user" || result.Account.MustChangePassword {
		t.Fatalf("result = %+v", result.Account)
	}
}

func TestPasswordServiceChangePasswordVerifiesOldPasswordForRegularUsers(t *testing.T) {
	now := time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC)
	oldHash := "old-hash"
	store := &managementPasswordChangerStub{
		credential: port.ManagementSystemAccountPasswordCredential{
			ID:           "sys_user",
			Username:     "user",
			Status:       "active",
			PasswordHash: oldHash,
		},
		credentialFound: true,
		updated: port.ManagementSystemAccountSummary{
			ID:        "sys_user",
			Username:  "user",
			Role:      "user",
			Status:    "active",
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
		},
		updateFound: true,
	}
	service := NewPasswordServiceWithOptions(PasswordServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
		HashPassword: func(password string) (string, error) {
			return "new-hash", nil
		},
		VerifyPassword: func(password string, hash string) bool {
			return password == "OldPass123" && hash == oldHash
		},
	})
	oldPassword := "OldPass123"

	if _, err := service.ChangePassword(context.Background(), PasswordChangeInput{
		AuthContext: Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			Role:            "user",
			SessionID:       "sess_current",
		},
		OldPassword: &oldPassword,
		NewPassword: "NewPass123",
	}); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if !store.credentialCalled || store.credentialUsername != "user" {
		t.Fatalf("credential lookup = %v/%q", store.credentialCalled, store.credentialUsername)
	}
	if store.updateInput.PasswordHash != "new-hash" {
		t.Fatalf("update input = %+v", store.updateInput)
	}
}

func TestPasswordServiceChangePasswordRejectsInvalidInputs(t *testing.T) {
	empty := ""
	space := "bad pass"
	oldSpace := "bad old"
	tests := []struct {
		name        string
		oldPassword *string
		newPassword string
		wantErr     error
		mustChange  bool
	}{
		{name: "short new password", newPassword: "abc", wantErr: ErrPasswordInvalid},
		{name: "empty old password", oldPassword: &empty, newPassword: "NewPass123", wantErr: ErrPasswordInvalid},
		{name: "new password whitespace", newPassword: space, wantErr: ErrPasswordWhitespace},
		{name: "old password whitespace", oldPassword: &oldSpace, newPassword: "NewPass123", wantErr: ErrPasswordWhitespace, mustChange: true},
		{name: "old password required", newPassword: "NewPass123", wantErr: ErrPasswordOldRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &managementPasswordChangerStub{}
			service := NewPasswordServiceWithOptions(PasswordServiceOptions{
				Store: store,
				HashPassword: func(string) (string, error) {
					t.Fatal("hash password should not be called for invalid input")
					return "", nil
				},
			})
			_, err := service.ChangePassword(context.Background(), PasswordChangeInput{
				AuthContext: Context{
					SystemAccountID:    "sys_user",
					Username:           "user",
					Role:               "user",
					MustChangePassword: tt.mustChange,
					SessionID:          "sess_current",
				},
				OldPassword: tt.oldPassword,
				NewPassword: tt.newPassword,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ChangePassword() error = %v, want %v", err, tt.wantErr)
			}
			if store.updateCalled || store.revokeCalled {
				t.Fatalf("store should not be mutated; update=%v revoke=%v", store.updateCalled, store.revokeCalled)
			}
		})
	}
}

func TestPasswordServiceChangePasswordRejectsWrongOldPassword(t *testing.T) {
	store := &managementPasswordChangerStub{
		credential: port.ManagementSystemAccountPasswordCredential{
			ID:           "sys_user",
			Status:       "active",
			PasswordHash: "old-hash",
		},
		credentialFound: true,
	}
	service := NewPasswordServiceWithOptions(PasswordServiceOptions{
		Store:          store,
		HashPassword:   func(string) (string, error) { return "new-hash", nil },
		VerifyPassword: func(string, string) bool { return false },
	})
	oldPassword := "WrongPass"

	_, err := service.ChangePassword(context.Background(), PasswordChangeInput{
		AuthContext: Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			Role:            "user",
			SessionID:       "sess_current",
		},
		OldPassword: &oldPassword,
		NewPassword: "NewPass123",
	})
	if !errors.Is(err, ErrPasswordOldIncorrect) {
		t.Fatalf("ChangePassword() error = %v, want %v", err, ErrPasswordOldIncorrect)
	}
	if store.updateCalled || store.revokeCalled {
		t.Fatalf("store should not be mutated; update=%v revoke=%v", store.updateCalled, store.revokeCalled)
	}
}

func TestPasswordServiceChangePasswordMapsStoreErrors(t *testing.T) {
	oldPassword := "OldPass123"
	tests := []struct {
		name        string
		store       *managementPasswordChangerStub
		wantErr     error
		useOldCheck bool
	}{
		{name: "credential not found", store: &managementPasswordChangerStub{}, wantErr: ErrPasswordOldIncorrect, useOldCheck: true},
		{name: "credential error", store: &managementPasswordChangerStub{credentialErr: errors.New("postgres down")}, wantErr: errors.New("postgres down"), useOldCheck: true},
		{name: "update not found", store: &managementPasswordChangerStub{updateFound: false}, wantErr: ErrPasswordAccountGone},
		{name: "revoke error", store: &managementPasswordChangerStub{updateFound: true, revokeErr: errors.New("redis down")}, wantErr: errors.New("redis down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			if tt.useOldCheck {
				store.credential.PasswordHash = "old-hash"
			}
			if store.updateFound {
				store.updated = port.ManagementSystemAccountSummary{ID: "sys_user", Role: "user", Status: "active"}
			}
			service := NewPasswordServiceWithOptions(PasswordServiceOptions{
				Store:        store,
				HashPassword: func(string) (string, error) { return "new-hash", nil },
				VerifyPassword: func(password string, hash string) bool {
					return hash == "old-hash" && password == oldPassword
				},
			})
			input := PasswordChangeInput{
				AuthContext: Context{
					SystemAccountID:    "sys_user",
					Username:           "user",
					Role:               "user",
					MustChangePassword: !tt.useOldCheck,
					SessionID:          "sess_current",
				},
				NewPassword: "NewPass123",
			}
			if tt.useOldCheck {
				input.OldPassword = &oldPassword
			}
			_, err := service.ChangePassword(context.Background(), input)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) && (err == nil || err.Error() != tt.wantErr.Error()) {
				t.Fatalf("ChangePassword() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

type managementPasswordChangerStub struct {
	credentialCalled   bool
	credentialUsername string
	credential         port.ManagementSystemAccountPasswordCredential
	credentialFound    bool
	credentialErr      error

	updateCalled bool
	updateInput  port.ManagementCurrentUserPasswordUpdateInput
	updated      port.ManagementSystemAccountSummary
	updateFound  bool
	updateErr    error

	revokeCalled          bool
	revokeSystemAccountID string
	revokeKeepSessionID   string
	revokeErr             error
}

func (s *managementPasswordChangerStub) FindManagementSystemAccountPasswordByUsername(_ context.Context, username string) (port.ManagementSystemAccountPasswordCredential, bool, error) {
	s.credentialCalled = true
	s.credentialUsername = username
	return s.credential, s.credentialFound, s.credentialErr
}

func (s *managementPasswordChangerStub) UpdateManagementCurrentUserPassword(_ context.Context, input port.ManagementCurrentUserPasswordUpdateInput) (port.ManagementSystemAccountSummary, bool, error) {
	s.updateCalled = true
	s.updateInput = input
	return s.updated, s.updateFound, s.updateErr
}

func (s *managementPasswordChangerStub) RevokeOtherManagementSessionsForAccount(_ context.Context, systemAccountID string, keepSessionID string) error {
	s.revokeCalled = true
	s.revokeSystemAccountID = systemAccountID
	s.revokeKeepSessionID = keepSessionID
	return s.revokeErr
}

package managementauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var fixedLoginNow = time.Date(2026, 7, 8, 12, 30, 0, 0, time.UTC)

func TestLoginServiceSuccessCreatesSessionAndClearsGuard(t *testing.T) {
	store := &loginStoreStub{
		credential: port.ManagementSystemAccountPasswordCredential{
			ID:           "sys_admin",
			Username:     "admin",
			Status:       "active",
			PasswordHash: "hash",
		},
		credentialFound: true,
		completeResult: port.ManagementLoginSessionResult{
			Account: port.ManagementSystemAccountSummary{
				ID:                 "sys_admin",
				Username:           "admin",
				DisplayName:        "管理员",
				Role:               "admin",
				Status:             "active",
				MustChangePassword: true,
				CreatedAt:          fixedLoginNow.Add(-time.Hour),
				UpdatedAt:          fixedLoginNow,
			},
			SessionID:        "sess_fixed",
			SessionExpiresAt: fixedLoginNow.Add(ManagementSessionTTL),
		},
		completeFound: true,
	}
	captcha := &loginCaptchaVerifierStub{ok: true}
	guard := &loginGuardStub{}
	service := NewLoginServiceWithOptions(LoginServiceOptions{
		Store:           store,
		Captcha:         captcha,
		Guard:           guard,
		Now:             func() time.Time { return fixedLoginNow },
		NewSessionToken: func() (string, error) { return "session-token", nil },
		NewSessionID:    func(time.Time) (string, error) { return "sess_fixed", nil },
		VerifyPassword:  func(password string, hash string) bool { return password == "secret" && hash == "hash" },
	})

	result, err := service.Login(context.Background(), LoginInput{
		Username:    "admin",
		Password:    "secret",
		CaptchaID:   "captcha-id",
		CaptchaCode: "ABCD2",
		ClientIP:    "203.0.113.10",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if !captcha.called || captcha.captchaID != "captcha-id" || captcha.captchaCode != "ABCD2" {
		t.Fatalf("captcha = %+v", captcha)
	}
	if guard.checkUsername != "admin" || guard.successUsername != "admin" || guard.failedCalled {
		t.Fatalf("guard = %+v", guard)
	}
	if store.completeInput.SystemAccountID != "sys_admin" ||
		store.completeInput.VerifiedPasswordHash != "hash" ||
		store.completeInput.SessionID != "sess_fixed" ||
		store.completeInput.TokenHash != HashSessionToken("session-token") ||
		!store.completeInput.LoggedInAt.Equal(fixedLoginNow) ||
		!store.completeInput.ExpiresAt.Equal(fixedLoginNow.Add(ManagementSessionTTL)) {
		t.Fatalf("complete input = %+v", store.completeInput)
	}
	if result.SessionToken != "session-token" || result.SessionID != "sess_fixed" || result.Account.ID != "sys_admin" {
		t.Fatalf("result = %+v", result)
	}
	if result.Account.MustChangePassword {
		t.Fatal("admin mustChangePassword should be normalized to false")
	}
}

func TestLoginServiceSkipsOnlyCaptchaWhenDisabled(t *testing.T) {
	store := &loginStoreStub{
		credential:      port.ManagementSystemAccountPasswordCredential{ID: "sys_admin", Username: "admin", Status: "active", PasswordHash: "hash"},
		credentialFound: true,
		completeResult: port.ManagementLoginSessionResult{
			Account:   port.ManagementSystemAccountSummary{ID: "sys_admin", Username: "admin", DisplayName: "管理员", Role: "admin", Status: "active"},
			SessionID: "sess_fixed", SessionExpiresAt: fixedLoginNow.Add(ManagementSessionTTL),
		},
		completeFound: true,
	}
	captcha := &loginCaptchaVerifierStub{ok: false}
	guard := &loginGuardStub{}
	service := NewLoginServiceWithOptions(LoginServiceOptions{
		Store: store, Captcha: captcha, Guard: guard, CaptchaDisabled: true,
		Now:             func() time.Time { return fixedLoginNow },
		NewSessionToken: func() (string, error) { return "session-token", nil },
		NewSessionID:    func(time.Time) (string, error) { return "sess_fixed", nil },
		VerifyPassword:  func(password string, hash string) bool { return password == "secret" && hash == "hash" },
	})

	result, err := service.Login(context.Background(), LoginInput{Username: "admin", Password: "secret", ClientIP: "203.0.113.10"})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if captcha.called {
		t.Fatal("captcha verifier should not be called when disabled")
	}
	if result.SessionToken != "session-token" || !guard.successCalled {
		t.Fatalf("result = %+v, guard = %+v", result, guard)
	}

	_, err = service.Login(context.Background(), LoginInput{Username: "admin", Password: "wrong", ClientIP: "203.0.113.10"})
	if !errors.Is(err, ErrLoginCredentialsInvalid) {
		t.Fatalf("wrong password error = %v, want credentials invalid", err)
	}
}

func TestLoginServiceDoesNotClearGuardWhenSessionIsNotCompleted(t *testing.T) {
	store := &loginStoreStub{
		credential: port.ManagementSystemAccountPasswordCredential{
			ID:           "sys_admin",
			Username:     "admin",
			Status:       "active",
			PasswordHash: "old-hash",
		},
		credentialFound: true,
		completeFound:   false,
	}
	guard := &loginGuardStub{}
	service := NewLoginServiceWithOptions(LoginServiceOptions{
		Store:           store,
		Captcha:         &loginCaptchaVerifierStub{ok: true},
		Guard:           guard,
		Now:             func() time.Time { return fixedLoginNow },
		NewSessionToken: func() (string, error) { return "session-token", nil },
		NewSessionID:    func(time.Time) (string, error) { return "sess_fixed", nil },
		VerifyPassword:  func(password string, hash string) bool { return password == "secret" && hash == "old-hash" },
	})

	_, err := service.Login(context.Background(), LoginInput{
		Username:    "admin",
		Password:    "secret",
		CaptchaID:   "captcha-id",
		CaptchaCode: "ABCD2",
		ClientIP:    "203.0.113.10",
	})
	if !errors.Is(err, ErrLoginCredentialsInvalid) {
		t.Fatalf("Login() error = %v, want credentials invalid", err)
	}
	if !store.completeCalled || store.completeInput.VerifiedPasswordHash != "old-hash" {
		t.Fatalf("complete input = %+v", store.completeInput)
	}
	if guard.successCalled {
		t.Fatalf("session failure should not clear login guard: %+v", guard)
	}
}

func TestLoginServiceCaptchaFailureStopsBeforeGuardAndCredentials(t *testing.T) {
	store := &loginStoreStub{}
	guard := &loginGuardStub{}
	service := NewLoginServiceWithOptions(LoginServiceOptions{
		Store:          store,
		Captcha:        &loginCaptchaVerifierStub{ok: false},
		Guard:          guard,
		VerifyPassword: func(string, string) bool { return true },
	})

	_, err := service.Login(context.Background(), LoginInput{
		Username:    "admin",
		Password:    "secret",
		CaptchaID:   "captcha-id",
		CaptchaCode: "wrong",
		ClientIP:    "203.0.113.10",
	})
	if !errors.Is(err, ErrLoginCaptchaInvalid) {
		t.Fatalf("Login() error = %v, want captcha invalid", err)
	}
	if guard.checkCalled || store.credentialCalled {
		t.Fatalf("captcha failure should stop before guard/store; guard=%+v store=%+v", guard, store)
	}
}

func TestLoginServiceBlockedBeforeCredentials(t *testing.T) {
	store := &loginStoreStub{}
	guard := &loginGuardStub{
		checkDecision: LoginGuardDecision{Blocked: true, Message: LoginGuardIPBlockedMessage, RetryAfterSeconds: 12},
	}
	service := NewLoginServiceWithOptions(LoginServiceOptions{
		Store:          store,
		Captcha:        &loginCaptchaVerifierStub{ok: true},
		Guard:          guard,
		VerifyPassword: func(string, string) bool { return true },
	})

	_, err := service.Login(context.Background(), LoginInput{
		Username:    "admin",
		Password:    "secret",
		CaptchaID:   "captcha-id",
		CaptchaCode: "ABCD2",
		ClientIP:    "203.0.113.10",
	})
	var limitErr *LoginLimitError
	if !errors.As(err, &limitErr) || limitErr.RetryAfterSeconds != 12 || limitErr.Message != LoginGuardIPBlockedMessage {
		t.Fatalf("Login() error = %v, want limit", err)
	}
	if store.credentialCalled {
		t.Fatal("blocked login should not query credentials")
	}
}

func TestLoginServiceWrongPasswordRecordsFailedLogin(t *testing.T) {
	store := &loginStoreStub{
		credential: port.ManagementSystemAccountPasswordCredential{
			ID:           "sys_admin",
			Username:     "admin",
			Status:       "active",
			PasswordHash: "hash",
		},
		credentialFound: true,
	}
	guard := &loginGuardStub{}
	service := NewLoginServiceWithOptions(LoginServiceOptions{
		Store:          store,
		Captcha:        &loginCaptchaVerifierStub{ok: true},
		Guard:          guard,
		VerifyPassword: func(password string, hash string) bool { return false },
	})

	_, err := service.Login(context.Background(), LoginInput{
		Username:    "admin",
		Password:    "wrong",
		CaptchaID:   "captcha-id",
		CaptchaCode: "ABCD2",
		ClientIP:    "203.0.113.10",
	})
	if !errors.Is(err, ErrLoginCredentialsInvalid) {
		t.Fatalf("Login() error = %v, want credentials invalid", err)
	}
	if !guard.failedCalled || guard.successCalled || store.completeCalled {
		t.Fatalf("wrong password should record failure only; guard=%+v store=%+v", guard, store)
	}
}

func TestLoginServiceFailureThatTriggersLockReturnsLimit(t *testing.T) {
	store := &loginStoreStub{credentialFound: false}
	guard := &loginGuardStub{
		failedDecision: LoginGuardDecision{Blocked: true, Message: LoginGuardUsernameBlockedMessage, RetryAfterSeconds: 900},
	}
	service := NewLoginServiceWithOptions(LoginServiceOptions{
		Store:          store,
		Captcha:        &loginCaptchaVerifierStub{ok: true},
		Guard:          guard,
		VerifyPassword: func(string, string) bool { return false },
	})

	_, err := service.Login(context.Background(), LoginInput{
		Username:    "missing",
		Password:    "secret",
		CaptchaID:   "captcha-id",
		CaptchaCode: "ABCD2",
		ClientIP:    "203.0.113.10",
	})
	var limitErr *LoginLimitError
	if !errors.As(err, &limitErr) || limitErr.Message != LoginGuardUsernameBlockedMessage || limitErr.RetryAfterSeconds != 900 {
		t.Fatalf("Login() error = %v, want username limit", err)
	}
}

func TestLoginServiceValidatesInput(t *testing.T) {
	service := NewLoginServiceWithOptions(LoginServiceOptions{
		Store:          &loginStoreStub{},
		Captcha:        &loginCaptchaVerifierStub{ok: true},
		Guard:          &loginGuardStub{},
		VerifyPassword: func(string, string) bool { return true },
	})
	for _, tc := range []struct {
		name  string
		input LoginInput
		want  error
	}{
		{name: "missing username", input: LoginInput{Password: "secret", CaptchaID: "c", CaptchaCode: "a"}, want: ErrLoginInvalidInput},
		{name: "missing captcha", input: LoginInput{Username: "admin", Password: "secret", CaptchaID: " ", CaptchaCode: "a"}, want: ErrLoginInvalidInput},
		{name: "username whitespace", input: LoginInput{Username: "ad min", Password: "secret", CaptchaID: "c", CaptchaCode: "a"}, want: ErrLoginWhitespace},
		{name: "password whitespace", input: LoginInput{Username: "admin", Password: "se cret", CaptchaID: "c", CaptchaCode: "a"}, want: ErrLoginWhitespace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Login(context.Background(), tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Login() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGenerateSessionTokenAndID(t *testing.T) {
	token, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken() error = %v", err)
	}
	if len(token) != 43 {
		t.Fatalf("token length = %d, want base64url 32-byte length 43", len(token))
	}
	sessionID, err := GenerateSessionID(fixedLoginNow)
	if err != nil {
		t.Fatalf("GenerateSessionID() error = %v", err)
	}
	if wantPrefix := "sess_1783513800000_"; len(sessionID) != len(wantPrefix)+8 || sessionID[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("sessionID = %q, want prefix %q plus 8 hex chars", sessionID, wantPrefix)
	}
}

type loginCaptchaVerifierStub struct {
	called      bool
	captchaID   string
	captchaCode string
	ok          bool
	err         error
}

func (s *loginCaptchaVerifierStub) VerifyChallenge(_ context.Context, captchaID string, captchaCode string) (bool, error) {
	s.called = true
	s.captchaID = captchaID
	s.captchaCode = captchaCode
	return s.ok, s.err
}

type loginGuardStub struct {
	checkCalled   bool
	checkUsername string
	checkDecision LoginGuardDecision
	checkErr      error

	failedCalled   bool
	failedUsername string
	failedDecision LoginGuardDecision
	failedErr      error

	successCalled   bool
	successUsername string
	successErr      error
}

func (s *loginGuardStub) CheckAllowed(_ context.Context, _ string, username string) (LoginGuardDecision, error) {
	s.checkCalled = true
	s.checkUsername = username
	return s.checkDecision, s.checkErr
}

func (s *loginGuardStub) RecordFailed(_ context.Context, _ string, username string) (LoginGuardDecision, error) {
	s.failedCalled = true
	s.failedUsername = username
	return s.failedDecision, s.failedErr
}

func (s *loginGuardStub) RecordSuccess(_ context.Context, _ string, username string) error {
	s.successCalled = true
	s.successUsername = username
	return s.successErr
}

type loginStoreStub struct {
	credentialCalled bool
	credential       port.ManagementSystemAccountPasswordCredential
	credentialFound  bool
	credentialErr    error

	completeCalled bool
	completeInput  port.ManagementLoginSessionInput
	completeResult port.ManagementLoginSessionResult
	completeFound  bool
	completeErr    error
}

func (s *loginStoreStub) FindManagementSystemAccountPasswordByUsername(_ context.Context, _ string) (port.ManagementSystemAccountPasswordCredential, bool, error) {
	s.credentialCalled = true
	return s.credential, s.credentialFound, s.credentialErr
}

func (s *loginStoreStub) CompleteManagementLogin(_ context.Context, input port.ManagementLoginSessionInput) (port.ManagementLoginSessionResult, bool, error) {
	s.completeCalled = true
	s.completeInput = input
	return s.completeResult, s.completeFound, s.completeErr
}

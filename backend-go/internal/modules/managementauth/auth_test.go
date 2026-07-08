package managementauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var fixedManagementAuthNow = time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

func TestHashSessionTokenMatchesNodeHashSecret(t *testing.T) {
	sum := sha256.Sum256([]byte("session-token"))
	want := hex.EncodeToString(sum[:])
	if got := HashSessionToken("session-token"); got != want {
		t.Fatalf("HashSessionToken() = %q, want %q", got, want)
	}
}

func TestParseCookieReadsSessionCookie(t *testing.T) {
	cookies := ParseCookie("theme=dark; juhe_ai_session=session-token%2Bencoded+raw; other=value")
	if got := cookies[SessionCookieName]; got != "session-token+encoded+raw" {
		t.Fatalf("session cookie = %q", got)
	}
}

func TestParseCookieSkipsMalformedEncodedCookie(t *testing.T) {
	cookies := ParseCookie("theme=dark; juhe_ai_session=session-token%ZZ")
	if _, ok := cookies[SessionCookieName]; ok {
		t.Fatalf("session cookie = %q, want skipped", cookies[SessionCookieName])
	}
}

func TestAuthenticateCookie(t *testing.T) {
	reader := &managementSessionReaderStub{
		record: activeManagementSession("session-token", "admin"),
		found:  true,
	}
	authenticator := NewAuthenticator(AuthenticatorOptions{Store: reader, Now: func() time.Time { return fixedManagementAuthNow }})

	ctx, err := authenticator.AuthenticateCookie(context.Background(), "juhe_ai_session=session-token")
	if err != nil {
		t.Fatalf("AuthenticateCookie() error = %v", err)
	}
	if reader.tokenHash != HashSessionToken("session-token") {
		t.Fatalf("tokenHash = %q", reader.tokenHash)
	}
	if ctx.SystemAccountID != "sys_admin" || ctx.Username != "admin" || ctx.Role != "admin" || ctx.SessionID != "sess_admin" {
		t.Fatalf("auth context = %+v", ctx)
	}
}

func TestAuthenticateCookieRejectsMissingOrExpiredSession(t *testing.T) {
	tests := []struct {
		name       string
		cookie     string
		record     port.ManagementSessionAccount
		found      bool
		wantStatus int
		wantMsg    string
	}{
		{name: "missing cookie", cookie: "", wantStatus: http.StatusUnauthorized, wantMsg: "请先登录"},
		{name: "not found", cookie: "juhe_ai_session=session-token", found: false, wantStatus: http.StatusUnauthorized, wantMsg: "登录会话已过期"},
		{name: "expired", cookie: "juhe_ai_session=session-token", record: expiredManagementSession("session-token", "admin"), found: true, wantStatus: http.StatusUnauthorized, wantMsg: "登录会话已过期"},
		{name: "disabled account", cookie: "juhe_ai_session=session-token", record: disabledManagementSession("session-token"), found: true, wantStatus: http.StatusUnauthorized, wantMsg: "登录会话已过期"},
		{name: "must change password", cookie: "juhe_ai_session=session-token", record: mustChangeManagementSession("session-token", "user"), found: true, wantStatus: http.StatusForbidden, wantMsg: "请先修改初始密码"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &managementSessionReaderStub{record: tt.record, found: tt.found}
			authenticator := NewAuthenticator(AuthenticatorOptions{Store: reader, Now: func() time.Time { return fixedManagementAuthNow }})

			_, err := authenticator.AuthenticateCookie(context.Background(), tt.cookie)
			var authErr *AuthError
			if !errors.As(err, &authErr) {
				t.Fatalf("error = %v, want AuthError", err)
			}
			if authErr.StatusCode != tt.wantStatus || authErr.Message != tt.wantMsg {
				t.Fatalf("auth error = %+v", authErr)
			}
			if tt.name == "must change password" && authErr.Code != ErrorCodeMustChangePassword {
				t.Fatalf("code = %q", authErr.Code)
			}
		})
	}
}

func TestAuthenticateCookieDoesNotBlockAdminMustChangePassword(t *testing.T) {
	reader := &managementSessionReaderStub{
		record: mustChangeManagementSession("session-token", "admin"),
		found:  true,
	}
	authenticator := NewAuthenticator(AuthenticatorOptions{Store: reader, Now: func() time.Time { return fixedManagementAuthNow }})

	ctx, err := authenticator.AuthenticateCookie(context.Background(), "juhe_ai_session=session-token")
	if err != nil {
		t.Fatalf("AuthenticateCookie() error = %v", err)
	}
	if ctx.Role != "admin" || ctx.MustChangePassword {
		t.Fatalf("auth context = %+v", ctx)
	}
}

func TestAuthenticateCookieForCurrentUserAllowsMustChangePassword(t *testing.T) {
	reader := &managementSessionReaderStub{
		record: mustChangeManagementSession("session-token", "user"),
		found:  true,
	}
	authenticator := NewAuthenticator(AuthenticatorOptions{Store: reader, Now: func() time.Time { return fixedManagementAuthNow }})

	ctx, err := authenticator.AuthenticateCookieForCurrentUser(context.Background(), "juhe_ai_session=session-token")
	if err != nil {
		t.Fatalf("AuthenticateCookieForCurrentUser() error = %v", err)
	}
	if ctx.Role != "user" || !ctx.MustChangePassword {
		t.Fatalf("auth context = %+v", ctx)
	}
}

func TestLogoutCookieRevokesSessionToken(t *testing.T) {
	revoker := &managementSessionRevokerStub{}
	authenticator := NewAuthenticator(AuthenticatorOptions{Revoker: revoker})

	if err := authenticator.LogoutCookie(context.Background(), "theme=dark; juhe_ai_session=session-token"); err != nil {
		t.Fatalf("LogoutCookie() error = %v", err)
	}
	if revoker.tokenHash != HashSessionToken("session-token") {
		t.Fatalf("tokenHash = %q", revoker.tokenHash)
	}
}

func TestLogoutCookieAllowsMissingSessionCookie(t *testing.T) {
	revoker := &managementSessionRevokerStub{}
	authenticator := NewAuthenticator(AuthenticatorOptions{Revoker: revoker})

	if err := authenticator.LogoutCookie(context.Background(), "theme=dark"); err != nil {
		t.Fatalf("LogoutCookie() error = %v", err)
	}
	if revoker.called {
		t.Fatal("LogoutCookie() revoked a session for missing cookie")
	}
}

func TestLogoutCookieUsesStoreRevokerWhenAvailable(t *testing.T) {
	store := &managementSessionStoreStub{}
	authenticator := NewAuthenticator(AuthenticatorOptions{Store: store})

	if err := authenticator.LogoutCookie(context.Background(), "juhe_ai_session=session-token"); err != nil {
		t.Fatalf("LogoutCookie() error = %v", err)
	}
	if store.managementSessionRevokerStub.tokenHash != HashSessionToken("session-token") {
		t.Fatalf("tokenHash = %q", store.managementSessionRevokerStub.tokenHash)
	}
}

func activeManagementSession(token string, role string) port.ManagementSessionAccount {
	return port.ManagementSessionAccount{
		SessionID:          "sess_admin",
		TokenHash:          HashSessionToken(token),
		ExpiresAt:          fixedManagementAuthNow.Add(time.Hour),
		LastSeenAt:         fixedManagementAuthNow.Add(-time.Minute),
		AccountID:          "sys_admin",
		Username:           "admin",
		DisplayName:        "管理员",
		Role:               role,
		Status:             "active",
		MustChangePassword: false,
	}
}

func expiredManagementSession(token string, role string) port.ManagementSessionAccount {
	session := activeManagementSession(token, role)
	session.ExpiresAt = fixedManagementAuthNow.Add(-time.Second)
	return session
}

func disabledManagementSession(token string) port.ManagementSessionAccount {
	session := activeManagementSession(token, "admin")
	session.Status = "disabled"
	return session
}

func mustChangeManagementSession(token string, role string) port.ManagementSessionAccount {
	session := activeManagementSession(token, role)
	session.MustChangePassword = true
	return session
}

type managementSessionReaderStub struct {
	tokenHash string
	record    port.ManagementSessionAccount
	found     bool
	err       error
}

func (s *managementSessionReaderStub) FindManagementSessionByTokenHash(_ context.Context, tokenHash string) (port.ManagementSessionAccount, bool, error) {
	s.tokenHash = tokenHash
	return s.record, s.found, s.err
}

type managementSessionRevokerStub struct {
	called    bool
	tokenHash string
	err       error
}

func (s *managementSessionRevokerStub) RevokeManagementSessionByTokenHash(_ context.Context, tokenHash string) error {
	s.called = true
	s.tokenHash = tokenHash
	return s.err
}

type managementSessionStoreStub struct {
	managementSessionReaderStub
	managementSessionRevokerStub
}

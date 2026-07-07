package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

func TestParseBearerTokenMatchesNodeContract(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{name: "missing", value: "", ok: false},
		{name: "wrong scheme", value: "Token abc", ok: false},
		{name: "empty token", value: "Bearer   ", ok: false},
		{name: "basic", value: "Bearer juis_token", want: "juis_token", ok: true},
		{name: "case insensitive", value: "bearer juis_token", want: "juis_token", ok: true},
		{name: "trims", value: "  Bearer   juis_token  ", want: "juis_token", ok: true},
		{name: "keeps internal spaces", value: "Bearer token with spaces", want: "token with spaces", ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseBearerToken(tt.value)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ParseBearerToken(%q) = %q, %v; want %q, %v", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestHashExternalSourceTokenMatchesNodeNamespace(t *testing.T) {
	sum := sha256.Sum256([]byte("external-integration-source-token:juis_plain"))
	want := hex.EncodeToString(sum[:])
	if got := HashExternalSourceToken("juis_plain"); got != want {
		t.Fatalf("HashExternalSourceToken() = %q, want %q", got, want)
	}
}

func TestAuthenticatorSuccessUsesScopeIntersectionAndTouchesLastUsed(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	previous := now.Add(-2 * time.Minute)
	reader := &authReaderStub{
		record: baseAuthRecord(),
		found:  true,
	}
	reader.record.SourceScopes = []string{publicapi.ScopeGroupListRead, publicapi.ScopeAccountListRead}
	reader.record.TokenScopes = []string{publicapi.ScopeAPIKeyListRead, publicapi.ScopeGroupListRead, publicapi.ScopeGroupAddWrite}
	reader.record.SourceLastUsedAt = &previous
	reader.record.TokenLastUsedAt = &previous
	authenticator := NewAuthenticator(AuthenticatorOptions{
		Store: reader,
		Now:   func() time.Time { return now },
	})

	ctx, err := authenticator.Authenticate(context.Background(), "Bearer juis_plain", publicapi.ScopeGroupListRead)

	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if ctx.SourceRefID != "source_1" || ctx.TokenID != "token_1" {
		t.Fatalf("context = %+v", ctx)
	}
	if got := ExternalSourceRateLimitKey(ctx); got != "source_1:token_1:juis_test" {
		t.Fatalf("ExternalSourceRateLimitKey() = %q", got)
	}
	if got, want := ctx.Scopes, []string{publicapi.ScopeGroupListRead}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("granted scopes = %#v, want %#v", got, want)
	}
	if reader.tokenHash != HashExternalSourceToken("juis_plain") {
		t.Fatalf("tokenHash = %q", reader.tokenHash)
	}
	if len(reader.touches) != 1 {
		t.Fatalf("touches = %d, want 1", len(reader.touches))
	}
	touch := reader.touches[0]
	if !touch.TouchSource || !touch.TouchToken || !touch.Now.Equal(now) {
		t.Fatalf("touch = %+v", touch)
	}
}

func TestAuthenticatorSkipsRecentLastUsedTouch(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	previous := now.Add(-30 * time.Second)
	reader := &authReaderStub{record: baseAuthRecord(), found: true}
	reader.record.SourceLastUsedAt = &previous
	reader.record.TokenLastUsedAt = &previous
	authenticator := NewAuthenticator(AuthenticatorOptions{
		Store: reader,
		Now:   func() time.Time { return now },
	})

	if _, err := authenticator.Authenticate(context.Background(), "Bearer juis_plain", publicapi.ScopeGroupListRead); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if len(reader.touches) != 0 {
		t.Fatalf("touches = %d, want 0", len(reader.touches))
	}
}

func TestAuthenticatorMarksBuiltInTestToken(t *testing.T) {
	reader := &authReaderStub{record: baseAuthRecord(), found: true}
	reader.record.SourceRefID = publicapi.BuiltInTestSourceID
	reader.record.TokenID = "ordinary_token"
	authenticator := NewAuthenticator(AuthenticatorOptions{Store: reader})

	ctx, err := authenticator.Authenticate(context.Background(), "Bearer juis_plain", publicapi.ScopeGroupListRead)

	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if !ctx.IsTestToken {
		t.Fatal("IsTestToken = false, want true")
	}
}

func TestAuthenticatorErrors(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	expired := now
	tests := []struct {
		name       string
		header     string
		record     port.PublicAPIAuthRecord
		err        error
		found      bool
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "missing bearer",
			header:     "Token no",
			wantStatus: http.StatusUnauthorized,
			wantCode:   ErrorCodeTokenMissing,
			wantMsg:    "缺少来源系统 token",
		},
		{
			name:       "not found",
			header:     "Bearer missing",
			found:      false,
			wantStatus: http.StatusUnauthorized,
			wantCode:   ErrorCodeUnauthorized,
			wantMsg:    "来源系统或 token 无效",
		},
		{
			name:       "source disabled",
			header:     "Bearer juis_plain",
			record:     withSourceStatus(baseAuthRecord(), "disabled"),
			found:      true,
			wantStatus: http.StatusForbidden,
			wantCode:   ErrorCodeSourceDisabled,
			wantMsg:    "来源系统未启用",
		},
		{
			name:       "source expired",
			header:     "Bearer juis_plain",
			record:     withSourceExpiresAt(baseAuthRecord(), &expired),
			found:      true,
			wantStatus: http.StatusForbidden,
			wantCode:   ErrorCodeSourceExpired,
			wantMsg:    "来源系统已过期",
		},
		{
			name:       "token disabled",
			header:     "Bearer juis_plain",
			record:     withTokenStatus(baseAuthRecord(), publicapi.TokenStatusRevoked),
			found:      true,
			wantStatus: http.StatusUnauthorized,
			wantCode:   ErrorCodeTokenUnavailable,
			wantMsg:    "来源系统 token 不可用",
		},
		{
			name:       "token expired",
			header:     "Bearer juis_plain",
			record:     withTokenExpiresAt(baseAuthRecord(), &expired),
			found:      true,
			wantStatus: http.StatusUnauthorized,
			wantCode:   ErrorCodeTokenUnavailable,
			wantMsg:    "来源系统 token 不可用",
		},
		{
			name:       "scope forbidden",
			header:     "Bearer juis_plain",
			record:     baseAuthRecord(),
			found:      true,
			wantStatus: http.StatusForbidden,
			wantCode:   ErrorCodeScopeForbidden,
			wantMsg:    "来源系统没有调用该接口的权限",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &authReaderStub{record: tt.record, err: tt.err, found: tt.found}
			authenticator := NewAuthenticator(AuthenticatorOptions{
				Store: reader,
				Now:   func() time.Time { return now },
			})

			_, err := authenticator.Authenticate(context.Background(), tt.header, publicapi.ScopeAccountListRead)

			var authErr *AuthError
			if !errors.As(err, &authErr) {
				t.Fatalf("Authenticate() error = %T %v, want AuthError", err, err)
			}
			if authErr.StatusCode != tt.wantStatus || authErr.Code != tt.wantCode || authErr.Message != tt.wantMsg {
				t.Fatalf("auth error = %+v", authErr)
			}
			if len(reader.touches) != 0 {
				t.Fatalf("touches = %d, want 0", len(reader.touches))
			}
		})
	}
}

func baseAuthRecord() port.PublicAPIAuthRecord {
	return port.PublicAPIAuthRecord{
		SourceRefID:  "source_1",
		SourceName:   "外部来源",
		SourceStatus: publicapi.SourceStatusActive,
		SourceScopes: []string{publicapi.ScopeGroupListRead},
		SourceRateLimits: []port.PublicAPIRateLimitRule{
			{WindowSeconds: 60, MaxRequests: 10},
		},
		TokenID:     "token_1",
		TokenName:   "来源 token",
		TokenPrefix: "juis_test",
		TokenStatus: publicapi.TokenStatusActive,
		TokenScopes: []string{publicapi.ScopeGroupListRead},
	}
}

func withSourceStatus(record port.PublicAPIAuthRecord, status string) port.PublicAPIAuthRecord {
	record.SourceStatus = status
	return record
}

func withTokenStatus(record port.PublicAPIAuthRecord, status string) port.PublicAPIAuthRecord {
	record.TokenStatus = status
	return record
}

func withSourceExpiresAt(record port.PublicAPIAuthRecord, expiresAt *time.Time) port.PublicAPIAuthRecord {
	record.SourceExpiresAt = expiresAt
	return record
}

func withTokenExpiresAt(record port.PublicAPIAuthRecord, expiresAt *time.Time) port.PublicAPIAuthRecord {
	record.TokenExpiresAt = expiresAt
	return record
}

type authReaderStub struct {
	record    port.PublicAPIAuthRecord
	err       error
	found     bool
	tokenHash string
	touches   []port.PublicAPIAuthLastUsedTouch
}

func (s *authReaderStub) FindPublicAPIAuthTokenByHash(_ context.Context, tokenHash string) (port.PublicAPIAuthRecord, bool, error) {
	s.tokenHash = tokenHash
	return s.record, s.found, s.err
}

func (s *authReaderStub) TouchPublicAPIAuthLastUsed(_ context.Context, touch port.PublicAPIAuthLastUsedTouch) error {
	s.touches = append(s.touches, touch)
	return nil
}

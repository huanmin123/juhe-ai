package modelcheckowner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// TestNewDevAutoLoginAuthorizeFallsBackOnlyWithoutCredentials 锁定
// BUG-0256 的 dev 自动登录回退契约：仅 ErrLoginRequired（完全无凭证）
// 且 source 命中 active 账号时回退；其他错误、source 未命中、source 为
// nil 都原样透传，requireAdmin 面对非 admin 角色必须 ErrForbidden。
func TestNewDevAutoLoginAuthorizeFallsBackOnlyWithoutCredentials(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/run/active", nil)
	// storageFailure 用同一实例：errors.Is 按身份比较，base 返回值与期望值
	// 必须指向同一个错误对象。
	storageFailure := errors.New("read management session: connection refused")
	hitSource := func(role string) DevAutoLoginSource {
		return func(context.Context) (DevAutoLoginAccount, bool) {
			return DevAutoLoginAccount{SystemAccountID: "sys-dev", Role: role}, true
		}
	}
	for _, tc := range []struct {
		name         string
		base         Authorize
		source       DevAutoLoginSource
		requireAdmin bool
		wantID       string
		wantErr      error
	}{
		{
			name:    "base success passes through",
			base:    func(context.Context, *http.Request) (string, error) { return "sys-session", nil },
			source:  hitSource("admin"),
			wantID:  "sys-session",
			wantErr: nil,
		},
		{
			name:         "missing credentials fall back to admin role",
			base:         func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrLoginRequired },
			source:       hitSource("admin"),
			requireAdmin: true,
			wantID:       "sys-dev",
			wantErr:      nil,
		},
		{
			name:         "super admin role satisfies require admin",
			base:         func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrLoginRequired },
			source:       hitSource("super_admin"),
			requireAdmin: true,
			wantID:       "sys-dev",
			wantErr:      nil,
		},
		{
			name:         "user role on admin face is forbidden",
			base:         func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrLoginRequired },
			source:       hitSource("user"),
			requireAdmin: true,
			wantErr:      modelcheckauth.ErrForbidden,
		},
		{
			name:    "expired session never falls back",
			base:    func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrSessionExpired },
			source:  hitSource("admin"),
			wantErr: modelcheckauth.ErrSessionExpired,
		},
		{
			name:    "invalid token never falls back",
			base:    func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrInvalidToken },
			source:  hitSource("admin"),
			wantErr: modelcheckauth.ErrInvalidToken,
		},
		{
			name:    "must change password never falls back",
			base:    func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrMustChange },
			source:  hitSource("admin"),
			wantErr: modelcheckauth.ErrMustChange,
		},
		{
			name:    "source miss keeps original error",
			base:    func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrLoginRequired },
			source:  func(context.Context) (DevAutoLoginAccount, bool) { return DevAutoLoginAccount{}, false },
			wantErr: modelcheckauth.ErrLoginRequired,
		},
		{
			name:    "nil source keeps original error",
			base:    func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrLoginRequired },
			wantErr: modelcheckauth.ErrLoginRequired,
		},
		{
			name:    "self face accepts any role",
			base:    func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrLoginRequired },
			source:  hitSource("user"),
			wantID:  "sys-dev",
			wantErr: nil,
		},
		{
			name:    "storage failure never falls back",
			base:    func(context.Context, *http.Request) (string, error) { return "", storageFailure },
			source:  hitSource("admin"),
			wantErr: storageFailure,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := NewDevAutoLoginAuthorize(tc.base, tc.source, tc.requireAdmin)(context.Background(), request)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v, want %v", err, tc.wantErr)
			}
			if id != tc.wantID {
				t.Fatalf("id=%q, want %q", id, tc.wantID)
			}
		})
	}
}

func TestNewDevAutoLoginAuthorizeDefendsNilBase(t *testing.T) {
	source := func(context.Context) (DevAutoLoginAccount, bool) {
		return DevAutoLoginAccount{SystemAccountID: "sys-dev", Role: "admin"}, true
	}
	_, err := NewDevAutoLoginAuthorize(nil, source, false)(context.Background(), httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if err == nil || err.Error() != "J3b Gateway authenticator is not initialized" {
		t.Fatalf("err=%v, want authenticator not initialized", err)
	}
}

package oauthmgmt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// insertProxyProfile 用测试 schema 的 proxy_profiles 表插入一行代理配置。
func insertProxyProfile(t *testing.T, env *testEnv, id, proxyType, host string, port int64, username, passwordEncrypted string, enabled bool) {
	t.Helper()
	enabledValue := 0
	if enabled {
		enabledValue = 1
	}
	if _, err := env.db.Exec(`INSERT INTO proxy_profiles
		(id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES (?, 'owner', ?, ?, ?, ?, ?, ?, ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		id, id, proxyType, host, port, username, passwordEncrypted, enabledValue); err != nil {
		t.Fatal(err)
	}
}

func requireValidationError(t *testing.T, err error, needle string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want ValidationError containing %q, got nil", needle)
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("want ValidationError, got %T: %v", err, err)
	}
	if !strings.Contains(validation.Message, needle) {
		t.Fatalf("ValidationError message %q does not contain %q", validation.Message, needle)
	}
}

// TestProxyProfileRequestURLBranches 覆盖 resolveRefreshProxyUrlOrThrow 对应物
// 的三个规定分支（未绑定→""；绑定+停用→错误；绑定+完整→含 scheme/host/port 且
// socks5 映射 socks5h），另加协议不支持/密码解密失败两个守卫分支。
func TestProxyProfileRequestURLBranches(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// 未绑定：空 / 空白 profileID → ""，保持现状直连行为。
	for _, id := range []string{"", "   "} {
		proxyURL, err := env.store.proxyProfileRequestURL(ctx, id)
		if err != nil {
			t.Fatalf("unbound proxy id %q: unexpected error %v", id, err)
		}
		if proxyURL != "" {
			t.Fatalf("unbound proxy id %q: got %q, want \"\"", id, proxyURL)
		}
	}

	// 绑定不存在 → 400 家族错误。
	if _, err := env.store.proxyProfileRequestURL(ctx, "proxy-missing"); err == nil {
		t.Fatal("missing proxy profile: want error")
	} else {
		requireValidationError(t, err, "不存在")
	}

	// 绑定+停用 → 错误，不做直连回退。
	insertProxyProfile(t, env, "proxy-disabled", "http", "127.0.0.1", 8080, "", "", false)
	if _, err := env.store.proxyProfileRequestURL(ctx, "proxy-disabled"); err == nil {
		t.Fatal("disabled proxy profile: want error")
	} else {
		requireValidationError(t, err, "已停用")
	}

	// 绑定+完整（socks5 无认证）→ URL 含 scheme/host/port 且 socks5→socks5h。
	insertProxyProfile(t, env, "proxy-socks5", "socks5", "127.0.0.1", 1080, "", "", true)
	proxyURL, err := env.store.proxyProfileRequestURL(ctx, "proxy-socks5")
	if err != nil {
		t.Fatalf("socks5 proxy profile: unexpected error %v", err)
	}
	parsed, parseErr := url.Parse(proxyURL)
	if parseErr != nil {
		t.Fatalf("socks5 proxy profile: invalid url %q: %v", proxyURL, parseErr)
	}
	if parsed.Scheme != "socks5h" {
		t.Fatalf("socks5 proxy profile: scheme = %q, want socks5h", parsed.Scheme)
	}
	if parsed.Host != "127.0.0.1:1080" {
		t.Fatalf("socks5 proxy profile: host = %q, want 127.0.0.1:1080", parsed.Host)
	}

	// 绑定+完整（http 带认证）→ 密码 envelope 解密后注入 UserInfo。
	envelope, err := encryptJSON(testSecret, map[string]any{"password": "proxy-pass"})
	if err != nil {
		t.Fatal(err)
	}
	insertProxyProfile(t, env, "proxy-auth", "http", "10.0.0.1", 3128, "proxy-user", envelope, true)
	proxyURL, err = env.store.proxyProfileRequestURL(ctx, "proxy-auth")
	if err != nil {
		t.Fatalf("auth proxy profile: unexpected error %v", err)
	}
	parsed, parseErr = url.Parse(proxyURL)
	if parseErr != nil {
		t.Fatalf("auth proxy profile: invalid url %q: %v", proxyURL, parseErr)
	}
	if parsed.Scheme != "http" || parsed.Host != "10.0.0.1:3128" {
		t.Fatalf("auth proxy profile: scheme/host = %q/%q", parsed.Scheme, parsed.Host)
	}
	if parsed.Port() != "3128" {
		t.Fatalf("auth proxy profile: port = %q, want 3128", parsed.Port())
	}
	if username := parsed.User.Username(); username != "proxy-user" {
		t.Fatalf("auth proxy profile: username = %q, want proxy-user", username)
	}
	if password, ok := parsed.User.Password(); !ok || password != "proxy-pass" {
		t.Fatalf("auth proxy profile: password = %q/%v, want proxy-pass", password, ok)
	}

	// 协议不支持 → 错误。
	insertProxyProfile(t, env, "proxy-socks6", "socks6", "127.0.0.1", 1080, "", "", true)
	if _, err := env.store.proxyProfileRequestURL(ctx, "proxy-socks6"); err == nil {
		t.Fatal("unsupported proxy scheme: want error")
	} else {
		requireValidationError(t, err, "代理协议不支持")
	}

	// 密码 envelope 解密失败 → 错误。
	insertProxyProfile(t, env, "proxy-bad-envelope", "http", "10.0.0.2", 8080, "proxy-user", "v1:xx:yy:zz", true)
	if _, err := env.store.proxyProfileRequestURL(ctx, "proxy-bad-envelope"); err == nil {
		t.Fatal("undecryptable proxy password: want error")
	} else {
		requireValidationError(t, err, "解密失败")
	}
}

// TestTokenHTTPExchangerProxyWiring 验证 token 传输层的代理接线：空 ProxyURL
// 保持直连现状；非空 ProxyURL 交由 upstreamhttp 构造出站 client，不支持的代理
// 协议直接报错，绝不静默回退直连。
func TestTokenHTTPExchangerProxyWiring(t *testing.T) {
	var hit bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"x"}`))
	}))
	defer server.Close()
	exchanger := NewHTTPTokenExchanger()

	// 空 ProxyURL：默认 client 直连（行为不变式）。
	response, err := exchanger.Do(context.Background(), TokenHTTPRequest{URL: server.URL, Body: "{}"})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("direct path: got (%+v, %v), want 200/nil", response, err)
	}
	if !hit {
		t.Fatal("direct path: upstream was not reached")
	}

	// 非空 ProxyURL + 不支持的协议：出站失败而不是直连。
	if _, err := exchanger.Do(context.Background(), TokenHTTPRequest{
		URL: server.URL, Body: "{}", ProxyURL: "ftp://127.0.0.1:1",
	}); err == nil {
		t.Fatal("proxied path with unsupported scheme: want error, got nil")
	}
}

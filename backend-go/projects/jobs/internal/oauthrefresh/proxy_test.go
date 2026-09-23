package oauthrefresh

// proxy_test.go 覆盖 Store.proxyProfileRequestURL 的分支行为，以及 keepalive
// 与 OpenAI 旋转链路把解析出的代理 URL 透传到 exchanger 收到的
// TokenHTTPRequest.ProxyURL。proxy_profiles 表 DDL 取自 sqlite_schema 业务库
// proxy_profiles 的被查列子集（与包内测试 fixture 风格一致）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

const proxyProfilesFixtureDDL = `
CREATE TABLE proxy_profiles (
	id TEXT PRIMARY KEY,
	type TEXT NOT NULL,
	host TEXT NOT NULL,
	port INTEGER NOT NULL,
	username TEXT,
	password_encrypted TEXT,
	enabled INTEGER NOT NULL DEFAULT 1
)`

type proxyProfileSeed struct {
	ID   string
	Type string
	Host string
	Port int64
	// Username/password 走认证分支；password 以 cryptoTestSecret 封装为
	// {"password": ...} envelope。
	Username string
	Password string
	// Enabled 为 nil 时写入默认启用（1）。
	Enabled *bool
}

func proxyEnabled(value bool) *bool { return &value }

func seedProxyProfile(t *testing.T, db *sql.DB, seed proxyProfileSeed) {
	t.Helper()
	enabled := int64(1)
	if seed.Enabled != nil && !*seed.Enabled {
		enabled = 0
	}
	var sealed string
	if seed.Password != "" {
		encrypted, err := EncryptJSON(cryptoTestSecret, map[string]any{"password": seed.Password})
		if err != nil {
			t.Fatal(err)
		}
		sealed = encrypted
	}
	if _, err := db.Exec(`INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		seed.ID, seed.Type, seed.Host, seed.Port,
		sql.NullString{String: seed.Username, Valid: seed.Username != ""},
		sql.NullString{String: sealed, Valid: sealed != ""},
		enabled); err != nil {
		t.Fatal(err)
	}
}

func TestProxyProfileRequestURLArms(t *testing.T) {
	store, db, _ := newTestStore(t)
	if _, err := db.Exec(proxyProfilesFixtureDDL); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 未绑定：不查库，返回空串保持既有直连行为。
	if url, err := store.proxyProfileRequestURL(ctx, ""); err != nil || url != "" {
		t.Fatalf("unbound url=%q err=%v", url, err)
	}

	// socks5 升级 socks5h（远端解析）并带认证。
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-socks", Type: "SOCKS5", Host: "proxy.example", Port: 1080, Username: "alice", Password: "secret"})
	if url, err := store.proxyProfileRequestURL(ctx, "pp-socks"); err != nil || url != "socks5h://alice:secret@proxy.example:1080" {
		t.Fatalf("socks url=%q err=%v", url, err)
	}

	// http 带认证。
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-http", Type: "http", Host: "proxy.example", Port: 8080, Username: "bob", Password: "pw"})
	if url, err := store.proxyProfileRequestURL(ctx, "pp-http"); err != nil || url != "http://bob:pw@proxy.example:8080" {
		t.Fatalf("http url=%q err=%v", url, err)
	}

	// 无认证 https 直出。
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-https", Type: "HTTPS", Host: "proxy.example", Port: 443})
	if url, err := store.proxyProfileRequestURL(ctx, "pp-https"); err != nil || url != "https://proxy.example:443" {
		t.Fatalf("https url=%q err=%v", url, err)
	}

	// 配置不存在。
	if _, err := store.proxyProfileRequestURL(ctx, "pp-missing"); err == nil || !IsLocalConfigurationError(err) || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing err=%v", err)
	}

	// 已停用：报错，不直连回退。
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-off", Type: "http", Host: "proxy.example", Port: 8080, Enabled: proxyEnabled(false)})
	if _, err := store.proxyProfileRequestURL(ctx, "pp-off"); err == nil || !IsLocalConfigurationError(err) || !strings.Contains(err.Error(), "停用") {
		t.Fatalf("disabled err=%v", err)
	}

	// 协议不支持。
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-bad-scheme", Type: "ftp", Host: "proxy.example", Port: 21})
	if _, err := store.proxyProfileRequestURL(ctx, "pp-bad-scheme"); err == nil || !strings.Contains(err.Error(), "协议不支持") {
		t.Fatalf("scheme err=%v", err)
	}

	// 端口越界。
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-bad-port", Type: "http", Host: "proxy.example", Port: 70000})
	if _, err := store.proxyProfileRequestURL(ctx, "pp-bad-port"); err == nil || !strings.Contains(err.Error(), "host/port 无效") {
		t.Fatalf("port err=%v", err)
	}

	// 有用户名但缺密码。
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-no-password", Type: "http", Host: "proxy.example", Port: 8080, Username: "carol"})
	if _, err := store.proxyProfileRequestURL(ctx, "pp-no-password"); err == nil || !strings.Contains(err.Error(), "缺少密码") {
		t.Fatalf("no password err=%v", err)
	}

	// 密码 envelope 损坏 → 解密失败。
	if _, err := db.Exec(`UPDATE proxy_profiles SET password_encrypted = 'v1:broken' WHERE id = 'pp-no-password'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.proxyProfileRequestURL(ctx, "pp-no-password"); err == nil || !strings.Contains(err.Error(), "解密失败") {
		t.Fatalf("decrypt err=%v", err)
	}
}

func TestKeepalivePassesProxyURLToTokenExchange(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	if _, err := db.Exec(proxyProfilesFixtureDDL); err != nil {
		t.Fatal(err)
	}
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-keepalive", Type: "socks5", Host: "proxy.example", Port: 1080, Username: "alice", Password: "secret"})
	seedAccountRow(t, db, accountRowSeed{ID: "an-proxy", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id = 'pp-keepalive' WHERE id = 'an-proxy'`); err != nil {
		t.Fatal(err)
	}
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a2","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	if got := exchanger.lastRequest().ProxyURL; got != "socks5h://alice:secret@proxy.example:1080" {
		t.Fatalf("exchanger proxyURL=%q", got)
	}
}

func TestKeepaliveInvalidProxyProfileFailsWithoutDirectDial(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	if _, err := db.Exec(proxyProfilesFixtureDDL); err != nil {
		t.Fatal(err)
	}
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-off", Type: "http", Host: "proxy.example", Port: 8080, Enabled: proxyEnabled(false)})
	seedAccountRow(t, db, accountRowSeed{ID: "an-proxy-off", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id = 'pp-off' WHERE id = 'an-proxy-off'`); err != nil {
		t.Fatal(err)
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 0 || result.Failed != 1 {
		t.Fatalf("result=%+v", result)
	}
	if exchanger.callCount() != 0 {
		t.Fatalf("停用代理必须在上游调用前失败，calls=%d", exchanger.callCount())
	}
}

func TestRefreshJobPassesProxyURLToTokenExchange(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	if _, err := db.Exec(proxyProfilesFixtureDDL); err != nil {
		t.Fatal(err)
	}
	seedProxyProfile(t, db, proxyProfileSeed{ID: "pp-openai", Type: "http", Host: "proxy.example", Port: 8080, Username: "bob", Password: "pw"})
	seedOpenAIOAuthAccount(t, db, "acc-proxy", openAICredentials(expiresInMillis(60_000)), clock.Now())
	if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id = 'pp-openai' WHERE id = 'acc-proxy'`); err != nil {
		t.Fatal(err)
	}
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	if got := exchanger.lastRequest().ProxyURL; got != "http://bob:pw@proxy.example:8080" {
		t.Fatalf("exchanger proxyURL=%q", got)
	}
}

func TestRefreshJobMissingProxyProfileIsLocalConfigurationFailure(t *testing.T) {
	job, store, db, clock, exchanger := newRefreshJobForTest(t)
	if _, err := db.Exec(proxyProfilesFixtureDDL); err != nil {
		t.Fatal(err)
	}
	seedOpenAIOAuthAccount(t, db, "acc-proxy-missing", openAICredentials(expiresInMillis(0)), clock.Now())
	if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id = 'pp-gone' WHERE id = 'acc-proxy-missing'`); err != nil {
		t.Fatal(err)
	}
	account, err := store.FindRotationAccount(context.Background(), "acc-proxy-missing")
	if err != nil || account == nil {
		t.Fatalf("find=%v err=%v", account, err)
	}
	if _, err := job.RefreshAccount(context.Background(), account, RefreshAccountOptions{}); err == nil ||
		!IsLocalConfigurationError(err) || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing proxy err=%v", err)
	}
	if exchanger.callCount() != 0 {
		t.Fatalf("代理解析失败不得发起上游调用，calls=%d", exchanger.callCount())
	}
}

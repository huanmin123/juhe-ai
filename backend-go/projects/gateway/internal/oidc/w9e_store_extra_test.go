package oidc

// w9e 覆盖率战役第二波：直达 store/crypto 的损坏行与构造载荷分支。
// 均基于 mock SQLite env 与本地构造密文，不触网络。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW9EOIDCTransactionRowValidation(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	now := isoMillis(env.clock.Now())

	_ = ctx
	craft := func(state, csrf string) string {
		ciphertext, err := EncryptOidcValue(oidcTestSecret, map[string]string{"state": state, "csrfToken": csrf})
		if err != nil {
			t.Fatal(err)
		}
		return ciphertext
	}
	// 空 state/csrf → 内容无效。
	mustExec(t, env.db, `INSERT INTO oauth_authorization_transactions (id, client_id, redirect_uri, scopes_json, state_ciphertext, code_challenge, csrf_hash, expires_at, created_at)
		VALUES ('tx-empty', ?, ?, '["openid"]', ?, 'challenge', 'hash', ?, ?)`,
		env.publicID, env.publicRedirect, craft("", "csrf"), isoMillis(env.clock.Now().Add(time.Minute)), now)
	if _, err := env.store.FindAuthorizationTransaction(ctx, "tx-empty"); err == nil || !strings.Contains(err.Error(), "授权事务内容无效") {
		t.Fatalf("空 state = %v", err)
	}
	// 坏 expires → requiredTimestamp 失败。
	validCipher := craft("s", "csrf")
	mustExec(t, env.db, `INSERT INTO oauth_authorization_transactions (id, client_id, redirect_uri, scopes_json, state_ciphertext, code_challenge, csrf_hash, expires_at, created_at)
		VALUES ('tx-badts', ?, ?, '["openid"]', ?, 'challenge', 'hash', 'not-a-time', ?)`,
		env.publicID, env.publicRedirect, validCipher, now)
	if _, err := env.store.FindAuthorizationTransaction(ctx, "tx-badts"); err == nil {
		t.Fatal("坏 expires 必须失败")
	}
}

func TestW9EOIDCIssueAccessTokenGrantFences(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	// 未知 grant。
	if _, err := env.store.issueAccessTokenInTransaction(ctx, env.store.db, "grant-ghost", env.publicID, isoMillis(env.clock.Now())); err == nil || !strings.Contains(err.Error(), "OAuth grant 已失效") {
		t.Fatalf("未知 grant = %v", err)
	}
	// grant 存在但 client 不符。
	env.insertGrant(t, "grant-owner", env.clock.Now().Add(time.Hour))
	if _, err := env.store.issueAccessTokenInTransaction(ctx, env.store.db, "grant-owner", env.confID, isoMillis(env.clock.Now())); err == nil || !strings.Contains(err.Error(), "OAuth grant 已失效") {
		t.Fatalf("client 不符 = %v", err)
	}
	// grant 已过期。
	env.insertGrant(t, "grant-expired", env.clock.Now().Add(-time.Minute))
	if _, err := env.store.issueAccessTokenInTransaction(ctx, env.store.db, "grant-expired", env.publicID, isoMillis(env.clock.Now())); err == nil || !strings.Contains(err.Error(), "OAuth grant 已失效") {
		t.Fatalf("过期 grant = %v", err)
	}
	// 合法 grant → 签发。
	issued, err := env.store.issueAccessTokenInTransaction(ctx, env.store.db, "grant-owner", env.publicID, isoMillis(env.clock.Now()))
	if err != nil || issued == nil || issued.AccessToken == "" {
		t.Fatalf("合法签发 = %v err=%v", issued, err)
	}
}

func TestW9EOIDCLoadSigningPrivateKeyPayloads(t *testing.T) {
	// 空 payload。
	empty, err := EncryptOidcValue(oidcTestSecret, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningPrivateKey(oidcTestSecret, empty); err == nil || !strings.Contains(err.Error(), "签名私钥内容无效") {
		t.Fatalf("空 payload = %v", err)
	}
	// 非 PEM 内容。
	notPem, err := EncryptOidcValue(oidcTestSecret, map[string]string{"privateKeyPem": "definitely not pem"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningPrivateKey(oidcTestSecret, notPem); err == nil {
		t.Fatal("非 PEM 必须失败")
	}
	// PEM 头但内容损坏。
	broken := strings.Join([]string{"-----BEGIN PRIVATE KEY-----", "Zm9v", "-----END PRIVATE KEY-----"}, "\n")
	brokenPem, err := EncryptOidcValue(oidcTestSecret, map[string]string{"privateKeyPem": broken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningPrivateKey(oidcTestSecret, brokenPem); err == nil {
		t.Fatal("损坏 PKCS8 必须失败")
	}
	// 非 RSA 私钥（EC）。
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	ecPem, err := EncryptOidcValue(oidcTestSecret, map[string]string{"privateKeyPem": ecPemText})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningPrivateKey(oidcTestSecret, ecPem); err == nil {
		t.Fatal("非 RSA 私钥必须失败")
	}
}

func TestW9EOIDCSubjectDerivationGuard(t *testing.T) {
	if _, err := OidcSubjectForSystemAccount("", "issuer", "acc"); err == nil {
		t.Fatal("空 secret 必须失败")
	}
	if _, err := OidcSubjectForSystemAccount("secret", "", "acc"); err == nil {
		t.Fatal("空 issuer 必须失败")
	}
	if got, err := OidcSubjectForSystemAccount("secret", "issuer", "acc"); err != nil || got == "" {
		t.Fatalf("合法派生 = %q err=%v", got, err)
	}
}

func TestW9EOIDCJwksCorruptRowSkipped(t *testing.T) {
	env := newStoreEnv(t)
	env.seedSigningKey(t, "kid-bad", env.clock.Now())
	mustExec(t, env.db, `UPDATE oauth_signing_keys SET public_jwk_json='{not-json' WHERE kid='kid-bad'`)
	keys, err := env.store.ListSigningJwks(context.Background())
	if err != nil {
		t.Fatalf("损坏 jwk 行应被降级而不是失败: %v", err)
	}
	for _, key := range keys {
		if key["kid"] == "kid-bad" {
			t.Fatal("损坏 jwk 不应进入输出")
		}
	}
}

func httptestNewRequestWithBasic(id, secret string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":"+secret)))
	return r
}

type w9eDepsHolder struct{}

func (env *oidcStoreEnv) depsNil() *Deps {
	return &Deps{Store: env.store}
}

func TestW9EOIDCAuthenticateClientIDMismatch(t *testing.T) {
	env := newStoreEnv(t)
	client, err := env.store.FindClient(context.Background(), env.confID)
	if err != nil || client == nil {
		t.Fatal(err)
	}
	r := httptestNewRequestWithBasic("wrong-id", env.confSecret)
	if env.depsNil().authenticateClient(r, client) {
		t.Fatal("id 不符应认证失败")
	}
	// 正确 id + 正确 secret。
	good := httptestNewRequestWithBasic(env.confID, env.confSecret)
	if !env.depsNil().authenticateClient(good, client) {
		t.Fatal("正确凭据应通过")
	}
	// public 客户端带 Authorization 头 → 失败。
	publicClient, err := env.store.FindClient(context.Background(), env.publicID)
	if err != nil || publicClient == nil {
		t.Fatal(err)
	}
	if env.depsNil().authenticateClient(good, publicClient) {
		t.Fatal("public 客户端不应接受 Basic 头")
	}
}

func TestW9EOIDCParseCookieHeaderSkipsJunk(t *testing.T) {
	got := parseCookieHeader(" ; broken ; a=1; b=2=3")
	if got["a"] != "1" || got["b"] != "2=3" {
		t.Fatalf("parseCookieHeader = %v", got)
	}
	if _, ok := got["broken"]; ok {
		t.Fatal("无值片段应跳过")
	}
}

func TestW9EOIDCTokenMalformedFormBody(t *testing.T) {
	env := newRouteEnv(t)
	rec := env.do(t, http.MethodPost, "/oauth/token", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, "%zz")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("坏编码表单 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9EOIDCRedirectMatcherLoopbackGaps(t *testing.T) {
	registered := []string{"::not-a-url::", "http://127.0.0.1:7777/callback?x=1"}
	if !matchesRegisteredRedirectUri(registered, "http://127.0.0.1:7777/callback?x=1") {
		t.Fatal("带 query 的回环应通过 host/path/query 匹配")
	}
	if matchesRegisteredRedirectUri(registered, "http://127.0.0.1:7777/other") {
		t.Fatal("不同 path 不应通过")
	}
	if matchesRegisteredRedirectUri([]string{"https://a.example/c"}, "http://[::1]:1/x") {
		t.Fatal("未注册回环不应通过")
	}
}

func TestW9EOIDCTokenExchangeCorruptNonceAndKey(t *testing.T) {
	env := newRouteEnv(t)
	code := env.seedAuthorizationCode(t, env.publicID, env.accountID, []string{"openid", "profile"},
		env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "n-1", time.Hour, time.Hour)
	// 损坏 nonce 密文 → 交换失败（500）。
	mustExec(t, env.db, `UPDATE oauth_authorization_code_oidc_contexts SET nonce_ciphertext='bad.bad.bad'`)
	rec := env.postForm(t, "/oauth/token", map[string]string{
		"grant_type": "authorization_code", "client_id": env.publicID, "code": code, "redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
	}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("损坏 nonce 交换 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9EOIDCTokenExchangeCorruptKeyOpenIDReturnsUnavailable(t *testing.T) {
	env := newRouteEnv(t)
	code := env.seedAuthorizationCode(t, env.publicID, env.accountID, []string{"openid", "profile"},
		env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "n-1", time.Hour, time.Hour)
	// 私钥密文损坏 → openid 交换的签名预检失败 → 503。
	mustExec(t, env.db, `UPDATE oauth_signing_keys SET private_key_ciphertext='corrupt.corrupt.corrupt'`)
	rec := env.postForm(t, "/oauth/token", map[string]string{
		"grant_type": "authorization_code", "client_id": env.publicID, "code": code, "redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
	}, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("损坏密钥 openid 交换 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9EOIDCSessionRowWithBadTimestamp(t *testing.T) {
	env := newStoreEnv(t)
	// 坏 last_seen_at → 会话按无效处理（nil, nil），不暴露错误。
	mustExec(t, env.db, `UPDATE system_sessions SET last_seen_at='garbage' WHERE token_hash=?`, hashSecret(env.sessionToken))
	session, err := env.store.FindSessionByToken(context.Background(), env.sessionToken)
	if err != nil || session != nil {
		t.Fatalf("坏 last_seen_at 应判定会话无效, session=%v err=%v", session, err)
	}
	// 空的 last_seen_at 同样按无效处理。
	mustExec(t, env.db, `INSERT INTO system_sessions (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
		VALUES ('sess-empty', ?, ?, ?, ?, '')`, env.accountID, hashSecret("tok-empty"),
		isoMillis(env.clock.Now().Add(time.Hour)), isoMillis(env.clock.Now()))
	session2, err := env.store.FindSessionByToken(context.Background(), "tok-empty")
	if err != nil || session2 != nil {
		t.Fatalf("空 last_seen_at 应判定会话无效, session=%v err=%v", session2, err)
	}
}

func TestW9EOIDCSignIDTokenWithCorruptKey(t *testing.T) {
	if _, err := SignIDToken(oidcTestSecret, "corrupt.corrupt.corrupt", "kid", "issuer", "aud", "sub", 100, "", time.Now()); err == nil {
		t.Fatal("损坏密钥签名必须失败")
	}
}

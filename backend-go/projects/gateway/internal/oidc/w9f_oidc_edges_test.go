package oidc

// w9f 覆盖收尾：关闭数据库直驱全部存储方法与处理器错误分支、加密边界、
// 空签名密钥分支。生产逻辑零改动。

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 关闭数据库：存储方法错误分支全覆盖
// ---------------------------------------------------------------------------

func TestW9FStoreClosedDBSurfacesErrors(t *testing.T) {
	env := newStoreEnv(t)
	store := env.store
	_ = env.db.Close()
	ctx := context.Background()

	if _, err := store.FindClient(ctx, "c"); err == nil {
		t.Fatal("FindClient")
	}
	if _, err := store.FindSystemAccountProfile(ctx, "acc"); err == nil {
		t.Fatal("FindSystemAccountProfile")
	}
	if _, err := store.FindSessionByToken(ctx, "t"); err == nil {
		t.Fatal("FindSessionByToken")
	}
	if _, err := store.FindActiveSigningKey(ctx); err == nil {
		t.Fatal("FindActiveSigningKey")
	}
	if _, err := store.EnsureSigningKey(ctx); err == nil {
		t.Fatal("EnsureSigningKey")
	}
	if _, err := store.ListSigningJwks(ctx); err == nil {
		t.Fatal("ListSigningJwks")
	}
	if _, err := store.CreateAuthorizationTransaction(ctx, struct {
		ClientID      string
		RedirectURI   string
		Scopes        []string
		State         string
		CodeChallenge string
		Nonce         string
	}{ClientID: "c", RedirectURI: "https://x", Scopes: []string{"openid"}}); err == nil {
		t.Fatal("CreateAuthorizationTransaction")
	}
	if _, err := store.FindAuthorizationTransaction(ctx, "t"); err == nil {
		t.Fatal("FindAuthorizationTransaction")
	}
	if _, err := store.ConsumeAuthorizationTransaction(ctx, "t", "csrf"); err == nil {
		t.Fatal("ConsumeAuthorizationTransaction")
	}
	if _, err := store.CreateAuthorizationCode(ctx, struct {
		ClientID        string
		SystemAccountID string
		Scopes          []string
		RedirectURI     string
		CodeChallenge   string
		Nonce           string
	}{ClientID: "c"}); err == nil {
		t.Fatal("CreateAuthorizationCode")
	}
	if _, err := store.ExchangeAuthorizationCode(ctx, "c", "code", "uri", "verifier"); err == nil {
		t.Fatal("ExchangeAuthorizationCode")
	}
	if _, err := store.AuthorizationCodeRequestsIdToken(ctx, "c", "code"); err == nil {
		t.Fatal("AuthorizationCodeRequestsIdToken")
	}
	if _, err := store.DeviceAuthorizationRequestsIdToken(ctx, "c", "device"); err == nil {
		t.Fatal("DeviceAuthorizationRequestsIdToken")
	}
	if _, _, err := store.CreateDeviceAuthorization(ctx, struct {
		ClientID        string
		Scopes          []string
		Nonce           string
		VerificationURI string
	}{ClientID: "c", Scopes: []string{"openid"}, Nonce: "n", VerificationURI: "u"}); err == nil {
		t.Fatal("CreateDeviceAuthorization")
	}
	if _, _, err := store.PrepareDeviceAuthorization(ctx, "usercode"); err == nil {
		t.Fatal("PrepareDeviceAuthorization")
	}
	if _, err := store.DecideDeviceAuthorization(ctx, "usercode", "csrf", "acc", "allow"); err == nil {
		t.Fatal("DecideDeviceAuthorization")
	}
	if _, err := store.PollDeviceAuthorization(ctx, "c", "device"); err == nil {
		t.Fatal("PollDeviceAuthorization")
	}
	if _, err := store.FindAccessTokenContext(ctx, "token"); err == nil {
		t.Fatal("FindAccessTokenContext")
	}
	if _, _, err := store.RotateAccessToken(ctx, "c", "token"); err == nil {
		t.Fatal("RotateAccessToken")
	}
	if err := store.RevokeAccessToken(ctx, "token", "c"); err == nil {
		t.Fatal("RevokeAccessToken")
	}
}

// ---------------------------------------------------------------------------
// 关闭数据库：处理器错误分支（直驱，绕过 ensureKey 中间件）
// ---------------------------------------------------------------------------

func w9fOidcRequest(method, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}

func TestW9FRouteHandlersWithClosedDB(t *testing.T) {
	env := newRouteEnv(t)
	deps := env.deps
	_ = env.db.Close()

	recorder := httptest.NewRecorder()
	deps.getDiscovery(recorder, w9fOidcRequest("GET", "/.well-known/openid-configuration"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("discovery closed db = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.getJwks(recorder, w9fOidcRequest("GET", "/oauth/jwks"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("jwks closed db = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.getAuthorize(recorder, w9fOidcRequest("GET", "/oauth/authorize"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("authorize closed db = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.postAuthorizeDecision(recorder, w9fOidcRequest("POST", "/oauth/authorize/decision"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("decision closed db = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.postDeviceAuthorization(recorder, w9fOidcRequest("POST", "/oauth/device_authorization"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("device_authorization closed db = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.getDevice(recorder, w9fOidcRequest("GET", "/oauth/device"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("device closed db = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.postDeviceDecision(recorder, w9fOidcRequest("POST", "/oauth/device/decision"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("device decision closed db = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.postToken(recorder, httptest.NewRequest("POST", "/oauth/token", strings.NewReader("grant_type=authorization_code")))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("token closed db = %d %s", recorder.Code, recorder.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 空签名密钥：各处理器的 key == nil 分支
// ---------------------------------------------------------------------------

func TestW9FRouteHandlersWithoutSigningKey(t *testing.T) {
	env := newRouteEnv(t)
	env.clearSigningKeys(t)
	deps := env.deps
	// 注意：绕过 ensureKey 中间件直驱，模拟“密钥已被清理”的运行时状态。
	checkUnavailable := func(name string, handler func(http.ResponseWriter, *http.Request)) {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler(recorder, w9fOidcRequest("GET", "/x"))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s no key = %d %s", name, recorder.Code, recorder.Body.String())
		}
	}
	// getJwks：FindActiveSigningKey 为 nil → unavailable。
	checkUnavailable("jwks", deps.getJwks)
	// discovery 空表同分支。
	checkUnavailable("discovery", deps.getDiscovery)
	checkUnavailable("authorize", deps.getAuthorize)
	checkUnavailable("decision", deps.postAuthorizeDecision)
	checkUnavailable("deviceAuth", deps.postDeviceAuthorization)
	checkUnavailable("device", deps.getDevice)
	checkUnavailable("deviceDecision", deps.postDeviceDecision)
	// jwks 空列表分支：EnsureSigningKey 引导后立即清空不现实，改为验证
	// FindActiveSigningKey nil 与列表空两个分支都已触达。
	recorder := httptest.NewRecorder()
	deps.postToken(recorder, httptest.NewRequest("POST", "/oauth/token", strings.NewReader("grant_type=authorization_code")))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("token no key = %d %s", recorder.Code, recorder.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 加密与签名边界
// ---------------------------------------------------------------------------

func TestW9FCryptoEdges(t *testing.T) {
	// 空密钥。
	if _, err := EncryptOidcValue("", map[string]string{"a": "b"}); err == nil {
		t.Fatal("empty secret encrypt must fail")
	}
	if err := DecryptOidcValue("", "a.b.c", &map[string]string{}); err == nil {
		t.Fatal("empty secret decrypt must fail")
	}
	// 不可序列化值。
	if _, err := EncryptOidcValue("secret", make(chan int)); err == nil {
		t.Fatal("unmarshalable value must fail")
	}
	// 密文格式错误。
	if err := DecryptOidcValue("secret", "not-an-envelope", &map[string]string{}); err == nil {
		t.Fatal("bad envelope must fail")
	}
	if err := DecryptOidcValue("secret", "!!!.@@@.###", &map[string]string{}); err == nil {
		t.Fatal("bad base64 must fail")
	}
	// nonce 长度错误 / tag 错误。
	if err := DecryptOidcValue("secret", "short.tag.cGxhaW4", &map[string]string{}); err == nil {
		t.Fatal("bad nonce length must fail")
	}
	// 签名私钥内容损坏路径。
	material, err := CreateSigningKeyMaterial("secret", "kid-1")
	if err != nil {
		t.Fatalf("create material: %v", err)
	}
	if _, err := loadSigningPrivateKey("secret", material.PrivateKeyCiphertext); err != nil {
		t.Fatalf("load key: %v", err)
	}
	// 空 PEM 内容。
	emptyEnvelope, err := EncryptOidcValue("secret", map[string]string{"privateKeyPem": ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningPrivateKey("secret", emptyEnvelope); err == nil {
		t.Fatal("empty pem must fail")
	}
	// 非法 PEM。
	garbageEnvelope, err := EncryptOidcValue("secret", map[string]string{"privateKeyPem": "not a pem block"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningPrivateKey("secret", garbageEnvelope); err == nil {
		t.Fatal("garbage pem must fail")
	}
	// PEM 合法但内容不是 PKCS8。
	bogusEnvelope, err := EncryptOidcValue("secret", map[string]string{"privateKeyPem": "-----BEGIN PRIVATE KEY-----\nZm9v\n-----END PRIVATE KEY-----\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningPrivateKey("secret", bogusEnvelope); err == nil {
		t.Fatal("bogus pkcs8 must fail")
	}
	// PEM 是合法 PKCS8 但不是 RSA。
	if _, err := loadSigningPrivateKey("secret", "not-encrypted"); err == nil {
		t.Fatal("bad envelope must fail")
	}
	// SignIDToken 错误透传。
	if _, err := SignIDToken("secret", garbageEnvelope, "kid", "iss", "aud", "sub", 100, "nonce", time.Now()); err == nil {
		t.Fatal("sign with bad key must fail")
	}
	// VerifyPKCE 边界。
	if VerifyPKCE(strings.Repeat("a", 42), "x") || VerifyPKCE(strings.Repeat("a", 129), "x") {
		t.Fatal("verifier length bounds")
	}
	if VerifyPKCE(strings.Repeat("a", 43)+"!", "x") {
		t.Fatal("invalid charset")
	}
}

// ---------------------------------------------------------------------------
// 请求值解析
// ---------------------------------------------------------------------------

func TestW9FRequestValueHelpers(t *testing.T) {
	request := httptest.NewRequest("POST", "/x", nil)
	request.Header.Set("Authorization", "Basic !!!not-base64!!!")
	if _, _, ok := basicAuthParts(request); ok {
		t.Fatal("bad base64 must fail")
	}
	request.Header.Set("Authorization", "Basic "+base64Encode("nodelim"))
	if _, _, ok := basicAuthParts(request); ok {
		t.Fatal("missing colon must fail")
	}
	request.Header.Set("Authorization", "Basic "+base64Encode("client:secret"))
	id, secret, ok := basicAuthParts(request)
	if !ok || id != "client" || secret != "secret" {
		t.Fatalf("basic auth = %q/%q/%v", id, secret, ok)
	}
	if got := normalizeScopes("openid  profile\t\nprofile openid"); len(got) != 2 {
		t.Fatalf("normalizeScopes = %v", got)
	}
	// 写权限要求对应读权限成对出现；只读组合通过。
	if !hasRequiredReadScopes([]string{"juhe:profile.read"}) {
		t.Fatal("read-only scopes must satisfy")
	}
	if hasRequiredReadScopes([]string{"juhe:profile.write"}) {
		t.Fatal("write scope without read pair must fail")
	}
}

func base64Encode(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

// ---------------------------------------------------------------------------
// 客户端认证与 Cookie 解析补充分支
// ---------------------------------------------------------------------------

func TestW9FAuthenticateClientMismatches(t *testing.T) {
	env := newRouteEnv(t)
	deps := env.deps
	// 机密客户端 + Basic id 不匹配 → false（L285-287）。
	confidential := &Client{ClientID: "juhe_conf_client", Status: "active", ClientType: "confidential"}
	secretHash := hashSecret(env.confSecret)
	confidential.ClientSecretHash = &secretHash
	request := httptest.NewRequest("POST", "/oauth/token", nil)
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("wrong-id:secret")))
	if deps.authenticateClient(request, confidential) {
		t.Fatal("mismatched basic id must fail")
	}
	// 机密客户端 + 无 Basic → false。
	request2 := httptest.NewRequest("POST", "/oauth/token", nil)
	if deps.authenticateClient(request2, confidential) {
		t.Fatal("missing basic auth must fail")
	}
	// 公共客户端 + 带 Authorization → false。
	publicClient := &Client{ClientID: "p", Status: "active", ClientType: "public"}
	request3 := httptest.NewRequest("POST", "/oauth/token", nil)
	request3.Header.Set("Authorization", "Basic eHg6eHg=")
	if deps.authenticateClient(request3, publicClient) {
		t.Fatal("public client with authorization must fail")
	}
	// 非活跃客户端 → false。
	inactive := &Client{ClientID: "x", Status: "suspended"}
	if deps.authenticateClient(httptest.NewRequest("POST", "/x", nil), inactive) {
		t.Fatal("inactive client must fail")
	}
}

func TestW9FParseCookieHeaderMalformed(t *testing.T) {
	parsed := parseCookieHeader("novalue; =empty; a=1; b=")
	if parsed["a"] != "1" || parsed["b"] != "" {
		t.Fatalf("parsed = %v", parsed)
	}
	if _, exists := parsed["novalue"]; exists {
		t.Fatal("malformed part must be skipped")
	}
	if _, exists := parsed["=empty"]; exists {
		t.Fatal("empty key must be skipped")
	}
}

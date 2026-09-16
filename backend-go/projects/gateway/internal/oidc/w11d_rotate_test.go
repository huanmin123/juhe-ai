package oidc

// w11d 覆盖补齐（三）：令牌续期成功链（72 小时资格 + 轮换落库）、
// EnsureSigningKey 活跃新鲜分支、决策期 FindClient 错误、兑换期 OIDC 上下文
// 错误、损坏签名密钥的设备轮询 preflight。

import (
	"net/http"
	"testing"
	"time"
)

func TestW11DOidcTokenRenewSuccessChain(t *testing.T) {
	env := newRouteEnv(t)
	code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
	payload := env.exchangeCode(t, code)
	accessToken := stringField(t, payload, "access_token")

	// 未满 72 小时 → not_eligible。
	rec := env.postForm(t, "/oauth/token/renew", map[string]string{
		"current_access_token": accessToken, "client_id": env.publicID,
	}, nil)
	assertOAuthError(t, rec, http.StatusBadRequest, "token_renewal_not_eligible", "当前令牌签发未满 72 小时")

	// 推进 73 小时 → 续期成功（轮换全链）。
	env.clock.Advance(73 * time.Hour)
	rec = env.postForm(t, "/oauth/token/renew", map[string]string{
		"current_access_token": accessToken, "client_id": env.publicID,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("续期 = %d %s", rec.Code, rec.Body.String())
	}
	renewed := decodeJSONBody(t, rec)
	newToken := stringField(t, renewed, "access_token")
	if newToken == "" || newToken == accessToken {
		t.Fatalf("续期令牌 = %q", newToken)
	}
	// 旧令牌再次续期 → invalid_token（轮换后不可复用）。
	rec = env.postForm(t, "/oauth/token/renew", map[string]string{
		"current_access_token": accessToken, "client_id": env.publicID,
	}, nil)
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_token", "令牌无效或授权已到期")
}

func TestW11DOidcEnsureSigningKeyFreshActiveReturn(t *testing.T) {
	env := newStoreEnv(t)
	// 活跃且在 7 天轮换窗内 → 原样返回（不进入轮换）。
	env.seedSigningKey(t, "kid-fresh", env.clock.Now().Add(-time.Hour))
	key, err := env.store.EnsureSigningKey(t.Context())
	if err != nil || key == nil || key.Kid != "kid-fresh" {
		t.Fatalf("新鲜活跃 key = %+v err=%v", key, err)
	}
}

func TestW11DOidcDecisionFindClientError(t *testing.T) {
	env := newRouteEnv(t)
	transactionID, _ := env.startAuthorize(t, env.authorizeQuery(nil))
	w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_clients`)
	rec := env.get(t, "/oauth/authorize?transaction_id="+urlQueryEscape(transactionID), sessionCookie(env.sessionToken))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("恢复授权 FindClient 错误 = %d", rec.Code)
	}
}

func TestW11DOidcExchangeOidcContextError(t *testing.T) {
	env := newRouteEnv(t)
	code := env.completeAuthorize(t, env.authorizeQuery(nil))
	// 兑换前丢弃 OIDC 上下文表 → AuthorizationCodeRequestsIdToken 错误。
	w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_authorization_code_oidc_contexts`)
	rec := env.postForm(t, "/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": code,
		"redirect_uri":  env.publicRedirect,
		"code_verifier": pkceTestVerifier,
		"client_id":     env.publicID,
	}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("兑换 OIDC 上下文错误 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW11DOidcDevicePollPreflightError(t *testing.T) {
	env := newRouteEnv(t)
	deviceCode := env.approveDevice(t, "openid profile juhe:profile.read", "w11d-nonce")
	// 损坏活跃签名密钥密文 → signingPreflight 失败 → 503。
	w11dExec(t, env.oidcStoreEnv, `UPDATE oauth_signing_keys SET private_key_ciphertext = 'not-v1-ciphertext' WHERE status = 'active'`)
	rec := env.postForm(t, "/oauth/token", map[string]string{
		"client_id": env.publicID, "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": deviceCode,
	}, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("损坏密钥设备轮询 = %d %s", rec.Code, rec.Body.String())
	}
}

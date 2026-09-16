package oidc

// w11d 覆盖补齐（五）：postToken 兑换期 requestsIDToken 错误、令牌签发
// 直调错误臂、空 issuer 的 userinfo subject 错误、触发器驱动的密钥轮换
// UPDATE/INSERT 失败臂。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestW11DOidcExchangeRequestsIDTokenError(t *testing.T) {
	env := newRouteEnv(t)
	// 有效客户端 + 授权码表丢弃 → AuthorizationCodeRequestsIdToken 错误。
	w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_authorization_codes`)
	rec := env.postForm(t, "/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "any-code",
		"redirect_uri":  env.publicRedirect,
		"code_verifier": pkceTestVerifier,
		"client_id":     env.publicID,
	}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("requestsIDToken 错误 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW11DOidcIssueTokenDirectArms(t *testing.T) {
	t.Run("grant-query-error", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dExec(t, env, `DROP TABLE oauth_grants`)
		if _, err := env.store.issueAccessTokenInTransaction(context.Background(), env.db, "g1", env.publicID, iso(env.clock.Now())); err == nil {
			t.Fatal("grants 表缺失必须报错")
		}
	})
	t.Run("grant-bad-expires", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dExec(t, env, `INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
			VALUES ('g_bad', ?, ?, '["openid"]', 'not-a-time', NULL, ?)`, env.publicID, env.accountID, iso(env.clock.Now()))
		if _, err := env.store.issueAccessTokenInTransaction(context.Background(), env.db, "g_bad", env.publicID, iso(env.clock.Now())); err == nil {
			t.Fatal("坏 grant expires 必须报错")
		}
	})
	t.Run("insert-token-error", func(t *testing.T) {
		env := newStoreEnv(t)
		env.insertGrant(t, "g_ok", env.clock.Now().Add(time.Hour))
		w11dExec(t, env, `DROP TABLE oauth_access_tokens`)
		token := IssuedToken{
			AccessToken: "w11d-ins",
			Context: AccessTokenContext{
				TokenID: "w11d-ins-t", ClientID: env.publicID, GrantID: "g_ok",
				SystemAccountID: env.accountID, Scopes: []string{"openid"},
				IssuedAt: iso(env.clock.Now()), ExpiresAt: iso(env.clock.Now().Add(time.Hour)),
			},
		}
		if _, err := env.store.insertAccessToken(context.Background(), env.db, token); err == nil {
			t.Fatal("tokens 表缺失必须报错")
		}
	})
	t.Run("create-code-context-table-error", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dExec(t, env, `DROP TABLE oauth_authorization_code_oidc_contexts`)
		input := struct {
			ClientID        string
			SystemAccountID string
			Scopes          []string
			RedirectURI     string
			CodeChallenge   string
			Nonce           string
		}{ClientID: env.publicID, SystemAccountID: env.accountID, Scopes: []string{"openid"}, RedirectURI: env.publicRedirect, Nonce: "w11d-n"}
		if _, err := env.store.CreateAuthorizationCode(context.Background(), input); err == nil {
			t.Fatal("OIDC 上下文表缺失必须报错")
		}
	})
	t.Run("create-code-grant-table-error", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dExec(t, env, `DROP TABLE oauth_grants`)
		input := struct {
			ClientID        string
			SystemAccountID string
			Scopes          []string
			RedirectURI     string
			CodeChallenge   string
			Nonce           string
		}{ClientID: env.publicID, SystemAccountID: env.accountID, Scopes: []string{"openid"}, RedirectURI: env.publicRedirect}
		if _, err := env.store.CreateAuthorizationCode(context.Background(), input); err == nil {
			t.Fatal("grants 表缺失必须报错")
		}
	})
}

func TestW11DOidcUserinfoEmptyIssuerSubjectError(t *testing.T) {
	env := newRouteEnv(t)
	deps := env.deps
	code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
	payload := env.exchangeCode(t, code)
	accessToken := stringField(t, payload, "access_token")

	savedIssuer := deps.OIDCIssuer
	deps.OIDCIssuer = ""
	defer func() { deps.OIDCIssuer = savedIssuer }()
	request := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	recorder := httptest.NewRecorder()
	deps.getUserinfo(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("空 issuer subject 错误 = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW11DOidcEnsureSigningKeyTriggerErrors(t *testing.T) {
	t.Run("update-trigger-error", func(t *testing.T) {
		env := newStoreEnv(t)
		// 活跃且到期（8 天前）→ 进入轮换；UPDATE 触发器中断。
		env.seedSigningKey(t, "kid-stale", env.clock.Now().Add(-8*24*time.Hour))
		w11dExec(t, env, `CREATE TRIGGER w11d_block_key_update BEFORE UPDATE ON oauth_signing_keys
			BEGIN SELECT RAISE(ABORT, 'w11d update blocked'); END`)
		if _, err := env.store.EnsureSigningKey(context.Background()); err == nil {
			t.Fatal("轮换 UPDATE 失败必须报错")
		}
	})
	t.Run("insert-trigger-error", func(t *testing.T) {
		env := newStoreEnv(t)
		// 空表直接引导；INSERT 触发器中断。
		w11dExec(t, env, `DELETE FROM oauth_signing_keys`)
		w11dExec(t, env, `CREATE TRIGGER w11d_block_key_insert BEFORE INSERT ON oauth_signing_keys
			BEGIN SELECT RAISE(ABORT, 'w11d insert blocked'); END`)
		if _, err := env.store.EnsureSigningKey(context.Background()); err == nil {
			t.Fatal("引导 INSERT 失败必须报错")
		}
	})
}

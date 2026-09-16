package oidc

// w11d 覆盖补齐（二）：设备流 approved 轮询成功链、重复消费臂、过期迁移臂、
// 损坏 nonce/时间戳臂、renew/revoke/userinfo 关闭库与表丢弃错误臂。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW11DOidcDeviceApprovedPollChain(t *testing.T) {
	env := newRouteEnv(t)
	deviceCode := env.approveDevice(t, "openid profile juhe:profile.read", "w11d-nonce")
	rec := env.postForm(t, "/oauth/token", map[string]string{
		"client_id": env.publicID, "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": deviceCode,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("approved poll = %d %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSONBody(t, rec)
	if stringField(t, payload, "access_token") == "" || stringField(t, payload, "id_token") == "" {
		t.Fatalf("approved poll payload = %v", payload)
	}
	// 再次轮询：设备已消费 → invalid_grant（affected != 1 臂）。
	rec = env.postForm(t, "/oauth/token", map[string]string{
		"client_id": env.publicID, "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": deviceCode,
	}, nil)
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "设备码无效或已使用")
}

func TestW11DOidcDevicePollCorruptArms(t *testing.T) {
	t.Run("bad-expires", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dRelaxDeviceTable(t, env)
		now := env.clock.Now()
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, expires_at, interval_seconds, status, system_account_id, created_at)
			VALUES ('d1', ?, ?, 'UC1', 'u', '["openid"]', 'not-a-time', 5, 'approved', ?, ?)`,
			env.publicID, hashSecret("dev-d1"), env.accountID, iso(now))
		if _, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, "dev-d1"); err == nil {
			t.Fatal("坏 expires_at 轮询必须报错")
		}
	})
	t.Run("expired-transition", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dRelaxDeviceTable(t, env)
		now := env.clock.Now()
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, expires_at, interval_seconds, status, system_account_id, created_at)
			VALUES ('d2', ?, ?, 'UC2', 'u', '["openid"]', ?, 5, 'approved', ?, ?)`,
			env.publicID, hashSecret("dev-d2"), iso(now.Add(-time.Minute)), env.accountID, iso(now))
		poll, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, "dev-d2")
		if err != nil || poll == nil || poll.Kind != PollExpired {
			t.Fatalf("过期迁移 = %+v err=%v", poll, err)
		}
	})
	t.Run("zero-interval-escalation", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dRelaxDeviceTable(t, env)
		now := env.clock.Now()
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, expires_at, interval_seconds, status, created_at)
			VALUES ('d3', ?, ?, 'UC3', 'u', '["openid"]', ?, 0, 'pending', ?)`,
			env.publicID, hashSecret("dev-d3"), iso(now.Add(time.Hour)), iso(now))
		poll, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, "dev-d3")
		if err != nil || poll == nil || poll.Kind != PollAuthorizationPending {
			t.Fatalf("零 interval 轮询 = %+v err=%v", poll, err)
		}
		_ = poll
	})
	t.Run("bad-nonce-ciphertext", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dRelaxDeviceTable(t, env)
		now := env.clock.Now()
		otherCiphertext, err := EncryptOidcValue(oidcTestSecret, map[string]string{"other": "x"})
		if err != nil {
			t.Fatal(err)
		}
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, nonce_ciphertext, expires_at, interval_seconds, status, system_account_id, created_at)
			VALUES ('d4', ?, ?, 'UC4', 'u', '["openid"]', ?, ?, 5, 'approved', ?, ?)`,
			env.publicID, hashSecret("dev-d4"), otherCiphertext, iso(now.Add(time.Hour)), env.accountID, iso(now))
		if _, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, "dev-d4"); err == nil {
			t.Fatal("坏 nonce 密文必须报错")
		}
	})
	t.Run("weird-status-decide", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dRelaxDeviceTable(t, env)
		now := env.clock.Now()
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, expires_at, interval_seconds, status, created_at)
			VALUES ('d5', ?, ?, 'UC5', 'u', '["openid"]', ?, 5, 'weird', ?)`,
			env.publicID, hashSecret("dev-d5"), iso(now.Add(time.Hour)), iso(now))
		if _, err := env.store.DecideDeviceAuthorization(context.Background(), "UC5", "csrf", env.accountID, "allow"); err != nil || true {
			// 未知状态返回 nil, nil（scanDeviceAuthorization default 臂）。
			if err != nil {
				t.Fatalf("未知状态决策 = %v", err)
			}
		}
	})
}

func TestW11DOidcConsumeAndDecideDuplicateArms(t *testing.T) {
	env := newStoreEnv(t)
	store := env.store
	ctx := context.Background()

	// 授权事务：创建 → 消费 → 二次消费返回 nil。
	created, err := store.CreateAuthorizationTransaction(ctx, struct {
		ClientID      string
		RedirectURI   string
		Scopes        []string
		State         string
		CodeChallenge string
		Nonce         string
	}{ClientID: env.publicID, RedirectURI: env.publicRedirect, Scopes: []string{"openid"}, State: "s", CodeChallenge: "c", Nonce: "n"})
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := store.ConsumeAuthorizationTransaction(ctx, created.ID, created.CSRFToken)
	if err != nil || consumed == nil {
		t.Fatalf("首次消费 = %+v err=%v", consumed, err)
	}
	again, err := store.ConsumeAuthorizationTransaction(ctx, created.ID, created.CSRFToken)
	if err != nil || again != nil {
		t.Fatalf("二次消费 = %+v err=%v", again, err)
	}

	// 授权码：签发 → 兑换 → 二次兑换 nil。
	input := struct {
		ClientID        string
		SystemAccountID string
		Scopes          []string
		RedirectURI     string
		CodeChallenge   string
		Nonce           string
	}{ClientID: env.publicID, SystemAccountID: env.accountID, Scopes: []string{"openid", "profile"}, RedirectURI: env.publicRedirect, CodeChallenge: pkceChallengeOf(pkceTestVerifier), Nonce: "n"}
	code, err := store.CreateAuthorizationCode(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := store.ExchangeAuthorizationCode(ctx, env.publicID, code, env.publicRedirect, pkceTestVerifier)
	if err != nil || issued == nil {
		t.Fatalf("首次兑换 = %+v err=%v", issued, err)
	}
	if _, err := store.ExchangeAuthorizationCode(ctx, env.publicID, code, env.publicRedirect, pkceTestVerifier); err != nil {
		t.Fatalf("二次兑换 = %v", err)
	}

	// 设备决策：批准后再次决策返回 nil（affected != 1 臂）。
	deviceInput := struct {
		ClientID        string
		Scopes          []string
		Nonce           string
		VerificationURI string
	}{ClientID: env.publicID, Scopes: []string{"openid"}, Nonce: "n", VerificationURI: env.publicRedirect}
	_, userCode, err := store.CreateDeviceAuthorization(ctx, deviceInput)
	if err != nil {
		t.Fatal(err)
	}
	csrfToken := "w11d-csrf"
	if _, err := store.DecideDeviceAuthorization(ctx, userCode, csrfToken, env.accountID, "allow"); err != nil {
		t.Fatalf("首次决策 = %v", err)
	}
	second, err := store.DecideDeviceAuthorization(ctx, userCode, csrfToken, env.accountID, "allow")
	if err != nil || second != nil {
		t.Fatalf("二次决策 = %+v err=%v", second, err)
	}
}

func TestW11DOidcClosedDBRenewRevokeUserinfo(t *testing.T) {
	env := newRouteEnv(t)
	deps := env.deps
	_ = env.db.Close()

	// postTokenRenew：FindActiveSigningKey 错误 → 503。
	recorder := httptest.NewRecorder()
	deps.postTokenRenew(recorder, httptest.NewRequest(http.MethodPost, "/oauth/token/renew", strings.NewReader("current_access_token=t")))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("renew closed db = %d", recorder.Code)
	}
	// postRevoke 同臂。
	recorder = httptest.NewRecorder()
	deps.postRevoke(recorder, httptest.NewRequest(http.MethodPost, "/oauth/revoke", strings.NewReader("token=t")))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("revoke closed db = %d", recorder.Code)
	}
	// getUserinfo 同臂。
	recorder = httptest.NewRecorder()
	deps.getUserinfo(recorder, httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("userinfo closed db = %d", recorder.Code)
	}
}

func TestW11DOidcRenewRevokeClientErrors(t *testing.T) {
	t.Run("renew-find-client-error", func(t *testing.T) {
		env := newRouteEnv(t)
		code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
		payload := env.exchangeCode(t, code)
		accessToken := stringField(t, payload, "access_token")
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_clients`)
		rec := env.postForm(t, "/oauth/token/renew", map[string]string{
			"current_access_token": accessToken, "client_id": env.publicID,
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("renew FindClient 错误 = %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("revoke-find-client-error", func(t *testing.T) {
		env := newRouteEnv(t)
		code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
		payload := env.exchangeCode(t, code)
		accessToken := stringField(t, payload, "access_token")
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_clients`)
		rec := env.postForm(t, "/oauth/revoke", map[string]string{
			"token": accessToken, "client_id": env.publicID,
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("revoke FindClient 错误 = %d", rec.Code)
		}
	})
	t.Run("userinfo-profile-error", func(t *testing.T) {
		env := newRouteEnv(t)
		code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
		payload := env.exchangeCode(t, code)
		accessToken := stringField(t, payload, "access_token")
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE system_accounts`)
		rec := env.get(t, "/oauth/userinfo", map[string]string{"Authorization": "Bearer " + accessToken})
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("userinfo profile 错误 = %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestW11DOidcMaybeIssueDirectArms(t *testing.T) {
	env := newRouteEnv(t)
	deps := env.deps
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	// issuer 为空 → unavailable。
	savedIssuer := deps.OIDCIssuer
	deps.OIDCIssuer = ""
	defer func() { deps.OIDCIssuer = savedIssuer }()
	if _, err := deps.maybeIssueIDToken(request, &AccessTokenContext{
		Scopes:    []string{"openid"},
		IssuedAt: iso(env.clock.Now()), ExpiresAt: iso(env.clock.Now().Add(time.Hour)),
	}, ""); err == nil {
		t.Fatal("空 issuer 必须返回 unavailable")
	}
	deps.OIDCIssuer = savedIssuer
	// 坏 ExpiresAt → 错误。
	if _, err := deps.maybeIssueIDToken(request, &AccessTokenContext{
		Scopes:    []string{"openid"},
		IssuedAt: iso(env.clock.Now()), ExpiresAt: "not-a-time",
	}, ""); err == nil {
		t.Fatal("坏 ExpiresAt 必须报错")
	}
	// sendTokenResponse 坏 ExpiresAt → 500。
	recorder := httptest.NewRecorder()
	deps.sendTokenResponse(recorder, "tok", &AccessTokenContext{ExpiresAt: "not-a-time"}, "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("sendTokenResponse 坏过期 = %d", recorder.Code)
	}
}

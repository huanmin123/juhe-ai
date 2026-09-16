package oidc

// w11d 覆盖补齐：损坏行驱动的存储解析臂、按表丢弃驱动的处理器/存储错误臂、
// OIDC 上下文与签名错误路径。全部走现有 SQLite 测试设施。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func w11dExec(t *testing.T, env *oidcStoreEnv, query string, args ...any) {
	t.Helper()
	if _, err := env.db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// ---------------------------------------------------------------------------
// 存储层：损坏行解析臂
// ---------------------------------------------------------------------------

func TestW11DOidcStoreCorruptRowArms(t *testing.T) {
	env := newStoreEnv(t)
	store := env.store
	ctx := context.Background()
	now := env.clock.Now()

	// ListSigningJwks：retired 行在保留窗口内仍输出（retired_at 解析路径）。
	w11dExec(t, env, `INSERT INTO oauth_signing_keys (id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at)
		VALUES ('k_ret', 'kid-ret', 'ct', '{"kid":"kid-ret","kty":"RSA","n":"x","e":"y"}', 'retired', ?, ?)`, iso(now.Add(-48*time.Hour)), iso(now.Add(-24*time.Hour)))
	keys, err := store.ListSigningJwks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sawRetired := false
	for _, key := range keys {
		if key["kid"] == "kid-ret" {
			sawRetired = true
		}
	}
	if !sawRetired {
		t.Fatal("retired key 必须保留在 JWKS 输出")
	}

	// EnsureSigningKey：活跃且未到期 → 原样返回（426 臂）。
	fresh, err := store.EnsureSigningKey(ctx)
	if err != nil || fresh == nil {
		t.Fatalf("活跃未到期 key = %+v err=%v", fresh, err)
	}

	// signingKeyRotationDue：坏 created_at 视为到期。
	if !signingKeyRotationDue("not-a-time", now) {
		t.Fatal("坏 created_at 必须视为需要轮换")
	}

	// tokenContextFromRow：坏 issued_at / expires_at。
	env.insertGrant(t, "g_bad", now.Add(time.Hour))
	w11dExec(t, env, `INSERT INTO oauth_access_tokens (id, token_hash, client_id, grant_id, issued_at, expires_at, created_at)
		VALUES ('tok_bad', ?, ?, 'g_bad', 'not-a-time', ?, 'x')`, hashSecret("w11d-bad-token"), env.publicID, iso(now.Add(time.Hour)))
	if _, err := store.FindAccessTokenContext(ctx, "w11d-bad-token"); err == nil {
		t.Fatal("坏 issued_at 必须报错")
	}
	w11dExec(t, env, `UPDATE oauth_access_tokens SET issued_at = ? WHERE id = 'tok_bad'`, iso(now))
	w11dExec(t, env, `UPDATE oauth_access_tokens SET expires_at = 'not-a-time' WHERE id = 'tok_bad'`)
	if _, err := store.FindAccessTokenContext(ctx, "w11d-bad-token"); err == nil {
		t.Fatal("坏 expires_at 必须报错")
	}
	w11dExec(t, env, `UPDATE oauth_access_tokens SET expires_at = ? WHERE id = 'tok_bad'`, iso(now.Add(time.Hour)))

	// insertAccessToken：坏时间戳直接调用（708-712 臂）。
	badToken := IssuedToken{
		AccessToken: "w11d-tok",
		Context: AccessTokenContext{
			TokenID: "w11d-t", ClientID: env.publicID, GrantID: "g_bad",
			SystemAccountID: env.accountID, Scopes: []string{"openid"}, IssuedAt: "not-a-time",
		},
	}
	if _, err := store.insertAccessToken(ctx, env.db, badToken); err == nil {
		t.Fatal("坏 issued_at 插入必须报错")
	}
	badToken.Context.IssuedAt = iso(now)
	badToken.Context.ExpiresAt = "not-a-time"
	if _, err := store.insertAccessToken(ctx, env.db, badToken); err == nil {
		t.Fatal("坏 expires_at 插入必须报错")
	}

	// nonceFromCiphertext：加密载荷缺 nonce 键。
	otherCiphertext, err := EncryptOidcValue(oidcTestSecret, map[string]string{"other": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nonceFromCiphertext(oidcTestSecret, otherCiphertext); err == nil {
		t.Fatal("缺 nonce 载荷必须报错")
	}
}

// w11dRelaxDeviceTable 重建 device 授权表（去掉 CHECK/NOT NULL 约束）以便播种损坏行。
func w11dRelaxDeviceTable(t *testing.T, env *oidcStoreEnv) {
	t.Helper()
	w11dExec(t, env, `DROP TABLE oauth_device_authorizations`)
	w11dExec(t, env, `CREATE TABLE oauth_device_authorizations (
		id TEXT PRIMARY KEY,
		client_id TEXT,
		device_code_hash TEXT UNIQUE,
		user_code TEXT UNIQUE,
		verification_uri TEXT,
		scopes_json TEXT,
		nonce_ciphertext TEXT,
		expires_at TEXT,
		interval_seconds INTEGER,
		last_polled_at TEXT,
		csrf_hash TEXT,
		status TEXT,
		system_account_id TEXT,
		approved_at TEXT,
		denied_at TEXT,
		consumed_at TEXT,
		created_at TEXT
	)`)
}

func TestW11DOidcDeviceCorruptArms(t *testing.T) {
	env := newStoreEnv(t)
	store := env.store
	ctx := context.Background()
	now := env.clock.Now()
	base := func(id, userCode, status string, interval int, expiresAt string) string {
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, expires_at, interval_seconds, status, created_at)
			VALUES (?, ?, ?, ?, 'https://w11d/verify', '["openid"]', ?, ?, ?, ?)`,
			id, env.publicID, hashSecret("dev-"+id), userCode, expiresAt, interval, status, iso(now))
		return id
	}

	// 未命中状态（weird）→ nil。
	w11dRelaxDeviceTable(t, env)
	base("d_weird", "UC-WEIRD", "weird", 5, iso(now.Add(time.Hour)))
	if prepared, _, err := store.PrepareDeviceAuthorization(ctx, "UC-WEIRD"); err != nil || prepared != nil {
		t.Fatalf("未知状态 = %+v err=%v", prepared, err)
	}
	// interval < 1 → 归 5。
	base("d_zero", "UC-ZERO", "pending", 0, iso(now.Add(time.Hour)))
	prepared, _, err := store.PrepareDeviceAuthorization(ctx, "UC-ZERO")
	if err != nil || prepared == nil || prepared.IntervalSeconds != 5 {
		t.Fatalf("interval 归 5 = %+v err=%v", prepared, err)
	}
	// 坏 expires_at → 错误。
	base("d_badexp", "UC-BAD1", "pending", 5, "not-a-time")
	if _, _, err := store.PrepareDeviceAuthorization(ctx, "UC-BAD1"); err == nil {
		t.Fatal("坏 expires_at 必须报错")
	}
	// 坏 last_polled_at → 错误（scanDeviceAuthorization 解析）。
	base("d_badpoll", "UC-BAD2", "pending", 5, iso(now.Add(time.Hour)))
	w11dExec(t, env, `UPDATE oauth_device_authorizations SET last_polled_at = 'not-a-time' WHERE id = 'd_badpoll'`)
	if _, _, err := store.PrepareDeviceAuthorization(ctx, "UC-BAD2"); err == nil {
		t.Fatal("坏 last_polled_at 必须报错")
	}
	// 扫描错误：NULL client_id。
	w11dExec(t, env, `INSERT INTO oauth_device_authorizations (id, client_id, user_code, expires_at, interval_seconds, status, created_at)
		VALUES ('d_null', NULL, 'UC-NULL', ?, 5, 'pending', ?)`, iso(now.Add(time.Hour)), iso(now))
	if _, _, err := store.PrepareDeviceAuthorization(ctx, "UC-NULL"); err == nil {
		t.Fatal("NULL client_id 扫描必须报错")
	}
}

// ---------------------------------------------------------------------------
// 存储层：表丢弃驱动的 SQL 错误臂
// ---------------------------------------------------------------------------

func TestW11DOidcStoreDropTableArms(t *testing.T) {
	t.Run("ensure-signing-key-rotation-errors", func(t *testing.T) {
		env := newStoreEnv(t)
		// 活跃但已到期（8 天前创建）→ 进入轮换；表丢弃后各语句失败。
		env.seedSigningKey(t, "kid-stale", env.clock.Now().Add(-8*24*time.Hour))
		w11dExec(t, env, `DROP TABLE oauth_signing_keys`)
		if _, err := env.store.EnsureSigningKey(context.Background()); err == nil {
			t.Fatal("轮换路径表缺失必须报错")
		}
	})
	t.Run("create-authorization-code-errors", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dExec(t, env, `DROP TABLE oauth_authorization_codes`)
		input := struct {
			ClientID        string
			SystemAccountID string
			Scopes          []string
			RedirectURI     string
			CodeChallenge   string
			Nonce           string
		}{ClientID: env.publicID, SystemAccountID: env.accountID, Scopes: []string{"openid"}, RedirectURI: env.publicRedirect}
		if _, err := env.store.CreateAuthorizationCode(context.Background(), input); err == nil {
			t.Fatal("codes 表缺失必须报错")
		}
	})
	t.Run("device-create-errors", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dExec(t, env, `DROP TABLE oauth_device_authorizations`)
		input := struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid"}, Nonce: "n", VerificationURI: "https://w11d/verify"}
		if _, _, err := env.store.CreateDeviceAuthorization(context.Background(), input); err == nil {
			t.Fatal("device 表缺失必须报错")
		}
	})
	t.Run("list-signing-jwks-error", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dExec(t, env, `DROP TABLE oauth_signing_keys`)
		if _, err := env.store.ListSigningJwks(context.Background()); err == nil {
			t.Fatal("签名密钥表缺失必须报错")
		}
	})
}

// ---------------------------------------------------------------------------
// 路由层：表丢弃 / 损坏数据驱动的错误分支
// ---------------------------------------------------------------------------

func TestW11DOidcRouteErrorArms(t *testing.T) {
	t.Run("authorize-find-client-error", func(t *testing.T) {
		env := newRouteEnv(t)
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_clients`)
		rec := env.get(t, "/oauth/authorize"+env.authorizeQuery(nil), sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("FindClient 错误 = %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("authorize-session-error", func(t *testing.T) {
		env := newRouteEnv(t)
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE system_sessions`)
		rec := env.get(t, "/oauth/authorize"+env.authorizeQuery(nil), sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("browserSession 错误 = %d", rec.Code)
		}
	})
	t.Run("decision-form-and-store-errors", func(t *testing.T) {
		env := newRouteEnv(t)
		transactionID, csrfToken := env.startAuthorize(t, env.authorizeQuery(nil))
		// 超大表体 → formBody 错误。
		huge := "transaction_id=" + transactionID + "&csrf_token=" + csrfToken + "&decision=allow&pad=" + strings.Repeat("x", 33*1024)
		rec := env.postForm(t, "/oauth/authorize/decision", map[string]string{
			"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "allow", "pad": strings.Repeat("x", 33*1024),
		}, sessionCookie(env.sessionToken))
		_ = huge
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("超大表体 = %d", rec.Code)
		}
		// 会话表丢弃 → browserSession 错误。
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE system_sessions`)
		rec = env.postForm(t, "/oauth/authorize/decision", map[string]string{
			"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "allow",
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("决策会话错误 = %d", rec.Code)
		}
	})
	t.Run("decision-consume-error", func(t *testing.T) {
		env := newRouteEnv(t)
		transactionID, csrfToken := env.startAuthorize(t, env.authorizeQuery(nil))
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_authorization_transactions`)
		rec := env.postForm(t, "/oauth/authorize/decision", map[string]string{
			"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "allow",
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("消费事务错误 = %d", rec.Code)
		}
	})
	t.Run("decision-create-code-error", func(t *testing.T) {
		env := newRouteEnv(t)
		transactionID, csrfToken := env.startAuthorize(t, env.authorizeQuery(nil))
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_authorization_codes`)
		rec := env.postForm(t, "/oauth/authorize/decision", map[string]string{
			"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "allow",
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("创建 code 错误 = %d", rec.Code)
		}
	})
	t.Run("decision-redirect-parse-error", func(t *testing.T) {
		env := newRouteEnv(t)
		transactionID, csrfToken := env.startAuthorize(t, env.authorizeQuery(nil))
		w11dExec(t, env.oidcStoreEnv, `UPDATE oauth_authorization_transactions SET redirect_uri = 'ht tp://bad uri' WHERE id = ?`, transactionID)
		rec := env.postForm(t, "/oauth/authorize/decision", map[string]string{
			"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "allow",
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("坏 redirect_uri = %d", rec.Code)
		}
	})
	t.Run("device-authorization-errors", func(t *testing.T) {
		env := newRouteEnv(t)
		// 超大表体。
		rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
			"client_id": env.publicID, "scope": "openid profile juhe:profile.read", "nonce": "w11d-n", "pad": strings.Repeat("x", 33*1024),
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device 超大表体 = %d", rec.Code)
		}
		// FindClient 错误。
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_clients`)
		rec = env.postForm(t, "/oauth/device_authorization", map[string]string{
			"client_id": env.publicID, "scope": "openid profile juhe:profile.read", "nonce": "w11d-n",
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device FindClient 错误 = %d", rec.Code)
		}
	})
	t.Run("device-create-error", func(t *testing.T) {
		env := newRouteEnv(t)
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_device_authorizations`)
		rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
			"client_id": env.publicID, "scope": "openid profile juhe:profile.read", "nonce": "w11d-n",
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device 创建错误 = %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("device-consent-errors", func(t *testing.T) {
		env := newRouteEnv(t)
		deviceCode := env.approveDevicePrep(t, false)
		_ = deviceCode
		// 会话表丢弃：GET 同意页与 POST 决策都报错。
		userCode := mustQueryString(t, env.db, `SELECT user_code FROM oauth_device_authorizations ORDER BY created_at DESC LIMIT 1`)
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE system_sessions`)
		rec := env.get(t, "/oauth/device?user_code="+urlQueryEscape(userCode), sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device 会话错误 = %d", rec.Code)
		}
	})
	t.Run("device-consent-prepare-error", func(t *testing.T) {
		env := newRouteEnv(t)
		env.approveDevicePrep(t, false)
		userCode := mustQueryString(t, env.db, `SELECT user_code FROM oauth_device_authorizations ORDER BY created_at DESC LIMIT 1`)
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_device_authorizations`)
		rec := env.get(t, "/oauth/device?user_code="+urlQueryEscape(userCode), sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device prepare 错误 = %d", rec.Code)
		}
	})
	t.Run("device-decision-errors", func(t *testing.T) {
		env := newRouteEnv(t)
		env.approveDevicePrep(t, false)
		userCode := mustQueryString(t, env.db, `SELECT user_code FROM oauth_device_authorizations ORDER BY created_at DESC LIMIT 1`)
		consent := env.get(t, "/oauth/device?user_code="+urlQueryEscape(userCode), sessionCookie(env.sessionToken))
		csrfToken := firstMatch(t, csrfFieldPattern, consent.Body.String())
		// 超大表体。
		rec := env.postForm(t, "/oauth/device/decision", map[string]string{
			"user_code": userCode, "csrf_token": csrfToken, "decision": "allow", "pad": strings.Repeat("x", 33*1024),
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device 决策超大表体 = %d", rec.Code)
		}
		// 会话错误。
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE system_sessions`)
		rec = env.postForm(t, "/oauth/device/decision", map[string]string{
			"user_code": userCode, "csrf_token": csrfToken, "decision": "allow",
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device 决策会话错误 = %d", rec.Code)
		}
	})
	t.Run("device-decision-store-error", func(t *testing.T) {
		env := newRouteEnv(t)
		env.approveDevicePrep(t, false)
		userCode := mustQueryString(t, env.db, `SELECT user_code FROM oauth_device_authorizations ORDER BY created_at DESC LIMIT 1`)
		consent := env.get(t, "/oauth/device?user_code="+urlQueryEscape(userCode), sessionCookie(env.sessionToken))
		csrfToken := firstMatch(t, csrfFieldPattern, consent.Body.String())
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_device_authorizations`)
		rec := env.postForm(t, "/oauth/device/decision", map[string]string{
			"user_code": userCode, "csrf_token": csrfToken, "decision": "allow",
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device 决策存储错误 = %d", rec.Code)
		}
	})
}

func TestW11DOidcTokenAndUserinfoErrorArms(t *testing.T) {
	t.Run("token-find-client-error", func(t *testing.T) {
		env := newRouteEnv(t)
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_clients`)
		rec := env.postForm(t, "/oauth/token", map[string]string{
			"grant_type": "authorization_code", "code": "c", "client_id": env.publicID,
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("token FindClient 错误 = %d", rec.Code)
		}
	})
	t.Run("token-device-poll-error", func(t *testing.T) {
		env := newRouteEnv(t)
		deviceCode := env.approveDevice(t, "openid", "w11d-nonce")
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_device_authorizations`)
		rec := env.postForm(t, "/oauth/token", map[string]string{
			"grant_type": "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": deviceCode, "client_id": env.publicID,
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("device poll 错误 = %d", rec.Code)
		}
	})
	t.Run("token-code-oidc-context-error", func(t *testing.T) {
		env := newRouteEnv(t)
		code := env.completeAuthorize(t, env.authorizeQuery(nil))
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_authorization_code_oidc_contexts`)
		rec := env.postForm(t, "/oauth/token", map[string]string{
			"grant_type": "authorization_code", "code": code,
			"redirect_uri":  env.publicRedirect,
			"code_verifier": pkceTestVerifier,
			"client_id":     env.publicID,
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code OIDC 上下文错误 = %d", rec.Code)
		}
	})
	t.Run("renew-and-revoke-error-arms", func(t *testing.T) {
		env := newRouteEnv(t)
		code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
		payload := env.exchangeCode(t, code)
		accessToken := stringField(t, payload, "access_token")
		// 续期：超大表体。
		rec := env.postForm(t, "/oauth/token/renew", map[string]string{
			"current_access_token": accessToken, "client_id": env.publicID, "pad": strings.Repeat("x", 33*1024),
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("renew 超大表体 = %d", rec.Code)
		}
		// 续期：tokens 表丢弃。
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_access_tokens`)
		rec = env.postForm(t, "/oauth/token/renew", map[string]string{"current_access_token": accessToken, "client_id": env.publicID}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("renew Rotate 错误 = %d", rec.Code)
		}
	})
	t.Run("revoke-error-arms", func(t *testing.T) {
		env := newRouteEnv(t)
		code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
		payload := env.exchangeCode(t, code)
		accessToken := stringField(t, payload, "access_token")
		rec := env.postForm(t, "/oauth/revoke", map[string]string{
			"token": accessToken, "client_id": env.publicID, "pad": strings.Repeat("x", 33*1024),
		}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("revoke 超大表体 = %d", rec.Code)
		}
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_access_tokens`)
		rec = env.postForm(t, "/oauth/revoke", map[string]string{"token": accessToken, "client_id": env.publicID}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("revoke 存储错误 = %d", rec.Code)
		}
	})
	t.Run("userinfo-error-arms", func(t *testing.T) {
		env := newRouteEnv(t)
		code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
		payload := env.exchangeCode(t, code)
		accessToken := stringField(t, payload, "access_token")
		bearer := map[string]string{"Authorization": "Bearer " + accessToken}
		// tokens 表丢弃。
		w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_access_tokens`)
		rec := env.get(t, "/oauth/userinfo", bearer)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("userinfo 令牌错误 = %d", rec.Code)
		}
	})
	t.Run("userinfo-profile-error-and-missing-account", func(t *testing.T) {
		env := newRouteEnv(t)
		code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
		payload := env.exchangeCode(t, code)
		accessToken := stringField(t, payload, "access_token")
		bearer := map[string]string{"Authorization": "Bearer " + accessToken}
		// 令牌指向不存在的账户 → 401 invalid_token。
		w11dExec(t, env.oidcStoreEnv, `DELETE FROM system_accounts WHERE id = ?`, env.accountID)
		rec := env.get(t, "/oauth/userinfo", bearer)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("缺失账户 = %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestW11DOidcMaybeIssueIDTokenArms(t *testing.T) {
	env := newRouteEnv(t)
	deps := env.deps
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	validContext := &AccessTokenContext{
		TokenID: "t1", ClientID: env.publicID,
		SystemAccountID: env.accountID, Scopes: []string{"openid"},
		IssuedAt: iso(env.clock.Now()), ExpiresAt: iso(env.clock.Now().Add(time.Hour)),
	}
	// 非 openid scope → 空串。
	if token, err := deps.maybeIssueIDToken(request, &AccessTokenContext{Scopes: []string{"profile"}}, ""); err != nil || token != "" {
		t.Fatalf("非 openid = %q err=%v", token, err)
	}
	// 签名密钥查询错误。
	w11dExec(t, env.oidcStoreEnv, `DROP TABLE oauth_signing_keys`)
	if _, err := deps.maybeIssueIDToken(request, validContext, ""); err == nil {
		t.Fatal("签名密钥错误必须上抛")
	}
}

func TestW11DOidcCryptoAndHelperArms(t *testing.T) {
	// oidcGCM：坏 secret 派生 key 失败路径由 aes.NewCipher 分支覆盖（SHA-256
	// 派生长度恒 32，该臂不可达），跳过。
	// 重定向匹配：坏 requested URI 与坏 registered URI。
	if matchesRegisteredRedirectUri([]string{"https://a/cb"}, "ht tp://bad") {
		t.Fatal("坏 requested URI 必须不匹配")
	}
	if matchesRegisteredRedirectUri([]string{"ht tp://bad"}, "http://127.0.0.1:8080/cb") {
		t.Fatal("坏 registered URI 必须跳过")
	}
	// secondsUntil 坏值。
	if _, err := secondsUntil("not-a-time", time.Now()); err == nil {
		t.Fatal("secondsUntil 坏值必须报错")
	}
	var _ sql.NullString
	var _ = http.MethodGet
	var _ = httptest.NewRecorder
}

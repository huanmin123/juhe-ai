package oidc

// w11d 覆盖补齐（四）：getUserinfo/jwks 直调错误臂、DecryptOidcValue 坏段、
// 活跃行携带 retired_at 的解析臂、损坏 JWK 的 JWKS 空表臂。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestW11DOidcUserinfoDirectArms(t *testing.T) {
	env := newRouteEnv(t)
	deps := env.deps
	code := env.completeAuthorize(t, env.authorizeQuery(map[string]string{"scope": "openid profile"}))
	payload := env.exchangeCode(t, code)
	accessToken := stringField(t, payload, "access_token")

	get := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
		request.Header.Set("Authorization", "Bearer "+accessToken)
		recorder := httptest.NewRecorder()
		deps.getUserinfo(recorder, request)
		return recorder
	}
	// 成功基线。
	if recorder := get(); recorder.Code != http.StatusOK {
		t.Fatalf("userinfo 基线 = %d %s", recorder.Code, recorder.Body.String())
	}
	// profile 查询错误：drop system_accounts。
	w11dExec(t, env.oidcStoreEnv, `DROP TABLE system_accounts`)
	if recorder := get(); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("userinfo profile 错误 = %d", recorder.Code)
	}
}

func TestW11DOidcJwksCorruptArms(t *testing.T) {
	// 活跃行携带 retired_at：FindActiveSigningKey 解析该列。
	t.Run("active-with-retired-at", func(t *testing.T) {
		env := newStoreEnv(t)
		env.seedSigningKey(t, "kid-a", env.clock.Now())
		w11dExec(t, env, `UPDATE oauth_signing_keys SET retired_at = ? WHERE status = 'active'`, iso(env.clock.Now().Add(-time.Hour)))
		key, err := env.store.FindActiveSigningKey(t.Context())
		if err != nil || key == nil || key.Kid != "kid-a" {
			t.Fatalf("携带 retired_at 的活跃 key = %+v err=%v", key, err)
		}
		if key.RetiredAt == nil {
			t.Fatal("retired_at 必须解析")
		}
	})
	// JWKS：坏 JSON 的 JWK 行被跳过 → 空表 → unavailable。
	t.Run("unparsable-jwk-empty-list", func(t *testing.T) {
		env := newRouteEnv(t)
		w11dExec(t, env.oidcStoreEnv, `UPDATE oauth_signing_keys SET public_jwk_json = 'not-json' WHERE status = 'active'`)
		rec := env.get(t, "/oauth/jwks", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("空 JWKS = %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestW11DOidcDecryptBadParts(t *testing.T) {
	// iv 合法、tag 坏。
	if err := DecryptOidcValue(oidcTestSecret, "AAAA.not-base64!!.BBBB", &struct{}{}); err == nil {
		t.Fatal("坏 tag 段必须报错")
	}
	// iv/tag 合法、密文坏。
	if err := DecryptOidcValue(oidcTestSecret, "AAAA.AAAA.not-base64!!", &struct{}{}); err == nil {
		t.Fatal("坏密文段必须报错")
	}
	// 三段之外形态。
	if err := DecryptOidcValue(oidcTestSecret, "AAAA.AAAA", &struct{}{}); err == nil {
		t.Fatal("缺段必须报错")
	}
}

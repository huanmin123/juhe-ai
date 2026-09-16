package oidc

// w11d 覆盖补齐（六）：触发器驱动的各存储写入错误臂（消费事务、兑换消费、
// 设备决策/轮询/签发链、CSRF 写回）。

import (
	"context"
	"testing"
	"time"
)

func TestW11DOidcWriteTriggerArms(t *testing.T) {
	t.Run("consume-transaction-update-error", func(t *testing.T) {
		env := newStoreEnv(t)
		created, err := env.store.CreateAuthorizationTransaction(context.Background(), struct {
			ClientID      string
			RedirectURI   string
			Scopes        []string
			State         string
			CodeChallenge string
			Nonce         string
		}{ClientID: env.publicID, RedirectURI: env.publicRedirect, Scopes: []string{"openid"}})
		if err != nil {
			t.Fatal(err)
		}
		w11dExec(t, env, `CREATE TRIGGER w11d_block_tx_update BEFORE UPDATE ON oauth_authorization_transactions
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, err := env.store.ConsumeAuthorizationTransaction(context.Background(), created.ID, created.CSRFToken); err == nil {
			t.Fatal("消费 UPDATE 失败必须报错")
		}
	})
	t.Run("exchange-consume-update-error", func(t *testing.T) {
		env := newStoreEnv(t)
		input := struct {
			ClientID        string
			SystemAccountID string
			Scopes          []string
			RedirectURI     string
			CodeChallenge   string
			Nonce           string
		}{ClientID: env.publicID, SystemAccountID: env.accountID, Scopes: []string{"openid"}, RedirectURI: env.publicRedirect, CodeChallenge: pkceChallengeOf(pkceTestVerifier)}
		code, err := env.store.CreateAuthorizationCode(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		w11dExec(t, env, `CREATE TRIGGER w11d_block_code_update BEFORE UPDATE ON oauth_authorization_codes
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, err := env.store.ExchangeAuthorizationCode(context.Background(), env.publicID, code, env.publicRedirect, pkceTestVerifier); err == nil {
			t.Fatal("兑换消费 UPDATE 失败必须报错")
		}
	})
	t.Run("decide-update-error", func(t *testing.T) {
		env := newStoreEnv(t)
		deviceInput := struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid"}, VerificationURI: env.publicRedirect}
		authorization, _, err := env.store.CreateDeviceAuthorization(context.Background(), deviceInput)
		if err != nil {
			t.Fatal(err)
		}
		userCode := authorization.UserCode
		prepared, csrfToken, err := env.store.PrepareDeviceAuthorization(context.Background(), userCode)
		if err != nil || prepared == nil {
			t.Fatalf("prepare = %+v err=%v", prepared, err)
		}
		w11dExec(t, env, `CREATE TRIGGER w11d_block_device_update BEFORE UPDATE ON oauth_device_authorizations
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, err := env.store.DecideDeviceAuthorization(context.Background(), userCode, csrfToken, env.accountID, "allow"); err == nil {
			t.Fatal("决策 UPDATE 失败必须报错")
		}
	})
	t.Run("prepare-csrf-update-error", func(t *testing.T) {
		env := newStoreEnv(t)
		deviceInput := struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid"}, VerificationURI: env.publicRedirect}
		authorization, _, err := env.store.CreateDeviceAuthorization(context.Background(), deviceInput)
		if err != nil {
			t.Fatal(err)
		}
		userCode := authorization.UserCode
		// 先完成一次 Prepare（写回 csrf_hash），再轮询触发 CSRF 更新错误需要
		// 二次 Prepare：把 csrf_hash 置空模拟未准备状态后再次 Prepare。
		if _, _, err := env.store.PrepareDeviceAuthorization(context.Background(), userCode); err != nil {
			t.Fatal(err)
		}
		w11dExec(t, env, `UPDATE oauth_device_authorizations SET csrf_hash = NULL WHERE user_code = ?`, userCode)
		w11dExec(t, env, `CREATE TRIGGER w11d_block_device_update2 BEFORE UPDATE ON oauth_device_authorizations
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, _, err := env.store.PrepareDeviceAuthorization(context.Background(), userCode); err == nil {
			t.Fatal("CSRF 写回失败必须报错")
		}
	})
	t.Run("poll-last-polled-update-error", func(t *testing.T) {
		env := newStoreEnv(t)
		deviceInput := struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid"}, VerificationURI: env.publicRedirect}
		_, deviceCode, err := env.store.CreateDeviceAuthorization(context.Background(), deviceInput)
		if err != nil {
			t.Fatal(err)
		}
		w11dExec(t, env, `CREATE TRIGGER w11d_block_device_update3 BEFORE UPDATE ON oauth_device_authorizations
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, deviceCode); err == nil {
			t.Fatal("last_polled UPDATE 失败必须报错")
		}
	})
	t.Run("expired-transition-update-error", func(t *testing.T) {
		env := newStoreEnv(t)
		w11dRelaxDeviceTable(t, env)
		now := env.clock.Now()
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, expires_at, interval_seconds, status, created_at)
			VALUES ('dexp', ?, ?, 'UCE', 'u', '["openid"]', ?, 5, 'pending', ?)`,
			env.publicID, hashSecret("dev-dexp"), iso(now.Add(-time.Minute)), iso(now))
		w11dExec(t, env, `CREATE TRIGGER w11d_block_device_update4 BEFORE UPDATE ON oauth_device_authorizations
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, "dev-dexp"); err == nil {
			t.Fatal("过期迁移 UPDATE 失败必须报错")
		}
	})
	t.Run("approved-poll-grant-insert-error", func(t *testing.T) {
		env := newStoreEnv(t)
		now := env.clock.Now()
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, nonce_ciphertext, expires_at, interval_seconds, status, system_account_id, created_at)
			VALUES ('dgr', ?, ?, 'UCG', 'u', '["openid"]', NULL, ?, 5, 'approved', ?, ?)`,
			env.publicID, hashSecret("dev-dgr"), iso(now.Add(time.Hour)), env.accountID, iso(now))
		w11dExec(t, env, `CREATE TRIGGER w11d_block_grant_insert BEFORE INSERT ON oauth_grants
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, "dev-dgr"); err == nil {
			t.Fatal("grant INSERT 失败必须报错")
		}
	})
	t.Run("approved-poll-token-insert-error", func(t *testing.T) {
		env := newStoreEnv(t)
		now := env.clock.Now()
		w11dExec(t, env, `INSERT INTO oauth_device_authorizations
			(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, nonce_ciphertext, expires_at, interval_seconds, status, system_account_id, created_at)
			VALUES ('dtok', ?, ?, 'UCT', 'u', '["openid"]', NULL, ?, 5, 'approved', ?, ?)`,
			env.publicID, hashSecret("dev-dtok"), iso(now.Add(time.Hour)), env.accountID, iso(now))
		w11dExec(t, env, `CREATE TRIGGER w11d_block_token_insert BEFORE INSERT ON oauth_access_tokens
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, "dev-dtok"); err == nil {
			t.Fatal("token INSERT 失败必须报错")
		}
	})
	t.Run("slow-down-escalation-update-error", func(t *testing.T) {
		env := newStoreEnv(t)
		deviceInput := struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid"}, VerificationURI: env.publicRedirect}
		_, deviceCode, err := env.store.CreateDeviceAuthorization(context.Background(), deviceInput)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, deviceCode); err != nil {
			t.Fatal(err)
		}
		w11dExec(t, env, `CREATE TRIGGER w11d_block_device_update5 BEFORE UPDATE ON oauth_device_authorizations
			BEGIN SELECT RAISE(ABORT, 'w11d blocked'); END`)
		if _, err := env.store.PollDeviceAuthorization(context.Background(), env.publicID, deviceCode); err == nil {
			t.Fatal("slow_down 升级 UPDATE 失败必须报错")
		}
	})
}

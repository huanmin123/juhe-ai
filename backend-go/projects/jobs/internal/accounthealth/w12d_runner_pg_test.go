package accounthealth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// w12d_runner_pg_test.go 覆盖 w12d 波次的 Runner.Run 主循环分支与 PG team
// grant 配额臂。SQLite 部分本地运行；team grant 部分走 w1cover 覆盖库。

// TestW12dRunnerRunLoopArms 覆盖 Run 主循环的租约失败/未获得/owned 错误臂。
func TestW12dRunnerRunLoopArms(t *testing.T) {
	base, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-loop.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	config := Config{
		InstanceID:       "w12d-loop-owner",
		InputDirectory:   t.TempDir(),
		InputKeys:        map[string][]byte{"current": []byte("k")},
		CredentialSecret: "s",
		ScanInterval:     30 * time.Millisecond,
		OwnerLease:       2 * time.Second,
		ProbeTimeout:     time.Second,
		MaxResponseBytes: 1024,
		MaxConcurrency:   1,
		Now:              time.Now,
	}
	// (a) Acquire 持续失败（closed store）→ setError + 重试，ctx 取消后返回。
	closed, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-loop-closed.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	brokenRunner := NewRunner(config, closed, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- brokenRunner.Run(ctx) }()
	deadline := time.Now().Add(300 * time.Millisecond)
	lastError := ""
	for time.Now().Before(deadline) {
		brokenRunner.mu.RLock()
		lastError = brokenRunner.status.LastError
		brokenRunner.mu.RUnlock()
		if lastError != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lastError == "" {
		cancel()
		<-done
		t.Fatal("acquire failure must surface in status")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run loop shutdown: %v", err)
	}
	// (b) lease 被他人占用 → 未获得分支（无错误、持续重试）。
	lease, acquired, err := base.AcquireOwnerLease(context.Background(), "w12d-other-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("pre-acquire: %v %t", err, acquired)
	}
	t.Cleanup(func() { _ = base.ReleaseOwnerLease(context.Background(), lease) })
	busyRunner := NewRunner(config, base, nil)
	busyDone := make(chan error, 1)
	busyCtx, busyCancel := context.WithCancel(context.Background())
	go func() { busyDone <- busyRunner.Run(busyCtx) }()
	time.Sleep(120 * time.Millisecond)
	busyRunner.mu.RLock()
	busyHeld := busyRunner.status.OwnerHeld
	busyRunner.mu.RUnlock()
	if busyHeld {
		t.Fatal("busy lease must not be held by runner")
	}
	busyCancel()
	if err := <-busyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("busy runner shutdown: %v", err)
	}
	// (c) runOwned 返回错误（input 目录缺失）→ 警告后 owner 释放并重试。
	errRunner := NewRunner(config, base, nil)
	errRunner.cfg.InputDirectory = filepath.Join(t.TempDir(), "absent")
	errDone := make(chan error, 1)
	errCtx, errCancel := context.WithCancel(context.Background())
	go func() { errDone <- errRunner.Run(errCtx) }()
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		errRunner.mu.RLock()
		held := errRunner.status.OwnerHeld
		errRunner.mu.RUnlock()
		if !held {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	errCancel()
	if err := <-errDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("error runner shutdown: %v", err)
	}
}

// TestW12dReaderTeamGrantQuotaArms 覆盖 team grant 配额臂（PG 覆盖库）。
func TestW12dReaderTeamGrantQuotaArms(t *testing.T) {
	db := w12dPgDB(t)
	w12dSeedSettings(t, db)
	secret := "w12d-team-secret"
	// 授权实例 + 源账户 + 授权（带 effective_source_team_id）。
	envelope, err := EncryptV1Envelope(secret, []byte(`{"api_key":"sk-w12d-team"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (
		id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
		client_compatibility, health_check_model, health_check_endpoint_mode,
		created_at, updated_at, config_revision, dispatch_revision
	) VALUES (
		'w12d-team-src', 'sys_admin', 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1',
		'w12d-team-src', 'api_key', 'active', $1, 'w12d-fp', 'w12d-mask',
		1, 0, 0, 0, 0,
		'', 'w12d-model', 'chat_json',
		$2, $2, 5, 7
	)`, envelope, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (
		id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
		client_compatibility, health_check_model, health_check_endpoint_mode,
		authorization_instance_source_account_id, authorization_instance_authorization_id,
		created_at, updated_at, config_revision, dispatch_revision
	) VALUES (
		'w12d-team-inst', 'sys_admin', 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1',
		'w12d-team-inst', 'api_key', 'active', $1, 'w12d-fp', 'w12d-mask',
		1, 0, 0, 0, 0,
		'', 'w12d-model', 'chat_json',
		'w12d-team-src', 'w12d-team-auth',
		$2, $2, 5, 7
	)`, envelope, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	for _, accountID := range []string{"w12d-team-src", "w12d-team-inst"} {
		if _, err := db.Exec(`INSERT INTO juhe_business.account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES ($1, 1, $2)
			ON CONFLICT (account_id) DO NOTHING`, accountID, w12dFixtureNowText); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES ('w12d-group', 'sys_admin', 'w12d-group', 'gpt', 1, 0, $1, $1) ON CONFLICT (id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
		VALUES ('sys_admin', 'w12d-group', 'w12d-team-inst', 'w12d-team-auth', 1, $1, $1) ON CONFLICT (group_id, account_id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
		effective_source_type, effective_source_team_id, activated_at, limits_json, created_by, created_at, updated_at
	) VALUES ('w12d-team-auth', 'account', 'w12d-team-src', 'sys_admin', 'sys_admin', 'health_check', 'active',
		'direct', 'w12d-team-a', $1, NULL, 'w12d', $1, $1)`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	// 无 active team grant → ErrNoRows 臂（候选仍合格）。
	reader, err := NewPostgresDirectInputReader(db, secret, time.Hour, func() time.Time {
		return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("team quota load: %v", err)
	}
	found := false
	for _, input := range result.Inputs {
		if input.AccountID == "w12d-team-inst" {
			found = true
		}
	}
	if !found {
		t.Fatalf("team candidate missing: %+v failures=%+v", result.Inputs, result.Failures)
	}
	// active team grant 存在且无 limits → 合格（grant limits NULL 分支）。
	if _, err := db.Exec(`INSERT INTO juhe_business.resource_authorization_grants (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_team_id,
		scope, status, created_by, created_at, updated_at
	) VALUES ('w12d-grant-1', 'account', 'w12d-team-src', 'sys_admin', 'team', 'w12d-team-a',
		'health_check', 'active', 'w12d', $1, $1)
		ON CONFLICT (id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	result, err = reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("grant load: %v", err)
	}
	found = false
	for _, input := range result.Inputs {
		if input.AccountID == "w12d-team-inst" {
			found = true
		}
	}
	if !found {
		t.Fatalf("team candidate must remain eligible: %+v failures=%+v", result.Inputs, result.Failures)
	}
}

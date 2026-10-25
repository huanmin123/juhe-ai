package accounthealth

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"testing"
	"time"
)

// w12d_runner_pg_test.go 覆盖 w12d 波次的 PG team grant 配额臂（w1cover 覆盖库）。
// 注：原借 SQLite store 载体的 Run 主循环分支用例随 sqlite 臂退役。

// TestW12dReaderTeamGrantQuotaArms 覆盖 team grant 配额臂（PG 覆盖库）。
func TestW12dReaderTeamGrantQuotaArms(t *testing.T) {
	db := w12dPgDB(t)
	w12dSeedSettings(t, db)
	secret := "w12d-team-secret"
	// 授权实例 + 源账户 + 授权（带 effective_source_team_id）。
	envelope, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_key":"sk-w12d-team"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (
		id, system_account_id, vendor_code, connection_driver_code, oauth_type, protocol_code, protocol_version,
		name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
		health_check_model, health_check_endpoint_mode,
		created_at, updated_at, config_revision, dispatch_revision
	) VALUES (
		'w12d-team-src', 'sys_admin', 'gpt', 'gpt', NULL, 'openai', 'v1',
		'w12d-team-src', 'api_key', 'active', $1, 'w12d-fp', 'w12d-mask',
		1, 0, 0, 0, 0,
		'w12d-model', 'chat_json',
		$2, $2, 5, 7
	)`, envelope, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	w12dSeedConnection(t, db, "w12d-team-src", "gpt_openai_v1", "https://api.openai.com/v1")
	// 授权行先于实例账户：accounts.authorization_instance_authorization_id 有
	// 外键约束（schema 漂移修复补齐），后插会违反 FK。
	if _, err := db.Exec(`INSERT INTO juhe_business.resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
		effective_source_type, effective_source_team_id, activated_at, limits_json, created_by, created_at, updated_at
	) VALUES ('w12d-team-auth', 'account', 'w12d-team-src', 'sys_admin', 'sys_admin', 'health_check', 'active',
		'direct', 'w12d-team-a', $1, NULL, 'w12d', $1, $1)`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (
		id, system_account_id, connection_driver_code, protocol_code, protocol_version,
		name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
		health_check_model, health_check_endpoint_mode,
		authorization_instance_source_account_id, authorization_instance_authorization_id,
		created_at, updated_at, config_revision, dispatch_revision
	) VALUES (
		'w12d-team-inst', 'sys_admin', 'gpt', 'openai', 'v1',
		'w12d-team-inst', 'api_key', 'active', $1, 'w12d-fp', 'w12d-mask',
		1, 0, 0, 0, 0,
		'w12d-model', 'chat_json',
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
	if _, err := db.Exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, enabled, is_default, created_at, updated_at)
		VALUES ('w12d-group', 'sys_admin', 'w12d-group', 1, 0, $1, $1) ON CONFLICT (id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
		VALUES ('sys_admin', 'w12d-group', 'w12d-team-inst', 'w12d-team-auth', 1, $1, $1) ON CONFLICT (group_id, account_id) DO NOTHING`, w12dFixtureNowText); err != nil {
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
	// grants.grantee_team_id 有外键约束：team 行先于 grant 插入。
	if _, err := db.Exec(`INSERT INTO juhe_business.system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('w12d-team-a', 'w12d-team-a', 'active', 'w12d', $1, $1) ON CONFLICT (id) DO NOTHING`, w12dFixtureNowText); err != nil {
		t.Fatal(err)
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

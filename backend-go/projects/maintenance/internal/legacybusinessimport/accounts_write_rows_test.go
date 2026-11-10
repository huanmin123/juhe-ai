package legacybusinessimport

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// newWriteAccountRowsFixture 建 writeAccountRows 目标形态的最小三表夹具
// （无外键、无触发器）：accounts 按 INSERT 的完整列集建表，另加连接与
// endpoint modes 表。
func newWriteAccountRowsFixture(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(_ context.Context, db *sql.DB) error {
		_, err := db.Exec(`
			CREATE SCHEMA juhe_business;
			CREATE TABLE juhe_business.accounts (
				id text PRIMARY KEY,
				config_revision integer NOT NULL DEFAULT 1,
				dispatch_revision bigint NOT NULL DEFAULT 1,
				circuit_projection_revision bigint NOT NULL DEFAULT 0,
				system_account_id text NOT NULL,
				vendor_code text,
				connection_driver_code text NOT NULL,
				oauth_type text,
				protocol_code text NOT NULL,
				protocol_version text NOT NULL,
				name text NOT NULL,
				type text NOT NULL,
				status text NOT NULL,
				credentials_encrypted text NOT NULL,
				credential_fingerprint text,
				credential_mask text NOT NULL DEFAULT '',
				oauth_access_token_expires_at text,
				oauth_refresh_token_present integer NOT NULL DEFAULT 0,
				proxy_profile_id text,
				concurrency_limit integer NOT NULL DEFAULT 0,
				rpm_limit integer NOT NULL DEFAULT 0,
				priority integer NOT NULL DEFAULT 0,
				super_priority_enabled integer NOT NULL DEFAULT 0,
				fallback_enabled integer NOT NULL DEFAULT 0,
				schedulable integer NOT NULL DEFAULT 0,
				availability_schedule_json text,
				availability_schedule_next_check_at text,
				notes text,
				account_expires_at text,
				last_used_at text,
				cooldown_until text,
				last_error_code text,
				last_error_message text,
				last_error_trace_id text,
				cooldown_retest_failure_count integer NOT NULL DEFAULT 0,
				cooldown_retest_observation_started_at text,
				cooldown_retest_generation text,
				cooldown_retest_last_at text,
				cooldown_retest_last_status_code integer,
				background_probe_enabled integer NOT NULL DEFAULT 0,
				temporary_unavailable_continuous_probe_enabled integer NOT NULL DEFAULT 0,
				expedited_recovery_enabled integer NOT NULL DEFAULT 0,
				capability_domain text,
				health_check_connection_id text,
				health_check_model text,
				health_check_endpoint_mode text,
				last_health_check_at text,
				next_health_check_at text,
				last_health_success_at text,
				health_check_failure_count integer NOT NULL DEFAULT 0,
				health_check_failure_started_at text,
				last_health_check_status_code integer,
				last_health_check_error_code text,
				last_health_check_error_message text,
				last_health_check_trace_id text,
				stream_failure_count integer NOT NULL DEFAULT 0,
				stream_failure_window_started_at text,
				balance_query_enabled integer NOT NULL DEFAULT 0,
				balance_query_config_json text NOT NULL DEFAULT '{}',
				balance_query_next_refresh_at text,
				created_at text NOT NULL,
				updated_at text NOT NULL
			);
			CREATE TABLE juhe_business.account_connections (
				id text PRIMARY KEY,
				account_id text NOT NULL,
				connection_preset_code text NOT NULL,
				protocol_surface_code text,
				base_url text NOT NULL,
				enabled integer NOT NULL DEFAULT 0,
				created_at text NOT NULL,
				updated_at text NOT NULL
			);
			CREATE TABLE juhe_business.account_endpoint_modes (
				connection_id text NOT NULL,
				endpoint_mode text NOT NULL,
				PRIMARY KEY (connection_id, endpoint_mode)
			);`)
		return err
	})
}

// TestWriteAccountRowsWritesAccountsAndConnections 直接覆盖 importAccounts 实际
// 写账户的调用点：未来若有人移除连接/endpoint mode 写入或账户行写入，本测试
// 会红。
func TestWriteAccountRowsWritesAccountsAndConnections(t *testing.T) {
	db := newWriteAccountRowsFixture(t)
	ctx := context.Background()
	const secret = "write-account-rows-test-secret"
	sealed, err := accountcrypto.EncryptJSON(secret, map[string]any{
		"base_url":                 "https://example.test/v1",
		"supported_endpoint_modes": []string{"chat_completions"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const stamp = "2026-11-03T00:00:00.000Z"
	baseRow := func(id, name string) accountRow {
		return accountRow{
			ID:               id,
			ConfigRevision:   2,
			DispatchRevision: 3,
			CircuitRevision:  0,
			Owner:            "sys-1",
			ProviderCode:     "openai",
			ProfileID:        "profile_openai_openai_v1",
			Vendor:           sql.NullString{},
			Driver:           "generic_http",
			Preset:           "gpt_openai_v1",
			AccountType:      "api_key",
			ProtocolCode:     "openai",
			ProtocolVersion:  "v1",
			Name:             name,
			Status:           "active",
			Credential:       sealed,
			CredentialMask:   "sk-***",
			ConcurrencyLimit: 5,
			RPMLimit:         60,
			Priority:         1,
			FallbackEnabled:  1,
			Schedulable:      1,
			Domain:           "chat",
			BalanceConfig:    "{}",
			CreatedAt:        stamp,
			UpdatedAt:        stamp,
		}
	}
	rows := []accountRow{
		baseRow("acc-cn", "k40 林夕"),
		baseRow("acc-empty", ""),
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := &Report{Tables: map[string]int{}}
	accountIDs, err := writeAccountRows(ctx, tx, rows, secret, report)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if len(accountIDs) != 2 {
		t.Fatalf("accountIDs = %v, want 2 个账户", accountIDs)
	}
	for _, accountID := range []string{"acc-cn", "acc-empty"} {
		if _, ok := accountIDs[accountID]; !ok {
			t.Fatalf("accountIDs 缺少 %s: %v", accountID, accountIDs)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM juhe_business.accounts WHERE id = $1`, accountID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s 账户行 count = %d, want 1", accountID, count)
		}
	}

	var baseURL, surface string
	if err := db.QueryRow(`SELECT base_url, protocol_surface_code FROM juhe_business.account_connections
		WHERE id = 'conn_acc-cn' AND account_id = 'acc-cn'`).Scan(&baseURL, &surface); err != nil {
		t.Fatalf("acc-cn 连接行应存在: %v", err)
	}
	if baseURL != "https://example.test/v1" || surface != "openai_v1" {
		t.Fatalf("连接行不符: base_url=%q surface=%q", baseURL, surface)
	}
	var modes int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_endpoint_modes
		WHERE connection_id = 'conn_acc-cn'`).Scan(&modes); err != nil {
		t.Fatal(err)
	}
	if modes == 0 {
		t.Fatal("acc-cn 应有 endpoint mode 行")
	}

	if report.Tables["accounts"] != 2 {
		t.Fatalf("report accounts = %d, want 2", report.Tables["accounts"])
	}
}

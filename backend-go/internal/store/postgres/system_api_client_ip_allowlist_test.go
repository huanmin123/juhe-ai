package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestW6SystemAPIClientIPAllowlistMigrationMatchesNodeTable(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000025_w6_system_api_client_ip_allowlist.sql")
	if err != nil {
		t.Fatalf("read W6 client IP allowlist migration: %v", err)
	}
	sql := string(source)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_policies",
		"id text PRIMARY KEY",
		"ip_hash text NOT NULL",
		"policy_type text NOT NULL",
		"status text NOT NULL",
		"reason text",
		"expires_at text",
		"created_by_system_account_id text NOT NULL",
		"created_at text NOT NULL",
		"updated_at text NOT NULL",
		"disabled_at text",
		"disabled_by_system_account_id text",
		"disabled_reason text",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_client_ip_policies_active_unique",
		"ON juhe_stats.client_ip_policies(ip_hash)",
		"WHERE status = 'active'",
		"CREATE INDEX IF NOT EXISTS idx_client_ip_policies_active",
		"ON juhe_stats.client_ip_policies(status, policy_type, ip_hash, expires_at)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("W6 client IP allowlist migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"client_ip_registry",
		"client_ip_stats_daily",
		"client_ip_usage_range_windows",
		"client_ip_account_stats_daily",
		"client_ip_account_usage_range_windows",
		"client_ip_policy_hits",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("W6 client IP allowlist migration should not contain %q", forbidden)
		}
	}
}

func TestW6SystemAPIClientIPAllowlistQueryUsesBoundedActiveAllowlistExists(t *testing.T) {
	source, err := os.ReadFile("queries/w6_system_api_client_ip_allowlist.sql")
	if err != nil {
		t.Fatalf("read W6 client IP allowlist query: %v", err)
	}
	sql := strings.ToLower(string(source))

	for _, want := range []string{
		"select exists (",
		"from juhe_stats.client_ip_policies as policies",
		"policies.ip_hash = sqlc.arg(ip_hash)::text",
		"policies.policy_type = 'allowlist'",
		"policies.status = 'active'",
		"policies.expires_at is null",
		"policies.expires_at > sqlc.arg(now_at)::text",
		"limit 1",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("W6 client IP allowlist query missing %q:\n%s", want, sql)
		}
	}

	for _, forbidden := range []string{
		"policy_type = 'blacklist'",
		"status = 'disabled'",
		"select *",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("W6 client IP allowlist query should not contain %q:\n%s", forbidden, sql)
		}
	}
}

func TestSystemAPIClientIPAllowlistedFormatsUTCAndReturnsResult(t *testing.T) {
	q := &systemAPIClientIPAllowlistQuerierStub{result: true}
	now := time.Date(2026, time.July, 10, 22, 31, 42, 123456789, time.FixedZone("UTC+8", 8*60*60))

	allowlisted, err := systemAPIClientIPAllowlisted(context.Background(), q, "sha256-ip-hash", now)
	if err != nil {
		t.Fatalf("systemAPIClientIPAllowlisted() error = %v", err)
	}
	if !allowlisted {
		t.Fatal("systemAPIClientIPAllowlisted() = false, want true")
	}
	if q.calls != 1 {
		t.Fatalf("query calls = %d, want 1", q.calls)
	}
	if q.arg.IpHash != "sha256-ip-hash" {
		t.Fatalf("ip hash = %q, want sha256-ip-hash", q.arg.IpHash)
	}
	if q.arg.NowAt != "2026-07-10T14:31:42.123456789Z" {
		t.Fatalf("now_at = %q, want UTC RFC3339Nano", q.arg.NowAt)
	}
}

func TestSystemAPIClientIPAllowlistedWrapsQueryError(t *testing.T) {
	queryErr := errors.New("query failed")
	q := &systemAPIClientIPAllowlistQuerierStub{err: queryErr}

	allowlisted, err := systemAPIClientIPAllowlisted(context.Background(), q, "sha256-ip-hash", time.Time{})
	if allowlisted {
		t.Fatal("systemAPIClientIPAllowlisted() = true on query error")
	}
	if !errors.Is(err, queryErr) {
		t.Fatalf("systemAPIClientIPAllowlisted() error = %v, want wrapped query error", err)
	}
	if err == nil || !strings.Contains(err.Error(), "check system api client IP allowlist") {
		t.Fatalf("systemAPIClientIPAllowlisted() error = %v, want operation context", err)
	}
}

type systemAPIClientIPAllowlistQuerierStub struct {
	result bool
	err    error
	calls  int
	arg    postgresqueries.SystemAPIClientIPAllowlistedParams
}

func (s *systemAPIClientIPAllowlistQuerierStub) SystemAPIClientIPAllowlisted(
	_ context.Context,
	arg postgresqueries.SystemAPIClientIPAllowlistedParams,
) (bool, error) {
	s.calls++
	s.arg = arg
	return s.result, s.err
}

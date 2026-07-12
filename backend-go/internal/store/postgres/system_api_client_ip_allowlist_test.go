package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestW6SystemAPIClientIPAllowlistMigrationMatchesNodeTable(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000025_w6_system_api_client_ip_allowlist.sql")
	if err != nil {
		t.Fatalf("read W6 client IP allowlist migration: %v", err)
	}
	sql := string(source)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_registry",
		"ip_hash text PRIMARY KEY",
		"bucket_no integer NOT NULL",
		"aggregate_ip_key text NOT NULL",
		"client_ip text NOT NULL",
		"ip_version integer NOT NULL",
		"first_seen_at text NOT NULL",
		"last_seen_at text NOT NULL",
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
		"CREATE INDEX IF NOT EXISTS idx_client_ip_registry_bucket",
		"ON juhe_stats.client_ip_registry(bucket_no, ip_hash)",
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
	sql := strings.ToLower(w6SystemAPIClientIPAllowlistNamedSQLSection(
		t,
		string(source),
		"FindSystemAPIClientIPAllowlistPolicy",
	))

	for _, want := range []string{
		"select policies.id, policies.expires_at",
		"from juhe_stats.client_ip_policies as policies",
		"inner join juhe_stats.client_ip_registry as registry",
		"on registry.ip_hash = policies.ip_hash",
		"policies.ip_hash = sqlc.arg(ip_hash)::text",
		"policies.policy_type = 'allowlist'",
		"policies.status = 'active'",
		"policies.expires_at is null",
		"policies.expires_at > sqlc.arg(now_at)::text",
		"order by policies.created_at desc, policies.id desc",
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

func w6SystemAPIClientIPAllowlistNamedSQLSection(
	t *testing.T,
	source string,
	name string,
) string {
	t.Helper()
	marker := "-- name: " + name + " "
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("named SQL query %s not found", name)
	}
	rest := source[start+len(marker):]
	if next := strings.Index(rest, "\n-- name: "); next >= 0 {
		return rest[:next]
	}
	return rest
}

func TestFindSystemAPIClientIPAllowlistPolicyFormatsUTCAndReturnsResult(t *testing.T) {
	q := &systemAPIClientIPAllowlistQuerierStub{
		row: postgresqueries.FindSystemAPIClientIPAllowlistPolicyRow{
			ID:        "policy_allowlist",
			ExpiresAt: pgtype.Text{String: "2026-07-10T15:00:00Z", Valid: true},
		},
	}
	now := time.Date(2026, time.July, 10, 22, 31, 42, 123456789, time.FixedZone("UTC+8", 8*60*60))

	policy, found, err := findSystemAPIClientIPAllowlistPolicy(context.Background(), q, "sha256-ip-hash", now)
	if err != nil {
		t.Fatalf("findSystemAPIClientIPAllowlistPolicy() error = %v", err)
	}
	if !found {
		t.Fatal("findSystemAPIClientIPAllowlistPolicy() found = false")
	}
	if policy.ID != "policy_allowlist" ||
		policy.ExpiresAt == nil ||
		!policy.ExpiresAt.Equal(time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("policy = %+v", policy)
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

func TestFindSystemAPIClientIPAllowlistPolicyReturnsNotFound(t *testing.T) {
	q := &systemAPIClientIPAllowlistQuerierStub{err: pgx.ErrNoRows}

	policy, found, err := findSystemAPIClientIPAllowlistPolicy(context.Background(), q, "sha256-ip-hash", time.Time{})
	if err != nil || found || policy.ID != "" {
		t.Fatalf("policy = %+v, found = %v, err = %v", policy, found, err)
	}
}

func TestFindSystemAPIClientIPAllowlistPolicyWrapsQueryError(t *testing.T) {
	queryErr := errors.New("query failed")
	q := &systemAPIClientIPAllowlistQuerierStub{err: queryErr}

	policy, found, err := findSystemAPIClientIPAllowlistPolicy(context.Background(), q, "sha256-ip-hash", time.Time{})
	if found || policy.ID != "" {
		t.Fatalf("policy = %+v, found = %v on query error", policy, found)
	}
	if !errors.Is(err, queryErr) {
		t.Fatalf("systemAPIClientIPAllowlisted() error = %v, want wrapped query error", err)
	}
	if err == nil || !strings.Contains(err.Error(), "find system api client IP allowlist policy") {
		t.Fatalf("findSystemAPIClientIPAllowlistPolicy() error = %v, want operation context", err)
	}
}

type systemAPIClientIPAllowlistQuerierStub struct {
	row   postgresqueries.FindSystemAPIClientIPAllowlistPolicyRow
	err   error
	calls int
	arg   postgresqueries.FindSystemAPIClientIPAllowlistPolicyParams
}

func (s *systemAPIClientIPAllowlistQuerierStub) FindSystemAPIClientIPAllowlistPolicy(
	_ context.Context,
	arg postgresqueries.FindSystemAPIClientIPAllowlistPolicyParams,
) (postgresqueries.FindSystemAPIClientIPAllowlistPolicyRow, error) {
	s.calls++
	s.arg = arg
	return s.row, s.err
}

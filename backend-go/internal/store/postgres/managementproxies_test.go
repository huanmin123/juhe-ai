package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestManagementProxyListLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 21},
		{input: -1, want: 21},
		{input: 1, want: 1},
		{input: 201, want: 201},
		{input: 202, want: 201},
	}
	for _, tt := range tests {
		if got := normalizeManagementProxyListLimit(tt.input); got != tt.want {
			t.Fatalf("normalizeManagementProxyListLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestManagementProxyOptionsSQLIncludesPagedListGuard(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_proxy_options.sql")
	if err != nil {
		t.Fatalf("read proxy query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: ListManagementProxies :many", "-- name: FindManagementProxy :one")
	for _, want := range []string{
		"-- name: ListManagementProxies :many",
		"description",
		"host",
		"port",
		"username",
		"test_status",
		"latency_ms",
		"outbound_ip",
		"outbound_region",
		"last_test_message",
		"last_tested_at",
		"name COLLATE \"C\" >= sqlc.arg(keyword)::text",
		"starts_with(name, sqlc.arg(keyword)::text)",
		"ORDER BY updated_at DESC, id DESC",
		"LIMIT sqlc.arg(row_limit)::int OFFSET sqlc.arg(row_offset)::int",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("proxy list query missing %q", want)
		}
	}
	for _, forbidden := range []string{"password_encrypted", "COUNT(*)", " ILIKE ", "LIKE '%"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("proxy list query should not include %q", forbidden)
		}
	}
}

func TestManagementProxyCRUDSQLUsesFixedBindingWindow(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_proxy_options.sql")
	if err != nil {
		t.Fatalf("read proxy query: %v", err)
	}
	sql := string(source)
	for _, marker := range []string{
		"-- name: FindManagementProxy :one",
		"-- name: FindManagementProxyForUpdate :one",
		"-- name: CreateManagementProxy :one",
		"-- name: UpdateManagementProxy :one",
		"-- name: DeleteManagementProxy :execrows",
		"-- name: ListManagementProxyAccountBindings :many",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("proxy CRUD SQL missing %q", marker)
		}
	}
	lockSQL := querySection(t, sql, "-- name: FindManagementProxyForUpdate :one", "-- name: ListManagementProxyOptions :many")
	if !strings.Contains(lockSQL, "FOR UPDATE") {
		t.Fatal("proxy update must lock the current row before merging a partial patch")
	}
	updateSQL := querySection(t, sql, "-- name: UpdateManagementProxy :one", "-- name: DeleteManagementProxy :execrows")
	for _, want := range []string{
		"password_encrypted = sqlc.narg(password_encrypted)::text",
		"test_status = CASE WHEN sqlc.arg(reset_test_state)::bool THEN 'unknown' ELSE test_status END",
		"last_tested_at = CASE WHEN sqlc.arg(reset_test_state)::bool THEN NULL ELSE last_tested_at END",
	} {
		if !strings.Contains(updateSQL, want) {
			t.Fatalf("proxy update SQL missing %q", want)
		}
	}
	bindingSQL := querySection(t, sql, "-- name: ListManagementProxyAccountBindings :many", "")
	for _, want := range []string{
		"WHERE proxy_profile_id = sqlc.arg(proxy_id)::text",
		"AND deleted_at IS NULL",
		"ORDER BY id ASC",
		"LIMIT sqlc.arg(row_limit)::int",
	} {
		if !strings.Contains(bindingSQL, want) {
			t.Fatalf("proxy binding SQL missing %q", want)
		}
	}
	if strings.Contains(bindingSQL, "COUNT(") {
		t.Fatal("proxy binding SQL must not perform exact count")
	}
}

func TestManagementProxyTestStateSQLPreservesOutboundWhenUnset(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_proxy_options.sql")
	if err != nil {
		t.Fatalf("read proxy query: %v", err)
	}
	sql := querySection(t, string(source), "-- name: UpdateManagementProxyTestState :one", "-- name: DeleteManagementProxy :execrows")
	for _, want := range []string{
		"-- name: UpdateManagementProxyTestState :one",
		"test_status = sqlc.arg(test_status)::text",
		"latency_ms = sqlc.narg(latency_ms)::int",
		"outbound_ip = CASE WHEN sqlc.arg(set_outbound_ip)::bool THEN sqlc.narg(outbound_ip)::text ELSE outbound_ip END",
		"outbound_region = CASE WHEN sqlc.arg(set_outbound_region)::bool THEN sqlc.narg(outbound_region)::text ELSE outbound_region END",
		"last_test_message = sqlc.arg(last_test_message)::text",
		"last_tested_at = sqlc.arg(last_tested_at)::timestamptz",
		"updated_at = sqlc.arg(updated_at)::timestamptz",
		"WHERE id = sqlc.arg(id)::text",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("proxy test state SQL missing %q", want)
		}
	}
	for _, forbidden := range []string{"COUNT(", "password_encrypted"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("proxy test state SQL should not include %q", forbidden)
		}
	}
}

func TestManagementProxyDuplicateNameError(t *testing.T) {
	for _, constraint := range []string{"idx_proxy_profiles_name_unique", "idx_proxy_profiles_name_unique_lower"} {
		if !managementProxyDuplicateNameError(&pgconn.PgError{Code: "23505", ConstraintName: constraint}) {
			t.Fatalf("constraint %q was not recognized", constraint)
		}
	}
	if managementProxyDuplicateNameError(&pgconn.PgError{Code: "23505", ConstraintName: "other_unique"}) {
		t.Fatal("unrelated unique violation should not be recognized")
	}
}

func TestManagementProxyInUseError(t *testing.T) {
	if !managementProxyInUseError(&pgconn.PgError{Code: "23503", ConstraintName: "accounts_proxy_profile_id_fkey"}) {
		t.Fatal("proxy account foreign key violation was not recognized")
	}
	if managementProxyInUseError(&pgconn.PgError{Code: "23503", ConstraintName: "other_foreign_key"}) {
		t.Fatal("unrelated foreign key violation should not be recognized")
	}
}

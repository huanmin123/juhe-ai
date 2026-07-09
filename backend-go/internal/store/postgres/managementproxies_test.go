package postgres

import (
	"os"
	"strings"
	"testing"
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
	sql := string(source)
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

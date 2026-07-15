package postgres

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestManagementExternalIntegrationSourceUpdateSQLContract(t *testing.T) {
	raw, err := os.ReadFile("queries/w2_management_external_integration_source_list.sql")
	if err != nil {
		t.Fatalf("read external integration source SQL: %v", err)
	}
	sql := string(raw)
	for _, required := range []string{
		"-- name: FindManagementExternalIntegrationSourceForUpdate :one",
		"FOR UPDATE;",
		"-- name: UpdateManagementExternalIntegrationSource :one",
		"-- name: SyncManagementExternalIntegrationSourceTokens :execrows",
		"CASE WHEN status = 'revoked' THEN status ELSE",
		"RETURNING",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("external integration source update SQL missing %q", required)
		}
	}
	lockStart := strings.Index(sql, "-- name: FindManagementExternalIntegrationSourceForUpdate")
	updateStart := strings.Index(sql, "-- name: UpdateManagementExternalIntegrationSource")
	if lockStart < 0 || updateStart <= lockStart {
		t.Fatal("external integration source update query ordering is invalid")
	}
	lockSQL := sql[lockStart:updateStart]
	if strings.Contains(lockSQL, "SELECT *") {
		t.Fatalf("lock query must use explicit columns:\n%s", lockSQL)
	}
}

func TestManagementExternalIntegrationSourceDuplicateNameError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "current lower-name index",
			err: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "idx_external_integration_sources_name_unique_lower",
			},
			want: true,
		},
		{name: "other unique index", err: &pgconn.PgError{Code: "23505", ConstraintName: "other"}},
		{name: "wrapped unrelated", err: errors.New("postgres unavailable")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := managementExternalIntegrationSourceDuplicateNameError(test.err); got != test.want {
				t.Fatalf("duplicate name = %t, want %t", got, test.want)
			}
		})
	}
}

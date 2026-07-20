package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementAccountDeleteSQLCoversTransactionalCleanup(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_account_delete.sql")
	if err != nil {
		t.Fatalf("read management account delete SQL: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"-- name: LockManagementAccountDeleteTarget :one",
		"sqlc.arg(can_access_all)::boolean",
		"accounts.system_account_id = sqlc.arg(effective_system_account_id)::text",
		"FOR UPDATE OF accounts",
		"-- name: ListManagementAccountDeleteInstances :many",
		"authorization_instance_source_account_id = sqlc.arg(source_account_id)::text",
		"-- name: ListManagementAccountDeleteAuthorizationIDs :many",
		"resource_type = 'account'",
		"status <> 'returned'",
		"-- name: RevokeManagementAccountDeleteGrants :exec",
		"status NOT IN ('revoked', 'returned')",
		"-- name: RevokeManagementAccountDeleteSources :exec",
		"ended_reason = COALESCE(ended_reason, 'account_deleted')",
		"-- name: RevokeManagementAccountDeleteAuthorizations :exec",
		"revoked_reason = COALESCE(revoked_reason, 'account_deleted')",
		"-- name: LogicallyDeleteManagementAccounts :many",
		"status = 'disabled'",
		"schedulable = false",
		"cooldown_until = NULL",
		"deleted_by = sqlc.arg(deleted_by)::text",
		"RETURNING id",
		"-- name: DeleteManagementAccountTagBindings :exec",
		"-- name: DeleteManagementAccountSearchTerms :exec",
		"-- name: DeleteManagementAccountSearchDocuments :exec",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("management account delete SQL missing %q", want)
		}
	}
	if strings.Contains(sql, "DELETE FROM juhe_business.accounts") {
		t.Fatal("first-pass account delete must remain logical")
	}
}

package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementAccountOptionLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 50},
		{input: -1, want: 50},
		{input: 1, want: 1},
		{input: 50, want: 50},
		{input: 51, want: 50},
	}
	for _, tt := range tests {
		if got := managementAccountOptionLimit(tt.input); got != tt.want {
			t.Fatalf("managementAccountOptionLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeAccountNameSearchText(t *testing.T) {
	got := normalizeAccountNameSearchText("  ＡＢＣ  ")
	if got != "ABC" {
		t.Fatalf("normalizeAccountNameSearchText() = %q, want ABC", got)
	}
}

func TestAccountNameSearchQueryTerms(t *testing.T) {
	tests := []struct {
		name    string
		keyword string
		want    []string
	}{
		{name: "empty", keyword: "   ", want: nil},
		{name: "single rune", keyword: "中", want: []string{"中"}},
		{name: "two runes", keyword: "中文", want: []string{"中文"}},
		{name: "three gram", keyword: "Alpha", want: []string{"Alp", "lph", "pha"}},
		{name: "duplicate grams", keyword: "aaaa", want: []string{"aaa"}},
		{name: "nfkc", keyword: " ＡＢＣＤ ", want: []string{"ABC", "BCD"}},
		{name: "over max", keyword: strings.Repeat("a", maxAccountNameSearchLength+1), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accountNameSearchQueryTerms(tt.keyword)
			if !sameStringSet(got, tt.want) {
				t.Fatalf("accountNameSearchQueryTerms(%q) = %#v, want %#v", tt.keyword, got, tt.want)
			}
		})
	}
}

func TestAccountNameSearchGramsSkipsBlankTerms(t *testing.T) {
	got := accountNameSearchGrams("a  b", 2)
	if !sameStringSet(got, []string{"a ", " b"}) {
		t.Fatalf("accountNameSearchGrams() = %#v", got)
	}
}

func TestManagementAccountOptionsSQLUsesNameSearchIndex(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_account_options.sql")
	if err != nil {
		t.Fatalf("read account options query: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"account_name_search_terms",
		"account_name_search_documents",
		"position(sqlc.arg(keyword_normalized)::text in documents.normalized_name) > 0",
		"HAVING COUNT(DISTINCT account_name_candidate_terms.term) = sqlc.arg(keyword_term_count)::int",
		"UNION ALL",
		"accounts.authorization_instance_authorization_id IS NULL",
		"INNER JOIN juhe_business.resource_authorizations AS resource_authorizations",
		"LEFT JOIN LATERAL",
		"group_accounts.account_authorization_id = resource_authorizations.id",
		"account_rows.access_type = 'authorized' AND group_accounts.account_authorization_id = account_rows.account_authorization_id",
		"resource_authorizations.resource_type = 'account'",
		"resource_authorizations.grantee_system_account_id = sqlc.arg(system_account_id)::text",
		"resource_authorizations.status IN ('active', 'paused', 'expired')",
		"false AS has_active_manual_authorization_source",
		"FROM juhe_business.resource_authorization_sources AS returnable_sources",
		"returnable_sources.authorization_id = resource_authorizations.id",
		"returnable_sources.source_type = 'manual'",
		"returnable_sources.status = 'active'",
		"account_rows.has_active_manual_authorization_source",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("account options query missing %q", want)
		}
	}
	for _, forbidden := range []string{" ILIKE ", "LIKE '%", "LIKE $", "credentials_encrypted"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("account options query should not use unbounded contains scan %q", forbidden)
		}
	}
}

func TestManagementAccountTagDeleteSQLProtectsBoundTags(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_account_tags.sql")
	if err != nil {
		t.Fatalf("read account tags query: %v", err)
	}
	sql := string(source)
	lockSQL := querySection(t, sql, "-- name: LockManagementAccountTagForDelete :one", "-- name: ManagementAccountTagHasActiveBindings :one")
	for _, want := range []string{
		"FROM juhe_business.account_tags",
		"WHERE id = sqlc.arg(tag_id)::text",
		"AND system_account_id = sqlc.arg(system_account_id)::text",
		"FOR UPDATE",
	} {
		if !strings.Contains(lockSQL, want) {
			t.Fatalf("account tag lock query missing %q", want)
		}
	}
	inUseSQL := querySection(t, sql, "-- name: ManagementAccountTagHasActiveBindings :one", "-- name: DeleteManagementAccountTag :execrows")
	for _, want := range []string{
		"FROM juhe_business.account_tag_bindings AS account_tag_bindings",
		"INNER JOIN juhe_business.accounts AS accounts",
		"accounts.deleted_at IS NULL",
		"LEFT JOIN juhe_business.resource_authorizations AS visible_authorizations",
		"visible_authorizations.status IN ('active', 'paused', 'expired')",
		"account_tag_bindings.tag_id = sqlc.arg(tag_id)::text",
		"account_tag_bindings.system_account_id = sqlc.arg(system_account_id)::text",
		"accounts.authorization_instance_authorization_id IS NULL",
		"OR visible_authorizations.id IS NOT NULL",
	} {
		if !strings.Contains(inUseSQL, want) {
			t.Fatalf("account tag in-use query missing %q", want)
		}
	}
	deleteSQL := querySection(t, sql, "-- name: DeleteManagementAccountTag :execrows", "-- name: LockManagementAccountForTagUpdate :one")
	for _, want := range []string{
		"DELETE FROM juhe_business.account_tags",
		"WHERE id = sqlc.arg(tag_id)::text",
		"AND system_account_id = sqlc.arg(system_account_id)::text",
	} {
		if !strings.Contains(deleteSQL, want) {
			t.Fatalf("account tag delete query missing %q", want)
		}
	}
	for _, forbidden := range []string{"account_tag_bindings", "accounts", "resource_authorizations"} {
		if strings.Contains(deleteSQL, forbidden) {
			t.Fatalf("account tag delete query should only delete tag row, found %q", forbidden)
		}
	}
}

func TestManagementAccountTagUpdateSQLScopesAndReplacesBindings(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_account_tags.sql")
	if err != nil {
		t.Fatalf("read account tags query: %v", err)
	}
	sql := string(source)
	lockSQL := querySection(t, sql, "-- name: LockManagementAccountForTagUpdate :one", "-- name: DeleteManagementAccountTagBindingsForAccount :exec")
	for _, want := range []string{
		"FROM juhe_business.accounts AS accounts",
		"LEFT JOIN juhe_business.resource_authorizations AS authorizations",
		"authorizations.resource_type = 'account'",
		"authorizations.grantee_system_account_id = accounts.system_account_id",
		"authorizations.resource_id = accounts.authorization_instance_source_account_id",
		"accounts.id = sqlc.arg(account_id)::text",
		"accounts.system_account_id = sqlc.arg(system_account_id)::text",
		"accounts.deleted_at IS NULL",
		"accounts.authorization_instance_authorization_id IS NULL",
		"OR authorizations.status IN ('active', 'paused', 'expired')",
		"FOR UPDATE",
	} {
		if !strings.Contains(lockSQL, want) {
			t.Fatalf("account tag update lock query missing %q", want)
		}
	}
	deleteBindingsSQL := querySection(t, sql, "-- name: DeleteManagementAccountTagBindingsForAccount :exec", "-- name: UpsertManagementAccountTagForAccount :one")
	for _, want := range []string{
		"DELETE FROM juhe_business.account_tag_bindings",
		"WHERE account_id = sqlc.arg(account_id)::text",
		"AND system_account_id = sqlc.arg(system_account_id)::text",
	} {
		if !strings.Contains(deleteBindingsSQL, want) {
			t.Fatalf("account tag binding delete query missing %q", want)
		}
	}
	upsertSQL := querySection(t, sql, "-- name: UpsertManagementAccountTagForAccount :one", "-- name: InsertManagementAccountTagBindingForAccount :exec")
	for _, want := range []string{
		"INSERT INTO juhe_business.account_tags",
		"ON CONFLICT (system_account_id, name) DO UPDATE SET",
		"name = EXCLUDED.name",
		"RETURNING id, name, created_at, updated_at",
	} {
		if !strings.Contains(upsertSQL, want) {
			t.Fatalf("account tag upsert query missing %q", want)
		}
	}
	insertBindingSQL := querySection(t, sql, "-- name: InsertManagementAccountTagBindingForAccount :exec", "-- name: GetManagementAccountTagUpdateAccount :one")
	for _, want := range []string{
		"INSERT INTO juhe_business.account_tag_bindings",
		"sqlc.arg(account_id)::text",
		"sqlc.arg(tag_id)::text",
		"sqlc.arg(system_account_id)::text",
		"ON CONFLICT (account_id, tag_id) DO NOTHING",
	} {
		if !strings.Contains(insertBindingSQL, want) {
			t.Fatalf("account tag binding insert query missing %q", want)
		}
	}
	accountSQL := querySection(t, sql, "-- name: GetManagementAccountTagUpdateAccount :one", "-- name: ListManagementAccountTagsForAccount :many")
	for _, want := range []string{
		"accounts.id",
		"accounts.system_account_id",
		"accounts.name",
		"COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id) AS owner_system_account_id",
		"LEFT JOIN juhe_business.resource_authorizations AS authorizations",
		"authorizations.resource_type = 'account'",
		"authorizations.grantee_system_account_id = accounts.system_account_id",
		"authorizations.resource_id = accounts.authorization_instance_source_account_id",
		"accounts.deleted_at IS NULL",
		"accounts.authorization_instance_authorization_id IS NULL",
		"OR authorizations.status IN ('active', 'paused', 'expired')",
	} {
		if !strings.Contains(accountSQL, want) {
			t.Fatalf("account tag update account query missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"source_accounts",
		"accounts.status AS status",
		"AS schedulable",
		"availability_schedule_json",
		"concurrency_limit",
		"credentials",
	} {
		if strings.Contains(accountSQL, forbidden) {
			t.Fatalf("account tag update account query should stay narrow, found %q", forbidden)
		}
	}
}

func TestAccountNameSearchDocumentTerms(t *testing.T) {
	got := make([]string, 0)
	for length := 1; length <= accountNameSearchMaxGramLength; length++ {
		got = append(got, accountNameSearchGrams("测试", length)...)
	}
	if !sameStringSet(got, []string{"测", "试", "测试"}) {
		t.Fatalf("document terms = %#v", got)
	}
}

func sameStringSet(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, value := range got {
		seen[value]++
	}
	for _, value := range want {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}

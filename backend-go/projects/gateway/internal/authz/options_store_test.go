package authz

// authorization-options 家族契约测试：grantee accounts/teams/groups 三面在
// store 层的排序、keyword 前缀、ids 白名单、limit 钳制与 groups 的 enabled +
// grantee active 门禁（Node authorization-options.repository.ts 语义）。

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
	"time"
)

func newOptionsFixture(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active')`,
		`CREATE TABLE system_teams (id TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL DEFAULT 'openai', enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sa-1', 'alice', 'Alice A'), ('sa-2', 'bob', 'Bob B'), ('sa-3', 'carol', 'Carol C')`,
		`UPDATE system_accounts SET status = 'disabled' WHERE id = 'sa-3'`,
		`INSERT INTO system_teams (id, name) VALUES ('team-1', 'Alpha'), ('team-2', 'Beta')`,
		`INSERT INTO groups (id, name, system_account_id, enabled, is_default, updated_at) VALUES
			('g-1', '默认组', 'sa-1', 1, 1, '2026-01-02'),
			('g-2', '备用组', 'sa-1', 1, 0, '2026-01-03'),
			('g-3', '停用组', 'sa-1', 0, 0, '2026-01-04'),
			('g-4', '他户组', 'sa-2', 1, 0, '2026-01-05')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
	store, err := NewStore(db, false, func() time.Time { return time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestGranteeAccountsOptions(t *testing.T) {
	store := newOptionsFixture(t)
	options, err := store.ListAuthorizationGranteeAccounts(context.Background(), authorizationPrincipalOptionListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Disabled principals still list (Node returns every row, status ordered).
	if len(options) != 3 {
		t.Fatalf("options length wrong: %#v", options)
	}
	if options[0].ID != "sa-1" || options[2].Status != "disabled" {
		t.Fatalf("options order wrong: %#v", options)
	}
	// keyword 前缀过滤（username/display_name 双列）。
	keyword, err := store.ListAuthorizationGranteeAccounts(context.Background(), authorizationPrincipalOptionListOptions{Keyword: "bo", Limit: 50})
	if err != nil {
		t.Fatalf("keyword list: %v", err)
	}
	if len(keyword) != 1 || keyword[0].Username != "bob" {
		t.Fatalf("keyword filter wrong: %#v", keyword)
	}
}

func TestGranteeTeamsOptions(t *testing.T) {
	store := newOptionsFixture(t)
	teams, err := store.ListAuthorizationGranteeTeams(context.Background(), authorizationPrincipalOptionListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(teams) != 2 || teams[0].Name != "Alpha" {
		t.Fatalf("teams wrong: %#v", teams)
	}
}

func TestGranteeGroupsOptions(t *testing.T) {
	store := newOptionsFixture(t)
	groups, err := store.ListAuthorizationGranteeGroups(context.Background(), authorizationGranteeGroupOptionListOptions{
		authorizationPrincipalOptionListOptions: authorizationPrincipalOptionListOptions{Limit: 50},
		GranteeSystemAccountID:                  "sa-1",
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 只含 owner 命名空间内 enabled 组，is_default 优先。
	if len(groups) != 2 || groups[0].ID != "g-1" || groups[1].ID != "g-2" {
		t.Fatalf("groups wrong: %#v", groups)
	}
	// preferDefault=false 切换为 updated_at 倒序。
	reordered, err := store.ListAuthorizationGranteeGroups(context.Background(), func() authorizationGranteeGroupOptionListOptions {
		preferDefault := false
		return authorizationGranteeGroupOptionListOptions{
			authorizationPrincipalOptionListOptions: authorizationPrincipalOptionListOptions{Limit: 50},
			GranteeSystemAccountID:                  "sa-1",
			PreferDefault:                           &preferDefault,
			HasPreferDefault:                        true,
		}
	}())
	if err != nil {
		t.Fatalf("reordered: %v", err)
	}
	if len(reordered) != 2 || reordered[0].ID != "g-2" {
		t.Fatalf("reordered groups wrong: %#v", reordered)
	}
	// 空被授权用户 → 空数组（路由层 400 之前的双保险）。
	empty, err := store.ListAuthorizationGranteeGroups(context.Background(), authorizationGranteeGroupOptionListOptions{})
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty grantee should yield no rows: %#v", empty)
	}
}

func TestOptionListParsing(t *testing.T) {
	values := url.Values{}
	values.Add("ids", "b,a")
	values.Add("ids", "c")
	values.Set("keyword", " ali ")
	values.Set("limit", "200")
	options := parseAuthorizationOptionListOptions(values)
	// queryTextList 保序去重（跨重复键），repo 层再排序；路由层保持保序。
	if len(options.IDs) != 3 || options.IDs[0] != "b" || options.IDs[2] != "c" {
		t.Fatalf("ids parsing wrong: %#v", options.IDs)
	}
	if options.Keyword != "ali" || options.Limit != 50 {
		t.Fatalf("keyword/limit parsing wrong: %#v", options)
	}
	missing := parseAuthorizationOptionListOptions(func() url.Values {
		values := url.Values{}
		values.Set("limit", "0")
		return values
	}())
	if missing.Limit != 1 {
		t.Fatalf("limit clamp wrong: %#v", missing.Limit)
	}
	absent := parseAuthorizationOptionListOptions(url.Values{})
	if absent.Limit != 50 {
		t.Fatalf("default limit wrong: %#v", absent.Limit)
	}
}

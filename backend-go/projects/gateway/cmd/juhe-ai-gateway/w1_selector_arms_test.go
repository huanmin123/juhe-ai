package main

// chain_accounts.go selector 极限分支覆盖：resolveGroupAccess 的
// 查询/空值/解析错误路径，resolveChainAccountAccess 的授权实例、
// prefetch map、owner/authorized/group_authorized 访问类型分支，
// ListOpenAIAccountsForGroupResult hydration batch dropping，
// loadAccountAuthorizationsForSelection authorized group 返回 nil map。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// resolveGroupAccess 错误路径覆盖
// ---------------------------------------------------------------------------

// TestW1ResolveGroupAccessQueryError 覆盖：查询 groups 表失败（列缺失）。
func TestW1ResolveGroupAccessQueryError(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// 缺列的 groups 表结构，触发查询错误
	if _, err := db.Exec(`CREATE TABLE groups (id TEXT, system_account_id TEXT)`); err != nil {
		t.Fatalf("create groups: %v", err)
	}
	selector, err := newChainAccountsSelector(db, false, "test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	_, err = selector.resolveGroupAccess(context.Background(), "nonexistent", "sys")
	if err == nil {
		t.Fatal("query error must surface")
	}
}

// TestW1ResolveGroupAccessEmptyOwner 覆盖：ownerID 为空时返回 nil。
func TestW1ResolveGroupAccessEmptyOwner(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT, provider_code TEXT, enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('g1', '', 'openai', 1, 'personal', NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	selector, err := newChainAccountsSelector(db, false, "test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	meta, err := selector.resolveGroupAccess(context.Background(), "g1", "sys")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta != nil {
		t.Fatalf("must return nil for empty owner: %+v", meta)
	}
}

// TestW1ResolveGroupAccessDisabledGroup 覆盖：enabled != 1 返回 nil。
func TestW1ResolveGroupAccessDisabledGroup(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT, provider_code TEXT, enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('g2', 'sys', 'openai', 0, 'personal', NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	selector, err := newChainAccountsSelector(db, false, "test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	meta, err := selector.resolveGroupAccess(context.Background(), "g2", "sys")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta != nil {
		t.Fatalf("must return nil for disabled group: %+v", meta)
	}
}

// TestW1ResolveGroupAccessBadGroupType 覆盖：group type 解析错误返回错误。
func TestW1ResolveGroupAccessBadGroupType(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT, provider_code TEXT, enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('g3', 'sys', 'openai', 1, 'invalid-type', NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	selector, err := newChainAccountsSelector(db, false, "test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	_, err = selector.resolveGroupAccess(context.Background(), "g3", "sys")
	if err == nil {
		t.Fatal("bad group type must return error")
	}
}

// TestW1ResolveGroupAccessAuthorizedGroupLocalSettings 覆盖：
// 授权组 + local settings enabled=0 时返回 nil。
func TestW1ResolveGroupAccessAuthorizedGroupLocalSettings(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT, provider_code TEXT, enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT)`,
		`CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, resource_owner_system_account_id TEXT,
			grantee_system_account_id TEXT, scope TEXT, status TEXT, expires_at TEXT,
			effective_source_type TEXT, effective_source_team_id TEXT, limits_json TEXT
		)`,
		`CREATE TABLE group_authorization_settings (
			authorization_id TEXT, system_account_id TEXT, group_id TEXT,
			enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('g_auth', 'sys_owner', 'openai', 1, 'personal', NULL)`); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations VALUES ('auth_1', 'group', 'g_auth', 'sys_owner', 'grantee_1', 'use', 'active', NULL, NULL, NULL, NULL)`); err != nil {
		t.Fatalf("insert authz: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO group_authorization_settings VALUES ('auth_1', 'grantee_1', 'g_auth', 0, NULL, NULL)`); err != nil {
		t.Fatalf("insert settings: %v", err)
	}
	selector, err := newChainAccountsSelector(db, false, "test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	meta, err := selector.resolveGroupAccess(context.Background(), "g_auth", "grantee_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta != nil {
		t.Fatalf("must return nil for local-enabled=0: %+v", meta)
	}
}

// ---------------------------------------------------------------------------
// resolveChainAccountAccess 分支覆盖
// ---------------------------------------------------------------------------

// TestW1ResolveChainAccountAccessAuthorizationInstanceOwnerMismatch 覆盖：
// 授权实例存在但 owner 与 caller 不匹配时，该账户被过滤。
func TestW1ResolveChainAccountAccessAuthorizationInstanceOwnerMismatch(t *testing.T) {
	fixture := newChainFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO accounts (
		id, system_account_id, provider_code, name, type, status, schedulable,
		authorization_instance_source_account_id, authorization_instance_authorization_id,
		authorization_instance_owner_system_account_id, deleted_at
	) VALUES ('acc_authz_inst', 'sys_owner', 'openai', '授权实例账户', 'api_key', 'active', 1,
		'src_acc', 'authz_id_1', 'other_owner', NULL)`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES (?, 'sys_owner', 'acc_authz_inst', 1, '2026-01-01T00:00:00.000Z')`, fixture.groupID); err != nil {
		t.Fatalf("insert group_accounts: %v", err)
	}
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, "grantee_2",
		gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, acc := range result.Accounts {
		if acc.ID == "acc_authz_inst" {
			t.Fatal("account with mismatched owner must be filtered")
		}
	}
}

// TestW1ResolveChainAccountAccessAuthorizedGroupOwnerAccount 覆盖：
// 授权组中，账户 owner 等于 group owner 时返回 group_authorized。
func TestW1ResolveChainAccountAccessAuthorizedGroupOwnerAccount(t *testing.T) {
	fixture := newChainFixture(t)
	grantee := "sys_grantee_group"
	if _, err := fixture.db.Exec(`INSERT INTO system_accounts (id, status, image_generation_enabled) VALUES (?, 'active', 1)`, grantee); err != nil {
		t.Fatalf("insert grantee: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		scope, status, expires_at, effective_source_type, effective_source_team_id, limits_json
	) VALUES ('authz_g2', 'group', ?, ?, ?, 'use', 'active', '2999-01-01T00:00:00.000Z', NULL, NULL, NULL)`,
		fixture.groupID, fixture.systemAccount, grantee); err != nil {
		t.Fatalf("insert authz: %v", err)
	}
	groupAccess, err := fixture.selector.resolveGroupAccess(context.Background(), fixture.groupID, grantee)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	if groupAccess == nil || groupAccess.GroupAccessType != gatewayruntimecache.GroupAccessTypeAuthorized {
		t.Fatalf("group access: %+v", groupAccess)
	}
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, grantee,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: groupAccess})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(result.Accounts))
	}
	if result.Accounts[0].AccountAccessType != "group_authorized" {
		t.Fatalf("accountAccessType = %q, want group_authorized", result.Accounts[0].AccountAccessType)
	}
}

// TestW1ResolveChainAccountAccessAuthorizedGroupNonOwnerFiltered 覆盖：
// 授权组中，账户 owner 不等于 group owner 时被过滤。
func TestW1ResolveChainAccountAccessAuthorizedGroupNonOwnerFiltered(t *testing.T) {
	fixture := newChainFixture(t)
	credentials, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"api_key": "sk-other"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO accounts (
		id, system_account_id, provider_code, name, type, status, schedulable, credentials_encrypted, deleted_at
	) VALUES ('acc_other_owner', 'sys_other', 'openai', '第三方账户', 'api_key', 'active', 1, ?, NULL)`, credentials); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES (?, 'sys_owner', 'acc_other_owner', 1, '2026-01-01T00:00:00.000Z')`, fixture.groupID); err != nil {
		t.Fatalf("insert group_accounts: %v", err)
	}
	grantee := "sys_grantee_filtered"
	if _, err := fixture.db.Exec(`INSERT INTO system_accounts (id, status, image_generation_enabled) VALUES (?, 'active', 1)`, grantee); err != nil {
		t.Fatalf("insert grantee: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		scope, status, expires_at, effective_source_type, effective_source_team_id, limits_json
	) VALUES ('authz_filtered', 'group', ?, ?, ?, 'use', 'active', '2999-01-01T00:00:00.000Z', NULL, NULL, NULL)`,
		fixture.groupID, fixture.systemAccount, grantee); err != nil {
		t.Fatalf("insert authz: %v", err)
	}
	groupAccess, err := fixture.selector.resolveGroupAccess(context.Background(), fixture.groupID, grantee)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	if groupAccess == nil {
		t.Fatal("group access missing")
	}
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, grantee,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: groupAccess})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, acc := range result.Accounts {
		if acc.ID == "acc_other_owner" {
			t.Fatal("non-owner account must be filtered in authorized group")
		}
	}
}

// TestW1ResolveChainAccountAccessOwnerScopeWithPrefetch 覆盖：
// 授权实例分支下 prefetch map 查找 authorization 并返回 account_authorized。
func TestW1ResolveChainAccountAccessOwnerScopeWithPrefetch(t *testing.T) {
	fixture := newChainFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		scope, status, expires_at, effective_source_type, effective_source_team_id, limits_json
	) VALUES ('authz_prefetch_id', 'account', 'acc_authz_prefetch', 'sys_owner', 'sys_owner',
		'use', 'active', '2999-01-01T00:00:00.000Z', 'team', 'team_1', NULL)`); err != nil {
		t.Fatalf("insert authz: %v", err)
	}
	row := &chainCandidateRow{
		ID:                                   "acc_authz_prefetch",
		SystemAccountID:                      "sys_owner",
		AuthorizationInstanceAuthorizationID: sql.NullString{String: "authz_prefetch_id", Valid: true},
	}
	access, err := fixture.selector.resolveChainAccountAccess(context.Background(), row, "sys_owner",
		&gatewayruntimecache.GroupUsageAccessMetadata{
			GroupOwnerSystemAccountID: "sys_owner",
			GroupAccessType:           gatewayruntimecache.GroupAccessTypeOwner,
		}, "", nil)
	if err != nil {
		t.Fatalf("resolve access: %v", err)
	}
	if access == nil {
		t.Fatal("access must not be nil")
	}
	if access.accountAccessType != chainAccountAccessAuthorized {
		t.Fatalf("accountAccessType = %q, want account_authorized", access.accountAccessType)
	}
	if access.accountAuthorizationID == nil || *access.accountAuthorizationID != "authz_prefetch_id" {
		t.Fatalf("accountAuthorizationID = %v", access.accountAuthorizationID)
	}
}

// ---------------------------------------------------------------------------
// loadAccountAuthorizationsForSelection 分支覆盖
// ---------------------------------------------------------------------------

// TestW1LoadAccountAuthorizationsForSelectionAuthorizedGroupReturnsNil 覆盖：
// authorized group 时返回 nil map（不做 prefetch）。
func TestW1LoadAccountAuthorizationsForSelectionAuthorizedGroupReturnsNil(t *testing.T) {
	fixture := newChainFixture(t)
	grantee := "sys_grantee_no_prefetch"
	if _, err := fixture.db.Exec(`INSERT INTO system_accounts (id, status, image_generation_enabled) VALUES (?, 'active', 1)`, grantee); err != nil {
		t.Fatalf("insert grantee: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		scope, status, expires_at, effective_source_type, effective_source_team_id, limits_json
	) VALUES ('authz_no_pf', 'group', ?, ?, ?, 'use', 'active', '2999-01-01T00:00:00.000Z', NULL, NULL, NULL)`,
		fixture.groupID, fixture.systemAccount, grantee); err != nil {
		t.Fatalf("insert authz: %v", err)
	}
	groupAccess, err := fixture.selector.resolveGroupAccess(context.Background(), fixture.groupID, grantee)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	if groupAccess == nil {
		t.Fatal("group access missing")
	}
	authorizations, err := fixture.selector.loadAccountAuthorizationsForSelection(context.Background(), nil, groupAccess, grantee)
	if err != nil {
		t.Fatalf("load authorizations: %v", err)
	}
	if authorizations != nil {
		t.Fatalf("authorized group must return nil map, got: %+v", authorizations)
	}
}

// ---------------------------------------------------------------------------
// ListOpenAIAccountsForGroupResult hydration batch dropping 覆盖
// ---------------------------------------------------------------------------

// TestW1ListAccountsHydrationDroppedCount 覆盖：无凭据账户被丢弃并计数。
func TestW1ListAccountsHydrationDroppedCount(t *testing.T) {
	fixture := newChainFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO accounts (
		id, system_account_id, provider_code, name, type, status, schedulable,
		credentials_encrypted, deleted_at
	) VALUES ('acc_no_creds', 'sys_owner', 'openai', '无凭据账户', 'api_key', 'active', 1,
		NULL, NULL)`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES (?, 'sys_owner', 'acc_no_creds', 1, '2026-01-01T00:00:00.000Z')`, fixture.groupID); err != nil {
		t.Fatalf("insert group_accounts: %v", err)
	}
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, acc := range result.Accounts {
		if acc.ID == "acc_no_creds" {
			t.Fatal("account without credentials must be dropped")
		}
	}
	if result.Diagnostics == nil {
		t.Fatal("diagnostics missing")
	}
	if result.Diagnostics.HydrationDroppedCount < 1 {
		t.Fatalf("hydrationDroppedCount = %d, want >= 1", result.Diagnostics.HydrationDroppedCount)
	}
}

// ---------------------------------------------------------------------------
// 可用性门错误路径
// ---------------------------------------------------------------------------

func TestW1ChainAccountAvailableForSelectionBadTimeFormat(t *testing.T) {
	row := &chainCandidateRow{Schedulable: 1, Status: "active"}
	if _, err := chainAccountAvailableForSelection(row, "invalid-time", false); err == nil {
		t.Fatal("bad time format must return error")
	}
}

func TestW1ChainPhysicalAccountAvailableBadExpiresAt(t *testing.T) {
	row := &chainCandidateRow{Schedulable: 1, Status: "active",
		AccountExpiresAt: sql.NullString{String: "bad-time", Valid: true}}
	if _, err := chainPhysicalAccountAvailable(row, 0, false); err == nil {
		t.Fatal("bad expires_at must return error")
	}
}

func TestW1ChainPhysicalAccountAvailableExpired(t *testing.T) {
	row := &chainCandidateRow{Schedulable: 1, Status: "active",
		AccountExpiresAt: sql.NullString{String: "2020-01-01T00:00:00.000Z", Valid: true}}
	available, err := chainPhysicalAccountAvailable(row, 1700000000000, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if available {
		t.Fatal("expired account must not be available")
	}
}

func TestW1ChainPhysicalAccountAvailableNotSchedulable(t *testing.T) {
	row := &chainCandidateRow{Schedulable: 0, Status: "active"}
	available, err := chainPhysicalAccountAvailable(row, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if available {
		t.Fatal("non-schedulable account must not be available")
	}
}

func TestW1ChainPhysicalAccountAvailableIncludeUnavailable(t *testing.T) {
	for _, status := range []string{"active", "rate_limited", "temporary_unavailable"} {
		row := &chainCandidateRow{Schedulable: 1, Status: status}
		available, err := chainPhysicalAccountAvailable(row, 0, true)
		if err != nil {
			t.Fatalf("status=%s unexpected error: %v", status, err)
		}
		if !available {
			t.Fatalf("status=%s must be available with includeUnavailable", status)
		}
	}
}

func TestW1ChainPhysicalAccountAvailableBadCooldown(t *testing.T) {
	row := &chainCandidateRow{Schedulable: 1, Status: "active",
		CooldownUntil: sql.NullString{String: "bad-time", Valid: true}}
	if _, err := chainPhysicalAccountAvailable(row, 0, false); err == nil {
		t.Fatal("bad cooldown must return error")
	}
}

func TestW1ChainResourceAccountAvailableBadResourceExpires(t *testing.T) {
	row := &chainCandidateRow{
		AuthorizationInstanceAuthorizationID: sql.NullString{String: "authz_1", Valid: true},
		ResourceAccountID:                    sql.NullString{String: "res_1", Valid: true},
		ResourceStatus:                       sql.NullString{String: "active", Valid: true},
		ResourceAccountExpiresAt:             sql.NullString{String: "bad-time", Valid: true},
		ResourceSchedulable:                  sql.NullInt64{Int64: 1, Valid: true},
	}
	if _, err := chainResourceAccountAvailable(row, 0, false); err == nil {
		t.Fatal("bad resource expires must return error")
	}
}

func TestW1ChainResourceAccountAvailableBadResourceCooldown(t *testing.T) {
	row := &chainCandidateRow{
		AuthorizationInstanceAuthorizationID: sql.NullString{String: "authz_1", Valid: true},
		ResourceAccountID:                    sql.NullString{String: "res_1", Valid: true},
		ResourceStatus:                       sql.NullString{String: "active", Valid: true},
		ResourceSchedulable:                  sql.NullInt64{Int64: 1, Valid: true},
		ResourceCooldownUntil:                sql.NullString{String: "bad-time", Valid: true},
	}
	if _, err := chainResourceAccountAvailable(row, 0, false); err == nil {
		t.Fatal("bad resource cooldown must return error")
	}
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

func TestW1ActiveResourceAuthorizationsByIDsEmptyList(t *testing.T) {
	fixture := newChainFixture(t)
	result, err := fixture.selector.activeResourceAuthorizationsByIDs(context.Background(), []string{}, "sys")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("empty list must return empty map, got: %+v", result)
	}
}

func TestW1UniqueNonEmptyWithDuplicatesAndBlanks(t *testing.T) {
	input := []string{"a", "", "b", "a", "  ", "c", "b"}
	result := uniqueNonEmpty(input)
	expected := []string{"a", "b", "c"}
	if len(result) != len(expected) {
		t.Fatalf("result length = %d, want %d: %+v", len(result), len(expected), result)
	}
	for i, v := range result {
		if v != expected[i] {
			t.Fatalf("result[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestW1RowsArgsConversion(t *testing.T) {
	input := []string{"x", "y", "z"}
	args := rowsArgs(input)
	if len(args) != 3 {
		t.Fatalf("args length = %d, want 3", len(args))
	}
	for i, v := range args {
		if s, ok := v.(string); !ok || s != input[i] {
			t.Fatalf("args[%d] = %v, want %q", i, v, input[i])
		}
	}
}

func TestW1NullStringPtrValidAndInvalid(t *testing.T) {
	valid := nullStringPtr(sql.NullString{String: "hello", Valid: true})
	if valid == nil || *valid != "hello" {
		t.Fatalf("valid NullString = %v", valid)
	}
	invalid := nullStringPtr(sql.NullString{Valid: false})
	if invalid != nil {
		t.Fatalf("invalid NullString must return nil, got: %v", invalid)
	}
}

func TestW1ChainProxyURLFromRowSocks5(t *testing.T) {
	url, err := chainProxyURLFromRow("secret", "socks5", "10.0.0.1", 1080, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "socks5h://10.0.0.1:1080" {
		t.Fatalf("socks5 URL = %q, want socks5h://10.0.0.1:1080", url)
	}
}

func TestW1ChainProxyURLFromRowWithCredentials(t *testing.T) {
	encrypted, err := accounts.EncryptJSON("secret", map[string]any{"password": "pass123"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	url, err := chainProxyURLFromRow("secret", "http", "proxy.example.com", 8080, "user", encrypted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "http://user:pass123@proxy.example.com:8080" {
		t.Fatalf("credentials URL = %q", url)
	}
}

func TestW1ChainProxyURLFromRowBadPassword(t *testing.T) {
	_, err := chainProxyURLFromRow("secret", "http", "proxy.example.com", 8080, "user", "invalid-encrypted")
	if err == nil {
		t.Fatal("bad encrypted password must return error")
	}
}

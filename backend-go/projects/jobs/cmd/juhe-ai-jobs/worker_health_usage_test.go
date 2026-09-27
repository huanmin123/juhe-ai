package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// probeUsageFixtureNow 是作用域解析 fixture 的固定时钟（过期用例以此为基准）。
var probeUsageFixtureNow = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func newProbeUsageScopeFixture(t *testing.T) *probeUsageWriter {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	ddl := []string{
		`CREATE TABLE accounts (id text PRIMARY KEY, deleted_at text, provider_code text,
			system_account_id text, authorization_instance_authorization_id text)`,
		`CREATE TABLE resource_authorizations (id text PRIMARY KEY, resource_type text,
			resource_id text, grantee_system_account_id text, resource_owner_system_account_id text,
			status text, effective_source_team_id text, expires_at text)`,
		`CREATE TABLE group_accounts (group_id text, account_id text, system_account_id text,
			enabled integer, account_authorization_id text, updated_at text)`,
		`CREATE TABLE groups (id text PRIMARY KEY, system_account_id text)`,
	}
	for _, statement := range ddl {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("create fixture table: %v", err)
		}
	}
	seed := []struct{ statement string }{
		{`INSERT INTO accounts VALUES ('acc-owner', NULL, 'openai', 'sysacc-owner', NULL)`},
		{`INSERT INTO accounts VALUES ('acc-instance', NULL, 'openai', 'sysacc-grantee', 'ra-acc-1')`},
		{`INSERT INTO accounts VALUES ('acc-gone', '2026-01-01', 'openai', 'sysacc-owner', NULL)`},
		{`INSERT INTO accounts VALUES ('acc-exp', NULL, 'openai', 'sysacc-owner', NULL)`},
		{`INSERT INTO accounts VALUES ('acc-exp-null', NULL, 'openai', 'sysacc-owner', NULL)`},
		{`INSERT INTO resource_authorizations VALUES ('ra-acc-1', 'account', 'acc-src-1', 'sysacc-grantee', 'sysacc-src-owner', 'active', 'team-1', NULL)`},
		{`INSERT INTO resource_authorizations VALUES ('ra-group-1', 'group', 'group-1', 'sysacc-owner', 'sysacc-group-owner', 'active', '', '2027-01-01T00:00:00.000Z')`},
		{`INSERT INTO resource_authorizations VALUES ('ra-group-2', 'group', 'group-2', 'sysacc-owner', 'sysacc-group-owner', 'active', '', '2026-01-01T00:00:00.000Z')`},
		{`INSERT INTO resource_authorizations VALUES ('ra-group-3', 'group', 'group-3', 'sysacc-owner', 'sysacc-group-owner', 'active', '', NULL)`},
		{`INSERT INTO group_accounts VALUES ('group-1', 'acc-owner', 'sysacc-owner', 1, NULL, '2026-09-27T00:00:00.000Z')`},
		{`INSERT INTO group_accounts VALUES ('group-2', 'acc-exp', 'sysacc-owner', 1, NULL, '2026-09-27T00:00:00.000Z')`},
		{`INSERT INTO group_accounts VALUES ('group-3', 'acc-exp-null', 'sysacc-owner', 1, NULL, '2026-09-27T00:00:00.000Z')`},
		{`INSERT INTO groups VALUES ('group-1', 'sysacc-group-owner')`},
		{`INSERT INTO groups VALUES ('group-2', 'sysacc-group-owner')`},
		{`INSERT INTO groups VALUES ('group-3', 'sysacc-group-owner')`},
	}
	for _, row := range seed {
		if _, err := db.ExecContext(context.Background(), row.statement); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}
	business, err := openBusinessDBHandleForTest(db)
	if err != nil {
		t.Fatalf("business handle: %v", err)
	}
	return &probeUsageWriter{
		business: business,
		clock:    usagewriter.ClockFunc(func() time.Time { return probeUsageFixtureNow }),
	}
}

// openBusinessDBHandleForTest 以 SQLite 形态构造业务库窄口（测试专用）。
func openBusinessDBHandleForTest(db *sql.DB) (*businessDB, error) {
	return &businessDB{db: db, postgres: false}, nil
}

// TestProbeUsageResolveScope 锚定作用域解析：owner/authorized 元组与分组
// 授权解析，归一化后全部存活（不被整组清空）。
func TestProbeUsageResolveScope(t *testing.T) {
	writer := newProbeUsageScopeFixture(t)
	ctx := context.Background()

	// 自有账户 → owner 元组 + 分组元组（分组授权 ra-group-1，manual 来源）。
	input := usagewriter.UsageRecordInput{AccountID: "acc-owner", SystemAccountID: "sysacc-owner"}
	writer.resolveScope(ctx, "acc-owner", &input)
	normalized, err := usagewriter.NormalizeUsageRecordInput(input, usagewriter.SystemClock{}, nil)
	if err != nil {
		t.Fatalf("normalize owner scope: %v", err)
	}
	if normalized.AccountID != "acc-owner" || normalized.AccountAccessType != usagewriter.AccountAccessTypeOwner ||
		normalized.AccountOwnerSystemAccountID != "sysacc-owner" {
		t.Fatalf("owner account scope survived: %+v", normalized)
	}
	if normalized.GroupID != "group-1" || normalized.GroupAccessType != usagewriter.GroupAccessTypeAuthorized ||
		normalized.GroupOwnerSystemAccountID != "sysacc-group-owner" || normalized.GroupAuthorizationID != "ra-group-1" {
		t.Fatalf("group scope survived: %+v", normalized)
	}

	// 授权实例账户 → account_authorized 元组（team 来源，团队 ID 必填）。
	input = usagewriter.UsageRecordInput{AccountID: "acc-instance", SystemAccountID: "sysacc-grantee"}
	writer.resolveScope(ctx, "acc-instance", &input)
	normalized, err = usagewriter.NormalizeUsageRecordInput(input, usagewriter.SystemClock{}, nil)
	if err != nil {
		t.Fatalf("normalize instance scope: %v", err)
	}
	if normalized.AccountAccessType != usagewriter.AccountAccessTypeAccountAuthorized ||
		normalized.AccountOwnerSystemAccountID != "sysacc-src-owner" ||
		normalized.AccountAuthorizationID != "ra-acc-1" ||
		normalized.AccountAuthorizationSourceType != usagewriter.AuthorizationSourceTypeTeam ||
		normalized.AccountAuthorizationSourceTeamID != "team-1" {
		t.Fatalf("instance account scope survived: %+v", normalized)
	}
	if normalized.ProviderCode != "openai" {
		t.Fatalf("provider from accounts row = %q", normalized.ProviderCode)
	}

	// 已删除账户：解析静默跳过，无作用域 → 归一化清空账户元组（不伪造）。
	input = usagewriter.UsageRecordInput{AccountID: "acc-gone", SystemAccountID: "sysacc-owner"}
	writer.resolveScope(ctx, "acc-gone", &input)
	normalized, err = usagewriter.NormalizeUsageRecordInput(input, usagewriter.SystemClock{}, nil)
	if err != nil {
		t.Fatalf("normalize deleted account: %v", err)
	}
	if normalized.AccountID != "" {
		t.Fatalf("deleted account scope must be cleared, got %+v", normalized)
	}
}

// TestProbeUsageResolveScopeGroupAuthorizationExpiry 锚定分组授权过期过滤与
// 读侧（statreads/accountusage.go）同口径：已过期授权不再标注 group 作用域，
// 未过期与 NULL（永不过期）授权正常标注。
func TestProbeUsageResolveScopeGroupAuthorizationExpiry(t *testing.T) {
	writer := newProbeUsageScopeFixture(t)
	ctx := context.Background()

	// 已过期分组授权（ra-group-2 于 fixture now 之前过期）→ group 作用域
	// 整组不标注，账户 owner 元组保留。
	input := usagewriter.UsageRecordInput{AccountID: "acc-exp", SystemAccountID: "sysacc-owner"}
	writer.resolveScope(ctx, "acc-exp", &input)
	normalized, err := usagewriter.NormalizeUsageRecordInput(input, usagewriter.SystemClock{}, nil)
	if err != nil {
		t.Fatalf("normalize expired group auth: %v", err)
	}
	if normalized.AccountAccessType != usagewriter.AccountAccessTypeOwner ||
		normalized.AccountOwnerSystemAccountID != "sysacc-owner" {
		t.Fatalf("account scope must survive expired group auth: %+v", normalized)
	}
	if normalized.GroupID != "" || normalized.GroupAuthorizationID != "" {
		t.Fatalf("expired group authorization must not annotate group scope: %+v", normalized)
	}

	// 未过期分组授权（ra-group-1 到 2027 年）→ 正常标注 group 作用域。
	input = usagewriter.UsageRecordInput{AccountID: "acc-owner", SystemAccountID: "sysacc-owner"}
	writer.resolveScope(ctx, "acc-owner", &input)
	normalized, err = usagewriter.NormalizeUsageRecordInput(input, usagewriter.SystemClock{}, nil)
	if err != nil {
		t.Fatalf("normalize unexpired group auth: %v", err)
	}
	if normalized.GroupID != "group-1" || normalized.GroupAuthorizationID != "ra-group-1" {
		t.Fatalf("unexpired group authorization must annotate group scope: %+v", normalized)
	}

	// NULL expires_at（永不过期）→ 正常标注 group 作用域。
	input = usagewriter.UsageRecordInput{AccountID: "acc-exp-null", SystemAccountID: "sysacc-owner"}
	writer.resolveScope(ctx, "acc-exp-null", &input)
	normalized, err = usagewriter.NormalizeUsageRecordInput(input, usagewriter.SystemClock{}, nil)
	if err != nil {
		t.Fatalf("normalize null-expires group auth: %v", err)
	}
	if normalized.GroupID != "group-3" || normalized.GroupAuthorizationID != "ra-group-3" {
		t.Fatalf("null-expires group authorization must annotate group scope: %+v", normalized)
	}
}

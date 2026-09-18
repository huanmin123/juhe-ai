// w13g2 failpoint 第三批：team 级联、quota 绑定构建、usage fallback、
// return_group 与统计聚合的剩余深层分支。附不可达登记：
//   - routes.go:241-243 RequireSelf auth==nil 守卫：sessionMiddleware 全部
//     放行路径均注入 AuthContext（见 w13g2_routes_gaps_test.go 头注）。
//   - mutations.go:880-882 randomSuffix 的 crand.Read panic：crypto/rand
//     失败即系统级故障，测试不可注入，登记不覆盖。
//   - mutations.go:486-489 patchForOwner 的 instantMilliseconds 兜底：
//     now 恒为 NextVersion 产出的合法 RFC3339 instant，ok 恒为 true。
//   - usage_routes.go usageQueryFailure 的 fallback 分支（126-127）：全部
//     调用点的 parser.fail 均已写入 issue，issue 为空时不可达。
//   - stats_loader.go:106-108 set 的 LRU oldest==nil break：循环条件保证
//     len>0，Back() 恒非 nil。
//   - expiry_reconcile.go:73-75/79-85：BeginTx 失败与"事务内读态已翻转"
//     的并发窗口，单连接串行 fixture 无法构造。
package authz

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestW13g2FailpointTeamCascade(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	for _, id := range []string{"owner", "tc1", "tc2"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_tc13', 'T', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_tc13', 'T', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"tc1", "tc2"} {
		if _, err := db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
			VALUES (?, 'team_tc13', ?, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, "teammem_tc_" + member, member); err != nil {
			t.Fatal(err)
		}
	}

	// team 授权：建 runtime + team source（两成员）。
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_tc13",
		GranteeType: "team", GranteeID: "team_tc13",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// RevokeTeamSourcesForMemberTx 成功路径（188-233）：tc1 的 team sources
	// 被回收并刷新。
	if err := s.RevokeTeamSourcesForMember(ctx, "team_tc13", "tc1", "owner"); err != nil {
		t.Fatalf("member 回收应成功: %v", err)
	}

	// revokeTeamGrantSources（sync.go 230-283）：Revoke 触发。
	if _, err := s.Revoke(ctx, created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
		t.Fatalf("revoke 应成功: %v", err)
	}

	// 超限数据：21 条 tc1 链接的 active team sources → member 回收超限
	// failf（team_cascade.go 214-216）。
	for i := 0; i < 21; i++ {
		suffix := string(rune('a' + i/26)) + string(rune('a' + i%26))
		if _, err := db.Exec(`INSERT INTO resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, activated_at, created_by, created_at, updated_at)
			VALUES (?, 'group', ?, 'owner', ?, 'use', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			"rt_tc13_" + suffix, "grp_tc13_" + suffix, "tc1"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorization_sources
			(id, authorization_id, source_type, source_team_id, status, activated_at, created_by, created_at, updated_at)
			VALUES (?, ?, 'team', 'team_tc13', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			"src_tc13_" + suffix, "rt_tc13_" + suffix); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RevokeTeamSourcesForMember(ctx, "team_tc13", "tc1", "owner"); err == nil || !strings.Contains(err.Error(), "最多支持") {
		t.Fatalf("member 超限应 failf: %v", err)
	}

	// 追加到 401 条 → revokeAllTeamSourcesTx 的 members×grants 上限 failf
	//（team_cascade.go 84-86）。
	for i := 21; i < 401; i++ {
		suffix := string(rune('a' + i/26)) + string(rune('a' + i%26))
		if _, err := db.Exec(`INSERT INTO resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, activated_at, created_by, created_at, updated_at)
			VALUES (?, 'group', ?, 'owner', 'tc2', 'use', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			"rt_tc13_" + suffix, "grp_tc13_" + suffix); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorization_sources
			(id, authorization_id, source_type, source_team_id, status, activated_at, created_by, created_at, updated_at)
			VALUES (?, ?, 'team', 'team_tc13', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			"src_tc13_" + suffix, "rt_tc13_" + suffix); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RevokeAllTeamSources(ctx, "team_tc13", "owner", "team_disabled"); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("revokeAll 超限应 failf: %v", err)
	}

	// failpoint：revokeAll 的 UPDATE 与 refresh 失败（89-99）。
	tx := mustTx(t, db)
	fp.arm("COALESCE(ended_reason, ?)")
	if err := s.RevokeAllTeamSourcesTx(ctx, tx, "team_tc13", "owner", "2026-01-01T00:00:00Z", "team_disabled"); err == nil {
		tx.Rollback()
		t.Fatalf("应失败")
	}
	tx.Rollback()
	fp.disarm()
	tx = mustTx(t, db)
	fp.arm("trg.expires_at > ?")
	defer fp.disarm()
	if err := s.RevokeTeamSourcesForMemberTx(ctx, tx, "team_tc13", "tc2", "owner", "2026-01-01T00:00:00Z"); err == nil {
		tx.Rollback()
		t.Fatalf("应失败")
	}
	tx.Rollback()
}

func mustTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestW13g2QuotaBindingBuild 覆盖 syncGrantQuotaScopeBindings 的绑定构建
//（downstream.go 196-248）：hourly limits 生成 account/group 授权绑定。
func TestW13g2QuotaBindingBuild(t *testing.T) {
	s, _ := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	for _, id := range []string{"owner", "qb1"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_qb13', 'Q', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	hourly := `{"hourly":{"enabled":true,"hours":5,"limit":100}}`
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_qb13",
		GranteeType: "system_account", GranteeID: "qb1",
		LimitsJSON: &hourly,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	var bindingCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings WHERE scope_type = 'group_authorization'`).Scan(&bindingCount); err != nil || bindingCount != 1 {
		t.Fatalf("group_authorization 绑定应生成: %d %v", bindingCount, err)
	}

	// team 授权 + hourly → group_authorization_team 绑定（237-246）。
	if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_qb13', 'T', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES ('teammem_qb13', 'team_qb13', 'qb1', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_qb13",
		GranteeType: "team", GranteeID: "team_qb13",
		LimitsJSON: &hourly,
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	var teamBinding int
	if err := db.QueryRow(`SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings WHERE scope_type = 'group_authorization_team'`).Scan(&teamBinding); err != nil || teamBinding != 1 {
		t.Fatalf("group_authorization_team 绑定应生成: %d %v", teamBinding, err)
	}
	_ = created
}

// TestW13g2UsageFallbackWithInstance 覆盖 loadTeamUsageFallbackSummary 的
// account 分支主体（usage_detail.go 599-637）：实例映射 + scope 汇总合并。
func TestW13g2UsageFallbackWithInstance(t *testing.T) {
	s, _ := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	if _, err := db.Exec(usageFixtureDDL); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "uf1"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_src', 'owner', '源', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status, provider_code, enabled, is_default, updated_at)
		VALUES ('tg_uf13', 'D', 'uf1', 'active', 'gpt', 1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_uf13', 'T', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES ('teammem_uf13', 'team_uf13', 'uf1', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	created, err := s.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_src",
		GranteeType: "system_account", GranteeID: "uf1",
		TargetGroupID: func() *string { v := "tg_uf13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// team 授权同资源 → gm 成员 runtime + instance 链。
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_src",
		GranteeType: "team", GranteeID: "team_uf13",
	}, "owner"); err != nil {
		t.Fatal(err)
	}

	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}
	teamGrant := &grantRow{ResourceType: "account", ResourceID: "acc_src", OwnerID: "owner",
		GranteeType: "team", GranteeTeamID: sql.NullString{String: "team_uf13", Valid: true}}
	usage, err := s.loadTeamUsageFallbackSummary(ctx, teamGrant, "team_uf13", rng)
	if err != nil {
		t.Fatalf("fallback 应成功: %v", err)
	}
	_ = usage
	_ = created

	// statsQueryDB 无注入句柄时回退业务句柄（usage_reads.go 145-150）。
	bare := &Store{db: s.db, statsCache: newAuthorizationStatsCache(time.Now)}
	if bare.statsQueryDB() != s.db {
		t.Fatalf("statsQueryDB 应回退业务句柄")
	}
}

// TestW13g2FailpointUsageArms 覆盖 usage 读族的深层错误臂。
func TestW13g2FailpointUsageArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	if _, err := db.Exec(usageFixtureDDL); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "ua1"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}
	access := accessInfo{IsAdmin: true, FilterID: "owner"}
	rows := []usageWindowRow{{ResourceFilterType: "account", ResourceFilterID: "acc_x"}, {ResourceFilterType: "group", ResourceFilterID: "grp_x"}}

	// loadResourceInfoMap 的 account/group 查询失败（131-146/152-167）。
	fp.arm("SELECT id, name, system_account_id FROM")
	if _, err := s.loadResourceInfoMap(ctx, rows); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// teamUsageRows 中 loadTeamNameMap/loadPrincipalMap 失败（经预聚合行）。
	if _, err := db.Exec(`INSERT INTO authorization_team_usage_range_windows
		(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
		 request_count, input_tokens, output_tokens, total_cost_usd, updated_at)
		VALUES ('owner', '2026-08-08', '2026-09-06', 'team_x', 'group', 'grp_x', 1, 2, 3, 0.5, '2026-09-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO authorization_user_usage_range_windows
		(system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
		 request_count, input_tokens, output_tokens, total_cost_usd, updated_at)
		VALUES ('owner', '2026-08-08', '2026-09-06', 'team_x', 'ua1', 'group', 'grp_x', 1, 2, 3, 0.5, '2026-09-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	fp.arm("SELECT id, name FROM system_teams")
	if _, err := s.teamUsageRows(ctx, UsageFilters{}, access, rng, 1, 20); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()
	fp.arm("SELECT id, username, display_name FROM system_accounts")
	defer fp.disarm()
	if _, err := s.loadPrincipalMap(ctx, []string{"ua1"}); err == nil {
		t.Fatalf("应失败")
	}
	fp.disarm()
	if _, err := s.userUsageRows(ctx, UsageFilters{}, access, rng, 1, 20); err != nil {
		t.Fatalf("无 failpoint 时应成功: %v", err)
	}

	// usageScopeRangeSummaries 的 scan 前 failpoint（查询失败 → 205-208）。
	fp.arm("FROM usage_scope_range_windows")
	defer fp.disarm()
	if _, err := s.usageScopeRangeSummaries(ctx, []usageScopeRequest{{rowKey: "k", systemAccountID: "owner", scopeID: "s"}}, "account_authorization", rng); err == nil {
		t.Fatalf("应失败")
	}
}

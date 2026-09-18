// w13g2 failpoint 第五批：badscan 注入（单列假行触发 rows.Scan 列数不匹配
// 错误）批量覆盖各读函数的 Scan/迭代错误臂，及剩余可选数据分支。
package authz

import (
	"context"
	"database/sql"
	"testing"
)

func TestW13g2BadScanArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	if _, err := db.Exec(usageFixtureDDL); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "bs1", "bs2"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_bs13', 'B', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_bs13', 'T', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES ('teammem_bs13', 'team_bs13', 'bs1', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_bs13', 'owner', 'B源', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_bs13",
		GranteeType: "system_account", GranteeID: "bs2",
	}, "owner"); err != nil {
		t.Fatal(err)
	}

	expectErr := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s 应失败", name)
		}
	}

	// sync.go：team 行循环 scan、revokeTeamGrantSources scan。
	t.Run("syncTeamRuntimeRowsScan", func(t *testing.T) {
		fp.armScan("ORDER BY ra.id ASC")
		defer fp.disarm()
		tx := mustTx(t, db)
		err := s.syncTeamGrantRuntime(ctx, tx, &grantRow{
			GranteeType: "team", Status: StatusPaused,
			GranteeTeamID: sqlNullString("team_bs13"),
			ResourceType:  "group", ResourceID: "grp_bs13",
		}, "owner", "2026-01-01T00:00:00Z")
		tx.Rollback()
		expectErr("syncTeam", err)
	})
	t.Run("revokeTeamGrantSourcesScan", func(t *testing.T) {
		fp.armScan("AND ras.status = 'active'")
		defer fp.disarm()
		tx := mustTx(t, db)
		err := s.revokeTeamGrantSources(ctx, tx, "group", "grp_bs13", "team_bs13", "owner", "2026-01-01T00:00:00Z")
		tx.Rollback()
		expectErr("revokeTeam", err)
	})

	// downstream.go：bindings scan、实例 scan。
	t.Run("loadBindingsScan", func(t *testing.T) {
		fp.armScan("SELECT ra.id, ra.resource_type")
		defer fp.disarm()
		tx := mustTx(t, db)
		err := s.syncGrantQuotaScopeBindings(ctx, tx, &grantRow{
			ID: "x", ResourceType: "group", ResourceID: "grp_bs13", GranteeType: "system_account",
		}, "2026-01-01T00:00:00Z")
		tx.Rollback()
		expectErr("bindings", err)
	})
	t.Run("enqueueInstancesScan", func(t *testing.T) {
		fp.armScan("WHERE authorization_instance_source_account_id = ?")
		defer fp.disarm()
		tx := mustTx(t, db)
		_, enqueueErr := s.enqueueGrantAccountHealthInputs(ctx, tx, &grantRow{
			ResourceType: "account", ResourceID: "acc_bs13",
		}, "2026-01-01T00:00:00Z")
		tx.Rollback()
		expectErr("enqueue", enqueueErr)
	})

	// usage_reads.go：team/user 窗口 scan、summary scan。
	t.Run("teamWindowScan", func(t *testing.T) {
		fp.armScan("FROM authorization_team_usage_range_windows")
		defer fp.disarm()
		_, err := s.teamUsageRows(ctx, UsageFilters{ResourceType: "account"}, accessInfo{IsAdmin: true, FilterID: "owner"},
			UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06"}, 1, 20)
		expectErr("teamRows", err)
	})
	t.Run("userWindowScan", func(t *testing.T) {
		fp.armScan("FROM authorization_user_usage_range_windows")
		defer fp.disarm()
		_, err := s.userUsageRows(ctx, UsageFilters{ResourceType: "account"}, accessInfo{IsAdmin: true, FilterID: "owner"},
			UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06"}, 1, 20)
		expectErr("userRows", err)
	})
	t.Run("usageSummaryScan", func(t *testing.T) {
		fp.armScan("FROM authorization_team_usage_range_windows")
		defer fp.disarm()
		_, err := s.teamUsageSummary(ctx, UsageFilters{TeamID: "team_bs13"}, accessInfo{IsAdmin: true, FilterID: "owner"},
			UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06"})
		expectErr("summary", err)
	})

	// usage_detail.go：scope rows、team runtime rows、instance ids。
	t.Run("scopeSummariesScan", func(t *testing.T) {
		fp.armScan("FROM usage_scope_range_windows")
		defer fp.disarm()
		_, err := s.usageScopeRangeSummaries(ctx, []usageScopeRequest{{rowKey: "k", systemAccountID: "owner", scopeID: "s"}},
			"account_authorization", UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06"})
		expectErr("scopeSummaries", err)
	})
	t.Run("teamRuntimeUsageScan", func(t *testing.T) {
		fp.armScan("SELECT DISTINCT")
		defer fp.disarm()
		_, err := s.loadTeamRuntimeUsageRows(ctx, "group", "grp_bs13", "owner", "team_bs13", 20, 0)
		expectErr("teamRuntimeRows", err)
	})
	t.Run("instanceAccountIDsScan", func(t *testing.T) {
		fp.armScan("authorization_instance_authorization_id IN")
		defer fp.disarm()
		_, err := s.loadAccountInstanceAccountIDs(ctx, []string{"x"})
		expectErr("instanceIDs", err)
	})

	// stats_loader.go：聚合 scan。
	t.Run("statsAggScan", func(t *testing.T) {
		fp.armScan("COUNT(DISTINCT ra.id) AS authorization_count")
		defer fp.disarm()
		_, err := s.loadResourceAuthorizationStatsFromDatabase(ctx, "group", []string{"grp_bs13"})
		expectErr("stats", err)
	})

	// usage_lookups.go：principal scan、resource info scan。
	t.Run("principalScan", func(t *testing.T) {
		fp.armScan("SELECT id, username, display_name FROM system_accounts")
		defer fp.disarm()
		_, err := s.loadPrincipalMap(ctx, []string{"bs1"})
		expectErr("principal", err)
	})
	t.Run("resourceInfoScan", func(t *testing.T) {
		fp.armScan("SELECT id, name, system_account_id FROM")
		defer fp.disarm()
		_, err := s.loadResourceInfoMap(ctx, []usageWindowRow{{ResourceFilterType: "account", ResourceFilterID: "acc_bs13"}})
		expectErr("resourceInfo", err)
	})

	// team_cascade.go：member/grant 行 scan。
	t.Run("activeTeamMemberRowsScan", func(t *testing.T) {
		fp.armScan("ORDER BY m.joined_at ASC")
		defer fp.disarm()
		tx := mustTx(t, db)
		_, err := s.activeTeamMemberRowsTx(ctx, tx, "team_bs13")
		tx.Rollback()
		expectErr("memberRows", err)
	})
	t.Run("activeTeamGrantRowsScan", func(t *testing.T) {
		fp.armScan("WHERE grantee_type = 'team' AND grantee_team_id = ?")
		defer fp.disarm()
		tx := mustTx(t, db)
		_, err := s.activeTeamGrantRowsTx(ctx, tx, "team_bs13")
		tx.Rollback()
		expectErr("grantRows", err)
	})

	// instance_provision.go：syncTx 行 scan。
	t.Run("syncNamesScan", func(t *testing.T) {
		fp.armScan("SELECT id, system_account_id, authorization_instance_authorization_id, name")
		defer fp.disarm()
		tx := mustTx(t, db)
		_, err := s.syncAccountAuthorizationInstanceNamesForSourceAccountTx(ctx, tx, "acc_bs13", "x", "now")
		tx.Rollback()
		expectErr("syncNames", err)
	})

	// query 族：授权行 scan（queryGrantRowsPage 的 scanErr）。
	t.Run("listPageScan", func(t *testing.T) {
		fp.armScan("SELECT g.id, g.resource_type")
		defer fp.disarm()
		_, _, _, err := s.ListPage(ctx, Filters{}, 1, 20)
		expectErr("listPage", err)
	})

	// mutations.go：revive 分支 remark 覆盖（192-196）。
	t.Run("reviveWithRemark", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_bs13b', 'B2', 'owner', 'active')`); err != nil {
			t.Fatal(err)
		}
		first, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_bs13b",
			GranteeType: "system_account", GranteeID: "bs2",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Revoke(ctx, first.Item.ID, first.Item.UpdatedAt, "owner"); err != nil {
			t.Fatal(err)
		}
		remark := "恢复备注"
		revived, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_bs13b",
			GranteeType: "system_account", GranteeID: "bs2",
			Remark: &remark,
		}, "owner")
		if err != nil || revived.Created || revived.PreviousStatus == nil {
			t.Fatalf("revive 应成功: %+v %v", revived, err)
		}
	})
}

func sqlNullString(v string) sql.NullString {
	return sql.NullString{String: v, Valid: true}
}

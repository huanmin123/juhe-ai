// w13g2 failpoint 第二批：instance_provision / mutations / sync 的深层错误
// 臂与状态分支。每个子测试自建数据、arm 一个 SQL 片段、断言失败后 disarm。
package authz

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// w13g2SeedProvisionFail 装配账户装载场景：源账户 + grantee 默认分组。
func w13g2SeedProvisionFail(t *testing.T, s *Store) {
	t.Helper()
	db := s.db
	for _, id := range []string{"owner", "grantee", "gn1", "gn2"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_f13', 'owner', '源F', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status, provider_code, enabled, is_default, updated_at)
		VALUES ('tg_f13', '默认F', 'grantee', 'active', 'gpt', 1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

// w13g2ResetProvision 清理装载产物，保证每个子测试从零开始。
func w13g2ResetProvision(t *testing.T, s *Store) {
	t.Helper()
	for _, stmt := range []string{
		`DELETE FROM group_accounts`,
		`DELETE FROM accounts WHERE authorization_instance_source_account_id = 'acc_f13'`,
		`DELETE FROM resource_authorization_sources`,
		`DELETE FROM resource_authorizations`,
		`DELETE FROM resource_authorization_grants WHERE resource_type = 'account'`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestW13g2FailpointProvisionArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	w13g2SeedProvisionFail(t, s)
	ctx := context.Background()

	w13g2CreateAccountGrant := func(t *testing.T, resourceID, granteeID string) {
		t.Helper()
		w13g2ResetProvision(t, s)
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: resourceID,
			GranteeType: "system_account", GranteeID: granteeID,
			TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	}

	t.Run("ensureLiveQuery", func(t *testing.T) {
		fp.arm("SELECT id, provider_code FROM")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "grantee")
	})

	t.Run("ensureSourceQuery", func(t *testing.T) {
		fp.arm("SELECT provider_code, provider_protocol_profile_id")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "gn1")
	})

	t.Run("deletedInstanceQuery", func(t *testing.T) {
		fp.arm("deleted_at IS NOT NULL")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "gn2")
	})

	t.Run("uniqueNameAvailable", func(t *testing.T) {
		fp.arm("WHERE system_account_id = ? AND name = ? AND deleted_at IS NULL")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "grantee")
	})

	t.Run("instanceInsert", func(t *testing.T) {
		fp.arm("(id, system_account_id, provider_code, provider_protocol_profile_id")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "grantee")
	})

	t.Run("searchTermsDelete", func(t *testing.T) {
		fp.arm("account_name_search_terms WHERE account_id = ?")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "grantee")
	})

	t.Run("searchTermsInsert", func(t *testing.T) {
		fp.arm("INSERT OR IGNORE INTO")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "grantee")
	})

	t.Run("searchDocumentsInsert", func(t *testing.T) {
		fp.arm("INSERT INTO account_name_search_documents\n")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "grantee")
	})

	t.Run("bindExistingQuery", func(t *testing.T) {
		fp.arm("SELECT group_id FROM")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "grantee")
	})

	t.Run("bindInsert", func(t *testing.T) {
		fp.arm("(system_account_id, group_id, account_id, account_authorization_id")
		defer fp.disarm()
		w13g2CreateAccountGrant(t, "acc_f13", "grantee")
	})

	t.Run("defaultGroupQuery", func(t *testing.T) {
		w13g2ResetProvision(t, s)
		fp.arm("is_default = 1")
		defer fp.disarm()
		// 无 targetGroupId → 默认分组查询命中 failpoint。
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_f13",
			GranteeType: "system_account", GranteeID: "grantee",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("advanceSelectAndRestore", func(t *testing.T) {
		// 先正常建实例，软删后走 restore arm。
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_f13",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		var instanceID string
		if err := s.db.QueryRow(`SELECT id FROM accounts WHERE authorization_instance_source_account_id = 'acc_f13' AND deleted_at IS NULL`).Scan(&instanceID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE accounts SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = ?`, instanceID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Revoke(ctx, created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
			t.Fatal(err)
		}
		revivedVersion := ""
		grant, err := s.GetGrantForMutation(ctx, nil, created.Item.ID)
		if err != nil || grant == nil {
			t.Fatal(err)
		}
		revivedVersion = grant.UpdatedAt
		fp.arm("SELECT dispatch_revision FROM accounts")
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_f13",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
		}, "owner"); err == nil {
			fp.disarm()
			t.Fatalf("应失败")
		}
		fp.disarm()
		fp.arm("SET dispatch_revision = dispatch_revision + 1")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_f13",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
		_ = revivedVersion
	})

	t.Run("restoreUpdateArm", func(t *testing.T) {
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_f13",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		var instanceID string
		if err := s.db.QueryRow(`SELECT id FROM accounts WHERE authorization_instance_source_account_id = 'acc_f13' AND deleted_at IS NULL`).Scan(&instanceID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE accounts SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = ?`, instanceID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Revoke(ctx, created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
			t.Fatal(err)
		}
		fp.arm("SET provider_code = ?")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_f13",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("renameCascade", func(t *testing.T) {
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_f13",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		_ = created
		fp.arm("SET name = ?, updated_at = ? WHERE id = ?")
		defer fp.disarm()
		if _, err := s.SyncAccountAuthorizationInstanceNamesForSourceAccount(ctx, "acc_f13", "新名字"); err == nil {
			t.Fatalf("应失败")
		}
	})
}

func TestW13g2FailpointMutationArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	w13g2SeedProvisionFail(t, s)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_m13', 'M', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}

	t.Run("teamNonOwnerCount", func(t *testing.T) {
		if _, err := s.db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
			VALUES ('team_m13', 'T', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
			VALUES ('teammem_m13', 'team_m13', 'gn1', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("AND m.system_account_id <> ?")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_m13",
			GranteeType: "team", GranteeID: "team_m13",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("checkGranteeAccountQuery", func(t *testing.T) {
		fp.arm("SELECT status FROM system_accounts WHERE id = ?")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_m13",
			GranteeType: "system_account", GranteeID: "grantee",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("checkGranteeTeamMemberCount", func(t *testing.T) {
		fp.arm("WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active'")
		defer fp.disarm()
		if _, err := s.db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
			VALUES ('team_m13b', 'T2', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
			VALUES ('teammem_m13b', 'team_m13b', 'gn2', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_m13",
			GranteeType: "team", GranteeID: "team_m13b",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("idempotentFindSummary", func(t *testing.T) {
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_m13",
			GranteeType: "system_account", GranteeID: "grantee",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		// 幂等重复 → post-commit findSummaryWithLimits 命中 failpoint。
		fp.arm("FROM resource_authorization_grants g WHERE g.id = ?")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_m13",
			GranteeType: "system_account", GranteeID: "grantee",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
		_ = created
	})

	t.Run("createUpsertSource", func(t *testing.T) {
		fp.arm("SELECT id FROM resource_authorization_sources")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_m13",
			GranteeType: "system_account", GranteeID: "gn1",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("createPostCommitFindSummary", func(t *testing.T) {
		fp.arm("FROM resource_authorization_grants g WHERE g.id = ?")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_m13",
			GranteeType: "system_account", GranteeID: "gn2",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("patchSyncErr", func(t *testing.T) {
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_m13",
			GranteeType: "system_account", GranteeID: "grantee",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("trg.expires_at > ?")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, created.Item.ID, PatchInput{
			LimitsSet: true, LimitsJSON: func() *string { v := `{"daily":{"enabled":true,"limit":9}}`; return &v }(),
		}, created.Item.UpdatedAt, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})
}

func TestW13g2FailpointSyncArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	w13g2SeedProvisionFail(t, s)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_s13f', 'S', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gs1", "gs2", "gs3"} {
		if _, err := s.db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("quotaBindingsPreviousQuery", func(t *testing.T) {
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_s13f",
			GranteeType: "system_account", GranteeID: "gs1",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM request_quota_hourly_window_scope_bindings")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, created.Item.ID, PatchInput{
			Status: func() *string { v := StatusPaused; return &v }(),
		}, created.Item.UpdatedAt, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("enqueueHealthInputsQuery", func(t *testing.T) {
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_s13f",
			GranteeType: "system_account", GranteeID: "gs2",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		// account 资源的 grant 才会扇出健康输入；换 account 资源。
		if _, err := s.db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_s13f', 'owner', 'S源', 'gpt', 'oauth')`); err != nil {
			t.Fatal(err)
		}
		accCreated, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_s13f",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("WHERE authorization_instance_source_account_id = ?")
		defer fp.disarm()
		if _, err := s.Revoke(ctx, accCreated.Item.ID, accCreated.Item.UpdatedAt, "owner"); err == nil {
			t.Fatalf("应失败")
		}
		_ = created
	})

	t.Run("pausedRefreshErr", func(t *testing.T) {
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_s13f",
			GranteeType: "system_account", GranteeID: "gs1",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("trg.status = 'paused'")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, created.Item.ID, PatchInput{
			Status: func() *string { v := StatusPaused; return &v }(),
		}, created.Item.UpdatedAt, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("manualSourceRefreshErr", func(t *testing.T) {
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_s13f",
			GranteeType: "system_account", GranteeID: "gs2",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("AND status = 'active'\n\t\tORDER BY activated_at ASC")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, created.Item.ID, PatchInput{
			Status: func() *string { v := StatusPaused; return &v }(),
		}, created.Item.UpdatedAt, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})
}

// TestW13g2FailpointReturnData 用数据构造 Return 的守卫分支（无 failpoint）。
func TestW13g2FailpointReturnData(t *testing.T) {
	s, _ := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_rt13', 'R', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")

	// owner==grantee 的授权 → Return not_found（698-700）。
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, created_by, created_at, updated_at)
		VALUES ('grant_rt13_self', 'group', 'grp_rt13', 'grantee', 'system_account', 'grantee', 'use', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	mutation, err := s.Return(ctx, "grant_rt13_self", "2026-01-01T00:00:00Z", "grantee")
	if err != nil || mutation.Status != "not_found" {
		t.Fatalf("owner 自归还应 not_found: %+v %v", mutation, err)
	}

	// 无 runtime 行 → not_found（718-720）。
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, created_by, created_at, updated_at)
		VALUES ('grant_rt13_nort', 'group', 'grp_rt13', 'owner', 'system_account', 'gn1', 'use', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	mutation, err = s.Return(ctx, "grant_rt13_nort", "2026-01-01T00:00:00Z", "gn1")
	if err != nil || mutation.Status != "not_found" {
		t.Fatalf("无 runtime 应 not_found: %+v %v", mutation, err)
	}

	// ExpireSweep：revoked_by 缺失 → sync actor 取 created_by（855-860）。
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, expires_at, created_by, created_at, updated_at)
		VALUES ('grant_rt13_x', 'group', 'grp_rt13', 'owner', 'system_account', 'gn2', 'use', 'active', ?, 'creator_rt13', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, past); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, activated_at, created_by, created_at, updated_at)
		VALUES ('rt_rt13_x', 'group', 'grp_rt13', 'owner', 'gn2', 'use', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_sources
		(id, authorization_id, source_type, status, activated_at, created_by, created_at, updated_at)
		VALUES ('src_rt13_x', 'rt_rt13_x', 'manual', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExpireSweep(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := db.QueryRow(`SELECT COALESCE(revoked_by,'') FROM resource_authorizations WHERE id = 'rt_rt13_x'`).Scan(&actor); err != nil || actor != "creator_rt13" {
		t.Fatalf("sync actor 应取 created_by: %q %v", actor, err)
	}
	_ = strings.Contains
	_ = sql.ErrNoRows
}

package cleanuprepo

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// w13e_deleteaccount_arms_test.go 覆盖 deleteaccount.go SQLite 半区剩余臂：
// 候选列表 / buildTarget / 授权装载器的 failOn 注入，以及装载器与删除链的
// 数据驱动分支。

func w13eDeletedFixture(t *testing.T) (*DeletedAccountStore, *kitRecordFixture, *DB) {
	t.Helper()
	f := newKitRecordFixture(t)
	store, business := newKitDeletedAccountStore(t, f.store)
	return store, f, business
}

// TestW13eDeletedAccountSweepArms：CleanupExpired 主链逐语句失败臂。候选级
// 失败会被吞进 summary.Failed，断言以「外传错误或 Failed=1」为准。
func TestW13eDeletedAccountSweepArms(t *testing.T) {
	stages := []pgStage{
		{"root candidates", "AND deleted_at <= ?"},
		{"instance candidates", "authorization_instance_source_account_id = ?"},
		{"authorization rows", "resource_type = 'account'"},
		{"active instances", "authorization_instance_authorization_id IN"},
		{"team scopes", "AND source_team_id IS NOT NULL"},
		{"source grants", "FROM resource_authorization_grants"},
		{"targets self check", "FROM account_record_cleanup_targets WHERE account_id = ?"},
		{"physically delete models", "DELETE FROM account_supported_models"},
		{"physically delete search terms", "DELETE FROM account_name_search_terms"},
		{"physically delete runtime states", "DELETE FROM account_api_key_runtime_states"},
		{"physically delete grants", "DELETE FROM resource_authorization_grants WHERE id IN"},
		{"physically delete authorizations", "DELETE FROM resource_authorizations WHERE id IN"},
		{"physically delete root", "DELETE FROM accounts WHERE id = ?"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		f := w13eFixture(t, "")
		path := filepath.Join(t.TempDir(), "w13e-business.sqlite3")
		seed, err := sql.Open("sqlite", path)
		if err != nil {
			return err
		}
		t.Cleanup(func() { _ = seed.Close() })
		seed.SetMaxOpenConns(1)
		createKitBusinessSchema(t, seed)
		store := &DeletedAccountStore{Business: &DB{DB: seed}, Records: w13eKitPlainStore(t, f), Now: kitNow}
		needsInstance := stage.name == "active instances"
		needsTeamSource := stage.name == "team scopes"
		if needsInstance {
			seedKitDeletedAccount(t, &DB{DB: seed}, "acc-1", "2026-06-01T00:00:00.000Z", "iauth-1", "")
			// 让实例授权进入装载结果，loadActiveAuthorizationInstanceIDs 才有输入。
			mustExecKit(t, &DB{DB: seed}, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id)
        VALUES ('iauth-1', 'authorization_instance', 'acc-1', 'sys-1')`)
		} else {
			seedKitDeletedAccount(t, &DB{DB: seed}, "acc-1", "2026-06-01T00:00:00.000Z", "", "")
		}
		if needsTeamSource {
			// 普通候选 + 授权来源 team：让 loadTeamScopeIDs 的查询被执行。
			mustExecKit(t, &DB{DB: seed}, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id)
        VALUES ('auth-1', 'account', 'acc-1', 'sys-1')`)
			mustExecKit(t, &DB{DB: seed}, `INSERT INTO resource_authorization_sources (id, authorization_id, source_team_id)
        VALUES ('src-1', 'auth-1', 'team-9')`)
		}
		if stage.name == "physically delete grants" || stage.name == "physically delete authorizations" {
			// 让目标携带授权与 grant，物理删除的授权/grant 分支才会执行。
			mustExecKit(t, &DB{DB: seed}, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id)
        VALUES ('auth-1', 'account', 'acc-1', 'sys-1')`)
			mustExecKit(t, &DB{DB: seed}, `INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id)
        VALUES ('g-1', 'account', 'acc-1', 'sys-1', 'sys-2')`)
		}
		if stage.name == "targets self check" {
			// 目标表自检走 Records.Dataset：needle 注册到 Dataset 失败句柄。
			// Business 不携带本阶段 needle——其语句不含该文本，注册必然
			// 不命中（w13e fired 断言会判为伪覆盖）。
			store.Business = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{})
			store.Records.Dataset = w13eOpenDecoratedSQLite(t, filepath.Join(f.dir, "dataset.sqlite3"),
				w13eSQLiteOptions{failOn: stage.failOn})
		} else {
			store.Business = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{failOn: stage.failOn})
		}
		summary, err := store.CleanupExpired(context.Background())
		if err != nil {
			return err
		}
		if summary.Failed == 1 {
			// 候选级失败被吞进 summary.Failed；LastTargetError 保留注入器
			// 原始文本（前缀 + oneLineSQL 语句），交回 runner 做前缀 + needle
			// 双重断言，代替旧的哨兵文本。
			return errors.New(store.LastTargetError)
		}
		return nil
	})
}

// TestW13eDeletedAccountDirectArms：直调装载器与数据驱动分支。
func TestW13eDeletedAccountDirectArms(t *testing.T) {
	ctx := context.Background()
	// listCandidates limit 饱和。
	{
		store, _, business := w13eDeletedFixture(t)
		seedKitDeletedAccount(t, business, "acc-1", "2026-06-01T00:00:00.000Z", "", "")
		seedKitDeletedAccount(t, business, "acc-2", "2026-06-01T00:00:00.000Z", "", "")
		candidates, err := store.listCandidates(ctx, "2026-09-10T00:00:00.000Z", 1)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("limit=1 应饱和: %d %v", len(candidates), err)
		}
	}
	// loadActiveAuthorizationInstanceIDs：实例行收集与空输入。
	{
		store, _, business := w13eDeletedFixture(t)
		// 持有实例授权的账户必须是活跃账户（deleted_at IS NULL）。
		seedKitDeletedAccount(t, business, "acc-live", "", "iauth-1", "")
		ids, err := store.loadActiveAuthorizationInstanceIDs(ctx, []string{"iauth-1", " "})
		if err != nil || !ids["iauth-1"] {
			t.Fatalf("活跃实例应被收集: %v %v", ids, err)
		}
	}
	// loadTeamScopeIDs：空 team 跳过、未知授权回退 fallback。
	{
		store, _, business := w13eDeletedFixture(t)
		mustExecKit(t, business, `INSERT INTO resource_authorization_sources (id, authorization_id, source_team_id)
      VALUES ('src-empty', 'auth-empty', ''), ('src-team', 'auth-team', 'team-9'), ('src-orphan', 'auth-orphan', 'team-7')`)
		teamScopes, err := store.loadTeamScopeIDs(ctx, []string{"auth-empty", "auth-team", "auth-orphan"},
			map[string]string{}, map[string]string{}, "acc-fallback")
		if err != nil {
			t.Fatalf("loadTeamScopeIDs: %v", err)
		}
		// 空 team 跳过；其余走实例映射缺失 → 资源映射缺失 → fallback。
		if len(teamScopes) != 2 || teamScopes[0] != "acc-fallback:team-9" || teamScopes[1] != "acc-fallback:team-7" {
			t.Fatalf("team scope 收集错误: %v", teamScopes)
		}
	}
	// loadAuthorizationInstanceGrantIDs：实例授权 grant 查询。
	{
		store, _, business := w13eDeletedFixture(t)
		// 授权/grant/sources 需按 JOIN 条件严格对齐（type+resource+owner+grantee+manual 来源）。
		mustExecKit(t, business, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id)
      VALUES ('iauth-1', 'authorization_instance', 'acc-1', 'sys-1', 'sys-1')`)
		mustExecKit(t, business, `INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id)
      VALUES ('g-1', 'authorization_instance', 'acc-1', 'sys-1', 'system_account', 'sys-1')`)
		mustExecKit(t, business, `INSERT INTO resource_authorization_sources (id, authorization_id, source_type)
      VALUES ('src-1', 'iauth-1', 'manual')`)
		grantIDs, err := store.loadAuthorizationInstanceGrantIDs(ctx, []string{"iauth-1"})
		if err != nil || len(grantIDs) != 1 {
			t.Fatalf("实例 grant 应被收集: %v %v", grantIDs, err)
		}
	}
	// logicallyDeleteAccountsTx：空 ids 直通。
	{
		store, _, _ := w13eDeletedFixture(t)
		tx, err := store.Business.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		deleted, err := store.logicallyDeleteAccountsTx(ctx, tx, nil, "actor", kitUpdatedAt)
		if err != nil {
			t.Fatalf("空 ids 不应报错: %v", err)
		}
		_ = deleted
		_ = tx.Rollback()
	}
	// buildTarget：普通账户（非实例）目标构建。
	{
		f := newKitRecordFixture(t)
		store, business := newKitDeletedAccountStore(t, f.store)
		seedKitDeletedAccount(t, business, "acc-1", "2026-06-01T00:00:00.000Z", "", "")
		mustExecKit(t, business, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id)
      VALUES ('auth-1', 'account', 'acc-1', 'sys-1')`)
		mustExecKit(t, business, `INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id)
      VALUES ('g-1', 'account', 'acc-1', 'sys-1', 'sys-2')`)
		candidates, err := store.listCandidates(ctx, "2026-09-10T00:00:00.000Z", 10)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("候选应存在: %d %v", len(candidates), err)
		}
		target, err := store.buildTarget(ctx, candidates[0])
		if err != nil || target == nil {
			t.Fatalf("buildTarget: %v", err)
		}
		if len(target.AuthorizationIDs) == 0 || len(target.GrantIDs) == 0 {
			t.Fatalf("授权/grant 应被装载: %+v", target)
		}
		_ = retention.ExpiredDeletedAccountTarget{}
	}
	// 孤儿扫尾（SQLite）真实执行：孤儿实例账户被逻辑删除并计数（原「显式
	// 跳过」臂已随扫尾双方言化反转为真实扫尾断言）。
	{
		store, _, business := w13eDeletedFixture(t)
		// 孤儿实例：活跃账户持有的实例授权指向缺失授权行（ra.id IS NULL
		// 分支），source 账户行同样缺失。
		seedKitOrphanInstance(t, business, "acc-orphan", "iauth-missing", "acc-gone")
		summary, err := store.CleanupExpired(ctx)
		if err != nil {
			t.Fatalf("CleanupExpired: %v", err)
		}
		if summary.OrphanedAuthorizationInstances != 1 {
			t.Fatalf("SQLite 孤儿扫尾应命中 1: %+v", summary)
		}
		if got := mustQueryCountKit(t, business, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-orphan' AND deleted_at IS NOT NULL`); got != 1 {
			t.Fatalf("孤儿实例应被逻辑删除")
		}
	}
	// 目标移交 record cleanup 时 buildTarget 的 related ids 汇集。
	{
		store, f, business := w13eDeletedFixture(t)
		seedKitDeletedAccount(t, business, "acc-1", "2026-06-01T00:00:00.000Z", "", "")
		mustExecKit(t, business, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id)
      VALUES ('auth-1', 'account', 'acc-1', 'sys-1')`)
		mustExecKit(t, f.stats, `INSERT INTO usage_stats_totals
      (system_account_id, scope_type, scope_id, request_count) VALUES ('sys-1','account','acc-1',1)`)
		summary, err := store.CleanupExpired(ctx)
		if err != nil || summary.Deferred != 1 {
			t.Fatalf("应移交 record cleanup: %+v %v", summary, err)
		}
	}
}

// TestOrphanSweepSQLiteErrorStages：SQLite 孤儿扫尾链逐语句失败臂（照
// sqlite_error_stages 模式：真实孤儿数据 + failOn 装饰句柄，断言错误来自
// 注入器命中的语句，杜绝伪覆盖）。种子走普通句柄，被测 store 用注入句柄。
func TestOrphanSweepSQLiteErrorStages(t *testing.T) {
	stages := []pgStage{
		{"orphan select", "LEFT JOIN"},
		{"revoke lookup", "SELECT resource_type, resource_id"},
		{"quota bindings", "DELETE FROM request_quota_hourly_window_scope_bindings"},
		{"grants revoke", "UPDATE resource_authorization_grants"},
		{"sources revoke", "UPDATE resource_authorization_sources"},
		{"auth revoke", "UPDATE resource_authorizations"},
		{"account update", "SET status = 'disabled'"},
		{"deleted ids", "deleted_at = ? AND id IN"},
		{"tombstone select", "provider_code IN"},
		{"version select", "SELECT current_version"},
		{"version upsert", "INSERT INTO account_health_jobs_input_versions"},
		{"outbox insert", "INSERT INTO account_health_jobs_input_outbox"},
		{"tags delete", "DELETE FROM account_tag_bindings"},
		{"search terms", "DELETE FROM account_name_search_terms"},
		{"search documents", "DELETE FROM account_name_search_documents"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		path := filepath.Join(t.TempDir(), "w13e-orphan-business.sqlite3")
		seed, err := sql.Open("sqlite", path)
		if err != nil {
			return err
		}
		t.Cleanup(func() { _ = seed.Close() })
		seed.SetMaxOpenConns(1)
		createKitBusinessSchema(t, seed)
		seedKitOrphanFullChain(t, &DB{DB: seed})
		store := &DeletedAccountStore{Business: w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{failOn: stage.failOn}), Now: kitNow}
		_, err = store.orphanSweepSQLite(context.Background(), 10)
		return err
	})
}

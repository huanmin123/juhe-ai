package groupspolicymigration

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// fixtureDDL 在隔离测试库上建迁移面三表（列形状与生产业务库同构的测试子集，
// 参照 mergesupermigration fixture）。迁移是纯数据步骤：列形态不变。
const fixtureDDL = `
CREATE SCHEMA juhe_business;
SET search_path = juhe_business;
CREATE TABLE groups (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
  enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0,
  group_type TEXT NOT NULL DEFAULT 'personal', group_kind TEXT NOT NULL DEFAULT 'standard',
  scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE group_authorization_settings (
  authorization_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1, group_type TEXT NOT NULL DEFAULT 'personal',
  scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE super_group_members (
  id TEXT PRIMARY KEY, super_group_id TEXT NOT NULL, member_group_id TEXT NOT NULL,
  priority INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (super_group_id, member_group_id), UNIQUE (super_group_id, priority));
`

const fixtureNow = "2026-10-01T00:00:00Z"

func openFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(fixtureDDL)
		return err
	})
	return db
}

func execFixture(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture 写入失败 %s: %v", query, err)
	}
}

func insertGroup(t *testing.T, db *sql.DB, id, owner, name, kind, groupType string, policy any) {
	t.Helper()
	execFixture(t, db, `INSERT INTO juhe_business.groups
		(id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, 1, 0, $4, $5, $6, $7, $7)`,
		id, owner, name, groupType, kind, policy, fixtureNow)
}

func insertAuthSetting(t *testing.T, db *sql.DB, authorizationID, owner, groupID, groupType string, policy any) {
	t.Helper()
	execFixture(t, db, `INSERT INTO juhe_business.group_authorization_settings
		(authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ($1, $2, $3, 1, $4, $5, $6, $6)`,
		authorizationID, owner, groupID, groupType, policy, fixtureNow)
}

func insertSuperMember(t *testing.T, db *sql.DB, superGroupID, memberID string, priority int) {
	t.Helper()
	execFixture(t, db, `INSERT INTO juhe_business.super_group_members
		(id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, $5, $5)`,
		"sgm_"+superGroupID+"_"+memberID, superGroupID, memberID, priority, fixtureNow)
}

// groupFact 读回一行的 group_type / 策略列 / updated_at。
func groupFact(t *testing.T, db *sql.DB, id string) (string, sql.NullString, string) {
	t.Helper()
	var groupType string
	var updated string
	var policy sql.NullString
	if err := db.QueryRow(`SELECT group_type, scheduling_policy_json, updated_at FROM juhe_business.groups WHERE id = $1`, id).
		Scan(&groupType, &policy, &updated); err != nil {
		t.Fatalf("读回分组 %s: %v", id, err)
	}
	return groupType, policy, updated
}

// seedLegacyDataset 铺一套退役前形态的数据：3 个 high_concurrency 分组（旧默认
// 5000、显式阈值 12+queue+image lane、无 IP 限制）、2 个 personal 行（NULL 与
// 合规两键）、1 个超级分组（首成员带显式阈值）、2 行授权设置（high 覆盖与
// personal）。
func seedLegacyDataset(t *testing.T, db *sql.DB) {
	t.Helper()
	const legacyDefault = `{"mode":"balanced_fast","defaultSoftConcurrency":5000,"fastFirstEnabled":true,
		"fallbackOnQueueEnabled":true,"breakAffinityOnSoftLimit":true,"breakAffinityOnQueueWaitMs":0,
		"slowRequestThresholdMs":30000,"firstOutputSlowThresholdMs":15000,"recentTimeoutWindowSeconds":120,
		"recentTimeoutPenaltyThreshold":2,"maxQueueWaitMs":60000,"maxQueueSize":5000,"perApiKeyQueueLimit":5000,
		"clientIpConcurrencyLimit":0,"clientIpConcurrencyOverflowMode":"reject","imageLaneMaxConcurrency":0}`
	const legacyExplicit = `{"mode":"balanced_fast","defaultSoftConcurrency":12,"fastFirstEnabled":true,
		"fallbackOnQueueEnabled":true,"breakAffinityOnSoftLimit":true,"breakAffinityOnQueueWaitMs":0,
		"slowRequestThresholdMs":30000,"firstOutputSlowThresholdMs":15000,"recentTimeoutWindowSeconds":120,
		"recentTimeoutPenaltyThreshold":2,"maxQueueWaitMs":45000,"maxQueueSize":5000,"perApiKeyQueueLimit":5000,
		"clientIpConcurrencyLimit":8,"clientIpConcurrencyOverflowMode":"queue","imageLaneMaxConcurrency":6}`
	const legacyNoIP = `{"mode":"balanced_fast","defaultSoftConcurrency":6,"fastFirstEnabled":false,
		"fallbackOnQueueEnabled":false,"breakAffinityOnSoftLimit":false,"breakAffinityOnQueueWaitMs":0,
		"slowRequestThresholdMs":30000,"firstOutputSlowThresholdMs":15000,"recentTimeoutWindowSeconds":120,
		"recentTimeoutPenaltyThreshold":2,"maxQueueWaitMs":30000,"maxQueueSize":5000,"perApiKeyQueueLimit":5000,
		"clientIpConcurrencyLimit":0,"clientIpConcurrencyOverflowMode":"reject","imageLaneMaxConcurrency":0}`
	insertGroup(t, db, "g_default", "u1", "旧默认高并发", "standard", LegacyGroupTypeHighConcurrency, legacyDefault)
	insertGroup(t, db, "g_explicit", "u1", "显式阈值高并发", "standard", LegacyGroupTypeHighConcurrency, legacyExplicit)
	insertGroup(t, db, "g_noip", "u2", "无IP高并发", "standard", LegacyGroupTypeHighConcurrency, legacyNoIP)
	insertGroup(t, db, "g_plain", "u1", "普通分组", "standard", GroupTypePersonal, nil)
	insertGroup(t, db, "g_twok", "u1", "两键普通分组", "standard", GroupTypePersonal,
		`{"clientIpConcurrencyLimit":5,"defaultSoftConcurrency":5}`)
	insertGroup(t, db, "g_super", "u1", "超级分组", "super", LegacyGroupTypeHighConcurrency, legacyDefault)
	insertSuperMember(t, db, "g_super", "g_twok", 0)
	insertSuperMember(t, db, "g_super", "g_plain", 1)
	insertAuthSetting(t, db, "auth_high", "u3", "g_explicit", LegacyGroupTypeHighConcurrency,
		`{"defaultSoftConcurrency":9,"clientIpConcurrencyLimit":2,"maxQueueWaitMs":60000}`)
	insertAuthSetting(t, db, "auth_plain", "u4", "g_plain", GroupTypePersonal, nil)
}

func TestPlanIsReadOnly(t *testing.T) {
	db := openFixture(t)
	seedLegacyDataset(t, db)
	before := snapshotGroups(t, db)

	plan, err := BuildPlan(context.Background(), db, Options{GlobalMax: 5000})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked != 0 {
		t.Fatalf("标准夹具不应有 needs_review 行: %+v", plan.NeedsReview)
	}
	if plan.HighConcurrencyGroups != 4 || plan.HighConcurrencyAuthSettings != 1 {
		t.Fatalf("high_concurrency 计数 = (groups %d, auth %d), want (4, 1)",
			plan.HighConcurrencyGroups, plan.HighConcurrencyAuthSettings)
	}
	if plan.SuperGroups != 1 {
		t.Fatalf("超级分组计数 = %d, want 1", plan.SuperGroups)
	}
	if plan.QueueOverflowRows != 1 || plan.ImageLaneRows != 1 {
		t.Fatalf("行为标注计数 = (queue %d, imageLane %d), want (1, 1)", plan.QueueOverflowRows, plan.ImageLaneRows)
	}
	if plan.Mapped == 0 || plan.Unchanged == 0 {
		t.Fatalf("计划应同时含 update 与 unchanged 行: update=%d unchanged=%d", plan.Mapped, plan.Unchanged)
	}
	if plan.DefaultSoftDistribution["-1"] == 0 || plan.DefaultSoftDistribution["12"] != 1 || plan.DefaultSoftDistribution["6"] != 1 {
		t.Fatalf("阈值分布不符: %v", plan.DefaultSoftDistribution)
	}
	after := snapshotGroups(t, db)
	if !equalSnapshots(before, after) {
		t.Fatal("Plan 必须只读：库内行发生变化")
	}
}

func TestApplyMigratesBothTablesAndSuperGroup(t *testing.T) {
	db := openFixture(t)
	seedLegacyDataset(t, db)

	plan, err := Apply(context.Background(), db, Options{GlobalMax: 5000})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if plan.Applied == 0 || plan.Blocked != 0 {
		t.Fatalf("apply 计数 = (applied %d, blocked %d)", plan.Applied, plan.Blocked)
	}
	// 旧默认 5000 → 两项都默认 → NULL；显式阈值保留；IP 保留；group_type 归一。
	groupType, policy, updated := groupFact(t, db, "g_default")
	if groupType != GroupTypePersonal || policy.Valid {
		t.Fatalf("g_default = (%q, %v), want (personal, NULL)", groupType, policy)
	}
	if updated != fixtureNow {
		t.Fatalf("g_default updated_at = %q, 迁移不得触碰 updated_at", updated)
	}
	groupType, policy, _ = groupFact(t, db, "g_explicit")
	if groupType != GroupTypePersonal {
		t.Fatalf("g_explicit type = %q", groupType)
	}
	if policy.String != `{"clientIpConcurrencyLimit":8,"defaultSoftConcurrency":12}` {
		t.Fatalf("g_explicit policy = %s", policy.String)
	}
	// 超级分组自身行按首成员（g_twok，两键 5/5）重算，保持成员指纹一致。
	groupType, policy, _ = groupFact(t, db, "g_super")
	if groupType != GroupTypePersonal || policy.String != `{"clientIpConcurrencyLimit":5,"defaultSoftConcurrency":5}` {
		t.Fatalf("g_super = (%q, %s), want 按首成员重算 (personal, 5/5)", groupType, policy.String)
	}
	// 授权设置覆盖退役：group_type=personal、策略列 NULL。
	var authType string
	var authPolicy sql.NullString
	if err := db.QueryRow(`SELECT group_type, scheduling_policy_json FROM juhe_business.group_authorization_settings WHERE authorization_id = 'auth_high'`).
		Scan(&authType, &authPolicy); err != nil {
		t.Fatal(err)
	}
	if authType != GroupTypePersonal || authPolicy.Valid {
		t.Fatalf("auth_high = (%q, %v), want (personal, NULL)", authType, authPolicy)
	}
	// 完成态谓词通过。
	satisfied, err := Satisfied(context.Background(), db)
	if err != nil {
		t.Fatalf("Satisfied: %v", err)
	}
	if !satisfied {
		report, err := VerifyPostflight(context.Background(), db)
		if err != nil {
			t.Fatalf("VerifyPostflight: %v", err)
		}
		t.Fatalf("迁移后完成态未满足: %+v", report)
	}
}

func TestApplyNormalizesEmptySuperGroup(t *testing.T) {
	db := openFixture(t)
	// 空超级分组是正常状态（创建后尚未添加成员）：不阻断，自身行收敛
	// personal + NULL（成员加入时由 super_members 回写）。
	insertGroup(t, db, "g_empty_super", "u1", "空超级分组", "super", LegacyGroupTypeHighConcurrency,
		`{"defaultSoftConcurrency":12,"clientIpConcurrencyLimit":5,"maxQueueWaitMs":60000}`)
	plan, err := Apply(context.Background(), db, Options{GlobalMax: 5000})
	if err != nil {
		t.Fatalf("空超级分组不得阻断 Apply: %v", err)
	}
	if plan.Blocked != 0 {
		t.Fatalf("blocked = %d, want 0", plan.Blocked)
	}
	groupType, policy, updated := groupFact(t, db, "g_empty_super")
	if groupType != GroupTypePersonal || policy.Valid {
		t.Fatalf("空超级分组 = (%q, %v), want (personal, NULL)", groupType, policy)
	}
	if updated != fixtureNow {
		t.Fatalf("updated_at = %q, 迁移不得触碰", updated)
	}
}

func TestApplyIsIdempotentRerunZeroWrites(t *testing.T) {
	db := openFixture(t)
	seedLegacyDataset(t, db)
	if _, err := Apply(context.Background(), db, Options{GlobalMax: 5000}); err != nil {
		t.Fatalf("首次 Apply: %v", err)
	}
	before := snapshotGroups(t, db)

	rerun, err := Apply(context.Background(), db, Options{GlobalMax: 5000})
	if err != nil {
		t.Fatalf("幂等重跑 Apply: %v", err)
	}
	if rerun.Applied != 0 || rerun.Mapped != 0 || rerun.Blocked != 0 {
		t.Fatalf("幂等重跑 = (applied %d, mapped %d, blocked %d), want 全零", rerun.Applied, rerun.Mapped, rerun.Blocked)
	}
	after := snapshotGroups(t, db)
	if !equalSnapshots(before, after) {
		t.Fatal("幂等重跑零写入承诺被破坏")
	}
}

func TestApplyBlockedByNeedsReviewZeroWrites(t *testing.T) {
	db := openFixture(t)
	seedLegacyDataset(t, db)
	// 三种畸形形态各一行：损坏 JSON / 未知键 / 未知 group_type。
	insertGroup(t, db, "g_broken", "u1", "损坏JSON", "standard", LegacyGroupTypeHighConcurrency,
		`{"defaultSoftConcurrency":`)
	insertGroup(t, db, "g_unknown_key", "u1", "未知键", "standard", GroupTypePersonal,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5,"legacyCustom":true}`)
	insertGroup(t, db, "g_unknown_type", "u1", "未知类型", "standard", "team", nil)

	plan, err := Apply(context.Background(), db, Options{GlobalMax: 5000})
	if !errors.Is(err, ErrNeedsReview) {
		t.Fatalf("needs_review 行必须以 ErrNeedsReview 阻断 Apply: %v", err)
	}
	if plan.Blocked != 3 || plan.Applied != 0 {
		t.Fatalf("阻断计数 = (blocked %d, applied %d), want (3, 0)", plan.Blocked, plan.Applied)
	}
	// 零写入：畸形行与待迁移行全部保持原状。
	groupType, _, _ := groupFact(t, db, "g_default")
	if groupType != LegacyGroupTypeHighConcurrency {
		t.Fatalf("阻断后 g_default type = %q, 不得部分迁移", groupType)
	}
	// 未迁移库的完成态谓词必须为假。
	satisfied, err := Satisfied(context.Background(), db)
	if err != nil {
		t.Fatalf("Satisfied: %v", err)
	}
	if satisfied {
		t.Fatal("未迁移库不应满足完成态谓词")
	}
}

func TestVerifyPostflightDetectsInvalidPolicy(t *testing.T) {
	db := openFixture(t)
	seedLegacyDataset(t, db)
	if _, err := Apply(context.Background(), db, Options{GlobalMax: 5000}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// 迁移后手工写入一个违规形状（模拟旧二进制/外部写入残留）。
	execFixture(t, db, `UPDATE juhe_business.groups SET scheduling_policy_json = $1 WHERE id = 'g_plain'`,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5,"mode":"balanced_fast"}`)
	report, err := VerifyPostflight(context.Background(), db)
	if err != nil {
		t.Fatalf("VerifyPostflight: %v", err)
	}
	if report.Ready() || report.InvalidPolicyGroups != 1 || len(report.Examples) == 0 {
		t.Fatalf("postflight 应报告策略列违规: %+v", report)
	}
}

// TestVerifyPostflightRejectsMissingTables 钉住错库防护：--check=postflight
// 指向没有 juhe_business.groups 的库时必须报错，不能按 0 行误报 Ready；
// upgrade 步骤的 fresh 库语义由 Satisfied 的 tableExists 守卫单独承担。
func TestVerifyPostflightRejectsMissingTables(t *testing.T) {
	db := pgtestkit.New(t)
	if _, err := VerifyPostflight(context.Background(), db); err == nil {
		t.Fatal("缺迁移面表时必须报错，不得误报完成态")
	}
}

// snapshotGroups 读回 groups/授权设置全表（幂等比较用）。
func snapshotGroups(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := db.Query(`SELECT id, group_type, COALESCE(scheduling_policy_json,''), updated_at FROM juhe_business.groups`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, groupType, policy, updated string
		if err := rows.Scan(&id, &groupType, &policy, &updated); err != nil {
			t.Fatal(err)
		}
		out[id] = groupType + "|" + policy + "|" + updated
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows2, err := db.Query(`SELECT authorization_id, group_type, COALESCE(scheduling_policy_json,'') FROM juhe_business.group_authorization_settings`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var id, groupType, policy string
		if err := rows2.Scan(&id, &groupType, &policy); err != nil {
			t.Fatal(err)
		}
		out["auth:"+id] = groupType + "|" + policy
	}
	if err := rows2.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func equalSnapshots(before, after map[string]string) bool {
	if len(before) != len(after) {
		return false
	}
	for key, value := range before {
		if after[key] != value {
			return false
		}
	}
	return true
}

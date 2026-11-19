package keyipmigration

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// fixtureDDL 在隔离测试库上建迁移面五表（列形状与生产业务库同构的测试子集）。
// 迁移是纯数据步骤 + 已存在列的写入；不建外键以简化夹具。
const fixtureDDL = `
CREATE SCHEMA juhe_business;
SET search_path = juhe_business;
CREATE TABLE groups (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
  enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0,
  group_type TEXT NOT NULL DEFAULT 'personal', group_kind TEXT NOT NULL DEFAULT 'standard',
  scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE super_group_members (
  id TEXT PRIMARY KEY, super_group_id TEXT NOT NULL, member_group_id TEXT NOT NULL,
  priority INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (super_group_id, member_group_id), UNIQUE (super_group_id, priority));
CREATE TABLE route_strategies (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'normal',
  status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, config_json TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE route_strategy_groups (
  id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE api_keys (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, name TEXT NOT NULL,
  client_ip_concurrency_limit INTEGER NOT NULL DEFAULT -1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
`

const fixtureNow = "2026-11-01T00:00:00Z"

func openFixture(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(fixtureDDL)
		return err
	})
}

func execFixture(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture 写入失败 %s: %v", query, err)
	}
}

func insertGroup(t *testing.T, db *sql.DB, id, owner, name, kind, groupType string, enabled bool, policy any) {
	t.Helper()
	execFixture(t, db, `INSERT INTO juhe_business.groups
		(id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, $4, 0, $5, $6, $7, $8, $8)`,
		id, owner, name, boolInt(enabled), groupType, kind, policy, fixtureNow)
}

func insertSuperMember(t *testing.T, db *sql.DB, superGroupID, memberID string, priority int, enabled bool) {
	t.Helper()
	execFixture(t, db, `INSERT INTO juhe_business.super_group_members
		(id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)`,
		"sgm_"+superGroupID+"_"+memberID, superGroupID, memberID, priority, boolInt(enabled), fixtureNow)
}

func insertStrategy(t *testing.T, db *sql.DB, id, owner, mode, status string) {
	t.Helper()
	execFixture(t, db, `INSERT INTO juhe_business.route_strategies
		(id, system_account_id, name, mode, status, created_at, updated_at)
		VALUES ($1, $2, $1, $3, $4, $5, $5)`, id, owner, mode, status, fixtureNow)
}

func insertBinding(t *testing.T, db *sql.DB, id, strategyID, owner, groupID, status string) {
	t.Helper()
	execFixture(t, db, `INSERT INTO juhe_business.route_strategy_groups
		(id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, 1, $5, $6, $6)`, id, strategyID, owner, groupID, status, fixtureNow)
}

func insertKey(t *testing.T, db *sql.DB, id, owner, strategyID string, current int) {
	t.Helper()
	execFixture(t, db, `INSERT INTO juhe_business.api_keys
		(id, system_account_id, route_strategy_id, name, client_ip_concurrency_limit, created_at, updated_at)
		VALUES ($1, $2, $3, $1, $4, $5, $5)`, id, owner, strategyID, current, fixtureNow)
}

func groupFact(t *testing.T, db *sql.DB, id string) (string, sql.NullString, string) {
	t.Helper()
	var groupType, updated string
	var policy sql.NullString
	if err := db.QueryRow(`SELECT group_type, scheduling_policy_json, updated_at FROM juhe_business.groups WHERE id = $1`, id).
		Scan(&groupType, &policy, &updated); err != nil {
		t.Fatalf("读回分组 %s: %v", id, err)
	}
	return groupType, policy, updated
}

func keyFact(t *testing.T, db *sql.DB, id string) (int, string) {
	t.Helper()
	var value int
	var updated string
	if err := db.QueryRow(`SELECT client_ip_concurrency_limit, updated_at FROM juhe_business.api_keys WHERE id = $1`, id).
		Scan(&value, &updated); err != nil {
		t.Fatalf("读回 Key %s: %v", id, err)
	}
	return value, updated
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// TestApplyMapsUniqueValueAndRewritesGroups 覆盖唯一正值写入、无值写 0 与分组
// 单键化；updated_at 不得变化。
func TestApplyMapsUniqueValueAndRewritesGroups(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}`)
	insertGroup(t, db, "g2", "u1", "组二", "standard", "personal", true, nil)
	insertStrategy(t, db, "s1", "u1", "normal", "active")
	insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
	insertBinding(t, db, "b2", "s1", "u1", "g2", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)

	plan, err := Apply(context.Background(), db, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if plan.Applied == 0 || plan.Blocked != 0 {
		t.Fatalf("apply 计数 = applied %d / blocked %d", plan.Applied, plan.Blocked)
	}
	if value, updated := keyFact(t, db, "k1"); value != 8 || updated != fixtureNow {
		t.Fatalf("Key = (%d, %s), want (8, %s)", value, updated, fixtureNow)
	}
	groupType, policy, updated := groupFact(t, db, "g1")
	if groupType != "personal" || policy.String != `{"defaultSoftConcurrency":5}` || updated != fixtureNow {
		t.Fatalf("g1 = (%q, %v, %s), want (personal, 单键 5, %s)", groupType, policy, updated, fixtureNow)
	}
	_, policy, _ = groupFact(t, db, "g2")
	if policy.Valid {
		t.Fatalf("g2 NULL 策略不应被改写: %v", policy)
	}
	satisfied, err := Satisfied(context.Background(), db)
	if err != nil || !satisfied {
		t.Fatalf("迁移后 Satisfied = %t/%v", satisfied, err)
	}
}

// TestApplyBlockByMultipleValuesZeroWrites 覆盖多值默认 block：退出哨兵错误、
// 零写入。
func TestApplyBlockByMultipleValuesZeroWrites(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5}`)
	insertGroup(t, db, "g2", "u1", "组二", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":10}`)
	insertStrategy(t, db, "s1", "u1", "normal", "active")
	insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
	insertBinding(t, db, "b2", "s1", "u1", "g2", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)

	plan, err := Apply(context.Background(), db, Options{ConflictPolicy: ConflictPolicyBlock})
	if !errors.Is(err, ErrNeedsReview) {
		t.Fatalf("多值必须 ErrNeedsReview: %v", err)
	}
	if plan.Applied != 0 || plan.Blocked == 0 {
		t.Fatalf("计数 = applied %d / blocked %d", plan.Applied, plan.Blocked)
	}
	if value, _ := keyFact(t, db, "k1"); value != -1 {
		t.Fatalf("阻断后 Key 值 = %d, want -1", value)
	}
	_, policy, _ := groupFact(t, db, "g1")
	if policy.String != `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5}` {
		t.Fatalf("阻断后分组策略不应变化: %v", policy)
	}
}

// TestApplyMinResolvesMultipleAndRecordsConflict 覆盖 min 收敛与冲突留档。
func TestApplyMinResolvesMultipleAndRecordsConflict(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5}`)
	insertGroup(t, db, "g2", "u1", "组二", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":10}`)
	insertStrategy(t, db, "s1", "u1", "normal", "active")
	insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
	insertBinding(t, db, "b2", "s1", "u1", "g2", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)

	plan, err := Apply(context.Background(), db, Options{ConflictPolicy: ConflictPolicyMin})
	if err != nil {
		t.Fatalf("Apply(min): %v", err)
	}
	if value, _ := keyFact(t, db, "k1"); value != 5 {
		t.Fatalf("min 后 Key 值 = %d, want 5", value)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Kind != ConflictKindMultipleGroupValues ||
		plan.Conflicts[0].ChosenValue != 5 || plan.Conflicts[0].Resolution != ConflictResolutionMinSelected {
		t.Fatalf("冲突行 = %+v", plan.Conflicts)
	}
}

// TestPreexistingValues 覆盖人工裁决值语义。
func TestPreexistingValues(t *testing.T) {
	t.Run("命中观测值不阻断", func(t *testing.T) {
		db := openFixture(t)
		insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5}`)
		insertGroup(t, db, "g2", "u1", "组二", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":10}`)
		insertStrategy(t, db, "s1", "u1", "normal", "active")
		insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
		insertBinding(t, db, "b2", "s1", "u1", "g2", "active")
		insertKey(t, db, "k1", "u1", "s1", 5)

		plan, err := Apply(context.Background(), db, Options{ConflictPolicy: ConflictPolicyBlock})
		if err != nil {
			t.Fatalf("人工值命中候选集合不应阻断: %v", err)
		}
		if plan.KeysPreexisting != 1 || len(plan.Conflicts) != 0 {
			t.Fatalf("preexisting=%d conflicts=%+v", plan.KeysPreexisting, plan.Conflicts)
		}
		if value, _ := keyFact(t, db, "k1"); value != 5 {
			t.Fatalf("Key 值 = %d, want 5（不覆盖）", value)
		}
	})

	t.Run("全部不同block", func(t *testing.T) {
		db := openFixture(t)
		insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5}`)
		insertGroup(t, db, "g2", "u1", "组二", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":10}`)
		insertStrategy(t, db, "s1", "u1", "normal", "active")
		insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
		insertBinding(t, db, "b2", "s1", "u1", "g2", "active")
		insertKey(t, db, "k1", "u1", "s1", 7)

		if _, err := Apply(context.Background(), db, Options{}); !errors.Is(err, ErrNeedsReview) {
			t.Fatalf("全部不同的 preexisting 值必须阻断: %v", err)
		}
		if value, _ := keyFact(t, db, "k1"); value != 7 {
			t.Fatalf("阻断后 Key 值 = %d, want 7", value)
		}
	})

	t.Run("全部不同min保留", func(t *testing.T) {
		db := openFixture(t)
		insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5}`)
		insertGroup(t, db, "g2", "u1", "组二", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":10}`)
		insertStrategy(t, db, "s1", "u1", "normal", "active")
		insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
		insertBinding(t, db, "b2", "s1", "u1", "g2", "active")
		insertKey(t, db, "k1", "u1", "s1", 7)

		plan, err := Apply(context.Background(), db, Options{ConflictPolicy: ConflictPolicyMin})
		if err != nil {
			t.Fatalf("min 模式 preexisting 冲突不应阻断: %v", err)
		}
		if value, _ := keyFact(t, db, "k1"); value != 7 {
			t.Fatalf("min 模式不得覆盖人工值: %d", value)
		}
		if len(plan.Conflicts) != 1 || plan.Conflicts[0].Resolution != ConflictResolutionPreexistingKept {
			t.Fatalf("冲突行 = %+v", plan.Conflicts)
		}
	})
}

// TestApplyInvalidJSONNeedsReview 覆盖畸形 JSON 的阻断与零写入。
func TestApplyInvalidJSONNeedsReview(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g_broken", "u1", "损坏", "standard", "personal", true,
		`{"defaultSoftConcurrency":`)
	insertStrategy(t, db, "s1", "u1", "normal", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)

	plan, err := Apply(context.Background(), db, Options{})
	if !errors.Is(err, ErrNeedsReview) {
		t.Fatalf("畸形 JSON 必须阻断: %v", err)
	}
	if plan.Applied != 0 {
		t.Fatalf("applied = %d, want 0", plan.Applied)
	}
	if _, policy, _ := groupFact(t, db, "g_broken"); policy.String != `{"defaultSoftConcurrency":` {
		t.Fatalf("畸形行原值被改写: %v", policy)
	}
}

// TestSuperGroupRecomputeAndMemberConsistency 覆盖超级分组重算、成员一致性
// 阻断、禁用成员仍收集旧值。
func TestSuperGroupRecomputeAndMemberConsistency(t *testing.T) {
	t.Run("按首成员重算并收集全部成员旧值", func(t *testing.T) {
		db := openFixture(t)
		insertGroup(t, db, "m1", "u1", "成员一", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":6}`)
		insertGroup(t, db, "m2", "u1", "成员二", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":6}`)
		insertGroup(t, db, "sg", "u1", "超级", "super", "high_concurrency", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":2}`)
		insertSuperMember(t, db, "sg", "m1", 1, true)
		insertSuperMember(t, db, "sg", "m2", 2, false)
		insertStrategy(t, db, "s1", "u1", "normal", "active")
		insertBinding(t, db, "b1", "s1", "u1", "sg", "active")
		insertKey(t, db, "k1", "u1", "s1", -1)

		plan, err := Apply(context.Background(), db, Options{})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if value, _ := keyFact(t, db, "k1"); value != 6 {
			t.Fatalf("Key 值 = %d, want 6（超级分组成员旧值）", value)
		}
		groupType, policy, _ := groupFact(t, db, "sg")
		if groupType != "personal" || policy.String != `{"defaultSoftConcurrency":5}` {
			t.Fatalf("超级分组自身行 = (%q, %v), want (personal, 单键 5)", groupType, policy)
		}
		if plan.EmptySuperGroups != 0 {
			t.Fatalf("emptySuperGroups = %d", plan.EmptySuperGroups)
		}
		// 禁用成员仍进入可达来源，runtimeActive 标注 false。
		var foundDisabled, foundActive bool
		for _, key := range plan.Keys {
			for _, source := range key.SourceGroups {
				if source.GroupID == "m2" && !source.RuntimeActive {
					foundDisabled = true
				}
				if source.GroupID == "m1" && source.RuntimeActive {
					foundActive = true
				}
			}
		}
		if !foundDisabled || !foundActive {
			t.Fatalf("runtimeActive 标注不符: %+v", plan.Keys)
		}
	})

	t.Run("自身行基准跳过禁用的高优先级成员", func(t *testing.T) {
		db := openFixture(t)
		// 优先级最高的成员被禁用且类型不同：自身行必须按首个**启用**成员
		// （m2, personal）重算，postflight 也必须用同一基准，否则 Apply 后会
		// 立即误报超级分组不一致。
		insertGroup(t, db, "m1", "u1", "禁用成员", "standard", "high_concurrency", false,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":6}`)
		insertGroup(t, db, "m2", "u1", "启用成员", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":6}`)
		insertGroup(t, db, "sg", "u1", "超级", "super", "high_concurrency", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":2}`)
		insertSuperMember(t, db, "sg", "m1", 1, false)
		insertSuperMember(t, db, "sg", "m2", 2, true)
		insertStrategy(t, db, "s1", "u1", "normal", "active")
		insertBinding(t, db, "b1", "s1", "u1", "sg", "active")
		insertKey(t, db, "k1", "u1", "s1", -1)

		if _, err := Apply(context.Background(), db, Options{}); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		groupType, policy, _ := groupFact(t, db, "sg")
		if groupType != "personal" || policy.String != `{"defaultSoftConcurrency":5}` {
			t.Fatalf("超级分组自身行 = (%q, %v), want (personal, 单键 5)", groupType, policy)
		}
		report, err := VerifyPostflight(context.Background(), db)
		if err != nil {
			t.Fatalf("VerifyPostflight: %v", err)
		}
		if !report.Ready() {
			t.Fatalf("Apply 后完成态必须就绪: %+v", report)
		}
	})

	t.Run("成员阈值不一致阻断", func(t *testing.T) {
		db := openFixture(t)
		insertGroup(t, db, "m1", "u1", "成员一", "standard", "personal", true,
			`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":6}`)
		insertGroup(t, db, "m2", "u1", "成员二", "standard", "personal", true,
			`{"defaultSoftConcurrency":6,"clientIpConcurrencyLimit":6}`)
		insertGroup(t, db, "sg", "u1", "超级", "super", "personal", true, nil)
		insertSuperMember(t, db, "sg", "m1", 1, true)
		insertSuperMember(t, db, "sg", "m2", 2, true)
		insertStrategy(t, db, "s1", "u1", "normal", "active")
		insertKey(t, db, "k1", "u1", "s1", -1)

		if _, err := Apply(context.Background(), db, Options{}); !errors.Is(err, ErrNeedsReview) {
			t.Fatalf("成员阈值不一致必须阻断: %v", err)
		}
		if _, policy, _ := groupFact(t, db, "sg"); policy.Valid {
			t.Fatalf("阻断后超级分组策略不应变化: %v", policy)
		}
	})

	t.Run("成员阈值不一致时postflight与Satisfied均不满足", func(t *testing.T) {
		db := openFixture(t)
		insertGroup(t, db, "m1", "u1", "成员一", "standard", "personal", true,
			`{"defaultSoftConcurrency":5}`)
		insertGroup(t, db, "m2", "u1", "成员二", "standard", "personal", true,
			`{"defaultSoftConcurrency":6}`)
		// 自身行与首成员一致，但成员之间阈值不一致：postflight 与 Satisfied
		// 都必须发现，不得因自身行一致而静默放行。
		insertGroup(t, db, "sg", "u1", "超级", "super", "personal", true,
			`{"defaultSoftConcurrency":5}`)
		insertSuperMember(t, db, "sg", "m1", 1, true)
		insertSuperMember(t, db, "sg", "m2", 2, true)
		insertStrategy(t, db, "s1", "u1", "normal", "active")
		insertKey(t, db, "k1", "u1", "s1", -1)

		if _, err := Apply(context.Background(), db, Options{}); !errors.Is(err, ErrNeedsReview) {
			t.Fatalf("成员阈值不一致必须阻断: %v", err)
		}
		report, err := VerifyPostflight(context.Background(), db)
		if err != nil {
			t.Fatalf("VerifyPostflight: %v", err)
		}
		if report.Ready() || report.SuperGroupMismatches != 1 {
			t.Fatalf("成员阈值不一致 postflight 必须不满足: %+v", report)
		}
		satisfied, err := Satisfied(context.Background(), db)
		if err != nil {
			t.Fatalf("Satisfied: %v", err)
		}
		if satisfied {
			t.Fatal("成员阈值不一致时 Satisfied 不得返回 true（否则 upgrade 会静默放行）")
		}
	})
}

// TestEmptySuperGroupConverges 覆盖空超级分组 personal + NULL。
func TestEmptySuperGroupConverges(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "sg", "u1", "空超级", "super", "high_concurrency", true,
		`{"defaultSoftConcurrency":-1,"clientIpConcurrencyLimit":5}`)
	plan, err := Apply(context.Background(), db, Options{})
	if err != nil {
		t.Fatalf("空超级分组不得阻断: %v", err)
	}
	if plan.EmptySuperGroups != 1 {
		t.Fatalf("emptySuperGroups = %d, want 1", plan.EmptySuperGroups)
	}
	groupType, policy, _ := groupFact(t, db, "sg")
	if groupType != "personal" || policy.Valid {
		t.Fatalf("空超级分组 = (%q, %v), want (personal, NULL)", groupType, policy)
	}
}

// TestApplyIdempotentRerunZeroWrites 覆盖完成态重跑零写入（含 preexisting
// 值不被清空）。
func TestApplyIdempotentRerunZeroWrites(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}`)
	insertStrategy(t, db, "s1", "u1", "normal", "active")
	insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)
	if _, err := Apply(context.Background(), db, Options{}); err != nil {
		t.Fatalf("首次 Apply: %v", err)
	}
	beforeKey, beforeKeyUpdated := keyFact(t, db, "k1")
	beforeType, beforePolicy, beforeGroupUpdated := groupFact(t, db, "g1")

	plan, err := Apply(context.Background(), db, Options{})
	if err != nil {
		t.Fatalf("幂等重跑: %v", err)
	}
	if plan.Applied != 0 || plan.Blocked != 0 {
		t.Fatalf("幂等重跑 = applied %d / blocked %d", plan.Applied, plan.Blocked)
	}
	afterKey, afterKeyUpdated := keyFact(t, db, "k1")
	afterType, afterPolicy, afterGroupUpdated := groupFact(t, db, "g1")
	if afterKey != beforeKey || afterKeyUpdated != beforeKeyUpdated {
		t.Fatalf("Key 被重写: (%d,%s) -> (%d,%s)", beforeKey, beforeKeyUpdated, afterKey, afterKeyUpdated)
	}
	if afterType != beforeType || afterPolicy.String != beforePolicy.String || afterGroupUpdated != beforeGroupUpdated {
		t.Fatalf("分组被重写: (%q,%v,%s) -> (%q,%v,%s)",
			beforeType, beforePolicy, beforeGroupUpdated, afterType, afterPolicy, afterGroupUpdated)
	}
}

// TestAuthorizedCrossOwnerGroupIsMapped 覆盖合法授权形态：被授权人的 Key 经
// 自己的策略绑定授权方所有的分组（binding owner=grantee，group owner=grantor）。
// 设计 §4.2-1 要求按配置拓扑全量解析、不按授权状态过滤，旧 IP 值必须迁移到
// Key，而不是把跨 owner 当作脏数据阻断。
func TestAuthorizedCrossOwnerGroupIsMapped(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u2", "授权组", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}`)
	insertStrategy(t, db, "s1", "u1", "normal", "active")
	insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)

	plan, err := Apply(context.Background(), db, Options{})
	if err != nil {
		t.Fatalf("授权跨 owner 分组不得阻断: %v", err)
	}
	if plan.KeysUpdated != 1 {
		t.Fatalf("keysUpdated = %d, want 1", plan.KeysUpdated)
	}
	if value, _ := keyFact(t, db, "k1"); value != 8 {
		t.Fatalf("Key 限制 = %d, want 8", value)
	}
	if _, policy, _ := groupFact(t, db, "g1"); policy.String != `{"defaultSoftConcurrency":5}` {
		t.Fatalf("分组策略应单键化: %v", policy)
	}
	// 幂等重跑零写入。
	plan, err = Apply(context.Background(), db, Options{})
	if err != nil {
		t.Fatalf("幂等重跑: %v", err)
	}
	if plan.Applied != 0 || plan.Blocked != 0 {
		t.Fatalf("幂等重跑 = applied %d / blocked %d", plan.Applied, plan.Blocked)
	}
}

// TestRouteStrategyOwnerMismatchNeedsReview 覆盖 Key 绑定他人路由策略的非法
// 拓扑（策略不可共享），仍需人工复核。
func TestRouteStrategyOwnerMismatchNeedsReview(t *testing.T) {
	db := openFixture(t)
	insertStrategy(t, db, "s1", "u2", "normal", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)

	if _, err := Apply(context.Background(), db, Options{}); !errors.Is(err, ErrNeedsReview) {
		t.Fatalf("路由策略 owner 不一致必须阻断: %v", err)
	}
}

// TestSatisfiedPlanGuardIndependentOfPostflight 钉住 Satisfied 的第二道守卫：
// 分组策略已是单键、Key 值合法（postflight 形状 Ready），但 Key 的
// route_strategy_id 悬挂时，计划盘点仍必须阻断，避免 upgrade 静默放行。
func TestSatisfiedPlanGuardIndependentOfPostflight(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
		`{"defaultSoftConcurrency":5}`)
	insertKey(t, db, "k1", "u1", "s_missing", -1)

	report, err := VerifyPostflight(context.Background(), db)
	if err != nil {
		t.Fatalf("VerifyPostflight: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("本场景形状检查应通过（用于证明计划守卫独立生效）: %+v", report)
	}
	satisfied, err := Satisfied(context.Background(), db)
	if err != nil {
		t.Fatalf("Satisfied: %v", err)
	}
	if satisfied {
		t.Fatal("路由策略悬挂时 Satisfied 不得返回 true")
	}
}

// TestSatisfiedRequiresNoPendingWrites 钉住完成态还要求盘点无待写入行：
// `{"defaultSoftConcurrency":-1}` 形状 postflight 视为合法，但设计 §4.2
// 要求 -1 落 SQL NULL；Satisfied 必须判未完成，Apply 后才满足。
func TestSatisfiedRequiresNoPendingWrites(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
		`{"defaultSoftConcurrency":-1}`)
	insertStrategy(t, db, "s1", "u1", "normal", "active")
	insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)

	report, err := VerifyPostflight(context.Background(), db)
	if err != nil || !report.Ready() {
		t.Fatalf("本场景 postflight 形状应通过（用于证明待写入守卫独立生效）: %+v/%v", report, err)
	}
	satisfied, err := Satisfied(context.Background(), db)
	if err != nil {
		t.Fatalf("Satisfied: %v", err)
	}
	if satisfied {
		t.Fatal("仍有分组待单键化（-1 → NULL）时 Satisfied 不得返回 true")
	}
	if _, err := Apply(context.Background(), db, Options{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, policy, _ := groupFact(t, db, "g1"); policy.Valid {
		t.Fatalf("soft=-1 应落 SQL NULL: %v", policy)
	}
	satisfied, err = Satisfied(context.Background(), db)
	if err != nil || !satisfied {
		t.Fatalf("Apply 后 Satisfied = %t/%v, want true", satisfied, err)
	}
}

// TestBuildPlanDryRunZeroWrites 覆盖 dry-run 只读承诺。
func TestBuildPlanDryRunZeroWrites(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}`)
	insertStrategy(t, db, "s1", "u1", "normal", "active")
	insertBinding(t, db, "b1", "s1", "u1", "g1", "active")
	insertKey(t, db, "k1", "u1", "s1", -1)

	plan, err := BuildPlan(context.Background(), db, Options{DryRun: true})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if !plan.DryRun || plan.Applied != 0 {
		t.Fatalf("dry-run 报告 = %+v", plan)
	}
	if value, _ := keyFact(t, db, "k1"); value != -1 {
		t.Fatalf("dry-run 写入 Key = %d, want -1", value)
	}
	_, policy, _ := groupFact(t, db, "g1")
	if policy.String != `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}` {
		t.Fatalf("dry-run 写入分组: %v", policy)
	}
}

// TestVerifyPostflightDetectsLegacyKey 覆盖 postflight 对旧键残留的检测。
func TestVerifyPostflightDetectsLegacyKey(t *testing.T) {
	db := openFixture(t)
	insertGroup(t, db, "g1", "u1", "组一", "standard", "personal", true,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}`)
	insertKey(t, db, "k1", "u1", "s1", -1)
	report, err := VerifyPostflight(context.Background(), db)
	if err != nil {
		t.Fatalf("VerifyPostflight: %v", err)
	}
	if report.Ready() || report.LegacyIPKeyGroups != 1 || len(report.Examples) == 0 {
		t.Fatalf("postflight 应报告旧键残留: %+v", report)
	}
	// 修复后 Ready。
	execFixture(t, db, `UPDATE juhe_business.groups SET scheduling_policy_json = '{"defaultSoftConcurrency":5}' WHERE id = 'g1'`)
	execFixture(t, db, `UPDATE juhe_business.api_keys SET client_ip_concurrency_limit = 8 WHERE id = 'k1'`)
	report, err = VerifyPostflight(context.Background(), db)
	if err != nil || !report.Ready() {
		t.Fatalf("修复后 postflight = %+v/%v", report, err)
	}
}

// TestVerifyPostflightRejectsMissingTables 钉住错库防护：缺表直接报错，不按 0
// 行误报 Ready。
func TestVerifyPostflightRejectsMissingTables(t *testing.T) {
	db := pgtestkit.New(t)
	if _, err := VerifyPostflight(context.Background(), db); err == nil {
		t.Fatal("缺迁移面表时必须报错，不得误报完成态")
	}
}

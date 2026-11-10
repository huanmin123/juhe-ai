package legacybusinessimport

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// 回归背景（2026-11-09 高并发分组退役，普通分组可选调度策略设计 §5.2）：
// 回迁不再复制 high_concurrency 分组类型与 16 键旧策略——groups 一律归一
// personal + 两键/NULL（globalMax 取旧进程默认 5000），授权设置策略列恒 NULL
// （覆盖退役）；needs_review 形状中断导入并保留原始错误。
var groupPolicyImportDDLs = []string{
	`CREATE TABLE juhe_business.groups (
		id text PRIMARY KEY, system_account_id text NOT NULL, name text NOT NULL,
		description text, enabled integer NOT NULL DEFAULT 1, is_default integer NOT NULL DEFAULT 0,
		group_type text NOT NULL DEFAULT 'personal', group_kind text NOT NULL DEFAULT 'standard',
		scheduling_policy_json text, created_at text NOT NULL, updated_at text NOT NULL)`,
	`CREATE TABLE juhe_business.group_authorization_settings (
		authorization_id text PRIMARY KEY, system_account_id text NOT NULL, group_id text NOT NULL,
		enabled integer NOT NULL DEFAULT 1, group_type text NOT NULL DEFAULT 'personal',
		scheduling_policy_json text, created_at text NOT NULL, updated_at text NOT NULL)`,
}

func openGroupPolicyImportDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, conn *sql.DB) error {
		if _, err := conn.Exec(`CREATE SCHEMA juhe_business`); err != nil {
			return err
		}
		for _, ddl := range groupPolicyImportDDLs {
			if _, err := conn.Exec(ddl); err != nil {
				return err
			}
		}
		return nil
	})
}

func TestImportGroupsNormalizesLegacyPolicy(t *testing.T) {
	source := openGroupPolicyImportDB(t)
	target := openGroupPolicyImportDB(t)
	tx, err := target.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("开启目标事务: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
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
	for _, row := range []struct{ id, groupType, policy string }{
		{"g_hc_default", "high_concurrency", legacyDefault},
		{"g_hc_explicit", "high_concurrency", legacyExplicit},
		{"g_plain", "personal", ""},
	} {
		policyArg := any(nil)
		if row.policy != "" {
			policyArg = row.policy
		}
		if _, err := source.Exec(`INSERT INTO juhe_business.groups
			(id, system_account_id, name, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
			VALUES ($1,'owner',$1,1,0,$2,$3,'2026-07-01T00:00:00Z','2026-07-01T00:00:00Z')`,
			row.id, row.groupType, policyArg); err != nil {
			t.Fatalf("源库夹具写入 %s: %v", row.id, err)
		}
	}
	report := &Report{Tables: map[string]int{}}
	owners := map[string]struct{}{"owner": {}}
	ids, err := importGroups(context.Background(), source, tx, owners, report)
	if err != nil {
		t.Fatalf("importGroups: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("导入分组数 = %d, want 3", len(ids))
	}
	// 旧默认 5000 → 两项都默认 → NULL；显式阈值保留两键形状。
	for id, wantPolicy := range map[string]string{
		"g_hc_default":  "",
		"g_hc_explicit": `{"clientIpConcurrencyLimit":8,"defaultSoftConcurrency":12}`,
		"g_plain":       "",
	} {
		var groupType string
		var policy sql.NullString
		if err := tx.QueryRow(`SELECT group_type, scheduling_policy_json FROM juhe_business.groups WHERE id = $1`, id).
			Scan(&groupType, &policy); err != nil {
			t.Fatalf("读回 %s: %v", id, err)
		}
		if groupType != "personal" {
			t.Fatalf("%s group_type = %q, want personal", id, groupType)
		}
		got := ""
		if policy.Valid {
			got = policy.String
		}
		if got != wantPolicy {
			t.Fatalf("%s 策略列 = %q, want %q", id, got, wantPolicy)
		}
	}
	// 未知 group_type fail-closed：中断导入，保留原始错误。
	if _, err := source.Exec(`INSERT INTO juhe_business.groups
		(id, system_account_id, name, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ('g_bad','owner','畸形',1,0,'team',NULL,'2026-07-01T00:00:00Z','2026-07-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := importGroups(context.Background(), source, tx, owners, report); err == nil || !strings.Contains(err.Error(), "需要人工复核") {
		t.Fatalf("未知 group_type 应中断导入并提示人工复核: %v", err)
	}
}

func TestImportAuthorizationSettingsRetiresOverrides(t *testing.T) {
	source := openGroupPolicyImportDB(t)
	target := openGroupPolicyImportDB(t)
	tx, err := target.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("开启目标事务: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range []string{"g1", "g2"} {
		if _, err := source.Exec(`INSERT INTO juhe_business.groups
			(id, system_account_id, name, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
			VALUES ($1,'owner',$1,1,0,'personal',NULL,'2026-07-01T00:00:00Z','2026-07-01T00:00:00Z')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.Exec(`INSERT INTO juhe_business.group_authorization_settings
		(authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ('a1','grantee','g1',1,'high_concurrency','{"defaultSoftConcurrency":9,"clientIpConcurrencyLimit":2,"maxQueueWaitMs":60000}',
		'2026-07-01T00:00:00Z','2026-07-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	report := &Report{Tables: map[string]int{}}
	groups := map[string]struct{}{"g1": {}, "g2": {}}
	if err := importGroupAuthorizationSettings(context.Background(), source, tx, groups, report); err != nil {
		t.Fatalf("importGroupAuthorizationSettings: %v", err)
	}
	var groupType string
	var policy sql.NullString
	if err := tx.QueryRow(`SELECT group_type, scheduling_policy_json FROM juhe_business.group_authorization_settings WHERE authorization_id = 'a1'`).
		Scan(&groupType, &policy); err != nil {
		t.Fatal(err)
	}
	if groupType != "personal" || policy.Valid {
		t.Fatalf("授权设置 = (%q, %v), want (personal, NULL)——覆盖退役", groupType, policy)
	}
}

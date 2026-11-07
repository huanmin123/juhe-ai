// 安全策略实体（API Key 安全策略与出站内容脱敏设计 §4.1，批A 存储）存储与
// 种子测试：PG seed 语句与参数（wmSchemaRecorder 假驱动捕获），镜像
// scheduling_preference_test.go。
package schema

import (
	"context"
	"strings"
	"testing"
	"time"
)

var securityPolicyTestClock = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

// TestSeedPostgresDefaultsSeedsBuiltinSecurityPolicyStatement 用假驱动捕获 PG
// seed 的内置安全策略 upsert：语句出现、参数为固定 id 与权威 name。
func TestSeedPostgresDefaultsSeedsBuiltinSecurityPolicyStatement(t *testing.T) {
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	if _, err := SeedPostgresDefaults(context.Background(), db, SeedOptions{Now: func() time.Time { return securityPolicyTestClock }}); err != nil {
		t.Fatalf("SeedPostgresDefaults: %v", err)
	}
	found := false
	for _, statement := range rec.execs {
		if !strings.Contains(statement.query, `INSERT INTO "juhe_business"."security_policies"`) {
			continue
		}
		found = true
		if len(statement.args) != 4 {
			t.Fatalf("builtin security policy upsert args = %v, want 4", statement.args)
		}
		if statement.args[0] != BuiltinSecurityPolicyID || statement.args[1] != BuiltinSecurityPolicyName {
			t.Fatalf("builtin security policy upsert args = %v, want id %q name %q",
				statement.args, BuiltinSecurityPolicyID, BuiltinSecurityPolicyName)
		}
		if !strings.Contains(statement.query, `'{"rules":[]}'`) {
			t.Fatalf("builtin security policy upsert 缺少权威空规则集字面量: %s", statement.query)
		}
	}
	if !found {
		t.Fatal("SeedPostgresDefaults must issue the builtin security policy upsert")
	}
}

// TestSeedBuiltinSecurityPolicyPostgresEnsureArm 钉住 --ensure-schema 补种臂：
// 语句与 --seed 链共用同一权威 upsert（恢复式、幂等）。
func TestSeedBuiltinSecurityPolicyPostgresEnsureArm(t *testing.T) {
	if pgSeedBuiltinSecurityPolicyUpsert == "" {
		t.Fatal("pgSeedBuiltinSecurityPolicyUpsert must not be empty")
	}
	for _, must := range []string{
		`INSERT INTO "juhe_business"."security_policies"`,
		`ON CONFLICT (id) DO UPDATE SET`,
		`WHERE "juhe_business"."security_policies".built_in = 1`,
		`'{"rules":[]}'`,
	} {
		if !strings.Contains(pgSeedBuiltinSecurityPolicyUpsert, must) {
			t.Fatalf("pgSeedBuiltinSecurityPolicyUpsert 缺少契约片段 %q", must)
		}
	}
	// 更新列不得携带时间戳：重复执行对已一致的内置行不推进 created_at/updated_at。
	conflictIndex := strings.Index(pgSeedBuiltinSecurityPolicyUpsert, "ON CONFLICT")
	if conflictIndex < 0 {
		t.Fatal("pgSeedBuiltinSecurityPolicyUpsert 缺少 ON CONFLICT 臂")
	}
	updateSet := pgSeedBuiltinSecurityPolicyUpsert[conflictIndex:]
	if strings.Contains(updateSet, "created_at =") || strings.Contains(updateSet, "updated_at =") {
		t.Fatal("内置安全策略 upsert 不得更新 created_at/updated_at")
	}
}

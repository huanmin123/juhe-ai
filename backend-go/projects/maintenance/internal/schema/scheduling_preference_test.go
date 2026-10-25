// 调度偏好独立实体（PLAN-20261012T033000000Z P1）存储与种子测试：PG seed
// 语句与参数（wmSchemaRecorder 假驱动捕获）。SQLite 表形状/守卫加列/内置行
// 种子臂随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。
package schema

import (
	"context"
	"strings"
	"testing"
	"time"
)

var schedulingPreferenceTestClock = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

// TestSeedPostgresDefaultsSeedsBuiltinSchedulingPreferenceStatement 用假驱动
// 捕获 PG seed 的内置调度偏好 upsert：语句出现、参数为固定 id 与权威 name。
func TestSeedPostgresDefaultsSeedsBuiltinSchedulingPreferenceStatement(t *testing.T) {
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	if _, err := SeedPostgresDefaults(context.Background(), db, SeedOptions{Now: func() time.Time { return schedulingPreferenceTestClock }}); err != nil {
		t.Fatalf("SeedPostgresDefaults: %v", err)
	}
	found := false
	for _, statement := range rec.execs {
		if !strings.Contains(statement.query, `INSERT INTO "juhe_business"."scheduling_preferences"`) {
			continue
		}
		found = true
		if len(statement.args) != 4 {
			t.Fatalf("builtin scheduling preference upsert args = %v, want 4", statement.args)
		}
		if statement.args[0] != BuiltinSchedulingPreferenceID || statement.args[1] != BuiltinSchedulingPreferenceName {
			t.Fatalf("builtin scheduling preference upsert args = %v, want id %q name %q",
				statement.args, BuiltinSchedulingPreferenceID, BuiltinSchedulingPreferenceName)
		}
	}
	if !found {
		t.Fatal("SeedPostgresDefaults must issue the builtin scheduling preference upsert")
	}
}

// 调度偏好独立实体（PLAN-20261012T033000000Z P1）存储与种子测试：
//   - SQLite 表形状（列集 / 默认值 / CHECK 词表 / 四枚索引与 api_keys 绑定
//     索引 / owner 内名称唯一与内置豁免），沿 media_jobs_test.go 模式；
//   - api_keys.scheduling_preference_id 守卫加列（fresh 内联 / legacy 守卫
//     ALTER，两到达路径都带列级 REFERENCES 与绑定索引）；
//   - 内置行种子（fresh seed、ensure 臂补种、重复执行 updated_at 不漂移、
//     内置行被删 / 被改动后重跑恢复、固定 id 被自定义行占用时不触碰）；
//   - PG seed 语句与参数（wmSchemaRecorder 假驱动捕获）。
package schema

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var schedulingPreferenceTestClock = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

func openSchedulingPreferenceSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:scheduling-preference-shape?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := EnsureAllSQLite(context.Background(), db); err != nil {
		t.Fatalf("EnsureAllSQLite: %v", err)
	}
	return db
}

func TestSchedulingPreferencesTableShape(t *testing.T) {
	db := openSchedulingPreferenceSQLite(t)

	// 列集：9 列精确集合。
	required := map[string]bool{
		"id": true, "system_account_id": true, "name": true, "chat_mode": true,
		"chat_speed_first_config_json": true, "built_in": true, "enabled": true,
		"created_at": true, "updated_at": true,
	}
	for column := range required {
		if !sqliteTableHasColumn(t, db, "scheduling_preferences", column) {
			t.Fatalf("scheduling_preferences missing column %s", column)
		}
	}
	var columns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('scheduling_preferences')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != len(required) {
		t.Fatalf("scheduling_preferences columns = %d, want %d", columns, len(required))
	}

	// 合法自定义行：built_in/enabled 默认值落库（0/1）。
	if _, err := db.Exec(`INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, created_at, updated_at)
	      VALUES ('sp_user_1', 'owner_1', '我的偏好', 'speed_first', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert valid custom preference: %v", err)
	}
	var builtIn, enabled int
	var configJSON sql.NullString
	if err := db.QueryRow(`SELECT built_in, enabled, chat_speed_first_config_json
	      FROM scheduling_preferences WHERE id = 'sp_user_1'`).Scan(&builtIn, &enabled, &configJSON); err != nil {
		t.Fatal(err)
	}
	if builtIn != 0 || enabled != 1 || configJSON.Valid {
		t.Fatalf("defaults = built_in %d enabled %d config %v, want 0/1/NULL", builtIn, enabled, configJSON)
	}

	// CHECK 词表：非法 chat_mode / built_in / enabled 拒绝。
	for _, tc := range []struct {
		name string
		dml  string
	}{
		{"invalid-chat-mode", `INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, created_at, updated_at)
		      VALUES ('sp_bad_mode', 'owner_1', '坏形态', 'balanced', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`},
		{"invalid-built-in", `INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, built_in, created_at, updated_at)
		      VALUES ('sp_bad_builtin', 'owner_1', '坏内置位', 'cost_first', 2, '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`},
		{"invalid-enabled", `INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, enabled, created_at, updated_at)
		      VALUES ('sp_bad_enabled', 'owner_1', '坏启用位', 'cost_first', 2, '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`},
	} {
		if _, err := db.Exec(tc.dml); err == nil {
			t.Fatalf("%s must be rejected by CHECK", tc.name)
		}
	}

	// 索引：四枚 scheduling_preferences 索引 + api_keys 绑定列索引。
	for _, indexName := range []string{
		"idx_scheduling_preferences_owner_name_unique",
		"idx_scheduling_preferences_owner_name_unique_lower",
		"idx_scheduling_preferences_builtin_id_unique",
		"idx_scheduling_preferences_owner",
		"idx_api_keys_scheduling_preference",
	} {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("index %s missing", indexName)
		}
	}

	// owner 内名称唯一（精确 + lower 归一），内置行（built_in=1）豁免。
	insert := func(id, owner, name string, builtIn int) error {
		_, err := db.Exec(`INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, built_in, created_at, updated_at)
		      VALUES (?, ?, ?, 'cost_first', ?, '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`, id, nullableOwnerText(owner), name, builtIn)
		return err
	}
	if err := insert("sp_owner2", "owner_2", "同名不同 owner", 0); err != nil {
		t.Fatalf("same name different owner must be allowed: %v", err)
	}
	if err := insert("sp_case_a", "owner_1", "CaseName", 0); err != nil {
		t.Fatalf("insert case name: %v", err)
	}
	if err := insert("sp_case_b", "owner_1", "casename", 0); err == nil {
		t.Fatal("same owner lower-equivalent name must be rejected by unique_lower index")
	}
	if err := insert("sp_builtin_dup_a", NULLOwnerSentinel, "内置同名", 1); err != nil {
		t.Fatalf("builtin row A: %v", err)
	}
	if err := insert("sp_builtin_dup_b", NULLOwnerSentinel, "内置同名", 1); err != nil {
		t.Fatalf("builtin rows are exempt from the built_in=0 owner-name unique: %v", err)
	}
	if err := insert("sp_mixed", "owner_1", "内置同名", 0); err != nil {
		t.Fatalf("custom row may share a name with builtin rows: %v", err)
	}
}

// nullableOwnerText 把空串映射为 SQL NULL（测试夹具用）。
func nullableOwnerText(owner string) any {
	if owner == "" {
		return nil
	}
	return owner
}

// NULLOwnerSentinel 是 nullableOwnerText 的 NULL 哨兵。
const NULLOwnerSentinel = ""

// legacyAPIKeysDDL 是缺 scheduling_preference_id 列的存量 api_keys 形状
// （现建表 DDL 摘除该列；列为守卫链提供 legacy 到达路径）。
const legacyAPIKeysDDL = `CREATE TABLE api_keys (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      route_strategy_id TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      key_hash TEXT NOT NULL UNIQUE,
      key_prefix TEXT NOT NULL,
      key_suffix TEXT NOT NULL,
      key_secret_encrypted TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      is_default INTEGER NOT NULL DEFAULT 0,
      purpose TEXT NOT NULL DEFAULT 'general' CHECK (purpose IN ('general', 'chat')),
      expires_at TEXT,
      quota_limits_json TEXT,
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id)
    )`

func TestEnsureSQLiteBusinessDeliversAPIKeySchedulingPreferenceColumn(t *testing.T) {
	t.Run("fresh database declares the column and index", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-schpref-fresh")
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness: %v", err)
		}
		if !sqliteTableHasColumn(t, db, "api_keys", "scheduling_preference_id") {
			t.Fatal("fresh api_keys lacks scheduling_preference_id")
		}
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='api_keys'`).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "scheduling_preference_id TEXT REFERENCES scheduling_preferences(id)") {
			t.Fatalf("fresh api_keys DDL must declare the FK reference:\n%s", ddl)
		}
		assertSchedulingPreferenceAPIKeyIndex(t, db, true)
	})

	t.Run("legacy database receives the guarded ALTER and index", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-schpref-legacy")
		if _, err := db.Exec(legacyAPIKeysDDL); err != nil {
			t.Fatalf("seed legacy DDL: %v", err)
		}
		if sqliteTableHasColumn(t, db, "api_keys", "scheduling_preference_id") {
			t.Fatal("legacy precondition violated: api_keys already has scheduling_preference_id")
		}
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness over legacy api_keys: %v", err)
		}
		if !sqliteTableHasColumn(t, db, "api_keys", "scheduling_preference_id") {
			t.Fatal("legacy api_keys lacks scheduling_preference_id after ensure")
		}
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='api_keys'`).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "REFERENCES scheduling_preferences(id)") {
			t.Fatalf("legacy guard ALTER must carry the FK reference declaration:\n%s", ddl)
		}
		assertSchedulingPreferenceAPIKeyIndex(t, db, true)
	})
}

func assertSchedulingPreferenceAPIKeyIndex(t *testing.T, db *sql.DB, want bool) {
	t.Helper()
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_api_keys_scheduling_preference'`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if (exists == 1) != want {
		t.Fatalf("idx_api_keys_scheduling_preference presence = %d, want present=%v", exists, want)
	}
}

func readBuiltinSchedulingPreference(t *testing.T, db *sql.DB) (systemAccountID sql.NullString, name, chatMode string, builtIn, enabled int, createdAt, updatedAt string) {
	t.Helper()
	err := db.QueryRow(`SELECT system_account_id, name, chat_mode, built_in, enabled, created_at, updated_at
	      FROM scheduling_preferences WHERE id = ?`, BuiltinSchedulingPreferenceID).
		Scan(&systemAccountID, &name, &chatMode, &builtIn, &enabled, &createdAt, &updatedAt)
	if err != nil {
		t.Fatalf("read builtin scheduling preference: %v", err)
	}
	return
}

func TestSeedSQLiteDefaultsSeedsBuiltinSchedulingPreference(t *testing.T) {
	db := openSharedMemorySQLite(t, "authsys-schema-test-schpref-seed")
	ctx := context.Background()
	if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
		t.Fatalf("EnsureSQLiteBusiness: %v", err)
	}
	options := SeedOptions{Now: func() time.Time { return schedulingPreferenceTestClock }, Secret: "juhe-ai-seed-test-secret"}
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("first SeedSQLiteDefaults: %v", err)
	}

	// 权威值：NULL owner、cost_first、built_in=1、enabled=1、config NULL。
	_, name, chatMode, builtIn, enabled, createdAt, updatedAt := readBuiltinSchedulingPreference(t, db)
	if name != BuiltinSchedulingPreferenceName || chatMode != "cost_first" || builtIn != 1 || enabled != 1 || createdAt != updatedAt {
		t.Fatalf("builtin row = %q %q built_in=%d enabled=%d created=%s updated=%s", name, chatMode, builtIn, enabled, createdAt, updatedAt)
	}

	// 重复执行幂等：updated_at 不漂移；换时钟同样不漂移（恢复式 upsert 的
	// 更新列不含 updated_at，权威字段一致时行值稳定）。
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("second SeedSQLiteDefaults: %v", err)
	}
	_, _, _, _, _, _, updatedAtAfterRerun := readBuiltinSchedulingPreference(t, db)
	if updatedAtAfterRerun != updatedAt {
		t.Fatalf("updated_at drifted on rerun: %s -> %s", updatedAt, updatedAtAfterRerun)
	}
	laterClock := SeedOptions{Now: func() time.Time { return schedulingPreferenceTestClock.Add(time.Hour) }, Secret: "juhe-ai-seed-test-secret"}
	if _, err := SeedSQLiteDefaults(ctx, db, laterClock); err != nil {
		t.Fatalf("third SeedSQLiteDefaults (later clock): %v", err)
	}
	_, _, _, _, _, _, updatedAtAfterLaterClock := readBuiltinSchedulingPreference(t, db)
	if updatedAtAfterLaterClock != updatedAt {
		t.Fatalf("updated_at drifted on later-clock rerun: %s -> %s", updatedAt, updatedAtAfterLaterClock)
	}

	// 内置行被删后重跑恢复。删除前临时关闭外键并随后恢复：ensure 语句流把
	// PRAGMA foreign_keys=ON 留在连接上（sqliteBusinessScript 首语句），而
	// seed 预置的默认 Key 绑定内置固定 ID（接口契约「调度偏好」章五写入口
	// 矩阵），FK 生效时直接删除触发 787 约束——FK 是否留存在连接上取决于
	// 驱动连接复用，必须显式关闭保证确定性。
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM scheduling_preferences WHERE id = ?`, BuiltinSchedulingPreferenceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedSQLiteDefaults(ctx, db, laterClock); err != nil {
		t.Fatalf("SeedSQLiteDefaults after delete: %v", err)
	}
	_, name, chatMode, builtIn, enabled, _, _ = readBuiltinSchedulingPreference(t, db)
	if name != BuiltinSchedulingPreferenceName || chatMode != "cost_first" || builtIn != 1 || enabled != 1 {
		t.Fatalf("restored builtin row = %q %q built_in=%d enabled=%d", name, chatMode, builtIn, enabled)
	}

	// 内置行被外部改动后重跑拉回权威值（恢复式 upsert 的 DO UPDATE 臂）。
	if _, err := db.Exec(`UPDATE scheduling_preferences SET name = '被改动', chat_mode = 'speed_first', enabled = 0 WHERE id = ?`, BuiltinSchedulingPreferenceID); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedSQLiteDefaults(ctx, db, laterClock); err != nil {
		t.Fatalf("SeedSQLiteDefaults after drift: %v", err)
	}
	_, name, chatMode, builtIn, enabled, _, _ = readBuiltinSchedulingPreference(t, db)
	if name != BuiltinSchedulingPreferenceName || chatMode != "cost_first" || builtIn != 1 || enabled != 1 {
		t.Fatalf("drifted builtin row must be restored, got %q %q built_in=%d enabled=%d", name, chatMode, builtIn, enabled)
	}

	// 固定 id 被自定义行（built_in=0）占用时不触碰（WHERE built_in=1 守卫）。
	// 删除前置默认 Key 仍绑定该内置行，与上一处同理临时关闭外键并恢复。
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM scheduling_preferences WHERE id = ?`, BuiltinSchedulingPreferenceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, built_in, enabled, created_at, updated_at)
	      VALUES (?, 'owner_1', '占用者', 'speed_first', 0, 0, '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`, BuiltinSchedulingPreferenceID); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedSQLiteDefaults(ctx, db, laterClock); err != nil {
		t.Fatalf("SeedSQLiteDefaults with custom row occupying the fixed id: %v", err)
	}
	_, name, chatMode, builtIn, enabled, _, _ = readBuiltinSchedulingPreference(t, db)
	if name != "占用者" || chatMode != "speed_first" || builtIn != 0 || enabled != 0 {
		t.Fatalf("custom row occupying the fixed id must stay untouched, got %q %q built_in=%d enabled=%d", name, chatMode, builtIn, enabled)
	}
}

// TestSeedBuiltinSchedulingPreferenceSQLiteEnsureArm 覆盖 --ensure-schema 补
// 种臂（存量库升级 = 跑 ensure-schema 即得内置行）：schema 已 ensure 但从未
// seed 的库补种成功；重复补种 updated_at 不漂移；--seed 链与补种臂同源幂等。
func TestSeedBuiltinSchedulingPreferenceSQLiteEnsureArm(t *testing.T) {
	db := openSharedMemorySQLite(t, "authsys-schema-test-schpref-ensure-arm")
	ctx := context.Background()
	if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
		t.Fatalf("EnsureSQLiteBusiness: %v", err)
	}
	// 仅 ensure、未 seed：内置行不存在（ensure 的 DDL 臂不落数据）。
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scheduling_preferences`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ensure-only database must not carry seed rows, got %d", count)
	}

	if err := SeedBuiltinSchedulingPreferenceSQLite(ctx, db); err != nil {
		t.Fatalf("ensure-arm seeding: %v", err)
	}
	_, _, _, _, _, _, updatedAt := readBuiltinSchedulingPreference(t, db)
	if err := SeedBuiltinSchedulingPreferenceSQLite(ctx, db); err != nil {
		t.Fatalf("ensure-arm reseeding: %v", err)
	}
	_, _, _, _, _, _, updatedAtAfterRerun := readBuiltinSchedulingPreference(t, db)
	if updatedAtAfterRerun != updatedAt {
		t.Fatalf("ensure-arm updated_at drifted: %s -> %s", updatedAt, updatedAtAfterRerun)
	}

	// 补种臂之后跑完整 --seed 链：同源权威值，updated_at 不漂移。
	if _, err := SeedSQLiteDefaults(ctx, db, SeedOptions{Now: func() time.Time { return schedulingPreferenceTestClock }, Secret: "juhe-ai-seed-test-secret"}); err != nil {
		t.Fatalf("full seed after ensure-arm: %v", err)
	}
	_, name, chatMode, builtIn, enabled, _, updatedAtAfterFullSeed := readBuiltinSchedulingPreference(t, db)
	if name != BuiltinSchedulingPreferenceName || chatMode != "cost_first" || builtIn != 1 || enabled != 1 {
		t.Fatalf("builtin row after full seed = %q %q built_in=%d enabled=%d", name, chatMode, builtIn, enabled)
	}
	if updatedAtAfterFullSeed != updatedAt {
		t.Fatalf("full seed drifted the ensure-arm row: %s -> %s", updatedAt, updatedAtAfterFullSeed)
	}
}

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

// 迁移器测试（SQLite 真库，进程内直调 Preflight/Run/Verify；PG 侧行为走
// opt-in live smoke 门禁 TestPostgresSchedulingPreferencesMigrationLiveSmoke，
// 环境变量 JUHE_AI_SCHEDPREF_MIGRATION_PG_SMOKE_URL 指向一次性隔离库时才运
// 行，口径同 capabilitydomain）：
//   - 预检报告：五种模式各自有效调度参数、坏配置清单（JSON 损坏/非法值/越界/
//     未知模式）、显式绑定/NULL 普通/chat/studio 计数、重名冲突预测、Blocking；
//   - 执行：等值偏好生成（键序不同与旧 firstByteThresholdMs 别名均按规范化参
//     数等值迁移）、owner 内重名自动加序号 -2、cost_first/缺省回填内置 ID、
//     显式绑定与 chat / studio Key 一律不动（studio NULL Key 与 chat 同规，
//     2026-10-13 主代理裁决 B1 修复）、route_strategies.config_json 逐字节不
//     变（回滚 oracle）、按 owner 分批短事务、完成记录计数；
//   - 断点恢复：running 记录续跑、确定性 ID 复用不重复建行、名称占用联动加
//     序号；复用行配置不等值 fail-closed；
//   - 坏数据阻断：零写入、无完成记录，修复后同命令重跑收敛；
//   - 后检拒绝：NULL 残留/悬空绑定/计数不一致拒绝写完成记录；
//   - 记录防护：损坏 JSON、ruleVersion 不符、非法 stage 拒绝执行；
//   - verify：记录缺失/未完成/残留/悬空各失败臂与完成态放行。

package schedprefmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
	_ "modernc.org/sqlite"
)

const fixtureTimestamp = "2026-10-12T08:00:00.000Z"

var fixtureClock = time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

// openMigrationDB 打开一个完成 ensure 的临时业务库（单连接，进程内）。
func openMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatalf("ensure business schema: %v", err)
	}
	// ensure 只建表不种子：迁移前置条件（内置行）按契约权威值补种（enabled
	// 已随 v2 用户裁决修订第 4 条退役，行不再携带该列）。
	if _, err := db.Exec(`INSERT INTO scheduling_preferences
		(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
		VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', NULL, 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed builtin scheduling preference: %v", err)
	}
	return db
}

// seedStrategyFixture 插入一条策略行（configJSON 为空串时落 NULL）。
func seedStrategyFixture(t *testing.T, db *sql.DB, id, owner, name, mode, status, configJSON string) {
	t.Helper()
	var config any
	if configJSON != "" {
		config = configJSON
	}
	if _, err := db.Exec(`INSERT INTO route_strategies
		(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, ?, 0, ?, ?, ?)`,
		id, owner, name, mode, status, config, fixtureTimestamp, fixtureTimestamp); err != nil {
		t.Fatalf("seed strategy %s: %v", id, err)
	}
}

// seedPreferenceFixture 插入一条调度偏好行（configJSON 为空串时落 NULL；
// builtIn=0 自定义行；内置行走 ensure 链不在此造数）。
func seedPreferenceFixture(t *testing.T, db *sql.DB, id, owner, name, chatMode, configJSON string) {
	t.Helper()
	var config any
	if configJSON != "" {
		config = configJSON
	}
	if _, err := db.Exec(`INSERT INTO scheduling_preferences
		(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		id, owner, name, chatMode, config, fixtureTimestamp, fixtureTimestamp); err != nil {
		t.Fatalf("seed preference %s: %v", id, err)
	}
}

// seedKeyFixture 插入一条 API Key（bound 为空串时绑定列落 NULL）。要求外键
// 指向的行已存在。
func seedKeyFixture(t *testing.T, db *sql.DB, id, owner, strategyID, purpose, bound string) {
	t.Helper()
	var boundAny any
	if bound != "" {
		boundAny = bound
	}
	if _, err := db.Exec(`INSERT INTO api_keys
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULL, ?, 'sk-prefix', 'suf', 'enc', 'active', 0, ?, ?, ?, ?)`,
		id, owner, strategyID, id, "hash-"+id, purpose, boundAny, fixtureTimestamp, fixtureTimestamp); err != nil {
		t.Fatalf("seed api key %s: %v", id, err)
	}
}

// toggleForeignKeys 切换 SQLite 外键（夹具造数悬空引用时关，迁移运行时开，
// 顺带验证「偏好先行、回填随后」的 owner 短事务顺序满足列级 REFERENCES）。
func toggleForeignKeys(t *testing.T, db *sql.DB, on bool) {
	t.Helper()
	value := "OFF"
	if on {
		value = "ON"
	}
	if _, err := db.Exec("PRAGMA foreign_keys=" + value); err != nil {
		t.Fatalf("toggle foreign_keys: %v", err)
	}
}

func migrationOptions() Options {
	return Options{Dialect: capabilitydomain.DialectSQLite, Now: fixtureClock, BatchSize: 2}
}

// storedPreference 读取一条偏好行（无行返回 ok=false）。
func storedPreference(t *testing.T, db *sql.DB, id string) (name, chatMode string, configJSON sql.NullString, ok bool) {
	t.Helper()
	err := db.QueryRow(`SELECT name, chat_mode, chat_speed_first_config_json FROM scheduling_preferences WHERE id = ?`, id).
		Scan(&name, &chatMode, &configJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", sql.NullString{}, false
	}
	if err != nil {
		t.Fatalf("read preference %s: %v", id, err)
	}
	return name, chatMode, configJSON, true
}

// storedKeyBinding 读取一条 Key 的绑定列。
func storedKeyBinding(t *testing.T, db *sql.DB, id string) sql.NullString {
	t.Helper()
	var bound sql.NullString
	if err := db.QueryRow(`SELECT scheduling_preference_id FROM api_keys WHERE id = ?`, id).Scan(&bound); err != nil {
		t.Fatalf("read key binding %s: %v", id, err)
	}
	return bound
}

// storedStrategyConfig 读取一条策略的 config_json（回滚 oracle 比对用）。
func storedStrategyConfig(t *testing.T, db *sql.DB, id string) sql.NullString {
	t.Helper()
	var config sql.NullString
	if err := db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, id).Scan(&config); err != nil {
		t.Fatalf("read strategy config %s: %v", id, err)
	}
	return config
}

// storedMigrationRecord 读取迁移记录（无记录返回 nil）。
func storedMigrationRecord(t *testing.T, db *sql.DB) *schedprefdomain.Record {
	t.Helper()
	record, err := readRecord(context.Background(), db, capabilitydomain.DialectSQLite)
	if err != nil {
		t.Fatalf("read migration record: %v", err)
	}
	return record
}

// countRows 快速计数。
func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return count
}

// databaseSnapshot 取与迁移相关的行数快照（失败臂「零写入」断言用）。
func databaseSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	return fmt.Sprintf("prefs=%d keys=%d bound=%d settings=%d",
		countRows(t, db, `SELECT COUNT(*) FROM scheduling_preferences WHERE built_in = 0`),
		countRows(t, db, `SELECT COUNT(*) FROM api_keys`),
		countRows(t, db, `SELECT COUNT(*) FROM api_keys WHERE scheduling_preference_id IS NOT NULL`),
		countRows(t, db, `SELECT COUNT(*) FROM global_settings`))
}

// speedConfigJSON 渲染偏好存储形状的期望 JSON（marshalSpeedConfig 键序稳定）。
func speedConfigJSON(t *testing.T, deadlineMS int, slowTriggerCount int) string {
	t.Helper()
	normal, err := parseStrategyConfigJSON(sql.NullString{String: fmt.Sprintf(
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":%d,"speedFirstConfig":{"slowTriggerCount":%d}}}`,
		deadlineMS, slowTriggerCount), Valid: true})
	if err != nil {
		t.Fatalf("build speed config: %v", err)
	}
	return string(marshalSpeedConfig(normal))
}

// mustRun 执行迁移并要求就绪。
func mustRun(t *testing.T, db *sql.DB) Report {
	t.Helper()
	report, err := Run(context.Background(), db, migrationOptions())
	if err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("迁移必须就绪，failures=%+v", report.Failures)
	}
	return report
}

// seedEndToEndFixtures 造一个双 owner 全路径数据集（策略名受 owner 内唯一约
// 束，重名冲突经既有偏好行占用派生名制造）：
//   - sys-a：speed_first×2（其一派生名被占用须加序号、其一旧字段别名）、显式
//     cost_first、缺省；Key 覆盖 NULL/显式绑定/chat；
//   - sys-b：单个 speed_first + NULL Key（跨 owner 分批）。
func seedEndToEndFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	toggleForeignKeys(t, db, false)
	// sys-a
	seedStrategyFixture(t, db, "st-speed-1", "sys-a", "策略甲", "normal", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000,"speedFirstConfig":{"slowTriggerCount":4}}}`)
	seedStrategyFixture(t, db, "st-speed-2", "sys-a", "策略乙", "normal", "active",
		`{"normalRoutingConfig":{"speedFirstConfig":{"firstByteThresholdMs":45000},"schedulingPreference":"speed_first"}}`)
	seedStrategyFixture(t, db, "st-cost", "sys-a", "策略丙", "normal", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"cost_first"}}`)
	seedStrategyFixture(t, db, "st-default", "sys-a", "策略丁", "weighted", "active", "")
	seedPreferenceFixture(t, db, "pref-p0", "sys-a", "已有偏好", "cost_first", "")
	seedPreferenceFixture(t, db, "pref-occupy", "sys-a", "策略甲·速度优先", "speed_first", speedConfigJSON(t, 30000, 3))
	seedKeyFixture(t, db, "k1", "sys-a", "st-speed-1", "general", "")
	seedKeyFixture(t, db, "k2", "sys-a", "st-speed-2", "general", "")
	seedKeyFixture(t, db, "k3", "sys-a", "st-cost", "general", "")
	seedKeyFixture(t, db, "k4", "sys-a", "st-default", "general", "")
	seedKeyFixture(t, db, "k5", "sys-a", "st-speed-1", "general", "pref-p0")
	seedKeyFixture(t, db, "k6", "sys-a", "st-speed-1", "chat", "")
	// studio NULL Key（EnsureStudioKey 形态：绑定列 NULL）：与 chat 同规排除。
	seedKeyFixture(t, db, "k-studio", "sys-a", "st-cost", "studio", "")
	// sys-b
	seedStrategyFixture(t, db, "st-b-speed", "sys-b", "策略B", "failover", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first"}}`)
	seedKeyFixture(t, db, "kb", "sys-b", "st-b-speed", "general", "")
	toggleForeignKeys(t, db, true)
}

func TestRunEndToEnd(t *testing.T) {
	db := openMigrationDB(t)
	seedEndToEndFixtures(t, db)
	beforeConfigs := map[string]sql.NullString{
		"st-speed-1": storedStrategyConfig(t, db, "st-speed-1"),
		"st-speed-2": storedStrategyConfig(t, db, "st-speed-2"),
		"st-cost":    storedStrategyConfig(t, db, "st-cost"),
		"st-default": storedStrategyConfig(t, db, "st-default"),
	}

	report := mustRun(t, db)

	// 报告计数：速度优先策略 3、新建偏好 3、NULL 普通 Key 5 全部回填、复用 0。
	if report.StrategiesSpeedFirst != 3 || report.PreferencesCreated != 3 || report.PreferencesReused != 0 {
		t.Fatalf("报告计数异常: %+v", report)
	}
	if report.APIKeysNullGeneral != 5 || report.APIKeysBackfilled != 5 {
		t.Fatalf("回填计数异常: nullGeneral=%d backfilled=%d", report.APIKeysNullGeneral, report.APIKeysBackfilled)
	}
	if report.APIKeysChat != 1 || report.APIKeysStudioExcluded != 1 || report.APIKeysExplicitBound != 1 {
		t.Fatalf("chat/studio/显式绑定计数异常: chat=%d studio=%d explicit=%d",
			report.APIKeysChat, report.APIKeysStudioExcluded, report.APIKeysExplicitBound)
	}

	// 等值偏好：派生名被既有偏好行占用时自动加序号 -2（pref-occupy 占用
	// 「策略甲·速度优先」，st-speed-1 让序号；st-speed-2 得基础名「策略乙·
	// 速度优先」）。
	name1, chatMode1, config1, ok := storedPreference(t, db, "schpref-mig-st-speed-1")
	if !ok {
		t.Fatalf("st-speed-1 等值偏好缺失")
	}
	if chatMode1 != "speed_first" {
		t.Fatalf("偏好 chat_mode = %q, want speed_first", chatMode1)
	}
	wantConfig1 := speedConfigJSON(t, 20000, 4)
	if config1.String != wantConfig1 {
		t.Fatalf("st-speed-1 偏好配置不等值:\ngot  %s\nwant %s", config1.String, wantConfig1)
	}
	name2, _, config2, ok := storedPreference(t, db, "schpref-mig-st-speed-2")
	if !ok {
		t.Fatalf("st-speed-2 等值偏好缺失")
	}
	// 旧 firstByteThresholdMs 别名 → firstByteDeadlineMs=45000，其余字段缺省
	// 回填；规范化等值而非字节等值（策略 config_json 键序不同）。
	wantConfig2 := speedConfigJSON(t, 45000, 3)
	if config2.String != wantConfig2 {
		t.Fatalf("st-speed-2 偏好配置不等值:\ngot  %s\nwant %s", config2.String, wantConfig2)
	}
	if name1 != "策略甲·速度优先-2" || name2 != "策略乙·速度优先" {
		t.Fatalf("重名加序号异常: %q / %q", name1, name2)
	}
	nameB, _, _, ok := storedPreference(t, db, "schpref-mig-st-b-speed")
	if !ok || nameB != "策略B·速度优先" {
		t.Fatalf("sys-b 偏好异常: %q ok=%v", nameB, ok)
	}

	// 回填目标：speed_first → 确定性 ID；cost_first/缺省 → 内置 ID；显式与
	// chat / studio Key 一律不动（studio NULL Key 与 chat 同规：绑定列保持
	// NULL，B1 修复——不进回填、不阻断完成记录）。
	for keyID, want := range map[string]string{
		"k1": "schpref-mig-st-speed-1", "k2": "schpref-mig-st-speed-2",
		"k3": builtinID, "k4": builtinID, "kb": "schpref-mig-st-b-speed",
	} {
		if got := storedKeyBinding(t, db, keyID); !got.Valid || got.String != want {
			t.Fatalf("key %s binding = %v, want %s", keyID, got, want)
		}
	}
	if got := storedKeyBinding(t, db, "k5"); got.Valid && got.String != "pref-p0" {
		t.Fatalf("显式绑定 Key 被改动: %v", got)
	}
	if got := storedKeyBinding(t, db, "k6"); got.Valid {
		t.Fatalf("chat Key 被改动: %v", got)
	}
	if got := storedKeyBinding(t, db, "k-studio"); got.Valid {
		t.Fatalf("studio Key 被改动: %v", got)
	}

	// 回滚 oracle：route_strategies.config_json 逐字节不变。
	for id, before := range beforeConfigs {
		if after := storedStrategyConfig(t, db, id); after != before {
			t.Fatalf("策略 %s config_json 被改动:\nbefore %v\nafter  %v", id, before, after)
		}
	}

	// 完成记录：stage/ruleVersion/counts。
	record := storedMigrationRecord(t, db)
	if record == nil || record.Stage != schedprefdomain.StageCompleted || record.RuleVersion != schedprefdomain.RuleVersion {
		t.Fatalf("完成记录异常: %+v", record)
	}
	if record.Counts.StrategiesSpeedFirstMigrated != 3 || record.Counts.PreferencesCreated != 3 ||
		record.Counts.APIKeysNullGeneral != 5 || record.Counts.APIKeysBackfilled != 5 {
		t.Fatalf("完成记录计数异常: %+v", record.Counts)
	}
	if _, err := time.Parse(time.RFC3339, record.CompletedAt); err != nil {
		t.Fatalf("completedAt 不是 RFC3339: %v", err)
	}

	// verify 放行。
	verify, err := Verify(context.Background(), db, migrationOptions())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verify.Ready() || verify.APIKeysNullGeneralResidual != 0 || verify.DanglingBindingCount != 0 {
		t.Fatalf("完成态 verify 必须放行: %+v", verify)
	}

	// 重复执行只报告完成，零写入。
	snapshot := databaseSnapshot(t, db)
	rerun, err := Run(context.Background(), db, migrationOptions())
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !rerun.AlreadyComplete || rerun.CompletedStage != "completed" {
		t.Fatalf("重复执行必须只报告完成: %+v", rerun)
	}
	if databaseSnapshot(t, db) != snapshot {
		t.Fatalf("重复执行不得改写数据库")
	}
}

func TestPreflightReport(t *testing.T) {
	db := openMigrationDB(t)
	toggleForeignKeys(t, db, false)
	// 五种模式 × 有效参数 + 坏数据 + 停用策略。
	seedStrategyFixture(t, db, "st-normal-speed", "sys-a", "策略甲", "normal", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000,"speedFirstConfig":{"slowTriggerCount":4}}}`)
	seedStrategyFixture(t, db, "st-weighted-cost", "sys-a", "策略乙", "weighted", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"cost_first"}}`)
	seedStrategyFixture(t, db, "st-failover-default", "sys-a", "策略丙", "failover", "active", "")
	seedStrategyFixture(t, db, "st-rr-speed", "sys-a", "策略己", "round_robin", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first"}}`)
	seedStrategyFixture(t, db, "st-merge-default", "sys-a", "策略庚", "merge", "active", `{}`)
	seedStrategyFixture(t, db, "st-bad-json", "sys-a", "坏JSON", "normal", "active", `{"normalRoutingConfig":`)
	seedStrategyFixture(t, db, "st-bad-value", "sys-a", "坏值", "normal", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"turbo"}}`)
	seedStrategyFixture(t, db, "st-bad-range", "sys-a", "越界", "normal", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":5000}}`)
	seedStrategyFixture(t, db, "st-unknown-mode", "sys-a", "未知模式", "experimental", "active", "")
	seedStrategyFixture(t, db, "st-disabled-speed", "sys-a", "停用", "normal", "disabled",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first"}}`)
	seedPreferenceFixture(t, db, "pref-p0", "sys-a", "已有偏好", "cost_first", "")
	seedPreferenceFixture(t, db, "pref-occupy", "sys-a", "策略甲·速度优先", "speed_first", speedConfigJSON(t, 30000, 3))
	seedKeyFixture(t, db, "k-a1", "sys-a", "st-normal-speed", "general", "")
	seedKeyFixture(t, db, "k-explicit", "sys-a", "st-normal-speed", "general", "pref-p0")
	seedKeyFixture(t, db, "k-chat", "sys-a", "st-normal-speed", "chat", "")
	seedKeyFixture(t, db, "k-studio", "sys-a", "st-normal-speed", "studio", "")
	seedKeyFixture(t, db, "k-cost", "sys-a", "st-failover-default", "general", "")
	seedKeyFixture(t, db, "k-bad", "sys-a", "st-bad-json", "general", "")
	toggleForeignKeys(t, db, true)

	report, err := Preflight(context.Background(), db, migrationOptions())
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}

	if report.OwnersTotal != 1 || report.StrategiesTotal != 10 || report.PreferencesExisting != 2 {
		t.Fatalf("总量计数异常: owners=%d strategies=%d prefs=%d", report.OwnersTotal, report.StrategiesTotal, report.PreferencesExisting)
	}
	// 五种模式各自有效调度参数（normal 含坏配置 3 行与停用速度 1 行）。
	expectModes := map[string]ModeReport{
		"normal":      {Total: 5, SpeedFirst: 2, BadConfig: 3},
		"weighted":    {Total: 1, CostFirstExplicit: 1},
		"failover":    {Total: 1, CostFirstDefault: 1},
		"round_robin": {Total: 1, SpeedFirst: 1},
		"merge":       {Total: 1, CostFirstDefault: 1},
	}
	for mode, want := range expectModes {
		if got := report.Modes[mode]; got != want {
			t.Fatalf("模式 %s 报告 = %+v, want %+v", mode, got, want)
		}
	}
	if got := report.Modes["experimental"]; got.Total != 1 || got.BadConfig != 1 {
		t.Fatalf("未知模式必须计入坏数据: %+v", got)
	}

	// Key 计数：显式 1、NULL 普通 3、chat 1、studio 1（studio 不计入普通 Key，
	// B1 修复后 apiKeysGeneral 只含 purpose=general）。
	if report.APIKeysTotal != 6 || report.APIKeysGeneral != 4 || report.APIKeysChat != 1 ||
		report.APIKeysStudioExcluded != 1 ||
		report.APIKeysExplicitBound != 1 || report.APIKeysNullGeneral != 3 {
		t.Fatalf("Key 计数异常: %+v", report)
	}

	// 计划：三条等值偏好（st-normal-speed 派生名被占用加序号；st-rr-speed 与
	// 停用的 st-disabled-speed 各得基础名）、内置回填 2、重名预测 1。
	if len(report.PlannedPreferences) != 3 {
		t.Fatalf("计划偏好数 = %d, want 3: %+v", len(report.PlannedPreferences), report.PlannedPreferences)
	}
	if report.PlannedBuiltinBindings != 2 {
		t.Fatalf("内置回填计划 = %d, want 2", report.PlannedBuiltinBindings)
	}
	if len(report.NameConflictPredictions) != 1 || report.NameConflictPredictions[0].StrategyID != "st-normal-speed" ||
		report.NameConflictPredictions[0].FinalName != "策略甲·速度优先-2" {
		t.Fatalf("重名预测异常: %+v", report.NameConflictPredictions)
	}

	// 坏配置清单：JSON 损坏/非法值/越界/未知模式 = 4，全部阻断。
	if report.Blocking != 4 || len(report.BadConfigs) != 4 {
		t.Fatalf("坏配置清单异常: blocking=%d badConfigs=%+v", report.Blocking, report.BadConfigs)
	}
	kinds := map[string]int{}
	for _, bad := range report.BadConfigs {
		kinds[bad.Kind]++
	}
	if kinds[FailureStrategyConfigInvalid] != 3 || kinds[FailureStrategyModeInvalid] != 1 {
		t.Fatalf("坏配置分类异常: %+v", kinds)
	}
	if report.SpeedRuntimeNote == "" {
		t.Fatalf("报告必须携带速度运行态处置说明（裁决①）")
	}

	// 预检全程零写入。
	snapshot := databaseSnapshot(t, db)
	if _, err := Preflight(context.Background(), db, migrationOptions()); err != nil {
		t.Fatalf("preflight rerun: %v", err)
	}
	if databaseSnapshot(t, db) != snapshot {
		t.Fatalf("预检不得写入")
	}
}

func TestRunResumeFromRunningRecord(t *testing.T) {
	db := openMigrationDB(t)
	seedEndToEndFixtures(t, db)

	// 模拟上次中断：已领取 running 记录 + st-speed-2 的等值偏好已落库（其
	// 名称「策略乙·速度优先」被占用），Key 尚未回填。
	claimTx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if acquired, err := schedprefdomain.ClaimRunningStage(context.Background(), claimTx, capabilitydomain.DialectSQLite, fixtureClock); err != nil || !acquired {
		t.Fatalf("claim running: acquired=%v err=%v", acquired, err)
	}
	if err := claimTx.Commit(); err != nil {
		t.Fatal(err)
	}
	storedConfig := speedConfigJSON(t, 45000, 3)
	seedPreferenceFixture(t, db, "schpref-mig-st-speed-2", "sys-a", "策略乙·速度优先", "speed_first", storedConfig)

	report := mustRun(t, db)
	if report.PreferencesCreated != 2 || report.PreferencesReused != 1 {
		t.Fatalf("续跑计数异常: created=%d reused=%d", report.PreferencesCreated, report.PreferencesReused)
	}
	// 复用行不被改写（名称与配置保持原值）。
	name, chatMode, configJSON, _ := storedPreference(t, db, "schpref-mig-st-speed-2")
	if name != "策略乙·速度优先" || chatMode != "speed_first" || configJSON.String != storedConfig {
		t.Fatalf("复用行被改写: %q %q %s", name, chatMode, configJSON.String)
	}
	// st-speed-1 本轮新建；其基础名「策略甲·速度优先」被 pref-occupy 占用 → -2。
	if name1, _, _, _ := storedPreference(t, db, "schpref-mig-st-speed-1"); name1 != "策略甲·速度优先-2" {
		t.Fatalf("续跑重名加序号异常: %q", name1)
	}
	if got := storedKeyBinding(t, db, "k2"); !got.Valid || got.String != "schpref-mig-st-speed-2" {
		t.Fatalf("续跑回填缺失: %v", got)
	}
	record := storedMigrationRecord(t, db)
	if record == nil || record.Stage != schedprefdomain.StageCompleted || record.Counts.PreferencesCreated != 2 {
		t.Fatalf("续跑完成记录异常: %+v", record)
	}
}

func TestRunReuseMismatchFailsClosed(t *testing.T) {
	db := openMigrationDB(t)
	seedEndToEndFixtures(t, db)
	// 确定性 ID 已被不等值配置占用（停写窗口被改写）：拒绝复用也拒绝覆盖。
	seedPreferenceFixture(t, db, "schpref-mig-st-speed-1", "sys-a", "手工行", "speed_first",
		speedConfigJSON(t, 59000, 3))

	report, err := Run(context.Background(), db, migrationOptions())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Ready() {
		t.Fatalf("复用不等值必须失败")
	}
	found := false
	for _, failure := range report.Failures {
		if failure.Kind == FailurePreferenceMismatch && failure.StrategyID == "st-speed-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("必须报告 preference_mismatch: %+v", report.Failures)
	}
	// 零写入：Key 未回填、无完成记录、占用行未改。
	if got := storedKeyBinding(t, db, "k1"); got.Valid {
		t.Fatalf("失败路径不得回填: %v", got)
	}
	if record := storedMigrationRecord(t, db); record != nil {
		t.Fatalf("失败路径不得写完成记录: %+v", record)
	}
	if name, _, configJSON, _ := storedPreference(t, db, "schpref-mig-st-speed-1"); name != "手工行" || configJSON.String != speedConfigJSON(t, 59000, 3) {
		t.Fatalf("占用行被改写: %q %s", name, configJSON.String)
	}
}

func TestRunBadDataBlocksThenRepairResume(t *testing.T) {
	db := openMigrationDB(t)
	toggleForeignKeys(t, db, false)
	seedStrategyFixture(t, db, "st-bad", "sys-a", "坏配置", "normal", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":5000}}`)
	seedStrategyFixture(t, db, "st-speed", "sys-a", "策略甲", "normal", "active",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first"}}`)
	seedKeyFixture(t, db, "k-bad", "sys-a", "st-bad", "general", "")
	seedKeyFixture(t, db, "k-speed", "sys-a", "st-speed", "general", "")
	// 悬空策略引用：无法定位回填目标。
	seedKeyFixture(t, db, "k-dangling", "sys-a", "st-missing", "general", "")
	toggleForeignKeys(t, db, true)

	snapshot := databaseSnapshot(t, db)
	report, err := Run(context.Background(), db, migrationOptions())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Ready() {
		t.Fatalf("坏数据必须阻断")
	}
	kinds := map[string]int{}
	for _, failure := range report.Failures {
		kinds[failure.Kind]++
	}
	if kinds[FailureStrategyConfigInvalid] != 1 || kinds[FailureKeyStrategyMissing] != 1 {
		t.Fatalf("失败清单异常: %+v", report.Failures)
	}
	if databaseSnapshot(t, db) != snapshot {
		t.Fatalf("坏数据阻断必须零写入")
	}

	// 修复后同命令重跑收敛（断点恢复纪律）。
	if _, err := db.Exec(`UPDATE route_strategies SET config_json = '{"normalRoutingConfig":{"schedulingPreference":"cost_first"}}' WHERE id = 'st-bad'`); err != nil {
		t.Fatal(err)
	}
	toggleForeignKeys(t, db, false)
	if _, err := db.Exec(`DELETE FROM api_keys WHERE id = 'k-dangling'`); err != nil {
		t.Fatal(err)
	}
	toggleForeignKeys(t, db, true)
	fixed := mustRun(t, db)
	// 修复后：st-bad（cost_first）→ 内置 ID、st-speed → 等值偏好，两条 NULL 全部回填。
	if fixed.StrategiesSpeedFirst != 1 || fixed.APIKeysBackfilled != 2 {
		t.Fatalf("修复后迁移计数异常: %+v", fixed)
	}
}

func TestRunRejectsCorruptOrForeignRecord(t *testing.T) {
	scenarios := map[string]string{
		"corrupt-json":    `{"stage":`,
		"unknown-stage":   `{"stage":"paused","ruleVersion":1,"completedAt":"2026-10-12T08:00:00Z","counts":{}}`,
		"rule-version":    `{"stage":"running","ruleVersion":2,"completedAt":"2026-10-12T08:00:00Z","counts":{}}`,
		"negative-counts": `{"stage":"running","ruleVersion":1,"completedAt":"2026-10-12T08:00:00Z","counts":{"strategiesSpeedFirstMigrated":-1}}`,
		"bad-timestamp":   `{"stage":"running","ruleVersion":1,"completedAt":"yesterday","counts":{}}`,
	}
	for name, recordJSON := range scenarios {
		t.Run(name, func(t *testing.T) {
			db := openMigrationDB(t)
			seedEndToEndFixtures(t, db)
			if _, err := db.Exec(`INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, ?)`,
				schedprefdomain.MigrationSettingKey, recordJSON, fixtureTimestamp); err != nil {
				t.Fatal(err)
			}
			snapshot := databaseSnapshot(t, db)
			if _, err := Run(context.Background(), db, migrationOptions()); err == nil {
				t.Fatalf("记录 %s 必须拒绝执行", name)
			}
			if _, err := Verify(context.Background(), db, migrationOptions()); err == nil {
				t.Fatalf("verify 对记录 %s 必须报错", name)
			}
			if databaseSnapshot(t, db) != snapshot {
				t.Fatalf("记录拒绝路径不得写入")
			}
		})
	}
}

func TestPostcheckRejections(t *testing.T) {
	db := openMigrationDB(t)
	toggleForeignKeys(t, db, false)
	seedStrategyFixture(t, db, "st-1", "sys-a", "策略甲", "normal", "active", "")
	seedKeyFixture(t, db, "k-residual", "sys-a", "st-1", "general", "")
	toggleForeignKeys(t, db, true)

	// NULL 残留：计划以为 0、实际库里有 1。
	failures, err := postcheck(context.Background(), db, capabilitydomain.DialectSQLite, &planOutput{}, 0)
	if err != nil {
		t.Fatalf("postcheck: %v", err)
	}
	if len(failures) == 0 || failures[0].Kind != FailureResidualNullKeys {
		t.Fatalf("NULL 残留必须拒绝: %+v", failures)
	}

	// 计数不一致：计划 1、实际回填 0（残留同现）。
	failures, err = postcheck(context.Background(), db, capabilitydomain.DialectSQLite, &planOutput{nullGeneral: 1}, 0)
	if err != nil {
		t.Fatalf("postcheck: %v", err)
	}
	foundResidual, foundCount := false, false
	for _, failure := range failures {
		switch failure.Kind {
		case FailureResidualNullKeys:
			foundResidual = true
		case "backfill_count_mismatch":
			foundCount = true
		}
	}
	if !foundResidual || !foundCount {
		t.Fatalf("残留与计数不一致必须同时报告: %+v", failures)
	}

	// 悬空绑定：指向不存在的偏好行。
	toggleForeignKeys(t, db, false)
	if _, err := db.Exec(`UPDATE api_keys SET scheduling_preference_id = 'schpref-ghost' WHERE id = 'k-residual'`); err != nil {
		t.Fatal(err)
	}
	toggleForeignKeys(t, db, true)
	failures, err = postcheck(context.Background(), db, capabilitydomain.DialectSQLite, &planOutput{nullGeneral: 0}, 0)
	if err != nil {
		t.Fatalf("postcheck: %v", err)
	}
	foundDangling := false
	for _, failure := range failures {
		if failure.Kind == FailureDanglingBinding {
			foundDangling = true
		}
	}
	if !foundDangling {
		t.Fatalf("悬空绑定必须拒绝: %+v", failures)
	}

	// 干净库零失败。
	toggleForeignKeys(t, db, false)
	if _, err := db.Exec(`DELETE FROM api_keys`); err != nil {
		t.Fatal(err)
	}
	toggleForeignKeys(t, db, true)
	failures, err = postcheck(context.Background(), db, capabilitydomain.DialectSQLite, &planOutput{}, 0)
	if err != nil {
		t.Fatalf("postcheck: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("干净库必须零失败: %+v", failures)
	}
}

func TestVerifyFailureArms(t *testing.T) {
	t.Run("record-missing", func(t *testing.T) {
		db := openMigrationDB(t)
		report, err := Verify(context.Background(), db, migrationOptions())
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.Ready() || report.RecordPresent || len(report.Failures) != 1 || report.Failures[0].Kind != FailureRecordMissing {
			t.Fatalf("记录缺失必须报告: %+v", report)
		}
		if !strings.Contains(report.Failures[0].Detail, "--migrate-scheduling-preferences") {
			t.Fatalf("失败详情必须携带迁移命令指引: %q", report.Failures[0].Detail)
		}
	})
	t.Run("record-running", func(t *testing.T) {
		db := openMigrationDB(t)
		claimTx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := schedprefdomain.ClaimRunningStage(context.Background(), claimTx, capabilitydomain.DialectSQLite, fixtureClock); err != nil {
			t.Fatal(err)
		}
		if err := claimTx.Commit(); err != nil {
			t.Fatal(err)
		}
		report, err := Verify(context.Background(), db, migrationOptions())
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.Ready() || report.Failures[0].Kind != FailureRecordNotCompleted {
			t.Fatalf("running 记录必须报未完成: %+v", report)
		}
	})
	t.Run("residual-and-dangling", func(t *testing.T) {
		db := openMigrationDB(t)
		if _, err := db.Exec(`INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, ?)`,
			schedprefdomain.MigrationSettingKey,
			`{"stage":"completed","ruleVersion":1,"completedAt":"2026-10-12T08:00:00Z","counts":{}}`,
			fixtureTimestamp); err != nil {
			t.Fatal(err)
		}
		toggleForeignKeys(t, db, false)
		seedStrategyFixture(t, db, "st-1", "sys-a", "策略甲", "normal", "active", "")
		seedKeyFixture(t, db, "k-null", "sys-a", "st-1", "general", "")
		seedKeyFixture(t, db, "k-ghost", "sys-a", "st-1", "general", "schpref-ghost")
		toggleForeignKeys(t, db, true)
		report, err := Verify(context.Background(), db, migrationOptions())
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.Ready() || report.APIKeysNullGeneralResidual != 1 || report.DanglingBindingCount != 1 {
			t.Fatalf("残留与悬空必须报告: %+v", report)
		}
		kinds := map[string]int{}
		for _, failure := range report.Failures {
			kinds[failure.Kind]++
		}
		if kinds[FailureResidualNullKeys] != 1 || kinds[FailureDanglingBinding] != 1 {
			t.Fatalf("verify 失败分类异常: %+v", report.Failures)
		}
	})
}

func TestBuildPlanNameSuffixExhaustion(t *testing.T) {
	// owner 内名称全部被占用（含 -2..-N 加序号空间）：fail-closed 报失败而
	// 不是死循环或覆盖。
	prefs := make([]scannedPref, 0, nameSuffixProbeUpper+1)
	prefs = append(prefs, scannedPref{id: "p-base", owner: "sys-a", name: "策略甲·速度优先", lowerName: "策略甲·速度优先"})
	for suffix := 2; suffix < nameSuffixProbeUpper+2; suffix++ {
		name := fmt.Sprintf("策略甲·速度优先-%d", suffix)
		prefs = append(prefs, scannedPref{id: fmt.Sprintf("p-%d", suffix), owner: "sys-a", name: name, lowerName: name})
	}
	plan := buildPlan(planInput{
		strategies: []scannedStrategy{{
			id: "st-1", owner: "sys-a", name: "策略甲", mode: "normal", status: "active",
			config:    &NormalRoutingConfig{SchedulingPreference: "speed_first"},
			hasConfig: true,
		}},
		prefs: prefs,
	})
	if len(plan.failures) != 1 || plan.failures[0].Kind != FailurePreferenceMismatch {
		t.Fatalf("加序号耗尽必须失败: %+v", plan.failures)
	}
	if len(plan.perOwner["sys-a"].preferences) != 0 {
		t.Fatalf("耗尽路径不得计划写入")
	}
}

// TestPostgresSchedulingPreferencesMigrationLiveSmoke 是 opt-in 真库测试：设
// JUHE_AI_SCHEDPREF_MIGRATION_PG_SMOKE_URL 指向一次性隔离库时运行真实 PG 方
// 言全流程（EnsurePostgres 建表 → 内置行 → 夹具 → Preflight/Run/Verify/重
// 跑），plain `go test` 跳过（口径同 capabilitydomain live smoke）。
func TestPostgresSchedulingPreferencesMigrationLiveSmoke(t *testing.T) {
	databaseURL := os.Getenv("JUHE_AI_SCHEDPREF_MIGRATION_PG_SMOKE_URL")
	if databaseURL == "" {
		t.Skip("JUHE_AI_SCHEDPREF_MIGRATION_PG_SMOKE_URL not set; PostgreSQL scheduling-preferences migration smoke test skipped")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	if _, err := schema.EnsurePostgres(ctx, db); err != nil {
		t.Fatalf("ensure postgres: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO "juhe_business"."scheduling_preferences"
		(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
		VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', NULL, 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed builtin row: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	seed(`INSERT INTO "juhe_business"."route_strategies"
		(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, 'normal', 'active', 0, $4, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
		"st-pg-speed", "sys-pg", "策略PG",
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000}}`)
	seed(`INSERT INTO "juhe_business"."api_keys"
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULL, $5, 'sk-prefix', 'suf', 'enc', 'active', 0, 'general', NULL, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
		"k-pg", "sys-pg", "st-pg-speed", "k-pg", "hash-pg")

	options := Options{Dialect: capabilitydomain.DialectPostgres, Now: fixtureClock}
	preflight, err := Preflight(ctx, db, options)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if preflight.APIKeysNullGeneral != 1 || len(preflight.PlannedPreferences) != 1 {
		t.Fatalf("PG 预检计数异常: %+v", preflight)
	}
	report, err := Run(ctx, db, options)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !report.Ready() || report.PreferencesCreated != 1 || report.APIKeysBackfilled != 1 {
		t.Fatalf("PG 迁移异常: %+v failures=%+v", report, report.Failures)
	}
	var bound sql.NullString
	if err := db.QueryRow(`SELECT scheduling_preference_id FROM "juhe_business"."api_keys" WHERE id = 'k-pg'`).Scan(&bound); err != nil || !bound.Valid || bound.String != "schpref-mig-st-pg-speed" {
		t.Fatalf("PG 回填异常: %v %v", bound, err)
	}
	verify, err := Verify(ctx, db, options)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verify.Ready() {
		t.Fatalf("PG verify 必须放行: %+v", verify)
	}
	rerun, err := Run(ctx, db, options)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !rerun.AlreadyComplete {
		t.Fatalf("PG 重复执行必须只报告完成: %+v", rerun)
	}
}

// scheduling_preferences 启动只读校验（调度偏好独立实体
// PLAN-20261012T033000000Z P1，接口契约「调度偏好」章；P2 迁移完成记录门禁；
// v2 用户裁决修订 2026-10-13）——与 juhe-ai-gateway 同名文件互为镜像（形状
// 校验两模块无共享包）。
//
// 边界：建表 / api_keys 加列 / 内置行补种 / enabled 列退役 DROP COLUMN 归
// maintenance（--ensure-schema 的 ensure 臂恢复式补种内置行并幂等删列），jobs
// 启动只做一次性只读校验，不 DDL、不补种。四臂任一不符 fail-fast（部署顺序
// 契约：先 ensure-schema，再在停写窗口跑 --migrate-scheduling-preferences，
// 后启动新版本）：1) scheduling_preferences 表形状（8 列 + chat_mode /
// built_in 两组 CHECK 词表，enabled 已随 v2 第 4 条退役）；2) api_keys.
// scheduling_preference_id 绑定列（text/TEXT 可空）；3) 内置行存在且
// built_in=1、chat_mode 合法；4) P2 迁移完成记录（global_settings 键
// scheduling_preferences_migration）存在且 stage=completed——新二进制 + 旧库
// （无记录）必须被拦截，fresh 空库由 --ensure-schema/--seed 自动写记录；判定
// 语义单源于共享包 schedprefdomain.RequireCompleted，本文件不复制。错误信息
// 统一给出运维动作（juhe-ai-maintenance --ensure-schema /
// --migrate-scheduling-preferences）。权威 DDL 在 maintenance/internal/schema
// （sqlite_schema_business.go scheduling_preferences 建表语句 /
// pg_schema_business_tables.go 同构条目）。
//
// 调用面：run() 在能力域启动校验之后、buildWorkerAssembly 前执行一次（守护
// 路径与 --run-jobs-once 都到达）；方言连接打开方式与
// checkAccountsCapabilityDomainSchemaAtStartup 一致（SQLite mode=ro 只读句
// 柄即开即关；PG 一次性 pgx 连接，校验后立即关闭）。
//
// 判据说明（与 capability_domain_schema.go 同口径）：
//   - SQLite：PRAGMA table_info 取列集与类型/可空；CHECK 读 sqlite_master
//     建表文本做包含匹配，比较前小写化并去空白。
//   - PostgreSQL：information_schema.columns 取列集/类型/可空；CHECK 读
//     pg_constraint 的 pg_get_constraintdef——PG 会把 IN 归一化为
//     = ANY(ARRAY[...])，整数词表按「列名 + 去空白后的 in(0,1) 或
//     array[0,1]」双形态判定，字符串词表按字面量齐备判定。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
)

const (
	// schedulingPreferenceBuiltinID 是内置行固定 id（契约固定值，与
	// maintenance schema.BuiltinSchedulingPreferenceID 同源）。
	schedulingPreferenceBuiltinID = "schpref-builtin-chat-cost-first"

	// schedulingPreferenceBusinessPGSchema 是 PG 模式业务 schema（worker/
	// 任务族 juhe_business 前缀约定同源）。
	schedulingPreferenceBusinessPGSchema = "juhe_business"

	// schedulingPreferenceStartupCheckTimeout 限定启动校验的单次连接预算
	// （与能力域启动校验的 10s timeout 同量级）。
	schedulingPreferenceStartupCheckTimeout = 10 * time.Second

	// schedulingPreferenceStartupRemedy 是三臂失败共用的运维动作。
	schedulingPreferenceStartupRemedy = "先运行 juhe-ai-maintenance --ensure-schema（幂等建表/补列/删列并补种内置调度偏好行）再启动"

	// schedulingPreferenceExpectedShape 是错误信息中的期望表形状（与
	// maintenance DDL 同构；enabled 已随 v2 第 4 条退役）。
	schedulingPreferenceExpectedShape = "scheduling_preferences(id TEXT PK, system_account_id TEXT NULL, name TEXT NOT NULL, chat_mode TEXT NOT NULL CHECK (chat_mode IN ('cost_first','speed_first')), chat_speed_first_config_json TEXT NULL, built_in INTEGER NOT NULL DEFAULT 0 CHECK (built_in IN (0,1)), created_at TEXT NOT NULL, updated_at TEXT NOT NULL)"
)

// schedulingPreferenceColumns 是权威 8 列（顺序即 maintenance DDL 列序；
// enabled 已随 v2 用户裁决修订第 4 条退役，存量库由 ensure 守卫删列）。
var schedulingPreferenceColumns = [8]string{
	"id", "system_account_id", "name", "chat_mode", "chat_speed_first_config_json",
	"built_in", "created_at", "updated_at",
}

// schedulingPreferenceSQLiteCheckTexts 是 SQLite 建表文本中两组 CHECK 的判据
// 片段（比较前双方归一化；enabled 词表已随 v2 第 4 条退役）。
var schedulingPreferenceSQLiteCheckTexts = [2]string{
	"chat_mode in ('cost_first','speed_first')",
	"built_in in (0,1)",
}

// schedulingPreferencePostgresCheckTexts 是 PG pg_get_constraintdef 归一化
// 文本中两组 CHECK 的判定：chat_mode 按字符串字面量齐备判定（PG 保留字面
// 量）；built_in 的整数词表按「列名 + in(0,1) 或 array[0,1] 双形态」判定
// （PG 把 IN 归一化为 = ANY(ARRAY[...])）。enabled 词表已随 v2 第 4 条退役。
var schedulingPreferencePostgresCheckTexts = [2][3]string{
	{"chat_mode", "'cost_first'", "'speed_first'"},
	{"built_in", "in(0,1)", "array[0,1]"},
}

// checkSchedulingPreferenceSchemaAtStartup 按装配方言打开一次性只读连接执行
// 调度偏好三臂校验；worker 配置层面的 fail-fast 已在此前完成。
func checkSchedulingPreferenceSchemaAtStartup(ctx context.Context, config workerConfig) error {
	checkCtx, cancel := context.WithTimeout(ctx, schedulingPreferenceStartupCheckTimeout)
	defer cancel()
	if config.Driver == "postgres" {
		db, err := sql.Open("pgx", config.PostgresURL)
		if err != nil {
			return fmt.Errorf("scheduling_preferences 启动校验失败：打开业务 PostgreSQL 连接失败: %w", err)
		}
		defer db.Close()
		if err := db.PingContext(checkCtx); err != nil {
			return fmt.Errorf("scheduling_preferences 启动校验失败：连接业务 PostgreSQL 失败: %w", err)
		}
		return checkSchedulingPreferenceSchema(checkCtx, db, true)
	}
	dsn, err := sqliteReadOnlyFileDSN(config.BusinessSQLitePath)
	if err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败: %w", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：打开业务 SQLite 只读句柄失败: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(checkCtx); err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：连接业务 SQLite 失败（%s）: %w", config.BusinessSQLitePath, err)
	}
	return checkSchedulingPreferenceSchema(checkCtx, db, false)
}

// checkSchedulingPreferenceSchema 对业务库做调度偏好四臂只读校验；postgres
// 选择方言。返回 nil 表示表形状、绑定列、内置行与 P2 迁移完成记录全部符合契
// 约。
func checkSchedulingPreferenceSchema(ctx context.Context, db *sql.DB, postgres bool) error {
	if db == nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：业务库句柄为空（%s）", schedulingPreferenceStartupRemedy)
	}
	if postgres {
		return checkSchedulingPreferenceSchemaPostgres(ctx, db, schedulingPreferenceBusinessPGSchema)
	}
	return checkSchedulingPreferenceSchemaSQLite(ctx, db)
}

// checkSchedulingPreferenceMigrationRecord 是第四臂：P2 迁移完成记录必须存在
// 且 stage=completed（判定语义与文案单源于 schedprefdomain.RequireCompleted，
// 本文件只做方言选择；与 gateway 镜像同实现）。
func checkSchedulingPreferenceMigrationRecord(ctx context.Context, db *sql.DB, postgres bool) error {
	dialect := capabilitydomain.DialectSQLite
	if postgres {
		dialect = capabilitydomain.DialectPostgres
	}
	return schedprefdomain.RequireCompleted(ctx, db, dialect)
}

// schedulingPreferenceNormalized 小写化并移除全部空白（空格/换行/制表），供
// CHECK 判据包含匹配使用（与 capabilityDomainNormalized 同实现，镜像文件不
// 共享包故各自持有）。
func schedulingPreferenceNormalized(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), "")
}

// checkSchedulingPreferenceSchemaSQLite 执行 SQLite 方言四臂校验。
func checkSchedulingPreferenceSchemaSQLite(ctx context.Context, db *sql.DB) error {
	// 臂 1：表形状（8 列精确集合 + 两组 CHECK 词表）。
	columns := map[string]bool{}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info("scheduling_preferences")`)
	if err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：读取 PRAGMA table_info 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scheduling_preferences 启动校验失败：扫描 PRAGMA table_info 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("scheduling_preferences 启动校验失败：遍历 PRAGMA table_info 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	rows.Close()
	if len(columns) == 0 {
		return fmt.Errorf("scheduling_preferences 启动校验失败：表缺失，期望形状 %s（%s）", schedulingPreferenceExpectedShape, schedulingPreferenceStartupRemedy)
	}
	for _, column := range schedulingPreferenceColumns {
		if !columns[column] {
			return fmt.Errorf("scheduling_preferences 启动校验失败：缺列 %s，期望形状 %s（%s）", column, schedulingPreferenceExpectedShape, schedulingPreferenceStartupRemedy)
		}
	}
	var ddl sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='scheduling_preferences'`).Scan(&ddl); err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：读取建表文本失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	if !ddl.Valid {
		return fmt.Errorf("scheduling_preferences 启动校验失败：建表文本为空，期望形状 %s（%s）", schedulingPreferenceExpectedShape, schedulingPreferenceStartupRemedy)
	}
	normalizedDDL := schedulingPreferenceNormalized(ddl.String)
	for _, checkText := range schedulingPreferenceSQLiteCheckTexts {
		if !strings.Contains(normalizedDDL, schedulingPreferenceNormalized(checkText)) {
			return fmt.Errorf("scheduling_preferences 启动校验失败：建表文本缺少 CHECK 判据 %s，期望形状 %s（%s）", checkText, schedulingPreferenceExpectedShape, schedulingPreferenceStartupRemedy)
		}
	}

	// 臂 2：api_keys.scheduling_preference_id 绑定列（TEXT 可空）。
	foundAPIKeyColumn := false
	apiKeyRows, err := db.QueryContext(ctx, `PRAGMA table_info("api_keys")`)
	if err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：读取 api_keys PRAGMA table_info 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	for apiKeyRows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := apiKeyRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			apiKeyRows.Close()
			return fmt.Errorf("scheduling_preferences 启动校验失败：扫描 api_keys PRAGMA table_info 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
		}
		if name == "scheduling_preference_id" {
			foundAPIKeyColumn = true
			if !strings.EqualFold(strings.TrimSpace(columnType), "TEXT") {
				apiKeyRows.Close()
				return fmt.Errorf("scheduling_preferences 启动校验失败：api_keys.scheduling_preference_id 列类型为 %q，期望 TEXT（%s）", columnType, schedulingPreferenceStartupRemedy)
			}
			if notNull != 0 {
				apiKeyRows.Close()
				return fmt.Errorf("scheduling_preferences 启动校验失败：api_keys.scheduling_preference_id 列不可空，期望可空 TEXT 绑定列（%s）", schedulingPreferenceStartupRemedy)
			}
		}
	}
	if err := apiKeyRows.Err(); err != nil {
		apiKeyRows.Close()
		return fmt.Errorf("scheduling_preferences 启动校验失败：遍历 api_keys PRAGMA table_info 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	apiKeyRows.Close()
	if !foundAPIKeyColumn {
		return fmt.Errorf("scheduling_preferences 启动校验失败：api_keys.scheduling_preference_id 绑定列缺失，期望 api_keys 增加 scheduling_preference_id TEXT NULL（%s）", schedulingPreferenceStartupRemedy)
	}

	// 臂 3：内置行存在且 built_in=1、chat_mode 合法。
	if err := checkSchedulingPreferenceBuiltinRow(ctx, db, "scheduling_preferences", "?"); err != nil {
		return err
	}
	// 臂 4：P2 迁移完成记录存在且 stage=completed。
	return checkSchedulingPreferenceMigrationRecord(ctx, db, false)
}

// checkSchedulingPreferenceBuiltinRow 读取内置行并做两值校验（built_in +
// chat_mode；enabled 校验随 v2 第 4 条退役）。qualifiedTable 已带方言所需限
// 定（SQLite 裸表名；PG 方言由调用方传 "schema".表 限定形式），placeholder
// 是方言占位符（SQLite ? / PG $1）。独立成函数供镜像与测试复用。
func checkSchedulingPreferenceBuiltinRow(ctx context.Context, db *sql.DB, qualifiedTable, placeholder string) error {
	var builtIn int
	var chatMode string
	query := fmt.Sprintf(`SELECT built_in, chat_mode FROM %s WHERE id = %s`, qualifiedTable, placeholder)
	err := db.QueryRowContext(ctx, query, schedulingPreferenceBuiltinID).Scan(&builtIn, &chatMode)
	if err == sql.ErrNoRows {
		return fmt.Errorf("scheduling_preferences 启动校验失败：内置调度偏好行（id=%s）缺失，期望 %s（%s）", schedulingPreferenceBuiltinID, schedulingPreferenceExpectedShape, schedulingPreferenceStartupRemedy)
	}
	if err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：读取内置调度偏好行失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	if builtIn != 1 {
		return fmt.Errorf("scheduling_preferences 启动校验失败：内置行 built_in=%d，期望 1（内置行是全局只读例外，被改动属初始化错误；%s）", builtIn, schedulingPreferenceStartupRemedy)
	}
	if chatMode != "cost_first" && chatMode != "speed_first" {
		return fmt.Errorf("scheduling_preferences 启动校验失败：内置行 chat_mode=%q 非法，期望 cost_first/speed_first（%s）", chatMode, schedulingPreferenceStartupRemedy)
	}
	return nil
}

// checkSchedulingPreferenceSchemaPostgres 执行 PG 方言四臂校验（schema 形如
// juhe_business，由测试以 scratch schema 注入）。
func checkSchedulingPreferenceSchemaPostgres(ctx context.Context, db *sql.DB, schema string) error {
	// 臂 1：表形状（8 列精确集合）。
	columnRows, err := db.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'scheduling_preferences'`, schema)
	if err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：读取 information_schema 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	columns := map[string]bool{}
	for columnRows.Next() {
		var name string
		if err := columnRows.Scan(&name); err != nil {
			columnRows.Close()
			return fmt.Errorf("scheduling_preferences 启动校验失败：扫描 information_schema 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
		}
		columns[name] = true
	}
	if err := columnRows.Err(); err != nil {
		columnRows.Close()
		return fmt.Errorf("scheduling_preferences 启动校验失败：遍历 information_schema 失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	columnRows.Close()
	if len(columns) == 0 {
		return fmt.Errorf("scheduling_preferences 启动校验失败：%s.scheduling_preferences 表缺失，期望形状 %s（%s）", schema, schedulingPreferenceExpectedShape, schedulingPreferenceStartupRemedy)
	}
	for _, column := range schedulingPreferenceColumns {
		if !columns[column] {
			return fmt.Errorf("scheduling_preferences 启动校验失败：%s.scheduling_preferences 缺列 %s，期望形状 %s（%s）", schema, column, schedulingPreferenceExpectedShape, schedulingPreferenceStartupRemedy)
		}
	}
	checkRows, err := db.QueryContext(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_catalog.pg_constraint
		WHERE conrelid = $1::regclass AND contype = 'c'`, schema+".scheduling_preferences")
	if err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：读取 %s.scheduling_preferences CHECK 约束失败: %w（%s）", schema, err, schedulingPreferenceStartupRemedy)
	}
	definitions := []string{}
	for checkRows.Next() {
		var definition string
		if err := checkRows.Scan(&definition); err != nil {
			checkRows.Close()
			return fmt.Errorf("scheduling_preferences 启动校验失败：扫描 CHECK 约束失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
		}
		definitions = append(definitions, definition)
	}
	if err := checkRows.Err(); err != nil {
		checkRows.Close()
		return fmt.Errorf("scheduling_preferences 启动校验失败：遍历 CHECK 约束失败: %w（%s）", err, schedulingPreferenceStartupRemedy)
	}
	checkRows.Close()
	normalizedDefinitions := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		normalizedDefinitions = append(normalizedDefinitions, schedulingPreferenceNormalized(definition))
	}
	for _, expected := range schedulingPreferencePostgresCheckTexts {
		satisfied := false
		for _, normalized := range normalizedDefinitions {
			if !strings.Contains(normalized, expected[0]) {
				continue
			}
			if strings.Contains(normalized, expected[1]) || strings.Contains(normalized, expected[2]) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			return fmt.Errorf("scheduling_preferences 启动校验失败：%s.scheduling_preferences 的 CHECK 约束未覆盖 %s 词表（期望形状 %s；%s）", schema, expected[0], schedulingPreferenceExpectedShape, schedulingPreferenceStartupRemedy)
		}
	}

	// 臂 2：api_keys.scheduling_preference_id 绑定列（text 可空）。
	var dataType, isNullable string
	err = db.QueryRowContext(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'api_keys' AND column_name = 'scheduling_preference_id'`, schema).
		Scan(&dataType, &isNullable)
	if err == sql.ErrNoRows {
		return fmt.Errorf("scheduling_preferences 启动校验失败：%s.api_keys.scheduling_preference_id 绑定列缺失，期望 api_keys 增加 scheduling_preference_id text NULL（%s）", schema, schedulingPreferenceStartupRemedy)
	}
	if err != nil {
		return fmt.Errorf("scheduling_preferences 启动校验失败：读取 %s.api_keys information_schema 失败: %w（%s）", schema, err, schedulingPreferenceStartupRemedy)
	}
	if !strings.EqualFold(strings.TrimSpace(dataType), "text") {
		return fmt.Errorf("scheduling_preferences 启动校验失败：api_keys.scheduling_preference_id 列类型为 %q，期望 text（%s）", dataType, schedulingPreferenceStartupRemedy)
	}
	if !strings.EqualFold(strings.TrimSpace(isNullable), "YES") {
		return fmt.Errorf("scheduling_preferences 启动校验失败：api_keys.scheduling_preference_id 列不可空，期望可空 text 绑定列（%s）", schedulingPreferenceStartupRemedy)
	}

	// 臂 3：内置行存在且 built_in=1、chat_mode 合法。
	if err := checkSchedulingPreferenceBuiltinRow(ctx, db, fmt.Sprintf(`"%s".scheduling_preferences`, schema), "$1"); err != nil {
		return err
	}
	// 臂 4：P2 迁移完成记录存在且 stage=completed。
	return checkSchedulingPreferenceMigrationRecord(ctx, db, true)
}

// accounts.capability_domain 启动只读形状校验（AI 账户能力域拆分
// PLAN-20261011T141004000Z §数据变化）。
//
// 边界：加列与域赋值归 maintenance（migrate/verify 命令 + SQLite ensure 守卫
// 臂），jobs 启动只做一次性只读校验，不 ALTER、不重推账户域。列缺失或四值
// 形状（TEXT / NOT NULL / DEFAULT 'chat' / CHECK 四枚举）不符时 fail-fast
// 停止，错误信息携带期望形状；不静默修补（部署顺序契约：先迁移后启动新版
// 本）。M2 起在形状校验之后追加阶段记录门禁（要求
// account_capability_domain_migration 已封存 m2_finalized，判定语义与运维
// 文案在共享包 capabilitydomain.RequireM2Finalized，与 gateway 启动门禁共
// 用）。本文件与 juhe-ai-gateway 同名文件互为镜像（形状校验两模块无共享包），
// 权威 DDL 在 maintenance/internal/schema 的 accounts 建表语句。
//
// 调用面：run() 在 loadWorkerConfig 成功后、buildWorkerAssembly 前执行一次
// （守护路径与 --run-jobs-once 都到达；配置级 fail-fast 臂仍在它之前）。
// SQLite 用 mode=ro 只读句柄即开即关（复用 sqliteReadOnlyFileDSN 的转义与
// pragma 约定；此时 F1 store 已打开成功，业务库文件必已存在）；PG 用一次性
// pgx 连接，校验后立即关闭，不占用共享池。
//
// 判据说明：
//   - SQLite：PRAGMA table_info 取类型/NOT NULL/DEFAULT；CHECK 读
//     sqlite_master 建表文本做包含匹配（modernc.org/sqlite 对 ALTER TABLE
//     ADD COLUMN 会把新列声明连同 CHECK 回写建表文本），比较前做小写化与
//     去空白归一。
//   - PostgreSQL：information_schema.columns 取类型/可空/默认值；CHECK 读
//     pg_constraint 的 pg_get_constraintdef——PG 会把 IN 归一化为
//     = ANY(ARRAY[...])，因此按「约束含 capability_domain 且四值字面量齐备」
//     判定值集形状，不匹配 IN 语法字面。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// capabilityDomainColumn 是受检列名；capabilityDomainExpectedShape 用于错误
// 信息（期望形状原文，与 maintenance DDL 同构）。
const (
	capabilityDomainColumn         = "capability_domain"
	capabilityDomainDefaultLiteral = "'chat'"
	// capabilityDomainCheckText 是列级 CHECK 的四值判据（SQLite 建表文本包含
	// 匹配的 needle，比较前双方小写化并去空白）。
	capabilityDomainCheckText = "capability_domain in ('chat','video','audio','image')"

	// capabilityDomainExpectedShape 是错误信息中的期望形状（与
	// maintenance/internal/schema accounts DDL 的列声明一致）。
	capabilityDomainExpectedShape = "capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))"

	// capabilityDomainBusinessPGSchema 是 PG 模式业务 schema（worker/任务族
	// juhe_business 前缀约定同源）。
	capabilityDomainBusinessPGSchema = "juhe_business"

	// capabilityDomainStartupCheckTimeout 限定启动校验的单次连接预算（与
	// J1/J3a 契约检查的 10s timeout 同量级）。
	capabilityDomainStartupCheckTimeout = 10 * time.Second
)

// capabilityDomainExpectedValues 是 CHECK 允许值集（PG pg_get_constraintdef
// 按值集判定）。数组字面量不可为 const，按 var 承载。
var capabilityDomainExpectedValues = [4]string{"'chat'", "'video'", "'audio'", "'image'"}

// checkAccountsCapabilityDomainSchemaAtStartup 按装配方言打开一次性只读连接
// 执行形状校验与 M2 阶段记录门禁；worker 配置层面的 fail-fast 已在此前完成。
func checkAccountsCapabilityDomainSchemaAtStartup(ctx context.Context, config workerConfig) error {
	checkCtx, cancel := context.WithTimeout(ctx, capabilityDomainStartupCheckTimeout)
	defer cancel()
	if config.Driver == "postgres" {
		db, err := sql.Open("pgx", config.PostgresURL)
		if err != nil {
			return fmt.Errorf("accounts.capability_domain 启动校验失败：打开业务 PostgreSQL 连接失败: %w", err)
		}
		defer db.Close()
		if err := db.PingContext(checkCtx); err != nil {
			return fmt.Errorf("accounts.capability_domain 启动校验失败：连接业务 PostgreSQL 失败: %w", err)
		}
		if err := checkAccountsCapabilityDomainSchema(checkCtx, db, true); err != nil {
			return err
		}
		return checkAccountCapabilityDomainStageRecord(checkCtx, db, true)
	}
	dsn, err := sqliteReadOnlyFileDSN(config.BusinessSQLitePath)
	if err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败: %w", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：打开业务 SQLite 只读句柄失败: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(checkCtx); err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：连接业务 SQLite 失败（%s）: %w", config.BusinessSQLitePath, err)
	}
	if err := checkAccountsCapabilityDomainSchema(checkCtx, db, false); err != nil {
		return err
	}
	return checkAccountCapabilityDomainStageRecord(checkCtx, db, false)
}

// checkAccountCapabilityDomainStageRecord 是 M2 启动阶段门禁（AI 账户能力域
// 拆分 v7 定案）：在形状校验之后读业务库阶段记录，要求存在且 stage 已是
// m2_finalized（ruleVersion 一致性与记录形状由共享包 ParseRecord 在读取时
// 强制）。任一不满足即 fail-fast，错误信息直接给出运维动作（停写 → 显式跑
// maintenance M1 → m2-cutover）。判定语义与文案单一事实源在共享包
// RequireM2Finalized（与 gateway 启动门禁共用），本包装只做方言选择；启动
// 只读，不变成迁移执行者。
func checkAccountCapabilityDomainStageRecord(ctx context.Context, db *sql.DB, postgres bool) error {
	if db == nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：业务库句柄为空")
	}
	dialect := capdomain.DialectSQLite
	if postgres {
		dialect = capdomain.DialectPostgres
	}
	return capdomain.RequireM2Finalized(ctx, db, dialect)
}

// checkAccountsCapabilityDomainSchema 对业务库 accounts.capability_domain 做
// 一次性只读形状校验；postgres 选择方言。返回 nil 表示列存在且形状正确。
func checkAccountsCapabilityDomainSchema(ctx context.Context, db *sql.DB, postgres bool) error {
	if db == nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：业务库句柄为空")
	}
	if postgres {
		return checkAccountsCapabilityDomainSchemaPostgres(ctx, db, capabilityDomainBusinessPGSchema)
	}
	return checkAccountsCapabilityDomainSchemaSQLite(ctx, db)
}

func checkAccountsCapabilityDomainSchemaSQLite(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info("accounts")`)
	if err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：读取 PRAGMA table_info 失败: %w", err)
	}
	defer rows.Close()
	found := false
	var declaredType string
	var notNull int
	var defaultValue any
	for rows.Next() {
		var cid, pk int
		var rowNotNull int
		var name, columnType string
		var rowDefault any
		if err := rows.Scan(&cid, &name, &columnType, &rowNotNull, &rowDefault, &pk); err != nil {
			return fmt.Errorf("accounts.capability_domain 启动校验失败：扫描 PRAGMA table_info 失败: %w", err)
		}
		// 逐行局部变量承接，命中目标列时才提升：外层变量直扫会被末列覆盖。
		if name == capabilityDomainColumn {
			found = true
			declaredType, notNull, defaultValue = columnType, rowNotNull, rowDefault
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：遍历 PRAGMA table_info 失败: %w", err)
	}
	if !found {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：列缺失，期望形状 %s（先运行 juhe-ai-maintenance 能力域迁移再启动）", capabilityDomainExpectedShape)
	}
	var dflt sql.NullString
	if value, ok := defaultValue.(string); ok {
		dflt = sql.NullString{String: value, Valid: true}
	} else if defaultValue != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：PRAGMA table_info dflt_value 类型异常 %T，期望形状 %s", defaultValue, capabilityDomainExpectedShape)
	}
	if !strings.EqualFold(strings.TrimSpace(declaredType), "TEXT") {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：列类型为 %q，期望 TEXT，期望形状 %s", declaredType, capabilityDomainExpectedShape)
	}
	if notNull != 1 {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：列可空，期望 NOT NULL，期望形状 %s", capabilityDomainExpectedShape)
	}
	if !dflt.Valid || dflt.String != capabilityDomainDefaultLiteral {
		rendered := "NULL（无默认值）"
		if dflt.Valid {
			rendered = dflt.String
		}
		return fmt.Errorf("accounts.capability_domain 启动校验失败：默认值为 %s，期望 %s，期望形状 %s", rendered, capabilityDomainDefaultLiteral, capabilityDomainExpectedShape)
	}
	var ddl sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='accounts'`).Scan(&ddl); err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：读取 accounts 建表文本失败: %w", err)
	}
	if !ddl.Valid || !strings.Contains(capabilityDomainNormalized(ddl.String), capabilityDomainNormalized(capabilityDomainCheckText)) {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：建表文本缺少四值 CHECK 判据 %s，期望形状 %s", capabilityDomainCheckText, capabilityDomainExpectedShape)
	}
	return nil
}

func checkAccountsCapabilityDomainSchemaPostgres(ctx context.Context, db *sql.DB, schema string) error {
	var dataType, isNullable string
	var columnDefault sql.NullString
	err := db.QueryRowContext(ctx, `SELECT data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'accounts' AND column_name = $2`, schema, capabilityDomainColumn).
		Scan(&dataType, &isNullable, &columnDefault)
	if err == sql.ErrNoRows {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：%s.accounts 缺列，期望形状 %s（先运行 juhe-ai-maintenance 能力域迁移再启动）", schema, capabilityDomainExpectedShape)
	}
	if err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：读取 information_schema 失败: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(dataType), "text") {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：列类型为 %q，期望 text，期望形状 %s", dataType, capabilityDomainExpectedShape)
	}
	if !strings.EqualFold(strings.TrimSpace(isNullable), "NO") {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：列可空，期望 NOT NULL，期望形状 %s", capabilityDomainExpectedShape)
	}
	if !columnDefault.Valid || !strings.HasPrefix(columnDefault.String, capabilityDomainDefaultLiteral) {
		rendered := "NULL（无默认值）"
		if columnDefault.Valid {
			rendered = columnDefault.String
		}
		return fmt.Errorf("accounts.capability_domain 启动校验失败：默认值为 %s，期望前缀 %s（PG 以 ::text 等形式携带类型转换），期望形状 %s", rendered, capabilityDomainDefaultLiteral, capabilityDomainExpectedShape)
	}
	checkRows, err := db.QueryContext(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_catalog.pg_constraint
		WHERE conrelid = $1::regclass AND contype = 'c'`, schema+".accounts")
	if err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：读取 %s.accounts CHECK 约束失败: %w", schema, err)
	}
	defer checkRows.Close()
	definitions := []string{}
	for checkRows.Next() {
		var definition string
		if err := checkRows.Scan(&definition); err != nil {
			checkRows.Close()
			return fmt.Errorf("accounts.capability_domain 启动校验失败：扫描 CHECK 约束失败: %w", err)
		}
		definitions = append(definitions, definition)
	}
	if err := checkRows.Err(); err != nil {
		checkRows.Close()
		return fmt.Errorf("accounts.capability_domain 启动校验失败：遍历 CHECK 约束失败: %w", err)
	}
	checkRows.Close()
	// PG 把 IN (...) 归一化为 = ANY(ARRAY[...])，按「同一约束含列名与四值
	// 字面量」判定值集形状；不做 IN 语法字面匹配。
	for _, definition := range definitions {
		normalized := capabilityDomainNormalized(definition)
		if !strings.Contains(normalized, capabilityDomainColumn) {
			continue
		}
		allValues := true
		for _, value := range capabilityDomainExpectedValues {
			if !strings.Contains(normalized, value) {
				allValues = false
				break
			}
		}
		if allValues {
			return nil
		}
	}
	return fmt.Errorf("accounts.capability_domain 启动校验失败：%s.accounts 的 CHECK 约束未覆盖四值域集 %v，期望形状 %s", schema, capabilityDomainExpectedValues, capabilityDomainExpectedShape)
}

// capabilityDomainNormalized 小写化并移除全部空白（空格/换行/制表），供判据
// 包含匹配使用。
func capabilityDomainNormalized(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), "")
}

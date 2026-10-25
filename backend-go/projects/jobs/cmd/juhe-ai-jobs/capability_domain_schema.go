// accounts.capability_domain 启动只读形状校验（AI 账户能力域拆分
// PLAN-20261011T141004000Z §数据变化）。
//
// 边界：加列与域赋值归 maintenance（migrate/verify 命令），jobs 启动只做一次
// 性只读校验，不 ALTER、不重推账户域。列缺失或四值
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
// PG 用一次性 pgx 连接，校验后立即关闭，不占用共享池（原 SQLite mode=ro
// 只读句柄臂已随 sqlite 形态退役删除）。
//
// 判据说明：information_schema.columns 取类型/可空/默认值；CHECK 读
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

// checkAccountsCapabilityDomainSchemaAtStartup 打开一次性只读 PG 连接执行
// 形状校验与 M2 阶段记录门禁；worker 配置层面的 fail-fast 已在此前完成。
func checkAccountsCapabilityDomainSchemaAtStartup(ctx context.Context, config workerConfig) error {
	checkCtx, cancel := context.WithTimeout(ctx, capabilityDomainStartupCheckTimeout)
	defer cancel()
	db, err := sql.Open("pgx", config.PostgresURL)
	if err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：打开业务 PostgreSQL 连接失败: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(checkCtx); err != nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：连接业务 PostgreSQL 失败: %w", err)
	}
	if err := checkAccountsCapabilityDomainSchemaPostgres(checkCtx, db, capabilityDomainBusinessPGSchema); err != nil {
		return err
	}
	return checkAccountCapabilityDomainStageRecord(checkCtx, db)
}

// checkAccountCapabilityDomainStageRecord 是 M2 启动阶段门禁（AI 账户能力域
// 拆分 v7 定案）：在形状校验之后读业务库阶段记录，要求存在且 stage 已是
// m2_finalized（ruleVersion 一致性与记录形状由共享包 ParseRecord 在读取时
// 强制）。任一不满足即 fail-fast，错误信息直接给出运维动作（停写 → 显式跑
// maintenance M1 → m2-cutover）。判定语义与文案单一事实源在共享包
// RequireM2Finalized（与 gateway 启动门禁共用）；启动只读，不变成迁移执行者。
func checkAccountCapabilityDomainStageRecord(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("accounts.capability_domain 启动校验失败：业务库句柄为空")
	}
	return capdomain.RequireM2Finalized(ctx, db)
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

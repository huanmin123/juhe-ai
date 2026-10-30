// 结构收敛分相执行（升级兼容与迁移账本设计 §7.3）：EnsureAll（既有行为）
// 之外提供 Additive/Destructive 两相过滤，upgrade 以
// 「加法相 → 数据步骤 → 破坏相」顺序调用，使破坏性窗口缩小到最后一步、
// 数据步骤失败时旧二进制仍可运行。语句分类为构建期规则（isDestructiveDDL），
// 不手写清单；各相内部保持 Node 清单相对顺序不变，跨相重排合法性由
// PhaseOrderSafetyTest 静态钉住。
package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// EnsurePhase 选择执行的语句相。
type EnsurePhase int

const (
	// EnsureAll 执行全部语句（加法 + 破坏，--ensure-schema CLI 行为不变）。
	EnsureAll EnsurePhase = iota
	// EnsureAdditive 仅执行非破坏语句（DROP TABLE/COLUMN 以外全部）。
	EnsureAdditive
	// EnsureDestructive 仅执行 DROP TABLE / DROP COLUMN。
	EnsureDestructive
)

// EnsurePostgresFiltered 按 phase 过滤执行。返回的 StatementCount 是实际执行
// 的语句数（分相下小于全量清单）；SchemaCount 同语义。能力域列形状校验只在
// 含加法语句的相位执行（ADD COLUMN 属加法相）。
func EnsurePostgresFiltered(ctx context.Context, db *sql.DB, phase EnsurePhase) (PGResult, error) {
	createdSchemas := make(map[string]bool)
	executed := 0
	for i, statement := range postgresSchemaStatements {
		if !phaseIncludes(phase, statement.SQL) {
			continue
		}
		if !createdSchemas[statement.SchemaName] {
			createdSchemas[statement.SchemaName] = true
			createSchema := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quotePGIdentifier(statement.SchemaName))
			if err := execPostgresStatementWithLockRetry(ctx, db, createSchema, createSchema); err != nil {
				return PGResult{}, fmt.Errorf("create postgres schema %s: %w", statement.SchemaName, err)
			}
		}
		execSQL := fmt.Sprintf("SET search_path TO %s, public;\n%s", quotePGIdentifier(statement.SchemaName), statement.SQL)
		if err := execPostgresStatementWithLockRetry(ctx, db, execSQL, statement.SQL); err != nil {
			return PGResult{}, fmt.Errorf("postgres schema statement %d (%s/%s): %w", i, statement.SchemaName, statement.Source, err)
		}
		executed++
	}
	if phase != EnsureDestructive {
		// AI 账户能力域拆分（PLAN-20261011T141004000Z）：ADD COLUMN IF NOT EXISTS
		// 只保证列存在，不以命中替代形状检查——加法相执行完后对既存列做四值
		// 形状校验（类型/NOT NULL/DEFAULT/CHECK），不符原样报错停止。
		if err := VerifyPostgresBusinessCapabilityDomainShape(ctx, db); err != nil {
			return PGResult{}, fmt.Errorf("verify accounts.capability_domain shape: %w", err)
		}
	}
	return PGResult{SchemaCount: len(createdSchemas), StatementCount: executed}, nil
}

func phaseIncludes(phase EnsurePhase, sqlText string) bool {
	switch phase {
	case EnsureAdditive:
		return !isDestructiveDDL(sqlText)
	case EnsureDestructive:
		return isDestructiveDDL(sqlText)
	default:
		return true
	}
}

// destructiveTargets 提取全部破坏性语句的目标（schema.table 与列名），
// 供相间顺序静态测试与报告披露使用。解析依赖清单语句的统一形态：
//
//	ALTER TABLE <schema>.<table> DROP COLUMN IF EXISTS <column>
//	DROP TABLE IF EXISTS <schema>.<table>
func DestructiveTargets() []DestructiveTarget {
	targets := []DestructiveTarget{}
	for _, statement := range postgresSchemaStatements {
		if !isDestructiveDDL(statement.SQL) {
			continue
		}
		upper := strings.ToUpper(statement.SQL)
		if strings.Contains(upper, "DROP COLUMN") {
			const marker = "DROP COLUMN IF EXISTS "
			idx := strings.Index(upper, marker)
			column := strings.TrimSpace(statement.SQL[idx+len(marker):])
			table := tableBetweenAlterAndDrop(statement.SQL)
			targets = append(targets, DestructiveTarget{Schema: schemaOf(statement), Table: table, Column: column})
			continue
		}
		if strings.Contains(upper, "DROP TABLE") {
			const marker = "DROP TABLE IF EXISTS "
			idx := strings.Index(upper, marker)
			qualified := strings.TrimSpace(statement.SQL[idx+len(marker):])
			schemaName, table := splitQualified(qualified)
			targets = append(targets, DestructiveTarget{Schema: schemaName, Table: table})
		}
	}
	return targets
}

// DestructiveTarget 是一条破坏性语句的删除对象。
type DestructiveTarget struct {
	Schema string
	Table  string
	Column string // DROP TABLE 时为空
}

func schemaOf(statement PGStatement) string { return statement.SchemaName }

func tableBetweenAlterAndDrop(sqlText string) string {
	upper := strings.ToUpper(sqlText)
	const alterMarker = "ALTER TABLE "
	start := strings.Index(upper, alterMarker)
	if start < 0 {
		return ""
	}
	rest := sqlText[start+len(alterMarker):]
	if end := strings.IndexAny(rest, " \n"); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return strings.TrimSpace(rest)
}

func splitQualified(qualified string) (schemaName, table string) {
	parts := strings.Split(qualified, ".")
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.Trim(strings.TrimSpace(parts[1]), `";`)
	}
	return "", strings.Trim(qualified, `";`)
}

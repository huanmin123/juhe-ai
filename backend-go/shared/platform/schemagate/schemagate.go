// Package schemagate 是 gateway / jobs 共用的升级启动门禁（升级兼容与迁移
// 账本设计 §11.1）：对业务库做一次只读检查，按五行求值顺序表判定
// 「库是否被本二进制的 upgrade 收敛过」，不符即 fail-fast 并给出可执行提示。
// 只读——本包绝不执行迁移、绝不写库；升级永远由用户显式运行
// `maintenance upgrade` 触发（§2.2-5 不做自动升级）。
//
// 判定输入：dbRevision = schema_state.schema_revision（缺失视为 0）、
// stepMax = max(schema_migrations.revision)（缺失视为 0）、
// binary = contracts.SchemaRevision。账本表缺失时由读取方按 0 处理。
package schemagate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/jackc/pgx/v5/pgconn"
)

// Decision 是五行求值顺序表的产出：通过为空；不通过携带完整 fail-fast 文案
//（含运维动作命令）。
type Decision struct {
	OK      bool
	Message string
}

// Check 连接业务库执行一次只读检查。db 由调用方注入（gateway/jobs 各自的
// 业务库连接）。
func Check(ctx context.Context, db *sql.DB) Decision {
	dbRevision, err := readStateRevision(ctx, db)
	if err != nil {
		return Decision{Message: fmt.Sprintf(
			"升级启动门禁读取 schema_state 失败: %v。请运行 juhe-ai-maintenance upgrade --dry-run 核对升级状态", err)}
	}
	stepMax, ledgerReady, err := readLedgerMaxRevision(ctx, db)
	if err != nil {
		return Decision{Message: fmt.Sprintf(
			"升级启动门禁读取 schema_migrations 失败: %v。请运行 juhe-ai-maintenance upgrade --dry-run 核对升级状态", err)}
	}
	return Evaluate(dbRevision, stepMax, ledgerReady)
}

// Evaluate 是五行求值顺序表的纯函数（可单测）：
//
//	顺序 1  stepMax > dbRevision                          → 升级中断在途
//	顺序 2  dbRevision > binary                           → 二进制过旧
//	顺序 3  dbRevision < binary                           → 库待升级
//	顺序 4  dbRevision > 0 且账本不存在/为空               → 账本反向不一致
//	顺序 5  其余（dbRevision == binary 且 stepMax <= db）  → 通过
func Evaluate(dbRevision, stepMax int64, ledgerReady bool) Decision {
	binary := int64(contracts.SchemaRevision)
	missing := schema_stateMissingText()
	switch {
	case stepMax > dbRevision:
		return Decision{Message: fmt.Sprintf(
			"上次升级未完成（账本 revision=%d 高于已收敛版本 %d）：请使用 schemaRevision ≥ %d 的 maintenance 重跑 `juhe-ai-maintenance upgrade --dsn <主库URL>` 续跑",
			stepMax, dbRevision, stepMax)}
	case dbRevision > binary:
		return Decision{Message: fmt.Sprintf(
			"二进制版本低于数据库结构（库 revision=%d > 二进制 schemaRevision=%d）：请升级二进制后启动",
			dbRevision, binary)}
	case dbRevision < binary:
		return Decision{Message: fmt.Sprintf(
			"数据库尚未升级到本版本（库 revision=%d < 二进制 schemaRevision=%d）：请先停止 gateway/jobs、完成备份，然后运行 `juhe-ai-maintenance upgrade --dry-run --dsn <主库URL>` 预演并按提示执行 upgrade。待执行步骤见 upgrade 计划输出%s",
			dbRevision, binary, missing)}
	case dbRevision > 0 && !ledgerReady:
		return Decision{Message: fmt.Sprintf(
			"升级账本缺失或被清空（state revision=%d 但无账本行）：请恢复备份，或重跑 `juhe-ai-maintenance upgrade --dsn <主库URL>` 重建账本并重新求值全部步骤",
			dbRevision)}
	default:
		return Decision{OK: true}
	}
}

func schema_stateMissingText() string {
	if int64(contracts.SchemaRevision) > 0 {
		return "（首次升级：全新安装序列见 docker/single-server/README.md）"
	}
	return ""
}

// readStateRevision 读 schema_state.schema_revision；表缺失/无行按 0。
func readStateRevision(ctx context.Context, db *sql.DB) (int64, error) {
	var revision int64
	err := db.QueryRowContext(ctx,
		`SELECT schema_revision FROM juhe_business.schema_state WHERE id = 1`).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil && isUndefinedTable(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// readLedgerMaxRevision 读账本最高 revision 与表就绪标记；表缺失返回 (0, false)。
func readLedgerMaxRevision(ctx context.Context, db *sql.DB) (int64, bool, error) {
	ready, err := tableReady(ctx, db, "schema_migrations")
	if err != nil || !ready {
		return 0, false, err
	}
	var stepMax sql.NullInt64
	err = db.QueryRowContext(ctx,
		`SELECT max(revision) FROM juhe_business.schema_migrations`).Scan(&stepMax)
	if err != nil {
		return 0, true, err
	}
	return stepMax.Int64, true, nil
}

func tableReady(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'juhe_business' AND table_name = $1`,
		table).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

// Package schemagate 是 gateway / jobs 共用的升级启动门禁（升级兼容与迁移
// 账本设计 §11.1、多节点发布设计 §5.2）：对业务库做一次只读检查，按求值
// 顺序表判定「库是否被本二进制的 upgrade 收敛过」，不符即 fail-fast 并给出
// 可执行提示。只读——本包绝不执行迁移、绝不写库；升级永远由用户显式运行
// `maintenance upgrade` 触发（§2.2-5 不做自动升级）。
//
// 判定输入：dbRevision = schema_state.schema_revision（缺失视为 0）、
// stepMax = max(schema_migrations.revision)（表缺失或为空视为 0）、
// binary = contracts.SchemaRevision、minRequired = contracts.MinRequiredRevision、
// minReadableBinary = schema_state.min_readable_binary_revision、
// expandCompleted = schema_state.expand_completed、中断步骤的 phase/destructive。
//
// 旧库没有 min_readable_binary_revision / phase 列时回退旧五表精确匹配，
// 保证门禁字段上线前的部署行为不变。
package schemagate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/jackc/pgx/v5/pgconn"
)

// Decision 是求值表的产出：OK=false 时 Message 是 fail-fast 文案；OK=true 且
// Warning 非空时调用方应打印 WARN（例如 expand 中断但旧二进制仍可服务）。
type Decision struct {
	OK      bool
	Message string
	Warning string
}

// Check 连接业务库执行一次只读检查。db 由调用方注入（gateway/jobs 各自的
// 业务库连接）。
//
// 旧库（没有 min_readable_binary_revision / phase 列）回退到旧五表精确匹配，
// 保证新门禁上线前已有部署行为不变；新库按多节点发布设计 §5.2 求值。
func Check(ctx context.Context, db *sql.DB) Decision {
	state, hasNewState, err := readGateState(ctx, db)
	if err != nil {
		return Decision{Message: fmt.Sprintf(
			"升级启动门禁读取 schema_state 失败: %v。请运行 juhe-ai-maintenance upgrade --dry-run 核对升级状态", err)}
	}
	stepMax, phase, destructive, hasPhase, err := readLedgerState(ctx, db)
	if err != nil {
		return Decision{Message: fmt.Sprintf(
			"升级启动门禁读取 schema_migrations 失败: %v。请运行 juhe-ai-maintenance upgrade --dry-run 核对升级状态", err)}
	}
	state.StepMax = stepMax
	state.InterruptedPhase = phase
	state.InterruptedDestructive = destructive
	if !hasNewState || !hasPhase {
		return Evaluate(state.DBRevision, stepMax)
	}
	return EvaluateWithState(state)
}

// Evaluate 是五行求值顺序表的纯函数（可单测）：
//
//	顺序 1  stepMax > dbRevision                          → 升级中断在途
//	顺序 2  dbRevision > binary                           → 二进制过旧
//	顺序 3  dbRevision < binary                           → 库待升级（stepMax==0 且
//	                                                        dbRevision>0 为空号中断形态，
//	                                                        文案改为直接续跑）
//	顺序 4  dbRevision > 0 且 stepMax == 0                → 账本缺失/被清空（反向不一致；
//	                                                        按行数而非表存在性判定，
//	                                                        TRUNCATE 同样命中）
//	顺序 5  其余（dbRevision == binary 且 stepMax <= db）  → 通过
//
// 顺序 3 先于顺序 4：dbRevision < binary 且 stepMax == 0 的空号中断按顺序 3 的
// 续跑文案处理；顺序 4 只承接「state 声称已完成但账本为空」的完成态不一致。
func Evaluate(dbRevision, stepMax int64) Decision {
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
		if stepMax == 0 && dbRevision > 0 {
			return Decision{Message: fmt.Sprintf(
				"结构未收敛完成（库 revision=%d，账本为空，属升级中断形态）：请直接重跑 `juhe-ai-maintenance upgrade --dsn <主库URL>` 续跑",
				dbRevision)}
		}
		return Decision{Message: fmt.Sprintf(
			"数据库尚未升级到本版本（库 revision=%d < 二进制 schemaRevision=%d）：请先停止 gateway/jobs、完成备份，然后运行 `juhe-ai-maintenance upgrade --dry-run --dsn <主库URL>` 预演并按提示执行 upgrade。待执行步骤见 upgrade 计划输出%s",
			dbRevision, binary, missing)}
	case dbRevision > 0 && stepMax == 0:
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

// State 是多节点发布门禁（设计 §5.2）的求值输入。旧库回退路径不构造本结构。
type State struct {
	DBRevision                int64
	StepMax                   int64
	BinaryRevision            int64
	MinRequiredRevision       int64
	MinReadableBinaryRevision int64
	ExpandCompleted           bool
	InterruptedPhase          string
	InterruptedDestructive    bool
}

// EvaluateWithState 实现多节点发布设计 §5.2 求值表：
//
//	0  fresh（库/账本均为 0）              → 旧 fresh 文案
//	1  contract/破坏性中断                 → 所有二进制 fail-fast
//	1a expand 中断（旧二进制可读当前库）   → 旧二进制放行 + WARN；新二进制拒绝
//	2  binary < min_readable_binary        → 库要求更新二进制
//	3  dbRevision < MinRequiredRevision    → 库太旧，先 expand
//	4  dbRevision > 0 且 stepMax == 0      → 账本缺失/被清空
//	5  其余                                 → 通过
func EvaluateWithState(state State) Decision {
	binary := state.BinaryRevision
	if binary == 0 {
		binary = int64(contracts.SchemaRevision)
	}
	minRequired := state.MinRequiredRevision
	if minRequired == 0 {
		minRequired = int64(contracts.MinRequiredRevision)
	}
	if state.DBRevision == 0 && state.StepMax == 0 {
		return Evaluate(0, 0)
	}
	switch {
	case state.InterruptedDestructive || state.InterruptedPhase == "contract":
		return Decision{Message: fmt.Sprintf(
			"上次升级中断在 contract/破坏性步骤（账本 revision=%d，已收敛版本 %d）：旧结构可能已部分删除，所有二进制不得启动；请恢复备份，或重跑 `juhe-ai-maintenance upgrade --phase=contract --dsn <主库URL>` 前向续跑",
			state.StepMax, state.DBRevision)}
	case state.StepMax > state.DBRevision:
		if binary <= state.DBRevision {
			detail := "expand 中断"
			if state.ExpandCompleted {
				detail = "expand 已记账但 schema_state 未推进"
			}
			return Decision{OK: true, Warning: fmt.Sprintf(
				"%s（账本 revision=%d，已收敛版本 %d）：库结构仍兼容本二进制，可继续服务；请重跑 `juhe-ai-maintenance upgrade --phase=expand --dsn <主库URL>` 收尾",
				detail, state.StepMax, state.DBRevision)}
		}
		return Decision{Message: fmt.Sprintf(
			"升级中断在 expand（账本 revision=%d，已收敛版本 %d）：新二进制要求 expand 完成后启动；请重跑 `juhe-ai-maintenance upgrade --phase=expand --dsn <主库URL>` 续跑",
			state.StepMax, state.DBRevision)}
	case binary < state.MinReadableBinaryRevision:
		return Decision{Message: fmt.Sprintf(
			"二进制版本低于库要求的最小可读版本（二进制 schemaRevision=%d < 库要求=%d）：请升级二进制后启动",
			binary, state.MinReadableBinaryRevision)}
	case state.DBRevision < minRequired:
		return Decision{Message: fmt.Sprintf(
			"数据库尚未升级到本版本（库 revision=%d < 二进制最低要求=%d）：请先执行 `juhe-ai-maintenance upgrade --phase=expand --online --dsn <主库URL>`",
			state.DBRevision, minRequired)}
	case state.DBRevision > 0 && state.StepMax == 0:
		return Decision{Message: fmt.Sprintf(
			"升级账本缺失或被清空（state revision=%d 但无账本行）：请恢复备份，或重跑 `juhe-ai-maintenance upgrade --dsn <主库URL>` 重建账本并重新求值全部步骤",
			state.DBRevision)}
	default:
		return Decision{OK: true}
	}
}

// readGateState 读取 schema_state 的新列；旧库缺列时返回 hasNew=false 并由
// Check 回退旧求值。
func readGateState(ctx context.Context, db *sql.DB) (State, bool, error) {
	state := State{
		BinaryRevision:      int64(contracts.SchemaRevision),
		MinRequiredRevision: int64(contracts.MinRequiredRevision),
		ExpandCompleted:     true,
	}
	hasColumn, err := columnReady(ctx, db, "schema_state", "min_readable_binary_revision")
	if err != nil {
		return State{}, false, err
	}
	if !hasColumn {
		revision, err := readStateRevision(ctx, db)
		state.DBRevision = revision
		return state, false, err
	}
	err = db.QueryRowContext(ctx,
		`SELECT schema_revision, min_readable_binary_revision, expand_completed
		 FROM juhe_business.schema_state WHERE id = 1`,
	).Scan(&state.DBRevision, &state.MinReadableBinaryRevision, &state.ExpandCompleted)
	if errors.Is(err, sql.ErrNoRows) {
		return state, true, nil
	}
	if err != nil && isUndefinedTable(err) {
		return state, true, nil
	}
	if err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

// readLedgerState 读取账本最高 revision 及其中断步骤的 phase/destructive；
// 旧库缺 phase 列时 hasPhase=false，由 Check 回退旧求值。
func readLedgerState(ctx context.Context, db *sql.DB) (int64, string, bool, bool, error) {
	ready, err := tableReady(ctx, db, "schema_migrations")
	if err != nil {
		return 0, "", false, false, err
	}
	if !ready {
		return 0, "", false, false, nil
	}
	hasPhase, err := columnReady(ctx, db, "schema_migrations", "phase")
	if err != nil {
		return 0, "", false, false, err
	}
	if !hasPhase {
		stepMax, err := readLedgerMaxRevision(ctx, db)
		return stepMax, "", false, false, err
	}
	var stepMax sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT max(revision) FROM juhe_business.schema_migrations`).Scan(&stepMax); err != nil {
		return 0, "", false, true, err
	}
	if !stepMax.Valid {
		return 0, "", false, true, nil
	}
	var phase string
	var destructive bool
	err = db.QueryRowContext(ctx,
		`SELECT phase, destructive FROM juhe_business.schema_migrations WHERE revision = $1`,
		stepMax.Int64).Scan(&phase, &destructive)
	if errors.Is(err, sql.ErrNoRows) {
		return stepMax.Int64, "", false, true, nil
	}
	if err != nil {
		return 0, "", false, true, err
	}
	return stepMax.Int64, phase, destructive, true, nil
}

func columnReady(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = 'juhe_business' AND table_name = $1 AND column_name = $2`,
		table, column).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
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

// readLedgerMaxRevision 读账本最高 revision；表缺失或无行返回 0。
func readLedgerMaxRevision(ctx context.Context, db *sql.DB) (int64, error) {
	ready, err := tableReady(ctx, db, "schema_migrations")
	if err != nil {
		return 0, err
	}
	if !ready {
		return 0, nil
	}
	var stepMax sql.NullInt64
	err = db.QueryRowContext(ctx,
		`SELECT max(revision) FROM juhe_business.schema_migrations`).Scan(&stepMax)
	if err != nil {
		return 0, err
	}
	return stepMax.Int64, nil
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

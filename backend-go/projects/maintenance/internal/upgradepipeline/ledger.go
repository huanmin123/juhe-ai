// 升级账本读写（升级兼容与迁移账本设计 §7.2）。DDL 权威在
// internal/schema/upgrade_ledger.go；本文件只做行级读写与状态机迁移。
package upgradepipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// MigrationRow 是 schema_migrations 一行的读取投影。
type MigrationRow struct {
	Revision    int64
	StepID      string
	Status      string // running | applied
	Checksum    string
	ImplVersion int
	Adopted     bool
	DetailJSON  string
}

// SchemaState 是 schema_state 单行的读取投影。
type SchemaState struct {
	Revision int64
	Digest   string
	AppVersion string
	ConvergedAt string
}

// EnsureLedger 幂等创建账本两表（幂等 DDL 见 schema 包；upgrade 每次运行先执行）。
func EnsureLedger(ctx context.Context, db *sql.DB) error {
	return ensureLedgerDDL(ctx, db)
}

// ReadSchemaState 读取单行 state；表缺失/无行返回 Revision=0（零值语义，
// §9.4-4）。
func ReadSchemaState(ctx context.Context, db *sql.DB) (SchemaState, error) {
	state := SchemaState{}
	var id int
	err := db.QueryRowContext(ctx,
		`SELECT id, schema_revision, schema_digest, app_version, converged_at FROM juhe_business.schema_state WHERE id = 1`,
	).Scan(&id, &state.Revision, &state.Digest, &state.AppVersion, &state.ConvergedAt)
	if err == sql.ErrNoRows {
		return SchemaState{}, nil
	}
	if err != nil {
		// 表不存在视为未初始化（首次升级）；其余错误上抛。
		if isUndefinedTable(err) {
			return SchemaState{}, nil
		}
		return SchemaState{}, fmt.Errorf("读取 schema_state 失败: %w", err)
	}
	return state, nil
}

// ReadMigrations 读取全部账本行；表缺失返回空 map（首次升级）。
func ReadMigrations(ctx context.Context, db *sql.DB) (map[int64]MigrationRow, error) {
	rows := map[int64]MigrationRow{}
	ready, err := ledgerTableReady(ctx, db)
	if err != nil {
		return nil, err
	}
	if !ready {
		return rows, nil
	}
	result, err := db.QueryContext(ctx,
		`SELECT revision, step_id, status, checksum, impl_version, adopted, COALESCE(detail_json, '')
		 FROM juhe_business.schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("读取 schema_migrations 失败: %w", err)
	}
	defer result.Close()
	for result.Next() {
		var row MigrationRow
		if err := result.Scan(&row.Revision, &row.StepID, &row.Status, &row.Checksum, &row.ImplVersion, &row.Adopted, &row.DetailJSON); err != nil {
			return nil, fmt.Errorf("扫描 schema_migrations 行失败: %w", err)
		}
		rows[row.Revision] = row
	}
	return rows, result.Err()
}

// MarkRunning 写入/改写 running 行（幂等重跑直接覆盖 started_at 与 checksum）。
func MarkRunning(ctx context.Context, db *sql.DB, step Step, appVersion string, started time.Time) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.schema_migrations
  (revision, step_id, title, target, destructive, status, checksum, impl_version, adopted, started_at, app_version)
VALUES ($1, $2, $3, $4, $5, 'running', $6, $7, false, $8, $9)
ON CONFLICT (revision) DO UPDATE SET
  status = 'running', checksum = EXCLUDED.checksum, impl_version = EXCLUDED.impl_version,
  started_at = EXCLUDED.started_at, app_version = EXCLUDED.app_version, detail_json = NULL`,
		step.Revision, step.ID, step.Title, string(step.Target), step.Destructive,
		step.Checksum(), step.ImplVersion, formatTime(started), appVersion)
	if err != nil {
		return fmt.Errorf("写步骤 running 行失败（%s）: %w", step.ID, err)
	}
	return nil
}

// MarkApplied 将 running 行推进为 applied（§7.2 状态机唯一终态）。
func MarkApplied(ctx context.Context, db *sql.DB, step Step, durationMs int64, appVersion string, applied time.Time) error {
	result, err := db.ExecContext(ctx, `
UPDATE juhe_business.schema_migrations
SET status = 'applied', applied_at = $1, duration_ms = $2, app_version = $3, detail_json = NULL
WHERE revision = $4::bigint AND status = 'running'`,
		formatTime(applied), durationMs, appVersion, step.Revision)
	if err != nil {
		return fmt.Errorf("写步骤 applied 行失败（%s）: %w", step.ID, err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("步骤 %s 无 running 行可推进（账本状态机被外部改动）", step.ID)
	}
	return nil
}

// MarkAdopted 记账 adopt（Satisfied 为真，未执行 Apply；impl_version 漂移时
// 重求值后同步更新 impl_version，§7.4）。已存在行走 UPDATE（不翻转历史证据，
// I8 裁决口径），无行则插入。
func MarkAdopted(ctx context.Context, db *sql.DB, step Step, appVersion string, adopted time.Time) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.schema_migrations
  (revision, step_id, title, target, destructive, status, checksum, impl_version, adopted, started_at, applied_at, duration_ms, app_version)
VALUES ($1, $2, $3, $4, $5, 'applied', $6, $7, true, $8, $8, 0, $9)
ON CONFLICT (revision) DO UPDATE SET
  status = 'applied', adopted = true, impl_version = EXCLUDED.impl_version`,
		step.Revision, step.ID, step.Title, string(step.Target), step.Destructive,
		step.Checksum(), step.ImplVersion, formatTime(adopted), appVersion)
	if err != nil {
		return fmt.Errorf("写步骤 adopted 行失败（%s）: %w", step.ID, err)
	}
	return nil
}

// RecordStepFailure 把最近一次失败覆盖写进 running 行的 detail_json（§7.2；
// 历史诊断靠 --report-out 落盘，不进账本）。
func RecordStepFailure(ctx context.Context, db *sql.DB, step Step, detail Detail, applyErr error) error {
	payload := map[string]any{"error": applyErr.Error()}
	if detail != nil {
		payload["detail"] = detail
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(fmt.Sprintf(`{"error":%q,"marshal_error":%q}`, applyErr.Error(), err.Error()))
	}
	_, err = db.ExecContext(ctx,
		`UPDATE juhe_business.schema_migrations SET detail_json = $2 WHERE revision = $1::bigint AND status = 'running'`,
		step.Revision, string(encoded))
	if err != nil {
		return fmt.Errorf("写步骤失败 detail_json 失败（%s）: %w", step.ID, err)
	}
	return nil
}

// WriteSchemaState 在 upgrade 完整跑完 postflight 后写入单行 state（§11.1：
// 只有 upgrade 写；--ensure-schema 不写）。
func WriteSchemaState(ctx context.Context, db *sql.DB, revision int64, digest, appVersion string, converged time.Time) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.schema_state (id, schema_revision, schema_digest, app_version, converged_at)
VALUES (1, $1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE SET
  schema_revision = EXCLUDED.schema_revision, schema_digest = EXCLUDED.schema_digest,
  app_version = EXCLUDED.app_version, converged_at = EXCLUDED.converged_at`,
		revision, digest, appVersion, formatTime(converged))
	if err != nil {
		return fmt.Errorf("写 schema_state 失败: %w", err)
	}
	return nil
}

func formatTime(value time.Time) string {
	// ISO-8601 UTC 毫秒（juhe_business 时间列惯例）。
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

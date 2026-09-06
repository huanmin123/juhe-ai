package manualtestrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// 手动账号测试任务仓储：移植 Node storage/account-test-tasks.repository.ts
// 的 worker 侧任务生命周期面（account_test_task_maintenance / mark_running /
// complete / fail / cancel / update_message），SQL 与 Node 逐字段一致；
// 双模方言差异与 Node 相同（postgres 走原生比较，SQLite 走文本时间与
// 单 writer 串行）。业务表 account_test_tasks / account_test_sessions /
// account_test_session_tasks 由生产迁移所有，本包不建表，只做契约校验入口。

// 任务生命周期常量对齐 Node account-test-tasks.repository.ts。
const (
	taskRetentionHours     = 24
	sessionIdleCompleteMS  = 15_000
	cleanupBatchSize       = 200
	minimumStaleRunningMS  = 60_000
	defaultMaintenanceSize = 200
)

// Config 组装仓储。
type Config struct {
	DB       *sql.DB
	Postgres bool
	Now      func() time.Time
}

// Repo 实现 opsjobs.ManualTestTaskRepo。
type Repo struct {
	db       *sql.DB
	postgres bool
	now      func() time.Time
}

// New 构建仓储；输入校验失败返回错误。
func New(config Config) (*Repo, error) {
	if config.DB == nil {
		return nil, errors.New("manualtestrepo 缺少业务库句柄")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Repo{db: config.DB, postgres: config.Postgres, now: now}, nil
}

// ValidateCoreTables 校验手动测试任务契约表存在（不代建）。
func (r *Repo) ValidateCoreTables(ctx context.Context) error {
	for _, table := range []string{"account_test_tasks", "account_test_sessions", "account_test_session_tasks"} {
		query := "SELECT COUNT(*) FROM " + r.table(table) + " WHERE 1 = 0"
		var count int
		if err := r.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return fmt.Errorf("业务库缺少手动测试任务表 %s: %w", table, err)
		}
	}
	return nil
}

func (r *Repo) table(name string) string {
	if r.postgres {
		return "juhe_business." + name
	}
	return name
}

func (r *Repo) nowIso() string { return r.now().UTC().Format(time.RFC3339Nano) }

func normalizedText(value string, maxLength int) (string, bool) {
	normalized := strings.TrimSpace(value)
	return normalized, normalized != "" && len(normalized) <= maxLength
}

// ---- 任务记录 ----

type taskRow struct {
	id                         string
	accountID                  string
	message                    sql.NullString
	model                      sql.NullString
	testEndpointMode           sql.NullString
	diagnostics                string
	requestSystemAccountID     string
	requestRole                string
	requestSystemAccountFilter sql.NullString
	startedAt                  sql.NullString
	draftAccountEncrypted      sql.NullString
	status                     string
	cancelRequested            bool
}

const taskScanTargets = `
  t.id, t.account_id, t.status_message, t.model, t.test_endpoint_mode, t.diagnostics,
  t.request_system_account_id, t.request_role, t.request_system_account_filter_id,
  t.started_at, t.draft_account_encrypted, t.status, t.cancel_requested`

func scanTaskRow(row interface{ Scan(...any) error }) (*taskRow, error) {
	var scanned taskRow
	err := row.Scan(&scanned.id, &scanned.accountID, &scanned.message, &scanned.model,
		&scanned.testEndpointMode, &scanned.diagnostics, &scanned.requestSystemAccountID,
		&scanned.requestRole, &scanned.requestSystemAccountFilter, &scanned.startedAt,
		&scanned.draftAccountEncrypted, &scanned.status, &scanned.cancelRequested)
	if err != nil {
		return nil, err
	}
	scanned.cancelRequested = databaseBoolean(scanned.cancelRequested)
	return &scanned, nil
}

func databaseBoolean(value bool) bool { return value }

func (row *taskRow) record() *opsjobs.ManualTestTaskRecord {
	record := &opsjobs.ManualTestTaskRecord{
		ID:                     row.id,
		AccountID:              row.accountID,
		Diagnostics:            row.diagnostics,
		RequestSystemAccountID: row.requestSystemAccountID,
		RequestRole:            row.requestRole,
		HasDraftAccount:        row.draftAccountEncrypted.Valid && row.draftAccountEncrypted.String != "",
	}
	if row.draftAccountEncrypted.Valid {
		record.DraftAccountEncrypted = row.draftAccountEncrypted.String
	}
	if row.message.Valid {
		record.Message = row.message.String
	}
	if row.model.Valid {
		record.Model = row.model.String
	}
	if row.testEndpointMode.Valid {
		record.TestEndpointMode = row.testEndpointMode.String
	}
	if row.requestSystemAccountFilter.Valid {
		record.RequestSystemAccountFilterID = row.requestSystemAccountFilter.String
	}
	if row.startedAt.Valid && row.startedAt.String != "" {
		value := row.startedAt.String
		record.StartedAt = &value
	}
	return record
}

func (r *Repo) getTaskRecord(ctx context.Context, q queryContext, id string) (*opsjobs.ManualTestTaskRecord, error) {
	row := q.QueryRowContext(ctx, `
    SELECT `+r.taskScanQualified()+`
    FROM `+r.table("account_test_tasks")+` t
    WHERE t.id = ?
    LIMIT 1`, id)
	scanned, err := scanTaskRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return scanned.record(), nil
}

func (r *Repo) taskScanQualified() string {
	return strings.ReplaceAll(taskScanTargets, "t.", "t.")
}

// queryContext 抽象 *sql.DB 与 *sql.Tx 的查询面。
type queryContext interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// sessionCancelReason 对齐 accountTestTaskSessionCancelReason：任务归属会话
// 的取消/过期理由优先于任务行状态。
func (r *Repo) sessionCancelReason(ctx context.Context, q queryContext, taskID string) (string, error) {
	var (
		status       string
		cancelReason sql.NullString
	)
	err := q.QueryRowContext(ctx, `
    SELECT s.status, s.cancel_reason
    FROM `+r.table("account_test_session_tasks")+` st
    JOIN `+r.table("account_test_sessions")+` s ON s.id = st.session_id
    WHERE st.task_id = ?
    LIMIT 1`, taskID).Scan(&status, &cancelReason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	switch status {
	case "canceled":
		if cancelReason.Valid && cancelReason.String != "" {
			return cancelReason.String, nil
		}
		return "已停止测试", nil
	case "expired":
		if cancelReason.Valid && cancelReason.String != "" {
			return cancelReason.String, nil
		}
		return "账户测试会话已过期", nil
	default:
		if status != "running" {
			if cancelReason.Valid && cancelReason.String != "" {
				return cancelReason.String, nil
			}
			return "账户测试会话已结束", nil
		}
	}
	return "", nil
}

// ---- opsjobs.ManualTestTaskRepo ----

// Maintenance 对齐 handleAccountTestTaskMaintenanceAsync：
// cleanup → idle session 收口 →（sweep）queued 超限自动失败 →（start）
// 中断回收 /（sweep）可运行补充。
func (r *Repo) Maintenance(ctx context.Context, input opsjobs.ManualTestMaintenanceInput) (opsjobs.ManualTestMaintenanceResult, error) {
	if input.Action != "start" && input.Action != "sweep" {
		return opsjobs.ManualTestMaintenanceResult{}, fmt.Errorf("手动测试维护 action 无效: %s", input.Action)
	}
	maxQueuedMS := input.MaxQueuedMS
	if maxQueuedMS < 1 {
		maxQueuedMS = 10 * 60_000
	}
	sweepLimit := input.SweepLimit
	if sweepLimit < 1 {
		sweepLimit = defaultMaintenanceSize
	}
	refillLimit := input.RefillLimit
	if refillLimit < 1 {
		refillLimit = 100
	}
	staleRunningMS := 0
	if input.StaleRunningMS != nil {
		staleRunningMS = int(*input.StaleRunningMS)
	}
	result := opsjobs.ManualTestMaintenanceResult{TaskIDs: []string{}, CanceledTaskIDs: []string{}, ExpiredQueuedTaskIDs: []string{}}
	if err := r.cleanupExpired(ctx); err != nil {
		return result, err
	}
	if _, err := r.completeIdleSessions(ctx, defaultMaintenanceSize); err != nil {
		return result, err
	}
	if input.Action == "sweep" {
		expired, err := r.failExpiredQueued(ctx, maxQueuedMS, sweepLimit)
		if err != nil {
			return result, err
		}
		result.ExpiredQueuedTaskIDs = expired
		taskIDs, err := r.listRunnable(ctx, refillLimit)
		if err != nil {
			return result, err
		}
		result.TaskIDs = taskIDs
		return result, nil
	}
	if staleRunningMS < minimumStaleRunningMS {
		staleRunningMS = minimumStaleRunningMS
	}
	taskIDs, err := r.requeueInterrupted(ctx, staleRunningMS, refillLimit)
	if err != nil {
		return result, err
	}
	result.TaskIDs = taskIDs
	return result, nil
}

// MarkRunning 对齐 markAccountTestTaskRunning（session 取消优先 + queued 原子
// claim + started_at 围栏）。
func (r *Repo) MarkRunning(ctx context.Context, taskID string) (*opsjobs.ManualTestTaskRecord, error) {
	id, ok := normalizedText(taskID, 256)
	if !ok {
		return nil, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	cancelReason, err := r.sessionCancelReason(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if cancelReason != "" {
		if _, err := r.markCanceledTx(ctx, tx, id, cancelReason, nil); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	now := r.nowIso()
	update := `
    UPDATE ` + r.table("account_test_tasks") + `
    SET status = 'running',
        status_message = '后台测试中',
        started_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'queued'
      AND cancel_requested = ` + r.boolFalse()
	result, err := tx.ExecContext(ctx, update, now, now, id)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		var status string
		var cancelRequested bool
		err := tx.QueryRowContext(ctx, `
      SELECT status, cancel_requested FROM `+r.table("account_test_tasks")+` WHERE id = ? LIMIT 1`, id).
			Scan(&status, &cancelRequested)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, tx.Commit()
		}
		if err != nil {
			return nil, err
		}
		if status == "queued" && databaseBoolean(cancelRequested) {
			if _, err := r.markCanceledTx(ctx, tx, id, "已停止测试", nil); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	record, err := r.getTaskRecord(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return record, tx.Commit()
}

// Complete 对齐 completeAccountTestTask（running + cancel_requested=0 +
// started_at 围栏；未命中时按取消收口；result_json 写入结果信封原文）。
func (r *Repo) Complete(ctx context.Context, taskID string, resultValue opsjobs.ManualTestTaskExecutorResult, expectedStartedAt *string) error {
	id, ok := normalizedText(taskID, 256)
	if !ok {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	status := "failed"
	if resultValue.Success {
		status = "success"
	}
	now := r.nowIso()
	fenceSQL, fenceArgs := r.startedAtFence(expectedStartedAt)
	write, err := tx.ExecContext(ctx, `
    UPDATE `+r.table("account_test_tasks")+`
    SET status = ?,
        status_message = ?,
        result_json = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
      AND cancel_requested = `+r.boolFalse()+fenceSQL,
		append([]any{status, resultValue.Message, nullIfEmpty(resultValue.ResultJSON), sqlString(resultValue.Success, resultValue.Message), now, now, id}, fenceArgs...)...)
	if err != nil {
		return err
	}
	changed, err := write.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		if err := r.finalizeIfCanceledTx(ctx, tx, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	return tx.Commit()
}

// Fail 对齐 failAccountTestTask（queued|running → failed；resultJSON 非空时
// 写入 result_json 结果信封，对齐 Node result ? JSON.stringify(result) : null）。
func (r *Repo) Fail(ctx context.Context, taskID string, message string, resultJSON string, expectedStartedAt *string) error {
	id, ok := normalizedText(taskID, 256)
	if !ok {
		return nil
	}
	message, ok = normalizedText(message, 2048)
	if !ok {
		message = "账号测试任务执行失败"
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := r.nowIso()
	fenceSQL, fenceArgs := r.startedAtFence(expectedStartedAt)
	write, err := tx.ExecContext(ctx, `
    UPDATE `+r.table("account_test_tasks")+`
    SET status = 'failed',
        status_message = ?,
        result_json = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status IN ('queued', 'running')
      AND cancel_requested = `+r.boolFalse()+fenceSQL,
		append([]any{message, nullIfEmpty(resultJSON), message, now, now, id}, fenceArgs...)...)
	if err != nil {
		return err
	}
	changed, err := write.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		if err := r.finalizeIfCanceledTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Cancel 对齐 markAccountTestTaskCanceled（queued|running → canceled；保留
// 已有取消消息）。
func (r *Repo) Cancel(ctx context.Context, taskID string, message string, expectedStartedAt *string) error {
	id, ok := normalizedText(taskID, 256)
	if !ok {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := r.markCanceledTx(ctx, tx, id, message, expectedStartedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repo) markCanceledTx(ctx context.Context, tx queryContext, id, message string, expectedStartedAt *string) (bool, error) {
	if _, ok := normalizedText(message, 2048); !ok {
		message = "已停止测试"
	}
	now := r.nowIso()
	fenceSQL, fenceArgs := r.startedAtFence(expectedStartedAt)
	write, err := tx.ExecContext(ctx, `
    UPDATE `+r.table("account_test_tasks")+`
    SET status = 'canceled',
        status_message = CASE
          WHEN cancel_requested = `+r.boolTrue()+` AND status_message IS NOT NULL AND TRIM(status_message) != '' THEN status_message
          ELSE ?
        END,
        cancel_requested = `+r.boolTrue()+`,
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status IN ('queued', 'running')`+fenceSQL,
		append([]any{message, now, now, id}, fenceArgs...)...)
	if err != nil {
		return false, err
	}
	changed, err := write.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed > 0, nil
}

// UpdateMessage 对齐 updateAccountTestTaskMessage（running 进度消息，
// started_at 围栏）。
func (r *Repo) UpdateMessage(ctx context.Context, taskID string, message string, expectedStartedAt *string) error {
	id, ok := normalizedText(taskID, 256)
	if !ok {
		return nil
	}
	normalized, ok := normalizedText(message, 2048)
	if !ok {
		return nil
	}
	now := r.nowIso()
	fenceSQL, fenceArgs := r.startedAtFence(expectedStartedAt)
	_, err := r.db.ExecContext(ctx, `
    UPDATE `+r.table("account_test_tasks")+`
    SET status_message = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
      AND cancel_requested = `+r.boolFalse()+fenceSQL,
		append([]any{normalized, now, id}, fenceArgs...)...)
	return err
}

func (r *Repo) finalizeIfCanceledTx(ctx context.Context, tx queryContext, id string) error {
	record, err := r.getTaskRecord(ctx, tx, id)
	if err != nil || record == nil {
		return err
	}
	var cancelRequested bool
	if err := tx.QueryRowContext(ctx, `SELECT cancel_requested FROM `+r.table("account_test_tasks")+` WHERE id = ?`, id).Scan(&cancelRequested); err != nil {
		return err
	}
	status := ""
	if err := tx.QueryRowContext(ctx, `SELECT status FROM `+r.table("account_test_tasks")+` WHERE id = ?`, id).Scan(&status); err != nil {
		return err
	}
	if databaseBoolean(cancelRequested) && (status == "queued" || status == "running") {
		message := record.Message
		if _, ok := normalizedText(message, 2048); !ok {
			message = "已停止测试"
		}
		_, err := r.markCanceledTx(ctx, tx, id, message, nil)
		return err
	}
	return nil
}

// startedAtFence 返回 started_at 围栏子句与绑定参数（对齐 Node 的
// (? IS NULL OR started_at = ?) 形状，两种驱动统一使用双绑定）。
func (r *Repo) startedAtFence(expectedStartedAt *string) (string, []any) {
	if expectedStartedAt == nil {
		return ` AND (? IS NULL OR started_at = ?)`, []any{nil, nil}
	}
	return ` AND (? IS NULL OR started_at = ?)`, []any{*expectedStartedAt, *expectedStartedAt}
}

func (r *Repo) boolTrue() string {
	if r.postgres {
		return "TRUE"
	}
	return "1"
}

func (r *Repo) boolFalse() string {
	if r.postgres {
		return "FALSE"
	}
	return "0"
}

func sqlString(condition bool, value string) any {
	if condition {
		return nil
	}
	return value
}

// nullIfEmpty 等价 Node result ? JSON.stringify(result) : null 的空信封分支。
func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// ---- maintenance 原语 ----

func (r *Repo) cleanupExpired(ctx context.Context) error {
	cutoff := r.now().Add(-taskRetentionHours * time.Hour).UTC().Format(time.RFC3339Nano)
	tasks := r.table("account_test_tasks")
	sessions := r.table("account_test_sessions")
	sessionTasks := r.table("account_test_session_tasks")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
    DELETE FROM `+tasks+`
    WHERE id IN (
      SELECT id
      FROM `+tasks+`
      WHERE finished_at IS NOT NULL
        AND finished_at < ?
      ORDER BY finished_at ASC, id ASC
      LIMIT ?
    )`, instantParam(r.postgres, cutoff, r.now), cleanupBatchSize); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
    DELETE FROM `+sessions+`
    WHERE id IN (
      SELECT s.id
      FROM `+sessions+` s
      WHERE s.updated_at < ?
        AND NOT EXISTS (
          SELECT 1
          FROM `+sessionTasks+` st
          JOIN `+tasks+` t ON t.id = st.task_id
          WHERE st.session_id = s.id
            AND t.status IN ('queued', 'running')
        )
      ORDER BY s.updated_at ASC, s.id ASC
      LIMIT ?
    )`, instantParam(r.postgres, cutoff, r.now), cleanupBatchSize); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repo) completeIdleSessions(ctx context.Context, limit int) (int, error) {
	cutoff := r.now().Add(-sessionIdleCompleteMS * time.Millisecond).UTC().Format(time.RFC3339Nano)
	sessions := r.table("account_test_sessions")
	sessionTasks := r.table("account_test_session_tasks")
	tasks := r.table("account_test_tasks")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
    SELECT s.id
    FROM `+sessions+` s
    WHERE s.status = 'running'
      AND s.last_heartbeat_at < ?
      AND NOT EXISTS (
        SELECT 1
        FROM `+sessionTasks+` st
        JOIN `+tasks+` t ON t.id = st.task_id
        WHERE st.session_id = s.id
          AND t.status IN ('queued', 'running')
      )
    ORDER BY s.last_heartbeat_at ASC, s.id ASC
    LIMIT ?`, instantParam(r.postgres, cutoff, r.now), maxInt(1, limit))
	if err != nil {
		return 0, err
	}
	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		sessionIDs = append(sessionIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	completed := 0
	now := r.nowIso()
	for _, sessionID := range sessionIDs {
		result, err := tx.ExecContext(ctx, `
      UPDATE `+sessions+`
      SET status = 'completed',
          finished_at = COALESCE(finished_at, ?),
          updated_at = ?
      WHERE id = ?
        AND status = 'running'
        AND NOT EXISTS (
          SELECT 1
          FROM `+sessionTasks+` st
          JOIN `+tasks+` t ON t.id = st.task_id
          WHERE st.session_id = ?
            AND t.status IN ('queued', 'running')
        )`, now, now, sessionID, sessionID)
		if err != nil {
			return completed, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return completed, err
		}
		if changed > 0 {
			completed++
		}
	}
	return completed, tx.Commit()
}

func (r *Repo) listRunnable(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
    SELECT t.id
    FROM `+r.table("account_test_tasks")+` t
    LEFT JOIN `+r.table("account_test_session_tasks")+` st ON st.task_id = t.id
    LEFT JOIN `+r.table("account_test_sessions")+` s ON s.id = st.session_id
    WHERE t.status = 'queued'
      AND (
        st.session_id IS NULL
        OR s.status = 'running'
      )
    ORDER BY t.queued_at ASC, t.id ASC
    LIMIT ?`, maxInt(1, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var taskIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		taskIDs = append(taskIDs, id)
	}
	return taskIDs, rows.Err()
}

func (r *Repo) failExpiredQueued(ctx context.Context, maxQueuedMS int64, limit int) ([]string, error) {
	now := r.now()
	queuedCutoff := now.Add(-time.Duration(maxQueuedMS) * time.Millisecond).UTC().Format(time.RFC3339Nano)
	tasks := r.table("account_test_tasks")
	sessionTasks := r.table("account_test_session_tasks")
	sessions := r.table("account_test_sessions")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
    SELECT t.id
    FROM `+tasks+` t
    LEFT JOIN `+sessionTasks+` st ON st.task_id = t.id
    LEFT JOIN `+sessions+` s ON s.id = st.session_id
    WHERE t.status = 'queued'
      AND t.cancel_requested = `+r.boolFalse()+`
      AND (
        (t.queued_deadline_at IS NOT NULL AND t.queued_deadline_at <= ?)
        OR (t.queued_deadline_at IS NULL AND t.queued_at < ?)
      )
      AND (
        st.session_id IS NULL
        OR s.status = 'running'
      )
    ORDER BY t.queued_at ASC, t.id ASC
    LIMIT ?`, instantParam(r.postgres, r.nowIso(), r.now), instantParam(r.postgres, queuedCutoff, r.now), maxInt(1, limit))
	if err != nil {
		return nil, err
	}
	var taskIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		taskIDs = append(taskIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(taskIDs) == 0 {
		return []string{}, tx.Commit()
	}
	placeholders := placeholdersFor(len(taskIDs))
	args := make([]any, 0, len(taskIDs)+4)
	message := queuedWaitExpiredMessage(maxQueuedMS)
	nowIso := r.nowIso()
	args = append(args, message, message, instantParam(r.postgres, nowIso, r.now), instantParam(r.postgres, nowIso, r.now))
	for _, id := range taskIDs {
		args = append(args, id)
	}
	if _, err := tx.ExecContext(ctx, `
    UPDATE `+tasks+`
    SET status = 'failed',
        status_message = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id IN (`+placeholders+`)
      AND status = 'queued'
      AND cancel_requested = `+r.boolFalse(), args...); err != nil {
		return nil, err
	}
	return taskIDs, tx.Commit()
}

func (r *Repo) requeueInterrupted(ctx context.Context, staleRunningMS int, refillLimit int) ([]string, error) {
	now := r.now()
	staleCutoff := now.Add(-time.Duration(maxInt(minimumStaleRunningMS, staleRunningMS)) * time.Millisecond).UTC().Format(time.RFC3339Nano)
	tasks := r.table("account_test_tasks")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	nowIso := instantParam(r.postgres, r.nowIso(), r.now)
	if _, err := tx.ExecContext(ctx, `
    UPDATE `+tasks+`
    SET status = 'canceled',
        status_message = '已停止测试',
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE status = 'running'
      AND cancel_requested = `+r.boolTrue(), nowIso, nowIso); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
    UPDATE `+tasks+`
    SET status = 'queued',
        status_message = '后台 worker 重启后重新排队',
        started_at = NULL,
        cancel_requested = `+r.boolFalse()+`,
        updated_at = ?
    WHERE status = 'running'
      AND cancel_requested = `+r.boolFalse()+`
      AND updated_at < ?`, nowIso, instantParam(r.postgres, staleCutoff, r.now)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.listRunnable(ctx, refillLimit)
}

// ---- 小工具 ----

func instantParam(postgres bool, value string, now func() time.Time) any {
	if postgres {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
	}
	return value
}

func placeholdersFor(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// queuedWaitExpiredMessage 对齐 accountTestQueuedWaitExpiredMessage（文案逐字一致）。
func queuedWaitExpiredMessage(maxQueuedMS int64) string {
	return fmt.Sprintf("后台测试队列等待超过 %s，任务已自动收口；请检查运维 worker 或降低批量并发", formatQueuedWait(maxQueuedMS))
}

func formatQueuedWait(maxQueuedMS int64) string {
	seconds := (maxQueuedMS + 999) / 1000
	if seconds < 1 {
		seconds = 1
	}
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := (seconds + 59) / 60
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	hours := (minutes + 59) / 60
	return fmt.Sprintf("%d 小时", hours)
}

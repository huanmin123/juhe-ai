package auditlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var nonPersistedTrafficSources = []string{"account_health_check", "runtime_recovery_probe", "cooldown_retest"}

// CleanupRetention performs one bounded, owner-fenced retention pass. Child
// rows are removed before audit_logs, and blob metadata/files are considered
// only after all payload references have been removed.
func (s *sqlStore) CleanupRetention(ctx context.Context, lease OwnerLease, config RetentionConfig) (RetentionResult, error) {
	config, err := normalizeRetentionConfig(config)
	if err != nil {
		return RetentionResult{}, err
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return RetentionResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("开始 F3 retention 事务失败: %w", err)
	}
	defer tx.Rollback()
	if err := s.verifyLeaseTx(ctx, tx, lease); err != nil {
		return RetentionResult{}, err
	}

	result := RetentionResult{}
	candidateBlobIDs := make([]string, 0)
	appendCandidates := func(ids []string) { candidateBlobIDs = append(candidateBlobIDs, ids...) }

	trimWhere := `audit_outcome = 'success' AND created_at < ? AND created_at >= ? AND sample_bucket >= ? AND capture_status <> 'metadata_only'`
	trimArgs := []any{config.SuccessHotCutoff, config.SuccessCutoff, config.SuccessSampleBucketThreshold}
	trimIDs, err := s.retentionIDs(ctx, tx, trimWhere, trimArgs, config.BatchSize)
	if err != nil {
		return RetentionResult{}, err
	}
	trimIDs, err = s.lockAndRevalidateRetentionAuditIDs(ctx, tx, trimIDs, trimWhere, trimArgs)
	if err != nil {
		return RetentionResult{}, err
	}
	ids, blobs, err := s.deleteRetentionChildren(ctx, tx, trimIDs, false)
	if err != nil {
		return RetentionResult{}, err
	}
	appendCandidates(blobs)
	if len(ids) > 0 {
		result.SuccessHotTrimmed = int64(len(ids))
		placeholders := placeholders(len(ids))
		query := `UPDATE ` + s.table("audit_logs") + ` SET attempt_count=0,payload_count=0,raw_payload_bytes=0,compressed_payload_bytes=0,compression_saved_bytes=0,capture_status='metadata_only' WHERE id IN (` + placeholders + `) AND capture_status <> 'metadata_only'`
		updated, err := tx.ExecContext(ctx, s.bind(query), stringArgs(ids)...)
		if err != nil {
			return RetentionResult{}, fmt.Errorf("标记 F3 success audit metadata-only 失败: %w", err)
		}
		if count, countErr := updated.RowsAffected(); countErr == nil {
			result.SuccessHotTrimmed = count
		}
	}

	// These sources are explicitly outside persisted audit scope in the Node
	// baseline, so any rows left by an old writer are removed regardless of age.
	nonPersistedWhere := `traffic_source IN (` + placeholders(len(nonPersistedTrafficSources)) + `)`
	nonPersistedArgs := stringAny(nonPersistedTrafficSources)
	nonPersistedIDs, err := s.retentionIDs(ctx, tx, nonPersistedWhere, nonPersistedArgs, config.BatchSize)
	if err != nil {
		return RetentionResult{}, err
	}
	nonPersistedIDs, err = s.lockAndRevalidateRetentionAuditIDs(ctx, tx, nonPersistedIDs, nonPersistedWhere, nonPersistedArgs)
	if err != nil {
		return RetentionResult{}, err
	}
	_, blobs, err = s.deleteRetentionChildren(ctx, tx, nonPersistedIDs, true)
	if err != nil {
		return RetentionResult{}, err
	}
	appendCandidates(blobs)
	if deleted, err := s.deleteAuditLogRows(ctx, tx, nonPersistedIDs); err != nil {
		return RetentionResult{}, err
	} else {
		result.DeletedNonPersistedLogs = int64(deleted)
	}

	deleteWhere := `((audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`
	deleteArgs := []any{config.SuccessCutoff, config.FailureCutoff}
	deleteIDs, err := s.retentionIDs(ctx, tx, deleteWhere, deleteArgs, config.BatchSize)
	if err != nil {
		return RetentionResult{}, err
	}
	deleteIDs, err = s.lockAndRevalidateRetentionAuditIDs(ctx, tx, deleteIDs, deleteWhere, deleteArgs)
	if err != nil {
		return RetentionResult{}, err
	}
	_, blobs, err = s.deleteRetentionChildren(ctx, tx, deleteIDs, true)
	if err != nil {
		return RetentionResult{}, err
	}
	appendCandidates(blobs)
	if deleted, err := s.deleteAuditLogRows(ctx, tx, deleteIDs); err != nil {
		return RetentionResult{}, err
	} else {
		result.DeletedLogs = int64(deleted)
	}

	groupIDs, err := s.retentionIDsFromTable(ctx, tx, "audit_error_groups", `updated_at < ? AND NOT EXISTS (SELECT 1 FROM `+s.table("audit_logs")+` WHERE `+s.table("audit_logs")+`.error_group_id = `+s.table("audit_error_groups")+`.id)`, []any{config.ErrorGroupCutoff}, config.BatchSize)
	if err != nil {
		return RetentionResult{}, err
	}
	if len(groupIDs) > 0 {
		query := `DELETE FROM ` + s.table("audit_error_groups") + ` WHERE id IN (` + placeholders(len(groupIDs)) + `) AND updated_at < ? AND NOT EXISTS (SELECT 1 FROM ` + s.table("audit_logs") + ` WHERE ` + s.table("audit_logs") + `.error_group_id = ` + s.table("audit_error_groups") + `.id)`
		deleted, err := tx.ExecContext(ctx, s.bind(query), append(stringArgs(groupIDs), config.ErrorGroupCutoff)...)
		if err != nil {
			return RetentionResult{}, fmt.Errorf("删除 F3 error groups 失败: %w", err)
		}
		if count, countErr := deleted.RowsAffected(); countErr == nil {
			result.DeletedErrorGroups = count
		}
	}

	// Candidate IDs are retained only when no reference remains. The query also
	// catches pre-existing orphan metadata so a previous failed pass converges.
	candidateBlobIDs = uniqueStrings(candidateBlobIDs)
	blobRows, err := s.unreferencedBlobRows(ctx, tx, candidateBlobIDs, config.BatchSize)
	if err != nil {
		return RetentionResult{}, err
	}
	if err := s.scheduleUnreferencedBlobGC(ctx, tx, blobRows); err != nil {
		return RetentionResult{}, err
	}

	s.hotMu.Lock()
	deletedHotFiles, err := s.cleanupHotSearchFilesBefore(config.SuccessHotCutoff, config.BatchSize)
	s.hotMu.Unlock()
	if err != nil {
		return RetentionResult{}, err
	}
	result.DeletedHotSearchFiles = deletedHotFiles
	if err := s.verifyLeaseBeforeCommit(ctx, tx, lease); err != nil {
		return RetentionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RetentionResult{}, fmt.Errorf("提交 F3 retention 事务失败: %w", err)
	}
	deletedBlobs, err := s.cleanupScheduledBlobFiles(ctx, lease, config.BatchSize)
	result.DeletedPayloadBlobs += deletedBlobs
	if err != nil {
		return result, err
	}
	deletedOrphans, err := s.reconcileOrphanBlobFiles(ctx, lease, config.BatchSize)
	result.DeletedOrphanBlobFiles = deletedOrphans
	if err != nil {
		return result, err
	}
	return result, nil
}

// Retain is a concise alias used by maintenance callers.
func (s *sqlStore) Retain(ctx context.Context, lease OwnerLease, config RetentionConfig) (RetentionResult, error) {
	return s.CleanupRetention(ctx, lease, config)
}

type retentionBlobRow struct{ id, storageKey string }
type pendingBlobGCRow struct{ blobID, storageKey string }

func normalizeRetentionConfig(config RetentionConfig) (RetentionConfig, error) {
	if config.SuccessHotCutoff.IsZero() || config.SuccessCutoff.IsZero() || config.FailureCutoff.IsZero() || config.ErrorGroupCutoff.IsZero() {
		return RetentionConfig{}, fmt.Errorf("F3 retention 必须明确 success hot/success/failure/error-group cutoff")
	}
	if config.SuccessHotCutoff.Before(config.SuccessCutoff) {
		return RetentionConfig{}, fmt.Errorf("success hot cutoff 不得早于 success cutoff")
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 1000
	}
	if config.BatchSize > 5096 {
		config.BatchSize = 5096
	}
	if config.SuccessSampleBucketThreshold < 0 {
		config.SuccessSampleBucketThreshold = 0
	}
	if config.SuccessSampleBucketThreshold > 10000 {
		config.SuccessSampleBucketThreshold = 10000
	}
	return config, nil
}

func (s *sqlStore) retentionIDs(ctx context.Context, tx *sql.Tx, where string, args []any, limit int) ([]string, error) {
	return s.retentionIDsFromTable(ctx, tx, "audit_logs", where, args, limit)
}

// lockAndRevalidateRetentionAuditIDs makes a retention candidate current only
// after it owns the same audit-ID lifecycle gate as Persist. A delayed
// finalized input can therefore replace an in-progress candidate before the
// retention transaction commits, but it cannot be deleted by that stale
// candidate after the lock is acquired.
func (s *sqlStore) lockAndRevalidateRetentionAuditIDs(ctx context.Context, tx *sql.Tx, ids []string, where string, args []any) ([]string, error) {
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	if err := s.lockAuditLogLifecycleIDs(ctx, tx, ids); err != nil {
		return nil, err
	}
	query := `SELECT id FROM ` + s.table("audit_logs") + ` WHERE id IN (` + placeholders(len(ids)) + `) AND (` + where + `) ORDER BY id ASC`
	arguments := append(stringArgs(ids), args...)
	rows, err := tx.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return nil, fmt.Errorf("复核 F3 retention audit IDs 失败: %w", err)
	}
	defer rows.Close()
	matched := make([]string, 0, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			matched = append(matched, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return matched, nil
}

func (s *sqlStore) retentionIDsFromTable(ctx context.Context, tx *sql.Tx, tableName, where string, args []any, limit int) ([]string, error) {
	orderColumn := "created_at"
	if tableName == "audit_error_groups" {
		orderColumn = "updated_at"
	}
	query := `SELECT id FROM ` + s.table(tableName) + ` WHERE ` + where + ` ORDER BY ` + orderColumn + ` ASC,id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("读取 F3 retention IDs 失败: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *sqlStore) deleteRetentionChildren(ctx context.Context, tx *sql.Tx, ids []string, _ bool) ([]string, []string, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	if err := s.lockAuditLogLifecycleIDs(ctx, tx, ids); err != nil {
		return nil, nil, err
	}
	pl := placeholders(len(ids))
	query := `SELECT headers_blob_id,body_blob_id FROM ` + s.table("audit_payload_refs") + ` WHERE audit_log_id IN (` + pl + `)`
	rows, err := tx.QueryContext(ctx, s.bind(query), stringArgs(ids)...)
	if err != nil {
		return nil, nil, err
	}
	blobIDs := make([]string, 0)
	for rows.Next() {
		var headers, body sql.NullString
		if err := rows.Scan(&headers, &body); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if headers.Valid {
			blobIDs = append(blobIDs, headers.String)
		}
		if body.Valid {
			blobIDs = append(blobIDs, body.String)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_payload_refs")+` WHERE audit_log_id IN (`+pl+`)`), stringArgs(ids)...); err != nil {
		return nil, nil, fmt.Errorf("删除 F3 payload refs 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_log_attempts")+` WHERE audit_log_id IN (`+pl+`)`), stringArgs(ids)...); err != nil {
		return nil, nil, fmt.Errorf("删除 F3 attempts 失败: %w", err)
	}
	return ids, uniqueStrings(blobIDs), nil
}

func (s *sqlStore) deleteAuditLogRows(ctx context.Context, tx *sql.Tx, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.lockAuditLogLifecycleIDs(ctx, tx, ids); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_logs")+` WHERE id IN (`+placeholders(len(ids))+`)`), stringArgs(ids)...)
	if err != nil {
		return 0, fmt.Errorf("删除 F3 audit_logs 失败: %w", err)
	}
	return result.RowsAffected()
}

func (s *sqlStore) unreferencedBlobRows(ctx context.Context, tx *sql.Tx, ids []string, limit int) ([]retentionBlobRow, error) {
	query := `SELECT id,storage_key FROM ` + s.table("audit_payload_blobs") + ` WHERE NOT EXISTS (SELECT 1 FROM ` + s.table("audit_payload_refs") + ` WHERE headers_blob_id = ` + s.table("audit_payload_blobs") + `.id OR body_blob_id = ` + s.table("audit_payload_blobs") + `.id)`
	args := []any{}
	if len(ids) > 0 {
		query += ` AND id IN (` + placeholders(len(ids)) + `)`
		args = append(args, stringArgs(ids)...)
	}
	// This is only a candidate snapshot. PostgreSQL blob lifecycle operations
	// must acquire the per-blob advisory lock before a blob row lock; the
	// schedule phase below rechecks the candidate under FOR UPDATE after that
	// advisory lock is held.
	query += ` ORDER BY created_at ASC,id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("查询 F3 unreferenced blobs 失败: %w", err)
	}
	defer rows.Close()
	result := make([]retentionBlobRow, 0)
	for rows.Next() {
		var row retentionBlobRow
		if err := rows.Scan(&row.id, &row.storageKey); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// blobRowsByID loads storage keys for explicitly known blob ids so callers
// can reuse scheduleUnreferencedBlobGC's per-blob lock + NOT EXISTS recheck
// path outside a retention sweep.
func (s *sqlStore) blobRowsByID(ctx context.Context, tx *sql.Tx, ids []string) ([]retentionBlobRow, error) {
	query := `SELECT id,storage_key FROM ` + s.table("audit_payload_blobs") + ` WHERE id IN (` + placeholders(len(ids)) + `)`
	rows, err := tx.QueryContext(ctx, s.bind(query), stringArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("查询 F3 blob 元数据失败: %w", err)
	}
	defer rows.Close()
	result := make([]retentionBlobRow, 0, len(ids))
	for rows.Next() {
		var row retentionBlobRow
		if err := rows.Scan(&row.id, &row.storageKey); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// scheduleUnreferencedBlobGC commits a durable cleanup intent before any
// physical file mutation. PostgreSQL uses the same per-blob advisory lock as
// Persist, so a new reference either cancels this intent or waits until the
// old file and metadata are retired.
func (s *sqlStore) scheduleUnreferencedBlobGC(ctx context.Context, tx *sql.Tx, rows []retentionBlobRow) error {
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	for _, row := range rows {
		if err := s.lockBlobLifecycleTx(ctx, tx, row.id); err != nil {
			return err
		}
		query := `SELECT storage_key FROM ` + s.table("audit_payload_blobs") + ` WHERE id=? AND NOT EXISTS (SELECT 1 FROM ` + s.table("audit_payload_refs") + ` WHERE headers_blob_id=? OR body_blob_id=?) FOR UPDATE`
		var storageKey string
		err := tx.QueryRowContext(ctx, s.bind(query), row.id, row.id, row.id).Scan(&storageKey)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("复核 F3 unreferenced blob 失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("audit_payload_blob_gc")+` (blob_id,storage_key,scheduled_at) VALUES (?,?,?) ON CONFLICT(blob_id) DO UPDATE SET storage_key=excluded.storage_key,scheduled_at=excluded.scheduled_at`), row.id, storageKey, time.Now().UTC()); err != nil {
			return fmt.Errorf("记录 F3 pending blob GC 失败: %w", err)
		}
	}
	return nil
}

func (s *sqlStore) cleanupScheduledBlobFiles(ctx context.Context, lease OwnerLease, limit int) (int64, error) {
	query := `SELECT blob_id,storage_key FROM ` + s.table("audit_payload_blob_gc") + ` ORDER BY scheduled_at ASC,blob_id ASC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), limit)
	if err != nil {
		return 0, fmt.Errorf("读取 F3 pending blob GC 失败: %w", err)
	}
	deferred := make([]pendingBlobGCRow, 0)
	for rows.Next() {
		var row pendingBlobGCRow
		if err := rows.Scan(&row.blobID, &row.storageKey); err != nil {
			rows.Close()
			return 0, fmt.Errorf("读取 F3 pending blob GC 行失败: %w", err)
		}
		deferred = append(deferred, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("遍历 F3 pending blob GC 失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("关闭 F3 pending blob GC 查询失败: %w", err)
	}

	var deleted int64
	for _, row := range deferred {
		removed, err := s.cleanupScheduledBlobFile(ctx, lease, row)
		if err != nil {
			return deleted, err
		}
		if removed {
			deleted++
		}
	}
	return deleted, nil
}

func (s *sqlStore) cleanupScheduledBlobFile(ctx context.Context, lease OwnerLease, pending pendingBlobGCRow) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("开始 F3 pending blob GC 事务失败: %w", err)
	}
	defer tx.Rollback()
	if err := s.verifyLeaseTx(ctx, tx, lease); err != nil {
		return false, err
	}
	if err := s.lockBlobLifecycleTx(ctx, tx, pending.blobID); err != nil {
		return false, err
	}

	var storageKey string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT storage_key FROM `+s.table("audit_payload_blob_gc")+` WHERE blob_id=?`), pending.blobID).Scan(&storageKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取 F3 pending blob GC 状态失败: %w", err)
	}

	var referenced bool
	err = tx.QueryRowContext(ctx, s.bind(`SELECT EXISTS (SELECT 1 FROM `+s.table("audit_payload_refs")+` WHERE headers_blob_id=? OR body_blob_id=?)`), pending.blobID, pending.blobID).Scan(&referenced)
	if err != nil {
		return false, fmt.Errorf("检查 F3 pending blob 引用失败: %w", err)
	}
	if referenced {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_payload_blob_gc")+` WHERE blob_id=?`), pending.blobID); err != nil {
			return false, fmt.Errorf("取消已重新引用的 F3 pending blob GC 失败: %w", err)
		}
		if err := s.verifyLeaseBeforeCommit(ctx, tx, lease); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("提交 F3 pending blob GC 取消失败: %w", err)
		}
		return false, nil
	}

	var exists bool
	err = tx.QueryRowContext(ctx, s.bind(`SELECT EXISTS (SELECT 1 FROM `+s.table("audit_payload_blobs")+` WHERE id=?)`), pending.blobID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("检查 F3 pending blob 元数据失败: %w", err)
	}
	if err := removeBlobFile(s.blobDir, storageKey); err != nil {
		// The transaction rolls back and keeps this durable locator for a later
		// retention pass. Do not delete metadata after a failed file operation.
		return false, err
	}
	if exists {
		deleted, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_payload_blobs")+` WHERE id=? AND NOT EXISTS (SELECT 1 FROM `+s.table("audit_payload_refs")+` WHERE headers_blob_id=? OR body_blob_id=?)`), pending.blobID, pending.blobID, pending.blobID)
		if err != nil {
			return false, fmt.Errorf("删除 F3 pending blob 元数据失败: %w", err)
		}
		if affected, affectedErr := deleted.RowsAffected(); affectedErr != nil {
			return false, fmt.Errorf("读取 F3 pending blob 删除结果失败: %w", affectedErr)
		} else if affected != 1 {
			return false, fmt.Errorf("F3 pending blob 在删除期间重新获得引用: id=%s", pending.blobID)
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_payload_blob_gc")+` WHERE blob_id=?`), pending.blobID); err != nil {
		return false, fmt.Errorf("清理 F3 pending blob GC 状态失败: %w", err)
	}
	if err := s.verifyLeaseBeforeCommit(ctx, tx, lease); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("提交 F3 pending blob GC 事务失败；物理文件已删除，待清理状态将重试: %w", err)
	}
	return exists, nil
}

// orphanBlobSafetyWindow 是 FS→DB 孤儿对账的安全窗口：窗口内的 canonical 文件
// 可能正处在并发 Persist 的 publish→commit 之间（或 commit 结果未知的等待期），
// 一律跳过。窗口远大于单次 Persist 的发布间隔与 owner 租约 TTL。
const orphanBlobSafetyWindow = time.Hour

// orphanBlobCandidate 是一轮对账初筛出的孤儿文件候选：文件名反推的
// content-addressed blob id（供 per-blob advisory lock 与 Persist 对齐）、
// slash 形态 storage_key（供反查表行与删除）与磁盘绝对路径。
type orphanBlobCandidate struct{ blobID, storageKey, path string }

// reconcileOrphanBlobFiles 是读侧周期性孤儿对账（W47）：`Persist` 的
// 「先发布文件、后提交事务」顺序在失败臂与进程崩溃下留下磁盘有文件、
// audit_payload_blobs 无行的孤儿，引用感知清理的候选集对它们结构性不可见。
// 本函数遍历 `<blobDir>/sha256/**` 下的 canonical 文件，按 storage_key 反查
// 表行；表中无行且 mtime 早于安全窗口的文件逐条走与既有 GC 相同的
// per-blob advisory lock + 复核路径删除。`.f3-audit-blob-*-*.tmp` 临时文件
// 不匹配 canonical 文件名，永不进入候选（由 CleanupOwnedBlobTemps/
// CleanupOrphanedBlobTemps 负责）。
//
// 单轮有界：最多检查 limit 个 canonical 文件（含因有行或窗口内被跳过的），
// 遍历按目录顺序推进，游标记录本轮最后检查的文件，单轮有剩余时下一轮从其后
// 继续；走完整棵树后游标回卷到起点，保证任何文件都会被周期性重新检查。
func (s *sqlStore) reconcileOrphanBlobFiles(ctx context.Context, lease OwnerLease, limit int) (int64, error) {
	s.orphanMu.Lock()
	defer s.orphanMu.Unlock()
	cursor := s.orphanCursor
	cutoff := time.Now().UTC().Add(-orphanBlobSafetyWindow)
	examined, lastExamined := 0, ""
	exhausted := true
	candidates := make([]orphanBlobCandidate, 0, limit)
	walkErr := filepath.WalkDir(filepath.Join(s.blobDir, "sha256"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		digest, contentTypeHash, ok := parseCanonicalBlobFileName(entry.Name())
		if !ok {
			return nil
		}
		relative, err := filepath.Rel(s.blobDir, path)
		if err != nil {
			return err
		}
		storageKey := filepath.ToSlash(relative)
		if storageKey <= cursor {
			return nil
		}
		if examined == limit {
			exhausted = false
			return fs.SkipAll
		}
		examined++
		lastExamined = storageKey
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			return nil
		}
		candidates = append(candidates, orphanBlobCandidate{
			blobID:     "blob:" + digest + ":" + contentTypeHash,
			storageKey: storageKey,
			path:       path,
		})
		return nil
	})
	if errors.Is(walkErr, os.ErrNotExist) {
		walkErr = nil
	}
	if walkErr != nil {
		return 0, fmt.Errorf("遍历 F3 audit blob 目录失败: %w", walkErr)
	}
	if exhausted {
		s.orphanCursor = ""
	} else {
		s.orphanCursor = lastExamined
	}

	existing := make(map[string]struct{}, len(candidates))
	if len(candidates) > 0 {
		keys := make([]string, len(candidates))
		for index, candidate := range candidates {
			keys[index] = candidate.storageKey
		}
		query := `SELECT storage_key FROM ` + s.table("audit_payload_blobs") + ` WHERE storage_key IN (` + placeholders(len(keys)) + `)`
		rows, err := s.db.QueryContext(ctx, s.bind(query), stringArgs(keys)...)
		if err != nil {
			return 0, fmt.Errorf("反查 F3 孤儿 blob 候选元数据失败: %w", err)
		}
		for rows.Next() {
			var storageKey string
			if err := rows.Scan(&storageKey); err != nil {
				rows.Close()
				return 0, err
			}
			existing[storageKey] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, fmt.Errorf("遍历 F3 孤儿 blob 候选元数据失败: %w", err)
		}
		if err := rows.Close(); err != nil {
			return 0, fmt.Errorf("关闭 F3 孤儿 blob 候选元数据查询失败: %w", err)
		}
	}

	var deleted int64
	for _, candidate := range candidates {
		if _, ok := existing[candidate.storageKey]; ok {
			continue
		}
		removed, err := s.deleteOrphanBlobFile(ctx, lease, candidate, cutoff)
		if err != nil {
			return deleted, err
		}
		if removed {
			deleted++
		}
	}
	return deleted, nil
}

// deleteOrphanBlobFile 对单个初筛通过的孤儿候选走与既有 GC 相同的
// lease-fenced 删除路径：per-blob advisory lock 持有期间复核表中确实无行、
// 文件仍存在且仍处于安全窗口外，然后删除物理文件。任何一步不满足或失败时
// 整事务回滚且不计删除，文件留给下一轮对账复核。
func (s *sqlStore) deleteOrphanBlobFile(ctx context.Context, lease OwnerLease, candidate orphanBlobCandidate, cutoff time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("开始 F3 孤儿 blob 对账事务失败: %w", err)
	}
	defer tx.Rollback()
	if err := s.verifyLeaseTx(ctx, tx, lease); err != nil {
		return false, err
	}
	if err := s.lockBlobLifecycleTx(ctx, tx, candidate.blobID); err != nil {
		return false, err
	}
	var exists bool
	err = tx.QueryRowContext(ctx, s.bind(`SELECT EXISTS (SELECT 1 FROM `+s.table("audit_payload_blobs")+` WHERE storage_key=?)`), candidate.storageKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("复核 F3 孤儿 blob 元数据失败: %w", err)
	}
	if exists {
		return false, nil
	}
	info, err := os.Stat(candidate.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("复核 F3 孤儿 blob 文件失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
		return false, nil
	}
	if err := removeBlobFile(s.blobDir, candidate.storageKey); err != nil {
		return false, err
	}
	if err := s.verifyLeaseBeforeCommit(ctx, tx, lease); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("提交 F3 孤儿 blob 对账事务失败: %w", err)
	}
	return true, nil
}

// parseCanonicalBlobFileName 解析 sha256 目录下的 canonical blob 文件名
// （<64 位 sha256>-<16 位 contentType 短哈希>.blob|.gz），返回反推 blob id
// 所需的两段十六进制。`.f3-audit-blob-*-*.tmp` 临时文件与其它杂项文件不匹配
// 该形态，返回 false。
func parseCanonicalBlobFileName(name string) (digest, contentTypeHash string, ok bool) {
	base := strings.TrimSuffix(name, ".blob")
	if base == name {
		base = strings.TrimSuffix(name, ".gz")
		if base == name {
			return "", "", false
		}
	}
	digest, contentTypeHash, found := strings.Cut(base, "-")
	if !found || len(digest) != 64 || len(contentTypeHash) != 16 || !isHexDigits(digest) || !isHexDigits(contentTypeHash) {
		return "", "", false
	}
	return digest, contentTypeHash, true
}

func isHexDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		case character >= 'A' && character <= 'F':
		default:
			return false
		}
	}
	return true
}

func removeBlobFile(root, storageKey string) error {
	if strings.TrimSpace(storageKey) == "" {
		return nil
	}
	path, err := blobFilePath(root, storageKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除 F3 audit blob 文件失败: %w", err)
	}
	return nil
}

func blobFileMatchesMetadata(root, storageKey string, size int64) (bool, error) {
	path, err := blobFilePath(root, storageKey)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查 F3 audit blob 文件失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, fmt.Errorf("F3 audit blob 文件与元数据不一致: %s", path)
	}
	return true, nil
}

func blobFilePath(root, storageKey string) (string, error) {
	if strings.TrimSpace(storageKey) == "" {
		return "", fmt.Errorf("audit blob storage_key 不能为空")
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(cleanRoot, filepath.FromSlash(storageKey))
	relative, err := filepath.Rel(cleanRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("audit blob storage_key 越界: %q", storageKey)
	}
	return path, nil
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	parts := make([]string, count)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}
func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}
func stringAny(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

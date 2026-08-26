package auditlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
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
	trimArgs := []any{dbTime(s.mode, config.SuccessHotCutoff), dbTime(s.mode, config.SuccessCutoff), config.SuccessSampleBucketThreshold}
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
	deleteArgs := []any{dbTime(s.mode, config.SuccessCutoff), dbTime(s.mode, config.FailureCutoff)}
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

	groupIDs, err := s.retentionIDsFromTable(ctx, tx, "audit_error_groups", `updated_at < ? AND NOT EXISTS (SELECT 1 FROM `+s.table("audit_logs")+` WHERE `+s.table("audit_logs")+`.error_group_id = `+s.table("audit_error_groups")+`.id)`, []any{dbTime(s.mode, config.ErrorGroupCutoff)}, config.BatchSize)
	if err != nil {
		return RetentionResult{}, err
	}
	if len(groupIDs) > 0 {
		query := `DELETE FROM ` + s.table("audit_error_groups") + ` WHERE id IN (` + placeholders(len(groupIDs)) + `) AND updated_at < ? AND NOT EXISTS (SELECT 1 FROM ` + s.table("audit_logs") + ` WHERE ` + s.table("audit_logs") + `.error_group_id = ` + s.table("audit_error_groups") + `.id)`
		deleted, err := tx.ExecContext(ctx, s.bind(query), append(stringArgs(groupIDs), dbTime(s.mode, config.ErrorGroupCutoff))...)
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
		query := `SELECT storage_key FROM ` + s.table("audit_payload_blobs") + ` WHERE id=? AND NOT EXISTS (SELECT 1 FROM ` + s.table("audit_payload_refs") + ` WHERE headers_blob_id=? OR body_blob_id=?)`
		if s.mode == ModePostgres {
			query += ` FOR UPDATE`
		}
		var storageKey string
		err := tx.QueryRowContext(ctx, s.bind(query), row.id, row.id, row.id).Scan(&storageKey)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("复核 F3 unreferenced blob 失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("audit_payload_blob_gc")+` (blob_id,storage_key,scheduled_at) VALUES (?,?,?) ON CONFLICT(blob_id) DO UPDATE SET storage_key=excluded.storage_key,scheduled_at=excluded.scheduled_at`), row.id, storageKey, dbTime(s.mode, time.Now().UTC())); err != nil {
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

package auditlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	trimIDs, err := s.retentionIDs(ctx, tx, `audit_outcome = 'success' AND created_at < ? AND created_at >= ? AND sample_bucket >= ? AND capture_status <> 'metadata_only'`, []any{dbTime(s.mode, config.SuccessHotCutoff), dbTime(s.mode, config.SuccessCutoff), config.SuccessSampleBucketThreshold}, config.BatchSize)
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
	nonPersistedIDs, err := s.retentionIDs(ctx, tx, `traffic_source IN (`+placeholders(len(nonPersistedTrafficSources))+`)`, stringAny(nonPersistedTrafficSources), config.BatchSize)
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

	deleteIDs, err := s.retentionIDs(ctx, tx, `((audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`, []any{dbTime(s.mode, config.SuccessCutoff), dbTime(s.mode, config.FailureCutoff)}, config.BatchSize)
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
	for _, row := range blobRows {
		if err := removeBlobFile(s.blobDir, row.storageKey); err != nil {
			return RetentionResult{}, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_payload_blobs")+` WHERE id=? AND NOT EXISTS (SELECT 1 FROM `+s.table("audit_payload_refs")+` WHERE headers_blob_id=? OR body_blob_id=?)`), row.id, row.id, row.id); err != nil {
			return RetentionResult{}, fmt.Errorf("删除 F3 unreferenced blob metadata 失败: %w", err)
		}
		result.DeletedPayloadBlobs++
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
	return result, nil
}

// Retain is a concise alias used by maintenance callers.
func (s *sqlStore) Retain(ctx context.Context, lease OwnerLease, config RetentionConfig) (RetentionResult, error) {
	return s.CleanupRetention(ctx, lease, config)
}

type retentionBlobRow struct{ id, storageKey string }

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
	if config.BatchSize > 10000 {
		config.BatchSize = 10000
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

func removeBlobFile(root, storageKey string) error {
	if strings.TrimSpace(storageKey) == "" {
		return nil
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path := filepath.Join(cleanRoot, filepath.FromSlash(storageKey))
	relative, err := filepath.Rel(cleanRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("audit blob storage_key 越界: %q", storageKey)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除 F3 audit blob 文件失败: %w", err)
	}
	return nil
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

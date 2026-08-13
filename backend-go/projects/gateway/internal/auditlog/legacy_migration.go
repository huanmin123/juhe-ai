package auditlog

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlitepath"

	_ "modernc.org/sqlite"
)

// LegacyMigrationOptions describes an explicit, offline copy from the old
// Node SQLite dataset into the dedicated Go audit SQLite database.
// Both stop gates are mandatory; this function never starts or stops either
// runtime and is never called by the normal application startup path.
type LegacyMigrationOptions struct {
	SourceDatabasePath  string
	TargetDatabasePath  string
	SourceBlobDirectory string
	TargetBlobDirectory string
	NodeStopped         bool
	GoStopped           bool
}

type LegacyMigrationResult struct {
	NoOp        bool             `json:"noOp"`
	TableCounts map[string]int64 `json:"tableCounts"`
	BlobCount   int64            `json:"blobCount"`
}

var legacyAuditTables = []struct {
	name    string
	columns string
	key     string
}{
	{"audit_logs", "id,trace_id,traffic_source,system_account_id,api_key_id,conversation_key,session_id,session_client_type,group_id,account_id,provider_code,method,path,query_string,model,upstream_model,pricing_model,model_mapping_applied,model_mapping_source,source_endpoint_family,upstream_endpoint_family,stream,client_ip,user_agent,audit_outcome,success,final_status_code,error_phase,error_code,error_message,sample_bucket,sample_reason,attempt_count,payload_count,raw_payload_bytes,compressed_payload_bytes,compression_saved_bytes,error_group_id,capture_status,lifecycle_status,started_at,ended_at,duration_ms,http_completed_at,http_duration_ms,first_token_ms,created_at", "id"},
	{"audit_log_attempts", "id,audit_log_id,attempt_index,account_id,account_owner_system_account_id,group_id,proxy_url,provider_code,attempt_model,attempt_upstream_model,attempt_pricing_model,attempt_model_mapping_applied,attempt_model_mapping_source,attempt_source_endpoint_family,attempt_upstream_endpoint_family,upstream_method,upstream_url,upstream_status_code,success,error_phase,error_code,error_message,started_at,ended_at,duration_ms", "id"},
	{"audit_payload_blobs", "id,sha256,raw_size_bytes,compressed_size_bytes,content_type,content_encoding,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at", "id"},
	{"audit_payload_refs", "id,audit_log_id,attempt_id,part_type,sequence_index,content_type,content_encoding,headers_blob_id,body_blob_id,headers_sha256,body_sha256,raw_size_bytes,compressed_size_bytes,capture_status,drop_reason,created_at", "id"},
	{"audit_error_groups", "id,fingerprint,window_started_at,window_ended_at,system_account_id,api_key_id,group_id,account_id,provider_code,path,model,status_code,error_phase,error_code,error_type,request_fingerprint,error_fingerprint,count,first_event_id,last_event_id,sample_event_id,last_message,created_at,updated_at", "id"},
}

// MigrateLegacySQLite performs one explicit offline migration. It returns an
// error for a missing source or missing legacy tables; it never silently
// creates an empty migration source and never runs automatically.
func MigrateLegacySQLite(ctx context.Context, options LegacyMigrationOptions) (LegacyMigrationResult, error) {
	if !options.NodeStopped || !options.GoStopped {
		return LegacyMigrationResult{}, errors.New("F3 审计 SQLite 迁移要求停机：必须同时确认 Node 和 Go 已停止（--node-stopped --go-stopped）")
	}
	sourcePath := strings.TrimSpace(options.SourceDatabasePath)
	targetPath := strings.TrimSpace(options.TargetDatabasePath)
	if sourcePath == "" || targetPath == "" {
		return LegacyMigrationResult{}, errors.New("F3 审计 SQLite 迁移必须提供 source 和 target 数据库路径")
	}
	var err error
	if sourcePath, err = filepath.Abs(sourcePath); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("解析迁移源 SQLite 路径失败: %w", err)
	}
	if targetPath, err = filepath.Abs(targetPath); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("解析迁移目标 SQLite 路径失败: %w", err)
	}
	same, err := sqlitepath.SameFile(sourcePath, targetPath)
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("校验迁移源/目标 SQLite 隔离失败: %w", err)
	}
	if same {
		return LegacyMigrationResult{}, errors.New("F3 审计 SQLite 迁移源与目标不得是同一文件")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("访问旧审计 SQLite 源文件失败: %w", err)
	}
	source, err := sql.Open("sqlite", readOnlySQLiteDSN(sourcePath))
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("打开旧审计 SQLite 源失败: %w", err)
	}
	defer source.Close()
	if err := source.PingContext(ctx); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("连接旧审计 SQLite 源失败: %w", err)
	}
	if err := verifySQLiteIntegrityDB(ctx, source, "旧审计 SQLite"); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err := verifyLegacyAuditTables(ctx, source); err != nil {
		return LegacyMigrationResult{}, err
	}
	sourceBlobRoot := strings.TrimSpace(options.SourceBlobDirectory)
	targetBlobRoot := strings.TrimSpace(options.TargetBlobDirectory)
	if sourceBlobRoot == "" || targetBlobRoot == "" {
		return LegacyMigrationResult{}, errors.New("F3 审计 SQLite 迁移必须提供 source 和 target blob 目录")
	}
	sourceBlobPath, err := sqlitepath.CanonicalPath(sourceBlobRoot)
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("解析 source blob 目录失败: %w", err)
	}
	targetBlobPath, err := sqlitepath.CanonicalPath(targetBlobRoot)
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("解析 target blob 目录失败: %w", err)
	}
	if equalLegacyPath(sourceBlobPath, targetBlobPath) {
		return LegacyMigrationResult{}, errors.New("F3 审计迁移 source 和 target blob 目录不得相同")
	}
	sourceBlobRoot = sourceBlobPath
	targetBlobRoot = targetBlobPath
	if err := verifyBlobSource(ctx, source, sourceBlobRoot); err != nil {
		return LegacyMigrationResult{}, err
	}

	target, err := sql.Open("sqlite", targetSQLiteDSN(targetPath))
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("打开 F3 审计 SQLite 目标失败: %w", err)
	}
	defer target.Close()
	target.SetMaxOpenConns(1)
	if err := configureSQLite(target); err != nil {
		return LegacyMigrationResult{}, err
	}
	if _, err := target.ExecContext(ctx, sqliteSchema); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("初始化 F3 审计 SQLite 目标 schema 失败: %w", err)
	}
	if _, err := target.ExecContext(ctx, "ATTACH DATABASE ? AS legacy_source", sourcePath); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("附加旧审计 SQLite 源失败: %w", err)
	}
	attached := true
	defer func() {
		if attached {
			_, _ = target.ExecContext(context.Background(), "DETACH DATABASE legacy_source")
		}
	}()

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("开始 F3 审计迁移事务失败: %w", err)
	}
	defer tx.Rollback()
	result := LegacyMigrationResult{TableCounts: make(map[string]int64)}
	for _, table := range legacyAuditTables {
		query := fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) SELECT %s FROM legacy_source.%s", table.name, table.columns, table.columns, table.name)
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return LegacyMigrationResult{}, fmt.Errorf("复制旧审计表 %s 失败: %w", table.name, err)
		}
		var sourceCount, targetCount int64
		if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM legacy_source.%s", table.name)).Scan(&sourceCount); err != nil {
			return LegacyMigrationResult{}, fmt.Errorf("读取旧审计表 %s 行数失败: %w", table.name, err)
		}
		if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table.name)).Scan(&targetCount); err != nil {
			return LegacyMigrationResult{}, fmt.Errorf("读取目标审计表 %s 行数失败: %w", table.name, err)
		}
		if sourceCount != targetCount {
			return LegacyMigrationResult{}, fmt.Errorf("审计表 %s 行数不一致: source=%d target=%d", table.name, sourceCount, targetCount)
		}
		var missing int64
		if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM legacy_source.%s s LEFT JOIN %s t ON t.%s=s.%s WHERE t.%s IS NULL", table.name, table.name, table.key, table.key, table.key)).Scan(&missing); err != nil {
			return LegacyMigrationResult{}, fmt.Errorf("校验审计表 %s 主键失败: %w", table.name, err)
		}
		if missing != 0 {
			return LegacyMigrationResult{}, fmt.Errorf("审计表 %s 存在 %d 条未迁移主键", table.name, missing)
		}
		var mismatched int64
		if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM (SELECT %s FROM legacy_source.%s EXCEPT SELECT %s FROM %s)", table.columns, table.name, table.columns, table.name)).Scan(&mismatched); err != nil {
			return LegacyMigrationResult{}, fmt.Errorf("校验审计表 %s 字段失败: %w", table.name, err)
		}
		if mismatched != 0 {
			return LegacyMigrationResult{}, fmt.Errorf("审计表 %s 存在 %d 条字段不一致记录", table.name, mismatched)
		}
		result.TableCounts[table.name] = sourceCount
	}
	if err := verifyAuditReferencesTx(ctx, tx, "legacy_source", "旧审计 SQLite"); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err := verifyAuditBlobRefCountsTx(ctx, tx, "legacy_source", "旧审计 SQLite"); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err := verifyAuditReferencesTx(ctx, tx, "main", "目标审计 SQLite"); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err := verifyAuditBlobRefCountsTx(ctx, tx, "main", "目标审计 SQLite"); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("提交 F3 审计迁移事务失败: %w", err)
	}
	if err := os.MkdirAll(targetBlobRoot, 0o750); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("创建目标 blob 目录失败: %w", err)
	}
	if err := copyLegacyBlobs(ctx, source, sourceBlobRoot, targetBlobRoot); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err := verifySQLiteIntegrityDB(ctx, target, "目标审计 SQLite"); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err := verifyTargetBlobFiles(ctx, target, targetBlobRoot); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err := detachDatabase(ctx, target, "legacy_source"); err != nil {
		return LegacyMigrationResult{}, err
	}
	attached = false
	result.BlobCount = result.TableCounts["audit_payload_blobs"]
	return result, nil
}

func equalLegacyPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func readOnlySQLiteDSN(path string) string {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return "file:" + filepath.ToSlash(path) + "?mode=ro"
	}
	return dsn + "&mode=ro"
}
func targetSQLiteDSN(path string) string {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
	}
	return dsn
}

func verifyLegacyAuditTables(ctx context.Context, db *sql.DB) error {
	for _, table := range legacyAuditTables {
		var name string
		if err := db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table.name).Scan(&name); err != nil {
			return fmt.Errorf("旧审计 SQLite 缺少必需表 %s（不执行 no-op）: %w", table.name, err)
		}
	}
	return nil
}

func verifySQLiteIntegrityDB(ctx context.Context, db *sql.DB, label string) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("%s integrity_check 失败: %w", label, err)
	}
	if !strings.EqualFold(strings.TrimSpace(result), "ok") {
		return fmt.Errorf("%s integrity_check 返回异常: %s", label, result)
	}
	return nil
}

func verifyBlobSource(ctx context.Context, db *sql.DB, root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("F3 审计 SQLite 迁移必须提供 source blob 目录")
	}
	rows, err := db.QueryContext(ctx, "SELECT storage_key,sha256,raw_size_bytes,compressed_size_bytes,compression FROM audit_payload_blobs")
	if err != nil {
		return fmt.Errorf("读取旧审计 blob 元数据失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, digest, compression string
		var rawSize, compressedSize int64
		if err := rows.Scan(&key, &digest, &rawSize, &compressedSize, &compression); err != nil {
			return fmt.Errorf("读取旧审计 blob 元数据行失败: %w", err)
		}
		if _, err := verifyBlobFile(root, key, digest, rawSize, compressedSize, compression); err != nil {
			return fmt.Errorf("旧审计 blob %q 校验失败: %w", key, err)
		}
	}
	return rows.Err()
}

func verifyTargetBlobFiles(ctx context.Context, db *sql.DB, root string) error {
	rows, err := db.QueryContext(ctx, "SELECT storage_key,sha256,raw_size_bytes,compressed_size_bytes,compression FROM audit_payload_blobs")
	if err != nil {
		return fmt.Errorf("读取目标审计 blob 元数据失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, digest, compression string
		var rawSize, compressedSize int64
		if err := rows.Scan(&key, &digest, &rawSize, &compressedSize, &compression); err != nil {
			return err
		}
		if _, err := verifyBlobFile(root, key, digest, rawSize, compressedSize, compression); err != nil {
			return fmt.Errorf("目标审计 blob %q 校验失败: %w", key, err)
		}
	}
	return rows.Err()
}

func verifyBlobFile(root, storageKey, digest string, rawSize, compressedSize int64, compression string) ([]byte, error) {
	if filepath.IsAbs(storageKey) || strings.Contains(storageKey, "..") {
		return nil, errors.New("storage_key 必须是相对且不可越界路径")
	}
	path := filepath.Join(root, filepath.FromSlash(storageKey))
	inside, err := sqlitepath.PathWithin(root, path)
	if err != nil || !inside {
		return nil, errors.New("storage_key 越出 blob 根目录")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != compressedSize {
		return nil, fmt.Errorf("compressed_size_bytes=%d 实际=%d", compressedSize, len(data))
	}
	raw := data
	if strings.EqualFold(compression, "gzip") {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		raw, err = io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			return nil, err
		}
	} else if compression != "" && !strings.EqualFold(compression, "none") {
		return nil, fmt.Errorf("不支持的 blob compression=%q", compression)
	}
	if int64(len(raw)) != rawSize {
		return nil, fmt.Errorf("raw_size_bytes=%d 实际=%d", rawSize, len(raw))
	}
	sum := sha256.Sum256(raw)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), digest) {
		return nil, fmt.Errorf("sha256 不匹配")
	}
	return data, nil
}

func copyLegacyBlobs(ctx context.Context, db *sql.DB, sourceRoot, targetRoot string) error {
	rows, err := db.QueryContext(ctx, "SELECT storage_key,sha256,raw_size_bytes,compressed_size_bytes,compression FROM audit_payload_blobs")
	if err != nil {
		return fmt.Errorf("读取旧审计 blob 文件失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, digest, compression string
		var rawSize, compressedSize int64
		if err := rows.Scan(&key, &digest, &rawSize, &compressedSize, &compression); err != nil {
			return err
		}
		data, err := verifyBlobFile(sourceRoot, key, digest, rawSize, compressedSize, compression)
		if err != nil {
			return fmt.Errorf("读取旧审计 blob %q 失败: %w", key, err)
		}
		destination := filepath.Join(targetRoot, filepath.FromSlash(key))
		if existing, err := os.ReadFile(destination); err == nil {
			if !bytes.Equal(existing, data) {
				return fmt.Errorf("目标 blob %q 已存在但内容不一致", key)
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(destination), ".f3-migrate-*.tmp")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		if _, err = tmp.Write(data); err == nil {
			err = tmp.Sync()
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(tmpName, destination)
		}
		if err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("发布目标 blob %q 失败: %w", key, err)
		}
	}
	return rows.Err()
}

func verifyAuditReferencesTx(ctx context.Context, tx *sql.Tx, schema, label string) error {
	queries := []struct{ name, query string }{
		{"payload->log", fmt.Sprintf("SELECT COUNT(*) FROM %s.audit_payload_refs r LEFT JOIN %s.audit_logs l ON l.id=r.audit_log_id WHERE l.id IS NULL", schema, schema)},
		{"payload->attempt", fmt.Sprintf("SELECT COUNT(*) FROM %s.audit_payload_refs r LEFT JOIN %s.audit_log_attempts a ON a.id=r.attempt_id WHERE r.attempt_id IS NOT NULL AND a.id IS NULL", schema, schema)},
		{"payload->header blob", fmt.Sprintf("SELECT COUNT(*) FROM %s.audit_payload_refs r LEFT JOIN %s.audit_payload_blobs b ON b.id=r.headers_blob_id WHERE r.headers_blob_id IS NOT NULL AND b.id IS NULL", schema, schema)},
		{"payload->body blob", fmt.Sprintf("SELECT COUNT(*) FROM %s.audit_payload_refs r LEFT JOIN %s.audit_payload_blobs b ON b.id=r.body_blob_id WHERE r.body_blob_id IS NOT NULL AND b.id IS NULL", schema, schema)},
	}
	for _, item := range queries {
		var count int64
		if err := tx.QueryRowContext(ctx, item.query).Scan(&count); err != nil {
			return fmt.Errorf("%s %s 引用完整性校验失败: %w", label, item.name, err)
		}
		if count != 0 {
			return fmt.Errorf("%s %s 存在 %d 条悬空引用", label, item.name, count)
		}
	}
	return nil
}

func verifyAuditBlobRefCountsTx(ctx context.Context, tx *sql.Tx, schema, label string) error {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s.audit_payload_blobs b
LEFT JOIN %s.audit_payload_refs r ON r.headers_blob_id=b.id OR r.body_blob_id=b.id
GROUP BY b.id,b.ref_count
HAVING b.ref_count != SUM(CASE WHEN r.headers_blob_id=b.id THEN 1 ELSE 0 END)+SUM(CASE WHEN r.body_blob_id=b.id THEN 1 ELSE 0 END)`, schema, schema)
	var mismatched int64
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("%s blob ref_count 完整性校验失败: %w", label, err)
	}
	for rows.Next() {
		mismatched++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("%s blob ref_count 完整性读取失败: %w", label, err)
	}
	_ = rows.Close()
	if mismatched != 0 {
		return fmt.Errorf("%s 存在 %d 条 blob ref_count 不一致记录", label, mismatched)
	}
	return nil
}

func detachDatabase(ctx context.Context, db *sql.DB, name string) error {
	_, err := db.ExecContext(ctx, "DETACH DATABASE "+name)
	return err
}

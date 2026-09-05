package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// usage-record-shards.ts 的 SQLite 清理侧移植：分片定位（目录 catalog 驱动）、
// 分片库打开缓存、目录条目删除（含 scope 收缩）、空分片文件清理。
// 文件路径以 usage_record_shards.file_path 为权威（Node 同约定）。

// ShardLocation 照 Node UsageRecordShardLocation。
type ShardLocation struct {
	ShardKey      string
	BucketDate    string
	BucketDateKey string
	ShardID       int64
	FilePath      string
}

// ShardLocationWindow 照 Node UsageRecordShardLocationWindow。
type ShardLocationWindow struct {
	Locations []ShardLocation
	HasMore   bool
}

// ShardStore 承载 SQLite usage 分片访问（仅 sqlite 模式使用）。
type ShardStore struct {
	// Root 是 JUHE_AI_USAGE_SHARD_ROOT（组合根已按 Node 语义解析）。
	Root string
	// Open 手动注入测试分片库；为空时按 filePath 直开。
	open func(filePath string) (*sql.DB, error)

	databases map[string]*sql.DB
}

// NewShardStore 构建分片存储。
func NewShardStore(root string) *ShardStore {
	return &ShardStore{Root: root, databases: map[string]*sql.DB{}}
}

// SetOpener 注入自定义打开器（测试用）。
func (s *ShardStore) SetOpener(open func(filePath string) (*sql.DB, error)) {
	s.open = open
}

// Open 打开一个分片库（进程内缓存，单写者串行）。
func (s *ShardStore) Open(filePath string) (*sql.DB, error) {
	if cached, ok := s.databases[filePath]; ok {
		return cached, nil
	}
	var (
		db  *sql.DB
		err error
	)
	if s.open != nil {
		db, err = s.open(filePath)
	} else {
		db, err = sql.Open("sqlite", "file:"+filePath+"?_pragma=busy_timeout(5000)&_txlock=immediate")
		if err == nil {
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open usage shard sqlite 失败: %w", err)
	}
	if s.databases == nil {
		s.databases = map[string]*sql.DB{}
	}
	s.databases[filePath] = db
	return db, nil
}

// Close 关闭全部分片库（Node closeUsageRecordShardDatabases）。
func (s *ShardStore) Close() error {
	var firstErr error
	for path, db := range s.databases {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.databases, path)
	}
	return firstErr
}

func shardLocationFromRegistryRow(shardKey, bucketDate string, shardID int64, filePath string) *ShardLocation {
	if strings.TrimSpace(shardKey) == "" || strings.TrimSpace(bucketDate) == "" ||
		strings.TrimSpace(filePath) == "" || shardID <= 0 {
		return nil
	}
	bucketDateKey := strings.ReplaceAll(bucketDate, "-", "")
	return &ShardLocation{
		ShardKey:      strings.TrimSpace(shardKey),
		BucketDate:    strings.TrimSpace(bucketDate),
		BucketDateKey: bucketDateKey,
		ShardID:       shardID,
		FilePath:      strings.TrimSpace(filePath),
	}
}

// listLocationsByScopeCatalog 照 listUsageRecordShardLocationsByScopeCatalog。
func listLocationsByScopeCatalog(ctx context.Context, catalog *DB, tableName, whereClause string, params []any, limit int) (ShardLocationWindow, error) {
	normalizedLimit := batchLimit(limit)
	query := fmt.Sprintf(`
      SELECT s.shard_key, s.bucket_date, s.shard_id, s.file_path
      FROM %s c
      JOIN usage_record_shards s ON s.shard_key = c.shard_key
      WHERE s.status = 'active'
        AND %s
      ORDER BY c.first_created_at ASC, s.shard_id ASC
      LIMIT ?
	`, catalog.Table("", tableName), whereClause)
	rows, err := catalog.QueryContext(ctx, catalog.Bind(query), append(params, any(normalizedLimit+1))...)
	if err != nil {
		return ShardLocationWindow{}, err
	}
	defer rows.Close()
	var raw []ShardLocation
	for rows.Next() {
		var shardKey, bucketDate, filePath sql.NullString
		var shardID sql.NullInt64
		if err := rows.Scan(&shardKey, &bucketDate, &shardID, &filePath); err != nil {
			return ShardLocationWindow{}, err
		}
		if location := shardLocationFromRegistryRow(shardKey.String, bucketDate.String, shardID.Int64, filePath.String); location != nil {
			raw = append(raw, *location)
		}
	}
	if err := rows.Err(); err != nil {
		return ShardLocationWindow{}, err
	}
	hasMore := len(raw) > normalizedLimit
	if hasMore {
		raw = raw[:normalizedLimit]
	}
	return ShardLocationWindow{Locations: raw, HasMore: hasMore}, nil
}

// ListLocationsForApiKey 照 listUsageRecordShardLocationsForApiKey。
func ListLocationsForApiKey(ctx context.Context, catalog *DB, apiKeyID, systemAccountID string, limit int) (ShardLocationWindow, error) {
	apiKeyID = strings.TrimSpace(apiKeyID)
	systemAccountID = strings.TrimSpace(systemAccountID)
	if apiKeyID == "" || systemAccountID == "" {
		return ShardLocationWindow{}, nil
	}
	return listLocationsByScopeCatalog(ctx, catalog, "usage_record_api_key_shards",
		"c.api_key_id = ? AND c.system_account_id = ?", []any{apiKeyID, systemAccountID}, limit)
}

// ListLocationsForAccount 照 listUsageRecordShardLocationsForAccount。
func ListLocationsForAccount(ctx context.Context, catalog *DB, accountID string, limit int) (ShardLocationWindow, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ShardLocationWindow{}, nil
	}
	return listLocationsByScopeCatalog(ctx, catalog, "usage_record_account_shards",
		"c.account_id = ?", []any{accountID}, limit)
}

// scopeEntry 查询 usage_record_shard_entries 的 scope 行。
type scopeEntry struct {
	UsageID         string
	ShardKey        string
	SystemAccountID string
	APIKeyID        string
	AccountID       string
}

func listScopeEntries(ctx context.Context, catalog *DB, ids []string) ([]scopeEntry, error) {
	var scopes []scopeEntry
	for _, chunk := range chunkValues(ids, 900) {
		if len(chunk) == 0 {
			continue
		}
		query := fmt.Sprintf(`
      SELECT usage_id, shard_key, system_account_id, api_key_id, account_id
      FROM usage_record_shard_entries
      WHERE usage_id IN (%s)
		`, catalog.BindIn(len(chunk)))
		rows, err := catalog.QueryContext(ctx, catalog.Bind(query), stringSliceToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var entry scopeEntry
			var usageID, shardKey, systemAccountID, apiKeyID, accountID sql.NullString
			if err := rows.Scan(&usageID, &shardKey, &systemAccountID, &apiKeyID, &accountID); err != nil {
				rows.Close()
				return nil, err
			}
			entry.UsageID = usageID.String
			entry.ShardKey = shardKey.String
			entry.SystemAccountID = systemAccountID.String
			entry.APIKeyID = apiKeyID.String
			entry.AccountID = accountID.String
			if entry.UsageID != "" && entry.ShardKey != "" && entry.SystemAccountID != "" {
				scopes = append(scopes, entry)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return scopes, nil
}

func stringSliceToAny(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

// cleanupScopeShardCatalog 照 cleanupUsageRecordScopeShardCatalog（SQLite 版）。
func cleanupScopeShardCatalog(ctx context.Context, catalog *DB, scopes []scopeEntry) error {
	accountScopes := map[string]bool{}
	apiKeyScopes := map[string]bool{}
	for _, scope := range scopes {
		if accountID := strings.TrimSpace(scope.AccountID); accountID != "" {
			accountScopes[accountID+"\x00"+scope.ShardKey] = true
		}
		if apiKeyID := strings.TrimSpace(scope.APIKeyID); apiKeyID != "" {
			apiKeyScopes[apiKeyID+"\x00"+scope.SystemAccountID+"\x00"+scope.ShardKey] = true
		}
	}
	deleteAccount := catalog.Bind(`
    DELETE FROM usage_record_account_shards
    WHERE account_id = ? AND shard_key = ?
      AND NOT EXISTS (
        SELECT 1 FROM usage_record_shard_entries
        WHERE account_id = ? AND shard_key = ?
        LIMIT 1
      )
	`)
	deleteAPIKey := catalog.Bind(`
    DELETE FROM usage_record_api_key_shards
    WHERE api_key_id = ? AND system_account_id = ? AND shard_key = ?
      AND NOT EXISTS (
        SELECT 1 FROM usage_record_shard_entries
        WHERE api_key_id = ? AND system_account_id = ? AND shard_key = ?
        LIMIT 1
      )
	`)
	for key := range accountScopes {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		if _, err := catalog.ExecContext(ctx, deleteAccount, parts[0], parts[1], parts[0], parts[1]); err != nil {
			return err
		}
	}
	for key := range apiKeyScopes {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			continue
		}
		if _, err := catalog.ExecContext(ctx, deleteAPIKey, parts[0], parts[1], parts[2], parts[0], parts[1], parts[2]); err != nil {
			return err
		}
	}
	return nil
}

// DeleteShardEntries 照 deleteUsageRecordShardEntries：删除目录条目并收缩
// scope catalog，返回受影响行数。
func (s *ShardStore) DeleteShardEntries(ctx context.Context, catalog *DB, ids []string) (int64, error) {
	normalized := uniqueNonEmpty(ids)
	if len(normalized) == 0 {
		return 0, nil
	}
	var deletedRows int64
	for _, chunk := range chunkValues(normalized, 900) {
		scopes, err := listScopeEntries(ctx, catalog, chunk)
		if err != nil {
			return deletedRows, err
		}
		result, err := catalog.ExecContext(ctx, catalog.Bind(fmt.Sprintf(
			`DELETE FROM usage_record_shard_entries WHERE usage_id IN (%s)`, catalog.BindIn(len(chunk)))),
			stringSliceToAny(chunk)...)
		if err != nil {
			return deletedRows, err
		}
		affected, err := changes(result)
		if err != nil {
			return deletedRows, err
		}
		deletedRows += affected
		if err := cleanupScopeShardCatalog(ctx, catalog, scopes); err != nil {
			return deletedRows, err
		}
	}
	return deletedRows, nil
}

// EmptyShardFileCleanupResult 照 EmptyUsageRecordShardFileCleanupResult。
type EmptyShardFileCleanupResult struct {
	UsageRecordShards int64
	UsageShardFiles   int64
	HasMore           bool
}

// CleanupEmptyShardFilesBefore 照 cleanupEmptyUsageRecordShardFilesBefore：
// 删除不再持有目录条目的空分片（先关分片库，再删文件，最后删目录行）。
func (s *ShardStore) CleanupEmptyShardFilesBefore(ctx context.Context, catalog *DB, cutoffAt string, limit int) (EmptyShardFileCleanupResult, error) {
	result := EmptyShardFileCleanupResult{}
	cutoff, ok := parseInstant(cutoffAt)
	if !ok {
		return result, fmt.Errorf("usage record createdAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	cutoffDate := cutoff.UTC().Format("2006-01-02")
	batch := batchLimit(limit)
	query := catalog.Bind(`
      SELECT s.shard_key, s.bucket_date, s.shard_id, s.file_path
      FROM usage_record_shards s
      WHERE s.status = 'active'
        AND s.bucket_date <= ?
        AND NOT EXISTS (
          SELECT 1 FROM usage_record_shard_entries ue WHERE ue.shard_key = s.shard_key LIMIT 1
        )
      ORDER BY s.bucket_date ASC, s.shard_id ASC
      LIMIT ?
	`)
	rows, err := catalog.QueryContext(ctx, query, cutoffDate, batch+1)
	if err != nil {
		return result, err
	}
	var candidates []ShardLocation
	for rows.Next() {
		var shardKey, bucketDate, filePath sql.NullString
		var shardID sql.NullInt64
		if err := rows.Scan(&shardKey, &bucketDate, &shardID, &filePath); err != nil {
			rows.Close()
			return result, err
		}
		if location := shardLocationFromRegistryRow(shardKey.String, bucketDate.String, shardID.Int64, filePath.String); location != nil {
			candidates = append(candidates, *location)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	hasMore := len(candidates) > batch
	if hasMore {
		candidates = candidates[:batch]
	}
	if len(candidates) == 0 {
		return EmptyShardFileCleanupResult{HasMore: hasMore}, nil
	}

	// Node 先 closeUsageRecordShardDatabases() 再删文件。
	if err := s.Close(); err != nil {
		return result, err
	}
	for _, location := range candidates {
		deleted, err := deleteShardFileSet(location.FilePath)
		if err != nil {
			return result, err
		}
		result.UsageShardFiles += deleted
	}
	shardKeys := make([]string, 0, len(candidates))
	for _, location := range candidates {
		shardKeys = append(shardKeys, location.ShardKey)
	}
	for _, chunk := range chunkValues(shardKeys, 900) {
		affected, err := execChanged(ctx, catalog, fmt.Sprintf(
			`DELETE FROM usage_record_shards WHERE shard_key IN (%s)`, catalog.BindIn(len(chunk))),
			stringSliceToAny(chunk)...)
		if err != nil {
			return result, err
		}
		result.UsageRecordShards += affected
	}
	result.HasMore = hasMore
	return result, nil
}

// deleteShardFileSet 照 deleteUsageShardFileSet：删除主库与 -wal/-shm/-journal。
func deleteShardFileSet(filePath string) (int64, error) {
	var deleted int64
	for _, target := range []string{filePath, filePath + "-wal", filePath + "-shm", filePath + "-journal"} {
		err := os.Remove(target)
		if err == nil {
			deleted++
			continue
		}
		if os.IsNotExist(err) {
			continue
		}
		return deleted, err
	}
	return deleted, nil
}

// shardFilePathForTest 组装与 Node usageRecordShardLocation 一致的分片路径
// （Root/YYYY/MM/DD/usage-YYYYMMDD-sNN.sqlite3），供测试 seed 使用。
func shardFilePathForTest(root, bucketDateKey string, shardID int64) string {
	shardIDNormalized := shardID
	if shardIDNormalized < 0 {
		shardIDNormalized = 0
	}
	return filepath.Join(root,
		bucketDateKey[0:4], bucketDateKey[4:6], bucketDateKey[6:8],
		fmt.Sprintf("usage-%s-s%02d.sqlite3", bucketDateKey, shardIDNormalized))
}

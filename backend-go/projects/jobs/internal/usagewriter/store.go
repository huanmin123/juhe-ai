package usagewriter

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ShardStore ports the durable side of the batch write (usage-records
// repository + shard storage): one atomic call per flush batch. The writer
// queue tests run against in-memory mocks of this port; the concrete
// SQLite/Postgres stores below mirror the Node SQL.
type ShardStore interface {
	// WriteBatch persists one flush batch: shard rows, catalog entries and
	// the account side effects, mirroring createUsageRecordsBatch/
	// createUsageRecordsBatchAsync. Returns the number of newly inserted
	// usage rows (ON CONFLICT DO NOTHING misses excluded).
	WriteBatch(ctx Ctx, plan WritePlan) (int, error)
}

// CatalogSchemaSQL mirrors backend/src/storage/schema/usage-catalog-schema.ts
// (applyUsageCatalogSchema).
const CatalogSchemaSQL = `
    CREATE TABLE IF NOT EXISTS usage_record_shards (
          shard_key TEXT PRIMARY KEY,
          bucket_date TEXT NOT NULL,
          shard_id INTEGER NOT NULL,
          file_path TEXT NOT NULL,
          schema_version INTEGER NOT NULL DEFAULT 1,
          status TEXT NOT NULL DEFAULT 'active',
          first_seen_at TEXT NOT NULL,
          last_write_at TEXT,
          last_error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS usage_record_shard_entries (
          usage_id TEXT PRIMARY KEY,
          shard_key TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          trace_id TEXT NOT NULL,
          api_key_id TEXT,
          account_id TEXT,
          group_id TEXT,
          model TEXT,
          traffic_source TEXT NOT NULL,
          success INTEGER NOT NULL DEFAULT 0,
          status_code INTEGER,
          client_ip TEXT,
          first_token_ms INTEGER,
          duration_ms INTEGER,
          cost_usd REAL,
          created_at TEXT NOT NULL,
          indexed_at TEXT NOT NULL,
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS usage_record_account_shards (
          account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (
          api_key_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (api_key_id, system_account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE INDEX IF NOT EXISTS idx_usage_record_shards_bucket ON usage_record_shards(bucket_date, shard_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_account_shards_account_created ON usage_record_account_shards(account_id, first_created_at, shard_key);
    CREATE INDEX IF NOT EXISTS idx_usage_record_api_key_shards_key_created ON usage_record_api_key_shards(api_key_id, system_account_id, first_created_at, shard_key);

    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_shard ON usage_record_shard_entries(shard_key, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_created_sort ON usage_record_shard_entries(created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_created_sort ON usage_record_shard_entries(system_account_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_trace_created_sort ON usage_record_shard_entries(system_account_id, trace_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_api_key_created_sort ON usage_record_shard_entries(system_account_id, api_key_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_group_created_sort ON usage_record_shard_entries(system_account_id, group_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_account_created_sort ON usage_record_shard_entries(system_account_id, account_id, created_at, usage_id);
`

// UsageShardBaseSchemaSQL mirrors applyUsageRecordShardBaseSchema
// (usage-record-shards.ts), including the legacy index drops.
const UsageShardBaseSchemaSQL = `
    CREATE TABLE IF NOT EXISTS usage_records (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      trace_id TEXT NOT NULL,
      traffic_source TEXT NOT NULL,
      client_ip TEXT,
      api_key_id TEXT,
      group_id TEXT,
      account_id TEXT,
      endpoint TEXT,
      provider_code TEXT,
      provider_protocol_profile_id TEXT,
      usage_semantic TEXT,
      model TEXT,
      upstream_model TEXT,
      upstream_response_model TEXT,
      pricing_model TEXT,
      requested_service_tier TEXT NOT NULL DEFAULT 'default',
      effective_service_tier TEXT NOT NULL DEFAULT 'default',
      reported_service_tier TEXT,
      billed_service_tier TEXT NOT NULL DEFAULT 'default',
      requested_reasoning_effort TEXT,
      effective_reasoning_effort TEXT,
      cost_breakdown_snapshot_json TEXT,
      model_mapping_applied INTEGER NOT NULL DEFAULT 0,
      model_mapping_source TEXT,
      source_endpoint_family TEXT,
      upstream_endpoint_family TEXT,
      stream INTEGER NOT NULL DEFAULT 0,
      status_code INTEGER,
      success INTEGER NOT NULL DEFAULT 0,
      failure_attribution TEXT,
      first_token_ms INTEGER,
      duration_ms INTEGER,
      input_tokens INTEGER,
      output_tokens INTEGER,
      cache_read_tokens INTEGER,
      cache_read_cost_usd REAL,
      cache_write_tokens INTEGER,
      cache_write_1h_tokens INTEGER,
      cache_write_cost_usd REAL,
      thinking_tokens INTEGER,
      input_image_tokens INTEGER,
      output_image_tokens INTEGER,
      input_audio_tokens INTEGER,
      output_audio_tokens INTEGER,
      output_image_count INTEGER,
      cost_usd REAL,
      error_code TEXT,
      error_message TEXT,
      request_snapshot_json TEXT,
      response_snapshot_json TEXT,
      account_owner_system_account_id TEXT,
      group_owner_system_account_id TEXT,
      account_access_type TEXT,
      group_access_type TEXT,
      account_authorization_id TEXT,
      account_authorization_source_type TEXT,
      account_authorization_source_team_id TEXT,
      group_authorization_id TEXT,
      group_authorization_source_type TEXT,
      group_authorization_source_team_id TEXT,
      created_at TEXT NOT NULL
    );

    DROP INDEX IF EXISTS idx_usage_records_created_at;
    DROP INDEX IF EXISTS idx_usage_records_system_account_created_at;
    DROP INDEX IF EXISTS idx_usage_records_group_real_usage;
    DROP INDEX IF EXISTS idx_usage_records_group_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_first_token_sort;
    DROP INDEX IF EXISTS idx_usage_records_duration_sort;
    DROP INDEX IF EXISTS idx_usage_records_cost_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_first_token_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_duration_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_cost_sort;
    DROP INDEX IF EXISTS idx_usage_records_api_key_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_account_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_trace_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_model_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_model_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_traffic_source_created;
    DROP INDEX IF EXISTS idx_usage_records_client_ip_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_system_account_client_ip_created_sort;
    DROP INDEX IF EXISTS idx_usage_records_provider_protocol_profile_created_at;

    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_sort ON usage_records(system_account_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_trace_created_sort ON usage_records(system_account_id, trace_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_group_created_sort ON usage_records(system_account_id, group_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_api_key_created_sort ON usage_records(system_account_id, api_key_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_account_created_sort ON usage_records(system_account_id, account_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_usage_records_account_owner ON usage_records(account_owner_system_account_id, account_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_group_owner ON usage_records(group_owner_system_account_id, group_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_account_authorization ON usage_records(account_authorization_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_group_authorization ON usage_records(group_authorization_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_stats_cursor ON usage_records(created_at, id);
`

// sqlitePlaceholder builds "?, ?, ?" of the requested size.
func sqlitePlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

// SqliteShardStoreConfig mirrors the facts the SQLite write path consumes.
type SqliteShardStoreConfig struct {
	// CatalogDB is the usage-catalog database (usage_record_shards and the
	// scope catalog tables).
	CatalogDB *sql.DB
	// ShardRoot is the usage-shard root directory (usageRecordShardRoot).
	ShardRoot string
	// ShardCount mirrors runtimeConfig.usageShardCount.
	ShardCount int
	// BusinessDB optionally carries the business database used for the
	// accounts.last_used_at side effect (nil mirrors queryOnly mode: the
	// side effect is skipped with a warning).
	BusinessDB *sql.DB
	// BusyTimeoutMs mirrors sqliteBusyTimeoutMs.
	BusyTimeoutMs int
	// Now substitutes nowIso(); nil = wall clock.
	Now func() time.Time
}

// SqliteShardStore mirrors writeUsageRecordShardRows +
// recordUsageRecordShardEntries + flushUsageRecordBusinessSideEffects over
// database/sql with the modernc.org/sqlite driver.
type SqliteShardStore struct {
	config SqliteShardStoreConfig

	mu       sync.Mutex
	shardDBs map[string]*sql.DB
}

// NewSqliteShardStore builds the store.
func NewSqliteShardStore(config SqliteShardStoreConfig) *SqliteShardStore {
	if config.ShardCount < 1 {
		config.ShardCount = DefaultUsageShardCount
	}
	if config.BusyTimeoutMs <= 0 {
		config.BusyTimeoutMs = 5000
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now() }
	}
	return &SqliteShardStore{
		config:   config,
		shardDBs: map[string]*sql.DB{},
	}
}

func (s *SqliteShardStore) nowIso() string {
	return s.config.Now().UTC().Format(timeRFC3339Millis)
}

// EnsureCatalogSchema applies applyUsageCatalogSchema once.
func (s *SqliteShardStore) EnsureCatalogSchema() error {
	_, err := s.config.CatalogDB.Exec(CatalogSchemaSQL)
	return err
}

func (s *SqliteShardStore) openShardDB(location UsageRecordShardLocation) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, exists := s.shardDBs[location.FilePath]; exists {
		return cached, nil
	}
	// Windows paths must be slash-normalized inside the file: URI, or the
	// sqlite URI parser fails with "unable to open database file".
	dsn := "file:" + filepath.ToSlash(location.FilePath) + "?_pragma=busy_timeout(" + itoa(s.config.BusyTimeoutMs) + ")&_pragma=journal_mode(WAL)"
	// getUsageRecordShardDatabase mirrors the Node mkdirSync of the shard
	// directory before creating the database file.
	if err := os.MkdirAll(filepath.Dir(location.FilePath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(UsageShardBaseSchemaSQL); err != nil {
		db.Close()
		return nil, err
	}
	// The upstream_response_model column add mirrors
	// ensureUsageRecordShardUpstreamResponseModelColumn for pre-existing
	// shard files created before the column existed.
	if err := ensureUpstreamResponseModelColumn(db); err != nil {
		db.Close()
		return nil, err
	}
	s.shardDBs[location.FilePath] = db
	return db, nil
}

func ensureUpstreamResponseModelColumn(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(usage_records)")
	if err != nil {
		return nil
	}
	defer rows.Close()
	has := false
	for rows.Next() {
		var cid int
		var name string
		var ctype sql.NullString
		var notNull any
		var dflt any
		var pk any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err == nil && name == "upstream_response_model" {
			has = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec("ALTER TABLE usage_records ADD COLUMN upstream_response_model TEXT"); err != nil {
			// A concurrent writer may have added it first; treat duplicate
			// column errors as success like the Node presence check.
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return err
			}
		}
	}
	return nil
}

// WriteBatch implements ShardStore: shard rows (transactional insert with
// ON CONFLICT(id) DO NOTHING), then the catalog entries transaction, then
// the accounts.last_used_at side effect (warn-only like Node).
func (s *SqliteShardStore) WriteBatch(ctx Ctx, plan WritePlan) (int, error) {
	inserted := 0
	lastUsedAt := map[string]string{}
	healthSuccessAt := map[string]string{}
	for _, shardRows := range plan.RowsByShard {
		count, err := s.writeShardRows(shardRows.Location, shardRows.Rows)
		if err != nil {
			return inserted, err
		}
		inserted += count
		MergeShardWriteResult(lastUsedAt, healthSuccessAt, shardRows.Rows)
	}
	if err := s.recordShardEntries(ctx, plan.ShardEntries, plan.Locations); err != nil {
		return inserted, err
	}
	s.flushBusinessSideEffects(lastUsedAt)
	return inserted, nil
}

// writeShardRows mirrors writeUsageRecordShardRows.
func (s *SqliteShardStore) writeShardRows(location UsageRecordShardLocation, rows []ShardWriteRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	db, err := s.openShardDB(location)
	if err != nil {
		return 0, err
	}
	insertSQL := fmt.Sprintf(
		"INSERT INTO usage_records (%s) VALUES (%s) ON CONFLICT(id) DO NOTHING",
		strings.Join(UsageRecordColumns, ", "),
		sqlitePlaceholders(len(UsageRecordColumns)),
	)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, err
	}
	statement, err := tx.Prepare(insertSQL)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer statement.Close()
	inserted := 0
	for _, row := range rows {
		result, err := statement.Exec(row.Params...)
		if err != nil {
			tx.Rollback()
			return 0, err
		}
		if affected, err := result.RowsAffected(); err == nil && affected > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// recordShardEntries mirrors recordUsageRecordShardEntries: register new
// shard locations, upsert the catalog entries and the account/api-key scope
// catalogs inside one transaction.
func (s *SqliteShardStore) recordShardEntries(ctx Ctx, entries []ShardEntry, locations []UsageRecordShardLocation) error {
	uniqueEntries := uniqueShardEntries(entries)
	if len(uniqueEntries) == 0 {
		return nil
	}
	timestamp := s.nowIso()
	db := s.config.CatalogDB
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	seenLocations := map[string]bool{}
	registerLocation, err := tx.Prepare(`
      INSERT INTO usage_record_shards (
        shard_key, bucket_date, shard_id, file_path, schema_version, status,
        first_seen_at, last_write_at, last_error_message, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'active', ?, NULL, NULL, ?, ?)
      ON CONFLICT(shard_key) DO UPDATE SET
        bucket_date = excluded.bucket_date,
        shard_id = excluded.shard_id,
        file_path = excluded.file_path,
        schema_version = excluded.schema_version,
        status = 'active',
        updated_at = excluded.updated_at
    `)
	if err != nil {
		return err
	}
	defer registerLocation.Close()
	register := func(location UsageRecordShardLocation) error {
		if seenLocations[location.ShardKey] {
			return nil
		}
		seenLocations[location.ShardKey] = true
		_, err := registerLocation.Exec(location.ShardKey, location.BucketDate, location.ShardID, location.FilePath, usageRecordShardSchemaVersion, timestamp, timestamp, timestamp)
		return err
	}
	for _, location := range uniqueLocations(locations) {
		if err := register(location); err != nil {
			return err
		}
	}
	for _, entry := range uniqueEntries {
		if location, ok := UsageRecordShardLocationFromKey(entry.ShardKey, s.config.ShardRoot); ok {
			if err := register(location); err != nil {
				return err
			}
		}
	}

	entryStatement, err := tx.Prepare(`
      INSERT INTO usage_record_shard_entries (
        usage_id, shard_key, system_account_id, trace_id, api_key_id, account_id, group_id, model, traffic_source,
        success, status_code, client_ip, first_token_ms, duration_ms, cost_usd, created_at, indexed_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(usage_id) DO UPDATE SET
        shard_key = excluded.shard_key,
        system_account_id = excluded.system_account_id,
        trace_id = excluded.trace_id,
        api_key_id = excluded.api_key_id,
        account_id = excluded.account_id,
        group_id = excluded.group_id,
        model = excluded.model,
        traffic_source = excluded.traffic_source,
        success = excluded.success,
        status_code = excluded.status_code,
        client_ip = excluded.client_ip,
        first_token_ms = excluded.first_token_ms,
        duration_ms = excluded.duration_ms,
        cost_usd = excluded.cost_usd,
        created_at = excluded.created_at,
        indexed_at = excluded.indexed_at
      WHERE usage_record_shard_entries.shard_key IS NOT excluded.shard_key
        OR usage_record_shard_entries.system_account_id IS NOT excluded.system_account_id
        OR usage_record_shard_entries.trace_id IS NOT excluded.trace_id
        OR usage_record_shard_entries.api_key_id IS NOT excluded.api_key_id
        OR usage_record_shard_entries.account_id IS NOT excluded.account_id
        OR usage_record_shard_entries.group_id IS NOT excluded.group_id
        OR usage_record_shard_entries.model IS NOT excluded.model
        OR usage_record_shard_entries.traffic_source IS NOT excluded.traffic_source
        OR usage_record_shard_entries.success IS NOT excluded.success
        OR usage_record_shard_entries.status_code IS NOT excluded.status_code
        OR usage_record_shard_entries.client_ip IS NOT excluded.client_ip
        OR usage_record_shard_entries.first_token_ms IS NOT excluded.first_token_ms
        OR usage_record_shard_entries.duration_ms IS NOT excluded.duration_ms
        OR usage_record_shard_entries.cost_usd IS NOT excluded.cost_usd
        OR usage_record_shard_entries.created_at IS NOT excluded.created_at
    `)
	if err != nil {
		return err
	}
	defer entryStatement.Close()
	for _, entry := range uniqueEntries {
		if _, err := entryStatement.Exec(
			entry.ID,
			entry.ShardKey,
			entry.SystemAccountID,
			entry.TraceID,
			entry.APIKeyID,
			entry.AccountID,
			entry.GroupID,
			entry.Model,
			entry.TrafficSource,
			boolToInt(&entry.Success),
			entry.StatusCode,
			entry.ClientIP,
			entry.FirstTokenMs,
			entry.DurationMs,
			entry.CostUsd,
			entry.CreatedAt,
			timestamp,
		); err != nil {
			return err
		}
	}
	if err := upsertScopeShardCatalog(ctx, tx, uniqueEntries); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		tx = nil
		return err
	}
	tx = nil
	return nil
}

// upsertScopeShardCatalog mirrors upsertUsageRecordScopeShardCatalog.
func upsertScopeShardCatalog(ctx Ctx, tx *sql.Tx, entries []ShardEntry) error {
	accountRows := map[string]*ShardEntry{}
	apiKeyRows := map[string]*ShardEntry{}
	for _, entry := range entries {
		if accountID := trimValue(entry.AccountID); accountID != "" {
			key := accountID + "\x00" + entry.ShardKey
			if existing, exists := accountRows[key]; exists {
				mergeFirstLast(existing, entry)
			} else {
				clone := entry
				clone.AccountID = &accountID
				accountRows[key] = &clone
			}
		}
		apiKeyID := trimValue(entry.APIKeyID)
		systemAccountID := strings.TrimSpace(entry.SystemAccountID)
		if apiKeyID != "" && systemAccountID != "" {
			key := apiKeyID + "\x00" + systemAccountID + "\x00" + entry.ShardKey
			if existing, exists := apiKeyRows[key]; exists {
				mergeFirstLast(existing, entry)
			} else {
				clone := entry
				clone.APIKeyID = &apiKeyID
				clone.SystemAccountID = systemAccountID
				apiKeyRows[key] = &clone
			}
		}
	}

	accountStatement, err := tx.Prepare(`
      INSERT INTO usage_record_account_shards (account_id, shard_key, first_created_at, last_seen_at)
      VALUES (?, ?, ?, ?)
      ON CONFLICT(account_id, shard_key) DO UPDATE SET
        first_created_at = CASE
          WHEN excluded.first_created_at < usage_record_account_shards.first_created_at THEN excluded.first_created_at
          ELSE usage_record_account_shards.first_created_at
        END,
        last_seen_at = CASE
          WHEN excluded.last_seen_at > usage_record_account_shards.last_seen_at THEN excluded.last_seen_at
          ELSE usage_record_account_shards.last_seen_at
        END
      WHERE excluded.first_created_at < usage_record_account_shards.first_created_at
        OR excluded.last_seen_at > usage_record_account_shards.last_seen_at
    `)
	if err != nil {
		return err
	}
	defer accountStatement.Close()
	for _, row := range accountRows {
		if _, err := accountStatement.Exec(*row.AccountID, row.ShardKey, row.CreatedAt, row.CreatedAt); err != nil {
			return err
		}
	}

	apiKeyStatement, err := tx.Prepare(`
      INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key, first_created_at, last_seen_at)
      VALUES (?, ?, ?, ?, ?)
      ON CONFLICT(api_key_id, system_account_id, shard_key) DO UPDATE SET
        first_created_at = CASE
          WHEN excluded.first_created_at < usage_record_api_key_shards.first_created_at THEN excluded.first_created_at
          ELSE usage_record_api_key_shards.first_created_at
        END,
        last_seen_at = CASE
          WHEN excluded.last_seen_at > usage_record_api_key_shards.last_seen_at THEN excluded.last_seen_at
          ELSE usage_record_api_key_shards.last_seen_at
        END
      WHERE excluded.first_created_at < usage_record_api_key_shards.first_created_at
        OR excluded.last_seen_at > usage_record_api_key_shards.last_seen_at
    `)
	if err != nil {
		return err
	}
	defer apiKeyStatement.Close()
	for _, row := range apiKeyRows {
		if _, err := apiKeyStatement.Exec(*row.APIKeyID, row.SystemAccountID, row.ShardKey, row.CreatedAt, row.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func mergeFirstLast(existing *ShardEntry, incoming ShardEntry) {
	if incoming.CreatedAt < existing.CreatedAt {
		existing.CreatedAt = incoming.CreatedAt
	}
}

func trimValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func uniqueShardEntries(entries []ShardEntry) []ShardEntry {
	unique := map[string]ShardEntry{}
	order := []string{}
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, exists := unique[id]; !exists {
			order = append(order, id)
		}
		unique[id] = entry
	}
	out := make([]ShardEntry, 0, len(order))
	for _, id := range order {
		out = append(out, unique[id])
	}
	return out
}

func uniqueLocations(locations []UsageRecordShardLocation) []UsageRecordShardLocation {
	unique := map[string]UsageRecordShardLocation{}
	order := []string{}
	for _, location := range locations {
		if _, exists := unique[location.ShardKey]; !exists {
			order = append(order, location.ShardKey)
		}
		unique[location.ShardKey] = location
	}
	out := make([]UsageRecordShardLocation, 0, len(order))
	for _, key := range order {
		out = append(out, unique[key])
	}
	return out
}

// flushBusinessSideEffects mirrors flushUsageRecordBusinessSideEffects: the
// accounts.last_used_at update is warn-only and skipped without a business
// DB (the Node queryOnly case).
func (s *SqliteShardStore) flushBusinessSideEffects(lastUsedAt map[string]string) {
	if len(lastUsedAt) == 0 || s.config.BusinessDB == nil {
		return
	}
	tx, err := s.config.BusinessDB.BeginTx(context.Background(), nil)
	if err != nil {
		return
	}
	statement, err := tx.Prepare(`
      UPDATE accounts
      SET last_used_at = ?, updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND (last_used_at IS NULL OR last_used_at < ?)
    `)
	if err != nil {
		tx.Rollback()
		return
	}
	defer statement.Close()
	for _, accountID := range sortedAccountIDs(lastUsedAt) {
		lastUsed := lastUsedAt[accountID]
		if _, err := statement.Exec(lastUsed, lastUsed, accountID, lastUsed); err != nil {
			tx.Rollback()
			return
		}
	}
	tx.Commit()
}

func sortedAccountIDs(values map[string]string) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

// Close closes every cached shard database.
func (s *SqliteShardStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, db := range s.shardDBs {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.shardDBs = map[string]*sql.DB{}
	return firstErr
}

// PostgresShardStoreConfig mirrors the facts the Postgres write path
// consumes (createUsageRecordsBatchPostgres).
type PostgresShardStoreConfig struct {
	// DB is the usage pool client.
	DB *sql.DB
	// ShardCount mirrors runtimeConfig.usageShardCount.
	ShardCount int
	// BusinessDB carries the business pool for the accounts side effect; nil
	// keeps the side effect inside the main transaction like Node does.
	BusinessDB *sql.DB
	// Now substitutes nowIso(); nil = wall clock.
	Now func() time.Time
}

// PostgresShardStore mirrors createUsageRecordsBatchPostgres: one
// transaction locking the side-effect accounts, inserting the usage rows
// (ON CONFLICT(created_at, id) DO NOTHING) and flushing last_used_at.
type PostgresShardStore struct {
	config PostgresShardStoreConfig
}

// NewPostgresShardStore builds the store.
func NewPostgresShardStore(config PostgresShardStoreConfig) *PostgresShardStore {
	if config.ShardCount < 1 {
		config.ShardCount = DefaultUsageShardCount
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now() }
	}
	return &PostgresShardStore{config: config}
}

// WriteBatch implements ShardStore.
func (s *PostgresShardStore) WriteBatch(ctx Ctx, plan WritePlan) (int, error) {
	if len(plan.RowsByShard) == 0 {
		return 0, nil
	}
	lastUsedAt := map[string]string{}
	healthSuccessAt := map[string]string{}
	for _, shardRows := range plan.RowsByShard {
		MergeShardWriteResult(lastUsedAt, healthSuccessAt, shardRows.Rows)
	}

	tx, err := s.config.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	accountIDs := map[string]bool{}
	for id := range lastUsedAt {
		accountIDs[id] = true
	}
	for id := range healthSuccessAt {
		accountIDs[id] = true
	}
	if len(accountIDs) > 0 {
		ids := sortedAccountIDList(accountIDs)
		lockSQL := fmt.Sprintf(`
      SELECT id
      FROM juhe_business.accounts
      WHERE id IN (%s)
        AND deleted_at IS NULL
      ORDER BY id
      FOR NO KEY UPDATE
    `, sqlitePlaceholders(len(ids)))
		args := make([]any, 0, len(ids))
		for _, id := range ids {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx, lockSQL, args...); err != nil {
			return 0, err
		}
	}

	inserted := 0
	columnList := strings.Join(UsageRecordColumns, ", ")
	for _, shardRows := range plan.RowsByShard {
		if len(shardRows.Rows) == 0 {
			continue
		}
		rowPlaceholders := make([]string, 0, len(shardRows.Rows))
		args := make([]any, 0, len(shardRows.Rows)*len(UsageRecordColumns))
		for _, row := range shardRows.Rows {
			rowPlaceholders = append(rowPlaceholders, "("+sqlitePlaceholders(len(UsageRecordColumns))+")")
			args = append(args, row.Params...)
		}
		insertSQL := fmt.Sprintf(`
      INSERT INTO juhe_usage.usage_records (%s)
      VALUES %s
      ON CONFLICT(created_at, id) DO NOTHING
    `, columnList, strings.Join(rowPlaceholders, ", "))
		result, err := tx.ExecContext(ctx, insertSQL, args...)
		if err != nil {
			return 0, err
		}
		if affected, err := result.RowsAffected(); err == nil {
			inserted += int(affected)
		}
	}

	for _, accountID := range sortedAccountIDs(lastUsedAt) {
		lastUsed := lastUsedAt[accountID]
		if _, err := tx.ExecContext(ctx, `
      UPDATE juhe_business.accounts
      SET last_used_at = ?, updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND (last_used_at IS NULL OR last_used_at < ?)
    `, lastUsed, lastUsed, accountID, lastUsed); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		tx = nil
		return 0, err
	}
	tx = nil
	return inserted, nil
}

func sortedAccountIDList(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

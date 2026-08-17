package auditlog

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const sqliteBusyTimeoutMS = 5000

const (
	auditBlobCompressionThresholdBytes = 4 * 1024
	auditBlobCompressionMaxBytes       = 1024 * 1024
	postgresBlobLifecycleLockSeed      = 763847294
	postgresAuditLogLifecycleLockSeed  = 763847295
)

var ErrOwnerLeaseLost = errors.New("F3 audit owner lease 已失效或已移交")

type sqlStore struct {
	db          *sql.DB
	mode        Mode
	blobDir     string
	hotDir      string
	writeMu     sync.Mutex
	hotMu       sync.Mutex
	schemaMu    sync.Mutex
	schemaReady bool
}

type blobPlan struct {
	record   blobRecord
	rawBytes []byte
	bytes    []byte // bytes exactly as stored (possibly gzip-compressed)
	root     string
	tempPath string
	existing bool
}

const sqliteBlobGCSchema = `
CREATE TABLE IF NOT EXISTS audit_payload_blob_gc (
  blob_id TEXT PRIMARY KEY, storage_key TEXT NOT NULL, scheduled_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_payload_blob_gc_scheduled ON audit_payload_blob_gc(scheduled_at, blob_id);
`

const postgresBlobGCSchema = `
CREATE TABLE IF NOT EXISTS juhe_dataset.audit_payload_blob_gc (
  blob_id text PRIMARY KEY, storage_key text NOT NULL, scheduled_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_payload_blob_gc_scheduled ON juhe_dataset.audit_payload_blob_gc(scheduled_at, blob_id);
`

// PostgreSQL lease predicates use the database's wall clock.  The first
// predicate is deliberately non-locking so concurrent Persist calls do not
// serialize around the sole lease row.  The final predicate is a no-op UPDATE
// immediately before Commit; it evaluates clock_timestamp() while taking the
// row lock, which holds any successor handoff until this commit resolves.
const postgresAcquireLeaseSQL = `INSERT INTO juhe_dataset.audit_log_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES (?, ?, 1, clock_timestamp() + (? * INTERVAL '1 millisecond'), clock_timestamp())
ON CONFLICT (lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id, fence_token=juhe_dataset.audit_log_owner_leases.fence_token+1, lease_until=EXCLUDED.lease_until, updated_at=clock_timestamp()
WHERE juhe_dataset.audit_log_owner_leases.lease_until <= clock_timestamp() RETURNING fence_token`

const postgresRenewLeaseSQL = `UPDATE juhe_dataset.audit_log_owner_leases SET lease_until=clock_timestamp() + (? * INTERVAL '1 millisecond'),updated_at=clock_timestamp() WHERE lease_key=? AND owner_id=? AND fence_token=? AND lease_until > clock_timestamp()`

const postgresInitialFenceSQL = `SELECT 1 FROM juhe_dataset.audit_log_owner_leases WHERE lease_key=? AND owner_id=? AND fence_token=? AND lease_until > clock_timestamp()`

const postgresCommitFenceSQL = `UPDATE juhe_dataset.audit_log_owner_leases SET updated_at=updated_at WHERE lease_key=? AND owner_id=? AND fence_token=? AND lease_until > clock_timestamp() RETURNING fence_token`

func OpenStore(cfg Config) (Store, error) {
	// Opening a store (and the current foundation CLI) has no blob-directory
	// side effects. Blob files may only be created or cleaned by an acquired,
	// fenced owner during Persist/CleanupOwnedBlobTemps.
	if cfg.Mode == ModeSQLite {
		dsn, err := sqliteDSN(cfg.AuditDatabasePath)
		if err != nil {
			return nil, fmt.Errorf("解析 F3 SQLite 专库路径失败: %w", err)
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, fmt.Errorf("打开 F3 SQLite 专库失败: %w", err)
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := configureSQLite(db); err != nil {
			_ = db.Close()
			return nil, err
		}
		hotDir := cfg.HotSearchDirectory
		if hotDir == "" {
			hotDir = filepath.Join(filepath.Dir(cfg.PayloadBlobDirectory), "search-hot")
		}
		return &sqlStore{db: db, mode: cfg.Mode, blobDir: cfg.PayloadBlobDirectory, hotDir: hotDir}, nil
	}
	db, err := sql.Open("pgx", cfg.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("打开 F3 PostgreSQL 失败: %w", err)
	}
	// The pool is deliberately bounded: direct F3 writes use the actual DB
	// transaction capacity rather than a synthetic task queue.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	hotDir := cfg.HotSearchDirectory
	if hotDir == "" {
		hotDir = filepath.Join(filepath.Dir(cfg.PayloadBlobDirectory), "search-hot")
	}
	return &sqlStore{db: db, mode: cfg.Mode, blobDir: cfg.PayloadBlobDirectory, hotDir: hotDir}, nil
}

func sqliteDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "_pragma=busy_timeout(5000)"}).String(), nil
}

func configureSQLite(db *sql.DB) error {
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("启用 F3 SQLite foreign_keys 失败: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", sqliteBusyTimeoutMS)); err != nil {
		return fmt.Errorf("设置 F3 SQLite busy_timeout 失败: %w", err)
	}
	var timeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		return fmt.Errorf("回读 F3 SQLite busy_timeout 失败: %w", err)
	}
	if timeout != sqliteBusyTimeoutMS {
		return fmt.Errorf("F3 SQLite busy_timeout 未生效，实际为 %d", timeout)
	}
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journal); err != nil {
		return fmt.Errorf("启用 F3 SQLite WAL 失败: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(journal), "wal") {
		return fmt.Errorf("F3 SQLite WAL 未生效，实际为 %q", journal)
	}
	return nil
}

func (s *sqlStore) Close() error { return s.db.Close() }

func (s *sqlStore) EnsureSchema(ctx context.Context) error {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		if _, err := s.db.ExecContext(ctx, sqliteSchema); err != nil {
			return fmt.Errorf("初始化 F3 SQLite schema 失败: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, sqliteBlobGCSchema); err != nil {
			return fmt.Errorf("初始化 F3 SQLite blob GC schema 失败: %w", err)
		}
	} else {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("开始 F3 PostgreSQL schema 事务失败: %w", err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(763847293)"); err != nil {
			return fmt.Errorf("获取 F3 PostgreSQL schema 锁失败: %w", err)
		}
		for _, statement := range strings.Split(postgresSchema, ";") {
			if statement = strings.TrimSpace(statement); statement == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("初始化 F3 PostgreSQL schema 失败: %w", err)
			}
		}
		for _, statement := range strings.Split(postgresBlobGCSchema, ";") {
			if statement = strings.TrimSpace(statement); statement == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("初始化 F3 PostgreSQL blob GC schema 失败: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("提交 F3 PostgreSQL schema 失败: %w", err)
		}
	}
	s.schemaReady = true
	return nil
}

func (s *sqlStore) leaseTable() string {
	if s.mode == ModePostgres {
		return "juhe_dataset.audit_log_owner_leases"
	}
	return "audit_log_owner_leases"
}
func (s *sqlStore) table(name string) string {
	if s.mode == ModePostgres {
		return "juhe_dataset." + name
	}
	return name
}

func (s *sqlStore) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return OwnerLease{}, false, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	if s.mode == ModePostgres {
		var token int64
		err := s.db.QueryRowContext(ctx, s.bind(postgresAcquireLeaseSQL), "f3-audit-log-persistence", ownerID, duration.Milliseconds()).Scan(&token)
		if errors.Is(err, sql.ErrNoRows) {
			return OwnerLease{}, false, nil
		}
		if err != nil {
			return OwnerLease{}, false, fmt.Errorf("获取 F3 PostgreSQL owner lease 失败: %w", err)
		}
		return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
	}
	now := time.Now().UTC()
	until := now.Add(duration)
	query := `INSERT INTO ` + s.leaseTable() + ` (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES (?, ?, 1, ?, ?) ON CONFLICT (lease_key) DO UPDATE SET owner_id=excluded.owner_id,
fence_token=` + s.leaseTable() + `.fence_token + 1, lease_until=excluded.lease_until, updated_at=excluded.updated_at
WHERE ` + s.leaseTable() + `.lease_until <= ? RETURNING fence_token`
	var token int64
	err := s.db.QueryRowContext(ctx, s.bind(query), "f3-audit-log-persistence", ownerID, dbTime(s.mode, until), dbTime(s.mode, now), dbTime(s.mode, now)).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, fmt.Errorf("获取 F3 owner lease 失败: %w", err)
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func (s *sqlStore) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return false, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	if s.mode == ModePostgres {
		result, err := s.db.ExecContext(ctx, s.bind(postgresRenewLeaseSQL), duration.Milliseconds(), "f3-audit-log-persistence", lease.OwnerID, lease.FenceToken)
		if err != nil {
			return false, fmt.Errorf("续约 F3 PostgreSQL owner lease 失败: %w", err)
		}
		count, err := result.RowsAffected()
		return count == 1, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.leaseTable()+` SET lease_until=?, updated_at=? WHERE lease_key=? AND owner_id=? AND fence_token=? AND lease_until > ?`), dbTime(s.mode, now.Add(duration)), dbTime(s.mode, now), "f3-audit-log-persistence", lease.OwnerID, lease.FenceToken, dbTime(s.mode, now))
	if err != nil {
		return false, fmt.Errorf("续约 F3 owner lease 失败: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *sqlStore) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error {
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	// Keep the fence row and only expire it. Deleting it would reset the next
	// token to 1, allowing a delayed former owner to become indistinguishable
	// from a new owner after a voluntary handoff.
	query := `UPDATE ` + s.leaseTable() + ` SET lease_until=?, updated_at=? WHERE lease_key=? AND owner_id=? AND fence_token=?`
	arguments := []any{dbTime(s.mode, time.Unix(0, 0).UTC()), dbTime(s.mode, time.Now().UTC()), "f3-audit-log-persistence", lease.OwnerID, lease.FenceToken}
	if s.mode == ModePostgres {
		query = `UPDATE ` + s.leaseTable() + ` SET lease_until=to_timestamp(0),updated_at=clock_timestamp() WHERE lease_key=? AND owner_id=? AND fence_token=?`
		arguments = []any{"f3-audit-log-persistence", lease.OwnerID, lease.FenceToken}
	}
	result, err := s.db.ExecContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return fmt.Errorf("释放 F3 owner lease 失败: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrOwnerLeaseLost
	}
	return nil
}

func (s *sqlStore) Persist(ctx context.Context, lease OwnerLease, input AuditLogInput) (PersistResult, error) {
	var err error
	input, err = normalizeAuditInput(input)
	if err != nil {
		return PersistResult{}, err
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return PersistResult{}, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PersistResult{}, err
	}
	committed := false
	plans := []blobPlan(nil)
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
		_ = cleanupBlobTemps(plans)
	}()
	if err := s.verifyLeaseTx(ctx, tx, lease); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	if err := s.lockAuditLogLifecycleTx(ctx, tx, input.ID); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	prepared, plans, err := s.planPayloads(ctx, tx, input)
	if err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	if err := s.lockBlobLifecyclePlans(ctx, tx, plans); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	if err := s.reactivateScheduledBlobGC(ctx, tx, plans); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	prepared, plans, err = s.prepareExistingBlobRecords(ctx, tx, prepared, plans)
	if err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	wroteParent, err := s.upsertLog(ctx, tx, input, prepared)
	if err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	if !wroteParent {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return PersistResult{}, errors.Join(fmt.Errorf("重复或迟到的审计输入被忽略"), rollbackErr)
		}
		return PersistResult{Ignored: true}, nil
	}
	for index := range plans {
		plans[index].root = s.blobDir
	}
	if err := verifyExistingBlobFiles(plans); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	if err := s.writeBlobTemps(lease, plans); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	if err := publishBlobPlans(plans); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	if err := s.resolveBlobIDs(ctx, tx, plans); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	prepared = attachBlobRecords(prepared, plans)
	if normalizeLifecycle(input.LifecycleStatus) == LifecycleFinalized {
		if err := s.insertFinalChildren(ctx, tx, input, prepared); err != nil {
			return PersistResult{}, errors.Join(err, tx.Rollback())
		}
	}
	if err := s.verifyLeaseBeforeCommit(ctx, tx, lease); err != nil {
		return PersistResult{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		// Commit outcome can be unknown. Published blobs are retained and the
		// later reference-aware cleanup owns any orphan decision.
		return PersistResult{}, fmt.Errorf("提交 F3 审计事务失败，无法判断提交结果；已保留已发布 blob 等待引用清理: %w", err)
	}
	committed = true
	return PersistResult{}, nil
}

// PostgreSQL transactions that can both mutate an audit event and its blob
// references enter through this per-audit-ID gate before taking a blob lock or
// an audit row lock. It prevents Persist from holding a blob lifecycle lock
// while retention holds the same audit row and waits for that blob lock.
func (s *sqlStore) lockAuditLogLifecycleTx(ctx context.Context, tx *sql.Tx, auditLogID string) error {
	if s.mode != ModePostgres || auditLogID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, s.bind(`SELECT pg_advisory_xact_lock(hashtextextended(?, `+strconv.FormatInt(postgresAuditLogLifecycleLockSeed, 10)+`::bigint))`), auditLogID); err != nil {
		return fmt.Errorf("获取 F3 audit log 生命周期锁失败: %w", err)
	}
	return nil
}

func (s *sqlStore) lockAuditLogLifecycleIDs(ctx context.Context, tx *sql.Tx, auditLogIDs []string) error {
	ids := uniqueStrings(auditLogIDs)
	sort.Strings(ids)
	for _, id := range ids {
		if err := s.lockAuditLogLifecycleTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// lockBlobLifecyclePlans serializes only a canonical blob's lifecycle in
// PostgreSQL. Persist takes the audit-ID lifecycle gate first, while different
// audit IDs remain concurrent until they contend for the same canonical blob.
func (s *sqlStore) lockBlobLifecyclePlans(ctx context.Context, tx *sql.Tx, plans []blobPlan) error {
	ids := make([]string, 0, len(plans))
	seen := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		if plan.record.id == "" {
			continue
		}
		if _, ok := seen[plan.record.id]; ok {
			continue
		}
		seen[plan.record.id] = struct{}{}
		ids = append(ids, plan.record.id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := s.lockBlobLifecycleTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqlStore) lockBlobLifecycleTx(ctx context.Context, tx *sql.Tx, blobID string) error {
	if s.mode != ModePostgres || blobID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, s.bind(`SELECT pg_advisory_xact_lock(hashtextextended(?, `+strconv.FormatInt(postgresBlobLifecycleLockSeed, 10)+`::bigint))`), blobID); err != nil {
		return fmt.Errorf("获取 F3 blob 生命周期锁失败: %w", err)
	}
	return nil
}

// reactivateScheduledBlobGC cancels a pending deletion while the same
// per-blob lock is held. If an earlier file deletion succeeded but the
// metadata transaction did not, it removes that stale metadata first so this
// Persist can publish a fresh canonical file rather than reference a missing
// one.
func (s *sqlStore) reactivateScheduledBlobGC(ctx context.Context, tx *sql.Tx, plans []blobPlan) error {
	ids := make([]string, 0, len(plans))
	seen := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		if plan.record.id == "" {
			continue
		}
		if _, ok := seen[plan.record.id]; ok {
			continue
		}
		seen[plan.record.id] = struct{}{}
		ids = append(ids, plan.record.id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var storageKey string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT storage_key FROM `+s.table("audit_payload_blob_gc")+` WHERE blob_id=?`), id).Scan(&storageKey)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("读取 F3 pending blob GC 失败: %w", err)
		}

		var compressedSize int64
		err = tx.QueryRowContext(ctx, s.bind(`SELECT compressed_size_bytes FROM `+s.table("audit_payload_blobs")+` WHERE id=?`), id).Scan(&compressedSize)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_payload_blob_gc")+` WHERE blob_id=?`), id); err != nil {
				return fmt.Errorf("清理失配的 F3 pending blob GC 失败: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("读取 F3 pending blob 元数据失败: %w", err)
		}

		present, err := blobFileMatchesMetadata(s.blobDir, storageKey, compressedSize)
		if err != nil {
			return err
		}
		if !present {
			deleted, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_payload_blobs")+` WHERE id=? AND NOT EXISTS (SELECT 1 FROM `+s.table("audit_payload_refs")+` WHERE headers_blob_id=? OR body_blob_id=?)`), id, id, id)
			if err != nil {
				return fmt.Errorf("删除缺失物理文件的 F3 pending blob 元数据失败: %w", err)
			}
			if affected, affectedErr := deleted.RowsAffected(); affectedErr != nil {
				return fmt.Errorf("读取缺失物理文件的 F3 pending blob 删除结果失败: %w", affectedErr)
			} else if affected != 1 {
				return fmt.Errorf("F3 pending blob 在恢复期间重新获得引用: id=%s", id)
			}
		}
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_payload_blob_gc")+` WHERE blob_id=?`), id); err != nil {
			return fmt.Errorf("取消 F3 pending blob GC 失败: %w", err)
		}
	}
	return nil
}

func (s *sqlStore) verifyLeaseTx(ctx context.Context, tx *sql.Tx, lease OwnerLease) error {
	if lease.OwnerID == "" || lease.FenceToken <= 0 {
		return ErrOwnerLeaseLost
	}
	if s.mode == ModeSQLite {
		// This matching UPDATE acquires SQLite's writer reservation before any
		// audit rows are changed. A separate F3 process cannot advance the
		// expired lease between this fence check and COMMIT.
		result, err := tx.ExecContext(ctx, `UPDATE audit_log_owner_leases SET updated_at = updated_at
WHERE lease_key=? AND owner_id=? AND fence_token=? AND lease_until > ?`, "f3-audit-log-persistence", lease.OwnerID, lease.FenceToken, dbTime(s.mode, time.Now().UTC()))
		if err != nil {
			return fmt.Errorf("校验 F3 SQLite owner lease fence 失败: %w", err)
		}
		matched, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("读取 F3 SQLite owner lease fence 结果失败: %w", err)
		}
		if matched != 1 {
			return ErrOwnerLeaseLost
		}
		return nil
	}
	var found int
	err := tx.QueryRowContext(ctx, s.bind(postgresInitialFenceSQL), "f3-audit-log-persistence", lease.OwnerID, lease.FenceToken).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOwnerLeaseLost
	}
	if err != nil {
		return fmt.Errorf("校验 F3 owner lease fence 失败: %w", err)
	}
	return nil
}

func (s *sqlStore) verifyLeaseBeforeCommit(ctx context.Context, tx *sql.Tx, lease OwnerLease) error {
	if s.mode == ModeSQLite {
		return s.verifyLeaseTx(ctx, tx, lease)
	}
	var token int64
	err := tx.QueryRowContext(ctx, s.bind(postgresCommitFenceSQL), "f3-audit-log-persistence", lease.OwnerID, lease.FenceToken).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOwnerLeaseLost
	}
	if err != nil {
		return fmt.Errorf("提交前校验 F3 PostgreSQL owner lease fence 失败: %w", err)
	}
	if token != lease.FenceToken {
		return ErrOwnerLeaseLost
	}
	return nil
}

type preparedPayload struct {
	input                   AuditLogPayloadInput
	headers, body           *blobRecord
	headersSHA, bodySHA     string
	rawSize, compressedSize int64
	attemptID               string
	sequenceIndex           int
}
type blobRecord struct {
	id, sha256, contentType, contentEncoding, storageKey string
	compression                                          string
	rawSize, compressedSize                              int64
}

func (s *sqlStore) planPayloads(ctx context.Context, tx *sql.Tx, input AuditLogInput) ([]preparedPayload, []blobPlan, error) {
	result := make([]preparedPayload, 0, len(input.Payloads))
	plans := make([]blobPlan, 0, len(input.Payloads)*2)
	for index, payload := range input.Payloads {
		sequenceIndex := index
		if payload.SequenceIndex != nil {
			sequenceIndex = *payload.SequenceIndex
		}
		entry := preparedPayload{input: payload, sequenceIndex: sequenceIndex}
		if payload.AttemptTempID != "" {
			entry.attemptID = payload.AttemptTempID
		}
		if payload.Headers != nil {
			bytes, err := marshalNodeHeaders(payload.Headers)
			if err != nil {
				return nil, nil, fmt.Errorf("编码 audit headers 失败: %w", err)
			}
			record, storedBytes, err := newBlobRecord(bytes, "application/json; audit=headers", "")
			if err != nil {
				return nil, nil, err
			}
			entry.headers = &record
			entry.headersSHA, entry.rawSize, entry.compressedSize = record.sha256, record.rawSize, record.compressedSize
			plans = append(plans, blobPlan{record: record, rawBytes: bytes, bytes: storedBytes})
		}
		if payload.Body.Present {
			contentType := payload.ContentType
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			record, storedBytes, err := newBlobRecord(payload.Body.Bytes, contentType, payload.ContentEncoding)
			if err != nil {
				return nil, nil, err
			}
			entry.body = &record
			entry.bodySHA = record.sha256
			entry.compressedSize += record.compressedSize
			plans = append(plans, blobPlan{record: record, rawBytes: payload.Body.Bytes, bytes: storedBytes})
		}
		// Node treats these input fields as explicit capture metadata. They take
		// precedence even when a body was retained locally (e.g. a truncated
		// body with source-side hash/size), while stored-byte totals still use
		// the actual selected blob representation.
		if payload.BodySHA256 != "" {
			entry.bodySHA = payload.BodySHA256
		}
		rawBodySize := int64(0)
		if payload.RawBodySizeBytes != nil {
			rawBodySize = nonNegative(*payload.RawBodySizeBytes)
		} else if entry.body != nil {
			rawBodySize = entry.body.rawSize
		}
		entry.rawSize += rawBodySize
		result = append(result, entry)
	}
	return result, plans, nil
}

func newBlobRecord(rawBytes []byte, contentType, contentEncoding string) (blobRecord, []byte, error) {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	storedBytes, compression, err := compressAuditBlob(rawBytes, contentType, contentEncoding)
	if err != nil {
		return blobRecord{}, nil, fmt.Errorf("压缩 audit payload blob 失败: %w", err)
	}
	sum := sha256.Sum256(rawBytes)
	digest := hex.EncodeToString(sum[:])
	suffix := ".blob"
	if compression == "gzip" {
		suffix = ".gz"
	}
	// The database canonical identity includes content type. Keep physical
	// objects separate as well: the same raw bytes can legitimately compress
	// differently under two content types.
	storageKey := filepath.Join("sha256", digest+"-"+shortHash(contentType)+suffix)
	return blobRecord{id: "blob:" + digest + ":" + shortHash(contentType), sha256: digest, contentType: contentType, contentEncoding: contentEncoding, compression: compression, storageKey: filepath.ToSlash(storageKey), rawSize: int64(len(rawBytes)), compressedSize: int64(len(storedBytes))}, storedBytes, nil
}

func compressAuditBlob(rawBytes []byte, contentType, contentEncoding string) ([]byte, string, error) {
	if len(rawBytes) < auditBlobCompressionThresholdBytes || len(rawBytes) > auditBlobCompressionMaxBytes || !isCompressibleAuditPayload(contentType, contentEncoding) {
		return rawBytes, "none", nil
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(rawBytes); err != nil {
		_ = writer.Close()
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	if compressed.Len() >= len(rawBytes) {
		return rawBytes, "none", nil
	}
	return compressed.Bytes(), "gzip", nil
}

func isCompressibleAuditPayload(contentType, contentEncoding string) bool {
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	if encoding != "" && encoding != "identity" {
		return false
	}
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "json") || strings.Contains(contentType, "text") || strings.Contains(contentType, "xml") || strings.Contains(contentType, "event-stream") || strings.Contains(contentType, "javascript") || strings.Contains(contentType, "x-www-form-urlencoded")
}

func (s *sqlStore) writeBlobTemps(lease OwnerLease, plans []blobPlan) error {
	for index := range plans {
		plan := &plans[index]
		finalPath := filepath.Join(plan.root, filepath.FromSlash(plan.record.storageKey))
		if info, err := os.Stat(finalPath); err == nil {
			if !info.Mode().IsRegular() || info.Size() != plan.record.compressedSize {
				return fmt.Errorf("已发布 audit blob 与待写元数据不一致: %s", finalPath)
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("检查已发布 audit blob 失败: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
			return fmt.Errorf("创建 audit blob 子目录失败: %w", err)
		}
		file, err := os.CreateTemp(filepath.Dir(finalPath), blobTempPattern(lease))
		if err != nil {
			return fmt.Errorf("创建 audit blob 临时文件失败: %w", err)
		}
		plan.tempPath = file.Name()
		if _, err := file.Write(plan.bytes); err != nil {
			_ = file.Close()
			return fmt.Errorf("写入 audit blob 临时文件失败: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("fsync audit blob 临时文件失败: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("关闭 audit blob 临时文件失败: %w", err)
		}
	}
	return nil
}

func (s *sqlStore) blobRecordFromExisting(ctx context.Context, tx *sql.Tx, record blobRecord) (blobRecord, bool, error) {
	var existing blobRecord
	var contentEncoding sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id,storage_key,content_encoding,compression,compressed_size_bytes FROM `+s.table("audit_payload_blobs")+` WHERE sha256=? AND raw_size_bytes=? AND content_type=?`), record.sha256, record.rawSize, record.contentType).Scan(&existing.id, &existing.storageKey, &contentEncoding, &existing.compression, &existing.compressedSize)
	if errors.Is(err, sql.ErrNoRows) {
		return record, false, nil
	}
	if err != nil {
		return blobRecord{}, false, fmt.Errorf("查询已有 audit blob 失败: %w", err)
	}
	if contentEncoding.Valid {
		existing.contentEncoding = contentEncoding.String
	}
	existing.sha256, existing.rawSize, existing.contentType = record.sha256, record.rawSize, record.contentType
	return existing, true, nil
}

func (s *sqlStore) prepareExistingBlobRecords(ctx context.Context, tx *sql.Tx, prepared []preparedPayload, plans []blobPlan) ([]preparedPayload, []blobPlan, error) {
	replacements := map[string]blobRecord{}
	for index := range plans {
		plan := &plans[index]
		resolved, exists, err := s.blobRecordFromExisting(ctx, tx, plan.record)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			// gzip streams contain implementation/runtime metadata. Node's
			// canonical gzip cannot be proven byte-identical by recompressing in
			// Go, so retain its metadata and only trust an existing physical file
			// after its declared storage size is observed.
			plan.record, plan.bytes, plan.existing = resolved, nil, true
			replacements[plan.record.sha256+"\x00"+plan.record.contentType] = resolved
		}
	}
	for index := range prepared {
		if prepared[index].headers != nil {
			if value, ok := replacements[prepared[index].headers.sha256+"\x00"+prepared[index].headers.contentType]; ok {
				prepared[index].headers = &value
			}
		}
		if prepared[index].body != nil {
			if value, ok := replacements[prepared[index].body.sha256+"\x00"+prepared[index].body.contentType]; ok {
				prepared[index].body = &value
			}
		}
		recomputePreparedCompressedSize(&prepared[index])
	}
	return prepared, plans, nil
}

func verifyExistingBlobFiles(plans []blobPlan) error {
	seen := map[string]struct{}{}
	for _, plan := range plans {
		if !plan.existing {
			continue
		}
		key := plan.record.id + "\x00" + plan.record.storageKey
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		info, err := os.Stat(filepath.Join(plan.root, filepath.FromSlash(plan.record.storageKey)))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("已有 canonical audit blob 缺少物理文件，拒绝伪造或重编码: id=%s", plan.record.id)
			}
			return fmt.Errorf("检查已有 canonical audit blob 文件失败: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() != plan.record.compressedSize {
			return fmt.Errorf("已有 canonical audit blob 文件与元数据不一致: id=%s", plan.record.id)
		}
	}
	return nil
}

func publishBlobPlans(plans []blobPlan) error {
	for index := range plans {
		plan := &plans[index]
		if plan.existing {
			// Existing canonical metadata was selected above. Its file has already
			// been verified; never rewrite it from a potentially different gzip
			// encoder.
			continue
		}
		if plan.tempPath == "" {
			continue
		}
		finalPath := filepath.Join(plan.root, filepath.FromSlash(plan.record.storageKey))
		if err := os.Rename(plan.tempPath, finalPath); err == nil {
			plan.tempPath = ""
			if err := syncBlobParent(filepath.Dir(finalPath)); err != nil {
				return fmt.Errorf("同步 audit blob 父目录失败: %w", err)
			}
			continue
		} else if info, statErr := os.Stat(finalPath); statErr != nil {
			return fmt.Errorf("原子发布 audit blob 失败: %w", err)
		} else if !info.Mode().IsRegular() || info.Size() != plan.record.compressedSize {
			return fmt.Errorf("并发发布的 audit blob 与元数据不一致: %s", finalPath)
		}
		// Another process won content-addressed publication. Its immutable hash
		// key is the same; only our still-private temp may be removed.
		if err := os.Remove(plan.tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("清理重复 audit blob 临时文件失败: %w", err)
		}
		plan.tempPath = ""
	}
	return nil
}

func syncBlobParent(directory string) error {
	// Windows does not provide portable directory fsync through os.File. The
	// file itself was Sync'd before Rename; do not pretend a parent-directory
	// flush is available there. On Unix, request the extra metadata flush.
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func cleanupBlobTemps(plans []blobPlan) error {
	var joined error
	for _, plan := range plans {
		if plan.tempPath == "" {
			continue
		}
		if err := os.Remove(plan.tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			joined = errors.Join(joined, fmt.Errorf("清理 audit blob 临时文件失败: %w", err))
		}
	}
	return joined
}

func blobTempOwnerKey(lease OwnerLease) string {
	return shortHash(lease.OwnerID) + "-" + strconv.FormatInt(lease.FenceToken, 10)
}

func blobTempPattern(lease OwnerLease) string {
	return ".f3-audit-blob-" + blobTempOwnerKey(lease) + "-*.tmp"
}

func (s *sqlStore) CleanupOwnedBlobTemps(ctx context.Context, lease OwnerLease, cutoff time.Time) error {
	return s.cleanupBlobTempsWithLease(ctx, lease, func() error {
		return cleanupOwnedStaleBlobTemps(s.blobDir, blobTempOwnerKey(lease), cutoff)
	})
}

func (s *sqlStore) CleanupOrphanedBlobTemps(ctx context.Context, lease OwnerLease, cutoff time.Time) error {
	return s.cleanupBlobTempsWithLease(ctx, lease, func() error {
		return cleanupOrphanedStaleBlobTemps(s.blobDir, lease.FenceToken, cutoff)
	})
}

func (s *sqlStore) cleanupBlobTempsWithLease(ctx context.Context, lease OwnerLease, cleanup func() error) error {
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始 F3 临时 blob 清理事务失败: %w", err)
	}
	defer tx.Rollback()
	if err := s.verifyLeaseTx(ctx, tx, lease); err != nil {
		return err
	}
	if err := cleanup(); err != nil {
		return fmt.Errorf("清理 F3 过期 blob 临时文件失败: %w", err)
	}
	if err := s.verifyLeaseBeforeCommit(ctx, tx, lease); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 F3 临时 blob 清理事务失败: %w", err)
	}
	return nil
}

func cleanupOwnedStaleBlobTemps(root, ownerFenceKey string, cutoff time.Time) error {
	prefix := ".f3-audit-blob-" + ownerFenceKey + "-"
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func cleanupOrphanedStaleBlobTemps(root string, currentFence int64, cutoff time.Time) error {
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".f3-audit-blob-") || !strings.HasSuffix(name, ".tmp") {
			return nil
		}
		parts := strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(name, ".f3-audit-blob-"), ".tmp"), "-", 3)
		if len(parts) != 3 || parts[0] == "" {
			return nil
		}
		fence, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil || fence <= 0 || fence >= currentFence {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *sqlStore) resolveBlobIDs(ctx context.Context, tx *sql.Tx, plans []blobPlan) error {
	for index := range plans {
		plan := &plans[index]
		query := `INSERT INTO ` + s.table("audit_payload_blobs") + ` (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,content_encoding,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at) VALUES (?,?,?,?,?,?,?,?,0,?,?,?) ON CONFLICT(sha256,raw_size_bytes,content_type) DO NOTHING RETURNING id,storage_key,content_encoding,compression,compressed_size_bytes`
		now := dbTime(s.mode, time.Now().UTC())
		var id, storageKey, compression string
		var contentEncoding sql.NullString
		var compressedSize int64
		err := tx.QueryRowContext(ctx, s.bind(query), plan.record.id, plan.record.sha256, plan.record.rawSize, plan.record.compressedSize, plan.record.contentType, nullText(plan.record.contentEncoding), plan.record.compression, plan.record.storageKey, now, now, now).Scan(&id, &storageKey, &contentEncoding, &compression, &compressedSize)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, s.bind(`SELECT id,storage_key,content_encoding,compression,compressed_size_bytes FROM `+s.table("audit_payload_blobs")+` WHERE sha256=? AND raw_size_bytes=? AND content_type=?`), plan.record.sha256, plan.record.rawSize, plan.record.contentType).Scan(&id, &storageKey, &contentEncoding, &compression, &compressedSize)
		}
		if err != nil {
			return fmt.Errorf("解析已存在 audit blob 元数据失败: %w", err)
		}
		plan.record.id, plan.record.storageKey, plan.record.compression, plan.record.compressedSize = id, storageKey, compression, compressedSize
		if contentEncoding.Valid {
			plan.record.contentEncoding = contentEncoding.String
		} else {
			plan.record.contentEncoding = ""
		}
	}
	return nil
}

func attachBlobRecords(payloads []preparedPayload, plans []blobPlan) []preparedPayload {
	byKey := make(map[string]blobRecord, len(plans))
	for _, plan := range plans {
		byKey[plan.record.sha256+"\x00"+plan.record.contentType] = plan.record
	}
	for index := range payloads {
		if payloads[index].headers != nil {
			if value, ok := byKey[payloads[index].headers.sha256+"\x00"+payloads[index].headers.contentType]; ok {
				payloads[index].headers = &value
			}
		}
		if payloads[index].body != nil {
			if value, ok := byKey[payloads[index].body.sha256+"\x00"+payloads[index].body.contentType]; ok {
				payloads[index].body = &value
			}
		}
		recomputePreparedCompressedSize(&payloads[index])
	}
	return payloads
}

func recomputePreparedCompressedSize(payload *preparedPayload) {
	payload.compressedSize = 0
	if payload.headers != nil {
		payload.compressedSize += payload.headers.compressedSize
	}
	if payload.body != nil {
		payload.compressedSize += payload.body.compressedSize
	}
}

func marshalNodeHeaders(headers map[string]HeaderValues) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(headers); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
func stableID(parts ...string) string { return strings.Join(parts, ":") }

func (s *sqlStore) upsertLog(ctx context.Context, tx *sql.Tx, input AuditLogInput, payloads []preparedPayload) (bool, error) {
	raw, compressed := int64(0), int64(0)
	for _, payload := range payloads {
		raw += payload.rawSize
		compressed += payload.compressedSize
	}
	lifecycle := normalizeLifecycle(input.LifecycleStatus)
	capture := input.CaptureStatus
	if capture == "" {
		capture = "complete"
	}
	createdAt := input.CreatedAt
	query := `INSERT INTO ` + s.table("audit_logs") + ` (id,trace_id,traffic_source,system_account_id,api_key_id,conversation_key,session_id,session_client_type,group_id,account_id,provider_code,method,path,query_string,model,upstream_model,pricing_model,model_mapping_applied,model_mapping_source,source_endpoint_family,upstream_endpoint_family,stream,client_ip,user_agent,audit_outcome,success,final_status_code,error_phase,error_code,error_message,sample_bucket,sample_reason,attempt_count,payload_count,raw_payload_bytes,compressed_payload_bytes,compression_saved_bytes,capture_status,lifecycle_status,started_at,ended_at,duration_ms,http_completed_at,http_duration_ms,first_token_ms,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET trace_id=excluded.trace_id,traffic_source=excluded.traffic_source,system_account_id=excluded.system_account_id,api_key_id=excluded.api_key_id,conversation_key=excluded.conversation_key,session_id=excluded.session_id,session_client_type=excluded.session_client_type,group_id=excluded.group_id,account_id=excluded.account_id,provider_code=excluded.provider_code,method=excluded.method,path=excluded.path,query_string=excluded.query_string,model=excluded.model,upstream_model=excluded.upstream_model,pricing_model=excluded.pricing_model,model_mapping_applied=excluded.model_mapping_applied,model_mapping_source=excluded.model_mapping_source,source_endpoint_family=excluded.source_endpoint_family,upstream_endpoint_family=excluded.upstream_endpoint_family,stream=excluded.stream,client_ip=excluded.client_ip,user_agent=excluded.user_agent,audit_outcome=excluded.audit_outcome,success=excluded.success,final_status_code=excluded.final_status_code,error_phase=excluded.error_phase,error_code=excluded.error_code,error_message=excluded.error_message,sample_bucket=excluded.sample_bucket,sample_reason=excluded.sample_reason,attempt_count=excluded.attempt_count,payload_count=excluded.payload_count,raw_payload_bytes=excluded.raw_payload_bytes,compressed_payload_bytes=excluded.compressed_payload_bytes,compression_saved_bytes=excluded.compression_saved_bytes,error_group_id=excluded.error_group_id,capture_status=excluded.capture_status,lifecycle_status=excluded.lifecycle_status,started_at=excluded.started_at,ended_at=excluded.ended_at,duration_ms=excluded.duration_ms,http_completed_at=excluded.http_completed_at,http_duration_ms=excluded.http_duration_ms,first_token_ms=excluded.first_token_ms,created_at=excluded.created_at WHERE ` + s.table("audit_logs") + `.lifecycle_status='in_progress' AND excluded.lifecycle_status='finalized'`
	result, err := tx.ExecContext(ctx, s.bind(query), input.ID, input.TraceID, string(input.TrafficSource), nullText(input.SystemAccountID), nullText(input.APIKeyID), nullText(input.ConversationKey), nullText(input.SessionID), nullText(input.SessionClientType), nullText(input.GroupID), nullText(input.AccountID), nullText(input.ProviderCode), input.Method, input.Path, nullText(input.QueryString), nullText(input.Model), nullText(input.UpstreamModel), nullText(input.PricingModel), boolValue(input.ModelMappingApplied), nullText(input.ModelMappingSource), nullText(input.SourceEndpointFamily), nullText(input.UpstreamEndpointFamily), boolValue(input.Stream), nullText(input.ClientIP), nullText(input.UserAgent), string(input.AuditOutcome), input.Success, input.FinalStatusCode, nullText(input.ErrorPhase), nullText(input.ErrorCode), nullText(input.ErrorMessage), input.SampleBucket, input.SampleReason, len(input.Attempts), len(input.Payloads), raw, compressed, maxInt64(0, raw-compressed), capture, string(lifecycle), dbTimeText(input.StartedAt), dbTimeText(input.EndedAt), input.DurationMS, nullableTime(s.mode, input.HTTPCompletedAt), input.HTTPDurationMS, input.FirstTokenMS, nullableTime(s.mode, createdAt))
	if err != nil {
		return false, fmt.Errorf("写入 audit_logs 失败: %w", err)
	}
	wrote, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 audit_logs 写入结果失败: %w", err)
	}
	return wrote == 1, nil
}

func (s *sqlStore) insertFinalChildren(ctx context.Context, tx *sql.Tx, input AuditLogInput, payloads []preparedPayload) error {
	attemptIDs := map[string]string{}
	for _, attempt := range input.Attempts {
		id := attempt.ID
		if id == "" {
			id = stableID(input.ID, "attempt", strconv.Itoa(attempt.AttemptIndex))
		}
		query := `INSERT INTO ` + s.table("audit_log_attempts") + ` (id,audit_log_id,attempt_index,account_id,account_owner_system_account_id,group_id,proxy_url,provider_code,attempt_model,attempt_upstream_model,attempt_pricing_model,attempt_model_mapping_applied,attempt_model_mapping_source,attempt_source_endpoint_family,attempt_upstream_endpoint_family,upstream_method,upstream_url,upstream_status_code,success,error_phase,error_code,error_message,started_at,ended_at,duration_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING RETURNING id`
		var insertedID string
		err := tx.QueryRowContext(ctx, s.bind(query), id, input.ID, attempt.AttemptIndex, nullText(attempt.AccountID), nullText(attempt.AccountOwnerSystemAccountID), nullText(attempt.GroupID), nullText(attempt.ProxyURL), nullText(attempt.ProviderCode), nullText(attempt.Model), nullText(attempt.UpstreamModel), nullText(attempt.PricingModel), boolValue(attempt.ModelMappingApplied), nullText(attempt.ModelMappingSource), nullText(attempt.SourceEndpointFamily), nullText(attempt.UpstreamEndpointFamily), attempt.UpstreamMethod, attempt.UpstreamURL, attempt.UpstreamStatusCode, boolValue(attempt.Success), nullText(attempt.ErrorPhase), nullText(attempt.ErrorCode), nullText(attempt.ErrorMessage), dbTimeText(attempt.StartedAt), nullableTime(s.mode, attempt.EndedAt), attempt.DurationMS).Scan(&insertedID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("audit_log_attempts")+` WHERE id=? AND audit_log_id=?`), id, input.ID).Scan(&insertedID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
		}
		if err != nil {
			return fmt.Errorf("写入 audit_log_attempts 失败: %w", err)
		}
		if attempt.TempID != "" {
			attemptIDs[attempt.TempID] = insertedID
		}
		attemptIDs[insertedID] = insertedID
	}
	for payloadOffset, payload := range payloads {
		if err := s.insertBlob(ctx, tx, payload.headers); err != nil {
			return err
		}
		if err := s.insertBlob(ctx, tx, payload.body); err != nil {
			return err
		}
		id := payload.input.ID
		if id == "" {
			id = stableID(input.ID, "payload", strconv.Itoa(payloadOffset), string(payload.input.PartType), strconv.Itoa(payload.sequenceIndex))
		}
		attemptID := attemptIDs[payload.attemptID]
		var attemptRef any
		if attemptID != "" {
			attemptRef = attemptID
		}
		status := payload.input.CaptureStatus
		if status == "" {
			status = PayloadCaptureComplete
		}
		var headerID, bodyID any
		var headerSHA, bodySHA any = nullText(payload.headersSHA), nullText(payload.bodySHA)
		if payload.headers != nil {
			headerID = payload.headers.id
		}
		if payload.body != nil {
			bodyID = payload.body.id
		}
		raw, compressed := payload.rawSize, payload.compressedSize
		query := `INSERT INTO ` + s.table("audit_payload_refs") + ` (id,audit_log_id,attempt_id,part_type,sequence_index,content_type,content_encoding,headers_blob_id,body_blob_id,headers_sha256,body_sha256,raw_size_bytes,compressed_size_bytes,capture_status,drop_reason,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`
		createdAt := payload.input.CreatedAt
		if strings.TrimSpace(createdAt) == "" {
			createdAt = input.CreatedAt
		}
		result, err := tx.ExecContext(ctx, s.bind(query), id, input.ID, attemptRef, string(payload.input.PartType), payload.sequenceIndex, nullText(payload.input.ContentType), nullText(payload.input.ContentEncoding), headerID, bodyID, headerSHA, bodySHA, raw, compressed, string(status), nullText(string(payload.input.DropReason)), nullableTime(s.mode, createdAt))
		if err != nil {
			return fmt.Errorf("写入 audit_payload_refs 失败: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 1 {
			if err := s.incrementRefs(ctx, tx, payload.headers, payload.body); err != nil {
				return err
			}
		}
	}
	if input.AuditOutcome != AuditOutcomeSuccess {
		if err := s.upsertErrorGroup(ctx, tx, input, payloads); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqlStore) insertBlob(ctx context.Context, tx *sql.Tx, blob *blobRecord) error {
	if blob == nil {
		return nil
	}
	now := dbTime(s.mode, time.Now().UTC())
	// resolveBlobIDs inserted or selected the canonical row before references
	// are written. This second idempotent touch keeps last_seen observable.
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("audit_payload_blobs")+` SET last_seen_at=? WHERE id=?`), now, blob.id); err != nil {
		return fmt.Errorf("更新 audit_payload_blobs 时间失败: %w", err)
	}
	return nil
}

func (s *sqlStore) incrementRefs(ctx context.Context, tx *sql.Tx, blobs ...*blobRecord) error {
	if !shouldMaintainBlobRefCount(s.mode) {
		// PostgreSQL treats audit_payload_refs as the source of truth. Updating a
		// mutable cache counter here races retention/readers and can drift under
		// concurrent writers; F3 PG therefore leaves ref_count untouched.
		return nil
	}
	for _, blob := range blobs {
		if blob == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("audit_payload_blobs")+` SET ref_count=ref_count+1,last_seen_at=? WHERE sha256=? AND raw_size_bytes=? AND content_type=?`), dbTime(s.mode, time.Now().UTC()), blob.sha256, blob.rawSize, blob.contentType); err != nil {
			return fmt.Errorf("更新 audit blob 引用计数失败: %w", err)
		}
	}
	return nil
}

func shouldMaintainBlobRefCount(mode Mode) bool { return mode == ModeSQLite }

func (s *sqlStore) upsertErrorGroup(ctx context.Context, tx *sql.Tx, input AuditLogInput, payloads []preparedPayload) error {
	group, err := derivedErrorGroup(input, payloads)
	if err != nil {
		return err
	}
	id := stableID("error", shortHash(group.fingerprint), shortHash(group.windowStartedAt))
	eventAt := input.CreatedAt
	eventTime := nullableTime(s.mode, eventAt)
	table := s.table("audit_error_groups")
	query := `INSERT INTO ` + table + ` (id,fingerprint,window_started_at,window_ended_at,system_account_id,api_key_id,group_id,account_id,provider_code,path,model,status_code,error_phase,error_code,error_type,request_fingerprint,error_fingerprint,count,first_event_id,last_event_id,sample_event_id,last_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?, ?,?,?) ON CONFLICT(fingerprint,window_started_at) DO UPDATE SET count=` + table + `.count+1,window_ended_at=CASE WHEN excluded.window_ended_at > ` + table + `.window_ended_at THEN excluded.window_ended_at ELSE ` + table + `.window_ended_at END,first_event_id=CASE WHEN excluded.created_at < ` + table + `.created_at OR (excluded.created_at = ` + table + `.created_at AND excluded.first_event_id < ` + table + `.first_event_id) THEN excluded.first_event_id ELSE ` + table + `.first_event_id END,last_event_id=CASE WHEN excluded.updated_at > ` + table + `.updated_at OR (excluded.updated_at = ` + table + `.updated_at AND excluded.last_event_id > ` + table + `.last_event_id) THEN excluded.last_event_id ELSE ` + table + `.last_event_id END,sample_event_id=COALESCE(` + table + `.sample_event_id, excluded.sample_event_id),last_message=CASE WHEN excluded.updated_at > ` + table + `.updated_at OR (excluded.updated_at = ` + table + `.updated_at AND excluded.last_event_id > ` + table + `.last_event_id) THEN excluded.last_message ELSE ` + table + `.last_message END,created_at=CASE WHEN excluded.created_at < ` + table + `.created_at THEN excluded.created_at ELSE ` + table + `.created_at END,updated_at=CASE WHEN excluded.updated_at > ` + table + `.updated_at THEN excluded.updated_at ELSE ` + table + `.updated_at END RETURNING id`
	var returned string
	if err := tx.QueryRowContext(ctx, s.bind(query), id, group.fingerprint, dbTimeText(group.windowStartedAt), dbTimeText(group.windowEndedAt), nullText(input.SystemAccountID), nullText(input.APIKeyID), nullText(input.GroupID), nullText(input.AccountID), nullText(input.ProviderCode), input.Path, nullText(input.Model), input.FinalStatusCode, nullText(input.ErrorPhase), nullText(input.ErrorCode), string(input.AuditOutcome), group.requestFingerprint, group.errorFingerprint, input.ID, input.ID, input.ID, nullText(input.ErrorMessage), eventTime, eventTime).Scan(&returned); err != nil {
		return fmt.Errorf("写入 audit_error_groups 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("audit_logs")+` SET error_group_id=? WHERE id=?`), returned, input.ID); err != nil {
		return fmt.Errorf("关联 audit_error_group 失败: %w", err)
	}
	return nil
}

const auditErrorGroupWindow = 5 * time.Minute

// derivedErrorGroup mirrors the current Node writer's error grouping identity:
// non-"success" outcomes are grouped into fixed five-minute UTC windows from
// stable request and normalized error fingerprints. Callers need not fabricate
// an ErrorGroup payload merely to preserve the existing audit contract.
type derivedAuditErrorGroup struct {
	fingerprint, windowStartedAt, windowEndedAt, requestFingerprint, errorFingerprint string
}

func derivedErrorGroup(input AuditLogInput, payloads []preparedPayload) (derivedAuditErrorGroup, error) {
	when, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(input.CreatedAt))
	if err != nil {
		return derivedAuditErrorGroup{}, fmt.Errorf("审计 error group createdAt 非法: %w", err)
	}
	windowStart := when.UTC().Truncate(auditErrorGroupWindow)
	windowEnd := windowStart.Add(auditErrorGroupWindow)
	requestBodySHA := ""
	for _, payload := range payloads {
		if payload.input.PartType != PayloadPartClientRequest {
			continue
		}
		if payload.input.BodySHA256 != "" {
			requestBodySHA = payload.input.BodySHA256
		} else if payload.body != nil {
			requestBodySHA = payload.body.sha256
		}
		break
	}
	requestFingerprint := hashNodeValue(map[string]any{"method": input.Method, "path": input.Path, "model": input.Model, "stream": input.Stream != nil && *input.Stream, "bodySha256": requestBodySHA})
	failed := AuditLogAttemptInput{}
	for _, attempt := range input.Attempts {
		if attempt.Success != nil && !*attempt.Success {
			failed = attempt
			break
		}
	}
	statusCode, errorPhase, errorCode, errorMessage := input.FinalStatusCode, input.ErrorPhase, input.ErrorCode, input.ErrorMessage
	if statusCode == nil {
		statusCode = failed.UpstreamStatusCode
	}
	if errorPhase == "" {
		errorPhase = failed.ErrorPhase
	}
	if errorCode == "" {
		errorCode = failed.ErrorCode
	}
	if errorMessage == "" {
		errorMessage = failed.ErrorMessage
	}
	errorFingerprint := hashNodeValue(map[string]any{"outcome": string(input.AuditOutcome), "statusCode": nodeStatusCode(statusCode), "phase": errorPhase, "code": errorCode, "message": normalizeErrorMessage(errorMessage)})
	fingerprint := hashNodeValue(map[string]any{"systemAccountId": input.SystemAccountID, "apiKeyId": input.APIKeyID, "groupId": input.GroupID, "accountId": input.AccountID, "providerCode": input.ProviderCode, "trafficSource": string(input.TrafficSource), "path": input.Path, "model": input.Model, "statusCode": nodeStatusCode(input.FinalStatusCode), "errorPhase": input.ErrorPhase, "errorCode": input.ErrorCode, "requestFingerprint": requestFingerprint, "errorFingerprint": errorFingerprint})
	return derivedAuditErrorGroup{fingerprint: fingerprint, windowStartedAt: nodeISOString(windowStart), windowEndedAt: nodeISOString(windowEnd), requestFingerprint: requestFingerprint, errorFingerprint: errorFingerprint}, nil
}

func hashNodeValue(value any) string {
	// encoding/json orders map keys. SetEscapeHTML(false) preserves JSON string
	// bytes such as '<' exactly as Node JSON.stringify does. These identity maps
	// contain scalar values only, matching Node stableJsonStringify.
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	sum := sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func nodeISOString(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func nodeStatusCode(value *int) any {
	if value == nil {
		return ""
	}
	return *value
}

// nodeUTF16String preserves the exact JavaScript String.slice code-unit
// result, including a possible trailing lone surrogate. Go strings cannot
// represent that state faithfully, so this type owns JSON serialization for
// error-group hashing.
type nodeUTF16String []uint16

func normalizeErrorMessage(value string) nodeUTF16String {
	units := utf16.Encode([]rune(value))
	if len(units) > 500 {
		units = units[:500]
	}
	units = replaceASCIIUnitRuns(units, isASCIIHex, 16, []uint16{'{', 'h', 'e', 'x', '}'})
	return replaceASCIIUnitRuns(units, isASCIIDigit, 3, []uint16{'{', 'n', 'u', 'm', '}'})
}

func replaceASCIIUnitRuns(units []uint16, matches func(uint16) bool, minimum int, replacement []uint16) []uint16 {
	result := make([]uint16, 0, len(units))
	for index := 0; index < len(units); {
		end := index
		for end < len(units) && matches(units[end]) {
			end++
		}
		if end-index >= minimum {
			result = append(result, replacement...)
		} else {
			result = append(result, units[index:end]...)
		}
		if end == index {
			result = append(result, units[index])
			end++
		}
		index = end
	}
	return result
}

func isASCIIHex(unit uint16) bool {
	return unit >= '0' && unit <= '9' || unit >= 'a' && unit <= 'f' || unit >= 'A' && unit <= 'F'
}
func isASCIIDigit(unit uint16) bool { return unit >= '0' && unit <= '9' }

func (value nodeUTF16String) MarshalJSON() ([]byte, error) {
	var builder strings.Builder
	builder.WriteByte('"')
	for index := 0; index < len(value); index++ {
		unit := value[index]
		switch unit {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if unit < 0x20 || utf16.IsSurrogate(rune(unit)) {
				if utf16.IsSurrogate(rune(unit)) && index+1 < len(value) && utf16.IsSurrogate(rune(value[index+1])) {
					character := utf16.DecodeRune(rune(unit), rune(value[index+1]))
					if character != unicodeReplacementRune {
						builder.WriteRune(character)
						index++
						continue
					}
				}
				builder.WriteString(fmt.Sprintf(`\u%04x`, unit))
			} else {
				builder.WriteRune(rune(unit))
			}
		}
	}
	builder.WriteByte('"')
	return []byte(builder.String()), nil
}

const unicodeReplacementRune = '\uFFFD'

func (s *sqlStore) bind(query string) string {
	if s.mode != ModePostgres {
		return query
	}
	var builder strings.Builder
	index := 0
	for _, character := range query {
		if character != '?' {
			builder.WriteRune(character)
			continue
		}
		index++
		builder.WriteByte('$')
		builder.WriteString(strconv.Itoa(index))
	}
	return builder.String()
}

func dbTime(mode Mode, value time.Time) any {
	if mode == ModePostgres {
		return value.UTC()
	}
	return value.UTC().Format(time.RFC3339Nano)
}
func dbTimeText(value string) any {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}
func nullableTime(mode Mode, value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	if mode == ModePostgres {
		return parsed.UTC()
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}
func nullText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func boolValue(value *bool) any {
	if value == nil {
		return false
	}
	return *value
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

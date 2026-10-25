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
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	auditBlobCompressionThresholdBytes    = 4 * 1024
	auditBlobCompressionMaxBytes          = 1024 * 1024
	postgresBlobLifecycleLockSeed         = 763847294
	postgresAuditLogLifecycleLockSeed     = 763847295
	postgresErrorGroupFingerprintLockSeed = 763847296
)

var ErrOwnerLeaseLost = errors.New("F3 audit owner lease 已失效或已移交")

type sqlStore struct {
	db          *sql.DB
	blobDir     string
	hotDir      string
	hotMu       sync.Mutex
	schemaMu    sync.Mutex
	schemaReady bool
	pool        *pgpool.Handle
}

type blobPlan struct {
	record   blobRecord
	rawBytes []byte
	bytes    []byte // bytes exactly as stored (possibly gzip-compressed)
	root     string
	tempPath string
	existing bool
}

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
//
// The acquire conflict branch takes over an expired row OR an unexpired row
// owned by the same owner_id. owner_id 是进程身份（DefaultInstanceID →
// hostname，同一容器跨整机重启不变），同一 owner 的再获取只可能来自本实例
// 自己的重启/失租恢复：fence_token 自增把旧世代写入挡在外面，语义与过期接管
// 完全一致。跨 owner 仍必须等过期（BUG-0228 发布窗口等待契约不变）。
const postgresAcquireLeaseSQL = `INSERT INTO juhe_dataset.audit_log_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES (?, ?, 1, clock_timestamp() + (? * INTERVAL '1 millisecond'), clock_timestamp())
ON CONFLICT (lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id, fence_token=juhe_dataset.audit_log_owner_leases.fence_token+1, lease_until=EXCLUDED.lease_until, updated_at=clock_timestamp()
WHERE juhe_dataset.audit_log_owner_leases.lease_until <= clock_timestamp() OR juhe_dataset.audit_log_owner_leases.owner_id = EXCLUDED.owner_id RETURNING fence_token`

const postgresRenewLeaseSQL = `UPDATE juhe_dataset.audit_log_owner_leases SET lease_until=clock_timestamp() + (? * INTERVAL '1 millisecond'),updated_at=clock_timestamp() WHERE lease_key=? AND owner_id=? AND fence_token=? AND lease_until > clock_timestamp()`

const postgresInitialFenceSQL = `SELECT 1 FROM juhe_dataset.audit_log_owner_leases WHERE lease_key=? AND owner_id=? AND fence_token=? AND lease_until > clock_timestamp()`

const postgresCommitFenceSQL = `UPDATE juhe_dataset.audit_log_owner_leases SET updated_at=updated_at WHERE lease_key=? AND owner_id=? AND fence_token=? AND lease_until > clock_timestamp() RETURNING fence_token`

func OpenStore(cfg Config) (Store, error) {
	// Opening a store (and the current foundation CLI) has no blob-directory
	// side effects. Blob files may only be created or cleaned by an acquired,
	// fenced owner during Persist/CleanupOwnedBlobTemps.
	maxOpen := cfg.PostgresMaxOpenConns
	if maxOpen == 0 {
		maxOpen = 1000
	}
	maxIdle := cfg.PostgresMaxIdleConns
	if maxIdle == 0 {
		maxIdle = 1000
	}
	var err error
	pool := cfg.PostgresPool
	if pool == nil {
		registry := pgpool.NewRegistry()
		pool, err = registry.Acquire(cfg.PostgresURL, "audit-log", maxOpen, maxIdle)
		if err != nil {
			return nil, fmt.Errorf("打开 F3 PostgreSQL 连接池失败: %w", err)
		}
	}
	hotDir := cfg.HotSearchDirectory
	if hotDir == "" {
		hotDir = filepath.Join(filepath.Dir(cfg.PayloadBlobDirectory), "search-hot")
	}
	return &sqlStore{db: pool.DB(), pool: pool, blobDir: cfg.PayloadBlobDirectory, hotDir: hotDir}, nil
}

func (s *sqlStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.pool != nil {
		return s.pool.Close()
	}
	return s.db.Close()
}

func (s *sqlStore) EnsureSchema(ctx context.Context) error {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}
	{
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
		for _, column := range superGroupAuditColumns {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+s.table(column.table)+" ADD COLUMN IF NOT EXISTS "+column.column+" "+column.definition); err != nil {
				return fmt.Errorf("补 F3 PostgreSQL 列 %s.%s 失败: %w", column.table, column.column, err)
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

func (s *sqlStore) table(name string) string {
	return "juhe_dataset." + name
}

func (s *sqlStore) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return OwnerLease{}, false, err
	}
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

func (s *sqlStore) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, s.bind(postgresRenewLeaseSQL), duration.Milliseconds(), "f3-audit-log-persistence", lease.OwnerID, lease.FenceToken)
	if err != nil {
		return false, fmt.Errorf("续约 F3 PostgreSQL owner lease 失败: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *sqlStore) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error {
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	// Keep the fence row and only expire it. Deleting it would reset the next
	// token to 1, allowing a delayed former owner to become indistinguishable
	// from a new owner after a voluntary handoff.
	query := `UPDATE ` + s.table("audit_log_owner_leases") + ` SET lease_until=to_timestamp(0),updated_at=clock_timestamp() WHERE lease_key=? AND owner_id=? AND fence_token=?`
	arguments := []any{"f3-audit-log-persistence", lease.OwnerID, lease.FenceToken}
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
	prepared, plans, err = s.stripErrorGroupWindowDuplicates(ctx, tx, input, prepared, plans)
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
	if auditLogID == "" {
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

// lockErrorGroupFingerprintTx serializes same-fingerprint persists in
// PostgreSQL so the per-window sample classification (strip) and the
// cross-window handover (dedupe) read a stable latest window row: without it,
// concurrent producer workers could both materialize payloads for one window
// and leave the duplicate's blobs stored until retention trims the row.
// It must be taken before any blob lifecycle
// lock — the fingerprint gate joins the audit-ID gate at the head of the
// persist lock order (retention takes neither, so no new lock cycles).
func (s *sqlStore) lockErrorGroupFingerprintTx(ctx context.Context, tx *sql.Tx, fingerprint string) error {
	if fingerprint == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, s.bind(`SELECT pg_advisory_xact_lock(hashtextextended(?, `+strconv.FormatInt(postgresErrorGroupFingerprintLockSeed, 10)+`::bigint))`), fingerprint); err != nil {
		return fmt.Errorf("获取 F3 错误组指纹串行锁失败: %w", err)
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
	if blobID == "" {
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
		now := time.Now().UTC()
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
	// 窗口内重复的降级部件在剥离时冻结尺寸：blob 指针清空后 SHA 与大小仍保留，
	// 与跨窗口降级后的 refs 语义一致；按空指针重算会错误归零。
	if payload.input.CaptureStatus == PayloadCaptureDeduped {
		return
	}
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
	query := `INSERT INTO ` + s.table("audit_logs") + ` (id,trace_id,traffic_source,system_account_id,api_key_id,conversation_key,session_id,session_client_type,group_id,route_group_id,account_id,provider_code,method,path,query_string,model,upstream_model,pricing_model,model_mapping_applied,model_mapping_source,source_endpoint_family,upstream_endpoint_family,stream,client_ip,user_agent,audit_outcome,success,final_status_code,error_phase,error_code,error_message,sample_bucket,sample_reason,attempt_count,payload_count,raw_payload_bytes,compressed_payload_bytes,compression_saved_bytes,capture_status,lifecycle_status,started_at,ended_at,duration_ms,http_completed_at,http_duration_ms,first_token_ms,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET trace_id=excluded.trace_id,traffic_source=excluded.traffic_source,system_account_id=excluded.system_account_id,api_key_id=excluded.api_key_id,conversation_key=excluded.conversation_key,session_id=excluded.session_id,session_client_type=excluded.session_client_type,group_id=excluded.group_id,route_group_id=excluded.route_group_id,account_id=excluded.account_id,provider_code=excluded.provider_code,method=excluded.method,path=excluded.path,query_string=excluded.query_string,model=excluded.model,upstream_model=excluded.upstream_model,pricing_model=excluded.pricing_model,model_mapping_applied=excluded.model_mapping_applied,model_mapping_source=excluded.model_mapping_source,source_endpoint_family=excluded.source_endpoint_family,upstream_endpoint_family=excluded.upstream_endpoint_family,stream=excluded.stream,client_ip=excluded.client_ip,user_agent=excluded.user_agent,audit_outcome=excluded.audit_outcome,success=excluded.success,final_status_code=excluded.final_status_code,error_phase=excluded.error_phase,error_code=excluded.error_code,error_message=excluded.error_message,sample_bucket=excluded.sample_bucket,sample_reason=excluded.sample_reason,attempt_count=excluded.attempt_count,payload_count=excluded.payload_count,raw_payload_bytes=excluded.raw_payload_bytes,compressed_payload_bytes=excluded.compressed_payload_bytes,compression_saved_bytes=excluded.compression_saved_bytes,error_group_id=excluded.error_group_id,capture_status=excluded.capture_status,lifecycle_status=excluded.lifecycle_status,started_at=excluded.started_at,ended_at=excluded.ended_at,duration_ms=excluded.duration_ms,http_completed_at=excluded.http_completed_at,http_duration_ms=excluded.http_duration_ms,first_token_ms=excluded.first_token_ms,created_at=excluded.created_at WHERE ` + s.table("audit_logs") + `.lifecycle_status='in_progress' AND excluded.lifecycle_status='finalized'`
	result, err := tx.ExecContext(ctx, s.bind(query), input.ID, input.TraceID, string(input.TrafficSource), nullText(input.SystemAccountID), nullText(input.APIKeyID), nullText(input.ConversationKey), nullText(input.SessionID), nullText(input.SessionClientType), nullText(input.GroupID), nullText(input.RouteGroupID), nullText(input.AccountID), nullText(input.ProviderCode), input.Method, input.Path, nullText(input.QueryString), nullText(input.Model), nullText(input.UpstreamModel), nullText(input.PricingModel), boolValue(input.ModelMappingApplied), nullText(input.ModelMappingSource), nullText(input.SourceEndpointFamily), nullText(input.UpstreamEndpointFamily), boolValue(input.Stream), nullText(input.ClientIP), nullText(input.UserAgent), string(input.AuditOutcome), input.Success, input.FinalStatusCode, nullText(input.ErrorPhase), nullText(input.ErrorCode), nullText(input.ErrorMessage), input.SampleBucket, input.SampleReason, len(input.Attempts), len(input.Payloads), raw, compressed, maxInt64(0, raw-compressed), capture, string(lifecycle), dbTimeText(input.StartedAt), dbTimeText(input.EndedAt), input.DurationMS, nullableTime(input.HTTPCompletedAt), input.HTTPDurationMS, input.FirstTokenMS, nullableTime(createdAt))
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
		query := `INSERT INTO ` + s.table("audit_log_attempts") + ` (id,audit_log_id,attempt_index,account_id,account_owner_system_account_id,group_id,member_group_id,proxy_url,provider_code,attempt_model,attempt_upstream_model,attempt_pricing_model,attempt_model_mapping_applied,attempt_model_mapping_source,attempt_source_endpoint_family,attempt_upstream_endpoint_family,upstream_method,upstream_url,upstream_status_code,success,error_phase,error_code,error_message,started_at,ended_at,duration_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING RETURNING id`
		var insertedID string
		err := tx.QueryRowContext(ctx, s.bind(query), id, input.ID, attempt.AttemptIndex, nullText(attempt.AccountID), nullText(attempt.AccountOwnerSystemAccountID), nullText(attempt.GroupID), nullText(attempt.MemberGroupID), nullText(attempt.ProxyURL), nullText(attempt.ProviderCode), nullText(attempt.Model), nullText(attempt.UpstreamModel), nullText(attempt.PricingModel), boolValue(attempt.ModelMappingApplied), nullText(attempt.ModelMappingSource), nullText(attempt.SourceEndpointFamily), nullText(attempt.UpstreamEndpointFamily), attempt.UpstreamMethod, attempt.UpstreamURL, attempt.UpstreamStatusCode, boolValue(attempt.Success), nullText(attempt.ErrorPhase), nullText(attempt.ErrorCode), nullText(attempt.ErrorMessage), dbTimeText(attempt.StartedAt), nullableTime(attempt.EndedAt), attempt.DurationMS).Scan(&insertedID)
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
		result, err := tx.ExecContext(ctx, s.bind(query), id, input.ID, attemptRef, string(payload.input.PartType), payload.sequenceIndex, nullText(payload.input.ContentType), nullText(payload.input.ContentEncoding), headerID, bodyID, headerSHA, bodySHA, raw, compressed, string(status), nullText(string(payload.input.DropReason)), nullableTime(createdAt))
		if err != nil {
			return fmt.Errorf("写入 audit_payload_refs 失败: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		// PostgreSQL treats audit_payload_refs as the source of truth; the
		// audit_payload_blobs.ref_count cache column is left untouched (a
		// mutable counter here would race retention/readers and drift under
		// concurrent writers).
		_ = inserted
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
	now := time.Now().UTC()
	// resolveBlobIDs inserted or selected the canonical row before references
	// are written. This second idempotent touch keeps last_seen observable.
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("audit_payload_blobs")+` SET last_seen_at=? WHERE id=?`), now, blob.id); err != nil {
		return fmt.Errorf("更新 audit_payload_blobs 时间失败: %w", err)
	}
	return nil
}

func (s *sqlStore) upsertErrorGroup(ctx context.Context, tx *sql.Tx, input AuditLogInput, payloads []preparedPayload) error {
	group, err := derivedErrorGroup(input, payloads)
	if err != nil {
		return err
	}
	id := stableID("error", shortHash(group.fingerprint), shortHash(group.windowStartedAt))
	eventAt := input.CreatedAt
	eventTime := nullableTime(eventAt)
	table := s.table("audit_error_groups")
	// 错误组存储去重：fingerprint 不含窗口，样本只在跨窗口时移交；窗口内重复
	// 事件的响应部件已在 stripErrorGroupWindowDuplicates 出生即降级（不落盘、
	// 零 GC 往返），窗口首条保持样本身份。必须在 upsert 之前读取最近窗口组的
	// 保留样本（含当前窗口行更新前的值）；幂等重放（旧样本即本事件）直接跳过，
	// 跨窗口时在同一事务内降级旧样本并登记 GC。
	if err := s.dedupePreviousErrorGroupSample(ctx, tx, group.fingerprint, group.windowStartedAt, input.ID); err != nil {
		return err
	}
	query := `INSERT INTO ` + table + ` (id,fingerprint,window_started_at,window_ended_at,system_account_id,api_key_id,group_id,account_id,provider_code,path,model,status_code,error_phase,error_code,error_type,request_fingerprint,error_fingerprint,count,first_event_id,last_event_id,sample_event_id,last_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?, ?,?,?) ON CONFLICT(fingerprint,window_started_at) DO UPDATE SET count=` + table + `.count+1,window_ended_at=CASE WHEN excluded.window_ended_at > ` + table + `.window_ended_at THEN excluded.window_ended_at ELSE ` + table + `.window_ended_at END,first_event_id=CASE WHEN excluded.created_at < ` + table + `.created_at OR (excluded.created_at = ` + table + `.created_at AND excluded.first_event_id < ` + table + `.first_event_id) THEN excluded.first_event_id ELSE ` + table + `.first_event_id END,last_event_id=CASE WHEN excluded.updated_at > ` + table + `.updated_at OR (excluded.updated_at = ` + table + `.updated_at AND excluded.last_event_id > ` + table + `.last_event_id) THEN excluded.last_event_id ELSE ` + table + `.last_event_id END,last_message=CASE WHEN excluded.updated_at > ` + table + `.updated_at OR (excluded.updated_at = ` + table + `.updated_at AND excluded.last_event_id > ` + table + `.last_event_id) THEN excluded.last_message ELSE ` + table + `.last_message END,created_at=CASE WHEN excluded.created_at < ` + table + `.created_at THEN excluded.created_at ELSE ` + table + `.created_at END,updated_at=CASE WHEN excluded.updated_at > ` + table + `.updated_at THEN excluded.updated_at ELSE ` + table + `.updated_at END RETURNING id`
	var returned string
	if err := tx.QueryRowContext(ctx, s.bind(query), id, group.fingerprint, dbTimeText(group.windowStartedAt), dbTimeText(group.windowEndedAt), nullText(input.SystemAccountID), nullText(input.APIKeyID), nullText(input.GroupID), nullText(input.AccountID), nullText(input.ProviderCode), input.Path, nullText(input.Model), input.FinalStatusCode, nullText(input.ErrorPhase), nullText(input.ErrorCode), string(input.AuditOutcome), group.requestFingerprint, group.errorFingerprint, input.ID, input.ID, input.ID, nullText(input.ErrorMessage), eventTime, eventTime).Scan(&returned); err != nil {
		return fmt.Errorf("写入 audit_error_groups 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("audit_logs")+` SET error_group_id=? WHERE id=?`), returned, input.ID); err != nil {
		return fmt.Errorf("关联 audit_error_group 失败: %w", err)
	}
	return nil
}

// latestErrorGroupSample returns the most recent window row's group id, window
// and retained sample for one fingerprint, across any window and including the
// current one: read before the upsert above, the row still carries its
// pre-upsert sample, which covers both the cross-window and the same-window
// predecessor in one query.
func (s *sqlStore) latestErrorGroupSample(ctx context.Context, tx *sql.Tx, fingerprint string) (string, string, string, error) {
	query := `SELECT id,window_started_at,sample_event_id FROM ` + s.table("audit_error_groups") + ` WHERE fingerprint=? ORDER BY window_started_at DESC,id DESC LIMIT 1`
	var groupID, windowText string
	var sampleEventID sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(query), fingerprint).Scan(&groupID, &windowText, &sampleEventID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", nil
	}
	if err != nil {
		return "", "", "", fmt.Errorf("查询错误组当前保留样本失败: %w", err)
	}
	// 列值经 dbTimeText（RFC3339Nano）写出，整秒的 .000 毫秒被省略；统一回
	// nodeISOString 形态，与内存中的 group.windowStartedAt 同形比较，窗口
	// 不可解析时返回空串（按未知窗口处理）。
	windowStartedAt := ""
	if parsed, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(windowText)); parseErr == nil {
		windowStartedAt = nodeISOString(parsed)
	}
	return groupID, windowStartedAt, sampleEventID.String, nil
}

// dedupePreviousErrorGroupSample implements error-group storage dedup: the
// fingerprint spans windows, so a persistent failure hands its retained sample
// over only when a new window opens — the previous window's sample keeps the
// payloads until then. The handover downgrades the previous sample in the same
// transaction (blob ids NULLed, capture_status='deduped', drop_reason
// 'error_group_deduped', SHA256 values kept), released blobs receive a durable
// GC intent through the retention
// scheduling path, and the previous window row follows the sample pointer to
// the new event. Same-window duplicates never reach the downgrade: their own
// response parts were born deduped in stripErrorGroupWindowDuplicates, and the
// window's first event stays the sample. Success events never enter this path.
// dedupeDowngradedParts 是错误组去重的降级范围：组内内容随时间漂移、跨记录
// 复用会串样本的响应/错误类部件。请求侧（client_request/upstream_request）与
// gateway_metadata 不降级——fingerprint 含 client_request 的 bodySha256，同组
// 请求体必然一致并共享 canonical blob，保留引用零额外存储，且历史记录的请求
// 原文打开即可见（BUG-0320 用户裁决：去重对前端读侧无感）。
var dedupeDowngradedParts = [3]PayloadPartType{PayloadPartUpstreamResponse, PayloadPartGatewayResponse, PayloadPartGatewayError}

func (s *sqlStore) dedupePreviousErrorGroupSample(ctx context.Context, tx *sql.Tx, fingerprint, currentWindow, currentEventID string) error {
	previousGroupID, previousWindow, previousSampleID, err := s.latestErrorGroupSample(ctx, tx, fingerprint)
	if err != nil {
		return err
	}
	if previousSampleID == "" || previousSampleID == currentEventID {
		return nil
	}
	if previousWindow == currentWindow {
		// 同窗口重复：样本不跨窗口不移交，窗口首条保持样本身份，无降级写。
		return nil
	}
	// 跨事件变更遵循既有 per-audit-ID 生命周期门契约：先取旧样本事件的门，
	// 再触碰它的引用行或 blob 生命周期锁。Persist 自身 blob 通知锁在此前已
	// 持有，与 retention（audit 门 → blob 锁）在该窄窗口构成局部逆序，本序
	// 只缩小而未消除互锁窗口（旧样本与新事件共享 canonical blob 的并发删除
	// 场景，PG 死锁检测兜底，见 BUG-0321）。
	if err := s.lockAuditLogLifecycleTx(ctx, tx, previousSampleID); err != nil {
		return err
	}
	// UPDATE ... RETURNING 只返回置空后的新值，旧 blob id 必须先读出来。
	// 降级仅覆盖 dedupeDowngradedParts；请求侧引用原样保留。
	query := `SELECT headers_blob_id,body_blob_id FROM ` + s.table("audit_payload_refs") + ` WHERE audit_log_id=? AND part_type IN (?,?,?) AND (headers_blob_id IS NOT NULL OR body_blob_id IS NOT NULL)`
	rows, err := tx.QueryContext(ctx, s.bind(query), previousSampleID, string(dedupeDowngradedParts[0]), string(dedupeDowngradedParts[1]), string(dedupeDowngradedParts[2]))
	if err != nil {
		return fmt.Errorf("查询错误组旧样本 payload 引用失败: %w", err)
	}
	releasedBlobIDs := make([]string, 0)
	for rows.Next() {
		var headers, body sql.NullString
		if err := rows.Scan(&headers, &body); err != nil {
			rows.Close()
			return err
		}
		if headers.Valid {
			releasedBlobIDs = append(releasedBlobIDs, headers.String)
		}
		if body.Valid {
			releasedBlobIDs = append(releasedBlobIDs, body.String)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历错误组旧样本 payload 引用失败: %w", err)
	}
	downgrade := `UPDATE ` + s.table("audit_payload_refs") + ` SET headers_blob_id=NULL,body_blob_id=NULL,capture_status=?,drop_reason=? WHERE audit_log_id=? AND part_type IN (?,?,?) AND (headers_blob_id IS NOT NULL OR body_blob_id IS NOT NULL)`
	if _, err := tx.ExecContext(ctx, s.bind(downgrade), string(PayloadCaptureDeduped), string(PayloadDropErrorGroupDeduped), previousSampleID, string(dedupeDowngradedParts[0]), string(dedupeDowngradedParts[1]), string(dedupeDowngradedParts[2])); err != nil {
		return fmt.Errorf("降级错误组历史样本失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("audit_error_groups")+` SET sample_event_id=? WHERE id=?`), currentEventID, previousGroupID); err != nil {
		return fmt.Errorf("移交错误组样本事件失败: %w", err)
	}
	releasedBlobIDs = uniqueStrings(releasedBlobIDs)
	if len(releasedBlobIDs) == 0 {
		return nil
	}
	blobRows, err := s.blobRowsByID(ctx, tx, releasedBlobIDs)
	if err != nil {
		return err
	}
	// Reuse the retention GC scheduling path verbatim: per-blob advisory lock
	// first (PostgreSQL), then the NOT EXISTS recheck — a blob still shared
	// with the new event is never scheduled — and the durable
	// audit_payload_blob_gc intent for the owner retention tick.
	return s.scheduleUnreferencedBlobGC(ctx, tx, blobRows)
}

const auditErrorGroupWindow = 10 * time.Minute

// stripErrorGroupWindowDuplicates is the write-side half of per-window sample
// handover: when the fingerprint's most recent window row already covers this
// event's window, the event is a within-window duplicate — its response/error
// parts are born deduped (SHA256 and sizes frozen, blob ids never materialize,
// no blob files, lifecycle locks or GC churn) and the window's first event
// stays the sample without any downgrade write. Cross-window and first-ever
// events return unchanged; their handover runs in upsertErrorGroup. Must run
// between planPayloads and lockBlobLifecyclePlans so stripped blobs never take
// locks or files.
func (s *sqlStore) stripErrorGroupWindowDuplicates(ctx context.Context, tx *sql.Tx, input AuditLogInput, prepared []preparedPayload, plans []blobPlan) ([]preparedPayload, []blobPlan, error) {
	if input.AuditOutcome == AuditOutcomeSuccess || normalizeLifecycle(input.LifecycleStatus) != LifecycleFinalized {
		return prepared, plans, nil
	}
	group, err := derivedErrorGroup(input, prepared)
	if err != nil {
		return nil, nil, err
	}
	// 同指纹并发写先在此串行化（PG advisory lock）：strip 分类与
	// 同事务稍后的 dedupe 移交读到稳定的最近窗口行，"窗口内仅首条保留"按提交
	// 顺序确定生效。
	if err := s.lockErrorGroupFingerprintTx(ctx, tx, group.fingerprint); err != nil {
		return nil, nil, err
	}
	_, latestWindow, latestSample, err := s.latestErrorGroupSample(ctx, tx, group.fingerprint)
	if err != nil {
		return nil, nil, err
	}
	if latestSample == "" || latestWindow != group.windowStartedAt {
		return prepared, plans, nil
	}
	// 未剥离部件仍引用的 blob 键从剥离集合扣除：同事件内响应/错误部件与请求
	// 侧部件内容共键（同 sha+contentType）时，误删保留侧计划会让其引用指向
	// 一个从不解析的 canonical 行（不存在时 FK 违例，整条 Persist 失败）。
	retained := make(map[string]struct{})
	for index := range prepared {
		part := prepared[index].input.PartType
		if part == dedupeDowngradedParts[0] || part == dedupeDowngradedParts[1] || part == dedupeDowngradedParts[2] {
			continue
		}
		for _, blob := range []*blobRecord{prepared[index].headers, prepared[index].body} {
			if blob != nil {
				retained[blob.sha256+"\x00"+blob.contentType] = struct{}{}
			}
		}
	}
	stripped := make(map[string]struct{})
	for index := range prepared {
		part := prepared[index].input.PartType
		if part != dedupeDowngradedParts[0] && part != dedupeDowngradedParts[1] && part != dedupeDowngradedParts[2] {
			continue
		}
		// 仅剥离真实持有 blob 引用的部件：hash_only/dropped 等本无正文的部件
		// 保持原捕获语义，不被改标 deduped。
		if prepared[index].headers == nil && prepared[index].body == nil {
			continue
		}
		prepared[index].input.CaptureStatus = PayloadCaptureDeduped
		prepared[index].input.DropReason = PayloadDropErrorGroupDeduped
		for _, blob := range []*blobRecord{prepared[index].headers, prepared[index].body} {
			if blob == nil {
				continue
			}
			key := blob.sha256 + "\x00" + blob.contentType
			if _, shared := retained[key]; !shared {
				stripped[key] = struct{}{}
			}
		}
		prepared[index].headers = nil
		prepared[index].body = nil
	}
	if len(stripped) == 0 {
		return prepared, plans, nil
	}
	kept := plans[:0]
	for _, plan := range plans {
		if _, ok := stripped[plan.record.sha256+"\x00"+plan.record.contentType]; ok {
			continue
		}
		kept = append(kept, plan)
	}
	return prepared, kept, nil
}

// derivedErrorGroup keeps the Node writer's grouping identity — stable request
// and normalized error fingerprints. The window length is owned by
// auditErrorGroupWindow（存储去重样本移交周期，2026-10-16 用户裁决自 5 分钟放宽
// 为 10 分钟：窗口内连续相同错误视为一致）. Callers need not fabricate an
// ErrorGroup payload merely to preserve the existing audit contract.
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

func dbTimeText(value string) any {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}
func nullableTime(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return parsed.UTC()
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

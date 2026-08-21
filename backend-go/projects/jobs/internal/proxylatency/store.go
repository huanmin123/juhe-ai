package proxylatency

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type StoreMode string

const (
	StoreSQLite   StoreMode = "sqlite"
	StorePostgres StoreMode = "postgres"
)

var (
	ErrOwnerLeaseLost  = errors.New("proxy-latency owner lease 已丢失")
	ErrProxyLeaseLost  = errors.New("proxy-latency proxy lease 已丢失")
	ErrProxyLeaseHeld  = errors.New("proxy-latency proxy lease 被其他活动 owner 持有")
	ErrRequestConflict = errors.New("proxy-latency request outcome 身份冲突")
	ErrRequestInFlight = errors.New("proxy-latency request 正在执行")
	ErrInputFence      = errors.New("proxy-latency issued input fence 不匹配或已过期")
)

// executionAdmission is the only executor hand-off. Input is decoded from
// the durable Store payload, not borrowed from the caller's slices/pointers.
type executionAdmission struct {
	Input      IssuedInput
	ClaimToken string
	Outcome    *Outcome
}

type StoreConfig struct {
	Mode                 StoreMode
	DatabasePath         string
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         *pgpool.Handle
}

type OwnerLease struct {
	OwnerID    string
	FenceToken int64
}

type ProxyLease struct {
	ProxyID    string
	OwnerID    string
	FenceToken int64
	// LeaseUntil is the persisted expiry returned at admission. Callers must
	// use the remaining wall-clock window, not the configured duration again.
	LeaseUntil time.Time
}

// Store owns only J3a jobs facts. It never opens a Node business SQLite file
// and PostgreSQL writes are constrained to the externally provisioned
// juhe_jobs schema.
type Store struct {
	db      *sql.DB
	mode    StoreMode
	writeMu sync.Mutex
	pool    *pgpool.Handle
}

func OpenStore(config StoreConfig) (*Store, error) {
	switch config.Mode {
	case StoreSQLite:
		path := strings.TrimSpace(config.DatabasePath)
		if path == "" {
			return nil, errors.New("proxy-latency sqlite 缺少数据库路径")
		}
		dsn, err := proxyLatencySQLiteDSN(path)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if _, err := db.Exec("PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("配置 proxy-latency sqlite 单 writer 失败: %w", err)
		}
		return &Store{db: db, mode: StoreSQLite}, nil
	case StorePostgres:
		if strings.TrimSpace(config.PostgresURL) == "" {
			return nil, errors.New("proxy-latency postgres 缺少连接 URL")
		}
		maxOpen := config.PostgresMaxOpenConns
		if maxOpen == 0 {
			maxOpen = 1000
		}
		maxIdle := config.PostgresMaxIdleConns
		if maxIdle == 0 {
			maxIdle = 1000
		}
		if maxOpen < 1 || maxIdle < 1 || maxIdle > maxOpen {
			return nil, fmt.Errorf("proxy-latency postgres max open/idle 必须满足 1 <= idle <= open，实际为 %d/%d", maxOpen, maxIdle)
		}
		var err error
		pool := config.PostgresPool
		if pool == nil {
			registry := pgpool.NewRegistry()
			pool, err = registry.Acquire("pgx", config.PostgresURL, "proxy-latency-store", maxOpen, maxIdle)
			if err != nil {
				return nil, err
			}
		}
		return &Store{db: pool.DB(), mode: StorePostgres, pool: pool}, nil
	default:
		return nil, errors.New("proxy-latency store mode 必须为 sqlite 或 postgres")
	}
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.pool != nil {
		return s.pool.Close()
	}
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("proxy-latency store 未初始化")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.mode == StorePostgres {
		tx, err := s.beginTx(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var owner, currentUser string
		err = tx.QueryRowContext(ctx, `SELECT pg_get_userbyid(nspowner), current_user FROM pg_namespace WHERE nspname='juhe_jobs'`).Scan(&owner, &currentUser)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("缺少外部 bootstrap 创建的 juhe_jobs schema")
		}
		if err != nil {
			return fmt.Errorf("读取 proxy-latency postgres schema owner 失败: %w", err)
		}
		if owner != currentUser {
			return fmt.Errorf("juhe_jobs schema owner 必须是当前 jobs role: owner=%s current=%s", owner, currentUser)
		}
		for _, statement := range strings.Split(postgresSchema, ";") {
			if statement = strings.TrimSpace(statement); statement != "" {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return fmt.Errorf("初始化 proxy-latency postgres schema 失败: %w", err)
				}
			}
		}
		return tx.Commit()
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("获取 proxy-latency sqlite schema 锁失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if _, err := conn.ExecContext(ctx, sqliteSchema); err != nil {
		return fmt.Errorf("初始化 proxy-latency sqlite schema 失败: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Store) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	if strings.TrimSpace(ownerID) == "" || duration <= 0 {
		return OwnerLease{}, false, errors.New("owner lease 参数无效")
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return OwnerLease{}, false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	var token int64
	var err error
	if s.mode == StorePostgres {
		err = s.db.QueryRowContext(ctx, `INSERT INTO juhe_jobs.proxy_latency_owner_leases(lease_key,owner_id,fence_token,lease_until,updated_at)
VALUES('proxy-latency-owner',$1,1,$2,$3)
ON CONFLICT(lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id,fence_token=juhe_jobs.proxy_latency_owner_leases.fence_token+1,lease_until=EXCLUDED.lease_until,updated_at=EXCLUDED.updated_at
WHERE juhe_jobs.proxy_latency_owner_leases.lease_until<=$3
RETURNING fence_token`, ownerID, now.Add(duration), now).Scan(&token)
	} else {
		nowText := now.Format(time.RFC3339Nano)
		err = s.db.QueryRowContext(ctx, `INSERT INTO proxy_latency_owner_leases(lease_key,owner_id,fence_token,lease_until,updated_at)
VALUES('proxy-latency-owner',?,1,?,?)
ON CONFLICT(lease_key) DO UPDATE SET owner_id=excluded.owner_id,fence_token=proxy_latency_owner_leases.fence_token+1,lease_until=excluded.lease_until,updated_at=excluded.updated_at
WHERE proxy_latency_owner_leases.lease_until<=?
RETURNING fence_token`, ownerID, now.Add(duration).Format(time.RFC3339Nano), nowText, nowText).Scan(&token)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, err
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func (s *Store) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error {
	if strings.TrimSpace(lease.OwnerID) == "" || lease.FenceToken < 1 {
		return errors.New("owner lease 释放参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.mode == StorePostgres {
		result, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.proxy_latency_owner_leases SET lease_until=statement_timestamp(),updated_at=statement_timestamp() WHERE lease_key='proxy-latency-owner' AND owner_id=$1 AND fence_token=$2 AND lease_until>statement_timestamp()`, lease.OwnerID, lease.FenceToken)
		return releasedLeaseResult(result, err, ErrOwnerLeaseLost)
	}
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE proxy_latency_owner_leases SET lease_until=?,updated_at=? WHERE lease_key='proxy-latency-owner' AND owner_id=? AND fence_token=? AND lease_until>?`, nowText, nowText, lease.OwnerID, lease.FenceToken, nowText)
	return releasedLeaseResult(result, err, ErrOwnerLeaseLost)
}

// RenewOwnerLease extends an existing owner fence without changing its token.
// A lost or expired lease is returned as ErrOwnerLeaseLost so callers stop work.
func (s *Store) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) error {
	if strings.TrimSpace(lease.OwnerID) == "" || lease.FenceToken < 1 || duration <= 0 {
		return errors.New("owner lease 续租参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	var result sql.Result
	var err error
	if s.mode == StorePostgres {
		result, err = s.db.ExecContext(ctx, `UPDATE juhe_jobs.proxy_latency_owner_leases SET lease_until=statement_timestamp()+($3 * INTERVAL '1 microsecond'),updated_at=statement_timestamp() WHERE lease_key='proxy-latency-owner' AND owner_id=$1 AND fence_token=$2 AND lease_until>statement_timestamp()`, lease.OwnerID, lease.FenceToken, duration.Microseconds())
	} else {
		nowText := now.Format(time.RFC3339Nano)
		result, err = s.db.ExecContext(ctx, `UPDATE proxy_latency_owner_leases SET lease_until=?,updated_at=? WHERE lease_key='proxy-latency-owner' AND owner_id=? AND fence_token=? AND lease_until>?`, now.Add(duration).Format(time.RFC3339Nano), nowText, lease.OwnerID, lease.FenceToken, nowText)
	}
	return releasedLeaseResult(result, err, ErrOwnerLeaseLost)
}

func (s *Store) VerifyOwnerLease(ctx context.Context, lease OwnerLease) error {
	if !validOwnerLease(lease) {
		return ErrOwnerLeaseLost
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.verifyOwnerTx(ctx, tx, lease, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcquireProxyLease(ctx context.Context, owner OwnerLease, proxyID string, duration time.Duration) (ProxyLease, bool, error) {
	if strings.TrimSpace(proxyID) == "" || duration <= 0 || !validOwnerLease(owner) {
		return ProxyLease{}, false, errors.New("proxy lease 参数无效")
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return ProxyLease{}, false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return ProxyLease{}, false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if err := s.verifyOwnerTx(ctx, tx, owner, now); err != nil {
		return ProxyLease{}, false, err
	}
	var token int64
	if s.mode == StorePostgres {
		err = tx.QueryRowContext(ctx, `INSERT INTO juhe_jobs.proxy_latency_proxy_leases(proxy_id,owner_id,fence_token,lease_until,updated_at)
VALUES($1,$2,1,$3,$4)
ON CONFLICT(proxy_id) DO UPDATE SET owner_id=EXCLUDED.owner_id,fence_token=juhe_jobs.proxy_latency_proxy_leases.fence_token+1,lease_until=EXCLUDED.lease_until,updated_at=EXCLUDED.updated_at
WHERE juhe_jobs.proxy_latency_proxy_leases.lease_until<=$4 OR NOT EXISTS(
 SELECT 1 FROM juhe_jobs.proxy_latency_owner_leases owner_lease
 WHERE owner_lease.lease_key='proxy-latency-owner' AND owner_lease.owner_id=juhe_jobs.proxy_latency_proxy_leases.owner_id AND owner_lease.lease_until>$4
)
RETURNING fence_token`, proxyID, owner.OwnerID, now.Add(duration), now).Scan(&token)
	} else {
		text := now.Format(time.RFC3339Nano)
		err = tx.QueryRowContext(ctx, `INSERT INTO proxy_latency_proxy_leases(proxy_id,owner_id,fence_token,lease_until,updated_at)
VALUES(?,?,1,?,?)
ON CONFLICT(proxy_id) DO UPDATE SET owner_id=excluded.owner_id,fence_token=proxy_latency_proxy_leases.fence_token+1,lease_until=excluded.lease_until,updated_at=excluded.updated_at
WHERE proxy_latency_proxy_leases.lease_until<=? OR NOT EXISTS(
 SELECT 1 FROM proxy_latency_owner_leases owner_lease
 WHERE owner_lease.lease_key='proxy-latency-owner' AND owner_lease.owner_id=proxy_latency_proxy_leases.owner_id AND owner_lease.lease_until>?
)
RETURNING fence_token`, proxyID, owner.OwnerID, now.Add(duration).Format(time.RFC3339Nano), text, text, text).Scan(&token)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return ProxyLease{}, false, commitErr
		}
		if s.activeProxyLeaseBelongsTo(ctx, proxyID, owner.OwnerID, now) {
			return ProxyLease{}, false, nil
		}
		return ProxyLease{}, false, ErrProxyLeaseHeld
	}
	if err != nil {
		return ProxyLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ProxyLease{}, false, err
	}
	return ProxyLease{ProxyID: proxyID, OwnerID: owner.OwnerID, FenceToken: token, LeaseUntil: now.Add(duration)}, true, nil
}

func (s *Store) activeProxyLeaseBelongsTo(ctx context.Context, proxyID, ownerID string, now time.Time) bool {
	var foundOwner string
	var until any
	query := `SELECT owner_id,lease_until FROM proxy_latency_proxy_leases WHERE proxy_id=?`
	args := []any{proxyID}
	if s.mode == StorePostgres {
		query = `SELECT owner_id,lease_until FROM juhe_jobs.proxy_latency_proxy_leases WHERE proxy_id=$1`
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&foundOwner, &until); err != nil {
		return false
	}
	leaseUntil, err := sqlTime(until)
	return err == nil && foundOwner == ownerID && leaseUntil.After(now)
}

func (s *Store) ReleaseProxyLease(ctx context.Context, lease ProxyLease) error {
	if strings.TrimSpace(lease.ProxyID) == "" || strings.TrimSpace(lease.OwnerID) == "" || lease.FenceToken < 1 {
		return errors.New("proxy lease 释放参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.mode == StorePostgres {
		result, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.proxy_latency_proxy_leases SET lease_until=statement_timestamp(),updated_at=statement_timestamp() WHERE proxy_id=$1 AND owner_id=$2 AND fence_token=$3 AND lease_until>statement_timestamp()`, lease.ProxyID, lease.OwnerID, lease.FenceToken)
		return releasedLeaseResult(result, err, ErrProxyLeaseLost)
	}
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE proxy_latency_proxy_leases SET lease_until=?,updated_at=? WHERE proxy_id=? AND owner_id=? AND fence_token=? AND lease_until>?`, nowText, nowText, lease.ProxyID, lease.OwnerID, lease.FenceToken, nowText)
	return releasedLeaseResult(result, err, ErrProxyLeaseLost)
}

func releasedLeaseResult(result sql.Result, err error, lost error) error {
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return lost
	}
	return nil
}

// AppendOutcome commits immutable, sanitized evidence once. A replay must
// match the original request identity exactly; a later config never changes
// the interpretation of an earlier request ID.
func (s *Store) AppendOutcome(ctx context.Context, owner OwnerLease, proxy ProxyLease, outcome Outcome) (bool, error) {
	if err := validateOutcome(outcome); err != nil {
		return false, err
	}
	payload, err := json.Marshal(outcome)
	if err != nil {
		return false, fmt.Errorf("编码 proxy-latency outcome 失败: %w", err)
	}
	payloadDigest := sha256.Sum256(payload)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	// Replays are identified by an already persisted outcome. They stay
	// idempotent even after the issuing input or execution lease expires;
	// current fences apply only to a first insert.
	existingMatches, err := s.outcomeMatchesTx(ctx, tx, outcome, hex.EncodeToString(payloadDigest[:]))
	if err == nil {
		if !existingMatches {
			return false, ErrRequestConflict
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	// Caller lease/token shape is checked only for a first insert. A replay
	// must remain idempotent after lease takeover, so the successor's current
	// token is intentionally not compared before the persisted request lookup.
	if proxy.OwnerID != owner.OwnerID || proxy.ProxyID != outcome.ProxyID || !validOwnerLease(owner) || proxy.FenceToken < 1 || outcome.OwnerFenceToken != owner.FenceToken || outcome.ProxyFenceToken != proxy.FenceToken {
		return false, errors.New("proxy-latency outcome lease 与输入不匹配")
	}
	if err := s.verifyOwnerTx(ctx, tx, owner, now); err != nil {
		return false, err
	}
	if err := s.verifyProxyTx(ctx, tx, proxy, now); err != nil {
		return false, err
	}
	if outcome.executionClaimToken != "" {
		if err := s.verifyExecutionClaimTx(ctx, tx, owner, proxy, outcome, outcome.executionClaimToken, now); err != nil {
			return false, err
		}
	}
	if err := s.verifyIssuedInputTx(ctx, tx, outcome, now); err != nil {
		return false, err
	}
	inserted, err := s.insertOutcomeTx(ctx, tx, outcome, string(payload), hex.EncodeToString(payloadDigest[:]), now)
	if err != nil {
		return false, err
	}
	if !inserted {
		matches, err := s.outcomeMatchesTx(ctx, tx, outcome, hex.EncodeToString(payloadDigest[:]))
		if err != nil {
			return false, err
		}
		if !matches {
			return false, ErrRequestConflict
		}
	}
	if outcome.executionClaimToken != "" {
		if err := s.deleteExecutionClaimTx(ctx, tx, outcome.RequestID, outcome.executionClaimToken); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

// IssueInput is the only path that turns a read-only business draft into an
// executable request. The request ID and per-proxy input version are persisted
// before the caller can probe upstream, making replay identity independent of
// the current proxy snapshot.
func (s *Store) IssueInput(ctx context.Context, draft InputDraft) (IssuedInput, error) {
	canonicalDraft, err := canonicalizeInputDraft(draft)
	if err != nil {
		return IssuedInput{}, err
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return IssuedInput{}, err
	}
	requestID, err := newRequestID()
	if err != nil {
		return IssuedInput{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return IssuedInput{}, err
	}
	defer tx.Rollback()
	version, err := nextInputVersion(ctx, tx, s.mode, canonicalDraft.ProxyID)
	if err != nil {
		return IssuedInput{}, err
	}
	issued := IssuedInput{RequestID: requestID, ProxyID: canonicalDraft.ProxyID, InputVersion: version, ConfigRevision: canonicalDraft.ConfigRevision, Trigger: canonicalDraft.Trigger, IssuedAt: canonicalDraft.IssuedAt, ExpiresAt: canonicalDraft.ExpiresAt, PolicyVersion: canonicalDraft.PolicyVersion, ProxyType: canonicalDraft.ProxyType, ProxyHost: canonicalDraft.ProxyHost, ProxyPort: canonicalDraft.ProxyPort, ProxyUsername: canonicalDraft.ProxyUsername, ProxyPassword: canonicalDraft.ProxyPassword, Targets: append([]Target(nil), canonicalDraft.Targets...)}
	payload, err := json.Marshal(issued)
	if err != nil {
		return IssuedInput{}, fmt.Errorf("编码 J3a issued input 失败: %w", err)
	}
	digest := sha256.Sum256(payload)
	if s.mode == StorePostgres {
		_, err = tx.ExecContext(ctx, `INSERT INTO juhe_jobs.proxy_latency_inputs(request_id,proxy_id,input_version,config_revision,trigger,issued_at,expires_at,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, requestID, issued.ProxyID, issued.InputVersion, issued.ConfigRevision, issued.Trigger, issued.IssuedAt, issued.ExpiresAt, payload, hex.EncodeToString(digest[:]))
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO proxy_latency_inputs(request_id,proxy_id,input_version,config_revision,trigger,issued_at,expires_at,payload,payload_digest) VALUES(?,?,?,?,?,?,?,?,?)`, requestID, issued.ProxyID, issued.InputVersion, issued.ConfigRevision, issued.Trigger, issued.IssuedAt.Format(time.RFC3339Nano), issued.ExpiresAt.Format(time.RFC3339Nano), payload, hex.EncodeToString(digest[:]))
	}
	if err != nil {
		return IssuedInput{}, fmt.Errorf("持久化 J3a issued input 失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return IssuedInput{}, err
	}
	return issued, nil
}

// LoadCommittedOutcome returns a durable replay only when input is equivalent
// to the Store-issued immutable request under the canonical JSON digest. It
// deliberately does not require a currently valid execution lease: a
// committed request remains idempotent after lease handoff or input expiry.
func (s *Store) LoadCommittedOutcome(ctx context.Context, input IssuedInput) (Outcome, bool, error) {
	if err := validateIssuedInput(input); err != nil {
		return Outcome{}, false, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Outcome{}, false, err
	}
	defer tx.Rollback()
	if err := s.verifyIssuedInputSnapshotTx(ctx, tx, input, time.Now().UTC(), false); err != nil {
		return Outcome{}, false, err
	}
	query := `SELECT payload,payload_digest FROM proxy_latency_outcomes WHERE request_id=? AND committed=1`
	if s.mode == StorePostgres {
		query = `SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 AND committed=TRUE`
	}
	var payload []byte
	var storedDigest string
	if err := tx.QueryRowContext(ctx, query, input.RequestID).Scan(&payload, &storedDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Commit(); err != nil {
				return Outcome{}, false, err
			}
			return Outcome{}, false, nil
		}
		return Outcome{}, false, fmt.Errorf("读取 J3a committed outcome 失败: %w", err)
	}
	var outcome Outcome
	if err := json.Unmarshal(payload, &outcome); err != nil || validateOutcome(outcome) != nil {
		return Outcome{}, false, ErrRequestConflict
	}
	if !payloadDigestMatches(s.mode, payload, outcome, storedDigest) {
		return Outcome{}, false, ErrRequestConflict
	}
	if outcome.RequestID != input.RequestID || outcome.ProxyID != input.ProxyID || outcome.InputVersion != input.InputVersion || outcome.ConfigRevision != input.ConfigRevision || outcome.Trigger != input.Trigger {
		return Outcome{}, false, ErrRequestConflict
	}
	if err := tx.Commit(); err != nil {
		return Outcome{}, false, err
	}
	return outcome, true, nil
}

// VerifyExecutionInput checks the Store-issued input and the live owner/proxy
// fences before an executor may contact an upstream. AppendOutcome repeats the
// checks in its write transaction, so a lease lost during the probe cannot
// commit a stale outcome.
func (s *Store) VerifyExecutionInput(ctx context.Context, owner OwnerLease, proxy ProxyLease, input IssuedInput) error {
	if err := validateIssuedInput(input); err != nil {
		return err
	}
	if proxy.OwnerID != owner.OwnerID || proxy.ProxyID != input.ProxyID || !validOwnerLease(owner) || proxy.FenceToken < 1 {
		return errors.New("proxy-latency executor lease 与 issued input 不匹配")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if err := s.verifyOwnerTx(ctx, tx, owner, now); err != nil {
		return err
	}
	if err := s.verifyProxyTx(ctx, tx, proxy, now); err != nil {
		return err
	}
	if err := s.verifyIssuedInputSnapshotTx(ctx, tx, input, now, true); err != nil {
		return err
	}
	return tx.Commit()
}

// AdmitExecution atomically resolves the durable input, checks the live
// fences, and claims the request before any upstream I/O. The returned input
// is a JSON round-trip deep copy owned by the Store; callers must not use the
// supplied IssuedInput after this hand-off.
func (s *Store) AdmitExecution(ctx context.Context, owner OwnerLease, proxy ProxyLease, supplied IssuedInput) (IssuedInput, string, *Outcome, error) {
	if err := validateIssuedInput(supplied); err != nil {
		return IssuedInput{}, "", nil, err
	}
	if proxy.OwnerID != owner.OwnerID || proxy.ProxyID != supplied.ProxyID || !validOwnerLease(owner) || proxy.FenceToken < 1 {
		return IssuedInput{}, "", nil, errors.New("proxy-latency executor lease 与 issued input 不匹配")
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return IssuedInput{}, "", nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return IssuedInput{}, "", nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	persisted, storedDigest, err := s.loadPersistedInputTx(ctx, tx, supplied.RequestID)
	if err != nil {
		return IssuedInput{}, "", nil, err
	}
	if !issuedInputsEqual(supplied, persisted, storedDigest) {
		return IssuedInput{}, "", nil, ErrInputFence
	}
	if committed, err := s.loadCommittedOutcomeTx(ctx, tx, persisted); err != nil {
		return IssuedInput{}, "", nil, err
	} else if committed != nil {
		if err := tx.Commit(); err != nil {
			return IssuedInput{}, "", nil, err
		}
		return persisted, "", committed, nil
	}
	if err := s.verifyOwnerTx(ctx, tx, owner, now); err != nil {
		return IssuedInput{}, "", nil, err
	}
	if err := s.verifyProxyTx(ctx, tx, proxy, now); err != nil {
		return IssuedInput{}, "", nil, err
	}
	if !persisted.ExpiresAt.After(now) || now.Before(persisted.IssuedAt) {
		return IssuedInput{}, "", nil, ErrInputFence
	}
	claimToken, err := newClaimToken()
	if err != nil {
		return IssuedInput{}, "", nil, err
	}
	outcomeID := stableOutcomeID(persisted.RequestID)
	claimUntil := persisted.ExpiresAt
	claimExists, claimUntilStored, err := s.executionClaimTx(ctx, tx, persisted.RequestID)
	if err != nil {
		return IssuedInput{}, "", nil, err
	}
	if claimExists && claimUntilStored.After(now) {
		return IssuedInput{}, "", nil, ErrRequestInFlight
	}
	args := []any{persisted.RequestID, claimToken, outcomeID, persisted.ProxyID, persisted.InputVersion, persisted.ConfigRevision, persisted.Trigger, owner.OwnerID, owner.FenceToken, proxy.FenceToken, storedDigest, claimUntil, now}
	var execErr error
	if claimExists {
		if s.mode == StorePostgres {
			_, execErr = tx.ExecContext(ctx, `UPDATE juhe_jobs.proxy_latency_execution_claims SET claim_token=$2,outcome_id=$3,proxy_id=$4,input_version=$5,config_revision=$6,trigger=$7,owner_id=$8,owner_fence_token=$9,proxy_fence_token=$10,input_digest=$11,claim_until=$12,updated_at=$13 WHERE request_id=$1`, args...)
		} else {
			_, execErr = tx.ExecContext(ctx, `UPDATE proxy_latency_execution_claims SET claim_token=?,outcome_id=?,proxy_id=?,input_version=?,config_revision=?,trigger=?,owner_id=?,owner_fence_token=?,proxy_fence_token=?,input_digest=?,claim_until=?,updated_at=? WHERE request_id=?`, claimToken, outcomeID, persisted.ProxyID, persisted.InputVersion, persisted.ConfigRevision, persisted.Trigger, owner.OwnerID, owner.FenceToken, proxy.FenceToken, storedDigest, claimUntil.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), persisted.RequestID)
		}
	} else if s.mode == StorePostgres {
		_, execErr = tx.ExecContext(ctx, `INSERT INTO juhe_jobs.proxy_latency_execution_claims(request_id,claim_token,outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, args...)
	} else {
		_, execErr = tx.ExecContext(ctx, `INSERT INTO proxy_latency_execution_claims(request_id,claim_token,outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, persisted.RequestID, claimToken, outcomeID, persisted.ProxyID, persisted.InputVersion, persisted.ConfigRevision, persisted.Trigger, owner.OwnerID, owner.FenceToken, proxy.FenceToken, storedDigest, claimUntil.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	}
	if execErr != nil {
		return IssuedInput{}, "", nil, fmt.Errorf("创建 J3a execution claim 失败: %w", execErr)
	}
	if err := tx.Commit(); err != nil {
		return IssuedInput{}, "", nil, err
	}
	return persisted, claimToken, nil, nil
}

func (s *Store) ReleaseExecutionClaim(ctx context.Context, requestID, claimToken string) error {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(claimToken) == "" {
		return ErrInputFence
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.mode == StorePostgres {
		_, err := s.db.ExecContext(ctx, `DELETE FROM juhe_jobs.proxy_latency_execution_claims WHERE request_id=$1 AND claim_token=$2`, requestID, claimToken)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM proxy_latency_execution_claims WHERE request_id=? AND claim_token=?`, requestID, claimToken)
	return err
}

func (s *Store) loadPersistedInputTx(ctx context.Context, tx *sql.Tx, requestID string) (IssuedInput, string, error) {
	query := `SELECT payload,payload_digest FROM proxy_latency_inputs WHERE request_id=?`
	if s.mode == StorePostgres {
		query = `SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1 FOR UPDATE`
	}
	var payload []byte
	var digest string
	if err := tx.QueryRowContext(ctx, query, requestID).Scan(&payload, &digest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssuedInput{}, "", ErrInputFence
		}
		return IssuedInput{}, "", err
	}
	var input IssuedInput
	if err := json.Unmarshal(payload, &input); err != nil || validateIssuedInput(input) != nil || input.RequestID != requestID {
		return IssuedInput{}, "", ErrInputFence
	}
	if !payloadDigestMatches(s.mode, payload, input, digest) {
		return IssuedInput{}, "", ErrInputFence
	}
	return input, digest, nil
}

func issuedInputsEqual(supplied, persisted IssuedInput, storedDigest string) bool {
	digest, err := canonicalJSONDigest(supplied)
	return err == nil && supplied.RequestID == persisted.RequestID && digest == storedDigest
}

func (s *Store) loadCommittedOutcomeTx(ctx context.Context, tx *sql.Tx, input IssuedInput) (*Outcome, error) {
	query := `SELECT payload,payload_digest FROM proxy_latency_outcomes WHERE request_id=? AND committed=1`
	if s.mode == StorePostgres {
		query = `SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 AND committed=TRUE FOR UPDATE`
	}
	var payload []byte
	var storedDigest string
	if err := tx.QueryRowContext(ctx, query, input.RequestID).Scan(&payload, &storedDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var outcome Outcome
	if err := json.Unmarshal(payload, &outcome); err != nil || validateOutcome(outcome) != nil || outcome.RequestID != input.RequestID || outcome.ProxyID != input.ProxyID || outcome.InputVersion != input.InputVersion || outcome.ConfigRevision != input.ConfigRevision || outcome.Trigger != input.Trigger {
		return nil, ErrRequestConflict
	}
	if !payloadDigestMatches(s.mode, payload, outcome, storedDigest) {
		return nil, ErrRequestConflict
	}
	return &outcome, nil
}

// canonicalJSONDigest hashes the typed JSON representation used at write time.
// PostgreSQL payload columns are JSONB, so hashing raw SELECT bytes is unsafe:
// JSONB may reorder object keys while preserving the same semantic value.
func canonicalJSONDigest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func payloadDigestMatches(mode StoreMode, raw []byte, decoded any, stored string) bool {
	if mode != StorePostgres {
		digest := sha256.Sum256(raw)
		return stored == hex.EncodeToString(digest[:])
	}
	actual, err := canonicalJSONDigest(decoded)
	return err == nil && stored == actual
}

func (s *Store) executionClaimTx(ctx context.Context, tx *sql.Tx, requestID string) (bool, time.Time, error) {
	query := `SELECT claim_until FROM proxy_latency_execution_claims WHERE request_id=?`
	if s.mode == StorePostgres {
		query = `SELECT claim_until FROM juhe_jobs.proxy_latency_execution_claims WHERE request_id=$1 FOR UPDATE`
	}
	var raw any
	if err := tx.QueryRowContext(ctx, query, requestID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, time.Time{}, nil
		}
		return false, time.Time{}, err
	}
	until, err := sqlTime(raw)
	if err != nil {
		return false, time.Time{}, ErrInputFence
	}
	return true, until, nil
}

func newClaimToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成 J3a execution claim token 失败: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func stableOutcomeID(requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return "j3a-outcome-" + hex.EncodeToString(digest[:16])
}

func validateInputDraft(draft InputDraft) error {
	_, err := canonicalizeInputDraft(draft)
	return err
}

func canonicalizeInputDraft(draft InputDraft) (InputDraft, error) {
	if strings.TrimSpace(draft.ProxyID) == "" || draft.IssuedAt.IsZero() || draft.ExpiresAt.IsZero() || draft.IssuedAt.Location() != time.UTC || draft.ExpiresAt.Location() != time.UTC {
		return InputDraft{}, errors.New("J3a input draft 时间必须为 UTC")
	}
	issuedAt := draft.IssuedAt
	expiresAt := draft.ExpiresAt
	if !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) < time.Minute || expiresAt.Sub(issuedAt) > 15*time.Minute || draft.Trigger != TriggerPeriodic && draft.Trigger != TriggerManual || draft.PolicyVersion != proxyLatencyInputPolicyVersion || !validProxyLatencyType(draft.ProxyType) || strings.TrimSpace(draft.ProxyHost) == "" || draft.ProxyPort < 1 || draft.ProxyPort > 65535 || len(draft.Targets) == 0 {
		return InputDraft{}, errors.New("J3a input draft 字段无效")
	}
	canonicalRevision, err := canonicalConfigRevision(draft.ConfigRevision)
	if err != nil {
		return InputDraft{}, err
	}
	if draft.ProxyPassword != nil && !validProxyLatencyEnvelope(draft.ProxyPassword.Ciphertext) {
		return InputDraft{}, errors.New("J3a input draft password envelope 无效")
	}
	if draft.ProxyPassword != nil && draft.ProxyPassword.Kind != "proxy_password" {
		return InputDraft{}, errors.New("J3a input draft password envelope 类型无效")
	}
	// Node's proxy URL builder only emits credentials when a username exists.
	// Drop a password-only envelope at the canonical boundary rather than
	// changing that behavior into an HTTP/SOCKS ":password" credential.
	if strings.TrimSpace(draft.ProxyUsername) == "" {
		draft.ProxyPassword = nil
	}
	seenProviders := make(map[string]struct{}, len(draft.Targets))
	canonicalTargets := make([]Target, 0, len(draft.Targets))
	for _, target := range draft.Targets {
		canonicalTarget, err := canonicalizeTarget(target)
		if err != nil {
			return InputDraft{}, err
		}
		providerKey := canonicalTarget.Provider
		if _, exists := seenProviders[providerKey]; exists {
			return InputDraft{}, errors.New("J3a input draft target 重复")
		}
		seenProviders[providerKey] = struct{}{}
		canonicalTargets = append(canonicalTargets, canonicalTarget)
	}
	draft.ConfigRevision = canonicalRevision
	draft.Targets = canonicalTargets
	return draft, nil
}

func validateIssuedInput(input IssuedInput) error {
	if strings.TrimSpace(input.RequestID) == "" || input.InputVersion < 1 {
		return ErrInputFence
	}
	// A durable input must not retain the password-only envelope that the
	// Node URL builder drops. Reject legacy/tampered rows rather than allowing
	// the executor to reinterpret them as a credential.
	if input.ProxyPassword != nil && strings.TrimSpace(input.ProxyUsername) == "" {
		return ErrInputFence
	}
	draft, err := canonicalizeInputDraft(InputDraft{
		ProxyID: input.ProxyID, ConfigRevision: input.ConfigRevision, Trigger: input.Trigger,
		IssuedAt: input.IssuedAt, ExpiresAt: input.ExpiresAt, PolicyVersion: input.PolicyVersion,
		ProxyType: input.ProxyType, ProxyHost: input.ProxyHost, ProxyPort: input.ProxyPort,
		ProxyUsername: input.ProxyUsername, ProxyPassword: input.ProxyPassword, Targets: input.Targets,
	})
	if err != nil || draft.ConfigRevision != input.ConfigRevision || len(draft.Targets) != len(input.Targets) {
		return ErrInputFence
	}
	for index := range draft.Targets {
		if draft.Targets[index] != input.Targets[index] {
			return ErrInputFence
		}
	}
	return nil
}

func canonicalConfigRevision(value string) (string, error) {
	if strings.TrimSpace(value) != value {
		return "", errors.New("config revision 不得包含外围空白")
	}
	parsed, err := parseProxyLatencyUTC(value, "config revision")
	if err != nil {
		return "", err
	}
	return parsed.Format(time.RFC3339Nano), nil
}

func canonicalizeTarget(target Target) (Target, error) {
	provider := strings.ToLower(strings.TrimSpace(target.Provider))
	profileID := strings.TrimSpace(target.ProfileID)
	if provider == "" || profileID == "" {
		return Target{}, errors.New("J3a input draft target 标识无效")
	}
	parsed, err := parseTargetURL(target.URL)
	if err != nil {
		return Target{}, errors.New("J3a input draft target URL 无效")
	}
	return Target{Provider: provider, ProfileID: profileID, URL: parsed.String()}, nil
}

func (s *Store) verifyIssuedInputTx(ctx context.Context, tx *sql.Tx, outcome Outcome, now time.Time) error {
	query := `SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM proxy_latency_inputs WHERE request_id=?`
	if s.mode == StorePostgres {
		query = `SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1 FOR UPDATE`
	}
	var proxyID, configRevision, trigger string
	var inputVersion int64
	var issuedAtRaw, expiresAtRaw any
	args := []any{outcome.RequestID}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&proxyID, &inputVersion, &configRevision, &trigger, &issuedAtRaw, &expiresAtRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInputFence
		}
		return fmt.Errorf("读取 J3a issued input fence 失败: %w", err)
	}
	issuedAt, err := sqlTime(issuedAtRaw)
	if err != nil {
		return ErrInputFence
	}
	expiresAt, err := sqlTime(expiresAtRaw)
	if err != nil || !expiresAt.After(now) || outcome.ObservedAt.Before(issuedAt) || !outcome.ObservedAt.Before(expiresAt) {
		return ErrInputFence
	}
	if proxyID != outcome.ProxyID || inputVersion != outcome.InputVersion || configRevision != outcome.ConfigRevision || trigger != string(outcome.Trigger) {
		return ErrInputFence
	}
	return nil
}

func (s *Store) verifyExecutionClaimTx(ctx context.Context, tx *sql.Tx, owner OwnerLease, proxy ProxyLease, outcome Outcome, claimToken string, now time.Time) error {
	query := `SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until FROM proxy_latency_execution_claims WHERE request_id=? AND claim_token=?`
	if s.mode == StorePostgres {
		query = `SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until FROM juhe_jobs.proxy_latency_execution_claims WHERE request_id=$1 AND claim_token=$2 FOR UPDATE`
	}
	var outcomeID, proxyID, configRevision, trigger, claimOwner, inputDigest string
	var inputVersion, ownerFence, proxyFence int64
	var claimUntilRaw any
	if err := tx.QueryRowContext(ctx, query, outcome.RequestID, claimToken).Scan(&outcomeID, &proxyID, &inputVersion, &configRevision, &trigger, &claimOwner, &ownerFence, &proxyFence, &inputDigest, &claimUntilRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInputFence
		}
		return err
	}
	claimUntil, err := sqlTime(claimUntilRaw)
	if err != nil || !claimUntil.After(now) || outcomeID != stableOutcomeID(outcome.RequestID) || proxyID != outcome.ProxyID || inputVersion != outcome.InputVersion || configRevision != outcome.ConfigRevision || trigger != string(outcome.Trigger) || claimOwner != owner.OwnerID || ownerFence != owner.FenceToken || proxyFence != proxy.FenceToken {
		return ErrInputFence
	}
	inputQuery := `SELECT payload_digest FROM proxy_latency_inputs WHERE request_id=?`
	if s.mode == StorePostgres {
		inputQuery = `SELECT payload_digest FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1 FOR UPDATE`
	}
	var currentDigest string
	if err := tx.QueryRowContext(ctx, inputQuery, outcome.RequestID).Scan(&currentDigest); err != nil || currentDigest != inputDigest {
		return ErrInputFence
	}
	return nil
}

func (s *Store) deleteExecutionClaimTx(ctx context.Context, tx *sql.Tx, requestID, claimToken string) error {
	query := `DELETE FROM proxy_latency_execution_claims WHERE request_id=? AND claim_token=?`
	if s.mode == StorePostgres {
		query = `DELETE FROM juhe_jobs.proxy_latency_execution_claims WHERE request_id=$1 AND claim_token=$2`
	}
	result, err := tx.ExecContext(ctx, query, requestID, claimToken)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrInputFence
	}
	return nil
}

func (s *Store) verifyIssuedInputSnapshotTx(ctx context.Context, tx *sql.Tx, input IssuedInput, now time.Time, requireActive bool) error {
	query := `SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at,payload_digest FROM proxy_latency_inputs WHERE request_id=?`
	if s.mode == StorePostgres {
		query = `SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at,payload_digest FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1 FOR UPDATE`
	}
	var proxyID, configRevision, trigger, storedDigest string
	var inputVersion int64
	var issuedAtRaw, expiresAtRaw any
	if err := tx.QueryRowContext(ctx, query, input.RequestID).Scan(&proxyID, &inputVersion, &configRevision, &trigger, &issuedAtRaw, &expiresAtRaw, &storedDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInputFence
		}
		return fmt.Errorf("读取 J3a issued input snapshot 失败: %w", err)
	}
	issuedAt, err := sqlTime(issuedAtRaw)
	if err != nil {
		return ErrInputFence
	}
	expiresAt, err := sqlTime(expiresAtRaw)
	if err != nil || (requireActive && (!expiresAt.After(now) || now.Before(issuedAt))) {
		return ErrInputFence
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ErrInputFence
	}
	digest := sha256.Sum256(payload)
	if proxyID != input.ProxyID || inputVersion != input.InputVersion || configRevision != input.ConfigRevision || trigger != string(input.Trigger) || !issuedAt.Equal(input.IssuedAt) || !expiresAt.Equal(input.ExpiresAt) || storedDigest != hex.EncodeToString(digest[:]) {
		return ErrInputFence
	}
	return nil
}

func nextInputVersion(ctx context.Context, tx *sql.Tx, mode StoreMode, proxyID string) (int64, error) {
	if mode == StorePostgres {
		var version int64
		err := tx.QueryRowContext(ctx, `INSERT INTO juhe_jobs.proxy_latency_input_versions(proxy_id,next_version,updated_at) VALUES($1,2,statement_timestamp()) ON CONFLICT(proxy_id) DO UPDATE SET next_version=juhe_jobs.proxy_latency_input_versions.next_version+1,updated_at=statement_timestamp() RETURNING next_version-1`, proxyID).Scan(&version)
		return version, err
	}
	var version int64
	err := tx.QueryRowContext(ctx, `INSERT INTO proxy_latency_input_versions(proxy_id,next_version,updated_at) VALUES(?,2,?) ON CONFLICT(proxy_id) DO UPDATE SET next_version=proxy_latency_input_versions.next_version+1,updated_at=excluded.updated_at RETURNING next_version-1`, proxyID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&version)
	return version, err
}

func newRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成 J3a request ID 失败: %w", err)
	}
	return "j3a-" + hex.EncodeToString(raw[:]), nil
}

func validOwnerLease(lease OwnerLease) bool {
	return strings.TrimSpace(lease.OwnerID) != "" && lease.FenceToken > 0
}

func validateOutcome(outcome Outcome) error {
	if strings.TrimSpace(outcome.OutcomeID) == "" || strings.TrimSpace(outcome.RequestID) == "" || strings.TrimSpace(outcome.ProxyID) == "" || outcome.InputVersion < 1 || outcome.OwnerFenceToken < 1 || outcome.ProxyFenceToken < 1 || outcome.ObservedAt.IsZero() {
		return errors.New("proxy-latency outcome 缺少幂等或 fence 字段")
	}
	canonicalRevision, err := canonicalConfigRevision(outcome.ConfigRevision)
	if err != nil || canonicalRevision != outcome.ConfigRevision {
		return errors.New("proxy-latency outcome config revision 必须为规范 RFC3339 UTC")
	}
	if outcome.Trigger != TriggerPeriodic && outcome.Trigger != TriggerManual {
		return errors.New("proxy-latency outcome trigger 无效")
	}
	switch outcome.OverallStatus {
	case OverallPassed, OverallWarning, OverallFailed, OverallUnknown:
	default:
		return errors.New("proxy-latency outcome overall status 无效")
	}
	return nil
}

func (s *Store) beginTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if s.mode == StorePostgres {
		if _, err := tx.ExecContext(ctx, postgresSetLocalSQL); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("配置 proxy-latency PostgreSQL transaction timeout 失败: %w", err)
		}
	}
	return tx, nil
}

func (s *Store) verifyOwnerTx(ctx context.Context, tx *sql.Tx, owner OwnerLease, now time.Time) error {
	var foundOwner string
	var token int64
	var until any
	query := `SELECT owner_id,fence_token,lease_until FROM proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner'`
	if s.mode == StorePostgres {
		query = `SELECT owner_id,fence_token,lease_until FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' FOR UPDATE`
	}
	if err := tx.QueryRowContext(ctx, query).Scan(&foundOwner, &token, &until); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOwnerLeaseLost
		}
		return err
	}
	leaseUntil, err := sqlTime(until)
	if err != nil || foundOwner != owner.OwnerID || token != owner.FenceToken || !leaseUntil.After(now) {
		return ErrOwnerLeaseLost
	}
	return nil
}

func (s *Store) verifyProxyTx(ctx context.Context, tx *sql.Tx, proxy ProxyLease, now time.Time) error {
	var foundOwner string
	var token int64
	var until any
	query := `SELECT owner_id,fence_token,lease_until FROM proxy_latency_proxy_leases WHERE proxy_id=?`
	args := []any{proxy.ProxyID}
	if s.mode == StorePostgres {
		query = `SELECT owner_id,fence_token,lease_until FROM juhe_jobs.proxy_latency_proxy_leases WHERE proxy_id=$1 FOR UPDATE`
	}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&foundOwner, &token, &until); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProxyLeaseLost
		}
		return err
	}
	leaseUntil, err := sqlTime(until)
	if err != nil || foundOwner != proxy.OwnerID || token != proxy.FenceToken || !leaseUntil.After(now) {
		return ErrProxyLeaseLost
	}
	return nil
}

func (s *Store) insertOutcomeTx(ctx context.Context, tx *sql.Tx, outcome Outcome, payload, payloadDigest string, storedAt time.Time) (bool, error) {
	if s.mode == StorePostgres {
		var id string
		err := tx.QueryRowContext(ctx, `INSERT INTO juhe_jobs.proxy_latency_outcomes(outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,observed_at,stored_at,payload,payload_digest,committed)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,statement_timestamp(),$10,$11,TRUE) ON CONFLICT(request_id) DO NOTHING RETURNING outcome_id`, outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, outcome.Trigger, outcome.OwnerFenceToken, outcome.ProxyFenceToken, outcome.ObservedAt.UTC(), payload, payloadDigest).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO proxy_latency_outcomes(outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,observed_at,stored_at,payload,payload_digest,committed)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1) ON CONFLICT(request_id) DO NOTHING`, outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, outcome.Trigger, outcome.OwnerFenceToken, outcome.ProxyFenceToken, outcome.ObservedAt.UTC().Format(time.RFC3339Nano), storedAt.UTC().Format(time.RFC3339Nano), payload, payloadDigest)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) outcomeMatchesTx(ctx context.Context, tx *sql.Tx, outcome Outcome, payloadDigest string) (bool, error) {
	query := `SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,payload_digest FROM proxy_latency_outcomes WHERE request_id=?`
	if s.mode == StorePostgres {
		query = `SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,payload_digest FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 FOR UPDATE`
	}
	var outcomeID, proxyID, configRevision, trigger, storedDigest string
	var inputVersion, ownerFenceToken, proxyFenceToken int64
	if err := tx.QueryRowContext(ctx, query, outcome.RequestID).Scan(&outcomeID, &proxyID, &inputVersion, &configRevision, &trigger, &ownerFenceToken, &proxyFenceToken, &storedDigest); err != nil {
		return false, err
	}
	return outcomeID == outcome.OutcomeID && proxyID == outcome.ProxyID && inputVersion == outcome.InputVersion && configRevision == outcome.ConfigRevision && trigger == string(outcome.Trigger) && ownerFenceToken == outcome.OwnerFenceToken && proxyFenceToken == outcome.ProxyFenceToken && storedDigest == payloadDigest, nil
}

func sqlTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case string:
		return time.Parse(time.RFC3339Nano, typed)
	case []byte:
		return time.Parse(time.RFC3339Nano, string(typed))
	default:
		return time.Time{}, errors.New("proxy-latency lease 时间类型无效")
	}
}

func proxyLatencySQLiteDSN(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析 proxy-latency sqlite 路径失败: %w", err)
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "_txlock=immediate&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"}).String(), nil
}

func containsForbiddenPostgresDDL(schema string) bool {
	for _, forbidden := range []string{"CREATE SCHEMA", "ALTER TABLE", "DROP ", "TRUNCATE ", "proxy_profiles", "juhe_business"} {
		if strings.Contains(strings.ToUpper(schema), strings.ToUpper(forbidden)) {
			return true
		}
	}
	return false
}

const postgresSetLocalSQL = `SET LOCAL statement_timeout = '5000ms'; SET LOCAL lock_timeout = '1000ms'`

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS proxy_latency_owner_leases (
 lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, lease_until TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS proxy_latency_proxy_leases (
 proxy_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, lease_until TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS proxy_latency_outcomes (
 outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, proxy_id TEXT NOT NULL, input_version INTEGER NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_fence_token INTEGER NOT NULL, proxy_fence_token INTEGER NOT NULL, observed_at TEXT NOT NULL, stored_at TEXT NOT NULL, payload TEXT NOT NULL, payload_digest TEXT NOT NULL, committed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS proxy_latency_input_versions (
 proxy_id TEXT PRIMARY KEY, next_version INTEGER NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS proxy_latency_inputs (
 request_id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL, input_version INTEGER NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT NOT NULL, payload TEXT NOT NULL, payload_digest TEXT NOT NULL, UNIQUE(proxy_id, input_version)
);
CREATE TABLE IF NOT EXISTS proxy_latency_execution_claims (
 request_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, proxy_id TEXT NOT NULL, input_version INTEGER NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_id TEXT NOT NULL, owner_fence_token INTEGER NOT NULL, proxy_fence_token INTEGER NOT NULL, input_digest TEXT NOT NULL, claim_until TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_proxy ON proxy_latency_outcomes(proxy_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_cursor ON proxy_latency_outcomes(stored_at, outcome_id);
`

const postgresSchema = `
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_owner_leases (
 lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_proxy_leases (
 proxy_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_outcomes (
 outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_fence_token BIGINT NOT NULL, proxy_fence_token BIGINT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, committed BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_input_versions (
 proxy_id TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_inputs (
 request_id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, UNIQUE(proxy_id, input_version)
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_execution_claims (
 request_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_id TEXT NOT NULL, owner_fence_token BIGINT NOT NULL, proxy_fence_token BIGINT NOT NULL, input_digest TEXT NOT NULL, claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_proxy ON juhe_jobs.proxy_latency_outcomes(proxy_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_cursor ON juhe_jobs.proxy_latency_outcomes(stored_at, outcome_id);
`

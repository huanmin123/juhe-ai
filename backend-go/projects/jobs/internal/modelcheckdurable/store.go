// Package modelcheckdurable persists the J3b input/claim/outcome boundary.
// It has no provider or Node dependency; callers must complete this boundary
// before any network probe is started.
package modelcheckdurable

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

var (
	ErrBusy            = errors.New("model check input is claimed by another owner")
	ErrExpired         = errors.New("model check input or claim is expired")
	ErrStaleFence      = errors.New("model check claim fence is stale")
	ErrOutcomeConflict = errors.New("model check outcome conflicts with existing outcome")
	ErrInputTampered   = errors.New("stored model check input failed integrity verification")
)

type Store struct {
	db   *sql.DB
	mode Mode
}
type Issued struct {
	Input       modelcheckinput.IssuedInput
	IdentityKey string
}
type Claim struct {
	InputID, ClaimToken, OutcomeID, OwnerID string
	FenceToken                              int64
	ClaimUntil                              time.Time
}
type Outcome struct {
	OutcomeID, InputID, InputDigest string
	FenceToken                      int64
	ObservedAt, StoredAt            time.Time
	Payload                         json.RawMessage
	PayloadDigest                   string
	Committed                       bool
}

func OpenSQLite(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("model check durable SQLite path is required")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open durable SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &Store{db: db, mode: SQLite}, nil
}

func OpenPostgres(dsn string, maxOpen int) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("model check durable PostgreSQL URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open durable PostgreSQL: %w", err)
	}
	if maxOpen <= 0 {
		maxOpen = 1000
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	return &Store{db: db, mode: Postgres}, nil
}

func New(db *sql.DB, mode Mode) (*Store, error) {
	if db == nil || (mode != SQLite && mode != Postgres) {
		return nil, errors.New("invalid model check durable store")
	}
	return &Store{db: db, mode: mode}, nil
}
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("model check durable store is not initialized")
	}
	if s.mode == Postgres {
		return s.CheckSchema(ctx)
	}
	_, err := s.db.ExecContext(ctx, sqliteSchema)
	if err != nil {
		return fmt.Errorf("initialize model check durable schema: %w", err)
	}
	return nil
}

func (s *Store) CheckSchema(ctx context.Context) error {
	for _, table := range []string{"model_check_input_versions", "model_check_inputs", "model_check_execution_claims", "model_check_outcomes"} {
		q := "SELECT 1 FROM " + table + " LIMIT 0"
		if s.mode == Postgres {
			q = "SELECT 1 FROM juhe_jobs." + table + " LIMIT 0"
		}
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("verify model check durable schema %s: %w", table, err)
		}
	}
	return nil
}

func (s *Store) Issue(ctx context.Context, draft modelcheckinput.Draft) (Issued, error) {
	base, err := modelcheckinput.Issue(draft)
	if err != nil {
		return Issued{}, err
	}
	identity, err := base.IdentityKey()
	if err != nil {
		return Issued{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issued{}, err
	}
	defer tx.Rollback()
	inputTable := s.table("model_check_inputs")
	versionsTable := s.table("model_check_input_versions")
	var existingPayload []byte
	err = tx.QueryRowContext(ctx, s.bind("SELECT payload FROM "+inputTable+" WHERE input_id=?"), base.InputID).Scan(&existingPayload)
	if err == nil {
		var existing modelcheckinput.IssuedInput
		if json.Unmarshal(existingPayload, &existing) != nil || existing.Verify() != nil {
			return Issued{}, errors.New("stored model check input is invalid")
		}
		candidate, issueErr := modelcheckinput.IssueVersioned(draft, existing.InputVersion)
		if issueErr != nil || candidate.InputDigest != existing.InputDigest {
			return Issued{}, errors.New("model check input ID is already bound to different immutable input")
		}
		if err := tx.Commit(); err != nil {
			return Issued{}, err
		}
		return Issued{Input: existing, IdentityKey: identity}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Issued{}, err
	}
	var next int64
	err = tx.QueryRowContext(ctx, s.lock("SELECT next_version FROM "+versionsTable+" WHERE identity_key=?"), identity).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		next = 1
		if _, err = tx.ExecContext(ctx, s.bind("INSERT INTO "+versionsTable+"(identity_key,next_version,updated_at) VALUES(?,?,?)"), identity, int64(2), s.timeValue(time.Now())); err != nil {
			return Issued{}, err
		}
	} else if err != nil {
		return Issued{}, err
	} else {
		if next < 1 {
			return Issued{}, errors.New("stored model check version is invalid")
		}
		if _, err = tx.ExecContext(ctx, s.bind("UPDATE "+versionsTable+" SET next_version=?,updated_at=? WHERE identity_key=?"), next+1, s.timeValue(time.Now()), identity); err != nil {
			return Issued{}, err
		}
	}
	input, err := modelcheckinput.IssueVersioned(draft, next)
	if err != nil {
		return Issued{}, err
	}
	payload, err := input.Payload()
	if err != nil {
		return Issued{}, err
	}
	_, err = tx.ExecContext(ctx, s.bind("INSERT INTO "+inputTable+"(input_id,identity_key,input_version,input_digest,target_id,config_revision,policy_revision,trigger,issued_at,expires_at,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?)"), input.InputID, identity, input.InputVersion, input.InputDigest, input.Target.ID, input.Target.ConfigRevision, input.Policy.Revision, input.Trigger, s.timeValue(input.IssuedAt), s.timeValue(input.DeadlineAt), payload)
	if err != nil {
		return Issued{}, fmt.Errorf("persist model check input: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Issued{}, err
	}
	return Issued{Input: input, IdentityKey: identity}, nil
}

// LoadInput replays the exact durable snapshot and verifies both its payload
// digest and the catalog identity key before a caller can execute probes.
func (s *Store) LoadInput(ctx context.Context, inputID string, now time.Time) (Issued, error) {
	if strings.TrimSpace(inputID) == "" {
		return Issued{}, errors.New("model check input ID is required")
	}
	var identity string
	var payload []byte
	var expiresRaw any
	if err := s.db.QueryRowContext(ctx, s.bind("SELECT identity_key,payload,expires_at FROM "+s.table("model_check_inputs")+" WHERE input_id=?"), inputID).Scan(&identity, &payload, &expiresRaw); err != nil {
		return Issued{}, err
	}
	var input modelcheckinput.IssuedInput
	if json.Unmarshal(payload, &input) != nil || input.Verify() != nil || input.InputID != inputID {
		return Issued{}, ErrInputTampered
	}
	computedIdentity, err := input.IdentityKey()
	if err != nil || computedIdentity != identity {
		return Issued{}, ErrInputTampered
	}
	var expires time.Time
	if expires, err = s.readTime(expiresRaw); err != nil {
		return Issued{}, err
	}
	if !now.Before(expires) {
		return Issued{}, ErrExpired
	}
	return Issued{Input: input, IdentityKey: identity}, nil
}

func (s *Store) Claim(ctx context.Context, inputID, ownerID, claimToken, outcomeID string, now time.Time, lease time.Duration) (Claim, error) {
	if strings.TrimSpace(inputID) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(claimToken) == "" || strings.TrimSpace(outcomeID) == "" || lease <= 0 {
		return Claim{}, errors.New("invalid model check claim")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback()
	var expiresRaw any
	if err := tx.QueryRowContext(ctx, s.lock("SELECT expires_at FROM "+s.table("model_check_inputs")+" WHERE input_id=?"), inputID).Scan(&expiresRaw); err != nil {
		return Claim{}, err
	}
	expires, err := s.readTime(expiresRaw)
	if err != nil {
		return Claim{}, err
	}
	if !now.Before(expires) {
		return Claim{}, ErrExpired
	}
	var c Claim
	var untilRaw any
	err = tx.QueryRowContext(ctx, s.lock("SELECT claim_token,outcome_id,owner_id,fence_token,claim_until FROM "+s.table("model_check_execution_claims")+" WHERE input_id=?"), inputID).Scan(&c.ClaimToken, &c.OutcomeID, &c.OwnerID, &c.FenceToken, &untilRaw)
	var until time.Time
	if err == nil {
		until, err = s.readTime(untilRaw)
		if err != nil {
			return Claim{}, err
		}
	}
	if err == nil && now.Before(until) && (c.ClaimToken != claimToken || c.OwnerID != ownerID) {
		return Claim{}, ErrBusy
	}
	if err == nil && c.ClaimToken == claimToken && c.OwnerID == ownerID && c.OutcomeID == outcomeID && now.Before(until) {
		c.InputID = inputID
		c.ClaimUntil = until
		if err := tx.Commit(); err != nil {
			return Claim{}, err
		}
		return c, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		c.FenceToken = 1
	} else if err != nil {
		return Claim{}, err
	} else {
		c.FenceToken++
	}
	c = Claim{InputID: inputID, ClaimToken: claimToken, OutcomeID: outcomeID, OwnerID: ownerID, FenceToken: c.FenceToken, ClaimUntil: now.Add(lease)}
	_, err = tx.ExecContext(ctx, s.bind("INSERT INTO "+s.table("model_check_execution_claims")+"(input_id,claim_token,outcome_id,owner_id,fence_token,claim_until,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(input_id) DO UPDATE SET claim_token=excluded.claim_token,outcome_id=excluded.outcome_id,owner_id=excluded.owner_id,fence_token=excluded.fence_token,claim_until=excluded.claim_until,updated_at=excluded.updated_at"), c.InputID, c.ClaimToken, c.OutcomeID, c.OwnerID, c.FenceToken, s.timeValue(c.ClaimUntil), s.timeValue(now))
	if err != nil {
		return Claim{}, err
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, err
	}
	return c, nil
}

func (s *Store) CommitOutcome(ctx context.Context, outcome Outcome, claim Claim, now time.Time) error {
	if outcome.InputID == "" || outcome.OutcomeID == "" || claim.ClaimToken == "" || outcome.InputDigest == "" || len(outcome.Payload) == 0 || !json.Valid(outcome.Payload) {
		return errors.New("invalid model check outcome")
	}
	sum := sha256.Sum256(outcome.Payload)
	digest := hex.EncodeToString(sum[:])
	if outcome.PayloadDigest != "" && outcome.PayloadDigest != digest {
		return errors.New("model check outcome payload digest mismatch")
	}
	outcome.PayloadDigest = digest
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var inputDigest string
	if err := tx.QueryRowContext(ctx, s.lock("SELECT input_digest FROM "+s.table("model_check_inputs")+" WHERE input_id=?"), outcome.InputID).Scan(&inputDigest); err != nil {
		return err
	}
	if inputDigest != outcome.InputDigest {
		return errors.New("model check outcome input digest mismatch")
	}
	var token, owner, oid string
	var fence int64
	var untilRaw any
	if err := tx.QueryRowContext(ctx, s.lock("SELECT claim_token,owner_id,outcome_id,fence_token,claim_until FROM "+s.table("model_check_execution_claims")+" WHERE input_id=?"), outcome.InputID).Scan(&token, &owner, &oid, &fence, &untilRaw); err != nil {
		return err
	}
	until, err := s.readTime(untilRaw)
	if err != nil {
		return err
	}
	if token != claim.ClaimToken || owner != claim.OwnerID || oid != claim.OutcomeID || fence != claim.FenceToken {
		return ErrStaleFence
	}
	var oldDigest string
	err = tx.QueryRowContext(ctx, s.lock("SELECT payload_digest FROM "+s.table("model_check_outcomes")+" WHERE input_id=?"), outcome.InputID).Scan(&oldDigest)
	if err == nil {
		if oldDigest != outcome.PayloadDigest {
			return ErrOutcomeConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !now.Before(until) {
		return ErrExpired
	}
	if outcome.ObservedAt.IsZero() {
		outcome.ObservedAt = now
	}
	if outcome.StoredAt.IsZero() {
		outcome.StoredAt = now
	}
	outcome.FenceToken = fence
	outcome.Committed = true
	committed := any(1)
	if s.mode == Postgres {
		committed = true
	}
	_, err = tx.ExecContext(ctx, s.bind("INSERT INTO "+s.table("model_check_outcomes")+"(outcome_id,input_id,input_digest,fence_token,observed_at,stored_at,payload,payload_digest,committed) VALUES(?,?,?,?,?,?,?,?,?)"), outcome.OutcomeID, outcome.InputID, outcome.InputDigest, outcome.FenceToken, s.timeValue(outcome.ObservedAt), s.timeValue(outcome.StoredAt), outcome.Payload, outcome.PayloadDigest, committed)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) table(name string) string {
	if s.mode == Postgres {
		return "juhe_jobs." + name
	}
	return name
}
func (s *Store) lock(q string) string {
	if s.mode == Postgres {
		q += " FOR UPDATE"
	}
	return s.bind(q)
}
func (s *Store) bind(q string) string {
	if s.mode != Postgres {
		return q
	}
	for i := 1; strings.Contains(q, "?"); i++ {
		q = strings.Replace(q, "?", fmt.Sprintf("$%d", i), 1)
	}
	return q
}

func (s *Store) timeValue(value time.Time) any {
	value = value.UTC()
	if s.mode == SQLite {
		return value.Format(time.RFC3339Nano)
	}
	return value
}

func (s *Store) readTime(raw any) (time.Time, error) {
	switch value := raw.(type) {
	case time.Time:
		return value.UTC(), nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid stored model check timestamp: %w", err)
		}
		return parsed.UTC(), nil
	case []byte:
		return s.readTime(string(value))
	default:
		return time.Time{}, errors.New("invalid stored model check timestamp type")
	}
}

const sqliteSchema = `CREATE TABLE IF NOT EXISTS model_check_input_versions(identity_key TEXT PRIMARY KEY,next_version INTEGER NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS model_check_inputs(input_id TEXT PRIMARY KEY,identity_key TEXT NOT NULL,input_version INTEGER NOT NULL,input_digest TEXT NOT NULL,target_id TEXT NOT NULL,config_revision TEXT NOT NULL,policy_revision TEXT NOT NULL,trigger TEXT NOT NULL,issued_at TEXT NOT NULL,expires_at TEXT NOT NULL,payload BLOB NOT NULL,UNIQUE(identity_key,input_version),UNIQUE(identity_key,input_digest));
CREATE TABLE IF NOT EXISTS model_check_execution_claims(input_id TEXT PRIMARY KEY,claim_token TEXT NOT NULL,outcome_id TEXT NOT NULL,owner_id TEXT NOT NULL,fence_token INTEGER NOT NULL,claim_until TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS model_check_outcomes(outcome_id TEXT PRIMARY KEY,input_id TEXT NOT NULL UNIQUE,input_digest TEXT NOT NULL,fence_token INTEGER NOT NULL,observed_at TEXT NOT NULL,stored_at TEXT NOT NULL,payload BLOB NOT NULL,payload_digest TEXT NOT NULL,committed INTEGER NOT NULL CHECK(committed IN (0,1)));
CREATE INDEX IF NOT EXISTS idx_model_check_outcomes_cursor ON model_check_outcomes(stored_at,outcome_id);
CREATE INDEX IF NOT EXISTS idx_model_check_inputs_target ON model_check_inputs(target_id,issued_at);`

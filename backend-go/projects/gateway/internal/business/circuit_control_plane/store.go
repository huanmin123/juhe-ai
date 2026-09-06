// Package circuitcontrolplane owns the Gateway Business circuit control-plane
// transaction group. It deliberately has no Node, IPC, queue, or HTTP dependency.
package circuitcontrolplane

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	sharedcontracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

const ProjectionKey = "account_circuit_runtime_v1"

var (
	ErrOwnerGate      = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrCAS            = errors.New("account circuit compare-and-set conflict")
	ErrIdentityReplay = errors.New("account circuit replay identity conflict")
	ErrInvalidSchema  = errors.New("circuit control-plane PostgreSQL schema is invalid")
)

type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

// OwnerGate is external, auditable handoff evidence. A schema alone never
// authorizes Gateway writes while Node remains a writer.
type OwnerGate struct{ Confirmed, SchemaReady, NodeWriterStopped bool }

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	now    func() time.Time
	token  func() (string, error)
}

type DispatchRevision struct {
	AccountID, AccountRuntimeKey, TransitionID string
	NowMS                                      int64
}

type DispatchResult struct {
	Status                                     string
	AccountID, AccountRuntimeKey, TransitionID string
	DispatchRevision                           int64
}

type DispatchRevisionSnapshot struct {
	AccountID        string
	DispatchRevision int64
}

type DispatchRevisionPage struct {
	Items              []DispatchRevisionSnapshot
	NextAfterAccountID string
}

type IncidentProjectionLoad struct {
	Status                  string
	CurrentDispatchRevision int64
	Incident                Incident
}

type Incident struct {
	CircuitScopeKey, AccountID, AccountRuntimeKey, ScopeKind string
	KeyFingerprint, ProtocolCode, RequestLane, ModelFamily   *string
	ClientModel, CapabilityHash                              *string
	CredentialSourceAccountID, ClientEndpointFamily          *string
	FinalUpstreamModel, UpstreamEndpointMode                 *string
	IncidentID, ParentIncidentID, CausedByTerminalOutcomeID  *string
	ChildIncidentIDs                                         []string
	State, FailureScope                                      string
	Generation, DispatchRevision, LedgerRevision             int64
	ProjectedLedgerRevision                                  int64
	TransitionID                                             string
	CooldownObservationGeneration                            int64
	OpenUntilMS, NextTransitionAtMS                          *int64
	LeaseID, LeasePurpose, LeaseOwnerRunID                   *string
	LeaseUntilMS, AttemptStartedAtMS, AttemptHardDeadlineMS  *int64
	UpstreamAttemptObserved                                  bool
	BackoffLevel, ConsecutiveFailures                        int64
	ConfirmationFailuresRequired                             int64
	ConfirmationFailureEvidenceKeys                          []string
	RecoveringSuccesses                                      int64
	LastFailureClass                                         *string
	RetainedUntilMS                                          *int64
	CreatedAtMS, UpdatedAtMS                                 int64
}

type IncidentMutation struct {
	Incident
	ExpectedLedgerRevision *int64 // nil means the scope must not yet exist.
}

type IncidentResult struct {
	Status                  string
	CurrentDispatchRevision int64
	Incident                *Incident
}

type Outbox struct {
	EventID, ProjectionKey, DedupeKey, EventType string
	AccountID, AccountRuntimeKey                 string
	CircuitScopeKey, IncidentID                  *string
	TransitionID                                 string
	DispatchRevision                             int64
	Generation, LedgerRevision                   *int64
	Status                                       string
	AvailableAtMS                                int64
	ClaimToken, ClaimedBy                        *string
	ClaimUntilMS                                 *int64
	AttemptCount                                 int64
	LastErrorClass                               *string
	AcknowledgedAtMS                             *int64
	CreatedAtMS, UpdatedAtMS                     int64
}

type RebuildPage struct {
	Items      []Incident
	NextCursor *IncidentCursor
}
type IncidentCursor struct {
	UpdatedAtMS     int64
	CircuitScopeKey string
}
type ProjectionGaps struct {
	Dispatch  []AccountProjectionGap
	Incidents []Incident
}
type AccountProjectionGap struct {
	AccountID                                   string
	DispatchRevision, ProjectedDispatchRevision int64
}
type CleanupResult struct{ DeletedIncidents, DeletedOutbox int64 }

const defaultBusinessSchema = "juhe_business"

const (
	maxClaimLeaseMS = int64(60 * 60_000)
	maxRetryDelayMS = int64(24 * 60 * 60_000)
	// This is a transaction/recovery window, never an artificial throughput or
	// concurrency limit. Keep it far above the former Node event-loop batch cap.
	maxBatchLimit = 100_000
	maxInt64      = int64(1<<63 - 1)
)

var postgresIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var machineCategory = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
var sha256Hex = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

func New(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Store, error) {
	if db == nil {
		return nil, errors.New("circuit control-plane database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, errors.New("circuit control-plane database mode is invalid")
	}
	schema = strings.TrimSpace(schema)
	if mode == Postgres {
		if schema == "" {
			schema = defaultBusinessSchema
		}
		if !postgresIdentifier.MatchString(schema) {
			return nil, ErrInvalidSchema
		}
	}
	return &Store{db: db, mode: mode, schema: schema, gate: gate, now: time.Now, token: randomToken}, nil
}

func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}
func (s *Store) table(name string) string {
	if s.mode == Postgres && s.schema != "" {
		// The schema is validated at construction and quoted at every use. Table
		// names are package constants, not caller input.
		return quoteIdentifier(s.schema) + "." + name
	}
	return name
}
func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
func (s *Store) forUpdate() string {
	if s.mode == Postgres {
		return " FOR UPDATE"
	}
	return ""
}
func (s *Store) forUpdateSkipLocked() string {
	if s.mode == Postgres {
		return " FOR UPDATE SKIP LOCKED"
	}
	return ""
}
func (s *Store) bind(q string) string {
	if s.mode != Postgres {
		return q
	}
	var b strings.Builder
	n := 1
	for _, r := range q {
		if r == '?' {
			fmt.Fprintf(&b, "$%d", n)
			n++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CheckContract verifies only pre-existing relations. Runtime schema creation
// would hide a maintenance/backfill omission and is intentionally forbidden.
func (s *Store) CheckContract(ctx context.Context) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	contracts := []struct {
		name string
		cols string
	}{
		{name: "accounts", cols: "id,dispatch_revision,circuit_projection_revision,deleted_at"},
		{name: "account_circuit_incidents", cols: incidentColumns},
		{name: "account_circuit_outbox", cols: outboxColumns},
	}
	for _, contract := range contracts {
		if _, err := s.db.ExecContext(ctx, "SELECT "+contract.cols+" FROM "+s.table(contract.name)+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify circuit contract %s: %w", contract.name, err)
		}
	}
	if err := s.checkKeyModelCapabilityIndex(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) checkKeyModelCapabilityIndex(ctx context.Context) error {
	spec, exists := sharedcontracts.BusinessSQLiteSchema["account_circuit_incidents"]
	const indexName = "idx_account_circuit_incidents_key_model_capability"
	if !exists || len(spec.IndexDefinitions) == 0 {
		return fmt.Errorf("verify circuit key_model capability index: shared contract definition is missing")
	}
	var required sharedcontracts.SQLiteIndexDefinition
	foundDefinition := false
	for _, definition := range spec.IndexDefinitions {
		if definition.Name != indexName {
			continue
		}
		if foundDefinition {
			return fmt.Errorf("verify circuit key_model capability index: shared contract definition is duplicated")
		}
		required = definition
		foundDefinition = true
	}
	if !foundDefinition {
		return fmt.Errorf("verify circuit key_model capability index %s: shared contract definition is missing", indexName)
	}
	if s.mode == SQLite {
		ok, detail, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required)
		if err != nil {
			return fmt.Errorf("verify circuit key_model capability index: %w", err)
		}
		if !ok {
			return fmt.Errorf("verify circuit key_model capability index %s is incompatible: %s", required.Name, detail)
		}
		return nil
	}
	var unique bool
	var predicate sql.NullString
	var columns sql.NullString
	query := `SELECT i.indisunique, pg_get_expr(i.indpred,i.indrelid), array_to_string(ARRAY(SELECT a.attname FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum,ord) JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum ORDER BY k.ord), ',') FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indexrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_class target ON target.oid=i.indrelid JOIN pg_catalog.pg_namespace target_ns ON target_ns.oid=target.relnamespace WHERE n.nspname=? AND c.relname=? AND target_ns.nspname=? AND target.relname=? AND i.indisvalid AND i.indisready AND i.indnkeyatts=? AND i.indnatts=i.indnkeyatts`
	err := s.db.QueryRowContext(ctx, s.bind(query), s.schema, required.Name, s.schema, "account_circuit_incidents", len(required.Columns)).Scan(&unique, &predicate, &columns)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("verify circuit key_model capability index %s is missing", required.Name)
	}
	if err != nil {
		return fmt.Errorf("verify circuit key_model capability index: %w", err)
	}
	actualColumns := strings.Split(columns.String, ",")
	if !unique || !sameIndexColumns(actualColumns, required.Columns) || !predicatesEquivalent(predicate.String, required.Predicate) {
		return fmt.Errorf("verify circuit key_model capability index %s is incompatible: unique=%t columns=%v predicate=%q", required.Name, unique, actualColumns, predicate.String)
	}
	return nil
}

func checkSQLiteIndexDefinition(ctx context.Context, db *sql.DB, table string, required sharedcontracts.SQLiteIndexDefinition) (bool, string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA index_list("+quoteIdentifier(table)+")")
	if err != nil {
		return false, "read index_list failed", err
	}
	defer rows.Close()
	var found, unique, partial bool
	for rows.Next() {
		var seq, u, p int
		var name, origin string
		if err := rows.Scan(&seq, &name, &u, &origin, &p); err != nil {
			return false, "scan index_list failed", err
		}
		if name == required.Name {
			found, unique, partial = true, u == 1, p == 1
			break
		}
	}
	if err := rows.Err(); err != nil {
		return false, "read index_list failed", err
	}
	if err := rows.Close(); err != nil {
		return false, "close index_list failed", err
	}
	if !found {
		return false, "index is missing", nil
	}
	if unique != required.Unique {
		return false, fmt.Sprintf("unique=%t want=%t", unique, required.Unique), nil
	}
	colRows, err := db.QueryContext(ctx, "PRAGMA index_info("+quoteIdentifier(required.Name)+")")
	if err != nil {
		return false, "read index_info failed", err
	}
	defer colRows.Close()
	columns := []string{}
	for colRows.Next() {
		var seq, cid int
		var name sql.NullString
		if err := colRows.Scan(&seq, &cid, &name); err != nil {
			return false, "scan index_info failed", err
		}
		if !name.Valid {
			return false, "index contains an expression", nil
		}
		columns = append(columns, name.String)
	}
	if err := colRows.Err(); err != nil {
		return false, "read index_info failed", err
	}
	if err := colRows.Close(); err != nil {
		return false, "close index_info failed", err
	}
	if !sameIndexColumns(columns, required.Columns) {
		return false, fmt.Sprintf("columns=%v want=%v", columns, required.Columns), nil
	}
	var definition sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='index' AND name=?", required.Name).Scan(&definition); err != nil {
		return false, "read sqlite_master definition failed", err
	}
	actualPredicate := indexPredicate(definition.String)
	if strings.TrimSpace(required.Predicate) == "" {
		if partial || actualPredicate != "" {
			return false, "unexpected partial predicate", nil
		}
	} else if !partial || !predicatesEquivalent(actualPredicate, required.Predicate) {
		return false, fmt.Sprintf("predicate=%q want=%q", actualPredicate, required.Predicate), nil
	}
	return true, "", nil
}

func sameIndexColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func indexPredicate(sqlText string) string {
	lower := strings.ToLower(sqlText)
	idx := strings.Index(lower, " where ")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(sqlText[idx+7:])
}
func predicatesEquivalent(actual, expected string) bool {
	norm := func(v string) []string {
		v = strings.ToLower(strings.TrimSpace(v))
		v = strings.NewReplacer("`", "", "[", "", "]", "", `"`, "").Replace(v)
		v = regexp.MustCompile(`::[a-z0-9_]+`).ReplaceAllString(v, "")
		v = strings.ReplaceAll(strings.ReplaceAll(v, "(", ""), ")", "")
		v = strings.Join(strings.Fields(v), " ")
		for strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
			v = strings.TrimSpace(v[1 : len(v)-1])
		}
		parts := strings.Split(v, " and ")
		for i := range parts {
			parts[i] = strings.Join(strings.Fields(parts[i]), " ")
		}
		sort.Strings(parts)
		return parts
	}
	return sameIndexColumns(norm(actual), norm(expected))
}

func (s *Store) AdvanceDispatchRevision(ctx context.Context, in DispatchRevision) (DispatchResult, error) {
	if err := s.requireOwner(); err != nil {
		return DispatchResult{}, err
	}
	in.AccountID = strings.TrimSpace(in.AccountID)
	in.AccountRuntimeKey = strings.TrimSpace(in.AccountRuntimeKey)
	in.TransitionID = strings.TrimSpace(in.TransitionID)
	if err := requireTextBounded(in.AccountID, 256, "account id"); err != nil {
		return DispatchResult{}, err
	}
	if err := validateRuntimeKey(in.AccountRuntimeKey); err != nil {
		return DispatchResult{}, err
	}
	if err := requireTextBounded(in.TransitionID, 256, "transition id"); err != nil {
		return DispatchResult{}, err
	}
	if err := requireNonNegativeMS(in.NowMS, "nowMs"); err != nil {
		return DispatchResult{}, err
	}
	now := in.NowMS
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DispatchResult{}, err
	}
	defer tx.Rollback()
	dedupe := "dispatch:" + in.TransitionID
	if replay, found, err := s.outboxByDedupe(ctx, tx, dedupe); err != nil {
		return DispatchResult{}, err
	} else if found {
		if replay.EventType != "dispatch_revision_changed" || replay.AccountID != in.AccountID || replay.AccountRuntimeKey != in.AccountRuntimeKey {
			return DispatchResult{}, ErrIdentityReplay
		}
		if err := tx.Commit(); err != nil {
			return DispatchResult{}, err
		}
		return DispatchResult{Status: "idempotent", AccountID: replay.AccountID, AccountRuntimeKey: replay.AccountRuntimeKey, TransitionID: replay.TransitionID, DispatchRevision: replay.DispatchRevision}, nil
	}
	var revision int64
	err = tx.QueryRowContext(ctx, s.bind("SELECT dispatch_revision FROM "+s.table("accounts")+" WHERE id=?"+s.forUpdate()), in.AccountID).Scan(&revision)
	if err != nil {
		return DispatchResult{}, err
	}
	res, err := tx.ExecContext(ctx, s.bind("UPDATE "+s.table("accounts")+" SET dispatch_revision=dispatch_revision+1 WHERE id=? AND dispatch_revision=?"), in.AccountID, revision)
	if err != nil {
		return DispatchResult{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return DispatchResult{}, ErrCAS
	}
	revision++
	eventID, err := s.token()
	if err != nil {
		return DispatchResult{}, err
	}
	if err = s.insertOutbox(ctx, tx, Outbox{EventID: eventID, ProjectionKey: ProjectionKey, DedupeKey: dedupe, EventType: "dispatch_revision_changed", AccountID: in.AccountID, AccountRuntimeKey: in.AccountRuntimeKey, TransitionID: in.TransitionID, DispatchRevision: revision, Status: "pending", AvailableAtMS: now, CreatedAtMS: now, UpdatedAtMS: now}); err != nil {
		return DispatchResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return DispatchResult{}, err
	}
	return DispatchResult{Status: "applied", AccountID: in.AccountID, AccountRuntimeKey: in.AccountRuntimeKey, TransitionID: in.TransitionID, DispatchRevision: revision}, nil
}

// ListDispatchRevisions is the read side used by the in-process Redis runtime
// index backfill. It reads the current Business revision fence; it never
// creates schema or writes Redis/Node state.
func (s *Store) ListDispatchRevisions(ctx context.Context, afterAccountID string, limit int) (DispatchRevisionPage, error) {
	if err := s.requireOwner(); err != nil {
		return DispatchRevisionPage{}, err
	}
	afterAccountID = strings.TrimSpace(afterAccountID)
	if afterAccountID != "" {
		if err := requireTextBounded(afterAccountID, 256, "afterAccountId"); err != nil {
			return DispatchRevisionPage{}, err
		}
	}
	if limit <= 0 || limit > maxBatchLimit {
		return DispatchRevisionPage{}, errors.New("dispatch revision page limit is outside the allowed bounds")
	}
	rows, err := s.db.QueryContext(ctx, s.bind("SELECT id,dispatch_revision FROM "+s.table("accounts")+" WHERE id>? ORDER BY id LIMIT ?"), afterAccountID, limit)
	if err != nil {
		return DispatchRevisionPage{}, err
	}
	defer rows.Close()
	page := DispatchRevisionPage{}
	for rows.Next() {
		var item DispatchRevisionSnapshot
		if err := rows.Scan(&item.AccountID, &item.DispatchRevision); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) == limit {
		page.NextAfterAccountID = page.Items[len(page.Items)-1].AccountID
	}
	return page, nil
}

// LoadIncidentForProjection returns the durable incident snapshot fenced to the
// account's current dispatch revision. It is consumed by the in-process Redis
// projector; it never calls Node or another process.
func (s *Store) LoadIncidentForProjection(ctx context.Context, event Outbox) (IncidentProjectionLoad, error) {
	if err := s.requireOwner(); err != nil {
		return IncidentProjectionLoad{}, err
	}
	if event.AccountID == "" || event.CircuitScopeKey == nil || *event.CircuitScopeKey == "" || event.IncidentID == nil || *event.IncidentID == "" {
		return IncidentProjectionLoad{}, errors.New("incident projection event identity is invalid")
	}
	var currentRevision int64
	if err := s.db.QueryRowContext(ctx, s.bind("SELECT dispatch_revision FROM "+s.table("accounts")+" WHERE id=?"), event.AccountID).Scan(&currentRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IncidentProjectionLoad{Status: "missing"}, nil
		}
		return IncidentProjectionLoad{}, err
	}
	if currentRevision != event.DispatchRevision {
		return IncidentProjectionLoad{Status: "stale", CurrentDispatchRevision: currentRevision}, nil
	}
	incident, found, err := s.incidentByScope(ctx, s.db, *event.CircuitScopeKey)
	if err != nil {
		return IncidentProjectionLoad{}, err
	}
	if !found || incident.IncidentID == nil || *incident.IncidentID != *event.IncidentID || incident.DispatchRevision != currentRevision || (event.Generation != nil && incident.Generation != *event.Generation) || (event.LedgerRevision != nil && incident.LedgerRevision < *event.LedgerRevision) {
		return IncidentProjectionLoad{Status: "missing", CurrentDispatchRevision: currentRevision}, nil
	}
	return IncidentProjectionLoad{Status: "current", CurrentDispatchRevision: currentRevision, Incident: incident}, nil
}

func (s *Store) CompareAndSetIncident(ctx context.Context, in IncidentMutation) (IncidentResult, error) {
	if err := s.requireOwner(); err != nil {
		return IncidentResult{}, err
	}
	if in.ChildIncidentIDs == nil {
		in.ChildIncidentIDs = []string{}
	}
	if in.ConfirmationFailureEvidenceKeys == nil {
		in.ConfirmationFailureEvidenceKeys = []string{}
	}
	if in.ExpectedLedgerRevision != nil && *in.ExpectedLedgerRevision < 0 {
		return IncidentResult{}, errors.New("expected ledger revision cannot be negative")
	}
	if err := validateIncident(&in.Incident); err != nil {
		return IncidentResult{}, err
	}
	if err := requireNonNegativeMS(in.UpdatedAtMS, "updatedAtMs"); err != nil {
		return IncidentResult{}, err
	}
	if err := validateIncidentTimes(&in.Incident, in.UpdatedAtMS); err != nil {
		return IncidentResult{}, err
	}
	now := in.UpdatedAtMS
	in.UpdatedAtMS = now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IncidentResult{}, err
	}
	defer tx.Rollback()
	// Node hotfix（migration-backup/node/final-archive/backend/src/storage/
	// account-circuit-control-plane.repository.ts compareAndSetAccountCircuitIncidentInClient）：
	// 物理清理会在逻辑删除后级联 circuit ledger，锁行 SELECT 需补 deleted_at
	// 列；账户行缺失或已逻辑删除时，迟到的运行态观察必须落为 account_not_found
	// 终态而不是错误或 stale 重试。jobs 侧同键读面见
	// backend-go/projects/jobs/internal/circuitstore/controlplane.go（跨 module
	// 不可 import，同键同语义注释互指）。
	var currentDispatch int64
	var accountDeletedAt sql.NullString
	err = tx.QueryRowContext(ctx, s.bind("SELECT dispatch_revision, deleted_at FROM "+s.table("accounts")+" WHERE id=?"+s.forUpdate()), in.AccountID).Scan(&currentDispatch, &accountDeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return IncidentResult{Status: "account_not_found", CurrentDispatchRevision: 0}, nil
	}
	if err != nil {
		return IncidentResult{}, err
	}
	if accountDeletedAt.Valid {
		_ = tx.Rollback()
		return IncidentResult{Status: "account_not_found", CurrentDispatchRevision: currentDispatch}, nil
	}
	if currentDispatch != in.DispatchRevision {
		_ = tx.Rollback()
		return IncidentResult{Status: "stale_dispatch_revision", CurrentDispatchRevision: currentDispatch}, nil
	}
	dedupe := "incident:" + in.TransitionID
	if replay, found, err := s.outboxByDedupe(ctx, tx, dedupe); err != nil {
		return IncidentResult{}, err
	} else if found {
		if replay.EventType != "incident_changed" || replay.AccountID != in.AccountID || replay.AccountRuntimeKey != in.AccountRuntimeKey || value(replay.CircuitScopeKey) != in.CircuitScopeKey {
			return IncidentResult{}, ErrIdentityReplay
		}
		incident, found, err := s.incidentByScope(ctx, tx, in.CircuitScopeKey)
		if err != nil {
			return IncidentResult{}, err
		}
		if !found {
			return IncidentResult{}, errors.New("deduplicated incident receipt has no incident")
		}
		if err = tx.Commit(); err != nil {
			return IncidentResult{}, err
		}
		return IncidentResult{Status: "idempotent", CurrentDispatchRevision: currentDispatch, Incident: &incident}, nil
	}
	// The incident row is part of the CAS critical section. PostgreSQL must
	// lock an existing row before comparing/upserting it; otherwise concurrent
	// writers can both observe the same ledger revision and overwrite one
	// another despite the account row lock above.
	current, found, err := s.incidentByScopeForUpdate(ctx, tx, in.CircuitScopeKey)
	if err != nil {
		return IncidentResult{}, err
	}
	if (in.ExpectedLedgerRevision == nil && found) || (in.ExpectedLedgerRevision != nil && (!found || current.LedgerRevision != *in.ExpectedLedgerRevision)) {
		_ = tx.Rollback()
		return IncidentResult{Status: "cas_conflict", CurrentDispatchRevision: currentDispatch, Incident: optionalIncident(current, found)}, nil
	}
	if found && current.AccountID != in.AccountID {
		return IncidentResult{}, errors.New("circuit scope key belongs to another account")
	}
	if found && current.Generation > in.Generation {
		_ = tx.Rollback()
		return IncidentResult{Status: "cas_conflict", CurrentDispatchRevision: currentDispatch, Incident: &current}, nil
	}
	if found {
		in.ProjectedLedgerRevision = current.ProjectedLedgerRevision
		in.CreatedAtMS = current.CreatedAtMS
		if err = validateIncidentTimes(&in.Incident, now); err != nil {
			return IncidentResult{}, err
		}
	} else {
		in.ProjectedLedgerRevision = 0
	}
	in.LedgerRevision = 1
	if found {
		in.LedgerRevision = current.LedgerRevision + 1
	}
	if err = s.upsertIncident(ctx, tx, in.Incident); err != nil {
		return IncidentResult{}, err
	}
	eventID, err := s.token()
	if err != nil {
		return IncidentResult{}, err
	}
	if err = s.insertOutbox(ctx, tx, Outbox{EventID: eventID, ProjectionKey: ProjectionKey, DedupeKey: dedupe, EventType: "incident_changed", AccountID: in.AccountID, AccountRuntimeKey: in.AccountRuntimeKey, CircuitScopeKey: ptr(in.CircuitScopeKey), IncidentID: ptr(value(in.IncidentID)), TransitionID: in.TransitionID, DispatchRevision: in.DispatchRevision, Generation: ptr(in.Generation), LedgerRevision: ptr(in.LedgerRevision), Status: "pending", AvailableAtMS: now, CreatedAtMS: now, UpdatedAtMS: now}); err != nil {
		return IncidentResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return IncidentResult{}, err
	}
	return IncidentResult{Status: "applied", CurrentDispatchRevision: currentDispatch, Incident: &in.Incident}, nil
}

func (s *Store) GetIncident(ctx context.Context, scopeKey string) (Incident, bool, error) {
	if err := s.requireOwner(); err != nil {
		return Incident{}, false, err
	}
	scopeKey = strings.TrimSpace(scopeKey)
	if err := requireTextBounded(scopeKey, 2048, "circuit scope key"); err != nil {
		return Incident{}, false, err
	}
	return s.incidentByScope(ctx, s.db, scopeKey)
}

func (s *Store) ClaimOutbox(ctx context.Context, owner string, nowMS, leaseMS int64, limit int) ([]Outbox, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	owner = strings.TrimSpace(owner)
	if err := requireTextBounded(owner, 128, "claim owner"); err != nil {
		return nil, err
	}
	if leaseMS <= 0 || leaseMS > maxClaimLeaseMS || limit <= 0 || limit > maxBatchLimit {
		return nil, errors.New("claim lease and limit are outside the allowed bounds")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireNonNegativeMS(nowMS, "nowMs"); err != nil {
		return nil, err
	}
	now := nowMS
	if leaseMS > 0 && now > maxInt64-leaseMS {
		return nil, errors.New("claim lease exceeds timestamp bounds")
	}
	q := "SELECT " + outboxColumns + " FROM " + s.table("account_circuit_outbox") + " WHERE (status='pending' AND available_at_ms<=?) OR (status='processing' AND claim_until_ms<=?) ORDER BY available_at_ms,created_at_ms,event_id LIMIT ?" + s.forUpdateSkipLocked()
	rows, err := tx.QueryContext(ctx, s.bind(q), now, now, limit)
	if err != nil {
		return nil, err
	}
	var candidates []Outbox
	for rows.Next() {
		v, e := scanOutbox(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		candidates = append(candidates, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	claimed := make([]Outbox, 0, len(candidates))
	for _, candidate := range candidates {
		token, e := s.token()
		if e != nil {
			return nil, e
		}
		until := now + leaseMS
		res, e := tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_circuit_outbox")+" SET status='processing',claim_token=?,claimed_by=?,claim_until_ms=?,attempt_count=attempt_count+1,updated_at_ms=? WHERE event_id=? AND ((status='pending' AND available_at_ms<=?) OR (status='processing' AND claim_until_ms<=?))"), token, owner, until, now, candidate.EventID, now, now)
		if e != nil {
			return nil, e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		candidate.Status = "processing"
		candidate.ClaimToken = &token
		candidate.ClaimedBy = &owner
		candidate.ClaimUntilMS = &until
		candidate.AttemptCount++
		candidate.UpdatedAtMS = now
		claimed = append(claimed, candidate)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *Store) AcknowledgeOutbox(ctx context.Context, eventID, projectionKey, claimToken string, acknowledgedAtMS int64) (bool, error) {
	if err := s.requireOwner(); err != nil {
		return false, err
	}
	eventID = strings.TrimSpace(eventID)
	projectionKey = strings.TrimSpace(projectionKey)
	claimToken = strings.TrimSpace(claimToken)
	if err := requireTextBounded(eventID, 256, "event id"); err != nil {
		return false, err
	}
	if projectionKey != ProjectionKey {
		return false, nil
	}
	if err := requireTextBounded(claimToken, 256, "claim token"); err != nil {
		return false, err
	}
	if err := requireNonNegativeMS(acknowledgedAtMS, "acknowledgedAtMs"); err != nil {
		return false, err
	}
	now := acknowledgedAtMS
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	event, found, err := s.outboxByID(ctx, tx, eventID)
	if err != nil {
		return false, err
	}
	if !found || event.ProjectionKey != projectionKey {
		return false, nil
	}
	if event.Status == "dispatched" {
		return true, tx.Commit()
	}
	if event.Status != "processing" || value(event.ClaimToken) != claimToken {
		return false, nil
	}
	res, err := tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_circuit_outbox")+" SET status='dispatched',claim_token=NULL,claimed_by=NULL,claim_until_ms=NULL,acknowledged_at_ms=?,last_error_class=NULL,updated_at_ms=? WHERE event_id=? AND status='processing' AND claim_token=? AND projection_key=?"), now, now, eventID, claimToken, projectionKey)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if event.EventType == "dispatch_revision_changed" {
		_, err = tx.ExecContext(ctx, s.bind("UPDATE "+s.table("accounts")+" SET circuit_projection_revision=CASE WHEN circuit_projection_revision<? THEN ? ELSE circuit_projection_revision END WHERE id=? AND dispatch_revision>=?"), event.DispatchRevision, event.DispatchRevision, event.AccountID, event.DispatchRevision)
	} else if event.CircuitScopeKey != nil && event.IncidentID != nil && event.LedgerRevision != nil {
		_, err = tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_circuit_incidents")+" SET projected_ledger_revision=CASE WHEN projected_ledger_revision<? THEN ? ELSE projected_ledger_revision END WHERE circuit_scope_key=? AND incident_id=? AND ledger_revision>=?"), *event.LedgerRevision, *event.LedgerRevision, *event.CircuitScopeKey, *event.IncidentID, *event.LedgerRevision)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) ReleaseOutboxForReplay(ctx context.Context, eventID, claimToken, errorClass string, nowMS, delayMS int64) (bool, error) {
	if err := s.requireOwner(); err != nil {
		return false, err
	}
	eventID = strings.TrimSpace(eventID)
	claimToken = strings.TrimSpace(claimToken)
	errorClass = strings.TrimSpace(errorClass)
	if err := requireTextBounded(eventID, 256, "event id"); err != nil {
		return false, err
	}
	if err := requireTextBounded(claimToken, 256, "claim token"); err != nil {
		return false, err
	}
	if err := requireText(errorClass, "error class"); err != nil {
		return false, err
	}
	var normalizeErr error
	errorClass, normalizeErr = normalizeErrorClass(errorClass)
	if normalizeErr != nil {
		return false, normalizeErr
	}
	if delayMS < 0 || delayMS > maxRetryDelayMS {
		return false, errors.New("retry delay is outside the allowed bounds")
	}
	if err := requireNonNegativeMS(nowMS, "nowMs"); err != nil {
		return false, err
	}
	if nowMS > maxInt64-delayMS {
		return false, errors.New("retry delay exceeds timestamp bounds")
	}
	now := nowMS
	res, err := s.db.ExecContext(ctx, s.bind("UPDATE "+s.table("account_circuit_outbox")+" SET status='pending',available_at_ms=?,claim_token=NULL,claimed_by=NULL,claim_until_ms=NULL,last_error_class=?,updated_at_ms=? WHERE event_id=? AND status='processing' AND claim_token=?"), now+delayMS, errorClass, now, eventID, claimToken)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) ListForRebuild(ctx context.Context, nowMS, afterUpdatedMS int64, afterScope string, limit int) (RebuildPage, error) {
	if err := s.requireOwner(); err != nil {
		return RebuildPage{}, err
	}
	afterScope = strings.TrimSpace(afterScope)
	if afterScope != "" {
		if err := requireTextBounded(afterScope, 2048, "afterScope"); err != nil {
			return RebuildPage{}, err
		}
	}
	if limit <= 0 || limit > maxBatchLimit {
		return RebuildPage{}, errors.New("rebuild limit is outside the allowed bounds")
	}
	if err := requireNonNegativeMS(nowMS, "nowMs"); err != nil {
		return RebuildPage{}, err
	}
	if err := requireNonNegativeMS(afterUpdatedMS, "afterUpdatedMs"); err != nil {
		return RebuildPage{}, err
	}
	now := nowMS
	q := "SELECT " + incidentColumns + " FROM " + s.table("account_circuit_incidents") + " i WHERE (i.state<>'CLOSED' OR i.retained_until_ms>?) AND i.dispatch_revision=(SELECT a.dispatch_revision FROM " + s.table("accounts") + " a WHERE a.id=i.account_id) AND (i.updated_at_ms>? OR (i.updated_at_ms=? AND i.circuit_scope_key>?)) ORDER BY i.updated_at_ms,i.circuit_scope_key LIMIT ?"
	page, err := s.listIncidents(ctx, q, now, afterUpdatedMS, afterUpdatedMS, afterScope, limit)
	if err != nil {
		return RebuildPage{}, err
	}
	if len(page.Items) < limit {
		page.NextCursor = nil
	}
	return page, nil
}

func (s *Store) ListByRuntimeKeys(ctx context.Context, keys []string, includeRetainedClosed bool, nowMS int64) ([]Incident, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	var err error
	keys, err = uniqueKeys(keys)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []Incident{}, nil
	}
	if len(keys) > 100 {
		return nil, errors.New("at most 100 runtime keys")
	}
	if err := requireNonNegativeMS(nowMS, "nowMs"); err != nil {
		return nil, err
	}
	now := nowMS
	ph := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	condition := "i.state<>'CLOSED'"
	args := make([]any, 0, len(keys)+1)
	for _, k := range keys {
		args = append(args, k)
	}
	if includeRetainedClosed {
		condition = "(i.state<>'CLOSED' OR i.retained_until_ms>?)"
		args = append(args, now)
	}
	q := "SELECT " + incidentColumns + " FROM " + s.table("account_circuit_incidents") + " i WHERE i.account_runtime_key IN (" + ph + ") AND " + condition + " AND i.dispatch_revision=(SELECT a.dispatch_revision FROM " + s.table("accounts") + " a WHERE a.id=i.account_id) ORDER BY i.account_runtime_key,i.updated_at_ms,i.circuit_scope_key"
	rows, err := s.db.QueryContext(ctx, s.bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readIncidents(rows)
}

func (s *Store) ListProjectionGaps(ctx context.Context, afterAccountID string, afterUpdatedMS int64, afterScope string, limit int) (ProjectionGaps, error) {
	if err := s.requireOwner(); err != nil {
		return ProjectionGaps{}, err
	}
	afterAccountID = strings.TrimSpace(afterAccountID)
	afterScope = strings.TrimSpace(afterScope)
	if afterAccountID != "" {
		if err := requireTextBounded(afterAccountID, 256, "afterAccountId"); err != nil {
			return ProjectionGaps{}, err
		}
	}
	if afterScope != "" {
		if err := requireTextBounded(afterScope, 2048, "afterScope"); err != nil {
			return ProjectionGaps{}, err
		}
	}
	if limit <= 0 || limit > maxBatchLimit {
		return ProjectionGaps{}, errors.New("projection gap limit is outside the allowed bounds")
	}
	if err := requireNonNegativeMS(afterUpdatedMS, "afterUpdatedMs"); err != nil {
		return ProjectionGaps{}, err
	}
	accounts := s.table("accounts")
	incidents := s.table("account_circuit_incidents")
	rows, err := s.db.QueryContext(ctx, s.bind("SELECT id,dispatch_revision,circuit_projection_revision FROM "+accounts+" WHERE circuit_projection_revision<dispatch_revision AND id>? ORDER BY id LIMIT ?"), afterAccountID, limit)
	if err != nil {
		return ProjectionGaps{}, err
	}
	gaps := ProjectionGaps{}
	for rows.Next() {
		var v AccountProjectionGap
		if err = rows.Scan(&v.AccountID, &v.DispatchRevision, &v.ProjectedDispatchRevision); err != nil {
			rows.Close()
			return gaps, err
		}
		gaps.Dispatch = append(gaps.Dispatch, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return gaps, err
	}
	if err = rows.Close(); err != nil {
		return gaps, err
	}
	q := "SELECT " + incidentColumns + " FROM " + incidents + " i WHERE i.projected_ledger_revision<i.ledger_revision AND i.dispatch_revision=(SELECT a.dispatch_revision FROM " + accounts + " a WHERE a.id=i.account_id) AND (i.updated_at_ms>? OR (i.updated_at_ms=? AND i.circuit_scope_key>?)) ORDER BY i.updated_at_ms,i.circuit_scope_key LIMIT ?"
	page, err := s.listIncidents(ctx, q, afterUpdatedMS, afterUpdatedMS, afterScope, limit)
	if err != nil {
		return gaps, err
	}
	gaps.Incidents = page.Items
	return gaps, nil
}

func (s *Store) Cleanup(ctx context.Context, nowMS, acknowledgedBeforeMS int64, limit int) (CleanupResult, error) {
	if err := s.requireOwner(); err != nil {
		return CleanupResult{}, err
	}
	if limit <= 0 || limit > maxBatchLimit {
		return CleanupResult{}, errors.New("cleanup limit is outside the allowed bounds")
	}
	if err := requireNonNegativeMS(nowMS, "nowMs"); err != nil {
		return CleanupResult{}, err
	}
	if err := requireNonNegativeMS(acknowledgedBeforeMS, "acknowledgedBeforeMs"); err != nil {
		return CleanupResult{}, err
	}
	now := nowMS
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CleanupResult{}, err
	}
	defer tx.Rollback()
	out := CleanupResult{}
	if out.DeletedOutbox, err = s.deleteLimited(ctx, tx, s.table("account_circuit_outbox"), "event_id", "status='dispatched' AND acknowledged_at_ms<=?", "acknowledged_at_ms,event_id", acknowledgedBeforeMS, limit); err != nil {
		return out, err
	}
	condition := "state='CLOSED' AND retained_until_ms<=? AND projected_ledger_revision>=ledger_revision AND NOT EXISTS (SELECT 1 FROM " + s.table("account_circuit_outbox") + " o WHERE o.circuit_scope_key=" + s.table("account_circuit_incidents") + ".circuit_scope_key AND o.status<>'dispatched')"
	if out.DeletedIncidents, err = s.deleteLimited(ctx, tx, s.table("account_circuit_incidents"), "circuit_scope_key", condition, "retained_until_ms,updated_at_ms,circuit_scope_key", now, limit); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type rowQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) incidentByScope(ctx context.Context, q queryer, scope string) (Incident, bool, error) {
	row := q.QueryRowContext(ctx, s.bind("SELECT "+incidentColumns+" FROM "+s.table("account_circuit_incidents")+" WHERE circuit_scope_key=?"), scope)
	v, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Incident{}, false, nil
	}
	return v, err == nil, err
}
func (s *Store) incidentByScopeForUpdate(ctx context.Context, q queryer, scope string) (Incident, bool, error) {
	row := q.QueryRowContext(ctx, s.bind("SELECT "+incidentColumns+" FROM "+s.table("account_circuit_incidents")+" WHERE circuit_scope_key=?"+s.forUpdate()), scope)
	v, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Incident{}, false, nil
	}
	return v, err == nil, err
}
func (s *Store) outboxByDedupe(ctx context.Context, q queryer, dedupe string) (Outbox, bool, error) {
	row := q.QueryRowContext(ctx, s.bind("SELECT "+outboxColumns+" FROM "+s.table("account_circuit_outbox")+" WHERE projection_key=? AND dedupe_key=?"), ProjectionKey, dedupe)
	v, err := scanOutbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Outbox{}, false, nil
	}
	return v, err == nil, err
}
func (s *Store) outboxByID(ctx context.Context, q queryer, id string) (Outbox, bool, error) {
	row := q.QueryRowContext(ctx, s.bind("SELECT "+outboxColumns+" FROM "+s.table("account_circuit_outbox")+" WHERE event_id=?"), id)
	v, err := scanOutbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Outbox{}, false, nil
	}
	return v, err == nil, err
}
func (s *Store) listIncidents(ctx context.Context, q string, args ...any) (RebuildPage, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(q), args...)
	if err != nil {
		return RebuildPage{}, err
	}
	defer rows.Close()
	items, err := readIncidents(rows)
	if err != nil {
		return RebuildPage{}, err
	}
	out := RebuildPage{Items: items}
	if len(items) > 0 {
		last := items[len(items)-1]
		out.NextCursor = &IncidentCursor{UpdatedAtMS: last.UpdatedAtMS, CircuitScopeKey: last.CircuitScopeKey}
	}
	return out, nil
}

func (s *Store) insertOutbox(ctx context.Context, tx *sql.Tx, v Outbox) error {
	q := "INSERT INTO " + s.table("account_circuit_outbox") + " (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,transition_id,dispatch_revision,generation,ledger_revision,status,available_at_ms,attempt_count,created_at_ms,updated_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
	_, err := tx.ExecContext(ctx, s.bind(q), v.EventID, v.ProjectionKey, v.DedupeKey, v.EventType, v.AccountID, v.AccountRuntimeKey, v.CircuitScopeKey, v.IncidentID, v.TransitionID, v.DispatchRevision, v.Generation, v.LedgerRevision, "pending", v.AvailableAtMS, 0, v.CreatedAtMS, v.UpdatedAtMS)
	return err
}
func (s *Store) upsertIncident(ctx context.Context, tx *sql.Tx, v Incident) error {
	if v.ChildIncidentIDs == nil {
		v.ChildIncidentIDs = []string{}
	}
	if v.ConfirmationFailureEvidenceKeys == nil {
		v.ConfirmationFailureEvidenceKeys = []string{}
	}
	children, err := json.Marshal(v.ChildIncidentIDs)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(v.ConfirmationFailureEvidenceKeys)
	if err != nil {
		return err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", strings.Count(incidentColumns, ",")+1), ",")
	q := "INSERT INTO " + s.table("account_circuit_incidents") + " (" + incidentColumns + ") VALUES (" + placeholders + ") ON CONFLICT(circuit_scope_key) DO UPDATE SET account_id=excluded.account_id,account_runtime_key=excluded.account_runtime_key,scope_kind=excluded.scope_kind,key_fingerprint=excluded.key_fingerprint,protocol_code=excluded.protocol_code,request_lane=excluded.request_lane,model_family=excluded.model_family,client_model=excluded.client_model,capability_hash=excluded.capability_hash,credential_source_account_id=excluded.credential_source_account_id,client_endpoint_family=excluded.client_endpoint_family,final_upstream_model=excluded.final_upstream_model,upstream_endpoint_mode=excluded.upstream_endpoint_mode,incident_id=excluded.incident_id,parent_incident_id=excluded.parent_incident_id,child_incident_ids_json=excluded.child_incident_ids_json,caused_by_terminal_outcome_id=excluded.caused_by_terminal_outcome_id,state=excluded.state,failure_scope=excluded.failure_scope,generation=excluded.generation,dispatch_revision=excluded.dispatch_revision,ledger_revision=excluded.ledger_revision,projected_ledger_revision=excluded.projected_ledger_revision,transition_id=excluded.transition_id,cooldown_observation_generation=excluded.cooldown_observation_generation,open_until_ms=excluded.open_until_ms,next_transition_at_ms=excluded.next_transition_at_ms,lease_id=excluded.lease_id,lease_purpose=excluded.lease_purpose,lease_owner_run_id=excluded.lease_owner_run_id,lease_until_ms=excluded.lease_until_ms,attempt_started_at_ms=excluded.attempt_started_at_ms,attempt_hard_deadline_ms=excluded.attempt_hard_deadline_ms,upstream_attempt_observed=excluded.upstream_attempt_observed,backoff_level=excluded.backoff_level,consecutive_failures=excluded.consecutive_failures,confirmation_failures_required=excluded.confirmation_failures_required,confirmation_failure_evidence_keys_json=excluded.confirmation_failure_evidence_keys_json,recovering_successes=excluded.recovering_successes,last_failure_class=excluded.last_failure_class,retained_until_ms=excluded.retained_until_ms,updated_at_ms=excluded.updated_at_ms WHERE account_id=excluded.account_id"
	res, err := tx.ExecContext(ctx, s.bind(q), v.CircuitScopeKey, v.AccountID, v.AccountRuntimeKey, v.ScopeKind, v.KeyFingerprint, v.ProtocolCode, v.RequestLane, v.ModelFamily, v.ClientModel, v.CapabilityHash, v.CredentialSourceAccountID, v.ClientEndpointFamily, v.FinalUpstreamModel, v.UpstreamEndpointMode, v.IncidentID, v.ParentIncidentID, string(children), v.CausedByTerminalOutcomeID, v.State, nullableText(v.FailureScope), v.Generation, v.DispatchRevision, v.LedgerRevision, v.ProjectedLedgerRevision, v.TransitionID, v.CooldownObservationGeneration, v.OpenUntilMS, v.NextTransitionAtMS, v.LeaseID, v.LeasePurpose, v.LeaseOwnerRunID, v.LeaseUntilMS, v.AttemptStartedAtMS, v.AttemptHardDeadlineMS, boolInt(v.UpstreamAttemptObserved), v.BackoffLevel, v.ConsecutiveFailures, v.ConfirmationFailuresRequired, string(evidence), v.RecoveringSuccesses, v.LastFailureClass, v.RetainedUntilMS, v.CreatedAtMS, v.UpdatedAtMS)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrCAS
	}
	return nil
}
func (s *Store) deleteLimited(ctx context.Context, tx *sql.Tx, table, id, condition, order string, arg any, limit int) (int64, error) {
	q := "SELECT " + id + " FROM " + table + " WHERE " + condition + " ORDER BY " + order + " LIMIT ?"
	rows, err := tx.QueryContext(ctx, s.bind(q), arg, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil || len(ids) == 0 {
		return 0, err
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	values := make([]any, len(ids))
	for i := range ids {
		values[i] = ids[i]
	}
	res, err := tx.ExecContext(ctx, s.bind("DELETE FROM "+table+" WHERE "+id+" IN ("+ph+")"), values...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const incidentColumns = "circuit_scope_key,account_id,account_runtime_key,scope_kind,key_fingerprint,protocol_code,request_lane,model_family,client_model,capability_hash,credential_source_account_id,client_endpoint_family,final_upstream_model,upstream_endpoint_mode,incident_id,parent_incident_id,child_incident_ids_json,caused_by_terminal_outcome_id,state,failure_scope,generation,dispatch_revision,ledger_revision,projected_ledger_revision,transition_id,cooldown_observation_generation,open_until_ms,next_transition_at_ms,lease_id,lease_purpose,lease_owner_run_id,lease_until_ms,attempt_started_at_ms,attempt_hard_deadline_ms,upstream_attempt_observed,backoff_level,consecutive_failures,confirmation_failures_required,confirmation_failure_evidence_keys_json,recovering_successes,last_failure_class,retained_until_ms,created_at_ms,updated_at_ms"
const outboxColumns = "event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,transition_id,dispatch_revision,generation,ledger_revision,status,available_at_ms,claim_token,claimed_by,claim_until_ms,attempt_count,last_error_class,acknowledged_at_ms,created_at_ms,updated_at_ms"

type scanner interface{ Scan(...any) error }

func scanIncident(s scanner) (Incident, error) {
	var v Incident
	var children, evidence sql.NullString
	var failureScope sql.NullString
	var upstream int
	err := s.Scan(&v.CircuitScopeKey, &v.AccountID, &v.AccountRuntimeKey, &v.ScopeKind, &v.KeyFingerprint, &v.ProtocolCode, &v.RequestLane, &v.ModelFamily, &v.ClientModel, &v.CapabilityHash, &v.CredentialSourceAccountID, &v.ClientEndpointFamily, &v.FinalUpstreamModel, &v.UpstreamEndpointMode, &v.IncidentID, &v.ParentIncidentID, &children, &v.CausedByTerminalOutcomeID, &v.State, &failureScope, &v.Generation, &v.DispatchRevision, &v.LedgerRevision, &v.ProjectedLedgerRevision, &v.TransitionID, &v.CooldownObservationGeneration, &v.OpenUntilMS, &v.NextTransitionAtMS, &v.LeaseID, &v.LeasePurpose, &v.LeaseOwnerRunID, &v.LeaseUntilMS, &v.AttemptStartedAtMS, &v.AttemptHardDeadlineMS, &upstream, &v.BackoffLevel, &v.ConsecutiveFailures, &v.ConfirmationFailuresRequired, &evidence, &v.RecoveringSuccesses, &v.LastFailureClass, &v.RetainedUntilMS, &v.CreatedAtMS, &v.UpdatedAtMS)
	if err != nil {
		return Incident{}, err
	}
	if failureScope.Valid {
		v.FailureScope = failureScope.String
	}
	v.UpstreamAttemptObserved = upstream != 0
	childrenJSON := children.String
	if !children.Valid || strings.TrimSpace(childrenJSON) == "" {
		return Incident{}, errors.New("decode child incident ids: persisted JSON is null or empty")
	} else if err = json.Unmarshal([]byte(childrenJSON), &v.ChildIncidentIDs); err != nil {
		return Incident{}, fmt.Errorf("decode child incident ids: %w", err)
	}
	if v.ChildIncidentIDs == nil {
		return Incident{}, errors.New("decode child incident ids: persisted value is not an array")
	}
	if len(v.ChildIncidentIDs) > 64 {
		return Incident{}, errors.New("decode child incident ids: array exceeds 64 items")
	}
	for _, childID := range v.ChildIncidentIDs {
		if err := requireTextBounded(childID, 256, "child incident id"); err != nil {
			return Incident{}, fmt.Errorf("decode child incident ids: %w", err)
		}
	}
	evidenceJSON := evidence.String
	if !evidence.Valid || strings.TrimSpace(evidenceJSON) == "" {
		return Incident{}, errors.New("decode confirmation evidence: persisted JSON is null or empty")
	} else if err = json.Unmarshal([]byte(evidenceJSON), &v.ConfirmationFailureEvidenceKeys); err != nil {
		return Incident{}, fmt.Errorf("decode confirmation evidence: %w", err)
	}
	if v.ConfirmationFailureEvidenceKeys == nil {
		return Incident{}, errors.New("decode confirmation evidence: persisted value is not an array")
	}
	if len(v.ConfirmationFailureEvidenceKeys) > int(v.ConfirmationFailuresRequired)+1 {
		return Incident{}, errors.New("decode confirmation evidence: array exceeds configured bound")
	}
	for _, evidenceKey := range v.ConfirmationFailureEvidenceKeys {
		evidenceKey = strings.ToLower(strings.TrimSpace(evidenceKey))
		if !sha256Hex.MatchString(evidenceKey) {
			return Incident{}, errors.New("decode confirmation evidence: values must be SHA256")
		}
	}
	if err := validateIncident(&v); err != nil {
		return Incident{}, fmt.Errorf("validate persisted incident: %w", err)
	}
	if err := validateIncidentTimes(&v, v.UpdatedAtMS); err != nil {
		return Incident{}, fmt.Errorf("validate persisted incident times: %w", err)
	}
	return v, nil
}
func scanOutbox(s scanner) (Outbox, error) {
	var v Outbox
	err := s.Scan(&v.EventID, &v.ProjectionKey, &v.DedupeKey, &v.EventType, &v.AccountID, &v.AccountRuntimeKey, &v.CircuitScopeKey, &v.IncidentID, &v.TransitionID, &v.DispatchRevision, &v.Generation, &v.LedgerRevision, &v.Status, &v.AvailableAtMS, &v.ClaimToken, &v.ClaimedBy, &v.ClaimUntilMS, &v.AttemptCount, &v.LastErrorClass, &v.AcknowledgedAtMS, &v.CreatedAtMS, &v.UpdatedAtMS)
	return v, err
}
func readIncidents(rows *sql.Rows) ([]Incident, error) {
	var out []Incident
	for rows.Next() {
		v, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func validateIncident(v *Incident) error {
	if err := normalizeIncidentText(v); err != nil {
		return err
	}
	for name, field := range map[string]struct {
		value string
		max   int
	}{
		"circuit scope key":   {v.CircuitScopeKey, 2048},
		"account id":          {v.AccountID, 256},
		"account runtime key": {v.AccountRuntimeKey, 1024},
		"scope kind":          {v.ScopeKind, 32},
		"incident id":         {value(v.IncidentID), 256},
		"state":               {v.State, 64},
		"transition id":       {v.TransitionID, 256},
	} {
		if err := requireTextBounded(field.value, field.max, name); err != nil {
			return err
		}
	}
	if err := validateRuntimeKey(v.AccountRuntimeKey); err != nil {
		return err
	}
	if !allowedIncidentStates[v.State] {
		return fmt.Errorf("invalid incident state: %s", v.State)
	}
	if !allowedScopeKinds[v.ScopeKind] {
		return fmt.Errorf("invalid scope kind: %s", v.ScopeKind)
	}
	if v.FailureScope != "" && !allowedScopeKinds[v.FailureScope] {
		return fmt.Errorf("invalid failure scope: %s", v.FailureScope)
	}
	if err := validateScopeShape(v); err != nil {
		return err
	}
	if err := validateOptionalText(v.KeyFingerprint, 256, "key fingerprint"); err != nil {
		return err
	}
	if err := validateOptionalText(v.ProtocolCode, 64, "protocol code"); err != nil {
		return err
	}
	if err := validateOptionalText(v.RequestLane, 64, "request lane"); err != nil {
		return err
	}
	if err := validateOptionalText(v.ModelFamily, 256, "model family"); err != nil {
		return err
	}
	for name, field := range map[string]struct {
		value *string
		max   int
	}{
		"client model":                 {v.ClientModel, 256},
		"capability hash":              {v.CapabilityHash, 128},
		"credential source account id": {v.CredentialSourceAccountID, 256},
		"client endpoint family":       {v.ClientEndpointFamily, 128},
		"final upstream model":         {v.FinalUpstreamModel, 256},
		"upstream endpoint mode":       {v.UpstreamEndpointMode, 128},
	} {
		if err := validateOptionalText(field.value, field.max, name); err != nil {
			return err
		}
	}
	if err := validateOptionalText(v.LeaseID, 256, "lease id"); err != nil {
		return err
	}
	if err := validateOptionalText(v.LeaseOwnerRunID, 256, "lease owner run id"); err != nil {
		return err
	}
	if err := validateOptionalText(v.ParentIncidentID, 256, "parent incident id"); err != nil {
		return err
	}
	if err := validateOptionalText(v.CausedByTerminalOutcomeID, 256, "caused by terminal outcome id"); err != nil {
		return err
	}
	if v.LeasePurpose != nil && !allowedLeasePurposes[*v.LeasePurpose] {
		return fmt.Errorf("invalid lease purpose: %s", *v.LeasePurpose)
	}
	leaseFieldCount := 0
	if v.LeaseID != nil {
		leaseFieldCount++
	}
	if v.LeasePurpose != nil {
		leaseFieldCount++
	}
	if v.LeaseOwnerRunID != nil {
		leaseFieldCount++
	}
	if v.LeaseUntilMS != nil {
		leaseFieldCount++
	}
	if leaseFieldCount != 0 && leaseFieldCount != 4 {
		return errors.New("lease id, purpose, owner run id and until must be provided together")
	}
	if leaseFieldCount == 0 && (v.AttemptStartedAtMS != nil || v.AttemptHardDeadlineMS != nil) {
		return errors.New("attempt timestamps require an active lease")
	}
	if leaseFieldCount == 4 && (v.AttemptStartedAtMS == nil || v.AttemptHardDeadlineMS == nil) {
		return errors.New("active lease requires attempt start and hard deadline")
	}
	if v.LastFailureClass != nil && !allowedFailureClasses[*v.LastFailureClass] {
		return fmt.Errorf("invalid last failure class: %s", *v.LastFailureClass)
	}
	if len(v.ChildIncidentIDs) > 64 {
		return errors.New("child incident ids exceed the maximum of 64")
	}
	childIDs := make(map[string]struct{}, len(v.ChildIncidentIDs))
	for i, childID := range v.ChildIncidentIDs {
		childID = strings.TrimSpace(childID)
		v.ChildIncidentIDs[i] = childID
		if err := requireTextBounded(childID, 256, "child incident id"); err != nil {
			return err
		}
		if _, duplicate := childIDs[childID]; duplicate {
			return errors.New("child incident ids must be unique")
		}
		childIDs[childID] = struct{}{}
	}
	if len(v.ConfirmationFailureEvidenceKeys) > int(v.ConfirmationFailuresRequired)+1 {
		return errors.New("confirmation evidence keys exceed the configured bound")
	}
	evidenceKeys := make(map[string]struct{}, len(v.ConfirmationFailureEvidenceKeys))
	for i, evidenceKey := range v.ConfirmationFailureEvidenceKeys {
		evidenceKey = strings.ToLower(strings.TrimSpace(evidenceKey))
		v.ConfirmationFailureEvidenceKeys[i] = evidenceKey
		if !sha256Hex.MatchString(evidenceKey) {
			return errors.New("confirmation evidence keys must be SHA256 values")
		}
		if _, duplicate := evidenceKeys[evidenceKey]; duplicate {
			return errors.New("confirmation evidence keys must be unique")
		}
		evidenceKeys[evidenceKey] = struct{}{}
	}
	if v.DispatchRevision < 1 || v.Generation < 0 || v.CooldownObservationGeneration < 0 || v.ConsecutiveFailures < 0 || v.BackoffLevel < 0 || v.ConfirmationFailuresRequired < 1 || v.RecoveringSuccesses < 0 {
		return errors.New("incident numeric values are invalid")
	}
	if v.ConsecutiveFailures > v.ConfirmationFailuresRequired {
		return errors.New("consecutive failures exceed confirmation failures required")
	}
	if v.State == "CLOSED" && v.RetainedUntilMS == nil {
		return errors.New("closed incident requires retained_until_ms")
	}
	if v.State != "CLOSED" && v.RetainedUntilMS != nil {
		return errors.New("non-closed incident cannot have retained_until_ms")
	}
	return nil
}
func validateIncidentTimes(v *Incident, nowMS int64) error {
	if err := requireNonNegativeMS(v.CreatedAtMS, "createdAtMs"); err != nil {
		return err
	}
	if err := requireNonNegativeMS(v.UpdatedAtMS, "updatedAtMs"); err != nil {
		return err
	}
	if v.CreatedAtMS > v.UpdatedAtMS {
		return errors.New("created_at_ms cannot follow updated_at_ms")
	}
	for name, value := range map[string]*int64{
		"openUntilMs": v.OpenUntilMS, "nextTransitionAtMs": v.NextTransitionAtMS,
		"leaseUntilMs": v.LeaseUntilMS, "attemptStartedAtMs": v.AttemptStartedAtMS,
		"attemptHardDeadlineMs": v.AttemptHardDeadlineMS, "retainedUntilMs": v.RetainedUntilMS,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%s cannot be negative", name)
		}
	}
	if v.State == "CLOSED" && v.RetainedUntilMS != nil && *v.RetainedUntilMS < nowMS {
		return errors.New("closed incident retained_until_ms cannot precede updated_at_ms")
	}
	if v.LeaseUntilMS != nil && v.AttemptStartedAtMS != nil && v.AttemptHardDeadlineMS != nil {
		if *v.AttemptStartedAtMS > *v.AttemptHardDeadlineMS || *v.AttemptHardDeadlineMS > *v.LeaseUntilMS {
			return errors.New("lease timestamps must satisfy attempt start <= hard deadline <= lease until")
		}
	}
	return nil
}
func validateScopeShape(v *Incident) error {
	switch v.ScopeKind {
	case "account":
		if v.KeyFingerprint != nil || v.ProtocolCode != nil || v.RequestLane != nil || v.ModelFamily != nil || hasKeyModelFields(v) {
			return errors.New("account scope cannot carry key/protocol/model fields")
		}
	case "key":
		if v.KeyFingerprint == nil || v.ProtocolCode != nil || v.RequestLane != nil || v.ModelFamily != nil || hasKeyModelFields(v) {
			return errors.New("key scope requires only key fingerprint")
		}
	case "protocol_model":
		if v.KeyFingerprint != nil || v.ProtocolCode == nil || v.RequestLane == nil || v.ModelFamily == nil || hasKeyModelFields(v) {
			return errors.New("protocol_model scope requires protocol, request lane and model family")
		}
	case "key_model":
		if v.KeyFingerprint == nil || v.ProtocolCode != nil || v.RequestLane != nil || v.ModelFamily != nil || !hasAllKeyModelFields(v) {
			return errors.New("key_model scope requires key fingerprint and complete key-model identity")
		}
	}
	return nil
}

func hasKeyModelFields(v *Incident) bool {
	return v.ClientModel != nil || v.CapabilityHash != nil || v.CredentialSourceAccountID != nil || v.ClientEndpointFamily != nil || v.FinalUpstreamModel != nil || v.UpstreamEndpointMode != nil
}

func hasAllKeyModelFields(v *Incident) bool {
	return v.ClientModel != nil && v.CapabilityHash != nil && v.CredentialSourceAccountID != nil && v.ClientEndpointFamily != nil && v.FinalUpstreamModel != nil && v.UpstreamEndpointMode != nil
}
func validateOptionalText(value *string, maxLength int, name string) error {
	if value == nil {
		return nil
	}
	if err := requireText(*value, name); err != nil {
		return err
	}
	if len(*value) > maxLength {
		return fmt.Errorf("%s is too long", name)
	}
	return nil
}
func requireText(v, name string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}
func requireTextBounded(v string, maxLength int, name string) error {
	if err := requireText(v, name); err != nil {
		return err
	}
	if len(v) > maxLength {
		return fmt.Errorf("%s is too long", name)
	}
	return nil
}
func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func ptr[T any](v T) *T { return &v }
func optionalIncident(v Incident, ok bool) *Incident {
	if !ok {
		return nil
	}
	return &v
}
func nullableText(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func uniqueKeys(in []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if err := validateRuntimeKey(v); err != nil {
			return nil, err
		}
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out, nil
}

var allowedIncidentStates = map[string]bool{
	"CLOSED": true, "SUSPECT": true, "OPEN": true, "HALF_OPEN": true,
	"RECOVERING": true, "PERSISTING": true, "SHADOWED_BY_PERSISTENT": true,
}
var allowedScopeKinds = map[string]bool{"account": true, "key": true, "protocol_model": true, "key_model": true}
var allowedLeasePurposes = map[string]bool{"confirmation": true, "half_open": true, "recovery": true, "cooldown_retest": true, "background_probe": true}
var allowedFailureClasses = map[string]bool{"connect_failed": true, "timeout_before_complete": true, "read_interrupted": true, "incomplete_response": true, "explicit_policy": true}

func normalizeErrorClass(v string) (string, error) {
	v = strings.TrimSpace(v)
	if err := requireText(v, "error class"); err != nil {
		return "", err
	}
	if len(v) > 64 {
		return "", errors.New("error class is too long")
	}
	if !machineCategory.MatchString(v) {
		return "", errors.New("error class must be a bounded machine category")
	}
	return v, nil
}
func validateRuntimeKey(v string) error {
	// Node's persistence contract treats runtime keys as trimmed, bounded text;
	// they are not restricted to machine-category characters.
	return requireTextBounded(v, 1024, "account runtime key")
}

func normalizeIncidentText(v *Incident) error {
	for name, field := range map[string]*string{
		"circuit scope key":   &v.CircuitScopeKey,
		"account id":          &v.AccountID,
		"account runtime key": &v.AccountRuntimeKey,
		"transition id":       &v.TransitionID,
	} {
		*field = strings.TrimSpace(*field)
		if err := requireTextBounded(*field, map[string]int{
			"circuit scope key": 2048, "account id": 256, "account runtime key": 1024,
			"transition id": 256,
		}[name], name); err != nil {
			return err
		}
	}
	for name, field := range map[string]*string{
		"incident id": v.IncidentID, "parent incident id": v.ParentIncidentID,
		"caused by terminal outcome id": v.CausedByTerminalOutcomeID,
		"key fingerprint":               v.KeyFingerprint, "protocol code": v.ProtocolCode,
		"request lane": v.RequestLane, "model family": v.ModelFamily,
		"client model": v.ClientModel, "capability hash": v.CapabilityHash,
		"credential source account id": v.CredentialSourceAccountID,
		"client endpoint family":       v.ClientEndpointFamily, "final upstream model": v.FinalUpstreamModel,
		"upstream endpoint mode": v.UpstreamEndpointMode, "lease id": v.LeaseID,
		"lease owner run id": v.LeaseOwnerRunID,
	} {
		if field == nil {
			continue
		}
		*field = strings.TrimSpace(*field)
		max := 256
		switch name {
		case "key fingerprint":
			max = 256
		case "protocol code", "request lane":
			max = 64
		case "model family", "client model", "final upstream model":
			max = 256
		case "capability hash":
			max = 128
		case "credential source account id":
			max = 256
		case "client endpoint family", "upstream endpoint mode":
			max = 128
		}
		if err := requireTextBounded(*field, max, name); err != nil {
			return err
		}
	}
	return nil
}
func requireNonNegativeMS(v int64, name string) error {
	if v < 0 {
		return fmt.Errorf("%s cannot be negative", name)
	}
	return nil
}
func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CoveredManifestOperations is evidence for this isolated port. It does not
// authorize changing the transaction group's capability status by itself.
var CoveredManifestOperations = []string{"advance_account_circuit_dispatch_revision", "compare_and_set_account_circuit_incident", "get_account_circuit_incident_by_scope_key", "claim_account_circuit_outbox", "ack_account_circuit_outbox", "release_account_circuit_outbox_for_replay", "list_account_circuit_incidents_for_rebuild", "list_account_circuit_incidents_by_runtime_keys", "list_account_circuit_projection_gaps", "cleanup_account_circuit_control_plane"}

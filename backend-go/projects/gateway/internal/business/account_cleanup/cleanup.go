// Package accountcleanup owns the Business side of the
// cleanup_expired_deleted_accounts transaction group.
//
// The Node operation has two storage domains. This package owns only the
// Business domain: it can revoke/soft-delete orphaned account instances and
// physically remove an expired tombstone once a separate, read-only record
// fence proves that dataset/usage/stats work is complete. It never creates a
// dataset target, calls Node, or talks to HTTP, IPC, or a queue.
package accountcleanup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrOwnerGate     = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrInvalidMode   = errors.New("account cleanup database mode is invalid")
	ErrInvalidSchema = errors.New("account cleanup PostgreSQL schema is invalid")
	ErrInvalidFence  = errors.New("account cleanup record fence is invalid")
	ErrCAS           = errors.New("account cleanup account fence is stale")
)

type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

// OwnerGate is external, auditable handoff evidence. A partial handoff never
// permits a Business write, even when the database and all tables exist.
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

func (g OwnerGate) Ready() bool {
	return g.Confirmed && g.SchemaReady && g.NodeWriterStopped
}

// CleanupTarget is the durable target shape consumed by the dataset/usage/
// stats record-maintenance owner. The target is deliberately not persisted by
// this package: Node writes it to juhe_dataset, which is outside the Business
// owner contract. Until that writer and a completion fence are migrated, the
// target is returned as a pending handoff.
type CleanupTarget struct {
	AccountID         string   `json:"accountId"`
	SystemAccountID   string   `json:"systemAccountId"`
	RelatedAccountIDs []string `json:"relatedAccountIds,omitempty"`
	AuthorizationIDs  []string `json:"authorizationIds,omitempty"`
	TeamScopeIDs      []string `json:"teamScopeIds,omitempty"`
}

type RecordFenceStatus string

const (
	RecordFenceUnknown RecordFenceStatus = "unknown"
	RecordFencePending RecordFenceStatus = "pending"
	RecordFenceCleared RecordFenceStatus = "cleared"
)

// RecordFence is read-only evidence supplied by the record owner. A cleared
// fence must carry a non-empty token (for example a committed target/cursor
// identity); without it the Business side cannot safely delete an account.
type RecordFence struct {
	Status RecordFenceStatus `json:"status"`
	Token  string            `json:"token,omitempty"`
	Reason string            `json:"reason,omitempty"`
}

// RecordFenceReader is intentionally a read-only seam. Its implementation
// may inspect dataset/usage/stats, but must not mutate them or bridge through
// Node, HTTP, IPC, or a queue.
type RecordFenceReader interface {
	ReadRecordFence(context.Context, CleanupTarget) (RecordFence, error)
}

// RecordFenceReaderFunc adapts a function while keeping the seam explicit in
// tests and in future in-process readers.
type RecordFenceReaderFunc func(context.Context, CleanupTarget) (RecordFence, error)

func (f RecordFenceReaderFunc) ReadRecordFence(ctx context.Context, target CleanupTarget) (RecordFence, error) {
	if f == nil {
		return RecordFence{Status: RecordFenceUnknown, Reason: "record fence reader is not configured"}, nil
	}
	return f(ctx, target)
}

type CleanupInput struct {
	// Empty uses one month before the Store clock, matching Node's physical
	// cleanup retention. A supplied value is preserved byte-for-byte after
	// non-empty validation by the caller's storage semantics.
	CutoffDeletedAt string
	Limit           int
	RecordFence     RecordFenceReader
}

type CleanupFailure struct {
	AccountID string `json:"accountId"`
	Stage     string `json:"stage"`
	Error     string `json:"error"`
}

type CleanupResult struct {
	CutoffDeletedAt                 string           `json:"cutoffDeletedAt"`
	OrphanedAuthorizationInstances  int              `json:"orphanedAuthorizationInstances"`
	Attempted                       int              `json:"attempted"`
	Completed                       int              `json:"completed"`
	Deferred                        int              `json:"deferred"`
	Failed                          int              `json:"failed"`
	DeletedRows                     int              `json:"deletedRows"`
	PhysicallyDeletedAccounts       int              `json:"physicallyDeletedAccounts"`
	PhysicallyDeletedAuthorizations int              `json:"physicallyDeletedAuthorizations"`
	PhysicallyDeletedGrants         int              `json:"physicallyDeletedGrants"`
	PhysicallyDeletedGroupBindings  int              `json:"physicallyDeletedGroupBindings"`
	RecordCleanupTargets            []CleanupTarget  `json:"recordCleanupTargets"`
	Failures                        []CleanupFailure `json:"failures,omitempty"`
}

type accountRow struct {
	ID                            string
	SystemAccountID               string
	AuthorizationInstanceAuthID   sql.NullString
	AuthorizationInstanceSourceID sql.NullString
	DeletedAt                     sql.NullString
	UpdatedAt                     sql.NullString
}

type relatedAccountRow struct {
	ID                          string
	SystemAccountID             string
	AuthorizationInstanceAuthID sql.NullString
	DeletedAt                   sql.NullString
	UpdatedAt                   sql.NullString
}

type authorizationRow struct {
	ID                   string
	ResourceID           string
	GranteeSystemAccount string
}

type teamSourceRow struct {
	AuthorizationID string
	SourceTeamID    string
}

type candidate struct {
	row                    accountRow
	RelatedAccountIDs      []string
	RelatedRows            []relatedAccountRow
	AccountIDs             []string
	AuthorizationIDs       []string
	TeamScopeIDs           []string
	GrantIDs               []string
	AuthorizationInstances map[string]string
}

type businessDeleteResult struct {
	Accounts       int
	Authorizations int
	Grants         int
	GroupBindings  int
}

type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	now    func() time.Time
}

// Port is the package-local owner boundary. Gateway wiring is intentionally
// left to the integration owner; implementing this interface does not expose
// an HTTP route or register a main-process handler.
type Port interface {
	CheckContract(context.Context) error
	CleanupExpiredDeletedAccounts(context.Context, CleanupInput) (CleanupResult, error)
}

var _ Port = (*Store)(nil)

var postgresIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const (
	defaultCleanupLimit = 20
	maxCleanupLimit     = 200
	nodeTimeLayout      = "2006-01-02T15:04:05.000Z"
	retentionMonths     = 1
)

// New constructs an isolated Business SQL owner. It never creates or alters
// schema; callers must establish SchemaReady from an external preflight.
func New(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Store, error) {
	if db == nil {
		return nil, errors.New("account cleanup database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, ErrInvalidMode
	}
	schema = strings.TrimSpace(schema)
	if mode == Postgres {
		if schema == "" {
			schema = "juhe_business"
		}
		if !postgresIdentifier.MatchString(schema) {
			return nil, ErrInvalidSchema
		}
	}
	return &Store{db: db, mode: mode, schema: schema, gate: gate, now: time.Now}, nil
}

func NewStore(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Store, error) {
	return New(db, mode, schema, gate)
}

func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

// CheckContract verifies the pre-existing Business relations needed by this
// package. Runtime schema creation is forbidden. The record cleanup target and
// all dataset/usage/stats relations remain outside this Business owner.
func (s *Store) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	for _, relation := range requiredRelations {
		if _, err := s.db.ExecContext(ctx, "SELECT 1 FROM "+s.table(relation)+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify account cleanup relation %s: %w", relation, err)
		}
	}
	return nil
}

// Cleanup executes the sole owner-manifest operation. Orphaned authorization
// instances are soft-deleted first. Expired roots/instances are then handled
// in Node's root-first order; each candidate is physically deleted only after
// a cleared durable record fence, and every Business mutation is transactional
// and protected by a row identity/update CAS.
func (s *Store) Cleanup(ctx context.Context, input CleanupInput) (CleanupResult, error) {
	if err := s.requireOwner(); err != nil {
		return CleanupResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return CleanupResult{}, err
	}
	cutoff := strings.TrimSpace(input.CutoffDeletedAt)
	if cutoff == "" {
		cutoff = s.now().UTC().AddDate(0, -retentionMonths, 0).Format(nodeTimeLayout)
	}
	limit := normalizeLimit(input.Limit)
	result := CleanupResult{CutoffDeletedAt: cutoff, RecordCleanupTargets: []CleanupTarget{}}

	orphans, err := s.listOrphanedInstances(ctx, limit)
	if err != nil {
		return result, fmt.Errorf("list orphaned authorization instances: %w", err)
	}
	for _, row := range orphans {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if changed, err := s.softDeleteOrphan(ctx, row); err != nil {
			return result, fmt.Errorf("soft-delete orphaned authorization instance %s: %w", row.ID, err)
		} else if changed {
			result.OrphanedAuthorizationInstances++
		}
	}

	candidates, err := s.listCandidates(ctx, cutoff, limit)
	if err != nil {
		return result, fmt.Errorf("list expired deleted accounts: %w", err)
	}
	for _, row := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Attempted++
		target, buildErr := s.buildCandidate(ctx, row)
		if buildErr != nil {
			result.Failed++
			result.Failures = append(result.Failures, CleanupFailure{AccountID: row.ID, Stage: "build_target", Error: buildErr.Error()})
			continue
		}
		publicTarget := target.public()
		fence := RecordFence{Status: RecordFenceUnknown, Reason: "dataset/usage/stats owner has not supplied a durable completion fence"}
		if input.RecordFence != nil {
			fence, err = input.RecordFence.ReadRecordFence(ctx, publicTarget)
			if err != nil {
				result.Failed++
				result.RecordCleanupTargets = append(result.RecordCleanupTargets, publicTarget)
				result.Failures = append(result.Failures, CleanupFailure{AccountID: row.ID, Stage: "read_record_fence", Error: err.Error()})
				continue
			}
		}
		switch fence.Status {
		case RecordFenceUnknown, RecordFencePending:
			result.Deferred++
			result.RecordCleanupTargets = append(result.RecordCleanupTargets, publicTarget)
			continue
		case RecordFenceCleared:
			if strings.TrimSpace(fence.Token) == "" {
				result.Failed++
				result.RecordCleanupTargets = append(result.RecordCleanupTargets, publicTarget)
				result.Failures = append(result.Failures, CleanupFailure{AccountID: row.ID, Stage: "read_record_fence", Error: ErrInvalidFence.Error()})
				continue
			}
		default:
			result.Failed++
			result.RecordCleanupTargets = append(result.RecordCleanupTargets, publicTarget)
			result.Failures = append(result.Failures, CleanupFailure{AccountID: row.ID, Stage: "read_record_fence", Error: ErrInvalidFence.Error()})
			continue
		}
		deleted, err := s.deleteBusiness(ctx, target, cutoff)
		if err != nil {
			if errors.Is(err, ErrCAS) {
				result.Failed++
				result.Failures = append(result.Failures, CleanupFailure{AccountID: row.ID, Stage: "business_cas", Error: err.Error()})
				continue
			}
			result.Failed++
			result.Failures = append(result.Failures, CleanupFailure{AccountID: row.ID, Stage: "business_delete", Error: err.Error()})
			continue
		}
		result.Completed++
		result.PhysicallyDeletedAccounts += deleted.Accounts
		result.PhysicallyDeletedAuthorizations += deleted.Authorizations
		result.PhysicallyDeletedGrants += deleted.Grants
		result.PhysicallyDeletedGroupBindings += deleted.GroupBindings
		// Node's cleanup operation keeps deletedRows at zero; the typed
		// per-relation counters above are the authoritative Business evidence.
	}
	return result, nil
}

// CleanupExpiredDeletedAccounts is the operation-shaped alias used by future
// Gateway wiring. It does not register a route or change the owner manifest.
func (s *Store) CleanupExpiredDeletedAccounts(ctx context.Context, input CleanupInput) (CleanupResult, error) {
	return s.Cleanup(ctx, input)
}

func normalizeLimit(limit int) int {
	if limit == 0 {
		return defaultCleanupLimit
	}
	if limit < 0 {
		return 1
	}
	if limit > maxCleanupLimit {
		return maxCleanupLimit
	}
	return limit
}

func (s *Store) table(name string) string {
	if s.mode == Postgres {
		return s.schema + "." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if s.mode != Postgres {
		return query
	}
	var out strings.Builder
	position := 1
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&out, "$%d", position)
			position++
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

var requiredRelations = []string{
	"accounts",
	"account_supported_models",
	"account_model_mappings",
	"account_tag_bindings",
	"account_name_search_terms",
	"account_name_search_documents",
	"account_api_key_runtime_states",
	"group_accounts",
	"resource_authorizations",
	"resource_authorization_sources",
	"resource_authorization_grants",
	"request_quota_hourly_window_scope_bindings",
}

var CoveredManifestOperations = []string{"cleanup_expired_deleted_accounts"}

func (c candidate) public() CleanupTarget {
	related := unique(c.RelatedAccountIDs)
	authorizations := unique(c.AuthorizationIDs)
	teams := unique(c.TeamScopeIDs)
	sort.Strings(related)
	sort.Strings(authorizations)
	sort.Strings(teams)
	return CleanupTarget{
		AccountID:         c.row.ID,
		SystemAccountID:   c.row.SystemAccountID,
		RelatedAccountIDs: related,
		AuthorizationIDs:  authorizations,
		TeamScopeIDs:      teams,
	}
}

func (s *Store) listOrphanedInstances(ctx context.Context, limit int) ([]accountRow, error) {
	q := `SELECT accounts.id,accounts.system_account_id,
        accounts.authorization_instance_authorization_id,
        accounts.authorization_instance_source_account_id,
        accounts.deleted_at,accounts.updated_at
      FROM ` + s.table("accounts") + ` accounts
      LEFT JOIN ` + s.table("resource_authorizations") + ` ra
        ON ra.id = accounts.authorization_instance_authorization_id
      LEFT JOIN ` + s.table("accounts") + ` source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
      LEFT JOIN ` + s.table("accounts") + ` resource_accounts
        ON resource_accounts.id = ra.resource_id
      WHERE accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NOT NULL
        AND (
          ra.id IS NULL
          OR ra.resource_type <> 'account'
          OR (accounts.authorization_instance_source_account_id IS NOT NULL AND source_accounts.id IS NULL)
          OR source_accounts.deleted_at IS NOT NULL
          OR resource_accounts.id IS NULL
          OR resource_accounts.deleted_at IS NOT NULL
        )
      ORDER BY accounts.updated_at ASC,accounts.id ASC
      LIMIT ?`
	return s.queryAccounts(ctx, q, limit)
}

func (s *Store) listCandidates(ctx context.Context, cutoff string, limit int) ([]accountRow, error) {
	root := `SELECT id,system_account_id,authorization_instance_authorization_id,
        authorization_instance_source_account_id,deleted_at,updated_at
      FROM ` + s.table("accounts") + `
      WHERE deleted_at IS NOT NULL AND deleted_at<=?
        AND authorization_instance_authorization_id IS NULL
      ORDER BY deleted_at ASC,updated_at ASC,id ASC LIMIT ?`
	rows, err := s.queryAccounts(ctx, root, cutoff, limit)
	if err != nil {
		return nil, err
	}
	remaining := limit - len(rows)
	if remaining <= 0 {
		return rows, nil
	}
	instances := `SELECT child.id,child.system_account_id,child.authorization_instance_authorization_id,
        child.authorization_instance_source_account_id,child.deleted_at,child.updated_at
      FROM ` + s.table("accounts") + ` child
      LEFT JOIN ` + s.table("accounts") + ` source_accounts
        ON source_accounts.id=child.authorization_instance_source_account_id
      WHERE child.deleted_at IS NOT NULL AND child.deleted_at<=?
        AND child.authorization_instance_authorization_id IS NOT NULL
        AND (child.authorization_instance_source_account_id IS NULL
          OR source_accounts.id IS NULL OR source_accounts.deleted_at IS NULL
          OR source_accounts.deleted_at>?)
      ORDER BY child.deleted_at ASC,child.updated_at ASC,child.id ASC LIMIT ?`
	more, err := s.queryAccounts(ctx, instances, cutoff, cutoff, remaining)
	if err != nil {
		return nil, err
	}
	return append(rows, more...), nil
}

func (s *Store) queryAccounts(ctx context.Context, query string, args ...any) ([]accountRow, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []accountRow
	for rows.Next() {
		var row accountRow
		if err := rows.Scan(&row.ID, &row.SystemAccountID, &row.AuthorizationInstanceAuthID, &row.AuthorizationInstanceSourceID, &row.DeletedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) softDeleteOrphan(ctx context.Context, row accountRow) (bool, error) {
	if strings.TrimSpace(row.ID) == "" || !row.AuthorizationInstanceAuthID.Valid {
		return false, nil
	}
	now := s.now().UTC().Format(nodeTimeLayout)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	selectQuery := `SELECT authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at
		FROM ` + s.table("accounts") + ` WHERE id=? AND system_account_id=?`
	if s.mode == Postgres {
		selectQuery += " FOR UPDATE"
	}
	var currentAuth, currentSource, currentDeleted, currentUpdated sql.NullString
	if err := tx.QueryRowContext(ctx, s.bind(selectQuery), row.ID, row.SystemAccountID).Scan(&currentAuth, &currentSource, &currentDeleted, &currentUpdated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if currentDeleted.Valid {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if !sameNullString(currentAuth, row.AuthorizationInstanceAuthID) || !sameNullString(currentSource, row.AuthorizationInstanceSourceID) || !sameNullString(currentUpdated, row.UpdatedAt) {
		return false, ErrCAS
	}
	if err := s.revokeInstance(ctx, tx, row, now); err != nil {
		return false, err
	}
	q := `UPDATE ` + s.table("accounts") + `
      SET status='disabled',schedulable=0,cooldown_until=NULL,deleted_at=?,deleted_by=?,updated_at=?
		WHERE id=? AND system_account_id=? AND authorization_instance_authorization_id=?
		  AND ((authorization_instance_source_account_id IS NULL AND ? IS NULL)
		    OR authorization_instance_source_account_id=?)
		  AND deleted_at IS NULL AND updated_at=?`
	var sourceID any
	if row.AuthorizationInstanceSourceID.Valid {
		sourceID = row.AuthorizationInstanceSourceID.String
	}
	res, err := tx.ExecContext(ctx, s.bind(q), now, "sys_admin", now, row.ID, row.SystemAccountID, row.AuthorizationInstanceAuthID.String, sourceID, sourceID, row.UpdatedAt.String)
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		// A concurrent writer may have already completed this exact tombstone.
		var deleted sql.NullString
		if err := tx.QueryRowContext(ctx, s.bind("SELECT deleted_at FROM "+s.table("accounts")+" WHERE id=? AND system_account_id=?"), row.ID, row.SystemAccountID).Scan(&deleted); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if deleted.Valid {
			if err := tx.Commit(); err != nil {
				return false, err
			}
			return false, nil
		}
		return false, ErrCAS
	}
	for _, table := range []string{"account_tag_bindings", "account_name_search_terms", "account_name_search_documents"} {
		if _, err := tx.ExecContext(ctx, s.bind("DELETE FROM "+s.table(table)+" WHERE account_id=?"), row.ID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) revokeInstance(ctx context.Context, tx *sql.Tx, row accountRow, now string) error {
	authID := row.AuthorizationInstanceAuthID.String
	var resourceType, resourceID string
	err := tx.QueryRowContext(ctx, s.bind("SELECT resource_type,resource_id FROM "+s.table("resource_authorizations")+" WHERE id=? LIMIT 1"), authID).Scan(&resourceType, &resourceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && resourceType == "account" && strings.TrimSpace(resourceID) != "" {
		if err := s.revokeAccountAuthorizations(ctx, tx, resourceID, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
      SET status='revoked',ended_at=COALESCE(ended_at,?),ended_reason=COALESCE(ended_reason,'account_deleted'),
          revoked_by=?,revoked_at=?,updated_at=?
      WHERE authorization_id=? AND status IN ('active','superseded')`), now, "sys_admin", now, now, authID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
      SET status='revoked',effective_source_type=NULL,effective_source_team_id=NULL,
          revoked_by=COALESCE(revoked_by,?),revoked_at=COALESCE(revoked_at,?),
          revoked_reason=COALESCE(revoked_reason,'account_deleted'),last_source_changed_at=?,updated_at=?
      WHERE id=? AND status<>'returned'`), "sys_admin", now, now, now, authID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind("DELETE FROM "+s.table("group_accounts")+" WHERE account_authorization_id=?"), authID)
	return err
}

func (s *Store) revokeAccountAuthorizations(ctx context.Context, tx *sql.Tx, accountID, now string) error {
	q := `UPDATE ` + s.table("resource_authorization_sources") + `
      SET status='revoked',ended_at=COALESCE(ended_at,?),ended_reason=COALESCE(ended_reason,'account_deleted'),
          revoked_by=?,revoked_at=?,updated_at=?
      WHERE authorization_id IN (SELECT id FROM ` + s.table("resource_authorizations") + ` WHERE resource_type='account' AND resource_id=? AND status<>'returned')
        AND status IN ('active','superseded')`
	if _, err := tx.ExecContext(ctx, s.bind(q), now, "sys_admin", now, now, accountID); err != nil {
		return err
	}
	q = `UPDATE ` + s.table("resource_authorizations") + `
      SET status='revoked',effective_source_type=NULL,effective_source_team_id=NULL,
          revoked_by=COALESCE(revoked_by,?),revoked_at=COALESCE(revoked_at,?),
          revoked_reason=COALESCE(revoked_reason,'account_deleted'),last_source_changed_at=?,updated_at=?
      WHERE resource_type='account' AND resource_id=? AND status<>'returned'`
	if _, err := tx.ExecContext(ctx, s.bind(q), "sys_admin", now, now, now, accountID); err != nil {
		return err
	}
	q = `DELETE FROM ` + s.table("request_quota_hourly_window_scope_bindings") + `
      WHERE source_type='resource_authorization_grant' AND source_id IN
       (SELECT id FROM ` + s.table("resource_authorization_grants") + ` WHERE resource_type='account' AND resource_id=?)`
	if _, err := tx.ExecContext(ctx, s.bind(q), accountID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
      SET status='revoked',revoked_by=COALESCE(revoked_by,?),revoked_at=COALESCE(revoked_at,?),updated_at=?
      WHERE resource_type='account' AND resource_id=? AND status NOT IN ('revoked','returned')`), "sys_admin", now, now, accountID)
	return err
}

func (s *Store) buildCandidate(ctx context.Context, row accountRow) (candidate, error) {
	c := candidate{row: row, AuthorizationInstances: map[string]string{}}
	isInstance := row.AuthorizationInstanceAuthID.Valid && strings.TrimSpace(row.AuthorizationInstanceAuthID.String) != ""
	if !isInstance {
		q := `SELECT id,system_account_id,authorization_instance_authorization_id,deleted_at,updated_at FROM ` + s.table("accounts") + ` WHERE authorization_instance_source_account_id=? ORDER BY created_at ASC,id ASC`
		related, err := s.queryRelated(ctx, q, row.ID)
		if err != nil {
			return candidate{}, err
		}
		c.RelatedRows = related
		for _, relatedRow := range related {
			c.RelatedAccountIDs = appendUnique(c.RelatedAccountIDs, relatedRow.ID)
			if relatedRow.AuthorizationInstanceAuthID.Valid && strings.TrimSpace(relatedRow.AuthorizationInstanceAuthID.String) != "" {
				c.AuthorizationInstances[relatedRow.AuthorizationInstanceAuthID.String] = relatedRow.ID
			}
		}
	}
	c.AccountIDs = appendUnique(c.AccountIDs, row.ID)
	for _, id := range c.RelatedAccountIDs {
		c.AccountIDs = appendUnique(c.AccountIDs, id)
	}
	if isInstance {
		c.AuthorizationInstances[row.AuthorizationInstanceAuthID.String] = row.ID
	}
	authRows, err := s.queryAuthorizations(ctx, c.AccountIDs, mapKeys(c.AuthorizationInstances))
	if err != nil {
		return candidate{}, err
	}
	for _, auth := range authRows {
		c.AuthorizationIDs = appendUnique(c.AuthorizationIDs, auth.ID)
	}
	active, err := s.activeAuthorizationInstances(ctx, c.AuthorizationIDs, isInstance)
	if err != nil {
		return candidate{}, err
	}
	filtered := c.AuthorizationIDs[:0]
	for _, id := range c.AuthorizationIDs {
		if !active[id] {
			filtered = append(filtered, id)
		}
	}
	c.AuthorizationIDs = filtered
	resourceByID := map[string]string{}
	for _, auth := range authRows {
		resourceByID[auth.ID] = auth.ResourceID
	}
	teamRows, err := s.queryTeamSources(ctx, c.AuthorizationIDs)
	if err != nil {
		return candidate{}, err
	}
	for _, team := range teamRows {
		accountID := c.AuthorizationInstances[team.AuthorizationID]
		if accountID == "" {
			accountID = resourceByID[team.AuthorizationID]
		}
		if accountID == "" {
			accountID = row.ID
		}
		c.TeamScopeIDs = appendUnique(c.TeamScopeIDs, accountID+":"+team.SourceTeamID)
	}
	grantRows, err := s.queryGrantIDs(ctx, c.AccountIDs, c.AuthorizationIDs, isInstance)
	if err != nil {
		return candidate{}, err
	}
	for _, id := range grantRows {
		c.GrantIDs = appendUnique(c.GrantIDs, id)
	}
	return c, nil
}

func (s *Store) queryRelated(ctx context.Context, query string, args ...any) ([]relatedAccountRow, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []relatedAccountRow
	for rows.Next() {
		var row relatedAccountRow
		if err := rows.Scan(&row.ID, &row.SystemAccountID, &row.AuthorizationInstanceAuthID, &row.DeletedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) queryAuthorizations(ctx context.Context, accountIDs, explicitIDs []string) ([]authorizationRow, error) {
	seen := map[string]authorizationRow{}
	for _, chunk := range chunks(unique(accountIDs), 900) {
		if len(chunk) == 0 {
			continue
		}
		q := `SELECT id,resource_id,grantee_system_account_id FROM ` + s.table("resource_authorizations") + ` WHERE resource_type='account' AND resource_id IN (` + placeholders(len(chunk)) + `)`
		rows, err := s.queryAuthorizationRows(ctx, q, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			seen[row.ID] = row
		}
	}
	for _, chunk := range chunks(unique(explicitIDs), 900) {
		if len(chunk) == 0 {
			continue
		}
		q := `SELECT id,resource_id,grantee_system_account_id FROM ` + s.table("resource_authorizations") + ` WHERE id IN (` + placeholders(len(chunk)) + `)`
		rows, err := s.queryAuthorizationRows(ctx, q, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			seen[row.ID] = row
		}
	}
	out := make([]authorizationRow, 0, len(seen))
	for _, row := range seen {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) queryAuthorizationRows(ctx context.Context, query string, args ...any) ([]authorizationRow, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []authorizationRow
	for rows.Next() {
		var row authorizationRow
		if err := rows.Scan(&row.ID, &row.ResourceID, &row.GranteeSystemAccount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) activeAuthorizationInstances(ctx context.Context, ids []string, onlyForInstance bool) (map[string]bool, error) {
	active := map[string]bool{}
	if !onlyForInstance {
		return active, nil
	}
	for _, chunk := range chunks(unique(ids), 900) {
		if len(chunk) == 0 {
			continue
		}
		q := `SELECT DISTINCT authorization_instance_authorization_id FROM ` + s.table("accounts") + ` WHERE authorization_instance_authorization_id IN (` + placeholders(len(chunk)) + `) AND deleted_at IS NULL`
		rows, err := s.db.QueryContext(ctx, s.bind(q), stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id sql.NullString
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if id.Valid {
				active[id.String] = true
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return active, nil
}

func (s *Store) queryTeamSources(ctx context.Context, ids []string) ([]teamSourceRow, error) {
	var out []teamSourceRow
	for _, chunk := range chunks(unique(ids), 900) {
		if len(chunk) == 0 {
			continue
		}
		q := `SELECT authorization_id,source_team_id FROM ` + s.table("resource_authorization_sources") + ` WHERE authorization_id IN (` + placeholders(len(chunk)) + `) AND source_team_id IS NOT NULL`
		rows, err := s.db.QueryContext(ctx, s.bind(q), stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row teamSourceRow
			if err := rows.Scan(&row.AuthorizationID, &row.SourceTeamID); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, row)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthorizationID == out[j].AuthorizationID {
			return out[i].SourceTeamID < out[j].SourceTeamID
		}
		return out[i].AuthorizationID < out[j].AuthorizationID
	})
	return out, nil
}

func (s *Store) queryGrantIDs(ctx context.Context, accountIDs, authIDs []string, instance bool) ([]string, error) {
	var out []string
	if !instance {
		for _, chunk := range chunks(unique(accountIDs), 900) {
			if len(chunk) == 0 {
				continue
			}
			q := `SELECT id FROM ` + s.table("resource_authorization_grants") + ` WHERE resource_type='account' AND resource_id IN (` + placeholders(len(chunk)) + `)`
			ids, err := s.queryIDs(ctx, q, stringArgs(chunk)...)
			if err != nil {
				return nil, err
			}
			out = append(out, ids...)
		}
		return unique(out), nil
	}
	for _, chunk := range chunks(unique(authIDs), 900) {
		if len(chunk) == 0 {
			continue
		}
		q := `SELECT DISTINCT grants.id FROM ` + s.table("resource_authorization_grants") + ` grants
      INNER JOIN ` + s.table("resource_authorizations") + ` authorizations
        ON authorizations.resource_type=grants.resource_type AND authorizations.resource_id=grants.resource_id
        AND authorizations.resource_owner_system_account_id=grants.resource_owner_system_account_id
        AND grants.grantee_type='system_account' AND grants.grantee_system_account_id=authorizations.grantee_system_account_id
      INNER JOIN ` + s.table("resource_authorization_sources") + ` sources
        ON sources.authorization_id=authorizations.id AND sources.source_type='manual'
      WHERE authorizations.id IN (` + placeholders(len(chunk)) + `)`
		ids, err := s.queryIDs(ctx, q, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		out = append(out, ids...)
	}
	return unique(out), nil
}

func (s *Store) queryIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) deleteBusiness(ctx context.Context, c candidate, cutoff string) (businessDeleteResult, error) {
	result := businessDeleteResult{}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	accountIDs := unique(c.AccountIDs)
	authIDs := unique(c.AuthorizationIDs)
	grantIDs := unique(c.GrantIDs)
	if len(accountIDs) == 0 || strings.TrimSpace(c.row.ID) == "" {
		return result, ErrInvalidFence
	}

	// The candidate was selected by deleted_at <= cutoff. Re-read every
	// account row in the same transaction and require the exact tombstone
	// identity. This prevents a late retry from deleting a recreated/reused ID.
	for _, chunk := range chunks(accountIDs, 900) {
		q := `SELECT id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at FROM ` + s.table("accounts") + ` WHERE id IN (` + placeholders(len(chunk)) + `)`
		args := stringArgs(chunk)
		if s.mode == Postgres {
			q += " FOR UPDATE"
		}
		rows, e := tx.QueryContext(ctx, s.bind(q), args...)
		if e != nil {
			return result, e
		}
		found := map[string]struct{}{}
		for rows.Next() {
			var id, systemAccountID string
			var authID, sourceID, deletedAt, updatedAt sql.NullString
			if e = rows.Scan(&id, &systemAccountID, &authID, &sourceID, &deletedAt, &updatedAt); e != nil {
				rows.Close()
				return result, e
			}
			found[id] = struct{}{}
			if id == c.row.ID && (systemAccountID != c.row.SystemAccountID || !sameNullString(authID, c.row.AuthorizationInstanceAuthID) || !sameNullString(sourceID, c.row.AuthorizationInstanceSourceID) || !deletedAt.Valid || deletedAt.String > cutoff || deletedAt.String != c.row.DeletedAt.String || updatedAt.String != c.row.UpdatedAt.String) {
				rows.Close()
				return result, ErrCAS
			}
			for _, related := range c.RelatedRows {
				if related.ID == id && (systemAccountID != related.SystemAccountID || !sameNullString(authID, related.AuthorizationInstanceAuthID) || !sourceID.Valid || sourceID.String != c.row.ID || !deletedAt.Valid || deletedAt.String > cutoff || deletedAt.String != related.DeletedAt.String || updatedAt.String != related.UpdatedAt.String) {
					rows.Close()
					return result, ErrCAS
				}
			}
		}
		if e = rows.Close(); e != nil {
			return result, e
		}
		if len(found) != len(chunk) {
			return result, ErrCAS
		}
	}

	for _, chunk := range chunks(accountIDs, 900) {
		args := stringArgs(chunk)
		result.GroupBindings, err = execChanged(ctx, tx, result.GroupBindings, s.bind(`DELETE FROM `+s.table("group_accounts")+` WHERE account_id IN (`+placeholders(len(chunk))+`)`), args...)
		if err != nil {
			return result, err
		}
		relationTables := []string{"account_supported_models", "account_model_mappings", "account_tag_bindings"}
		if s.mode == Postgres {
			relationTables = append(relationTables, "account_name_search_terms", "account_name_search_documents", "account_api_key_runtime_states")
		}
		for _, table := range relationTables {
			if _, err = tx.ExecContext(ctx, s.bind("DELETE FROM "+s.table(table)+" WHERE account_id IN ("+placeholders(len(chunk))+")"), args...); err != nil {
				return result, err
			}
		}
	}
	for _, chunk := range chunks(authIDs, 900) {
		args := stringArgs(chunk)
		result.GroupBindings, err = execChanged(ctx, tx, result.GroupBindings, s.bind(`DELETE FROM `+s.table("group_accounts")+` WHERE account_authorization_id IN (`+placeholders(len(chunk))+`)`), args...)
		if err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, s.bind("DELETE FROM "+s.table("resource_authorization_sources")+" WHERE authorization_id IN ("+placeholders(len(chunk))+")"), args...); err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, s.bind("DELETE FROM "+s.table("request_quota_hourly_window_scope_bindings")+" WHERE scope_type IN ('account_authorization','group_authorization') AND scope_id IN ("+placeholders(len(chunk))+")"), args...); err != nil {
			return result, err
		}
	}
	for _, chunk := range chunks(grantIDs, 900) {
		args := stringArgs(chunk)
		if _, err = tx.ExecContext(ctx, s.bind("DELETE FROM "+s.table("request_quota_hourly_window_scope_bindings")+" WHERE source_type='resource_authorization_grant' AND source_id IN ("+placeholders(len(chunk))+")"), args...); err != nil {
			return result, err
		}
		result.Grants, err = execChanged(ctx, tx, result.Grants, s.bind("DELETE FROM "+s.table("resource_authorization_grants")+" WHERE id IN ("+placeholders(len(chunk))+")"), args...)
		if err != nil {
			return result, err
		}
	}
	related := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		if id != c.row.ID {
			related = append(related, id)
		}
	}
	for _, chunk := range chunks(related, 900) {
		before := result.Accounts
		result.Accounts, err = execChanged(ctx, tx, result.Accounts, s.bind("DELETE FROM "+s.table("accounts")+" WHERE id IN ("+placeholders(len(chunk))+")"), stringArgs(chunk)...)
		if err != nil {
			return result, err
		}
		if result.Accounts-before != len(chunk) {
			return result, ErrCAS
		}
	}
	before := result.Accounts
	result.Accounts, err = execChanged(ctx, tx, result.Accounts, s.bind("DELETE FROM "+s.table("accounts")+" WHERE id=? AND system_account_id=? AND deleted_at=? AND updated_at=?"), c.row.ID, c.row.SystemAccountID, c.row.DeletedAt.String, c.row.UpdatedAt.String)
	if err != nil {
		return result, err
	}
	if result.Accounts-before != 1 {
		return result, ErrCAS
	}
	for _, chunk := range chunks(authIDs, 900) {
		result.Authorizations, err = execChanged(ctx, tx, result.Authorizations, s.bind("DELETE FROM "+s.table("resource_authorizations")+" WHERE id IN ("+placeholders(len(chunk))+")"), stringArgs(chunk)...)
		if err != nil {
			return result, err
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func addChanged(total int, res sql.Result, err error) (int, error) {
	if err != nil {
		return total, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return total, err
	}
	return total + int(n), nil
}

func execChanged(ctx context.Context, tx *sql.Tx, total int, query string, args ...any) (int, error) {
	res, err := tx.ExecContext(ctx, query, args...)
	return addChanged(total, res, err)
}

func unique(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func mapKeys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

func chunks(values []string, size int) [][]string {
	if size <= 0 {
		size = 900
	}
	var out [][]string
	for len(values) > 0 {
		n := size
		if n > len(values) {
			n = len(values)
		}
		out = append(out, values[:n])
		values = values[n:]
	}
	return out
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func stringArgs(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func sameNullString(left, right sql.NullString) bool {
	if left.Valid != right.Valid {
		return false
	}
	return !left.Valid || left.String == right.String
}

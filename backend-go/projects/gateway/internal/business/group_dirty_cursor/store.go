// Package groupdirtycursor owns the Gateway Business group-account-stats dirty
// marker and full-refresh cursor transaction group. It deliberately contains
// no Node, IPC, queue, HTTP, or schema-creation dependency.
package groupdirtycursor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	GroupAccountStatsDirtyAll = "__all__"
	AllCursorPrefix           = "all_cursor:"
)

var (
	ErrOwnerGate       = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrDirtyRowMissing = errors.New("group account stats dirty row not found")
	ErrCursorRegressed = errors.New("group account stats all cursor cannot move backward")
	ErrCursorMalformed = errors.New("group account stats all cursor is malformed")
)

// ErrCursorRegression is retained as a descriptive alias for callers that
// name the monotonicity violation rather than the rejected operation.
var ErrCursorRegression = ErrCursorRegressed

// Mode selects the SQL dialect. Postgres uses the configured schema and
// numbered placeholders; SQLite uses unqualified table names and ? markers.
type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

// OwnerGate is external, auditable handoff evidence. A partial handoff never
// permits writes, even when the table exists.
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

func (g OwnerGate) Ready() bool {
	return g.Confirmed && g.SchemaReady && g.NodeWriterStopped
}

// DirtyRow is the replay identity used by the Node delete operation. Reason is
// retained for callers that loaded a full row, but deletion is intentionally
// fenced by only (group_id, updated_at), matching Node's CAS-like delete.
type DirtyRow struct {
	GroupID   string
	Reason    string
	UpdatedAt string
}

// GroupAccountStatsDirtyRow is the manifest/domain-shaped spelling retained
// for callers that mirror the Node repository row name.
type GroupAccountStatsDirtyRow = DirtyRow

// DirtyStateWriter is the narrow transaction-group port used by a future
// refresh worker. Implementations must keep each operation in a short DB
// transaction and must not bridge to Node or an IPC/queue transport.
type DirtyStateWriter interface {
	MarkAllDirty(context.Context, string) error
	DeleteRows(context.Context, []DirtyRow) (int64, error)
	UpdateAllCursor(context.Context, string) error
}

type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	now    func() time.Time
}

// New constructs the isolated transaction-group store. It never creates or
// alters schema; callers must establish SchemaReady from an external check.
func New(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Store, error) {
	if db == nil {
		return nil, errors.New("group dirty cursor database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, errors.New("group dirty cursor database mode is invalid")
	}
	return &Store{
		db:     db,
		mode:   mode,
		schema: strings.TrimSpace(schema),
		gate:   gate,
		now:    time.Now,
	}, nil
}

// NewStore is an explicit alias for callers that prefer constructor names
// which identify the returned value as a store.
func NewStore(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Store, error) {
	return New(db, mode, schema, gate)
}

func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

func (s *Store) table(name string) string {
	if s.mode == Postgres && s.schema != "" {
		return s.schema + "." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if s.mode != Postgres {
		return query
	}
	var b strings.Builder
	index := 0
	for _, r := range query {
		if r == '?' {
			index++
			fmt.Fprintf(&b, "$%d", index)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) dirtyTable() string {
	return s.table("group_account_stats_dirty")
}

func (s *Store) stamp() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

// CheckContract verifies the pre-existing dirty-marker relation. Runtime
// schema creation is forbidden so a missing column remains visible.
func (s *Store) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	_, err := s.db.ExecContext(ctx, "SELECT group_id,reason,updated_at FROM "+s.dirtyTable()+" LIMIT 0")
	if err != nil {
		return fmt.Errorf("verify group_account_stats_dirty relation: %w", err)
	}
	return nil
}

// MarkAll implements mark_all_group_account_stats_dirty. One transaction and
// one timestamp cover the whole upsert, making repeat marker calls deterministic
// while preserving the single __all__ row boundary.
func (s *Store) MarkAll(ctx context.Context, reason string) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "write"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.bind("INSERT INTO "+s.dirtyTable()+" (group_id,reason,updated_at) VALUES (?,?,?) ON CONFLICT(group_id) DO UPDATE SET reason=excluded.reason,updated_at=excluded.updated_at"), GroupAccountStatsDirtyAll, reason, s.stamp())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// MarkAllGroupAccountStatsDirty is the manifest-shaped method name.
func (s *Store) MarkAllGroupAccountStatsDirty(ctx context.Context, reason string) error {
	return s.MarkAll(ctx, reason)
}

// MarkAllDirty is the narrow writer-port name used by the refresh worker.
func (s *Store) MarkAllDirty(ctx context.Context, reason string) error {
	return s.MarkAll(ctx, reason)
}

// DeleteRows implements delete_group_account_stats_dirty_rows. It deletes by
// group_id plus the observed updated_at, so stale rows are harmless and repeat
// deletes are deterministic no-ops. A malformed batch rolls back the entire
// transaction rather than partially applying earlier rows.
func (s *Store) DeleteRows(ctx context.Context, rows []DirtyRow) (int64, error) {
	if err := s.requireOwner(); err != nil {
		return 0, err
	}
	for _, row := range rows {
		if strings.TrimSpace(row.GroupID) == "" || strings.TrimSpace(row.UpdatedAt) == "" {
			return 0, errors.New("dirty row group_id and updated_at are required")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, s.bind("DELETE FROM "+s.dirtyTable()+" WHERE group_id=? AND updated_at=?"))
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var deleted int64
	for _, row := range rows {
		result, execErr := stmt.ExecContext(ctx, strings.TrimSpace(row.GroupID), strings.TrimSpace(row.UpdatedAt))
		if execErr != nil {
			return 0, execErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return 0, countErr
		}
		deleted += count
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// DeleteGroupAccountStatsDirtyRows is the manifest-shaped method name.
func (s *Store) DeleteGroupAccountStatsDirtyRows(ctx context.Context, rows []DirtyRow) error {
	_, err := s.DeleteRows(ctx, rows)
	return err
}

// UpdateAllCursor implements update_group_account_stats_all_cursor. The
// existing row is locked in the transaction on Postgres. A candidate below
// the stored lexical group-id cursor fails closed; an equal candidate is an
// idempotent no-op. Non-cursor reasons are the initial marker state and may
// advance to the first cursor.
func (s *Store) UpdateAllCursor(ctx context.Context, cursorGroupID string) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	cursorGroupID = strings.TrimSpace(cursorGroupID)
	if cursorGroupID == "" {
		return ErrCursorMalformed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	selectQuery := "SELECT reason FROM " + s.dirtyTable() + " WHERE group_id=?"
	if s.mode == Postgres {
		selectQuery += " FOR UPDATE"
	}
	var reason sql.NullString
	err = tx.QueryRowContext(ctx, s.bind(selectQuery), GroupAccountStatsDirtyAll).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDirtyRowMissing
	}
	if err != nil {
		return err
	}
	if reason.Valid && strings.HasPrefix(reason.String, AllCursorPrefix) {
		current := strings.TrimPrefix(reason.String, AllCursorPrefix)
		if current == "" {
			return ErrCursorMalformed
		}
		var regressed int
		if err := tx.QueryRowContext(ctx, s.bind("SELECT CASE WHEN ? < ? THEN 1 ELSE 0 END"), cursorGroupID, current).Scan(&regressed); err != nil {
			return err
		}
		if regressed != 0 {
			return ErrCursorRegressed
		}
		var equal int
		if err := tx.QueryRowContext(ctx, s.bind("SELECT CASE WHEN ? = ? THEN 1 ELSE 0 END"), cursorGroupID, current).Scan(&equal); err != nil {
			return err
		}
		if equal != 0 {
			return tx.Commit()
		}
	}
	result, err := tx.ExecContext(ctx, s.bind("UPDATE "+s.dirtyTable()+" SET reason=?,updated_at=? WHERE group_id=?"), AllCursorPrefix+cursorGroupID, s.stamp(), GroupAccountStatsDirtyAll)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrDirtyRowMissing
	}
	return tx.Commit()
}

// UpdateGroupAccountStatsAllCursor is the manifest-shaped method name.
func (s *Store) UpdateGroupAccountStatsAllCursor(ctx context.Context, cursorGroupID string) error {
	return s.UpdateAllCursor(ctx, cursorGroupID)
}

// CoveredManifestOperations records the exact owner-manifest operations
// implemented by this isolated port. It does not mutate capability status.
var CoveredManifestOperations = []string{
	"mark_all_group_account_stats_dirty",
	"delete_group_account_stats_dirty_rows",
	"update_group_account_stats_all_cursor",
}

var _ DirtyStateWriter = (*Store)(nil)

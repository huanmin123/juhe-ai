// Package accounttesttask owns account_test_tasks lifecycle in Gateway.
// The implementation is deliberately storage-local: state transitions are
// CAS guarded and worker claims use a durable fence lease.
package accounttesttask

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
)

var (
	ErrOwnerGate = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrStaleCAS  = errors.New("account test task CAS is stale")
	ErrLeaseLost = errors.New("account test task lease is lost")
)

type OwnerGate struct{ Confirmed, SchemaReady, NodeWriterStopped bool }

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

type Store struct {
	db       *sql.DB
	postgres bool
	gate     OwnerGate
	ownerID  string
	now      func() time.Time
}

func New(db *sql.DB, postgres bool, gate OwnerGate, ownerID string) (*Store, error) {
	if db == nil || strings.TrimSpace(ownerID) == "" {
		return nil, errors.New("account test task database and owner are required")
	}
	return &Store{db: db, postgres: postgres, gate: gate, ownerID: ownerID, now: time.Now}, nil
}
func (s *Store) table(n string) string {
	if s.postgres {
		return "juhe_business." + n
	}
	return n
}
func (s *Store) bind(q string) string {
	if !s.postgres {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func (s *Store) requireWrite() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}
func (s *Store) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	tx, e := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SELECT id,account_id,status,status_message,result_json,cancel_requested,queued_at,started_at,finished_at,updated_at FROM "+s.table("account_test_tasks")+" LIMIT 0"); e != nil {
		return fmt.Errorf("account test task contract: %w", e)
	}
	if _, e = tx.ExecContext(ctx, "SELECT task_id,owner_id,fence_token,lease_until FROM "+s.table("account_test_task_leases")+" LIMIT 0"); e != nil {
		return fmt.Errorf("account test task lease contract: %w", e)
	}
	return tx.Commit()
}

type Task struct {
	ID, AccountID, Status, Message, ResultJSON string
	CancelRequested                            bool
	Revision                                   int64
	QueuedAt, StartedAt, FinishedAt, UpdatedAt string
}
type Lease struct {
	TaskID, OwnerID string
	Fence           int64
	Until           time.Time
}
type Result struct {
	Success bool
	Message string
	Data    any
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Store) Get(ctx context.Context, id string) (Task, error) {
	var t Task
	var cancel bool
	q := s.bind("SELECT id,account_id,status,COALESCE(status_message,''),COALESCE(result_json,''),cancel_requested,queued_at,COALESCE(started_at,''),COALESCE(finished_at,''),updated_at FROM " + s.table("account_test_tasks") + " WHERE id=?")
	e := s.db.QueryRowContext(ctx, q, id).Scan(&t.ID, &t.AccountID, &t.Status, &t.Message, &t.ResultJSON, &cancel, &t.QueuedAt, &t.StartedAt, &t.FinishedAt, &t.UpdatedAt)
	if e != nil {
		return Task{}, e
	}
	t.CancelRequested = cancel
	return t, nil
}
func (s *Store) Acquire(ctx context.Context, id string, lease time.Duration) (Lease, error) {
	if e := s.requireWrite(); e != nil {
		return Lease{}, e
	}
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	now := s.now().UTC()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Lease{}, e
	}
	defer tx.Rollback()
	var status string
	var cancel bool
	if e = tx.QueryRowContext(ctx, s.bind("SELECT status,cancel_requested FROM "+s.table("account_test_tasks")+" WHERE id=?"), id).Scan(&status, &cancel); e != nil {
		return Lease{}, e
	}
	if status != "queued" || cancel {
		return Lease{}, ErrStaleCAS
	}
	var fence int64
	var until sql.NullString
	errLease := tx.QueryRowContext(ctx, s.bind("SELECT fence_token,lease_until FROM "+s.table("account_test_task_leases")+" WHERE task_id=?"), id).Scan(&fence, &until)
	if errLease == nil && until.Valid {
		if t, _ := time.Parse(time.RFC3339Nano, until.String); t.After(now) {
			return Lease{}, ErrStaleCAS
		}
	}
	if errLease != nil && !errors.Is(errLease, sql.ErrNoRows) {
		return Lease{}, errLease
	}
	fence++
	q := "INSERT INTO " + s.table("account_test_task_leases") + " (task_id,owner_id,fence_token,lease_until,updated_at) VALUES (?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET owner_id=excluded.owner_id,fence_token=excluded.fence_token,lease_until=excluded.lease_until,updated_at=excluded.updated_at"
	if _, e = tx.ExecContext(ctx, s.bind(q), id, s.ownerID, fence, now.Add(lease).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); e != nil {
		return Lease{}, e
	}
	r, e := tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_tasks")+" SET status='running',status_message=?,started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND status='queued' AND cancel_requested=false"), "后台测试中", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id)
	if e != nil {
		return Lease{}, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return Lease{}, ErrStaleCAS
	}
	if e = tx.Commit(); e != nil {
		return Lease{}, e
	}
	return Lease{TaskID: id, OwnerID: s.ownerID, Fence: fence, Until: now.Add(lease)}, nil
}

func (s *Store) verifyLease(ctx context.Context, tx *sql.Tx, l Lease) error {
	var until string
	e := tx.QueryRowContext(ctx, s.bind("SELECT lease_until FROM "+s.table("account_test_task_leases")+" WHERE task_id=? AND owner_id=? AND fence_token=?"), l.TaskID, l.OwnerID, l.Fence).Scan(&until)
	if e != nil {
		return ErrLeaseLost
	}
	t, _ := time.Parse(time.RFC3339Nano, until)
	if !t.After(s.now().UTC()) {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) finish(ctx context.Context, l Lease, result Result, failed bool) error {
	if e := s.requireWrite(); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = s.verifyLease(ctx, tx, l); e != nil {
		if errors.Is(e, ErrLeaseLost) {
			status := "success"
			if failed || !result.Success {
				status = "failed"
			}
			var currentStatus, currentResult string
			readErr := tx.QueryRowContext(ctx, s.bind("SELECT status,COALESCE(result_json,'') FROM "+s.table("account_test_tasks")+" WHERE id=?"), l.TaskID).Scan(&currentStatus, &currentResult)
			if readErr == nil && currentStatus == status && currentResult == mustJSON(result) {
				return tx.Commit()
			}
		}
		return e
	}
	status := "success"
	if failed || !result.Success {
		status = "failed"
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	r, e := tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_tasks")+" SET status=?,status_message=?,result_json=?,error_message=?,finished_at=?,updated_at=? WHERE id=? AND status='running' AND cancel_requested=false"), status, result.Message, mustJSON(result), nullableError(result, failed), now, now, l.TaskID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		var canceled bool
		_ = tx.QueryRowContext(ctx, s.bind("SELECT cancel_requested FROM "+s.table("account_test_tasks")+" WHERE id=?"), l.TaskID).Scan(&canceled)
		if canceled {
			return ErrStaleCAS
		}
		return ErrStaleCAS
	}
	if _, e = tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_task_leases")+" SET lease_until=?,updated_at=? WHERE task_id=? AND owner_id=? AND fence_token=?"), now, now, l.TaskID, l.OwnerID, l.Fence); e != nil {
		return e
	}
	return tx.Commit()
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func nullableError(r Result, failed bool) any {
	if failed || !r.Success {
		return r.Message
	}
	return nil
}
func (s *Store) Complete(ctx context.Context, l Lease, r Result) error {
	return s.finish(ctx, l, r, false)
}
func (s *Store) Fail(ctx context.Context, l Lease, r Result) error { return s.finish(ctx, l, r, true) }
func (s *Store) Cancel(ctx context.Context, id, msg string) error {
	if e := s.requireWrite(); e != nil {
		return e
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	r, e := s.db.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_tasks")+" SET status='canceled',status_message=CASE WHEN cancel_requested=true AND NULLIF(status_message,'') IS NOT NULL THEN status_message ELSE ? END,cancel_requested=true,finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status IN ('queued','running')"), msg, now, now, id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrStaleCAS
	}
	return nil
}
func (s *Store) UpdateMessage(ctx context.Context, id, msg string) error {
	if e := s.requireWrite(); e != nil {
		return e
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return nil
	}
	r, e := s.db.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_tasks")+" SET status_message=?,updated_at=? WHERE id=? AND status='running' AND cancel_requested=false"), msg, s.now().UTC().Format(time.RFC3339Nano), id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrStaleCAS
	}
	return nil
}
func (s *Store) IsCancelRequested(ctx context.Context, id string) (bool, error) {
	var v bool
	e := s.db.QueryRowContext(ctx, s.bind("SELECT cancel_requested FROM "+s.table("account_test_tasks")+" WHERE id=?"), id).Scan(&v)
	return v, e
}
func (s *Store) CancelMessage(ctx context.Context, id string) (string, error) {
	var m string
	e := s.db.QueryRowContext(ctx, s.bind("SELECT COALESCE(NULLIF(status_message,''),'已停止测试') FROM "+s.table("account_test_tasks")+" WHERE id=?"), id).Scan(&m)
	return m, e
}
func (s *Store) Maintenance(ctx context.Context, action string, maxQueued time.Duration) error {
	if e := s.requireWrite(); e != nil {
		return e
	}
	if action != "start" && action != "sweep" {
		return errors.New("unknown account test task maintenance action")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if action == "start" {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_task_leases")+" SET lease_until=?,updated_at=? WHERE task_id IN (SELECT id FROM "+s.table("account_test_tasks")+" WHERE status='running')"), now, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_tasks")+" SET status='canceled',status_message=CASE WHEN NULLIF(status_message,'') IS NOT NULL THEN status_message ELSE '已停止测试' END,finished_at=COALESCE(finished_at,?),updated_at=? WHERE status='running' AND cancel_requested=true"), now, now); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_tasks")+" SET status='queued',status_message='后台 worker 重启后重新排队',started_at=NULL,updated_at=? WHERE status='running' AND cancel_requested=false"), now)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	if action == "sweep" && maxQueued > 0 {
		cut := s.now().UTC().Add(-maxQueued).Format(time.RFC3339Nano)
		_, e := s.db.ExecContext(ctx, s.bind("UPDATE "+s.table("account_test_tasks")+" SET status='failed',status_message='排队超时',error_message='排队超时',finished_at=?,updated_at=? WHERE status='queued' AND cancel_requested=false AND queued_at<?"), now, now, cut)
		if e != nil {
			return e
		}
	}
	return nil
}

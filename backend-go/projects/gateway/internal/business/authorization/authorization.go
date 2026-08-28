// Package authorization contains the Gateway-owned authorization and
// availability transaction group. It does not call Node or any other process.
package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrOwnerGate = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrCAS       = errors.New("authorization state changed before commit")
)

type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

type OwnerGate struct{ Confirmed, SchemaReady, NodeWriterStopped bool }

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

// UsagePort deliberately receives only scope identifiers and limits. The
// Store remains the direct Business SQL owner, while usage aggregation can be
// a separate in-process Gateway component. A configured limit without this
// port fails closed rather than silently allowing traffic.
type UsagePort interface {
	Usage(context.Context, Scope) (Usage, error)
}
type Scope struct {
	AuthorizationID, ScopeType, Window string
	StartsAt, EndsAt                   time.Time
}
type Usage struct{ Requests int64 }
type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	now    func() time.Time
	usage  UsagePort
}
type QuotaRequest struct {
	GroupAuthorizationID string
	Accounts             []AccountScope
}
type AccountScope struct{ AccountID, AuthorizationID string }
type QuotaDecision struct {
	Allowed    bool
	Message    string
	Violations []string
}
type ExpireResult struct{ GrantsExpired, AuthorizationsExpired, BindingsRemoved int64 }
type ScheduleEvaluator interface {
	Evaluate(context.Context, string, time.Time) (ScheduleDecision, error)
}
type ScheduleDecision struct {
	NextCheckAt      *time.Time
	EventKey, Status string
}
type AvailabilityResult struct{ Scanned, Activated, Disabled, Unchanged, Invalid, Skipped int }

func New(db *sql.DB, mode Mode, schema string, gate OwnerGate, usage UsagePort) (*Store, error) {
	if db == nil {
		return nil, errors.New("authorization database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, errors.New("authorization database mode is invalid")
	}
	return &Store{db: db, mode: mode, schema: strings.TrimSpace(schema), gate: gate, now: time.Now, usage: usage}, nil
}
func (s *Store) table(n string) string {
	if s.mode == Postgres && s.schema != "" {
		return s.schema + "." + n
	}
	return n
}
func (s *Store) bind(q string) string {
	if s.mode != Postgres {
		return q
	}
	n := 1
	var b strings.Builder
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
func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

// CheckQuotaBatch evaluates the exact four-owner-manifest read operations
// against current authorization rows. Expired/revoked rows are ignored; an
// enabled quota with no usage owner denies, because allowing would hide a
// migration omission.
func (s *Store) CheckQuotaBatch(ctx context.Context, in QuotaRequest) ([]QuotaDecision, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := make([]QuotaDecision, len(in.Accounts))
	for i, a := range in.Accounts {
		ids := unique(in.GroupAuthorizationID, a.AuthorizationID)
		d := QuotaDecision{Allowed: true}
		for _, id := range ids {
			limits, active, err := s.authorizationLimits(ctx, tx, id, now)
			if err != nil {
				return nil, err
			}
			if !active {
				continue
			}
			ok, msg, err := s.evaluate(ctx, id, limits, now)
			if err != nil {
				return nil, err
			}
			if !ok {
				d.Allowed = false
				d.Message = "authorization quota exceeded"
				d.Violations = append(d.Violations, msg)
			}
		}
		result[i] = d
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *Store) CheckQuota(ctx context.Context, groupAuthorizationID, accountAuthorizationID string) (QuotaDecision, error) {
	ds, e := s.CheckQuotaBatch(ctx, QuotaRequest{GroupAuthorizationID: groupAuthorizationID, Accounts: []AccountScope{{AuthorizationID: accountAuthorizationID}}})
	if e != nil {
		return QuotaDecision{}, e
	}
	return ds[0], nil
}
func unique(values ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func (s *Store) authorizationLimits(ctx context.Context, tx *sql.Tx, id string, now time.Time) (string, bool, error) {
	var limits string
	var status string
	var expiry sql.NullString
	err := tx.QueryRowContext(ctx, s.bind("SELECT COALESCE(limits_json,''),status,expires_at FROM "+s.table("resource_authorizations")+" WHERE id=?"), id).Scan(&limits, &status, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return limits, status == "active" && (!expiry.Valid || expiry.String > now.Format(time.RFC3339Nano)), nil
}
func (s *Store) evaluate(ctx context.Context, id, raw string, now time.Time) (bool, string, error) {
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return true, "", nil
	}
	var limits struct {
		Hourly *struct {
			Requests int64 `json:"requests"`
		} `json:"hourly"`
		Daily *struct {
			Requests int64 `json:"requests"`
		} `json:"daily"`
	}
	if err := json.Unmarshal([]byte(raw), &limits); err != nil {
		return false, "", fmt.Errorf("invalid authorization limits_json: %w", err)
	}
	for _, w := range []struct {
		name  string
		hours int
		limit *struct {
			Requests int64 `json:"requests"`
		}
	}{{"hourly", 1, limits.Hourly}, {"daily", 24, limits.Daily}} {
		if w.limit == nil || w.limit.Requests <= 0 {
			continue
		}
		if s.usage == nil {
			return false, "", errors.New("authorization usage port is required for enabled quota")
		}
		end := now
		start := end.Add(-time.Duration(w.hours) * time.Hour)
		u, err := s.usage.Usage(ctx, Scope{AuthorizationID: id, ScopeType: "authorization", Window: w.name, StartsAt: start, EndsAt: end})
		if err != nil {
			return false, "", err
		}
		if u.Requests >= w.limit.Requests {
			return false, w.name, nil
		}
	}
	return true, "", nil
}

// ExpireDue atomically expires both grant records and effective authorization
// bindings. It removes dependent group bindings only after their authorization
// reaches terminal state, so a stale sweeper cannot delete a still-active row.
func (s *Store) ExpireDue(ctx context.Context, limit int) (ExpireResult, error) {
	if err := s.requireOwner(); err != nil {
		return ExpireResult{}, err
	}
	if limit <= 0 {
		limit = 1000000
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExpireResult{}, err
	}
	defer tx.Rollback()
	r := ExpireResult{}
	grants, err := tx.QueryContext(ctx, s.bind("SELECT id,updated_at FROM "+s.table("resource_authorization_grants")+" WHERE status IN ('active','paused') AND expires_at IS NOT NULL AND expires_at<=? ORDER BY expires_at,id LIMIT ?"), now, limit)
	if err != nil {
		return r, err
	}
	type grant struct{ id, updated string }
	var due []grant
	for grants.Next() {
		var g grant
		if err := grants.Scan(&g.id, &g.updated); err != nil {
			grants.Close()
			return r, err
		}
		due = append(due, g)
	}
	if err := grants.Close(); err != nil {
		return r, err
	}
	for _, g := range due {
		q := "UPDATE " + s.table("resource_authorization_grants") + " SET status='expired',revoked_at=COALESCE(revoked_at,?),updated_at=? WHERE id=? AND updated_at=? AND status IN ('active','paused')"
		x, e := tx.ExecContext(ctx, s.bind(q), now, now, g.id, g.updated)
		if e != nil {
			return r, e
		}
		n, _ := x.RowsAffected()
		if n != 1 {
			return r, ErrCAS
		}
		r.GrantsExpired += n
	}
	q := "UPDATE " + s.table("resource_authorizations") + " SET status='expired',revoked_at=COALESCE(revoked_at,?),revoked_reason=COALESCE(revoked_reason,'authorization_expired'),updated_at=? WHERE status='active' AND expires_at IS NOT NULL AND expires_at<=?"
	x, err := tx.ExecContext(ctx, s.bind(q), now, now, now)
	if err != nil {
		return r, err
	}
	r.AuthorizationsExpired, _ = x.RowsAffected()
	q = "DELETE FROM " + s.table("group_accounts") + " WHERE account_authorization_id IN (SELECT id FROM " + s.table("resource_authorizations") + " WHERE status IN ('expired','revoked','returned'))"
	x, err = tx.ExecContext(ctx, s.bind(q))
	if err != nil {
		return r, err
	}
	r.BindingsRemoved, _ = x.RowsAffected()
	q = "DELETE FROM " + s.table("group_authorization_settings") + " WHERE authorization_id IN (SELECT id FROM " + s.table("resource_authorizations") + " WHERE status IN ('expired','revoked','returned'))"
	if _, err = tx.ExecContext(ctx, s.bind(q)); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return r, nil
}

// SyncAvailability evaluates persisted schedules and applies each event using
// event-key idempotency plus an account updated_at CAS fence. Schedule parsing
// is injected to keep the SQL owner independent from transport/config syntax.
func (s *Store) SyncAvailability(ctx context.Context, evaluator ScheduleEvaluator, limit int) (AvailabilityResult, error) {
	if err := s.requireOwner(); err != nil {
		return AvailabilityResult{}, err
	}
	if evaluator == nil {
		return AvailabilityResult{}, errors.New("availability schedule evaluator is required")
	}
	if limit <= 0 {
		limit = 1000000
	}
	nowTime := s.now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AvailabilityResult{}, err
	}
	defer tx.Rollback()
	q := "SELECT id,status,availability_schedule_json,availability_schedule_next_check_at,updated_at FROM " + s.table("accounts") + " WHERE availability_schedule_json IS NOT NULL AND deleted_at IS NULL AND (availability_schedule_next_check_at IS NULL OR availability_schedule_next_check_at<=?) ORDER BY availability_schedule_next_check_at,id LIMIT ?"
	rows, err := tx.QueryContext(ctx, s.bind(q), now, limit)
	if err != nil {
		return AvailabilityResult{}, err
	}
	type row struct {
		id, status, json, updated string
		next                      sql.NullString
	}
	var all []row
	for rows.Next() {
		var v row
		if err := rows.Scan(&v.id, &v.status, &v.json, &v.next, &v.updated); err != nil {
			rows.Close()
			return AvailabilityResult{}, err
		}
		all = append(all, v)
	}
	if err := rows.Close(); err != nil {
		return AvailabilityResult{}, err
	}
	out := AvailabilityResult{Scanned: len(all)}
	for _, v := range all {
		d, e := evaluator.Evaluate(ctx, v.json, nowTime)
		invalid := e != nil
		if invalid {
			out.Invalid++
			d = ScheduleDecision{}
			if v.status == "active" {
				d.Status = "disabled"
			}
		}
		next := sqlNullTime(d.NextCheckAt)
		if d.Status != "active" && d.Status != "disabled" {
			d.Status = ""
		}
		if d.Status != "" && d.EventKey != "" {
			x, e := tx.ExecContext(ctx, s.bind("INSERT INTO "+s.table("account_schedule_status_events")+"(event_key,account_id,status,executed_at) VALUES(?,?,?,?) ON CONFLICT(event_key) DO NOTHING"), d.EventKey, v.id, d.Status, now)
			if e != nil {
				return out, e
			}
			n, _ := x.RowsAffected()
			if n == 0 {
				out.Skipped++
				if e = s.updateNext(ctx, tx, v.id, v.updated, next); e != nil {
					return out, e
				}
				continue
			}
		}
		if d.Status == "" || d.Status == v.status {
			out.Unchanged++
			if e = s.updateNext(ctx, tx, v.id, v.updated, next); e != nil {
				return out, e
			}
			continue
		}
		q = "UPDATE " + s.table("accounts") + " SET status=?,availability_schedule_next_check_at=?,updated_at=? WHERE id=? AND updated_at=? AND status=? AND deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM " + s.table("account_quality_enforcements") + " aqe WHERE aqe.account_id=" + s.table("accounts") + ".id AND aqe.state='active' AND aqe.action='disable')"
		x, e := tx.ExecContext(ctx, s.bind(q), d.Status, next, now, v.id, v.updated, v.status)
		if e != nil {
			return out, e
		}
		n, _ := x.RowsAffected()
		if n == 0 {
			return out, ErrCAS
		}
		if d.Status == "active" {
			out.Activated++
		} else {
			out.Disabled++
		}
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}
func sqlNullTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UTC().Format(time.RFC3339Nano)
}
func (s *Store) updateNext(ctx context.Context, tx *sql.Tx, id, expected string, next any) error {
	q := "UPDATE " + s.table("accounts") + " SET availability_schedule_next_check_at=? WHERE id=? AND updated_at=?"
	r, e := tx.ExecContext(ctx, s.bind(q), next, id, expected)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrCAS
	}
	return nil
}

// Port is the dependency boundary for future Gateway routing; it is entirely
// in-process and prevents handler code from obtaining a raw DB handle.
type Port interface {
	CheckQuota(context.Context, string, string) (QuotaDecision, error)
	CheckQuotaBatch(context.Context, QuotaRequest) ([]QuotaDecision, error)
	ExpireDue(context.Context, int) (ExpireResult, error)
	SyncAvailability(context.Context, ScheduleEvaluator, int) (AvailabilityResult, error)
}

var _ Port = (*Store)(nil)

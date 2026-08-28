package authorization

import (
	"context"
	"database/sql"
	"errors"
	_ "modernc.org/sqlite"
	"testing"
	"time"
)

type usage int64

func (u usage) Usage(context.Context, Scope) (Usage, error) { return Usage{Requests: int64(u)}, nil }

type evaluator struct {
	decision ScheduleDecision
	err      error
}

func (e evaluator) Evaluate(context.Context, string, time.Time) (ScheduleDecision, error) {
	return e.decision, e.err
}
func testStore(t *testing.T) (*Store, *sql.DB) {
	db, e := sql.Open("sqlite", "file:authz-"+t.Name()+"?mode=memory&cache=shared")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`CREATE TABLE resource_authorizations(id TEXT PRIMARY KEY,limits_json TEXT,status TEXT,expires_at TEXT,revoked_at TEXT,revoked_reason TEXT,updated_at TEXT);CREATE TABLE resource_authorization_grants(id TEXT PRIMARY KEY,status TEXT,expires_at TEXT,revoked_at TEXT,updated_at TEXT);CREATE TABLE group_accounts(account_authorization_id TEXT);CREATE TABLE group_authorization_settings(authorization_id TEXT);CREATE TABLE account_schedule_status_events(event_key TEXT PRIMARY KEY,account_id TEXT,status TEXT,executed_at TEXT);CREATE TABLE account_quality_enforcements(account_id TEXT,state TEXT,action TEXT);CREATE TABLE accounts(id TEXT PRIMARY KEY,status TEXT,availability_schedule_json TEXT,availability_schedule_next_check_at TEXT,updated_at TEXT,deleted_at TEXT);`)
	if e != nil {
		t.Fatal(e)
	}
	s, _ := New(db, SQLite, "", OwnerGate{true, true, true}, usage(0))
	s.now = func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
	return s, db
}
func TestQuotaFailsClosedWithoutUsageOwner(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO resource_authorizations VALUES('a','{"hourly":{"requests":1}}','active',NULL,NULL,NULL,'r')`)
	s.usage = nil
	if _, e := s.CheckQuota(context.Background(), "a", ""); e == nil {
		t.Fatal("enabled quota without usage owner must fail")
	}
}
func TestExpireCleansTerminalBindings(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO resource_authorizations VALUES('a','{}','active','2026-08-28T11:00:00Z',NULL,NULL,'r');INSERT INTO resource_authorization_grants VALUES('g','active','2026-08-28T11:00:00Z',NULL,'r');INSERT INTO group_accounts VALUES('a');INSERT INTO group_authorization_settings VALUES('a')`)
	r, e := s.ExpireDue(context.Background(), 10)
	if e != nil || r.GrantsExpired != 1 || r.AuthorizationsExpired != 1 || r.BindingsRemoved != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM group_authorization_settings`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("n=%d e=%v", n, e)
	}
}
func TestAvailabilityCASAndIdempotentEvent(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO accounts VALUES('a','active','{}',NULL,'r',NULL)`)
	next := s.now().Add(time.Hour)
	r, e := s.SyncAvailability(context.Background(), evaluator{decision: ScheduleDecision{NextCheckAt: &next, EventKey: "a:off", Status: "disabled"}}, 10)
	if e != nil || r.Disabled != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	_, e = s.SyncAvailability(context.Background(), evaluator{decision: ScheduleDecision{NextCheckAt: &next, EventKey: "a:off", Status: "disabled"}}, 10)
	if e != nil && !errors.Is(e, ErrCAS) {
		t.Fatal(e)
	}
}
func TestOwnerGate(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	s.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if _, e := s.ExpireDue(context.Background(), 1); !errors.Is(e, ErrOwnerGate) {
		t.Fatal(e)
	}
}
func TestPostgresBindingContract(t *testing.T) {
	s := &Store{mode: Postgres, schema: "juhe_business"}
	q := s.bind("SELECT * FROM " + s.table("resource_authorizations") + " WHERE id=? AND status=?")
	if q != "SELECT * FROM juhe_business.resource_authorizations WHERE id=$1 AND status=$2" {
		t.Fatal(q)
	}
}

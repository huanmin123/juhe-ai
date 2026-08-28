package accounttesttask

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func sqliteStore(t *testing.T, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE account_test_tasks (id TEXT PRIMARY KEY,account_id TEXT NOT NULL,status TEXT NOT NULL,status_message TEXT,result_json TEXT,error_message TEXT,cancel_requested INTEGER NOT NULL DEFAULT 0,queued_at TEXT NOT NULL,started_at TEXT,finished_at TEXT,updated_at TEXT NOT NULL)`,
		Schema,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	s, err := New(db, false, gate, "gateway-test")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	clock := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	return s, db
}

func seedTask(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO account_test_tasks(id,account_id,status,queued_at,updated_at) VALUES(?,?,?,?,?)`, id, "acct", status, now, now); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerGateAndContractFailClosed(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true})
	defer db.Close()
	if err := s.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), "missing", time.Minute); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("want owner gate, got %v", err)
	}
}

func TestAcquireCompleteAndStaleFenceReplay(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	defer db.Close()
	seedTask(t, db, "task-1", "queued")
	first, err := s.Acquire(context.Background(), "task-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fence != 1 {
		t.Fatalf("fence=%d", first.Fence)
	}
	if err := s.Complete(context.Background(), first, Result{Success: true, Message: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(context.Background(), first, Result{Success: true, Message: "ok"}); err != nil {
		t.Fatalf("exact completion replay err=%v", err)
	}
	if err := s.Complete(context.Background(), first, Result{Success: true, Message: "different"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("divergent replay err=%v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='task-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Fatalf("status=%s", status)
	}
}

func TestCancelWinsOverFinishAndMessageRead(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	defer db.Close()
	seedTask(t, db, "task-2", "queued")
	lease, err := s.Acquire(context.Background(), "task-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(context.Background(), "task-2", "用户取消"); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(context.Background(), lease, Result{Success: true}); !errors.Is(err, ErrStaleCAS) {
		t.Fatalf("cancel must win, err=%v", err)
	}
	canceled, err := s.IsCancelRequested(context.Background(), "task-2")
	if err != nil || !canceled {
		t.Fatalf("canceled=%v err=%v", canceled, err)
	}
	msg, err := s.CancelMessage(context.Background(), "task-2")
	if err != nil || msg != "用户取消" {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
}

func TestMaintenanceRequeuesOnlyNonCanceledRunning(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	defer db.Close()
	seedTask(t, db, "task-3", "running")
	seedTask(t, db, "task-4", "running")
	if _, err := db.Exec(`UPDATE account_test_tasks SET cancel_requested=1 WHERE id='task-4'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintenance(context.Background(), "start", 0); err != nil {
		t.Fatal(err)
	}
	var a, b string
	_ = db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='task-3'`).Scan(&a)
	_ = db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='task-4'`).Scan(&b)
	if a != "queued" || b != "canceled" {
		t.Fatalf("statuses non-canceled=%s canceled=%s", a, b)
	}
}

func TestPostgresSmokeWhenExplicitlyConfigured(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("JUHE_AI_GATEWAY_ACCOUNT_TEST_TASK_POSTGRES_URL"))
	if url == "" {
		t.Skip("未设置 JUHE_AI_GATEWAY_ACCOUNT_TEST_TASK_POSTGRES_URL")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	// No destructive DDL is issued by this smoke; callers must provision the
	// contract in an isolated database before opting in.
	if _, err := New(db, true, OwnerGate{}, "gateway-test"); err != nil {
		t.Fatal(err)
	}
}

package groupdirtycursor

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:group-dirty-cursor-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT NOT NULL); CREATE INDEX idx_group_account_stats_dirty_updated ON group_account_stats_dirty(updated_at)`)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db, SQLite, "", gate)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func TestOwnerGateBlocksAllWrites(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true})
	ctx := context.Background()
	if err := s.MarkAll(ctx, "blocked"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("mark err=%v", err)
	}
	if _, err := s.DeleteRows(ctx, []DirtyRow{{GroupID: "g1", UpdatedAt: "u1"}}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("delete err=%v", err)
	}
	if err := s.UpdateAllCursor(ctx, "g1"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("cursor err=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM group_account_stats_dirty`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestMarkerUpsertAndCursorMonotonicity(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	clock := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	s.now = func() time.Time { return clock }
	ctx := context.Background()
	if err := s.MarkAll(ctx, "initial_cache_build"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAll(ctx, "later_reason"); err != nil {
		t.Fatal(err)
	}
	var groupID, reason, updatedAt string
	if err := db.QueryRow(`SELECT group_id,reason,updated_at FROM group_account_stats_dirty`).Scan(&groupID, &reason, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if groupID != GroupAccountStatsDirtyAll || reason != "later_reason" || updatedAt != clock.Format(time.RFC3339Nano) {
		t.Fatalf("row=%s/%s/%s", groupID, reason, updatedAt)
	}
	clock = clock.Add(time.Second)
	if err := s.UpdateAllCursor(ctx, "g10"); err != nil {
		t.Fatal(err)
	}
	var cursorReason, cursorTime string
	if err := db.QueryRow(`SELECT reason,updated_at FROM group_account_stats_dirty WHERE group_id=?`, GroupAccountStatsDirtyAll).Scan(&cursorReason, &cursorTime); err != nil {
		t.Fatal(err)
	}
	if cursorReason != AllCursorPrefix+"g10" || cursorTime != clock.Format(time.RFC3339Nano) {
		t.Fatalf("cursor=%s/%s", cursorReason, cursorTime)
	}
	if err := s.UpdateAllCursor(ctx, "g10"); err != nil {
		t.Fatal(err)
	}
	var equalTime string
	if err := db.QueryRow(`SELECT updated_at FROM group_account_stats_dirty WHERE group_id=?`, GroupAccountStatsDirtyAll).Scan(&equalTime); err != nil {
		t.Fatal(err)
	}
	if equalTime != cursorTime {
		t.Fatalf("equal cursor changed timestamp: before=%s after=%s", cursorTime, equalTime)
	}
	if err := s.UpdateAllCursor(ctx, "g09"); !errors.Is(err, ErrCursorRegressed) {
		t.Fatalf("regression err=%v", err)
	}
	if err := db.QueryRow(`SELECT reason FROM group_account_stats_dirty WHERE group_id=?`, GroupAccountStatsDirtyAll).Scan(&cursorReason); err != nil {
		t.Fatal(err)
	}
	if cursorReason != AllCursorPrefix+"g10" {
		t.Fatalf("regression mutated reason=%s", cursorReason)
	}
}

func TestDeleteUsesUpdatedAtFenceAndIsIdempotent(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO group_account_stats_dirty(group_id,reason,updated_at) VALUES ('g1','r1','u1'),('g2','r2','u2')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	deleted, err := s.DeleteRows(ctx, []DirtyRow{{GroupID: "g1", UpdatedAt: "stale"}})
	if err != nil || deleted != 0 {
		t.Fatalf("stale deleted=%d err=%v", deleted, err)
	}
	deleted, err = s.DeleteRows(ctx, []DirtyRow{{GroupID: "g1", UpdatedAt: "u1"}, {GroupID: "g1", UpdatedAt: "u1"}})
	if err != nil || deleted != 1 {
		t.Fatalf("duplicate deleted=%d err=%v", deleted, err)
	}
	deleted, err = s.DeleteRows(ctx, []DirtyRow{{GroupID: "g1", UpdatedAt: "u1"}})
	if err != nil || deleted != 0 {
		t.Fatalf("repeat deleted=%d err=%v", deleted, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM group_account_stats_dirty`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("remaining=%d err=%v", count, err)
	}
}

func TestDeleteBatchValidationRollsBackBeforeWrite(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO group_account_stats_dirty(group_id,reason,updated_at) VALUES ('g1','r1','u1')`); err != nil {
		t.Fatal(err)
	}
	_, err := s.DeleteRows(context.Background(), []DirtyRow{{GroupID: "g1", UpdatedAt: "u1"}, {GroupID: "", UpdatedAt: "u2"}})
	if err == nil {
		t.Fatal("malformed batch must fail")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM group_account_stats_dirty`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
}

func TestCursorRequiresExistingMarkerAndRejectsMalformedState(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err := s.UpdateAllCursor(context.Background(), "g1"); !errors.Is(err, ErrDirtyRowMissing) {
		t.Fatalf("missing marker err=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO group_account_stats_dirty(group_id,reason,updated_at) VALUES ('__all__','all_cursor:','u1')`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAllCursor(context.Background(), "g1"); !errors.Is(err, ErrCursorMalformed) {
		t.Fatalf("malformed cursor err=%v", err)
	}
}

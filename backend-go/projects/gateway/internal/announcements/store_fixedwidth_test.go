package announcements

import (
	"context"
	"regexp"
	"testing"
	"time"
)

// Written instants must be fixed-width UTC milliseconds: updated_at is the
// lexicographic ORDER BY ... DESC key and time.RFC3339Nano drops trailing
// zeros, which misordered '...Z' rows against '...000Z' rows.
func TestAnnouncementTimestampsFixedWidthMillis(t *testing.T) {
	db := newAnnouncementsPGDatabase(t)
	// 夹具 schema 已由 pgkit_test.go 注入（juhe_business.announcements /
	// announcement_reads / system_accounts，列集与原 sqlite 夹具一致）。
	// Sub-ms clock fraction proves the fixed-width truncation.
	clock := time.Date(2026, 1, 10, 8, 30, 0, 123456789, time.UTC)
	store, err := NewStore(db, func() time.Time { return clock }, func(prefix string) string { return prefix + "_1" })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	title, content := "定宽", "正文"
	status := "published"
	receipt, err := store.Create(ctx, MutationInput{Title: &title, Content: &content, Status: &status}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if fixedWidthInstantPattern.MatchString(receipt.Revision) == false || receipt.Revision != "2026-01-10T08:30:00.123Z" {
		t.Fatalf("create revision not fixed-width ms: %q", receipt.Revision)
	}
	var createdAt, updatedAt string
	if err := db.QueryRow(fixtureBind(`SELECT created_at, updated_at FROM announcements WHERE id = ?`), receipt.ID).Scan(&createdAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if createdAt != receipt.Revision || updatedAt != receipt.Revision {
		t.Fatalf("stored columns not fixed-width: %q/%q", createdAt, updatedAt)
	}

	if _, err := store.MarkRead(ctx, "reader", []string{receipt.ID}); err != nil {
		t.Fatal(err)
	}
	var readAt string
	if err := db.QueryRow(`SELECT read_at FROM announcement_reads WHERE system_account_id = 'reader'`).Scan(&readAt); err != nil {
		t.Fatal(err)
	}
	if !fixedWidthInstantPattern.MatchString(readAt) {
		t.Fatalf("read_at not fixed-width ms: %q", readAt)
	}

	// Legacy variable-width revision still patches: the bumped revision is
	// fixed-width and strictly later in real time (08:29 legacy → 08:30.123).
	legacy := "2026-01-10T08:29:00Z"
	if _, err := db.Exec(fixtureBind(`INSERT INTO announcements (id, title, content, level, status, created_by, updated_by, created_at, updated_at)
		VALUES ('ann_legacy', '旧', '文', 'info', 'draft', 'actor', 'actor', ?, ?)`), legacy, legacy); err != nil {
		t.Fatal(err)
	}
	newTitle := "旧改"
	outcome, err := store.Patch(ctx, "ann_legacy", MutationInput{Title: &newTitle}, legacy, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if !fixedWidthInstantPattern.MatchString(outcome.Receipt.Revision) || outcome.Receipt.Revision != "2026-01-10T08:30:00.123Z" {
		t.Fatalf("legacy patch revision = %q", outcome.Receipt.Revision)
	}

	// Both rows now carry fixed-width stamps: DESC order follows real time
	// (the .123Z row outranks the .000Z legacy row it patched past).
	one, fifty := 1, 50
	list, err := store.ListPage(ctx, &one, &fifty)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Items[0].ID != receipt.ID || list.Items[1].ID != "ann_legacy" {
		t.Fatalf("order after fixed-width writes = %+v", list.Items)
	}
}

// fixedWidthInstantPattern pins the canonical UTC millisecond form.
var fixedWidthInstantPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

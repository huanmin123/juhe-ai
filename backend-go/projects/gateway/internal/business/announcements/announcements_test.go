package announcements

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func announcementDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:announcements-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, display_name TEXT)`,
		`CREATE TABLE announcements (id TEXT PRIMARY KEY,title TEXT NOT NULL,content TEXT NOT NULL,level TEXT NOT NULL,status TEXT NOT NULL,created_by TEXT NOT NULL,updated_by TEXT,published_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE announcement_reads (announcement_id TEXT NOT NULL,system_account_id TEXT NOT NULL,read_at TEXT NOT NULL,PRIMARY KEY (announcement_id,system_account_id))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO system_accounts(id,display_name) VALUES ('admin','Administrator'),('user','User')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func announcementStore(t *testing.T) *Store {
	db := announcementDB(t)
	store, err := NewStore(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	return store
}

func TestOwnerGateBlocksAllMutations(t *testing.T) {
	db := announcementDB(t)
	store, err := NewStore(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAnnouncement(context.Background(), CreateInput{Title: "x", Content: "y"}, "admin"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("create err=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM announcements`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestPublicListReadAndDetailArePublishedOnly(t *testing.T) {
	store := announcementStore(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO announcements(id,title,content,level,status,created_by,updated_by,published_at,created_at,updated_at) VALUES ('draft','Draft','hidden','info','draft','admin','admin',NULL,'2026-08-28T11:00:00.000Z','2026-08-28T11:00:00.000Z'),('old','Old','old','normal','published','admin','admin','2026-08-28T10:00:00.000Z','2026-08-28T09:00:00.000Z','2026-08-28T10:00:00.000Z'),('new','New','new','critical','published','admin','admin','2026-08-28T11:00:00.000Z','2026-08-28T10:30:00.000Z','2026-08-28T11:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListPublicAnnouncements(ctx, "user", 0)
	if err != nil || len(list) != 2 || list[0].ID != "new" || list[1].ID != "old" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if _, err := store.FindPublicAnnouncement(ctx, "draft"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft detail err=%v", err)
	}
	first, err := store.MarkPublicAnnouncementsRead(ctx, "user", []string{"new", "new", "draft", " old "})
	if err != nil || first.Count != 2 {
		t.Fatalf("first read=%+v err=%v", first, err)
	}
	second, err := store.MarkPublicAnnouncementsRead(ctx, "user", []string{"new", "old"})
	if err != nil || second.Count != 0 {
		t.Fatalf("second read=%+v err=%v", second, err)
	}
	list, err = store.ListPublicAnnouncements(ctx, "user", 30)
	if err != nil || list[0].ReadAt == nil || list[1].ReadAt == nil {
		t.Fatalf("read markers=%+v err=%v", list, err)
	}
}

func TestManagementLifecycleCASAndReadReset(t *testing.T) {
	store := announcementStore(t)
	ctx := context.Background()
	created, err := store.CreateAnnouncement(ctx, CreateInput{Title: " Draft ", Content: " body ", Level: LevelWarning}, "admin")
	if err != nil || created.After == nil || created.After.Title != "Draft" || created.After.Status != StatusDraft {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err := store.PatchAnnouncement(ctx, created.Receipt.ID, "admin", "stale", PatchInput{Title: stringPtr("new")}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale patch err=%v", err)
	}
	patched, err := store.PatchAnnouncement(ctx, created.Receipt.ID, "admin", created.Receipt.Revision, PatchInput{Content: stringPtr("updated")})
	if err != nil || patched == nil || !patched.Changed || patched.After.Content != "updated" {
		t.Fatalf("patched=%+v err=%v", patched, err)
	}
	read, err := store.MarkPublicAnnouncementsRead(ctx, "user", []string{created.Receipt.ID})
	if err != nil || read.Count != 0 {
		t.Fatalf("draft read=%+v err=%v", read, err)
	}
	published, err := store.PublishAnnouncement(ctx, created.Receipt.ID, "admin", patched.Receipt.Revision)
	if err != nil || published == nil || published.After.Status != StatusPublished || published.After.PublishedAt == nil {
		t.Fatalf("published=%+v err=%v", published, err)
	}
	if _, err := store.MarkPublicAnnouncementsRead(ctx, "user", []string{created.Receipt.ID}); err != nil {
		t.Fatal(err)
	}
	archived, err := store.UnpublishAnnouncement(ctx, created.Receipt.ID, "admin", published.Receipt.Revision)
	if err != nil || archived == nil || archived.After.Status != StatusArchived || archived.After.PublishedAt == nil || *archived.After.PublishedAt != *published.After.PublishedAt {
		t.Fatalf("archived=%+v err=%v", archived, err)
	}
	republished, err := store.PublishAnnouncement(ctx, created.Receipt.ID, "admin", archived.Receipt.Revision)
	if err != nil || republished == nil || republished.After.PublishedAt == nil || *republished.After.PublishedAt == *published.After.PublishedAt {
		t.Fatalf("republished=%+v err=%v", republished, err)
	}
	deleted, err := store.DeleteAnnouncement(ctx, created.Receipt.ID, republished.Receipt.Revision)
	if err != nil || !deleted.Deleted || deleted.Before == nil {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	if _, err := store.FindAnnouncementDetail(ctx, created.Receipt.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted detail err=%v", err)
	}
}

func TestAdminPaginationAndValidation(t *testing.T) {
	store := announcementStore(t)
	ctx := context.Background()
	if err := store.CheckContract(ctx); err != nil {
		t.Fatalf("contract check err=%v", err)
	}
	if _, err := store.CreateAnnouncement(ctx, CreateInput{Title: "x", Content: "y"}, "user"); err != nil {
		t.Fatalf("storage create accepts caller id: %v", err)
	}
	if _, err := store.CreateAnnouncement(ctx, CreateInput{Title: "z", Content: "q"}, "user"); err != nil {
		t.Fatalf("second storage create err=%v", err)
	}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListAnnouncements(ctx, Actor{SystemAccountID: "user", Role: "user"}, ListOptions{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin list err=%v", err)
	}
	if _, err := service.CreateAnnouncement(ctx, Actor{SystemAccountID: "admin", Role: "admin"}, CreateInput{Title: "", Content: "body"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty title err=%v", err)
	}
	if _, err := DecodeCreateInput(strings.NewReader(`{"title":"x","content":"y","unexpected":1}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown field err=%v", err)
	}
	if _, err := DecodeCreateInput(strings.NewReader(`{"title":"x","content":"y","status":""}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty enum err=%v", err)
	}
	if _, err := DecodePatchRequest(strings.NewReader(`{"expectedRevision":"r1","title":null}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("null optional field err=%v", err)
	}
	if _, err := DecodePatchRequest(strings.NewReader(`{"expectedRevision":"","title":"x"}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty expected revision err=%v", err)
	}
	if _, err := DecodePatchRequest(strings.NewReader(`{"expectedRevision":"r1","title":""}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty patch title err=%v", err)
	}
	if _, err := DecodePatchRequest(strings.NewReader(`{"expectedRevision":"r1"}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty patch err=%v", err)
	}
	decodedPatch, err := DecodePatchRequest(strings.NewReader(`{"expectedRevision":" r1 ","title":" trimmed "}`))
	if err != nil || decodedPatch.ExpectedRevision != "r1" || decodedPatch.Title == nil || *decodedPatch.Title != "trimmed" {
		t.Fatalf("normalized patch=%+v err=%v", decodedPatch, err)
	}
	if _, err := store.ListPublicAnnouncements(ctx, "user", 31); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid public limit err=%v", err)
	}
	tooMany := make([]string, PublicLimit+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("a-%d", i)
	}
	if _, err := store.MarkPublicAnnouncementsRead(ctx, "user", tooMany); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("too many read ids err=%v", err)
	}
	listed, err := service.ListAnnouncements(ctx, Actor{SystemAccountID: "admin", Role: "super_admin"}, ListOptions{Page: 1, PageSize: 1})
	if err != nil || len(listed.Items) != 1 || listed.Page != 1 || listed.PageSize != 1 || !listed.HasMore || listed.Total != 2 {
		t.Fatalf("admin list=%+v err=%v", listed, err)
	}
	decoded, err := DecodeCreateInput(strings.NewReader(`{"title":"first","title":"last","content":"y"}`))
	if err != nil || decoded.Title != "last" {
		t.Fatalf("duplicate key decoded=%+v err=%v", decoded, err)
	}
}

func TestPublishRollbackAndNoopPatch(t *testing.T) {
	store := announcementStore(t)
	ctx := context.Background()
	created, err := store.CreateAnnouncement(ctx, CreateInput{Title: "x", Content: "y"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	noOp, err := store.PatchAnnouncement(ctx, created.Receipt.ID, "admin", created.Receipt.Revision, PatchInput{Title: stringPtr("x")})
	if err != nil || noOp == nil || noOp.Changed || noOp.Receipt.Revision != created.Receipt.Revision {
		t.Fatalf("noop=%+v err=%v", noOp, err)
	}
	if _, err := store.db.Exec(`INSERT INTO announcement_reads(announcement_id,system_account_id,read_at) VALUES (?,?,?)`, created.Receipt.ID, "user", "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE announcement_reads`); err != nil {
		t.Fatal(err)
	}
	status := StatusPublished
	if _, err := store.PatchAnnouncement(ctx, created.Receipt.ID, "admin", created.Receipt.Revision, PatchInput{Status: &status}); err == nil {
		t.Fatal("missing reads relation must fail closed")
	}
	var storedStatus, storedRevision string
	if err := store.db.QueryRow(`SELECT status,updated_at FROM announcements WHERE id=?`, created.Receipt.ID).Scan(&storedStatus, &storedRevision); err != nil {
		t.Fatal(err)
	}
	if storedStatus != string(StatusDraft) || storedRevision != created.Receipt.Revision {
		t.Fatalf("failed publication was not rolled back status=%s revision=%s", storedStatus, storedRevision)
	}
}

func TestRevisionIsStrictlyIncreasingAtSameClock(t *testing.T) {
	store := announcementStore(t)
	a := store.stamp()
	b := store.stamp()
	if !(b > a) {
		t.Fatalf("stamps are not monotonic: %s then %s", a, b)
	}
	c, err := store.nextRevision(a)
	if err != nil || !(c > b) {
		t.Fatalf("next revision=%s b=%s err=%v", c, b, err)
	}
}

func TestTextValidationUsesUTF16Units(t *testing.T) {
	if _, err := normalizeText(strings.Repeat("😀", TitleMaxUTF16/2), "title", TitleMaxUTF16); err != nil {
		t.Fatalf("emoji at exact UTF-16 limit rejected: %v", err)
	}
	if _, err := normalizeText(strings.Repeat("😀", TitleMaxUTF16/2+1), "title", TitleMaxUTF16); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("emoji over UTF-16 limit err=%v", err)
	}
	if _, err := normalizeText(string([]byte{0xff}), "title", TitleMaxUTF16); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid UTF-8 err=%v", err)
	}
}

func TestPostgresQualificationAndPlaceholders(t *testing.T) {
	db := announcementDB(t)
	store, err := NewStore(db, Postgres, "tenant_schema", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	qualified := store.table("announcements")
	if qualified != "tenant_schema.announcements" {
		t.Fatalf("qualified=%q", qualified)
	}
	query := store.bind(`SELECT * FROM ` + qualified + ` WHERE id=? AND status=?`)
	if query != "SELECT * FROM tenant_schema.announcements WHERE id=$1 AND status=$2" {
		t.Fatalf("query=%q", query)
	}
	if _, err := NewStore(db, Postgres, "bad-name", OwnerGate{}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("invalid schema err=%v", err)
	}
}

type recordingAfterCommit struct {
	events []AfterCommitEvent
	err    error
}

func (r *recordingAfterCommit) AfterAnnouncementCommit(_ context.Context, event AfterCommitEvent) error {
	r.events = append(r.events, event)
	return r.err
}

func TestAfterCommitOnlyRunsAfterCommittedMutationAndNeverCarriesContent(t *testing.T) {
	store := announcementStore(t)
	recorder := &recordingAfterCommit{err: errors.New("sink unavailable")}
	service, err := NewService(store, recorder)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.CreateAnnouncement(context.Background(), Actor{SystemAccountID: "admin", Role: "admin"}, CreateInput{Title: "title", Content: "secret announcement body", Status: StatusPublished})
	if outcome.After == nil || !outcome.Changed || err == nil || !strings.Contains(err.Error(), "sink unavailable") {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if len(recorder.events) != 1 || recorder.events[0].PublicAction != "upsert" || recorder.events[0].AnnouncementID != outcome.Receipt.ID {
		t.Fatalf("events=%+v", recorder.events)
	}
	if strings.Contains(strings.ToLower(fmt.Sprint(recorder.events[0])), "secret announcement body") {
		t.Fatal("after-commit event must not carry announcement content")
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM announcements WHERE id=?`, outcome.Receipt.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("committed count=%d err=%v", count, err)
	}
}

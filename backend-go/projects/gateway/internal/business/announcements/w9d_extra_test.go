package announcements

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// w9d：补齐 SQL 中段失败臂、PG FOR UPDATE 分支与解码校验臂。
// 不可达登记：
//   - newID 的 rand.Read 失败臂（406-408）：crypto/rand 在本平台不返回错误。
//   - normalizeListOptions 的 maxPage<1 臂（1154-1156）：MaxPageSize=100 时
//     maxPage=(AdminWindowRows-1)/pageSize ≥ 10，恒不小于 1。
// ---------------------------------------------------------------------------

var w9dErrBoom = errors.New("w9d scripted failure")

func TestW9DClockAndActorArms(t *testing.T) {
	db := announcementDB(t)
	fixed := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	store, err := NewStoreWithClock(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, func() time.Time { return fixed })
	if err != nil {
		t.Fatal(err)
	}
	if !store.now().Equal(fixed) {
		t.Fatal("custom clock must be installed")
	}
	if _, err := NewStoreWithClock(db, SQLite, "", OwnerGate{}, nil); err != nil {
		t.Fatalf("nil clock must keep default: %v", err)
	}
	if err := (&Service{}).requireActor(Actor{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("empty service actor err=%v", err)
	}
}

func TestW9DDecodeArms(t *testing.T) {
	long := strings.Repeat("x", ContentMaxUTF16+1)
	// DecodeCreateInput：content 超限 / 多 JSON 值。
	if _, err := DecodeCreateInput(strings.NewReader(fmt.Sprintf(`{"title":"t","content":%q}`, long))); err == nil {
		t.Fatal("oversize content must fail")
	}
	if _, err := DecodeCreateInput(strings.NewReader(`{"title":"a"} {"x":1}`)); err == nil || !strings.Contains(err.Error(), "multiple JSON") {
		t.Fatalf("multiple values err=%v", err)
	}
	// DecodePatchRequest：非对象 / 未知字段 / content 超限 / 多 JSON 值。
	if _, err := DecodePatchRequest(strings.NewReader(`[1,2]`)); err == nil {
		t.Fatal("array payload must fail")
	}
	if _, err := DecodePatchRequest(strings.NewReader(`{"unknownField":1,"expectedRevision":"r","title":"t"}`)); err == nil {
		t.Fatal("unknown field must fail")
	}
	if _, err := DecodePatchRequest(strings.NewReader(fmt.Sprintf(`{"expectedRevision":"r","content":%q}`, long))); err == nil {
		t.Fatal("oversize patch content must fail")
	}
	if _, err := DecodeCreateInput(strings.NewReader(`{"title":"a"} garbage`)); err == nil {
		t.Fatal("trailing garbage must fail")
	}
	// normalizeCreate：content 超限。
	store := announcementStore(t)
	if _, err := store.CreateAnnouncement(context.Background(), CreateInput{Title: "t", Content: long}, "admin"); err == nil {
		t.Fatal("oversize create content must fail")
	}
}

// ---------------------------------------------------------------------------
// 脚本化 driver
// ---------------------------------------------------------------------------

type w9dStep struct {
	contains    string
	cols        []string
	rows        [][]driver.Value
	rowsErr     error
	eofErr      error
	execErr     error
	affected    int64
	affectedErr error
	beginErr    error
	commitErr   error
	noRows      bool
}

type w9dScript struct{ steps []w9dStep }

func (s *w9dScript) next(kind, query string) (*w9dStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w9d script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w9d step mismatch: want %q got %s: %s", step.contains, kind, query)
	}
	return step, nil
}

type w9dConn struct{ script *w9dScript }

func (c *w9dConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w9d: prepare unsupported")
}
func (c *w9dConn) Close() error { return nil }
func (c *w9dConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w9dTx{script: c.script}, nil
}
func (c *w9dConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	if step.noRows || (len(step.rows) == 0 && step.cols == nil) {
		return &w9dRows{cols: []string{"x"}}, nil
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"id"}
	}
	return &w9dRows{cols: cols, values: step.rows, eofErr: step.eofErr}, nil
}
func (c *w9dConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w9dResult{affected: step.affected, err: step.affectedErr}, nil
}

type w9dTx struct{ script *w9dScript }

func (t *w9dTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w9dTx) Rollback() error { return nil }

type w9dResult struct {
	affected int64
	err      error
}

func (r w9dResult) LastInsertId() (int64, error) { return 0, nil }
func (r w9dResult) RowsAffected() (int64, error) { return r.affected, r.err }

type w9dRows struct {
	cols   []string
	values [][]driver.Value
	eofErr error
	index  int
}

func (r *w9dRows) Columns() []string { return r.cols }
func (r *w9dRows) Close() error      { return nil }
func (r *w9dRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	if r.eofErr != nil {
		return r.eofErr
	}
	return io.EOF
}
func (r *w9dRows) Err() error { return nil }

type w9dDriver struct{ script *w9dScript }

func (d w9dDriver) Open(string) (driver.Conn, error) { return &w9dConn{script: d.script}, nil }

var w9dDriverSeq int64

func w9dOpen(t *testing.T, mode Mode, steps []w9dStep) *Store {
	t.Helper()
	name := fmt.Sprintf("w9d-ann-scripted-%d", atomic.AddInt64(&w9dDriverSeq, 1))
	sql.Register(name, w9dDriver{script: &w9dScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, mode, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

var w9dAdmin = Actor{SystemAccountID: "admin", Role: "admin"}

func TestW9DPublicReadScriptedArms(t *testing.T) {
	ctx := context.Background()
	// 公开列表：查询失败 / scan 失败。
	q := w9dOpen(t, SQLite, []w9dStep{{rowsErr: w9dErrBoom}})
	if _, err := q.ListPublicAnnouncements(ctx, "user", 10); err == nil {
		t.Fatal("public list query failure must fail")
	}
	scan := w9dOpen(t, SQLite, []w9dStep{{cols: []string{"a", "b", "c", "d", "e"}, rows: [][]driver.Value{{nil, "t", "info", "p", nil}}}})
	if _, err := scan.ListPublicAnnouncements(ctx, "user", 10); err == nil {
		t.Fatal("public list scan failure must fail")
	}
	// 公开详情：查询失败。
	find := w9dOpen(t, SQLite, []w9dStep{{rowsErr: w9dErrBoom}})
	if _, err := find.FindPublicAnnouncement(ctx, "a1"); err == nil {
		t.Fatal("public find failure must fail")
	}
	// 标记已读：begin / exec / rowsAffected / commit。
	markBegin := w9dOpen(t, SQLite, []w9dStep{{beginErr: w9dErrBoom}})
	if _, err := markBegin.MarkPublicAnnouncementsRead(ctx, "user", []string{"a1"}); err == nil {
		t.Fatal("mark begin failure must fail")
	}
	markExec := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {contains: "INSERT INTO announcement_reads", execErr: w9dErrBoom}})
	if _, err := markExec.MarkPublicAnnouncementsRead(ctx, "user", []string{"a1"}); err == nil {
		t.Fatal("mark exec failure must fail")
	}
	markAffected := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {contains: "INSERT INTO announcement_reads", affectedErr: w9dErrBoom}})
	if _, err := markAffected.MarkPublicAnnouncementsRead(ctx, "user", []string{"a1"}); err == nil {
		t.Fatal("mark rowsAffected failure must fail")
	}
	markCommit := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {contains: "INSERT INTO announcement_reads", affected: 1}, {commitErr: w9dErrBoom}})
	if _, err := markCommit.MarkPublicAnnouncementsRead(ctx, "user", []string{"a1"}); err == nil {
		t.Fatal("mark commit failure must fail")
	}
}

func TestW9DAdminListScriptedArms(t *testing.T) {
	ctx := context.Background()
	// 查询失败。
	q := w9dOpen(t, SQLite, []w9dStep{{rowsErr: w9dErrBoom}})
	if _, err := q.ListAnnouncements(ctx, ListOptions{}); err == nil {
		t.Fatal("admin list query failure must fail")
	}
	// scan 失败（nil → *string）。
	scan := w9dOpen(t, SQLite, []w9dStep{{cols: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, rows: [][]driver.Value{{nil, "t", "p", 0, "info", "draft", nil, nil, "r"}}}})
	if _, err := scan.ListAnnouncements(ctx, ListOptions{}); err == nil {
		t.Fatal("admin list scan failure must fail")
	}
	// rows.Err() 臂：driver 在 EOF 前返回非 EOF 错误。
	rowsErr := w9dOpen(t, SQLite, []w9dStep{{cols: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, eofErr: w9dErrBoom}})
	if _, err := rowsErr.ListAnnouncements(ctx, ListOptions{}); err == nil {
		t.Fatal("rows iteration failure must fail")
	}
	// 详情查询失败。
	find := w9dOpen(t, SQLite, []w9dStep{{rowsErr: w9dErrBoom}})
	if _, err := find.FindAnnouncement(ctx, "a1"); err == nil {
		t.Fatal("find failure must fail")
	}
	detail := w9dOpen(t, SQLite, []w9dStep{{rowsErr: w9dErrBoom}})
	if _, err := detail.FindAnnouncementDetail(ctx, "a1"); err == nil {
		t.Fatal("detail failure must fail")
	}
}

func TestW9DCreateScriptedArms(t *testing.T) {
	ctx := context.Background()
	in := CreateInput{Title: "t", Content: "c"}
	begin := w9dOpen(t, SQLite, []w9dStep{{beginErr: w9dErrBoom}})
	if _, err := begin.CreateAnnouncement(ctx, in, "admin"); err == nil {
		t.Fatal("create begin failure must fail")
	}
	exec := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {contains: "INSERT INTO announcements", execErr: w9dErrBoom}})
	if _, err := exec.CreateAnnouncement(ctx, in, "admin"); err == nil {
		t.Fatal("create exec failure must fail")
	}
	commit := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {contains: "INSERT INTO announcements", affected: 1}, {commitErr: w9dErrBoom}})
	if _, err := commit.CreateAnnouncement(ctx, in, "admin"); err == nil {
		t.Fatal("create commit failure must fail")
	}
}

var w9dMutationCols = []string{"id", "title", "content", "level", "status", "published_at", "updated_at"}

func w9dMutationRow(id string) []driver.Value {
	return []driver.Value{id, "t", "c", "info", "draft", nil, "2026-08-01T00:00:00Z"}
}

func TestW9DPatchScriptedArms(t *testing.T) {
	ctx := context.Background()
	newTitle := "t2"
	in := PatchInput{Title: &newTitle}
	// begin 失败。
	begin := w9dOpen(t, SQLite, []w9dStep{{beginErr: w9dErrBoom}})
	if _, err := begin.PatchAnnouncement(ctx, "a1", "admin", "rev", in); err == nil {
		t.Fatal("patch begin failure must fail")
	}
	// PG FOR UPDATE 分支 + 行缺失（NoRows → nil, nil）。
	pg := w9dOpen(t, Postgres, []w9dStep{{beginErr: nil}, {contains: "FOR UPDATE", noRows: true}})
	out, err := pg.PatchAnnouncement(ctx, "a1", "admin", "rev", in)
	if err != nil || out != nil {
		t.Fatalf("pg missing row out=%v err=%v", out, err)
	}
	// 行扫描失败。
	badScan := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {rowsErr: w9dErrBoom}})
	if _, err := badScan.PatchAnnouncement(ctx, "a1", "admin", "rev", in); err == nil {
		t.Fatal("patch scan failure must fail")
	}
	// 无变化（字段值与当前一致）→ commit 失败。
	sameTitle := "t"
	unchanged := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}}, {commitErr: w9dErrBoom}})
	if _, err := unchanged.PatchAnnouncement(ctx, "a1", "admin", "2026-08-01T00:00:00Z", PatchInput{Title: &sameTitle}); err == nil {
		t.Fatal("unchanged commit failure must fail")
	}
	// UPDATE 失败 / CAS / commit 失败。
	updateOK := func() w9dStep { return w9dStep{contains: "UPDATE announcements SET", affected: 1} }
	exec := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}}, {contains: "UPDATE announcements SET", execErr: w9dErrBoom}})
	if _, err := exec.PatchAnnouncement(ctx, "a1", "admin", "2026-08-01T00:00:00Z", in); err == nil {
		t.Fatal("patch exec failure must fail")
	}
	cas := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}}, {contains: "UPDATE announcements SET", affected: 0}})
	if _, err := cas.PatchAnnouncement(ctx, "a1", "admin", "2026-08-01T00:00:00Z", in); err == nil {
		t.Fatal("patch cas must fail")
	}
	commit := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}}, updateOK(), {commitErr: w9dErrBoom}})
	if _, err := commit.PatchAnnouncement(ctx, "a1", "admin", "2026-08-01T00:00:00Z", in); err == nil {
		t.Fatal("patch commit failure must fail")
	}
	// 发布切换路径：DELETE reads 失败臂。
	publishStatus := Status("published")
	published := w9dOpen(t, SQLite, []w9dStep{
		{beginErr: nil},
		{cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}},
		updateOK(),
		{contains: "DELETE FROM announcement_reads", execErr: w9dErrBoom},
	})
	if _, err := published.PatchAnnouncement(ctx, "a1", "admin", "2026-08-01T00:00:00Z", PatchInput{Status: &publishStatus}); err == nil {
		t.Fatal("publish reads delete failure must fail")
	}
}

func TestW9DDeleteScriptedArms(t *testing.T) {
	ctx := context.Background()
	rev := "2026-08-01T00:00:00Z"
	begin := w9dOpen(t, SQLite, []w9dStep{{beginErr: w9dErrBoom}})
	if _, err := begin.DeleteAnnouncement(ctx, "a1", rev); err == nil {
		t.Fatal("delete begin failure must fail")
	}
	// PG FOR UPDATE 分支（行缺失 → 幂等返回）。
	pg := w9dOpen(t, Postgres, []w9dStep{{beginErr: nil}, {contains: "FOR UPDATE", noRows: true}})
	deleted, err := pg.DeleteAnnouncement(ctx, "a1", rev)
	if err != nil || deleted.Deleted {
		t.Fatalf("pg missing row deleted=%+v err=%v", deleted, err)
	}
	scan := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {rowsErr: w9dErrBoom}})
	if _, err := scan.DeleteAnnouncement(ctx, "a1", rev); err == nil {
		t.Fatal("delete scan failure must fail")
	}
	reads := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}}, {contains: "DELETE FROM announcement_reads", execErr: w9dErrBoom}})
	if _, err := reads.DeleteAnnouncement(ctx, "a1", rev); err == nil {
		t.Fatal("delete reads failure must fail")
	}
	delExec := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}}, {contains: "DELETE FROM announcement_reads", affected: 1}, {contains: "DELETE FROM announcements WHERE", execErr: w9dErrBoom}})
	if _, err := delExec.DeleteAnnouncement(ctx, "a1", rev); err == nil {
		t.Fatal("delete exec failure must fail")
	}
	delCAS := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}}, {contains: "DELETE FROM announcement_reads", affected: 1}, {contains: "DELETE FROM announcements WHERE", affected: 0}})
	if _, err := delCAS.DeleteAnnouncement(ctx, "a1", rev); err == nil {
		t.Fatal("delete cas must fail")
	}
	delCommit := w9dOpen(t, SQLite, []w9dStep{{beginErr: nil}, {cols: w9dMutationCols, rows: [][]driver.Value{w9dMutationRow("a1")}}, {contains: "DELETE FROM announcement_reads", affected: 1}, {contains: "DELETE FROM announcements WHERE", affected: 1}, {commitErr: w9dErrBoom}})
	if _, err := delCommit.DeleteAnnouncement(ctx, "a1", rev); err == nil {
		t.Fatal("delete commit failure must fail")
	}
}

func TestW9DNextRevisionRejectsBadRevision(t *testing.T) {
	store := announcementStore(t)
	if _, err := store.nextRevisionForTest("not-a-time"); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("bad revision err=%v", err)
	}
	// 经由 Patch 触发同一守卫：手工插入坏 revision 行。
	db := store.db
	if _, err := db.Exec(`INSERT INTO announcements (id,title,content,level,status,created_by,updated_by,published_at,created_at,updated_at) VALUES ('bad','t','c','info','draft','admin','admin',NULL,'2026-01-01T00:00:00Z','garbage')`); err != nil {
		t.Fatal(err)
	}
	// 对坏 revision 行做修改型 patch → nextRevision 守卫拒绝。
	newTitle := "t2"
	if _, err := store.PatchAnnouncement(context.Background(), "bad", "admin", "garbage", PatchInput{Title: &newTitle}); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("bad revision patch err=%v", err)
	}
}

package modelcheckauth

// w7d（modelcheckauth 覆盖补齐）：构造器守卫、token 解析边界、时间解析分支、
// Postgres 专有路径（表前缀、$n 绑定改写、has_table_privilege 契约、
// last_seen touch）与只读契约错误臂。PG 语义用脚本化 database/sql driver
// 进程内模拟；SQLite 臂使用内存库。不连接真实数据库。

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
)

// ---------------------------------------------------------------------------
// 脚本化 driver：按步骤次序应答（与 w7d_reader_arms_test.go 同手法）
// ---------------------------------------------------------------------------

var w7dAuthDriverSeq int64

type w7dAuthStep struct {
	contains   string
	cols       []string
	row        []driver.Value
	execErr    error
	queryErr   error
	commitErr  error
	rowsEOFErr error
}

type w7dAuthScript struct{ steps []w7dAuthStep }

func (s *w7dAuthScript) next(kind, query string) (*w7dAuthStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w7d auth scripted 脚本在 %s 处耗尽: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w7d auth scripted 步骤不匹配: want %q got %s %s", step.contains, kind, query)
	}
	return step, nil
}

type w7dAuthConn struct{ script *w7dAuthScript }

func (c *w7dAuthConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w7d auth: Prepare unsupported")
}
func (c *w7dAuthConn) Close() error              { return nil }
func (c *w7dAuthConn) Begin() (driver.Tx, error) { return &w7dAuthTx{script: c.script}, nil }

// BeginTx 接受 ReadOnly+RepeatableRead（模拟 PG 驱动）。
func (c *w7dAuthConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c *w7dAuthConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.queryErr != nil {
		return nil, step.queryErr
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"permitted"}
	}
	rows := &w7dAuthRows{cols: cols, eofErr: step.rowsEOFErr}
	if step.row != nil {
		rows.values = [][]driver.Value{step.row}
	}
	return rows, nil
}
func (c *w7dAuthConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return driver.RowsAffected(1), nil
}

type w7dAuthTx struct{ script *w7dAuthScript }

func (t *w7dAuthTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w7dAuthTx) Rollback() error { return nil }

type w7dAuthRows struct {
	cols   []string
	values [][]driver.Value
	eofErr error
	index  int
}

func (r *w7dAuthRows) Columns() []string { return r.cols }
func (r *w7dAuthRows) Close() error      { return nil }
func (r *w7dAuthRows) Err() error        { return nil }
func (r *w7dAuthRows) Next(dest []driver.Value) error {
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

type w7dAuthDriver struct{ script *w7dAuthScript }

func (d w7dAuthDriver) Open(string) (driver.Conn, error) {
	return &w7dAuthConn{script: d.script}, nil
}

func w7dOpenAuthDB(t *testing.T, steps []w7dAuthStep) *sql.DB {
	t.Helper()
	name := "w7d-auth-scripted-" + fmt.Sprint(atomic.AddInt64(&w7dAuthDriverSeq, 1))
	sql.Register(name, w7dAuthDriver{script: &w7dAuthScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// 构造器与解析守卫
// ---------------------------------------------------------------------------

func TestW7DNewRejectsNilDBAndUnknownMode(t *testing.T) {
	if _, err := New(nil, SQLite, nil); err == nil {
		t.Fatal("nil db 必须拒绝")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := New(db, Mode(9), nil); err == nil {
		t.Fatal("未知 mode 必须拒绝")
	}
	auth, err := New(db, SQLite, nil)
	if err != nil || auth == nil {
		t.Fatalf("nil now 必须回落 time.Now: %v", err)
	}
}

func TestW7DUninitializedAuthenticatorFailsClosed(t *testing.T) {
	var nilAuth *Authenticator
	if err := nilAuth.CheckContract(context.Background()); err == nil {
		t.Fatal("nil authenticator CheckContract 必须失败")
	}
	if _, err := nilAuth.Authenticate(context.Background(), "", ""); err == nil {
		t.Fatal("nil authenticator Authenticate 必须失败")
	}
	if _, err := nilAuth.AuthenticateToken(context.Background(), "x"); err == nil {
		t.Fatal("nil authenticator AuthenticateToken 必须失败")
	}
	if _, err := nilAuth.RequireAdmin(context.Background(), "", ""); err == nil {
		t.Fatal("nil authenticator RequireAdmin 必须失败")
	}
	if _, err := nilAuth.RequireAdminToken(context.Background(), "x"); err == nil {
		t.Fatal("nil authenticator RequireAdminToken 必须失败")
	}
	// table/bind 的 nil receiver 在生产路径被前置守卫拦截，这里不直呼；
	// 两种 mode 的分支由构造实例驱动。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sqliteAuth, _ := New(db, SQLite, nil)
	if got := sqliteAuth.table("system_sessions"); got != "system_sessions" {
		t.Fatalf("SQLite 表名不加前缀: %q", got)
	}
	postgresAuth, _ := New(db, Postgres, nil)
	if got := postgresAuth.table("system_sessions"); got != "juhe_business.system_sessions" {
		t.Fatalf("Postgres 表名必须带前缀: %q", got)
	}
	if got := sqliteAuth.bind("UPDATE t SET a=? WHERE b=?"); strings.Contains(got, "$") {
		t.Fatalf("SQLite 绑定必须保留 ?: %q", got)
	}
	if got := postgresAuth.bind("UPDATE t SET a=? WHERE b=?"); got != "UPDATE t SET a=$1 WHERE b=$2" {
		t.Fatalf("Postgres 绑定必须改写为 $n: %q", got)
	}
	timeValue := sqliteAuth.timeValue(time.Date(2026, 9, 14, 8, 0, 0, 123456789, time.UTC))
	if timeValue != "2026-09-14T08:00:00.123Z" {
		t.Fatalf("timeValue 必须截断到毫秒: %v", timeValue)
	}
}

func TestW7DResolveTokenBoundaries(t *testing.T) {
	if _, err := resolveToken("", "   "); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("空白 cookie 必须要求登录: %v", err)
	}
	// 非 Bearer 前缀 / 非 temporary 形态的令牌一律无效。
	if _, err := resolveToken("Basic abc", ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Basic 认证必须拒绝: %v", err)
	}
	if _, err := resolveToken("bearer tok", ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("小写 bearer 的非法令牌必须拒绝: %v", err)
	}
	temporary := "juhe_tmp_" + strings.Repeat("B", 43)
	if got, err := resolveToken("  BEARER  "+temporary+"  ", ""); err != nil || got != temporary {
		t.Fatalf("大小写不敏感 Bearer 必须 trim 后通过: got=%q err=%v", got, err)
	}
	// AuthenticateToken 空白 token 守卫。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth, _ := New(db, SQLite, nil)
	if _, err := auth.AuthenticateToken(context.Background(), "   "); err == nil {
		t.Fatal("空白 token 必须拒绝")
	}
}

func TestW7DParseTimeBranches(t *testing.T) {
	stamp := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	if got, err := parseTime(stamp); err != nil || !got.Equal(stamp) {
		t.Fatalf("time.Time 分支: %v %v", got, err)
	}
	if got, err := parseTime([]byte("2026-09-14T08:00:00Z")); err != nil || !got.Equal(stamp) {
		t.Fatalf("[]byte 分支: %v %v", got, err)
	}
	if _, err := parseTime(12345); err == nil {
		t.Fatal("不支持的类型必须报错")
	}
	if _, err := parseTime("not-a-time"); err == nil {
		t.Fatal("非法字符串必须报错")
	}
}

// ---------------------------------------------------------------------------
// 脚本化 Postgres 契约路径
// ---------------------------------------------------------------------------

func TestW7DPostgresCheckContractHappyPathAndPrivilege(t *testing.T) {
	db := w7dOpenAuthDB(t, []w7dAuthStep{
		{contains: "SELECT 1 FROM juhe_business.system_sessions LIMIT 0"},
		{contains: "SELECT 1 FROM juhe_business.system_accounts LIMIT 0"},
		{contains: "SELECT has_table_privilege(current_user, 'juhe_business.system_sessions', 'UPDATE')", cols: []string{"permitted"}, row: []driver.Value{"true"}},
		{},
	})
	auth, err := New(db, Postgres, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckContract(context.Background()); err != nil {
		t.Fatalf("PG 契约 happy path: %v", err)
	}

	// 权限缺失 fail-closed。
	db2 := w7dOpenAuthDB(t, []w7dAuthStep{
		{},
		{},
		{contains: "has_table_privilege", cols: []string{"permitted"}, row: []driver.Value{"false"}},
		{},
	})
	auth2, _ := New(db2, Postgres, nil)
	if err := auth2.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "management role lacks") {
		t.Fatalf("UPDATE 权限缺失必须阻止启动: %v", err)
	}
}

func TestW7DPostgresCheckContractErrorArms(t *testing.T) {
	// 关系校验失败。
	db := w7dOpenAuthDB(t, []w7dAuthStep{
		{execErr: errors.New("w7d: relation missing")},
	})
	auth, _ := New(db, Postgres, nil)
	if err := auth.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "juhe_business.system_sessions") {
		t.Fatalf("关系校验失败必须暴露表名: %v", err)
	}

	// 权限查询错误。
	db2 := w7dOpenAuthDB(t, []w7dAuthStep{
		{},
		{},
		{contains: "has_table_privilege", queryErr: errors.New("w7d: privilege query boom")},
	})
	auth2, _ := New(db2, Postgres, nil)
	if err := auth2.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "read management session update privilege") {
		t.Fatalf("权限查询错误必须暴露: %v", err)
	}

	// COMMIT 错误。
	db3 := w7dOpenAuthDB(t, []w7dAuthStep{
		{},
		{},
		{contains: "has_table_privilege", cols: []string{"permitted"}, row: []driver.Value{"true"}},
		{commitErr: errors.New("w7d: commit boom")},
	})
	auth3, _ := New(db3, Postgres, nil)
	if err := auth3.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "commit management auth contract") {
		t.Fatalf("契约事务 commit 错误必须暴露: %v", err)
	}
}

func TestW7DPostgresAuthenticateTokenBindsPlaceholders(t *testing.T) {
	now := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	// 命中会话 + last_seen 超过 1 分钟 → touch UPDATE 也走 $n 绑定。
	db := w7dOpenAuthDB(t, []w7dAuthStep{
		{contains: "FROM juhe_business.system_sessions ss INNER JOIN juhe_business.system_accounts sa ON sa.id=ss.system_account_id WHERE ss.token_hash=$1",
			cols: []string{"id", "expires_at", "last_seen_at", "sa.id", "username", "display_name", "role", "must_change_password"},
			row:  []driver.Value{"sess-1", "2026-09-14T09:00:00Z", "2026-09-14T07:00:00Z", "acc-1", "admin", "Admin", "admin", "false"}},
		{contains: "UPDATE juhe_business.system_sessions SET last_seen_at=$1 WHERE id=$2 AND last_seen_at<$3"},
	})
	auth, err := New(db, Postgres, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	actor, err := auth.AuthenticateToken(context.Background(), "juhe_tmp_"+strings.Repeat("C", 43))
	if err != nil {
		t.Fatal(err)
	}
	if actor.SessionID != "sess-1" || actor.SystemAccountID != "acc-1" {
		t.Fatalf("PG 会话投影不正确: %#v", actor)
	}
}

func TestW7DAuthenticateTokenErrorArmsOverScriptedDriver(t *testing.T) {
	token := "juhe_tmp_" + strings.Repeat("D", 43)
	now := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)

	// 非 NoRows 查询错误。
	db := w7dOpenAuthDB(t, []w7dAuthStep{
		{queryErr: errors.New("w7d: session query boom")},
	})
	auth, _ := New(db, SQLite, func() time.Time { return now })
	if _, err := auth.AuthenticateToken(context.Background(), token); err == nil || !strings.Contains(err.Error(), "read management session") {
		t.Fatalf("会话查询错误必须暴露: %v", err)
	}

	// 行迭代错误（expires 原始值非法路径不同：这里在 Scan 阶段失败）。
	db2 := w7dOpenAuthDB(t, []w7dAuthStep{
		{cols: []string{"a"}, rowsEOFErr: errors.New("w7d: rows boom")},
	})
	auth2, _ := New(db2, SQLite, func() time.Time { return now })
	if _, err := auth2.AuthenticateToken(context.Background(), token); err == nil || !strings.Contains(err.Error(), "read management session") {
		t.Fatalf("行迭代错误必须暴露: %v", err)
	}

	// expires 时间非法 → 会话过期。
	db3 := w7dOpenAuthDB(t, []w7dAuthStep{
		{cols: []string{"id", "expires_at", "last_seen_at", "sa.id", "username", "display_name", "role", "must_change_password"}, row: []driver.Value{"sess", "bad-time", "2026-09-14T07:00:00Z", "acc", "admin", "Admin", "admin", "false"}},
	})
	auth3, _ := New(db3, SQLite, func() time.Time { return now })
	if _, err := auth3.AuthenticateToken(context.Background(), token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("非法 expires 必须按过期处理: %v", err)
	}

	// last_seen 非法 → 会话过期（touch 不执行）。
	db4 := w7dOpenAuthDB(t, []w7dAuthStep{
		{cols: []string{"id", "expires_at", "last_seen_at", "sa.id", "username", "display_name", "role", "must_change_password"}, row: []driver.Value{"sess", "2026-09-14T09:00:00Z", "bad-seen", "acc", "admin", "Admin", "admin", "false"}},
	})
	auth4, _ := New(db4, SQLite, func() time.Time { return now })
	if _, err := auth4.AuthenticateToken(context.Background(), token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("非法 last_seen 必须按过期处理: %v", err)
	}

	// touch UPDATE 失败必须暴露。
	db5 := w7dOpenAuthDB(t, []w7dAuthStep{
		{cols: []string{"id", "expires_at", "last_seen_at", "sa.id", "username", "display_name", "role", "must_change_password"}, row: []driver.Value{"sess", "2026-09-14T09:00:00Z", "2026-09-14T07:00:00Z", "acc", "admin", "Admin", "admin", "false"}},
		{execErr: errors.New("w7d: touch boom")},
	})
	auth5, _ := New(db5, SQLite, func() time.Time { return now })
	if _, err := auth5.AuthenticateToken(context.Background(), token); err == nil || !strings.Contains(err.Error(), "touch management session") {
		t.Fatalf("touch 失败必须暴露: %v", err)
	}

	// touch 跳过边界（last_seen 不足 1 分钟）+ must_change → ErrMustChange。
	db6 := w7dOpenAuthDB(t, []w7dAuthStep{
		{cols: []string{"id", "expires_at", "last_seen_at", "sa.id", "username", "display_name", "role", "must_change_password"}, row: []driver.Value{"sess", "2026-09-14T09:00:00Z", "2026-09-14T07:59:30Z", "acc", "admin", "Admin", "admin", "true"}},
	})
	auth6, _ := New(db6, SQLite, func() time.Time { return now })
	if _, err := auth6.AuthenticateToken(context.Background(), token); !errors.Is(err, ErrMustChange) {
		t.Fatalf("1 分钟内的 last_seen 不得触发 touch: %v", err)
	}

	// RequireAdminToken 对非管理员角色拒绝。
	db7 := w7dOpenAuthDB(t, []w7dAuthStep{
		{cols: []string{"id", "expires_at", "last_seen_at", "sa.id", "username", "display_name", "role", "must_change_password"}, row: []driver.Value{"sess", "2026-09-14T09:00:00Z", now.Format(time.RFC3339Nano), "acc", "user", "User", "user", "false"}},
	})
	auth7, _ := New(db7, SQLite, func() time.Time { return now })
	if _, err := auth7.RequireAdminToken(context.Background(), token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非管理员必须拒绝: %v", err)
	}
}

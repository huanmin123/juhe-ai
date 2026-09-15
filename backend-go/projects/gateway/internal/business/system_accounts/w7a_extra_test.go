package systemaccounts

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

var w7aErrBoom = errors.New("w7a boom")

// ---------------------------------------------------------------------------
// 脚本化 database/sql driver（w7a 前缀）
// ---------------------------------------------------------------------------

type w7aStep struct {
	contains    string
	cols        []string
	row         []driver.Value
	noRows      bool
	rowsErr     error
	eofErr      error
	execErr     error
	affected    int64
	affectedErr error
	beginErr    error
	commitErr   error
}

var w7aDriverSeq int64

type w7aScript struct{ steps []w7aStep }

func (s *w7aScript) next(kind, query string) (*w7aStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w7a script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w7a step mismatch: want %q got %s: %s", step.contains, kind, query)
	}
	return step, nil
}

type w7aConn struct{ script *w7aScript }

func (c *w7aConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w7a: prepare unsupported")
}
func (c *w7aConn) Close() error { return nil }
func (c *w7aConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w7aTx{script: c.script}, nil
}
func (c *w7aConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	if step.noRows || (step.row == nil && step.cols == nil) {
		return &w7aRows{cols: []string{"x"}}, nil
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"id"}
	}
	values := [][]driver.Value{step.row}
	if step.row == nil {
		values = nil
	}
	return &w7aRows{cols: cols, eofErr: step.eofErr, values: values}, nil
}
func (c *w7aConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w7aResult{affected: step.affected, err: step.affectedErr}, nil
}

type w7aTx struct{ script *w7aScript }

func (t *w7aTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w7aTx) Rollback() error { return nil }

type w7aResult struct {
	affected int64
	err      error
}

func (r w7aResult) LastInsertId() (int64, error) { return 0, nil }
func (r w7aResult) RowsAffected() (int64, error) { return r.affected, r.err }

type w7aRows struct {
	cols   []string
	values [][]driver.Value
	eofErr error
	index  int
}

func (r *w7aRows) Columns() []string { return r.cols }
func (r *w7aRows) Close() error      { return nil }
func (r *w7aRows) Next(dest []driver.Value) error {
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
func (r *w7aRows) Err() error { return nil }

type w7aDriver struct{ script *w7aScript }

func (d w7aDriver) Open(string) (driver.Conn, error) { return &w7aConn{script: d.script}, nil }

func w7aOpen(t *testing.T, steps []w7aStep) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("w7a-sysacc-%d", atomic.AddInt64(&w7aDriverSeq, 1))
	sql.Register(name, w7aDriver{script: &w7aScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// 构造函数与 Service 包装
// ---------------------------------------------------------------------------

func TestW7AConstructorArms(t *testing.T) {
	if _, err := NewStore(nil, SQLite, "", OwnerGate{}, testCipher{}); err == nil {
		t.Fatal("nil db must be rejected")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewStore(db, "oracle", "", OwnerGate{}, testCipher{}); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("mode=%v", err)
	}
	pg, err := NewStore(db, Postgres, "", OwnerGate{}, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	if pg.schema != "juhe_business" {
		t.Fatalf("default schema=%q", pg.schema)
	}
	if _, err := NewStore(db, Postgres, "bad.schema", OwnerGate{}, testCipher{}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("schema=%v", err)
	}
}

func TestW7AServiceForwardsAllOperations(t *testing.T) {
	if _, err := New(nil, SQLite, "", OwnerGate{}, testCipher{}); err == nil {
		t.Fatal("nil db must fail service constructor")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fullGate := OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
	svc, err := New(db, SQLite, "", fullGate, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckContract(context.Background()); err == nil {
		t.Fatal("contract must fail without schema")
	}
	svc.store.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if _, err := svc.Create(context.Background(), CreateInput{Username: "alice", DisplayName: "Alice", PasswordHash: "hash"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("service create partial gate=%v", err)
	}
	// 真实库上的全方法转发。
	_, db2 := testStore(t, fullGate)
	defer db2.Close()
	svc2, err := New(db2, SQLite, "", fullGate, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc2.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	created, err := svc2.Create(context.Background(), CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc2.List(context.Background(), ListOptions{PageSize: 5})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("service list=%+v err=%v", list, err)
	}
	opts, err := svc2.Options(context.Background(), OptionListOptions{IDs: []string{"sys-1"}})
	if err != nil || len(opts) != 1 {
		t.Fatalf("service options=%+v err=%v", opts, err)
	}
	password := "hash2"
	patched, err := svc2.PatchCAS(context.Background(), "sys-1", Patch{ExpectedUpdatedAt: created.UpdatedAt, PasswordHash: &password})
	if err != nil || patched.Kind != "updated" {
		t.Fatalf("service patch=%+v err=%v", patched, err)
	}
}

// ---------------------------------------------------------------------------
// List / Options 关键词与分页臂
// ---------------------------------------------------------------------------

func TestW7AListKeywordPaginationAndEscapes(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	updatedAt := "2026-08-28T12:00:00Z"
	seed := `INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password,image_generation_enabled,created_at,updated_at) VALUES`
	for _, row := range []string{
		`('s1','alice','Alice','user','active','h',0,0,'` + updatedAt + `','` + updatedAt + `')`,
		`('s2','bob100','Bob%X','user','active','h',0,0,'` + updatedAt + `','` + updatedAt + `')`,
		`('s3','carol','Carol_x','admin','disabled','h',0,0,'` + updatedAt + `','` + updatedAt + `')`,
	} {
		if _, err := db.Exec(seed + row); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.List(ctx, ListOptions{Keyword: "bob"})
	if err != nil || len(got.Items) != 1 || got.Items[0].Username != "bob100" {
		t.Fatalf("keyword list=%+v err=%v", got, err)
	}
	// % 必须按字面量匹配，而不是通配符。
	got, err = store.List(ctx, ListOptions{Keyword: "bob%"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Items[0].Username != "bob100" {
		t.Fatalf("literal percent list=%+v", got.Items)
	}
	got, err = store.List(ctx, ListOptions{Keyword: "Alice"})
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != "s1" {
		t.Fatalf("display keyword=%+v err=%v", got, err)
	}
	// pageSize<1 默认、>100 收敛、page 超窗收敛。
	got, err = store.List(ctx, ListOptions{PageSize: 0, Page: 999999})
	if err != nil || got.PageSize != 20 || got.Page == 999999 {
		t.Fatalf("clamped list=%+v err=%v", got, err)
	}
	got, err = store.List(ctx, ListOptions{PageSize: 1000})
	if err != nil || got.PageSize != 100 {
		t.Fatalf("pageSize clamp=%+v err=%v", got, err)
	}
	got, err = store.List(ctx, ListOptions{PageSize: 2, Page: 1})
	if err != nil || len(got.Items) != 2 || !got.HasMore {
		t.Fatalf("page1=%+v err=%v", got, err)
	}
	got, err = store.List(ctx, ListOptions{PageSize: 2, Page: 2})
	if err != nil || len(got.Items) != 1 || got.HasMore {
		t.Fatalf("page2=%+v err=%v", got, err)
	}
	if _, err := store.List(ctx, ListOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestW7AOptionsKeywordLimitAndDisabledReason(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	updatedAt := "2026-08-28T12:00:00Z"
	seed := `INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password,image_generation_enabled,created_at,updated_at) VALUES`
	for _, row := range []string{
		`('s1','alice','Alice','user','active','h',0,0,'` + updatedAt + `','` + updatedAt + `')`,
		`('s2','bob','Bob','user','disabled','h',0,0,'` + updatedAt + `','` + updatedAt + `')`,
	} {
		if _, err := db.Exec(seed + row); err != nil {
			t.Fatal(err)
		}
	}
	opts, err := store.Options(ctx, OptionListOptions{Keyword: " ", Limit: 0})
	if err != nil || len(opts) != 2 {
		t.Fatalf("options=%+v err=%v", opts, err)
	}
	var disabled *Option
	for i := range opts {
		if opts[i].ID == "s2" {
			disabled = &opts[i]
		}
	}
	if disabled == nil || disabled.DisabledReason != "account_disabled" {
		t.Fatalf("disabled reason missing: %+v", opts)
	}
	opts, err = store.Options(ctx, OptionListOptions{IDs: []string{" s2 ", "", "s2", "s1"}, Limit: 99})
	if err != nil || len(opts) != 2 {
		t.Fatalf("ids options=%+v err=%v", opts, err)
	}
}

// ---------------------------------------------------------------------------
// Create 校验矩阵与唯一性
// ---------------------------------------------------------------------------

func TestW7ACreateValidationMatrix(t *testing.T) {
	store, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	longDesc := strings.Repeat("😀", 101)
	value := "x"
	number := int64(-1)
	cases := []struct {
		name   string
		mutate func(*CreateInput)
	}{
		{"blank password", func(in *CreateInput) { in.PasswordHash = " " }},
		{"blank username", func(in *CreateInput) { in.Username = "   " }},
		{"internal space username", func(in *CreateInput) { in.Username = "ali ce" }},
		{"padded username", func(in *CreateInput) { in.Username = " alice " }},
		{"blank display name", func(in *CreateInput) { in.DisplayName = " " }},
		{"internal space display name", func(in *CreateInput) { in.DisplayName = "Al ice" }},
		{"long description", func(in *CreateInput) { in.Description = &longDesc }},
		{"invalid role", func(in *CreateInput) { in.Role = "root" }},
		{"super admin role", func(in *CreateInput) { in.Role = "super_admin" }},
		{"invalid status", func(in *CreateInput) { in.Status = "paused" }},
		{"negative ai limit", func(in *CreateInput) { in.AIAccountLimit = &number }},
		{"bad request limits", func(in *CreateInput) { in.RequestLimitsJSON = &value }},
	}
	for _, tc := range cases {
		in := CreateInput{Username: "alice", DisplayName: "Alice", PasswordHash: "hash"}
		tc.mutate(&in)
		if _, err := store.Create(ctx, in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s err=%v", tc.name, err)
		}
	}
	// 超大 AI 上限同样越界。
	big := int64(1_000_001)
	if _, err := store.Create(ctx, CreateInput{Username: "alice", DisplayName: "Alice", PasswordHash: "hash", AIAccountLimit: &big}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("big ai limit=%v", err)
	}
}

func TestW7ACreateRequestLimitsMatrix(t *testing.T) {
	store, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	var seq int
	run := func(raw string) error {
		seq++
		value := raw
		_, err := store.Create(ctx, CreateInput{Username: fmt.Sprintf("alice%d", seq), DisplayName: fmt.Sprintf("Alice%d", seq), PasswordHash: "hash", RequestLimitsJSON: &value})
		return err
	}
	for name, raw := range map[string]string{
		"array":         `[1]`,
		"unknown field": `{"other":1}`,
		"float window":  `{"perMinute":0.5}`,
		"negative":      `{"perDay":-1}`,
		"overflow":      `{"perDay":1000000001}`,
		"bad expiresOn": `{"expiresOn":"2026-13-01"}`,
		"date format":   `{"expiresOn":"2026/01/02"}`,
		"invalid json":  `{`,
	} {
		if err := run(raw); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s err=%v", name, err)
		}
	}
	// json null 与空对象同样归一化为 NULL。
	for _, raw := range []string{`null`, `{}`} {
		if err := run(raw); err != nil {
			t.Fatalf("null-like limits %q err=%v", raw, err)
		}
	}
	// 仅 expiresOn（无任何窗口）会归一化为 NULL 并成功创建。
	if err := run(`{"expiresOn":"2026-09-01"}`); err != nil {
		t.Fatalf("expiresOn-only must normalize to nil, err=%v", err)
	}
}

func TestW7ACreateUniqueChecksAndCipherArms(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	seed := `INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password,image_generation_enabled,created_at,updated_at) VALUES`
	if _, err := db.Exec(seed + `('s1','alice','Alice','user','active','h',0,0,'2026-08-28T12:00:00Z','2026-08-28T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, CreateInput{Username: "ALICE", DisplayName: "Other", PasswordHash: "hash"}); err == nil {
		t.Fatal("case-insensitive username conflict must fail")
	}
	if _, err := store.Create(ctx, CreateInput{Username: "alice2", DisplayName: "aLiCe", PasswordHash: "hash"}); err == nil {
		t.Fatal("case-insensitive display conflict must fail")
	}
	// admin 角色强制 mustChangePassword=false。
	admin, err := store.Create(ctx, CreateInput{Username: "boss", DisplayName: "Boss", PasswordHash: "hash", Role: "admin", MustChangePassword: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if admin.MustChangePassword {
		t.Fatalf("admin must not require password change: %+v", admin)
	}
}

func TestW7ACreateCipherFailureArms(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	store.cipher = w7aErrCipher{}
	if _, err := store.Create(ctx, CreateInput{Username: "alice", DisplayName: "Alice", PasswordHash: "hash"}); err == nil {
		t.Fatal("cipher failure must fail create")
	}
	store.cipher = w7aEmptyCipher{}
	if _, err := store.Create(ctx, CreateInput{Username: "bob", DisplayName: "Bob", PasswordHash: "hash"}); err == nil {
		t.Fatal("empty ciphertext must fail create")
	}
}

type w7aErrCipher struct{}

func (w7aErrCipher) Encrypt(context.Context, []byte) (string, error) { return "", w7aErrBoom }

type w7aEmptyCipher struct{}

func (w7aEmptyCipher) Encrypt(context.Context, []byte) (string, error) { return "", nil }

// ---------------------------------------------------------------------------
// PatchCAS 全字段与错误臂
// ---------------------------------------------------------------------------

func TestW7APatchCASValidationArms(t *testing.T) {
	store, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	account, err := store.Create(ctx, CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	name := "x"
	_, err = store.PatchCAS(ctx, " ", Patch{ExpectedUpdatedAt: account.UpdatedAt, DisplayName: &name})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank id=%v", err)
	}
	_, err = store.PatchCAS(ctx, "sys-1", Patch{ExpectedUpdatedAt: " ", DisplayName: &name})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank expected=%v", err)
	}
	_, err = store.PatchCAS(ctx, "sys-1", Patch{ExpectedUpdatedAt: "not-a-time", DisplayName: &name})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad instant=%v", err)
	}
	_, err = store.PatchCAS(ctx, "missing", Patch{ExpectedUpdatedAt: account.UpdatedAt, DisplayName: &name})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing=%v", err)
	}
}

func TestW7APatchCoversEveryField(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	account, err := store.Create(ctx, CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash", Description: stringPtrV("intro"), RequestLimitsJSON: &([]string{`{"perMinute":1}`}[0])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions(id,system_account_id,token_hash,expires_at,created_at,last_seen_at) VALUES ('s1','sys-1','h','2099','c','l')`); err != nil {
		t.Fatal(err)
	}
	newName := "Alice"
	newDesc := "updated"
	clearDesc := ""
	newRole := "admin"
	status := "disabled"
	image := true
	limit := int64(5)
	limitPtr := &limit
	clearLimit := int64(0)
	clearLimitPtr := &clearLimit
	limits := `{"perDay":2,"perWeek":3}`
	limitsPtr := &limits
	descPtr := &newDesc
	clearDescPtr := &clearDesc
	patched, err := store.PatchCAS(ctx, "sys-1", Patch{
		ExpectedUpdatedAt:      account.UpdatedAt,
		DisplayName:            &newName,
		Description:            &descPtr,
		Role:                   &newRole,
		Status:                 &status,
		ImageGenerationEnabled: &image,
		AIAccountLimit:         &limitPtr,
		RequestLimitsJSON:      &limitsPtr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Kind != "updated" || len(patched.Changes) != 7 || patched.Account.Role != "admin" || patched.Account.Status != "disabled" {
		t.Fatalf("patched=%+v changes=%+v", patched, patched.Changes)
	}
	if patched.Account.MustChangePassword {
		t.Fatal("admin must not require password change after patch")
	}
	if patched.Account.AIAccountLimit == nil || *patched.Account.AIAccountLimit != 5 {
		t.Fatalf("ai limit=%+v", patched.Account.AIAccountLimit)
	}
	// 第二轮：清空可空字段并确认变更计数。
	next := patched.Account
	patched2, err := store.PatchCAS(ctx, "sys-1", Patch{
		ExpectedUpdatedAt: next.UpdatedAt,
		Description:       &clearDescPtr,
		AIAccountLimit:    &clearLimitPtr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if patched2.Account.Description != nil || patched2.Account.AIAccountLimit == nil || *patch2Limit(patched2) != 0 {
		t.Fatalf("cleared=%+v", patched2.Account)
	}
	noop, err := store.PatchCAS(ctx, "sys-1", Patch{ExpectedUpdatedAt: patched2.Account.UpdatedAt, DisplayName: &newName})
	if err != nil || noop.Kind != "no_op" || len(noop.Changes) != 0 {
		t.Fatalf("noop=%+v err=%v", noop, err)
	}
}

func patch2Limit(r PatchResult) *int64 { return r.Account.AIAccountLimit }

func TestW7APatchErrorArms(t *testing.T) {
	store, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	account, err := store.Create(ctx, CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	version := account.UpdatedAt
	space := "Al ice"
	superAdmin := "super_admin"
	badRole := "root"
	badStatus := "paused"
	badLimit := int64(-2)
	badLimitPtr := &badLimit
	blankHash := " "
	value := "x"
	valuePtr := &value
	for _, tc := range []struct {
		name  string
		patch Patch
	}{
		{"display name space", Patch{ExpectedUpdatedAt: version, DisplayName: &space}},
		{"super admin role", Patch{ExpectedUpdatedAt: version, Role: &superAdmin}},
		{"bad role", Patch{ExpectedUpdatedAt: version, Role: &badRole}},
		{"bad status", Patch{ExpectedUpdatedAt: version, Status: &badStatus}},
		{"bad ai limit", Patch{ExpectedUpdatedAt: version, AIAccountLimit: &badLimitPtr}},
		{"blank hash", Patch{ExpectedUpdatedAt: version, PasswordHash: &blankHash}},
		{"bad limits", Patch{ExpectedUpdatedAt: version, RequestLimitsJSON: &valuePtr}},
	} {
		if _, err := store.PatchCAS(ctx, "sys-1", tc.patch); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s err=%v", tc.name, err)
		}
	}
}

func TestW7APatchDisplayNameConflictAndSuperAdminInvariant(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	updatedAt := "2026-08-28T12:00:00Z"
	seed := `INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password,image_generation_enabled,created_at,updated_at) VALUES`
	for _, row := range []string{
		`('sa','root','Root','super_admin','active','h',0,0,'` + updatedAt + `','` + updatedAt + `')`,
		`('sb','root2','Root2','super_admin','active','h',0,0,'` + updatedAt + `','` + updatedAt + `')`,
	} {
		if _, err := db.Exec(seed + row); err != nil {
			t.Fatal(err)
		}
	}
	name := "Root2"
	if _, err := store.PatchCAS(ctx, "sa", Patch{ExpectedUpdatedAt: updatedAt, DisplayName: &name}); err == nil {
		t.Fatal("display name conflict must fail")
	}
	role := "user"
	// 还有另一个活跃 super_admin，允许降级。
	patched, err := store.PatchCAS(ctx, "sa", Patch{ExpectedUpdatedAt: updatedAt, Role: &role})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Account.Role != "user" {
		t.Fatalf("role patch=%+v", patched)
	}
	// 只剩一个活跃 super_admin 时禁止降级。
	if _, err := store.PatchCAS(ctx, "sb", Patch{ExpectedUpdatedAt: updatedAt, Role: &role}); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("last super admin=%v", err)
	}
	// 禁用最后一个活跃 super_admin 同样禁止。
	if _, err := store.PatchCAS(ctx, "sb", Patch{ExpectedUpdatedAt: updatedAt, Status: stringPtrV("disabled")}); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("disable last super admin=%v", err)
	}
	// 角色降级不吊销会话。
	if patched.RevokedSessionCount != 0 {
		t.Fatalf("role patch revoked=%d", patched.RevokedSessionCount)
	}
}

// ---------------------------------------------------------------------------
// 辅助函数直测
// ---------------------------------------------------------------------------

func TestW7AHelperArms(t *testing.T) {
	if !boolValue(true) || !boolValue(int64(2)) || !boolValue(3) || !boolValue([]byte("1")) || !boolValue([]byte("TRUE")) || !boolValue("true") || boolValue(struct{}{}) {
		t.Fatal("boolValue arms")
	}
	str := "s"
	if cloneString(&str) == &str || cloneString(nil) != nil {
		t.Fatal("cloneString arms")
	}
	num := int64(7)
	if cloneInt64(&num) == &num || cloneInt64(nil) != nil {
		t.Fatal("cloneInt64 arms")
	}
	if !sameOptionalString(&str, &str) || sameOptionalString(&str, nil) {
		t.Fatal("sameOptionalString arms")
	}
	if !sameOptionalInt64(&num, &num) || sameOptionalInt64(&num, nil) {
		t.Fatal("sameOptionalInt64 arms")
	}
	if optionalValue[*string](nil) != nil || optionalValue(&str) != "s" {
		t.Fatal("optionalValue arms")
	}
	if got := escapeLikePrefix(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("escapeLikePrefix=%q", got)
	}
	if placeholders(0) != "NULL" {
		t.Fatal("placeholders(0)")
	}
	ids := uniqueStrings([]string{" b ", "b", "", "a"})
	if len(ids) != 2 || ids[0] != "a" {
		t.Fatalf("uniqueStrings=%v", ids)
	}
	many := make([]string, 60)
	for i := range many {
		many[i] = fmt.Sprintf("id-%02d", i)
	}
	if got := uniqueStrings(many); len(got) != maxOptionLimit {
		t.Fatalf("uniqueStrings cap=%d", len(got))
	}
	// nextTimestamp：非法 current 回退到当前时钟；now<=current 时至少 +1ms。
	store, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := parseInstant(store.nextTimestamp("bogus")); err != nil {
		t.Fatalf("nextTimestamp fallback=%q", store.nextTimestamp("bogus"))
	}
	future := "2030-01-01T00:00:00Z"
	if got, err := parseInstant(store.nextTimestamp(future)); err != nil || !got.After(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("nextTimestamp future=%v err=%v", got, err)
	}
	if !validDate("2026-09-01") || validDate("2026-02-30") || validDate("20260901") {
		t.Fatal("validDate arms")
	}
	// keywordWhere 两种方言。
	sqliteStoreRef := store
	where, args := sqliteStoreRef.keywordWhere("ali", "username", "display_name")
	if !strings.Contains(where, "COLLATE NOCASE") || len(args) != 4 {
		t.Fatalf("sqlite keywordWhere=%q args=%v", where, args)
	}
	pgStore := &Store{mode: Postgres, schema: "juhe_business"}
	where, args = pgStore.keywordWhere("ali", "username")
	if !strings.Contains(where, "lower(username)") || len(args) != 2 {
		t.Fatalf("pg keywordWhere=%q args=%v", where, args)
	}
	if where, args := pgStore.keywordWhere("", "username"); where != "" || args != nil {
		t.Fatal("empty keyword must yield no clause")
	}
	// table/bind postgres。
	if got := pgStore.table("system_accounts"); got != "juhe_business.system_accounts" {
		t.Fatal(got)
	}
	if got := (&Store{mode: Postgres, schema: ""}).table("x"); got != ".x" {
		t.Fatalf("empty schema table=%q", got)
	}
	if got := pgStore.bind("a=? b=?"); got != "a=$1 b=$2" {
		t.Fatal(got)
	}
}

// ---------------------------------------------------------------------------
// 脚本化失败臂
// ---------------------------------------------------------------------------

func w7aAccountRowCols() ([]string, []driver.Value) {
	cols := []string{"id", "username", "display_name", "description", "role", "status", "password_hash", "must_change_password", "image_generation_enabled", "ai_account_limit", "request_limits_json", "last_login_at", "created_at", "updated_at"}
	row := []driver.Value{"sys-1", "alice", "Alice", nil, "user", "active", "hash", int64(1), int64(0), nil, nil, nil, "c", "2026-08-28T12:00:00Z"}
	return cols, row
}

func TestW7AScriptedCreateFailureArms(t *testing.T) {
	ctx := context.Background()
	uniqueOK := func() []w7aStep {
		return []w7aStep{
			{contains: "WHERE lower(username)=lower(?)", noRows: true},
			{contains: "WHERE lower(display_name)=lower(?)", noRows: true},
		}
	}
	commitSteps := []w7aStep{{beginErr: nil}}
	cases := []struct {
		name  string
		steps []w7aStep
	}{
		{"begin", []w7aStep{{beginErr: w7aErrBoom}}},
		{"username unique read", []w7aStep{{beginErr: nil}, {contains: "WHERE lower(username)=lower(?)", rowsErr: w7aErrBoom}}},
		{"display unique read", append([]w7aStep{{beginErr: nil}, {contains: "WHERE lower(username)=lower(?)", noRows: true}}, w7aStep{contains: "WHERE lower(display_name)=lower(?)", rowsErr: w7aErrBoom})},
		{"insert", append(append([]w7aStep{{beginErr: nil}}, uniqueOK()...), w7aStep{contains: "INSERT INTO system_accounts", execErr: w7aErrBoom})},
	}
	// commit 失败必须走完全部默认资源写入。
	commitSteps = append([]w7aStep{{beginErr: nil}}, uniqueOK()...)
	commitSteps = append(commitSteps, w7aStep{contains: "INSERT INTO system_accounts", affected: 1})
	for i := 0; i < 8; i++ {
		commitSteps = append(commitSteps, w7aStep{contains: "INSERT INTO groups", affected: 1})
	}
	for i := 0; i < 7; i++ {
		commitSteps = append(commitSteps, w7aStep{contains: "INSERT INTO route_strategies", affected: 1})
		commitSteps = append(commitSteps, w7aStep{contains: "INSERT INTO route_strategy_groups", affected: 1})
		commitSteps = append(commitSteps, w7aStep{contains: "INSERT INTO api_keys", affected: 1})
	}
	commitSteps = append(commitSteps, w7aStep{contains: "INSERT INTO api_keys", affected: 1})
	commitSteps = append(commitSteps, w7aStep{commitErr: w7aErrBoom})
	cases = append(cases, struct {
		name  string
		steps []w7aStep
	}{"commit", commitSteps})
	for _, tc := range cases {
		db := w7aOpen(t, tc.steps)
		store, err := NewStore(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, testCipher{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(ctx, CreateInput{ID: "sys-x", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"}); err == nil {
			t.Fatalf("create %s must fail", tc.name)
		}
	}
}

func TestW7AScriptedPatchFailureArms(t *testing.T) {
	ctx := context.Background()
	accCols, accRow := w7aAccountRowCols()
	row := w7aStep{contains: "SELECT id,username,display_name", cols: accCols, row: accRow}
	superRow := append([]driver.Value{}, accRow...)
	superRow[4] = "super_admin"
	name := "Alice2"
	hash := "hash2"
	role := "user"
	version := "2026-08-28T12:00:00Z"
	begin := w7aStep{}
	displayOK := w7aStep{contains: "lower(display_name)=lower(?) AND id<>?", noRows: true}
	updateOK := w7aStep{contains: "UPDATE system_accounts SET", affected: 1}
	// 常规 sqlite 模式失败臂。
	cases := []struct {
		name  string
		mode  Mode
		patch Patch
		steps []w7aStep
	}{
		{"begin", SQLite, Patch{ExpectedUpdatedAt: version, DisplayName: &name}, []w7aStep{{beginErr: w7aErrBoom}}},
		{"getForUpdate read", SQLite, Patch{ExpectedUpdatedAt: version, DisplayName: &name}, []w7aStep{begin, {contains: "SELECT id,username", rowsErr: w7aErrBoom}}},
		{"super admin count", SQLite, Patch{ExpectedUpdatedAt: version, Role: &role}, []w7aStep{begin, {contains: "SELECT id,username", cols: accCols, row: superRow}, {contains: "SELECT COUNT(*) FROM system_accounts", rowsErr: w7aErrBoom}}},
		{"display unique read", SQLite, Patch{ExpectedUpdatedAt: version, DisplayName: &name}, []w7aStep{begin, row, {contains: "lower(display_name)=lower(?) AND id<>?", rowsErr: w7aErrBoom}}},
		{"update", SQLite, Patch{ExpectedUpdatedAt: version, DisplayName: &name}, []w7aStep{begin, row, displayOK, {contains: "UPDATE system_accounts SET", execErr: w7aErrBoom}}},
		{"update rowsAffected", SQLite, Patch{ExpectedUpdatedAt: version, DisplayName: &name}, []w7aStep{begin, row, displayOK, {contains: "UPDATE system_accounts SET", affectedErr: w7aErrBoom}}},
		{"update cas", SQLite, Patch{ExpectedUpdatedAt: version, DisplayName: &name}, []w7aStep{begin, row, displayOK, {contains: "UPDATE system_accounts SET", affected: 0}}},
		{"session delete", SQLite, Patch{ExpectedUpdatedAt: version, PasswordHash: &hash}, []w7aStep{begin, row, updateOK, {contains: "DELETE FROM system_sessions", execErr: w7aErrBoom}}},
		{"session rowsAffected", SQLite, Patch{ExpectedUpdatedAt: version, PasswordHash: &hash}, []w7aStep{begin, row, updateOK, {contains: "DELETE FROM system_sessions", affectedErr: w7aErrBoom}}},
		{"commit", SQLite, Patch{ExpectedUpdatedAt: version, DisplayName: &name}, []w7aStep{begin, row, displayOK, updateOK, {commitErr: w7aErrBoom}}},
		// PostgreSQL 模式：advisory lock 与 FOR UPDATE 行锁分支。
		{"pg advisory error", Postgres, Patch{ExpectedUpdatedAt: version, Role: &role}, []w7aStep{begin, {contains: "pg_advisory_xact_lock", execErr: w7aErrBoom}}},
		{"pg advisory and update", Postgres, Patch{ExpectedUpdatedAt: version, DisplayName: &name}, []w7aStep{begin, {contains: "pg_advisory_xact_lock", affected: 1}, row, displayOK, updateOK, {commitErr: w7aErrBoom}}},
	}
	for _, tc := range cases {
		db := w7aOpen(t, tc.steps)
		store, err := NewStore(db, tc.mode, "juhe_business", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, testCipher{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PatchCAS(ctx, "sys-1", tc.patch); err == nil {
			t.Fatalf("patch %s must fail", tc.name)
		}
	}
}

func stringPtrV(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }

// ---------------------------------------------------------------------------
// 剩余防御臂
// ---------------------------------------------------------------------------

func TestW7ACheckContractNilDB(t *testing.T) {
	if err := (&Store{}).CheckContract(context.Background()); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil db contract=%v", err)
	}
}

func TestW7AListAndOptionsGateAndKeywordArms(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true})
	ctx := context.Background()
	if _, err := store.Options(ctx, OptionListOptions{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("options gate=%v", err)
	}
	if _, err := store.Create(ctx, CreateInput{Username: "alice", DisplayName: "Alice", PasswordHash: "hash"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("create gate=%v", err)
	}
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
	if _, err := db.Exec("INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password,image_generation_enabled,created_at,updated_at) VALUES ('s1','bobby','Bobby','user','active','h',0,0,'c','u')"); err != nil {
		t.Fatal(err)
	}
	opts, err := store.Options(ctx, OptionListOptions{Keyword: "bobby"})
	if err != nil || len(opts) != 1 {
		t.Fatalf("keyword options=%+v err=%v", opts, err)
	}
	// 子测试获得独立 DSN，用于关闭数据库后的查询错误臂。
	t.Run("closed-db", func(t *testing.T) {
		store2, db2 := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
		if err := db2.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := store2.List(ctx, ListOptions{}); err == nil {
			t.Fatal("closed db list must fail")
		}
		if _, err := store2.Options(ctx, OptionListOptions{}); err == nil {
			t.Fatal("closed db options must fail")
		}
	})
}

func TestW7AScriptedListScanArms(t *testing.T) {
	ctx := context.Background()
	cols := []string{"id", "username", "display_name", "description", "role", "status", "must_change_password", "image_generation_enabled", "ai_account_limit", "request_limits_json", "last_login_at", "created_at", "updated_at"}
	s := func(steps []w7aStep) *Store {
		db := w7aOpen(t, steps)
		store, err := NewStore(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, testCipher{})
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	nilRow := append([]driver.Value{nil}, make([]driver.Value, 12)...)
	if _, err := s([]w7aStep{{contains: "SELECT id,username", cols: cols, row: nilRow}}).List(ctx, ListOptions{}); err == nil {
		t.Fatal("NULL id scan must fail")
	}
	if _, err := s([]w7aStep{{contains: "SELECT id,username", cols: cols, eofErr: w7aErrBoom}}).List(ctx, ListOptions{}); err == nil {
		t.Fatal("rows iteration error must fail")
	}
	optCols := []string{"id", "display_name", "status"}
	if _, err := s([]w7aStep{{contains: "SELECT id,display_name,status", cols: optCols, row: []driver.Value{nil, "x", "active"}}}).Options(ctx, OptionListOptions{}); err == nil {
		t.Fatal("options scan error must fail")
	}
}

func TestW7APatchValuesRemainingArms(t *testing.T) {
	store, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	account, err := store.Create(ctx, CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	longDesc := strings.Repeat("字", 201)
	descPtr := &longDesc
	if _, err := store.PatchCAS(ctx, "sys-1", Patch{ExpectedUpdatedAt: account.UpdatedAt, Description: &descPtr}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("long description=%v", err)
	}
	flag := false
	patched, err := store.PatchCAS(ctx, "sys-1", Patch{ExpectedUpdatedAt: account.UpdatedAt, MustChangePassword: &flag})
	if err != nil || patched.Account.MustChangePassword {
		t.Fatalf("explicit mustChangePassword=%+v err=%v", patched, err)
	}
}

func TestW7ACreateDefaultResourceFailureArms(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		drops []string
	}{
		{"groups", []string{"groups"}},
		{"route_strategies", []string{"route_strategies"}},
		{"route_strategy_groups", []string{"route_strategy_groups"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
			for _, table := range tc.drops {
				if _, err := db.Exec("DROP TABLE " + table); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Create(ctx, CreateInput{ID: "sys-x", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"}); err == nil {
				t.Fatalf("%s failure must roll back create", tc.name)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM system_accounts").Scan(&count); err != nil || count != 0 {
				t.Fatalf("%s rollback count=%d err=%v", tc.name, count, err)
			}
		})
	}
	t.Run("chat-key-cipher", func(t *testing.T) {
		store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
		store.cipher = w7aCountingCipher{}
		if _, err := store.Create(ctx, CreateInput{ID: "sys-y", Username: "chris", DisplayName: "Chris", PasswordHash: "hash"}); err == nil {
			t.Fatal("chat key cipher failure must fail create")
		}
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&count); err != nil || count != 0 {
			t.Fatalf("chat key rollback keys=%d err=%v", count, err)
		}
	})
}

type w7aCountingCipher struct{}

func (w7aCountingCipher) Encrypt(_ context.Context, plaintext []byte) (string, error) {
	w7aCipherCalls++
	if w7aCipherCalls == 8 {
		return "", w7aErrBoom
	}
	if len(plaintext) == 0 {
		return "", errors.New("empty secret")
	}
	return "v1:test-ciphertext", nil
}

var w7aCipherCalls int

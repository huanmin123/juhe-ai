package accounts

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

	_ "modernc.org/sqlite"
)

// w7aDB 提供命名共享内存 SQLite：cache=shared 让多条连接命中同一内存库，
// 支撑 List 的嵌套查询；每次调用使用唯一库名避免跨用例串库。
var w7aDBSeq atomic.Int64

func w7aDB(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("w7a-accounts-%d", w7aDBSeq.Add(1))
	db, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,protocol_version TEXT,name TEXT,type TEXT,status TEXT,credentials_encrypted TEXT,config_revision INTEGER NOT NULL DEFAULT 1,dispatch_revision INTEGER NOT NULL DEFAULT 1,schedulable INTEGER,availability_schedule_json TEXT,created_at TEXT,updated_at TEXT,deleted_at TEXT,deleted_by TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT,provider_code TEXT,model TEXT,created_at TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,provider_code TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER,created_at TEXT,updated_at TEXT)`,
		`CREATE TABLE account_tags (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,created_at TEXT,updated_at TEXT,UNIQUE(system_account_id,name))`,
		`CREATE TABLE account_tag_bindings (account_id TEXT,tag_id TEXT,system_account_id TEXT,created_at TEXT,UNIQUE(account_id,tag_id))`,
		`CREATE TABLE group_accounts (account_id TEXT)`,
		`CREATE TABLE account_api_key_runtime_states (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,key_fingerprint TEXT,status TEXT,created_at TEXT,updated_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func w7aSvc(t *testing.T) *Service {
	t.Helper()
	s, err := New(w7aDB(t), false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---------------------------------------------------------------------------
// 脚本化 database/sql driver（w7a 前缀，参照 cmd/juhe-ai-gateway/w2 先例）。
// 按调用次序应答 Query/Exec/Begin/Commit，用于注入单靠 SQLite 无法触达的
// 失败防御臂（begin/commit 失败、RowsAffected!=1、查询中途报错）。
// ---------------------------------------------------------------------------

type w7aStep struct {
	contains    string
	cols        []string
	row         []driver.Value
	rowsErr     error
	eofErr      error
	noRows      bool
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
	return &w7aRows{cols: cols, eofErr: step.eofErr, values: [][]driver.Value{step.row}}, nil
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
	name := fmt.Sprintf("w7a-accounts-%d", atomic.AddInt64(&w7aDriverSeq, 1))
	sql.Register(name, w7aDriver{script: &w7aScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	// List 会在外层 rows 未关闭时嵌套查询，必须允许多连接。
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func w7aReadyScripted(t *testing.T, steps []w7aStep) *Service {
	t.Helper()
	s, err := New(w7aOpen(t, steps), false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---------------------------------------------------------------------------
// 构造函数与门禁
// ---------------------------------------------------------------------------

func TestW7AConstructorAndGateArms(t *testing.T) {
	if _, err := New(nil, false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}); err == nil {
		t.Fatal("nil db must be rejected")
	}
	if _, err := NewStore(nil, false, OwnerGate{}); err == nil {
		t.Fatal("nil db store must be rejected")
	}
	var nilStore *Store
	if err := nilStore.requireOwner(); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil store requireOwner=%v", err)
	}
	if err := nilStore.CheckContract(context.Background()); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil store contract=%v", err)
	}
	empty := &Store{}
	if err := empty.CheckContract(context.Background()); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil db contract=%v", err)
	}
	gate := OwnerGate{Confirmed: true, SchemaReady: true}
	if gate.Ready() {
		t.Fatal("partial gate must not be ready")
	}
}

// ---------------------------------------------------------------------------
// CheckContract / List / Get
// ---------------------------------------------------------------------------

func TestW7ACheckContractVerifiesEveryRelation(t *testing.T) {
	s := w7aSvc(t)
	if err := s.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := w7aDB(t)
	svc, err := New(db, false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE group_accounts`); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckContract(context.Background()); err == nil {
		t.Fatal("missing relation must fail the contract")
	}
}

func TestW7AListReturnsAccountsInOrder(t *testing.T) {
	s := w7aSvc(t)
	ctx := context.Background()
	in := input()
	in.ID = "a2"
	in.APIKeyBindings = []APIKeyBinding{{ID: "k2", Fingerprint: "fp2"}}
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "a1" || list[1].ID != "a2" {
		t.Fatalf("list=%+v", list)
	}
	empty, err := s.List(ctx, "other")
	if err != nil || len(empty) != 0 {
		t.Fatalf("other list=%+v err=%v", empty, err)
	}
	// 关闭数据库后 list 的第一处查询错误必须原样上抛。
	broken := w7aSvc(t)
	if err := broken.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := broken.List(ctx, "sys"); err == nil {
		t.Fatal("closed db list must fail")
	}
}

func TestW7AGetNotFoundScheduleAndInjectedError(t *testing.T) {
	s := w7aSvc(t)
	ctx := context.Background()
	in := input()
	schedule := "{\"tz\":\"UTC\"}"
	in.AvailabilityScheduleJSON = &schedule
	in.Schedulable = false
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	a, err := s.Get(ctx, "sys", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if a.AvailabilityScheduleJSON == nil || *a.AvailabilityScheduleJSON != schedule {
		t.Fatalf("schedule=%v", a.AvailabilityScheduleJSON)
	}
	if a.Schedulable {
		t.Fatal("schedulable false must round trip")
	}
	if _, err := s.Get(ctx, "sys", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing=%v", err)
	}
	// 注入非 ErrNoRows 查询错误。
	injected := w7aReadyScripted(t, []w7aStep{{contains: "SELECT id,system_account_id", rowsErr: errors.New("disk boom")}})
	if _, err := injected.Get(ctx, "sys", "a1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("injected get err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// normalize 全防御臂
// ---------------------------------------------------------------------------

func TestW7ANormalizeRejectsEveryMissingField(t *testing.T) {
	cases := map[string]func(*CreateInput){
		"id":            func(in *CreateInput) { in.ID = " " },
		"systemAccount": func(in *CreateInput) { in.SystemAccountID = "" },
		"provider":      func(in *CreateInput) { in.ProviderCode = "" },
		"profile":       func(in *CreateInput) { in.ProviderProtocolProfileID = "" },
		"protocol":      func(in *CreateInput) { in.ProtocolCode = "" },
		"version":       func(in *CreateInput) { in.ProtocolVersion = "" },
		"name":          func(in *CreateInput) { in.Name = " " },
		"type":          func(in *CreateInput) { in.Type = "" },
		"credentials":   func(in *CreateInput) { in.CredentialsEncrypted = "" },
		"status":        func(in *CreateInput) { in.Status = "" },
	}
	for name, mutate := range cases {
		in := input()
		mutate(&in)
		if err := normalize(in); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if err := normalize(input()); err != nil {
		t.Fatalf("valid input err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Create 幂等 / 冲突 / 注入失败臂
// ---------------------------------------------------------------------------

func TestW7ACreateConflictWithDifferentContent(t *testing.T) {
	s := w7aSvc(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	different := input()
	different.Name = "Other"
	if _, err := s.Create(ctx, different); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("different content err=%v", err)
	}
	if !strings.Contains(fmt.Sprint(ErrRevisionConflict), "revision conflict") {
		t.Fatal("sentinel text changed")
	}
}

func TestW7ACreateIdempotentIgnoresOrderAndKeyStatus(t *testing.T) {
	s := w7aSvc(t)
	ctx := context.Background()
	first := input()
	first.ModelMappings = []ModelMapping{
		{SourceModel: "m1", SourceEndpointFamily: "chat", UpstreamModel: "u1", UpstreamEndpointFamily: "chat", Enabled: true},
		{SourceModel: "m2", SourceEndpointFamily: "chat", UpstreamModel: "u2", UpstreamEndpointFamily: "chat", Enabled: false},
	}
	if _, err := s.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	retry := input()
	retry.ModelMappings = []ModelMapping{
		{SourceModel: "m2", SourceEndpointFamily: "chat", UpstreamModel: "u2", UpstreamEndpointFamily: "chat", Enabled: false},
		{SourceModel: "m1", SourceEndpointFamily: "chat", UpstreamModel: "u1", UpstreamEndpointFamily: "chat", Enabled: true},
	}
	retry.APIKeyBindings = []APIKeyBinding{{ID: "k1", Fingerprint: "fp1", Status: "active"}}
	again, err := s.Create(ctx, retry)
	if err != nil {
		t.Fatalf("reordered retry err=%v", err)
	}
	if again.ConfigRevision != 1 || len(again.ModelMappings) != 2 || len(again.APIKeyBindings) != 1 {
		t.Fatalf("idempotent retry=%+v", again)
	}
	// schedule 差异必须视为冲突。
	scheduled := input()
	flag := "{}"
	scheduled.AvailabilityScheduleJSON = &flag
	if _, err := s.Create(ctx, scheduled); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("schedule conflict err=%v", err)
	}
}

func TestW7AScriptedCreateFailureArms(t *testing.T) {
	ctx := context.Background()
	// Begin 失败。
	s := w7aReadyScripted(t, []w7aStep{{beginErr: errors.New("begin boom")}})
	if _, err := s.Create(ctx, input()); err == nil {
		t.Fatal("begin failure must surface")
	}
	// 无关联字段输入：boolInt(false)/nullableString(nil) 臂 + commit 失败。
	bare := input()
	bare.SupportedModels, bare.ModelMappings, bare.Tags, bare.APIKeyBindings = nil, nil, nil, nil
	bare.Schedulable = false
	bare.AvailabilityScheduleJSON = nil
	s2 := w7aReadyScripted(t, []w7aStep{
		{},
		{contains: "INSERT INTO accounts", affected: 1},
		{commitErr: errors.New("commit boom")},
	})
	if _, err := s2.Create(ctx, bare); err == nil {
		t.Fatal("commit failure must surface")
	}
}

// ---------------------------------------------------------------------------
// Patch 全字段、校验臂与注入 RowsAffected!=1
// ---------------------------------------------------------------------------

func TestW7APatchValidatesAndNotFound(t *testing.T) {
	s := w7aSvc(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Patch(ctx, "sys", "a1", Patch{}); err == nil {
		t.Fatal("expected revision <1 must fail")
	}
	if _, err := s.Patch(ctx, "sys", "missing", Patch{ExpectedConfigRevision: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing patch=%v", err)
	}
	blank := " "
	for field, p := range map[string]Patch{
		"name":       {ExpectedConfigRevision: 1, Name: &blank},
		"status":     {ExpectedConfigRevision: 1, Status: &blank},
		"credential": {ExpectedConfigRevision: 1, CredentialsEncrypted: &blank},
	} {
		if _, err := s.Patch(ctx, "sys", "a1", p); err == nil {
			t.Fatalf("%s blank must fail", field)
		}
	}
}

func TestW7APatchCoversEveryFieldAndRelationsOnly(t *testing.T) {
	s := w7aSvc(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	name, status, creds := "B", "disabled", "v1:secret2"
	sched := false
	schedule := "{\"tz\":\"UTC\"}"
	schedulePtr := &schedule
	patched, err := s.Patch(ctx, "sys", "a1", Patch{
		ExpectedConfigRevision:   1,
		Name:                     &name,
		Status:                   &status,
		Schedulable:              &sched,
		CredentialsEncrypted:     &creds,
		AvailabilityScheduleJSON: &schedulePtr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if patched.ConfigRevision != 2 || patched.Status != "disabled" || patched.Schedulable || patched.Name != "B" {
		t.Fatalf("patched=%+v", patched)
	}
	if patched.AvailabilityScheduleJSON == nil || *patched.AvailabilityScheduleJSON != schedule {
		t.Fatalf("schedule=%v", patched.AvailabilityScheduleJSON)
	}
	// 双指针 nil 清空 schedule。
	var nilSchedule *string
	cleared, err := s.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 2, AvailabilityScheduleJSON: &nilSchedule})
	if err != nil || cleared.AvailabilityScheduleJSON != nil {
		t.Fatalf("cleared=%+v err=%v", cleared, err)
	}
	// 关联-only patch（无基础字段）：模型、映射、标签、密钥各臂。
	models := []string{"gpt-4", "gpt-5"}
	mappings := []ModelMapping{{SourceModel: "m1", UpstreamModel: "u1", Enabled: true}}
	tags := []string{"blue", "prod", "prod", " "}
	keys := []APIKeyBinding{{ID: "k2", Fingerprint: "fp2", Status: "active"}}
	relations, err := s.Patch(ctx, "sys", "a1", Patch{
		ExpectedConfigRevision: cleared.ConfigRevision,
		SupportedModels:        &models,
		ModelMappings:          &mappings,
		Tags:                   &tags,
		APIKeyBindings:         &keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	if relations.ConfigRevision != cleared.ConfigRevision+1 {
		t.Fatalf("relations patch revision=%d", relations.ConfigRevision)
	}
	if strings.Join(relations.SupportedModels, ",") != "gpt-4,gpt-5" || len(relations.ModelMappings) != 1 || len(relations.Tags) != 2 || len(relations.APIKeyBindings) != 1 {
		t.Fatalf("relations=%+v", relations)
	}
	// 无效子绑定必须回滚。
	badKeys := []APIKeyBinding{{ID: " ", Fingerprint: "fp"}}
	if _, err := s.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: relations.ConfigRevision, APIKeyBindings: &badKeys}); err == nil {
		t.Fatal("blank key binding must fail")
	}
}

func TestW7AScriptedPatchRowsAffectedArms(t *testing.T) {
	ctx := context.Background()
	// 基础字段 CAS 更新影响 0 行。
	s := w7aReadyScripted(t, []w7aStep{
		{},
		{contains: "SELECT config_revision", cols: []string{"config_revision"}, row: []driver.Value{int64(1)}},
		{contains: "UPDATE accounts SET name=?", affected: 0},
	})
	name := "X"
	if _, err := s.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1, Name: &name}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("base cas err=%v", err)
	}
	// 关联-only 分支 CAS 更新影响 0 行。
	s2 := w7aReadyScripted(t, []w7aStep{
		{},
		{contains: "SELECT config_revision", cols: []string{"config_revision"}, row: []driver.Value{int64(1)}},
		{contains: "DELETE FROM account_supported_models", affected: 1},
		{contains: "SELECT provider_code", cols: []string{"provider_code"}, row: []driver.Value{"openai"}},
		{contains: "INSERT INTO account_supported_models", affected: 1},
		{contains: "UPDATE accounts SET config_revision=config_revision+1,updated_at=?", affected: 0},
	})
	models := []string{"m"}
	if _, err := s2.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1, SupportedModels: &models}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("relations cas err=%v", err)
	}
	// Begin 失败。
	s3 := w7aReadyScripted(t, []w7aStep{{beginErr: errors.New("begin boom")}})
	if _, err := s3.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1}); err == nil {
		t.Fatal("begin failure must surface")
	}
}

// ---------------------------------------------------------------------------
// Delete 冲突臂与注入失败臂
// ---------------------------------------------------------------------------

func TestW7ADeleteValidatesAndConflicts(t *testing.T) {
	s := w7aSvc(t)
	ctx := context.Background()
	if _, err := s.Delete(ctx, "sys", "a1", 0); err == nil {
		t.Fatal("expected<1 must fail")
	}
	if _, err := s.Delete(ctx, "sys", "missing", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete=%v", err)
	}
	if _, err := s.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(ctx, "sys", "a1", 999); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete=%v", err)
	}
	deleted, err := s.Delete(ctx, "sys", "a1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.DispatchRevision != 2 || deleted.Schedulable {
		t.Fatalf("deleted=%+v", deleted)
	}
}

func TestW7AScriptedDeleteBeginFailure(t *testing.T) {
	s := w7aReadyScripted(t, []w7aStep{{beginErr: errors.New("begin boom")}})
	if _, err := s.Delete(context.Background(), "sys", "a1", 1); err == nil {
		t.Fatal("begin failure must surface")
	}
}

// ---------------------------------------------------------------------------
// 等价比较辅助函数
// ---------------------------------------------------------------------------

func TestW7AHelperPredicates(t *testing.T) {
	a, b := "a", "b"
	if !sameOptionalString(&a, &a) || sameOptionalString(&a, &b) || sameOptionalString(&a, nil) || sameOptionalString(nil, nil) != true {
		t.Fatal("sameOptionalString arms")
	}
	if !sameStrings([]string{"b", "a"}, []string{"a", "b"}) || sameStrings([]string{"a"}, nil) || sameStrings([]string{"a"}, []string{"b"}) {
		t.Fatal("sameStrings arms")
	}
	if !sameMappings([]ModelMapping{{SourceModel: "x", Enabled: true}}, []ModelMapping{{SourceModel: "x", Enabled: true}}) ||
		sameMappings([]ModelMapping{{SourceModel: "x"}}, []ModelMapping{{SourceModel: "y"}}) ||
		sameMappings([]ModelMapping{{SourceModel: "x"}}, []ModelMapping{{SourceModel: "y"}, {SourceModel: "z"}}) {
		t.Fatal("sameMappings arms")
	}
	if mappingKey(ModelMapping{SourceModel: "s", SourceEndpointFamily: "e", UpstreamModel: "u", UpstreamEndpointFamily: "f", Enabled: true}) == "" {
		t.Fatal("mappingKey empty")
	}
	keys := []APIKeyBinding{{ID: "k", Status: ""}}
	if !sameKeys(keys, []APIKeyBinding{{ID: "k", Status: "active"}}) {
		t.Fatal("sameKeys must default status")
	}
	if sameKeys([]APIKeyBinding{{ID: "k1"}}, []APIKeyBinding{{ID: "k1"}, {ID: "k2"}}) ||
		sameKeys([]APIKeyBinding{{ID: "k1"}}, []APIKeyBinding{{ID: "k2"}}) {
		t.Fatal("sameKeys mismatch arms")
	}
	if sortedKeys([]APIKeyBinding{{ID: "b"}, {ID: "a"}})[0].ID != "a" {
		t.Fatal("sortedKeys order")
	}
	if sortedStrings([]string{"b", "a"})[0] != "a" {
		t.Fatal("sortedStrings order")
	}
	if sortedMappings([]ModelMapping{{SourceModel: "b"}, {SourceModel: "a"}})[0].SourceModel != "a" {
		t.Fatal("sortedMappings order")
	}
}

// ---------------------------------------------------------------------------
// 门禁与校验臂
// ---------------------------------------------------------------------------

var w7aErrBoom = errors.New("w7a boom")

func TestW7AOwnerGateGuardsEveryOperation(t *testing.T) {
	s, err := New(w7aDB(t), false, OwnerGate{Confirmed: true, SchemaReady: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Create(ctx, input()); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("create=%v", err)
	}
	if _, err := s.Get(ctx, "sys", "a1"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("get=%v", err)
	}
	if _, err := s.List(ctx, "sys"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("list=%v", err)
	}
	if _, err := s.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("patch=%v", err)
	}
	if _, err := s.Delete(ctx, "sys", "a1", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("delete=%v", err)
	}
}

func TestW7ACreateRejectsInvalidInputBeforeTx(t *testing.T) {
	s := w7aSvc(t)
	in := input()
	in.Name = " "
	if _, err := s.Create(context.Background(), in); err == nil {
		t.Fatal("blank name must fail before any SQL")
	}
}

// ---------------------------------------------------------------------------
// 脚本化失败矩阵：get/readRelations/list/delete/patch/create
// ---------------------------------------------------------------------------

func w7aAccountRowCols() ([]string, []driver.Value) {
	cols := []string{"id", "system_account_id", "provider_code", "provider_protocol_profile_id", "protocol_code", "protocol_version", "name", "type", "status", "config_revision", "dispatch_revision", "schedulable", "availability_schedule_json", "created_at", "updated_at"}
	row := []driver.Value{"a1", "sys", "openai", "p1", "openai", "v1", "A", "api_key", "active", int64(1), int64(1), int64(1), nil, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"}
	return cols, row
}

func w7aRelationSteps() []w7aStep {
	return []w7aStep{
		{contains: "FROM account_supported_models", cols: []string{"model"}, row: []driver.Value{"gpt-5"}},
		{contains: "FROM account_model_mappings", cols: []string{"source_model", "source_endpoint_family", "upstream_model", "upstream_endpoint_family", "enabled"}, row: []driver.Value{"gpt-5", "chat", "gpt-5", "chat", int64(1)}},
		{contains: "FROM account_tag_bindings", cols: []string{"name"}, row: []driver.Value{"prod"}},
		{contains: "FROM account_api_key_runtime_states", cols: []string{"id", "key_fingerprint", "status"}, row: []driver.Value{"k1", "fp1", "active"}},
	}
}

func TestW7AScriptedReadRelationsFailureArms(t *testing.T) {
	ctx := context.Background()
	accCols, accRow := w7aAccountRowCols()
	build := func(relation, mode string) []w7aStep {
		steps := []w7aStep{{contains: "SELECT id,system_account_id", cols: accCols, row: accRow}}
		for _, rel := range w7aRelationSteps() {
			step := rel
			if strings.Contains(rel.contains, relation) {
				switch mode {
				case "queryErr":
					step.rowsErr = w7aErrBoom
				case "scanNil":
					step.row = append([]driver.Value{nil}, rel.row[1:]...)
				case "eofErr":
					step.eofErr = w7aErrBoom
				}
			}
			steps = append(steps, step)
		}
		return steps
	}
	for _, relation := range []string{"account_supported_models", "account_model_mappings", "account_tag_bindings", "account_api_key_runtime_states"} {
		for _, mode := range []string{"queryErr", "scanNil", "eofErr"} {
			s := w7aReadyScripted(t, build(relation, mode))
			if _, err := s.Get(ctx, "sys", "a1"); err == nil {
				t.Fatalf("%s %s must surface", relation, mode)
			}
		}
	}
}

func TestW7AScriptedListFailureArms(t *testing.T) {
	ctx := context.Background()
	// 行扫描失败（NULL id）。
	s := w7aReadyScripted(t, []w7aStep{{contains: "SELECT id FROM accounts", cols: []string{"id"}, row: []driver.Value{nil}}})
	if _, err := s.List(ctx, "sys"); err == nil {
		t.Fatal("NULL id scan must surface")
	}
	// 列表内逐账号 get 失败。
	s2 := w7aReadyScripted(t, []w7aStep{
		{contains: "SELECT id FROM accounts", cols: []string{"id"}, row: []driver.Value{"a1"}},
		{contains: "SELECT id,system_account_id", rowsErr: w7aErrBoom},
	})
	if _, err := s2.List(ctx, "sys"); err == nil {
		t.Fatal("inner get failure must surface")
	}
}

func TestW7AScriptedDeleteFailureArms(t *testing.T) {
	ctx := context.Background()
	accCols, accRow := w7aAccountRowCols()
	// get 内部会继续 readRelations，脚本必须补齐关联查询。
	getSteps := append([]w7aStep{{contains: "SELECT id,system_account_id", cols: accCols, row: accRow}}, w7aRelationSteps()...)
	cases := []struct {
		name  string
		steps []w7aStep
	}{
		{"begin", append(append([]w7aStep{}, getSteps...), w7aStep{beginErr: w7aErrBoom})},
		{"update", append(append([]w7aStep{}, getSteps...), w7aStep{}, w7aStep{contains: "UPDATE accounts SET status='disabled'", execErr: w7aErrBoom})},
		{"commit", append(append([]w7aStep{}, getSteps...), w7aStep{}, w7aStep{contains: "UPDATE accounts SET status='disabled'", affected: 1}, w7aStep{commitErr: w7aErrBoom})},
	}
	for _, tc := range cases {
		s := w7aReadyScripted(t, tc.steps)
		if _, err := s.Delete(ctx, "sys", "a1", 1); err == nil {
			t.Fatalf("delete %s failure must surface", tc.name)
		}
	}
}

func TestW7AScriptedPatchFailureArms(t *testing.T) {
	ctx := context.Background()
	rev := w7aStep{contains: "SELECT config_revision", cols: []string{"config_revision"}, row: []driver.Value{int64(1)}}
	name := "X"
	models := []string{"m"}
	cases := []struct {
		name  string
		patch Patch
		steps []w7aStep
	}{
		{"notfound", Patch{ExpectedConfigRevision: 1}, []w7aStep{{}, {contains: "SELECT config_revision", noRows: true}}},
		{"revision read err", Patch{ExpectedConfigRevision: 1}, []w7aStep{{}, {contains: "SELECT config_revision", rowsErr: w7aErrBoom}}},
		{"base update err", Patch{ExpectedConfigRevision: 1, Name: &name}, []w7aStep{{}, rev, {contains: "UPDATE accounts SET name=?", execErr: w7aErrBoom}}},
		{"base commit err", Patch{ExpectedConfigRevision: 1, Name: &name}, []w7aStep{{}, rev, {contains: "UPDATE accounts SET name=?", affected: 1}, {commitErr: w7aErrBoom}}},
		{"relations update err", Patch{ExpectedConfigRevision: 1, SupportedModels: &models}, []w7aStep{
			{}, rev,
			{contains: "DELETE FROM account_supported_models", affected: 1},
			{contains: "SELECT provider_code", cols: []string{"provider_code"}, row: []driver.Value{"openai"}},
			{contains: "INSERT INTO account_supported_models", affected: 1},
			{contains: "SET config_revision=config_revision+1,updated_at=?", execErr: w7aErrBoom},
		}},
	}
	for _, tc := range cases {
		s := w7aReadyScripted(t, tc.steps)
		if _, err := s.Patch(ctx, "sys", "a1", tc.patch); err == nil {
			t.Fatalf("patch %s failure must surface", tc.name)
		}
	}
}

func TestW7AScriptedCreateConflictReadFailure(t *testing.T) {
	ctx := context.Background()
	// 冲突回读失败时必须上抛原始 insert 错误。
	s := w7aReadyScripted(t, []w7aStep{
		{},
		{contains: "INSERT INTO accounts", execErr: w7aErrBoom},
		{contains: "SELECT id,system_account_id", rowsErr: w7aErrBoom},
	})
	if _, err := s.Create(ctx, input()); !errors.Is(err, w7aErrBoom) {
		t.Fatalf("original insert error must surface, got %v", err)
	}
}

func TestW7AScriptedWriteRelationsFailureArms(t *testing.T) {
	ctx := context.Background()
	targets := []string{
		"INSERT INTO account_supported_models",
		"INSERT INTO account_model_mappings",
		"INSERT INTO account_tags",
		"INSERT INTO account_tag_bindings",
		"INSERT INTO account_api_key_runtime_states",
	}
	for _, target := range targets {
		steps := []w7aStep{{}, {contains: "INSERT INTO accounts", affected: 1}}
		for _, insert := range targets {
			step := w7aStep{contains: insert, affected: 1}
			if insert == target {
				step.execErr = w7aErrBoom
			}
			steps = append(steps, step)
		}
		s := w7aReadyScripted(t, steps)
		if _, err := s.Create(ctx, input()); err == nil {
			t.Fatalf("%s failure must surface", target)
		}
	}
}

func TestW7AScriptedPatchRelationsFailureArms(t *testing.T) {
	ctx := context.Background()
	rev := w7aStep{contains: "SELECT config_revision", cols: []string{"config_revision"}, row: []driver.Value{int64(1)}}
	models := []string{"m"}
	mappings := []ModelMapping{{SourceModel: "m1", UpstreamModel: "u1"}}
	tags := []string{"t1"}
	keys := []APIKeyBinding{{ID: "k9", Fingerprint: "fp9"}}
	ok := func(contains string) w7aStep { return w7aStep{contains: contains, affected: 1} }
	cases := []struct {
		name  string
		patch Patch
		steps []w7aStep
	}{
		{"models delete", Patch{ExpectedConfigRevision: 1, SupportedModels: &models}, []w7aStep{{}, rev, {contains: "DELETE FROM account_supported_models", execErr: w7aErrBoom}}},
		{"models provider read", Patch{ExpectedConfigRevision: 1, SupportedModels: &models}, []w7aStep{{}, rev, ok("DELETE FROM account_supported_models"), {contains: "SELECT provider_code", rowsErr: w7aErrBoom}}},
		{"models insert", Patch{ExpectedConfigRevision: 1, SupportedModels: &models}, []w7aStep{{}, rev, ok("DELETE FROM account_supported_models"), {contains: "SELECT provider_code", cols: []string{"provider_code"}, row: []driver.Value{"openai"}}, {contains: "INSERT INTO account_supported_models", execErr: w7aErrBoom}}},
		{"mappings delete", Patch{ExpectedConfigRevision: 1, ModelMappings: &mappings}, []w7aStep{{}, rev, {contains: "DELETE FROM account_model_mappings", execErr: w7aErrBoom}}},
		{"mappings provider read", Patch{ExpectedConfigRevision: 1, ModelMappings: &mappings}, []w7aStep{{}, rev, ok("DELETE FROM account_model_mappings"), {contains: "SELECT provider_code", rowsErr: w7aErrBoom}}},
		{"mappings insert", Patch{ExpectedConfigRevision: 1, ModelMappings: &mappings}, []w7aStep{{}, rev, ok("DELETE FROM account_model_mappings"), {contains: "SELECT provider_code", cols: []string{"provider_code"}, row: []driver.Value{"openai"}}, {contains: "INSERT INTO account_model_mappings", execErr: w7aErrBoom}}},
		{"tags delete", Patch{ExpectedConfigRevision: 1, Tags: &tags}, []w7aStep{{}, rev, {contains: "DELETE FROM account_tag_bindings", execErr: w7aErrBoom}}},
		{"tags insert", Patch{ExpectedConfigRevision: 1, Tags: &tags}, []w7aStep{{}, rev, ok("DELETE FROM account_tag_bindings"), {contains: "INSERT INTO account_tags", execErr: w7aErrBoom}}},
		{"keys delete", Patch{ExpectedConfigRevision: 1, APIKeyBindings: &keys}, []w7aStep{{}, rev, {contains: "DELETE FROM account_api_key_runtime_states", execErr: w7aErrBoom}}},
		{"keys insert", Patch{ExpectedConfigRevision: 1, APIKeyBindings: &keys}, []w7aStep{{}, rev, ok("DELETE FROM account_api_key_runtime_states"), {contains: "INSERT INTO account_api_key_runtime_states", execErr: w7aErrBoom}}},
	}
	for _, tc := range cases {
		s := w7aReadyScripted(t, tc.steps)
		if _, err := s.Patch(ctx, "sys", "a1", tc.patch); err == nil {
			t.Fatalf("patch %s failure must surface", tc.name)
		}
	}
}

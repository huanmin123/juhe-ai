package settings

import (
	"context"
	"database/sql"
	"errors"
	_ "modernc.org/sqlite"
	"testing"
)

func testStore(t *testing.T) (*Store, *sql.DB) {
	db, e := sql.Open("sqlite", "file:settings-"+t.Name()+"?mode=memory&cache=shared")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	// fixture 按 fresh DDL 形态预置 vendors + models 单表（供应商解耦 §3/§8）。
	_, e = db.Exec(`CREATE TABLE global_settings(key TEXT PRIMARY KEY,value_json TEXT NOT NULL,updated_at TEXT NOT NULL); CREATE TABLE system_settings(system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,key)); CREATE TABLE vendors(code TEXT PRIMARY KEY,updated_at TEXT NOT NULL); CREATE TABLE models(id TEXT PRIMARY KEY,vendor_code TEXT,public_model TEXT NOT NULL,upstream_model TEXT NOT NULL,scope TEXT NOT NULL,system_account_id TEXT,status TEXT NOT NULL DEFAULT 'active',catalog_visible INTEGER NOT NULL DEFAULT 1,catalog_order INTEGER,supported_endpoint_modes_json TEXT NOT NULL DEFAULT '[]',context_window_tokens INTEGER,max_input_tokens INTEGER,max_output_tokens INTEGER,source TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	if e != nil {
		t.Fatal(e)
	}
	s, _ := New(db, SQLite, "", OwnerGate{true, true, true})
	return s, db
}
func TestSettingsCASAndRead(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, e := s.PutGlobal(ctx, "x", `{"v":1}`, ""); e != nil {
		t.Fatal(e)
	}
	got, ok, e := s.GetGlobal(ctx, "x")
	if e != nil || !ok || got.ValueJSON != `{"v":1}` {
		t.Fatalf("%+v %v %v", got, ok, e)
	}
	if _, e = s.PutGlobal(ctx, "x", `{"v":2}`, "stale"); !errors.Is(e, ErrCAS) {
		t.Fatalf("want CAS, got %v", e)
	}
	if _, e = s.PutGlobal(ctx, "x", `not-json`, got.UpdatedAt); e == nil {
		t.Fatal("invalid json must fail")
	}
}
func TestSettingsOwnerGate(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	s.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if _, e := s.PutGlobal(context.Background(), "x", `1`, ""); !errors.Is(e, ErrOwnerGate) {
		t.Fatal(e)
	}
}
func TestProviderModelList(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO models(id,vendor_code,public_model,upstream_model,scope,status,catalog_visible,catalog_order,supported_endpoint_modes_json,context_window_tokens,max_input_tokens,max_output_tokens,source,created_at,updated_at) VALUES ('gpt','openai','gpt-5','gpt-5','builtin','active',1,2,'[]',100,90,10,'test','r','r'),('old','openai','old','old','builtin','disabled',1,1,'[]',1,1,1,'test','r','r')`)
	m, e := s.ListProviderModels(context.Background(), "openai", false)
	if e != nil || len(m) != 1 || m[0].Model != "gpt-5" {
		t.Fatalf("%+v %v", m, e)
	}
}

func TestReplaceProviderCatalogCASAndCleanup(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO vendors VALUES ('openai','r1'); INSERT INTO models(id,vendor_code,public_model,upstream_model,scope,status,catalog_visible,catalog_order,supported_endpoint_modes_json,source,created_at,updated_at) VALUES ('old','openai','old','old','builtin','active',1,1,'[]','seed','r','r')`)
	if e := s.ReplaceProviderCatalog(context.Background(), CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r1", Models: []CatalogModel{{ID: "new", Model: "gpt-5", Status: "active", CatalogVisible: true, SupportedAPIProtocolsJSON: "[]", Source: "test"}}}); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM models WHERE vendor_code='openai' AND scope='builtin' AND public_model='gpt-5'`).Scan(&n); e != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, e)
	}
	// CAS 门为 vendors 只读比较：修订不匹配 / 未知 vendor 拒绝；同修订重跑
	// 语义为幂等重插（vendors 不回写，无旧 UPDATE 漂移）。
	if e := s.ReplaceProviderCatalog(context.Background(), CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "stale"}); !errors.Is(e, ErrCAS) {
		t.Fatalf("stale revision want CAS %v", e)
	}
	if e := s.ReplaceProviderCatalog(context.Background(), CatalogReplacement{ProviderCode: "ghost", ExpectedProviderUpdatedAt: "r1"}); !errors.Is(e, ErrCAS) {
		t.Fatalf("unknown vendor want CAS %v", e)
	}
	if e := s.ReplaceProviderCatalog(context.Background(), CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r1", Models: []CatalogModel{{ID: "new", Model: "gpt-5", Status: "active", CatalogVisible: true, SupportedAPIProtocolsJSON: "[]", Source: "test"}}}); e != nil {
		t.Fatalf("same-revision rerun must be idempotent: %v", e)
	}
}

// TestStoreBoolBindDialect 锁定 catalog_visible 的方言绑定契约：PG 绑 Go
// bool（pgx 拒绝整数编码 boolean 列），SQLite 绑 INTEGER 0/1。
func TestStoreBoolBindDialect(t *testing.T) {
	pg := &Store{mode: Postgres}
	if v, ok := pg.boolBind(true).(bool); !ok || !v {
		t.Fatalf("PG true 绑定=%#v, want bool true", pg.boolBind(true))
	}
	if v, ok := pg.boolBind(false).(bool); !ok || v {
		t.Fatalf("PG false 绑定=%#v, want bool false", pg.boolBind(false))
	}
	sqliteStore := &Store{mode: SQLite}
	if v, ok := sqliteStore.boolBind(true).(int); !ok || v != 1 {
		t.Fatalf("SQLite true 绑定=%#v, want int 1", sqliteStore.boolBind(true))
	}
	if v, ok := sqliteStore.boolBind(false).(int); !ok || v != 0 {
		t.Fatalf("SQLite false 绑定=%#v, want int 0", sqliteStore.boolBind(false))
	}
}

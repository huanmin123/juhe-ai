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
	_, e = db.Exec(`CREATE TABLE global_settings(key TEXT PRIMARY KEY,value_json TEXT NOT NULL,updated_at TEXT NOT NULL); CREATE TABLE system_settings(system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,key)); CREATE TABLE providers(code TEXT PRIMARY KEY,updated_at TEXT NOT NULL); CREATE TABLE provider_model_catalog(id TEXT PRIMARY KEY,provider_code TEXT,model TEXT,status TEXT,catalog_visible INTEGER,catalog_order INTEGER,mode TEXT,supported_api_protocols_json TEXT,context_window_tokens INTEGER,max_input_tokens INTEGER,max_output_tokens INTEGER,source TEXT,created_at TEXT,updated_at TEXT)`)
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
	_, _ = db.Exec(`INSERT INTO provider_model_catalog(id,provider_code,model,status,catalog_visible,catalog_order,mode,supported_api_protocols_json,context_window_tokens,max_input_tokens,max_output_tokens,source,created_at,updated_at) VALUES ('gpt','openai','gpt-5','active',1,2,'chat','[]',100,90,10,'test','r','r'),('old','openai','old','disabled',1,1,'chat','[]',1,1,1,'test','r','r')`)
	m, e := s.ListProviderModels(context.Background(), "openai", false)
	if e != nil || len(m) != 1 || m[0].Model != "gpt-5" {
		t.Fatalf("%+v %v", m, e)
	}
}

func TestReplaceProviderCatalogCASAndCleanup(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO providers VALUES ('openai','r1'); INSERT INTO provider_model_catalog(id,provider_code,model,status,catalog_visible,catalog_order,supported_api_protocols_json,source,created_at,updated_at) VALUES ('old','openai','old','active',1,1,'[]','seed','r','r')`)
	if e := s.ReplaceProviderCatalog(context.Background(), CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r1", Models: []CatalogModel{{ID: "new", Model: "gpt-5", Status: "active", CatalogVisible: true, SupportedAPIProtocolsJSON: "[]", Source: "test"}}}); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM provider_model_catalog WHERE provider_code='openai' AND model='gpt-5'`).Scan(&n); e != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, e)
	}
	if e := s.ReplaceProviderCatalog(context.Background(), CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r1"}); !errors.Is(e, ErrCAS) {
		t.Fatalf("want CAS %v", e)
	}
}

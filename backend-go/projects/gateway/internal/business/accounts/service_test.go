package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"strings"
	"testing"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
	// 目标 DDL 形态（设计 §2.1/§4）：accounts 携 vendor_code /
	// connection_driver_code；account_supported_models 收敛为
	// (account_id, model_id)；models 表承接模型身份键
	// (vendor_code, upstream_model) + scope 可见性。
	`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,protocol_version TEXT,vendor_code TEXT,connection_driver_code TEXT,name TEXT,type TEXT,status TEXT,credentials_encrypted TEXT,config_revision INTEGER NOT NULL DEFAULT 1,dispatch_revision INTEGER NOT NULL DEFAULT 1,schedulable INTEGER,availability_schedule_json TEXT,created_at TEXT,updated_at TEXT,deleted_at TEXT,deleted_by TEXT)`,
	`CREATE TABLE models (id TEXT PRIMARY KEY,vendor_code TEXT,public_model TEXT NOT NULL,upstream_model TEXT NOT NULL,scope TEXT NOT NULL,system_account_id TEXT,status TEXT NOT NULL DEFAULT 'active')`,
	`CREATE TABLE account_supported_models (account_id TEXT,model_id TEXT,created_at TEXT,PRIMARY KEY (account_id, model_id))`,
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
	// input() 的支持模型 gpt-5 走 generic（vendor NULL）身份键解析。
	seed := `INSERT INTO models (id,vendor_code,public_model,upstream_model,scope,system_account_id,status) VALUES ('m-gpt5',NULL,'gpt-5','gpt-5','builtin',NULL,'active')`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	return db
}

func ready(t *testing.T) *Service {
	db := testDB(t)
	s, err := New(db, false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func input() CreateInput {
	return CreateInput{ID: "a1", SystemAccountID: "sys", ProviderCode: "openai", ProviderProtocolProfileID: "p1", ProtocolCode: "openai", ProtocolVersion: "v1", Name: "A", Type: "api_key", Status: "active", CredentialsEncrypted: "v1:secret", Schedulable: true, SupportedModels: []string{"gpt-5"}, ModelMappings: []ModelMapping{{SourceModel: "gpt-5", SourceEndpointFamily: "chat", UpstreamModel: "gpt-5", UpstreamEndpointFamily: "chat", Enabled: true}}, Tags: []string{"prod"}, APIKeyBindings: []APIKeyBinding{{ID: "k1", Fingerprint: "fp1"}}}
}

func TestOwnerGateFailClosed(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, false, OwnerGate{Confirmed: true, SchemaReady: true})
	_, err := s.Create(context.Background(), input())
	if !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("err=%v", err)
	}
}
func TestAccountTransactionCASAndRelations(t *testing.T) {
	s := ready(t)
	ctx := context.Background()
	a, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	if a.ConfigRevision != 1 || len(a.SupportedModels) != 1 || len(a.ModelMappings) != 1 {
		t.Fatalf("account=%+v", a)
	}
	name := "B"
	updated, err := s.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1, Name: &name, Tags: &[]string{"prod", "blue"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ConfigRevision != 2 || updated.Name != "B" {
		t.Fatalf("updated=%+v", updated)
	}
	_, err = s.Patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1, Name: &name})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale err=%v", err)
	}
	deleted, err := s.Delete(ctx, "sys", "a1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != "disabled" {
		t.Fatalf("deleted=%+v", deleted)
	}
}

func TestCreateIsIdempotentAndDoesNotExposeEncryptedCredential(t *testing.T) {
	s := ready(t)
	ctx := context.Background()
	first, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.ConfigRevision != 1 || len(second.Tags) != 1 || len(second.APIKeyBindings) != 1 {
		t.Fatalf("idempotent result=%+v", second)
	}
	if strings.Contains(fmt.Sprintf("%+v", second), "v1:secret") {
		t.Fatalf("account output leaks encrypted credential: %+v", second)
	}
}

func TestRelationFailureRollsBackAccountCreate(t *testing.T) {
	s := ready(t)
	in := input()
	in.APIKeyBindings = []APIKeyBinding{{ID: "", Fingerprint: "fp"}}
	if _, err := s.Create(context.Background(), in); err == nil {
		t.Fatal("invalid child binding must fail")
	}
	if _, err := s.Get(context.Background(), "sys", "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed create must rollback account, err=%v", err)
	}
}
// B3 目标形态：支持模型名按 (vendor_code, upstream_model) 解析不到时以
// ErrModelNotFound 拒绝并整体回滚，不静默跳过。
func TestCreateRejectsUnknownSupportedModel(t *testing.T) {
	s := ready(t)
	ctx := context.Background()
	in := input()
	in.SupportedModels = []string{"nope"}
	if _, err := s.Create(ctx, in); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("unresolved supported model err=%v", err)
	}
	if _, err := s.Get(ctx, "sys", "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed create must rollback account, err=%v", err)
	}
}

func TestPostgresDialectBinding(t *testing.T) {
	s := &Store{postgres: true}
	q := s.bind("SELECT * FROM juhe_business.accounts WHERE id=? AND system_account_id=?")
	if !strings.Contains(q, "$1") || !strings.Contains(q, "$2") {
		t.Fatal(q)
	}
	if s.table("accounts") != "juhe_business.accounts" {
		t.Fatal(s.table("accounts"))
	}
}

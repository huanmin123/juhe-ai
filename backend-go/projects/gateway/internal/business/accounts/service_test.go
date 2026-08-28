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

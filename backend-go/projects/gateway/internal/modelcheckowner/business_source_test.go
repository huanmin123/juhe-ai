package modelcheckowner

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBusinessTargetSourceReadsScopedActiveAccount(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,config_revision INTEGER,status TEXT,schedulable INTEGER,credentials_encrypted TEXT,deleted_at TEXT)`,
		`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,provider_code TEXT,enabled INTEGER,protocol_code TEXT,base_url TEXT)`,
		`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,enabled INTEGER)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	envelope := testCredentialEnvelope(t, "secret", `{"api_key":"key-1"}`)
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1','openai',1,'openai_responses','https://example.invalid/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts VALUES ('acct-1','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-1','sys-1','openai','profile_openai_openai_v1','openai',3,'active',1,?,NULL)`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-2','sys-2','openai','profile_openai_openai_v1','openai',3,'active',1,?,NULL)`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys-1',4,'quick',82,'fallback')`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	target, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Endpoint != "https://example.invalid/v1" || target.Headers.Get("Authorization") != "Bearer key-1" {
		t.Fatalf("target=%+v headers=%v", target, target.Headers)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-2", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("cross-account target must be rejected")
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", ConfigRevision: "2"}); err == nil || !strings.Contains(err.Error(), "config revision") {
		t.Fatalf("stale config revision err=%v", err)
	}
	request, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if request.PolicyRevision != "4" || request.Threshold != 82 || request.PenaltyAction != "fallback" || request.ConfigRevision != "3" || request.ProviderCode != "openai" {
		t.Fatalf("built request=%+v", request)
	}
}

func TestBusinessTargetSourceCheckContractIsReadOnly(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,enabled INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE groups (id TEXT PRIMARY KEY,enabled INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT)`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenBusinessTargetSourceUsesReadOnlySQLiteURI(t *testing.T) {
	path := t.TempDir() + "/business.db"
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{`CREATE TABLE accounts (id TEXT PRIMARY KEY)`, `CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY)`, `CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,enabled INTEGER)`, `CREATE TABLE groups (id TEXT PRIMARY KEY,enabled INTEGER)`, `CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT)`} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	source, closeSource, err := OpenBusinessTargetSource(context.Background(), Config{Enabled: true, StoreMode: "sqlite", BusinessDatabasePath: path, CredentialSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if source == nil || closeSource == nil {
		t.Fatal("factory returned incomplete source")
	}
	if err := closeSource(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenBusinessTargetConnectionAllowsWritesOnlyAfterHandoff(t *testing.T) {
	path := t.TempDir() + "/business.db"
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{`CREATE TABLE accounts (id TEXT PRIMARY KEY)`, `CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY)`, `CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,enabled INTEGER)`, `CREATE TABLE groups (id TEXT PRIMARY KEY,enabled INTEGER)`, `CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT)`} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	connection, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, StoreMode: "sqlite", BusinessDatabasePath: path, CredentialSecret: "secret", BusinessHandoffConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var queryOnly int
	if err := connection.DB.QueryRow(`PRAGMA query_only`).Scan(&queryOnly); err != nil {
		t.Fatal(err)
	}
	if queryOnly != 0 {
		t.Fatalf("handoff owner connection must not be query-only: %d", queryOnly)
	}
}

func TestOpenBusinessTargetConnectionSharesValidatedHandle(t *testing.T) {
	path := t.TempDir() + "/business.db"
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{`CREATE TABLE accounts (id TEXT PRIMARY KEY)`, `CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY)`, `CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,enabled INTEGER)`, `CREATE TABLE groups (id TEXT PRIMARY KEY,enabled INTEGER)`, `CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT)`} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, StoreMode: "sqlite", BusinessDatabasePath: path, CredentialSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if connection.DB == nil || connection.Source == nil || connection.Source.db != connection.DB {
		t.Fatal("source and connection must share DB handle")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
}

func testCredentialEnvelope(t *testing.T, secret, plaintext string) string {
	t.Helper()
	key := sha256Bytes(secret)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil)
	cut := len(sealed) - gcm.Overhead()
	return strings.Join([]string{"v1", base64.RawURLEncoding.EncodeToString(iv), base64.RawURLEncoding.EncodeToString(sealed[cut:]), base64.RawURLEncoding.EncodeToString(sealed[:cut])}, ":")
}

func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

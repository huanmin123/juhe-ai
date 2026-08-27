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
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,enabled INTEGER)`,
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
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-3','sys-1','openai','profile_openai_openai_v1','openai',4,'active',1,?,NULL)`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts VALUES ('acct-3','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys-1',4,'quick',82,'fallback',15)`); err != nil {
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
	if _, err := db.Exec(`UPDATE accounts SET status='quality_isolated' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("ordinary runs must reject a quality-isolated account")
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", TriggerKind: string(SchedulerQualityRecovery)}); err != nil {
		t.Fatalf("quality recovery must resolve the isolated account: %v", err)
	}
	if _, err := db.Exec(`UPDATE accounts SET status='active' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	request, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if request.PolicyRevision != "4" || request.Threshold != 82 || request.PenaltyAction != "fallback" || request.RecoveryIntervalMinutes != 15 || request.ConfigRevision != "3" || request.ProviderCode != "openai" {
		t.Fatalf("built request=%+v", request)
	}
	if _, err := db.Exec(`UPDATE model_quality_policies SET profile='full' WHERE system_account_id='sys-1'`); err != nil {
		t.Fatal(err)
	}
	comparisonRequest, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", Profile: "full", TrustedComparison: true, TrustedComparisonID: "acct-3"})
	if err != nil {
		t.Fatal(err)
	}
	if !comparisonRequest.TrustedComparison || comparisonRequest.TrustedComparisonAccountID != "acct-3" || comparisonRequest.TrustedComparisonConfigRevision != "4" || !strings.Contains(comparisonRequest.IdentityKey, "comparison:acct-3:4") {
		t.Fatalf("trusted comparison request=%+v", comparisonRequest)
	}
	comparisonTarget, err := source.ComparisonResolver()(context.Background(), comparisonRequest)
	if err != nil || comparisonTarget.ConfigRevision != "4" {
		t.Fatalf("trusted comparison target=%+v err=%v", comparisonTarget, err)
	}
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", Profile: "quick", TrustedComparison: true, TrustedComparisonID: "acct-3"}); err == nil {
		t.Fatal("quick profile trusted comparison must be rejected")
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models VALUES ('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("configured account models must restrict the static catalog")
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra',1)`); err != nil {
		t.Fatal(err)
	}
	mapped, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || mapped.UpstreamModel != "gpt-5.6-terra" {
		t.Fatalf("enabled mapping must resolve the configured upstream model: target=%+v err=%v", mapped, err)
	}
}

func TestBusinessTargetSourceCheckContractIsReadOnly(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
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
	for _, ddl := range businessSourceContractDDL() {
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
	for _, ddl := range businessSourceContractDDL() {
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
	for _, ddl := range businessSourceContractDDL() {
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

func businessSourceContractDDL() []string {
	return []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,config_revision INTEGER,status TEXT,schedulable INTEGER,credentials_encrypted TEXT,deleted_at TEXT)`,
		`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,enabled INTEGER,base_url TEXT)`,
		`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,enabled INTEGER)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,enabled INTEGER)`,
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

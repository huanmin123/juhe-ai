package modelchecksource

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestResolveCredentialMaterialUsesSupportedModeAndConfiguredBaseURL(t *testing.T) {
	const secret = "model-check-reader-credential-secret"
	ciphertext, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_keys":["key-a"],"base_url":"https://provider.example/v1/","supported_endpoint_modes":["responses_json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	material, err := resolveCredentialMaterial(secret, postgresCandidate{
		providerCode: "openai", profileID: "profile_openai_openai_v1", credentialType: "api_key", endpointMode: "responses_json", credentialsEncrypted: ciphertext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if material.baseURL != "https://provider.example/v1" {
		t.Fatalf("baseURL=%q", material.baseURL)
	}
	if _, err := resolveCredentialMaterial(secret, postgresCandidate{
		providerCode: "openai", profileID: "profile_openai_openai_v1", credentialType: "api_key", endpointMode: "chat_json", credentialsEncrypted: ciphertext,
	}); err == nil || !strings.Contains(err.Error(), "endpoint mode") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildProxyEnvelopeKeepsPasswordOutOfCandidateRevision(t *testing.T) {
	const secret = "model-check-reader-proxy-secret"
	password, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"password":"proxy-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	proxy, revision, err := buildProxyEnvelope(secret, postgresCandidate{
		proxyID:                validString("proxy-1"),
		proxyEnabled:           validBool(true),
		proxyType:              validString("socks5"),
		proxyHost:              validString("127.0.0.1"),
		proxyPort:              validInt(1080),
		proxyUsername:          validString("tester"),
		proxyPasswordEncrypted: validString(password),
	})
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil || proxy.Kind != "proxy_url" || revision == "" || strings.Contains(revision, "proxy-secret") {
		t.Fatalf("proxy=%#v revision=%q", proxy, revision)
	}
	plain, err := accounthealth.DecryptV1Envelope(secret, proxy.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["url"] != "socks5h://tester:proxy-secret@127.0.0.1:1080" {
		t.Fatalf("proxy URL=%q", payload["url"])
	}
}

func TestProfileRevisionChangesWithEffectiveProfileState(t *testing.T) {
	base := postgresCandidate{profileID: "profile_openai_openai_v1", providerCode: "openai", protocolCode: "openai", protocolVersion: "v1", profileEnabled: true, profileBaseURL: "https://api.example", profileUpdatedAt: "2026-08-27T00:00:00Z"}
	changed := base
	changed.profileUpdatedAt = "2026-08-27T00:01:00Z"
	if profileRevision(base) == profileRevision(changed) {
		t.Fatal("profile revision did not fence profile change")
	}
}

func TestPostgresReaderContractWithDevDatabase(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("J3B_MODEL_CHECK_DEV_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("J3B_MODEL_CHECK_DEV_POSTGRES_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader, err := NewPostgresReader(db, "dev-contract-secret", "dev-identity-secret", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := reader.CheckContract(ctx); err != nil {
		t.Fatalf("dev PostgreSQL model-check reader contract: %v", err)
	}
}

func TestSQLiteReaderFreezesAndReplaysOwnerCandidateWithoutLeakingCredentials(t *testing.T) {
	const secret = "model-check-sqlite-reader-secret"
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(sqliteReaderFixtureSchema); err != nil {
		t.Fatal(err)
	}
	credentials, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_keys":["sqlite-key"],"base_url":"https://provider.example/v1","supported_endpoint_modes":["responses_json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles(id,enabled,base_url,updated_at) VALUES(?,?,?,?)`, "profile_openai_openai_v1", 1, "https://api.openai.com", "2026-08-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,health_check_endpoint_mode,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,type,credentials_encrypted,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL)`, "account-1", "system-1", 7, "active", 1, "responses_json", "openai", "profile_openai_openai_v1", "openai", "v1", "api_key", credentials); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,enabled) VALUES(?,?,?)`, "group-1", "system-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(system_account_id,group_id,account_id,account_authorization_id,enabled) VALUES(?,?,?,?,?)`, "system-1", "group-1", "account-1", nil, 1); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSQLiteReader(db, secret, "sqlite-identity-secret", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	frozen, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	durable, err := json.Marshal(frozen.DurableAccount)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(durable), "sqlite-key") || strings.Contains(string(durable), "provider.example") || frozen.DurableAccount.ConfigRevision != "7" {
		t.Fatalf("durable snapshot leaked or drifted: %s", durable)
	}
	if frozen.TargetOwnerSystemID != "system-1" || frozen.GroupID != "group-1" {
		t.Fatalf("frozen target metadata=%#v", frozen)
	}
	target, err := reader.Resolve(context.Background(), modelcheckexecutor.ResolutionRequest{
		Input:   modelcheckinput.IssuedInput{SystemAccountID: "system-1", Model: "gpt-5.6-sol", Trigger: modelcheckinput.TriggerManual},
		Account: frozen.DurableAccount,
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.Endpoint != "https://provider.example/v1" || target.Headers.Get("Authorization") != "Bearer sqlite-key" || target.Client == nil {
		t.Fatalf("resolved target=%#v headers=%#v", target, target.Headers)
	}
}

func TestSQLiteReaderMatchesNodeModelCheckAvailabilityBoundary(t *testing.T) {
	const secret = "model-check-sqlite-availability-secret"
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(sqliteReaderFixtureSchema); err != nil {
		t.Fatal(err)
	}
	credentials, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_keys":["sqlite-key"],"base_url":"https://provider.example/v1","supported_endpoint_modes":["responses_json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO provider_protocol_profiles(id,enabled,base_url,updated_at) VALUES(?,?,?,?)`, []any{"profile_openai_openai_v1", 1, "https://api.openai.com", "2026-08-27T00:00:00Z"}},
		{`INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,health_check_endpoint_mode,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,type,credentials_encrypted,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL)`, []any{"account-1", "system-1", 7, "active", 1, "responses_json", "openai", "profile_openai_openai_v1", "openai", "v1", "api_key", credentials}},
		{`INSERT INTO groups(id,system_account_id,enabled) VALUES(?,?,?)`, []any{"group-1", "system-1", 1}},
		{`INSERT INTO group_accounts(system_account_id,group_id,account_id,account_authorization_id,enabled) VALUES(?,?,?,?,?)`, []any{"system-1", "group-1", "account-1", nil, 1}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := NewSQLiteReader(db, secret, "sqlite-identity-secret", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "gpt-5.6-sol"}
	if _, err := reader.FreezeTarget(context.Background(), request); err != nil {
		t.Fatalf("active account rejected: %v", err)
	}
	if _, err := db.Exec(`UPDATE accounts SET status='pending_test' WHERE id='account-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.FreezeTarget(context.Background(), request); err == nil {
		t.Fatal("pending_test account was accepted for normal model check")
	}
	if _, err := db.Exec(`UPDATE accounts SET status='quality_isolated', schedulable=0 WHERE id='account-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.FreezeTarget(context.Background(), request); err == nil {
		t.Fatal("quality isolated account was accepted outside recovery")
	}
	request.AllowQualityIsolated = true
	if _, err := reader.FreezeTarget(context.Background(), request); err != nil {
		t.Fatalf("quality recovery did not accept quality isolated account: %v", err)
	}
}

func TestModelCheckStatusEligible(t *testing.T) {
	for _, test := range []struct {
		status               string
		allowQualityIsolated bool
		want                 bool
	}{
		{status: "active", want: true},
		{status: "temporary_unavailable", want: true},
		{status: "rate_limited", want: true},
		{status: "pending_test", want: false},
		{status: "disabled", want: false},
		{status: "quality_isolated", want: false},
		{status: "quality_isolated", allowQualityIsolated: true, want: true},
	} {
		if got := modelCheckStatusEligible(test.status, test.allowQualityIsolated); got != test.want {
			t.Fatalf("modelCheckStatusEligible(%q, %t)=%t want %t", test.status, test.allowQualityIsolated, got, test.want)
		}
	}
}

const sqliteReaderFixtureSchema = `
CREATE TABLE accounts(
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, config_revision INTEGER NOT NULL,
  status TEXT NOT NULL, schedulable INTEGER NOT NULL, health_check_endpoint_mode TEXT NOT NULL,
  authorization_instance_authorization_id TEXT, authorization_instance_source_account_id TEXT, authorization_instance_owner_system_account_id TEXT,
  name TEXT NOT NULL DEFAULT '',
  provider_code TEXT NOT NULL, provider_protocol_profile_id TEXT NOT NULL, protocol_code TEXT NOT NULL,
  protocol_version TEXT NOT NULL, type TEXT NOT NULL, credentials_encrypted TEXT NOT NULL,
  proxy_profile_id TEXT, last_error_code TEXT, account_expires_at TEXT, cooldown_until TEXT, deleted_at TEXT
);
CREATE TABLE provider_protocol_profiles(id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, base_url TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE group_accounts(system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL, account_authorization_id TEXT, enabled INTEGER NOT NULL, updated_at TEXT);
CREATE TABLE groups(id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL);
CREATE TABLE resource_authorizations(id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, resource_owner_system_account_id TEXT, grantee_system_account_id TEXT, scope TEXT, status TEXT, expires_at TEXT);
CREATE TABLE proxy_profiles(id TEXT PRIMARY KEY, enabled INTEGER, type TEXT, host TEXT, port INTEGER, username TEXT, password_encrypted TEXT);
CREATE TABLE account_supported_models(account_id TEXT NOT NULL, model TEXT NOT NULL);
CREATE TABLE account_model_mappings(account_id TEXT NOT NULL, source_model TEXT NOT NULL, source_endpoint_family TEXT NOT NULL, upstream_model TEXT NOT NULL, upstream_endpoint_family TEXT NOT NULL, enabled INTEGER NOT NULL);
`

func validString(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
func validBool(value bool) sql.NullBool       { return sql.NullBool{Bool: value, Valid: true} }
func validInt(value int64) sql.NullInt64      { return sql.NullInt64{Int64: value, Valid: true} }

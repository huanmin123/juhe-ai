package operationlog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSQLitePersistIsIdempotentAndRetentionIsFenced(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	cfg := Config{Enabled: true, InstanceID: "test-owner", Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, ok, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	input := Input{ID: "oplog-test-1", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", ResourceID: "account-1", Summary: "updated account", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	ignored, err := store.Persist(context.Background(), lease, input)
	if err != nil || ignored {
		t.Fatalf("first persist: ignored=%v err=%v", ignored, err)
	}
	ignored, err = store.Persist(context.Background(), lease, input)
	if err != nil || !ignored {
		t.Fatalf("duplicate persist: ignored=%v err=%v", ignored, err)
	}
	concrete := store.(*sqlStore)
	var targets int
	if err = concrete.db.QueryRow(`SELECT COUNT(*) FROM operation_log_targets WHERE operation_log_id=?`, input.ID).Scan(&targets); err != nil || targets != 1 {
		t.Fatalf("duplicate must not duplicate target rows: targets=%d err=%v", targets, err)
	}
	result, err := store.List(context.Background(), ListOptions{})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("list: %+v err=%v", result, err)
	}
	if result.Items[0].ActorSystemAccountName != "Actor 1" {
		t.Fatalf("list must preserve actor account name projection: %+v", result.Items[0])
	}
	detail, found, err := store.Detail(context.Background(), input.ID, "actor-1")
	if err != nil || !found || len(detail.Targets) != 1 {
		t.Fatalf("detail: found=%v detail=%+v err=%v", found, detail, err)
	}
	deleted, err := store.CleanupRetention(context.Background(), lease, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || deleted != 1 {
		t.Fatalf("retention: deleted=%d err=%v", deleted, err)
	}
}

func TestSQLiteCreatedAtIsCanonicalUTCForOrderAndRetention(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	opened, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	lease, ok, err := opened.AcquireOwnerLease(context.Background(), "utc-order", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	older := Input{ID: "utc-older", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "older", CreatedAt: "2026-08-13T08:00:00.100000000+08:00"}
	later := older
	later.ID = "utc-later"
	later.Summary = "later"
	later.CreatedAt = "2026-08-13T00:00:00.110000000Z"
	if _, err = opened.Persist(context.Background(), lease, older); err != nil {
		t.Fatal(err)
	}
	if _, err = opened.Persist(context.Background(), lease, later); err != nil {
		t.Fatal(err)
	}
	var storedCreatedAt string
	if err = opened.(*sqlStore).db.QueryRow(`SELECT created_at FROM operation_logs WHERE id=?`, older.ID).Scan(&storedCreatedAt); err != nil || storedCreatedAt != "2026-08-13T00:00:00.100000000Z" {
		t.Fatalf("createdAt must use fixed-width UTC storage: value=%q err=%v", storedCreatedAt, err)
	}
	result, err := opened.List(context.Background(), ListOptions{})
	if err != nil || len(result.Items) != 2 || result.Items[0].ID != later.ID {
		t.Fatalf("UTC canonical ordering result=%+v err=%v", result, err)
	}
	deleted, err := opened.CleanupRetention(context.Background(), lease, time.Date(2026, 8, 13, 0, 0, 0, 105000000, time.UTC), 10)
	if err != nil || deleted != 1 {
		t.Fatalf("UTC canonical retention deleted=%d err=%v", deleted, err)
	}
	result, err = opened.List(context.Background(), ListOptions{})
	if err != nil || len(result.Items) != 1 || result.Items[0].ID != later.ID {
		t.Fatalf("UTC canonical retention result=%+v err=%v", result, err)
	}
}

func TestLoadConfigRejectsUsageShardPhysicalOverlap(t *testing.T) {
	root := t.TempDir()
	shardRoot := filepath.Join(root, "usage-shards")
	shard := filepath.Join(shardRoot, "2026", "08", "13", "usage-20260813-s0.sqlite3")
	if err := os.MkdirAll(filepath.Dir(shard), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shard, []byte("shard"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{
		"JUHE_AI_OPERATION_LOG_STORE":                  "sqlite",
		"JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS":   "127.0.0.1:3304",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID":            "f4-test",
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH": filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_USAGE_SHARD_ROOT":                     shardRoot,
	}
	load := func(databasePath string) error {
		values := make(map[string]string, len(base))
		for key, value := range base {
			values[key] = value
		}
		values["JUHE_AI_OPERATION_LOG_DATABASE_PATH"] = databasePath
		_, err := LoadConfig(func(key string) string { return values[key] })
		return err
	}
	if err := load(filepath.Join(shardRoot, "operation.sqlite3")); err == nil {
		t.Fatal("F4 database inside usage shard root must be rejected")
	}
	hardLink := filepath.Join(root, "operation-hardlink.sqlite3")
	if err := os.Link(shard, hardLink); err != nil {
		t.Fatalf("create usage shard hardlink: %v", err)
	}
	if err := load(hardLink); err == nil {
		t.Fatal("F4 database hardlink to a usage shard must be rejected")
	}
	if runtime.GOOS != "windows" {
		linkRoot := filepath.Join(root, "usage-link")
		if err := os.Symlink(shardRoot, linkRoot); err != nil {
			t.Fatal(err)
		}
		base["JUHE_AI_USAGE_SHARD_ROOT"] = linkRoot
		if err := load(filepath.Join(root, "operation.sqlite3")); err == nil {
			t.Fatal("symlink usage shard root must be rejected")
		}

		base["JUHE_AI_USAGE_SHARD_ROOT"] = shardRoot
		operation := filepath.Join(root, "operation.sqlite3")
		if err := os.WriteFile(operation, []byte("operation"), 0o600); err != nil {
			t.Fatal(err)
		}
		childLink := filepath.Join(filepath.Dir(shard), "usage-20260813-symlink.sqlite3")
		if err := os.Symlink(operation, childLink); err != nil {
			t.Fatal(err)
		}
		if err := load(operation); err == nil {
			t.Fatal("symlink inside usage shard root must be rejected")
		}
	}
}

func TestLoadConfigAppliesBoundedRuntimeSettings(t *testing.T) {
	root := t.TempDir()
	values := map[string]string{
		"JUHE_AI_OPERATION_LOG_STORE":                  "sqlite",
		"JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS":   "127.0.0.1:3304",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID":            "f4-config",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH":          filepath.Join(root, "operation.sqlite3"),
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH": filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_USAGE_SHARD_ROOT":                     filepath.Join(root, "usage-shards"),
		"JUHE_AI_OPERATION_LOG_OWNER_LEASE":            "45s",
		"JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL":     "2m",
		"JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE":   "321",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil || cfg.OwnerLease != 45*time.Second || cfg.RetentionInterval != 2*time.Minute || cfg.RetentionBatchSize != 321 {
		t.Fatalf("F4 runtime settings cfg=%+v err=%v", cfg, err)
	}
	values["JUHE_AI_OPERATION_LOG_OWNER_LEASE"] = "4s"
	if _, err = LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("short F4 owner lease must fail")
	}
}

func TestHTTPBusinessFailureKeepsListenerHealthy(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	cfg := Config{Enabled: true, InstanceID: "test-http-owner", Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, ok, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	h := &handler{store: store, lease: lease, cfg: InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second}, logger: slog.Default(), healthy: newAtomicTrue()}
	badBody, _ := json.Marshal(envelope{SchemaVersion: 1, OperationLog: Input{ID: "bad", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}})
	bad := httptest.NewRequest(http.MethodPost, InputPath, bytes.NewReader(badBody))
	bad.RemoteAddr = "127.0.0.1:1"
	signRequest(bad, "test-secret", badBody, time.Now().UTC(), "bad-request")
	badResponse := httptest.NewRecorder()
	h.ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusInternalServerError {
		t.Fatalf("business failure status=%d", badResponse.Code)
	}
	if !h.healthy.Load() {
		t.Fatal("business failure must not mark listener unhealthy")
	}
	valid := Input{ID: "good", ActorSystemAccountID: "actor", ActorRole: "user", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", Summary: "good", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	validBody, _ := json.Marshal(envelope{SchemaVersion: 1, OperationLog: valid})
	invalidSignature := httptest.NewRequest(http.MethodPost, InputPath, bytes.NewReader(validBody))
	invalidSignature.RemoteAddr = "127.0.0.1:1"
	invalidSignature.Header.Set("Content-Type", "application/json")
	invalidSignature.Header.Set(TimestampHeader, time.Now().UTC().Format(time.RFC3339Nano))
	invalidSignature.Header.Set(NonceHeader, "invalid-signature")
	invalidSignature.Header.Set(SignatureHeader, "v1=invalid")
	invalidResponse := httptest.NewRecorder()
	h.ServeHTTP(invalidResponse, invalidSignature)
	if invalidResponse.Code != http.StatusUnauthorized || !h.healthy.Load() {
		t.Fatalf("signature rejection status=%d healthy=%v", invalidResponse.Code, h.healthy.Load())
	}
	validRequest := httptest.NewRequest(http.MethodPost, InputPath, bytes.NewReader(validBody))
	validRequest.RemoteAddr = "127.0.0.1:1"
	signRequest(validRequest, "test-secret", validBody, time.Now().UTC(), "valid-request")
	validResponse := httptest.NewRecorder()
	h.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusNoContent {
		t.Fatalf("subsequent valid request status=%d", validResponse.Code)
	}
}

func TestHTTPLeaseLossSignalsComponentRestart(t *testing.T) {
	fatal := make(chan error, 1)
	h := &handler{cfg: InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes}, logger: slog.Default(), healthy: newAtomicTrue(), fatal: fatal, store: &leaseLostStore{}, lease: OwnerLease{OwnerID: "lost", FenceToken: 1}}
	body, _ := json.Marshal(envelope{SchemaVersion: 1, OperationLog: Input{ID: "lease-lost", ActorSystemAccountID: "actor", ActorRole: "user", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", Summary: "lost", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}})
	request := httptest.NewRequest(http.MethodPost, InputPath, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1"
	signRequest(request, "test-secret", body, time.Now().UTC(), "lease-lost")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || h.healthy.Load() {
		t.Fatalf("lease loss status=%d healthy=%v", response.Code, h.healthy.Load())
	}
	select {
	case err := <-fatal:
		if !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("fatal=%v", err)
		}
	default:
		t.Fatal("lease loss must notify the component loop")
	}
}

func TestHTTPRejectsExpiredAndReplayedSignedRequests(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	store, err := OpenStore(Config{Enabled: true, InstanceID: "replay", Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "replay", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	h := &handler{store: store, lease: lease, cfg: InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second, ReplayWindow: time.Minute}, logger: slog.Default(), healthy: newAtomicTrue()}
	valid := Input{ID: "replay", ActorSystemAccountID: "actor", ActorRole: "user", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", Summary: "good", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	body, _ := json.Marshal(envelope{SchemaVersion: 1, OperationLog: valid})
	expired := httptest.NewRequest(http.MethodPost, InputPath, bytes.NewReader(body))
	expired.RemoteAddr = "127.0.0.1:1"
	signRequest(expired, "test-secret", body, time.Now().UTC().Add(-2*time.Minute), "expired")
	expiredResponse := httptest.NewRecorder()
	h.ServeHTTP(expiredResponse, expired)
	if expiredResponse.Code != http.StatusUnauthorized {
		t.Fatal("expired request must be rejected")
	}
	first := httptest.NewRequest(http.MethodPost, InputPath, bytes.NewReader(body))
	first.RemoteAddr = "127.0.0.1:1"
	signRequest(first, "test-secret", body, time.Now().UTC(), "same-nonce")
	firstResponse := httptest.NewRecorder()
	h.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusNoContent {
		t.Fatalf("first request status=%d", firstResponse.Code)
	}
	second := httptest.NewRequest(http.MethodPost, InputPath, bytes.NewReader(body))
	second.RemoteAddr = "127.0.0.1:1"
	signRequest(second, "test-secret", body, time.Now().UTC(), "same-nonce")
	secondResponse := httptest.NewRecorder()
	h.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replay status=%d", secondResponse.Code)
	}
}

func TestHTTPHandlersInheritRequestContext(t *testing.T) {
	assertCanceled := func(ctx context.Context) error {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("store context must inherit canceled request context: %v", ctx.Err())
		}
		return ctx.Err()
	}
	store := &requestContextStore{persist: assertCanceled, list: assertCanceled, detail: assertCanceled}
	h := &handler{store: store, cfg: InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second, ReplayWindow: time.Minute}, logger: slog.Default(), healthy: newAtomicTrue()}
	inputBody, _ := json.Marshal(envelope{SchemaVersion: 1, OperationLog: Input{ID: "context-write"}})
	for _, test := range []struct {
		path  string
		body  []byte
		nonce string
	}{
		{InputPath, inputBody, "context-write"},
		{ListPath, []byte(`{"options":{}}`), "context-list"},
		{DetailPath + "context-detail", []byte(`{}`), "context-detail"},
	} {
		requestCtx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(test.body)).WithContext(requestCtx)
		request.RemoteAddr = "127.0.0.1:1"
		signRequest(request, "test-secret", test.body, time.Now().UTC(), test.nonce)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("%s canceled request status=%d", test.path, response.Code)
		}
	}
}

func TestViewerListAndSummaryDetailArePermissionBounded(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "12")
	store, err := OpenStore(Config{Enabled: true, InstanceID: "viewer", Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "viewer", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.Persist(context.Background(), lease, Input{ID: "visible", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "visible", CreatedAt: now, Viewers: []Viewer{{SystemAccountID: "viewer", VisibilityReason: "actor_self", DetailLevel: "summary"}, {SystemAccountID: "viewer", VisibilityReason: "resource_owner", DetailLevel: "full"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Persist(context.Background(), lease, Input{ID: "hidden", ActorSystemAccountID: "other", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "hidden", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.List(context.Background(), ListOptions{ViewerID: "viewer", Module: "all"})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != "visible" {
		t.Fatalf("viewer list=%+v err=%v", list, err)
	}
	_, err = store.Persist(context.Background(), lease, Input{ID: "global", ActorSystemAccountID: "other", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "global trace-abc", TraceID: "trace-abc-001", VisibilityScope: "all_users", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	adminFiltered, err := store.List(context.Background(), ListOptions{AffectedSystemAccountID: "viewer"})
	if err != nil || len(adminFiltered.Items) != 2 || adminFiltered.Items[0].ID != "visible" || adminFiltered.Items[1].ID != "global" {
		t.Fatalf("all_users affected-account filter=%+v err=%v", adminFiltered, err)
	}
	allAffected, err := store.List(context.Background(), ListOptions{AffectedSystemAccountID: "all"})
	if err != nil || len(allAffected.Items) != 3 {
		t.Fatalf("affected=all must be ignored: result=%+v err=%v", allAffected, err)
	}
	traceFiltered, err := store.List(context.Background(), ListOptions{TraceID: "trace-abc"})
	if err != nil || len(traceFiltered.Items) != 1 || traceFiltered.Items[0].ID != "global" {
		t.Fatalf("trace prefix filter=%+v err=%v", traceFiltered, err)
	}
	invalidKeyword, err := store.List(context.Background(), ListOptions{SummaryKeyword: "!!!"})
	if err != nil || len(invalidKeyword.Items) != 0 {
		t.Fatalf("invalid summary keyword must return no rows: %+v err=%v", invalidKeyword, err)
	}
	nfkc, err := store.List(context.Background(), ListOptions{SummaryKeyword: "Ｇｌｏｂａｌ"})
	if err != nil || len(nfkc.Items) != 1 || nfkc.Items[0].ID != "global" {
		t.Fatalf("NFKC summary keyword=%+v err=%v", nfkc, err)
	}
	window, err := store.List(context.Background(), ListOptions{Page: 99999, PageSize: 50})
	if err != nil || window.Page != 20 {
		t.Fatalf("page must stay inside 1001-row window: %+v err=%v", window, err)
	}
	detail, found, err := store.Detail(context.Background(), "visible", "viewer")
	if err != nil || !found || len(detail.Changes) != 0 || detail.Method != "" {
		t.Fatalf("summary detail=%+v found=%v err=%v", detail, found, err)
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil || bytes.Contains(detailJSON, []byte(`"targets":null`)) || bytes.Contains(detailJSON, []byte(`"viewers":null`)) || !bytes.Contains(detailJSON, []byte(`"targets":[]`)) || !bytes.Contains(detailJSON, []byte(`"viewers":[]`)) {
		t.Fatalf("summary detail JSON must preserve array DTO shape: body=%s err=%v", detailJSON, err)
	}
	if days, err := store.RetentionDays(context.Background(), 365); err != nil || days != 12 {
		t.Fatalf("retention setting=%d err=%v", days, err)
	}
	businessWriter, err := sql.Open("sqlite", business)
	if err != nil {
		t.Fatal(err)
	}
	defer businessWriter.Close()
	if _, err := businessWriter.Exec(`UPDATE system_settings SET value_json='0' WHERE system_account_id='sys_admin' AND key='operationLogRetentionDays'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetentionDays(context.Background(), 365); err == nil {
		t.Fatal("invalid business setting must not silently fall back")
	}
}

func createBusinessSettings(t *testing.T, path, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,key)); CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT NOT NULL); INSERT INTO system_accounts VALUES ('actor','actor','Actor'),('other','other','Other'),('viewer','viewer','Viewer'),('owner','owner','Owner'),('actor-1','actor-1','Actor 1'); INSERT INTO system_settings VALUES ('sys_admin','operationLogRetentionDays', ?, '2026-08-13T00:00:00Z')`, value)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRetentionSettingContract(t *testing.T) {
	query := "SELECT value_json FROM juhe_business.system_settings WHERE system_account_id=$1 AND key=$2"
	if query != fmt.Sprintf("SELECT value_json FROM %s.system_settings WHERE system_account_id=$1 AND key=$2", "juhe_business") {
		t.Fatal("postgres business settings query contract changed")
	}
}

type schemaCatalogStub struct {
	strings             map[string]string
	bools               map[string]bool
	compositeForeignKey bool
}

func (s schemaCatalogStub) String(_ context.Context, query string, args ...any) (string, error) {
	if strings.Contains(query, "format_type") {
		return s.strings[args[0].(string)+"."+args[1].(string)], nil
	}
	if strings.Contains(query, "pg_get_indexdef") {
		return s.strings["index."+args[0].(string)], nil
	}
	return s.strings["pk."+args[0].(string)], nil
}

func (s schemaCatalogStub) Bool(_ context.Context, query string, args ...any) (bool, error) {
	if strings.Contains(query, "attnotnull") {
		return s.bools["notnull."+args[0].(string)+"."+args[1].(string)], nil
	}
	if strings.Contains(query, "pg_constraint") {
		if s.compositeForeignKey && strings.Contains(query, "cardinality(c.conkey)=1") && strings.Contains(query, "cardinality(c.confkey)=1") {
			return false, nil
		}
		return s.bools["fk."+args[0].(string)], nil
	}
	return s.bools["idx."+args[0].(string)], nil
}

func validSchemaCatalogStub() schemaCatalogStub {
	stub := schemaCatalogStub{strings: map[string]string{}, bools: map[string]bool{}}
	for table, columns := range postgresSchemaColumns {
		for _, column := range columns {
			stub.strings["juhe_dataset."+table+"."+column.name] = column.typeName
		}
		stub.strings["pk.juhe_dataset."+table] = postgresPrimaryKeys[table]
		for _, column := range postgresRequiredNotNull[table] {
			stub.bools["notnull.juhe_dataset."+table+"."+column] = true
		}
	}
	for _, table := range postgresForeignKeyTables {
		stub.bools["fk.juhe_dataset."+table] = true
	}
	for index, definition := range postgresRequiredIndexDefinitions {
		stub.strings["index."+index] = definition
	}
	return stub
}

func TestPostgresSchemaValidationRejectsSameNameIncompatibleIndex(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.strings["index.idx_operation_log_targets_target"] = "CREATE INDEX idx_operation_log_targets_target ON juhe_dataset.operation_log_targets USING btree (target_id, created_at)"
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "idx_operation_log_targets_target definition") {
		t.Fatalf("same-name incompatible F4 index must fail closed: %v", err)
	}
}

func TestPostgresSchemaValidationRejectsNullableRequiredColumn(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.bools["notnull.juhe_dataset.operation_logs.summary"] = false
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "operation_logs.summary must be NOT NULL") {
		t.Fatalf("nullable F4 required column must fail closed: %v", err)
	}
}

func TestPostgresSchemaValidationRejectsLegacyViewerPrimaryKey(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.strings["pk.juhe_dataset.operation_log_viewers"] = "operation_log_id,system_account_id,visibility_reason"
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "operation_log_viewers primary key") || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("legacy Node viewer primary key must fail closed: %v", err)
	}
}

func TestPostgresSchemaValidationRejectsUnrelatedCascadeForeignKey(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.bools["fk.juhe_dataset.operation_log_targets"] = false
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "operation_log_targets must have an exact single-column") {
		t.Fatalf("unrelated cascade foreign key must not satisfy F4 operation_log_id->id contract: %v", err)
	}
}

func TestPostgresSchemaValidationRejectsCompositeForeignKey(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.compositeForeignKey = true
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "operation_log_targets must have an exact single-column") {
		t.Fatalf("composite foreign key must not satisfy F4 operation_log_id->id contract: %v", err)
	}
}

func TestPostgresOperationLogStoreSmoke(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("JUHE_AI_OPERATION_LOG_POSTGRES_SMOKE_URL"))
	if url == "" {
		t.Skip("未设置 JUHE_AI_OPERATION_LOG_POSTGRES_SMOKE_URL；F4 PostgreSQL smoke 未执行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: url})
	if err != nil {
		t.Fatalf("打开 F4 PostgreSQL store 失败: %v", redactPostgresSmokeError(err, url))
	}
	store := opened.(*sqlStore)
	t.Cleanup(func() { _ = store.Close() })
	var applicationName string
	if err = store.db.QueryRowContext(ctx, "SHOW application_name").Scan(&applicationName); err != nil || applicationName != postgresApplicationName {
		t.Fatalf("F4 PostgreSQL application_name=%q want %q err=%v", applicationName, postgresApplicationName, redactPostgresSmokeError(err, url))
	}
	timeoutTx, err := store.beginTx(ctx)
	if err != nil {
		t.Fatalf("开始 F4 PostgreSQL timeout transaction 失败: %v", redactPostgresSmokeError(err, url))
	}
	for setting, expected := range map[string]string{
		"statement_timeout":                   postgresStatementTimeout,
		"lock_timeout":                        postgresLockTimeout,
		"idle_in_transaction_session_timeout": postgresIdleTransactionTimeout,
	} {
		var actual string
		if err = timeoutTx.QueryRowContext(ctx, "SHOW "+setting).Scan(&actual); err != nil || actual != expected {
			_ = timeoutTx.Rollback()
			t.Fatalf("F4 PostgreSQL transaction %s=%q want %q err=%v", setting, actual, expected, redactPostgresSmokeError(err, url))
		}
	}
	if err = timeoutTx.Rollback(); err != nil {
		t.Fatalf("结束 F4 PostgreSQL timeout transaction 失败: %v", redactPostgresSmokeError(err, url))
	}
	if _, err = store.db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business; CREATE TABLE IF NOT EXISTS juhe_business.system_settings (system_account_id text NOT NULL,key text NOT NULL,value_json jsonb NOT NULL,updated_at timestamptz NOT NULL,PRIMARY KEY(system_account_id,key)); CREATE TABLE IF NOT EXISTS juhe_business.system_accounts (id text PRIMARY KEY,username text NOT NULL,display_name text NOT NULL)`); err != nil {
		t.Fatalf("初始化 F4 smoke business schema 失败: %v", redactPostgresSmokeError(err, url))
	}
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatalf("初始化 F4 PostgreSQL schema 失败: %v", redactPostgresSmokeError(err, url))
	}
	for _, table := range []string{"operation_logs", "operation_log_targets", "operation_log_viewers", "operation_log_summary_search_terms", "operation_log_owner_leases"} {
		var count int
		if err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM juhe_dataset."+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("F4 PostgreSQL smoke 必须使用空的专用数据库：table=%s count=%d err=%v", table, count, redactPostgresSmokeError(err, url))
		}
	}
	if _, err = store.db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts (id,username,display_name) VALUES ('actor','actor','Actor'),('viewer','viewer','Viewer'),('owner','owner','Owner') ON CONFLICT (id) DO UPDATE SET username=EXCLUDED.username,display_name=EXCLUDED.display_name; INSERT INTO juhe_business.system_settings (system_account_id,key,value_json,updated_at) VALUES ('sys_admin','operationLogRetentionDays','12'::jsonb,clock_timestamp()) ON CONFLICT (system_account_id,key) DO UPDATE SET value_json=EXCLUDED.value_json,updated_at=EXCLUDED.updated_at`); err != nil {
		t.Fatalf("写入 F4 smoke business settings 失败: %v", redactPostgresSmokeError(err, url))
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "f4-postgres-smoke-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("F4 PostgreSQL owner lease 获取失败：acquired=%v err=%v", acquired, redactPostgresSmokeError(err, url))
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	if _, acquired, err = store.AcquireOwnerLease(ctx, "f4-postgres-smoke-other", time.Minute); err != nil || acquired {
		t.Fatalf("未过期的 F4 PostgreSQL owner lease 不应被夺取：acquired=%v err=%v", acquired, redactPostgresSmokeError(err, url))
	}
	visible := Input{ID: "f4-postgres-visible", ActorSystemAccountID: "actor", ActorRole: "user", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", ResourceID: "account-1", Summary: "更新 Alpha 账户", ClientIP: "203.0.113.1", CreatedAt: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano), Targets: []Target{{TargetType: "account", TargetID: "account-1", TargetOwnerSystemAccountID: "owner", Relation: "primary"}}, Viewers: []Viewer{{SystemAccountID: "viewer", VisibilityReason: "actor_self", DetailLevel: "full"}}}
	ignored, err := store.Persist(ctx, lease, visible)
	if err != nil || ignored {
		t.Fatalf("F4 PostgreSQL 首次写入失败：ignored=%v err=%v", ignored, redactPostgresSmokeError(err, url))
	}
	ignored, err = store.Persist(ctx, lease, visible)
	if err != nil || !ignored {
		t.Fatalf("F4 PostgreSQL 稳定 ID 幂等失败：ignored=%v err=%v", ignored, redactPostgresSmokeError(err, url))
	}
	var targetCount int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.operation_log_targets WHERE operation_log_id=$1`, visible.ID).Scan(&targetCount); err != nil || targetCount != 1 {
		t.Fatalf("F4 PostgreSQL 幂等写入重复了 targets：count=%d err=%v", targetCount, redactPostgresSmokeError(err, url))
	}
	list, err := store.List(ctx, ListOptions{ViewerID: "viewer", Module: "all", SummaryKeyword: "pha 账"})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != visible.ID {
		t.Fatalf("F4 PostgreSQL viewer/search list 失败：result=%+v err=%v", list, redactPostgresSmokeError(err, url))
	}
	allAffected, err := store.List(ctx, ListOptions{AffectedSystemAccountID: "all"})
	if err != nil || len(allAffected.Items) != 1 || allAffected.Items[0].ID != visible.ID {
		t.Fatalf("F4 PostgreSQL affected=all list 失败：result=%+v err=%v", allAffected, redactPostgresSmokeError(err, url))
	}
	targetedSameSecond := visible
	targetedSameSecond.ID = "f4-postgres-targeted-same-second"
	targetedSameSecond.Summary = "targeted same second"
	targetedSameSecond.CreatedAt = "2026-08-13T00:00:00.100000000Z"
	if _, err = store.Persist(ctx, lease, targetedSameSecond); err != nil {
		t.Fatalf("F4 PostgreSQL targeted same-second 写入失败：%v", redactPostgresSmokeError(err, url))
	}
	globalSameSecond := targetedSameSecond
	globalSameSecond.ID = "f4-postgres-global-same-second"
	globalSameSecond.Summary = "global same second"
	globalSameSecond.CreatedAt = "2026-08-13T00:00:00.110000000Z"
	globalSameSecond.VisibilityScope = "all_users"
	globalSameSecond.Viewers = nil
	if _, err = store.Persist(ctx, lease, globalSameSecond); err != nil {
		t.Fatalf("F4 PostgreSQL all-users same-second 写入失败：%v", redactPostgresSmokeError(err, url))
	}
	personalSameSecond, err := store.List(ctx, ListOptions{ViewerID: "viewer"})
	if err != nil || len(personalSameSecond.Items) < 2 || personalSameSecond.Items[0].ID != globalSameSecond.ID || personalSameSecond.Items[1].ID != targetedSameSecond.ID {
		t.Fatalf("F4 PostgreSQL personal same-second 排序失败：result=%+v err=%v", personalSameSecond, redactPostgresSmokeError(err, url))
	}
	detail, found, err := store.Detail(ctx, visible.ID, "viewer")
	detailJSON, _ := json.Marshal(detail)
	if err != nil || !found || detail.ClientIP != "" || len(detail.Targets) != 1 || detail.Targets[0].TargetOwnerSystemAccountName != "Owner" || bytes.Contains(detailJSON, []byte("targetOwnerSystemAccountId")) {
		t.Fatalf("F4 PostgreSQL 个人详情裁剪失败：detail=%+v found=%v err=%v", detail, found, redactPostgresSmokeError(err, url))
	}
	lockDB, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("打开 F4 PostgreSQL lock probe 失败: %v", redactPostgresSmokeError(err, url))
	}
	defer lockDB.Close()
	lockTx, err := lockDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("打开 F4 PostgreSQL lock transaction 失败: %v", redactPostgresSmokeError(err, url))
	}
	if _, err = lockTx.ExecContext(ctx, "LOCK TABLE juhe_dataset.operation_logs IN ACCESS EXCLUSIVE MODE"); err != nil {
		_ = lockTx.Rollback()
		t.Fatalf("锁定 F4 PostgreSQL operation_logs 失败: %v", redactPostgresSmokeError(err, url))
	}
	blocked := visible
	blocked.ID = "f4-postgres-lock-timeout"
	started := time.Now()
	_, persistErr := store.Persist(ctx, lease, blocked)
	elapsed := time.Since(started)
	_ = lockTx.Rollback()
	if persistErr == nil || elapsed >= 4*time.Second {
		t.Fatalf("F4 PostgreSQL lock timeout 未生效：err=%v elapsed=%s", redactPostgresSmokeError(persistErr, url), elapsed)
	}
	expired := visible
	expired.ID = "f4-postgres-expired"
	expired.CreatedAt = "2020-01-01T00:00:00Z"
	if _, err = store.Persist(ctx, lease, expired); err != nil {
		t.Fatalf("F4 PostgreSQL 过期记录写入失败: %v", redactPostgresSmokeError(err, url))
	}
	deleted, err := store.CleanupRetention(ctx, lease, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), 100)
	if err != nil || deleted != 1 {
		t.Fatalf("F4 PostgreSQL 保留清理失败：deleted=%d err=%v", deleted, redactPostgresSmokeError(err, url))
	}
	if days, err := store.RetentionDays(ctx, 365); err != nil || days != 12 {
		t.Fatalf("F4 PostgreSQL operationLogRetentionDays 读取失败：days=%d err=%v", days, redactPostgresSmokeError(err, url))
	}
	var targetIndex bool
	if err = store.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='juhe_dataset' AND tablename='operation_log_targets' AND indexname='idx_operation_log_targets_log')`).Scan(&targetIndex); err != nil || !targetIndex {
		t.Fatalf("F4 PostgreSQL operation_log_targets 外键索引缺失：exists=%v err=%v", targetIndex, redactPostgresSmokeError(err, url))
	}
}

func TestPostgresLegacyOperationLogMigrationSmoke(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("JUHE_AI_OPERATION_LOG_POSTGRES_LEGACY_MIGRATION_SMOKE_URL"))
	if url == "" {
		t.Skip("未设置 JUHE_AI_OPERATION_LOG_POSTGRES_LEGACY_MIGRATION_SMOKE_URL；F4 PostgreSQL 历史迁移 smoke 未执行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	opened, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: url})
	if err != nil {
		t.Fatalf("打开 F4 PostgreSQL 历史迁移 smoke store 失败: %v", redactPostgresSmokeError(err, url))
	}
	store := opened.(*sqlStore)
	t.Cleanup(func() { _ = store.Close() })
	if _, err = store.db.ExecContext(ctx, `
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE TABLE juhe_dataset.operation_logs (id text PRIMARY KEY,trace_id text,actor_system_account_id text NOT NULL,actor_username text,actor_display_name text,actor_role text NOT NULL,operation_scope_system_account_id text,mode text NOT NULL,module text NOT NULL,action text NOT NULL,operation_key text NOT NULL,resource_type text NOT NULL,resource_id text,resource_name text,summary text NOT NULL,detail_level text NOT NULL,visibility_scope text NOT NULL,changes_json jsonb NOT NULL,metadata_json jsonb NOT NULL,method text,path text,status_code integer,client_ip text,user_agent text,created_at timestamptz NOT NULL);
CREATE TABLE juhe_dataset.operation_log_targets (id text PRIMARY KEY,operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,target_type text NOT NULL,target_id text,target_name text,target_owner_system_account_id text,relation text NOT NULL,created_at timestamptz NOT NULL);
CREATE TABLE juhe_dataset.operation_log_viewers (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,system_account_id text NOT NULL,visibility_reason text NOT NULL,detail_level text NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(operation_log_id,system_account_id,visibility_reason));
CREATE TABLE juhe_dataset.operation_log_summary_search_terms (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,term text NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(term,operation_log_id));
INSERT INTO juhe_dataset.operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,summary,detail_level,visibility_scope,changes_json,metadata_json,method,path,status_code,created_at) VALUES ('legacy-oplog','actor','admin','self','accounts','update','accounts.update','account','legacy row','full','targeted','[{"field":"enabled","before":false,"after":true}]'::jsonb,'{"source":"legacy","flags":["x"]}'::jsonb,'PATCH','/__aisys__/api/accounts/1',202,clock_timestamp());
INSERT INTO juhe_dataset.operation_log_targets VALUES ('legacy-target','legacy-oplog','account','account-1','Legacy','owner','primary',clock_timestamp());
INSERT INTO juhe_dataset.operation_log_viewers VALUES ('legacy-oplog','actor','actor_self','full',clock_timestamp());
INSERT INTO juhe_dataset.operation_log_summary_search_terms VALUES ('legacy-oplog','legacy',clock_timestamp());
INSERT INTO juhe_dataset.operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,summary,detail_level,visibility_scope,changes_json,metadata_json,created_at)
SELECT 'legacy-batch-' || gs::text,'actor','admin','self','accounts','update','accounts.update','account','legacy batch row ' || gs::text,'full','all_users','[]'::jsonb,'{}'::jsonb,clock_timestamp() + gs * interval '1 microsecond'
FROM generate_series(1,250) AS gs;`); err != nil {
		t.Fatalf("初始化旧 Node F4 PostgreSQL schema 失败: %v", redactPostgresSmokeError(err, url))
	}
	timeoutTx, err := store.beginLegacyMigrationTx(ctx)
	if err != nil {
		t.Fatalf("创建 F4 PostgreSQL 历史迁移事务失败: %v", redactPostgresSmokeError(err, url))
	}
	for setting, expected := range map[string]string{"statement_timeout": legacyMigrationStatementTimeout, "lock_timeout": legacyMigrationLockTimeout, "idle_in_transaction_session_timeout": legacyMigrationIdleTimeout} {
		var actual string
		if err = timeoutTx.QueryRowContext(ctx, "SHOW "+setting).Scan(&actual); err != nil || actual != expected {
			_ = timeoutTx.Rollback()
			t.Fatalf("F4 PostgreSQL 历史迁移 %s=%q，want %q, err=%v", setting, actual, expected, redactPostgresSmokeError(err, url))
		}
	}
	if err = timeoutTx.Rollback(); err != nil {
		t.Fatalf("回滚 F4 PostgreSQL 历史迁移超时检查失败: %v", redactPostgresSmokeError(err, url))
	}
	factTx, err := store.beginLegacyMigrationTx(ctx)
	if err != nil {
		t.Fatalf("创建 F4 PostgreSQL 迁移事实校验事务失败: %v", redactPostgresSmokeError(err, url))
	}
	samples, err := snapshotPostgresLegacySamples(ctx, factTx)
	if err == nil {
		_, err = factTx.ExecContext(ctx, `UPDATE juhe_dataset.operation_logs SET summary='tampered before migration' WHERE id='legacy-oplog'`)
	}
	if err == nil {
		err = verifyPostgresLegacySamples(ctx, factTx, samples)
	}
	if rollbackErr := factTx.Rollback(); rollbackErr != nil {
		t.Fatalf("回滚 F4 PostgreSQL 迁移事实校验失败: %v", redactPostgresSmokeError(rollbackErr, url))
	}
	if err == nil || !strings.Contains(err.Error(), "业务事实不一致") {
		t.Fatalf("F4 PostgreSQL 迁移必须拒绝被篡改的首尾业务事实: %v", redactPostgresSmokeError(err, url))
	}
	result, err := MigrateLegacyPostgres(ctx, Config{Mode: ModePostgres, PostgresURL: url}, LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true})
	if err != nil {
		t.Fatalf("F4 PostgreSQL 历史迁移失败: %v", redactPostgresSmokeError(err, url))
	}
	if result.NoOp || !result.SearchTermsRebuilt || result.MigratedOperationLogs != 251 || result.TargetCounts["operation_log_viewers"] != 1 {
		t.Fatalf("unexpected F4 PostgreSQL historical migration result: %+v", result)
	}
	if err = validatePostgresSchema(ctx, postgresSQLCatalog{queryer: store.db}); err != nil {
		t.Fatalf("F4 PostgreSQL 历史迁移后 catalog 校验失败: %v", redactPostgresSmokeError(err, url))
	}
	var detailLevel string
	if err = store.db.QueryRowContext(ctx, `SELECT detail_level FROM juhe_dataset.operation_log_viewers WHERE operation_log_id='legacy-oplog' AND system_account_id='actor'`).Scan(&detailLevel); err != nil || detailLevel != "full" {
		t.Fatalf("F4 PostgreSQL 历史 viewer 未保留: detail=%q err=%v", detailLevel, redactPostgresSmokeError(err, url))
	}
	var rebuiltTerms int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id='legacy-oplog'`).Scan(&rebuiltTerms); err != nil || rebuiltTerms == 0 {
		t.Fatalf("F4 PostgreSQL 历史 search terms 未重建: count=%d err=%v", rebuiltTerms, redactPostgresSmokeError(err, url))
	}
	repeated, err := MigrateLegacyPostgres(ctx, Config{Mode: ModePostgres, PostgresURL: url}, LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true})
	if err != nil || !repeated.NoOp {
		t.Fatalf("F4 PostgreSQL 历史迁移重复执行必须 no-op: result=%+v err=%v", repeated, redactPostgresSmokeError(err, url))
	}
}

func TestNodeGoNodeSmokeServer(t *testing.T) {
	if strings.TrimSpace(os.Getenv("JUHE_AI_OPERATION_LOG_NODE_GO_SMOKE")) == "" {
		t.Skip("未设置 JUHE_AI_OPERATION_LOG_NODE_GO_SMOKE；真实 Node-Go-Node smoke 未执行")
	}
	runNodeGoNodeSmokeScript(t, "operation-log-go-real-sidecar-smoke.ts")
}

func TestNodeGoNodeSystemAPIProducerSmoke(t *testing.T) {
	if strings.TrimSpace(os.Getenv("JUHE_AI_OPERATION_LOG_SYSTEM_API_SMOKE")) == "" {
		t.Skip("未设置 JUHE_AI_OPERATION_LOG_SYSTEM_API_SMOKE；真实 System API F4 smoke 未执行")
	}
	runNodeGoNodeSmokeScript(t, "operation-log-go-system-api-settings-smoke.ts")
}

func TestNodeGoNodeOAuthProducerSmoke(t *testing.T) {
	if strings.TrimSpace(os.Getenv("JUHE_AI_OPERATION_LOG_OAUTH_SMOKE")) == "" {
		t.Skip("未设置 JUHE_AI_OPERATION_LOG_OAUTH_SMOKE；真实 OAuth producer F4 smoke 未执行")
	}
	runNodeGoNodeSmokeScript(t, "operation-log-go-oauth-producer-smoke.ts")
}

func TestNodeGoNodeWorkerProducerSmoke(t *testing.T) {
	if strings.TrimSpace(os.Getenv("JUHE_AI_OPERATION_LOG_WORKER_SMOKE")) == "" {
		t.Skip("未设置 JUHE_AI_OPERATION_LOG_WORKER_SMOKE；真实 worker producer F4 smoke 未执行")
	}
	runNodeGoNodeSmokeScript(t, "operation-log-go-worker-producer-smoke.ts")
}

func runNodeGoNodeSmokeScript(t *testing.T, script string) {
	t.Helper()
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	store, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "node-go-node", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	secret := "node-go-node-smoke-secret-with-at-least-32-bytes"
	h := &handler{store: store, lease: lease, cfg: InputServerConfig{SharedSecret: secret, MaxBytes: defaultInputMaxBytes, RequestTimeout: 5 * time.Second, ReplayWindow: time.Minute}, logger: slog.Default(), healthy: newAtomicTrue()}
	server := &http.Server{Handler: h}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-serverDone
	}()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate F4 smoke test source")
	}
	command := exec.Command("node", "--import", "tsx", filepath.ToSlash(filepath.Join("src", "scripts", "regression", script)))
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "..", "..", "backend"))
	command.Env = append(os.Environ(), "JUHE_AI_OPERATION_LOG_INPUT_URL=http://"+listener.Addr().String(), "JUHE_AI_OPERATION_LOG_INPUT_SECRET="+secret, "JUHE_AI_OPERATION_LOG_INPUT_TIMEOUT_MS=5000", "JUHE_AI_LOG_FILE_ENABLED=false", "JUHE_AI_LOG_CONSOLE_ENABLED=false", "NODE_ENV=test")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("真实 Node-Go-Node smoke %s 失败: %v\n%s", script, err, output)
	}
}

func redactPostgresSmokeError(err error, url string) string {
	if err == nil {
		return "<nil>"
	}
	return strings.ReplaceAll(err.Error(), url, "[redacted PostgreSQL URL]")
}

func TestSQLiteBusinessDatabaseMustBeDistinct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	createBusinessSettings(t, path, "365")
	_, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: path, BusinessSettingsPath: path})
	if err == nil {
		t.Fatal("F4 SQLite store must reject a shared business database")
	}
}

func TestSQLiteStoreRejectsPhysicalAliasesAndOptionalPathsRemainOptional(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	operation := filepath.Join(root, "operation.sqlite3")
	if store, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: operation, BusinessSettingsPath: business, SQLiteIsolationPaths: []string{filepath.Join(root, "missing-usage.sqlite3")}}); err != nil {
		t.Fatal(err)
	} else {
		_ = store.Close()
	}
	if err := os.Link(business, operation); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: operation, BusinessSettingsPath: business}); err == nil {
		t.Fatal("hard-linked business database must be rejected")
	}
}

func TestInputNormalizationBoundsViewersTargetsAndPersonalDetail(t *testing.T) {
	base := Input{ID: "normalize", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "summary", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), VisibilityScope: "all_users", Viewers: []Viewer{{SystemAccountID: "attacker", VisibilityReason: "actor_self"}}}
	normalized, err := normalizeInput(base)
	if err != nil || len(normalized.Viewers) != 0 {
		t.Fatalf("all_users must not persist caller viewers: %+v err=%v", normalized.Viewers, err)
	}
	base.VisibilityScope = "targeted"
	base.Viewers = []Viewer{{SystemAccountID: "", VisibilityReason: "actor_self"}}
	base.Targets = []Target{{TargetType: "r", Relation: "unexpected"}}
	if _, err = normalizeInput(base); err == nil {
		t.Fatal("unknown target relation must fail")
	}
}

func TestSearchTermsAndPersonalFullDetailRemainBounded(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	store, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "search", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	input := Input{ID: "search", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "更新 Alpha-账户", ClientIP: "203.0.113.8", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Targets: []Target{{TargetType: "r", TargetOwnerSystemAccountID: "owner", Relation: "primary"}}, Viewers: []Viewer{{SystemAccountID: "viewer", VisibilityReason: "actor_self", DetailLevel: "full"}}}
	if _, err = store.Persist(context.Background(), lease, input); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(context.Background(), ListOptions{SummaryKeyword: "pha 账"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("normalized continuous substring search failed: %+v err=%v", list, err)
	}
	detail, found, err := store.Detail(context.Background(), input.ID, "viewer")
	detailJSON, _ := json.Marshal(detail)
	if err != nil || !found || detail.ClientIP != "" || detail.Targets[0].TargetOwnerSystemAccountName != "Owner" || bytes.Contains(detailJSON, []byte("targetOwnerSystemAccountId")) {
		t.Fatalf("personal detail leaked fields: %+v found=%v err=%v", detail, found, err)
	}
}

func TestInvalidMetadataIsARecordFailure(t *testing.T) {
	_, err := normalizeInput(Input{ID: "metadata", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "bad metadata", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: []byte("{")})
	if err == nil {
		t.Fatal("invalid metadata must be rejected as a single-record failure")
	}
}

type requestContextStore struct {
	persist func(context.Context) error
	list    func(context.Context) error
	detail  func(context.Context) error
}

type leaseLostStore struct{ requestContextStore }

func (*leaseLostStore) Persist(context.Context, OwnerLease, Input) (bool, error) {
	return false, ErrOwnerLeaseLost
}

func (*requestContextStore) EnsureSchema(context.Context) error { return nil }
func (*requestContextStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{}, true, nil
}
func (*requestContextStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	return true, nil
}
func (*requestContextStore) ReleaseOwnerLease(context.Context, OwnerLease) error { return nil }
func (s *requestContextStore) Persist(ctx context.Context, _ OwnerLease, _ Input) (bool, error) {
	return false, s.persist(ctx)
}
func (s *requestContextStore) List(ctx context.Context, _ ListOptions) (ListResult, error) {
	return ListResult{}, s.list(ctx)
}
func (s *requestContextStore) Detail(ctx context.Context, _, _ string) (DetailSupplement, bool, error) {
	return DetailSupplement{}, false, s.detail(ctx)
}
func (*requestContextStore) CleanupRetention(context.Context, OwnerLease, time.Time, int) (int64, error) {
	return 0, nil
}
func (*requestContextStore) RetentionDays(context.Context, int) (int, error) { return 365, nil }
func (*requestContextStore) Close() error                                    { return nil }

func newAtomicTrue() *atomic.Bool { value := &atomic.Bool{}; value.Store(true); return value }

func signRequest(request *http.Request, secret string, body []byte, timestamp time.Time, nonce string) {
	value := timestamp.Format(time.RFC3339Nano)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(TimestampHeader, value)
	request.Header.Set(NonceHeader, nonce)
	request.Header.Set(SignatureHeader, SignInput(secret, value, nonce, body))
}

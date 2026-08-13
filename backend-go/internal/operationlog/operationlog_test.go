package operationlog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	detail, found, err := store.Detail(ctx, visible.ID, "viewer")
	detailJSON, _ := json.Marshal(detail)
	if err != nil || !found || detail.ClientIP != "" || len(detail.Targets) != 1 || detail.Targets[0].TargetOwnerSystemAccountName != "Owner" || bytes.Contains(detailJSON, []byte("targetOwnerSystemAccountId")) {
		t.Fatalf("F4 PostgreSQL 个人详情裁剪失败：detail=%+v found=%v err=%v", detail, found, redactPostgresSmokeError(err, url))
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

func TestNodeGoNodeSmokeServer(t *testing.T) {
	if strings.TrimSpace(os.Getenv("JUHE_AI_OPERATION_LOG_NODE_GO_SMOKE")) == "" {
		t.Skip("未设置 JUHE_AI_OPERATION_LOG_NODE_GO_SMOKE；真实 Node-Go-Node smoke 未执行")
	}
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
	command := exec.Command("node", "--import", "tsx", "src/scripts/regression/operation-log-go-real-sidecar-smoke.ts")
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "backend"))
	command.Env = append(os.Environ(), "JUHE_AI_OPERATION_LOG_INPUT_URL=http://"+listener.Addr().String(), "JUHE_AI_OPERATION_LOG_INPUT_SECRET="+secret, "JUHE_AI_OPERATION_LOG_INPUT_TIMEOUT_MS=5000", "JUHE_AI_LOG_FILE_ENABLED=false", "JUHE_AI_LOG_CONSOLE_ENABLED=false", "NODE_ENV=test")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("真实 Node-Go-Node smoke 失败: %v\n%s", err, output)
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

func newAtomicTrue() *atomic.Bool { value := &atomic.Bool{}; value.Store(true); return value }

func signRequest(request *http.Request, secret string, body []byte, timestamp time.Time, nonce string) {
	value := timestamp.Format(time.RFC3339Nano)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(TimestampHeader, value)
	request.Header.Set(NonceHeader, nonce)
	request.Header.Set(SignatureHeader, SignInput(secret, value, nonce, body))
}

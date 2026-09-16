package logreads

// w11f 覆盖波次（文件 2/2）：故障注入环境 + 读面/handler 错误臂与剩余分支。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var w11fBoom = errors.New("w11f boom")

// ---------------------------------------------------------------------------
// 故障脚本
// ---------------------------------------------------------------------------

type w11fRule struct {
	substr   string
	queryErr error
	cols     []string
	rows     [][]driver.Value
	nextErr  error
	limit    int
	hits     int
}

type w11fScript struct {
	mu    sync.Mutex
	rules []*w11fRule
}

func (s *w11fScript) failQuery(substr string) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &w11fRule{substr: substr, queryErr: w11fBoom}
	s.rules = append(s.rules, r)
	return r
}

func (s *w11fScript) cannedOnce(substr string, cols []string, rows [][]driver.Value, nextErr error) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &w11fRule{substr: substr, cols: cols, rows: rows, nextErr: nextErr}
	s.rules = append(s.rules, r)
	return r
}

func (s *w11fScript) take(query string) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if !strings.Contains(query, r.substr) {
			continue
		}
		if r.limit >= 0 && r.hits >= max(r.limit, 1) {
			continue
		}
		r.hits++
		return r
	}
	return nil
}

type w11fConnector struct {
	base   driver.Connector
	script *w11fScript
}

func (c w11fConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w11fConn{base: conn, script: c.script}, nil
}

func (c w11fConnector) Driver() driver.Driver { return c.base.Driver() }

type w11fConn struct {
	base   driver.Conn
	script *w11fScript
}

func (c *w11fConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w11fConn) Close() error                              { return c.base.Close() }
func (c *w11fConn) Begin() (driver.Tx, error)                 { return c.base.Begin() }

func (c *w11fConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rule := c.script.take(query); rule != nil {
		if rule.queryErr != nil {
			return nil, rule.queryErr
		}
		if rule.cols != nil {
			return &w11fRowsW{cols: rule.cols, values: rule.rows, nextErr: rule.nextErr}, nil
		}
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *w11fConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

type w11fRowsW struct {
	cols    []string
	values  [][]driver.Value
	next    int
	nextErr error
}

func (r *w11fRowsW) Columns() []string { return r.cols }
func (r *w11fRowsW) Close() error      { return nil }
func (r *w11fRowsW) Next(dest []driver.Value) error {
	if r.next >= len(r.values) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	row := r.values[r.next]
	r.next++
	copy(dest, row)
	return nil
}

var w11fDBSeq atomic.Int64

// w11fFaultEnv 构造与 readsTestEnv 等价但物理连接经过故障脚本的环境。
func w11fFaultEnv(t *testing.T, datasetDDL []string) (*readsTestEnv, *w11fScript) {
	t.Helper()
	base, err := sqlite.NewConnector("file:w11f-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + fmt.Sprintf("%d", w11fDBSeq.Add(1)) + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fScript{}
	db := sql.OpenDB(w11fConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range append(readsAuthTables, datasetDDL...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	directories := t.TempDir()
	hotDir := filepath.Join(directories, "audit-hot")
	blobDir := filepath.Join(directories, "audit-blobs")
	logDir := filepath.Join(directories, "logs")
	audit, err := NewAuditLogQueryReader(db, ReadSQLite, AuditQueryDirectories{HotSearchDirectory: hotDir, PayloadBlobDirectory: blobDir})
	if err != nil {
		t.Fatal(err)
	}
	runtimeReader, err := NewRuntimeLogSQLReader(db, ReadSQLite)
	if err != nil {
		t.Fatal(err)
	}
	public, err := NewPublicApiLogSQLStore(db, ReadSQLite)
	if err != nil {
		t.Fatal(err)
	}
	grep := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: true, Directory: logDir, MaxFiles: 500, RetentionDays: 30})
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&ReadsDeps{Audit: audit, Runtime: runtimeReader, Public: public, Grep: grep, Auth: deps}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	env := &readsTestEnv{server: server, db: db, jar: map[string]string{}, accounts: accounts, hotDir: hotDir, blobDir: blobDir, logDir: logDir, grep: grep}
	if _, err := accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &mustChangeFalse,
		Username: "admin", DisplayName: "admin_name", Password: "admin-password-123", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"admin","password":"admin-password-123"}`)
	if code != http.StatusOK {
		t.Fatalf("admin login failed: %d %v", code, payload)
	}
	return env, script
}

// ---------------------------------------------------------------------------
// readWriteStoreError 与 query 基础设施
// ---------------------------------------------------------------------------

func TestW11FReadWriteStoreError(t *testing.T) {
	recorder := httptest.NewRecorder()
	if readWriteStoreError(recorder, nil) {
		t.Fatal("nil error must not be handled")
	}
	recorder = httptest.NewRecorder()
	if !readWriteStoreError(recorder, readParamErrorf("参数错误 %d", 1)) || recorder.Code != http.StatusBadRequest {
		t.Fatal("param error must 400")
	}
	recorder = httptest.NewRecorder()
	if !readWriteStoreError(recorder, w11fBoom) || recorder.Code != http.StatusInternalServerError {
		t.Fatal("generic error must 500")
	}

	env, script := w11fFaultEnv(t, auditReadsDDL)
	ctx := context.Background()
	_ = env
	// readQueryMaps: 查询错误 / rows.Err / Scan 错误 / 命中。
	script.failQuery("FROM missing_table_w11f")
	if _, err := readQueryMaps(ctx, env.db, ReadSQLite, "SELECT * FROM missing_table_w11f"); err == nil {
		t.Fatal("query maps fault must fail")
	}
	script.cannedOnce("SELECT 1 AS v", []string{"v"}, [][]driver.Value{{int64(1)}}, w11fBoom)
	if _, err := readQueryMaps(ctx, env.db, ReadSQLite, "SELECT 1 AS v"); err == nil {
		t.Fatal("rows.Err fault must fail")
	}
	// 直接 SQL 错误（真实 sqlite）。
	if _, err := readQueryMaps(ctx, env.db, ReadSQLite, "SELECT * FROM nope"); err == nil {
		t.Fatal("sql error must fail")
	}
	if row, err := readQueryOneMap(ctx, env.db, ReadSQLite, "SELECT 1 AS v"); err != nil || row == nil || row["v"] != int64(1) {
		t.Fatalf("one map = %v/%v", row, err)
	}
	if row, err := readQueryOneMap(ctx, env.db, ReadSQLite, "SELECT 1 AS v WHERE 1 = 0"); err != nil || row != nil {
		t.Fatalf("empty one map = %v/%v", row, err)
	}
}

// ---------------------------------------------------------------------------
// 审计读面故障与分支
// ---------------------------------------------------------------------------

func TestW11FAuditReaderFaults(t *testing.T) {
	env, script := w11fFaultEnv(t, auditReadsDDL)
	ctx := context.Background()
	audit := envAudit(env)
	// 种子一行真实审计日志，供 attempts/payloads/errorGroup 分支走到二级查询。
	env.exec(t, `INSERT INTO audit_logs (id, trace_id, traffic_source, system_account_id, api_key_id, conversation_key,
		session_id, session_client_type, group_id, account_id, provider_code, method, path, model, upstream_model,
		model_mapping_applied, stream, client_ip, audit_outcome, success, final_status_code, sample_bucket, sample_reason,
		attempt_count, payload_count, raw_payload_bytes, compressed_payload_bytes, compression_saved_bytes,
		lifecycle_status, started_at, ended_at, duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at, error_group_id)
		VALUES ('w11f-seed', 'trace-w11f', 'gateway', 'sys-1', 'key-1', 'conv-1', 'sess-1', 'web', 'grp-1', 'acc-1', 'openai',
		'POST', '/v1/chat/completions', 'gpt-4o', 'gpt-4o', 1, 1, '10.0.0.1', 'success', 1, 200, 1, 'sampled',
		1, 1, 512, 256, 256, 'finalized', '2026-06-02T10:00:00.000Z', '2026-06-02T10:00:00.200Z', 200,
		'2026-06-02T10:00:00.220Z', 220, 40, '2026-06-02T10:00:01.000Z', NULL)`)

	// 种子一行带 error_group_id 的完整审计日志。
	env.exec(t, `INSERT INTO audit_logs (id, trace_id, traffic_source, system_account_id, api_key_id, conversation_key,
		session_id, session_client_type, group_id, account_id, provider_code, method, path, model, upstream_model,
		model_mapping_applied, stream, client_ip, audit_outcome, success, final_status_code, sample_bucket, sample_reason,
		attempt_count, payload_count, raw_payload_bytes, compressed_payload_bytes, compression_saved_bytes,
		lifecycle_status, started_at, ended_at, duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at, error_group_id)
		VALUES ('w11f-seed-eg', 'trace-w11f-eg', 'gateway', 'sys-1', 'key-1', 'conv-1', 'sess-1', 'web', 'grp-1', 'acc-1', 'openai',
		'POST', '/v1/chat/completions', 'gpt-4o', 'gpt-4o', 1, 1, '10.0.0.1', 'success', 1, 200, 1, 'sampled',
		1, 1, 512, 256, 256, 'finalized', '2026-06-02T10:00:00.000Z', '2026-06-02T10:00:00.200Z', 200,
		'2026-06-02T10:00:00.220Z', 220, 40, '2026-06-02T10:00:01.000Z', 'w11f-eg')`)
	// ListAuditLogs 查询错误。
	script.failQuery("FROM \"audit_logs\"")
	if _, err := audit.ListAuditLogs(ctx, AuditLogListOptions{}); err == nil {
		t.Fatal("list audit fault must fail")
	}
	// ListAuditErrorGroups: 全过滤 + 查询错误 + 命中。
	filters := AuditErrorGroupListOptions{
		Path: "/v1/x", Model: "gpt-x", StatusCode: intPtrW11F(500),
		SystemAccountID: "sa", APIKeyID: "ak", GroupID: "g", AccountID: "acc",
	}
	if _, err := audit.ListAuditErrorGroups(ctx, filters); err != nil {
		t.Fatal(err)
	}
	script.failQuery("FROM \"audit_error_groups\"")
	if _, err := audit.ListAuditErrorGroups(ctx, AuditErrorGroupListOptions{}); err == nil {
		t.Fatal("error groups fault must fail")
	}
	// GetAuditLogDetail: 空 ID / 查询错误 / 缺失。
	if detail, err := audit.GetAuditLogDetail(ctx, "  "); err != nil || detail != nil {
		t.Fatalf("blank detail = %v/%v", detail, err)
	}
	script.failQuery("WHERE al.id")
	if _, err := audit.GetAuditLogDetail(ctx, "w11f-id"); err == nil {
		t.Fatal("detail fault must fail")
	}
	if detail, err := audit.GetAuditLogDetail(ctx, "w11f-missing"); err != nil || detail != nil {
		t.Fatalf("missing detail = %v/%v", detail, err)
	}
	// attempts / payloads / errorGroup 查询错误。
	script.failQuery("FROM \"audit_log_attempts\"")
	if _, err := audit.GetAuditLogDetail(ctx, "w11f-seed"); err == nil {
		t.Fatal("attempts fault must fail")
	}
	script.failQuery("FROM \"audit_payload_refs\"")
	if _, err := audit.GetAuditLogDetail(ctx, "w11f-seed"); err == nil {
		t.Fatal("payloads fault must fail")
	}
	script.failQuery("FROM \"audit_error_groups\"")
	if _, err := audit.GetAuditLogDetail(ctx, "w11f-seed-eg"); err == nil {
		t.Fatal("error group fault must fail")
	}
}

func envAudit(env *readsTestEnv) *auditLogSQLReader {
	reader, err := NewAuditLogQueryReader(env.db, ReadSQLite, AuditQueryDirectories{HotSearchDirectory: env.hotDir, PayloadBlobDirectory: env.blobDir})
	if err != nil {
		panic(err)
	}
	return reader.(*auditLogSQLReader)
}

// ---------------------------------------------------------------------------
// 审计 handler 层故障
// ---------------------------------------------------------------------------

func TestW11FAuditHandlerFaults(t *testing.T) {
	env, script := w11fFaultEnv(t, auditReadsDDL)

	// 列表: 参数错误（坏 trafficSource）→ 400; 存储错误 → 500。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?trafficSource=bogus", "")
	if code != http.StatusBadRequest || !strings.Contains(payloadW11FMessage(payload), "来源筛选无效") {
		t.Fatalf("bad trafficSource = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?startAt=zzz", "")
	if code != http.StatusBadRequest {
		t.Fatalf("bad startAt = %d %v", code, payload)
	}
	script.failQuery("FROM \"audit_logs\"")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs", "")
	if code != http.StatusInternalServerError || payloadW11FMessage(payload) != "服务器内部错误" {
		t.Fatalf("list fault = %d %v", code, payload)
	}
	// error-groups: 存储错误 → 500。
	script.failQuery("FROM \"audit_error_groups\"")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/error-groups", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("error groups fault = %d %v", code, payload)
	}
	// error-group-events: 参数错误 / 存储错误。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/error-groups/g1/events?endAt=zzz", "")
	if code != http.StatusBadRequest {
		t.Fatalf("events bad param = %d %v", code, payload)
	}
	script.failQuery("FROM \"audit_logs\"")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/error-groups/g1/events", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("events fault = %d %v", code, payload)
	}
	// 详情: 存储错误 / 缺失。
	script.failQuery("WHERE al.id")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/w11f-x", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("detail fault = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/w11f-missing", "")
	if code != http.StatusNotFound || payloadW11FMessage(payload) != "审计日志不存在" {
		t.Fatalf("detail missing = %d %v", code, payload)
	}
}

func payloadW11FMessage(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if message, ok := payload["message"].(string); ok {
		return message
	}
	if data, ok := payload["data"].(map[string]any); ok {
		if message, ok := data["message"].(string); ok {
			return message
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 热搜索 handler 与扫描分支
// ---------------------------------------------------------------------------

func TestW11FHotSearchHandlerAndScan(t *testing.T) {
	env, script := w11fFaultEnv(t, auditReadsDDL)
	_ = script

	// handler: 短关键字 → 200 消息契约；坏时间 → 400。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=x", "")
	if code != http.StatusOK || !strings.Contains(payloadW11FMessage(payload), "请输入要搜索的审计内容关键字") {
		t.Fatalf("short keyword = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=abc&startAt=zzz", "")
	if code != http.StatusBadRequest {
		t.Fatalf("bad window = %d %v", code, payload)
	}
	// 命中: 桶文件内匹配。
	bucketName := "audit-hot-" + time.Now().UTC().Format("2006010215") + ".ndjson"
	line := `{"auditLogId":"w11f-aid","createdAt":"` + time.Now().UTC().Format(time.RFC3339) + `","text":"w11f-hot keyword hit"}`
	if err := os.MkdirAll(env.hotDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.hotDir, bucketName), []byte(line+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=w11f-hot", "")
	if code != http.StatusOK {
		t.Fatalf("hot hit = %d %v", code, payload)
	}
	// 目录缺失 → available + 缺目录消息。
	if err := os.RemoveAll(env.hotDir); err != nil {
		t.Fatal(err)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=w11f-hot", "")
	if code != http.StatusOK {
		t.Fatalf("hot missing dir = %d %v", code, payload)
	}

	// scanAuditHotBucket: 打开失败（路径不存在）/ 预算耗尽 / 目录当作文件读错。
	if _, err := scanAuditHotBucket(context.Background(), filepath.Join(env.hotDir, "missing.ndjson"), []string{"kw"}, 0, 1<<62, new(int64), new(int), map[string]int64{}); err == nil {
		t.Fatal("missing bucket must fail")
	}
	if err := os.MkdirAll(env.hotDir, 0o750); err != nil {
		t.Fatal(err)
	}
	budgetPath := filepath.Join(env.hotDir, "w11f-budget.ndjson")
	if err := os.WriteFile(budgetPath, []byte("{}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	remainingBytes := int64(0)
	remainingLines := 0
	truncated, err := scanAuditHotBucket(context.Background(), budgetPath, []string{"kw"}, 0, 1<<62, &remainingBytes, &remainingLines, map[string]int64{})
	if err != nil || !truncated {
		t.Fatalf("budget exhausted = %v/%v", truncated, err)
	}

	// 取消上下文。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanAuditHotBucket(canceled, filepath.Join(env.hotDir, "missing2.ndjson"), []string{"kw"}, 0, 1<<62, new(int64), new(int), map[string]int64{}); err == nil {
		t.Fatal("canceled context must surface")
	}
}

// ---------------------------------------------------------------------------
// public / runtime 读面故障
// ---------------------------------------------------------------------------

func TestW11FPublicAndRuntimeFaults(t *testing.T) {
	env, script := w11fFaultEnv(t, publicReadsDDL)

	// NewPublicApiLogSQLStore nil db。
	if _, err := NewPublicApiLogSQLStore(nil, ReadSQLite); err == nil {
		t.Fatal("nil public store must fail")
	}
	// 列表: 存储错误 → 500。
	script.failQuery("FROM \"public_api_logs\"")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("public list fault = %d %v", code, payload)
	}
	// 详情: 存储错误 → 500; 缺失 → 404。
	script.failQuery("WHERE pal.id")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs/w11f-x", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("public detail fault = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs/w11f-missing", "")
	if code != http.StatusNotFound {
		t.Fatalf("public detail missing = %d %v", code, payload)
	}

	// runtime: 列表与详情故障。
	runtimeEnv, runtimeScript := w11fFaultEnv(t, runtimeReadsDDL)
	runtimeScript.failQuery("FROM \"runtime_logs\"")
	code, payload = runtimeEnv.do(t, http.MethodGet, "/__aisys__/api/runtime-logs", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("runtime list fault = %d %v", code, payload)
	}
	runtimeScript.failQuery("WHERE id = ? LIMIT 1")
	code, payload = runtimeEnv.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/w11f-x", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("runtime detail fault = %d %v", code, payload)
	}
}

var _ = os.Getenv

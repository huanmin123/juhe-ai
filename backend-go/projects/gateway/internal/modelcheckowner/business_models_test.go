package modelcheckowner

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

func TestResolveConfiguredUpstreamModelMappingPreservesFamilies(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	seedMappingSupported(t, db, "acct-1", "gpt-5.6-terra")
	seedMappingEntry(t, db, "acct-1", "gpt-5.6-sol", "responses", "gpt-5.6-terra", "chat_completions")
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.SourceEndpointFamily != modelcheckprofile.EndpointResponses || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointChatCompletions {
		t.Fatalf("resolution=%+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingRejectsUnsupportedCrossFamily(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	seedMappingSupported(t, db, "acct-1", "gpt-5.6-terra")
	seedMappingEntry(t, db, "acct-1", "gpt-5.6-sol", "responses", "gpt-5.6-terra", "messages")
	_, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported cross-family mapping must fail closed, err=%v", err)
	}
}

func TestResolveConfiguredUpstreamModelMappingAcceptsSameFamily(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	seedMappingSupported(t, db, "acct-1", "gpt-5.6-terra")
	seedMappingEntry(t, db, "acct-1", "gpt-5.6-sol", "responses", "gpt-5.6-terra", "responses")
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.SourceEndpointFamily != modelcheckprofile.EndpointResponses || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointResponses {
		t.Fatalf("resolution=%+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingReadsMappingWithoutModelRestriction(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	seedMappingEntry(t, db, "acct-1", "gpt-5.6-sol", "responses", "gpt-5.6-terra", "chat_completions")
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointChatCompletions {
		t.Fatalf("mapping must apply when supported model list is empty: %+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingMappingPrecedesDirectMatch(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	seedMappingSupported(t, db, "acct-1", "gpt-5.6-sol")
	seedMappingSupported(t, db, "acct-1", "gpt-5.6-terra")
	seedMappingEntry(t, db, "acct-1", "gpt-5.6-sol", "responses", "gpt-5.6-terra", "chat_completions")
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointChatCompletions {
		t.Fatalf("explicit mapping must precede direct match: %+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingAdmitsAccountSupportedCatalogExternalModel(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	seedMappingSupported(t, db, "acct-1", "deepseek-v4.1-flash")
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "deepseek-v4.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "deepseek-v4.1-flash" || resolved.SourceEndpointFamily != modelcheckprofile.EndpointResponses || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointResponses {
		t.Fatalf("account-supported catalog-external model must resolve directly: %+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingStillRejectsUnsupportedCatalogExternalModel(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	seedMappingSupported(t, db, "acct-1", "gpt-5.6-terra")
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "deepseek-v4.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "" {
		t.Fatalf("catalog-external model outside account restriction must stay rejected: %+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingAppliesMappingForCatalogExternalModel(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	seedMappingSupported(t, db, "acct-1", "glm-5.2")
	seedMappingSupported(t, db, "acct-1", "gpt-5.6-terra")
	seedMappingEntry(t, db, "acct-1", "glm-5.2", "responses", "gpt-5.6-terra", "responses")
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "glm-5.2")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointResponses {
		t.Fatalf("configured mapping must apply to catalog-external source model: %+v", resolved)
	}
	// The mapped upstream must still be covered by the account restriction.
	if _, err := db.Exec(`DELETE FROM account_supported_models WHERE account_id='acct-1' AND model_id=?`, seedMappingModel(t, db, "gpt-5.6-terra")); err != nil {
		t.Fatal(err)
	}
	restricted, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "glm-5.2")
	if err != nil {
		t.Fatal(err)
	}
	if restricted.UpstreamModel != "" {
		t.Fatalf("mapping upstream outside account restriction must stay rejected: %+v", restricted)
	}
}

// seedMappingModel 写入内置 models 行（public_model = upstream_model = name）
// 并返回 model_id，供 account_supported_models / account_entry_mappings 外键
// 与模型名解析使用（v16 契约：请求名 public_model、上游名 upstream_model）。
func seedMappingModel(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	id := "model-" + strings.NewReplacer(".", "-", "_", "-").Replace(name)
	if _, err := db.Exec(`INSERT OR IGNORE INTO models (id,vendor_code,public_model,upstream_model,scope,system_account_id,status,catalog_visible) VALUES (?,'gpt',?,?,'builtin',NULL,'active',1)`, id, name, name); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedMappingSupported 写入一条账户支持模型（v16 契约存 model_id）。
func seedMappingSupported(t *testing.T, db *sql.DB, accountID, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_supported_models (account_id,model_id) VALUES (?,?)`, accountID, seedMappingModel(t, db, name)); err != nil {
		t.Fatal(err)
	}
}

// seedMappingEntry 写入一条账户入口映射（target_model_id 外键解析出
// models.upstream_model，target_endpoint_family 对应旧 upstream_endpoint_family）。
func seedMappingEntry(t *testing.T, db *sql.DB, accountID, sourceModel, sourceFamily, upstreamModel, upstreamFamily string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_entry_mappings (id,account_id,source_model,source_endpoint_family,target_connection_id,target_model_id,target_endpoint_family,enabled) VALUES (? ,?, ?, ?, 'conn-'||?, ?, ?, 1)`,
		"map-"+accountID+"-"+sourceModel+"-"+sourceFamily, accountID, sourceModel, sourceFamily, accountID, seedMappingModel(t, db, upstreamModel), upstreamFamily); err != nil {
		t.Fatal(err)
	}
}

func newModelMappingDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/model-mapping.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE models (id TEXT PRIMARY KEY,vendor_code TEXT,public_model TEXT,upstream_model TEXT,scope TEXT,system_account_id TEXT,status TEXT,catalog_visible INTEGER)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model_id TEXT,PRIMARY KEY (account_id, model_id))`,
		`CREATE TABLE account_entry_mappings (id TEXT PRIMARY KEY,account_id TEXT,source_model TEXT,source_endpoint_family TEXT,target_connection_id TEXT,target_model_id TEXT,target_endpoint_family TEXT,enabled INTEGER)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	return db
}

// ---- PG 分支录制驱动（对齐 jobs wfix_balance_due_fence_test 的 fenceRecorder
// 模式）：不依赖真实 PostgreSQL，锁定 postgres=true 实际发送的 SQL 文本。----

type modelMappingStatement struct {
	query string
	args  []driver.Value
}

type modelMappingRecorder struct {
	mu         sync.Mutex
	statements []modelMappingStatement
}

func (r *modelMappingRecorder) capture(query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, modelMappingStatement{query: query, args: values})
}

func (r *modelMappingRecorder) all() []modelMappingStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]modelMappingStatement{}, r.statements...)
}

type modelMappingConnector struct{ rec *modelMappingRecorder }

func (c modelMappingConnector) Connect(context.Context) (driver.Conn, error) {
	return &modelMappingConn{rec: c.rec}, nil
}

func (c modelMappingConnector) Driver() driver.Driver { return modelMappingDriver{} }

type modelMappingDriver struct{}

func (modelMappingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("recorder: Open 不应被调用（走 sql.OpenDB）")
}

type modelMappingConn struct{ rec *modelMappingRecorder }

func (c *modelMappingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recorder: Prepare 不应被调用（走 QueryerContext/ExecerContext）")
}

func (c *modelMappingConn) Close() error { return nil }

func (c *modelMappingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recorder: Begin 不应被调用")
}

func (c *modelMappingConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.capture(query, args)
	if strings.Contains(query, "account_supported_models") {
		return &modelMappingSingleRowRows{columns: []string{"model"}, value: "gpt-5.6-sol"}, nil
	}
	return &modelMappingEmptyRows{columns: []string{"upstream_model", "upstream_endpoint_family"}}, nil
}

func (c *modelMappingConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.rec.capture(query, args)
	return driver.RowsAffected(0), nil
}

func (c *modelMappingConn) CheckNamedValue(value *driver.NamedValue) error {
	switch value.Value.(type) {
	case nil, int64, float64, bool, []byte, string:
		return nil
	default:
		return driver.ErrSkip
	}
}

type modelMappingEmptyRows struct{ columns []string }

func (r *modelMappingEmptyRows) Columns() []string         { return r.columns }
func (r *modelMappingEmptyRows) Close() error              { return nil }
func (r *modelMappingEmptyRows) Next([]driver.Value) error { return io.EOF }

type modelMappingSingleRowRows struct {
	columns []string
	value   string
	sent    bool
}

func (r *modelMappingSingleRowRows) Columns() []string { return r.columns }
func (r *modelMappingSingleRowRows) Close() error      { return nil }
func (r *modelMappingSingleRowRows) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	dest[0] = r.value
	return nil
}

// PG 分支映射查询的字面量契约（BUG-0205）：enabled 在双方言 schema 均为
// integer 列，SQL 文本必须使用整数字面量 1；布尔字面量 TRUE 在 PostgreSQL
// 触发 42883 operator does not exist: integer = boolean。同时锁定
// juhe_business. 前缀与 $N 占位符形态。
func TestResolveConfiguredUpstreamModelMappingPostgresQueryUsesIntegerEnabledLiteral(t *testing.T) {
	profile, ok := modelcheckprofile.Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	rec := &modelMappingRecorder{}
	db := sql.OpenDB(modelMappingConnector{rec})
	defer func() { _ = db.Close() }()
	if _, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, true, "acct-1", profile, "gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	mapping := ""
	for _, statement := range rec.all() {
		if strings.Contains(statement.query, "account_entry_mappings") {
			mapping = statement.query
		}
	}
	if mapping == "" {
		t.Fatal("postgres 模式必须发送 account_entry_mappings 查询")
	}
	for _, want := range []string{"juhe_business.account_entry_mappings", "$1", "enabled=1"} {
		if !strings.Contains(mapping, want) {
			t.Fatalf("postgres 映射查询缺少 %q: %s", want, mapping)
		}
	}
	if strings.Contains(mapping, "TRUE") {
		t.Fatalf("integer 列禁止布尔字面量 TRUE: %s", mapping)
	}
}

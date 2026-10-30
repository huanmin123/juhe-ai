package accounts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

const testSecret = "m08-accounts-test-secret"

// testProviderDDL / testProfileDDL 是 provider/profile 壳表的夹具种子（生产
// §9.11 已无 provider 维度读写，表仅承接旧种子助手直插）；幂等以
// ON CONFLICT DO NOTHING 承载（原 INSERT OR IGNORE 的 PG 对应物）。
const testProviderDDL = `INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
	VALUES ('prov-gpt', 'gpt', 'OpenAI', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')
	ON CONFLICT DO NOTHING`

const testProfileDDL = `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
	protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
	capabilities_json, created_at, updated_at)
	VALUES ('prof-gpt', 'gpt', 'OpenAI 官方协议', 1, 'openai', 'v1', 'https://api.openai.com/v1',
	'gpt-4o-mini', '["api_key","oauth"]', '[]', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')
	ON CONFLICT DO NOTHING`

type recordingSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}

func (s *recordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *recordingSink) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, entry := range s.entries {
		out = append(out, entry.Module+"."+entry.Action)
	}
	return out
}

// sensitive verifies the named change entries are flagged sensitive and never
// carry credential material.
func (s *recordingSink) sensitive(field, material string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, entry := range s.entries {
		for _, change := range entry.Changes {
			if change.Field != field {
				continue
			}
			if strings.Contains(change.After, material) || strings.Contains(change.Before, material) {
				return false
			}
			if change.Sensitive {
				found = true
			}
		}
	}
	return found
}

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	sink   *recordingSink
	db     *sql.DB
	store  *Store
}

// newTestEnv 是测试的共享环境：pgtestkit 隔离 PG 测试库 + 生产 store
// （与生产 PG 装配同一方言）；schema 与手写 SQL 方言见 pgkit_test.go。
// 原 SQLite-only 组B环境（newSQLiteTestEnv）已随生产 sqlite 臂删除批次
// 同批退役（D17）。
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := newAccountsPGDatabase(t)
	// vendors 种子（设计 §9.1）：创建链 vendor 归属校验按 vendors 表判定，
	// gpt 为夹具默认真实厂商；anthropic 等其余厂商保持未预置（供「不支持的
	// 供应商」臂断言），openai 为 generic 语义（无 vendors 行，校验跳过）。
	// default_supported_models_json 与生产 pgSeedProviders 的 gpt 缺省同口径
	//（导入链在来源未携带 models 时按 vendor 缺省回落，B4 ⑦）。
	if _, err := db.Exec(`INSERT INTO juhe_business.vendors (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('ven-fixture-gpt', 'gpt', 'GPT', 1, '["gpt-4o-mini"]', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	seedFixtureModels(t, db)
	service, err := businessauth.New(db, modelcheckauth.Postgres, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.Postgres, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	sink := &recordingSink{}
	store, err := NewStore(db, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, db: db, store: store}
}

func (e *testEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	e.mu.Lock()
	for name, value := range e.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	e.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for _, c := range response.Cookies() {
		if c.Value != "" {
			e.jar[c.Name] = c.Value
		} else {
			delete(e.jar, c.Name)
		}
	}
	e.mu.Unlock()
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return response.StatusCode, payload
}

func (e *testEnv) login(t *testing.T, username, password, role string) string {
	t.Helper()
	id := ""
	if existing, err := e.deps.Accounts.FindByUsername(context.Background(), username); err == nil {
		id = existing.ID
	}
	if id == "" {
		mustChange := false
		created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
			Username: username, DisplayName: username + "_name", Password: password, Role: role,
			MustChangePassword: &mustChange,
		})
		if err != nil {
			t.Fatal(err)
		}
		id = created.ID
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
	return id
}

// seedProviderAndDefaultGroup mirrors the create flow prerequisites: the gpt
// provider plus an enabled protocol profile and the owner's default group.
// seedVendor 为指定真实厂商补 vendors 行（创建链 vendor 归属校验按 vendors
// 表判定，设计 §9.1）；默认夹具只预置 gpt，非 gpt 厂商的创建用例按需补种。
func (e *testEnv) seedVendor(t *testing.T, code string) {
	t.Helper()
	e.exec(t, `INSERT INTO vendors (id, code, name, enabled, created_at, updated_at)
		VALUES (?, ?, ?, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')
		ON CONFLICT DO NOTHING`, "ven-"+code, code, code)
}

func (e *testEnv) seedProviderAndDefaultGroup(t *testing.T, ownerID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Idempotent per env: several fixtures seed the same provider/profile.
	e.exec(t, testProviderDDL)
	e.exec(t, testProfileDDL)
	e.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES (?, ?, '默认分组', 1, 1, 'personal', ?, ?)`, "grp-default-"+ownerID, ownerID, now, now)
}

// nullStringForFixture 把空串 vendor 落 SQL NULL（与目标 schema 的可空
// vendor_code 同形，models 身份键 COALESCE 两侧一致）。
func nullStringForFixture(code string) any {
	if code == "" {
		return nil
	}
	return code
}

// seedFixtureModels 是 B4 W2 夹具模型种子：supportedModels / entry mapping 的
// target 模型名按 models.upstream_model 身份键解析（owner 可见 + active），
// builtin 行全局可见。覆盖本包测试引用的全部模型名（盘点：supportedModels
// 字面量与直插种子）。每个名字按 §1.1 九厂商 + NULL(generic) 全值域落 vendor 身份：写侧
// 按账户 provider 推导的 vendor_code（gpt→'gpt'，openai/generic→”）做身份键
// 匹配。
func seedFixtureModels(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, model := range []struct{ id, name string }{
		{"gpt-4o", "gpt-4o"},
		{"gpt-4o-mini", "gpt-4o-mini"},
		{"gpt-4-1", "gpt-4.1"},
		{"m-default", "m-default"},
		{"m1", "m1"},
		{"other-model", "other-model"},
		{"gpt-4o-up", "gpt-4o-up"},
		{"gpt-4o-mini-up", "gpt-4o-mini-up"},
		{"up-4o", "up-4o"},
		{"up-mini", "up-mini"},
		{"custom-a", "custom-a"},
		{"gpt-w18-pass", "gpt-w18-pass"},
		{"aaa-model", "aaa-model"},
		{"m-a", "m-a"},
		{"u-a", "u-a"},
		{"o3-mini", "o3-mini"},
		{"outside-upstream", "outside-upstream"},
		{"gpt-4o-mini-2", "gpt-4o-mini-2"},
		{"gpt-w18-b", "gpt-w18-b"},
	} {
		for _, vendor := range []struct{ id, code string }{
			{"-gpt", "gpt"}, {"-xai", "xai"}, {"-deepseek", "deepseek"}, {"-anthropic", "anthropic"},
			{"-gemini", "gemini"}, {"-glm", "glm"}, {"-minimax", "minimax"}, {"-volcengine", "volcengine"},
			{"-qwen", "qwen"}, {"-generic", ""},
		} {
			statement := `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, catalog_visible, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'builtin', 'active', '["audio_speech","audio_transcription_json","chat_json","chat_sse","generate_content_json","generate_content_sse","images_json","interactions_json","interactions_sse","messages_json","messages_sse","responses_json","responses_sse","video_create","video_get","video_content","video_cancel"]', false, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
			ON CONFLICT DO NOTHING`
			statement = fixtureBind(statement)
			if _, err := db.Exec(statement,
				"mod-fixture-"+model.id+vendor.id, nullStringForFixture(vendor.code), model.name, model.name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// seedEntryMapping 是 B4 W2 目标形态的直插助手（读路径夹具用）：为账户落一行
// enabled 连接（preset 账户内自然键）+ 双 chat endpoint modes + upstream 模型
// 支持成员 + 一条入口映射行——等价于写路径 resolveEntryMappingTargets 的最小
// 事实集（设计 §2.1/§5.1/§9）。
func (e *testEnv) seedEntryMapping(t *testing.T, accountID, preset, sourceModel, sourceFamily, upstreamModel, targetFamily string, enabled int) {
	t.Helper()
	now := "2026-01-01T00:00:00Z"
	connectionID := "conn-" + accountID + "-" + preset
	e.exec(t, `INSERT INTO account_connections (id, account_id, connection_preset_code, base_url, enabled, created_at, updated_at)
		VALUES (?, ?, ?, 'https://fixture.invalid/v1', 1, ?, ?)
		ON CONFLICT DO NOTHING`, connectionID, accountID, preset, now, now)
	for _, mode := range []string{"chat_json", "chat_sse"} {
		e.exec(t, `INSERT INTO account_endpoint_modes (connection_id, endpoint_mode) VALUES (?, ?)
			ON CONFLICT DO NOTHING`, connectionID, mode)
	}
	e.exec(t, `INSERT INTO account_supported_models (account_id, model_id, created_at)
		VALUES (?, (SELECT id FROM models WHERE upstream_model = ? ORDER BY id LIMIT 1), ?)
		ON CONFLICT DO NOTHING`, accountID, upstreamModel, now)
	e.exec(t, `INSERT INTO account_entry_mappings (id, account_id, source_model, source_endpoint_family,
		target_connection_id, target_model_id, target_endpoint_family, enabled, created_at, updated_at)
		VALUES ('entmap-fix-'||?||'-'||?, ?, ?, ?, ?, (SELECT id FROM models WHERE upstream_model = ? ORDER BY id LIMIT 1), ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		accountID, sourceModel, accountID, sourceModel, sourceFamily, connectionID, upstreamModel, targetFamily, enabled, now, now)
}

// seedAccountConnection 为已存在账户落写路径前置事实（B4 W2）：enabled 连接
// 行（preset 账户内自然键）+ 双 chat endpoint modes + upstream 模型支持成员
// （FK 引用面）——不含映射行，映射由被测写路径自行落库。
func (e *testEnv) seedAccountConnection(t *testing.T, accountID, preset string, upstreamModels ...string) {
	t.Helper()
	now := "2026-01-01T00:00:00Z"
	connectionID := "conn-" + accountID + "-" + preset
	e.exec(t, `INSERT INTO account_connections (id, account_id, connection_preset_code, base_url, enabled, created_at, updated_at)
		VALUES (?, ?, ?, 'https://fixture.invalid/v1', 1, ?, ?)
		ON CONFLICT DO NOTHING`, connectionID, accountID, preset, now, now)
	for _, mode := range []string{"chat_json", "chat_sse"} {
		e.exec(t, `INSERT INTO account_endpoint_modes (connection_id, endpoint_mode) VALUES (?, ?)
			ON CONFLICT DO NOTHING`, connectionID, mode)
	}
	for _, upstream := range upstreamModels {
		e.exec(t, `INSERT INTO account_supported_models (account_id, model_id, created_at)
			VALUES (?, (SELECT id FROM models WHERE upstream_model = ? ORDER BY id LIMIT 1), ?)
			ON CONFLICT DO NOTHING`, accountID, upstream, now)
	}
}

// seedAccount is the minimal direct row for read-path fixtures.
func (e *testEnv) seedAccount(t *testing.T, id, ownerID, name, status string) {
	t.Helper()
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-seeded-" + id})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES (?, ?, 'gpt', 'gpt', 'openai', 'v1', ?, 'api_key', ?, ?, 'sk-see***'||?, 'gpt-4o-mini', ?, ?)`,
		id, ownerID, name, status, sealed, id, now, now)
}

func (e *testEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(e.bindFixture(statement), args...); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := e.db.QueryRow(e.bindFixture(query), args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (e *testEnv) queryCell(t *testing.T, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := e.db.QueryRow(e.bindFixture(query), args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value.String
}

// bindFixture 把测试手写 SQL 的 ? 占位符对齐生产 PG 臂的 $n。
func (e *testEnv) bindFixture(query string) string {
	return fixtureBind(query)
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

// dataArray extracts an array-shaped data envelope (options, tags).
func dataArray(t *testing.T, payload map[string]any) []any {
	t.Helper()
	data, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("missing data array: %v", payload)
	}
	return data
}

func createPayload(name string) string {
	// B1 断点批（设计 §8）：连接集合与健康检查三件套显式提交——gpt/gpt_openai_v1
	// 形态，检查模型取支持模型首个。
	return `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"` + name +
		`","type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},` +
		`"supportedModels":["gpt-4o-mini","gpt-4.1"],"status":"active","tags":["生产","主力"],` +
		`"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],` +
		`"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini"}`
}

// b1GPTFragment 是 B1 断点批夹具的 §8 连接 + 健康检查三件套 JSON 片段
// （gpt/gpt_openai_v1/chat_json 形态；healthCheckModel 必须属于载荷
// supportedModels），供内联创建载荷拼接。
const b1GPTFragment = `"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],` +
	`"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini"`

// b1GenericGPTFragment 是 generic（openai 兼容）语义 provider 的连接片段
// （generic_http driver 只允许 generic_* 预设，§1.1 组合表）。
const b1GenericGPTFragment = `"connections":[{"connectionPresetCode":"generic_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],` +
	`"healthCheckConnectionPresetCode":"generic_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini"`

// b1ConnectionsJSON 是 B1 断点批夹具的 §8 connections 片段构造器：单连接
// gpt_openai_v1 形态（endpointModes 可变），供域感知媒体夹具复用。
func b1ConnectionsJSON(endpointModes ...string) string {
	list := ""
	for index, mode := range endpointModes {
		if index > 0 {
			list += ","
		}
		list += `"` + mode + `"`
	}
	return `[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":[` + list + `],"enabled":true}]`
}

func TestAccountCreateLifecycleAndSealedCredentials(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// Create: 201 + revision pair, credentials sealed at rest.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	created := dataMap(t, payload)
	id := created["id"].(string)
	if !strings.HasPrefix(id, "acc_") {
		t.Fatalf("id prefix: %v", id)
	}
	if created["status"] != "active" || created["configRevision"] != float64(1) {
		t.Fatalf("create payload: %v", created)
	}
	// The create transaction lands the dispatch family advance (BUG-0174 M-8,
	// repositories.ts:2412-2417), so dispatch_revision starts at 2.
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND config_revision = 1 AND dispatch_revision = 2
		AND status = 'active' AND schedulable = 1 AND system_account_id = ?`, id, adminID) != 1 {
		t.Fatal("create row contract violated")
	}

	// Stored row: ciphertext hides the plaintext and decrypts back; the
	// fingerprint and mask mirror Node's sha256/maskSecret columns.
	sealed := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id)
	if strings.Contains(sealed, "sk-live-secret-1234567890") {
		t.Fatal("credentials_encrypted must not contain the plaintext secret")
	}
	var unsealed Credentials
	if err := DecryptJSON(testSecret, sealed, &unsealed); err != nil {
		t.Fatalf("decrypt stored credentials: %v", err)
	}
	if unsealed["api_key"] != "sk-live-secret-1234567890" || unsealed["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("round trip mismatch: %v", unsealed)
	}
	sum := sha256.Sum256([]byte("sk-live-secret-1234567890"))
	if got := env.queryCell(t, `SELECT credential_fingerprint FROM accounts WHERE id = ?`, id); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("credential_fingerprint: %v", got)
	}
	if got := env.queryCell(t, `SELECT credential_mask FROM accounts WHERE id = ?`, id); got != "sk-liv***7890" {
		t.Fatalf("credential_mask: %v", got)
	}

	// Satellites: group binding, supported models, tags, name search.
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ? AND enabled = 1`, id) != 1 {
		t.Fatal("group binding missing")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_supported_models WHERE account_id = ?`, id) != 2 {
		t.Fatal("supported models missing")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = ?`, id) != 2 {
		t.Fatal("tag bindings missing")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_name_search_documents WHERE account_id = ?`, id) != 1 {
		t.Fatal("name search document missing")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_name_search_terms WHERE account_id = ? AND term = 'alp'`, id) != 1 {
		t.Fatal("name search terms missing")
	}

	// Detail: owner fields present, credentials surface unmasked for editing.
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, detail)
	}
	detailData := dataMap(t, detail)
	// 供应商解耦（设计 §9.11）：账户 provider 回显取 vendor_code（gpt）。
	if detailData["name"] != "alpha" || detailData["ownerSystemAccountId"] != adminID ||
		detailData["providerCode"] != "gpt" ||
		detailData["status"] != "active" || detailData["configRevision"] != float64(1) {
		t.Fatalf("detail contract: %v", detailData)
	}
	// 编辑表单以 providerProtocolProfileId 选择账户类型与凭证组件；列退役后必须
	// 由健康检查连接 preset 派生（与 patch 读侧同源），空值会让编辑弹窗整块空白。
	if detailData["providerProtocolProfileId"] != "profile_gpt_openai_v1" {
		t.Fatalf("edit detail must derive profile id from health check preset: %v", detailData["providerProtocolProfileId"])
	}
	credentials := detailData["credentials"].(map[string]any)
	if credentials["api_key"] != "sk-live-secret-1234567890" {
		t.Fatalf("detail credentials must be readable for editing: %v", credentials)
	}
	if credentials["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("base_url 应原样返回：%v", credentials)
	}
	if detailData["boundGroupId"] == nil || detailData["boundGroupName"] == nil {
		t.Fatalf("bound group missing: %v", detailData)
	}
	// The edit-basic alias returns the same owner field set.
	code, alias := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id+"/edit-basic", "")
	if code != 200 || dataMap(t, alias)["id"] != id {
		t.Fatalf("edit-basic alias: %d %v", code, alias)
	}

	// List: item contract with permissions, tags, availability and usage zeros.
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, list)
	}
	listData := dataMap(t, list)
	items := listData["items"].([]any)
	if len(items) != 1 || listData["page"] != float64(1) || listData["pageSize"] != float64(50) {
		t.Fatalf("list payload: %v", list)
	}
	item := items[0].(map[string]any)
	if item["id"] != id || item["accessType"] != "owner" || item["groupBindStatus"] != "bound" {
		t.Fatalf("item identity: %v", item)
	}
	permissions := item["permissions"].(map[string]any)
	if permissions["canEdit"] != true || permissions["canDelete"] != true || permissions["canLock"] != true {
		t.Fatalf("owner permissions: %v", permissions)
	}
	if len(item["tags"].([]any)) != 2 {
		t.Fatalf("tags: %v", item["tags"])
	}
	effective := item["effectiveAvailability"].(map[string]any)
	if effective["available"] != true || effective["status"] != "available" {
		t.Fatalf("effective availability: %v", effective)
	}
	usage := item["usage"].(map[string]any)
	if usage["requestCount"] != float64(0) || usage["totalTokens"] != float64(0) {
		t.Fatalf("usage zero value: %v", usage)
	}
	if _, hasOwner := item["systemAccountId"]; !hasOwner {
		t.Fatal("admin list must include owner fields")
	}

	// Operation log: create recorded with masked credentials.
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["accounts.create"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
	if !env.sink.sensitive("credentials", "sk-live-secret-1234567890") {
		t.Fatal("credentials changes must be recorded sensitive without material")
	}
}

func TestAccountListPagingAndFilters(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	ids := []string{}
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}
	// bravo becomes disabled, charlie keeps pending_test.
	if _, err := env.db.Exec(`UPDATE accounts SET status = 'disabled' WHERE name = 'bravo'`); err != nil {
		t.Fatal(err)
	}

	// Pagination: pageSize=2 probes pageSize+1 and reports the upper bound.
	code, page := env.do(t, http.MethodGet, "/__aisys__/api/accounts?pageSize=2", "")
	if code != http.StatusOK {
		t.Fatalf("page: %d %v", code, page)
	}
	pageData := dataMap(t, page)
	if len(pageData["items"].([]any)) != 2 || pageData["hasMore"] != true || pageData["total"] != float64(3) {
		t.Fatalf("page payload: %v", page)
	}
	// Default sort is priority asc then created/id asc: stable identity order
	// across repeated fetches (same-millisecond creates fall back to id order).
	code, page2 := env.do(t, http.MethodGet, "/__aisys__/api/accounts?pageSize=2", "")
	if code != http.StatusOK {
		t.Fatalf("second page fetch: %d %v", code, page2)
	}
	firstSequence := ""
	for _, entry := range pageData["items"].([]any) {
		firstSequence += entry.(map[string]any)["id"].(string) + ","
	}
	secondSequence := ""
	for _, entry := range dataMap(t, page2)["items"].([]any) {
		secondSequence += entry.(map[string]any)["id"].(string) + ","
	}
	if firstSequence != secondSequence || firstSequence == "" {
		t.Fatalf("unstable page order: %s vs %s", firstSequence, secondSequence)
	}

	// Keyword prefix match.
	code, keyword := env.do(t, http.MethodGet, "/__aisys__/api/accounts?keyword=bra", "")
	if items := dataMap(t, keyword)["items"].([]any); code != 200 || len(items) != 1 || items[0].(map[string]any)["name"] != "bravo" {
		t.Fatalf("keyword filter: %d %v", code, keyword)
	}
	// Contains via the search terms index.
	code, contains := env.do(t, http.MethodGet, "/__aisys__/api/accounts?keyword=arli", "")
	if items := dataMap(t, contains)["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("contains filter: %d %v", code, contains)
	}
	// Status filter (effective = disabled column state here).
	code, status := env.do(t, http.MethodGet, "/__aisys__/api/accounts?status=disabled", "")
	if items := dataMap(t, status)["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("status filter: %d %v", code, status)
	}
	// Type + provider filters.
	code, typed := env.do(t, http.MethodGet, "/__aisys__/api/accounts?type=api_key&providerCode=gpt", "")
	if items := dataMap(t, typed)["items"].([]any); code != 200 || len(items) != 3 {
		t.Fatalf("type/provider filter: %d %v", code, typed)
	}
	// Group filter through the enabled binding.
	groupID := env.queryCell(t, `SELECT group_id FROM group_accounts WHERE account_id = ?`, ids[0])
	code, group := env.do(t, http.MethodGet, "/__aisys__/api/accounts?groupId="+groupID, "")
	if items := dataMap(t, group)["items"].([]any); code != 200 || len(items) != 3 {
		t.Fatalf("group filter: %d %v", code, group)
	}
	// ids filter.
	code, byIDs := env.do(t, http.MethodGet, "/__aisys__/api/accounts?ids="+ids[1]+","+ids[2], "")
	if items := dataMap(t, byIDs)["items"].([]any); code != 200 || len(items) != 2 {
		t.Fatalf("ids filter: %d %v", code, byIDs)
	}
	// Schedulable=disabled: bravo (disabled) + nothing else.
	code, sched := env.do(t, http.MethodGet, "/__aisys__/api/accounts?schedulable=disabled", "")
	if items := dataMap(t, sched)["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("schedulable filter: %d %v", code, sched)
	}
	// Name sort desc flips the order.
	code, sorted := env.do(t, http.MethodGet, "/__aisys__/api/accounts?sorts=name:desc&pageSize=3", "")
	sortedItems := dataMap(t, sorted)["items"].([]any)
	if code != 200 || sortedItems[0].(map[string]any)["name"] != "charlie" {
		t.Fatalf("name sort: %d %v", code, sorted)
	}
	// Admin scope filter.
	code, scoped := env.do(t, http.MethodGet, "/__aisys__/api/accounts?systemAccountId="+adminID, "")
	if items := dataMap(t, scoped)["items"].([]any); code != 200 || len(items) != 3 {
		t.Fatalf("scope filter: %d %v", code, scoped)
	}
}

func TestAccountCreateValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// Distinct names per attempt: the mutation guard fingerprints {owner,
	// name, ...}, so a failed create poisons that name for the failed-TTL
	// window (same as Node).
	cases := []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{"unknown field", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"x1","type":"api_key","bogus":1}`, 400, "账户参数无效"},
		{"missing provider", `{"providerProtocolProfileId":"prof-gpt","name":"x2","type":"api_key"}`, 400, "账户参数无效"},
		{"unknown provider", `{"providerCode":"anthropic","providerProtocolProfileId":"prof-gpt","name":"x3","type":"api_key"}`, 400, "不支持的供应商：anthropic"},
		// B1 剩余①：账户类型合法性由 registry 组合门承载（google_oauth 与
		// gpt_openai_v1 组合不在 §1.1 表内）；载荷未携连接时先触发
		// connections 必填校验（§8 连接集合前置）。
		{"bad account type", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"x4","type":"google_oauth"}`, 400, "创建载荷缺少 connections：必须显式提交非空连接集合（connectionPresetCode/baseUrl/endpointModes/enabled）"},
		// B1 剩余①：connections 必填校验（§8 连接集合前置）先于凭据归一化触发。
		{"missing credentials", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"x5","type":"api_key","supportedModels":["gpt-4o-mini"]}`, 400, "创建载荷缺少 connections：必须显式提交非空连接集合（connectionPresetCode/baseUrl/endpointModes/enabled）"},
		// generic 语义 provider（非 §1.1 真实厂商）不再有 vendor 拒绝臂
		//（vendor_code=NULL，设计 §2.1）；载荷未携连接时先触发 connections
		// 必填校验。
		{"unknown provider", `{"providerCode":"provider-zzz","providerProtocolProfileId":"prof-gpt","name":"g2","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"]}`, 400, "创建载荷缺少 connections：必须显式提交非空连接集合（connectionPresetCode/baseUrl/endpointModes/enabled）"},
		// 检查模型越界（b1GPTFragment 连接合法 + 检查模型不在支持集合）。
		{"unsupported health model", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"h1","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"claude-3"}`, 400, "账户检查模型必须属于账户支持模型"},
		{"super and fallback", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"sf1","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],` + b1GPTFragment + `,"superPriorityEnabled":true,"fallbackEnabled":true}`, 400, "超级优先和降级备用不能同时开启"},
		{"bad expires", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"e1","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],"accountExpiresAt":"yesterday",` + b1GPTFragment + `}`, 400, "账户套餐到期时间必须是有效时间字符串"},
	}
	// Enabled provider whose owner lacks a default group.
	now2 := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-openai', 'openai', 'OpenAI 平台', 1, ?, ?)`, now2, now2)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('prof-openai', 'openai', 'OpenAI 平台协议', 1, 'openai', 'v1', 'https://api.openai.com/v1',
		'gpt-4o-mini', '["api_key","oauth"]', '[]', ?, ?)`, now2, now2)

	// B1 断点批：分组缺失臂先于三件套校验触发（分组解析在前），无需携带连接；
	// 下方成功路径载荷统一补 b1GPTFragment。

	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", testCase.body)
		if code != testCase.status || payload["message"] != testCase.message {
			t.Fatalf("%s: %d %v (want %d %s)", testCase.name, code, payload, testCase.status, testCase.message)
		}
	}

	// 缺少启用默认分组（组无 provider 维度，owner 是唯一解析维度，设计 §8）：
	// 停用 owner 的默认分组后创建按 400 拒绝（分组解析先于三件套校验之外的
	// 后续写段；generic 片段用 generic_openai_v1——generic_http driver 组合表）。
	env.exec(t, `UPDATE groups SET enabled = 0 WHERE id = ?`, "grp-default-"+adminID)
	code, missingGroup := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"openai","providerProtocolProfileId":"prof-openai","name":"g3","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],`+b1GenericGPTFragment+`}`)
	if code != 400 || missingGroup["message"] != "当前用户缺少启用的默认分组" {
		t.Fatalf("missing default group: %d %v", code, missingGroup)
	}
	env.exec(t, `UPDATE groups SET enabled = 1 WHERE id = ?`, "grp-default-"+adminID)

	// Explicit group bound to another provider is rejected; same-provider
	// explicit groups are honored.
	code, wrongGroup := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"wg","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],`+b1GPTFragment+`,"groupId":"grp-missing"}`)
	if code != 400 || wrongGroup["message"] != "账户分组无效" {
		t.Fatalf("wrong group: %d %v", code, wrongGroup)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-extra', ?, '扩展分组', 1, 0, 'personal', ?, ?)`, adminID, now, now)
	code, okGroup := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"okgroup","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],`+b1GPTFragment+`,"groupId":"grp-extra"}`)
	if code != http.StatusCreated {
		t.Fatalf("explicit group create: %d %v", code, okGroup)
	}
	if got := env.queryCell(t, `SELECT group_id FROM group_accounts WHERE account_id = ?`, dataMap(t, okGroup)["id"]); got != "grp-extra" {
		t.Fatalf("explicit group binding: %v", got)
	}

	// Duplicate owner-scoped name → 409 with the Node copy.
	code, duplicate := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha2"))
	if code != http.StatusCreated {
		t.Fatalf("alpha2 create: %d %v", code, duplicate)
	}
	env.exec(t, `UPDATE accounts SET name = 'alpha' WHERE id = ?`, dataMap(t, duplicate)["id"])
	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/accounts?systemAccountId="+adminID, createPayload("alpha"))
	if code != http.StatusConflict || conflict["message"] != "同一用户下账户名称已存在：alpha" {
		t.Fatalf("duplicate name: %d %v", code, conflict)
	}

	// Default creation status: pending_test + health-check notice + paused
	// scheduling (Node's accountCreationStatusInput derivation).
	code, pending := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"pending","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],`+b1GPTFragment+`}`)
	if code != http.StatusCreated {
		t.Fatalf("pending create: %d %v", code, pending)
	}
	pendingID := dataMap(t, pending)["id"].(string)
	if dataMap(t, pending)["status"] != "pending_test" {
		t.Fatalf("pending status: %v", pending)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'pending_test' AND schedulable = 0
		AND last_error_message = '账户已保存，等待后台健康检查'`, pendingID) != 1 {
		t.Fatal("pending creation contract violated")
	}

	// Expired package disables at creation with the account_expired marker.
	code, expired := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"expired","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],"status":"active","accountExpiresAt":"2000-01-01T00:00:00Z",`+b1GPTFragment+`}`)
	if code != http.StatusCreated {
		t.Fatalf("expired create: %d %v", code, expired)
	}
	expiredID := dataMap(t, expired)["id"].(string)
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'disabled' AND schedulable = 0
		AND last_error_code = 'account_expired'`, expiredID) != 1 {
		t.Fatal("expired creation contract violated")
	}
}

func TestAccountPatchOptimisticLock(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)

	// Successful basic edit: revision increments, diff is reported.
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"name":"alpha-renamed","notes":"运营备注","priority":3,"concurrencyLimit":800,"schedulable":false}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	patchData := dataMap(t, patched)
	if patchData["configRevision"] != float64(2) {
		t.Fatalf("patch revision: %v", patchData)
	}
	changed := map[string]bool{}
	for _, field := range patchData["changedFields"].([]any) {
		changed[field.(string)] = true
	}
	for _, field := range []string{"name", "notes", "priority", "concurrencyLimit", "schedulable"} {
		if !changed[field] {
			t.Fatalf("changed fields missing %s: %v", field, patchData["changedFields"])
		}
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND config_revision = 2 AND name = 'alpha-renamed'
		AND priority = 3 AND concurrency_limit = 800 AND schedulable = 0`, id) != 1 {
		t.Fatal("patch row contract violated")
	}

	// Stale revision → 409 with the Node copy.
	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"name":"too-late"}`)
	if code != http.StatusConflict || conflict["message"] != "账户配置已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale revision: %d %v", code, conflict)
	}

	// Status + tags patch path (tag endpoint variant shares the store).
	code, tagsPatched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id+"/tags",
		`{"expectedConfigRevision":2,"tags":["观察"]}`)
	if code != http.StatusOK || dataMap(t, tagsPatched)["configRevision"] != float64(3) {
		t.Fatalf("tags patch: %d %v", code, tagsPatched)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = ?`, id) != 1 {
		t.Fatal("tags replace missing")
	}

	// Credentials patch: re-sealed with fresh fingerprint, still no plaintext.
	code, credentialsPatched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":3,"credentials":{"api_key":"sk-rotated-secret-9876543210"}}`)
	if code != http.StatusOK {
		t.Fatalf("credentials patch: %d %v", code, credentialsPatched)
	}
	sealed := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id)
	if strings.Contains(sealed, "sk-rotated-secret-9876543210") {
		t.Fatal("rotated credentials must be sealed")
	}
	sum := sha256.Sum256([]byte("sk-rotated-secret-9876543210"))
	if got := env.queryCell(t, `SELECT credential_fingerprint FROM accounts WHERE id = ?`, id); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("rotated fingerprint: %v", got)
	}
	// base_url survives the merge semantics.
	var credentials Credentials
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("credential merge dropped base_url: %v", credentials)
	}

	// Validation set.
	code, badRevision := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, `{"expectedConfigRevision":0}`)
	if code != 400 || badRevision["message"] != "账户配置版本无效" {
		t.Fatalf("bad revision: %d %v", code, badRevision)
	}
	code, unknown := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, `{"expectedConfigRevision":4,"bogus":1}`)
	if code != 400 || unknown["message"] != "账户更新参数无效" {
		t.Fatalf("unknown field: %d %v", code, unknown)
	}
	code, badStatus := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, `{"expectedConfigRevision":4,"status":"flying"}`)
	if code != 400 || badStatus["message"] != "账户状态无效" {
		t.Fatalf("bad status: %d %v", code, badStatus)
	}
	code, blankName := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, `{"expectedConfigRevision":4,"name":"  "}`)
	if code != 400 || blankName["message"] != "账户名称不能为空" {
		t.Fatalf("blank name: %d %v", code, blankName)
	}

	// Operation log: update entries recorded, credential material masked.
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["accounts.update"] || !seen["accounts.update_tags"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
	if !env.sink.sensitive("credentials", "sk-rotated-secret-9876543210") {
		t.Fatal("credentials changes must be sensitive without material")
	}
}

func TestAccountLockUnlockFamily(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)

	// Lock: LOCKED_IDLE + generation 1 + config revision bump.
	code, locked := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/lock", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("lock: %d %v", code, locked)
	}
	state := dataMap(t, locked)
	if state["lockState"] != "LOCKED_IDLE" || state["enabled"] != true || state["generation"] != float64(1) ||
		state["lockDeathTimeoutSeconds"] != float64(300) || state["lockRetryIntervalSeconds"] != float64(5) {
		t.Fatalf("lock state: %v", state)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND config_revision = 2`, id) != 1 {
		t.Fatal("lock must bump config_revision")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_lock_states WHERE account_id = ? AND enabled = 1 AND lock_state = 'LOCKED_IDLE'`, id) != 1 {
		t.Fatal("lock state row missing")
	}

	// List items carry the lock overlay.
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	var lockItem map[string]any
	for _, entry := range dataMap(t, list)["items"].([]any) {
		if entry.(map[string]any)["id"] == id {
			lockItem = entry.(map[string]any)
		}
	}
	if lockItem == nil || lockItem["lockState"] != "LOCKED_IDLE" ||
		lockItem["lockDeathTimeoutSeconds"] != float64(300) {
		t.Fatalf("lock overlay: %v", lockItem)
	}

	// Stale config revision → 409 (lock copy).
	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/lock", `{"expectedConfigRevision":1,"lockDeathTimeoutSeconds":300}`)
	if code != http.StatusConflict || conflict["message"] != "账户配置已发生并发变更，请刷新列表后重试" {
		t.Fatalf("stale lock: %d %v", code, conflict)
	}

	// Lock-config: keep enabled, update timeout/interval.
	code, configured := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/lock-config",
		`{"expectedConfigRevision":2,"lockDeathTimeoutSeconds":600,"lockRetryIntervalSeconds":15}`)
	if code != http.StatusOK {
		t.Fatalf("lock-config: %d %v", code, configured)
	}
	configState := dataMap(t, configured)
	if configState["lockState"] != "LOCKED_IDLE" || configState["lockDeathTimeoutSeconds"] != float64(600) ||
		configState["lockRetryIntervalSeconds"] != float64(15) || configState["generation"] != float64(2) {
		t.Fatalf("lock-config state: %v", configState)
	}
	code, noFields := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/lock-config", `{"expectedConfigRevision":3}`)
	if code != 400 || noFields["message"] != "请至少提交一项锁死配置" {
		t.Fatalf("empty lock-config: %d %v", code, noFields)
	}

	// Unlock.
	code, unlocked := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/unlock", `{"expectedConfigRevision":3}`)
	if code != http.StatusOK {
		t.Fatalf("unlock: %d %v", code, unlocked)
	}
	unlockState := dataMap(t, unlocked)
	if unlockState["lockState"] != "UNLOCKED" || unlockState["enabled"] != false {
		t.Fatalf("unlock state: %v", unlockState)
	}

	// Missing account → 404 with the lock copy.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/lock", `{"expectedConfigRevision":1}`)
	if code != http.StatusNotFound || missing["message"] != "账户不存在或无权操作" {
		t.Fatalf("missing lock: %d %v", code, missing)
	}

	// Operation logs for the family.
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	for _, want := range []string{"accounts.lock", "accounts.unlock", "accounts.lock-config"} {
		if !seen[want] {
			t.Fatalf("operation log actions: %v", env.sink.actions())
		}
	}
}

func TestAccountOptionsEndpoint(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	ids := []string{}
	for _, name := range []string{"alpha", "bravo"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}
	env.exec(t, `UPDATE accounts SET status = 'disabled' WHERE id = ?`, ids[1])

	code, options := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options", "")
	if code != http.StatusOK {
		t.Fatalf("options: %d %v", code, options)
	}
	items := dataArray(t, options)
	if len(items) != 2 {
		t.Fatalf("options payload: %v", options)
	}
	first := items[0].(map[string]any)
	if first["accessType"] != "owner" || first["permissions"] == nil || first["status"] == nil {
		t.Fatalf("option contract: %v", first)
	}
	// ids filter + keyword.
	code, filtered := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?ids="+ids[1], "")
	if items := dataArray(t, filtered); code != 200 || len(items) != 1 {
		t.Fatalf("ids filter: %d %v", code, filtered)
	}
	code, keyword := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?keyword=alp", "")
	if items := dataArray(t, keyword); code != 200 || len(items) != 1 {
		t.Fatalf("keyword filter: %d %v", code, keyword)
	}
	// status filter drops the disabled account from the enabled option set.
	code, active := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?status=active", "")
	if items := dataArray(t, active); code != 200 || len(items) != 1 {
		t.Fatalf("status filter: %d %v", code, active)
	}
	// limit clamp (1..50).
	code, limited := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?limit=1", "")
	if items := dataArray(t, limited); code != 200 || len(items) != 1 {
		t.Fatalf("limit: %d %v", code, limited)
	}
	// Self surface: forceSelfAccessScope pins the caller.
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		SELECT 'prov-gpt2', 'gpt', 'OpenAI', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'
		WHERE NOT EXISTS (SELECT 1 FROM providers WHERE code = 'gpt')`)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		SELECT 'prof-gpt2', 'gpt', 'OpenAI 官方协议', 1, 'openai', 'v1', 'https://api.openai.com/v1', 'gpt-4o-mini',
		'["api_key","oauth"]', '[]', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'
		WHERE NOT EXISTS (SELECT 1 FROM provider_protocol_profiles WHERE id = 'prof-gpt2')`)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-alice', ?, '默认分组', 1, 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, aliceID)
	code, mine := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/options", "")
	if items := dataArray(t, mine); code != 200 || len(items) != 0 {
		t.Fatalf("alice empty options: %d %v", code, mine)
	}
	code, myCreate := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts", createPayload("alice-account"))
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, myCreate)
	}
	code, mineAfter := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/options", "")
	items = dataArray(t, mineAfter)
	if code != 200 || len(items) != 1 {
		t.Fatalf("alice options: %d %v", code, mineAfter)
	}
	if _, hasOwner := items[0].(map[string]any)["systemAccountId"]; hasOwner {
		t.Fatal("self surface must omit systemAccountId")
	}
}

// T79（BUG 复审）：owner 作用域 options?groupId= 的占位符顺序曾交叉绑定
// （group_id 占位符被塞入 scoped、system_account_id 占位符被塞入 groupId），
// 任何 owner 作用域分组过滤恒空集。本用例要求数据级命中，杜绝回归。
func TestAccountOptionsOwnerScopeGroupIdFilter(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-alice-t79', ?, '默认分组', 1, 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, aliceID)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts", createPayload("alice-t79"))
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, created)
	}
	code, hit := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/options?groupId=grp-alice-t79", "")
	if items := dataArray(t, hit); code != http.StatusOK || len(items) != 1 {
		t.Fatalf("owner groupId hit: %d %v", code, hit)
	}
	code, miss := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/options?groupId=grp-other", "")
	if items := dataArray(t, miss); code != http.StatusOK || len(items) != 0 {
		t.Fatalf("owner groupId miss: %d %v", code, miss)
	}
	// 管理视图断言前切回管理员会话（env.do 复用最近一次登录的 cookie）。
	env.login(t, "root", "root-pass", "super_admin")
	code, adminView := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?groupId=grp-alice-t79", "")
	if items := dataArray(t, adminView); code != http.StatusOK || len(items) != 1 {
		t.Fatalf("admin groupId: %d %v", code, adminView)
	}
}

func TestAccountDeleteSoftCleanup(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)

	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	// Soft delete semantics: row survives with the tombstone columns set.
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND deleted_at IS NOT NULL
		AND status = 'disabled' AND schedulable = 0 AND cooldown_until IS NULL AND deleted_by = ?`, id, adminID) != 1 {
		t.Fatal("soft delete contract violated")
	}
	// Satellites are cleaned.
	if env.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = ?`, id) != 0 {
		t.Fatal("tag bindings must be cleaned")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_name_search_terms WHERE account_id = ?`, id) != 0 {
		t.Fatal("name search terms must be cleaned")
	}
	// Reads 404 with the Node copy; list drops the row.
	code, gone := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNotFound || gone["message"] != "账户不存在" {
		t.Fatalf("after delete: %d %v", code, gone)
	}
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if items := dataMap(t, list)["items"].([]any); code != 200 || len(items) != 0 {
		t.Fatalf("list after delete: %d %v", code, list)
	}
	// Double delete 404s.
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNotFound {
		t.Fatalf("double delete: %d", code)
	}
	seen := false
	for _, action := range env.sink.actions() {
		if action == "accounts.delete" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
}

func TestAccountTagsEndpoints(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)

	// Tag list with the live binding count.
	code, tags := env.do(t, http.MethodGet, "/__aisys__/api/accounts/tags", "")
	if code != http.StatusOK {
		t.Fatalf("tags: %d %v", code, tags)
	}
	tagItems := dataArray(t, tags)
	if len(tagItems) != 2 {
		t.Fatalf("tags payload: %v", tags)
	}
	counts := map[string]float64{}
	for _, entry := range tagItems {
		tag := entry.(map[string]any)
		counts[tag["name"].(string)] = tag["accountCount"].(float64)
	}
	if counts["生产"] != 1 || counts["主力"] != 1 {
		t.Fatalf("tag counts: %v", counts)
	}

	// In-use tag refuses deletion.
	productionID := env.queryCell(t, `SELECT id FROM account_tags WHERE name = '生产' AND system_account_id = ?`, adminID)
	code, inUse := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/"+productionID, "")
	if code != 400 || inUse["message"] != "标签已绑定账户，不能删除" {
		t.Fatalf("in-use tag: %d %v", code, inUse)
	}
	// Unbind then delete succeeds.
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id+"/tags", `{"expectedConfigRevision":1,"tags":[]}`)
	if code != http.StatusOK {
		t.Fatalf("unbind: %d %v", code, tags)
	}
	code, deleted := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/"+productionID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete unused tag: %d %v", code, deleted)
	}
	// Missing tag 404.
	code, missing := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/tag-missing", "")
	if code != http.StatusNotFound || missing["message"] != "标签不存在" {
		t.Fatalf("missing tag: %d %v", code, missing)
	}
}

func TestAccountPermissionMatrix(t *testing.T) {
	env := newTestEnv(t)

	// Anonymous requests are refused on both surfaces.
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusUnauthorized || anonymous["message"] != "请先登录" {
		t.Fatalf("anonymous my list: %d %v", code, anonymous)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin list: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/my-accounts/acc-x", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous my delete: %d", code)
	}

	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.seedProviderAndDefaultGroup(t, aliceID)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts", createPayload("alice-account"))
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, created)
	}
	aliceAccountID := dataMap(t, created)["id"].(string)

	// user role cannot reach the admin surface (permission matrix).
	code, denied := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusForbidden || denied["message"] != "需要管理员权限" {
		t.Fatalf("admin list as user: %d %v", code, denied)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("nope"))
	if code != http.StatusForbidden {
		t.Fatalf("admin create as user: %d", code)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+aliceAccountID, `{"expectedConfigRevision":1,"notes":"x"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin patch as user: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+aliceAccountID, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin delete as user: %d", code)
	}

	// Bob cannot see or mutate alice's account through my-*.
	env.login(t, "bob", "bob-pass", "user")
	code, forbidden := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/"+aliceAccountID, "")
	if code != http.StatusNotFound || forbidden["message"] != "账户不存在" {
		t.Fatalf("bob detail alice account: %d %v", code, forbidden)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/my-accounts/"+aliceAccountID, `{"expectedConfigRevision":1,"notes":"hijack"}`)
	if code != http.StatusNotFound {
		t.Fatalf("bob patch alice account: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/my-accounts/"+aliceAccountID, "")
	if code != http.StatusNotFound {
		t.Fatalf("bob delete alice account: %d", code)
	}

	// Alice still sees her own account on the self surface (no owner fields).
	env.login(t, "alice", "alice-pass", "user")
	code, myList := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	myItems := dataMap(t, myList)["items"].([]any)
	if code != 200 || len(myItems) != 1 || myItems[0].(map[string]any)["id"] != aliceAccountID {
		t.Fatalf("alice list: %d %v", code, myList)
	}
	if _, hasOwner := myItems[0].(map[string]any)["systemAccountId"]; hasOwner {
		t.Fatal("self surface must omit systemAccountId")
	}
	// Blank explicit scope on the detail route is rejected.
	code, blank := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/"+aliceAccountID+"?systemAccountId=", "")
	if code != http.StatusBadRequest || blank["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope: %d %v", code, blank)
	}

	// Admin sees alice's account with owner fields and can scope-filter.
	env.login(t, "root", "root-pass", "super_admin")
	code, all := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	adminItems := dataMap(t, all)["items"].([]any)
	if code != 200 || len(adminItems) != 1 {
		t.Fatalf("admin list: %d %v", code, all)
	}
	if adminItems[0].(map[string]any)["systemAccountId"] != aliceID {
		t.Fatalf("admin owner fields: %v", adminItems[0])
	}
	code, otherOwner := env.do(t, http.MethodGet, "/__aisys__/api/accounts?systemAccountId=someone-else", "")
	if items := dataMap(t, otherOwner)["items"].([]any); code != 200 || len(items) != 0 {
		t.Fatalf("admin other-owner filter: %d %v", code, otherOwner)
	}
	// my-* for an admin is force-self-scoped: the admin owns nothing here.
	code, adminSelf := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if items := dataMap(t, adminSelf)["items"].([]any); code != 200 || len(items) != 0 {
		t.Fatalf("admin my list must be self-scoped: %d %v", code, adminSelf)
	}
}

func TestCryptoMaskAndNameSearchUnits(t *testing.T) {
	// MaskSecret mirrors maskSecret.
	if got := MaskSecret("sk-live-secret-1234567890"); got != "sk-liv***7890" {
		t.Fatalf("long mask: %v", got)
	}
	if got := MaskSecret("shortkey"); got != "sh***ey" {
		t.Fatalf("short mask: %v", got)
	}
	if got := MaskSecret(""); got != "" {
		t.Fatalf("empty mask: %v", got)
	}
	if got := MaskSecret(12345); got != "" {
		t.Fatalf("non-string mask: %v", got)
	}

	// Account ids follow Node newId('acc').
	id := NewAccountID()
	if !strings.HasPrefix(id, "acc_") || !strings.Contains(id, "_") {
		t.Fatalf("account id shape: %v", id)
	}

	// Name search grams mirror the 1..3-gram contract.
	terms := buildAccountNameSearchTerms("alpha")
	if len(terms) != 11 || !containsTerm(terms, "alp") || !containsTerm(terms, "lph") || containsTerm(terms, "alpha") {
		t.Fatalf("name search terms: %v", terms)
	}
	if got := normalizeAccountNameSearchText("  ＡＬＰＨＡ "); got != "ALPHA" {
		t.Fatalf("nfkc normalize: %q", got)
	}

	// Encryption round trip + tamper rejection (Node envelope layout).
	envelope, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-roundtrip"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(envelope, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		t.Fatalf("envelope shape: %v", envelope)
	}
	var payload Credentials
	if err := DecryptJSON(testSecret, envelope, &payload); err != nil || payload["api_key"] != "sk-roundtrip" {
		t.Fatalf("round trip: %v %v", err, payload)
	}
	if err := DecryptJSON("other-secret", envelope, &payload); err == nil {
		t.Fatal("wrong secret must fail")
	}
}

func containsTerm(terms []string, target string) bool {
	for _, term := range terms {
		if term == target {
			return true
		}
	}
	return false
}

func TestAccountCloneContext(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("clone-src"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	accountID := dataMap(t, payload)["id"].(string)

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+accountID+"/clone-context", "")
	if code != 200 {
		t.Fatalf("clone-context: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["name"] != "clone-src" {
		t.Fatalf("clone name = %v", data["name"])
	}
	if data["configRevision"] == nil {
		t.Fatal("configRevision missing")
	}
	// 克隆表单以 providerProtocolProfileId 选择账户类型与凭证组件；列退役后必须
	// 由健康检查连接 preset 派生（与编辑面读侧同源，BUG-0332/0333），空值会让
	// 克隆弹窗整块空白、创建按钮禁用。
	if data["providerProtocolProfileId"] != "profile_gpt_openai_v1" {
		t.Fatalf("clone context must derive profile id from health check preset: %v", data["providerProtocolProfileId"])
	}
	raw, _ := json.Marshal(payload)
	if strings.Contains(string(raw), "sk-live-secret-1234567890") {
		t.Fatal("clone context leaked raw credential material")
	}

	missingCode, _ := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc_nope/clone-context", "")
	if missingCode != http.StatusNotFound {
		t.Fatalf("missing clone-context: %d", missingCode)
	}
}

// TestCreateDerivesClientCompatibilityColumn 覆盖 E2E-FINDING #7 写侧修复：
// INSERT 的 client_compatibility 列必须取 deriveOpenAIAccountClientCompatibility
// 派生值（openai/v1 下的 gpt api_key/oauth → codex_responses），不得硬编码
// openai_standard；该列是调度侧 pinned 比较与凭据端点模式默认集的源头。
func TestCreateDerivesClientCompatibilityColumn(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("codex-兼容列"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	// 设计 §9.11：client_compatibility 列已从账户表退役，写侧派生值只进凭据
	// 归一化（下方 responses_sse 断言保持），不再有列可读。列存在性内省按 PG
	// information_schema（原 sqlite pragma_table_info 的 PG 对应物，列集契约
	// 与断言值不变）。
	if columns := env.count(t, `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'juhe_business' AND table_name = 'accounts' AND column_name = 'client_compatibility'`); columns != 0 {
		t.Fatalf("accounts.client_compatibility 列应已退役，实际存在 %d 列", columns)
	}
	// 端点模式默认集与列语义同源：codex_responses 账户必须带 responses_sse。
	sealed := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id)
	var credentials Credentials
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatalf("凭据解密失败: %v", err)
	}
	modes, ok := credentials["supported_endpoint_modes"].([]any)
	if !ok {
		t.Fatalf("supported_endpoint_modes 缺失: %v", credentials)
	}
	hasSSE := false
	for _, mode := range modes {
		if mode == "responses_sse" {
			hasSSE = true
		}
	}
	if !hasSSE {
		t.Fatalf("codex_responses 账户端点模式应含 responses_sse: %v", modes)
	}
}

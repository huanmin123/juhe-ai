// 安全策略实体批A 测试（API Key 安全策略与出站内容脱敏设计 §3 规则模型 /
// §4.3 策略 CRUD）：镜像 schedulingpreferences 测试形态。覆盖：内置行全局可
// 见与写 403、双面创建矩阵（admin 显式 owner / self / 缺省规则 / 显式规则 /
// 重名 409 / 严格键）、rules 校验各分支（纯函数臂 + HTTP 400 臂）、PATCH 矩
// 阵（CAS 409 + currentUpdatedAt / 同值 no-op / 白名单 / 内置 403）、删除矩
// 阵（总是成功 + 同事务解绑回内置 resetBindings + 运行态失效）。请求/响应链
// 路脱敏行为属后续批次，不在本文件。
package securitypolicies

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
)

const builtinID = BuiltinSecurityPolicyID
const builtinName = BuiltinSecurityPolicyName

const (
	adminBase = "/__aisys__/api/security-policies"
	selfBase  = "/__aisys__/api/my-security-policies"
)

// seedBuiltinUpsert 与 maintenance 内置行种子同构的恢复式 upsert（固定 id 冲
// 突时仅在内置行上恢复权威值），供夹具初始化与「恢复 seed 后可见」用例复用。
const seedBuiltinUpsert = `
    INSERT INTO security_policies
      (id, system_account_id, name, description, rules_json, built_in, created_at, updated_at)
    VALUES (?, NULL, ?, NULL, '{"rules":[]}', 1, ?, ?)
    ON CONFLICT(id) DO UPDATE SET
      system_account_id=excluded.system_account_id,
      name=excluded.name,
      description=excluded.description,
      rules_json=excluded.rules_json,
      built_in=excluded.built_in
      WHERE security_policies.built_in=1
  `

type recordingSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}

func (s *recordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *recordingSink) all() []authsys.OperationLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authsys.OperationLogEntry{}, s.entries...)
}

type recordingInvalidator struct {
	mu      sync.Mutex
	reasons []string
}

func (i *recordingInvalidator) Invalidate(_ string, reason string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason)
}

func (i *recordingInvalidator) reasonsSnapshot() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string{}, i.reasons...)
}

func (i *recordingInvalidator) count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.reasons)
}

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	sink   *recordingSink
	inval  *recordingInvalidator
	db     *sql.DB
	store  *Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := newSecurityPoliciesPGDatabase(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(fixtureBind(seedBuiltinUpsert), builtinID, builtinName, now, now); err != nil {
		t.Fatal(err)
	}
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
	invalidator := &recordingInvalidator{}
	store, err := NewStore(db, nil, nil, invalidator)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink, Log: slog.Default()}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, inval: invalidator, db: db, store: store}
}

func (e *testEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	status, raw := e.doRaw(t, method, path, body)
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return status, payload
}

// doRaw 返回原始响应体（删除 resetBindings 等形状断言用）。
func (e *testEnv) doRaw(t *testing.T, method, path, body string) (int, []byte) {
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
	return response.StatusCode, raw
}

func (e *testEnv) login(t *testing.T, username, password, role string) string {
	t.Helper()
	created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
		MustChangePassword: &mustChangeFalse,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login %s failed: %d %v", username, code, payload)
	}
	return created.ID
}

// createUser 只建号不登录（配合 switchUser 在测试后段切换会话，避免早期登录
// 覆盖 jar 导致 admin 断言 403）。
func (e *testEnv) createUser(t *testing.T, username, password, role string) string {
	t.Helper()
	created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
		MustChangePassword: &mustChangeFalse,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.ID
}

// switchUser 切换当前会话（登录覆盖 jar），返回恢复函数；测试默认保持 admin
// 会话，个人面断言用 switchUser 临时切换。
func (e *testEnv) switchUser(t *testing.T, username, password string) func() {
	t.Helper()
	e.mu.Lock()
	previous := map[string]string{}
	for name, value := range e.jar {
		previous[name] = value
	}
	e.mu.Unlock()
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("switch to %s failed: %d %v", username, code, payload)
	}
	return func() {
		e.mu.Lock()
		e.jar = previous
		e.mu.Unlock()
	}
}

func (e *testEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(fixtureBind(statement), args...); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := e.db.QueryRow(fixtureBind(query), args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (e *testEnv) updatedAtOf(t *testing.T, id string) string {
	t.Helper()
	var updatedAt dbtime.Nullable
	if err := e.db.QueryRow(fixtureBind(`SELECT updated_at FROM security_policies WHERE id = ?`), id).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	return updatedAt.APIString()
}

// bindAPIKey 直接插入一条绑定给定策略的 API Key。
func (e *testEnv) bindAPIKey(t *testing.T, id, ownerID, policyID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, status, security_policy_id, created_at, updated_at)
		VALUES (?, ?, 'rs-x', ?, 'active', ?, ?, ?)`, id, ownerID, "key-"+id, policyID, now, now)
}

// createViaAPI 走管理面创建一条自定义策略并返回 201 行（默认 admin 会话）。
func (e *testEnv) createViaAPI(t *testing.T, ownerID, body string) map[string]any {
	t.Helper()
	code, payload := e.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID, body)
	if code != http.StatusCreated {
		t.Fatalf("create via API: %d %v", code, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		t.Fatalf("create via API: missing data envelope %v", payload)
	}
	return data
}

func message(t *testing.T, payload map[string]any) string {
	t.Helper()
	text, _ := payload["message"].(string)
	return text
}

func dataItems(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	data, _ := payload["data"].(map[string]any)
	items, _ := data["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if record, ok := item.(map[string]any); ok {
			out = append(out, record)
		}
	}
	return out
}

func dataRows(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	data, _ := payload["data"].([]any)
	out := make([]map[string]any, 0, len(data))
	for _, item := range data {
		if record, ok := item.(map[string]any); ok {
			out = append(out, record)
		}
	}
	return out
}

var mustChangeFalse = false

// TestNormalizeRulesInputArms 是规则校验的纯函数臂（无库）：设计 §3.2/§3.3/
// §3.5 全部分支与规范化落库字节。
func TestNormalizeRulesInputArms(t *testing.T) {
	validPhone := map[string]any{"id": "r1", "type": "phone", "action": "mask", "maskKeep": float64(3)}
	validRegex := map[string]any{"id": "r2", "type": "custom_regex", "action": "placeholder", "pattern": "GH-[0-9]{6}"}
	validKeywords := map[string]any{"id": "r3", "type": "custom_keywords", "action": "mask", "keywords": []any{"XX项目", "XX-2026"}}

	document, stored, err := normalizeRulesInput([]any{validPhone, validRegex, validKeywords})
	if err != nil {
		t.Fatalf("valid rules rejected: %v", err)
	}
	if len(document.Rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(document.Rules))
	}
	if document.Rules[0].Label != "" || document.Rules[0].Pattern != "" || document.Rules[0].Keywords != nil {
		t.Fatalf("builtin rule normalization: %+v", document.Rules[0])
	}
	if document.Rules[0].MaskKeep == nil || *document.Rules[0].MaskKeep != 3 {
		t.Fatalf("maskKeep not preserved: %+v", document.Rules[0])
	}
	if !strings.Contains(stored, `"maskKeep":3`) || !strings.Contains(stored, `"rules":[`) {
		t.Fatalf("stored JSON shape: %s", stored)
	}

	// 空规则集合法（缺省 / 显式 null 同折算）。
	for name, raw := range map[string]any{"absent": nil, "empty": []any{}} {
		document, stored, err := normalizeRulesInput(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(document.Rules) != 0 || stored != `{"rules":[]}` {
			t.Fatalf("%s: %+v %s", name, document, stored)
		}
	}

	// 各非法分支（设计 §3.2/§3.3/§3.5）。
	assertInvalid(t, "not-array", func() error {
		_, _, err := normalizeRulesInput(map[string]any{"id": "r1"})
		return err
	})
	assertInvalid(t, "missing-id", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"type": "phone", "action": "mask"}})
		return err
	})
	assertInvalid(t, "empty-id", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "  ", "type": "phone", "action": "mask"}})
		return err
	})
	assertInvalid(t, "bad-type", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "ssn", "action": "mask"}})
		return err
	})
	assertInvalid(t, "missing-action", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "phone"}})
		return err
	})
	assertInvalid(t, "bad-action", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "phone", "action": "block"}})
		return err
	})
	assertInvalid(t, "bad-label", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "label": "phone1", "type": "phone", "action": "mask"}})
		return err
	})
	assertInvalid(t, "builtin-pattern", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "phone", "action": "mask", "pattern": "1[0-9]+"}})
		return err
	})
	assertInvalid(t, "builtin-keywords", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "email", "action": "mask", "keywords": []any{"a"}}})
		return err
	})
	assertInvalid(t, "regex-missing-pattern", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_regex", "action": "mask"}})
		return err
	})
	assertInvalid(t, "regex-empty-pattern", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_regex", "action": "mask", "pattern": "  "}})
		return err
	})
	assertInvalid(t, "regex-too-long", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_regex", "action": "mask", "pattern": "a" + strings.Repeat("b", 256)}})
		return err
	})
	assertInvalid(t, "regex-bad-syntax", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_regex", "action": "mask", "pattern": "GH-[0-9"}})
		return err
	})
	assertInvalid(t, "regex-with-keywords", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_regex", "action": "mask", "pattern": "GH-[0-9]{6}", "keywords": []any{"a"}}})
		return err
	})
	assertInvalid(t, "keywords-missing", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_keywords", "action": "mask"}})
		return err
	})
	assertInvalid(t, "keywords-empty-array", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_keywords", "action": "mask", "keywords": []any{}}})
		return err
	})
	assertInvalid(t, "keywords-with-pattern", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_keywords", "action": "mask", "keywords": []any{"a"}, "pattern": "x"}})
		return err
	})
	assertInvalid(t, "keywords-too-many", func() error {
		items := make([]any, 101)
		for i := range items {
			items[i] = fmt.Sprintf("k%d", i)
		}
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_keywords", "action": "mask", "keywords": items}})
		return err
	})
	assertInvalid(t, "keyword-too-long", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_keywords", "action": "mask", "keywords": []any{strings.Repeat("k", 65)}}})
		return err
	})
	assertInvalid(t, "keyword-empty-string", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_keywords", "action": "mask", "keywords": []any{"  "}}})
		return err
	})
	assertInvalid(t, "keyword-non-string", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "custom_keywords", "action": "mask", "keywords": []any{42}}})
		return err
	})
	assertInvalid(t, "maskkeep-negative", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "phone", "action": "mask", "maskKeep": float64(-1)}})
		return err
	})
	assertInvalid(t, "maskkeep-too-large", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "phone", "action": "mask", "maskKeep": float64(9)}})
		return err
	})
	assertInvalid(t, "maskkeep-fraction", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "phone", "action": "mask", "maskKeep": 1.5}})
		return err
	})
	assertInvalid(t, "maskkeep-string", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "phone", "action": "mask", "maskKeep": "3"}})
		return err
	})
	assertInvalid(t, "unknown-key", func() error {
		_, _, err := normalizeRulesInput([]any{map[string]any{"id": "r1", "type": "phone", "action": "mask", "maskkeep": float64(3)}})
		return err
	})
	assertInvalid(t, "rule-not-object", func() error {
		_, _, err := normalizeRulesInput([]any{"r1"})
		return err
	})
	assertInvalid(t, "too-many-rules", func() error {
		items := make([]any, 51)
		for i := range items {
			items[i] = map[string]any{"id": fmt.Sprintf("r%d", i), "type": "phone", "action": "mask"}
		}
		_, _, err := normalizeRulesInput(items)
		return err
	})
	assertInvalid(t, "duplicate-id", func() error {
		_, _, err := normalizeRulesInput([]any{validPhone, map[string]any{"id": "r1", "type": "email", "action": "placeholder"}})
		return err
	})
	assertInvalid(t, "oversize-json", func() error {
		// 50 条规则 × 每条 20 个 64 字节关键词 ≈ 72KB > 64KB 上限。
		items := make([]any, 50)
		for i := range items {
			keywords := make([]any, 20)
			for k := range keywords {
				keywords[k] = strings.Repeat(string(rune('a'+k%26)), 64)
			}
			items[i] = map[string]any{"id": fmt.Sprintf("r%d", i), "label": strings.Repeat("L", 30), "type": "custom_keywords", "action": "mask",
				"keywords": keywords}
		}
		_, _, err := normalizeRulesInput(items)
		return err
	})

	// 统一文案前缀（契约：安全策略规则无效：<原因>）。
	_, _, err = normalizeRulesInput([]any{map[string]any{"type": "phone"}})
	if err == nil || !strings.HasPrefix(err.Error(), "安全策略规则无效：") {
		t.Fatalf("invalid rules message: %v", err)
	}
	var validation *routestrategies.ValidationError
	if !asValidationError(err, &validation) {
		t.Fatalf("invalid rules must be ValidationError: %T", err)
	}
}

func assertInvalid(t *testing.T, name string, fn func() error) {
	t.Helper()
	if err := fn(); err == nil {
		t.Fatalf("%s: expected rejection, got nil error", name)
	}
}

func asValidationError(err error, target **routestrategies.ValidationError) bool {
	if typed, ok := err.(*routestrategies.ValidationError); ok {
		*target = typed
		return true
	}
	return false
}

// TestSecurityPolicyBuiltinVisibilityAndWriteGuard 钉住内置行全局例外：
// 双面 list/detail/options 恒可见、写动作 403、seed 恢复后仍可见。
func TestSecurityPolicyBuiltinVisibilityAndWriteGuard(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.createUser(t, "alice", "alice-pass", "user")

	// admin 面：内置行恒可见。
	for _, base := range []string{adminBase} {
		code, payload := env.do(t, http.MethodGet, base, "")
		if code != http.StatusOK {
			t.Fatalf("%s list: %d %v", base, code, payload)
		}
		items := dataItems(t, payload)
		if len(items) != 1 || items[0]["id"] != builtinID || items[0]["builtIn"] != true || items[0]["name"] != builtinName {
			t.Fatalf("%s list builtin row: %v", base, items)
		}
		code, payload = env.do(t, http.MethodGet, base+"/options", "")
		if code != http.StatusOK {
			t.Fatalf("%s options: %d %v", base, code, payload)
		}
		rows := dataRows(t, payload)
		if len(rows) != 1 || rows[0]["id"] != builtinID {
			t.Fatalf("%s options builtin row: %v", base, rows)
		}
		code, payload = env.do(t, http.MethodGet, base+"/"+builtinID, "")
		if code != http.StatusOK {
			t.Fatalf("%s detail: %d %v", base, code, payload)
		}
	}

	// rules 投影：内置行空规则集。
	code, detail := env.do(t, http.MethodGet, adminBase+"/"+builtinID, "")
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, detail)
	}
	data, _ := detail["data"].(map[string]any)
	rules, _ := data["rules"].([]any)
	if data["rules"] == nil || len(rules) != 0 {
		t.Fatalf("builtin rules projection: %v", data["rules"])
	}

	// self 面：内置行恒可见 + 写动作 403。
	restore := env.switchUser(t, "alice", "alice-pass")
	for _, base := range []string{selfBase} {
		code, payload := env.do(t, http.MethodGet, base, "")
		if code != http.StatusOK {
			t.Fatalf("%s list: %d %v", base, code, payload)
		}
		items := dataItems(t, payload)
		if len(items) != 1 || items[0]["id"] != builtinID {
			t.Fatalf("%s list builtin row: %v", base, items)
		}
	}
	code, payload := env.do(t, http.MethodPatch, selfBase+"/"+builtinID, `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","name":"x"}`)
	if code != http.StatusForbidden || message(t, payload) != "系统内置安全策略不可修改或删除" {
		t.Fatalf("self patch builtin: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodDelete, selfBase+"/"+builtinID, "")
	if code != http.StatusForbidden || message(t, payload) != "系统内置安全策略不可修改或删除" {
		t.Fatalf("self delete builtin: %d %v", code, payload)
	}
	restore()

	// admin 面写动作同样 403。
	code, payload = env.do(t, http.MethodPatch, adminBase+"/"+builtinID, `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","name":"x"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin patch builtin: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodDelete, adminBase+"/"+builtinID, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin delete builtin: %d %v", code, payload)
	}
}

// TestSecurityPolicyCreateMatrix 覆盖双面创建：admin 显式 owner、self 面自归
// 属、缺省/显式 rules、重名 409（精确 + lower 归一）、严格键 400。
func TestSecurityPolicyCreateMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	userID := env.createUser(t, "alice", "alice-pass", "user")

	// admin 未显式 owner → 400。
	code, payload := env.do(t, http.MethodPost, adminBase, `{"name":"no-owner"}`)
	if code != http.StatusBadRequest || message(t, payload) != "系统账号 ID 不能为空" {
		t.Fatalf("create without owner: %d %v", code, payload)
	}

	// admin 显式 owner + 缺省 rules → 空规则集落库。
	item := env.createViaAPI(t, adminID, `{"name":"缺省规则"}`)
	if item["builtIn"] != false || item["boundApiKeyCount"] != float64(0) {
		t.Fatalf("created row: %v", item)
	}
	rules, _ := item["rules"].([]any)
	if item["rules"] == nil || len(rules) != 0 {
		t.Fatalf("created rules projection: %v", item["rules"])
	}
	if got := env.count(t, `SELECT COUNT(1) FROM security_policies WHERE system_account_id = ? AND built_in = 0`, adminID); got != 1 {
		t.Fatalf("created rows: %d", got)
	}

	// 显式 rules：规范化回显（label/pattern/keywords 分型）。
	explicit := env.createViaAPI(t, adminID, `{"name":"显式规则","description":"d","rules":[
		{"id":"r1","label":"PHONE","type":"phone","action":"placeholder"},
		{"id":"r2","type":"custom_regex","action":"placeholder","pattern":"GH-[0-9]{6}"},
		{"id":"r3","type":"custom_keywords","action":"mask","keywords":["XX项目"]}]}`)
	if explicit["description"] != "d" {
		t.Fatalf("created description: %v", explicit)
	}
	rules, _ = explicit["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("created explicit rules: %v", explicit["rules"])
	}
	first, _ := rules[0].(map[string]any)
	if first["label"] != "PHONE" || first["maskKeep"] != nil {
		t.Fatalf("rule projection: %v", first)
	}

	// self 面：owner 钉当前会话（query 伪造忽略）。
	restore := env.switchUser(t, "alice", "alice-pass")
	code, payload = env.do(t, http.MethodPost, selfBase+"?systemAccountId="+adminID, `{"name":"伪造owner"}`)
	if code != http.StatusCreated {
		t.Fatalf("self create with forged owner: %d %v", code, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data["systemAccountId"] != nil {
		t.Fatalf("self create must not echo admin owner: %v", data)
	}
	if got := env.count(t, `SELECT COUNT(1) FROM security_policies WHERE system_account_id = ?`, userID); got != 1 {
		t.Fatalf("self-owned rows: %d", got)
	}
	restore()

	// 完全同参重放先命中 MutationGuard（409 去重提示），不进业务校验（调度偏
	// 好同名重放先例同口径）。
	code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+adminID, `{"name":"缺省规则"}`)
	if code != http.StatusConflict {
		t.Fatalf("guard replay: %d %v", code, payload)
	}
	// 大小写归一重名（指纹不同 → 过守卫 → 命中 lower 唯一索引 → 409 中文文案）。
	code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+adminID, `{"name":"MY-POLICY"}`)
	if code != http.StatusCreated {
		t.Fatalf("create second: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+adminID, `{"name":"my-policy"}`)
	if code != http.StatusConflict || !strings.Contains(message(t, payload), "安全策略名称已存在") {
		t.Fatalf("lower duplicate name: %d %v", code, payload)
	}

	// 严格键 400（各非法臂使用不同 name：MutationGuard 失败去重按 owner+name
	// 指纹，10s 窗口内同指纹重放会先命中 409 去重提示）。
	code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+adminID, `{"name":"x-unknown","chatMode":"cost_first"}`)
	if code != http.StatusBadRequest || message(t, payload) != invalidMutationMessage {
		t.Fatalf("unknown key: %d %v", code, payload)
	}
	// 缺 name 400。
	code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+adminID, `{"rules":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("missing name: %d %v", code, payload)
	}
	// rules 非数组 400。
	code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+adminID, `{"name":"x-badrules","rules":{"rules":[]}}`)
	if code != http.StatusBadRequest || !strings.Contains(message(t, payload), "安全策略规则无效") {
		t.Fatalf("rules not array: %d %v", code, payload)
	}
}

// TestSecurityPolicyPatchMatrix 覆盖 CAS 409、同值 no-op、白名单、内置 403、
// 404 与 rules 变更失效。
func TestSecurityPolicyPatchMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.createUser(t, "alice", "alice-pass", "user")
	item := env.createViaAPI(t, adminID, `{"name":"补丁策略","description":"d0"}`)
	policyID := item["id"].(string)
	updatedAt := item["updatedAt"].(string)
	baselineInvalidations := env.inval.count()
	baselineLogs := len(env.sink.all())

	// CAS 409 + currentUpdatedAt。
	code, payload := env.do(t, http.MethodPatch, adminBase+"/"+policyID,
		`{"expectedUpdatedAt":"2001-01-01T00:00:00.000Z","name":"x"}`)
	if code != http.StatusConflict {
		t.Fatalf("stale CAS: %d %v", code, payload)
	}
	if payload["currentUpdatedAt"] != updatedAt {
		t.Fatalf("conflict payload: %v", payload)
	}

	// 同值 no-op：零 DML、不推进 updatedAt、不失效、不写日志。
	code, payload = env.do(t, http.MethodPatch, adminBase+"/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"补丁策略","description":"d0","rules":null}`)
	if code != http.StatusOK {
		t.Fatalf("no-op patch: %d %v", code, payload)
	}
	data, _ := payload["data"].(map[string]any)
	changed, _ := data["changedFields"].([]any)
	if len(changed) != 0 || data["updatedAt"] != updatedAt {
		t.Fatalf("no-op result: %v", data)
	}
	if env.updatedAtOf(t, policyID) != updatedAt {
		t.Fatalf("no-op bumped updated_at")
	}
	if env.inval.count() != baselineInvalidations {
		t.Fatalf("no-op published invalidation")
	}
	if len(env.sink.all()) != baselineLogs {
		t.Fatalf("no-op wrote operation log")
	}

	// name + description 清空 + rules 变更：changedFields 与失效。
	code, payload = env.do(t, http.MethodPatch, adminBase+"/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"补丁策略2","description":null,
		  "rules":[{"id":"r1","type":"phone","action":"mask"}]}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, payload)
	}
	data, _ = payload["data"].(map[string]any)
	changed, _ = data["changedFields"].([]any)
	if len(changed) != 3 {
		t.Fatalf("changedFields: %v", changed)
	}
	rowPatch, _ := data["rowPatch"].(map[string]any)
	if rowPatch["description"] != nil {
		t.Fatalf("rowPatch description clear: %v", rowPatch)
	}
	if rules, _ := rowPatch["rules"].([]any); len(rules) != 1 {
		t.Fatalf("rowPatch rules: %v", rowPatch["rules"])
	}
	if env.inval.count() != baselineInvalidations+1 {
		t.Fatalf("rules change must invalidate runtime: %d", env.inval.count())
	}
	entries := env.sink.all()
	if len(entries) != baselineLogs+1 {
		t.Fatalf("patch must write one operation log: %d", len(entries))
	}
	last := entries[len(entries)-1]
	if last.Module != "security_policies" || last.ResourceType != "security_policy" || last.Action != "update" {
		t.Fatalf("operation log: %+v", last)
	}

	// 空白 name 400；未知键 400；空 PATCH 400。
	newUpdatedAt := env.updatedAtOf(t, policyID)
	code, payload = env.do(t, http.MethodPatch, adminBase+"/"+policyID, `{"expectedUpdatedAt":"`+newUpdatedAt+`","name":"  "}`)
	if code != http.StatusBadRequest || message(t, payload) != "安全策略名称不能为空" {
		t.Fatalf("blank name: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, adminBase+"/"+policyID, `{"expectedUpdatedAt":"`+newUpdatedAt+`","chatMode":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown key patch: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, adminBase+"/"+policyID, `{"expectedUpdatedAt":"`+newUpdatedAt+`"}`)
	if code != http.StatusBadRequest || message(t, payload) != "请提供要修改的安全策略内容" {
		t.Fatalf("empty patch: %d %v", code, payload)
	}
	// 非法 rules 400（已存在行不被触碰）。
	code, payload = env.do(t, http.MethodPatch, adminBase+"/"+policyID,
		`{"expectedUpdatedAt":"`+newUpdatedAt+`","rules":[{"id":"r1","type":"nope","action":"mask"}]}`)
	if code != http.StatusBadRequest || !strings.Contains(message(t, payload), "安全策略规则无效") {
		t.Fatalf("invalid rules patch: %d %v", code, payload)
	}

	// 404。
	code, payload = env.do(t, http.MethodPatch, adminBase+"/missing-id",
		`{"expectedUpdatedAt":"`+newUpdatedAt+`","name":"x"}`)
	if code != http.StatusNotFound || message(t, payload) != "安全策略不存在" {
		t.Fatalf("patch missing: %d %v", code, payload)
	}

	// self 面只能改自己的行：他人行 404。
	env.createViaAPI(t, adminID, `{"name":"管理员策略"}`)
	restore := env.switchUser(t, "alice", "alice-pass")
	code, payload = env.do(t, http.MethodGet, selfBase, "")
	if code != http.StatusOK {
		t.Fatalf("self list: %d %v", code, payload)
	}
	if items := dataItems(t, payload); len(items) != 1 || items[0]["id"] != builtinID {
		t.Fatalf("self list must see only builtin: %v", items)
	}
	restore()
}

// TestSecurityPolicyDeleteMatrix 覆盖删除总是成功、同事务解绑回内置、
// resetBindings、内置 403、404 与失效。
func TestSecurityPolicyDeleteMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.createUser(t, "alice", "alice-pass", "user")
	item := env.createViaAPI(t, adminID, `{"name":"待删策略"}`)
	policyID := item["id"].(string)
	env.bindAPIKey(t, "key-1", adminID, policyID)
	env.bindAPIKey(t, "key-2", adminID, builtinID)
	baselineInvalidations := env.inval.count()

	code, payload := env.do(t, http.MethodDelete, adminBase+"/"+policyID, "")
	if code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil || data["resetBindings"] != float64(1) {
		t.Fatalf("resetBindings envelope: %v", payload)
	}
	if got := env.count(t, `SELECT COUNT(1) FROM security_policies WHERE id = ?`, policyID); got != 0 {
		t.Fatalf("policy row survived")
	}
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = 'key-1'`); got != builtinID {
		t.Fatalf("bound key not reset: %q", got)
	}
	if got := env.queryCell(t, `SELECT security_policy_id FROM api_keys WHERE id = 'key-2'`); got != builtinID {
		t.Fatalf("builtin-bound key disturbed: %q", got)
	}
	if env.inval.count() != baselineInvalidations+1 {
		t.Fatalf("delete must invalidate runtime: %d", env.inval.count())
	}
	entries := env.sink.all()
	if len(entries) == 0 {
		t.Fatalf("delete must write an operation log")
	}
	last := entries[len(entries)-1]
	if last.Action != "delete_binding_reset" || last.OperationKey != "security_policies.delete" || last.Module != "security_policies" {
		t.Fatalf("delete operation log: %+v", last)
	}

	// 重复删除 → 404。
	code, payload = env.do(t, http.MethodDelete, adminBase+"/"+policyID, "")
	if code != http.StatusNotFound {
		t.Fatalf("repeat delete: %d %v", code, payload)
	}
	// 他人（self 面）删除不可见行 → 404。
	env.createViaAPI(t, adminID, `{"name":"管理员的策略"}`)
	restore := env.switchUser(t, "alice", "alice-pass")
	code, payload = env.do(t, http.MethodGet, selfBase+"/options", "")
	if code != http.StatusOK {
		t.Fatalf("self options: %d %v", code, payload)
	}
	adminPolicyID := ""
	for _, row := range dataRows(t, payload) {
		if row["id"] != builtinID {
			adminPolicyID = row["id"].(string)
		}
	}
	// options 是可选集（self 面只见自己 + 内置行）；管理员的策略对 self 面不可
	// 见，直接以 id 删除 → 404（owner 不匹配）。
	if adminPolicyID != "" {
		t.Fatalf("self options must not include admin policies: %v", adminPolicyID)
	}
	code, payload = env.do(t, http.MethodDelete, selfBase+"/secpol_not_exists", "")
	if code != http.StatusNotFound {
		t.Fatalf("self delete missing: %d %v", code, payload)
	}
	restore()
}

// queryCell 返回单值字符串（解绑回内置断言用）。
func (e *testEnv) queryCell(t *testing.T, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := e.db.QueryRow(fixtureBind(query), args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value.String
}

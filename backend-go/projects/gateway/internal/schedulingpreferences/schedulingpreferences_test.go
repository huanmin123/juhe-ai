package schedulingpreferences

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

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

const builtinID = "schpref-builtin-chat-cost-first"
const builtinName = "默认调度偏好"

const (
	adminBase = "/__aisys__/api/scheduling-preferences"
	selfBase  = "/__aisys__/api/my-scheduling-preferences"
)

// seedBuiltinUpsert 与 maintenance 内置行种子同构的恢复式 upsert（固定 id 冲
// 突时仅在内置行上恢复权威值），供夹具初始化与「恢复 seed 后可见」用例复用。
// enabled 已随 v2 用户裁决修订第 4 条退役，语句与夹具 DDL 均不再携带该列。
const seedBuiltinUpsert = `
    INSERT INTO scheduling_preferences
      (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
    VALUES (?, NULL, ?, 'cost_first', NULL, 1, ?, ?)
    ON CONFLICT(id) DO UPDATE SET
      system_account_id=excluded.system_account_id,
      name=excluded.name,
      chat_mode=excluded.chat_mode,
      chat_speed_first_config_json=excluded.chat_speed_first_config_json,
      built_in=excluded.built_in
      WHERE scheduling_preferences.built_in=1
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
	db := newSchedulingPreferencesPGDatabase(t)
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

func (e *testEnv) scalarText(t *testing.T, query string, args ...any) sql.NullString {
	t.Helper()
	var value sql.NullString
	if err := e.db.QueryRow(fixtureBind(query), args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (e *testEnv) updatedAtOf(t *testing.T, id string) string {
	t.Helper()
	var updatedAt string
	if err := e.db.QueryRow(fixtureBind(`SELECT updated_at FROM scheduling_preferences WHERE id = ?`), id).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	return updatedAt
}

// bindAPIKey 直接插入一条绑定给定偏好的 API Key（含停用形态）。
func (e *testEnv) bindAPIKey(t *testing.T, id, ownerID, preferenceID, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, status, scheduling_preference_id, created_at, updated_at)
		VALUES (?, ?, 'rs-x', ?, ?, ?, ?, ?)`, id, ownerID, "key-"+id, status, preferenceID, now, now)
}

// createViaAPI 走管理面创建一条自定义偏好并返回 201 行（默认 admin 会话）。
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

// TestSchedulingPreferenceBuiltinVisibilityAndWriteGuard 钉住内置行全局例外：
// 双面 list/detail/options 恒可见、写动作 403、seed 恢复后仍可见。
func TestSchedulingPreferenceBuiltinVisibilityAndWriteGuard(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "alice", "alice-password", "user")
	env.login(t, "root", "root-password", "admin")

	t.Run("builtin_in_lists_and_details", func(t *testing.T) {
		for _, path := range []string{adminBase, selfBase} {
			code, payload := env.do(t, http.MethodGet, path, "")
			if code != http.StatusOK {
				t.Fatalf("GET %s: %d %v", path, code, payload)
			}
			var builtin map[string]any
			for _, item := range dataItems(t, payload) {
				if item["id"] == builtinID {
					builtin = item
				}
			}
			if builtin == nil {
				t.Fatalf("builtin row missing in %s: %v", path, dataItems(t, payload))
			}
			if builtin["name"] != builtinName || builtin["chatMode"] != "cost_first" ||
				builtin["builtIn"] != true {
				t.Fatalf("builtin projection wrong: %v", builtin)
			}
			if _, hasEnabled := builtin["enabled"]; hasEnabled {
				t.Fatalf("builtin row must not carry the retired enabled flag: %v", builtin)
			}
			if _, hasOwner := builtin["systemAccountId"]; hasOwner {
				t.Fatalf("builtin row must not carry an owner summary: %v", builtin)
			}
		}
		for _, path := range []string{adminBase + "/" + builtinID, selfBase + "/" + builtinID} {
			code, payload := env.do(t, http.MethodGet, path, "")
			if code != http.StatusOK {
				t.Fatalf("GET %s: %d %v", path, code, payload)
			}
			data, _ := payload["data"].(map[string]any)
			if data["id"] != builtinID {
				t.Fatalf("detail %s: %v", path, data)
			}
		}
	})
	t.Run("builtin_in_options_both_surfaces", func(t *testing.T) {
		for _, path := range []string{adminBase + "/options", selfBase + "/options"} {
			code, payload := env.do(t, http.MethodGet, path, "")
			if code != http.StatusOK {
				t.Fatalf("GET %s: %d %v", path, code, payload)
			}
			found := false
			for _, item := range dataRows(t, payload) {
				if item["id"] == builtinID {
					found = true
				}
			}
			if !found {
				t.Fatalf("options %s missing builtin: %v", path, payload)
			}
		}
	})
	t.Run("builtin_write_guards_403", func(t *testing.T) {
		cases := []struct {
			name   string
			method string
			path   string
			body   string
		}{
			{"admin_patch", http.MethodPatch, adminBase + "/" + builtinID,
				`{"name":"改名","expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`},
			{"self_patch", http.MethodPatch, selfBase + "/" + builtinID,
				`{"name":"改名","expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`},
			{"admin_delete", http.MethodDelete, adminBase + "/" + builtinID, ""},
			{"self_delete", http.MethodDelete, selfBase + "/" + builtinID, ""},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				code, payload := env.do(t, testCase.method, testCase.path, testCase.body)
				if code != http.StatusForbidden {
					t.Fatalf("%s: %d %v", testCase.name, code, payload)
				}
				if got := message(t, payload); got != "系统内置调度偏好不可修改或删除" {
					t.Fatalf("message = %q", got)
				}
			})
		}
		if env.count(t, `SELECT COUNT(1) FROM scheduling_preferences`) != 1 {
			t.Fatalf("builtin guard must leave the table untouched")
		}
		if len(env.sink.all()) != 0 {
			t.Fatalf("builtin 403 must not write operation logs: %+v", env.sink.all())
		}
		if env.inval.count() != 0 {
			t.Fatalf("builtin 403 must not publish invalidation: %v", env.inval.reasonsSnapshot())
		}
	})
	t.Run("builtin_restored_by_seed_visible", func(t *testing.T) {
		// 外部篡改内置行名称（built_in 仍 1）→ 恢复式 upsert 拉回权威值 → 可见。
		env.exec(t, `UPDATE scheduling_preferences SET name = '被篡改' WHERE id = ?`, builtinID)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := env.db.Exec(fixtureBind(seedBuiltinUpsert), builtinID, builtinName, now, now); err != nil {
			t.Fatal(err)
		}
		code, payload := env.do(t, http.MethodGet, selfBase+"/"+builtinID, "")
		if code != http.StatusOK {
			t.Fatalf("GET builtin after restore: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		if data["name"] != builtinName {
			t.Fatalf("builtin name after restore = %v", data["name"])
		}
	})
}

// TestSchedulingPreferenceCreateMatrix 覆盖创建契约：owner 规则、校验文案、
// 严格 body、speed config 九字段边界与名称唯一性。
func TestSchedulingPreferenceCreateMatrix(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "alice", "alice-password", "user")
	env.login(t, "root", "root-password", "admin")

	t.Run("owner_rules", func(t *testing.T) {
		cases := []struct {
			name    string
			query   string
			body    string
			status  int
			message string
		}{
			{"admin_missing_owner", "", `{"name":"ow-missing","chatMode":"cost_first"}`, 400, "系统账号 ID 不能为空"},
			{"admin_blank_owner", "?systemAccountId=", `{"name":"ow-blank","chatMode":"cost_first"}`, 400, "系统账号 ID 不能为空"},
			{"admin_all_not_supported", "?systemAccountId=all", `{"name":"ow-all","chatMode":"cost_first"}`, 400, "系统账号 ID 不能为空"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				code, payload := env.do(t, http.MethodPost, adminBase+testCase.query, testCase.body)
				if code != testCase.status {
					t.Fatalf("%s: %d %v", testCase.name, code, payload)
				}
				if testCase.message != "" && message(t, payload) != testCase.message {
					t.Fatalf("%s message = %q", testCase.name, message(t, payload))
				}
			})
		}
		// self 面 owner=会话，伪造 systemAccountId 被忽略（admin 会话下 self 面
		// 亦钉死会话人）。
		restore := env.switchUser(t, "alice", "alice-password")
		defer restore()
		code, payload := env.do(t, http.MethodPost, selfBase+"?systemAccountId=root", `{"name":"self-owned","chatMode":"cost_first"}`)
		if code != http.StatusCreated {
			t.Fatalf("self create: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		if _, hasOwner := data["systemAccountId"]; hasOwner {
			t.Fatalf("self created row must not carry owner fields: %v", data)
		}
		var dbOwner string
		if err := env.db.QueryRow(fixtureBind(`SELECT system_account_id FROM scheduling_preferences WHERE id = ?`), data["id"]).Scan(&dbOwner); err != nil {
			t.Fatal(err)
		}
		if dbOwner != ownerID {
			t.Fatalf("self create owner = %q want alice %q", dbOwner, ownerID)
		}
	})

	t.Run("validation_messages", func(t *testing.T) {
		// 名称逐用例唯一：POST 创建挂 MutationGuard 防重复提交（指纹=owner+名
		// 称），同名重放会先命中 409 去重提示而非参数校验。
		cases := []struct {
			name    string
			query   string
			body    string
			message string
		}{
			// 缺名/空名经 trim 后指纹相同（防重复提交按 owner+名称聚合），用不同
			// owner 查询串区分指纹；两用例均在 owner 校验前 400，owner 存在性无涉。
			{"missing_name", "?systemAccountId=fingerprint-a", `{"chatMode":"cost_first"}`, "调度偏好名称不能为空"},
			{"blank_name", "?systemAccountId=fingerprint-b", `{"name":"   ","chatMode":"cost_first"}`, "调度偏好名称不能为空"},
			{"missing_chat_mode", "?systemAccountId=" + ownerID, `{"name":"v-missing-mode"}`, "调度偏好对话形态无效"},
			{"invalid_chat_mode", "?systemAccountId=" + ownerID, `{"name":"v-bad-mode","chatMode":"balanced"}`, "调度偏好对话形态无效"},
			{"cost_first_with_config", "?systemAccountId=" + ownerID, `{"name":"v-cost-config","chatMode":"cost_first","chatSpeedFirstConfig":{"firstByteDeadlineMs":30000}}`, "成本优先不接受速度参数配置"},
			// speed_first 缺省/显式 null 配置不再是 400：按 D2 缺省表补齐，
			// 断言见 create_speed_first_default_config 子测试。
			{"unknown_top_key", "?systemAccountId=" + ownerID, `{"name":"v-unknown-top","chatMode":"cost_first","enabled":true}`, "调度偏好参数无效"},
			{"enabled_retired_rejected", "?systemAccountId=" + ownerID, `{"name":"v-enabled-retired","chatMode":"cost_first","enabled":false}`, "调度偏好参数无效"},
			{"unknown_speed_key", "?systemAccountId=" + ownerID, `{"name":"v-unknown-speed","chatMode":"speed_first","chatSpeedFirstConfig":{"mystery":1}}`, "速度优先配置无效"},
			{"speed_inner_unknown_key", "?systemAccountId=" + ownerID, `{"name":"v-unknown-inner","chatMode":"speed_first","chatSpeedFirstConfig":{"speedFirstConfig":{"mystery":1}}}`, "速度优先配置无效"},
			{"dual_deadline_rejected", "?systemAccountId=" + ownerID, `{"name":"v-dual-deadline","chatMode":"speed_first","chatSpeedFirstConfig":{"firstByteDeadlineMs":30000,"speedFirstConfig":{"firstByteThresholdMs":30000}}}`, "首字截止时间不能同时配置 firstByteDeadlineMs 和旧 firstByteThresholdMs"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				code, payload := env.do(t, http.MethodPost, adminBase+testCase.query, testCase.body)
				if code != http.StatusBadRequest {
					t.Fatalf("%s: %d %v", testCase.name, code, payload)
				}
				if got := message(t, payload); got != testCase.message {
					t.Fatalf("%s message = %q want %q", testCase.name, got, testCase.message)
				}
			})
		}
	})

	t.Run("create_row_projection", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID,
			`{"name":"proj-speed","chatMode":"speed_first","chatSpeedFirstConfig":{}}`)
		if code != http.StatusCreated {
			t.Fatalf("create: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		if data["builtIn"] != false {
			t.Fatalf("created row builtIn: %v", data)
		}
		if _, hasEnabled := data["enabled"]; hasEnabled {
			t.Fatalf("created row must not carry the retired enabled flag: %v", data)
		}
		if fmt.Sprint(data["boundApiKeyCount"]) != "0" {
			t.Fatalf("created boundApiKeyCount = %v", data["boundApiKeyCount"])
		}
		if data["systemAccountId"] != ownerID {
			t.Fatalf("admin created row must carry owner: %v", data)
		}
		// 缺省补齐的规范化文档（D2 默认）。
		config, _ := data["chatSpeedFirstConfig"].(map[string]any)
		if fmt.Sprint(config["firstByteDeadlineMs"]) != "30000" {
			t.Fatalf("normalized deadline = %v", config)
		}
		inner, _ := config["speedFirstConfig"].(map[string]any)
		if fmt.Sprint(inner["slowTriggerCount"]) != "3" || fmt.Sprint(inner["compactionTotalTimeDeadlineSeconds"]) != "300" {
			t.Fatalf("normalized speedFirstConfig = %v", inner)
		}
	})

	t.Run("create_speed_first_default_config", func(t *testing.T) {
		// 裁决 N2：创建 speed_first 未携带 chatSpeedFirstConfig（缺省或显式
		// null）按 D2 缺省表补齐，与 PATCH 切入路径一致；九字段逐一断言。
		defaults := map[string]any{
			"firstByteDeadlineMs":                "30000",
			"slowTriggerCount":                   "3",
			"slowWindowSeconds":                  "120",
			"recoverySuccessCount":               "3",
			"probeIntervalSeconds":               "30",
			"degradedTtlSeconds":                 "300",
			"maxFirstByteRetriesPerRequest":      "2",
			"totalTimeDeadlineSeconds":           "120",
			"compactionTotalTimeDeadlineSeconds": "300",
		}
		assertD2Defaults := func(t *testing.T, config any) {
			t.Helper()
			outer, ok := config.(map[string]any)
			if !ok {
				t.Fatalf("chatSpeedFirstConfig must be an object, got %v", config)
			}
			if got := fmt.Sprint(outer["firstByteDeadlineMs"]); got != defaults["firstByteDeadlineMs"] {
				t.Fatalf("firstByteDeadlineMs = %v want %v", got, defaults["firstByteDeadlineMs"])
			}
			inner, ok := outer["speedFirstConfig"].(map[string]any)
			if !ok {
				t.Fatalf("speedFirstConfig must be an object, got %v", outer["speedFirstConfig"])
			}
			for _, field := range []string{
				"slowTriggerCount", "slowWindowSeconds", "recoverySuccessCount", "probeIntervalSeconds",
				"degradedTtlSeconds", "maxFirstByteRetriesPerRequest", "totalTimeDeadlineSeconds",
				"compactionTotalTimeDeadlineSeconds",
			} {
				if got := fmt.Sprint(inner[field]); got != defaults[field] {
					t.Fatalf("speedFirstConfig.%s = %v want %v", field, got, defaults[field])
				}
			}
		}
		// 缺省配置 → 201 + D2 默认九字段。
		code, payload := env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID,
			`{"name":"proj-speed-absent","chatMode":"speed_first"}`)
		if code != http.StatusCreated {
			t.Fatalf("create absent config: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		if data["chatMode"] != "speed_first" {
			t.Fatalf("created chatMode = %v", data["chatMode"])
		}
		assertD2Defaults(t, data["chatSpeedFirstConfig"])

		// 显式 null 配置 → 同一缺省补齐路径。
		code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID,
			`{"name":"proj-speed-null","chatMode":"speed_first","chatSpeedFirstConfig":null}`)
		if code != http.StatusCreated {
			t.Fatalf("create null config: %d %v", code, payload)
		}
		data, _ = payload["data"].(map[string]any)
		assertD2Defaults(t, data["chatSpeedFirstConfig"])

		// 落库 JSON 与投影一致（存储非 NULL 配置文档）。
		var stored string
		if err := env.db.QueryRow(`SELECT chat_speed_first_config_json FROM scheduling_preferences WHERE name = 'proj-speed-null'`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stored, `"firstByteDeadlineMs":30000`) {
			t.Fatalf("stored config json = %s", stored)
		}
	})

	t.Run("speed_config_boundaries", func(t *testing.T) {
		// 九字段：缺省全默认（create_row_projection 已断言）；显式合法值保留；
		// 越界逐字段拒绝（与策略路由同文案，默认值单一来源）。名称逐用例唯一，
		// 避免撞 owner 内唯一索引。
		outerCases := []struct {
			field   string
			value   int
			legal   bool
			message string
		}{
			{"firstByteDeadlineMs", 25000, true, ""},
			{"firstByteDeadlineMs", 9000, false, "首字截止时间必须是 10000-60000 毫秒"},
			{"firstByteDeadlineMs", 60001, false, "首字截止时间必须是 10000-60000 毫秒"},
		}
		innerCases := []struct {
			field   string
			value   int
			legal   bool
			message string
		}{
			{"slowTriggerCount", 5, true, ""},
			{"slowTriggerCount", 1, false, "速度优先触发次数必须是 2-10"},
			{"slowTriggerCount", 11, false, "速度优先触发次数必须是 2-10"},
			{"slowWindowSeconds", 200, true, ""},
			{"slowWindowSeconds", 59, false, "速度优先窗口期必须是 60-600 秒"},
			{"slowWindowSeconds", 601, false, "速度优先窗口期必须是 60-600 秒"},
			{"recoverySuccessCount", 7, true, ""},
			{"recoverySuccessCount", 2, false, "速度优先恢复次数必须是 3-10"},
			{"recoverySuccessCount", 11, false, "速度优先恢复次数必须是 3-10"},
			{"probeIntervalSeconds", 45, true, ""},
			{"probeIntervalSeconds", 9, false, "速度优先探针间隔必须是 10-300 秒"},
			{"probeIntervalSeconds", 301, false, "速度优先探针间隔必须是 10-300 秒"},
			{"degradedTtlSeconds", 600, true, ""},
			{"degradedTtlSeconds", 59, false, "速度优先降级保留时间必须是 60-3600 秒"},
			{"degradedTtlSeconds", 3601, false, "速度优先降级保留时间必须是 60-3600 秒"},
			{"maxFirstByteRetriesPerRequest", 2, true, ""},
			{"maxFirstByteRetriesPerRequest", 0, false, "速度优先单请求切号次数必须是 1-3"},
			{"maxFirstByteRetriesPerRequest", 4, false, "速度优先单请求切号次数必须是 1-3"},
			{"totalTimeDeadlineSeconds", 200, true, ""},
			{"totalTimeDeadlineSeconds", 59, false, "请求总时间截止必须是 60-270 秒"},
			{"totalTimeDeadlineSeconds", 271, false, "请求总时间截止必须是 60-270 秒"},
			{"compactionTotalTimeDeadlineSeconds", 600, true, ""},
			{"compactionTotalTimeDeadlineSeconds", 299, false, "压缩总时间截止必须是 300-900 秒"},
			{"compactionTotalTimeDeadlineSeconds", 901, false, "压缩总时间截止必须是 300-900 秒"},
		}
		for _, testCase := range append(outerCases, innerCases...) {
			t.Run(testCase.field+"_"+itoa(testCase.value), func(t *testing.T) {
				config := `{"speedFirstConfig":{"` + testCase.field + `":` + itoa(testCase.value) + `}}`
				if testCase.field == "firstByteDeadlineMs" {
					config = `{"firstByteDeadlineMs":` + itoa(testCase.value) + `}`
				}
				body := `{"name":"bounds-` + testCase.field + `-` + itoa(testCase.value) + `","chatMode":"speed_first","chatSpeedFirstConfig":` + config + `}`
				code, payload := env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID, body)
				if testCase.legal && code != http.StatusCreated {
					t.Fatalf("legal value rejected: %d %v", code, payload)
				}
				if !testCase.legal {
					if code != http.StatusBadRequest || message(t, payload) != testCase.message {
						t.Fatalf("out-of-range: %d %v (want %s)", code, payload, testCase.message)
					}
				}
			})
		}
		t.Run("legal_values_kept", func(t *testing.T) {
			body := `{"name":"bounds-kept","chatMode":"speed_first","chatSpeedFirstConfig":{"firstByteDeadlineMs":45000,"speedFirstConfig":{"slowTriggerCount":7,"slowWindowSeconds":200,"recoverySuccessCount":7,"probeIntervalSeconds":45,"degradedTtlSeconds":600,"maxFirstByteRetriesPerRequest":3,"totalTimeDeadlineSeconds":200,"compactionTotalTimeDeadlineSeconds":600}}}`
			code, payload := env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID, body)
			if code != http.StatusCreated {
				t.Fatalf("legal values: %d %v", code, payload)
			}
			data, _ := payload["data"].(map[string]any)
			config, _ := data["chatSpeedFirstConfig"].(map[string]any)
			inner, _ := config["speedFirstConfig"].(map[string]any)
			for key, want := range map[string]int{
				"slowTriggerCount": 7, "slowWindowSeconds": 200, "recoverySuccessCount": 7,
				"probeIntervalSeconds": 45, "degradedTtlSeconds": 600,
				"maxFirstByteRetriesPerRequest": 3, "totalTimeDeadlineSeconds": 200,
				"compactionTotalTimeDeadlineSeconds": 600,
			} {
				if fmt.Sprint(inner[key]) != fmt.Sprint(want) {
					t.Fatalf("speedFirstConfig.%s = %v want %d", key, inner[key], want)
				}
			}
			if fmt.Sprint(config["firstByteDeadlineMs"]) != "45000" {
				t.Fatalf("firstByteDeadlineMs = %v want 45000", config["firstByteDeadlineMs"])
			}
		})
		t.Run("legacy_alias_stored_canonical", func(t *testing.T) {
			code, payload := env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID,
				`{"name":"bounds-alias","chatMode":"speed_first","chatSpeedFirstConfig":{"speedFirstConfig":{"firstByteThresholdMs":20000}}}`)
			if code != http.StatusCreated {
				t.Fatalf("create: %d %v", code, payload)
			}
			data, _ := payload["data"].(map[string]any)
			stored := env.scalarText(t, `SELECT chat_speed_first_config_json FROM scheduling_preferences WHERE id = ?`, data["id"])
			if !stored.Valid || !strings.Contains(stored.String, `"firstByteDeadlineMs":20000`) || strings.Contains(stored.String, "firstByteThresholdMs") {
				t.Fatalf("stored JSON must be canonical: %v", stored)
			}
		})
	})

	t.Run("name_uniqueness_within_owner", func(t *testing.T) {
		body := `{"name":"P1","chatMode":"cost_first"}`
		env.createViaAPI(t, ownerID, body)
		// 完全同参重放先命中 MutationGuard（409 去重提示），不进业务校验。
		code, payload := env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID, body)
		if code != http.StatusConflict {
			t.Fatalf("guard replay: %d %v", code, payload)
		}
		// 大小写归一重名（指纹不同 → 过守卫 → 命中 lower 唯一索引 → 409 中文文案）。
		code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID, `{"name":"p1","chatMode":"cost_first"}`)
		if code != http.StatusConflict || message(t, payload) != "调度偏好名称已存在：p1" {
			t.Fatalf("duplicate lower-normalized: %d %v", code, payload)
		}
		// 不同 owner 同名合法。
		code, _ = env.do(t, http.MethodPost, adminBase+"?systemAccountId=root", body)
		if code != http.StatusCreated {
			t.Fatalf("same name different owner must pass: %d", code)
		}
		// 内置行名称不受 owner 唯一索引约束（部分索引 WHERE built_in=0）。
		code, payload = env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID,
			`{"name":"`+builtinName+`","chatMode":"cost_first"}`)
		if code != http.StatusCreated {
			t.Fatalf("builtin-name reuse must pass: %d %v", code, payload)
		}
	})

	t.Run("create_invalidation_and_log", func(t *testing.T) {
		reasonsBefore := len(env.inval.reasonsSnapshot())
		logsBefore := len(env.sink.all())
		code, payload := env.do(t, http.MethodPost, adminBase+"?systemAccountId="+ownerID,
			`{"name":"logged-create","chatMode":"speed_first","chatSpeedFirstConfig":{"firstByteDeadlineMs":33000}}`)
		if code != http.StatusCreated {
			t.Fatalf("create: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		reasons := env.inval.reasonsSnapshot()
		if len(reasons) != reasonsBefore+1 || reasons[len(reasons)-1] != "scheduling_preference_created" {
			t.Fatalf("invalidation reasons = %v", reasons)
		}
		logs := env.sink.all()
		if len(logs) != logsBefore+1 {
			t.Fatalf("operation logs = %d want +%d", len(logs), 1)
		}
		entry := logs[len(logs)-1]
		if entry.Module != "scheduling_preferences" || entry.Action != "create" ||
			entry.OperationKey != "scheduling_preferences.create" || entry.ResourceType != "scheduling_preference" {
			t.Fatalf("log identity: %+v", entry)
		}
		if entry.Summary != "创建调度偏好：logged-create" {
			t.Fatalf("log summary = %q", entry.Summary)
		}
		if entry.OperationScopeSystemAccountID != ownerID || entry.ResourceID != data["id"] {
			t.Fatalf("log scope/resource: %+v", entry)
		}
		if len(entry.Viewers) != 1 || entry.Viewers[0].SystemAccountID != ownerID || entry.Viewers[0].Reason != "resource_owner" {
			t.Fatalf("log viewers: %+v", entry.Viewers)
		}
	})
}

// TestSchedulingPreferenceListScopeFiltersAndCounts 覆盖列表作用域（内置行
// 全局例外）、过滤、分页上界、绑定计数与 404 文案。
func TestSchedulingPreferenceListScopeFiltersAndCounts(t *testing.T) {
	env := newTestEnv(t)
	ownerA := env.login(t, "alice", "alice-password", "user")
	ownerB := env.login(t, "bob", "bob-password", "user")
	env.login(t, "root", "root-password", "admin")

	speedRow := env.createViaAPI(t, ownerA, `{"name":"A-speed","chatMode":"speed_first","chatSpeedFirstConfig":{}}`)
	costRow := env.createViaAPI(t, ownerA, `{"name":"A-cost","chatMode":"cost_first"}`)
	otherRow := env.createViaAPI(t, ownerB, `{"name":"B-cost","chatMode":"cost_first"}`)
	env.bindAPIKey(t, "key-1", ownerA, speedRow["id"].(string), "active")
	env.bindAPIKey(t, "key-2", ownerA, speedRow["id"].(string), "disabled")

	t.Run("admin_global_view", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, adminBase, "")
		if code != http.StatusOK {
			t.Fatalf("admin list: %d %v", code, payload)
		}
		items := dataItems(t, payload)
		// 全局视图 = 全部 owner 自定义行 + 内置行。
		if len(items) != 4 {
			t.Fatalf("admin global items = %d: %v", len(items), items)
		}
		for _, item := range items {
			if item["id"] == builtinID {
				continue
			}
			if _, has := item["systemAccountId"]; !has {
				t.Fatalf("admin rows carry owner summaries: %v", item)
			}
		}
	})
	t.Run("admin_narrowed_view_keeps_builtin", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, adminBase+"?systemAccountId="+ownerA, "")
		if code != http.StatusOK {
			t.Fatalf("admin narrowed: %d %v", code, payload)
		}
		items := dataItems(t, payload)
		if len(items) != 3 {
			t.Fatalf("narrowed items = %d: %v", len(items), items)
		}
		for _, item := range items {
			switch item["id"] {
			case builtinID, speedRow["id"], costRow["id"]:
			default:
				t.Fatalf("unexpected row leaked into narrowed view: %v", item)
			}
		}
	})
	t.Run("self_pinned_with_builtin", func(t *testing.T) {
		restore := env.switchUser(t, "alice", "alice-password")
		defer restore()
		code, payload := env.do(t, http.MethodGet, selfBase, "")
		if code != http.StatusOK {
			t.Fatalf("self list: %d %v", code, payload)
		}
		items := dataItems(t, payload)
		if len(items) != 3 {
			t.Fatalf("self items = %d (own 2 + builtin): %v", len(items), items)
		}
		for _, item := range items {
			if _, has := item["systemAccountId"]; has {
				t.Fatalf("self rows must not carry owner fields: %v", item)
			}
			if item["id"] == otherRow["id"] {
				t.Fatalf("other owner row leaked into self list: %v", items)
			}
		}
		// 个人面 detail：他人行 404、内置行可见。
		code, payload = env.do(t, http.MethodGet, selfBase+"/"+otherRow["id"].(string), "")
		if code != http.StatusNotFound || message(t, payload) != "调度偏好不存在" {
			t.Fatalf("self other-owner detail: %d %v", code, payload)
		}
	})
	t.Run("filters", func(t *testing.T) {
		cases := []struct {
			name    string
			query   string
			wantIDs []string
			wantLen int
		}{
			{"chat_mode", "?chatMode=speed_first", []string{speedRow["id"].(string)}, 1},
			// status 启停过滤已随 v2 第 4 条退役：查询参数按未知参数忽略。
			{"status_filter_retired", "?status=disabled", nil, 4},
			{"keyword_prefix", "?keyword=A-", []string{speedRow["id"].(string), costRow["id"].(string)}, 2},
			{"keyword_literal_percent", "?keyword=%25", nil, 0},
			{"invalid_filters_ignored", "?chatMode=balanced&status=weird", nil, 4},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				code, payload := env.do(t, http.MethodGet, adminBase+testCase.query, "")
				if code != http.StatusOK {
					t.Fatalf("%s: %d %v", testCase.name, code, payload)
				}
				items := dataItems(t, payload)
				if len(items) != testCase.wantLen {
					t.Fatalf("%s items = %d want %d: %v", testCase.name, len(items), testCase.wantLen, items)
				}
				ids := map[string]bool{}
				for _, item := range items {
					ids[item["id"].(string)] = true
				}
				for _, want := range testCase.wantIDs {
					if !ids[want] {
						t.Fatalf("%s missing %s: %v", testCase.name, want, items)
					}
				}
			})
		}
	})
	t.Run("bound_api_key_count_list_and_detail", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, adminBase, "")
		if code != http.StatusOK {
			t.Fatalf("admin list: %d %v", code, payload)
		}
		for _, item := range dataItems(t, payload) {
			switch item["id"] {
			case speedRow["id"]:
				// 未删除且绑定该偏好的 Key 精确计数（含停用 Key）。
				if fmt.Sprint(item["boundApiKeyCount"]) != "2" {
					t.Fatalf("boundApiKeyCount = %v want 2", item["boundApiKeyCount"])
				}
			case builtinID:
				if fmt.Sprint(item["boundApiKeyCount"]) != "0" {
					t.Fatalf("builtin boundApiKeyCount = %v want 0", item["boundApiKeyCount"])
				}
			}
		}
		code, payload = env.do(t, http.MethodGet, adminBase+"/"+speedRow["id"].(string), "")
		if code != http.StatusOK {
			t.Fatalf("detail: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		if fmt.Sprint(data["boundApiKeyCount"]) != "2" {
			t.Fatalf("detail boundApiKeyCount = %v want 2", data["boundApiKeyCount"])
		}
	})
	t.Run("pagination_progressive_total", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, adminBase+"?page=1&pageSize=3", "")
		if code != http.StatusOK {
			t.Fatalf("page 1: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		items, _ := data["items"].([]any)
		if len(items) != 3 || data["hasMore"] != true {
			t.Fatalf("page 1 shape: %v", data)
		}
		// total 是渐进上界：(page-1)*pageSize + items + 1。
		if fmt.Sprint(data["total"]) != "4" {
			t.Fatalf("page 1 total = %v want 4", data["total"])
		}
		code, payload = env.do(t, http.MethodGet, adminBase+"?page=2&pageSize=3", "")
		if code != http.StatusOK {
			t.Fatalf("page 2: %d %v", code, payload)
		}
		data, _ = payload["data"].(map[string]any)
		items, _ = data["items"].([]any)
		if len(items) != 1 || data["hasMore"] != false {
			t.Fatalf("page 2 shape: %v", data)
		}
		// pageSize 缺省 50、范围 1..200（显式 0 归一为 1）。
		code, payload = env.do(t, http.MethodGet, adminBase+"?pageSize=0", "")
		if code != http.StatusOK {
			t.Fatalf("pageSize=0: %d %v", code, payload)
		}
		data, _ = payload["data"].(map[string]any)
		if fmt.Sprint(data["pageSize"]) != "1" {
			t.Fatalf("pageSize=0 normalized = %v", data["pageSize"])
		}
		code, _ = env.do(t, http.MethodGet, adminBase+"?pageSize=500", "")
		_ = code
	})
	t.Run("detail_404_message", func(t *testing.T) {
		for _, path := range []string{adminBase + "/schpref-missing", selfBase + "/schpref-missing"} {
			code, payload := env.do(t, http.MethodGet, path, "")
			if code != http.StatusNotFound || message(t, payload) != "调度偏好不存在" {
				t.Fatalf("%s: %d %v", path, code, payload)
			}
		}
	})
}

// TestSchedulingPreferenceOptionsQuery 覆盖 options：ids 三种序列化形态、
// limit/activeOnly/keyword、双面投影差异。
func TestSchedulingPreferenceOptionsQuery(t *testing.T) {
	env := newTestEnv(t)
	ownerA := env.login(t, "alice", "alice-password", "user")
	ownerB := env.login(t, "bob", "bob-password", "user")
	env.login(t, "root", "root-password", "admin")

	rowA := env.createViaAPI(t, ownerA, `{"name":"A-pref","chatMode":"cost_first"}`)
	rowB := env.createViaAPI(t, ownerB, `{"name":"B-pref","chatMode":"cost_first"}`)

	t.Run("ids_serialization_forms", func(t *testing.T) {
		cases := []struct {
			name  string
			query string
			want  map[string]bool
		}{
			{"comma_form", "?ids=" + rowA["id"].(string) + "," + rowB["id"].(string),
				map[string]bool{rowA["id"].(string): true, rowB["id"].(string): true}},
			{"repeated_form", "?ids=" + rowA["id"].(string) + "&ids=" + rowB["id"].(string),
				map[string]bool{rowA["id"].(string): true, rowB["id"].(string): true}},
			{"bracket_form_axios_default", "?ids[]=" + rowA["id"].(string) + "&ids[]=" + rowB["id"].(string),
				map[string]bool{rowA["id"].(string): true, rowB["id"].(string): true}},
			{"bracket_form_dedup_and_trim", "?ids[]=" + rowA["id"].(string) + "&ids[]=%20" + rowA["id"].(string) + "%20",
				map[string]bool{rowA["id"].(string): true}},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				code, payload := env.do(t, http.MethodGet, adminBase+"/options"+testCase.query, "")
				if code != http.StatusOK {
					t.Fatalf("%s: %d %v", testCase.name, code, payload)
				}
				got := map[string]bool{}
				for _, item := range dataRows(t, payload) {
					got[item["id"].(string)] = true
				}
				if len(got) != len(testCase.want) {
					t.Fatalf("%s got %v want %v", testCase.name, got, testCase.want)
				}
				for id := range testCase.want {
					if !got[id] {
						t.Fatalf("%s missing %s: %v", testCase.name, id, got)
					}
				}
			})
		}
	})
	t.Run("builtin_visible_without_retired_active_only", func(t *testing.T) {
		// activeOnly 启停过滤已随 v2 第 4 条退役：查询参数按未知参数忽略，
		// 全部行（含内置行）恒可选。
		code, payload := env.do(t, http.MethodGet, adminBase+"/options", "")
		if code != http.StatusOK {
			t.Fatalf("options: %d %v", code, payload)
		}
		ids := map[string]bool{}
		for _, item := range dataRows(t, payload) {
			ids[item["id"].(string)] = true
		}
		if !ids[builtinID] || !ids[rowA["id"].(string)] || !ids[rowB["id"].(string)] {
			t.Fatalf("options must keep builtin + custom rows: %v", ids)
		}
		code, payload = env.do(t, http.MethodGet, adminBase+"/options?activeOnly=false", "")
		if code != http.StatusOK {
			t.Fatalf("activeOnly=false: %d %v", code, payload)
		}
		ids = map[string]bool{}
		for _, item := range dataRows(t, payload) {
			ids[item["id"].(string)] = true
		}
		if !ids[builtinID] || !ids[rowA["id"].(string)] || !ids[rowB["id"].(string)] {
			t.Fatalf("retired activeOnly param must be ignored: %v", ids)
		}
		// 非法值同样忽略（全部行可选）。
		code, payload = env.do(t, http.MethodGet, adminBase+"/options?activeOnly=weird", "")
		if code != http.StatusOK {
			t.Fatalf("activeOnly=weird: %d %v", code, payload)
		}
		ids = map[string]bool{}
		for _, item := range dataRows(t, payload) {
			ids[item["id"].(string)] = true
		}
		if !ids[builtinID] || !ids[rowA["id"].(string)] || !ids[rowB["id"].(string)] {
			t.Fatalf("invalid activeOnly must be ignored: %v", ids)
		}
	})
	t.Run("limit_clamped", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, adminBase+"/options?limit=2", "")
		if code != http.StatusOK {
			t.Fatalf("limit: %d %v", code, payload)
		}
		if rows := dataRows(t, payload); len(rows) != 2 {
			t.Fatalf("limit=2 rows = %d", len(rows))
		}
	})
	t.Run("projection_difference", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, adminBase+"/options?ids[]="+rowA["id"].(string), "")
		if code != http.StatusOK {
			t.Fatalf("admin options: %d %v", code, payload)
		}
		item := dataRows(t, payload)[0]
		if item["systemAccountId"] != ownerA {
			t.Fatalf("admin option must carry owner: %v", item)
		}
		if _, has := item["systemAccountName"]; !has {
			t.Fatalf("admin option must carry owner name: %v", item)
		}
		if _, has := item["chatSpeedFirstConfig"]; has {
			t.Fatalf("options must not return config JSON: %v", item)
		}
		restore := env.switchUser(t, "alice", "alice-password")
		defer restore()
		code, payload = env.do(t, http.MethodGet, selfBase+"/options?ids[]="+rowA["id"].(string), "")
		if code != http.StatusOK {
			t.Fatalf("self options: %d %v", code, payload)
		}
		item = dataRows(t, payload)[0]
		if _, has := item["systemAccountId"]; has {
			t.Fatalf("self option must not carry owner fields: %v", item)
		}
	})
	t.Run("self_options_contain_builtin", func(t *testing.T) {
		restore := env.switchUser(t, "bob", "bob-password")
		defer restore()
		code, payload := env.do(t, http.MethodGet, selfBase+"/options", "")
		if code != http.StatusOK {
			t.Fatalf("self options: %d %v", code, payload)
		}
		found := false
		for _, item := range dataRows(t, payload) {
			if item["id"] == builtinID {
				found = true
			}
			if item["id"] == rowA["id"] {
				t.Fatalf("other owner custom row leaked into self options: %v", payload)
			}
		}
		if !found {
			t.Fatalf("self options missing builtin: %v", payload)
		}
	})
}

// TestSchedulingPreferencePatchMatrix 覆盖 PATCH 契约：CAS、no-op、白名单、
// 形态切换清配置、内置 403 与作用域。
func TestSchedulingPreferencePatchMatrix(t *testing.T) {
	env := newTestEnv(t)
	ownerA := env.login(t, "alice", "alice-password", "user")
	env.login(t, "bob", "bob-password", "user")
	env.login(t, "root", "root-password", "admin")

	speedRow := env.createViaAPI(t, ownerA, `{"name":"A-speed","chatMode":"speed_first","chatSpeedFirstConfig":{}}`)
	costRow := env.createViaAPI(t, ownerA, `{"name":"A-cost","chatMode":"cost_first"}`)
	speedID := speedRow["id"].(string)
	costID := costRow["id"].(string)

	patch := func(base, id, body string) (int, map[string]any) {
		return env.do(t, http.MethodPatch, base+"/"+id, body)
	}
	expectedOf := func(id string) string { return env.updatedAtOf(t, id) }

	t.Run("expected_version_validation", func(t *testing.T) {
		cases := []struct {
			name string
			body string
		}{
			{"missing", `{"name":"x"}`},
			{"non_string", `{"name":"x","expectedUpdatedAt":123}`},
			{"invalid_rfc3339", `{"name":"x","expectedUpdatedAt":"2026-01-01 00:00:00"}`},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				code, payload := patch(adminBase, costID, testCase.body)
				if code != http.StatusBadRequest || message(t, payload) != "调度偏好配置版本格式不正确" {
					t.Fatalf("%s: %d %v", testCase.name, code, payload)
				}
			})
		}
	})
	t.Run("empty_change_rejected", func(t *testing.T) {
		code, payload := patch(adminBase, costID, `{"expectedUpdatedAt":"`+expectedOf(costID)+`"}`)
		if code != http.StatusBadRequest || message(t, payload) != "请提供要修改的调度偏好内容" {
			t.Fatalf("empty change: %d %v", code, payload)
		}
	})
	t.Run("unknown_key_rejected", func(t *testing.T) {
		code, payload := patch(adminBase, costID, `{"builtIn":true,"expectedUpdatedAt":"`+expectedOf(costID)+`"}`)
		if code != http.StatusBadRequest || message(t, payload) != "调度偏好参数无效" {
			t.Fatalf("unknown key: %d %v", code, payload)
		}
	})
	t.Run("same_value_is_noop", func(t *testing.T) {
		before := expectedOf(costID)
		reasonsBefore := env.inval.count()
		logsBefore := len(env.sink.all())
		code, payload := patch(adminBase, costID, `{"name":"A-cost","chatMode":"cost_first","expectedUpdatedAt":"`+before+`"}`)
		if code != http.StatusOK {
			t.Fatalf("no-op: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		changed, _ := data["changedFields"].([]any)
		if len(changed) != 0 {
			t.Fatalf("no-op changedFields = %v", changed)
		}
		if data["updatedAt"] != before {
			t.Fatalf("no-op must not bump updatedAt: %v want %v", data["updatedAt"], before)
		}
		if env.updatedAtOf(t, costID) != before {
			t.Fatalf("no-op must be zero-DML on updated_at")
		}
		if env.inval.count() != reasonsBefore {
			t.Fatalf("no-op must not publish invalidation: %v", env.inval.reasonsSnapshot())
		}
		if len(env.sink.all()) != logsBefore {
			t.Fatalf("no-op must not write operation logs")
		}
	})
	t.Run("equivalent_rfc3339_canonicalized", func(t *testing.T) {
		// +08:00 offset 的等价表示归一后匹配 UTC millis 存储（同值 → no-op 200）。
		parsed, err := time.Parse(time.RFC3339Nano, expectedOf(costID))
		if err != nil {
			t.Fatal(err)
		}
		offsetForm := parsed.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02T15:04:05.000Z07:00")
		code, payload := patch(adminBase, costID, `{"name":"A-cost","expectedUpdatedAt":"`+offsetForm+`"}`)
		if code != http.StatusOK {
			t.Fatalf("equivalent offset form: %d %v", code, payload)
		}
	})
	t.Run("cas_conflict_409_with_current", func(t *testing.T) {
		code, payload := patch(adminBase, costID, `{"name":"A-cost-2","expectedUpdatedAt":"2001-01-01T00:00:00.000Z"}`)
		if code != http.StatusConflict {
			t.Fatalf("conflict: %d %v", code, payload)
		}
		if message(t, payload) != "调度偏好已被其他操作更新，请刷新后重试" {
			t.Fatalf("conflict message = %q", message(t, payload))
		}
		if payload["currentUpdatedAt"] != expectedOf(costID) {
			t.Fatalf("currentUpdatedAt = %v want %v", payload["currentUpdatedAt"], expectedOf(costID))
		}
	})
	t.Run("name_change_and_conflict", func(t *testing.T) {
		code, payload := patch(adminBase, costID, `{"name":"A-cost-renamed","expectedUpdatedAt":"`+expectedOf(costID)+`"}`)
		if code != http.StatusOK {
			t.Fatalf("rename: %d %v", code, payload)
		}
		data, _ := payload["data"].(map[string]any)
		changed, _ := data["changedFields"].([]any)
		if len(changed) != 1 || changed[0] != "name" {
			t.Fatalf("changedFields = %v", changed)
		}
		env.createViaAPI(t, ownerA, `{"name":"A-collide","chatMode":"cost_first"}`)
		code, payload = patch(adminBase, costID, `{"name":"A-collide","expectedUpdatedAt":"`+expectedOf(costID)+`"}`)
		if code != http.StatusConflict || message(t, payload) != "调度偏好名称已存在：A-collide" {
			t.Fatalf("patch name conflict: %d %v", code, payload)
		}
	})
	t.Run("mode_switch_clears_and_fills_config", func(t *testing.T) {
		// speed_first 携带配置替换。
		code, payload := patch(adminBase, speedID,
			`{"chatSpeedFirstConfig":{"firstByteDeadlineMs":45000},"expectedUpdatedAt":"`+expectedOf(speedID)+`"}`)
		if code != http.StatusOK {
			t.Fatalf("config replace: %d %v", code, payload)
		}
		stored := env.scalarText(t, `SELECT chat_speed_first_config_json FROM scheduling_preferences WHERE id = ?`, speedID)
		if !strings.Contains(stored.String, `"firstByteDeadlineMs":45000`) {
			t.Fatalf("config replace stored = %v", stored)
		}
		// speed_first → cost_first：配置清空为 NULL。
		code, payload = patch(adminBase, speedID, `{"chatMode":"cost_first","expectedUpdatedAt":"`+expectedOf(speedID)+`"}`)
		if code != http.StatusOK {
			t.Fatalf("switch to cost: %d %v", code, payload)
		}
		stored = env.scalarText(t, `SELECT chat_speed_first_config_json FROM scheduling_preferences WHERE id = ?`, speedID)
		if stored.Valid && stored.String != "" {
			t.Fatalf("speed→cost must clear config JSON, got %v", stored)
		}
		data, _ := payload["data"].(map[string]any)
		rowPatch, _ := data["rowPatch"].(map[string]any)
		if value, has := rowPatch["chatSpeedFirstConfig"]; !has || value != nil {
			t.Fatalf("rowPatch chatSpeedFirstConfig must be null: %v", rowPatch)
		}
		// cost_first 携带配置拒绝。
		code, payload = patch(adminBase, speedID,
			`{"chatMode":"cost_first","chatSpeedFirstConfig":{"firstByteDeadlineMs":45000},"expectedUpdatedAt":"`+expectedOf(speedID)+`"}`)
		if code != http.StatusBadRequest || message(t, payload) != "成本优先不接受速度参数配置" {
			t.Fatalf("cost with config: %d %v", code, payload)
		}
		// cost_first → speed_first 不带配置：按 D2 默认生成。
		code, payload = patch(adminBase, speedID, `{"chatMode":"speed_first","expectedUpdatedAt":"`+expectedOf(speedID)+`"}`)
		if code != http.StatusOK {
			t.Fatalf("switch to speed: %d %v", code, payload)
		}
		stored = env.scalarText(t, `SELECT chat_speed_first_config_json FROM scheduling_preferences WHERE id = ?`, speedID)
		if !strings.Contains(stored.String, `"firstByteDeadlineMs":30000`) {
			t.Fatalf("switch to speed must fill defaults, got %v", stored)
		}
	})
	t.Run("enabled_write_retired", func(t *testing.T) {
		// v2 用户裁决修订第 4 条：不设停用态，enabled 从写入口白名单退役——
		// 携带者由严格键校验拒绝（400），不产生 DML、不失效、不写日志。
		reasonsBefore := env.inval.count()
		logsBefore := len(env.sink.all())
		for _, body := range []string{
			`{"enabled":false,"expectedUpdatedAt":"` + expectedOf(costID) + `"}`,
			`{"enabled":true,"expectedUpdatedAt":"` + expectedOf(costID) + `"}`,
		} {
			code, payload := patch(adminBase, costID, body)
			if code != http.StatusBadRequest || message(t, payload) != "调度偏好参数无效" {
				t.Fatalf("enabled write must be rejected: %d %v", code, payload)
			}
		}
		if env.inval.count() != reasonsBefore {
			t.Fatalf("retired enabled write must not publish invalidation: %v", env.inval.reasonsSnapshot())
		}
		if len(env.sink.all()) != logsBefore {
			t.Fatalf("retired enabled write must not write operation logs")
		}
	})
	t.Run("invalid_mode_rejected", func(t *testing.T) {
		code, payload := patch(adminBase, costID, `{"chatMode":"balanced","expectedUpdatedAt":"`+expectedOf(costID)+`"}`)
		if code != http.StatusBadRequest || message(t, payload) != "调度偏好对话形态无效" {
			t.Fatalf("invalid mode: %d %v", code, payload)
		}
	})
	t.Run("builtin_403_precedes_version_check", func(t *testing.T) {
		code, payload := patch(adminBase, builtinID, `{"name":"x","expectedUpdatedAt":"2001-01-01T00:00:00.000Z"}`)
		if code != http.StatusForbidden || message(t, payload) != "系统内置调度偏好不可修改或删除" {
			t.Fatalf("builtin patch with stale version must still 403: %d %v", code, payload)
		}
	})
	t.Run("self_cannot_patch_other_owner_row", func(t *testing.T) {
		restore := env.switchUser(t, "bob", "bob-password")
		defer restore()
		code, payload := patch(selfBase, costID, `{"name":"hijack","expectedUpdatedAt":"`+expectedOf(costID)+`"}`)
		if code != http.StatusNotFound || message(t, payload) != "调度偏好不存在" {
			t.Fatalf("cross-owner patch: %d %v", code, payload)
		}
	})
	t.Run("patch_log_entry", func(t *testing.T) {
		logsBefore := len(env.sink.all())
		code, payload := patch(adminBase, costID, `{"name":"A-cost-final","expectedUpdatedAt":"`+expectedOf(costID)+`"}`)
		if code != http.StatusOK {
			t.Fatalf("patch: %d %v", code, payload)
		}
		logs := env.sink.all()
		if len(logs) != logsBefore+1 {
			t.Fatalf("operation logs = %d want +1", len(logs))
		}
		entry := logs[len(logs)-1]
		if entry.Module != "scheduling_preferences" || entry.Action != "update" ||
			entry.OperationKey != "scheduling_preferences.update" || entry.Mode != "admin" {
			t.Fatalf("log identity: %+v", entry)
		}
		if entry.Summary != "更新调度偏好：A-cost-final" {
			t.Fatalf("log summary = %q", entry.Summary)
		}
	})
}

// TestSchedulingPreferenceDeleteMatrix 覆盖 v2 用户裁决修订第 5 条 DELETE 契
// 约：删除总是成功并在同事务内把绑定 Key 重置回内置默认（响应 data 携带
// resetBindings、失效发布、审计落库），内置 403、404 与自删不变。
func TestSchedulingPreferenceDeleteMatrix(t *testing.T) {
	env := newTestEnv(t)
	ownerA := env.login(t, "alice", "alice-password", "user")
	env.login(t, "bob", "bob-password", "user")
	env.login(t, "root", "root-password", "admin")

	referenced := env.createViaAPI(t, ownerA, `{"name":"del-referenced","chatMode":"speed_first","chatSpeedFirstConfig":{"firstByteDeadlineMs":45000}}`)
	free := env.createViaAPI(t, ownerA, `{"name":"del-free","chatMode":"cost_first"}`)
	env.bindAPIKey(t, "key-del-1", ownerA, referenced["id"].(string), "active")
	env.bindAPIKey(t, "key-del-2", ownerA, referenced["id"].(string), "disabled")

	t.Run("referenced_delete_resets_bindings_and_succeeds", func(t *testing.T) {
		reasonsBefore := env.inval.count()
		logsBefore := len(env.sink.all())
		// 删除总是成功：被引用行不再 409，响应 data 携带受影响 Key 数。
		status, raw := env.doRaw(t, http.MethodDelete, adminBase+"/"+referenced["id"].(string), "")
		if status != http.StatusOK {
			t.Fatalf("referenced delete: %d %s", status, raw)
		}
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		data, _ := payload["data"].(map[string]any)
		if data == nil || fmt.Sprint(data["resetBindings"]) != "2" {
			t.Fatalf("resetBindings = %v want 2", data)
		}
		if env.count(t, `SELECT COUNT(1) FROM scheduling_preferences WHERE id = ?`, referenced["id"]) != 0 {
			t.Fatalf("referenced row must be hard-deleted")
		}
		// 两个绑定 Key（含停用 Key）都在同事务内重置回内置默认 ID。
		for _, keyID := range []string{"key-del-1", "key-del-2"} {
			if got := env.scalarText(t, `SELECT scheduling_preference_id FROM api_keys WHERE id = ?`, keyID); got.String != builtinID {
				t.Fatalf("%s binding after reset = %v want builtin", keyID, got)
			}
		}
		// 提交后发布全局失效（重置与删除都改变绑定 Key 的有效配置来源）。
		if env.inval.count() != reasonsBefore+1 || env.inval.reasonsSnapshot()[len(env.inval.reasonsSnapshot())-1] != "scheduling_preference_deleted" {
			t.Fatalf("delete must publish invalidation: %v", env.inval.reasonsSnapshot())
		}
		// 审计落库：action 含 binding_reset 语义，changes 记录受影响 Key 数。
		logs := env.sink.all()
		if len(logs) != logsBefore+1 {
			t.Fatalf("operation logs = %d want +1", len(logs))
		}
		entry := logs[len(logs)-1]
		if entry.Action != "delete_binding_reset" || entry.OperationKey != "scheduling_preferences.delete" {
			t.Fatalf("delete log identity: %+v", entry)
		}
		if entry.Summary != "删除调度偏好：del-referenced（2 个绑定 Key 重置为默认调度偏好）" {
			t.Fatalf("delete log summary = %q", entry.Summary)
		}
		foundReset := false
		for _, change := range entry.Changes {
			if change.Field == "resetBindings" && change.After == "2" {
				foundReset = true
			}
		}
		if !foundReset {
			t.Fatalf("delete log must record resetBindings: %+v", entry.Changes)
		}
	})
	t.Run("unreferenced_200_zero_reset", func(t *testing.T) {
		reasonsBefore := env.inval.count()
		logsBefore := len(env.sink.all())
		status, raw := env.doRaw(t, http.MethodDelete, adminBase+"/"+free["id"].(string), "")
		if status != http.StatusOK {
			t.Fatalf("free delete: %d %s", status, raw)
		}
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		data, _ := payload["data"].(map[string]any)
		if data == nil || fmt.Sprint(data["resetBindings"]) != "0" {
			t.Fatalf("resetBindings = %v want 0", data)
		}
		if env.count(t, `SELECT COUNT(1) FROM scheduling_preferences WHERE id = ?`, free["id"]) != 0 {
			t.Fatalf("row must be hard-deleted")
		}
		if env.inval.count() != reasonsBefore+1 {
			t.Fatalf("delete must publish invalidation: %v", env.inval.reasonsSnapshot())
		}
		logs := env.sink.all()
		if len(logs) != logsBefore+1 {
			t.Fatalf("operation logs = %d want +1", len(logs))
		}
		entry := logs[len(logs)-1]
		if entry.Action != "delete_binding_reset" || entry.OperationKey != "scheduling_preferences.delete" ||
			entry.Summary != "删除调度偏好：del-free" {
			t.Fatalf("delete log: %+v", entry)
		}
	})
	t.Run("builtin_403", func(t *testing.T) {
		code, payload := env.do(t, http.MethodDelete, adminBase+"/"+builtinID, "")
		if code != http.StatusForbidden || message(t, payload) != "系统内置调度偏好不可修改或删除" {
			t.Fatalf("builtin delete: %d %v", code, payload)
		}
	})
	t.Run("missing_404", func(t *testing.T) {
		code, payload := env.do(t, http.MethodDelete, adminBase+"/schpref-missing", "")
		if code != http.StatusNotFound || message(t, payload) != "调度偏好不存在" {
			t.Fatalf("missing delete: %d %v", code, payload)
		}
	})
	t.Run("self_cannot_delete_other_owner_row", func(t *testing.T) {
		restore := env.switchUser(t, "bob", "bob-password")
		defer restore()
		code, payload := env.do(t, http.MethodDelete, selfBase+"/"+referenced["id"].(string), "")
		if code != http.StatusNotFound || message(t, payload) != "调度偏好不存在" {
			t.Fatalf("cross-owner delete: %d %v", code, payload)
		}
	})
	t.Run("self_delete_own_row", func(t *testing.T) {
		owned := env.createViaAPI(t, ownerA, `{"name":"del-self","chatMode":"cost_first"}`)
		restore := env.switchUser(t, "alice", "alice-password")
		code, payload := env.do(t, http.MethodDelete, selfBase+"/"+owned["id"].(string), "")
		restore()
		if code != http.StatusOK {
			t.Fatalf("self delete own: %d %v", code, payload)
		}
		if env.count(t, `SELECT COUNT(1) FROM scheduling_preferences WHERE id = ?`, owned["id"]) != 0 {
			t.Fatalf("self-deleted row must be gone")
		}
	})
}

// TestSchedulingPreferenceNormalizeReuse 钉住与策略路由共享的 normalize 出口：
// 偏好写入的默认值/文案不得另写一套（对齐计划 D2「复用/抽取」约束）。
func TestSchedulingPreferenceNormalizeReuse(t *testing.T) {
	normal, stored, err := normalizeSpeedConfig(map[string]any{
		"firstByteDeadlineMs": 50000,
		"speedFirstConfig":    map[string]any{"slowTriggerCount": 9},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if normal.FirstByteDeadlineMs != 50000 {
		t.Fatalf("deadline = %d", normal.FirstByteDeadlineMs)
	}
	if normal.SpeedFirstConfig == nil || normal.SpeedFirstConfig.SlowTriggerCount != 9 ||
		normal.SpeedFirstConfig.TotalTimeDeadlineSeconds != 120 {
		t.Fatalf("speed config = %+v", normal.SpeedFirstConfig)
	}
	if !strings.Contains(stored.String, `"firstByteDeadlineMs":50000`) || strings.Contains(stored.String, "schedulingPreference") {
		t.Fatalf("stored document must be the D2 shape: %v", stored)
	}
	if _, _, err := normalizeSpeedConfig(map[string]any{
		"speedFirstConfig": map[string]any{"slowTriggerCount": 99},
	}); err == nil || err.Error() != "速度优先触发次数必须是 2-10" {
		t.Fatalf("range error = %v", err)
	}
	// 缺省输入全默认（D2 表）。
	document, _, err := normalizeSpeedConfig(map[string]any{})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if document.FirstByteDeadlineMs != 30000 || document.SpeedFirstConfig.SlowTriggerCount != 3 ||
		document.SpeedFirstConfig.CompactionTotalTimeDeadlineSeconds != 300 {
		t.Fatalf("defaults = %+v", document)
	}
}

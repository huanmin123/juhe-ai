package delegated

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oidc"
)

// ---------------------------------------------------------------------------
// api-keys PATCH flow arms (revision conflict, guards, quota binding sync).
// ---------------------------------------------------------------------------

func TestW9CPatchApiKeyRevisionConflict409(t *testing.T) {
	f := newApiKeyFixture(t)
	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2000-01-01T00:00:00.000Z","name":"renamed"}`, f.token)
	if r.status != http.StatusConflict {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	if r.body["message"] != apiKeyRevisionConflictMessage {
		t.Fatalf("message = %v", r.body["message"])
	}
	if r.body["currentRevision"] != "2026-01-10T08:30:00.000Z" {
		t.Fatalf("currentRevision = %v", r.body["currentRevision"])
	}
}

func TestW9CPatchApiKeyMissing404AndUnknownStrategy(t *testing.T) {
	f := newApiKeyFixture(t)
	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-nope",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"x"}`, f.token)
	if r.status != http.StatusNotFound || r.body["message"] != "API Key 不存在" {
		t.Fatalf("missing key = %d %v", r.status, r.body["message"])
	}
	// Unknown route strategy reference.
	r = f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","routeStrategyId":"rst-nope"}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "API Key 绑定的策略路由不存在或不属于当前用户" {
		t.Fatalf("unknown strategy = %d %v", r.status, r.body["message"])
	}
	// Disabled route strategy reference.
	r = f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","routeStrategyId":"rst-2"}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "API Key 只能绑定启用状态的策略路由" {
		t.Fatalf("disabled strategy = %d %v", r.status, r.body["message"])
	}
	// Strategy owned by another account.
	other := f.otherOwner()
	f.env.seedStrategy("rst-other", other, "other", "normal", "active")
	r = f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","routeStrategyId":"rst-other"}`, f.token)
	if r.status != http.StatusBadRequest {
		t.Fatalf("foreign strategy = %d (%s)", r.status, r.raw)
	}
}

func TestW9CPatchApiKeyDuplicateName409(t *testing.T) {
	f := newApiKeyFixture(t)
	f.env.seedApiKey("ak-2", f.accountID, "key-2", "rst-1", "active")
	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"key-2"}`, f.token)
	if r.status != http.StatusConflict || !strings.Contains(fmt.Sprint(r.body["message"]), "API Key 名称已存在") {
		t.Fatalf("duplicate name = %d %v", r.status, r.body["message"])
	}
}

func TestW9CPatchApiKeyChatAndDefaultGuards(t *testing.T) {
	t.Run("chat_rename_guard", func(t *testing.T) {
		f := newApiKeyFixture(t)
		f.env.exec(`UPDATE api_keys SET purpose = 'chat' WHERE id = 'ak-1'`)
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"new-name"}`, f.token)
		if r.status != http.StatusBadRequest || r.body["message"] != "AI 对话 API Key 不允许修改名称" {
			t.Fatalf("chat guard = %d %v", r.status, r.body["message"])
		}
	})
	t.Run("default_rename_guard", func(t *testing.T) {
		f := newApiKeyFixture(t)
		f.env.exec(`UPDATE api_keys SET is_default = 1 WHERE id = 'ak-1'`)
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"new-name"}`, f.token)
		if r.status != http.StatusBadRequest || r.body["message"] != "默认 API Key 不允许修改名称" {
			t.Fatalf("default rename guard = %d %v", r.status, r.body["message"])
		}
	})
	t.Run("default_strategy_guard", func(t *testing.T) {
		f := newApiKeyFixture(t)
		f.env.exec(`UPDATE api_keys SET is_default = 1 WHERE id = 'ak-1'`)
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","routeStrategyId":"rst-3"}`, f.token)
		if r.status != http.StatusBadRequest || r.body["message"] != "默认 API Key 不允许更换策略路由" {
			t.Fatalf("default strategy guard = %d %v", r.status, r.body["message"])
		}
	})
}

func TestW9CPatchApiKeyDBNil500(t *testing.T) {
	f := newApiKeyFixture(t)
	f.env.deps.DB = nil
	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"x"}`, f.token)
	if r.status != http.StatusInternalServerError || r.body["message"] != "服务器内部错误" {
		t.Fatalf("db nil = %d %v", r.status, r.body["message"])
	}
}

func TestW9CPatchApiKeyQuotaBindingSync(t *testing.T) {
	f := newApiKeyFixture(t)
	f.env.exec(`UPDATE api_keys SET quota_limits_json = ? WHERE id = 'ak-1'`, `{"hourly":{"enabled":true,"hours":5}}`)

	// Disable: binding delete only, no dirty row (SQLite full-rebuild mode).
	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","status":"disabled"}`, f.token)
	if r.status != http.StatusOK {
		t.Fatalf("disable = %d (%s)", r.status, r.raw)
	}
	var count int
	if err := f.env.db.QueryRow(`SELECT COUNT(1) FROM request_quota_hourly_window_scope_bindings WHERE source_id = 'ak-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("disabled binding rows = %d", count)
	}

	// Re-activate: binding insert with the hourly window.
	r = f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		fmt.Sprintf(`{"expectedRevision":%q,"status":"active"}`, "2026-01-10T08:30:00.001000Z"), f.token)
	if r.status != http.StatusOK {
		t.Fatalf("activate = %d (%s)", r.status, r.raw)
	}
	var windowHours int
	if err := f.env.db.QueryRow(`SELECT window_hours FROM request_quota_hourly_window_scope_bindings WHERE source_id = 'ak-1'`).Scan(&windowHours); err != nil {
		t.Fatalf("binding must exist after activation: %v", err)
	}
	if windowHours != 5 {
		t.Fatalf("window_hours = %d", windowHours)
	}

	// Quota variants that must not insert a binding.
	variants := []string{
		`{invalid`,
		`{}`,
		`{"hourly":{"enabled":false,"hours":5}}`,
		`{"hourly":{"enabled":true}}`,
		`{"hourly":{"enabled":true,"hours":0}}`,
		`{"hourly":{"enabled":true,"hours":9999}}`,
	}
	for _, variant := range variants {
		t.Run("no_binding_"+variant, func(t *testing.T) {
			f2 := newApiKeyFixture(t)
			f2.env.exec(`UPDATE api_keys SET status = 'disabled', quota_limits_json = ? WHERE id = 'ak-1'`, variant)
			r := f2.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
				`{"expectedRevision":"2026-01-10T08:30:00.000Z","status":"active"}`, f2.token)
			if r.status != http.StatusOK {
				t.Fatalf("activate with quota %s = %d (%s)", variant, r.status, r.raw)
			}
			var count int
			if err := f2.env.db.QueryRow(`SELECT COUNT(1) FROM request_quota_hourly_window_scope_bindings WHERE source_id = 'ak-1'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("quota %s must not insert a binding, got %d rows", variant, count)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// profile flow arms.
// ---------------------------------------------------------------------------

// w9CRequestAs builds a handler-level request carrying the delegated access
// context (the requireDelegatedAccess output) without a stored account row.
// The Content-Length header is set explicitly: hasRequestBody reads the
// header, and httptest.NewRequest only fills the field (a real HTTP round
// trip fills both).
func w9CRequestAs(t *testing.T, method, target, body, systemAccountID string) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	return request.WithContext(withContext(request.Context(), &AccessContext{
		Token: &oidc.AccessTokenContext{SystemAccountID: systemAccountID},
	}))
}

func TestW9CGetProfileMissingUser404(t *testing.T) {
	f := newFixture(t, "juhe:profile.read")
	// Token contexts keep validating only while the account row exists; drive
	// the handler directly with an access context for a missing account.
	rec := httptest.NewRecorder()
	f.env.deps.getProfile(rec, w9CRequestAs(t, http.MethodGet, Prefix+"/profile", "", "acc-ghost"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "用户不存在") {
		t.Fatalf("missing profile = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9CPatchProfileArms(t *testing.T) {
	f := newFixture(t, "juhe:profile.write")
	f.env.seedSystemAccount("acc-other", "bob")

	// Non-string displayName (the non-CJK zod copy localizes to the default).
	r := f.env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":5}`, f.token)
	if r.status != http.StatusBadRequest {
		t.Fatalf("non-string = %d %v", r.status, r.body["message"])
	}
	// Unknown extra key.
	r = f.env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"x","nope":1}`, f.token)
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown key = %d %v", r.status, r.body["message"])
	}
	// Blank (already covered by TestPatchProfileValidation; keep the status
	// contract here without the localized copy).
	r = f.env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"   "}`, f.token)
	if r.status != http.StatusBadRequest {
		t.Fatalf("blank = %d %v", r.status, r.body["message"])
	}
	// Interior whitespace → store normalizeRequiredText rejection (409).
	r = f.env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"has space"}`, f.token)
	if r.status != http.StatusConflict || r.body["message"] != "用户名称不能包含空格" {
		t.Fatalf("whitespace = %d %v", r.status, r.body["message"])
	}
	// Case-insensitive display-name conflict.
	f.env.exec(`UPDATE system_accounts SET display_name = 'Bob-Display' WHERE id = 'acc-other'`)
	r = f.env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"bob-display"}`, f.token)
	if r.status != http.StatusConflict || r.body["message"] != "用户名称已存在" {
		t.Fatalf("conflict = %d %v", r.status, r.body["message"])
	}
	// Missing account → 404 (handler-level, see TestW9CGetProfileMissingUser404).
	rec := httptest.NewRecorder()
	f.env.deps.patchProfile(rec, w9CRequestAs(t, http.MethodPatch, Prefix+"/profile", `{"displayName":"newname"}`, "acc-ghost"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "用户不存在") {
		t.Fatalf("missing account = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9CGetRequestLimitsMissingUser404(t *testing.T) {
	f := newFixture(t, "juhe:request_limits.read")
	rec := httptest.NewRecorder()
	f.env.deps.getRequestLimitsSnapshot(rec, w9CRequestAs(t, http.MethodGet, Prefix+"/request-limits", "", "acc-ghost"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "用户不存在") {
		t.Fatalf("missing user = %d %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// groups / route strategies flow arms.
// ---------------------------------------------------------------------------

func TestW9CGetAndDeleteGroupMissing404(t *testing.T) {
	f := newFixture(t, "juhe:groups.read", "juhe:groups.write")
	r := f.env.do(http.MethodGet, Prefix+"/groups/grp-nope", "", f.token)
	if r.status != http.StatusNotFound || r.body["message"] != "分组不存在" {
		t.Fatalf("get group = %d %v", r.status, r.body["message"])
	}
	r = f.env.do(http.MethodDelete, Prefix+"/groups/grp-nope", "", f.token)
	if r.status != http.StatusNotFound || r.body["message"] != "分组不存在" {
		t.Fatalf("delete group = %d %v", r.status, r.body["message"])
	}
}

func TestW9CDeleteGroupForbiddenWithoutWriteScope(t *testing.T) {
	f := newFixture(t, "juhe:groups.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
	// Group bound to a strategy: delete without route_strategies.write scope
	// answers 409 insufficient_scope.
	f.env.seedStrategy("rst-1", f.accountID, "s1", "normal", "active")
	f.env.seedStrategyBinding("rsb-1", "rst-1", f.accountID, "grp-1", 1, 1, "active")
	r := f.env.do(http.MethodDelete, Prefix+"/groups/grp-1", "", f.token)
	if r.status != http.StatusForbidden {
		t.Fatalf("bound group delete without scope = %d (%s)", r.status, r.raw)
	}
	if got := r.header.Get("WWW-Authenticate"); !strings.Contains(got, "route_strategies.write") {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	// Unbound group deletes fine without the extra scope.
	f.env.seedGroup("grp-2", f.accountID, "g2", "openai", "personal", true)
	r = f.env.do(http.MethodDelete, Prefix+"/groups/grp-2", "", f.token)
	if r.status != http.StatusNoContent {
		t.Fatalf("unbound group delete = %d (%s)", r.status, r.raw)
	}
}

func TestW9CPatchGroupValidationArms(t *testing.T) {
	f := newFixture(t, "juhe:groups.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
	unknown := `{"nope":1}`
	r := f.env.do(http.MethodPatch, Prefix+"/groups/grp-1", unknown, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "分组参数无效" {
		t.Fatalf("unknown field = %d %v", r.status, r.body["message"])
	}
	r = f.env.do(http.MethodPatch, Prefix+"/groups/grp-1", `{"expectedUpdatedAt":"nope","name":"x"}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "分组版本格式不正确" {
		t.Fatalf("bad version = %d %v", r.status, r.body["message"])
	}
	r = f.env.do(http.MethodPatch, Prefix+"/groups/grp-1", `{"expectedUpdatedAt":"2026-01-10T08:30:00.000Z"}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "请提供要修改的分组内容" {
		t.Fatalf("no change = %d %v", r.status, r.body["message"])
	}
	// Stale version → store conflict → 409.
	r = f.env.do(http.MethodPatch, Prefix+"/groups/grp-1",
		`{"expectedUpdatedAt":"2000-01-01T00:00:00.000Z","name":"renamed"}`, f.token)
	if r.status != http.StatusConflict {
		t.Fatalf("stale version = %d (%s)", r.status, r.raw)
	}
	// Invalid groupType in an otherwise valid patch → 400 分组参数无效.
	r = f.env.do(http.MethodPatch, Prefix+"/groups/grp-1",
		`{"expectedUpdatedAt":"2026-01-10T08:30:00.000Z","groupType":"weird"}`, f.token)
	if r.status != http.StatusBadRequest {
		t.Fatalf("bad groupType = %d (%s)", r.status, r.raw)
	}
	// Missing group → 404.
	r = f.env.do(http.MethodPatch, Prefix+"/groups/grp-nope",
		`{"expectedUpdatedAt":"2026-01-10T08:30:00.000Z","name":"x"}`, f.token)
	if r.status != http.StatusNotFound {
		t.Fatalf("missing group patch = %d (%s)", r.status, r.raw)
	}
}

func TestW9CCreateGroupDisabledProvider400(t *testing.T) {
	// newFixture already seeds the providers; no re-seed here.
	f := newFixture(t, "juhe:groups.write")
	r := f.env.do(http.MethodPost, Prefix+"/groups",
		`{"name":"g-new","providerCode":"disabled-provider"}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "供应商不存在或已停用" {
		t.Fatalf("disabled provider = %d %v", r.status, r.body["message"])
	}
	r = f.env.do(http.MethodPost, Prefix+"/groups",
		`{"name":"g-new","providerCode":"unknown-provider"}`, f.token)
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown provider = %d (%s)", r.status, r.raw)
	}
}

func TestW9CRouteStrategyGetAndMutationArms(t *testing.T) {
	f := newFixture(t, "juhe:route_strategies.read", "juhe:route_strategies.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)

	r := f.env.do(http.MethodGet, Prefix+"/route-strategies/rst-nope", "", f.token)
	if r.status != http.StatusNotFound || r.body["message"] != "策略路由不存在" {
		t.Fatalf("get strategy = %d %v", r.status, r.body["message"])
	}

	// Create without bindings → dedicated message.
	r = f.env.do(http.MethodPost, Prefix+"/route-strategies", `{"name":"s1"}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "策略路由至少需要绑定一个分组" {
		t.Fatalf("create without bindings = %d %v", r.status, r.body["message"])
	}
	// Create bound to a foreign group → 400.
	other := f.otherOwner()
	f.env.seedGroup("grp-other", other, "other", "openai", "personal", true)
	r = f.env.do(http.MethodPost, Prefix+"/route-strategies",
		`{"name":"s1","groupBindings":[{"groupId":"grp-other"}]}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "策略路由只能绑定自己的分组" {
		t.Fatalf("foreign binding = %d %v", r.status, r.body["message"])
	}
	// Valid create.
	r = f.env.do(http.MethodPost, Prefix+"/route-strategies",
		`{"name":"s1","groupBindings":[{"groupId":"grp-1"}]}`, f.token)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d (%s)", r.status, r.raw)
	}
	createdID, _ := r.body["data"].(map[string]any)["id"].(string)
	if createdID == "" {
		t.Fatalf("created id missing: %v", r.body)
	}

	// Version conflict on stale patch.
	versioned, _ := json.Marshal(map[string]any{
		"expectedUpdatedAt": "2000-01-01T00:00:00.000Z", "name": "s1-renamed",
	})
	r = f.env.do(http.MethodPatch, Prefix+"/route-strategies/"+createdID, string(versioned), f.token)
	if r.status != http.StatusConflict {
		t.Fatalf("stale strategy patch = %d (%s)", r.status, r.raw)
	}
	if _, hasCurrent := r.body["currentUpdatedAt"]; !hasCurrent {
		t.Fatalf("version conflict body = %v", r.body)
	}

	// Delete the strategy.
	r = f.env.do(http.MethodDelete, Prefix+"/route-strategies/"+createdID, "", f.token)
	if r.status != http.StatusNoContent {
		t.Fatalf("delete strategy = %d (%s)", r.status, r.raw)
	}
	r = f.env.do(http.MethodDelete, Prefix+"/route-strategies/"+createdID, "", f.token)
	if r.status != http.StatusNotFound {
		t.Fatalf("delete missing strategy = %d (%s)", r.status, r.raw)
	}
}

func TestW9CListAiAccountsArms(t *testing.T) {
	f := newFixture(t, "juhe:ai_accounts.write")
	f.env.seedAiAccount("acct-1", f.accountID, "own-1", "active", "")
	// Validation arms are already covered by TestPatchAiAccount; exercise the
	// revision conflict 409 arm here.
	r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1",
		`{"expectedConfigRevision":99,"name":"x"}`, f.token)
	if r.status != http.StatusConflict {
		t.Fatalf("revision conflict = %d (%s)", r.status, r.raw)
	}
	// Disabled status patch succeeds and bumps the revision.
	r = f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1",
		`{"expectedConfigRevision":1,"status":"disabled"}`, f.token)
	if r.status != http.StatusOK {
		t.Fatalf("disable = %d (%s)", r.status, r.raw)
	}
	item := r.data(t)
	if item["status"] != "disabled" {
		t.Fatalf("status = %v", item["status"])
	}
	// Bad status name → 400.
	r = f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1",
		`{"expectedConfigRevision":2,"status":"paused"}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "AI 账户参数无效" {
		t.Fatalf("bad status = %d %v", r.status, r.body["message"])
	}
	// Non-string name → 400.
	r = f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1",
		`{"expectedConfigRevision":2,"name":5}`, f.token)
	if r.status != http.StatusBadRequest {
		t.Fatalf("bad name = %d (%s)", r.status, r.raw)
	}
}

package delegated

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// ---------------------------------------------------------------------------
// Shared fixtures.
// ---------------------------------------------------------------------------

type fixture struct {
	env       *env
	accountID string
	token     string
}

// newFixture seeds one owner system account with a token carrying the given
// scopes (each test starts from a fresh env/database).
func newFixture(t *testing.T, scopes ...string) *fixture {
	env := newEnv(t)
	env.seedProviders()
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", scopes...)
	return &fixture{env: env, accountID: "acc-1", token: token}
}

func (f *fixture) otherOwner() string {
	f.env.seedSystemAccount("acc-2", "bob")
	return "acc-2"
}

func groupKeys(t *testing.T, item map[string]any, wantID, wantName, wantProvider, wantGroupType string, wantEnabled bool) {
	t.Helper()
	want := map[string]any{
		"id": wantID, "name": wantName, "providerCode": wantProvider,
		"enabled": wantEnabled, "groupType": wantGroupType,
		"updatedAt": "2026-01-10T08:30:00.000Z",
	}
	if len(item) != len(want) {
		t.Fatalf("group dto keys = %v, want exactly %v", keysOf(item), keysOf(want))
	}
	for key, value := range want {
		if item[key] != value {
			t.Fatalf("group dto[%q] = %v, want %v (full item %v)", key, item[key], value, item)
		}
	}
}

func keysOf(item map[string]any) []string {
	keys := []string{}
	for key := range item {
		keys = append(keys, key)
	}
	return keys
}

// numEqual compares decoded JSON numbers (always float64) with literals.
func numEqual(got any, want float64) bool {
	number, ok := got.(float64)
	return ok && number == want
}

func assertListEnvelope(t *testing.T, r response, wantTotal int, wantHasMore bool, wantPage, wantPageSize int, wantItemCount int) {
	t.Helper()
	data := r.data(t)
	if !numEqual(data["total"], float64(wantTotal)) {
		t.Fatalf("data[total] = %v, want %d (data %v)", data["total"], wantTotal, data)
	}
	if data["hasMore"] != wantHasMore {
		t.Fatalf("data[hasMore] = %v, want %v", data["hasMore"], wantHasMore)
	}
	if !numEqual(data["page"], float64(wantPage)) {
		t.Fatalf("data[page] = %v, want %d", data["page"], wantPage)
	}
	if !numEqual(data["pageSize"], float64(wantPageSize)) {
		t.Fatalf("data[pageSize] = %v, want %d", data["pageSize"], wantPageSize)
	}
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("data.items missing: %v", data)
	}
	if len(items) != wantItemCount {
		t.Fatalf("len(items) = %d, want %d", len(items), wantItemCount)
	}
}

// ---------------------------------------------------------------------------
// Groups.
// ---------------------------------------------------------------------------

func TestListGroupsEmptyAndPaged(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newFixture(t, "juhe:groups.read")
		r := f.env.do(http.MethodGet, Prefix+"/groups", "", f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		// groups.Store defaults: page 1, pageSize 50 (Node
		// group-read.repository.ts defaultGroupListPageSize = 50).
		assertListEnvelope(t, r, 0, false, 1, 50, 0)
	})

	t.Run("paging", func(t *testing.T) {
		f := newFixture(t, "juhe:groups.read")
		f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
		f.env.seedGroup("grp-2", f.accountID, "g2", "openai", "personal", true)
		f.env.seedGroup("grp-3", f.accountID, "g3", "anthropic", "high_concurrency", false)

		r := f.env.do(http.MethodGet, Prefix+"/groups?page=1&pageSize=2", "", f.token)
		assertListEnvelope(t, r, 3, true, 1, 2, 2)

		r = f.env.do(http.MethodGet, Prefix+"/groups?page=2&pageSize=2", "", f.token)
		assertListEnvelope(t, r, 3, false, 2, 2, 1)

		// Non-positive or non-numeric paging falls back to the store defaults.
		r = f.env.do(http.MethodGet, Prefix+"/groups?page=abc&pageSize=-1", "", f.token)
		assertListEnvelope(t, r, 3, false, 1, 50, 3)
	})

	t.Run("dto_shape", func(t *testing.T) {
		f := newFixture(t, "juhe:groups.read")
		f.env.seedGroup("grp-9", f.accountID, "g9", "openai", "personal", true)
		r := f.env.do(http.MethodGet, Prefix+"/groups", "", f.token)
		items := r.dataArray(t, "items")
		item, _ := items[0].(map[string]any)
		groupKeys(t, item, "grp-9", "g9", "openai", "personal", true)
	})

	t.Run("other_owner_group_hidden", func(t *testing.T) {
		f := newFixture(t, "juhe:groups.read")
		other := f.otherOwner()
		f.env.seedGroup("grp-other", other, "bob-group", "openai", "personal", true)
		r := f.env.do(http.MethodGet, Prefix+"/groups", "", f.token)
		assertListEnvelope(t, r, 0, false, 1, 50, 0)
	})
}

func TestGetGroup(t *testing.T) {
	f := newFixture(t, "juhe:groups.read")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)

	t.Run("found", func(t *testing.T) {
		r := f.env.do(http.MethodGet, Prefix+"/groups/grp-1", "", f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		groupKeys(t, r.data(t), "grp-1", "g1", "openai", "personal", true)
	})

	t.Run("missing", func(t *testing.T) {
		r := f.env.do(http.MethodGet, Prefix+"/groups/grp-nope", "", f.token)
		r.requireMessage(t, http.StatusNotFound, "分组不存在")
	})

	t.Run("foreign_group_404", func(t *testing.T) {
		other := f.otherOwner()
		f.env.seedGroup("grp-foreign", other, "bob-group", "openai", "personal", true)
		r := f.env.do(http.MethodGet, Prefix+"/groups/grp-foreign", "", f.token)
		r.requireMessage(t, http.StatusNotFound, "分组不存在")
	})
}

func TestCreateGroup(t *testing.T) {
	f := newFixture(t, "juhe:groups.write")

	t.Run("created_201_envelope", func(t *testing.T) {
		r := f.env.do(http.MethodPost, Prefix+"/groups", `{"name":"  g1  ","providerCode":"openai","enabled":false,"description":"描述"}`, f.token)
		if r.status != http.StatusCreated {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if _, hasMessage := r.body["message"]; hasMessage {
			t.Fatalf("201 envelope must not carry message: %s", r.raw)
		}
		data := r.data(t)
		if data["name"] != "g1" || data["description"] != "描述" || data["enabled"] != false {
			t.Fatalf("created dto = %v", data)
		}
		if data["groupType"] != "personal" || data["providerCode"] != "openai" {
			t.Fatalf("created dto = %v", data)
		}
		if _, ok := data["updatedAt"].(string); !ok {
			t.Fatalf("created dto missing updatedAt: %v", data)
		}
	})

	t.Run("high_concurrency_type", func(t *testing.T) {
		r := f.env.do(http.MethodPost, Prefix+"/groups", `{"name":"g2","providerCode":"openai","groupType":"high_concurrency"}`, f.token)
		if r.status != http.StatusCreated {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if r.data(t)["groupType"] != "high_concurrency" {
			t.Fatalf("groupType = %v", r.data(t)["groupType"])
		}
	})

	t.Run("validation_400", func(t *testing.T) {
		cases := []struct{ name, body string }{
			{"empty_object", `{}`},
			{"blank_name", `{"name":"   ","providerCode":"openai"}`},
			{"missing_provider", `{"name":"g"}`},
			{"blank_provider", `{"name":"g","providerCode":"  "}`},
			{"unknown_extra_field", `{"name":"g","providerCode":"openai","nope":1}`},
			{"bad_group_type", `{"name":"g","providerCode":"openai","groupType":"team"}`},
			{"description_not_string", `{"name":"g","providerCode":"openai","description":3}`},
			{"enabled_not_bool", `{"name":"g","providerCode":"openai","enabled":"yes"}`},
			{"scheduling_not_object", `{"name":"g","providerCode":"openai","schedulingPolicy":[]}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				r := f.env.do(http.MethodPost, Prefix+"/groups", tc.body, f.token)
				r.requireMessage(t, http.StatusBadRequest, "分组参数无效")
			})
		}
	})

	t.Run("provider_unknown_or_disabled_400", func(t *testing.T) {
		// Node routes precheck findProviderOptionByCodeAsync and render the
		// merged copy for both unknown and disabled codes.
		r := f.env.do(http.MethodPost, Prefix+"/groups", `{"name":"g","providerCode":"nope"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "供应商不存在或已停用")
		r = f.env.do(http.MethodPost, Prefix+"/groups", `{"name":"g","providerCode":"disabled-provider"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "供应商不存在或已停用")
	})

	t.Run("duplicate_name_409", func(t *testing.T) {
		f.env.seedGroup("grp-dup", f.accountID, "dup", "openai", "personal", true)
		r := f.env.do(http.MethodPost, Prefix+"/groups", `{"name":"dup","providerCode":"openai"}`, f.token)
		if r.status != http.StatusConflict {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		message, _ := r.body["message"].(string)
		if !strings.Contains(message, "已存在") {
			t.Fatalf("duplicate message = %q, want 已存在 probe", message)
		}
	})
}

func TestPatchGroup(t *testing.T) {
	f := newFixture(t, "juhe:groups.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
	stamp := "2026-01-10T08:30:00.000Z"

	t.Run("rename_200", func(t *testing.T) {
		body := `{"name":"renamed","expectedUpdatedAt":"` + stamp + `"}`
		r := f.env.do(http.MethodPatch, Prefix+"/groups/grp-1", body, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		data := r.data(t)
		if data["name"] != "renamed" {
			t.Fatalf("name = %v", data["name"])
		}
		if got, _ := data["updatedAt"].(string); !strings.HasSuffix(got, "Z") || got == stamp {
			t.Fatalf("updatedAt = %v, want advanced past %s", data["updatedAt"], stamp)
		}
	})

	t.Run("version_format_400", func(t *testing.T) {
		for _, body := range []string{
			`{"name":"x"}`,
			`{"name":"x","expectedUpdatedAt":""}`,
			`{"name":"x","expectedUpdatedAt":"not-a-time"}`,
			`{"name":"x","expectedUpdatedAt":123}`,
		} {
			r := f.env.do(http.MethodPatch, Prefix+"/groups/grp-1", body, f.token)
			r.requireMessage(t, http.StatusBadRequest, "分组版本格式不正确")
		}
	})

	t.Run("no_change_400", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/groups/grp-1", `{"expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "请提供要修改的分组内容")
	})

	t.Run("unknown_field_400", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/groups/grp-1", `{"nope":1,"expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "分组参数无效")
	})

	t.Run("bad_group_type_400", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/groups/grp-1", `{"groupType":"team","expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "分组参数无效")
	})

	t.Run("missing_404", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/groups/grp-nope", `{"name":"x","expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusNotFound, "分组不存在")
	})

	t.Run("version_conflict_409", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/groups/grp-1",
			`{"name":"x","expectedUpdatedAt":"2020-01-01T00:00:00.000Z"}`, f.token)
		r.requireMessage(t, http.StatusConflict, "分组已被其他操作更新，请刷新后重试")
	})
}

func TestDeleteGroup(t *testing.T) {
	f := newFixture(t, "juhe:groups.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)

	t.Run("unbound_204", func(t *testing.T) {
		r := f.env.do(http.MethodDelete, Prefix+"/groups/grp-1", "", f.token)
		if r.status != http.StatusNoContent {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if len(r.raw) != 0 {
			t.Fatalf("204 body = %q, want empty", r.raw)
		}
	})

	t.Run("missing_404", func(t *testing.T) {
		r := f.env.do(http.MethodDelete, Prefix+"/groups/grp-nope", "", f.token)
		r.requireMessage(t, http.StatusNotFound, "分组不存在")
	})

	t.Run("bound_without_strategy_scope_403", func(t *testing.T) {
		// A dedicated env with a token lacking route_strategies.write; the
		// paged binding scan must reject the delete with the OAuth envelope.
		env := newEnv(t)
		env.seedProviders()
		env.seedSystemAccount("acc-1", "alice")
		token := env.seedDelegatedToken("acc-1", "juhe:groups.write")
		env.seedGroup("grp-1", "acc-1", "g1", "openai", "personal", true)
		env.seedStrategy("rst-1", "acc-1", "s1", "normal", "active")
		env.seedStrategyBinding("rsg-1", "rst-1", "acc-1", "grp-1", 1, 1, "active")
		r := env.do(http.MethodDelete, Prefix+"/groups/grp-1", "", token)
		r.requireOAuthError(t, http.StatusForbidden, insufficientScopeBody,
			`Bearer error="insufficient_scope", scope="juhe:route_strategies.write"`)
	})

	t.Run("binding_scan_across_pages", func(t *testing.T) {
		// The binding lives on the last strategy page: the scan must keep
		// paging (the strategies store clamps pageSize to 1 by default, which
		// makes the multi-page walk deterministic).
		env := newEnv(t)
		env.seedProviders()
		env.seedSystemAccount("acc-1", "alice")
		token := env.seedDelegatedToken("acc-1", "juhe:groups.write")
		env.seedGroup("grp-1", "acc-1", "g1", "openai", "personal", true)
		env.seedGroup("grp-2", "acc-1", "g2", "openai", "personal", true)
		env.seedGroup("grp-3", "acc-1", "g3", "openai", "personal", true)
		env.seedStrategy("rst-1", "acc-1", "s1", "normal", "active")
		env.seedStrategy("rst-2", "acc-1", "s2", "normal", "active")
		env.seedStrategy("rst-3", "acc-1", "s3", "normal", "active")
		env.seedStrategyBinding("rsg-1", "rst-1", "acc-1", "grp-1", 1, 1, "active")
		env.seedStrategyBinding("rsg-2", "rst-2", "acc-1", "grp-2", 1, 1, "active")
		env.seedStrategyBinding("rsg-3", "rst-3", "acc-1", "grp-3", 1, 1, "active")
		r := env.do(http.MethodDelete, Prefix+"/groups/grp-3", "", token)
		r.requireOAuthError(t, http.StatusForbidden, insufficientScopeBody,
			`Bearer error="insufficient_scope", scope="juhe:route_strategies.write"`)
	})
}

// ---------------------------------------------------------------------------
// Route strategies.
// ---------------------------------------------------------------------------

func strategyListKeys(t *testing.T, item map[string]any, wantID, wantName, wantMode, wantStatus string, wantIsDefault bool, wantBindings int) {
	t.Helper()
	want := map[string]any{
		"id": wantID, "name": wantName, "mode": wantMode, "status": wantStatus,
		"isDefault": wantIsDefault, "apiKeyCount": float64(0),
	}
	if len(item) != len(want)+4 { // + bindingCount, groupBindings, createdAt, updatedAt asserted below
		t.Fatalf("strategy dto keys = %v", keysOf(item))
	}
	for key, value := range want {
		if item[key] != value {
			t.Fatalf("strategy dto[%q] = %v, want %v (full %v)", key, item[key], value, item)
		}
	}
	if !numEqual(item["bindingCount"], float64(wantBindings)) {
		t.Fatalf("bindingCount = %v, want %d", item["bindingCount"], wantBindings)
	}
	bindings, ok := item["groupBindings"].([]any)
	if !ok || len(bindings) != wantBindings {
		t.Fatalf("groupBindings = %v, want %d entries", item["groupBindings"], wantBindings)
	}
	for _, key := range []string{"createdAt", "updatedAt"} {
		if _, ok := item[key].(string); !ok {
			t.Fatalf("strategy dto missing %q: %v", key, item)
		}
	}
	for _, forbidden := range []string{"systemAccountId", "normalRoutingConfig"} {
		if _, ok := item[forbidden]; ok {
			t.Fatalf("strategy list dto must not carry %q: %v", forbidden, item)
		}
	}
}

func TestListRouteStrategies(t *testing.T) {
	t.Run("empty_defaults", func(t *testing.T) {
		f := newFixture(t, "juhe:route_strategies.read")
		r := f.env.do(http.MethodGet, Prefix+"/route-strategies", "", f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		// routestrategies.Store clamps a missing pageSize to 1 (0 → 1).
		assertListEnvelope(t, r, 0, false, 1, 1, 0)
	})

	t.Run("paging_and_shape", func(t *testing.T) {
		f := newFixture(t, "juhe:route_strategies.read")
		f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
		f.env.seedStrategy("rst-1", f.accountID, "s1", "normal", "active")
		f.env.seedStrategy("rst-2", f.accountID, "s2", "weighted", "disabled")
		f.env.seedStrategyBinding("rsg-1", "rst-1", f.accountID, "grp-1", 2, 10, "active")

		r := f.env.do(http.MethodGet, Prefix+"/route-strategies?page=1&pageSize=1", "", f.token)
		assertListEnvelope(t, r, 2, true, 1, 1, 1)
		first, _ := r.dataArray(t, "items")[0].(map[string]any)
		if first["id"] != "rst-2" {
			t.Fatalf("first item = %v, want recency-ordered rst-2", first)
		}

		r = f.env.do(http.MethodGet, Prefix+"/route-strategies?page=2&pageSize=1", "", f.token)
		assertListEnvelope(t, r, 2, false, 2, 1, 1)
		item, _ := r.dataArray(t, "items")[0].(map[string]any)
		strategyListKeys(t, item, "rst-1", "s1", "normal", "active", false, 1)
		binding, _ := item["groupBindings"].([]any)[0].(map[string]any)
		// The Node preview Pick (RouteStrategyGroupBindingPreview) carries
		// only id/groupId/groupName/providerCode/status/groupEnabled — no
		// priority/weight keys even though the binding row stores 2/10.
		want := map[string]any{
			"id": "rsg-1", "groupId": "grp-1", "groupName": "g1",
			"providerCode": "openai", "status": "active", "groupEnabled": true,
		}
		if len(binding) != len(want) {
			t.Fatalf("binding preview keys = %v", keysOf(binding))
		}
		for key, value := range want {
			if binding[key] != value {
				t.Fatalf("binding preview[%q] = %v, want %v (full %v)", key, binding[key], value, binding)
			}
		}
		if _, has := binding["priority"]; has {
			t.Fatalf("binding preview must not carry priority: %v", binding)
		}
		if _, has := binding["weight"]; has {
			t.Fatalf("binding preview must not carry weight: %v", binding)
		}
	})

	t.Run("mode_and_status_filters", func(t *testing.T) {
		f := newFixture(t, "juhe:route_strategies.read")
		f.env.seedStrategy("rst-f1", f.accountID, "f1", "failover", "active")
		f.env.seedStrategy("rst-f2", f.accountID, "f2", "normal", "disabled")

		// The list total is filter-aware (mode/status narrow the count).
		r := f.env.do(http.MethodGet, Prefix+"/route-strategies?pageSize=10&mode=failover", "", f.token)
		assertListEnvelope(t, r, 1, false, 1, 10, 1)

		r = f.env.do(http.MethodGet, Prefix+"/route-strategies?pageSize=10&status=disabled", "", f.token)
		assertListEnvelope(t, r, 1, false, 1, 10, 1)

		// Unknown mode/status values are ignored (no filter), mirroring
		// routeStrategyModeQuery/routeStrategyStatusQuery.
		r = f.env.do(http.MethodGet, Prefix+"/route-strategies?pageSize=10&mode=bogus&status=bogus", "", f.token)
		assertListEnvelope(t, r, 2, false, 1, 10, 2)

		r = f.env.do(http.MethodGet, Prefix+"/route-strategies?pageSize=10&keyword=f1", "", f.token)
		assertListEnvelope(t, r, 1, false, 1, 10, 1)
	})
}

func TestGetRouteStrategy(t *testing.T) {
	f := newFixture(t, "juhe:route_strategies.read")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
	f.env.seedStrategy("rst-1", f.accountID, "s1", "normal", "active")
	f.env.seedStrategyBinding("rsg-1", "rst-1", f.accountID, "grp-1", 1, 1, "active")

	t.Run("found", func(t *testing.T) {
		r := f.env.do(http.MethodGet, Prefix+"/route-strategies/rst-1", "", f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		data := r.data(t)
		for _, key := range []string{"id", "name", "mode", "status", "isDefault",
			"normalRoutingConfig", "hybridRoutingConfig", "groupBindings", "apiKeyCount", "createdAt", "updatedAt"} {
			if _, ok := data[key]; !ok {
				t.Fatalf("strategy detail missing %q: %v", key, data)
			}
		}
		// A NULL config_json projects the mode default routing config.
		normal, _ := data["normalRoutingConfig"].(map[string]any)
		if normal["schedulingPreference"] != "cost_first" {
			t.Fatalf("normalRoutingConfig = %v, want the normal-mode default", data["normalRoutingConfig"])
		}
		if data["hybridRoutingConfig"] != nil {
			t.Fatalf("hybridRoutingConfig = %v, want null", data["hybridRoutingConfig"])
		}
		binding, _ := data["groupBindings"].([]any)[0].(map[string]any)
		if binding["groupName"] != "g1" {
			t.Fatalf("binding groupName = %v", binding["groupName"])
		}
	})

	t.Run("missing", func(t *testing.T) {
		r := f.env.do(http.MethodGet, Prefix+"/route-strategies/rst-nope", "", f.token)
		r.requireMessage(t, http.StatusNotFound, "策略路由不存在")
	})
}

func TestCreateRouteStrategy(t *testing.T) {
	f := newFixture(t, "juhe:route_strategies.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)

	t.Run("created_201_with_bindings", func(t *testing.T) {
		// Node: 201 ok(routeStrategyListDto(created)) — the bindings reach the
		// store and the created strategy is echoed with its binding preview.
		r := f.env.do(http.MethodPost, Prefix+"/route-strategies",
			`{"name":"s1","mode":"normal","groupBindings":[{"groupId":"grp-1"}]}`, f.token)
		if r.status != http.StatusCreated {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if _, hasMessage := r.body["message"]; hasMessage {
			t.Fatalf("201 envelope must not carry message: %s", r.raw)
		}
		data := r.data(t)
		if data["name"] != "s1" || data["mode"] != "normal" || data["status"] != "active" || data["isDefault"] != false {
			t.Fatalf("created dto = %v", data)
		}
		bindings, ok := data["groupBindings"].([]any)
		if !ok || len(bindings) != 1 {
			t.Fatalf("groupBindings = %v", data["groupBindings"])
		}
		binding, _ := bindings[0].(map[string]any)
		if binding["groupId"] != "grp-1" || binding["status"] != "active" {
			t.Fatalf("binding = %v", binding)
		}
		if !numEqual(data["bindingCount"], 1) {
			t.Fatalf("bindingCount = %v", data["bindingCount"])
		}
		// The persisted binding row keeps the store default priority (index+1).
		var priority int
		if err := f.env.db.QueryRow(`SELECT priority FROM route_strategy_groups WHERE group_id = 'grp-1'`).Scan(&priority); err != nil {
			t.Fatal(err)
		}
		if priority != 1 {
			t.Fatalf("persisted priority = %d, want 1", priority)
		}
	})

	t.Run("requires_one_binding", func(t *testing.T) {
		r := f.env.do(http.MethodPost, Prefix+"/route-strategies", `{"name":"s"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由至少需要绑定一个分组")
		r = f.env.do(http.MethodPost, Prefix+"/route-strategies", `{"name":"s","groupBindings":null}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由至少需要绑定一个分组")
		// An empty list fails the array length check instead.
		r = f.env.do(http.MethodPost, Prefix+"/route-strategies", `{"name":"s","groupBindings":[]}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由参数无效")
	})

	t.Run("foreign_group_400", func(t *testing.T) {
		other := f.otherOwner()
		f.env.seedGroup("grp-foreign", other, "bob", "openai", "personal", true)
		r := f.env.do(http.MethodPost, Prefix+"/route-strategies",
			`{"name":"s","groupBindings":[{"groupId":"grp-foreign"}]}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由只能绑定自己的分组")
	})

	t.Run("unknown_group_400", func(t *testing.T) {
		r := f.env.do(http.MethodPost, Prefix+"/route-strategies",
			`{"name":"s","groupBindings":[{"groupId":"grp-nope"}]}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由只能绑定自己的分组")
	})

	t.Run("validation_400", func(t *testing.T) {
		cases := []struct{ name, body string }{
			{"empty_object", `{}`},
			{"blank_name", `{"name":"  ","groupBindings":[{"groupId":"grp-1"}]}`},
			{"unknown_field", `{"name":"s","groupBindings":[{"groupId":"grp-1"}],"extra":1}`},
			{"bad_mode", `{"name":"s","mode":"random","groupBindings":[{"groupId":"grp-1"}]}`},
			{"bad_status", `{"name":"s","status":"paused","groupBindings":[{"groupId":"grp-1"}]}`},
			{"description_too_long", `{"name":"s","description":"` + strings.Repeat("长", 201) + `","groupBindings":[{"groupId":"grp-1"}]}`},
			{"bindings_not_list", `{"name":"s","groupBindings":{}}`},
			{"binding_extra_field", `{"name":"s","groupBindings":[{"groupId":"grp-1","extra":1}]}`},
			{"binding_blank_group", `{"name":"s","groupBindings":[{"groupId":" "}]}`},
			{"binding_priority_zero", `{"name":"s","groupBindings":[{"groupId":"grp-1","priority":0}]}`},
			{"binding_priority_fraction", `{"name":"s","groupBindings":[{"groupId":"grp-1","priority":1.5}]}`},
			{"binding_weight_zero", `{"name":"s","groupBindings":[{"groupId":"grp-1","weight":0}]}`},
			{"binding_weight_over", `{"name":"s","groupBindings":[{"groupId":"grp-1","weight":101}]}`},
			{"binding_bad_status", `{"name":"s","groupBindings":[{"groupId":"grp-1","status":"paused"}]}`},
			{"normal_config_not_object", `{"name":"s","groupBindings":[{"groupId":"grp-1"}],"normalRoutingConfig":[]}`},
			{"hybrid_config_not_object", `{"name":"s","groupBindings":[{"groupId":"grp-1"}],"hybridRoutingConfig":"x"}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				r := f.env.do(http.MethodPost, Prefix+"/route-strategies", tc.body, f.token)
				r.requireMessage(t, http.StatusBadRequest, "策略路由参数无效")
			})
		}
	})

	t.Run("too_many_bindings_400", func(t *testing.T) {
		bindings := []string{}
		for i := 0; i < 21; i++ {
			bindings = append(bindings, `{"groupId":"grp-1"}`)
		}
		body := `{"name":"s","groupBindings":[` + strings.Join(bindings, ",") + `]}`
		r := f.env.do(http.MethodPost, Prefix+"/route-strategies", body, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由参数无效")
	})
}

func TestPatchRouteStrategy(t *testing.T) {
	// Each mutation sub-test gets a fresh fixture: a successful patch
	// advances the row updated_at, which would turn the fixed
	// expectedUpdatedAt stamps of later sub-tests into version conflicts.
	stamp := "2026-01-10T08:30:00.000Z"

	newPatchFixture := func(t *testing.T) *fixture {
		f := newFixture(t, "juhe:route_strategies.write")
		f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
		f.env.seedStrategy("rst-1", f.accountID, "s1", "normal", "active")
		f.env.seedStrategyBinding("rsg-1", "rst-1", f.accountID, "grp-1", 1, 1, "active")
		return f
	}

	t.Run("status_only_200", func(t *testing.T) {
		// Node partial schema: status alone patches without name/bindings.
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1",
			`{"status":"disabled","expectedUpdatedAt":"`+stamp+`"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if r.data(t)["status"] != "disabled" {
			t.Fatalf("status = %v", r.data(t)["status"])
		}
	})

	t.Run("name_only_200", func(t *testing.T) {
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1",
			`{"name":"renamed","expectedUpdatedAt":"`+stamp+`"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if r.data(t)["name"] != "renamed" {
			t.Fatalf("name = %v", r.data(t)["name"])
		}
	})

	t.Run("description_only_200", func(t *testing.T) {
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1",
			`{"description":"说明","expectedUpdatedAt":"`+stamp+`"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if r.data(t)["description"] != "说明" {
			t.Fatalf("description = %v", r.data(t)["description"])
		}
	})

	t.Run("disable_with_bindings_200", func(t *testing.T) {
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1",
			`{"name":"s1","groupBindings":[{"groupId":"grp-1"}],"status":"disabled","expectedUpdatedAt":"`+stamp+`"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if r.data(t)["status"] != "disabled" {
			t.Fatalf("status = %v", r.data(t)["status"])
		}
	})

	t.Run("blank_name_400", func(t *testing.T) {
		// A present-but-blank name still fails the partial schema.
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1",
			`{"name":"  ","expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由参数无效")
	})

	t.Run("version_format_400", func(t *testing.T) {
		f := newPatchFixture(t)
		for _, body := range []string{
			`{"name":"x"}`,
			`{"name":"x","expectedUpdatedAt":"nope"}`,
			`{"name":"x","expectedUpdatedAt":1}`,
		} {
			r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1", body, f.token)
			r.requireMessage(t, http.StatusBadRequest, "策略路由配置版本格式不正确")
		}
	})

	t.Run("no_change_400", func(t *testing.T) {
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1", `{"expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "请提供要修改的策略路由内容")
	})

	t.Run("unknown_field_400", func(t *testing.T) {
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1", `{"zzz":1,"expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由参数无效")
	})

	t.Run("missing_404", func(t *testing.T) {
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-nope",
			`{"name":"x","groupBindings":[{"groupId":"grp-1"}],"expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusNotFound, "策略路由不存在")
	})

	t.Run("foreign_binding_400", func(t *testing.T) {
		f := newPatchFixture(t)
		other := f.otherOwner()
		f.env.seedGroup("grp-foreign", other, "bob", "openai", "personal", true)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1",
			`{"name":"s1","groupBindings":[{"groupId":"grp-foreign"}],"expectedUpdatedAt":"`+stamp+`"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "策略路由只能绑定自己的分组")
	})

	t.Run("version_conflict_409_with_current_updated_at", func(t *testing.T) {
		// Node RouteStrategyVersionConflictError → 409 {message,
		// currentUpdatedAt} byte-exact (struct field order mirrors the Node
		// object literal).
		f := newPatchFixture(t)
		r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/rst-1",
			`{"name":"s1","groupBindings":[{"groupId":"grp-1"}],"expectedUpdatedAt":"2020-01-01T00:00:00.000Z"}`, f.token)
		want := `{"message":"策略路由已被其他操作更新，请刷新后重试","currentUpdatedAt":"` + stamp + `"}`
		if r.status != http.StatusConflict {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if string(r.raw) != want {
			t.Fatalf("body = %s, want %s", r.raw, want)
		}
		if got := r.header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
	})
}

func TestDeleteRouteStrategy(t *testing.T) {
	f := newFixture(t, "juhe:route_strategies.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
	f.env.seedStrategy("rst-1", f.accountID, "s1", "normal", "active")
	f.env.seedStrategyBinding("rsg-1", "rst-1", f.accountID, "grp-1", 1, 1, "active")

	t.Run("deleted_204", func(t *testing.T) {
		r := f.env.do(http.MethodDelete, Prefix+"/route-strategies/rst-1", "", f.token)
		if r.status != http.StatusNoContent {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
	})

	t.Run("repeat_404", func(t *testing.T) {
		r := f.env.do(http.MethodDelete, Prefix+"/route-strategies/rst-1", "", f.token)
		r.requireMessage(t, http.StatusNotFound, "策略路由不存在")
	})
}

// ---------------------------------------------------------------------------
// API keys (list-only subset).
// ---------------------------------------------------------------------------

func TestListApiKeys(t *testing.T) {
	t.Run("empty_defaults", func(t *testing.T) {
		// api_keys INNER JOINs route_strategies, so seed one row family.
		f := newFixture(t, "juhe:api_keys.read")
		f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
		f.env.seedStrategy("rst-1", f.accountID, "s1", "normal", "active")
		r := f.env.do(http.MethodGet, Prefix+"/api-keys", "", f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		// apikeys.Store defaults: page 1, pageSize 50.
		assertListEnvelope(t, r, 0, false, 1, 50, 0)
	})

	t.Run("dto_shape_with_strategy", func(t *testing.T) {
		f := newFixture(t, "juhe:api_keys.read")
		f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
		f.env.seedStrategy("rst-1", f.accountID, "s1", "weighted", "active")
		f.env.seedStrategy("rst-2", f.accountID, "s2", "normal", "active")
		f.env.seedApiKey("ak-1", f.accountID, "key-1", "rst-1", "active")
		f.env.seedApiKey("ak-2", f.accountID, "key-2", "rst-2", "disabled")

		r := f.env.do(http.MethodGet, Prefix+"/api-keys?pageSize=1&page=1", "", f.token)
		assertListEnvelope(t, r, 2, true, 1, 1, 1)
		r = f.env.do(http.MethodGet, Prefix+"/api-keys?pageSize=1&page=2", "", f.token)
		assertListEnvelope(t, r, 2, false, 2, 1, 1)

		r = f.env.do(http.MethodGet, Prefix+"/api-keys", "", f.token)
		items := r.dataArray(t, "items")
		byID := map[string]map[string]any{}
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			byID[item["id"].(string)] = item
		}
		first := byID["ak-1"]
		want := map[string]any{
			"id": "ak-1", "name": "key-1", "keyPrefix": "sk-abcd", "keySuffix": "1234",
			"status": "active", "routeStrategyId": "rst-1", "revision": "2026-01-10T08:30:00.000Z",
			"routeStrategyName": "s1", "routeStrategyMode": "weighted", "routeStrategyStatus": "active",
		}
		if len(first) != len(want) {
			t.Fatalf("api key dto keys = %v, want exactly %v", keysOf(first), keysOf(want))
		}
		for key, value := range want {
			if first[key] != value {
				t.Fatalf("api key dto[%q] = %v, want %v", key, first[key], value)
			}
		}
	})

	t.Run("other_owner_hidden", func(t *testing.T) {
		f := newFixture(t, "juhe:api_keys.read")
		other := f.otherOwner()
		f.env.seedGroup("grp-2", other, "g2", "openai", "personal", true)
		f.env.seedStrategy("rst-9", other, "s9", "normal", "active")
		f.env.seedApiKey("ak-foreign", other, "foreign", "rst-9", "active")
		f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
		f.env.seedStrategy("rst-1", f.accountID, "s1", "normal", "active")
		f.env.seedApiKey("ak-mine", f.accountID, "mine", "rst-1", "active")
		r := f.env.do(http.MethodGet, Prefix+"/api-keys", "", f.token)
		assertListEnvelope(t, r, 1, false, 1, 50, 1)
	})
}

// ---------------------------------------------------------------------------
// AI accounts (list + delegated patch subset).
// ---------------------------------------------------------------------------

func aiAccountKeys(t *testing.T, item map[string]any, wantID, wantName, wantStatus string, wantRevision int64) {
	t.Helper()
	want := map[string]any{
		"id": wantID, "providerCode": "openai",
		"providerProtocolProfileId": "ppp_1", "protocolCode": "openai", "protocolVersion": "v1",
		"name": wantName, "type": "api_key", "status": wantStatus,
		"schedulable": true, "priority": float64(0),
		"superPriorityEnabled": false, "fallbackEnabled": false,
		"healthCheckModel": "", "healthCheckEndpointMode": "chat_json",
	}
	// +2 numeric (configRevision/concurrencyLimit below) and +2 array keys
	// (supportedModels/modelMappings, asserted after the loop: Node emits them
	// as [] even without facts).
	if len(item) != len(want)+4 {
		t.Fatalf("ai account dto keys = %v", keysOf(item))
	}
	for key, value := range want {
		if item[key] != value {
			t.Fatalf("ai account dto[%q] = %v, want %v (full %v)", key, item[key], value, item)
		}
	}
	if models, ok := item["supportedModels"].([]any); !ok || len(models) != 0 {
		t.Fatalf("supportedModels = %v, want empty array", item["supportedModels"])
	}
	if mappings, ok := item["modelMappings"].([]any); !ok || len(mappings) != 0 {
		t.Fatalf("modelMappings = %v, want empty array", item["modelMappings"])
	}
	if !numEqual(item["configRevision"], float64(wantRevision)) {
		t.Fatalf("configRevision = %v, want %d", item["configRevision"], wantRevision)
	}
	if !numEqual(item["concurrencyLimit"], 5000) {
		t.Fatalf("concurrencyLimit = %v, want 5000", item["concurrencyLimit"])
	}
}

func TestListAiAccountsOwnerOnly(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newFixture(t, "juhe:ai_accounts.read")
		r := f.env.do(http.MethodGet, Prefix+"/ai-accounts", "", f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		// accounts.Store defaults: page 1, pageSize 50.
		assertListEnvelope(t, r, 0, false, 1, 50, 0)
	})

	t.Run("filters_inherited_instances", func(t *testing.T) {
		// Node isOwnedPhysicalAccount drops rows whose
		// authorizationInstanceSourceAccountId is set, even when the row's
		// accessType is owner (inherited authorization instances).
		f := newFixture(t, "juhe:ai_accounts.read")
		f.env.seedAiAccount("acct-1", f.accountID, "own-1", "active", "")
		f.env.seedAiAccount("acct-2", f.accountID, "inherited-1", "active", "acct-1")
		other := f.otherOwner()
		f.env.seedAiAccount("acct-3", other, "foreign", "active", "")
		f.env.seedAiAccount("acct-4", other, "foreign-inherited", "active", "acct-3")
		r := f.env.do(http.MethodGet, Prefix+"/ai-accounts", "", f.token)
		// hasMore is forced false and total counts the filtered items.
		assertListEnvelope(t, r, 1, false, 1, 50, 1)
		item, _ := r.dataArray(t, "items")[0].(map[string]any)
		aiAccountKeys(t, item, "acct-1", "own-1", "active", 1)
	})
}

func TestPatchAiAccount(t *testing.T) {
	f := newFixture(t, "juhe:ai_accounts.write")
	f.env.seedAiAccount("acct-1", f.accountID, "own-1", "active", "")

	t.Run("rename_200", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1",
			`{"expectedConfigRevision":1,"name":"renamed"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		aiAccountKeys(t, r.data(t), "acct-1", "renamed", "active", 2)
	})

	t.Run("disable_200", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1",
			`{"expectedConfigRevision":2,"status":"disabled"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		aiAccountKeys(t, r.data(t), "acct-1", "renamed", "disabled", 3)
	})

	t.Run("validation_400", func(t *testing.T) {
		cases := []struct{ name, body, want string }{
			{"missing_revision", `{"name":"x"}`, "AI 账户参数无效"},
			{"revision_zero", `{"expectedConfigRevision":0,"name":"x"}`, "AI 账户参数无效"},
			{"revision_fraction", `{"expectedConfigRevision":1.5,"name":"x"}`, "AI 账户参数无效"},
			{"revision_string", `{"expectedConfigRevision":"1","name":"x"}`, "AI 账户参数无效"},
			{"unknown_field", `{"expectedConfigRevision":3,"priority":2}`, "AI 账户参数无效"},
			{"blank_name", `{"expectedConfigRevision":3,"name":"  "}`, "AI 账户参数无效"},
			{"bad_status", `{"expectedConfigRevision":3,"status":"paused"}`, "AI 账户参数无效"},
			{"no_change", `{"expectedConfigRevision":3}`, "请提供要修改的 AI 账户内容"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1", tc.body, f.token)
				r.requireMessage(t, http.StatusBadRequest, tc.want)
			})
		}
	})

	t.Run("missing_404", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-nope",
			`{"expectedConfigRevision":1,"name":"x"}`, f.token)
		r.requireMessage(t, http.StatusNotFound, "AI 账户不存在")
	})

	t.Run("foreign_404", func(t *testing.T) {
		other := f.otherOwner()
		f.env.seedAiAccount("acct-foreign", other, "foreign", "active", "")
		r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-foreign",
			`{"expectedConfigRevision":1,"name":"x"}`, f.token)
		r.requireMessage(t, http.StatusNotFound, "AI 账户不存在")
	})

	t.Run("inherited_instance_404", func(t *testing.T) {
		// The delegated patch owns physical accounts only: inherited
		// authorization instances are invisible even when accessType=owner.
		f := newFixture(t, "juhe:ai_accounts.write")
		f.env.seedAiAccount("acct-1", f.accountID, "own-1", "active", "")
		f.env.seedAiAccount("acct-2", f.accountID, "inherited-1", "active", "acct-1")
		r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-2",
			`{"expectedConfigRevision":1,"name":"x"}`, f.token)
		r.requireMessage(t, http.StatusNotFound, "AI 账户不存在")
	})

	t.Run("stale_revision_conflict_409", func(t *testing.T) {
		// Node renders AccountManagementPatchRevisionConflictError as 409
		// with the shared revision-conflict copy.
		f := newFixture(t, "juhe:ai_accounts.write")
		f.env.seedAiAccount("acct-1", f.accountID, "own-1", "active", "")
		r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1",
			`{"expectedConfigRevision":99,"name":"x"}`, f.token)
		r.requireMessage(t, http.StatusConflict, "账户配置已被其他操作更新，请刷新后重试")
	})
}

// ---------------------------------------------------------------------------
// AI accounts: supportedModels / modelMappings hydration
// (Node aiAccountDto spreads them only when set, delegated-api.routes.ts:638-639;
// the facts come from the account_supported_models / account_model_mappings
// child tables, account-read.repository.ts:553-577).
// ---------------------------------------------------------------------------

func seedAiAccountModelFacts(env *env, accountID string) {
	env.t.Helper()
	stamp := isoMillis(env.clock.Now())
	env.exec(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'openai', 'gpt-4o-mini', ?), (?, 'openai', 'gpt-4o', ?)`,
		accountID, stamp, accountID, stamp)
	env.exec(`INSERT INTO account_model_mappings (account_id, provider_code, source_model,
			source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES (?, 'openai', 'gpt-4o', 'chat_json', 'gpt-4o-2024-08-06', 'chat_json', 1, ?, ?),
			(?, 'openai', 'gpt-4o', 'responses_sse', 'gpt-4o-responses', 'responses_json', 0, ?, ?)`,
		accountID, stamp, stamp, accountID, stamp, stamp)
}

func TestListAiAccountsModelFacts(t *testing.T) {
	f := newFixture(t, "juhe:ai_accounts.read")
	f.env.seedAiAccount("acct-1", f.accountID, "with-models", "active", "")
	f.env.seedAiAccount("acct-2", f.accountID, "no-models", "active", "")
	seedAiAccountModelFacts(f.env, "acct-1")

	r := f.env.do(http.MethodGet, Prefix+"/ai-accounts", "", f.token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	items := r.dataArray(t, "items")
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}

	// acct-1: fields present with the loader ordering (models by model ASC,
	// mappings by source_model, source_endpoint_family ASC) and the JSON
	// mapping shape (sourceModel/.../enabled).
	item := items[0].(map[string]any)
	if item["id"] != "acct-1" {
		t.Fatalf("items[0].id = %v, want acct-1", item["id"])
	}
	models, ok := item["supportedModels"].([]any)
	if !ok {
		t.Fatalf("supportedModels missing: %v", item)
	}
	if len(models) != 2 || models[0] != "gpt-4o" || models[1] != "gpt-4o-mini" {
		t.Fatalf("supportedModels = %v, want [gpt-4o gpt-4o-mini]", models)
	}
	mappings, ok := item["modelMappings"].([]any)
	if !ok {
		t.Fatalf("modelMappings missing: %v", item)
	}
	if len(mappings) != 2 {
		t.Fatalf("len(modelMappings) = %d, want 2", len(mappings))
	}
	first := mappings[0].(map[string]any)
	wantFirst := map[string]any{
		"sourceModel": "gpt-4o", "sourceEndpointFamily": "chat_json",
		"upstreamModel": "gpt-4o-2024-08-06", "upstreamEndpointFamily": "chat_json",
		"enabled": true,
	}
	if len(first) != len(wantFirst) {
		t.Fatalf("mapping keys = %v", keysOf(first))
	}
	for key, want := range wantFirst {
		if first[key] != want {
			t.Fatalf("modelMappings[0][%q] = %v, want %v", key, first[key], want)
		}
	}
	second := mappings[1].(map[string]any)
	if second["sourceEndpointFamily"] != "responses_sse" || second["upstreamModel"] != "gpt-4o-responses" ||
		second["upstreamEndpointFamily"] != "responses_json" || second["enabled"] != false {
		t.Fatalf("modelMappings[1] = %v", second)
	}

	// acct-2: Node still emits both keys as empty arrays ([] is truthy in the
	// JS spread, account-summary.repository.ts:972-973).
	empty := items[1].(map[string]any)
	if empty["id"] != "acct-2" {
		t.Fatalf("items[1].id = %v, want acct-2", empty["id"])
	}
	if models, ok := empty["supportedModels"].([]any); !ok || len(models) != 0 {
		t.Fatalf("supportedModels = %v, want empty array", empty["supportedModels"])
	}
	if mappings, ok := empty["modelMappings"].([]any); !ok || len(mappings) != 0 {
		t.Fatalf("modelMappings = %v, want empty array", empty["modelMappings"])
	}
}

func TestPatchAiAccountResponseCarriesModelFacts(t *testing.T) {
	f := newFixture(t, "juhe:ai_accounts.write")
	f.env.seedAiAccount("acct-1", f.accountID, "own-1", "active", "")
	seedAiAccountModelFacts(f.env, "acct-1")

	r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/acct-1",
		`{"expectedConfigRevision":1,"name":"renamed"}`, f.token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	data := r.data(t)
	models, ok := data["supportedModels"].([]any)
	if !ok || len(models) != 2 || models[0] != "gpt-4o" {
		t.Fatalf("patch response supportedModels = %v (data %v)", data["supportedModels"], data)
	}
	if _, ok := data["modelMappings"].([]any); !ok {
		t.Fatalf("patch response modelMappings missing: %v", data)
	}
}

// ---------------------------------------------------------------------------
// PATCH /api-keys/{id} (api_keys.write): the Node patchApiKeyAsync subset —
// name/status/routeStrategyId with expectedRevision optimistic locking.
// ---------------------------------------------------------------------------

func newApiKeyFixture(t *testing.T) *fixture {
	f := newFixture(t, "juhe:api_keys.write")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
	f.env.seedGroup("grp-2", f.accountID, "g2", "openai", "personal", true)
	f.env.seedStrategy("rst-1", f.accountID, "s1", "weighted", "active")
	f.env.seedStrategy("rst-2", f.accountID, "s2", "normal", "disabled")
	f.env.seedStrategy("rst-3", f.accountID, "s3", "normal", "active")
	f.env.seedApiKey("ak-1", f.accountID, "key-1", "rst-1", "active")
	return f
}

func TestPatchApiKeyRename(t *testing.T) {
	f := newApiKeyFixture(t)
	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"renamed-key"}`, f.token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	// Node renders ok(outcome.result): {id, revision, changedFields, rowPatch}
	// with the microsecond revision rendering (previous + 1ms on the fixed
	// clock).
	want := `{"data":{"id":"ak-1","revision":"2026-01-10T08:30:00.001000Z","changedFields":["name"],` +
		`"rowPatch":{"name":"renamed-key","revision":"2026-01-10T08:30:00.001000Z"}}}`
	if strings.TrimSpace(string(r.raw)) != want {
		t.Fatalf("body = %s, want %s", r.raw, want)
	}
	var stored string
	if err := f.env.db.QueryRow(`SELECT name FROM api_keys WHERE id = 'ak-1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "renamed-key" {
		t.Fatalf("stored name = %q", stored)
	}
}

func TestPatchApiKeyNoChangeKeepsRevision(t *testing.T) {
	f := newApiKeyFixture(t)
	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"key-1"}`, f.token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	want := `{"data":{"id":"ak-1","revision":"2026-01-10T08:30:00.000Z","changedFields":[],"rowPatch":{"revision":"2026-01-10T08:30:00.000Z"}}}`
	if strings.TrimSpace(string(r.raw)) != want {
		t.Fatalf("body = %s, want %s", r.raw, want)
	}
}

func TestPatchApiKeyStatusAndRouteStrategy(t *testing.T) {
	f := newApiKeyFixture(t)
	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","status":"disabled","routeStrategyId":"rst-3"}`, f.token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	data := r.data(t)
	if data["revision"] != "2026-01-10T08:30:00.001000Z" {
		t.Fatalf("revision = %v", data["revision"])
	}
	rowPatch, _ := data["rowPatch"].(map[string]any)
	if rowPatch["status"] != "disabled" || rowPatch["routeStrategyId"] != "rst-3" ||
		rowPatch["routeStrategyName"] != "s3" || rowPatch["routeStrategyMode"] != "normal" ||
		rowPatch["routeStrategyStatus"] != "active" {
		t.Fatalf("rowPatch = %v", rowPatch)
	}
	changed, _ := data["changedFields"].([]any)
	if len(changed) != 2 {
		t.Fatalf("changedFields = %v", changed)
	}
	// Status mutation rebuilds the hourly-window scope binding (quota NULL →
	// delete only).
	var count int
	if err := f.env.db.QueryRow(`SELECT COUNT(1) FROM request_quota_hourly_window_scope_bindings
		WHERE source_type = 'api_key' AND source_id = 'ak-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("scope binding rows = %d, want 0", count)
	}
}

func TestPatchApiKeyValidation(t *testing.T) {
	f := newApiKeyFixture(t)
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		// zod issue copy (no CJK) localizes to the 400 status default.
		{"missing_expectedRevision", `{}`, 400, `{"message":"请求参数无效"}`},
		{"blank_expectedRevision", `{"expectedRevision":"  ","name":"n"}`, 400, `{"message":"请求参数无效"}`},
		{"bad_status", `{"expectedRevision":"r","status":"paused"}`, 400, `{"message":"请求参数无效"}`},
		{"unknown_field", `{"expectedRevision":"r","name":"n","nope":1}`, 400, `{"message":"请求参数无效"}`},
		{"blank_name", `{"expectedRevision":"r","name":""}`, 400, `{"message":"请求参数无效"}`},
		// The refine copy is authored in Chinese and survives localization.
		{"no_change", `{"expectedRevision":"r"}`, 400, `{"message":"请提供要修改的 API Key 内容"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1", tc.body, f.token)
			if r.status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", r.status, tc.wantStatus, r.raw)
			}
			if strings.TrimSpace(string(r.raw)) != tc.wantBody {
				t.Fatalf("body = %s, want %s", r.raw, tc.wantBody)
			}
		})
	}
}

func TestPatchApiKeyGuards(t *testing.T) {
	f := newApiKeyFixture(t)

	t.Run("revision_conflict_409", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2020-01-01T00:00:00.000Z","name":"n"}`, f.token)
		want := `{"message":"API Key 已被其他操作修改，请刷新后重试","currentRevision":"2026-01-10T08:30:00.000Z"}`
		if r.status != http.StatusConflict {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if strings.TrimSpace(string(r.raw)) != want {
			t.Fatalf("body = %s, want %s", r.raw, want)
		}
	})

	t.Run("missing_404", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-nope",
			`{"expectedRevision":"r","name":"n"}`, f.token)
		r.requireMessage(t, http.StatusNotFound, "API Key 不存在")
	})

	t.Run("foreign_404", func(t *testing.T) {
		other := f.otherOwner()
		f.env.seedGroup("grp-f", other, "gf", "openai", "personal", true)
		f.env.seedStrategy("rst-f", other, "sf", "normal", "active")
		f.env.seedApiKey("ak-foreign", other, "foreign", "rst-f", "active")
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-foreign",
			`{"expectedRevision":"r","name":"n"}`, f.token)
		r.requireMessage(t, http.StatusNotFound, "API Key 不存在")
	})

	t.Run("disabled_route_strategy_400", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","routeStrategyId":"rst-2"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "API Key 只能绑定启用状态的策略路由")
	})

	t.Run("unknown_route_strategy_400", func(t *testing.T) {
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","routeStrategyId":"rst-nope"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "API Key 绑定的策略路由不存在或不属于当前用户")
	})

	t.Run("default_key_guards", func(t *testing.T) {
		// is_default=1 purpose=general keys reject renames and strategy moves.
		f := newApiKeyFixture(t)
		f.env.exec(`UPDATE api_keys SET is_default = 1 WHERE id = 'ak-1'`)
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"n"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "默认 API Key 不允许修改名称")
		r = f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","routeStrategyId":"rst-1"}`, f.token)
		// rst-1 equals the current binding → no change → 200.
		if r.status != http.StatusOK {
			t.Fatalf("same strategy status = %d (body %s)", r.status, r.raw)
		}
		r = f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","routeStrategyId":"rst-missing"}`, f.token)
		r.requireMessage(t, http.StatusBadRequest, "默认 API Key 不允许更换策略路由")
	})

	t.Run("duplicate_name_409", func(t *testing.T) {
		f.env.seedApiKey("ak-2", f.accountID, "taken", "rst-1", "active")
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","name":"taken"}`, f.token)
		r.requireMessage(t, http.StatusConflict, "API Key 名称已存在：taken")
	})
}

// ---------------------------------------------------------------------------
// PATCH /api-keys/{id}: committed cache invalidation (Node
// api-key.repository.ts:1256-1271 — validation flush required on
// routeStrategyId/status/expiresAt/quotaLimits with the 500 contract
// delegated-api.routes.ts:365-368; runtime lookup (name) best effort).
// ---------------------------------------------------------------------------

// fakePatchInvalidator records the apikeys.CacheInvalidator calls the
// delegated patch issues; validationErr simulates a failing bus.
type fakePatchInvalidator struct {
	validationCalls []string
	validationHash  [][]string
	runtimeCalls    []string
	quotaCalls      []string
	validationErr   error
}

func (f *fakePatchInvalidator) InvalidateValidation(apiKeyID, reason string, keyHashes []string) error {
	f.validationCalls = append(f.validationCalls, apiKeyID+" "+reason)
	f.validationHash = append(f.validationHash, keyHashes)
	return f.validationErr
}

func (f *fakePatchInvalidator) InvalidateRuntime(apiKeyID, reason string) {
	f.runtimeCalls = append(f.runtimeCalls, apiKeyID+" "+reason)
}

func (f *fakePatchInvalidator) InvalidateQuota(apiKeyID, reason string) {
	f.quotaCalls = append(f.quotaCalls, apiKeyID+" "+reason)
}

const (
	seededKeyRevision = "2026-01-10T08:30:00.000Z"
	seededKeyHash     = "ak-1key-1" // seedApiKey hashes apikeys.HashSecret(id+name)
)

func TestPatchApiKeyCacheInvalidation(t *testing.T) {
	t.Run("route_strategy_change_requires_validation", func(t *testing.T) {
		f := newApiKeyFixture(t)
		inval := &fakePatchInvalidator{}
		f.env.deps.Inval = inval
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"`+seededKeyRevision+`","routeStrategyId":"rst-3"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if len(inval.validationCalls) != 1 || inval.validationCalls[0] != "ak-1 api_key_updated" {
			t.Fatalf("validation calls = %v", inval.validationCalls)
		}
		hashes := inval.validationHash[0]
		if len(hashes) != 1 || hashes[0] != apikeys.HashSecret(seededKeyHash) {
			t.Fatalf("validation key hashes = %v, want [%s]", hashes, apikeys.HashSecret(seededKeyHash))
		}
		if len(inval.runtimeCalls) != 0 || len(inval.quotaCalls) != 0 {
			t.Fatalf("runtime = %v quota = %v, want none", inval.runtimeCalls, inval.quotaCalls)
		}
	})

	t.Run("status_change_requires_validation", func(t *testing.T) {
		f := newApiKeyFixture(t)
		inval := &fakePatchInvalidator{}
		f.env.deps.Inval = inval
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"`+seededKeyRevision+`","status":"disabled"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if len(inval.validationCalls) != 1 || inval.validationCalls[0] != "ak-1 api_key_updated" {
			t.Fatalf("validation calls = %v", inval.validationCalls)
		}
		if len(inval.runtimeCalls) != 0 {
			t.Fatalf("runtime calls = %v, want none", inval.runtimeCalls)
		}
	})

	t.Run("rename_is_runtime_only", func(t *testing.T) {
		f := newApiKeyFixture(t)
		inval := &fakePatchInvalidator{}
		f.env.deps.Inval = inval
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"`+seededKeyRevision+`","name":"renamed-key"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if len(inval.validationCalls) != 0 {
			t.Fatalf("validation calls = %v, want none", inval.validationCalls)
		}
		if len(inval.runtimeCalls) != 1 || inval.runtimeCalls[0] != "ak-1 api_key_updated" {
			t.Fatalf("runtime calls = %v", inval.runtimeCalls)
		}
	})

	t.Run("no_change_skips_invalidation", func(t *testing.T) {
		f := newApiKeyFixture(t)
		inval := &fakePatchInvalidator{}
		f.env.deps.Inval = inval
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"`+seededKeyRevision+`","name":"key-1"}`, f.token)
		if r.status != http.StatusOK {
			t.Fatalf("status = %d (body %s)", r.status, r.raw)
		}
		if len(inval.validationCalls) != 0 || len(inval.runtimeCalls) != 0 || len(inval.quotaCalls) != 0 {
			t.Fatalf("calls = %v %v %v, want none",
				inval.validationCalls, inval.runtimeCalls, inval.quotaCalls)
		}
	})

	t.Run("validation_failure_500", func(t *testing.T) {
		// Node delegated-api.routes.ts:365-368 renders the verbatim 500 copy
		// when the required validation flush fails.
		f := newApiKeyFixture(t)
		inval := &fakePatchInvalidator{validationErr: errors.New("bus down")}
		f.env.deps.Inval = inval
		r := f.env.do(http.MethodPatch, Prefix+"/api-keys/ak-1",
			`{"expectedRevision":"`+seededKeyRevision+`","routeStrategyId":"rst-3"}`, f.token)
		r.requireMessage(t, http.StatusInternalServerError, "API Key 已更新，但 validation cache 失效失败")
		if len(inval.validationCalls) != 1 {
			t.Fatalf("validation calls = %v", inval.validationCalls)
		}
	})
}

// ---------------------------------------------------------------------------
// JSON envelope contract spot check: success envelope carries only data.
// ---------------------------------------------------------------------------

func TestSuccessEnvelopeHasNoMessageField(t *testing.T) {
	f := newFixture(t, "juhe:groups.read")
	f.env.seedGroup("grp-1", f.accountID, "g1", "openai", "personal", true)
	r := f.env.do(http.MethodGet, Prefix+"/groups/grp-1", "", f.token)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(r.raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope["message"]; ok {
		t.Fatalf("success envelope must omit message: %s", r.raw)
	}
	if _, ok := envelope["data"]; !ok {
		t.Fatalf("success envelope missing data: %s", r.raw)
	}
}

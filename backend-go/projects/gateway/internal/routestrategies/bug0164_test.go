// BUG-0164 paired regressions: null-field rejection, read-model integrity,
// options / edit-basic / speed-first-runtime endpoints, authorized group
// binding, scope-query validation, expectedUpdatedAt canonicalization,
// Number() query semantics, UTF-16 description bound, six-field create audit
// log, mutation error status matrix and the PG filter/lock SQL contract.

package routestrategies

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// recordingValidationInvalidator captures validation-cache invalidations and
// can be armed to fail them (Node GatewayApiKeyValidationCacheInvalidationError).
type recordingValidationInvalidator struct {
	mu    sync.Mutex
	alarm bool
}

func (r *recordingValidationInvalidator) InvalidateValidationCache(reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alarm {
		return errors.New("validation cache unavailable")
	}
	return nil
}

func (r *recordingValidationInvalidator) setAlarm(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alarm = on
}

// fakeSpeedFirstFacade records runtime reads and cleanups for the
// speed-first-runtime endpoint and the mutation cleanup hooks.
type fakeSpeedFirstFacade struct {
	mu        sync.Mutex
	items     []SpeedFirstRuntimeItem
	available bool
	failReads bool
	cleared   []string
}

func (f *fakeSpeedFirstFacade) ListDegradedRuntime(_ context.Context, systemAccountID *string, routeStrategyIDs []string) ([]SpeedFirstRuntimeItem, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if systemAccountID != nil {
		if len(routeStrategyIDs) == 0 {
			return nil, f.available, nil
		}
	}
	if f.failReads {
		return nil, false, errors.New("runtime store unavailable")
	}
	return f.items, f.available, nil
}

func (f *fakeSpeedFirstFacade) ClearDegradedRuntime(_ context.Context, routeStrategyID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, routeStrategyID)
	return 1, nil
}

func TestRouteStrategyNullFieldRejection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	// Explicit null on non-nullable fields is a schema failure (zod
	// .optional() without .nullable()).
	code, payload := env.createStrategy(t, path, `{"name":"nm","mode":null,"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由参数无效" {
		t.Fatalf("null mode: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path, `{"name":"ns","status":null,"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由参数无效" {
		t.Fatalf("null status: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path, `{"name":"np","groupBindings":[{"groupId":"`+groupA+`","priority":null}]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由参数无效" {
		t.Fatalf("null binding priority: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path, `{"name":"nw","groupBindings":[{"groupId":"`+groupA+`","weight":null}]}`)
	if code != http.StatusBadRequest || payload["message"] != "分组权重必须是数字" {
		t.Fatalf("null binding weight: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path, `{"name":"nb2","groupBindings":[{"groupId":"`+groupA+`","status":null}]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由参数无效" {
		t.Fatalf("null binding status: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path, `{"name":"ngb","groupBindings":null}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由参数无效" {
		t.Fatalf("null groupBindings: %d %v", code, payload)
	}

	// description:null and top-level config nulls stay accepted (nullable).
	code, created := env.createStrategy(t, path,
		`{"name":"nullable","description":null,"normalRoutingConfig":null,"hybridRoutingConfig":null,"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("nullable fields accepted: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)

	// PATCH null semantics: mode/status null rejected, description null ok.
	updatedAt := env.strategyUpdatedAt(t, strategyID)
	code, payload = env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","mode":null}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由参数无效" {
		t.Fatalf("patch null mode: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","groupBindings":[{"groupId":"`+groupA+`","weight":null}]}`)
	if code != http.StatusBadRequest || payload["message"] != "分组权重必须是数字" {
		t.Fatalf("patch null weight: %d %v", code, payload)
	}
}

func TestRouteStrategyReadModelIntegrity(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, created := env.createStrategy(t, path, `{"name":"integrity","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)

	// Corrupt config_json must fail the read like Node's
	// parseRouteStrategyRuntimeConfigJson, not silently fall back to
	// cost_first defaults.
	env.exec(t, `UPDATE route_strategies SET config_json = '{broken-json' WHERE id = ?`, strategyID)
	code, listPayload := env.do(t, http.MethodGet, path, "")
	if code != http.StatusInternalServerError {
		t.Fatalf("broken config list must 500: %d %v", code, listPayload)
	}
	code, detailPayload := env.do(t, http.MethodGet, path+"/"+strategyID, "")
	if code != http.StatusInternalServerError {
		t.Fatalf("broken config detail must 500: %d %v", code, detailPayload)
	}

	// Invalid normal config sub-items also fail the read.
	env.exec(t, `UPDATE route_strategies SET config_json = ? WHERE id = ?`,
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":9999}}`, strategyID)
	code, _ = env.do(t, http.MethodGet, path, "")
	if code != http.StatusInternalServerError {
		t.Fatalf("invalid normal config list must 500: %d", code)
	}
	// Restore the original stored value (NULL for a cost_first row).
	env.exec(t, `UPDATE route_strategies SET config_json = NULL WHERE id = ?`, strategyID)

	// Unknown stored mode/status values surface the domain error instead of
	// being echoed into a 200 payload; EMPTY values normalize to
	// normal/active exactly like Node's normalizers.
	for _, dirty := range []struct{ column, value string }{
		{"mode", "bogus"},
		{"status", "weird"},
	} {
		original := ""
		if err := env.db.QueryRow(`SELECT `+dirty.column+` FROM route_strategies WHERE id = ?`, strategyID).Scan(&original); err != nil {
			t.Fatal(err)
		}
		env.exec(t, `UPDATE route_strategies SET `+dirty.column+` = ? WHERE id = ?`, dirty.value, strategyID)
		code, payload := env.do(t, http.MethodGet, path, "")
		if code != http.StatusInternalServerError {
			t.Fatalf("dirty %s=%q list must 500: %d %v", dirty.column, dirty.value, code, payload)
		}
		env.exec(t, `UPDATE route_strategies SET `+dirty.column+` = ? WHERE id = ?`, original, strategyID)
	}
	// Empty storage values normalize instead of failing.
	env.exec(t, `UPDATE route_strategies SET mode = '', status = '' WHERE id = ?`, strategyID)
	code, normalized := env.do(t, http.MethodGet, path+"/"+strategyID, "")
	if code != 200 {
		t.Fatalf("empty mode/status must normalize: %d %v", code, normalized)
	}
	normalizedRow := dataMap(t, normalized)
	if normalizedRow["mode"] != "normal" || normalizedRow["status"] != "active" {
		t.Fatalf("empty value normalization: %v", normalizedRow)
	}
	env.exec(t, `UPDATE route_strategies SET mode = 'normal', status = 'active' WHERE id = ?`, strategyID)

	// Binding weight integrity: 0, 101 and non-integer weights fail the read.
	code, hybridCreated := env.createStrategy(t, path,
		`{"name":"weighted","mode":"weighted","groupBindings":[{"groupId":"`+groupA+`","priority":1,"weight":2}]}`)
	if code != http.StatusCreated {
		t.Fatalf("weighted create: %d %v", code, hybridCreated)
	}
	weightedID := dataMap(t, hybridCreated)["id"].(string)
	for _, weight := range []any{0, 101, "abc"} {
		env.exec(t, `UPDATE route_strategy_groups SET weight = ? WHERE route_strategy_id = ?`, weight, weightedID)
		code, _ = env.do(t, http.MethodGet, path, "")
		if code != http.StatusInternalServerError {
			t.Fatalf("dirty weight %v list must 500: %d", weight, code)
		}
		code, detailPayload := env.do(t, http.MethodGet, path+"/"+weightedID, "")
		if code != http.StatusInternalServerError {
			t.Fatalf("dirty weight %v detail must 500: %d %v", weight, code, detailPayload)
		}
	}
	env.exec(t, `UPDATE route_strategy_groups SET weight = 2 WHERE route_strategy_id = ?`, weightedID)
	code, okDetail := env.do(t, http.MethodGet, path+"/"+weightedID, "")
	if code != 200 {
		t.Fatalf("restored detail: %d %v", code, okDetail)
	}
	bindings := dataMap(t, okDetail)["groupBindings"].([]any)
	if bindings[0].(map[string]any)["weight"] != float64(2) {
		t.Fatalf("weight readback: %v", bindings)
	}
}

func TestRouteStrategyOptionsEndpoint(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, first := env.createStrategy(t, path, `{"name":"Default One","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create 1: %d %v", code, first)
	}
	id1 := dataMap(t, first)["id"].(string)
	code, second := env.createStrategy(t, path, `{"name":"ZZ Last","status":"disabled","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create 2: %d %v", code, second)
	}
	id2 := dataMap(t, second)["id"].(string)
	env.exec(t, `UPDATE route_strategies SET is_default = 1 WHERE id = ?`, id1)

	// Default: activeOnly, default limit, is_default DESC then updated_at
	// DESC ordering; owner fields present for admins.
	code, optionsPayload := env.do(t, http.MethodGet, path+"/options", "")
	if code != 200 {
		t.Fatalf("options: %d %v", code, optionsPayload)
	}
	optionsList, ok := optionsPayload["data"].([]any)
	if !ok || len(optionsList) != 1 {
		t.Fatalf("activeOnly must hide the disabled strategy: %v", optionsPayload)
	}
	option := optionsList[0].(map[string]any)
	if option["id"] != id1 || option["isDefault"] != true || option["systemAccountId"] != adminID ||
		option["systemAccountName"] == nil || option["mode"] != "normal" || option["status"] != "active" {
		t.Fatalf("option shape: %v", option)
	}

	// activeOnly=false surfaces both, is_default first.
	code, optionsPayload = env.do(t, http.MethodGet, path+"/options?activeOnly=false", "")
	optionsList = optionsPayload["data"].([]any)
	if code != 200 || len(optionsList) != 2 || optionsList[0].(map[string]any)["id"] != id1 ||
		optionsList[1].(map[string]any)["id"] != id2 {
		t.Fatalf("activeOnly=false ordering: %d %v", code, optionsPayload)
	}

	// ids filter + keyword respecting activeOnly.
	code, optionsPayload = env.do(t, http.MethodGet, path+"/options?activeOnly=false&ids="+id2, "")
	optionsList = optionsPayload["data"].([]any)
	if code != 200 || len(optionsList) != 1 || optionsList[0].(map[string]any)["id"] != id2 {
		t.Fatalf("ids filter: %d %v", code, optionsPayload)
	}
	code, optionsPayload = env.do(t, http.MethodGet, path+"/options?keyword=ZZ", "")
	optionsList = optionsPayload["data"].([]any)
	if code != 200 || len(optionsList) != 0 {
		t.Fatalf("keyword must respect activeOnly: %d %v", code, optionsPayload)
	}
	code, optionsPayload = env.do(t, http.MethodGet, path+"/options?keyword=Default", "")
	optionsList = optionsPayload["data"].([]any)
	if code != 200 || len(optionsList) != 1 {
		t.Fatalf("keyword filter: %d %v", code, optionsPayload)
	}

	// limit accepts Number() semantics.
	code, optionsPayload = env.do(t, http.MethodGet, path+"/options?limit=2&activeOnly=false", "")
	if code != 200 || len(optionsPayload["data"].([]any)) != 2 {
		t.Fatalf("limit 2: %d %v", code, optionsPayload)
	}
	code, optionsPayload = env.do(t, http.MethodGet, path+"/options?limit=1.0&activeOnly=false", "")
	if code != 200 || len(optionsPayload["data"].([]any)) != 1 {
		t.Fatalf("limit 1.0 must parse: %d %v", code, optionsPayload)
	}

	// Self mirror is scoped to the caller and omits owner fields.
	carolID := env.login(t, "carol", "carol-pass", "user")
	code, myOptions := env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/options", "")
	if code != 200 || len(myOptions["data"].([]any)) != 0 {
		t.Fatalf("my options start empty: %d %v", code, myOptions)
	}
	myGroup := env.createGroup(t, carolID, "carol-group", true)
	code, mine := env.createStrategy(t, "/__aisys__/api/my-route-strategies", `{"name":"mine","groupBindings":[`+bindingJSON(myGroup, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("my create: %d %v", code, mine)
	}
	myID := dataMap(t, mine)["id"].(string)
	code, myOptions = env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/options", "")
	optionsList = myOptions["data"].([]any)
	if code != 200 || len(optionsList) != 1 || optionsList[0].(map[string]any)["id"] != myID {
		t.Fatalf("my options: %d %v", code, myOptions)
	}
	if optionsList[0].(map[string]any)["systemAccountId"] != nil {
		t.Fatalf("self options must omit owner fields: %v", optionsList[0])
	}
}

func TestRouteStrategyEditBasicEndpoint(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, created := env.createStrategy(t, path,
		`{"name":"editbasic","description":"desc","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)

	code, editPayload := env.do(t, http.MethodGet, path+"/"+strategyID+"/edit-basic", "")
	if code != 200 {
		t.Fatalf("edit-basic: %d %v", code, editPayload)
	}
	edit := dataMap(t, editPayload)
	if edit["id"] != strategyID || edit["name"] != "editbasic" || edit["description"] != "desc" ||
		edit["mode"] != "normal" || edit["status"] != "active" || edit["isDefault"] != false ||
		edit["updatedAt"] == nil || edit["systemAccountName"] != nil {
		t.Fatalf("edit-basic projection: %v", edit)
	}
	bindings, ok := edit["groupBindings"].([]any)
	if !ok || len(bindings) != 1 || bindings[0].(map[string]any)["groupId"] != groupA {
		t.Fatalf("edit-basic bindings: %v", edit)
	}
	if _, hasNormal := edit["normalRoutingConfig"]; !hasNormal {
		t.Fatalf("normal mode must project normalRoutingConfig: %v", edit)
	}

	// Unknown id → 404 策略路由不存在.
	code, missing := env.do(t, http.MethodGet, path+"/missing/edit-basic", "")
	if code != http.StatusNotFound || missing["message"] != "策略路由不存在" {
		t.Fatalf("missing edit-basic: %d %v", code, missing)
	}

	// Blank scope query → 400 before any business query.
	code, blank := env.do(t, http.MethodGet, path+"/"+strategyID+"/edit-basic?systemAccountId=", "")
	if code != http.StatusBadRequest || blank["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope edit-basic: %d %v", code, blank)
	}

	// Self mirror: cross-owner rows invisible; owner rows omit
	// systemAccountId.
	daveID := env.login(t, "dave", "dave-pass", "user")
	code, forbidden := env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/"+strategyID+"/edit-basic", "")
	if code != http.StatusNotFound {
		t.Fatalf("cross-owner edit-basic must 404: %d %v", code, forbidden)
	}
	myGroup := env.createGroup(t, daveID, "dave-group", true)
	code, mine := env.createStrategy(t, "/__aisys__/api/my-route-strategies", `{"name":"dave","groupBindings":[`+bindingJSON(myGroup, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("my create: %d %v", code, mine)
	}
	myID := dataMap(t, mine)["id"].(string)
	code, myEdit := env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/"+myID+"/edit-basic", "")
	if code != 200 {
		t.Fatalf("my edit-basic: %d %v", code, myEdit)
	}
	if dataMap(t, myEdit)["systemAccountId"] != nil {
		t.Fatalf("self edit-basic must omit systemAccountId: %v", myEdit)
	}
}

func TestRouteStrategySpeedFirstRuntimeEndpoint(t *testing.T) {
	facade := &fakeSpeedFirstFacade{available: true}
	env := newTestEnvFull(t, facade)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, weighted := env.createStrategy(t, path, `{"name":"wr","mode":"weighted","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("weighted create: %d %v", code, weighted)
	}
	weightedID := dataMap(t, weighted)["id"].(string)

	// Non-speed-first strategies render the static disabled payload.
	code, runtimePayload := env.do(t, http.MethodGet, path+"/"+weightedID+"/speed-first-runtime", "")
	if code != 200 {
		t.Fatalf("weighted runtime: %d %v", code, runtimePayload)
	}
	runtime := dataMap(t, runtimePayload)
	if runtime["routeStrategyId"] != weightedID || runtime["enabled"] != false ||
		runtime["runtimeAvailable"] != true || runtime["degradedCount"] != float64(0) ||
		runtime["generatedAt"] == nil {
		t.Fatalf("disabled runtime payload: %v", runtime)
	}
	if runtimeItems, ok := runtime["items"].([]any); !ok || len(runtimeItems) != 0 {
		t.Fatalf("disabled runtime items: %v", runtime["items"])
	}

	code, speed := env.createStrategy(t, path,
		`{"name":"sf","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000},"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("speed create: %d %v", code, speed)
	}
	speedID := dataMap(t, speed)["id"].(string)

	facade.mu.Lock()
	facade.items = []SpeedFirstRuntimeItem{{
		AccountID:     "acc-1",
		Scope:         runtimeScope{RouteStrategyID: speedID, GroupID: groupA},
		SlowCount:     3,
		DegradedUntil: "2026-01-01T00:05:00.000Z",
		Reason:        "slow_first_byte",
	}, {
		AccountID:     "acc-1",
		Scope:         runtimeScope{RouteStrategyID: speedID, GroupID: groupA},
		DegradedUntil: "2026-01-01T00:09:00.000Z",
		Reason:        "slow_first_byte",
	}}
	facade.mu.Unlock()

	code, runtimePayload = env.do(t, http.MethodGet, path+"/"+speedID+"/speed-first-runtime", "")
	if code != 200 {
		t.Fatalf("speed runtime: %d %v", code, runtimePayload)
	}
	runtime = dataMap(t, runtimePayload)
	if runtime["enabled"] != true || runtime["runtimeAvailable"] != true || runtime["degradedCount"] != float64(1) {
		t.Fatalf("enabled runtime payload: %v", runtime)
	}
	runtimeItems, _ := runtime["items"].([]any)
	if len(runtimeItems) != 1 {
		t.Fatalf("runtime items must dedupe by account: %v", runtimeItems)
	}
	item := runtimeItems[0].(map[string]any)
	if item["accountId"] != "acc-1" || item["degradedUntil"] != "2026-01-01T00:09:00.000Z" {
		t.Fatalf("deduped item: %v", item)
	}
	if scope, ok := item["scope"].(map[string]any); !ok || scope["routeStrategyId"] != speedID {
		t.Fatalf("item scope: %v", item["scope"])
	}

	// Facade unavailable → runtimeAvailable:false, request still 200.
	facade.mu.Lock()
	facade.failReads = true
	facade.mu.Unlock()
	code, runtimePayload = env.do(t, http.MethodGet, path+"/"+speedID+"/speed-first-runtime", "")
	if code != 200 {
		t.Fatalf("unavailable runtime status: %d %v", code, runtimePayload)
	}
	runtime = dataMap(t, runtimePayload)
	if runtime["enabled"] != true || runtime["runtimeAvailable"] != false || runtime["degradedCount"] != float64(0) {
		t.Fatalf("unavailable runtime payload: %v", runtime)
	}
	facade.mu.Lock()
	facade.failReads = false
	facade.mu.Unlock()

	// PATCH touching only the description must not clear the runtime.
	updatedAt := env.strategyUpdatedAt(t, speedID)
	code, patched := env.do(t, http.MethodPatch, path+"/"+speedID,
		`{"expectedUpdatedAt":"`+updatedAt+`","description":"renamed-sf"}`)
	if code != 200 {
		t.Fatalf("rename patch: %d %v", code, patched)
	}
	facade.mu.Lock()
	clearedAfterDescription := len(facade.cleared)
	facade.mu.Unlock()
	if clearedAfterDescription != 0 {
		t.Fatalf("description-only patch must not clear runtime: %v", facade.cleared)
	}

	// PATCH touching normalRoutingConfig triggers the cleanup hook.
	updatedAt = env.strategyUpdatedAt(t, speedID)
	code, patched = env.do(t, http.MethodPatch, path+"/"+speedID,
		`{"expectedUpdatedAt":"`+updatedAt+`","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000}}`)
	if code != 200 {
		t.Fatalf("config patch: %d %v", code, patched)
	}
	facade.mu.Lock()
	clearedConfig := append([]string{}, facade.cleared...)
	facade.mu.Unlock()
	if len(clearedConfig) != 1 || clearedConfig[0] != speedID {
		t.Fatalf("normalRoutingConfig patch must clear runtime: %v", clearedConfig)
	}

	// DELETE clears the runtime too.
	code, _ = env.do(t, http.MethodDelete, path+"/"+weightedID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	facade.mu.Lock()
	clearedDelete := append([]string{}, facade.cleared...)
	facade.mu.Unlock()
	if len(clearedDelete) != 2 || clearedDelete[1] != weightedID {
		t.Fatalf("delete must clear runtime: %v", clearedDelete)
	}

	// List enrichment marks speed-first rows only.
	code, listPayload := env.do(t, http.MethodGet, path, "")
	if code != 200 {
		t.Fatalf("list: %d %v", code, listPayload)
	}
	for _, raw := range items(t, listPayload) {
		listItem := raw.(map[string]any)
		summary, has := listItem["speedFirstLatencyRuntime"]
		if listItem["id"] == speedID {
			if !has || summary.(map[string]any)["runtimeAvailable"] != true {
				t.Fatalf("speed-first list enrichment: %v", listItem)
			}
		} else if has {
			t.Fatalf("non-speed-first rows must not carry runtime summary: %v", listItem)
		}
	}

	// 404 for unknown ids.
	code, missing := env.do(t, http.MethodGet, path+"/missing/speed-first-runtime", "")
	if code != http.StatusNotFound || missing["message"] != "策略路由不存在" {
		t.Fatalf("missing runtime: %d %v", code, missing)
	}
}

func TestRouteStrategyAuthorizedGroupBinding(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	aliceID := env.login(t, "alice2", "alice-pass", "user")
	myPath := "/__aisys__/api/my-route-strategies"

	// Without a grant alice cannot bind the admin-owned group.
	code, denied := env.createStrategy(t, myPath, `{"name":"no-grant","groupBindings":[{"groupId":"`+groupA+`"}]}`)
	if code != http.StatusBadRequest || denied["message"] != "策略路由只能绑定自己的分组或有效授权给自己的分组" {
		t.Fatalf("unauthorized bind must fail: %d %v", code, denied)
	}

	// Active grant without expiry makes the group bindable.
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status, expires_at, created_at, updated_at)
		VALUES ('auth-active', 'group', ?, ?, 'active', NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, groupA, aliceID)
	code, granted := env.createStrategy(t, myPath, `{"name":"with-grant","groupBindings":[{"groupId":"`+groupA+`"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("authorized bind: %d %v", code, granted)
	}
	grantedID := dataMap(t, granted)["id"].(string)
	code, detail := env.do(t, http.MethodGet, myPath+"/"+grantedID, "")
	bindings := dataMap(t, detail)["groupBindings"].([]any)
	if code != 200 || bindings[0].(map[string]any)["groupEnabled"] != true {
		t.Fatalf("authorized binding read: %d %v", code, detail)
	}

	// Settings disabled + disabled binding is accepted (can_bind ignores the
	// settings enabled flag) but the row reads groupEnabled=false. Weighted
	// mode carries the required own active binding alongside the disabled
	// authorized one.
	env.exec(t, `INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, created_at, updated_at)
		VALUES ('auth-active', ?, ?, 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, aliceID, groupA)
	aliceGroup := env.createGroup(t, aliceID, "alice-own", true)
	code, disabledBind := env.createStrategy(t, myPath, `{"name":"settings-off","mode":"weighted","groupBindings":[{"groupId":"`+aliceGroup+`","priority":1},{"groupId":"`+groupA+`","priority":2,"status":"disabled"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("disabled bind with settings off: %d %v", code, disabledBind)
	}
	// Active binding with settings disabled is refused (group_enabled = 0).
	code, activeRefused := env.createStrategy(t, myPath, `{"name":"settings-off-active","groupBindings":[{"groupId":"`+groupA+`"}]}`)
	if code != http.StatusBadRequest || activeRefused["message"] != "策略路由不能启用已停用分组：alpha" {
		t.Fatalf("active bind with settings off: %d %v", code, activeRefused)
	}
	disabledID := dataMap(t, disabledBind)["id"].(string)
	code, disabledDetail := env.do(t, http.MethodGet, myPath+"/"+disabledID, "")
	disabledBindings := dataMap(t, disabledDetail)["groupBindings"].([]any)
	if code != 200 || len(disabledBindings) != 2 {
		t.Fatalf("weighted binding read: %d %v", code, disabledDetail)
	}
	disabledAuthorized := disabledBindings[1].(map[string]any)
	if disabledAuthorized["groupId"] != groupA || disabledAuthorized["groupEnabled"] != false {
		t.Fatalf("settings-off groupEnabled: %v", disabledBindings)
	}

	// Expired grants stop binding again.
	env.exec(t, `DELETE FROM group_authorization_settings WHERE authorization_id = 'auth-active'`)
	env.exec(t, `DELETE FROM resource_authorizations WHERE id = 'auth-active'`)
	code, expired := env.createStrategy(t, myPath, `{"name":"expired","groupBindings":[{"groupId":"`+groupA+`"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expired grant must refuse: %d %v", code, expired)
	}
}

func TestRouteStrategyScopeQueryValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, created := env.createStrategy(t, path, `{"name":"scoped","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)
	updatedAt := env.strategyUpdatedAt(t, strategyID)

	// parseRequestScopeQuery: present-but-blank systemAccountId → 400 on the
	// detail / patch / delete / create surfaces before business queries.
	code, payload := env.do(t, http.MethodGet, path+"/"+strategyID+"?systemAccountId=", "")
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope detail: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, path+"/"+strategyID+"?systemAccountId=%20",
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"renamed"}`)
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope patch: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodDelete, path+"/"+strategyID+"?systemAccountId=", "")
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope delete: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, path+"?systemAccountId=", `{"name":"x","groupBindings":[{"groupId":"`+groupA+`"}]}`)
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope create: %d %v", code, payload)
	}
	// Self surface validates the same way.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/"+strategyID+"?systemAccountId=", "")
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope my detail: %d %v", code, payload)
	}

	// The "all" admin filter keeps working; rejected mutations must not touch
	// the row.
	code, _ = env.do(t, http.MethodGet, path+"?systemAccountId=all", "")
	if code != 200 {
		t.Fatalf("all scope: %d", code)
	}
	if env.strategyUpdatedAt(t, strategyID) != updatedAt {
		t.Fatal("rejected mutations must not touch the row")
	}
}

func TestRouteStrategyExpectedUpdatedAtCanonicalization(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, created := env.createStrategy(t, path, `{"name":"versioned","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)

	// Equivalent RFC3339 representations must CAS against the stored UTC
	// millisecond version exactly like the Node canonicalization.
	parsed, err := time.Parse(time.RFC3339Nano, env.strategyUpdatedAt(t, strategyID))
	if err != nil {
		t.Fatal(err)
	}
	offsetVersion := parsed.UTC().Add(8 * time.Hour).Format("2006-01-02T15:04:05.000+08:00")
	code, patched := env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+offsetVersion+`","name":"via-offset"}`)
	if code != 200 {
		t.Fatalf("offset version patch: %d %v", code, patched)
	}

	// Extra fractional digits truncate to the stored millisecond version.
	nextVersion := env.strategyUpdatedAt(t, strategyID)
	parsed, err = time.Parse(time.RFC3339Nano, nextVersion)
	if err != nil {
		t.Fatal(err)
	}
	padded := parsed.UTC().Format("2006-01-02T15:04:05.999999999Z")
	code, patched = env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+padded+`","name":"via-fraction"}`)
	if code != 200 {
		t.Fatalf("fraction version patch: %d %v", code, patched)
	}

	// Timezone-less values stay rejected.
	currentVersion := env.strategyUpdatedAt(t, strategyID)
	naive := strings.Split(currentVersion, ".")[0]
	code, rejected := env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+naive+`","name":"naive"}`)
	if code != http.StatusBadRequest || rejected["message"] != "策略路由配置版本格式不正确" {
		t.Fatalf("naive version: %d %v", code, rejected)
	}
}

func TestRouteStrategyListQueryNumberSemantics(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"
	for _, name := range []string{"q1", "q2", "q3"} {
		code, created := env.createStrategy(t, path, `{"name":"`+name+`","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, created)
		}
	}

	// integerQueryValue accepts scientific notation and trailing-zero
	// decimals (JavaScript Number semantics).
	code, payload := env.do(t, http.MethodGet, path+"?pageSize=1e2", "")
	if code != 200 || dataMap(t, payload)["pageSize"] != float64(100) {
		t.Fatalf("pageSize 1e2: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, path+"?pageSize=2.0", "")
	if code != 200 || dataMap(t, payload)["pageSize"] != float64(2) || dataMap(t, payload)["total"] != float64(3) {
		t.Fatalf("pageSize 2.0: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, path+"?page=2.0&pageSize=2", "")
	page2 := dataMap(t, payload)
	if code != 200 || page2["page"] != float64(2) || len(items(t, payload)) != 1 {
		t.Fatalf("page 2.0: %d %v", code, payload)
	}
	// Non-integer or non-numeric text falls back to the defaults.
	code, payload = env.do(t, http.MethodGet, path+"?pageSize=abc&page=xyz", "")
	if code != 200 || dataMap(t, payload)["pageSize"] != float64(50) || dataMap(t, payload)["page"] != float64(1) {
		t.Fatalf("fallback defaults: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, path+"?pageSize=2.5", "")
	if code != 200 || dataMap(t, payload)["pageSize"] != float64(50) {
		t.Fatalf("non-integer pageSize must fall back: %d %v", code, payload)
	}
}

func TestRouteStrategyDescriptionUTF16Boundary(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	// 100 astral emoji = 200 UTF-16 code units → accepted (rune count is
	// only 100).
	exactly200 := strings.Repeat("\U0001F600", 100)
	code, created := env.createStrategy(t, path,
		`{"name":"utf16","description":"`+exactly200+`","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("200 code-unit description must pass: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)

	// 101 emoji = 202 UTF-16 code units → rejected even though it is only 101
	// runes.
	tooLong := strings.Repeat("\U0001F600", 101)
	code, rejected := env.createStrategy(t, path,
		`{"name":"utf16b","description":"`+tooLong+`","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || rejected["message"] != "策略路由说明不能超过 200 个字符" {
		t.Fatalf("202 code-unit description must fail: %d %v", code, rejected)
	}
	// PATCH path enforces the same bound.
	updatedAt := env.strategyUpdatedAt(t, strategyID)
	code, rejected = env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","description":"`+tooLong+`"}`)
	if code != http.StatusBadRequest || rejected["message"] != "策略路由说明不能超过 200 个字符" {
		t.Fatalf("patch description bound: %d %v", code, rejected)
	}
}

func TestRouteStrategyCreateOperationLogSixFields(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	// cost_first normal create without hybrid config still logs all six
	// audit fields, with bindings rendered presence-only.
	code, created := env.createStrategy(t, path,
		`{"name":"audited","groupBindings":[{"groupId":"`+groupA+`"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	entries := env.sink.entries
	var createEntry *authsys.OperationLogEntry
	for index := range entries {
		if entries[index].OperationKey == "route_strategies.create" {
			createEntry = &entries[index]
		}
	}
	if createEntry == nil {
		t.Fatalf("create log missing: %v", env.sink.keys())
	}
	if len(createEntry.Changes) != 6 {
		t.Fatalf("create must log exactly six changes: %d (%+v)", len(createEntry.Changes), createEntry.Changes)
	}
	wantFields := []string{"name", "mode", "status", "groupBindings", "normalRoutingConfig", "hybridRoutingConfig"}
	for index, field := range wantFields {
		if createEntry.Changes[index].Field != field {
			t.Fatalf("change order %d: got %s want %s", index, createEntry.Changes[index].Field, field)
		}
	}
	// normalRoutingConfig keeps the cost_first projection.
	if createEntry.Changes[4].After == "" || !strings.Contains(createEntry.Changes[4].After, "cost_first") {
		t.Fatalf("normalRoutingConfig change: %q", createEntry.Changes[4].After)
	}
	// hybridRoutingConfig absent → empty audit value.
	if createEntry.Changes[5].After != "" {
		t.Fatalf("hybridRoutingConfig must render absent: %q", createEntry.Changes[5].After)
	}
	// groupBindings logs only what the caller sent (no defaulted
	// priority/status/weight keys).
	if createEntry.Changes[3].After != `[{"groupId":"`+groupA+`"}]` {
		t.Fatalf("groupBindings change: %q", createEntry.Changes[3].After)
	}

	// hybrid create logs the raw hybrid input and provided binding fields.
	code, hybrid := env.createStrategy(t, path,
		`{"name":"audited-hybrid","mode":"hybrid_smart","hybridRoutingConfig":`+hybridConfigBody(5, "model-high")+`,"groupBindings":[{"groupId":"`+groupA+`","priority":3,"weight":7,"status":"active"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("hybrid create: %d %v", code, hybrid)
	}
	entries = env.sink.entries
	createEntry = nil
	for index := range entries {
		if entries[index].OperationKey == "route_strategies.create" && entries[index].ResourceName == "audited-hybrid" {
			createEntry = &entries[index]
		}
	}
	if createEntry == nil {
		t.Fatal("hybrid create log missing")
	}
	if !strings.Contains(createEntry.Changes[5].After, "model-high") {
		t.Fatalf("hybridRoutingConfig change: %q", createEntry.Changes[5].After)
	}
	if createEntry.Changes[3].After != `[{"groupId":"`+groupA+`","priority":3,"weight":7,"status":"active"}]` {
		t.Fatalf("groupBindings raw change: %q", createEntry.Changes[3].After)
	}
}

func TestRouteStrategyMutationErrorStatusMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, created := env.createStrategy(t, path, `{"name":"failing","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)
	updatedAt := env.strategyUpdatedAt(t, strategyID)

	// Validation-cache invalidation failure → 500 with the Node message even
	// though the row was committed. A runtime-relevant field must change
	// (name-only patches never fire the invalidation, like Node).
	env.validationInval.setAlarm(true)
	code, patched := env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","status":"disabled"}`)
	if code != http.StatusInternalServerError || patched["message"] != "策略路由已更新，但 API Key validation cache 失效失败" {
		t.Fatalf("validation cache failure: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM route_strategies WHERE id = ? AND status = 'disabled'`, strategyID) != 1 {
		t.Fatal("the row must stay committed after the invalidation failure")
	}
	env.validationInval.setAlarm(false)
	// Successful runtime-relevant patch fires the validation invalidation
	// without failing.
	updatedVersion := env.strategyUpdatedAt(t, strategyID)
	code, patched = env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedVersion+`","status":"active"}`)
	if code != 200 {
		t.Fatalf("healthy patch: %d %v", code, patched)
	}

	// Unclassified store failures render the Node catch-all 400 (not 500):
	// an error without a typed category maps to badRequest(message). The
	// auth middleware owns its own failure contract, so the mapping is
	// exercised at the store→route boundary like the Node repository→route
	// pair.
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, createErr := env.store.Create(context.Background(), MutationInput{
		Name:        ptrString("closed"),
		HasBindings: true,
		Bindings:    []BindingInput{{GroupID: groupA, Priority: intPtr(1), Status: "active"}},
	}, AccessScope{ViewerID: adminID})
	var validationErr *ValidationError
	var conflictErr *ConflictError
	var versionErr *VersionConflictError
	if createErr == nil ||
		errorsAs(createErr, &validationErr) || errorsAs(createErr, &conflictErr) || errorsAs(createErr, &versionErr) {
		t.Fatalf("broken store must surface an unclassified error: %v", createErr)
	}
	recorder := httptest.NewRecorder()
	(&Deps{}).writeMutationError(recorder, createErr)
	// Non-CJK driver messages localize to the 400 status default exactly like
	// Node's localizeSystemErrorMessage.
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "请求参数无效") {
		t.Fatalf("unclassified mutation error must render localized 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	(&Deps{}).writeMutationError(recorder, &ValidationError{Message: "策略路由名称不能为空"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("validation error must render 400: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	(&Deps{}).writeMutationError(recorder, &ConflictError{Message: "策略路由名称已存在：x"})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate name must render 409: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	(&Deps{}).writeMutationError(recorder, &VersionConflictError{Message: "策略路由已被其他操作更新，请刷新后重试", CurrentUpdatedAt: "2026-01-01T00:00:00.000Z"})
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "currentUpdatedAt") {
		t.Fatalf("version conflict must render 409 + currentUpdatedAt: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	(&Deps{}).writeMutationError(recorder, &ValidationCacheInvalidationError{Message: "策略路由已更新，但 API Key validation cache 失效失败"})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("validation cache failure must render 500: %d", recorder.Code)
	}
}

func TestRouteStrategyPGFilterSQLContract(t *testing.T) {
	sqliteStore := &Store{}
	clause, args := sqliteStore.keywordFilter("Alpha")
	if clause != "(route_strategies.name >= ? AND route_strategies.name < ?)" || len(args) != 2 {
		t.Fatalf("sqlite keyword filter: %q %v", clause, args)
	}
	pgStore := &Store{pg: true}
	clause, args = pgStore.keywordFilter("Alpha")
	if clause != `(route_strategies.name COLLATE "C" >= ? AND route_strategies.name COLLATE "C" < ?)` || len(args) != 2 {
		t.Fatalf("pg keyword filter must pin the C collation: %q %v", clause, args)
	}
	// The bindable-groups query carries the authorization joins and, on
	// PostgreSQL create/patch, the Node FOR UPDATE OF groups lock.
	lockQuery := pgStore.bindableGroupsQuery([]string{"?"}, true)
	if !strings.Contains(lockQuery, "FOR UPDATE OF groups") {
		t.Fatalf("pg locked bindable groups query missing FOR UPDATE OF groups: %s", lockQuery)
	}
	if strings.Contains(pgStore.bindableGroupsQuery([]string{"?"}, false), "FOR UPDATE") {
		t.Fatal("unlocked bindable groups query must not lock")
	}
	if !strings.Contains(lockQuery, "resource_authorizations") || !strings.Contains(lockQuery, "group_authorization_settings") {
		t.Fatalf("bindable groups query must join authorization tables: %s", lockQuery)
	}
	bindingQuery := pgStore.bindingRowColumns() + " " + pgStore.bindingRowFrom()
	if !strings.Contains(bindingQuery, "resource_authorizations") || !strings.Contains(bindingQuery, "COALESCE(group_authorization_settings.enabled, 1)") {
		t.Fatalf("binding rows must carry the authorized group_enabled branch: %s", bindingQuery)
	}
	// UTF-16 length helper parity with String.length.
	if utf16CodeUnits("\U0001F600") != 2 || utf16CodeUnits("aé") != 2 {
		t.Fatal("utf16 code unit counting")
	}
	// integerQueryValue mirrors Number()/Number.isInteger.
	if value, ok := intQueryValue(" 1e2 "); !ok || value != 100 {
		t.Fatalf("intQueryValue 1e2: %d %v", value, ok)
	}
	if _, ok := intQueryValue("12.5"); ok {
		t.Fatal("12.5 must not be an integer query value")
	}
	// Canonical instants collapse equivalent representations.
	canonical, ok := canonicalRFC3339Instant("2026-01-01T08:00:00.123456+08:00")
	if !ok || canonical != "2026-01-01T00:00:00.123Z" {
		t.Fatalf("canonical instant: %q %v", canonical, ok)
	}
	if _, ok := canonicalRFC3339Instant("2026-01-01T00:00:00"); ok {
		t.Fatal("timezone-less instant must fail")
	}
}

package apikeys

import (
	"net/http"
	"testing"
)

// listItemByName 读取列表响应中指定名称的 API Key 行（管理面与个人面共用
// shape）。
func listItemByName(t *testing.T, payload map[string]any, name string) map[string]any {
	t.Helper()
	raw, ok := dataMap(t, payload)["items"].([]any)
	if !ok {
		t.Fatalf("items missing: %v", payload)
	}
	for _, entry := range raw {
		item, isObject := entry.(map[string]any)
		if isObject && item["name"] == name {
			return item
		}
	}
	t.Fatalf("list item %q missing: %v", name, payload)
	return nil
}

func TestAPIKeyClientIPConcurrencyCreateDefaultsAndValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	// 缺省 → 0：列落默认值，列表恒返回该字段。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"limit-default"}`)
	if code != http.StatusCreated {
		t.Fatalf("create default: %d %v", code, created)
	}
	defaultID := dataMap(t, created)["id"].(string)
	if got := env.queryCell(t, `SELECT client_ip_concurrency_limit FROM api_keys WHERE id = ?`, defaultID); got != "0" {
		t.Fatalf("default column: %q", got)
	}
	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?keyword=limit-default", "")
	if code != http.StatusOK {
		t.Fatalf("list default: %d %v", code, listed)
	}
	if item := listItemByName(t, listed, "limit-default"); item["clientIpConcurrencyLimit"] != float64(0) {
		t.Fatalf("default list item: %v", item)
	}

	// 显式 null → 0。
	code, nullCreated := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"limit-null","clientIpConcurrencyLimit":null}`)
	if code != http.StatusCreated {
		t.Fatalf("create null: %d %v", code, nullCreated)
	}
	nullID := dataMap(t, nullCreated)["id"].(string)
	if got := env.queryCell(t, `SELECT client_ip_concurrency_limit FROM api_keys WHERE id = ?`, nullID); got != "0" {
		t.Fatalf("null column: %q", got)
	}

	// 合法值落库并出现在列表（含上界 1000000）。
	for _, testCase := range []struct {
		name  string
		limit int
	}{
		{"limit-25", 25},
		{"limit-max", MaxClientIPConcurrencyLimit},
	} {
		body := `{"name":"` + testCase.name + `","clientIpConcurrencyLimit":` + itoa(testCase.limit) + `}`
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", body)
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", testCase.name, code, payload)
		}
		id := dataMap(t, payload)["id"].(string)
		if got := env.queryCell(t, `SELECT client_ip_concurrency_limit FROM api_keys WHERE id = ?`, id); got != itoa(testCase.limit) {
			t.Fatalf("%s column: %q", testCase.name, got)
		}
		code, listed := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?keyword="+testCase.name, "")
		if code != http.StatusOK {
			t.Fatalf("list %s: %d %v", testCase.name, code, listed)
		}
		if item := listItemByName(t, listed, testCase.name); item["clientIpConcurrencyLimit"] != float64(testCase.limit) {
			t.Fatalf("%s list item: %v", testCase.name, item)
		}
		// 单条详情复用同一列表行映射。
		code, detail := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+id, "")
		if code != http.StatusOK {
			t.Fatalf("detail %s: %d %v", testCase.name, code, detail)
		}
		if item := dataMap(t, detail); item["clientIpConcurrencyLimit"] != float64(testCase.limit) {
			t.Fatalf("%s detail: %v", testCase.name, detail)
		}
	}

	// 类型/整数/范围错误 → 400（每次都换名称，规避 mutation guard 的失败
	// 指纹污染），且不静默落库。
	invalid := []struct {
		name string
		body string
	}{
		{"limit-over", `{"name":"limit-over","clientIpConcurrencyLimit":1000001}`},
		{"limit-negative", `{"name":"limit-negative","clientIpConcurrencyLimit":-1}`},
		{"limit-float", `{"name":"limit-float","clientIpConcurrencyLimit":1.5}`},
		{"limit-string", `{"name":"limit-string","clientIpConcurrencyLimit":"5"}`},
		{"limit-bool", `{"name":"limit-bool","clientIpConcurrencyLimit":true}`},
	}
	for _, testCase := range invalid {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", testCase.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: %d %v", testCase.name, code, payload)
		}
		if count := env.count(t, `SELECT COUNT(*) FROM api_keys WHERE name = ?`, testCase.name); count != 0 {
			t.Fatalf("%s must not persist", testCase.name)
		}
	}
}

func TestAPIKeyClientIPConcurrencyPatchSemantics(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"limit-patch","clientIpConcurrencyLimit":10}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	keyID := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)

	// PATCH 缺省该字段 → 保持原值，不进入 changedFields/rowPatch。
	code, renamed := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","name":"limit-patch-2"}`)
	if code != http.StatusOK {
		t.Fatalf("rename: %d %v", code, renamed)
	}
	if fields := changedFieldsOf(t, renamed); len(fields) != 1 || fields[0] != "name" {
		t.Fatalf("rename changedFields: %v", fields)
	}
	rowPatch := rowPatchOf(t, renamed)
	if _, hasLimit := rowPatch["clientIpConcurrencyLimit"]; hasLimit {
		t.Fatalf("rename rowPatch must omit limit: %v", rowPatch)
	}
	if got := env.queryCell(t, `SELECT client_ip_concurrency_limit FROM api_keys WHERE id = ?`, keyID); got != "10" {
		t.Fatalf("omitted patch must keep 10: %q", got)
	}
	revision = dataMap(t, renamed)["revision"].(string)

	// 同值提交 → no-op：不 bump revision、不写日志、不触发缓存失效。
	reasonsBefore := len(env.inval.reasons)
	logsBefore := len(updateEntries(env.sink))
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","clientIpConcurrencyLimit":10}`)
	if code != http.StatusOK {
		t.Fatalf("noop: %d %v", code, noop)
	}
	if fields := changedFieldsOf(t, noop); len(fields) != 0 {
		t.Fatalf("noop changedFields: %v", fields)
	}
	if dataMap(t, noop)["revision"] != revision {
		t.Fatalf("noop must keep revision: %v", noop)
	}
	if patch := rowPatchOf(t, noop); len(patch) != 1 || patch["revision"] != revision {
		t.Fatalf("noop rowPatch: %v", patch)
	}
	if len(updateEntries(env.sink)) != logsBefore || len(env.inval.reasons) != reasonsBefore {
		t.Fatal("no-op must not log or invalidate")
	}

	// 显式 0 → 关闭：changedFields/rowPatch/审计标签/运行时缓存失效。
	code, disabled := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","clientIpConcurrencyLimit":0}`)
	if code != http.StatusOK {
		t.Fatalf("disable: %d %v", code, disabled)
	}
	if fields := changedFieldsOf(t, disabled); len(fields) != 1 || fields[0] != "clientIpConcurrencyLimit" {
		t.Fatalf("disable changedFields: %v", fields)
	}
	if patch := rowPatchOf(t, disabled); patch["clientIpConcurrencyLimit"] != float64(0) {
		t.Fatalf("disable rowPatch: %v", patch)
	}
	if got := env.queryCell(t, `SELECT client_ip_concurrency_limit FROM api_keys WHERE id = ?`, keyID); got != "0" {
		t.Fatalf("disable column: %q", got)
	}
	updates := updateEntries(env.sink)
	if len(updates) != logsBefore+1 {
		t.Fatalf("update logs: %d (want %d)", len(updates), logsBefore+1)
	}
	change := fieldChange(t, updates[len(updates)-1], "clientIpConcurrencyLimit")
	if change.Label != "单 IP 并发限制" || change.Before != "10" || change.After != "0" {
		t.Fatalf("limit change: %+v", change)
	}
	if !env.inval.has("api_key_updated") {
		t.Fatalf("runtime invalidation missing: %v", env.inval.reasons)
	}
	revision = dataMap(t, disabled)["revision"].(string)

	// 再次开启后，非法值全部 400 且不改变已落库值/revision。
	code, enabled := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","clientIpConcurrencyLimit":5}`)
	if code != http.StatusOK {
		t.Fatalf("enable: %d %v", code, enabled)
	}
	revision = dataMap(t, enabled)["revision"].(string)
	for _, body := range []string{
		`{"expectedRevision":"` + revision + `","clientIpConcurrencyLimit":1000001}`,
		`{"expectedRevision":"` + revision + `","clientIpConcurrencyLimit":-1}`,
		`{"expectedRevision":"` + revision + `","clientIpConcurrencyLimit":1.25}`,
		`{"expectedRevision":"` + revision + `","clientIpConcurrencyLimit":"5"}`,
		`{"expectedRevision":"` + revision + `","clientIpConcurrencyLimit":null}`,
	} {
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID, body)
		if code != http.StatusBadRequest {
			t.Fatalf("invalid patch %s: %d %v", body, code, payload)
		}
		if got := env.queryCell(t, `SELECT client_ip_concurrency_limit FROM api_keys WHERE id = ?`, keyID); got != "5" {
			t.Fatalf("invalid patch must keep 5: %q", got)
		}
	}
}

func TestAPIKeyClientIPConcurrencySelfSurface(t *testing.T) {
	env := newTestEnv(t)
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.seedDefaultRouteStrategy(t, aliceID, "rs-alice")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/my-api-keys",
		`{"name":"self-limit","clientIpConcurrencyLimit":3}`)
	if code != http.StatusCreated {
		t.Fatalf("self create: %d %v", code, created)
	}
	keyID := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)
	if got := env.queryCell(t, `SELECT client_ip_concurrency_limit FROM api_keys WHERE id = ?`, keyID); got != "3" {
		t.Fatalf("self create column: %q", got)
	}

	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys", "")
	if code != http.StatusOK {
		t.Fatalf("self list: %d %v", code, listed)
	}
	if item := listItemByName(t, listed, "self-limit"); item["clientIpConcurrencyLimit"] != float64(3) {
		t.Fatalf("self list item: %v", item)
	}

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/my-api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","clientIpConcurrencyLimit":0}`)
	if code != http.StatusOK {
		t.Fatalf("self patch: %d %v", code, patched)
	}
	if fields := changedFieldsOf(t, patched); len(fields) != 1 || fields[0] != "clientIpConcurrencyLimit" {
		t.Fatalf("self patch changedFields: %v", fields)
	}
	if got := env.queryCell(t, `SELECT client_ip_concurrency_limit FROM api_keys WHERE id = ?`, keyID); got != "0" {
		t.Fatalf("self patch column: %q", got)
	}

	revision = dataMap(t, patched)["revision"].(string)
	code, rejected := env.do(t, http.MethodPatch, "/__aisys__/api/my-api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","clientIpConcurrencyLimit":1000001}`)
	if code != http.StatusBadRequest {
		t.Fatalf("self over-limit: %d %v", code, rejected)
	}
}

func TestClientIPConcurrencyFieldParsers(t *testing.T) {
	valid := []struct {
		raw  any
		want int
	}{
		{float64(0), 0},
		{float64(5), 5},
		{float64(MaxClientIPConcurrencyLimit), MaxClientIPConcurrencyLimit},
		{float64(1e2), 100},
	}
	for _, testCase := range valid {
		got, issue := parseClientIPConcurrencyLimitField(testCase.raw)
		if issue != "" || got != testCase.want {
			t.Fatalf("parse %v: %d %q", testCase.raw, got, issue)
		}
	}
	invalid := []struct {
		raw   any
		issue string
	}{
		{"5", "Expected number, received string"},
		{nil, "Expected number, received null"},
		{true, "Expected number, received boolean"},
		{float64(1.5), "Expected integer, received float"},
		{float64(-1), "Number must be greater than or equal to 0"},
		{float64(MaxClientIPConcurrencyLimit + 1), "Number must be less than or equal to 1000000"},
	}
	for _, testCase := range invalid {
		got, issue := parseClientIPConcurrencyLimitField(testCase.raw)
		if issue != testCase.issue || got != 0 {
			t.Fatalf("parse %v: %d %q (want %q)", testCase.raw, got, issue, testCase.issue)
		}
	}
	if value, err := normalizeClientIPConcurrencyLimit(0); err != nil || value != 0 {
		t.Fatalf("normalize 0: %d %v", value, err)
	}
	if value, err := normalizeClientIPConcurrencyLimit(MaxClientIPConcurrencyLimit); err != nil || value != MaxClientIPConcurrencyLimit {
		t.Fatalf("normalize max: %d %v", value, err)
	}
	for _, value := range []int{-1, MaxClientIPConcurrencyLimit + 1} {
		var validation *ValidationError
		if _, err := normalizeClientIPConcurrencyLimit(value); err == nil || !asValidationError(err, &validation) {
			t.Fatalf("normalize %d must fail with ValidationError: %v", value, err)
		}
	}
}

// asValidationError 是本文件的小工具，避免为一次断言引入 errors.As 的重复
// 样板。
func asValidationError(err error, target **ValidationError) bool {
	validation, ok := err.(*ValidationError)
	if ok {
		*target = validation
	}
	return ok
}

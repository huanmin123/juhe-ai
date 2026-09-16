package openaicompat

// w11b 波次：files / vector stores 路由边界（list 过滤、404、容器文件、
// 参数辅助）与 store / vectorstoresstore 纯函数分支。

import (
	"context"
	"testing"
)

func TestW11BFilesRoutesListParamsAndMissing(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := env.Deps.Store
	createTestFile(t, store, env.FilesRoot, "w11b-a", "assistants", []byte("hello world hello"), "text/plain", nil)
	createTestFile(t, store, env.FilesRoot, "w11b-b", "assistants", []byte("second file"), "text/plain", nil)
	createTestFile(t, store, env.FilesRoot, "w11b-c", "batch", []byte("other purpose"), "text/plain", nil)

	// purpose 过滤。
	status, raw := env.doJSON(t, "GET", "/v1/files?purpose=batch", testScopeA, "")
	if status != 200 || len(raw["data"].([]any)) != 1 {
		t.Fatalf("purpose filter = %d %v", status, raw)
	}
	// limit 截断。
	status, raw = env.doJSON(t, "GET", "/v1/files?limit=2", testScopeA, "")
	if status != 200 || len(raw["data"].([]any)) != 2 {
		t.Fatalf("limit = %d %v", status, raw)
	}
	if raw["first_id"] == "" || raw["last_id"] == "" {
		t.Fatalf("envelope ids = %v", raw)
	}
	// order=desc。
	status, raw = env.doJSON(t, "GET", "/v1/files?order=desc&limit=1", testScopeA, "")
	if status != 200 {
		t.Fatalf("desc = %d", status)
	}
	// after 游标：下一页不再包含当前首条。
	firstID := raw["data"].([]any)[0].(map[string]any)["id"].(string)
	status, raw = env.doJSON(t, "GET", "/v1/files?limit=10&after="+firstID, testScopeA, "")
	if status != 200 {
		t.Fatalf("after page = %d", status)
	}
	for _, item := range raw["data"].([]any) {
		if item.(map[string]any)["id"] == firstID {
			t.Fatalf("after page 不应包含游标条目: %v", raw)
		}
	}
	// 不存在文件 / 下载。
	status, _ = env.doJSON(t, "GET", "/v1/files/missing-w11b", testScopeA, "")
	if status != 404 {
		t.Fatalf("missing file = %d", status)
	}
	contentStatus, contentBody := env.do(t, "GET", "/v1/files/w11b-a/content", testScopeA, "", nil)
	if contentStatus != 200 || contentBody != "hello world hello" {
		t.Fatalf("content = %d %q", contentStatus, contentBody)
	}
	// 删除两次。
	status, _ = env.doJSON(t, "DELETE", "/v1/files/w11b-a", testScopeA, "")
	if status != 200 {
		t.Fatalf("delete = %d", status)
	}
	status, _ = env.doJSON(t, "DELETE", "/v1/files/w11b-a", testScopeA, "")
	if status != 404 {
		t.Fatalf("redelete = %d", status)
	}
	// 跨 scope 不可见。
	status, _ = env.doJSON(t, "GET", "/v1/files/w11b-b", testScopeB, "")
	if status != 404 {
		t.Fatalf("cross scope = %d", status)
	}
	// 容器文件列表 + 单查 + 404。
	container := "ctr-w11b"
	createTestFile(t, store, env.FilesRoot, "w11b-ct", "code_interpreter_output", []byte("container file"), "text/plain", &container)
	status, raw = env.doJSON(t, "GET", "/v1/containers/ctr-w11b/files", testScopeA, "")
	if status != 200 || len(raw["data"].([]any)) != 1 {
		t.Fatalf("container list = %d %v", status, raw)
	}
	status, raw = env.doJSON(t, "GET", "/v1/containers/ctr-w11b/files/w11b-ct", testScopeA, "")
	if status != 200 || raw["object"] != "container.file" {
		t.Fatalf("container file = %d %v", status, raw)
	}
	status, _ = env.doJSON(t, "GET", "/v1/containers/ctr-w11b/files/missing", testScopeA, "")
	if status != 404 {
		t.Fatalf("container missing = %d", status)
	}
}

func TestW11BVectorStoreRoutesEdges(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := env.Deps.Store
	status, raw := env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `{"name":"w11b-vs"}`)
	if status != 200 {
		t.Fatalf("create = %d", status)
	}
	vsID := raw["id"].(string)
	content := []byte("w11b vector text")
	createTestFile(t, store, env.FilesRoot, "w11b-vf", "assistants", content, "text/plain", nil)

	// 单查 404 / 删除 404。
	status, _ = env.doJSON(t, "GET", "/v1/vector_stores/missing-w11b", testScopeA, "")
	if status != 404 {
		t.Fatalf("missing vs = %d", status)
	}
	status, _ = env.doJSON(t, "DELETE", "/v1/vector_stores/missing-w11b", testScopeA, "")
	if status != 404 {
		t.Fatalf("delete missing vs = %d", status)
	}
	// 文件 404 / 内容 404。
	status, _ = env.doJSON(t, "GET", "/v1/vector_stores/"+vsID+"/files/missing", testScopeA, "")
	if status != 404 {
		t.Fatalf("missing vs file = %d", status)
	}
	status, _ = env.doJSON(t, "GET", "/v1/vector_stores/missing/files/w11b-vf/content", testScopeA, "")
	if status != 404 {
		t.Fatalf("content missing vs = %d", status)
	}
	status, _ = env.doJSON(t, "DELETE", "/v1/vector_stores/"+vsID+"/files/missing", testScopeA, "")
	if status != 404 {
		t.Fatalf("delete missing vs file = %d", status)
	}
	// list 空 + after + limit。
	status, raw = env.doJSON(t, "GET", "/v1/vector_stores/"+vsID+"/files?limit=5&after=x", testScopeA, "")
	if status != 200 || len(raw["data"].([]any)) != 0 {
		t.Fatalf("vs files empty = %d %v", status, raw)
	}
	// 附加文件后搜索：query 命中。
	status, _ = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/files", testScopeA, `{"file_id":"w11b-vf"}`)
	if status != 200 {
		t.Fatalf("attach = %d", status)
	}
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/search", testScopeA, `{"query":"vector","max_num_results":1}`)
	if status != 200 || len(raw["data"].([]any)) != 1 {
		t.Fatalf("search = %d %v", status, raw)
	}
	// 搜索无命中。
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/search", testScopeA, `{"query":"zzz-not-there"}`)
	if status != 200 || len(raw["data"].([]any)) != 0 {
		t.Fatalf("search miss = %d %v", status, raw)
	}
	// 属性过滤：不匹配 → 空结果。
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/search", testScopeA,
		`{"query":"vector","filters":{"key":"lang","type":"eq","value":"en"}}`)
	if status != 200 || len(raw["data"].([]any)) != 0 {
		t.Fatalf("filtered search = %d %v", status, raw)
	}
	// 删除存储。
	status, raw = env.doJSON(t, "DELETE", "/v1/vector_stores/"+vsID, testScopeA, "")
	if status != 200 || raw["object"] != "vector_store.deleted" {
		t.Fatalf("delete vs = %d %v", status, raw)
	}
}

func TestW11BStorePureHelpers(t *testing.T) {
	if coerceInt64(int64(3)) != 3 || coerceInt64(7) != 7 || coerceInt64(float64(2.9)) != 2 {
		t.Fatal("数值转换")
	}
	if coerceInt64([]byte(" 42 ")) != 42 || coerceInt64("x") != 0 || coerceInt64(true) != 0 {
		t.Fatal("字符串/其它转换")
	}
	if parseInt64("bad") != 0 || parseInt64(" 17 ") != 17 {
		t.Fatal("parseInt64")
	}
	if ensureCtx(nil) == nil {
		t.Fatal("nil ctx 兜底")
	}
	store := newTestStore(t)
	if id := store.generateID("file"); id == "" {
		t.Fatal("file ID 生成")
	}
	if id := store.generateID("vector_store"); id == "" {
		t.Fatal("vector store ID 生成")
	}
	storeWithGen := newTestStore(t)
	storeWithGen.newID = func(kind string) string { return "custom-" + kind }
	if id := storeWithGen.generateID("file"); id != "custom-file" {
		t.Fatalf("custom ID = %q", id)
	}
	value := "x"
	if trimmedPointer(nil) != nil || trimmedPointer(&value) == nil {
		t.Fatal("trimmedPointer")
	}
	blank := "   "
	if trimmedPointer(&blank) != nil {
		t.Fatal("空白串归 nil")
	}
	zero, big, mid := 0, 10001, 25
	if clampLimit(nil, defaultListLimit, maxListLimit) != defaultListLimit ||
		clampLimit(&zero, 20, 100) != 1 || clampLimit(&big, 20, 100) != 100 || clampLimit(&mid, 20, 100) != 25 {
		t.Fatal("clampLimit")
	}
	if array := parseJSONObject(`[1]`); len(array) != 0 {
		t.Fatalf("数组折叠空对象: %v", array)
	}
	if bad := parseJSONObject(`bad`); len(bad) != 0 {
		t.Fatalf("坏 JSON 折叠空对象: %v", bad)
	}
	if empty := parseJSONObject(``); len(empty) != 0 {
		t.Fatal("空串折叠空对象")
	}
	object := parseJSONObject(`{"a":1}`)
	if object["a"] != float64(1) {
		t.Fatal("对象解析")
	}
	// cursorClause 方向组合。
	ascAfter := cursorClause("asc", "created_at", "file_id", true)
	if ascAfter != "(created_at > ? OR (created_at = ? AND file_id > ?))" {
		t.Fatalf("asc after = %q", ascAfter)
	}
	descBefore := cursorClause("desc", "created_at", "file_id", false)
	if descBefore != "(created_at > ? OR (created_at = ? AND file_id > ?))" {
		t.Fatalf("desc before = %q", descBefore)
	}
	if invert("<") != ">" || invert(">") != "<" {
		t.Fatal("invert")
	}
	_ = context.Background
}

func TestW11BVectorStorePureHelpers(t *testing.T) {
	if number, ok := asNumber(float64(3.5)); !ok || number != 3.5 {
		t.Fatal("float64 数值")
	}
	if _, ok := asNumber(int64(2)); ok {
		t.Fatal("int64 非数值面")
	}
	if _, ok := asNumber("text"); ok {
		t.Fatal("字符串非数值")
	}
	if _, ok := asNumber("  "); ok {
		t.Fatal("空白串非数值")
	}
	if number, ok := asNumber("42.5"); !ok || number != 42.5 {
		t.Fatal("数字字符串解析")
	}
	if _, ok := asNumber("nan"); ok {
		t.Fatal("nan 字符串非数值")
	}
	if jsonScalarEqual(float64(1), float64(1)) != true || jsonScalarEqual("a", "b") || jsonScalarEqual(nil, "a") {
		t.Fatal("标量相等")
	}
	if jsonScalarEqual(float64(1), "1") {
		t.Fatal("跨类型不相等")
	}
	// matchesAttributeFilter。
	fileAttrs := map[string]any{"lang": "en", "tier": float64(2)}
	if !matchesAttributeFilter(fileAttrs, map[string]any{"type": "eq", "key": "lang", "value": "en"}) {
		t.Fatal("eq 匹配")
	}
	if matchesAttributeFilter(fileAttrs, map[string]any{"type": "eq", "key": "lang", "value": "fr"}) {
		t.Fatal("eq 不匹配")
	}
	if matchesAttributeFilter(map[string]any{}, map[string]any{"type": "eq", "key": "lang", "value": "en"}) {
		t.Fatal("缺属性不匹配")
	}
	if !matchesAttributeFilter(fileAttrs, map[string]any{"type": "ne", "key": "lang", "value": "fr"}) {
		t.Fatal("ne 匹配")
	}
	if !matchesAttributeFilter(fileAttrs, map[string]any{"type": "in", "key": "lang", "value": []any{"fr", "en"}}) {
		t.Fatal("in 匹配")
	}
	if matchesAttributeFilter(fileAttrs, map[string]any{"type": "nin", "key": "lang", "value": []any{"fr", "en"}}) {
		t.Fatal("nin 不匹配")
	}
	if !matchesAttributeFilter(fileAttrs, map[string]any{"type": "gt", "key": "tier", "value": float64(1)}) {
		t.Fatal("gt 匹配")
	}
	if matchesAttributeFilter(fileAttrs, map[string]any{"type": "lt", "key": "tier", "value": float64(1)}) {
		t.Fatal("lt 不匹配")
	}
	if !matchesAttributeFilter(fileAttrs, map[string]any{"type": "and", "filters": []any{
		map[string]any{"type": "eq", "key": "lang", "value": "en"},
	}}) {
		t.Fatal("and 匹配")
	}
	if matchesAttributeFilter(fileAttrs, map[string]any{"type": "or", "filters": []any{
		map[string]any{"type": "eq", "key": "lang", "value": "fr"},
	}}) {
		t.Fatal("or 不匹配")
	}
	if !matchesAttributeFilter(fileAttrs, map[string]any{}) {
		t.Fatal("空过滤恒匹配")
	}
	if !matchesAttributeFilter(fileAttrs, map[string]any{"type": "eq", "value": "x"}) {
		t.Fatal("缺 key 恒匹配")
	}
	if matchesAttributeFilter(fileAttrs, map[string]any{"type": "eq", "key": "missing", "value": "x"}) {
		t.Fatal("eq 缺属性不匹配")
	}
	if !matchesAttributeFilter(fileAttrs, map[string]any{"type": "gte", "key": "tier", "value": float64(2)}) {
		t.Fatal("gte 匹配")
	}
	if !matchesAttributeFilter(fileAttrs, map[string]any{"type": "lte", "key": "tier", "value": float64(2)}) {
		t.Fatal("lte 匹配")
	}
	if matchesAttributeFilter(fileAttrs, map[string]any{"type": "gt", "key": "lang", "value": "en"}) {
		t.Fatal("gt 非数值不匹配")
	}
	if matchesAttributeFilter(fileAttrs, map[string]any{"type": "in", "key": "lang", "value": "not-list"}) {
		t.Fatal("in 非列表不匹配")
	}
	if matchesAttributeFilter(fileAttrs, map[string]any{"type": "nin", "key": "lang", "value": "not-list"}) {
		t.Fatal("nin 非列表不匹配")
	}
	// findVectorStoreFileCursor：不存在与存在两分支（DB 查询）。
	vsStore := newTestStore(t)
	if cursor, err := vsStore.findVectorStoreFileCursor(context.Background(), "missing", "vs-x", "acct", "key"); err != nil || cursor != nil {
		t.Fatalf("missing cursor = %v %v", cursor, err)
	}
}

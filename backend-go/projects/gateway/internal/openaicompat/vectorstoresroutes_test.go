package openaicompat

import (
	"strings"
	"testing"
)

// Vector store route tests mirror vector-stores.routes.ts envelopes, guards
// and the Chinese error copy byte-for-byte.

func TestVectorStoresRoutesCreateListGetDelete(t *testing.T) {
	env := newRouteEnv(t, nil)

	// 401 contract.
	status, raw := env.doJSON(t, "POST", "/v1/vector_stores", "", `{}`)
	if status != 401 {
		t.Fatalf("status = %d body %s", status, raw)
	}
	errObj := raw["error"].(map[string]any)
	if errObj["message"] != "缺少或无效的 API Key" || errObj["code"] != "invalid_api_key" {
		t.Fatalf("error = %v", errObj)
	}

	// Create without expiry.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `{"name":" docs ","description":"desc","metadata":{"k":"v"}}`)
	if status != 200 {
		t.Fatalf("status = %d body %v", status, raw)
	}
	object := raw
	if object["object"] != "vector_store" || object["name"] != "docs" || object["status"] != "completed" {
		t.Fatalf("vector store = %v", object)
	}
	if object["file_counts"] == nil || object["expires_after"] != nil {
		t.Fatalf("vector store = %v", object)
	}
	vsID := object["id"].(string)

	// Create with expiry (positive days -> expires_after + expires_at).
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `{"expires_after":{"anchor":"last_active_at","days":7}}`)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	withExpiry := raw
	if withExpiry["expires_after"] == nil || withExpiry["expires_at"] == nil {
		t.Fatalf("expiry object = %v", withExpiry)
	}
	// days = 0 with no anchor: JS truthiness omits expires_after entirely.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `{"expires_after":{"days":0}}`)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if raw["expires_after"] != nil {
		t.Fatalf("zero days must omit expires_after: %v", raw)
	}

	// Invalid JSON bodies.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `{"name":`)
	if status != 400 {
		t.Fatalf("status = %d", status)
	}
	errObj = raw["error"].(map[string]any)
	if errObj["message"] != "JSON 请求体无效" || errObj["code"] != "invalid_json_body" {
		t.Fatalf("error = %v", errObj)
	}
	// Non-object body.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `[1,2]`)
	if status != 400 {
		t.Fatalf("status = %d", status)
	}
	if decodeErr := raw["error"].(map[string]any); decodeErr["message"] != "JSON 请求体必须是对象" {
		t.Fatalf("error = %v", decodeErr)
	}

	// List.
	status, raw = env.doJSON(t, "GET", "/v1/vector_stores", testScopeA, "")
	if status != 200 || raw["object"] != "list" || raw["has_more"] != false {
		t.Fatalf("list = %v", raw)
	}
	if len(raw["data"].([]any)) != 3 {
		t.Fatalf("list data = %v", raw["data"])
	}

	// Get + 404 + 越权.
	status, raw = env.doJSON(t, "GET", "/v1/vector_stores/"+vsID, testScopeA, "")
	if status != 200 || raw["id"] != vsID {
		t.Fatalf("get = %d %v", status, raw)
	}
	status, _ = env.doJSON(t, "GET", "/v1/vector_stores/"+vsID, testScopeB, "")
	if status != 404 {
		t.Fatalf("越权 get = %d", status)
	}
	status, raw = env.doJSON(t, "GET", "/v1/vector_stores/missing", testScopeA, "")
	if status != 404 {
		t.Fatalf("status = %d", status)
	}
	errObj = raw["error"].(map[string]any)
	if errObj["message"] != "向量存储不存在" || errObj["code"] != "vector_store_not_found" {
		t.Fatalf("error = %v", errObj)
	}

	// Delete (byte-order: id, object, deleted).
	status, raw = env.doJSON(t, "DELETE", "/v1/vector_stores/"+vsID, testScopeA, "")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if raw["id"] != vsID || raw["object"] != "vector_store.deleted" || raw["deleted"] != true {
		t.Fatalf("delete = %v", raw)
	}
	status, _ = env.doJSON(t, "DELETE", "/v1/vector_stores/"+vsID, testScopeA, "")
	if status != 404 {
		t.Fatalf("second delete = %d", status)
	}
}

func TestVectorStoresRoutesFiles(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := env.Deps.Store
	// Seed a store.
	status, raw := env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `{"name":"docs"}`)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	vsID := raw["id"].(string)
	content := []byte("alpha beta")
	createTestFile(t, store, env.FilesRoot, "file-add", "assistants", content, "text/plain", nil)

	// missing file_id -> 400.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/files", testScopeA, `{}`)
	if status != 400 {
		t.Fatalf("status = %d", status)
	}
	errObj := raw["error"].(map[string]any)
	if errObj["message"] != "缺少必填字段：file_id" || errObj["code"] != "missing_file_id" {
		t.Fatalf("error = %v", errObj)
	}

	// unknown file -> 404 文件不存在.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/files", testScopeA, `{"file_id":"missing"}`)
	if status != 404 {
		t.Fatalf("status = %d", status)
	}
	errObj = raw["error"].(map[string]any)
	if errObj["message"] != "文件不存在" || errObj["code"] != "file_not_found" {
		t.Fatalf("error = %v", errObj)
	}

	// unknown store -> 404 向量存储不存在.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/missing/files", testScopeA, `{"file_id":"file-add"}`)
	if status != 404 {
		t.Fatalf("status = %d", status)
	}
	errObj = raw["error"].(map[string]any)
	if errObj["message"] != "向量存储不存在" {
		t.Fatalf("error = %v", errObj)
	}

	// Create attaches in_progress; synchronous indexing completes it.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/files", testScopeA,
		`{"file_id":"file-add","attributes":{"lang":"en"}}`)
	if status != 200 {
		t.Fatalf("status = %d body %v", status, raw)
	}
	fileObject := raw
	if fileObject["id"] != "file-add" || fileObject["object"] != "vector_store.file" {
		t.Fatalf("file object = %v", fileObject)
	}
	if fileObject["status"] != "in_progress" {
		// Node answers the create with the in_progress record.
		t.Fatalf("create should answer in_progress: %v", fileObject)
	}
	status, raw = env.doJSON(t, "GET", "/v1/vector_stores/"+vsID+"/files/file-add", testScopeA, "")
	if status != 200 || raw["status"] != "completed" {
		t.Fatalf("indexing should have completed: %d %v", status, raw)
	}
	// Default chunking strategy rendered.
	strategy := fileObject["chunking_strategy"].(map[string]any)
	if strategy["type"] != "static" {
		t.Fatalf("chunking strategy = %v", strategy)
	}
	static := strategy["static"].(map[string]any)
	if static["max_chunk_size_tokens"] != float64(800) || static["chunk_overlap_tokens"] != float64(400) {
		t.Fatalf("static strategy = %v", static)
	}

	// File content chunks.
	status, raw = env.doJSON(t, "GET", "/v1/vector_stores/"+vsID+"/files/file-add/content", testScopeA, "")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	data := raw["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("content chunks = %v", data)
	}
	chunk := data[0].(map[string]any)
	if chunk["object"] != "vector_store.file_content" || chunk["type"] != "text" || chunk["text"] != "alpha beta" {
		t.Fatalf("chunk = %v", chunk)
	}

	// Files list.
	status, raw = env.doJSON(t, "GET", "/v1/vector_stores/"+vsID+"/files", testScopeA, "")
	if status != 200 || len(raw["data"].([]any)) != 1 {
		t.Fatalf("files list = %v", raw)
	}

	// Unsupported media type -> failed status with the exact last_error.
	createTestFile(t, store, env.FilesRoot, "file-bin", "assistants", []byte{0xff, 0x00}, "application/octet-stream", nil)
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/files", testScopeA, `{"file_id":"file-bin"}`)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	// The create answers in_progress; the failed state is observable on the
	// follow-up read.
	status, raw = env.doJSON(t, "GET", "/v1/vector_stores/"+vsID+"/files/file-bin", testScopeA, "")
	if status != 200 || raw["status"] != "failed" {
		t.Fatalf("binary file should fail indexing: %d %v", status, raw)
	}
	lastError := raw["last_error"].(map[string]any)
	if lastError["code"] != "openai_compatible_file_mime_unsupported" {
		t.Fatalf("last error = %v", lastError)
	}
	if lastError["message"] != "文件 file-bin 的媒体类型不受本地向量存储文本索引支持" {
		t.Fatalf("last error message = %v", lastError["message"])
	}

	// Delete file envelope.
	status, raw = env.doJSON(t, "DELETE", "/v1/vector_stores/"+vsID+"/files/file-add", testScopeA, "")
	if status != 200 || raw["object"] != "vector_store.file.deleted" || raw["id"] != "file-add" {
		t.Fatalf("delete file = %v", raw)
	}
	status, _ = env.doJSON(t, "DELETE", "/v1/vector_stores/"+vsID+"/files/file-add", testScopeA, "")
	if status != 404 {
		t.Fatalf("second delete = %d", status)
	}
}

func TestVectorStoresRoutesSearch(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := env.Deps.Store
	_, raw := env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `{"name":"docs"}`)
	vsID := raw["id"].(string)
	createTestFile(t, store, env.FilesRoot, "file-search", "assistants", []byte("searchable alpha content"), "text/plain", nil)
	var status int

	// Missing query -> 400.
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/search", testScopeA, `{"max_num_results":3}`)
	if status != 400 {
		t.Fatalf("status = %d", status)
	}
	errObj := raw["error"].(map[string]any)
	if errObj["message"] != "缺少必填字段：query" || errObj["code"] != "missing_query" {
		t.Fatalf("error = %v", errObj)
	}

	// Search before indexing: completed = 0, failed = 0 -> passes readiness
	// and returns no results (matches Node: guard only trips when failed > 0).
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/search", testScopeA, `{"query":"alpha"}`)
	if status != 200 {
		t.Fatalf("status = %d body %v", status, raw)
	}
	if raw["object"] != "vector_store.search_results.page" || raw["search_query"] != "alpha" {
		t.Fatalf("search response = %v", raw)
	}
	if raw["has_more"] != false || raw["next_page"] != nil {
		t.Fatalf("search pagination = %v", raw)
	}

	// Index the file, then search hits it.
	_, _ = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/files", testScopeA, `{"file_id":"file-search"}`)
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/search", testScopeA,
		`{"query":"alpha","max_num_results":5,"ranking_options":{"score_threshold":0.1}}`)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	data := raw["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("results = %v", data)
	}
	item := data[0].(map[string]any)
	if item["file_id"] != "file-search" || item["filename"] != "file-search.txt" {
		t.Fatalf("item = %v", item)
	}
	contentBlock := item["content"].([]any)[0].(map[string]any)
	if contentBlock["type"] != "text" || contentBlock["text"] != "searchable alpha content" {
		t.Fatalf("content = %v", contentBlock)
	}

	// 409: in-progress store blocks search.
	inProgressStore := "vs-inprogress"
	if _, err := store.CreateVectorStore(t.Context(), inProgressStore, VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, store, `INSERT INTO openai_compatible_vector_store_files
		(vector_store_id, file_id, system_account_id, api_key_id, attributes_json, chunking_strategy_json, status, usage_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, '{}', '{}', 'in_progress', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		inProgressStore, "file-x", testScopeA, testKeyA)
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+inProgressStore+"/search", testScopeA, `{"query":"alpha"}`)
	if status != 409 {
		t.Fatalf("status = %d body %v", status, raw)
	}
	errObj = raw["error"].(map[string]any)
	if errObj["message"] != "向量存储文件仍在建立索引" || errObj["code"] != "openai_compatible_vector_store_not_ready" {
		t.Fatalf("error = %v", errObj)
	}

	// 400: failed-only store.
	failedStore := "vs-failed"
	if _, err := store.CreateVectorStore(t.Context(), failedStore, VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, store, `INSERT INTO openai_compatible_vector_store_files
		(vector_store_id, file_id, system_account_id, api_key_id, attributes_json, chunking_strategy_json, status, usage_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, '{}', '{}', 'failed', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		failedStore, "file-y", testScopeA, testKeyA)
	status, raw = env.doJSON(t, "POST", "/v1/vector_stores/"+failedStore+"/search", testScopeA, `{"query":"alpha"}`)
	if status != 400 {
		t.Fatalf("status = %d", status)
	}
	errObj = raw["error"].(map[string]any)
	if errObj["message"] != "向量存储没有可检索的已完成文件" || errObj["code"] != "openai_compatible_vector_store_file_failed" {
		t.Fatalf("error = %v", errObj)
	}

	// 越权 search: store invisible -> 404.
	status, _ = env.doJSON(t, "POST", "/v1/vector_stores/"+vsID+"/search", testScopeB, `{"query":"alpha"}`)
	if status != 404 {
		t.Fatalf("越权 search = %d", status)
	}
}

func TestVectorStoresRoutesOversizeBody(t *testing.T) {
	env := newRouteEnv(t, nil)
	huge := `{"query":"` + strings.Repeat("x", 1024*1024) + `"}`
	status, raw := env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, huge)
	if status != 413 {
		t.Fatalf("status = %d", status)
	}
	errObj := raw["error"].(map[string]any)
	if errObj["message"] != "JSON 请求体过大" || errObj["code"] != "request_body_too_large" || errObj["type"] != "request_too_large" {
		t.Fatalf("error = %v", errObj)
	}
}

func TestVectorStoresRoutesIDsGeneratedNodeShaped(t *testing.T) {
	env := newRouteEnv(t, nil)
	_, raw := env.doJSON(t, "POST", "/v1/vector_stores", testScopeA, `{}`)
	id := raw["id"].(string)
	if !strings.HasPrefix(id, "vs_") {
		t.Fatalf("id = %s", id)
	}
	// vs_<base36 ms>_<20 hex>
	suffix := strings.TrimPrefix(id, "vs_")
	parts := strings.Split(suffix, "_")
	if len(parts) != 2 || len(parts[1]) != 20 {
		t.Fatalf("id shape = %s", id)
	}
}

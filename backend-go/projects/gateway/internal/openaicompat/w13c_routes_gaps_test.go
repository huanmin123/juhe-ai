package openaicompat

// w13c 覆盖率补充测试（路由族）：store 错误臂、缺 scope 顺序、路径参数缺失、
// 对象时间戳不变量与 multipart 上传错误形态。
//
// 说明：routeEnv 使用每测试独立的内存 SQLite；关闭其底层 DB 后所有 Store
// 调用返回错误，用于覆盖各路由的 store 错误透传臂。

import (
	"bytes"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestW13CRouteStoreErrorSweep(t *testing.T) {
	env := newRouteEnv(t, nil)
	// 关闭底层 DB：所有 Store 调用失败，覆盖路由错误透传臂。
	if err := env.Deps.Store.db.Close(); err != nil {
		t.Fatal(err)
	}
	requests := []struct {
		method, path, body string
	}{
		{"GET", "/v1/files", ""},
		{"GET", "/v1/containers/ctr-w13c/files", ""},
		{"GET", "/v1/files/w13c-f1", ""},
		{"GET", "/v1/files/w13c-f1/content", ""},
		{"GET", "/v1/containers/ctr-w13c/files/w13c-f1", ""},
		{"GET", "/v1/containers/ctr-w13c/files/w13c-f1/content", ""},
		{"DELETE", "/v1/files/w13c-f1", ""},
		{"POST", "/v1/vector_stores", `{"name":"w13c-vs"}`},
		{"GET", "/v1/vector_stores", ""},
		{"GET", "/v1/vector_stores/w13c-vs", ""},
		{"DELETE", "/v1/vector_stores/w13c-vs", ""},
		{"POST", "/v1/vector_stores/w13c-vs/files", `{"file_id":"w13c-f1"}`},
		{"GET", "/v1/vector_stores/w13c-vs/files", ""},
		{"GET", "/v1/vector_stores/w13c-vs/files/w13c-f1", ""},
		{"DELETE", "/v1/vector_stores/w13c-vs/files/w13c-f1", ""},
		{"GET", "/v1/vector_stores/w13c-vs/files/w13c-f1/content", ""},
		{"POST", "/v1/vector_stores/w13c-vs/search", `{"query":"x"}`},
	}
	for _, item := range requests {
		status, _ := env.doJSON(t, item.method, item.path, testScopeA, item.body)
		if status < 500 {
			t.Errorf("%s %s 对已关闭 DB 应返回 5xx，实际 %d", item.method, item.path, status)
		}
	}
}

func TestW13CLookupWithoutScope(t *testing.T) {
	env := newRouteEnv(t, nil)
	requests := []struct{ method, path string }{
		{"GET", "/v1/files/w13c-f1"},
		{"GET", "/v1/files/w13c-f1/content"},
		{"GET", "/v1/containers/ctr-w13c/files/w13c-f1"},
		{"GET", "/v1/containers/ctr-w13c/files/w13c-f1/content"},
		{"GET", "/v1/vector_stores/w13c-vs"},
		{"GET", "/v1/vector_stores/w13c-vs/files/w13c-f1"},
		{"GET", "/v1/vector_stores/w13c-vs/files/w13c-f1/content"},
	}
	for _, item := range requests {
		status, _ := env.do(t, item.method, item.path, "", "", nil)
		if status != http.StatusUnauthorized {
			t.Errorf("%s 无 scope 应 401，实际 %d", item.path, status)
		}
	}
}

func TestW13CPathValueMissingArms(t *testing.T) {
	env := newRouteEnv(t, nil)
	// 直接调用处理器：绕过 mux 后 PathValue 恒为空，触发缺失 ID 错误臂。
	bare := func() *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/v1/files/x", nil)
		request.Header.Set("X-Test-Scope", testScopeA)
		return request
	}
	recorder := httptest.NewRecorder()
	if err := env.Deps.deleteFile(recorder, bare()); err == nil || !strings.Contains(err.Error(), "缺少文件 ID") {
		t.Fatalf("deleteFile = %v", err)
	}
	recorder = httptest.NewRecorder()
	if err := env.Deps.getFile(recorder, bare()); err == nil || !strings.Contains(err.Error(), "缺少文件 ID") {
		t.Fatalf("getFile = %v", err)
	}
	recorder = httptest.NewRecorder()
	if err := env.Deps.downloadFileContent(recorder, bare()); err == nil || !strings.Contains(err.Error(), "缺少文件 ID") {
		t.Fatalf("downloadFileContent = %v", err)
	}
	recorder = httptest.NewRecorder()
	if err := env.Deps.listContainerFiles(recorder, bare()); err == nil || !strings.Contains(err.Error(), "缺少容器 ID") {
		t.Fatalf("listContainerFiles = %v", err)
	}
	recorder = httptest.NewRecorder()
	if err := env.Deps.getContainerFile(recorder, bare()); err == nil {
		t.Fatal("getContainerFile 应先失败在容器 ID")
	}
	recorder = httptest.NewRecorder()
	if err := env.Deps.downloadContainerFileContent(recorder, bare()); err == nil {
		t.Fatal("downloadContainerFileContent 应先失败在容器 ID")
	}
	recorder = httptest.NewRecorder()
	if err := env.Deps.getVectorStore(recorder, bare()); err == nil || !strings.Contains(err.Error(), "缺少向量存储 ID") {
		t.Fatalf("getVectorStore = %v", err)
	}
	recorder = httptest.NewRecorder()
	if err := env.Deps.deleteVectorStore(recorder, bare()); err == nil || !strings.Contains(err.Error(), "缺少向量存储 ID") {
		t.Fatalf("deleteVectorStore = %v", err)
	}
	// 路径参数助手直调。
	if _, err := pathFileID(httptest.NewRequest(http.MethodGet, "/x", nil)); err == nil {
		t.Fatal("pathFileID 空值应报错")
	}
	if _, err := pathContainerID(httptest.NewRequest(http.MethodGet, "/x", nil)); err == nil {
		t.Fatal("pathContainerID 空值应报错")
	}
	if _, err := pathVectorStoreID(httptest.NewRequest(http.MethodGet, "/x", nil)); err == nil {
		t.Fatal("pathVectorStoreID 空值应报错")
	}
	if _, err := pathVectorStoreFileID(httptest.NewRequest(http.MethodGet, "/x", nil)); err == nil {
		t.Fatal("pathVectorStoreFileID 空值应报错")
	}
}

func TestW13CObjectTimestampInvariants(t *testing.T) {
	if _, err := newFileObject(FileRecord{CreatedAt: "bad-time", ExpiresAt: w13cStrPtr("bad-time")}); err == nil {
		t.Fatal("非法 ExpiresAt 必须报错")
	}
	if _, err := newContainerFileObject(FileRecord{CreatedAt: "bad-time"}); err == nil {
		t.Fatal("非法 CreatedAt 必须报错")
	}
	if _, err := newVectorStoreObject(VectorStoreRecord{CreatedAt: "bad-time"}); err == nil {
		t.Fatal("非法 CreatedAt 必须报错")
	}
	if _, err := newVectorStoreObject(VectorStoreRecord{CreatedAt: invalidRFC3339Value, ExpiresAt: w13cStrPtr("bad-time")}); err == nil {
		t.Fatal("非法 ExpiresAt 必须报错")
	}
	if _, err := newVectorStoreFileObject(VectorStoreFileRecord{CreatedAt: "bad-time"}); err == nil {
		t.Fatal("非法 CreatedAt 必须报错")
	}
}

const invalidRFC3339Value = "2020-01-01T00:00:00+99:00"

func w13cStrPtr(value string) *string { return &value }

func TestW13CWriteJSONBodyMarshalFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSONBody(recorder, http.StatusOK, math.NaN())
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("marshal 失败应 500，实际 %d", recorder.Code)
	}
}

func TestW13CUploadMultipartErrorShapes(t *testing.T) {
	env := newRouteEnv(t, nil)

	// multipart 前缀合法但 boundary 为空：ParseMediaType 失败。
	request := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader("x"))
	request.Header.Set("X-Test-Scope", testScopeA)
	request.Header.Set("Content-Type", "multipart/mixed; boundary=")
	recorder := httptest.NewRecorder()
	if err := env.Deps.uploadFile(recorder, request); err == nil {
		t.Fatal("空 boundary 必须失败")
	}

	// 截断的 multipart：NextPart 错误。
	truncated := "--w13c\r\nContent-Disposition: form-data; name=\"purpose\"\r\n\r\nassistants"
	request = httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(truncated))
	request.Header.Set("X-Test-Scope", testScopeA)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=w13c")
	recorder = httptest.NewRecorder()
	if err := env.Deps.uploadFile(recorder, request); err == nil {
		t.Fatal("截断 multipart 必须失败")
	}

	// 两个 file part：too_many_files。
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("purpose", "assistants"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		part, err := writer.CreateFormFile("file", "a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("data")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/files", body)
	request.Header.Set("X-Test-Scope", testScopeA)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder = httptest.NewRecorder()
	if err := env.Deps.uploadFile(recorder, request); err == nil || !strings.Contains(err.Error(), "每次请求只能上传一个文件") {
		t.Fatalf("双文件必须报 too_many_files，实际 %v", err)
	}

	// 文件 part 的 form name 不是 file：缺少 file 字段。
	body = &bytes.Buffer{}
	writer = multipart.NewWriter(body)
	if err := writer.WriteField("purpose", "assistants"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("attachment", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/files", body)
	request.Header.Set("X-Test-Scope", testScopeA)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder = httptest.NewRecorder()
	if err := env.Deps.uploadFile(recorder, request); err == nil || !strings.Contains(err.Error(), "缺少必填 multipart 文件字段") {
		t.Fatalf("错误命名的文件必须报 missing_file，实际 %v", err)
	}
}

func TestW13CUploadWriteFailureArms(t *testing.T) {
	// FilesRoot 落在一个常规文件之下：MkdirAll 失败 → 写入错误臂。
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := newRouteEnv(t, func(deps *Deps) {
		deps.Config.FilesRoot = filepath.Join(blocker, "nested")
	})
	contentType, payload := multipartBody(t,
		map[string]string{"purpose": "assistants"},
		[]struct {
			Name, Filename, ContentType string
			Content                     []byte
		}{{Name: "file", Filename: "a.txt", ContentType: "text/plain", Content: []byte("hello")}},
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(payload))
	request.Header.Set("X-Test-Scope", testScopeA)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	if err := env.Deps.uploadFile(recorder, request); err == nil {
		t.Fatal("MkdirAll 失败必须上抛")
	}
}

func TestW13CDownloadMissingAndRemovedFile(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := newTestStore(t)
	_ = store
	// 缺失文件的 content 下载 → 400。
	status, _ := env.do(t, "GET", "/v1/files/w13c-missing/content", testScopeA, "", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("缺失内容下载应 400，实际 %d", status)
	}
	// 记录存在但磁盘文件已删除 → 500 errUnhandled。
	record := createTestFile(t, env.Deps.Store, env.FilesRoot, "w13c-gone", "assistants", []byte("payload"), "text/plain", nil)
	filePath, err := FileObjectPath(env.FilesRoot, record.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	status, _ = env.do(t, "GET", "/v1/files/w13c-gone/content", testScopeA, "", nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("磁盘文件缺失应 500，实际 %d", status)
	}
}

package openaicompat

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func multipartBody(t *testing.T, fields map[string]string, files []struct {
	Name, Filename, ContentType string
	Content                     []byte
}) (string, string) {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range files {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, file.Name, file.Filename))
		if file.ContentType != "" {
			header.Set("Content-Type", file.ContentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.Content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), buffer.String()
}

func TestFilesRoutesUpload(t *testing.T) {
	env := newRouteEnv(t, nil)

	tests := []struct {
		name        string
		scope       string
		contentType string
		fields      map[string]string
		files       []struct {
			Name, Filename, ContentType string
			Content                     []byte
		}
		wantStatus int
		wantError  string
		wantCode   string
	}{
		{
			name:        "missing runtime scope -> 401",
			scope:       "",
			contentType: "multipart/form-data; boundary=x",
			wantStatus:  401,
			wantError:   "缺少或无效的 API Key",
			wantCode:    "invalid_api_key",
		},
		{
			name:        "content type mismatch -> 400",
			scope:       testScopeA,
			contentType: "application/json",
			wantStatus:  400,
			wantError:   "文件上传请求必须使用 multipart/form-data",
			wantCode:    "invalid_content_type",
		},
		{
			name:   "missing purpose -> 400 and file removed",
			scope:  testScopeA,
			fields: map[string]string{},
			files: []struct {
				Name, Filename, ContentType string
				Content                     []byte
			}{{Name: "file", Filename: "a.txt", ContentType: "text/plain", Content: []byte("hello")}},
			wantStatus: 400,
			wantError:  "缺少必填 multipart 字段：purpose",
			wantCode:   "missing_purpose",
		},
		{
			name:       "missing file -> 400",
			scope:      testScopeA,
			fields:     map[string]string{"purpose": "assistants"},
			wantStatus: 400,
			wantError:  "缺少必填 multipart 文件字段：file",
			wantCode:   "missing_file",
		},
		{
			name:   "empty file -> 400",
			scope:  testScopeA,
			fields: map[string]string{"purpose": "assistants"},
			files: []struct {
				Name, Filename, ContentType string
				Content                     []byte
			}{{Name: "file", Filename: "a.txt", ContentType: "text/plain"}},
			wantStatus: 400,
			wantError:  "上传文件为空",
			wantCode:   "empty_file",
		},
		{
			name:   "two file parts -> 400 too_many_files",
			scope:  testScopeA,
			fields: map[string]string{"purpose": "assistants"},
			files: []struct {
				Name, Filename, ContentType string
				Content                     []byte
			}{{Name: "file", Filename: "a.txt", ContentType: "text/plain", Content: []byte("x")}, {Name: "file", Filename: "b.txt", ContentType: "text/plain", Content: []byte("y")}},
			wantStatus: 400,
			wantError:  "每次请求只能上传一个文件",
			wantCode:   "too_many_files",
		},
		{
			name:   "success upload persists record and object",
			scope:  testScopeA,
			fields: map[string]string{"purpose": "assistants"},
			files: []struct {
				Name, Filename, ContentType string
				Content                     []byte
			}{{Name: "file", Filename: "data.csv", ContentType: "application/octet-stream", Content: []byte("a,b\n1,2")}},
			wantStatus: 200,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contentType, body := multipartBody(t, tc.fields, tc.files)
			useType := tc.contentType
			if useType == "" {
				useType = contentType
			}
			status, raw := env.do(t, "POST", "/v1/files", tc.scope, useType, strings.NewReader(body))
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body %s", status, tc.wantStatus, raw)
			}
			decoded := decodeJSON(t, raw)
			if tc.wantError != "" {
				errObj := decoded["error"].(map[string]any)
				if errObj["message"] != tc.wantError || errObj["code"] != tc.wantCode {
					t.Fatalf("error payload = %v", errObj)
				}
				if errObj["type"] != "invalid_request_error" {
					t.Fatalf("error type = %v", errObj["type"])
				}
				return
			}
			object := decoded
			if object["object"] != "file" || object["filename"] != "data.csv" || object["purpose"] != "assistants" {
				t.Fatalf("file object = %v", object)
			}
			if object["bytes"] != float64(7) || object["status"] != "processed" {
				t.Fatalf("file object = %v", object)
			}
		})
	}
}

func TestFilesRoutesUploadOversize(t *testing.T) {
	env := newRouteEnv(t, func(deps *Deps) {
		deps.Config.MaxFileBytes = 8
	})
	contentType, body := multipartBody(t, map[string]string{"purpose": "assistants"}, []struct {
		Name, Filename, ContentType string
		Content                     []byte
	}{{Name: "file", Filename: "big.txt", ContentType: "text/plain", Content: []byte("123456789")}})
	status, raw := env.do(t, "POST", "/v1/files", testScopeA, contentType, strings.NewReader(body))
	if status != 413 {
		t.Fatalf("status = %d body %s", status, raw)
	}
	decoded := decodeJSON(t, raw)
	errObj := decoded["error"].(map[string]any)
	if errObj["message"] != "上传文件过大" || errObj["type"] != "request_too_large" || errObj["code"] != "file_too_large" {
		t.Fatalf("error payload = %v", errObj)
	}
}

func TestFilesRoutesGetListDeleteDownload(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := env.Deps.Store
	content := []byte("download-me")
	record := createTestFile(t, store, env.FilesRoot, "file-get", "assistants", content, "text/plain", nil)

	// 正常读取。
	status, raw := env.do(t, "GET", "/v1/files/file-get", testScopeA, "", nil)
	if status != 200 {
		t.Fatalf("status = %d body %s", status, raw)
	}
	object := decodeJSON(t, raw)
	if object["id"] != "file-get" || object["object"] != "file" || object["created_at"] == nil {
		t.Fatalf("file object = %v", object)
	}

	// 字节级 envelope 顺序（id, object, bytes, created_at, filename, purpose, status）。
	if !strings.Contains(raw, `"id":"file-get","object":"file","bytes":11,"created_at":1788508800,"filename":"file-get.txt","purpose":"assistants","status":"processed"}`) {
		t.Fatalf("unexpected envelope bytes: %s", raw)
	}

	// 404。
	status, raw = env.do(t, "GET", "/v1/files/missing", testScopeA, "", nil)
	if status != 404 {
		t.Fatalf("status = %d", status)
	}
	errObj := decodeJSON(t, raw)["error"].(map[string]any)
	if errObj["message"] != "文件不存在" || errObj["code"] != "file_not_found" {
		t.Fatalf("error payload = %v", errObj)
	}

	// 越权。
	status, _ = env.do(t, "GET", "/v1/files/file-get", testScopeB, "", nil)
	if status != 404 {
		t.Fatalf("cross-scope read must 404, got %d", status)
	}

	// 列表 envelope（单条数据：first_id = last_id，无更多页）。
	status, raw = env.do(t, "GET", "/v1/files?limit=1", testScopeA, "", nil)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	list := decodeJSON(t, raw)
	if list["object"] != "list" || list["has_more"] != false {
		t.Fatalf("list = %v", list)
	}
	if list["first_id"] != "file-get" || list["last_id"] != "file-get" {
		t.Fatalf("list ids = %v", list)
	}

	// 下载。
	status, raw = env.do(t, "GET", "/v1/files/file-get/content", testScopeA, "", nil)
	if status != 200 || string(raw) != string(content) {
		t.Fatalf("download = %d %q", status, raw)
	}

	// 删除 envelope（字节级顺序 id, object, deleted）。
	status, raw = env.do(t, "DELETE", "/v1/files/file-get", testScopeA, "", nil)
	if status != 200 || raw != `{"id":"file-get","object":"file","deleted":true}` {
		t.Fatalf("delete = %d %s", status, raw)
	}
	if _, err := os.Stat(filepath.Join(env.FilesRoot, record.StorageKey)); !os.IsNotExist(err) {
		t.Fatalf("physical object should be removed, err=%v", err)
	}
	status, _ = env.do(t, "DELETE", "/v1/files/file-get", testScopeA, "", nil)
	if status != 404 {
		t.Fatalf("second delete = %d", status)
	}
}

func TestFilesRoutesContainerFiles(t *testing.T) {
	env := newRouteEnv(t, nil)
	container := "container-9"
	createTestFile(t, env.Deps.Store, env.FilesRoot, "file-container", "code_interpreter_output", []byte("plot"), "image/png", &container)
	createTestFile(t, env.Deps.Store, env.FilesRoot, "file-other", "assistants", []byte("x"), "text/plain", nil)

	status, raw := env.do(t, "GET", "/v1/containers/container-9/files", testScopeA, "", nil)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	list := decodeJSON(t, raw)
	data := list["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("container list = %v", list)
	}
	item := data[0].(map[string]any)
	if item["object"] != "container.file" || item["container_id"] != container {
		t.Fatalf("container file = %v", item)
	}

	// Container file detail + content.
	status, raw = env.do(t, "GET", "/v1/containers/container-9/files/file-container", testScopeA, "", nil)
	if status != 200 || decodeJSON(t, raw)["container_id"] != container {
		t.Fatalf("detail = %d %s", status, raw)
	}
	status, raw = env.do(t, "GET", "/v1/containers/container-9/files/file-container/content", testScopeA, "", nil)
	if status != 200 || raw != "plot" {
		t.Fatalf("content = %d %s", status, raw)
	}

	// Wrong container / wrong purpose -> 404 容器文件不存在.
	status, raw = env.do(t, "GET", "/v1/containers/other/files/file-container", testScopeA, "", nil)
	if status != 404 {
		t.Fatalf("wrong container = %d %s", status, raw)
	}
	errObj := decodeJSON(t, raw)["error"].(map[string]any)
	if errObj["message"] != "容器文件不存在" || errObj["code"] != "container_file_not_found" {
		t.Fatalf("error = %v", errObj)
	}
	status, _ = env.do(t, "GET", "/v1/containers/container-9/files/file-other", testScopeA, "", nil)
	if status != 404 {
		t.Fatalf("wrong purpose = %d", status)
	}
}

func TestFilesRoutesUploadPersistsUnderStorageRoot(t *testing.T) {
	env := newRouteEnv(t, nil)
	contentType, body := multipartBody(t, map[string]string{"purpose": "batch"}, []struct {
		Name, Filename, ContentType string
		Content                     []byte
	}{{Name: "file", Filename: "notes.txt", ContentType: "text/plain", Content: []byte("persisted")}})
	status, raw := env.do(t, "POST", "/v1/files", testScopeA, contentType, strings.NewReader(body))
	if status != 200 {
		t.Fatalf("status = %d body %s", status, raw)
	}
	id := decodeJSON(t, raw)["id"].(string)
	record, err := env.Deps.Store.FindFile(t.Context(), id, testScopeA, testKeyA)
	if err != nil || record == nil {
		t.Fatalf("record missing: %v", err)
	}
	if !strings.HasPrefix(record.StorageKey, "files/") {
		t.Fatalf("storage key = %s", record.StorageKey)
	}
	onDisk, err := os.ReadFile(filepath.Join(env.FilesRoot, record.StorageKey))
	if err != nil || string(onDisk) != "persisted" {
		t.Fatalf("physical object mismatch: %v %q", err, onDisk)
	}
}

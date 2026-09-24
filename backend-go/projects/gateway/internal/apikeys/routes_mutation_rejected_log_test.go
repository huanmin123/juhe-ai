package apikeys

// api_key_mutation_rejected WARN 回归：PATCH 校验拒绝与 writeMutationError 的
// 原始错误文本必须在 localize 改写为「请求参数无效」之前先落服务端日志（对齐
// internal/routestrategies 先例）；响应行为（状态码与消息）保持不变。

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// TestAPIKeyPatchRejectedWarnLogsOriginalError：经 HTTP PATCH 提交非法 body，
// 断言 WARN 落 api_key_mutation_rejected 且携带原始英文 error 文本，响应仍为
// 400 且消息未被本改动改变。
func TestAPIKeyPatchRejectedWarnLogsOriginalError(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	var buf bytes.Buffer
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	env.deps.MountAuth(k, "lax", false)
	(&Deps{Store: env.store, Auth: env.deps, Sink: env.sink, Log: slog.New(slog.NewTextHandler(&buf, nil))}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)

	jar := map[string]string{}
	do := func(method, path, body string) (int, map[string]any) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request, err := http.NewRequest(method, server.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		for name, value := range jar {
			request.AddCookie(&http.Cookie{Name: name, Value: value})
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		for _, cookie := range response.Cookies() {
			if cookie.Value != "" {
				jar[cookie.Name] = cookie.Value
			} else {
				delete(jar, cookie.Name)
			}
		}
		var payload map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &payload)
		}
		return response.StatusCode, payload
	}
	code, payload := do(http.MethodPost, "/__aisys__/api/auth/login", `{"username":"root","password":"root-pass"}`)
	if code != http.StatusOK {
		t.Fatalf("login: %d %v", code, payload)
	}

	// 缺 expectedRevision → parse 失败消息 "Required"（parse 分支 WARN）。
	// 响应侧保持既有 localize 行为：非中文消息仍被改写为「请求参数无效」，
	// 原文只落入服务端日志——这正是本修复补的落点。
	code, payload = do(http.MethodPatch, "/__aisys__/api/api-keys/key_1", `{"name":"renamed"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400: %v", code, payload)
	}
	if message, _ := payload["message"].(string); message != "请求参数无效" {
		t.Fatalf("patch message = %v, want 请求参数无效 (localize behavior must not change)", payload["message"])
	}
	logs := buf.String()
	if !strings.Contains(logs, "api_key_mutation_rejected") {
		t.Fatalf("logs missing api_key_mutation_rejected: %s", logs)
	}
	if !strings.Contains(logs, "error=Required") {
		t.Fatalf("logs missing original error text: %s", logs)
	}

	// 未知 key → "Unrecognized key(s) in object: 'bogus'" 同口径（schema
	// 顺序：expectedRevision 先于 unknown-key 检查，body 需带 revision）。
	buf.Reset()
	code, _ = do(http.MethodPatch, "/__aisys__/api/api-keys/key_1", `{"name":"renamed","expectedRevision":"rev","bogus":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400", code)
	}
	logs = buf.String()
	if !strings.Contains(logs, "api_key_mutation_rejected") ||
		!strings.Contains(logs, "Unrecognized key(s) in object: 'bogus'") {
		t.Fatalf("logs missing rejected-mutation WARN with original text: %s", logs)
	}
}

// TestAPIKeyWriteMutationErrorWarnLogsOriginalError：writeMutationError 的
// store 错误兜底在响应写向前落 WARN（原文 err.Error()），响应行为不变。
func TestAPIKeyWriteMutationErrorWarnLogsOriginalError(t *testing.T) {
	var buf bytes.Buffer
	deps := &Deps{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/api-keys/key_1", nil)
	deps.writeMutationError(recorder, req, errors.New("Expected object, received null"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (response behavior must not change)", recorder.Code)
	}
	logs := buf.String()
	if !strings.Contains(logs, "api_key_mutation_rejected") {
		t.Fatalf("logs missing api_key_mutation_rejected: %s", logs)
	}
	if !strings.Contains(logs, "Expected object, received null") {
		t.Fatalf("logs missing original error text: %s", logs)
	}
	if !strings.Contains(logs, "path=/__aisys__/api/api-keys/key_1") {
		t.Fatalf("logs missing path field: %s", logs)
	}
}

// TestAPIKeyMutationRejectedNilLogStaysSilent：Log 未接线时保持静默（nil 安全，
// 对齐 routestrategies 先例），响应行为不变。
func TestAPIKeyMutationRejectedNilLogStaysSilent(t *testing.T) {
	deps := &Deps{}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/api-keys/key_1", nil)
	deps.writeMutationError(recorder, req, errors.New("boom"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
}

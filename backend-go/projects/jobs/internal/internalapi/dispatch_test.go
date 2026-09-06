package internalapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSecret = "internal-api-test-secret"

func newTestServer(t *testing.T, dispatch DispatchFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(NewAccountTestDispatchHandler(AccountTestDispatchRouterOptions{
		Secret:   testSecret,
		Dispatch: dispatch,
	}))
	t.Cleanup(server.Close)
	return server
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(AccountTestDispatchSignatureDomain))
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func validBody(taskID string) []byte {
	body, _ := json.Marshal(map[string]any{"version": 1, "taskId": taskID})
	return body
}

func doRequest(t *testing.T, server *httptest.Server, method, path string, headers map[string]string, body []byte) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp, string(payload)
}

func assertJSONMessage(t *testing.T, payload, want string) {
	t.Helper()
	var decoded map[string]string
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("响应不是 JSON: %q", payload)
	}
	if decoded["message"] != want {
		t.Fatalf("message = %q, want %q", decoded["message"], want)
	}
}

// 签名验证矩阵：通过/篡改拒绝/错误域/重放。
func TestAccountTestDispatchSignatureMatrix(t *testing.T) {
	body := validBody("task-1")
	cases := []struct {
		name       string
		headers    map[string]string
		body       []byte
		wantStatus int
		wantBody   string
	}{
		{"有效签名", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody(testSecret, body),
		}, body, http.StatusAccepted, ""},
		{"缺少签名头", map[string]string{"Content-Type": "application/json"}, body, http.StatusUnauthorized, "认证失败"},
		{"签名格式非法", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": "v1=zzzz",
		}, body, http.StatusUnauthorized, "认证失败"},
		{"签名篡改（body 换成不同 taskId）", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody(testSecret, body),
		}, validBody("task-2"), http.StatusUnauthorized, "认证失败"},
		{"错误密钥", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody("other-secret", body),
		}, body, http.StatusUnauthorized, "认证失败"},
		{"错误签名域", func() map[string]string {
			mac := hmac.New(sha256.New, []byte(testSecret))
			_, _ = mac.Write([]byte("wrong:domain\n"))
			_, _ = mac.Write(body)
			return map[string]string{
				"Content-Type":        "application/json",
				"X-Juhe-Ai-Signature": "v1=" + hex.EncodeToString(mac.Sum(nil)),
			}
		}(), body, http.StatusUnauthorized, "认证失败"},
		{"大写十六进制拒绝（Node 正则仅小写）", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": strings.ToUpper(signBody(testSecret, body)),
		}, body, http.StatusUnauthorized, "认证失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t, func(context.Context, string) (bool, error) {
				return true, nil
			})
			resp, payload := doRequest(t, server, http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", tc.headers, tc.body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", resp.StatusCode, tc.wantStatus, payload)
			}
			if tc.wantBody != "" {
				assertJSONMessage(t, payload, tc.wantBody)
			}
			if tc.wantStatus == http.StatusAccepted && !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
				t.Fatal("必须设置 Cache-Control: no-store")
			}
		})
	}

	t.Run("重放窗口：相同签名+body 重复提交被接受（Node 对齐：无时间戳域，幂等由任务队列去重）", func(t *testing.T) {
		count := 0
		server := newTestServer(t, func(context.Context, string) (bool, error) {
			count++
			return true, nil
		})
		headers := map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody(testSecret, body),
		}
		for i := 0; i < 2; i++ {
			resp, _ := doRequest(t, server, http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", headers, body)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("重放第 %d 次应 202，got %d", i+1, resp.StatusCode)
			}
		}
		if count != 2 {
			t.Fatalf("dispatch 调用次数 = %d", count)
		}
	})
}

// 请求前置校验矩阵：loopback/content-type/编码/体积/JSON 形状。
func TestAccountTestDispatchRequestMatrix(t *testing.T) {
	body := validBody("task-1")
	signature := signBody(testSecret, body)
	authHeaders := map[string]string{
		"Content-Type":        "application/json",
		"X-Juhe-Ai-Signature": signature,
	}
	server := newTestServer(t, func(context.Context, string) (bool, error) { return true, nil })

	cases := []struct {
		name       string
		method     string
		path       string
		headers    map[string]string
		body       []byte
		wantStatus int
		wantBody   string
	}{
		{"非 loopback 来源", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", authHeaders, body, 0, ""},
		{"错误方法", http.MethodGet, "/__aiinternal__/v1/account-test/dispatch", authHeaders, nil, http.StatusNotFound, ""},
		{"错误路径", http.MethodPost, "/__aiinternal__/v1/other", authHeaders, body, http.StatusNotFound, ""},
		{"非 JSON content-type", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "text/plain",
			"X-Juhe-Ai-Signature": signature,
		}, body, http.StatusUnsupportedMediaType, "仅支持 JSON 请求"},
		{"JSON 子类型允许", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "application/merge-patch+json",
			"X-Juhe-Ai-Signature": signature,
		}, body, http.StatusAccepted, ""},
		{"gzip 编码拒绝", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "application/json",
			"Content-Encoding":    "gzip",
			"X-Juhe-Ai-Signature": signature,
		}, body, http.StatusUnsupportedMediaType, "不支持压缩请求体"},
		{"identity 编码允许", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "application/json",
			"Content-Encoding":    "identity",
			"X-Juhe-Ai-Signature": signature,
		}, body, http.StatusAccepted, ""},
		{"body 超过 1024 字节", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody(testSecret, []byte(`{"version":1,"taskId":"`+strings.Repeat("x", 1100)+`"}`)),
		}, []byte(`{"version":1,"taskId":"` + strings.Repeat("x", 1100) + `"}`), http.StatusRequestEntityTooLarge, "请求体过大"},
		// Node：express.raw 不解析 JSON，无效 JSON 在签名校验后于 parseTaskID 失败。
		{"无效 JSON + 有效签名 → 400 请求参数无效", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody(testSecret, []byte("{invalid")),
		}, []byte("{invalid"), http.StatusBadRequest, "请求参数无效"},
		{"无效 JSON 无签名 → 401", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type": "application/json",
		}, []byte("{invalid"), http.StatusUnauthorized, "认证失败"},
		{"尾随 JSON 值 + 有效签名 → 400 请求参数无效", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", func() map[string]string {
			body := append(validBody("task-1"), []byte(` {}`)...)
			return map[string]string{
				"Content-Type":        "application/json",
				"X-Juhe-Ai-Signature": signBody(testSecret, body),
			}
		}(), append(validBody("task-1"), []byte(` {}`)...), http.StatusBadRequest, "请求参数无效"},
		{"非法 UTF-8 + 有效签名 → 400 请求参数无效", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", func() map[string]string {
			body := append(validBody("task-1"), byte(0xff))
			return map[string]string{
				"Content-Type":        "application/json",
				"X-Juhe-Ai-Signature": signBody(testSecret, body),
			}
		}(), append(validBody("task-1"), byte(0xff)), http.StatusBadRequest, "请求参数无效"},
		{"taskId 缺失", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", authHeadersJSON(`{"version":1}`), []byte(`{"version":1}`), http.StatusBadRequest, "请求参数无效"},
		{"多一个字段", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", authHeadersJSON(`{"version":1,"taskId":"a","extra":true}`), []byte(`{"version":1,"taskId":"a","extra":true}`), http.StatusBadRequest, "请求参数无效"},
		{"version 非 1", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", authHeadersJSON(`{"version":2,"taskId":"a"}`), []byte(`{"version":2,"taskId":"a"}`), http.StatusBadRequest, "请求参数无效"},
		{"taskId 空白", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", authHeadersJSON(`{"version":1,"taskId":"   "}`), []byte(`{"version":1,"taskId":"   "}`), http.StatusBadRequest, "请求参数无效"},
		{"数组 body", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", authHeadersJSON(`[]`), []byte(`[]`), http.StatusBadRequest, "请求参数无效"},
		{"taskId trim 后接受", http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", authHeadersJSON(`{"version":1,"taskId":"  task-9  "}`), []byte(`{"version":1,"taskId":"  task-9  "}`), http.StatusAccepted, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantStatus == 0 {
				// 非 loopback：httptest 客户端来源是本机回环端口，
				// 这里直接用单元函数断言（等价 Node BlockList 语义）。
				if !IsLoopbackRemoteAddress("127.0.0.1:64333") || IsLoopbackRemoteAddress("10.0.0.8:1234") || IsLoopbackRemoteAddress("") {
					t.Fatal("loopback 判定与 Node BlockList 不一致")
				}
				return
			}
			resp, payload := doRequest(t, server, tc.method, tc.path, tc.headers, tc.body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", resp.StatusCode, tc.wantStatus, payload)
			}
			if tc.wantBody != "" {
				assertJSONMessage(t, payload, tc.wantBody)
			}
		})
	}

	t.Run("taskId 透传", func(t *testing.T) {
		var got string
		server := newTestServer(t, func(_ context.Context, taskID string) (bool, error) {
			got = taskID
			return true, nil
		})
		taskBody := validBody("task-77")
		resp, _ := doRequest(t, server, http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody(testSecret, taskBody),
		}, taskBody)
		if resp.StatusCode != http.StatusAccepted || got != "task-77" {
			t.Fatalf("status=%d taskId=%q", resp.StatusCode, got)
		}
	})
}

func authHeadersJSON(body string) map[string]string {
	return map[string]string{
		"Content-Type":        "application/json",
		"X-Juhe-Ai-Signature": signBody(testSecret, []byte(body)),
	}
}

// 派发结果映射：false → 503 服务暂不可用；error → 500。
func TestAccountTestDispatchOutcomeMapping(t *testing.T) {
	t.Run("拒绝 → 503", func(t *testing.T) {
		server := newTestServer(t, func(context.Context, string) (bool, error) { return false, nil })
		resp, payload := doRequest(t, server, http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody(testSecret, validBody("task-1")),
		}, validBody("task-1"))
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		assertJSONMessage(t, payload, "服务暂不可用")
	})
	t.Run("错误 → 500", func(t *testing.T) {
		server := newTestServer(t, func(context.Context, string) (bool, error) { return false, context.DeadlineExceeded })
		resp, _ := doRequest(t, server, http.MethodPost, "/__aiinternal__/v1/account-test/dispatch", map[string]string{
			"Content-Type":        "application/json",
			"X-Juhe-Ai-Signature": signBody(testSecret, validBody("task-1")),
		}, validBody("task-1"))
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
}

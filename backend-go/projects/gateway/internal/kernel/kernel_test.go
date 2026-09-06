package kernel

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestKernel(t *testing.T, ready func() (int, any)) *Kernel {
	t.Helper()
	return New(Options{Readiness: ready, TrustProxyCount: 1})
}

func TestSecurityHeadersOnManagementPrefixOnly(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("GET /__aisys__/api/ping", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, map[string]string{"pong": "1"}, "")
	})
	k.RegisterFunc("GET /other/ping", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, map[string]string{"pong": "1"}, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	management, err := http.Get(server.URL + "/__aisys__/api/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer management.Body.Close()
	if got := management.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("management X-Frame-Options = %q, want DENY", got)
	}
	if got := management.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := management.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
	if !strings.Contains(management.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing CSP frame-ancestors")
	}
	if got := management.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("API Cache-Control = %q, want no-store", got)
	}

	other, err := http.Get(server.URL + "/other/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Body.Close()
	if got := other.Header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("non-management X-Frame-Options = %q, want absent", got)
	}
	if got := other.Header.Get("Cache-Control"); got == "no-store" {
		t.Fatal("non-management response must not be no-store")
	}
}

func TestErrorLocalizationContract(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("POST /__aisys__/api/echo-error", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		_ = DecodeJSON(w, r, &body)
		switch body.Mode {
		case "english":
			WriteError(w, http.StatusBadRequest, "something failed")
		case "chinese":
			WriteError(w, http.StatusBadRequest, "自定义中文错误")
		case "nested":
			WriteJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "nested english error"}})
		case "upstream":
			MarkUpstreamError(w)
			WriteError(w, http.StatusBadGateway, "upstream said no")
		case "plain-string":
			WriteJSON(w, http.StatusUnprocessableEntity, "plain failure")
		default:
			WriteError(w, http.StatusNotFound, "missing")
		}
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	post := func(mode string) map[string]any {
		response, err := http.Post(server.URL+"/__aisys__/api/echo-error", "application/json", strings.NewReader(`{"mode":"`+mode+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(response.Body).Decode(&payload)
		return payload
	}

	postRaw := func(mode string) string {
		response, err := http.Post(server.URL+"/__aisys__/api/echo-error", "application/json", strings.NewReader(`{"mode":"`+mode+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return string(raw)
	}

	if got := post("english")["message"]; got != "请求参数无效" {
		t.Fatalf("english 400 message = %v, want localized", got)
	}
	if got := post("chinese")["message"]; got != "自定义中文错误" {
		t.Fatalf("chinese message must be preserved, got %v", got)
	}
	nested := post("nested")
	if errorMap, ok := nested["error"].(map[string]any); !ok || errorMap["message"] != "请求参数无效" {
		t.Fatalf("nested error.message not localized: %v", nested)
	}
	if got := post("upstream")["message"]; got != "upstream said no" {
		t.Fatalf("upstream-marked message must be preserved, got %v", got)
	}
	// res.json("string") emits a bare JSON string literal, localized.
	if got := postRaw("plain-string"); got != "\"请求内容无法处理\"" {
		t.Fatalf("string payload not localized: %q", got)
	}
	if got := post("missing")["message"]; got != "请求的资源不存在" {
		t.Fatalf("default 404 body mismatch: %v", got)
	}
}

func TestSuccessEnvelope(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("GET /__aisys__/api/ok", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, map[string]int{"n": 1}, "已创建")
	})
	k.RegisterFunc("GET /__aisys__/api/ok-quiet", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, map[string]int{"n": 2}, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/__aisys__/api/ok")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	if payload["data"].(map[string]any)["n"] != float64(1) || payload["message"] != "已创建" {
		t.Fatalf("envelope mismatch: %s", raw)
	}

	quiet, err := http.Get(server.URL + "/__aisys__/api/ok-quiet")
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Body.Close()
	rawQuiet, _ := io.ReadAll(quiet.Body)
	if strings.Contains(string(rawQuiet), "message") {
		t.Fatalf("empty message must be omitted: %s", rawQuiet)
	}
}

func TestCompressionThresholdAndSkips(t *testing.T) {
	k := newTestKernel(t, nil)
	large := strings.Repeat("juhe", 512) // 2048 bytes
	k.RegisterFunc("GET /large", func(w http.ResponseWriter, r *http.Request) {
		// Node compression.filter: a response without Content-Type is never
		// compressible, so the golden declares text/plain like res.send.
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(large))
	})
	k.RegisterFunc("GET /small", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("tiny"))
	})
	k.RegisterFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 100; i++ {
			_, _ = w.Write([]byte("data: x\n\n"))
		}
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	request := func(path, encoding string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if encoding != "" {
			req.Header.Set("Accept-Encoding", encoding)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response, body
	}

	response, body := request("/large", "gzip")
	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("large body must be gzipped")
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, _ := io.ReadAll(reader)
	if string(decompressed) != large {
		t.Fatal("gzip payload mismatch")
	}

	small, _ := request("/small", "gzip")
	if small.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("small body must stay uncompressed")
	}
	if string(body) != "" && small.StatusCode != http.StatusOK {
		t.Fatalf("small status = %d", small.StatusCode)
	}

	stream, streamBody := request("/stream", "gzip")
	if stream.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("event-stream must never be compressed")
	}
	if !strings.Contains(string(streamBody), "data: x") {
		t.Fatal("stream body missing")
	}

	plain, _ := request("/large", "")
	if plain.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("client without gzip must not receive gzip")
	}
}

func TestJSONBodyLimitAndDecodeContract(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("POST /__aisys__/api/decode", func(w http.ResponseWriter, r *http.Request) {
		var target map[string]any
		if DecodeJSON(w, r, &target) {
			WriteOK(w, target, "")
		}
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	oversized := `{"blob":"` + strings.Repeat("x", 300*1024) + `"}`
	response, err := http.Post(server.URL+"/__aisys__/api/decode", "application/json", strings.NewReader(oversized))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(raw), "请求体过大") {
		t.Fatalf("oversized body: status=%d body=%s", response.StatusCode, raw)
	}

	malformed, err := http.Post(server.URL+"/__aisys__/api/decode", "application/json", strings.NewReader("{nope"))
	if err != nil {
		t.Fatal(err)
	}
	defer malformed.Body.Close()
	rawMalformed, _ := io.ReadAll(malformed.Body)
	if malformed.StatusCode != http.StatusBadRequest || !strings.Contains(string(rawMalformed), "请求体无效") {
		t.Fatalf("malformed body: status=%d body=%s", malformed.StatusCode, rawMalformed)
	}

	empty, err := http.Post(server.URL+"/__aisys__/api/decode", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Body.Close()
	if empty.StatusCode != http.StatusOK {
		t.Fatalf("empty body must decode as untouched target, got %d", empty.StatusCode)
	}
}

func TestNotFoundAndMethodMismatchContract(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("GET /__aisys__/api/only-get", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, nil, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	missing, err := http.Get(server.URL + "/__aisys__/api/nothing")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	raw, _ := io.ReadAll(missing.Body)
	if missing.StatusCode != http.StatusNotFound || !strings.Contains(string(raw), "资源不存在") {
		t.Fatalf("unmatched path: status=%d body=%s", missing.StatusCode, raw)
	}

	wrongMethod, err := http.Post(server.URL+"/__aisys__/api/only-get", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer wrongMethod.Body.Close()
	rawMethod, _ := io.ReadAll(wrongMethod.Body)
	if wrongMethod.StatusCode != http.StatusNotFound || !strings.Contains(string(rawMethod), "资源不存在") {
		t.Fatalf("method mismatch must fall through to 404 JSON: status=%d body=%s", wrongMethod.StatusCode, rawMethod)
	}
}

// 显式 405 豁免：Node 中间件（requireHelpSession 等）直接写
// res.status(405).json(...)，该响应必须原样到达客户端；只有 mux 自身的
// 方法不匹配才落到 404 JSON（TestNotFoundAndMethodMismatchContract）。
func TestExplicitMethodContractKeepsHandlerWritten405(t *testing.T) {
	k := newTestKernel(t, nil)
	// 无方法 pattern：所有方法进入 handler（helpweb 挂载方式）。
	k.RegisterFunc("/__aisys__/help", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			MarkExplicitMethodContract(w)
			WriteError(w, http.StatusMethodNotAllowed, "帮助文档只支持读取")
			return
		}
		WriteOK(w, nil, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	post, err := http.Post(server.URL+"/__aisys__/help", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer post.Body.Close()
	raw, _ := io.ReadAll(post.Body)
	if post.StatusCode != http.StatusMethodNotAllowed || string(raw) != `{"message":"帮助文档只支持读取"}` {
		t.Fatalf("explicit 405 must survive: status=%d body=%s", post.StatusCode, raw)
	}

	// 同一 handler 的 GET 分支不受影响。
	get, err := http.Get(server.URL + "/__aisys__/help")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get branch broken: %d", get.StatusCode)
	}
}

func TestMutationGuardLifecycle(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_000_000, 0)}
	store := NewDeduplicationStore(clock.Now)
	k := newTestKernel(t, nil)
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "test.op",
		Store:        store,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"name": TextField(BodyField(r, "name"))}, nil
		},
	})
	handlerSucceeds := true
	innerNext := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		println("INNER NEXT REACHED, succeeds=", handlerSucceeds)
		if !handlerSucceeds {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		WriteOK(w, nil, "")
	})
	k.Register("POST /__aisys__/api/guarded", guard(innerNext))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	post := func() *http.Response {
		response, err := http.Post(server.URL+"/__aisys__/api/guarded", "application/json", strings.NewReader(`{"name":"a"}`))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := post()
	rawFirst, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first claim must pass, got %d body=%s", first.StatusCode, rawFirst)
	}

	second := post()
	rawSecond, _ := io.ReadAll(second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusConflict || !strings.Contains(string(rawSecond), "该操作刚刚已处理") {
		t.Fatalf("succeeded duplicate must 409, got %d body=%s", second.StatusCode, rawSecond)
	}

	clock.advance(61 * time.Second)
	handlerSucceeds = false
	third := post()
	thirdRaw, _ := io.ReadAll(third.Body)
	third.Body.Close()
	t.Logf("third: status=%d body=%q headers=%v", third.StatusCode, string(thirdRaw), third.Header)
	// The claim must pass the guard (not 409); the handler itself fails with
	// 500, which completes the claim as failed for the next assertion.
	if third.StatusCode != http.StatusInternalServerError {
		t.Fatalf("claim after TTL must reach handler, got %d", third.StatusCode)
	}

	fourth := post()
	rawFourth, _ := io.ReadAll(fourth.Body)
	fourth.Body.Close()
	if fourth.StatusCode != http.StatusConflict || !strings.Contains(string(rawFourth), "请求刚刚失败") {
		t.Fatalf("failed duplicate message mismatch: %d %s", fourth.StatusCode, rawFourth)
	}

	clock.advance(11 * time.Second)
	handlerSucceeds = true
}

func TestTraceAndClientIPContext(t *testing.T) {
	k := newTestKernel(t, nil)
	var seen *RequestContext
	k.RegisterFunc("GET /ctx", func(w http.ResponseWriter, r *http.Request) {
		seen = Context(r)
		WriteOK(w, nil, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/ctx", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	request.Header.Set("X-Forwarded-For", "203.0.113.9, 70.41.3.18")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if seen == nil {
		t.Fatal("request context missing")
	}
	if seen.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("traceparent trace id not honored: %q", seen.TraceID)
	}
	if seen.ClientIP != "70.41.3.18" {
		t.Fatalf("trusted forwarded IP = %q", seen.ClientIP)
	}

	plain, _ := http.Get(server.URL + "/ctx")
	plain.Body.Close()
	if len(seen.TraceID) != 36 {
		t.Fatalf("generated trace id must be UUID-shaped, got %q", seen.TraceID)
	}
}

type manualClock struct{ now time.Time }

func (c *manualClock) Now() time.Time          { return c.now }
func (c *manualClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// 回归测试：DedupNoRetention 必须在 Complete 时删除条目（Node mutationGuard
// succeededTtlMs/failedTtlMs 0 的「不保留」语义）。冻结时钟下任何正 TTL 都
// 永不过期，历史实现以 time.Nanosecond 近似导致同指纹重试被 409 拦死
// （accounts runtime-reset TestRuntimeResetOwnerAccount 复现）。
func TestMutationGuardNoRetentionAllowsImmediateRetry(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_000_000, 0)}
	store := NewDeduplicationStore(clock.Now)
	k := newTestKernel(t, nil)
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "test.noretention",
		Store:        store,
		SucceededTTL: DedupNoRetention,
		FailedTTL:    DedupNoRetention,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"name": TextField(BodyField(r, "name"))}, nil
		},
	})
	attempts := 0
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		WriteOK(w, nil, "")
	}))
	k.Register("POST /__aisys__/api/guarded-noretention", handler)
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	body := strings.NewReader(`{"name":"same"}`)
	resp, err := http.Post(server.URL+"/__aisys__/api/guarded-noretention", "application/json", body)
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("first attempt must fail with 400: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	// 冻结时钟未推进：失败条目若被保留（默认 TTL），本次 claim 必 409。
	resp2, err := http.Post(server.URL+"/__aisys__/api/guarded-noretention", "application/json", strings.NewReader(`{"name":"same"}`))
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("no-retention must allow immediate retry under frozen clock: %v %d", err, resp2.StatusCode)
	}
	resp2.Body.Close()
}

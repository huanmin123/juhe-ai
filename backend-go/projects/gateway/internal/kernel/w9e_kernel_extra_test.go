package kernel

// w9e 覆盖率战役：补 kernel 包 envelope/localize/security/ctx/dedupe/
// compression/observability 的未覆盖分支。全部用 httptest 或直接函数调用，
// 不依赖外部资源；全局 hook（metric/sink）用完即还原，避免污染其他测试。

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestW9ESystemErrorMessageForStatusAllBranches(t *testing.T) {
	cases := map[int]string{
		http.StatusBadRequest:            "请求参数无效",
		http.StatusUnauthorized:          "身份验证失败，请检查访问凭据",
		http.StatusForbidden:             "无权执行此操作",
		http.StatusNotFound:              "请求的资源不存在",
		http.StatusMethodNotAllowed:      "请求方法不被支持",
		http.StatusConflict:              "请求状态已发生变化，请刷新后重试",
		http.StatusRequestEntityTooLarge: "请求内容过大",
		http.StatusUnprocessableEntity:   "请求内容无法处理",
		http.StatusTooManyRequests:       "请求过于频繁，请稍后重试",
		http.StatusBadGateway:            "服务处理上游响应失败，请稍后重试",
		http.StatusServiceUnavailable:    "服务暂时不可用，请稍后重试",
		http.StatusGatewayTimeout:        "服务处理超时，请稍后重试",
		http.StatusInternalServerError:   "请求处理失败，请稍后重试",
		http.StatusTeapot:                "请求处理失败，请稍后重试",
	}
	for status, want := range cases {
		if got := SystemErrorMessageForStatus(status); got != want {
			t.Fatalf("status %d message = %q, want %q", status, got, want)
		}
	}
}

func TestW9ELocalizeSystemErrorPayloadPlainText(t *testing.T) {
	// 非 JSON 的明文 payload 在解析失败后按纯文本 JSON 重写。
	encoded, changed := localizeSystemErrorPayload([]byte("  upstream blew up  "), http.StatusInternalServerError, false)
	if !changed {
		t.Fatal("plain text payload should be localized")
	}
	if string(encoded) != "请求处理失败，请稍后重试" {
		t.Fatalf("localized = %q", string(encoded))
	}
	// 以 { 开头的非法 JSON 不重写。
	if _, changed := localizeSystemErrorPayload([]byte("{not-json"), http.StatusInternalServerError, false); changed {
		t.Fatal("object-looking invalid JSON should stay untouched")
	}
	// 字符串标量 payload 重写为带引号的 JSON 字符串。
	encoded, changed = localizeSystemErrorPayload([]byte(`"boom"`), http.StatusBadGateway, false)
	if !changed || string(encoded) != `"服务处理上游响应失败，请稍后重试"` {
		t.Fatalf("scalar payload = %q changed=%v", string(encoded), changed)
	}
	// message 已含中文则保持原样。
	if _, changed := localizeSystemErrorPayload([]byte(`{"message":"业务原始错误"}`), 500, false); changed {
		t.Fatal("CJK message should stay untouched")
	}
	// 无 message 字段不重写。
	if _, changed := localizeSystemErrorPayload([]byte(`{"data":1}`), 500, false); changed {
		t.Fatal("message-less payload should stay untouched")
	}
	// 嵌套 error.message 重写。
	encoded, changed = localizeSystemErrorPayload([]byte(`{"error":{"message":"raw upstream"}}`), 500, false)
	if !changed {
		t.Fatal("nested error.message should be localized")
	}
	if !strings.Contains(string(encoded), "请求处理失败，请稍后重试") {
		t.Fatalf("nested localization = %q", string(encoded))
	}
	// 数组 payload 走 default 分支。
	if _, changed := localizeSystemErrorPayload([]byte(`[1,2]`), 500, false); changed {
		t.Fatal("array payload should stay untouched")
	}
}

func TestW9EIsPlainTextJSON(t *testing.T) {
	if !isPlainTextJSON([]byte("plain")) {
		t.Fatal("plain text should be detected")
	}
	if isPlainTextJSON([]byte(`{"a":1}`)) {
		t.Fatal("object is not plain text")
	}
	if isPlainTextJSON([]byte(`[1]`)) {
		t.Fatal("array is not plain text")
	}
	if isPlainTextJSON([]byte(`"str"`)) {
		t.Fatal("quoted string is not plain text")
	}
	if isPlainTextJSON([]byte("   ")) {
		t.Fatal("blank payload is not plain text")
	}
}

func TestW9ESessionCookieOptionsApply(t *testing.T) {
	cookie := &http.Cookie{Name: "sid", Value: "v"}
	SessionCookieOptions{SameSite: "strict", Secure: true}.Apply(cookie)
	if cookie.SameSite != http.SameSiteStrictMode || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" {
		t.Fatalf("strict cookie = %+v", cookie)
	}
	cookie = &http.Cookie{Name: "sid"}
	SessionCookieOptions{SameSite: "none"}.Apply(cookie)
	if cookie.SameSite != http.SameSiteNoneMode || cookie.Secure {
		t.Fatalf("none cookie = %+v", cookie)
	}
	cookie = &http.Cookie{Name: "sid"}
	SessionCookieOptions{SameSite: "lax"}.Apply(cookie)
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("lax cookie = %+v", cookie)
	}
	cookie = &http.Cookie{Name: "sid"}
	SessionCookieOptions{SameSite: "bogus"}.Apply(cookie)
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unknown same-site falls back to lax, got %v", cookie.SameSite)
	}
}

func TestW9ENotFoundAndHealthHandlers(t *testing.T) {
	rec := httptest.NewRecorder()
	NotFoundHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nowhere", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("NotFoundHandler status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "资源不存在") {
		t.Fatalf("NotFoundHandler body = %q", rec.Body.String())
	}

	okCalled := false
	rec = httptest.NewRecorder()
	HealthHandler(func() (int, any) { okCalled = true; return http.StatusOK, map[string]string{"status": "ok"} }).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !okCalled || rec.Code != http.StatusOK {
		t.Fatalf("HealthHandler status = %d called=%v", rec.Code, okCalled)
	}
	rec = httptest.NewRecorder()
	HealthHandler(func() (int, any) { return http.StatusServiceUnavailable, map[string]string{"status": "degraded"} }).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded health status = %d", rec.Code)
	}
}

func TestW9EEnvelopeHelpers(t *testing.T) {
	// WriteNotFound / WriteBadRequest / WriteRawJSON 直连契约。
	rec := httptest.NewRecorder()
	WriteNotFound(rec, "缺失")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "缺失") {
		t.Fatalf("WriteNotFound = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	WriteBadRequest(rec, "参数")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "参数") {
		t.Fatalf("WriteBadRequest = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	WriteRawJSON(rec, http.StatusOK, []byte(`{"raw":true}`))
	if rec.Body.String() != `{"raw":true}` {
		t.Fatalf("WriteRawJSON body = %q", rec.Body.String())
	}
	// localizeWriter：Write 隐式 200 + StatusCode 记录。
	lw := newLocalizeWriter(rec)
	if lw.StatusCode() != 0 {
		t.Fatalf("fresh localizeWriter status = %d", lw.StatusCode())
	}
	if _, err := lw.Write([]byte("hello")); err != nil {
		t.Fatalf("localizeWriter.Write err = %v", err)
	}
	if lw.StatusCode() != http.StatusOK {
		t.Fatalf("implicit status = %d", lw.StatusCode())
	}
	// writeJSON marshal 失败回退 500 envelope。
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, make(chan int), false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("marshal fallback status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "服务器内部错误") {
		t.Fatalf("marshal fallback body = %q", rec.Body.String())
	}
}

func TestW9EMountPathMatch(t *testing.T) {
	cases := []struct {
		path, prefix string
		want         bool
	}{
		{"/api/x", "", true},
		{"/api/x", "/", true},
		{"/api", "/api", true},
		{"/api/x", "/api", true},
		{"/apix", "/api", false},
		{"/api", "/ap", false},
	}
	for _, tc := range cases {
		if got := mountPathMatch(tc.path, tc.prefix); got != tc.want {
			t.Fatalf("mountPathMatch(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
		}
	}
}

func TestW9EContextFallbackWithoutMiddleware(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/plain/path", nil)
	ctx := Context(r)
	if ctx == nil || ctx.TraceID == "" || ctx.RequestID == "" {
		t.Fatalf("fallback context = %+v", ctx)
	}
	if ctx.Method != http.MethodPost || ctx.Path != "/plain/path" {
		t.Fatalf("fallback context fields = %+v", ctx)
	}
}

func TestW9EKernelHandlerMarkedUpstreamAndEnvelope(t *testing.T) {
	k := New(Options{CompressionDisabled: true})
	var sawWriter bool
	k.RegisterFunc("POST /__aisys__/api/w9e-upstream", func(w http.ResponseWriter, r *http.Request) {
		MarkUpstreamError(w)
		lw := ResponseWriterFromContext(r.Context())
		sawWriter = lw != nil && lw.MarkedUpstream()
		WriteOK(w, map[string]string{"ok": "1"}, "原始消息保留")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()
	resp, err := http.Post(server.URL+"/__aisys__/api/w9e-upstream", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !sawWriter {
		t.Fatal("handler should observe a marked localizeWriter via context")
	}
}

func TestW9EMethodContractWriterBranches(t *testing.T) {
	// 显式 405：handler 先 MarkExplicitMethodContract，405 原样透传。
	k := New(Options{CompressionDisabled: true})
	k.RegisterFunc("GET /__aisys__/api/w9e-explicit-405", func(w http.ResponseWriter, r *http.Request) {
		MarkExplicitMethodContract(w)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"message":"显式405"}`))
	})
	k.RegisterFunc("GET /__aisys__/api/w9e-plain-405", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"message":"应被转换"}`))
	})
	k.RegisterFunc("GET /__aisys__/api/w9e-ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fine"))
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/__aisys__/api/w9e-explicit-405")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 256)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(string(body[:n]), "显式405") {
		t.Fatalf("explicit 405 = %d %q", resp.StatusCode, string(body[:n]))
	}

	resp, err = http.Get(server.URL + "/__aisys__/api/w9e-plain-405")
	if err != nil {
		t.Fatal(err)
	}
	body = make([]byte, 256)
	n, _ = resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unmarked 405 should convert to 404, got %d", resp.StatusCode)
	}
	if strings.Contains(string(body[:n]), "应被转换") {
		t.Fatalf("converted body should swallow handler body, got %q", string(body[:n]))
	}

	resp, err = http.Get(server.URL + "/__aisys__/api/w9e-ok")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plain 200 = %d", resp.StatusCode)
	}
}

func TestW9ECompressionHelpersUnit(t *testing.T) {
	header := http.Header{}
	if !compressionBelowThreshold(header, true, 100) {
		t.Fatal("estimate below threshold should compress")
	}
	if compressionBelowThreshold(header, false, 100) {
		t.Fatal("unknown estimate should never be below threshold")
	}
	header.Set("Content-Length", "10")
	if !compressionBelowThreshold(header, false, 999999) {
		t.Fatal("small content-length should be below threshold")
	}
	header.Set("Content-Length", "999999")
	if compressionBelowThreshold(header, true, 1) {
		t.Fatal("large content-length should not be below threshold")
	}
	header.Set("Content-Length", "not-a-number")
	if compressionBelowThreshold(header, true, 1) {
		t.Fatal("unparseable content-length should never be below threshold")
	}

	sse := http.Header{}
	sse.Set("Content-Type", "text/event-stream")
	if compressionFilter(sse) {
		t.Fatal("SSE should not compress")
	}
	encoded := http.Header{}
	encoded.Set("Content-Encoding", "gzip")
	if compressionFilter(encoded) {
		t.Fatal("already-encoded response should not compress")
	}
	attachment := http.Header{}
	attachment.Set("Content-Type", "application/json")
	attachment.Set("Content-Disposition", "attachment; filename=a.json")
	if compressionFilter(attachment) {
		t.Fatal("attachment should not compress")
	}
	compressible := http.Header{}
	compressible.Set("Content-Type", "application/json; charset=utf-8")
	if !compressionFilter(compressible) {
		t.Fatal("JSON should compress")
	}
	incompressible := http.Header{}
	incompressible.Set("Content-Type", "image/png")
	if compressionFilter(incompressible) {
		t.Fatal("png should not compress")
	}
	fallback := http.Header{}
	fallback.Set("Content-Type", "text/csv")
	if !compressionFilter(fallback) {
		t.Fatal("text/* should compress via fallback")
	}

	// vary 处理
	h := http.Header{}
	varyAcceptEncoding(h)
	if h.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("vary = %q", h.Get("Vary"))
	}
	h.Set("Vary", "Origin")
	varyAcceptEncoding(h)
	if h.Get("Vary") != "Origin, Accept-Encoding" {
		t.Fatalf("vary append = %q", h.Get("Vary"))
	}
	h.Set("Vary", "ACCEPT-ENCODING")
	varyAcceptEncoding(h)
	if h.Get("Vary") != "ACCEPT-ENCODING" {
		t.Fatalf("vary dedupe (case-insensitive) = %q", h.Get("Vary"))
	}
	h.Set("Vary", "*")
	varyAcceptEncoding(h)
	if h.Get("Vary") != "*" {
		t.Fatalf("vary star preserved = %q", h.Get("Vary"))
	}
	if got := appendVaryField("accept-encoding", "Accept-Encoding"); got != "accept-encoding" {
		t.Fatalf("appendVaryField dedupe = %q", got)
	}
	if !sliceContainsString([]string{"a", "b"}, "b") || sliceContainsString([]string{"a"}, "c") {
		t.Fatal("sliceContainsString contract broken")
	}
	if got := keyValueValue([]string{"q", "0.5"}); got != "0.5" {
		t.Fatalf("keyValueValue pair = %q", got)
	}
	if got := keyValueValue([]string{"q"}); got != "" {
		t.Fatalf("keyValueValue single = %q", got)
	}

	// recordedStatus 在 WriteHeader 后可读，之前不可读。
	cw := &compressionWriter{ResponseWriter: httptest.NewRecorder()}
	if _, ok := cw.recordedStatus(); ok {
		t.Fatal("fresh compression writer has no recorded status")
	}
	cw.WriteHeader(http.StatusTeapot)
	status, ok := cw.recordedStatus()
	if !ok || status != http.StatusTeapot {
		t.Fatalf("recordedStatus = %d %v", status, ok)
	}
	// MarkedUpstream 向下转发。
	inner := newLocalizeWriter(httptest.NewRecorder())
	inner.MarkUpstream()
	wrapper := &compressionWriter{ResponseWriter: inner}
	if !wrapper.MarkedUpstream() {
		t.Fatal("compressionWriter.MarkedUpstream should forward to marker")
	}
	plain := &compressionWriter{ResponseWriter: httptest.NewRecorder()}
	if plain.MarkedUpstream() {
		t.Fatal("non-marker inner should report false")
	}
}

func TestW9EDedupeStoreTrim(t *testing.T) {
	store := NewDeduplicationStore(func() time.Time { return time.Unix(0, 0) })
	// 填充超过 dedupMaxEntries 个条目触发 trimIfNeeded 的淘汰分支。
	overflow := dedupMaxEntries + 2
	for i := 0; i < overflow; i++ {
		claimed, _ := store.Claim("actor:scope:POST:op:key-"+string(rune('a'+i%26))+time.Unix(0, int64(i)).Format("150405.000000000"), "op", time.Hour)
		if !claimed {
			t.Fatalf("claim %d should succeed with unique keys", i)
		}
	}
	store.mu.Lock()
	size := len(store.entry)
	store.mu.Unlock()
	if size > dedupMaxEntries {
		t.Fatalf("store size %d exceeds cap %d after trim", size, dedupMaxEntries)
	}
}

func TestW9EDedupeStoreCompleteBranches(t *testing.T) {
	clockNow := time.Unix(1000, 0)
	store := NewDeduplicationStore(func() time.Time { return clockNow })
	key := "a:b:POST:op:fp"
	store.Claim(key, "op", time.Hour)
	// 完成不存在的 key 是 no-op。
	store.Complete("missing", DedupSucceeded, time.Minute, time.Minute)
	// 成功完成采用 succeeded TTL。
	store.Complete(key, DedupSucceeded, 2*time.Minute, time.Minute)
	store.mu.Lock()
	entry := *store.entry[key]
	store.mu.Unlock()
	if entry.Status != DedupSucceeded || !entry.ExpiresAt.Equal(clockNow.Add(2*time.Minute)) {
		t.Fatalf("completed entry = %+v", entry)
	}
	// 再次完成（非 processing）是 no-op。
	store.Complete(key, DedupFailed, time.Minute, time.Minute)
	// DedupNoRetention 删除条目。
	key2 := "a:b:POST:op:fp2"
	store.Claim(key2, "op", time.Hour)
	store.Complete(key2, DedupSucceeded, DedupNoRetention, DedupNoRetention)
	store.mu.Lock()
	_, exists := store.entry[key2]
	store.mu.Unlock()
	if exists {
		t.Fatal("DedupNoRetention should delete the entry")
	}
	// 过期条目被 cleanupExpiredLocked 清理（触发 cleanupIfNeeded 的清理窗口）。
	clockNow = clockNow.Add(dedupCleanupInterval + time.Minute)
	store.Claim("a:b:POST:op:fp3", "op", time.Nanosecond)
	clockNow = clockNow.Add(dedupCleanupInterval + time.Minute)
	claimed, _ := store.Claim("a:b:POST:op:fp4", "op", time.Hour)
	if !claimed {
		t.Fatal("expired entry should be reclaimable")
	}
}

func TestW9EDedupeFingerprintErrorAndBodyHelpers(t *testing.T) {
	err := &fingerprintError{message: "指纹无效"}
	if err.Error() != "指纹无效" {
		t.Fatalf("fingerprintError = %q", err.Error())
	}
	// ParsedBody/BodyField 在无 body 上下文时返回 nil。
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	if ParsedBody(r) != nil || BodyField(r, "name") != nil {
		t.Fatal("missing parsed body should be nil")
	}
	// hasRequestBody 的 Transfer-Encoding 分支。
	chunked := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
	chunked.Header.Set("Transfer-Encoding", "chunked")
	chunked.Header.Del("Content-Length")
	if !hasRequestBody(chunked) {
		t.Fatal("chunked request should have a body")
	}
	bad := httptest.NewRequest(http.MethodPost, "/x", nil)
	bad.Header.Set("Content-Length", "abc")
	if hasRequestBody(bad) {
		t.Fatal("invalid content-length should not count as a body")
	}
	emptyCT := httptest.NewRequest(http.MethodPost, "/x", nil)
	emptyCT.Header.Set("Content-Type", "application/problem+json")
	if jsonBodyMediaType(emptyCT.Header) {
		t.Fatal("problem+json should not parse as application/json")
	}
	weird := httptest.NewRequest(http.MethodPost, "/x", nil)
	weird.Header.Set("Content-Type", "not a media type")
	if jsonBodyMediaType(weird.Header) {
		t.Fatal("invalid media type should not parse")
	}
}

func TestW9EObservabilitySinkAndMetricHooks(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	SetRequestEventSink(NewSlogRequestEventSink(logger))
	defer SetRequestEventSink(nil)

	var starts, finishes atomic.Int64
	SetHTTPMetricHooks(&HTTPMetricHooks{
		Start: func(path string, method string, startedAtMs int64) any {
			starts.Add(1)
			return "handle"
		},
		Finish: func(request any, statusCode *int, outcome string, finishedAtMs int64, failureScope string) {
			finishes.Add(1)
		},
	})
	defer SetHTTPMetricHooks(nil)

	k := New(Options{CompressionDisabled: true})
	k.RegisterFunc("GET /v1/w9e-metric", func(w http.ResponseWriter, r *http.Request) {
		WriteRawJSON(w, http.StatusOK, []byte(`{"ok":true}`))
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/w9e-metric")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if starts.Load() != 1 || finishes.Load() != 1 {
		t.Fatalf("metric hooks starts=%d finishes=%d", starts.Load(), finishes.Load())
	}
	logs := buf.String()
	for _, want := range []string{"http_request_started", "http_request_completed", "gateway.request.timing_summary", `"outcome":"success"`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q, got %s", want, logs)
		}
	}
	// 非 gateway 路径不产生 timing_summary。
	buf.Reset()
	resp, err = http.Get(server.URL + "/__aisys__/api/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if strings.Contains(buf.String(), "gateway.request.timing_summary") {
		t.Fatalf("non-gateway path should not emit timing summary: %s", buf.String())
	}
}

func TestW9EObservabilityClosedPathAndLevels(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	SetRequestEventSink(NewSlogRequestEventSink(logger))
	defer SetRequestEventSink(nil)
	SetHTTPMetricHooks(&HTTPMetricHooks{
		Start: func(path string, method string, startedAtMs int64) any { return "h" },
		Finish: func(request any, statusCode *int, outcome string, finishedAtMs int64, failureScope string) {
			if outcome != "aborted" {
				t.Fatalf("aborted request metric outcome = %q", outcome)
			}
		},
	})
	defer SetHTTPMetricHooks(nil)

	k := New(Options{CompressionDisabled: true})
	// 阻塞直到客户端断开，再返回 → close（aborted）契约。
	k.RegisterFunc("POST /v1/w9e-abort", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/w9e-abort", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// 服务端 handler 返回与 finishRequestObservability 是异步于客户端的，
	// 轮询等待关闭事件落地。
	deadline := time.Now().Add(3 * time.Second)
	for {
		logs := buf.String()
		ok := true
		for _, want := range []string{"http_request_closed", "下游连接关闭", `"outcome":"aborted"`} {
			if !strings.Contains(logs, want) {
				ok = false
				break
			}
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed-path logs missing events, got %s", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestW9EObservabilityEventLevelsAndHealthDemotion(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := NewSlogRequestEventSink(logger)
	// 直接驱动 sink 的四个级别分支。
	sink.EmitRequestEvent("debug", map[string]any{"k": 1}, "d")
	sink.EmitRequestEvent("warn", nil, "w")
	sink.EmitRequestEvent("error", nil, "e")
	sink.EmitRequestEvent("info", nil, "i")
	logs := buf.String()
	for _, want := range []string{`"msg":"d"`, `"msg":"w"`, `"msg":"e"`, `"msg":"i"`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("sink level logs missing %q, got %s", want, logs)
		}
	}

	// 4xx → warn，健康检查 → debug 降级。
	SetRequestEventSink(sink)
	defer SetRequestEventSink(nil)
	k := New(Options{CompressionDisabled: true})
	k.RegisterFunc("GET /__aisys__/api/health", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusNotFound, "missing")
	})
	k.RegisterFunc("GET /__aisys__/api/w9e-forbidden", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusForbidden, "denied")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	buf.Reset()
	resp, err := http.Get(server.URL + "/__aisys__/api/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("4xx health request should log warn, got %s", buf.String())
	}

	buf.Reset()
	resp, err = http.Get(server.URL + "/__aisys__/api/w9e-forbidden")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("4xx request should log warn, got %s", buf.String())
	}

	// 5xx → error。
	buf.Reset()
	srv5xx := httptest.NewServer(RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})))
	defer srv5xx.Close()
	resp, err = http.Get(srv5xx.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("5xx request should log error, got %s", buf.String())
	}
}

func TestW9EResolveRequestSummaryOutcome(t *testing.T) {
	if got := resolveRequestSummaryOutcome(nil); got != "success" {
		t.Fatalf("nil status outcome = %q", got)
	}
	if got := resolveRequestSummaryOutcome(intPtr(200)); got != "success" {
		t.Fatalf("2xx outcome = %q", got)
	}
	if got := resolveRequestSummaryOutcome(intPtr(404)); got != "expected_failure" {
		t.Fatalf("4xx outcome = %q", got)
	}
	if got := resolveRequestSummaryOutcome(intPtr(500)); got != "unexpected_failure" {
		t.Fatalf("5xx outcome = %q", got)
	}
}

func TestW9EHTTPMetricFailureScope(t *testing.T) {
	if got := httpMetricFailureScope(intPtr(502), "completed"); got != "gateway" {
		t.Fatalf("5xx failure scope = %q", got)
	}
	if got := httpMetricFailureScope(intPtr(502), "aborted"); got != "none" {
		t.Fatalf("aborted failure scope = %q", got)
	}
	if got := httpMetricFailureScope(nil, "completed"); got != "none" {
		t.Fatalf("nil status failure scope = %q", got)
	}
	if got := httpMetricFailureScope(intPtr(404), "completed"); got != "none" {
		t.Fatalf("4xx failure scope = %q", got)
	}
}

func TestW9EStatusTrackingWriterAndClassify(t *testing.T) {
	rec := httptest.NewRecorder()
	tw := &statusTrackingWriter{ResponseWriter: rec}
	if tw.statusPointer() != nil {
		t.Fatal("fresh tracking writer has no status")
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatalf("tracking write err = %v", err)
	}
	if p := tw.statusPointer(); p == nil || *p != http.StatusOK {
		t.Fatalf("implicit status pointer = %v", tw.statusPointer())
	}
	if !ClassifyGatewayRoutePath("/") || !ClassifyGatewayRoutePath("/v1") || !ClassifyGatewayRoutePath("/v1/x") {
		t.Fatal("gateway route classification broken")
	}
	if ClassifyGatewayRoutePath("/v2/x") {
		t.Fatal("/v2 should not be a gateway route")
	}
	if !isHealthRequestPath("/__aisys__/health") || !isHealthRequestPath("/__aisys__/api/health") || isHealthRequestPath("/other") {
		t.Fatal("health path classification broken")
	}
}

func intPtr(v int) *int { return &v }

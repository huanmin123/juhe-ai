package kernel

// w11g 覆盖补充：client IP 归一化、去重守卫透传与解析失败臂、压缩协商
// 细节、稳定哈希与本地化无变化路径。

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW11GNormalizeClientIPVariants(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"   ":         "",
		"1.2.3.4":     "1.2.3.4",
		"1.2.3.4:80":  "1.2.3.4",
		"[::1]":       "::1",
		"::ffff:1.2.3.4": "1.2.3.4",
		"::1":         "::1",
	}
	for input, want := range cases {
		if got := normalizeClientIP(input); got != want {
			t.Fatalf("normalizeClientIP(%q)=%q want %q", input, got, want)
		}
	}
	if isAllZeroHex("") || isAllZeroHex("01") || !isAllZeroHex("000") {
		t.Fatal("isAllZeroHex variants")
	}
	if NewSlogRequestEventSink(nil) == nil {
		t.Fatal("nil logger sink must default")
	}
}

func TestW11GDedupeHelpers(t *testing.T) {
	// 稳定哈希：不可序列化值回退 null。
	if got := HashStableValue(make(chan int)); got == "" {
		t.Fatal("unserializable value must hash null")
	}
	if got := SortedTextValues("not-a-list"); got != nil {
		t.Fatalf("non-list sorted = %v", got)
	}
	if got := SortedTextValues([]any{"b", "a"}); len(got) != 2 || got[0] != "a" {
		t.Fatalf("sorted = %v", got)
	}
	if got := statusFromCode(500); got != DedupFailed || statusFromCode(301) != DedupSucceeded {
		t.Fatal("statusFromCode boundaries")
	}
}

func TestW11GMutationGuardPassThroughArms(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	// GET 透传。
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "w11g-op",
		Fingerprint:  func(*http.Request) (any, error) { return map[string]any{"k": 1}, nil },
	})
	recorder := httptest.NewRecorder()
	guard(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/x", nil))
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("get passthrough = %d", recorder.Code)
	}
	// 无指纹透传。
	guard = MutationGuardMiddleware(MutationGuardOptions{OperationKey: "w11g-op"})
	recorder = httptest.NewRecorder()
	guard(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/x", nil))
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("no fingerprint passthrough = %d", recorder.Code)
	}
	// 指纹失败：普通错误与自定义消息。
	bad := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "w11g-op",
		Fingerprint:  func(*http.Request) (any, error) { return nil, context.DeadlineExceeded },
	})
	recorder = httptest.NewRecorder()
	bad(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/x", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("fingerprint error = %d", recorder.Code)
	}
	bad = MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "w11g-op",
		Fingerprint: func(*http.Request) (any, error) {
			return nil, &fingerprintError{message: "w11g 指纹失败"}
		},
	})
	recorder = httptest.NewRecorder()
	bad(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/x", nil))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "w11g 指纹失败") {
		t.Fatalf("fingerprint message = %d %s", recorder.Code, recorder.Body.String())
	}
	// Scope 失败与非字符串 scope。
	scoped := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "w11g-op",
		Fingerprint:  func(*http.Request) (any, error) { return map[string]any{"k": 1}, nil },
		Scope:        func(*http.Request) (any, error) { return nil, context.Canceled },
	})
	recorder = httptest.NewRecorder()
	scoped(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/x", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("scope error = %d", recorder.Code)
	}
	scoped = MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "w11g-op",
		Fingerprint:  func(*http.Request) (any, error) { return map[string]any{"k": 1}, nil },
		Scope:        func(*http.Request) (any, error) { return 42, nil },
	})
	recorder = httptest.NewRecorder()
	scoped(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/x", nil))
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("non-string scope passthrough = %d", recorder.Code)
	}
}

func TestW11GMutationGuardDuplicateClaim(t *testing.T) {
	store := NewDeduplicationStore(time.Now)
	handler := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "w11g-dup",
		Store:        store,
		Fingerprint:  func(*http.Request) (any, error) { return map[string]any{"k": "fixed"}, nil },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	// 第一次成功。
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/x", nil))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("first claim = %d", recorder.Code)
	}
	// 立即重复：409（processing）。
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/x", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate claim = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestW11GMutationGuardParserArms(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "w11g-parse",
		Fingerprint:  func(*http.Request) (any, error) { return map[string]any{"k": 1}, nil },
	})
	// 坏 JSON → 400（显式 Content-Length 才算有 body）。
	request := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte("not-json")))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", "8")
	recorder := httptest.NewRecorder()
	guard(next).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d", recorder.Code)
	}
	// 超限 JSON → 413。
	request = httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(bytes.Repeat([]byte("a"), 2<<20)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", "2097152")
	request.Body = http.MaxBytesReader(nil, request.Body, 128)
	recorder = httptest.NewRecorder()
	guard(next).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized json = %d", recorder.Code)
	}
	// 非 JSON 媒体类型透传。
	request = httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte("raw")))
	recorder = httptest.NewRecorder()
	guard(next).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("non-json passthrough = %d", recorder.Code)
	}
}

func TestW11GCompressionHelpers(t *testing.T) {
	// 可压缩媒体类型判断。
	if !compressibleMediaType("application/json; charset=utf-8") {
		t.Fatal("json must be compressible")
	}
	if compressibleMediaType("image/png") {
		t.Fatal("png must not be compressible")
	}
	// vary 追加与 * 通配。
	if got := appendVaryField("Accept-Encoding", "accept-encoding"); got != "Accept-Encoding" {
		t.Fatalf("appendVary same = %q", got)
	}
	if got := appendVaryField("*", "accept-encoding"); got != "*" {
		t.Fatalf("appendVary star = %q", got)
	}
	if got := appendVaryField("Accept-Language", "accept-encoding"); !strings.Contains(strings.ToLower(got), "accept-encoding") {
		t.Fatalf("appendVary merge = %q", got)
	}
	// jsParseFloat 前缀语法。
	if jsParseFloat("0.5x") != 0.5 || !isNaN(jsParseFloat("x")) {
		t.Fatal("jsParseFloat prefix")
	}
	// 协商：空输入回退 identity，零权重剔除，nan 权重回退偏好序。
	if got := negotiateAcceptEncoding(""); got != "identity" {
		t.Fatalf("empty accept = %q", got)
	}
	if got := negotiateAcceptEncoding("gzip;q=0"); got != "identity" {
		t.Fatalf("zero q = %q", got)
	}
	if got := negotiateAcceptEncoding("gzip;q=nan"); got != "identity" {
		t.Fatalf("nan q = %q", got)
	}
}

func isNaN(v float64) bool { return v != v }

func TestW11GLocalizeNoChangePath(t *testing.T) {
	// 非本地化消息：原样返回 changed=false。
	payload := []byte(`{"message":"请求参数无效"}`)
	out, changed := localizeSystemErrorPayload(payload, http.StatusBadRequest, false)
	if changed || !bytes.Equal(out, payload) {
		t.Fatalf("no-change localize out=%s changed=%v", out, changed)
	}
	// 嵌套 error.message 本地化。
	payload = []byte(`{"error":{"message":"Internal server error"}}`)
	out, changed = localizeSystemErrorPayload(payload, http.StatusInternalServerError, false)
	if !changed || !bytes.Contains(out, []byte("请求处理失败")) {
		t.Fatalf("nested localize out=%s changed=%v", out, changed)
	}
}

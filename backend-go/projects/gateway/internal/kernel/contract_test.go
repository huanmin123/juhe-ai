package kernel

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

// The golden values in this file were produced against the locked Node
// dependency chain (compression@1.8.1 + negotiator@0.6.4 + compressible@2.0.18
// + vary@1.1.2 + body-parser@1.20.5 + type-is@1.6.18) and the archived Node
// sources under migration-backup/node/final-archive/backend/src/shared.

// TestNegotiateAcceptEncodingGolden locks negotiator@0.6.4's encoding choice
// over the compression supported/preferred sets (recorded by running the
// Node module directly).
func TestNegotiateAcceptEncodingGolden(t *testing.T) {
	golden := map[string]string{
		"gzip":                  "gzip",
		"gzip;q=0":              "identity",
		"br":                    "br",
		"deflate":               "deflate",
		"":                      "identity",
		"identity":              "identity",
		"gzip;q=0.5, br;q=0.5":  "br",
		"*":                     "br",
		"identity;q=0, gzip":    "gzip",
		"gzip;q=abc":            "identity",
		"br;q=1.0, *;q=0":       "br",
		"GZIP":                  "gzip",
		"gzip ; q=0.8":          "gzip",
		"deflate, gzip;q=0.9":   "deflate",
		"compress, gzip":        "gzip",
		"gzip;q=0, deflate;q=0": "identity",
	}
	for header, want := range golden {
		if got := negotiateAcceptEncoding(header); got != want {
			t.Errorf("negotiateAcceptEncoding(%q) = %q, want %q", header, got, want)
		}
	}
}

// TestCompressibleMediaTypeGolden locks compressible@2.0.18 against
// mime-db@1.52.0 flags plus the regexp fallback.
func TestCompressibleMediaTypeGolden(t *testing.T) {
	golden := map[string]bool{
		"text/plain":               true,
		"text/html; charset=utf-8": true,
		"application/json":         true,
		"application/problem+json": true,
		"application/vnd.api+json": true,
		"application/xhtml+xml":    true,
		"image/svg+xml":            true,
		"application/wasm":         true,
		"text/unknown-custom":      true,
		"application/unknown+json": true,
		"image/png":                false,
		"application/zip":          false,
		"application/octet-stream": false,
		"application/gzip":         false,
		"video/mp4":                false,
		"application/yaml":         false,
		"":                         false,
		"application":              false,
	}
	for contentType, want := range golden {
		if got := compressibleMediaType(contentType); got != want {
			t.Errorf("compressibleMediaType(%q) = %v, want %v", contentType, got, want)
		}
	}
}

func newCompressionTestKernel(t *testing.T) (*Kernel, *httptest.Server) {
	t.Helper()
	k := newTestKernel(t, nil)
	large := strings.Repeat("x", 2048)
	register := func(pattern string, handler http.HandlerFunc) {
		k.RegisterFunc(pattern, handler)
	}
	register("GET /text-large", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(large))
	})
	register("GET /text-small", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("tiny"))
	})
	register("GET /png-large", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(large))
	})
	register("GET /octet-large", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(large))
	})
	register("GET /no-transform", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "no-store, no-transform")
		_, _ = w.Write([]byte(large))
	})
	register("GET /chunked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("a"))
		_, _ = w.Write([]byte("b"))
	})
	register("GET /sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: first\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		_, _ = w.Write([]byte("data: second\n\n"))
	})
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return k, server
}

func compressionProbe(t *testing.T, server *httptest.Server, path, acceptEncoding, method string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acceptEncoding != "" {
		request.Header.Set("Accept-Encoding", acceptEncoding)
	}
	// Explicit AE keeps the transport from auto-decompressing.
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

// TestCompressionNegotiationContract mirrors the Node probes:
// /large ae=gzip -> ce=gzip vary cl-gone; gzip;q=0 -> identity+vary;
// br -> br; deflate -> deflate; small json/text -> identity+vary;
// png/octet/no-transform -> identity without vary; HEAD -> identity+vary.
func TestCompressionNegotiationContract(t *testing.T) {
	_, server := newCompressionTestKernel(t)
	large := strings.Repeat("x", 2048)

	response, body := compressionProbe(t, server, "/text-large", "gzip", http.MethodGet)
	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip negotiation: ce=%q vary=%q", response.Header.Get("Content-Encoding"), response.Header.Get("Vary"))
	}
	if response.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("gzip negotiation must add Vary, got %q", response.Header.Get("Vary"))
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, _ := io.ReadAll(reader)
	if string(decompressed) != large {
		t.Fatal("gzip payload mismatch")
	}

	// gzip;q=0 must stay identity but keep Vary (Node: vary precedes the
	// threshold/negotiation branches).
	response, body = compressionProbe(t, server, "/text-large", "gzip;q=0", http.MethodGet)
	if response.Header.Get("Content-Encoding") != "" {
		t.Fatalf("gzip;q=0 must not compress, ce=%q", response.Header.Get("Content-Encoding"))
	}
	if response.Header.Get("Vary") != "Accept-Encoding" || string(body) != large {
		t.Fatalf("gzip;q=0 identity response: vary=%q len=%d", response.Header.Get("Vary"), len(body))
	}

	// br and deflate.
	response, body = compressionProbe(t, server, "/text-large", "br", http.MethodGet)
	if response.Header.Get("Content-Encoding") != "br" {
		t.Fatalf("br negotiation: ce=%q", response.Header.Get("Content-Encoding"))
	}
	brReader := brotli.NewReader(bytes.NewReader(body))
	decompressed, _ = io.ReadAll(brReader)
	if string(decompressed) != large {
		t.Fatal("brotli payload mismatch")
	}

	response, body = compressionProbe(t, server, "/text-large", "deflate", http.MethodGet)
	if response.Header.Get("Content-Encoding") != "deflate" {
		t.Fatalf("deflate negotiation: ce=%q", response.Header.Get("Content-Encoding"))
	}
	zr, err := zlib.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, _ = io.ReadAll(zr)
	if string(decompressed) != large {
		t.Fatal("deflate payload mismatch")
	}

	// Small response: identity with Vary (threshold branch runs after vary).
	response, _ = compressionProbe(t, server, "/text-small", "gzip", http.MethodGet)
	if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("small response must be identity+Vary, ce=%q vary=%q", response.Header.Get("Content-Encoding"), response.Header.Get("Vary"))
	}

	// Binary types: filtered before vary — no Content-Encoding, no Vary.
	for _, path := range []string{"/png-large", "/octet-large"} {
		response, body = compressionProbe(t, server, path, "gzip", http.MethodGet)
		if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Vary") != "" {
			t.Fatalf("%s must be unfiltered identity without Vary, ce=%q vary=%q", path, response.Header.Get("Content-Encoding"), response.Header.Get("Vary"))
		}
		if string(body) != large {
			t.Fatalf("%s body must stay raw", path)
		}
	}

	// no-transform: no Content-Encoding, no Vary.
	response, body = compressionProbe(t, server, "/no-transform", "gzip", http.MethodGet)
	if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Vary") != "" || string(body) != large {
		t.Fatalf("no-transform must skip compression and Vary, ce=%q vary=%q", response.Header.Get("Content-Encoding"), response.Header.Get("Vary"))
	}

	// HEAD: nocompress after vary.
	response, _ = compressionProbe(t, server, "/text-large", "gzip", http.MethodHead)
	if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("HEAD must be identity+Vary, ce=%q vary=%q", response.Header.Get("Content-Encoding"), response.Header.Get("Vary"))
	}

	// Chunked (no Content-Length): Node compresses even when the first
	// chunk is far below the threshold.
	response, body = compressionProbe(t, server, "/chunked", "gzip", http.MethodGet)
	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("chunked response must compress, ce=%q vary=%q", response.Header.Get("Content-Encoding"), response.Header.Get("Vary"))
	}
	reader, _ = gzip.NewReader(bytes.NewReader(body))
	decompressed, _ = io.ReadAll(reader)
	if string(decompressed) != "ab" {
		t.Fatalf("chunked payload mismatch: %q", decompressed)
	}

	// No Accept-Encoding at all: identity negotiation still adds Vary.
	rawClient := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/text-large", nil)
	response, err = rawClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ = io.ReadAll(response.Body)
	if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Vary") != "Accept-Encoding" || string(body) != large {
		t.Fatalf("absent AE must be identity+Vary, ce=%q vary=%q", response.Header.Get("Content-Encoding"), response.Header.Get("Vary"))
	}
}

// TestFlushBeforeWriteDoesNotLockIdentity mirrors the Node probe
// res.flush(); res.end(large) -> ce=gzip.
func TestFlushBeforeWriteDoesNotLockIdentity(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("GET /flush-first", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	response, body := compressionProbe(t, server, "/flush-first", "gzip", http.MethodGet)
	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("flush before write must not lock identity, ce=%q", response.Header.Get("Content-Encoding"))
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, _ := io.ReadAll(reader)
	if len(decompressed) != 2048 {
		t.Fatalf("flush-first payload mismatch: %d bytes", len(decompressed))
	}
}

// TestSSEFlushTimelinessWithoutCompression locks the Flusher propagation
// fix: the first event must reach the client while the handler is still
// waiting, with or without Accept-Encoding.
func TestSSEFlushTimelinessWithoutCompression(t *testing.T) {
	_, server := newCompressionTestKernel(t)
	for _, acceptEncoding := range []string{"", "gzip"} {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/sse", nil)
		if acceptEncoding != "" {
			request.Header.Set("Accept-Encoding", acceptEncoding)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		firstLine := make(chan string, 1)
		go func() {
			buffer := make([]byte, 64)
			for {
				n, err := response.Body.Read(buffer)
				if n > 0 {
					firstLine <- string(buffer[:n])
					return
				}
				if err != nil {
					firstLine <- ""
					return
				}
			}
		}()
		select {
		case line := <-firstLine:
			if !strings.HasPrefix(line, "data: first") {
				t.Fatalf("ae=%q unexpected first chunk %q", acceptEncoding, line)
			}
		case <-time.After(120 * time.Millisecond):
			t.Fatalf("ae=%q first SSE chunk did not arrive before handler delay (flusher lost)", acceptEncoding)
		}
		response.Body.Close()
	}
}

// TestManagementSecurityHeadersByteExact locks http-security.ts
// managementHeaders byte for byte (script-src 'self' only).
func TestManagementSecurityHeadersByteExact(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("GET /__aisys__/api/ping", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, nil, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/__aisys__/api/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	want := "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data: https:; connect-src 'self' https: wss:; worker-src 'self' blob:; media-src 'self' data: blob: https:; manifest-src 'self'"
	if got := response.Header.Get("Content-Security-Policy"); got != want {
		t.Fatalf("CSP mismatch:\n got  %q\n want %q", got, want)
	}
	if strings.Contains(want, "script-src 'self' 'unsafe-inline'") {
		t.Fatal("script-src must not allow unsafe-inline")
	}
}

// TestTraceContract locks request-context.ts: x-trace-id response header,
// fallback chain (traceparent -> x-trace-id -> x-correlation-id) and strict
// traceparent validation.
func TestTraceContract(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("GET /ctx", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, map[string]string{"trace": Context(r).TraceID}, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	get := func(headers map[string]string) (*http.Response, map[string]string) {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/ctx", nil)
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(response.Body).Decode(&payload)
		body := map[string]string{}
		if text, ok := payload["data"].(map[string]any)["trace"].(string); ok {
			body["trace"] = text
		}
		return response, body
	}

	validTrace := "4bf92f3577b34da6a3ce929d0e0e4736"
	// Legal traceparent: echoed in body and response header.
	response, body := get(map[string]string{"Traceparent": "00-" + validTrace + "-00f067aa0ba902b7-01"})
	if response.Header.Get("X-Trace-Id") != validTrace || body["trace"] != validTrace {
		t.Fatalf("traceparent must propagate to header and context: %q %q", response.Header.Get("X-Trace-Id"), body["trace"])
	}
	// Uppercase traceparent normalized to lowercase.
	response, body = get(map[string]string{"Traceparent": "00-" + strings.ToUpper(validTrace) + "-00F067AA0BA902B7-01"})
	if body["trace"] != validTrace {
		t.Fatalf("uppercase traceparent must normalize: %q", body["trace"])
	}

	// Invalid traceparent falls back to x-trace-id verbatim.
	response, body = get(map[string]string{
		"Traceparent": "ff-" + validTrace + "-00f067aa0ba902b7-01",
		"X-Trace-Id":  "client-chain-42",
	})
	if response.Header.Get("X-Trace-Id") != "client-chain-42" || body["trace"] != "client-chain-42" {
		t.Fatalf("x-trace-id fallback mismatch: %q %q", response.Header.Get("X-Trace-Id"), body["trace"])
	}
	// Invalid traceparent + invalid x-trace-id falls back to x-correlation-id.
	response, body = get(map[string]string{
		"Traceparent":      "bad-" + validTrace + "-x",
		"X-Trace-Id":       "bad id with spaces!!",
		"X-Correlation-Id": "corr-7, ignored-second",
	})
	if body["trace"] != "corr-7" {
		t.Fatalf("x-correlation-id fallback (first comma value) mismatch: %q", body["trace"])
	}
	// Nothing valid: generated UUID shapes both header and context.
	response, body = get(nil)
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidPattern.MatchString(body["trace"]) || response.Header.Get("X-Trace-Id") != body["trace"] {
		t.Fatalf("generated trace mismatch: header=%q body=%q", response.Header.Get("X-Trace-Id"), body["trace"])
	}

	// Strict parseTraceParent rejections.
	for _, invalid := range []string{
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", // version ff
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01", // all-zero trace id
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", // all-zero parent id
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",    // missing flags
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
		"0-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01",
	} {
		if got := ParseTraceParent(invalid); got != "" {
			t.Fatalf("ParseTraceParent(%q) = %q, want rejected", invalid, got)
		}
	}
	// normalizeHeaderId boundaries: length cap and charset.
	if got := normalizeHeaderID(strings.Repeat("a", 129)); got != "" {
		t.Fatalf("129-char id must be rejected, got %q", got)
	}
	if got := normalizeHeaderID(strings.Repeat("a", 124) + ":._-"); got == "" {
		t.Fatal("128-char id with allowed charset must pass")
	}
	if got := normalizeHeaderID("bad/id"); got != "" {
		t.Fatalf("slash must be rejected, got %q", got)
	}
}

// TestJSONParserBeforeMutationGuardClaim locks system-api-app.ts ordering:
// parser errors answer before the guard can claim, so repeated malformed or
// oversized requests keep the parser status instead of drifting to 409.
func TestJSONParserBeforeMutationGuardClaim(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_000_000, 0)}
	store := NewDeduplicationStore(clock.Now)
	k := newTestKernel(t, nil)
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "parser.order",
		Store:        store,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"name": TextField(BodyField(r, "name"))}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !DecodeJSON(w, r, &body) {
			return
		}
		if body == nil || TextField(body["name"]) == "" {
			WriteError(w, http.StatusBadRequest, "请求参数无效")
			return
		}
		WriteOK(w, body, "")
	}))
	k.Register("POST /__aisys__/api/guarded-parser", handler)
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	post := func(contentType, body string) *http.Response {
		response, err := http.Post(server.URL+"/__aisys__/api/guarded-parser", contentType, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	// Malformed JSON: 400 on every attempt within the failed TTL, never 409.
	for attempt := 1; attempt <= 2; attempt++ {
		response := post("application/json", "{")
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "请求体无效") {
			t.Fatalf("malformed attempt %d: status=%d body=%s", attempt, response.StatusCode, raw)
		}
	}
	// Oversized JSON: 413 on every attempt, never 409.
	oversized := `{"name":"` + strings.Repeat("x", 300*1024) + `"}`
	for attempt := 1; attempt <= 2; attempt++ {
		response := post("application/json", oversized)
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(raw), "请求体过大") {
			t.Fatalf("oversized attempt %d: status=%d body=%s", attempt, response.StatusCode, raw)
		}
	}

	// Non-JSON media type: the Node parser skips it, so the guard claims an
	// empty fingerprint and the handler rejects the missing fields; a quick
	// retry hits the failed dedup entry exactly like Node.
	first := post("text/plain", `{"name":"a"}`)
	rawFirst, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusBadRequest || !strings.Contains(string(rawFirst), "请求参数无效") {
		t.Fatalf("text/plain must skip parsing: status=%d body=%s", first.StatusCode, rawFirst)
	}
	second := post("text/plain", `{"name":"a"}`)
	rawSecond, _ := io.ReadAll(second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusConflict || !strings.Contains(string(rawSecond), "请求刚刚失败") {
		t.Fatalf("text/plain retry must 409 on the failed claim: status=%d body=%s", second.StatusCode, rawSecond)
	}

	// Valid JSON body still parses, claims and succeeds.
	valid := post("application/json", `{"name":"ok"}`)
	rawValid, _ := io.ReadAll(valid.Body)
	valid.Body.Close()
	if valid.StatusCode != http.StatusOK || !strings.Contains(string(rawValid), `"name":"ok"`) {
		t.Fatalf("valid json body must parse: status=%d body=%s", valid.StatusCode, rawValid)
	}
	// The valid JSON claim is succeeded; an immediate duplicate 409s.
	duplicate := post("application/json", `{"name":"ok"}`)
	rawDuplicate, _ := io.ReadAll(duplicate.Body)
	duplicate.Body.Close()
	if duplicate.StatusCode != http.StatusConflict || !strings.Contains(string(rawDuplicate), "该操作刚刚已处理") {
		t.Fatalf("valid duplicate must 409: status=%d body=%s", duplicate.StatusCode, rawDuplicate)
	}
}

// TestDecodeJSONContentTypeGate locks the express.json default type check:
// only exact application/json is parsed; other media types leave the target
// untouched so handler validation answers.
func TestDecodeJSONContentTypeGate(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("POST /__aisys__/api/decode-ct", func(w http.ResponseWriter, r *http.Request) {
		var target map[string]any
		if !DecodeJSON(w, r, &target) {
			return
		}
		if target == nil {
			WriteError(w, http.StatusBadRequest, "请求参数无效")
			return
		}
		WriteOK(w, target, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	post := func(contentType, body string) (*http.Response, string) {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/__aisys__/api/decode-ct", strings.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return response, string(raw)
	}

	response, raw := post("application/json", `{"n":1}`)
	if response.StatusCode != http.StatusOK || !strings.Contains(raw, `"n":1`) {
		t.Fatalf("application/json must parse: %d %s", response.StatusCode, raw)
	}
	response, raw = post("application/json; charset=utf-8", `{"n":2}`)
	if response.StatusCode != http.StatusOK || !strings.Contains(raw, `"n":2`) {
		t.Fatalf("application/json with parameters must parse: %d %s", response.StatusCode, raw)
	}
	for _, contentType := range []string{"text/plain", "application/problem+json", ""} {
		response, raw = post(contentType, `{"n":3}`)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("content-type %q must skip parsing (handler 400), got %d %s", contentType, response.StatusCode, raw)
		}
	}
}

// TestPrefixSegmentBoundary locks the Express mount semantics: adjacent
// prefixes must not inherit the system API cross-cutting middleware.
func TestPrefixSegmentBoundary(t *testing.T) {
	k := newTestKernel(t, nil)
	k.RegisterFunc("GET /__aisys__/api/inside", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, nil, "")
	})
	k.RegisterFunc("GET /__aisys__/apix", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Adjacent", "1")
		WriteOK(w, nil, "")
	})
	k.RegisterFunc("GET /other", func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, nil, "")
	})
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	get := func(path string) *http.Response {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response
	}
	for _, path := range []string{"/__aisys__/api/inside", "/__aisys__/api", "/__aisys__/api/"} {
		if response := get(path); response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s must carry no-store, got %q", path, response.Header.Get("Cache-Control"))
		}
	}
	if response := get("/__aisys__/apix"); response.Header.Get("Cache-Control") == "no-store" {
		t.Fatal("/__aisys__/apix must not inherit the system API no-store")
	}
	// Node mounts the management headers at '/__aisys__' (server.ts:208), so
	// /__aisys__/apix legitimately keeps them — only the system API prefix
	// (/__aisys__/api) must respect the segment boundary.
	if response := get("/__aisys__/apix"); response.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("/__aisys__/apix must keep the management security headers")
	}
	if response := get("/other"); response.Header.Get("X-Frame-Options") != "" {
		t.Fatal("/other must not inherit management security headers")
	}
	// Encoded separator: Express matches the raw pathname, so %2F stays
	// inside one segment and the system API mount must not match.
	if response := get("/__aisys__/api%2Fx"); response.Header.Get("Cache-Control") == "no-store" {
		t.Fatal("encoded separator must stay within one segment")
	}
}

// TestMutationGuardBodyReachableForFingerprints keeps the guard contract
// that fingerprints observe the parsed body map (Node req.body) while the
// downstream handler can still decode the restored raw body.
func TestMutationGuardBodyReachableForFingerprints(t *testing.T) {
	store := NewDeduplicationStore(nil)
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "body.reach",
		Store:        store,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"name":  TextField(BodyField(r, "name")),
				"items": SortedTextValues(BodyField(r, "items")),
			}, nil
		},
	})
	inner := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !DecodeJSON(w, r, &body) {
			return
		}
		WriteOK(w, body, "")
	}))
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/body-reach", strings.NewReader(`{"name":"dup","items":["b","a"]}`))
	request.Header.Set("Content-Type", "application/json")
	// type-is hasBody reads the Content-Length header; a direct ServeHTTP
	// call (no net/http server in front) must set it like the wire would.
	request.Header.Set("Content-Length", "29")
	recorder := httptest.NewRecorder()
	inner.ServeHTTP(recorder, request)

	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response not json: %v %s", err, recorder.Body.String())
	}
	// The handler echoes the raw body order; only the fingerprint sorts.
	want := map[string]any{"name": "dup", "items": []any{"b", "a"}}
	if !reflect.DeepEqual(payload.Data, want) {
		t.Fatalf("downstream decode mismatch: got %#v want %#v", payload.Data, want)
	}
	// The fingerprint saw the same sorted values (a second claim with an
	// equal body must conflict instead of reaching the handler).
	request2 := httptest.NewRequest(http.MethodPost, "/__aisys__/api/body-reach", strings.NewReader(`{"name":"dup","items":["a","b"]}`))
	request2.Header.Set("Content-Type", "application/json")
	request2.Header.Set("Content-Length", "29")
	recorder2 := httptest.NewRecorder()
	inner.ServeHTTP(recorder2, request2)
	if recorder2.Code != http.StatusConflict {
		t.Fatalf("equal fingerprint must conflict, got %d %s", recorder2.Code, recorder2.Body.String())
	}
}

// TestGuardedRouteStaysOnKernelChain locks the guard serving fix: guarded
// responses must keep the compression layer (Node mutationGuard runs inside
// the router, below the compression middleware) and the failed-claim
// classification must observe statuses written through the deferred
// compression writer.
func TestGuardedRouteStaysOnKernelChain(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_000_000, 0)}
	store := NewDeduplicationStore(clock.Now)
	k := newTestKernel(t, nil)
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "chain.compress",
		Store:        store,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"name": TextField(BodyField(r, "name"))}, nil
		},
	})
	fail := false
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !DecodeJSON(w, r, &body) {
			return
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Large enough to cross the compression threshold.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"blob":"` + strings.Repeat("x", 2048) + `"}}`))
	}))
	k.Register("POST /__aisys__/api/guarded-chain", handler)
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	postAE := func() (*http.Response, []byte) {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/__aisys__/api/guarded-chain", strings.NewReader(`{"name":"big"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		// Explicit AE keeps the transport from stripping Content-Encoding.
		request.Header.Set("Accept-Encoding", "gzip")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return response, raw
	}
	response, raw := postAE()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("guarded response must be compressed: status=%d ce=%q", response.StatusCode, response.Header.Get("Content-Encoding"))
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, _ := io.ReadAll(reader)
	if !strings.Contains(string(decompressed), `"blob"`) {
		t.Fatalf("guarded compressed payload mismatch: %d bytes", len(decompressed))
	}

	// Failed handler through the deferred writer must complete the claim as
	// failed: the immediate retry conflicts with 请求刚刚失败.
	fail = true
	clock.advance(200 * time.Second)
	failed, _ := postAE()
	if failed.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failed attempt must surface 500, got %d", failed.StatusCode)
	}
	retry, rawRetry := postAE()
	if retry.StatusCode != http.StatusConflict || !strings.Contains(string(rawRetry), "请求刚刚失败") {
		t.Fatalf("retry after failed guarded call must 409: status=%d body=%s", retry.StatusCode, rawRetry)
	}
}

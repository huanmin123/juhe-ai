package gatewaybody

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	wantTooLargeBody = `{"error":{"message":"请求体过大","type":"request_too_large"}}`
	wantInFlightBody = `{"error":{"message":"网关请求体在途总量过高，请稍后重试","type":"server_overloaded","code":"gateway_body_in_flight_limit_exceeded"}}`
	wantBusyBody     = `{"error":{"message":"网关请求解析繁忙，请稍后重试","type":"server_overloaded","code":"gateway_json_parser_busy"}}`
	wantFailedBody   = `{"error":{"message":"网关请求解析暂时不可用，请稍后重试","type":"server_overloaded","code":"gateway_json_parser_failed"}}`
	wantAbortBody    = `{"error":{"message":"请求体上传未完成，请重试","type":"request_timeout"}}`
	wantInvalidBody  = `{"error":{"message":"网关请求体无效","type":"invalid_request_error"}}`
)

type recorderMock struct {
	mu     sync.Mutex
	calls  []RejectionInput
	pathOf []string
}

func newRecorderMock() *recorderMock {
	return &recorderMock{}
}

func (r *recorderMock) RecordGatewayBodyRejection(_ *http.Request, req *Request, input RejectionInput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, input)
	if req != nil && req.State != nil {
		r.pathOf = append(r.pathOf, string(req.State.JSONParseStatus))
	}
}

func (r *recorderMock) snapshot() []RejectionInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RejectionInput(nil), r.calls...)
}

func TestClassifyRawBodyParserError(t *testing.T) {
	tests := []struct {
		name       string
		err        *RawBodyParserError
		wantStatus int
		wantCopy   string
		wantType   string
		wantAttr   string
	}{
		{
			name:       "request aborted",
			err:        &RawBodyParserError{Type: "request.aborted", StatusCode: 400},
			wantStatus: 408, wantCopy: "请求体上传未完成，请重试", wantType: "request_timeout", wantAttr: "downstream_closed",
		},
		{
			name:       "size invalid",
			err:        &RawBodyParserError{Type: "request.size.invalid"},
			wantStatus: 408, wantCopy: "请求体上传未完成，请重试", wantType: "request_timeout", wantAttr: "downstream_closed",
		},
		{
			name:       "entity too large",
			err:        &RawBodyParserError{Type: "entity.too.large", StatusCode: 413},
			wantStatus: 413, wantCopy: "请求体过大", wantType: "request_too_large", wantAttr: "gateway_policy",
		},
		{
			name:       "unknown falls back to invalid",
			err:        &RawBodyParserError{StatusCode: 400},
			wantStatus: 400, wantCopy: "网关请求体无效", wantType: "invalid_request_error", wantAttr: "gateway_policy",
		},
		{
			name:       "missing status defaults to 400",
			err:        &RawBodyParserError{},
			wantStatus: 400, wantCopy: "网关请求体无效", wantType: "invalid_request_error", wantAttr: "gateway_policy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyRawBodyParserError(tt.err)
			if got.StatusCode != tt.wantStatus || got.Message != tt.wantCopy || got.ErrorType != tt.wantType || got.FailureAttribution != tt.wantAttr {
				t.Fatalf("classify = %+v", got)
			}
		})
	}
}

func TestReadRawBody(t *testing.T) {
	t.Run("reads under the hard limit", func(t *testing.T) {
		m := NewMiddleware(Config{})
		payload := `{"model":"gpt-4o"}`
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		raw, perr := m.ReadRawBody(httptest.NewRecorder(), r)
		if perr != nil || string(raw) != payload {
			t.Fatalf("raw = %q, perr = %+v", raw, perr)
		}
	})

	t.Run("oversize read reports entity too large", func(t *testing.T) {
		m := NewMiddleware(Config{})
		m.rawBodyLimit = 16
		payload := strings.Repeat("a", 64)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		r.Header.Set("Content-Length", "64")
		recorder := httptest.NewRecorder()
		raw, perr := m.ReadRawBody(recorder, r)
		if perr == nil || perr.Type != "entity.too.large" || perr.StatusCode != 413 {
			t.Fatalf("perr = %+v", perr)
		}
		if perr.Limit != 16 {
			t.Fatalf("limit = %d", perr.Limit)
		}
		if len(raw) == 0 {
			t.Fatalf("partial read must surface received bytes")
		}
	})

	t.Run("generic read error classifies as invalid", func(t *testing.T) {
		m := NewMiddleware(Config{})
		r := httptest.NewRequest(http.MethodPost, "/", errReader{})
		_, perr := m.ReadRawBody(httptest.NewRecorder(), r)
		if perr == nil || perr.Type != "" || perr.StatusCode != 400 {
			t.Fatalf("perr = %+v", perr)
		}
		response := ClassifyRawBodyParserError(perr)
		if response.StatusCode != 400 || response.Message != "网关请求体无效" {
			t.Fatalf("response = %+v", response)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestHandleParserRejectionCopies(t *testing.T) {
	tests := []struct {
		name      string
		perr      *RawBodyParserError
		wantCode  int
		wantBody  string
		wantError string
	}{
		{name: "aborted 408", perr: &RawBodyParserError{Type: "request.aborted"}, wantCode: 408, wantBody: wantAbortBody, wantError: "request.aborted"},
		{name: "too large 413", perr: &RawBodyParserError{Type: "entity.too.large", StatusCode: 413, Limit: 67108864}, wantCode: 413, wantBody: wantTooLargeBody, wantError: "entity.too.large"},
		{name: "invalid 400", perr: &RawBodyParserError{}, wantCode: 400, wantBody: wantInvalidBody, wantError: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := newRecorderMock()
			m := NewMiddleware(Config{Recorder: recorder})
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if !m.HandleParserRejection(w, r, tt.perr) {
				t.Fatalf("expected handled")
			}
			response := w.Result()
			if response.StatusCode != tt.wantCode {
				t.Fatalf("status = %d, want %d", response.StatusCode, tt.wantCode)
			}
			body, _ := io.ReadAll(response.Body)
			if string(body) != tt.wantBody {
				t.Fatalf("body = %s, want %s", body, tt.wantBody)
			}
			calls := recorder.snapshot()
			if len(calls) != 1 {
				t.Fatalf("calls = %d", len(calls))
			}
			if calls[0].Reason != RejectReasonGatewayBodyParser || calls[0].ErrorCode != tt.wantError {
				t.Fatalf("rejection = %+v", calls[0])
			}
		})
	}

	t.Run("no parser error continues", func(t *testing.T) {
		m := NewMiddleware(Config{})
		if m.HandleParserRejection(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil), nil) {
			t.Fatalf("nil parser error must not be handled")
		}
	})
}

func TestRejectByContentLength(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		contentLength string
		textLimitMB   int
		hasTextLimit  bool
		wantHandled   bool
		wantStatus    int
		wantBody      string
	}{
		{
			name:          "text path over default limit",
			path:          "/v1/chat/completions",
			contentLength: "16777217",
			wantHandled:   true, wantStatus: 413, wantBody: wantTooLargeBody,
		},
		{
			name:          "text path within configured limit",
			path:          "/v1/chat/completions",
			contentLength: "1048576",
			textLimitMB:   1, hasTextLimit: true,
			wantHandled: false,
		},
		{
			name:          "text path over configured limit",
			path:          "/v1/messages",
			contentLength: "2097152",
			textLimitMB:   1, hasTextLimit: true,
			wantHandled: true, wantStatus: 413, wantBody: wantTooLargeBody,
		},
		{
			name:          "image path allows text-size bodies",
			path:          "/v1/images/generations",
			contentLength: "33554432",
			wantHandled:   false,
		},
		{
			name:          "image path over 64mb",
			path:          "/v1/images/edits",
			contentLength: "67108865",
			wantHandled:   true, wantStatus: 413, wantBody: wantTooLargeBody,
		},
		{
			name:          "unrelated path skipped",
			path:          "/v1/audio/transcriptions",
			contentLength: "999999999",
			wantHandled:   false,
		},
		{
			name: "missing header skipped",
			path: "/v1/chat/completions",
		},
		{
			name:          "garbage header skipped",
			path:          "/v1/chat/completions",
			contentLength: "abc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := newRecorderMock()
			cfg := Config{Recorder: recorder}
			if tt.hasTextLimit {
				limit := tt.textLimitMB
				cfg.TextRawBodyLimitMegabytes = func() (int, bool) { return limit, true }
			}
			m := NewMiddleware(cfg)
			r := httptest.NewRequest(http.MethodPost, tt.path, nil)
			if tt.contentLength != "" {
				r.Header.Set("Content-Length", tt.contentLength)
			}
			w := httptest.NewRecorder()
			handled := m.RejectByContentLength(w, r)
			if handled != tt.wantHandled {
				t.Fatalf("handled = %v", handled)
			}
			if !tt.wantHandled {
				return
			}
			response := w.Result()
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d", response.StatusCode)
			}
			body, _ := io.ReadAll(response.Body)
			if string(body) != tt.wantBody {
				t.Fatalf("body = %s, want %s", body, tt.wantBody)
			}
			calls := recorder.snapshot()
			if len(calls) != 1 || calls[0].Reason != RejectReasonGatewayBodySizeLimit || calls[0].ErrorCode != "request_too_large" {
				t.Fatalf("rejection = %+v", calls)
			}
		})
	}
}

func TestCapturePipeline(t *testing.T) {
	newTestMiddleware := func(recorder *recorderMock) *Middleware {
		return NewMiddleware(Config{Recorder: recorder})
	}

	t.Run("empty body", func(t *testing.T) {
		recorder := newRecorderMock()
		m := newTestMiddleware(recorder)
		w := httptest.NewRecorder()
		r := newJSONRequest(t, "/", "")
		req, err := m.Capture(w, r, nil)
		if err != nil || req == nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		if req.State.JSONParseStatus != JSONParseStatusEmpty || !req.State.IsJSON {
			t.Fatalf("state = %+v", req.State)
		}
		if req.Body != nil {
			t.Fatalf("body = %#v", req.Body)
		}
		if len(recorder.snapshot()) != 0 || w.Body.Len() != 0 {
			t.Fatalf("unexpected rejection")
		}
	})

	t.Run("empty raw body slice behaves like nil", func(t *testing.T) {
		m := newTestMiddleware(newRecorderMock())
		req, err := m.Capture(httptest.NewRecorder(), newJSONRequest(t, "/", ""), []byte{})
		if err != nil || req == nil || req.State.JSONParseStatus != JSONParseStatusEmpty {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
	})

	t.Run("small json scanned inline", func(t *testing.T) {
		m := newTestMiddleware(newRecorderMock())
		payload := `{"model":"gpt-4o","stream":true,"tools":[{"type":"function"}]}`
		req, err := m.Capture(httptest.NewRecorder(), newJSONRequest(t, "/v1/chat/completions", payload), []byte(payload))
		if err != nil || req == nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		if req.State.JSONParseStatus != JSONParseStatusScannedJSON {
			t.Fatalf("status = %v", req.State.JSONParseStatus)
		}
		if req.State.Model == nil || *req.State.Model != "gpt-4o" || req.State.Stream == nil || !*req.State.Stream {
			t.Fatalf("state = %+v", req.State)
		}
		if !req.State.StrictOutputRequirement {
			t.Fatalf("strict output = %+v", req.State)
		}
		if req.Body != nil {
			t.Fatalf("body must stay deferred")
		}
	})

	t.Run("invalid json still continues", func(t *testing.T) {
		m := newTestMiddleware(newRecorderMock())
		payload := `{"model":"gpt-4o",`
		req, err := m.Capture(httptest.NewRecorder(), newJSONRequest(t, "/", payload), []byte(payload))
		if err != nil || req == nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		if req.State.JSONParseStatus != JSONParseStatusInvalidJSON {
			t.Fatalf("status = %v", req.State.JSONParseStatus)
		}
		if req.State.Model == nil || *req.State.Model != "gpt-4o" {
			t.Fatalf("partial metadata lost: %+v", req.State)
		}
	})

	t.Run("large json scans on the pool", func(t *testing.T) {
		m := newTestMiddleware(newRecorderMock())
		payload := largeJSONPayloadWithTools(t, "gpt-image-1")
		req, err := m.Capture(httptest.NewRecorder(), newJSONRequest(t, "/v1/responses", payload), []byte(payload))
		if err != nil || req == nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		if req.State.JSONParseStatus != JSONParseStatusScannedJSON {
			t.Fatalf("status = %v", req.State.JSONParseStatus)
		}
		// A plain image_generation tool entry sets imageGeneration but never
		// the forced flag (Node: forced requires tool_choice/type forcing).
		if !req.State.ImageGeneration || req.State.ImageGenerationForced {
			t.Fatalf("image generation flags = %+v", req.State)
		}
		if req.State.Model == nil || *req.State.Model != "gpt-image-1" {
			t.Fatalf("model = %v", req.State.Model)
		}
	})

	t.Run("oversize body rejected with deferred state", func(t *testing.T) {
		recorder := newRecorderMock()
		m := newTestMiddleware(recorder)
		m.rawBodyLimit = 1024
		payload := strings.Repeat("x", 2048)
		w := httptest.NewRecorder()
		req, err := m.Capture(w, newJSONRequest(t, "/", payload), []byte(payload))
		if req != nil || err != nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		response := w.Result()
		if response.StatusCode != 413 {
			t.Fatalf("status = %d", response.StatusCode)
		}
		body, _ := io.ReadAll(response.Body)
		if string(body) != wantTooLargeBody {
			t.Fatalf("body = %s", body)
		}
		calls := recorder.snapshot()
		if len(calls) != 1 || calls[0].LimitScope != RawBodyLimitScopeGateway || calls[0].LimitBytes != 1024 {
			t.Fatalf("rejection = %+v", calls)
		}
	})

	t.Run("in-flight budget exhausted", func(t *testing.T) {
		recorder := newRecorderMock()
		m := newTestMiddleware(recorder)
		m.limiter.SetMaxBytesForTest(1024)
		payload := strings.Repeat("x", 2048)
		w := httptest.NewRecorder()
		req, err := m.Capture(w, newJSONRequest(t, "/", payload), []byte(payload))
		if req != nil || err != nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		response := w.Result()
		if response.StatusCode != 503 {
			t.Fatalf("status = %d", response.StatusCode)
		}
		if response.Header.Get("Retry-After") != "1" {
			t.Fatalf("Retry-After = %q", response.Header.Get("Retry-After"))
		}
		body, _ := io.ReadAll(response.Body)
		if string(body) != wantInFlightBody {
			t.Fatalf("body = %s", body)
		}
		calls := recorder.snapshot()
		if len(calls) != 1 || calls[0].Reason != RejectReasonGatewayBodyInFlightLimit {
			t.Fatalf("rejection = %+v", calls)
		}
	})

	t.Run("text lane limit rejects large json", func(t *testing.T) {
		recorder := newRecorderMock()
		cfg := Config{Recorder: recorder}
		cfg.TextRawBodyLimitMegabytes = func() (int, bool) { return 1, true }
		m := NewMiddleware(cfg)
		payload := largeJSONPayloadWithSize(t, "gpt-4o", 4096)
		w := httptest.NewRecorder()
		req, err := m.Capture(w, newJSONRequest(t, "/v1/chat/completions", payload), []byte(payload))
		if req != nil || err != nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		response := w.Result()
		if response.StatusCode != 413 {
			t.Fatalf("status = %d", response.StatusCode)
		}
		calls := recorder.snapshot()
		if len(calls) != 1 || calls[0].LimitScope != RawBodyLimitScopeText || calls[0].LimitBytes != 1024*1024 {
			t.Fatalf("rejection = %+v", calls)
		}
	})

	t.Run("image lane allows text-size bodies", func(t *testing.T) {
		cfg := Config{}
		cfg.TextRawBodyLimitMegabytes = func() (int, bool) { return 1, true }
		m := NewMiddleware(cfg)
		payload := largeJSONPayloadWithSize(t, "gpt-4o", 4096)
		req, err := m.Capture(httptest.NewRecorder(), newJSONRequest(t, "/v1/images/generations", payload), []byte(payload))
		if err != nil || req == nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
	})

	t.Run("multipart image model captured", func(t *testing.T) {
		m := newTestMiddleware(newRecorderMock())
		raw, contentType := multipartBody(t, []string{formField("model", "gpt-image-1"), filePart("image", "a.png")})
		r := httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)
		r.Header.Set("Content-Type", contentType)
		req, err := m.Capture(httptest.NewRecorder(), r, raw)
		if err != nil || req == nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		if req.State.JSONParseStatus != JSONParseStatusNotJSON {
			t.Fatalf("status = %v", req.State.JSONParseStatus)
		}
		if req.State.Model == nil || *req.State.Model != "gpt-image-1" {
			t.Fatalf("model = %v", req.State.Model)
		}
	})

	t.Run("multipart audio response format captured", func(t *testing.T) {
		m := newTestMiddleware(newRecorderMock())
		raw, contentType := multipartBody(t, []string{formField("response_format", "Verbose_Json"), filePart("file", "a.mp3")})
		r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
		r.Header.Set("Content-Type", contentType)
		req, err := m.Capture(httptest.NewRecorder(), r, raw)
		if err != nil || req == nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		if req.State.ResponseFormat == nil || *req.State.ResponseFormat != "verbose_json" {
			t.Fatalf("responseFormat = %v", req.State.ResponseFormat)
		}
	})

	t.Run("aborted request releases the lease silently", func(t *testing.T) {
		recorder := newRecorderMock()
		m := newTestMiddleware(recorder)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := newJSONRequest(t, "/v1/chat/completions", `{"model":"gpt-4o"}`).WithContext(ctx)
		w := httptest.NewRecorder()
		req, err := m.Capture(w, r, []byte(`{"model":"gpt-4o"}`))
		if req != nil || err != nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		if w.Body.Len() != 0 {
			t.Fatalf("aborted requests must not answer, got %s", w.Body.String())
		}
		if len(recorder.snapshot()) != 0 {
			t.Fatalf("aborted requests must not record rejections")
		}
		state := m.limiter.State(0)
		if state.CurrentBytes != 0 || state.RequestCount != 0 {
			t.Fatalf("lease not released: %+v", state)
		}
	})

	t.Run("canceled deferred scan releases the lease silently", func(t *testing.T) {
		recorder := newRecorderMock()
		m := newTestMiddleware(recorder)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		payload := largeJSONPayload(t, "gpt-4o")
		r := newJSONRequest(t, "/v1/chat/completions", payload).WithContext(ctx)
		w := httptest.NewRecorder()
		req, err := m.Capture(w, r, []byte(payload))
		if req != nil || err != nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		if w.Body.Len() != 0 {
			t.Fatalf("aborted requests must not answer")
		}
		state := m.limiter.State(0)
		if state.CurrentBytes != 0 {
			t.Fatalf("lease not released: %+v", state)
		}
	})
}

func TestCaptureDeferredScanRejections(t *testing.T) {
	largePayload := func(t *testing.T) []byte {
		t.Helper()
		return []byte(largeJSONPayload(t, "gpt-4o"))
	}

	t.Run("busy pool answers 503 busy", func(t *testing.T) {
		recorder := newRecorderMock()
		parser := NewJSONParser(JSONParserOptions{PoolSize: 1, MaxQueuedJobs: 2, Logger: DiscardLogger{}})
		defer parser.Stop()
		m := NewMiddleware(Config{Recorder: recorder, Parser: parser})
		m.metadataScanTimeout = 10 * time.Second

		release := make(chan struct{})
		var once sync.Once
		closeRelease := func() { once.Do(func() { close(release) }) }
		defer closeRelease()
		parser.scanFunc = func(raw []byte) JSONBodyMetadata {
			<-release
			return JSONBodyMetadata{}
		}

		payload := largePayload(t)
		// Sequentially occupy the worker and both queue slots so the pool is
		// deterministically saturated before the capture runs.
		// Sequentially occupy the worker and both queue slots. Each step waits
		// for a stable pool state, so the third submission is guaranteed to
		// observe a saturated queue and the capture below deterministically
		// hits the queue-full admission.
		submitBlocked := func() <-chan error {
			errCh := make(chan error, 1)
			go func() {
				_, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), payload, 30*time.Second)
				errCh <- err
				closeRelease()
			}()
			return errCh
		}
		blocked := make([]<-chan error, 0, 3)
		blocked = append(blocked, submitBlocked())
		waitFor(t, 10*time.Second, func() bool {
			parser.mu.Lock()
			defer parser.mu.Unlock()
			return parser.busyWorkers == 1
		})
		blocked = append(blocked, submitBlocked())
		waitFor(t, 10*time.Second, func() bool {
			parser.mu.Lock()
			defer parser.mu.Unlock()
			return len(parser.queue) == 1
		})
		blocked = append(blocked, submitBlocked())
		waitFor(t, 10*time.Second, func() bool {
			parser.mu.Lock()
			defer parser.mu.Unlock()
			return len(parser.queue) == 2
		})
		_ = blocked

		w := httptest.NewRecorder()
		req, err := m.Capture(w, newJSONRequest(t, "/v1/chat/completions", string(payload)), payload)
		if req != nil || err != nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		response := w.Result()
		if response.StatusCode != 503 {
			t.Fatalf("status = %d", response.StatusCode)
		}
		body, _ := io.ReadAll(response.Body)
		if string(body) != wantBusyBody {
			t.Fatalf("body = %s", body)
		}
		calls := recorder.snapshot()
		if len(calls) != 1 || calls[0].ErrorCode != "gateway_json_parser_busy" {
			t.Fatalf("rejection = %+v", calls)
		}
	})

	t.Run("failing scan answers 503 failed", func(t *testing.T) {
		recorder := newRecorderMock()
		parser := NewJSONParser(JSONParserOptions{PoolSize: 1, Logger: DiscardLogger{}})
		defer parser.Stop()
		m := NewMiddleware(Config{Recorder: recorder, Parser: parser})
		m.metadataScanTimeout = 20 * time.Millisecond

		release := make(chan struct{})
		parser.scanFunc = func(raw []byte) JSONBodyMetadata {
			<-release
			return JSONBodyMetadata{}
		}
		defer close(release)

		payload := largePayload(t)
		w := httptest.NewRecorder()
		req, err := m.Capture(w, newJSONRequest(t, "/v1/chat/completions", string(payload)), payload)
		if req != nil || err != nil {
			t.Fatalf("req = %+v, err = %v", req, err)
		}
		response := w.Result()
		if response.StatusCode != 503 {
			t.Fatalf("status = %d", response.StatusCode)
		}
		body, _ := io.ReadAll(response.Body)
		if string(body) != wantFailedBody {
			t.Fatalf("body = %s", body)
		}
		calls := recorder.snapshot()
		if len(calls) != 1 || calls[0].ErrorCode != "gateway_json_parser_failed" {
			t.Fatalf("rejection = %+v", calls)
		}
	})
}

func newJSONRequest(t *testing.T, path, payload string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, nil)
	if payload != "" {
		r.Body = io.NopCloser(strings.NewReader(payload))
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

func largeJSONPayload(t *testing.T, model string) string {
	t.Helper()
	return largeJSONPayloadWithSize(t, model, 2048)
}

func largeJSONPayloadWithTools(t *testing.T, model string) string {
	t.Helper()
	return strings.Replace(largeJSONPayloadWithSize(t, model, 2048), `"messages":`,
		`"tools":[{"type":"image_generation"}],"messages":`, 1)
}

func largeJSONPayloadWithSize(t *testing.T, model string, messages int) string {
	t.Helper()
	var builder strings.Builder
	builder.WriteString(`{"model":"` + model + `","messages":[`)
	for i := 0; i < messages; i++ {
		if i > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"role":"user","content":"` + strings.Repeat("x", 256) + `"}`)
	}
	builder.WriteString(`]}`)
	payload := builder.String()
	if len(payload) <= GatewayJSONBodyInlineMetadataScanMaxBytes {
		t.Fatalf("payload too small: %d", len(payload))
	}
	return payload
}

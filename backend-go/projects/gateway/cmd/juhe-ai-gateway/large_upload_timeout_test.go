package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLargeUploadReadTimeoutSelection(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		path    string
		timeout time.Duration
		ok      bool
	}{
		{name: "files upload retired", method: http.MethodPost, path: "/v1/files"},
		{name: "files upload trailing slash retired", method: http.MethodPost, path: "/v1/files/"},
		{name: "files upload bare form retired", method: http.MethodPost, path: "/files"},
		{name: "image edit", method: http.MethodPost, path: "/v1/images/edits", timeout: mediaUploadReadTimeout, ok: true},
		{name: "image edit bare form", method: http.MethodPost, path: "/images/edits", timeout: mediaUploadReadTimeout, ok: true},
		{name: "audio transcription", method: http.MethodPost, path: "/v1/audio/transcriptions", timeout: mediaUploadReadTimeout, ok: true},
		{name: "audio transcription bare form", method: http.MethodPost, path: "/audio/transcriptions", timeout: mediaUploadReadTimeout, ok: true},
		{name: "audio translation", method: http.MethodPost, path: "/v1/audio/translations", timeout: mediaUploadReadTimeout, ok: true},
		{name: "audio translation bare form", method: http.MethodPost, path: "/audio/translations", timeout: mediaUploadReadTimeout, ok: true},
		{name: "files list", method: http.MethodGet, path: "/v1/files"},
		{name: "files list bare form", method: http.MethodGet, path: "/files"},
		{name: "chat", method: http.MethodPost, path: "/v1/chat/completions"},
		{name: "containers", method: http.MethodPost, path: "/v1/containers"},
		{name: "v1 prefix without slash", method: http.MethodPost, path: "/v1files"},
		{name: "v1beta prefix", method: http.MethodPost, path: "/v1beta/files"},
		{name: "nested v1", method: http.MethodPost, path: "/v1/v1/files"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, nil)
			timeout, ok := largeUploadReadTimeout(request)
			if ok != testCase.ok || timeout != testCase.timeout {
				t.Fatalf("largeUploadReadTimeout(%s %s) = (%v, %v), want (%v, %v)",
					testCase.method, testCase.path, timeout, ok, testCase.timeout, testCase.ok)
			}
		})
	}
	if _, ok := largeUploadReadTimeout(nil); ok {
		t.Fatal("nil request must not select a large-upload timeout")
	}
}

type deadlineRecordingWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
	calls    int
}

func (w *deadlineRecordingWriter) SetReadDeadline(deadline time.Time) error {
	w.deadline = deadline
	w.calls++
	return nil
}

func TestWithLargeUploadReadTimeoutExtendsOnlyUploadRoutes(t *testing.T) {
	now := time.Now()
	for _, testCase := range []struct {
		name      string
		method    string
		path      string
		wantCalls int
		wantAfter time.Duration
	}{
		{name: "image edit", method: http.MethodPost, path: "/v1/images/edits", wantCalls: 1, wantAfter: mediaUploadReadTimeout},
		{name: "chat stays default", method: http.MethodPost, path: "/v1/chat/completions"},
		{name: "files retired stays default", method: http.MethodGet, path: "/v1/files"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			writer := &deadlineRecordingWriter{ResponseRecorder: httptest.NewRecorder()}
			called := false
			handler := withLargeUploadReadTimeout(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			handler.ServeHTTP(writer, httptest.NewRequest(testCase.method, testCase.path, nil))
			if !called {
				t.Fatal("next handler was not called")
			}
			if writer.calls != testCase.wantCalls {
				t.Fatalf("SetReadDeadline calls = %d, want %d", writer.calls, testCase.wantCalls)
			}
			if testCase.wantCalls == 0 {
				if !writer.deadline.IsZero() {
					t.Fatalf("unexpected deadline %v", writer.deadline)
				}
				return
			}
			minimum := now.Add(testCase.wantAfter - time.Minute)
			maximum := now.Add(testCase.wantAfter + time.Minute)
			if writer.deadline.Before(minimum) || writer.deadline.After(maximum) {
				t.Fatalf("deadline = %v, want within [%v, %v]", writer.deadline, minimum, maximum)
			}
		})
	}
}

// TestLargeUploadConcurrencyLimit pins the pre-auth slow-body bound: when all
// extended-deadline slots are taken, another large upload is rejected before
// the body is read, while normal routes stay unaffected.
func TestLargeUploadConcurrencyLimit(t *testing.T) {
	previous := largeUploadSlots
	largeUploadSlots = newUploadConcurrencyLimiter(1)
	defer func() { largeUploadSlots = previous }()
	if !largeUploadSlots.tryAcquire() {
		t.Fatal("could not fill the test slot")
	}
	defer largeUploadSlots.release()

	largeCalled := false
	handler := withLargeUploadReadTimeout(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		largeCalled = true
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("full limiter status = %d, want 503", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After = %q, want 5", recorder.Header().Get("Retry-After"))
	}
	if !strings.Contains(recorder.Body.String(), "大文件上传并发已满") {
		t.Fatalf("unexpected 503 body: %s", recorder.Body.String())
	}
	if largeCalled {
		t.Fatal("large upload handler ran without a slot")
	}

	normalCalled := false
	normal := withLargeUploadReadTimeout(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		normalCalled = true
	}))
	normal.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if !normalCalled {
		t.Fatal("normal route was blocked by the large-upload limiter")
	}
}

// TestLargeUploadReadDeadlineAllowsSlowBody proves the listener-level
// extension actually overrides the server ReadTimeout: a body that arrives
// well after the default deadline must still be fully read. The handler
// returns 500 on a truncated read so removing SetReadDeadline makes this
// test fail instead of silently returning 204.
func TestLargeUploadReadDeadlineAllowsSlowBody(t *testing.T) {
	const body = "slow body"
	handler := withLargeUploadReadTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		copied, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, "read failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if copied != int64(len(body)) {
			http.Error(w, "short body", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewUnstartedServer(handler)
	server.Config.ReadTimeout = 100 * time.Millisecond
	server.Start()
	defer server.Close()

	reader, writer := io.Pipe()
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = writer.Write([]byte(body))
		_ = writer.Close()
	}()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/images/edits", reader)
	if err != nil {
		t.Fatalf("build slow upload request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("slow upload failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("slow upload status = %d, want 204", response.StatusCode)
	}
}

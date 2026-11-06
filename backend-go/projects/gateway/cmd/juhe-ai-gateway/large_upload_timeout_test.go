package main

import (
	"net/http"
	"net/http/httptest"
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
		{name: "files upload", method: http.MethodPost, path: "/v1/files", timeout: defaultLargeUploadReadTimeout, ok: true},
		{name: "files upload trailing slash", method: http.MethodPost, path: "/v1/files/", timeout: defaultLargeUploadReadTimeout, ok: true},
		{name: "files upload bare form", method: http.MethodPost, path: "/files", timeout: defaultLargeUploadReadTimeout, ok: true},
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
		{name: "files upload", method: http.MethodPost, path: "/v1/files", wantCalls: 1, wantAfter: defaultLargeUploadReadTimeout},
		{name: "chat stays default", method: http.MethodPost, path: "/v1/chat/completions"},
		{name: "files list stays default", method: http.MethodGet, path: "/v1/files"},
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

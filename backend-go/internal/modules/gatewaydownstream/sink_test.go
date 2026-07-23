package gatewaydownstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewPlanFiltersTransportHeadersAndPreservesBusinessHeaders(t *testing.T) {
	header := http.Header{
		"Connection":       {"keep-alive, X-Secret-Hop"},
		"X-Secret-Hop":     {"drop"},
		"Content-Encoding": {"gzip"},
		"Content-Length":   {"999"},
		"Set-Cookie":       {"secret=1"},
		"X-Litellm-Trace":  {"drop"},
		"Retry-After":      {"7"},
		"Www-Authenticate": {`Bearer realm="upstream"`},
		"Location":         {"/resource/1"},
		"Content-Type":     {"application/json"},
	}
	length := int64(12)
	plan, err := NewPlan(http.StatusTooManyRequests, header, ModeOpaque, &length)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Connection", "X-Secret-Hop", "Set-Cookie", "X-Litellm-Trace"} {
		if plan.Header.Get(name) != "" {
			t.Fatalf("%s was not removed", name)
		}
	}
	if plan.Header.Get("Content-Encoding") != "gzip" || plan.Header.Get("Content-Length") != "12" || plan.Header.Get("Retry-After") != "7" || plan.Header.Get("Www-Authenticate") == "" || plan.Header.Get("Location") != "/resource/1" {
		t.Fatalf("plan headers = %#v", plan.Header)
	}
}

func TestNewPlanRejectsEncodedStructuredBodyAndUpstreamGatewayHeaders(t *testing.T) {
	if _, err := NewPlan(http.StatusOK, http.Header{"Content-Encoding": {"gzip"}}, ModeJSON, nil); err == nil {
		t.Fatal("expected encoded JSON response to be rejected")
	}
	plan, err := NewPlan(http.StatusOK, http.Header{"X-Request-Id": {"upstream"}, "Access-Control-Allow-Origin": {"*"}, "Content-Encoding": {"identity"}}, ModeSSE, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Header.Get("X-Request-Id") != "" || plan.Header.Get("Access-Control-Allow-Origin") != "" || plan.Header.Get("Content-Encoding") != "" {
		t.Fatalf("untrusted response headers survived: %#v", plan.Header)
	}
}

func TestNewPlanAppliesSSEDefaults(t *testing.T) {
	plan, err := NewPlan(http.StatusOK, http.Header{"Cache-Control": {"private"}}, ModeSSE, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || plan.Header.Get("Cache-Control") != "private" || plan.Header.Get("X-Accel-Buffering") != "no" || plan.Header.Get("Content-Length") != "" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestHTTPWriterSinkStagesCommitsAndSnapshotsMonotonically(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("X-Trace-Id", "gateway-trace")
	recorder.Header().Set("Set-Cookie", "middleware-secret=1")
	recorder.Header().Set("Content-Length", "999")
	sink, err := NewHTTPWriterSink(recorder)
	if err != nil {
		t.Fatal(err)
	}
	length := int64(2)
	plan, _ := NewPlan(http.StatusCreated, http.Header{"X-Trace-Id": {"upstream-trace"}, "Content-Type": {"application/json"}}, ModeJSON, &length)
	if err := sink.Stage(plan); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || sink.Snapshot().TransportCommitted {
		t.Fatalf("stage committed response: code=%d state=%+v", recorder.Code, sink.Snapshot())
	}
	if err := sink.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusCreated || recorder.Header().Get("X-Trace-Id") != "gateway-trace" || recorder.Header().Get("Set-Cookie") != "" || recorder.Header().Get("Content-Length") != "2" || !sink.Snapshot().TransportCommitted {
		t.Fatalf("commit = code=%d headers=%#v state=%+v", recorder.Code, recorder.Header(), sink.Snapshot())
	}
	if n, err := sink.Write(context.Background(), []byte("{}")); err != nil || n != 2 {
		t.Fatalf("write = %d/%v", n, err)
	}
	sink.MarkSemantic()
	state := sink.Snapshot()
	if !state.TransportCommitted || !state.SemanticCommitted || state.DownstreamBytes != 2 || strings.TrimSpace(recorder.Body.String()) != "{}" {
		t.Fatalf("state/body = %+v/%q", state, recorder.Body.String())
	}
	if err := sink.Stage(plan); err == nil {
		t.Fatal("stage after commit error = nil")
	}
}

func TestHTTPWriterSinkHeaderOnlyCommitHasZeroBytes(t *testing.T) {
	recorder := httptest.NewRecorder()
	sink, _ := NewHTTPWriterSink(recorder)
	plan, _ := NewPlan(http.StatusNoContent, http.Header{"Content-Length": {"99"}}, ModeJSON, nil)
	if err := sink.Stage(plan); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink.MarkSemantic()
	state := sink.Snapshot()
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("Content-Length") != "" || !state.TransportCommitted || !state.SemanticCommitted || state.DownstreamBytes != 0 {
		t.Fatalf("recorder/state = %#v/%+v", recorder, state)
	}
}

func TestHTTPWriterSinkRejectsHandBuiltInvalidPlan(t *testing.T) {
	sink, _ := NewHTTPWriterSink(httptest.NewRecorder())
	if err := sink.Stage(Plan{StatusCode: http.StatusOK, Header: make(http.Header), Mode: Mode("invalid")}); err == nil {
		t.Fatal("expected invalid hand-built plan to be rejected")
	}
}

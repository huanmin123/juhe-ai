package kernel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestContextRecordFailureReason(t *testing.T) {
	ctx := &RequestContext{}
	if ctx.FailureReason() != "" {
		t.Fatal("fresh context must not carry a failure reason")
	}
	ctx.RecordFailureReason("pq: column does not exist")
	ctx.RecordFailureReason("second cause must not override the first")
	if got := ctx.FailureReason(); got != "pq: column does not exist" {
		t.Fatalf("FailureReason = %q, want first cause", got)
	}
}

func TestRequestContextRecordFailureReasonTruncatesAndFlattens(t *testing.T) {
	ctx := &RequestContext{}
	long := strings.Repeat("x", 500) + "\npq: next line"
	ctx.RecordFailureReason(long)
	got := ctx.FailureReason()
	if len(got) != 400 {
		t.Fatalf("reason length = %d, want 400 (truncated)", len(got))
	}
	if strings.Contains(got, "\n") {
		t.Fatal("reason must be flattened to a single log line")
	}
	ctxNil := (*RequestContext)(nil)
	ctxNil.RecordFailureReason("nil receiver must not panic")
}

func TestWriteErrorCauseRecordsReasonAndWritesEnvelope(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts", nil)
	rec := httptest.NewRecorder()
	WriteErrorCause(r, rec, http.StatusInternalServerError, "服务器内部错误", errors.New("pq: relation missing"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "服务器内部错误") {
		t.Fatalf("client envelope must stay generic, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pq: relation missing") {
		t.Fatal("cause must not leak to the client payload")
	}

	// 无 kernel 中间件时 Context(r) 返回兜底实例，WriteErrorCause 不得 panic。
	rec2 := httptest.NewRecorder()
	WriteErrorCause(httptest.NewRequest(http.MethodGet, "/x", nil), rec2, http.StatusInternalServerError, "服务器内部错误", nil)
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("nil-cause status = %d, want 500", rec2.Code)
	}

	// 4xx 不记录 cause（客户端可见消息即原因）。
	rec3 := httptest.NewRecorder()
	WriteErrorCause(r, rec3, http.StatusBadRequest, "参数错误", errors.New("bad param detail"))
	if Context(r).FailureReason() != "" {
		t.Fatal("4xx must not record a failure reason")
	}
}

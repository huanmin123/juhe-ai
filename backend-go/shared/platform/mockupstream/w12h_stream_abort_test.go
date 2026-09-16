package mockupstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// w12h 流式中断臂：直接以受控 Request/ResponseWriter 驱动 serve，
// 覆盖 writeChunk 的取消/写失败臂、responses SSE 的 malformed/tool_call
// 中断返回、以及 chunkDelay 期间客户端断开的停发路径。

var w12hErrWriteFailed = errors.New("w12h 注入写失败")

// w12hFailWriter 从第 failAt 次 Write（1 基）开始注入写失败；failAt<=0 表示立即失败。
type w12hFailWriter struct {
	header http.Header
	failAt int
	writes int
}

func (w *w12hFailWriter) Header() http.Header { return w.header }
func (w *w12hFailWriter) WriteHeader(int)     {}
func (w *w12hFailWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.failAt <= 0 || w.writes >= w.failAt {
		return 0, w12hErrWriteFailed
	}
	return len(p), nil
}

func w12hStreamRequest(t *testing.T, path, body string, cancel bool) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if cancel {
		ctx, cancelFn := context.WithCancel(req.Context())
		cancelFn()
		req = req.WithContext(ctx)
	}
	return req
}

// chat 流首个分块前请求已取消：writeChunk 取消臂 + serveChatSSE 停发返回。
func TestW12HChatStreamClientGoneBeforeFirstChunk(t *testing.T) {
	sv := New()
	defer sv.Close()
	req := w12hStreamRequest(t, "/v1/chat/completions", `{"model":"gpt-mock","stream":true,"messages":[]}`, true)
	rec := httptest.NewRecorder()
	sv.serve(rec, req)
	if got := sv.Aborts(); got != 1 {
		t.Fatalf("客户端已取消应记录 1 次中断: %d", got)
	}
}

// responses 默认流：请求已取消时 delta 事件写前即中止（437 停发返回）。
func TestW12HResponsesStreamClientGoneBeforeDelta(t *testing.T) {
	sv := New()
	defer sv.Close()
	req := w12hStreamRequest(t, "/v1/responses", `{"model":"gpt-mock","stream":true,"input":"hi"}`, true)
	rec := httptest.NewRecorder()
	sv.serve(rec, req)
	if got := sv.Aborts(); got != 1 {
		t.Fatalf("客户端已取消应记录 1 次中断: %d", got)
	}
}

// responses malformed_sse 流：畸形行分支正常写完并返回。
func TestW12HResponsesStreamMalformedSSEBranch(t *testing.T) {
	sv := New()
	defer sv.Close()
	req := w12hStreamRequest(t, "/v1/responses", `{"model":"gpt-mock","stream":true,"input":"hi"}`, false)
	req.Header.Set("X-Mock-Scenario", string(ScenarioMalformedSSE))
	rec := httptest.NewRecorder()
	sv.serve(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data: {broken json line") {
		t.Fatalf("malformed_sse responses 流应输出畸形行: %d %s", rec.Code, rec.Body.String())
	}
	if got := sv.Aborts(); got != 0 {
		t.Fatalf("写成功不应记录中断: %d", got)
	}
}

// responses tool_call 流：首事件写失败 → added 事件后停发（写失败臂 + added 中断返回）。
func TestW12HResponsesToolCallAddedEventWriteFailure(t *testing.T) {
	sv := New()
	defer sv.Close()
	req := w12hStreamRequest(t, "/v1/responses", `{"model":"gpt-mock","stream":true,"input":"hi"}`, false)
	req.Header.Set("X-Mock-Scenario", string(ScenarioToolCall))
	fw := &w12hFailWriter{header: http.Header{}, failAt: 0}
	sv.serve(fw, req)
	if got := sv.Aborts(); got != 1 {
		t.Fatalf("写失败应记录 1 次中断: %d", got)
	}
}

// responses tool_call 流：added 成功、done 写失败 → done 中断返回。
func TestW12HResponsesToolCallDoneEventWriteFailure(t *testing.T) {
	sv := New()
	defer sv.Close()
	req := w12hStreamRequest(t, "/v1/responses", `{"model":"gpt-mock","stream":true,"input":"hi"}`, false)
	req.Header.Set("X-Mock-Scenario", string(ScenarioToolCall))
	fw := &w12hFailWriter{header: http.Header{}, failAt: 2}
	sv.serve(fw, req)
	if fw.writes < 2 {
		t.Fatalf("应至少尝试两次写入: %d", fw.writes)
	}
	if got := sv.Aborts(); got != 1 {
		t.Fatalf("第二次写失败应记录 1 次中断: %d", got)
	}
}

// responses 默认流：chunkDelay 期间客户端断开 → sleep 返回 false，completed 停发。
func TestW12HResponsesStreamClientGoneDuringChunkDelay(t *testing.T) {
	sv := New()
	defer sv.Close()
	sv.SetStreamChunkDelay(150 * time.Millisecond)
	req := w12hStreamRequest(t, "/v1/responses", `{"model":"gpt-mock","stream":true,"input":"hi"}`, false)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	time.AfterFunc(30*time.Millisecond, cancel)
	rec := httptest.NewRecorder()
	sv.serve(rec, req)
	if got := sv.Aborts(); got != 1 {
		t.Fatalf("延迟期间断开应记录 1 次中断: %d", got)
	}
	if strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("completed 事件不应再发送: %s", rec.Body.String())
	}
}

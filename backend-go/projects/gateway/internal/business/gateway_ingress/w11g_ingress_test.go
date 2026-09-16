package gatewayingress

// w11g 覆盖补充：网关入口 dispatch 错误渲染分支与 body 中继边界。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var errW11GUpstream = errors.New("w11g upstream failure")

func TestW11GIngressErrorRenderArms(t *testing.T) {
	// writeDispatchError：取消（错误与请求上下文两种来源）与一般错误渲染。
	recorder := httptest.NewRecorder()
	writeDispatchError(recorder, context.Background(), context.Canceled)
	if recorder.Code != 499 || recorder.Body.String() == "" {
		t.Fatalf("cancelled dispatch = %d %s", recorder.Code, recorder.Body.String())
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder = httptest.NewRecorder()
	writeDispatchError(recorder, cancelledCtx, errW11GUpstream)
	if recorder.Code != 499 {
		t.Fatalf("cancelled ctx dispatch = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	writeDispatchError(recorder, context.Background(), errW11GUpstream)
	if recorder.Code == 0 || recorder.Body.String() == "" {
		t.Fatalf("generic dispatch = %d %s", recorder.Code, recorder.Body.String())
	}
	// relayBody：空 body 与正常 JSON body 中继。
	response := httptest.NewRecorder()
	if err := relayBody(response, bytes.NewReader(nil)); err != nil {
		t.Fatalf("empty body err=%v", err)
	}
	response = httptest.NewRecorder()
	if err := relayBody(response, bytes.NewReader([]byte(`{"w11g":1}`))); err != nil {
		t.Fatalf("json body err=%v", err)
	}
	if response.Body.String() != `{"w11g":1}` {
		t.Fatalf("relayed body = %s", response.Body.String())
	}
}

func TestW11GIngressServeHTTPGuardArms(t *testing.T) {
	// nil request：400。
	recorder := httptest.NewRecorder()
	(Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		return Response{}, nil
	}}}).ServeHTTP(recorder, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("nil request = %d", recorder.Code)
	}
	// 响应 Body 缺失：502 且 Finish 以 Aborted 结算。
	finishOutcome := Outcome("")
	recorder = httptest.NewRecorder()
	(Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		return Response{StatusCode: 200, Finish: func(_ context.Context, outcome Outcome) error {
			finishOutcome = outcome
			return nil
		}}, nil
	}}}).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/w11g", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("nil body = %d", recorder.Code)
	}
	if finishOutcome != OutcomeAborted {
		t.Fatalf("finish outcome=%q", finishOutcome)
	}
	// 非法状态码回退 502。
	recorder = httptest.NewRecorder()
	(Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		return Response{StatusCode: 0, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}}}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/w11g", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("invalid status = %d", recorder.Code)
	}
}

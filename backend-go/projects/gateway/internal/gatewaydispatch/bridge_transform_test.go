package gatewaydispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// B-4 response transformer port wiring: PerformUpstreamRequestAttempt applies
// the engine ResponseTransformer after a successful upstream attempt (httptest
// fake upstream), keeps the raw response when the port is nil or returns nil,
// and surfaces transformer errors.

// fakeTransformBody is a minimal ReadCloser body.
type fakeTransformBody struct {
	io.Reader
}

func (fakeTransformBody) Close() error { return nil }

// recordingTransformer mirrors the composition-root transformer contract.
type recordingTransformer struct {
	calls     int
	lastInput UpstreamResponseTransformInput
	nextBody  string
	returnNil bool
	failWith  error
}

func (r *recordingTransformer) TransformUpstreamResponseForAccount(input UpstreamResponseTransformInput) (*GatewayUpstreamResponse, error) {
	r.calls++
	r.lastInput = input
	if r.failWith != nil {
		return nil, r.failWith
	}
	if r.returnNil {
		return nil, nil
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("X-Bridge-Transformed", "1")
	return NewGatewayUpstreamResponseForTransform(input.Response.Status(), header, io.NopCloser(strings.NewReader(r.nextBody))), nil
}

func newBridgeTransformRequest(t *testing.T, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request := gatewaypreauth.NewGatewayRequest(raw)
	request.Body = &gatewaybody.Request{
		RawBody: []byte(body),
		State:   &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusParsed},
	}
	return request
}

func newTransformTestEngine(transformer UpstreamResponseTransformer) *Engine {
	engine := &Engine{Config: DefaultEngineConfig(), Clock: gatewaypreauth.SystemClock{}}
	engine.Transport = TransportDeps{Governor: NopConcurrencyGovernor{}, URLPolicy: PassthroughUpstreamURLPolicy{}}
	engine.ResponseTransformer = transformer
	return engine
}

func TestPerformUpstreamRequestAttemptAppliesResponseTransformer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upstream":"raw"}`))
	}))
	defer upstream.Close()

	transformer := &recordingTransformer{nextBody: `{"converted":true}`}
	engine := newTransformTestEngine(transformer)
	request := newBridgeTransformRequest(t, `{"model":"m"}`)
	response, err := engine.PerformUpstreamRequestAttempt(context.Background(), AttemptInput{
		Req:     request,
		Account: AccountCandidate{ID: "acc-1"},
		UpstreamURL: upstream.URL,
		Headers: http.Header{},
		TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
	})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if transformer.calls != 1 {
		t.Fatalf("transformer calls = %d", transformer.calls)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.TrimSpace(string(body)) != `{"converted":true}` {
		t.Fatalf("transformed body = %s", body)
	}
	if response.Header.Get("X-Bridge-Transformed") != "1" {
		t.Fatalf("transformed headers missing")
	}
	if transformer.lastInput.Account.ID != "acc-1" {
		t.Fatalf("transform input account = %+v", transformer.lastInput.Account)
	}
}

func TestPerformUpstreamRequestAttemptNilTransformerKeepsRaw(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"upstream":"raw"}`))
	}))
	defer upstream.Close()

	engine := newTransformTestEngine(nil)
	request := newBridgeTransformRequest(t, `{"model":"m"}`)
	response, err := engine.PerformUpstreamRequestAttempt(context.Background(), AttemptInput{
		Req:            request,
		Account:        AccountCandidate{ID: "acc-1"},
		UpstreamURL:    upstream.URL,
		Headers:        http.Header{},
		TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
	})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.TrimSpace(string(body)) != `{"upstream":"raw"}` {
		t.Fatalf("raw body = %s", body)
	}
}

func TestPerformUpstreamRequestAttemptTransformerPassThrough(t *testing.T) {
	// 端口返回 nil（非桥请求）时保持原始响应。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"upstream":"raw"}`))
	}))
	defer upstream.Close()

	transformer := &recordingTransformer{returnNil: true}
	engine := newTransformTestEngine(transformer)
	request := newBridgeTransformRequest(t, `{"model":"m"}`)
	response, err := engine.PerformUpstreamRequestAttempt(context.Background(), AttemptInput{
		Req:            request,
		Account:        AccountCandidate{ID: "acc-1"},
		UpstreamURL:    upstream.URL,
		Headers:        http.Header{},
		TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
	})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.TrimSpace(string(body)) != `{"upstream":"raw"}` {
		t.Fatalf("raw body = %s", body)
	}
}

func TestPerformUpstreamRequestAttemptTransformerErrorSurfaces(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"upstream":"raw"}`))
	}))
	defer upstream.Close()

	transformer := &recordingTransformer{failWith: errFakeTransform}
	engine := newTransformTestEngine(transformer)
	request := newBridgeTransformRequest(t, `{"model":"m"}`)
	_, err := engine.PerformUpstreamRequestAttempt(context.Background(), AttemptInput{
		Req:            request,
		Account:        AccountCandidate{ID: "acc-1"},
		UpstreamURL:    upstream.URL,
		Headers:        http.Header{},
		TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
	})
	if err != errFakeTransform {
		t.Fatalf("err = %v, want errFakeTransform", err)
	}
}

var errFakeTransform = &fakeTransformError{}

type fakeTransformError struct{}

func (*fakeTransformError) Error() string { return "bridge transform failed" }

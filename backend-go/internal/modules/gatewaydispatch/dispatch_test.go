package gatewaydispatch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayupstream"
	"juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestDispatchBuildsAndSendsOneAttemptWithoutClassifyingResponse(t *testing.T) {
	client := &recordingClient{response: &http.Response{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader(`{"error":"slow down"}`)),
	}}
	dispatcher := Dispatcher{Client: client, Builder: gatewayupstream.Builder{MaxBodyBytes: 1024}}
	credential, err := gatewayupstream.NewCredential("secret", gatewayupstream.CredentialOptions{})
	if err != nil {
		t.Fatalf("NewCredential() error = %v", err)
	}
	result, err := dispatcher.Dispatch(gatewayupstream.Input{
		Context: context.Background(),
		Request: gateway.RequestShape{Method: http.MethodPost, Path: "/v1/responses"},
		Candidate: port.GatewayAccountCandidate{
			ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key",
		},
		BaseURL:    "https://upstream.example.test",
		Credential: credential,
		Body:       []byte(`{"model":"gpt-5.5"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if result.Definition.ID != "openai-v1" || result.Response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("result = %#v", result)
	}
	if client.request == nil || client.request.URL.Path != "/v1/responses" {
		t.Fatalf("request = %#v", client.request)
	}
	if client.request.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("authorization = %q", client.request.Header.Get("Authorization"))
	}
}

func TestReadBodyBoundsAndClosesResponse(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader("123456")}
	dispatcher := Dispatcher{MaxResponseBodyBytes: 5}
	_, err := dispatcher.ReadBody(Result{Response: &http.Response{Body: body}})
	if !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("ReadBody() error = %v, want body-too-large", err)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestReadBodyReturnsCloseErrorAfterSuccessfulRead(t *testing.T) {
	closeErr := errors.New("close failed")
	body := &trackingBody{Reader: strings.NewReader("ok"), closeErr: closeErr}
	_, err := (Dispatcher{MaxResponseBodyBytes: 10}).ReadBody(Result{Response: &http.Response{Body: body}})
	if !errors.Is(err, ErrResponseBodyClose) || !errors.Is(err, closeErr) {
		t.Fatalf("ReadBody() error = %v", err)
	}
}

func TestRelayClosesResponseAndUsesHTTPStatus(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader("data: [DONE]\n\n")}
	result := Result{Response: &http.Response{StatusCode: http.StatusOK, Body: body}}
	sink := &bytes.Buffer{}
	relayResult, err := (Dispatcher{}).Relay(context.Background(), result, sinkAdapter{buffer: sink}, gatewaystreamrelay.Options{Limits: gatewaystreamrelay.Limits{
		MaxBytes: 1024, BufferBytes: 32, IdleTimeout: time.Second, TotalTimeout: time.Second,
	}})
	if err != nil {
		t.Fatalf("Relay() error = %v", err)
	}
	if sink.String() != "data: [DONE]\n\n" || !body.closed || relayResult.Handoff.Usage.StatusCode == nil || *relayResult.Handoff.Usage.StatusCode != http.StatusOK {
		t.Fatalf("sink/body/result = %q/%v/%#v", sink.String(), body.closed, relayResult)
	}
}

func TestDispatchPreservesTransportError(t *testing.T) {
	transportErr := errors.New("connection reset")
	credential, _ := gatewayupstream.NewCredential("secret", gatewayupstream.CredentialOptions{})
	_, err := (Dispatcher{Client: &recordingClient{err: transportErr}}).Dispatch(gatewayupstream.Input{
		Context: context.Background(), Request: gateway.RequestShape{Method: "POST", Path: "/v1/responses"},
		Candidate: port.GatewayAccountCandidate{ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key"},
		BaseURL:   "https://upstream.example.test", Credential: credential,
	})
	if !errors.Is(err, transportErr) {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

type recordingClient struct {
	request  *http.Request
	response *http.Response
	err      error
}

func (c *recordingClient) Do(request *http.Request) (*http.Response, error) {
	c.request = request
	return c.response, c.err
}

type trackingBody struct {
	*strings.Reader
	closed   bool
	closeErr error
}

func (b *trackingBody) Close() error {
	b.closed = true
	return b.closeErr
}

type sinkAdapter struct{ buffer *bytes.Buffer }

func (s sinkAdapter) Write(_ context.Context, p []byte) (int, error) { return s.buffer.Write(p) }

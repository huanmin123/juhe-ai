package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
)

func TestReadGatewayResponsesInboundReadsOnceAndReturnsCopySafeFacts(t *testing.T) {
	body := &gatewayInboundTrackingBody{reader: strings.NewReader(`{"model":"gpt-5.6","stream":true,"input":[{"role":"user","content":"hello"}]}`)}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	request.Header.Set("X-Codex-Turn-Metadata", `{"turn_id":"turn-1","session_id":"session-1"}`)

	inbound, err := ReadGatewayResponsesInbound(request)
	if err != nil {
		t.Fatalf("ReadGatewayResponsesInbound() error = %v", err)
	}
	if body.closes != 1 {
		t.Fatalf("body close count = %d, want 1", body.closes)
	}
	if body.reads == 0 {
		t.Fatal("reader was not consumed")
	}
	metadata := inbound.Metadata()
	if metadata.Model != "gpt-5.6" || !metadata.Stream || !metadata.CodexTurnMetadataValid {
		t.Fatalf("metadata = %#v", metadata)
	}
	if got := inbound.Preparation().Protocol(); got != gatewayrequestprep.ProtocolOpenAI {
		t.Fatalf("preparation protocol = %q, want OpenAI", got)
	}
	if got := inbound.Preparation().DownstreamProtocol(); got != gatewayrequestprep.DownstreamResponsesSSE {
		t.Fatalf("preparation downstream = %q, want responses SSE", got)
	}
	if got := inbound.Preparation().ClientProfile(); got != gatewayrequestprep.ClientProfileCodex {
		t.Fatalf("preparation client profile = %q, want Codex", got)
	}
	first := inbound.RawBody()
	first[0] = 'x'
	if got := string(inbound.RawBody()); !strings.HasPrefix(got, `{"model"`) {
		t.Fatalf("RawBody leaked mutable backing storage: %q", got)
	}
}

func TestReadGatewayResponsesInboundRejectsNonCanonicalMethodOrPathAndClosesBody(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   error
	}{
		{name: "method", method: http.MethodGet, path: "/v1/responses", want: ErrGatewayResponsesInboundMethod},
		{name: "trailing slash", method: http.MethodPost, path: "/v1/responses/", want: ErrGatewayResponsesInboundPath},
		{name: "query", method: http.MethodPost, path: "/v1/responses?debug=true", want: ErrGatewayResponsesInboundPath},
		{name: "empty query", method: http.MethodPost, path: "/v1/responses?", want: ErrGatewayResponsesInboundPath},
		{name: "escaped slash", method: http.MethodPost, path: "/v1%2Fresponses", want: ErrGatewayResponsesInboundPath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &gatewayInboundTrackingBody{reader: strings.NewReader(`{"model":"gpt","stream":false}`)}
			request := httptest.NewRequest(test.method, test.path, body)
			_, err := ReadGatewayResponsesInbound(request)
			if !errors.Is(err, test.want) {
				t.Fatalf("ReadGatewayResponsesInbound() error = %v, want %v", err, test.want)
			}
			if body.reads != 0 {
				t.Fatalf("body reads = %d, want no read before route validation", body.reads)
			}
			if body.closes != 1 {
				t.Fatalf("body closes = %d, want 1", body.closes)
			}
		})
	}
}

func TestReadGatewayResponsesInboundRejectsNonOriginFormURI(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "scheme", mutate: func(request *http.Request) { request.URL.Scheme = "https" }},
		{name: "host", mutate: func(request *http.Request) { request.URL.Host = "gateway.example.test" }},
		{name: "user", mutate: func(request *http.Request) { request.URL.User = url.User("user") }},
		{name: "opaque", mutate: func(request *http.Request) { request.URL.Opaque = "//gateway.example.test/v1/responses" }},
		{name: "raw fragment", mutate: func(request *http.Request) { request.URL.RawFragment = "fragment" }},
		{name: "raw path", mutate: func(request *http.Request) { request.URL.RawPath = "/v1%2Fresponses" }},
		{name: "request URI", mutate: func(request *http.Request) { request.RequestURI = "/v1/responses?debug=true" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &gatewayInboundTrackingBody{reader: strings.NewReader(`{"model":"gpt","stream":false}`)}
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
			test.mutate(request)
			_, err := ReadGatewayResponsesInbound(request)
			if !errors.Is(err, ErrGatewayResponsesInboundPath) {
				t.Fatalf("ReadGatewayResponsesInbound() error = %v, want path error", err)
			}
			if body.reads != 0 || body.closes != 1 {
				t.Fatalf("body reads/closes = %d/%d, want 0/1", body.reads, body.closes)
			}
		})
	}
}

func TestReadGatewayResponsesInboundEnforcesContentLengthAndChunkedLimits(t *testing.T) {
	reader := GatewayResponsesInboundReader{RawBodyHardLimitBytes: 32}
	t.Run("content length preflight", func(t *testing.T) {
		body := &gatewayInboundTrackingBody{reader: strings.NewReader(`{"model":"gpt","stream":false}`)}
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
		request.ContentLength = 33
		_, err := reader.Read(request)
		if !errors.Is(err, ErrGatewayResponsesInboundBodyTooLarge) {
			t.Fatalf("ReadGatewayResponsesInbound() error = %v", err)
		}
		if body.reads != 0 || body.closes != 1 {
			t.Fatalf("body reads/closes = %d/%d, want 0/1", body.reads, body.closes)
		}
	})

	t.Run("chunked body", func(t *testing.T) {
		body := &gatewayInboundTrackingBody{reader: io.LimitReader(strings.NewReader(strings.Repeat("x", 34)), 34)}
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
		request.ContentLength = -1
		request.TransferEncoding = []string{"chunked"}
		_, err := reader.Read(request)
		if !errors.Is(err, ErrGatewayResponsesInboundBodyTooLarge) {
			t.Fatalf("ReadGatewayResponsesInbound() error = %v", err)
		}
		if body.reads == 0 || body.closes != 1 {
			t.Fatalf("body reads/closes = %d/%d, want >0/1", body.reads, body.closes)
		}
	})
}

func TestGatewayResponsesInboundReaderDefaultsToRawIngressHardLimit(t *testing.T) {
	tests := []struct {
		name   string
		reader GatewayResponsesInboundReader
		want   int64
	}{
		{name: "default", want: gatewayResponsesInboundDefaultRawBodyHardLimitBytes},
		{name: "zero", reader: GatewayResponsesInboundReader{RawBodyHardLimitBytes: 0}, want: gatewayResponsesInboundDefaultRawBodyHardLimitBytes},
		{name: "negative", reader: GatewayResponsesInboundReader{RawBodyHardLimitBytes: -1}, want: gatewayResponsesInboundDefaultRawBodyHardLimitBytes},
		{name: "above hard cap", reader: GatewayResponsesInboundReader{RawBodyHardLimitBytes: gatewayResponsesInboundDefaultRawBodyHardLimitBytes + 1}, want: gatewayResponsesInboundDefaultRawBodyHardLimitBytes},
		{name: "isolated lower cap", reader: GatewayResponsesInboundReader{RawBodyHardLimitBytes: 32}, want: 32},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.reader.rawBodyHardLimitBytes(); got != test.want {
				t.Fatalf("rawBodyHardLimitBytes() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestReadGatewayResponsesInboundRejectsMalformedAndDuplicateJSON(t *testing.T) {
	tests := []string{
		`{"model":`,
		`{"model":"first","model":"second"}`,
		`{"model":"gpt","stream":false,"input":{"item":1,"item":2}}`,
		`{"model":true}`,
		`{"model":"gpt","stream":"true"}`,
		`[]`,
		`{"model":"gpt"} {}`,
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			body := &gatewayInboundTrackingBody{reader: strings.NewReader(raw)}
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
			_, err := ReadGatewayResponsesInbound(request)
			if !errors.Is(err, ErrGatewayResponsesInboundJSON) {
				t.Fatalf("ReadGatewayResponsesInbound() error = %v, want invalid JSON", err)
			}
			if body.closes != 1 {
				t.Fatalf("body closes = %d, want 1", body.closes)
			}
		})
	}
}

func TestReadGatewayResponsesInboundUsesOnlyTopLevelMetadata(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt","stream":false,"input":{"model":"not-a-route-model","stream":true}}`))
	inbound, err := ReadGatewayResponsesInbound(request)
	if err != nil {
		t.Fatalf("ReadGatewayResponsesInbound() error = %v", err)
	}
	if got := inbound.Metadata(); got.Model != "gpt" || got.Stream {
		t.Fatalf("metadata = %#v, want top-level model and stream only", got)
	}
}

func TestReadGatewayResponsesInboundSkipsBoundedUnknownScalarsWithoutDecodingThem(t *testing.T) {
	reader := GatewayResponsesInboundReader{RawBodyHardLimitBytes: 256 << 10}
	huge := strings.Repeat("x", 96<<10)
	tests := []string{
		`{"model":"gpt","stream":false,"unknown":"` + huge + `"}`,
		`{"model":"gpt","stream":false,"unknown":` + strings.Repeat("9", 96<<10) + `}`,
	}
	for _, raw := range tests {
		inbound, err := reader.Read(httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(raw)))
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if got := inbound.Metadata(); got.Model != "gpt" || got.Stream {
			t.Fatalf("metadata = %#v, want only known top-level facts", got)
		}
	}

	tooLargeKey := `{"` + strings.Repeat("k", gatewayInboundJSONMaxKeyLiteralBytes+1) + `":true}`
	_, err := reader.Read(httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tooLargeKey)))
	if !errors.Is(err, ErrGatewayResponsesInboundJSON) {
		t.Fatalf("Read() huge unknown key error = %v, want invalid JSON metadata", err)
	}
}

func TestReadGatewayResponsesInboundPropagatesRequestCancellationAndClosesBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &gatewayInboundCancelOnReadBody{reader: strings.NewReader(`{"model":"gpt","stream":false}`), cancel: cancel}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", body).WithContext(ctx)
	_, err := ReadGatewayResponsesInbound(request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadGatewayResponsesInbound() error = %v, want context cancellation", err)
	}
	if body.reads == 0 || body.closes != 1 {
		t.Fatalf("body reads/closes = %d/%d, want >0/1", body.reads, body.closes)
	}
}

func TestReadGatewayResponsesInboundCancellationClosesBlockingBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := newGatewayInboundBlockingBody()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", body).WithContext(ctx)
	type readResult struct{ err error }
	done := make(chan readResult, 1)
	go func() {
		_, err := ReadGatewayResponsesInbound(request)
		done <- readResult{err: err}
	}()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("body read did not start")
	}
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("ReadGatewayResponsesInbound() error = %v, want context cancellation", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close and unblock body")
	}
	if body.closes != 1 {
		t.Fatalf("body closes = %d, want 1", body.closes)
	}
}

type gatewayInboundTrackingBody struct {
	reader io.Reader
	reads  int
	closes int
}

func (b *gatewayInboundTrackingBody) Read(p []byte) (int, error) {
	b.reads++
	return b.reader.Read(p)
}

func (b *gatewayInboundTrackingBody) Close() error {
	b.closes++
	return nil
}

type gatewayInboundCancelOnReadBody struct {
	reader io.Reader
	cancel context.CancelFunc
	reads  int
	closes int
}

func (b *gatewayInboundCancelOnReadBody) Read(p []byte) (int, error) {
	b.reads++
	n, err := b.reader.Read(p)
	b.cancel()
	return n, err
}

func (b *gatewayInboundCancelOnReadBody) Close() error {
	b.closes++
	return nil
}

type gatewayInboundBlockingBody struct {
	started   chan struct{}
	released  chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	closes    int
}

func newGatewayInboundBlockingBody() *gatewayInboundBlockingBody {
	return &gatewayInboundBlockingBody{started: make(chan struct{}), released: make(chan struct{})}
}

func (b *gatewayInboundBlockingBody) Read([]byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-b.released
	return 0, io.EOF
}

func (b *gatewayInboundBlockingBody) Close() error {
	b.closeOnce.Do(func() {
		b.closes++
		close(b.released)
	})
	return nil
}

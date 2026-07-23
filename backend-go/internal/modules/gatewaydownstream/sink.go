package gatewaydownstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpguts"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
)

const (
	MaxHeaderFields = 128
	MaxHeaderBytes  = 64 << 10
)

type Mode string

const (
	ModeOpaque Mode = "opaque"
	ModeJSON   Mode = "json"
	ModeSSE    Mode = "sse"
)

type Plan struct {
	StatusCode int
	Header     http.Header
	Mode       Mode
}

type StagedSink interface {
	gatewaystreamrelay.StatefulSink
	Stage(Plan) error
}

var staticDeniedHeaders = map[string]struct{}{
	"alt-svc": {}, "connection": {}, "content-length": {},
	"keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {}, "proxy-connection": {},
	"set-cookie": {}, "te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
	"x-accel-buffering": {},
}

var deniedHeaderPrefixes = []string{"cf-aig-", "helicone-", "x-bt-", "x-kong-", "x-litellm-", "x-portkey-"}

func NewPlan(statusCode int, upstream http.Header, mode Mode, bodyBytes *int64) (Plan, error) {
	if statusCode < 100 || statusCode > 599 {
		return Plan{}, fmt.Errorf("gateway downstream status code is invalid")
	}
	if mode != ModeOpaque && mode != ModeJSON && mode != ModeSSE {
		return Plan{}, fmt.Errorf("gateway downstream mode is invalid")
	}
	if mode != ModeOpaque && encodedBody(upstream) {
		return Plan{}, fmt.Errorf("gateway downstream encoded structured response is unsupported")
	}
	dynamicDenied := connectionTokens(upstream)
	header := make(http.Header)
	for name, values := range upstream {
		lower := strings.ToLower(strings.TrimSpace(name))
		if !httpguts.ValidHeaderFieldName(name) || gatewayOwnedHeader(lower) || headerDenied(lower, dynamicDenied, mode) {
			continue
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				continue
			}
			header.Add(name, value)
		}
	}
	if mode == ModeSSE {
		if header.Get("Content-Type") == "" {
			header.Set("Content-Type", "text/event-stream; charset=utf-8")
		}
		if header.Get("Cache-Control") == "" {
			header.Set("Cache-Control", "no-cache, no-transform")
		}
		header.Set("X-Accel-Buffering", "no")
	} else if bodyBytes != nil && responseAllowsBody(statusCode) {
		if *bodyBytes < 0 {
			return Plan{}, fmt.Errorf("gateway downstream body length is invalid")
		}
		header.Set("Content-Length", strconv.FormatInt(*bodyBytes, 10))
	}
	if len(header) > MaxHeaderFields || headerBytes(header) > MaxHeaderBytes {
		return Plan{}, fmt.Errorf("gateway downstream response headers exceed limit")
	}
	return Plan{StatusCode: statusCode, Header: header.Clone(), Mode: mode}, nil
}

func encodedBody(header http.Header) bool {
	for _, value := range header.Values("Content-Encoding") {
		for _, encoding := range strings.Split(value, ",") {
			encoding = strings.TrimSpace(encoding)
			if encoding != "" && !strings.EqualFold(encoding, "identity") {
				return true
			}
		}
	}
	return false
}

type HTTPWriterSink struct {
	mu     sync.Mutex
	writer http.ResponseWriter
	plan   Plan
	staged bool
	state  gatewaystreamrelay.SinkState
}

func NewHTTPWriterSink(writer http.ResponseWriter) (*HTTPWriterSink, error) {
	if writer == nil {
		return nil, fmt.Errorf("gateway downstream response writer is required")
	}
	return &HTTPWriterSink{writer: writer}, nil
}

func (s *HTTPWriterSink) Stage(plan Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.TransportCommitted {
		return fmt.Errorf("gateway downstream response is already committed")
	}
	if err := validatePlan(plan); err != nil {
		return fmt.Errorf("gateway downstream plan is invalid")
	}
	s.plan = Plan{StatusCode: plan.StatusCode, Header: plan.Header.Clone(), Mode: plan.Mode}
	s.staged = true
	return nil
}

func validatePlan(plan Plan) error {
	if plan.StatusCode < 100 || plan.StatusCode > 599 || plan.Header == nil {
		return fmt.Errorf("invalid status or headers")
	}
	if plan.Mode != ModeOpaque && plan.Mode != ModeJSON && plan.Mode != ModeSSE {
		return fmt.Errorf("invalid mode")
	}
	if len(plan.Header) > MaxHeaderFields || headerBytes(plan.Header) > MaxHeaderBytes {
		return fmt.Errorf("headers exceed limit")
	}
	for name, values := range plan.Header {
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("invalid header name")
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("invalid header value")
			}
		}
	}
	return nil
}

func (s *HTTPWriterSink) Commit(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.TransportCommitted {
		return nil
	}
	if !s.staged {
		return fmt.Errorf("gateway downstream response head is not staged")
	}
	applyHeader(s.writer.Header(), s.plan.Header)
	s.writer.WriteHeader(s.plan.StatusCode)
	s.state.TransportCommitted = true
	if s.plan.Mode == ModeSSE {
		if err := http.NewResponseController(s.writer).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	return nil
}

func (s *HTTPWriterSink) Write(ctx context.Context, body []byte) (int, error) {
	if err := s.Commit(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	controller := http.NewResponseController(s.writer)
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return 0, err
			}
			defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		}
	}
	n, err := s.writer.Write(body)
	s.state.DownstreamBytes += int64(max(n, 0))
	return n, err
}

func (s *HTTPWriterSink) MarkSemantic() {
	s.mu.Lock()
	s.state.SemanticCommitted = true
	s.mu.Unlock()
}

func (s *HTTPWriterSink) Snapshot() gatewaystreamrelay.SinkState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func connectionTokens(header http.Header) map[string]struct{} {
	result := make(map[string]struct{})
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.ToLower(strings.TrimSpace(token)); token != "" {
				result[token] = struct{}{}
			}
		}
	}
	return result
}

func headerDenied(name string, dynamic map[string]struct{}, mode Mode) bool {
	if name == "content-encoding" && mode != ModeOpaque {
		return true
	}
	if _, denied := staticDeniedHeaders[name]; denied {
		return true
	}
	if _, denied := dynamic[name]; denied {
		return true
	}
	for _, prefix := range deniedHeaderPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func responseAllowsBody(status int) bool {
	return status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}

func headerBytes(header http.Header) int {
	total := 0
	for name, values := range header {
		for _, value := range values {
			total += len(name) + len(value) + 4
		}
	}
	return total
}

func applyHeader(target, source http.Header) {
	for name := range target {
		if !gatewayOwnedHeader(name) {
			target.Del(name)
		}
	}
	for name, values := range source {
		if gatewayOwnedHeader(name) && len(target.Values(name)) > 0 {
			continue
		}
		target.Del(name)
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func gatewayOwnedHeader(name string) bool {
	name = strings.ToLower(name)
	return name == "x-request-id" || name == "x-trace-id" || name == "server-timing" || strings.HasPrefix(name, "access-control-")
}

var _ StagedSink = (*HTTPWriterSink)(nil)

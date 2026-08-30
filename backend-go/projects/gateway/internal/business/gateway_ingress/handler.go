// Package gatewayingress provides the HTTP boundary used by a future Gateway
// public caller. It deliberately owns no routing, credential, or upstream
// protocol decisions: those stay behind the injected DispatchPort.
package gatewayingress

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

var ErrDispatcherRequired = errors.New("gateway ingress dispatcher is required")

// DispatchPort is the sole way this HTTP boundary reaches a caller. The
// request retains its original context and body, so cancellation and streaming
// request bodies reach the injected implementation without buffering here.
// Implementations own all routing, candidate, credential, and protocol work.
type DispatchPort interface {
	Dispatch(context.Context, *http.Request) (Response, error)
}

// Response is an upstream response supplied by the injected dispatcher.
// Finish is optional and runs exactly once after the handler has determined
// whether the response body was relayed completely. It must not retain the
// request body or log credentials.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
	Finish     func(context.Context, Outcome) error
}

type Outcome string

const (
	OutcomeComplete Outcome = "complete"
	OutcomeAborted  Outcome = "aborted"
)

// Handler is a process-local HTTP handler. Constructing it does not open a
// listener or provide any public route; the owner process must explicitly
// mount it after wiring a real dispatcher.
type Handler struct {
	Dispatcher DispatchPort
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway_ingress_unavailable", ErrDispatcherRequired.Error())
		return
	}
	if r == nil {
		writeError(w, http.StatusBadRequest, "gateway_ingress_invalid_request", "gateway ingress request is required")
		return
	}

	response, err := h.Dispatcher.Dispatch(r.Context(), r)
	if err != nil {
		writeDispatchError(w, r.Context(), err)
		return
	}
	if response.Body == nil {
		if response.Finish != nil {
			_ = response.Finish(r.Context(), OutcomeAborted)
		}
		writeError(w, http.StatusBadGateway, "gateway_upstream_invalid_response", "gateway upstream response body is required")
		return
	}
	copyHeaders(w.Header(), response.Header)
	status := response.StatusCode
	if status < 100 || status > 999 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	copyErr := relayBody(w, response.Body)
	closeErr := response.Body.Close()
	outcome := OutcomeComplete
	if copyErr != nil || closeErr != nil || r.Context().Err() != nil {
		outcome = OutcomeAborted
	}
	if response.Finish != nil {
		// A disconnected peer cancels r.Context(); Finish receives the same
		// cancellation signal so an injected dispatcher can stop its work.
		_ = response.Finish(r.Context(), outcome)
	}
}

func relayBody(w http.ResponseWriter, body io.Reader) error {
	buffer := make([]byte, 32<<10)
	flusher, canFlush := w.(http.Flusher)
	for {
		read, readErr := body.Read(buffer)
		if read > 0 {
			if _, writeErr := w.Write(buffer[:read]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if read == 0 {
			return io.ErrNoProgress
		}
	}
}

func writeDispatchError(w http.ResponseWriter, ctx context.Context, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		writeError(w, 499, "gateway_request_cancelled", "gateway request was cancelled")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "gateway_request_timeout", "gateway request timed out")
	default:
		writeError(w, http.StatusBadGateway, "gateway_upstream_unavailable", "gateway upstream request failed")
	}
}

func copyHeaders(target, source http.Header) {
	for name, values := range source {
		if isHopByHopHeader(name) {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

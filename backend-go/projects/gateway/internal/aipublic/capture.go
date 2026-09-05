// Capture hook for the /__aipublic__ family, ported from
// modules/public-api-logs/public-api-log-capture.middleware.ts mounted at
// publicApiPrefix (system-api-app.ts:129). aipublic owns the request-lifecycle
// wrapping — the first response payload (the res.json/res.send interception),
// the finish status, the client-closed 499 mapping and the JSON body
// parse-failed marker — while the sink stays a port: Deps.Capture receives the
// finished publicapilogs.CaptureSpec and bridges to the
// internal/publicapilogs pipeline (BuildInput/Enqueue live there). Nil keeps
// the routes functional without capture.
package aipublic

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/publicapilogs"
)

// PublicApiLogCapture is the capture port: exactly one call per request after
// the response is written (or the client went away). Implementations hand the
// spec to publicapilogs (BuildInput + Pipeline.Enqueue).
type PublicApiLogCapture interface {
	CaptureAIPublic(spec publicapilogs.CaptureSpec)
}

// PublicApiLogCaptureSink adapts a raw input sink (normally
// publicapilogs.Pipeline.Enqueue) into the port, running BuildInput here so
// adapters stay one-liners.
type PublicApiLogCaptureSink func(publicapilogs.Input) bool

// CaptureAIPublic implements the port through the shared publicapilogs
// Capture recorder (BuildInput + the exactly-once flag).
func (sink PublicApiLogCaptureSink) CaptureAIPublic(spec publicapilogs.CaptureSpec) {
	if sink == nil {
		return
	}
	recorder := publicapilogs.NewCapture(spec, sink)
	recorder.Now = func() time.Time { return spec.EndedAt }
	if spec.Closed {
		recorder.RecordClosed(spec.StatusCode, spec.ResponsePayload)
		return
	}
	recorder.RecordFinish(spec.StatusCode, spec.ResponsePayload)
}

// captureSnapshotBudget mirrors publicApiSnapshotMaxBytes: the wrapper keeps
// at most this many response bytes for the snapshot preview.
const captureSnapshotBudget = 32 * 1024

// captureResponseWriter mirrors the res.json/res.send interception: the first
// response payload (status + body bytes) feeds the response snapshot.
type captureResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	payload     []byte
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(payload []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	if len(w.payload) < captureSnapshotBudget {
		room := captureSnapshotBudget - len(w.payload)
		if len(payload) < room {
			room = len(payload)
		}
		w.payload = append(w.payload, payload[:room]...)
	}
	return w.ResponseWriter.Write(payload)
}

// Flush forwards to the wrapped writer when it supports flushing (the capture
// stays transparent for streaming responses).
func (w *captureResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// captureRequestBody buffers the JSON body exactly like the Node body parser
// running under the capture middleware: the bytes stay readable downstream and
// the parse failure surfaces as the publicApiRequestBodyRejected marker.
type captureRequestBody struct {
	raw         []byte
	parseFailed bool
}

// bufferCaptureRequestBody caches the request body for POST/PUT/PATCH (the
// Node body-parser methods) without consuming it.
func bufferCaptureRequestBody(r *http.Request) *captureRequestBody {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return nil
	}
	if r.Body == nil {
		return &captureRequestBody{}
	}
	raw, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return &captureRequestBody{parseFailed: true}
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	buffered := &captureRequestBody{raw: raw}
	if len(bytes.TrimSpace(raw)) > 0 {
		var parsed map[string]any
		if json.Unmarshal(raw, &parsed) != nil {
			buffered.parseFailed = true
		}
	}
	return buffered
}

// captureSourceContext maps the authenticated aipublic context onto the
// source fields (res.locals.externalIntegrationSource); unauthenticated
// requests stay nil exactly like Node.
func captureSourceContext(context *AuthContext) *publicapilogs.SourceContext {
	if context == nil {
		return nil
	}
	return &publicapilogs.SourceContext{
		SourceRefID: context.SourceRefID,
		SourceName:  context.SourceName,
		TokenID:     context.TokenID,
		TokenName:   context.TokenName,
		TokenPrefix: context.TokenPrefix,
		IsTestToken: context.IsTestToken,
	}
}

// specQuery mirrors express req.query: an object whose values are strings for
// single occurrences and arrays for repeated keys.
func specQuery(r *http.Request) map[string]any {
	values := r.URL.Query()
	query := make(map[string]any, len(values))
	for key, items := range values {
		if len(items) == 1 {
			query[key] = items[0]
			continue
		}
		list := make([]any, 0, len(items))
		for _, item := range items {
			list = append(list, item)
		}
		query[key] = list
	}
	return query
}

// recordCapture assembles the finished CaptureSpec (the buildPublicApiLogInput
// inputs) and hands it to the port.
func (d *Deps) recordCapture(spec publicapilogs.CaptureSpec) {
	if d.Capture == nil {
		return
	}
	d.Capture.CaptureAIPublic(spec)
}

// captureSourceHolder carries the authenticated source out of the wrapped
// handler: the auth context enters the request context only inside the guard
// (after bearer validation), while the record happens after the handler
// returns.
type captureSourceHolder struct {
	mutex  sync.Mutex
	source *AuthContext
}

func (h *captureSourceHolder) set(source *AuthContext) {
	if h == nil {
		return
	}
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.source = source
}

func (h *captureSourceHolder) get() *AuthContext {
	if h == nil {
		return nil
	}
	h.mutex.Lock()
	defer h.mutex.Unlock()
	return h.source
}

// withCapture wraps the guard handler with the capture lifecycle when the
// port is installed; otherwise the handler runs untouched (nil-safe). The
// source context arrives through the holder (res.locals.externalIntegrationSource):
// unauthenticated/rate-limited requests record with a nil source exactly like
// Node.
func (d *Deps) withCapture(handler func(w http.ResponseWriter, r *http.Request, source *captureSourceHolder)) http.HandlerFunc {
	if d.Capture == nil {
		return func(w http.ResponseWriter, r *http.Request) {
			handler(w, r, nil)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		startedAt := d.clock()
		requestCtx := kernel.Context(r)
		buffered := bufferCaptureRequestBody(r)
		recorder := &captureResponseWriter{ResponseWriter: w, status: http.StatusOK}
		holder := &captureSourceHolder{}
		handler(recorder, r, holder)
		closed := r.Context().Err() != nil
		statusCode := recorder.status
		responsePayload := decodeCapturePayload(recorder.payload)
		var bodyRejected *publicapilogs.BodyRejection
		if buffered != nil && buffered.parseFailed && statusCode >= 400 {
			bodyRejected = &publicapilogs.BodyRejection{StatusCode: statusCode, ErrorType: "entity.parse_failed"}
		}
		d.recordCapture(publicapilogs.CaptureSpec{
			Method:          r.Method,
			BaseURL:         Prefix,
			Path:            r.URL.EscapedPath(),
			OriginalURL:     r.URL.RequestURI(),
			Query:           specQuery(r),
			Body:            decodeCaptureBody(buffered),
			ContentType:     r.Header.Get("Content-Type"),
			ContentLength:   r.Header.Get("Content-Length"),
			UserAgent:       r.Header.Get("User-Agent"),
			StatusCode:      statusCode,
			Closed:          closed,
			ResponsePayload: responsePayload,
			StartedAt:       startedAt,
			EndedAt:         d.clock(),
			DurationMS:      d.clock().Sub(startedAt).Milliseconds(),
			TraceID:         requestCtx.TraceID,
			ClientIP:        requestCtx.ClientIP,
			Source:          captureSourceContext(holder.get()),
			BodyRejected:    bodyRejected,
		})
	}
}

// decodeCapturePayload mirrors the res.json(body) object the Node middleware
// captures: the written JSON text re-decodes into the generic document; a
// non-JSON body degrades to its string form (the res.send(string) branch).
func decodeCapturePayload(payload []byte) any {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil
	}
	var document any
	if json.Unmarshal(trimmed, &document) == nil {
		return document
	}
	return string(payload)
}

// decodeCaptureBody mirrors req.body: the parsed JSON document when the body
// parsed, undefined (nil) when absent or failed (the parse-failed marker
// carries the drop reason instead).
func decodeCaptureBody(buffered *captureRequestBody) any {
	if buffered == nil || buffered.parseFailed || len(bytes.TrimSpace(buffered.raw)) == 0 {
		return nil
	}
	var parsed any
	if json.Unmarshal(buffered.raw, &parsed) != nil {
		return nil
	}
	return parsed
}

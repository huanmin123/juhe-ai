package kernel

import (
	"encoding/json"
	"net/http"
)

// Envelope helpers mirror shared/http.ts ({data, message}) plus the error
// writer contract of system-api-app.ts ({message} with CJK localization).
// All gateway handlers must write JSON through these helpers so the
// localization and envelope contracts hold everywhere. The kernel chain wraps
// every handler in a localizeWriter; MarkUpstreamError flags that wrapper so
// a later WriteError keeps the verbatim message.

type apiResponse struct {
	Data    any    `json:"data"`
	Message string `json:"message,omitempty"`
}

// UpstreamMarker is implemented by the kernel response wrapper.
type UpstreamMarker interface {
	MarkUpstream()
	MarkedUpstream() bool
}

// MarkUpstreamError flags the response so error localization preserves the
// verbatim message (mirror of markResponseErrorMessageAsUpstream).
func MarkUpstreamError(w http.ResponseWriter) {
	if marker, ok := w.(UpstreamMarker); ok {
		marker.MarkUpstream()
	}
}

func upstreamMarked(w http.ResponseWriter) bool {
	if marker, ok := w.(UpstreamMarker); ok {
		return marker.MarkedUpstream()
	}
	return false
}

// localizeWriter records the response status and the upstream-error flag.
type localizeWriter struct {
	http.ResponseWriter
	status           int
	wroteHeader      bool
	preserveUpstream bool
}

func newLocalizeWriter(w http.ResponseWriter) *localizeWriter {
	return &localizeWriter{ResponseWriter: w}
}

func (l *localizeWriter) WriteHeader(status int) {
	if !l.wroteHeader {
		l.status = status
		l.wroteHeader = true
	}
	l.ResponseWriter.WriteHeader(status)
}

func (l *localizeWriter) Write(body []byte) (int, error) {
	if !l.wroteHeader {
		l.status = http.StatusOK
		l.wroteHeader = true
	}
	return l.ResponseWriter.Write(body)
}

func (l *localizeWriter) MarkUpstream()        { l.preserveUpstream = true }
func (l *localizeWriter) MarkedUpstream() bool { return l.preserveUpstream }
func (l *localizeWriter) StatusCode() int      { return l.status }

// WriteOK writes the {data, message} success envelope with status 200.
func WriteOK(w http.ResponseWriter, data any, message string) {
	writeJSON(w, http.StatusOK, apiResponse{Data: data, Message: message}, upstreamMarked(w))
}

// WriteJSON writes an arbitrary JSON body with localization applied when the
// status is an error and the body carries a localizable message field.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	writeJSON(w, status, payload, upstreamMarked(w))
}

// WriteError writes {"message": ...} after localization.
func WriteError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message}, upstreamMarked(w))
}

// WriteBadRequest mirrors sendBadRequest: 400 + {"message"}.
func WriteBadRequest(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusBadRequest, message)
}

// WriteNotFound mirrors sendNotFound: 404 + {"message"}.
func WriteNotFound(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusNotFound, message)
}

// WriteRawJSON writes pre-encoded JSON without localization. Reserved for
// byte-exact payloads (SSE); do not use for error envelopes.
func WriteRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, payload any, preserveUpstream bool) {
	body, err := json.Marshal(payload)
	if err != nil {
		status = http.StatusInternalServerError
		body, _ = json.Marshal(map[string]string{"message": "服务器内部错误"})
		preserveUpstream = false
	}
	if encoded, changed := localizeSystemErrorPayload(body, status, preserveUpstream); changed {
		body = encoded
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// WriteAPINotFound mirrors the system/public API 404 JSON contract
// (system-api-app.ts end-of-chain handler).
func WriteAPINotFound(w http.ResponseWriter) {
	WriteError(w, http.StatusNotFound, "资源不存在")
}

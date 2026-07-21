package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/config"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	"juhe-ai/backend-go/internal/modules/publicapilog"
)

const (
	publicAPIAuthContextKey    contextKey = "public_api_auth_context"
	publicAPIEndpointKey       contextKey = "public_api_endpoint"
	publicAPIRequestBodyKey    contextKey = "public_api_request_body"
	publicAPILogEnqueueTimeout            = 5 * time.Second
)

type PublicAPIAuthenticator interface {
	Authenticate(ctx context.Context, authorizationHeader string, requiredScope string) (publicapiauth.AuthContext, error)
}

type PublicAPIRateLimiter interface {
	Allow(ctx context.Context, authContext publicapiauth.AuthContext) (publicapiratelimit.Decision, error)
}

type PublicAPIShellOptions struct {
	Config                  config.Config
	Logger                  *slog.Logger
	Authenticator           PublicAPIAuthenticator
	RateLimiter             PublicAPIRateLimiter
	LogClient               publicapilogjob.EnqueueClient
	EndpointHandlers        map[string]http.Handler
	Now                     func() time.Time
	NewLogID                func() string
	SkipRequestIDMiddleware bool
}

type publicAPIShell struct {
	logger        *slog.Logger
	clientIPs     clientIPResolver
	authenticator PublicAPIAuthenticator
	rateLimiter   PublicAPIRateLimiter
	logClient     publicapilogjob.EnqueueClient
	handlers      map[string]http.Handler
	now           func() time.Time
	newLogID      func() string
}

type publicAPIRequestState struct {
	endpoint           *publicapi.Endpoint
	authContext        *publicapiauth.AuthContext
	requestBody        any
	requestBodySize    *int64
	bodyRejectedReason string
	responsePayload    any
}

type publicAPIParsedBody struct {
	body           any
	sizeBytes      *int64
	rejectedReason string
	statusCode     int
	message        string
}

func NewPublicAPIShell(opts PublicAPIShellOptions) http.Handler {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newLogID := opts.NewLogID
	if newLogID == nil {
		newLogID = func() string {
			return "publog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	shell := &publicAPIShell{
		logger:        opts.Logger,
		clientIPs:     newClientIPResolver(opts.Config),
		authenticator: opts.Authenticator,
		rateLimiter:   opts.RateLimiter,
		logClient:     opts.LogClient,
		handlers:      clonePublicAPIHandlers(opts.EndpointHandlers),
		now:           now,
		newLogID:      newLogID,
	}

	if opts.SkipRequestIDMiddleware {
		return shell
	}
	return requestIDMiddleware(shell)
}

func PublicAPIAuthContextFromRequest(r *http.Request) (publicapiauth.AuthContext, bool) {
	value, ok := r.Context().Value(publicAPIAuthContextKey).(publicapiauth.AuthContext)
	return value, ok
}

func PublicAPIEndpointFromRequest(r *http.Request) (publicapi.Endpoint, bool) {
	value, ok := r.Context().Value(publicAPIEndpointKey).(publicapi.Endpoint)
	return value, ok
}

func PublicAPIRequestBodyFromRequest(r *http.Request) (any, bool) {
	value := r.Context().Value(publicAPIRequestBodyKey)
	return value, value != nil
}

func (s *publicAPIShell) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := s.now().UTC()
	state := &publicAPIRequestState{}
	capture := newPublicAPIResponseCapture(w)
	responseWriter := capture.ResponseWriter()
	defer s.enqueueLog(r, capture, state, startedAt)
	defer func() {
		if recovered := recover(); recovered != nil {
			if s.logger != nil {
				s.logger.Error("public API shell panic",
					slog.Any("error", recovered),
					slog.String("path", r.URL.Path),
					slog.String("request_id", requestIDFromContext(r.Context())),
				)
			}
			if !capture.WroteHeader() {
				payload := map[string]any{"message": "服务器内部错误"}
				state.responsePayload = payload
				writeJSON(responseWriter, http.StatusInternalServerError, payload)
			}
		}
	}()

	parsedBody := parsePublicAPIJSONBody(w, r)
	state.requestBody = parsedBody.body
	state.requestBodySize = parsedBody.sizeBytes
	state.bodyRejectedReason = parsedBody.rejectedReason
	if parsedBody.rejectedReason != "" {
		payload := map[string]any{"message": parsedBody.message}
		state.responsePayload = payload
		writeJSON(responseWriter, parsedBody.statusCode, payload)
		return
	}

	endpoint, ok := publicapi.FindEndpoint(r.Method, r.URL.Path)
	if !ok {
		payload := map[string]any{"message": "资源不存在"}
		state.responsePayload = payload
		writeJSON(responseWriter, http.StatusNotFound, payload)
		return
	}
	state.endpoint = &endpoint

	if s.authenticator == nil {
		payload := map[string]any{"message": "服务器内部错误"}
		state.responsePayload = payload
		writeJSON(responseWriter, http.StatusInternalServerError, payload)
		return
	}
	authContext, err := s.authenticator.Authenticate(r.Context(), r.Header.Get("Authorization"), endpoint.Scope)
	if err != nil {
		s.writeAuthError(responseWriter, state, err)
		return
	}
	state.authContext = &authContext

	if s.rateLimiter == nil {
		payload := map[string]any{"message": "服务器内部错误"}
		state.responsePayload = payload
		writeJSON(responseWriter, http.StatusInternalServerError, payload)
		return
	}
	decision, err := s.rateLimiter.Allow(r.Context(), authContext)
	if err != nil {
		payload := map[string]any{"message": "服务器内部错误"}
		state.responsePayload = payload
		writeJSON(responseWriter, http.StatusInternalServerError, payload)
		return
	}
	if !decision.Allowed {
		retryAfter := max(1, decision.RetryAfterSeconds)
		responseWriter.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		payload := map[string]any{
			"message": "来源系统调用过于频繁，请稍后重试",
			"code":    "external_source_rate_limited",
			"details": map[string]any{
				"windowSeconds":     decision.Rule.WindowSeconds,
				"maxRequests":       decision.Rule.MaxRequests,
				"retryAfterSeconds": retryAfter,
			},
		}
		state.responsePayload = payload
		writeJSON(responseWriter, http.StatusTooManyRequests, payload)
		return
	}

	handler := s.handlers[endpoint.ID]
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"message": "公开接口尚未迁移",
				"code":    "public_api_endpoint_not_implemented",
			})
		})
	}

	ctx := context.WithValue(r.Context(), publicAPIEndpointKey, endpoint)
	ctx = context.WithValue(ctx, publicAPIAuthContextKey, authContext)
	if state.requestBody != nil {
		ctx = context.WithValue(ctx, publicAPIRequestBodyKey, state.requestBody)
	}
	handler.ServeHTTP(responseWriter, r.WithContext(ctx))
}

func (s *publicAPIShell) writeAuthError(w http.ResponseWriter, state *publicAPIRequestState, err error) {
	var authErr *publicapiauth.AuthError
	if !errors.As(err, &authErr) {
		payload := map[string]any{"message": "服务器内部错误"}
		state.responsePayload = payload
		writeJSON(w, http.StatusInternalServerError, payload)
		return
	}
	if authErr.Context != nil {
		state.authContext = authErr.Context
	}
	payload := map[string]any{
		"message": authErr.Message,
		"code":    authErr.Code,
	}
	state.responsePayload = payload
	writeJSON(w, authErr.StatusCode, payload)
}

func (s *publicAPIShell) enqueueLog(r *http.Request, response *publicAPIResponseCapture, state *publicAPIRequestState, startedAt time.Time) {
	if s.logClient == nil {
		return
	}

	endedAt := s.now().UTC()
	durationMs := max(int64(0), endedAt.Sub(startedAt).Milliseconds())
	responsePayload := state.responsePayload
	if responsePayload == nil {
		responsePayload = response.Payload()
	}
	closed := publicAPIRequestClosed(r, response)
	statusCode := response.StatusCode()
	if closed {
		statusCode = 499
	}
	responseSize := response.SizeBytes()
	queryString := r.URL.RawQuery
	requestSnapshot := publicapilog.BuildRequestSnapshot(publicapilog.RequestSnapshotInput{
		Method:             r.Method,
		Path:               r.URL.Path,
		Query:              publicAPIQueryMap(r.URL.Query()),
		Body:               state.requestBody,
		ContentType:        r.Header.Get("Content-Type"),
		ContentLength:      r.Header.Get("Content-Length"),
		BodySizeBytes:      state.requestBodySize,
		QueryString:        queryString,
		BodyRejectedReason: state.bodyRejectedReason,
	})
	responseSnapshot := publicapilog.BuildResponseSnapshot(publicapilog.ResponseSnapshotInput{
		StatusCode:    statusCode,
		Body:          responsePayload,
		BodySizeBytes: &responseSize,
	})
	errorCode, errorMessage := publicapilog.ErrorInfoFromResponse(responsePayload, statusCode)

	traceID := traceIDFromContext(r.Context())
	if traceID == "" {
		traceID = requestIDFromContext(r.Context())
	}
	logInput := publicapilog.BuildPublicAPILogInput(publicapilog.BuildInput{
		ID:               s.newLogID(),
		TraceID:          traceID,
		Source:           publicAPILogSourceContext(state.authContext),
		Method:           r.Method,
		Path:             r.URL.Path,
		QueryString:      queryString,
		ClientIP:         s.clientIPs.FromRequest(r),
		UserAgent:        r.UserAgent(),
		StatusCode:       statusCode,
		DurationMs:       durationMs,
		RequestSnapshot:  requestSnapshot,
		ResponseSnapshot: responseSnapshot,
		ErrorCode:        errorCode,
		ErrorMessage:     errorMessage,
		StartedAt:        startedAt,
		EndedAt:          endedAt,
		Closed:           closed,
	})
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), publicAPILogEnqueueTimeout)
	defer cancel()
	if _, err := publicapilogjob.EnqueueWrite(enqueueCtx, s.logClient, logInput); err != nil && s.logger != nil {
		s.logger.Warn("公开接口日志入队失败",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()),
		)
	}
}

func publicAPIRequestClosed(r *http.Request, response *publicAPIResponseCapture) bool {
	return errors.Is(r.Context().Err(), context.Canceled) || publicAPIWriteErrorIsClientClosed(response.WriteError())
}

func publicAPIWriteErrorIsClientClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "use of closed network connection")
}

func parsePublicAPIJSONBody(w http.ResponseWriter, r *http.Request) publicAPIParsedBody {
	if !publicAPIRequestMayHaveBody(r) {
		return publicAPIParsedBody{}
	}
	if !publicAPIRequestIsJSON(r) {
		size := r.ContentLength
		if size < 0 {
			size = 0
		}
		return publicAPIParsedBody{sizeBytes: &size}
	}

	if r.ContentLength > publicapi.JSONBodyLimitBytes {
		size := r.ContentLength
		return publicAPIParsedBody{
			sizeBytes:      &size,
			rejectedReason: "request_body_too_large",
			statusCode:     http.StatusRequestEntityTooLarge,
			message:        "请求体过大",
		}
	}

	limited := http.MaxBytesReader(w, r.Body, publicapi.JSONBodyLimitBytes)
	defer func() {
		_ = limited.Close()
	}()

	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	var body any
	if err := decoder.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			size := int64(0)
			return publicAPIParsedBody{sizeBytes: &size}
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			size := maxBytesErr.Limit + 1
			return publicAPIParsedBody{
				sizeBytes:      &size,
				rejectedReason: "request_body_too_large",
				statusCode:     http.StatusRequestEntityTooLarge,
				message:        "请求体过大",
			}
		}
		size := r.ContentLength
		if size < 0 {
			size = 0
		}
		return publicAPIParsedBody{
			sizeBytes:      &size,
			rejectedReason: "request_body_parse_failed",
			statusCode:     http.StatusBadRequest,
			message:        "请求体无效",
		}
	}

	var extra any
	if err := decoder.Decode(&extra); err == nil || !errors.Is(err, io.EOF) {
		size := r.ContentLength
		if size < 0 {
			size = publicapilog.EstimatePayloadSizeBytes(body)
		}
		return publicAPIParsedBody{
			sizeBytes:      &size,
			rejectedReason: "request_body_parse_failed",
			statusCode:     http.StatusBadRequest,
			message:        "请求体无效",
		}
	}

	size := r.ContentLength
	if size < 0 {
		size = publicapilog.EstimatePayloadSizeBytes(body)
	}
	return publicAPIParsedBody{body: body, sizeBytes: &size}
}

func publicAPIRequestIsJSON(r *http.Request) bool {
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func publicAPIRequestMayHaveBody(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	if r.ContentLength == 0 && len(r.TransferEncoding) == 0 {
		return false
	}
	return true
}

func publicAPILogSourceContext(authContext *publicapiauth.AuthContext) *publicapilog.SourceContext {
	if authContext == nil {
		return nil
	}
	return &publicapilog.SourceContext{
		SourceRefID: authContext.SourceRefID,
		SourceName:  authContext.SourceName,
		TokenID:     authContext.TokenID,
		TokenName:   authContext.TokenName,
		TokenPrefix: authContext.TokenPrefix,
		IsTestToken: authContext.IsTestToken,
	}
}

func publicAPIQueryMap(values map[string][]string) map[string]any {
	out := make(map[string]any, len(values))
	for key, items := range values {
		switch len(items) {
		case 0:
			out[key] = ""
		case 1:
			out[key] = items[0]
		default:
			copied := make([]string, len(items))
			copy(copied, items)
			out[key] = copied
		}
	}
	return out
}

func clonePublicAPIHandlers(handlers map[string]http.Handler) map[string]http.Handler {
	if len(handlers) == 0 {
		return nil
	}
	out := make(map[string]http.Handler, len(handlers))
	for key, handler := range handlers {
		out[key] = handler
	}
	return out
}

type publicAPIResponseCapture struct {
	writer     http.ResponseWriter
	statusCode int
	sizeBytes  int64
	body       bytes.Buffer
	writeErr   error
}

func newPublicAPIResponseCapture(w http.ResponseWriter) *publicAPIResponseCapture {
	capture := &publicAPIResponseCapture{}
	capture.writer = httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: capture.wrapWriteHeader,
		Write:       capture.wrapWrite,
		Flush:       capture.wrapFlush,
		ReadFrom:    capture.wrapReadFrom,
	})
	return capture
}

func (w *publicAPIResponseCapture) ResponseWriter() http.ResponseWriter {
	return w.writer
}

func (w *publicAPIResponseCapture) WriteHeader(statusCode int) {
	w.writer.WriteHeader(statusCode)
}

func (w *publicAPIResponseCapture) Write(data []byte) (int, error) {
	return w.writer.Write(data)
}

func (w *publicAPIResponseCapture) Header() http.Header {
	return w.writer.Header()
}

func (w *publicAPIResponseCapture) wrapWriteHeader(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
	return func(statusCode int) {
		if statusCode >= 100 && statusCode <= 199 {
			next(statusCode)
			return
		}
		if w.statusCode != 0 {
			return
		}
		w.statusCode = statusCode
		next(statusCode)
	}
}

func (w *publicAPIResponseCapture) wrapWrite(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
	return func(data []byte) (int, error) {
		if w.statusCode == 0 {
			w.statusCode = http.StatusOK
		}
		n, err := next(data)
		w.sizeBytes += int64(n)
		if n > 0 {
			w.appendBody(data[:n])
		}
		if err != nil && w.writeErr == nil {
			w.writeErr = err
		}
		return n, err
	}
}

func (w *publicAPIResponseCapture) wrapFlush(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
	return func() {
		if w.statusCode == 0 {
			w.statusCode = http.StatusOK
		}
		next()
	}
}

func (w *publicAPIResponseCapture) wrapReadFrom(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
	return func(src io.Reader) (int64, error) {
		if w.statusCode == 0 {
			w.statusCode = http.StatusOK
		}
		n, err := next(io.TeeReader(src, publicAPIResponseBodyWriter{capture: w}))
		w.sizeBytes += n
		if err != nil && w.writeErr == nil {
			w.writeErr = err
		}
		return n, err
	}
}

func (w *publicAPIResponseCapture) appendBody(data []byte) {
	remaining := publicapilog.SnapshotMaxBytes + 1 - w.body.Len()
	if remaining > 0 {
		w.body.Write(data[:min(len(data), remaining)])
	}
}

func (w *publicAPIResponseCapture) StatusCode() int {
	if w.statusCode == 0 {
		return http.StatusOK
	}
	return w.statusCode
}

func (w *publicAPIResponseCapture) WroteHeader() bool {
	return w.statusCode != 0
}

func (w *publicAPIResponseCapture) SizeBytes() int64 {
	return w.sizeBytes
}

func (w *publicAPIResponseCapture) WriteError() error {
	return w.writeErr
}

func (w *publicAPIResponseCapture) Payload() any {
	if w.body.Len() == 0 {
		return nil
	}
	data := w.body.Bytes()
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	if strings.Contains(contentType, "json") {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var payload any
		if err := decoder.Decode(&payload); err == nil {
			return payload
		}
	}
	return string(data)
}

var _ http.ResponseWriter = (*publicAPIResponseCapture)(nil)

type publicAPIResponseBodyWriter struct {
	capture *publicAPIResponseCapture
}

func (w publicAPIResponseBodyWriter) Write(data []byte) (int, error) {
	w.capture.appendBody(data)
	return len(data), nil
}

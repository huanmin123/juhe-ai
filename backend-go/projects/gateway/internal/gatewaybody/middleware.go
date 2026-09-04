package gatewaybody

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Gateway body boundary middleware, mirroring request/body-middleware.ts
// plus the express.raw boundary wired in server.ts
// (wrapGatewayRawBodyParser / handleGatewayRawBodyError).

// Body rejection reasons (GatewayBodyRejectReason).
const (
	RejectReasonGatewayBodyParser         = "gateway_body_parser"
	RejectReasonGatewayBodySizeLimit      = "gateway_body_size_limit"
	RejectReasonGatewayBodyInFlightLimit  = "gateway_body_in_flight_limit"
	RejectReasonGatewayBodyMetadataWorker = "gateway_body_metadata_worker"
	RejectReasonGatewayBodyAdmission      = "gateway_body_admission"
)

// Failure attributions (UsageFailureAttribution subset used here).
const (
	FailureAttributionDownstreamClosed = "downstream_closed"
	FailureAttributionGatewayPolicy    = "gateway_policy"
)

// Logger is the logging seam. The composer adapts its structured logger;
// nil loggers are discarded.
type Logger interface {
	Debug(msg string, fields map[string]any)
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}

// DiscardLogger drops every record.
type DiscardLogger struct{}

func (DiscardLogger) Debug(string, map[string]any) {}
func (DiscardLogger) Info(string, map[string]any)  {}
func (DiscardLogger) Warn(string, map[string]any)  {}
func (DiscardLogger) Error(string, map[string]any) {}

// ErrorBody mirrors the gatewayErrorPayload error object with Node key order
// (message, type, code).
type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// ErrorPayload mirrors GatewayErrorPayload.
type ErrorPayload struct {
	Error ErrorBody `json:"error"`
}

// GatewayErrorPayload mirrors gatewayErrorPayload(message, type, code?).
func GatewayErrorPayload(message string, errorType string, code string) ErrorPayload {
	return ErrorPayload{Error: ErrorBody{Message: message, Type: errorType, Code: code}}
}

// RejectionInput mirrors GatewayBodyRejectionInput. The audit/usage writer
// (G17 seam) composes the Node audit message from these fields; nil recorder
// fields stay zero.
type RejectionInput struct {
	StatusCode         int
	ResponsePayload    ErrorPayload
	RawBodyBytes       int
	Reason             string
	ErrorCode          string
	ErrorMessage       string
	LimitBytes         int
	LimitScope         RawBodyLimitScope
	FailureAttribution string
}

// RejectionRecorder records body rejections for audit and usage storage.
// Node awaits recordGatewayFailure and the dropped audit capture; the Go
// composer adapts its async writers behind this interface.
type RejectionRecorder interface {
	RecordGatewayBodyRejection(r *http.Request, req *Request, input RejectionInput)
}

// Lane mirrors OpenAIGatewayRequestLane.
type Lane string

const (
	LaneText  Lane = "text"
	LaneImage Lane = "image"
)

// LaneResolver overrides the capture-time lane resolution. The default
// mirrors resolveOpenAIGatewayRequestLane evaluated at body capture time,
// where the body is never parsed, so the observable inputs are the request
// path, the state model hint and the state imageGeneration flag.
type LaneResolver func(r *http.Request, req *Request) Lane

// Config mirrors the runtime inputs of the Node body pipeline.
type Config struct {
	// BodyInFlightMaxBytes mirrors runtimeConfig.gateway.bodyInFlightMaxBytes
	// (env JUHE_AI_GATEWAY_BODY_IN_FLIGHT_MAX_MB, default 256 MiB).
	BodyInFlightMaxBytes int
	// TextRawBodyLimitMegabytes mirrors
	// req.gatewayRuntime.settings.gatewayTextRawBodyLimitMegabytes (G05
	// runtime snapshot; nil = unconfigured default 16 MiB).
	TextRawBodyLimitMegabytes TextRawBodyLimitProvider
	// LaneResolver overrides the default capture-time lane mirror.
	LaneResolver LaneResolver
	// Recorder records audit/usage rejections (nil = no-op until G17 wires).
	Recorder RejectionRecorder
	// Logger receives the pipeline events (nil = discard).
	Logger Logger
	// Parser is the shared bounded JSON pool (nil = new default pool).
	Parser *JSONParser
	// InFlight is the shared in-flight byte budget (nil = new limiter).
	InFlight *InFlightLimiter
}

// Middleware owns the body pipeline for the gateway chain.
type Middleware struct {
	cfg     Config
	limiter *InFlightLimiter
	parser  *JSONParser
	logger  Logger

	// rawBodyLimit overrides the express.raw limit in tests; production
	// always uses GatewayRawBodyHardLimitBytes.
	rawBodyLimit int
	// metadataScanTimeout overrides the worker job timeout used by the
	// deferred large-JSON scan in tests; production uses the 30s default.
	metadataScanTimeout time.Duration
}

// NewMiddleware assembles the pipeline. See Config for the runtime inputs.
func NewMiddleware(cfg Config) *Middleware {
	if cfg.Parser == nil {
		cfg.Parser = NewJSONParser(JSONParserOptions{Logger: cfg.Logger})
	}
	if cfg.InFlight == nil {
		cfg.InFlight = NewInFlightLimiter()
	}
	if cfg.Logger == nil {
		cfg.Logger = DiscardLogger{}
	}
	return &Middleware{
		cfg:                 cfg,
		limiter:             cfg.InFlight,
		parser:              cfg.Parser,
		logger:              cfg.Logger,
		rawBodyLimit:        GatewayRawBodyHardLimitBytes,
		metadataScanTimeout: DefaultJSONWorkerJobTimeout,
	}
}

// Parser exposes the shared bounded pool for other slices (codex adapters).
func (m *Middleware) Parser() *JSONParser { return m.parser }

// InFlight exposes the shared in-flight budget for diagnostics.
func (m *Middleware) InFlight() *InFlightLimiter { return m.limiter }

// RawBodyParserError mirrors GatewayRawBodyParserError: the express.raw /
// raw-body error surface (type, statusCode/status, received, length, limit).
type RawBodyParserError struct {
	Type       string
	StatusCode int
	Received   int
	Length     int
	Limit      int
	Message    string
}

// RawBodyParserErrorResponse mirrors GatewayRawBodyParserErrorResponse.
type RawBodyParserErrorResponse struct {
	StatusCode         int
	Message            string
	ErrorType          string
	FailureAttribution string
}

// ClassifyRawBodyParserError mirrors classifyGatewayRawBodyParserError with
// byte-identical Chinese copy.
func ClassifyRawBodyParserError(err *RawBodyParserError) RawBodyParserErrorResponse {
	if err == nil {
		return RawBodyParserErrorResponse{StatusCode: http.StatusBadRequest, Message: "网关请求体无效", ErrorType: "invalid_request_error", FailureAttribution: FailureAttributionGatewayPolicy}
	}
	parserType := strings.TrimSpace(err.Type)
	if parserType == "request.aborted" || parserType == "request.size.invalid" {
		return RawBodyParserErrorResponse{
			StatusCode:         http.StatusRequestTimeout,
			Message:            "请求体上传未完成，请重试",
			ErrorType:          "request_timeout",
			FailureAttribution: FailureAttributionDownstreamClosed,
		}
	}
	statusCode := http.StatusBadRequest
	if err.StatusCode != 0 {
		statusCode = err.StatusCode
	}
	if statusCode == http.StatusRequestEntityTooLarge {
		return RawBodyParserErrorResponse{
			StatusCode:         statusCode,
			Message:            "请求体过大",
			ErrorType:          "request_too_large",
			FailureAttribution: FailureAttributionGatewayPolicy,
		}
	}
	return RawBodyParserErrorResponse{
		StatusCode:         statusCode,
		Message:            "网关请求体无效",
		ErrorType:          "invalid_request_error",
		FailureAttribution: FailureAttributionGatewayPolicy,
	}
}

// ReadRawBody mirrors wrapGatewayRawBodyParser + express.raw({type: () =>
// true, limit: '64mb'}): the whole body is always buffered as raw bytes
// regardless of content type, and over-limit reads surface the
// entity.too.large parser error.
func (m *Middleware) ReadRawBody(w http.ResponseWriter, r *http.Request) ([]byte, *RawBodyParserError) {
	limit := int64(m.rawBodyLimit)
	if limit <= 0 {
		limit = GatewayRawBodyHardLimitBytes
	}
	var body io.ReadCloser = r.Body
	if body != nil {
		body = http.MaxBytesReader(w, body, limit)
	}
	raw, err := io.ReadAll(body)
	if err == nil {
		// A completed read is never a parser failure; a client that vanished
		// mid-request is handled by Capture's post-capture abort check.
		return raw, nil
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return raw, &RawBodyParserError{
			Type:       "entity.too.large",
			StatusCode: http.StatusRequestEntityTooLarge,
			Received:   len(raw),
			Length:     requestContentLength(r),
			Limit:      int(maxErr.Limit),
		}
	}
	if r.Context().Err() != nil {
		return raw, &RawBodyParserError{Type: "request.aborted", StatusCode: http.StatusBadRequest, Received: len(raw)}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return raw, &RawBodyParserError{
			Type:       "request.size.invalid",
			StatusCode: http.StatusBadRequest,
			Received:   len(raw),
			Length:     requestContentLength(r),
		}
	}
	return raw, &RawBodyParserError{StatusCode: http.StatusBadRequest, Received: len(raw), Message: err.Error()}
}

func requestContentLength(r *http.Request) int {
	if value, ok := requestContentLengthBytes(r); ok {
		return value
	}
	return 0
}

// HandleParserRejection mirrors server.ts handleGatewayRawBodyError:
// classify, record and answer the parser rejection. It returns false when
// there is no parser error (caller continues) or headers were already sent.
func (m *Middleware) HandleParserRejection(w http.ResponseWriter, r *http.Request, perr *RawBodyParserError) bool {
	if perr == nil {
		return false
	}
	failure := ClassifyRawBodyParserError(perr)
	statusCode := failure.StatusCode
	if statusCode < 400 || statusCode >= 600 {
		statusCode = http.StatusBadRequest
	}
	payload := GatewayErrorPayload(failure.Message, failure.ErrorType, "")
	rawBodyBytes := perr.Received
	if rawBodyBytes == 0 {
		rawBodyBytes = perr.Length
	}
	if rawBodyBytes == 0 {
		rawBodyBytes = perr.Limit
	}
	limitBytes := 0
	limitScope := RawBodyLimitScope("")
	if statusCode == http.StatusRequestEntityTooLarge {
		limitBytes = perr.Limit
		if limitBytes == 0 {
			limitBytes = GatewayRawBodyHardLimitBytes
		}
		limitScope = RawBodyLimitScopeGateway
	}
	m.recordRejection(r, nil, RejectionInput{
		StatusCode:         statusCode,
		ResponsePayload:    payload,
		RawBodyBytes:       rawBodyBytes,
		Reason:             RejectReasonGatewayBodyParser,
		ErrorCode:          perr.Type,
		ErrorMessage:       failure.Message,
		LimitBytes:         limitBytes,
		LimitScope:         limitScope,
		FailureAttribution: failure.FailureAttribution,
	})
	m.logger.Warn("网关原始请求体被拒绝", map[string]any{
		"event":         "gateway_raw_body_rejected",
		"method":        r.Method,
		"path":          r.URL.Path,
		"originalUrl":   sanitizeURLForLog(r.URL.RequestURI()),
		"statusCode":    statusCode,
		"errorType":     failure.ErrorType,
		"receivedBytes": perr.Received,
		"bodyLength":    perr.Length,
		"bodyLimit":     perr.Limit,
	})
	writeErrorJSON(w, statusCode, payload)
	return true
}

// Capture mirrors captureGatewayRawBody. The raw body must have been read
// with ReadRawBody first. When the request is rejected (response written) or
// the client already went away, Capture returns (nil, nil) and the composer
// stops the chain; unexpected internal failures return (nil, err), the
// equivalent of Express next(error).
func (m *Middleware) Capture(w http.ResponseWriter, r *http.Request, rawBody []byte) (*Request, error) {
	contentType := r.Header.Get("Content-Type")
	isJSON := IsJSONContentType(contentType)
	requestPath := requestPathOf(r)
	requestCtx := r.Context()

	// 1) gateway hard limit (gatewayRawBodyHardLimitBytes).
	if len(rawBody) > m.rawBodyLimit {
		req := &Request{
			State:             deferredLargeBodyState(len(rawBody), contentType, isJSON),
			ContentTypeHeader: contentType,
			ctx:               requestCtx,
		}
		m.rejectRawBodyTooLarge(w, r, req, len(rawBody), int64(m.rawBodyLimit), RawBodyLimitScopeGateway)
		return nil, nil
	}

	// 2) in-flight byte admission.
	lease, ok := m.limiter.TryAcquire(len(rawBody), m.cfg.BodyInFlightMaxBytes)
	if !ok {
		req := &Request{
			State:             deferredLargeBodyState(len(rawBody), contentType, isJSON),
			ContentTypeHeader: contentType,
			ctx:               requestCtx,
		}
		m.rejectRawBodyInFlightLimit(w, r, req, len(rawBody))
		return nil, nil
	}
	req := &Request{
		RawBody:           rawBody,
		ContentTypeHeader: contentType,
		Lease:             lease,
		ctx:               requestCtx,
	}

	// 3) body classification.
	if len(rawBody) == 0 {
		req.State = CreateBodyState(BodyStateInput{
			RawBody:         rawBody,
			ContentType:     contentType,
			JSONParseStatus: JSONParseStatusEmpty,
		})
		req.Body = nil
	} else if !isJSON {
		model, _ := ExtractMultipartImageModel(rawBody, contentType, requestPath)
		responseFormat, _ := ExtractMultipartAudioResponseFormat(rawBody, contentType, requestPath)
		input := BodyStateInput{
			RawBody:         rawBody,
			ContentType:     contentType,
			JSONParseStatus: JSONParseStatusNotJSON,
		}
		if model != "" {
			input.Model = &model
		}
		if responseFormat != "" {
			input.ResponseFormat = &responseFormat
		}
		req.State = CreateBodyState(input)
		req.Body = nil
		if m.rejectByRequestLane(w, r, req, rawBody) {
			return nil, nil
		}
	} else if len(rawBody) > GatewayJSONBodyInlineMetadataScanMaxBytes {
		metadata, perr := m.parser.ExtractJSONBodyMetadataAsync(requestCtx, rawBody, m.metadataScanTimeout)
		if perr != nil {
			if requestCtx.Err() != nil {
				// Node: aborted request — silent cleanup, no response.
				req.RawBody = nil
				req.Body = nil
				req.ReleaseInFlight()
				return nil, nil
			}
			if IsQueueFullError(perr) {
				m.rejectMetadataWorkerBusy(w, r, req, len(rawBody))
				return nil, nil
			}
			m.rejectMetadataWorkerFailed(w, r, req, len(rawBody))
			return nil, nil
		}
		logFields := map[string]any{
			"event":                          "gateway_large_json_body_deferred",
			"method":                         r.Method,
			"path":                           r.URL.Path,
			"originalUrl":                    sanitizeURLForLog(r.URL.RequestURI()),
			"rawBodyBytes":                   len(rawBody),
			"jsonInlineMetadataScanMaxBytes": GatewayJSONBodyInlineMetadataScanMaxBytes,
			"jsonParseWarningBytes":          GatewayJSONBodyLargeWarningBytes,
			"model":                          metadataString(metadata.Model),
			"stream":                         metadataBool(metadata.Stream),
			"serviceTier":                    metadataString(metadata.ServiceTier),
			"reasoningEffort":                metadataString(metadata.ReasoningEffort),
			"maxOutputTokens":                metadataInt(metadata.MaxOutputTokens),
			"imageGeneration":                metadata.ImageGeneration,
			"imageGenerationForced":          metadata.ImageGenerationForced,
			"invalidJson":                    metadata.InvalidJSON,
		}
		if len(rawBody) > GatewayJSONBodyLargeWarningBytes {
			m.logger.Warn("网关大 JSON 请求体已完成顶层元数据扫描，完整解析延迟到账号适配或请求改写阶段", logFields)
		} else {
			m.logger.Debug("网关 JSON 请求体超过主进程内联解析阈值，已转入 worker 元数据扫描", logFields)
		}
		req.State = CreateBodyState(metadataStateInput(rawBody, contentType, metadata))
		req.Body = nil
		if m.rejectByRequestLane(w, r, req, rawBody) {
			return nil, nil
		}
	} else {
		metadata := ExtractJSONBodyMetadata(rawBody)
		req.State = CreateBodyState(metadataStateInput(rawBody, contentType, metadata))
		req.Body = nil
		if m.rejectByRequestLane(w, r, req, rawBody) {
			return nil, nil
		}
	}

	// 4) post-capture abort check.
	if requestCtx.Err() != nil {
		req.RawBody = nil
		req.Body = nil
		req.ReleaseInFlight()
		return nil, nil
	}
	return req, nil
}

func deferredLargeBodyState(rawBodyBytes int, contentType string, isJSON bool) *BodyState {
	status := JSONParseStatusNotJSON
	if isJSON {
		status = JSONParseStatusDeferredLargeJSON
	}
	return &BodyState{
		RawBodyBytes:          rawBodyBytes,
		ContentType:           contentType,
		IsJSON:                isJSON,
		JSONParseStatus:       status,
		JSONParseWarningBytes: GatewayJSONBodyLargeWarningBytes,
	}
}

func metadataStateInput(rawBody []byte, contentType string, metadata JSONBodyMetadata) BodyStateInput {
	input := BodyStateInput{
		RawBody:         rawBody,
		ContentType:     contentType,
		Model:           metadata.Model,
		Stream:          metadata.Stream,
		ServiceTier:     metadata.ServiceTier,
		ReasoningEffort: metadata.ReasoningEffort,
		MaxOutputTokens: metadata.MaxOutputTokens,
	}
	if metadata.InvalidJSON {
		input.JSONParseStatus = JSONParseStatusInvalidJSON
		return input
	}
	input.JSONParseStatus = JSONParseStatusScannedJSON
	input.ImageGeneration = &metadata.ImageGeneration
	input.ImageGenerationForced = &metadata.ImageGenerationForced
	input.StrictOutputRequirement = &metadata.StrictOutputRequirement
	input.CodexCompactionTrigger = &metadata.CodexCompactionTrigger
	return input
}

func metadataString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func metadataBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func metadataInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// RejectByContentLength mirrors rejectGatewayRawBodyByContentLength: before
// reading the body, reject requests whose declared Content-Length exceeds
// the per-request-type limit. Returns true when the request was answered.
func (m *Middleware) RejectByContentLength(w http.ResponseWriter, r *http.Request) bool {
	contentLength, ok := requestContentLengthBytes(r)
	if !ok {
		return false
	}
	limit, scope, applicable := m.resolveContentLengthLimit(r)
	if !applicable {
		return false
	}
	if contentLength <= limit {
		return false
	}
	m.logger.Warn("网关请求体 Content-Length 超过当前请求类型上限，已在读取 body 前拒绝", map[string]any{
		"event":             "gateway_raw_body_content_length_limit_rejected",
		"method":            r.Method,
		"path":              r.URL.Path,
		"originalUrl":       sanitizeURLForLog(r.URL.RequestURI()),
		"rawBodyBytes":      contentLength,
		"rawBodyLimitBytes": limit,
		"rawBodyLimitScope": string(scope),
	})
	m.recordRejection(r, nil, RejectionInput{
		StatusCode:      http.StatusRequestEntityTooLarge,
		ResponsePayload: GatewayErrorPayload("请求体过大", "request_too_large", ""),
		RawBodyBytes:    contentLength,
		Reason:          RejectReasonGatewayBodySizeLimit,
		ErrorCode:       "request_too_large",
		ErrorMessage:    "请求体过大",
		LimitBytes:      limit,
		LimitScope:      scope,
	})
	writeErrorJSON(w, http.StatusRequestEntityTooLarge, GatewayErrorPayload("请求体过大", "request_too_large", ""))
	return true
}

func (m *Middleware) resolveContentLengthLimit(r *http.Request) (int, RawBodyLimitScope, bool) {
	path := strings.ToLower(EndpointPathOf(requestPathOf(r)))
	if isImageEndpointPath(path) {
		return GatewayImageRawBodyHardLimitBytes, RawBodyLimitScopeImage, true
	}
	if isGatewayTextJSONBodyPath(path) {
		megabytes, configured := m.textRawBodyLimitMegabytes()
		return GatewayTextRawBodyLimitBytes(megabytes, configured), RawBodyLimitScopeText, true
	}
	return 0, "", false
}

func isGatewayTextJSONBodyPath(path string) bool {
	return path == "/chat/completions" ||
		path == "/v1/chat/completions" ||
		path == "/messages" ||
		strings.HasPrefix(path, "/messages/") ||
		path == "/v1/messages" ||
		strings.HasPrefix(path, "/v1/messages/") ||
		path == "/embeddings" ||
		path == "/v1/embeddings"
}

func (m *Middleware) textRawBodyLimitMegabytes() (int, bool) {
	if m.cfg.TextRawBodyLimitMegabytes == nil {
		return 0, false
	}
	return m.cfg.TextRawBodyLimitMegabytes()
}

// requestContentLengthBytes mirrors requestContentLengthBytes: a present,
// safe non-negative integer header value.
func requestContentLengthBytes(r *http.Request) (int, bool) {
	text := r.Header.Get("Content-Length")
	if trimJSSpace(text) == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(trimJSSpace(text), 64)
	if err != nil {
		return 0, false
	}
	if parsed < 0 || parsed > maxSafeInteger || parsed != float64(int64(parsed)) {
		return 0, false
	}
	return int(parsed), true
}

func (m *Middleware) rejectByRequestLane(w http.ResponseWriter, r *http.Request, req *Request, rawBody []byte) bool {
	limitBytes, scope := m.resolveRequestLimit(r, req)
	if len(rawBody) <= limitBytes {
		return false
	}
	m.rejectRawBodyTooLarge(w, r, req, len(rawBody), int64(limitBytes), scope)
	return true
}

func (m *Middleware) resolveRequestLimit(r *http.Request, req *Request) (int, RawBodyLimitScope) {
	if m.resolveLane(r, req) == LaneImage {
		return GatewayImageRawBodyHardLimitBytes, RawBodyLimitScopeImage
	}
	megabytes, configured := m.textRawBodyLimitMegabytes()
	return GatewayTextRawBodyLimitBytes(megabytes, configured), RawBodyLimitScopeText
}

// resolveLane mirrors resolveOpenAIGatewayRequestLane at body capture time.
func (m *Middleware) resolveLane(r *http.Request, req *Request) Lane {
	if m.cfg.LaneResolver != nil {
		return m.cfg.LaneResolver(r, req)
	}
	path := strings.ToLower(EndpointPathOf(requestPathOf(r)))
	if isImageEndpointPath(path) {
		return LaneImage
	}
	var model string
	if req.State != nil && req.State.Model != nil {
		model = *req.State.Model
	}
	if isImageGenerationModel(model) {
		return LaneImage
	}
	if req.State != nil && req.State.ImageGeneration {
		return LaneImage
	}
	return LaneText
}

var geminiImageModelPattern = regexp.MustCompile(`(?:^|-)gemini(?:[^/]*-)?image(?:-|$)`)

// isImageGenerationModel mirrors isOpenAIGatewayImageGenerationModel.
func isImageGenerationModel(model string) bool {
	normalized := strings.ToLower(trimJSSpace(model))
	if normalized == "" {
		return false
	}
	return strings.HasPrefix(normalized, "gpt-image") ||
		strings.HasPrefix(normalized, "dall-e") ||
		strings.HasPrefix(normalized, "imagen-") ||
		strings.HasPrefix(normalized, "nano-banana") ||
		geminiImageModelPattern.MatchString(normalized)
}

func (m *Middleware) rejectRawBodyTooLarge(w http.ResponseWriter, r *http.Request, req *Request, rawBodyBytes int, limitBytes int64, scope RawBodyLimitScope) {
	event := "gateway_raw_body_request_limit_rejected"
	message := "网关请求体超过当前请求类型上限，已拒绝以保护主进程"
	if scope == RawBodyLimitScopeGateway {
		event = "gateway_raw_body_hard_limit_rejected"
		message = "网关请求体超过硬上限，已拒绝以保护主进程"
	}
	m.logger.Warn(message, map[string]any{
		"event":                             event,
		"method":                            r.Method,
		"path":                              r.URL.Path,
		"originalUrl":                       sanitizeURLForLog(r.URL.RequestURI()),
		"rawBodyBytes":                      rawBodyBytes,
		"rawBodyLimitBytes":                 limitBytes,
		"rawBodyLimitScope":                 string(scope),
		"rawBodyHardLimitBytes":             GatewayRawBodyHardLimitBytes,
		"gatewayTextRawBodyHardLimitBytes":  GatewayTextRawBodyHardLimitBytes,
		"gatewayImageRawBodyHardLimitBytes": GatewayImageRawBodyHardLimitBytes,
	})
	req.RawBody = nil
	req.Body = nil
	req.ReleaseInFlight()
	m.recordRejection(r, req, RejectionInput{
		StatusCode:      http.StatusRequestEntityTooLarge,
		ResponsePayload: GatewayErrorPayload("请求体过大", "request_too_large", ""),
		RawBodyBytes:    rawBodyBytes,
		Reason:          RejectReasonGatewayBodySizeLimit,
		ErrorCode:       "request_too_large",
		ErrorMessage:    "请求体过大",
		LimitBytes:      int(limitBytes),
		LimitScope:      scope,
	})
	writeErrorJSON(w, http.StatusRequestEntityTooLarge, GatewayErrorPayload("请求体过大", "request_too_large", ""))
}

func (m *Middleware) rejectRawBodyInFlightLimit(w http.ResponseWriter, r *http.Request, req *Request, rawBodyBytes int) {
	state := m.limiter.State(m.cfg.BodyInFlightMaxBytes)
	m.logger.Warn("网关请求体在途总量超过上限，已拒绝以保护主进程", map[string]any{
		"event":                     "gateway_raw_body_in_flight_limit_rejected",
		"method":                    r.Method,
		"path":                      r.URL.Path,
		"originalUrl":               sanitizeURLForLog(r.URL.RequestURI()),
		"rawBodyBytes":              rawBodyBytes,
		"bodyInFlightBytes":         state.CurrentBytes,
		"bodyInFlightRequestCount":  state.RequestCount,
		"bodyInFlightMaxBytes":      state.MaxBytes,
		"bodyInFlightRejectedCount": state.RejectedCount,
	})
	req.RawBody = nil
	req.Body = nil
	payload := GatewayErrorPayload("网关请求体在途总量过高，请稍后重试", "server_overloaded", "gateway_body_in_flight_limit_exceeded")
	m.recordRejection(r, req, RejectionInput{
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: payload,
		RawBodyBytes:    rawBodyBytes,
		Reason:          RejectReasonGatewayBodyInFlightLimit,
		ErrorCode:       "gateway_body_in_flight_limit_exceeded",
		ErrorMessage:    "网关请求体在途总量过高，请稍后重试",
	})
	w.Header().Set("Retry-After", "1")
	writeErrorJSON(w, http.StatusServiceUnavailable, payload)
}

func (m *Middleware) rejectMetadataWorkerBusy(w http.ResponseWriter, r *http.Request, req *Request, rawBodyBytes int) {
	m.logger.Warn("网关大 JSON 请求体元数据 worker 队列已满，拒绝本次请求以保护主进程", map[string]any{
		"event":        "gateway_large_json_body_metadata_worker_queue_full",
		"method":       r.Method,
		"path":         r.URL.Path,
		"originalUrl":  sanitizeURLForLog(r.URL.RequestURI()),
		"rawBodyBytes": rawBodyBytes,
	})
	payload := GatewayErrorPayload("网关请求解析繁忙，请稍后重试", "server_overloaded", "gateway_json_parser_busy")
	m.recordRejection(r, req, RejectionInput{
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: payload,
		RawBodyBytes:    rawBodyBytes,
		Reason:          RejectReasonGatewayBodyMetadataWorker,
		ErrorCode:       "gateway_json_parser_busy",
		ErrorMessage:    "网关请求解析繁忙，请稍后重试",
	})
	writeErrorJSON(w, http.StatusServiceUnavailable, payload)
}

func (m *Middleware) rejectMetadataWorkerFailed(w http.ResponseWriter, r *http.Request, req *Request, rawBodyBytes int) {
	m.logger.Warn("网关大 JSON 请求体元数据 worker 扫描失败，拒绝本次请求以保护主进程", map[string]any{
		"event":        "gateway_large_json_body_metadata_worker_failed",
		"method":       r.Method,
		"path":         r.URL.Path,
		"originalUrl":  sanitizeURLForLog(r.URL.RequestURI()),
		"rawBodyBytes": rawBodyBytes,
	})
	payload := GatewayErrorPayload("网关请求解析暂时不可用，请稍后重试", "server_overloaded", "gateway_json_parser_failed")
	m.recordRejection(r, req, RejectionInput{
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: payload,
		RawBodyBytes:    rawBodyBytes,
		Reason:          RejectReasonGatewayBodyMetadataWorker,
		ErrorCode:       "gateway_json_parser_failed",
		ErrorMessage:    "网关请求解析暂时不可用，请稍后重试",
	})
	writeErrorJSON(w, http.StatusServiceUnavailable, payload)
}

func (m *Middleware) recordRejection(r *http.Request, req *Request, input RejectionInput) {
	if m.cfg.Recorder == nil {
		return
	}
	m.cfg.Recorder.RecordGatewayBodyRejection(r, req, input)
}

// writeErrorJSON mirrors res.status(...).json({...}): plain JSON, no
// localization pass (the Node gateway writes its error envelopes directly).
func writeErrorJSON(w http.ResponseWriter, status int, payload ErrorPayload) {
	body, err := json.Marshal(payload)
	if err != nil {
		// ErrorPayload is a plain struct; marshaling cannot fail.
		body = []byte(`{"error":{"message":"网关请求体无效","type":"invalid_request_error"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// requestPathOf mirrors String(req.path || req.originalUrl || ”).
func requestPathOf(r *http.Request) string {
	if r.URL.Path != "" {
		return r.URL.Path
	}
	return r.URL.RequestURI()
}

// sanitizeURLForLog strips the query string. The Node sanitizeUrlForLog
// masks credential-bearing query parameters; gateway endpoints carry no
// credentials in queries, so query stripping keeps the parity that matters
// here. A composer may wrap Logger for the full sanitizer.
func sanitizeURLForLog(rawURL string) string {
	return EndpointPathOf(rawURL)
}

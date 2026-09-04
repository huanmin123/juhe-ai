package gatewaypreauth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Port of request/pre-auth.ts. Error copy, status codes and the guard order
// mirror the Node source byte for byte; the deep runtime guards (circuits,
// client-ip policy, user request limits) arrive through the ports.

// ResolveGatewayRuntimeOptions mirrors ResolveGatewayRuntimeOptions.
type ResolveGatewayRuntimeOptions struct {
	// CloseConnectionOnAuthFailure mirrors the option of the same name.
	CloseConnectionOnAuthFailure bool
	// InspectClientIPPolicyAfterRuntime mirrors the option; nil keeps the
	// Node default (inspect).
	InspectClientIPPolicyAfterRuntime *bool
}

func (o ResolveGatewayRuntimeOptions) inspectClientIPPolicyAfterRuntime() bool {
	return o.InspectClientIPPolicyAfterRuntime == nil || *o.InspectClientIPPolicyAfterRuntime
}

// PreResolveGatewayRuntime mirrors preResolveGatewayRuntime: the middleware
// stage before the body pipeline. next mirrors the express next callback;
// returning an error mirrors next(error) (unexpected failure).
func (s *Service) PreResolveGatewayRuntime(ctx context.Context, res GatewayResponseWriter, req *GatewayRequest, next func()) error {
	stageStartedAt := s.StartedAt()
	resolutionReason := ""
	resolutionOutcome := "success"
	var resolutionError error
	defer func() {
		fields := map[string]any{
			"traceId":  s.Observability.TraceID(),
			"resolved": req != nil && req.Runtime != nil && req.Runtime.APIKey != nil,
			"reason":   resolutionReason,
		}
		switch resolutionOutcome {
		case "unexpected_failure":
			fields["error"] = resolutionError
		case "expected_failure":
			fields["failureReason"] = resolutionReason
		}
		s.Observability.LogRequestStage("runtime_resolution", fields, resolutionOutcome, stageStartedAt)
	}()
	if IsGatewayModelsRequest(req) {
		resolutionReason = "models_endpoint"
		next()
		return nil
	}
	runtime, err := s.ResolveGatewayRuntimeAsync(ctx, res, req, ResolveGatewayRuntimeOptions{
		CloseConnectionOnAuthFailure:      true,
		InspectClientIPPolicyAfterRuntime: boolPtr(false),
	})
	if err != nil {
		resolutionOutcome = "unexpected_failure"
		resolutionReason = errorName(err)
		resolutionError = err
		return err
	}
	if runtime == nil || runtime.APIKey == nil {
		resolutionOutcome = "expected_failure"
		resolutionReason = "runtime_unresolved"
		s.recordEarlyGatewayAuthFailure(res, req)
		return nil
	}
	if IsImageGenerationDisabledForAPIKey(runtime.APIKey, ResolveOpenAIGatewayRequestLane(req)) {
		resolutionOutcome = "expected_failure"
		resolutionReason = "image_generation_disabled"
		s.sendEarlyImageGenerationDisabledResponse(ctx, res, req)
		return nil
	}
	clientIP, _ := ExtractClientIP(req)
	if s.rejectCachedClientIPBlacklist(ctx, res, req, clientIP, ResolveGatewayRuntimeOptions{CloseConnectionOnAuthFailure: true}, true) {
		resolutionOutcome = "expected_failure"
		resolutionReason = "client_ip_blacklisted"
		return nil
	}
	decision := s.UserLimits.Consume(UserRequestLimitConsumeInput{
		SystemAccountID: runtime.APIKey.SystemAccountID,
		Settings:        runtime.Settings,
		Overrides:       runtime.APIKey.SystemAccountRequestLimits,
	})
	if !decision.Allowed {
		resolutionOutcome = "expected_failure"
		resolutionReason = "user_request_limit_exceeded"
		s.sendUserRequestLimitExceededResponse(ctx, res, req, decision)
		return nil
	}
	req.Runtime = runtime
	next()
	return nil
}

// ResolveGatewayRuntimeAsync mirrors resolveGatewayRuntimeAsync. A nil
// runtime result means the response has been written (auth rejection or
// circuit short circuit); a non-nil error mirrors a thrown failure.
func (s *Service) ResolveGatewayRuntimeAsync(ctx context.Context, res GatewayResponseWriter, req *GatewayRequest, options ResolveGatewayRuntimeOptions) (*gatewayruntimecache.GatewayRuntime, error) {
	if req.Runtime != nil && req.Runtime.APIKey != nil {
		return req.Runtime, nil
	}
	clientIP, _ := ExtractClientIP(req)
	authorization := req.Header("authorization")
	gatewayAuthSource := s.GatewayPreAuthSource(req, authorization)
	if s.rejectCachedClientIPBlacklist(ctx, res, req, clientIP, options, true) {
		return nil, nil
	}
	preAuthDecision, err := s.Circuits.InspectPreAuthCircuit(ctx, PreAuthCircuitInput{
		ClientIP:      clientIP,
		Authorization: gatewayAuthSource,
	})
	if err != nil {
		return nil, err
	}
	if preAuthDecision.Blocked {
		s.Observability.Logger().Warn("gateway_pre_auth_error_circuit_blocked", map[string]any{
			"reason":            preAuthDecision.Reason,
			"retryAfterSeconds": preAuthDecision.RetryAfterSeconds,
			"failureCount":      preAuthDecision.FailureCount,
			"endpoint":          s.requestEndpointForLog(req),
		}, "网关认证前来源保护已短路请求")
		s.prepareEarlyAuthFailureResponse(res, options)
		s.sendPreAuthCircuitResponse(req, res, preAuthDecision)
		return nil, nil
	}
	gatewayAPIKey, ok := s.ExtractGatewayAPIKey(req, authorization)
	if !ok {
		failureDecision, err := s.recordPreAuthFailure(ctx, req, res, PreAuthFailureMissingBearerToken, options)
		if err != nil {
			return nil, err
		}
		if failureDecision.Blocked {
			return nil, nil
		}
		s.Observability.Logger().Warn("gateway_auth_failed", map[string]any{
			"reason":   string(PreAuthFailureMissingBearerToken),
			"endpoint": s.requestEndpointForLog(req),
		}, "网关认证失败")
		s.prepareEarlyAuthFailureResponse(res, options)
		SendGatewayJSONError(res, http.StatusUnauthorized, GatewayErrorPayloadOf("缺少访问令牌", "invalid_request_error"), SendGatewayErrorOptions{
			Protocol: s.clientErrorProtocolOrOpenAI(req),
		})
		return nil, nil
	}

	runtime, err := s.RuntimeCache.ReadCachedGatewayRuntimeAsync(ctx, gatewayAPIKey)
	if err != nil {
		return nil, err
	}
	if runtime.APIKey == nil {
		failureDecision, err := s.recordPreAuthFailure(ctx, req, res, PreAuthFailureInvalidAPIKey, options)
		if err != nil {
			return nil, err
		}
		if failureDecision.Blocked {
			return nil, nil
		}
		s.Observability.Logger().Warn("gateway_auth_failed", map[string]any{
			"reason":   string(PreAuthFailureInvalidAPIKey),
			"endpoint": s.requestEndpointForLog(req),
		}, "网关认证失败")
		s.prepareEarlyAuthFailureResponse(res, options)
		SendGatewayJSONError(res, http.StatusUnauthorized, GatewayErrorPayloadOf("API Key 无效", "invalid_request_error"), SendGatewayErrorOptions{
			Protocol: s.clientErrorProtocolOrOpenAI(req),
		})
		return nil, nil
	}
	if options.inspectClientIPPolicyAfterRuntime() &&
		s.rejectCachedClientIPBlacklist(ctx, res, req, clientIP, options, true) {
		return nil, nil
	}
	return &runtime, nil
}

// ResolveGatewayAPIKeyForModelsAsync mirrors resolveGatewayApiKeyForModelsAsync.
func (s *Service) ResolveGatewayAPIKeyForModelsAsync(ctx context.Context, res GatewayResponseWriter, req *GatewayRequest, options ResolveGatewayRuntimeOptions) (*gatewayruntimecache.GatewayAPIKeyRow, error) {
	clientIP, _ := ExtractClientIP(req)
	authorization := req.Header("authorization")
	gatewayAuthSource := s.GatewayPreAuthSource(req, authorization)
	if s.rejectCachedClientIPBlacklist(ctx, res, req, clientIP, options, true) {
		return nil, nil
	}
	preAuthDecision, err := s.Circuits.InspectPreAuthCircuit(ctx, PreAuthCircuitInput{
		ClientIP:      clientIP,
		Authorization: gatewayAuthSource,
	})
	if err != nil {
		return nil, err
	}
	if preAuthDecision.Blocked {
		s.prepareEarlyAuthFailureResponse(res, options)
		s.sendPreAuthCircuitResponse(req, res, preAuthDecision)
		return nil, nil
	}
	gatewayAPIKey, ok := s.ExtractGatewayAPIKey(req, authorization)
	if !ok {
		if err := s.rejectMissingOrInvalidGatewayCredential(ctx, req, res, PreAuthFailureMissingBearerToken, options); err != nil {
			return nil, err
		}
		return nil, nil
	}
	apiKey, err := s.APIKeyValidator.Validate(ctx, gatewayAPIKey)
	if err != nil {
		return nil, err
	}
	if apiKey == nil {
		if err := s.rejectMissingOrInvalidGatewayCredential(ctx, req, res, PreAuthFailureInvalidAPIKey, options); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if options.inspectClientIPPolicyAfterRuntime() &&
		s.rejectCachedClientIPBlacklist(ctx, res, req, clientIP, options, true) {
		return nil, nil
	}
	return apiKey, nil
}

// rejectMissingOrInvalidGatewayCredential mirrors the private helper.
func (s *Service) rejectMissingOrInvalidGatewayCredential(ctx context.Context, req *GatewayRequest, res GatewayResponseWriter, reason PreAuthFailureReason, options ResolveGatewayRuntimeOptions) error {
	failureDecision, err := s.recordPreAuthFailure(ctx, req, res, reason, options)
	if err != nil {
		return err
	}
	if failureDecision.Blocked {
		return nil
	}
	s.Observability.Logger().Warn("gateway_auth_failed", map[string]any{
		"reason":   string(reason),
		"endpoint": s.requestEndpointForLog(req),
	}, "网关认证失败")
	s.prepareEarlyAuthFailureResponse(res, options)
	message := "缺少访问令牌"
	if reason == PreAuthFailureInvalidAPIKey {
		message = "API Key 无效"
	}
	SendGatewayJSONError(res, http.StatusUnauthorized, GatewayErrorPayloadOf(message, "invalid_request_error"), SendGatewayErrorOptions{
		Protocol: s.clientErrorProtocolOrOpenAI(req),
	})
	return nil
}

// IsOpenAIStreamRequest mirrors isOpenAIStreamRequest.
func IsOpenAIStreamRequest(req *GatewayRequest) bool {
	return RequestStream(req)
}

// prepareEarlyAuthFailureResponse mirrors the private helper: flag the
// connection close before the header goes out.
func (s *Service) prepareEarlyAuthFailureResponse(res GatewayResponseWriter, options ResolveGatewayRuntimeOptions) {
	if options.CloseConnectionOnAuthFailure && !res.HeadersSent() {
		res.Header().Set("Connection", "close")
	}
}

// rejectCachedClientIPBlacklist mirrors the private helper; cacheOnly is
// always true at the call sites.
func (s *Service) rejectCachedClientIPBlacklist(ctx context.Context, res GatewayResponseWriter, req *GatewayRequest, clientIP string, options ResolveGatewayRuntimeOptions, cacheOnly bool) bool {
	ipPolicyDecision, err := s.IPPolicy.InspectClientIPPolicy(ctx, clientIP, cacheOnly)
	if err != nil || !ipPolicyDecision.Blocked || ipPolicyDecision.BlacklistPolicy == nil {
		return false
	}
	blacklistPolicy := ipPolicyDecision.BlacklistPolicy
	s.Observability.Logger().Warn("gateway_client_ip_blacklist_blocked", map[string]any{
		"policyId": blacklistPolicy.ID,
		"ipHash":   blacklistPolicy.IPHash,
		"endpoint": s.requestEndpointForLog(req),
	}, "网关来源 IP 命中管理员封禁")
	// recordClientIpPolicyHitAsync(...).catch(...): fire-and-forget with a
	// warn on failure handled inside the port implementation.
	s.IPPolicy.RecordClientIPPolicyHit(*blacklistPolicy)
	s.prepareEarlyAuthFailureResponse(res, options)
	clientIPText := ""
	aggregateIPKey := ""
	if ipPolicyDecision.NormalizedIP != nil {
		clientIPText = ipPolicyDecision.NormalizedIP.ClientIP
		aggregateIPKey = ipPolicyDecision.NormalizedIP.AggregateIPKey
	}
	if clientIPText == "" {
		clientIPText = blacklistPolicy.ClientIP
	}
	if aggregateIPKey == "" {
		aggregateIPKey = blacklistPolicy.AggregateIPKey
	}
	s.sendClientIPBlacklistResponse(req, res, blacklistResponseInput{
		reason:         blacklistPolicy.Reason,
		clientIP:       clientIPText,
		aggregateIPKey: aggregateIPKey,
	})
	return true
}

// recordPreAuthFailure mirrors the private helper.
func (s *Service) recordPreAuthFailure(ctx context.Context, req *GatewayRequest, res GatewayResponseWriter, reason PreAuthFailureReason, options ResolveGatewayRuntimeOptions) (CircuitDecision, error) {
	clientIP, _ := ExtractClientIP(req)
	decision, err := s.Circuits.RecordPreAuthFailure(ctx, PreAuthFailureInput{
		ClientIP:      clientIP,
		Authorization: s.GatewayPreAuthSource(req, req.Header("authorization")),
		Reason:        reason,
	})
	if err != nil {
		return CircuitDecision{}, err
	}
	if !decision.Blocked {
		return decision, nil
	}
	s.Observability.Logger().Warn("gateway_pre_auth_error_circuit_opened", map[string]any{
		"reason":            decision.Reason,
		"retryAfterSeconds": decision.RetryAfterSeconds,
		"failureCount":      decision.FailureCount,
		"endpoint":          s.requestEndpointForLog(req),
	}, "网关认证前来源保护已进入短期熔断")
	s.prepareEarlyAuthFailureResponse(res, options)
	s.sendPreAuthCircuitResponse(req, res, decision)
	return decision, nil
}

// ExtractGatewayAPIKey mirrors extractGatewayApiKey: Bearer token, then the
// x-api-key header, then the Gemini native key (x-goog-api-key / ?key=).
func (s *Service) ExtractGatewayAPIKey(req *GatewayRequest, authorization string) (string, bool) {
	if token, ok := ExtractBearerToken(authorization); ok {
		return token, true
	}
	if token, ok := headerToken(req, "x-api-key"); ok {
		return token, true
	}
	return s.geminiNativeGatewayAPIKey(req)
}

// GatewayPreAuthSource mirrors gatewayPreAuthSource: the authorization value
// as-is for Bearer, otherwise a prefixed synthetic source for the alternate
// credentials.
func (s *Service) GatewayPreAuthSource(req *GatewayRequest, authorization string) string {
	if _, ok := ExtractBearerToken(authorization); ok {
		return authorization
	}
	if apiKey, ok := headerToken(req, "x-api-key"); ok {
		return "x-api-key " + apiKey
	}
	if geminiKey, ok := s.geminiNativeGatewayAPIKey(req); ok {
		return "gemini-key " + geminiKey
	}
	return authorization
}

// headerToken mirrors headerToken: trimmed header value or missing.
func headerToken(req *GatewayRequest, name string) (string, bool) {
	text := strings.TrimSpace(req.Header(name))
	return text, text != ""
}

// geminiNativeGatewayAPIKey mirrors geminiNativeGatewayApiKey.
func (s *Service) geminiNativeGatewayAPIKey(req *GatewayRequest) (string, bool) {
	if !IsGatewayProtocolNativeRequest(req, ProtocolCodeGemini) {
		return "", false
	}
	if value, ok := headerToken(req, "x-goog-api-key"); ok {
		return value, true
	}
	return queryToken(req, "key")
}

// queryToken mirrors queryToken: read the named query parameter from the
// original URL.
func queryToken(req *GatewayRequest, name string) (string, bool) {
	originalURL := req.PathAndQuery()
	queryIndex := strings.Index(originalURL, "?")
	if queryIndex < 0 {
		return "", false
	}
	value, ok := queryParamFirstValue(originalURL[queryIndex+1:], name)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(value)
	return text, text != ""
}

// sendPreAuthCircuitResponse mirrors sendPreAuthCircuitResponse.
func (s *Service) sendPreAuthCircuitResponse(req *GatewayRequest, res GatewayResponseWriter, decision CircuitDecision) {
	message := "当前来源短时间认证失败过多，请稍后重试"
	if decision.RetryAfterSeconds != nil && !res.HeadersSent() {
		res.Header().Set("Retry-After", formatInt64(*decision.RetryAfterSeconds))
	}
	s.setGatewayAuthFailureAudit(req, auditFailureCopy{
		errorMessage: message,
		errorCode:    "client_ip_pre_auth_circuit_open",
	})
	SendGatewayJSONError(res, http.StatusTooManyRequests,
		GatewayErrorPayloadOf(message, "rate_limit_exceeded", "client_ip_pre_auth_circuit_open"),
		SendGatewayErrorOptions{Protocol: s.clientErrorProtocolOrOpenAI(req)})
}

// blacklistResponseInput mirrors sendClientIpBlacklistResponse's input.
type blacklistResponseInput struct {
	reason         string
	clientIP       string
	aggregateIPKey string
}

// sendClientIPBlacklistResponse mirrors sendClientIpBlacklistResponse.
func (s *Service) sendClientIPBlacklistResponse(req *GatewayRequest, res GatewayResponseWriter, input blacklistResponseInput) {
	ipText := blacklistIPMessage(input.clientIP, input.aggregateIPKey)
	message := ""
	if input.reason != "" {
		message = "当前来源" + ipText + "已被管理员封禁：" + input.reason
	} else {
		message = "当前来源" + ipText + "已被管理员封禁"
	}
	s.setGatewayAuthFailureAudit(req, auditFailureCopy{
		errorMessage: message,
		errorCode:    "client_ip_blacklisted",
	})
	payload := GatewayErrorPayloadOf(message, "forbidden", "client_ip_blacklisted")
	if input.clientIP != "" {
		if payload.Error.Extra == nil {
			payload.Error.Extra = map[string]any{}
		}
		payload.Error.Extra["client_ip"] = input.clientIP
	}
	if input.aggregateIPKey != "" && input.aggregateIPKey != input.clientIP {
		if payload.Error.Extra == nil {
			payload.Error.Extra = map[string]any{}
		}
		payload.Error.Extra["aggregate_ip_key"] = input.aggregateIPKey
	}
	SendGatewayJSONError(res, http.StatusForbidden, payload, SendGatewayErrorOptions{
		Protocol: s.clientErrorProtocolOrOpenAI(req),
	})
}

// blacklistIPMessage mirrors blacklistIpMessage.
func blacklistIPMessage(clientIP, aggregateIPKey string) string {
	displayIP := trimOrNull(clientIP)
	displayRange := trimOrNull(aggregateIPKey)
	if displayIP == "" && displayRange == "" {
		return ""
	}
	if displayIP != "" && displayRange != "" && displayIP != displayRange {
		return " IP " + displayIP + "（封禁范围：" + displayRange + "）"
	}
	value := displayIP
	if value == "" {
		value = displayRange
	}
	return " IP " + value
}

// auditFailureCopy mirrors setGatewayAuthFailureAudit's input.
type auditFailureCopy struct {
	errorMessage string
	errorCode    string
}

// setGatewayAuthFailureAudit mirrors setGatewayAuthFailureAudit: record the
// failure copy on the request state (Node res.locals).
func (s *Service) setGatewayAuthFailureAudit(req *GatewayRequest, input auditFailureCopy) {
	req.AuthFailureErrorMessage = input.errorMessage
	req.AuthFailureErrorCode = input.errorCode
}

// recordEarlyGatewayAuthFailure mirrors recordEarlyGatewayAuthFailure: emit a
// dropped audit capture when a response already went out.
func (s *Service) recordEarlyGatewayAuthFailure(res GatewayResponseWriter, req *GatewayRequest) {
	if !res.HeadersSent() {
		return
	}
	authErrorMessage := req.AuthFailureErrorMessage
	if authErrorMessage == "" {
		if _, ok := ExtractBearerToken(req.Header("authorization")); ok {
			authErrorMessage = "API Key 无效"
		} else {
			authErrorMessage = "缺少访问令牌"
		}
	}
	authErrorCode := req.AuthFailureErrorCode
	if authErrorCode == "" {
		authErrorCode = "invalid_request_error"
	}
	s.dispatchDroppedAuditCapture(droppedCaptureInput{
		traceID:       s.observedTraceID(req),
		trafficSource: "gateway",
		auditOutcome:  "gateway_failed",
		success:       false,
		bytes:         0,
		reason:        "gateway_auth_rejected",
		method:        req.MethodUpper(),
		path:          requestPathOnly(req),
		queryString:   requestQueryString(req),
		statusCode:    res.StatusCode(),
		errorPhase:    "auth",
		errorCode:     authErrorCode,
		errorMessage:  authErrorMessage,
		clientIP:      s.observedClientIP(req),
		userAgent:     req.Header("user-agent"),
	})
}

// sendEarlyImageGenerationDisabledResponse mirrors
// sendEarlyImageGenerationDisabledResponse.
func (s *Service) sendEarlyImageGenerationDisabledResponse(ctx context.Context, res GatewayResponseWriter, req *GatewayRequest) {
	if !res.HeadersSent() {
		SendGatewayJSONError(res, http.StatusForbidden,
			GatewayErrorPayloadOf(ImageGenerationDisabledMessage, "forbidden", ImageGenerationDisabledCode),
			SendGatewayErrorOptions{Protocol: s.clientErrorProtocolOrOpenAI(req)})
	}
	s.dispatchDroppedAuditCapture(droppedCaptureInput{
		traceID:       s.observedTraceID(req),
		trafficSource: "gateway",
		auditOutcome:  "gateway_failed",
		success:       false,
		bytes:         0,
		reason:        "gateway_permission_rejected",
		method:        req.MethodUpper(),
		path:          requestPathOnly(req),
		queryString:   requestQueryString(req),
		statusCode:    http.StatusForbidden,
		errorPhase:    "authorization",
		errorCode:     ImageGenerationDisabledCode,
		errorMessage:  ImageGenerationDisabledMessage,
		clientIP:      s.observedClientIP(req),
		userAgent:     req.Header("user-agent"),
	})
}

// sendUserRequestLimitExceededResponse mirrors sendUserRequestLimitExceededResponse.
func (s *Service) sendUserRequestLimitExceededResponse(ctx context.Context, res GatewayResponseWriter, req *GatewayRequest, decision UserRequestLimitDecision) {
	limit := int64(0)
	if decision.Limit != nil {
		limit = *decision.Limit
	}
	message := "你的" + userRequestLimitWindowLabel(decision.Window) + "请求数已达到 " + formatInt64(limit) + " 次，请联系管理员提升额度。"
	if decision.RetryAfterSeconds != nil {
		res.Header().Set("Retry-After", formatInt64(*decision.RetryAfterSeconds))
	}
	s.setGatewayAuthFailureAudit(req, auditFailureCopy{
		errorMessage: message,
		errorCode:    "user_request_limit_exceeded",
	})
	SendGatewayJSONError(res, http.StatusTooManyRequests,
		GatewayErrorPayloadOf(message, "rate_limit_exceeded", "user_request_limit_exceeded"),
		SendGatewayErrorOptions{Protocol: s.clientErrorProtocolOrOpenAI(req)})
	s.dispatchDroppedAuditCapture(droppedCaptureInput{
		traceID:       s.observedTraceID(req),
		trafficSource: "gateway",
		auditOutcome:  "gateway_failed",
		success:       false,
		bytes:         0,
		reason:        "user_request_limit_exceeded",
		method:        req.MethodUpper(),
		path:          requestPathOnly(req),
		queryString:   requestQueryString(req),
		statusCode:    http.StatusTooManyRequests,
		errorPhase:    "authorization",
		errorCode:     "user_request_limit_exceeded",
		errorMessage:  message,
		clientIP:      s.observedClientIP(req),
		userAgent:     req.Header("user-agent"),
	})
}

// userRequestLimitWindowLabel mirrors userRequestLimitWindowLabel.
func userRequestLimitWindowLabel(window UserRequestLimitWindow) string {
	switch window {
	case UserRequestLimitPerMinute:
		return "每分钟"
	case UserRequestLimitPerDay:
		return "每日"
	case UserRequestLimitPerWeek:
		return "每周"
	default:
		return "每月"
	}
}

// droppedCaptureInput mirrors dispatchDroppedAuditCapture's input.
type droppedCaptureInput struct {
	traceID       string
	trafficSource string
	auditOutcome  string
	success       bool
	bytes         int
	reason        string
	method        string
	path          string
	queryString   string
	statusCode    int
	errorPhase    string
	errorCode     string
	errorMessage  string
	clientIP      string
	userAgent     string
}

// dispatchDroppedAuditCapture mirrors dispatchDroppedAuditCapture: gate on
// the audit settings, build the finalized envelope and hand it to the Go
// audit writer.
func (s *Service) dispatchDroppedAuditCapture(input droppedCaptureInput) {
	if s.AuditSettings == nil || !s.AuditSettings.AuditLogEnabled() {
		return
	}
	timestamp := s.Clock.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	rawPath := strings.TrimSpace(input.path)
	if rawPath == "" {
		rawPath = "unknown"
	}
	target := rawPath
	if input.queryString != "" {
		target = rawPath + "?" + input.queryString
	}
	sanitized := s.Observability.SanitizeURLForLog(target)
	parts := strings.Split(sanitized, "?")
	path := parts[0]
	queryString := ""
	if len(parts) > 1 {
		queryString = strings.Join(parts[1:], "?")
	}
	if path == "" {
		path = "unknown"
	}
	method := strings.ToUpper(input.method)
	if method == "" {
		method = "UNKNOWN"
	}
	s.AuditDispatch.Dispatch(DispatchedAuditLogInput{
		ID:              s.newAuditID(),
		LifecycleStatus: "finalized",
		TraceID:         input.traceID,
		TrafficSource:   input.trafficSource,
		AuditOutcome:    input.auditOutcome,
		Success:         input.success,
		Method:          method,
		Path:            path,
		QueryString:     queryString,
		ClientIP:        input.clientIP,
		UserAgent:       input.userAgent,
		FinalStatusCode: input.statusCode,
		ErrorPhase:      input.errorPhase,
		ErrorCode:       input.errorCode,
		ErrorMessage:    input.errorMessage,
		SampleBucket:    0,
		SampleReason:    input.reason,
		CaptureStatus:   "complete",
		StartedAt:       timestamp,
		EndedAt:         timestamp,
	})
}

// observedClientIP mirrors `context?.clientIp ?? extractClientIp(req)`.
func (s *Service) observedClientIP(req *GatewayRequest) string {
	// kernel.Context carries the per-request client ip exactly like the Node
	// request context; fall back to the metadata extraction.
	if req.HTTP != nil {
		if ctx := kernel.Context(req.HTTP); ctx.ClientIP != "" {
			return ctx.ClientIP
		}
	}
	ip, _ := ExtractClientIP(req)
	return ip
}

// observedTraceID mirrors `context?.traceId ?? createTraceId()`.
func (s *Service) observedTraceID(req *GatewayRequest) string {
	if req.HTTP != nil {
		if ctx := kernel.Context(req.HTTP); ctx.TraceID != "" {
			return ctx.TraceID
		}
	}
	if s.Observability != nil {
		if traceID := s.Observability.TraceID(); traceID != "" {
			return traceID
		}
		return s.Observability.CreateTraceID()
	}
	return ""
}

// requestEndpointForLog mirrors `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`.
func (s *Service) requestEndpointForLog(req *GatewayRequest) string {
	return req.MethodUpper() + " " + s.Observability.SanitizeURLForLog(req.PathAndQuery())
}

// requestPathOnly mirrors req.originalUrl.split('?')[0] || req.path.
func requestPathOnly(req *GatewayRequest) string {
	path := strings.SplitN(req.PathAndQuery(), "?", 2)[0]
	if path == "" {
		path = req.Path()
	}
	return path
}

// requestQueryString mirrors the originalUrl query reconstruction.
func requestQueryString(req *GatewayRequest) string {
	originalURL := req.PathAndQuery()
	if !strings.Contains(originalURL, "?") {
		return ""
	}
	parts := strings.SplitN(originalURL, "?", 2)
	return parts[1]
}

// clientErrorProtocolOrOpenAI mirrors gatewayErrorProtocolForRequest with the
// unknown-request fallback expressed as an error in Node; the pre-auth error
// paths treat the default as openai to keep the response contract.
func (s *Service) clientErrorProtocolOrOpenAI(req *GatewayRequest) GatewayErrorProtocol {
	protocol, err := GatewayProtocolClientErrorProtocolForRequest(req)
	if err != nil {
		return GatewayErrorProtocolOpenAI
	}
	return protocol
}

func trimOrNull(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return trimmed
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func errorName(err error) string {
	if err == nil {
		return ""
	}
	var name interface{ ErrorName() string }
	if errors.As(err, &name) {
		return name.ErrorName()
	}
	// Mirror `error instanceof Error ? error.name : 'NonErrorThrown'`: Go
	// errors carry the type name instead.
	return "Error"
}

func boolPtr(value bool) *bool { return &value }

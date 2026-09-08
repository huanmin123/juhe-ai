package gatewayresponse

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ResponseSink 实现，对齐 failure-response.ts + fixed-responses.ts，满足
// gatewaypreauth.ResponseSink（G05 冻结 port）。usage / audit 收尾经 Deps
// 交接给 G17。

// SinkDeps 是 Sink 的端口集合；允许 nil（跳过对应副作用）。
type SinkDeps struct {
	UsageRecords   FailureUsageRecorder
	UsageDispatch  UsageDispatcher
	ModelCatalog   ModelCatalogLoader
	HTTPCompletion HTTPCompletionObserver
	Logger         StreamLogger
	// UsageSemanticForProfile 对齐 usageSemanticForProfile（providers/drivers
	// registry）的 profile 维度解析（G17 语义注册表装配）；nil 时按
	// usageSemanticForProviderCode 的驱动回退解析（D-125）。
	UsageSemanticForProfile func(providerCode string, profileID string, protocolCode string, protocolVersion string) string
	// NowMs 注入时钟。
	NowMs func() int64
}

// Sink 实现 gatewaypreauth.ResponseSink。
type Sink struct {
	Deps SinkDeps
}

// NewSink 构造。
func NewSink(deps SinkDeps) *Sink { return &Sink{Deps: deps} }

func (s *Sink) nowMs() int64 {
	if s.Deps.NowMs != nil {
		return s.Deps.NowMs()
	}
	return defaultNowMs()
}

func (s *Sink) logger() StreamLogger {
	if s.Deps.Logger != nil {
		return s.Deps.Logger
	}
	return nopStreamLogger{}
}

var _ gatewaypreauth.ResponseSink = (*Sink)(nil)

// SendGatewayFailureResponse 对齐 sendGatewayFailureResponse。
func (s *Sink) SendGatewayFailureResponse(input gatewaypreauth.FailureResponseInput) {
	protocol := gatewayErrorProtocolForRequest(input.Req)
	deliveredPayload := gatewaypreauth.LocalizedGatewayErrorPayload(input.ResponsePayload, input.StatusCode)
	if input.PreserveUpstreamErrorMessage {
		deliveredPayload = input.ResponsePayload
	}
	clientPayload := gatewaypreauth.GatewayErrorPayloadForProtocol(deliveredPayload, protocol)
	clientPayloadJSON := marshalClientPayload(clientPayload)
	// 对齐 failure-response.ts:71-74：failureScope 缺省按 outcome/attribution
	// 推断，upstream 域标注给 http metric（D-116/D-122）。
	failureScope := input.FailureScope
	if failureScope == "" {
		failureScope = inferGatewayFailureScope(input.Audit.Outcome, input.FailureAttribution)
	}
	if failureScope == "upstream" {
		markHTTPMetricFailureScope("upstream")
	}

	sendGatewayErrorResponseForSink(input.Res, input.StatusCode, deliveredPayload, gatewaypreauth.SendGatewayErrorResponseOptions{
		Protocol:                     protocol,
		PreserveUpstreamErrorMessage: input.PreserveUpstreamErrorMessage,
	})
	input.AuditCapture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Outcome:          input.Audit.Outcome,
		Success:          false,
		StatusCode:       input.StatusCode,
		ResponseHeaders:  responseHeadersToObject(input.Res.Header()),
		ResponseBody:     clientPayloadJSON,
		ResponsePartType: "gateway_error",
		ErrorPhase:       input.Audit.ErrorPhase,
		ErrorCode:        input.Audit.ErrorCode,
		ErrorMessage:     orDefault(input.Audit.ErrorMessage, deliveredPayload.Error.Message),
	})
	recordUsage := input.RecordUsage == nil || *input.RecordUsage
	if !recordUsage || s.Deps.UsageRecords == nil {
		return
	}
	usageContext := input.UsageContext
	startedAt := input.StartedAt
	statusCode := input.StatusCode
	usageErrorMessage := input.UsageErrorMessage
	failureAttribution := input.FailureAttribution
	delivered := deliveredPayload
	// 各协议渲染（openai/anthropic/gemini）均保留 error.message 原文，
	// 与 buildGatewayErrorResponseSnapshot 读取 clientPayload.error.message 一致。
	clientPayloadMessage := deliveredPayload.Error.Message
	clientPayloadSnapshot := clientPayloadJSON
	completion := s.observeCompletion(input.Res)
	go func() {
		var completedAtMs int64
		if completion != nil {
			select {
			case value, ok := <-completion.Wait():
				if ok {
					completedAtMs = value
				}
			case <-failureUsageFinalizeTimeout():
				// 完成观察缺失时不阻塞记录（Node 由 trackGatewayFailureUsageFinalization
				// 兜底；这里保守超时归零记录）。
			}
		} else {
			completedAtMs = s.nowMs()
		}
		s.Deps.UsageRecords.RecordGatewayFailure(FailureUsageRecordInput{
			UsageContext:  usageContext,
			StatusCode:    statusCode,
			StartedAtMs:   startedAt,
			CompletedAtMs: completedAtMs,
			ResponsePayload: GatewayErrorPayloadCarrier{
				Error: gatewayErrorBodyMap(delivered.Error),
				Extra: delivered.Extra,
			},
			ErrorMessage:       usageErrorMessage,
			FailureAttribution: failureAttribution,
			// 对齐 buildGatewayErrorResponseSnapshot：gateway 生成失败快照携带
			// content-type、正文与 error.message（D-122 usage 快照缺 errorMessage）。
			ResponseSnapshot: &UsageResponseSnapshotView{
				StatusCode:   statusCode,
				Headers:      map[string]string{"content-type": "application/json; charset=utf-8"},
				BodyText:     clientPayloadSnapshot,
				ErrorMessage: clientPayloadMessage,
				GeneratedBy:  "gateway",
			},
		})
	}()
}

// responseHeadersToObject 对齐 responseHeadersToObject：响应头快照取每键首值
// （键保持 WriteHeader 前的规范化形态）。
func responseHeadersToObject(header http.Header) map[string]any {
	out := make(map[string]any, len(header))
	for name, values := range header {
		if len(values) > 0 {
			out[name] = values[0]
		}
	}
	return out
}

// failureUsageFinalizeTimeout 是完成观察缺失时的保守兜底（Node 由
// trackGatewayFailureUsageFinalization 兜底，Go 侧以 5s 上限防止泄漏）。
func failureUsageFinalizeTimeout() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		<-time.After(5 * time.Second)
		close(ch)
	}()
	return ch
}

func (s *Sink) observeCompletion(res gatewaypreauth.GatewayResponseWriter) HTTPCompletion {
	if s.Deps.HTTPCompletion == nil {
		return nil
	}
	return s.Deps.HTTPCompletion.Observe(res)
}

// FinalizeGatewayAuthFailureAudit 对齐 finalizeGatewayAuthFailureAudit。
func (s *Sink) FinalizeGatewayAuthFailureAudit(req *gatewaypreauth.GatewayRequest, res gatewaypreauth.GatewayResponseWriter, auditCapture gatewaypreauth.AuditCaptureContext) {
	if lazy, ok := auditCapture.(AttemptAuditCapture); ok {
		lazy.FinalizeLazy(func() gatewaypreauth.AuditFinalizeInput {
			authErrorMessage := req.AuthFailureErrorMessage
			authErrorCode := req.AuthFailureErrorCode
			if authErrorMessage == "" {
				if strings.TrimSpace(req.Header("authorization")) != "" {
					authErrorMessage = "API Key 无效"
				} else {
					authErrorMessage = "缺少访问令牌"
				}
			}
			if authErrorCode == "" {
				authErrorCode = "invalid_request_error"
			}
			payload := gatewaypreauth.GatewayErrorPayloadOf(authErrorMessage, "invalid_request_error", authErrorCode)
			return gatewaypreauth.AuditFinalizeInput{
				Outcome:          gatewaypreauth.AuditOutcomeGatewayFailed,
				Success:          false,
				StatusCode:       res.StatusCode(),
				ResponseBody:     marshalClientPayload(payload),
				ResponsePartType: "gateway_error",
				ErrorPhase:       "auth",
				ErrorCode:        authErrorCode,
				ErrorMessage:     authErrorMessage,
			}
		})
		return
	}
	// 未实现 lazy 的捕获实现按立即收尾处理（触发点保持一致）。
	authErrorMessage := req.AuthFailureErrorMessage
	if authErrorMessage == "" {
		if strings.TrimSpace(req.Header("authorization")) != "" {
			authErrorMessage = "API Key 无效"
		} else {
			authErrorMessage = "缺少访问令牌"
		}
	}
	authErrorCode := orDefault(req.AuthFailureErrorCode, "invalid_request_error")
	payload := gatewaypreauth.GatewayErrorPayloadOf(authErrorMessage, "invalid_request_error", authErrorCode)
	auditCapture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Outcome:          gatewaypreauth.AuditOutcomeGatewayFailed,
		Success:          false,
		StatusCode:       res.StatusCode(),
		ResponseBody:     marshalClientPayload(payload),
		ResponsePartType: "gateway_error",
		ErrorPhase:       "auth",
		ErrorCode:        authErrorCode,
		ErrorMessage:     authErrorMessage,
	})
}

// SendAuthenticatedModelsGatewayResponse 对齐
// sendAuthenticatedModelsGatewayResponse。
func (s *Sink) SendAuthenticatedModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput) {
	s.sendModelsGatewayResponse(input, input.Protocol)
}

// SendOpenAIModelsGatewayResponse 对齐 sendOpenAIModelsGatewayResponse。
func (s *Sink) SendOpenAIModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput) {
	s.sendModelsGatewayResponse(input, "openai")
}

// SendAnthropicModelsGatewayResponse 对齐 sendAnthropicModelsGatewayResponse。
func (s *Sink) SendAnthropicModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput) {
	s.sendModelsGatewayResponse(input, "anthropic")
}

// SendGeminiModelsGatewayResponse 对齐 sendGeminiModelsGatewayResponse。
func (s *Sink) SendGeminiModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput) {
	s.sendModelsGatewayResponse(input, "gemini")
}

func (s *Sink) sendModelsGatewayResponse(input gatewaypreauth.ModelsResponseInput, protocol string) {
	providerCodes := normalizedProviderCodeList(input.ProviderCodes)
	providerCode := modelsUsageProviderCode(providerCodes, input.UsageContext.ProviderCode)
	responsePayload := s.sendModelsGatewayResponsePayload(input, protocol, providerCode, providerCodes)
	if s.Deps.UsageDispatch == nil {
		return
	}
	now := s.nowMs()
	elapsed := now - input.StartedAt
	s.Deps.UsageDispatch.DispatchUsageRecord(ModelsUsageDispatchInput{
		UsageContext:  input.UsageContext,
		ProviderCode:  providerCode,
		Stream:        false,
		StatusCode:    200,
		Success:       true,
		FirstTokenMs:  elapsed,
		DurationMs:    elapsed,
		UsageSemantic: s.usageSemanticFor(input, providerCode),
	})
	_ = responsePayload
}

// usageSemanticFor 对齐 usageSemanticForProfile({providerCode,
// providerProtocolProfileId, protocolCode, protocolVersion})：注册表未装配时
// 按 providerCode 的驱动回退解析（anthropic→anthropic、gemini→gemini、
// 其余→openai；D-125 空占位修复）。
func (s *Sink) usageSemanticFor(input gatewaypreauth.ModelsResponseInput, providerCode string) string {
	if s.Deps.UsageSemanticForProfile != nil {
		return s.Deps.UsageSemanticForProfile(
			providerCode,
			input.UsageContext.ProviderProtocolProfileID,
			input.UsageContext.ProtocolCode,
			input.UsageContext.ProtocolVersion,
		)
	}
	return usageSemanticForProviderCode(providerCode)
}

// usageSemanticForProviderCode 对齐 usageSemanticForProviderCode：驱动注册表
// 的 providerCode → usageSemantic 投影；未命中驱动回退 'openai'。
func usageSemanticForProviderCode(providerCode string) string {
	switch normalizeProviderToken(providerCode) {
	case "anthropic":
		return "anthropic"
	case "gemini":
		return "gemini"
	default:
		return "openai"
	}
}

func (s *Sink) sendModelsGatewayResponsePayload(input gatewaypreauth.ModelsResponseInput, protocol string, providerCode string, providerCodes []string) any {
	systemAccountID := input.UsageContext.SystemAccountID
	catalog := s.loadCatalog(systemAccountID, providerCodes)
	var responsePayload any
	switch protocol {
	case "anthropic":
		responsePayload = buildAnthropicModelsPayload(catalog)
	case "gemini":
		responsePayload = buildGeminiModelsPayload(catalog)
	default:
		responsePayload = buildOpenAIModelsPayload(catalog, input.Req)
	}
	if systemAccountID != "" {
		setAuthenticatedModelsClientCacheHeaders(input.Res)
	}
	kernelWriteJSON(input.Res, 200, responsePayload)
	// 对齐 fixed-responses.ts 的 finalizeLazy：审计携带 firstTokenMs
	//（Date.now() - startedAt）。G05 冻结的 AuditFinalizeInput 不含首 token
	// 字段，扩展面由实现方以 FinalizeExtended 接收；缺省回退 Finalize。
	firstTokenMs := s.nowMs() - input.StartedAt
	finalizeInput := gatewaypreauth.AuditFinalizeInput{
		Outcome:          gatewaypreauth.AuditOutcomeSuccess,
		Success:          true,
		StatusCode:       200,
		ResponseHeaders:  responseHeadersToObject(input.Res.Header()),
		ResponseBody:     marshalClientPayload(responsePayload),
		ResponsePartType: "gateway_response",
	}
	if extender, ok := input.AuditCapture.(AuditFinalizeExtender); ok {
		extender.FinalizeExtended(finalizeInput, AuditFinalizeExtras{FirstTokenMs: &firstTokenMs})
		return responsePayload
	}
	input.AuditCapture.Finalize(finalizeInput)
	return responsePayload
}

func (s *Sink) loadCatalog(systemAccountID string, providerCodes []string) []ModelCatalogEntry {
	if s.Deps.ModelCatalog == nil {
		return nil
	}
	return s.Deps.ModelCatalog.ListClientModelCatalog(systemAccountID, providerCodes)
}

func normalizedProviderCodeList(providerCodes []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(providerCodes))
	for _, item := range providerCodes {
		code := normalizeProviderToken(item)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func modelsUsageProviderCode(providerCodes []string, fallback string) string {
	if len(providerCodes) > 0 {
		return providerCodes[0]
	}
	if fallback != "" {
		return fallback
	}
	return "openai_compatible"
}

// normalizeProviderToken 对齐 normalizeProviderToken（domain/provider-protocol）。
func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func marshalClientPayload(payload any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func gatewayErrorBodyMap(body gatewaypreauth.GatewayErrorBody) map[string]any {
	object := map[string]any{
		"message": body.Message,
		"type":    body.Type,
	}
	if body.Code != "" {
		object["code"] = body.Code
	}
	for key, value := range body.Extra {
		object[key] = value
	}
	return object
}

func gatewayErrorProtocolForRequest(req *gatewaypreauth.GatewayRequest) gatewaypreauth.GatewayErrorProtocol {
	// 对齐 gatewayErrorProtocolForRequest(req) = gatewayProtocolClientErrorProtocolForRequest(req)：
	// 失败响应默认按请求路径解析协议（models/anthropic/gemini 路径）。
	if req != nil {
		path := LowercasedRequestPath(req.PathAndQuery())
		if strings.Contains(path, "/messages") || strings.Contains(path, "/anthropic") {
			return gatewaypreauth.GatewayErrorProtocolAnthropic
		}
		if strings.Contains(path, "/gemini") || strings.Contains(path, "generatecontent") {
			return gatewaypreauth.GatewayErrorProtocolGemini
		}
	}
	return gatewaypreauth.GatewayErrorProtocolOpenAI
}

// inferGatewayFailureScope 对齐 inferGatewayFailureScope。
func inferGatewayFailureScope(outcome string, attribution string) string {
	if outcome == "upstream_failed" {
		return "upstream"
	}
	if attribution == "account_upstream" || attribution == "account_dependency" || attribution == "opaque_upstream" {
		return "upstream"
	}
	return ""
}

// sendGatewayErrorResponseForSink 转发 G05 的 sendGatewayErrorResponse。
func sendGatewayErrorResponseForSink(res gatewaypreauth.GatewayResponseWriter, statusCode int, payload gatewaypreauth.GatewayErrorPayload, options gatewaypreauth.SendGatewayErrorResponseOptions) {
	gatewaypreauth.SendGatewayErrorResponse(res, statusCode, payload, options)
}

func kernelWriteJSON(res gatewaypreauth.GatewayResponseWriter, status int, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		res.WriteHeader(http.StatusInternalServerError)
		return
	}
	header := res.Header()
	if header.Get("content-type") == "" {
		header.Set("content-type", "application/json; charset=utf-8")
	}
	res.WriteHeader(status)
	_, _ = res.Write(encoded)
}

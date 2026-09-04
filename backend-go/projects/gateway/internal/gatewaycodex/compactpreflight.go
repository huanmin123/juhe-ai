package gatewaycodex

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of codex-responses/compact-preflight.ts: the gateway-summary compact
// dispatch for Chat bridge accounts. The upstream summary exchange
// (Node fetchFirstAvailableUpstream + readUpstreamBodyLimited +
// hotQualityAttempt bookkeeping + releaseConcurrency) stays behind the
// CompactSummaryDispatcher seam so the dispatch pipeline can wire its real
// implementation later; every local decision, copy, audit record and status
// code lives here and mirrors Node byte for byte.

// Compact summary request constants mirror buildCompactSummaryChatBody.
const (
	compactSummaryFallbackModel = "codex-compact-summary"
	compactContextMaxChars      = 120000
	compactSummaryMaxBytes      = 1024 * 1024
)

// CompactQualityTerminal mirrors hotQualityAttempt.recordTerminal's input.
type CompactQualityTerminal struct {
	OutcomeClass string
	FailureScope string
	Source       string
	FirstByteMs  int64
}

// CompactUpstreamExchange mirrors the consumed surface of the dispatched
// upstream result: the account, the (bounded) response body text and the
// lifecycle hooks the compact preflight owes the hot-quality attempt.
type CompactUpstreamExchange struct {
	Account        gatewayruntimecache.OpenAIAccountSecret
	BodyText       string
	Truncated      bool
	FirstByteMs    int64
	UpstreamOK     bool
	UpstreamStatus int
	// RecordTerminal mirrors hotQualityAttempt.recordTerminal; nil drops it.
	RecordTerminal func(terminal CompactQualityTerminal)
	// ReleaseConcurrency mirrors upstreamResult.releaseConcurrency.
	ReleaseConcurrency func()
}

// CompactSummaryDispatchInput carries the synthetic chat completions
// exchange request.
type CompactSummaryDispatchInput struct {
	// Body is the chat completions request object built by
	// BuildCompactSummaryChatBody.
	Body             map[string]any
	RawBody          []byte
	Accounts         []gatewayruntimecache.OpenAIAccountSecret
	Settings         gatewayruntimecache.GatewaySettings
	Signal           context.Context
	StartedAt        int64
	SyntheticRequest *gatewaypreauth.GatewayRequest
}

// CompactSummaryDispatcher mirrors fetchFirstAvailableUpstream for the
// synthetic compact summary request.
type CompactSummaryDispatcher interface {
	DispatchCompactSummary(ctx context.Context, input CompactSummaryDispatchInput) (*CompactUpstreamExchange, error)
}

// CompactSummaryDispatcherFunc adapts a function to the interface.
type CompactSummaryDispatcherFunc func(ctx context.Context, input CompactSummaryDispatchInput) (*CompactUpstreamExchange, error)

// DispatchCompactSummary implements CompactSummaryDispatcher.
func (f CompactSummaryDispatcherFunc) DispatchCompactSummary(ctx context.Context, input CompactSummaryDispatchInput) (*CompactUpstreamExchange, error) {
	return f(ctx, input)
}

// CompactPreflightService wires the compact preflight collaborators.
type CompactPreflightService struct {
	Bridge     *ChatBridgeStateService
	Registry   *ContextRequestStateRegistry
	Dispatcher CompactSummaryDispatcher
	Clock      Clock
	Sink       gatewaypreauth.ResponseSink
}

// CompactPreflightInput mirrors applyCodexResponsesChatBridgeCompactPreflight's
// input bag (the G05 port shape).
type CompactPreflightInput struct {
	Req                        *gatewaypreauth.GatewayRequest
	Res                        gatewaypreauth.GatewayResponseWriter
	AuditCapture               gatewaypreauth.AuditCaptureContext
	UsageContext               gatewaypreauth.GatewayFailureUsageContext
	StartedAt                  int64
	SystemAccountID            string
	APIKeyID                   string
	GroupID                    string
	GroupAccess                gatewayruntimecache.GroupUsageAccessMetadata
	RequestClientCompatibility string
	DispatchAccounts           []gatewayruntimecache.OpenAIAccountSecret
	ActiveGatewaySettings      gatewayruntimecache.GatewaySettings
	ModelPriority              *gatewayrouting.GatewayAccountModelPriority
	RequestLane                string
	// ClientIPAccountAvoidance / GroupSchedulingPolicy / RequestCoordination
	// are pass-through handles for the dispatch seam (Node forwards them to
	// fetchFirstAvailableUpstream untouched).
	ClientIPAccountAvoidance any
	GroupSchedulingPolicy    *map[string]any
	RequestCoordination      any
	OnDispatchedAccount      func(account gatewayruntimecache.OpenAIAccountSecret)
	Signal                   context.Context
}

// CompactPreflightResult mirrors the outcome union.
type CompactPreflightResult struct {
	// Completed mirrors outcome === 'completed'.
	Completed bool
	// Accounts mirrors the post-preflight dispatch accounts of the
	// 'continued' outcome.
	Accounts []gatewayruntimecache.OpenAIAccountSecret
}

// ApplyChatBridgeCompactPreflight mirrors
// applyCodexResponsesChatBridgeCompactPreflight.
func (s *CompactPreflightService) ApplyChatBridgeCompactPreflight(ctx context.Context, input CompactPreflightInput) (CompactPreflightResult, error) {
	prepare := s.Bridge.PrepareCodexResponsesCompactDispatchForAccounts(s.Registry, input.Req, input.DispatchAccounts)
	if !isOpenAIResponsesCompactPostRequest(input.Req) || !prepare {
		accounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(input.DispatchAccounts))
		for _, account := range input.DispatchAccounts {
			if s.Bridge.CodexResponsesContextAllowsAccount(s.Registry, input.Req, account) {
				accounts = append(accounts, account)
			}
		}
		return CompactPreflightResult{Completed: false, Accounts: accounts}, nil
	}
	bridgeDispatchAccounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(input.DispatchAccounts))
	for _, account := range input.DispatchAccounts {
		if s.Bridge.HasExplicitCodexResponsesChatBridgeRuntimeAccount(s.Registry, input.Req, []gatewayruntimecache.OpenAIAccountSecret{account}) {
			bridgeDispatchAccounts = append(bridgeDispatchAccounts, account)
		}
	}
	body, err := s.Bridge.ParseGatewayJSONObjectPublic(input.Req)
	if err != nil {
		return CompactPreflightResult{}, err
	}
	previousResponseID := normalizedOptionalText(body["previous_response_id"])
	boundary := codexContextBoundary(input.SystemAccountID, input.APIKeyID, input.GroupID, input.GroupAccess)
	restoreResult, err := s.Bridge.RestoreChatBridgeInputForCompact(ctx, struct {
		PreviousResponseID string
		Boundary           CodexContextStateBoundary
		CurrentInput       any
	}{
		PreviousResponseID: previousResponseID,
		Boundary:           boundary,
		CurrentInput:       body["input"],
	})
	if err != nil {
		return CompactPreflightResult{}, err
	}
	if restoreResult.Outcome != "found" && restoreResult.Outcome != "no_previous" {
		s.sendCompactFailure(input, restoreFailureForCompact(restoreResult.Outcome))
		return CompactPreflightResult{Completed: true}, nil
	}

	model := normalizedOptionalText(body["model"])
	if model == "" {
		model = compactSummaryFallbackModel
	}
	summaryRequest := BuildCompactSummaryChatBody(model, restoreResult.Input)
	synthetic := BuildSyntheticChatCompletionsRequest(input.Req, summaryRequest)
	var exchange *CompactUpstreamExchange
	func() {
		if s.Dispatcher == nil {
			exchange = nil
			return
		}
		result, dispatchErr := s.Dispatcher.DispatchCompactSummary(ctx, CompactSummaryDispatchInput{
			Body:             summaryRequest,
			RawBody:          gatewaybody.SerializeGatewayJSONObject(summaryRequest).Raw,
			Accounts:         bridgeDispatchAccounts,
			Settings:         input.ActiveGatewaySettings,
			Signal:           input.Signal,
			StartedAt:        input.StartedAt,
			SyntheticRequest: synthetic,
		})
		exchange = result
		if dispatchErr != nil {
			err = dispatchErr
		}
	}()
	if err != nil {
		s.recordExchangeTerminal(exchange, input, CompactQualityTerminal{
			OutcomeClass: terminalOutcomeClass(input.Signal, err),
			FailureScope: terminalFailureScope(input.Signal, err),
			Source:       terminalSource(input.Signal, err),
		})
		s.releaseExchange(exchange)
		s.sendCompactFailure(input, gatewayFailure{
			statusCode: 502,
			_type:      "bad_gateway",
			code:       "codex_bridge_compact_summary_failed",
			message:    fmt.Sprintf("上游摘要请求失败：%s", errorMessageText(err)),
		})
		return CompactPreflightResult{Completed: true}, nil
	}
	if exchange == nil {
		return CompactPreflightResult{}, fmt.Errorf("codex compact summary dispatcher 未配置")
	}
	if input.OnDispatchedAccount != nil {
		input.OnDispatchedAccount(exchange.Account)
	}
	summary := ExtractChatCompletionSummary(exchange.BodyText)
	if summary == "" {
		s.sendCompactFailure(input, gatewayFailure{
			statusCode: 502,
			_type:      "bad_gateway",
			code:       "codex_bridge_compact_summary_empty",
			message:    "上游摘要模型没有返回可用的压缩摘要",
		})
		s.releaseExchange(exchange)
		return CompactPreflightResult{Completed: true}, nil
	}
	summaryUpstreamModel := normalizedOptionalText(summaryRequest["model"])
	if resolved := ResolveGatewayUsageModel(exchange.Account, normalizedOptionalText(summaryRequest["model"]), "chat_completions"); resolved != "" {
		summaryUpstreamModel = resolved
	}
	compactSnapshot, err := s.Bridge.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SessionID:         restoreResult.SessionID,
		SourceResponseID:  previousResponseID,
		Boundary:          boundary,
		Summary:           summary,
		UpstreamAccountID: exchange.Account.ID,
		Model:             normalizedOptionalText(body["model"]),
		UpstreamModel:     summaryUpstreamModel,
	})
	if err != nil {
		s.recordExchangeTerminal(exchange, input, CompactQualityTerminal{
			OutcomeClass: "read_interruption",
			FailureScope: "protocol_model",
			Source:       "gateway_transport",
		})
		s.releaseExchange(exchange)
		s.sendCompactFailure(input, gatewayFailure{
			statusCode: 502,
			_type:      "bad_gateway",
			code:       "codex_bridge_compact_summary_failed",
			message:    fmt.Sprintf("上游摘要请求失败：%s", errorMessageText(err)),
		})
		return CompactPreflightResult{Completed: true}, nil
	}
	responsePayload := BuildCodexCompactResponse(compactSnapshot.CompactID, compactSnapshot.EncryptedContent, s.Clock.Now())
	input.AuditCapture.AddGatewayMetadata("codex_responses_chat_bridge_compact", map[string]any{
		"mode":                  "gateway_summary_compact",
		"previousResponseId":    previousResponseID,
		"sessionId":             restoreResult.SessionID,
		"restoredResponseCount": restoreResult.ResponseCount,
		"accountId":             exchange.Account.ID,
		"upstreamStatus":        exchange.UpstreamStatus,
	})
	writeJSONResponse(input.Res, http.StatusOK, responsePayload)
	input.AuditCapture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Outcome:          gatewaypreauth.AuditOutcomeSuccess,
		Success:          true,
		StatusCode:       200,
		ResponseHeaders:  responseHeadersToObject(input.Res),
		ResponseBody:     string(mustMarshalJSON(responsePayload)),
		ResponsePartType: "gateway_response",
	})
	s.releaseExchange(exchange)
	return CompactPreflightResult{Completed: true}, nil
}

func (s *CompactPreflightService) recordExchangeTerminal(exchange *CompactUpstreamExchange, input CompactPreflightInput, terminal CompactQualityTerminal) {
	if exchange == nil || exchange.RecordTerminal == nil {
		return
	}
	terminal.FirstByteMs = exchange.FirstByteMs
	exchange.RecordTerminal(terminal)
}

func (s *CompactPreflightService) releaseExchange(exchange *CompactUpstreamExchange) {
	if exchange == nil || exchange.ReleaseConcurrency == nil {
		return
	}
	exchange.ReleaseConcurrency()
}

func terminalOutcomeClass(signal context.Context, err error) string {
	if signal != nil && signal.Err() != nil {
		return "client_cancellation"
	}
	return "read_interruption"
}

func terminalFailureScope(signal context.Context, err error) string {
	if signal != nil && signal.Err() != nil {
		return "none"
	}
	return "protocol_model"
}

func terminalSource(signal context.Context, err error) string {
	if signal != nil && signal.Err() != nil {
		return "request_lifecycle"
	}
	return "gateway_transport"
}

func errorMessageText(err error) string {
	return err.Error()
}

// BuildCompactSummaryChatBody mirrors buildCompactSummaryChatBody.
func BuildCompactSummaryChatBody(model string, restoredInput []any) map[string]any {
	return map[string]any{
		"model":  model,
		"stream": false,
		"messages": []any{
			map[string]any{
				"role": "system",
				"content": strings.Join([]string{
					"你负责为 Codex Responses 会话做上下文压缩。",
					"输出一段可继续对话的摘要，保留用户目标、关键决策、工具调用结果、文件路径、错误和待办。",
					"不要输出解释、标题、Markdown 包装或无关寒暄。",
				}, "\n"),
			},
			map[string]any{
				"role":    "user",
				"content": compactContextText(restoredInput),
			},
		},
	}
}

func compactContextText(input []any) string {
	text := string(mustMarshalJSON(input))
	if len(text) <= compactContextMaxChars {
		return text
	}
	// Node slices the JS string by UTF-16 code units; Go slices bytes. For
	// the ASCII JSON bodies this produces the identical prefix.
	return text[:compactContextMaxChars] + "\n[truncated]"
}

// BuildCodexCompactResponse mirrors buildCodexCompactResponse.
func BuildCodexCompactResponse(compactID, encryptedContent string, now time.Time) map[string]any {
	responseID := "resp_" + strings.TrimPrefix(compactID, "cmp_")
	return map[string]any{
		"id":         responseID,
		"object":     "response.compaction",
		"created_at": int64(math.Floor(float64(now.UnixMilli()) / 1000)),
		"output": []any{
			map[string]any{
				"id":                compactID,
				"type":              "compaction",
				"encrypted_content": encryptedContent,
			},
		},
		"usage": map[string]any{
			"input_tokens": 0,
			"input_tokens_details": map[string]any{
				"cached_tokens": 0,
			},
			"output_tokens": 0,
			"output_tokens_details": map[string]any{
				"reasoning_tokens": 0,
			},
			"total_tokens": 0,
		},
	}
}

// ExtractChatCompletionSummary mirrors extractChatCompletionSummary.
func ExtractChatCompletionSummary(bodyText string) string {
	var parsed any
	if err := json.Unmarshal([]byte(bodyText), &parsed); err != nil {
		return ""
	}
	record, isObject := parsed.(map[string]any)
	if !isObject {
		return ""
	}
	choices, isArray := record["choices"].([]any)
	if !isArray {
		return ""
	}
	for _, choice := range choices {
		choiceRecord, isChoiceObject := choice.(map[string]any)
		if !isChoiceObject {
			continue
		}
		message, messageIsObject := choiceRecord["message"].(map[string]any)
		var content string
		if messageIsObject {
			content = normalizedOptionalText(message["content"])
		}
		if content != "" {
			return content
		}
	}
	return ""
}

// ResolveGatewayUsageModel mirrors resolveGatewayUsageModel(account, model,
// 'chat_completions').upstreamModel: without model mappings the requested
// model passes through.
func ResolveGatewayUsageModel(account gatewayruntimecache.OpenAIAccountSecret, requestedModel, sourceEndpointFamily string) string {
	if requestedModel == "" {
		return ""
	}
	runtime := projectRuntimeAccount(account)
	resolved := gatewayopenai.ResolveAccountModelMapping(runtime, requestedModel, sourceEndpointFamily)
	if resolved == nil {
		return requestedModel
	}
	return resolved.UpstreamModel
}

// BuildSyntheticChatCompletionsRequest mirrors buildSyntheticChatCompletionsRequest:
// the synthetic request carries the chat completions path, the forced JSON
// headers and the parsed chat body.
func BuildSyntheticChatCompletionsRequest(sourceReq *gatewaypreauth.GatewayRequest, body map[string]any) *gatewaypreauth.GatewayRequest {
	rawBody := gatewaybody.SerializeGatewayJSONObject(body)
	syntheticHTTP := sourceReq.HTTP.Clone(context.Background())
	syntheticHTTP.Method = http.MethodPost
	if syntheticHTTP.URL != nil {
		clone := *syntheticHTTP.URL
		clone.Path = "/v1/chat/completions"
		clone.RawPath = ""
		clone.Host = syntheticHTTP.URL.Host
		syntheticHTTP.URL = &clone
	}
	syntheticHTTP.RequestURI = "/v1/chat/completions"
	syntheticHTTP.Header = sourceReq.HTTP.Header.Clone()
	syntheticHTTP.Header.Set("Accept", "application/json")
	syntheticHTTP.Header.Set("Content-Type", "application/json")
	synthetic := gatewaypreauth.NewGatewayRequest(syntheticHTTP)
	synthetic.Body = &gatewaybody.Request{
		RawBody: rawBody.Raw,
		Body:    body,
		State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{
			RawBody:         rawBody.Raw,
			ContentType:     "application/json",
			JSONParseStatus: gatewaybody.JSONParseStatusParsed,
			ParsedBody:      body,
			Stream:          boolPtr(false),
		}),
		Serialized: rawBody,
	}
	synthetic.ClientIP = sourceReq.ClientIP
	synthetic.RemoteAddr = sourceReq.RemoteAddr
	return synthetic
}

func boolPtr(value bool) *bool { return &value }

func restoreFailureForCompact(outcome string) gatewayFailure {
	if outcome == RestoreOutcomeBoundaryMismatch {
		return gatewayFailure{
			statusCode: 403,
			_type:      "invalid_request_error",
			code:       "codex_bridge_compact_boundary_mismatch",
			message:    "compact 上下文不属于当前 API Key、分组或供应商边界",
		}
	}
	if outcome == RestoreOutcomeChainTooDeep {
		return gatewayFailure{
			statusCode: 413,
			_type:      "invalid_request_error",
			code:       "codex_bridge_compact_chain_too_deep",
			message:    "compact 上下文链过长，无法在当前网关限制内压缩",
		}
	}
	return gatewayFailure{
		statusCode: 404,
		_type:      "invalid_request_error",
		code:       "codex_bridge_compact_context_not_found",
		message:    "compact 对应的服务端上下文不存在、已过期或校验失败",
	}
}

func (s *CompactPreflightService) sendCompactFailure(input CompactPreflightInput, failure gatewayFailure) {
	if s.Sink == nil {
		return
	}
	responsePayload := gatewaypreauth.GatewayErrorPayloadOf(failure.message, failure._type, failure.code)
	s.Sink.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             input.Req,
		Res:             input.Res,
		AuditCapture:    input.AuditCapture,
		UsageContext:    input.UsageContext,
		StartedAt:       input.StartedAt,
		StatusCode:      failure.statusCode,
		ResponsePayload: responsePayload,
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
			ErrorPhase:   "request_validation",
			ErrorCode:    failure.code,
			ErrorMessage: failure.message,
		},
	})
}

// writeJSONResponse mirrors res.status(status).json(payload).
func writeJSONResponse(res gatewaypreauth.GatewayResponseWriter, status int, payload any) {
	if res == nil {
		return
	}
	body := mustMarshalJSON(payload)
	header := res.Header()
	if header != nil {
		header.Set("Content-Type", "application/json; charset=utf-8")
	}
	res.WriteHeader(status)
	_, _ = res.Write(body)
}

// responseHeadersToObject mirrors responseHeadersToObject(res).
func responseHeadersToObject(res gatewaypreauth.GatewayResponseWriter) map[string]any {
	headers := map[string]any{}
	if res == nil {
		return headers
	}
	for key, values := range res.Header() {
		if len(values) == 1 {
			headers[key] = values[0]
			continue
		}
		headers[key] = append([]string(nil), values...)
	}
	return headers
}

func mustMarshalJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return encoded
}

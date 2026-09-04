package gatewayhybrid

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Hybrid response quality inspection, mirroring
// backend/src/modules/gateway/hybrid/quality-inspection.service.ts.

// Quality limits mirroring the Node constants.
const (
	hybridQualityContextMaxBytes      = 192 * 1024
	hybridQualityResponseMaxBytes     = 2 * 1024 * 1024
	hybridQualityRequestParseMaxBytes = 128 * 1024
)

// Hybrid quality enums (mirror the Node string unions).
const (
	HybridQualityFailureProtocolInvalid        = "protocol_invalid"
	HybridQualityFailureMissingRequiredOutput  = "missing_required_output"
	HybridQualityFailureLowQuality             = "low_quality"
	HybridQualityFailureUnsafeOrPolicy         = "unsafe_or_policy"
	HybridQualityFailureToolOrSchemaMismatch   = "tool_or_schema_mismatch"
	HybridQualityFailureOther                  = "other"
)

const (
	HybridQualityRetryAccept           = "accept"
	HybridQualityRetrySameModel        = "retry_same_model"
	HybridQualityRetryUpgradeNextLevel = "upgrade_next_level"
	HybridQualityRetryReturnError      = "return_error"
)

const (
	HybridQualityActionRepairThenUpgrade = "repair_then_upgrade"
	HybridQualityActionPassThrough       = "pass_through"
)

// Quality error codes / messages (byte-identical with the Node literals).
const (
	QualityErrorCodeNoAccount = "no_quality_scoring_account"
	QualityErrorCodeDispatch  = "hybrid_quality_scoring_failed"
	QualityErrorCodeHTTPError = "hybrid_quality_scoring_http_error"

	QualityNoAccountMessage = "混合路由绑定分组池没有可用质量评分账户"
	QualityDispatchMessage  = "混合路由质量评分模型调用失败"
	QualityTooLargeMessage  = "混合路由质量评分响应超过保护上限"
	QualityUnfinishedMessage = "混合路由质量评分调用未完成收尾"
)

// HybridQualityScoreResult mirrors HybridQualityScoreResult.
type HybridQualityScoreResult struct {
	Pass                bool
	Score               float64
	Confidence          *float64
	FailureType         string
	HasFailureType      bool
	Reason              *string
	RetryRecommendation string
}

// HybridQualityInspectionOutcome mirrors HybridQualityInspectionOutcome.
type HybridQualityInspectionOutcome struct {
	Triggered         bool
	TriggerReason     string
	Pass              bool
	Result            *HybridQualityScoreResult
	ActualAction      string
	QualityAccountID  string
	StatusCode        *int
	ErrorCode         string
	ErrorMessage      string
}

// QualityInspectionConfig mirrors ApiKeyHybridQualityInspectionConfig from
// routestrategies (type alias keeps signatures short).
type QualityInspectionConfig = routestrategies.HybridQualityInspection

// QualityTrigger mirrors the { triggered, reason } pair.
type QualityTrigger struct {
	Triggered bool
	Reason    string
}

// ShouldTriggerHybridQualityInspection mirrors
// shouldTriggerHybridQualityInspection.
func ShouldTriggerHybridQualityInspection(input QualityTriggerInput) QualityTrigger {
	qualityConfig := input.Config.QualityInspection
	if qualityConfig == nil || !qualityConfig.Enabled {
		return QualityTrigger{Triggered: false, Reason: "quality_inspection_disabled"}
	}
	if qualityConfig.TriggerMode == "always_for_hybrid" {
		return QualityTrigger{Triggered: true, Reason: "always_for_hybrid"}
	}
	if qualityConfig.TriggerMode == "quality_first_only" {
		if input.Config.QualityPreference == "quality_first" {
			return QualityTrigger{Triggered: true, Reason: "quality_first_preference"}
		}
		return QualityTrigger{Triggered: false, Reason: "quality_first_only_not_matched"}
	}
	if input.Config.QualityPreference == "quality_first" {
		return QualityTrigger{Triggered: true, Reason: "quality_first_preference"}
	}
	if HasStrictOutputRequirement(input.View) {
		return QualityTrigger{Triggered: true, Reason: "strict_output_requirement"}
	}
	if strings.TrimSpace(input.ResponseBodyText) == "" {
		return QualityTrigger{Triggered: true, Reason: "empty_response_body"}
	}
	if input.TargetRoute.MinLevel <= qualityConfig.MaxTriggerLevel {
		return QualityTrigger{Triggered: true, Reason: "low_or_mid_route_level"}
	}
	return QualityTrigger{Triggered: false, Reason: "low_risk_request"}
}

// QualityTriggerInput mirrors the shouldTrigger input.
type QualityTriggerInput struct {
	View            *GatewayRequestView
	Config          *routestrategies.HybridRoutingConfig
	Scoring         HybridScoringResult
	TargetRoute     routestrategies.HybridLevelRoute
	ResponseBodyText string
}

// HasStrictOutputRequirement mirrors hasStrictOutputRequirement.
func HasStrictOutputRequirement(view *GatewayRequestView) bool {
	body := view.bodyObject()
	if body == nil {
		state := view.BodyState
		if state == nil {
			return false
		}
		return state.StrictOutputRequirement ||
			boolValue(state.ImageGeneration) ||
			boolValue(state.ImageGenerationForced)
	}
	return OrderedValue(body, "response_format") != nil ||
		OrderedValue(body, "tools") != nil ||
		OrderedValue(body, "tool_choice") != nil
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

// ResolveHybridQualityAction mirrors resolveHybridQualityAction.
func ResolveHybridQualityAction(result *HybridQualityScoreResult, config *QualityInspectionConfig) string {
	if result.Pass {
		return HybridQualityRetryAccept
	}
	if result.HasFailureType && result.FailureType == HybridQualityFailureUnsafeOrPolicy {
		return HybridQualityRetryReturnError
	}
	if result.RetryRecommendation == HybridQualityRetryReturnError {
		return HybridQualityRetryReturnError
	}
	return config.FailureAction
}

// QualityInspectInput mirrors the inspectHybridGatewayQuality input.
type QualityInspectInput struct {
	View             *GatewayRequestView
	APIKeyRecord     APIKeyRecord
	Config           *routestrategies.HybridRoutingConfig
	Scoring          HybridScoringResult
	TargetRoute      routestrategies.HybridLevelRoute
	TargetModel      string
	ResponseBodyText string
	TraceID          string
	ClientIP         string
	Endpoint         string
}

// QualityInspectionService mirrors inspectHybridGatewayQuality.
type QualityInspectionService struct {
	clock      Clock
	dispatcher AuxiliaryDispatcher
	recorder   UsageRecorder
	warn       WarnFunc
}

func NewQualityInspectionService(clock Clock, dispatcher AuxiliaryDispatcher, recorder UsageRecorder, warn WarnFunc) *QualityInspectionService {
	if clock == nil {
		clock = time.Now
	}
	return &QualityInspectionService{clock: clock, dispatcher: dispatcher, recorder: recorder, warn: warn}
}

func (service *QualityInspectionService) recordAttempt(ctx context.Context, record ScoringAttemptRecord) error {
	if service.recorder == nil {
		return nil
	}
	return service.recorder.RecordHybridScoringAttempt(ctx, record)
}

// Inspect mirrors inspectHybridGatewayQuality.
func (service *QualityInspectionService) Inspect(ctx context.Context, input QualityInspectInput) HybridQualityInspectionOutcome {
	qualityConfig := input.Config.QualityInspection
	trigger := ShouldTriggerHybridQualityInspection(QualityTriggerInput{
		View:             input.View,
		Config:           input.Config,
		Scoring:          input.Scoring,
		TargetRoute:      input.TargetRoute,
		ResponseBodyText: input.ResponseBodyText,
	})
	if qualityConfig == nil || !qualityConfig.Enabled || !trigger.Triggered {
		reason := trigger.Reason
		if qualityConfig == nil || !qualityConfig.Enabled {
			reason = "quality_inspection_disabled"
		}
		return HybridQualityInspectionOutcome{
			Triggered:     false,
			TriggerReason: reason,
			Pass:          true,
		}
	}

	startedAt := service.clock()
	requestBody := parseHybridQualityRequestBody(input.View)
	contextText := buildHybridQualityContext(input, requestBody, trigger.Reason)
	qualityBody := BuildHybridQualityRequestBody(qualityConfig, contextText)
	rawBody := []byte(NodeJSONStringify(qualityBody))
	success, failure := service.dispatcher.DispatchHybridAuxiliaryChatCompletion(ctx, AuxiliaryDispatchInput{
		Body:                       qualityBody,
		RawBody:                    rawBody,
		APIKeyRecord:               input.APIKeyRecord,
		TargetModel:                qualityConfig.ScoringModel,
		TraceID:                    input.TraceID,
		ClientIP:                   input.ClientIP,
		Endpoint:                   input.Endpoint,
		TrafficSource:              AuxiliaryTrafficSourceHybridQualityScoring,
		TimeoutMs:                  input.Config.ScoringTimeoutMs,
		ResponseMaxBytes:           hybridQualityResponseMaxBytes,
		NoAccountErrorCode:         QualityErrorCodeNoAccount,
		NoAccountErrorMessage:      QualityNoAccountMessage,
		DispatchErrorCode:          QualityErrorCodeDispatch,
		DispatchErrorMessage:       QualityDispatchMessage,
		HTTPErrorCode:              QualityErrorCodeHTTPError,
		ResponseTooLargeMessage:    QualityTooLargeMessage,
		RequestClientCompatibility: "openai_standard",
	})
	if failure != nil {
		if failure.ShouldRecordUsage && failure.Account != nil && failure.HasGroupID {
			service.recordAttempt(ctx, ScoringAttemptRecord{
				TraceID:          input.TraceID,
				ClientIP:         input.ClientIP,
				SystemAccountID:  input.APIKeyRecord.SystemAccountID,
				APIKeyID:         input.APIKeyRecord.ID,
				GroupID:          failure.GroupID,
				Account:          failure.Account,
				Endpoint:         input.Endpoint + "#hybrid-quality-scoring",
				StatusCode:       statusCodePointer(failure.HasStatusCode, failure.StatusCode),
				Success:          false,
				StartedAt:        startedAt,
				ScoringModel:     qualityConfig.ScoringModel,
				Usage:            gatewayproto.EmptyUsage(),
				ErrorCode:        failure.ErrorCode,
				ErrorMessage:     failure.ErrorMessage,
				RequestSnapshot:  ScoringRequestSnapshot{Model: qualityConfig.ScoringModel, ContextBytes: len(contextText)},
				ResponseSnapshot: ScoringResponseSnapshot{StatusCode: statusCodePointer(failure.HasStatusCode, failure.StatusCode)},
				TrafficSource:    AuxiliaryTrafficSourceHybridQualityScoring,
			})
		}
		return qualityInspectionUnavailable(
			failure.ErrorCode,
			failure.ErrorMessage,
			qualityConfig.UnavailableAction,
			failure.Account,
			statusCodePointer(failure.HasStatusCode, failure.StatusCode),
		)
	}

	finish := onceFinish(success.Finish, ctx)
	parsed, parseErr := ParseHybridQualityResponse(success.ParsedResponseBody)
	if parseErr != nil {
		errorMessage := parseErr.Error()
		finish(AuxiliaryDispatchFinishInput{Success: false, ErrorCode: QualityErrorCodeDispatch, ErrorMessage: errorMessage})
		statusCode := success.StatusCode
		service.recordAttempt(ctx, ScoringAttemptRecord{
			TraceID:          input.TraceID,
			ClientIP:         input.ClientIP,
			SystemAccountID:  input.APIKeyRecord.SystemAccountID,
			APIKeyID:         input.APIKeyRecord.ID,
			GroupID:          success.GroupID,
			Account:          &success.Account,
			Endpoint:         input.Endpoint + "#hybrid-quality-scoring",
			StatusCode:       &statusCode,
			Success:          false,
			StartedAt:        startedAt,
			ScoringModel:     qualityConfig.ScoringModel,
			Usage:            success.Usage,
			ErrorCode:        QualityErrorCodeDispatch,
			ErrorMessage:     errorMessage,
			RequestSnapshot:  ScoringRequestSnapshot{Model: qualityConfig.ScoringModel, ContextBytes: len(contextText)},
			ResponseSnapshot: ScoringResponseSnapshot{StatusCode: &statusCode, Body: responseBodySnippet(success.ResponseBody)},
			TrafficSource:    AuxiliaryTrafficSourceHybridQualityScoring,
		})
		return qualityInspectionUnavailable(QualityErrorCodeDispatch, errorMessage, qualityConfig.UnavailableAction, &success.Account, &success.StatusCode)
	}

	actualAction := ResolveHybridQualityAction(parsed, qualityConfig)
	finish(AuxiliaryDispatchFinishInput{Success: true})
	statusCode := success.StatusCode
	if err := service.recordAttempt(ctx, ScoringAttemptRecord{
		TraceID:          input.TraceID,
		ClientIP:         input.ClientIP,
		SystemAccountID:  input.APIKeyRecord.SystemAccountID,
		APIKeyID:         input.APIKeyRecord.ID,
		GroupID:          success.GroupID,
		Account:          &success.Account,
		Endpoint:         input.Endpoint + "#hybrid-quality-scoring",
		StatusCode:       &statusCode,
		Success:          true,
		StartedAt:        startedAt,
		ScoringModel:     qualityConfig.ScoringModel,
		Usage:            success.Usage,
		RequestSnapshot:  ScoringRequestSnapshot{Model: qualityConfig.ScoringModel, ContextBytes: len(contextText)},
		ResponseSnapshot: ScoringResponseSnapshot{StatusCode: &statusCode, Parsed: parsed.asOrderedJSON()},
		TrafficSource:    AuxiliaryTrafficSourceHybridQualityScoring,
	}); err != nil {
		if service.warn != nil {
			service.warn("hybrid_quality_scoring_success_usage_record_failed", "混合路由质量评分已完成，成功使用记录写入失败")
		}
	}
	return HybridQualityInspectionOutcome{
		Triggered:        true,
		TriggerReason:    trigger.Reason,
		Pass:             parsed.Pass,
		Result:           parsed,
		ActualAction:     actualAction,
		QualityAccountID: success.Account.ID,
		StatusCode:       &success.StatusCode,
	}
}

// parseHybridQualityRequestBody mirrors parseHybridQualityRequestBody.
func parseHybridQualityRequestBody(view *GatewayRequestView) *OrderedJSON {
	if body := view.bodyObject(); body != nil {
		return body
	}
	if !view.hasRawBody() || view.rawBodyBytes() > hybridQualityRequestParseMaxBytes {
		return nil
	}
	parsed, err := ParseJSONOrdered(view.RawBody)
	if err != nil {
		return nil
	}
	object, ok := parsed.(*OrderedJSON)
	if !ok {
		return nil
	}
	return object
}

// BuildHybridQualityRequestBody mirrors buildHybridQualityRequestBody.
func BuildHybridQualityRequestBody(config *QualityInspectionConfig, context string) *OrderedJSON {
	systemLines := []string{
		"你是网关响应质量评分器，只判断一个 200 响应是否足以交付。",
		"必须根据原始请求目标、必要约束覆盖度、输出协议、明显遗漏、自相矛盾、可验证性、失败返工成本和安全边界进行抽象判断。",
		"不要按业务领域、技术栈、模块名、关键词、样本题类型或模型名称给固定结论；同一领域请求可能因为约束和风险不同而不同。",
		"只输出 JSON：{\"pass\":布尔值,\"score\":0到100,\"confidence\":0到1,\"failureType\":\"protocol_invalid|missing_required_output|low_quality|unsafe_or_policy|tool_or_schema_mismatch|other\",\"reason\":\"一句话\",\"retryRecommendation\":\"accept|retry_same_model|upgrade_next_level|return_error\"}。",
	}
	systemMessage := NewOrderedJSON()
	systemMessage.Set("role", "system")
	systemMessage.Set("content", strings.Join(systemLines, "\n"))
	userMessage := NewOrderedJSON()
	userMessage.Set("role", "user")
	userMessage.Set("content", context)
	body := NewOrderedJSON()
	body.Set("model", config.ScoringModel)
	body.Set("stream", false)
	body.Set("temperature", float64(0))
	body.Set("max_tokens", 220)
	body.Set("messages", []any{systemMessage, userMessage})
	return body
}

// buildHybridQualityContext mirrors buildHybridQualityContext.
func buildHybridQualityContext(input QualityInspectInput, requestBody *OrderedJSON, triggerReason string) string {
	payload := NewOrderedJSON()
	payload.Set("method", input.View.Method)
	payload.Set("path", input.View.Path)
	if input.View.OriginalModelPresent {
		payload.Set("originalOrCurrentModel", input.View.OriginalModel)
	} else {
		payload.Set("originalOrCurrentModel", Undefined)
	}
	payload.Set("targetModel", input.TargetModel)
	payload.Set("triggerReason", triggerReason)
	routeScoring := NewOrderedJSON()
	routeScoring.Set("level", input.Scoring.Level)
	if input.Scoring.Confidence != nil {
		routeScoring.Set("confidence", *input.Scoring.Confidence)
	} else {
		routeScoring.Set("confidence", Undefined)
	}
	if input.Scoring.Reason != nil {
		routeScoring.Set("reason", *input.Scoring.Reason)
	} else {
		routeScoring.Set("reason", Undefined)
	}
	routeScoring.Set("defaulted", input.Scoring.Defaulted)
	payload.Set("routeScoring", routeScoring)
	if requestBody != nil {
		sanitizer := &qualitySanitizer{}
		payload.Set("request", sanitizer.sanitize(requestBody))
	} else {
		payload.Set("request", qualityRequestBodySummary(input.View))
	}
	payload.Set("response", sanitizeQualityResponseText(input.ResponseBodyText, 16_384))
	text := NodeJSONStringify(payload)
	if len(text) <= hybridQualityContextMaxBytes {
		return text
	}
	payload.Set("request", "[request_omitted_for_quality_context_size]")
	payload.Set("response", sanitizeQualityResponseText(input.ResponseBodyText, 8192))
	payload.Set("truncated", true)
	return NodeJSONStringify(payload)
}

// qualityRequestBodySummary mirrors requestBodySummary.
func qualityRequestBodySummary(view *GatewayRequestView) any {
	state := view.BodyState
	if state == nil {
		return Undefined
	}
	summary := NewOrderedJSON()
	summary.Set("rawBodyBytes", state.RawBodyBytes)
	summary.Set("contentType", state.ContentType)
	summary.Set("jsonParseStatus", state.JSONParseStatus)
	if state.Model != "" {
		summary.Set("model", state.Model)
	} else {
		summary.Set("model", Undefined)
	}
	if state.Stream != nil {
		summary.Set("stream", *state.Stream)
	} else {
		summary.Set("stream", Undefined)
	}
	if state.ImageGeneration != nil {
		summary.Set("imageGeneration", *state.ImageGeneration)
	} else {
		summary.Set("imageGeneration", Undefined)
	}
	if state.ImageGenerationForced != nil {
		summary.Set("imageGenerationForced", *state.ImageGenerationForced)
	} else {
		summary.Set("imageGenerationForced", Undefined)
	}
	return summary
}

// qualitySanitizer mirrors sanitizeQualityValue (no truncated flag, 192KiB
// budget shared across the walk).
type qualitySanitizer struct {
	bytes int
	depth int
}

func (sanitizer *qualitySanitizer) sanitize(value any) any {
	switch value.(type) {
	case nil, bool, float64, int, int64, undefinedType:
		sanitizer.bytes += 8
		return value
	case string:
		return sanitizer.sanitizeString(value.(string))
	}
	if sanitizer.depth >= 8 || sanitizer.bytes >= hybridQualityContextMaxBytes {
		return "[truncated]"
	}
	switch typed := value.(type) {
	case []any:
		output := []any{}
		for index := 0; index < len(typed) && index < 50; index++ {
			sanitizer.depth++
			output = append(output, sanitizer.sanitize(typed[index]))
			sanitizer.depth--
			if sanitizer.bytes >= hybridQualityContextMaxBytes {
				break
			}
		}
		if len(typed) > len(output) {
			output = append(output, "["+strconv.Itoa(len(typed)-len(output))+" items truncated]")
		}
		return output
	case *OrderedJSON:
		output := NewOrderedJSON()
		count := 0
		for _, key := range typed.Keys() {
			item, _ := typed.Get(key)
			if count >= 80 || sanitizer.bytes >= hybridQualityContextMaxBytes {
				output.Set("_truncated", true)
				break
			}
			sanitizer.bytes += len(key)
			sanitizer.depth++
			output.Set(key, sanitizer.sanitize(item))
			sanitizer.depth--
			count++
		}
		return output
	case map[string]any:
		output := NewOrderedJSON()
		count := 0
		for _, key := range sortedMapKeys(typed) {
			if count >= 80 || sanitizer.bytes >= hybridQualityContextMaxBytes {
				output.Set("_truncated", true)
				break
			}
			sanitizer.bytes += len(key)
			sanitizer.depth++
			output.Set(key, sanitizer.sanitize(typed[key]))
			sanitizer.depth--
			count++
		}
		return output
	default:
		return jsonAnyString(value)
	}
}

func (sanitizer *qualitySanitizer) sanitizeString(value string) string {
	maxLength := hybridQualityContextMaxBytes - sanitizer.bytes
	if maxLength > 4096 {
		maxLength = 4096
	}
	if maxLength < 0 {
		maxLength = 0
	}
	if byteLength := len(value); byteLength < maxLength {
		sanitizer.bytes += byteLength
	} else {
		sanitizer.bytes += maxLength
	}
	if utf16Length(value) > maxLength {
		return truncateUTF16(value, maxLength) + "...[truncated]"
	}
	return value
}

// sanitizeQualityResponseText mirrors sanitizeQualityResponseText.
func sanitizeQualityResponseText(value string, maxChars int) string {
	if utf16Length(value) > maxChars {
		return truncateUTF16(value, maxChars) + "...[truncated]"
	}
	return value
}

// ParseHybridQualityResponse mirrors parseHybridQualityResponse. Error
// messages are byte-identical with the Node literals.
func ParseHybridQualityResponse(body NonStreamJSONBody) (*HybridQualityScoreResult, error) {
	if body.Status != "valid" || !IsJSONObject(body.Value) {
		return nil, &HybridError{Message: "质量评分模型未返回合法 JSON"}
	}
	response := body.Value.(*OrderedJSON)
	choice := OrderedChildObjectAtIndex(response, "choices", 0)
	content := OrderedValue(OrderedChild(choice, "message"), "content")
	text := choiceContentText(content)
	jsonText := ExtractJSONObjectText(text)
	if jsonText == "" {
		return nil, &HybridError{Message: "质量评分模型未返回 JSON"}
	}
	parsed, err := ParseJSONOrdered([]byte(jsonText))
	if err != nil {
		return nil, &HybridError{Message: "质量评分模型未返回 JSON"}
	}
	object, ok := parsed.(*OrderedJSON)
	if !ok {
		return nil, &HybridError{Message: "质量评分模型未返回 JSON"}
	}
	pass := false
	if rawPass, ok := object.Get("pass"); ok {
		pass, _ = rawPass.(bool)
	}
	// A missing score is Number(undefined) = NaN (pass?100:0); a present
	// null coerces to 0.
	finalScore := float64(0)
	if pass {
		finalScore = 100
	}
	if rawScore, present := object.Get("score"); present {
		if score, scoreOK := NodeNumber(rawScore); scoreOK && !math.IsNaN(score) && !math.IsInf(score, 0) {
			finalScore = math.Max(0, math.Min(100, score))
		}
	}
	confidence := parseConfidence(object)
	failureType, hasFailureType := normalizeFailureType(OrderedValue(object, "failureType"))
	return &HybridQualityScoreResult{
		Pass:                pass,
		Score:               finalScore,
		Confidence:          confidence,
		FailureType:         failureType,
		HasFailureType:      hasFailureType,
		Reason:              optionalStringPointer(OrderedValue(object, "reason")),
		RetryRecommendation: normalizeRetryRecommendation(OrderedValue(object, "retryRecommendation"), pass),
	}, nil
}

// asOrderedJSON renders the parsed result for the usage-record response
// snapshot (Node stores the parsed object as-is).
func (result *HybridQualityScoreResult) asOrderedJSON() *OrderedJSON {
	object := NewOrderedJSON()
	object.Set("pass", result.Pass)
	object.Set("score", result.Score)
	if result.Confidence != nil {
		object.Set("confidence", *result.Confidence)
	}
	if result.HasFailureType {
		object.Set("failureType", result.FailureType)
	}
	if result.Reason != nil {
		object.Set("reason", *result.Reason)
	}
	object.Set("retryRecommendation", result.RetryRecommendation)
	return object
}

// normalizeFailureType mirrors normalizeFailureType.
func normalizeFailureType(value any) (string, bool) {
	switch value {
	case HybridQualityFailureProtocolInvalid,
		HybridQualityFailureMissingRequiredOutput,
		HybridQualityFailureLowQuality,
		HybridQualityFailureUnsafeOrPolicy,
		HybridQualityFailureToolOrSchemaMismatch,
		HybridQualityFailureOther:
		return value.(string), true
	}
	return "", false
}

// normalizeRetryRecommendation mirrors normalizeRetryRecommendation.
func normalizeRetryRecommendation(value any, pass bool) string {
	if pass {
		return HybridQualityRetryAccept
	}
	switch value {
	case HybridQualityRetrySameModel, HybridQualityRetryUpgradeNextLevel, HybridQualityRetryReturnError:
		return value.(string)
	}
	return HybridQualityRetryUpgradeNextLevel
}

// qualityInspectionUnavailable mirrors qualityInspectionUnavailable.
func qualityInspectionUnavailable(errorCode string, errorMessage string, unavailableAction string, account *OpenAIAccountSecret, statusCode *int) HybridQualityInspectionOutcome {
	passThrough := unavailableAction == "pass_through"
	outcome := HybridQualityInspectionOutcome{
		Triggered:     true,
		TriggerReason: "quality_scoring_unavailable",
		Pass:          passThrough,
		Result: &HybridQualityScoreResult{
			Pass:                false,
			Score:               0,
			Reason:              &errorMessage,
			RetryRecommendation: HybridQualityRetryReturnError,
		},
		ActualAction: HybridQualityRetryReturnError,
		StatusCode:   statusCode,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	}
	if passThrough {
		outcome.ActualAction = HybridQualityActionPassThrough
	}
	if account != nil {
		outcome.QualityAccountID = account.ID
	}
	return outcome
}

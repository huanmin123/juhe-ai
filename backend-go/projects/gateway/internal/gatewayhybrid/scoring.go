package gatewayhybrid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Hybrid request difficulty scoring, mirroring
// backend/src/modules/gateway/hybrid/scoring.service.ts.

// HybridScoringResult mirrors HybridScoringResult.
type HybridScoringResult struct {
	Level            int
	Confidence       *float64
	Factors          []string
	Reason           *string
	Defaulted        bool
	Failed           bool
	CacheHit         bool
	ErrorCode        string
	ErrorMessage     string
	ScoringAccountID string
	ScoringGroupID   string
	StatusCode       *int
}

// HybridScoringCacheEntry mirrors HybridScoringCacheEntry.
type HybridScoringCacheEntry struct {
	Level      int      `json:"level"`
	Confidence *float64 `json:"confidence,omitempty"`
	Factors    []string `json:"factors,omitempty"`
	Reason     *string  `json:"reason,omitempty"`
}

// Scoring limits mirroring the Node constants.
const (
	hybridScoringContextMaxBytes      = 128 * 1024
	hybridScoringRawBodyParseMaxBytes = hybridScoringContextMaxBytes
	hybridScoringResponseMaxBytes     = 2 * 1024 * 1024
	hybridScoringMaxTokens            = 240
	hybridScoringCacheMaxEntries      = 10_000
	hybridScoringCacheMaxTtlMs        = int64(60 * 60 * 1000)
)

// Scoring error codes (mirror the literals passed to the dispatcher).
const (
	ScoringErrorCodeNoAccount = "no_scoring_account"
	ScoringErrorCodeDispatch  = "hybrid_scoring_failed"
	ScoringErrorCodeHTTPError = "hybrid_scoring_http_error"
)

// Scoring error messages (byte-identical with the Node literals).
const (
	ScoringNoAccountMessage  = "混合路由绑定分组池没有可用评分账户"
	ScoringDispatchMessage   = "混合路由评分模型调用失败"
	ScoringTooLargeMessage   = "混合路由评分响应超过保护上限"
	ScoringUnfinishedMessage = "混合路由评分调用未完成收尾"
)

// WarnFunc ports logger.warn for non-fatal side-effect failures.
type WarnFunc func(event, message string)

// hybridLRUCache mirrors createAppCache (lru-cache, updateAgeOnGet=false):
// TTL fixed at set time, LRU recency moved on get, max-entries eviction.
type hybridLRUCache struct {
	mu      sync.Mutex
	max     int
	entries map[string]hybridLRUEntry
	order   []string // front = least recent
}

type hybridLRUEntry struct {
	value     HybridScoringCacheEntry
	expiresAt time.Time
}

func newHybridLRUCache(max int) *hybridLRUCache {
	return &hybridLRUCache{max: max, entries: map[string]hybridLRUEntry{}}
}

func (cache *hybridLRUCache) get(key string, now time.Time) (HybridScoringCacheEntry, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok {
		return HybridScoringCacheEntry{}, false
	}
	if !entry.expiresAt.After(now) {
		cache.removeLocked(key)
		return HybridScoringCacheEntry{}, false
	}
	cache.touchLocked(key)
	return entry.value, true
}

func (cache *hybridLRUCache) set(key string, value HybridScoringCacheEntry, ttl time.Duration, now time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = map[string]hybridLRUEntry{}
	}
	cache.entries[key] = hybridLRUEntry{value: value, expiresAt: now.Add(ttl)}
	cache.touchLocked(key)
	for len(cache.entries) > cache.max && len(cache.order) > 0 {
		cache.removeLocked(cache.order[0])
	}
}

func (cache *hybridLRUCache) touchLocked(key string) {
	for index, candidate := range cache.order {
		if candidate == key {
			cache.order = append(cache.order[:index], cache.order[index+1:]...)
			break
		}
	}
	cache.order = append(cache.order, key)
}

func (cache *hybridLRUCache) removeLocked(key string) {
	delete(cache.entries, key)
	for index, candidate := range cache.order {
		if candidate == key {
			cache.order = append(cache.order[:index], cache.order[index+1:]...)
			return
		}
	}
}

func (cache *hybridLRUCache) clear() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.entries = map[string]hybridLRUEntry{}
	cache.order = nil
}

// ScoringService mirrors scoreHybridGatewayRequest and its module caches.
type ScoringService struct {
	clock      Clock
	dispatcher AuxiliaryDispatcher
	recorder   UsageRecorder
	shared     SharedJSONCache // nil = memory-only cache driver
	warn       WarnFunc

	lru *hybridLRUCache
}

// NewScoringService builds the scoring service. shared nil = memory-only
// cache driver (Node runtimeConfig.cacheDriver !== 'redis').
func NewScoringService(clock Clock, dispatcher AuxiliaryDispatcher, recorder UsageRecorder, shared SharedJSONCache, warn WarnFunc) *ScoringService {
	if clock == nil {
		clock = time.Now
	}
	return &ScoringService{
		clock:      clock,
		dispatcher: dispatcher,
		recorder:   recorder,
		shared:     shared,
		warn:       warn,
		lru:        newHybridLRUCache(hybridScoringCacheMaxEntries),
	}
}

// ClearCacheForTest mirrors clearHybridScoringCacheForTest.
func (service *ScoringService) ClearCacheForTest() {
	service.lru.clear()
}

// ScoreInput mirrors the scoreHybridGatewayRequest input.
type ScoreInput struct {
	View         *GatewayRequestView
	APIKeyRecord APIKeyRecord
	Config       *routestrategies.HybridRoutingConfig
	TraceID      string
	ClientIP     string
	Endpoint     string
}

// Score mirrors scoreHybridGatewayRequest. Failures degrade to failed
// scoring results exactly like the Node try/catch contract.
func (service *ScoringService) Score(ctx context.Context, input ScoreInput) HybridScoringResult {
	startedAt := service.clock()
	body := parseHybridRequestBody(input.View)
	contextText := buildHybridScoringContext(input.View, body)
	cacheKey := buildHybridScoringCacheKey(input.View, input.APIKeyRecord, input.Config, input.Endpoint, contextText)

	if cacheKey != "" {
		if cached, ok := service.lru.get(cacheKey, service.clock()); ok {
			return hybridScoringCacheHitResult(cached)
		}
	}
	if cacheKey != "" && service.shared != nil {
		sharedCached, err := service.shared.Get(ctx, cacheKey)
		if err == nil && sharedCached != nil {
			service.lru.set(cacheKey, *sharedCached, hybridScoringCacheTTL(input.Config.ScoringCacheTTLSeconds), service.clock())
			return hybridScoringCacheHitResult(*sharedCached)
		}
	}

	scoringBody := BuildHybridScoringRequestBody(input.Config.ScoringModel, contextText)
	rawBody := []byte(NodeJSONStringify(scoringBody))
	success, failure := service.dispatcher.DispatchHybridAuxiliaryChatCompletion(ctx, AuxiliaryDispatchInput{
		Body:                       scoringBody,
		RawBody:                    rawBody,
		APIKeyRecord:               input.APIKeyRecord,
		TargetModel:                input.Config.ScoringModel,
		TraceID:                    input.TraceID,
		ClientIP:                   input.ClientIP,
		Endpoint:                   input.Endpoint,
		TrafficSource:              AuxiliaryTrafficSourceHybridScoring,
		TimeoutMs:                  input.Config.ScoringTimeoutMs,
		ResponseMaxBytes:           hybridScoringResponseMaxBytes,
		NoAccountErrorCode:         ScoringErrorCodeNoAccount,
		NoAccountErrorMessage:      ScoringNoAccountMessage,
		DispatchErrorCode:          ScoringErrorCodeDispatch,
		DispatchErrorMessage:       ScoringDispatchMessage,
		HTTPErrorCode:              ScoringErrorCodeHTTPError,
		ResponseTooLargeMessage:    ScoringTooLargeMessage,
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
				Endpoint:         input.Endpoint + "#hybrid-scoring",
				StatusCode:       statusCodePointer(failure.HasStatusCode, failure.StatusCode),
				Success:          false,
				StartedAt:        startedAt,
				ScoringModel:     input.Config.ScoringModel,
				Usage:            gatewayproto.EmptyUsage(),
				ErrorCode:        failure.ErrorCode,
				ErrorMessage:     failure.ErrorMessage,
				RequestSnapshot:  ScoringRequestSnapshot{Model: input.Config.ScoringModel, ContextBytes: len(contextText)},
				ResponseSnapshot: ScoringResponseSnapshot{StatusCode: statusCodePointer(failure.HasStatusCode, failure.StatusCode)},
			})
		}
		return failedScoringResult(input.Config, failure.ErrorCode, failure.ErrorMessage, failure.Account, statusCodePointer(failure.HasStatusCode, failure.StatusCode))
	}

	finish := onceFinish(success.Finish, ctx)
	parsed, parseErr := ParseHybridScoringResponse(success.ParsedResponseBody)
	if parseErr != nil {
		errorMessage := parseErr.Error()
		finish(AuxiliaryDispatchFinishInput{Success: false, ErrorCode: ScoringErrorCodeDispatch, ErrorMessage: errorMessage})
		statusCode := success.StatusCode
		service.recordAttempt(ctx, ScoringAttemptRecord{
			TraceID:          input.TraceID,
			ClientIP:         input.ClientIP,
			SystemAccountID:  input.APIKeyRecord.SystemAccountID,
			APIKeyID:         input.APIKeyRecord.ID,
			GroupID:          success.GroupID,
			Account:          &success.Account,
			Endpoint:         input.Endpoint + "#hybrid-scoring",
			StatusCode:       &statusCode,
			Success:          false,
			StartedAt:        startedAt,
			ScoringModel:     input.Config.ScoringModel,
			Usage:            success.Usage,
			ErrorCode:        ScoringErrorCodeDispatch,
			ErrorMessage:     errorMessage,
			RequestSnapshot:  ScoringRequestSnapshot{Model: input.Config.ScoringModel, ContextBytes: len(contextText)},
			ResponseSnapshot: ScoringResponseSnapshot{StatusCode: &statusCode, Body: responseBodySnippet(success.ResponseBody)},
		})
		return failedScoringResult(input.Config, ScoringErrorCodeDispatch, errorMessage, &success.Account, &success.StatusCode)
	}

	scoringResult := HybridScoringResult{
		Level:            clampLevelFromAny(parsed.level),
		Confidence:       parsed.confidence,
		Factors:          parsed.factors,
		Reason:           parsed.reason,
		Defaulted:        false,
		CacheHit:         false,
		ScoringAccountID: success.Account.ID,
		ScoringGroupID:   success.GroupID,
		StatusCode:       &success.StatusCode,
	}
	finish(AuxiliaryDispatchFinishInput{Success: true})
	statusCode := success.StatusCode
	if err := service.recordAttempt(ctx, ScoringAttemptRecord{
		TraceID:          input.TraceID,
		ClientIP:         input.ClientIP,
		SystemAccountID:  input.APIKeyRecord.SystemAccountID,
		APIKeyID:         input.APIKeyRecord.ID,
		GroupID:          success.GroupID,
		Account:          &success.Account,
		Endpoint:         input.Endpoint + "#hybrid-scoring",
		StatusCode:       &statusCode,
		Success:          true,
		StartedAt:        startedAt,
		ScoringModel:     input.Config.ScoringModel,
		Usage:            success.Usage,
		RequestSnapshot:  ScoringRequestSnapshot{Model: input.Config.ScoringModel, ContextBytes: len(contextText)},
		ResponseSnapshot: ScoringResponseSnapshot{StatusCode: &statusCode, Parsed: parsed.asOrderedJSON()},
	}); err != nil {
		service.warnNonFatal("hybrid_scoring_success_usage_record_failed", "混合路由评分已完成，成功使用记录写入失败")
	}
	service.rememberScoringCacheResult(cacheKey, scoringResult, input.Config.ScoringCacheTTLSeconds)
	return scoringResult
}

// onceFinish mirrors the dispatchFinished guard around dispatch.finish.
func onceFinish(finish func(ctx context.Context, finish AuxiliaryDispatchFinishInput) error, ctx context.Context) func(AuxiliaryDispatchFinishInput) {
	var once sync.Once
	return func(input AuxiliaryDispatchFinishInput) {
		once.Do(func() {
			_ = finish(ctx, input)
		})
	}
}

func (service *ScoringService) recordAttempt(ctx context.Context, record ScoringAttemptRecord) error {
	if service.recorder == nil {
		return nil
	}
	return service.recorder.RecordHybridScoringAttempt(ctx, record)
}

func (service *ScoringService) warnNonFatal(event, message string) {
	if service.warn != nil {
		service.warn(event, message)
	}
}

func statusCodePointer(has bool, value int) *int {
	if !has {
		return nil
	}
	return &value
}

// rememberScoringCacheResult mirrors rememberHybridScoringCacheResult.
func (service *ScoringService) rememberScoringCacheResult(key string, result HybridScoringResult, ttlSeconds int) {
	if key == "" || result.Defaulted || result.CacheHit {
		return
	}
	entry := HybridScoringCacheEntry{
		Level:      result.Level,
		Confidence: result.Confidence,
		Factors:    result.Factors,
		Reason:     result.Reason,
	}
	service.lru.set(key, entry, hybridScoringCacheTTL(ttlSeconds), service.clock())
	if service.shared == nil {
		return
	}
	sharedEntry := HybridScoringCacheEntry{
		Level:      entry.Level,
		Confidence: entry.Confidence,
		Factors:    entry.Factors,
	}
	if err := service.shared.Set(context.Background(), key, sharedEntry, hybridScoringCacheTTLMs(ttlSeconds)); err != nil {
		service.warnNonFatal("hybrid_scoring_success_cache_write_failed", "混合路由评分已完成，结果缓存写入失败")
	}
}

// hybridScoringCacheTTLMs mirrors hybridScoringCacheTtlMs.
func hybridScoringCacheTTLMs(ttlSeconds int) int64 {
	scaled := int64(ttlSeconds) * 1000
	if scaled < 1 {
		scaled = 1
	}
	if scaled > hybridScoringCacheMaxTtlMs {
		scaled = hybridScoringCacheMaxTtlMs
	}
	return scaled
}

func hybridScoringCacheTTL(ttlSeconds int) time.Duration {
	return time.Duration(hybridScoringCacheTTLMs(ttlSeconds)) * time.Millisecond
}

func hybridScoringCacheHitResult(entry HybridScoringCacheEntry) HybridScoringResult {
	return HybridScoringResult{
		Level:      entry.Level,
		Confidence: entry.Confidence,
		Factors:    entry.Factors,
		Reason:     entry.Reason,
		Defaulted:  false,
		CacheHit:   true,
	}
}

func failedScoringResult(config *routestrategies.HybridRoutingConfig, errorCode string, errorMessage string, account *OpenAIAccountSecret, statusCode *int) HybridScoringResult {
	result := HybridScoringResult{
		Level:        config.ScoringFallbackMaxLevel,
		Defaulted:    false,
		Failed:       true,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
		StatusCode:   statusCode,
	}
	if account != nil {
		result.ScoringAccountID = account.ID
	}
	return result
}

// parseHybridRequestBody mirrors parseHybridRequestBody: object body →
// bounded raw-body parse; nil when unavailable or oversized.
func parseHybridRequestBody(view *GatewayRequestView) *OrderedJSON {
	if body := view.bodyObject(); body != nil {
		return body
	}
	if !view.hasRawBody() {
		return nil
	}
	if view.rawBodyBytes() > hybridScoringRawBodyParseMaxBytes {
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

// BuildHybridScoringRequestBody mirrors buildHybridScoringRequestBody: the
// difficulty scorer system prompt is byte-identical with the Node literal.
func BuildHybridScoringRequestBody(model string, context string) *OrderedJSON {
	systemLines := []string{
		"你是网关请求难度评分器，只负责给当前请求打一个 1 到 10 的绝对难度等级，用于后续成本路由。",
		"你只评估本次请求本身的难度、质量风险和返工成本，评分必须是客观判断。",
		"不要考虑任何模型名称、模型价格、供应商、用户配置的档位范围或最终会路由到哪个模型。",
		"不要考虑省钱偏好、质量偏好或任何用户路由策略；这些只属于路由层，不属于难度评分。",
		"不要假设 1 到 10 会被如何分组；档位范围由用户另行配置，与你无关。",
		"不要按固定关键词、业务领域、技术栈、文件名、任务名称或题型机械分级；同一类请求可能因为上下文、风险、约束和质量要求不同而得到完全不同的等级。",
		"评分尺度：1 表示极低难度，目标明确、上下文很少、影响局部、几乎无依赖、失败容易发现且容易修正。",
		"评分尺度：5 表示中等难度，有多个约束或一定上下文，需要结构化处理、保持局部一致性，失败会带来一定返工。",
		"评分尺度：10 表示最高难度，上下文复杂、多模块或多轮依赖强，需要高可靠推理或最终质量把关，失败代价很高。",
		"不要为了覆盖完整空间而强行拉高或拉低；但当请求风险明显不同，应敢于使用更高或更低等级。",
		"评分时综合判断：目标明确度、上下文跨度、依赖范围、约束数量、约束相互影响、跨文件或跨步骤一致性、严格输出格式、规划或架构判断、复杂推理、风险权衡、失败可发现性、可回滚性和是否会污染后续步骤。",
		"如果上下文显示被截断、信息缺失或关键依赖不可见，应把不确定性和潜在返工风险计入评分。",
		"上下文少只代表上下文处理成本低，不等于请求本身一定低难度；如果本次请求需要多步精确推理、优化选择、证明、组合约束、边界条件处理或严格正确性，应按真实推理难度提高等级。",
		"可验证性只降低发现错误的成本，不等于降低完成难度；如果错误会导致结果不可用、需要重新生成或污染后续调用，仍应计入返工成本。",
		"等级不是固定题型或固定范围映射；只能根据当前请求实际暴露的信息动态判断。直接作答、局部执行、状态变化、候选权衡、系统化比较、性能要求、质量要求、跨上下文一致性和失败代价都只是影响因素，不是硬编码规则。",
		"如果请求产物会被后续系统、测试、用户流程、业务决策或其他调用直接使用，要把可运行性、边界条件、异常路径、性能约束、输入不变性、协议语义、可维护性和后续修复成本纳入整体判断；只有当失败会影响后续流程、造成错误传播、需要较大返工或难以局部修复时，才显著提高等级。",
		"如果请求明确包含上一次失败、验收失败、缺文件、输出协议不合格或修复要求，要把真实返工风险纳入评分；但不能因为出现“修复”等字样机械给高等级。",
		"如果已有清晰计划且本次只是低风险局部执行，可以降低等级；但如果本次执行本身仍有复杂推理、严格质量要求或高返工风险，不应仅因已有计划而降级。",
		"如果是累计接入、修复失败、跨文件一致性、最终验收或高返工成本，应提高等级。",
		"只输出你对本次请求的绝对难度等级。",
		"level 必须是 1 到 10 的整数；confidence 表示你对本次评分的把握；reason 必须说明本次请求的具体依据，不要写泛泛的任务类别。",
		"factors 必须是 1 到 5 个短标签，只写本次评分最关键的因素，例如上下文跨度、约束耦合、严格格式、多步推理、性能要求、后续污染、返工成本、最终验收等；不要写模型名或价格。",
		"只输出 JSON：{\"level\":数字,\"confidence\":0到1,\"reason\":\"一句话\",\"factors\":[\"短标签\"]}。",
	}
	systemMessage := NewOrderedJSON()
	systemMessage.Set("role", "system")
	systemMessage.Set("content", strings.Join(systemLines, "\n"))
	userMessage := NewOrderedJSON()
	userMessage.Set("role", "user")
	userMessage.Set("content", context)
	body := NewOrderedJSON()
	body.Set("model", model)
	body.Set("stream", false)
	body.Set("temperature", float64(0))
	body.Set("max_tokens", hybridScoringMaxTokens)
	body.Set("messages", []any{systemMessage, userMessage})
	return body
}

// buildHybridScoringContext mirrors buildHybridScoringContext.
func buildHybridScoringContext(view *GatewayRequestView, body *OrderedJSON) string {
	payload := NewOrderedJSON()
	payload.Set("method", view.Method)
	payload.Set("path", view.Path)
	if view.OriginalModelPresent {
		payload.Set("originalModel", view.OriginalModel)
	} else {
		payload.Set("originalModel", Undefined)
	}
	payload.Set("body", hybridScoringBodyContext(view, body))
	text := NodeJSONStringify(payload)
	if len(text) <= hybridScoringContextMaxBytes {
		return text
	}
	payload.Set("body", "[request_too_large_for_full_scoring_context]")
	payload.Set("rawBodyBytes", view.rawBodyBytes())
	payload.Set("truncated", true)
	return NodeJSONStringify(payload)
}

func hybridScoringBodyContext(view *GatewayRequestView, body *OrderedJSON) any {
	if body != nil {
		sanitizer := &scoringSanitizer{}
		return sanitizer.sanitize(body)
	}
	state := view.BodyState
	if state == nil || state.RawBodyBytes <= 0 {
		return nil
	}
	gatewayBody := NewOrderedJSON()
	gatewayBody.Set("rawBodyBytes", state.RawBodyBytes)
	gatewayBody.Set("contentType", state.ContentType)
	gatewayBody.Set("jsonParseStatus", state.JSONParseStatus)
	if state.Model != "" {
		gatewayBody.Set("model", state.Model)
	} else {
		gatewayBody.Set("model", Undefined)
	}
	if state.Stream != nil {
		gatewayBody.Set("stream", *state.Stream)
	} else {
		gatewayBody.Set("stream", Undefined)
	}
	if state.ImageGeneration != nil {
		gatewayBody.Set("imageGeneration", *state.ImageGeneration)
	} else {
		gatewayBody.Set("imageGeneration", Undefined)
	}
	if state.ImageGenerationForced != nil {
		gatewayBody.Set("imageGenerationForced", *state.ImageGenerationForced)
	} else {
		gatewayBody.Set("imageGenerationForced", Undefined)
	}
	if state.RawBodyBytes > int64(hybridScoringRawBodyParseMaxBytes) {
		gatewayBody.Set("omittedReason", "raw_body_exceeds_hybrid_scoring_parse_limit")
	} else {
		gatewayBody.Set("omittedReason", "body_not_available")
	}
	wrapper := NewOrderedJSON()
	wrapper.Set("_gatewayBody", gatewayBody)
	return wrapper
}

// scoringSanitizer mirrors sanitizeScoringValue with its byte budget.
type scoringSanitizer struct {
	bytes     int
	truncated bool
	depth     int
}

func (sanitizer *scoringSanitizer) sanitize(value any) any {
	switch value.(type) {
	case nil, bool, float64, int, int64, undefinedType:
		sanitizer.bytes += 8
		return value
	case string:
		return sanitizer.sanitizeString(value.(string))
	}
	if sanitizer.depth >= 8 || sanitizer.bytes >= hybridScoringContextMaxBytes {
		sanitizer.truncated = true
		return "[truncated]"
	}
	switch typed := value.(type) {
	case []any:
		output := []any{}
		for index := 0; index < len(typed) && index < 50; index++ {
			sanitizer.depth++
			output = append(output, sanitizer.sanitize(typed[index]))
			sanitizer.depth--
			if sanitizer.bytes >= hybridScoringContextMaxBytes {
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
			if count >= 80 || sanitizer.bytes >= hybridScoringContextMaxBytes {
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
			if count >= 80 || sanitizer.bytes >= hybridScoringContextMaxBytes {
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

func (sanitizer *scoringSanitizer) sanitizeString(value string) string {
	maxLength := hybridScoringContextMaxBytes - sanitizer.bytes
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

// buildHybridScoringCacheKey mirrors buildHybridScoringCacheKey: sha256 over
// the JSON.stringify'd identity + config fingerprint + request digest set.
func buildHybridScoringCacheKey(view *GatewayRequestView, record APIKeyRecord, config *routestrategies.HybridRoutingConfig, endpoint string, contextText string) string {
	if !config.ScoringCacheEnabled || config.ScoringCacheTTLSeconds <= 0 {
		return ""
	}
	payload := NewOrderedJSON()
	payload.Set("systemAccountId", record.SystemAccountID)
	payload.Set("apiKeyId", record.ID)
	payload.Set("endpoint", endpoint)
	payload.Set("config", hybridScoringConfigFingerprint(config))
	request := NewOrderedJSON()
	request.Set("method", view.Method)
	request.Set("path", view.Path)
	if view.OriginalModelPresent {
		request.Set("originalModel", view.OriginalModel)
	} else {
		request.Set("originalModel", Undefined)
	}
	if view.hasRawBody() {
		request.Set("rawBodyDigest", digestBytes(view.RawBody))
	} else {
		request.Set("rawBodyDigest", Undefined)
	}
	request.Set("contextDigest", digestText(contextText))
	if view.ConversationKey != "" {
		request.Set("conversationKey", view.ConversationKey)
	} else {
		request.Set("conversationKey", Undefined)
	}
	payload.Set("request", request)
	return digestText(NodeJSONStringify(payload))
}

func hybridScoringConfigFingerprint(config *routestrategies.HybridRoutingConfig) *OrderedJSON {
	fingerprint := NewOrderedJSON()
	fingerprint.Set("scoringModel", config.ScoringModel)
	fingerprint.Set("scoringContextMode", config.ScoringContextMode)
	fingerprint.Set("qualityPreference", config.QualityPreference)
	fingerprint.Set("scoringTimeoutMs", config.ScoringTimeoutMs)
	fingerprint.Set("scoringFallbackMaxLevel", config.ScoringFallbackMaxLevel)
	fingerprint.Set("cacheAffinityEnabled", config.CacheAffinityEnabled)
	fingerprint.Set("affinityTtlSeconds", config.AffinityTTLSeconds)
	fingerprint.Set("switchMinLevelDelta", config.SwitchMinLevelDelta)
	fingerprint.Set("downgradeConsecutiveLowCount", config.DowngradeConsecutiveLowCount)
	routes := make([]any, 0, len(config.LevelRoutes))
	for _, route := range config.LevelRoutes {
		entry := NewOrderedJSON()
		entry.Set("minLevel", route.MinLevel)
		entry.Set("maxLevel", route.MaxLevel)
		entry.Set("targetModel", route.TargetModel)
		entry.Set("enabled", route.Enabled)
		routes = append(routes, entry)
	}
	fingerprint.Set("levelRoutes", routes)
	return fingerprint
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// parsedScoringPayload carries ParseHybridScoringResponse output; level keeps
// the raw parsed value because clampHybridLevel re-coerces it in Node.
type parsedScoringPayload struct {
	level      any
	confidence *float64
	factors    []string
	reason     *string
}

func (payload *parsedScoringPayload) asOrderedJSON() *OrderedJSON {
	object := NewOrderedJSON()
	object.Set("level", payload.level)
	if payload.confidence != nil {
		object.Set("confidence", *payload.confidence)
	}
	if payload.factors != nil {
		factors := make([]any, len(payload.factors))
		for index, factor := range payload.factors {
			factors[index] = factor
		}
		object.Set("factors", factors)
	}
	if payload.reason != nil {
		object.Set("reason", *payload.reason)
	}
	return object
}

// ParseHybridScoringResponse mirrors parseHybridScoringResponse. Error
// messages are byte-identical with the Node literals.
func ParseHybridScoringResponse(body NonStreamJSONBody) (*parsedScoringPayload, error) {
	if body.Status != "valid" || !IsJSONObject(body.Value) {
		return nil, &HybridError{Message: "混合评分模型未返回合法 JSON"}
	}
	response := body.Value.(*OrderedJSON)
	choice := OrderedChildObjectAtIndex(response, "choices", 0)
	message := OrderedChild(choice, "message")
	content := OrderedValue(message, "content")
	text := choiceContentText(content)
	jsonText := ExtractJSONObjectText(text)
	if jsonText == "" {
		if reasoning := OrderedString(message, "reasoning_content"); strings.TrimSpace(reasoning) != "" {
			if OrderedString(choice, "finish_reason") == "length" {
				return nil, &HybridError{Message: "评分模型只返回思考内容且达到输出上限，未产生 JSON"}
			}
			return nil, &HybridError{Message: "评分模型只返回思考内容，未产生 JSON"}
		}
		return nil, &HybridError{Message: "评分模型未返回 JSON"}
	}
	parsed, err := ParseJSONOrdered([]byte(jsonText))
	if err != nil {
		return nil, &HybridError{Message: "评分模型返回的 level 无效"}
	}
	object, ok := parsed.(*OrderedJSON)
	if !ok {
		return nil, &HybridError{Message: "评分模型返回的 level 无效"}
	}
	levelValue := OrderedValueOrUndefined(object, "level")
	level, levelOK := NodeNumber(levelValue)
	if !levelOK || math.IsNaN(level) || math.IsInf(level, 0) {
		return nil, &HybridError{Message: "评分模型返回的 level 无效"}
	}
	return &parsedScoringPayload{
		level:      levelValue,
		confidence: parseConfidence(object),
		factors:    parseHybridScoringFactors(OrderedValue(object, "factors")),
		reason:     optionalStringPointer(OrderedValue(object, "reason")),
	}, nil
}

// choiceContentText mirrors the content coercion: string passes through,
// arrays join their items (JSON.stringify for non-strings) with newline.
func choiceContentText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				parts = append(parts, text)
			} else {
				parts = append(parts, NodeJSONStringify(item))
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// parseConfidence mirrors `Number.isFinite(Number(parsed.confidence))` with
// the undefined contract: a missing key is NaN (undefined result), a present
// null coerces to 0.
func parseConfidence(object *OrderedJSON) *float64 {
	value, present := object.Get("confidence")
	if !present {
		return nil
	}
	number, ok := NodeNumber(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil
	}
	clamped := math.Max(0, math.Min(1, number))
	return &clamped
}

// parseHybridScoringFactors mirrors parseHybridScoringFactors: trim, drop
// empties, cap 40 chars (UTF-16), dedupe preserving order, cap 5.
func parseHybridScoringFactors(value any) []string {
	array, ok := value.([]any)
	if !ok {
		return nil
	}
	factors := make([]string, 0, len(array))
	seen := map[string]bool{}
	for _, item := range array {
		text, ok := item.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		capped := truncateUTF16(trimmed, 40)
		if seen[capped] {
			continue
		}
		seen[capped] = true
		factors = append(factors, capped)
	}
	if len(factors) == 0 {
		return nil
	}
	if len(factors) > 5 {
		factors = factors[:5]
	}
	return factors
}

var fencedJSONPattern = regexp.MustCompile("(?i)```(?:json)?\\s*([\\s\\S]*?)```")

// ExtractJSONObjectText mirrors extractJsonObjectText.
func ExtractJSONObjectText(text string) string {
	fenced := ""
	if match := fencedJSONPattern.FindStringSubmatch(text); match != nil {
		fenced = strings.TrimSpace(match[1])
	}
	source := fenced
	if source == "" {
		source = strings.TrimSpace(text)
	}
	if strings.HasPrefix(source, "{") && strings.HasSuffix(source, "}") {
		return source
	}
	start := strings.Index(source, "{")
	end := strings.LastIndex(source, "}")
	if start >= 0 && end > start {
		return source[start : end+1]
	}
	return ""
}

func responseBodySnippet(body []byte) string {
	if len(body) > 2048 {
		return string(body[:2048])
	}
	return string(body)
}

func sortedMapKeys(source map[string]any) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func jsonAnyString(value any) string {
	return NodeJSONStringify(value)
}

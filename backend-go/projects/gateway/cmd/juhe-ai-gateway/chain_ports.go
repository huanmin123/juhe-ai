package main

// Composition-root port adapters for the /v1 chain: the small bridges between
// the frozen orchestration ports (gatewaypreauth / gatewaydispatch /
// gatewayresponse / gatewayusage) and their Go owner packages, plus the
// explicit disabled implementations for the runtime collaborators whose
// production service is not injected. Each disabled port logs one line on
// first use — degraded wiring is observable, never silent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// ---------------------------------------------------------------------------
// hybrid collaborator aliases
// ---------------------------------------------------------------------------

type hybridSharedJSONCache = gatewayhybrid.SharedJSONCache
type hybridRuntimeStateStore = gatewayhybrid.RuntimeStateStore
type hybridAuxiliaryDispatcher = gatewayhybrid.AuxiliaryDispatcher
type hybridUsageRecorder = gatewayhybrid.UsageRecorder
type hybridRouteDiagnostics = gatewayhybrid.RouteDiagnosticsPublisher

// hybridSessionIdentityPort degrades the hybrid affinity identity: without
// the G14 session identity service bound into the hybrid core the
// conversation key is unknown, which mirrors the Node no-identity branch.
type hybridSessionIdentityPort struct{}

func (hybridSessionIdentityPort) HybridRouteAffinityKey(_ *gatewayhybrid.GatewayRequestView, _ gatewayhybrid.AffinityKeyScope) string {
	return ""
}

// hybridTargetGroups implements gatewayhybrid.TargetGroupSelector over the
// routing runtime cache bridge (selectGatewayModelTargetGroup).
type hybridTargetGroups struct {
	cache *gatewayruntimecache.Service
}

func (s hybridTargetGroups) SelectTargetGroup(ctx context.Context, input gatewayhybrid.TargetGroupSelectorInput) (*gatewayhybrid.TargetGroupSelection, error) {
	if s.cache == nil || input.APIKeyRecord.SelectedGroupID == "" {
		return nil, nil
	}
	groupAccess, err := s.cache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, input.APIKeyRecord.SelectedGroupID, input.APIKeyRecord.SystemAccountID)
	if err != nil {
		return nil, err
	}
	if groupAccess == nil {
		return nil, nil
	}
	accounts, err := s.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, input.APIKeyRecord.SelectedGroupID, input.APIKeyRecord.SystemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
		RequestedModel: input.TargetModel,
	})
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, nil
	}
	selection := &gatewayhybrid.TargetGroupSelection{
		GroupID:                    input.APIKeyRecord.SelectedGroupID,
		GroupAccess:                gatewayhybrid.GroupUsageAccessMetadata{ProviderCode: groupAccess.ProviderCode},
		ResponseInspectionPolicies: []gatewayhybrid.ResponseInspectionPolicySummary{},
	}
	for _, account := range accounts {
		selection.Accounts = append(selection.Accounts, gatewayhybrid.OpenAIAccountSecret{ID: account.ID})
	}
	return selection, nil
}

// ---------------------------------------------------------------------------
// dispatch engine adapters
// ---------------------------------------------------------------------------

// usageAttemptRecorderAdapter implements gatewaydispatch.UsageAttemptRecorder
// over the gatewayusage service (records.ts recordFailedUpstreamAttempt).
type usageAttemptRecorderAdapter struct {
	service *gatewayusage.Service
}

func (a usageAttemptRecorderAdapter) RecordFailedUpstreamAttempt(ctx context.Context, req *gatewaypreauth.GatewayRequest, usageContext gatewaypreauth.GatewayFailureUsageContext, account gatewaydispatch.AccountCandidate, record gatewaydispatch.FailedAttemptRecord) error {
	if a.service == nil {
		return nil
	}
	return a.service.RecordFailedUpstreamAttempt(ctx, usageContextOf(usageContext), usageModelAccountOf(account), gatewayusage.RecordFailedUpstreamAttemptInput{
		Model:                      requestModelHintOf(req),
		UpstreamURL:                record.UpstreamURL,
		StartedAtMs:                record.StartedAt,
		StatusCode:                 attemptStatusCodeOf(record),
		BodyText:                   record.BodyText,
		ErrorMessage:               record.ErrorMessage,
		FailureAttribution:         gatewayusage.UsageFailureAttribution(record.FailureAttribution),
		InterpretUpstreamSemantics: record.InterpretUpstreamSemantics,
	})
}

func attemptStatusCodeOf(record gatewaydispatch.FailedAttemptRecord) *int {
	if !record.HasStatusCode {
		return nil
	}
	status := record.StatusCode
	return &status
}

// usageFailureContextOf converts the frozen failure context into the usage
// service failure context.
func usageFailureContextOf(context gatewaypreauth.GatewayFailureUsageContext) gatewayusage.GatewayFailureUsageContext {
	return gatewayusage.GatewayFailureUsageContext{
		GatewayUsageContext:            usageContextOf(context),
		ProviderCode:                   context.ProviderCode,
		ProviderProtocolProfileID:      context.ProviderProtocolProfileID,
		ProtocolCode:                   context.ProtocolCode,
		ProtocolVersion:                context.ProtocolVersion,
		GroupOwnerSystemAccountID:      context.GroupOwnerSystemAccountID,
		GroupAccessType:                context.GroupAccessType,
		GroupAuthorizationID:           context.GroupAuthorizationID,
		GroupAuthorizationSourceType:   context.GroupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID: context.GroupAuthorizationSourceTeamID,
	}
}

// usageContextOf projects the frozen failure usage context onto the usage
// service context.
func usageContextOf(context gatewaypreauth.GatewayFailureUsageContext) gatewayusage.GatewayUsageContext {
	return gatewayusage.GatewayUsageContext{
		TraceID:                  context.TraceID,
		TrafficSource:            gatewayusage.OpenAIGatewayTrafficSource(context.TrafficSource),
		ClientIP:                 context.ClientIP,
		SystemAccountID:          context.SystemAccountID,
		APIKeyID:                 context.APIKeyID,
		GroupID:                  context.GroupID,
		Endpoint:                 context.Endpoint,
		RequestedServiceTier:     context.RequestedServiceTier,
		EffectiveServiceTier:     context.EffectiveServiceTier,
		RequestedReasoningEffort: context.RequestedReasoningEffort,
		EffectiveReasoningEffort: context.EffectiveReasoningEffort,
	}
}

// usageDispatchAdapter implements the gatewayresponse usage ports over the
// usage service + recorder (models fast-path dispatchUsageRecord +
// recordGatewayFailure). The dispatch record lands through the same spooled
// recorder the engine failures use (G17 finalization pipeline).
type usageDispatchAdapter struct {
	service  *gatewayusage.Service
	recorder gatewayusage.UsageRecorder
}

// DispatchUsageRecord mirrors dispatchUsageRecord: the finalized aggregate
// becomes one durable usage record. The frozen
// gatewayresponse.ModelsUsageDispatchInput carries the identity context and
// the result metrics; the token/cost accounting rides on the response
// snapshot captured by the audit pipeline (registered takeover point until
// the G17 pricing slice mounts).
func (a usageDispatchAdapter) DispatchUsageRecord(input gatewayresponse.ModelsUsageDispatchInput) {
	if a.recorder == nil {
		return
	}
	record := gatewayusage.UsageRecordInput{
		TraceID:         input.UsageContext.TraceID,
		TrafficSource:   gatewayusage.OpenAIGatewayTrafficSource(input.UsageContext.TrafficSource),
		ClientIP:        input.UsageContext.ClientIP,
		SystemAccountID: input.UsageContext.SystemAccountID,
		APIKeyID:        input.UsageContext.APIKeyID,
		GroupID:         input.UsageContext.GroupID,
		ProviderCode:    input.ProviderCode,
		Endpoint:        input.UsageContext.Endpoint,
		UsageSemantic:   input.UsageSemantic,
		Success:         input.Success,
		CreatedAt:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	stream := input.Stream
	record.Stream = &stream
	statusCode := input.StatusCode
	record.StatusCode = &statusCode
	firstTokenMs := int(input.FirstTokenMs)
	record.FirstTokenMs = &firstTokenMs
	durationMs := int(input.DurationMs)
	record.DurationMs = &durationMs
	_ = a.recorder.EnqueueUsageRecord(context.Background(), record)
}

// RecordGatewayFailure implements gatewayresponse.FailureUsageRecorder.
func (a usageDispatchAdapter) RecordGatewayFailure(input gatewayresponse.FailureUsageRecordInput) {
	if a.service == nil {
		return
	}
	var responsePayload any
	if input.ResponsePayload.Error != nil || len(input.ResponsePayload.Extra) > 0 {
		merged := map[string]any{}
		if input.ResponsePayload.Error != nil {
			merged["error"] = input.ResponsePayload.Error
		}
		for key, value := range input.ResponsePayload.Extra {
			merged[key] = value
		}
		responsePayload = merged
	}
	failureContext := usageFailureContextOf(input.UsageContext)
	_ = a.service.RecordGatewayFailure(context.Background(), failureContext, gatewayusage.RecordGatewayFailureInput{
		StatusCode:      input.StatusCode,
		StartedAtMs:     input.StartedAtMs,
		CompletedAtMs:   input.CompletedAtMs,
		ResponsePayload: responsePayload,
		ErrorMessage:    input.ErrorMessage,
	})
}

// ---------------------------------------------------------------------------
// G16 failure dispatch (response/failure-dispatch.ts)
// ---------------------------------------------------------------------------

// chainFailureKindOpaqueHTTP mirrors the 'opaque_http' member of the Node
// failureKind union (only the explicit-policy member has a Go constant).
const chainFailureKindOpaqueHTTP = "opaque_http"

// chainFailureKindCompatibilityRecovery mirrors the 'compatibility_recovery'
// member of the Node failureKind union (failure-dispatch.ts:162).
const chainFailureKindCompatibilityRecovery = "compatibility_recovery"

// chainErrorPolicyRuleSourceSystem mirrors the 'system' member of the
// accountErrorPolicyDecision ruleSource union (chain_error_policy.go writes
// the literal): a system-quota decision is stronger evidence than the
// fixed-model probe, so the request-failure health-check dispatch stays off.
const chainErrorPolicyRuleSourceSystem = "system"

// chainFailureErrorBodyCaptureBytes mirrors upstreamErrorBodyCaptureBytes
// (upstream/body.ts): the bounded failure-body capture feeding retry
// diagnostics and usage records.
const chainFailureErrorBodyCaptureBytes = 256 * 1024

// chainFailureDispatcher is the composition-root FailureDispatcher: the
// handleFailedUpstreamResponse / handleUpstreamRequestError decision tree of
// the archived response/failure-dispatch.ts.
//
//	traffic source        | failed-response decision
//	----------------------+-----------------------------------------------------
//	account diagnostic    | return_response — diagnostics observe the
//	                      | provider's actual terminal response; the response
//	                      | layer's ok gate (routes.ts:1550) renders a non-2xx
//	                      | + SSE body as the non-stream error contract
//	non-gateway (hybrid)  | forget session affinity + return_response
//	gateway               | bounded body capture + audit complete + usage
//	                      | record + skip_account (candidate failover) with
//	                      | the same-account key-rotation facts
//
// Request errors (transport failures / downstream closes) always skip the
// account after recording; the engine owns the rethrow contracts around the
// aborted branch. The codex encrypted-content compatibility recovery
// (retry_with_compatibility_recovery) runs on the mounted gatewaycodex
// recovery helper and the engine's semantic-retry bridge. The request-failure
// health-check dispatch (Node dispatchRequestFailureHealthCheck) and the
// source-avoidance availability probe (turn-availability-probe.service.ts)
// mount through chain_request_failure_health.go / chain_turn_probe_store.go;
// an unwired dispatch bridge keeps both on the explicit logged degradation.
type chainFailureDispatcher struct {
	usage    *gatewayusage.Service
	affinity gatewaydispatch.SessionAffinityPort
	// clientStrategy re-resolves the G18 client strategy at failure time
	// (failure-dispatch.ts scheduleGatewayClientSourceAvoidanceFailure); nil
	// keeps the avoidance recording off.
	clientStrategy *gatewaycodex.ClientStrategyDeps
	// turnRetry owns the client-source avoidance state (memory driver);
	// nil keeps the recording off.
	turnRetry *gatewaycodex.TurnRetryService
	// avoidanceProbe dispatches the activation availability probe; nil keeps
	// the short avoidance without an early probe clear (logged once).
	avoidanceProbe *gatewaycodex.TurnAvoidanceProbeService
	// healthDispatch is the request-failure account health-check port
	// (failure-dispatch.ts:404/571); nil keeps the dispatch degraded (logged
	// once).
	healthDispatch *chainRequestFailureHealthDispatcher
	// policy 决策服务（nil → 默认时钟 + 池隔离关闭的实例）；effects 是显式
	// 策略决策的状态写侧窄口（nil → 显式降级实现，首次使用记录一条日志）。
	// 见 chain_error_policy.go / chain_error_policy_effects.go。
	policy  *chainErrorPolicyService
	effects chainAccountErrorPolicyEffects
	// apiKeyObservation 捕获进程内 API-Key 失败观察代际（Node
	// captureGatewayAccountApiKeyFailureObservation，
	// account-api-key-failure-guard.service.ts:92-101）；生产装配为
	// chainRuntimeServices.AccountAPIKeyGuard。nil 时挂起失败不带代际——
	// 记录侧 guard 会把空代际按 stale 观察拒绝，不会误占 fence。
	apiKeyObservation chainAPIKeyObservationPort
	// codexUsageHeaders 是 codex 用量响应头的失败面持久化窄口（Node
	// persistOpenAICodexHeadersIfNeeded，failure-dispatch.ts:340-344）。
	// gatewaycodex.PersistOpenAICodexHeadersIfNeeded 对 nil 派发器静默返回
	// （gatewaycodex 包契约：不合格账户与无 codex 头静默跳过）；生产装配为
	// compose_codex_usage_headers.go 的 record_maintenance_jobs 快照通道。
	codexUsageHeaders gatewaycodex.CodexUsageHeadersDispatcher
}

// chainAPIKeyObservationPort 是 AccountAPIKeyFailureGuard.CaptureFailureObservation
// 的窄口投影（gatewaydispatch.AccountCandidate 即
// gatewayruntimecache.OpenAIAccountSecret 的类型别名，无需适配层）。
type chainAPIKeyObservationPort interface {
	CaptureFailureObservation(account gatewayruntimecache.OpenAIAccountSecret) *int64
}

func (d *chainFailureDispatcher) errorPolicyOf() *chainErrorPolicyService {
	if d.policy != nil {
		return d.policy
	}
	return newChainErrorPolicyService(chainErrorPolicyDeps{})
}

func (d *chainFailureDispatcher) errorPolicyEffectsOf() chainAccountErrorPolicyEffects {
	if d.effects != nil {
		return d.effects
	}
	return &degradedChainErrorPolicyEffects{}
}

func (d *chainFailureDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input gatewaydispatch.FailedUpstreamResponseInput) (gatewaydispatch.FailedUpstreamResponseResult, error) {
	trafficSource := input.UsageContext.TrafficSource
	// failure-dispatch.ts:198-200: account diagnostics must observe the
	// provider's actual terminal HTTP response — generic gateway takeover
	// would hide the sampled status sequence.
	if gatewayusage.IsAccountDiagnosticTrafficSource(trafficSource) {
		return gatewaydispatch.FailedUpstreamResponseResult{
			Action:   gatewaydispatch.FailedResponseActionReturnResponse,
			Response: input.Response,
		}, nil
	}
	// failure-dispatch.ts:204-210: non-gateway traffic observes its actual
	// response; customer gateway traffic follows the candidate failover.
	if trafficSource != gatewayTrafficSource {
		d.forgetSessionAffinity(ctx, input.SessionAffinityKey, input.Account.ID)
		return gatewaydispatch.FailedUpstreamResponseResult{
			Action:   gatewaydispatch.FailedResponseActionReturnResponse,
			Response: input.Response,
		}, nil
	}

	// Gateway candidate-failover branch (failure-dispatch.ts:212-436).
	statusCode := 0
	hasStatus := false
	if input.Response != nil {
		statusCode = input.Response.Status()
		hasStatus = true
	}
	bodyText, truncated, readErr := readUpstreamFailureBody(ctx, input.Response, chainFailureErrorBodyCaptureBytes)
	if input.Response != nil {
		// The skip path discards the response; the body must not leak.
		_ = input.Response.Body.Close()
	}
	if truncated {
		slog.Warn("上游失败响应体超过网关捕获上限，已截断用于重试诊断",
			"event", "gateway_upstream_retry_error_body_truncated",
			"accountId", input.Account.ID, "statusCode", statusCode)
	} else if readErr != nil {
		// The failure decision (status is already known) does not depend on
		// the body capture; the read failure only costs the diagnostics.
		slog.Debug("上游失败响应体读取失败，跳过重试诊断捕获",
			"event", "gateway_upstream_retry_error_body_read_failed",
			"accountId", input.Account.ID, "statusCode", statusCode, "error", readErr.Error())
	}

	// failure-dispatch.ts:229-254: the structured failed-response warning.
	// Node classifies with the phase only (no status/error code inputs), so
	// metricReasonClass stays the phase default; the account-probe debug
	// demotion is unreachable here — the branches above already returned for
	// every non-gateway traffic source.
	failureObservation := gatewayresponse.ClassifyGatewayUpstreamFailure(gatewayresponse.GatewayUpstreamFailureClassificationInput{
		Phase: "upstream_response",
	})
	slog.Warn("上游返回非成功状态",
		"event", "gateway_upstream_response_failed",
		"accountId", input.Account.ID,
		"accountType", input.Account.Type,
		"upstreamUrl", chainSanitizedUpstreamURL(input.UpstreamURL),
		"attemptIndex", input.AttemptIndex,
		"auditAttemptIndex", input.AuditAttemptIndex,
		"statusCode", statusCode,
		"contentType", responseContentTypeOf(input.Response),
		"elapsedMs", time.Now().UnixMilli()-input.AttemptStartedAt,
		"responseBodyBytes", len(bodyText),
		"responseBodyTruncated", truncated,
		"failureClass", failureObservation.FailureClass,
		"metricReasonClass", failureObservation.MetricReasonClass,
		"classificationReason", failureObservation.ClassificationReason,
		"trafficSource", trafficSource)

	// failure-dispatch.ts:219-227: parseFailureBodyFacts + decideAccountErrorPolicy
	// run right after the bounded capture — the explicit account error policy
	// decision drives the failureKind, the key-rotation authorization, the
	// audit/usage quota attribution and the state changes below.
	parsedFailureBody := parsedFailureBodyOf(input.Response, bodyText)
	policyHeader := responseHTTPHeaderOf(input.Response)
	decision, decisionErr := d.errorPolicyOf().Decide(input.Account, statusCode, policyHeader, bodyText, parsedFailureBody, input.Settings)
	if decisionErr != nil {
		// Node: 读取侧规则归一是同一严格校验，抛出同步异常并按请求错误处理。
		return gatewaydispatch.FailedUpstreamResponseResult{}, decisionErr
	}
	failurePayload := failureProtocolPayloadOf(parsedFailureBody)
	upstreamErrorSummary := accountErrorPayloadSummary(failurePayload)

	lastAttempt := failedResponseAttemptOf(input, bodyText, parsedFailureBody)
	if input.AuditAttemptID != "" {
		input.AuditCapture.CompleteAttempt(input.AuditAttemptID, gatewaydispatch.CompleteAttemptInput{
			Success:      false,
			ErrorPhase:   "upstream_response",
			ErrorMessage: bodyText,
		})
	} else {
		input.AuditCapture.RecordFailedDispatchAttempt(gatewaydispatch.FailedDispatchAttemptInput{
			Account:                   input.Account,
			AttemptIndex:              input.AuditAttemptIndex,
			UpstreamURL:               input.UpstreamURL,
			Method:                    requestMethodOf(input.Req),
			StartedAtMs:               input.AttemptStartedAt,
			ErrorPhase:                "upstream_response",
			ErrorMessage:              bodyText,
			RequestForModelAccounting: input.Req,
		})
	}
	if d.usage != nil {
		if err := d.usage.RecordFailedUpstreamAttempt(ctx, usageContextOf(input.UsageContext), usageModelAccountOf(input.Account), gatewayusage.RecordFailedUpstreamAttemptInput{
			UpstreamURL:  input.UpstreamURL,
			StartedAtMs:  input.AttemptStartedAt,
			StatusCode:   statusPointer(hasStatus, statusCode),
			Headers:      usageFailureHeadersOf(input.Response),
			BodyText:     bodyText,
			ErrorMessage: lastAttempt.Message,
			ErrorPayload: usageErrorPayloadOf(failurePayload),
		}); err != nil {
			return gatewaydispatch.FailedUpstreamResponseResult{}, err
		}
	}

	// failure-dispatch.ts:292-327: the codex encrypted-content compatibility
	// recovery runs after the audit/usage records and before the policy
	// branches. A retry_with_body_variant result short-circuits the skip flow
	// — the engine replays the same account with the sanitized body and the
	// semantic-retry id. A not_recoverable verdict with a signal only adds the
	// skip audit metadata; the decision stays the ordinary candidate failover.
	recovery := gatewaycodex.RecoverCodexEncryptedContent(ctx, gatewaycodex.EncryptedContentRecoveryInput{
		Req:               input.Req,
		Account:           input.Account,
		Body:              input.RequestBody,
		UpstreamErrorText: bodyText,
	})
	if recovery.Action == gatewaycodex.RecoveryActionRetryWithBodyVariant {
		input.AuditCapture.AddGatewayMetadata("codex_encrypted_content_recovery_retry", codexRecoveryMetadataOf(input, recovery))
		return gatewaydispatch.FailedUpstreamResponseResult{
			Action:      gatewaydispatch.FailedResponseActionRetryWithCompatibilityRecovery,
			FailureKind: chainFailureKindCompatibilityRecovery,
			LastAttempt: lastAttempt,
			Recovery: gatewaydispatch.CompatibilityRecovery{
				Body:            recovery.Body,
				SemanticRetryID: recovery.SemanticRetryID,
			},
		}, nil
	}
	if recovery.Action == gatewaycodex.RecoveryActionNotRecoverable && recovery.Signal != "" {
		skipped := map[string]any{
			"accountId":   input.Account.ID,
			"upstreamUrl": input.UpstreamURL,
			"transport":   "http",
			"signal":      recovery.Signal,
		}
		if recovery.Reason != "" {
			skipped["reason"] = recovery.Reason
		}
		input.AuditCapture.AddGatewayMetadata("codex_encrypted_content_recovery_skipped", skipped)
	}

	// failure-dispatch.ts:339-344: the codex usage headers persist on the
	// failure face too — an eligible OAuth codex account with codex headers
	// dispatches the side effect with the gateway_error source rewrite. nil
	// dispatcher stays silent inside the helper (gatewaycodex contract).
	if input.AccountStateMutationEnabled {
		codexHeadersSource := trafficSource
		if trafficSource == gatewayTrafficSource {
			codexHeadersSource = "gateway_error"
		}
		gatewaycodex.PersistOpenAICodexHeadersIfNeeded(ctx, input.Account, policyHeader,
			codexHeadersSource, gatewaycodex.SystemClock{}, d.codexUsageHeaders)
	}

	// failure-dispatch.ts:346: forget the session affinity before the policy
	// branches — the failed account leaves this conversation's ordering.
	d.forgetSessionAffinity(ctx, input.SessionAffinityKey, input.Account.ID)

	// failure-dispatch.ts:348-367: the keyScoped system-quota decision records
	// the Key-scoped failure directly (rate_limited + quota recovery code).
	// account-api-key-effects.service.ts:141-171: the write failure is
	// caught-and-warned (gateway_account_api_key_failure_side_effect_failed) —
	// the request keeps its skip_account candidate failover instead of
	// aborting the attempt on a bookkeeping error.
	if decision != nil && decision.KeyScoped && input.AccountStateMutationEnabled {
		if err := d.errorPolicyEffectsOf().RecordKeyScopedQuotaFailure(ctx, input.Account, *decision, chainErrorPolicyFailureInput{
			StatusCode:                   statusCode,
			HasStatusCode:                hasStatus,
			BodyText:                     bodyText,
			UpstreamErrorSummary:         upstreamErrorSummary,
			UpstreamErrorSummaryResolved: true,
			TraceID:                      input.UsageContext.TraceID,
			AttemptStartedAtMs:           input.AttemptStartedAt,
		}); err != nil {
			slog.Warn("账户内 API Key 失败运行态写入失败",
				"event", "gateway_account_api_key_failure_side_effect_failed",
				"accountId", input.Account.ID,
				"selectedApiKeyFingerprint", stringValueOf(input.Account.SelectedAPIKeyFingerprint),
				"source", "system_quota_policy",
				"error", err)
		}
	}

	// failure-dispatch.ts:369-390: the explicit-policy audit attribution and
	// the state change (cooldown / disable) through the effects port.
	if decision != nil && input.AccountStateMutationEnabled {
		input.AuditCapture.AddGatewayMetadata("account_error_policy_matched", map[string]any{
			"accountId":               input.Account.ID,
			"ruleId":                  decision.RuleID,
			"ruleName":                decision.RuleName,
			"ruleSource":              decision.RuleSource,
			"action":                  decision.Action,
			"cooldownStatus":          decision.CooldownStatus,
			"keyScoped":               decision.KeyScoped,
			"quotaRecoveryMode":       decision.QuotaRecoveryMode,
			"quotaRecoveryHintSource": decision.QuotaRecoveryHintSource,
		})
		if decision.Action != decisionActionRetryNext {
			failureInput := chainErrorPolicyFailureInput{
				StatusCode:                   statusCode,
				HasStatusCode:                hasStatus,
				BodyText:                     bodyText,
				UpstreamErrorSummary:         upstreamErrorSummary,
				UpstreamErrorSummaryResolved: true,
				TraceID:                      input.UsageContext.TraceID,
				AttemptStartedAtMs:           input.AttemptStartedAt,
			}
			if _, _, err := d.errorPolicyEffectsOf().ApplyAccountErrorPolicyDecision(ctx, input.Account, *decision, failureInput); err != nil {
				return gatewaydispatch.FailedUpstreamResponseResult{}, err
			}
		}
	}

	// failure-dispatch.ts:392-396: an explicit retry_next / keyScoped decision
	// authorizes the same-account key rotation regardless of the pre-commit
	// deferral; the automatic path additionally requires the absence of an
	// explicit decision — a cooldown/disable decision changes account-level
	// state and never rotates, even when the deferral is unset.
	hasAlternativeAccountAPIKeys := input.Account.SelectedAPIKeyFingerprint != nil && len(input.Account.APIKeys) > 1
	explicitRotation := decision != nil && (decision.Action == decisionActionRetryNext || decision.KeyScoped)
	automaticRotation := decision == nil && !input.DeferAutomaticSameAccountKeyRotation
	sameAccountKeyRotation := hasAlternativeAccountAPIKeys && (explicitRotation || automaticRotation)
	failureKind := chainFailureKindOpaqueHTTP
	if decision != nil {
		failureKind = gatewaydispatch.FailureKindExplicitPolicy
	}
	// failure-dispatch.ts:399-405: a complete gateway HTTP failure is
	// independent evidence that this account needs the fixed-model
	// availability confirmation — except when a system-quota decision made
	// the stronger call (its probe must not compete with the explicit error
	// state). The request-level throttle keeps retry_next candidate replays
	// and the transport branch from double firing.
	if decision == nil || decision.RuleSource != chainErrorPolicyRuleSourceSystem {
		d.dispatchRequestFailureHealthCheck(ctx, input.Req, input.UsageContext.TrafficSource, input.Account.ID)
	}
	result := gatewaydispatch.FailedUpstreamResponseResult{
		Action:           gatewaydispatch.FailedResponseActionSkipAccount,
		FailureKind:      failureKind,
		LastAttempt:      lastAttempt,
		KeyScopedFailure: sameAccountKeyRotation,
	}
	// failure-dispatch.ts:419-435: a completed failure alone stays neutral —
	// the pending Key failure becomes shared evidence only after a sibling Key
	// of this same account succeeds; the keyScoped system-quota decision
	// already recorded its own failure above.
	if sameAccountKeyRotation && input.AccountStateMutationEnabled &&
		(decision == nil || !decision.KeyScoped) &&
		input.Account.SelectedAPIKeyFingerprint != nil && !input.Account.APIKeyRuntimeStateDisabled {
		// failure-dispatch.ts:421-434: the pending Key failure carries the
		// process-local observation epoch captured at failure time — the
		// confirmed-rotation recorder only accepts a non-stale observation
		// (account-api-key-failure-guard.service.ts:92-101).
		result.PendingApiKeyFailure = &gatewaydispatch.PendingAccountApiKeyFailure{
			Account:          input.Account,
			Status:           "temporary_unavailable",
			StatusCode:       statusCode,
			ErrorMessage:     upstreamErrorSummary,
			MutationContext:  map[string]any{"authority": "confirmed_same_account_key_rotation", "trafficSource": gatewayTrafficSource},
			ObservationEpoch: chainObservationEpochOf(d.apiKeyObservation, input.Account),
		}
	}
	return result, nil
}

func (d *chainFailureDispatcher) HandleUpstreamRequestError(ctx context.Context, input gatewaydispatch.UpstreamRequestErrorInput) (gatewaydispatch.UpstreamRequestErrorResult, error) {
	// failure-dispatch.ts:478-517: the downstream-closed branch records the
	// attempt with the fixed downstream attribution. The Go engine only calls
	// this port once shouldRecordAbortedUpstreamAttempt(err) held, which is
	// the Node recording condition, so the branch is unconditional here.
	if gatewaydispatch.IsUpstreamRequestAbortedError(input.Error) {
		if err := d.recordDownstreamClosedRequestError(ctx, input); err != nil {
			return gatewaydispatch.UpstreamRequestErrorResult{}, err
		}
		return gatewaydispatch.UpstreamRequestErrorResult{
			Action:           gatewaydispatch.FailedResponseActionSkipAccount,
			LastAttempt:      downstreamClosedAttemptOf(input),
			KeyScopedFailure: false,
		}, nil
	}

	// failure-dispatch.ts:519-591: the transport-failure branch.
	message := formatUpstreamRequestErrorMessage(input.Error)
	lastAttempt := transportFailureAttemptOf(input, message, formatUpstreamRequestTransportFailureKind(input.Error, input.LastAttempt))
	// failure-dispatch.ts:522-542: the structured transport-failure warning.
	// Node classifies with the phase only, so the metric reason class is the
	// fixed transport default; errorCode degrades to "" for Go error values
	// without a code surface.
	requestFailureObservation := gatewayresponse.ClassifyGatewayUpstreamFailure(gatewayresponse.GatewayUpstreamFailureClassificationInput{
		Phase: "upstream_request",
	})
	slog.Warn("网关请求上游失败",
		"event", "gateway_upstream_request_failed",
		"accountId", input.Account.ID,
		"accountType", input.Account.Type,
		"upstreamUrl", chainSanitizedUpstreamURL(input.UpstreamURL),
		"attemptIndex", input.AttemptIndex,
		"auditAttemptIndex", input.AuditAttemptIndex,
		"elapsedMs", time.Now().UnixMilli()-input.AttemptStartedAt,
		"stream", gatewaydispatch.IsEffectiveOpenAIStreamRequest(input.Req, chainUpstreamHeaderAccountOf(input.Account)),
		"errorName", upstreamRequestErrorName(input.Error),
		"errorCode", upstreamRequestErrorCode(input.Error),
		"errorMessage", message,
		"failureClass", requestFailureObservation.FailureClass,
		"metricReasonClass", requestFailureObservation.MetricReasonClass,
		"classificationReason", requestFailureObservation.ClassificationReason,
		"trafficSource", input.UsageContext.TrafficSource)
	if input.AuditAttemptID != "" {
		input.AuditCapture.CompleteAttempt(input.AuditAttemptID, gatewaydispatch.CompleteAttemptInput{
			Success:      false,
			ErrorPhase:   "upstream_request",
			ErrorMessage: message,
		})
	} else {
		input.AuditCapture.RecordFailedDispatchAttempt(gatewaydispatch.FailedDispatchAttemptInput{
			Account:                   input.Account,
			AttemptIndex:              input.AuditAttemptIndex,
			UpstreamURL:               input.UpstreamURL,
			Method:                    requestMethodOf(input.Req),
			StartedAtMs:               input.AttemptStartedAt,
			ErrorPhase:                "upstream_request",
			ErrorMessage:              message,
			RequestForModelAccounting: input.Req,
		})
	}
	if d.usage != nil {
		if err := d.usage.RecordFailedUpstreamAttempt(ctx, usageContextOf(input.UsageContext), usageModelAccountOf(input.Account), gatewayusage.RecordFailedUpstreamAttemptInput{
			UpstreamURL:  input.UpstreamURL,
			StartedAtMs:  input.AttemptStartedAt,
			ErrorMessage: message,
		}); err != nil {
			return gatewaydispatch.UpstreamRequestErrorResult{}, err
		}
	}
	d.forgetSessionAffinity(ctx, input.SessionAffinityKey, input.Account.ID)
	// failure-dispatch.ts:571: the transport failure is independent evidence
	// for the fixed-model availability confirmation (before the source
	// avoidance record; the downstream-closed branch does not dispatch).
	d.dispatchRequestFailureHealthCheck(ctx, input.Req, input.UsageContext.TrafficSource, input.Account.ID)
	// failure-dispatch.ts:572-579: the transport-failure branch schedules the
	// client-source avoidance record after the affinity forget (the Node
	// downstream-closed branch does not record; neither does the failed-
	// response branch).
	d.scheduleClientSourceAvoidanceFailure(ctx, input, message)
	return gatewaydispatch.UpstreamRequestErrorResult{
		Action:           gatewaydispatch.FailedResponseActionSkipAccount,
		LastAttempt:      lastAttempt,
		KeyScopedFailure: false,
	}, nil
}

// scheduleClientSourceAvoidanceFailure mirrors
// scheduleGatewayClientSourceAvoidanceFailure (failure-dispatch.ts:646-684):
// re-resolve the client strategy at failure time with the dispatch identity,
// record the source-scoped account failure and hand a fresh activation to the
// availability probe. Degrades observably: without the collaborators the
// avoidance stays off (Node keeps it off for a missing source key too), and a
// probe dispatch failure keeps the short avoidance.
func (d *chainFailureDispatcher) scheduleClientSourceAvoidanceFailure(ctx context.Context, input gatewaydispatch.UpstreamRequestErrorInput, message string) {
	if d.clientStrategy == nil || d.turnRetry == nil {
		if input.UsageContext.TrafficSource == gatewayTrafficSource {
			slogOnceWarn("gatewaydispatch.ClientSourceAvoidanceRecording", "来源级失败避让记录未装配，避让保持关闭")
		}
		return
	}
	if input.UsageContext.TrafficSource != gatewayTrafficSource {
		return
	}
	strategy := d.clientStrategy.ResolveOpenAIGatewayClientStrategy(input.Req, gatewaycodex.ClientStrategyIdentity{
		SystemAccountID:           input.UsageContext.SystemAccountID,
		APIKeyID:                  input.UsageContext.APIKeyID,
		GroupID:                   input.UsageContext.GroupID,
		Endpoint:                  input.UsageContext.Endpoint,
		ProviderCode:              input.Account.ProviderCode,
		ProviderProtocolProfileID: input.Account.ProviderProtocolProfileID,
		ProtocolCode:              input.Account.ProtocolCode,
		ProtocolVersion:           input.Account.ProtocolVersion,
		ClientIP:                  input.UsageContext.ClientIP,
	})
	if !strategy.AllowClientSourceAccountAvoidance {
		return
	}
	record, err := d.turnRetry.RememberGatewayClientSourceFailureAsync(ctx, strategy, input.Account.ID, gatewaycodex.CodexTurnFailureInput{
		ErrorCode:     upstreamRequestErrorName(input.Error),
		Message:       message,
		ObservationID: input.AuditAttemptID + ":transport_failure",
	})
	if err != nil {
		slog.Warn("来源级失败避让未能记录，保留短期避让",
			"event", "gateway_client_source_avoidance_failure_schedule_failed",
			"accountId", input.Account.ID, "error", err.Error())
		return
	}
	if record == nil || record.Activation == nil {
		return
	}
	if d.avoidanceProbe == nil {
		slogOnceWarn("gatewaycodex.TurnAvoidanceProbeService", "来源级避让激活后探活未装配，保留短期避让")
		return
	}
	activation := *record.Activation
	account := input.Account
	probe := d.avoidanceProbe
	go func() {
		probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := probe.RunGatewayClientSourceAvoidanceAvailabilityProbe(probeCtx, gatewaycodex.CodexTurnAvoidanceProbeInput{
			Account:    account,
			Strategy:   strategy,
			Activation: activation,
		}); err != nil {
			slog.Warn("来源级失败避让未能投递探活，保留短期避让",
				"event", "gateway_client_source_avoidance_failure_schedule_failed",
				"accountId", account.ID, "error", err.Error())
		}
	}()
}

// codexRecoveryMetadataOf mirrors the Node retry audit metadata:
// accountId / upstreamUrl / transport plus the recovery metadata fields.
func codexRecoveryMetadataOf(input gatewaydispatch.FailedUpstreamResponseInput, recovery gatewaycodex.CodexEncryptedContentRecoveryResult) map[string]any {
	metadata := recovery.Metadata
	if metadata == nil {
		return map[string]any{
			"accountId":   input.Account.ID,
			"upstreamUrl": input.UpstreamURL,
			"transport":   "http",
		}
	}
	return map[string]any{
		"accountId":                             input.Account.ID,
		"upstreamUrl":                           input.UpstreamURL,
		"transport":                             "http",
		"strategy":                              metadata.Strategy,
		"signal":                                metadata.Signal,
		"removedReasoningEncryptedContentCount": metadata.RemovedReasoningEncryptedContentCount,
		"removedFunctionOutputEncryptedContentCount": metadata.RemovedFunctionOutputEncryptedContentCount,
		"removedAgentMessageEncryptedContentCount":   metadata.RemovedAgentMessageEncryptedContentCount,
		"removedCompactionEncryptedContentCount":     metadata.RemovedCompactionEncryptedContentCount,
		"removedReasoningItemCount":                  metadata.RemovedReasoningItemCount,
		"removedAgentMessageItemCount":               metadata.RemovedAgentMessageItemCount,
		"removedCompactionItemCount":                 metadata.RemovedCompactionItemCount,
		"preservedPreviousResponseID":                metadata.PreservedPreviousResponseID,
		"bodyBytesBefore":                            metadata.BodyBytesBefore,
		"bodyBytesAfter":                             metadata.BodyBytesAfter,
	}
}

// upstreamRequestErrorName mirrors the Node `error.name` diagnostic carried
// on the avoidance record: the dynamic error type name without the package
// qualifier.
func upstreamRequestErrorName(err error) string {
	if err == nil {
		return ""
	}
	name := fmt.Sprintf("%T", err)
	name = strings.TrimPrefix(name, "*")
	if index := strings.LastIndexByte(name, '.'); index >= 0 {
		name = name[index+1:]
	}
	return name
}

// upstreamRequestErrorCode mirrors the Node
// `objectStringProperty(error, 'code')` diagnostic: Go error values expose a
// code only through the optional ErrorCode surface, anything else degrades to
// the empty string.
func upstreamRequestErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if coded, ok := err.(interface{ ErrorCode() string }); ok {
		return coded.ErrorCode()
	}
	return ""
}

// chainSanitizedUpstreamURL mirrors
// sanitizeUrlCredentialsForLog(upstreamUrl) ?? 'unknown'.
func chainSanitizedUpstreamURL(upstreamURL string) string {
	sanitized := strings.TrimSpace(upstreamURL)
	if sanitized == "" {
		return "unknown"
	}
	return sanitized
}

// responseContentTypeOf mirrors response.headers.get('content-type').
func responseContentTypeOf(response *gatewaydispatch.GatewayUpstreamResponse) string {
	if response == nil || response.Header == nil {
		return ""
	}
	return response.Header.Get("Content-Type")
}

// chainUpstreamHeaderAccountOf projects the dispatch candidate onto the
// stream-detection account shape (gatewaydispatch headerAccountOf).
func chainUpstreamHeaderAccountOf(account gatewaydispatch.AccountCandidate) *gatewaydispatch.UpstreamHeaderAccount {
	return &gatewaydispatch.UpstreamHeaderAccount{
		ID:                        account.ID,
		APIKey:                    account.APIKey,
		Type:                      account.Type,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		Credentials:               account.Credentials,
	}
}

// chainObservationEpochOf captures the process-local failure observation epoch
// and renders it in the pending-failure string form (decimal int64); a nil
// port or an unavailable epoch keeps it empty.
func chainObservationEpochOf(port chainAPIKeyObservationPort, account gatewaydispatch.AccountCandidate) string {
	if port == nil {
		return ""
	}
	epoch := port.CaptureFailureObservation(account)
	if epoch == nil {
		return ""
	}
	return strconv.FormatInt(*epoch, 10)
}

// recordDownstreamClosedRequestError mirrors the recording half of the Node
// downstream-closed branch: usage carries the fixed downstream attribution,
// the audit attempt closes with the downstream phase.
func (d *chainFailureDispatcher) recordDownstreamClosedRequestError(ctx context.Context, input gatewaydispatch.UpstreamRequestErrorInput) error {
	lastAttempt := input.LastAttempt
	statusCode := 0
	hasStatus := false
	if lastAttempt != nil && lastAttempt.AccountID == input.Account.ID &&
		lastAttempt.UpstreamURL == input.UpstreamURL && lastAttempt.HasStatus {
		statusCode = lastAttempt.Status
		hasStatus = true
	}
	if d.usage != nil {
		if err := d.usage.RecordFailedUpstreamAttempt(ctx, usageContextOf(input.UsageContext), usageModelAccountOf(input.Account), gatewayusage.RecordFailedUpstreamAttemptInput{
			UpstreamURL:        input.UpstreamURL,
			StartedAtMs:        input.AttemptStartedAt,
			StatusCode:         statusPointer(hasStatus, statusCode),
			ErrorMessage:       gatewayresponse.DownstreamConnectionClosedMessage,
			FailureAttribution: gatewayusage.FailureAttributionDownstreamClosed,
		}); err != nil {
			return err
		}
	}
	if input.AuditAttemptID != "" {
		input.AuditCapture.CompleteAttempt(input.AuditAttemptID, gatewaydispatch.CompleteAttemptInput{
			Success:      false,
			ErrorPhase:   "downstream",
			ErrorMessage: gatewayresponse.DownstreamConnectionClosedMessage,
		})
	} else {
		input.AuditCapture.RecordFailedDispatchAttempt(gatewaydispatch.FailedDispatchAttemptInput{
			Account:                   input.Account,
			AttemptIndex:              input.AuditAttemptIndex,
			UpstreamURL:               input.UpstreamURL,
			Method:                    requestMethodOf(input.Req),
			StartedAtMs:               input.AttemptStartedAt,
			ErrorPhase:                "downstream",
			ErrorMessage:              gatewayresponse.DownstreamConnectionClosedMessage,
			RequestForModelAccounting: input.Req,
		})
	}
	d.forgetSessionAffinity(ctx, input.SessionAffinityKey, input.Account.ID)
	return nil
}

func (d *chainFailureDispatcher) IsOpaqueUpstreamFailoverAllowed(_ *gatewaypreauth.GatewayRequest) bool {
	// failure-dispatch.ts:73-79: opaque HTTP failures may not retry a sibling
	// API Key; account-level failover is the failed-response path's decision.
	return false
}

// dispatchRequestFailureHealthCheck mirrors dispatchRequestFailureAccountHealthCheck:
// the gateway-traffic gate and the per-request throttle live inside the
// dispatcher; an unwired port degrades with one process-level log line.
func (d *chainFailureDispatcher) dispatchRequestFailureHealthCheck(_ context.Context, req *gatewaypreauth.GatewayRequest, trafficSource, accountID string) {
	if d.healthDispatch == nil {
		if trafficSource == gatewayTrafficSource {
			slogOnceWarn("gateway.response.requestFailureHealthCheckDispatch", "请求失败健康检查派发未装配，跳过派发")
		}
		return
	}
	d.healthDispatch.DispatchRequestFailureAccountHealthCheck(req, trafficSource, accountID)
}

func (d *chainFailureDispatcher) forgetSessionAffinity(ctx context.Context, sessionAffinityKey, accountID string) {
	if d.affinity == nil || sessionAffinityKey == "" || accountID == "" {
		return
	}
	_ = d.affinity.ForgetAsync(ctx, sessionAffinityKey, accountID)
}

// readUpstreamFailureBody mirrors readUpstreamBodyForPolicyInspection's
// bounded capture: at most maxBytes are kept for diagnostics, a further byte
// marks the read truncated.
func readUpstreamFailureBody(ctx context.Context, response *gatewaydispatch.GatewayUpstreamResponse, maxBytes int64) (string, bool, error) {
	if response == nil || response.Body == nil {
		return "", false, nil
	}
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return "", false, err
	}
	if int64(len(data)) > maxBytes {
		return string(data[:maxBytes]), true, nil
	}
	return string(data), false, nil
}

// failedResponseAttemptOf rebuilds the Node lastAttempt for a failed
// response: the previous attempt facts (when present) with the provider
// fields overridden and the response facts attached.
func failedResponseAttemptOf(input gatewaydispatch.FailedUpstreamResponseInput, bodyText string, parsedBody map[string]any) *gatewaydispatch.UpstreamAttempt {
	attempt := &gatewaydispatch.UpstreamAttempt{}
	if input.LastAttempt != nil {
		copied := *input.LastAttempt
		attempt = &copied
	}
	attempt.AccountID = input.Account.ID
	attempt.AccountName = input.Account.Name
	attempt.ProviderCode = input.Account.ProviderCode
	attempt.ProviderProtocolProfileID = input.Account.ProviderProtocolProfileID
	attempt.ProtocolCode = input.Account.ProtocolCode
	attempt.ProtocolVersion = input.Account.ProtocolVersion
	attempt.UpstreamURL = input.UpstreamURL
	if input.Response != nil {
		attempt.Status = input.Response.Status()
		attempt.HasStatus = true
	}
	attempt.ResponseHeaders = responseHeadersOf(input.Response)
	attempt.ResponseBodyText = bodyText
	attempt.ParsedResponseBody = parsedBody
	return attempt
}

// downstreamClosedAttemptOf mirrors the Node rebuilt lastAttempt of the
// downstream-closed branch.
func downstreamClosedAttemptOf(input gatewaydispatch.UpstreamRequestErrorInput) *gatewaydispatch.UpstreamAttempt {
	attempt := &gatewaydispatch.UpstreamAttempt{
		AccountID:                 input.Account.ID,
		AccountName:               input.Account.Name,
		ProviderCode:              input.Account.ProviderCode,
		ProviderProtocolProfileID: input.Account.ProviderProtocolProfileID,
		ProtocolCode:              input.Account.ProtocolCode,
		ProtocolVersion:           input.Account.ProtocolVersion,
		UpstreamURL:               input.UpstreamURL,
		Message:                   gatewayresponse.DownstreamConnectionClosedMessage,
	}
	if input.LastAttempt != nil && input.LastAttempt.AccountID == input.Account.ID &&
		input.LastAttempt.UpstreamURL == input.UpstreamURL && input.LastAttempt.HasStatus {
		attempt.Status = input.LastAttempt.Status
		attempt.HasStatus = true
	}
	return attempt
}

// transportFailureAttemptOf mirrors the Node lastAttempt of the
// transport-failure branch: previous facts carried over, message + transport
// failure kind attached.
func transportFailureAttemptOf(input gatewaydispatch.UpstreamRequestErrorInput, message, transportFailureKind string) *gatewaydispatch.UpstreamAttempt {
	attempt := &gatewaydispatch.UpstreamAttempt{}
	if input.LastAttempt != nil {
		copied := *input.LastAttempt
		attempt = &copied
	}
	attempt.AccountID = input.Account.ID
	attempt.AccountName = input.Account.Name
	attempt.ProviderCode = input.Account.ProviderCode
	attempt.ProviderProtocolProfileID = input.Account.ProviderProtocolProfileID
	attempt.ProtocolCode = input.Account.ProtocolCode
	attempt.ProtocolVersion = input.Account.ProtocolVersion
	attempt.UpstreamURL = input.UpstreamURL
	attempt.Message = message
	attempt.TransportFailureKind = transportFailureKind
	return attempt
}

// formatUpstreamRequestErrorMessage mirrors formatUpstreamRequestErrorMessage.
func formatUpstreamRequestErrorMessage(err error) string {
	if err != nil {
		if message := strings.TrimSpace(err.Error()); message != "" {
			return message
		}
	}
	return "请求失败"
}

// formatUpstreamRequestTransportFailureKind mirrors upstreamRequestFailureKind:
// the diagnostic joins the error type name (Node error.name) with the
// message; timeout diagnostics win, otherwise an unproven response start is a
// connection failure and a started one degrades to read_incomplete.
func formatUpstreamRequestTransportFailureKind(err error, previousAttempt *gatewaydispatch.UpstreamAttempt) string {
	diagnostic := ""
	if err != nil {
		diagnostic = strings.ToLower(fmt.Sprintf("%T %s", err, err.Error()))
	}
	if strings.Contains(diagnostic, "timeout") || strings.Contains(diagnostic, "timedout") ||
		strings.Contains(diagnostic, "timed out") || strings.Contains(diagnostic, "etimedout") ||
		strings.Contains(diagnostic, "超时") {
		return gatewaydispatch.TransportFailureKindTimeout
	}
	if previousAttempt == nil || !previousAttempt.HasStatus {
		return gatewaydispatch.TransportFailureKindConnection
	}
	return gatewaydispatch.TransportFailureKindReadIncomplete
}

// responseHeadersOf mirrors headersToObject: lowercased names, values joined.
func responseHeadersOf(response *gatewaydispatch.GatewayUpstreamResponse) map[string]string {
	if response == nil || response.Header == nil {
		return nil
	}
	headers := make(map[string]string, len(response.Header))
	for name, values := range response.Header {
		if len(values) == 0 {
			continue
		}
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return headers
}

// usageFailureHeadersOf projects the response headers onto the usage record.
func usageFailureHeadersOf(response *gatewaydispatch.GatewayUpstreamResponse) map[string]any {
	headers := responseHeadersOf(response)
	if headers == nil {
		return nil
	}
	projected := make(map[string]any, len(headers))
	for name, value := range headers {
		projected[name] = value
	}
	return projected
}

// parsedFailureBodyOf mirrors parseFailureBodyFacts' parsedResponseBody
// attachment: the JSON value only when the failure body parsed as valid JSON.
func parsedFailureBodyOf(response *gatewaydispatch.GatewayUpstreamResponse, bodyText string) map[string]any {
	if response == nil || bodyText == "" {
		return nil
	}
	parsed := gatewayresponse.ParseGatewayNonStreamJsonBody(bodyText, true, response.Header)
	if parsed.Status != gatewayresponse.NonStreamJSONStatusValid {
		return nil
	}
	if value, ok := parsed.Value.(map[string]any); ok {
		return value
	}
	return nil
}

// responseHTTPHeaderOf projects the captured response onto net/http.Header
// for the protocol error payload parser.
func responseHTTPHeaderOf(response *gatewaydispatch.GatewayUpstreamResponse) http.Header {
	if response == nil {
		return nil
	}
	return response.Header
}

// failureProtocolPayloadOf mirrors parseFailureBodyFacts' errorPayload
// (failure-dispatch.ts:439-447): the protocol-aware projection of the parsed
// JSON body; a body that did not parse as valid JSON keeps the payload empty
// ({} in Node) — decision and usage both see the empty payload and must not
// re-parse the captured text.
func failureProtocolPayloadOf(parsedBody map[string]any) gatewayproto.ErrorPayload {
	if parsedBody != nil {
		return gatewayopenai.ParseErrorPayloadFromJSONValue(parsedBody)
	}
	return gatewayproto.ErrorPayload{}
}

// usageErrorPayloadOf mirrors recordFailedUpstreamAttempt's errorPayload
// argument: the extracted evidence as a plain map; nil when the payload
// carries no evidence so the usage layer keeps its own interpretation.
func usageErrorPayloadOf(payload gatewayproto.ErrorPayload) any {
	if !payload.HasEvidence() {
		return nil
	}
	value := map[string]any{}
	if payload.Code != "" {
		value["code"] = payload.Code
	}
	if payload.Type != "" {
		value["type"] = payload.Type
	}
	if payload.Message != "" {
		value["message"] = payload.Message
	}
	return value
}

func requestMethodOf(req *gatewaypreauth.GatewayRequest) string {
	if req == nil {
		return ""
	}
	return req.MethodUpper()
}

func statusPointer(has bool, value int) *int {
	if !has {
		return nil
	}
	return &value
}

// localSessionAffinity implements gatewaydispatch.SessionAffinityPort with a
// process-local memory map (Node sessionAffinityState semantics in the
// memory runtime-state mode: entries never cross instances, ordering only
// re-ranks remembered accounts, the affinity TTL refreshes on remember).
// The Redis-driver shared store lands with the hybrid runtime slice; the
// degradation is logged once on first use.
type localSessionAffinity struct {
	once sync.Once
	mu   sync.Mutex
	ttls map[string]time.Time
	keys map[string]localAffinityEntry
}

type localAffinityEntry struct {
	accountID string
	scopeKey  string
}

func newLocalSessionAffinity() *localSessionAffinity {
	return &localSessionAffinity{
		ttls: map[string]time.Time{},
		keys: map[string]localAffinityEntry{},
	}
}

const localSessionAffinityTTL = 24 * time.Hour

func (a *localSessionAffinity) expired(key string) bool {
	deadline, ok := a.ttls[key]
	return !ok || time.Now().After(deadline)
}

// OrderAsync mirrors orderOpenAIAccountsBySessionAffinityAsync: a remembered
// account moves to the front of its ordering group; everything else keeps
// the scheduling order.
func (a *localSessionAffinity) OrderAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, sessionAffinityKey string, _ gatewaydispatch.AffinityOrderingOptions) ([]gatewaydispatch.AccountCandidate, error) {
	if sessionAffinityKey == "" {
		return accounts, nil
	}
	a.mu.Lock()
	if a.expired(sessionAffinityKey) {
		a.mu.Unlock()
		return accounts, nil
	}
	remembered := a.keys[sessionAffinityKey].accountID
	a.mu.Unlock()
	if remembered == "" {
		return accounts, nil
	}
	ordered := make([]gatewaydispatch.AccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if account.ID == remembered {
			ordered = append([]gatewaydispatch.AccountCandidate{account}, ordered...)
			continue
		}
		ordered = append(ordered, account)
	}
	return ordered, nil
}

// ClaimAsync mirrors claimOpenAIAccountForSessionAsync: the remembered
// account wins; otherwise the proposed account claims the session.
func (a *localSessionAffinity) ClaimAsync(_ context.Context, sessionAffinityKey, proposedAccountID string, scope gatewaydispatch.AffinityScope) (string, bool) {
	a.once.Do(func() {
		slog.Warn("网关链端口显式降级", "port", "gatewaydispatch.SessionAffinityPort", "effect", "会话亲和保持进程内记忆")
	})
	if sessionAffinityKey == "" || proposedAccountID == "" {
		return proposedAccountID, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.expired(sessionAffinityKey) {
		if entry, ok := a.keys[sessionAffinityKey]; ok && entry.accountID != "" {
			return entry.accountID, true
		}
	}
	a.keys[sessionAffinityKey] = localAffinityEntry{accountID: proposedAccountID, scopeKey: scope.GroupID}
	a.ttls[sessionAffinityKey] = time.Now().Add(localSessionAffinityTTL)
	return proposedAccountID, true
}

// RememberAsync mirrors rememberOpenAIAccountForSessionAsync.
func (a *localSessionAffinity) RememberAsync(_ context.Context, sessionAffinityKey, accountID string, scope gatewaydispatch.AffinityScope) {
	if sessionAffinityKey == "" || accountID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keys[sessionAffinityKey] = localAffinityEntry{accountID: accountID, scopeKey: scope.GroupID}
	a.ttls[sessionAffinityKey] = time.Now().Add(localSessionAffinityTTL)
}

// ForgetAsync mirrors forgetOpenAIAccountForSessionAsync.
func (a *localSessionAffinity) ForgetAsync(_ context.Context, sessionAffinityKey, _ string) error {
	if sessionAffinityKey == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.keys, sessionAffinityKey)
	delete(a.ttls, sessionAffinityKey)
	return nil
}

// AreHighConcurrencyAccountsBusyForLaneAsync mirrors
// areOpenAIHighConcurrencyAccountsBusyForLaneAsync: the concurrency runtime
// store is absent from this slice, so high-concurrency accounts are never
// considered busy (Node memory-mode equivalent without the runtime state
// driver; the live counter rides on gatewayruntimecache ConcurrencySource).
func (a *localSessionAffinity) AreHighConcurrencyAccountsBusyForLaneAsync(context.Context, []gatewaydispatch.AccountCandidate, gatewaydispatch.HighConcurrencyBusyOptions) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// disabled collaborators (explicit, logged, Node-absent-runtime semantics)
// ---------------------------------------------------------------------------

// disabledSuppression keeps every account dispatchable (Node: local
// suppression state absent → no suppression).
type disabledSuppression struct {
	once sync.Once
}

func (d *disabledSuppression) FilterAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ gatewaydispatch.SuppressionFilterOptions) (gatewaydispatch.SuppressionFilterResult, error) {
	d.once.Do(func() {
		slog.Warn("网关链端口显式降级", "port", "gatewaydispatch.SuppressionPort", "effect", "账户保持可派发")
	})
	return gatewaydispatch.SuppressionFilterResult{Accounts: accounts}, nil
}

func (d *disabledSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input gatewaydispatch.LocalSuppressionPreflightInput) (*gatewaydispatch.SuppressionFilterResult, bool, error) {
	d.once.Do(func() {
		slog.Warn("网关链端口显式降级", "port", "gatewaydispatch.SuppressionPort", "effect", "本地预检直通")
	})
	result := gatewaydispatch.SuppressionFilterResult{Accounts: input.Accounts}
	return &result, false, nil
}

// disabledDegradation keeps the configured order (Node: degradation runtime
// absent → ordering disabled).
type disabledDegradation struct {
	once sync.Once
}

func (d *disabledDegradation) OrderGatewayAccountsByRuntimeDegradation(accounts []gatewaydispatch.AccountCandidate, _ map[string]int) gatewaydispatch.DegradationOrder {
	d.once.Do(func() {
		slog.Warn("网关链端口显式降级", "port", "gatewaydispatch.DegradationPort", "effect", "派发顺序保持不变")
	})
	return gatewaydispatch.DegradationOrder{Accounts: accounts}
}

func (d *disabledDegradation) OrderWithLaneAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ string, _ *gatewayruntimecache.GroupSchedulingPolicy, _ *gatewayrouting.GatewayAccountModelPriority) (gatewaydispatch.DegradationOrder, error) {
	return gatewaydispatch.DegradationOrder{Accounts: accounts}, nil
}

func (d *disabledDegradation) OrderSync(accounts []gatewaydispatch.AccountCandidate, _ *gatewayrouting.GatewayAccountModelPriority) gatewaydispatch.DegradationOrder {
	return gatewaydispatch.DegradationOrder{Accounts: accounts}
}

// disabledAccountLocks answers with unlocked accounts (Node: lock owner
// absent → no cross-account block, no retry lease).
type disabledAccountLocks struct {
	once sync.Once
}

func (d *disabledAccountLocks) FindStateAsync(_ context.Context, _ string) (*gatewaydispatch.AccountLockStateView, error) {
	d.once.Do(func() {
		slog.Warn("网关链端口显式降级", "port", "gatewaydispatch.AccountLocks", "effect", "账户锁视为未锁")
	})
	return &gatewaydispatch.AccountLockStateView{}, nil
}

func (d *disabledAccountLocks) AcquireRetryLeaseAsync(_ context.Context, _ string, _ int64) (gatewaydispatch.LockLeaseAcquire, error) {
	return gatewaydispatch.LockLeaseAcquire{Allowed: true}, nil
}

func (d *disabledAccountLocks) ConsumeRetryLeaseAsync(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}

func (d *disabledAccountLocks) ReleaseRetryLeaseAsync(_ context.Context, _ gatewaydispatch.ReleaseRetryLeaseInput) (bool, error) {
	return true, nil
}

func (d *disabledAccountLocks) AbandonRetryReservationAsync(_ context.Context, _ gatewaydispatch.AccountLockRetryLease) error {
	return nil
}

func (d *disabledAccountLocks) RecordFailureAsync(_ context.Context, _, _ string, _ *gatewaydispatch.AccountLockObservation) error {
	return nil
}

func (d *disabledAccountLocks) SettleDeadlineAsync(_ context.Context, _ string, _ int64, _ *gatewaydispatch.AccountLockObservation) error {
	return nil
}

func (d *disabledAccountLocks) ListStatesAsync(_ context.Context, accountIDs []string) (map[string]gatewaydispatch.AccountLockStateView, error) {
	states := make(map[string]gatewaydispatch.AccountLockStateView, len(accountIDs))
	for _, id := range accountIDs {
		states[id] = gatewaydispatch.AccountLockStateView{}
	}
	return states, nil
}

// ---------------------------------------------------------------------------
// preauth collaborator adapters (G14/G18)
// ---------------------------------------------------------------------------

// clientStrategyAdapter implements gatewaypreauth.ClientStrategy over the
// G18 gatewaycodex strategy deps.
type clientStrategyAdapter struct {
	deps *gatewaycodex.ClientStrategyDeps
}

func (a clientStrategyAdapter) Resolve(req *gatewaypreauth.GatewayRequest, input gatewaypreauth.ClientStrategyInput) gatewaypreauth.ClientStrategyContext {
	if a.deps == nil {
		return gatewaypreauth.ClientStrategyContext{ClientProfile: gatewaycodex.ClientProfileGenericOpenAI}
	}
	identity := gatewaycodex.ClientStrategyIdentity{
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         input.GroupID,
		Endpoint:        input.Endpoint,
		ProviderCode:    input.ProviderCode,
		ClientIP:        input.ClientIP,
	}
	resolved := a.deps.ResolveOpenAIGatewayClientStrategy(req, identity)
	return gatewaypreauth.ClientStrategyContext{
		ClientProfile:              resolved.ClientProfile,
		DownstreamProtocol:         resolved.DownstreamProtocol,
		RequestClientCompatibility: resolved.RequestClientCompatibility,
		Opaque:                     resolved,
	}
}

func (a clientStrategyAdapter) AuditMetadata(strategy gatewaypreauth.ClientStrategyContext) map[string]any {
	return map[string]any{
		"clientProfile":              strategy.ClientProfile,
		"downstreamProtocol":         strategy.DownstreamProtocol,
		"requestClientCompatibility": strategy.RequestClientCompatibility,
	}
}

// sessionIdentityAdapter implements gatewaypreauth.SessionIdentityResolver.
// The full G14 resolvers attach through sessionIdentityServices; the
// fallback mirrors the Node header-passthrough session id.
type sessionIdentityAdapter struct {
	services *sessionIdentityServices
}

// codexSourceSessionAdapter bridges the G14 IdentityService onto the
// gatewaycodex SessionIdentityResolver seam (source-identity.ts consumes the
// full session projection: status, conversation key and semantic namespace).
type codexSourceSessionAdapter struct {
	identity *gatewaysession.IdentityService
}

// ResolveSessionIdentity implements gatewaycodex.SessionIdentityResolver with
// the same G14 default resolvers the preauth identity path uses.
func (a codexSourceSessionAdapter) ResolveSessionIdentity(req *gatewaypreauth.GatewayRequest, input gatewaypreauth.SessionIdentityInput) gatewaysession.GatewaySessionIdentity {
	if a.identity == nil || req == nil {
		return gatewaysession.GatewaySessionIdentity{Status: gatewaysession.IdentityStatusMissing}
	}
	identity, err := a.identity.Resolve(
		chainIdentityRequest{req: req},
		gatewaysession.IdentityScope{
			ClientProfile:   input.ClientProfile,
			SystemAccountID: input.SystemAccountID,
			APIKeyID:        input.APIKeyID,
		},
		gatewaysession.DefaultGatewaySessionIdentityResolvers,
	)
	if err != nil {
		return gatewaysession.GatewaySessionIdentity{Status: gatewaysession.IdentityStatusMissing}
	}
	return identity
}

// chainIdentityRequest adapts the gateway request onto the G14
// IdentityRequest surface (originalUrl / path / multi-value headers).
type chainIdentityRequest struct {
	req *gatewaypreauth.GatewayRequest
}

func (r chainIdentityRequest) OriginalURL() string { return r.req.PathAndQuery() }
func (r chainIdentityRequest) Path() string        { return r.req.Path() }
func (r chainIdentityRequest) HeaderValues(name string) []string {
	return r.req.HTTP.Header.Values(name)
}

// ResolveGatewaySessionIdentity mirrors resolveGatewaySessionIdentity: the
// G14 IdentityService collects the default header resolvers (codex /
// claude-code session headers) and derives the conversation key; the
// resolved session id + conversation key ride on the frozen identity. When
// the identity services are absent the adapter keeps the Node
// header-passthrough session id fallback.
func (a sessionIdentityAdapter) ResolveGatewaySessionIdentity(req *gatewaypreauth.GatewayRequest, input gatewaypreauth.SessionIdentityInput) gatewaypreauth.SessionIdentity {
	if req == nil {
		return gatewaypreauth.SessionIdentity{}
	}
	if a.services != nil && a.services.Identity != nil {
		identity, err := a.services.Identity.Resolve(
			chainIdentityRequest{req: req},
			gatewaysession.IdentityScope{
				ClientProfile:   input.ClientProfile,
				SystemAccountID: input.SystemAccountID,
				APIKeyID:        input.APIKeyID,
			},
			gatewaysession.DefaultGatewaySessionIdentityResolvers,
		)
		if err == nil && identity.Status == gatewaysession.IdentityStatusResolved {
			return gatewaypreauth.SessionIdentity{
				SessionID:       identity.SessionID,
				ConversationKey: identity.ConversationKey,
			}
		}
		return gatewaypreauth.SessionIdentity{}
	}
	identity := gatewaypreauth.SessionIdentity{}
	if sessionID := trimmedHeader(req, "x-session-id"); sessionID != "" {
		identity.SessionID = sessionID
	}
	if conversationKey := trimmedHeader(req, "x-conversation-key"); conversationKey != "" {
		identity.ConversationKey = conversationKey
	}
	return identity
}

// sessionAffinityAdapter implements gatewaypreauth.SessionAffinity over the
// G14 affinity service.
type sessionAffinityAdapter struct {
	services *sessionIdentityServices
}

func (a sessionAffinityAdapter) ResolveKeyFromClientSource(clientSource *gatewaypreauth.ClientSource, scope gatewaypreauth.SessionAffinityScope) (string, bool) {
	if a.services == nil || a.services.Affinity == nil || clientSource == nil || clientSource.SessionIdentity == nil {
		return "", false
	}
	key, ok := a.services.Affinity.ResolveOpenAIGatewaySessionAffinityKeyFromClientSource(clientSource.SessionIdentity.ConversationKey, gatewaySessionAffinityScopeOf(scope, a.services.Secret))
	return key, ok
}

func (a sessionAffinityAdapter) ResolveKey(identity gatewaypreauth.SessionIdentity, scope gatewaypreauth.SessionAffinityScope) (string, bool) {
	if a.services == nil || a.services.Affinity == nil || identity.ConversationKey == "" {
		return "", false
	}
	key, ok := a.services.Affinity.ResolveOpenAIGatewaySessionAffinityKey(identity.ConversationKey, gatewaySessionAffinityScopeOf(scope, a.services.Secret))
	return key, ok
}

// gatewaySessionAffinityScopeOf maps the frozen affinity scope onto the G14
// key scope (the HMAC secret comes from the session services).
func gatewaySessionAffinityScopeOf(scope gatewaypreauth.SessionAffinityScope, secret string) gatewaysession.GatewaySessionAffinityKeyScope {
	return gatewaysession.GatewaySessionAffinityKeyScope{
		HMACSecret:      secret,
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		RouteStrategyID: scope.RouteStrategyID,
		GroupID:         scope.GroupID,
	}
}

// ---------------------------------------------------------------------------
// observability adapter
// ---------------------------------------------------------------------------

// slogObservability adapts slog to the preauth Observability port
// (shared/request-context.ts surface: request logger, trace ids, stage logs).
type slogObservability struct {
	logger *slog.Logger
	clock  gatewaypreauth.Clock
}

func newSlogObservability(logger *slog.Logger, clock gatewaypreauth.Clock) *slogObservability {
	if logger == nil {
		logger = slog.Default()
	}
	if clock == nil {
		clock = gatewaypreauth.SystemClock{}
	}
	return &slogObservability{logger: logger, clock: clock}
}

func (o *slogObservability) Logger() gatewaypreauth.Logger { return slogWarnLogger{inner: o.logger} }

// TraceID mirrors getTraceId(): empty without a request-bound context; the
// /v1 orchestrator creates one per request.
func (o *slogObservability) TraceID() string { return "" }

func (o *slogObservability) CreateTraceID() string {
	return "trace_" + fmtInt64(o.clock.Now().UnixNano())
}

func (o *slogObservability) SanitizeURLForLog(value string) string { return value }

func (o *slogObservability) LogRequestStage(stage string, fields map[string]any, outcome string, startedAt time.Time) {
	args := []any{"stage", stage, "outcome", outcome, "durationMs", time.Since(startedAt).Milliseconds()}
	for key, value := range fields {
		args = append(args, key, value)
	}
	o.logger.Info("gateway_request_stage", args...)
}

// slogWarnLogger adapts slog to the preauth Logger (logger.warn contract).
type slogWarnLogger struct{ inner *slog.Logger }

func (l slogWarnLogger) Warn(event string, fields map[string]any, message string) {
	l.inner.Warn(message, append([]any{"event", event}, fieldsArgs(fields)...)...)
}

func fmtInt64(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func trimmedHeader(req *gatewaypreauth.GatewayRequest, name string) string {
	value := req.Header(name)
	return trimSpaceLocal(value)
}

func trimSpaceLocal(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}

// codexPreflightAdapter implements gatewaypreauth.CodexBridgePreflight over
// the G18 chat bridge state service.
type codexPreflightAdapter struct {
	bridge *gatewaycodex.ChatBridgeStateService
}

// chainCodexBridgePreflight keeps the preflight port non-nil: the adapter
// mirrors the Node registry-miss continue branch when the bridge service is
// not wired.
func chainCodexBridgePreflight(bridge gatewaypreauth.CodexBridgePreflight) gatewaypreauth.CodexBridgePreflight {
	if bridge != nil {
		return bridge
	}
	return codexPreflightAdapter{}
}

func (a codexPreflightAdapter) CompactionExpectedForRequest(req *gatewaypreauth.GatewayRequest) bool {
	return gatewaycodex.CodexCompactionExpectedForRequest(req)
}

// auditSettingsAdapter implements gatewaypreauth.AuditSettings.
type auditSettingsAdapter struct {
	enabled func() bool
}

func (a auditSettingsAdapter) AuditLogEnabled() bool {
	return a.enabled != nil && a.enabled()
}

func (a codexPreflightAdapter) ApplyContextStatePreflight(_ context.Context, input gatewaypreauth.CodexContextStateInput) (bool, error) {
	// The context-state preflight finishes inside the bridge service; the
	// adapter degrades to "not completed" when the service is absent so the
	// request proceeds to dispatch (Node: registry miss → continue).
	_ = input
	return false, nil
}

func (a codexPreflightAdapter) ApplyChatBridgeCompactPreflight(_ context.Context, input gatewaypreauth.CodexCompactPreflightInput) (gatewaypreauth.CodexCompactPreflightResult, error) {
	// The context-state preflight finishes inside the bridge service; the
	// adapter degrades to "not completed" when the service is absent so the
	// request proceeds to dispatch (Node: registry miss → continue) with the
	// dispatch accounts passed through unchanged.
	return gatewaypreauth.CodexCompactPreflightResult{Completed: false, Accounts: input.DispatchAccounts}, nil
}

// ---------------------------------------------------------------------------
// audit plumbing
// ---------------------------------------------------------------------------

// auditDispatchAdapter implements gatewaypreauth.AuditDispatcher: finalized
// dropped captures POST to the F3 audit input server (Node
// dispatchAuditLogToGo).
type auditDispatchAdapter struct {
	target string
	logger *slog.Logger
	client *http.Client
}

func (a auditDispatchAdapter) Dispatch(input gatewaypreauth.DispatchedAuditLogInput) {
	if a.target == "" {
		return
	}
	status := input.FinalStatusCode
	payload := auditlog.AuditLogInput{
		ID:              input.ID,
		LifecycleStatus: auditlog.LifecycleStatus(input.LifecycleStatus),
		TraceID:         input.TraceID,
		TrafficSource:   auditlog.TrafficSource(input.TrafficSource),
		AuditOutcome:    auditlog.AuditOutcome(input.AuditOutcome),
		Success:         input.Success,
		Method:          input.Method,
		Path:            input.Path,
		QueryString:     input.QueryString,
		ClientIP:        input.ClientIP,
		UserAgent:       input.UserAgent,
		FinalStatusCode: &status,
		ErrorPhase:      input.ErrorPhase,
		ErrorCode:       input.ErrorCode,
		ErrorMessage:    input.ErrorMessage,
		SampleBucket:    input.SampleBucket,
		SampleReason:    input.SampleReason,
		CaptureStatus:   auditlog.AuditCaptureStatus(input.CaptureStatus),
		StartedAt:       input.StartedAt,
		EndedAt:         input.EndedAt,
	}
	a.post(auditlog.AuditInputPath, payload)
}

func (a auditDispatchAdapter) post(path string, payload any) {
	client := a.client
	if client == nil {
		client = http.DefaultClient
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	request, err := http.NewRequest(http.MethodPost, a.target+path, bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return
	}
	_ = response.Body.Close()
}

// auditUsageDispatcher implements gatewayusage.AuditDispatcher with the same
// input-server target.
type auditUsageDispatcher struct {
	target string
	logger *slog.Logger
	client *http.Client
}

func (d auditUsageDispatcher) DispatchAuditLog(ctx gatewayusage.Ctx, input gatewayusage.AuditLogInput) {
	if d.target == "" {
		return
	}
	client := d.client
	if client == nil {
		client = http.DefaultClient
	}
	body, err := json.Marshal(input)
	if err != nil {
		return
	}
	request, err := http.NewRequest(http.MethodPost, d.target+auditlog.AuditInputPath, bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return
	}
	_ = response.Body.Close()
}

// auditSettingsSourceAdapter implements gatewayusage.AuditLogSettingsSource.
type auditSettingsSourceAdapter struct {
	enabled func() bool
}

func (a auditSettingsSourceAdapter) ReadAuditLogSettings() gatewayusage.AuditLogSettings {
	enabled := a.enabled != nil && a.enabled()
	return gatewayusage.AuditLogSettings{Enabled: enabled}
}

// usageModelResolverAdapter implements gatewayusage.UsageModelResolver: the
// driver-owned upstream model resolution (registry.ts
// resolveGatewayUsageModel). Without a mapping the requested model passes
// through untouched.
type usageModelResolverAdapter struct{}

func (usageModelResolverAdapter) ResolveUsageModel(account gatewayusage.UsageModelAccount, requestedModel, sourceEndpointFamily string) gatewayusage.UsageModelResolution {
	return gatewayusage.UsageModelResolution{
		UpstreamModel:          requestedModel,
		ModelMappingApplied:    false,
		SourceEndpointFamily:   sourceEndpointFamily,
		UpstreamEndpointFamily: sourceEndpointFamily,
	}
}

// ---------------------------------------------------------------------------
// protocol gate helpers
// ---------------------------------------------------------------------------

func gatewayopenaiIsProtocolPath(pathAndQuery string) bool {
	return gatewayopenai.IsProtocolRequestPath(pathAndQuery)
}

func gatewayanthropicIsNative(r *http.Request) bool {
	return gatewayanthropic.IsNativeRequest(r)
}

func gatewaygeminiIsNative(r *http.Request) bool {
	return gatewaygemini.IsNativeRequest(r)
}

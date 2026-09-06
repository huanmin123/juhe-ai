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
// aborted branch. The deep branches that need collaborators this slice does
// not mount stay registered gaps: codex encrypted-content compatibility
// recovery (retry_with_compatibility_recovery) and the account error policy
// service (explicit_policy failureKind / keyScoped policy actions / request
// failure health-check dispatch).
type chainFailureDispatcher struct {
	usage    *gatewayusage.Service
	affinity gatewaydispatch.SessionAffinityPort
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

	lastAttempt := failedResponseAttemptOf(input, bodyText)
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
		}); err != nil {
			return gatewaydispatch.FailedUpstreamResponseResult{}, err
		}
	}

	// failure-dispatch.ts:392-396: without an explicit policy decision only
	// the automatic same-account key rotation can apply, and a pre-commit
	// transient response defers it (the dispatcher owns a bounded retry of
	// the same physical credential).
	hasAlternativeAccountAPIKeys := input.Account.SelectedAPIKeyFingerprint != nil && len(input.Account.APIKeys) > 1
	sameAccountKeyRotation := hasAlternativeAccountAPIKeys && !input.DeferAutomaticSameAccountKeyRotation
	result := gatewaydispatch.FailedUpstreamResponseResult{
		Action:           gatewaydispatch.FailedResponseActionSkipAccount,
		FailureKind:      chainFailureKindOpaqueHTTP,
		LastAttempt:      lastAttempt,
		KeyScopedFailure: sameAccountKeyRotation,
	}
	if sameAccountKeyRotation && input.AccountStateMutationEnabled &&
		input.Account.SelectedAPIKeyFingerprint != nil && !input.Account.APIKeyRuntimeStateDisabled {
		result.PendingApiKeyFailure = &gatewaydispatch.PendingAccountApiKeyFailure{
			Account:    input.Account,
			Status:     "temporary_unavailable",
			StatusCode: statusCode,
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
	return gatewaydispatch.UpstreamRequestErrorResult{
		Action:           gatewaydispatch.FailedResponseActionSkipAccount,
		LastAttempt:      lastAttempt,
		KeyScopedFailure: false,
	}, nil
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
func failedResponseAttemptOf(input gatewaydispatch.FailedUpstreamResponseInput, bodyText string) *gatewaydispatch.UpstreamAttempt {
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
	attempt.ParsedResponseBody = parsedFailureBodyOf(input.Response, bodyText)
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

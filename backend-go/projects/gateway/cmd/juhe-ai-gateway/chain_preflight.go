package main

// G20 phase-2 composition-root adapters:
//
//   - gatewaypreauth.GatewayAPIKeyValidator (service.go): the models fast
//     path raw-key validation over the gateway runtime cache read (Node
//     storage/gateway-api-key.repository.ts validateGatewayApiKeyAsync).
//   - gatewaypreauth.ImagePermissionPreflight (ports.go): the Go owner of
//     request/image-permission-preflight.ts, built from the gatewaybody
//     downgrade primitives (request/body.ts + image-permission-downgrade.ts).

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// chainAPIKeyValidator implements gatewaypreauth.GatewayAPIKeyValidator. The
// runtime cache read performs the full repository validation (status,
// expiry, owner join) and negative-caches the miss exactly like the Node
// gateway_api_key_validation cache, so Validate projects the row through.
type chainAPIKeyValidator struct {
	cache *gatewayruntimecache.Service
}

func (v *chainAPIKeyValidator) Validate(ctx context.Context, apiKey string) (*gatewayruntimecache.GatewayAPIKeyRow, error) {
	if apiKey == "" {
		return nil, nil
	}
	runtime, err := v.cache.ReadCachedGatewayRuntimeAsync(ctx, apiKey)
	if err != nil {
		return nil, err
	}
	if runtime.APIKey == nil {
		return nil, nil
	}
	row := gatewayruntimecache.CloneGatewayAPIKeyRow(*runtime.APIKey)
	return &row, nil
}

// chainImagePreflight implements gatewaypreauth.ImagePermissionPreflight,
// mirroring applyOpenAIGatewayImagePermissionPreflight: disabled-permission
// detection, oversized auto-downgrade rejection, auto tool removal with the
// text-lane downgrade, the deferred forced-tool continue and the final 403.
type chainImagePreflight struct {
	preauth *gatewaypreauth.Service
}

func (p *chainImagePreflight) Apply(ctx context.Context, input gatewaypreauth.ImagePermissionPreflightInput) (gatewaypreauth.ImagePermissionPreflightResult, error) {
	requestLane := gatewayproto.RequestLane(input.RequestLane)
	if !gatewaypreauth.IsImageGenerationDisabledForAPIKey(input.APIKeyRecord, requestLane) {
		return gatewaypreauth.ImagePermissionPreflightResult{Completed: false, RequestLane: string(requestLane)}, nil
	}

	imageEndpointOrModel := gatewaypreauth.IsOpenAIGatewayImageEndpointOrModelRequest(input.Req)
	if !imageEndpointOrModel && p.rejectOversizedAutoDowngrade(input) {
		return gatewaypreauth.ImagePermissionPreflightResult{Completed: true}, nil
	}

	downgrade := gatewaybody.ImageGenerationToolDowngradeResult{Reason: gatewaybody.DowngradeReasonImageEndpointOrModel}
	if !imageEndpointOrModel {
		downgrade = gatewaybody.DowngradeGatewayAutoImageGenerationTool(bodyOf(input.Req))
	}
	if downgrade.Downgraded {
		// Node image-permission-preflight.ts:62-68 logs the auto tool
		// downgrade before the audit metadata.
		if p.preauth != nil && p.preauth.Observability != nil {
			p.preauth.Observability.Logger().Warn("gateway_image_generation_tool_downgraded", map[string]any{
				"removedToolCount": downgrade.RemovedToolCount,
				"systemAccountId":  input.SystemAccountID,
				"apiKeyId":         input.APIKeyID,
				"groupId":          input.GroupID,
			}, "系统账户未开启图像生成，已移除 Responses auto 图像生成工具并按文本请求继续")
		}
		input.AuditCapture.AddGatewayMetadata("system_account_image_generation_permission", map[string]any{
			"allowed":          false,
			"downgraded":       true,
			"removedToolCount": downgrade.RemovedToolCount,
			"reason":           string(downgrade.Reason),
		})
		return gatewaypreauth.ImagePermissionPreflightResult{Completed: false, RequestLane: string(gatewaypreauth.RequestLaneText)}, nil
	}

	if downgrade.Reason == gatewaybody.DowngradeReasonForcedImageGenerationTool && input.DeferForcedImageGenerationTool {
		input.AuditCapture.AddGatewayMetadata("system_account_image_generation_permission", map[string]any{
			"allowed":    false,
			"downgraded": false,
			"deferred":   true,
			"reason":     string(downgrade.Reason),
		})
		return gatewaypreauth.ImagePermissionPreflightResult{Completed: false, RequestLane: string(requestLane)}, nil
	}

	if downgrade.Reason == gatewaybody.DowngradeReasonInvalidJSON {
		p.preauth.SendInvalidJSONGatewayResponse(ctx, gatewaypreauth.InvalidJSONResponseInput{
			Req:             input.Req,
			Res:             input.Res,
			AuditCapture:    input.AuditCapture,
			UsageContext:    input.UsageContext,
			StartedAt:       input.StartedAt,
			SystemAccountID: input.SystemAccountID,
			APIKeyID:        input.APIKeyID,
			GroupID:         input.GroupID,
			ClientIP:        input.ClientIP,
			Endpoint:        input.Endpoint,
		})
		return gatewaypreauth.ImagePermissionPreflightResult{Completed: true}, nil
	}

	if downgrade.Reason == gatewaybody.DowngradeReasonJSONWorkerOverloaded {
		responsePayload := gatewaypreauth.GatewayErrorPayloadOf("网关请求解析繁忙，请稍后重试", "server_overloaded", "server_overloaded")
		input.AuditCapture.AddGatewayMetadata("system_account_image_generation_permission", map[string]any{
			"allowed":    false,
			"downgraded": false,
			"reason":     string(downgrade.Reason),
		})
		// Node's json_worker_overloaded arm sets no Connection header (the
		// 503 body rides the normal failure response contract).
		p.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
			Req:             input.Req,
			Res:             input.Res,
			AuditCapture:    input.AuditCapture,
			UsageContext:    input.UsageContext,
			StartedAt:       input.StartedAt,
			StatusCode:      503,
			ResponsePayload: responsePayload,
			Audit: gatewaypreauth.FailureAudit{
				Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
				ErrorPhase:   "request_validation",
				ErrorCode:    "server_overloaded",
				ErrorMessage: responsePayload.Error.Message,
			},
		})
		return gatewaypreauth.ImagePermissionPreflightResult{Completed: true}, nil
	}

	input.AuditCapture.AddGatewayMetadata("system_account_image_generation_permission", map[string]any{
		"allowed":    false,
		"downgraded": false,
		"reason":     string(downgrade.Reason),
	})
	responsePayload := gatewaypreauth.GatewayErrorPayloadOf(gatewaypreauth.ImageGenerationDisabledMessage, "forbidden", gatewaypreauth.ImageGenerationDisabledCode)
	p.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             input.Req,
		Res:             input.Res,
		AuditCapture:    input.AuditCapture,
		UsageContext:    input.UsageContext,
		StartedAt:       input.StartedAt,
		StatusCode:      403,
		ResponsePayload: responsePayload,
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
			ErrorPhase:   "authorization",
			ErrorCode:    gatewaypreauth.ImageGenerationDisabledCode,
			ErrorMessage: responsePayload.Error.Message,
		},
	})
	return gatewaypreauth.ImagePermissionPreflightResult{Completed: true}, nil
}

// rejectOversizedAutoDowngrade mirrors rejectOversizedAutoImageGenerationTextDowngrade:
// a scanned JSON body that only downgrades to text but exceeds the text raw
// body limit is rejected with 413 and the in-flight lease released.
func (p *chainImagePreflight) rejectOversizedAutoDowngrade(input gatewaypreauth.ImagePermissionPreflightInput) bool {
	req := input.Req
	if req == nil || req.Body == nil || req.BodyState() == nil {
		return false
	}
	state := req.BodyState()
	rawBody := req.Body.RawBody
	if len(rawBody) == 0 ||
		!gatewaybody.IsScannedJSONBody(req.Body) ||
		!state.ImageGeneration ||
		state.ImageGenerationForced {
		return false
	}
	limitBytes := gatewaybody.GatewayTextRawBodyLimitBytes(int(derefInt64(input.GatewayTextRawBodyLimitMegabytes)), input.GatewayTextRawBodyLimitMegabytes != nil)
	if len(rawBody) <= limitBytes {
		return false
	}
	input.AuditCapture.AddGatewayMetadata("system_account_image_generation_permission", map[string]any{
		"allowed":               false,
		"downgraded":            false,
		"reason":                "auto_image_generation_text_body_too_large",
		"rawBodyBytes":          len(rawBody),
		"textRawBodyLimitBytes": limitBytes,
	})
	req.Body.RawBody = nil
	req.Body.Body = nil
	req.Body.ReleaseInFlight()
	responsePayload := gatewaypreauth.GatewayErrorPayloadOf("请求体过大", "request_too_large", "request_body_too_large")
	p.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             req,
		Res:             input.Res,
		AuditCapture:    input.AuditCapture,
		UsageContext:    input.UsageContext,
		StartedAt:       input.StartedAt,
		StatusCode:      413,
		ResponsePayload: responsePayload,
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
			ErrorPhase:   "request_validation",
			ErrorCode:    "request_body_too_large",
			ErrorMessage: responsePayload.Error.Message,
		},
	})
	return true
}

func bodyOf(req *gatewaypreauth.GatewayRequest) *gatewaybody.Request {
	if req == nil {
		return nil
	}
	return req.Body
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// ---------------------------------------------------------------------------
// speed-first body admission (server.ts:496 admitSpeedFirstRequestBody)
// ---------------------------------------------------------------------------

// chainSpeedFirstBodyAdmissionGate mirrors admitSpeedFirstRequestBody
// (backend/src/server.ts:496 + request/speed-first-body-admission.middleware.ts):
// the body-stage back-pressure for speed_first keys bound to high_concurrency
// groups. When the group's aggregate account concurrency is saturated the
// request queues for the policy's queue budget and otherwise answers the Node
// 429「当前分组繁忙…」contract.
//
// The gate is an independently mountable composition-root adapter: it takes
// the resolved gateway runtime snapshot (req.gatewayRuntime), the lane the
// orchestrator already computed, and the shared G13b
// gatewayhotquality body-admission registry (the module-global Node `states`
// map). Mount point (mirrors the Node middleware order
// rejectGatewayRawBodyByContentLength -> admitSpeedFirstRequestBody ->
// parseGatewayRawBody): inside handleOpenAIGatewayRequest after
// bodyPipeline.RejectByContentLength and before bodyPipeline.ReadRawBody.
type chainSpeedFirstBodyAdmissionGate struct {
	preauth *gatewaypreauth.Service
	// QueueDefaults carries the runtimeConfig.concurrency.globalMax derived
	// DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY values
	// (maxQueueSize / perApiKeyQueueLimit); maxQueueWaitMs keeps the static
	// 60_000 default.
	QueueDefaults gatewayclientip.HighConcurrencyPolicyDefaults
	// Recorder records the 429 body rejection for audit/usage (Node
	// recordGatewayBodyRejection); nil keeps the write-out disabled, matching
	// the current body pipeline wiring.
	Recorder gatewaybody.RejectionRecorder
}

// chainSpeedFirstBodyAdmissionOutcome mirrors the middleware continuation:
// Handled=true means the gate already ended the request (429 or client
// abort); Handled=false with a non-nil Release admits the request and hands
// the lease to the orchestrator, which must call Release once the response
// completes (Node res.once('finish'/'close', release)).
type chainSpeedFirstBodyAdmissionOutcome struct {
	Release func()
	Handled bool
}

// AdmitBody mirrors admitSpeedFirstRequestBody. The error return carries the
// scheduling-policy validation failure (Node throw -> next(error)).
func (g *chainSpeedFirstBodyAdmissionGate) AdmitBody(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	res gatewaypreauth.GatewayResponseWriter,
	requestLane gatewayproto.RequestLane,
) (chainSpeedFirstBodyAdmissionOutcome, error) {
	if g == nil || g.preauth == nil {
		return chainSpeedFirstBodyAdmissionOutcome{}, nil
	}
	stageStartedAt := g.preauth.StartedAt()
	runtime := req.Runtime
	if !chainSpeedFirstBodyAdmissionApplies(runtime, requestLane) {
		g.preauth.Observability.LogRequestStage("body.speed_first_admission", map[string]any{
			"admissionMode": "speed_first_high_concurrency",
			"applicable":    false,
			"requestLane":   string(requestLane),
		}, "skipped", stageStartedAt)
		return chainSpeedFirstBodyAdmissionOutcome{}, nil
	}
	apiKey := runtime.APIKey
	groupAccess := runtime.GroupAccess

	maxQueueWaitMs, maxQueueSize, perAPIKeyQueueLimit, err := chainSpeedFirstQueuePolicy(groupAccess.SchedulingPolicy, g.QueueDefaults)
	if err != nil {
		return chainSpeedFirstBodyAdmissionOutcome{}, err
	}
	capacity := chainBodyAdmissionCapacity(runtime.Accounts)
	decision := gatewayhotquality.AcquireSpeedFirstBodyAdmission(ctx, gatewayhotquality.SpeedFirstBodyAdmissionInput{
		SystemAccountID:     apiKey.SystemAccountID,
		RouteStrategyID:     apiKey.RouteStrategyID,
		GroupID:             apiKey.SelectedGroupID,
		APIKeyID:            apiKey.ID,
		Capacity:            capacity,
		MaxQueueWaitMs:      maxQueueWaitMs,
		MaxQueueSize:        maxQueueSize,
		PerAPIKeyQueueLimit: perAPIKeyQueueLimit,
	})

	if !decision.Acquired {
		if decision.Reason == gatewayhotquality.BodyAdmissionRejectAborted || (ctx != nil && ctx.Err() != nil) {
			g.preauth.Observability.LogRequestStage("body.speed_first_admission", map[string]any{
				"admissionMode": "speed_first_high_concurrency",
				"reason":        string(decision.Reason),
			}, "aborted", stageStartedAt)
			return chainSpeedFirstBodyAdmissionOutcome{Handled: true}, nil
		}
		message := "当前分组繁忙，请稍后重试或增加可用账户。"
		failureReason := "speed_first_body_admission_" + string(decision.Reason)
		res.Header().Set("Connection", "close")
		if g.Recorder != nil {
			g.Recorder.RecordGatewayBodyRejection(req.HTTP, req.Body, gatewaybody.RejectionInput{
				StatusCode:      http.StatusTooManyRequests,
				ResponsePayload: gatewaybody.GatewayErrorPayload(message, "rate_limit_error", ""),
				RawBodyBytes:    chainContentLengthBytes(req),
				Reason:          gatewaybody.RejectReasonGatewayBodyAdmission,
				ErrorCode:       failureReason,
				ErrorMessage:    message,
			})
		}
		gatewaypreauth.SendGatewayJSONError(res, http.StatusTooManyRequests,
			gatewaypreauth.GatewayErrorPayloadOf(message, "rate_limit_error"),
			gatewaypreauth.SendGatewayErrorOptions{})
		g.preauth.Observability.LogRequestStage("body.speed_first_admission", map[string]any{
			"admissionMode": "speed_first_high_concurrency",
			"failureReason": failureReason,
			"decisionInputs": map[string]any{
				"reason":              string(decision.Reason),
				"capacity":            capacity,
				"maxQueueWaitMs":      maxQueueWaitMs,
				"maxQueueSize":        maxQueueSize,
				"perApiKeyQueueLimit": perAPIKeyQueueLimit,
			},
		}, "expected_failure", stageStartedAt)
		return chainSpeedFirstBodyAdmissionOutcome{Handled: true}, nil
	}

	// Idempotent release (Node released flag + res finish/close listeners;
	// the orchestrator calls Release when the response completes).
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(decision.Release) }
	if ctx != nil && ctx.Err() != nil {
		release()
		g.preauth.Observability.LogRequestStage("body.speed_first_admission", map[string]any{
			"admissionMode": "speed_first_high_concurrency",
		}, "aborted", stageStartedAt)
		return chainSpeedFirstBodyAdmissionOutcome{Handled: true}, nil
	}
	g.preauth.Observability.LogRequestStage("body.speed_first_admission", map[string]any{
		"admissionMode": "speed_first_high_concurrency",
		"acquired":      true,
		"capacity":      capacity,
	}, "success", stageStartedAt)
	return chainSpeedFirstBodyAdmissionOutcome{Release: release}, nil
}

// chainSpeedFirstBodyAdmissionApplies mirrors the applicability guard:
// normal-mode speed_first key on a high_concurrency group with hydrated
// accounts, off the image lane.
func chainSpeedFirstBodyAdmissionApplies(runtime *gatewayruntimecache.GatewayRuntime, requestLane gatewayproto.RequestLane) bool {
	if runtime == nil || runtime.APIKey == nil || runtime.GroupAccess == nil {
		return false
	}
	apiKey := runtime.APIKey
	if apiKey.RouteStrategyMode != gatewayruntimecache.RouteStrategyModeNormal {
		return false
	}
	if apiKey.NormalRoutingConfig == nil || apiKey.NormalRoutingConfig.SchedulingPreference != "speed_first" {
		return false
	}
	if groupType := runtime.GroupAccess.GroupType; groupType == nil || *groupType != "high_concurrency" {
		return false
	}
	if len(runtime.Accounts) == 0 {
		return false
	}
	return requestLane != gatewayproto.LaneImage
}

// chainSpeedFirstQueuePolicy projects the consumed queue subset off the
// high_concurrency scheduling_policy_json payload (Node
// resolveGroupSchedulingPolicy('high_concurrency', ...) ?? DEFAULT: the
// bounded validators over maxQueueSize / perApiKeyQueueLimit /
// maxQueueWaitMs; malformed values fail the request like the Node throw).
func chainSpeedFirstQueuePolicy(value *gatewayruntimecache.GroupSchedulingPolicy, defaults gatewayclientip.HighConcurrencyPolicyDefaults) (maxQueueWaitMs int64, maxQueueSize int, perAPIKeyQueueLimit int, err error) {
	policy, err := gatewayclientip.ResolveGroupSchedulingPolicy(derefGroupSchedulingPolicy(value), defaults)
	if err != nil {
		return 0, 0, 0, err
	}
	return policy.MaxQueueWaitMs, policy.MaxQueueSize, policy.PerAPIKeyQueueLimit, nil
}

func derefGroupSchedulingPolicy(value *gatewayruntimecache.GroupSchedulingPolicy) map[string]any {
	if value == nil {
		return nil
	}
	return *value
}

// chainBodyAdmissionCapacity mirrors bodyAdmissionCapacity +
// gatewayAccountConcurrencyLimitsByAccountId: per resolved concurrency
// account (credentialSourceAccountId ?? id) the smallest positive
// concurrency limit, summed across accounts.
func chainBodyAdmissionCapacity(accounts []gatewayruntimecache.OpenAIAccountSecret) int {
	limits := map[string]int{}
	for _, account := range accounts {
		accountID := strings.TrimSpace(account.ID)
		if source := strings.TrimSpace(chainDerefString(account.CredentialSourceAccountID)); source != "" {
			accountID = source
		}
		if accountID == "" {
			continue
		}
		limit := account.ConcurrencyLimit
		if limit < 1 {
			limit = 1
		}
		if existing, ok := limits[accountID]; !ok || limit < existing {
			limits[accountID] = limit
		}
	}
	total := 0
	for _, limit := range limits {
		total += limit
	}
	return total
}

// chainContentLengthBytes mirrors contentLengthBytes: the non-negative
// content-length header, 0 when absent or malformed.
func chainContentLengthBytes(req *gatewaypreauth.GatewayRequest) int {
	value := strings.TrimSpace(req.Header("content-length"))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return int(parsed)
}

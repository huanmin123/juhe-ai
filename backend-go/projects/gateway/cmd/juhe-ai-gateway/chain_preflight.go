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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
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
		input.Res.Header().Set("Connection", "close")
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

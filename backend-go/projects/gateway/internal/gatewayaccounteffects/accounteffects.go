package gatewayaccounteffects

import (
	"context"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// AccountEffects mirrors the enqueue facade of account-effects.ts:
// applyAccountErrorHandlingWithCacheInvalidation and the temporary
// unavailability / stream failure clearing side effects.
type AccountEffects struct {
	service *SideEffectsService
	deps    SideEffectDeps
	config  SideEffectsConfig
	now     func() string
	traceID func() string
}

// AccountEffectsDeps carries the facade ports.
type AccountEffectsDeps struct {
	// TraceID mirrors getTraceId(); empty when outside a request.
	TraceID func() string
}

// NewAccountEffects builds the facade on top of the side effects service.
func NewAccountEffects(service *SideEffectsService, config SideEffectsConfig, deps SideEffectDeps, effects AccountEffectsDeps) *AccountEffects {
	traceID := effects.TraceID
	if traceID == nil {
		traceID = func() string { return "" }
	}
	return &AccountEffects{
		service: service,
		deps:    deps,
		config:  config,
		traceID: traceID,
	}
}

// AccountErrorHandlingRequest mirrors applyAccountErrorHandlingWithCacheInvalidation's
// input.
type AccountErrorHandlingRequest struct {
	Success                      bool
	StatusCode                   *int64
	Headers                      map[string][]string
	BodyText                     *string
	ErrorMessage                 *string
	UpstreamErrorSummary         *string
	UpstreamErrorSummaryResolved *bool
	TrafficSource                string
	PolicyDecision               any
}

// ApplyAccountErrorHandlingWithCacheInvalidation mirrors
// applyAccountErrorHandlingWithCacheInvalidation: normalized input plus the
// enqueue call.
func (f *AccountEffects) ApplyAccountErrorHandlingWithCacheInvalidation(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, input AccountErrorHandlingRequest) error {
	normalized := AccountErrorHandlingInput{
		Success:                      input.Success,
		StatusCode:                   input.StatusCode,
		Headers:                      input.Headers,
		BodyText:                     input.BodyText,
		ErrorMessage:                 input.ErrorMessage,
		UpstreamErrorSummary:         input.UpstreamErrorSummary,
		UpstreamErrorSummaryResolved: input.UpstreamErrorSummaryResolved,
		TraceID:                      optionalText(f.traceID()),
		ObservedAt:                   canonicalRFC3339(f.service.clock.Now()),
		DispatchRevision:             account.DispatchRevision,
		TrafficSource:                input.TrafficSource,
		PolicyDecision:               input.PolicyDecision,
	}
	return f.service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, AccountSideEffectOperation{
		Type:    AccountSideEffectOperationType,
		Account: account,
		Input:   normalized,
	})
}

// TemporaryUnavailableWriter is the mark_account_temporary_unavailable port.
type TemporaryUnavailableWriter interface {
	MarkAccountTemporaryUnavailable(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, reason string, traceID string) (bool, error)
}

// MarkGatewayAccountTemporaryUnavailableWithCacheInvalidation mirrors
// markGatewayAccountTemporaryUnavailableWithCacheInvalidation.
func (f *AccountEffects) MarkGatewayAccountTemporaryUnavailableWithCacheInvalidation(ctx context.Context, writer TemporaryUnavailableWriter, account gatewayruntimecache.OpenAIAccountSecret, reason string, source string) bool {
	updated, err := writer.MarkAccountTemporaryUnavailable(ctx, account, truncateRunes(reason, 1000), f.traceID())
	if err != nil {
		f.deps.Logger.Warn(map[string]any{
			"event":     "gateway_account_temporary_unavailable_side_effect_failed",
			"accountId": account.ID,
			"source":    source,
		}, "网关账号临时不可调用副作用写入失败")
		return false
	}
	if !updated {
		return false
	}
	runtimeKey, keyErr := GatewayAccountRuntimeKeyForSecret(account)
	if keyErr != nil {
		runtimeKey = account.ID
	}
	cleared := false
	if f.deps.ClearRuntimeAvailabilityLocal != nil {
		cleared = f.deps.ClearRuntimeAvailabilityLocal(runtimeKey)
	}
	if !cleared && f.deps.InvalidateRuntimeCache != nil {
		f.deps.InvalidateRuntimeCache()
	}
	return true
}

// StreamFailureStateClearer is the clear_account_stream_failure_state port.
type StreamFailureStateClearer interface {
	ClearAccountStreamFailureState(ctx context.Context, accountID string) (bool, error)
}

// ClearAccountStreamFailureStateWithCacheInvalidation mirrors
// clearAccountStreamFailureStateWithCacheInvalidation (fire and forget).
func (f *AccountEffects) ClearAccountStreamFailureStateWithCacheInvalidation(ctx context.Context, clearer StreamFailureStateClearer, account gatewayruntimecache.OpenAIAccountSecret) {
	go func() {
		changed, err := clearer.ClearAccountStreamFailureState(context.WithoutCancel(ctx), account.ID)
		if err != nil {
			f.deps.Logger.Warn(map[string]any{
				"event":     "gateway_account_stream_failure_clear_failed",
				"accountId": account.ID,
			}, "网关清理账号流式失败计数失败")
			return
		}
		if changed && f.deps.InvalidateRuntimeCache != nil {
			f.deps.InvalidateRuntimeCache()
		}
	}()
}

// HandleStreamFailure mirrors handleStreamFailure: stream framing and
// protocol observations are request-local. Only the transport circuit or an
// explicit user policy may authorize shared state.
func (f *AccountEffects) HandleStreamFailure() error { return nil }

// truncateRunes mirrors String.prototype.slice(0, 1000) semantics (UTF-16
// code units approximated by runes).
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func optionalText(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copied := value
	return &copied
}

package gatewayquota

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"
)

// In-flight quota constants mirror api-key-inflight-quota.service.ts.
const (
	// DefaultEstimatedOutputTokens mirrors defaultEstimatedOutputTokens.
	DefaultEstimatedOutputTokens int64 = 4096
	// DefaultReleaseDelayMs mirrors defaultReleaseDelayMs (65s).
	DefaultReleaseDelayMs = 65_000
	// reservationLeakTTL mirrors reservationLeakTtlMs (30min leak guard).
	reservationLeakTTL = 30 * time.Minute
)

// CostEstimator is the model-pricing port (estimateCatalogCostUsdAsync).
// ok=false mirrors the undefined estimate (no inflight reservation).
type CostEstimator interface {
	EstimateCatalogCostUSD(ctx context.Context, input CatalogCostInput) (float64, bool)
}

// CatalogCostInput mirrors estimateCatalogCostUsdAsync's input.
type CatalogCostInput struct {
	ProviderCode    string
	SystemAccountID string
	Model           string
	ServiceTier     string
	InputTokens     int64
	OutputTokens    int64
}

// EstimateRequestInput carries the gateway request facts the estimate derives
// from (body state + request model are upstream slices).
type EstimateRequestInput struct {
	ProviderCode    string
	SystemAccountID string
	Model           string
	// ServiceTier defaults to "default" when empty.
	ServiceTier string
	// RawBodyBytes mirrors bodyState?.rawBodyBytes (caller default:
	// Buffer.byteLength(JSON.stringify(req.body ?? {}))).
	RawBodyBytes int64
	// MaxOutputTokens mirrors bodyState?.maxOutputTokens (default 4096).
	HasMaxOutputTokens bool
	MaxOutputTokens    int64
}

// EstimateGatewayRequestCostUsd mirrors estimateGatewayRequestCostUsd: the
// token math (inputTokens = max(1, ceil(bytes/4))) lives here so callers only
// supply the request facts.
func EstimateGatewayRequestCostUsd(ctx context.Context, estimator CostEstimator, input EstimateRequestInput) (float64, bool) {
	if estimator == nil {
		return 0, false
	}
	inputTokens := (input.RawBodyBytes + 3) / 4
	if inputTokens < 1 {
		inputTokens = 1
	}
	outputTokens := DefaultEstimatedOutputTokens
	if input.HasMaxOutputTokens {
		outputTokens = input.MaxOutputTokens
	}
	serviceTier := input.ServiceTier
	if serviceTier == "" {
		serviceTier = "default"
	}
	return estimator.EstimateCatalogCostUSD(ctx, CatalogCostInput{
		ProviderCode:    input.ProviderCode,
		SystemAccountID: input.SystemAccountID,
		Model:           input.Model,
		ServiceTier:     serviceTier,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
	})
}

// InflightDecision mirrors ApiKeyInflightQuotaDecision:
// {allowed:true, estimatedCostUsd?, reservation?} | {allowed:false,
// estimatedCostUsd}. HasEstimatedCostUsd distinguishes the absent estimate.
type InflightDecision struct {
	Allowed             bool
	EstimatedCostUsd    float64
	HasEstimatedCostUsd bool
	Reservation         *InflightReservation
}

// InflightReservation mirrors ApiKeyInflightQuotaReservation (Complete only).
type InflightReservation struct {
	complete func()
}

// Complete mirrors reservation.complete(): stops the leak timer and schedules
// the release after the completion delay.
func (r *InflightReservation) Complete() {
	if r == nil || r.complete == nil {
		return
	}
	r.complete()
}

type inflightState struct {
	reservedCostUsd float64
}

// TimerScheduler schedules delayed callbacks (Node setTimeout/unref). The
// default uses time.AfterFunc; tests inject a manual scheduler.
type TimerScheduler func(delay time.Duration, fn func()) (stop func())

func defaultTimerScheduler(delay time.Duration, fn func()) (stop func()) {
	timer := time.AfterFunc(delay, fn)
	return func() { timer.Stop() }
}

// InflightQuotaConfig wires the in-flight service.
type InflightQuotaConfig struct {
	APIKeys   *APIKeyQuotaService
	DBService DBServiceClient
	Estimator CostEstimator
	Log       LogHook
	// Timer overrides the release scheduler (tests); nil uses time.AfterFunc.
	Timer TimerScheduler
}

// InflightQuotaService ports api-key-inflight-quota.service.ts: per api-key
// reserved cost accumulation with leak-guarded reservations. The state is
// intentionally process-local exactly like the Node module (the in-flight
// counter never crosses the runtime state driver).
type InflightQuotaService struct {
	apiKeys   *APIKeyQuotaService
	dbService DBServiceClient
	estimator CostEstimator
	log       LogHook
	timer     TimerScheduler

	mu     sync.Mutex
	states map[string]*inflightState
}

// NewInflightQuotaService builds the in-flight tracker.
func NewInflightQuotaService(cfg InflightQuotaConfig) (*InflightQuotaService, error) {
	log := cfg.Log
	if log == nil {
		log = noopLog
	}
	timer := cfg.Timer
	if timer == nil {
		timer = defaultTimerScheduler
	}
	return &InflightQuotaService{
		apiKeys:   cfg.APIKeys,
		dbService: cfg.DBService,
		estimator: cfg.Estimator,
		log:       log,
		timer:     timer,
		states:    map[string]*inflightState{},
	}, nil
}

// normalizedCost mirrors normalizedCost: clamp at 0 and round to 10 decimals
// (Number(Math.max(0, value).toFixed(10))); non-finite collapses to 0.
func normalizedCost(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value < 0 {
		value = 0
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 10, 64), 64)
	if err != nil {
		return 0
	}
	return rounded
}

// addCostToAllWindows mirrors addCostToAllWindows.
func addCostToAllWindows(costs RequestQuotaCosts, amount float64) RequestQuotaCosts {
	return RequestQuotaCosts{
		Hourly:  costs.Hourly + amount,
		Daily:   costs.Daily + amount,
		Weekly:  costs.Weekly + amount,
		Monthly: costs.Monthly + amount,
		Total:   costs.Total + amount,
	}
}

// isProjectedRequestQuotaExceeded mirrors isProjectedRequestQuotaExceeded —
// note the STRICT > comparison (unlike IsRequestQuotaExceeded's >=): the
// projection includes the not-yet-billed estimate, so equality stays allowed.
func isProjectedRequestQuotaExceeded(limits RequestQuotaLimits, costs RequestQuotaCosts) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled && costs.Hourly > limits.Hourly.Limit) ||
		(limits.Daily != nil && limits.Daily.Enabled && costs.Daily > limits.Daily.Limit) ||
		(limits.Weekly != nil && limits.Weekly.Enabled && costs.Weekly > limits.Weekly.Limit) ||
		(limits.Monthly != nil && limits.Monthly.Enabled && costs.Monthly > limits.Monthly.Limit) ||
		(limits.Total != nil && limits.Total.Enabled && costs.Total > limits.Total.Limit)
}

// ReserveInput mirrors reserveApiKeyInflightCost's input.
type ReserveInput struct {
	APIKeyID         string
	Limits           RequestQuotaLimits
	CurrentCosts     RequestQuotaCosts
	EstimatedCostUsd float64
	// ReleaseDelayMs overrides DefaultReleaseDelayMs when non-nil.
	ReleaseDelayMs *int
}

// Reserve mirrors reserveApiKeyInflightCost: project current + reserved +
// estimate against every enabled window, reserve on pass. The check and the
// reservation share one critical section so concurrent reservations observe
// each other (Node is single-threaded, where this ordering is implicit).
func (s *InflightQuotaService) Reserve(input ReserveInput) InflightDecision {
	estimatedCostUsd := normalizedCost(input.EstimatedCostUsd)
	if estimatedCostUsd <= 0 {
		return InflightDecision{Allowed: true}
	}
	s.mu.Lock()
	state, ok := s.states[input.APIKeyID]
	if !ok {
		state = &inflightState{reservedCostUsd: 0}
	}
	projected := addCostToAllWindows(input.CurrentCosts, state.reservedCostUsd+estimatedCostUsd)
	if isProjectedRequestQuotaExceeded(input.Limits, projected) {
		s.mu.Unlock()
		return InflightDecision{Allowed: false, EstimatedCostUsd: estimatedCostUsd, HasEstimatedCostUsd: true}
	}
	state.reservedCostUsd = normalizedCost(state.reservedCostUsd + estimatedCostUsd)
	s.states[input.APIKeyID] = state
	s.mu.Unlock()
	releaseDelay := DefaultReleaseDelayMs
	if input.ReleaseDelayMs != nil {
		releaseDelay = *input.ReleaseDelayMs
	}
	return InflightDecision{
		Allowed:             true,
		EstimatedCostUsd:    estimatedCostUsd,
		HasEstimatedCostUsd: true,
		Reservation:         s.createReservation(input.APIKeyID, estimatedCostUsd, releaseDelay),
	}
}

// GatewayReserveInput mirrors reserveGatewayApiKeyInflightCost's input.
type GatewayReserveInput struct {
	APIKey       APIKeyRow
	ProviderCode string
	Estimate     EstimateRequestInput
}

// ReserveGatewayCost mirrors reserveGatewayApiKeyInflightCost: enabled-limit
// gate, catalog estimate, snapshot cost with DB-service fallback, then the
// synchronous reservation core.
func (s *InflightQuotaService) ReserveGatewayCost(ctx context.Context, input GatewayReserveInput) (InflightDecision, error) {
	limits, err := ParseRequestQuotaLimitsJSON(input.APIKey.QuotaLimitsJSON)
	if err != nil {
		return InflightDecision{}, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return InflightDecision{Allowed: true}, nil
	}
	estimatedCostUsd, ok := EstimateGatewayRequestCostUsd(ctx, s.estimator, input.Estimate)
	if !ok || estimatedCostUsd <= 0 {
		return InflightDecision{Allowed: true}, nil
	}
	var currentCosts RequestQuotaCosts
	found := false
	if s.apiKeys != nil {
		currentCosts, found, err = s.apiKeys.ReadAPIKeyQuotaCostsSnapshotAsync(ctx, input.APIKey)
		if err != nil {
			return InflightDecision{}, err
		}
	}
	if !found {
		if s.dbService == nil {
			s.log("gateway_api_key_inflight_quota_exact_cost_failed", map[string]any{
				"apiKeyId": input.APIKey.ID,
				"error":    "db service client is not configured",
			}, "API Key 在途额度缺少成本快照且精确成本读取失败，按保护策略拒绝请求")
			return InflightDecision{Allowed: false, EstimatedCostUsd: estimatedCostUsd, HasEstimatedCostUsd: true}, nil
		}
		currentCosts, err = s.dbService.ReadAPIKeyQuotaCosts(ctx, input.APIKey)
		if err != nil {
			s.log("gateway_api_key_inflight_quota_exact_cost_failed", map[string]any{
				"apiKeyId": input.APIKey.ID,
				"error":    err.Error(),
			}, "API Key 在途额度缺少成本快照且精确成本读取失败，按保护策略拒绝请求")
			return InflightDecision{Allowed: false, EstimatedCostUsd: estimatedCostUsd, HasEstimatedCostUsd: true}, nil
		}
	}
	return s.Reserve(ReserveInput{
		APIKeyID:         input.APIKey.ID,
		Limits:           limits,
		CurrentCosts:     currentCosts,
		EstimatedCostUsd: estimatedCostUsd,
	}), nil
}

// createReservation mirrors createReservation: immediate release via the
// 30-minute leak timer, rescheduled by Complete() to the release delay.
func (s *InflightQuotaService) createReservation(apiKeyID string, costUsd float64, releaseDelayMs int) *InflightReservation {
	var releaseOnce sync.Once
	var completeOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			s.release(apiKeyID, costUsd)
		})
	}
	stopLeak := s.timer(reservationLeakTTL, release)
	return &InflightReservation{complete: func() {
		completeOnce.Do(func() {
			stopLeak()
			delay := time.Duration(releaseDelayMs) * time.Millisecond
			if delay < 0 {
				delay = 0
			}
			s.timer(delay, release)
		})
	}}
}

func (s *InflightQuotaService) release(apiKeyID string, costUsd float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[apiKeyID]
	if !ok {
		return
	}
	state.reservedCostUsd = normalizedCost(math.Max(0, state.reservedCostUsd-costUsd))
	if state.reservedCostUsd == 0 {
		delete(s.states, apiKeyID)
	}
}

// APIKeyInflightQuotaState mirrors apiKeyInflightQuotaSnapshot's element.
type APIKeyInflightQuotaState struct {
	APIKeyID        string
	ReservedCostUsd float64
}

// Snapshot mirrors apiKeyInflightQuotaSnapshot.
func (s *InflightQuotaService) Snapshot() []APIKeyInflightQuotaState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]APIKeyInflightQuotaState, 0, len(s.states))
	for apiKeyID, state := range s.states {
		out = append(out, APIKeyInflightQuotaState{APIKeyID: apiKeyID, ReservedCostUsd: state.reservedCostUsd})
	}
	return out
}

// ClearForTest mirrors clearApiKeyInflightQuotaReservationsForTest.
func (s *InflightQuotaService) ClearForTest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = map[string]*inflightState{}
}

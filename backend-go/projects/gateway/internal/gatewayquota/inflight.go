package gatewayquota

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
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
// DeniedScopeKey 标记被拒的是哪条 scope 状态键（API Key 或授权/团队 scope），
// 调用方据此选择对外文案（T93-4）。
type InflightDecision struct {
	Allowed             bool
	EstimatedCostUsd    float64
	HasEstimatedCostUsd bool
	DeniedScopeKey      string
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
	// Redis 非 nil 时把在途预留放到 Redis（多副本共享）；nil 保持进程内，
	// 供测试与 chain-disabled 组合使用。
	Redis          redis.UniversalClient
	RedisNamespace string
}

// InflightQuotaService ports api-key-inflight-quota.service.ts: per api-key
// reserved cost accumulation with leak-guarded reservations. Production uses
// a Redis-backed store so reservations from all gateway replicas are visible;
// tests and chain-disabled compositions keep the process-local map.
type InflightQuotaService struct {
	apiKeys   *APIKeyQuotaService
	dbService DBServiceClient
	estimator CostEstimator
	log       LogHook
	timer     TimerScheduler
	remote    *redisInflightQuota

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
	var remote *redisInflightQuota
	if cfg.Redis != nil {
		var err error
		remote, err = newRedisInflightQuota(cfg.Redis, cfg.RedisNamespace)
		if err != nil {
			return nil, err
		}
	}
	return &InflightQuotaService{
		apiKeys:   cfg.APIKeys,
		dbService: cfg.DBService,
		estimator: cfg.Estimator,
		log:       log,
		timer:     timer,
		remote:    remote,
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
//
// ScopeKey 是 T93-4（C3-H8）扩展的 scope 维度：空值保持 API Key 口径（用
// APIKeyID 作进程内状态键），非空时按给定稳定身份累加（授权/团队 scope 用
// authorizationScopeStateKey 构造）。同一进程内状态表同时承载两类 scope，
// 释放生命周期（30 分钟泄漏兜底 + Complete 后延迟释放）完全一致。
type ReserveInput struct {
	APIKeyID         string
	ScopeKey         string
	Limits           RequestQuotaLimits
	CurrentCosts     RequestQuotaCosts
	EstimatedCostUsd float64
	// ReleaseDelayMs overrides DefaultReleaseDelayMs when non-nil.
	ReleaseDelayMs *int
}

func reserveStateKey(input ReserveInput) string {
	if input.ScopeKey != "" {
		return input.ScopeKey
	}
	return input.APIKeyID
}

// Reserve keeps the historical synchronous contract for memory-backed tests;
// production callers that can surface infrastructure failures use
// ReserveContext.
func (s *InflightQuotaService) Reserve(input ReserveInput) InflightDecision {
	decision, _ := s.ReserveContext(context.Background(), input)
	return decision
}

// ReserveContext reserves one API-key/scope state. Redis failures are returned
// to the caller (fail-closed); the memory driver never errors.
func (s *InflightQuotaService) ReserveContext(ctx context.Context, input ReserveInput) (InflightDecision, error) {
	if s.remote != nil {
		decision, releases, err := s.remote.reserve(ctx, input)
		if err != nil {
			return decision, err
		}
		if decision.Allowed && len(releases) > 0 {
			delay := DefaultReleaseDelayMs
			if input.ReleaseDelayMs != nil {
				delay = *input.ReleaseDelayMs
			}
			decision.Reservation = s.createReservation(releases, delay)
		}
		return decision, nil
	}
	return s.reserveMemory(input), nil
}

// reserveMemory mirrors reserveApiKeyInflightCost: project current + reserved +
// estimate against every enabled window, reserve on pass. The check and the
// reservation share one critical section so concurrent reservations observe
// each other (Node is single-threaded, where this ordering is implicit).
func (s *InflightQuotaService) reserveMemory(input ReserveInput) InflightDecision {
	estimatedCostUsd := normalizedCost(input.EstimatedCostUsd)
	if estimatedCostUsd <= 0 {
		return InflightDecision{Allowed: true}
	}
	stateKey := reserveStateKey(input)
	s.mu.Lock()
	state, ok := s.states[stateKey]
	if !ok {
		state = &inflightState{reservedCostUsd: 0}
	}
	projected := addCostToAllWindows(input.CurrentCosts, state.reservedCostUsd+estimatedCostUsd)
	if isProjectedRequestQuotaExceeded(input.Limits, projected) {
		s.mu.Unlock()
		return InflightDecision{Allowed: false, EstimatedCostUsd: estimatedCostUsd, HasEstimatedCostUsd: true, DeniedScopeKey: stateKey}
	}
	state.reservedCostUsd = normalizedCost(state.reservedCostUsd + estimatedCostUsd)
	s.states[stateKey] = state
	s.mu.Unlock()
	releaseDelay := DefaultReleaseDelayMs
	if input.ReleaseDelayMs != nil {
		releaseDelay = *input.ReleaseDelayMs
	}
	return InflightDecision{
		Allowed:             true,
		EstimatedCostUsd:    estimatedCostUsd,
		HasEstimatedCostUsd: true,
		Reservation:         s.createReservation([]inflightRelease{{stateKey: stateKey, costUsd: estimatedCostUsd}}, releaseDelay),
	}
}

// InflightScope 是一条额度 scope 的在途预留描述：稳定身份（进程内状态键）、
// 该 scope 的启用量额窗口与已结算成本快照。
type InflightScope struct {
	ScopeKey     string
	Limits       RequestQuotaLimits
	CurrentCosts RequestQuotaCosts
}

// ScopeReserveInput 是一组 scope 的原子在途预留输入。
type ScopeReserveInput struct {
	Scopes           []InflightScope
	EstimatedCostUsd float64
	// ReleaseDelayMs overrides DefaultReleaseDelayMs when non-nil.
	ReleaseDelayMs *int
}

// ReserveScopes keeps the historical synchronous contract for memory-backed
// tests; production callers use ReserveScopesContext.
func (s *InflightQuotaService) ReserveScopes(input ScopeReserveInput) InflightDecision {
	decision, _ := s.ReserveScopesContext(context.Background(), input)
	return decision
}

// ReserveScopesContext 对一组 scope 做原子在途预留（T93-4 / C3-H8）：先统一
// 校验所有 scope 的投影（已结算 + 已在途 + 本次估算）是否超限，任一超限即
// 整体拒绝且不写入任何状态；全部通过后统一累加并返回一次性释放全部 scope
// 的预留句柄。Redis 实现用单 Lua 脚本保证跨实例原子性。
func (s *InflightQuotaService) ReserveScopesContext(ctx context.Context, input ScopeReserveInput) (InflightDecision, error) {
	if s.remote != nil {
		decision, releases, err := s.remote.reserveScopes(ctx, input)
		if err != nil {
			return decision, err
		}
		if decision.Allowed && len(releases) > 0 {
			delay := DefaultReleaseDelayMs
			if input.ReleaseDelayMs != nil {
				delay = *input.ReleaseDelayMs
			}
			decision.Reservation = s.createReservation(releases, delay)
		}
		return decision, nil
	}
	return s.reserveScopesMemory(input), nil
}

func (s *InflightQuotaService) reserveScopesMemory(input ScopeReserveInput) InflightDecision {
	estimatedCostUsd := normalizedCost(input.EstimatedCostUsd)
	scopes := make([]InflightScope, 0, len(input.Scopes))
	for _, scope := range input.Scopes {
		if scope.ScopeKey == "" {
			continue
		}
		scopes = append(scopes, scope)
	}
	if estimatedCostUsd <= 0 || len(scopes) == 0 {
		return InflightDecision{Allowed: true}
	}
	s.mu.Lock()
	for _, scope := range scopes {
		reserved := 0.0
		if state, ok := s.states[scope.ScopeKey]; ok {
			reserved = state.reservedCostUsd
		}
		projected := addCostToAllWindows(scope.CurrentCosts, reserved+estimatedCostUsd)
		if isProjectedRequestQuotaExceeded(scope.Limits, projected) {
			s.mu.Unlock()
			return InflightDecision{
				Allowed:             false,
				EstimatedCostUsd:    estimatedCostUsd,
				HasEstimatedCostUsd: true,
				DeniedScopeKey:      scope.ScopeKey,
			}
		}
	}
	releases := make([]inflightRelease, 0, len(scopes))
	for _, scope := range scopes {
		state, ok := s.states[scope.ScopeKey]
		if !ok {
			state = &inflightState{reservedCostUsd: 0}
		}
		state.reservedCostUsd = normalizedCost(state.reservedCostUsd + estimatedCostUsd)
		s.states[scope.ScopeKey] = state
		releases = append(releases, inflightRelease{stateKey: scope.ScopeKey, costUsd: estimatedCostUsd})
	}
	s.mu.Unlock()
	releaseDelay := DefaultReleaseDelayMs
	if input.ReleaseDelayMs != nil {
		releaseDelay = *input.ReleaseDelayMs
	}
	return InflightDecision{
		Allowed:             true,
		EstimatedCostUsd:    estimatedCostUsd,
		HasEstimatedCostUsd: true,
		Reservation:         s.createReservation(releases, releaseDelay),
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
	return s.ReserveContext(ctx, ReserveInput{
		APIKeyID:         input.APIKey.ID,
		Limits:           limits,
		CurrentCosts:     currentCosts,
		EstimatedCostUsd: estimatedCostUsd,
	})
}

// inflightRelease 是一条待释放在途预留（状态键 + 本次预留成本）。
type inflightRelease struct {
	stateKey string
	costUsd  float64
}

// createReservation mirrors createReservation: immediate release via the
// 30-minute leak timer, rescheduled by Complete() to the release delay. T93-4
// 起一次预留可覆盖多条 scope（API Key + 授权/团队），泄漏兜底与延迟释放
// 语义对每条 scope 一致，不新造第二套生命周期。
func (s *InflightQuotaService) createReservation(releases []inflightRelease, releaseDelayMs int) *InflightReservation {
	var releaseOnce sync.Once
	var completeOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			s.release(releases)
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

func (s *InflightQuotaService) release(releases []inflightRelease) {
	if s.remote != nil {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.remote.release(releaseCtx, releases); err != nil {
			s.log("gateway_api_key_inflight_quota_release_failed", map[string]any{
				"error": err.Error(),
			}, "API Key 在途额度 Redis 释放失败，等待 TTL 兜底")
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range releases {
		state, ok := s.states[entry.stateKey]
		if !ok {
			continue
		}
		state.reservedCostUsd = normalizedCost(math.Max(0, state.reservedCostUsd-entry.costUsd))
		if state.reservedCostUsd == 0 {
			delete(s.states, entry.stateKey)
		}
	}
}

// APIKeyInflightQuotaState mirrors apiKeyInflightQuotaSnapshot's element.
type APIKeyInflightQuotaState struct {
	APIKeyID        string
	ReservedCostUsd float64
}

// Snapshot mirrors apiKeyInflightQuotaSnapshot: 只返回 API Key scope 的在途
// 状态（授权/团队 scope 走 ScopeSnapshot，保持既有快照契约不变）。
func (s *InflightQuotaService) Snapshot() []APIKeyInflightQuotaState {
	if s.remote != nil {
		states, err := s.remote.snapshot(context.Background())
		if err != nil {
			s.log("gateway_api_key_inflight_quota_snapshot_failed", map[string]any{"error": err.Error()}, "API Key 在途额度快照读取失败")
			return nil
		}
		out := make([]APIKeyInflightQuotaState, 0, len(states))
		for _, state := range states {
			if !isAuthorizationInflightScopeKey(state.ScopeKey) {
				out = append(out, APIKeyInflightQuotaState{APIKeyID: state.ScopeKey, ReservedCostUsd: state.ReservedCostUsd})
			}
		}
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]APIKeyInflightQuotaState, 0, len(s.states))
	for stateKey, state := range s.states {
		if isAuthorizationInflightScopeKey(stateKey) {
			continue
		}
		out = append(out, APIKeyInflightQuotaState{APIKeyID: stateKey, ReservedCostUsd: state.reservedCostUsd})
	}
	return out
}

// ScopeSnapshot 返回授权/团队 scope 的在途状态（T93-4）。
func (s *InflightQuotaService) ScopeSnapshot() []InflightScopeState {
	if s.remote != nil {
		states, err := s.remote.snapshot(context.Background())
		if err != nil {
			s.log("gateway_api_key_inflight_quota_snapshot_failed", map[string]any{"error": err.Error()}, "授权 scope 在途额度快照读取失败")
			return nil
		}
		out := make([]InflightScopeState, 0, len(states))
		for _, state := range states {
			if isAuthorizationInflightScopeKey(state.ScopeKey) {
				out = append(out, InflightScopeState{ScopeKey: state.ScopeKey, ReservedCostUsd: state.ReservedCostUsd})
			}
		}
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InflightScopeState, 0, len(s.states))
	for stateKey, state := range s.states {
		if !isAuthorizationInflightScopeKey(stateKey) {
			continue
		}
		out = append(out, InflightScopeState{ScopeKey: stateKey, ReservedCostUsd: state.reservedCostUsd})
	}
	return out
}

// InflightScopeState 是一条授权/团队 scope 的在途状态。
type InflightScopeState struct {
	ScopeKey        string
	ReservedCostUsd float64
}

// ClearForTest mirrors clearApiKeyInflightQuotaReservationsForTest.
func (s *InflightQuotaService) ClearForTest() {
	if s.remote != nil {
		if err := s.remote.clear(context.Background()); err != nil {
			s.log("gateway_api_key_inflight_quota_clear_failed", map[string]any{"error": err.Error()}, "在途额度 Redis 清理失败")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = map[string]*inflightState{}
}

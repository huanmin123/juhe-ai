package gatewayhotquality

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
)

// Gateway hot quality runtime mirroring
// backend/src/modules/gateway/runtime/hot-quality-runtime.service.ts.
// Node module singletons (runtimeConfig, observeGatewayRouting, randomUUID)
// become an injected driver config, an observer port and crypto/rand UUID v4.

const (
	gatewayHotQualityCapacity        = 10_000
	explorationReservationLeaseMs    = int64(15_000)
	knownSampleStaleAfterMs          = int64(10 * 60_000)
	hotQualityModelFamilyBucketCount = 256
)

// RuntimeDriverConfig carries the runtimeConfig inputs the Node singleton
// reads: runtimeMode, runtimeStateDriver, redis.stateUrl and the Redis
// namespace for key layout.
type RuntimeDriverConfig struct {
	RuntimeMode        string // 'standalone' | 'performance'
	RuntimeStateDriver string // 'memory' | 'redis'
	RedisStateURL      string
	RedisNamespace     string
}

// RoutingObservation mirrors one observeGatewayRouting event payload.
type RoutingObservation struct {
	Kind      string // 'attempt' | 'hot_quality_mutation' | 'exploration'
	Outcome   string
	Operation string // hot_quality_mutation only: 'attempt' | 'terminal'
	Status    string // hot_quality_mutation only
}

// RoutingObserver mirrors observeGatewayRouting.
type RoutingObserver interface {
	ObserveGatewayRouting(observation RoutingObservation)
}

// RoutingObserverFunc adapts a function to RoutingObserver.
type RoutingObserverFunc func(observation RoutingObservation)

// ObserveGatewayRouting implements RoutingObserver.
func (fn RoutingObserverFunc) ObserveGatewayRouting(observation RoutingObservation) {
	fn(observation)
}

// GatewayHotQualityLogger mirrors getRequestLogger().warn usage in the
// attempt lifecycle (only warn fields are consumed).
type GatewayHotQualityLogger interface {
	Warn(fields map[string]interface{}, msg string)
}

// GatewayHotQualityRuntime mirrors GatewayHotQualityRuntime. The observer and
// logger ports replace the Node module-global observability/logger singletons;
// nil implementations are no-ops.
type GatewayHotQualityRuntime struct {
	HotQualityStore  HotQualityStore
	ExplorationStore SameTierExplorationStore
	Observer         RoutingObserver
	Logger           GatewayHotQualityLogger
}

var hotQualityRuntime = struct {
	sync.Mutex
	hotQualityStore  HotQualityStore
	explorationStore SameTierExplorationStore
	identity         string
	config           *RuntimeDriverConfig
	runtime          *GatewayHotQualityRuntime
}{}

// GetGatewayHotQualityRuntime mirrors getGatewayHotQualityRuntime: one store
// pair per runtime identity ('standalone:memory' or
// 'performance:redis:<sha256(redisUrl)>').
func GetGatewayHotQualityRuntime(ctx context.Context, config RuntimeDriverConfig) (*GatewayHotQualityRuntime, error) {
	identity, err := gatewayHotQualityRuntimeIdentity(config)
	if err != nil {
		return nil, err
	}
	hotQualityRuntime.Lock()
	defer hotQualityRuntime.Unlock()
	if hotQualityRuntime.identity != identity || hotQualityRuntime.hotQualityStore == nil || hotQualityRuntime.explorationStore == nil {
		var runtime GatewayHotQualityRuntime
		if config.RuntimeMode == "standalone" {
			if config.RuntimeStateDriver != "memory" {
				return nil, errors.New("standalone 热质量要求 memory runtime state driver")
			}
			hotQualityStore, err := NewMemoryHotQualityStore(MemoryHotQualityStoreOptions{KeyCapacity: intPtr(gatewayHotQualityCapacity)})
			if err != nil {
				return nil, err
			}
			explorationStore, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{PoolCapacity: intPtr(gatewayHotQualityCapacity)})
			if err != nil {
				return nil, err
			}
			runtime.HotQualityStore = hotQualityStore
			runtime.ExplorationStore = explorationStore
		} else {
			if config.RuntimeStateDriver != "redis" {
				return nil, errors.New("performance 热质量要求 redis runtime state driver")
			}
			if strings.TrimSpace(config.RedisStateURL) == "" {
				return nil, errors.New("performance 热质量缺少 JUHE_AI_REDIS_STATE_URL")
			}
			client, err := GetRedisClient(ctx, config.RedisStateURL)
			if err != nil {
				return nil, err
			}
			runner := NewRedisScriptRunner(client)
			hotQualityStore, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{
				Namespace:   config.RedisNamespace,
				KeyCapacity: intPtr(gatewayHotQualityCapacity),
			})
			if err != nil {
				return nil, err
			}
			explorationStore, err := NewRedisSameTierExplorationStore(runner, RedisSameTierExplorationStoreOptions{
				Namespace:    config.RedisNamespace,
				PoolCapacity: intPtr(gatewayHotQualityCapacity),
			})
			if err != nil {
				return nil, err
			}
			runtime.HotQualityStore = hotQualityStore
			runtime.ExplorationStore = explorationStore
		}
		hotQualityRuntime.hotQualityStore = runtime.HotQualityStore
		hotQualityRuntime.explorationStore = runtime.ExplorationStore
		hotQualityRuntime.identity = identity
		hotQualityRuntime.config = &config
		hotQualityRuntime.runtime = &runtime
	}
	return hotQualityRuntime.runtime, nil
}

// ResetGatewayHotQualityRuntimeForTest mirrors resetGatewayHotQualityRuntimeForTest.
func ResetGatewayHotQualityRuntimeForTest() {
	hotQualityRuntime.Lock()
	defer hotQualityRuntime.Unlock()
	hotQualityRuntime.hotQualityStore = nil
	hotQualityRuntime.explorationStore = nil
	hotQualityRuntime.identity = ""
	hotQualityRuntime.config = nil
	hotQualityRuntime.runtime = nil
}

func gatewayHotQualityRuntimeIdentity(config RuntimeDriverConfig) (string, error) {
	if config.RuntimeMode == "standalone" {
		if config.RuntimeStateDriver != "memory" {
			return "", errors.New("standalone 热质量要求 memory runtime state driver")
		}
		return "standalone:memory", nil
	}
	if config.RuntimeStateDriver != "redis" {
		return "", errors.New("performance 热质量要求 redis runtime state driver")
	}
	redisURL := strings.TrimSpace(config.RedisStateURL)
	if redisURL == "" {
		return "", errors.New("performance 热质量缺少 JUHE_AI_REDIS_STATE_URL")
	}
	digest := sha256.Sum256([]byte(redisURL))
	return "performance:redis:" + hex.EncodeToString(digest[:]), nil
}

// OnceGatewayHotQualityExplorationSettlement mirrors
// onceGatewayHotQualityExplorationSettlement: the first call wins, later
// calls observe the memoized result (Node memoizes the promise; Go memoizes
// the error of the first invocation, which runs with the first caller's ctx).
func OnceGatewayHotQualityExplorationSettlement(
	callback func(ctx context.Context, outcome string) error,
) func(ctx context.Context, outcome string) error {
	var once sync.Once
	var err error
	return func(ctx context.Context, outcome string) error {
		once.Do(func() {
			err = callback(ctx, outcome)
		})
		return err
	}
}

// GatewayHotQualityAccountView mirrors the UpstreamAccount fields the
// ordering pipeline reads (Node projects the full account object).
type GatewayHotQualityAccountView struct {
	ID                        string
	AccessType                string // 'owner' | 'authorized' (optional)
	AccountAccessType         string // 'owner' | 'account_authorized' | 'group_authorized'
	BindingSystemAccountID    string
	BoundGroupID              string
	AccountAuthorizationID    string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	FallbackEnabled           bool
	SuperPriorityEnabled      bool
	Priority                  int
}

// GatewayAccountRuntimeKey mirrors gatewayAccountRuntimeKey
// (account-runtime-keys.ts) for the account view above.
func GatewayAccountRuntimeKey(account GatewayHotQualityAccountView) (string, error) {
	if account.AccountAccessType == "account_authorized" || account.AccessType == "authorized" {
		systemAccountID := account.BindingSystemAccountID
		groupID := account.BoundGroupID
		authorizationID := account.AccountAuthorizationID
		if systemAccountID != "" && groupID != "" && authorizationID != "" {
			return fmt.Sprintf("%s:authorized:%s:%s:%s", account.ID, systemAccountID, groupID, authorizationID), nil
		}
		return "", errors.New("授权账户运行态键缺少绑定上下文")
	}
	return account.ID, nil
}

// GatewayHotQualityCandidateOrderInput mirrors
// GatewayHotQualityCandidateOrderInput; the generic payload + Base projection
// mirrors the Node account objects.
type GatewayHotQualityCandidateOrderInput[T any] struct {
	Accounts                       []T
	Base                           func(T) GatewayHotQualityAccountView
	ModelPriorityRankByAccountID   map[string]int
	Mode                           gatewayhybrid.HotQualityRoutingMode
	SystemAccountID                string
	RouteStrategyID                string
	GroupID                        string
	RequestLane                    string
	Model                          *string
	RequestID                      string
	LatencyDegradedAccountIDs      map[string]bool
	StableBindingOrderByRuntimeKey map[string]int
	EligibleFirstPrimaryDispatch   bool
	NowMs                          *int64
}

// GatewayHotQualityExplorationReservation extends
// SameTierExplorationReservation with the pool key.
type GatewayHotQualityExplorationReservation struct {
	SameTierExplorationReservation
	PoolKey string
}

// GatewayHotQualityCandidateOrderResult mirrors
// GatewayHotQualityCandidateOrderResult. Node's `selectedAccountId?: string`
// and `explorationReservation?` become ” / nil.
type GatewayHotQualityCandidateOrderResult[T any] struct {
	Accounts                       []T
	QualityReorderedTierKeys       []string
	LatencyDegradedOverrideApplied bool
	SelectedAccountID              string
	DispatchIntent                 string
	ExplorationStatus              string
	ExplorationReservation         *GatewayHotQualityExplorationReservation
	SettleExplorationAfterDispatch func(ctx context.Context, outcome string) error
}

type orderedCandidatePayload[T any] struct {
	base    gatewayhybrid.HotQualityCandidate
	account T
}

// OrderGatewayAccountsByHotQuality mirrors
// orderGatewayAccountsByHotQualityAsync.
func OrderGatewayAccountsByHotQuality[T any](
	ctx context.Context,
	runtime *GatewayHotQualityRuntime,
	input GatewayHotQualityCandidateOrderInput[T],
) (*GatewayHotQualityCandidateOrderResult[T], error) {
	nowMs, err := runtimeNormalizedNow(derefOrDefault(input.NowMs, defaultUnixMilli))
	if err != nil {
		return nil, err
	}
	if len(input.Accounts) == 0 {
		return &GatewayHotQualityCandidateOrderResult[T]{
			Accounts:                 []T{},
			QualityReorderedTierKeys: []string{},
			DispatchIntent:           "primary_service",
			ExplorationStatus:        "no_candidate",
		}, nil
	}
	stableOrders, err := stableBindingOrders(input.Accounts, input.Base, input.StableBindingOrderByRuntimeKey)
	if err != nil {
		return nil, err
	}
	protocolGroups, err := groupByProtocolProfile(input.Accounts, input.Base)
	if err != nil {
		return nil, err
	}
	var firstResult *gatewayHotQualityOrderResultFields
	accountsPayloads := make([]orderedCandidatePayload[T], 0, len(input.Accounts))

	for _, group := range protocolGroups {
		routeScopeKey, err := GatewayHotQualityRouteScopeKey(gatewayHotQualityRouteScopeKeyInput{
			SystemAccountID: input.SystemAccountID,
			RouteStrategyID: input.RouteStrategyID,
			GroupID:         input.GroupID,
			ProtocolProfile: group.protocolProfile,
			RequestLane:     input.RequestLane,
		})
		if err != nil {
			return nil, err
		}
		candidateScopes := make([]HotQualityScope, 0, len(group.accounts))
		payloads := make([]orderedCandidatePayload[T], 0, len(group.accounts))
		for _, account := range group.accounts {
			view := input.Base(account)
			scope, err := hotQualityScopeForAccount(view, input.RequestLane, input.Model)
			if err != nil {
				return nil, err
			}
			candidateScopes = append(candidateScopes, scope)
			payloads = append(payloads, orderedCandidatePayload[T]{account: account})
		}
		snapshots := make([]*HotQualitySnapshot, len(candidateScopes))
		for index, scope := range candidateScopes {
			snapshot, err := runtime.HotQualityStore.Get(ctx, scope, &nowMs)
			if err != nil {
				return nil, err
			}
			snapshots[index] = snapshot
		}
		candidates := make([]gatewayhybrid.HotQualityCandidate, len(group.accounts))
		for index, account := range group.accounts {
			view := input.Base(account)
			runtimeKey, err := GatewayAccountRuntimeKey(view)
			if err != nil {
				return nil, err
			}
			candidates[index] = gatewayhybrid.HotQualityCandidate{
				AccountID:         view.ID,
				AccountRuntimeKey: runtimeKey,
				RouteScopeKey:     routeScopeKey,
				ConfigurationTier: gatewayhybrid.GatewayAccountConfigurationTier{
					ModelMatchRank:       modelRank(view, input.ModelPriorityRankByAccountID),
					FallbackEnabled:      view.FallbackEnabled,
					SuperPriorityEnabled: view.SuperPriorityEnabled,
					Priority:             view.Priority,
				},
				StableBindingOrder: stableOrders[runtimeKey],
				HotQuality:         selectionViewOrNil(snapshots[index]),
				LatencyDegraded:    input.LatencyDegradedAccountIDs[view.ID],
			}
			payloads[index].base = candidates[index]
		}

		isFirstProtocolGroup := firstResult == nil
		var selection *gatewayHotQualityGroupSelection[T]
		if isFirstProtocolGroup {
			selection, err = selectFirstProtocolGroup(ctx, runtime, input, payloads, candidates, routeScopeKey, group.protocolProfile, nowMs)
			if err != nil {
				return nil, err
			}
		} else {
			decision, err := gatewayhybrid.DecideHotQualityCandidate[gatewayhybrid.HotQualityCandidate](gatewayhybrid.DecideHotQualityCandidateInput[gatewayhybrid.HotQualityCandidate]{
				Mode:          input.Mode,
				RouteScopeKey: routeScopeKey,
				Candidates:    candidates,
				Base:          func(candidate gatewayhybrid.HotQualityCandidate) gatewayhybrid.HotQualityCandidate { return candidate },
			})
			if err != nil {
				return nil, err
			}
			orderedPayloads, err := orderedPayloadsFromDecision(payloads, candidates, decision.OrderedCandidates)
			if err != nil {
				return nil, err
			}
			selectedAccountID := ""
			if decision.SelectedCandidate != nil {
				selectedAccountID = decision.SelectedCandidate.AccountID
			}
			selection = &gatewayHotQualityGroupSelection[T]{
				accounts: orderedPayloads,
				result: gatewayHotQualityOrderResultFields{
					QualityReorderedTierKeys:       decision.Explanation.QualityReorderedTierKeys,
					LatencyDegradedOverrideApplied: decision.Explanation.LatencyDegradedOverrideApplied,
					SelectedAccountID:              selectedAccountID,
					DispatchIntent:                 decision.DispatchIntent,
					ExplorationStatus:              decision.Explanation.Exploration.Status,
				},
			}
		}
		accountsPayloads = append(accountsPayloads, selection.accounts...)
		if isFirstProtocolGroup {
			firstResult = &selection.result
		}
	}

	first := gatewayHotQualityOrderResultFields{
		QualityReorderedTierKeys:       []string{},
		LatencyDegradedOverrideApplied: false,
		DispatchIntent:                 "primary_service",
		ExplorationStatus:              "no_candidate",
	}
	if firstResult != nil {
		first = *firstResult
	}
	accounts := make([]T, 0, len(accountsPayloads))
	for _, payload := range accountsPayloads {
		accounts = append(accounts, payload.account)
	}
	return &GatewayHotQualityCandidateOrderResult[T]{
		Accounts:                       accounts,
		QualityReorderedTierKeys:       first.QualityReorderedTierKeys,
		LatencyDegradedOverrideApplied: first.LatencyDegradedOverrideApplied,
		SelectedAccountID:              first.SelectedAccountID,
		DispatchIntent:                 first.DispatchIntent,
		ExplorationStatus:              first.ExplorationStatus,
		ExplorationReservation:         first.ExplorationReservation,
		SettleExplorationAfterDispatch: first.SettleExplorationAfterDispatch,
	}, nil
}

type gatewayHotQualityOrderResultFields struct {
	QualityReorderedTierKeys       []string
	LatencyDegradedOverrideApplied bool
	SelectedAccountID              string
	DispatchIntent                 string
	ExplorationStatus              string
	ExplorationReservation         *GatewayHotQualityExplorationReservation
	SettleExplorationAfterDispatch func(ctx context.Context, outcome string) error
}

type gatewayHotQualityGroupSelection[T any] struct {
	accounts []orderedCandidatePayload[T]
	result   gatewayHotQualityOrderResultFields
}

type protocolAccountGroup[T any] struct {
	protocolProfile string
	accounts        []T
}

func selectFirstProtocolGroup[T any](
	ctx context.Context,
	runtime *GatewayHotQualityRuntime,
	input GatewayHotQualityCandidateOrderInput[T],
	payloads []orderedCandidatePayload[T],
	candidates []gatewayhybrid.HotQualityCandidate,
	routeScopeKey string,
	protocolProfile string,
	nowMs int64,
) (*gatewayHotQualityGroupSelection[T], error) {
	if len(candidates) == 0 {
		return nil, errors.New("热质量首协议候选为空")
	}
	topCandidate := candidates[0]
	tierKey, err := gatewayhybrid.GatewayAccountConfigurationTierKey(topCandidate.ConfigurationTier)
	if err != nil {
		return nil, err
	}
	poolKey, err := SameTierExplorationPoolKey(routeScopeKey, tierKey)
	if err != nil {
		return nil, err
	}
	requestID, err := runtimeRequiredKey(input.RequestID, "requestId")
	if err != nil {
		return nil, err
	}
	accrualToken := requestID + ":" + protocolProfile
	sharedState, err := runtime.ExplorationStore.Accrue(ctx, SameTierExplorationAccrueInput{
		PoolKey:      poolKey,
		AccrualToken: accrualToken,
		Eligible:     input.EligibleFirstPrimaryDispatch,
		NowMs:        &nowMs,
	})
	if err != nil {
		return nil, err
	}
	decisionState, err := sameTierExplorationDecisionState(sharedState, nowMs, input.EligibleFirstPrimaryDispatch)
	if err != nil {
		return nil, err
	}
	decision, err := gatewayhybrid.DecideHotQualityCandidate(orderedDecisionInput(payloads, candidates, input.Mode, routeScopeKey, decisionState))
	if err != nil {
		return nil, err
	}
	selectedAccountID := ""
	if decision.SelectedCandidate != nil {
		selectedAccountID = decision.SelectedCandidate.AccountID
	}
	if decision.DispatchIntent != gatewayhybrid.DispatchIntentSameTierExploration || decision.SelectedCandidate == nil {
		orderedPayloads, err := orderedPayloadsFromDecision(payloads, candidates, decision.OrderedCandidates)
		if err != nil {
			return nil, err
		}
		return &gatewayHotQualityGroupSelection[T]{
			accounts: orderedPayloads,
			result: gatewayHotQualityOrderResultFields{
				QualityReorderedTierKeys:       decision.Explanation.QualityReorderedTierKeys,
				LatencyDegradedOverrideApplied: decision.Explanation.LatencyDegradedOverrideApplied,
				SelectedAccountID:              selectedAccountID,
				DispatchIntent:                 decision.DispatchIntent,
				ExplorationStatus:              decision.Explanation.Exploration.Status,
			},
		}, nil
	}

	reservationId, err := newUUIDv4()
	if err != nil {
		return nil, err
	}
	leaseUntilMs := nowMs + explorationReservationLeaseMs
	reservation, err := runtime.ExplorationStore.Reserve(ctx, SameTierExplorationReserveInput{
		PoolKey:           poolKey,
		ReservationID:     reservationId,
		AccountRuntimeKey: decision.SelectedCandidate.AccountRuntimeKey,
		LeaseUntilMs:      leaseUntilMs,
		NowMs:             &nowMs,
	})
	if err != nil {
		return nil, err
	}
	if reservation.Status != ExplorationReservationReserved || reservation.Reservation == nil {
		observeRouting(runtime, RoutingObservation{Kind: "exploration", Outcome: "contended"})
		qualityOrderedPayloads, err := orderedPayloadsFromDecision(payloads, candidates, decision.QualityOrderedCandidates)
		if err != nil {
			return nil, err
		}
		return &gatewayHotQualityGroupSelection[T]{
			accounts: qualityOrderedPayloads,
			result: gatewayHotQualityOrderResultFields{
				QualityReorderedTierKeys:       decision.Explanation.QualityReorderedTierKeys,
				LatencyDegradedOverrideApplied: decision.Explanation.LatencyDegradedOverrideApplied,
				SelectedAccountID:              firstAccountID(decision.QualityOrderedCandidates),
				DispatchIntent:                 "primary_service",
				ExplorationStatus:              "reservation_" + reservation.Status,
			},
		}, nil
	}
	observeRouting(runtime, RoutingObservation{Kind: "exploration", Outcome: "reserved"})
	selectedReservation := &GatewayHotQualityExplorationReservation{
		SameTierExplorationReservation: *reservation.Reservation,
		PoolKey:                        poolKey,
	}
	orderedPayloads, err := orderedPayloadsFromDecision(payloads, candidates, decision.OrderedCandidates)
	if err != nil {
		return nil, err
	}
	settleExplorationAfterDispatch := func(ctx context.Context, outcome string) error {
		if _, err := runtime.ExplorationStore.Settle(ctx, SameTierExplorationSettleInput{
			PoolKey:           poolKey,
			ReservationID:     selectedReservation.ReservationID,
			AccountRuntimeKey: selectedReservation.AccountRuntimeKey,
			Outcome:           outcome,
		}); err != nil {
			return err
		}
		settlementOutcome := "restored"
		if outcome == "dispatched" {
			settlementOutcome = "dispatched"
		}
		observeRouting(runtime, RoutingObservation{Kind: "exploration", Outcome: settlementOutcome})
		return nil
	}
	return &gatewayHotQualityGroupSelection[T]{
		accounts: orderedPayloads,
		result: gatewayHotQualityOrderResultFields{
			QualityReorderedTierKeys:       decision.Explanation.QualityReorderedTierKeys,
			LatencyDegradedOverrideApplied: decision.Explanation.LatencyDegradedOverrideApplied,
			SelectedAccountID:              decision.SelectedCandidate.AccountID,
			DispatchIntent:                 decision.DispatchIntent,
			ExplorationStatus:              "reserved",
			ExplorationReservation:         selectedReservation,
			SettleExplorationAfterDispatch: settleExplorationAfterDispatch,
		},
	}, nil
}

// orderedDecisionInput pairs the decision candidates with payload ordering.
func orderedDecisionInput[T any](
	payloads []orderedCandidatePayload[T],
	candidates []gatewayhybrid.HotQualityCandidate,
	mode gatewayhybrid.HotQualityRoutingMode,
	routeScopeKey string,
	exploration *gatewayhybrid.SameTierExplorationState,
) gatewayhybrid.DecideHotQualityCandidateInput[gatewayhybrid.HotQualityCandidate] {
	return gatewayhybrid.DecideHotQualityCandidateInput[gatewayhybrid.HotQualityCandidate]{
		Mode:          mode,
		RouteScopeKey: routeScopeKey,
		Candidates:    candidates,
		Base:          func(candidate gatewayhybrid.HotQualityCandidate) gatewayhybrid.HotQualityCandidate { return candidate },
		Exploration:   exploration,
	}
}

func orderedPayloadsFromDecision[T any](
	payloads []orderedCandidatePayload[T],
	candidates []gatewayhybrid.HotQualityCandidate,
	ordered []gatewayhybrid.HotQualityCandidate,
) ([]orderedCandidatePayload[T], error) {
	result := make([]orderedCandidatePayload[T], 0, len(ordered))
	for _, orderedCandidate := range ordered {
		matchIndex := -1
		for index, candidate := range candidates {
			if candidate.AccountRuntimeKey == orderedCandidate.AccountRuntimeKey {
				matchIndex = index
				break
			}
		}
		if matchIndex < 0 {
			return nil, fmt.Errorf("热质量候选缺少账号 %s", orderedCandidate.AccountRuntimeKey)
		}
		result = append(result, payloads[matchIndex])
	}
	return result, nil
}

func firstAccountID(candidates []gatewayhybrid.HotQualityCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].AccountID
}

func observeRouting(runtime *GatewayHotQualityRuntime, observation RoutingObservation) {
	if runtime != nil && runtime.Observer != nil {
		runtime.Observer.ObserveGatewayRouting(observation)
	}
}

func sameTierExplorationDecisionState(
	state *SameTierExplorationState,
	nowMs int64,
	eligibleFirstPrimaryDispatch bool,
) (*gatewayhybrid.SameTierExplorationState, error) {
	if state == nil {
		return nil, errors.New("同层探索状态缺失")
	}
	inFlight := make([]string, 0, len(state.Reservations))
	for _, reservation := range state.Reservations {
		inFlight = append(inFlight, reservation.AccountRuntimeKey)
	}
	cooldown := make(map[string]int64, len(state.CooldownUntilMsByRuntimeKey))
	for key, value := range state.CooldownUntilMsByRuntimeKey {
		cooldown[key] = value
	}
	return &gatewayhybrid.SameTierExplorationState{
		Enabled:                           true,
		EligibleFirstPrimaryDispatch:      eligibleFirstPrimaryDispatch,
		CreditAccrualAlreadyApplied:       true,
		RequestAlreadyExplored:            false,
		HasLeftHighestNormalTier:          false,
		Credit:                            state.Credit,
		Cursor:                            state.Cursor,
		NowMs:                             nowMs,
		KnownSampleStaleAfterMs:           knownSampleStaleAfterMs,
		TargetInFlightRuntimeKeys:         inFlight,
		TargetCooldownUntilMsByRuntimeKey: cooldown,
	}, nil
}

func stableBindingOrders[T any](
	accounts []T,
	base func(T) GatewayHotQualityAccountView,
	provided map[string]int,
) (map[string]int, error) {
	result := make(map[string]int, len(accounts))
	for index, account := range accounts {
		runtimeKey, err := GatewayAccountRuntimeKey(base(account))
		if err != nil {
			return nil, err
		}
		order := index
		if providedOrder, ok := provided[runtimeKey]; ok && providedOrder >= 0 && int64(providedOrder) <= maxSafeInteger {
			order = providedOrder
		}
		result[runtimeKey] = order
	}
	return result, nil
}

func groupByProtocolProfile[T any](accounts []T, base func(T) GatewayHotQualityAccountView) ([]protocolAccountGroup[T], error) {
	var groups []protocolAccountGroup[T]
	indexByProfile := make(map[string]int)
	for _, account := range accounts {
		view := base(account)
		profile, err := runtimeRequiredKey(
			orDefaultString(view.ProviderProtocolProfileID, view.ProtocolCode+":"+view.ProtocolVersion),
			"protocolProfile",
		)
		if err != nil {
			return nil, err
		}
		if index, ok := indexByProfile[profile]; ok {
			groups[index].accounts = append(groups[index].accounts, account)
			continue
		}
		indexByProfile[profile] = len(groups)
		groups = append(groups, protocolAccountGroup[T]{protocolProfile: profile, accounts: []T{account}})
	}
	return groups, nil
}

func hotQualityScopeForAccount(
	account GatewayHotQualityAccountView,
	requestLane string,
	model *string,
) (HotQualityScope, error) {
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		return HotQualityScope{}, err
	}
	protocolProfile, err := runtimeRequiredKey(
		orDefaultString(account.ProviderProtocolProfileID, account.ProtocolCode+":"+account.ProtocolVersion),
		"protocolProfile",
	)
	if err != nil {
		return HotQualityScope{}, err
	}
	return HotQualityScope{
		AccountRuntimeKey: runtimeKey,
		ProtocolProfile:   protocolProfile,
		RequestLane:       requestLane,
		ModelFamily:       GatewayHotQualityModelFamily(model),
	}, nil
}

var gatewayModelFamilyCatalog *HotQualityModelFamilyCatalog
var gatewayModelFamilyCatalogOnce sync.Once

// GatewayHotQualityModelFamily mirrors gatewayHotQualityModelFamily.
func GatewayHotQualityModelFamily(model *string) HotQualityModelFamily {
	gatewayModelFamilyCatalogOnce.Do(func() {
		families := make([]string, 0, hotQualityModelFamilyBucketCount)
		for index := 0; index < hotQualityModelFamilyBucketCount; index++ {
			families = append(families, fmt.Sprintf("model-bucket-%02x", index))
		}
		catalog, err := NewHotQualityModelFamilyCatalog(families, HotQualityModelFamilyCatalogLimit)
		if err != nil {
			panic(err)
		}
		gatewayModelFamilyCatalog = catalog
	})
	if model == nil {
		return HotQualityUnknownModelFamily
	}
	normalized := strings.ToLower(strings.TrimSpace(*model))
	if normalized == "" || len(normalized) > 256 {
		return HotQualityUnknownModelFamily
	}
	for i := 0; i < len(normalized); i++ {
		c := normalized[i]
		if c <= 0x1f || c == 0x7f {
			return HotQualityUnknownModelFamily
		}
	}
	digest := sha256.Sum256([]byte(normalized))
	return gatewayModelFamilyCatalog.Resolve(fmt.Sprintf("model-bucket-%02x", digest[0]))
}

func modelRank(account GatewayHotQualityAccountView, rankByAccountID map[string]int) int {
	value, ok := rankByAccountID[account.ID]
	if !ok {
		return 3
	}
	if value < 0 || int64(value) > maxSafeInteger {
		return 3
	}
	return value
}

type gatewayHotQualityRouteScopeKeyInput struct {
	SystemAccountID string
	RouteStrategyID string
	GroupID         string
	ProtocolProfile string
	RequestLane     string
}

// GatewayHotQualityRouteScopeKey mirrors gatewayHotQualityRouteScopeKey.
func GatewayHotQualityRouteScopeKey(input gatewayHotQualityRouteScopeKeyInput) (string, error) {
	systemAccountID, err := runtimeRequiredKey(input.SystemAccountID, "systemAccountId")
	if err != nil {
		return "", err
	}
	groupID, err := runtimeRequiredKey(input.GroupID, "groupId")
	if err != nil {
		return "", err
	}
	protocolProfile, err := runtimeRequiredKey(input.ProtocolProfile, "protocolProfile")
	if err != nil {
		return "", err
	}
	routeStrategyID := strings.TrimSpace(input.RouteStrategyID)
	if routeStrategyID == "" {
		routeStrategyID = "direct"
	}
	return encodedScopeKey([]string{systemAccountID, routeStrategyID, groupID, protocolProfile, input.RequestLane}), nil
}

// SameTierExplorationPoolKey mirrors sameTierExplorationPoolKey.
func SameTierExplorationPoolKey(routeScopeKey string, configurationTierKey string) (string, error) {
	normalizedRouteScopeKey, err := runtimeRequiredKey(routeScopeKey, "routeScopeKey")
	if err != nil {
		return "", err
	}
	normalizedTierKey, err := runtimeRequiredKey(configurationTierKey, "configurationTierKey")
	if err != nil {
		return "", err
	}
	return encodedScopeKey([]string{normalizedRouteScopeKey, normalizedTierKey}), nil
}

// runtimeRequiredKey is the error-returning form of requiredRuntimeKey.
func runtimeRequiredKey(value string, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("%s不能为空", name)
	}
	return normalized, nil
}

func runtimeNormalizedNow(value int64) (int64, error) {
	if value < 0 || value > maxSafeInteger {
		return 0, errors.New("当前时间必须是非负安全整数")
	}
	return value, nil
}

func orDefaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func intPtr(value int) *int { return &value }

func defaultUnixMilli() int64 { return time.Now().UnixMilli() }

// newUUIDv4 mirrors Node randomUUID().
func newUUIDv4() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

func selectionViewOrNil(snapshot *HotQualitySnapshot) *gatewayhybrid.HotQualitySnapshot {
	if snapshot == nil {
		return nil
	}
	view := snapshot.SelectionView()
	return &view
}

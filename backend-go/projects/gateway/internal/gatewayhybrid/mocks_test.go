package gatewayhybrid

import (
	"context"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Shared test doubles: injected clock, mock dispatcher, recorder, selector,
// identity, body gateway, diagnostics and state store.

func testClock(start *time.Time) Clock {
	return func() time.Time { return *start }
}

func advanceClock(start *time.Time, delta time.Duration) {
	*start = start.Add(delta)
}

// mockDispatcher records dispatch inputs and replays scripted outcomes.
type mockDispatcher struct {
	mu        sync.Mutex
	inputs    []AuxiliaryDispatchInput
	script    []dispatchOutcome
	finishLog []dispatchFinish
}

type dispatchOutcome struct {
	success *AuxiliaryDispatchSuccess
	failure *AuxiliaryDispatchFailure
}

type dispatchFinish struct {
	success bool
	code    string
	message string
}

func (mock *mockDispatcher) DispatchHybridAuxiliaryChatCompletion(ctx context.Context, input AuxiliaryDispatchInput) (AuxiliaryDispatchSuccess, *AuxiliaryDispatchFailure) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	mock.inputs = append(mock.inputs, input)
	index := len(mock.inputs) - 1
	if index >= len(mock.script) {
		index = len(mock.script) - 1
	}
	outcome := mock.script[index]
	if outcome.failure != nil {
		return AuxiliaryDispatchSuccess{}, outcome.failure
	}
	success := *outcome.success
	originalFinish := success.Finish
	success.Finish = func(ctx context.Context, finish AuxiliaryDispatchFinishInput) error {
		mock.mu.Lock()
		mock.finishLog = append(mock.finishLog, dispatchFinish{success: finish.Success, code: finish.ErrorCode, message: finish.ErrorMessage})
		mock.mu.Unlock()
		if originalFinish != nil {
			return originalFinish(ctx, finish)
		}
		return nil
	}
	return success, nil
}

func (mock *mockDispatcher) dispatchCount() int {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	return len(mock.inputs)
}

func successDispatch(accountID string, groupID string, statusCode int, bodyText string, usage gatewayproto.ParsedUsage) *AuxiliaryDispatchSuccess {
	return &AuxiliaryDispatchSuccess{
		Account:               OpenAIAccountSecret{ID: accountID},
		GroupID:               groupID,
		StatusCode:            statusCode,
		ResponseBody:          []byte(bodyText),
		ResponseBodyText:      bodyText,
		ResponseBodyTruncated: false,
		ParsedResponseBody:    ParseNonStreamJSONBody(bodyText, "application/json"),
		Usage:                 usage,
	}
}

func failureDispatch(code string, message string, accountID string, groupID string, statusCode int, shouldRecord bool) *AuxiliaryDispatchFailure {
	failure := &AuxiliaryDispatchFailure{
		ErrorCode:         code,
		ErrorMessage:      message,
		ShouldRecordUsage: shouldRecord,
		HasGroupID:        groupID != "",
		GroupID:           groupID,
		HasStatusCode:     statusCode != 0,
		StatusCode:        statusCode,
	}
	if accountID != "" {
		failure.Account = &OpenAIAccountSecret{ID: accountID}
	}
	return failure
}

// mockRecorder records usage attempts.
type mockRecorder struct {
	mu      sync.Mutex
	records []ScoringAttemptRecord
	err     error
}

func (mock *mockRecorder) RecordHybridScoringAttempt(ctx context.Context, record ScoringAttemptRecord) error {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.err != nil {
		return mock.err
	}
	mock.records = append(mock.records, record)
	return nil
}

// mockSelector replays selections per target model.
type mockSelector struct {
	selections map[string]*TargetGroupSelection
	err        error
	calls      []string
}

func (mock *mockSelector) SelectTargetGroup(ctx context.Context, input TargetGroupSelectorInput) (*TargetGroupSelection, error) {
	mock.calls = append(mock.calls, input.TargetModel)
	if mock.err != nil {
		return nil, mock.err
	}
	return mock.selections[input.TargetModel], nil
}

// mockIdentity derives a deterministic affinity key.
type mockIdentity struct {
	hmacSalt string
}

func (mock *mockIdentity) HybridRouteAffinityKey(view *GatewayRequestView, scope AffinityKeyScope) string {
	if view == nil || view.ConversationKey == "" {
		return ""
	}
	return "aff:" + view.ConversationKey + ":" + scope.SystemAccountID + ":" + scope.APIKeyID + ":" + scope.RouteStrategyID + ":" + scope.GroupID
}

// mockBodyGateway mirrors the mutable request body contract.
type mockBodyGateway struct {
	object        *OrderedJSON
	rawBody       []byte
	replaceOK     bool
	parseValue    any
	parseErr      error
	replaceParsed bool
	replacedModel string
}

func (mock *mockBodyGateway) ReplaceModel(targetModel string) bool {
	if mock.replaceOK && mock.object != nil {
		mock.object.Set("model", targetModel)
		mock.replacedModel = targetModel
		return true
	}
	return false
}

func (mock *mockBodyGateway) HasRawBody() bool { return len(mock.rawBody) > 0 }

func (mock *mockBodyGateway) ParseRawBody(ctx context.Context) (any, error) {
	if mock.parseErr != nil {
		return nil, mock.parseErr
	}
	if mock.parseValue != nil {
		return mock.parseValue, nil
	}
	parsed, err := ParseJSONOrdered(mock.rawBody)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func (mock *mockBodyGateway) ReplaceModelWithParsed(targetModel string, parsed *OrderedJSON) bool {
	if mock.replaceParsed && parsed != nil {
		mock.replacedModel = targetModel
		return true
	}
	return false
}

// mockDiagnostics records published diagnostics.
type mockDiagnostics struct {
	mu     sync.Mutex
	values []*OrderedJSON
}

func (mock *mockDiagnostics) PublishHybridRouteDecision(metadata *OrderedJSON) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	mock.values = append(mock.values, metadata)
}

// mockAudit records gateway metadata.
type mockAudit struct {
	labels    []string
	metadatas []*OrderedJSON
}

func (mock *mockAudit) AddGatewayMetadata(label string, metadata *OrderedJSON) {
	mock.labels = append(mock.labels, label)
	mock.metadatas = append(mock.metadatas, metadata)
}

// mockStateStore is an in-memory RuntimeStateStore for driver tests.
type mockStateStore struct {
	mu     sync.Mutex
	values map[string][]byte
	ttls   map[string]int64
}

func newMockStateStore() *mockStateStore {
	return &mockStateStore{values: map[string][]byte{}, ttls: map[string]int64{}}
}

func (store *mockStateStore) GetJSON(ctx context.Context, key string, value any) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, ok := store.values[key]
	if !ok {
		return false, nil
	}
	return true, unmarshalInto(raw, value)
}

func (store *mockStateStore) SetJSON(ctx context.Context, key string, value any, ttlMs int64) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	encoded, err := jsonMarshal(value)
	if err != nil {
		return err
	}
	store.values[key] = encoded
	store.ttls[key] = ttlMs
	return nil
}

// mockSharedCache is an in-memory SharedJSONCache.
type mockSharedCache struct {
	mu      sync.Mutex
	values  map[string]HybridScoringCacheEntry
	getErr  error
	setErr  error
	cleared int
}

func newMockSharedCache() *mockSharedCache {
	return &mockSharedCache{values: map[string]HybridScoringCacheEntry{}}
}

func (mock *mockSharedCache) Get(ctx context.Context, key string) (*HybridScoringCacheEntry, error) {
	if mock.getErr != nil {
		return nil, mock.getErr
	}
	if entry, ok := mock.values[key]; ok {
		return &entry, nil
	}
	return nil, nil
}

func (mock *mockSharedCache) Set(ctx context.Context, key string, entry HybridScoringCacheEntry, ttlMs int64) error {
	if mock.setErr != nil {
		return mock.setErr
	}
	mock.values[key] = entry
	return nil
}

func (mock *mockSharedCache) Clear(ctx context.Context) error {
	mock.cleared++
	mock.values = map[string]HybridScoringCacheEntry{}
	return nil
}

// testLevelRoute mirrors the shape used to build configs in tests.
type testLevelRoute struct {
	MinLevel    int
	MaxLevel    int
	TargetModel string
	Enabled     bool
}

func configWithRoutes(routes []testLevelRoute, fallbackMaxLevel int) *routestrategies.HybridRoutingConfig {
	config := hybridConfig()
	config.LevelRoutes = config.LevelRoutes[:0]
	for _, route := range routes {
		config.LevelRoutes = append(config.LevelRoutes, routestrategies.HybridLevelRoute{
			MinLevel:    route.MinLevel,
			MaxLevel:    route.MaxLevel,
			TargetModel: route.TargetModel,
			Enabled:     route.Enabled,
		})
	}
	config.ScoringFallbackMaxLevel = fallbackMaxLevel
	return config
}

func testLevelRoutes() []testLevelRoute {
	return []testLevelRoute{
		{MinLevel: 1, MaxLevel: 3, TargetModel: "m-low", Enabled: true},
		{MinLevel: 4, MaxLevel: 6, TargetModel: "m-mid", Enabled: true},
		{MinLevel: 7, MaxLevel: 10, TargetModel: "m-high", Enabled: false},
		{MinLevel: 7, MaxLevel: 10, TargetModel: "m-high2", Enabled: true},
	}
}

// hybridConfig builds a default normalized hybrid config for tests
// (1-5 gpt-5-mini, 6-10 gpt-5).
func hybridConfig() *routestrategies.HybridRoutingConfig {
	return &routestrategies.HybridRoutingConfig{
		ScoringModel:                 "gpt-scoring",
		ScoringContextMode:           "full_request",
		QualityPreference:            "balanced",
		ScoringTimeoutMs:             15_000,
		ScoringFallbackMaxLevel:      5,
		ScoringCacheEnabled:          true,
		ScoringCacheTTLSeconds:       300,
		CacheAffinityEnabled:         true,
		AffinityTTLSeconds:           900,
		SwitchMinLevelDelta:          2,
		DowngradeConsecutiveLowCount: 2,
		LevelRoutes: []routestrategies.HybridLevelRoute{
			{MinLevel: 1, MaxLevel: 5, TargetModel: "gpt-5-mini", Enabled: true},
			{MinLevel: 6, MaxLevel: 10, TargetModel: "gpt-5", Enabled: true},
		},
		QualityInspection: &routestrategies.HybridQualityInspection{
			Enabled:           true,
			ScoringModel:      "gpt-scoring",
			TriggerMode:       "risk_based",
			MaxTriggerLevel:   6,
			MaxRetries:        2,
			FailureAction:     "repair_then_upgrade",
			UnavailableAction: "pass_through",
		},
	}
}

func floatPtr(value float64) *float64 { return &value }
func intPtr(value int) *int           { return &value }
func int64Ptr(value int64) *int64     { return &value }
func boolPtr(value bool) *bool        { return &value }
func strPtr(value string) *string     { return &value }

func gatewayprotoEmptyUsage() gatewayproto.ParsedUsage {
	return gatewayproto.EmptyUsage()
}

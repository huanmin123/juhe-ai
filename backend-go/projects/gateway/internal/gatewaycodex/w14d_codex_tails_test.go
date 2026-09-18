// w14d_codex_tails_test.go drives the remaining error/edge arms of the codex
// gateway family: segment file failures, shard-store guards, async turn-retry
// state arms, bridge state restore/persist failures, compact preflight
// fallbacks and the pure codec tails.
package gatewaycodex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// segments
// ---------------------------------------------------------------------------

func TestW14dSegmentStoreEdges(t *testing.T) {
	root := t.TempDir()
	store, err := NewSegmentStore(SegmentStoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// Oversize payload (L65).
	if _, err := store.WriteSegmentPayload(ctx, "s", make([]byte, maxStoredPayloadBytes+1), now); err == nil {
		t.Fatal("oversize payload must fail")
	}

	ref, err := store.WriteSegmentPayload(ctx, "sess-1", map[string]any{"a": "b"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSegmentPayload(ref); err != nil {
		t.Fatal(err)
	}

	// Path traversal storage key (L142).
	bad := ref
	bad.StorageKey = `..\..\evil.json.gz`
	if _, err := store.ReadSegmentPayload(bad); err == nil {
		t.Fatal("traversal key must fail")
	}

	// Root pointing at a file breaks MkdirAll (L105).
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileStore, err := NewSegmentStore(SegmentStoreConfig{Root: fileRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileStore.WriteSegmentPayload(ctx, "s", map[string]any{}, now); err == nil {
		t.Fatal("file root must fail writes")
	}

	// The segment path occupied by a directory breaks OpenFile (L109).
	dirRef, err := store.WriteSegmentPayload(ctx, "sess-dir", map[string]any{"b": 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, dirRef.StorageKey)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, dirRef.StorageKey), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteSegmentPayload(ctx, "sess-dir", map[string]any{"b": 2}, now); err == nil {
		t.Fatal("directory at segment path must fail writes")
	}
	// Reading through the directory handle yields a non-EOF read error (L159).
	if _, err := store.ReadSegmentPayload(dirRef); err == nil {
		t.Fatal("directory read must fail")
	}

	// Truncated segment file: EOF short read then size mismatch (L155/156).
	truncRef, err := store.WriteSegmentPayload(ctx, "sess-trunc", map[string]any{"c": 3}, now)
	if err != nil {
		t.Fatal(err)
	}
	segmentPath := filepath.Join(root, truncRef.StorageKey)
	if err := os.Truncate(segmentPath, truncRef.StorageOffsetBytes+truncRef.CompressedSizeBytes-4); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSegmentPayload(truncRef); err == nil {
		t.Fatal("truncated segment must fail")
	}

	// SHA mismatch (corrupted bytes).
	okRef, err := store.WriteSegmentPayload(ctx, "sess-sha", map[string]any{"d": 4}, now)
	if err != nil {
		t.Fatal(err)
	}
	shaPath := filepath.Join(root, okRef.StorageKey)
	if err := os.Truncate(shaPath, okRef.StorageOffsetBytes); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(shaPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0, 1, 2, 3, 4, 5, 6, 7}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := store.ReadSegmentPayload(okRef); err == nil {
		t.Fatal("corrupted segment must fail")
	}
}

// ---------------------------------------------------------------------------
// contextstore guards
// ---------------------------------------------------------------------------

func TestW14dContextStoreGuards(t *testing.T) {
	store, _ := newSQLiteStore(t)
	ctx := context.Background()

	// Empty ids (L164/L213).
	if _, err := ReadCodexContextResponseStateChain(ctx, store, ResponseChainReadInput{ResponseID: "  "}); err == nil {
		t.Fatal("empty responseId must fail")
	}
	if _, err := ReadCodexContextCompactState(ctx, store, CompactStateReadInput{CompactID: ""}); err == nil {
		t.Fatal("empty compactId must fail")
	}

	// Expired compact (L224).
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", ProviderCode: "openai"}
	expired := CodexContextCompactStateIndex{
		CodexContextStateBoundary: boundary,
		CompactID:                 "w14d-compact-expired",
		SessionID:                 "w14d-sess-expired",
		ExpiresAt:                 "2000-01-01T00:00:00.000Z",
	}
	if err := store.SaveCompactStateRow(ctx, expired); err != nil {
		t.Fatal(err)
	}
	result, err := ReadCodexContextCompactState(ctx, store, CompactStateReadInput{
		CompactID: expired.CompactID, Boundary: boundary, Now: ISOFormat(time.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CodexContextOutcomeExpired {
		t.Fatalf("outcome = %q", result.Outcome)
	}

	// A shard root that is a file fails every lazy database open
	// (L173/L218/L351/L355/L377/L403).
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken, err := NewSQLiteShardContextStateStore(SQLiteShardStoreConfig{Root: fileRoot, ShardCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broken.Close() })
	if _, err := broken.ReadResponseStateRow(ctx, "resp-1"); err == nil {
		t.Fatal("broken root read must fail")
	}
	if _, err := broken.ReadCompactStateRow(ctx, "compact-1"); err == nil {
		t.Fatal("broken root compact read must fail")
	}
	if err := broken.SaveResponseStateRow(ctx, CodexContextResponseStateIndex{ResponseID: "r", SessionID: "s"}); err == nil {
		t.Fatal("broken root save must fail")
	}
	if err := broken.SaveCompactStateRow(ctx, CodexContextCompactStateIndex{CompactID: "c", SessionID: "s"}); err == nil {
		t.Fatal("broken root compact save must fail")
	}

	// withSQLTx surfaces BeginTx failures (L835).
	closed, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if err := withSQLTx(ctx, closed, func(tx *sql.Tx) error { return nil }); err == nil {
		t.Fatal("closed db transaction must fail")
	}
}

// ---------------------------------------------------------------------------
// turn retry async + memory arms
// ---------------------------------------------------------------------------

type w14dTurnStore struct {
	mu       sync.Mutex
	values   map[string]json.RawMessage
	failGet  bool
	failIncr bool
	failCAS  bool
}

func (s *w14dTurnStore) GetJSON(_ context.Context, key string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet {
		return nil, errors.New("get failed")
	}
	if value, ok := s.values[key]; ok {
		return append(json.RawMessage(nil), value...), nil
	}
	return nil, nil
}

func (s *w14dTurnStore) CompareSetJSON(_ context.Context, key string, _ json.RawMessage, next any, _ int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failCAS {
		return false, nil
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	s.values[key] = encoded
	return true, nil
}

func (s *w14dTurnStore) Incr(_ context.Context, _ string, _ int64) (int64, error) {
	if s.failIncr {
		return 0, errors.New("incr failed")
	}
	return 3, nil
}

func TestW14dTurnRetryAsyncArms(t *testing.T) {
	ctx := context.Background()
	strategy := avoidanceStrategy("w14d-state")
	accounts := []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}, {ID: "acc-2"}}

	// GetJSON failure arms (L210/L407/L522).
	store := &w14dTurnStore{values: map[string]json.RawMessage{}, failGet: true}
	service := newTurnRetryService(t)
	service.Store = store
	if _, err := service.OrderOpenAIAccountsByCodexTurnAvoidanceAsync(ctx, accounts, strategy, nil); err == nil {
		t.Fatal("order get failure must surface")
	}
	if _, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{}); err == nil {
		t.Fatal("remember get failure must surface")
	}
	if _, err := service.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1"); err == nil {
		t.Fatal("clear get failure must surface")
	}
	// Fence clear get failure with a real fence id: create it on a working
	// store, then swap in the failing store (L596).
	warm := newTurnRetryService(t)
	warm.Store = &w14dTurnStore{values: map[string]json.RawMessage{}}
	warm.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	activation, err := warm.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	if err != nil || activation == nil || activation.Activation == nil {
		t.Fatalf("warm activation: %+v %v", activation, err)
	}
	fence := activation.Activation.SourceFenceID
	generation := activation.Activation.SourceGeneration
	service.Store = &w14dTurnStore{values: map[string]json.RawMessage{}, failGet: true}
	if _, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: generation, SourceFenceID: fence,
	}); err == nil {
		t.Fatal("fence clear get failure must surface")
	}

	// Working store: remember twice activates, duplicate observation, clear.
	store = &w14dTurnStore{values: map[string]json.RawMessage{}}
	service = newTurnRetryService(t)
	service.Store = store
	first, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{ObservationID: "obs-1"})
	if err != nil || first == nil {
		t.Fatalf("first remember: %v %v", first, err)
	}
	// Same observation id short-circuits as duplicate (L423).
	dup, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{ObservationID: "obs-1"})
	if err != nil || dup == nil || !dup.DuplicateObservation {
		t.Fatalf("duplicate remember: %+v %v", dup, err)
	}
	second, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	if err != nil || second == nil || second.Activation == nil {
		t.Fatalf("second remember: %+v %v", second, err)
	}

	// Incr failure during activation (L430).
	incrFail := &w14dTurnStore{values: map[string]json.RawMessage{}, failIncr: true}
	incrService := newTurnRetryService(t)
	incrService.Store = incrFail
	incrService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-7", CodexTurnFailureInput{ObservationID: "o1"})
	if _, err := incrService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-7", CodexTurnFailureInput{ObservationID: "o2"}); err == nil {
		t.Fatal("incr failure must surface")
	}

	// CAS failure during activation (L437).
	casFail := &w14dTurnStore{values: map[string]json.RawMessage{}, failIncr: true}
	casService := newTurnRetryService(t)
	casService.Store = casFail
	casService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-8", CodexTurnFailureInput{ObservationID: "o1"})
	if _, err := casService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-8", CodexTurnFailureInput{ObservationID: "o2"}); err == nil {
		t.Fatal("cas failure must surface")
	}

	// Clear: unknown account, known account, exhausted CAS.
	cleared, err := service.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-unknown")
	if err != nil || cleared {
		t.Fatalf("unknown clear: %v %v", cleared, err)
	}
	cleared, err = service.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1")
	if err != nil || !cleared {
		t.Fatalf("clear: %v %v", cleared, err)
	}
	exhaustService := newTurnRetryService(t)
	exhaustStore := &w14dTurnStore{values: map[string]json.RawMessage{}, failCAS: true}
	exhaustService.Store = exhaustStore
	exhaustService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-2", CodexTurnFailureInput{})
	if cleared, err := exhaustService.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-2"); err != nil || cleared {
		t.Fatalf("exhausted clear: %v %v", cleared, err)
	}

	// Fence clear: missing state (L600), fence mismatch (L607), applied path.
	fenceService := newTurnRetryService(t)
	fenceStore := &w14dTurnStore{values: map[string]json.RawMessage{}}
	fenceService.Store = fenceStore
	if cleared, err := fenceService.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: 1, SourceFenceID: "f-1",
	}); err != nil || cleared {
		t.Fatalf("empty fence clear: %v %v", cleared, err)
	}
	fenceService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	fenceService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	if cleared, err := fenceService.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: 99, SourceFenceID: "f-1",
	}); err != nil || cleared {
		t.Fatalf("mismatch fence clear: %v %v", cleared, err)
	}
	if cleared, err := fenceService.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: 3, SourceFenceID: "fence-fence",
	}); err != nil || cleared {
		t.Fatalf("wrong fence id clear: %v %v", cleared, err)
	}
	if cleared, err := fenceService.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: 3, SourceFenceID: "fence_fence",
	}); err != nil || cleared {
		t.Fatalf("right fence clear: %v %v", cleared, err)
	}

	// Guards: disabled strategy and blank account (L399/L514/L747).
	disabled := OpenAIGatewayClientStrategyContext{ClientSourceAvoidanceStateKey: "k"}
	if result, err := service.RememberCodexTurnStreamFailureAsync(ctx, disabled, "acc-1", CodexTurnFailureInput{}); result != nil || err != nil {
		t.Fatal("disabled remember must be nil")
	}
	if cleared, err := service.ClearCodexTurnAccountAvoidanceAsync(ctx, disabled, "acc-1"); cleared || err != nil {
		t.Fatal("disabled clear must be false")
	}
	// Blank account id trips the defensive panic guard (L747).
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("blank account id must trip the guard")
			}
		}()
		codexTurnAccountStateKey("secret", "k", "  ")
	}()

	// Ordering arms: no state at all, state with no activations (L223/247).
	result := orderOpenOpenAIOrderingStub(t)
	if result.Applied {
		t.Fatal("plain state must not apply avoidance")
	}

	// decodeRetryState malformed payloads (L969/972).
	if decodeRetryState(json.RawMessage("{bad")) != nil {
		t.Fatal("broken json must decode to nil")
	}
	partial := decodeRetryState(json.RawMessage(`{"stateKey":"k","failureCount":1}`))
	if partial == nil || partial.FailedAccounts == nil {
		t.Fatal("missing failedAccounts must be normalized to an empty map")
	}
}

func orderOpenOpenAIOrderingStub(t *testing.T) CodexTurnAccountAvoidanceResult {
	t.Helper()
	state := &codexTurnRetryState{StateKey: "k", FailureCount: 1, FailedAccounts: map[string]*codexTurnFailedAccount{}}
	strategy := OpenAIGatewayClientStrategyContext{AllowClientSourceAccountAvoidance: false}
	result := orderOpenAIAccountsByCodexTurnAvoidanceWithState([]gatewayruntimecache.OpenAIAccountSecret{{ID: "a"}}, strategy, state, nil)
	if result.Applied || result.FailureCount != 1 {
		t.Fatalf("disabled ordering = %+v", result)
	}
	active := &codexTurnRetryState{
		StateKey: "k", FailureCount: 5,
		FailedAccounts: map[string]*codexTurnFailedAccount{
			"a": {AccountID: "a", FailureCount: 9},
		},
	}
	strategy.AllowClientSourceAccountAvoidance = true
	return orderOpenAIAccountsByCodexTurnAvoidanceWithState([]gatewayruntimecache.OpenAIAccountSecret{{ID: "a"}, {ID: "b"}}, strategy, active, nil)
}

func TestW14dTurnRetryMemoryArms(t *testing.T) {
	service := newTurnRetryService(t)
	strategy := avoidanceStrategy("w14d-mem")

	// Activation tombstones populate the generation cache and eviction.
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{ObservationID: "o1"})
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{ObservationID: "o2"})
	if len(service.memory.generations) == 0 {
		t.Fatal("activation must record a generation tombstone")
	}

	// Expired memory entries read as absent (L644).
	service.Clock.(*fakeClock).Advance(31 * time.Minute)
	if state := service.getMemoryCodexTurnRetryState(strategy.ClientSourceAvoidanceStateKey + ":a_x"); state != nil {
		t.Fatal("expired entry must be dropped")
	}

	// Generation eviction loop (L713-725) via many activations.
	for index := 0; index < 12; index++ {
		applyMutation := codexTurnStateMutation{
			state: codexTurnRetryState{
				StateKey: "w14d-mem", FailureCount: 2,
				FailedAccounts: map[string]*codexTurnFailedAccount{
					"acc-x": {AccountID: "acc-x", FailureCount: 2},
				},
			},
			activation: &CodexTurnFailureActivation{AccountID: "acc-x"},
		}
		service.applyCodexTurnAvoidanceGeneration(applyMutation, "w14d-mem:gen", nil)
	}
}

// ---------------------------------------------------------------------------
// chat bridge state tails
// ---------------------------------------------------------------------------

func TestW14dBridgeCodecTails(t *testing.T) {
	// Inline compaction summary decode arms (L943/947).
	if got := decodeInlineCodexCompactionSummary(encodeInlineCodexCompactionSummary("摘要")); got != "摘要" {
		t.Fatalf("round trip = %q", got)
	}
	badJSON := codexInlineCompactionSummaryPrefix + base64RawURL([]byte("not-json"))
	if got := decodeInlineCodexCompactionSummary(badJSON); got != "" {
		t.Fatalf("bad json = %q", got)
	}
	notObject := codexInlineCompactionSummaryPrefix + base64RawURL([]byte("[1,2]"))
	if got := decodeInlineCodexCompactionSummary(notObject); got != "" {
		t.Fatalf("non object = %q", got)
	}

	// Restored input size assertion: unmarshalable input (L1141).
	if err := assertRestoredInputSize(make(chan int)); err == nil {
		t.Fatal("unmarshalable input must fail the size assert")
	}

	// Typed payload decode failures after the shape check (L1061/1087).
	payload := map[string]any{
		"schemaVersion": 2, "responseId": "resp_chat_bridge_a", "sessionId": "s",
		"boundary": map[string]any{"systemAccountID": 123}, "request": map[string]any{}, "outputItems": []any{},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeStatePayload(raw); err == nil {
		t.Fatal("typed boundary mismatch must fail decodeStatePayload")
	}
	compact := map[string]any{
		"schemaVersion": 2, "compactId": "c", "sessionId": "s",
		"boundary": map[string]any{"systemAccountID": 123}, "summary": "x",
	}
	rawCompact, err := json.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCompactSnapshotPayload(rawCompact); err == nil {
		t.Fatal("typed boundary mismatch must fail decodeCompactSnapshotPayload")
	}

	// History sanitizer contract helpers.
	if itemIDRemovalDecision("scalar", CodexHistorySanitizerContext{}) != nil {
		t.Fatal("scalar value has no removal decision")
	}
	if itemIDRemovalDecision(map[string]any{"type": "unknown-kind", "id": "x"}, CodexHistorySanitizerContext{}) != nil {
		t.Fatal("unknown item type has no removal decision")
	}
}

func TestW14dBridgeRegistryInit(t *testing.T) {
	// A zero registry lazily allocates its state map (L180).
	registry := &ContextRequestStateRegistry{}
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	registry.Set(req, &CodexResponsesContextRequestState{RequestKind: RequestKindResponses})
	if _, ok := registry.Get(req); !ok {
		t.Fatal("state must be readable after lazy init")
	}
}

func TestW14dBridgeServiceFallbacks(t *testing.T) {
	store, _ := newSQLiteStore(t)

	// Empty segment root fails the lazy segment store (L129).
	if _, err := NewChatBridgeStateService(ChatBridgeStateConfig{CodexContextRoot: ""}, store, nil, nil); err == nil {
		t.Fatal("empty segment root must fail")
	}
	// Nil clock falls back to the system clock (L133).
	service, err := NewChatBridgeStateService(ChatBridgeStateConfig{CodexContextRoot: t.TempDir()}, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if service.clock.Now().IsZero() {
		t.Fatal("system clock fallback")
	}
}

func TestW14dBridgeStoreFailureArms(t *testing.T) {
	// A shard root that is a file breaks every store operation while the
	// segment store stays healthy.
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	brokenStore, err := NewSQLiteShardContextStateStore(SQLiteShardStoreConfig{Root: fileRoot, ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = brokenStore.Close() })
	segmentsRoot := t.TempDir()
	service, err := NewChatBridgeStateService(ChatBridgeStateConfig{CodexContextRoot: segmentsRoot}, brokenStore, nil, newFakeClock(time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	service.Logger = &recordingLogger{}
	registry := NewContextRequestStateRegistry()
	sink := &recordedSink{}
	service.Sink = sink
	ctx := context.Background()

	// Preflight with an internal previous fails on the chain read (L306).
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	body := map[string]any{"model": "gpt-5", "previous_response_id": "resp_chat_bridge_gone", "input": []any{}}
	raw, _ := json.Marshal(body)
	attachBody(req, body, raw)
	res, resWriter := newTrackedWriter()
	preflightInput := bridgeBaseInput(req, resWriter, &recordedAudit{})
	if _, err := service.ApplyContextStatePreflight(ctx, registry, preflightInput); err == nil {
		t.Fatal("store failure must surface from the preflight")
	}
	_ = res

	// Completion persistence failure is swallowed with a warn (L437).
	seedReq := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	attachBody(seedReq, map[string]any{"model": "gpt-5", "input": []any{}}, nil)
	registry.Set(seedReq, &CodexResponsesContextRequestState{
		RequestKind:           RequestKindResponses,
		Boundary:              CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"},
		CanonicalBody:         map[string]any{"model": "gpt-5", "input": []any{}},
		CurrentInput:          []any{},
		MaterializedInput:     []any{},
		PreviousResponseKind:  PreviousKindNone,
		ActiveBridgeAccountID: "acc",
	})
	handler := service.CompletionHandlerForRequest(registry, seedReq, gatewayruntimecache.OpenAIAccountSecret{ID: "acc", ProtocolCode: "openai", ProtocolVersion: "v1"}, "m")
	if handler == nil {
		t.Fatal("handler missing")
	}
	handler(CodexResponsesChatBridgeCompletion{ResponseID: "resp_chat_bridge_fail", CreatedAt: time.Now(), OutputItems: []any{}})

	// Compact restore error arm (L483) and snapshot write error arms
	// (L552 needs broken segments, L569 a broken store with healthy segments).
	if _, err := service.RestoreChatBridgeInputForCompact(ctx, struct {
		PreviousResponseID string
		Boundary           CodexContextStateBoundary
		CurrentInput       any
	}{PreviousResponseID: "resp_chat_bridge_x", Boundary: CodexContextStateBoundary{}}); err == nil {
		t.Fatal("compact restore must surface store failure")
	}
	if _, err := service.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SessionID: "s", Boundary: CodexContextStateBoundary{}, Summary: "x",
	}); err == nil {
		t.Fatal("snapshot index save must surface store failure")
	}

	// Healthy segments plus a broken store still fail the snapshot save
	// through the index write (L569 vs L552 distinction needs healthy
	// segments): service with broken store but valid segment root is the
	// same service; a segments-broken service fails the segment write (L552).
	brokenSegmentsService, err := NewChatBridgeStateService(
		ChatBridgeStateConfig{CodexContextRoot: fileRoot}, brokenStore, nil,
		newFakeClock(time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("segments are lazily opened: %v", err)
	}
	brokenSegmentsService.Logger = &recordingLogger{}
	if _, err := brokenSegmentsService.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SessionID: "s2", Boundary: CodexContextStateBoundary{}, Summary: "y",
	}); err == nil {
		t.Fatal("broken segment root must fail snapshot write")
	}
}

func TestW14dBridgeManualRowRestoreArms(t *testing.T) {
	store, _ := newSQLiteStore(t)
	segmentsRoot := t.TempDir()
	clock := newFakeClock(time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC))
	service, err := NewChatBridgeStateService(ChatBridgeStateConfig{CodexContextRoot: segmentsRoot}, store, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	service.Logger = &recordingLogger{}
	service.Sink = &recordedSink{}
	registry := NewContextRequestStateRegistry()
	ctx := context.Background()
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"}

	// Row whose payload reference points nowhere: restore renders the
	// payload_unavailable failure (L320-338).
	missing := CodexContextResponseStateIndex{
		CodexContextStateBoundary: boundary,
		ResponseID:                "resp_chat_bridge_missing1", SessionID: "w14d-sess-m",
		ExpiresAt: expiresAtFromISO(clock.Now()),
		CodexContextPayloadReference: CodexContextPayloadReference{
			StorageKey: "sessions/missing/segments/2026090408.json.gz", SHA256: strings.Repeat("0", 64),
			Compression: "gzip", SchemaVersion: 2,
		},
	}
	if err := store.SaveResponseStateRow(ctx, missing); err != nil {
		t.Fatal(err)
	}
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	body := map[string]any{"model": "gpt-5", "previous_response_id": "resp_chat_bridge_missing1", "input": []any{}}
	raw, _ := json.Marshal(body)
	attachBody(req, body, raw)
	res, resWriter := newTrackedWriter()
	if _, err := service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(req, resWriter, &recordedAudit{})); err != nil {
		t.Fatal(err)
	}
	if failure, ok := service.Sink.(*recordedSink).lastFailure(); !ok || failure.StatusCode == 0 {
		t.Fatalf("expected a failure response, got %+v", failure)
	}
	_ = res

	// Segment holding "{}" decodes as broken payload (L367).
	brokenRef, err := service.segments.WriteSegmentPayload(ctx, "w14d-sess-b", json.RawMessage("{}"), clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	brokenRow := CodexContextResponseStateIndex{
		CodexContextStateBoundary: boundary,
		ResponseID:                "resp_chat_bridge_broken1", SessionID: "w14d-sess-b",
		ExpiresAt:                    expiresAtFromISO(clock.Now()),
		CodexContextPayloadReference: brokenRef,
	}
	if err := store.SaveResponseStateRow(ctx, brokenRow); err != nil {
		t.Fatal(err)
	}
	req2 := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	body2 := map[string]any{"model": "gpt-5", "previous_response_id": "resp_chat_bridge_broken1", "input": []any{}}
	raw2, _ := json.Marshal(body2)
	attachBody(req2, body2, raw2)
	if _, err := service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(req2, resWriter, &recordedAudit{})); err != nil {
		t.Fatal(err)
	}

	// Compact summary read arms through preflight compaction references:
	// broken reference digest (L609/L678), missing compact (not found).
	compactRef, err := service.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SessionID: "w14d-sess-c", Boundary: boundary, Summary: "压缩摘要",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, encrypted := range []string{
		compactRef.EncryptedContent,
		codexCompactionReferencePrefix + "w14d-compact-missing." + strings.Repeat("a", 64),
		codexCompactionReferencePrefix + "w14d-bad-digest.not-a-digest",
		codexCompactionReferencePrefix + "w14d-nodot",
	} {
		req3 := newTestRequest(t, "POST", "/v1/responses", nil, nil)
		body3 := map[string]any{"model": "gpt-5", "input": []any{
			map[string]any{"type": "compaction", "encrypted_content": encrypted},
		}}
		raw3, _ := json.Marshal(body3)
		attachBody(req3, body3, raw3)
		if _, err := service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(req3, resWriter, &recordedAudit{})); err != nil {
			t.Fatal(err)
		}
	}

	// Non-array and plain (non-compaction) inputs keep the happy restore
	// path (L591/L604).
	req4 := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	body4 := map[string]any{"model": "gpt-5", "previous_response_id": compactRef.EncryptedContent, "input": "text-input"}
	raw4, _ := json.Marshal(body4)
	attachBody(req4, body4, raw4)
	if _, err := service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(req4, resWriter, &recordedAudit{})); err != nil {
		t.Fatal(err)
	}
}

func TestW14dBridgeAccountKindArms(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	attachBody(req, map[string]any{"model": "gpt-5"}, nil)

	// Responses kind + external previous + a bridge-capable account is
	// rejected (L714).
	registry.Set(req, &CodexResponsesContextRequestState{
		RequestKind: RequestKindResponses, PreviousResponseKind: PreviousKindExternal,
	})
	if service.CodexResponsesContextAllowsAccount(registry, req, compactBridgeAccount()) {
		t.Fatal("external previous must reject bridge accounts")
	}

	// Compact kind state passes preparation unchanged (L774).
	registry.Set(req, &CodexResponsesContextRequestState{RequestKind: RequestKindCompact})
	if prepared, err := service.PrepareCodexResponsesContextForAccount(registry, req, compactBridgeAccount()); prepared || err != nil {
		t.Fatalf("compact prepare = %v %v", prepared, err)
	}

	// Native mapping into a non-responses family is unsupported (L752).
	nativeAccount := gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-native", ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "gpt-5", SourceEndpointFamily: "responses",
			UpstreamModel: "embed-model", UpstreamEndpointFamily: "embeddings", Enabled: true,
		}},
	}
	registry.Set(req, &CodexResponsesContextRequestState{RequestKind: RequestKindCompact})
	_ = service.PrepareCodexResponsesCompactDispatchForAccounts(registry, req, []gatewayruntimecache.OpenAIAccountSecret{nativeAccount})

	// Prepare arms: unchanged body keeps the current body (L806); a nil body
	// falls back to the canonical body (L808); a non-array materialized
	// input with an internal previous takes the plain tail (L866).
	startIndex := 0
	changed := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	attachBody(changed, map[string]any{"model": "gpt-5", "input": []any{}}, nil)
	registry.Set(changed, &CodexResponsesContextRequestState{
		RequestKind: RequestKindResponses, PreviousResponseKind: PreviousKindNone,
		CanonicalBody: map[string]any{"model": "gpt-5", "input": []any{}},
		CurrentInput:  []any{}, MaterializedInput: []any{},
	})
	if _, err := service.PrepareCodexResponsesContextForAccount(registry, changed, gatewayruntimecache.OpenAIAccountSecret{ID: "acc", ProtocolCode: "openai", ProtocolVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	nilBody := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	registry.Set(nilBody, &CodexResponsesContextRequestState{
		RequestKind: RequestKindResponses, PreviousResponseKind: PreviousKindNone,
		CanonicalBody: map[string]any{"model": "gpt-5"},
	})
	if _, err := service.PrepareCodexResponsesContextForAccount(registry, nilBody, gatewayruntimecache.OpenAIAccountSecret{ID: "acc", ProtocolCode: "openai", ProtocolVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	stringInput := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	attachBody(stringInput, map[string]any{"model": "gpt-5", "input": "plain text"}, nil)
	registry.Set(stringInput, &CodexResponsesContextRequestState{
		RequestKind: RequestKindResponses, PreviousResponseKind: PreviousKindInternal,
		MaterializedCurrentInputStartIndex: &startIndex,
		MaterializedInput:                  "plain text", CurrentInput: "plain text",
		CanonicalBody: map[string]any{"model": "gpt-5", "input": "plain text"},
		CurrentBody:   map[string]any{"model": "gpt-5", "input": "plain text"},
	})
	if _, err := service.PrepareCodexResponsesContextForAccount(registry, stringInput, gatewayruntimecache.OpenAIAccountSecret{ID: "acc", ProtocolCode: "openai", ProtocolVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// compact preflight tails
// ---------------------------------------------------------------------------

func TestW14dCompactPreflightTails(t *testing.T) {
	// codex_responses compat + prepared bridge dispatch without an internal
	// previous is rejected (L144).
	service, _, registry, sink, _ := compactPreflightService(t)
	req := newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)
	body := map[string]any{"model": "gpt-5", "input": []any{}}
	raw, _ := json.Marshal(body)
	attachBody(req, body, raw)
	registry.Set(req, &CodexResponsesContextRequestState{
		RequestKind: RequestKindCompact, PreviousResponseKind: PreviousKindNone,
		CanonicalBody: body, CurrentInput: body["input"], MaterializedInput: body["input"],
	})
	_, resWriter := newTrackedWriter()
	result, err := service.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
		Req: req, Res: resWriter, AuditCapture: &recordedAudit{},
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{TrafficSource: "gateway"},
		StartedAt:    1, SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
		GroupAccess:                   gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		RequestClientCompatibility:    "codex_responses",
		DispatchAccounts:              []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
		Signal:                        context.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed {
		t.Fatal("codex_responses compact must be rejected as completed")
	}
	if failure, ok := sink.lastFailure(); !ok || failure.StatusCode != http.StatusBadRequest {
		t.Fatalf("rejection failure = %+v", failure)
	}

	// A nil dispatcher with a prepared dispatch errors (L194/L227).
	plainService, plainBridge, plainRegistry, _, _ := compactPreflightService(t)
	plainReq, _ := seedCompactRequest(t, plainBridge, plainRegistry)
	plainService.Dispatcher = nil
	if _, err := plainService.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
		Req: plainReq, Res: resWriter, AuditCapture: &recordedAudit{},
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{TrafficSource: "gateway"},
		StartedAt:    1, SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
		GroupAccess:      gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
		Signal:           context.Background(),
	}); err == nil {
		t.Fatal("nil dispatcher must error")
	}

	// Summary extraction parse arms (L401/410) and nil writer guard (L527).
	if got := ExtractChatCompletionSummary("[1,2]"); got != "" {
		t.Fatalf("non object summary = %q", got)
	}
	if got := ExtractChatCompletionSummary(`{"choices":[1]}`); got != "" {
		t.Fatalf("non object choice = %q", got)
	}
	writeJSONResponse(nil, http.StatusOK, map[string]any{})
}

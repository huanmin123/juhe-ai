package gatewaycircuit

// w11c: redis store validation, degraded/invalid responses and pure helpers.

import (
	"context"
	"errors"
	"strings"
	"testing"

	redis "github.com/redis/go-redis/v9"
	miniredis "github.com/alicebob/miniredis/v2"
)

// w11cStubRedis overrides selected commands of an embedded Cmdable.
type w11cStubRedis struct {
	redis.Cmdable
	evalErr       error
	evalValue     any
	hgetErr       error
	hgetErrValue any
	hlenErr       error
	zcountErr     error
}

func (s *w11cStubRedis) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	if s.evalErr != nil {
		cmd.SetErr(s.evalErr)
		return cmd
	}
	cmd.SetVal(s.evalValue)
	return cmd
}

func (s *w11cStubRedis) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	if s.hgetErr != nil {
		cmd.SetErr(s.hgetErr)
		return cmd
	}
	if value, ok := s.hgetErrValue.(string); ok && value != "" {
		cmd.SetVal(value)
		return cmd
	}
	cmd.SetErr(redis.Nil)
	return cmd
}

func (s *w11cStubRedis) HLen(ctx context.Context, key string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx)
	if s.hlenErr != nil {
		cmd.SetErr(s.hlenErr)
		return cmd
	}
	cmd.SetVal(0)
	return cmd
}

func (s *w11cStubRedis) ZCount(ctx context.Context, key, min, max string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx)
	if s.zcountErr != nil {
		cmd.SetErr(s.zcountErr)
		return cmd
	}
	cmd.SetVal(0)
	return cmd
}

func w11cStubStore(t *testing.T, stub *w11cStubRedis) *RedisStore {
	t.Helper()
	store, err := NewRedisStore(RedisStoreOptions{Client: stub, Capacity: 10, Now: func() int64 { return 0 }})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	return store
}

func TestW11CRedisStoreOptionValidation(t *testing.T) {
	// Missing redis URL.
	if _, err := NewRedisStore(RedisStoreOptions{Capacity: 10}); err == nil {
		t.Fatalf("expected redis url validation error")
	}
	// Malformed redis URL.
	if _, err := NewRedisStore(RedisStoreOptions{RedisURL: "://bad", Capacity: 10}); err == nil {
		t.Fatalf("expected parse url error")
	}
	server := miniredis.RunT(t)
	// Capacity / retention / replay validations.
	for _, tc := range []struct {
		name    string
		options RedisStoreOptions
	}{
		{"capacity", RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 0}},
		{"closedRetention", RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 5, ClosedRetentionMs: -1}},
		{"replayLimit", RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 5, ReplayLimitPerScope: -1}},
	} {
		if _, err := NewRedisStore(tc.options); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
	// Defaults apply without a name.
	store, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 5})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	if !strings.Contains(store.keys.states, "gateway-account-circuit") {
		t.Fatalf("default name missing: %s", store.keys.states)
	}
}

func TestW11CRedisMutationInputValidation(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 10, Now: func() int64 { return 0 }})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	ctx := context.Background()
	scope := accountScope("w11c-acc")
	bad := int64(-1)
	if _, err := store.Suspect(ctx, SuspectInput{Scope: scope, DispatchRevision: "7", TransitionID: "t", ConfirmationFailuresRequired: &bad}); err == nil {
		t.Fatalf("expected suspect confirmation validation error")
	}
	// Escalation input validation.
	base := ProtocolModelOpenEvidenceInput{
		Scope: protocolScope("w11c-acc"), Generation: 1, DispatchRevision: "7",
		EvidenceID: "e", AccountTransitionID: "p", Reason: "r",
		DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 8,
	}
	invalid := base
	invalid.ConfirmedFailureCount = 0
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, invalid); err == nil {
		t.Fatalf("expected confirmedFailureCount validation error")
	}
	invalid = base
	invalid.WindowMs = 0
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, invalid); err == nil {
		t.Fatalf("expected windowMs validation error")
	}
	invalid = base
	invalid.MaxProtocolScopes = 0
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, invalid); err == nil {
		t.Fatalf("expected maxProtocolScopes validation error")
	}
	invalid = base
	invalid.DistinctScopeThreshold = -1
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, invalid); err == nil {
		t.Fatalf("expected threshold validation error")
	}
	invalid = base
	invalid.DistinctScopeThreshold = 20
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, invalid); err == nil {
		t.Fatalf("expected threshold/max validation error")
	}
	invalid = base
	invalid.EvidenceID = " "
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, invalid); err == nil {
		t.Fatalf("expected evidenceId validation error")
	}
	invalid = base
	invalid.AccountTransitionID = " "
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, invalid); err == nil {
		t.Fatalf("expected accountTransitionId validation error")
	}
	invalid = base
	invalid.Reason = " "
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, invalid); err == nil {
		t.Fatalf("expected reason validation error")
	}
	// Clear escalation validation.
	if _, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: " ", DispatchRevision: "7", EvidenceID: "e",
	}); err == nil {
		t.Fatalf("expected accountRuntimeKey validation error")
	}
	if _, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "w11c-acc", DispatchRevision: " ", EvidenceID: "e",
	}); err == nil {
		t.Fatalf("expected dispatchRevision validation error")
	}
	if _, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "w11c-acc", DispatchRevision: "7", EvidenceID: "",
	}); err == nil {
		t.Fatalf("expected evidenceId validation error")
	}
	// Restore validation: scope key mismatch and bad confirmation state.
	badConfirmation := ClosedState(scope, "7", 1, "t", 0)
	badConfirmation.Phase = PhaseSuspect
	badConfirmation.ConfirmationFailuresRequired = &bad
	if _, err := store.Restore(ctx, badConfirmation, int64Ptr(0)); err == nil {
		t.Fatalf("expected restore normalization error")
	}
	mismatched := ClosedState(scope, "7", 1, "t", 0)
	mismatched.Scope = accountScope("other")
	if _, err := store.Restore(ctx, mismatched, int64Ptr(0)); err == nil {
		t.Fatalf("expected restore scope key error")
	}
	// ListDue limit validation.
	if _, err := store.ListDue(ctx, 0, 0); err == nil {
		t.Fatalf("expected listDue limit validation error")
	}
}

func TestW11CRedisDegradedEvalResponses(t *testing.T) {
	ctx := context.Background()
	scope := accountScope("w11c-acc")

	// Transport failure on Eval.
	stub := &w11cStubRedis{evalErr: errors.New("w11c eval down")}
	errStore := w11cStubStore(t, stub)
	if _, err := errStore.Get(ctx, scope, nil); err == nil {
		t.Fatalf("expected get eval error")
	}
	if _, err := errStore.Suspect(ctx, SuspectInput{Scope: scope, DispatchRevision: "7", TransitionID: "t"}); err == nil {
		t.Fatalf("expected suspect eval error")
	}
	if _, err := errStore.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{Scope: scope, DispatchRevision: "7", TransitionID: "t"}); err == nil {
		t.Fatalf("expected replace eval error")
	}
	if _, err := errStore.Restore(ctx, ClosedState(scope, "7", 1, "t", 0), int64Ptr(0)); err == nil {
		t.Fatalf("expected restore eval error")
	}
	if _, err := errStore.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", EvidenceID: "e", AccountTransitionID: "p", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 8,
	}); err == nil {
		t.Fatalf("expected escalation eval error")
	}
	if _, err := errStore.ReplaceAccountDispatchRevision(ctx, ReplaceAccountDispatchRevisionInput{
		AccountRuntimeKey: "w11c-acc", DispatchRevision: "9", TransitionID: "t",
	}); err == nil {
		t.Fatalf("expected account revision eval error")
	}

	// Non-string / empty Eval replies are rejected.
	for _, value := range []any{nil, "", 42} {
		stub := &w11cStubRedis{evalValue: value}
		badStore := w11cStubStore(t, stub)
		if _, err := badStore.Get(ctx, scope, nil); err == nil {
			t.Fatalf("expected invalid reply error for %v", value)
		}
		if _, err := badStore.Restore(ctx, ClosedState(scope, "7", 1, "t", 0), int64Ptr(0)); err == nil {
			t.Fatalf("expected invalid restore reply error for %v", value)
		}
	}
	// Malformed JSON replies are rejected.
	stub = &w11cStubRedis{evalValue: "{not-json"}
	badJSON := w11cStubStore(t, stub)
	if _, err := badJSON.Get(ctx, scope, nil); err == nil {
		t.Fatalf("expected malformed json error")
	}
	if _, err := badJSON.Restore(ctx, ClosedState(scope, "7", 1, "t", 0), int64Ptr(0)); err == nil {
		t.Fatalf("expected malformed restore json error")
	}
	if _, err := badJSON.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", EvidenceID: "e", AccountTransitionID: "p", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 8,
	}); err == nil {
		t.Fatalf("expected malformed escalation json error")
	}
	// Structurally empty transition results are rejected.
	stub = &w11cStubRedis{evalValue: `{"status":""}`}
	emptyStatus := w11cStubStore(t, stub)
	if _, err := emptyStatus.Get(ctx, scope, nil); err == nil {
		t.Fatalf("expected empty status error")
	}
	// Escalation result without status is rejected.
	stub = &w11cStubRedis{evalValue: `{"accountState":{}}`}
	emptyEscalation := w11cStubStore(t, stub)
	if _, err := emptyEscalation.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", EvidenceID: "e", AccountTransitionID: "p", Reason: "r",
		ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 8,
	}); err == nil {
		t.Fatalf("expected empty escalation error")
	}
	// Account revision paging that never converges is rejected.
	stub = &w11cStubRedis{evalValue: `{"statesCursor":"0","evidenceCursor":"done","changed":0}`}
	looping := w11cStubStore(t, stub)
	if _, err := looping.ReplaceAccountDispatchRevision(ctx, ReplaceAccountDispatchRevisionInput{
		AccountRuntimeKey: "w11c-acc", DispatchRevision: "9", TransitionID: "t",
	}); err == nil {
		t.Fatalf("expected cursor loop error")
	}
	// Empty account revision page is rejected.
	stub = &w11cStubRedis{evalValue: ""}
	emptyPage := w11cStubStore(t, stub)
	if _, err := emptyPage.ReplaceAccountDispatchRevision(ctx, ReplaceAccountDispatchRevisionInput{
		AccountRuntimeKey: "w11c-acc", DispatchRevision: "9", TransitionID: "t",
	}); err == nil {
		t.Fatalf("expected empty page error")
	}
}

func TestW11CRedisListDueAndSizeDegradedPaths(t *testing.T) {
	ctx := context.Background()

	// ListDue with a failing eval.
	stub := &w11cStubRedis{evalErr: errors.New("w11c listdue down")}
	errStore := w11cStubStore(t, stub)
	if _, err := errStore.ListDue(ctx, 0, 10); err == nil {
		t.Fatalf("expected listDue eval error")
	}
	// ListDue with an invalid page payload.
	stub = &w11cStubRedis{evalValue: "{bad"}
	badPage := w11cStubStore(t, stub)
	if _, err := badPage.ListDue(ctx, 0, 10); err == nil {
		t.Fatalf("expected listDue parse error")
	}
	// A page missing scopeKeys is rejected.
	stub = &w11cStubRedis{evalValue: `{"scanned":0,"nextOffset":0,"exhausted":true}`}
	missingKeys := w11cStubStore(t, stub)
	if _, err := missingKeys.ListDue(ctx, 0, 10); err == nil {
		t.Fatalf("expected listDue scopeKeys error")
	}
	// A page with a negative cursor is rejected.
	stub = &w11cStubRedis{evalValue: `{"scopeKeys":[],"scanned":-1,"nextOffset":0,"exhausted":true}`}
	negative := w11cStubStore(t, stub)
	if _, err := negative.ListDue(ctx, 0, 10); err == nil {
		t.Fatalf("expected listDue cursor error")
	}
	// An exhausted page with a stored state whose HGet fails hard.
	stub = &w11cStubRedis{evalValue: `{"scopeKeys":["w11c-key"],"scanned":1,"nextOffset":0,"exhausted":true}`, hgetErr: errors.New("w11c hget down")}
	hgetFail := w11cStubStore(t, stub)
	if _, err := hgetFail.ListDue(ctx, 0, 10); err == nil {
		t.Fatalf("expected listDue hget error")
	}
	// An invalid stored payload is rejected when the entry parses to an
	// empty state shape.
	stub = &w11cStubRedis{evalValue: `{"scopeKeys":["w11c-key"],"scanned":1,"nextOffset":0,"exhausted":true}`, hgetErrValue: "{\"state\":{}}"}
	invalidEntry := w11cStubStore(t, stub)
	if _, err := invalidEntry.ListDue(ctx, 0, 10); err == nil {
		t.Fatalf("expected listDue invalid entry error")
	}

	// Size with command failures.
	stub = &w11cStubRedis{hlenErr: errors.New("w11c hlen down")}
	sizeFail := w11cStubStore(t, stub)
	if _, err := sizeFail.Size(ctx); err == nil {
		t.Fatalf("expected size hlen error")
	}
	stub = &w11cStubRedis{zcountErr: errors.New("w11c zcount down")}
	zcountFail := w11cStubStore(t, stub)
	if _, err := zcountFail.Size(ctx); err == nil {
		t.Fatalf("expected size zcount error")
	}
	// Size with a failing cleanup script.
	stub = &w11cStubRedis{evalErr: errors.New("w11c size down")}
	sizeEval := w11cStubStore(t, stub)
	if _, err := sizeEval.Size(ctx); err == nil {
		t.Fatalf("expected size eval error")
	}
	// Size with an invalid cleanup reply.
	stub = &w11cStubRedis{evalValue: "{bad"}
	sizeBad := w11cStubStore(t, stub)
	if _, err := sizeBad.Size(ctx); err == nil {
		t.Fatalf("expected size parse error")
	}
}

func TestW11CRedisPureHelpers(t *testing.T) {
	// pointerNowMs handles int64, pointer and missing values.
	value := int64(7)
	if got := pointerNowMs(map[string]any{"nowMs": value}); got == nil || *got != 7 {
		t.Fatalf("pointerNowMs int64 = %v", got)
	}
	if got := pointerNowMs(map[string]any{"nowMs": &value}); got == nil || *got != 7 {
		t.Fatalf("pointerNowMs pointer = %v", got)
	}
	if pointerNowMs(map[string]any{}) != nil {
		t.Fatalf("pointerNowMs missing should be nil")
	}
	// cursorString accepts strings, floats, json numbers and falls back.
	if cursorString("5", "done") != "5" || cursorString("", "done") != "done" ||
		cursorString(3.0, "done") != "3" || cursorString(nil, "done") != "done" {
		t.Fatalf("cursorString misbehaves")
	}
	// numericRedisResult parsing.
	if got, err := numericRedisResult(int64(3)); err != nil || got != 3 {
		t.Fatalf("numeric int64 = (%d, %v)", got, err)
	}
	if got, err := numericRedisResult(4.0); err != nil || got != 4 {
		t.Fatalf("numeric float = (%d, %v)", got, err)
	}
	if got, err := numericRedisResult("5"); err != nil || got != 5 {
		t.Fatalf("numeric string = (%d, %v)", got, err)
	}
	if _, err := numericRedisResult("bad"); err == nil {
		t.Fatalf("expected numeric string error")
	}
	if _, err := numericRedisResult(nil); err == nil {
		t.Fatalf("expected numeric type error")
	}
	// redisStringResult for strings and bytes.
	if got, ok := redisStringResult("x"); !ok || got != "x" {
		t.Fatalf("redisStringResult string = (%s, %v)", got, ok)
	}
	if got, ok := redisStringResult([]byte("y")); !ok || got != "y" {
		t.Fatalf("redisStringResult bytes = (%s, %v)", got, ok)
	}
	if _, ok := redisStringResult(42); ok {
		t.Fatalf("redisStringResult should reject non strings")
	}
	// Key sanitization and namespacing.
	keys := redisAccountCircuitStoreKeys("My Store!", "team a")
	if !strings.Contains(keys.states, "My_Store_") {
		t.Fatalf("name sanitization = %s", keys.states)
	}
	if !strings.Contains(keys.states, "juhe-ai:team_a:account-circuit") {
		t.Fatalf("namespace insertion = %s", keys.states)
	}
	if got := redisAccountCircuitStoreKeys("", ""); !strings.Contains(got.states, "juhe-ai:account-circuit:gateway-account-circuit") {
		t.Fatalf("default keys = %s", got.states)
	}
	// Already-namespaced keys are not double-prefixed.
	if got := redisNamespacedKey("juhe-ai:team_a:account-circuit:x", "team_a"); got != "juhe-ai:team_a:account-circuit:x" {
		t.Fatalf("double prefix = %s", got)
	}
	if got := redisNamespacedKey("juhe-ai:x", "team_a"); got != "juhe-ai:team_a:x" {
		t.Fatalf("root insertion = %s", got)
	}
	if got := redisNamespacedKey("other", "team_a"); got != "juhe-ai:team_a:other" {
		t.Fatalf("plain insertion = %s", got)
	}
	if got := sanitizeRedisNamespacePart("a--b!!"); got != "a--b" {
		t.Fatalf("namespace sanitize = %s", got)
	}
	if got := sanitizeRedisNamespacePart("  "); got != "" {
		t.Fatalf("blank namespace = %q", got)
	}
	w11cExpectPanic(t, func() { redisNamespacedKey("", "") })
	// parseListDuePage happy path.
	page, err := parseListDuePage(`{"scopeKeys":["k1","k2"],"scanned":2,"nextOffset":2,"exhausted":false}`)
	if err != nil || len(page.scopeKeys) != 2 || page.scanned != 2 || page.nextOffset != 2 || page.exhausted {
		t.Fatalf("page = (%+v, %v)", page, err)
	}
	// validateOperationPayload guards.
	if err := validateOperationPayload("suspect", map[string]any{}); err == nil {
		t.Fatalf("expected transitionId guard")
	}
	if err := validateOperationPayload("suspect", map[string]any{"transitionId": "t"}); err == nil {
		t.Fatalf("expected dispatchRevision guard")
	}
	if err := validateOperationPayload("replace_revision", map[string]any{"transitionId": "t"}); err == nil {
		t.Fatalf("expected replace dispatchRevision guard")
	}
	if err := validateOperationPayload("get", map[string]any{}); err != nil {
		t.Fatalf("get should not require transitionId: %v", err)
	}
	// requiredEvidenceKeyPayload / payloadInt64 guards.
	evidence := strings.Repeat("e", 64)
	if err := requiredEvidenceKeyPayload(map[string]any{}, "failureEvidenceKey"); err == nil {
		t.Fatalf("expected evidence payload guard")
	}
	if err := requiredEvidenceKeyPayload(map[string]any{"failureEvidenceKey": "not-sha"}, "failureEvidenceKey"); err == nil {
		t.Fatalf("expected evidence type guard")
	}
	if err := requiredEvidenceKeyPayload(map[string]any{"failureEvidenceKey": evidence}, "failureEvidenceKey"); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
	if got, ok := payloadInt64(int64(3)); !ok || got != 3 {
		t.Fatalf("payloadInt64 int64 = (%d, %v)", got, ok)
	}
	if got, ok := payloadInt64(4.0); !ok || got != 4 {
		t.Fatalf("payloadInt64 float = (%d, %v)", got, ok)
	}
	if _, ok := payloadInt64("x"); ok {
		t.Fatalf("payloadInt64 should reject strings")
	}
	// decodeStrict rejects trailing values.
	if err := decodeStrict(`{"a":1} {"b":2}`, &map[string]any{}); err == nil {
		t.Fatalf("expected trailing json error")
	}
	if err := decodeStrict("{bad", &map[string]any{}); err == nil {
		t.Fatalf("expected json decode error")
	}
}

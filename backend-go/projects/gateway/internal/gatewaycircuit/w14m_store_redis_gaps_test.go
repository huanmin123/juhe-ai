package gatewaycircuit

// w14m 覆盖率补强：Redis store 的升级校验臂、分页游标臂、ListDue 扫描臂、
// Size 尾返回、Restore 结构校验与包内纯辅助函数。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	redis "github.com/redis/go-redis/v9"
	miniredis "github.com/alicebob/miniredis/v2"
)

// w14mEvalReply 描述一次 Redis 回复（值或错误）。
type w14mEvalReply struct {
	value any
	err   error
}

// w14mRedisStub 按序消费预设回复；耗尽后重复最后一个。
type w14mRedisStub struct {
	redis.Cmdable
	evals   []w14mEvalReply
	evalIdx int
	hgets   []w14mEvalReply
	hgetIdx int
	hlens   []w14mEvalReply
	hlenIdx int
	zcount  w14mEvalReply
}

func (s *w14mRedisStub) next(seq *[]w14mEvalReply, idx *int) w14mEvalReply {
	list := *seq
	if len(list) == 0 {
		return w14mEvalReply{err: errors.New("w14m-stub: 未预设回复")}
	}
	reply := list[*idx]
	if *idx < len(list)-1 {
		*idx++
	}
	return reply
}

func (s *w14mRedisStub) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	reply := s.next(&s.evals, &s.evalIdx)
	if reply.err != nil {
		cmd.SetErr(reply.err)
		return cmd
	}
	cmd.SetVal(reply.value)
	return cmd
}

func (s *w14mRedisStub) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	reply := s.next(&s.hgets, &s.hgetIdx)
	if reply.err != nil {
		cmd.SetErr(reply.err)
		return cmd
	}
	cmd.SetVal(reply.value.(string))
	return cmd
}

func (s *w14mRedisStub) HLen(ctx context.Context, key string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx)
	reply := s.next(&s.hlens, &s.hlenIdx)
	if reply.err != nil {
		cmd.SetErr(reply.err)
		return cmd
	}
	cmd.SetVal(reply.value.(int64))
	return cmd
}

func (s *w14mRedisStub) ZCount(ctx context.Context, key, min, max string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx)
	if s.zcount.err != nil {
		cmd.SetErr(s.zcount.err)
		return cmd
	}
	cmd.SetVal(s.zcount.value.(int64))
	return cmd
}

func w14mRedisStore(t *testing.T, client redis.Cmdable) *RedisStore {
	t.Helper()
	store, err := NewRedisStore(RedisStoreOptions{Client: client, Capacity: 10, Now: func() int64 { return 1_000 }})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	return store
}

func w14mAccountScope(tag string) Scope {
	return Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "w14m-" + tag}
}

func TestW14MRedisEvidencePayloadArmsViaMiniredis(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{
		RedisURL: "redis://" + server.Addr(), Capacity: 10, Now: func() int64 { return 1_000 },
	})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	ctx := context.Background()
	scope := w14mAccountScope("evidence")
	evidence := strings.Repeat("ab", 32)
	if _, err := store.Suspect(ctx, SuspectInput{
		Scope: scope, DispatchRevision: "rev-1", TransitionID: "w14m-t1",
		Reason: "r", FailureEvidenceKey: strPtr(evidence), NowMs: int64Ptr(1_000),
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	// AcquireConfirmationLease 显式提供 expected/confirmation evidence 键。
	if _, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "rev-1", TransitionID: "w14m-t2",
		LeaseID: "w14m-lease", LeaseUntilMs: 9_000,
		ExpectedFailureEvidenceKey: strPtr(evidence), ConfirmationEvidenceKey: strPtr(strings.Repeat("cd", 32)),
		NowMs: int64Ptr(1_000),
	}); err != nil {
		t.Fatalf("acquire confirmation lease: %v", err)
	}
	// CompleteCanary 显式提供 evidenceScopeKey（结果允许状态不匹配，只要求不报错）。
	if _, err := store.CompleteCanary(ctx, CompleteCanaryInput{
		Scope: scope, Generation: 1, DispatchRevision: "rev-1", TransitionID: "w14m-t3",
		LeaseID: "w14m-lease-2", Outcome: OutcomeUnknown,
		EvidenceScopeKey: strPtr("account:w14m-evidence"), NowMs: int64Ptr(1_000),
	}); err != nil {
		t.Fatalf("complete canary: %v", err)
	}
}

func TestW14MRedisRecordEvidenceValidationArms(t *testing.T) {
	base := w14mEvalReply{}
	stub := &w14mRedisStub{evals: []w14mEvalReply{base}}
	store := w14mRedisStore(t, stub)
	valid := ProtocolModelOpenEvidenceInput{
		Scope: w14mAccountScope("val"), DispatchRevision: "rev-1", EvidenceID: "ev-1",
		AccountTransitionID: "tr-1", Reason: "r", ConfirmedFailureCount: 1, WindowMs: 1_000,
		MaxProtocolScopes: 8,
	}
	// distinctScopeThreshold 不能超过 maxProtocolScopes。
	tooWide := valid
	tooWide.DistinctScopeThreshold = 9
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), tooWide); err == nil {
		t.Fatal("threshold > max must fail")
	}
	// 缺 evidenceId / accountTransitionId / reason。
	for _, mutate := range []func(*ProtocolModelOpenEvidenceInput){
		func(in *ProtocolModelOpenEvidenceInput) { in.EvidenceID = "" },
		func(in *ProtocolModelOpenEvidenceInput) { in.AccountTransitionID = "" },
		func(in *ProtocolModelOpenEvidenceInput) { in.Reason = "" },
	} {
		invalid := valid
		mutate(&invalid)
		if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), invalid); err == nil {
			t.Fatalf("invalid input %+v must fail", invalid)
		}
	}
	// 升级脚本返回值不是字符串。
	badType := stub.evals[0]
	badType.value = int64(3)
	stub.evals = []w14mEvalReply{badType}
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), valid); err == nil {
		t.Fatal("non-string escalation reply must fail")
	}
	// 空字符串回复同样无效。
	empty := stub.evals[0]
	empty.value = ""
	stub.evals = []w14mEvalReply{empty}
	if _, err := store.RecordProtocolModelOpenEvidence(context.Background(), valid); err == nil {
		t.Fatal("empty escalation reply must fail")
	}
}

func TestW14MRedisClearEvidenceErrorArms(t *testing.T) {
	stub := &w14mRedisStub{}
	store := w14mRedisStore(t, stub)
	input := ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: "w14m-clear", DispatchRevision: "rev-1", EvidenceID: "ev-1",
	}
	stub.evals = []w14mEvalReply{{err: errors.New("w14m-eval-boom")}}
	if _, err := store.ClearAccountEscalationEvidence(context.Background(), input); err == nil {
		t.Fatal("eval error must surface")
	}
	// 非数值回复触发 numericRedisResult 错误臂。
	stub.evals = []w14mEvalReply{{value: "not-a-number"}}
	if _, err := store.ClearAccountEscalationEvidence(context.Background(), input); err == nil {
		t.Fatal("non-numeric reply must fail")
	}
}

func TestW14MRedisListDueScanChunkClamp(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{
		RedisURL: "redis://" + server.Addr(), Capacity: 10, Now: func() int64 { return 1_000 },
	})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	// limit=300 使 scanChunkSize 触发 >512 收敛臂；空库直接耗尽返回。
	states, err := store.ListDue(context.Background(), 1_000, 300)
	if err != nil || len(states) != 0 {
		t.Fatalf("list due = (%d, %v)", len(states), err)
	}
}

func w14mListPage(scopeKeys ...string) w14mEvalReply {
	keys := make([]any, len(scopeKeys))
	for i, key := range scopeKeys {
		keys[i] = key
	}
	return w14mEvalReply{value: `{"scopeKeys":[` + joinQuoted(scopeKeys) + `],"scanned":1,"nextOffset":0,"exhausted":true}`}
}

func joinQuoted(keys []string) string {
	quoted := make([]string, len(keys))
	for i, key := range keys {
		quoted[i] = `"` + key + `"`
	}
	return strings.Join(quoted, ",")
}

func w14mSuspectStateJSON(runtimeKey string) string {
	state := map[string]any{
		"scopeKey":   "account:" + runtimeKey,
		"scope":      map[string]any{"kind": "account", "accountRuntimeKey": runtimeKey},
		"phase":      PhaseSuspect,
		"retryAtMs":  100,
		"updatedAtMs": 100,
	}
	encoded, _ := json.Marshal(map[string]any{
		"status": MutationApplied,
		"state":  state,
	})
	return string(encoded)
}

func TestW14MRedisListDueHGetAndErrorArms(t *testing.T) {
	// 场景一：due 索引中的键在 states hash 缺失（redis.Nil）→ 跳过。
	stub := &w14mRedisStub{
		evals: []w14mEvalReply{w14mListPage("account:w14m-vanished")},
		hgets: []w14mEvalReply{{err: redis.Nil}},
	}
	store := w14mRedisStore(t, stub)
	states, err := store.ListDue(context.Background(), 1_000, 5)
	if err != nil || len(states) != 0 {
		t.Fatalf("nil hget list = (%d, %v)", len(states), err)
	}

	// 场景二：Get（第二次 Eval）失败 → 透传错误。
	stub2 := &w14mRedisStub{
		evals: []w14mEvalReply{
			w14mListPage("account:w14m-broken"),
			{err: errors.New("w14m-get-boom")},
		},
		hgets: []w14mEvalReply{{value: w14mSuspectStateJSON("w14m-broken")}},
	}
	store2 := w14mRedisStore(t, stub2)
	if _, err := store2.ListDue(context.Background(), 1_000, 5); err == nil {
		t.Fatal("get error must surface")
	}

	// 场景三：第一个到期状态即达 limit → break。
	stub3 := &w14mRedisStub{
		evals: []w14mEvalReply{
			w14mListPage("account:w14m-due-1", "account:w14m-due-2"),
			{value: w14mSuspectStateJSON("w14m-due-1")},
			{value: w14mSuspectStateJSON("w14m-due-2")},
		},
		hgets: []w14mEvalReply{
			{value: w14mSuspectStateJSON("w14m-due-1")},
			{value: w14mSuspectStateJSON("w14m-due-2")},
		},
	}
	store3 := w14mRedisStore(t, stub3)
	states3, err := store3.ListDue(context.Background(), 1_000, 1)
	if err != nil || len(states3) != 1 {
		t.Fatalf("limit break list = (%d, %v)", len(states3), err)
	}
}

func TestW14MRedisSizeLoopTail(t *testing.T) {
	stub := &w14mRedisStub{
		hlens:  []w14mEvalReply{{value: int64(0)}},
		zcount: w14mEvalReply{value: int64(512)},
		evals:  []w14mEvalReply{{value: `{"size":7,"processed":256}`}},
	}
	store := w14mRedisStore(t, stub)
	size, err := store.Size(context.Background())
	if err != nil || size != 7 {
		t.Fatalf("size = (%d, %v)", size, err)
	}
}

func TestW14MRedisRestoreInvalidStatus(t *testing.T) {
	stub := &w14mRedisStub{evals: []w14mEvalReply{{value: `{"status":""}`}}}
	store := w14mRedisStore(t, stub)
	state := ClosedState(w14mAccountScope("restore"), "rev-1", 1, "w14m-t1", 1_000)
	if _, err := store.Restore(context.Background(), state, int64Ptr(1_000)); err == nil {
		t.Fatal("restore with empty status must fail")
	}
}

func TestW14MRedisReplaceRevisionPaginationArms(t *testing.T) {
	input := ReplaceAccountDispatchRevisionInput{
		AccountRuntimeKey: "w14m-rev", DispatchRevision: "rev-2", TransitionID: "w14m-t1",
	}
	// 场景一：HLen 直接失败。
	stub := &w14mRedisStub{hlens: []w14mEvalReply{{err: errors.New("w14m-hlen-boom")}}}
	store := w14mRedisStore(t, stub)
	if _, err := store.ReplaceAccountDispatchRevision(context.Background(), input); err == nil {
		t.Fatal("first hlen error must surface")
	}
	// 场景二：第二个 HLen（escalation）失败。
	stub2 := &w14mRedisStub{hlens: []w14mEvalReply{{value: int64(0)}, {err: errors.New("w14m-hlen2-boom")}}}
	store2 := w14mRedisStore(t, stub2)
	if _, err := store2.ReplaceAccountDispatchRevision(context.Background(), input); err == nil {
		t.Fatal("second hlen error must surface")
	}
	// 场景三：计数放大 maxPages 且游标不前进 → 分页未能收敛。
	stub3 := &w14mRedisStub{
		hlens: []w14mEvalReply{{value: int64(5)}, {value: int64(2)}},
		evals: []w14mEvalReply{{value: `{"statesCursor":"0","evidenceCursor":"0","changed":1}`}},
	}
	store3 := w14mRedisStore(t, stub3)
	if _, err := store3.ReplaceAccountDispatchRevision(context.Background(), input); err == nil {
		t.Fatal("non-converging pagination must fail")
	}
	// 场景四：分页回复不是 JSON。
	stub4 := &w14mRedisStub{
		hlens: []w14mEvalReply{{value: int64(0)}, {value: int64(0)}},
		evals: []w14mEvalReply{{value: "not-json"}},
	}
	store4 := w14mRedisStore(t, stub4)
	if _, err := store4.ReplaceAccountDispatchRevision(context.Background(), input); err == nil {
		t.Fatal("invalid page json must fail")
	}
}

func TestW14MRedisCursorStringAndDecodeStrictHelpers(t *testing.T) {
	if got := cursorString(json.Number("42"), "done"); got != "42" {
		t.Fatalf("json.Number cursor = %q", got)
	}
	if got := cursorString(float64(7), "done"); got != "7" {
		t.Fatalf("float cursor = %q", got)
	}
	if got := cursorString("", "done"); got != "done" {
		t.Fatalf("empty cursor = %q", got)
	}
	var dst map[string]any
	if err := decodeStrict(`{"a":1} trailing-garbage`, &dst); err == nil {
		t.Fatal("trailing garbage must fail decodeStrict")
	}
	if err := decodeStrict(`{"a":1}`, &dst); err != nil {
		t.Fatalf("clean decode = %v", err)
	}
}

func TestW14MValidateOperationPayloadArms(t *testing.T) {
	// complete_confirmation 缺 leaseId。
	err := validateOperationPayload("complete_confirmation", map[string]any{"transitionId": "w14m-t1"})
	if err == nil {
		t.Fatal("missing leaseId must fail")
	}
	// acquire_confirmation 携带非法 confirmationEvidenceKey。
	err = validateOperationPayload("acquire_confirmation", map[string]any{
		"transitionId": "w14m-t1", "leaseId": "w14m-lease", "leaseUntilMs": int64(2_000), "nowMs": int64(1_000),
		"confirmationEvidenceKey": "",
	})
	if err == nil {
		t.Fatal("invalid confirmationEvidenceKey must fail")
	}
}

package speedfirstrepo

// 速度运行态 v2 命名空间（调度偏好身份）jobs 侧同构测试：stateKeyFor 双版
// 本（含 sanitize）、v1 历史键零字节漂移、candidate-match 围栏跨代隔离、双
// 代候选扫描与派生键版本分离。契约见 docs/functions/接口契约与权限矩阵.md
// 「速度运行态命名空间与查询契约」。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

func wspProbeAccountRef(candidate opsjobs.ProbeCandidate) opsjobs.ProbeAccountRef {
	return opsjobs.ProbeAccountRef{AccountID: candidate.AccountID, GroupID: candidate.Scope.GroupID}
}

// 键构造双版本：v1 五段逐字节等值于历史拼接；v2 七段插入偏好身份两段；
// sanitizeKeyPart 规则两代一致；版本判定只看 PreferenceID。
func TestWspSpeedFirstStateKeyVersions(t *testing.T) {
	v1Scope := speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1"}
	if got := stateKeyFor(v1Scope, "acc-1"); got != "v1:sys-1:strategy-1:group-1:acc-1" {
		t.Fatalf("v1 key 历史形状漂移: %q", got)
	}
	v2Scope := speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1", PreferenceID: "pref-1", ConfigFingerprint: "fp1234567890ab"}
	if got := stateKeyFor(v2Scope, "acc-1"); got != "v2:sys-1:strategy-1:pref-1:fp1234567890ab:group-1:acc-1" {
		t.Fatalf("v2 key = %q", got)
	}
	odd := speedFirstScope{SystemAccountID: "sy s", RouteStrategyID: "rs/1", GroupID: "", PreferenceID: "p 1", ConfigFingerprint: ""}
	if got := stateKeyFor(odd, ""); got != "v2:sy_s:rs_1:p_1:_:_:_" {
		t.Fatalf("v2 sanitize key = %q", got)
	}
	fingerprintOnly := speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1", ConfigFingerprint: "fp1234567890ab"}
	if got := stateKeyFor(fingerprintOnly, "acc-1"); got != "v1:sys-1:strategy-1:group-1:acc-1" {
		t.Fatalf("空 PreferenceID 必须保持 v1 键: %q", got)
	}
}

// v1 派生键零字节漂移 + v2 按版本段同构；mutation-lock / probe-claim 从
// stateKey 版本段定位键族。
func TestWspSpeedFirstDerivedKeys(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	if store.generationKey() != "v1:generation" || store.allIndexKey() != "v1:all-index" || store.probeIndexKey() != "v1:probe-index" {
		t.Fatal("v1 派生键漂移")
	}
	if store.generationKeyFor(speedFirstStateVersionV2) != "v2:generation" ||
		store.allIndexKeyFor(speedFirstStateVersionV2) != "v2:all-index" ||
		store.probeIndexKeyFor(speedFirstStateVersionV2) != "v2:probe-index" {
		t.Fatal("v2 派生键形状错误")
	}
	v2Key := stateKeyFor(speedFirstScope{SystemAccountID: "s", RouteStrategyID: "r", GroupID: "g", PreferenceID: "p", ConfigFingerprint: "f"}, "a")
	if store.mutationLockKey(v2Key) != "v2:mutation-lock:"+v2Key {
		t.Fatalf("v2 mutation lock = %q", store.mutationLockKey(v2Key))
	}
	candidate := opsjobs.ProbeCandidate{StateKey: v2Key, Generation: "gen-1"}
	if store.probeClaimLockKey(candidate) != "v2:probe-claim:gen-1:"+v2Key {
		t.Fatalf("v2 probe claim = %q", store.probeClaimLockKey(candidate))
	}
}

// 双代候选扫描：v1 与 v2 probe-index 中的降级 state 都进入候选且携带各自
// 身份；generation marker 按版本分离（v2 独立代际不阻塞 v1 候选）。
func TestWspSpeedFirstListProbeCandidatesBothGenerations(t *testing.T) {
	store, server := newWFSpeedFirstStore(t)
	ctx := context.Background()

	v1Generation, err := store.LoadGenerationForVersion(ctx, speedFirstStateVersion)
	if err != nil {
		t.Fatal(err)
	}
	v2Generation, err := store.LoadGenerationForVersion(ctx, speedFirstStateVersionV2)
	if err != nil {
		t.Fatal(err)
	}
	// 两代 generation marker 键分离（初始 token 相同是预期：同一初始事件，
	// 但 marker 读写各落在自己的版本键上，互不连带失效）。
	if !server.Exists(store.redisKey(store.generationKeyFor(speedFirstStateVersion))) ||
		!server.Exists(store.redisKey(store.generationKeyFor(speedFirstStateVersionV2))) {
		t.Fatal("两代 generation marker 键必须独立存在")
	}

	v1State := wfSpeedFirstState(v1Generation)
	nextProbeAt := wfSpeedFirstBase.Add(-time.Second).UnixMilli()
	v1State.NextProbeAtMS = &nextProbeAt
	v1Key := stateKeyFor(v1State.Scope, v1State.RuntimeKey)
	v2State := wfSpeedFirstState(v2Generation)
	v2State.Scope = speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1", PreferenceID: "pref-1", ConfigFingerprint: "fp1234567890ab"}
	v2State.RuntimeKey = "acc-2"
	v2State.AccountID = "acc-2"
	v2State.NextProbeAtMS = &nextProbeAt
	v2Key := stateKeyFor(v2State.Scope, v2State.RuntimeKey)
	if !strings.HasPrefix(v2Key, "v2:") {
		t.Fatalf("v2 state 键必须落 v2 键族: %q", v2Key)
	}

	store.setJSON(ctx, v1Key, v1State, time.Hour)
	store.setJSON(ctx, v2Key, v2State, time.Hour)
	wfSpeedFirstSeedIndex(t, store, store.probeIndexKeyFor(speedFirstStateVersion), []string{v1Key})
	wfSpeedFirstSeedIndex(t, store, store.probeIndexKeyFor(speedFirstStateVersionV2), []string{v2Key})

	candidates, err := store.ListProbeCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("两代候选必须并存（candidates=%d）", len(candidates))
	}
	foundV2 := false
	for _, candidate := range candidates {
		if candidate.Scope.PreferenceID == "pref-1" {
			foundV2 = true
			if candidate.Scope.ConfigFingerprint != "fp1234567890ab" || candidate.StateKey != v2Key {
				t.Fatalf("v2 候选身份或键错误: %+v", candidate)
			}
		}
	}
	if !foundV2 {
		t.Fatal("v2 候选缺失")
	}
}

// candidate-match 围栏跨代隔离：身份字段参与匹配——旧身份迟到候选不能更新
// 新身份 state；v1/v2 互配不成立；真实 store 写路径（RecordSuccess）对身份
// 不匹配的候选保持 no-op。
func TestWspSpeedFirstCandidateFenceAcrossGenerations(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()

	v2Generation, err := store.LoadGenerationForVersion(ctx, speedFirstStateVersionV2)
	if err != nil {
		t.Fatal(err)
	}
	state := wfSpeedFirstState(v2Generation)
	state.Scope = speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1", PreferenceID: "pref-1", ConfigFingerprint: "fp1234567890ab"}
	key := stateKeyFor(state.Scope, state.RuntimeKey)
	store.setJSON(ctx, key, state, time.Hour)
	wfSpeedFirstSeedIndex(t, store, store.probeIndexKeyFor(speedFirstStateVersionV2), []string{key})

	// 同身份候选：正常恢复窗口推进。
	matched := state.candidate()
	matched.Generation = v2Generation
	firstByte := int64(1_000)
	result, err := store.RecordSuccess(ctx, matched, wspProbeAccountRef(matched), &firstByte)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cleared {
		t.Fatal("首轮探针成功不应清状态")
	}

	// 指纹轮换后的迟到候选（旧身份）不得更新新身份 state。
	rotated := state
	rotated.Scope = speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1", PreferenceID: "pref-1", ConfigFingerprint: "fpffffffffffff"}
	rotatedCandidate := rotated.candidate()
	rotatedCandidate.Generation = v2Generation
	rotatedCandidate.StateKey = key
	if candidateMatchesState(rotatedCandidate, &state) {
		t.Fatal("指纹轮换后的候选不得匹配旧身份 state")
	}

	// v1 身份候选不得匹配 v2 state。
	v1State := wfSpeedFirstState(v2Generation)
	v1Candidate := v1State.candidate()
	v1Candidate.Generation = v2Generation
	v1Candidate.StateKey = key
	if candidateMatchesState(v1Candidate, &state) {
		t.Fatal("v1 身份候选不得匹配 v2 state")
	}

	// 围栏兜底写入路径：Discard 用不匹配候选必须保持 no-op（state 仍在）。
	if err := store.Discard(ctx, rotatedCandidate); err != nil {
		t.Fatal(err)
	}
	var current speedFirstState
	found, err := store.getJSON(ctx, key, &current)
	if err != nil || !found {
		t.Fatalf("不匹配候选的 Discard 必须保持 state 不变（found=%v err=%v）", found, err)
	}
}

// mutation-lock 与 probe-claim 键按版本分离：v2 候选的 claim 落 v2 键族，
// 与 v1 同内容候选互不冲突。
func TestWspSpeedFirstClaimKeysByVersion(t *testing.T) {
	store, server := newWFSpeedFirstStore(t)
	ctx := context.Background()

	v2State := wfSpeedFirstState("gen")
	v2State.Scope = speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1", PreferenceID: "pref-1", ConfigFingerprint: "fp1234567890ab"}
	v2Candidate := v2State.candidate()
	v2Candidate.StateKey = stateKeyFor(v2State.Scope, v2State.RuntimeKey)

	claim, err := store.AcquireClaim(ctx, v2Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatal("v2 claim 领用失败")
	}
	wantClaimKey := "v2:probe-claim:" + v2Candidate.Generation + ":" + v2Candidate.StateKey
	if !server.Exists(store.redisKey(wantClaimKey)) {
		t.Fatalf("claim 必须落 v2 键族（缺 %s）", wantClaimKey)
	}

	// v1 同内容候选（不同键族）仍可独立领用，不与 v2 冲突。
	v1State := wfSpeedFirstState("gen")
	v1Candidate := v1State.candidate()
	v1Candidate.StateKey = stateKeyFor(v1State.Scope, v1State.RuntimeKey)
	v1Claim, err := store.AcquireClaim(ctx, v1Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if v1Claim == nil {
		t.Fatal("v1 claim 必须与 v2 互不冲突")
	}
	if err := store.ReleaseClaim(ctx, *claim); err != nil {
		t.Fatal(err)
	}
}

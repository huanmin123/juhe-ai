package speedfirstrepo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// 本文件覆盖 SpeedFirstStore 的 Redis 运行态契约：generation 标记、claim/锁
// CAS Lua 语义、Discard/Defer/RecordSuccess/RecordFailure 状态机、候选扫描
// 与索引维护。全部使用 miniredis 内存实例 + 固定时钟/随机函数，保证确定性
// 与可重放；不依赖真实 Redis。
//
// w12f 修复备注：addIndexKey/filterIndexKeys 曾把索引键本身作为 SetNX 锁键，
// CAS 写入用索引 JSON 覆盖锁值，导致同一索引 TTL 内的第二次写入必然以
// "索引锁获取失败"告终。现锁键已改为 indexLockKey(indexKey+"-lock")，
// 同一索引可重复写入；原"行为存疑"断言同步更新为第二次写入成功。

// wfSpeedFirstBase 是测试固定的当前时间（毫秒精度，UTC）。
var wfSpeedFirstBase = time.UnixMilli(1_700_000_000_000).UTC()

// wfSpeedFirstDelay 是 random 固定 0.5 时 passiveOffsetApply(5000) 的结果：
// window=2500，sampled=int64(0.5*5001)-2500=0 → offset 强制为 1。
const wfSpeedFirstDelay = int64(5001)

// newWFSpeedFirstStore 启动独立 miniredis 并打开固定时钟/随机的 Store。
func newWFSpeedFirstStore(t *testing.T) (*SpeedFirstStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := OpenSpeedFirstStore(SpeedFirstRedisConfig{
		Enabled: true, URL: "redis://" + server.Addr(), Namespace: "wf-test",
	}, nil)
	if err != nil {
		t.Fatalf("打开测试 Store 失败: %v", err)
	}
	store.now = func() time.Time { return wfSpeedFirstBase }
	store.random = func() float64 { return 0.5 }
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭 Store 失败: %v", err)
		}
	})
	return store, server
}

// wfSpeedFirstState 构造一个通过 valid() 校验的降级状态。
func wfSpeedFirstState(generation string) speedFirstState {
	degradedUntil := wfSpeedFirstBase.Add(10 * time.Minute).UnixMilli()
	return speedFirstState{
		Generation:      generation,
		AccountID:       "acc-1",
		AccountName:     "账户一",
		RuntimeKey:      "acc-1",
		Scope:           speedFirstScope{SystemAccountID: "sys-1", RouteStrategyID: "strategy-1", GroupID: "group-1"},
		Config:          speedFirstConfig{SlowTriggerCount: 3, SlowWindowSeconds: 120, RecoverySuccessCount: 2, ProbeIntervalSeconds: 30, DegradedTTLSeconds: 600, MaxFirstByteRetriesPerReq: 1, FirstByteDeadlineMS: 2000},
		FirstSlowAtMS:   wfSpeedFirstBase.Add(-time.Hour).UnixMilli(),
		LastSlowAtMS:    wfSpeedFirstBase.Add(-time.Minute).UnixMilli(),
		SlowCount:       3,
		DegradedUntilMS: &degradedUntil,
		Reason:          "普通路由速度优先降级",
	}
}

// wfSpeedFirstCandidate 生成与 state 逐字段匹配的候选（candidate-match 围栏）。
func wfSpeedFirstCandidate(t *testing.T, store *SpeedFirstStore, state *speedFirstState) opsjobs.ProbeCandidate {
	t.Helper()
	candidate := state.candidate()
	candidate.Generation = state.Generation
	if candidate.Generation == "" {
		generation, err := store.LoadGeneration(context.Background())
		if err != nil {
			t.Fatalf("LoadGeneration 失败: %v", err)
		}
		candidate.Generation = generation
		state.Generation = generation
	}
	return candidate
}

// wfSpeedFirstSeed 只写状态 JSON，不登记索引（绕开索引锁语义）。
func wfSpeedFirstSeed(t *testing.T, store *SpeedFirstStore, state speedFirstState) string {
	t.Helper()
	key := stateKeyFor(state.Scope, state.RuntimeKey)
	if err := store.setJSON(context.Background(), key, state, time.Hour); err != nil {
		t.Fatalf("写入测试状态失败: %v", err)
	}
	return key
}

// wfSpeedFirstSeedIndex 直接写索引 JSON。
func wfSpeedFirstSeedIndex(t *testing.T, store *SpeedFirstStore, indexKey string, keys []string) {
	t.Helper()
	if err := store.setJSON(context.Background(), indexKey, map[string]any{"keys": keys}, time.Hour); err != nil {
		t.Fatalf("写入测试索引失败: %v", err)
	}
}

// wfSpeedFirstEnsureGeneration 初始化 generation 标记并回填到状态。
func wfSpeedFirstEnsureGeneration(t *testing.T, store *SpeedFirstStore, state *speedFirstState) {
	t.Helper()
	generation, err := store.LoadGeneration(context.Background())
	if err != nil {
		t.Fatalf("初始化 generation 失败: %v", err)
	}
	state.Generation = generation
}

func TestWFSpeedFirstOpenAndClose(t *testing.T) {
	if _, err := OpenSpeedFirstStore(SpeedFirstRedisConfig{}, nil); err == nil {
		t.Fatal("未启用配置必须拒绝打开")
	}
	if _, err := OpenSpeedFirstStore(SpeedFirstRedisConfig{Enabled: true, URL: "://bad-url", Namespace: "wf"}, nil); err == nil {
		t.Fatal("非法 Redis URL 必须报错")
	}
	server := miniredis.RunT(t)
	t.Cleanup(server.Close)
	store, err := OpenSpeedFirstStore(SpeedFirstRedisConfig{Enabled: true, URL: "redis://" + server.Addr(), Namespace: "wf"}, nil)
	if err != nil {
		t.Fatalf("合法配置必须能打开: %v", err)
	}
	if store.now().IsZero() {
		t.Fatal("now 为 nil 时必须回退到默认时钟")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	// nil 接收者与空 client 的 Close 必须是安全 no-op。
	var empty *SpeedFirstStore
	if err := empty.Close(); err != nil {
		t.Fatalf("nil Store Close 失败: %v", err)
	}
	if err := (&SpeedFirstStore{}).Close(); err != nil {
		t.Fatalf("空 Store Close 失败: %v", err)
	}
}

func TestWFSpeedFirstRandomAndFormatHelpers(t *testing.T) {
	value, err := randomInt63()
	if err != nil {
		t.Fatalf("randomInt63 失败: %v", err)
	}
	if value < 0 {
		t.Fatalf("randomInt63=%d 必须非负", value)
	}
	sample := defaultRandom()
	if sample < 0 || sample >= 1 {
		t.Fatalf("defaultRandom=%v 超出 [0,1)", sample)
	}
	token := randomToken(16)
	if len(token) != 16 {
		t.Fatalf("randomToken 长度=%d", len(token))
	}
	for _, char := range token {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", char) {
			t.Fatalf("randomToken 出现非法字符 %q", char)
		}
	}
	if boxed := intPtr(7); boxed == nil || *boxed != 7 {
		t.Fatal("intPtr 装箱错误")
	}
	if max64(2, 3) != 3 || max64(4, 1) != 4 || maxInt(1, 2) != 2 || maxInt(5, 3) != 5 {
		t.Fatal("max64/maxInt 比较错误")
	}
	if got := formatMillis(0); got != "1970-01-01T00:00:00.000Z" {
		t.Fatalf("formatMillis(0)=%q", got)
	}
	store := &SpeedFirstStore{}
	if store.indexLockKey("v1:probe-index") != "v1:probe-index-lock" {
		t.Fatalf("indexLockKey=%q", store.indexLockKey("v1:probe-index"))
	}
}

func TestWFSpeedFirstPassiveJitterWindowTable(t *testing.T) {
	tests := []struct {
		name       string
		intervalMS int64
		want       int64
	}{
		{name: "小于1收敛为0", intervalMS: 0, want: 0},
		{name: "1毫秒窗口为0", intervalMS: 1, want: 0},
		{name: "常规间隔取一半", intervalMS: 5_000, want: 2_500},
		{name: "接近分钟上限30秒", intervalMS: 119_999, want: 30_000},
		{name: "分钟档30秒", intervalMS: 60_000, want: 30_000},
		{name: "小时档30分钟", intervalMS: 60 * 60_000, want: 30 * 60_000},
		{name: "日内档1小时", intervalMS: 24 * 60 * 60_000, want: 60 * 60_000},
		{name: "周档8小时", intervalMS: 7 * 24 * 60 * 60_000, want: 8 * 60 * 60_000},
		{name: "超长档8小时", intervalMS: 30 * 24 * 60 * 60_000, want: 8 * 60 * 60_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := passiveJitterWindowMS(tt.intervalMS); got != tt.want {
				t.Fatalf("passiveJitterWindowMS(%d)=%d want %d", tt.intervalMS, got, tt.want)
			}
		})
	}
}

func TestWFSpeedFirstPassiveOffsetApplyClamps(t *testing.T) {
	// 采样值被夹紧到 [0,1]，且延迟不允许小于 1ms。
	tests := []struct {
		name     string
		random   func() float64
		interval int64
		want     int64
	}{
		{name: "负采样按0处理", random: func() float64 { return -1 }, interval: 5_000, want: 2_500},
		{name: "超1采样按1处理", random: func() float64 { return 2 }, interval: 5_000, want: 7_500},
		{name: "窗口为0时offset为0", random: func() float64 { return 0.5 }, interval: 1, want: 1},
		{name: "间隔0时延迟至少1", random: func() float64 { return 0.5 }, interval: 0, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := passiveOffsetApply(tt.interval, tt.random); got != tt.want {
				t.Fatalf("passiveOffsetApply(%d)=%d want %d", tt.interval, got, tt.want)
			}
		})
	}
}

func TestWFSpeedFirstLoadConfigNilGetenv(t *testing.T) {
	if _, err := LoadSpeedFirstRedisConfig(nil); err == nil {
		t.Fatal("getenv 为 nil 必须 fail closed")
	}
}

func TestWFSpeedFirstSanitizeKeyPart(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "空白回退占位", value: "  ", want: "_"},
		{name: "非法字符逐字替换", value: "sys 1:策略.x", want: "sys_1:__.x"},
		{name: "合法字符原样保留", value: "a-b_c.d:e", want: "a-b_c.d:e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeKeyPart(tt.value); got != tt.want {
				t.Fatalf("sanitizeKeyPart(%q)=%q want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestWFSpeedFirstNormalizeGenerationToken(t *testing.T) {
	if _, err := normalizeGenerationEvent(speedFirstGenerationEvent{Version: "  ", PublishedAt: "1970-01-01T00:00:00Z"}); err == nil {
		t.Fatal("缺少 version 必须报错")
	}
	if _, err := normalizeGenerationEvent(speedFirstGenerationEvent{Version: "v1", PublishedAt: "not-a-time"}); err == nil {
		t.Fatal("publishedAt 非法必须报错")
	}
	normalized, err := normalizeGenerationEvent(speedFirstGenerationEvent{Version: " v1 ", PublishedAt: "1970-01-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("归一化失败: %v", err)
	}
	if normalized.Version != "v1" || normalized.PublishedAt != "1970-01-01T00:00:00.000Z" {
		t.Fatalf("归一化结果=%#v", normalized)
	}
	if _, err := generationToken(speedFirstGenerationEvent{Version: "v1", PublishedAt: "bad"}); err == nil {
		t.Fatal("generationToken 对非法时间必须报错")
	}
}

func TestWFSpeedFirstGenerationLifecycle(t *testing.T) {
	store, server := newWFSpeedFirstStore(t)
	ctx := context.Background()
	generationKey := store.redisKey(store.generationKey())
	first, err := store.LoadGeneration(ctx)
	if err != nil {
		t.Fatalf("首次 LoadGeneration 失败: %v", err)
	}
	if first != `[0,"initial"]` {
		t.Fatalf("初始 token=%q", first)
	}
	if !server.Exists(generationKey) {
		t.Fatal("初始 generation 标记必须落盘")
	}
	if ttl := server.TTL(generationKey); ttl <= 0 {
		t.Fatalf("generation TTL=%v 必须为正（48h 契约）", ttl)
	}
	second, err := store.LoadGeneration(ctx)
	if err != nil || second != first {
		t.Fatalf("二次读取 token=%q err=%v，必须与首次一致", second, err)
	}
	// 非 canonical 事件（毫秒缺失/带空白）读取时必须被 CAS 改写为 canonical。
	if err := server.Set(generationKey, `{"version":" initial ","publishedAt":"1970-01-01T00:00:00Z"}`); err != nil {
		t.Fatalf("seed 非 canonical 事件失败: %v", err)
	}
	token, err := store.LoadGeneration(ctx)
	if err != nil || token != first {
		t.Fatalf("canonical 改写后 token=%q err=%v", token, err)
	}
	if raw, _ := server.Get(generationKey); raw != `{"version":"initial","publishedAt":"1970-01-01T00:00:00.000Z"}` {
		t.Fatalf("canonical 改写结果=%s", raw)
	}
	// renewGeneration：token 匹配才续租。
	renewed, err := store.renewGeneration(ctx, first)
	if err != nil || !renewed {
		t.Fatalf("匹配 token 续租 renewed=%v err=%v", renewed, err)
	}
	renewed, err = store.renewGeneration(ctx, `[999,"other"]`)
	if err != nil || renewed {
		t.Fatalf("不匹配 token 续租 renewed=%v err=%v", renewed, err)
	}
	if !server.Del(generationKey) {
		t.Fatal("删除 generation 失败")
	}
	renewed, err = store.renewGeneration(ctx, first)
	if err != nil || renewed {
		t.Fatalf("缺失 generation 续租 renewed=%v err=%v（Node undefined 语义）", renewed, err)
	}
}

func TestWFSpeedFirstJSONPrimitiveCAS(t *testing.T) {
	store, server := newWFSpeedFirstStore(t)
	ctx := context.Background()
	key := "v1:unit-key"
	redisKey := store.redisKey(key)

	// compareSetJSON：expected=nil 仅在键不存在时创建（Lua CAS 语义）。
	created, err := store.compareSetJSON(ctx, key, nil, map[string]any{"v": 1}, time.Minute)
	if err != nil || !created {
		t.Fatalf("空键 CAS 创建 created=%v err=%v", created, err)
	}
	blocked, err := store.compareSetJSON(ctx, key, nil, map[string]any{"v": 2}, time.Minute)
	if err != nil || blocked {
		t.Fatalf("键已存在时空 expected 不得覆盖 blocked=%v err=%v", blocked, err)
	}
	updated, err := store.compareSetJSON(ctx, key, map[string]any{"v": 1}, map[string]any{"v": 2}, time.Minute)
	if err != nil || !updated {
		t.Fatalf("匹配 CAS 更新 updated=%v err=%v", updated, err)
	}
	missed, err := store.compareSetJSON(ctx, key, map[string]any{"v": 99}, map[string]any{"v": 3}, time.Minute)
	if err != nil || missed {
		t.Fatalf("不匹配 CAS 不得写入 missed=%v err=%v", missed, err)
	}
	// compareDeleteJSON：值匹配才删除。
	deleted, err := store.compareDeleteJSON(ctx, key, map[string]any{"v": 2})
	if err != nil || !deleted {
		t.Fatalf("匹配删除 deleted=%v err=%v", deleted, err)
	}
	if server.Exists(redisKey) {
		t.Fatal("匹配删除后键必须消失")
	}
	deleted, err = store.compareDeleteJSON(ctx, key, map[string]any{"v": 2})
	if err != nil || deleted {
		t.Fatalf("缺失键删除 deleted=%v err=%v", deleted, err)
	}

	// getJSON：损坏数据必须删除并按缺失处理（Node 损坏数据删除契约）。
	if err := server.Set(redisKey, "{not-json"); err != nil {
		t.Fatalf("写入损坏 JSON 失败: %v", err)
	}
	var target map[string]any
	found, err := store.getJSON(ctx, key, &target)
	if err != nil || found {
		t.Fatalf("损坏数据 found=%v err=%v", found, err)
	}
	if server.Exists(redisKey) {
		t.Fatal("损坏数据必须被删除")
	}
	found, err = store.getJSON(ctx, key, &target)
	if err != nil || found {
		t.Fatalf("缺失键 found=%v err=%v", found, err)
	}
	if err := store.setJSON(ctx, key, map[string]any{"ok": true}, time.Minute); err != nil {
		t.Fatalf("setJSON 失败: %v", err)
	}
	if ttl := server.TTL(redisKey); ttl <= 0 {
		t.Fatalf("setJSON TTL=%v 必须为正", ttl)
	}
}

func TestWFSpeedFirstLockPrimitives(t *testing.T) {
	store, server := newWFSpeedFirstStore(t)
	ctx := context.Background()
	key := "v1:lock-unit"
	redisKey := store.redisKey(key)

	acquired, err := store.acquireLock(ctx, key, "token-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("首次加锁 acquired=%v err=%v", acquired, err)
	}
	acquired, err = store.acquireLock(ctx, key, "token-b", time.Minute)
	if err != nil || acquired {
		t.Fatalf("重复加锁 acquired=%v err=%v", acquired, err)
	}
	renewed, err := store.renewLock(ctx, key, "token-a", time.Minute)
	if err != nil || !renewed {
		t.Fatalf("持有者续锁 renewed=%v err=%v", renewed, err)
	}
	renewed, err = store.renewLock(ctx, key, "token-b", time.Minute)
	if err != nil || renewed {
		t.Fatalf("非持有者续锁 renewed=%v err=%v", renewed, err)
	}
	if ttl := server.TTL(redisKey); ttl <= 0 {
		t.Fatalf("续锁后 TTL=%v 必须刷新", ttl)
	}
	if err := store.releaseLock(ctx, key, "token-b"); err != nil {
		t.Fatalf("非持有者释放不得报错: %v", err)
	}
	if !server.Exists(redisKey) {
		t.Fatal("非持有者释放不得删除锁")
	}
	if err := store.releaseLock(ctx, key, "token-a"); err != nil {
		t.Fatalf("持有者释放失败: %v", err)
	}
	if server.Exists(redisKey) {
		t.Fatal("持有者释放后锁必须删除")
	}
}

func TestWFSpeedFirstClaimLifecycle(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	candidate := wfSpeedFirstCandidate(t, store, &state)

	claim, err := store.AcquireClaim(ctx, candidate)
	if err != nil {
		t.Fatalf("AcquireClaim 失败: %v", err)
	}
	if claim == nil || claim.Token == "" || claim.Candidate.StateKey != candidate.StateKey {
		t.Fatalf("claim=%+v 必须携带 token 与候选", claim)
	}
	// 同一候选的第二个 claim 必须拿不到锁（互斥契约）。
	blocked, err := store.AcquireClaim(ctx, candidate)
	if err != nil || blocked != nil {
		t.Fatalf("重复 claim blocked=%v err=%v", blocked, err)
	}
	if !store.claimLockHeld(candidate) {
		t.Fatal("claim 成功后 claim 锁必须被持有")
	}
	ok, err := store.RenewClaim(ctx, *claim)
	if err != nil || !ok {
		t.Fatalf("RenewClaim ok=%v err=%v", ok, err)
	}
	wrongToken := *claim
	wrongToken.Token = "wrong-token"
	ok, err = store.RenewClaim(ctx, wrongToken)
	if err != nil || ok {
		t.Fatalf("错误 token 续租 ok=%v err=%v", ok, err)
	}
	if err := store.ReleaseClaim(ctx, wrongToken); err != nil {
		t.Fatalf("错误 token 释放不得报错: %v", err)
	}
	if !store.claimLockHeld(candidate) {
		t.Fatal("错误 token 释放不得删除 claim 锁")
	}
	if err := store.ReleaseClaim(ctx, *claim); err != nil {
		t.Fatalf("ReleaseClaim 失败: %v", err)
	}
	if store.claimLockHeld(candidate) {
		t.Fatal("持有者释放后 claim 锁必须删除")
	}
	again, err := store.AcquireClaim(ctx, candidate)
	if err != nil || again == nil {
		t.Fatalf("释放后必须可重新 claim: %v", err)
	}
}

// claimLockHeld 观察 claim 锁是否仍被持有（锁键形状契约）。
func (s *SpeedFirstStore) claimLockHeld(candidate opsjobs.ProbeCandidate) bool {
	count, err := s.client.Exists(context.Background(), s.redisKey(s.probeClaimLockKey(candidate))).Result()
	return err == nil && count == 1
}

func TestWFSpeedFirstDiscardMismatchNoOp(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 候选与状态不匹配（nextProbeAt 已变化）时不得删除（围栏契约）。
	stale := candidate
	future := wfSpeedFirstBase.Add(time.Second).UnixMilli()
	state.NextProbeAtMS = &future
	wfSpeedFirstSeed(t, store, state)
	if err := store.Discard(ctx, stale); err != nil {
		t.Fatalf("Discard 不匹配候选失败: %v", err)
	}
	if found, err := store.loadState(ctx, key, state.Generation); err != nil || found == nil {
		t.Fatalf("不匹配候选不得删除状态 found=%v err=%v", found, err)
	}
}

func TestWFSpeedFirstDiscardDeletesMatchedState(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	if err := store.Discard(ctx, candidate); err != nil {
		t.Fatalf("Discard 失败: %v", err)
	}
	if found, err := store.loadState(ctx, key, state.Generation); err != nil || found != nil {
		t.Fatalf("匹配候选删除后状态必须消失 found=%v err=%v", found, err)
	}
	for _, indexKey := range []string{store.probeIndexKey(), store.allIndexKey()} {
		keys, err := store.loadIndexKeys(ctx, indexKey)
		if err != nil {
			t.Fatalf("读取索引 %s 失败: %v", indexKey, err)
		}
		for _, existing := range keys {
			if existing == key {
				t.Fatalf("索引 %s 仍包含已删除状态 %s", indexKey, key)
			}
		}
	}
}

func TestWFSpeedFirstDiscardLoadsGenerationWhenBlank(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)
	candidate.Generation = ""
	if err := store.Discard(ctx, candidate); err != nil {
		t.Fatalf("空 generation Discard 失败: %v", err)
	}
	if found, _ := store.loadState(ctx, key, state.Generation); found != nil {
		t.Fatal("空 generation 路径同样必须删除匹配状态")
	}
}

func TestWFSpeedFirstDeferAppliesWhileDegraded(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 仍在降级期内：保留降级并顺延下一轮探针，重置双探针窗口计数。
	applied, err := store.Defer(ctx, candidate)
	if err != nil || !applied {
		t.Fatalf("Defer applied=%v err=%v", applied, err)
	}
	deferred, err := store.loadState(ctx, key, state.Generation)
	if err != nil || deferred == nil {
		t.Fatalf("Defer 后状态必须保留 found=%v err=%v", deferred, err)
	}
	if deferred.roundAttempts() != 0 || deferred.roundSuccesses() != 0 {
		t.Fatalf("Defer 后窗口计数 attempts=%d successes=%d 必须归零", deferred.roundAttempts(), deferred.roundSuccesses())
	}
	if want := wfSpeedFirstBase.UnixMilli() + wfSpeedFirstDelay; deferred.NextProbeAtMS == nil || *deferred.NextProbeAtMS != want {
		t.Fatalf("Defer 后 nextProbeAt=%v want %d", deferred.NextProbeAtMS, want)
	}
	if keys, err := store.loadIndexKeys(ctx, store.probeIndexKey()); err != nil || len(keys) != 1 || keys[0] != key {
		t.Fatalf("Defer 后 probe-index=%v err=%v 必须包含候选", keys, err)
	}
}

func TestWFSpeedFirstDeferExpiredDeletes(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	state.DegradedUntilMS = int64Ptr(wfSpeedFirstBase.Add(-time.Second).UnixMilli())
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 降级已到期：删除而不是顺延。
	applied, err := store.Defer(ctx, candidate)
	if err != nil || applied {
		t.Fatalf("到期 Defer applied=%v err=%v", applied, err)
	}
	if found, _ := store.loadState(ctx, key, state.Generation); found != nil {
		t.Fatal("到期 Defer 必须删除状态")
	}
}

func TestWFSpeedFirstDeferMismatchAndMissingNoOp(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 候选不匹配：无动作。
	mismatch := candidate
	mismatch.DegradationEventID = "event-other"
	applied, err := store.Defer(ctx, mismatch)
	if err != nil || applied {
		t.Fatalf("不匹配 Defer applied=%v err=%v", applied, err)
	}
	if found, _ := store.loadState(ctx, key, state.Generation); found == nil {
		t.Fatal("不匹配候选不得删除状态")
	}

	// 状态缺失：无动作且不报错。
	missing := candidate
	missing.StateKey = "v1:missing:state:key"
	applied, err = store.Defer(ctx, missing)
	if err != nil || applied {
		t.Fatalf("缺失状态 Defer applied=%v err=%v", applied, err)
	}
}

func TestWFSpeedFirstRecordSuccessClearsExpired(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	ref := opsjobs.ProbeAccountRef{AccountID: "acc-1"}
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	state.DegradedUntilMS = int64Ptr(wfSpeedFirstBase.Add(-time.Second).UnixMilli())
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 到期降级：成功探测直接清除。
	result, err := store.RecordSuccess(ctx, candidate, ref, nil)
	if err != nil {
		t.Fatalf("到期 RecordSuccess 失败: %v", err)
	}
	if !result.Cleared || result.RecoverySuccessCount != 0 || result.RequiredRecoverySuccessCount != 2 {
		t.Fatalf("到期结果=%+v", result)
	}
	if found, _ := store.loadState(ctx, key, state.Generation); found != nil {
		t.Fatal("到期成功必须删除状态")
	}
}

func TestWFSpeedFirstRecordSuccessFirstRound(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	ref := opsjobs.ProbeAccountRef{AccountID: "acc-1"}
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 双探针窗口：第一次成功累计且不恢复。
	result, err := store.RecordSuccess(ctx, candidate, ref, nil)
	if err != nil {
		t.Fatalf("首轮 RecordSuccess 失败: %v", err)
	}
	if result.Cleared || result.RecoverySuccessCount != 1 {
		t.Fatalf("首轮结果=%+v 必须为未恢复的 1 次成功", result)
	}
	updated, _ := store.loadState(ctx, key, state.Generation)
	if updated == nil || updated.roundAttempts() != 1 || updated.roundSuccesses() != 1 {
		t.Fatalf("首轮状态=%+v 必须记录 1/1", updated)
	}
	if want := wfSpeedFirstBase.UnixMilli() + wfSpeedFirstDelay; updated.NextProbeAtMS == nil || *updated.NextProbeAtMS != want {
		t.Fatalf("首轮 nextProbeAt=%v want %d", updated.NextProbeAtMS, want)
	}
}

func TestWFSpeedFirstRecordSuccessCompletesRound(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	ref := opsjobs.ProbeAccountRef{AccountID: "acc-1"}
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	state.RecoveryProbeRoundAttemptCount = intPtr(1)
	state.RecoveryProbeRoundSuccessCount = intPtr(1)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 第二次成功（2/2）恢复并删除状态。
	result, err := store.RecordSuccess(ctx, candidate, ref, nil)
	if err != nil {
		t.Fatalf("轮末 RecordSuccess 失败: %v", err)
	}
	if !result.Cleared || result.RecoverySuccessCount != 2 {
		t.Fatalf("轮末结果=%+v 必须恢复", result)
	}
	if found, _ := store.loadState(ctx, key, state.Generation); found != nil {
		t.Fatal("恢复后状态必须删除")
	}
}

func TestWFSpeedFirstRecordSuccessResetsMixedWindow(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	ref := opsjobs.ProbeAccountRef{AccountID: "acc-1"}
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	state.RecoveryProbeRoundAttemptCount = intPtr(1)
	state.RecoveryProbeRoundSuccessCount = intPtr(0)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 窗口内失败过（1/0 → 2/1 不满足全成功）→ 窗口重置。
	result, err := store.RecordSuccess(ctx, candidate, ref, nil)
	if err != nil {
		t.Fatalf("混合窗口 RecordSuccess 失败: %v", err)
	}
	if result.Cleared || result.RecoverySuccessCount != 0 {
		t.Fatalf("混合窗口结果=%+v 必须重置且未恢复", result)
	}
	updated, _ := store.loadState(ctx, key, state.Generation)
	if updated == nil || updated.roundAttempts() != 0 || updated.roundSuccesses() != 0 {
		t.Fatalf("混合窗口状态=%+v 必须重置为 0/0", updated)
	}
}

func TestWFSpeedFirstRecordSuccessMismatchNoOp(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	ref := opsjobs.ProbeAccountRef{AccountID: "acc-1"}
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 账户引用不匹配：无动作。
	result, err := store.RecordSuccess(ctx, candidate, opsjobs.ProbeAccountRef{AccountID: "acc-other"}, nil)
	if err != nil {
		t.Fatalf("账户不匹配 RecordSuccess 失败: %v", err)
	}
	if result.Cleared || result.RecoverySuccessCount != 0 {
		t.Fatalf("账户不匹配结果=%+v 必须为空动作", result)
	}
	if found, _ := store.loadState(ctx, key, state.Generation); found == nil {
		t.Fatal("账户不匹配不得删除状态")
	}

	// 候选不匹配：无动作。
	stale := candidate
	stale.DegradationEventID = "event-other"
	result, err = store.RecordSuccess(ctx, stale, ref, nil)
	if err != nil || result.Cleared {
		t.Fatalf("候选不匹配结果=%+v err=%v", result, err)
	}
}

func TestWFSpeedFirstRecordFailureUpdates(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 窗口未完成：更新慢计数/原因/下一轮时间，attempts+1，successes 保留，降级期不变。
	wantUntil := *state.DegradedUntilMS
	if err := store.RecordFailure(ctx, candidate, "探针失败原因"); err != nil {
		t.Fatalf("RecordFailure 失败: %v", err)
	}
	updated, err := store.loadState(ctx, key, state.Generation)
	if err != nil || updated == nil {
		t.Fatalf("失败后状态必须存在 found=%v err=%v", updated, err)
	}
	if updated.LastSlowAtMS != wfSpeedFirstBase.UnixMilli() {
		t.Fatalf("LastSlowAt=%d want %d", updated.LastSlowAtMS, wfSpeedFirstBase.UnixMilli())
	}
	if updated.SlowCount != 3 {
		t.Fatalf("SlowCount=%d", updated.SlowCount)
	}
	if updated.roundAttempts() != 1 || updated.roundSuccesses() != 0 {
		t.Fatalf("窗口计数=%d/%d want 1/0", updated.roundAttempts(), updated.roundSuccesses())
	}
	if updated.DegradedUntilMS == nil || *updated.DegradedUntilMS != wantUntil {
		t.Fatalf("DegradedUntil=%v want %d", updated.DegradedUntilMS, wantUntil)
	}
	if updated.Reason != "探针失败原因" {
		t.Fatalf("Reason=%q", updated.Reason)
	}
}

func TestWFSpeedFirstRecordFailureExtendsCompletedRound(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	state.DegradedUntilMS = int64Ptr(wfSpeedFirstBase.Add(5 * time.Minute).UnixMilli())
	state.RecoveryProbeRoundAttemptCount = intPtr(1)
	state.RecoveryProbeRoundSuccessCount = intPtr(0)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	// 窗口完成且全失败（2/0）：延长降级期到 now+max(60,ttl)s。
	if err := store.RecordFailure(ctx, candidate, "再次失败"); err != nil {
		t.Fatalf("窗口完成 RecordFailure 失败: %v", err)
	}
	updated, _ := store.loadState(ctx, key, state.Generation)
	if updated == nil {
		t.Fatal("延长路径状态必须存在")
	}
	wantExtended := wfSpeedFirstBase.Add(600 * time.Second).UnixMilli()
	if updated.DegradedUntilMS == nil || *updated.DegradedUntilMS != wantExtended {
		t.Fatalf("延长后 DegradedUntil=%v want %d", updated.DegradedUntilMS, wantExtended)
	}
	if updated.roundAttempts() != 0 || updated.roundSuccesses() != 0 {
		t.Fatalf("窗口完成后计数=%d/%d 必须重置", updated.roundAttempts(), updated.roundSuccesses())
	}
}

func TestWFSpeedFirstRecordFailureExpiredDeletes(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	state.DegradedUntilMS = int64Ptr(wfSpeedFirstBase.Add(-time.Second).UnixMilli())
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	if err := store.RecordFailure(ctx, candidate, "到期"); err != nil {
		t.Fatalf("到期 RecordFailure 失败: %v", err)
	}
	if found, _ := store.loadState(ctx, key, state.Generation); found != nil {
		t.Fatal("到期失败必须删除状态")
	}
}

func TestWFSpeedFirstRecordFailureMismatchNoOp(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := wfSpeedFirstSeed(t, store, state)
	candidate := wfSpeedFirstCandidate(t, store, &state)

	mismatch := candidate
	mismatch.DegradationEventID = "event-other"
	if err := store.RecordFailure(ctx, mismatch, "不匹配"); err != nil {
		t.Fatalf("不匹配 RecordFailure 失败: %v", err)
	}
	updated, _ := store.loadState(ctx, key, state.Generation)
	if updated == nil || updated.Reason == "不匹配" {
		t.Fatalf("不匹配候选不得改写状态：%+v", updated)
	}
}

func TestWFSpeedFirstListProbeCandidates(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx := context.Background()

	// 空索引直接返回空。
	candidates, err := store.ListProbeCandidates(ctx, 10)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("空索引 candidates=%v err=%v", candidates, err)
	}

	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	dueFirst := state
	dueFirst.AccountID = "acc-b"
	dueFirst.RuntimeKey = "acc-b"
	dueFirst.NextProbeAtMS = int64Ptr(wfSpeedFirstBase.Add(-2 * time.Second).UnixMilli())
	dueFirstKey := wfSpeedFirstSeed(t, store, dueFirst)
	dueSecond := state
	dueSecond.AccountID = "acc-a"
	dueSecond.RuntimeKey = "acc-a"
	dueSecond.NextProbeAtMS = int64Ptr(wfSpeedFirstBase.Add(-time.Second).UnixMilli())
	dueSecondKey := wfSpeedFirstSeed(t, store, dueSecond)
	// 未到期：被过滤。
	future := state
	future.AccountID = "acc-future"
	future.RuntimeKey = "acc-future"
	future.NextProbeAtMS = int64Ptr(wfSpeedFirstBase.Add(time.Hour).UnixMilli())
	futureKey := wfSpeedFirstSeed(t, store, future)
	// 降级已过期：被过滤。
	expired := state
	expired.AccountID = "acc-expired"
	expired.RuntimeKey = "acc-expired"
	expired.DegradedUntilMS = int64Ptr(wfSpeedFirstBase.Add(-time.Second).UnixMilli())
	expiredKey := wfSpeedFirstSeed(t, store, expired)
	// generation 不匹配：被过滤。
	foreign := state
	foreign.AccountID = "acc-foreign"
	foreign.RuntimeKey = "acc-foreign"
	foreign.Generation = "gen-foreign"
	foreignKey := wfSpeedFirstSeed(t, store, foreign)
	wfSpeedFirstSeedIndex(t, store, store.probeIndexKey(), []string{dueFirstKey, dueSecondKey, futureKey, expiredKey, foreignKey})

	candidates, err = store.ListProbeCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("ListProbeCandidates 失败: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("候选数=%d want 2（未到期/过期/异代必须过滤）", len(candidates))
	}
	if candidates[0].AccountID != "acc-b" || candidates[1].AccountID != "acc-a" {
		t.Fatalf("排序=%s,%s 必须按 nextProbeAt 升序", candidates[0].AccountID, candidates[1].AccountID)
	}
	if candidates[0].StateKey != dueFirstKey || candidates[1].StateKey != dueSecondKey {
		t.Fatalf("StateKey=%s/%s 必须来自索引键", candidates[0].StateKey, candidates[1].StateKey)
	}
	if candidates[0].Config.FirstByteDeadlineMS != 2000 || candidates[0].RecoverySuccessCount != 0 {
		t.Fatalf("候选投影字段不完整：%+v", candidates[0])
	}

	// limit 截断与夹紧。
	limited, err := store.ListProbeCandidates(ctx, 1)
	if err != nil || len(limited) != 1 || limited[0].AccountID != "acc-b" {
		t.Fatalf("limit=1 结果=%v err=%v", limited, err)
	}
	for _, limit := range []int{0, -5, 501} {
		if _, err := store.ListProbeCandidates(ctx, limit); err != nil {
			t.Fatalf("limit=%d 必须被夹紧而不报错: %v", limit, err)
		}
	}
}

func TestWFSpeedFirstLoadStateRejectsInvalidOrForeign(t *testing.T) {
	store, server := newWFSpeedFirstStore(t)
	ctx := context.Background()
	state := wfSpeedFirstState("")
	wfSpeedFirstEnsureGeneration(t, store, &state)
	key := stateKeyFor(state.Scope, state.RuntimeKey)

	// 缺失键 → nil。
	if found, err := store.loadState(ctx, key, state.Generation); err != nil || found != nil {
		t.Fatalf("缺失键 found=%v err=%v", found, err)
	}
	// 无效状态（缺 scope）→ nil。
	invalid := state
	invalid.Scope = speedFirstScope{}
	raw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatalf("编码无效状态失败: %v", err)
	}
	if err := server.Set(store.redisKey(key), string(raw)); err != nil {
		t.Fatalf("写入无效状态失败: %v", err)
	}
	if found, err := store.loadState(ctx, key, state.Generation); err != nil || found != nil {
		t.Fatalf("无效状态 found=%v err=%v", found, err)
	}
	// generation 不匹配 → nil。
	if err := store.setJSON(ctx, key, state, time.Hour); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	if found, err := store.loadState(ctx, key, "gen-other"); err != nil || found != nil {
		t.Fatalf("异代状态 found=%v err=%v", found, err)
	}
	if found, err := store.loadState(ctx, key, state.Generation); err != nil || found == nil {
		t.Fatalf("匹配状态必须可读 found=%v err=%v", found, err)
	}
}

func TestWFSpeedFirstIndexMaintenance(t *testing.T) {
	store, server := newWFSpeedFirstStore(t)
	ctx := context.Background()

	// 新鲜索引首次写入：创建 {"keys":[k]} 并带 TTL。
	if err := store.addIndexKey(ctx, "v1:idx-test", "key-a"); err != nil {
		t.Fatalf("首次 addIndexKey 失败: %v", err)
	}
	keys, err := store.loadIndexKeys(ctx, "v1:idx-test")
	if err != nil || len(keys) != 1 || keys[0] != "key-a" {
		t.Fatalf("索引=%v err=%v", keys, err)
	}
	if ttl := server.TTL(store.redisKey("v1:idx-test")); ttl <= 0 {
		t.Fatalf("索引 TTL=%v 必须为正", ttl)
	}

	// w12f 修复后：同一索引的第二次 addIndexKey 正常追加（锁键与索引键分离）。
	if err := store.addIndexKey(ctx, "v1:idx-test", "key-b"); err != nil {
		t.Fatalf("第二次 addIndexKey 失败: %v", err)
	}
	keys, err = store.loadIndexKeys(ctx, "v1:idx-test")
	if err != nil || len(keys) != 2 || keys[0] != "key-a" || keys[1] != "key-b" {
		t.Fatalf("第二次写入后索引=%v err=%v", keys, err)
	}

	// 新鲜索引上的过滤：缺失索引被改写为空列表（删除路径的预期行为）。
	if err := store.removeIndexKeys(ctx, nil); err != nil {
		t.Fatalf("空移除失败: %v", err)
	}
	if err := store.filterIndexKeys(ctx, "v1:idx-filter", map[string]bool{"key-a": true}); err != nil {
		t.Fatalf("filterIndexKeys 失败: %v", err)
	}
	keys, err = store.loadIndexKeys(ctx, "v1:idx-filter")
	if err != nil || len(keys) != 0 {
		t.Fatalf("过滤后索引=%v err=%v", keys, err)
	}
	if err := store.removeIndexKeys(ctx, []string{"missing-key"}); err != nil {
		t.Fatalf("缺失索引移除失败: %v", err)
	}
	keys, err = store.loadIndexKeys(ctx, store.probeIndexKey())
	if err != nil || len(keys) != 0 {
		t.Fatalf("缺失索引过滤后必须为空列表 keys=%v err=%v", keys, err)
	}
}

func TestWFSpeedFirstAcquireIndexLockContextCancelled(t *testing.T) {
	store, _ := newWFSpeedFirstStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	lockKey := "v1:idx-cancel"
	if acquired, err := store.acquireLock(ctx, lockKey, "holder", time.Minute); err != nil || !acquired {
		t.Fatalf("预占索引锁失败 acquired=%v err=%v", acquired, err)
	}
	cancel()
	// 锁被占且 ctx 已取消：等待循环必须立刻以 ctx.Err() 失败，而不是耗尽重试。
	if err := store.addIndexKey(ctx, lockKey, "key"); err == nil {
		t.Fatal("ctx 取消时 addIndexKey 必须失败")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误=%v 必须保留 context.Canceled", err)
	}
}

func TestWFSpeedFirstStateCandidateProjection(t *testing.T) {
	state := wfSpeedFirstState("gen-1")
	candidate := state.candidate()
	if candidate.StateKey != "v1:sys-1:strategy-1:group-1:acc-1" {
		t.Fatalf("StateKey=%q", candidate.StateKey)
	}
	if candidate.DegradedUntil == "" {
		t.Fatalf("DegradedUntil 必须投影：%+v", candidate)
	}
	if candidate.RoundAttemptCount != 0 || candidate.RoundSuccessCount != 0 {
		t.Fatalf("nil 窗口计数必须投影为 0：%+v", candidate)
	}
	withNext := state
	nextAt := wfSpeedFirstBase.Add(time.Second).UnixMilli()
	withNext.NextProbeAtMS = &nextAt
	if withNext.candidate().NextProbeAt == "" {
		t.Fatal("NextProbeAtMS 必须投影为 RFC3339 字符串")
	}
	withoutOptionals := state
	withoutOptionals.DegradedUntilMS = nil
	withoutOptionals.NextProbeAtMS = nil
	bare := withoutOptionals.candidate()
	if bare.DegradedUntil != "" || bare.NextProbeAt != "" {
		t.Fatalf("nil 可选时间必须投影为空串：%+v", bare)
	}
}

func TestWFSpeedFirstValidStateContract(t *testing.T) {
	base := wfSpeedFirstState("gen-1")
	if !base.valid() {
		t.Fatal("基准状态必须有效")
	}
	invalid := base
	invalid.Generation = ""
	if invalid.valid() {
		t.Fatal("缺 generation 必须无效")
	}
	invalid = base
	invalid.Config.MaxFirstByteRetriesPerReq = -1
	if invalid.valid() {
		t.Fatal("负重试上限必须无效")
	}
	invalid = base
	invalid.Config.ProbeIntervalSeconds = 0
	if invalid.valid() {
		t.Fatal("0 探针间隔必须无效")
	}
}

// int64Ptr 是测试内 int64 装箱辅助。
func int64Ptr(value int64) *int64 { return &value }

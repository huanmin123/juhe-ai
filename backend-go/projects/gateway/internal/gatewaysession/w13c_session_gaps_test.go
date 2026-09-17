package gatewaysession

// w13c 覆盖率补充测试：补齐 w9c 之后仍 uncovered 的分支。
//
// 不可达语句清单（维持守卫、不删除）：
//   - affinity.go 中所有紧跟 shouldUseRedisSessionAffinity()==false 的
//     canUseProcessLocalSessionAffinity 守卫（Remember/Claim/Forget/Migrate
//     的本地臂、setSessionAffinityBindingLocked、migrationCandidateKeys）：
//     canUseProcessLocal 仅在 CacheDriver==redis 时为 false，与前置条件矛盾。
//   - affinity.go claimRedis 刷新臂与 forgetRedis 删除臂中的
//     redisSessionAffinityClient 错误分支：能走到这两处说明此前的
//     client() 调用已成功，同一 client() 不会先成功后失败。
//   - affinity.go RememberOpenAIAccountTrafficMigrationPreferenceAsync 的
//     非 redis 回落分支之后不会再触发（shouldUse 为 false 直接走 sync）。
//   - identityservice.go CreateGatewayConversationKey 错误臂：与候选校验
//     使用同一 effective secret，校验通过后不可能失败。
//   - portgateway.go ResolveGatewaySessionIdentity 的错误臂：端口层不透传
//     HMAC secret，构造期已验证服务 secret，Resolve 不会出错。
//   - ttlcache.go evictOverflow 的 oldest == nil 分支：entries > max 时
//     链表非空，状态不一致才可达。
//   - affinityorder.go resolvePolicyOrDefault 的 policy == nil 分支：入参
//     恒为 GroupTypeHighConcurrency，校验函数只对非该类型返回 nil。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w13cStubRedis 是可编程的 RedisClient 桩：nil 钩子一律返回错误。
type w13cStubRedis struct {
	getHook   func(key string) (*string, error)
	evalHook  func(script string, keys []string) (any, error)
	delHook   func(keys []string) error
	setPXHook func(key string, value string) error
	sendHook  func(args ...any) (any, error)
}

func (s *w13cStubRedis) Get(ctx context.Context, key string) (*string, error) {
	if s.getHook != nil {
		return s.getHook(key)
	}
	return nil, errors.New("w13c stub get failure")
}

func (s *w13cStubRedis) SetPX(ctx context.Context, key string, value string, ttlMs int64) error {
	if s.setPXHook != nil {
		return s.setPXHook(key, value)
	}
	return errors.New("w13c stub setpx failure")
}

func (s *w13cStubRedis) Del(ctx context.Context, keys ...string) error {
	if s.delHook != nil {
		return s.delHook(keys)
	}
	return errors.New("w13c stub del failure")
}

func (s *w13cStubRedis) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	if s.evalHook != nil {
		return s.evalHook(script, keys)
	}
	return nil, errors.New("w13c stub eval failure")
}

func (s *w13cStubRedis) SendCommand(ctx context.Context, args ...any) (any, error) {
	if s.sendHook != nil {
		return s.sendHook(args...)
	}
	return nil, errors.New("w13c stub send failure")
}

const w13cBindingValue = `{"accountId":"acc-1","scope":{"systemAccountId":"sys-1","apiKeyId":"key-1","groupId":"grp-1"}}`

func w13cRedisService(t *testing.T, stub *w13cStubRedis) (*AffinityService, *captureLogger) {
	t.Helper()
	service, _, logger := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.Redis = stub
	})
	return service, logger
}

func TestW13CNewAffinityServiceDefaultsAndSecretGuard(t *testing.T) {
	if _, err := NewAffinityService(AffinityConfig{Secret: "   "}); err == nil {
		t.Fatal("blank secret must fail construction")
	}
	service, err := NewAffinityService(AffinityConfig{Secret: testHMACSecret})
	if err != nil {
		t.Fatal(err)
	}
	if service.clock == nil || service.logger == nil {
		t.Fatal("nil clock/logger must fall back to defaults")
	}
}

func TestW13CAffinityKeyResolveArms(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	if _, ok := service.ResolveOpenAIGatewaySessionAffinityKey("", GatewaySessionAffinityKeyScope{}); ok {
		t.Fatal("empty conversation key must not resolve")
	}
	// 空白 HMAC secret 覆盖派生错误臂。
	if _, ok := service.ResolveOpenAIGatewaySessionAffinityKey("conv", GatewaySessionAffinityKeyScope{HMACSecret: "  "}); ok {
		t.Fatal("blank override secret must fail derivation")
	}
	if _, ok := service.ResolveOpenAIGatewaySessionAffinityKeyFromClientSource("  ", GatewaySessionAffinityKeyScope{}); ok {
		t.Fatal("blank client key must not resolve")
	}
	if _, ok := service.ResolveOpenAIGatewaySessionAffinityKeyFromClientSource(" client-key ", GatewaySessionAffinityKeyScope{HMACSecret: "  "}); ok {
		t.Fatal("blank override secret must fail client-source derivation")
	}
	if key, ok := service.ResolveOpenAIGatewaySessionAffinityKey("conv", GatewaySessionAffinityKeyScope{}); !ok || key == "" {
		t.Fatalf("valid derivation = %q %v", key, ok)
	}
}

func TestW13CRedisDriverReadArms(t *testing.T) {
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	// 全失败桩：claim / forget / migrate / preference 读取全部走错误臂。
	failing := &w13cStubRedis{}
	service, logger := w13cRedisService(t, failing)
	if owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "w13c-s1", "acc-1", scope); ok || owner != "" {
		t.Fatalf("failing claim = %q %v", owner, ok)
	}
	if !logger.HasEvent("redis_openai_session_affinity_remember_failed") {
		t.Fatal("claim failure must warn")
	}
	if err := service.ForgetOpenAIAccountForSessionAsync(context.Background(), "w13c-s1", "acc-1"); err != nil {
		t.Fatal(err)
	}
	if !logger.HasEvent("redis_openai_session_affinity_forget_failed") {
		t.Fatal("forget failure must warn")
	}
	if _, err := service.MigrateOpenAIAccountSessionAffinityAsync(context.Background(), "acc-1", "acc-2", scope, MigrationOptions{}); err == nil {
		t.Fatal("migration failure must surface")
	}
	if !logger.HasEvent("redis_openai_session_affinity_migration_failed") {
		t.Fatal("migration failure must warn")
	}
	if preference := service.trafficMigrationPreferenceForAccountsAsync(context.Background(), []string{"acc-1", "acc-2"}, scope); preference != nil {
		t.Fatal("failing preference read must degrade to nil")
	}
	if !logger.HasEvent("redis_openai_traffic_migration_preference_read_failed") {
		t.Fatal("preference read failure must warn")
	}
	if binding := service.getRedisSessionAffinityBindingForOrdering(context.Background(), "w13c-s1"); binding != nil {
		t.Fatal("failing binding read must degrade to nil")
	}
	if binding := service.getRedisSessionAffinityBindingForOrdering(context.Background(), ""); binding != nil {
		t.Fatal("empty key must skip the read")
	}

	// ThrowOnRedisError 透传写错误。
	if err := service.RememberOpenAIAccountTrafficMigrationPreferenceAsync(context.Background(), "acc-1", "acc-2", scope, TrafficMigrationPreferenceWriteOptions{ThrowOnRedisError: true}); err == nil {
		t.Fatal("throw-on-error must surface the write failure")
	}
	// 相同 source/target 短路。
	if err := service.RememberOpenAIAccountTrafficMigrationPreferenceAsync(context.Background(), "acc-1", "acc-1", scope, TrafficMigrationPreferenceWriteOptions{ThrowOnRedisError: true}); err != nil {
		t.Fatalf("same account must short-circuit, got %v", err)
	}
}

func TestW13CRedisDriverCorruptRecordArms(t *testing.T) {
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	// 绑定值损坏：Del 失败 / Del 成功两个臂。
	corrupt := &w13cStubRedis{getHook: func(string) (*string, error) {
		value := "{not-json"
		return &value, nil
	}}
	service, _ := w13cRedisService(t, corrupt)
	if _, err := service.getRedisSessionAffinityRecord(context.Background(), "w13c-c1", false); err == nil {
		t.Fatal("corrupt record with failing del must error")
	}
	workingDel := &w13cStubRedis{
		getHook: func(string) (*string, error) {
			value := "{not-json"
			return &value, nil
		},
		delHook: func([]string) error { return nil },
	}
	service2, _ := w13cRedisService(t, workingDel)
	record, err := service2.getRedisSessionAffinityRecord(context.Background(), "w13c-c1", false)
	if err != nil || record != nil {
		t.Fatalf("corrupt record with working del = %v, %v", record, err)
	}

	// 有效绑定 + 刷新失败（order 读取降级）。
	refreshFail := &w13cStubRedis{
		getHook: func(string) (*string, error) {
			value := w13cBindingValue
			return &value, nil
		},
	}
	service3, logger3 := w13cRedisService(t, refreshFail)
	if binding := service3.getRedisSessionAffinityBindingForOrdering(context.Background(), "w13c-c2"); binding != nil {
		t.Fatal("refresh failure must degrade to nil")
	}
	if !logger3.HasEvent("redis_openai_session_affinity_read_failed") {
		t.Fatal("refresh failure must warn on ordering read")
	}
	_ = scope
}

func TestW13CRedisDriverWriteArms(t *testing.T) {
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	// Get 成功（无记录）+ Eval 失败：claim 的 CAS 写入错误臂。
	evalFail := &w13cStubRedis{getHook: func(string) (*string, error) { return nil, nil }}
	service, logger := w13cRedisService(t, evalFail)
	if owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "w13c-w1", "acc-1", scope); ok || owner != "" {
		t.Fatalf("eval failure claim = %q %v", owner, ok)
	}
	if !logger.HasEvent("redis_openai_session_affinity_remember_failed") {
		t.Fatal("CAS failure must warn")
	}
	if err := service.ForgetOpenAIAccountForSessionAsync(context.Background(), "w13c-w1", "acc-1"); err != nil {
		t.Fatal(err)
	}

	// Get 成功（有记录）+ Eval 失败：forget 删除错误臂。
	claimFail := &w13cStubRedis{
		getHook: func(string) (*string, error) {
			value := w13cBindingValue
			return &value, nil
		},
	}
	service2, logger2 := w13cRedisService(t, claimFail)
	if err := service2.ForgetOpenAIAccountForSessionAsync(context.Background(), "w13c-w2", "acc-1"); err != nil {
		t.Fatal(err)
	}
	if !logger2.HasEvent("redis_openai_session_affinity_forget_failed") {
		t.Fatal("delete failure must warn")
	}
	// 账号不匹配时跳过删除。
	if err := service2.ForgetOpenAIAccountForSessionAsync(context.Background(), "w13c-w2", "acc-other"); err != nil {
		t.Fatal(err)
	}

	// CAS 竞争：第一次写入返回 0，第二次读取失败。
	getCalls := 0
	racy := &w13cStubRedis{
		getHook: func(string) (*string, error) {
			getCalls++
			if getCalls >= 2 {
				return nil, errors.New("w13c second read failure")
			}
			return nil, nil
		},
		evalHook: func(string, []string) (any, error) { return int64(0), nil },
	}
	service3, _ := w13cRedisService(t, racy)
	if owner, ok := service3.ClaimOpenAIAccountForSessionAsync(context.Background(), "w13c-w3", "acc-1", scope); ok || owner != "" {
		t.Fatalf("racy claim = %q %v", owner, ok)
	}

	// CAS 竞争两次：第二次读取为空、写入仍失败 → 返回空 owner。
	racyTwice := &w13cStubRedis{
		getHook:  func(string) (*string, error) { return nil, nil },
		evalHook: func(string, []string) (any, error) { return int64(0), nil },
	}
	service4, _ := w13cRedisService(t, racyTwice)
	if owner, ok := service4.ClaimOpenAIAccountForSessionAsync(context.Background(), "w13c-w4", "acc-1", scope); ok || owner != "" {
		t.Fatalf("double-racy claim = %q %v", owner, ok)
	}
}

func TestW13CRedisTrafficPreferenceArms(t *testing.T) {
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	// 偏好值损坏 + Del 成功：返回无偏好。
	corrupt := &w13cStubRedis{
		getHook: func(string) (*string, error) {
			value := "not-json"
			return &value, nil
		},
		delHook: func([]string) error { return nil },
	}
	service, _ := w13cRedisService(t, corrupt)
	if preference, err := service.getRedisTrafficMigrationPreference(context.Background(), "w13c-p1"); err != nil || preference != nil {
		t.Fatalf("corrupt preference = %v, %v", preference, err)
	}

	// 偏好值损坏 + Del 失败：错误臂。
	brokenDel := &w13cStubRedis{
		getHook: func(string) (*string, error) {
			value := "not-json"
			return &value, nil
		},
	}
	service2, _ := w13cRedisService(t, brokenDel)
	if _, err := service2.getRedisTrafficMigrationPreference(context.Background(), "w13c-p1"); err == nil {
		t.Fatal("failing del on corrupt preference must error")
	}

	// 有效偏好 + PEXPIRE 失败：错误臂。
	expireFail := &w13cStubRedis{
		getHook: func(string) (*string, error) {
			value := `{"sourceAccountId":"acc-1","targetAccountId":"acc-2"}`
			return &value, nil
		},
	}
	service3, _ := w13cRedisService(t, expireFail)
	if _, err := service3.getRedisTrafficMigrationPreference(context.Background(), "w13c-p1"); err == nil {
		t.Fatal("failing pexpire must error")
	}

	// 写入侧 SetPX 错误（不抛出时仅 warn）。
	setFail := &w13cStubRedis{}
	service4, logger4 := w13cRedisService(t, setFail)
	if err := service4.RememberOpenAIAccountTrafficMigrationPreferenceAsync(context.Background(), "acc-1", "acc-2", scope, TrafficMigrationPreferenceWriteOptions{}); err != nil {
		t.Fatalf("non-throwing write must swallow, got %v", err)
	}
	if !logger4.HasEvent("redis_openai_traffic_migration_preference_write_failed") {
		t.Fatal("write failure must warn")
	}
	_ = scope
}

func TestW13CRedisKeyBuilderArms(t *testing.T) {
	// 空 namespace 触发各 key 构造器的错误臂。
	if _, err := redisSessionAffinityBindingKey("", "k"); err == nil {
		t.Fatal("empty namespace must fail")
	}
	if _, err := redisSessionAffinityAccountIndexKey("", "a"); err == nil {
		t.Fatal("empty namespace must fail")
	}
	if _, err := redisSessionAffinityAccountSystemIndexKey("", "a", "s"); err == nil {
		t.Fatal("empty namespace must fail")
	}
	if _, err := redisSessionAffinityAccountSystemAPIKeyIndexKey("", "a", "s", "k"); err == nil {
		t.Fatal("empty namespace must fail")
	}
	if _, err := redisTrafficMigrationPreferenceKey("", "scope"); err == nil {
		t.Fatal("empty namespace must fail")
	}
	// 有效 namespace 的 key 形状。
	key, err := redisSessionAffinityBindingKey("ns-1", "session key/1")
	if err != nil || !strings.HasPrefix(key, "juhe-ai:ns-1:session-affinity:binding:") {
		t.Fatalf("binding key = %q err=%v", key, err)
	}
	// 迁移索引 key 的三种 scope 形状。
	if _, err := redisSessionAffinityMigrationIndexKey("ns-1", "a", &OpenAIGatewaySessionAffinityScope{SystemAccountID: "s", APIKeyID: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := redisSessionAffinityMigrationIndexKey("ns-1", "a", &OpenAIGatewaySessionAffinityScope{SystemAccountID: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := redisSessionAffinityMigrationIndexKey("ns-1", "a", nil); err != nil {
		t.Fatal(err)
	}
}

func TestW13CLazyRedisClientErrorArms(t *testing.T) {
	empty := newLazyRedisClient("")
	ctx := context.Background()
	if _, err := empty.Get(ctx, "k"); err == nil {
		t.Fatal("empty URL get must fail")
	}
	if err := empty.SetPX(ctx, "k", "v", 1000); err == nil {
		t.Fatal("empty URL setpx must fail")
	}
	if err := empty.Del(ctx, "k"); err == nil {
		t.Fatal("empty URL del must fail")
	}
	if _, err := empty.Eval(ctx, "return 1", []string{}); err == nil {
		t.Fatal("empty URL eval must fail")
	}
	if _, err := empty.SendCommand(ctx, "PING"); err == nil {
		t.Fatal("empty URL send must fail")
	}
	// Close 对非 closer 委托走 nil 返回臂。
	nonCloser := newLazyRedisClient("")
	nonCloser.delegate = &w13cStubRedis{}
	if err := nonCloser.Close(); err != nil {
		t.Fatalf("non-closer close = %v", err)
	}
}

func TestW13CSchedulingPolicyRemainingArms(t *testing.T) {
	defaults := SchedulingDefaults{GlobalMax: 5000}
	cases := []map[string]any{
		{"bogus": 1},
		{"maxQueueSize": "x"},
		{"mode": "turbo"},
		{"defaultSoftConcurrency": "x"},
		{"fastFirstEnabled": "yes"},
		{"fallbackOnQueueEnabled": 1},
		{"breakAffinityOnSoftLimit": "no"},
		{"breakAffinityOnQueueWaitMs": -1},
		{"firstOutputSlowThresholdMs": 0},
		{"recentTimeoutWindowSeconds": "x"},
		{"recentTimeoutPenaltyThreshold": 0},
		{"maxQueueWaitMs": "x"},
		{"perApiKeyQueueLimit": "x"},
		{"clientIpConcurrencyLimit": -1},
		{"clientIpConcurrencyOverflowMode": 1},
		{"imageLaneMaxConcurrency": -5},
	}
	for index, policy := range cases {
		if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, policy, defaults); err == nil {
			t.Fatalf("case %d (%v) must fail", index, policy)
		}
	}
	// 超大整数臂。
	if _, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, map[string]any{"maxQueueSize": float64(1) * 1e18}, defaults); err == nil {
		t.Fatal("overflow maxQueueSize must fail")
	}
}

type w13cStubConcurrency struct {
	loadTotalErr error
	loadImageErr error
	loadStatsErr error
	current      map[string]int
	image        map[string]int
}

func (c *w13cStubConcurrency) GetAccountCurrentConcurrency(accountID string, lane string) int {
	if lane == RequestLaneImage {
		return c.image[accountID]
	}
	return c.current[accountID]
}

func (c *w13cStubConcurrency) LoadAccountCurrentConcurrencyByIDsAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	if lane == RequestLaneImage {
		if c.loadImageErr != nil {
			return nil, c.loadImageErr
		}
		return c.image, nil
	}
	if c.loadTotalErr != nil {
		return nil, c.loadTotalErr
	}
	return c.current, nil
}

func (c *w13cStubConcurrency) LoadAccountInFlightStatsByIDs(accountIDs []string, thresholds InFlightThresholds) map[string]AccountInFlightStats {
	stats := map[string]AccountInFlightStats{}
	for id, value := range c.current {
		stats[id] = AccountInFlightStats{CurrentConcurrency: value}
	}
	return stats
}

func (c *w13cStubConcurrency) LoadAccountInFlightStatsByIDsAsync(ctx context.Context, accountIDs []string, thresholds InFlightThresholds) (map[string]AccountInFlightStats, error) {
	if c.loadStatsErr != nil {
		return nil, c.loadStatsErr
	}
	stats := map[string]AccountInFlightStats{}
	for id, value := range c.current {
		stats[id] = AccountInFlightStats{CurrentConcurrency: value}
	}
	return stats, nil
}

func w13cAccount(id string, priority int, concurrency int) gatewayruntimecache.OpenAIAccountSecret {
	concurrencyValue := concurrency
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                 id,
		Priority:           priority,
		ConcurrencyLimit:   5,
		CurrentConcurrency: &concurrencyValue,
	}
}

func TestW13CBusyLaneArms(t *testing.T) {
	stub := &w13cStubConcurrency{current: map[string]int{}}
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.Concurrency = stub
	})
	accounts := []gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 1), w13cAccount("a2", 2, 1)}

	// 非 redis 运行时驱动回落同步 busy-lane。
	busy, err := service.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(context.Background(), accounts, BusyLaneOptions{
		DispatchOrderingOptions: DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency},
		RequestLane:             "text",
	})
	if err != nil || busy {
		t.Fatalf("text lane = %v, %v", busy, err)
	}

	// image lane + 非法调度策略 → 校验错误臂。
	badPolicy, err := service.AreOpenAIHighConcurrencyAccountsBusyForLane(accounts, BusyLaneOptions{
		DispatchOrderingOptions: DispatchOrderingOptions{
			GroupType:        GroupTypeHighConcurrency,
			SchedulingPolicy: map[string]any{"mode": "turbo"},
		},
		RequestLane: RequestLaneImage,
	})
	if err == nil || badPolicy {
		t.Fatalf("bad policy busy = %v, %v", badPolicy, err)
	}

	// redis 运行时驱动 + Concurrency 加载错误。
	errStub := &w13cStubConcurrency{loadTotalErr: errors.New("w13c load failure")}
	errService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{}
		cfg.Concurrency = errStub
	})
	if _, err := errService.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(context.Background(), accounts, BusyLaneOptions{
		DispatchOrderingOptions: DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency},
	}); err == nil {
		t.Fatal("load failure must surface")
	}

	// redis 运行时驱动 + image lane 加载错误。
	imageErrStub := &w13cStubConcurrency{current: map[string]int{}, loadImageErr: errors.New("w13c image load failure")}
	imageErrService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{}
		cfg.Concurrency = imageErrStub
	})
	if _, err := imageErrService.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(context.Background(), accounts, BusyLaneOptions{
		DispatchOrderingOptions: DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency},
		RequestLane:             RequestLaneImage,
	}); err == nil {
		t.Fatal("image load failure must surface")
	}

	// redis 运行时驱动 + Concurrency 为 nil → 非 busy。
	nilConcurrencyService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{}
		cfg.Concurrency = nil
	})
	busy, err = nilConcurrencyService.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(context.Background(), accounts, BusyLaneOptions{
		DispatchOrderingOptions: DispatchOrderingOptions{GroupType: GroupTypeHighConcurrency},
	})
	if err != nil || busy {
		t.Fatalf("nil concurrency = %v, %v", busy, err)
	}
}

func TestW13CHighConcurrencyOrderArms(t *testing.T) {
	accounts := []gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 1), w13cAccount("a2", 2, 1)}

	// 非 redis 运行时驱动回落同步排序。
	stub := &w13cStubConcurrency{current: map[string]int{}}
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverMemory
		cfg.Concurrency = stub
	})
	if _, err := service.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "w13c-key", DispatchOrderingOptions{
		GroupType: GroupTypeHighConcurrency,
	}); err != nil {
		t.Fatal(err)
	}

	// redis 运行时驱动 + Concurrency nil：async 统计空表 + fallback 本地绑定读取。
	nilConcurrencyService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverMemory
		cfg.Concurrency = nil
	})
	// fastFirst 关闭 + 无迁移目标 → personal by binding。
	ordered, err := nilConcurrencyService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "w13c-key", DispatchOrderingOptions{
		GroupType:        GroupTypeHighConcurrency,
		SchedulingPolicy: map[string]any{"fastFirstEnabled": false},
	})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("fast-first off = %v, %v", ordered, err)
	}
	// fastFirst 关闭 + 有迁移目标 → hard-busy-last async。
	ordered, err = nilConcurrencyService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "w13c-key", DispatchOrderingOptions{
		GroupType:             GroupTypeHighConcurrency,
		SchedulingPolicy:      map[string]any{"fastFirstEnabled": false},
		TrafficMigrationScope: &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1"},
		ModelPriority:         nil,
	})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("hard-busy-last async = %v, %v", ordered, err)
	}

	// 非法调度策略在 async 路径的传播。
	if _, err := nilConcurrencyService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "", DispatchOrderingOptions{
		GroupType:        GroupTypeHighConcurrency,
		SchedulingPolicy: map[string]any{"mode": "turbo"},
	}); err == nil {
		t.Fatal("bad policy must surface from async ordering")
	}

	// 硬满载分组：hardBusyLast async 两个桶。
	fullStub := &w13cStubConcurrency{current: map[string]int{"a1": 9, "a2": 0}}
	fullService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{}
		cfg.Concurrency = fullStub
	})
	ordered, err = fullService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "", DispatchOrderingOptions{
		GroupType:             GroupTypeHighConcurrency,
		TrafficMigrationScope: &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1"},
	})
	if err != nil || ordered[0].ID != "a2" {
		t.Fatalf("hard busy last = %v, %v", ordered, err)
	}
	// 全部空闲：直接原序返回。
	freeStub := &w13cStubConcurrency{current: map[string]int{"a1": 0, "a2": 0}}
	freeService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{}
		cfg.Concurrency = freeStub
	})
	ordered, err = freeService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "", DispatchOrderingOptions{
		GroupType:             GroupTypeHighConcurrency,
		TrafficMigrationScope: &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1"},
	})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("all free = %v, %v", ordered, err)
	}

	// LoadAccountCurrentConcurrencyByIDsAsync 错误传播（fastFirst 关闭 +
	// 迁移目标存在 → hard-busy-last async）。
	hardBusyLoadErr := &w13cStubConcurrency{loadTotalErr: errors.New("w13c load failure")}
	preferenceStub := &w13cStubRedis{getHook: func(key string) (*string, error) {
		if strings.Contains(key, "traffic-migration-preference") {
			value := `{"sourceAccountId":"gone-1","targetAccountId":"a2"}`
			return &value, nil
		}
		return nil, errors.New("w13c binding read failure")
	}, sendHook: func(args ...any) (any, error) {
		return "OK", nil
	}}
	loadErrService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = preferenceStub
		cfg.Concurrency = hardBusyLoadErr
	})
	if _, err := loadErrService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "", DispatchOrderingOptions{
		GroupType:             GroupTypeHighConcurrency,
		SchedulingPolicy:      map[string]any{"fastFirstEnabled": false},
		TrafficMigrationScope: &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"},
	}); err == nil {
		t.Fatal("load failure must surface from hard-busy-last async")
	}

	// LoadAccountInFlightStatsByIDsAsync 错误传播（fastFirst 开启路径）。
	statsErrService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{}
		cfg.Concurrency = &w13cStubConcurrency{loadStatsErr: errors.New("w13c stats failure")}
	})
	if _, err := statsErrService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "", DispatchOrderingOptions{
		GroupType: GroupTypeHighConcurrency,
	}); err == nil {
		t.Fatal("stats load failure must surface")
	}
}

func TestW13CCompareHighConcurrencyCandidateArms(t *testing.T) {
	policy := DefaultHighConcurrencyGroupSchedulingPolicy(SchedulingDefaults{GlobalMax: 5000})
	policy.FallbackOnQueueEnabled = true
	policy.BreakAffinityOnSoftLimit = true
	base := highConcurrencyCandidate{account: w13cAccount("a1", 1, 0), index: 0, softLimit: 5}
	peer := highConcurrencyCandidate{account: w13cAccount("a2", 2, 1), index: 1, softLimit: 5}
	bound := base
	bound.affinityAllowed = true
	preferred := base
	preferred.trafficMigrationPreferred = true
	softBusy := base
	softBusy.softBusy = true
	super := highConcurrencyCandidate{account: w13cAccount("a3", 1, 0), index: 2, softLimit: 5}
	super.account.SuperPriorityEnabled = true
	fallback := highConcurrencyCandidate{account: w13cAccount("a4", 1, 0), index: 3, softLimit: 5}
	fallback.account.FallbackEnabled = true
	qualityLow := highConcurrencyCandidate{account: w13cAccount("a5", 1, 0), index: 4, softLimit: 5}
	qualityHigh := highConcurrencyCandidate{account: w13cAccount("a6", 1, 0), index: 5, softLimit: 5}
	qualityHigh.account.QualityScore = float64Ptr(99)
	loaded := highConcurrencyCandidate{account: w13cAccount("a7", 1, 4), index: 6, softLimit: 5}
	idle := highConcurrencyCandidate{account: w13cAccount("a8", 1, 0), index: 7, softLimit: 5}

	if compareHighConcurrencyCandidates(base, peer, &policy, false, nil) >= 0 {
		t.Fatal("lower index must win ties")
	}
	if compareHighConcurrencyCandidates(softBusy, base, &policy, false, nil) <= 0 {
		t.Fatal("soft-busy must rank worse")
	}
	if compareHighConcurrencyCandidates(super, base, &policy, false, nil) >= 0 {
		t.Fatal("super priority must rank better")
	}
	if compareHighConcurrencyCandidates(base, super, &policy, false, nil) <= 0 {
		t.Fatal("plain must rank worse than super")
	}
	if compareHighConcurrencyCandidates(base, peer, &policy, true, nil) >= 0 {
		t.Fatal("priority must decide with equal load")
	}
	if compareHighConcurrencyCandidates(loaded, idle, &policy, false, nil) >= 0 {
		t.Fatal("higher load ratio must rank worse")
	}
	if compareHighConcurrencyCandidates(qualityHigh, qualityLow, &policy, false, nil) >= 0 {
		t.Fatal("higher quality must rank better")
	}
	if compareHighConcurrencyCandidates(bound, base, &policy, false, nil) >= 0 {
		t.Fatal("affinity allowed must rank better")
	}
	noFallback := policy
	noFallback.FallbackOnQueueEnabled = false
	if compareHighConcurrencyCandidates(fallback, base, &noFallback, true, nil) <= 0 {
		t.Fatal("fallback rank must decide when queue fallback disabled")
	}
	withFallback := policy
	withFallback.FallbackOnQueueEnabled = true
	allBusy := highConcurrencyCandidate{account: w13cAccount("a9", 1, 9), index: 8, softLimit: 5, softBusy: true, hardBusy: true}
	allBusyPeer := highConcurrencyCandidate{account: w13cAccount("a10", 1, 9), index: 9, softLimit: 5, softBusy: true, hardBusy: true}
	if compareHighConcurrencyCandidates(allBusy, allBusyPeer, &withFallback, false, nil) >= 0 {
		t.Fatal("fallback rank must decide when nothing soft available")
	}
	if compareHighConcurrencyCandidates(preferred, base, &policy, false, nil) >= 0 {
		t.Fatal("traffic preferred must rank better")
	}
	if compareHighConcurrencyCandidates(base, preferred, &policy, false, nil) <= 0 {
		t.Fatal("non-preferred must rank worse")
	}
}

func float64Ptr(value float64) *float64 {
	return &value
}

type w13cStubResolver struct{}

func (r *w13cStubResolver) ID() string { return "w13c-stub" }

func (r *w13cStubResolver) Collect(context ResolverContext) []RawCandidate {
	return []RawCandidate{{
		ResolverID:        "w13c-stub",
		SemanticKind:      IdentitySemanticKindSession,
		SemanticNamespace: "w13c-ns",
		RawValue:          "w13c-session",
		Priority:          1,
	}}
}

func TestW13CSmallHelperArms(t *testing.T) {
	// JS 解码截断 rune。
	if r, size := decodeJSRune("\xff"); r != 0xfffd || size != 1 {
		t.Fatalf("decodeJSRune invalid = %q %d", r, size)
	}
	if r, size := decodeLastJSRune("a\xff"); r != 0xfffd || size != 1 {
		t.Fatalf("decodeLastJSRune invalid = %q %d", r, size)
	}
	// 候选校验在 secret 为空时的错误臂。
	if _, _, err := ValidateGatewaySessionIdentityCandidate(RawCandidate{RawValue: "sess"}, ResolvedGatewaySessionIdentityScope{}); !errors.Is(err, ErrEmptyHMACSecret) {
		t.Fatalf("empty secret = %v", err)
	}
	// 并发身份 ID 去重与空值跳过。
	ids := GatewayAccountConcurrencyAccountIDs([]GatewayAccountConcurrencyIdentity{
		{ID: "a"},
		{ID: ""},
		{ID: "a"},
	})
	if len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("ids = %v", ids)
	}
	// 双缺失质量分：rank 相同。
	if compareAccountQualityRank(gatewayruntimecache.OpenAIAccountSecret{}, gatewayruntimecache.OpenAIAccountSecret{}) != 0 {
		t.Fatal("both missing quality must tie")
	}
	// 端口层 nil 请求视图。
	view := gatewayRequestIdentityView{}
	if view.Path() != "" || view.OriginalURL() != "" {
		t.Fatal("nil request view must be empty")
	}
	// TTL 缓存删除缺失 key。
	cache := newTTLCache[string](4, time.Minute, false)
	cache.Delete("missing")
	// 空白 HMAC 覆盖下的 Resolve 错误臂（经由一个产出候选的解析器）。
	identityService, err := NewIdentityService(testHMACSecret)
	if err != nil {
		t.Fatal(err)
	}
	stubResolver := &w13cStubResolver{}
	if _, err := identityService.Resolve(gatewayRequestIdentityView{}, IdentityScope{HMACSecret: "  "}, []Resolver{stubResolver}); err == nil {
		t.Fatal("blank scope secret must fail resolve")
	}
	// 正常 secret 下同一解析器可解析。
	identity, err := identityService.Resolve(gatewayRequestIdentityView{}, IdentityScope{}, []Resolver{stubResolver})
	if err != nil || identity.Status != IdentityStatusResolved {
		t.Fatalf("resolve = %+v err=%v", identity, err)
	}
	// 偏好排序：目标已在首位 / 无处可移。
	accounts := []gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 0), w13cAccount("a2", 2, 0)}
	if got := orderOpenAIAccountsByTrafficMigrationPreference(accounts, "a2", nil); len(got) != 2 {
		t.Fatalf("already ordered = %v", got)
	}
	if got := orderOpenAIAccountsByTrafficMigrationPreference(accounts, "missing", nil); len(got) != 2 {
		t.Fatalf("missing target = %v", got)
	}
	if got := orderOpenAIAccountsByTrafficMigrationPreference(accounts, "", nil); len(got) != 2 {
		t.Fatalf("empty target = %v", got)
	}
	if got := orderOpenAIAccountsByTrafficMigrationPreference([]gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 0)}, "a1", nil); len(got) != 1 {
		t.Fatalf("single account = %v", got)
	}
}

func TestW13CNilRedisServiceClientArms(t *testing.T) {
	// CacheDriver=redis 但未注入客户端：所有入口在 clientForUse 处失败。
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.Redis = nil
		cfg.RedisCacheURL = ""
	})
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	binding := SessionBinding{AccountID: "acc-1", Scope: scope}
	if _, err := service.setRedisSessionAffinityBinding(context.Background(), "w13c-k", binding, nil); err == nil {
		t.Fatal("missing redis client must fail set")
	}
	if err := service.setRedisTrafficMigrationPreference(context.Background(), "w13c-scope", TrafficMigrationPreference{SourceAccountID: "a", TargetAccountID: "b"}); err == nil {
		t.Fatal("missing redis client must fail preference set")
	}
	if _, err := service.getRedisTrafficMigrationPreference(context.Background(), "w13c-scope"); err == nil {
		t.Fatal("missing redis client must fail preference get")
	}
	if err := service.deleteRedisTrafficMigrationPreference(context.Background(), "w13c-scope"); err == nil {
		t.Fatal("missing redis client must fail preference delete")
	}
	if _, err := service.redisSessionAffinityMigrationCandidateKeys(context.Background(), "acc-1", scope); err == nil {
		t.Fatal("missing redis client must fail migration candidates")
	}
}

func TestW13CRedisKeyArmsInsideFlows(t *testing.T) {
	// 空 namespace：各流程在 key 构造器处失败。
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.Redis = &w13cStubRedis{}
		cfg.RedisNamespace = ""
	})
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	if owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "w13c-k", "acc-1", scope); ok || owner != "" {
		t.Fatalf("empty namespace claim = %q %v", owner, ok)
	}
	if err := service.ForgetOpenAIAccountForSessionAsync(context.Background(), "w13c-k", "acc-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.redisSessionAffinityMigrationCandidateKeys(context.Background(), "acc-1", scope); err == nil {
		t.Fatal("empty namespace must fail candidate keys")
	}
	if err := service.setRedisTrafficMigrationPreference(context.Background(), "scope", TrafficMigrationPreference{SourceAccountID: "a", TargetAccountID: "b"}); err == nil {
		t.Fatal("empty namespace must fail preference key")
	}
	if _, err := service.getRedisTrafficMigrationPreference(context.Background(), "scope"); err == nil {
		t.Fatal("empty namespace must fail preference get key")
	}
	if err := service.deleteRedisTrafficMigrationPreference(context.Background(), "scope"); err == nil {
		t.Fatal("empty namespace must fail preference delete key")
	}

	// 直接调用 refresh/delete 的 key 构造错误臂。
	record := &redisSessionBindingRecord{rawValue: w13cBindingValue}
	if _, err := service.refreshRedisSessionAffinityBinding(context.Background(), service.redis, "w13c-k", record); err == nil {
		t.Fatal("empty namespace must fail refresh")
	}
	if _, err := service.deleteRedisSessionAffinityBinding(context.Background(), service.redis, "w13c-k", record); err == nil {
		t.Fatal("empty namespace must fail delete")
	}
}

func TestW13CMigrationCandidateSendFailure(t *testing.T) {
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	sendFail := &w13cStubRedis{sendHook: func(args ...any) (any, error) {
		return nil, errors.New("w13c zset failure")
	}}
	service, _ := w13cRedisService(t, sendFail)
	if _, err := service.redisSessionAffinityMigrationCandidateKeys(context.Background(), "acc-1", scope); err == nil {
		t.Fatal("zset failure must propagate")
	}
}

func TestW13CMigrateRedisHappyAndSkipArms(t *testing.T) {
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	// ZRANGEBYSCORE 返回两个 key：一个已消失，一个属于其他账号。
	evalCalls := 0
	mixed := &w13cStubRedis{
		getHook: func(key string) (*string, error) {
			if strings.Contains(key, "binding:w13c-vanished") {
				return nil, nil
			}
			value := `{"accountId":"acc-other","scope":{"systemAccountId":"sys-1","apiKeyId":"key-1","groupId":"grp-1"}}`
			return &value, nil
		},
		sendHook: func(args ...any) (any, error) {
			if args[0] == "ZRANGEBYSCORE" {
				return []any{"w13c-vanished", "w13c-other"}, nil
			}
			return "OK", nil
		},
		evalHook: func(script string, keys []string) (any, error) {
			evalCalls++
			return int64(1), nil
		},
	}
	service, _ := w13cRedisService(t, mixed)
	result, err := service.migrateRedisOpenAIAccountSessionAffinity(context.Background(), "acc-1", "acc-2", scope, MigrationOptions{})
	if err != nil || result.MigratedSessionCount != 0 {
		t.Fatalf("mixed migrate = %+v err=%v evals=%d", result, err, evalCalls)
	}

	// 正常迁移：候选 key 属于 source 账号，CAS 成功。
	happy := &w13cStubRedis{
		getHook: func(key string) (*string, error) {
			value := `{"accountId":"acc-1","scope":{"systemAccountId":"sys-1","apiKeyId":"key-1","groupId":"grp-1"}}`
			return &value, nil
		},
		sendHook: func(args ...any) (any, error) {
			if args[0] == "ZRANGEBYSCORE" {
				return []any{"w13c-owned"}, nil
			}
			return "OK", nil
		},
		evalHook: func(script string, keys []string) (any, error) { return int64(1), nil },
	}
	service2, _ := w13cRedisService(t, happy)
	result, err = service2.migrateRedisOpenAIAccountSessionAffinity(context.Background(), "acc-1", "acc-2", scope, MigrationOptions{})
	if err != nil || result.MigratedSessionCount != 1 {
		t.Fatalf("happy migrate = %+v err=%v", result, err)
	}

	// scope 不匹配跳过。
	scopeMismatch, err := service2.migrateRedisOpenAIAccountSessionAffinity(context.Background(), "acc-1", "acc-2", &OpenAIGatewaySessionAffinityScope{SystemAccountID: "other", APIKeyID: "key-1", GroupID: "grp-9"}, MigrationOptions{})
	if err != nil || scopeMismatch.MigratedSessionCount != 0 {
		t.Fatalf("scope mismatch migrate = %+v err=%v", scopeMismatch, err)
	}

	// CAS 失败：写入未生效不计入迁移。
	casFail := &w13cStubRedis{
		getHook: func(key string) (*string, error) {
			value := `{"accountId":"acc-1","scope":{"systemAccountId":"sys-1","apiKeyId":"key-1","groupId":"grp-1"}}`
			return &value, nil
		},
		sendHook: func(args ...any) (any, error) {
			if args[0] == "ZRANGEBYSCORE" {
				return []any{"w13c-owned"}, nil
			}
			return "OK", nil
		},
		evalHook: func(script string, keys []string) (any, error) { return int64(0), nil },
	}
	service3, _ := w13cRedisService(t, casFail)
	result, err = service3.migrateRedisOpenAIAccountSessionAffinity(context.Background(), "acc-1", "acc-2", scope, MigrationOptions{})
	if err != nil || result.MigratedSessionCount != 0 {
		t.Fatalf("cas-fail migrate = %+v err=%v", result, err)
	}

	// CAS 错误：迁移整体失败。
	casErr := &w13cStubRedis{
		getHook: func(key string) (*string, error) {
			value := `{"accountId":"acc-1","scope":{"systemAccountId":"sys-1","apiKeyId":"key-1","groupId":"grp-1"}}`
			return &value, nil
		},
		sendHook: func(args ...any) (any, error) {
			if args[0] == "ZRANGEBYSCORE" {
				return []any{"w13c-owned"}, nil
			}
			return "OK", nil
		},
	}
	service4, _ := w13cRedisService(t, casErr)
	if _, err := service4.migrateRedisOpenAIAccountSessionAffinity(context.Background(), "acc-1", "acc-2", scope, MigrationOptions{}); err == nil {
		t.Fatal("CAS failure must surface")
	}
}

func TestW13CClaimRefreshAndSecondReadArms(t *testing.T) {
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	// 同账号再认领：刷新成功返回 owner。
	owned := &w13cStubRedis{
		getHook: func(key string) (*string, error) {
			value := w13cBindingValue
			return &value, nil
		},
		evalHook: func(script string, keys []string) (any, error) { return int64(1), nil },
		sendHook: func(args ...any) (any, error) { return "OK", nil },
	}
	service, _ := w13cRedisService(t, owned)
	owner, ok := service.ClaimOpenAIAccountForSessionAsync(context.Background(), "w13c-owned", "acc-1", scope)
	if !ok || owner != "acc-1" {
		t.Fatalf("refresh claim = %q %v", owner, ok)
	}

	// 同账号再认领 + 刷新失败。
	refreshFail := &w13cStubRedis{
		getHook: func(key string) (*string, error) {
			value := w13cBindingValue
			return &value, nil
		},
	}
	service2, logger2 := w13cRedisService(t, refreshFail)
	if owner, ok := service2.ClaimOpenAIAccountForSessionAsync(context.Background(), "w13c-owned", "acc-1", scope); ok || owner != "" {
		t.Fatalf("refresh failure claim = %q %v", owner, ok)
	}
	if !logger2.HasEvent("redis_openai_session_affinity_remember_failed") {
		t.Fatal("refresh failure must warn")
	}

	// CAS 竞争：第 3 次 Get（第 2 轮尝试）失败。
	getCalls := 0
	racy := &w13cStubRedis{
		getHook: func(key string) (*string, error) {
			getCalls++
			if getCalls >= 3 {
				return nil, errors.New("w13c third read failure")
			}
			return nil, nil
		},
		evalHook: func(script string, keys []string) (any, error) { return int64(0), nil },
	}
	service3, _ := w13cRedisService(t, racy)
	if owner, ok := service3.ClaimOpenAIAccountForSessionAsync(context.Background(), "w13c-racy", "acc-1", scope); ok || owner != "" {
		t.Fatalf("third-read failure claim = %q %v", owner, ok)
	}
}

func TestW13CLocalMigrateAndForgetArms(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	service.RememberOpenAIAccountForSessionLocal("w13c-local-1", "acc-1", scope)

	// 遗忘未知 key / 账号不匹配。
	service.ForgetOpenAIAccountForSession("w13c-unknown", "acc-1")
	service.ForgetOpenAIAccountForSession("w13c-local-1", "acc-other")

	// 本地迁移 happy path。
	result := service.MigrateOpenAIAccountSessionAffinity("acc-1", "acc-2", scope, MigrationOptions{})
	if result.MigratedSessionCount != 1 {
		t.Fatalf("local migrate = %+v", result)
	}
	// 无候选的迁移为空。
	empty := service.MigrateOpenAIAccountSessionAffinity("acc-3", "acc-4", scope, MigrationOptions{})
	if empty.MigratedSessionCount != 0 {
		t.Fatalf("empty migrate = %+v", empty)
	}
	// 非 redis 驱动的 Forget 异步入口回落同步清理。
	if err := service.ForgetOpenAIAccountForSessionAsync(context.Background(), "w13c-local-1", "acc-2"); err != nil {
		t.Fatal(err)
	}
}

func TestW13CPersonalOrderingArms(t *testing.T) {
	service, _, _ := newTestAffinityService(t, nil)
	// 无 key 短路。
	accounts := []gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 0), w13cAccount("a2", 2, 0)}
	if got, err := service.OrderOpenAIAccountsBySessionAffinity(accounts, "", DispatchOrderingOptions{}); err != nil || len(got) != 2 {
		t.Fatalf("empty key order = %v err=%v", got, err)
	}
	// super priority 短路。
	superList := []gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 0), w13cAccount("a2", 2, 0)}
	superList[0].SuperPriorityEnabled = true
	if got := service.orderOpenAIPersonalAccountsBySessionBinding(superList, &SessionBinding{AccountID: "a2"}, nil); got[0].ID != "a1" {
		t.Fatalf("super priority must short-circuit: %v", got)
	}
	// 单账号短路。
	if got := service.orderOpenAIPersonalAccountsBySessionBinding([]gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 0)}, nil, nil); len(got) != 1 {
		t.Fatalf("single = %v", got)
	}
	// 绑定账号缺失短路。
	if got := service.orderOpenAIPersonalAccountsBySessionBinding(accounts, &SessionBinding{AccountID: "zz"}, nil); got[0].ID != "a1" {
		t.Fatalf("missing bound = %v", got)
	}
	// 同层轮转终止：绑定 a2（中间位），质量分更优晋升到 a1，a3 质量更差不参与轮转。
	qualityA1 := 5.0
	qualityA2 := 1.0
	qualityA3 := 9.0
	a1 := w13cAccount("a1", 1, 0)
	a1.QualityScore = &qualityA1
	a2 := w13cAccount("a2", 1, 0)
	a2.QualityScore = &qualityA2
	a3 := w13cAccount("a3", 1, 0)
	a3.QualityScore = &qualityA3
	trio := []gatewayruntimecache.OpenAIAccountSecret{a1, a2, a3}
	ordered := service.orderOpenAIPersonalAccountsBySessionBinding(trio, &SessionBinding{AccountID: "a2"}, nil)
	if ordered[0].ID != "a2" || ordered[1].ID != "a1" {
		t.Fatalf("promotion ordering = %v", ordered)
	}
	// 高并发排序单账号短路。
	if got, err := service.orderOpenAIHighConcurrencyAccounts([]gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 0)}, "k", nil, "", nil); err != nil || len(got) != 1 {
		t.Fatalf("single hc = %v err=%v", got, err)
	}
	// fastFirst 关闭 + 无迁移目标 → personal 亲和排序。
	if got, err := service.orderOpenAIHighConcurrencyAccounts(accounts, "", map[string]any{"fastFirstEnabled": false}, "", nil); err != nil || len(got) != 2 {
		t.Fatalf("fast-first off = %v err=%v", got, err)
	}
	// fastFirst 关闭 + 迁移目标 → hard-busy-last（a1 满载沉底）。
	hardBusyMock := newMockConcurrency()
	hardBusyMock.SetInFlight("a1", AccountInFlightStats{CurrentConcurrency: 9})
	hardBusyService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.Concurrency = hardBusyMock
	})
	if got, err := hardBusyService.orderOpenAIHighConcurrencyAccounts(accounts, "", map[string]any{"fastFirstEnabled": false}, "a2", nil); err != nil || got[0].ID != "a2" {
		t.Fatalf("hard busy last sync = %v err=%v", got, err)
	}
}

func TestW13CHighConcurrencyAsyncRemainingArms(t *testing.T) {
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	// image lane + 非法策略（stats 加载正常）。
	stub := &w13cStubConcurrency{current: map[string]int{"a1": 0, "a2": 0}}
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{sendHook: func(args ...any) (any, error) { return "OK", nil }}
		cfg.Concurrency = stub
	})
	accounts := []gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 0), w13cAccount("a2", 2, 0)}
	if _, err := service.AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(context.Background(), accounts, BusyLaneOptions{
		DispatchOrderingOptions: DispatchOrderingOptions{
			GroupType:        GroupTypeHighConcurrency,
			SchedulingPolicy: map[string]any{"mode": "turbo"},
		},
		RequestLane: RequestLaneImage,
	}); err == nil {
		t.Fatal("bad policy must surface from async image lane")
	}

	// fastFirst 关闭 + 迁移目标存在 → hard-busy-last async，统计生效。
	fullStub := &w13cStubConcurrency{current: map[string]int{"a1": 9, "a2": 0}}
	fullService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{
			getHook: func(key string) (*string, error) {
				if strings.Contains(key, "traffic-migration-preference") {
					value := `{"sourceAccountId":"gone-1","targetAccountId":"a2"}`
					return &value, nil
				}
				return nil, errors.New("w13c binding read failure")
			},
			sendHook: func(args ...any) (any, error) { return "OK", nil },
		}
		cfg.Concurrency = fullStub
	})
	ordered, err := fullService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "", DispatchOrderingOptions{
		GroupType:             GroupTypeHighConcurrency,
		SchedulingPolicy:      map[string]any{"fastFirstEnabled": false},
		TrafficMigrationScope: scope,
	})
	if err != nil || ordered[0].ID != "a2" {
		t.Fatalf("async hard busy last = %v err=%v", ordered, err)
	}

	// 全部空闲：单桶直接原序。
	freeService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{
			getHook: func(key string) (*string, error) {
				if strings.Contains(key, "traffic-migration-preference") {
					value := `{"sourceAccountId":"gone-1","targetAccountId":"a2"}`
					return &value, nil
				}
				return nil, errors.New("w13c binding read failure")
			},
			sendHook: func(args ...any) (any, error) { return "OK", nil },
		}
		cfg.Concurrency = &w13cStubConcurrency{current: map[string]int{"a1": 0, "a2": 0}}
	})
	ordered, err = freeService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "", DispatchOrderingOptions{
		GroupType:             GroupTypeHighConcurrency,
		SchedulingPolicy:      map[string]any{"fastFirstEnabled": false},
		TrafficMigrationScope: scope,
	})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("async all free = %v err=%v", ordered, err)
	}

	// Concurrency 为 nil + fastFirst 开启：stats 空表分支。
	nilConcurrencyService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverRedis
		cfg.Redis = &w13cStubRedis{}
		cfg.Concurrency = nil
	})
	if _, err := nilConcurrencyService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "", DispatchOrderingOptions{
		GroupType: GroupTypeHighConcurrency,
	}); err != nil {
		t.Fatal(err)
	}

	// 本地缓存绑定 + breakAffinityOnSoftLimit 关闭：softBusy=false 分支。
	boundService, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.CacheDriver = CacheDriverMemory
		cfg.Concurrency = &w13cStubConcurrency{current: map[string]int{"a1": 0, "a2": 0}}
	})
	boundService.RememberOpenAIAccountForSessionLocal("w13c-bound", "a1", nil)
	if _, err := boundService.OrderOpenAIAccountsBySessionAffinityAsync(context.Background(), accounts, "w13c-bound", DispatchOrderingOptions{
		GroupType:        GroupTypeHighConcurrency,
		SchedulingPolicy: map[string]any{"breakAffinityOnSoftLimit": false},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestW13CCompareCandidateRemainingArms(t *testing.T) {
	policy := DefaultHighConcurrencyGroupSchedulingPolicy(SchedulingDefaults{GlobalMax: 5000})
	// 模型优先级分歧。
	priority := &GatewayAccountModelPriority{RankByAccountID: map[string]int{"a1": 1, "a2": 2}}
	left := highConcurrencyCandidate{account: w13cAccount("a1", 1, 0), index: 0, softLimit: 5}
	right := highConcurrencyCandidate{account: w13cAccount("a2", 2, 0), index: 1, softLimit: 5}
	if compareHighConcurrencyCandidates(right, left, &policy, false, priority) <= 0 {
		t.Fatal("better model rank must win")
	}
	// 软忙右位。
	busyRight := highConcurrencyCandidate{account: w13cAccount("a2", 1, 5), index: 1, softLimit: 5, softBusy: true}
	if compareHighConcurrencyCandidates(left, busyRight, &policy, false, nil) >= 0 {
		t.Fatal("idle must outrank soft-busy")
	}
	// 亲和右位。
	bound := highConcurrencyCandidate{account: w13cAccount("a2", 1, 0), index: 1, softLimit: 5, affinityAllowed: true}
	if compareHighConcurrencyCandidates(left, bound, &policy, false, nil) <= 0 {
		t.Fatal("non-bound must rank worse than bound")
	}
	// 相同负载率但并发数不同。
	ratioA := highConcurrencyCandidate{account: w13cAccount("a1", 1, 1), index: 0, softLimit: 5}
	ratioB := highConcurrencyCandidate{account: w13cAccount("a2", 1, 2), index: 1, softLimit: 10}
	if compareHighConcurrencyCandidates(ratioB, ratioA, &policy, false, nil) <= 0 {
		t.Fatal("higher concurrency with equal ratio must rank worse")
	}
	// 迁移偏好排序：目标在更优账号之后不动。
	accounts := []gatewayruntimecache.OpenAIAccountSecret{w13cAccount("a1", 1, 0), w13cAccount("a2", 2, 0)}
	rankedPriority := &GatewayAccountModelPriority{RankByAccountID: map[string]int{"a1": 1, "a2": 2}}
	if got := orderOpenAIAccountsByTrafficMigrationPreference(accounts, "a2", rankedPriority); got[0].ID != "a1" {
		t.Fatalf("unmovable target = %v", got)
	}
	// deleteSetValue 空集合守卫。
	deleteSetValue(map[string]map[string]struct{}{}, "a", "k")
}

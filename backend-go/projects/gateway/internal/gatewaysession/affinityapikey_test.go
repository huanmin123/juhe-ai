package gatewaysession

// 账户内 API Key 会话亲和（账户内APIKey会话亲和设计 §9）：绑定记录指纹字段
// 编解码、sticky 读 / Key 指纹写回的进程内与 Redis 双路径、CAS 竞态臂。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
)

// affinityApiKeyTestFingerprint 是恰好 64 位小写 hex 的合法指纹样例。
const affinityApiKeyTestFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestValidUpstreamApiKeyFingerprintFormat(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "valid", value: affinityApiKeyTestFingerprint, want: true},
		{name: "short", value: affinityApiKeyTestFingerprint[:63], want: false},
		{name: "long", value: affinityApiKeyTestFingerprint + "0", want: false},
		{name: "uppercase", value: strings.ToUpper(affinityApiKeyTestFingerprint), want: false},
		{name: "non_hex", value: strings.Repeat("g", 64), want: false},
		{name: "empty", value: "", want: false},
	}
	for _, testCase := range cases {
		if got := ValidUpstreamApiKeyFingerprint(testCase.value); got != testCase.want {
			t.Fatalf("%s: ValidUpstreamApiKeyFingerprint = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestSessionBindingJSONUpstreamApiKeyFingerprint(t *testing.T) {
	// 非空才写入。
	withField := marshalSessionBinding(SessionBinding{AccountID: "acc-1", UpstreamApiKeyFingerprint: affinityApiKeyTestFingerprint})
	if !strings.Contains(withField, `"upstreamApiKeyFingerprint":"`+affinityApiKeyTestFingerprint+`"`) {
		t.Fatalf("marshal 应写入指纹字段：%s", withField)
	}
	without := marshalSessionBinding(SessionBinding{AccountID: "acc-1"})
	if strings.Contains(without, "upstreamApiKeyFingerprint") {
		t.Fatalf("空指纹不应写入字段：%s", without)
	}

	// 合法指纹解析回读。
	parsed := parseRedisSessionBinding(withField)
	if parsed == nil || parsed.AccountID != "acc-1" || parsed.UpstreamApiKeyFingerprint != affinityApiKeyTestFingerprint {
		t.Fatalf("合法指纹解析失败：%+v", parsed)
	}

	// 存量 JSON 无该字段 → 零值兼容。
	legacy := parseRedisSessionBinding(`{"accountId":"acc-1"}`)
	if legacy == nil || legacy.AccountID != "acc-1" || legacy.UpstreamApiKeyFingerprint != "" {
		t.Fatalf("存量 JSON 兼容失败：%+v", legacy)
	}

	// 字段非法（长度/字符集/类型）只丢该字段，绑定其余部分保留。
	invalidCases := map[string]string{
		"wrong_length": `{"accountId":"acc-1","upstreamApiKeyFingerprint":"abc"}`,
		"non_hex":      `{"accountId":"acc-1","upstreamApiKeyFingerprint":"` + strings.Repeat("g", 64) + `"}`,
		"uppercase":    `{"accountId":"acc-1","upstreamApiKeyFingerprint":"` + strings.ToUpper(affinityApiKeyTestFingerprint) + `"}`,
		"number":       `{"accountId":"acc-1","upstreamApiKeyFingerprint":42}`,
	}
	for name, raw := range invalidCases {
		parsed := parseRedisSessionBinding(raw)
		if parsed == nil || parsed.AccountID != "acc-1" {
			t.Fatalf("%s: 绑定 accountId 应保留：%+v", name, parsed)
		}
		if parsed.UpstreamApiKeyFingerprint != "" {
			t.Fatalf("%s: 非法指纹应被丢弃，实际 %q", name, parsed.UpstreamApiKeyFingerprint)
		}
	}

	// JSON 语法损坏仍整条作废（既有行为不变）。
	if parsed := parseRedisSessionBinding(`{`); parsed != nil {
		t.Fatalf("语法损坏应返回 nil：%+v", parsed)
	}
}

func TestStickyAndRememberApiKeyFingerprintLocal(t *testing.T) {
	service, clock, _ := newTestAffinityService(t, nil)
	ctx := context.Background()
	key := "aff_v1_sticky_local"
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	// 无绑定读 → 空串。
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != "" {
		t.Fatalf("无绑定黏性读应为空，实际 %q", got)
	}

	// 未绑定写回 → 跳过，不得创建绑定。
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", affinityApiKeyTestFingerprint)
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != "" {
		t.Fatalf("Key 写回不得创建绑定，黏性读 = %q", got)
	}

	// claim 建立账户绑定后：异 owner 写回跳过，同 owner 写回生效。
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-a", scope); !ok {
		t.Fatal("claim 失败")
	}
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-b", affinityApiKeyTestFingerprint)
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-b"); got != "" {
		t.Fatalf("异 owner 写回应被跳过，黏性读 = %q", got)
	}
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", affinityApiKeyTestFingerprint)
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != affinityApiKeyTestFingerprint {
		t.Fatalf("同 owner 写回后黏性读 = %q", got)
	}

	// 非法指纹在入口被拒绝（写回无副作用）。
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", "short")
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != affinityApiKeyTestFingerprint {
		t.Fatalf("非法指纹应被拒绝，黏性读 = %q", got)
	}

	// 同指纹重复写回刷新 TTL：55 分钟处再写回后，总时长 110 分钟仍黏住
	//（TTL 60 分钟，未刷新应在 60 分钟处过期）。
	clock.Advance(55 * time.Minute)
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", affinityApiKeyTestFingerprint)
	clock.Advance(55 * time.Minute)
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != affinityApiKeyTestFingerprint {
		t.Fatalf("写回应刷新绑定 TTL，实际黏性读 = %q", got)
	}

	// 同 owner 更新保留 Scope 与流量迁移标记（设计 §6.3：只替换指纹字段）。
	service.mu.Lock()
	service.setSessionAffinityBindingLocked(key, SessionBinding{
		AccountID:                 "account-a",
		Scope:                     scope,
		TrafficMigrationPreferred: true,
	})
	service.mu.Unlock()
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", affinityApiKeyTestFingerprint)
	service.mu.Lock()
	preserved := service.sessionAffinityCacheGetLocked(key)
	service.mu.Unlock()
	if preserved == nil || preserved.UpstreamApiKeyFingerprint != affinityApiKeyTestFingerprint {
		t.Fatalf("写回后指纹应更新：%+v", preserved)
	}
	if preserved.Scope == nil || preserved.Scope.GroupID != scope.GroupID {
		t.Fatalf("写回应保留 Scope，实际 %+v", preserved.Scope)
	}
	if !preserved.TrafficMigrationPreferred {
		t.Fatal("写回应保留 TrafficMigrationPreferred")
	}

	// Forget 后同账户重新 Claim：晚到的协议成功回调可更新现存新绑定
	// （设计 §3/§8——Forget 不栅栏在途回调，回调只更新线性化时仍存在的绑定）。
	if err := service.ForgetOpenAIAccountForSessionAsync(ctx, key, "account-a"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-a", scope); !ok {
		t.Fatal("re-claim 失败")
	}
	const reboundFingerprint = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", reboundFingerprint)
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != reboundFingerprint {
		t.Fatalf("重 claim 后晚到回调应更新新绑定，黏性读 = %q", got)
	}

	// Forget 后整条绑定（含指纹）删除。
	if err := service.ForgetOpenAIAccountForSessionAsync(ctx, key, "account-a"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != "" {
		t.Fatalf("forget 后黏性读应为空，实际 %q", got)
	}

	// 流量迁移为字段级重建：指纹不随迁移保留（设计 §7）。
	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-a", scope); !ok {
		t.Fatal("re-claim 失败")
	}
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", affinityApiKeyTestFingerprint)
	service.MigrateOpenAIAccountSessionAffinity("account-a", "account-b", nil, MigrationOptions{})
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-b"); got != "" {
		t.Fatalf("迁移不得携带旧 Key 指纹，黏性读 = %q", got)
	}
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != "" {
		t.Fatalf("迁移后原 owner 黏性读应为空，实际 %q", got)
	}
}

func TestStickyAndRememberApiKeyFingerprintRedis(t *testing.T) {
	server := miniredis.RunT(t)
	service, _, _ := newTestAffinityService(t, func(cfg *AffinityConfig) {
		cfg.CacheDriver = CacheDriverRedis
		cfg.RuntimeStateDriver = RuntimeStateDriverRedis
		cfg.Redis = newLazyRedisClient("redis://" + server.Addr())
	})
	ctx := context.Background()
	key := "aff_v1_sticky_redis"
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	if _, ok := service.ClaimOpenAIAccountForSessionAsync(ctx, key, "account-a", scope); !ok {
		t.Fatal("claim 失败")
	}
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", affinityApiKeyTestFingerprint)
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != affinityApiKeyTestFingerprint {
		t.Fatalf("Redis 路径同 owner 写回后黏性读 = %q", got)
	}

	// 异 owner 写回跳过。
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-b", affinityApiKeyTestFingerprint)
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-b"); got != "" {
		t.Fatalf("异 owner 写回应被跳过，黏性读 = %q", got)
	}

	// Forget 后整条删除；写回不得重建。
	if err := service.ForgetOpenAIAccountForSessionAsync(ctx, key, "account-a"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "account-a", affinityApiKeyTestFingerprint)
	if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "account-a"); got != "" {
		t.Fatalf("forget 后写回不得重建绑定，黏性读 = %q", got)
	}
}

func TestStickyAndRememberApiKeyFingerprintRedisArms(t *testing.T) {
	ctx := context.Background()
	key := "aff_v1_sticky_arms"
	validRecord := `{"accountId":"acc-1","scope":{"systemAccountId":"sys-1","groupId":"grp-1"}}`
	ownerChangedRecord := `{"accountId":"acc-2"}`

	t.Run("missing record skips without eval", func(t *testing.T) {
		evalCalls := 0
		stub := &w13cStubRedis{
			getHook:  func(string) (*string, error) { return nil, nil },
			evalHook: func(string, []string) (any, error) { evalCalls++; return int64(1), nil },
		}
		service, logger := w13cRedisService(t, stub)
		service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "acc-1", affinityApiKeyTestFingerprint)
		if evalCalls != 0 {
			t.Fatalf("记录缺失不得执行 CAS，eval 次数 = %d", evalCalls)
		}
		if logger.HasEvent("redis_openai_session_affinity_api_key_write_failed") {
			t.Fatal("记录缺失是正常跳过，不应告警")
		}
	})

	t.Run("owner mismatch skips", func(t *testing.T) {
		evalCalls := 0
		stub := &w13cStubRedis{
			getHook:  func(string) (*string, error) { raw := ownerChangedRecord; return &raw, nil },
			evalHook: func(string, []string) (any, error) { evalCalls++; return int64(1), nil },
		}
		service, _ := w13cRedisService(t, stub)
		service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "acc-1", affinityApiKeyTestFingerprint)
		if evalCalls != 0 {
			t.Fatalf("owner 不匹配不得执行 CAS，eval 次数 = %d", evalCalls)
		}
	})

	t.Run("cas conflict then owner change abandons", func(t *testing.T) {
		evalCalls := 0
		getCalls := 0
		stub := &w13cStubRedis{
			getHook: func(string) (*string, error) {
				getCalls++
				if getCalls == 1 {
					raw := validRecord
					return &raw, nil
				}
				raw := ownerChangedRecord
				return &raw, nil
			},
			evalHook: func(string, []string) (any, error) { evalCalls++; return int64(0), nil },
		}
		service, logger := w13cRedisService(t, stub)
		service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "acc-1", affinityApiKeyTestFingerprint)
		if evalCalls != 1 {
			t.Fatalf("首轮冲突后重读发现 owner 变化应放弃，eval 次数 = %d", evalCalls)
		}
		if logger.HasEvent("redis_openai_session_affinity_api_key_cas_conflict") {
			t.Fatal("owner 变化属正常放弃，不应计 CAS 冲突告警")
		}
	})

	t.Run("double cas conflict warns and gives up", func(t *testing.T) {
		evalCalls := 0
		stub := &w13cStubRedis{
			getHook:  func(string) (*string, error) { raw := validRecord; return &raw, nil },
			evalHook: func(string, []string) (any, error) { evalCalls++; return int64(0), nil },
		}
		service, logger := w13cRedisService(t, stub)
		service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "acc-1", affinityApiKeyTestFingerprint)
		if evalCalls != 2 {
			t.Fatalf("CAS 上限为 2 轮，eval 次数 = %d", evalCalls)
		}
		if !logger.HasEvent("redis_openai_session_affinity_api_key_cas_conflict") {
			t.Fatal("两轮冲突后应计 CAS 冲突告警")
		}
	})

	t.Run("eval error warns write failure", func(t *testing.T) {
		stub := &w13cStubRedis{
			getHook:  func(string) (*string, error) { raw := validRecord; return &raw, nil },
			evalHook: func(string, []string) (any, error) { return nil, errors.New("stub eval down") },
		}
		service, logger := w13cRedisService(t, stub)
		service.RememberOpenAIApiKeyFingerprintAsync(ctx, key, "acc-1", affinityApiKeyTestFingerprint)
		if !logger.HasEvent("redis_openai_session_affinity_api_key_write_failed") {
			t.Fatal("Eval 错误应计写回失败告警")
		}
	})

	t.Run("sticky read failure degrades with warn", func(t *testing.T) {
		stub := &w13cStubRedis{}
		service, logger := w13cRedisService(t, stub)
		if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "acc-1"); got != "" {
			t.Fatalf("读取失败应降级为空串，实际 %q", got)
		}
		if !logger.HasEvent("redis_openai_session_affinity_read_failed") {
			t.Fatal("读取失败应按既有读取失败路径告警")
		}
	})

	t.Run("sticky read does not refresh ttl", func(t *testing.T) {
		evalCalls := 0
		stub := &w13cStubRedis{
			getHook:  func(string) (*string, error) { raw := validRecord; return &raw, nil },
			evalHook: func(string, []string) (any, error) { evalCalls++; return int64(1), nil },
		}
		service, _ := w13cRedisService(t, stub)
		if got := service.StickyOpenAIApiKeyFingerprintAsync(ctx, key, "acc-1"); got != "" {
			t.Fatalf("无指纹记录黏性读应为空，实际 %q", got)
		}
		if evalCalls != 0 {
			t.Fatalf("黏性读不得刷新 TTL（无 Eval），eval 次数 = %d", evalCalls)
		}
	})
}

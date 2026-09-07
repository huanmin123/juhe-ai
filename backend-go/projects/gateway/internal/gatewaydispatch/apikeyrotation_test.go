package gatewaydispatch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// BUG-0174 波1 回归：B-1 指纹统一、B-3 轮转策略族、M-7 池隔离谓词。
// Node 基线：migration-backup/node/final-archive/backend/src/storage/account-api-key-rotation.ts。

const testRotationSecret = "juhe-ai-test-secret"

// newRotationTestEngine builds an engine with the shared test secret and a
// fresh in-process rotation counter per test (deterministic sequences).
func newRotationTestEngine() *Engine {
	engine := &Engine{Config: DefaultEngineConfig()}
	engine.Config.Secret = testRotationSecret
	engine.KeyRotation = newInProcessAPIKeyRotationCounter()
	engine.Cache = &fakeCache{}
	return engine
}

// poolCredentials builds api_keys credentials with an optional strategy and
// weights (Node credentials.api_keys / api_key_strategy / api_key_weights).
func poolCredentials(keys []string, strategy string, weights []any) map[string]any {
	credentials := map[string]any{}
	rawKeys := make([]any, len(keys))
	for index, key := range keys {
		rawKeys[index] = key
	}
	credentials["api_keys"] = rawKeys
	if strategy != "" {
		credentials["api_key_strategy"] = strategy
	}
	if weights != nil {
		credentials["api_key_weights"] = weights
	}
	return credentials
}

// poolAccount builds an api_key candidate carrying the credentials pool.
func poolAccount(id, providerCode, protocolCode, protocolVersion string, credentials map[string]any) AccountCandidate {
	account := AccountCandidate{
		ID:             id,
		Name:           "账号 " + id,
		Type:           "api_key",
		Status:         "active",
		ProviderCode:   providerCode,
		ProtocolCode:   protocolCode,
		ProtocolVersion: protocolVersion,
		Credentials:    credentials,
	}
	if keys, ok := credentials["api_keys"].([]any); ok {
		account.APIKeys = make([]string, 0, len(keys))
		for _, key := range keys {
			if text, ok := key.(string); ok {
				account.APIKeys = append(account.APIKeys, text)
			}
		}
	}
	if single, ok := credentials["api_key"].(string); ok {
		account.APIKey = single
	}
	return account
}

// TestAPIKeyFingerprintHMACNotBareSHA256（B-1）：同一 Key 的两种算法值不同，
// dispatch 指纹与 accountkeystates 侧（探活/DB 池写入方）逐字节一致。
func TestAPIKeyFingerprintHMACNotBareSHA256(t *testing.T) {
	key := "sk-cross-check-key"
	secret := testRotationSecret

	got := apiKeyFingerprint(secret, key)
	bareSHA256 := sha256HexBytes([]byte(key))
	if got == bareSHA256 {
		t.Fatalf("裸 sha256 与 HMAC-SHA256(secret,key) 不应相等：%s", got)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("指纹不应为空")
	}

	// 独立复算 HMAC（测试内标准库，不经过被测函数路径）。
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(key))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("dispatch 指纹 %s != 独立 HMAC 复算 %s", got, want)
	}

	// 交叉断言：accountkeystates.Store.FingerprintAPIKey（probe/DB 写入方）。
	store, err := accountkeystates.NewStore(accountkeystates.Config{DB: &sql.DB{}, Secret: secret})
	if err != nil {
		t.Fatalf("accountkeystates.NewStore: %v", err)
	}
	if cross := store.FingerprintAPIKey(key); cross != got {
		t.Fatalf("dispatch 指纹 %s != accountkeystates 指纹 %s", got, cross)
	}
}

// scriptCounter 是脚本化计数器：按序返回预设索引，验证 rr/weighted 的取模
// 接线（模由选择器负责的分支除外）。
type scriptCounter struct {
	indices []int
	calls   []string
}

func (c *scriptCounter) NextIndex(_ context.Context, _ string, strategy string, modulo int) (int, error) {
	c.calls = append(c.calls, strategy)
	if len(c.indices) == 0 {
		return 0, nil
	}
	index := c.indices[0]
	c.indices = c.indices[1:]
	return index % modulo, nil
}

// TestSelectAccountAPIKeyFailoverAlwaysFirst（B-3）：failover 恒首把可用。
func TestSelectAccountAPIKeyFailoverAlwaysFirst(t *testing.T) {
	credentials := poolCredentials([]string{"k0", "k1", "k2"}, accountAPIKeyStrategyFailover, nil)
	entries := accountApiKeyEntries(testRotationSecret, credentials)
	if len(entries) != 3 {
		t.Fatalf("entries 数量 = %d，期望 3", len(entries))
	}
	counter := &scriptCounter{}
	for round := 0; round < 5; round++ {
		selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
			AccountID:   "acc-1",
			credentials: credentials,
			entries:     entries,
			counter:     counter,
		})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if selected == nil || selected.key != "k0" {
			t.Fatalf("round %d: failover 应恒选 k0，实际 %+v", round, selected)
		}
	}
	if len(counter.calls) != 0 {
		t.Fatalf("failover 不应消耗计数器，实际 %v", counter.calls)
	}
}

// TestSelectAccountAPIKeyRoundRobinSequence（B-3）：rr 计数轮转序列
// k0→k1→k2→k0（对齐 Redis INCR (value-1)%modulo）。
func TestSelectAccountAPIKeyRoundRobinSequence(t *testing.T) {
	credentials := poolCredentials([]string{"k0", "k1", "k2"}, accountAPIKeyStrategyRoundRobin, nil)
	entries := accountApiKeyEntries(testRotationSecret, credentials)
	counter := newInProcessAPIKeyRotationCounter()
	want := []string{"k0", "k1", "k2", "k0", "k1", "k2"}
	for round, expected := range want {
		selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
			AccountID:   "acc-rr",
			credentials: credentials,
			entries:     entries,
			counter:     counter,
		})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if selected == nil || selected.key != expected {
			t.Fatalf("round %d: rr 序列应选 %s，实际 %+v", round, expected, selected)
		}
	}
}

// TestSelectAccountAPIKeyDefaultStrategyIsRoundRobin（B-3）：未声明
// api_key_strategy 时按 round_robin（归档 :177-181 缺省分支）。
func TestSelectAccountAPIKeyDefaultStrategyIsRoundRobin(t *testing.T) {
	credentials := poolCredentials([]string{"k0", "k1"}, "", nil)
	entries := accountApiKeyEntries(testRotationSecret, credentials)
	counter := newInProcessAPIKeyRotationCounter()
	want := []string{"k0", "k1", "k0"}
	for round, expected := range want {
		selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
			AccountID:   "acc-default",
			credentials: credentials,
			entries:     entries,
			counter:     counter,
		})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if selected == nil || selected.key != expected {
			t.Fatalf("round %d: 缺省策略应按 rr 选 %s，实际 %+v", round, expected, selected)
		}
	}
}

// TestSelectAccountAPIKeyWeightedDistribution（B-3）：weighted 按权重游标
// 落位，权重 [3,1]（total=4）序列 k0,k0,k0,k1（归档 :213-224）。
func TestSelectAccountAPIKeyWeightedDistribution(t *testing.T) {
	credentials := poolCredentials([]string{"k0", "k1"}, accountAPIKeyStrategyWeightedRoundRobin, []any{float64(3), float64(1)})
	entries := accountApiKeyEntries(testRotationSecret, credentials)
	if entries[0].weight != 3 || entries[1].weight != 1 {
		t.Fatalf("权重归一失败：%+v", entries)
	}
	counter := &scriptCounter{indices: []int{0, 1, 2, 3}}
	want := []string{"k0", "k0", "k0", "k1"}
	for round, expected := range want {
		selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
			AccountID:   "acc-weighted",
			credentials: credentials,
			entries:     entries,
			counter:     counter,
		})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if selected == nil || selected.key != expected {
			t.Fatalf("round %d: weighted 应选 %s，实际 %+v", round, expected, selected)
		}
	}
	if len(counter.calls) != 4 || counter.calls[0] != apiKeyRotationScopeWeighted {
		t.Fatalf("weighted 应走 weighted 计数器域，实际 %v", counter.calls)
	}
}

// TestSelectAccountAPIKeyNonActiveStatesExcluded（B-3）：states 过滤
// status !== 'active'（水合折叠为 Disabled），unverified 全剔除；指纹按
// HMAC 计算才能命中（B-1 修复后 states 才生效）。
func TestSelectAccountAPIKeyNonActiveStatesExcluded(t *testing.T) {
	credentials := poolCredentials([]string{"k0", "k1", "k2"}, accountAPIKeyStrategyFailover, nil)
	entries := accountApiKeyEntries(testRotationSecret, credentials)
	fingerprintOf := func(key string) string { return apiKeyFingerprint(testRotationSecret, key) }

	cases := []struct {
		name   string
		status string // 水合语义：status != 'active' → Disabled=true
	}{
		{name: "disabled", status: "disabled"},
		{name: "unverified", status: "unverified"},
		{name: "rate_limited", status: "rate_limited"},
		{name: "temporary_unavailable", status: "temporary_unavailable"},
		{name: "error", status: "error"},
	}
	for _, testCase := range cases {
		states := []gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{
			{Fingerprint: fingerprintOf("k0"), Disabled: testCase.status != "active"},
		}
		selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
			AccountID:     "acc-states",
			credentials:   credentials,
			RuntimeStates: states,
			entries:       entries,
			counter:       newInProcessAPIKeyRotationCounter(),
		})
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if selected == nil || selected.key != "k1" {
			t.Fatalf("%s(status=%s): k0 应被剔除并选 k1，实际 %+v", testCase.name, testCase.status, selected)
		}
	}

	// 全部非 active → 无候选。
	allDisabled := []gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{
		{Fingerprint: fingerprintOf("k0"), Disabled: true},
		{Fingerprint: fingerprintOf("k1"), Disabled: true},
		{Fingerprint: fingerprintOf("k2"), Disabled: true},
	}
	selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
		AccountID:     "acc-states",
		credentials:   credentials,
		RuntimeStates: allDisabled,
		entries:       entries,
		counter:       newInProcessAPIKeyRotationCounter(),
	})
	if err != nil {
		t.Fatalf("all disabled: %v", err)
	}
	if selected != nil {
		t.Fatalf("全非 active 应无候选，实际 %+v", selected)
	}

	// active 状态不剔除。
	activeStates := []gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{
		{Fingerprint: fingerprintOf("k0"), Disabled: false},
	}
	selected, err = selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
		AccountID:     "acc-states",
		credentials:   credentials,
		RuntimeStates: activeStates,
		entries:       entries,
		counter:       newInProcessAPIKeyRotationCounter(),
	})
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if selected == nil || selected.key != "k0" {
		t.Fatalf("active 状态不应剔除，实际 %+v", selected)
	}
}

// TestSelectAccountAPIKeyContinueAfterFingerprint（B-3 continuation，归档
// :254-267）：从上一指纹的下一位环回找第一个可用候选。
func TestSelectAccountAPIKeyContinueAfterFingerprint(t *testing.T) {
	credentials := poolCredentials([]string{"k0", "k1", "k2"}, accountAPIKeyStrategyWeightedRoundRobin, nil)
	entries := accountApiKeyEntries(testRotationSecret, credentials)
	fingerprintOf := func(key string) string { return apiKeyFingerprint(testRotationSecret, key) }

	selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
		AccountID:                "acc-cont",
		credentials:              credentials,
		ContinueAfterFingerprint: fingerprintOf("k1"),
		entries:                  entries,
		counter:                  newInProcessAPIKeyRotationCounter(),
	})
	if err != nil {
		t.Fatalf("continuation: %v", err)
	}
	if selected == nil || selected.key != "k2" {
		t.Fatalf("k1 之后应选 k2，实际 %+v", selected)
	}

	// 未知指纹回退首候选；continuation 优先于策略（不消耗计数器）。
	selected, err = selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
		AccountID:                "acc-cont",
		credentials:              credentials,
		ContinueAfterFingerprint: fingerprintOf("missing"),
		entries:                  entries,
		counter:                  &scriptCounter{},
	})
	if err != nil {
		t.Fatalf("continuation fallback: %v", err)
	}
	if selected == nil || selected.key != "k0" {
		t.Fatalf("未知指纹应回退 k0，实际 %+v", selected)
	}
}

// TestSelectAccountAPIKeyCounterErrorPropagates：计数器错误按 Node 语义
// 传播（selectAccountRuntimeApiKeyEntryAsync 抛错冒泡）。
func TestSelectAccountAPIKeyCounterErrorPropagates(t *testing.T) {
	credentials := poolCredentials([]string{"k0", "k1"}, accountAPIKeyStrategyRoundRobin, nil)
	entries := accountApiKeyEntries(testRotationSecret, credentials)
	counterErr := errors.New("redis counter down")
	_, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
		AccountID:   "acc-err",
		credentials: credentials,
		entries:     entries,
		counter:     errCounter{err: counterErr},
	})
	if !errors.Is(err, counterErr) {
		t.Fatalf("计数器错误应传播，实际 %v", err)
	}
}

type errCounter struct{ err error }

func (c errCounter) NextIndex(context.Context, string, string, int) (int, error) {
	return 0, c.err
}

// TestAccountAPIKeyPoolIsolationMatrix（M-7）：谓词对齐 Node :141-171，
// deepseek/glm/gemini/anthropic 多 Key 账户现在隔离；OAuth 不再入池判定。
func TestAccountAPIKeyPoolIsolationMatrix(t *testing.T) {
	twoKeys := []string{"k0", "k1"}
	cases := []struct {
		name            string
		accountType     string
		providerCode    string
		protocolCode    string
		protocolVersion string
		keys            []string
		want            bool
	}{
		{name: "openai api_key 池", accountType: "api_key", providerCode: "openai", keys: twoKeys, want: true},
		{name: "gpt api_key 池", accountType: "api_key", providerCode: "GPT", keys: twoKeys, want: true},
		{name: "deepseek 池（修复前不隔离）", accountType: "api_key", providerCode: "deepseek", keys: twoKeys, want: true},
		{name: "glm 池（修复前不隔离）", accountType: "api_key", providerCode: "glm", keys: twoKeys, want: true},
		{name: "gemini 池（修复前不隔离）", accountType: "api_key", providerCode: "gemini", keys: twoKeys, want: true},
		{name: "anthropic 供应商池（修复前不隔离）", accountType: "api_key", providerCode: "anthropic", keys: twoKeys, want: true},
		{name: "anthropic 协议 profile 池（修复前不隔离）", accountType: "api_key", providerCode: "vendor-x", protocolCode: "anthropic", protocolVersion: "v1", keys: twoKeys, want: true},
		{name: "anthropic 协议版本不符", accountType: "api_key", providerCode: "vendor-x", protocolCode: "anthropic", protocolVersion: "v2", keys: twoKeys, want: false},
		{name: "不受支持供应商", accountType: "api_key", providerCode: "unknown-vendor", keys: twoKeys, want: false},
		{name: "单 Key 不隔离", accountType: "api_key", providerCode: "deepseek", keys: []string{"k0"}, want: false},
		{name: "oauth 类型不入池判定（旧 gpt+openai+OAuth 分支废弃）", accountType: "oauth", providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1", keys: twoKeys, want: false},
		{name: "去重后单 Key 不隔离", accountType: "api_key", providerCode: "openai", keys: []string{" k1 ", "k1"}, want: false},
	}
	for _, testCase := range cases {
		credentials := poolCredentials(testCase.keys, "", nil)
		account := poolAccount("acc-matrix", testCase.providerCode, testCase.protocolCode, testCase.protocolVersion, credentials)
		account.Type = testCase.accountType
		entries := accountApiKeyEntries(testRotationSecret, credentials)
		if got := isAccountApiKeyPoolIsolationEnabled(account, entries); got != testCase.want {
			t.Fatalf("%s: isAccountApiKeyPoolIsolationEnabled = %v，期望 %v", testCase.name, got, testCase.want)
		}
	}
}

// TestAccountAPIKeyEntriesNodeAlignment：entries 构造对齐归档 :117-139
// （去空白、去重保原始 index、api_keys 空数组回落 api_key、权重归一）。
func TestAccountAPIKeyEntriesNodeAlignment(t *testing.T) {
	credentials := map[string]any{
		"api_keys":        []any{" k0 ", "k1", "", 42, "k0"},
		"api_key_weights": []any{float64(50), float64(2.5), float64(101), float64(0)},
	}
	entries := accountApiKeyEntries(testRotationSecret, credentials)
	if len(entries) != 2 {
		t.Fatalf("去空白去重后应剩 2 条，实际 %d：%+v", len(entries), entries)
	}
	if entries[0].key != "k0" || entries[0].index != 0 || entries[0].weight != 50 {
		t.Fatalf("entries[0] 应为 (k0,index=0,weight=50)，实际 %+v", entries[0])
	}
	if entries[1].key != "k1" || entries[1].index != 1 || entries[1].weight != 1 {
		t.Fatalf("entries[1] 应为 (k1,index=1,weight=1)（2.5 非整数回落 1），实际 %+v", entries[1])
	}
	if entries[0].fingerprint != apiKeyFingerprint(testRotationSecret, "k0") {
		t.Fatalf("entries 指纹应为 HMAC(k0)，实际 %s", entries[0].fingerprint)
	}

	// api_keys 空数组回落单 api_key（归档 :118-120）。
	fallback := map[string]any{"api_keys": []any{}, "api_key": "solo"}
	entries = accountApiKeyEntries(testRotationSecret, fallback)
	if len(entries) != 1 || entries[0].key != "solo" {
		t.Fatalf("空 api_keys 应回落 api_key，实际 %+v", entries)
	}
}

// TestSelectAccountApiKeyForDispatchRotationEndToEnd：引擎入口端到端——
// deepseek 双 Key 账户现在隔离（SelectedAPIKeyFingerprint 非空且为 HMAC），
// rr 策略在多次调用间轮转。
func TestSelectAccountApiKeyForDispatchRotationEndToEnd(t *testing.T) {
	engine := newRotationTestEngine()
	credentials := poolCredentials([]string{"k0", "k1"}, accountAPIKeyStrategyRoundRobin, nil)
	account := poolAccount("acc-e2e", "deepseek", "", "", credentials)

	fingerprintOf := func(key string) string { return apiKeyFingerprint(testRotationSecret, key) }
	seen := make(map[string]bool)
	for round := 0; round < 4; round++ {
		selected, ok, err := engine.SelectAccountApiKeyForDispatch(context.Background(), account, SelectApiKeyOptions{})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if !ok {
			t.Fatalf("round %d: 选择不应失败", round)
		}
		if selected.SelectedAPIKeyFingerprint == nil {
			t.Fatalf("round %d: 池隔离账户必须携带 SelectedAPIKeyFingerprint（M-7 修复后 deepseek 入池）", round)
		}
		fingerprint := *selected.SelectedAPIKeyFingerprint
		if fingerprint != fingerprintOf(selected.APIKey) {
			t.Fatalf("round %d: 指纹 %s 与所选 Key %s 不一致", round, fingerprint, selected.APIKey)
		}
		seen[selected.APIKey] = true
	}
	if !seen["k0"] || !seen["k1"] {
		t.Fatalf("rr 应在多次选择中覆盖两把 Key，实际 %v", seen)
	}

	// 交叉断言：SelectedAPIKeyFingerprint 与 accountkeystates 侧一致。
	store, err := accountkeystates.NewStore(accountkeystates.Config{DB: &sql.DB{}, Secret: testRotationSecret})
	if err != nil {
		t.Fatalf("accountkeystates.NewStore: %v", err)
	}
	if store.FingerprintAPIKey("k0") != fingerprintOf("k0") {
		t.Fatal("dispatch 与 accountkeystates 的 k0 指纹应一致")
	}
}

// TestInProcessAPIKeyRotationCounter：进程内回退计数器的模回环与键域隔离。
func TestInProcessAPIKeyRotationCounter(t *testing.T) {
	counter := newInProcessAPIKeyRotationCounter()
	want := []int{0, 1, 2, 0, 1}
	for round, expected := range want {
		got, err := counter.NextIndex(context.Background(), "acc-1", apiKeyRotationScopeRoundRobin, 3)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if got != expected {
			t.Fatalf("round %d: 计数索引 = %d，期望 %d", round, got, expected)
		}
	}
	// modulo<=0 契约返回 0。
	if got, err := counter.NextIndex(context.Background(), "acc-1", apiKeyRotationScopeRoundRobin, 0); err != nil || got != 0 {
		t.Fatalf("modulo=0 应返回 (0,nil)，实际 (%d,%v)", got, err)
	}
	// 不同 (accountID, strategy) 键域独立。
	if got, _ := counter.NextIndex(context.Background(), "acc-2", apiKeyRotationScopeRoundRobin, 3); got != 0 {
		t.Fatalf("新键域应从 0 开始，实际 %d", got)
	}
	if got, _ := counter.NextIndex(context.Background(), "acc-1", apiKeyRotationScopeWeighted, 5); got != 0 {
		t.Fatalf("不同 strategy 键域应独立，实际 %d", got)
	}
}

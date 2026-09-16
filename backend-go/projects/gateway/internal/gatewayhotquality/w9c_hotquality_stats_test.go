package gatewayhotquality

import (
	"context"
	"errors"
	"testing"
)

func TestW9CExplorationRedisAccrueArms(t *testing.T) {
	ctx := context.Background()
	goodReply := `{"status":"applied","state":{"poolKey":"pool","credit":1,"cursor":0,"reservations":[],"cooldownUntilMsByRuntimeKey":{},"accruedTokens":["tok-1"],"settledReservationIds":[],"expiresAtMs":1000000}}`
	runner := newMockScriptRunner(t, []mockScriptCall{
		{scriptMarker: "local operation = ARGV[5]", reply: goodReply},
	})
	store := newW9CExplorationRedisStore(t, runner)
	state, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "pool", AccrualToken: "tok-1", Eligible: true})
	if err != nil || state == nil || state.Credit != 1 || len(state.AccruedTokens) != 1 {
		t.Fatalf("accrue = %+v, %v", state, err)
	}
	if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "pool", AccrualToken: "  "}); err == nil {
		t.Fatal("empty accrual token must be rejected")
	}
	if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "  ", AccrualToken: "tok"}); err == nil {
		t.Fatal("empty pool key must be rejected")
	}
	negative := int64(-2)
	if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "p", AccrualToken: "t", NowMs: &negative}); err == nil {
		t.Fatal("negative now must be rejected")
	}
}

func TestW9CRedisStatsArms(t *testing.T) {
	ctx := context.Background()
	build := func(t *testing.T, reply interface{}, scriptErr error) *RedisHotQualityStore {
		t.Helper()
		runner := newMockScriptRunner(t, []mockScriptCall{
			{scriptMarker: "ZREMRANGEBYSCORE", reply: reply, err: scriptErr},
		})
		store, err := NewRedisHotQualityStore(runner, RedisHotQualityStoreOptions{Namespace: "dev", Now: func() int64 { return 1_000 }})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return store
	}

	good := `{"keyCount":2,"attemptIdentityCount":3,"terminalIdentityCount":4,"keyCreationRefusals":5,"highCardinalityDegradations":6,"attemptCapacityRefusals":7,"terminalQualityKeyMisses":8}`
	stats, err := build(t, good, nil).Stats(ctx, nil)
	if err != nil {
		t.Fatalf("stats err = %v", err)
	}
	if stats.KeyCount != 2 || stats.AttemptIdentityCount != 3 || stats.TerminalIdentityCount != 4 ||
		stats.KeyCreationRefusals != 5 || stats.HighCardinalityDegradations != 6 ||
		stats.AttemptCapacityRefusals != 7 || stats.TerminalQualityKeyMisses != 8 {
		t.Fatalf("stats = %+v", stats)
	}

	if _, err := build(t, nil, errors.New("stats boom")).Stats(ctx, nil); err == nil {
		t.Fatal("script error must surface")
	}
	if _, err := build(t, "", nil).Stats(ctx, nil); err == nil || err.Error() != "Redis 热质量统计返回值无效" {
		t.Fatalf("empty reply err = %v", err)
	}
	if _, err := build(t, 3.14, nil).Stats(ctx, nil); err == nil {
		t.Fatal("non-string reply must fail")
	}
	if _, err := build(t, `{invalid`, nil).Stats(ctx, nil); err == nil {
		t.Fatal("malformed stats json must fail")
	}
	missingRequired := []string{
		`{"attemptIdentityCount":1,"terminalIdentityCount":1}`,
		`{"keyCount":1,"terminalIdentityCount":1}`,
		`{"keyCount":1,"attemptIdentityCount":1}`,
	}
	for index, reply := range missingRequired {
		if _, err := build(t, reply, nil).Stats(ctx, nil); err == nil {
			t.Fatalf("missing required metric arm %d must fail", index)
		}
	}
	if _, err := build(t, `{"keyCount":-1,"attemptIdentityCount":1,"terminalIdentityCount":1}`, nil).Stats(ctx, nil); err == nil {
		t.Fatal("negative keyCount must fail")
	}
	if _, err := build(t, `{"keyCount":1,"attemptIdentityCount":1,"terminalIdentityCount":1,"keyCreationRefusals":-2}`, nil).Stats(ctx, nil); err == nil {
		t.Fatal("negative refusal must fail")
	}
	if _, err := build(t, good, nil).Stats(ctx, int64Ptr(-1)); err == nil {
		t.Fatal("negative now must fail")
	}
}

func TestW9CHotQualityScopeForAccountArms(t *testing.T) {
	model := "gpt-5"
	scope, err := hotQualityScopeForAccount(testAccount("acc-1", 5), "text", &model)
	if err != nil {
		t.Fatalf("scope err = %v", err)
	}
	if scope.AccountRuntimeKey != "acc-1" || scope.ProtocolProfile != "openai:2024" ||
		scope.RequestLane != "text" || scope.ModelFamily == "" {
		t.Fatalf("scope = %+v", scope)
	}
	// Authorized account without binding context must fail the runtime key.
	broken := testAccount("acc-2", 5)
	broken.AccountAccessType = "account_authorized"
	if _, err := hotQualityScopeForAccount(broken, "text", nil); err == nil {
		t.Fatal("authorized account without binding must be rejected")
	}
	// Fully empty profile fields fall back to ":" (the Node fallback string)
	// which still satisfies the 1..512 key rule.
	noProfile := testAccount("acc-3", 5)
	noProfile.ProviderProtocolProfileID = ""
	noProfile.ProtocolCode = ""
	noProfile.ProtocolVersion = ""
	scope, err = hotQualityScopeForAccount(noProfile, "image", nil)
	if err != nil || scope.ProtocolProfile != ":" {
		t.Fatalf("empty-profile scope = %+v, %v", scope, err)
	}
	// Protocol fallback builds the profile from code and version.
	fallback := testAccount("acc-4", 5)
	fallback.ProviderProtocolProfileID = ""
	fallback.ProtocolCode = "openai"
	fallback.ProtocolVersion = "v1"
	scope, err = hotQualityScopeForAccount(fallback, "image", nil)
	if err != nil || scope.ProtocolProfile != "openai:v1" {
		t.Fatalf("fallback scope = %+v, %v", scope, err)
	}
}

func TestW9CStableBindingOrdersArms(t *testing.T) {
	accounts := []GatewayHotQualityAccountView{testAccount("a", 1), testAccount("b", 2)}
	orders, err := stableBindingOrders(accounts, baseView, map[string]int{"a": 4})
	if err != nil || orders["a"] != 4 || orders["b"] != 1 {
		t.Fatalf("orders = %v, %v", orders, err)
	}
	// Negative provided order falls back to the index.
	orders, err = stableBindingOrders(accounts, baseView, map[string]int{"a": -2})
	if err != nil || orders["a"] != 0 {
		t.Fatalf("negative order = %v, %v", orders, err)
	}
	// Broken runtime key must surface the error.
	broken := testAccount("c", 3)
	broken.AccountAccessType = "account_authorized"
	if _, err := stableBindingOrders([]GatewayHotQualityAccountView{broken}, baseView, nil); err == nil {
		t.Fatal("broken runtime key must be rejected")
	}
}

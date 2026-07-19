package redis

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestNewClientRequiresURL(t *testing.T) {
	if _, err := NewClient("", "w0"); err == nil {
		t.Fatal("NewClient() error = nil, want error")
	}
}

func TestNewClientRequiresNamespace(t *testing.T) {
	if _, err := NewClient("redis://127.0.0.1:6379/0", ""); err == nil {
		t.Fatal("NewClient() error = nil, want namespace error")
	}
}

func TestNewClientEnablesContextTimeouts(t *testing.T) {
	client, err := NewClient("redis://127.0.0.1:6379/0", "test")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()
	if !client.client.Options().ContextTimeoutEnabled {
		t.Fatal("Redis client must enable context timeout propagation")
	}
}

func TestKeyUsesNamespaceAndTrimsSeparators(t *testing.T) {
	client := &Client{namespace: "juhe:w0"}
	if got, want := client.Key(":cache:", ":item:"), "juhe:w0:cache:item"; got != want {
		t.Fatalf("Key() = %q, want %q", got, want)
	}
}

func TestErrNotFoundComparable(t *testing.T) {
	if !errors.Is(ErrNotFound, ErrNotFound) {
		t.Fatal("ErrNotFound should be comparable with errors.Is")
	}
}

func TestValidateKeyAndTTL(t *testing.T) {
	if err := validateKeyAndTTL("", 1); err == nil {
		t.Fatal("validateKeyAndTTL() error = nil, want key error")
	}
	if err := validateKeyAndTTL("key", 0); err == nil {
		t.Fatal("validateKeyAndTTL() error = nil, want ttl error")
	}
	if err := validateKeyAndTTL("key", 1); err != nil {
		t.Fatalf("validateKeyAndTTL() error = %v", err)
	}
}

func TestSetRawValidatesInputBeforeRedisCall(t *testing.T) {
	client := &Client{namespace: "juhe:w3"}
	if err := client.SetRaw(t.Context(), "", []byte("value"), time.Second); err == nil {
		t.Fatal("SetRaw() error = nil, want key error")
	}
	if err := client.SetRaw(t.Context(), "juhe-ai:test:key", []byte("value"), 0); err == nil {
		t.Fatal("SetRaw() error = nil, want ttl error")
	}
}

func TestGetDeleteValidatesKey(t *testing.T) {
	client := &Client{namespace: "juhe:w3"}
	if _, err := client.GetDelete(t.Context(), ""); err == nil {
		t.Fatal("GetDelete() error = nil, want key error")
	}
}

func TestGetRawValidatesInputBeforeRedisCall(t *testing.T) {
	client := &Client{namespace: "juhe:w3"}
	if _, err := client.GetRaw(t.Context(), ""); err == nil {
		t.Fatal("GetRaw() error = nil, want key error")
	}
}

func TestDeleteValidatesKey(t *testing.T) {
	client := &Client{namespace: "juhe:w3"}
	if err := client.Delete(t.Context(), ""); err == nil {
		t.Fatal("Delete() error = nil, want key error")
	}
}

func TestFailureLockScriptArgs(t *testing.T) {
	client := &Client{namespace: "juhe:w3"}
	keys, args, err := client.failureLockScriptArgs([]FailureLockScope{
		{CounterKey: "disabled", LockKey: "disabled-lock", Threshold: 0, Window: time.Minute, Lock: time.Minute},
		{CounterKey: ":auth_login_guard:ip:abc:count:", LockKey: ":auth_login_guard:ip:abc:lock:", Threshold: 10, Window: 10 * time.Minute, Lock: 15 * time.Minute},
	})
	if err != nil {
		t.Fatalf("failureLockScriptArgs() error = %v", err)
	}
	wantKeys := []string{
		"juhe:w3:auth_login_guard:ip:abc:count",
		"juhe:w3:auth_login_guard:ip:abc:lock",
	}
	if len(keys) != len(wantKeys) {
		t.Fatalf("keys length = %d, want %d: %#v", len(keys), len(wantKeys), keys)
	}
	for i := range wantKeys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("keys[%d] = %q, want %q", i, keys[i], wantKeys[i])
		}
	}
	wantArgs := []interface{}{"10", "600000", "900000"}
	if len(args) != len(wantArgs) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(wantArgs), args)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %#v, want %#v", i, args[i], wantArgs[i])
		}
	}
}

func TestFailureLockScriptArgsValidatesEnabledScopes(t *testing.T) {
	client := &Client{namespace: "juhe:w3"}
	if _, _, err := client.failureLockScriptArgs([]FailureLockScope{
		{CounterKey: "", LockKey: "lock", Threshold: 1, Window: time.Minute, Lock: time.Minute},
	}); err == nil {
		t.Fatal("failureLockScriptArgs() error = nil, want counter key error")
	}
	if _, _, err := client.failureLockScriptArgs([]FailureLockScope{
		{CounterKey: "count", LockKey: "", Threshold: 1, Window: time.Minute, Lock: time.Minute},
	}); err == nil {
		t.Fatal("failureLockScriptArgs() error = nil, want lock key error")
	}
	if _, _, err := client.failureLockScriptArgs([]FailureLockScope{
		{CounterKey: "count", LockKey: "lock", Threshold: 1, Window: 0, Lock: time.Minute},
	}); err == nil {
		t.Fatal("failureLockScriptArgs() error = nil, want window error")
	}
	if _, _, err := client.failureLockScriptArgs([]FailureLockScope{
		{CounterKey: "count", LockKey: "lock", Threshold: 1, Window: time.Minute, Lock: 0},
	}); err == nil {
		t.Fatal("failureLockScriptArgs() error = nil, want lock ttl error")
	}
}

func TestFixedWindowScriptArgsSkipsDisabledLimits(t *testing.T) {
	client := &Client{namespace: "juhe:w1"}
	keys, args, err := client.fixedWindowScriptArgs([]FixedWindowLimit{
		{Key: "disabled", Limit: 0, Window: time.Second},
		{Key: ":system-api:ip-read:abc:minute:", Limit: 2, Window: time.Minute},
	})
	if err != nil {
		t.Fatalf("fixedWindowScriptArgs() error = %v", err)
	}
	if got, want := len(keys), 1; got != want {
		t.Fatalf("keys length = %d, want %d", got, want)
	}
	if got, want := keys[0], "juhe:w1:system-api:ip-read:abc:minute"; got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
	if got, want := len(args), 2; got != want {
		t.Fatalf("args length = %d, want %d", got, want)
	}
	if args[0] != "2" || args[1] != "60000" {
		t.Fatalf("args = %#v", args)
	}
}

func TestFixedWindowScriptArgsValidatesEnabledLimits(t *testing.T) {
	client := &Client{namespace: "juhe:w1"}
	if _, _, err := client.fixedWindowScriptArgs([]FixedWindowLimit{
		{Key: "", Limit: 1, Window: time.Second},
	}); err == nil {
		t.Fatal("fixedWindowScriptArgs() error = nil, want key error")
	}
	if _, _, err := client.fixedWindowScriptArgs([]FixedWindowLimit{
		{Key: "minute", Limit: 1, Window: 0},
	}); err == nil {
		t.Fatal("fixedWindowScriptArgs() error = nil, want window error")
	}
}

func TestNamedFixedWindowRawScriptArgs(t *testing.T) {
	now := time.UnixMilli(1_752_125_678_901).UTC()
	keys, args, err := namedFixedWindowRawScriptArgs(now, []NamedFixedWindowLimit{
		{
			RawKey:    "juhe-ai:rate-limit:fixed:disabled-hash:key-hash",
			StoreName: "system_api_disabled",
			Limit:     0,
			Window:    time.Second,
		},
		{
			RawKey:    "juhe-ai:rate-limit:fixed:minute-hash:key-hash",
			StoreName: "system_api_ip_minute",
			Limit:     600,
			Window:    time.Minute,
		},
		{
			RawKey:    "another-namespace:rate-limit:fixed:burst-hash:key-hash",
			StoreName: "system_api_ip_burst",
			Limit:     120,
			Window:    10 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("namedFixedWindowRawScriptArgs() error = %v", err)
	}

	wantKeys := []string{
		"juhe-ai:rate-limit:fixed:disabled-hash:key-hash",
		"juhe-ai:rate-limit:fixed:minute-hash:key-hash",
		"another-namespace:rate-limit:fixed:burst-hash:key-hash",
	}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("keys = %#v, want %#v", keys, wantKeys)
	}
	wantArgs := []interface{}{
		"1752125678901",
		"3",
		"system_api_disabled",
		"1000",
		"0",
		"system_api_ip_minute",
		"60000",
		"600",
		"system_api_ip_burst",
		"10000",
		"120",
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestAllowNamedFixedWindowRawAllowsEmptyLimitsWithoutRedisCall(t *testing.T) {
	client := &Client{namespace: "must-not-be-used"}
	decision, err := client.AllowNamedFixedWindowRaw(t.Context(), time.UnixMilli(1), nil)
	if err != nil {
		t.Fatalf("AllowNamedFixedWindowRaw() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("AllowNamedFixedWindowRaw() decision = %+v, want allowed", decision)
	}
}

func TestNamedFixedWindowRawScriptArgsValidatesLimits(t *testing.T) {
	tests := []struct {
		name  string
		limit NamedFixedWindowLimit
	}{
		{
			name: "raw key",
			limit: NamedFixedWindowLimit{
				StoreName: "system_api_ip_minute",
				Limit:     0,
				Window:    time.Minute,
			},
		},
		{
			name: "blank raw key",
			limit: NamedFixedWindowLimit{
				RawKey:    " ",
				StoreName: "system_api_ip_minute",
				Limit:     1,
				Window:    time.Minute,
			},
		},
		{
			name: "store name",
			limit: NamedFixedWindowLimit{
				RawKey: "raw:key",
				Limit:  0,
				Window: time.Minute,
			},
		},
		{
			name: "blank store name",
			limit: NamedFixedWindowLimit{
				RawKey:    "raw:key",
				StoreName: " ",
				Limit:     1,
				Window:    time.Minute,
			},
		},
		{
			name: "zero window",
			limit: NamedFixedWindowLimit{
				RawKey:    "raw:key",
				StoreName: "system_api_ip_minute",
				Limit:     0,
			},
		},
		{
			name: "sub-millisecond window",
			limit: NamedFixedWindowLimit{
				RawKey:    "raw:key",
				StoreName: "system_api_ip_minute",
				Limit:     1,
				Window:    time.Nanosecond,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := namedFixedWindowRawScriptArgs(time.UnixMilli(1), []NamedFixedWindowLimit{
				test.limit,
			}); err == nil {
				t.Fatal("namedFixedWindowRawScriptArgs() error = nil, want validation error")
			}
		})
	}
}

func TestParseNamedFixedWindowDecision(t *testing.T) {
	allowed, err := parseNamedFixedWindowDecision([]interface{}{
		int64(1),
		int64(0),
		"",
		int64(0),
	})
	if err != nil {
		t.Fatalf("parseNamedFixedWindowDecision() allowed error = %v", err)
	}
	if want := (NamedFixedWindowDecision{Allowed: true}); allowed != want {
		t.Fatalf("allowed decision = %+v, want %+v", allowed, want)
	}

	blocked, err := parseNamedFixedWindowDecision([]interface{}{
		"0",
		"7",
		[]byte("system_api_ip_burst"),
		"120",
	})
	if err != nil {
		t.Fatalf("parseNamedFixedWindowDecision() blocked error = %v", err)
	}
	wantBlocked := NamedFixedWindowDecision{
		Allowed:           false,
		RetryAfterSeconds: 7,
		StoreName:         "system_api_ip_burst",
		Limit:             120,
	}
	if blocked != wantBlocked {
		t.Fatalf("blocked decision = %+v, want %+v", blocked, wantBlocked)
	}

	minimumRetry, err := parseNamedFixedWindowDecision([]interface{}{
		int64(0),
		int64(0),
		"system_api_ip_minute",
		int64(600),
	})
	if err != nil {
		t.Fatalf("parseNamedFixedWindowDecision() minimum retry error = %v", err)
	}
	if minimumRetry.RetryAfterSeconds != 1 {
		t.Fatalf("minimum retry-after = %d, want 1", minimumRetry.RetryAfterSeconds)
	}
}

func TestParseNamedFixedWindowDecisionRejectsMalformedResults(t *testing.T) {
	tests := []struct {
		name   string
		values []interface{}
	}{
		{name: "length", values: []interface{}{int64(1)}},
		{name: "allowed", values: []interface{}{"invalid", int64(0), "", int64(0)}},
		{name: "retry after", values: []interface{}{int64(0), "invalid", "store", int64(1)}},
		{name: "store name", values: []interface{}{int64(0), int64(1), int64(2), int64(1)}},
		{name: "limit", values: []interface{}{int64(0), int64(1), "store", "invalid"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseNamedFixedWindowDecision(test.values); err == nil {
				t.Fatal("parseNamedFixedWindowDecision() error = nil, want parse error")
			}
		})
	}
}

func TestPenaltyWindowScriptArgs(t *testing.T) {
	client := &Client{namespace: "juhe:w1b"}
	now := time.UnixMilli(1250).UTC()
	keys, args, err := client.penaltyWindowScriptArgs([]PenaltyWindowLimit{
		{
			StoreName:  "external_source_public_api",
			ScopeKey:   "source:token:juis",
			Window:     time.Second,
			Limit:      2,
			MaxPenalty: 15 * time.Minute,
			MaxIdle:    24 * time.Hour,
			Now:        now,
		},
	})
	if err != nil {
		t.Fatalf("penaltyWindowScriptArgs() error = %v", err)
	}
	wantKey := "juhe:w1b:rate-limit:penalty:" +
		keyHash("external_source_public_api") + ":" +
		keyHash("source:token:juis") + ":1:2"
	if got := keys[0]; got != wantKey {
		t.Fatalf("key = %q, want %q", got, wantKey)
	}
	wantArgs := []interface{}{"1250", "1", "1000", "1000", "2", "900000", "86400000"}
	if got, want := len(args), len(wantArgs); got != want {
		t.Fatalf("args length = %d, want %d: %#v", got, want, args)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %#v, want %#v; all args = %#v", i, args[i], wantArgs[i], args)
		}
	}
}

func TestPenaltyWindowScriptArgsSkipsDisabledLimits(t *testing.T) {
	client := &Client{namespace: "juhe:w1b"}
	keys, args, err := client.penaltyWindowScriptArgs([]PenaltyWindowLimit{
		{StoreName: "external_source_public_api", ScopeKey: "disabled", Window: time.Second, Limit: 0},
	})
	if err != nil {
		t.Fatalf("penaltyWindowScriptArgs() error = %v", err)
	}
	if len(keys) != 0 || len(args) != 0 {
		t.Fatalf("keys/args = %#v / %#v, want empty", keys, args)
	}
}

func TestPenaltyWindowScriptArgsValidatesEnabledLimits(t *testing.T) {
	client := &Client{namespace: "juhe:w1b"}
	if _, _, err := client.penaltyWindowScriptArgs([]PenaltyWindowLimit{
		{ScopeKey: "scope", Window: time.Second, Limit: 1},
	}); err == nil {
		t.Fatal("penaltyWindowScriptArgs() error = nil, want store name error")
	}
	if _, _, err := client.penaltyWindowScriptArgs([]PenaltyWindowLimit{
		{StoreName: "store", Window: time.Second, Limit: 1},
	}); err == nil {
		t.Fatal("penaltyWindowScriptArgs() error = nil, want scope key error")
	}
}

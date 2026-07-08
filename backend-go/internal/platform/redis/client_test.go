package redis

import (
	"errors"
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

func TestGetDeleteValidatesKey(t *testing.T) {
	client := &Client{namespace: "juhe:w3"}
	if _, err := client.GetDelete(t.Context(), ""); err == nil {
		t.Fatal("GetDelete() error = nil, want key error")
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

package accountbalance

import "testing"

func TestNormalizeConfigStrictDefaultsAndCustom(t *testing.T) {
	config, err := NormalizeConfig(map[string]any{"adapter": "builtin", "preferredBuiltinAdapter": "sub2api"})
	if err != nil {
		t.Fatal(err)
	}
	if config.IntervalMinutes != 5 || config.PreferredBuiltinAdapter != AdapterSub2API {
		t.Fatalf("unexpected builtin config: %#v", config)
	}
	custom, err := NormalizeConfig(map[string]any{
		"adapter":         "custom",
		"intervalMinutes": float64(10),
		"custom":          map[string]any{"path": "/balance", "remainingPointer": "/data/left~1balance", "divisor": "7.2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if custom.Custom == nil || custom.Custom.Path != "/balance" || custom.Custom.Divisor != "7.2" {
		t.Fatalf("unexpected custom config: %#v", custom)
	}
}

func TestNormalizeConfigRejectsAmbiguousOrUnknownFields(t *testing.T) {
	cases := []map[string]any{
		{"adapter": "builtin", "custom": map[string]any{}},
		{"adapter": "custom", "custom": map[string]any{"path": "/x", "remainingPointer": "/a", "totalPointer": "/b", "usedPointer": "/c"}},
		{"adapter": "custom", "custom": map[string]any{"path": "https://example.com/x", "remainingPointer": "/a"}},
		{"adapter": "custom", "custom": map[string]any{"path": "/x", "auth": "x-api-key", "remainingPointer": "/a"}},
		{"adapter": "builtin", "unexpected": true},
	}
	for _, input := range cases {
		if _, err := NormalizeConfig(input); err == nil {
			t.Fatalf("config must fail closed: %#v", input)
		}
	}
}

func TestValidateCapabilityNeverSelectsFirstKey(t *testing.T) {
	credentials := map[string]any{"api_keys": []any{"first", "second", "first"}}
	keys := EffectiveAPIKeys(credentials)
	if len(keys) != 2 || keys[0] != "first" || keys[1] != "second" {
		t.Fatalf("unexpected key pool: %#v", keys)
	}
	decision, err := ValidateCapability(CapabilityInput{Type: "api_key", Credentials: credentials}, true)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Enabled || !decision.AutoDisabledForMultipleKeys {
		t.Fatalf("multi-key capability must auto-disable: %#v", decision)
	}
	if _, err := ValidateCapability(CapabilityInput{Type: "oauth", Credentials: map[string]any{"api_key": "x"}}, true); err == nil {
		t.Fatal("oauth account must not enable balance query")
	}
}

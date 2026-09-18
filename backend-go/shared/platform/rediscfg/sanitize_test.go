package rediscfg

import "testing"

func TestSanitizeRedisName(t *testing.T) {
	cases := map[string]string{
		"  prod-space ": "prod-space",
		"abc_xyz-123":  "abc_xyz-123",
		"a b/c":        "a_b_c",
		"":             "",
	}
	for in, want := range cases {
		if got := SanitizeRedisName(in); got != want {
			t.Fatalf("SanitizeRedisName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSanitizeRedisNamespacePart(t *testing.T) {
	cases := map[string]string{
		"dev":         "dev",
		"a..b":        "a..b",
		" a__b ":      "a__b",
		"a *!b":       "a_b",
		"*!a":         "a",
		"a*!":         "a",
		"":            "",
		"  ":          "",
		"ns.1:role-2": "ns.1:role-2",
	}
	for in, want := range cases {
		if got := SanitizeRedisNamespacePart(in); got != want {
			t.Fatalf("SanitizeRedisNamespacePart(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNamespacedKey(t *testing.T) {
	cases := map[[2]string]string{
		{"probe:state:key", "dev"}:           "juhe-ai:dev:probe:state:key",
		{"juhe-ai:probe:state:key", "dev"}:   "juhe-ai:dev:probe:state:key",
		{"juhe-ai:dev:probe:state:key", "dev"}: "juhe-ai:dev:probe:state:key",
		{"probe:state:key", "  "}:            "probe:state:key",
	}
	for in, want := range cases {
		if got := NamespacedKey(in[0], in[1]); got != want {
			t.Fatalf("NamespacedKey(%q,%q)=%q want %q", in[0], in[1], got, want)
		}
	}
}

func TestNamespacedKeyEmptyKeyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("空 key 必须 panic（调用方契约）")
		}
	}()
	NamespacedKey("  ", "dev")
}

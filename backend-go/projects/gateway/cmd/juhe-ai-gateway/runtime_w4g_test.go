// Tests for the BUG-0175 W4-G runtime env surface (D-209/D-211/D-147 and the
// upstream URL security config feeding D-192/D-146): startup fail-fast and
// the restored env knobs.

package main

import (
	"strings"
	"testing"
)

func w4gEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func TestTrustProxyConfigFailFast(t *testing.T) {
	cases := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "", want: ""},
		{raw: "true", want: "true"},
		{raw: "TRUE", want: "true"},
		{raw: "yes", want: "true"},
		{raw: "off", want: "false"},
		{raw: "0", want: "0"},
		{raw: " 3 ", want: "3"},
		{raw: "16", want: "16"},
		{raw: "17", wantErr: true},
		{raw: "-1", wantErr: true},
		{raw: "abc", wantErr: true},
		{raw: "1.5", wantErr: true},
	}
	for _, tc := range cases {
		got, err := trustProxyConfig("JUHE_AI_TRUST_PROXY", tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("trustProxyConfig(%q) accepted", tc.raw)
			}
			if !strings.Contains(err.Error(), "0-16") {
				t.Fatalf("trustProxyConfig(%q) error = %v", tc.raw, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("trustProxyConfig(%q) = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("trustProxyConfig(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	// The kernel-facing hop count stays aligned.
	if count := trustProxyCount("yes"); count != 1 {
		t.Fatalf("trustProxyCount(yes) = %d", count)
	}
	if count := trustProxyCount("12"); count != 12 {
		t.Fatalf("trustProxyCount(12) = %d", count)
	}
	if count := trustProxyCount("17"); count != 0 {
		t.Fatalf("trustProxyCount(17) = %d, want 0 (untrusted)", count)
	}
}

func TestTemporaryAccessIPAllowlistConfig(t *testing.T) {
	got, err := temporaryAccessIPAllowlistConfig("JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST", " 10.0.0.1 , ::ffff:192.168.1.2, 10.0.0.1, 2001:db8::1 ")
	if err != nil {
		t.Fatalf("allowlist config: %v", err)
	}
	if len(got) != 3 || got[0] != "10.0.0.1" || got[1] != "192.168.1.2" || got[2] != "2001:db8::1" {
		t.Fatalf("allowlist = %#v", got)
	}
	for _, bad := range []string{"internal.example.com", "10.0.0.0/8", "10.0.0.1-10.0.0.5", "*"} {
		if _, err := temporaryAccessIPAllowlistConfig("JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST", bad); err == nil {
			t.Fatalf("allowlist accepted %q", bad)
		}
	}
}

func TestLoadRuntimeConfigUsageSpoolEnvs(t *testing.T) {
	// Defaults and the legacy DIR fallback.
	cfg, err := loadRuntimeConfig(w4gEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH":       "data/business.sqlite3",
		"JUHE_AI_USAGE_SPOOL_DIR":     "/tmp/spool-legacy",
		"JUHE_AI_STATS_DATABASE_PATH": "data/stats.sqlite3",
	}))
	if err != nil {
		t.Fatalf("load with legacy spool dir: %v", err)
	}
	if cfg.UsageSpoolDirectory != "/tmp/spool-legacy" {
		t.Fatalf("legacy DIR ignored: %q", cfg.UsageSpoolDirectory)
	}
	if cfg.UsageSpoolMaxItems != 250_000 || cfg.UsageSpoolMaxBytes != 4_096*1024*1024 ||
		cfg.UsageSpoolReplayBatchSize != 500 || cfg.UsageSpoolReplayIntervalMs != 1_000 {
		t.Fatalf("spool defaults = %#v", cfg)
	}
	// DIRECTORY wins over DIR.
	cfg, err = loadRuntimeConfig(w4gEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH":         "data/business.sqlite3",
		"JUHE_AI_USAGE_SPOOL_DIR":       "/tmp/spool-legacy",
		"JUHE_AI_USAGE_SPOOL_DIRECTORY": "/tmp/spool-new",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.UsageSpoolDirectory != "/tmp/spool-new" {
		t.Fatalf("DIRECTORY must win over DIR, got %q", cfg.UsageSpoolDirectory)
	}
	// Capacity knobs load and validate.
	cfg, err = loadRuntimeConfig(w4gEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH":                  "data/business.sqlite3",
		"JUHE_AI_USAGE_SPOOL_MAX_ITEMS":          "300000",
		"JUHE_AI_USAGE_SPOOL_MAX_MB":             "64",
		"JUHE_AI_USAGE_SPOOL_REPLAY_BATCH_SIZE":  "100",
		"JUHE_AI_USAGE_SPOOL_REPLAY_INTERVAL_MS": "250",
	}))
	if err != nil {
		t.Fatalf("load capacities: %v", err)
	}
	if cfg.UsageSpoolMaxItems != 300_000 || cfg.UsageSpoolMaxBytes != 64*1024*1024 ||
		cfg.UsageSpoolReplayBatchSize != 100 || cfg.UsageSpoolReplayIntervalMs != 250 {
		t.Fatalf("spool capacities = %#v", cfg)
	}
	for _, bad := range []struct{ env, value string }{
		{"JUHE_AI_USAGE_SPOOL_MAX_ITEMS", "10"}, // below 1000
		{"JUHE_AI_USAGE_SPOOL_MAX_MB", "32"},    // below 64
		{"JUHE_AI_USAGE_SPOOL_REPLAY_BATCH_SIZE", "0"},
		{"JUHE_AI_USAGE_SPOOL_REPLAY_INTERVAL_MS", "50"},
	} {
		if _, err := loadRuntimeConfig(w4gEnv(map[string]string{
			"JUHE_AI_DATABASE_PATH": "data/business.sqlite3",
			bad.env:                 bad.value,
		})); err == nil {
			t.Fatalf("%s=%s accepted", bad.env, bad.value)
		}
	}
}

func TestLoadRuntimeConfigUsageFinalizationMaxItems(t *testing.T) {
	base := map[string]string{"JUHE_AI_DATABASE_PATH": "data/business.sqlite3"}
	cfg, err := loadRuntimeConfig(w4gEnv(base))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.UsageFinalizationMaxItems != 2048 {
		t.Fatalf("default finalization max items = %d", cfg.UsageFinalizationMaxItems)
	}
	cfg, err = loadRuntimeConfig(w4gEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH":                        "data/business.sqlite3",
		"JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS": "4096",
	}))
	if err != nil {
		t.Fatalf("load finalization: %v", err)
	}
	if cfg.UsageFinalizationMaxItems != 4096 {
		t.Fatalf("finalization max items = %d", cfg.UsageFinalizationMaxItems)
	}
	badZero := map[string]string{"JUHE_AI_DATABASE_PATH": "d", "JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS": "0"}
	if _, err := loadRuntimeConfig(w4gEnv(badZero)); err == nil {
		t.Fatal("finalization max items 0 accepted")
	}
	badText := map[string]string{"JUHE_AI_DATABASE_PATH": "d", "JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS": "abc"}
	if _, err := loadRuntimeConfig(w4gEnv(badText)); err == nil {
		t.Fatal("non-number finalization max items accepted")
	}
}

func TestLoadRuntimeConfigUpstreamURLSecurity(t *testing.T) {
	cfg, err := loadRuntimeConfig(w4gEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH":                       "data/business.sqlite3",
		"JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST": "http://10.0.0.5:8080 , https://192.168.1.5",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.UpstreamURLSecurity.PrivateOriginAllowlist["http://10.0.0.5:8080"] {
		t.Fatalf("allowlist missing explicit-port key: %#v", cfg.UpstreamURLSecurity.PrivateOriginAllowlist)
	}
	if !cfg.UpstreamURLSecurity.PrivateOriginAllowlist["https://192.168.1.5:443"] {
		t.Fatalf("allowlist missing default-port key: %#v", cfg.UpstreamURLSecurity.PrivateOriginAllowlist)
	}
	if cfg.UpstreamURLSecurity.AllowPrivateBaseUrls {
		t.Fatal("allowPrivateBaseUrls must default off")
	}
	// Domain entries fail the startup.
	if _, err := loadRuntimeConfig(w4gEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH":                       "d",
		"JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST": "http://internal.example.com",
	})); err == nil {
		t.Fatal("domain allowlist entry accepted")
	}
	// allowPrivateBaseUrls is refused under the production signal.
	if _, err := loadRuntimeConfig(w4gEnv(map[string]string{
		"NODE_ENV":              "production",
		"JUHE_AI_DATABASE_PATH": "data/business.sqlite3",
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS": "true",
		"JUHE_AI_SECRET":          strings.Repeat("x", 40),
		"JUHE_AI_ALLOWED_ORIGINS": "https://admin.example.com",
	})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS") {
		t.Fatalf("production allowPrivateBaseUrls err = %v", err)
	}
	// Non-production keeps the escape hatch.
	cfg, err = loadRuntimeConfig(w4gEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH":                    "data/business.sqlite3",
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS": "true",
	}))
	if err != nil || !cfg.UpstreamURLSecurity.AllowPrivateBaseUrls {
		t.Fatalf("dev allowPrivateBaseUrls = %#v, %v", cfg.UpstreamURLSecurity, err)
	}
}

func TestLoadRuntimeConfigTrustProxyEnvFailsFast(t *testing.T) {
	if _, err := loadRuntimeConfig(w4gEnv(map[string]string{"JUHE_AI_TRUST_PROXY": "sometimes"})); err == nil {
		t.Fatal("invalid TRUST_PROXY accepted")
	}
	if _, err := loadRuntimeConfig(w4gEnv(map[string]string{"JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST": "not-an-ip"})); err == nil {
		t.Fatal("invalid allowlist accepted")
	}
}

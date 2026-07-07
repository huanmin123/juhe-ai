package config

import (
	"testing"
	"time"
)

func TestConfigDefaultsValidate(t *testing.T) {
	cfg := Config{
		Host:            "127.0.0.1",
		Port:            3000,
		Env:             "test",
		LogLevel:        "info",
		RedisNamespace:  "juhe-ai",
		TrustProxy:      "false",
		ShutdownTimeout: 15 * time.Second,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if got := cfg.Address(); got != "127.0.0.1:3000" {
		t.Fatalf("Address() = %q", got)
	}
}

func TestTrustProxyConfigDefaultsToDisabled(t *testing.T) {
	cfg := Config{}
	got, err := cfg.TrustProxyConfig()
	if err != nil {
		t.Fatalf("TrustProxyConfig() error = %v", err)
	}
	if got.Enabled || got.TrustAll || got.Hops != 0 {
		t.Fatalf("TrustProxyConfig() = %+v, want disabled", got)
	}
}

func TestTrustProxyConfigSupportsBooleanValues(t *testing.T) {
	cfg := Config{TrustProxy: "yes"}
	got, err := cfg.TrustProxyConfig()
	if err != nil {
		t.Fatalf("TrustProxyConfig() error = %v", err)
	}
	if !got.Enabled || !got.TrustAll {
		t.Fatalf("TrustProxyConfig() = %+v, want trust all", got)
	}

	cfg.TrustProxy = "off"
	got, err = cfg.TrustProxyConfig()
	if err != nil {
		t.Fatalf("TrustProxyConfig() error = %v", err)
	}
	if got.Enabled {
		t.Fatalf("TrustProxyConfig() = %+v, want disabled", got)
	}
}

func TestTrustProxyConfigSupportsHopCount(t *testing.T) {
	for _, tc := range []struct {
		value   string
		enabled bool
		hops    int
	}{
		{value: "0", enabled: false, hops: 0},
		{value: "1", enabled: true, hops: 1},
		{value: "2", enabled: true, hops: 2},
		{value: "16", enabled: true, hops: 16},
	} {
		t.Run(tc.value, func(t *testing.T) {
			cfg := Config{TrustProxy: tc.value}
			got, err := cfg.TrustProxyConfig()
			if err != nil {
				t.Fatalf("TrustProxyConfig() error = %v", err)
			}
			if got.Enabled != tc.enabled || got.TrustAll || got.Hops != tc.hops {
				t.Fatalf("TrustProxyConfig() = %+v, want enabled=%v hops=%d", got, tc.enabled, tc.hops)
			}
		})
	}
}

func TestTrustProxyConfigRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"maybe", "-1", "17", "1.5"} {
		t.Run(value, func(t *testing.T) {
			cfg := Config{
				Host:            "127.0.0.1",
				Port:            3000,
				RedisNamespace:  "juhe-ai",
				TrustProxy:      value,
				ShutdownTimeout: time.Second,
			}
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid trust proxy error")
			}
		})
	}
}

func TestConfigRejectsInvalidPort(t *testing.T) {
	cfg := Config{Host: "127.0.0.1", Port: 70000, RedisNamespace: "juhe-ai", ShutdownTimeout: time.Second}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("Validate() expected error")
	}
}

func TestConfigRejectsSameRedisDB(t *testing.T) {
	cfg := Config{
		Host:            "127.0.0.1",
		Port:            3000,
		RedisNamespace:  "juhe-ai",
		RedisCacheURL:   "redis://127.0.0.1:6379/0",
		RedisStateURL:   "redis://127.0.0.1:6379/0",
		ShutdownTimeout: time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want duplicate Redis DB error")
	}
}

func TestConfigRejectsSameRedisDBWithDefaultPort(t *testing.T) {
	cfg := Config{
		Host:            "127.0.0.1",
		Port:            3000,
		RedisNamespace:  "juhe-ai",
		RedisCacheURL:   "redis://127.0.0.1/0",
		RedisStateURL:   "redis://127.0.0.1:6379/0",
		ShutdownTimeout: time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want duplicate Redis DB error")
	}
}

func TestConfigAllowsDifferentRedisDBs(t *testing.T) {
	cfg := Config{
		Host:            "127.0.0.1",
		Port:            3000,
		RedisNamespace:  "juhe-ai",
		RedisCacheURL:   "redis://127.0.0.1:6379/0",
		RedisStateURL:   "redis://127.0.0.1:6379/1",
		RedisQueueURL:   "redis://127.0.0.1:6379/2",
		ShutdownTimeout: time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

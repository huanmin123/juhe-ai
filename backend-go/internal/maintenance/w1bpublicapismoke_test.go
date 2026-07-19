package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
)

func TestRunW1bPublicAPISmokeRequiresDependencyConfig(t *testing.T) {
	var out bytes.Buffer
	err := RunW1bPublicAPISmoke(context.Background(), config.Config{}, &out)
	if err == nil {
		t.Fatal("RunW1bPublicAPISmoke() error = nil, want missing config error")
	}
	for _, name := range []string{
		"JUHE_AI_POSTGRES_URL",
		"JUHE_AI_REDIS_CACHE_URL",
		"JUHE_AI_REDIS_STATE_URL",
		"JUHE_AI_REDIS_QUEUE_URL",
		"JUHE_AI_SECRET",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not mention %s", err, name)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty", out.String())
	}
}

func TestSmokeW1bDefaultRouterGuardKeepsPublicAPIDisabled(t *testing.T) {
	cfg := config.Config{
		Host:             "127.0.0.1",
		Port:             3000,
		RedisNamespace:   "juhe-ai",
		ShutdownTimeout:  time.Second,
		PublicAPIEnabled: true,
	}

	if err := smokeW1bDefaultRouterGuard(context.Background(), cfg); err != nil {
		t.Fatalf("smokeW1bDefaultRouterGuard() error = %v", err)
	}
}

func TestW1bPublicAPISmokeConfigUsesSafeNodeDummyURL(t *testing.T) {
	cfg, err := w1bPublicAPISmokeConfig(config.Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		PostgresURL:                "postgres://127.0.0.1:5432/juhe_ai",
		RedisCacheURL:              "redis://127.0.0.1:6379/0",
		RedisStateURL:              "redis://127.0.0.1:6380/1",
		RedisQueueURL:              "redis://127.0.0.1:6381/2",
		Secret:                     "12345678901234567890123456789012",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	})
	if err != nil {
		t.Fatalf("w1bPublicAPISmokeConfig() error = %v", err)
	}
	if !cfg.PublicAPIEnabled {
		t.Fatal("PublicAPIEnabled = false, want true")
	}
	if cfg.NodeInternalBaseURL != "http://127.0.0.1:1" {
		t.Fatalf("NodeInternalBaseURL = %q, want safe loopback dummy URL", cfg.NodeInternalBaseURL)
	}

	dispatcher := w1bPublicAPISmokeHealthCheckDispatcher{}
	if err := dispatcher.Dispatch(context.Background(), "acc_smoke", "activation"); err != nil {
		t.Fatalf("smoke dispatcher error = %v", err)
	}
}

func TestWriteW1bPublicAPISmokeResultNeverClaimsTakeover(t *testing.T) {
	var out bytes.Buffer
	err := writeW1bPublicAPISmokeResult(&out, W1bPublicAPISmokeResult{
		Checks: map[string]W1bPublicAPISmokeCheck{
			"postgres": {Status: "error", Error: "PostgreSQL 连接失败"},
		},
		PublicAPI: &W1bPublicAPISmokePublicAPI{
			ConfiguredEnabled: true,
			LocalSmokeEnabled: true,
			Prefix:            "/__aipublic__",
		},
		TakeoverEvidence: true,
		TakeoverAssessment: W1bPublicAPISmokeTakeover{
			ExplicitOptInMountWorks: true,
		},
	})
	if err == nil {
		t.Fatal("writeW1bPublicAPISmokeResult() error = nil, want failure")
	}

	var body W1bPublicAPISmokeResult
	if decodeErr := json.NewDecoder(&out).Decode(&body); decodeErr != nil {
		t.Fatalf("decode: %v", decodeErr)
	}
	if body.Scope != w1bPublicAPISmokeScope {
		t.Fatalf("scope = %q", body.Scope)
	}
	if body.TakeoverEvidence {
		t.Fatal("takeoverEvidence = true, want false")
	}
	if !body.TakeoverAssessment.ProductionTakeoverNotEvaluated {
		t.Fatal("productionTakeoverNotEvaluated = false, want true")
	}
	if !body.TakeoverAssessment.ExplicitOptInMountWorks {
		t.Fatal("explicitOptInMountWorks was overwritten")
	}
	if got := body.Checks["postgres"].Error; got != "PostgreSQL 连接失败" {
		t.Fatalf("safe error = %q", got)
	}
}

func TestMissingW1bPublicAPIConfigRequiresLongSecret(t *testing.T) {
	missing := missingW1bPublicAPIConfig(config.Config{
		PostgresURL:   "postgres://user:pass@127.0.0.1:5432/app",
		RedisCacheURL: "redis://127.0.0.1:6379/0",
		RedisStateURL: "redis://127.0.0.1:6380/1",
		RedisQueueURL: "redis://127.0.0.1:6381/2",
		Secret:        "short",
	})

	if len(missing) != 1 || !strings.Contains(missing[0], "JUHE_AI_SECRET") {
		t.Fatalf("missing = %v, want only JUHE_AI_SECRET", missing)
	}
}

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

func TestRunW2OperationLogsSmokeRequiresDependencyConfig(t *testing.T) {
	var out bytes.Buffer
	err := RunW2OperationLogsSmoke(context.Background(), config.Config{}, &out)
	if err == nil {
		t.Fatal("RunW2OperationLogsSmoke() error = nil, want missing config error")
	}
	for _, name := range []string{
		"JUHE_AI_POSTGRES_URL",
		"JUHE_AI_REDIS_STATE_URL",
		"JUHE_AI_REDIS_QUEUE_URL",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not mention %s", err, name)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty", out.String())
	}
}

func TestSmokeW2OperationLogsDefaultRouterGuardKeepsManagementDisabled(t *testing.T) {
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		RedisNamespace:       "juhe-ai",
		ShutdownTimeout:      time.Second,
		ManagementAPIEnabled: true,
	}

	if err := smokeW2OperationLogsDefaultRouterGuard(context.Background(), cfg); err != nil {
		t.Fatalf("smokeW2OperationLogsDefaultRouterGuard() error = %v", err)
	}
}

func TestWriteW2OperationLogsSmokeResultNeverClaimsTakeover(t *testing.T) {
	var out bytes.Buffer
	err := writeW2OperationLogsSmokeResult(&out, W2OperationLogsSmokeResult{
		Checks: map[string]W2SmokeCheck{
			"postgres": {Status: "error", Error: "PostgreSQL 连接失败"},
		},
		TakeoverEvidence: true,
		TakeoverAssessment: W2SmokeTakeover{
			ExplicitOptInMountWorks: true,
		},
	})
	if err == nil {
		t.Fatal("writeW2OperationLogsSmokeResult() error = nil, want failure")
	}

	var body W2OperationLogsSmokeResult
	if decodeErr := json.NewDecoder(&out).Decode(&body); decodeErr != nil {
		t.Fatalf("decode: %v", decodeErr)
	}
	if body.Scope != w2OperationLogsSmokeScope {
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

func TestEscapedW2SmokeLikePrefixEscapesWildcards(t *testing.T) {
	got := escapedW2SmokeLikePrefix(`sys_w2%foo\bar`)
	want := `sys\_w2\%foo\\bar%`
	if got != want {
		t.Fatalf("escaped prefix = %q, want %q", got, want)
	}
}

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
)

func TestRunW1aPublicSettingsSmokeRequiresDependencyURLs(t *testing.T) {
	var out bytes.Buffer
	err := RunW1aPublicSettingsSmoke(context.Background(), config.Config{}, &out)
	if err == nil {
		t.Fatal("RunW1aPublicSettingsSmoke() error = nil, want missing config error")
	}
	for _, name := range []string{
		"JUHE_AI_POSTGRES_URL",
		"JUHE_AI_REDIS_STATE_URL",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not mention %s", err, name)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty", out.String())
	}
}

func TestWriteW1aPublicSettingsSmokeResultKeepsSafeErrors(t *testing.T) {
	var out bytes.Buffer
	err := writeW1aPublicSettingsSmokeResult(&out, W1aPublicSettingsSmokeResult{
		Checks: map[string]W1aPublicSettingsSmokeCheck{
			"postgres": {Status: "error", Error: "PostgreSQL 连接失败"},
		},
	})
	if err == nil {
		t.Fatal("writeW1aPublicSettingsSmokeResult() error = nil, want failure")
	}

	var body W1aPublicSettingsSmokeResult
	if decodeErr := json.NewDecoder(&out).Decode(&body); decodeErr != nil {
		t.Fatalf("decode: %v", decodeErr)
	}
	if got := body.Checks["postgres"].Error; got != "PostgreSQL 连接失败" {
		t.Fatalf("safe error = %q", got)
	}
}

func TestUniqueSmokeIPv4UsesBenchmarkRange(t *testing.T) {
	ip := uniqueSmokeIPv4()
	if !strings.HasPrefix(ip, "198.18.") {
		t.Fatalf("uniqueSmokeIPv4() = %q, want 198.18/15 range", ip)
	}
}

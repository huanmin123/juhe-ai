package maintenance

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
)

func TestRunW0SmokeRequiresAllDependencyURLs(t *testing.T) {
	var out bytes.Buffer
	err := RunW0Smoke(context.Background(), config.Config{}, &out)
	if err == nil {
		t.Fatal("RunW0Smoke() error = nil, want missing config error")
	}
	for _, name := range []string{
		"JUHE_AI_POSTGRES_URL",
		"JUHE_AI_REDIS_CACHE_URL",
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

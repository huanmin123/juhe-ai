package modelcheckprobe

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// TestLiveModelCheck is opt-in and intentionally has no credentials or fixed
// endpoint in source. It provides a repeatable operator path for manual quick
// and full probes while keeping result evidence bounded to test logs.
func TestLiveModelCheck(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("J3B_LIVE_ENDPOINT"))
	apiKey := strings.TrimSpace(os.Getenv("J3B_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("J3B_LIVE_MODEL"))
	profile := strings.TrimSpace(os.Getenv("J3B_LIVE_PROFILE"))
	if endpoint == "" || apiKey == "" || model == "" || profile == "" {
		t.Skip("set J3B_LIVE_ENDPOINT, J3B_LIVE_API_KEY, J3B_LIVE_MODEL, and J3B_LIVE_PROFILE to run a live model check")
	}
	if profile != "quick" && profile != "full" {
		t.Fatalf("J3B_LIVE_PROFILE=%q, want quick or full", profile)
	}
	supportedModels := splitLiveModels(os.Getenv("J3B_LIVE_SUPPORTED_MODELS"))
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	items, err := RunSuite(ctx, Suite{
		Endpoint:        endpoint,
		Headers:         http.Header{"Authorization": []string{"Bearer " + apiKey}},
		Model:           model,
		Profile:         profile,
		Protocol:        modelcheckprofile.ProtocolOpenAIResponses,
		SupportedModels: supportedModels,
		Tokenizer:       liveTokenizer(t),
		Client:          &http.Client{},
		Retry:           RetryOptionsForProfile(profile),
	}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	statusCounts := map[string]int{}
	requestFailures := 0
	for _, item := range items {
		statusCounts[item.Status]++
		if evidenceBool(item.Evidence, "requestFailure") {
			requestFailures++
		}
		t.Logf("live item kind=%s status=%s score=%d/%d httpStatus=%v retryAttempts=%v modelMismatch=%v", item.Kind, item.Status, item.Score, item.MaxScore, item.Evidence["httpStatus"], item.Evidence["retryAttemptCount"], item.Evidence["modelMismatch"])
	}
	summary := SummarizeChecks(items, false, profile)
	t.Logf("live model check model=%s profile=%s itemStatus=%v requestFailures=%d summary=%s/%d", model, profile, statusCounts, requestFailures, summary.Level, summary.Score)
}

func liveTokenizer(t *testing.T) Tokenizer {
	t.Helper()
	tokenizer, err := NewO200kTokenizer()
	if err != nil {
		t.Fatal(err)
	}
	return tokenizer
}

func splitLiveModels(value string) []string {
	models := make([]string, 0)
	for _, value := range strings.Split(value, ",") {
		if value = strings.TrimSpace(value); value != "" {
			models = append(models, value)
		}
	}
	return models
}

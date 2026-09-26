package gatewaycodex

// PersistAnthropicUsageHeadersIfNeeded 的资格门回归（AI账户Grok用量快照设计
// §8.2）：与 TestPersistOpenAICodexHeadersIfNeeded 同表结构——仅
// provider_code='anthropic' AND type='oauth' 且带 unified rate limit 头的
// 账户派发；无头与 nil 派发器静默。

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

type fakeAnthropicUsageDispatcher struct {
	mu    sync.Mutex
	calls []string
}

func (d *fakeAnthropicUsageDispatcher) PersistAnthropicUsageHeaders(_ context.Context, accountID string, _ http.Header, source string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, accountID+"/"+source)
}

func TestPersistAnthropicUsageHeadersIfNeeded(t *testing.T) {
	anthropicOAuthAccount := gatewayruntimecache.OpenAIAccountSecret{
		ID:           "acc-1",
		Type:         "oauth",
		ProviderCode: "anthropic",
	}
	apiKeyAccount := anthropicOAuthAccount
	apiKeyAccount.Type = "api_key"
	nonAnthropic := anthropicOAuthAccount
	nonAnthropic.ProviderCode = "openai"
	anthropicHeaders := http.Header{}
	anthropicHeaders.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.14")
	plainHeaders := http.Header{}
	plainHeaders.Set("x-other", "1")

	tests := []struct {
		name     string
		account  gatewayruntimecache.OpenAIAccountSecret
		headers  http.Header
		wantCall bool
	}{
		{name: "anthropic oauth dispatches", account: anthropicOAuthAccount, headers: anthropicHeaders, wantCall: true},
		{name: "api key account skipped", account: apiKeyAccount, headers: anthropicHeaders},
		{name: "non anthropic provider skipped", account: nonAnthropic, headers: anthropicHeaders},
		{name: "no anthropic headers skipped", account: anthropicOAuthAccount, headers: plainHeaders},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := &fakeAnthropicUsageDispatcher{}
			PersistAnthropicUsageHeadersIfNeeded(context.Background(), tt.account, tt.headers, AnthropicUsageSnapshotSource, dispatcher)
			if tt.wantCall && len(dispatcher.calls) != 1 {
				t.Fatalf("dispatch calls = %d, want 1", len(dispatcher.calls))
			}
			if !tt.wantCall && len(dispatcher.calls) != 0 {
				t.Fatalf("dispatch calls = %d, want 0", len(dispatcher.calls))
			}
			if tt.wantCall && dispatcher.calls[0] != "acc-1/"+AnthropicUsageSnapshotSource {
				t.Fatalf("call = %q", dispatcher.calls[0])
			}
		})
	}

	// nil 派发器静默。
	PersistAnthropicUsageHeadersIfNeeded(context.Background(),
		anthropicOAuthAccount, anthropicHeaders, AnthropicUsageSnapshotSource, nil)
}

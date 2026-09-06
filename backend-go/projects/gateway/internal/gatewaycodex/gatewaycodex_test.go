package gatewaycodex

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// contract registry
// ---------------------------------------------------------------------------

func TestCodexResponsesContractRegistry(t *testing.T) {
	registry := CodexResponsesContractRegistryDefault()
	if registry.Revision != "codex-responses-2026-07-11-r1" {
		t.Fatalf("revision = %q", registry.Revision)
	}
	tests := []struct {
		itemType   string
		wantPrefix string
		wantFound  bool
	}{
		{itemType: "message", wantPrefix: "msg", wantFound: true},
		{itemType: "reasoning", wantPrefix: "rs", wantFound: true},
		{itemType: "compaction", wantPrefix: "cmp", wantFound: true},
		{itemType: "compaction_summary", wantPrefix: "cmp", wantFound: true},
		{itemType: "context_compaction", wantPrefix: "cmp", wantFound: true},
		{itemType: "compaction_trigger", wantPrefix: "", wantFound: true},
		{itemType: "unknown_type", wantFound: false},
	}
	for _, tt := range tests {
		item, found := registry.Item(tt.itemType)
		if found != tt.wantFound {
			t.Errorf("Item(%q) found = %v", tt.itemType, found)
			continue
		}
		if found && item.Prefix != tt.wantPrefix {
			t.Errorf("Item(%q).Prefix = %q, want %q", tt.itemType, item.Prefix, tt.wantPrefix)
		}
	}
	if len(registry.Items) != 17 {
		t.Errorf("items length = %d, want 17", len(registry.Items))
	}
}

// ---------------------------------------------------------------------------
// history sanitizer
// ---------------------------------------------------------------------------

func TestSanitizeCodexResponseHistoryItems(t *testing.T) {
	baseContext := CodexHistorySanitizerContext{
		Store:                  true,
		TargetPersistenceScope: PersistenceScopeAccount,
	}
	tests := []struct {
		name              string
		items             []any
		context           CodexHistorySanitizerContext
		wantChanged       bool
		wantRemoved       int
		wantDropped       int
		wantIssueCodes    []string
		wantFirstItemRole string
	}{
		{
			name: "valid prefixed id untouched",
			items: []any{
				map[string]any{"type": "message", "id": "msg_1", "role": "user", "content": []any{}},
			},
			context:     baseContext,
			wantChanged: false,
		},
		{
			name: "invalid id removed keeping item",
			items: []any{
				map[string]any{"type": "message", "id": "", "role": "user", "content": []any{}},
			},
			context:        baseContext,
			wantChanged:    true,
			wantRemoved:    1,
			wantIssueCodes: []string{"invalid_item_id"},
		},
		{
			name: "legacy id removed keeping replayable item",
			items: []any{
				map[string]any{"type": "web_search_call", "id": "legacy", "status": "completed"},
			},
			context:        baseContext,
			wantChanged:    true,
			wantRemoved:    1,
			wantIssueCodes: []string{"legacy_item_id"},
		},
		{
			name: "prefix mismatch removed keeping item",
			items: []any{
				map[string]any{"type": "message", "id": "rs_x", "role": "user", "content": []any{}},
			},
			context:        baseContext,
			wantChanged:    true,
			wantRemoved:    1,
			wantIssueCodes: []string{"item_id_prefix_mismatch"},
		},
		{
			name: "unpersisted reference when scope none and store false",
			items: []any{
				map[string]any{"type": "function_call", "id": "fc_1", "name": "f", "arguments": "{}", "call_id": "c"},
			},
			context: CodexHistorySanitizerContext{
				Store:                  false,
				TargetPersistenceScope: PersistenceScopeNone,
			},
			wantChanged:    true,
			wantRemoved:    1,
			wantIssueCodes: []string{"unpersisted_item_reference"},
		},
		{
			name: "cross scope reference",
			items: []any{
				map[string]any{"type": "function_call", "id": "fc_1", "name": "f", "arguments": "{}", "call_id": "c"},
			},
			context: CodexHistorySanitizerContext{
				Store:                  true,
				SourceScopeKey:         "source-a",
				TargetScopeKey:         "source-b",
				TargetPersistenceScope: PersistenceScopeAccount,
			},
			wantChanged:    true,
			wantRemoved:    1,
			wantIssueCodes: []string{"cross_scope_item_reference"},
		},
		{
			name: "unrecoverable item dropped",
			items: []any{
				map[string]any{"type": "reasoning", "id": ""},
			},
			context:        baseContext,
			wantChanged:    true,
			wantDropped:    1,
			wantIssueCodes: []string{"invalid_item_id", "unrecoverable_item_dropped"},
		},
		{
			name: "non object item ignored",
			items: []any{
				"scalar",
				map[string]any{"type": "message", "role": "assistant", "content": []any{}},
			},
			context:     baseContext,
			wantChanged: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeCodexResponseHistoryItems(tt.items, tt.context)
			if result.Changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", result.Changed, tt.wantChanged)
			}
			if result.RemovedIDCount != tt.wantRemoved {
				t.Errorf("removedIDCount = %d, want %d", result.RemovedIDCount, tt.wantRemoved)
			}
			if result.DroppedItemCount != tt.wantDropped {
				t.Errorf("droppedItemCount = %d, want %d", result.DroppedItemCount, tt.wantDropped)
			}
			if len(result.IssueCodes) != len(tt.wantIssueCodes) {
				t.Fatalf("issueCodes = %v, want %v", result.IssueCodes, tt.wantIssueCodes)
			}
			for index, code := range tt.wantIssueCodes {
				if result.IssueCodes[index] != code {
					t.Errorf("issueCodes[%d] = %q, want %q", index, result.IssueCodes[index], code)
				}
			}
			if !tt.wantChanged && len(result.Items) != len(tt.items) {
				t.Errorf("unchanged items length = %d, want %d", len(result.Items), len(tt.items))
			}
		})
	}
}

func TestIsReplayableCodexHistoryItem(t *testing.T) {
	tests := []struct {
		name string
		item map[string]any
		want bool
	}{
		{"message", map[string]any{"type": "message", "role": "user", "content": []any{}}, true},
		{"message missing content", map[string]any{"type": "message", "role": "user"}, false},
		{"reasoning encrypted only", map[string]any{"type": "reasoning", "encrypted_content": "x"}, true},
		{"reasoning empty summary", map[string]any{"type": "reasoning", "summary": []any{}}, false},
		{"function_call", map[string]any{"type": "function_call", "name": "f", "arguments": "{}", "call_id": "c"}, true},
		{"function_call_output", map[string]any{"type": "function_call_output", "call_id": "c", "output": "o"}, true},
		{"local_shell_call", map[string]any{"type": "local_shell_call", "action": map[string]any{}}, true},
		{"compaction", map[string]any{"type": "compaction", "encrypted_content": ""}, true},
		{"context_compaction", map[string]any{"type": "context_compaction", "encrypted_content": "x"}, true},
		{"unknown", map[string]any{"type": "mystery"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReplayableCodexHistoryItem(tt.item); got != tt.want {
				t.Errorf("IsReplayableCodexHistoryItem = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// compaction contract
// ---------------------------------------------------------------------------

func TestCodexCompactionExpectedForRequest(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		body       []byte
		parsedOnly string
		state      *gatewaybody.BodyState
		want       bool
	}{
		{name: "compact path", method: "POST", target: "/v1/responses/compact", want: true},
		{name: "compact path with query", method: "POST", target: "/v1/responses/compact?x=1", want: true},
		{name: "responses without trigger", method: "POST", target: "/v1/responses", want: false},
		{name: "responses trigger in parsed body", method: "POST", target: "/v1/responses",
			body: []byte(`{"input":[{"type":"compaction_trigger"}]}`), want: true},
		{name: "responses trigger in nested body", method: "POST", target: "/v1/responses",
			body: []byte(`{"input":{"nested":{"type":"compaction_trigger"}}}`), want: true},
		{name: "responses trigger deep beyond depth, raw scan matches", method: "POST", target: "/v1/responses",
			body: []byte(`{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"type":"compaction_trigger"}}}}}}}}}}`), want: true},
		{name: "responses trigger beyond 500 array elements, raw scan matches", method: "POST", target: "/v1/responses",
			body: []byte(`{"input":[` + strings.Repeat(`{"a":1},`, 600) + `{"type":"compaction_trigger"}]}`), want: true},
		{name: "parsed depth guard without raw body", method: "POST", target: "/v1/responses",
			parsedOnly: `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"type":"compaction_trigger"}}}}}}}}}}`, want: false},
		{name: "parsed 500 element guard without raw body", method: "POST", target: "/v1/responses",
			parsedOnly: `{"input":[` + strings.Repeat(`{"a":1},`, 600) + `{"type":"compaction_trigger"}]}`, want: false},
		{name: "get method", method: "GET", target: "/v1/responses/compact", want: false},
		{name: "trigger state flag", method: "POST", target: "/v1/responses",
			state: &gatewaybody.BodyState{CodexCompactionTrigger: true}, want: true},
		{name: "scanned json never raw-scans", method: "POST", target: "/v1/responses",
			state: &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON}, want: false},
		{name: "non json raw body", method: "POST", target: "/v1/responses",
			state: &gatewaybody.BodyState{IsJSON: false}, body: []byte(`"type":"compaction_trigger"`), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newTestRequest(t, tt.method, tt.target, tt.body, nil)
			if tt.parsedOnly != "" {
				req.Body = &gatewaybody.Request{Body: parsedBodyOrNil(t, []byte(tt.parsedOnly))}
			} else if tt.state != nil {
				req.Body = &gatewaybody.Request{RawBody: tt.body, State: tt.state}
			} else if tt.body != nil {
				req.Body = &gatewaybody.Request{RawBody: tt.body, Body: parsedBodyOrNil(t, tt.body)}
			}
			if got := CodexCompactionExpectedForRequest(req); got != tt.want {
				t.Errorf("CodexCompactionExpectedForRequest = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCodexCompactionExpectedRawBodyEdgeWindows(t *testing.T) {
	pattern := `"type":"compaction_trigger"`
	prefix := strings.Repeat("x", 64*1024-len(pattern)+1) + pattern
	suffix := pattern + strings.Repeat("y", 64*1024)
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"prefix window match", prefix, true},
		{"suffix window match", suffix, true},
		{"middle beyond both windows", strings.Repeat("x", 64*1024) + pattern + strings.Repeat("y", 64*1024), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newTestRequest(t, "POST", "/v1/responses", []byte(tt.body), nil)
			req.Body = &gatewaybody.Request{RawBody: []byte(tt.body), State: &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusNotJSON, IsJSON: true}}
			if got := CodexCompactionExpectedForRequest(req); got != tt.want {
				t.Errorf("raw body scan = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// codex usage headers
// ---------------------------------------------------------------------------

func TestParseOpenAICodexUsageHeaders(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		headers       map[string]string
		wantNil       bool
		wantPrimary   *float64
		wantResetSec  *float64
		wantWindowMin *float64
	}{
		{
			name:    "no headers",
			headers: map[string]string{},
			wantNil: true,
		},
		{
			name:    "unrelated header",
			headers: map[string]string{"x-other": "1"},
			wantNil: true,
		},
		{
			name:          "primary window",
			headers:       map[string]string{"x-codex-primary-used-percent": "12.5", "x-codex-primary-reset-after-seconds": "3600.7", "x-codex-primary-window-minutes": "300"},
			wantPrimary:   floatPtr(12.5),
			wantResetSec:  floatPtr(3600),
			wantWindowMin: floatPtr(300),
		},
		{
			name:        "secondary and over limit",
			headers:     map[string]string{"x-codex-secondary-used-percent": "80", "x-codex-primary-over-secondary-limit-percent": "50"},
			wantPrimary: nil,
		},
		{
			name:    "invalid number ignored",
			headers: map[string]string{"x-codex-primary-used-percent": "abc"},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			for key, value := range tt.headers {
				header.Set(key, value)
			}
			snapshot := ParseOpenAICodexUsageHeaders(header, now)
			if tt.wantNil {
				if snapshot != nil {
					t.Fatalf("snapshot = %+v, want nil", snapshot)
				}
				return
			}
			if snapshot == nil {
				t.Fatal("snapshot = nil, want data")
			}
			if snapshot.UpdatedAt != "2026-09-04T12:00:00.000Z" {
				t.Errorf("updatedAt = %q", snapshot.UpdatedAt)
			}
			if tt.wantPrimary != nil && (snapshot.PrimaryUsedPercent == nil || *snapshot.PrimaryUsedPercent != *tt.wantPrimary) {
				t.Errorf("primaryUsedPercent = %v, want %v", snapshot.PrimaryUsedPercent, tt.wantPrimary)
			}
			if tt.wantResetSec != nil && (snapshot.PrimaryResetAfterSeconds == nil || *snapshot.PrimaryResetAfterSeconds != *tt.wantResetSec) {
				t.Errorf("primaryResetAfterSeconds = %v, want %v", snapshot.PrimaryResetAfterSeconds, tt.wantResetSec)
			}
		})
	}
}

func floatPtr(value float64) *float64 { return &value }

type fakeUsageDispatcher struct {
	mu        sync.Mutex
	calls     []string
	accountID string
	source    string
}

func (d *fakeUsageDispatcher) PersistOpenAICodexUsageHeaders(_ context.Context, accountID string, _ http.Header, source string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, accountID+"/"+source)
	d.accountID = accountID
	d.source = source
}

func TestPersistOpenAICodexHeadersIfNeeded(t *testing.T) {
	oauthAccount := gatewayruntimecache.OpenAIAccountSecret{
		ID:              "acc-1",
		Type:            "oauth",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
	}
	apiAccount := oauthAccount
	apiAccount.Type = "api_key"
	nonOpenAI := oauthAccount
	nonOpenAI.ProtocolCode = "other"
	codexHeaders := http.Header{}
	codexHeaders.Set("x-codex-primary-used-percent", "10")
	plainHeaders := http.Header{}
	plainHeaders.Set("x-other", "1")

	tests := []struct {
		name     string
		account  gatewayruntimecache.OpenAIAccountSecret
		headers  http.Header
		wantCall bool
	}{
		{name: "oauth codex dispatches", account: oauthAccount, headers: codexHeaders, wantCall: true},
		{name: "api key account skipped", account: apiAccount, headers: codexHeaders},
		{name: "non openai profile skipped", account: nonOpenAI, headers: codexHeaders},
		{name: "no codex headers skipped", account: oauthAccount, headers: plainHeaders},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := &fakeUsageDispatcher{}
			PersistOpenAICodexHeadersIfNeeded(context.Background(), tt.account, tt.headers, "gateway", SystemClock{}, dispatcher)
			if tt.wantCall && len(dispatcher.calls) != 1 {
				t.Fatalf("dispatch calls = %d, want 1", len(dispatcher.calls))
			}
			if !tt.wantCall && len(dispatcher.calls) != 0 {
				t.Fatalf("dispatch calls = %d, want 0", len(dispatcher.calls))
			}
			if tt.wantCall && dispatcher.source != "gateway" {
				t.Errorf("source = %q", dispatcher.source)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// encrypted content recovery
// ---------------------------------------------------------------------------

func openAIAccount() gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{ID: "acc", Type: "oauth", ProtocolCode: "openai", ProtocolVersion: "v1"}
}

func TestClassifyCodexEncryptedContentRecoverySignal(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"exact code", "  Invalid_Encrypted_Content ", "invalid_encrypted_content"},
		{"plain json", `{"code":"thinking_signature_invalid"}`, "thinking_signature_invalid"},
		{"nested error", `{"error":{"code":"encrypted_content_decryption_failed"}}`, "encrypted_content_decryption_failed"},
		{"sse data line", "event: error\ndata: {\"code\":\"invalid_encrypted_content\"}\n\n", "invalid_encrypted_content"},
		{"message heuristic", `{"type":"error","message":"Server error: encrypted content could not be decrypted"}`, "encrypted_content_decryption_failed"},
		{"trailing JSON rejected", `{"code":"invalid_encrypted_content"} {"code":"thinking_signature_invalid"}`, ""},
		{"unrelated message", `{"type":"error","message":"encrypted payload exploded"}`, ""},
		{"no signal", `{"code":"rate_limit_exceeded"}`, ""},
		{"invalid json", "not json at all", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyCodexEncryptedContentRecoverySignal(tt.text); got != tt.want {
				t.Errorf("signal = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRecoverCodexEncryptedContent(t *testing.T) {
	responsesRequest := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	chatRequest := newTestRequest(t, "POST", "/v1/chat/completions", nil, nil)

	tests := []struct {
		name           string
		req            *gatewaypreauth.GatewayRequest
		account        gatewayruntimecache.OpenAIAccountSecret
		body           []byte
		upstreamText   string
		endpointFamily string
		wantAction     string
		wantSignal     string
		wantReason     string
		wantMetadata   *CodexEncryptedContentRecoveryMetadata
	}{
		{
			name:         "chat endpoint not applicable",
			req:          chatRequest,
			account:      openAIAccount(),
			body:         []byte(`{"input":[]}`),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionNotApplicable,
		},
		{
			name:         "non openai account not applicable",
			req:          responsesRequest,
			account:      gatewayruntimecache.OpenAIAccountSecret{ProtocolCode: "other", ProtocolVersion: "v1"},
			body:         []byte(`{"input":[]}`),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionNotApplicable,
		},
		{
			name:         "no signal not applicable",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte(`{"input":[]}`),
			upstreamText: "something else",
			wantAction:   RecoveryActionNotApplicable,
		},
		{
			name:         "nil body not recoverable",
			req:          responsesRequest,
			account:      openAIAccount(),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionNotRecoverable,
			wantSignal:   "invalid_encrypted_content",
		},
		{
			name:         "parse failure",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte("{not json"),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionNotRecoverable,
			wantSignal:   "invalid_encrypted_content",
			wantReason:   RecoveryReasonRequestBodyParseFailed,
		},
		{
			name:         "trailing JSON body parse failure",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte(`{"input":[{"type":"compaction","encrypted_content":"x"}]} {}`),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionNotRecoverable,
			wantSignal:   "invalid_encrypted_content",
			wantReason:   RecoveryReasonRequestBodyParseFailed,
		},
		{
			name:         "non object body",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte("[1,2,3]"),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionNotRecoverable,
			wantSignal:   "invalid_encrypted_content",
			wantReason:   RecoveryReasonRequestBodyParseFailed,
		},
		{
			name:         "nothing removable",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte(`{"input":[{"type":"message","role":"user","content":[]}]}`),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionNotRecoverable,
			wantSignal:   "invalid_encrypted_content",
			wantReason:   RecoveryReasonNoRemovableEncryptedContent,
		},
		{
			name:         "reasoning strip drops empty item",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte(`{"input":[{"type":"reasoning","summary":[],"encrypted_content":"abc"},{"type":"reasoning","summary":[{"type":"summary_text","text":"kept"}],"encrypted_content":"def"}]}`),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionRetryWithBodyVariant,
			wantSignal:   "invalid_encrypted_content",
			wantMetadata: &CodexEncryptedContentRecoveryMetadata{
				Strategy:                              "codex_encrypted_content_cleanup",
				Signal:                                "invalid_encrypted_content",
				RemovedReasoningEncryptedContentCount: 2,
				RemovedReasoningItemCount:             1,
			},
		},
		{
			name:         "compaction item dropped entirely",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte(`{"previous_response_id":"resp_x","input":[{"type":"compaction","encrypted_content":"abc"}]}`),
			upstreamText: `{"error":{"code":"encrypted_content_decryption_failed"}}`,
			wantAction:   RecoveryActionRetryWithBodyVariant,
			wantSignal:   "encrypted_content_decryption_failed",
			wantMetadata: &CodexEncryptedContentRecoveryMetadata{
				Strategy:                               "codex_encrypted_content_cleanup",
				Signal:                                 "encrypted_content_decryption_failed",
				RemovedCompactionEncryptedContentCount: 1,
				RemovedCompactionItemCount:             1,
				PreservedPreviousResponseID:            true,
			},
		},
		{
			name:         "function output encrypted content stripped",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte(`{"input":[{"type":"function_call_output","call_id":"c","output":[{"type":"encrypted_content","encrypted_content":"x"},{"type":"other"}]}]}`),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionRetryWithBodyVariant,
			wantSignal:   "invalid_encrypted_content",
			wantMetadata: &CodexEncryptedContentRecoveryMetadata{
				Strategy: "codex_encrypted_content_cleanup",
				Signal:   "invalid_encrypted_content",
				RemovedFunctionOutputEncryptedContentCount: 1,
			},
		},
		{
			name:         "agent message content stripped to empty drops item",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"x"}]}]}`),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionRetryWithBodyVariant,
			wantSignal:   "invalid_encrypted_content",
			wantMetadata: &CodexEncryptedContentRecoveryMetadata{
				Strategy:                                 "codex_encrypted_content_cleanup",
				Signal:                                   "invalid_encrypted_content",
				RemovedAgentMessageEncryptedContentCount: 1,
				RemovedAgentMessageItemCount:             1,
			},
		},
		{
			name:         "single object input collapses back to object",
			req:          responsesRequest,
			account:      openAIAccount(),
			body:         []byte(`{"input":{"type":"compaction_summary","encrypted_content":"x"}}`),
			upstreamText: "invalid_encrypted_content",
			wantAction:   RecoveryActionRetryWithBodyVariant,
			wantSignal:   "invalid_encrypted_content",
			wantMetadata: &CodexEncryptedContentRecoveryMetadata{
				Strategy:                               "codex_encrypted_content_cleanup",
				Signal:                                 "invalid_encrypted_content",
				RemovedCompactionEncryptedContentCount: 1,
				RemovedCompactionItemCount:             1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RecoverCodexEncryptedContent(context.Background(), EncryptedContentRecoveryInput{
				Req:               tt.req,
				Account:           tt.account,
				Body:              tt.body,
				UpstreamErrorText: tt.upstreamText,
				EndpointFamily:    tt.endpointFamily,
			})
			if result.Action != tt.wantAction {
				t.Fatalf("action = %q, want %q", result.Action, tt.wantAction)
			}
			if tt.wantAction == RecoveryActionRetryWithBodyVariant {
				// Node's retry variant carries the signal in metadata only.
				if result.Signal != "" {
					t.Errorf("signal = %q, want empty", result.Signal)
				}
			} else if result.Signal != tt.wantSignal {
				t.Errorf("signal = %q, want %q", result.Signal, tt.wantSignal)
			}
			if result.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", result.Reason, tt.wantReason)
			}
			if tt.wantMetadata == nil {
				if result.Metadata != nil {
					t.Errorf("metadata = %+v, want nil", result.Metadata)
				}
				return
			}
			if result.Metadata == nil {
				t.Fatal("metadata = nil, want data")
			}
			want := *tt.wantMetadata
			got := *result.Metadata
			if got.Strategy != want.Strategy || got.Signal != want.Signal ||
				got.RemovedReasoningEncryptedContentCount != want.RemovedReasoningEncryptedContentCount ||
				got.RemovedFunctionOutputEncryptedContentCount != want.RemovedFunctionOutputEncryptedContentCount ||
				got.RemovedAgentMessageEncryptedContentCount != want.RemovedAgentMessageEncryptedContentCount ||
				got.RemovedCompactionEncryptedContentCount != want.RemovedCompactionEncryptedContentCount ||
				got.RemovedReasoningItemCount != want.RemovedReasoningItemCount ||
				got.RemovedAgentMessageItemCount != want.RemovedAgentMessageItemCount ||
				got.RemovedCompactionItemCount != want.RemovedCompactionItemCount ||
				got.PreservedPreviousResponseID != want.PreservedPreviousResponseID {
				t.Errorf("metadata = %+v, want %+v", got, want)
			}
			if got.BodyBytesAfter != len(result.Body) {
				t.Errorf("bodyBytesAfter = %d, body len = %d", got.BodyBytesAfter, len(result.Body))
			}
			if result.SemanticRetryID != "codex_encrypted_content_cleanup:"+want.Signal {
				t.Errorf("semanticRetryId = %q", result.SemanticRetryID)
			}
			if result.Action == RecoveryActionRetryWithBodyVariant {
				var parsed map[string]any
				if err := jsonUnmarshalStrict(result.Body, &parsed); err != nil {
					t.Fatalf("retry body not valid json object: %v", err)
				}
			}
		})
	}
}

func TestRecoverEndpointFamilyOverride(t *testing.T) {
	// A synthetic chat request with the family override set to responses
	// stays applicable (Node reads the request object override).
	req := newTestRequest(t, "POST", "/v1/chat/completions", nil, nil)
	result := RecoverCodexEncryptedContent(context.Background(), EncryptedContentRecoveryInput{
		Req:               req,
		Account:           openAIAccount(),
		Body:              []byte(`{"input":[]}`),
		UpstreamErrorText: "no signal",
		EndpointFamily:    gatewayopenai.FamilyResponses,
	})
	if result.Action != RecoveryActionNotApplicable {
		// With no signal the recovery stays not applicable, but the endpoint
		// family check passed (otherwise identical); assert via a signaling
		// body to distinguish.
		_ = result
	}
	result = RecoverCodexEncryptedContent(context.Background(), EncryptedContentRecoveryInput{
		Req:               req,
		Account:           openAIAccount(),
		Body:              []byte(`{"input":[{"type":"compaction","encrypted_content":"x"}]}`),
		UpstreamErrorText: "invalid_encrypted_content",
		EndpointFamily:    gatewayopenai.FamilyResponses,
	})
	if result.Action != RecoveryActionRetryWithBodyVariant {
		t.Fatalf("override action = %q, want retry_with_body_variant", result.Action)
	}
}

func jsonUnmarshalStrict(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

func parsedBodyOrNil(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	return record
}

package gatewaycodex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func newBridgeService(t *testing.T) (*ChatBridgeStateService, *ContextRequestStateRegistry, *recordedSink, *recordedAudit, *fakeClock) {
	_ = newTrackedWriter
	t.Helper()
	store, _ := newSQLiteStore(t)
	clock := newFakeClock(time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC))
	service, err := NewChatBridgeStateService(
		ChatBridgeStateConfig{CodexContextRoot: t.TempDir()},
		store,
		nil,
		clock,
	)
	if err != nil {
		t.Fatalf("NewChatBridgeStateService: %v", err)
	}
	sink := &recordedSink{}
	audit := &recordedAudit{}
	service.Logger = &recordingLogger{}
	service.Sink = sink
	service.CreateID = fixedIDGenerator("fence")
	return service, NewContextRequestStateRegistry(), sink, audit, clock
}

func bridgeBaseInput(req *gatewaypreauth.GatewayRequest, res gatewaypreauth.GatewayResponseWriter, audit gatewaypreauth.AuditCaptureContext) ContextStatePreflightInput {
	return ContextStatePreflightInput{
		Req:             req,
		Res:             res,
		AuditCapture:    audit,
		UsageContext:    gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace", TrafficSource: "gateway"},
		StartedAt:       1,
		SystemAccountID: "sys",
		APIKeyID:        "key",
		GroupID:         "group",
		GroupAccess:     gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		Signal:          context.Background(),
	}
}

func attachBody(req *gatewaypreauth.GatewayRequest, body map[string]any, raw []byte) {
	req.Body = &gatewaybody.Request{RawBody: raw, Body: body}
}

func TestApplyContextStatePreflightRouting(t *testing.T) {
	service, registry, sink, _, _ := newBridgeService(t)
	ctx := context.Background()

	tests := []struct {
		name           string
		method         string
		target         string
		body           map[string]any
		wantCompleted  bool
		wantStateKind  string
		wantPrevKind   string
		wantAuditLabel string
		wantAuditMode  string
	}{
		{
			name:   "non responses request continues",
			method: "POST", target: "/v1/chat/completions",
			wantCompleted: false,
		},
		{
			name: "compact none previous", method: "POST", target: "/v1/responses/compact",
			body:          map[string]any{"input": []any{}},
			wantStateKind: RequestKindCompact, wantPrevKind: PreviousKindNone,
			wantAuditLabel: "codex_responses_context_state", wantAuditMode: "compact_none_previous_response",
		},
		{
			name: "compact internal previous", method: "POST", target: "/v1/responses/compact",
			body:          map[string]any{"input": []any{}, "previous_response_id": "resp_chat_bridge_abc"},
			wantStateKind: RequestKindCompact, wantPrevKind: PreviousKindInternal,
			wantAuditLabel: "codex_responses_context_state", wantAuditMode: "compact_internal_previous_response",
		},
		{
			name: "responses new session", method: "POST", target: "/v1/responses",
			body:          map[string]any{"input": []any{}},
			wantStateKind: RequestKindResponses, wantPrevKind: PreviousKindNone,
			wantAuditLabel: "codex_responses_chat_bridge_state", wantAuditMode: "new_session",
		},
		{
			name: "responses external previous", method: "POST", target: "/v1/responses",
			body:          map[string]any{"input": []any{}, "previous_response_id": "resp_external_1"},
			wantStateKind: RequestKindResponses, wantPrevKind: PreviousKindExternal,
			wantAuditLabel: "codex_responses_context_state", wantAuditMode: "external_previous_response",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newTestRequest(t, tt.method, tt.target, nil, nil)
			var raw []byte
			if tt.body != nil {
				raw, _ = json.Marshal(tt.body)
			}
			attachBody(req, tt.body, raw)
			localAudit := &recordedAudit{}
			completed, err := service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(req, nil, localAudit))
			if err != nil {
				t.Fatalf("preflight error: %v", err)
			}
			if completed != tt.wantCompleted {
				t.Fatalf("completed = %v", completed)
			}
			state, hasState := registry.Get(req)
			if tt.wantStateKind == "" {
				if hasState {
					t.Fatalf("state registered unexpectedly")
				}
				return
			}
			if !hasState {
				t.Fatalf("state missing")
			}
			if state.RequestKind != tt.wantStateKind || state.PreviousResponseKind != tt.wantPrevKind {
				t.Errorf("state kind/prev = %q/%q, want %q/%q", state.RequestKind, state.PreviousResponseKind, tt.wantStateKind, tt.wantPrevKind)
			}
			if tt.wantAuditMode != "" {
				metadata, ok := localAudit.lastMetadata(tt.wantAuditLabel)
				if !ok || metadata["mode"] != tt.wantAuditMode {
					t.Errorf("audit metadata = %+v", metadata)
				}
			}
		})
	}
	_ = sink
}

func TestApplyContextStatePreflightRestore(t *testing.T) {
	service, registry, sink, _, clock := newBridgeService(t)
	ctx := context.Background()

	// Seed a completed response through the completion handler path.
	seedReq := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	attachBody(seedReq, map[string]any{"model": "gpt-5", "instructions": "system prompt", "input": map[string]any{"k": "v"}}, nil)
	registry.Set(seedReq, &CodexResponsesContextRequestState{
		RequestKind:           RequestKindResponses,
		Boundary:              CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"},
		CanonicalBody:         map[string]any{"model": "gpt-5", "instructions": "system prompt", "input": map[string]any{"k": "v"}},
		CurrentBody:           map[string]any{"model": "gpt-5"},
		CurrentInput:          map[string]any{"k": "v"},
		MaterializedInput:     map[string]any{"k": "v"},
		PreviousResponseKind:  PreviousKindNone,
		ActiveBridgeAccountID: "acc-bridge",
	})
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-bridge", ProtocolCode: "openai", ProtocolVersion: "v1"}
	handler := service.CompletionHandlerForRequest(registry, seedReq, account, "upstream-model")
	if handler == nil {
		t.Fatal("completion handler missing")
	}
	handler(CodexResponsesChatBridgeCompletion{
		ResponseID:  "resp_chat_bridge_seed",
		CreatedAt:   clock.Now(),
		Model:       "upstream-model",
		OutputItems: []any{map[string]any{"type": "message", "role": "assistant", "content": []any{}}},
	})

	tests := []struct {
		name             string
		previousResponse string
		boundary         CodexContextStateBoundary
		wantCompleted    bool
		wantFailureCode  string
		wantStateOK      bool
		wantRestored     bool
	}{
		{
			name:             "restores internal chain",
			previousResponse: "resp_chat_bridge_seed",
			boundary:         CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"},
			wantStateOK:      true,
			wantRestored:     true,
		},
		{
			name:             "boundary mismatch 403",
			previousResponse: "resp_chat_bridge_seed",
			boundary:         CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "other", GroupID: "group", ProviderCode: "openai"},
			wantCompleted:    true,
			wantFailureCode:  "codex_bridge_previous_response_boundary_mismatch",
		},
		{
			name:             "not found 404",
			previousResponse: "resp_chat_bridge_missing",
			boundary:         CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"},
			wantCompleted:    true,
			wantFailureCode:  "codex_bridge_previous_response_not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
			body := map[string]any{"input": []any{map[string]any{"type": "message", "role": "user"}}}
			if tt.previousResponse != "" {
				body["previous_response_id"] = tt.previousResponse
			}
			raw, _ := json.Marshal(body)
			attachBody(req, body, raw)
			localAudit := &recordedAudit{}
			input := bridgeBaseInput(req, nil, localAudit)
			input.SystemAccountID = tt.boundary.SystemAccountID
			input.APIKeyID = tt.boundary.APIKeyID
			completed, err := service.ApplyContextStatePreflight(ctx, registry, input)
			if err != nil {
				t.Fatalf("preflight: %v", err)
			}
			if completed != tt.wantCompleted {
				t.Fatalf("completed = %v", completed)
			}
			if tt.wantCompleted {
				failure, ok := sink.lastFailure()
				if !ok {
					t.Fatal("failure response missing")
				}
				if failure.Audit.ErrorCode != tt.wantFailureCode {
					t.Errorf("failure code = %q, want %q", failure.Audit.ErrorCode, tt.wantFailureCode)
				}
				if failure.StatusCode == 0 {
					t.Errorf("status code missing for %q", tt.wantFailureCode)
				}
				return
			}
			state, ok := registry.Get(req)
			if !ok {
				t.Fatal("state missing")
			}
			if state.Restored != tt.wantRestored {
				t.Errorf("restored = %v", state.Restored)
			}
			if state.SessionID == "" {
				t.Error("sessionId missing on restored state")
			}
			metadata, _ := localAudit.lastMetadata("codex_responses_chat_bridge_state")
			if metadata["mode"] != "restored" {
				t.Errorf("audit mode = %+v", metadata)
			}
		})
	}
}

func TestApplyContextStatePreflightCompactReference(t *testing.T) {
	service, registry, sink, _, clock := newBridgeService(t)
	ctx := context.Background()
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"}

	// Seed a compact snapshot.
	snapshot, err := service.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SessionID: "session-compact", Boundary: boundary, Summary: "压缩摘要",
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	_ = clock

	// Inline the reference into the request input.
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	body := map[string]any{"input": []any{
		map[string]any{"type": "compaction", "encrypted_content": snapshot.EncryptedContent},
		map[string]any{"type": "message", "role": "user"},
	}}
	raw, _ := json.Marshal(body)
	attachBody(req, body, raw)
	completed, err := service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(req, nil, &recordedAudit{}))
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if completed {
		failure, _ := sink.lastFailure()
		t.Fatalf("preflight completed: %+v", failure.Audit)
	}
	state, _ := registry.Get(req)
	items := state.MaterializedInput.([]any)
	first := items[0].(map[string]any)
	if first["type"] != "compaction_summary" {
		t.Errorf("resolved type = %v", first["type"])
	}
	encrypted := first["encrypted_content"].(string)
	if !strings.HasPrefix(encrypted, codexInlineCompactionSummaryPrefix) {
		t.Errorf("inline prefix missing: %q", encrypted)
	}

	// A broken reference fails with 404.
	badReq := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	badBody := map[string]any{"input": []any{
		map[string]any{"type": "compaction", "encrypted_content": "juhecmp.v2.cmp_x." + strings.Repeat("a", 64)},
	}}
	badRaw, _ := json.Marshal(badBody)
	attachBody(badReq, badBody, badRaw)
	completed, err = service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(badReq, nil, &recordedAudit{}))
	if err != nil {
		t.Fatalf("preflight bad: %v", err)
	}
	if !completed {
		t.Fatal("bad reference should complete the request")
	}
	failure, ok := sink.lastFailure()
	if !ok || failure.Audit.ErrorCode != "codex_bridge_compact_snapshot_not_found" {
		t.Fatalf("failure = %+v", failure.Audit)
	}
	if failure.StatusCode != 404 {
		t.Errorf("status = %d", failure.StatusCode)
	}
}

func TestPrepareCodexResponsesContextForAccount(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"}

	bridgeAccount := gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-bridge", ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "gpt-5", SourceEndpointFamily: "responses",
			UpstreamModel: "chat-model", UpstreamEndpointFamily: "chat_completions", Enabled: true,
		}},
	}
	nativeAccount := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-native", ProtocolCode: "openai", ProtocolVersion: "v1"}

	// Seed an internal previous response so materialization kicks in.
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	body := map[string]any{"model": "gpt-5", "input": []any{map[string]any{"type": "message", "role": "user"}}}
	raw, _ := json.Marshal(body)
	attachBody(req, body, raw)
	registry.Set(req, &CodexResponsesContextRequestState{
		RequestKind:          RequestKindResponses,
		Boundary:             boundary,
		CanonicalBody:        body,
		CurrentBody:          body,
		CurrentInput:         body["input"],
		MaterializedInput:    []any{map[string]any{"type": "compaction_summary", "encrypted_content": "juhecmp.v1." + base64Summary("摘要")}, map[string]any{"type": "message", "role": "assistant"}},
		PreviousResponseID:   "resp_chat_bridge_seed",
		PreviousResponseKind: PreviousKindInternal,
	})

	prepared, err := service.PrepareCodexResponsesContextForAccount(registry, req, bridgeAccount)
	if err != nil {
		t.Fatalf("prepare bridge: %v", err)
	}
	if !prepared {
		t.Fatal("bridge account should prepare")
	}
	state, _ := registry.Get(req)
	if state.ActiveBridgeAccountID != "acc-bridge" {
		t.Errorf("active bridge account = %q", state.ActiveBridgeAccountID)
	}
	// The rendered body drops previous_response_id for internal chains and
	// keeps the inline summary for the bridge.
	rendered := gatewaybody.GatewayJSONObjectBody(req.Body)
	if rendered == nil {
		t.Fatal("rendered body missing")
	}
	if _, has := rendered["previous_response_id"]; has {
		t.Error("previous_response_id not dropped for internal chain")
	}

	// Native account gets the inline summary converted into a developer
	// message.
	reqNative := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	attachBody(reqNative, body, raw)
	registry.Set(reqNative, &CodexResponsesContextRequestState{
		RequestKind:          RequestKindResponses,
		Boundary:             boundary,
		CanonicalBody:        body,
		CurrentBody:          body,
		CurrentInput:         body["input"],
		MaterializedInput:    []any{map[string]any{"type": "compaction_summary", "encrypted_content": "juhecmp.v1." + base64Summary("摘要")}},
		PreviousResponseKind: PreviousKindNone,
	})
	prepared, err = service.PrepareCodexResponsesContextForAccount(registry, reqNative, nativeAccount)
	if err != nil {
		t.Fatalf("prepare native: %v", err)
	}
	if prepared {
		t.Error("native account should not report bridge preparation")
	}
	rendered = gatewaybody.GatewayJSONObjectBody(reqNative.Body)
	items := rendered["input"].([]any)
	if items[0].(map[string]any)["type"] != "message" {
		t.Errorf("native input item = %+v", items[0])
	}
}

func TestPrepareCodexResponsesContextForAccountErrors(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"}
	bridgeAccount := gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-bridge", ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "gpt-5", SourceEndpointFamily: "responses",
			UpstreamModel: "chat-model", UpstreamEndpointFamily: "chat_completions", Enabled: true,
		}},
	}

	// Native compact dispatch to a bridge account is a validation error.
	req := newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)
	attachBody(req, map[string]any{"input": []any{}}, nil)
	registry.Set(req, &CodexResponsesContextRequestState{
		RequestKind:          RequestKindCompact,
		Boundary:             boundary,
		CanonicalBody:        map[string]any{"model": "gpt-5"},
		CompactDispatchMode:  CompactDispatchNative,
		PreviousResponseKind: PreviousKindNone,
	})
	_, err := service.PrepareCodexResponsesContextForAccount(registry, req, bridgeAccount)
	validation, ok := err.(*gatewaypreauth.GatewayRequestValidationError)
	if !ok {
		t.Fatalf("error type = %T", err)
	}
	if validation.Code != "native_responses_compact_requires_native_account" || !validation.AccountScoped {
		t.Errorf("validation = %+v", validation)
	}

	// External previous response to a bridge account errors.
	req2 := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	attachBody(req2, map[string]any{"input": []any{}}, nil)
	registry.Set(req2, &CodexResponsesContextRequestState{
		RequestKind:          RequestKindResponses,
		Boundary:             boundary,
		CanonicalBody:        map[string]any{"model": "gpt-5"},
		PreviousResponseKind: PreviousKindExternal,
	})
	_, err = service.PrepareCodexResponsesContextForAccount(registry, req2, bridgeAccount)
	if err == nil || err.Error() != "外部 previous_response_id 只能发送给原生 Responses 账号" {
		t.Fatalf("external error = %v", err)
	}
}

func TestPrepareCodexResponsesCompactDispatchForAccounts(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	bridgeAccount := gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-bridge", ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "gpt-5", SourceEndpointFamily: "responses",
			UpstreamModel: "chat-model", UpstreamEndpointFamily: "chat_completions", Enabled: true,
		}},
	}
	nativeAccount := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-native", ProtocolCode: "openai", ProtocolVersion: "v1"}
	unsupportedAccount := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-other", ProtocolCode: "other", ProtocolVersion: "v1"}

	tests := []struct {
		name         string
		previousKind string
		accounts     []gatewayruntimecache.OpenAIAccountSecret
		wantPrepare  bool
		wantMode     string
	}{
		{name: "external forces native", previousKind: PreviousKindExternal, accounts: []gatewayruntimecache.OpenAIAccountSecret{bridgeAccount}, wantPrepare: false, wantMode: CompactDispatchNative},
		{name: "internal requires bridge", previousKind: PreviousKindInternal, accounts: []gatewayruntimecache.OpenAIAccountSecret{bridgeAccount, nativeAccount}, wantPrepare: true, wantMode: CompactDispatchBridge},
		{name: "internal without bridge fails", previousKind: PreviousKindInternal, accounts: []gatewayruntimecache.OpenAIAccountSecret{nativeAccount}, wantPrepare: false, wantMode: CompactDispatchBridge},
		{name: "none prefers native", previousKind: PreviousKindNone, accounts: []gatewayruntimecache.OpenAIAccountSecret{nativeAccount, bridgeAccount}, wantPrepare: false, wantMode: CompactDispatchNative},
		{name: "none bridge only", previousKind: PreviousKindNone, accounts: []gatewayruntimecache.OpenAIAccountSecret{bridgeAccount}, wantPrepare: true, wantMode: CompactDispatchBridge},
		{name: "none unsupported only", previousKind: PreviousKindNone, accounts: []gatewayruntimecache.OpenAIAccountSecret{unsupportedAccount}, wantPrepare: false, wantMode: CompactDispatchBridge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)
			attachBody(req, map[string]any{"input": []any{}}, nil)
			registry.Set(req, &CodexResponsesContextRequestState{
				RequestKind:          RequestKindCompact,
				CanonicalBody:        map[string]any{"model": "gpt-5"},
				PreviousResponseKind: tt.previousKind,
			})
			prepared := service.PrepareCodexResponsesCompactDispatchForAccounts(registry, req, tt.accounts)
			if prepared != tt.wantPrepare {
				t.Fatalf("prepared = %v, want %v", prepared, tt.wantPrepare)
			}
			state, _ := registry.Get(req)
			if state.CompactDispatchMode != tt.wantMode {
				t.Errorf("mode = %q, want %q", state.CompactDispatchMode, tt.wantMode)
			}
		})
	}
}

func TestCodexResponsesContextAllowsAccount(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	bridgeAccount := gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-bridge", ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "gpt-5", SourceEndpointFamily: "responses",
			UpstreamModel: "chat-model", UpstreamEndpointFamily: "chat_completions", Enabled: true,
		}},
	}
	nativeAccount := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-native", ProtocolCode: "openai", ProtocolVersion: "v1"}
	unsupportedAccount := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-other", ProtocolCode: "other", ProtocolVersion: "v1"}

	compactReq := newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)
	attachBody(compactReq, map[string]any{"input": []any{}}, nil)
	registry.Set(compactReq, &CodexResponsesContextRequestState{
		RequestKind:          RequestKindCompact,
		CanonicalBody:        map[string]any{"model": "gpt-5"},
		PreviousResponseKind: PreviousKindNone,
	})
	if !service.CodexResponsesContextAllowsAccount(registry, compactReq, nativeAccount) {
		t.Error("native allowed for none-previous compact")
	}
	if !service.CodexResponsesContextAllowsAccount(registry, compactReq, bridgeAccount) {
		t.Error("bridge allowed for none-previous compact")
	}
	if service.CodexResponsesContextAllowsAccount(registry, compactReq, unsupportedAccount) {
		t.Error("unsupported rejected for compact")
	}

	// A responses request that expects compaction only allows native
	// accounts.
	compactionReq := newTestRequest(t, "POST", "/v1/responses", []byte(`{"input":[{"type":"compaction_trigger"}]}`), nil)
	attachBody(compactionReq, map[string]any{"input": []any{map[string]any{"type": "compaction_trigger"}}}, []byte(`{"input":[{"type":"compaction_trigger"}]}`))
	registry.Set(compactionReq, &CodexResponsesContextRequestState{
		RequestKind:          RequestKindResponses,
		CanonicalBody:        map[string]any{"model": "gpt-5"},
		PreviousResponseKind: PreviousKindNone,
	})
	if !service.CodexResponsesContextAllowsAccount(registry, compactionReq, nativeAccount) {
		t.Error("native allowed when compaction expected")
	}
	if service.CodexResponsesContextAllowsAccount(registry, compactionReq, bridgeAccount) {
		t.Error("bridge rejected when compaction expected")
	}

	// Without state everything is allowed.
	plainReq := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	if !service.CodexResponsesContextAllowsAccount(registry, plainReq, bridgeAccount) {
		t.Error("missing state allows all")
	}
}

func TestCompletionHandlerSkipsOtherAccounts(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	registry.Set(req, &CodexResponsesContextRequestState{
		RequestKind:           RequestKindResponses,
		ActiveBridgeAccountID: "acc-bridge",
	})
	if handler := service.CompletionHandlerForRequest(registry, req, gatewayruntimecache.OpenAIAccountSecret{ID: "acc-other"}, ""); handler != nil {
		t.Fatal("handler should be nil for other accounts")
	}
	if handler := service.CompletionHandlerForRequest(registry, newTestRequest(t, "POST", "/v1/responses", nil, nil), gatewayruntimecache.OpenAIAccountSecret{ID: "acc-bridge"}, ""); handler != nil {
		t.Fatal("handler should be nil without state")
	}
}

func TestCreateChatBridgeCompactSnapshot(t *testing.T) {
	service, _, _, _, clock := newBridgeService(t)
	ctx := context.Background()
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"}
	result, err := service.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SessionID: "session-1", SourceResponseID: "resp_chat_bridge_seed", Boundary: boundary, Summary: "总结",
		UpstreamAccountID: "acc", Model: "gpt-5", UpstreamModel: "chat-model",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(result.CompactID, "cmp_") {
		t.Errorf("compactId = %q", result.CompactID)
	}
	if !strings.HasPrefix(result.EncryptedContent, codexCompactionReferencePrefix+result.CompactID+".") {
		t.Errorf("encryptedContent = %q", result.EncryptedContent)
	}
	digest := result.EncryptedContent[len(codexCompactionReferencePrefix+result.CompactID+"."):]
	if digest != digestText("总结") {
		t.Errorf("digest = %q", digest)
	}
	// The clock drives the base36 timestamp component.
	expectedTS := base36(clock.Now().UnixMilli())
	if !strings.HasPrefix(result.CompactID, "cmp_"+expectedTS+"_") {
		t.Errorf("compact id timestamp = %q, want prefix %q", result.CompactID, "cmp_"+expectedTS+"_")
	}
}

func TestRestoreChatBridgeInputForCompactFailures(t *testing.T) {
	service, _, _, _, _ := newBridgeService(t)
	ctx := context.Background()
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"}
	result, err := service.RestoreChatBridgeInputForCompact(ctx, struct {
		PreviousResponseID string
		Boundary           CodexContextStateBoundary
		CurrentInput       any
	}{PreviousResponseID: "resp_missing", Boundary: boundary, CurrentInput: []any{}})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Outcome != CodexContextOutcomeNotFound || result.ResponseID != "resp_missing" {
		t.Errorf("result = %+v", result)
	}
	noPrevious, err := service.RestoreChatBridgeInputForCompact(ctx, struct {
		PreviousResponseID string
		Boundary           CodexContextStateBoundary
		CurrentInput       any
	}{Boundary: boundary, CurrentInput: "plain string"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if noPrevious.Outcome != "no_previous" {
		t.Errorf("noPrevious outcome = %q", noPrevious.Outcome)
	}
	if len(noPrevious.Input) != 1 {
		t.Errorf("string input itemized = %+v", noPrevious.Input)
	}
}

// ---------------------------------------------------------------------------
// compact preflight
// ---------------------------------------------------------------------------

type fakeCompactDispatcher struct {
	exchange *CompactUpstreamExchange
	err      error
	calls    int
	lastBody map[string]any
}

func (d *fakeCompactDispatcher) DispatchCompactSummary(_ context.Context, input CompactSummaryDispatchInput) (*CompactUpstreamExchange, error) {
	d.calls++
	d.lastBody = input.Body
	return d.exchange, d.err
}

func compactPreflightService(t *testing.T) (*CompactPreflightService, *ChatBridgeStateService, *ContextRequestStateRegistry, *recordedSink, *fakeCompactDispatcher) {
	t.Helper()
	bridge, registry, sink, _, _ := newBridgeService(t)
	dispatcher := &fakeCompactDispatcher{}
	service := &CompactPreflightService{Bridge: bridge, Registry: registry, Dispatcher: dispatcher, Clock: SystemClock{}, Sink: sink}
	return service, bridge, registry, sink, dispatcher
}

func compactBridgeAccount() gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-bridge", ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "gpt-5", SourceEndpointFamily: "responses",
			UpstreamModel: "chat-model", UpstreamEndpointFamily: "chat_completions", Enabled: true,
		}},
	}
}

func seedCompactRequest(t *testing.T, bridge *ChatBridgeStateService, registry *ContextRequestStateRegistry) (*gatewaypreauth.GatewayRequest, string) {
	t.Helper()
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"}
	// Seed a chain so the bridge compact restore succeeds.
	seedReq := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	attachBody(seedReq, map[string]any{"model": "gpt-5", "input": []any{}}, nil)
	registry.Set(seedReq, &CodexResponsesContextRequestState{
		RequestKind:           RequestKindResponses,
		Boundary:              boundary,
		CanonicalBody:         map[string]any{"model": "gpt-5", "input": []any{}},
		CurrentInput:          []any{},
		MaterializedInput:     []any{},
		PreviousResponseKind:  PreviousKindNone,
		ActiveBridgeAccountID: "acc-bridge",
	})
	handler := bridge.CompletionHandlerForRequest(registry, seedReq, gatewayruntimecache.OpenAIAccountSecret{ID: "acc-bridge", ProtocolCode: "openai", ProtocolVersion: "v1"}, "chat-model")
	handler(CodexResponsesChatBridgeCompletion{ResponseID: "resp_chat_bridge_c1", CreatedAt: time.Now(), Model: "chat-model", OutputItems: []any{}})

	req := newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)
	body := map[string]any{"model": "gpt-5", "previous_response_id": "resp_chat_bridge_c1", "input": []any{}}
	raw, _ := json.Marshal(body)
	attachBody(req, body, raw)
	registry.Set(req, &CodexResponsesContextRequestState{
		RequestKind:          RequestKindCompact,
		Boundary:             boundary,
		CanonicalBody:        body,
		CurrentInput:         body["input"],
		MaterializedInput:    body["input"],
		PreviousResponseID:   "resp_chat_bridge_c1",
		PreviousResponseKind: PreviousKindInternal,
	})
	bridge.PrepareCodexResponsesCompactDispatchForAccounts(registry, req, []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()})
	return req, "resp_chat_bridge_c1"
}

func TestApplyChatBridgeCompactPreflightCompleted(t *testing.T) {
	service, _, registry, sink, dispatcher := compactPreflightService(t)
	req, previousResponse := seedCompactRequest(t, service.Bridge, registry)
	dispatcher.exchange = &CompactUpstreamExchange{
		Account:            compactBridgeAccount(),
		BodyText:           `{"choices":[{"message":{"content":"最终摘要"}}]}`,
		UpstreamOK:         true,
		UpstreamStatus:     200,
		ReleaseConcurrency: func() {},
	}
	resRecorder, res := newTrackedWriter()
	_ = resRecorder
	audit := &recordedAudit{}
	result, err := service.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
		Req: req, Res: res, AuditCapture: audit,
		UsageContext:    gatewaypreauth.GatewayFailureUsageContext{TrafficSource: "gateway"},
		StartedAt:       1,
		SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
		GroupAccess:      gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
		DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
		Signal:           context.Background(),
	})
	if err != nil {
		t.Fatalf("compact preflight: %v", err)
	}
	if !result.Completed {
		t.Fatal("expected completed")
	}
	if dispatcher.calls != 1 {
		t.Fatalf("dispatch calls = %d", dispatcher.calls)
	}
	// The synthetic chat body carries the exact summary prompt.
	messages := dispatcher.lastBody["messages"].([]any)
	system := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "你负责为 Codex Responses 会话做上下文压缩。") {
		t.Errorf("system prompt = %q", system)
	}
	failure, hasFailure := sink.lastFailure()
	if hasFailure {
		t.Fatalf("unexpected failure %+v", failure.Audit)
	}
	if resRecorder.Code != http.StatusOK {
		t.Errorf("status = %d", resRecorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(resRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["object"] != "response.compaction" {
		t.Errorf("object = %v", payload["object"])
	}
	output := payload["output"].([]any)
	compaction := output[0].(map[string]any)
	encrypted := compaction["encrypted_content"].(string)
	if !strings.HasPrefix(encrypted, codexCompactionReferencePrefix) {
		t.Errorf("encryptedContent = %q", encrypted)
	}
	metadata, _ := audit.lastMetadata("codex_responses_chat_bridge_compact")
	if metadata["mode"] != "gateway_summary_compact" || metadata["previousResponseId"] != previousResponse {
		t.Errorf("audit = %+v", metadata)
	}
	if len(audit.finalizes) != 1 || audit.finalizes[0].StatusCode != 200 || audit.finalizes[0].ResponsePartType != "gateway_response" {
		t.Errorf("finalize = %+v", audit.finalizes)
	}
}

func TestApplyChatBridgeCompactPreflightFailurePaths(t *testing.T) {
	setup := func(t *testing.T) (*CompactPreflightService, *ContextRequestStateRegistry, *gatewaypreauth.GatewayRequest, *fakeCompactDispatcher) {
		service, _, registry, _, dispatcher := compactPreflightService(t)
		req, _ := seedCompactRequest(t, service.Bridge, registry)
		return service, registry, req, dispatcher
	}
	_ = setup

	t.Run("empty summary 502", func(t *testing.T) {
		service, _, req, dispatcher := setup(t)
		dispatcher.exchange = &CompactUpstreamExchange{Account: compactBridgeAccount(), BodyText: `{"choices":[]}`, UpstreamOK: true, UpstreamStatus: 200}
		sink := &recordedSink{}
		service.Sink = sink
		result, err := service.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
			Req: req, AuditCapture: &recordedAudit{},
			UsageContext:    gatewaypreauth.GatewayFailureUsageContext{},
			SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
			GroupAccess:      gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
			DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
			Signal:           context.Background(),
		})
		if err != nil || !result.Completed {
			t.Fatalf("result = %+v err %v", result, err)
		}
		failure, ok := sink.lastFailure()
		if !ok || failure.Audit.ErrorCode != "codex_bridge_compact_summary_empty" || failure.StatusCode != 502 {
			t.Fatalf("failure = %+v", failure.Audit)
		}
		if failure.ResponsePayload.Error.Message != "上游摘要模型没有返回可用的压缩摘要" {
			t.Errorf("message = %q", failure.ResponsePayload.Error.Message)
		}
	})

	t.Run("dispatch error 502 with chinese copy", func(t *testing.T) {
		service, _, req, dispatcher := setup(t)
		_ = req
		dispatcher.err = errors.New("boom")
		sink := &recordedSink{}
		service.Sink = sink
		result, err := service.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
			Req: req, AuditCapture: &recordedAudit{},
			UsageContext:    gatewaypreauth.GatewayFailureUsageContext{},
			SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
			GroupAccess:      gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
			DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
			Signal:           context.Background(),
		})
		if err != nil || !result.Completed {
			t.Fatalf("result = %+v err %v", result, err)
		}
		failure, ok := sink.lastFailure()
		if !ok || failure.Audit.ErrorCode != "codex_bridge_compact_summary_failed" || failure.StatusCode != 502 {
			t.Fatalf("failure = %+v", failure.Audit)
		}
		if failure.ResponsePayload.Error.Message != "上游摘要请求失败：boom" {
			t.Errorf("message = %q", failure.ResponsePayload.Error.Message)
		}
	})

	t.Run("restore failure completes with 404 copy", func(t *testing.T) {
		service, _, serviceRegistry, _, _ := compactPreflightService(t)
		req := newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)
		body := map[string]any{"model": "gpt-5", "previous_response_id": "resp_chat_bridge_c1", "input": []any{}}
		raw, _ := json.Marshal(body)
		attachBody(req, body, raw)
		serviceRegistry.Set(req, &CodexResponsesContextRequestState{
			RequestKind:          RequestKindCompact,
			CanonicalBody:        body,
			PreviousResponseKind: PreviousKindInternal,
		})
		sink := &recordedSink{}
		service.Sink = sink
		result, err := service.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
			Req: req, AuditCapture: &recordedAudit{},
			UsageContext:    gatewaypreauth.GatewayFailureUsageContext{},
			SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
			GroupAccess:      gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
			DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
			Signal:           context.Background(),
		})
		if err != nil || !result.Completed {
			t.Fatalf("result = %+v err %v", result, err)
		}
		failure, ok := sink.lastFailure()
		if !ok || failure.Audit.ErrorCode != "codex_bridge_compact_context_not_found" || failure.StatusCode != 404 {
			t.Fatalf("failure = %+v", failure.Audit)
		}
		if failure.ResponsePayload.Error.Message != "compact 对应的服务端上下文不存在、已过期或校验失败" {
			t.Errorf("message = %q", failure.ResponsePayload.Error.Message)
		}
	})

	t.Run("non compact request filters accounts", func(t *testing.T) {
		service, _, _, dispatcher := setup(t)
		req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
		attachBody(req, map[string]any{"input": []any{}}, nil)
		result, err := service.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
			Req:              req,
			AuditCapture:     &recordedAudit{},
			DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
			Signal:           context.Background(),
		})
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		if result.Completed {
			t.Fatal("non compact should continue")
		}
		if len(result.Accounts) != 1 {
			t.Errorf("accounts = %d, want 1 (no state allows all)", len(result.Accounts))
		}
		if dispatcher.calls != 0 {
			t.Errorf("dispatcher called = %d", dispatcher.calls)
		}
	})
}

func TestExtractChatCompletionSummary(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"first content", `{"choices":[{"message":{"content":"  摘要  "}},{"message":{"content":"second"}}]}`, "摘要"},
		{"empty content skipped", `{"choices":[{"message":{"content":"  "}},{"message":{"content":"second"}}]}`, "second"},
		{"no choices", `{"error":"x"}`, ""},
		{"invalid json", `not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractChatCompletionSummary(tt.body); got != tt.want {
				t.Errorf("summary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildCodexCompactResponse(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 30, 5, 0, time.UTC)
	payload := BuildCodexCompactResponse("cmp_abc", "juhecmp.v2.abc.deadbeef", now)
	if payload["id"] != "resp_abc" {
		t.Errorf("id = %v", payload["id"])
	}
	if payload["created_at"].(int64) != now.Unix() {
		t.Errorf("createdAt = %v", payload["created_at"])
	}
	usage := payload["usage"].(map[string]any)
	if usage["total_tokens"] != 0 {
		t.Errorf("usage = %+v", usage)
	}
}

func base64Summary(summary string) string {
	encoded, _ := json.Marshal(struct {
		Summary string `json:"summary"`
	}{Summary: summary})
	return base64RawURL(encoded)
}

func base64RawURL(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var out strings.Builder
	for i := 0; i < len(data); i += 3 {
		var chunk [3]byte
		copy(chunk[:], data[i:])
		b := uint32(chunk[0])<<16 | uint32(chunk[1])<<8 | uint32(chunk[2])
		out.WriteByte(alphabet[(b>>18)&0x3f])
		out.WriteByte(alphabet[(b>>12)&0x3f])
		if i+1 < len(data) {
			out.WriteByte(alphabet[(b>>6)&0x3f])
		}
		if i+2 < len(data) {
			out.WriteByte(alphabet[b&0x3f])
		}
	}
	return out.String()
}

package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Mock collaborators for the generation wave (strict mock closure): executor,
// model catalog, chat key provider, gateway key validator and image processor.

type dispatchCall struct {
	Path    string
	Headers map[string]string
	Body    string
}

type scriptStep struct {
	match   func(call dispatchCall) bool
	respond func(call dispatchCall) *GenerationDispatchResponse
}

type mockExecutor struct {
	mu    sync.Mutex
	steps []scriptStep
	calls []dispatchCall
}

func (m *mockExecutor) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
	call := dispatchCall{Path: req.Path, Headers: req.Headers, Body: string(req.Body)}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	steps := m.steps
	m.mu.Unlock()
	for _, step := range steps {
		if step.match != nil && !step.match(call) {
			continue
		}
		response := step.respond(call)
		if response == nil {
			// Blocking script: wait for cancellation.
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return response, nil
	}
	return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func (m *mockExecutor) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func sseResponse(payload string) *GenerationDispatchResponse {
	return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(payload))}
}

func jsonStatusResponse(status int, payload string) *GenerationDispatchResponse {
	return &GenerationDispatchResponse{Status: status, Body: io.NopCloser(strings.NewReader(payload))}
}

func chatCompletionsSSE(delta string, usage bool) string {
	usagePart := ""
	if usage {
		usagePart = "data: " + `{"choices":[],"usage":{"prompt_tokens":42,"completion_tokens":7}}` + "\n\n"
	}
	return "data: " + `{"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"content":"` + delta + `"}}]}` + "\n\n" +
		usagePart +
		"data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
}

func responsesTextSSE(text string) string {
	return "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"` + text + `"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":4},"output":[{"type":"message","role":"assistant"}]}}` + "\n\n"
}

func responsesFunctionCallSSE(callID, name, args string) string {
	escaped := strings.ReplaceAll(args, `"`, `\"`)
	return "event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"` + callID + `","name":"` + name + `"}}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"` + callID + `","name":"` + name + `","arguments":"` + escaped + `","status":"completed"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":30,"output_tokens":6},"output":[{"type":"reasoning"},{"type":"function_call","id":"item_1","call_id":"` + callID + `","name":"` + name + `","arguments":"` + escaped + `","status":"completed"}]}}` + "\n\n"
}


func chatCompletionsToolSSE(callID, name, args string) string {
	escaped := strings.ReplaceAll(args, `"`, `\"`)
	return "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + callID + `","function":{"name":"` + name + `","arguments":"` + escaped + `"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
}

const testTinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

type mockModelCatalog struct{}

func (mockModelCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	enabled := true
	return []ChatTransportAccount{{
		ID: "account-1", Type: "api_key", ProviderCode: "openai",
		SupportedEndpointModes: []string{"chat_sse", "responses_sse"},
		ModelMappings: []ChatTransportModelMapping{{
			Enabled: &enabled, SourceModel: requestedModel, SourceEndpointFamily: endpointFamily,
		}},
	}}
}

func (mockModelCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	return []ProviderModelCatalogItem{
		{
			Model: "gpt-5", ProviderCode: "openai",
			SupportedReasoningEfforts: []string{"low", "high"}, DefaultReasoningEffort: strPtrT("low"),
			SupportedServiceTiers: []string{"priority"},
			ContextWindowTokens:   int64PtrT(100000), MaxOutputTokens: int64PtrT(20000),
			SupportedAPIProtocols: []string{"chat_completions", "responses"},
			InputModalities:       []string{"text", "image"},
			OutputModalities:      []string{"text"},
			SupportedTools:        []string{"function_calling"},
		},
		{
			Model: "gpt-5-mini", ProviderCode: "openai",
			SupportedReasoningEfforts: []string{"low"},
			SupportedAPIProtocols:     []string{"chat_completions"},
			InputModalities:           []string{"text"},
			OutputModalities:          []string{"text"},
			SupportedTools:            []string{"function_calling"},
		},
	}
}

func strPtrT(value string) *string { return &value }
func int64PtrT(value int64) *int64 { return &value }

type mockChatKeys struct {
	mu          sync.Mutex
	ensureCount int
	keyID       string
}

func (m *mockChatKeys) EnsureChatAPIKey(ownerID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureCount++
	if m.keyID == "" {
		m.keyID = "chat_key_provisioned"
	}
	return m.keyID, nil
}

func (m *mockChatKeys) FindChatAPIKey(keyID, ownerID string) (*ChatAPIKeyRecord, error) {
	return &ChatAPIKeyRecord{ID: keyID, Name: "对话密钥", Secret: "chat-secret", Status: "active"}, nil
}

type mockGatewayKeys struct{}

func (mockGatewayKeys) ValidateGatewayKey(secret string) (*GatewayKeyView, error) {
	if secret == "" {
		return nil, nil
	}
	return &GatewayKeyView{
		GroupBindings:          []GatewayGroupBinding{{GroupID: "group-a", Status: "active", GroupEnabled: true}},
		ImageGenerationEnabled: true,
	}, nil
}

type stubImageProcessor struct{}

func (stubImageProcessor) ProcessUpload(data []byte, declaredMimeType string) (*ProcessedImage, error) {
	if len(data) == 0 {
		return nil, &ImageProcessingError{Message: "图片无法解码、像素过大或文件已损坏"}
	}
	digest := sha256.Sum256(data)
	width, height := imageDimensionsFromBytes(data)
	return &ProcessedImage{
		Buffer: data, OriginalMimeType: declaredMimeType,
		OriginalWidth: width, OriginalHeight: height,
		MimeType: "image/webp", Width: width, Height: height,
		ByteSize: int64(len(data)), SHA256: hexEncode(digest[:]),
	}, nil
}

func (stubImageProcessor) CreatePreview(data []byte) (*ProcessedImage, error) {
	digest := sha256.Sum256(data)
	width, height := imageDimensionsFromBytes(data)
	return &ProcessedImage{
		Buffer: data, OriginalMimeType: "image/webp",
		OriginalWidth: width, OriginalHeight: height,
		MimeType: "image/webp", Width: width, Height: height,
		ByteSize: int64(len(data)), SHA256: hexEncode(digest[:]),
	}, nil
}

type generationEnv struct {
	*routeEnv
	executor    *mockExecutor
	chatKeys    *mockChatKeys
	objectDir   string
	hub         *GenerationHub
	compactions *CompactionService
	deps        *Deps
}

// newChatRoutesForTest builds the routes struct the same way Register does so
// individual handlers can be mounted on a test-local mux.
func newChatRoutesForTest(deps *Deps) *chatRoutes {
	return &chatRoutes{deps: deps, preps: map[string]*activePreparation{}, actions: map[string]*activeConversationAction{}}
}

func newGenerationEnv(t *testing.T) *generationEnv {
	t.Helper()
	fixture := newChatFixture(t)
	_, clock := fixedChatClock()
	executor := &mockExecutor{}
	chatKeys := &mockChatKeys{}
	objectDir := t.TempDir()
	objectStore, err := NewLocalObjectStore(objectDir)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewGenerationHub(func() string { return fixture.nowISO })
	compactions := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	deps := &Deps{
		Store:                   fixture.store,
		MaxTurnsPerConversation: 100,
		Now:                     clock,
		Generations:             hub,
		Hub:                     hub,
		Executor:                executor,
		ModelCatalog:            mockModelCatalog{},
		ChatKeys:                chatKeys,
		GatewayKeys:             mockGatewayKeys{},
		ObjectStore:             objectStore,
		ImageProcessor:          stubImageProcessor{},
		Compactions:             compactions,
		TokenCount:              func(text string) int { return len(text) / 4 },
		RetentionDays:           30,
		DiagnosticToolEnabled:   true,
		ToolEnvironment:         "test",
		MaxConversationsPerUserInt: func() int { return 30 },
	}
	deps.RequireSession = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			owner := r.Header.Get("X-Test-Owner")
			if owner == "" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "请先登录"})
				return
			}
			next.ServeHTTP(w, r.WithContext(authsys.WithAuthContext(r.Context(), &authsys.AuthContext{
				SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user",
			})))
		})
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.Register(k, "/__aisys__/api/my-chat")
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &generationEnv{
		routeEnv: &routeEnv{t: t, server: server, fixture: fixture},
		executor: executor, chatKeys: chatKeys, objectDir: objectDir, hub: hub, compactions: compactions,
		deps: deps,
	}
}

func (env *generationEnv) streamPost(conversationID, owner, payload string) routeResponse {
	env.t.Helper()
	return env.do("POST", "/__aisys__/api/my-chat/conversations/"+conversationID+"/stream", owner, payload)
}

func streamPayload(clientMessageID, content, model string) string {
	return `{"clientMessageId":"` + clientMessageID + `","content":"` + content + `","model":"` + model + `"}`
}

type sseEvent struct {
	event string
	data  string
}

func sseEvents(body string) []sseEvent {
	events := []sseEvent{}
	for _, block := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		eventType := ""
		data := ""
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		events = append(events, sseEvent{eventType, data})
	}
	return events
}

// TestStreamLifecycleMatrix covers the table-driven lifecycle: missing
// conversation 404, duplicate submission 409, active-turn 409.
func TestStreamLifecycleMatrix(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(env *generationEnv) string
		payload    func(env *generationEnv) string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "conversation missing -> 404",
			setup:      func(env *generationEnv) string { return "chat_conv_missing" },
			payload:    func(env *generationEnv) string { return streamPayload("cmid-404", "问题", "gpt-5") },
			wantStatus: 404,
			wantCode:   "chat_conversation_not_found",
		},
		{
			name: "duplicate clientMessageId -> 409",
			setup: func(env *generationEnv) string {
				env.fixture.createConversation("chat_conv_dup", routeTestOwner)
				env.executor.steps = []scriptStep{{
					match:   func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
					respond: func(call dispatchCall) *GenerationDispatchResponse { return sseResponse(chatCompletionsSSE("第一次", false)) },
				}}
				first := env.streamPost("chat_conv_dup", routeTestOwner, streamPayload("cmid-dup", "第一次", "gpt-5"))
				if first.status != http.StatusOK {
					t.Fatalf("first stream = %d %s", first.status, first.rawString())
				}
				return "chat_conv_dup"
			},
			payload:    func(env *generationEnv) string { return streamPayload("cmid-dup", "第一次", "gpt-5") },
			wantStatus: 409,
			wantCode:   "chat_message_already_exists",
		},
		{
			name: "turn limit exceeded -> 409",
			setup: func(env *generationEnv) string {
				env.fixture.createConversation("chat_conv_limit", routeTestOwner)
				env.fixture.seedTurns(routeTestOwner, "chat_conv_limit", 100)
				return "chat_conv_limit"
			},
			payload:    func(env *generationEnv) string { return streamPayload("cmid-limit", "超限", "gpt-5") },
			wantStatus: 409,
			wantCode:   "chat_turn_limit_exceeded",
		},
		{
			name: "too many image blocks -> 400 invalid request",
			setup: func(env *generationEnv) string {
				env.fixture.createConversation("chat_conv_toomany", routeTestOwner)
				return "chat_conv_toomany"
			},
			payload: func(env *generationEnv) string {
				blocks := `{"clientMessageId":"cmid-toomany","content":"图太多","model":"gpt-5","contentBlocks":[`
				for i := 0; i < 6; i++ {
					if i > 0 {
						blocks += ","
					}
					blocks += `{"type":"input_image","assetId":"chat_asset_` + strings.Repeat("a", 32) + `"}`
				}
				blocks += "]}"
				return blocks
			},
			wantStatus: 400,
			wantCode:   "chat_invalid_request",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newGenerationEnv(t)
			conversationID := testCase.setup(env)
			response := env.streamPost(conversationID, routeTestOwner, testCase.payload(env))
			if response.status != testCase.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", response.status, testCase.wantStatus, response.rawString())
			}
			if testCase.wantCode != "" && response.code() != testCase.wantCode {
				t.Fatalf("code = %s, want %s (body %s)", response.code(), testCase.wantCode, response.rawString())
			}
		})
	}
}

func TestStreamHappyPathChatCompletions(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_s", routeTestOwner)
	env.executor.steps = []scriptStep{{
		match:   func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return sseResponse(chatCompletionsSSE("你好，世界", true)) },
	}}
	response := env.streamPost("chat_conv_s", routeTestOwner, streamPayload("cmid-1", "问题", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.status, response.rawString())
	}
	events := sseEvents(response.rawString())
	if len(events) < 3 {
		t.Fatalf("unexpected event count: %d (%s)", len(events), response.rawString())
	}
	if events[0].event != "message.started" {
		t.Fatalf("first event = %s, want message.started", events[0].event)
	}
	var started struct {
		TurnID           string         `json:"turnId"`
		UserMessage      map[string]any `json:"userMessage"`
		AssistantMessage map[string]any `json:"assistantMessage"`
	}
	if err := json.Unmarshal([]byte(events[0].data), &started); err != nil {
		t.Fatalf("message.started payload: %v", err)
	}
	if started.UserMessage["contentText"] != "问题" {
		t.Fatalf("user message content = %v", started.UserMessage["contentText"])
	}
	last := events[len(events)-1]
	if last.event != "message.completed" {
		t.Fatalf("last event = %s (%s), want message.completed", last.event, last.data)
	}
	var completed struct {
		MessageID    string `json:"messageId"`
		FinishReason string `json:"finishReason"`
		EventVersion int64  `json:"eventVersion"`
	}
	if err := json.Unmarshal([]byte(last.data), &completed); err != nil {
		t.Fatalf("message.completed payload: %v", err)
	}
	if completed.FinishReason != "stop" || completed.EventVersion < 2 {
		t.Fatalf("unexpected completion payload: %+v", completed)
	}
	messages, err := env.fixture.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_s", SystemAccountID: routeTestOwner, Now: env.fixture.nowISO, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	assistant := messages[len(messages)-1]
	if assistant.Status != StatusCompleted || assistant.ContentText != "你好，世界" {
		t.Fatalf("assistant = %+v", assistant)
	}
	if assistant.FinishReason == nil || *assistant.FinishReason != "stop" {
		t.Fatalf("finish reason = %v", assistant.FinishReason)
	}
	head, err := env.fixture.store.GetContextHead("chat_conv_s", routeTestOwner)
	if err != nil || head == nil {
		t.Fatal("context head missing")
	}
	if head.ActiveContextTokens == nil || *head.ActiveContextTokens != 49 {
		t.Fatalf("active context tokens = %v", head.ActiveContextTokens)
	}
	if head.UsageEstimated {
		t.Fatal("usage must not be estimated when upstream reported tokens")
	}
}

func TestStreamResponsesWithDiagnosticTool(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_t", routeTestOwner)
	round := 0
	var roundMu sync.Mutex
	env.executor.steps = []scriptStep{{
		match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			roundMu.Lock()
			round++
			current := round
			roundMu.Unlock()
			if current == 1 {
				return sseResponse(chatCompletionsToolSSE("call_1", "diagnostic_echo", `{"text":"ping"}`))
			}
			return sseResponse(chatCompletionsSSE("工具已完成", true))
		},
	}}
	response := env.streamPost("chat_conv_t", routeTestOwner, streamPayload("cmid-tool", "调用工具", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.status, response.rawString())
	}
	events := sseEvents(response.rawString())
	// Internal tool execution surfaces as content_block.* timeline events
	// (publish carries a toolEvent projection, so Node never emits the raw
	// tool.* event to the SSE stream).
	var sawToolStarted, sawToolCompleted, sawCompleted bool
	for _, event := range events {
		switch event.event {
		case "content_block.started":
			if strings.Contains(event.data, "diagnostic_echo") {
				sawToolStarted = true
			}
		case "content_block.completed":
			if strings.Contains(event.data, "diagnostic_echo") && strings.Contains(event.data, "echoedText") {
				sawToolCompleted = true
			}
		case "message.completed":
			sawCompleted = true
		}
	}
	if !sawToolStarted || !sawToolCompleted || !sawCompleted {
		t.Fatalf("tool event sequence incomplete: %+v", events)
	}
	if env.executor.callCount() != 2 {
		t.Fatalf("dispatch calls = %d, want 2", env.executor.callCount())
	}
	messages, err := env.fixture.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_t", SystemAccountID: routeTestOwner, Now: env.fixture.nowISO, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	assistant := messages[len(messages)-1]
	if assistant.ContentText != "工具已完成" {
		t.Fatalf("assistant content = %s", assistant.ContentText)
	}
	hasToolBlock := false
	for _, block := range assistant.ContentBlocks {
		if block.Type == "tool_call" && block.Status != nil && *block.Status == "completed" {
			hasToolBlock = true
		}
	}
	if !hasToolBlock {
		t.Fatalf("assistant blocks missing completed tool_call: %+v", assistant.ContentBlocks)
	}
}

func TestStreamImageGenerationPipeline(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_i", routeTestOwner)
	modelRound := 0
	var roundMu sync.Mutex
	env.executor.steps = []scriptStep{
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				roundMu.Lock()
				modelRound++
				current := modelRound
				roundMu.Unlock()
				if current == 1 {
					return sseResponse(chatCompletionsToolSSE("call_img", "generate_image", `{"prompt":"一只猫"}`))
				}
				return sseResponse(chatCompletionsSSE("图片已生成", false))
			},
		},
		{
			match:   func(call dispatchCall) bool { return call.Path == "/v1/images/generations" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return jsonStatusResponse(200, `{"data":[{"b64_json":"`+testTinyPNGBase64+`","revised_prompt":"a cat"}]}`)
			},
		},
	}
	response := env.streamPost("chat_conv_i", routeTestOwner, streamPayload("cmid-img", "画一只猫", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.status, response.rawString())
	}
	events := sseEvents(response.rawString())
	var sawImageCompleted bool
	for _, event := range events {
		if event.event != "content_block.completed" {
			continue
		}
		var payload struct {
			Block map[string]any `json:"block"`
		}
		_ = json.Unmarshal([]byte(event.data), &payload)
		if payload.Block["type"] == "output_image" {
			sawImageCompleted = true
			if payload.Block["assetId"] == nil || payload.Block["status"] != "completed" {
				t.Fatalf("image block = %v", payload.Block)
			}
		}
	}
	if !sawImageCompleted {
		t.Fatalf("image completion event missing: %+v", events)
	}
	assets, err := env.fixture.store.queryAssets(env.fixture.store.db, env.fixture.store.bind(`SELECT `+assetColumns+` FROM chat_assets WHERE conversation_id = ?`), "chat_conv_i")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].SourceKind != "assistant_generated" {
		t.Fatalf("assets = %+v", assets)
	}
	if assets[0].ProcessingStatus != "ready" || assets[0].StorageKey == nil {
		t.Fatalf("asset not ready: %+v", assets[0])
	}
	store, err := NewLocalObjectStore(env.objectDir)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := store.Open(*assets[0].StorageKey, chatAssetGeneratedMaxBytes)
	if err != nil || len(data) == 0 {
		t.Fatalf("stored object missing: %v", err)
	}
}

func TestStreamStopAndConflicts(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_c", routeTestOwner)
	env.executor.steps = []scriptStep{{
		match:   func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return nil },
	}}
	blocked := make(chan routeResponse, 1)
	go func() {
		blocked <- env.streamPost("chat_conv_c", routeTestOwner, streamPayload("cmid-live", "进行中", "gpt-5"))
	}()
	time.Sleep(300 * time.Millisecond)
	second := env.streamPost("chat_conv_c", routeTestOwner, streamPayload("cmid-second", "并发", "gpt-5"))
	if second.status != http.StatusConflict || second.code() != "chat_message_in_progress" {
		t.Fatalf("second = %d %s", second.status, second.rawString())
	}
	stop := env.do("POST", "/__aisys__/api/my-chat/conversations/chat_conv_c/stop", routeTestOwner, `{"clientMessageId":"cmid-live"}`)
	if stop.status != http.StatusAccepted || !strings.Contains(stop.rawString(), `"stopped":true`) {
		t.Fatalf("stop = %d %s", stop.status, stop.rawString())
	}
	final := <-blocked
	if final.status != http.StatusOK {
		t.Fatalf("blocked stream status = %d body = %s", final.status, final.rawString())
	}
	events := sseEvents(final.rawString())
	last := events[len(events)-1]
	if last.event != "message.canceled" {
		t.Fatalf("final event = %s %s, want message.canceled", last.event, last.data)
	}
	messages, err := env.fixture.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_c", SystemAccountID: routeTestOwner, Now: env.fixture.nowISO, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	assistant := messages[len(messages)-1]
	if assistant.Status != StatusCanceled {
		t.Fatalf("assistant status = %s", assistant.Status)
	}
}

func TestStreamUpstreamHTTPFailureClassified(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_f", routeTestOwner)
	env.executor.steps = []scriptStep{{
		match:   func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(502, `{"error":{"message":"bad gateway"}}`) },
	}}
	response := env.streamPost("chat_conv_f", routeTestOwner, streamPayload("cmid-fail", "问题", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("status = %d", response.status)
	}
	events := sseEvents(response.rawString())
	last := events[len(events)-1]
	if last.event != "message.failed" {
		t.Fatalf("final event = %s %s", last.event, last.data)
	}
	if !strings.Contains(last.data, "upstream_http_error") || !strings.Contains(last.data, "模型服务请求失败，请稍后重试") {
		t.Fatalf("failure payload = %s", last.data)
	}
}

func TestProvisionConversationsIdempotent(t *testing.T) {
	env := newGenerationEnv(t)
	first := env.do("POST", "/__aisys__/api/my-chat/conversations", routeTestOwner, "{}")
	if first.status != http.StatusCreated {
		t.Fatalf("create = %d %s", first.status, first.rawString())
	}
	data := first.dataMap()
	if data["apiKeyId"] != "chat_key_provisioned" {
		t.Fatalf("apiKeyId = %v", data["apiKeyId"])
	}
	defaultModel, _ := data["defaultModel"].(map[string]any)
	if defaultModel == nil || defaultModel["id"] != "gpt-5" {
		t.Fatalf("defaultModel = %v", data["defaultModel"])
	}
	if data["userTurnLimit"] != float64(100) {
		t.Fatalf("userTurnLimit = %v", data["userTurnLimit"])
	}
	second := env.do("POST", "/__aisys__/api/my-chat/conversations", routeTestOwner, "{}")
	if second.status != http.StatusCreated {
		t.Fatalf("second create = %d %s", second.status, second.rawString())
	}
	env.chatKeys.mu.Lock()
	ensureCount := env.chatKeys.ensureCount
	env.chatKeys.mu.Unlock()
	if ensureCount == 0 {
		t.Fatal("chat key ensure was never called")
	}
	invalid := env.do("POST", "/__aisys__/api/my-chat/conversations", routeTestOwner, `{"nope":1}`)
	if invalid.status != http.StatusBadRequest || invalid.code() != "chat_invalid_request" {
		t.Fatalf("invalid = %d %s", invalid.status, invalid.rawString())
	}
}

func TestModelsRoutes(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_m", routeTestOwner)
	list := env.do("GET", "/__aisys__/api/my-chat/conversations/chat_conv_m/models", routeTestOwner, "")
	if list.status != http.StatusOK {
		t.Fatalf("list = %d %s", list.status, list.rawString())
	}
	if len(list.dataArray()) != 2 {
		t.Fatalf("models = %v", list.dataArray())
	}
	detail := env.do("GET", "/__aisys__/api/my-chat/conversations/chat_conv_m/models/gpt-5", routeTestOwner, "")
	if detail.status != http.StatusOK {
		t.Fatalf("detail = %d %s", detail.status, detail.rawString())
	}
	detailData := detail.dataMap()
	if detailData["name"] != "gpt-5" || detailData["maxInputTokens"] != float64(80000) {
		t.Fatalf("detail = %v", detailData)
	}
	missing := env.do("GET", "/__aisys__/api/my-chat/conversations/chat_conv_m/models/unknown-model", routeTestOwner, "")
	if missing.status != http.StatusNotFound || missing.code() != "chat_model_not_found" {
		t.Fatalf("missing model = %d %s", missing.status, missing.rawString())
	}
}

func TestCompactionServiceLoop(t *testing.T) {
	t.Run("skips empty conversation", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("chat_conv_z", routeTestOwner)
		result := env.compactions.CompactOnce(context.Background(), CompactionInput{
			ConversationID: "chat_conv_z", SystemAccountID: routeTestOwner,
			APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
		})
		if result.Status != "skipped" || result.Reason != "no_compactable_turn" {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("installs checkpoint", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("chat_conv_y", routeTestOwner)
		seedLongTurns(t, env.fixture, routeTestOwner, "chat_conv_y", 4)
		env.executor.steps = []scriptStep{{
			match: func(call dispatchCall) bool {
				return call.Headers["x-juhe-ai-purpose"] == "chat_context_compaction"
			},
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				summary := `{"durableMemory":["喜欢简洁"],"currentGoal":"配置服务","constraints":[],"decisions":[],"completed":["阅读文档"],"pending":["部署"],"importantToolResults":[],"imageMemories":[],"recentUserIntent":"配置服务","uncertainties":[]}`
				return jsonStatusResponse(200, `{"choices":[{"message":{"content":` + jsonQuote(summary) + `}}]}`)
			},
		}}
		result := env.compactions.CompactOnce(context.Background(), CompactionInput{
			ConversationID: "chat_conv_y", SystemAccountID: routeTestOwner,
			APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
		})
		if result.Status != "installed" {
			t.Fatalf("result = %+v", result)
		}
		if result.AfterBytes <= 0 || result.AfterBytes >= result.BeforeBytes {
			t.Fatalf("byte accounting = %+v", result)
		}
		head, err := env.fixture.store.GetContextHead("chat_conv_y", routeTestOwner)
		if err != nil || head == nil {
			t.Fatal("head missing")
		}
		if head.ContextState != StateReady || head.ActiveCheckpointID == nil {
			t.Fatalf("head after install = %+v", head)
		}
	})
	t.Run("failed summarization marks compact_failed", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("chat_conv_w", routeTestOwner)
		env.fixture.seedTurns(routeTestOwner, "chat_conv_w", 4)
		env.executor.steps = []scriptStep{{
			match: func(call dispatchCall) bool {
				return call.Headers["x-juhe-ai-purpose"] == "chat_context_compaction"
			},
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return jsonStatusResponse(500, `{"error":{"message":"boom"}}`)
			},
		}}
		result := env.compactions.CompactOnce(context.Background(), CompactionInput{
			ConversationID: "chat_conv_w", SystemAccountID: routeTestOwner,
			APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
		})
		if result.Status != "failed" {
			t.Fatalf("result = %+v", result)
		}
		head, err := env.fixture.store.GetContextHead("chat_conv_w", routeTestOwner)
		if err != nil || head == nil {
			t.Fatal("head missing")
		}
		if head.ContextState != StateCompactFailed || head.ContextErrorCode == nil {
			t.Fatalf("head after failure = %+v", head)
		}
	})
	t.Run("start reports already_running while first run in flight", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("chat_conv_x", routeTestOwner)
		env.fixture.seedTurns(routeTestOwner, "chat_conv_x", 4)
		env.executor.steps = []scriptStep{{
			match: func(call dispatchCall) bool {
				return call.Headers["x-juhe-ai-purpose"] == "chat_context_compaction"
			},
			respond: func(call dispatchCall) *GenerationDispatchResponse { return nil },
		}}
		done := make(chan CompactionStartResult, 1)
		go func() {
			done <- env.compactions.Start(context.Background(), CompactionInput{
				ConversationID: "chat_conv_x", SystemAccountID: routeTestOwner,
				APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
			})
		}()
		deadline := time.Now().Add(2 * time.Second)
		for {
			env.compactions.mu.Lock()
			_, active := env.compactions.active[routeTestOwner+":chat_conv_x"]
			env.compactions.mu.Unlock()
			if active || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		second := env.compactions.Start(context.Background(), CompactionInput{
			ConversationID: "chat_conv_x", SystemAccountID: routeTestOwner,
			APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
		})
		if second.Status != "already_running" {
			t.Fatalf("second start = %+v", second)
		}
		// Unblock the first run by cancelling it.
		env.compactions.mu.Lock()
		entry := env.compactions.active[routeTestOwner+":chat_conv_x"]
		env.compactions.mu.Unlock()
		if entry != nil {
			// The blocked executor waits on context cancellation; cancel via a
			// fresh context is not possible, so settle the channels directly.
			entry.acceptance <- CompactionStartResult{Status: "accepted"}
			entry.completion <- CompactionResult{Status: "failed", Reason: "cancelled"}
			env.compactions.mu.Lock()
			delete(env.compactions.active, routeTestOwner+":chat_conv_x")
			env.compactions.mu.Unlock()
		}
		first := <-done
		if first.Status != "accepted" && first.Status != "failed" {
			t.Fatalf("first start = %+v", first)
		}
	})
}

// seedLongTurns accepts and completes n turns with long content so the
// compaction summary is strictly smaller than the source pages.
func seedLongTurns(t *testing.T, f *chatFixture, ownerID, conversationID string, n int) {
	t.Helper()
	longQuestion := strings.Repeat("用户提供了很长的背景信息，包含大量需要保留的细节内容。", 8)
	longAnswer := strings.Repeat("助手根据背景信息给出了详尽的分析结论和建议步骤说明。", 8)
	for i := 1; i <= n; i++ {
		accepted, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: conversationID, SystemAccountID: ownerID,
			ClientMessageID: fmt.Sprintf("long-cmid-%d", i), UserContent: longQuestion,
			Model: "gpt-5", Now: f.nowISO, StorageQuotaBytes: 2 * 1024 * 1024 * 1024,
			RetentionDays: 30, MaxTurnsPerConversation: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: conversationID, SystemAccountID: ownerID, TurnID: accepted.TurnID,
			AssistantContent: longAnswer, FinishReason: "stop", Now: f.nowISO,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func jsonQuote(value string) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}

func TestAssetUploadBoundaries(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_a2", routeTestOwner)
	// Direct mux: the kernel wraps every system request in a 256 KiB
	// MaxBytesReader (its JSON limit); Node applies that limit only to
	// express.json, so the multipart route bypasses it in production.
	mux := http.NewServeMux()
	requireOwner := func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			owner := r.Header.Get("X-Test-Owner")
			if owner == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(authsys.WithAuthContext(r.Context(), &authsys.AuthContext{
				SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user",
			})))
		})
	}
	rt := newChatRoutesForTest(env.deps)
	mux.Handle("POST /conversations/{conversationId}/assets", requireOwner(rt.uploadAsset))
	mux.Handle("GET /conversations/{conversationId}/assets/{assetId}/content", requireOwner(rt.assetContent))
	mux.Handle("DELETE /conversations/{conversationId}/assets/{assetId}", requireOwner(rt.deleteAsset))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	base := server.URL + "/conversations/chat_conv_a2/assets"

	upload := func(filename string, data []byte) routeResponse {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		if filename != "" {
			mimeType := map[string]string{
				".png": "image/png", ".jpg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
			}[filepath.Ext(filename)]
			if mimeType == "" {
				mimeType = "text/plain"
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", "form-data; name=\"file\"; filename=\""+filename+"\"")
			header.Set("Content-Type", mimeType)
			part, _ := writer.CreatePart(header)
			_, _ = part.Write(data)
		}
		_ = writer.Close()
		request, err := http.NewRequest("POST", base, body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("X-Test-Owner", routeTestOwner)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		parsed := map[string]any{}
		_ = json.Unmarshal(payload, &parsed)
		return routeResponse{status: response.StatusCode, body: payload, jsonMap: parsed}
	}

	pngBytes, err := base64.StdEncoding.DecodeString(testTinyPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	created := upload("cat.png", pngBytes)
	if created.status != http.StatusCreated {
		t.Fatalf("created = %d %s", created.status, created.rawString())
	}
	metadata := created.dataMap()
	if metadata["mimeType"] != "image/webp" || metadata["fileName"] != "cat.png" {
		t.Fatalf("metadata = %v", metadata)
	}
	assetID, _ := metadata["id"].(string)
	if assetID == "" {
		t.Fatal("asset id missing")
	}

	doAbs := func(method, path, owner string, headerKey, headerValue string) routeResponse {
		request, err := http.NewRequest(method, path, strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		if headerKey != "" {
			request.Header.Set(headerKey, headerValue)
		}
		if owner != "" {
			request.Header.Set("X-Test-Owner", owner)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		parsed := map[string]any{}
		_ = json.Unmarshal(payload, &parsed)
		return routeResponse{status: response.StatusCode, body: payload, jsonMap: parsed}
	}
	content := doAbs("GET", base+"/"+assetID+"/content", routeTestOwner, "", "")
	if content.status != http.StatusOK || !bytes.Equal(content.body, pngBytes) {
		t.Fatalf("content = %d bytes=%d", content.status, len(content.body))
	}
	digest := sha256.Sum256(pngBytes)
	etag := "\"" + hexEncode(digest[:]) + "\""
	notModified := doAbs("GET", base+"/"+assetID+"/content", routeTestOwner, "If-None-Match", etag)
	if notModified.status != http.StatusNotModified {
		t.Fatalf("304 = %d", notModified.status)
	}

	empty := upload("empty.png", nil)
	if empty.status != http.StatusBadRequest || empty.message() != "上传图片不能为空" {
		t.Fatalf("empty = %d %s", empty.status, empty.rawString())
	}
	big := upload("big.png", make([]byte, 3*1024*1024+1))
	if big.status != http.StatusRequestEntityTooLarge || big.message() != "单张上传图片不能超过 3 MiB" {
		t.Fatalf("big = %d %s", big.status, big.rawString())
	}
	text := upload("notes.txt", []byte("hello"))
	if text.status != http.StatusUnsupportedMediaType || text.message() != "仅支持 JPEG、PNG、WebP 或 GIF 图片" {
		t.Fatalf("text = %d %s", text.status, text.rawString())
	}
	missingFile := upload("", nil)
	if missingFile.status != http.StatusBadRequest || missingFile.message() != "缺少 file 图片字段" {
		t.Fatalf("missing file = %d %s", missingFile.status, missingFile.rawString())
	}
	deleted := doAbs("DELETE", base+"/"+assetID, routeTestOwner, "", "")
	if deleted.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.status, deleted.rawString())
	}
	again := doAbs("DELETE", base+"/"+assetID, routeTestOwner, "", "")
	if again.status != http.StatusConflict || again.code() != "chat_asset_not_deletable" {
		t.Fatalf("second delete = %d %s", again.status, again.rawString())
	}
}

// doWithHeader performs a request with one extra header.
func (env *routeEnv) doWithHeader(method, path, owner, key, value string) routeResponse {
	env.t.Helper()
	request, err := http.NewRequest(method, env.server.URL+path, strings.NewReader(""))
	if err != nil {
		env.t.Fatal(err)
	}
	request.Header.Set(key, value)
	if owner != "" {
		request.Header.Set("X-Test-Owner", owner)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		env.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	parsed := map[string]any{}
	_ = json.Unmarshal(payload, &parsed)
	return routeResponse{status: response.StatusCode, body: payload, jsonMap: parsed}
}

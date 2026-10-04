package chat

// M7 问答音视频工具（docs/functions/问答音视频工具设计.md，2026-10-04）的
// 覆盖：工具注册/编排器阶段、媒体传输面（/v1/audio/speech、/v1/videos、任务
// 轮询与产物下载的请求形态 + 限额 + MIME 嗅探）、候选收敛、tool-preferences
// 与会话 PATCH 契约、media-tasks 结算幂等（并发两请求一资产）与消息块定点
// 补丁。Mock 沿 generation_test.go 的 mockExecutor scriptStep 先例。

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- 媒体字节夹具（真实魔数，与嗅探词表一一对应） ---

var (
	mediaWAVFixture = append([]byte("RIFF"), append(make([]byte, 4, 36), []byte("WAVEfmt ")...)...)
	mediaMP3Fixture = append([]byte("ID3"), []byte("\x03\x00\x00\x00\x00\x00\x00\x00")...)
	mediaOGGFixture = append([]byte("OggS"), []byte("\x00\x02\x00\x00\x00\x00\x00\x00")...)
	mediaM4AFixture = append([]byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p'}, []byte("M4A ")...)
	mediaMP4Fixture = append([]byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p'}, []byte("isom")...)
	mediaWebMFixture = append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("\x01\x00\x00\x00")...)
)

func TestChatMediaMimeTypeFromBytes(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"wav", mediaWAVFixture, "audio/wav"},
		{"mp3-id3", mediaMP3Fixture, "audio/mpeg"},
		{"mp3-frame-sync", []byte{0xFF, 0xFB, 0x90, 0x00}, "audio/mpeg"},
		{"ogg", mediaOGGFixture, "audio/ogg"},
		{"m4a", mediaM4AFixture, "audio/mp4"},
		{"mp4", mediaMP4Fixture, "video/mp4"},
		{"webm", mediaWebMFixture, "video/webm"},
		{"json-error-body", []byte(`{"error":{"message":"boom"}}`), ""},
		{"png-is-not-media", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, ""},
		{"empty", nil, ""},
	}
	for _, testCase := range cases {
		if got := chatMediaMimeTypeFromBytes(testCase.data); got != testCase.want {
			t.Fatalf("%s: chatMediaMimeTypeFromBytes = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

// mediaDispatchRecorder 捕获进程内派发调用并按路径路由响应。
type mediaDispatchRecorder struct {
	mu    sync.Mutex
	calls []dispatchCall
}

func (r *mediaDispatchRecorder) record(call dispatchCall) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
}

func (r *mediaDispatchRecorder) count(pathPrefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, call := range r.calls {
		if strings.HasPrefix(call.Path, pathPrefix) {
			total++
		}
	}
	return total
}

func TestGenerateChatAudioDispatchAndSniff(t *testing.T) {
	recorder := &mediaDispatchRecorder{}
	executor := &mockExecutor{steps: []scriptStep{{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/audio/speech") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			recorder.record(call)
			return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(string(mediaWAVFixture)))}
		},
	}}}
	artifact, err := GenerateChatAudio(context.Background(), executor, ChatAudioGenerationRequest{
		Model: "gpt-4o-mini-tts", Text: "你好世界", Voice: "alloy", Format: "wav",
	}, "chat-secret", "trace-1")
	if err != nil {
		t.Fatalf("GenerateChatAudio: %v", err)
	}
	if artifact.MimeType != "audio/wav" {
		t.Fatalf("mimeType = %q, want audio/wav", artifact.MimeType)
	}
	if artifact.SHA256 == "" || artifact.Bytes != int64(len(mediaWAVFixture)) {
		t.Fatalf("artifact digest/bytes 异常: %+v", artifact)
	}
	call := recorder.calls[0]
	if call.Path != "/v1/audio/speech" || call.Headers["x-purpose"] != chatMediaPurpose || call.Headers["authorization"] != "Bearer chat-secret" {
		t.Fatalf("dispatch 形态异常: %+v", call)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
		t.Fatalf("body 解析失败: %v", err)
	}
	if body["model"] != "gpt-4o-mini-tts" || body["input"] != "你好世界" || body["voice"] != "alloy" || body["response_format"] != "wav" {
		t.Fatalf("body 字段异常: %s", call.Body)
	}
}

func TestGenerateChatAudioRejections(t *testing.T) {
	respond := func(status int, payload string) *mockExecutor {
		return &mockExecutor{steps: []scriptStep{{respond: func(call dispatchCall) *GenerationDispatchResponse {
			return &GenerationDispatchResponse{Status: status, Body: io.NopCloser(strings.NewReader(payload))}
		}}}}
	}
	if _, err := GenerateChatAudio(context.Background(), respond(200, `{"error":{"message":"quota"}}`), ChatAudioGenerationRequest{Model: "tts-1", Text: "hi"}, "k", ""); err == nil {
		t.Fatal("2xx JSON 错误体应按无效音频格式失败")
	}
	oversize := make([]byte, chatMediaAudioMaxBytes+1)
	copy(oversize, mediaMP3Fixture)
	if _, err := GenerateChatAudio(context.Background(), respond(200, string(oversize)), ChatAudioGenerationRequest{Model: "tts-1", Text: "hi"}, "k", ""); err == nil || !strings.Contains(err.Error(), "32 MiB") {
		t.Fatalf("超限音频应失败并携带限额文案: %v", err)
	}
	if _, err := GenerateChatAudio(context.Background(), respond(403, `{"error":{"message":"denied"}}`), ChatAudioGenerationRequest{Model: "tts-1", Text: "hi"}, "k", ""); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("上游错误应透传 message: %v", err)
	}
	if _, err := GenerateChatAudio(context.Background(), respond(200, string(mediaMP4Fixture)), ChatAudioGenerationRequest{Model: "tts-1", Text: "hi"}, "k", ""); err == nil {
		t.Fatal("视频字节不应作为音频产物接受")
	}
}

func TestCreateChatVideoDispatch(t *testing.T) {
	recorder := &mediaDispatchRecorder{}
	executor := &mockExecutor{steps: []scriptStep{{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/videos") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			recorder.record(call)
			return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(`{"id":"video_abc","status":"queued","progress":0}`))}
		},
	}}}
	seconds := 8.0
	created, err := CreateChatVideo(context.Background(), executor, ChatVideoCreationRequest{
		Model: "sora-2", Prompt: "一只猫在月球上跳舞", Seconds: &seconds, Size: "1280x720",
	}, "chat-secret", "")
	if err != nil {
		t.Fatalf("CreateChatVideo: %v", err)
	}
	if created.JobID != "video_abc" || created.Status != "queued" || created.Model != "sora-2" {
		t.Fatalf("created = %+v", created)
	}
	call := recorder.calls[0]
	if call.Path != "/v1/videos" || call.Headers["x-purpose"] != chatMediaPurpose {
		t.Fatalf("dispatch 形态异常: %+v", call)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["seconds"] != 8.0 || body["size"] != "1280x720" {
		t.Fatalf("body 字段异常: %s", call.Body)
	}

	missingID := &mockExecutor{steps: []scriptStep{{respond: func(call dispatchCall) *GenerationDispatchResponse {
		return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(`{"status":"queued"}`))}
	}}}}
	if _, err := CreateChatVideo(context.Background(), missingID, ChatVideoCreationRequest{Model: "sora-2", Prompt: "p"}, "k", ""); err == nil || !strings.Contains(err.Error(), "job id") {
		t.Fatalf("缺 job id 应失败: %v", err)
	}
	rejected := &mockExecutor{steps: []scriptStep{{respond: func(call dispatchCall) *GenerationDispatchResponse {
		return &GenerationDispatchResponse{Status: 400, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"size invalid"}}`))}
	}}}}
	if _, err := CreateChatVideo(context.Background(), rejected, ChatVideoCreationRequest{Model: "sora-2", Prompt: "p"}, "k", ""); err == nil || !strings.Contains(err.Error(), "size invalid") {
		t.Fatalf("参数错误应透传: %v", err)
	}
}

func TestPollAndDownloadChatMediaTask(t *testing.T) {
	executor := &mockExecutor{steps: []scriptStep{
		{match: func(call dispatchCall) bool { return call.Path == "/v1/videos/video_abc" }, respond: func(call dispatchCall) *GenerationDispatchResponse {
			return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(`{"id":"video_abc","status":"completed","progress":100}`))}
		}},
		{match: func(call dispatchCall) bool { return call.Path == "/v1/videos/video_abc/content" }, respond: func(call dispatchCall) *GenerationDispatchResponse {
			return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(string(mediaMP4Fixture)))}
		}},
		{match: func(call dispatchCall) bool { return call.Path == "/v1/videos/video_missing" }, respond: func(call dispatchCall) *GenerationDispatchResponse {
			return &GenerationDispatchResponse{Status: 404, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Video not found"}}`))}
		}},
	}}
	polled, err := PollChatMediaTask(context.Background(), executor, "video_abc", "k", "")
	if err != nil {
		t.Fatalf("PollChatMediaTask: %v", err)
	}
	if polled.Status != "completed" || polled.Progress == nil || *polled.Progress != 100 {
		t.Fatalf("polled = %+v", polled)
	}
	artifact, err := DownloadChatMediaContent(context.Background(), executor, "video_abc", "k", "")
	if err != nil {
		t.Fatalf("DownloadChatMediaContent: %v", err)
	}
	if artifact.MimeType != "video/mp4" {
		t.Fatalf("mimeType = %q", artifact.MimeType)
	}
	if _, err := PollChatMediaTask(context.Background(), executor, "video_missing", "k", ""); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("404 应归一为不存在: %v", err)
	}
}

func TestDownloadChatMediaContentLimits(t *testing.T) {
	oversize := make([]byte, chatMediaVideoMaxBytes+1)
	copy(oversize, mediaMP4Fixture)
	executor := &mockExecutor{steps: []scriptStep{{respond: func(call dispatchCall) *GenerationDispatchResponse {
		return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(string(oversize)))}
	}}}}
	if _, err := DownloadChatMediaContent(context.Background(), executor, "video_abc", "k", ""); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatalf("超限视频应失败: %v", err)
	}
	notVideo := &mockExecutor{steps: []scriptStep{{respond: func(call dispatchCall) *GenerationDispatchResponse {
		return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(string(mediaWAVFixture)))}
	}}}}
	if _, err := DownloadChatMediaContent(context.Background(), notVideo, "video_abc", "k", ""); err == nil || !strings.Contains(err.Error(), "视频格式") {
		t.Fatalf("非视频字节应失败: %v", err)
	}
}

// --- 工具注册与编排器 ---

func TestChatMediaToolRegistry(t *testing.T) {
	// 媒体工具常驻：不受 imageGenerationEnabled 门控（设计 §4.1）。
	registry := newChatInternalToolRegistry("test", false, false)
	names := map[string]bool{}
	for _, definition := range registry.resolveTools(true) {
		names[definition.ModelName] = true
	}
	if !names["generate_video"] || !names["generate_audio"] {
		t.Fatalf("媒体工具未注册: %v", names)
	}
	video := registry.definitions["generate_video"]
	audio := registry.definitions["generate_audio"]
	if video.Kind != "model" || audio.Kind != "model" {
		t.Fatalf("kind 异常: %s / %s", video.Kind, audio.Kind)
	}
	if video.RequiresImageGenerationEnabled || audio.RequiresImageGenerationEnabled {
		t.Fatal("媒体工具不应携带生图门控")
	}
}

func TestChatMediaToolOrchestratorStages(t *testing.T) {
	registry := newChatInternalToolRegistry("test", true, true)
	tools := registry.resolveTools(true)
	context := &chatToolExecutionContext{
		VideoGeneration: func(request ChatVideoCreationRequest) (ChatVideoCreationResult, error) {
			return ChatVideoCreationResult{JobID: "video_job1", Status: "queued", Model: request.Model, Prompt: request.Prompt}, nil
		},
	}
	var events []ChatToolExecutionEvent
	orchestrator := newChatInternalToolOrchestrator(registry, tools, context, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 8, MaxImageCalls: 2}, func(event ChatToolExecutionEvent) {
		events = append(events, event)
	})
	_, err := orchestrator.Run(func(round int, continuation []any) (ChatToolModelTurn, error) {
		if round == 1 {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "call_v", ToolName: "generate_video", ArgumentsJSON: `{"prompt":"跳舞的猫"}`}}}, nil
		}
		return ChatToolModelTurn{Content: "任务已创建"}, nil
	})
	if err != nil {
		t.Fatalf("orchestrator: %v", err)
	}
	var started, completed *ChatToolExecutionEvent
	for index := range events {
		switch {
		case events[index].Status == "started" && events[index].ToolName == "generate_video":
			started = &events[index]
		case events[index].Status == "completed" && events[index].ToolName == "generate_video":
			completed = &events[index]
		}
	}
	if started == nil {
		t.Fatal("缺少 started 事件")
	}
	progress, ok := started.PublicResult["progress"].(chatWebSearchProgress)
	if !ok || progress.Stage != "submitting" {
		t.Fatalf("started progress = %v, want submitting", started.PublicResult["progress"])
	}
	if completed == nil || completed.PublicResult["jobId"] != "video_job1" {
		t.Fatalf("completed = %+v", completed)
	}
}

func TestChatAudioToolExecutionAndSink(t *testing.T) {
	fixture := newChatFixture(t)
	_, clock := fixedChatClock()
	objectStore, err := NewLocalObjectStore(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	executor := &mockExecutor{steps: []scriptStep{{match: func(call dispatchCall) bool {
		return strings.HasPrefix(call.Path, "/v1/audio/speech")
	}, respond: func(call dispatchCall) *GenerationDispatchResponse {
		return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(string(mediaMP3Fixture)))}
	}}}}
	rt := &chatRoutes{deps: &Deps{
		Store: fixture.store, Now: clock, ObjectStore: objectStore, RetentionDays: 30,
		Executor: executor, TokenCount: func(text string) int { return len(text) / 4 },
	}}
	// 直接构造已完成的助手消息（资产行绑定要求 turn/message 存在）。
	conversation := fixture.createConversation("chat_conv_audio", "owner-a")
	if _, err := fixture.db.Exec(`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id, sequence_no,
		role, status, content_text, content_blocks_json, content_bytes, storage_reserved_bytes, model, created_at, completed_at, expires_at)
		VALUES ('msg_asst_audio', ?, 'owner-a', 'turn_audio', 2, 'assistant', 'streaming', '生成中', '[]', 8, 448000, 'gpt-5', ?, ?, ?)`,
		conversation.ID, fixture.nowISO, fixture.nowISO, fixture.nowISO); err != nil {
		t.Fatal(err)
	}
	sink := &storeGeneratedMediaSink{
		routes: rt, ownerID: "owner-a", conversationID: conversation.ID,
		turnID: "turn_audio", assistantMessageID: "msg_asst_audio", nextContentOrder: func() int64 { return 1 },
	}
	context := &chatToolExecutionContext{
		DefaultAudioModel: "gpt-4o-mini-tts",
		AudioGeneration: func(request ChatAudioGenerationRequest) (ChatMediaArtifactResult, error) {
			if request.Model == "" {
				request.Model = "gpt-4o-mini-tts"
			}
			return GenerateChatAudio(context.Background(), executor, request, "k", "")
		},
		MediaArtifactSink: sink,
	}
	tools := []*toolDefinition{newGenerateAudioTool()}
	var toolEvents []ChatToolExecutionEvent
	orchestrator := newChatInternalToolOrchestrator(newChatInternalToolRegistry("test", true, true), tools, context, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 8}, func(event ChatToolExecutionEvent) {
		toolEvents = append(toolEvents, event)
	})
	result, err := orchestrator.Run(func(round int, continuation []any) (ChatToolModelTurn, error) {
		if round == 1 {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "call_a", ToolName: "generate_audio", ArgumentsJSON: `{"text":"早上好"}`}}}, nil
		}
		return ChatToolModelTurn{Content: "已生成"}, nil
	})
	if err != nil {
		t.Fatalf("orchestrator: %v", err)
	}
	for _, event := range toolEvents {
		if event.Status == "failed" {
			t.Fatalf("generate_audio 执行失败: %s / %s", event.ErrorCode, event.ErrorMessage)
		}
	}
	if result.ToolCalls != 1 {
		t.Fatalf("toolCalls = %d", result.ToolCalls)
	}
	// 资产行落库且 MIME/字节正确。
	var mimeType string
	var processedBytes int64
	if err := fixture.db.QueryRow(`SELECT processed_mime_type, processed_bytes FROM chat_assets WHERE conversation_id = ?`, conversation.ID).Scan(&mimeType, &processedBytes); err != nil {
		t.Fatalf("资产未落库: %v", err)
	}
	if mimeType != "audio/mpeg" || processedBytes != int64(len(mediaMP3Fixture)) {
		t.Fatalf("资产 MIME/bytes 异常: %s / %d", mimeType, processedBytes)
	}
}

// --- 候选收敛与约束 ---

// mediaTestCatalog 造出媒体候选：account-1（openai，视频+TTS 端点模式）与
// account-qwen（qwen，仅视频）；目录行 sora-2/gpt-4o-mini-tts（openai）、
// wan2.2-t2v-plus（qwen）。glm 目录行存在但 provider 不在白名单，不进候选。
type mediaTestCatalog struct{}

func (mediaTestCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	switch groupID {
	case "group-a":
		return []ChatTransportAccount{{
			ID: "account-1", Type: "api_key", ProviderCode: "openai",
			SupportedEndpointModes: []string{"chat_sse", "video_create", "audio_speech"},
		}}
	case "group-qwen":
		return []ChatTransportAccount{{
			ID: "account-qwen", Type: "api_key", ProviderCode: "qwen",
			SupportedEndpointModes: []string{"chat_sse", "video_create"},
		}}
	case "group-glm":
		return []ChatTransportAccount{{
			ID: "account-glm", Type: "api_key", ProviderCode: "glm",
			SupportedEndpointModes: []string{"chat_sse", "video_create", "audio_speech"},
		}}
	}
	return nil
}

func (mediaTestCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	switch providerCode {
	case "openai", "gpt":
		return []ProviderModelCatalogItem{
			{Model: "sora-2", ProviderCode: "gpt", SupportedAPIProtocols: []string{"video"}},
			{Model: "gpt-4o-mini-tts", ProviderCode: "gpt", SupportedAPIProtocols: []string{"audio_speech"}},
			{Model: "gpt-5", ProviderCode: "gpt", SupportedAPIProtocols: []string{"chat_completions"}},
		}
	case "qwen":
		return []ProviderModelCatalogItem{
			{Model: "wan2.2-t2v-plus", ProviderCode: "qwen", SupportedAPIProtocols: []string{"video"}},
		}
	case "glm":
		return []ProviderModelCatalogItem{
			{Model: "cogvideox-3", ProviderCode: "glm", SupportedAPIProtocols: []string{"video"}},
		}
	}
	return nil
}

type mediaTestLookup struct{}

func (mediaTestLookup) FindChatAccount(scope ChatBindScope, accountID string) (*ChatAccountRef, error) {
	switch accountID {
	case "account-1":
		return &ChatAccountRef{ID: accountID, Name: "账户 account-1", ProviderCode: "openai", Enabled: true, EnabledGroupIDs: []string{"group-a"}}, nil
	case "account-qwen":
		return &ChatAccountRef{ID: accountID, Name: "万相账户", ProviderCode: "qwen", Enabled: true, EnabledGroupIDs: []string{"group-qwen"}}, nil
	case "account-glm":
		return &ChatAccountRef{ID: accountID, Name: "GLM 账户", ProviderCode: "glm", Enabled: true, EnabledGroupIDs: []string{"group-glm"}}, nil
	}
	return nil, nil
}

type mediaTestOptions struct{}

func (mediaTestOptions) ListChatAccountOptions(_ context.Context, scope ChatBindScope) ([]ChatAccountOption, error) {
	return []ChatAccountOption{
		{ID: "account-1", Name: "账户 account-1", ProviderCode: "openai", Status: "active"},
		{ID: "account-qwen", Name: "万相账户", ProviderCode: "qwen", Status: "active"},
		{ID: "account-glm", Name: "GLM 账户", ProviderCode: "glm", Status: "active"},
	}, nil
}

func newMediaRoutesForCandidates(t *testing.T) *chatRoutes {
	t.Helper()
	fixture := newChatFixture(t)
	_, clock := fixedChatClock()
	return &chatRoutes{deps: &Deps{
		Store: fixture.store, Now: clock,
		ModelCatalog:          mediaTestCatalog{},
		AccountLookup:         mediaTestLookup{},
		AccountOptionsLookup:  mediaTestOptions{},
		MaxTurnsPerConversation: 10,
	}}
}

func TestResolveChatToolBindingCandidatesMedia(t *testing.T) {
	rt := newMediaRoutesForCandidates(t)
	candidates, err := rt.resolveChatToolBindingCandidates(ChatBindScope{ViewerID: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	join := func(list []ChatToolBindingCandidate) string {
		parts := []string{}
		for _, candidate := range list {
			parts = append(parts, candidate.AccountID+"@"+candidate.ModelID)
		}
		return strings.Join(parts, ",")
	}
	if got := join(candidates.video); got != "account-1@sora-2,account-qwen@wan2.2-t2v-plus" {
		t.Fatalf("video candidates = %s", got)
	}
	if got := join(candidates.audio); got != "account-1@gpt-4o-mini-tts" {
		t.Fatalf("audio candidates = %s", got)
	}
	// glm 目录行存在但不在供应商白名单。
	for _, candidate := range append(candidates.video, candidates.audio...) {
		if candidate.AccountID == "account-glm" {
			t.Fatal("glm 不应进入媒体候选")
		}
	}
}

func TestConstrainChatMediaModel(t *testing.T) {
	candidates := []ChatToolBindingCandidate{
		{AccountID: "account-1", ModelID: "sora-2"},
		{AccountID: "account-1", ModelID: "sora-2-pro"},
		{AccountID: "account-qwen", ModelID: "wan2.2-t2v-plus"},
	}
	if got := constrainChatMediaModel("sora-2", "account-1", "sora-2-pro", candidates); got != "sora-2" {
		t.Fatalf("已支持模型应原样返回: %s", got)
	}
	if got := constrainChatMediaModel("nope", "account-1", "sora-2-pro", candidates); got != "sora-2-pro" {
		t.Fatalf("未支持模型应回落默认: %s", got)
	}
	if got := constrainChatMediaModel("nope", "account-1", "", candidates); got != "sora-2" {
		t.Fatalf("无默认应回落候选首项: %s", got)
	}
	if got := constrainChatMediaModel("wan2.2-t2v-plus", "account-1", "sora-2-pro", candidates); got != "sora-2-pro" {
		t.Fatalf("跨账户模型应收敛到本账户集合: %s", got)
	}
	if got := constrainChatMediaModel("anything", "", "sora-2-pro", candidates); got != "anything" {
		t.Fatalf("未绑定不约束: %s", got)
	}
}

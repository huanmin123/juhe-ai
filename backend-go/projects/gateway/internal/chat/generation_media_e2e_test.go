package chat

// M7d chat 域问答媒体 E2E（docs/functions/问答音视频工具设计.md §2/§3/§4）：
//
//   - generate_audio 全链：主模型 function calling 触发工具（沿 w10d
//     chatCompletionsToolSSE 先例）→ /v1/audio/speech 经 executor mock 转发到
//     真实 mockupstream HTTP（media_tts_ok 场景按 response_format 协商 mp3
//     载荷）→ 字节嗅探 → chat_assets 落库 → output_audio 块投影。
//   - generate_video 任务链：工具创建 job（POST /v1/videos 报文按契约手写）→
//     output_media_task 块 → media-tasks 轮询（fake media_jobs 快照
//     in_progress，poll 推进 completed，content 回 mockupstream 的
//     PlayableMP4Payload 真实可播放字节）→ 结算落资产 → 块 assetId 补丁 +
//     资产字节含 moov。
//
// 取舍：音频链走真实 mockupstream HTTP（executor scriptStep 原样代理）；视频
// poll/content 不代理 mockupstream——其 videos 面仍按契约回 ftyp+mdat 占位
// 载荷（/v1 域 magic bytes 断言依据），故按报文契约手写响应、content 直接回
// PlayableMP4Payload()，验证 chat 侧对真实可播放字节的完整结算。

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// mediaE2ECatalog 合并主链与媒体目录视图：账户沿用 mockModelCatalog 的
// gpt-5 映射（主对话派发依据），端点模式并上 video_create/audio_speech
// （媒体候选解析依据，mediaTestCatalog 同款）；provider 目录合并两侧条目。
type mediaE2ECatalog struct{}

func (mediaE2ECatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	accounts := (mockModelCatalog{}).ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily)
	for index := range accounts {
		if accounts[index].ID == "account-1" {
			accounts[index].SupportedEndpointModes = []string{"chat_sse", "responses_sse", "video_create", "audio_speech"}
		}
	}
	return accounts
}

func (mediaE2ECatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	items := (mockModelCatalog{}).ListProviderCatalog(providerCode, systemAccountID)
	items = append(items, (mediaTestCatalog{}).ListProviderCatalog(providerCode, systemAccountID)...)
	return items
}

// proxyStepToMockUpstream 返回一个把进程内派发原样转发到真实 mockupstream
// HTTP 服务的 scriptStep：保留请求头与请求体，注入 X-Mock-Scenario 场景头，
// 把上游响应（状态/头/流式体）转回 GenerationDispatchResponse。
func proxyStepToMockUpstream(t *testing.T, server *platformmock.Server, scenario, pathPrefix string) scriptStep {
	t.Helper()
	return scriptStep{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, pathPrefix) },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			request, err := http.NewRequest(http.MethodPost, server.URL+call.Path, strings.NewReader(call.Body))
			if err != nil {
				t.Fatalf("构造 mockupstream 请求失败: %v", err)
				return nil
			}
			for key, value := range call.Headers {
				request.Header.Set(key, value)
			}
			request.Header.Set("X-Mock-Scenario", scenario)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatalf("mockupstream 请求失败: %v", err)
				return nil
			}
			return &GenerationDispatchResponse{Status: response.StatusCode, Header: response.Header, Body: response.Body}
		},
	}
}

// bindConversationMedia 写入会话媒体绑定列（audio/video 二选一或全写）。
func bindConversationMedia(t *testing.T, env *generationEnv, conversationID string, audioModel, videoModel string) {
	t.Helper()
	if audioModel != "" {
		if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET audio_account_id = 'account-1', default_audio_model = ? WHERE id = ?`, audioModel, conversationID); err != nil {
			t.Fatal(err)
		}
	}
	if videoModel != "" {
		if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET video_account_id = 'account-1', default_video_model = ? WHERE id = ?`, videoModel, conversationID); err != nil {
			t.Fatal(err)
		}
	}
}

// latestAssistantMessageID 取会话最新助手消息 ID（E2E 断言消息块用）。
func latestAssistantMessageID(t *testing.T, env *generationEnv, conversationID string) string {
	t.Helper()
	var messageID string
	if err := env.fixture.db.QueryRow(`SELECT id FROM chat_messages WHERE conversation_id = ? AND role = 'assistant' ORDER BY sequence_no DESC LIMIT 1`, conversationID).Scan(&messageID); err != nil {
		t.Fatalf("查询助手消息失败: %v", err)
	}
	return messageID
}

// TestChatAudioToolE2EViaMockUpstream 驱动 generate_audio 全链：主模型工具
// 调用 → mockupstream media_tts_ok（真实 HTTP，mp3 载荷）→ 资产落库 →
// output_audio 块。
func TestChatAudioToolE2EViaMockUpstream(t *testing.T) {
	upstream := platformmock.New()
	defer upstream.Close()
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = mediaE2ECatalog{}
	env.deps.AccountOptionsLookup = mediaTestOptions{}
	conversation := env.fixture.createConversation("conv_media_audio_e2e", routeTestOwner)
	bindConversationMedia(t, env, conversation.ID, "gpt-4o-mini-tts", "")

	round := 0
	env.executor.steps = []scriptStep{
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				round++
				if round == 1 {
					return sseResponse(chatCompletionsToolSSE("call_audio_1", "generate_audio", `{"text":"早上好，世界"}`))
				}
				return sseResponse(chatCompletionsSSE("语音已生成", true))
			},
		},
		proxyStepToMockUpstream(t, upstream, string(platformmock.ScenarioMediaTTSOK), "/v1/audio/speech"),
	}

	response := env.streamPost(conversation.ID, routeTestOwner, streamPayload("media-audio-e2e-1", "把这句话念出来", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("流式状态 = %d: %s", response.status, response.rawString())
	}
	if round != 2 {
		t.Fatalf("主模型轮次 = %d, want 2", round)
	}
	raw := response.rawString()
	if !strings.Contains(raw, "generate_audio") || !strings.Contains(raw, "output_audio") {
		t.Fatalf("SSE 缺少 generate_audio 工具事件或 output_audio 块: %s", raw)
	}
	if !strings.Contains(raw, `"mimeType":"audio/mpeg"`) || !strings.Contains(raw, `"assetId":"chat_asset_`) {
		t.Fatalf("SSE 缺少 audio/* MIME 或 assetId: %s", raw)
	}

	// mockupstream 侧收到契约形态的 TTS 请求：模型经会话默认绑定收敛、
	// response_format 默认 mp3、purpose 审计头透传。
	sawSpeech := false
	for _, request := range upstream.Requests() {
		if request.Path != "/v1/audio/speech" {
			continue
		}
		sawSpeech = true
		if !strings.Contains(request.Body, `"model":"gpt-4o-mini-tts"`) ||
			!strings.Contains(request.Body, `"response_format":"mp3"`) ||
			!strings.Contains(request.Body, `"input":"早上好，世界"`) {
			t.Fatalf("TTS 请求体异常: %s", request.Body)
		}
		if !strings.Contains(request.AuthHeader, "Bearer ") {
			t.Fatalf("TTS 请求缺少 Bearer 头: %q", request.AuthHeader)
		}
	}
	if !sawSpeech {
		t.Fatal("mockupstream 未收到 /v1/audio/speech 请求")
	}

	// 资产落库：MIME 嗅探出 audio/mpeg，绑定助手消息。
	var assetID, mimeType, messageID string
	var processedBytes int64
	if err := env.fixture.db.QueryRow(`SELECT id, processed_mime_type, processed_bytes, message_id FROM chat_assets WHERE conversation_id = ?`, conversation.ID).Scan(&assetID, &mimeType, &processedBytes, &messageID); err != nil {
		t.Fatalf("资产未落库: %v", err)
	}
	if !strings.HasPrefix(mimeType, "audio/") || mimeType != "audio/mpeg" || processedBytes == 0 {
		t.Fatalf("资产 MIME/字节异常: %s / %d", mimeType, processedBytes)
	}
	if messageID != latestAssistantMessageID(t, env, conversation.ID) {
		t.Fatalf("资产未绑定助手消息: %s", messageID)
	}

	// 助手消息块：output_audio 携带 assetId + audio/* mimeType。
	blocks := readMessageBlocks(t, env, conversation.ID, messageID)
	var audioBlock map[string]any
	for _, block := range blocks {
		if block["type"] == "output_audio" {
			audioBlock = block
		}
	}
	if audioBlock == nil {
		t.Fatalf("助手消息缺少 output_audio 块: %v", blocks)
	}
	if audioBlock["assetId"] != assetID {
		t.Fatalf("output_audio assetId = %v, want %s", audioBlock["assetId"], assetID)
	}
	if blockMime, _ := audioBlock["mimeType"].(string); !strings.HasPrefix(blockMime, "audio/") {
		t.Fatalf("output_audio mimeType = %v", audioBlock["mimeType"])
	}
}

// TestGenerateVideoTaskChainE2E 驱动 generate_video 任务链：工具创建 job →
// output_media_task 块 → media-tasks 轮询推进 completed → 结算落资产（真实
// 可播放 MP4 字节）→ 块 assetId 补丁。
func TestGenerateVideoTaskChainE2E(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = mediaE2ECatalog{}
	env.deps.AccountOptionsLookup = mediaTestOptions{}
	env.deps.MediaJobs = &fakeMediaJobs{status: "in_progress"}
	conversation := env.fixture.createConversation("conv_media_video_e2e", routeTestOwner)
	bindConversationMedia(t, env, conversation.ID, "", "sora-2")

	var createBody string
	var contentCalls int32
	round := 0
	env.executor.steps = []scriptStep{
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				round++
				if round == 1 {
					return sseResponse(chatCompletionsToolSSE("call_video_1", "generate_video", `{"prompt":"一只猫在月球上跳舞","seconds":8}`))
				}
				return sseResponse(chatCompletionsSSE("视频任务已创建，完成后会出现在消息里", true))
			},
		},
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/videos" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				createBody = call.Body
				return jsonStatusResponse(200, `{"id":"video_job1","status":"queued","progress":0}`)
			},
		},
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/videos/video_job1" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return jsonStatusResponse(200, `{"id":"video_job1","status":"completed","progress":100}`)
			},
		},
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/videos/video_job1/content" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				atomic.AddInt32(&contentCalls, 1)
				playable := platformmock.PlayableMP4Payload()
				return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(bytes.NewReader(playable))}
			},
		},
	}

	response := env.streamPost(conversation.ID, routeTestOwner, streamPayload("media-video-e2e-1", "生成一段跳舞猫视频", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("流式状态 = %d: %s", response.status, response.rawString())
	}
	if round != 2 {
		t.Fatalf("主模型轮次 = %d, want 2", round)
	}
	// 创建请求按契约：模型经会话默认绑定收敛 sora-2、prompt 与 seconds 透传。
	if !strings.Contains(createBody, `"model":"sora-2"`) ||
		!strings.Contains(createBody, `"prompt":"一只猫在月球上跳舞"`) ||
		!strings.Contains(createBody, `"seconds":8`) {
		t.Fatalf("POST /v1/videos 请求体异常: %s", createBody)
	}
	// 工具轮次内只创建任务：不触发 poll/content（异步轮询由 media-tasks 驱动）。
	if atomic.LoadInt32(&contentCalls) != 0 {
		t.Fatalf("工具轮次不应下载产物: %d", contentCalls)
	}
	raw := response.rawString()
	if !strings.Contains(raw, "generate_video") || !strings.Contains(raw, "output_media_task") || !strings.Contains(raw, "video_job1") {
		t.Fatalf("SSE 缺少 generate_video 事件或 output_media_task 块: %s", raw)
	}

	messageID := latestAssistantMessageID(t, env, conversation.ID)
	taskBlock := func() map[string]any {
		for _, block := range readMessageBlocks(t, env, conversation.ID, messageID) {
			if block["type"] == "output_media_task" {
				return block
			}
		}
		return nil
	}
	if taskBlock() == nil {
		t.Fatalf("助手消息缺少 output_media_task 块")
	}

	// 前端轮询：in_progress 快照 → poll 推进 completed → 结算下载真实可播放
	// MP4 → 落资产 → 块补丁 assetId。
	polled := env.do("GET", mediaTaskURL(conversation.ID), routeTestOwner, "")
	if polled.status != http.StatusOK {
		t.Fatalf("media-tasks 状态 = %d: %s", polled.status, polled.rawString())
	}
	data := polled.dataMap()
	if data["status"] != "completed" || data["assetId"] == nil || data["assetId"] == "" {
		t.Fatalf("轮询负载异常: %v", data)
	}
	if atomic.LoadInt32(&contentCalls) != 1 {
		t.Fatalf("content 下载次数 = %d, want 1", contentCalls)
	}
	assetID, _ := data["assetId"].(string)
	settled := taskBlock()
	if settled["status"] != "completed" || settled["assetId"] != assetID {
		t.Fatalf("任务块结算补丁异常: %v", settled)
	}

	// 资产行：video/mp4；对象字节是真实可播放 MP4（含 moov，完整可解码链）。
	var storageKey, mimeType string
	var processedBytes int64
	if err := env.fixture.db.QueryRow(`SELECT storage_key, processed_mime_type, processed_bytes FROM chat_assets WHERE id = ?`, assetID).Scan(&storageKey, &mimeType, &processedBytes); err != nil {
		t.Fatalf("资产未落库: %v", err)
	}
	if mimeType != "video/mp4" {
		t.Fatalf("资产 MIME = %s, want video/mp4", mimeType)
	}
	stored, err := os.ReadFile(filepath.Join(env.objectDir, filepath.FromSlash(storageKey)))
	if err != nil {
		t.Fatalf("读取资产对象失败: %v", err)
	}
	if !strings.Contains(string(stored), "moov") || !strings.Contains(string(stored), "mdat") {
		t.Fatalf("资产字节缺少 moov/mdat 盒: %d 字节", len(stored))
	}
	if len(stored) != len(platformmock.PlayableMP4Payload()) {
		t.Fatalf("资产字节长度 = %d, want %d", len(stored), len(platformmock.PlayableMP4Payload()))
	}
}

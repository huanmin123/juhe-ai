package chat

// M7 media-tasks 任务接口与绑定契约的路由级覆盖（问答音视频工具设计 §3/§7.1）：
// 终态结算落资产 + 消息块定点补丁、幂等（重复轮询/并发两请求一资产）、失败
// 终态、归属校验、tool-preferences 与会话 PATCH 的 videoBinding/audioBinding 键。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeMediaJobs 是 ChatMediaJobsLookup 的测试实现（media_jobs 行快照直供）。
type fakeMediaJobs struct {
	status string
}

func (f *fakeMediaJobs) FindChatMediaJob(jobID, apiKeyID string) (ChatMediaJobSnapshot, bool, error) {
	if jobID != "video_job1" {
		return ChatMediaJobSnapshot{}, false, nil
	}
	return ChatMediaJobSnapshot{ID: jobID, Kind: "video", Status: f.status, UpstreamJobID: "up-1"}, true, nil
}

// insertMediaTaskMessage 直插一条含 output_media_task 块的已完成助手消息
//（绕过流式链路：结算只依赖消息行与块 JSON）。
func insertMediaTaskMessage(t *testing.T, env *generationEnv, conversationID, messageID, blockStatus string) {
	t.Helper()
	blocks := fmt.Sprintf(`[{"type":"output_text","blockId":"assistant_block_1","order":1,"text":"视频生成中"},{"type":"output_media_task","blockId":"assistant_block_2","order":2,"jobId":"video_job1","kind":"video","status":%q,"model":"sora-2","promptSummary":"跳舞的猫"}]`, blockStatus)
	if _, err := env.fixture.db.Exec(`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id, sequence_no,
		role, status, content_text, content_blocks_json, content_bytes, storage_reserved_bytes, model, created_at, completed_at, expires_at)
		VALUES (?, ?, ?, 'turn_v1', 2, 'assistant', 'completed', '视频生成中', ?, ?, 0, 'gpt-5', ?, ?, ?)`,
		messageID, conversationID, "owner-a", blocks, len(blocks), env.fixture.nowISO, env.fixture.nowISO, env.fixture.nowISO); err != nil {
		t.Fatal(err)
	}
}

// newMediaTaskEnv 构建带媒体任务面的路由环境（MediaJobs 端口 + 内容下载
// executor 步骤）。
func newMediaTaskEnv(t *testing.T, jobStatus string) (*generationEnv, *fakeMediaJobs, *int32) {
	t.Helper()
	env := newGenerationEnv(t)
	jobs := &fakeMediaJobs{status: jobStatus}
	env.deps.MediaJobs = jobs
	var contentCalls int32
	env.executor.steps = []scriptStep{{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/videos/video_job1/content") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			atomic.AddInt32(&contentCalls, 1)
			return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(string(mediaMP4Fixture)))}
		},
	}}
	return env, jobs, &contentCalls
}

func mediaTaskURL(conversationID string) string {
	return "/__aisys__/api/my-chat/conversations/" + conversationID + "/media-tasks/video_job1"
}

func TestMediaTaskSettlementCompleted(t *testing.T) {
	env, _, contentCalls := newMediaTaskEnv(t, "in_progress")
	conversation := env.fixture.createConversation("chat_conv_v1", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_v1", "in_progress")
	// in_progress 快照 + 轮询响应 completed：poll 走 /v1 任务面。
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return call.Path == "/v1/videos/video_job1" }, respond: func(call dispatchCall) *GenerationDispatchResponse {
			return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(`{"id":"video_job1","status":"completed","progress":100}`))}
		}})
	response := env.do("GET", mediaTaskURL(conversation.ID), "owner-a", "")
	if response.status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.status, response.rawString())
	}
	data := response.dataMap()
	if data["status"] != "completed" || data["assetId"] == nil || data["assetId"] == "" {
		t.Fatalf("payload = %v", data)
	}
	// 资产行落库：video/mp4、绑定消息。
	var mimeType, messageID string
	var processedBytes int64
	if err := env.fixture.db.QueryRow(`SELECT processed_mime_type, processed_bytes, message_id FROM chat_assets WHERE conversation_id = ?`, conversation.ID).Scan(&mimeType, &processedBytes, &messageID); err != nil {
		t.Fatalf("资产未落库: %v", err)
	}
	if mimeType != "video/mp4" || messageID != "msg_asst_v1" {
		t.Fatalf("资产 MIME/绑定异常: %s / %s", mimeType, messageID)
	}
	// 块定点补丁：completed + assetId，其他块不动。
	blocks := readMessageBlocks(t, env, conversation.ID, "msg_asst_v1")
	task := blocks[1]
	if task["status"] != "completed" || task["assetId"] != data["assetId"] {
		t.Fatalf("任务块补丁异常: %v", task)
	}
	if blocks[0]["type"] != "output_text" || blocks[0]["text"] != "视频生成中" {
		t.Fatalf("其他块被改动: %v", blocks[0])
	}
	// 幂等：重复轮询不再下载（块已带 assetId）。
	again := env.do("GET", mediaTaskURL(conversation.ID), "owner-a", "")
	if again.status != http.StatusOK || again.dataMap()["assetId"] != data["assetId"] {
		t.Fatalf("重复结算应复用资产: %s", again.rawString())
	}
	if atomic.LoadInt32(contentCalls) != 1 {
		t.Fatalf("content 下载次数 = %d, want 1", *contentCalls)
	}
}

func TestMediaTaskSettlementConcurrentSingleAsset(t *testing.T) {
	env, _, contentCalls := newMediaTaskEnv(t, "completed")
	conversation := env.fixture.createConversation("chat_conv_v2", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_v2", "in_progress")
	var wg sync.WaitGroup
	assetIDs := make([]string, 5)
	for index := 0; index < 5; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			response := env.do("GET", mediaTaskURL(conversation.ID), "owner-a", "")
			if response.status != http.StatusOK {
				t.Errorf("并发请求 %d 失败: %s", slot, response.rawString())
				return
			}
			assetIDs[slot] = fmt.Sprint(response.dataMap()["assetId"])
		}(index)
	}
	wg.Wait()
	first := assetIDs[0]
	for _, assetID := range assetIDs {
		if assetID == "" || assetID != first {
			t.Fatalf("并发结算资产不一致: %v", assetIDs)
		}
	}
	if atomic.LoadInt32(contentCalls) != 1 {
		t.Fatalf("content 下载次数 = %d, want 1（分条互斥 + 块 assetId 复用）", *contentCalls)
	}
	var assetCount int
	if err := env.fixture.db.QueryRow(`SELECT COUNT(*) FROM chat_assets WHERE conversation_id = ?`, conversation.ID).Scan(&assetCount); err != nil || assetCount != 1 {
		t.Fatalf("资产行数 = %d err=%v, want 1", assetCount, err)
	}
}

func TestMediaTaskSettlementFailed(t *testing.T) {
	env, _, _ := newMediaTaskEnv(t, "failed")
	conversation := env.fixture.createConversation("chat_conv_v3", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_v3", "in_progress")
	response := env.do("GET", mediaTaskURL(conversation.ID), "owner-a", "")
	if response.status != http.StatusOK {
		t.Fatalf("status = %d: %s", response.status, response.rawString())
	}
	if response.dataMap()["status"] != "failed" || response.dataMap()["error"] == nil {
		t.Fatalf("payload = %v", response.dataMap())
	}
	blocks := readMessageBlocks(t, env, conversation.ID, "msg_asst_v3")
	if blocks[1]["status"] != "failed" || blocks[1]["error"] == nil {
		t.Fatalf("失败块补丁异常: %v", blocks[1])
	}
}

func TestMediaTaskOwnershipAndMissing(t *testing.T) {
	env, _, _ := newMediaTaskEnv(t, "completed")
	conversation := env.fixture.createConversation("chat_conv_v4", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_v4", "in_progress")
	// 他人会话：404（会话归属）。
	if response := env.do("GET", mediaTaskURL(conversation.ID), "owner-b", ""); response.status != http.StatusNotFound {
		t.Fatalf("跨属主 status = %d", response.status)
	}
	// 本会话但任务不存在：404。
	if response := env.do("GET", "/__aisys__/api/my-chat/conversations/"+conversation.ID+"/media-tasks/video_unknown", "owner-a", ""); response.status != http.StatusNotFound {
		t.Fatalf("未知任务 status = %d", response.status)
	}
	// 任务存在但不属于本会话（无对应块）：404。
	conversationOther := env.fixture.createConversation("chat_conv_v5", "owner-a")
	if response := env.do("GET", mediaTaskURL(conversationOther.ID), "owner-a", ""); response.status != http.StatusNotFound {
		t.Fatalf("跨会话任务 status = %d", response.status)
	}
}

func readMessageBlocks(t *testing.T, env *generationEnv, conversationID, messageID string) []map[string]any {
	t.Helper()
	var blocksJSON string
	if err := env.fixture.db.QueryRow(`SELECT content_blocks_json FROM chat_messages WHERE id = ? AND conversation_id = ?`, messageID, conversationID).Scan(&blocksJSON); err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(blocksJSON), &blocks); err != nil {
		t.Fatal(err)
	}
	return blocks
}

// --- tool-preferences / 会话 PATCH 的媒体绑定契约 ---

// newMediaPrefsEnv 在偏好环境上挂媒体候选 mock（account-1 × sora-2 视频 /
// gpt-4o-mini-tts TTS，见 mediaTestCatalog）。
func newMediaPrefsEnv(t *testing.T) *generationEnv {
	t.Helper()
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = mediaTestCatalog{}
	env.deps.AccountLookup = mediaTestLookup{}
	env.deps.AccountOptionsLookup = mediaTestOptions{}
	return env
}

func TestToolPreferencesMediaBindingContract(t *testing.T) {
	env := newMediaPrefsEnv(t)
	// GET 默认未绑定，tools[] 含两节新工具。
	response := env.do("GET", "/__aisys__/api/my-chat/tool-preferences", "owner-a", "")
	if response.status != http.StatusOK {
		t.Fatalf("status = %d", response.status)
	}
	tools := toolPrefsTools(t, response)
	for _, toolID := range []string{"generate_video", "generate_audio"} {
		tool, ok := tools[toolID]
		if !ok {
			t.Fatalf("tool-preferences 缺少 %s: %v", toolID, tools)
		}
		if tool["bound"] != false {
			t.Fatalf("%s 默认 bound = %v", toolID, tool["bound"])
		}
	}
	// PATCH 候选外二元组 → 400 + 候选负载。
	invalid := env.do("PATCH", "/__aisys__/api/my-chat/tool-preferences", "owner-a", `{"videoBinding":{"accountId":"account-1","modelId":"nope"}}`)
	if invalid.status != http.StatusBadRequest || invalid.code() != "chat_tool_binding_invalid" {
		t.Fatalf("候选外绑定 status = %d code = %s", invalid.status, invalid.code())
	}
	// PATCH 候选内二元组 → 200，GET 回读 bound/valid。
	valid := env.do("PATCH", "/__aisys__/api/my-chat/tool-preferences", "owner-a", `{"videoBinding":{"accountId":"account-1","modelId":"sora-2"},"audioBinding":{"accountId":"account-1","modelId":"gpt-4o-mini-tts"}}`)
	if valid.status != http.StatusOK {
		t.Fatalf("status = %d: %s", valid.status, valid.rawString())
	}
	tools = toolPrefsTools(t, valid)
	toolPrefsAssertBinding(t, tools, "generate_video", "account-1", "sora-2", true, true)
	toolPrefsAssertBinding(t, tools, "generate_audio", "account-1", "gpt-4o-mini-tts", true, true)
	// 解绑回读。
	cleared := env.do("PATCH", "/__aisys__/api/my-chat/tool-preferences", "owner-a", `{"videoBinding":null}`)
	if cleared.status != http.StatusOK {
		t.Fatalf("解绑 status = %d", cleared.status)
	}
	tools = toolPrefsTools(t, cleared)
	toolPrefsAssertBinding(t, tools, "generate_video", "", "", false, false)
	// 未知键拒绝。
	if response := env.do("PATCH", "/__aisys__/api/my-chat/tool-preferences", "owner-a", `{"defaultVideoModel":"sora-2"}`); response.status != http.StatusBadRequest {
		t.Fatalf("未知键 status = %d", response.status)
	}
}

func TestConversationPatchMediaBindingContract(t *testing.T) {
	env := newMediaPrefsEnv(t)
	conversation := env.fixture.createConversation("chat_conv_bind", "owner-a")
	// 候选外 → 400 + 候选。
	invalid := env.do("PATCH", "/__aisys__/api/my-chat/conversations/"+conversation.ID, "owner-a", `{"videoBinding":{"accountId":"account-qwen","modelId":"gpt-4o-mini-tts"}}`)
	if invalid.status != http.StatusBadRequest || invalid.code() != "chat_tool_binding_invalid" {
		t.Fatalf("候选外绑定 status = %d code = %s", invalid.status, invalid.code())
	}
	// 候选内（account-qwen × wan2.2-t2v-plus）→ 200，响应携带绑定列。
	valid := env.do("PATCH", "/__aisys__/api/my-chat/conversations/"+conversation.ID, "owner-a", `{"videoBinding":{"accountId":"account-qwen","modelId":"wan2.2-t2v-plus"},"audioBinding":{"accountId":"account-1","modelId":"gpt-4o-mini-tts"}}`)
	if valid.status != http.StatusOK {
		t.Fatalf("status = %d: %s", valid.status, valid.rawString())
	}
	data := valid.dataMap()
	if data["videoAccountId"] != "account-qwen" || data["defaultVideoModel"] != "wan2.2-t2v-plus" {
		t.Fatalf("会话响应媒体绑定列异常: %v", data)
	}
	if data["audioAccountId"] != "account-1" || data["defaultAudioModel"] != "gpt-4o-mini-tts" {
		t.Fatalf("会话响应音频绑定列异常: %v", data)
	}
	// 用户级默认回写。
	prefs := env.do("GET", "/__aisys__/api/my-chat/tool-preferences", "owner-a", "")
	tools := toolPrefsTools(t, prefs)
	toolPrefsAssertBinding(t, tools, "generate_video", "account-qwen", "wan2.2-t2v-plus", true, true)
	// 解绑。
	cleared := env.do("PATCH", "/__aisys__/api/my-chat/conversations/"+conversation.ID, "owner-a", `{"audioBinding":null}`)
	if cleared.status != http.StatusOK || cleared.dataMap()["audioAccountId"] != nil {
		t.Fatalf("解绑响应异常: %s", cleared.rawString())
	}
}

func TestCreateConversationInheritsMediaPreferences(t *testing.T) {
	env := newMediaPrefsEnv(t)
	if response := env.do("PATCH", "/__aisys__/api/my-chat/tool-preferences", "owner-a", `{"videoBinding":{"accountId":"account-1","modelId":"sora-2"},"audioBinding":{"accountId":"account-1","modelId":"gpt-4o-mini-tts"}}`); response.status != http.StatusOK {
		t.Fatalf("偏好写入失败: %s", response.rawString())
	}
	created := env.do("POST", "/__aisys__/api/my-chat/conversations", "owner-a", "{}")
	if created.status != http.StatusCreated {
		t.Fatalf("status = %d: %s", created.status, created.rawString())
	}
	data := created.dataMap()
	if data["videoAccountId"] != "account-1" || data["defaultVideoModel"] != "sora-2" {
		t.Fatalf("创建继承视频绑定异常: %v", data)
	}
	if data["audioAccountId"] != "account-1" || data["defaultAudioModel"] != "gpt-4o-mini-tts" {
		t.Fatalf("创建继承音频绑定异常: %v", data)
	}
}

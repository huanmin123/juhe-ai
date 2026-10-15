package chat

// 媒体任务结算的持久化失败透出（契约 docs/functions/问答音视频工具设计.md:
// 终态字段残缺 + settlementError = 保持待轮询，前端 chatMediaTaskPolling.ts
// isSettlementPendingSnapshot 同源）：
//
//	store 契约   PatchChatMediaTaskBlock 的 (affected, error) 直连单测（此前
//	            零覆盖）：happy 路径、块缺失、乐观并发 0 行、坏 JSON、
//	            256KiB 超限 DomainError。
//	路由形态    DB 错误 / 256KiB 超限 → HTTP 仍 200，响应删除终态字段
//	            （status/assetId/error）并附 settlementError；
//	            CAS 0 行 + 重读等价 completed（含 assetId）→ 按 completed
//	            返回；重读无等价 → 保守保持待轮形态。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- store 层：PatchChatMediaTaskBlock (affected, error) 契约 ---

func TestPatchChatMediaTaskBlockStoreContract(t *testing.T) {
	env := newGenerationEnv(t)
	conversation := env.fixture.createConversation("chat_conv_patch1", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_patch1", "in_progress")

	rt := newChatRoutesForTest(env.deps)
	input := settleChatMediaTaskInput{conversation: conversation, ownerID: "owner-a", jobID: "video_job1"}

	// hit 取自 store 读面：BlocksJSON 是存储原文（乐观并发守卫按字节比对）。
	hit, err := rt.deps.Store.FindChatMediaTaskBlock(conversation.ID, "owner-a", "video_job1")
	if err != nil || hit == nil {
		t.Fatalf("定位任务块: hit=%v err=%v", hit, err)
	}
	input.block = hit
	hundred := int64(100)
	affected, patchErr := rt.patchMediaTaskBlock(input, ChatMediaTaskBlockPatch{
		Status: stringPtr("completed"), AssetID: stringPtr("asset_x"), Progress: &hundred,
	})
	if patchErr != nil || !affected {
		t.Fatalf("happy patch = (%v, %v), want (true, nil)", affected, patchErr)
	}
	patched := readMessageBlocks(t, env, conversation.ID, "msg_asst_patch1")
	if patched[1]["status"] != "completed" || patched[1]["assetId"] != "asset_x" || patched[1]["progress"] != float64(100) {
		t.Fatalf("任务块补丁未落: %v", patched[1])
	}
	if patched[0]["text"] != "视频生成中" {
		t.Fatalf("兄弟块被改动: %v", patched[0])
	}

	// 乐观并发 0 行：原文守卫不匹配（happy 补丁后的行已变化）→ (false, nil)，
	// 行不被二次改写。
	affected, patchErr = rt.patchMediaTaskBlock(input, ChatMediaTaskBlockPatch{Status: stringPtr("failed")})
	if patchErr != nil || affected {
		t.Fatalf("陈旧原文补丁 = (%v, %v), want (false, nil)", affected, patchErr)
	}
	if again := readMessageBlocks(t, env, conversation.ID, "msg_asst_patch1"); again[1]["status"] != "completed" {
		t.Fatalf("0 行补丁不得改写行: %v", again[1])
	}

	// 块缺失（jobId 不在消息里）→ (false, nil)。
	missing := settleChatMediaTaskInput{conversation: conversation, ownerID: "owner-a", jobID: "video_job_missing", block: hit}
	if affected, missingErr := rt.patchMediaTaskBlock(missing, ChatMediaTaskBlockPatch{Status: stringPtr("failed")}); missingErr != nil || affected {
		t.Fatalf("块缺失 = (%v, %v), want (false, nil)", affected, missingErr)
	}

	// 坏 JSON 原文 → error。
	broken := settleChatMediaTaskInput{conversation: conversation, ownerID: "owner-a", jobID: "video_job1", block: &chatMediaTaskBlockHit{
		MessageID: "msg_asst_patch1", BlocksJSON: "{not-json", Block: hit.Block,
	}}
	if _, brokenErr := rt.patchMediaTaskBlock(broken, ChatMediaTaskBlockPatch{Status: stringPtr("failed")}); brokenErr == nil {
		t.Fatal("坏 JSON 原文必须报错")
	}
}

// TestPatchChatMediaTaskBlockSizeCap 钉住 256KiB DomainError（store 层）。
func TestPatchChatMediaTaskBlockSizeCap(t *testing.T) {
	env := newGenerationEnv(t)
	conversation := env.fixture.createConversation("chat_conv_patch2", "owner-a")
	// 直插一条接近 256KiB 上限的消息块（绕过流式链路的写入上限，制造补丁
	// 后越界的最小形态）。
	padding := strings.Repeat("x", 300*1024)
	blocks := fmt.Sprintf(`[{"type":"output_text","blockId":"assistant_block_1","order":1,"text":%q},{"type":"output_media_task","blockId":"assistant_block_2","order":2,"jobId":"video_job1","kind":"video","status":"in_progress","model":"sora-2"}]`, padding)
	if _, err := env.fixture.db.Exec(`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id, sequence_no,
		role, status, content_text, content_blocks_json, content_bytes, storage_reserved_bytes, model, created_at, completed_at, expires_at)
		VALUES ('msg_asst_patch2', ?, 'owner-a', 'turn_v1', 2, 'assistant', 'completed', 'x', ?, ?, 0, 'gpt-5', ?, ?, ?)`,
		conversation.ID, blocks, len(blocks), env.fixture.nowISO, env.fixture.nowISO, env.fixture.nowISO); err != nil {
		t.Fatal(err)
	}
	rt := newChatRoutesForTest(env.deps)
	input := settleChatMediaTaskInput{conversation: conversation, ownerID: "owner-a", jobID: "video_job1", block: &chatMediaTaskBlockHit{
		MessageID: "msg_asst_patch2", TurnID: "turn_v1", BlockID: "assistant_block_2",
		Block: map[string]any{"type": "output_media_task", "jobId": "video_job1"}, BlocksJSON: blocks,
	}}
	affected, err := rt.patchMediaTaskBlock(input, ChatMediaTaskBlockPatch{Status: stringPtr("completed")})
	if err == nil || affected {
		t.Fatalf("256KiB 超限 = (%v, %v), want (false, DomainError)", affected, err)
	}
	domainErr, ok := err.(*DomainError)
	if !ok || !strings.Contains(domainErr.Message, "256 KiB") {
		t.Fatalf("超限错误必须是 256KiB DomainError: %v", err)
	}
}

// --- 路由形态：持久化失败 → 待轮询响应 ---

// newFaultMediaTaskEnv 与 newMediaTaskEnv 等价，但 store 经过 w10d 故障脚本。
func newFaultMediaTaskEnv(t *testing.T, jobStatus string) (*generationEnv, *faultScriptW10D, *int32) {
	t.Helper()
	env, script := newFaultGenerationEnvW10D(t)
	jobs := &fakeMediaJobs{status: jobStatus}
	env.deps.MediaJobs = jobs
	var contentCalls int32
	env.executor.steps = []scriptStep{{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/videos/video_job1/content") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(string(mediaMP4Fixture)))}
		},
	}}
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return call.Path == "/v1/videos/video_job1" }, respond: func(call dispatchCall) *GenerationDispatchResponse {
			return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(`{"id":"video_job1","status":"completed","progress":100}`))}
		}})
	return env, script, &contentCalls
}

// TestMediaTaskSettlementStoreErrorKeepsPolling：块补丁 DB 错误 → HTTP 200，
// 响应无终态字段（status/assetId/error）且带 settlementError；消息块保持未
// 结算原样（前端保持轮询），资产已落库（重试经 digest 查重复用）。
func TestMediaTaskSettlementStoreErrorKeepsPolling(t *testing.T) {
	env, script, _ := newFaultMediaTaskEnv(t, "in_progress")
	conversation := env.fixture.createConversation("chat_conv_settle1", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_settle1", "in_progress")
	script.fail("SET content_blocks_json")

	response := env.do("GET", mediaTaskURL(conversation.ID), "owner-a", "")
	if response.status != http.StatusOK {
		t.Fatalf("持久化失败仍必须 200: %d %s", response.status, response.rawString())
	}
	data := response.dataMap()
	for _, key := range []string{"status", "assetId", "error"} {
		if _, exists := data[key]; exists {
			t.Fatalf("终态字段 %q 必须从响应删除: %v", key, data)
		}
	}
	if settlementErr, _ := data["settlementError"].(string); settlementErr == "" {
		t.Fatalf("必须携带 settlementError: %v", data)
	}
	// 消息块保持未结算（重试路径依赖）。
	blocks := readMessageBlocks(t, env, conversation.ID, "msg_asst_settle1")
	if blocks[1]["status"] != "in_progress" {
		t.Fatalf("补丁失败后块不得改写: %v", blocks[1])
	}
	// 资产已落库：下次轮询经 digest 查重复用，不重复下载。
	var assetCount int
	if err := env.fixture.db.QueryRow(`SELECT COUNT(*) FROM chat_assets WHERE conversation_id = ?`, conversation.ID).Scan(&assetCount); err != nil || assetCount != 1 {
		t.Fatalf("资产应已落库: count=%d err=%v", assetCount, err)
	}
}

// TestMediaTaskSettlementSizeCapKeepsPolling：256KiB 超限（DomainError）走同
// 一待轮形态。
func TestMediaTaskSettlementSizeCapKeepsPolling(t *testing.T) {
	env, _, _ := newFaultMediaTaskEnv(t, "in_progress")
	conversation := env.fixture.createConversation("chat_conv_settle2", "owner-a")
	padding := strings.Repeat("x", 300*1024)
	blocks := fmt.Sprintf(`[{"type":"output_text","blockId":"assistant_block_1","order":1,"text":%q},{"type":"output_media_task","blockId":"assistant_block_2","order":2,"jobId":"video_job1","kind":"video","status":"in_progress","model":"sora-2"}]`, padding)
	if _, err := env.fixture.db.Exec(`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id, sequence_no,
		role, status, content_text, content_blocks_json, content_bytes, storage_reserved_bytes, model, created_at, completed_at, expires_at)
		VALUES ('msg_asst_settle2', ?, 'owner-a', 'turn_v1', 2, 'assistant', 'completed', 'x', ?, ?, 0, 'gpt-5', ?, ?, ?)`,
		conversation.ID, blocks, len(blocks), env.fixture.nowISO, env.fixture.nowISO, env.fixture.nowISO); err != nil {
		t.Fatal(err)
	}

	response := env.do("GET", mediaTaskURL(conversation.ID), "owner-a", "")
	if response.status != http.StatusOK {
		t.Fatalf("超限失败仍必须 200: %d %s", response.status, response.rawString())
	}
	data := response.dataMap()
	for _, key := range []string{"status", "assetId", "error"} {
		if _, exists := data[key]; exists {
			t.Fatalf("终态字段 %q 必须从响应删除: %v", key, data)
		}
	}
	if settlementErr, _ := data["settlementError"].(string); !strings.Contains(settlementErr, "256 KiB") {
		t.Fatalf("settlementError 应透出 256KiB 超限: %v", data)
	}
}

// updateMediaTaskBlock 读取消息块 → 变异任务块 → 原样写回（制造 CAS 0 行的
// 并发写入形态）。
func updateMediaTaskBlock(t *testing.T, env *generationEnv, conversationID, messageID string, mutate func(task map[string]any)) {
	t.Helper()
	blocks := readMessageBlocks(t, env, conversationID, messageID)
	for _, block := range blocks {
		if blockType, _ := block["type"].(string); blockType == "output_media_task" {
			mutate(block)
		}
	}
	updated, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.db.Exec(`UPDATE chat_messages SET content_blocks_json = ? WHERE id = ?`, string(updated), messageID); err != nil {
		t.Fatal(err)
	}
}

// TestSettlementPatchCASRereadEquivalentCompleted：CAS 0 行且重读到并发方已
// 写入的等价 completed 终态（含 assetId）→ 按正常 completed 返回。
func TestSettlementPatchCASRereadEquivalentCompleted(t *testing.T) {
	env := newGenerationEnv(t)
	conversation := env.fixture.createConversation("chat_conv_cas1", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_cas1", "in_progress")

	// 乐观并发原文（读于并发写入前 → 陈旧；BlocksJSON 是存储原文）。
	staleHit, err := env.deps.Store.FindChatMediaTaskBlock(conversation.ID, "owner-a", "video_job1")
	if err != nil || staleHit == nil {
		t.Fatalf("定位任务块: hit=%v err=%v", staleHit, err)
	}
	// 并发方写入等价 completed 终态。
	updateMediaTaskBlock(t, env, conversation.ID, "msg_asst_cas1", func(task map[string]any) {
		task["status"] = "completed"
		task["assetId"] = "asset_prev"
	})

	rt := newChatRoutesForTest(env.deps)
	payload := map[string]any{"jobId": "video_job1", "status": "completed"}
	settled, equivalent := rt.settlePatchOrMarkPending(settleChatMediaTaskInput{
		conversation: conversation, ownerID: "owner-a", jobID: "video_job1", block: staleHit,
	}, payload, ChatMediaTaskBlockPatch{Status: stringPtr("completed"), AssetID: stringPtr("asset_new")})
	if !settled || !equivalent {
		t.Fatalf("重读到等价 completed 终态必须按成功结算返回: settled=%t equivalent=%t", settled, equivalent)
	}
	if payload["status"] != "completed" || payload["assetId"] != "asset_prev" {
		t.Fatalf("payload 应回显并发方终态: %v", payload)
	}
	if _, exists := payload["settlementError"]; exists {
		t.Fatalf("成功结算不得带 settlementError: %v", payload)
	}
}

// TestSettlementPatchCASRereadNoEquivalent：CAS 0 行且重读无等价终态 → 保守
// 保持待轮形态（删除终态字段 + settlementError）。
func TestSettlementPatchCASRereadNoEquivalent(t *testing.T) {
	env := newGenerationEnv(t)
	conversation := env.fixture.createConversation("chat_conv_cas2", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_cas2", "in_progress")

	staleHit, err := env.deps.Store.FindChatMediaTaskBlock(conversation.ID, "owner-a", "video_job1")
	if err != nil || staleHit == nil {
		t.Fatalf("定位任务块: hit=%v err=%v", staleHit, err)
	}
	// 并发方只推进了 progress（failed 等价终态无探针字段，无法证实）。
	updateMediaTaskBlock(t, env, conversation.ID, "msg_asst_cas2", func(task map[string]any) {
		task["progress"] = float64(50)
	})

	rt := newChatRoutesForTest(env.deps)
	payload := map[string]any{"jobId": "video_job1", "status": "failed", "error": "boom"}
	settled, equivalent := rt.settlePatchOrMarkPending(settleChatMediaTaskInput{
		conversation: conversation, ownerID: "owner-a", jobID: "video_job1", block: staleHit,
	}, payload, ChatMediaTaskBlockPatch{Status: stringPtr("failed"), Error: stringPtr("生成失败")})
	if settled || equivalent {
		t.Fatalf("重读无等价终态必须保持待轮形态: settled=%t equivalent=%t", settled, equivalent)
	}
	for _, key := range []string{"status", "assetId", "error"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("终态字段 %q 必须删除: %v", key, payload)
		}
	}
	if settlementErr, _ := payload["settlementError"].(string); settlementErr == "" {
		t.Fatalf("待轮形态必须携带 settlementError: %v", payload)
	}
}

// TestSettlementFailedPatchYieldsToEquivalentCompleted：failed 补丁遇 CAS 0 行
// 且重读到并发等价 completed 终态时，块内事实为准——payload 保持 completed，
// 不得被调用方覆盖为 failed+assetId 混合形态。
func TestSettlementFailedPatchYieldsToEquivalentCompleted(t *testing.T) {
	env := newGenerationEnv(t)
	conversation := env.fixture.createConversation("chat_conv_cas3", "owner-a")
	insertMediaTaskMessage(t, env, conversation.ID, "msg_asst_cas3", "in_progress")

	staleHit, err := env.deps.Store.FindChatMediaTaskBlock(conversation.ID, "owner-a", "video_job1")
	if err != nil || staleHit == nil {
		t.Fatalf("定位任务块: hit=%v err=%v", staleHit, err)
	}
	// 并发方已写 completed 终态。
	updateMediaTaskBlock(t, env, conversation.ID, "msg_asst_cas3", func(task map[string]any) {
		task["status"] = "completed"
		task["assetId"] = "asset_winner"
	})

	rt := newChatRoutesForTest(env.deps)
	payload := map[string]any{"jobId": "video_job1"}
	settled, equivalent := rt.settlePatchOrMarkPending(settleChatMediaTaskInput{
		conversation: conversation, ownerID: "owner-a", jobID: "video_job1", block: staleHit,
	}, payload, ChatMediaTaskBlockPatch{Status: stringPtr("failed"), Error: stringPtr("生成失败")})
	if !settled || !equivalent {
		t.Fatalf("failed 补丁遇等价 completed 必须让位: settled=%t equivalent=%t", settled, equivalent)
	}
	if payload["status"] != "completed" || payload["assetId"] != "asset_winner" {
		t.Fatalf("payload 必须保持并发方 completed 终态: %v", payload)
	}
	if _, exists := payload["error"]; exists {
		t.Fatalf("不得残留 failed error 字段: %v", payload)
	}
}

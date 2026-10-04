package chat

// M7 问答音视频工具任务接口（docs/functions/问答音视频工具设计.md §3/§4，
// 2026-10-04）：GET /my-chat/conversations/{cid}/media-tasks/{jobId}。无后台
// 常驻任务——前端对未终态 output_media_task 块轮询本接口；后端实时查
// media_jobs（经组合根注入的只读端口），未终态经进程内 /v1 任务面 poll 上游
//（任务面亲和：创建时归属 chat API Key），到达终态时幂等结算：
// completed→GET content 下载（64MB 限额）落 chat_assets→更新消息任务块附
// assetId；failed/cancelled/expired→块置终态错误。幂等性由三道闸保证：
// media_jobs 终态唯一性（/v1 链）、分条互斥（同 job 并发请求串行化）与资产
// digest 查重（重复结算复用既有资产行）。
//
// 消息块持久化取舍（裁决）：轮次 finalize 后消息行不可变（finalizeTurn 仅写
// streaming 行且要求活动轮次），不存在「更新已完成消息块」的既有通道。本文件
// 新增最小定点更新（PatchChatMediaTaskBlock）只改 output_media_task 块的任务
// 终态字段（status/assetId/error/progress），不迁移块类型、不改 content_text
// 与其他块、不 bump message_revision（前端经本接口响应就地更新任务块，页面
// 重开由本接口幂等结算回填）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// ChatMediaJobSnapshot 是 media_jobs 行的 chat 侧最小视图（归属校验 + 终态
// 快照；状态推进由进程内 /v1 任务面承担，chat 不写 media_jobs）。
type ChatMediaJobSnapshot struct {
	ID            string
	Kind          string
	Status        string
	UpstreamJobID string
}

// ChatMediaJobsLookup 是 media_jobs 的只读查询端口（组合根用 gatewaymedia.
// MediaJobsRepo 适配注入；nil 让任务接口返回显式错误）。
type ChatMediaJobsLookup interface {
	FindChatMediaJob(jobID, apiKeyID string) (ChatMediaJobSnapshot, bool, error)
}

// ChatMediaTaskBlockPatch 是任务块的定点终态补丁字段（nil = 不改）。
type ChatMediaTaskBlockPatch struct {
	Status   *string
	AssetID  *string
	Error    *string
	Progress *int64
}

// chatMediaTaskBlockHit 是块定位结果（结算资产绑定、块补丁与响应回填共用；
// BlocksJSON 是消息 content_blocks_json 原文，作为乐观并发补丁的 WHERE 守卫）。
type chatMediaTaskBlockHit struct {
	MessageID  string
	TurnID     string
	BlockID    string
	Block      map[string]any
	BlocksJSON string
}

// FindChatMediaTaskBlock 在会话最近的助手消息里定位含指定 jobId 的
// output_media_task 块（jobId 是生成侧写入的精确 JSON 键，LIKE 命中后再解析
// 复核；倒序取最近 50 条助手消息覆盖页面重开场景）。
func (s *Store) FindChatMediaTaskBlock(conversationID, ownerID, jobID string) (*chatMediaTaskBlockHit, error) {
	if jobID == "" || strings.ContainsAny(jobID, "%\r\n\t\x00") {
		// jobID 形如 video_ + hex；含 % 会破坏 LIKE 语义（其余通配符 _ 在
		// 解析后按精确 jobId 复核，不构成误报面）。
		return nil, nil
	}
	rows, err := s.db.Query(s.bind(`SELECT id, turn_id, content_blocks_json FROM `+s.table("chat_messages")+`
		WHERE conversation_id = ? AND system_account_id = ? AND role = 'assistant'
			AND content_blocks_json LIKE ?
		ORDER BY sequence_no DESC
		LIMIT 50`),
		conversationID, ownerID, `%"jobId":"`+jobID+`"%`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var messageID, turnID, blocksJSON string
		if err := rows.Scan(&messageID, &turnID, &blocksJSON); err != nil {
			return nil, err
		}
		var blocks []map[string]any
		if err := json.Unmarshal([]byte(blocksJSON), &blocks); err != nil {
			continue
		}
		for _, block := range blocks {
			blockType, _ := block["type"].(string)
			blockJobID, _ := block["jobId"].(string)
			if blockType != "output_media_task" || blockJobID != jobID {
				continue
			}
			blockID, _ := block["blockId"].(string)
			return &chatMediaTaskBlockHit{MessageID: messageID, TurnID: turnID, BlockID: blockID, Block: block, BlocksJSON: blocksJSON}, nil
		}
	}
	return nil, rows.Err()
}

// PatchChatMediaTaskBlock 定点更新任务块的终态字段：按 raw map 往返（保留块
// 内未知键，不重排块序）；WHERE 携带 blocks 原文做乐观并发守卫（并发结算
// 双写时仅一方生效，另一方按 0 行重读——幂等）。content_bytes 不回补（差异
// < 200B，资产本体已按 quota_bytes 计入资产配额，消息预留余量 448KB 覆盖）。
func (s *Store) PatchChatMediaTaskBlock(conversationID, ownerID, messageID, jobID, originalBlocksJSON string, patch ChatMediaTaskBlockPatch) (bool, error) {
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(originalBlocksJSON), &blocks); err != nil {
		return false, err
	}
	patched := false
	for _, block := range blocks {
		blockType, _ := block["type"].(string)
		blockJobID, _ := block["jobId"].(string)
		if blockType != "output_media_task" || blockJobID != jobID {
			continue
		}
		if patch.Status != nil {
			block["status"] = *patch.Status
		}
		if patch.AssetID != nil {
			block["assetId"] = *patch.AssetID
		}
		if patch.Error != nil {
			block["error"] = *patch.Error
		}
		if patch.Progress != nil {
			block["progress"] = *patch.Progress
		}
		patched = true
	}
	if !patched {
		return false, nil
	}
	updated, err := json.Marshal(blocks)
	if err != nil {
		return false, err
	}
	if len(updated) > maxContentBlocksBytes {
		return false, &DomainError{Message: "消息结构化内容超过 256 KiB 上限"}
	}
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_messages")+`
		SET content_blocks_json = ?
		WHERE id = ? AND conversation_id = ? AND system_account_id = ? AND content_blocks_json = ?`),
		string(updated), messageID, conversationID, ownerID, originalBlocksJSON)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// chatMediaTaskFailedReason 渲染任务失败类终态的块内错误文案。
func chatMediaTaskFailedReason(status, errorCode, errorMessage string) string {
	switch status {
	case "cancelled":
		return "视频生成任务已取消"
	case "expired":
		return "视频生成任务已过期"
	}
	if errorMessage != "" {
		return "视频生成失败：" + errorMessage
	}
	if errorCode != "" {
		return "视频生成失败（" + errorCode + "）"
	}
	return "视频生成失败"
}

// chatMediaJobStatusTerminal 与 gatewaymedia 终态词表一致（completed/failed/
// cancelled/expired）。
func chatMediaJobStatusTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "expired":
		return true
	}
	return false
}

// mediaTaskStatus mirrors GET /my-chat/conversations/{cid}/media-tasks/{jobId}
//（问答音视频工具设计 §3 任务接口）。
func (rt *chatRoutes) mediaTaskStatus(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeMessageCode(w, http.StatusNotFound, "会话不存在", "chat_conversation_not_found")
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	if jobID == "" {
		writeMessageCode(w, http.StatusNotFound, "媒体任务不存在", "chat_media_task_not_found")
		return
	}
	if rt.deps.MediaJobs == nil {
		writeChatRouteError(w, &DomainError{Message: "媒体任务面暂不可用，请稍后重试"})
		return
	}
	apiKey, err := rt.requireOwnedApiKey(derefString(conversation.APIKeyID), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	// 归属校验：media_jobs 行按 id + api_key_id（创建时归属会话 chat Key）；
	// 会话侧再以消息块复核（任务块必须属于本会话，跨会话 jobId 不泄露状态）。
	snapshot, found, err := rt.deps.MediaJobs.FindChatMediaJob(jobID, apiKey.ID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if !found {
		writeMessageCode(w, http.StatusNotFound, "媒体任务不存在", "chat_media_task_not_found")
		return
	}
	block, err := rt.deps.Store.FindChatMediaTaskBlock(conversation.ID, ownerID, jobID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if block == nil {
		writeMessageCode(w, http.StatusNotFound, "媒体任务不存在或不属于当前会话", "chat_media_task_not_found")
		return
	}
	status := snapshot.Status
	progress := chatMediaBlockProgress(block.Block)
	errorCode, errorMessage := "", ""
	// 未终态：经进程内 /v1 任务面 poll 上游（/v1 链推进 media_jobs 状态与终态
	// usage/计费回填，chat 不重复实现）；终态行不再 poll（幂等重开的重复轮询
	// 直接回本地快照，不消耗上游配额）。
	if !chatMediaJobStatusTerminal(status) {
		polled, pollErr := PollChatMediaTask(r.Context(), rt.deps.Executor, jobID, apiKey.Secret, rt.deps.traceID(r))
		if pollErr != nil {
			writeMessageCode(w, http.StatusBadGateway, pollErr.Error(), "chat_media_task_poll_failed")
			return
		}
		status = polled.Status
		if polled.Progress != nil {
			progress = polled.Progress
		}
		errorCode, errorMessage = polled.ErrorCode, polled.ErrorMessage
	}
	payload := rt.settleChatMediaTask(settleChatMediaTaskInput{
		conversation:  conversation,
		ownerID:       ownerID,
		apiKeySecret:  apiKey.Secret,
		traceID:       rt.deps.traceID(r),
		jobID:         jobID,
		status:        status,
		progress:      progress,
		errorCode:     errorCode,
		errorMessage:  errorMessage,
		block:         block,
	})
	setNoStoreHeaders(w)
	writeOK(w, payload)
}

// chatMediaBlockProgress 读取块内即时进度（无则 nil）。
func chatMediaBlockProgress(block map[string]any) *int64 {
	value, ok := numericValue(block["progress"])
	if !ok || value < 0 {
		return nil
	}
	progress := int64(value)
	return &progress
}

// settleChatMediaTaskInput 是一次任务查询的结算上下文。
type settleChatMediaTaskInput struct {
	conversation *Conversation
	ownerID      string
	apiKeySecret string
	traceID      string
	jobID        string
	status       string
	progress     *int64
	errorCode    string
	errorMessage string
	block        *chatMediaTaskBlockHit
}

// settleChatMediaTask 按任务当前态幂等结算并渲染响应负载：
//   - completed：块已有 assetId（先前结算）→ 直接回显；否则分条互斥下下载
//     产物（64MB 限额 + MIME 嗅探）→ digest 查重复用或落新资产（绑定块的
//     turn/message，内容序号取块 order）→ 块补丁（status=completed+assetId）。
//   - failed/cancelled/expired：块补丁置终态错误。
//   - 未终态：回显即时状态（不写块——渐进进度由前端就地更新，持久化块仅在
//     终态结算时定格）。
//
// 下载失败（产物过期/超限/格式无效）把块置 failed 并回显错误（产物时效由
// 上游定，设计 §2.1 不补救）。结算内部错误不静默：payload 携带
// settlementError 透出（下次轮询重试结算）。
func (rt *chatRoutes) settleChatMediaTask(input settleChatMediaTaskInput) map[string]any {
	payload := map[string]any{
		"jobId":  input.jobID,
		"kind":   "video",
		"status": input.status,
		"model":  chatMediaBlockString(input.block.Block, "model"),
	}
	if input.block.BlockID != "" {
		payload["blockId"] = input.block.BlockID
	}
	if input.block.MessageID != "" {
		payload["messageId"] = input.block.MessageID
	}
	if promptSummary := chatMediaBlockString(input.block.Block, "promptSummary"); promptSummary != "" {
		payload["promptSummary"] = promptSummary
	}
	if assetID := chatMediaBlockString(input.block.Block, "assetId"); assetID != "" {
		payload["assetId"] = assetID
	}
	if input.progress != nil {
		payload["progress"] = *input.progress
	}
	if !chatMediaJobStatusTerminal(input.status) {
		return payload
	}
	settledErr := withMediaSettlementLock(input.jobID, func() error {
		// 互斥内重读块：并发方可能已完成结算（重读即最新原文，补丁的乐观
		// 并发守卫以此为准）。
		fresh, err := rt.deps.Store.FindChatMediaTaskBlock(input.conversation.ID, input.ownerID, input.jobID)
		if err != nil {
			return err
		}
		if fresh == nil {
			return nil
		}
		input.block = fresh
		if assetID := chatMediaBlockString(fresh.Block, "assetId"); assetID != "" {
			payload["assetId"] = assetID
			payload["status"] = "completed"
			return nil
		}
		switch input.status {
		case "completed":
			artifact, downloadErr := DownloadChatMediaContent(context.Background(), rt.deps.Executor, input.jobID, input.apiKeySecret, input.traceID)
			if downloadErr != nil {
				rt.patchMediaTaskBlock(input, fresh, ChatMediaTaskBlockPatch{
					Status: stringPtr("failed"), Error: stringPtr(downloadErr.Error()),
				})
				payload["status"] = "failed"
				payload["error"] = downloadErr.Error()
				return nil
			}
			// digest 查重：同 job 重复结算（并发漏网/上游同字节）复用既有资产。
			existing, findErr := rt.deps.Store.FindGeneratedAssetByDigest(input.ownerID, input.conversation.ID, artifact.SHA256)
			if findErr != nil {
				return findErr
			}
			assetID := ""
			if existing != nil {
				assetID = existing.ID
			} else {
				sink := &storeGeneratedMediaSink{
					routes:             rt,
					ownerID:            input.ownerID,
					conversationID:     input.conversation.ID,
					turnID:             fresh.TurnID,
					assistantMessageID: fresh.MessageID,
					nextContentOrder:   func() int64 { return chatMediaBlockOrder(fresh.Block) },
				}
				committed, commitErr := sink.CommitGeneratedMedia(GeneratedMediaCommitInput{
					Artifact:    artifact,
					Kind:        "video",
					Model:       chatMediaBlockString(fresh.Block, "model"),
					Prompt:      chatMediaBlockString(fresh.Block, "promptSummary"),
					SourceJobID: input.jobID,
				})
				if commitErr != nil {
					return commitErr
				}
				assetID = committed.AssetID
			}
			payload["assetId"] = assetID
			hundred := int64(100)
			rt.patchMediaTaskBlock(input, fresh, ChatMediaTaskBlockPatch{
				Status: stringPtr("completed"), AssetID: stringPtr(assetID), Progress: &hundred,
			})
			payload["status"] = "completed"
			payload["progress"] = int64(100)
			return nil
		default:
			reason := chatMediaTaskFailedReason(input.status, input.errorCode, input.errorMessage)
			rt.patchMediaTaskBlock(input, fresh, ChatMediaTaskBlockPatch{
				Status: stringPtr("failed"), Error: stringPtr(reason),
			})
			payload["status"] = "failed"
			payload["error"] = reason
			return nil
		}
	})
	if settledErr != nil {
		payload["settlementError"] = settledErr.Error()
	}
	return payload
}

// patchMediaTaskBlock 应用块补丁（乐观并发 0 行 = 并发方已写入等价终态，按
// 重读语义忽略）。
func (rt *chatRoutes) patchMediaTaskBlock(input settleChatMediaTaskInput, hit *chatMediaTaskBlockHit, patch ChatMediaTaskBlockPatch) {
	_, _ = rt.deps.Store.PatchChatMediaTaskBlock(input.conversation.ID, input.ownerID, hit.MessageID, input.jobID, hit.BlocksJSON, patch)
}

// chatMediaBlockString 读取块内字符串字段。
func chatMediaBlockString(block map[string]any, key string) string {
	if block == nil {
		return ""
	}
	value, _ := block[key].(string)
	return value
}

// chatMediaBlockOrder 读取块 order（资产内容序号；缺失回落 0）。
func chatMediaBlockOrder(block map[string]any) int64 {
	value, ok := numericValue(block["order"])
	if !ok || value < 0 {
		return 0
	}
	return int64(value)
}

package mockdata

// chat 域媒体样本生成器（问答音视频工具设计 §3，M7d 语义的 PG 重写移植）：
// 媒体工具演示会话承载 generate_video / generate_audio 的消息块样本——
// output_media_task 三状态（in_progress / completed / failed）与 output_audio
// 即时播放块，配套可播放的 MP4 与 WAV 资产字节。本文件只包含纯值构造与纯字
// 节生成，不触碰任何存储：chatCodexMessage / chatCodexAsset 的落库由 chat
// 域（domain_chat_codex_modelcheck.go，后续波次交付）执行，media_jobs 的
// artifact data URI（business 域 seedMediaJobs）直接复用这里的字节。
//
// 块字段与 gateway internal/chat/generation_runner.go 的 assistantBlock 投影
// 逐字对齐（blockId/order/status/jobId/kind/model/promptSummary/assetId/error）；
// tool_call 块 item 与 publishApplicationToolEvent 的形状对齐（type=工具名 +
// executionOwner=application + 受理事实字段）。媒体资产行与
// chatassets.CommitChatGeneratedMediaAsset 同形：无宽高（NoDimensions）、
// 无预览变体、original_filename = <资产ID><扩展名>、存储键 variant 为 original。

import "time"

// chatCodexMessage / chatCodexAsset 是 chat 域落库前的一条消息 / 资产值对象。
// 字段语义与 gateway chat 存储列一一对应（juhe_chat.chat_messages /
// chat_assets 的写面投影）；定义在本文件是为了让媒体样本构造与字节生成完全
// 自包含、可独立测试，chat 域实现交付时直接消费这两个类型。
type chatCodexMessage struct {
	ID              string
	Sequence        int
	Role            string
	Status          string
	Text            string
	Blocks          []map[string]any
	Model           string
	TurnID          string
	ClientMessageID string
	Trace           string
	FinishReason    string
	CreatedAt       time.Time
	CompletedAt     time.Time
}

type chatCodexAsset struct {
	ID             string
	SourceKind     string
	Filename       string
	Conversation   string
	Owner          string
	TurnID         string
	MessageID      string
	Bytes          []byte
	MimeType       string
	NoDimensions   bool
	StorageVariant string
	QuotaBytes     int
	CommittedAt    time.Time
	CreatedAt      time.Time
}

// chat 域媒体样本的固定 ID（全部带清理标识前缀，幂等删除按前缀收敛）。
const (
	chatMediaConversationID = "mockdata_chat_conversation_media"
	chatMediaAssetVideoID   = "mockdata_chat_asset_video"
	chatMediaAssetAudioID   = "mockdata_chat_asset_audio"
	// chatMediaVideoModel / chatMediaAudioModel 是块内展示值：真实运行时来自
	// 目录 video / audio_speech 分类（business 域 media_jobs 样本同用 sora-2
	// 基准），块渲染只读字符串，不参与路由。
	chatMediaVideoModel = "sora-2"
	chatMediaAudioModel = "gpt-4o-mini-tts"
	// chatCodexFallbackModel 是媒体演示会话的消息 model 列兜底值：媒体块渲染
	// 不参与路由，只要求非空。
	chatCodexFallbackModel = chatMediaVideoModel
	// chatMediaTaskErrorFailed 与 media_task_routes.chatMediaTaskFailedReason
	// 的「errorCode 非空、message 空」分支同形（视频生成失败（upstream_error））。
	chatMediaTaskErrorFailed = "视频生成失败（upstream_error）"
)

// chatMediaMessages 构造媒体工具演示会话的八轮消息（user/assistant 交替）：
//   - 第 1/2 轮：generate_video tool_call + output_media_task(in_progress, 42%)
//     与 (completed, 挂 MP4 资产)；
//   - 第 3/4 轮：再来一遍的 user 消息 + completed 任务块（挂同一 MP4 资产）；
//   - 第 5/6 轮：failed 任务块（error 文案）；
//   - 第 7/8 轮：generate_audio tool_call + output_audio（挂 WAV 资产）。
//
// jobId 不指向 business 域 media_jobs 样本行（那批行的 api_key 归属与 chat 会话
// Key 不一致，轮询接口会按归属拒绝）：in_progress 样本展示的是块渲染态，浏览器
// 验收关注任务卡 / 进度条 / 播放器，而非轮询结算链路（结算链路走 gateway 自有
// E2E 覆盖）。
func chatMediaMessages(now time.Time) []chatCodexMessage {
	at := func(offset time.Duration) time.Time { return now.Add(offset) }
	videoToolCall := func(callID, jobID string) map[string]any {
		return map[string]any{
			"type": "tool_call", "blockId": "mockdata_chat_block_media_tool_" + callID, "order": 2,
			"callId": "mockdata_chat_call_" + callID, "toolType": "generate_video", "status": "completed",
			"item": map[string]any{
				"type": "generate_video", "executionOwner": "application",
				"jobId": jobID, "status": "queued", "kind": "video",
				"model": chatMediaVideoModel, "promptSummary": CleanupNamePrefix + "两秒纯色背景视频",
			},
		}
	}
	return []chatCodexMessage{
		{
			ID: "mockdata_chat_message_media_1", Sequence: 1, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "生成一段两秒的纯色背景视频",
			Blocks: []map[string]any{{"type": "input_text", "blockId": "mockdata_chat_block_media_user_1", "order": 1,
				"text": CleanupNamePrefix + "生成一段两秒的纯色背景视频"}},
			Model: chatCodexFallbackModel, TurnID: "mockdata_chat_turn_media_1", ClientMessageID: "mockdata_chat_client_media_1",
			CreatedAt: at(-4 * time.Hour), CompletedAt: at(-4 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_media_2", Sequence: 2, Role: "assistant", Status: "completed",
			Text: CleanupNamePrefix + "视频任务已提交，当前进度 42%，完成后可在此消息播放",
			Blocks: []map[string]any{
				{"type": "reasoning", "blockId": "mockdata_chat_block_media_reasoning", "order": 1,
					"text": CleanupNamePrefix + "先确认时长与尺寸，再提交视频任务", "status": "completed"},
				videoToolCall("video_1", "video_mockdata_chat_media_job_1"),
				{"type": "output_media_task", "blockId": "mockdata_chat_block_media_task_1", "order": 3,
					"jobId": "video_mockdata_chat_media_job_1", "kind": "video", "status": "in_progress",
					"progress": 42, "model": chatMediaVideoModel, "promptSummary": CleanupNamePrefix + "两秒纯色背景视频"},
				{"type": "output_text", "blockId": "mockdata_chat_block_media_text_1", "order": 4,
					"text": CleanupNamePrefix + "视频任务已提交，当前进度 42%，完成后可在此消息播放", "status": "completed"},
			},
			Model: chatCodexFallbackModel, TurnID: "mockdata_chat_turn_media_1", Trace: CleanupTracePrefix + "chat-media-1",
			FinishReason: "stop", CreatedAt: at(-4*time.Hour + time.Minute), CompletedAt: at(-4*time.Hour + 2*time.Minute),
		},
		{
			ID: "mockdata_chat_message_media_3", Sequence: 3, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "再来一段一样的，看完成态播放",
			Blocks: []map[string]any{{"type": "input_text", "blockId": "mockdata_chat_block_media_user_2", "order": 1,
				"text": CleanupNamePrefix + "再来一段一样的，看完成态播放"}},
			Model: chatCodexFallbackModel, TurnID: "mockdata_chat_turn_media_2", ClientMessageID: "mockdata_chat_client_media_2",
			CreatedAt: at(-3 * time.Hour), CompletedAt: at(-3 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_media_4", Sequence: 4, Role: "assistant", Status: "completed",
			Text: CleanupNamePrefix + "视频已生成完毕，可直接播放",
			Blocks: []map[string]any{
				videoToolCall("video_2", "video_mockdata_chat_media_job_2"),
				{"type": "output_media_task", "blockId": "mockdata_chat_block_media_task_2", "order": 2,
					"jobId": "video_mockdata_chat_media_job_2", "kind": "video", "status": "completed",
					"progress": 100, "model": chatMediaVideoModel, "promptSummary": CleanupNamePrefix + "两秒纯色背景视频",
					"assetId": chatMediaAssetVideoID},
				{"type": "output_text", "blockId": "mockdata_chat_block_media_text_2", "order": 3,
					"text": CleanupNamePrefix + "视频已生成完毕，可直接播放", "status": "completed"},
			},
			Model: chatCodexFallbackModel, TurnID: "mockdata_chat_turn_media_2", Trace: CleanupTracePrefix + "chat-media-2",
			FinishReason: "stop", CreatedAt: at(-3*time.Hour + time.Minute), CompletedAt: at(-3*time.Hour + 90*time.Second),
		},
		{
			ID: "mockdata_chat_message_media_5", Sequence: 5, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "演示一次失败的视频任务",
			Blocks: []map[string]any{{"type": "input_text", "blockId": "mockdata_chat_block_media_user_3", "order": 1,
				"text": CleanupNamePrefix + "演示一次失败的视频任务"}},
			Model: chatCodexFallbackModel, TurnID: "mockdata_chat_turn_media_3", ClientMessageID: "mockdata_chat_client_media_3",
			CreatedAt: at(-2 * time.Hour), CompletedAt: at(-2 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_media_6", Sequence: 6, Role: "assistant", Status: "completed",
			Text: CleanupNamePrefix + "该任务演示失败态：上游返回错误",
			Blocks: []map[string]any{
				videoToolCall("video_3", "video_mockdata_chat_media_job_3"),
				{"type": "output_media_task", "blockId": "mockdata_chat_block_media_task_3", "order": 2,
					"jobId": "video_mockdata_chat_media_job_3", "kind": "video", "status": "failed",
					"model": chatMediaVideoModel, "promptSummary": CleanupNamePrefix + "两秒纯色背景视频",
					"error": chatMediaTaskErrorFailed},
				{"type": "output_text", "blockId": "mockdata_chat_block_media_text_3", "order": 3,
					"text": CleanupNamePrefix + "该任务演示失败态：上游返回错误", "status": "completed"},
			},
			Model: chatCodexFallbackModel, TurnID: "mockdata_chat_turn_media_3", Trace: CleanupTracePrefix + "chat-media-3",
			FinishReason: "stop", CreatedAt: at(-2*time.Hour + time.Minute), CompletedAt: at(-2*time.Hour + 90*time.Second),
		},
		{
			ID: "mockdata_chat_message_media_7", Sequence: 7, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "把这句话合成语音：媒体工具演示",
			Blocks: []map[string]any{{"type": "input_text", "blockId": "mockdata_chat_block_media_user_4", "order": 1,
				"text": CleanupNamePrefix + "把这句话合成语音：媒体工具演示"}},
			Model: chatCodexFallbackModel, TurnID: "mockdata_chat_turn_media_4", ClientMessageID: "mockdata_chat_client_media_4",
			CreatedAt: at(-30 * time.Minute), CompletedAt: at(-30 * time.Minute),
		},
		{
			ID: "mockdata_chat_message_media_8", Sequence: 8, Role: "assistant", Status: "completed",
			Text: CleanupNamePrefix + "语音已合成，可直接播放",
			Blocks: []map[string]any{
				{"type": "tool_call", "blockId": "mockdata_chat_block_media_tool_audio", "order": 1,
					"callId": "mockdata_chat_call_audio_1", "toolType": "generate_audio", "status": "completed",
					"item": map[string]any{
						"type": "generate_audio", "executionOwner": "application",
						"assetId": chatMediaAssetAudioID, "mimeType": "audio/wav", "bytes": chatCodexWAVBytes,
						"model": chatMediaAudioModel, "kind": "audio",
					}},
				{"type": "output_audio", "blockId": "mockdata_chat_block_media_audio", "order": 2,
					"assetId": chatMediaAssetAudioID, "mimeType": "audio/wav", "status": "completed"},
				{"type": "output_text", "blockId": "mockdata_chat_block_media_text_4", "order": 3,
					"text": CleanupNamePrefix + "语音已合成，可直接播放", "status": "completed"},
			},
			Model: chatCodexFallbackModel, TurnID: "mockdata_chat_turn_media_4", Trace: CleanupTracePrefix + "chat-media-4",
			FinishReason: "stop", CreatedAt: at(-30*time.Minute + 10*time.Second), CompletedAt: at(-29 * time.Minute),
		},
	}
}

// chatMediaAssets 构造媒体资产样本（媒体行无宽高、无预览，存储键 variant 为
// original——与 generation_media.go 的真实落库路径一致）。
func chatMediaAssets(conversationID, ownerID string, now time.Time) []chatCodexAsset {
	return []chatCodexAsset{
		{
			ID: chatMediaAssetVideoID, SourceKind: "assistant_generated",
			Filename:     chatMediaAssetVideoID + ".mp4",
			Conversation: conversationID, Owner: ownerID, TurnID: "mockdata_chat_turn_media_2",
			MessageID: "mockdata_chat_message_media_4", Bytes: chatCodexMP4(), MimeType: "video/mp4",
			NoDimensions: true, StorageVariant: "original",
			QuotaBytes:  len(chatCodexMP4Bytes),
			CommittedAt: now.Add(-3*time.Hour + 90*time.Second), CreatedAt: now.Add(-3 * time.Hour),
		},
		{
			ID: chatMediaAssetAudioID, SourceKind: "assistant_generated",
			Filename:     chatMediaAssetAudioID + ".wav",
			Conversation: conversationID, Owner: ownerID, TurnID: "mockdata_chat_turn_media_4",
			MessageID: "mockdata_chat_message_media_8", Bytes: chatCodexWAV(), MimeType: "audio/wav",
			NoDimensions: true, StorageVariant: "original",
			QuotaBytes:  chatCodexWAVBytes,
			CommittedAt: now.Add(-29 * time.Minute), CreatedAt: now.Add(-30 * time.Minute),
		},
	}
}

// chatCodexWAVBytes 是 1 秒静音 PCM WAV 的字节数（44 字节 RIFF 头 + 8000 帧
// 16bit mono 采样），与 chatCodexWAV 的输出恒等。
const chatCodexWAVBytes = 44 + 8000*2

// chatCodexWAV 生成合法 RIFF WAV（PCM 16bit mono 8kHz，1 秒静音，16044 字节）：
// 浏览器 <audio> 只需容器与 fmt 块正确即可解码播放，静音数据满足「可播放且不
// 含内容」的造数要求。
func chatCodexWAV() []byte {
	const sampleRate = 8000
	const dataBytes = sampleRate * 2 // 1 秒 * 16bit mono
	file := []byte{'R', 'I', 'F', 'F'}
	file = appendLittleEndian(file, uint32(36+dataBytes))
	file = append(file, 'W', 'A', 'V', 'E', 'f', 'm', 't', ' ')
	file = appendLittleEndian(file, 16)           // fmt 块固定 16 字节（PCM 无扩展区）
	file = appendUint16(file, 1)                  // audio format = 1（PCM）
	file = appendUint16(file, 1)                  // 声道数 = 1（mono）
	file = appendLittleEndian(file, sampleRate)   // 采样率
	file = appendLittleEndian(file, sampleRate*2) // byte rate = 采样率 * 块对齐
	file = appendUint16(file, 2)                  // block align = 2（16bit mono）
	file = appendUint16(file, 16)                 // 位深 = 16
	file = append(file, 'd', 'a', 't', 'a')
	file = appendLittleEndian(file, dataBytes)
	return append(file, make([]byte, dataBytes)...)
}

// appendLittleEndian 以小端序追加 32 位字段（RIFF 尺寸、采样率等）。
func appendLittleEndian(target []byte, value uint32) []byte {
	return append(target, byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
}

// appendUint16 以小端序追加 16 位字段；RIFF 头里的 format/channels/block align/
// 位深都是 16 位宽，误用 4 字节 appendLittleEndian 会把头部撑错位（浏览器拒解）。
func appendUint16(target []byte, value uint16) []byte {
	return append(target, byte(value), byte(value>>8))
}

// chatCodexMP4 返回可播放 MP4 样本（chatCodexMP4Bytes 的防御性拷贝：调用方
// 拿到的是独立切片，追加或改写不会污染常量）。来源与生成命令见
// mp4_sample_bytes.go 文件头。
func chatCodexMP4() []byte {
	out := make([]byte, len(chatCodexMP4Bytes))
	copy(out, chatCodexMP4Bytes)
	return out
}

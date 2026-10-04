package chat

// M7 问答音视频工具执行链（docs/functions/问答音视频工具设计.md §4，2026-10-04）：
// generate_video（异步任务：POST /v1/videos 创建后立即完成工具调用）与
// generate_audio（同步 TTS：POST /v1/audio/speech，响应音频字节按 MIME 嗅探落
// chat_assets）。子调用一律经进程内 /v1 链（GenerationExecutor 固定派发绑定
// 账户；视频轮询/下载走任务面亲和——创建时归属 chat API Key，媒体任务行
// api_key_id 校验天然成立），purpose 头 chat_media_generation 供审计区分。
// 消息块投影（output_audio / output_media_task）与任务结算接口见
// stream_execute.go 与 media_task_routes.go。

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 媒体产物限额（设计 §2.1：视频 ≤64MB、音频 ≤32MB，超限失败不落资产）。
const (
	chatMediaAudioMaxBytes = 32 * 1024 * 1024
	chatMediaVideoMaxBytes = 64 * 1024 * 1024
	// chatMediaPurposeHeader 是媒体工具子调用的审计 purpose 头（设计 §2.4）。
	chatMediaPurpose = "chat_media_generation"
)

// ChatAudioGenerationRequest 是 generate_audio 的归一参数（TTS 契约：text 必填，
// voice/format/model 可选）。
type ChatAudioGenerationRequest struct {
	Model  string
	Text   string
	Voice  string
	Format string
}

// ChatVideoCreationRequest 是 generate_video 的归一参数（seconds/size 可选，
// 上游参数边界由 /v1 视频链的参数归一层裁定）。
type ChatVideoCreationRequest struct {
	Model   string
	Prompt  string
	Seconds *float64
	Size    string
}

// ChatVideoCreationResult 是 POST /v1/videos 的受理结果：对外 jobId + 任务初始
// 状态（创建即返回，不等待生成完成，设计 §2.2）。
type ChatVideoCreationResult struct {
	JobID    string
	Status   string
	Progress *int64
	Model    string
	Prompt   string
}

// ChatMediaArtifactResult 是媒体产物字节（同步音频或终态结算下载的视频）：
// MIME 由字节嗅探得出（不信任上游 Content-Type）。
type ChatMediaArtifactResult struct {
	Data     []byte
	Bytes    int64
	SHA256   string
	MimeType string
}

// ChatMediaTaskPollResult 是 GET /v1/videos/{id} 的任务面快照（进程内链实时
// 查询并推进 media_jobs 状态）。
type ChatMediaTaskPollResult struct {
	JobID    string
	Status   string // queued|in_progress|completed|failed|cancelled|expired
	Progress *int64
	ErrorCode    string
	ErrorMessage string
}

// chatMediaArtifactOf 把已下载产物归一为媒体结果（digest + MIME 嗅探）。
func chatMediaArtifactOf(data []byte) (ChatMediaArtifactResult, error) {
	if len(data) == 0 {
		return ChatMediaArtifactResult{}, errors.New("媒体产物内容为空")
	}
	digest := sha256.Sum256(data)
	mimeType := chatMediaMimeTypeFromBytes(data)
	if mimeType == "" {
		return ChatMediaArtifactResult{}, errors.New("生成媒体缺少可识别的音频/视频格式")
	}
	return ChatMediaArtifactResult{
		Data:     data,
		Bytes:    int64(len(data)),
		SHA256:   hexEncode(digest[:]),
		MimeType: mimeType,
	}, nil
}

// GenerateChatAudio 经进程内 /v1/audio/speech 同步合成音频（设计 §4.3）：响应
// 是音频字节本身（LimitReader 32MB 限额，超限失败不落资产）。
func GenerateChatAudio(ctx requestContext, executor GenerationExecutor, input ChatAudioGenerationRequest, apiKey, traceID string) (ChatMediaArtifactResult, error) {
	model := trimSpace(input.Model)
	text := trimSpace(input.Text)
	if model == "" {
		return ChatMediaArtifactResult{}, errors.New("语音合成模型不能为空")
	}
	if text == "" {
		return ChatMediaArtifactResult{}, errors.New("语音合成文本不能为空")
	}
	voice := trimSpace(input.Voice)
	audioFormat := normalizeChatAudioFormat(input.Format)
	payloadValues := map[string]any{"model": model, "input": text}
	if voice != "" {
		payloadValues["voice"] = voice
	}
	if audioFormat != "" {
		payloadValues["response_format"] = audioFormat
	}
	payload, _ := json.Marshal(payloadValues)
	headers := chatMediaDispatchHeaders(apiKey, traceID)
	response, err := executor.Dispatch(ctx, GenerationDispatchRequest{
		Path: "/v1/audio/speech", Method: http.MethodPost, Headers: headers, Body: payload,
	})
	if err != nil {
		return ChatMediaArtifactResult{}, err
	}
	if response == nil || response.Body == nil {
		return ChatMediaArtifactResult{}, errors.New("语音合成请求失败")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, chatMediaAudioMaxBytes+1))
	_ = response.Body.Close()
	if readErr != nil {
		return ChatMediaArtifactResult{}, readErr
	}
	if status := statusOrZero(response); status < 200 || status >= 300 {
		fallback := fmt.Sprintf("语音合成请求失败（HTTP %d）", status)
		return ChatMediaArtifactResult{}, errors.New(upstreamMessagePayload(string(data), fallback))
	}
	if int64(len(data)) > chatMediaAudioMaxBytes {
		return ChatMediaArtifactResult{}, errors.New("生成音频超过 32 MiB 上限")
	}
	// 只接受嗅探出的音频 MIME：上游把 JSON 错误体带回 2xx 或返回视频/图片字节
	// 均按格式不受支持失败。
	if chatMediaMimeTypeFromBytes(data) == "" || !chatMimeTypeIsAudio(chatMediaMimeTypeFromBytes(data)) {
		return ChatMediaArtifactResult{}, errors.New("语音合成响应缺少有效的音频格式")
	}
	return chatMediaArtifactOf(data)
}

// normalizeChatAudioFormat 归一 TTS response_format（空 = 上游默认 mp3；仅放行
// 嗅探器可识别的格式，避免主模型指定无法回验的封装）。
func normalizeChatAudioFormat(value any) string {
	if value == nil {
		return ""
	}
	normalized := strings.ToLower(trimSpace(fmt.Sprint(value)))
	switch normalized {
	case "", "mp3":
		return "mp3"
	case "wav", "pcm":
		return "wav"
	case "ogg":
		return "ogg"
	case "m4a":
		return "m4a"
	}
	return ""
}

// CreateChatVideo 经进程内 POST /v1/videos 创建异步视频任务（设计 §4.3）：
// 2xx + job id = 受理确立（/v1 链已落 media_jobs 并归一状态），立即返回。
func CreateChatVideo(ctx requestContext, executor GenerationExecutor, input ChatVideoCreationRequest, apiKey, traceID string) (ChatVideoCreationResult, error) {
	model := trimSpace(input.Model)
	prompt := trimSpace(input.Prompt)
	if model == "" {
		return ChatVideoCreationResult{}, errors.New("视频生成模型不能为空")
	}
	if prompt == "" {
		return ChatVideoCreationResult{}, errors.New("视频生成提示词不能为空")
	}
	payloadValues := map[string]any{"model": model, "prompt": prompt}
	if input.Seconds != nil && *input.Seconds > 0 {
		payloadValues["seconds"] = *input.Seconds
	}
	if size := trimSpace(input.Size); size != "" {
		payloadValues["size"] = size
	}
	payload, _ := json.Marshal(payloadValues)
	response, err := executor.Dispatch(ctx, GenerationDispatchRequest{
		Path: "/v1/videos", Method: http.MethodPost, Headers: chatMediaDispatchHeaders(apiKey, traceID), Body: payload,
	})
	if err != nil {
		return ChatVideoCreationResult{}, err
	}
	if response == nil || response.Body == nil {
		return ChatVideoCreationResult{}, errors.New("视频生成请求失败")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	_ = response.Body.Close()
	if readErr != nil {
		return ChatVideoCreationResult{}, readErr
	}
	if status := statusOrZero(response); status < 200 || status >= 300 {
		fallback := fmt.Sprintf("视频生成请求失败（HTTP %d）", status)
		return ChatVideoCreationResult{}, errors.New(upstreamMessagePayload(string(body), fallback))
	}
	var job struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Progress *int64 `json:"progress"`
	}
	if err := json.Unmarshal(body, &job); err != nil || trimSpace(job.ID) == "" {
		return ChatVideoCreationResult{}, errors.New("视频生成响应缺少任务凭据（job id）")
	}
	return ChatVideoCreationResult{
		JobID:    trimSpace(job.ID),
		Status:   trimSpace(job.Status),
		Progress: job.Progress,
		Model:    model,
		Prompt:   prompt,
	}, nil
}

// PollChatMediaTask 经进程内 GET /v1/videos/{jobId} 轮询任务（任务面亲和：
// media_jobs 行按 id + api_key_id 归属校验，创建时归属 chat API Key；未终态行
// 由 /v1 链实时查上游并推进本地状态，终态行直接回本地对象）。
func PollChatMediaTask(ctx requestContext, executor GenerationExecutor, jobID, apiKey, traceID string) (ChatMediaTaskPollResult, error) {
	jobID = trimSpace(jobID)
	if jobID == "" {
		return ChatMediaTaskPollResult{}, errors.New("媒体任务 ID 不能为空")
	}
	response, err := executor.Dispatch(ctx, GenerationDispatchRequest{
		Path: "/v1/videos/" + jobID, Method: http.MethodGet, Headers: chatMediaDispatchHeaders(apiKey, traceID),
	})
	if err != nil {
		return ChatMediaTaskPollResult{}, err
	}
	if response == nil || response.Body == nil {
		return ChatMediaTaskPollResult{}, errors.New("媒体任务查询失败")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	_ = response.Body.Close()
	if readErr != nil {
		return ChatMediaTaskPollResult{}, readErr
	}
	if status := statusOrZero(response); status < 200 || status >= 300 {
		if status == http.StatusNotFound {
			return ChatMediaTaskPollResult{}, errors.New("媒体任务不存在或不属于当前会话")
		}
		fallback := fmt.Sprintf("媒体任务查询失败（HTTP %d）", status)
		return ChatMediaTaskPollResult{}, errors.New(upstreamMessagePayload(string(body), fallback))
	}
	var job struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Progress *int64 `json:"progress"`
		Error    *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &job); err != nil || trimSpace(job.ID) == "" {
		return ChatMediaTaskPollResult{}, errors.New("媒体任务响应解析失败")
	}
	out := ChatMediaTaskPollResult{JobID: trimSpace(job.ID), Status: trimSpace(job.Status), Progress: job.Progress}
	if job.Error != nil {
		out.ErrorCode = trimSpace(job.Error.Code)
		out.ErrorMessage = trimSpace(job.Error.Message)
	}
	return out, nil
}

// DownloadChatMediaContent 经进程内 GET /v1/videos/{jobId}/content 下载终态产物
//（任务面亲和；completed 才可下载，上游 404/410 已由 /v1 链归一）。限额 64MB，
// 超限失败不落资产（设计 §2.1）。
func DownloadChatMediaContent(ctx requestContext, executor GenerationExecutor, jobID, apiKey, traceID string) (ChatMediaArtifactResult, error) {
	jobID = trimSpace(jobID)
	if jobID == "" {
		return ChatMediaArtifactResult{}, errors.New("媒体任务 ID 不能为空")
	}
	response, err := executor.Dispatch(ctx, GenerationDispatchRequest{
		Path: "/v1/videos/" + jobID + "/content", Method: http.MethodGet, Headers: chatMediaDispatchHeaders(apiKey, traceID),
	})
	if err != nil {
		return ChatMediaArtifactResult{}, err
	}
	if response == nil || response.Body == nil {
		return ChatMediaArtifactResult{}, errors.New("媒体产物下载失败")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, chatMediaVideoMaxBytes+1))
	_ = response.Body.Close()
	if readErr != nil {
		return ChatMediaArtifactResult{}, readErr
	}
	if status := statusOrZero(response); status < 200 || status >= 300 {
		fallback := fmt.Sprintf("媒体产物下载失败（HTTP %d）", status)
		return ChatMediaArtifactResult{}, errors.New(upstreamMessagePayload(string(data), fallback))
	}
	if int64(len(data)) > chatMediaVideoMaxBytes {
		return ChatMediaArtifactResult{}, errors.New("生成视频超过 64 MiB 上限")
	}
	artifact, err := chatMediaArtifactOf(data)
	if err != nil {
		return ChatMediaArtifactResult{}, err
	}
	if !chatMimeTypeIsVideo(artifact.MimeType) {
		return ChatMediaArtifactResult{}, errors.New("视频任务产物缺少有效的视频格式")
	}
	return artifact, nil
}

// chatMediaDispatchHeaders 组装媒体子调用的公共头（Bearer chat Key + purpose
// 审计 + 可选 trace）。
func chatMediaDispatchHeaders(apiKey, traceID string) map[string]string {
	headers := map[string]string{
		"authorization": "Bearer " + apiKey,
		"accept":        "application/json",
		"x-purpose":     chatMediaPurpose,
	}
	if traceID != "" {
		headers["x-trace-id"] = traceID
	}
	return headers
}

// --- MIME 嗅探（设计 §3：产物落库前按字节嗅探，不信任上游声明） ---

// chatMediaMimeTypeFromBytes 按魔数嗅探音频/视频 MIME；未知格式返回空串。
// 词表与 chat_assets processed_mime_type CHECK 扩展一致（audio/mpeg、
// audio/wav、audio/ogg、audio/mp4、video/mp4、video/webm）。
func chatMediaMimeTypeFromBytes(data []byte) string {
	switch {
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return "audio/wav"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS")):
		return "audio/ogg"
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")):
		return "audio/mpeg"
	case len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return "audio/mpeg"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return "video/webm"
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")):
		brand := string(data[8:12])
		if brand == "M4A " || brand == "M4B " {
			return "audio/mp4"
		}
		return "video/mp4"
	}
	return ""
}

func chatMimeTypeIsAudio(mimeType string) bool {
	return strings.HasPrefix(mimeType, "audio/")
}

func chatMimeTypeIsVideo(mimeType string) bool {
	return strings.HasPrefix(mimeType, "video/")
}

// chatMimeTypeIsGeneratedMedia 报告 MIME 是否属于问答媒体工具产物词表。
func chatMimeTypeIsGeneratedMedia(mimeType string) bool {
	switch mimeType {
	case "audio/mpeg", "audio/wav", "audio/ogg", "audio/mp4", "video/mp4", "video/webm":
		return true
	}
	return false
}

// --- 媒体资产接收器（沿 storeGeneratedImageSink 模式，设计 §2.1/§4.3） ---

// GeneratedMediaCommitInput 是媒体产物落 chat_assets 的输入（original 对象即
// 唯一对象：媒体无预览变体）。
type GeneratedMediaCommitInput struct {
	Artifact    ChatMediaArtifactResult
	Kind        string // audio|video（谱系用途：资产命名与排查）
	Model       string
	Prompt      string
	SourceJobID string // 视频：产物来源任务（音频为空）
}

// GeneratedMediaCommitResult 是媒体资产落库结果。
type GeneratedMediaCommitResult struct {
	AssetID  string
	MimeType string
	Bytes    int64
}

// ChatMediaArtifactSink 是媒体工具产物落资产的端口（组装侧注入，沿
// ChatGeneratedImageArtifactSink 模式）。
type ChatMediaArtifactSink interface {
	CommitGeneratedMedia(input GeneratedMediaCommitInput) (GeneratedMediaCommitResult, error)
}

// storeGeneratedMediaSink 经 ObjectStore + Store 把媒体产物落 chat_assets。
type storeGeneratedMediaSink struct {
	routes             *chatRoutes
	ownerID            string
	conversationID     string
	turnID             string
	assistantMessageID string
	nextContentOrder   func() int64
}

// CommitGeneratedMedia 写 original 对象（无预览变体）并提交资产行
//（source_kind=assistant_generated，媒体行无宽高/预览——schema M7 约束已放宽）。
func (s *storeGeneratedMediaSink) CommitGeneratedMedia(input GeneratedMediaCommitInput) (GeneratedMediaCommitResult, error) {
	result := GeneratedMediaCommitResult{}
	if s.routes.deps.ObjectStore == nil || s.routes.deps.Store == nil {
		return result, errors.New("媒体工具运行时未配置资产存储")
	}
	if !chatMimeTypeIsGeneratedMedia(input.Artifact.MimeType) {
		return result, errors.New("媒体产物 MIME 不在词表内")
	}
	maxBytes := int64(chatMediaAudioMaxBytes)
	if chatMimeTypeIsVideo(input.Artifact.MimeType) {
		maxBytes = chatMediaVideoMaxBytes
	}
	assetID := s.routes.deps.Store.newID("asset")
	originalKey := StorageKeyForChatAsset(assetID, input.Artifact.SHA256, input.Artifact.MimeType, "original")
	if err := s.routes.deps.ObjectStore.Write(originalKey, input.Artifact.Data, maxBytes, input.Artifact.SHA256); err != nil {
		return result, err
	}
	asset, err := s.routes.deps.Store.CommitChatGeneratedMediaAsset(MediaAssetCommitInput{
		ID:            assetID,
		SystemAccountID: s.ownerID,
		ConversationID: s.conversationID,
		TurnID:        s.turnID,
		MessageID:     s.assistantMessageID,
		ContentOrder:  s.nextContentOrder(),
		MimeType:      input.Artifact.MimeType,
		Bytes:         input.Artifact.Bytes,
		Sha256:        input.Artifact.SHA256,
		StorageKey:    originalKey,
		Kind:          input.Kind,
		Model:         input.Model,
		Prompt:        input.Prompt,
		SourceJobID:   input.SourceJobID,
		Now:           s.routes.now(),
		RetentionDays: s.routes.deps.RetentionDays,
	})
	if err != nil {
		_ = s.routes.deps.ObjectStore.Delete([]string{originalKey})
		return result, err
	}
	return GeneratedMediaCommitResult{AssetID: asset.ID, MimeType: derefString(asset.ProcessedMimeType), Bytes: derefAssistantI64(asset.ProcessedBytes)}, nil
}

// chatMediaPromptSummary 生成任务块的 prompt 摘要（截断到 80 字符）。
func chatMediaPromptSummary(prompt string) string {
	trimmed := trimSpace(prompt)
	runes := []rune(trimmed)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return trimmed
}

// mediaSettlementLocks 是按 jobId 分条的结算互斥（单进程内并发轮询请求的结算
// 去重第一道闸；第二道是资产 digest 查重，见 media_task_routes.go）。
var mediaSettlementLocks [64]chan struct{}

func init() {
	for index := range mediaSettlementLocks {
		mediaSettlementLocks[index] = make(chan struct{}, 1)
	}
}

// lockMediaSettlement 返回 jobId 对应的分条互斥通道（stripe = FNV-1a 低 6 位）。
func lockMediaSettlement(jobID string) chan struct{} {
	var hash uint32
	for _, char := range []byte(jobID) {
		hash ^= uint32(char)
		hash *= 16777619
	}
	return mediaSettlementLocks[hash%uint32(len(mediaSettlementLocks))]
}

// withMediaSettlementLock 在分条互斥下执行 fn（并发两请求同 job 结算串行化）。
func withMediaSettlementLock(jobID string, fn func() error) error {
	lock := lockMediaSettlement(jobID)
	lock <- struct{}{}
	defer func() { <-lock }()
	return fn()
}

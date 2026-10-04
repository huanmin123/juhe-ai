// qwen_asr.go 是 M3f 长音频异步转写的出站 adapter 端口与通义百炼
// （paraformer 系）首个实现（媒体设计 §4.2/§5：对外 /v1/audio/jobs 统一 job
// 语义 → 媒体 IR → 每厂商一个 adapter，注册表组织；本文件挂
// audioJobAdapters 的 "qwen" 条目）。报文依据契约 §10.2（B 级，2026-10-04
// 回填）：创建 POST /api/v1/services/audio/asr/transcription，Bearer
// DASHSCOPE_API_KEY 认证，**无需 X-DashScope-Async 头**（该服务天然异步，
// 与万相视频不同），请求 {"model":"paraformer-v2","input":{"file_urls":
// ["<公网音频 URL>"]},"parameters":{"language_hints":[...],
// "diarization_enabled":true,...}}——只接受公网 URL 数组不支持文件上传
// （与零存储对齐：对外 /v1/audio/jobs 首版输入只支持 input_url）；响应
// {"output":{"task_id","task_status":"PENDING"},"request_id"}——受理凭据 =
// output.task_id；轮询 GET /api/v1/tasks/{task_id}（与万相同一 DashScope
// 任务接口），SUCCEEDED → output.results[].transcription_url（**转写结果
// JSON 文件**的下载链接，产物为 JSON 非 audio），FAILED → output.message，
// usage（JSON 字符串，时长计量）。出站路径自带 /api/v1 服务根，URL 由链
// 上层经 chainQwenVideoUpstreamURL 归一（/api/v1 去重，契约 §10.1 先例，
// 该函数同时服务 §10.2 长转写面）。纯逻辑层，不触 chain/HTTP 执行。
package gatewaymedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// AudioJobProviderAdapter 是长音频异步转写（对外 /v1/audio/jobs，kind=
// audio_transcription）的出站 adapter 端口。方法集与 VideoProviderAdapter
// 的任务面半部结构同构（Provider/Build*/ParsePollResponse/ContentFromArtifact/
// SupportsCancel），链上层经二者共同满足的任务面接口按 kind 泛化消费（媒体
// 设计 §5：注册表挂载，不进组合根 switch；M3f 起新增供应商只注册新条目）。
// ctx 保留在 Create 签名中供链上层注入取消与追踪，纯构造实现不消费。
type AudioJobProviderAdapter interface {
	// Provider 返回注册键（provider_code 归一小写）。
	Provider() string
	// Capabilities 声明该厂商长转写请求面能力（参数归一与 ignored 计算
	// 的裁决输入）。
	Capabilities() AudioJobCapabilities
	// Create 把归一参数 + provider_options（对象形态，键=provider_code）
	// 映射为上游创建请求。错误为请求侧 400 语义（能力边界裁决）。
	Create(ctx context.Context, in AudioJobCreateInput) (AudioJobCreateOutput, error)
	// BuildPollRequest 构造轮询请求（GET 任务查询族）。upstreamJobID 由
	// 调用方保证非空。
	BuildPollRequest(upstreamJobID string) (method, path string, body []byte)
	// ParsePollResponse 把上游轮询/创建响应（状态码 + body）归一为
	// MediaJobIR（Kind=audio_transcription）。非 2xx 返回
	// *UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）。
	ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error)
	// BuildContentRequest 构造产物下载请求（ContentFromArtifact=true 时
	// 链上层不调用）。
	BuildContentRequest(upstreamJobID string) (method, path string)
	// ContentFromArtifact 声明产物下载定位来源：true 表示 completed 的
	// 下载地址是轮询冻结的绝对 Artifact.ContentURL（paraformer 的
	// transcription_url 结果 JSON 文件链接），链上层直连该 URL 且不携带
	// 账户认证，不调用 BuildContentRequest。
	ContentFromArtifact() bool
	// BuildCancelRequest 构造取消/删除请求（SupportsCancel=false 时链上层
	// 不调用，直接本地收敛 cancelled）。
	BuildCancelRequest(upstreamJobID string) (method, path string)
	// SupportsCancel 声明上游是否暴露任务取消端点：false（qwen 契约 §10.2
	// 面无取消 API 回填）时链上层不发上游请求、直接本地收敛 cancelled
	//（§2.6 cancelled 本地终态语义，沿 glm/minimax/volcengine/万相裁决）。
	SupportsCancel() bool
}

// AudioJobCapabilities 是长转写 adapter 的能力声明（对外 /v1/audio/jobs
// L1/L2 参数面的裁决输入，契约 §2.4 忽略/拒绝/回显规则）。
type AudioJobCapabilities struct {
	// SupportsLanguage：language 参数是否有上游对应字段（paraformer 的
	// parameters.language_hints 原生数组字段）→ true；无对应字段的厂商
	// 忽略 + 回显 params_ignored。
	SupportsLanguage bool
}

// AudioJobCreateInput 是创建请求的输入：归一参数 + provider_options 对象
// 形态（键=provider_code，Create 内按本 provider 取命中子对象 deep-merge
// 覆盖，其余子对象忽略，契约 §2.1 L3/§2.4 规则 4）。
type AudioJobCreateInput struct {
	Params          NormalizedAudioJobParams
	ProviderOptions map[string]any
}

// AudioJobCreateOutput 是创建请求的构造结果（与 VideoCreateOutput 同构；
// paraformer 无厂商私有创建头——天然异步服务不需要 X-DashScope-Async，
// ExtraHeaders 恒 nil）。
type AudioJobCreateOutput struct {
	Method       string
	Path         string
	Body         []byte
	ExtraHeaders map[string]string
}

// audioJobAdapters 是 kind=audio_transcription 的 provider → adapter 注册表
// （媒体设计 §5；key 空间与 videoAdapters 按任务种类分离——同一 provider 可
// 在两表各挂一条，链上层按 media_jobs.kind 选表）。M3f 首个条目 qwen
// （paraformer）；后续供应商只新增条目。
var audioJobAdapters = map[string]AudioJobProviderAdapter{
	"qwen": qwenASRAdapter{},
}

// AudioJobAdapterForProvider 按 provider_code 解析长转写 adapter；nil 表示
// 该 provider 无长转写 adapter（链上层按能力缺失处理，不静默回退）。
func AudioJobAdapterForProvider(providerCode string) AudioJobProviderAdapter {
	return audioJobAdapters[normalizeProviderKey(providerCode)]
}

// qwenASRAdapter 实现通义百炼（paraformer 系）长转写出站转换。
type qwenASRAdapter struct{}

func (qwenASRAdapter) Provider() string { return "qwen" }

// Capabilities 按 paraformer 请求面现状声明（契约 §10.2）：
//   - SupportsLanguage：parameters.language_hints 原生数组字段（语言提示
//     列表，如 ["zh", "en"]）→ true；对外 language 单字符串映射为单元素
//     数组（换算类参数，契约 §2.4 规则 3）。
func (qwenASRAdapter) Capabilities() AudioJobCapabilities {
	return AudioJobCapabilities{SupportsLanguage: true}
}

// Create 构造契约 §10.2 创建报文：POST /api/v1/services/audio/asr/
// transcription，body 为 {model,input,parameters}。映射规则：
//   - input_url → input.file_urls：公网音频 URL 单元素数组（契约 §10.2：
//     只接受公网 URL 数组；对外首版 input_url 是唯一输入形态——零存储不
//     暂存文件，multipart 在归一层前置拒绝）；
//   - language → parameters.language_hints：单字符串 → 单元素数组（官方
//     字段是数组形态，纯格式转换语义不变；未传不带该键）；
//   - provider_options 命中 qwen 的子对象 deep-merge 覆盖同名键（契约
//     §2.1 L3，嵌套对象递归合并，如 {"qwen":{"parameters":{
//     "diarization_enabled":true}}}）；
//   - 无 X-DashScope-Async 头（契约 §10.2：该服务天然异步，ExtraHeaders
//     恒 nil——与万相视频 §10.1 的关键差异）。
func (a qwenASRAdapter) Create(_ context.Context, in AudioJobCreateInput) (AudioJobCreateOutput, error) {
	params := in.Params
	if params.Model == "" {
		return AudioJobCreateOutput{}, fmt.Errorf("长音频转写请求缺少 model")
	}
	if params.InputURL == "" {
		return AudioJobCreateOutput{}, fmt.Errorf("长音频转写请求缺少 input_url")
	}
	body := map[string]any{
		"model": params.Model,
		"input": map[string]any{
			"file_urls": []string{params.InputURL},
		},
	}
	if params.Language != "" {
		body["parameters"] = map[string]any{
			"language_hints": []string{params.Language},
		}
	}
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return AudioJobCreateOutput{}, fmt.Errorf("编码 qwen 长转写创建请求体失败: %w", err)
	}
	return AudioJobCreateOutput{
		Method: http.MethodPost,
		Path:   "/api/v1/services/audio/asr/transcription",
		Body:   encoded,
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /api/v1/tasks/{task_id}（契约 §10.2：
// 与万相视频同一 DashScope 任务接口，task_id 即创建响应的受理凭据）。
func (qwenASRAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/api/v1/tasks/" + upstreamJobID, nil
}

// ContentFromArtifact 声明 qwen 长转写的产物下载定位：completed 的下载地址
// 是轮询冻结的 output.results[].transcription_url 绝对 URL（契约 §10.2：
// 转写结果 JSON 文件的下载链接——官方为 OSS 时效地址，凭据内嵌于 URL），
// 不能由任务 id 推导——链上层直连该 URL 且不携带账户认证，不调用
// BuildContentRequest（同万相 video_url 先例）。
func (qwenASRAdapter) ContentFromArtifact() bool { return true }

// BuildContentRequest 在 qwen 形态下不参与下载定位（ContentFromArtifact 为
// true）；保留接口实现返回空路径，误调用属链上层接线错误。
func (qwenASRAdapter) BuildContentRequest(string) (string, string) {
	return http.MethodGet, ""
}

// BuildCancelRequest 在 qwen 形态下不参与取消：契约 §10.2 回填面无任务取消
// 端点，SupportsCancel 为 false，链上层不发上游请求、直接本地收敛
// cancelled（§2.6 cancelled 本地终态语义，沿万相 §10.1 裁决）。
func (qwenASRAdapter) BuildCancelRequest(string) (string, string) {
	return http.MethodPost, ""
}

// SupportsCancel：qwen 契约 §10.2 面无取消端点 → false（回填后升级）。
func (qwenASRAdapter) SupportsCancel() bool { return false }

// qwenASRTaskObject 是契约 §10.2 任务响应对象的消费面：创建响应携带
// output.task_id + output.task_status（PENDING）；轮询响应 SUCCEEDED 形态
// 携带 output.results[].transcription_url（转写结果 JSON 文件链接）、FAILED
// 形态携带 output.message（§10.2 钉住；顶层 code/message 兜底兼容）。
// usage 为 JSON 字符串形态（与万相 §10.1 同族，双形态兼容读取）。
type qwenASRTaskObject struct {
	Output struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		Results    []struct {
			FileURL          string `json:"file_url"`
			TranscriptionURL string `json:"transcription_url"`
		} `json:"results"`
		// Message 是 FAILED 形态 output 内的错误消息（契约 §10.2）。
		Message string `json:"message"`
	} `json:"output"`
	Usage json.RawMessage `json:"usage"`
	Code  string          `json:"code"`
	// Message 是 FAILED 形态的顶层错误消息（兜底兼容，与万相同构）。
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// qwenASRUsageObject 是 usage JSON 的时长计量字段消费面（契约 §10.2：时长
// 计量）。paraformer 轮询 usage 携带 duration（秒，输入音频时长）。
type qwenASRUsageObject struct {
	Duration *float64 `json:"duration"`
}

// qwenASRUsageSecondsOf 解析契约 §10.2 的 usage 字段：DashScope 的 usage 是
// JSON 字符串形态（字符串内再嵌一层 JSON），对象形态兼容（与万相
// qwenUsageSecondsOf 同族双形态解析）。取 duration 正值秒 → AudioInputSeconds
// （时长计量照抽，契约 §10.2 计费行）；解析失败或无正值不报错（返回 nil，
// 终态计费由链上按契约 §2.8 兜底 0 计费 + usage_missing 标记）。
func qwenASRUsageSecondsOf(raw json.RawMessage) *float64 {
	if len(raw) == 0 {
		return nil
	}
	decoded := raw
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && strings.TrimSpace(text) != "" {
		decoded = []byte(text)
	}
	var usage qwenASRUsageObject
	if err := json.Unmarshal(decoded, &usage); err != nil {
		return nil
	}
	if usage.Duration != nil && *usage.Duration > 0 {
		value := *usage.Duration
		return &value
	}
	return nil
}

// ParsePollResponse 把创建/轮询响应归一为 MediaJobIR（Kind=audio_transcription，
// 同一任务响应对象族）：
//   - 非 2xx → *UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）；
//   - output.task_id 缺失 → 协议违约报错（受理凭据字段）；
//   - 状态归一与万相同表（qwenTaskStatusOf，契约 §10.2 钉住；未知值归
//     in_progress 记 RawStatus，不猜测失败）；
//   - SUCCEEDED → completed（output.results[].transcription_url 冻结进
//     Artifact.ContentURL——结果 JSON 文件链接，直连下载无凭据；全部
//     results 缺失 transcription_url 属上游协议违约，报错不猜测产物可达
//     性）+ usage 时长解析抽 AudioInputSeconds；
//   - FAILED → failed（错误摘要优先 output.message（契约 §10.2 钉住），
//     顶层 code/message 兜底；全部缺席以 status 原值兜底，不丢弃终态）。
func (qwenASRAdapter) ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj qwenASRTaskObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("qwen 长转写任务响应不是有效 JSON: %w", err)
	}
	if obj.Output.TaskID == "" {
		return nil, fmt.Errorf("qwen 长转写任务响应缺少 output.task_id（受理凭据字段）")
	}
	status, known := qwenTaskStatusOf(obj.Output.TaskStatus)
	ir := &MediaJobIR{
		Kind:          JobKindAudioTranscription,
		UpstreamJobID: obj.Output.TaskID,
		Status:        status,
	}
	if !known {
		ir.RawStatus = obj.Output.TaskStatus
	}
	switch status {
	case JobStatusCompleted:
		for _, result := range obj.Output.Results {
			if url := strings.TrimSpace(result.TranscriptionURL); url != "" {
				ir.Artifact.ContentURL = url
				break
			}
		}
		if ir.Artifact.ContentURL == "" {
			return nil, fmt.Errorf("qwen 长转写任务 SUCCEEDED 但缺少 output.results[].transcription_url 产物定位（task_id=%s）", obj.Output.TaskID)
		}
		if seconds := qwenASRUsageSecondsOf(obj.Usage); seconds != nil {
			ir.Usage.AudioInputSeconds = seconds
		}
	case JobStatusFailed:
		code, message := "FAILED", "qwen 长转写任务 task_status=FAILED"
		if obj.Code != "" {
			code = obj.Code
		}
		if obj.Output.Message != "" {
			message = obj.Output.Message
		} else if obj.Message != "" {
			message = obj.Message
		}
		ir.Error = &MediaJobError{Code: code, Message: message}
	}
	return ir, nil
}

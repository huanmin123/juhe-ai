// qwen_video.go 是 M3 异步视频的通义百炼（万相 wan 系）出站
// adapter（媒体设计 §5：媒体 IR → 每厂商一个 adapter，注册表组织；本文件挂
// videoAdapters 的 "qwen" 条目）。报文依据契约 §10.1（置信度 B：DashScope
// 官方文档入口 + 2026-10-04 经阿里云百炼 legacy 万相文生视频/图生视频 API
// 参考核实）：创建 POST /api/v1/services/aigc/video-generation/
// video-synthesis + **请求头 X-DashScope-Async: enable**（DashScope 异步
// 任务约定，缺失上游报 "current user api does not support synchronous
// calls"），Bearer DASHSCOPE_API_KEY 认证，请求 {model,input:{prompt,
// negative_prompt?,img_url?},parameters:{size?,duration?,seed?}}，响应
// {"output":{"task_id","task_status":"PENDING"},"request_id"}——受理凭据 =
// output.task_id；轮询 GET /api/v1/tasks/{task_id} →
// {"output":{"task_status":"PENDING|RUNNING|SUCCEEDED|FAILED","video_url"},
// "usage":"<JSON 字符串形态>","code","message"}。出站路径自带 /api/v1
// 服务根，URL 由链上层经 chainQwenVideoUpstreamURL 归一（base 已含 /api/v1
// 时去重，不走 openai /v1 强制补缀——同 glm /api/paas/v4、volcengine
// /api/v3 先例）；纯逻辑层，不触 chain/HTTP 执行。
package gatewaymedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// qwenVideoAdapter 实现通义百炼（万相 wan 系）视频出站转换。
type qwenVideoAdapter struct{}

func (qwenVideoAdapter) Provider() string { return "qwen" }

// Capabilities 按 video-synthesis 请求面现状声明（契约 §2.2/§10.1；官方
// legacy 万相文生视频/图生视频 API 参考 2026-10-04 核实）：
//   - SupportsNegativePrompt：input.negative_prompt 原生字段（官方示例章节
//     "使用反向提示词 支持模型：所有模型"，示例即 wan2.2-t2v-plus）→ true；
//   - SupportsSeed：parameters.seed 原生字段（可选整数，[0, 2147483647]，
//     适用于全部可选模型）→ true；
//   - SupportsAudio：请求面无"生成音频"开关参数（wan2.5/2.6 的 input.
//     audio_url 是输入音频素材字段、非输出开关，语义不同，经 L3
//     provider_options 传递）→ false（忽略 + 回显 params_ignored）；
//   - SupportsN：请求面无多段参数 → false；
//   - SupportsInputReference：input.img_url 原生字段承接公共
//     input_reference（图生视频形态，同端点）→ true；官方支持公网 URL 与
//     Base64 data URL 两种形态，均字符串直传（网关零存储不代为下载的裁决
//     不涉及——url 形态由上游自行拉取）；
//   - SupportsSeconds：parameters.duration 原生字段 → true，数值秒直传
//     （档位合法性由上游裁决，契约 §2.4 规则 3：wan2.2-t2v-plus 固定 5 秒
//     不支持修改，wan2.5 取 5/10、wan2.6 取 [2,15]——请求面字段存在，
//     模型级档位由上游裁决，同 volcengine duration 先例）。
func (qwenVideoAdapter) Capabilities() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: true,
		SupportsSeed:           true,
		SupportsAudio:          false,
		SupportsN:              false,
		SupportsInputReference: true,
		SupportsSeconds:        true,
	}
}

// qwenSizeOf 把公共 size 的 WxH 像素串（归一层已校验形态）换算为万相
// parameters.size 的 "W*H" 形态（官方分隔符是星号 *，2026-10-04 官方 API
// 参考核实，如 1920*1080/832*480；公共层是 x 分隔，纯格式转换语义不变）。
// 档位合法性（480P/1080P 全档词表因模型而异）由上游裁决，网关不硬编码
// 词表（契约 §2.4 规则 3：换算类参数实际生效值回显 params_applied）。
func qwenSizeOf(size string) (string, error) {
	trimmed := strings.TrimSpace(size)
	widthText, heightText, ok := strings.Cut(trimmed, "x")
	if !ok {
		return "", fmt.Errorf("%w: size %q 不是 WxH 像素串", ErrParamUnsupported, size)
	}
	width, widthErr := strconv.Atoi(widthText)
	height, heightErr := strconv.Atoi(heightText)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return "", fmt.Errorf("%w: size %q 不是正整数 WxH 像素串", ErrParamUnsupported, size)
	}
	return widthText + "*" + heightText, nil
}

// Create 构造契约 §10.1 创建报文：POST /api/v1/services/aigc/video-
// generation/video-synthesis，body 为 {model,input,parameters}。映射与透传
// 规则（官方 legacy 万相 API 参考 2026-10-04 核实，契约 §2.7 qwen 列）：
//   - prompt → input.prompt（字符串直传）；
//   - negative_prompt → input.negative_prompt（原生字段直传）；
//   - input_reference → input.img_url（图生视频形态，同端点；url/base64
//     data URL 字符串均直传，官方双形态，网关不代为下载）；
//   - size → parameters.size：WxH → "W*H" 星号分隔格式转换（qwenSizeOf）；
//   - seconds → parameters.duration：数值秒直传（整数形态序列化；模型级
//     档位合法性由上游裁决，契约 §2.4 规则 3）；
//   - seed → parameters.seed：整数直传；
//   - n/audio 归一层判为 ignored，不进报文（§2.4 规则 1）；
//   - provider_options 命中 qwen 的子对象 deep-merge 覆盖同名键（契约
//     §2.1 L3，嵌套对象递归合并，如 {"qwen":{"parameters":{"prompt_extend":
//     true}}}）。
//
// 创建请求必须携带 X-DashScope-Async: enable 头（DashScope 异步任务约定，
// 契约 §10.1）——经 VideoCreateOutput.ExtraHeaders 交由链上层注入，本方法
// 不触 HTTP。
func (a qwenVideoAdapter) Create(_ context.Context, in VideoCreateInput) (VideoCreateOutput, error) {
	params := in.Params
	if params.Model == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 model")
	}
	if params.Prompt == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 prompt")
	}
	input := map[string]any{
		"prompt": params.Prompt,
	}
	if params.InputReference != nil {
		reference := strings.TrimSpace(*params.InputReference)
		if reference == "" {
			return VideoCreateOutput{}, fmt.Errorf("%w: input_reference 不能为空", ErrParamUnsupported)
		}
		input["img_url"] = reference
	}
	body := map[string]any{
		"model": params.Model,
		"input": input,
	}
	parameters := map[string]any{}
	if params.Size != "" {
		size, err := qwenSizeOf(params.Size)
		if err != nil {
			return VideoCreateOutput{}, err
		}
		parameters["size"] = size
	}
	if params.Seconds != nil {
		parameters["duration"] = *params.Seconds
	}
	if params.Seed != nil {
		parameters["seed"] = *params.Seed
	}
	if params.NegativePrompt != "" {
		input["negative_prompt"] = params.NegativePrompt
	}
	if len(parameters) > 0 {
		body["parameters"] = parameters
	}
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 qwen 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/api/v1/services/aigc/video-generation/video-synthesis",
		Body:   encoded,
		// DashScope 异步任务约定（契约 §10.1）：创建请求必带 X-DashScope-
		// Async: enable，缺失上游按同步调用拒绝。链上层（driver 出站构造）
		// 把 ExtraHeaders 注入创建请求头；轮询/下载面不需要。
		ExtraHeaders: map[string]string{"X-DashScope-Async": "enable"},
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /api/v1/tasks/{task_id}（契约 §10.1；
// task_id 即创建响应的受理凭据，官方为 UUID 形态 URL 安全串直拼）。
func (qwenVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/api/v1/tasks/" + upstreamJobID, nil
}

// ContentFromArtifact 声明 qwen 形态的产物下载定位：completed 的下载地址是
// 轮询冻结的 output.video_url 绝对 URL（契约 §10.1：官方为 OSS 签名地址、
// 24 小时时效，凭据内嵌于 URL——video_url 直连，与 Veo uri / glm
// video_result.url / volcengine content.video_url 同族先例），不能由任务 id
// 推导——链上层直连该 URL 且不携带账户认证，不调用 BuildContentRequest。
func (qwenVideoAdapter) ContentFromArtifact() bool { return true }

// BuildContentRequest 在 qwen 形态下不参与下载定位（ContentFromArtifact 为
// true，链上层直连 Artifact.ContentURL）；保留接口实现返回空路径，误调用
// 属链上层接线错误。
func (qwenVideoAdapter) BuildContentRequest(string) (string, string) {
	return http.MethodGet, ""
}

// BuildCancelRequest 在 qwen 形态下不参与取消：契约 §10.1 回填面无任务取消
// 端点（官方轮询词表有 CANCELED 终态词，但未回填可调用的取消 API），M3 按
// §10.1 面实现，SupportsCancel 为 false，链上层不发上游请求、直接本地收敛
// cancelled（§2.6 cancelled 本地终态语义，沿 glm/minimax/volcengine 裁决）。
func (qwenVideoAdapter) BuildCancelRequest(string) (string, string) {
	return http.MethodPost, ""
}

// SupportsCancel：qwen 契约 §10.1 面无取消端点 → false（回填后升级）。
func (qwenVideoAdapter) SupportsCancel() bool { return false }

// qwenVideoTaskObject 是契约 §10.1 任务响应对象的消费面：创建响应携带
// output.task_id + output.task_status（PENDING）；轮询响应携带 output.
// task_status 词表与 succeeded 形态的 output.video_url、failed 形态的顶层
// code/message。usage 为 JSON 字符串形态（契约 §10.1 钉住），双形态兼容
// 读取（json.RawMessage）。
type qwenVideoTaskObject struct {
	Output struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		VideoURL   string `json:"video_url"`
	} `json:"output"`
	Usage json.RawMessage `json:"usage"`
	Code  string          `json:"code"`
	// Message 是 failed 形态的顶层错误消息（与 code 成对；成功响应不携带）。
	Message string `json:"message"`
	// RequestID 官方请求追踪字段，受理面不消费（保留解析容错）。
	RequestID string `json:"request_id"`
}

// qwenTaskStatusOf 把任务 output.task_status 归一为统一 status（契约 §2.6
// qwen 列）：PENDING→queued、RUNNING→in_progress、SUCCEEDED→completed、
// FAILED→failed。官方词表另有 CANCELED（任务被取消的终态）与 UNKNOWN
// （任务不存在或超 24 小时）——轮询面未在契约 §2.6 回填归一，按归一规则
// （未知值一律归 in_progress 并记录原始值，不猜测失败；上游 404 查询且本地
// 非终态保持本地状态直至 TTL 过期——UNKNOWN 的"任务不存在"语义由链上层
// 404 分支承载，mock 面不产生）归 in_progress 记 RawStatus（同 volcengine
// expired 先例）。
func qwenTaskStatusOf(status string) (MediaJobStatus, bool) {
	switch strings.TrimSpace(status) {
	case "":
		return JobStatusQueued, true
	case "PENDING":
		return JobStatusQueued, true
	case "RUNNING":
		return JobStatusInProgress, true
	case "SUCCEEDED":
		return JobStatusCompleted, true
	case "FAILED":
		return JobStatusFailed, true
	default:
		return JobStatusInProgress, false
	}
}

// qwenVideoUsageObject 是 usage JSON 的计量字段消费面（官方 legacy 万相 API
// 参考 2026-10-04 核实）：wan2.5 及以下版本返回 video_duration/video_ratio；
// wan2.6 返回 duration/input_video_duration/output_video_duration/SR/size/
// video_count。三个时长字段按 video_duration（wan2.2 系所在口径）→
// output_video_duration → duration 的顺序取第一个正值（字段族跨版本共存，
// 同一响应只出现一组）。
type qwenVideoUsageObject struct {
	VideoDuration       *float64 `json:"video_duration"`
	Duration            *float64 `json:"duration"`
	OutputVideoDuration *float64 `json:"output_video_duration"`
}

// qwenUsageSecondsOf 解析契约 §10.1 的 usage 字段：DashScope 的 usage 是
// **JSON 字符串**形态（字符串内再嵌一层 JSON），官方文档示例亦展示对象
// 形态——双形态兼容：先尝试按字符串解出再二次解析，失败则按对象直接
// 解析。解析失败或无正值时长字段不报错（返回 nil，终态计费由链上按契约
// §2.8 兜底 0 计费 + usage_missing 标记，同 volcengine 先例）；Raw 原文
// 不落任务面（media_jobs 无 usage 原文列，回填真实字段后再议留档面）。
func qwenUsageSecondsOf(raw json.RawMessage) *float64 {
	if len(raw) == 0 {
		return nil
	}
	decoded := raw
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && strings.TrimSpace(text) != "" {
		decoded = []byte(text)
	}
	var usage qwenVideoUsageObject
	if err := json.Unmarshal(decoded, &usage); err != nil {
		return nil
	}
	for _, seconds := range []*float64{usage.VideoDuration, usage.OutputVideoDuration, usage.Duration} {
		if seconds != nil && *seconds > 0 {
			value := *seconds
			return &value
		}
	}
	return nil
}

// ParsePollResponse 把创建/轮询响应归一为 MediaJobIR（同一任务响应对象族）：
//   - 非 2xx → *UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）；
//   - output.task_id 缺失 → 协议违约报错（受理凭据字段）；
//   - PENDING/RUNNING/未知非空 → queued/in_progress（未知值记 RawStatus）；
//   - SUCCEEDED → completed（output.video_url 冻结进 Artifact.ContentURL，
//     直连下载无凭据；url 缺失属上游协议违约，报错不猜测产物可达性）+
//     usage JSON 字符串解析抽 OutputVideoSeconds（qwenUsageSecondsOf；
//     解析失败不失败，仅无计量——§2.8 兜底）；
//   - FAILED → failed（错误摘要取顶层 code/message；缺席属协议违约，以
//     status 原值兜底，不丢弃终态）。
func (qwenVideoAdapter) ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj qwenVideoTaskObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("qwen 视频任务响应不是有效 JSON: %w", err)
	}
	if obj.Output.TaskID == "" {
		return nil, fmt.Errorf("qwen 视频任务响应缺少 output.task_id（受理凭据字段）")
	}
	status, known := qwenTaskStatusOf(obj.Output.TaskStatus)
	ir := &MediaJobIR{
		Kind:          JobKindVideo,
		UpstreamJobID: obj.Output.TaskID,
		Status:        status,
	}
	if !known {
		ir.RawStatus = obj.Output.TaskStatus
	}
	switch status {
	case JobStatusCompleted:
		url := strings.TrimSpace(obj.Output.VideoURL)
		if url == "" {
			return nil, fmt.Errorf("qwen 视频任务 SUCCEEDED 但缺少 output.video_url 产物定位（task_id=%s）", obj.Output.TaskID)
		}
		ir.Artifact.ContentURL = url
		if seconds := qwenUsageSecondsOf(obj.Usage); seconds != nil {
			ir.Usage.OutputVideoSeconds = seconds
		}
	case JobStatusFailed:
		code, message := "FAILED", "qwen 视频任务 task_status=FAILED"
		if obj.Code != "" {
			code = obj.Code
		}
		if obj.Message != "" {
			message = obj.Message
		}
		ir.Error = &MediaJobError{Code: code, Message: message}
	}
	return ir, nil
}

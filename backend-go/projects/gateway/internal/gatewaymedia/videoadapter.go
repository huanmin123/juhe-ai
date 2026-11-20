// videoadapter.go 是 M2 异步视频的出站 adapter 端口与 openai（sora）首个
// 实现（媒体设计 §5：媒体 IR → 每厂商一个 adapter，注册表组织，不进组合根
// switch；M3 只注册新条目）。报文依据契约 §4.3（置信度 A）：创建 POST
// /v1/videos、轮询 GET /v1/videos/{id}、下载 GET /v1/videos/{id}/content、
// 取消 DELETE /v1/videos/{id}。本文件是纯逻辑层（构造与解析），不触
// chain/HTTP 执行/仓储（M2 后续任务接入）。
package gatewaymedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

// VideoProviderAdapter 是异步视频出站 adapter 端口（对齐媒体设计 §5 职责
// 边界：参数映射、URL 构造、上游 job 语义 → MediaJobIR 状态归一、终态
// usage 抽取、产物定位）。路径均为相对账户 base_url 的上游路径，认证与
// base_url 拼接由链上层承载。ctx 保留在 Create 签名中供链上层注入取消与
// 追踪，纯构造实现不消费。
type VideoProviderAdapter interface {
	// Provider 返回注册键（provider_code 归一小写）。
	Provider() string
	// Capabilities 声明该厂商视频请求面能力（参数归一与 ignored 计算的
	// 裁决输入）。
	Capabilities() VideoCapabilities
	// Create 把归一参数 + metadata（键=厂商原生参数名）
	// 映射为上游创建请求。错误为请求侧 400 语义（能力边界裁决）。
	Create(ctx context.Context, in VideoCreateInput) (VideoCreateOutput, error)
	// BuildPollRequest 构造轮询请求（GET /v1/videos/{id} 族）。upstreamJobID
	// 由调用方保证非空。
	BuildPollRequest(upstreamJobID string) (method, path string, body []byte)
	// ParsePollResponse 把上游轮询响应（状态码 + body）归一为 MediaJobIR。
	// 非 2xx 返回 *UpstreamStatusError（链上层区分受理后 404 收敛与 5xx
	// 故障，不在此伪造终态）。
	ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error)
	// BuildContentRequest 构造产物下载请求（GET /v1/videos/{id}/content 族，
	// 纯流式透传代理的出站请求）。
	BuildContentRequest(upstreamJobID string) (method, path string)
	// ContentFromArtifact 声明产物下载定位来源：true 表示 completed 的下载
	// 地址是轮询冻结的绝对 Artifact.ContentURL（如 Veo 的 GCS 签名 URL，
	// 凭据内嵌、不能由 job id 推导），链上层直连该 URL 且不携带账户认证，
	// 不调用 BuildContentRequest；false（openai 族）按 BuildContentRequest
	// 由 base_url + job id 构造（M3 gemini 接入引入）。
	ContentFromArtifact() bool
	// BuildCancelRequest 构造取消/删除请求（DELETE /v1/videos/{id} 族）。
	BuildCancelRequest(upstreamJobID string) (method, path string)
	// SupportsCancel 声明上游是否暴露任务取消/删除端点：false（glm，契约
	// §7.1 无取消 API；M3 glm 接入引入）时链上层不发上游请求、直接本地收敛
	// cancelled（契约 §2.6 cancelled 本地终态语义）；true 的 provider 照常
	// BuildCancelRequest 转发（2xx/404/405 收敛本地 cancelled，veo 裁决）。
	SupportsCancel() bool
}

// VideoCreateInput 是创建请求的输入：归一参数 + metadata 对象形态
// （键=厂商原生参数名，Create 内只新增、不覆盖任何既有路径，冲突返回
// ErrMetadataReservedField，契约 §2.1 L3/§2.4 规则 4）。
type VideoCreateInput struct {
	Params   NormalizedVideoParams
	Metadata map[string]any
}

// VideoCreateOutput 是创建请求的构造结果：Method/Path/Body 组成上游出站
// 请求（Path 相对账户 base_url）。ExtraHeaders 是创建请求的厂商私有头
// （M3 qwen 接入引入，最小扩展面：DashScope 异步任务约定的
// X-DashScope-Async: enable，契约 §10.1）——链上层 driver 出站构造时注入
// 创建请求头；nil（openai/gemini/glm/minimax/volcengine 全部既有实现）时
// 零行为差异。轮询/下载/取消面无厂商私有头需求，不设对应字段。
type VideoCreateOutput struct {
	Method       string
	Path         string
	Body         []byte
	ExtraHeaders map[string]string
}

// UpstreamStatusError 保留上游非 2xx 的状态码与响应体。契约 §2.6：上游 404
// 查询且本地非终态 → 保持本地状态直至 TTL 过期；5xx 为受理后故障——两者
// 都不是 adapter 可归一的 job 状态，证据原样上抛由链上层裁决。
type UpstreamStatusError struct {
	StatusCode int
	Body       string
}

func (e *UpstreamStatusError) Error() string {
	return fmt.Sprintf("上游视频任务接口返回状态码 %d: %s", e.StatusCode, e.Body)
}

// videoAdapters 是 provider → adapter 注册表（媒体设计 §5：媒体 adapter 以
// provider 注册表挂载；M3 起 gemini（Veo）、glm（CogVideoX）、minimax
// （Hailuo）、volcengine（Seedance）、qwen（万相）与 xai（Grok Imagine
// Video）已接入，后续只新增条目）。
var videoAdapters = map[string]VideoProviderAdapter{
	"openai":     openaiVideoAdapter{},
	"gemini":     veoVideoAdapter{},
	"glm":        glmVideoAdapter{},
	"minimax":    minimaxVideoAdapter{},
	"volcengine": volcengineVideoAdapter{},
	"qwen":       qwenVideoAdapter{},
	"xai":        xaiVideoAdapter{},
}

// VideoAdapterForProvider 按 provider_code 解析视频 adapter；nil 表示该
// provider 无视频 adapter（链上层按能力缺失处理，不静默回退）。
func VideoAdapterForProvider(providerCode string) VideoProviderAdapter {
	return videoAdapters[normalizeProviderKey(providerCode)]
}

// VideoDefaultSecondsProvider 是视频 adapter 的可选能力：按模型声明厂商默认
// 时长（非对话媒体统一计费设计 §7.1/§14.4 #4）。默认时长只进 adapter 常量/
// 表，不进 media_pricing_json；未实现该接口的 adapter 不猜默认值。
type VideoDefaultSecondsProvider interface {
	DefaultSecondsForModel(model string) (float64, bool)
}

// VideoArtifactDurationProbeProvider 是视频 adapter 的可选能力：声明允许在
// 上游未回报时长、请求无生效 seconds、且无 adapter 默认时长时，对
// Artifact.ContentURL 做有界 Range 探测（非对话媒体统一计费设计 §8.4 P4）。
// 默认未实现 = 关闭；只有产物 URL 可直接下载且上游确实不保证回报时长的
// adapter 才显式开启，避免无依据地发起额外网络请求。
type VideoArtifactDurationProbeProvider interface {
	ProbeArtifactDuration() bool
}

// VideoEffectiveMeteringProvider 是视频 adapter 的可选能力：返回归一参数
// 经 adapter 换算后**实际出站**的秒数与分辨率档，用于创建时冻结计费快照。
// 未实现时链上层退回请求生效秒数与标准 size 短边归档；实现了但某些字段
// 无换算法（如无 size）时对应返回 nil/空串。这是计费口径与上游实际工作量
// 对齐的边界：例如 GLM 请求 7s 出站 10s、xAI 请求 7.6s 出站 8s、Veo 无
// seconds 字段出站默认 8s，均须按出站值冻结。
type VideoEffectiveMeteringProvider interface {
	EffectiveVideoMetering(params NormalizedVideoParams) (seconds *float64, resolutionTier string)
}

// VideoArtifactDurationProbeForProvider 解析某供应商是否允许产物时长探测。
func VideoArtifactDurationProbeForProvider(providerCode string) bool {
	adapter := VideoAdapterForProvider(providerCode)
	provider, ok := adapter.(VideoArtifactDurationProbeProvider)
	return ok && provider.ProbeArtifactDuration()
}

// VideoDefaultSecondsForModel 解析某供应商/模型的厂商默认时长。second=true
// 时调用方按 QuantitySource=default 计费；false=未声明，仍走 usage_missing。
func VideoDefaultSecondsForModel(providerCode, model string) (float64, bool) {
	adapter := VideoAdapterForProvider(providerCode)
	provider, ok := adapter.(VideoDefaultSecondsProvider)
	if !ok {
		return 0, false
	}
	return provider.DefaultSecondsForModel(model)
}

// openaiVideoAdapter 实现 openai（sora-2 系）视频直连转换。对外契约即本体
// （契约 §4.3：网关直连转发），本 adapter 承担：seconds 归一值的原生字符串
// 形态序列化、metadata 合并、轮询响应 → MediaJobIR 归一。
type openaiVideoAdapter struct{}

func (openaiVideoAdapter) Provider() string { return "openai" }

// Capabilities 按 sora 现状声明（契约 §2.2/§2.7/§4.3）：
//   - SupportsSeconds/SupportsInputReference：§4.3 请求面原生字段
//     （seconds/size/input_reference）→ true；
//   - SupportsN：非对话媒体统一计费设计 §14.4 #5 / AC-026 裁决 P1 不进入
//     统一计费——MediaJobIR 只承载单产物/单秒数，n>1 在参数层 400
//     （ErrParamUnsupported），不调上游、不按 1 个少计；多产物另立需求。
//     故即使 sora 协议面有 n，本 adapter 也声明 false；
//   - SupportsNegativePrompt：sora 请求面无该参数 → false（忽略+回显）；
//   - SupportsSeed：sora 请求面无该参数 → false（忽略+回显）；
//   - SupportsAudio：sora 2 视频原生带音频但请求面无开关参数（契约 §2.2
//     audio 行注）→ false（忽略+回显）。
func (openaiVideoAdapter) Capabilities() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: false,
		SupportsSeed:           false,
		SupportsAudio:          false,
		SupportsN:              false,
		SupportsInputReference: true,
		SupportsSeconds:        true,
		SupportsSize:           true,
	}
}

// Create 构造契约 §4.3 创建报文：POST /videos（base_url 版本根 /v1 + opPath
// /videos，v2 直拼契约；adapter opPath 不含版本段），body 为
// {model,prompt,seconds,size,n,input_reference}。seconds 序列化为 openai
// 原生字符串形态（§4.3 示例 "seconds":"4"；归一层兼容的数字形态在此统一
// 回归字符串）；归一层判为 ignored 的词表外字段（negative_prompt/seed/
// audio）不进报文；n 未显式传入时按默认 1 显式携带（§2.2）。input_reference
// 以字符串进入 JSON body；真实上游的 multipart 文件装配属链上层传输面
// （M2 后续任务），此处保持 IR 字符串透传不解析存储（契约 §2.7）。
func (a openaiVideoAdapter) Create(_ context.Context, in VideoCreateInput) (VideoCreateOutput, error) {
	params := in.Params
	if params.Model == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 model")
	}
	if params.Prompt == "" && params.InputReference == nil {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 prompt（无图引用时必填）")
	}
	body := map[string]any{
		"model": params.Model,
		"n":     params.N,
	}
	if params.Prompt != "" {
		body["prompt"] = params.Prompt
	}
	if params.Seconds != nil {
		body["seconds"] = strconv.FormatFloat(*params.Seconds, 'f', -1, 64)
	}
	if params.Size != "" {
		body["size"] = params.Size
	}
	if params.InputReference != nil {
		body["input_reference"] = *params.InputReference
	}
	body, err := MergeMetadataReserved(body, in.Metadata, openAIVideoMetadataReservedPaths)
	if err != nil {
		return VideoCreateOutput{}, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 openai 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/videos",
		Body:   encoded,
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /videos/{id}（契约 §4.3；opPath 不含
// 版本段，版本根由账户 base_url 承载）。
func (openaiVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/videos/" + upstreamJobID, nil
}

// BuildContentRequest 构造产物下载请求：GET /videos/{id}/content（契约
// §4.3，视频字节流，保留时效约 1 小时）。
func (openaiVideoAdapter) BuildContentRequest(upstreamJobID string) (string, string) {
	return http.MethodGet, "/videos/" + upstreamJobID + "/content"
}

// BuildCancelRequest 构造取消/删除请求：DELETE /videos/{id}（契约 §4.3；
// opPath 不含版本段）。
func (openaiVideoAdapter) BuildCancelRequest(upstreamJobID string) (string, string) {
	return http.MethodDelete, "/videos/" + upstreamJobID
}

// SupportsCancel：openai videos 族有 DELETE 取消端点（契约 §4.3）→ true。
func (openaiVideoAdapter) SupportsCancel() bool { return true }

// ContentFromArtifact：openai 形态的下载定位由 base_url + job id 构造
// （BuildContentRequest），产物不经绝对 URL 直连（M3 gemini 对比项）。
func (openaiVideoAdapter) ContentFromArtifact() bool { return false }

// openaiVideoObject 是契约 §4.3 video 对象的消费面（id/object/status/
// progress/model/prompt/seconds_length/size，completed 时 content 数组提供
// 下载定位，failed 时 error 对象）。字段顺序对齐 mockupstream videoObject
// （Mock 上游按本契约派生，adapter 按同一契约消费）。
type openaiVideoObject struct {
	ID            string                   `json:"id"`
	Object        string                   `json:"object"`
	Status        string                   `json:"status"`
	Progress      *int                     `json:"progress"`
	Model         string                   `json:"model"`
	Prompt        string                   `json:"prompt"`
	SecondsLength *int                     `json:"seconds_length"`
	Size          string                   `json:"size"`
	Content       []openaiVideoContentPart `json:"content"`
	Error         *openaiVideoError        `json:"error"`
}

// openaiVideoContentPart 是 completed 状态的下载定位（content[].url）。
type openaiVideoContentPart struct {
	Status string `json:"status"`
	Type   string `json:"type"`
	URL    string `json:"url"`
}

// openaiVideoError 是 failed 状态的错误对象（code/message 对）。
type openaiVideoError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ParsePollResponse 把轮询响应归一为 MediaJobIR：状态经 NormalizeOpenAIStatus
// 归一（未知值记 RawStatus，不猜失败）；failed 时错误对象进 Error；completed
// 时 content[].url 进 Artifact.ContentURL 且 seconds_length 进
// Usage.OutputVideoSeconds（契约 §2.8：openai 视频上游无 usage 回报，网关
// 自算按任务参数秒数，失败不虚计——非 completed 不填）。ParamsApplied/
// ParamsIgnored 不在轮询面重算（创建时落库回显，契约 §2.4 规则 5）。
func (openaiVideoAdapter) ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj openaiVideoObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("openai 视频任务响应不是有效 JSON: %w", err)
	}
	if obj.ID == "" {
		return nil, fmt.Errorf("openai 视频任务响应缺少 id（受理凭据字段）")
	}
	status, known := NormalizeOpenAIStatus(obj.Status)
	ir := &MediaJobIR{
		Kind:          JobKindVideo,
		UpstreamJobID: obj.ID,
		Status:        status,
		Progress:      obj.Progress,
	}
	if !known {
		ir.RawStatus = obj.Status
	}
	if obj.Error != nil {
		ir.Error = &MediaJobError{Code: obj.Error.Code, Message: obj.Error.Message}
	}
	if status == JobStatusCompleted {
		for _, part := range obj.Content {
			if part.URL != "" {
				ir.Artifact.ContentURL = part.URL
				break
			}
		}
		if ir.Artifact.ContentURL == "" {
			// 契约 §2.6：completed 的语义即 content 提供下载定位；缺失属
			// 上游协议违约，不猜测产物可达性。
			return nil, fmt.Errorf("openai 视频任务 completed 但缺少 content 下载定位（id=%s）", obj.ID)
		}
		if obj.SecondsLength != nil {
			seconds := float64(*obj.SecondsLength)
			ir.Usage.OutputVideoSeconds = &seconds
		}
		// 上游终态回带 size 时按实际尺寸归档，结算优先于创建时换算档位。
		if width, height, ok := mediapricing.ParseSize(obj.Size); ok {
			if tier, ok := mediapricing.TierForDimensions(mediapricing.KindVideo, width, height); ok {
				ir.Usage.ResolutionTier = tier
			}
		}
	}
	return ir, nil
}

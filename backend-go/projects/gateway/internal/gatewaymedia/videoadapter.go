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
	// Create 把归一参数 + provider_options（对象形态，键=provider_code）
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

// VideoCreateInput 是创建请求的输入：归一参数 + provider_options 对象形态
// （键=provider_code，Create 内按本 provider 取命中子对象 deep-merge 覆盖
// 同名 L2 值，其余子对象忽略，契约 §2.1 L3/§2.4 规则 4）。
type VideoCreateInput struct {
	Params          NormalizedVideoParams
	ProviderOptions map[string]any
}

// VideoCreateOutput 是创建请求的构造结果：Method/Path/Body 组成上游出站
// 请求（Path 相对账户 base_url）。
type VideoCreateOutput struct {
	Method string
	Path   string
	Body   []byte
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
// （Hailuo）与 volcengine（Seedance）已接入，qwen 只新增条目）。
var videoAdapters = map[string]VideoProviderAdapter{
	"openai":     openaiVideoAdapter{},
	"gemini":     veoVideoAdapter{},
	"glm":        glmVideoAdapter{},
	"minimax":    minimaxVideoAdapter{},
	"volcengine": volcengineVideoAdapter{},
}

// VideoAdapterForProvider 按 provider_code 解析视频 adapter；nil 表示该
// provider 无视频 adapter（链上层按能力缺失处理，不静默回退）。
func VideoAdapterForProvider(providerCode string) VideoProviderAdapter {
	return videoAdapters[normalizeProviderKey(providerCode)]
}

// openaiVideoAdapter 实现 openai（sora-2 系）视频直连转换。对外契约即本体
// （契约 §4.3：网关直连转发），本 adapter 承担：seconds 归一值的原生字符串
// 形态序列化、provider_options 合并、轮询响应 → MediaJobIR 归一。
type openaiVideoAdapter struct{}

func (openaiVideoAdapter) Provider() string { return "openai" }

// Capabilities 按 sora 现状声明（契约 §2.2/§2.7/§4.3）：
//   - SupportsSeconds/SupportsN/SupportsInputReference：§4.3 请求面原生
//     字段（seconds/size/n/input_reference）→ true；
//   - SupportsNegativePrompt：sora 请求面无该参数 → false（忽略+回显）；
//   - SupportsSeed：sora 请求面无该参数 → false（忽略+回显）；
//   - SupportsAudio：sora 2 视频原生带音频但请求面无开关参数（契约 §2.2
//     audio 行注）→ false（忽略+回显）。
func (openaiVideoAdapter) Capabilities() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: false,
		SupportsSeed:           false,
		SupportsAudio:          false,
		SupportsN:              true,
		SupportsInputReference: true,
		SupportsSeconds:        true,
	}
}

// Create 构造契约 §4.3 创建报文：POST /v1/videos，body 为
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
	if params.Prompt == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 prompt")
	}
	body := map[string]any{
		"model":  params.Model,
		"prompt": params.Prompt,
		"n":      params.N,
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
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 openai 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/v1/videos",
		Body:   encoded,
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /v1/videos/{id}（契约 §4.3）。
func (openaiVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/v1/videos/" + upstreamJobID, nil
}

// BuildContentRequest 构造产物下载请求：GET /v1/videos/{id}/content（契约
// §4.3，视频字节流，保留时效约 1 小时）。
func (openaiVideoAdapter) BuildContentRequest(upstreamJobID string) (string, string) {
	return http.MethodGet, "/v1/videos/" + upstreamJobID + "/content"
}

// BuildCancelRequest 构造取消/删除请求：DELETE /v1/videos/{id}（契约 §4.3）。
func (openaiVideoAdapter) BuildCancelRequest(upstreamJobID string) (string, string) {
	return http.MethodDelete, "/v1/videos/" + upstreamJobID
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
	}
	return ir, nil
}

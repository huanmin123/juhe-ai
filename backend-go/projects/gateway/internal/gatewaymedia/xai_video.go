// xai_video.go 是 M3 回填池的 xAI（Grok Imagine Video）出站 adapter
//（媒体设计 §5：媒体 IR → 每厂商一个 adapter，注册表组织；本文件挂
// videoAdapters 的 "xai" 条目）。报文依据契约 §6.1（置信度 A：官方文档
// 原文抓取 2026-10-04 经本地代理 web reader）：创建 POST /v1/videos/
// generations（Bearer API Key），请求 {model,prompt,duration?,aspect_ratio?,
// resolution?,image?,generate_audio?}，响应 {"request_id":"<uuid>"}——受理
// 凭据 = request_id；轮询 GET /v1/videos/{request_id} → status ∈
// pending|done|expired|failed，done 携带 video{url,duration}（url 为临时
// 直连 URL 无凭据、duration 是时长计量基源），failed 携带
// error{code,message}。出站路径为 /v1 前缀的 openai 族形态（api.x.ai 官方
// 根自带 /v1），URL 由链上层经 gatewayopenai.BuildUpstreamURL 归一（base
// 强制 /v1 结尾去重，同 minimax 先例，无需专用归一函数）；纯逻辑层，
// 不触 chain/HTTP 执行。
package gatewaymedia

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// xaiVideoAdapter 实现 xAI（Grok Imagine Video）视频出站转换。
type xaiVideoAdapter struct{}

func (xaiVideoAdapter) Provider() string { return "xai" }

// Capabilities 按 /v1/videos/generations 请求面现状声明（契约 §2.2/§6.1；
// 官方文档 2026-10-04 核实）：
//   - SupportsAudio：generate_audio 原生字段（默认 true 音轨开关）→ true，
//     布尔直传（M3 媒体池第二个 audio 生效的供应商，同 glm with_audio
//     先例）；
//   - SupportsInputReference：image 原生字段承接公共 input_reference →
//     true（图生视频首帧；官方支持公网 URL 与 base64 字符串双形态，均
//     字符串直传，网关零存储不代为下载）；
//   - SupportsSeconds：duration 原生字段（1–15 秒整数）→ true，数值取整
//     直传（区间合法性由上游裁决，契约 §2.4 规则 3）；
//   - SupportsNegativePrompt/SupportsSeed/SupportsN：请求面无对应字段
//     （negative_prompt 无、seed 无、多段无）→ false（忽略 + 回显
//     params_ignored，契约 §2.4 规则 1；n>1 本地 400）。
func (xaiVideoAdapter) Capabilities() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: false,
		SupportsSeed:           false,
		SupportsAudio:          true,
		SupportsN:              false,
		SupportsInputReference: true,
		SupportsSeconds:        true,
	}
}

// xaiAspectRatioOf 把公共 size 的宽高比归约为 xAI aspect_ratio 档位（官方
// 词表 1:1/16:9/9:16/4:3/3:4/3:2/2:3，默认 16:9，2026-10-04 官方文档核实；
// 照 volcengineRatioOf 结构）。W:H 约分后与词表精确相等才换算——近似贴合
// 属猜测，词表外取值按契约 §2.4 规则 2 报参数错误（语义无法表达，本地
// 400 不降级）。
func xaiAspectRatioOf(width, height int) (string, error) {
	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("%w: size 宽高必须是正整数", ErrParamUnsupported)
	}
	ratios := []struct {
		name          string
		width, height int
	}{
		{"1:1", 1, 1}, {"16:9", 16, 9}, {"9:16", 9, 16},
		{"4:3", 4, 3}, {"3:4", 3, 4}, {"3:2", 3, 2}, {"2:3", 2, 3},
	}
	divisor := gcd(width, height)
	reducedWidth, reducedHeight := width/divisor, height/divisor
	for _, ratio := range ratios {
		if reducedWidth == ratio.width && reducedHeight == ratio.height {
			return ratio.name, nil
		}
	}
	return "", fmt.Errorf("%w: size %dx%d 的宽高比不在 xAI aspect_ratio 档位（1:1/16:9/9:16/4:3/3:4/3:2/2:3）内", ErrParamUnsupported, width, height)
}

// xaiResolutionOf 把公共 size 的像素规模归约为 xAI resolution 档位（官方
// 词表 480p/720p/1080p，默认 480p；1080p 仅 1.5 的文/图生视频——组合合法
// 性由上游裁决）。按短边取最近档：≥900 → 1080p、≥600 → 720p、其余 →
// 480p（档位中点分档，照 volcengineResolutionOf；契约 §2.2 size 行"无精确
// 档取最近档"）。
func xaiResolutionOf(width, height int) string {
	shorter := width
	if height < shorter {
		shorter = height
	}
	switch {
	case shorter >= 900:
		return "1080p"
	case shorter >= 600:
		return "720p"
	default:
		return "480p"
	}
}

// xaiSizeToAspectRatioResolution 把公共 size 像素串（归一层已校验 WxH 形态）
// 拆为宽高并换算 xAI aspect_ratio + resolution 档位；词表外宽高比报参数
// 错误。
func xaiSizeToAspectRatioResolution(size string) (aspectRatio, resolution string, err error) {
	widthText, heightText, ok := strings.Cut(strings.TrimSpace(size), "x")
	if !ok {
		return "", "", fmt.Errorf("%w: size %q 不是 WxH 像素串", ErrParamUnsupported, size)
	}
	width, widthErr := strconv.Atoi(widthText)
	height, heightErr := strconv.Atoi(heightText)
	if widthErr != nil || heightErr != nil {
		return "", "", fmt.Errorf("%w: size %q 不是 WxH 整数像素串", ErrParamUnsupported, size)
	}
	aspectRatio, ratioErr := xaiAspectRatioOf(width, height)
	if ratioErr != nil {
		return "", "", ratioErr
	}
	return aspectRatio, xaiResolutionOf(width, height), nil
}

// Create 构造契约 §6.1 创建报文：POST /v1/videos/generations，body 为
// {model,prompt,duration?,aspect_ratio?,resolution?,image?,generate_audio?}。
// 映射与透传规则（官方文档 2026-10-04 核实，契约 §2.7 xai 列）：
//   - prompt → prompt（文生视频必填；图生视频官方另有可选语义，网关统一
//     必填沿公共 L1 校验，不私造宽松面）；
//   - seconds → duration：官方字段为 1–15 秒整数，归一层允许的数字/小数
//     形态在此取整（四舍五入到最近整数秒）后直传，区间合法性由上游裁决
//     （契约 §2.4 规则 3）；
//   - size → aspect_ratio + resolution 档位换算（xaiSizeToAspectRatioResolution；
//     词表外宽高比 400，不猜测）；
//   - input_reference → image：url/base64 字符串直传（官方双形态，网关
//     不代为下载）；
//   - audio → generate_audio：布尔直传（nil 不传，上游默认 true）；
//   - negative_prompt/seed/n 归一层判为 ignored（n>1 本地 400），不进报文
//     （§2.4 规则 1）；
//   - provider_options 命中 xai 的子对象 deep-merge 覆盖同名键（契约 §2.1
//     L3，如 {"xai":{"reference_audios":[...]}}——§6.1 记录的范围外能力经
//     该通道可达）。
func (a xaiVideoAdapter) Create(_ context.Context, in VideoCreateInput) (VideoCreateOutput, error) {
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
	}
	if params.Seconds != nil {
		body["duration"] = int64(math.Round(*params.Seconds))
	}
	if params.Size != "" {
		aspectRatio, resolution, err := xaiSizeToAspectRatioResolution(params.Size)
		if err != nil {
			return VideoCreateOutput{}, err
		}
		body["aspect_ratio"] = aspectRatio
		body["resolution"] = resolution
	}
	if params.InputReference != nil {
		reference := strings.TrimSpace(*params.InputReference)
		if reference == "" {
			return VideoCreateOutput{}, fmt.Errorf("%w: input_reference 不能为空", ErrParamUnsupported)
		}
		body["image"] = reference
	}
	if params.Audio != nil {
		body["generate_audio"] = *params.Audio
	}
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 xai 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/v1/videos/generations",
		Body:   encoded,
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /v1/videos/{request_id}（契约 §6.1；
// request_id 即创建响应的受理凭据，官方为 uuid 形态 URL 安全串直拼）。
func (xaiVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/v1/videos/" + upstreamJobID, nil
}

// ContentFromArtifact 声明 xai 形态的产物下载定位：completed 的下载地址是
// 轮询冻结的 video.url 临时绝对 URL（契约 §6.1：vidgen.x.ai 临时直连 URL，
// 无凭据——与 Veo uri / volcengine video_url 同族先例），不能由任务 id
// 推导——链上层直连该 URL 且不携带账户认证，不调用 BuildContentRequest。
func (xaiVideoAdapter) ContentFromArtifact() bool { return true }

// BuildContentRequest 在 xai 形态下不参与下载定位（ContentFromArtifact 为
// true，链上层直连 Artifact.ContentURL）；保留接口实现返回空路径，误调用
// 属链上层接线错误。
func (xaiVideoAdapter) BuildContentRequest(string) (string, string) {
	return http.MethodGet, ""
}

// BuildCancelRequest 在 xai 形态下不参与取消：契约 §6.1 面无任务取消端点，
// SupportsCancel 为 false，链上层不发上游请求、直接本地收敛 cancelled
//（§2.6 cancelled 本地终态语义，沿 glm/minimax/volcengine/qwen 裁决）。
func (xaiVideoAdapter) BuildCancelRequest(string) (string, string) {
	return http.MethodPost, ""
}

// SupportsCancel：xai 契约 §6.1 面无取消端点 → false。
func (xaiVideoAdapter) SupportsCancel() bool { return false }

// xaiVideoObject 是契约 §6.1 任务响应对象的消费面：创建响应只携带
// request_id（受理凭据字段，无 status）；轮询响应携带 status 词表与
// done 形态的 video{url,duration}、failed 形态的 error{code,message}。
// 轮询响应是否回带 request_id 契约未回填（§6.1 轮询行字段面只列 status/
// video/error）——在场则消费，缺席不报错（轮询路径不依赖该字段）。
type xaiVideoObject struct {
	RequestID string            `json:"request_id"`
	Status    string            `json:"status"`
	Video     *xaiVideoArtifact `json:"video"`
	Error     *xaiVideoError    `json:"error"`
}

// xaiVideoArtifact 是 done 状态的产物定位与时长计量（契约 §6.1：
// video{url,duration,respect_moderation}；respect_moderation 不消费）。
type xaiVideoArtifact struct {
	URL      string   `json:"url"`
	Duration *float64 `json:"duration"`
}

// xaiVideoError 是 failed 状态的错误对象（code/message 对；code 词表
// invalid_argument/permission_denied/failed_precondition/service_unavailable/
// internal_error——鉴权/限流错误在创建时同步返回，不进任务终态）。
type xaiVideoError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// xaiTaskStatusOf 把任务 status 归一为统一 status（契约 §2.6 xai 列/
// §6.1 状态归一行）：pending→in_progress、done→completed、failed→failed、
// expired→expired（上游原生终态，直接收敛——MediaJobStatus 词表已有该值，
// 终态语义 Terminal() 覆盖）。空串（创建响应无 status 字段）→ in_progress
//（受理凭据确立即已受理，pending 同义）；未知非空值归 in_progress 并返回
// 原始值记入 RawStatus（契约 §2.6 归一规则：不猜测失败）。
func xaiTaskStatusOf(status string) (MediaJobStatus, bool) {
	switch strings.TrimSpace(status) {
	case "":
		return JobStatusInProgress, true
	case "pending":
		return JobStatusInProgress, true
	case "done":
		return JobStatusCompleted, true
	case "failed":
		return JobStatusFailed, true
	case "expired":
		return JobStatusExpired, true
	default:
		return JobStatusInProgress, false
	}
}

// ParsePollResponse 把创建/轮询响应归一为 MediaJobIR：
//   - 非 2xx → *UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）；
//   - 创建形态（status 缺席）request_id 缺失 → 协议违约报错（受理凭据
//     字段，缺失即受理失败）；
//   - pending/空串/未知非空 → in_progress（未知值记 RawStatus）；
//   - done → completed（video.url 冻结进 Artifact.ContentURL；url 缺失属
//     上游协议违约，报错不猜测产物可达性；video.duration 在场则进
//     Usage.OutputVideoSeconds——契约 §6.1 时长计量基源，缺失不猜测、
//     终态计费由链上按契约 §2.8 兜底 usage_missing）；
//   - failed → failed（错误摘要取 error{code,message}；error 对象缺席属
//     协议违约，以 status 原值兜底，不丢弃终态）；
//   - expired → expired（上游原生终态直接收敛，无错误摘要——契约 §6.1
//     未回填该状态的载荷面，不编造）。
func (xaiVideoAdapter) ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj xaiVideoObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("xai 视频任务响应不是有效 JSON: %w", err)
	}
	if obj.RequestID == "" && obj.Status == "" {
		return nil, fmt.Errorf("xai 视频任务响应缺少 request_id（受理凭据字段）")
	}
	status, known := xaiTaskStatusOf(obj.Status)
	ir := &MediaJobIR{
		Kind:          JobKindVideo,
		UpstreamJobID: strings.TrimSpace(obj.RequestID),
		Status:        status,
	}
	if !known {
		ir.RawStatus = obj.Status
	}
	switch status {
	case JobStatusCompleted:
		url := ""
		if obj.Video != nil {
			url = strings.TrimSpace(obj.Video.URL)
		}
		if url == "" {
			return nil, fmt.Errorf("xai 视频任务 done 但缺少 video.url 产物定位（request_id=%s）", obj.RequestID)
		}
		ir.Artifact.ContentURL = url
		if obj.Video != nil && obj.Video.Duration != nil && *obj.Video.Duration > 0 {
			ir.Usage.OutputVideoSeconds = obj.Video.Duration
		}
	case JobStatusFailed:
		code, message := "failed", "xai 视频任务 status=failed"
		if obj.Error != nil {
			if obj.Error.Code != "" {
				code = obj.Error.Code
			}
			if obj.Error.Message != "" {
				message = obj.Error.Message
			}
		}
		ir.Error = &MediaJobError{Code: code, Message: message}
	}
	return ir, nil
}

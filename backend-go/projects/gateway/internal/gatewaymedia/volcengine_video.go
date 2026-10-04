// volcengine_video.go 是 M3 异步视频的火山方舟（豆包 Seedance 系）出站
// adapter（媒体设计 §5：媒体 IR → 每厂商一个 adapter，注册表组织；本文件挂
// videoAdapters 的 "volcengine" 条目）。报文依据契约 §9.1（置信度 B：官方
// 文档入口 + 多源确认；创建参数名 resolution/ratio/duration/seed 与 content
// 数组的 url/base64 双形态已于 2026-10-04 经官方 API 参考页核实）：创建
// POST /api/v3/contents/generations/tasks（Bearer ARK API Key），请求
// {model,content:[{type:text,text},{type:image_url,image_url:{url}}?],
// resolution?,ratio?,duration?,seed?}，响应 {id,status}——受理凭据 = id；
// 轮询 GET /api/v3/contents/generations/tasks/{id} → status ∈
// queued|running|succeeded|failed，succeeded 携带 content.video_url（产物
// 下载定位），failed 携带 error{code,message}。出站路径自带 /api/v3 服务根，
// URL 由链上层经 chainVolcengineVideoUpstreamURL 归一（base 已含 /api/v3
// 时去重，不走 openai /v1 强制补缀——同 glm /api/paas/v4 先例）；纯逻辑
// 层，不触 chain/HTTP 执行。
package gatewaymedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// volcengineVideoAdapter 实现火山方舟（Seedance 系）视频出站转换。
type volcengineVideoAdapter struct{}

func (volcengineVideoAdapter) Provider() string { return "volcengine" }

// Capabilities 按 contents/generations/tasks 请求面现状声明（契约 §2.2/
// §9.1；官方 API 参考 2026-10-04 核实）：
//   - SupportsInputReference：content[].image_url 原生字段承接公共
//     input_reference → true；官方 url 支持公网 URL 与 Base64 data URL 两种
//     形态，均字符串直传（与 minimax 仅 base64 不同，网关零存储不代为下载
//     的裁决不涉及——url 形态由上游自行拉取）；
//   - SupportsSeconds：duration 原生字段（整数秒，默认 5）→ true，数值
//     直传（档位合法性由上游裁决，契约 §2.4 规则 3）；
//   - SupportsSeed：seed 原生字段（整数）→ true；
//   - SupportsNegativePrompt/SupportsAudio/SupportsN：请求面无对应字段
//     （negative_prompt 无；generate_audio 仅 seedance 2.0/1.5 pro 支持，
//     M3 目录仅 1.0 pro 不声明；多段无）→ false（忽略 + 回显
//     params_ignored，契约 §2.4 规则 1）。
func (volcengineVideoAdapter) Capabilities() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: false,
		SupportsSeed:           true,
		SupportsAudio:          false,
		SupportsN:              false,
		SupportsInputReference: true,
		SupportsSeconds:        true,
	}
}

// volcengineRatioOf 把公共 size 的宽高比归约为火山 ratio 档位（官方词表
// 16:9/9:16/1:1/4:3/3:4/21:9，2026-10-04 官方 API 参考核实；keep_ratio/
// adaptive 是厂商原生取值，不参与 WxH 换算）。W:H 约分后与词表精确相等才
// 换算——近似贴合属猜测（如 1.9:1 ≠ 21:9 不硬套），词表外取值按契约 §2.4
// 规则 2 报参数错误（语义无法表达，本地 400 不降级）。
func volcengineRatioOf(width, height int) (string, error) {
	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("%w: size 宽高必须是正整数", ErrParamUnsupported)
	}
	ratios := []struct {
		name          string
		width, height int
	}{
		{"16:9", 16, 9}, {"9:16", 9, 16}, {"1:1", 1, 1},
		{"4:3", 4, 3}, {"3:4", 3, 4}, {"21:9", 21, 9},
	}
	divisor := gcd(width, height)
	reducedWidth, reducedHeight := width/divisor, height/divisor
	for _, ratio := range ratios {
		if reducedWidth == ratio.width && reducedHeight == ratio.height {
			return ratio.name, nil
		}
	}
	return "", fmt.Errorf("%w: size %dx%d 的宽高比不在火山方舟 ratio 档位（16:9/9:16/1:1/4:3/3:4/21:9）内", ErrParamUnsupported, width, height)
}

// gcd 求两正整数最大公约数（约分宽高比用）。
func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// volcengineResolutionOf 把公共 size 的像素规模归约为火山 resolution 档位
// （官方词表 480p/720p/1080p）。按短边取最近档：≥900 → 1080p、≥600 →
// 720p、其余 → 480p（档位中点分档：480/720 中点 600、720/1080 中点 900；
// 契约 §2.2 size 行"无精确档取最近档"，1080x1920 短边 1080 归 1080p；档位
// 与 ratio/模型的合法组合由上游裁决，网关不硬编码）。
func volcengineResolutionOf(width, height int) string {
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

// volcengineSizeToResolutionRatio 把公共 size 像素串（归一层已校验 WxH 形态）
// 拆为宽高并换算火山 resolution + ratio 档位；词表外宽高比报参数错误。
func volcengineSizeToResolutionRatio(size string) (resolution, ratio string, err error) {
	widthText, heightText, ok := strings.Cut(strings.TrimSpace(size), "x")
	if !ok {
		return "", "", fmt.Errorf("%w: size %q 不是 WxH 像素串", ErrParamUnsupported, size)
	}
	width, widthErr := strconv.Atoi(widthText)
	height, heightErr := strconv.Atoi(heightText)
	if widthErr != nil || heightErr != nil {
		return "", "", fmt.Errorf("%w: size %q 不是 WxH 整数像素串", ErrParamUnsupported, size)
	}
	ratio, ratioErr := volcengineRatioOf(width, height)
	if ratioErr != nil {
		return "", "", ratioErr
	}
	return volcengineResolutionOf(width, height), ratio, nil
}

// Create 构造契约 §9.1 创建报文：POST /api/v3/contents/generations/tasks，
// body 为 {model,content,resolution?,ratio?,duration?,seed?}。映射与透传
// 规则（官方 API 参考 2026-10-04 核实，契约 §2.7 volcengine 列）：
//   - prompt → content[0] = {"type":"text","text":...}（content 数组原生形态）；
//   - input_reference → content 追加 {"type":"image_url","image_url":{"url":...}}，
//     url/base64 data URL 字符串均直传（官方双形态，网关不代为下载）；
//   - size → resolution + ratio 档位换算（volcengineSizeToResolutionRatio；
//     词表外宽高比 400，不猜测）；
//   - seconds → duration：数值秒直传（整数形态序列化，档位合法性由上游
//     裁决，契约 §2.4 规则 3）；
//   - seed → seed：整数直传；
//   - negative_prompt/n/audio 归一层判为 ignored，不进报文（§2.4 规则 1）；
//   - provider_options 命中 volcengine 的子对象 deep-merge 覆盖同名键
//     （契约 §2.1 L3，如 {"volcengine":{"camera_fixed":true}}）。
func (a volcengineVideoAdapter) Create(_ context.Context, in VideoCreateInput) (VideoCreateOutput, error) {
	params := in.Params
	if params.Model == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 model")
	}
	if params.Prompt == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 prompt")
	}
	content := []map[string]any{
		{"type": "text", "text": params.Prompt},
	}
	if params.InputReference != nil {
		reference := strings.TrimSpace(*params.InputReference)
		if reference == "" {
			return VideoCreateOutput{}, fmt.Errorf("%w: input_reference 不能为空", ErrParamUnsupported)
		}
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": reference},
		})
	}
	body := map[string]any{
		"model":   params.Model,
		"content": content,
	}
	if params.Size != "" {
		resolution, ratio, err := volcengineSizeToResolutionRatio(params.Size)
		if err != nil {
			return VideoCreateOutput{}, err
		}
		body["resolution"] = resolution
		body["ratio"] = ratio
	}
	if params.Seconds != nil {
		body["duration"] = *params.Seconds
	}
	if params.Seed != nil {
		body["seed"] = *params.Seed
	}
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 volcengine 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/api/v3/contents/generations/tasks",
		Body:   encoded,
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /api/v3/contents/generations/tasks/{id}
// （契约 §9.1；id 即创建响应的受理凭据，官方 id 为 URL 安全串直拼）。
func (volcengineVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/api/v3/contents/generations/tasks/" + upstreamJobID, nil
}

// ContentFromArtifact 声明 volcengine 形态的产物下载定位：completed 的下载
// 地址是轮询冻结的 content.video_url 绝对 URL（契约 §9.1：video_url 直连
// 流式代理，与 Veo uri / glm video_result.url / minimax file_download_url
// 同族先例），不能由任务 id 推导——链上层直连该 URL 且不携带账户认证，
// 不调用 BuildContentRequest。
func (volcengineVideoAdapter) ContentFromArtifact() bool { return true }

// BuildContentRequest 在 volcengine 形态下不参与下载定位（ContentFromArtifact
// 为 true，链上层直连 Artifact.ContentURL）；保留接口实现返回空路径，误
// 调用属链上层接线错误。
func (volcengineVideoAdapter) BuildContentRequest(string) (string, string) {
	return http.MethodGet, ""
}

// BuildCancelRequest 在 volcengine 形态下不参与取消：契约 §9.1 回填面无任务
// 取消端点（官方回调文档提及任务存在 cancelled 终态、仅排队中任务可取消，
// 但取消端点未回填契约，M3 按 §9.1 面实现），SupportsCancel 为 false，链
// 上层不发上游请求、直接本地收敛 cancelled（§2.6 cancelled 本地终态语义，
// 沿 glm/minimax 裁决）。
func (volcengineVideoAdapter) BuildCancelRequest(string) (string, string) {
	return http.MethodPost, ""
}

// SupportsCancel：volcengine 契约 §9.1 面无取消端点 → false（回填后升级）。
func (volcengineVideoAdapter) SupportsCancel() bool { return false }

// volcengineVideoObject 是契约 §9.1 任务响应对象的消费面：创建响应携带
// id + status（queued）；轮询响应携带 status 词表与 succeeded 形态的
// content.video_url、failed 形态的 error{code,message}。
type volcengineVideoObject struct {
	ID      string                  `json:"id"`
	Status  string                  `json:"status"`
	Content *volcengineVideoContent `json:"content"`
	Error   *volcengineVideoError   `json:"error"`
}

// volcengineVideoContent 是 succeeded 状态的产物定位（content.video_url）。
type volcengineVideoContent struct {
	VideoURL string `json:"video_url"`
}

// volcengineVideoError 是 failed 状态的错误对象（code/message 对）。
type volcengineVideoError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// volcengineTaskStatusOf 把任务 status 归一为统一 status（契约 §2.6
// volcengine 列）：queued→queued、running→in_progress、succeeded→
// completed、failed→failed。空串/缺席（创建响应即 status=queued，受理凭据
// 确立即已受理）→ queued；未知非空值（官方回调面另有 expired 等终态词，
// §9.1 轮询面未回填）归 in_progress 并返回原始值记入 RawStatus（契约
// §2.6 归一规则：不猜测失败）。
func volcengineTaskStatusOf(status string) (MediaJobStatus, bool) {
	switch strings.TrimSpace(status) {
	case "":
		return JobStatusQueued, true
	case "queued":
		return JobStatusQueued, true
	case "running":
		return JobStatusInProgress, true
	case "succeeded":
		return JobStatusCompleted, true
	case "failed":
		return JobStatusFailed, true
	default:
		return JobStatusInProgress, false
	}
}

// ParsePollResponse 把创建/轮询响应归一为 MediaJobIR（同一任务响应对象族）：
//   - 非 2xx → *UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）；
//   - id 缺失 → 协议违约报错（受理凭据字段）；
//   - queued/running/未知非空 → queued/in_progress（未知值记 RawStatus）；
//   - succeeded → completed（content.video_url 冻结进 Artifact.ContentURL；
//     url 缺失属上游协议违约，报错不猜测产物可达性）；
//   - failed → failed（错误摘要取 error{code,message}；error 对象缺席属
//     协议违约，以 status 原值兜底，不丢弃终态）。
//
// usage：官方创建文档明示查询 API 返回 duration（"视频时长与计费相关"）
// 与 usage 字段，但字段名未回填契约（§9.1/§2.8"字段回填"）——M3 保守不
// 抽秒，不填 OutputVideoSeconds，终态计费由链上按契约 §2.8 兜底（0 计费 +
// usage_missing 标记，同 glm/minimax 先例）；字段回填后再启用。
func (volcengineVideoAdapter) ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj volcengineVideoObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("volcengine 视频任务响应不是有效 JSON: %w", err)
	}
	if obj.ID == "" {
		return nil, fmt.Errorf("volcengine 视频任务响应缺少 id（受理凭据字段）")
	}
	status, known := volcengineTaskStatusOf(obj.Status)
	ir := &MediaJobIR{
		Kind:          JobKindVideo,
		UpstreamJobID: obj.ID,
		Status:        status,
	}
	if !known {
		ir.RawStatus = obj.Status
	}
	switch status {
	case JobStatusCompleted:
		url := ""
		if obj.Content != nil {
			url = strings.TrimSpace(obj.Content.VideoURL)
		}
		if url == "" {
			return nil, fmt.Errorf("volcengine 视频任务 succeeded 但缺少 content.video_url 产物定位（id=%s）", obj.ID)
		}
		ir.Artifact.ContentURL = url
	case JobStatusFailed:
		code, message := "failed", "volcengine 视频任务 status=failed"
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

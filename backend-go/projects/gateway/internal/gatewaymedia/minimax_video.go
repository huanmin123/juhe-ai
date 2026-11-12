// minimax_video.go 是 M3 异步视频的 MiniMax（Hailuo 系）出站 adapter
// （媒体设计 §5：媒体 IR → 每厂商一个 adapter，注册表组织；本文件挂
// videoAdapters 的 "minimax" 条目）。报文依据契约 §8.1（置信度 B：多源
// 确认）：创建 POST /v1/video_generation（Bearer API Key），请求
// {model,prompt,duration,first_frame_image?}（prompt_optimizer 等厂商个例
// 经 provider_options.minimax deep-merge），响应
// {task_id,base_resp:{status_code,status_msg}}——受理凭据 = task_id，
// base_resp.status_code != 0 视为失败（受理前错误）；轮询 GET
// /v1/query/video_generation?task_id= → task_id + status ∈
// Preparing|Queueing|Processing|Success|Fail，Success 携带
// {file_id,file_download_url}（file_download_url 即产物下载定位）。
// 出站路径为 /v1 前缀的 openai 族形态（URL 由链上层经 gatewayopenai.
// BuildUpstreamURL 归一：base 强制 /v1 结尾、path 剥 /v1 前缀去重，
// api.minimax.chat 与 api.minimaxi.com 两种官方 host 均适用）；纯逻辑层，
// 不触 chain/HTTP 执行。
package gatewaymedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// minimaxVideoAdapter 实现 minimax（Hailuo 系）视频出站转换。
type minimaxVideoAdapter struct{}

func (minimaxVideoAdapter) Provider() string { return "minimax" }

// Capabilities 按 video_generation 请求面现状声明（契约 §2.2/§8.1）：
//   - SupportsSeconds：duration 原生字段（数值秒直传，Hailuo 支持 6/10
//     数值档，实际生效值以上游受理为准）→ true；
//   - DefaultSeconds：请求未携带 duration 时厂商按 6 秒输出（契约 §2.8.2），
//     作为按秒计价的计费基源，不写入上游报文；
//   - SupportsInputReference：first_frame_image 原生字段承接公共
//     input_reference → true，但仅支持 base64/data URL 直传（url 形态
//     不做下载，零存储，Create 内 400，沿 veo 裁决）；
//   - SupportsNegativePrompt/SupportsAudio/SupportsN/SupportsSeed：请求面
//     无对应字段 → false（忽略 + 回显 params_ignored，契约 §2.4 规则 1）。
func (minimaxVideoAdapter) Capabilities() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: false,
		SupportsSeed:           false,
		SupportsAudio:          false,
		SupportsN:              false,
		SupportsInputReference: true,
		SupportsSeconds:        true,
		DefaultSeconds:         6,
	}
}

// minimaxHailuoModel 报告模型是否走 Hailuo-2.3 v1 协议（支持 resolution
// 字段：768P 默认、1080P 仅 6s；720P 对 2.3 不可用）。Fast 与标准版同面。
func minimaxHailuoModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "minimax-hailuo-2.3", "minimax-hailuo-2.3-fast":
		return true
	default:
		return false
	}
}

// minimaxHailuoResolution 把公共 size 短边换算为 Hailuo 官方分辨率档：
// ≥900 → 1080P，其余 768P。Hailuo 2.3 无 720P 档，所以 720p 尺寸落 768P；
// 空 size 用官方默认 768P。
func minimaxHailuoResolution(size string) string {
	shortSide, ok := videoSizeShortSide(size)
	if ok && shortSide >= 900 {
		return "1080P"
	}
	return "768P"
}

// minimaxH3Model 报告模型是否走 H3 v2 协议（MiniMax-H3 / MiniMax-H3-Max）。
// H3-Regeneration / H3-Context-IR 是独立任务面，不走统一视频创建面，保持
// v1 分支（不猜测其协议形态）。
func minimaxH3Model(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "minimax-h3", "minimax-h3-max":
		return true
	default:
		return false
	}
}

// minimaxH3Resolution 把公共 size 换算为 H3 官方分辨率档：H3 为 768P/2K
// （短边 ≥1440 → 2K，其余 768P），H3-Max 为 480P/768P（短边 ≥720 → 768P，
// 其余 480P）。空/非法 size 返回主档 768P；档位与模型的合法组合由上游裁决。
func minimaxH3Resolution(model, size string) string {
	shortSide, ok := videoSizeShortSide(size)
	if !ok {
		return "768P"
	}
	if strings.EqualFold(strings.TrimSpace(model), "MiniMax-H3-Max") {
		if shortSide >= 720 {
			return "768P"
		}
		return "480P"
	}
	if shortSide >= 1440 {
		return "2K"
	}
	return "768P"
}

// BillingResolution 实现 VideoBillingResolutionProvider：H3/H3-Max 按官方
// 分辨率分档计价，创建时冻结计费档；Hailuo 按条计价，返回空串回落主档。
func (minimaxVideoAdapter) BillingResolution(params NormalizedVideoParams) string {
	if minimaxH3Model(params.Model) {
		return minimaxH3Resolution(params.Model, params.Size)
	}
	if minimaxHailuoModel(params.Model) {
		return minimaxHailuoResolution(params.Size)
	}
	return ""
}

// minimaxInputReferenceBase64 归一图生视频首帧引用为 first_frame_image 的
// base64 载荷。契约 §2.7 minimax 列标注 first_frame_image 为 base64 形态
// ——仅支持 base64 data URL 与裸 base64 直传，网关零资源存储（媒体设计
// §8.1）不代为下载 image_url；url 形态返回参数错误（契约 §2.4 规则 2：
// 语义无法表达，400 不降级，沿 veoInputReferenceBase64 同裁决）。
func minimaxInputReferenceBase64(reference string) (string, error) {
	trimmed := strings.TrimSpace(reference)
	if strings.HasPrefix(trimmed, "data:") {
		meta, payload, ok := strings.Cut(trimmed, ",")
		if !ok || payload == "" {
			return "", fmt.Errorf("%w: input_reference data URL 缺少 base64 载荷", ErrParamUnsupported)
		}
		if !strings.Contains(strings.ToLower(meta), ";base64") {
			return "", fmt.Errorf("%w: input_reference data URL 必须是 base64 编码形态", ErrParamUnsupported)
		}
		return payload, nil
	}
	if strings.Contains(trimmed, "://") {
		return "", fmt.Errorf("%w: minimax 图生视频首帧仅支持 base64/data URL 直传，不支持 image_url（网关不代为下载）", ErrParamUnsupported)
	}
	return trimmed, nil
}

// Create 构造契约 §8.1 创建报文：POST {base}/video_generation（opPath 按
// v2 契约不含版本段，/v1 由账户 base_url 版本根承载），body 为
// {model,prompt,duration?,first_frame_image?}。映射与透传规则：
//   - seconds → duration：数值秒直传（Hailuo 请求面为数值形态，档位
//     合法性由上游裁决，契约 §2.4 规则 3 不做换算猜测）；
//   - input_reference → first_frame_image：base64/data URL 直传
//     （minimaxInputReferenceBase64）；
//   - negative_prompt/n/seed/audio 归一层判为 ignored，不进报文；
//   - provider_options 命中 minimax 的子对象 deep-merge 覆盖同名键
//     （契约 §2.1 L3，如 {"minimax":{"prompt_optimizer":true}}）。
func (a minimaxVideoAdapter) Create(_ context.Context, in VideoCreateInput) (VideoCreateOutput, error) {
	params := in.Params
	if params.Model == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 model")
	}
	if params.Prompt == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 prompt")
	}
	if minimaxH3Model(params.Model) {
		return a.createH3(in)
	}
	body := map[string]any{
		"model":  params.Model,
		"prompt": params.Prompt,
	}
	if minimaxHailuoModel(params.Model) {
		// Hailuo 2.3 v1 原生 resolution 字段（720P/768P/1080P；2.3 仅
		// 768P/1080P，1080P 仅 6s）。显式发送使实际产出与冻结计费档一致。
		body["resolution"] = minimaxHailuoResolution(params.Size)
	}
	if params.Seconds != nil {
		body["duration"] = *params.Seconds
	}
	if params.InputReference != nil {
		base64Encoded, err := minimaxInputReferenceBase64(*params.InputReference)
		if err != nil {
			return VideoCreateOutput{}, err
		}
		body["first_frame_image"] = base64Encoded
	}
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 minimax 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/video_generation",
		Body:   encoded,
	}, nil
}

// createH3 构造 H3 v2 创建报文：POST {host}/v2/video_generation（版本段
// 随操作路径，链上层为 v2 路径在 host 根重建，契约 §8.1）。报文形态：
// {model,content[],duration,resolution,ratio?}。映射规则：
//   - prompt → content 首个 text item；input_reference → image_url item，
//     role=first_frame（url/data URL 直传，裸 base64 无 mime 不猜测 → 400）；
//   - duration 恒显式发送：用户 seconds，否则 Capabilities.DefaultSeconds
//     （6 秒，落在官方 4–15 区间），保证实际产出与计费基源一致；
//   - resolution 由 size 短边换算（minimaxH3Resolution），未带 size 用主档
//     768P；
//   - ratio 仅文生视频发送（官方要求且不能为 adaptive），按 size 精确约分
//     到六档词表；图生视频跟随首帧、不发送 ratio。无 size 默认 16:9。
func (a minimaxVideoAdapter) createH3(in VideoCreateInput) (VideoCreateOutput, error) {
	params := in.Params
	content := []any{map[string]any{"type": "text", "text": params.Prompt}}
	if params.InputReference != nil {
		imageURL, err := minimaxH3ImageURL(*params.InputReference)
		if err != nil {
			return VideoCreateOutput{}, err
		}
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": imageURL},
			"role":      "first_frame",
		})
	}
	duration := float64(6)
	if params.Seconds != nil {
		duration = *params.Seconds
	}
	body := map[string]any{
		"model":      params.Model,
		"content":    content,
		"duration":   duration,
		"resolution": minimaxH3Resolution(params.Model, params.Size),
	}
	if params.InputReference == nil {
		ratio, err := minimaxH3Ratio(params.Size)
		if err != nil {
			return VideoCreateOutput{}, err
		}
		body["ratio"] = ratio
	}
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 minimax H3 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/v2/video_generation",
		Body:   encoded,
	}, nil
}

// minimaxH3ImageURL 归一 H3 首帧引用：http(s) URL 与 data URL 原样直传
// （网关零存储不代为下载）；裸 base64 无法确定 mime，报参数错误不猜测。
func minimaxH3ImageURL(reference string) (string, error) {
	trimmed := strings.TrimSpace(reference)
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") || strings.Contains(trimmed, "://") {
		return trimmed, nil
	}
	return "", fmt.Errorf("%w: minimax H3 图生视频首帧仅支持 http(s) URL 或 base64 data URL，裸 base64 缺少 mime 无法表达", ErrParamUnsupported)
}

// minimaxH3Ratio 把 size 精确约分到 H3 ratio 档位。官方指南只给出 16:9
// 示例并指向 API reference；网关按通用六档（16:9/9:16/1:1/4:3/3:4/21:9）
// 精确匹配，词表外报 400（不近似贴合）。无 size 用官方示例默认 16:9。
func minimaxH3Ratio(size string) (string, error) {
	if strings.TrimSpace(size) == "" {
		return "16:9", nil
	}
	widthText, heightText, ok := strings.Cut(strings.TrimSpace(size), "x")
	if !ok {
		return "", fmt.Errorf("%w: size %q 不是 WxH 像素串", ErrParamUnsupported, size)
	}
	width, widthErr := strconv.Atoi(widthText)
	height, heightErr := strconv.Atoi(heightText)
	if widthErr != nil || heightErr != nil {
		return "", fmt.Errorf("%w: size %q 不是 WxH 整数像素串", ErrParamUnsupported, size)
	}
	return minimaxH3RatioOf(width, height)
}

// minimaxH3RatioOf 把宽高精确约分到 H3 ratio 六档；词表外报 400（不近似）。
func minimaxH3RatioOf(width, height int) (string, error) {
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
	return "", fmt.Errorf("%w: size %dx%d 的宽高比不在 H3 ratio 档位（16:9/9:16/1:1/4:3/3:4/21:9）内", ErrParamUnsupported, width, height)
}

// BuildPollRequest 构造轮询请求：GET {base}/query/video_generation?task_id=
// （契约 §8.1；task_id 即创建响应的受理凭据；opPath 不含版本段，/v1 由
// base_url 版本根承载）。task_id 由官方 API 使用
// URL 安全的十六进制任务号，query 直拼不经额外编码（与官方示例一致）。
func (minimaxVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/query/video_generation?task_id=" + upstreamJobID, nil
}

// BuildPollRequestForModel 实现 VideoModelTaskAdapter：H3 v2 轮询为
// GET {host}/v2/query/video_generation/{task_id}（路径参数）；Hailuo 保持
// v1 query 形态。
func (a minimaxVideoAdapter) BuildPollRequestForModel(model, upstreamJobID string) (string, string, []byte) {
	if minimaxH3Model(model) {
		return http.MethodGet, "/v2/query/video_generation/" + url.PathEscape(upstreamJobID), nil
	}
	return a.BuildPollRequest(upstreamJobID)
}

// ContentFromArtifact 声明 minimax 形态的产物下载定位：completed 的下载
// 地址是轮询冻结的 file_download_url 绝对 URL（契约 §8.1；下载形态裁决：
// 契约"GET /v1/files/retrieve?file_id= 或 file_download_url，以回填为准"
// ——取 file_download_url 直连，与 Veo uri / glm video_result.url 同族
// 先例），不能由 task_id 推导——链上层直连该 URL 且不携带账户认证，
// 不调用 BuildContentRequest。
func (minimaxVideoAdapter) ContentFromArtifact() bool { return true }

// BuildContentRequest 在 minimax 形态下不参与下载定位（ContentFromArtifact
// 为 true，链上层直连 Artifact.ContentURL）；保留接口实现返回空路径，误
// 调用属链上层接线错误。
func (minimaxVideoAdapter) BuildContentRequest(string) (string, string) {
	return http.MethodGet, ""
}

// BuildCancelRequest 在 minimax 形态下不参与取消：video_generation 回填面
// 无任务取消端点（契约 §8.1），SupportsCancel 为 false，链上层不发上游
// 请求、直接本地收敛 cancelled（§2.6 cancelled 本地终态语义，沿 glm
// 裁决）。
func (minimaxVideoAdapter) BuildCancelRequest(string) (string, string) {
	return http.MethodPost, ""
}

// SupportsCancel：minimax 无上游取消端点（契约 §8.1）→ false。
func (minimaxVideoAdapter) SupportsCancel() bool { return false }

// minimaxVideoObject 是契约 §8.1 video_generation 响应对象的消费面：
// 创建响应携带 task_id + base_resp（status_code=0 即受理成功）；轮询响应
// 携带 status 词表与 Success 形态的 file_id/file_download_url，Fail 形态
// 携带非零 base_resp（错误摘要来源）。
type minimaxVideoObject struct {
	TaskID          string           `json:"task_id"`
	Status          string           `json:"status"`
	FileID          string           `json:"file_id"`
	FileDownloadURL string           `json:"file_download_url"`
	BaseResp        *minimaxBaseResp `json:"base_resp"`
}

// minimaxBaseResp 是 minimax 的通用错误/状态信封（status_code=0 为成功）。
type minimaxBaseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

// minimaxTaskStatusOf 把轮询 status 归一为统一 status（契约 §2.6 minimax
// 列）：Preparing/Queueing→queued、Processing→in_progress、Success→
// completed、Fail→failed。空串/缺席（创建响应即无 status 字段，受理凭据
// 确立即已受理未开始）→ queued；未知非空值归 in_progress 并返回原始值
// 记入 RawStatus（契约 §2.6 归一规则：不猜测失败）。
func minimaxTaskStatusOf(status string) (MediaJobStatus, bool) {
	switch strings.TrimSpace(status) {
	case "":
		return JobStatusQueued, true
	case "Preparing", "Queueing":
		return JobStatusQueued, true
	case "Processing":
		return JobStatusInProgress, true
	case "Success":
		return JobStatusCompleted, true
	case "Fail":
		return JobStatusFailed, true
	default:
		return JobStatusInProgress, false
	}
}

// ParsePollResponse 把创建/轮询响应归一为 MediaJobIR（同一
// video_generation 响应对象族，按 status 字段二分）：
//   - 非 2xx → *UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）；
//   - status=Fail（轮询面终态）→ failed；错误摘要 code 取 base_resp.
//     status_code 数字串、message 取 status_msg（契约 §2.6 minimax 列
//     "Fail（base_resp）"；base_resp 缺席属协议违约，以 status 原值兜底）；
//   - 其余形态 base_resp.status_code != 0（创建面受理失败：200 + 错误
//     信封且无 Fail 终态标记）→ 受理前错误上抛（受理凭据未确立，链上层
//     按上游协议违约渲染，不落 media_jobs）；
//   - Success → completed（file_download_url 冻结进 Artifact.ContentURL；
//     url 缺失属上游协议违约，报错不猜测产物可达性）；
//   - Preparing/Queueing/Processing/未知非空 → queued/in_progress（未知值
//     记 RawStatus）；创建响应（status 缺席）→ queued（已受理未开始）。
//
// usage：minimax 轮询响应不回报时长/用量字段（§8.1 计费口径"任务响应
// 含用量/按次（回填）"未回填），adapter 不填 OutputVideoSeconds；终态计费
// 由链上按创建时冻结的计费秒数基源结算——用户 duration 生效则用请求值，
// 否则用 Capabilities.DefaultSeconds（6 秒，契约 §2.8.2）。按次计费行
// （Hailuo-2.3）仍优先走目录按次价，不依赖秒计量。
func (minimaxVideoAdapter) ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj minimaxVideoObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("minimax 视频任务响应不是有效 JSON: %w", err)
	}
	status, known := minimaxTaskStatusOf(obj.Status)
	if status == JobStatusFailed {
		if obj.TaskID == "" {
			return nil, fmt.Errorf("minimax 视频任务响应缺少 task_id（受理凭据字段）")
		}
		code, message := "Fail", "minimax 视频任务 status=Fail"
		if obj.BaseResp != nil && obj.BaseResp.StatusCode != 0 {
			code = strconv.Itoa(obj.BaseResp.StatusCode)
			if obj.BaseResp.StatusMsg != "" {
				message = obj.BaseResp.StatusMsg
			}
		}
		return &MediaJobIR{
			Kind:          JobKindVideo,
			UpstreamJobID: obj.TaskID,
			Status:        JobStatusFailed,
			Error:         &MediaJobError{Code: code, Message: message},
		}, nil
	}
	if obj.BaseResp != nil && obj.BaseResp.StatusCode != 0 {
		// 契约 §8.1：base_resp.status_code != 0 视为失败；非 Fail 终态形态
		// 即创建面的受理前错误（受理凭据未确立）。保留原始码与消息上抛。
		return nil, fmt.Errorf("minimax 视频任务失败（base_resp.status_code=%d）: %s",
			obj.BaseResp.StatusCode, obj.BaseResp.StatusMsg)
	}
	if obj.TaskID == "" {
		return nil, fmt.Errorf("minimax 视频任务响应缺少 task_id（受理凭据字段）")
	}
	ir := &MediaJobIR{
		Kind:          JobKindVideo,
		UpstreamJobID: obj.TaskID,
		Status:        status,
	}
	if !known {
		ir.RawStatus = obj.Status
	}
	if status == JobStatusCompleted {
		url := strings.TrimSpace(obj.FileDownloadURL)
		if url == "" {
			return nil, fmt.Errorf("minimax 视频任务 Success 但缺少 file_download_url 产物定位（task_id=%s）", obj.TaskID)
		}
		ir.Artifact.ContentURL = url
	}
	return ir, nil
}

// minimaxH3Task 是 H3 v2 轮询响应的 task 对象（官方指南：task.status、
// task.content.url、task.error；create 响应只给顶层 task_id）。
type minimaxH3Task struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Content *struct {
		URL string `json:"url"`
	} `json:"content"`
	Error json.RawMessage `json:"error"`
}

// minimaxH3PollEnvelope 同时接受官方嵌套形态 {"task":{...}} 与扁平形态
// （顶层 status/content）——指南只给出嵌套示例，扁平作为兼容读取面，
// 不改变写侧协议判断。
type minimaxH3PollEnvelope struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Content *struct {
		URL string `json:"url"`
	} `json:"content"`
	Error json.RawMessage `json:"error"`
	Task  *minimaxH3Task  `json:"task"`
}

// ParsePollResponseForModel 实现 VideoModelTaskAdapter：H3 走 v2 响应归一
// （succeeded/failed/cancelled 词表 + task.content.url 产物定位），Hailuo
// 保持 v1 解析。创建响应 {"task_id":...} 在 H3 分支同样归一为 queued。
func (a minimaxVideoAdapter) ParsePollResponseForModel(model string, statusCode int, body []byte) (*MediaJobIR, error) {
	if !minimaxH3Model(model) {
		return a.ParsePollResponse(statusCode, body)
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var envelope minimaxH3PollEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("minimax H3 视频任务响应不是有效 JSON: %w", err)
	}
	taskID := envelope.TaskID
	statusText := envelope.Status
	contentURL := ""
	errorRaw := envelope.Error
	if envelope.Content != nil {
		contentURL = envelope.Content.URL
	}
	if envelope.Task != nil {
		if taskID == "" {
			taskID = envelope.Task.ID
		}
		statusText = envelope.Task.Status
		contentURL = ""
		if envelope.Task.Content != nil {
			contentURL = envelope.Task.Content.URL
		}
		errorRaw = envelope.Task.Error
	}
	status, known := minimaxH3TaskStatusOf(statusText)
	ir := &MediaJobIR{
		Kind:          JobKindVideo,
		UpstreamJobID: taskID,
		Status:        status,
	}
	if !known {
		ir.RawStatus = statusText
	}
	switch status {
	case JobStatusCompleted:
		contentURL = strings.TrimSpace(contentURL)
		if contentURL == "" {
			return nil, fmt.Errorf("minimax H3 视频任务 succeeded 但缺少 task.content.url 产物定位（task_id=%s）", taskID)
		}
		ir.Artifact.ContentURL = contentURL
	case JobStatusFailed, JobStatusCancelled:
		code, message := minimaxH3ErrorOf(errorRaw)
		ir.Error = &MediaJobError{Code: code, Message: message}
	}
	return ir, nil
}

// minimaxH3TaskStatusOf 归一 v2 状态词表；空串（创建受理成功）→ queued；
// 未知非空值 → in_progress 并保留 RawStatus（不猜测失败）。
func minimaxH3TaskStatusOf(status string) (MediaJobStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "queued", "pending", "created", "preparing", "waiting", "submitted":
		return JobStatusQueued, true
	case "processing", "running", "in_progress", "in-progress":
		return JobStatusInProgress, true
	case "succeeded", "success", "completed":
		return JobStatusCompleted, true
	case "failed", "fail", "error":
		return JobStatusFailed, true
	case "cancelled", "canceled":
		return JobStatusCancelled, true
	default:
		return JobStatusInProgress, false
	}
}

// minimaxH3ErrorOf 提取 v2 error 摘要：字符串原样；对象取 code/message/
// msg；无法解析时保留原始 JSON（不丢弃上游事实）。
func minimaxH3ErrorOf(raw json.RawMessage) (string, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "Failed", "minimax H3 视频任务失败"
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
		return "Failed", text
	}
	var object struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if json.Unmarshal(raw, &object) == nil {
		code := "Failed"
		switch value := object.Code.(type) {
		case string:
			if value != "" {
				code = value
			}
		case float64:
			code = strconv.Itoa(int(value))
		}
		message := object.Message
		if message == "" {
			message = object.Msg
		}
		if message != "" {
			return code, message
		}
	}
	return "Failed", string(raw)
}

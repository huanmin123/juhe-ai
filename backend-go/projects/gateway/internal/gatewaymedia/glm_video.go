// glm_video.go 是 M3 异步视频的智谱 GLM（CogVideoX / 清影系）出站 adapter
// （媒体设计 §5：媒体 IR → 每厂商一个 adapter，注册表组织；本文件挂
// videoAdapters 的 "glm" 条目）。报文依据契约 §7.1（置信度 B：多源确认）：
// 创建 POST /api/paas/v4/videos/generations（Bearer API Key），请求
// {model,prompt,negative_prompt?,image_url?,size?,duration?,fps?,quality?,
// with_audio?}，响应 {id,request_id,task_status}——受理凭据 = id；轮询 GET
// /api/paas/v4/async-result/{id} → task_status ∈ PROCESSING|SUCCESS|FAIL，
// SUCCESS 携带 video_result{url,cover_image_url}（url 即产物下载定位）。
// 出站路径自带 /api/paas/v4 服务根（glm 官方根形态，账户 base_url 已含该根
// 时由链上层去重，chainGlmVideoUpstreamURL）；纯逻辑层，不触 chain/HTTP 执行。
package gatewaymedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// glmVideoAdapter 实现 glm（CogVideoX 系）视频出站转换。
type glmVideoAdapter struct{}

func (glmVideoAdapter) Provider() string { return "glm" }

// Capabilities 按 CogVideoX 请求面现状声明（契约 §2.2/§7.1）：
//   - SupportsNegativePrompt：negative_prompt 原生字段 → true；
//   - SupportsAudio：with_audio 原生字段（承接公共 audio）→ true；
//   - SupportsSeconds：duration 原生字段（"5s"/"10s" 档位，Create 内换算
//     最近档，契约 §2.4 规则 3）→ true；
//   - SupportsInputReference：image_url 原生字段（glm 本就是 URL 形态，
//     url 与 base64 字符串均透传，网关不代为下载，零存储）→ true；
//   - SupportsN/SupportsSeed：请求面无对应字段 → false（忽略 + 回显
//     params_ignored，契约 §2.4 规则 1）。
func (glmVideoAdapter) Capabilities() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: true,
		SupportsSeed:           false,
		SupportsAudio:          true,
		SupportsN:              false,
		SupportsInputReference: true,
		SupportsSeconds:        true,
	}
}

// glmDurationSteps 是 CogVideoX 的 duration 档位（契约 §7.1：如 5s/10s）。
var glmDurationSteps = []float64{5, 10}

// glmDurationOf 把公共 seconds 数值换算为 glm duration 档位字符串（契约
// §2.4 规则 3 / §7.1）：CogVideoX 档位 5s/10s，取数值最近档（等距取小档），
// 换算结果随创建报文生效（实际生效值以上游受理为准）。
func glmDurationOf(seconds float64) string {
	nearest := glmDurationSteps[0]
	bestGap := absFloat64(seconds - glmDurationSteps[0])
	for _, step := range glmDurationSteps[1:] {
		if gap := absFloat64(seconds - step); gap < bestGap {
			nearest = step
			bestGap = gap
		}
	}
	return fmt.Sprintf("%gs", nearest)
}

func absFloat64(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// Create 构造契约 §7.1 创建报文：POST /api/paas/v4/videos/generations，body
// 为 {model,prompt,negative_prompt?,image_url?,size?,duration?,with_audio?}。
// 换算与透传规则：
//   - size：glm 的 size 即 WxH 像素串形态（如 1440x720/1080x1920），与公共
//     L2 形态一致，原样直传——档位合法性由上游裁决（契约 §2.2：网关不硬
//     编码档位清单）；
//   - seconds → duration：数值换算 5s/10s 最近档（glmDurationOf）；
//   - input_reference → image_url：字符串透传（url/base64 均可，glm 原生
//     URL 形态，网关不解析不下载）；
//   - audio → with_audio：布尔直传；
//   - n/seed 归一层判为 ignored，不进报文；
//   - provider_options 命中 glm 的子对象 deep-merge 覆盖同名键（契约 §2.1
//     L3，如 {"glm":{"fps":30,"quality":"quality"}}）。
func (a glmVideoAdapter) Create(_ context.Context, in VideoCreateInput) (VideoCreateOutput, error) {
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
	if params.NegativePrompt != "" {
		body["negative_prompt"] = params.NegativePrompt
	}
	if params.InputReference != nil {
		body["image_url"] = *params.InputReference
	}
	if params.Size != "" {
		body["size"] = params.Size
	}
	if params.Seconds != nil {
		body["duration"] = glmDurationOf(*params.Seconds)
	}
	if params.Audio != nil {
		body["with_audio"] = *params.Audio
	}
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 glm 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/api/paas/v4/videos/generations",
		Body:   encoded,
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /api/paas/v4/async-result/{id}（契约
// §7.1；id 即创建响应的受理凭据）。
func (glmVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/api/paas/v4/async-result/" + upstreamJobID, nil
}

// ContentFromArtifact 声明 glm 形态的产物下载定位：completed 的下载地址是
// 轮询冻结的 video_result.url 绝对 URL（契约 §7.1），不能由任务 id 推导——
// 链上层直连该 URL（无凭据，沿 Veo uri 直连先例），不调用
// BuildContentRequest。
func (glmVideoAdapter) ContentFromArtifact() bool { return true }

// BuildContentRequest 在 glm 形态下不参与下载定位（ContentFromArtifact 为
// true，链上层直连 Artifact.ContentURL）；保留接口实现返回空路径，误调用
// 属链上层接线错误。
func (glmVideoAdapter) BuildContentRequest(string) (string, string) {
	return http.MethodGet, ""
}

// BuildCancelRequest 在 glm 形态下不参与取消：CogVideoX 无任务取消 API
// （契约 §7.1 回填面无取消端点），SupportsCancel 为 false，链上层不发上游
// 请求、直接本地收敛 cancelled（§2.6 cancelled 本地终态语义）。
func (glmVideoAdapter) BuildCancelRequest(string) (string, string) {
	return http.MethodPost, ""
}

// SupportsCancel：glm 无上游取消端点（契约 §7.1）→ false。
func (glmVideoAdapter) SupportsCancel() bool { return false }

// glmAsyncResultObject 是契约 §7.1 async-result（与创建响应同族）对象的
// 消费面：task_status 携带三态词表，SUCCESS 形态带 video_result.url；失败
// 形态的厂商原生错误字段未在回填面固定，可选解析 error 对象，缺席时以
// task_status 原值作错误摘要 code（沿 gemini「error 摘要 code 取 status」
// 裁决，§2.6）。
type glmAsyncResultObject struct {
	ID          string            `json:"id"`
	RequestID   string            `json:"request_id"`
	TaskStatus  string            `json:"task_status"`
	VideoResult *glmVideoResult   `json:"video_result"`
	Error       *glmUpstreamError `json:"error"`
}

// glmVideoResult 是 SUCCESS 形态的产物定位容器。
type glmVideoResult struct {
	URL           string `json:"url"`
	CoverImageURL string `json:"cover_image_url"`
}

// glmUpstreamError 是可选的厂商错误对象（code/message 对）。
type glmUpstreamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// glmTaskStatusOf 把 task_status 归一为统一 status（契约 §2.6 glm 列）：
// PROCESSING→in_progress、SUCCESS→completed、FAIL→failed；未知值（含空串）
// 归 in_progress 并返回原始值记入 RawStatus（不猜测失败）。
func glmTaskStatusOf(taskStatus string) (MediaJobStatus, bool) {
	switch strings.ToUpper(strings.TrimSpace(taskStatus)) {
	case "PROCESSING":
		return JobStatusInProgress, true
	case "SUCCESS":
		return JobStatusCompleted, true
	case "FAIL":
		return JobStatusFailed, true
	default:
		return JobStatusInProgress, false
	}
}

// ParsePollResponse 把创建/轮询响应归一为 MediaJobIR（同一 async-result
// 对象形态，创建响应即 task_status=PROCESSING 的受理回执）：
//   - 非 2xx → *UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）；
//   - SUCCESS → completed（video_result.url 冻结进 Artifact.ContentURL；
//     url 缺失属上游协议违约，报错不猜测产物可达性）；
//   - FAIL → failed（错误摘要：error 对象在场取其 code/message，缺席以
//     task_status 原值为 code）；
//   - PROCESSING/未知 → in_progress（未知值记 RawStatus）。
//
// usage：glm async-result 响应不回报时长/用量字段（§7.1），CogVideoX 按次
// 计费且网关计费面只有秒价维度——不填 OutputVideoSeconds，终态计费由链上
// 按契约 §2.8 兜底（0 计费 + usage_missing 标记，不猜测）。
func (glmVideoAdapter) ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj glmAsyncResultObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("glm 视频任务响应不是有效 JSON: %w", err)
	}
	if obj.ID == "" {
		return nil, fmt.Errorf("glm 视频任务响应缺少 id（受理凭据字段）")
	}
	status, known := glmTaskStatusOf(obj.TaskStatus)
	ir := &MediaJobIR{
		Kind:          JobKindVideo,
		UpstreamJobID: obj.ID,
		Status:        status,
	}
	if !known {
		ir.RawStatus = obj.TaskStatus
	}
	switch status {
	case JobStatusCompleted:
		url := ""
		if obj.VideoResult != nil {
			url = strings.TrimSpace(obj.VideoResult.URL)
		}
		if url == "" {
			return nil, fmt.Errorf("glm 视频任务 SUCCESS 但缺少 video_result.url 产物定位（id=%s）", obj.ID)
		}
		ir.Artifact.ContentURL = url
	case JobStatusFailed:
		code, message := obj.TaskStatus, "glm 视频任务 task_status=FAIL"
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

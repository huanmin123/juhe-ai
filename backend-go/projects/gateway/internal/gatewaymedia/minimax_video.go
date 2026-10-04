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
	"strconv"
	"strings"
)

// minimaxVideoAdapter 实现 minimax（Hailuo 系）视频出站转换。
type minimaxVideoAdapter struct{}

func (minimaxVideoAdapter) Provider() string { return "minimax" }

// Capabilities 按 video_generation 请求面现状声明（契约 §2.2/§8.1）：
//   - SupportsSeconds：duration 原生字段（数值秒直传，Hailuo 支持 6/10
//     数值档，实际生效值以上游受理为准）→ true；
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
	}
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

// Create 构造契约 §8.1 创建报文：POST /v1/video_generation，body 为
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
	body := map[string]any{
		"model":  params.Model,
		"prompt": params.Prompt,
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
		Path:   "/v1/video_generation",
		Body:   encoded,
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /v1/query/video_generation?task_id=
// （契约 §8.1；task_id 即创建响应的受理凭据）。task_id 由官方 API 使用
// URL 安全的十六进制任务号，query 直拼不经额外编码（与官方示例一致）。
func (minimaxVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/v1/query/video_generation?task_id=" + upstreamJobID, nil
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
	TaskID          string            `json:"task_id"`
	Status          string            `json:"status"`
	FileID          string            `json:"file_id"`
	FileDownloadURL string            `json:"file_download_url"`
	BaseResp        *minimaxBaseResp  `json:"base_resp"`
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
// 含用量/按次（回填）"未回填），Hailuo 按次/档位计费且网关计费面只有
// 秒价维度——不填 OutputVideoSeconds，终态计费由链上按契约 §2.8 兜底
//（0 计费 + usage_missing 标记，不猜测）。
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

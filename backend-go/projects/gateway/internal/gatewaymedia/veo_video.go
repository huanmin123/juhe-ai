// veo_video.go 是 M3 异步视频的 Gemini Veo 出站 adapter（媒体设计 §5：
// 媒体 IR → 每厂商一个 adapter，注册表组织；本文件挂 videoAdapters 的
// "gemini" 条目）。报文依据契约 §5.2（置信度 B：多源确认）：创建
// POST /v1beta/models/{model}:predictLongRunning（body
// {instances,parameters}），受理凭据 = 响应 name 字段
//（"models/<model>/operations/<op_id>"）；轮询 GET /v1beta/{name} →
// operation 对象（done/error/response.generateVideoResponse.
// generatedSamples[].video.uri，uri 为 GCS 签名 URL 约 2 天时效、直连下载
// 无凭据）；取消 POST /v1beta/{name}:cancel。URL 前缀按账户 base_url +
// /v1beta 形态（链上层经 gatewaygemini.BuildUpstreamURL 归一，本 adapter
// 只产出带 /v1beta 前缀的相对路径）。纯逻辑层，不触 chain/HTTP 执行。
package gatewaymedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// veoVideoAdapter 实现 gemini（Veo 3 系）视频出站转换。
type veoVideoAdapter struct{}

func (veoVideoAdapter) Provider() string { return "gemini" }

// Capabilities 按 Veo predictLongRunning 请求面现状声明（契约 §2.2/§5.2）：
//   - SupportsNegativePrompt：parameters.negativePrompt 原生字段 → true；
//   - SupportsInputReference：instances[0].image.bytesBase64Encoded 承接
//     图生视频首帧 → true，但仅支持 base64/data URL 直传（url 形态不做下载，
//     零存储，Create 内 400）；
//   - SupportsSeconds/SupportsN/SupportsSeed/SupportsAudio：请求面无对应
//     字段 → false（忽略 + 回显 params_ignored，契约 §2.4 规则 1）。
func (veoVideoAdapter) Capabilities() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: true,
		SupportsSeed:           false,
		SupportsAudio:          false,
		SupportsN:              false,
		SupportsInputReference: true,
		SupportsSeconds:        false,
	}
}

// veoAspectRatioAndResolutionOf 把 size 像素串换算为 Veo 的 aspectRatio 与
// resolution 档（契约 §2.2/§2.7：size→宽高比/分辨率换算落在 adapter，
// 无精确档取最近档）。规则：
//   - aspectRatio：宽 ≥ 高按横屏 16:9，反之 9:16（Veo 3 系仅两档；方形等
//     无精确档的取值按横/竖判定收敛到 16:9）；
//   - resolution：短边 ≥1080 → 1080p，否则 720p（两档映射，无更低档，
//     低于 720p 的取值按最近档收敛到 720p）。
//
// size 形态已由 ParseVideoParams 的 isValidVideoSize 校验（WxH 正整数串），
// 此处解析失败属上游调用违约，报错而非猜测。
func veoAspectRatioAndResolutionOf(size string) (aspectRatio, resolution string, err error) {
	widthText, heightText, ok := strings.Cut(strings.TrimSpace(size), "x")
	if !ok {
		return "", "", fmt.Errorf("veo size 换算失败（非 WxH 形态）: %s", size)
	}
	width, widthErr := strconv.Atoi(widthText)
	height, heightErr := strconv.Atoi(heightText)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return "", "", fmt.Errorf("veo size 换算失败（非法宽高）: %s", size)
	}
	if width >= height {
		aspectRatio = "16:9"
	} else {
		aspectRatio = "9:16"
	}
	shortSide := height
	if width < height {
		shortSide = width
	}
	if shortSide >= 1080 {
		resolution = "1080p"
	} else {
		resolution = "720p"
	}
	return aspectRatio, resolution, nil
}

// veoInputReferenceBase64 归一图生视频首帧引用为 bytesBase64Encoded 载荷。
// 契约 §5.2/M3 首版裁决：仅支持 base64 data URL 与裸 base64 直传——网关零
// 资源存储（媒体设计 §8.1）不代为下载 image_url；url 形态返回参数错误
// （契约 §2.4 规则 2：语义无法表达，400 不降级）。
func veoInputReferenceBase64(reference string) (string, error) {
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
		return "", fmt.Errorf("%w: gemini 图生视频首帧仅支持 base64/data URL 直传，不支持 image_url（网关不代为下载）", ErrParamUnsupported)
	}
	return trimmed, nil
}

// Create 构造契约 §5.2 创建报文：POST
// /v1beta/models/{model}:predictLongRunning，body 为
// {instances:[{prompt,image?}],parameters:{aspectRatio?,resolution?,
// negativePrompt?}}。归一层判为 ignored 的词表外字段（seconds/n/seed/audio）
// 不进报文；size 换算为 aspectRatio+resolution（无精确档取最近档，契约
// §2.4 规则 3，实际档位以上游报文为准）；provider_options 命中 gemini 的
// 子对象 deep-merge 进顶层（厂商个例参数如经 {"gemini":{"parameters":
// {...}}} 覆盖换算结果，契约 §2.1 L3）。
func (a veoVideoAdapter) Create(_ context.Context, in VideoCreateInput) (VideoCreateOutput, error) {
	params := in.Params
	if params.Model == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 model")
	}
	if params.Prompt == "" {
		return VideoCreateOutput{}, fmt.Errorf("视频生成请求缺少 prompt")
	}
	instance := map[string]any{"prompt": params.Prompt}
	if params.InputReference != nil {
		base64Encoded, err := veoInputReferenceBase64(*params.InputReference)
		if err != nil {
			return VideoCreateOutput{}, err
		}
		instance["image"] = map[string]any{"bytesBase64Encoded": base64Encoded}
	}
	body := map[string]any{"instances": []any{instance}}
	parameters := map[string]any{}
	if params.Size != "" {
		aspectRatio, resolution, err := veoAspectRatioAndResolutionOf(params.Size)
		if err != nil {
			return VideoCreateOutput{}, err
		}
		parameters["aspectRatio"] = aspectRatio
		parameters["resolution"] = resolution
	}
	if params.NegativePrompt != "" {
		parameters["negativePrompt"] = params.NegativePrompt
	}
	if len(parameters) > 0 {
		body["parameters"] = parameters
	}
	body = MergeProviderOptions(body, a.Provider(), in.ProviderOptions)
	encoded, err := json.Marshal(body)
	if err != nil {
		return VideoCreateOutput{}, fmt.Errorf("编码 gemini 视频创建请求体失败: %w", err)
	}
	return VideoCreateOutput{
		Method: http.MethodPost,
		Path:   "/v1beta/models/" + params.Model + ":predictLongRunning",
		Body:   encoded,
	}, nil
}

// BuildPollRequest 构造轮询请求：GET /v1beta/{name}（契约 §5.2；name 即
// 创建响应的受理凭据 operation 名）。
func (veoVideoAdapter) BuildPollRequest(upstreamJobID string) (string, string, []byte) {
	return http.MethodGet, "/v1beta/" + upstreamJobID, nil
}

// ContentFromArtifact 声明 Veo 形态的产物下载定位：completed 的下载地址是
// 轮询冻结的绝对 GCS 签名 URL（Artifact.ContentURL，约 2 天时效、凭据内嵌
// 无需账户认证，契约 §5.2），不能由 operation 名推导——链上层直连该 URL，
// 不调用 BuildContentRequest。
func (veoVideoAdapter) ContentFromArtifact() bool { return true }

// BuildContentRequest 在 Veo 形态下不参与下载定位（ContentFromArtifact 为
// true，链上层直连 Artifact.ContentURL）；保留接口实现返回空路径，误调用
// 属链上层接线错误。
func (veoVideoAdapter) BuildContentRequest(string) (string, string) {
	return http.MethodGet, ""
}

// BuildCancelRequest 构造取消请求：POST /v1beta/{name}:cancel（Gemini
// operations 的标准取消方法，契约 §5.2/M3 裁决：上游不支持时链上层收敛
// 本地 cancelled）。
func (veoVideoAdapter) BuildCancelRequest(upstreamJobID string) (string, string) {
	return http.MethodPost, "/v1beta/" + upstreamJobID + ":cancel"
}

// SupportsCancel：Gemini operations 暴露 :cancel 取消方法（契约 §5.2）→
// true（上游 2xx/404/405 均由链上层收敛本地 cancelled）。
func (veoVideoAdapter) SupportsCancel() bool { return true }

// veoOperationObject 是契约 §5.2 operation 对象的消费面：创建响应仅携带
// name（受理凭据）；轮询响应携带 done + response.generateVideoResponse.
// generatedSamples[].video.uri 或 error。name 在轮询响应中可缺席（轮询方
// 已持有本地行），UpstreamJobID 仅在在场时回填。
type veoOperationObject struct {
	Name     string               `json:"name"`
	Done     bool                 `json:"done"`
	Response *veoOperationPayload `json:"response"`
	Error    *veoOperationError   `json:"error"`
}

// veoOperationPayload 是 done:true 成功形态的产物定位容器。
type veoOperationPayload struct {
	GenerateVideoResponse *struct {
		GeneratedSamples []struct {
			Video struct {
				URI string `json:"uri"`
			} `json:"video"`
		} `json:"generatedSamples"`
	} `json:"generateVideoResponse"`
}

// veoOperationError 是 done:true 失败形态的错误对象（code/message/status
// 三元组，Gemini 错误形态）。
type veoOperationError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// ParsePollResponse 把创建/轮询响应归一为 MediaJobIR（同一 operation 对象
// 形态，创建响应即 done 缺席的 operation）：
//   - 非 2xx → *UpstreamStatusError（链上层区分 404 收敛与 5xx 故障）；
//   - 未 done → queued（契约 §2.6 gemini 列：operation 未 done 且无进展
//     字段 → queued；Veo 无 progress 字段，in_progress 不可达）；
//   - done 且 error → failed（error 进终态摘要）；
//   - done 且 generatedSamples[].video.uri → completed（uri 进
//     Artifact.ContentURL）；done 且无 error 无 uri 属上游协议违约，报错
//     不猜测产物可达性。
//
// usage：Veo operation 响应不回报输出秒数，且 seconds 属 ignored 参数
// （Capabilities）不构成计量基源——不填 OutputVideoSeconds，终态计费由
// 链上按契约 §2.8 兜底（0 计费 + usage_missing 标记，不猜测）。
func (veoVideoAdapter) ParsePollResponse(statusCode int, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj veoOperationObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("gemini 视频任务响应不是有效 JSON: %w", err)
	}
	ir := &MediaJobIR{
		Kind:          JobKindVideo,
		UpstreamJobID: obj.Name,
	}
	if !obj.Done {
		ir.Status = JobStatusQueued
		return ir, nil
	}
	if obj.Error != nil && obj.Error.Message != "" {
		ir.Status = JobStatusFailed
		code := obj.Error.Status
		if code == "" {
			code = strconv.Itoa(obj.Error.Code)
		}
		ir.Error = &MediaJobError{Code: code, Message: obj.Error.Message}
		return ir, nil
	}
	uri := ""
	if obj.Response != nil && obj.Response.GenerateVideoResponse != nil {
		for _, sample := range obj.Response.GenerateVideoResponse.GeneratedSamples {
			if sample.Video.URI != "" {
				uri = sample.Video.URI
				break
			}
		}
	}
	if uri == "" {
		return nil, fmt.Errorf("gemini 视频任务 done 但缺少 generatedSamples 产物定位（name=%s）", obj.Name)
	}
	ir.Status = JobStatusCompleted
	ir.Artifact.ContentURL = uri
	return ir, nil
}

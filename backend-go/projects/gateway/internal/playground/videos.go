package playground

// POST /videos + 轮询/下载 —— 异步视频（设计 §3.5）：POST
// /v1/video/generations 创建（响应 {jobId}，服务端不落库，jobId 由前端持有）、
// GET /v1/video/generations/{jobId} 代理
// 上游轮询（归一 status/progress/error）、GET /v1/video/generations/{jobId}/content 流式
// 代理视频字节。请求构造是 internal/chat/generation_media.go CreateChatVideo/
// PollChatMediaTask/DownloadChatMediaContent 的同构精简版（该包有并行未提交
// 改动且将整体退役，不 import）。首帧图生视频经 /v1 链的 input_reference 通道
// （gatewaymedia.ParseVideoParams：image_url 或 base64 data URL 字符串）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// 视频限额（chat 面 chatMediaVideoMaxBytes 同口径 64MB；创建响应/轮询响应读
// 上限 1MiB；jobId 白名单 1~512 字节，videosJobIDPattern）。
const (
	videoBodyMaxBytes = 64 * 1024 * 1024
	videoMetaMaxBytes = 1<<20 + 1
)

// videosJobIDPattern 是前端持有的任务凭据白名单（字母数字与 ._-，1~512 字节）：
// 不合规直接 400，不进入派发路径。
var videosJobIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,512}$`)

// videosRequest 是 POST /videos 请求体（设计 §3.5）。
type videosRequest struct {
	APIKeyID         string   `json:"apiKeyId"`
	ModelID          string   `json:"modelId"`
	Prompt           string   `json:"prompt"`
	Size             string   `json:"size"`
	Seconds          *float64 `json:"seconds"`
	InputImageBase64 string   `json:"inputImageBase64"`
}

// videosCreateResponse 是创建受理响应（设计 §3.5）。
type videosCreateResponse struct {
	JobID string `json:"jobId"`
}

// videosPollResponse 是轮询归一响应（设计 §3.5：status 枚举
// queued|running|completed|failed|cancelled，progress/error 可选）。
type videosPollResponse struct {
	Status   string           `json:"status"`
	Progress *int64           `json:"progress,omitempty"`
	Error    *videosPollError `json:"error,omitempty"`
}

// videosPollError 是归一的任务错误（上游 code/message 原样透出）。
type videosPollError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// videosCreateHandler 服务 POST /videos。
func (rt *playgroundRoutes) videosCreateHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	// 任务引用落盘是 §3.8 暂存契约的一部分（前端刷新后凭 GET /artifacts 恢复
	// 轮询），暂存面未接线时显式降级。
	if !rt.artifactsReady() {
		writeHTTPError(w, artifactsUnavailable())
		return
	}
	body, err := readJSONBody(r, inputImageMaxBytes*2)
	if err != nil {
		writeHTTPError(w, invalidRequest("请求体读取失败或超过上限"))
		return
	}
	var req videosRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, invalidRequest("请求体必须是合法 JSON"))
		return
	}
	options, candidateErr := rt.resolveKeyModels(r.Context(), scope.ViewerID, strings.TrimSpace(req.APIKeyID))
	if candidateErr != nil {
		writeHTTPError(w, candidateErr)
		return
	}
	if modelOptionOf(options, strings.TrimSpace(req.ModelID), KindVideo) == nil {
		writeHTTPError(w, modelNotAvailable("模型不在所选 Key 的演练场视频候选内"))
		return
	}
	payload, httpErr := rt.runVideosCreate(r.Context(), scope, &req)
	if httpErr != nil {
		writeHTTPError(w, httpErr)
		return
	}
	writeOKPayload(w, payload)
}

// runVideosCreate 执行创建派发并把任务引用写入暂存（§3.8：视频产物本体不落盘
// ——上游内容链接有效期 24h，凭 jobId 可持续获取；引用单条覆盖，前端刷新后凭
// GET /artifacts 恢复轮询。测试断言请求构造经 Mock DispatchPort 回放 + 真实
// 落盘验证）。
func (rt *playgroundRoutes) runVideosCreate(ctx context.Context, scope Scope, req *videosRequest) (*videosCreateResponse, *HTTPError) {
	model := strings.TrimSpace(req.ModelID)
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, invalidRequest("prompt 不能为空")
	}
	payloadValues := map[string]any{"model": model, "prompt": prompt}
	if size := strings.TrimSpace(req.Size); size != "" {
		payloadValues["size"] = size
	}
	if req.Seconds != nil && *req.Seconds > 0 {
		payloadValues["seconds"] = *req.Seconds
	}
	if reference := strings.TrimSpace(req.InputImageBase64); reference != "" {
		// 首帧引用按 /v1 链 input_reference 通道透传（image_url 或 base64
		// data URL 字符串；裸 base64 补 data URL 前缀，MIME 按嗅探缺省 png）。
		if !strings.Contains(reference, ",") && !strings.HasPrefix(reference, "http") {
			decoded, err := decodeBase64Image(cleanBase64(reference))
			if err != nil {
				return nil, invalidRequest("首帧图 base64 无法解码或超过上限")
			}
			mime := sniffImageMIME(decoded)
			if mime == "" {
				mime = "image/png"
			}
			reference = "data:" + mime + ";base64," + cleanBase64(reference)
		}
		payloadValues["input_reference"] = reference
	}
	payload, _ := json.Marshal(payloadValues)
	response, err := rt.deps.Dispatch.Dispatch(ctx, DispatchRequest{
		APIKeyID: strings.TrimSpace(req.APIKeyID), OwnerID: scope.ViewerID, RemoteAddr: scope.RemoteAddr,
		Path: "/v1/video/generations", Method: http.MethodPost,
		Headers: map[string]string{
			"accept":       "application/json",
			"content-type": "application/json",
		},
		Body: payload,
	})
	if err != nil {
		return nil, dispatchError("视频创建派发失败", err)
	}
	if response == nil {
		return nil, dispatchFailed("视频创建派发失败")
	}
	respBody, readErr := io.ReadAll(io.LimitReader(response.Body, videoMetaMaxBytes))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, dispatchFailed("视频创建响应读取失败")
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, dispatchFailed(upstreamDispatchMessage("视频创建", response.Status, respBody))
	}
	var job struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &job); err != nil || strings.TrimSpace(job.TaskID) == "" {
		return nil, dispatchFailed("视频创建响应缺少任务凭据（task_id）")
	}
	// 任务引用随暂存记录（§3.8 单条覆盖）：上游有初始状态则归一记录，缺省
	// queued；存储失败显式 500（jobId 未交付，用户重试即可）。
	jobID := strings.TrimSpace(job.TaskID)
	status := strings.TrimSpace(job.Status)
	if status == "" {
		status = "queued"
	} else {
		status = normalizeVideoStatus(status)
	}
	ref := videoTaskRef{
		JobID:     jobID,
		APIKeyID:  strings.TrimSpace(req.APIKeyID),
		ModelID:   model,
		Status:    status,
		CreatedAt: time.Now().Unix(),
	}
	if err := rt.artifacts.replaceVideoTask(scope.ViewerID, ref); err != nil {
		return nil, artifactsError(err)
	}
	return &videosCreateResponse{JobID: jobID}, nil
}

// videosPollHandler 服务 GET /videos/{jobId}?apiKeyId=（代理上游轮询；轮询用
// 创建时 Key——凭据由前端持有，不默认取「当前页 Key」）。
func (rt *playgroundRoutes) videosPollHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	if !videosJobIDPattern.MatchString(jobID) {
		writeHTTPError(w, invalidRequest("jobId 无效"))
		return
	}
	apiKeyID := strings.TrimSpace(r.URL.Query().Get("apiKeyId"))
	if apiKeyID == "" {
		writeHTTPError(w, invalidRequest("apiKeyId 必填（轮询用创建时 Key，凭前端持有的任务凭据轮询）"))
		return
	}
	payload, httpErr := rt.runVideosPoll(r.Context(), scope, apiKeyID, jobID)
	if httpErr != nil {
		writeHTTPError(w, httpErr)
		return
	}
	writeOKPayload(w, payload)
}

// runVideosPoll 执行轮询派发并归一状态（真实 Key 走完整 /v1 链：任务归属按
// 认证 Key 隔离，跨用户 jobId 轮询由 /v1 链归属过滤拒绝；Key 归属 fail-closed
// 校验在派发适配层解密时收敛）。
func (rt *playgroundRoutes) runVideosPoll(ctx context.Context, scope Scope, apiKeyID, jobID string) (*videosPollResponse, *HTTPError) {
	response, err := rt.deps.Dispatch.Dispatch(ctx, DispatchRequest{
		APIKeyID: apiKeyID, OwnerID: scope.ViewerID, RemoteAddr: scope.RemoteAddr,
		Path: "/v1/video/generations/" + jobID, Method: http.MethodGet,
		Headers: map[string]string{
			"accept": "application/json",
		},
	})
	if err != nil {
		return nil, dispatchError("视频任务轮询派发失败", err)
	}
	if response == nil {
		return nil, dispatchFailed("视频任务轮询派发失败")
	}
	respBody, readErr := io.ReadAll(io.LimitReader(response.Body, videoMetaMaxBytes))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, dispatchFailed("视频任务响应读取失败")
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, dispatchFailed(upstreamDispatchMessage("视频任务轮询", response.Status, respBody))
	}
	var job struct {
		Status   string `json:"status"`
		Progress *int64 `json:"progress"`
		Error    *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &job); err != nil {
		return nil, dispatchFailed("视频任务响应解析失败")
	}
	rawStatus := strings.TrimSpace(job.Status)
	out := &videosPollResponse{Status: normalizeVideoStatus(rawStatus), Progress: job.Progress}
	if job.Error != nil {
		out.Error = &videosPollError{Code: strings.TrimSpace(job.Error.Code), Message: strings.TrimSpace(job.Error.Message)}
	} else if !videoStatusKnown(rawStatus) {
		// 未知状态原文不丢弃（设计 §3.5 归一注释）：按 failed 收敛的同时把
		// 原文透出到 error.message，排查时可见上游真实状态值。
		out.Error = &videosPollError{Message: strings.TrimSpace("未知状态 " + rawStatus)}
	}
	return out, nil
}

// videoStatusKnown 报告状态是否在归一词表内（词表外原文经 error.message 透出）。
func videoStatusKnown(status string) bool {
	switch status {
	case "queued", "completed", "cancelled", "in_progress", "failed", "expired":
		return true
	}
	return false
}

// normalizeVideoStatus 把 /v1 链的任务状态归一到设计 §3.5 枚举：in_progress→
// running；expired 视为 failed（终态失败族）；未知值按 failed 收敛，原文由
// 调用方写入 error.message（「未知状态 <原文>」）。
func normalizeVideoStatus(status string) string {
	switch status {
	case "queued", "completed", "cancelled":
		return status
	case "in_progress":
		return "running"
	default:
		return "failed"
	}
}

// videosContentHandler 服务 GET /videos/{jobId}/content?apiKeyId=（流式代理
// 视频字节，Content-Type 透传，Content-Disposition: inline）。
func (rt *playgroundRoutes) videosContentHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	if !videosJobIDPattern.MatchString(jobID) {
		writeHTTPError(w, invalidRequest("jobId 无效"))
		return
	}
	apiKeyID := strings.TrimSpace(r.URL.Query().Get("apiKeyId"))
	if apiKeyID == "" {
		writeHTTPError(w, invalidRequest("apiKeyId 必填（下载用创建时 Key，凭前端持有的任务凭据取流）"))
		return
	}
	response, err := rt.deps.Dispatch.Dispatch(r.Context(), DispatchRequest{
		APIKeyID: apiKeyID, OwnerID: scope.ViewerID, RemoteAddr: scope.RemoteAddr,
		Path: "/v1/video/generations/" + jobID + "/content", Method: http.MethodGet,
		Headers: map[string]string{
			// 二进制字节流端点：accept 用 */*（约束过窄会让按内容协商的上游
			// 路径拒绝回放视频字节）。
			"accept": "*/*",
		},
	})
	if err != nil {
		writeHTTPError(w, dispatchError("视频产物下载派发失败", err))
		return
	}
	if response == nil {
		writeHTTPError(w, dispatchFailed("视频产物下载派发失败"))
		return
	}
	defer response.Body.Close()
	if response.Status < 200 || response.Status >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, videoMetaMaxBytes))
		writeHTTPError(w, dispatchFailed(upstreamDispatchMessage("视频产物下载", response.Status, body)))
		return
	}
	// 字节流边取边回（任务中心 serveMediaJobContent 同模式）：Content-Type 透
	// 传（空则 octet-stream），inline 呈现；长度有界（64MB），超限中途断流。
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(response.Body, videoBodyMaxBytes+1))
}

package playground

// POST /speech —— 同步语音合成（设计 §3.6）：固定派发 POST /v1/audio/speech，
// 音频字节落服务端暂存（§3.8，暂存是展示缓存非资产库），响应回
// {id,url,mimeType} JSON——不再直接回字节流，前端 <audio src=url> 播放、下载
// 走 url。请求构造是 internal/chat/generation_media.go GenerateChatAudio 的
// 同构精简版（该包有并行未提交改动且将整体退役，不 import）：input≤2000 字、
// voice/format/speed 可选、有界读取 32MB、账户无 audio 候选时 400。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// 语音限额（设计 §3.6 + chat 面 chatMediaAudioMaxBytes 同口径）。
const (
	speechInputMaxRunes = 2000
	speechAudioMaxBytes = 32 * 1024 * 1024
)

// speechRequest 是 POST /speech 请求体（设计 §3.6）。
type speechRequest struct {
	AccountID string   `json:"accountId"`
	ModelID   string   `json:"modelId"`
	Input     string   `json:"input"`
	Voice     string   `json:"voice"`
	Speed     *float64 `json:"speed"`
	Format    string   `json:"format"`
}

// speechHandler 服务 POST /speech。
func (rt *playgroundRoutes) speechHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	// 音频落盘是本端点响应契约的一部分（§3.6 响应即 {id,url,mimeType}），暂存
	// 面未接线时显式降级，不派发不静默改行为。
	if !rt.artifacts.available() {
		writeHTTPError(w, artifactsUnavailable())
		return
	}
	body, err := readJSONBody(r, 256*1024)
	if err != nil {
		writeHTTPError(w, invalidRequest("请求体读取失败或超过上限"))
		return
	}
	var req speechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, invalidRequest("请求体必须是合法 JSON"))
		return
	}
	_, options, candidateErr := rt.resolveAccountModels(r.Context(), scope, strings.TrimSpace(req.AccountID))
	if candidateErr != nil {
		writeHTTPError(w, candidateErr)
		return
	}
	// options == nil = 账户不存在或不在当前用户可见范围：与 chatstream 同口径
	// 404 playground_account_not_found（区别于模型越界的 400）。
	if options == nil {
		writeHTTPError(w, accountNotFound())
		return
	}
	if modelOptionOf(options, strings.TrimSpace(req.ModelID), KindAudio) == nil {
		writeHTTPError(w, modelNotAvailable("模型不在该账户的演练场语音候选内"))
		return
	}
	payload, httpErr := rt.runSpeech(r.Context(), scope, &req)
	if httpErr != nil {
		writeHTTPError(w, httpErr)
		return
	}
	writeOKPayload(w, payload)
}

// speechResponse 是 POST /speech 响应（设计 §3.6 契约形状）。
type speechResponse struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	MimeType string `json:"mimeType"`
}

// runSpeech 执行 TTS 派发、音频落暂存并归一响应（mimeType 按请求 format 的扩
// 展名词表——与 GET artifacts 清单及 content 端点 Content-Type 同一来源）。
func (rt *playgroundRoutes) runSpeech(ctx context.Context, scope Scope, req *speechRequest) (*speechResponse, *HTTPError) {
	model := strings.TrimSpace(req.ModelID)
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return nil, invalidRequest("input 不能为空")
	}
	if len([]rune(input)) > speechInputMaxRunes {
		return nil, invalidRequest("input 超过 2000 字上限")
	}
	payloadValues := map[string]any{"model": model, "input": input}
	if voice := strings.TrimSpace(req.Voice); voice != "" {
		payloadValues["voice"] = voice
	}
	if audioFormat := normalizeSpeechFormat(req.Format); audioFormat != "" {
		payloadValues["response_format"] = audioFormat
	}
	if req.Speed != nil && *req.Speed > 0 {
		payloadValues["speed"] = *req.Speed
	}
	payload, _ := json.Marshal(payloadValues)
	response, err := rt.deps.Dispatch.Dispatch(ctx, DispatchRequest{
		AccountID: strings.TrimSpace(req.AccountID), OwnerID: scope.ViewerID,
		Path: "/v1/audio/speech", Method: http.MethodPost,
		Headers: map[string]string{
			"accept":         "application/json",
			"content-type":   "application/json",
			purposeHeaderKey: playgroundPurposeSpeech,
		},
		Body: payload,
	})
	if err != nil {
		return nil, dispatchFailed("语音合成派发失败: " + err.Error())
	}
	if response == nil {
		return nil, dispatchFailed("语音合成派发失败")
	}
	// 有界读取整段音频再落暂存（32MB 上限内完成，超限按派发失败收敛——演练
	// 场演示时长音频不至于触界）；非 2xx 读错误体走错误面。
	data, readErr := io.ReadAll(io.LimitReader(response.Body, speechAudioMaxBytes+1))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, dispatchFailed("语音合成响应读取失败")
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, dispatchFailed(upstreamDispatchMessage("语音合成", response.Status, data))
	}
	if len(data) > speechAudioMaxBytes {
		return nil, dispatchFailed("生成音频超过 32 MiB 上限")
	}
	// 落暂存（§3.8 同 kind 覆盖：删旧音频写新条）；扩展名取请求 format 词表
	// 归一值（缺省 mp3），mimeType 与 content 端点 Content-Type 同源。
	ext := speechArtifactExtension(req.Format)
	id, storeErr := rt.artifacts.replaceSpeech(scope.ViewerID, data, ext)
	if storeErr != nil {
		return nil, artifactsError(storeErr)
	}
	return &speechResponse{
		ID:       id,
		URL:      artifactContentURL(id),
		MimeType: artifactMIMEByExtension(ext),
	}, nil
}

// speechArtifactExtension 归一语音产物的暂存扩展名（normalizeSpeechFormat 词
// 表内四值；空/未识别按上游缺省 mp3 落档）。
func speechArtifactExtension(format string) string {
	if normalized := normalizeSpeechFormat(format); normalized != "" {
		return normalized
	}
	return "mp3"
}

// normalizeSpeechFormat 归一 TTS response_format（空 = 上游默认 mp3；仅放行
// 可落档的封装词表，chat 面 normalizeChatAudioFormat 同词表）。
func normalizeSpeechFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "mp3":
		return "mp3"
	case "wav", "pcm":
		return "wav"
	case "ogg":
		return "ogg"
	case "m4a":
		return "m4a"
	}
	return ""
}

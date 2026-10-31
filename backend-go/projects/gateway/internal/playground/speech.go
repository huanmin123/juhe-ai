package playground

// POST /speech —— 同步语音合成（设计 §3.6）：固定派发 POST /v1/audio/speech，
// 响应为音频字节流（Content-Type 透传，前端 <audio> 播放或下载）。请求构造
// 是 internal/chat/generation_media.go GenerateChatAudio 的同构精简版（该包
// 有并行未提交改动且将整体退役，不 import）：input≤2000 字、voice/format/
// speed 可选、有界读取 32MB、账户无 audio 候选时 400。

import (
	"bytes"
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
	if httpErr := rt.runSpeech(r.Context(), scope, &req, w); httpErr != nil {
		writeHTTPError(w, httpErr)
		return
	}
}

// runSpeech 执行 TTS 派发并回写音频字节流（响应头已提交后不再走错误面；上游
// 失败发生在头提交前时返回 HTTPError 由 handler 渲染）。
func (rt *playgroundRoutes) runSpeech(ctx context.Context, scope Scope, req *speechRequest, w http.ResponseWriter) *HTTPError {
	model := strings.TrimSpace(req.ModelID)
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return invalidRequest("input 不能为空")
	}
	if len([]rune(input)) > speechInputMaxRunes {
		return invalidRequest("input 超过 2000 字上限")
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
		return dispatchFailed("语音合成派发失败: " + err.Error())
	}
	if response == nil {
		return dispatchFailed("语音合成派发失败")
	}
	// 有界缓冲到 Content-Type 判定后再提交响应头：2xx 直接边取边回（透传上
	// 游 Content-Type，空缺按嗅探兜底）；非 2xx 读错误体走错误面（32MB 上限
	// 内完成，超限断流按派发失败收敛——演练场演示时长音频不至于触界）。
	data, readErr := io.ReadAll(io.LimitReader(response.Body, speechAudioMaxBytes+1))
	_ = response.Body.Close()
	if readErr != nil {
		return dispatchFailed("语音合成响应读取失败")
	}
	if response.Status < 200 || response.Status >= 300 {
		return dispatchFailed(upstreamDispatchMessage("语音合成", response.Status, data))
	}
	if len(data) > speechAudioMaxBytes {
		return dispatchFailed("生成音频超过 32 MiB 上限")
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" || strings.HasPrefix(contentType, "application/json") {
		// 上游缺头或把 JSON 错误体带回 2xx 时按字节嗅探兜底，避免前端拿到
		// 不可播放的 JSON Content-Type。
		if sniffed := sniffMediaMIME(data); sniffed != "" {
			contentType = sniffed
		} else if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return nil
}

// normalizeSpeechFormat 归一 TTS response_format（空 = 上游默认 mp3；仅放行
// 嗅探器可识别的封装，chat 面 normalizeChatAudioFormat 同词表）。
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

// sniffMediaMIME 按魔数嗅探音频/视频 MIME，未知格式返回空串（来源：
// internal/chat/generation_media.go chatMediaMimeTypeFromBytes，退役复制；
// 词表 audio/wav、audio/ogg、audio/mpeg、audio/mp4、video/webm、video/mp4）。
func sniffMediaMIME(data []byte) string {
	switch {
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return "audio/wav"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS")):
		return "audio/ogg"
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")):
		return "audio/mpeg"
	case len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return "audio/mpeg"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return "video/webm"
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")):
		brand := string(data[8:12])
		if brand == "M4A " || brand == "M4B " {
			return "audio/mp4"
		}
		return "video/mp4"
	}
	return ""
}

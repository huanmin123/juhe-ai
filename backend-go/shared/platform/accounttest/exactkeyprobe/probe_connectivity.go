package exactkeyprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

const mediaConnectivityProbeTaskID = "probe-connectivity-check"

// isMediaConnectivityMode 判定 J1 媒体连通性模式：这些模式不走真实生成/
// 创建端点，统一由 probeMediaConnectivity 构造只读/空体请求。
func isMediaConnectivityMode(mode string) bool {
	switch mode {
	case "images_json", "video_create", "audio_speech", "audio_job_get":
		return true
	}
	return false
}

// probeMediaConnectivity 是 J1 媒体账户的无副作用连通性探测：
//   - images_json：GET /models（证明 Key + Base URL，不生成图片）；
//   - video_create：GET 查询不存在的固定占位任务（不创建视频）；
//   - audio_speech：POST 空文本/空体 TTS（不生成音频）。
//
// 2xx 与参数校验拒绝（400/422）视为 Key 有效；视频任务不存在（400/404）
// 也视为 Key 有效；401/403 为凭据失败；5xx/传输错误按既有分类。
func probeMediaConnectivity(ctx context.Context, client *http.Client, baseURL *url.URL, input Input, token string, options ProbeOptions) ProbeResult {
	method := http.MethodGet
	opPath := "/models"
	rawQuery := ""
	notFoundMeansReachable := false
	var body []byte
	extraHeaders := map[string]string{}
	switch input.EndpointMode {
	case "images_json":
		opPath, rawQuery, notFoundMeansReachable = mediaImagePath(input.Provider)
	case "video_create":
		method = http.MethodGet
		opPath, rawQuery = mediaVideoPath(input.Provider, input.HealthModel)
	case "audio_job_get":
		method = http.MethodGet
		opPath = mediaAudioJobPath(input.Provider)
	case "audio_speech":
		method = http.MethodPost
		var err error
		opPath, body, extraHeaders, err = mediaSpeechRequest(input, baseURL)
		if err != nil {
			return taskFailure("credential_unavailable", err.Error())
		}
	}
	target, err := upstreamenvelope.JoinUpstreamURL(baseURL.String(), opPath)
	if err != nil {
		return taskFailure("request_build_failed", err.Error())
	}
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return taskFailure("request_build_failed", err.Error())
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	if apiKey := strings.TrimSpace(extraHeaders["X-Api-Key"]); apiKey != "" {
		request.Header.Set("X-Api-Key", apiKey)
	} else if strings.EqualFold(strings.TrimSpace(input.Provider), "gemini") && input.Type != "google_oauth" {
		request.Header.Set("x-goog-api-key", strings.TrimSpace(token))
	} else {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	if strings.EqualFold(strings.TrimSpace(input.Provider), "gemini") && input.Type == "google_oauth" {
		if quotaProject := strings.TrimSpace(input.OAuthQuotaProjectID); quotaProject != "" {
			request.Header.Set("x-goog-user-project", quotaProject)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return transportFailure(err)
	}
	defer response.Body.Close()
	maxBytes := options.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBodyBytes
	}
	body, readErr := upstreamhttp.ReadBoundedPartial(response.Body, maxBytes)
	if response.StatusCode == http.StatusNotFound && input.EndpointMode == "images_json" && !notFoundMeansReachable {
		return httpFailure(response.StatusCode, "upstream_endpoint_unsupported", "上游未提供 /models 连通性端点")
	}
	success := response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
	switch input.EndpointMode {
	case "images_json":
		success = success || (notFoundMeansReachable && response.StatusCode == http.StatusNotFound)
	case "video_create":
		success = success || response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusNotFound
	case "audio_job_get":
		success = success || response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusNotFound
	case "audio_speech":
		success = success || response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity
	}
	// 火山方舟 403/45000030 是缺 resource_id（已过鉴权），不能当凭据无效；
	// 400/45001116 反而已过鉴权。按错误体补正，避免把健康账户判失败。
	if !success && strings.EqualFold(strings.TrimSpace(input.Provider), "volcengine") && response.StatusCode == http.StatusForbidden {
		bodyText := string(body)
		if strings.Contains(bodyText, "45000030") || strings.Contains(bodyText, "resource_id") {
			success = true
		}
	}
	if success {
		if readErr != nil {
			return responseReadFailure(readErr)
		}
		return ProbeResult{Outcome: OutcomeSuccess, StatusCode: response.StatusCode}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return httpFailure(response.StatusCode, "upstream_http_status", "上游拒绝访问")
	}
	if response.StatusCode >= http.StatusInternalServerError {
		return httpFailure(response.StatusCode, "upstream_http_status", "上游服务不可用")
	}
	return httpFailure(response.StatusCode, "upstream_http_status", fmt.Sprintf("上游返回 HTTP %d", response.StatusCode))
}

func mediaImagePath(provider string) (path, rawQuery string, notFoundMeansReachable bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "volcengine":
		// Ark 图像账户与视频共用任务面；固定占位任务查询是只读且不产生费用。
		return "/contents/generations/tasks/" + mediaConnectivityProbeTaskID, "", true
	case "qwen":
		// DashScope 任务查询同理；qwen_dashscope 的 base_url 是 origin。
		return "/api/v1/tasks/" + mediaConnectivityProbeTaskID, "", true
	default:
		return "/models", "", false
	}
}

func mediaVideoPath(provider, model string) (path, rawQuery string) {
	id := mediaConnectivityProbeTaskID
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "volcengine":
		return "/contents/generations/tasks/" + id, ""
	case "qwen":
		return "/api/v1/tasks/" + id, ""
	case "minimax":
		return "/query/video_generation", "task_id=" + url.QueryEscape(id)
	case "glm":
		return "/async-result/" + id, ""
	case "gemini":
		return "/models/" + url.PathEscape(strings.TrimSpace(model)) + "/operations/" + id, ""
	default:
		return "/videos/" + id, ""
	}
}

func mediaAudioJobPath(provider string) string {
	id := mediaConnectivityProbeTaskID
	if strings.EqualFold(strings.TrimSpace(provider), "qwen") {
		return "/api/v1/tasks/" + id
	}
	return "/audio/jobs/" + id
}

func mediaSpeechRequest(input Input, baseURL *url.URL) (path string, body []byte, extraHeaders map[string]string, err error) {
	model := strings.TrimSpace(input.HealthModel)
	switch strings.ToLower(strings.TrimSpace(input.Provider)) {
	case "gemini":
		// Gemini 原生 TTS 走 generateContent + AUDIO 模态；空文本由上游参数
		// 校验拒绝，不产生音频产物。
		encoded, marshalErr := json.Marshal(map[string]any{
			"contents":         []any{map[string]any{"parts": []any{map[string]any{"text": ""}}}},
			"generationConfig": map[string]any{"responseModalities": []string{"AUDIO"}},
		})
		if marshalErr != nil {
			return "", nil, nil, marshalErr
		}
		return "/models/" + url.PathEscape(model) + ":generateContent", encoded, nil, nil
	case "volcengine":
		// openspeech 是独立语音域，凭据是 speech_token + X-Api-Key；ark 的
		// OpenAI 兼容 TTS 走 /audio/speech + Bearer。
		if strings.Contains(strings.ToLower(baseURL.Hostname()), "openspeech") {
			if strings.TrimSpace(input.SpeechToken) == "" {
				return "", nil, nil, fmt.Errorf("火山语音账户缺少 speech_token 凭据")
			}
			encoded, marshalErr := json.Marshal(map[string]string{"model": model})
			if marshalErr != nil {
				return "", nil, nil, marshalErr
			}
			return "/api/v3/tts/create", encoded, map[string]string{"X-Api-Key": input.SpeechToken}, nil
		}
		encoded, marshalErr := json.Marshal(map[string]string{"model": model})
		if marshalErr != nil {
			return "", nil, nil, marshalErr
		}
		return "/audio/speech", encoded, nil, nil
	case "minimax":
		encoded, marshalErr := json.Marshal(map[string]string{"model": model, "text": ""})
		if marshalErr != nil {
			return "", nil, nil, marshalErr
		}
		return "/t2a_v2", encoded, nil, nil
	default:
		encoded, marshalErr := json.Marshal(map[string]string{"model": model})
		if marshalErr != nil {
			return "", nil, nil, marshalErr
		}
		return "/audio/speech", encoded, nil, nil
	}
}

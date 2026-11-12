// 媒体域密钥连通性探测（2026-11-01 用户裁决，AI账户检查模型与人工测试设计
// 「媒体域连通性探测」节）：video_create / images_json / audio_speech 三种
// 媒体模式的人工测试统一改为连通性探测——只验证 API key 能否访问上游，不真实
// 生成产物（视频避免真实计费任务，图像/语音避免真实消耗）。拦截点在
// probe.go executeAttempt 的模式分支（resolveProbeView 之后、buildTestRequest
// 之前）；本文件承担三模式的请求构造、发送与按状态码分类。
//
// 请求经既有出站通道：账户 base_url（版本根语义 R1/R7，经
// upstreamenvelope.JoinUpstreamURL 与信封 Build 同规则直拼）、账户凭据
// （upstreamenvelope.Auth.Apply 与 Build 同头部规则）、账户代理（与真实探针
// 同一 SharedClient 路径）、系统客户端身份头同源。
package accountprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// ConnectivityProbeTaskID 是 video_create 连通性探测的固定占位任务 ID：查询
// 一个必然不存在的视频任务来验证密钥与端点可达，绝不触碰创建端点（视频任务
// 创建会产生真实计费）。
const ConnectivityProbeTaskID = "probe-connectivity-check"

// isConnectivityProbeMode 判定媒体域连通性探测模式。audio_transcription_json
// 不在裁决范围（STT 探测保持内置 WAV 样本真实请求）；audio_job_create 永不
// POST，长音频账户统一用 audio_job_get 查询不存在任务。
func isConnectivityProbeMode(mode EndpointMode) bool {
	return mode == ModeVideoCreate || mode == ModeImagesJSON || mode == ModeAudioSpeech || mode == ModeAudioJobGet
}

// connectivityExpectedRejectionCodes 返回各模式的「预期拒绝 = 密钥有效」状态
// 码集：上游完成认证后因请求不完整或目标不存在而拒绝，即证明密钥有效——
//   - video_create：查询固定占位任务，404/400 = 任务不存在属预期；
//   - images_json / audio_speech：空 prompt / 空体被参数校验拒绝，400/422 属预期。
//
// 2xx 对三种模式均视为通过（极少数上游对探测请求直接放行也接受）。
func connectivityExpectedRejectionCodes(mode EndpointMode) map[int]bool {
	if mode == ModeVideoCreate || mode == ModeAudioJobGet {
		return map[int]bool{http.StatusBadRequest: true, http.StatusNotFound: true}
	}
	return map[int]bool{http.StatusBadRequest: true, http.StatusUnprocessableEntity: true}
}

// executeConnectivityAttempt 发起一次媒体域连通性请求并按状态码分类。分级
// 阶梯、Key 池调度与 limited 脱敏沿用既有 attemptWithTimeout 链路。
func (s *Service) executeConnectivityAttempt(ctx context.Context, view *View, mode EndpointMode, model, apiKey string, timeout time.Duration, limited bool) (*accountquality.ProbeObservation, error) {
	httpReq, buildErr := buildConnectivityRequest(ctx, view, mode, model, apiKey)
	if buildErr != nil {
		observation := classifyAttemptError(view, buildErr, timeout, limited, s.nowMS())
		observation.Evidence.Connectivity = true
		return observation, nil
	}
	if targetErr := s.validateProbeTarget(ctx, httpReq); targetErr != nil {
		observation := classifyAttemptError(view, targetErr, timeout, limited, s.nowMS())
		observation.Evidence.Connectivity = true
		return observation, nil
	}
	client := s.client
	if strings.TrimSpace(view.ProxyURL) != "" {
		var err error
		client, err = upstreamhttp.SharedClient(view.ProxyURL, upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeout, DialGuard: s.dialGuard})
		if err != nil {
			observation := classifyAttemptError(view, err, timeout, limited, s.nowMS())
			observation.Evidence.Connectivity = true
			return observation, nil
		}
	}
	startedAt := s.now()
	response, readErr := s.readUpstreamWithClient(ctx, client, httpReq, timeout, nil)
	durationMS := s.now().Sub(startedAt).Milliseconds()
	if response == nil {
		// 传输层失败分类与真实探针同口径（Node upstreamRequestFailureKind）。
		failure := transportFailureFromError(readErr, timeout)
		message := failure.message(readErr)
		if limited {
			message = limitedFailureMessage(0, failure.errorCode(), message)
		}
		return &accountquality.ProbeObservation{
			Result: accountquality.ProbeResult{
				Success:      false,
				ErrorCode:    failure.errorCode(),
				Message:      message,
				ProtocolCode: string(protocolCodeForView(view)),
				DurationMs:   durationMS,
				TraceID:      newTraceID(),
			},
			Evidence: accountquality.ProbeEvidence{
				HasRealUpstreamAttempt: true,
				TransportFailureKind:   failure.kind,
				TimedOut:               failure.timedOut,
				Connectivity:           true,
			},
		}, nil
	}
	return classifyConnectivityResponse(view, mode, response, readErr, durationMS, limited), nil
}

// videoConnectivityPath 返回各 vendor 的“查询不存在任务”GET 路径。所有分支
// 都只查询固定占位任务，不触碰创建端点；path 由 base_url 版本根拼接。
func videoConnectivityPath(provider, model string) (path, rawQuery string) {
	id := ConnectivityProbeTaskID
	switch provider {
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
		// OpenAI 官方视频任务面（gpt/xai OpenAI 兼容）。
		return "/videos/" + id, ""
	}
}

// audioJobConnectivityPath 返回长音频任务查询路径：qwen DashScope 与视频
// 任务共用 /api/v1/tasks/{id}；其余 OpenAI 兼容任务面用 /audio/jobs/{id}。
func audioJobConnectivityPath(provider string) string {
	id := ConnectivityProbeTaskID
	if provider == "qwen" {
		return "/api/v1/tasks/" + id
	}
	return "/audio/jobs/" + id
}

// buildConnectivityRequest 按 2026-11-01 裁决构造媒体域连通性请求：
//   - video_create：GET {base}/contents/generations/tasks/{ConnectivityProbeTaskID}
//     （volcengine_ark 预设模板的任务查询形态，绝不 POST 创建端点）；
//   - images_json：POST {base}/images/generations，body 仅 {"model":<model>}
//     （空 prompt；上游参数校验拒绝即判密钥有效）；
//   - audio_speech：POST {base}/tts/create（volcengine_openspeech 模板），空体。
//
// volcengineSpeechBaseURL 是火山语音域固定上游：与账户 ark base_url 无关，
// host 恒 openspeech.bytedance.com、路径 /api/v3/tts/create、鉴权
// X-Api-Key: speech_token（与 ark 的 Bearer 凭据体系独立，见火山方舟账号
// 接入「语音合成」节）。变量形态供测试注入 mock host。
var volcengineSpeechBaseURL = "https://openspeech.bytedance.com"

const volcengineSpeechCreatePath = "/api/v3/tts/create"

func buildConnectivityRequest(ctx context.Context, view *View, mode EndpointMode, model, apiKey string) (*http.Request, error) {
	baseURL := strings.TrimSpace(view.BaseURL)
	provider := strings.ToLower(strings.TrimSpace(view.ProviderCode))
	// volcengine 的 audio_speech 走独立语音域，不读账户 base_url（ark 版本根）。
	volcengineSpeech := mode == ModeAudioSpeech && provider == "volcengine"
	if baseURL == "" && !volcengineSpeech {
		return nil, errors.New("账户缺少上游 base_url")
	}
	var (
		method   string
		opPath   string
		body     []byte
		rawQuery string
	)
	switch mode {
	case ModeVideoCreate:
		method = http.MethodGet
		opPath, rawQuery = videoConnectivityPath(provider, model)
	case ModeAudioJobGet:
		method = http.MethodGet
		opPath = audioJobConnectivityPath(provider)
	case ModeImagesJSON:
		method = http.MethodPost
		if provider == "qwen" {
			opPath = "/api/v1/services/aigc/multimodal-generation/generation"
			encoded, err := json.Marshal(map[string]any{
				"model": model,
				"input": map[string]any{
					"messages": []any{map[string]any{
						"role":    "user",
						"content": []any{map[string]any{"text": ""}},
					}},
				},
				"parameters": map[string]any{},
			})
			if err != nil {
				return nil, fmt.Errorf("编码 qwen 图像连通性探测请求体失败: %w", err)
			}
			body = encoded
			break
		}
		opPath = "/images/generations"
		encoded, err := json.Marshal(map[string]string{"model": model})
		if err != nil {
			return nil, fmt.Errorf("编码连通性探测请求体失败: %w", err)
		}
		body = encoded
	case ModeAudioSpeech:
		method = http.MethodPost
		switch {
		case volcengineSpeech:
			opPath = "/api/v3/tts/create"
			encoded, err := json.Marshal(map[string]string{"model": model})
			if err != nil {
				return nil, fmt.Errorf("编码连通性探测请求体失败: %w", err)
			}
			body = encoded
		case provider == "gemini":
			opPath = "/models/" + strings.TrimSpace(model) + ":generateContent"
			encoded, err := json.Marshal(map[string]any{
				"contents":         []any{map[string]any{"parts": []any{map[string]any{"text": ""}}}},
				"generationConfig": map[string]any{"responseModalities": []string{"AUDIO"}},
			})
			if err != nil {
				return nil, fmt.Errorf("编码 gemini 音频连通性探测请求体失败: %w", err)
			}
			body = encoded
		case provider == "minimax":
			opPath = "/t2a_v2"
			encoded, err := json.Marshal(map[string]string{"model": model, "text": ""})
			if err != nil {
				return nil, fmt.Errorf("编码 minimax 音频连通性探测请求体失败: %w", err)
			}
			body = encoded
		default:
			// OpenAI/GLM/xAI 的同步 TTS 形态。
			opPath = "/audio/speech"
			encoded, err := json.Marshal(map[string]string{"model": model})
			if err != nil {
				return nil, fmt.Errorf("编码连通性探测请求体失败: %w", err)
			}
			body = encoded
		}
	default:
		return nil, fmt.Errorf("未知的连通性探测模式：%s", mode)
	}
	var target string
	var err error
	if volcengineSpeech {
		target, err = upstreamenvelope.JoinUpstreamURL(volcengineSpeechBaseURL, volcengineSpeechCreatePath)
	} else {
		target, err = upstreamenvelope.JoinUpstreamURL(baseURL, opPath)
		if err == nil && rawQuery != "" {
			target += "?" + rawQuery
		}
	}
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	// 认证策略与伴随头同 buildProbeSpec 口径（信封统一设计：认证字段随账户
	// 形态变化，凭据由调用方解析后传入）。
	auth := upstreamenvelope.Auth{Strategy: upstreamAuthStrategy(view), Token: apiKey, ExtraHeaders: map[string]string{}}
	if view.Type == "oauth" && !isAnthropicProtocol(view) && !isGeminiProtocol(view) &&
		(view.ProviderProtocolProfileID == "" || view.ProviderProtocolProfileID == "profile_gpt_openai_v1") {
		if accountID := firstNonEmpty(credentialText(view.Credentials, "account_id"), credentialText(view.Credentials, "chatgpt_user_id")); accountID != "" {
			auth.ExtraHeaders["chatgpt-account-id"] = accountID
		}
	}
	if isGeminiProtocol(view) && view.Type == "google_oauth" {
		if quotaProject := credentialText(view.Credentials, "quota_project_id"); quotaProject != "" {
			auth.ExtraHeaders["x-goog-user-project"] = quotaProject
		}
	}
	if volcengineSpeech {
		// 语音域凭据是 speech_token（新版语音控制台 API Key），与 ark api_key
		// 相互独立；缺失即该账户无语音能力，显式失败不静默回退。
		token := strings.TrimSpace(credentialText(view.Credentials, "speech_token"))
		if token == "" {
			return nil, errors.New("火山方舟语音账户缺少 speech_token 凭据")
		}
		request.Header.Set("X-Api-Key", token)
	} else if err := auth.Apply(request.Header); err != nil {
		return nil, err
	}
	// 系统客户端身份头与真实探针同源（A/B 形态不存在于媒体模式，本调用不会
	// 覆盖已写的认证头）。
	upstreamidentity.ApplySystemClientHeaders(request.Header, upstreamidentity.Input{
		ProviderCode:              view.ProviderCode,
		ProviderProtocolProfileID: view.ProviderProtocolProfileID,
		CredentialType:            view.Type,
		OAuthType:                 credentialText(view.Credentials, "oauth_type"),
		UpstreamHostname:          request.URL.Hostname(),
	})
	return request, nil
}

// connectivityVolcengineAuthPassed 处理火山方舟 403/45000030（缺 resource_id，
// 已过鉴权）与 400/45001116（参数校验前已过鉴权）的误判：响应体命中这些
// 业务码时视为 Key 可达，而不是凭据无效。
func connectivityVolcengineAuthPassed(view *View, statusCode int, body string) bool {
	if view == nil || !strings.EqualFold(strings.TrimSpace(view.ProviderCode), "volcengine") {
		return false
	}
	if statusCode != http.StatusForbidden {
		return false
	}
	return strings.Contains(body, "45000030") || strings.Contains(body, "resource_id")
}

// classifyConnectivityResponse 按裁决分类表判定连通性结果：
//   - 通过（2xx 或各模式预期拒绝码）：「密钥连通性验证通过（上游 HTTP <code>）」；
//   - 401/403：密钥无效，失败；
//   - 5xx：上游不可达，失败；
//   - 其余状态码：失败。
//
// 失败文案带状态码与脱敏上游消息（bounded 预览，同真实探针的响应体上限口径）。
func classifyConnectivityResponse(view *View, mode EndpointMode, response *upstreamResponse, readErr error, durationMS int64, limited bool) *accountquality.ProbeObservation {
	protocol := DiagnosticProtocol(protocolCodeForView(view))
	statusCode := response.status
	upstreamErrorCode := parseUpstreamErrorCodeFromBody(response.bodyText)
	success := (statusCode >= 200 && statusCode < 300) || connectivityExpectedRejectionCodes(mode)[statusCode] ||
		connectivityVolcengineAuthPassed(view, statusCode, response.bodyText)
	message := ""
	errorCode := ""
	switch {
	case success:
		message = fmt.Sprintf("密钥连通性验证通过（上游 HTTP %d）", statusCode)
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		message = fmt.Sprintf("密钥连通性验证失败：上游拒绝访问（HTTP %d）", statusCode)
		errorCode = upstreamErrorCode
	case statusCode >= 500:
		message = fmt.Sprintf("密钥连通性验证失败：上游服务不可用（HTTP %d）", statusCode)
		errorCode = upstreamErrorCode
	default:
		message = fmt.Sprintf("密钥连通性验证失败（HTTP %d）", statusCode)
		errorCode = upstreamErrorCode
	}
	if !success {
		if upstreamMessage := parseUpstreamMessage(response.bodyText, protocol, true); upstreamMessage != "" {
			message += "：" + upstreamMessage
		}
		if limited {
			// limited 诊断下的失败文案脱敏与真实探针同一规则。
			message = limitedFailureMessage(statusCode, errorCode, searchableText(message, response.bodyText))
		}
	}
	// 连通性判活依据是收到的 HTTP 状态码本身：framing 尾部中断不覆盖语义
	// 成功（与 executeAttempt 的尾部传输错误口径一致）。
	completed := readErr == nil || success
	result := accountquality.ProbeResult{
		Success:          success,
		StatusCode:       &statusCode,
		ErrorCode:        errorCode,
		Message:          message,
		DurationMs:       durationMS,
		ProtocolCode:     string(protocol),
		ResponseBodyText: response.bodyText,
	}
	if limited {
		if accountquality.SystemInsufficientQuotaRuleMatches(statusCode, errorCode, "", searchableText(message, response.bodyText)) {
			result.ResponseHeaders = quotaResponseHeaders(response.headers)
		}
	} else {
		result.ResponseHeaders = response.headers
	}
	return &accountquality.ProbeObservation{
		Result: result,
		Evidence: accountquality.ProbeEvidence{
			HasRealUpstreamAttempt: true,
			UpstreamCompleted:      completed,
			UpstreamStatus:         statusCode,
			Connectivity:           true,
		},
	}
}

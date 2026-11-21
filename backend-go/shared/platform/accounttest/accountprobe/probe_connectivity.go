// 账户连接测试（连通性探测）执行层。
//
// 2026-11-01 起媒体域人工测试只验证 API key 能否访问上游，不真实生成产物；
// 2026-11-21 起该口径扩展到全部「非对话能力账户」（图像 / 音频 / 视频，以及只
// 声明 embeddings / rerank / embed_content / count_tokens 的账户），并由
// contracts 连接器注册表提供每个 preset × endpoint mode 的真实探测路径。
// 本文件只负责按规格构造请求、发送并按状态码分类；模式选择、Key 池调度、
// 代理、身份头、分级超时与脱敏沿用 probe.go 既有链路。
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

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// ConnectivityProbeTaskID 是任务查询类连通性探测的固定占位任务 ID：查询一个
// 必然不存在的任务来验证密钥与端点可达，绝不触碰创建端点（媒体任务创建会
// 产生真实计费）。
const ConnectivityProbeTaskID = "probe-connectivity-check"

// ConnectivityProbeModeForView 返回账户可用的连接测试形态：优先使用视图
// 已声明形态中按注册表规格优先级命中的第一个；视图没有显式形态时回落到
// health_check_endpoint_mode。找不到规格时 fail closed。
func ConnectivityProbeModeForView(view *View) (EndpointMode, contracts.ConnectorConnectivityProbe, bool) {
	if view == nil {
		return "", contracts.ConnectorConnectivityProbe{}, false
	}
	modes := make([]string, 0, len(view.NormalizeEndpointModes))
	for mode := range view.NormalizeEndpointModes {
		modes = append(modes, string(mode))
	}
	if len(modes) == 0 {
		if mode := strings.TrimSpace(view.HealthCheckEndpointMode); mode != "" {
			modes = append(modes, mode)
		}
	}
	mode, spec, ok := contracts.ConnectivityProbeForModes(view.ConnectionPresetCode, modes)
	if !ok {
		return "", contracts.ConnectorConnectivityProbe{}, false
	}
	return EndpointMode(mode), spec, true
}

// isConnectivityProbeMode 判定本次 attempt 是否走连接测试分支：
//   - 视图显式标记 ConnectivityOnly（人工/自动连接测试入口）；
//   - 或该 preset 对当前 endpoint mode 登记了连通性探测规格。
func isConnectivityProbeMode(view *View, mode EndpointMode) bool {
	if view != nil && view.ConnectivityOnly {
		return true
	}
	if view == nil || strings.TrimSpace(view.ConnectionPresetCode) == "" {
		return false
	}
	_, ok := contracts.ConnectivityProbeForPreset(view.ConnectionPresetCode, string(mode))
	return ok
}

// executeConnectivityAttempt 发起一次连接测试请求并按状态码分类。分级阶梯、
// Key 池调度与 limited 脱敏沿用既有 attemptWithTimeout 链路。
func (s *Service) executeConnectivityAttempt(ctx context.Context, view *View, mode EndpointMode, model, apiKey string, timeout time.Duration, limited bool) (*accountquality.ProbeObservation, error) {
	spec, ok := contracts.ConnectivityProbeForPreset(view.ConnectionPresetCode, string(mode))
	if !ok {
		return nil, fmt.Errorf("连接预设 %q 未登记连通性探测形态 %q", view.ConnectionPresetCode, mode)
	}
	httpReq, buildErr := buildConnectivityRequest(ctx, view, spec, model, apiKey)
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
	return classifyConnectivityResponse(view, spec, response, readErr, durationMS, limited), nil
}

// buildConnectivityRequest 按注册表规格构造连接测试请求：
//   - 探测路径来自该 preset 生产适配器已证实的路径（operation 模板优先，
//     固定语音域/版本前缀属已登记例外），禁止跨供应商硬编码；
//   - 占位任务 ID 只用于任务查询，绝不 POST 创建端点；
//   - 请求体只允许 none / empty_json / model_only 三种最小形态；
//   - 认证沿用账户凭据策略；规格显式指定认证头时（火山 openspeech
//     speech_token）从凭据读取对应键，缺失显式失败。
func buildConnectivityRequest(ctx context.Context, view *View, spec contracts.ConnectorConnectivityProbe, model, apiKey string) (*http.Request, error) {
	baseURL := strings.TrimSpace(spec.BaseURLOverride)
	if baseURL == "" {
		baseURL = strings.TrimSpace(view.BaseURL)
	}
	if baseURL == "" {
		return nil, errors.New("账户缺少上游 base_url")
	}
	opPath := strings.ReplaceAll(spec.PathTemplate, "{probe_id}", ConnectivityProbeTaskID)
	opPath = strings.ReplaceAll(opPath, "{model}", connectivityProbeModelPath(model))
	target, err := upstreamenvelope.JoinUpstreamURL(baseURL, opPath)
	if err != nil {
		return nil, err
	}
	if query := strings.TrimSpace(spec.QueryTemplate); query != "" {
		query = strings.ReplaceAll(query, "{probe_id}", ConnectivityProbeTaskID)
		target += "?" + query
		// JoinUpstreamURL 已丢弃 base 的 query/fragment；这里只拼接本次探测
		// 的 query，若规格包含非法字符由 http.NewRequest 兜底报错。
	}
	var body []byte
	switch spec.BodyKind {
	case contracts.ConnectivityBodyNone:
	case contracts.ConnectivityBodyEmptyJSON:
		body = []byte("{}")
	case contracts.ConnectivityBodyModelOnly:
		encoded, err := json.Marshal(map[string]string{"model": model})
		if err != nil {
			return nil, fmt.Errorf("编码连接测试请求体失败: %w", err)
		}
		body = encoded
	default:
		return nil, fmt.Errorf("未知的连接测试请求体形态：%s", spec.BodyKind)
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, spec.Method, target, reader)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if spec.AuthHeaderName != "" || spec.AuthTokenCredentialKey != "" {
		if spec.AuthHeaderName == "" || spec.AuthTokenCredentialKey == "" {
			return nil, errors.New("连接测试认证规格不完整")
		}
		token := strings.TrimSpace(credentialText(view.Credentials, spec.AuthTokenCredentialKey))
		if token == "" {
			return nil, fmt.Errorf("账户缺少连接测试凭据：%s", spec.AuthTokenCredentialKey)
		}
		request.Header.Set(spec.AuthHeaderName, token)
	} else {
		// 认证策略与伴随头同 buildProbeSpec 口径（信封统一设计：认证字段随
		// 账户形态变化，凭据由调用方解析后传入）。
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
		if err := auth.Apply(request.Header); err != nil {
			return nil, err
		}
	}
	// 规格声明的协议必需头（火山 AUC 资源/请求/序号三件套）在认证之后
	// 写入；占位符替换口径与路径一致，{uuid} 每次请求生成独立 RFC4122 v4。
	for name, value := range spec.ExtraHeaders {
		value = strings.ReplaceAll(value, "{probe_id}", ConnectivityProbeTaskID)
		value = strings.ReplaceAll(value, "{model}", connectivityProbeModelPath(model))
		value = strings.ReplaceAll(value, "{uuid}", newUUID())
		request.Header.Set(name, value)
	}
	// 系统客户端身份头与真实探针同源；连接测试不覆盖已写的认证头。
	upstreamidentity.ApplySystemClientHeaders(request.Header, upstreamidentity.Input{
		ProviderCode:              view.ProviderCode,
		ProviderProtocolProfileID: view.ProviderProtocolProfileID,
		CredentialType:            view.Type,
		OAuthType:                 credentialText(view.Credentials, "oauth_type"),
		UpstreamHostname:          request.URL.Hostname(),
	})
	return request, nil
}

// connectivityProbeModelPath 把模型名转成路径占位值；Gemini 目录常带
// "models/" 前缀，而模板已含 /models/{model}，避免重复。
func connectivityProbeModelPath(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, "models/")
	if model == "" {
		return ConnectivityProbeTaskID
	}
	return url.PathEscape(model)
}

// classifyConnectivityResponse 按注册表规格判定连接测试结果：
//   - 通过（2xx 或规格预期拒绝码）：「密钥连通性验证通过（上游 HTTP <code>）」；
//   - 401/403：密钥无效，失败（任何规格都不得把这两个码列为预期拒绝）；
//   - 5xx：上游不可达，失败；
//   - 其余状态码：失败。
//
// 失败文案带状态码与脱敏上游消息（bounded 预览，同真实探针的响应体上限口径）。
func classifyConnectivityResponse(view *View, spec contracts.ConnectorConnectivityProbe, response *upstreamResponse, readErr error, durationMS int64, limited bool) *accountquality.ProbeObservation {
	protocol := DiagnosticProtocol(protocolCodeForView(view))
	statusCode := response.status
	upstreamErrorCode := parseUpstreamErrorCodeFromBody(response.bodyText)
	expected := make(map[int]bool, len(spec.ExpectedRejectionStatus))
	for _, code := range spec.ExpectedRejectionStatus {
		// 防御：认证失败码永远不是「预期拒绝」。
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			continue
		}
		expected[code] = true
	}
	success := (statusCode >= 200 && statusCode < 300) || expected[statusCode]
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

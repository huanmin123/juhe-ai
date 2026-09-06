package accountprobe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
)

// 诊断分级超时（Node accountDiagnosticRetryTimeoutMs = [10_000, 20_000, 30_000]）。
var DiagnosticRetryTimeouts = []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second}

// ImageDiagnosticRetryTimeouts 等价 Node accountImageDiagnosticRetryTimeoutMs =
// [120_000]（images_json 单次长预算，不晋级）。
var ImageDiagnosticRetryTimeouts = []time.Duration{120 * time.Second}

// CandidateSource 由仓储层实现：为一次探针解析账户与分组内候选凭据。
type CandidateSource interface {
	// LoadProbeView 返回探针所需的完整视图；账户/候选缺失时返回 (nil, nil)。
	LoadProbeView(ctx context.Context, req accountquality.ProbeRequest) (*View, error)
}

// KeyEntry 是账户凭据池内的一个 API Key（等价 Node AccountApiKeyEntry）。
type KeyEntry struct {
	Key         string
	Fingerprint string
	Index       int
}

// View 是探针输入的最小视图（等价 Node find_account_for_test 的 AccountSummary
// + find_openai_account_for_group 的 OpenAIAccountSecret 被消费字段）。
type View struct {
	AccountID       string
	AccountName     string
	Type            string // api_key | oauth | google_oauth
	Status          string
	ProviderCode    string
	ProtocolCode    string
	ProtocolVersion string
	// ProviderProtocolProfileID 为协议档案 ID（手动测试结果信封消费；
	// 探针请求构造不使用）。
	ProviderProtocolProfileID string
	// HealthCheckModel / HealthCheckEndpointMode 来自账户行。
	HealthCheckModel        string
	HealthCheckEndpointMode string
	SupportedModels         []string
	// BaseURL 与凭据来自分组候选（授权实例取来源账户）。
	BaseURL             string
	Credentials         map[string]any
	APIKeyEntries       []KeyEntry
	SelectedAPIKey      string // 默认凭据（oauth 取 access_token）
	FixedKey            *KeyEntry
	QuotaRecoveryPolicy map[string]any
	// NormalizeEndpointModes 是 credentials.supported_endpoint_modes 归一化后的
	// 支持集合（由仓储层按协议归一化注入）。
	NormalizeEndpointModes map[EndpointMode]bool
}

// Options 组装探针服务。
type Options struct {
	Source CandidateSource
	Client *http.Client
	Secret string // HMAC Key 指纹密钥（Node runtimeConfig.secret）
	Now    func() time.Time
	// Concurrency 限制并发探针数（Node runWithBackgroundFullDiagnosticSlot /
	// globalSharedQueueConcurrency）；0 表示不限制。
	Concurrency int
	// RetryTimeouts 覆盖分级超时序列；nil 使用 DiagnosticRetryTimeouts。
	RetryTimeouts []time.Duration
}

// Service 实现 accountquality.Prober，并为速度优先探针提供单账户探针入口。
type Service struct {
	source        CandidateSource
	client        *http.Client
	secret        string
	now           func() time.Time
	slots         chan struct{}
	retryTimeouts []time.Duration
}

// NewService 构建探针服务；client 为空时使用带代理不感知的默认传输。
func NewService(options Options) (*Service, error) {
	if options.Source == nil {
		return nil, errors.New("accountprobe 缺少 CandidateSource")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	client := options.Client
	if client == nil {
		client = &http.Client{}
	}
	var slots chan struct{}
	if options.Concurrency > 0 {
		slots = make(chan struct{}, options.Concurrency)
	}
	retryTimeouts := options.RetryTimeouts
	if len(retryTimeouts) == 0 {
		retryTimeouts = DiagnosticRetryTimeouts
	}
	return &Service{source: options.Source, client: client, secret: options.Secret, now: now, slots: slots, retryTimeouts: retryTimeouts}, nil
}

func (s *Service) nowMS() int64 { return s.now().UnixMilli() }

// FingerprintAPIKey 等价 Node fingerprintAccountApiKey（HMAC-SHA256(secret, key)）。
func (s *Service) FingerprintAPIKey(key string) string {
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// Probe 实现 accountquality.Prober。
//   - Full=true：precheck 的 Key 池诊断（Node runAccountApiKeyPoolDiagnostic，
//     allowSingleEntry）——对全部未禁用 Key 做分级尝试，任一成功即胜出；
//   - Full=false：cooldown-retest 的单 Key 有限诊断（diagnostics=limited）。
func (s *Service) Probe(ctx context.Context, req accountquality.ProbeRequest) (*accountquality.ProbeObservation, error) {
	view, err := s.source.LoadProbeView(ctx, req)
	if err != nil {
		return nil, err
	}
	if view == nil {
		// Node：候选缺失抛 AccountTestConfigurationError → 队列按异常处理。
		return nil, fmt.Errorf("账户 %s 不在当前分组或凭据不可用，无法执行网关测试", req.AccountID)
	}
	limited := !req.Full
	if req.Full {
		return s.probePool(ctx, view, false)
	}
	return s.probeFixedKey(ctx, view, view.FixedKey, limited)
}

// ProbeAccountView 供速度优先探针复用：按候选解析视图后发起完整诊断。
func (s *Service) ProbeAccountView(ctx context.Context, req accountquality.ProbeRequest) (*accountquality.ProbeObservation, error) {
	view, err := s.source.LoadProbeView(ctx, req)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, fmt.Errorf("账户 %s 不在当前分组或凭据不可用，无法执行网关测试", req.AccountID)
	}
	return s.probePool(ctx, view, false)
}

// ManualDiagnostics 是手动账号测试的诊断入口（对齐 Node worker 侧
// runOpenAIAccountTestWithoutStateMutation 的分支选择）：
//   - api_key 类型且凭据池可测（>1 把 Key）→ Key 池完整诊断
//     （Node runAccountApiKeyPoolTestIfNeeded → runAccountApiKeyPoolDiagnostic）；
//   - 其余 → 单凭据分级诊断（testOpenAIAccountWithDiagnosticRetries 的
//     stage 循环；limited 只影响失败文案脱敏）；
//   - images_json 形态走单次 120s 长预算（Node accountImageDiagnosticRetryTimeoutMs），
//     其余形态走 [10s, 20s, 30s] 分级（accountDiagnosticRetryTimeoutMs）。
func (s *Service) ManualDiagnostics(ctx context.Context, view *View, limited bool) (*accountquality.ProbeObservation, []PoolKeyAttempt, error) {
	if view == nil {
		return nil, nil, errors.New("探针视图缺失")
	}
	staged := s.withScheduleForView(view)
	if view.Type == "api_key" && len(view.APIKeyEntries) > 1 {
		return staged.probePoolDetailed(ctx, view, limited)
	}
	if len(view.APIKeyEntries) > 0 {
		observation, err := staged.probeFixedKey(ctx, view, &view.APIKeyEntries[0], limited)
		return observation, nil, err
	}
	// oauth / google_oauth：单凭据（SelectedAPIKey = access_token / refresh_token）。
	observation, err := staged.probeFixedKey(ctx, view, nil, limited)
	return observation, nil, err
}

// withScheduleForView 返回按视图端点形态取分级超时的派生服务
// （images_json 单次 120s，其余保持服务默认序列）。
func (s *Service) withScheduleForView(view *View) *Service {
	mode, err := resolveEndpointMode(view, EndpointMode(strings.TrimSpace(view.HealthCheckEndpointMode)))
	if err == nil && mode == ModeImagesJSON {
		return &Service{source: s.source, client: s.client, secret: s.secret, now: s.now, slots: s.slots, retryTimeouts: ImageDiagnosticRetryTimeouts}
	}
	return s
}

// probeFixedKey 对单个固定 Key 执行分级诊断（Node runAccountApiKeyPoolDiagnostic
// 的 stage 循环：仅“真实上游尝试后超时”晋级下一阶段）。
func (s *Service) probeFixedKey(ctx context.Context, view *View, entry *KeyEntry, limited bool) (*accountquality.ProbeObservation, error) {
	if entry == nil {
		return nil, errors.New("探针候选缺少可用 API Key")
	}
	var lastObservation *accountquality.ProbeObservation
	for stage := 0; stage < len(s.retryTimeouts); stage++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		observation := s.attemptWithTimeout(ctx, view, entry, s.retryTimeouts[stage], limited)
		lastObservation = observation
		if observation.Result.Success {
			return observation, nil
		}
		escalate := observation.Evidence.TimedOut &&
			observation.Evidence.HasRealUpstreamAttempt &&
			stage+1 < len(s.retryTimeouts)
		if !escalate {
			break
		}
	}
	return lastObservation, nil
}

// PoolKeyAttempt 是池诊断的单 Key 明细（manualtest 组装池摘要信封消费）。
type PoolKeyAttempt struct {
	Entry       KeyEntry
	Observation *accountquality.ProbeObservation
}

// probePool 等价 runAccountApiKeyPoolDiagnostic 的阶段循环：每个 Key 维护
// nextStage；同一阶段内逐 Key 尝试，成功即胜出，仅“真实上游尝试后超时”
// 晋级下一阶段，其余结果视为该 Key 完成。Node 侧并发跑同一阶段的 Key；
// Go 端串行执行（jobs 探针槽位已限流），胜出与晋级判定逐分支一致。
func (s *Service) probePool(ctx context.Context, view *View, limited bool) (*accountquality.ProbeObservation, error) {
	winner, _, err := s.probePoolDetailed(ctx, view, limited)
	return winner, err
}

// probePoolDetailed 返回胜出观测与已完成 Key 的明细（entry.index 升序，
// 对齐 Node diagnostic.attempts 的排序契约）。
func (s *Service) probePoolDetailed(ctx context.Context, view *View, limited bool) (*accountquality.ProbeObservation, []PoolKeyAttempt, error) {
	entries := make([]KeyEntry, len(view.APIKeyEntries))
	copy(entries, view.APIKeyEntries)
	if len(entries) == 0 {
		return nil, nil, errors.New("探针候选缺少可用 API Key")
	}
	type pendingKey struct {
		entry     KeyEntry
		nextStage int
		completed bool
	}
	pending := make([]*pendingKey, len(entries))
	results := make(map[int]*accountquality.ProbeObservation)
	for index := range entries {
		pending[index] = &pendingKey{entry: entries[index]}
	}
	var winner *accountquality.ProbeObservation
	for stage := 0; stage < len(s.retryTimeouts); stage++ {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		for _, item := range pending {
			if item.completed || item.nextStage != stage {
				continue
			}
			observation := s.attemptWithTimeout(ctx, view, &item.entry, s.retryTimeouts[stage], limited)
			if observation == nil {
				continue
			}
			if observation.Result.Success {
				winner = observation
				item.completed = true
				results[item.entry.Index] = observation
				break
			}
			if observation.Evidence.TimedOut &&
				observation.Evidence.HasRealUpstreamAttempt &&
				stage+1 < len(s.retryTimeouts) {
				item.nextStage = stage + 1
				continue
			}
			item.completed = true
			results[item.entry.Index] = observation
		}
		if winner != nil {
			break
		}
	}
	attempts := make([]PoolKeyAttempt, 0, len(results))
	for index := range entries {
		if observation, ok := results[entries[index].Index]; ok && observation != nil {
			attempts = append(attempts, PoolKeyAttempt{Entry: entries[index], Observation: observation})
		}
	}
	if winner != nil {
		return winner, attempts, nil
	}
	// Node：winner 缺失时取首个完成结果（diagnostic?.winner?.value ?? diagnostic?.attempts[0]?.value，
	// attempts 按 entry.index 升序）。
	for _, attempt := range attempts {
		return attempt.Observation, attempts, nil
	}
	return nil, attempts, errors.New("账户的 API Key 池诊断没有返回结果")
}

// attemptStaged 对单个 Key 走完整分级序列（Node 单 Key 的 stage 循环）。
func (s *Service) attemptStaged(ctx context.Context, view *View, entry *KeyEntry) *accountquality.ProbeObservation {
	var last *accountquality.ProbeObservation
	for stage := 0; stage < len(s.retryTimeouts); stage++ {
		observation := s.attemptWithTimeout(ctx, view, entry, s.retryTimeouts[stage], false)
		last = observation
		if observation == nil || observation.Result.Success {
			return observation
		}
		escalate := observation.Evidence.TimedOut &&
			observation.Evidence.HasRealUpstreamAttempt &&
			stage+1 < len(s.retryTimeouts)
		if !escalate {
			return observation
		}
	}
	return last
}

// attemptWithTimeout 以给定预算执行一次真实上游请求并分类结果
// （等价 Node 单个 diagnostic attempt）。
func (s *Service) attemptWithTimeout(ctx context.Context, view *View, entry *KeyEntry, timeout time.Duration, limited bool) *accountquality.ProbeObservation {
	if s.slots != nil {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-ctx.Done():
			return canceledObservation(view)
		}
	}
	attempt, err := s.executeAttempt(ctx, view, entry, timeout, limited)
	if err != nil {
		return classifyAttemptError(view, err, timeout, limited, s.nowMS())
	}
	return attempt
}

func canceledObservation(view *View) *accountquality.ProbeObservation {
	return &accountquality.ProbeObservation{
		Result: accountquality.ProbeResult{
			Success:      false,
			Message:      "账户测试已取消",
			ErrorCode:    "server_diagnostic_cancelled",
			ProtocolCode: protocolCodeForView(view),
		},
		Evidence: accountquality.ProbeEvidence{Canceled: true},
	}
}

func protocolCodeForView(view *View) string {
	switch {
	case strings.EqualFold(view.ProtocolCode, "anthropic"):
		return string(ProtocolAnthropic)
	case strings.EqualFold(view.ProtocolCode, "gemini"):
		return string(ProtocolGemini)
	default:
		return string(ProtocolOpenAI)
	}
}

// executeAttempt 构造请求、发起真实上游调用并做协议分类。
func (s *Service) executeAttempt(ctx context.Context, view *View, entry *KeyEntry, timeout time.Duration, limited bool) (*accountquality.ProbeObservation, error) {
	protocol := DiagnosticProtocol(protocolCodeForView(view))
	defaultMode := EndpointMode(strings.TrimSpace(view.HealthCheckEndpointMode))
	endpointMode, modeErr := resolveEndpointMode(view, defaultMode)
	if modeErr != nil {
		return nil, modeErr
	}
	model, modelErr := resolveTestModel(view, "")
	if modelErr != nil {
		return nil, modelErr
	}
	challenge := CreateOutputChallenge()
	request, buildErr := buildTestRequest(view, endpointMode, model, challenge)
	if buildErr != nil {
		return nil, buildErr
	}
	upstreamURL, urlErr := buildUpstreamURL(view, request.path)
	if urlErr != nil {
		return nil, urlErr
	}
	apiKey := view.SelectedAPIKey
	if entry != nil {
		apiKey = entry.Key
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("探针候选缺少可用凭据")
	}

	var firstByteAt time.Time
	httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, strings.NewReader(string(request.body)))
	if reqErr != nil {
		return nil, reqErr
	}
	isStream := endpointMode.streaming()
	accept := "application/json"
	if isStream {
		accept = "application/json, text/event-stream"
	}
	httpReq.Header.Set("accept", accept)
	httpReq.Header.Set("content-type", "application/json")
	for name, value := range request.headers {
		httpReq.Header.Set(name, value)
	}
	// buildUpstreamHeaders：统一 Bearer 认证。
	httpReq.Header.Set("authorization", "Bearer "+apiKey)
	httpReq.Header.Set("content-length", fmt.Sprintf("%d", len(request.body)))

	startedAt := s.now()
	response, readErr := s.readUpstream(ctx, httpReq, timeout, &firstByteAt)
	durationMS := s.now().Sub(startedAt).Milliseconds()
	firstTokenMS := int64(0)
	if !firstByteAt.IsZero() {
		firstTokenMS = firstByteAt.Sub(startedAt).Milliseconds()
	}
	if response != nil {
		attempt := classifyResponse(view, protocol, endpointMode, response.bodyText, response.headers, response.status, firstTokenMS, durationMS, challenge, limited)
		attempt.Evidence.HasRealUpstreamAttempt = true
		attempt.Evidence.UpstreamCompleted = readErr == nil
		attempt.Evidence.UpstreamStatus = response.status
		if readErr != nil {
			// HTTP framing 完成但读取响应体中断 → read_incomplete 证据。
			attempt.Evidence.TransportFailureKind = accountquality.TransportFailureRead
			attempt.Evidence.TimedOut = false
		}
		attempt.Result.TraceID = newTraceID()
		attempt.Result.DurationMs = durationMS
		return attempt, nil
	}
	// 传输层失败分类（Node upstreamRequestFailureKind / transport evidence）。
	failure := transportFailureFromError(readErr, timeout)
	observation := &accountquality.ProbeObservation{
		Result: accountquality.ProbeResult{
			Success:      false,
			ErrorCode:    failure.errorCode(),
			Message:      failure.message(readErr),
			ProtocolCode: string(protocol),
			DurationMs:   durationMS,
			TraceID:      newTraceID(),
		},
		Evidence: accountquality.ProbeEvidence{
			HasRealUpstreamAttempt: true,
			UpstreamCompleted:      false,
			TransportFailureKind:   failure.kind,
			TimedOut:               failure.timedOut,
		},
	}
	return observation, nil
}

type upstreamResponse struct {
	status   int
	headers  map[string]string
	bodyText string
}

// readUpstream 发起请求并读全响应体（探针响应体有界：256KB 预览上限与
// Node accountTestResponsePreviewBytes 一致）。返回 (nil, err) 表示 HTTP
// framing 未完成；返回 (response, err) 表示 framing 完成但读取中断。
func (s *Service) readUpstream(ctx context.Context, req *http.Request, timeout time.Duration, firstByteAt *time.Time) (*upstreamResponse, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req = req.WithContext(attemptCtx)
	response, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if firstByteAt != nil {
		*firstByteAt = s.now()
	}
	buffer := make([]byte, 0, 8192)
	chunk := make([]byte, 8192)
	total := 0
	for {
		n, readErr := response.Body.Read(chunk)
		if n > 0 {
			total += n
			if total > 256*1024 {
				break
			}
			buffer = append(buffer, chunk[:n]...)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			headers := map[string]string{}
			for name, values := range response.Header {
				if len(values) > 0 {
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
			}
			return &upstreamResponse{status: response.StatusCode, headers: headers, bodyText: string(buffer)}, readErr
		}
		if attemptCtx.Err() != nil {
			headers := map[string]string{}
			for name, values := range response.Header {
				if len(values) > 0 {
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
			}
			return &upstreamResponse{status: response.StatusCode, headers: headers, bodyText: string(buffer)}, attemptCtx.Err()
		}
		if n == 0 {
			break
		}
	}
	headers := map[string]string{}
	for name, values := range response.Header {
		if len(values) > 0 {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
	}
	return &upstreamResponse{
		status:   response.StatusCode,
		headers:  headers,
		bodyText: string(buffer),
	}, nil
}

type transportFailure struct {
	kind     string // "" | timeout | connection | read_incomplete
	timedOut bool
}

func (f transportFailure) errorCode() string {
	if f.timedOut {
		return "server_diagnostic_timeout"
	}
	return ""
}

func (f transportFailure) message(err error) string {
	if f.timedOut {
		return "账户测试超时"
	}
	return "账户测试失败：" + err.Error()
}

func transportFailureFromError(err error, timeout time.Duration) transportFailure {
	if err == nil {
		return transportFailure{}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return transportFailure{kind: "timeout", timedOut: true}
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return transportFailure{kind: "timeout", timedOut: true}
	}
	if errors.Is(err, context.Canceled) {
		return transportFailure{kind: "connection"}
	}
	// Node upstreamRequestFailureKind：无状态码的连接错误归 connection。
	return transportFailure{kind: "connection"}
}

func classifyAttemptError(view *View, err error, timeout time.Duration, limited bool, nowMS int64) *accountquality.ProbeObservation {
	failure := transportFailureFromError(err, timeout)
	message := "账户测试失败"
	if !limited {
		message = failure.message(err)
	} else {
		message = "上游请求失败"
	}
	errorCode := ""
	if failure.timedOut {
		errorCode = "server_diagnostic_timeout"
	}
	return &accountquality.ProbeObservation{
		Result: accountquality.ProbeResult{
			Success:      false,
			ErrorCode:    errorCode,
			Message:      message,
			ProtocolCode: protocolCodeForView(view),
		},
		Evidence: accountquality.ProbeEvidence{
			HasRealUpstreamAttempt: failure.kind != "",
			UpstreamCompleted:      false,
			TransportFailureKind:   failure.kind,
			TimedOut:               failure.timedOut,
		},
	}
}

// classifyResponse 移植 testOpenAIAccount 成功路径的结果分类。
func classifyResponse(view *View, protocol DiagnosticProtocol, mode EndpointMode, bodyText string, headers map[string]string, statusCode int, firstTokenMS, durationMS int64, challenge OutputChallenge, limited bool) *accountquality.ProbeObservation {
	context := parseResponseContext(bodyText)
	httpSucceeded := statusCode >= 200 && statusCode < 300
	protocolEvidence := false
	upstreamErrorCode := ""
	upstreamMessage := ""
	streamFailure := ""
	rawVisible := ""
	rawVisibleOK := false
	if mode == ModeImagesJSON {
		// Image tests verify only the HTTP outcome envelope（Node
		// inspectAccountTestImageResponseEnvelope 的成功证据窄投影）。
		protocolEvidence = hasImagesSuccessEvidence(context)
		if !protocolEvidence {
			upstreamErrorCode = parseUpstreamErrorCodeFromBody(bodyText)
			upstreamMessage = firstNonEmpty(
				protocolMessage(context.record, protocol),
				parseUpstreamMessage(context.bodyText, protocol, true),
			)
		}
	} else {
		protocolEvidence = hasProtocolSuccessEvidence(mode, context)
		upstreamErrorCode = parseUpstreamErrorCodeFromBody(bodyText)
		streamFailure = parseStreamFailureMessage(context, protocol)
		upstreamMessage = parseUpstreamMessage(context.bodyText, protocol, false)
		if upstreamMessage == "" && protocol != ProtocolOpenAI {
			upstreamMessage = parseUpstreamMessage(context.bodyText, ProtocolOpenAI, false)
		}
		if upstreamMessage == "" && bodyText != "" {
			upstreamMessage = truncateRunes(bodyText, 240)
		}
		rawVisible, rawVisibleOK = extractRawVisibleOutputText(context, protocol)
	}
	challengeMatched := mode == ModeImagesJSON
	if !challengeMatched {
		challengeMatched = rawVisibleOK && strings.Contains(rawVisible, challenge.ExpectedOutput)
	}
	outputChallengeError := ""
	if httpSucceeded && streamFailure == "" && protocolEvidence && !challengeMatched {
		outputChallengeError = "上游返回 HTTP 2xx 且协议完成，但输出未包含预期令牌"
	}
	success := httpSucceeded && streamFailure == "" && protocolEvidence && challengeMatched
	protocolEvidenceError := ""
	if httpSucceeded && streamFailure == "" && !protocolEvidence {
		if mode == ModeImagesJSON {
			if upstreamMessage != "" || upstreamErrorCode != "" {
				protocolEvidenceError = "上游 Images API 返回错误响应"
			} else {
				protocolEvidenceError = "上游 Images API 响应缺少有效图片结果"
			}
		} else {
			protocolEvidenceError = "上游返回 HTTP 2xx，但响应中缺少所选检查协议的完成证据"
		}
	}
	diagnosticStatusCode := statusCode
	message := ""
	errorCode := ""
	switch {
	case success:
		message = fmt.Sprintf("%s 测试通过", protocolName(mode))
	default:
		switch {
		case outputChallengeError != "":
			errorCode = "invalid_probe_output"
		case protocolEvidenceError != "":
			if mode == ModeImagesJSON {
				errorCode = upstreamErrorCode
				if errorCode == "" {
					errorCode = "invalid_protocol_success_response"
				}
			} else {
				errorCode = "invalid_protocol_success_response"
			}
		default:
			errorCode = upstreamErrorCode
		}
		if mode == ModeImagesJSON && upstreamMessage != "" {
			suffix := ""
			if upstreamErrorCode != "" {
				suffix = "（" + upstreamErrorCode + "）"
			}
			message = "上游 Images API 返回错误" + suffix + "：" + upstreamMessage
		} else {
			message = firstNonEmpty(outputChallengeError, protocolEvidenceError, upstreamMessage, streamFailure)
		}
		if message == "" {
			message = fmt.Sprintf("API 返回 HTTP %d", statusCode)
		}
	}
	if limited && !success {
		// limitedAccountTestMessage：额度规则命中显示“上游额度不足”。
		if accountquality.SystemInsufficientQuotaRuleMatches(statusCode, errorCode, "", searchableText(message, bodyText)) {
			message = "上游额度不足"
		} else {
			message = "上游请求失败"
		}
	}
	result := accountquality.ProbeResult{
		Success:      success,
		StatusCode:   &diagnosticStatusCode,
		ErrorCode:    errorCode,
		Message:      message,
		DurationMs:   durationMS,
		FirstTokenMS: firstTokenMS,
		ProtocolCode: string(protocol),
	}
	// Node：upstream attempt 的 responseBodyText 保留完整上游文本（额度 hint 输入），
	// limited 只影响 result.message/responseHeaders。
	result.ResponseBodyText = bodyText
	if limited {
		if accountquality.SystemInsufficientQuotaRuleMatches(statusCode, errorCode, "", searchableText(message, bodyText)) {
			result.ResponseHeaders = quotaResponseHeaders(headers)
		}
	} else {
		result.ResponseHeaders = headers
	}
	return &accountquality.ProbeObservation{
		Result: result,
		Evidence: accountquality.ProbeEvidence{
			HasRealUpstreamAttempt: true,
			UpstreamCompleted:      true,
			UpstreamStatus:         statusCode,
		},
	}
}

// hasImagesSuccessEvidence 是 inspectAccountTestImageResponseEnvelope 的窄投影：
// JSON 信封含 b64_json/url 图片数据或 created 标志即视为有效图片结果。
func hasImagesSuccessEvidence(context responseContext) bool {
	record := context.record
	if record == nil {
		return false
	}
	if arrayValue(record["data"]) != nil && len(arrayValue(record["data"])) > 0 {
		return true
	}
	if _, hasCreated := record["created"]; hasCreated {
		return true
	}
	return false
}

func searchableText(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// quotaResponseHeaders 等价 limitedQuotaResponseHeaders 的白名单。
func quotaResponseHeaders(headers map[string]string) map[string]string {
	var safe map[string]string
	for name, value := range headers {
		switch strings.ToLower(name) {
		case "retry-after", "x-quota-reset-at", "x-ratelimit-reset", "x-rate-limit-reset":
			if safe == nil {
				safe = map[string]string{}
			}
			safe[name] = value
		}
	}
	return safe
}

func parseUpstreamErrorCodeFromBody(bodyText string) string {
	context := parseResponseContext(bodyText)
	for _, payload := range context.payloads {
		if code := upstreamErrorCodeFromPayload(payload); code != "" {
			return code
		}
	}
	return ""
}

func protocolName(mode EndpointMode) string {
	switch {
	case mode.anthropic():
		return "Anthropic Messages"
	case mode.gemini():
		return "Gemini GenerateContent"
	case mode == ModeChatJSON || mode == ModeChatSSE:
		return "OpenAI Chat Completions"
	default:
		return "OpenAI Responses"
	}
}

func newTraceID() string {
	return "trace-" + newUUID()
}

// resolveEndpointMode 等价 resolveAccountTestEndpointMode（无显式请求 mode，
// 取支持列表首个）。
func resolveEndpointMode(view *View, defaultMode EndpointMode) (EndpointMode, error) {
	supported := manualTestEndpointModes(defaultMode, view.NormalizeEndpointModes, endpointOrderForView(view))
	return defaultEndpointMode(supported)
}

func endpointOrderForView(view *View) []EndpointMode {
	if isAnthropicProtocol(view) {
		return EndpointModeOrderAnthropic()
	}
	if isGeminiProtocol(view) {
		return EndpointModeOrderGemini()
	}
	if view.Type == "oauth" {
		return EndpointModeOrderOAuth()
	}
	return EndpointModeOrderOpenAI()
}

func isAnthropicProtocol(view *View) bool {
	return strings.EqualFold(strings.TrimSpace(view.ProtocolCode), "anthropic")
}

func isGeminiProtocol(view *View) bool {
	return strings.EqualFold(strings.TrimSpace(view.ProtocolCode), "gemini")
}

// resolveTestModel 等价 resolveAccountTestModelAsync（无显式模型分支）。
func resolveTestModel(view *View, explicitModel string) (string, error) {
	if explicitModel != "" {
		return explicitModel, nil
	}
	healthModel := strings.TrimSpace(view.HealthCheckModel)
	if healthModel == "" {
		return "", errors.New("账户检查模型未配置")
	}
	found := false
	for _, model := range view.SupportedModels {
		if strings.TrimSpace(model) == healthModel {
			found = true
			break
		}
	}
	if !found && len(view.SupportedModels) > 0 {
		return "", fmt.Errorf("账户检查模型不在支持模型列表中：%s", healthModel)
	}
	return healthModel, nil
}

// buildTestRequest 按 endpoint mode 构造探针请求（等价 createOpenAITestRequest /
// createAnthropicTestRequest / createGeminiTestRequest / Images）。
func buildTestRequest(view *View, mode EndpointMode, model string, challenge OutputChallenge) (*testRequest, error) {
	switch {
	case mode == ModeImagesJSON:
		body, err := buildImagesPayload(model)
		if err != nil {
			return nil, err
		}
		return &testRequest{path: "/v1/images/generations", body: body, model: model}, nil
	case mode.anthropic():
		sessionID := newUUID()
		body, err := buildAnthropicMessagesPayload(model, challenge.Prompt, mode.streaming(), sessionID)
		if err != nil {
			return nil, err
		}
		return &testRequest{
			path: "/v1/messages",
			body: body,
			headers: map[string]string{
				clientProfileHeader:        "claude_code",
				"x-claude-code-session-id": sessionID,
			},
			model: model,
		}, nil
	case mode.gemini():
		if mode == ModeInteractionsJSON || mode == ModeInteractionsSSE {
			stream := mode == ModeInteractionsSSE
			body, err := orderedJSON([]orderedField{
				{key: "model", marshal: func() ([]byte, error) { return rawText(model), nil }},
				{key: "input", marshal: func() ([]byte, error) { return rawText(challenge.Prompt), nil }},
				{key: "stream", marshal: func() ([]byte, error) { return rawBool(stream), nil }},
			})
			if err != nil {
				return nil, err
			}
			request := &testRequest{path: "/v1beta/interactions", body: body, model: model}
			if stream {
				request.headers = map[string]string{"accept": "text/event-stream"}
			}
			return request, nil
		}
		body, err := buildGeminiGenerateContentPayload(challenge.Prompt)
		if err != nil {
			return nil, err
		}
		method := "generateContent"
		query := ""
		if mode == ModeGenerateContentSSE {
			method = "streamGenerateContent"
			query = "?alt=sse"
		}
		return &testRequest{
			path:  "/v1beta/" + geminiModelPath(model) + ":" + method + query,
			body:  body,
			model: model,
		}, nil
	default:
		stream := mode.streaming()
		var body []byte
		var err error
		path := "/v1/responses"
		if mode == ModeChatJSON || mode == ModeChatSSE {
			path = "/v1/chat/completions"
			body, err = buildOpenAIChatCompletionsPayload(model, challenge.Prompt, stream)
		} else {
			body, err = buildOpenAIResponsesPayload(model, challenge.Prompt, view.Type == "oauth", stream)
		}
		if err != nil {
			return nil, err
		}
		return &testRequest{path: path, body: body, model: model}, nil
	}
}

// buildUpstreamURL 等价各协议 buildUpstreamUrl：
//   - OpenAI：base 归一化补 /v1，剥请求路径 /v1 前缀；
//   - Anthropic：base 原样去尾斜杠，保留完整请求路径；
//   - Gemini：base pathname + 请求路径（base 以 /v1beta 结尾时剥请求前缀）。
func buildUpstreamURL(view *View, pathAndQuery string) (string, error) {
	base := strings.TrimSpace(view.BaseURL)
	if base == "" {
		return "", errors.New("账户缺少上游 base_url")
	}
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", fmt.Errorf("账户 base_url 无效：%w", err)
	}
	if isGeminiProtocol(view) {
		basePath := strings.TrimRight(parsed.Path, "/")
		suffixPath := pathAndQuery
		if strings.HasSuffix(basePath, "/v1beta") {
			suffixPath = stripV1BetaPrefix(pathAndQuery)
		}
		parsed.Path = strings.ReplaceAll(basePath+stripTrailingSlashPath(suffixPath), "//", "/")
		return parsed.String(), nil
	}
	if isAnthropicProtocol(view) {
		return parsed.String() + pathAndQuery, nil
	}
	normalizedBase := parsed.String()
	if !strings.HasSuffix(normalizedBase, "/v1") {
		normalizedBase += "/v1"
	}
	return normalizedBase + openAIPathSuffix(pathAndQuery), nil
}

func openAIPathSuffix(pathAndQuery string) string {
	path := pathAndQuery
	query := ""
	if index := strings.Index(pathAndQuery, "?"); index >= 0 {
		path = pathAndQuery[:index]
		query = pathAndQuery[index:]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// Node: replace(/^\/v1(?=\/|$)/, '')。
	if len(path) >= 3 && path[:3] == "/v1" && (len(path) == 3 || path[3] == '/') {
		path = path[3:]
	}
	if path == "/" {
		path = ""
	}
	return path + query
}

func stripV1BetaPrefix(pathAndQuery string) string {
	if len(pathAndQuery) >= 8 && strings.EqualFold(pathAndQuery[:8], "/v1beta/") {
		return pathAndQuery[7:]
	}
	return pathAndQuery
}

func stripTrailingSlashPath(path string) string {
	if index := strings.Index(path, "?"); index >= 0 {
		return path[:index]
	}
	return path
}

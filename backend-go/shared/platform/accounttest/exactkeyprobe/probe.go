// Package exactkeyprobe 是 jobs internal/accounthealth「精确 Key+模型诊断探针」
// 执行器最小闭包的平台层下沉副本（PLAN-20261008T113056000Z 根治阶段）：gateway
// 进程内的 key-model 恢复驱动需要与 jobs 完全一致的探针语义，而 Go internal
// 可见性禁止跨项目 import，故整体下沉本包；jobs accounthealth 留守 J1 机制
// （reader/scheduler/projection/outbox）并 import 本包。两侧以本注释互指：
// 修改探针契约时必须两侧同步，禁止任一侧独自漂移。
package exactkeyprobe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

const (
	// probeChallengePromptFormat 是探针挑战报文格式串（两个十进制操作数）。
	// 2026-10-15 停用历史固定令牌形态（"接口连通性自动测试：请原样输出
	// juhe<随机数字>"）：上游 api.shenwenai.com 等内容审核把「请原样输出
	// 令牌」判为可疑请求并整段返回 HTTP 403（生产实测 14 次中 13 次被拒，
	// 探针失败把正常账户打成临时不可调用）；随机算术题实测 19/19 通过，
	// 且模型必须真实推理才能答对。
	probeChallengePromptFormat = "接口连通性自动测试：请计算 %d+%d，只回复数字"
	// probeOutputTokenLimit 是 F4 健康/恢复探针的文本输出预算别名（信封统一
	// 设计 §6 M3）：取值权威已上收 upstreamenvelope.DefaultProbeOutputTokens，
	// 本包不再私有定义数值；保留别名是 probe_test 的线上预算断言仍引用本名。
	// 1024 而非 256：推理模型的思考 token 计入输出上限，256 会被思考耗尽
	//（finish_reason=length）导致 content 为空，探针误判 invalid_probe_output。
	// 与 platform 包 accounttest/accountprobe 的 outputTokenLimit 保持同值。
	probeOutputTokenLimit = upstreamenvelope.DefaultProbeOutputTokens
	defaultMaxBodyBytes   = int64(256 * 1024)
)

// probeInstructionsPool 是 responses 形态 system instructions 的轮换池：
// 池首保留历史原值，其余为 OpenAI 生态常见无害 system 指令。轮换池消除
// 单一固定指纹（上游风控按固定报文指纹封号）。
var probeInstructionsPool = [...]string{
	"You are ChatGPT, a helpful assistant.",
	"You are ChatGPT, a large language model trained by OpenAI.",
	"You are a helpful assistant.",
	"You are a helpful AI assistant.",
	"You are an AI assistant.",
}

// challengeDigits 返回 3~6 位随机数字，供图片探针 prompt 打散固定报文指纹
// （文本形态改用 newProbeChallenge 的算术题，不再使用令牌后缀）。
// crypto/rand 读取失败时回退时间熵：降级仅影响随机性，不影响验证语义
// （图片形态只验 HTTP 信封，不校验文本）。与 platform 包
// accounttest/accountprobe 的挑战报文实现是有意保持的独立副本——两包报文
// 历史上各自独立（direct_input.go 已有的 accountprobe import 不为此扩展），
// 不为此新增跨包 API 依赖。
func challengeDigits() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		binary.LittleEndian.PutUint64(buf, uint64(time.Now().UnixNano()))
	}
	length := 3 + int(buf[0]&0x03)
	digits := make([]byte, length)
	for i := range digits {
		digits[i] = '0' + buf[i+1]%10
	}
	return string(digits)
}

// probeChallenge 是一次探针请求的挑战报文与期望输出；每次请求独立生成。
type probeChallenge struct {
	Prompt   string
	Expected string
}

// newProbeChallenge 生成随机算术题挑战：操作数取 10~89（按 256 均匀映射到
// 80 个取值，无模偏差），期望输出为两数之和的十进制文本。同 challengeDigits：
// crypto/rand 读取失败时回退时间熵，降级仅影响随机性，不影响验证语义。
func newProbeChallenge() probeChallenge {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		binary.LittleEndian.PutUint64(buf, uint64(time.Now().UnixNano()))
	}
	left := 10 + int(buf[0])*80/256
	right := 10 + int(buf[1])*80/256
	return probeChallenge{
		Prompt:   fmt.Sprintf(probeChallengePromptFormat, left, right),
		Expected: strconv.Itoa(left + right),
	}
}

// challengeAnswerMatched 判定可见输出是否包含期望的十进制答案：命中数字的
// 两侧都不得是数字，避免匹配到更长数字内部的片段（如期望 405 命中 "17940576"）。
func challengeAnswerMatched(visible, expected string) bool {
	if expected == "" {
		return false
	}
	for searchFrom := 0; searchFrom+len(expected) <= len(visible); {
		index := strings.Index(visible[searchFrom:], expected)
		if index < 0 {
			return false
		}
		begin := searchFrom + index
		end := begin + len(expected)
		leftOK := begin == 0 || !isASCIIDigit(visible[begin-1])
		rightOK := end == len(visible) || !isASCIIDigit(visible[end])
		if leftOK && rightOK {
			return true
		}
		searchFrom = begin + 1
	}
	return false
}

func isASCIIDigit(char byte) bool {
	return char >= '0' && char <= '9'
}

// pickProbeInstructions 用 crypto/rand 从轮换池随机选一条（len 取模）；
// 读取失败时回退池首历史原值，不影响验证语义。
func pickProbeInstructions() string {
	var index [1]byte
	if _, err := rand.Read(index[:]); err != nil {
		return probeInstructionsPool[0]
	}
	return probeInstructionsPool[int(index[0])%len(probeInstructionsPool)]
}

type ProbeOptions struct {
	// Client 用于替换 HTTP 依赖并确定性回放探针；nil 使用生产共享连接池。
	Client           *http.Client
	Secret           string
	Timeout          time.Duration
	MaxResponseBytes int64
	Now              func() time.Time
}

// ProbeOpenAI executes one frozen direct probe. The historical exported name is
// retained for callers, but dispatches by the signed protocol profile rather
// than assuming every account speaks OpenAI v1. "Frozen" covers the protocol
// wire shape only: since 2026-09 the image prompt and system instructions are
// deliberately randomized per request because upstream risk control bans fixed
// payload fingerprints. Since 2026-10-15 the text challenge is a per-request
// arithmetic question ("请计算 A+B，只回复数字") whose answer is verified with
// digit-boundary matching: fixed token-echo prompts ("请原样输出 juhe…") were
// answered with HTTP 403 by upstream content guards, failing healthy accounts.
func ProbeOpenAI(ctx context.Context, input Input, credential CredentialEnvelope, options ProbeOptions) ProbeResult {
	if err := validateInput(input, options); err != nil {
		return taskFailure("invalid_input", err.Error())
	}
	token, err := decryptToken(options.Secret, credential)
	if err != nil {
		return taskFailure("credential_unavailable", "凭据不可用")
	}
	baseURL, err := parseBaseURL(input.BaseURL, input.AllowInsecureBaseURL)
	if err != nil {
		return taskFailure("base_url_invalid", err.Error())
	}
	client, err := probeHTTPClient(input, options)
	if err != nil {
		return taskFailure("proxy_unavailable", err.Error())
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	challenge := newProbeChallenge()
	request, err := buildProbeRequest(requestCtx, baseURL, input, token, challenge)
	if err != nil {
		return taskFailure("request_build_failed", err.Error())
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
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return neutral(response.StatusCode, "upstream_http_status", "上游返回非成功状态")
	}
	if err := verifyResponse(input, challenge, body); err != nil {
		var failure *responsesSSEFailure
		if errors.As(err, &failure) {
			return neutral(response.StatusCode, "upstream_protocol_invalid", failure.Error())
		}
		if readErr != nil {
			return responseReadFailure(readErr)
		}
		return neutral(response.StatusCode, "upstream_protocol_invalid", "上游响应未满足探活语义")
	}
	if errors.Is(readErr, upstreamhttp.ErrResponseBodyTooLarge) {
		return responseReadFailure(readErr)
	}
	return ProbeResult{Outcome: OutcomeSuccess, StatusCode: response.StatusCode}
}

// ProbeExactKeyModel reuses the frozen J1 direct probe executor but deliberately
// bypasses its account-health cursor and outcome projection. The caller selects
// one physical key and one already-resolved model route; this function does not
// write accounts.status or any J1 cursor.
func ProbeExactKeyModel(ctx context.Context, input Input, keyFingerprint, finalModel, endpointMode string, options ProbeOptions) ProbeResult {
	input.HealthModel = strings.TrimSpace(finalModel)
	input.EndpointMode = strings.TrimSpace(endpointMode)
	if input.HealthModel == "" || input.EndpointMode == "" {
		return taskFailure("key_model_route_invalid", "Key-model 探测路由缺失")
	}
	if input.Type != "api_key" {
		return taskFailure("key_model_credential_type_unsupported", "首版 Key-model 恢复仅支持 API Key")
	}
	for _, key := range input.APIKeys {
		if key.Fingerprint == strings.TrimSpace(keyFingerprint) {
			return ProbeOpenAI(ctx, input, key.Credential, options)
		}
	}
	return taskFailure("key_model_key_unavailable", "原失败 API Key 已不可用")
}

func validateInput(input Input, options ProbeOptions) error {
	if strings.TrimSpace(input.AccountID) == "" || input.InputVersion <= 0 || input.ConfigRevision <= 0 || input.DispatchRevision <= 0 {
		return errors.New("input fence 无效")
	}
	profileProvider := probeProfileProvider(input.ProtocolProfileID, input.Provider)
	if !isSupportedDirectProfile(input.ProtocolProfileID, profileProvider, input.Type, input.EndpointMode) {
		return errors.New("未冻结的 provider 或账户类型")
	}
	if err := validateDirectProtocolMetadata(input.ProtocolProfileID, input.ProtocolCode, input.ProtocolVersion); err != nil {
		return err
	}
	protocol := directProbeProtocolForMode(input.ProtocolProfileID, profileProvider, input.EndpointMode)
	if protocol == "" || strings.TrimSpace(input.Provider) != protocol {
		return errors.New("provider 与协议 profile 不一致")
	}
	if strings.TrimSpace(input.HealthModel) == "" {
		return errors.New("health model 缺失")
	}
	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}
	if input.ExpiresAt.IsZero() || !input.ExpiresAt.After(now) {
		return errors.New("input 已过期")
	}
	if (input.Type == "oauth" || input.Type == "google_oauth") && (input.OAuthExpiresAt == nil || !input.OAuthExpiresAt.After(now.Add(time.Minute))) {
		return errors.New("OAuth access token 已到期或接近到期")
	}
	if input.Type == "google_oauth" && (input.OAuthType == "code_assist" || input.OAuthType == "google_one") {
		if protocol != "gemini" || strings.TrimSpace(input.OAuthProjectID) == "" {
			return errors.New("Gemini Code Assist / Google One 探活缺少协议或 project_id")
		}
	}
	return nil
}

func probeTransport(input Input, options ProbeOptions) (*http.Transport, error) {
	proxyURL, timeout, err := probeTransportConfig(input, options)
	if err != nil {
		return nil, err
	}
	return upstreamhttp.NewTransport(proxyURL, upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeout})
}

func probeHTTPClient(input Input, options ProbeOptions) (*http.Client, error) {
	proxyURL, timeout, err := probeTransportConfig(input, options)
	if err != nil {
		return nil, err
	}
	if options.Client != nil {
		return options.Client, nil
	}
	return upstreamhttp.SharedClient(proxyURL, upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeout})
}

func probeTransportConfig(input Input, options ProbeOptions) (string, time.Duration, error) {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if input.Proxy == nil {
		return "", timeout, nil
	}
	proxyText, err := decryptToken(options.Secret, *input.Proxy)
	if err != nil {
		return "", 0, errors.New("代理凭据不可用")
	}
	if _, err := upstreamhttp.ParseProxyURL(proxyText); err != nil {
		if errors.Is(err, upstreamhttp.ErrProxySchemeUnsupported) {
			return "", 0, errors.New("未支持的代理协议")
		}
		return "", 0, errors.New("代理 URL 无效")
	}
	return proxyText, timeout, nil
}

func buildProbeRequest(ctx context.Context, base *url.URL, input Input, token string, challenge probeChallenge) (*http.Request, error) {
	// endpoint catalog 条目统一取自信封（信封统一设计 §6 M3）：预算字段声明、
	// anthropic-version / api-revision 端点固定头与 Accept 口径都以 catalog 为
	// 准。未登记的 endpoint mode 保持既有 "未冻结的 endpoint mode" 错误契约。
	entry, err := upstreamenvelope.Lookup(upstreamenvelope.EndpointMode(input.EndpointMode))
	if err != nil {
		return nil, errors.New("未冻结的 endpoint mode")
	}
	protocol := directProbeProtocolForMode(input.ProtocolProfileID, input.Provider, input.EndpointMode)
	// 形态判定单源（§9.1 按账户形态分列）：请求构造与响应评分（verifyResponse）
	// 共用同一判定函数，评分跟随请求编码形态（§9.4 2026-10-12 W3 裁决）。
	// A 形态优先：§9.1-B 明确兼容画像是 API Key 形态，oauth 账户即使携带
	// ClientCompatibility=codex_responses 也按 A 处理（矩阵无第四形态）。
	oauthCodex := isOAuthCodexForm(input)
	codeAssist := isCodeAssistForm(input)
	codexCompatibility := !oauthCodex && protocol == "openai" && input.ClientCompatibility == "codex_responses" &&
		(input.EndpointMode == "responses_json" || input.EndpointMode == "responses_sse")
	// 请求侧报文随机化：挑战为每次请求独立的随机算术题（newProbeChallenge），
	// responses 形态的 system 指令从轮换池随机选取，图片形态 prompt 保留随机
	// 数字后缀。验证侧只匹配期望答案，不回读请求。与 accountprobe 的
	// CreateOutputChallenge 保持同义。
	instructions := pickProbeInstructions()
	// 探针语义 body 字段（§3.2/§9.4 调用方职责）：预算字段由信封按 catalog
	// 与 BudgetPolicy 统一追加，本包不再私有注入（applyProbeBudget 已删除）。
	var fields []upstreamenvelope.BodyField
	switch input.EndpointMode {
	case "chat_json", "chat_sse":
		fields = []upstreamenvelope.BodyField{
			{Key: "model", Value: input.HealthModel},
			{Key: "messages", Value: []map[string]any{{"role": "user", "content": challenge.Prompt}}},
			{Key: "stream", Value: input.EndpointMode == "chat_sse"},
		}
	case "responses_json", "responses_sse":
		fields = []upstreamenvelope.BodyField{
			{Key: "model", Value: input.HealthModel},
			{Key: "input", Value: []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": challenge.Prompt}}}}},
			{Key: "instructions", Value: instructions},
		}
		if !oauthCodex {
			// A 形态 stream 由 wrapCodexResponses 强制写入（§9.1-A 恒 SSE，
			// 行为变化属 §9.1 裁决）；B/C 保留账户配置模式名对应的 stream 值。
			fields = append(fields, upstreamenvelope.BodyField{Key: "stream", Value: input.EndpointMode == "responses_sse"})
		}
	case "images_json":
		fields = []upstreamenvelope.BodyField{
			{Key: "model", Value: input.HealthModel},
			{Key: "prompt", Value: "Solid black. " + challengeDigits()},
			{Key: "n", Value: 1},
			{Key: "size", Value: "1024x1024"},
			{Key: "quality", Value: "low"},
			{Key: "output_format", Value: "webp"},
			{Key: "output_compression", Value: 100},
		}
	case "messages_json", "messages_sse":
		fields = []upstreamenvelope.BodyField{
			{Key: "model", Value: input.HealthModel},
			{Key: "messages", Value: []map[string]any{{"role": "user", "content": challenge.Prompt}}},
			{Key: "stream", Value: input.EndpointMode == "messages_sse"},
		}
	case "generate_content_json", "generate_content_sse":
		fields = []upstreamenvelope.BodyField{
			{Key: "contents", Value: []map[string]any{{"role": "user", "parts": []map[string]any{{"text": challenge.Prompt}}}}},
		}
	case "interactions_json", "interactions_sse":
		fields = []upstreamenvelope.BodyField{
			{Key: "model", Value: input.HealthModel},
			{Key: "input", Value: challenge.Prompt},
			{Key: "stream", Value: input.EndpointMode == "interactions_sse"},
		}
	default:
		// isSupportedDirectProfile 保证不可达，属防御守卫。
		return nil, errors.New("未冻结的 endpoint mode")
	}
	spec := upstreamenvelope.Spec{
		BaseURL:           base.String(),
		Protocol:          entry.Protocol,
		ProtocolProfileID: input.ProtocolProfileID,
		EndpointMode:      entry.Mode,
		Model:             input.HealthModel,
		BodyFields:        fields,
		Auth:              upstreamenvelope.Auth{Token: token, ExtraHeaders: map[string]string{}},
	}
	// 认证策略映射（与信封 auth.go 的现状取证同源）：Anthropic 默认
	// x-api-key，OAuth 与 GLM anthropic 档案改 Bearer；Gemini api_key 用
	// x-goog-api-key，google_oauth 改 Bearer + x-goog-user-project；OAuth 系
	// 账户（type=oauth）用 AuthOAuth——头与 AuthBearer 同为 Bearer，差别在
	// 信封对 SSE 端点的 Accept 口径（OAuth 纯流式，现状同）。
	switch protocol {
	case "anthropic":
		switch {
		case input.Type == "oauth":
			spec.Auth.Strategy = upstreamenvelope.AuthOAuth
		case input.ProtocolProfileID == "profile_glm_coding_anthropic_v1":
			spec.Auth.Strategy = upstreamenvelope.AuthBearer
		default:
			spec.Auth.Strategy = upstreamenvelope.AuthAnthropicAPIKey
		}
	case "gemini":
		switch {
		case codeAssist:
			// Code Assist 线真相恒 SSE：请求改道 /v1internal:streamGenerateContent?alt=sse
			//（wrapGeminiCodeAssist 承担路径与三字段包装，project 经 Spec.Project），
			// Accept 必须按流式端点解析，不看账户配置模式名（responses/json
			// 模式名仅是账户配置，jobs 侧记录不变）。Accept 现状即纯流式。
			spec.Auth.Strategy = upstreamenvelope.AuthOAuth
			spec.Adapter = upstreamenvelope.AdapterGeminiCodeAssist
			spec.Project = input.OAuthProjectID
			spec.EndpointMode = upstreamenvelope.ModeGenerateContentSSE
			if value := strings.TrimSpace(input.OAuthQuotaProjectID); value != "" {
				spec.Auth.ExtraHeaders["x-goog-user-project"] = value
			}
		case input.Type == "google_oauth":
			// §4.1 口径统一（W4 跨包裁决，与 F2 同规）：google_oauth 非 Code
			// Assist 按 AuthOAuth 解析——认证头不变（Bearer + 配额项目），SSE
			// Accept 从 api-key 双 accept 收敛为纯流式（W3 曾按 AuthBearer 保
			// 双 accept 留现状，W4 统一）。
			spec.Auth.Strategy = upstreamenvelope.AuthOAuth
			if value := strings.TrimSpace(input.OAuthQuotaProjectID); value != "" {
				spec.Auth.ExtraHeaders["x-goog-user-project"] = value
			}
		default:
			spec.Auth.Strategy = upstreamenvelope.AuthGeminiAPIKey
		}
	default:
		if input.Type == "oauth" {
			spec.Auth.Strategy = upstreamenvelope.AuthOAuth
		} else {
			spec.Auth.Strategy = upstreamenvelope.AuthBearer
		}
	}
	switch {
	case oauthCodex:
		// A 形态（OAuth Codex，§9.1-A）：固定上游 + 强制 SSE + BudgetNone +
		// 身份单源（调用方每次请求生成，§9.3）。行为变化属 §9.1 裁决：恒
		// SSE + stream:true + 删 max_output_tokens + 新增身份头集合（原 A
		// 形态无身份头，与 F3 W2 接线同构）。chatgpt-account-id 是 OAuth
		// 账户专用伴随头，经 Auth.ExtraHeaders 注入（现状条件保留）。
		identity, err := newCodexIdentity()
		if err != nil {
			return nil, err
		}
		spec.Adapter = upstreamenvelope.AdapterOpenAIOAuthCodex
		spec.CodexIdentity = identity
		spec.Budget = upstreamenvelope.BudgetNone
		if accountID := strings.TrimSpace(input.OAuthAccountID); accountID != "" {
			spec.Auth.ExtraHeaders["ChatGPT-Account-Id"] = accountID
		}
	case codexCompatibility:
		// B 形态（codex_responses 兼容画像，§9.1-B）：保留档案预算字段
		//（不继承 A 的 BudgetNone 例外）；身份头/Lite 头/client_metadata/
		// prompt_cache_key 全由 Build 依同一份 Spec.CodexIdentity 承担
		//（§9.3 单源，替代原 applyCodexResponsesCompatibility +
		// applyCodexProbeHeaders 两段构造）。
		identity, err := newCodexIdentity()
		if err != nil {
			return nil, err
		}
		spec.Adapter = upstreamenvelope.AdapterOpenAICodexCompatibility
		spec.CodexIdentity = identity
		spec.Budget = upstreamenvelope.BudgetBounded(upstreamenvelope.DefaultProbeOutputTokens)
	default:
		// C 形态与 Code Assist：预算按 catalog 条目解析（F4 既有文本预算
		// 语义 1024；images/interactions 契约无预算字段返回 none，不伪造兜底）。
		budget, err := upstreamenvelope.ResolveBudgetPolicy(spec.EndpointMode, "", spec.Adapter)
		if err != nil {
			return nil, err
		}
		spec.Budget = budget
	}
	request, err := upstreamenvelope.Build(ctx, spec)
	if err != nil {
		return nil, err
	}
	// 系统客户端身份头保留调用方职责（§9.4：是否上收由 M2–M5 另行裁决）：
	// 在 Build 之后应用；A/B 形态 Build 已写 Codex UA，本调用对已带 UA 的
	// 请求保持沉默（upstreamidentity 契约），不会覆盖身份头。
	upstreamidentity.ApplySystemClientHeaders(request.Header, upstreamidentity.Input{
		ProviderProtocolProfileID: input.ProtocolProfileID,
		CredentialType:            input.Type,
		OAuthType:                 input.OAuthType,
		UpstreamHostname:          request.URL.Hostname(),
	})
	return request, nil
}

// isOAuthCodexForm 判定 A 形态账户输入（OAuth Codex，§9.1-A）：oauth + 空
// profile（GPT 泛化）或 profile_gpt_openai_v1 + responses 模式。协议由
// isSupportedDirectProfile 保证为 openai。请求构造与响应评分共用本判定。
func isOAuthCodexForm(input Input) bool {
	return input.Type == "oauth" &&
		(input.ProtocolProfileID == "" || input.ProtocolProfileID == "profile_gpt_openai_v1") &&
		(input.EndpointMode == "responses_json" || input.EndpointMode == "responses_sse")
}

// isCodeAssistForm 判定 Gemini Code Assist / Google One 形态输入（§9.1）：
// 请求恒改道 :streamGenerateContent?alt=sse，响应恒按 Gemini SSE 解析。
func isCodeAssistForm(input Input) bool {
	return input.Provider == "gemini" && input.Type == "google_oauth" &&
		(input.OAuthType == "code_assist" || input.OAuthType == "google_one")
}

// newCodexIdentity 生成一次 Codex 形态请求的身份值（信封统一设计 §9.3 身份
// 单源）：session=UUID、thread=session、turn=独立 UUID（F4 现值，区别于 F3
// 的 turn=session）、window=thread+":0"、installation=UUID，turn metadata
// JSON 含全部身份值与 request_kind/thread_source/sandbox/
// turn_started_at_unix_ms。同一份身份经 Spec.CodexIdentity 同时产出身份请求
// 头与（B 形态）body client_metadata，替代原 applyCodexResponsesCompatibility
// 与 applyCodexProbeHeaders 的两段构造。metadata marshal 失败显式报错，不
// 静默兜底（原 codexMetadataJSON 的 "{}" 兜底随接线删除）。
func newCodexIdentity() (upstreamenvelope.CodexIdentity, error) {
	sessionID, err := codexUUID()
	if err != nil {
		return upstreamenvelope.CodexIdentity{}, err
	}
	turnID, err := codexUUID()
	if err != nil {
		return upstreamenvelope.CodexIdentity{}, err
	}
	installationID, err := codexUUID()
	if err != nil {
		return upstreamenvelope.CodexIdentity{}, err
	}
	threadID := sessionID
	windowID := threadID + ":0"
	metadata, err := json.Marshal(map[string]any{
		"installation_id":         installationID,
		"session_id":              sessionID,
		"thread_id":               threadID,
		"turn_id":                 turnID,
		"window_id":               windowID,
		"request_kind":            "turn",
		"thread_source":           "user",
		"sandbox":                 "none",
		"turn_started_at_unix_ms": time.Now().UnixMilli(),
	})
	if err != nil {
		return upstreamenvelope.CodexIdentity{}, fmt.Errorf("生成 Codex 身份 metadata 失败: %w", err)
	}
	return upstreamenvelope.CodexIdentity{
		InstallationID:   installationID,
		SessionID:        sessionID,
		ThreadID:         threadID,
		TurnID:           turnID,
		WindowID:         windowID,
		TurnMetadataJSON: string(metadata),
	}, nil
}

func codexUUID() (string, error) {
	value, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("生成 Codex 身份 UUID 失败: %w", err)
	}
	return value.String(), nil
}

func parseBaseURL(raw string, allowInsecure bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("base URL 无效")
	}
	// 2026-09-19 用户决策（PLAN-20260919T000723744Z E2E 后续）：J1 探活
	// 同时允许 http 与 https 上游——dev/mock 上游为纯 HTTP，HTTPS-only
	// 会让额度/临时不可调用账户的 J1 自动恢复结构性失效。探针凭据按
	// base_url scheme 发送，部署方应确保上游地址处于可信网络。
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, errors.New("base URL 必须使用 http 或 https")
	}
	return parsed, nil
}

func decryptToken(secret string, envelope CredentialEnvelope) (string, error) {
	if strings.TrimSpace(secret) == "" || strings.TrimSpace(envelope.Ciphertext) == "" {
		return "", errors.New("凭据 envelope 缺失")
	}
	plaintext, err := DecryptV1Envelope(secret, envelope.Ciphertext)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(plaintext))
	if value == "" {
		return "", errors.New("凭据为空")
	}
	var fields map[string]any
	if json.Unmarshal(plaintext, &fields) == nil {
		for _, key := range []string{"api_key", "access_token", "token", "url"} {
			if text, ok := fields[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text), nil
			}
		}
	}
	return value, nil
}

func verifyResponse(input Input, challenge probeChallenge, body []byte) error {
	// 评分跟随请求编码形态（§9.4 2026-10-12 W3 裁决，与 buildProbeRequest
	// 共用形态判定单源）：Code Assist 恒改道 :streamGenerateContent?alt=sse，
	// A 形态（OAuth Codex）恒 SSE（§9.1）——两者响应统一按 SSE 解析，与 F3
	// 同构；responses_json/responses_sse、generate_content_json/_sse 为账户
	// 配置模式名，不改变业务检测语义。解析选择按请求形态确定，禁止
	// "先试 JSON 失败再试 SSE" 的 fallback。
	if isCodeAssistForm(input) {
		return verifyGeminiSSE(challenge, body)
	}
	if isOAuthCodexForm(input) {
		return verifyResponsesSSE(challenge, body)
	}
	switch input.EndpointMode {
	case "responses_sse":
		return verifyResponsesSSE(challenge, body)
	case "chat_sse":
		return verifyOpenAIChatSSE(challenge, body)
	case "messages_sse":
		return verifyAnthropicSSE(challenge, body)
	case "generate_content_sse":
		return verifyGeminiSSE(challenge, body)
	case "interactions_sse":
		return verifyInteractionsSSE(challenge, body)
	}
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return errors.New("响应不是 JSON")
	}
	if input.ProtocolProfileID == "" {
		if input.EndpointMode == "images_json" {
			return verifyImagesJSON(response)
		}
		if containsChallengeValue(response, challenge.Expected) {
			return nil
		}
		return errors.New("响应未包含挑战值")
	}
	switch input.EndpointMode {
	case "images_json":
		return verifyImagesJSON(response)
	case "chat_json":
		if !chatCompleted(response) || !containsChallengeValue(response, challenge.Expected) {
			return errors.New("Chat Completions 响应未完成挑战")
		}
		return nil
	case "responses_json":
		if textField(response, "status") != "completed" || !(response["object"] == "response" || response["output"] != nil) || !containsChallengeValue(response, challenge.Expected) {
			return errors.New("Responses 响应未完成挑战")
		}
		return nil
	case "messages_json":
		if response["type"] != "message" || textField(response, "stop_reason") == "" || !containsChallengeValue(response, challenge.Expected) {
			return errors.New("Anthropic Messages 响应未完成挑战")
		}
		return nil
	case "generate_content_json":
		if !geminiCandidateComplete(response) || !containsChallengeValue(response, challenge.Expected) {
			return errors.New("Gemini GenerateContent 响应未完成挑战")
		}
		return nil
	case "interactions_json":
		if textField(response, "status") != "completed" || !containsChallengeValue(response, challenge.Expected) {
			return errors.New("Gemini Interactions 响应未完成挑战")
		}
		return nil
	default:
		if containsChallengeValue(response, challenge.Expected) {
			return nil
		}
		return errors.New("响应未包含挑战值")
	}
}

func chatCompleted(response map[string]any) bool {
	choices, ok := response["choices"].([]any)
	if !ok {
		return false
	}
	for _, item := range choices {
		choice, _ := item.(map[string]any)
		if textField(choice, "finish_reason") != "" {
			return true
		}
	}
	return false
}

func verifyImagesJSON(response map[string]any) error {
	data, ok := response["data"].([]any)
	if !ok {
		return errors.New("Images API 响应缺少 data")
	}
	for _, item := range data {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if value := textField(record, "b64_json"); value != "" {
			return nil
		}
		if value := textField(record, "url"); value != "" {
			return nil
		}
	}
	return errors.New("Images API 响应缺少有效图片结果")
}

func textField(value map[string]any, name string) string {
	text, _ := value[name].(string)
	return strings.TrimSpace(text)
}

func geminiCandidateComplete(response map[string]any) bool {
	candidates, ok := response["candidates"].([]any)
	if !ok {
		return false
	}
	for _, item := range candidates {
		candidate, ok := item.(map[string]any)
		if ok && textField(candidate, "finishReason") != "" {
			return true
		}
	}
	return false
}

func verifyResponsesSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	completed := false
	var protocolErr error
	for _, block := range strings.Split(text, "\n\n") {
		if err := consumeResponsesSSEEvent(block, &output, &completed); err != nil {
			var failure *responsesSSEFailure
			if errors.As(err, &failure) {
				return failure
			}
			if protocolErr == nil {
				protocolErr = err
			}
		}
	}
	if protocolErr != nil {
		return protocolErr
	}
	if !completed {
		return errors.New("SSE 缺少 response.completed")
	}
	if !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("SSE 输出未包含挑战值")
	}
	return nil
}

func verifyOpenAIChatSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	done := false
	for _, block := range strings.Split(text, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				done = true
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return errors.New("Chat SSE data 不是 JSON")
			}
			choices, _ := payload["choices"].([]any)
			for _, item := range choices {
				choice, _ := item.(map[string]any)
				if textField(choice, "finish_reason") != "" {
					done = true
				}
				if delta, _ := choice["delta"].(map[string]any); delta != nil {
					output.WriteString(textField(delta, "content"))
				}
				if message, _ := choice["message"].(map[string]any); message != nil {
					output.WriteString(textField(message, "content"))
				}
			}
		}
	}
	if !done || !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("Chat SSE 未完成挑战")
	}
	return nil
}

func verifyAnthropicSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	stopped := false
	for _, block := range strings.Split(text, "\n\n") {
		var eventName string
		var data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return errors.New("Messages SSE data 不是 JSON")
		}
		if kind := textField(payload, "type"); kind != "" {
			eventName = kind
		}
		if eventName == "content_block_delta" {
			if delta, _ := payload["delta"].(map[string]any); delta != nil {
				output.WriteString(textField(delta, "text"))
			}
		}
		if eventName == "message_stop" {
			stopped = true
		}
	}
	if !stopped || !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("Messages SSE 未完成挑战")
	}
	return nil
}

func verifyGeminiSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	finished := false
	for _, block := range strings.Split(text, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				finished = true
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return errors.New("Gemini SSE data 不是 JSON")
			}
			candidatePayload := payload
			if nested, ok := payload["response"].(map[string]any); ok {
				candidatePayload = nested
			}
			if geminiCandidateComplete(candidatePayload) {
				finished = true
			}
			if candidates, ok := candidatePayload["candidates"].([]any); ok {
				for _, item := range candidates {
					candidate, _ := item.(map[string]any)
					content, _ := candidate["content"].(map[string]any)
					parts, _ := content["parts"].([]any)
					for _, part := range parts {
						if record, _ := part.(map[string]any); record != nil {
							output.WriteString(textField(record, "text"))
						}
					}
				}
			}
			if containsChallenge(payload) {
				output.WriteString(probeChallenge)
			}
		}
	}
	if !finished || !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("Gemini SSE 未完成挑战")
	}
	return nil
}

func verifyInteractionsSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	completed := false
	for _, block := range strings.Split(text, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				completed = true
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return errors.New("Interactions SSE data 不是 JSON")
			}
			if textField(payload, "status") == "completed" || textField(payload, "type") == "interaction.completed" {
				completed = true
			}
			if interaction, _ := payload["interaction"].(map[string]any); interaction != nil && textField(interaction, "status") == "completed" {
				completed = true
			}
			if containsChallenge(payload) {
				output.WriteString(probeChallenge)
			}
		}
	}
	if !completed || !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("Interactions SSE 未完成挑战")
	}
	return nil
}

// responsesSSEFailure 保留显式上游失败证据，避免被响应尾部读取错误覆盖。
type responsesSSEFailure struct {
	message string
}

func (failure *responsesSSEFailure) Error() string { return failure.message }

func responsesSSEFailureEvent(eventName string) bool {
	return eventName == "error" || eventName == "response.failed" || eventName == "response.incomplete"
}

func responsesSSEErrorMessage(value any) string {
	if record, ok := value.(map[string]any); ok {
		value = record["message"]
	}
	if message, ok := value.(string); ok && strings.TrimSpace(message) != "" {
		return message
	}
	return ""
}

func consumeResponsesSSEEvent(block string, output *strings.Builder, completed *bool) error {
	var eventName string
	var dataLines []string
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(dataLines) == 0 {
		if responsesSSEFailureEvent(eventName) {
			return &responsesSSEFailure{message: "SSE 上游未完成"}
		}
		return nil
	}
	data := strings.Join(dataLines, "\n")
	if data == "[DONE]" {
		if responsesSSEFailureEvent(eventName) {
			return &responsesSSEFailure{message: "SSE 上游未完成"}
		}
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil || payload == nil {
		return errors.New("SSE data 不是 JSON")
	}
	failed := responsesSSEFailureEvent(eventName)
	if kind := textField(payload, "type"); kind != "" {
		failed = failed || responsesSSEFailureEvent(kind)
		eventName = kind
	}
	response, _ := payload["response"].(map[string]any)
	message := responsesSSEErrorMessage(payload["error"])
	if payload["error"] != nil {
		failed = true
	}
	if response["error"] != nil {
		failed = true
		if message == "" {
			message = responsesSSEErrorMessage(response["error"])
		}
	}
	for _, status := range []string{textField(payload, "status"), textField(response, "status")} {
		if status == "failed" || status == "incomplete" || (eventName == "response.completed" && status != "" && status != "completed") {
			failed = true
		}
	}
	if failed {
		if message == "" {
			message = responsesSSEErrorMessage(payload["message"])
		}
		if message == "" {
			message = "SSE 上游未完成"
		}
		return &responsesSSEFailure{message: message}
	}
	switch eventName {
	case "response.output_text.delta":
		if delta, ok := payload["delta"].(string); ok {
			output.WriteString(delta)
		}
	case "response.output_text.done":
		if finalText, ok := payload["text"].(string); ok {
			output.WriteString(finalText)
		}
	case "response.completed":
		*completed = true
	}
	return nil
}

func containsChallenge(value any) bool {
	switch item := value.(type) {
	case string:
		return strings.Contains(strings.ToLower(item), probeChallenge)
	case []any:
		for _, child := range item {
			if containsChallenge(child) {
				return true
			}
		}
	case map[string]any:
		for _, child := range item {
			if containsChallenge(child) {
				return true
			}
		}
	}
	return false
}

func transportFailure(err error) ProbeResult {
	if errors.Is(err, context.Canceled) {
		return taskFailure("probe_cancelled", "探活已取消")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_timeout", ErrorMessage: "上游请求超时"}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_timeout", ErrorMessage: "上游请求超时"}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_connection_closed", ErrorMessage: "上游提前关闭连接"}
		case errors.Is(err, syscall.ECONNREFUSED):
			return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_connection_refused", ErrorMessage: "上游拒绝连接"}
		case errors.Is(err, syscall.ECONNRESET):
			return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_connection_reset", ErrorMessage: "上游重置连接"}
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_dns", ErrorMessage: "上游域名解析失败"}
		}
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_connection", ErrorMessage: "上游连接失败"}
	}
	return taskFailure("probe_local_failure", "探活本地执行失败")
}

func responseReadFailure(err error) ProbeResult {
	if errors.Is(err, context.Canceled) {
		return taskFailure("probe_cancelled", "探活已取消")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_timeout", ErrorMessage: "上游响应读取超时"}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_timeout", ErrorMessage: "上游响应读取超时"}
	}
	return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_read_incomplete", ErrorMessage: "上游响应读取未完成"}
}

func neutral(status int, code, message string) ProbeResult {
	return ProbeResult{Outcome: OutcomeNeutral, StatusCode: status, ErrorCode: code, ErrorMessage: message}
}

func taskFailure(code, message string) ProbeResult {
	return ProbeResult{Outcome: OutcomeTaskFailed, ErrorCode: code, ErrorMessage: message}
}

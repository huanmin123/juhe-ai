package upstreamenvelope

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// Adapter 是适配器选项位（§4.2：适配器只提供声明式端点、认证及能力描述，
// 不得私有构造最终请求）。GLM / gemini-openai 的路径规约不是适配器，而是
// 按 ProtocolProfileID 的 URL 拼接分支（见 buildUpstreamURL）。
type Adapter string

const (
	AdapterNone Adapter = ""
	// AdapterOpenAIOAuthCodex：OpenAI OAuth Codex 形态（F5）。上游固定为
	// CodexBaseURL；Responses body 归一化删除 max_output_tokens 等不接受的
	// tuning 字段，预算策略恒为 BudgetNone（§4.3 Codex 例外）。包装钩子
	// wrapCodexResponses 逐行为对齐 modelcheckprobe
	// normalizeOpenAIOAuthCodexRequest 的 body 改写（§6 M5）；请求头增强
	// （originator、session-id、x-codex-* 等）属于账户/凭据语义，仍由调用方
	// 经 Spec.Auth / Spec.ExtraHeaders 注入。
	AdapterOpenAIOAuthCodex Adapter = "openai_oauth_codex"
	// AdapterGeminiCodeAssist：Gemini Code Assist / Google One 形态。路径
	// /v1internal:streamGenerateContent?alt=sse，body 包装为
	// {model, project, request} 三字段（accountprobe buildTestRequest /
	// exactkeyprobe buildProbeRequest 现状）；project 由 Spec.Project 传入。
	// 包装钩子见 wrapGeminiCodeAssist（§6 M5）。
	AdapterGeminiCodeAssist Adapter = "gemini_code_assist"
)

// CodexBaseURL 是 OpenAI OAuth Codex 形态的固定上游（accountprobe
// buildUpstreamURL 的硬编码分支与 modelcheckprobe OpenAIOAuthCodexBaseURL
// 同源）。
const CodexBaseURL = "https://chatgpt.com/backend-api/codex"

// wrapFunc 是适配器的 path/body 包装钩子（§6 M5，实现见 adapterwrap.go）：
// 在 body 编码与预算字段注入之后应用，输入已编码 body 与 Spec，输出最终
// path 与 body。登记表不变量：每个适配器必须提供非 nil wrap，Build 遇 nil
// 显式报错，不静默跳过包装。
type wrapFunc func(spec Spec, path string, body []byte) (string, []byte, error)

type adapterDef struct {
	// budget 非空时覆盖 ResolveBudgetPolicy 的默认解析（Codex → BudgetNone）。
	budget *BudgetPolicy
	// fixedBaseURL 非空时替换调用方 base（Codex 固定上游）。
	fixedBaseURL string
	// wrap 是适配器包装钩子（登记项必填，见 wrapFunc）。
	wrap wrapFunc
}

// adapters 是适配器登记表（§8.1：新增变体先登记再使用）。
var adapters = map[Adapter]adapterDef{
	AdapterOpenAIOAuthCodex: {budget: budgetPtr(BudgetNone), fixedBaseURL: CodexBaseURL, wrap: wrapCodexResponses},
	AdapterGeminiCodeAssist: {wrap: wrapGeminiCodeAssist},
}

func budgetPtr(value BudgetPolicy) *BudgetPolicy { return &value }

// GLM 与 Gemini OpenAI 兼容档案（路径规约取证自 exactkeyprobe
// buildProbeRequest / accountprobe buildUpstreamURL）。
const (
	profileGLMGeneralOpenAI = "profile_glm_general_openai_v1"
	profileGLMCodingOpenAI  = "profile_glm_coding_openai_v1"
	profileGeminiOpenAIChat = "profile_gemini_openai_chat_v1beta"
)

// BodyField 是 JSON body 的一个有序字段。探针语义字段（协议必填字段、提示
// 词、schema、tools、图片/音频内容与采样意图）由探针族提供（§3.2）；
// 预算字段由信封按 catalog 与 BudgetPolicy 统一追加（§4.1）。Value 支持
// json.RawMessage 传入已编码片段。
type BodyField struct {
	Key   string
	Value any
}

// MultipartField 是 multipart 表单的文本字段。
type MultipartField struct {
	Name  string
	Value string
}

// MultipartFile 是 multipart 表单的二进制文件段。
type MultipartFile struct {
	FieldName   string
	Filename    string
	ContentType string
	Data        []byte
}

// MultipartContent 是 multipart 端点的 body 内容。
type MultipartContent struct {
	Fields []MultipartField
	File   MultipartFile
}

// Spec 是一次请求构造的全部输入（§4.1：调用方提供已解析的账户/供应商目标、
// 协议 profile、endpointMode、认证策略及凭据引用、探针语义 body 和预算
// 类别；不得传入已拼好的 URL、认证头或完整 envelope 绕过共享入口）。
type Spec struct {
	// BaseURL 是已解析的上游 base；归一化规则见 buildUpstreamURL。
	BaseURL string
	// Protocol 为空时取 catalog 条目的协议；显式传入且与条目不一致时报错。
	Protocol Protocol
	// ProtocolProfileID 驱动 gemini-openai / GLM 路径规约变体（§1 变体表）。
	ProtocolProfileID string
	EndpointMode      EndpointMode
	// Adapter 选择适配器变体；AdapterNone 表示无适配器。
	Adapter Adapter
	// Model 仅 gemini generateContent 的路径模板需要（{model} 占位符）；
	// 其余端点的 model 属于 body 字段，由 BodyFields 承载。
	// AdapterGeminiCodeAssist 例外：包装后的 {model, project, request} 顶层
	// model 字段也取本值（accountprobe / exactkeyprobe 现状：取请求模型原名）。
	Model string
	// Project 仅 AdapterGeminiCodeAssist 使用：Code Assist / Google One 账户
	// 凭据中的 project_id，包装为 body 顶层 project 字段（§6 M5，取证见
	// wrapGeminiCodeAssist）。它是包装协议字段而非 HTTP 头，不经
	// ExtraHeaders 传入；其他适配器忽略本字段。
	Project string
	// Auth 是策略与凭据（见 auth.go）。
	Auth Auth
	// Budget 是调用方解析后的预算策略（通常经 ResolveBudgetPolicy 得到）。
	// 零值视为未选择，Build 报错，不静默兜底。
	Budget BudgetPolicy
	// BodyFields 是 JSON/SSE 端点的有序 body 字段；multipart 端点必须为空。
	BodyFields []BodyField
	// Multipart 仅 multipart 端点使用。
	Multipart *MultipartContent
	// ExtraHeaders 是调用方附加头。系统客户端身份头
	// （upstreamidentity.ApplySystemClientHeaders）在 M1 仍由调用方经此处
	// 注入，是否上收由 M2–M5 迁移步骤裁决。
	ExtraHeaders map[string]string
}

// Build 生成最终 *http.Request（§4.1）：method、URL、标准头、认证头与
// body 编码全部由本包决定。未知 endpointMode、未知适配器、缺少包装钩子的
// 适配器、预算策略与端点契约冲突都返回明确错误，不静默兜底。
func Build(ctx context.Context, spec Spec) (*http.Request, error) {
	entry, err := Lookup(spec.EndpointMode)
	if err != nil {
		return nil, err
	}
	if spec.Protocol != "" && spec.Protocol != entry.Protocol {
		return nil, fmt.Errorf("upstreamenvelope: endpointMode %s 属于协议 %s，与 Spec.Protocol %q 不一致", spec.EndpointMode, entry.Protocol, spec.Protocol)
	}
	var def adapterDef
	if spec.Adapter != AdapterNone {
		var ok bool
		def, ok = adapters[spec.Adapter]
		if !ok {
			return nil, fmt.Errorf("upstreamenvelope: 适配器 %q 未在适配器登记表登记（设计文档 §4.2/§8.1）", spec.Adapter)
		}
		if def.wrap == nil {
			return nil, fmt.Errorf("upstreamenvelope: 适配器 %s 未实现请求包装钩子（适配器登记表不变量）", spec.Adapter)
		}
	}
	if spec.Budget.unspecified() {
		return nil, errors.New("upstreamenvelope: 缺少预算策略（经 ResolveBudgetPolicy 按 endpointMode+probe family+adapter 解析后传入，设计文档 §4.3）")
	}
	if !spec.Budget.IsNone() {
		if spec.Budget.Limit() <= 0 {
			return nil, fmt.Errorf("upstreamenvelope: 预算策略 %s 的上限非法：%d（必须为正）", spec.Budget.Name(), spec.Budget.Limit())
		}
		if entry.BudgetField == BudgetFieldNone {
			return nil, fmt.Errorf("upstreamenvelope: 端点 %s 契约无预算字段，预算策略 %s 不得注入（设计文档 §4.3 不得伪造字段）", entry.Mode, spec.Budget.Name())
		}
	}
	if entry.BudgetRequired && spec.Budget.IsNone() {
		return nil, fmt.Errorf("upstreamenvelope: 端点 %s 的预算字段 %s 为协议必填，不接受 none 策略", entry.Mode, entry.BudgetField)
	}

	path, err := resolveEndpointPath(entry, spec.Model)
	if err != nil {
		return nil, err
	}

	var contentType string
	var body []byte
	switch entry.Encoding {
	case EncodingJSON, EncodingSSE:
		if len(spec.BodyFields) == 0 {
			return nil, fmt.Errorf("upstreamenvelope: 端点 %s 需要 BodyFields 探针语义字段", entry.Mode)
		}
		if spec.Multipart != nil {
			return nil, fmt.Errorf("upstreamenvelope: 端点 %s 为 JSON 编码，不接受 Multipart 内容", entry.Mode)
		}
		body, err = encodeJSONBody(entry, spec.BodyFields, spec.Budget)
		if err != nil {
			return nil, err
		}
		contentType = "application/json"
	case EncodingMultipart:
		if spec.Multipart == nil {
			return nil, fmt.Errorf("upstreamenvelope: 端点 %s 为 multipart 编码，必须提供 Multipart 内容（含 file 段）", entry.Mode)
		}
		if len(spec.BodyFields) > 0 {
			return nil, fmt.Errorf("upstreamenvelope: 端点 %s 为 multipart 编码，不接受 BodyFields", entry.Mode)
		}
		body, contentType, err = encodeMultipartBody(*spec.Multipart)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("upstreamenvelope: 端点 %s 登记了未支持的 body 编码 %q", entry.Mode, entry.Encoding)
	}

	// 适配器包装（Codex Responses 归一化 / Gemini Code Assist 三字段包装）
	// 在 body 编码与预算字段注入之后应用（§6 M5，实现见 adapterwrap.go）。
	if def.wrap != nil {
		path, body, err = def.wrap(spec, path, body)
		if err != nil {
			return nil, err
		}
	}

	target, err := buildUpstreamURL(spec, entry, def, path)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, entry.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("upstreamenvelope: 构造请求失败：%w", err)
	}
	header := httpReq.Header
	header.Set("Accept", acceptHeader(entry, spec.Auth.Strategy))
	header.Set("Content-Type", contentType)
	for name, value := range entry.Headers {
		header.Set(name, value)
	}
	if err := spec.Auth.Apply(header); err != nil {
		return nil, err
	}
	for name, value := range spec.ExtraHeaders {
		header.Set(name, value)
	}
	return httpReq, nil
}

// acceptHeader 按 body 编码与认证策略决定 Accept（现状取证：accountprobe
// executeAttempt 与 exactkeyprobe buildProbeRequest——api_key 探针对 SSE 用
// 双 accept，OAuth/Code Assist 形态保持官方客户端纯流式头；JSON 形态一律
// application/json。modelcheckprobe 现状对 SSE 一律纯流式头，M2 迁移时以本
// 口径收敛）。
func acceptHeader(entry Endpoint, strategy AuthStrategy) string {
	if entry.Encoding != EncodingSSE {
		return "application/json"
	}
	if strategy == AuthOAuth {
		return "text/event-stream"
	}
	return "application/json, text/event-stream"
}

// encodeJSONBody 按条目编码有序 JSON body 并注入预算字段（§4.1）。预算
// 字段追加在调用方字段之后；JSON 字段顺序不影响语义，M2 起由迁移 golden
// 测试钉住最终形状。
func encodeJSONBody(entry Endpoint, fields []BodyField, budget BudgetPolicy) ([]byte, error) {
	all := append([]BodyField(nil), fields...)
	if !budget.IsNone() {
		switch entry.BudgetField {
		case BudgetFieldMaxTokens:
			all = append(all, BodyField{Key: "max_tokens", Value: budget.Limit()})
		case BudgetFieldMaxOutputTokens:
			all = append(all, BodyField{Key: "max_output_tokens", Value: budget.Limit()})
		case BudgetFieldGeminiGenerationConfig:
			merged, err := applyGeminiBudgetField(all, budget.Limit())
			if err != nil {
				return nil, err
			}
			all = merged
		default:
			return nil, fmt.Errorf("upstreamenvelope: 端点 %s 登记了未支持的预算字段 %q", entry.Mode, entry.BudgetField)
		}
	}
	return encodeOrderedJSON(all)
}

// applyGeminiBudgetField 把 maxOutputTokens 注入 generationConfig：字段已
// 存在时合并（音频等场景调用方自带 responseModalities / speechConfig），
// 不存在时追加。
func applyGeminiBudgetField(fields []BodyField, limit int) ([]BodyField, error) {
	for index, field := range fields {
		if field.Key != "generationConfig" {
			continue
		}
		encoded, err := json.Marshal(field.Value)
		if err != nil {
			return nil, fmt.Errorf("upstreamenvelope: 编码 generationConfig 失败：%w", err)
		}
		var config map[string]any
		if err := json.Unmarshal(encoded, &config); err != nil || config == nil {
			return nil, errors.New("upstreamenvelope: generationConfig 必须为 JSON 对象才能注入 maxOutputTokens")
		}
		config["maxOutputTokens"] = limit
		out := append([]BodyField(nil), fields...)
		out[index] = BodyField{Key: "generationConfig", Value: config}
		return out, nil
	}
	return append(fields, BodyField{Key: "generationConfig", Value: map[string]any{"maxOutputTokens": limit}}), nil
}

// encodeOrderedJSON 按字段顺序编码 JSON 对象（与 accountprobe orderedJSON
// 同构）；序列化失败显式报错，不静默降级。
func encodeOrderedJSON(fields []BodyField) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for index, field := range fields {
		if index > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(field.Key)
		if err != nil {
			return nil, fmt.Errorf("upstreamenvelope: 编码 body 字段名 %q 失败：%w", field.Key, err)
		}
		buf.Write(key)
		buf.WriteByte(':')
		value, err := json.Marshal(field.Value)
		if err != nil {
			return nil, fmt.Errorf("upstreamenvelope: 编码 body 字段 %q 失败：%w", field.Key, err)
		}
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// encodeMultipartBody 编码 multipart/form-data（文本字段 + 二进制 file 段），
// 返回 body 与含 boundary 的 Content-Type（§1：共享构造需支持边界编码与
// 二进制文件段）。
func encodeMultipartBody(content MultipartContent) ([]byte, string, error) {
	if len(content.File.Data) == 0 {
		return nil, "", errors.New("upstreamenvelope: multipart 内容必须携带非空 file 段")
	}
	if strings.TrimSpace(content.File.FieldName) == "" {
		return nil, "", errors.New("upstreamenvelope: multipart file 段缺少字段名")
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, field := range content.Fields {
		if err := writer.WriteField(field.Name, field.Value); err != nil {
			return nil, "", fmt.Errorf("upstreamenvelope: 写入 multipart 字段 %q 失败：%w", field.Name, err)
		}
	}
	disposition := mime.FormatMediaType("form-data", map[string]string{
		"name":     content.File.FieldName,
		"filename": content.File.Filename,
	})
	if disposition == "" {
		return nil, "", errors.New("upstreamenvelope: multipart file 段的 name/filename 非法")
	}
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", disposition)
	if contentType := strings.TrimSpace(content.File.ContentType); contentType != "" {
		partHeader.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return nil, "", fmt.Errorf("upstreamenvelope: 创建 multipart file 段失败：%w", err)
	}
	if _, err := part.Write(content.File.Data); err != nil {
		return nil, "", fmt.Errorf("upstreamenvelope: 写入 multipart file 段失败：%w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("upstreamenvelope: 关闭 multipart 编码失败：%w", err)
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

// resolveEndpointPath 展开端点路径模板；{model} 仅 gemini generateContent
// 使用，替换值为 geminiModelPath（含 models/ 前缀）。
func resolveEndpointPath(entry Endpoint, model string) (string, error) {
	if !strings.Contains(entry.PathTemplate, "{model}") {
		return entry.PathTemplate, nil
	}
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("upstreamenvelope: 端点 %s 的路径模板需要 Spec.Model", entry.Mode)
	}
	return strings.Replace(entry.PathTemplate, "{model}", geminiModelPath(model), 1), nil
}

// buildUpstreamURL 逐行为对齐 accountprobe buildUpstreamURL（probe.go :1423），
// 并补充 exactkeyprobe 的 GLM OpenAI 路径规约：
//   - OpenAI 系：base 归一化补 /v1，剥请求路径 /v1 前缀；
//   - Anthropic：base 原样去尾斜杠保留全路径（不以 /v1 结尾时补 /v1）；
//   - Gemini：base pathname 拼接（base 以 /v1beta 结尾时剥请求 /v1beta 前缀）；
//   - profile_gemini_openai_chat_v1beta：base 以 /v1beta/openai 结尾时按
//     gateway 与 J1 同规剥请求 /v1 前缀；
//   - GLM OpenAI 档案：官方根路径自带版本化服务路径（/api/paas/v4 等）时
//     剥请求 /v1 前缀，常规 /v1 根（或空路径）保留请求 /v1；
//   - 适配器 fixedBaseURL（Codex）：固定上游 + 剥 /v1 的请求路径。
func buildUpstreamURL(spec Spec, entry Endpoint, def adapterDef, pathAndQuery string) (string, error) {
	base := strings.TrimSpace(spec.BaseURL)
	if base == "" {
		return "", errors.New("upstreamenvelope: 缺少上游 base_url")
	}
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", fmt.Errorf("upstreamenvelope: base_url 无效：%w", err)
	}
	if entry.Protocol == ProtocolGemini {
		basePath := strings.TrimRight(parsed.Path, "/")
		suffixPath, suffixQuery := splitPathAndQuery(pathAndQuery)
		if strings.HasSuffix(basePath, "/v1beta") {
			suffixPath = stripV1BetaPrefix(suffixPath)
		}
		parsed.Path = strings.ReplaceAll(basePath+stripTrailingSlashPath(suffixPath), "//", "/")
		parsed.RawQuery = strings.TrimPrefix(suffixQuery, "?")
		return parsed.String(), nil
	}
	if spec.ProtocolProfileID == profileGeminiOpenAIChat {
		// Gemini OpenAI 兼容档案自带完整 /v1beta/openai 服务路径；仅在该
		// canonical base 下剥请求 /v1 前缀（accountprobe 与 gateway/J1 同规）。
		basePath := strings.TrimRight(parsed.Path, "/")
		if strings.HasSuffix(strings.ToLower(basePath), "/v1beta/openai") {
			suffixPath, suffixQuery := splitPathAndQuery(openAIPathSuffix(pathAndQuery))
			parsed.Path = basePath + suffixPath
			parsed.RawQuery = strings.TrimPrefix(suffixQuery, "?")
			return parsed.String(), nil
		}
	}
	if def.fixedBaseURL != "" {
		fixed, err := url.Parse(def.fixedBaseURL)
		if err != nil {
			return "", fmt.Errorf("upstreamenvelope: 适配器固定 base_url 无效：%w", err)
		}
		fixed.Path = strings.TrimRight(fixed.Path, "/") + openAIPathSuffix(pathAndQuery)
		return fixed.String(), nil
	}
	if entry.Protocol == ProtocolAnthropic {
		normalizedBase := strings.TrimRight(parsed.String(), "/")
		if !strings.HasSuffix(strings.ToLower(strings.TrimRight(parsed.Path, "/")), "/v1") {
			normalizedBase += "/v1"
		}
		return normalizedBase + openAIPathSuffix(pathAndQuery), nil
	}
	if isGLMOpenAIProfile(spec.ProtocolProfileID) && !glmOpenAIBaseOwnsV1Path(parsed) {
		// GLM 官方根路径已含版本化服务路径（/api/paas/v4 等），剥请求 /v1
		// 前缀；OpenAI 兼容代理的常规 /v1 根保留请求 /v1（exactkeyprobe
		// buildProbeRequest 同规，避免 /v1/chat 被拼成 /chat）。
		return parsed.String() + openAIPathSuffix(pathAndQuery), nil
	}
	normalizedBase := parsed.String()
	if !strings.HasSuffix(normalizedBase, "/v1") {
		normalizedBase += "/v1"
	}
	return normalizedBase + openAIPathSuffix(pathAndQuery), nil
}

// UpstreamURL 是 URL 归一化的导出入口（§4.1 L1）：供迁移中的调用方把私有
// 拼接迁到共享规则——modelcheckprobe buildURL（M2）。寻址规则与 Build 完全
// 一致；认证头与 body 编码仍由 Build 承担。
func UpstreamURL(baseURL string, mode EndpointMode, protocolProfileID string, adapter Adapter, pathAndQuery string) (string, error) {
	entry, err := Lookup(mode)
	if err != nil {
		return "", err
	}
	spec := Spec{BaseURL: baseURL, Protocol: entry.Protocol, ProtocolProfileID: protocolProfileID, EndpointMode: mode, Adapter: adapter}
	var def adapterDef
	if adapter != AdapterNone {
		d, ok := adapters[adapter]
		if !ok {
			return "", fmt.Errorf("upstreamenvelope: 适配器 %q 未在适配器登记表登记（设计文档 §4.2/§8.1）", adapter)
		}
		def = d
	}
	return buildUpstreamURL(spec, entry, def, pathAndQuery)
}

// AcceptHeader 是端点的 Accept 标准头口径（§4.1 L1）：JSON 形态
// application/json；SSE 形态 api-key 双 accept、OAuth/Code Assist 纯流式
// （现状取证：accountprobe/exactkeyprobe；modelcheckprobe 已于 M2 收敛）。
func AcceptHeader(mode EndpointMode, strategy AuthStrategy) (string, error) {
	entry, err := Lookup(mode)
	if err != nil {
		return "", err
	}
	return acceptHeader(entry, strategy), nil
}

// openAIPathSuffix 等价 accountprobe openAIPathSuffix：确保前导斜杠并剥一次
// /v1 前缀（Node replace(/^\/v1(?=\/|$)/, ”)），保留 query。
func openAIPathSuffix(pathAndQuery string) string {
	path, query := splitPathAndQuery(pathAndQuery)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) >= 3 && path[:3] == "/v1" && (len(path) == 3 || path[3] == '/') {
		path = path[3:]
	}
	if path == "/" {
		path = ""
	}
	return path + query
}

func splitPathAndQuery(pathAndQuery string) (path, query string) {
	if index := strings.Index(pathAndQuery, "?"); index >= 0 {
		return pathAndQuery[:index], pathAndQuery[index:]
	}
	return pathAndQuery, ""
}

// stripV1BetaPrefix 等价 accountprobe stripV1BetaPrefix：剥一次不区分大小写
// 的 /v1beta/ 前缀（保留后续路径的前导斜杠）。
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

// glmOpenAIBaseOwnsV1Path 等价 exactkeyprobe glmOpenAIBaseOwnsV1Path：空路径
// 与以 /v1 结尾的 base 视为常规 OpenAI 服务根（保留请求 /v1 前缀）。
func glmOpenAIBaseOwnsV1Path(base *url.URL) bool {
	if base == nil {
		return false
	}
	path := strings.TrimRight(base.Path, "/")
	return path == "" || strings.HasSuffix(path, "/v1")
}

func isGLMOpenAIProfile(profileID string) bool {
	return profileID == profileGLMGeneralOpenAI || profileID == profileGLMCodingOpenAI
}

// geminiModelPath 等价 accountprobe geminiModelPath：剥一次不区分大小写的
// models/ 前缀，空值回落 gemini-pro，按 encodeURIComponent 保留字符集转义。
func geminiModelPath(model string) string {
	normalized := strings.TrimSpace(model)
	if len(normalized) >= 7 && strings.EqualFold(normalized[:7], "models/") {
		normalized = normalized[7:]
	}
	if normalized == "" {
		normalized = "gemini-pro"
	}
	return "models/" + urlPathEscape(normalized)
}

// urlPathEscape 按 Node encodeURIComponent 的保留字符集转义，与
// accountprobe geminiModelPath 的上游路径形态一致（net/url.PathEscape 的
// 保留集不同，会造成路径分叉，不得替换）。
func urlPathEscape(value string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	out := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		char := value[index]
		if strings.IndexByte(unreserved, char) >= 0 {
			out = append(out, char)
			continue
		}
		out = append(out, '%', hexDigit(char>>4), hexDigit(char&0x0f))
	}
	return string(out)
}

func hexDigit(value byte) byte {
	if value < 10 {
		return '0' + value
	}
	return 'A' + (value - 10)
}

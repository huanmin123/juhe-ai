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
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// Adapter 是适配器选项位（§4.2：适配器只提供声明式端点、认证及能力描述，
// 不得私有构造最终请求）。URL 寻址统一走 JoinUpstreamURL 的 R1 直拼（v2 契约
// 删除了按 ProtocolProfileID 的 GLM / gemini-openai 路径规约分支）——协议
// 档案差异只由 base_url 版本根（R3）与 catalog 操作路径（R2）表达。
type Adapter string

const (
	AdapterNone Adapter = ""
	// AdapterOpenAIOAuthCodex：OpenAI OAuth Codex 形态（A，§9.1-A）。上游
	// 固定为 CodexBaseURL；Responses body 归一化删除 max_output_tokens 等不
	// 接受的 tuning 字段，预算策略恒为 BudgetNone（§4.3 Codex 例外）。包装
	// 钩子 wrapCodexResponses 逐行为对齐 modelcheckprobe
	// normalizeOpenAIOAuthCodexRequest 的 body 改写。强制 SSE（Accept 恒
	// text/event-stream）、openai-beta 缺省补齐与 Codex 身份头由 Build 按
	// Spec 形态统一写入（§9.1/§9.3/§9.5）；身份值经 Spec.CodexIdentity 传入，
	// 缺失显式失败。
	AdapterOpenAIOAuthCodex Adapter = "openai_oauth_codex"
	// AdapterOpenAICodexCompatibility：codex_responses 兼容画像形态（B，
	// API Key + ClientCompatibility，§9.1-B）。不设固定上游：URL 走账户
	// BaseURL + JoinUpstreamURL 直拼（R1/R3，base_url 携带版本根）；预算不
	// 覆盖（B 保留档案/探针族预算字段，不继承 A 的 BudgetNone 例外）；包装
	// 钩子 wrapCodexCompatibilityProfile 注入 store/include/reasoning/
	// parallel_tool_calls/prompt_cache_key/client_metadata（与身份头共用
	// Spec.CodexIdentity 一份身份，§9.3 单源）。身份头由 Build 统一写入
	//（集合与 A 相同），Accept 按 endpointMode 走现行 acceptHeader 口径，
	// 不强制 SSE、不补 openai-beta；Lite 头按 §9.5 模型条件与 A 同规。
	AdapterOpenAICodexCompatibility Adapter = "openai_codex_compatibility"
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
	AdapterOpenAIOAuthCodex:         {budget: budgetPtr(BudgetNone), fixedBaseURL: CodexBaseURL, wrap: wrapCodexResponses},
	AdapterOpenAICodexCompatibility: {wrap: wrapCodexCompatibilityProfile},
	AdapterGeminiCodeAssist:         {wrap: wrapGeminiCodeAssist},
}

func budgetPtr(value BudgetPolicy) *BudgetPolicy { return &value }

// GLM 与 Gemini OpenAI 兼容档案（档案 ID 词表取证自 exactkeyprobe
// buildProbeRequest / accountprobe buildUpstreamURL；URL 行为自版本根语义
// v2 起统一走 R1 直拼，不再按 GLM 档案分支）。
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

// CodexIdentity 是一次 Codex 形态请求的身份值（A: OAuth Codex / B:
// codex_responses 兼容画像，设计文档 §9.3 身份单源）。身份由调用方在每次
// 请求前生成（含时钟/随机源取时，信封不持有时钟/随机源之外的身份状态），
// 经 Spec 传入 Build 后同时产出 body client_metadata（B 形态，
// wrapCodexCompatibilityProfile）与全部身份请求头（Build）——禁止两个构造
// 阶段各随机生成。TurnMetadataJSON 是调用方 marshal 好的 turn metadata
// JSON 字符串（F4 现值含 turn_started_at_unix_ms）。零值=未提供身份。
type CodexIdentity struct {
	InstallationID   string
	SessionID        string
	ThreadID         string
	TurnID           string
	WindowID         string
	TurnMetadataJSON string
}

// Spec 是一次请求构造的全部输入（§4.1：调用方提供已解析的账户/供应商目标、
// 协议 profile、endpointMode、认证策略及凭据引用、探针语义 body 和预算
// 类别；不得传入已拼好的 URL、认证头或完整 envelope 绕过共享入口）。
type Spec struct {
	// BaseURL 是已解析的上游 base（版本根语义 R3：版本段只随 base 传入）；
	// 拼接规则见 JoinUpstreamURL。
	BaseURL string
	// Protocol 为空时取 catalog 条目的协议；显式传入且与条目不一致时报错。
	Protocol Protocol
	// ProtocolProfileID 是协议档案标识（§1 变体表）。自版本根语义 v2（R7）
	// 起不再驱动 URL 拼接分支，仅作为档案标识随 Spec 透传（调用方日志/评分
	// 归档仍可用）；档案差异由 base_url 版本根表达。
	ProtocolProfileID string
	EndpointMode      EndpointMode
	// Adapter 选择适配器变体；AdapterNone 表示无适配器。
	Adapter Adapter
	// Model 仅 gemini generateContent 的路径模板需要（{model} 占位符）；
	// 其余端点的 model 属于 body 字段，由 BodyFields 承载。
	// AdapterGeminiCodeAssist 例外：包装后的 {model, project, request} 顶层
	// model 字段也取本值（accountprobe / exactkeyprobe 现状：取请求模型原名）。
	// Codex 形态适配器（AdapterOpenAIOAuthCodex / AdapterOpenAICodex
	// Compatibility）语义扩展：最终上游模型（模型映射后），Build 按 §9.5
	// 以本值决定 X-OpenAI-Internal-Codex-Responses-Lite 是否发送。
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
	// CodexIdentity 仅 Codex 形态适配器（A/B，见 isCodexFormAdapter）使用：
	// 调用方每次请求生成的身份值（§9.3 单源）。零值=无身份，Codex 形态缺失
	// 时 Build 显式报错（防静默丢身份）；其他适配器忽略本字段。
	CodexIdentity CodexIdentity
	// ExtraHeaders 是调用方附加头。Codex 形态身份头（originator、session-id、
	// x-codex-* 等，§9.1）自 W1 起由 Build 统一写入，先于本映射应用——调用
	// 方仍可经此处覆盖单项（保留既有覆盖语义）；其余系统客户端身份头
	//（upstreamidentity.ApplySystemClientHeaders）是否上收由 M2–M5 迁移
	// 步骤另行裁决。
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
	if isCodexFormAdapter(spec.Adapter) && spec.CodexIdentity == (CodexIdentity{}) {
		return nil, fmt.Errorf("upstreamenvelope: Codex 形态适配器 %s 缺少 Spec.CodexIdentity（设计文档 §9.3 身份单源：身份由调用方每次请求生成后传入，缺失必须显式失败，防静默丢身份）", spec.Adapter)
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
	if spec.Adapter == AdapterOpenAIOAuthCodex {
		// A 形态强制 SSE（§9.1）：Accept 恒 text/event-stream，覆盖
		// acceptHeader 结果；B/C 形态不强制，走现行 acceptHeader 口径。
		header.Set("Accept", "text/event-stream")
	}
	header.Set("Content-Type", contentType)
	for name, value := range entry.Headers {
		header.Set(name, value)
	}
	if err := spec.Auth.Apply(header); err != nil {
		return nil, err
	}
	if isCodexFormAdapter(spec.Adapter) {
		// A/B 形态身份头单源（§9.1/§9.3）：与 body client_metadata 共用
		// Spec.CodexIdentity 一份身份；先于 ExtraHeaders 写入，调用方仍可
		// 经 ExtraHeaders 覆盖（保留既有覆盖语义）。
		applyCodexIdentityHeaders(header, spec.CodexIdentity, spec.Model)
	}
	for name, value := range spec.ExtraHeaders {
		header.Set(name, value)
	}
	if spec.Adapter == AdapterOpenAIOAuthCodex {
		// OAuth Codex 的响应编码由适配器固定，附加头不能将它改回 JSON。
		header.Set("Accept", "text/event-stream")
	}
	if spec.Adapter == AdapterOpenAIOAuthCodex && header.Get("OpenAI-Beta") == "" {
		// A 形态 openai-beta 缺省补齐（§9.5）；ExtraHeaders 已带显式值时保留，
		// 不覆盖（OpenAI账号接入 透传策略同源）。仅 A 形态执行；B/C 不补。
		header.Set("OpenAI-Beta", "responses=experimental")
	}
	return httpReq, nil
}

// isCodexFormAdapter 报告适配器是否为 Codex 形态（A: OAuth Codex；B:
// codex_responses 兼容画像）：两形态共享同一身份头集合与 §9.5 Lite 头模型
// 条件；强制 SSE 与 openai-beta 缺省补齐仅 A 形态适用。
func isCodexFormAdapter(adapter Adapter) bool {
	return adapter == AdapterOpenAIOAuthCodex || adapter == AdapterOpenAICodexCompatibility
}

// applyCodexIdentityHeaders 写入 A/B 形态共享的身份头集合（§9.1；取证：
// exactkeyprobe applyCodexProbeHeaders——X-Client-Request-Id 与 Session-Id
// 同取身份 SessionID，与 F3/F4 现状一致）。仅最终上游模型命中 §9.5 Lite
// 清单时附加 X-OpenAI-Internal-Codex-Responses-Lite: true，其余模型不发送。
func applyCodexIdentityHeaders(header http.Header, identity CodexIdentity, model string) {
	header.Set("Originator", "codex_exec")
	header.Set("User-Agent", upstreamidentity.EffectiveCodexUserAgent())
	header.Set("Session-Id", identity.SessionID)
	header.Set("Thread-Id", identity.ThreadID)
	header.Set("X-Client-Request-Id", identity.SessionID)
	header.Set("X-Codex-Beta-Features", "remote_compaction_v2")
	header.Set("X-Codex-Window-Id", identity.WindowID)
	header.Set("X-Codex-Turn-Metadata", identity.TurnMetadataJSON)
	if codexLiteModel(model) {
		header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	}
}

// codexLiteModel 按 §9.5 判断最终上游模型是否命中 Codex Responses Lite
// 清单：小写去空白后精确等于 gpt-5.6-sol / gpt-5.6-terra / gpt-5.6-luna
// （与 exactkeyprobe isCodexResponsesLiteModel 同规）。目录扩减模型时本清单
// 随模型目录裁决更新，不做前缀/通配推断。
func codexLiteModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return true
	}
	return false
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

// encodeOrderedJSON 按字段顺序编码 JSON 对象（§9.2-3：最终编码只由共享包
// 负责）；序列化失败显式报错，不静默降级。
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

// JoinUpstreamURL 是上游最终 URL 的唯一构造规则（上游 Base URL 版本根语义
// v2 R1/R7）：最终 URL = TrimRight(base_url, "/") + opPath；opPath 以 / 开头、
// 可含 query。运行时不对 base 或 opPath 补插、剥离、去重任何版本段——版本
// 段（/v1、/v1beta、/api/v3、/api/paas/v4、/anthropic/v1 等）只存在于
// base_url 版本根（R3）。Build/buildUpstreamURL/UpstreamURL 与 upstreamcatalog
// 的 URL 构造全部经本函数拼接。
func JoinUpstreamURL(baseURL, opPathWithQuery string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return "", errors.New("upstreamenvelope: 缺少上游 base_url")
	}
	opPath := strings.TrimSpace(opPathWithQuery)
	if !strings.HasPrefix(opPath, "/") {
		return "", fmt.Errorf("upstreamenvelope: 操作路径必须以 / 开头，收到 %q", opPathWithQuery)
	}
	return strings.TrimRight(base, "/") + opPath, nil
}

// buildUpstreamURL 按 R1 直拼生成最终 URL：catalog opPath 已不含版本前缀
// （R2），版本段只随 base_url 版本根传入（R3），拼接规则见 JoinUpstreamURL。
// 唯一分支是适配器 fixedBaseURL（Codex）：固定上游整体替换账户 base。运行时
// 不对 base 或 opPath 补插、剥离、去重任何版本段（v2 契约删除了旧 Gemini
// EscapedPath 重组、gemini-openai-chat 豁免、Anthropic/GLM/通用补 /v1 与剥
// 前缀分支——{model} 展开值已由 geminiModelPath 按 encodeURIComponent 转义，
// 直拼保持其转义形态，无需 url.URL 重组）。
func buildUpstreamURL(spec Spec, entry Endpoint, def adapterDef, pathAndQuery string) (string, error) {
	if def.fixedBaseURL != "" {
		return JoinUpstreamURL(def.fixedBaseURL, pathAndQuery)
	}
	return JoinUpstreamURL(spec.BaseURL, pathAndQuery)
}

// UpstreamURL 是 URL 归一化的导出入口（§4.1 L1）：供迁移中的调用方把私有
// 拼接迁到共享规则——modelcheckprobe buildURL（M2）。寻址规则与 Build 完全
// 一致（R1 直拼：base_url 携带版本根，pathAndQuery 为去版本前缀的操作路径，
// 见 JoinUpstreamURL）；认证头与 body 编码仍由 Build 承担。
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

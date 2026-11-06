import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE
} from '@/shared/providerProtocol'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { accountImportProtocolMarkdown, aiConversionPrompt, importTemplate } from '../../views/accounts/accountImportProtocol'

const importModalSource = readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), '../../views/accounts/AccountImportModal.vue'),
  'utf8'
)
const sourceExampleBlock = sourceBetween(importModalSource, 'const sourceExample = computed', 'const accountColumns')

interface ImportTemplateAccount {
  ref?: string
  providerCode?: string
  providerProtocolProfileId?: string
  clientCompatibility?: string
  type?: string
  credentials?: Record<string, unknown>
  supportedModels?: unknown
  connections?: unknown
  healthCheckConnectionPresetCode?: unknown
  healthCheckModel?: unknown
  healthCheckEndpointMode?: unknown
  rpmLimit?: unknown
  modelMappings?: Array<{
    sourceModel?: string
    sourceEndpointFamily?: string
    upstreamModel?: string
    upstreamEndpointFamily?: string
    enabled?: boolean
  }>
  groupName?: string
}

interface ImportTemplateDocument {
  type?: string
  version?: number
  proxies?: unknown[]
  accounts?: ImportTemplateAccount[]
}

interface ProtocolSemanticRequirement {
  name: string
  exportedPattern: RegExp
  formalPattern: RegExp
}

type DynamicImport = (specifier: string) => Promise<unknown>

const dynamicImport = new Function('specifier', 'return import(specifier)') as DynamicImport
const nodeFs = await dynamicImport('node:fs') as { readFileSync: (path: string, encoding: 'utf8') => string }
const nodePath = await dynamicImport('node:path') as {
  dirname: (path: string) => string
  resolve: (...segments: string[]) => string
}
const nodeUrl = await dynamicImport('node:url') as { fileURLToPath: (url: string) => string }
const repoRoot = nodePath.resolve(nodePath.dirname(nodeUrl.fileURLToPath(import.meta.url)), '../../../..')
const formalProtocolMarkdown = nodeFs.readFileSync(nodePath.resolve(repoRoot, 'docs/functions/AI账户导入协议.md'), 'utf8')
const protocolSemanticRequirements: readonly ProtocolSemanticRequirement[] = [
  {
    name: '账户级健康检查模型与请求形态',
    exportedPattern: /\| `healthCheckEndpointMode` \| 是 \| string \| 健康检查三件套之一；后台健康检查精确请求形态，可填 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`（另有 `images_json`、`audio_speech`、`audio_transcription_json`、`video_create` 受模型目录与域能力约束），且必须属于 `healthCheckConnectionPresetCode` 指向连接的 `endpointModes`。 \|/,
    formalPattern: /\| `healthCheckEndpointMode` \| 是 \| 健康检查三件套之一；后台健康检查精确请求形态。允许 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`、`images_json`。`images_json` 只允许模型目录证明检查模型支持 `images` 时使用。必须属于 `healthCheckConnectionPresetCode` 指向连接的 `endpointModes`，且检查模型须在模型目录声明该形态。非原生来源省略时按流式优先解析（\[AI账户检查模型与人工测试设计\]\(AI账户检查模型与人工测试设计\.md\) §健康检查请求形态 2026-10-19 定案）：先从连接 `endpointModes` 取首个可检查 `_sse` 形态，无 `_sse` 才按声明顺序回退；`native` 导入必须显式提交，不做缺省解析。 \|/
  },
  {
    name: 'connections 与健康检查三件套必填',
    exportedPattern: /每个账户必须显式提交非空 `connections` 数组和健康检查三件套 `healthCheckConnectionPresetCode`、`healthCheckModel`、`healthCheckEndpointMode`；native 导入不做任何缺省补齐，缺一个就整账户失败。/,
    formalPattern: /每个账户必须显式提交非空 `connections` 数组和健康检查三件套 `healthCheckConnectionPresetCode`、`healthCheckModel`、`healthCheckEndpointMode`；`native` 导入不做缺省补齐，四键缺任一该账户在预览中标记失败/
  },
  {
    name: 'connections 条目契约与三件套指向约束',
    exportedPattern: /## connections 条目字段[\s\S]*?\| `connectionPresetCode` \| 是 \| string \| 连接器注册表预设标识[\s\S]*?\| `baseUrl` \| 是 \| string \| 包含版本前缀的根地址[\s\S]*?\| `endpointModes` \| 是 \| string\[\] \| 非空字符串数组，无重复[\s\S]*?\| `enabled` \| 是 \| boolean \| 是否启用；必须显式填写，省略或非布尔即失败。健康检查只能指向 `enabled: true` 的连接。 \|/,
    formalPattern: /## connections 条目字段[\s\S]*?\| `connectionPresetCode` \| 是 \| 连接器注册表预设标识[\s\S]*?\| `enabled` \| 是 \| 是否启用；必须显式填写，省略或非布尔即失败。健康检查只能指向 `enabled: true` 的连接。 \|/
  },
  {
    name: 'xAI / Grok OAuth 约束',
    exportedPattern: /`providerCode = xai` 必须使用 `profile_xai_openai_v1`，可选 `api_key` 或 `oauth`。API Key 必须有 `credentials\.api_key`，通常使用 `https:\/\/api\.x\.ai\/v1`；Grok OAuth 必须有 `credentials\.access_token`，通常使用 `https:\/\/cli-chat-proxy\.grok\.com\/v1`，`supported_endpoint_modes` 只能是 `responses_json`、`responses_sse`。导入 JSON 不接受 SSO、`sso`、`sso-rw` 或 Cookie 字段/,
    formalPattern: /`providerCode = xai` 允许 `type = api_key\|oauth`，并必须使用 `profile_xai_openai_v1`。API Key 必须有 `credentials\.api_key`，默认地址 `https:\/\/api\.x\.ai\/v1`；OAuth 必须有 `credentials\.access_token`，可选 Refresh \/ ID Token 和公开 claims，默认地址 `https:\/\/cli-chat-proxy\.grok\.com\/v1`，endpoint modes 只能是 `responses_json\/responses_sse`。SSO Cookie 不属于通用导入凭据，必须通过 Grok 专属 `\/sso-to-oauth` 接口转换，导入 JSON 不接受 `sso`、`sso-rw` 或 Cookie 字段/
  },
  {
    name: 'Gemini Google OAuth 模式与 GenerateContent 约束',
    exportedPattern: /Gemini `google_oauth` 必须使用 `profile_gemini_native_v1beta` 并显式填写 `oauth_type`。`ai_studio` 使用 `https:\/\/generativelanguage\.googleapis\.com\/v1beta`；`code_assist`、`google_one` 必须有 `project_id`，使用 `https:\/\/cloudcode-pa\.googleapis\.com`，并且只能启用 `generate_content_json`、`generate_content_sse`，不能借 endpoint modes 放开 Interactions、Count Tokens 或 Embedding/,
    formalPattern: /`oauth_type = code_assist\|google_one` 时 `credentials\.base_url` 使用 `https:\/\/cloudcode-pa\.googleapis\.com`、必须有 `project_id`，endpoint modes 只能是 `generate_content_json\/generate_content_sse`；`code_assist` 默认 `tier_id=gcp_standard`，`google_one` 默认 `tier_id=google_one_free`。`oauth_type = ai_studio` 时使用 `https:\/\/generativelanguage\.googleapis\.com\/v1beta`（Gemini 原生版本根）、自定义 Google OAuth client 和 native 档案 endpoint modes，默认 `tier_id=aistudio_free`/
  },
  {
    name: '服务等级与思考级别覆盖边界',
    exportedPattern: /文本模型账户可选填写字符串 `service_tier_override`、`reasoning_effort_override`；空值表示不覆盖客户端请求。保存校验只做 token 形状与协议族全集校验[\s\S]*?不再要求已选模型在当前供应商目录声明该值[\s\S]*?思考级别按档位序向下兼容降级到不高于配置值的最高可用档[\s\S]*?服务等级不降级/,
    formalPattern: /`service_tier_override` 与 `reasoning_effort_override` 在模型映射和协议桥接确定实际上游模型后逐字段判断；思考级别按档位序向下兼容降级到最终模型声明值中不高于配置值的最高档[\s\S]*?服务等级不降级，`default` 恒删除客户端 `service_tier`，`priority`\/`flex` 仅在最终模型声明支持时写入/
  },
  {
    name: '模型映射与跨协议边界',
    exportedPattern: /`modelMappings` 只做账号模型别名和 OpenAI v1 bridge：Chat Completions → Chat Completions，Responses → Responses，Responses → Chat Completions（仅 stream=true），Messages → Messages，GenerateContent \/ 流式 GenerateContent → GenerateContent，video_generation → video_generation，tts → tts。其他跨协议方向会被拒绝/,
    formalPattern: /`modelMappings` 只做账号模型别名和 OpenAI v1 bridge：Chat Completions → Chat Completions，Responses → Responses，Responses → Chat Completions（仅 stream=true），Messages → Messages，GenerateContent \/ 流式 GenerateContent → GenerateContent，video_generation → video_generation，tts → tts。其他跨协议方向会被拒绝/
  },
  {
    name: '来源模型映射与模型目录拦截',
    exportedPattern: /## 来源模型映射与目录拦截[\s\S]*?Channel 的 `models` 字段（逗号 \/ 分号 \/ 换行分隔）映射为账户 `supportedModels`[\s\S]*?账户支持模型必须已存在于平台模型目录（含价格）[\s\S]*?账户支持模型不在供应商模型目录中/,
    formalPattern: /## 来源模型映射与目录拦截[\s\S]*?Channel 的 `models` 字段（逗号 \/ 分号 \/ 换行分隔）[\s\S]*?映射为账户 `supportedModels`，去空白、去重、保序[\s\S]*?账户支持模型必须已存在于平台模型目录（含价格）[\s\S]*?请先在模型目录创建这些模型并配置价格后再导入/
  }
]

const template = JSON.parse(importTemplate) as ImportTemplateDocument

assertEqual(template.type, 'juhe-ai-account-import', '导入模板 type 必须保持当前协议')
assertEqual(template.version, 1, '导入模板 version 必须保持 v1')
assertEqual(template.accounts?.length, 8, '导入模板应覆盖 GPT API Key、GPT OAuth、DeepSeek 双接入、GLM 三接入和 Anthropic API Key')
assertEqual(template.proxies?.length, 1, '导入模板应继续包含代理 ref 示例')

const apiKeyAccount = template.accounts?.find((account) => account.type === 'api_key')
const oauthAccount = template.accounts?.find((account) => account.type === 'oauth')
const deepSeekOpenAIAccount = template.accounts?.find((account) => account.ref === 'deepseek-key-001')
const deepSeekClaudeCodeAccount = template.accounts?.find((account) => account.ref === 'deepseek-claude-code-001')
const glmGeneralAccount = template.accounts?.find((account) => account.providerCode === GLM_PROVIDER_CODE && account.providerProtocolProfileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID)
const glmCodingAccount = template.accounts?.find((account) => account.providerCode === GLM_PROVIDER_CODE && account.providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID)
const glmCodingAnthropicAccount = template.accounts?.find((account) => account.providerCode === GLM_PROVIDER_CODE && account.providerProtocolProfileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID)
const anthropicAccount = template.accounts?.find((account) => account.providerCode === ANTHROPIC_PROVIDER_CODE)
const deprecatedGlmProfileField = ['connection', 'Type'].join('')
assertDefined(apiKeyAccount, '导入模板应包含 API Key 账号示例')
assertDefined(oauthAccount, '导入模板应包含 OAuth 账号示例')
assertDefined(deepSeekOpenAIAccount, '导入模板应包含 DeepSeek OpenAI-compatible API Key 账号示例')
assertDefined(deepSeekClaudeCodeAccount, '导入模板应包含 DeepSeek Claude Code API Key 账号示例')
assertDefined(glmGeneralAccount, '导入模板应包含 GLM 通用 API Key 账号示例')
assertDefined(glmCodingAccount, '导入模板应包含 GLM Coding Plan Key 账号示例')
assertDefined(glmCodingAnthropicAccount, '导入模板应包含 GLM Coding Anthropic Key 账号示例')
assertDefined(anthropicAccount, '导入模板应包含 Anthropic API Key 账号示例')
assertTrue(template.accounts?.every((account) => typeof account.providerProtocolProfileId === 'string' && account.providerProtocolProfileId.trim().length > 0) === true, '导入模板每个账号都必须显式填写 providerProtocolProfileId')
assertEqual(apiKeyAccount.providerCode, GPT_VENDOR_CODE, 'API Key 示例应继续使用 GPT 供应商')
assertEqual(apiKeyAccount.providerProtocolProfileId, GPT_OPENAI_V1_PROFILE_ID, 'API Key 示例应显式填写 GPT OpenAI v1 profile')
assertFalse(Object.prototype.hasOwnProperty.call(apiKeyAccount, 'clientCompatibility'), 'API Key 示例不应暴露账号兼容字段')
assertEqual(oauthAccount.providerCode, GPT_VENDOR_CODE, 'OAuth 示例应继续使用 GPT 供应商')
assertEqual(oauthAccount.providerProtocolProfileId, GPT_OPENAI_V1_PROFILE_ID, 'OAuth 示例应显式填写 GPT OpenAI v1 profile')
assertFalse(Object.prototype.hasOwnProperty.call(oauthAccount, 'clientCompatibility'), 'OAuth 示例不应暴露账号兼容字段')
assertEqual(typeof apiKeyAccount.credentials?.api_key, 'string', 'API Key 示例必须保留 credentials.api_key')
assertTrue(Array.isArray(apiKeyAccount.credentials?.supported_endpoint_modes), 'API Key 示例应包含 supported_endpoint_modes')
assertEqual(typeof oauthAccount.credentials?.refresh_token, 'string', 'OAuth 示例必须保留 refresh_token')
assertTrue(Array.isArray(oauthAccount.credentials?.supported_endpoint_modes), 'OAuth 示例应包含 supported_endpoint_modes')
assertEqual(typeof deepSeekOpenAIAccount.credentials?.api_key, 'string', 'DeepSeek OpenAI-compatible 示例必须保留 credentials.api_key')
assertEqual(deepSeekOpenAIAccount.providerProtocolProfileId, DEEPSEEK_OPENAI_V1_PROFILE_ID, 'DeepSeek OpenAI-compatible 示例应显式填写 OpenAI v1 profile')
assertEqual(deepSeekOpenAIAccount.credentials?.base_url, 'https://api.deepseek.com/v1', 'DeepSeek OpenAI-compatible 示例应使用官方 base URL（URL v2 版本根）')
assertEqual(JSON.stringify(deepSeekOpenAIAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse', 'responses_json', 'responses_sse']), 'DeepSeek OpenAI-compatible 示例应启用 Chat Completions 与原生 Responses JSON/Streaming')
assertFalse(Object.prototype.hasOwnProperty.call(deepSeekOpenAIAccount, 'clientCompatibility'), 'DeepSeek OpenAI-compatible 普通示例不应默认暴露客户端兼容字段')
assertEqual(deepSeekClaudeCodeAccount.providerProtocolProfileId, DEEPSEEK_ANTHROPIC_V1_PROFILE_ID, 'DeepSeek Claude Code 示例应显式填写 Anthropic v1 profile')
assertFalse(Object.prototype.hasOwnProperty.call(deepSeekClaudeCodeAccount, 'clientCompatibility'), 'DeepSeek Claude Code 示例不应暴露客户端兼容字段')
assertEqual(deepSeekClaudeCodeAccount.credentials?.base_url, 'https://api.deepseek.com/anthropic/v1', 'DeepSeek Claude Code 示例应使用 Anthropic-compatible base URL（URL v2 版本根）')
assertEqual(JSON.stringify(deepSeekClaudeCodeAccount.credentials?.supported_endpoint_modes), JSON.stringify(['messages_json', 'messages_sse']), 'DeepSeek Claude Code 示例应只启用 Messages JSON/Streaming')
assertFalse(Object.prototype.hasOwnProperty.call(glmGeneralAccount, 'clientCompatibility'), 'GLM 通用示例不应暴露客户端兼容字段')
assertFalse(Object.prototype.hasOwnProperty.call(glmCodingAccount, 'clientCompatibility'), 'GLM Coding 示例不应暴露客户端兼容字段')
assertFalse(Object.prototype.hasOwnProperty.call(glmGeneralAccount, deprecatedGlmProfileField), 'GLM 通用示例不应暴露已废弃的接入类型字段')
assertFalse(Object.prototype.hasOwnProperty.call(glmCodingAccount, deprecatedGlmProfileField), 'GLM Coding 示例不应暴露已废弃的接入类型字段')
assertEqual(JSON.stringify(glmGeneralAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse']), 'GLM 通用示例应只启用 Chat Completions JSON/Streaming')
assertEqual(JSON.stringify(glmCodingAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse']), 'GLM Coding 示例应只启用 Chat Completions JSON/Streaming')
assertFalse(Object.prototype.hasOwnProperty.call(glmCodingAnthropicAccount, 'clientCompatibility'), 'GLM Coding Anthropic 示例不应暴露客户端兼容字段')
assertFalse(Object.prototype.hasOwnProperty.call(glmCodingAnthropicAccount, deprecatedGlmProfileField), 'GLM Coding Anthropic 示例不应暴露已废弃的接入类型字段')
assertEqual(glmCodingAnthropicAccount.credentials?.base_url, 'https://open.bigmodel.cn/api/anthropic/v1', 'GLM Coding Anthropic 示例应使用 Anthropic-compatible base URL（URL v2 版本根）')
assertEqual(JSON.stringify(glmCodingAnthropicAccount.credentials?.supported_endpoint_modes), JSON.stringify(['messages_json', 'messages_sse']), 'GLM Coding Anthropic 示例应只启用 Messages JSON/Streaming')
assertEqual(typeof anthropicAccount.credentials?.api_key, 'string', 'Anthropic 示例必须保留 credentials.api_key')
assertEqual(anthropicAccount.providerProtocolProfileId, ANTHROPIC_ANTHROPIC_V1_PROFILE_ID, 'Anthropic 示例应显式填写 Anthropic v1 profile')
assertTrue(Array.isArray(anthropicAccount.credentials?.supported_endpoint_modes), 'Anthropic 示例应包含 supported_endpoint_modes')
assertFalse(Object.prototype.hasOwnProperty.call(anthropicAccount.credentials ?? {}, 'anthropic_version'), 'Anthropic 导入示例不应把 anthropic-version 当作账号凭据')
assertFalse(Object.prototype.hasOwnProperty.call(anthropicAccount.credentials ?? {}, 'anthropic_beta'), 'Anthropic 导入示例不应把 anthropic-beta 当作账号凭据')
assertEqual(typeof apiKeyAccount.groupName, 'string', '模板账号必须保留 groupName 示例')
assertTrue(template.accounts?.every((account) => Array.isArray(account.connections) && account.connections.length > 0) === true, '导入模板每个账号都必须显式提交非空 connections 数组（native 导入不做缺省补齐）')
assertTrue(template.accounts?.every((account) => typeof account.healthCheckConnectionPresetCode === 'string' && account.healthCheckConnectionPresetCode.trim().length > 0) === true, '导入模板每个账号都必须显式提交 healthCheckConnectionPresetCode')
assertTrue(template.accounts?.every((account) => typeof account.healthCheckModel === 'string' && account.healthCheckModel.trim().length > 0) === true, '导入模板每个账号都必须显式提交 healthCheckModel')
assertTrue(template.accounts?.every((account) => typeof account.healthCheckEndpointMode === 'string' && account.healthCheckEndpointMode.trim().length > 0) === true, '导入模板每个账号都必须显式提交 healthCheckEndpointMode')

// ---- rpmLimit（RPM 设计 §6.4）：模板 8 账号统一补 rpmLimit: 0，字段表与规则同步 ----

assertTrue(
  template.accounts?.every((account) => account.rpmLimit === 0) === true,
  '导入模板每个账号都必须显式补 rpmLimit: 0（每分钟请求上限，0 表示不限制）'
)
assertMatch(accountImportProtocolMarkdown, /\| `rpmLimit` \| 否 \| number \| 每分钟请求上限；非负整数且不超过 2147483647（int4 上限），0 或缺省表示不限制。 \|/, '协议 Markdown 应把 rpmLimit 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /`rpmLimit` 必须是非负整数且不超过 2147483647/, '协议 Markdown 应说明 rpmLimit 非负整数与 int4 上界')

// ---- 来源格式示例（DR-39）：四个来源示例都必须携带模型列表，否则后端按
// 「来源账户缺少模型列表，无法按模型名推断厂商」逐条跳过，复制示例 0 条可导入 ----
assertMatch(sourceExampleBlock, /"supportedModels": \["gpt-5\.2"\]/, 'Sub2API 示例必须携带 supportedModels')
assertEqual((sourceExampleBlock.match(/"models": "gpt-5\.2"/g) ?? []).length, 2, 'NewAPI/One-API 示例必须各携带 models')
assertMatch(sourceExampleBlock, /models:\r?\n\s+- gpt-5\.2/, 'CLIProxyAPI 示例必须携带 models 列表')

assertMatch(aiConversionPrompt, /juhe-ai-account-import v1 JSON/, 'AI 提示词应继续要求输出当前导入协议 JSON')
assertMatch(aiConversionPrompt, /只输出合法 JSON/, 'AI 提示词应继续禁止输出解释或 Markdown')
assertMatch(aiConversionPrompt, /不要编造来源数据里不存在的 token/, 'AI 提示词应继续约束 token 不可编造')
assertMatch(aiConversionPrompt, /pending_test 或 disabled/, 'AI 提示词应允许不确定账户导入为待检查或停用')
assertMatch(aiConversionPrompt, /普通 OpenAI v1 账户可以用 modelMappings 显式填写 Responses -> Chat Completions/, 'AI 提示词应说明普通 OpenAI v1 账户可导入 Responses 到 Chat 映射')
assertMatch(aiConversionPrompt, /每个账户必须显式提交非空 connections 数组/, 'AI 提示词应要求每个账户显式提交 connections')
assertMatch(aiConversionPrompt, /healthCheckConnectionPresetCode（指向该账户一条 enabled 连接的 connectionPresetCode）、healthCheckModel（必须属于 supportedModels）和 healthCheckEndpointMode/, 'AI 提示词应说明健康检查三件套的键名与指向约束')

assertMatch(accountImportProtocolMarkdown, /# juhe-ai AI 账户导入协议 v1/, '协议 Markdown 应继续保留标题')
assertMatch(accountImportProtocolMarkdown, /```json[\s\S]+juhe-ai-account-import[\s\S]+```/, '协议 Markdown 应继续包含 JSON 示例代码块')
assertTrue(accountImportProtocolMarkdown.includes(importTemplate), '协议 Markdown 的完整示例应继续嵌入导入模板')
assertMatch(accountImportProtocolMarkdown, /当前默认使用 `providerCode: "gpt"`/, '协议 Markdown 应继续说明默认 GPT providerCode')
assertMatch(accountImportProtocolMarkdown, /`deepseek`/, '协议 Markdown 应说明 DeepSeek providerCode')
assertMatch(accountImportProtocolMarkdown, /`xai`/, '协议 Markdown 应说明 xAI providerCode')
assertMatch(accountImportProtocolMarkdown, /profile_xai_openai_v1/, '协议 Markdown 应说明 xAI 官方协议档案')
assertFalse(accountImportProtocolMarkdown.includes('workload_identity'), '协议 Markdown 不得开放 Anthropic Workload Identity 账户类型')
assertMatch(accountImportProtocolMarkdown, /`google_oauth`/, '协议 Markdown 应说明 Gemini Google OAuth 账户类型')
assertMatch(accountImportProtocolMarkdown, /`interactions_json`、`interactions_sse`/, '协议 Markdown 应说明 Gemini Interactions 请求形态')
assertMatch(accountImportProtocolMarkdown, /不要导入 Claude 订阅 OAuth \/ Setup Token/, '协议 Markdown 应说明 Claude 订阅 OAuth 不可导入')
assertMatch(accountImportProtocolMarkdown, /DeepSeek OpenAI-compatible 默认启用 Chat Completions 与 Responses 的 JSON\/Streaming/, '协议 Markdown 应说明 DeepSeek OpenAI-compatible 默认接口能力')
assertMatch(accountImportProtocolMarkdown, /账号导入不填写 `clientCompatibility`/, '协议 Markdown 应说明账号导入不再填写客户端兼容字段')
assertFalse(/\| `clientCompatibility` \| 否 \| string \|/.test(accountImportProtocolMarkdown), '协议 Markdown 不应把 clientCompatibility 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /\| `providerProtocolProfileId` \| 是 \| string \|/, '协议 Markdown 应把 providerProtocolProfileId 暴露为必填导入字段')
assertMatch(accountImportProtocolMarkdown, /\| `tags` \| 否 \| string\[\] \|/, '协议 Markdown 应把 tags 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /`active`、`pending_test` 或 `disabled`/, '协议 Markdown 应说明导入状态支持 pending_test')
assertMatch(accountImportProtocolMarkdown, /`status: "active"` 会转为 `pending_test`/, '协议 Markdown 应说明 active 导入创建会转为待检查')
assertMatch(accountImportProtocolMarkdown, /下游客户端画像由网关运行时自动识别/, '协议 Markdown 应说明客户端画像由网关内部识别')
assertFalse(new RegExp(deprecatedGlmProfileField).test(accountImportProtocolMarkdown), '协议 Markdown 不应再暴露已废弃的 GLM 接入类型字段')
assertMatch(accountImportProtocolMarkdown, /"providerProtocolProfileId": "profile_glm_coding_anthropic_v1"/, '协议 Markdown 应说明 GLM Coding Anthropic profile')
assertMatch(accountImportProtocolMarkdown, /profile_deepseek_anthropic_v1/, '协议 Markdown 应说明 DeepSeek Claude Code profile')
assertMatch(accountImportProtocolMarkdown, /`modelMappings` 只做账号模型别名/, '协议 Markdown 应说明 modelMappings 只做账号模型别名')
assertMatch(accountImportProtocolMarkdown, /其他跨协议方向会被拒绝/, '协议 Markdown 应说明跨协议方向会被拒绝')
assertMatch(accountImportProtocolMarkdown, /Responses → Responses，Responses → Chat Completions（仅 stream=true）/, '协议 Markdown 应说明普通 OpenAI v1 账户可声明 Responses 同族改名与 Responses 到 Chat 映射')
assertMatch(accountImportProtocolMarkdown, /左侧 `sourceModel` 是下游 Responses 别名，可选择当前供应商目录内的 Chat-only 模型/, '协议 Markdown 应说明 OpenAI v1 Responses 到 Chat 映射可用 Chat-only 模型作为下游别名')
assertMatch(accountImportProtocolMarkdown, /两者都必须来自当前账户供应商模型目录/, '协议 Markdown 应说明 source/upstream 均受当前供应商目录约束')
assertMatch(accountImportProtocolMarkdown, /DeepSeek Claude Code 与 GLM Coding Anthropic 使用 Anthropic v1 Messages 原生协议，`credentials\.supported_endpoint_modes` 填 `messages_json`、`messages_sse`，不要填 `message_token_counting`/, '协议 Markdown 应说明第三方 Anthropic 档案不支持 count_tokens')
assertMatch(accountImportProtocolMarkdown, /`credentials\.supported_endpoint_modes` 用于声明上游接口能力/, '协议 Markdown 应使用上游接口能力说明 supported_endpoint_modes')
assertFalse(/接口能力限制/.test(accountImportProtocolMarkdown), '协议 Markdown 不应继续使用接口能力限制旧文案')
assertMatch(accountImportProtocolMarkdown, /不接受 `credentials\.anthropic_version` 或 `credentials\.anthropic_beta`/, '协议 Markdown 应明确 Anthropic header 不属于账号凭据')
assertMatch(accountImportProtocolMarkdown, /`proxyRef` 和 `proxyProfileId` 不能同时填写/, '协议 Markdown 应继续说明代理字段互斥')
assertMatch(accountImportProtocolMarkdown, /账户支持模型不在供应商模型目录中/, '协议 Markdown 应说明模型目录拦截失败提示')
assertMatch(formalProtocolMarkdown, /账户支持模型不在供应商模型目录中/, '正式协议文档应说明模型目录拦截失败提示')
assertMatch(formalProtocolMarkdown, /# AI 账户导入协议/, '正式协议文档应可读取')
assertMatch(accountImportProtocolMarkdown, /\| `connections` \| 是 \| object\[\] \|/, '导出协议 Markdown 应把 connections 暴露为必填导入字段')
assertMatch(accountImportProtocolMarkdown, /\| `healthCheckConnectionPresetCode` \| 是 \| string \|/, '导出协议 Markdown 应把 healthCheckConnectionPresetCode 暴露为必填导入字段')
assertMatch(accountImportProtocolMarkdown, /\| `healthCheckModel` \| 是 \| string \|/, '导出协议 Markdown 应把 healthCheckModel 暴露为必填导入字段')
assertMatch(accountImportProtocolMarkdown, /## connections 条目字段/, '导出协议 Markdown 应包含 connections 条目字段说明')
assertMatch(accountImportProtocolMarkdown, /账户 `connections` 不能为空：导入包必须显式提交非空 connections 数组（connectionPresetCode\/baseUrl\/endpointModes\/enabled）/, '导出协议 Markdown 应说明 connections 缺失失败提示')
assertMatch(formalProtocolMarkdown, /账户 `connections` 不能为空：导入包必须显式提交非空 connections 数组（connectionPresetCode\/baseUrl\/endpointModes\/enabled）/, '正式协议文档应说明 connections 缺失失败提示')
assertMatch(formalProtocolMarkdown, /`healthCheckConnectionPresetCode` 不能为空：健康检查三件套必须显式提交/, '正式协议文档应说明健康检查三件套缺失失败提示')
assertMatch(formalProtocolMarkdown, /health_check_connection_conflict/, '正式协议文档应说明健康检查指向失败的执行阶段错误码')

for (const requirement of protocolSemanticRequirements) {
  assertMatch(accountImportProtocolMarkdown, requirement.exportedPattern, `导出协议 Markdown 应完整覆盖${requirement.name}`)
  assertMatch(formalProtocolMarkdown, requirement.formalPattern, `正式协议文档应完整覆盖${requirement.name}`)
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}，实际 ${String(actual)}，期望 ${String(expected)}`)
  }
}

function assertTrue(value: boolean, message: string): void {
  if (!value) {
    throw new Error(message)
  }
}

function assertFalse(value: boolean, message: string): void {
  if (value) {
    throw new Error(message)
  }
}

function assertMatch(value: string, pattern: RegExp, message: string): void {
  if (!pattern.test(value)) {
    throw new Error(message)
  }
}

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start + startMarker.length)
  if (start < 0 || end <= start) {
    throw new Error(`无法定位源码片段: ${startMarker}`)
  }
  return source.slice(start, end)
}

function assertDefined<T>(value: T | undefined, message: string): asserts value is T {
  if (value === undefined) {
    throw new Error(message)
  }
}

// ---------------------------------------------------------------------------
// native 导入账户结构复算器
//
// 规则来源（只读对齐，不改后端）：
// - backend-go/projects/gateway/internal/accounts/accountstransfer/import.go
//   （planImportAccount 尾段 1250-1261：connections/健康检查三件套显式必填；
//   validateImportModelCatalogFields 1489-1490：healthCheckModel ∈ supportedModels）
// - backend-go/projects/gateway/internal/accounts/accountscore/writeinput.go
//   （ParseConnectionInput 84-123：元素键形状 + 未知键拒绝；
//   AccountHealthCheckEndpointModes 232-244：检查形态白名单）
// - backend-go/projects/gateway/internal/accounts/connections.go
//   （执行链 Store.Create：preset ∈ registry、账户内唯一、modes ∈ AllowedModes、
//   finalizeAccountHealthCheckTrio 429-456：三件套指向 enabled 连接 + 形态 ∈ 连接 modes）
// - backend-go/shared/contracts/connector_registry.go（14 preset、词表与 URL 后缀契约）
// ---------------------------------------------------------------------------

const connectorPresetCodes: readonly string[] = [
  'gpt_openai_v1', 'xai_openai_v1', 'deepseek_openai_v1', 'deepseek_anthropic_v1',
  'anthropic_v1', 'gemini_openai_chat_v1beta', 'gemini_native_v1beta',
  'glm_general_openai_v1', 'glm_coding_openai_v1', 'glm_coding_anthropic_v1',
  'minimax_media', 'volcengine_ark', 'volcengine_openspeech', 'qwen_dashscope'
]

const openAIVocabulary: readonly string[] = [
  'chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'images_json',
  'audio_speech', 'audio_transcription_json',
  'audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel',
  'video_create', 'video_get', 'video_content', 'video_cancel', 'realtime_session'
]
const anthropicVocabulary: readonly string[] = ['messages_json', 'messages_sse', 'message_token_counting']
const geminiNativeVocabulary: readonly string[] = [
  'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse',
  'count_tokens', 'embed_content', 'audio_speech',
  'video_create', 'video_get', 'video_content', 'video_cancel'
]
const glmOpenAINarrowedVocabulary: readonly string[] = [
  'chat_json', 'chat_sse',
  'video_create', 'video_get', 'video_content', 'video_cancel',
  'audio_speech', 'audio_transcription_json'
]
const mediaVocabulary: readonly string[] = ['audio_speech', 'video_create', 'video_get', 'video_content', 'video_cancel']

interface PresetCapability {
  /** registry preset record 的 OperationPathTemplates（R8 基地址路径校验依据）。 */
  operationTemplates: readonly string[]
  /** preset 注册词表（AllowedModes = 域 lane ∩ 词表；脚本只按词表复算模板涉及的 chat 域值）。 */
  vocabulary: readonly string[]
}

const openAIOperationTemplates: readonly string[] = [
  'POST /chat/completions', 'POST /responses', 'POST /images/generations',
  'POST /audio/speech', 'POST /audio/transcriptions', 'POST /audio/translations',
  'POST /audio/jobs', 'GET /audio/jobs/{id}', 'GET /audio/jobs/{id}/content', 'DELETE /audio/jobs/{id}',
  'POST /videos', 'GET /videos/{id}', 'GET /videos/{id}/content', 'DELETE /videos/{id}',
  'GET /realtime'
]
const anthropicOperationTemplates: readonly string[] = ['POST /messages', 'POST /messages/count_tokens']
const geminiNativeOperationTemplates: readonly string[] = [
  'POST /models/{model}:generateContent', 'POST /models/{model}:streamGenerateContent',
  'POST /models/{model}:countTokens', 'POST /models/{model}:embedContent',
  'POST /interactions', 'GET /interactions/{id}', 'DELETE /interactions/{id}', 'POST /interactions/{id}/cancel'
]

const presetCapabilities: Record<string, PresetCapability> = {
  gpt_openai_v1: { operationTemplates: openAIOperationTemplates, vocabulary: openAIVocabulary },
  xai_openai_v1: { operationTemplates: openAIOperationTemplates, vocabulary: openAIVocabulary },
  deepseek_openai_v1: { operationTemplates: openAIOperationTemplates, vocabulary: openAIVocabulary },
  deepseek_anthropic_v1: { operationTemplates: anthropicOperationTemplates, vocabulary: ['messages_json', 'messages_sse'] },
  anthropic_v1: { operationTemplates: anthropicOperationTemplates, vocabulary: anthropicVocabulary },
  gemini_openai_chat_v1beta: { operationTemplates: ['POST /chat/completions'], vocabulary: ['chat_json', 'chat_sse'] },
  gemini_native_v1beta: { operationTemplates: geminiNativeOperationTemplates, vocabulary: geminiNativeVocabulary },
  glm_general_openai_v1: { operationTemplates: openAIOperationTemplates, vocabulary: glmOpenAINarrowedVocabulary },
  glm_coding_openai_v1: { operationTemplates: openAIOperationTemplates, vocabulary: glmOpenAINarrowedVocabulary },
  glm_coding_anthropic_v1: { operationTemplates: anthropicOperationTemplates, vocabulary: ['messages_json', 'messages_sse'] },
  minimax_media: { operationTemplates: ['POST /video_generation', 'GET /query/video_generation', 'POST /t2a_v2'], vocabulary: mediaVocabulary },
  volcengine_ark: { operationTemplates: ['POST /contents/generations/tasks', 'GET /contents/generations/tasks/{id}'], vocabulary: ['chat_json', 'chat_sse', 'images_json', 'video_create', 'video_get', 'video_content', 'video_cancel'] },
  volcengine_openspeech: { operationTemplates: ['POST /tts/create', 'POST /auc/bigmodel/submit', 'POST /auc/bigmodel/query'], vocabulary: ['audio_speech', 'audio_transcription_json', 'audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel'] },
  qwen_dashscope: { operationTemplates: ['POST /compatible-mode/v1/chat/completions', 'POST /api/v1/services/audio/asr/transcription', 'POST /api/v1/services/aigc/video-generation/video-synthesis', 'GET /api/v1/tasks/{task_id}'], vocabulary: ['chat_json', 'chat_sse', 'video_create', 'video_get', 'video_content', 'video_cancel', 'audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel'] }
}

const accountHealthCheckEndpointModes: readonly string[] = [
  'images_json', 'chat_json', 'chat_sse',
  'responses_json', 'responses_sse',
  'messages_json', 'messages_sse',
  'generate_content_json', 'generate_content_sse',
  'interactions_json', 'interactions_sse',
  'audio_speech', 'audio_transcription_json', 'video_create'
]

const connectionAllowedKeys: readonly string[] = ['connectionPresetCode', 'protocolSurfaceCode', 'baseUrl', 'endpointModes', 'enabled']

interface ConnectionRecord {
  connectionPresetCode: string
  baseUrl: string
  endpointModes: string[]
  enabled: boolean
}

/** 复算 ParseConnectionInput（writeinput.go:84-123）+ 执行链 registry 校验。 */
function parseConnectionsForValidation(rawConnections: unknown, label: string): ConnectionRecord[] {
  assertTrue(Array.isArray(rawConnections) && rawConnections.length > 0, `${label}：账户 connections 不能为空：导入包必须显式提交非空 connections 数组（connectionPresetCode/baseUrl/endpointModes/enabled）`)
  const seenPresets = new Set<string>()
  return rawConnections.map((entry, index) => {
    const entryLabel = `${label} connections[${index}]`
    assertTrue(typeof entry === 'object' && entry !== null && !Array.isArray(entry), `${entryLabel} 条目必须是对象`)
    const record = entry as Record<string, unknown>
    for (const key of Object.keys(record)) {
      assertTrue(connectionAllowedKeys.includes(key), `${entryLabel} 含未知键 ${key}（ParseConnectionInput 严格解析，不造兼容层）`)
    }
    const preset = typeof record.connectionPresetCode === 'string' ? record.connectionPresetCode.trim() : ''
    const baseUrl = typeof record.baseUrl === 'string' ? record.baseUrl.trim() : ''
    assertTrue(preset !== '', `${entryLabel} 缺少 connectionPresetCode`)
    assertTrue(baseUrl !== '', `${entryLabel} 缺少 baseUrl`)
    assertTrue(typeof record.enabled === 'boolean', `${entryLabel} 必须显式提交布尔 enabled`)
    assertTrue(Array.isArray(record.endpointModes) && record.endpointModes.length > 0, `${entryLabel} endpointModes 必须是非空字符串数组`)
    const modes = (record.endpointModes as unknown[]).map((mode) => {
      assertTrue(typeof mode === 'string' && mode.trim() !== '', `${entryLabel} endpointModes 存在空值`)
      return mode.trim()
    })
    assertTrue(connectorPresetCodes.includes(preset), `${entryLabel} connectionPresetCode ${preset} 不在连接器注册表 17 值内`)
    assertTrue(!seenPresets.has(preset), `${entryLabel} 同一连接预设重复提交：${preset}`)
    seenPresets.add(preset)
    const capability = presetCapabilities[preset]
    for (const mode of modes) {
      assertTrue(capability.vocabulary.includes(mode), `${entryLabel} endpoint mode ${mode} 不在预设 ${preset} 允许词表内`)
    }
    const baseUrlViolation = connectionBaseUrlViolation(baseUrl, capability.operationTemplates)
    assertTrue(baseUrlViolation === null, `${entryLabel} baseUrl ${baseUrl} 未通过连接基地址校验：${baseUrlViolation ?? ''}`)
    return { connectionPresetCode: preset, baseUrl, endpointModes: modes, enabled: record.enabled }
  })
}

/**
 * 复算 validateConnectionBaseURL（connection_base_url.go:65-125 + R8）：
 * 返回 null 表示通过，否则返回失败原因。核心路径契约是「任意连续后缀段
 * 不命中操作模板完整路径或按段前缀」，不是后缀结尾匹配。
 */
function connectionBaseUrlViolation(rawUrl: string, templates: readonly string[]): string | null {
  let parsed: URL
  try {
    parsed = new URL(rawUrl)
  } catch {
    return '连接 Base URL 格式无效'
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return '连接 Base URL 只允许 http 或 https 协议'
  if (parsed.hostname === '') return '连接 Base URL 必须包含主机名'
  if (parsed.username !== '' || parsed.password !== '') return '连接 Base URL 不能包含用户名或密码'
  if (parsed.search !== '') return '连接 Base URL 不能包含查询参数'
  if (parsed.hash !== '') return '连接 Base URL 不能包含片段标识'
  const segments = decodedConnectionPathSegments(parsed.pathname)
  if (segments === null) return '连接 Base URL 路径包含连续斜杠、. 或 .. 段或无效编码'
  if (basePathHitsOperationPath(templates, segments)) return '连接 Base URL 不能填写具体接口路径，请填写包含版本前缀的根地址，例如 /v1、/api/v3'
  return null
}

/** 对齐 decodedConnectionPathSegments（connection_base_url.go:131-164）：失败返回 null。 */
function decodedConnectionPathSegments(rawPath: string): string[] | null {
  if (rawPath === '' || rawPath === '/') return []
  let trimmed = rawPath
  while (trimmed.length > 1 && trimmed.endsWith('/')) {
    trimmed = trimmed.slice(0, -1)
  }
  const segments: string[] = []
  for (const raw of trimmed.replace(/^\//, '').split('/')) {
    if (raw === '') return null
    let decoded: string
    try {
      decoded = decodeURIComponent(raw)
    } catch {
      return null
    }
    if (decoded.includes('/') || decoded.includes('\\')) return null
    if (decoded === '.' || decoded === '..') return null
    segments.push(decoded)
  }
  return segments
}

/** 对齐 basePathHitsOperationPath（connection_base_url.go:186-193）。 */
function basePathHitsOperationPath(templates: readonly string[], path: readonly string[]): boolean {
  return path.some((_, index) => connectionOperationPathViolation(templates, path.slice(index)))
}

/** 对齐 connectionOperationPathViolation（connection_base_url.go:198-216）：完整路径或中间前缀命中。 */
function connectionOperationPathViolation(templates: readonly string[], path: readonly string[]): boolean {
  if (path.length === 0) return false
  for (const template of templates) {
    const templatePath = template.includes(' ') ? template.slice(template.indexOf(' ') + 1) : template
    const segments = templatePath.split('/').filter((part) => part !== '')
    if (segments.length === 0) continue
    if (path.length === segments.length && templateSegmentsFullMatch(segments, path)) return true
    if (path.length < segments.length && templateSegmentsPrefixMatch(segments, path)) return true
  }
  return false
}

/** 对齐 connectionTemplateSegmentsFullMatch：占位符 {name} 通配非空文本，字面段精确匹配。 */
function templateSegmentsFullMatch(segments: readonly string[], path: readonly string[]): boolean {
  return segments.every((templateSegment, index) => templateSegmentPattern(templateSegment).test(path[index]))
}

/** 对齐 connectionTemplateSegmentsPrefixMatch：前 n-1 段全匹配 + 边界段 possible-prefix。 */
function templateSegmentsPrefixMatch(segments: readonly string[], path: readonly string[]): boolean {
  for (let index = 0; index < path.length - 1; index++) {
    if (!templateSegmentPattern(segments[index]).test(path[index])) return false
  }
  const boundaryTemplate = segments[path.length - 1]
  const boundaryPath = path[path.length - 1]
  if (boundaryPath === '') return false
  return boundaryTemplate.startsWith('{') || boundaryTemplate.startsWith(boundaryPath)
}

/** 对齐 connectionTemplateSegmentPattern：{name} → 非空任意文本正则，其余字符字面。 */
function templateSegmentPattern(templateSegment: string): RegExp {
  const source = templateSegment
    .split(/(\{[^}]+\})/)
    .map((part) => part.startsWith('{') && part.endsWith('}') ? '[\\s\\S]+?' : part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
    .join('')
  return new RegExp(`^${source}$`)
}

/** 复算 planImportAccount 尾段（import.go:1250-1261）+ finalizeAccountHealthCheckTrio（connections.go:429-456）。 */
function validateNativeImportAccountShape(account: Record<string, unknown>, label: string): void {
  const connections = parseConnectionsForValidation(account.connections, label)
  const presetCode = typeof account.healthCheckConnectionPresetCode === 'string' ? account.healthCheckConnectionPresetCode.trim() : ''
  assertTrue(presetCode !== '', `${label}：账户 healthCheckConnectionPresetCode 不能为空：健康检查三件套必须显式提交`)
  const target = connections.find((connection) => connection.connectionPresetCode === presetCode && connection.enabled)
  assertDefined(target, `${label}：health_check_connection_conflict：connection preset ${presetCode} 解析不到本账户 enabled 连接`)
  const healthCheckModel = typeof account.healthCheckModel === 'string' ? account.healthCheckModel.trim() : ''
  assertTrue(healthCheckModel !== '', `${label}：账户 healthCheckModel 不能为空：健康检查三件套必须显式提交`)
  const healthCheckMode = typeof account.healthCheckEndpointMode === 'string' ? account.healthCheckEndpointMode.trim() : ''
  assertTrue(healthCheckMode !== '', `${label}：账户 healthCheckEndpointMode 不能为空：健康检查三件套必须显式提交`)
  assertTrue(accountHealthCheckEndpointModes.includes(healthCheckMode), `${label}：healthCheckEndpointMode ${healthCheckMode} 不在检查形态白名单内`)
  assertTrue(target.endpointModes.includes(healthCheckMode), `${label}：health_check_connection_conflict：检查形态 ${healthCheckMode} 不在该连接已启用 mode 内`)
  // 模板/样例面显式给出 supportedModels 以便静态复算成员关系；后端允许省略并按
  // 供应商默认回填（validateImportModelCatalogFields 1475-1484），回填后同样执行
  // healthCheckModel ∈ supportedModels 校验。
  assertTrue(Array.isArray(account.supportedModels) && account.supportedModels.length > 0, `${label}：模板复算要求显式 supportedModels（后端允许省略并默认回填）`)
  assertTrue((account.supportedModels as unknown[]).includes(healthCheckModel), `${label}：账户 healthCheckModel 必须属于 supportedModels`)
}

// ---- native 导入必填键同规则复算 ----
// 调用段放在文件尾部：复算器引用的 registry 常量以顶层 const 声明，
// 顺序执行必须先完成初始化（function 声明提升、const 不提升）。

for (const account of template.accounts ?? []) {
  validateNativeImportAccountShape(account as Record<string, unknown>, `模板账号 ${account.ref ?? '(无 ref)'}`)
}

// 手填样例：按新模板规则手工构造一个模板未覆盖的 xAI API Key 账号，
// 走同一校验器复算，证明按文档新增字段手填的导入包能通过后端校验规则。
const handWrittenXaiAccount: Record<string, unknown> = {
  ref: 'xai-key-001',
  name: 'xAI Grok API Key 账号 1',
  providerCode: 'xai',
  providerProtocolProfileId: 'profile_xai_openai_v1',
  type: 'api_key',
  status: 'active',
  groupName: '默认 xAI 分组',
  credentials: {
    api_key: 'xai-xxx',
    base_url: 'https://api.x.ai/v1',
    supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
  },
  supportedModels: ['grok-4.5'],
  connections: [
    {
      connectionPresetCode: 'xai_openai_v1',
      baseUrl: 'https://api.x.ai/v1',
      endpointModes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
      enabled: true
    }
  ],
  healthCheckConnectionPresetCode: 'xai_openai_v1',
  healthCheckModel: 'grok-4.5',
  healthCheckEndpointMode: 'chat_sse',
  notes: '按新模板手填的 xAI 官方 API Key 账号样例'
}
validateNativeImportAccountShape(handWrittenXaiAccount, '手填样例 xai-key-001')

// 负向验证：缺键 / 指向 disabled 连接 / 未知键的账户必须被复算器拒绝，
// 证明它与后端失败语义同向（不恒通过）。
function expectNativeImportAccountRejected(account: Record<string, unknown>, label: string): void {
  let rejected = false
  try {
    validateNativeImportAccountShape(account, label)
  } catch {
    rejected = true
  }
  assertTrue(rejected, `${label} 必须被 native 导入复算器拒绝`)
}

expectNativeImportAccountRejected({ ...handWrittenXaiAccount, connections: [] }, '负向样例（空 connections）')
const disabledConnectionAccount = JSON.parse(JSON.stringify(handWrittenXaiAccount)) as Record<string, unknown>
disabledConnectionAccount.connections = (disabledConnectionAccount.connections as Array<Record<string, unknown>>).map((connection) => ({ ...connection, enabled: false }))
expectNativeImportAccountRejected(disabledConnectionAccount, '负向样例（healthCheckConnectionPresetCode 指向 disabled 连接）')
const unknownKeyAccount = JSON.parse(JSON.stringify(handWrittenXaiAccount)) as Record<string, unknown>
unknownKeyAccount.connections = (unknownKeyAccount.connections as Array<Record<string, unknown>>).map((connection) => ({ ...connection, credential: 'leak' }))
expectNativeImportAccountRejected(unknownKeyAccount, '负向样例（connections 含未知键）')
const missingTrioAccount = { ...handWrittenXaiAccount } as Record<string, unknown>
delete missingTrioAccount.healthCheckEndpointMode
expectNativeImportAccountRejected(missingTrioAccount, '负向样例（缺 healthCheckEndpointMode）')

console.log('账户导入协议回归通过：模板 JSON、AI 提示词与两份协议的关键语义矩阵保持一致；模板 8 账号与手填 xAI 样例均通过 native 导入必填键同规则复算，4 个负向样例均被正确拒绝')

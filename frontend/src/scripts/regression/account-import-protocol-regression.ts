import {
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  GPT_VENDOR_CODE
} from '@/shared/providerProtocol'
import { accountImportProtocolMarkdown, aiConversionPrompt, importTemplate } from '../../views/accounts/accountImportProtocol'

interface ImportTemplateAccount {
  ref?: string
  providerCode?: string
  providerProtocolProfileId?: string
  connectionType?: string
  clientCompatibility?: string
  type?: string
  credentials?: Record<string, unknown>
  groupName?: string
}

interface ImportTemplateDocument {
  type?: string
  version?: number
  proxies?: unknown[]
  accounts?: ImportTemplateAccount[]
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

const template = JSON.parse(importTemplate) as ImportTemplateDocument

assertEqual(template.type, 'juhe-ai-account-import', '导入模板 type 必须保持当前协议')
assertEqual(template.version, 1, '导入模板 version 必须保持 v1')
assertEqual(template.accounts?.length, 8, '导入模板应继续覆盖 GPT API Key、GPT OAuth、DeepSeek 双接入、GLM 三接入和 Anthropic API Key 账号')
assertEqual(template.proxies?.length, 1, '导入模板应继续包含代理 ref 示例')

const apiKeyAccount = template.accounts?.find((account) => account.type === 'api_key')
const oauthAccount = template.accounts?.find((account) => account.type === 'oauth')
const deepSeekOpenAIAccount = template.accounts?.find((account) => account.ref === 'deepseek-key-001')
const deepSeekClaudeCodeAccount = template.accounts?.find((account) => account.ref === 'deepseek-claude-code-001')
const glmGeneralAccount = template.accounts?.find((account) => account.providerCode === GLM_PROVIDER_CODE && account.connectionType === 'general_api_key')
const glmCodingAccount = template.accounts?.find((account) => account.providerCode === GLM_PROVIDER_CODE && account.connectionType === 'coding_api_key')
const glmCodingAnthropicAccount = template.accounts?.find((account) => account.providerCode === GLM_PROVIDER_CODE && account.connectionType === 'coding_anthropic_api_key')
const anthropicAccount = template.accounts?.find((account) => account.providerCode === ANTHROPIC_PROVIDER_CODE)
assertDefined(apiKeyAccount, '导入模板应包含 API Key 账号示例')
assertDefined(oauthAccount, '导入模板应包含 OAuth 账号示例')
assertDefined(deepSeekOpenAIAccount, '导入模板应包含 DeepSeek OpenAI-compatible API Key 账号示例')
assertDefined(deepSeekClaudeCodeAccount, '导入模板应包含 DeepSeek Claude Code API Key 账号示例')
assertDefined(glmGeneralAccount, '导入模板应包含 GLM 通用 API Key 账号示例')
assertDefined(glmCodingAccount, '导入模板应包含 GLM Coding Plan Key 账号示例')
assertDefined(glmCodingAnthropicAccount, '导入模板应包含 GLM Coding Anthropic Key 账号示例')
assertDefined(anthropicAccount, '导入模板应包含 Anthropic API Key 账号示例')
assertEqual(apiKeyAccount.providerCode, GPT_VENDOR_CODE, 'API Key 示例应继续使用 GPT 供应商')
assertFalse(Object.prototype.hasOwnProperty.call(apiKeyAccount, 'clientCompatibility'), 'API Key 示例不应暴露账号兼容字段')
assertEqual(oauthAccount.providerCode, GPT_VENDOR_CODE, 'OAuth 示例应继续使用 GPT 供应商')
assertFalse(Object.prototype.hasOwnProperty.call(oauthAccount, 'clientCompatibility'), 'OAuth 示例不应暴露账号兼容字段')
assertEqual(typeof apiKeyAccount.credentials?.api_key, 'string', 'API Key 示例必须保留 credentials.api_key')
assertTrue(Array.isArray(apiKeyAccount.credentials?.supported_endpoint_modes), 'API Key 示例应包含 supported_endpoint_modes')
assertEqual(typeof oauthAccount.credentials?.refresh_token, 'string', 'OAuth 示例必须保留 refresh_token')
assertTrue(Array.isArray(oauthAccount.credentials?.supported_endpoint_modes), 'OAuth 示例应包含 supported_endpoint_modes')
assertEqual(typeof deepSeekOpenAIAccount.credentials?.api_key, 'string', 'DeepSeek OpenAI-compatible 示例必须保留 credentials.api_key')
assertEqual(deepSeekOpenAIAccount.credentials?.base_url, 'https://api.deepseek.com', 'DeepSeek OpenAI-compatible 示例应使用官方 base URL')
assertEqual(JSON.stringify(deepSeekOpenAIAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse']), 'DeepSeek OpenAI-compatible 示例应只启用 Chat Completions JSON/Streaming')
assertFalse(Object.prototype.hasOwnProperty.call(deepSeekOpenAIAccount, 'clientCompatibility'), 'DeepSeek OpenAI-compatible 普通示例不应默认暴露客户端兼容字段')
assertEqual(deepSeekClaudeCodeAccount.providerProtocolProfileId, DEEPSEEK_ANTHROPIC_V1_PROFILE_ID, 'DeepSeek Claude Code 示例应显式填写 Anthropic v1 profile')
assertFalse(Object.prototype.hasOwnProperty.call(deepSeekClaudeCodeAccount, 'clientCompatibility'), 'DeepSeek Claude Code 示例不应暴露客户端兼容字段')
assertEqual(deepSeekClaudeCodeAccount.credentials?.base_url, 'https://api.deepseek.com/anthropic', 'DeepSeek Claude Code 示例应使用 Anthropic-compatible base URL')
assertEqual(JSON.stringify(deepSeekClaudeCodeAccount.credentials?.supported_endpoint_modes), JSON.stringify(['messages_json', 'messages_sse']), 'DeepSeek Claude Code 示例应只启用 Messages JSON/Streaming')
assertEqual(glmGeneralAccount.clientCompatibility, 'openai_standard', 'GLM 通用示例应显式使用 OpenAI-compatible 客户端兼容')
assertEqual(glmCodingAccount.clientCompatibility, 'codex_responses', 'GLM Coding 示例应显式使用 Codex Responses 客户端兼容')
assertEqual(JSON.stringify(glmGeneralAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse']), 'GLM 通用示例应只启用 Chat Completions JSON/Streaming')
assertEqual(JSON.stringify(glmCodingAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse']), 'GLM Coding 示例应只启用 Chat Completions JSON/Streaming')
assertFalse(Object.prototype.hasOwnProperty.call(glmCodingAnthropicAccount, 'clientCompatibility'), 'GLM Coding Anthropic 示例不应暴露客户端兼容字段')
assertEqual(glmCodingAnthropicAccount.credentials?.base_url, 'https://open.bigmodel.cn/api/anthropic', 'GLM Coding Anthropic 示例应使用 Anthropic-compatible base URL')
assertEqual(JSON.stringify(glmCodingAnthropicAccount.credentials?.supported_endpoint_modes), JSON.stringify(['messages_json', 'messages_sse']), 'GLM Coding Anthropic 示例应只启用 Messages JSON/Streaming')
assertEqual(typeof anthropicAccount.credentials?.api_key, 'string', 'Anthropic 示例必须保留 credentials.api_key')
assertTrue(Array.isArray(anthropicAccount.credentials?.supported_endpoint_modes), 'Anthropic 示例应包含 supported_endpoint_modes')
assertFalse(Object.prototype.hasOwnProperty.call(anthropicAccount.credentials ?? {}, 'anthropic_version'), 'Anthropic 导入示例不应把 anthropic-version 当作账号凭据')
assertFalse(Object.prototype.hasOwnProperty.call(anthropicAccount.credentials ?? {}, 'anthropic_beta'), 'Anthropic 导入示例不应把 anthropic-beta 当作账号凭据')
assertEqual(typeof apiKeyAccount.groupName, 'string', '模板账号必须保留 groupName 示例')

assertMatch(aiConversionPrompt, /juhe-ai-account-import v1 JSON/, 'AI 提示词应继续要求输出当前导入协议 JSON')
assertMatch(aiConversionPrompt, /只输出合法 JSON/, 'AI 提示词应继续禁止输出解释或 Markdown')
assertMatch(aiConversionPrompt, /不要编造来源数据里不存在的 token/, 'AI 提示词应继续约束 token 不可编造')
assertMatch(aiConversionPrompt, /pending_test 或 disabled/, 'AI 提示词应允许不确定账户导入为待测试或停用')

assertMatch(accountImportProtocolMarkdown, /# juhe-ai AI 账户导入协议 v1/, '协议 Markdown 应继续保留标题')
assertMatch(accountImportProtocolMarkdown, /```json[\s\S]+juhe-ai-account-import[\s\S]+```/, '协议 Markdown 应继续包含 JSON 示例代码块')
assertTrue(accountImportProtocolMarkdown.includes(importTemplate), '协议 Markdown 的完整示例应继续嵌入导入模板')
assertMatch(accountImportProtocolMarkdown, /当前默认使用 `providerCode: "gpt"`/, '协议 Markdown 应继续说明默认 GPT providerCode')
assertMatch(accountImportProtocolMarkdown, /`deepseek`/, '协议 Markdown 应说明 DeepSeek providerCode')
assertMatch(accountImportProtocolMarkdown, /DeepSeek OpenAI-compatible 默认 Chat Completion \(JSON\/Streaming\)/, '协议 Markdown 应说明 DeepSeek OpenAI-compatible 默认接口能力')
assertMatch(accountImportProtocolMarkdown, /`clientCompatibility` 可选填写 `openai_standard` 或 `codex_responses`/, '协议 Markdown 应说明客户端兼容显式字段')
assertMatch(accountImportProtocolMarkdown, /\| `clientCompatibility` \| 否 \| string \|/, '协议 Markdown 应把 clientCompatibility 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /\| `providerProtocolProfileId` \| 否 \| string \|/, '协议 Markdown 应把 providerProtocolProfileId 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /\| `tags` \| 否 \| string\[\] \|/, '协议 Markdown 应把 tags 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /`active`、`pending_test` 或 `disabled`/, '协议 Markdown 应说明导入状态支持 pending_test')
assertMatch(accountImportProtocolMarkdown, /`status: "active"` 会转为 `pending_test`/, '协议 Markdown 应说明 active 导入创建会转为待测试')
assertMatch(accountImportProtocolMarkdown, /GLM Coding OpenAI Chat、DeepSeek OpenAI-compatible、Gemini OpenAI Chat 如需承接 Codex 客户端必须显式填写 `codex_responses`，不要再通过账户 `modelMappings` 做 Responses 到 Chat 的跨协议映射/, '协议 Markdown 应说明 GLM、DeepSeek 与 Gemini OpenAI Chat Codex bridge 的显式开关')
assertMatch(accountImportProtocolMarkdown, /`connectionType: "coding_anthropic_api_key"`/, '协议 Markdown 应说明 GLM Coding Anthropic 接入类型')
assertMatch(accountImportProtocolMarkdown, /DeepSeek Claude Code 必须显式填写 `providerProtocolProfileId: "profile_deepseek_anthropic_v1"`/, '协议 Markdown 应说明 DeepSeek Claude Code profile')
assertMatch(accountImportProtocolMarkdown, /DeepSeek Claude Code、GLM Coding Anthropic、官方 Anthropic 和 Gemini 原生档案不填写 `clientCompatibility`/, '协议 Markdown 应说明 Anthropic v1 第三方档案与 Gemini 原生档案不填写客户端兼容字段')
assertMatch(accountImportProtocolMarkdown, /`modelMappings` 只做账号模型别名/, '协议 Markdown 应说明 modelMappings 只做账号模型别名')
assertMatch(accountImportProtocolMarkdown, /其他跨协议方向不要写入账户导入数据/, '协议 Markdown 应说明跨协议方向不写入账户导入数据')
assertMatch(accountImportProtocolMarkdown, /需要把 OpenAI Responses 转到 Chat Completions[\s\S]+请导入账号后在 API Key 显式混合路由配置规则/, '协议 Markdown 应说明跨协议桥接改到 API Key 显式混合路由')
assertMatch(accountImportProtocolMarkdown, /两者都必须来自当前账户供应商模型目录/, '协议 Markdown 应说明 source/upstream 均受当前供应商目录约束')
assertMatch(accountImportProtocolMarkdown, /DeepSeek Claude Code 与 GLM Coding Anthropic 使用 Anthropic v1 Messages 原生协议，`credentials\.supported_endpoint_modes` 填 `messages_json`、`messages_sse`，不要填 `message_token_counting`/, '协议 Markdown 应说明第三方 Anthropic 档案不支持 count_tokens')
assertMatch(accountImportProtocolMarkdown, /supported_endpoint_modes/, '协议 Markdown 应说明接口能力限制字段')
assertMatch(accountImportProtocolMarkdown, /不接受 `credentials\.anthropic_version` 或 `credentials\.anthropic_beta`/, '协议 Markdown 应明确 Anthropic header 不属于账号凭据')
assertMatch(accountImportProtocolMarkdown, /`proxyRef` 和 `proxyProfileId` 不能同时填写/, '协议 Markdown 应继续说明代理字段互斥')
assertMatch(formalProtocolMarkdown, /# AI 账户导入协议/, '正式协议文档应可读取')
assertMatch(formalProtocolMarkdown, /`modelMappings` 只做账号模型别名/, '正式协议文档应说明 modelMappings 只做账号模型别名')
assertMatch(formalProtocolMarkdown, /其他跨协议方向不要写入账户导入数据/, '正式协议文档应说明跨协议方向不写入账户导入数据')
assertMatch(formalProtocolMarkdown, /API Key 显式混合路由配置规则/, '正式协议文档应说明跨协议桥接改到 API Key 显式混合路由')

console.log('账户导入协议回归通过：模板 JSON、AI 提示词和协议 Markdown 保持一致')

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

function assertDefined<T>(value: T | undefined, message: string): asserts value is T {
  if (value === undefined) {
    throw new Error(message)
  }
}

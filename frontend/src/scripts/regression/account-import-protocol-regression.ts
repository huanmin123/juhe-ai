import { ANTHROPIC_PROVIDER_CODE, DEEPSEEK_PROVIDER_CODE, GLM_PROVIDER_CODE, GPT_VENDOR_CODE } from '@/shared/providerProtocol'
import { accountImportProtocolMarkdown, aiConversionPrompt, importTemplate } from '../../views/accounts/accountImportProtocol'

interface ImportTemplateAccount {
  providerCode?: string
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

const template = JSON.parse(importTemplate) as ImportTemplateDocument

assertEqual(template.type, 'juhe-ai-account-import', '导入模板 type 必须保持当前协议')
assertEqual(template.version, 1, '导入模板 version 必须保持 v1')
assertEqual(template.accounts?.length, 6, '导入模板应继续覆盖 GPT API Key、GPT OAuth、DeepSeek API Key、GLM 双接入和 Anthropic API Key 账号')
assertEqual(template.proxies?.length, 1, '导入模板应继续包含代理 ref 示例')

const apiKeyAccount = template.accounts?.find((account) => account.type === 'api_key')
const oauthAccount = template.accounts?.find((account) => account.type === 'oauth')
const deepSeekAccount = template.accounts?.find((account) => account.providerCode === DEEPSEEK_PROVIDER_CODE)
const glmGeneralAccount = template.accounts?.find((account) => account.providerCode === GLM_PROVIDER_CODE && account.connectionType === 'general_api_key')
const glmCodingAccount = template.accounts?.find((account) => account.providerCode === GLM_PROVIDER_CODE && account.connectionType === 'coding_api_key')
const anthropicAccount = template.accounts?.find((account) => account.providerCode === ANTHROPIC_PROVIDER_CODE)
assertDefined(apiKeyAccount, '导入模板应包含 API Key 账号示例')
assertDefined(oauthAccount, '导入模板应包含 OAuth 账号示例')
assertDefined(deepSeekAccount, '导入模板应包含 DeepSeek API Key 账号示例')
assertDefined(glmGeneralAccount, '导入模板应包含 GLM 通用 API Key 账号示例')
assertDefined(glmCodingAccount, '导入模板应包含 GLM Coding Plan Key 账号示例')
assertDefined(anthropicAccount, '导入模板应包含 Anthropic API Key 账号示例')
assertEqual(apiKeyAccount.providerCode, GPT_VENDOR_CODE, 'API Key 示例应继续使用 GPT 供应商')
assertFalse(Object.prototype.hasOwnProperty.call(apiKeyAccount, 'clientCompatibility'), 'API Key 示例不应暴露账号兼容字段')
assertEqual(oauthAccount.providerCode, GPT_VENDOR_CODE, 'OAuth 示例应继续使用 GPT 供应商')
assertFalse(Object.prototype.hasOwnProperty.call(oauthAccount, 'clientCompatibility'), 'OAuth 示例不应暴露账号兼容字段')
assertEqual(typeof apiKeyAccount.credentials?.api_key, 'string', 'API Key 示例必须保留 credentials.api_key')
assertTrue(Array.isArray(apiKeyAccount.credentials?.supported_endpoint_modes), 'API Key 示例应包含 supported_endpoint_modes')
assertEqual(typeof oauthAccount.credentials?.refresh_token, 'string', 'OAuth 示例必须保留 refresh_token')
assertTrue(Array.isArray(oauthAccount.credentials?.supported_endpoint_modes), 'OAuth 示例应包含 supported_endpoint_modes')
assertEqual(typeof deepSeekAccount.credentials?.api_key, 'string', 'DeepSeek 示例必须保留 credentials.api_key')
assertEqual(deepSeekAccount.credentials?.base_url, 'https://api.deepseek.com', 'DeepSeek 示例应使用官方 base URL')
assertEqual(JSON.stringify(deepSeekAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse']), 'DeepSeek 示例应只启用 Chat JSON/SSE')
assertEqual(glmGeneralAccount.clientCompatibility, 'openai_standard', 'GLM 通用示例应显式使用 OpenAI 标准客户端兼容')
assertEqual(glmCodingAccount.clientCompatibility, 'codex_responses', 'GLM Coding 示例应显式使用 Codex Responses 客户端兼容')
assertEqual(JSON.stringify(glmGeneralAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse']), 'GLM 通用示例应只启用 Chat JSON/SSE')
assertEqual(JSON.stringify(glmCodingAccount.credentials?.supported_endpoint_modes), JSON.stringify(['chat_json', 'chat_sse']), 'GLM Coding 示例应只启用 Chat JSON/SSE')
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
assertMatch(accountImportProtocolMarkdown, /DeepSeek API Key 默认 Chat JSON\/SSE/, '协议 Markdown 应说明 DeepSeek 默认接口能力')
assertMatch(accountImportProtocolMarkdown, /`clientCompatibility` 可选填写 `openai_standard` 或 `codex_responses`/, '协议 Markdown 应说明客户端兼容显式字段')
assertMatch(accountImportProtocolMarkdown, /\| `clientCompatibility` \| 否 \| string \|/, '协议 Markdown 应把 clientCompatibility 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /\| `providerProtocolProfileId` \| 否 \| string \|/, '协议 Markdown 应把 providerProtocolProfileId 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /\| `tags` \| 否 \| string\[\] \|/, '协议 Markdown 应把 tags 暴露为导入字段')
assertMatch(accountImportProtocolMarkdown, /`active`、`pending_test` 或 `disabled`/, '协议 Markdown 应说明导入状态支持 pending_test')
assertMatch(accountImportProtocolMarkdown, /`status: "active"` 会转为 `pending_test`/, '协议 Markdown 应说明 active 导入创建会转为待测试')
assertMatch(accountImportProtocolMarkdown, /GLM Coding Plan、DeepSeek OpenAI-compatible 如需承接 Codex 客户端必须显式填写 `codex_responses`/, '协议 Markdown 应说明 GLM 与 DeepSeek Codex bridge 的显式开关')
assertMatch(accountImportProtocolMarkdown, /DeepSeek bridge 账号的 `credentials\.supported_endpoint_modes` 仍保存真实上游能力 `chat_json`、`chat_sse`/, '协议 Markdown 应说明 DeepSeek Codex bridge 仍保存 Chat endpoint modes')
assertMatch(accountImportProtocolMarkdown, /supported_endpoint_modes/, '协议 Markdown 应说明接口能力限制字段')
assertMatch(accountImportProtocolMarkdown, /不接受 `credentials\.anthropic_version` 或 `credentials\.anthropic_beta`/, '协议 Markdown 应明确 Anthropic header 不属于账号凭据')
assertMatch(accountImportProtocolMarkdown, /`proxyRef` 和 `proxyProfileId` 不能同时填写/, '协议 Markdown 应继续说明代理字段互斥')

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

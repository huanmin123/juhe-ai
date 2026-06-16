import { GPT_VENDOR_CODE } from '@/shared/providerProtocol'
import { accountImportProtocolMarkdown, aiConversionPrompt, importTemplate } from '../../views/accounts/accountImportProtocol'

interface ImportTemplateAccount {
  providerCode?: string
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
assertEqual(template.accounts?.length, 2, '导入模板应继续覆盖 API Key 和 OAuth 两类账号')
assertEqual(template.proxies?.length, 1, '导入模板应继续包含代理 ref 示例')

const apiKeyAccount = template.accounts?.find((account) => account.type === 'api_key')
const oauthAccount = template.accounts?.find((account) => account.type === 'oauth')
assertDefined(apiKeyAccount, '导入模板应包含 API Key 账号示例')
assertDefined(oauthAccount, '导入模板应包含 OAuth 账号示例')
assertEqual(apiKeyAccount.providerCode, GPT_VENDOR_CODE, 'API Key 示例应继续使用 GPT 供应商')
assertEqual(oauthAccount.providerCode, GPT_VENDOR_CODE, 'OAuth 示例应继续使用 GPT 供应商')
assertEqual(typeof apiKeyAccount.credentials?.api_key, 'string', 'API Key 示例必须保留 credentials.api_key')
assertEqual(typeof oauthAccount.credentials?.refresh_token, 'string', 'OAuth 示例必须保留 refresh_token')
assertEqual(typeof apiKeyAccount.groupName, 'string', '模板账号必须保留 groupName 示例')

assertMatch(aiConversionPrompt, /juhe-ai-account-import v1 JSON/, 'AI 提示词应继续要求输出当前导入协议 JSON')
assertMatch(aiConversionPrompt, /只输出合法 JSON/, 'AI 提示词应继续禁止输出解释或 Markdown')
assertMatch(aiConversionPrompt, /不要编造来源数据里不存在的 token/, 'AI 提示词应继续约束 token 不可编造')

assertMatch(accountImportProtocolMarkdown, /# juhe-ai AI 账户导入协议 v1/, '协议 Markdown 应继续保留标题')
assertMatch(accountImportProtocolMarkdown, /```json[\s\S]+juhe-ai-account-import[\s\S]+```/, '协议 Markdown 应继续包含 JSON 示例代码块')
assertTrue(accountImportProtocolMarkdown.includes(importTemplate), '协议 Markdown 的完整示例应继续嵌入导入模板')
assertMatch(accountImportProtocolMarkdown, /当前默认使用 `providerCode: "gpt"`/, '协议 Markdown 应继续说明默认 GPT providerCode')
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

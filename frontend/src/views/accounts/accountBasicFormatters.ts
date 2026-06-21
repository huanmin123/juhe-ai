import { serverDateTimeTimestamp } from '@/shared/formatters'
import {
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  isDeepSeekProviderCode,
  isGlmProviderCode,
  isGptVendorCode
} from '@/shared/providerProtocol'
import type { AccountClientCompatibility, AccountSummary, AccountType } from '@/types/domain'

function hasAuthorizedAccessType(account: AccountSummary): boolean {
  return account.accessType === 'authorized'
}

export function accountDisplayExpiresAt(account: AccountSummary): string | undefined {
  if (hasAuthorizedAccessType(account) && account.authorizationExpiresAt) {
    return account.authorizationExpiresAt
  }
  return account.accountExpiresAt
}

export function isAccountDisplayExpired(account: AccountSummary): boolean {
  const expiresAt = accountDisplayExpiresAt(account)
  if (!expiresAt) return false
  const time = serverDateTimeTimestamp(expiresAt)
  return time !== undefined && time <= Date.now()
}

export function accountDisplayName(account: AccountSummary): string {
  if (!hasAuthorizedAccessType(account)) return account.name
  const cleaned = account.name.replace(/（授权(?: [^）]+)?）$/, '')
  return cleaned || account.name
}

export function accountTypeText(type: AccountType) {
  if (type === 'oauth') return 'OAuth'
  if (type === 'api_key') return 'API Key'
  return type || '-'
}

export function accountClientCompatibilityText(value?: AccountClientCompatibility): string {
  if (value === 'codex_responses') return 'Codex Responses'
  return 'OpenAI 标准'
}

export function accountTypeTitle(providerName: string, type: AccountType) {
  if (type === 'oauth') return `${providerName} OAuth`
  if (type === 'api_key') return `${providerName} API Key`
  return `${providerName} ${type}`.trim()
}

export function accountTypeDescription(providerCode: string, type: AccountType, providerProtocolProfileId?: string) {
  if (isGptVendorCode(providerCode) && type === 'oauth') return '适合 GPT / ChatGPT OAuth 授权账户；网关只支持 Responses / compact 路径。'
  if (isGptVendorCode(providerCode) && type === 'api_key') return '适合 GPT 官方或 OpenAI v1 兼容透传，可配置 Base URL。'
  if (isGlmProviderCode(providerCode) && type === 'api_key' && providerProtocolProfileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID) return '适合智谱通用 GLM API Key；默认只启用 Chat JSON/SSE，不承接 Responses。'
  if (isGlmProviderCode(providerCode) && type === 'api_key' && providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID) return '适合 GLM Coding Plan Key；使用 Coding 专用 Base URL，可在客户端兼容中显式选择 Codex Responses 桥接。'
  if (isGlmProviderCode(providerCode) && type === 'api_key') return '适合智谱 GLM API Key；通用 API 与 Coding Plan 需要选择对应接入档案。'
  if (isDeepSeekProviderCode(providerCode) && type === 'api_key') return '适合 DeepSeek OpenAI-compatible Chat Completions 直连；默认只启用 Chat JSON/SSE。'
  return '该账户类型会使用供应商定义的创建流程。'
}

export function asString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

export function normalizeKeyword(value: unknown): string {
  return String(value ?? '').trim().toLowerCase()
}

export function accountLastUsedAt(account: AccountSummary): string | undefined {
  return account.lastUsedAt
}

export function compareAccountLastUsedAt(left: AccountSummary, right: AccountSummary): number {
  return timestampOf(accountLastUsedAt(left)) - timestampOf(accountLastUsedAt(right))
}

export function compareAccountExpiresAt(left: AccountSummary, right: AccountSummary): number {
  return timestampOf(accountDisplayExpiresAt(left)) - timestampOf(accountDisplayExpiresAt(right))
}

export function compareAccountConcurrency(left: AccountSummary, right: AccountSummary): number {
  return left.concurrencyLimit - right.concurrencyLimit || left.currentConcurrency - right.currentConcurrency || left.name.localeCompare(right.name, 'zh-CN')
}

function timestampOf(value?: string): number {
  if (!value) return 0
  return serverDateTimeTimestamp(value) ?? 0
}

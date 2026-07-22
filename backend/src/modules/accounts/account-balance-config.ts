import { z } from 'zod'
import { createHash } from 'node:crypto'

import type {
  AccountBalanceCustomConfig,
  AccountBalanceQueryConfig
} from './account-balance.types.js'

const decimalPattern = /^(?:0|[1-9]\d*)(?:\.\d+)?$/
const jsonPointerPattern = /^(?:\/(?:[^~/]|~[01])*)*$/
const accountBalanceBuiltinAdapterSchema = z.enum(['sub2api', 'newapi', 'litellm', 'user_balance'])

export const MULTI_KEY_ACCOUNT_BALANCE_QUERY_MESSAGE = '多 Key 账户不支持余额查询，保存后将自动关闭余额查询'

const accountBalanceCustomConfigSchema = z.object({
  path: z.string().trim().min(1),
  remainingPointer: z.string().trim().optional(),
  totalPointer: z.string().trim().optional(),
  usedPointer: z.string().trim().optional(),
  divisor: z.string().trim().optional()
}).strict().superRefine((value, context) => {
  if (!value.path.startsWith('/') || value.path.startsWith('//')) {
    context.addIssue({ code: z.ZodIssueCode.custom, path: ['path'], message: '自定义查询地址必须是同源相对路径' })
  }
  for (const field of ['remainingPointer', 'totalPointer', 'usedPointer'] as const) {
    const pointer = value[field]
    if (pointer !== undefined && !jsonPointerPattern.test(pointer)) {
      context.addIssue({ code: z.ZodIssueCode.custom, path: [field], message: `${field} 必须是合法 JSON Pointer` })
    }
  }
  const hasRemaining = Boolean(value.remainingPointer)
  const hasTotalAndUsed = Boolean(value.totalPointer) && Boolean(value.usedPointer)
  if (hasRemaining === hasTotalAndUsed) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      message: '自定义查询必须配置余额 JSON Pointer，或同时配置总额和已用 JSON Pointer'
    })
  }
  if (value.divisor !== undefined && (!decimalPattern.test(value.divisor) || /^0(?:\.0+)?$/.test(value.divisor))) {
    context.addIssue({ code: z.ZodIssueCode.custom, path: ['divisor'], message: '自定义金额除数必须是正数' })
  }
})

export const accountBalanceQueryConfigSchema = z.object({
  adapter: z.enum(['builtin', 'custom']),
  intervalMinutes: z.number().int().min(1).max(10).optional(),
  preferredBuiltinAdapter: accountBalanceBuiltinAdapterSchema.optional(),
  custom: accountBalanceCustomConfigSchema.optional()
}).strict().superRefine((value, context) => {
  if (value.adapter === 'custom' && !value.custom) {
    context.addIssue({ code: z.ZodIssueCode.custom, path: ['custom'], message: '自定义查询必须提供查询配置' })
  }
  if (value.adapter !== 'custom' && value.custom) {
    context.addIssue({ code: z.ZodIssueCode.custom, path: ['custom'], message: '内置查询类型不能提供自定义配置' })
  }
  if (value.adapter === 'custom' && value.preferredBuiltinAdapter) {
    context.addIssue({ code: z.ZodIssueCode.custom, path: ['preferredBuiltinAdapter'], message: '自定义查询不能提供内置适配偏好' })
  }
})

export function normalizeAccountBalanceConfig(input: unknown): AccountBalanceQueryConfig {
  const parsed = accountBalanceQueryConfigSchema.safeParse(input)
  if (!parsed.success) {
    const issue = parsed.error.issues[0]
    const field = issue?.path.at(-1)
    if (field === 'intervalMinutes') throw new Error(`余额刷新周期无效：${issue.message}`)
    if (field === 'adapter') throw new Error(`余额查询类型无效：${issue.message}`)
    throw new Error(issue?.message ?? '余额查询配置无效')
  }
  return {
    adapter: parsed.data.adapter,
    intervalMinutes: parsed.data.intervalMinutes ?? 5,
    ...(parsed.data.preferredBuiltinAdapter ? { preferredBuiltinAdapter: parsed.data.preferredBuiltinAdapter } : {}),
    ...(parsed.data.custom ? { custom: parsed.data.custom as AccountBalanceCustomConfig } : {})
  }
}

interface AccountBalanceCapabilityInput {
  type: string
  credentials?: Record<string, unknown>
  authorizationInstanceAuthorizationId?: string | null
  accountAuthorizationId?: string | null
  accessType?: string
}

export interface AccountBalanceCapabilityDecision {
  enabled: boolean
  autoDisabledForMultipleApiKeys: boolean
}

export function validateAccountBalanceCapability(
  account: AccountBalanceCapabilityInput,
  enabled: boolean
): AccountBalanceCapabilityDecision {
  const authorizedInstance = Boolean(
    account.authorizationInstanceAuthorizationId
    || account.accountAuthorizationId
    || account.accessType === 'authorized'
  )
  if (authorizedInstance) {
    if (enabled) throw new Error('授权实例不能配置上游余额查询')
    return { enabled: false, autoDisabledForMultipleApiKeys: false }
  }
  if (enabled && account.type !== 'api_key') {
    throw new Error('上游余额查询仅支持 API Key 账户')
  }
  const apiKeyCount = effectiveAccountApiKeyCount(account.credentials)
  if (account.type === 'api_key' && apiKeyCount > 1) {
    return { enabled: false, autoDisabledForMultipleApiKeys: true }
  }
  if (!enabled) {
    return { enabled: false, autoDisabledForMultipleApiKeys: false }
  }
  if (apiKeyCount !== 1) {
    throw new Error('上游余额查询需要一个有效的 API Key')
  }
  return { enabled: true, autoDisabledForMultipleApiKeys: false }
}

export function effectiveAccountApiKeyCount(credentials: Record<string, unknown> | undefined): number {
  return effectiveAccountApiKeys(credentials).length
}

export function effectiveAccountApiKeys(credentials: Record<string, unknown> | undefined): string[] {
  const pool = Array.isArray(credentials?.api_keys)
    ? credentials.api_keys
    : []
  const keys = new Set(
    pool
      .filter((value): value is string => typeof value === 'string')
      .map((value) => value.trim())
      .filter(Boolean)
  )
  if (keys.size > 0) return [...keys]
  const legacyKey = typeof credentials?.api_key === 'string' ? credentials.api_key.trim() : ''
  return legacyKey ? [legacyKey] : []
}

export function accountBalanceQueryIdentity(input: {
  enabled: boolean
  config?: AccountBalanceQueryConfig
  providerCode: string
  accountType: string
  credentials?: Record<string, unknown>
  proxyProfileId?: string
}): Record<string, unknown> {
  return {
    enabled: input.enabled,
    normalizedConfig: input.config ? normalizeAccountBalanceConfig(input.config) : undefined,
    providerCode: input.providerCode,
    accountType: input.accountType,
    effectiveApiKeyFingerprints: effectiveAccountApiKeys(input.credentials).map(balanceApiKeyFingerprint),
    normalizedBaseUrl: normalizedBalanceBaseUrl(input.credentials?.base_url),
    proxyProfileId: input.proxyProfileId?.trim() || undefined
  }
}

function normalizedBalanceBaseUrl(value: unknown): string {
  const text = typeof value === 'string' ? value.trim() : ''
  if (!text) return ''
  try {
    const url = new URL(text)
    url.hash = ''
    url.search = ''
    return url.toString().replace(/\/+$/, '')
  } catch {
    return text.replace(/\/+$/, '')
  }
}

function balanceApiKeyFingerprint(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

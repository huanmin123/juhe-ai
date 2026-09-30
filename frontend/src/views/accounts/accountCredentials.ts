import {
  writeAccountErrorPolicyToCredentials
} from './accountErrorPolicyPayload'
import {
  writeAccountResponseInspectionRulesToCredentials
} from './accountResponseInspectionPolicyPayload'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { asString } from './accountBasicFormatters'
import { accountApiKeysForForm } from './accountEditFormPayload'
import type { AccountFormModel } from './accountFormTypes'
import { loadAccountQuotaRecoveryPolicy } from './accountQuotaRecoveryPolicyTypes'
import { compactAccountCredentials } from './accountFormDefaults'
import { writeAccountGptRequestOverrides } from './accountGptRequestOverrides'

/**
 * BUG-0238 凭据占位符（前后端契约常量）：编辑明细接口把敏感凭据键替换为该占位内容，
 * 保存 payload 中占位值一律视为"未修改"，不得提交（后端另有同规则兜底）。
 */
export const CREDENTIAL_CIPHER_PLACEHOLDER = '__ENCRYPTED__'

export function isCredentialCipherPlaceholder(value: unknown): boolean {
  return typeof value === 'string' && value.trim() === CREDENTIAL_CIPHER_PLACEHOLDER
}

/** 敏感单键的 payload 值：占位符转为空串（交由 compact 省略），其余原样。 */
function credentialPayloadText(value: string): string {
  return isCredentialCipherPlaceholder(value) ? '' : value
}

const oauthCredentialMetadataKeys = [
  'expires_at',
  'client_id',
  'id_token',
  'token_type',
  'scope',
  'email',
  'account_id',
  'organization_id',
  'chatgpt_user_id',
  'plan_type',
  'sub',
  'team_id',
  'subscription_tier',
  'entitlement_status',
  'base_url',
  'supported_endpoint_modes'
] as const

export function buildAccountCredentials(input: {
  currentCredentials?: Record<string, unknown>
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  form: AccountFormModel
}): Record<string, unknown> {
  const credentials: Record<string, unknown> = input.form.type === 'api_key'
    ? buildApiKeyCredentials(input.form)
    : input.form.type === 'google_oauth'
      ? buildGoogleOAuthCredentials(input.form)
      : buildOAuthCredentials(input.form, input.currentCredentials ?? {})
  writeAccountGptRequestOverrides(credentials, input.form)
  writeAccountErrorPolicyToCredentials(credentials, input.errorPolicyRules)
  if (input.form.errorHandlingRuleOverrides?.length) {
    credentials.error_handling_rule_overrides = input.form.errorHandlingRuleOverrides
  }
  writeAccountResponseInspectionRulesToCredentials(credentials, input.responseInspectionRules)
  if (input.form.quotaRecoveryPolicy && Object.keys(input.form.quotaRecoveryPolicy).length) {
    credentials.quota_recovery_policy = loadAccountQuotaRecoveryPolicy(input.form.quotaRecoveryPolicy)
  }
  return credentials
}

/**
 * BUG-0243：api_key 密钥池保存守卫的基线——从（可能已掩码的）已存凭据提取
 * 池行数、策略与权重。掩码池 api_keys 各行均为占位符，但行数与权重列仍承载真实语义。
 */
export interface AccountApiKeyPoolBaseline {
  poolCount: number
  strategy?: string
  weights?: number[]
}

export function accountApiKeyPoolBaseline(credentials: Record<string, unknown> | undefined): AccountApiKeyPoolBaseline | undefined {
  if (!credentials) return undefined
  const values = Array.isArray(credentials.api_keys) && credentials.api_keys.length
    ? credentials.api_keys
    : [credentials.api_key]
  let poolCount = 0
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) poolCount += 1
  }
  if (!poolCount) return undefined
  const weights = Array.isArray(credentials.api_key_weights)
    ? credentials.api_key_weights.filter((value): value is number => typeof value === 'number')
    : []
  return {
    poolCount,
    strategy: typeof credentials.api_key_strategy === 'string' ? credentials.api_key_strategy : undefined,
    weights: weights.length ? weights : undefined
  }
}

/**
 * BUG-0243：api_key 表单存在密文占位行时的保存守卫（在保存前校验链调用，不在此改写数据）。
 * - 混合行（占位 + 真实值）：直接阻断——按占位过滤逻辑保存会把池静默截断为真实行子集，
 *   其余未揭示的真实 Key 被删除。
 * - 纯占位池：行数/策略/权重相对已存基线有变化时同样阻断——这些修改会在占位过滤后
 *   与基线一致化而被"未检测到修改"静默丢弃。
 * 无需阻断时返回 undefined。
 */
export function validateAccountApiKeyCipherRows(
  form: AccountFormModel,
  baseline?: AccountApiKeyPoolBaseline
): string | undefined {
  if (form.type !== 'api_key') return undefined
  const rows = normalizedAccountApiKeys(form)
  const placeholderRowCount = rows.filter((row) => isCredentialCipherPlaceholder(row)).length
  if (!placeholderRowCount) return undefined
  if (placeholderRowCount < rows.length) {
    return '部分密钥仍为密文占位，请先点击眼睛获取明文，或清空占位行后再保存'
  }
  if (baseline && apiKeyPoolShapeChanged(form, rows.length, baseline)) {
    return '密钥仍为密文占位，修改密钥行、策略或权重前请先点击眼睛获取明文'
  }
  return undefined
}

function apiKeyPoolShapeChanged(form: AccountFormModel, rowCount: number, baseline: AccountApiKeyPoolBaseline): boolean {
  if (rowCount !== baseline.poolCount) return true
  if (typeof baseline.strategy === 'string' && form.apiKeyStrategy !== baseline.strategy) return true
  if (baseline.weights?.length) {
    const weights = normalizedAccountApiKeyWeights(form, rowCount)
    if (weights.length !== baseline.weights.length) return true
    if (weights.some((weight, index) => weight !== baseline.weights?.[index])) return true
  }
  return false
}

function buildApiKeyCredentials(form: AccountFormModel): Record<string, unknown> {
  const apiKeys = normalizedAccountApiKeys(form)
  // BUG-0238：占位行不代表用户输入，保存前按行剔除；权重随行同步过滤。
  const weights = normalizedAccountApiKeyWeights(form, apiKeys.length)
  const keptRows = apiKeys
    .map((key, index) => ({ key, weight: weights[index] }))
    .filter((row) => !isCredentialCipherPlaceholder(row.key))
  const keptKeys = keptRows.map((row) => row.key)
  const apiKey = keptKeys[0] ?? ''
  const credentials = compactAccountCredentials({
    api_key: apiKey,
    base_url: form.baseUrl,
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
  if (keptKeys.length > 1) {
    credentials.api_keys = keptKeys
    credentials.api_key_strategy = form.apiKeyStrategy
    if (credentials.api_key_strategy === 'weighted_round_robin') {
      credentials.api_key_weights = keptRows.map((row) => row.weight)
    }
  }
  return credentials
}

export function normalizedAccountApiKeys(form: AccountFormModel): string[] {
  const values = form.apiKeys?.length ? form.apiKeys : [form.apiKey]
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const key = value.trim()
    if (!key) continue
    // BUG-0238：占位行各占一行（保持池行数/权重列/运行状态语义），不参与去重；
    // 真实键值仍按既有规则去重。
    if (isCredentialCipherPlaceholder(key)) {
      output.push(key)
      continue
    }
    if (seen.has(key)) continue
    seen.add(key)
    output.push(key)
  }
  return output
}

export function normalizedAccountApiKeyWeights(form: AccountFormModel, count = normalizedAccountApiKeys(form).length): number[] {
  return Array.from({ length: count }, (_, index) => {
    const value = Number(form.apiKeyWeights?.[index] ?? 1)
    return Number.isInteger(value) ? Math.min(100, Math.max(1, value)) : 1
  })
}

function normalizeCredentialText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function buildOAuthCredentials(form: AccountFormModel, currentCredentials: Record<string, unknown>): Record<string, unknown> {
  return compactAccountCredentials({
    ...pickOAuthCredentialMetadata(currentCredentials),
    base_url: form.baseUrl || normalizeCredentialText(currentCredentials.base_url),
    access_token: credentialPayloadText(form.accessToken),
    refresh_token: credentialPayloadText(form.refreshToken),
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
}

function buildGoogleOAuthCredentials(form: AccountFormModel): Record<string, unknown> {
  return compactAccountCredentials({
    access_token: credentialPayloadText(form.accessToken),
    refresh_token: credentialPayloadText(form.refreshToken),
    client_id: form.googleClientId,
    client_secret: credentialPayloadText(form.googleClientSecret),
    quota_project_id: form.googleQuotaProjectId,
    oauth_type: form.oauthType,
    tier_id: form.tierId,
    project_id: form.projectId,
    base_url: form.baseUrl,
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
}

/**
 * BUG-0238：把 reveal 接口取回的明文凭据写回编辑表单（按账户类型存在的敏感字段）。
 * BUG-0243 问题 5b：不再整表覆盖——占位态下用户已输入的新值（非占位、非空）优先保留，
 * 占位行/占位字段按序写入服务端真实值（对齐高级配置加载 preserveTypedApiKeys 语义）；
 * 服务端未被占位行消费的真实 Key 追加到行尾，不静默丢弃。
 */
export function applyRevealedAccountCredentials(form: AccountFormModel, credentials: Record<string, unknown>): void {
  if (form.type === 'api_key') {
    const apiKeys = mergeRevealedApiKeyRows(normalizedAccountApiKeys(form), accountApiKeysForForm(credentials))
    form.apiKey = apiKeys[0] ?? ''
    form.apiKeys = apiKeys
    return
  }
  form.accessToken = revealedOrTypedCredentialText(form.accessToken, credentials.access_token)
  form.refreshToken = revealedOrTypedCredentialText(form.refreshToken, credentials.refresh_token)
  if (form.type === 'google_oauth') {
    form.googleClientSecret = revealedOrTypedCredentialText(form.googleClientSecret, credentials.client_secret)
  }
}

/** 单键字段：非占位且非空的用户输入保留；占位/空值由服务端真实值替换。 */
function revealedOrTypedCredentialText(current: string, revealed: unknown): string {
  if (current.trim() && !isCredentialCipherPlaceholder(current)) return current
  return asString(revealed)
}

/** api_keys 行合并：占位行按序消费服务端真实值，用户已输入行原样保留，
 * 服务端剩余真实值追加行尾；真实值按既有规则去重。 */
function mergeRevealedApiKeyRows(rows: string[], revealedKeys: string[]): string[] {
  const remaining = revealedKeys.filter((key) => key.trim())
  const merged: string[] = []
  for (const row of rows) {
    if (isCredentialCipherPlaceholder(row)) {
      const revealed = remaining.shift()
      if (revealed !== undefined) merged.push(revealed)
      continue
    }
    merged.push(row)
  }
  merged.push(...remaining)
  const seen = new Set<string>()
  return merged.filter((key) => {
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function pickOAuthCredentialMetadata(currentCredentials: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const key of oauthCredentialMetadataKeys) {
    if (Object.prototype.hasOwnProperty.call(currentCredentials, key)) {
      output[key] = currentCredentials[key]
    }
  }
  return output
}

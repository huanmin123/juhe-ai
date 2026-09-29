import type { AccountFormModel } from './accountFormTypes'
import {
  isCredentialCipherPlaceholder,
  normalizedAccountApiKeys,
  normalizedAccountApiKeyWeights
} from './accountCredentials'
import { normalizeFormTagNames } from './accountEditFormPayload'
import {
  buildAccountUpdatePayload,
  type AccountSavePayload
} from './accountSavePayload'

export type AccountCredentialsPatch = Record<string, unknown | null>

export type AccountUpdateDelta = Record<string, unknown> & {
  expectedConfigRevision: number
  credentialsPatch?: AccountCredentialsPatch
}

export interface AccountBasicEditSnapshot {
  credentials: Record<string, unknown>
  values: Record<string, unknown>
}

export function buildAccountBasicEditSnapshot(
  form: AccountFormModel,
  currentCredentials: Record<string, unknown> = {}
): AccountBasicEditSnapshot {
  const values = compactRecord({
    name: form.name.trim(),
    concurrencyLimit: Math.trunc(Number(form.concurrencyLimit)),
    priority: Math.trunc(Number(form.priority)),
    ...(form.statusSelectionExplicit ? { status: form.status } : {}),
    superPriorityEnabled: form.privilege === 'super_priority',
    fallbackEnabled: form.privilege === 'fallback',
    groupId: form.groupId,
    tags: normalizeFormTagNames(form.tags).sort(),
    supportedModels: normalizedTextList(form.supportedModels).sort(),
    healthCheckModel: form.healthCheckModel.trim(),
    healthCheckEndpointMode: form.healthCheckEndpointMode
  })
  values.notes = form.notes
  return {
    credentials: buildBasicEditCredentials(form, currentCredentials),
    values
  }
}

export function buildAccountBasicUpdatePatch(
  current: AccountBasicEditSnapshot,
  baseline: AccountBasicEditSnapshot,
  expectedConfigRevision: number
): AccountUpdateDelta | undefined {
  return buildAccountUpdateDelta(
    { ...current.values, credentials: current.credentials },
    { ...baseline.values, credentials: baseline.credentials },
    expectedConfigRevision
  )
}

export function buildAccountAdvancedUpdatePatch(
  current: AccountSavePayload,
  baseline: AccountSavePayload,
  expectedConfigRevision: number,
  statusSelectionExplicit = false
): AccountUpdateDelta | undefined {
  return buildAccountUpdateDelta(
    buildAccountUpdatePayload(current, statusSelectionExplicit),
    buildAccountUpdatePayload(baseline),
    expectedConfigRevision
  )
}

function buildAccountUpdateDelta(
  current: Record<string, unknown>,
  baseline: Record<string, unknown>,
  expectedConfigRevision: number
): AccountUpdateDelta | undefined {
  if (!Number.isInteger(expectedConfigRevision) || expectedConfigRevision < 1) {
    throw new Error('账户配置版本无效，请刷新列表后重试')
  }
  const delta: Record<string, unknown> = {}
  for (const key of new Set([...Object.keys(current), ...Object.keys(baseline)])) {
    if (key === 'credentials') continue
    const currentHasKey = hasDefinedOwn(current, key)
    const baselineHasKey = hasDefinedOwn(baseline, key)
    const currentValue = currentHasKey ? current[key] : undefined
    const baselineValue = baselineHasKey ? baseline[key] : undefined
    if (sameValue(currentValue, baselineValue)) continue
    if (key === 'balanceQueryConfig' && !currentHasKey) continue
    delta[key] = currentHasKey ? currentValue : null
  }

  const credentialsPatch = diffCredentials(
    credentialRecord(current.credentials),
    credentialRecord(baseline.credentials)
  )
  if (Object.keys(credentialsPatch).length) delta.credentialsPatch = credentialsPatch
  if (!Object.keys(delta).length) return undefined
  return { ...delta, expectedConfigRevision }
}

function diffCredentials(
  current: Record<string, unknown>,
  baseline: Record<string, unknown>
): AccountCredentialsPatch {
  const patch: AccountCredentialsPatch = {}
  for (const key of new Set([...Object.keys(current), ...Object.keys(baseline)])) {
    const currentHasKey = hasDefinedOwn(current, key)
    const currentValue = currentHasKey ? current[key] : undefined
    const baselineValue = hasDefinedOwn(baseline, key) ? baseline[key] : undefined
    if (sameValue(currentValue, baselineValue)) continue
    patch[key] = currentHasKey ? currentValue : null
  }
  return patch
}

function buildBasicEditCredentials(
  form: AccountFormModel,
  currentCredentials: Record<string, unknown>
): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    supported_endpoint_modes: normalizedTextList(form.supportedEndpointModes).sort()
  }
  if (form.type === 'api_key') {
    // BUG-0238：占位行不代表用户输入，保存前剔除；权重随行同步过滤。
    const apiKeys = normalizedAccountApiKeys(form)
    const weights = normalizedAccountApiKeyWeights(form, apiKeys.length)
    const keptRows = apiKeys
      .map((key, index) => ({ key, weight: weights[index] }))
      .filter((row) => !isCredentialCipherPlaceholder(row.key))
    const keptKeys = keptRows.map((row) => row.key)
    credentials.base_url = form.baseUrl.trim()
    if (keptKeys.length) credentials.api_key = keptKeys[0]
    if (keptKeys.length > 1) {
      credentials.api_keys = keptKeys
      credentials.api_key_strategy = form.apiKeyStrategy
      if (credentials.api_key_strategy === 'weighted_round_robin') {
        credentials.api_key_weights = keptRows.map((row) => row.weight)
      }
    }
    return compactRecord(credentials)
  }

  credentials.base_url = form.baseUrl.trim() || credentialText(currentCredentials.base_url)
  if (form.type === 'oauth') {
    credentials.access_token = sensitiveCredentialText(form.accessToken, currentCredentials.access_token)
    credentials.refresh_token = sensitiveCredentialText(form.refreshToken, currentCredentials.refresh_token)
    return compactRecord(credentials)
  }

  credentials.access_token = sensitiveCredentialText(form.accessToken, currentCredentials.access_token)
  credentials.refresh_token = sensitiveCredentialText(form.refreshToken, currentCredentials.refresh_token)
  credentials.client_id = form.googleClientId.trim() || credentialText(currentCredentials.client_id)
  credentials.client_secret = sensitiveCredentialText(form.googleClientSecret, currentCredentials.client_secret)
  credentials.quota_project_id = form.googleQuotaProjectId.trim() || credentialText(currentCredentials.quota_project_id)
  credentials.oauth_type = form.oauthType
  credentials.tier_id = form.tierId.trim()
  credentials.project_id = form.projectId.trim() || credentialText(currentCredentials.project_id)
  return compactRecord(credentials)
}

/**
 * BUG-0238：敏感单键的基础编辑值。表单值为占位或为空时回退已存值，
 * 已存值（来自加密明细）同样为占位则整体省略该键（后端按省略保留现值）。
 */
function sensitiveCredentialText(formValue: string, storedValue: unknown): string | undefined {
  const formText = formValue.trim()
  if (formText && !isCredentialCipherPlaceholder(formText)) return formText
  const storedText = credentialText(storedValue)
  return storedText && !isCredentialCipherPlaceholder(storedText) ? storedText : undefined
}

function compactRecord(input: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(input)) {
    if (value === undefined || value === '') continue
    output[key] = value
  }
  return output
}

function credentialRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {}
}

function credentialText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizedTextList(values: readonly string[] | undefined): string[] {
  return [...new Set((values ?? []).map((value) => value.trim()).filter(Boolean))]
}

function hasDefinedOwn(value: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key) && value[key] !== undefined
}

function sameValue(left: unknown, right: unknown): boolean {
  return stableValue(left) === stableValue(right)
}

function stableValue(value: unknown): string {
  return JSON.stringify(sortValue(value))
}

function sortValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortValue)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(
    Object.entries(value as Record<string, unknown>)
      .filter(([, item]) => item !== undefined)
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([key, item]) => [key, sortValue(item)])
  )
}

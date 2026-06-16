import type { AccountDraftTestAccountPayload } from '@/api/client'
import type { AccountSummary, ProviderModelPricing } from '@/types/domain'
import { asString } from './accountBasicFormatters'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountSavePayload } from './accountSavePayload'
import type { SuccessfulDraftActivationTest } from './useAccountTestModal'

export interface AccountModelSelectOption {
  label: string
  value: string
}

export function providerModelsToOptions(models: ProviderModelPricing[]): AccountModelSelectOption[] {
  return models.map((item) => ({
    label: item.model,
    value: item.model
  }))
}

export function cloneAccountName(name: string): string {
  const trimmed = name.trim()
  return trimmed ? `${trimmed} - 克隆` : ''
}

export function cloneAccountModelMappings(value: AccountSummary['modelMappings']): AccountFormModel['modelMappings'] {
  return (value ?? []).map((item) => ({ ...item }))
}

export function accountTagNames(value: AccountSummary['tags']): string[] {
  return (value ?? []).map((tag) => tag.name).filter(Boolean)
}

export function sameTagNames(left: string[], right: AccountSummary['tags']): boolean {
  return stableTagKey(normalizeFormTagNames(left)) === stableTagKey(accountTagNames(right))
}

export function normalizeFormTagNames(value: string[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of value ?? []) {
    const name = item.replace(/\s+/g, ' ').trim()
    if (!name) continue
    const key = name.toLocaleLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    output.push(name)
  }
  return output
}

function stableTagKey(value: string[]): string {
  return normalizeFormTagNames(value).map((item) => item.toLocaleLowerCase()).sort().join('\n')
}

export function accountApiKeysForForm(credentials: Record<string, unknown>): string[] {
  const values = Array.isArray(credentials.api_keys)
    ? credentials.api_keys
    : [credentials.api_key]
  const keys = values.map((value) => asString(value) ?? '').filter(Boolean)
  return keys.length ? keys : ['']
}

export function accountApiKeyWeightsForForm(credentials: Record<string, unknown>): number[] {
  const keys = accountApiKeysForForm(credentials)
  const rawWeights = Array.isArray(credentials.api_key_weights) ? credentials.api_key_weights : []
  return keys.map((_, index) => {
    const value = Number(rawWeights[index] ?? 1)
    return Number.isInteger(value) ? Math.min(100, Math.max(1, value)) : 1
  })
}

export function accountCreatePayloadWithActivationTest(
  payload: AccountSavePayload,
  activationTest: SuccessfulDraftActivationTest | undefined,
  fallbackName: string
): AccountSavePayload & { status?: 'active'; activationTestTaskId?: string } {
  if (!activationTest || !isActivationTestForPayload(activationTest, payload, fallbackName)) {
    return payload
  }
  return {
    ...payload,
    status: 'active',
    activationTestTaskId: activationTest.taskId
  }
}

function isActivationTestForPayload(
  activationTest: SuccessfulDraftActivationTest,
  payload: AccountSavePayload,
  fallbackName: string
): boolean {
  return stablePayloadFingerprint(activationTest.account) === stablePayloadFingerprint(accountDraftPayloadFromSavePayload(payload, fallbackName))
}

function accountDraftPayloadFromSavePayload(
  payload: AccountSavePayload,
  fallbackName: string
): AccountDraftTestAccountPayload {
  return {
    providerCode: payload.providerCode,
    providerProtocolProfileId: payload.providerProtocolProfileId,
    name: payload.name ?? fallbackName,
    type: payload.type,
    credentials: payload.credentials,
    concurrencyLimit: payload.concurrencyLimit,
    priority: payload.priority,
    clientCompatibility: payload.clientCompatibility,
    supportedModels: payload.supportedModels,
    modelMappings: payload.modelMappings,
    proxyProfileId: payload.proxyProfileId,
    groupId: payload.groupId ?? '',
    accountExpiresAt: payload.accountExpiresAt,
    availabilitySchedule: payload.availabilitySchedule as AccountDraftTestAccountPayload['availabilitySchedule'],
    notes: payload.notes
  }
}

function stablePayloadFingerprint(value: unknown): string {
  return JSON.stringify(stablePayloadValue(value))
}

function stablePayloadValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stablePayloadValue)
  if (!value || typeof value !== 'object') return value
  const output: Record<string, unknown> = {}
  for (const key of Object.keys(value).sort()) {
    const item = (value as Record<string, unknown>)[key]
    if (item !== undefined) {
      output[key] = stablePayloadValue(item)
    }
  }
  return output
}

import type { AccountDraftTestAccountPayload } from '@/api/client'
import type { AccountApiKeyRuntimeDetail, AccountTestResult } from '@/types/domain'

export interface DraftApiKeyTestSnapshot {
  account: AccountDraftTestAccountPayload
  result: AccountTestResult
}

export function draftApiKeyTestRuntimeDetailsForPayload(
  snapshot: DraftApiKeyTestSnapshot | undefined,
  currentPayload: AccountDraftTestAccountPayload | undefined
): AccountApiKeyRuntimeDetail[] | undefined {
  if (!snapshot || !currentPayload) return undefined
  if (currentPayload.type !== 'api_key' || snapshot.account.type !== 'api_key') return undefined
  if (!sameDraftApiKeyTestTarget(snapshot.account, currentPayload)) return undefined

  const keys = draftPayloadApiKeys(currentPayload)
  if (keys.length <= 1) return undefined
  const weights = draftPayloadApiKeyWeights(currentPayload, keys.length)
  const poolItems = snapshot.result.apiKeyPool?.results
  if (poolItems?.length) {
    const details = poolItems
      .map((item) => accountApiKeyRuntimeDetailFromPoolItem({
        item,
        key: keys[item.keyIndex],
        weight: weights[item.keyIndex]
      }))
      .filter((item): item is AccountApiKeyRuntimeDetail => Boolean(item))
    return details.length ? details : undefined
  }

  const fallbackIndexes = snapshot.result.success
    ? [keys.findIndex((key) => Boolean(key.trim()))]
    : keys.map((key, index) => key.trim() ? index : -1)
  const details = fallbackIndexes
    .filter((index) => index >= 0)
    .map((index) => accountApiKeyRuntimeDetailFromResult({
      key: keys[index],
      keyIndex: index,
      result: snapshot.result,
      weight: weights[index]
    }))
  return details.length ? details : undefined
}

function accountApiKeyRuntimeDetailFromPoolItem(input: {
  item: NonNullable<AccountTestResult['apiKeyPool']>['results'][number]
  key: string | undefined
  weight: number | undefined
}): AccountApiKeyRuntimeDetail | undefined {
  if (!input.key?.trim() && !input.item.keyPrefix && !input.item.keySuffix) return undefined
  return {
    keyIndex: input.item.keyIndex,
    keyFingerprintPrefix: input.item.keyPrefix ?? '',
    keySuffix: input.item.keySuffix ?? keySuffixForDisplay(input.key),
    weight: normalizeWeight(input.weight),
    status: input.item.success ? 'active' : 'temporary_unavailable',
    failureCount: input.item.success ? 0 : 1,
    consecutiveFailures: input.item.success ? 0 : 1,
    successCount: input.item.success ? 1 : 0,
    lastErrorCode: input.item.success ? undefined : input.item.errorCode,
    lastErrorMessage: input.item.success ? undefined : input.item.message
  }
}

function accountApiKeyRuntimeDetailFromResult(input: {
  key: string | undefined
  keyIndex: number
  result: AccountTestResult
  weight: number | undefined
}): AccountApiKeyRuntimeDetail {
  return {
    keyIndex: input.keyIndex,
    keyFingerprintPrefix: keyPrefixForDisplay(input.key),
    keySuffix: keySuffixForDisplay(input.key),
    weight: normalizeWeight(input.weight),
    status: input.result.success ? 'active' : 'temporary_unavailable',
    failureCount: input.result.success ? 0 : 1,
    consecutiveFailures: input.result.success ? 0 : 1,
    successCount: input.result.success ? 1 : 0,
    lastErrorCode: input.result.success ? undefined : input.result.errorCode,
    lastErrorMessage: input.result.success ? undefined : input.result.message
  }
}

function sameDraftApiKeyTestTarget(left: AccountDraftTestAccountPayload, right: AccountDraftTestAccountPayload): boolean {
  return stablePayloadFingerprint(draftApiKeyTestTarget(left)) === stablePayloadFingerprint(draftApiKeyTestTarget(right))
}

function draftApiKeyTestTarget(payload: AccountDraftTestAccountPayload): Record<string, unknown> {
  return {
    providerCode: payload.providerCode,
    providerProtocolProfileId: payload.providerProtocolProfileId,
    type: payload.type,
    groupId: payload.groupId,
    proxyProfileId: payload.proxyProfileId,
    credentials: payload.credentials
  }
}

function draftPayloadApiKeys(payload: AccountDraftTestAccountPayload): string[] {
  const credentials = payload.credentials ?? {}
  const rawKeys = Array.isArray(credentials.api_keys) && credentials.api_keys.length
    ? credentials.api_keys
    : [credentials.api_key]
  return rawKeys.map((value) => typeof value === 'string' ? value.trim() : '')
}

function draftPayloadApiKeyWeights(payload: AccountDraftTestAccountPayload, count: number): number[] {
  const rawWeights = Array.isArray(payload.credentials?.api_key_weights) ? payload.credentials.api_key_weights : []
  return Array.from({ length: count }, (_, index) => normalizeWeight(rawWeights[index]))
}

function normalizeWeight(value: unknown): number {
  const numberValue = Number(value ?? 1)
  if (!Number.isInteger(numberValue)) return 1
  return Math.min(100, Math.max(1, numberValue))
}

function keyPrefixForDisplay(key: string | undefined): string {
  const text = key?.trim() ?? ''
  return text ? text.slice(0, 4) : ''
}

function keySuffixForDisplay(key: string | undefined): string | undefined {
  const text = key?.trim() ?? ''
  return text ? text.slice(-4) : undefined
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

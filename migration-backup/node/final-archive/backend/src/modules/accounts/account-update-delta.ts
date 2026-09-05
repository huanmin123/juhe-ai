import { isDeepStrictEqual } from 'node:util'

import type { AccountSummary } from '../../domain/types.js'

const directAccountFields = [
  'name',
  'credentials',
  'healthCheckModel',
  'healthCheckEndpointMode',
  'status',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'schedulable',
  'availabilitySchedule',
  'accountExpiresAt',
  'temporaryUnavailableContinuousProbeEnabled',
  'notes'
] as const

export interface AccountUpdateDelta {
  input: Record<string, unknown>
  changedFields: string[]
}

export function actualAccountUpdateDelta(
  current: AccountSummary,
  requested: Record<string, unknown>,
  currentBalance?: { enabled: boolean; config?: unknown }
): AccountUpdateDelta {
  const input: Record<string, unknown> = {}
  const changedFields: string[] = []

  for (const field of directAccountFields) {
    if (!hasOwn(requested, field)) continue
    const requestedValue = normalizeDirectValue(field, requested[field])
    const currentValue = normalizeDirectValue(field, current[field])
    if (isDeepStrictEqual(requestedValue, currentValue)) continue
    input[field] = requested[field]
    changedFields.push(field)
  }

  if (hasOwn(requested, 'proxyProfileId')) {
    const requestedProxyProfileId = nullableText(requested.proxyProfileId)
    const currentProxyProfileId = nullableText(current.proxyProfileId)
    if (requestedProxyProfileId !== currentProxyProfileId) {
      input.proxyProfileId = requested.proxyProfileId
      changedFields.push('proxyProfileId')
    }
  }

  if (hasOwn(requested, 'supportedModels')) {
    const nextModels = normalizedStringSet(requested.supportedModels)
    const currentModels = normalizedStringSet(current.supportedModels)
    if (!isDeepStrictEqual(nextModels, currentModels)) {
      input.supportedModels = requested.supportedModels
      changedFields.push('supportedModels')
    }
  }

  if (hasOwn(requested, 'modelMappings')) {
    const nextMappings = normalizedModelMappings(requested.modelMappings)
    const currentMappings = normalizedModelMappings(current.modelMappings)
    if (!isDeepStrictEqual(nextMappings, currentMappings)) {
      input.modelMappings = requested.modelMappings
      changedFields.push('modelMappings')
    }
  }

  if (hasOwn(requested, 'tags')) {
    const nextTags = normalizedStringSet(requested.tags)
    const currentTags = normalizedStringSet((current.tags ?? []).map((item) => item.name))
    if (!isDeepStrictEqual(nextTags, currentTags)) {
      input.tags = requested.tags
      changedFields.push('tags')
    }
  }

  if (hasOwn(requested, 'balanceQueryEnabled')) {
    const nextEnabled = requested.balanceQueryEnabled === true
    if (nextEnabled !== (currentBalance?.enabled === true)) {
      input.balanceQueryEnabled = nextEnabled
      changedFields.push('balanceQueryEnabled')
    }
  }
  if (hasOwn(requested, 'balanceQueryConfig') && !isDeepStrictEqual(requested.balanceQueryConfig, currentBalance?.config)) {
    input.balanceQueryConfig = requested.balanceQueryConfig
    changedFields.push('balanceQueryConfig')
  }

  return { input, changedFields }
}

export function changedCredentialPatchFields(
  current: Record<string, unknown>,
  patch: Record<string, unknown>
): string[] {
  const output: string[] = []
  for (const [key, value] of Object.entries(patch)) {
    const currentHasKey = hasOwn(current, key)
    if (value === null) {
      if (currentHasKey) output.push(`credentials.${key}`)
      continue
    }
    if (!currentHasKey || !isDeepStrictEqual(current[key], value)) {
      output.push(`credentials.${key}`)
    }
  }
  return output.sort()
}

function normalizeDirectValue(field: typeof directAccountFields[number], value: unknown): unknown {
  if (field === 'notes') return nullableText(value)
  if (field === 'accountExpiresAt') return nullableText(value)
  return value
}

function normalizedStringSet(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value
    .map((item) => typeof item === 'string' ? item.trim() : '')
    .filter(Boolean))]
    .sort()
}

function normalizedModelMappings(value: unknown): unknown[] {
  if (!Array.isArray(value)) return []
  return value.map((item) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return item
    const mapping = item as Record<string, unknown>
    return {
      sourceModel: mapping.sourceModel,
      sourceEndpointFamily: mapping.sourceEndpointFamily,
      upstreamModel: mapping.upstreamModel,
      upstreamEndpointFamily: mapping.upstreamEndpointFamily,
      enabled: mapping.enabled !== false
    }
  })
}

function nullableText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function hasOwn(value: object, key: PropertyKey): boolean {
  return Object.prototype.hasOwnProperty.call(value, key)
}

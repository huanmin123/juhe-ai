import { createHmac } from 'node:crypto'

import { runtimeConfig } from '../config/runtime.js'
import {
  ANTHROPIC_PROVIDER_CODE,
  isAnthropicProtocolProfile,
  isDeepSeekProviderCode,
  isGlmProviderCode,
  isOpenAICompatibleProviderCode,
  normalizeProviderToken
} from '../domain/provider-protocol.js'

export type AccountApiKeyStrategy = 'round_robin' | 'weighted_round_robin'
export type AccountApiKeyRuntimeStatus = 'active' | 'temporary_unavailable' | 'rate_limited' | 'error' | 'disabled'

export interface AccountApiKeyEntry {
  id: string
  key: string
  fingerprint: string
  index: number
  weight: number
}

export interface AccountApiKeyRuntimeSelectionState {
  keyFingerprint: string
  status: AccountApiKeyRuntimeStatus
  keyIndex?: number
  cooldownUntil?: string
  nextProbeAt?: string
}

interface RoundRobinState {
  nextIndex: number
}

interface WeightedState {
  currentWeights: Map<string, number>
}

const roundRobinStates = new Map<string, RoundRobinState>()
const weightedStates = new Map<string, WeightedState>()

export function selectAccountRuntimeApiKey(input: {
  accountId: string
  credentials: Record<string, unknown>
  runtimeStates?: AccountApiKeyRuntimeSelectionState[]
  excludeFingerprints?: Iterable<string>
}): string | undefined {
  return selectAccountRuntimeApiKeyEntry(input)?.key
}

export function selectAccountRuntimeApiKeyEntry(input: {
  accountId: string
  credentials: Record<string, unknown>
  runtimeStates?: AccountApiKeyRuntimeSelectionState[]
  excludeFingerprints?: Iterable<string>
}): AccountApiKeyEntry | undefined {
  const entries = accountApiKeyEntries(input.credentials)
  if (!entries.length) return undefined
  const excludedFingerprints = new Set([...(input.excludeFingerprints ?? [])].map((value) => value.trim()).filter(Boolean))
  const candidateEntries = excludedFingerprints.size
    ? entries.filter((entry) => !excludedFingerprints.has(entry.fingerprint))
    : entries
  if (!candidateEntries.length) return undefined
  if (entries.length === 1) return candidateEntries[0]
  const availableEntries = accountApiKeyEntriesAvailableForDispatch(candidateEntries, input.runtimeStates)
  if (!availableEntries.length) return undefined
  const strategy = accountApiKeyStrategy(input.credentials)
  return strategy === 'weighted_round_robin'
    ? selectWeightedApiKey(input.accountId, availableEntries)
    : selectRoundRobinApiKey(input.accountId, availableEntries)
}

export function accountApiKeyEntries(credentials: Record<string, unknown>): AccountApiKeyEntry[] {
  const rawKeys = Array.isArray(credentials.api_keys) && credentials.api_keys.length
    ? credentials.api_keys
    : [credentials.api_key]
  const weights = Array.isArray(credentials.api_key_weights) ? credentials.api_key_weights : []
  const entries: AccountApiKeyEntry[] = []
  const seen = new Set<string>()
  for (const [index, value] of rawKeys.entries()) {
    if (typeof value !== 'string') continue
    const key = value.trim()
    if (!key || seen.has(key)) continue
    seen.add(key)
    const fingerprint = fingerprintAccountApiKey(key)
    entries.push({
      id: fingerprint,
      key,
      fingerprint,
      index,
      weight: normalizeApiKeyWeight(weights[index])
    })
  }
  return entries
}

export function isAccountApiKeyPoolIsolationEnabled(input: {
  providerCode?: unknown
  protocolCode?: unknown
  protocolVersion?: unknown
  type?: unknown
  credentials?: Record<string, unknown>
  apiKeys?: string[]
}): boolean {
  const keyCount = input.credentials
    ? accountApiKeyEntries(input.credentials).length
    : Array.isArray(input.apiKeys) ? new Set(input.apiKeys.map((key) => key.trim()).filter(Boolean)).size : 0
  return input.type === 'api_key'
    && isAccountApiKeyPoolProviderSupported(input)
    && keyCount > 1
}

function isAccountApiKeyPoolProviderSupported(input: {
  providerCode?: unknown
  protocolCode?: unknown
  protocolVersion?: unknown
}): boolean {
  return isOpenAICompatibleProviderCode(input.providerCode)
    || isDeepSeekProviderCode(input.providerCode)
    || isGlmProviderCode(input.providerCode)
    || isAnthropicProtocolProfile({
      protocolCode: normalizeProviderToken(input.protocolCode),
      protocolVersion: normalizeProviderToken(input.protocolVersion)
    })
    || normalizeProviderToken(input.providerCode) === ANTHROPIC_PROVIDER_CODE
}

export function fingerprintAccountApiKey(key: string): string {
  return createHmac('sha256', runtimeConfig.secret).update(key).digest('hex')
}

function accountApiKeyStrategy(credentials: Record<string, unknown>): AccountApiKeyStrategy {
  return credentials.api_key_strategy === 'weighted_round_robin' ? 'weighted_round_robin' : 'round_robin'
}

function selectRoundRobinApiKey(accountId: string, entries: AccountApiKeyEntry[]): AccountApiKeyEntry {
  const state = roundRobinStates.get(accountId) ?? { nextIndex: 0 }
  const index = state.nextIndex % entries.length
  roundRobinStates.set(accountId, { nextIndex: (index + 1) % entries.length })
  return entries[index]
}

function selectWeightedApiKey(accountId: string, entries: AccountApiKeyEntry[]): AccountApiKeyEntry {
  const state = weightedStates.get(accountId) ?? { currentWeights: new Map<string, number>() }
  cleanupWeightedState(state, entries)
  const totalWeight = entries.reduce((sum, entry) => sum + entry.weight, 0)
  let selected = entries[0]
  let selectedCurrentWeight = Number.NEGATIVE_INFINITY
  for (const entry of entries) {
    const current = (state.currentWeights.get(entry.id) ?? 0) + entry.weight
    state.currentWeights.set(entry.id, current)
    if (current > selectedCurrentWeight || (current === selectedCurrentWeight && entry.id.localeCompare(selected.id) < 0)) {
      selected = entry
      selectedCurrentWeight = current
    }
  }
  state.currentWeights.set(selected.id, (state.currentWeights.get(selected.id) ?? 0) - totalWeight)
  weightedStates.set(accountId, state)
  return selected
}

function cleanupWeightedState(state: WeightedState, entries: AccountApiKeyEntry[]): void {
  const activeIds = new Set(entries.map((entry) => entry.id))
  for (const id of state.currentWeights.keys()) {
    if (!activeIds.has(id)) {
      state.currentWeights.delete(id)
    }
  }
}

function normalizeApiKeyWeight(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 100 ? value : 1
}

function accountApiKeyEntriesAvailableForDispatch(
  entries: AccountApiKeyEntry[],
  runtimeStates: AccountApiKeyRuntimeSelectionState[] | undefined
): AccountApiKeyEntry[] {
  if (!runtimeStates?.length) return entries
  const unavailableFingerprints = new Set(
    runtimeStates
      .filter((state) => state.status !== 'active')
      .map((state) => state.keyFingerprint)
      .filter(Boolean)
  )
  if (!unavailableFingerprints.size) return entries
  return entries.filter((entry) => !unavailableFingerprints.has(entry.fingerprint))
}

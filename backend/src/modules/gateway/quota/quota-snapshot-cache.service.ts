import type { RequestQuotaCosts } from './request-quota-checker.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { registerAuthorizationQuotaCacheInvalidator } from '../../../shared/gateway-cache-invalidation.js'
import { logger } from '../../../shared/logger.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../../shared/rfc3339.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../../shared/runtime-state-store.js'

export interface GatewayQuotaDecision {
  allowed: boolean
  message?: string
}

export interface GatewayQuotaCostSnapshotEntry {
  systemAccountId: string
  scopeType: string
  scopeId: string
  hourlyWindowHours?: number
  costs: RequestQuotaCosts
}

export interface GatewayAuthorizationQuotaSnapshotEntry {
  scopeType: 'account_authorization' | 'group_authorization'
  authorizationId: string
  decision: GatewayQuotaDecision
}

export interface GatewayQuotaSnapshot {
  generatedAt: string
  costEntries: GatewayQuotaCostSnapshotEntry[]
  authorizationEntries: GatewayAuthorizationQuotaSnapshotEntry[]
  costEntriesComplete?: boolean
  authorizationEntriesComplete?: boolean
  timezone?: string
  statDate?: string
  statWeek?: string
  statMonth?: string
}

export const gatewayQuotaSnapshotCostPageSize = 5000
export const gatewayQuotaSnapshotAuthorizationPageSize = 5000
export const maxGatewayQuotaSnapshotCostEntries = gatewayQuotaSnapshotCostPageSize
export const maxGatewayQuotaSnapshotAuthorizationEntries = gatewayQuotaSnapshotAuthorizationPageSize
export const gatewayQuotaSnapshotRuntimeStateStoreName = 'gateway_quota_snapshot'
export const gatewayQuotaSnapshotRuntimeStateKey = 'current'

let snapshotGeneratedAt: string | undefined
let costSnapshotComplete = false
let authorizationSnapshotComplete = false
let authorizationSnapshotInvalidated = false
let authorizationSnapshotInvalidatedAtMs = 0
let authorizationSnapshotVersion = 0
const costSnapshot = new Map<string, RequestQuotaCosts>()
const authorizationSnapshot = new Map<string, GatewayQuotaDecision>()
let runtimeStateStore: RuntimeStateStore | undefined
let runtimeStateStoreDriver: string | undefined
let sharedSnapshot: GatewayQuotaSnapshot | undefined
let sharedSnapshotCostEntries = new Map<string, RequestQuotaCosts>()
let sharedSnapshotAuthorizationEntries = new Map<string, GatewayQuotaDecision>()
let sharedSnapshotFetchedAtMs = 0
let sharedSnapshotLoadPromise: Promise<GatewayQuotaSnapshot | undefined> | undefined
const sharedSnapshotMemoTtlMs = 1000

export function replaceGatewayQuotaSnapshot(snapshot: GatewayQuotaSnapshot): void {
  const generatedAt = requiredRfc3339Instant(snapshot.generatedAt, '网关额度快照 generatedAt')
  if (runtimeConfig.cacheDriver === 'redis') {
    clearGatewayQuotaSnapshot()
    return
  }
  snapshotGeneratedAt = generatedAt
  costSnapshotComplete = snapshot.costEntriesComplete ?? true
  authorizationSnapshotComplete = snapshot.authorizationEntriesComplete ?? true
  authorizationSnapshotInvalidated = false
  authorizationSnapshotVersion += 1
  costSnapshot.clear()
  authorizationSnapshot.clear()
  for (const entry of snapshot.costEntries) {
    costSnapshot.set(costSnapshotKey(entry), cloneRequestQuotaCosts(entry.costs))
  }
  for (const entry of snapshot.authorizationEntries) {
    authorizationSnapshot.set(authorizationSnapshotKey(entry.scopeType, entry.authorizationId), { ...entry.decision })
  }
  if (!costSnapshotComplete || !authorizationSnapshotComplete) {
    logger.warn({
      event: 'gateway_quota_snapshot_incomplete',
      generatedAt,
      costEntryCount: snapshot.costEntries.length,
      authorizationEntryCount: snapshot.authorizationEntries.length,
      costEntriesComplete: costSnapshotComplete,
      authorizationEntriesComplete: authorizationSnapshotComplete,
      maxCostEntries: maxGatewayQuotaSnapshotCostEntries,
      maxAuthorizationEntries: maxGatewayQuotaSnapshotAuthorizationEntries
    }, '网关配额快照不完整，运行时将对缺失 scope 通过 DB service 精确补判')
  }
}

export function clearGatewayQuotaSnapshot(): void {
  snapshotGeneratedAt = undefined
  costSnapshotComplete = false
  authorizationSnapshotComplete = false
  authorizationSnapshotInvalidated = false
  authorizationSnapshotInvalidatedAtMs = 0
  authorizationSnapshotVersion += 1
  costSnapshot.clear()
  authorizationSnapshot.clear()
  clearSharedGatewayQuotaSnapshotMemo()
}

export function invalidateGatewayAuthorizationQuotaSnapshot(metadata: { publishedAt?: string } = {}): void {
  const publishedAt = metadata.publishedAt === undefined
    ? undefined
    : requiredRfc3339Instant(metadata.publishedAt, '网关额度快照授权失效 publishedAt')
  const publishedAtMs = publishedAt === undefined
    ? Date.now()
    : rfc3339InstantMilliseconds(publishedAt)
  if (publishedAtMs === undefined) {
    throw new Error('网关额度快照授权失效 publishedAt 规范化后无效')
  }
  authorizationSnapshotInvalidated = true
  authorizationSnapshotInvalidatedAtMs = Math.max(
    authorizationSnapshotInvalidatedAtMs,
    publishedAtMs
  )
  authorizationSnapshotComplete = false
  authorizationSnapshotVersion += 1
  authorizationSnapshot.clear()
  clearSharedGatewayQuotaSnapshotMemo()
}

export function readGatewayQuotaCostsSnapshot(input: {
  systemAccountId: string
  scopeType: string
  scopeId: string
  hourlyWindowHours?: number
}): RequestQuotaCosts | undefined {
  if (runtimeConfig.cacheDriver === 'redis') return undefined
  const costs = costSnapshot.get(costSnapshotKey(input))
  return costs ? cloneRequestQuotaCosts(costs) : undefined
}

export async function readGatewayQuotaCostsSnapshotAsync(input: {
  systemAccountId: string
  scopeType: string
  scopeId: string
  hourlyWindowHours?: number
}): Promise<RequestQuotaCosts | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return readGatewayQuotaCostsSnapshot(input)
  const snapshot = await readSharedGatewayQuotaSnapshot()
  if (!snapshot) return undefined
  const costs = sharedSnapshotCostEntries.get(costSnapshotKey(input))
  return costs ? cloneRequestQuotaCosts(costs) : undefined
}

export function readGatewayAuthorizationQuotaSnapshot(
  scopeType: 'account_authorization' | 'group_authorization',
  authorizationId?: string
): GatewayQuotaDecision | undefined {
  if (runtimeConfig.cacheDriver === 'redis') return undefined
  if (!authorizationId) return undefined
  const decision = authorizationSnapshot.get(authorizationSnapshotKey(scopeType, authorizationId))
  return decision ? { ...decision } : undefined
}

export async function readGatewayAuthorizationQuotaSnapshotAsync(
  scopeType: 'account_authorization' | 'group_authorization',
  authorizationId?: string
): Promise<GatewayQuotaDecision | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return readGatewayAuthorizationQuotaSnapshot(scopeType, authorizationId)
  if (!authorizationId) return undefined
  const snapshot = await readSharedGatewayQuotaSnapshot()
  if (!snapshot || !sharedSnapshotAuthorizationUsable(snapshot)) return undefined
  const decision = sharedSnapshotAuthorizationEntries.get(authorizationSnapshotKey(scopeType, authorizationId))
  return decision ? { ...decision } : undefined
}

export function gatewayQuotaSnapshotRuntime(): {
  generatedAt?: string
  costEntryCount: number
  authorizationEntryCount: number
  costEntriesComplete: boolean
  authorizationEntriesComplete: boolean
} {
  if (runtimeConfig.cacheDriver === 'redis') {
    return {
      generatedAt: sharedSnapshot?.generatedAt,
      costEntryCount: sharedSnapshotCostEntries.size,
      authorizationEntryCount: sharedSnapshotAuthorizationEntries.size,
      costEntriesComplete: sharedSnapshot?.costEntriesComplete ?? false,
      authorizationEntriesComplete: sharedSnapshot ? sharedSnapshotAuthorizationComplete(sharedSnapshot) : false
    }
  }
  return {
    generatedAt: snapshotGeneratedAt,
    costEntryCount: costSnapshot.size,
    authorizationEntryCount: authorizationSnapshot.size,
    costEntriesComplete: costSnapshotComplete,
    authorizationEntriesComplete: authorizationSnapshotComplete
  }
}

export function isGatewayQuotaCostSnapshotComplete(): boolean {
  if (runtimeConfig.cacheDriver === 'redis') return false
  return snapshotGeneratedAt !== undefined && costSnapshotComplete
}

export async function isGatewayQuotaCostSnapshotCompleteAsync(): Promise<boolean> {
  if (runtimeConfig.cacheDriver !== 'redis') return isGatewayQuotaCostSnapshotComplete()
  const snapshot = await readSharedGatewayQuotaSnapshot()
  return Boolean(snapshot && (snapshot.costEntriesComplete ?? true))
}

export function isGatewayAuthorizationSnapshotComplete(): boolean {
  if (runtimeConfig.cacheDriver === 'redis') return false
  return snapshotGeneratedAt !== undefined && authorizationSnapshotComplete && !authorizationSnapshotInvalidated
}

export async function isGatewayAuthorizationSnapshotCompleteAsync(): Promise<boolean> {
  if (runtimeConfig.cacheDriver !== 'redis') return isGatewayAuthorizationSnapshotComplete()
  const snapshot = await readSharedGatewayQuotaSnapshot()
  return Boolean(snapshot && sharedSnapshotAuthorizationComplete(snapshot))
}

export function isGatewayQuotaCostSnapshotIncomplete(): boolean {
  if (runtimeConfig.cacheDriver === 'redis') return true
  return snapshotGeneratedAt !== undefined && !costSnapshotComplete
}

export async function isGatewayQuotaCostSnapshotIncompleteAsync(): Promise<boolean> {
  if (runtimeConfig.cacheDriver !== 'redis') return isGatewayQuotaCostSnapshotIncomplete()
  const snapshot = await readSharedGatewayQuotaSnapshot()
  return Boolean(snapshot && !(snapshot.costEntriesComplete ?? true))
}

export function isGatewayAuthorizationSnapshotIncomplete(): boolean {
  if (runtimeConfig.cacheDriver === 'redis') return true
  return authorizationSnapshotInvalidated || (snapshotGeneratedAt !== undefined && !authorizationSnapshotComplete)
}

export async function isGatewayAuthorizationSnapshotIncompleteAsync(): Promise<boolean> {
  if (runtimeConfig.cacheDriver !== 'redis') return isGatewayAuthorizationSnapshotIncomplete()
  const snapshot = await readSharedGatewayQuotaSnapshot()
  if (!snapshot) return authorizationSnapshotInvalidated
  return !sharedSnapshotAuthorizationComplete(snapshot)
}

export async function hasGatewayQuotaSnapshotAsync(): Promise<boolean> {
  if (runtimeConfig.cacheDriver !== 'redis') return snapshotGeneratedAt !== undefined
  return Boolean(await readSharedGatewayQuotaSnapshot())
}

export function gatewayAuthorizationQuotaSnapshotVersion(): number {
  return authorizationSnapshotVersion
}

async function readSharedGatewayQuotaSnapshot(): Promise<GatewayQuotaSnapshot | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis' || runtimeConfig.runtimeStateDriver !== 'redis') {
    return undefined
  }
  const now = Date.now()
  if (sharedSnapshotFetchedAtMs > 0 && now - sharedSnapshotFetchedAtMs < sharedSnapshotMemoTtlMs) {
    return sharedSnapshot
  }
  if (!sharedSnapshotLoadPromise) {
    sharedSnapshotLoadPromise = runtimeState().getJson<GatewayQuotaSnapshot>(gatewayQuotaSnapshotRuntimeStateKey)
      .then(
        (snapshot) => {
          try {
            replaceSharedGatewayQuotaSnapshotMemo(snapshot)
            return sharedSnapshot
          } catch (error) {
            clearSharedGatewayQuotaSnapshotMemo()
            throw error
          }
        },
        (error) => {
          logger.warn({
            event: 'gateway_quota_snapshot_runtime_state_read_failed',
            error
          }, '读取 Redis runtime state 网关配额快照失败，将回退到 DB service 精确补判')
          clearSharedGatewayQuotaSnapshotMemo()
          sharedSnapshotFetchedAtMs = Date.now()
          return undefined
        }
      )
      .finally(() => {
        sharedSnapshotLoadPromise = undefined
      })
  }
  return await sharedSnapshotLoadPromise
}

function runtimeState(): RuntimeStateStore {
  if (!runtimeStateStore || runtimeStateStoreDriver !== runtimeConfig.runtimeStateDriver) {
    runtimeStateStore = createRuntimeStateStore('gateway_quota_snapshot')
    runtimeStateStoreDriver = runtimeConfig.runtimeStateDriver
  }
  return runtimeStateStore
}

function replaceSharedGatewayQuotaSnapshotMemo(snapshot: GatewayQuotaSnapshot | undefined): void {
  sharedSnapshotFetchedAtMs = Date.now()
  if (snapshot === undefined) {
    sharedSnapshot = undefined
    sharedSnapshotCostEntries = new Map()
    sharedSnapshotAuthorizationEntries = new Map()
    return
  }
  const normalizedSnapshot: GatewayQuotaSnapshot = {
    ...snapshot,
    generatedAt: requiredRfc3339Instant(snapshot.generatedAt, 'Redis runtime state 网关额度快照 generatedAt')
  }
  sharedSnapshot = normalizedSnapshot
  sharedSnapshotCostEntries = new Map(normalizedSnapshot.costEntries.map((entry) => [
    costSnapshotKey(entry),
    cloneRequestQuotaCosts(entry.costs)
  ]))
  sharedSnapshotAuthorizationEntries = new Map(normalizedSnapshot.authorizationEntries.map((entry) => [
    authorizationSnapshotKey(entry.scopeType, entry.authorizationId),
    { ...entry.decision }
  ]))
  if (authorizationSnapshotInvalidated && sharedSnapshotAuthorizationUsable(normalizedSnapshot)) {
    authorizationSnapshotInvalidated = false
    authorizationSnapshotInvalidatedAtMs = 0
    authorizationSnapshotVersion += 1
  }
  if (!(normalizedSnapshot.costEntriesComplete ?? true) || !sharedSnapshotAuthorizationComplete(normalizedSnapshot)) {
    logger.warn({
      event: 'gateway_quota_snapshot_runtime_state_incomplete',
      generatedAt: normalizedSnapshot.generatedAt,
      costEntryCount: normalizedSnapshot.costEntries.length,
      authorizationEntryCount: normalizedSnapshot.authorizationEntries.length,
      costEntriesComplete: normalizedSnapshot.costEntriesComplete ?? true,
      authorizationEntriesComplete: sharedSnapshotAuthorizationComplete(normalizedSnapshot),
      maxCostEntries: maxGatewayQuotaSnapshotCostEntries,
      maxAuthorizationEntries: maxGatewayQuotaSnapshotAuthorizationEntries
    }, 'Redis runtime state 网关配额快照不完整，运行时将对缺失 scope 通过 DB service 精确补判')
  }
}

function clearSharedGatewayQuotaSnapshotMemo(): void {
  sharedSnapshot = undefined
  sharedSnapshotCostEntries = new Map()
  sharedSnapshotAuthorizationEntries = new Map()
  sharedSnapshotFetchedAtMs = 0
  sharedSnapshotLoadPromise = undefined
}

function sharedSnapshotAuthorizationComplete(snapshot: GatewayQuotaSnapshot): boolean {
  return (snapshot.authorizationEntriesComplete ?? true) && sharedSnapshotAuthorizationUsable(snapshot)
}

function sharedSnapshotAuthorizationUsable(snapshot: GatewayQuotaSnapshot): boolean {
  if (!authorizationSnapshotInvalidated) return true
  const generatedAtMs = rfc3339InstantMilliseconds(snapshot.generatedAt)
  if (generatedAtMs === undefined) {
    throw new Error('Redis runtime state 网关额度快照 generatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return generatedAtMs > authorizationSnapshotInvalidatedAtMs
}

function costSnapshotKey(input: {
  systemAccountId: string
  scopeType: string
  scopeId: string
  hourlyWindowHours?: number
}): string {
  return [
    input.systemAccountId,
    input.scopeType,
    input.scopeId,
    normalizeHourlyWindowHours(input.hourlyWindowHours) ?? ''
  ].join('\u0000')
}

function authorizationSnapshotKey(scopeType: 'account_authorization' | 'group_authorization', authorizationId: string): string {
  return `${scopeType}\u0000${authorizationId}`
}

function normalizeHourlyWindowHours(value?: number): number | undefined {
  return value === undefined ? undefined : Math.max(1, Math.trunc(value))
}

function cloneRequestQuotaCosts(costs: RequestQuotaCosts): RequestQuotaCosts {
  return {
    hourly: costs.hourly,
    daily: costs.daily,
    weekly: costs.weekly,
    monthly: costs.monthly,
    total: costs.total
  }
}

registerAuthorizationQuotaCacheInvalidator(invalidateGatewayAuthorizationQuotaSnapshot)

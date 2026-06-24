import type { RequestQuotaCosts } from './request-quota-checker.js'
import { logger } from '../../../shared/logger.js'

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
}

export const gatewayQuotaSnapshotCostPageSize = 5000
export const gatewayQuotaSnapshotAuthorizationPageSize = 5000
export const maxGatewayQuotaSnapshotCostEntries = gatewayQuotaSnapshotCostPageSize
export const maxGatewayQuotaSnapshotAuthorizationEntries = gatewayQuotaSnapshotAuthorizationPageSize

let snapshotGeneratedAt: string | undefined
let costSnapshotComplete = false
let authorizationSnapshotComplete = false
let authorizationSnapshotInvalidated = false
let authorizationSnapshotVersion = 0
const costSnapshot = new Map<string, RequestQuotaCosts>()
const authorizationSnapshot = new Map<string, GatewayQuotaDecision>()

export function replaceGatewayQuotaSnapshot(snapshot: GatewayQuotaSnapshot): void {
  snapshotGeneratedAt = snapshot.generatedAt
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
      generatedAt: snapshot.generatedAt,
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
  authorizationSnapshotVersion += 1
  costSnapshot.clear()
  authorizationSnapshot.clear()
}

export function invalidateGatewayAuthorizationQuotaSnapshot(): void {
  authorizationSnapshotInvalidated = true
  authorizationSnapshotComplete = false
  authorizationSnapshotVersion += 1
  authorizationSnapshot.clear()
}

export function readGatewayQuotaCostsSnapshot(input: {
  systemAccountId: string
  scopeType: string
  scopeId: string
  hourlyWindowHours?: number
}): RequestQuotaCosts | undefined {
  const costs = costSnapshot.get(costSnapshotKey(input))
  return costs ? cloneRequestQuotaCosts(costs) : undefined
}

export function readGatewayAuthorizationQuotaSnapshot(
  scopeType: 'account_authorization' | 'group_authorization',
  authorizationId?: string
): GatewayQuotaDecision | undefined {
  if (!authorizationId) return undefined
  const decision = authorizationSnapshot.get(authorizationSnapshotKey(scopeType, authorizationId))
  return decision ? { ...decision } : undefined
}

export function gatewayQuotaSnapshotRuntime(): {
  generatedAt?: string
  costEntryCount: number
  authorizationEntryCount: number
  costEntriesComplete: boolean
  authorizationEntriesComplete: boolean
} {
  return {
    generatedAt: snapshotGeneratedAt,
    costEntryCount: costSnapshot.size,
    authorizationEntryCount: authorizationSnapshot.size,
    costEntriesComplete: costSnapshotComplete,
    authorizationEntriesComplete: authorizationSnapshotComplete
  }
}

export function isGatewayQuotaCostSnapshotComplete(): boolean {
  return snapshotGeneratedAt !== undefined && costSnapshotComplete
}

export function isGatewayAuthorizationSnapshotComplete(): boolean {
  return snapshotGeneratedAt !== undefined && authorizationSnapshotComplete && !authorizationSnapshotInvalidated
}

export function isGatewayQuotaCostSnapshotIncomplete(): boolean {
  return snapshotGeneratedAt !== undefined && !costSnapshotComplete
}

export function isGatewayAuthorizationSnapshotIncomplete(): boolean {
  return authorizationSnapshotInvalidated || (snapshotGeneratedAt !== undefined && !authorizationSnapshotComplete)
}

export function gatewayAuthorizationQuotaSnapshotVersion(): number {
  return authorizationSnapshotVersion
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

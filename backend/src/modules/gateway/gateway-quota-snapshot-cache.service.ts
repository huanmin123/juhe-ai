import type { RequestQuotaCosts } from './request-quota-checker.js'

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

export const maxGatewayQuotaSnapshotCostEntries = 5000
export const maxGatewayQuotaSnapshotAuthorizationEntries = 5000

let snapshotGeneratedAt: string | undefined
let costSnapshotComplete = false
let authorizationSnapshotComplete = false
const costSnapshot = new Map<string, RequestQuotaCosts>()
const authorizationSnapshot = new Map<string, GatewayQuotaDecision>()

export function replaceGatewayQuotaSnapshot(snapshot: GatewayQuotaSnapshot): void {
  snapshotGeneratedAt = snapshot.generatedAt
  costSnapshotComplete = snapshot.costEntriesComplete ?? snapshot.costEntries.length < maxGatewayQuotaSnapshotCostEntries
  authorizationSnapshotComplete = snapshot.authorizationEntriesComplete ?? snapshot.authorizationEntries.length < maxGatewayQuotaSnapshotAuthorizationEntries
  costSnapshot.clear()
  authorizationSnapshot.clear()
  for (const entry of snapshot.costEntries.slice(0, maxGatewayQuotaSnapshotCostEntries)) {
    costSnapshot.set(costSnapshotKey(entry), cloneRequestQuotaCosts(entry.costs))
  }
  for (const entry of snapshot.authorizationEntries.slice(0, maxGatewayQuotaSnapshotAuthorizationEntries)) {
    authorizationSnapshot.set(authorizationSnapshotKey(entry.scopeType, entry.authorizationId), { ...entry.decision })
  }
}

export function clearGatewayQuotaSnapshot(): void {
  snapshotGeneratedAt = undefined
  costSnapshotComplete = false
  authorizationSnapshotComplete = false
  costSnapshot.clear()
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
  return snapshotGeneratedAt !== undefined && authorizationSnapshotComplete
}

export function isGatewayQuotaCostSnapshotIncomplete(): boolean {
  return snapshotGeneratedAt !== undefined && !costSnapshotComplete
}

export function isGatewayAuthorizationSnapshotIncomplete(): boolean {
  return snapshotGeneratedAt !== undefined && !authorizationSnapshotComplete
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

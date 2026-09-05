import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyQuotaLimits,
  RouteStrategyMode,
  RouteStrategyStatus
} from '../domain/types.js'
import { normalizeRouteStrategyMode } from '../domain/route-strategy.js'
import { includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { parseApiKeyAvailabilityScheduleJson } from './api-key-availability-schedule.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import {
  loadApiKeyListUsageSummariesForScopes,
  loadApiKeyListUsageSummariesForScopesAsync,
  type ApiKeyListUsageSummary
} from './usage-summary-loaders.js'

export interface ApiKeyListRow {
  id: string
  system_account_id: string
  system_account_name?: string | null
  route_strategy_id: string
  route_strategy_name?: string | null
  route_strategy_mode?: RouteStrategyMode | null
  route_strategy_status?: RouteStrategyStatus | null
  name: string
  description: string | null
  key_prefix: string
  key_suffix: string
  status: 'active' | 'disabled'
  is_default?: number | string | boolean | null
  purpose?: 'general' | 'chat' | string | null
  expires_at: string | null
  quota_limits_json: string | null
  availability_schedule_json?: string | null
  updated_at: string
}

export interface ApiKeyListItem {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  description?: string
  keyPrefix: string
  keySuffix: string
  status: 'active' | 'disabled'
  isDefault: boolean
  purpose: 'general' | 'chat'
  routeStrategyId: string
  routeStrategyName?: string
  routeStrategyMode?: RouteStrategyMode
  routeStrategyStatus?: RouteStrategyStatus
  expiresAt?: string
  quotaLimits: ApiKeyQuotaLimits
  availabilitySchedule?: ApiKeyAvailabilitySchedule
  usage: ApiKeyListUsageSummary
  revision: string
}

const emptyApiKeyListUsageSummary: ApiKeyListUsageSummary = {
  requestCount: 0,
  totalTokens: 0,
  totalCost: 0
}

export function apiKeyListItemsFromRows(rows: ApiKeyListRow[], access?: AccessScope): ApiKeyListItem[] {
  const usage = loadApiKeyListUsageSummariesForScopes(rows.map(apiKeyUsageScope))
  return apiKeyListItemsFromRowsAndUsage(rows, usage, access)
}

export async function apiKeyListItemsFromRowsAsync(rows: ApiKeyListRow[], access?: AccessScope): Promise<ApiKeyListItem[]> {
  const usage = await loadApiKeyListUsageSummariesForScopesAsync(rows.map(apiKeyUsageScope))
  return apiKeyListItemsFromRowsAndUsage(rows, usage, access)
}

function apiKeyListItemsFromRowsAndUsage(
  rows: ApiKeyListRow[],
  usage: Map<string, ApiKeyListUsageSummary>,
  access?: AccessScope
): ApiKeyListItem[] {
  const includeOwner = includeSystemAccountFields(access)
  return rows.map((row) => ({
    id: row.id,
    ...(includeOwner
      ? {
          systemAccountId: row.system_account_id,
          systemAccountName: row.system_account_name ?? undefined
        }
      : {}),
    name: row.name,
    description: row.description ?? undefined,
    keyPrefix: row.key_prefix,
    keySuffix: row.key_suffix,
    status: row.status,
    isDefault: normalizeApiKeyDefaultFlag(row.is_default),
    purpose: row.purpose === 'chat' ? 'chat' : 'general',
    routeStrategyId: row.route_strategy_id,
    routeStrategyName: row.route_strategy_name ?? undefined,
    routeStrategyMode: row.route_strategy_mode ? normalizeRouteStrategyMode(row.route_strategy_mode) : undefined,
    routeStrategyStatus: normalizeRouteStrategyStatus(row.route_strategy_status),
    expiresAt: row.expires_at ?? undefined,
    quotaLimits: parseRequestQuotaLimitsJson(row.quota_limits_json),
    availabilitySchedule: parseApiKeyAvailabilityScheduleJson(row.availability_schedule_json),
    usage: usage.get(row.id) ?? emptyApiKeyListUsageSummary,
    revision: row.updated_at
  }))
}

function apiKeyUsageScope(row: ApiKeyListRow) {
  return { rowKey: row.id, systemAccountId: row.system_account_id, scopeId: row.id }
}

function normalizeRouteStrategyStatus(value: unknown): RouteStrategyStatus | undefined {
  return value === 'active' || value === 'disabled' ? value : undefined
}

function normalizeApiKeyDefaultFlag(value: unknown): boolean {
  return value === true || value === 1 || value === '1'
}

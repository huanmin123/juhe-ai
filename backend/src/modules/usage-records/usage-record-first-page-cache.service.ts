import { randomUUID } from 'node:crypto'

import { createSharedJsonCache, type SharedJsonCacheOperationOptions } from '../../shared/cache.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { canAccessAll, scopedSystemAccountId, type AccessScope } from '../../storage/access-scope.js'
import { dateKey, startOfZonedDateKeyIso, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import type { UsageRecordInput, UsageRecordListOptions, UsageRecordListResult, UsageRecordSummary } from '../../storage/usage-records.repository.js'

const firstPageLimit = 50
const firstPageResponseSize = 20
export interface UsageRecordFirstPageNameMaps {
  apiKeyNames: ReadonlyMap<string, string>
  groupNames: ReadonlyMap<string, string>
  accountNames: ReadonlyMap<string, string>
}

const cache = createSharedJsonCache<UsageRecordSummary[]>({
  name: 'usage_record_first_page_v2',
  max: 512,
  ttlMs: 36 * 60 * 60 * 1000,
  version: 'v2'
})

export async function publishUsageRecordFirstPage(inputs: UsageRecordInput[], names: UsageRecordFirstPageNameMaps): Promise<void> {
  const gatewayInputs = usageRecordFirstPageCandidateInputs(inputs)
  if (gatewayInputs.length === 0) return
  const timezone = await usageStatsTimezoneAsync()
  const byKey = new Map<string, UsageRecordSummary[]>()
  for (const input of gatewayInputs) {
    const date = dateKey(new Date(input.createdAt!), timezone)
    const key = firstPageCacheKey(input.systemAccountId!, date)
    const rows = byKey.get(key) ?? []
    rows.push(usageRecordFirstPageSummaryFromInput(input, names))
    byKey.set(key, rows)
  }
  for (const [key, rows] of byKey) {
    try {
      await mergeFirstPageCache(key, rows)
    } catch (error) {
      logger.warn(errorLogFields(error, { event: 'usage_record_first_page_cache_write_failed', cacheKey: key }), '使用记录首屏热列表更新失败，已保留数据库事实')
    }
  }
}

export function usageRecordFirstPageCandidateInputs(inputs: UsageRecordInput[]): UsageRecordInput[] {
  return inputs.filter((input) => input.trafficSource === 'gateway' && input.systemAccountId && input.id && input.createdAt)
}

export async function seedUsageRecordFirstPage(
  access: AccessScope | undefined,
  options: UsageRecordListOptions | undefined,
  result: UsageRecordListResult
): Promise<void> {
  const systemAccountId = scopedSystemAccountId(access)
  if (!systemAccountId || canAccessAll(access) || !isDefaultFirstPageOptions(options)) return
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const todayStart = startOfZonedDateKeyIso(today, timezone)
  const tomorrow = dateKey(new Date(Date.now() + 24 * 60 * 60 * 1000), timezone)
  const tomorrowStart = startOfZonedDateKeyIso(tomorrow, timezone)
  if (options?.startAt !== todayStart || options?.endAt !== tomorrowStart) return
  try {
    await seedUsageRecordFirstPageForDate(systemAccountId, today, result.items)
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'usage_record_first_page_cache_seed_failed', systemAccountId }), '使用记录首屏热列表回填失败，已保留数据库查询结果')
  }
}

export async function hasUsageRecordFirstPageForDate(systemAccountId: string, date: string, options?: SharedJsonCacheOperationOptions): Promise<boolean> {
  return await cache.get(firstPageCacheKey(systemAccountId, date), options) !== undefined
}

export async function seedUsageRecordFirstPageForDate(
  systemAccountId: string,
  date: string,
  rows: UsageRecordSummary[],
  options?: SharedJsonCacheOperationOptions
): Promise<void> {
  await mergeFirstPageCache(firstPageCacheKey(systemAccountId, date), rows, options)
}

async function mergeFirstPageCache(key: string, rows: UsageRecordSummary[], options?: SharedJsonCacheOperationOptions): Promise<void> {
  const token = randomUUID()
  for (let attempt = 0; attempt < 3; attempt += 1) {
    options?.signal?.throwIfAborted()
    if (!await cache.acquireLease(key, { ttlMs: 2_000, token, ...options })) {
      await abortableCacheDelay(10 * (attempt + 1), options?.signal)
      continue
    }
    try {
      const existing = await cache.get(key, options) ?? []
      const merged = dedupeAndSort([...rows, ...existing]).slice(0, firstPageLimit)
      if (!await cache.setIfLeaseOwner(key, token, merged, options)) throw new Error('使用记录首屏缓存租约已失效')
      return
    } finally {
      await cache.releaseLease(key, token)
    }
  }
  throw new Error('使用记录首屏缓存更新竞争超时')
}

function abortableCacheDelay(delayMs: number, signal?: AbortSignal): Promise<void> {
  signal?.throwIfAborted()
  if (!signal) return new Promise((resolve) => setTimeout(resolve, delayMs))
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, delayMs)
    const onAbort = () => {
      clearTimeout(timer)
      reject(signal.reason)
    }
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

export async function getUsageRecordFirstPage(
  access: AccessScope | undefined,
  options: UsageRecordListOptions | undefined
): Promise<UsageRecordListResult | undefined> {
  const systemAccountId = scopedSystemAccountId(access)
  if (!systemAccountId || canAccessAll(access) || !isDefaultFirstPageOptions(options)) return undefined
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const todayStart = startOfZonedDateKeyIso(today, timezone)
  const tomorrow = dateKey(new Date(Date.now() + 24 * 60 * 60 * 1000), timezone)
  const tomorrowStart = startOfZonedDateKeyIso(tomorrow, timezone)
  if (options?.startAt !== todayStart || options?.endAt !== tomorrowStart) return undefined
  try {
    const rows = await cache.get(firstPageCacheKey(systemAccountId, today))
    if (!rows) return undefined
    return { items: rows.slice(0, firstPageResponseSize), total: rows.length, page: 1, pageSize: firstPageResponseSize, hasMore: rows.length > firstPageResponseSize }
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'usage_record_first_page_cache_read_failed', systemAccountId }), '使用记录首屏热列表读取失败，回源数据库')
    return undefined
  }
}

export function isUsageRecordFirstPageOptions(options: UsageRecordListOptions | undefined): boolean {
  return isDefaultFirstPageOptions(options)
}

function isDefaultFirstPageOptions(options: UsageRecordListOptions | undefined): boolean {
  return (options?.page ?? 1) === 1
    && (options?.pageSize ?? 50) === firstPageResponseSize
    && (options?.sortBy ?? 'createdAt') === 'createdAt'
    && (options?.sortOrder ?? 'desc') === 'desc'
    && options?.trafficSource === 'gateway'
    && !options?.traceId && !options?.accountKeyword && !options?.clientIp
    && !options?.result && options?.statusCode === undefined && !options?.groupId && !options?.model
}

function firstPageCacheKey(systemAccountId: string, date: string): string {
  return `${systemAccountId}:${date}`
}

export function usageRecordFirstPageSummaryFromInput(input: UsageRecordInput, names: UsageRecordFirstPageNameMaps): UsageRecordSummary {
  return {
    id: input.id!,
    systemAccountId: input.systemAccountId,
    traceId: input.traceId,
    trafficSource: input.trafficSource,
    clientIp: input.clientIp,
    apiKeyId: input.apiKeyId,
    apiKeyName: input.apiKeyId ? names.apiKeyNames.get(input.apiKeyId) : undefined,
    groupId: input.groupId,
    groupName: input.groupId ? names.groupNames.get(input.groupId) : undefined,
    accountId: input.accountId,
    accountName: input.accountId ? names.accountNames.get(input.accountId) : undefined,
    endpoint: input.endpoint,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    usageSemantic: input.usageSemantic,
    model: input.model,
    upstreamModel: input.upstreamModel,
    pricingModel: input.pricingModel,
    requestedServiceTier: input.requestedServiceTier,
    effectiveServiceTier: input.effectiveServiceTier,
    reportedServiceTier: input.reportedServiceTier,
    billedServiceTier: input.billedServiceTier,
    requestedReasoningEffort: input.requestedReasoningEffort,
    effectiveReasoningEffort: input.effectiveReasoningEffort,
    pricingSnapshot: input.pricingSnapshot,
    modelMappingApplied: input.modelMappingApplied,
    modelMappingSource: input.modelMappingSource,
    sourceEndpointFamily: input.sourceEndpointFamily,
    upstreamEndpointFamily: input.upstreamEndpointFamily,
    stream: input.stream === true,
    statusCode: input.statusCode,
    success: input.success,
    failureAttribution: input.failureAttribution,
    firstTokenMs: input.firstTokenMs,
    durationMs: input.durationMs,
    inputTokens: input.inputTokens,
    outputTokens: input.outputTokens,
    cacheReadTokens: input.cacheReadTokens,
    cacheReadCostUsd: input.cacheReadCostUsd,
    cacheWriteTokens: input.cacheWriteTokens,
    cacheWrite1hTokens: input.cacheWrite1hTokens,
    cacheWriteCostUsd: input.cacheWriteCostUsd,
    thinkingTokens: input.thinkingTokens,
    inputImageTokens: input.inputImageTokens,
    outputImageTokens: input.outputImageTokens,
    inputAudioTokens: input.inputAudioTokens,
    outputAudioTokens: input.outputAudioTokens,
    outputImageCount: input.outputImageCount,
    costUsd: input.costUsd,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    createdAt: input.createdAt!
  }
}

function dedupeAndSort(rows: UsageRecordSummary[]): UsageRecordSummary[] {
  const seen = new Set<string>()
  return rows
    .filter((row) => !seen.has(row.id) && seen.add(row.id))
    .sort((left, right) => right.createdAt.localeCompare(left.createdAt) || right.id.localeCompare(left.id))
}

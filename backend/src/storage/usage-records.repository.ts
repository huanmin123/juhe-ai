import type { SQLInputValue } from 'node:sqlite'

import { buildSystemAccountScopeClause, canAccessAll, includeSystemAccountFields, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getUsageCatalogDatabase, mainDatabaseRuntimeInfo, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadSystemAccountNameMapByIds, loadSystemAccountNameMapByIdsAsync } from './repository-lookups.js'
import { buildUsageAccessLookupContext, systemAccountIdForUsage, usageAccessMetadata, usageApiKeyExists } from './usage-record-access-metadata.js'
import { buildUsageRecordFilters, buildUsageRecordOrderClause, type NormalizedUsageRecordListOptions, normalizeUsageRecordListOptions, type UsageRecordFilterResult, type UsageRecordFilterSettings } from './usage-record-list-query.js'
import { hydrateUsageRecordNames, hydrateUsageRecordNamesAsync, usageRecordSummaryFromRow, type UsageRecordRow } from './usage-record-mappers.js'
import {
  generateUsageRecordId,
  getUsageRecordShardDatabase,
  findRegisteredUsageRecordShardLocation,
  listUsageRecordShardLocations,
  queryUsageRecordShardById,
  recordUsageRecordShardEntries,
  usageRecordShardLocationForRecord,
  writeUsageRecordShardRows,
  type UsageRecordShardEntryInput,
  type UsageRecordShardLocation,
  type UsageRecordShardQueryWindow,
  type UsageRecordShardWriteResult,
  type UsageRecordShardWriteRow
} from './usage-record-shards.js'
import { writeUsageRecordShardRowsWithWriterPool } from './usage-record-writer-pool.js'
import { optionalString } from './value-utils.js'
import type { ResourceAuthorizationSourceType } from '../domain/types.js'
import { recordAccountHealthSuccessSignals } from './account-health-check.repository.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import {
  ensurePostgresUsageRecordPartitions,
  postgresUsageRecordPartitionPruningClauseForId
} from './postgres-usage-record-partitions.js'
import {
  estimateCatalogCacheReadCostUsdAsync,
  estimateCatalogCacheWriteCostUsdAsync,
  estimateCatalogCostUsdAsync,
  resolveCatalogPricingModelAsync
} from '../modules/model-pricing/model-catalog.service.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export interface UsageRecordLogSnapshot {
  [key: string]: unknown
}

export interface UsageRecordSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  traceId: string
  trafficSource?: UsageRecordTrafficSource
  clientIp?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  endpoint?: string
  providerCode?: string
  providerProtocolProfileId?: string
  usageSemantic?: string
  model?: string
  upstreamModel?: string
  pricingModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
  stream: boolean
  statusCode?: number
  success: boolean
  failureAttribution?: UsageFailureAttribution
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheReadCostUsd?: number
  cacheWriteTokens?: number
  cacheWrite1hTokens?: number
  cacheWriteCostUsd?: number
  thinkingTokens?: number
  inputImageTokens?: number
  outputImageTokens?: number
  inputAudioTokens?: number
  outputAudioTokens?: number
  outputImageCount?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: UsageRecordLogSnapshot
  responseSnapshot?: UsageRecordLogSnapshot
  createdAt: string
}

export type UsageRecordTrafficSource = 'gateway' | 'manual_account_test' | 'account_health_check' | 'runtime_recovery_probe' | 'cooldown_retest' | 'hybrid_scoring' | 'hybrid_quality_scoring'
export type UsageFailureAttribution = 'account_upstream' | 'account_dependency' | 'gateway_capacity' | 'gateway_policy' | 'client_lifecycle'
export type UsageRecordSortField = 'createdAt' | 'firstTokenMs' | 'durationMs' | 'costUsd'
export type UsageRecordSortDirection = 'asc' | 'desc'

export interface UsageRecordListOptions {
  page?: number
  pageSize?: number
  sortBy?: UsageRecordSortField
  sortOrder?: UsageRecordSortDirection
  traceId?: string
  accountKeyword?: string
  clientIp?: string
  result?: 'success' | 'failed' | 'all'
  statusCode?: number
  groupId?: string
  model?: string
  trafficSource?: UsageRecordTrafficSource
  startAt?: string
  endAt?: string
}

export interface UsageRecordListResult {
  items: UsageRecordSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  requiresSystemAccountSelection?: boolean
}

export interface UsageRecordInput {
  id?: string
  systemAccountId?: string
  traceId: string
  trafficSource: UsageRecordTrafficSource
  clientIp?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  accountOwnerSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType?: 'owner' | 'authorized'
  accountAuthorizationId?: string
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
  endpoint?: string
  providerCode?: string
  providerProtocolProfileId?: string
  usageSemantic?: string
  model?: string
  upstreamModel?: string
  pricingModel?: string
  serviceTier?: 'default' | 'priority' | 'flex'
  priorityPriceMultiplier?: number
  flexPriceMultiplier?: number
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
  stream?: boolean
  statusCode?: number
  success: boolean
  failureAttribution?: UsageFailureAttribution
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheReadCostUsd?: number
  cacheWriteTokens?: number
  cacheWrite1hTokens?: number
  cacheWriteCostUsd?: number
  thinkingTokens?: number
  inputImageTokens?: number
  outputImageTokens?: number
  inputAudioTokens?: number
  outputAudioTokens?: number
  outputImageCount?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: unknown
  responseSnapshot?: unknown
  createdAt?: string
}

const usageRecordAccountKeywordMatchLimit = 200

export function listUsageRecords(access?: AccessScope, options?: UsageRecordListOptions): UsageRecordListResult {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('JUHE_AI_DATABASE_DRIVER=postgres 时请使用 listUsageRecordsAsync 读取使用记录')
  }
  const listOptions = normalizeUsageRecordListOptions(options)
  if (usageRecordListRequiresSystemAccountSelection(access)) {
    return emptyUsageRecordListResult(listOptions, true)
  }
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const offset = (listOptions.page - 1) * listOptions.pageSize
  const rows = listUsageRecordRowsFromShards(
    buildUsageRecordFilters(access, options),
    listOptions,
    buildUsageRecordOrderClause(listOptions),
    offset + listOptions.pageSize + 1,
    usageRecordShardQueryWindowFromOptions(options)
  )
  const pageRows = takePageRows(rows.slice(offset), listOptions.pageSize)
  const rowsWithNames = hydrateUsageRecordNames(pageRows.rows)
  const accountNames = shouldIncludeSystemAccountFields
    ? loadSystemAccountNameMapByIds(rowsWithNames.map((row) => optionalString(row.system_account_id)))
    : new Map<string, string>()
  const items = rowsWithNames.map((row) => usageRecordSummaryFromRow(row, shouldIncludeSystemAccountFields, accountNames))
  return {
    items,
    total: pagedTotalUpperBound(listOptions.page, listOptions.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

function usageRecordListRequiresSystemAccountSelection(access?: AccessScope): boolean {
  return canAccessAll(access) && !scopedSystemAccountId(access)
}

function emptyUsageRecordListResult(listOptions: NormalizedUsageRecordListOptions, requiresSystemAccountSelection = false): UsageRecordListResult {
  return {
    items: [],
    total: 0,
    hasMore: false,
    page: listOptions.page,
    pageSize: listOptions.pageSize,
    requiresSystemAccountSelection
  }
}

export async function listUsageRecordsAsync(access?: AccessScope, options?: UsageRecordListOptions): Promise<UsageRecordListResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_usage_records_read_only',
      access,
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listUsageRecords(access, options)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const listOptions = normalizeUsageRecordListOptions(options)
  if (usageRecordListRequiresSystemAccountSelection(access)) {
    return emptyUsageRecordListResult(listOptions, true)
  }
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const offset = (listOptions.page - 1) * listOptions.pageSize
  const filters = await buildUsageRecordFiltersAsync(client, access, options)
  const rows = await listPostgresUsageRecordRows(
    client,
    filters,
    buildUsageRecordOrderClause(listOptions),
    offset + listOptions.pageSize + 1
  )
  const pageRows = takePageRows(rows.slice(offset), listOptions.pageSize)
  const rowsWithNames = await hydrateUsageRecordNamesAsync(client, pageRows.rows)
  const accountNames = shouldIncludeSystemAccountFields
    ? await loadSystemAccountNameMapByIdsAsync(client, rowsWithNames.map((row) => optionalString(row.system_account_id)))
    : new Map<string, string>()
  const items = rowsWithNames.map((row) => usageRecordSummaryFromRow(row, shouldIncludeSystemAccountFields, accountNames))
  return {
    items,
    total: pagedTotalUpperBound(listOptions.page, listOptions.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export function getUsageRecordDetail(id: string, access?: AccessScope): UsageRecordSummary | undefined {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('JUHE_AI_DATABASE_DRIVER=postgres 时请使用 getUsageRecordDetailAsync 读取使用记录详情')
  }
  const recordId = id.trim()
  if (!recordId) return undefined
  const scope = buildSystemAccountScopeClause(access, 'ur.system_account_id')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const detailSql = `
      SELECT
        ur.*
      FROM usage_records ur
      WHERE ur.id = ?
      ${scope.clause}
      LIMIT 1
    `
  const detailParams = [recordId, ...scope.params]
  const row = queryUsageRecordShardById<UsageRecordRow>(recordId, detailSql, detailParams)
    ?? queryUsageRecordShardByCatalogEntry<UsageRecordRow>(recordId, detailSql, detailParams)
  const namedRow = row ? hydrateUsageRecordNames([row])[0] : undefined
  const accountNames = shouldIncludeSystemAccountFields
    ? loadSystemAccountNameMapByIds([optionalString(namedRow?.system_account_id)])
    : new Map<string, string>()
  return namedRow ? usageRecordSummaryFromRow(namedRow, shouldIncludeSystemAccountFields, accountNames, true) : undefined
}

function queryUsageRecordShardByCatalogEntry<T extends Record<string, unknown>>(
  id: string,
  selectSql: string,
  params: SQLInputValue[]
): T | undefined {
  const entry = getUsageCatalogDatabase()
    .prepare(`
      SELECT shard_key
      FROM usage_record_shard_entries
      WHERE usage_id = ?
      LIMIT 1
    `)
    .get(id) as { shard_key?: string } | undefined
  if (!entry?.shard_key) return undefined
  const location = findRegisteredUsageRecordShardLocation(entry.shard_key)
  if (!location) return undefined
  return getUsageRecordShardDatabase(location)
    .prepare(selectSql)
    .get(...params) as T | undefined
}

export async function getUsageRecordDetailAsync(id: string, access?: AccessScope): Promise<UsageRecordSummary | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_usage_record_detail_read_only',
      id,
      access
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getUsageRecordDetail(id, access)
  }
  const recordId = id.trim()
  if (!recordId) return undefined
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const scope = buildSystemAccountScopeClause(access, 'ur.system_account_id')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const partitionPruning = postgresUsageRecordPartitionPruningClauseForId(recordId)
  const row = await client.one<UsageRecordRow>(`
    SELECT ur.*
    FROM juhe_usage.usage_records ur
    WHERE ur.id = ?
    ${partitionPruning.clause}
    ${scope.clause}
    LIMIT 1
  `, [recordId, ...partitionPruning.params, ...scope.params])
  const namedRow = row ? (await hydrateUsageRecordNamesAsync(client, [row]))[0] : undefined
  const accountNames = shouldIncludeSystemAccountFields
    ? await loadSystemAccountNameMapByIdsAsync(client, [optionalString(namedRow?.system_account_id)])
    : new Map<string, string>()
  return namedRow ? usageRecordSummaryFromRow(namedRow, shouldIncludeSystemAccountFields, accountNames, true) : undefined
}

export function createUsageRecord(input: UsageRecordInput): void {
  createUsageRecordsBatch([input])
}

export function createUsageRecordsBatch(inputs: UsageRecordInput[]): void {
  if (inputs.length === 0) {
    return
  }

  const businessDatabase = getBusinessDatabase()
  const accountLastUsedAt = new Map<string, string>()
  const accountHealthSuccessAt = new Map<string, string>()
  const writePlan = buildUsageRecordBatchWritePlan(inputs)
  let accountLastUsedFlushed = false

  try {
    for (const shardRows of writePlan.rowsByShard.values()) {
      const writeResult = writeUsageRecordShardRows(shardRows.location, shardRows.rows)
      mergeUsageRecordShardWriteResult(accountLastUsedAt, accountHealthSuccessAt, writeResult)
    }

    recordUsageRecordShardEntries(writePlan.shardEntries, { locations: writePlan.locations })
    flushUsageRecordBusinessSideEffects(accountLastUsedAt, accountHealthSuccessAt, businessDatabase)
    accountLastUsedFlushed = true
  } catch (error) {
    if (!accountLastUsedFlushed && accountLastUsedAt.size > 0) {
      flushUsageRecordBusinessSideEffects(accountLastUsedAt, accountHealthSuccessAt, businessDatabase)
    }
    throw error
  }
}

export async function createUsageRecordsBatchAsync(inputs: UsageRecordInput[]): Promise<void> {
  if (inputs.length === 0) {
    return
  }
  if (runtimeConfig.databaseDriver === 'postgres') {
    await createUsageRecordsBatchPostgres(inputs)
    return
  }

  const businessDatabase = getBusinessDatabase()
  const accountLastUsedAt = new Map<string, string>()
  const accountHealthSuccessAt = new Map<string, string>()
  const writePlan = buildUsageRecordBatchWritePlan(inputs)
  let accountLastUsedFlushed = false

  try {
    const writeResults = await Promise.allSettled([...writePlan.rowsByShard.values()].map(async (shardRows) => {
      return await writeUsageRecordShardRowsWithWriterPool(shardRows.location, shardRows.rows)
    }))
    const writeErrors: unknown[] = []
    for (const writeResult of writeResults) {
      if (writeResult.status === 'fulfilled') {
        mergeUsageRecordShardWriteResult(accountLastUsedAt, accountHealthSuccessAt, writeResult.value)
      } else {
        writeErrors.push(writeResult.reason)
      }
    }
    if (writeErrors.length > 0) {
      throw normalizeUsageRecordBatchWriteError(writeErrors)
    }
    recordUsageRecordShardEntries(writePlan.shardEntries, { locations: writePlan.locations })
    flushUsageRecordBusinessSideEffects(accountLastUsedAt, accountHealthSuccessAt, businessDatabase)
    accountLastUsedFlushed = true
  } catch (error) {
    if (!accountLastUsedFlushed && accountLastUsedAt.size > 0) {
      flushUsageRecordBusinessSideEffects(accountLastUsedAt, accountHealthSuccessAt, businessDatabase)
    }
    throw error
  }
}

async function createUsageRecordsBatchPostgres(inputs: UsageRecordInput[]): Promise<void> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const accountLastUsedAt = new Map<string, string>()
  const accountHealthSuccessAt = new Map<string, string>()
  const enrichedInputs = await enrichUsageRecordPricingAsync(inputs)
  const writePlan = buildUsageRecordBatchWritePlan(enrichedInputs, {
    accessLookupMode: 'provided',
    shardLocationMode: 'postgres'
  })
  if (writePlan.shardEntries.length === 0) return

  await client.transaction(async (tx) => {
    for (const shardRows of writePlan.rowsByShard.values()) {
      await insertPostgresUsageRecordRows(tx, shardRows.rows)
      collectPostgresUsageRecordBusinessSideEffects(accountLastUsedAt, accountHealthSuccessAt, shardRows.rows)
    }
    await flushPostgresUsageRecordBusinessSideEffects(tx, accountLastUsedAt, accountHealthSuccessAt)
  })
}

async function enrichUsageRecordPricingAsync(inputs: UsageRecordInput[]): Promise<UsageRecordInput[]> {
  const pricingModelCache = new Map<string, Promise<string | undefined>>()
  const resolvePricingModel = async (input: { providerCode: string; model?: string; systemAccountId?: string }): Promise<string | undefined> => {
    const key = [input.providerCode, input.systemAccountId ?? '', input.model ?? ''].join('\u0000')
    let pending = pricingModelCache.get(key)
    if (!pending) {
      pending = resolveCatalogPricingModelAsync(input)
      pricingModelCache.set(key, pending)
    }
    return await pending
  }
  const enriched: UsageRecordInput[] = []
  for (const input of inputs) {
    enriched.push(await enrichSingleUsageRecordPricingAsync(input, resolvePricingModel))
  }
  return enriched
}

async function enrichSingleUsageRecordPricingAsync(
  input: UsageRecordInput,
  resolvePricingModel: (input: { providerCode: string; model?: string; systemAccountId?: string }) => Promise<string | undefined>
): Promise<UsageRecordInput> {
  if (!input.providerCode) return input
  const providerCode = input.providerCode
  const catalogSystemAccountId = input.accountOwnerSystemAccountId || input.systemAccountId
  const upstreamModel = normalizeUsageRecordPricingModel(input.upstreamModel)
  const requestedModel = normalizeUsageRecordPricingModel(input.model)
  const existingPricingModel = normalizeUsageRecordPricingModel(input.pricingModel)

  try {
    const pricingModel = existingPricingModel
      ?? await resolveUsageRecordPricingModelAsync({
        providerCode,
        systemAccountId: catalogSystemAccountId,
        upstreamModel,
        requestedModel,
        resolvePricingModel
      })
    const costModel = pricingModel ?? upstreamModel ?? requestedModel
    if (!costModel) {
      return pricingModel && pricingModel !== input.pricingModel
        ? { ...input, pricingModel }
        : input
    }

    const enriched: UsageRecordInput = pricingModel && pricingModel !== input.pricingModel
      ? { ...input, pricingModel }
      : { ...input }

    if (enriched.cacheReadCostUsd === undefined && enriched.cacheReadTokens !== undefined) {
      enriched.cacheReadCostUsd = await estimateCatalogCacheReadCostUsdAsync({
        providerCode,
        systemAccountId: catalogSystemAccountId,
        model: costModel,
        serviceTier: enriched.serviceTier,
        priorityPriceMultiplier: enriched.priorityPriceMultiplier,
        flexPriceMultiplier: enriched.flexPriceMultiplier,
        cacheReadTokens: enriched.cacheReadTokens
      })
    }

    if (
      enriched.cacheWriteCostUsd === undefined
      && (enriched.cacheWriteTokens !== undefined || enriched.cacheWrite1hTokens !== undefined)
    ) {
      enriched.cacheWriteCostUsd = await estimateCatalogCacheWriteCostUsdAsync({
        providerCode,
        systemAccountId: catalogSystemAccountId,
        model: costModel,
        serviceTier: enriched.serviceTier,
        priorityPriceMultiplier: enriched.priorityPriceMultiplier,
        flexPriceMultiplier: enriched.flexPriceMultiplier,
        cacheWriteTokens: enriched.cacheWriteTokens,
        cacheWrite1hTokens: enriched.cacheWrite1hTokens
      })
    }

    if (enriched.costUsd === undefined && hasUsageRecordCostDimension(enriched)) {
      enriched.costUsd = await estimateCatalogCostUsdAsync({
        providerCode,
        systemAccountId: catalogSystemAccountId,
        model: costModel,
        serviceTier: enriched.serviceTier,
        priorityPriceMultiplier: enriched.priorityPriceMultiplier,
        flexPriceMultiplier: enriched.flexPriceMultiplier,
        inputTokens: enriched.inputTokens,
        outputTokens: enriched.outputTokens,
        cacheReadTokens: enriched.cacheReadTokens,
        cacheWriteTokens: enriched.cacheWriteTokens,
        cacheWrite1hTokens: enriched.cacheWrite1hTokens,
        thinkingTokens: enriched.thinkingTokens,
        inputImageTokens: enriched.inputImageTokens,
        outputImageTokens: enriched.outputImageTokens,
        inputAudioTokens: enriched.inputAudioTokens,
        outputAudioTokens: enriched.outputAudioTokens,
        outputImageCount: enriched.outputImageCount
      })
    }

    return enriched
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'usage_record_pricing_enrichment_failed',
      traceId: input.traceId,
      providerCode,
      model: input.model,
      upstreamModel: input.upstreamModel,
      trafficSource: input.trafficSource
    }), '使用记录写入前补算成本失败，保留原始用量记录')
    return input
  }
}

async function resolveUsageRecordPricingModelAsync(input: {
  providerCode: string
  systemAccountId?: string
  upstreamModel?: string
  requestedModel?: string
  resolvePricingModel: (input: { providerCode: string; model?: string; systemAccountId?: string }) => Promise<string | undefined>
}): Promise<string | undefined> {
  const upstreamPricingModel = await input.resolvePricingModel({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    model: input.upstreamModel
  })
  if (upstreamPricingModel) return upstreamPricingModel

  if (!input.requestedModel || input.requestedModel === input.upstreamModel) return undefined
  return await input.resolvePricingModel({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    model: input.requestedModel
  })
}

function normalizeUsageRecordPricingModel(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  return normalized || undefined
}

function hasUsageRecordCostDimension(input: UsageRecordInput): boolean {
  return input.inputTokens !== undefined
    || input.outputTokens !== undefined
    || input.cacheReadTokens !== undefined
    || input.cacheWriteTokens !== undefined
    || input.cacheWrite1hTokens !== undefined
    || input.inputImageTokens !== undefined
    || input.outputImageTokens !== undefined
    || input.inputAudioTokens !== undefined
    || input.outputAudioTokens !== undefined
    || input.outputImageCount !== undefined
}

function normalizeUsageRecordBatchWriteError(errors: unknown[]): Error {
  const first = errors[0]
  if (first instanceof Error) {
    return first
  }
  return new Error(first === undefined ? 'usage record shard write failed' : String(first))
}

interface UsageRecordBatchWritePlan {
  rowsByShard: Map<string, { location: UsageRecordShardLocation; rows: UsageRecordShardWriteRow[] }>
  shardEntries: UsageRecordShardEntryInput[]
  locations: UsageRecordShardLocation[]
}

function buildUsageRecordBatchWritePlan(
  inputs: UsageRecordInput[],
  options: { accessLookupMode?: 'database' | 'provided'; shardLocationMode?: 'sqlite' | 'postgres' } = {}
): UsageRecordBatchWritePlan {
  const providedOnly = options.accessLookupMode === 'provided'
  const postgresLogicalShardLocations = options.shardLocationMode === 'postgres'
  const accessLookupContext = providedOnly ? undefined : buildUsageAccessLookupContext(inputs)
  const rowsByShard = new Map<string, { location: UsageRecordShardLocation; rows: UsageRecordShardWriteRow[] }>()
  const shardEntries: UsageRecordShardEntryInput[] = []

  for (const input of inputs) {
    if (!providedOnly && input.apiKeyId && !usageApiKeyExists(input.apiKeyId, accessLookupContext)) {
      continue
    }
    const now = input.createdAt ?? nowIso()
    const id = input.id ?? generateUsageRecordId(now, newId('usage'))
    const systemAccountId = input.systemAccountId ?? systemAccountIdFromUsageInput(input, accessLookupContext, providedOnly)
    const accessMetadata = providedOnly
      ? usageAccessMetadataFromProvidedInput({ ...input, systemAccountId })
      : usageAccessMetadata({ ...input, systemAccountId }, accessLookupContext)
    const trafficSource = normalizeUsageRecordTrafficSource(input.trafficSource)
    const failureAttribution = usageFailureAttributionForInput(input)
    const row: UsageRecordShardWriteRow = {
      id,
      params: [
        id,
        systemAccountId,
        input.traceId,
        trafficSource,
        input.clientIp ?? null,
        input.apiKeyId ?? null,
        input.groupId ?? null,
        input.accountId ?? null,
        input.endpoint ?? null,
        input.providerCode ?? null,
        input.providerProtocolProfileId ?? null,
        input.usageSemantic ?? null,
        input.model ?? null,
        input.upstreamModel ?? null,
        input.pricingModel ?? null,
        input.modelMappingApplied ? 1 : 0,
        input.modelMappingSource ?? null,
        input.sourceEndpointFamily ?? null,
        input.upstreamEndpointFamily ?? null,
        input.stream ? 1 : 0,
        input.statusCode ?? null,
        input.success ? 1 : 0,
        failureAttribution,
        input.firstTokenMs ?? null,
        input.durationMs ?? null,
        input.inputTokens ?? null,
        input.outputTokens ?? null,
        input.cacheReadTokens ?? null,
        input.cacheReadCostUsd ?? null,
        input.cacheWriteTokens ?? null,
        input.cacheWrite1hTokens ?? null,
        input.cacheWriteCostUsd ?? null,
        input.thinkingTokens ?? null,
        input.inputImageTokens ?? null,
        input.outputImageTokens ?? null,
        input.inputAudioTokens ?? null,
        input.outputAudioTokens ?? null,
        input.outputImageCount ?? null,
        input.costUsd ?? null,
        input.errorCode ?? null,
        input.errorMessage ?? null,
        input.requestSnapshot ? JSON.stringify(input.requestSnapshot) : null,
        input.responseSnapshot ? JSON.stringify(input.responseSnapshot) : null,
        accessMetadata.accountOwnerSystemAccountId ?? null,
        accessMetadata.groupOwnerSystemAccountId ?? null,
        accessMetadata.accountAccessType ?? null,
        accessMetadata.groupAccessType ?? null,
        accessMetadata.accountAuthorizationId ?? null,
        accessMetadata.accountAuthorizationSourceType ?? null,
        accessMetadata.accountAuthorizationSourceTeamId ?? null,
        accessMetadata.groupAuthorizationId ?? null,
        accessMetadata.groupAuthorizationSourceType ?? null,
        accessMetadata.groupAuthorizationSourceTeamId ?? null,
        now
      ],
      accountId: input.accountId,
      accountLastUsedAt: shouldRecordAccountUsageSideEffects(trafficSource) ? now : undefined,
      accountHealthSuccessAt: shouldRecordAccountUsageSideEffects(trafficSource) && input.success ? now : undefined
    }

    const location = postgresLogicalShardLocations
      ? usageRecordLogicalShardLocationForPostgres(id, now)
      : usageRecordShardLocationForRecord(id, now)
    const shardRows = rowsByShard.get(location.shardKey) ?? { location, rows: [] }
    shardRows.rows.push(row)
    rowsByShard.set(location.shardKey, shardRows)
    shardEntries.push({
      id,
      shardKey: location.shardKey,
      systemAccountId,
      traceId: input.traceId,
      apiKeyId: input.apiKeyId ?? null,
      accountId: input.accountId ?? null,
      groupId: input.groupId ?? null,
      model: input.model ?? null,
      trafficSource,
      success: input.success,
      failureAttribution,
      statusCode: input.statusCode ?? null,
      clientIp: input.clientIp ?? null,
      firstTokenMs: input.firstTokenMs ?? null,
      durationMs: input.durationMs ?? null,
      costUsd: input.costUsd ?? null,
      createdAt: now
    })
  }

  return {
    rowsByShard,
    shardEntries,
    locations: [...rowsByShard.values()].map((entry) => entry.location)
  }
}

function systemAccountIdFromUsageInput(
  input: UsageRecordInput,
  accessLookupContext: ReturnType<typeof buildUsageAccessLookupContext> | undefined,
  providedOnly: boolean
): string {
  if (input.systemAccountId) return input.systemAccountId
  if (providedOnly) {
    throw new Error('PostgreSQL 使用记录写入必须提供 systemAccountId')
  }
  return systemAccountIdForUsage(input, accessLookupContext)
}

function usageAccessMetadataFromProvidedInput(input: UsageRecordInput & { systemAccountId: string }): ReturnType<typeof usageAccessMetadata> {
  return {
    accountOwnerSystemAccountId: input.accountOwnerSystemAccountId,
    groupOwnerSystemAccountId: input.groupOwnerSystemAccountId,
    accountAccessType: input.accountAccessType,
    groupAccessType: input.groupAccessType,
    accountAuthorizationId: input.accountAccessType === 'account_authorized' ? input.accountAuthorizationId : undefined,
    accountAuthorizationSourceType: input.accountAccessType === 'account_authorized' ? input.accountAuthorizationSourceType : undefined,
    accountAuthorizationSourceTeamId: input.accountAccessType === 'account_authorized' ? input.accountAuthorizationSourceTeamId : undefined,
    groupAuthorizationId: input.groupAuthorizationId,
    groupAuthorizationSourceType: input.groupAuthorizationId ? input.groupAuthorizationSourceType : undefined,
    groupAuthorizationSourceTeamId: input.groupAuthorizationId ? input.groupAuthorizationSourceTeamId : undefined
  }
}

const postgresUsageRecordColumns = [
  'id',
  'system_account_id',
  'trace_id',
  'traffic_source',
  'client_ip',
  'api_key_id',
  'group_id',
  'account_id',
  'endpoint',
  'provider_code',
  'provider_protocol_profile_id',
  'usage_semantic',
  'model',
  'upstream_model',
  'pricing_model',
  'model_mapping_applied',
  'model_mapping_source',
  'source_endpoint_family',
  'upstream_endpoint_family',
  'stream',
  'status_code',
  'success',
  'failure_attribution',
  'first_token_ms',
  'duration_ms',
  'input_tokens',
  'output_tokens',
  'cache_read_tokens',
  'cache_read_cost_usd',
  'cache_write_tokens',
  'cache_write_1h_tokens',
  'cache_write_cost_usd',
  'thinking_tokens',
  'input_image_tokens',
  'output_image_tokens',
  'input_audio_tokens',
  'output_audio_tokens',
  'output_image_count',
  'cost_usd',
  'error_code',
  'error_message',
  'request_snapshot_json',
  'response_snapshot_json',
  'account_owner_system_account_id',
  'group_owner_system_account_id',
  'account_access_type',
  'group_access_type',
  'account_authorization_id',
  'account_authorization_source_type',
  'account_authorization_source_team_id',
  'group_authorization_id',
  'group_authorization_source_type',
  'group_authorization_source_team_id',
  'created_at'
] as const

async function insertPostgresUsageRecordRows(client: DatabaseClient, rows: UsageRecordShardWriteRow[]): Promise<void> {
  if (rows.length === 0) return
  await ensurePostgresUsageRecordPartitions(client, rows.map((row) => String(row.params[postgresUsageRecordColumns.indexOf('created_at')] ?? '')))
  const placeholders = rows.map(() => `(${postgresUsageRecordColumns.map(() => '?').join(', ')})`).join(', ')
  await client.execute(`
    INSERT INTO juhe_usage.usage_records (${postgresUsageRecordColumns.join(', ')})
    VALUES ${placeholders}
    ON CONFLICT(created_at, id) DO NOTHING
  `, rows.flatMap((row) => row.params))
}

async function recordPostgresUsageRecordShardEntries(
  client: DatabaseClient,
  entries: UsageRecordShardEntryInput[],
  locations: UsageRecordShardLocation[]
): Promise<void> {
  const uniqueEntries = uniquePostgresUsageRecordShardEntries(entries)
  if (uniqueEntries.length === 0) return
  const timestamp = nowIso()
  await registerPostgresUsageRecordShardLocations(client, uniquePostgresUsageRecordShardLocations([
    ...locations,
    ...uniqueEntries
      .map((entry) => usageRecordLogicalShardLocationForPostgres(entry.id, entry.createdAt))
  ]), timestamp)
  await upsertPostgresUsageRecordShardEntries(client, uniqueEntries, timestamp)
  await upsertPostgresUsageRecordScopeShardCatalog(client, uniqueEntries)
}

function usageRecordLogicalShardLocationForPostgres(id: string, createdAt?: string): UsageRecordShardLocation {
  const parsed = /^usage_(\d{8})_s(\d+)_/.exec(id)
  const bucketDateKey = parsed?.[1] ?? usageRecordBucketDateKey(createdAt)
  const shardId = parsed?.[2] ? Number(parsed[2]) : usageRecordLogicalShardId(id)
  const normalizedShardId = Number.isInteger(shardId) ? Math.max(0, Math.trunc(shardId)) : usageRecordLogicalShardId(id)
  const shardKey = `${bucketDateKey}:s${formatUsageRecordLogicalShardId(normalizedShardId)}`
  return {
    shardKey,
    bucketDate: `${bucketDateKey.slice(0, 4)}-${bucketDateKey.slice(4, 6)}-${bucketDateKey.slice(6, 8)}`,
    bucketDateKey,
    shardId: normalizedShardId,
    filePath: `postgres:juhe_usage.usage_records:${shardKey}`
  }
}

function usageRecordBucketDateKey(createdAt?: string): string {
  const value = createdAt ?? nowIso()
  const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(value)
  if (match) return `${match[1]}${match[2]}${match[3]}`
  const fallback = nowIso()
  return `${fallback.slice(0, 4)}${fallback.slice(5, 7)}${fallback.slice(8, 10)}`
}

function usageRecordLogicalShardId(value: string): number {
  let hash = 0
  for (let index = 0; index < value.length; index += 1) {
    hash = (hash * 31 + value.charCodeAt(index)) >>> 0
  }
  return hash % runtimeConfig.usageShardCount
}

function formatUsageRecordLogicalShardId(value: number): string {
  return String(Math.max(0, Math.trunc(value))).padStart(3, '0')
}

async function registerPostgresUsageRecordShardLocations(
  client: DatabaseClient,
  locations: UsageRecordShardLocation[],
  timestamp: string
): Promise<void> {
  if (locations.length === 0) return
  const columns = ['shard_key', 'bucket_date', 'shard_id', 'file_path', 'schema_version', 'status', 'first_seen_at', 'last_write_at', 'created_at', 'updated_at']
  const params = locations.flatMap((location) => [
    location.shardKey,
    location.bucketDate,
    location.shardId,
    location.filePath,
    2,
    'active',
    timestamp,
    timestamp,
    timestamp,
    timestamp
  ])
  await client.execute(`
    INSERT INTO juhe_usage.usage_record_shards (${columns.join(', ')})
    VALUES ${multiRowPlaceholders(locations.length, columns.length)}
    ON CONFLICT(shard_key) DO UPDATE SET
      last_write_at = EXCLUDED.last_write_at,
      updated_at = EXCLUDED.updated_at,
      status = 'active'
  `, params)
}

async function upsertPostgresUsageRecordShardEntries(
  client: DatabaseClient,
  entries: UsageRecordShardEntryInput[],
  timestamp: string
): Promise<void> {
  const columns = [
    'usage_id',
    'shard_key',
    'system_account_id',
    'trace_id',
    'api_key_id',
    'account_id',
    'group_id',
    'model',
    'traffic_source',
    'success',
    'status_code',
    'client_ip',
    'first_token_ms',
    'duration_ms',
    'cost_usd',
    'created_at',
    'indexed_at'
  ]
  const params = entries.flatMap((entry) => [
    entry.id,
    entry.shardKey,
    entry.systemAccountId,
    entry.traceId,
    entry.apiKeyId ?? null,
    entry.accountId ?? null,
    entry.groupId ?? null,
    entry.model ?? null,
    entry.trafficSource,
    entry.success ? 1 : 0,
    entry.statusCode ?? null,
    entry.clientIp ?? null,
    entry.firstTokenMs ?? null,
    entry.durationMs ?? null,
    entry.costUsd ?? null,
    entry.createdAt,
    timestamp
  ])
  await client.execute(`
    INSERT INTO juhe_usage.usage_record_shard_entries (${columns.join(', ')})
    VALUES ${multiRowPlaceholders(entries.length, columns.length)}
    ON CONFLICT(usage_id) DO UPDATE SET
      shard_key = EXCLUDED.shard_key,
      system_account_id = EXCLUDED.system_account_id,
      trace_id = EXCLUDED.trace_id,
      api_key_id = EXCLUDED.api_key_id,
      account_id = EXCLUDED.account_id,
      group_id = EXCLUDED.group_id,
      model = EXCLUDED.model,
      traffic_source = EXCLUDED.traffic_source,
      success = EXCLUDED.success,
      status_code = EXCLUDED.status_code,
      client_ip = EXCLUDED.client_ip,
      first_token_ms = EXCLUDED.first_token_ms,
      duration_ms = EXCLUDED.duration_ms,
      cost_usd = EXCLUDED.cost_usd,
      created_at = EXCLUDED.created_at,
      indexed_at = EXCLUDED.indexed_at
  `, params)
}

async function upsertPostgresUsageRecordScopeShardCatalog(client: DatabaseClient, entries: UsageRecordShardEntryInput[]): Promise<void> {
  const accountRows = new Map<string, { accountId: string; shardKey: string; firstCreatedAt: string; lastSeenAt: string }>()
  const apiKeyRows = new Map<string, { apiKeyId: string; systemAccountId: string; shardKey: string; firstCreatedAt: string; lastSeenAt: string }>()
  for (const entry of entries) {
    const accountId = entry.accountId?.trim()
    if (accountId) {
      const key = `${accountId}\u0000${entry.shardKey}`
      const existing = accountRows.get(key)
      if (existing) {
        if (entry.createdAt < existing.firstCreatedAt) existing.firstCreatedAt = entry.createdAt
        if (entry.createdAt > existing.lastSeenAt) existing.lastSeenAt = entry.createdAt
      } else {
        accountRows.set(key, {
          accountId,
          shardKey: entry.shardKey,
          firstCreatedAt: entry.createdAt,
          lastSeenAt: entry.createdAt
        })
      }
    }
    const apiKeyId = entry.apiKeyId?.trim()
    const systemAccountId = entry.systemAccountId.trim()
    if (apiKeyId && systemAccountId) {
      const key = `${apiKeyId}\u0000${systemAccountId}\u0000${entry.shardKey}`
      const existing = apiKeyRows.get(key)
      if (existing) {
        if (entry.createdAt < existing.firstCreatedAt) existing.firstCreatedAt = entry.createdAt
        if (entry.createdAt > existing.lastSeenAt) existing.lastSeenAt = entry.createdAt
      } else {
        apiKeyRows.set(key, {
          apiKeyId,
          systemAccountId,
          shardKey: entry.shardKey,
          firstCreatedAt: entry.createdAt,
          lastSeenAt: entry.createdAt
        })
      }
    }
  }

  if (accountRows.size > 0) {
    const columns = ['account_id', 'shard_key', 'first_created_at', 'last_seen_at']
    await client.execute(`
      INSERT INTO juhe_usage.usage_record_account_shards (${columns.join(', ')})
      VALUES ${multiRowPlaceholders(accountRows.size, columns.length)}
      ON CONFLICT(account_id, shard_key) DO UPDATE SET
        first_created_at = LEAST(usage_record_account_shards.first_created_at, EXCLUDED.first_created_at),
        last_seen_at = GREATEST(usage_record_account_shards.last_seen_at, EXCLUDED.last_seen_at)
    `, [...accountRows.values()].flatMap((row) => [row.accountId, row.shardKey, row.firstCreatedAt, row.lastSeenAt]))
  }

  if (apiKeyRows.size > 0) {
    const columns = ['api_key_id', 'system_account_id', 'shard_key', 'first_created_at', 'last_seen_at']
    await client.execute(`
      INSERT INTO juhe_usage.usage_record_api_key_shards (${columns.join(', ')})
      VALUES ${multiRowPlaceholders(apiKeyRows.size, columns.length)}
      ON CONFLICT(api_key_id, system_account_id, shard_key) DO UPDATE SET
        first_created_at = LEAST(usage_record_api_key_shards.first_created_at, EXCLUDED.first_created_at),
        last_seen_at = GREATEST(usage_record_api_key_shards.last_seen_at, EXCLUDED.last_seen_at)
    `, [...apiKeyRows.values()].flatMap((row) => [row.apiKeyId, row.systemAccountId, row.shardKey, row.firstCreatedAt, row.lastSeenAt]))
  }
}

function collectPostgresUsageRecordBusinessSideEffects(
  accountLastUsedAt: Map<string, string>,
  accountHealthSuccessAt: Map<string, string>,
  rows: UsageRecordShardWriteRow[]
): void {
  for (const row of rows) {
    if (!row.accountId) continue
    if (row.accountLastUsedAt) {
      mergePostgresMaxIsoValue(accountLastUsedAt, row.accountId, row.accountLastUsedAt)
    }
    if (row.accountHealthSuccessAt) {
      mergePostgresMaxIsoValue(accountHealthSuccessAt, row.accountId, row.accountHealthSuccessAt)
    }
  }
}

function mergePostgresMaxIsoValue(target: Map<string, string>, key: string, value: string): void {
  const previous = target.get(key)
  if (!previous || value > previous) {
    target.set(key, value)
  }
}

async function flushPostgresUsageRecordBusinessSideEffects(
  client: DatabaseClient,
  accountLastUsedAt: Map<string, string>,
  accountHealthSuccessAt: Map<string, string>
): Promise<void> {
  for (const [accountId, lastUsedAt] of accountLastUsedAt) {
    await client.execute(`
      UPDATE juhe_business.accounts
      SET last_used_at = ?, updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND (last_used_at IS NULL OR last_used_at < ?)
    `, [lastUsedAt, lastUsedAt, accountId, lastUsedAt])
  }
  for (const [accountId, successAt] of accountHealthSuccessAt) {
    await client.execute(`
      UPDATE juhe_business.accounts
      SET last_health_success_at = ?,
          health_check_failure_count = 0,
          last_health_check_error_code = NULL,
          last_health_check_error_message = NULL,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
        AND (last_health_success_at IS NULL OR last_health_success_at <= ?)
    `, [successAt, successAt, accountId, successAt])
  }
}

function multiRowPlaceholders(rowCount: number, columnCount: number): string {
  return Array.from({ length: rowCount }, () => `(${Array.from({ length: columnCount }, () => '?').join(', ')})`).join(', ')
}

function uniquePostgresUsageRecordShardEntries(entries: UsageRecordShardEntryInput[]): UsageRecordShardEntryInput[] {
  const unique = new Map<string, UsageRecordShardEntryInput>()
  for (const entry of entries) {
    const id = entry.id.trim()
    if (!id) continue
    unique.set(id, entry)
  }
  return [...unique.values()]
}

function uniquePostgresUsageRecordShardLocations(locations: UsageRecordShardLocation[]): UsageRecordShardLocation[] {
  const unique = new Map<string, UsageRecordShardLocation>()
  for (const location of locations) {
    unique.set(location.shardKey, location)
  }
  return [...unique.values()]
}

function mergeUsageRecordShardWriteResult(
  accountLastUsedAt: Map<string, string>,
  accountHealthSuccessAt: Map<string, string>,
  result: UsageRecordShardWriteResult
): void {
  mergeAccountLastUsedAt(accountLastUsedAt, new Map(result.accountLastUsedAt.map((row) => [row.accountId, row.lastUsedAt])))
  mergeAccountLastUsedAt(accountHealthSuccessAt, new Map(result.accountHealthSuccessAt.map((row) => [row.accountId, row.successAt])))
}

interface UsageRecordEntryRow {
  usage_id: string
  shard_key: string
  created_at: string
}

async function buildUsageRecordFiltersAsync(
  client: DatabaseClient,
  access: AccessScope | undefined,
  options?: UsageRecordListOptions
): Promise<UsageRecordFilterResult> {
  const accountKeyword = options?.accountKeyword?.trim()
  if (!accountKeyword) {
    return buildUsageRecordFilters(access, options, usageRecordFilterSettingsForClient(client))
  }
  const filters = buildUsageRecordFilters(access, { ...options, accountKeyword: undefined }, usageRecordFilterSettingsForClient(client))
  const accountIds = await postgresAccountIdsForKeyword(client, accountKeyword, access)
  const accountClause = accountIds.length
    ? `ur.account_id IN (${sqlPlaceholders(accountIds.length)})`
    : '1 = 0'
  return {
    clause: filters.clause ? `${filters.clause} AND ${accountClause}` : `WHERE ${accountClause}`,
    params: [...filters.params, ...accountIds],
    tracePrefixLookup: filters.tracePrefixLookup
  }
}

function usageRecordFilterSettingsForClient(client: DatabaseClient): UsageRecordFilterSettings | undefined {
  return client.driver === 'postgres'
    ? { textPrefixCollation: '"C"', textPrefixUpperBoundMode: 'binary' }
    : undefined
}

async function postgresAccountIdsForKeyword(client: DatabaseClient, keyword: string, access?: AccessScope): Promise<string[]> {
  const ownerSystemAccountId = scopedSystemAccountId(access)
  const normalizedKeyword = normalizeUsageRecordAccountKeyword(keyword)
  const upperBound = usageRecordAccountKeywordUpperBound(normalizedKeyword)
  const ids: string[] = []
  const accountNameExpression = '(accounts.name COLLATE "C")'
  const sourceNameExpression = '(source_accounts.name COLLATE "C")'
  const accountNameClause = `${accountNameExpression} >= ? AND ${accountNameExpression} < ?`
  const sourceNameClause = `${sourceNameExpression} >= ? AND ${sourceNameExpression} < ?`
  const ownerClause = ownerSystemAccountId ? 'AND accounts.system_account_id = ?' : ''
  const ownerParams = ownerSystemAccountId ? [ownerSystemAccountId] : []

  appendUsageRecordAccountIds(ids, await client.query<{ id?: string }>(`
    SELECT accounts.id
    FROM juhe_business.accounts accounts
    WHERE accounts.deleted_at IS NULL
      AND ${accountNameClause}
      ${ownerClause}
    ORDER BY ${accountNameExpression} ASC, accounts.id ASC
    LIMIT ?
  `, [normalizedKeyword, upperBound, ...ownerParams, usageRecordAccountKeywordMatchLimit]))

  appendUsageRecordAccountIds(ids, await client.query<{ id?: string }>(`
    SELECT instance_accounts.id
    FROM juhe_business.accounts source_accounts
    INNER JOIN juhe_business.accounts instance_accounts
      ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
    WHERE source_accounts.deleted_at IS NULL
      AND instance_accounts.deleted_at IS NULL
      AND ${sourceNameClause}
      ${ownerSystemAccountId ? 'AND instance_accounts.system_account_id = ?' : ''}
    ORDER BY ${sourceNameExpression} ASC, instance_accounts.id ASC
    LIMIT ?
  `, [normalizedKeyword, upperBound, ...ownerParams, usageRecordAccountKeywordMatchLimit]))

  if (ownerSystemAccountId) {
    appendUsageRecordAccountIds(ids, await client.query<{ id?: string }>(`
      SELECT accounts.id
      FROM juhe_business.accounts accounts
      INNER JOIN juhe_business.resource_authorizations ra
        ON ra.resource_type = 'account'
        AND ra.resource_id = accounts.id
        AND ra.grantee_system_account_id = ?
      WHERE accounts.deleted_at IS NULL
        AND ${accountNameClause}
      ORDER BY ${accountNameExpression} ASC, accounts.id ASC
      LIMIT ?
    `, [ownerSystemAccountId, normalizedKeyword, upperBound, usageRecordAccountKeywordMatchLimit]))
    appendUsageRecordAccountIds(ids, await client.query<{ id?: string }>(`
      SELECT accounts.id
      FROM juhe_business.accounts accounts
      INNER JOIN juhe_business.group_accounts ga
        ON ga.account_id = accounts.id
        AND ga.enabled = 1
      INNER JOIN juhe_business.resource_authorizations ra
        ON ra.resource_type = 'group'
        AND ra.resource_id = ga.group_id
        AND ra.grantee_system_account_id = ?
      WHERE accounts.deleted_at IS NULL
        AND ${accountNameClause}
      ORDER BY ${accountNameExpression} ASC, accounts.id ASC
      LIMIT ?
    `, [ownerSystemAccountId, normalizedKeyword, upperBound, usageRecordAccountKeywordMatchLimit]))
  }

  return ids.slice(0, usageRecordAccountKeywordMatchLimit)
}

function appendUsageRecordAccountIds(target: string[], rows: Array<{ id?: string }>): void {
  const seen = new Set(target)
  for (const row of rows) {
    if (!row.id || seen.has(row.id) || target.length >= usageRecordAccountKeywordMatchLimit) continue
    target.push(row.id)
    seen.add(row.id)
  }
}

function usageRecordTextPrefixUpperBound(value: string): string {
  return `${value}\uffff`
}

function normalizeUsageRecordAccountKeyword(value: string): string {
  return value.normalize('NFKC').trim()
}

function usageRecordAccountKeywordUpperBound(value: string): string {
  return usageRecordBinaryPrefixUpperBound(value)
}

function usageRecordBinaryPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\u{10ffff}`
}

function listUsageRecordEntries(
  filters: UsageRecordFilterResult,
  orderClause: string,
  limit: number
): UsageRecordEntryRow[] {
  return getUsageCatalogDatabase()
    .prepare(`
      SELECT usage_id, shard_key, created_at
      FROM usage_record_shard_entries ue
      ${filters.clause}
      ${orderClause}
      LIMIT ?
    `)
    .all(...filters.params, Math.max(1, Math.trunc(limit))) as unknown as UsageRecordEntryRow[]
}

async function listUsageRecordEntriesAsync(
  client: DatabaseClient,
  filters: UsageRecordFilterResult,
  orderClause: string,
  limit: number
): Promise<UsageRecordEntryRow[]> {
  return await client.query<UsageRecordEntryRow>(`
    SELECT usage_id, shard_key, created_at
    FROM juhe_usage.usage_record_shard_entries ue
    ${filters.clause}
    ${orderClause}
    LIMIT ?
  `, [...filters.params, Math.max(1, Math.trunc(limit))])
}

function loadUsageRecordRowsByEntries(entries: UsageRecordEntryRow[]): UsageRecordRow[] {
  if (entries.length === 0) return []
  const rowsById = new Map<string, UsageRecordRow>()
  const entriesByShardKey = new Map<string, UsageRecordEntryRow[]>()
  for (const entry of entries) {
    entriesByShardKey.set(entry.shard_key, [...(entriesByShardKey.get(entry.shard_key) ?? []), entry])
  }
  for (const [shardKey, shardEntries] of entriesByShardKey) {
    const location = findRegisteredUsageRecordShardLocation(shardKey)
    if (!location) continue
    const shardDatabase = getUsageRecordShardDatabase(location)
    for (const chunk of chunkValues(shardEntries.map((entry) => entry.usage_id), 900)) {
      const rows = shardDatabase
        .prepare(`
          SELECT
            ur.id,
            ur.system_account_id,
            ur.trace_id,
            ur.traffic_source,
            ur.client_ip,
            ur.api_key_id,
            ur.group_id,
            ur.account_id,
            ur.endpoint,
            ur.provider_code,
            ur.provider_protocol_profile_id,
            ur.usage_semantic,
            ur.model,
            ur.upstream_model,
            ur.pricing_model,
            ur.model_mapping_applied,
            ur.model_mapping_source,
            ur.source_endpoint_family,
            ur.upstream_endpoint_family,
            ur.stream,
            ur.status_code,
            ur.success,
            ur.failure_attribution,
            ur.first_token_ms,
            ur.duration_ms,
            ur.input_tokens,
            ur.output_tokens,
            ur.cache_read_tokens,
            ur.cache_read_cost_usd,
            ur.cache_write_tokens,
            ur.cache_write_1h_tokens,
            ur.cache_write_cost_usd,
            ur.thinking_tokens,
            ur.input_image_tokens,
            ur.output_image_tokens,
            ur.input_audio_tokens,
            ur.output_audio_tokens,
            ur.output_image_count,
            ur.cost_usd,
            ur.error_code,
            ur.error_message,
            ur.created_at
          FROM usage_records ur
          WHERE ur.id IN (${sqlPlaceholders(chunk.length)})
        `)
        .all(...chunk) as UsageRecordRow[]
      for (const row of rows) {
        rowsById.set(String(row.id), row)
      }
    }
  }
  return entries.map((entry) => rowsById.get(entry.usage_id)).filter((row): row is UsageRecordRow => Boolean(row))
}

async function loadUsageRecordRowsByEntriesAsync(client: DatabaseClient, entries: UsageRecordEntryRow[]): Promise<UsageRecordRow[]> {
  if (entries.length === 0) return []
  const rowsById = new Map<string, UsageRecordRow>()
  for (const chunk of chunkValues(entries.map((entry) => entry.usage_id), 900)) {
    const rows = await client.query<UsageRecordRow>(`
      SELECT
        ur.id,
        ur.system_account_id,
        ur.trace_id,
        ur.traffic_source,
        ur.client_ip,
        ur.api_key_id,
        ur.group_id,
        ur.account_id,
        ur.endpoint,
        ur.provider_code,
        ur.provider_protocol_profile_id,
        ur.usage_semantic,
        ur.model,
        ur.upstream_model,
        ur.pricing_model,
        ur.model_mapping_applied,
        ur.model_mapping_source,
        ur.source_endpoint_family,
        ur.upstream_endpoint_family,
        ur.stream,
        ur.status_code,
        ur.success,
        ur.failure_attribution,
        ur.first_token_ms,
        ur.duration_ms,
        ur.input_tokens,
        ur.output_tokens,
        ur.cache_read_tokens,
        ur.cache_read_cost_usd,
        ur.cache_write_tokens,
        ur.cache_write_1h_tokens,
        ur.cache_write_cost_usd,
        ur.thinking_tokens,
        ur.input_image_tokens,
        ur.output_image_tokens,
        ur.input_audio_tokens,
        ur.output_audio_tokens,
        ur.output_image_count,
        ur.cost_usd,
        ur.error_code,
        ur.error_message,
        ur.created_at
      FROM juhe_usage.usage_records ur
      WHERE ur.id IN (${sqlPlaceholders(chunk.length)})
    `, chunk)
    for (const row of rows) {
      rowsById.set(String(row.id), row)
    }
  }
  return entries.map((entry) => rowsById.get(entry.usage_id)).filter((row): row is UsageRecordRow => Boolean(row))
}

async function listPostgresUsageRecordRows(
  client: DatabaseClient,
  filters: UsageRecordFilterResult,
  orderClause: string,
  limit: number
): Promise<UsageRecordRow[]> {
  const normalizedLimit = Math.max(1, Math.trunc(limit))
  const selectColumns = `
      ur.id,
      ur.system_account_id,
      ur.trace_id,
      ur.traffic_source,
      ur.client_ip,
      ur.api_key_id,
      ur.group_id,
      ur.account_id,
      ur.endpoint,
      ur.provider_code,
      ur.provider_protocol_profile_id,
      ur.usage_semantic,
      ur.model,
      ur.upstream_model,
      ur.pricing_model,
      ur.model_mapping_applied,
      ur.model_mapping_source,
      ur.source_endpoint_family,
      ur.upstream_endpoint_family,
      ur.stream,
      ur.status_code,
      ur.success,
      ur.failure_attribution,
      ur.first_token_ms,
      ur.duration_ms,
      ur.input_tokens,
      ur.output_tokens,
      ur.cache_read_tokens,
      ur.cache_read_cost_usd,
      ur.cache_write_tokens,
      ur.cache_write_1h_tokens,
      ur.cache_write_cost_usd,
      ur.thinking_tokens,
      ur.input_image_tokens,
      ur.output_image_tokens,
      ur.input_audio_tokens,
      ur.output_audio_tokens,
      ur.output_image_count,
      ur.cost_usd,
      ur.error_code,
      ur.error_message,
      ur.created_at`
  if (filters.tracePrefixLookup) {
    return await client.query<UsageRecordRow>(`
      WITH matched_usage_records AS MATERIALIZED (
        SELECT ur.id, ur.created_at
        FROM juhe_usage.usage_records ur
        ${filters.clause}
        ${orderClause}
        LIMIT ?
      )
      SELECT
        ${selectColumns}
      FROM matched_usage_records matched_usage_records
      INNER JOIN juhe_usage.usage_records ur
        ON ur.created_at = matched_usage_records.created_at
        AND ur.id = matched_usage_records.id
      ${orderClause}
      LIMIT ?
    `, [...filters.params, normalizedLimit, normalizedLimit])
  }
  return await client.query<UsageRecordRow>(`
    SELECT
      ${selectColumns}
    FROM juhe_usage.usage_records ur
    ${filters.clause}
    ${orderClause}
    LIMIT ?
  `, [...filters.params, normalizedLimit])
}

function listUsageRecordRowsFromShards(
  filters: UsageRecordFilterResult,
  listOptions: NormalizedUsageRecordListOptions,
  orderClause: string,
  perShardLimit: number,
  window: UsageRecordShardQueryWindow = {}
): UsageRecordRow[] {
  const locations = listUsageRecordShardLocations(window)
  const rows: UsageRecordRow[] = []
  for (const location of locations) {
    const shardDatabase = getUsageRecordShardDatabase(location)
    rows.push(...shardDatabase
      .prepare(`
        SELECT
          ur.id,
          ur.system_account_id,
          ur.trace_id,
          ur.traffic_source,
          ur.client_ip,
          ur.api_key_id,
          ur.group_id,
          ur.account_id,
          ur.endpoint,
          ur.provider_code,
          ur.provider_protocol_profile_id,
          ur.usage_semantic,
          ur.model,
          ur.upstream_model,
          ur.pricing_model,
          ur.model_mapping_applied,
          ur.model_mapping_source,
          ur.source_endpoint_family,
          ur.upstream_endpoint_family,
          ur.stream,
          ur.status_code,
          ur.success,
          ur.failure_attribution,
          ur.first_token_ms,
          ur.duration_ms,
          ur.input_tokens,
          ur.output_tokens,
          ur.cache_read_tokens,
          ur.cache_read_cost_usd,
          ur.cache_write_tokens,
          ur.cache_write_1h_tokens,
          ur.cache_write_cost_usd,
          ur.thinking_tokens,
          ur.input_image_tokens,
          ur.output_image_tokens,
          ur.input_audio_tokens,
          ur.output_audio_tokens,
          ur.output_image_count,
          ur.cost_usd,
          ur.error_code,
          ur.error_message,
          ur.created_at
        FROM usage_records ur
        ${filters.clause}
        ${orderClause}
        LIMIT ?
      `)
      .all(...filters.params, perShardLimit) as UsageRecordRow[])
  }
  return rows.sort((left, right) => compareUsageRecordRows(left, right, listOptions)).slice(0, perShardLimit)
}

function usageRecordShardQueryWindowFromOptions(options?: UsageRecordListOptions): UsageRecordShardQueryWindow {
  return {
    startAt: options?.startAt,
    endAt: options?.endAt
  }
}

function compareUsageRecordRows(left: UsageRecordRow, right: UsageRecordRow, options: NormalizedUsageRecordListOptions): number {
  const direction = options.sortOrder === 'asc' ? 1 : -1
  if (options.sortBy !== 'createdAt') {
    const byRequestedField = compareNullableValues(
      usageRecordSortValue(left, options.sortBy),
      usageRecordSortValue(right, options.sortBy),
      direction
    )
    if (byRequestedField !== 0) return byRequestedField
  }
  const byCreatedAt = String(left.created_at ?? '').localeCompare(String(right.created_at ?? '')) * direction
  if (byCreatedAt !== 0) return byCreatedAt
  return String(left.id ?? '').localeCompare(String(right.id ?? '')) * direction
}

function usageRecordSortValue(row: UsageRecordRow, sortBy: UsageRecordSortField): string | number | null | undefined {
  if (sortBy === 'firstTokenMs') return sortableUsageRecordValue(row.first_token_ms)
  if (sortBy === 'durationMs') return sortableUsageRecordValue(row.duration_ms)
  if (sortBy === 'costUsd') return sortableUsageRecordValue(row.cost_usd)
  return sortableUsageRecordValue(row.created_at)
}

function sortableUsageRecordValue(value: unknown): string | number | null | undefined {
  if (value === null || value === undefined) return value
  if (typeof value === 'number' || typeof value === 'string') return value
  return undefined
}

function compareNullableValues(left: string | number | null | undefined, right: string | number | null | undefined, direction: 1 | -1): number {
  const leftMissing = left === null || left === undefined
  const rightMissing = right === null || right === undefined
  if (leftMissing && rightMissing) return 0
  if (leftMissing) return -1 * direction
  if (rightMissing) return 1 * direction
  if (typeof left === 'number' && typeof right === 'number') {
    return left === right ? 0 : left > right ? direction : -direction
  }
  return String(left).localeCompare(String(right)) * direction
}

function mergeAccountLastUsedAt(target: Map<string, string>, source: Map<string, string>): void {
  for (const [accountId, lastUsedAt] of source) {
    const previous = target.get(accountId)
    if (!previous || lastUsedAt > previous) {
      target.set(accountId, lastUsedAt)
    }
  }
}

function flushUsageRecordBusinessSideEffects(
  accountLastUsedAt: Map<string, string>,
  accountHealthSuccessAt: Map<string, string>,
  database: ReturnType<typeof getBusinessDatabase>
): void {
  if (accountLastUsedAt.size === 0 && accountHealthSuccessAt.size === 0) return
  if (mainDatabaseRuntimeInfo('business').queryOnly) return
  try {
    updateAccountLastUsedAt(accountLastUsedAt, database)
    recordAccountHealthSuccessSignals(accountHealthSuccessAt)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'usage_record_business_side_effect_flush_failed',
      accountLastUsedCount: accountLastUsedAt.size,
      accountHealthSuccessCount: accountHealthSuccessAt.size
    }), '使用记录业务副作用写入失败，已保留使用记录落库结果')
  }
}

function updateAccountLastUsedAt(accountLastUsedAt: Map<string, string>, database: ReturnType<typeof getBusinessDatabase>): void {
  if (accountLastUsedAt.size === 0) return
  const updateAccountStatement = database.prepare(`
    UPDATE accounts
    SET last_used_at = ?, updated_at = ?
    WHERE id = ?
      AND deleted_at IS NULL
      AND (last_used_at IS NULL OR last_used_at < ?)
  `)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const [accountId, lastUsedAt] of accountLastUsedAt) {
      updateAccountStatement.run(lastUsedAt, lastUsedAt, accountId, lastUsedAt)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function normalizeUsageRecordTrafficSource(value: unknown): UsageRecordTrafficSource {
  if (
    value === 'gateway'
    || value === 'manual_account_test'
    || value === 'account_health_check'
    || value === 'runtime_recovery_probe'
    || value === 'cooldown_retest'
    || value === 'hybrid_scoring'
    || value === 'hybrid_quality_scoring'
  ) {
    return value
  }
  throw new Error('使用记录来源无效')
}

function usageFailureAttributionForInput(input: UsageRecordInput): UsageFailureAttribution | null {
  if (input.success) {
    return null
  }
  if (input.failureAttribution) {
    return normalizeUsageFailureAttribution(input.failureAttribution)
  }
  return input.accountId ? 'account_upstream' : 'gateway_policy'
}

function normalizeUsageFailureAttribution(value: unknown): UsageFailureAttribution {
  if (
    value === 'account_upstream'
    || value === 'account_dependency'
    || value === 'gateway_capacity'
    || value === 'gateway_policy'
    || value === 'client_lifecycle'
  ) {
    return value
  }
  throw new Error('使用记录失败归因无效')
}

function shouldRecordAccountUsageSideEffects(trafficSource: UsageRecordTrafficSource): boolean {
  return trafficSource !== 'manual_account_test'
    && trafficSource !== 'account_health_check'
    && trafficSource !== 'runtime_recovery_probe'
    && trafficSource !== 'cooldown_retest'
    && trafficSource !== 'hybrid_scoring'
    && trafficSource !== 'hybrid_quality_scoring'
}

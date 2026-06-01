import {
  latestClientIpStatsLagSeconds,
  listClientIpStats,
  type ClientIpStatsSortField,
  type ClientIpUsageSummary
} from '../../storage/client-ip-stats.repository.js'
import { getBusinessDatabase, getStatsDatabase } from '../../storage/database.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders } from '../../storage/query-utils.js'
import {
  dateKey,
  normalizeAccountUsageStatsRange,
  usageStatsTimezone
} from '../../storage/usage-stats-helpers.js'
import { latestUsageStatsLagSeconds } from '../../storage/usage-stats.repository.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from '../../storage/usage-stats-types.js'

export type PublicWelfareRangePreset = 'today' | 'last7d' | 'last31d'
export type PublicClientIpUsageSortField = ClientIpStatsSortField
export type PublicAccountUsageSortField = 'requestCount' | 'successCount' | 'errorCount' | 'errorRate' | 'totalTokens' | 'totalCost' | 'activeDays' | 'lastUsedAt'
export type PublicConsumptionRankingMetric = 'totalTokens' | 'totalCost' | 'requestCount'
export type PublicWelfareDataSource = 'stats' | 'mock'

export interface PublicClientIpUsageQuery {
  range?: PublicWelfareRangePreset
  page?: number
  pageSize?: number
  keyword?: string
  sortField?: PublicClientIpUsageSortField
  sortOrder?: 'asc' | 'desc'
}

export interface PublicAccountUsageQuery {
  range?: PublicWelfareRangePreset
  page?: number
  pageSize?: number
  keyword?: string
  sortField?: PublicAccountUsageSortField
  sortOrder?: 'asc' | 'desc'
}

export interface PublicConsumptionRankingQuery {
  range?: PublicWelfareRangePreset
  limit?: number
  metric?: PublicConsumptionRankingMetric
}

export interface PublicWelfareRange {
  preset: PublicWelfareRangePreset
  label: string
  startDate: string
  endDate: string
  days: number
  maxDays: number
}

export interface PublicClientIpUsageItem {
  rank: number
  dimension: 'client_ip'
  ip: string
  requestCount: number
  successCount: number
  errorCount: number
  errorRate: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheRate: number
  totalTokens: number
  totalCost: number
  cacheReadCost: number
  activeDays: number
  averageFirstTokenMs?: number
  averageDurationMs?: number
  maxDurationMs?: number
  lastUsedAt?: string
  lastErrorAt?: string
}

export interface PublicAccountUsageItem {
  rank: number
  dimension: 'account'
  accountId: string
  accountName: string
  providerCode?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  type?: string
  status?: string
  requestCount: number
  successCount: number
  errorCount: number
  errorRate: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheRate: number
  totalTokens: number
  totalCost: number
  cacheReadCost: number
  activeDays: number
  averageFirstTokenMs?: number
  averageDurationMs?: number
  maxDurationMs?: number
  lastUsedAt?: string
  lastErrorAt?: string
}

export interface PublicClientIpUsageResponse {
  source: PublicWelfareDataSource
  generatedAt: string
  statsLagSeconds?: number
  range: PublicWelfareRange
  rangeReady: boolean
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicClientIpUsageItem[]
}

export interface PublicAccountUsageResponse {
  source: PublicWelfareDataSource
  generatedAt: string
  statsLagSeconds?: number
  range: PublicWelfareRange
  rangeReady: boolean
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicAccountUsageItem[]
}

export interface PublicConsumptionRankingItem extends PublicClientIpUsageItem {
  id: string
  name: string
  metricValue: number
}

export interface PublicConsumptionRankingResponse {
  source: PublicWelfareDataSource
  generatedAt: string
  statsLagSeconds?: number
  dimension: 'client_ip'
  metric: PublicConsumptionRankingMetric
  range: PublicWelfareRange
  rangeReady: boolean
  items: PublicConsumptionRankingItem[]
}

export interface PublicAccessInfoResponse {
  source: PublicWelfareDataSource
  generatedAt: string
  publicApiPrefix: '/__aipublic__'
  dataDimension: 'client_ip'
  supportedDimensions: Array<'client_ip' | 'account'>
  authType: 'Bearer'
  supportedRanges: PublicWelfareRangePreset[]
  supportedMetrics: PublicConsumptionRankingMetric[]
  endpoints: Array<{
    method: 'GET' | 'POST'
    path: string
    description: string
  }>
  boundary: {
    provides: string[]
    notProvided: string[]
  }
}

const dayMs = 24 * 60 * 60 * 1000

interface AccountUsageRangeRow {
  scope_id: string
  request_count: number
  success_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  total_cost_usd: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max: number
  first_token_ms_sum: number
  first_token_ms_count: number
  first_token_ms_max: number
  active_days: number
  last_used_at: string | null
  last_error_at: string | null
}

interface AccountUsageMetadataRow {
  id: string
  name: string
  provider_code: string
  system_account_id: string
  owner_system_account_name: string | null
  type: string
  status: string
}

const mockRows: Array<{ ip: string; usage: ClientIpUsageSummary }> = [
  {
    ip: '203.0.113.10',
    usage: {
      requestCount: 1280,
      successCount: 1252,
      errorCount: 28,
      errorRate: 28 / 1280,
      inputTokens: 516000,
      outputTokens: 326000,
      cacheReadTokens: 126000,
      cacheReadCost: 0.42,
      totalTokens: 842000,
      totalCost: 12.36,
      activeDays: 7,
      averageFirstTokenMs: 820,
      averageDurationMs: 3160,
      maxDurationMs: 12880,
      lastUsedAt: '2026-05-30T00:00:00.000Z'
    }
  },
  {
    ip: '198.51.100.25',
    usage: {
      requestCount: 936,
      successCount: 914,
      errorCount: 22,
      errorRate: 22 / 936,
      inputTokens: 317400,
      outputTokens: 214000,
      cacheReadTokens: 68420,
      cacheReadCost: 0.31,
      totalTokens: 531400,
      totalCost: 8.42,
      activeDays: 6,
      averageFirstTokenMs: 940,
      averageDurationMs: 3440,
      maxDurationMs: 14210,
      lastUsedAt: '2026-05-30T00:00:00.000Z'
    }
  },
  {
    ip: '192.0.2.66',
    usage: {
      requestCount: 648,
      successCount: 631,
      errorCount: 17,
      errorRate: 17 / 648,
      inputTokens: 184200,
      outputTokens: 120600,
      cacheReadTokens: 38200,
      cacheReadCost: 0.18,
      totalTokens: 304800,
      totalCost: 5.18,
      activeDays: 5,
      averageFirstTokenMs: 760,
      averageDurationMs: 2810,
      maxDurationMs: 10140,
      lastUsedAt: '2026-05-30T00:00:00.000Z'
    }
  }
]

const mockAccountRows: Array<{ accountId: string; accountName: string; providerCode: string; ownerSystemAccountId: string; usage: AccountUsageRangeRow }> = [
  {
    accountId: 'acc_mock_public_welfare_main',
    accountName: '公益体验入口',
    providerCode: 'openai',
    ownerSystemAccountId: 'sysacc_mock',
    usage: mockAccountUsageRow('acc_mock_public_welfare_main', 1280, 1252, 28, 516000, 326000, 126000, 12.36, 7)
  },
  {
    accountId: 'acc_mock_public_welfare_backup',
    accountName: '校园社群入口',
    providerCode: 'openai',
    ownerSystemAccountId: 'sysacc_mock',
    usage: mockAccountUsageRow('acc_mock_public_welfare_backup', 936, 914, 22, 317400, 214000, 68420, 8.42, 6)
  },
  {
    accountId: 'acc_mock_public_welfare_test',
    accountName: '志愿者测试入口',
    providerCode: 'openai',
    ownerSystemAccountId: 'sysacc_mock',
    usage: mockAccountUsageRow('acc_mock_public_welfare_test', 648, 631, 17, 184200, 120600, 38200, 5.18, 5)
  }
]

export function getPublicClientIpUsage(input: PublicClientIpUsageQuery = {}, options: { mock?: boolean } = {}): PublicClientIpUsageResponse {
  const range = resolvePublicRange(input)
  const page = boundedInteger(input.page, 1, 1000, 1)
  const pageSize = boundedInteger(input.pageSize, 1, 100, 20)
  if (options.mock) {
    const offset = (page - 1) * pageSize
    const items = mockRows.slice(offset, offset + pageSize).map((row, index) => mapPublicClientIpUsageItem(row.ip, row.usage, offset + index + 1))
    return {
      source: 'mock',
      generatedAt: new Date().toISOString(),
      statsLagSeconds: 0,
      range,
      rangeReady: true,
      page,
      pageSize,
      pageUpperBound: offset + items.length + (mockRows.length > offset + pageSize ? 1 : 0),
      hasMore: mockRows.length > offset + pageSize,
      items
    }
  }

  const result = listClientIpStats({
    page,
    pageSize,
    keyword: input.keyword,
    startDate: range.startDate,
    endDate: range.endDate,
    sortField: input.sortField,
    sortOrder: input.sortOrder
  })
  const offset = (result.page - 1) * result.pageSize
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    statsLagSeconds: latestClientIpStatsLagSeconds(),
    range: {
      ...range,
      startDate: result.range.startDate,
      endDate: result.range.endDate,
      days: result.range.days,
      maxDays: result.range.maxDays
    },
    rangeReady: result.rangeReady,
    page: result.page,
    pageSize: result.pageSize,
    pageUpperBound: result.pageUpperBound,
    hasMore: result.hasMore,
    items: result.items.map((item, index) => mapPublicClientIpUsageItem(item.aggregateIpKey, item.rangeUsage, offset + index + 1))
  }
}

export function getPublicAccountUsage(input: PublicAccountUsageQuery, options: { mock?: boolean } = {}): PublicAccountUsageResponse {
  const range = resolvePublicRange(input)
  const page = boundedInteger(input.page, 1, 1000, 1)
  const pageSize = boundedInteger(input.pageSize, 1, 100, 20)
  if (options.mock) {
    const offset = (page - 1) * pageSize
    const filteredRows = filterMockAccountRows(input.keyword)
    const items = filteredRows
      .slice(offset, offset + pageSize)
      .map((row, index) => mapPublicAccountUsageItem({
        ...row.usage,
        accountName: row.accountName,
        providerCode: row.providerCode,
        ownerSystemAccountId: row.ownerSystemAccountId,
        ownerSystemAccountName: '内置测试来源'
      }, offset + index + 1))
    return {
      source: 'mock',
      generatedAt: new Date().toISOString(),
      statsLagSeconds: 0,
      range,
      rangeReady: true,
      page,
      pageSize,
      pageUpperBound: offset + items.length + (filteredRows.length > offset + pageSize ? 1 : 0),
      hasMore: filteredRows.length > offset + pageSize,
      items
    }
  }

  const result = listPublicAccountUsageStats({
    range,
    page,
    pageSize,
    keyword: input.keyword,
    sortField: input.sortField,
    sortOrder: input.sortOrder
  })
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    statsLagSeconds: latestUsageStatsLagSeconds(),
    range,
    rangeReady: result.rangeReady,
    page: result.page,
    pageSize: result.pageSize,
    pageUpperBound: result.pageUpperBound,
    hasMore: result.hasMore,
    items: result.items
  }
}

export function getPublicConsumptionRanking(input: PublicConsumptionRankingQuery = {}, options: { mock?: boolean } = {}): PublicConsumptionRankingResponse {
  const metric = input.metric ?? 'totalTokens'
  const usage = getPublicClientIpUsage({
    range: input.range,
    page: 1,
    pageSize: boundedInteger(input.limit, 1, 100, 20),
    sortField: consumptionMetricSortField(metric),
    sortOrder: 'desc'
  }, options)
  return {
    source: usage.source,
    generatedAt: usage.generatedAt,
    statsLagSeconds: usage.statsLagSeconds,
    dimension: 'client_ip',
    metric,
    range: usage.range,
    rangeReady: usage.rangeReady,
    items: usage.items.map((item) => ({
      ...item,
      id: `ip:${item.ip}`,
      name: item.ip,
      metricValue: consumptionMetricValue(item, metric)
    }))
  }
}

export function getPublicAccessInfo(options: { mock?: boolean } = {}): PublicAccessInfoResponse {
  return {
    source: options.mock ? 'mock' : 'stats',
    generatedAt: new Date().toISOString(),
    publicApiPrefix: '/__aipublic__',
    dataDimension: 'client_ip',
    supportedDimensions: ['client_ip', 'account'],
    authType: 'Bearer',
    supportedRanges: ['today', 'last7d', 'last31d'],
    supportedMetrics: ['totalTokens', 'totalCost', 'requestCount'],
    endpoints: [
      {
        method: 'GET',
        path: '/__aipublic__/ip/usage',
        description: '读取 IP 维度用量聚合列表。'
      },
      {
        method: 'GET',
        path: '/__aipublic__/account/usage',
        description: '读取账号维度实际用量聚合列表，用于公益站按已登记 sub2apiAccountId 生成贡献榜。'
      },
      {
        method: 'GET',
        path: '/__aipublic__/consumption/ranking',
        description: '读取基于 IP 维度聚合的消耗排行。'
      },
      {
        method: 'GET',
        path: '/__aipublic__/access/info',
        description: '读取公开接口接入边界和可用指标。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/account/add',
        description: '新增账号到指定系统用户和分组。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/account/update',
        description: '修改指定系统用户和分组内的账号。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/account/del',
        description: '删除指定系统用户和分组内的账号。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/group/add',
        description: '新增指定系统用户下的分组。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/group/update',
        description: '修改指定系统用户下的分组。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/group/del',
        description: '删除指定系统用户下的分组。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/api-key/add',
        description: '新增指定系统用户下的 API Key。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/api-key/update',
        description: '修改指定系统用户下的 API Key。'
      },
      {
        method: 'POST',
        path: '/__aipublic__/api-key/del',
        description: '删除指定系统用户下的 API Key。'
      }
    ],
    boundary: {
      provides: [
        '来源系统 Bearer token 鉴权',
        'IP 维度请求数、Token、缓存、成本、活跃天数和速度指标聚合',
        '账号维度实际请求数、Token、缓存、成本、活跃天数和速度指标聚合',
        '基于 IP 聚合表的消耗排行便利视图',
        '分组、API Key 和账号的受控新增、修改与删除入口'
      ],
      notProvided: [
        '公益站用户维度排行榜快照',
        'IP 到公益站用户、账号到公益站登记人的业务归属',
        '公益站公网 IP 拦截或访问频率控制',
        '普通 API Key、上游账号凭据或内部授权关系'
      ]
    }
  }
}

function resolvePublicRange(input: { range?: PublicWelfareRangePreset }): PublicWelfareRange {
  const timezone = usageStatsTimezone()
  const preset = input.range ?? 'today'
  const today = new Date()
  const days = preset === 'last31d' ? 31 : preset === 'last7d' ? 7 : 1
  const startDate = dateKey(new Date(today.getTime() - (days - 1) * dayMs), timezone)
  const endDate = dateKey(today, timezone)
  const range = normalizeAccountUsageStatsRange({ startDate, endDate }, timezone)
  return {
    preset,
    label: publicRangeLabel(preset),
    ...range
  }
}

function publicRangeLabel(preset: PublicWelfareRange['preset']): string {
  switch (preset) {
    case 'last31d':
      return '最近31天'
    case 'last7d':
      return '最近7天'
    case 'today':
    default:
      return '今日'
  }
}

function listPublicAccountUsageStats(input: {
  range: PublicWelfareRange
  page: number
  pageSize: number
  keyword?: string
  sortField?: PublicAccountUsageSortField
  sortOrder?: 'asc' | 'desc'
}): Pick<PublicAccountUsageResponse, 'items' | 'page' | 'pageSize' | 'pageUpperBound' | 'hasMore' | 'rangeReady'> {
  const database = getStatsDatabase()
  const pageSize = boundedInteger(input.pageSize, 1, 100, 20)
  const page = boundedInteger(input.page, 1, 1000, 1)
  const rangeReady = publicAccountUsageRangeReady(database, input.range)
  if (!rangeReady) {
    return {
      items: [],
      page,
      pageSize,
      pageUpperBound: 0,
      hasMore: false,
      rangeReady
    }
  }

  const keyword = input.keyword?.trim()
  const accountIds = keyword ? loadPublicAccountUsageKeywordAccountIds(keyword) : undefined
  if (keyword && accountIds?.length === 0) {
    return {
      items: [],
      page,
      pageSize,
      pageUpperBound: 0,
      hasMore: false,
      rangeReady
    }
  }

  const offset = (page - 1) * pageSize
  const accountFilter = accountIds ? buildPublicAccountUsageIdFilter(accountIds) : { sql: '', params: [] }
  const orderBy = publicAccountUsageOrderBy(input.sortField, input.sortOrder)
  const rows = database.prepare(`
    SELECT
      usage_window.scope_id,
      usage_window.request_count,
      usage_window.success_count,
      usage_window.error_count,
      usage_window.input_tokens,
      usage_window.output_tokens,
      usage_window.cache_read_tokens,
      usage_window.cache_read_cost_usd,
      usage_window.total_cost_usd,
      usage_window.duration_ms_sum,
      usage_window.duration_ms_count,
      usage_window.duration_ms_max,
      usage_window.first_token_ms_sum,
      usage_window.first_token_ms_count,
      usage_window.first_token_ms_max,
      usage_window.active_days,
      usage_window.last_used_at,
      usage_window.last_error_at
    FROM usage_scope_range_windows usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = 'account'
      AND usage_window.start_date = ?
      AND usage_window.end_date = ?
      ${accountFilter.sql}
    ORDER BY ${orderBy}, usage_window.scope_id ASC
    LIMIT ? OFFSET ?
  `).all(
    GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
    input.range.startDate,
    input.range.endDate,
    ...accountFilter.params,
    pageSize + 1,
    offset
  ) as unknown as AccountUsageRangeRow[]
  const pageRows = rows.slice(0, pageSize)
  const hasMore = rows.length > pageSize
  const metadataById = loadPublicAccountUsageMetadata(pageRows.map((row) => row.scope_id))
  return {
    items: pageRows.map((row, index) => {
      const metadata = metadataById.get(row.scope_id)
      return mapPublicAccountUsageItem({
        ...row,
        accountName: metadata?.name ?? row.scope_id,
        providerCode: metadata?.provider_code,
        ownerSystemAccountId: metadata?.system_account_id,
        ownerSystemAccountName: metadata?.owner_system_account_name ?? undefined,
        type: metadata?.type,
        status: metadata?.status
      }, offset + index + 1)
    }),
    page,
    pageSize,
    pageUpperBound: pagedTotalUpperBound(page, pageSize, pageRows.length, hasMore),
    hasMore,
    rangeReady
  }
}

function publicAccountUsageRangeReady(database: ReturnType<typeof getStatsDatabase>, range: PublicWelfareRange): boolean {
  const row = database.prepare(`
    SELECT 1
    FROM usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND start_date = ?
      AND end_date = ?
    LIMIT 1
  `).get(GLOBAL_STATS_SYSTEM_ACCOUNT_ID, range.startDate, range.endDate) as unknown as { 1?: number } | undefined
  return Boolean(row)
}

function loadPublicAccountUsageKeywordAccountIds(keyword: string): string[] {
  const value = keyword.trim()
  if (!value) return []
  const ids = new Set<string>()
  const database = getBusinessDatabase()
  collectPublicAccountUsageKeywordIds(database, ids, `
    SELECT id
    FROM accounts
    WHERE id >= ? AND id < ?
    ORDER BY id ASC
    LIMIT ?
  `, [value, `${value}\uffff`])
  collectPublicAccountUsageKeywordIds(database, ids, `
    SELECT id
    FROM accounts INDEXED BY idx_accounts_name_lookup
    WHERE name COLLATE NOCASE >= ? AND name COLLATE NOCASE < ?
    ORDER BY name COLLATE NOCASE ASC, id ASC
    LIMIT ?
  `, [value, `${value}\uffff`])
  collectPublicAccountUsageKeywordIds(database, ids, `
    SELECT id
    FROM accounts INDEXED BY idx_accounts_provider_lookup
    WHERE provider_code COLLATE NOCASE >= ? AND provider_code COLLATE NOCASE < ?
    ORDER BY provider_code COLLATE NOCASE ASC, id ASC
    LIMIT ?
  `, [value, `${value}\uffff`])
  collectPublicAccountUsageKeywordIds(database, ids, `
    SELECT id
    FROM accounts INDEXED BY idx_accounts_type_lookup
    WHERE type COLLATE NOCASE >= ? AND type COLLATE NOCASE < ?
    ORDER BY type COLLATE NOCASE ASC, id ASC
    LIMIT ?
  `, [value, `${value}\uffff`])
  return [...ids]
}

function collectPublicAccountUsageKeywordIds(
  database: ReturnType<typeof getBusinessDatabase>,
  ids: Set<string>,
  sql: string,
  params: [string, string]
): void {
  const remaining = 1000 - ids.size
  if (remaining <= 0) return
  const rows = database.prepare(sql).all(...params, remaining) as unknown as Array<{ id?: string }>
  for (const row of rows) {
    if (row.id) ids.add(row.id)
    if (ids.size >= 1000) break
  }
}

function buildPublicAccountUsageIdFilter(accountIds: string[]): { sql: string; params: string[] } {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return { sql: 'AND 0 = 1', params: [] }
  const chunks = chunkValues(ids, 400)
  return {
    sql: `AND (${chunks.map((chunk) => `usage_window.scope_id IN (${sqlPlaceholders(chunk.length)})`).join(' OR ')})`,
    params: chunks.flat()
  }
}

function loadPublicAccountUsageMetadata(accountIds: string[]): Map<string, AccountUsageMetadataRow> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  const result = new Map<string, AccountUsageMetadataRow>()
  if (!ids.length) return result
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 400)) {
    const rows = database.prepare(`
      SELECT
        accounts.id,
        accounts.name,
        accounts.provider_code,
        accounts.system_account_id,
        COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS owner_system_account_name,
        accounts.type,
        accounts.status
      FROM accounts
      LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id
      WHERE accounts.id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as AccountUsageMetadataRow[]
    for (const row of rows) {
      result.set(row.id, row)
    }
  }
  return result
}

function publicAccountUsageOrderBy(field: PublicAccountUsageSortField | undefined, order: 'asc' | 'desc' | undefined): string {
  const direction = order === 'asc' ? 'ASC' : 'DESC'
  switch (field) {
    case 'requestCount':
      return `usage_window.request_count ${direction}`
    case 'successCount':
      return `usage_window.success_count ${direction}`
    case 'errorCount':
      return `usage_window.error_count ${direction}`
    case 'errorRate':
      return `CASE WHEN usage_window.request_count > 0 THEN CAST(usage_window.error_count AS REAL) / usage_window.request_count ELSE 0 END ${direction}`
    case 'totalCost':
      return `usage_window.total_cost_usd ${direction}`
    case 'activeDays':
      return `usage_window.active_days ${direction}`
    case 'lastUsedAt':
      return `usage_window.last_used_at ${direction}`
    case 'totalTokens':
    default:
      return `(usage_window.input_tokens + usage_window.output_tokens) ${direction}`
  }
}

function mapPublicAccountUsageItem(
  row: AccountUsageRangeRow & {
    accountName?: string
    providerCode?: string
    ownerSystemAccountId?: string
    ownerSystemAccountName?: string
    type?: string
    status?: string
  },
  rank: number
): PublicAccountUsageItem {
  const requestCount = Number(row.request_count ?? 0)
  const successCount = Number(row.success_count ?? requestCount)
  const errorCount = Number(row.error_count ?? 0)
  const inputTokens = Number(row.input_tokens ?? 0)
  const outputTokens = Number(row.output_tokens ?? 0)
  const cacheReadTokens = Number(row.cache_read_tokens ?? 0)
  const firstTokenMsCount = Number(row.first_token_ms_count ?? 0)
  const durationMsCount = Number(row.duration_ms_count ?? 0)
  return {
    rank,
    dimension: 'account',
    accountId: row.scope_id,
    accountName: row.accountName ?? row.scope_id,
    providerCode: row.providerCode,
    ownerSystemAccountId: row.ownerSystemAccountId,
    ownerSystemAccountName: row.ownerSystemAccountName,
    type: row.type,
    status: row.status,
    requestCount,
    successCount,
    errorCount,
    errorRate: roundRatio(requestCount > 0 ? errorCount / requestCount : 0),
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheRate: roundRatio(inputTokens > 0 ? cacheReadTokens / inputTokens : 0),
    totalTokens: inputTokens + outputTokens,
    totalCost: Number(row.total_cost_usd ?? 0),
    cacheReadCost: Number(row.cache_read_cost_usd ?? 0),
    activeDays: Number(row.active_days ?? 0),
    averageFirstTokenMs: firstTokenMsCount > 0 ? Number((Number(row.first_token_ms_sum ?? 0) / firstTokenMsCount).toFixed(1)) : undefined,
    averageDurationMs: durationMsCount > 0 ? Number((Number(row.duration_ms_sum ?? 0) / durationMsCount).toFixed(1)) : undefined,
    maxDurationMs: Number(row.duration_ms_max ?? 0) || undefined,
    lastUsedAt: row.last_used_at ?? undefined,
    lastErrorAt: row.last_error_at ?? undefined
  }
}

function mapPublicClientIpUsageItem(ip: string, usage: ClientIpUsageSummary, rank: number): PublicClientIpUsageItem {
  return {
    rank,
    dimension: 'client_ip',
    ip,
    requestCount: usage.requestCount,
    successCount: usage.successCount,
    errorCount: usage.errorCount,
    errorRate: roundRatio(usage.errorRate),
    inputTokens: usage.inputTokens,
    outputTokens: usage.outputTokens,
    cacheReadTokens: usage.cacheReadTokens,
    cacheRate: roundRatio(usage.inputTokens > 0 ? usage.cacheReadTokens / usage.inputTokens : 0),
    totalTokens: usage.totalTokens,
    totalCost: usage.totalCost,
    cacheReadCost: usage.cacheReadCost,
    activeDays: usage.activeDays,
    averageFirstTokenMs: usage.averageFirstTokenMs,
    averageDurationMs: usage.averageDurationMs,
    maxDurationMs: usage.maxDurationMs,
    lastUsedAt: usage.lastUsedAt,
    lastErrorAt: usage.lastErrorAt
  }
}

function consumptionMetricSortField(metric: PublicConsumptionRankingMetric): PublicClientIpUsageSortField {
  switch (metric) {
    case 'totalCost':
      return 'totalCost'
    case 'requestCount':
      return 'requestCount'
    case 'totalTokens':
    default:
      return 'totalTokens'
  }
}

function consumptionMetricValue(item: PublicClientIpUsageItem, metric: PublicConsumptionRankingMetric): number {
  switch (metric) {
    case 'totalCost':
      return item.totalCost
    case 'requestCount':
      return item.requestCount
    case 'totalTokens':
    default:
      return item.totalTokens
  }
}

function filterMockAccountRows(keyword?: string): typeof mockAccountRows {
  const value = keyword?.trim().toLowerCase()
  if (!value) return mockAccountRows
  return mockAccountRows.filter((row) =>
    row.accountId.toLowerCase().startsWith(value)
    || row.accountName.toLowerCase().includes(value)
    || row.providerCode.toLowerCase().startsWith(value)
  )
}

function mockAccountUsageRow(
  scopeId: string,
  requestCount: number,
  successCount: number,
  errorCount: number,
  inputTokens: number,
  outputTokens: number,
  cacheReadTokens: number,
  totalCostUsd: number,
  activeDays: number
): AccountUsageRangeRow {
  return {
    scope_id: scopeId,
    request_count: requestCount,
    success_count: successCount,
    error_count: errorCount,
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    cache_read_tokens: cacheReadTokens,
    cache_read_cost_usd: cacheReadTokens / 100000,
    total_cost_usd: totalCostUsd,
    duration_ms_sum: requestCount * 3200,
    duration_ms_count: requestCount,
    duration_ms_max: 12880,
    first_token_ms_sum: requestCount * 860,
    first_token_ms_count: requestCount,
    first_token_ms_max: 1820,
    active_days: activeDays,
    last_used_at: '2026-05-30T00:00:00.000Z',
    last_error_at: errorCount > 0 ? '2026-05-30T00:00:00.000Z' : null
  }
}

function boundedInteger(value: unknown, min: number, max: number, fallback: number): number {
  const number = typeof value === 'number' ? Math.trunc(value) : Number.NaN
  if (!Number.isFinite(number)) {
    return fallback
  }
  return Math.min(max, Math.max(min, number))
}

function roundRatio(value: number): number {
  return Number.isFinite(value) ? Number(value.toFixed(4)) : 0
}

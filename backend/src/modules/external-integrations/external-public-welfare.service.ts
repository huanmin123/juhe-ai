import {
  latestClientIpStatsLagSeconds,
  listClientIpStats,
  type ClientIpStatsSortField,
  type ClientIpUsageSummary
} from '../../storage/client-ip-stats.repository.js'
import {
  dateKey,
  normalizeAccountUsageStatsRange,
  usageStatsTimezone
} from '../../storage/usage-stats-helpers.js'

export type PublicWelfareRangePreset = 'today' | 'last7d' | 'last31d'
export type PublicClientIpUsageSortField = ClientIpStatsSortField
export type PublicConsumptionRankingMetric = 'totalTokens' | 'totalCost' | 'requestCount'
export type PublicWelfareDataSource = 'stats' | 'mock'

export interface PublicClientIpUsageQuery {
  range?: PublicWelfareRangePreset
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
  sortField?: PublicClientIpUsageSortField
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

export function getPublicClientIpUsage(input: PublicClientIpUsageQuery = {}, options: { mock?: boolean } = {}): PublicClientIpUsageResponse {
  const range = resolvePublicRange(input)
  const page = boundedInteger(input.page, 1, 1000, 1)
  const pageSize = boundedInteger(input.pageSize ?? input.limit, 1, 100, 20)
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
        '基于 IP 聚合表的消耗排行便利视图',
        '分组、API Key 和账号的受控新增、修改与删除入口'
      ],
      notProvided: [
        '公益站用户维度排行榜快照',
        'IP 到公益站用户的业务归属',
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

function boundedInteger(value: unknown, min: number, max: number, fallback: number): number {
  const number = Math.trunc(Number(value))
  if (!Number.isFinite(number)) {
    return fallback
  }
  return Math.min(max, Math.max(min, number))
}

function roundRatio(value: number): number {
  return Number.isFinite(value) ? Number(value.toFixed(4)) : 0
}

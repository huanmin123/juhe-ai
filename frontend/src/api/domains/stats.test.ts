import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { myStatsApi, statsApi, tableMonitorApi } from './stats'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
  signal?: unknown
}

const originalAdapter = http.defaults.adapter
let requests: CapturedRequest[] = []
let responseData: unknown = {}

/** 替换 axios adapter：捕获请求形状并返回可配置的 `{ data: { data } }` 响应。 */
function installCaptureAdapter(data: unknown = {}): void {
  responseData = data
  requests = []
  http.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    requests.push({
      method: String(config.method ?? '').toUpperCase(),
      url: String(config.url ?? ''),
      params: config.params,
      data: config.data,
      signal: config.signal
    })
    return { data: { data: responseData }, status: 200, statusText: 'OK', headers: {}, config }
  }
}

function requestShapes(): Array<[string, string]> {
  return requests.map((request) => [request.method, request.url])
}

function payloadOf(request: CapturedRequest): Record<string, unknown> {
  return JSON.parse(String(request.data)) as Record<string, unknown>
}

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('statsApi 请求形状', () => {
  it('用量概览与窗口方法发出正确的 method 与 URL', async () => {
    await statsApi.usageWindow()
    await statsApi.usageOverviewSummary()
    await statsApi.usageOverviewDailyTrend()
    await statsApi.usageOverviewHourlyTrend()
    await statsApi.usageOverviewModelDistribution()
    await statsApi.usageOverviewErrors()
    expect(requestShapes()).toEqual([
      ['GET', '/stats/usage-window'],
      ['GET', '/stats/usage-overview/summary'],
      ['GET', '/stats/usage-overview/daily-trend'],
      ['GET', '/stats/usage-overview/hourly-trend'],
      ['GET', '/stats/usage-overview/model-distribution'],
      ['GET', '/stats/usage-overview/errors']
    ])
  })

  it('账户用量方法发出正确的 method 与 URL 并归一化参数', async () => {
    await statsApi.accountUsage()
    await statsApi.accountUsageOptions()
    await statsApi.accountUsageSummary()
    await statsApi.accountUsageTrend()
    expect(requestShapes()).toEqual([
      ['GET', '/stats/account-usage'],
      ['GET', '/stats/account-usage/options'],
      ['GET', '/stats/account-usage/summary'],
      ['GET', '/stats/account-usage/trend']
    ])
    await statsApi.accountUsage({ systemAccountId: 'sa-1', accountIds: ['a-1', 'a-2'], schedulable: 'all' })
    expect(requests[4].params).toEqual({ systemAccountId: 'sa-1', accountIds: 'a-1,a-2' })
  })

  it('账户用量选项方法归一化 selectedIds（keyword 直接透传）', async () => {
    await statsApi.accountUsageOptions({ keyword: ' k ', limit: 3, selectedIds: ['a', 'b'] })
    expect(requests[0].params).toEqual({ keyword: ' k ', limit: 3, selectedIds: 'a,b' })
  })

  it('AI 性能与健康方法发出正确的 method 与 URL', async () => {
    await statsApi.aiPerformanceAccounts()
    await statsApi.aiPerformance()
    await statsApi.aiPerformanceSeries({ accountIds: ['a-1'] })
    await statsApi.aiHealth()
    await statsApi.aiHealthHourDetail({ accountId: 'a-1', statHour: '2026-01-01T00:00:00Z' } as never)
    expect(requestShapes()).toEqual([
      ['GET', '/stats/ai-performance/accounts'],
      ['GET', '/stats/ai-performance'],
      ['GET', '/stats/ai-performance/series'],
      ['GET', '/stats/ai-health'],
      ['GET', '/stats/ai-health/hour-detail']
    ])
  })

  it('aiPerformanceSeries 以 URLSearchParams 序列化重复 accountIds', async () => {
    await statsApi.aiPerformanceSeries({ systemAccountId: 'sa-1', accountIds: ['a-1', 'a-2'] })
    expect(requests[0].params).toBeInstanceOf(URLSearchParams)
    expect((requests[0].params as URLSearchParams).toString()).toBe('systemAccountId=sa-1&accountIds=a-1&accountIds=a-2')
  })

  it('aiPerformance 归一化日期区间参数', async () => {
    await statsApi.aiPerformance({ systemAccountId: 'sa-1', startDate: '2026-01-01', endDate: '2026-01-02' })
    expect(requests[0].params).toEqual({ systemAccountId: 'sa-1', startDate: '2026-01-01', endDate: '2026-01-02' })
  })

  it('系统指标运行态方法发出正确的 method 与 URL 并透传 signal', async () => {
    const controller = new AbortController()
    await statsApi.goRuntimeTrend({ startDate: 's', endDate: 'e' }, { signal: controller.signal })
    await statsApi.systemMetricsHealthSnapshot({ signal: controller.signal })
    await statsApi.systemMetricsRuntimeJobs({ page: 1, pageSize: 10 }, { signal: controller.signal })
    expect(requestShapes()).toEqual([
      ['GET', '/stats/system-metrics/go-runtime-trend'],
      ['GET', '/stats/system-metrics/health-snapshot'],
      ['GET', '/stats/system-metrics/runtime/jobs']
    ])
    expect(requests[0].params).toEqual({ startDate: 's', endDate: 'e' })
    expect(requests[0].signal).toBe(controller.signal)
    expect(requests[2].params).toEqual({ page: 1, pageSize: 10 })
    expect(requests[2].signal).toBe(controller.signal)
  })

  it('健康监控方法透传 signal', async () => {
    const controller = new AbortController()
    await statsApi.aiHealth(undefined, { signal: controller.signal })
    expect(requests[0].url).toBe('/stats/ai-health')
    expect(requests[0].signal).toBe(controller.signal)
    await statsApi.aiHealthHourDetail({ accountId: 'a-1', statHour: 'h' } as never, { signal: controller.signal })
    expect(requests[1].url).toBe('/stats/ai-health/hour-detail')
    expect(requests[1].signal).toBe(controller.signal)
  })
})

describe('tableMonitorApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await tableMonitorApi.overview()
    await tableMonitorApi.history({ databaseRole: 'business', tableName: 'accounts' } as never)
    await tableMonitorApi.databaseHistory()
    await tableMonitorApi.cleanupNonBusinessData({ confirm: true } as never)
    expect(requestShapes()).toEqual([
      ['GET', '/table-monitor/overview'],
      ['GET', '/table-monitor/history'],
      ['GET', '/table-monitor/database-history'],
      ['POST', '/table-monitor/non-business-data/cleanup']
    ])
  })

  it('cleanupNonBusinessData 携带确认 payload', async () => {
    await tableMonitorApi.cleanupNonBusinessData({ confirm: true } as never)
    expect(payloadOf(requests[0])).toEqual({ confirm: true })
  })
})

describe('myStatsApi 请求形状', () => {
  it('用量概览方法命中 /my-stats 前缀并剥离 systemAccountId', async () => {
    await myStatsApi.usageWindow()
    await myStatsApi.usageOverviewSummary({ systemAccountId: 'sa-1', startDate: 's' } as never)
    expect(requestShapes()).toEqual([
      ['GET', '/my-stats/usage-window'],
      ['GET', '/my-stats/usage-overview/summary']
    ])
    expect(requests[1].params).toEqual({ startDate: 's' })
  })

  it('账户用量方法命中 /my-stats 前缀且不携带 systemAccountId', async () => {
    await myStatsApi.accountUsage({ systemAccountId: 'sa-1', page: 1 })
    expect(requests[0].params).toEqual({ page: 1 })
    await myStatsApi.accountUsageOptions({ systemAccountId: 'sa-1', limit: 2 })
    expect(requests[1].params).toEqual({ limit: 2 })
  })

  it('AI 性能与健康方法命中 /my-stats 前缀', async () => {
    await myStatsApi.aiPerformanceAccounts()
    await myStatsApi.aiPerformance()
    await myStatsApi.aiPerformanceSeries({ accountIds: ['a-1'] })
    await myStatsApi.aiHealth({ systemAccountId: 'sa-1', hours: 24 } as never)
    await myStatsApi.aiHealthHourDetail({ accountId: 'a-1', statHour: 'h' } as never)
    expect(requestShapes()).toEqual([
      ['GET', '/my-stats/ai-performance/accounts'],
      ['GET', '/my-stats/ai-performance'],
      ['GET', '/my-stats/ai-performance/series'],
      ['GET', '/my-stats/ai-health'],
      ['GET', '/my-stats/ai-health/hour-detail']
    ])
    expect(requests[3].params).toEqual({ hours: 24 })
  })

  it('个人视角健康监控方法透传 signal', async () => {
    const controller = new AbortController()
    await myStatsApi.aiHealth(undefined, { signal: controller.signal })
    expect(requests[0].signal).toBe(controller.signal)
    await myStatsApi.aiHealthHourDetail({ accountId: 'a-1', statHour: 'h' } as never, { signal: controller.signal })
    expect(requests[1].signal).toBe(controller.signal)
  })
})

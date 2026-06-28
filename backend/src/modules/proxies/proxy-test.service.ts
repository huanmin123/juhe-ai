import type { IncomingHttpHeaders } from 'node:http'
import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'

import type { ProviderDefinition } from '../../domain/types.js'
import { listProvidersAsync } from '../../storage/provider.repository.js'
import {
  getProxyTestConfigAsync,
  listEnabledProxyTestConfigsAsync,
  type ProxyProfileTestConfig
} from '../../storage/proxy.repository.js'
import type { updateProxyTestState } from '../../storage/proxy.repository.js'
import { BoundedBufferCollector } from '../../shared/bounded-buffer.js'
import { requestBackgroundWorkerDbService } from '../background/background-ipc.js'
import { createProxyAgent } from '../openai-oauth/openai-oauth.service.js'

export type ProxyTestItemStatus = 'passed' | 'warning' | 'failed'
export type ProxyTestOverallStatus = 'passed' | 'warning' | 'failed' | 'unknown'

export interface ProxyTestItem {
  name: string
  status: ProxyTestItemStatus
  httpStatus?: number
  latencyMs?: number
  message: string
  targetUrl?: string
}

export interface ProxyTestReport {
  proxyId: string
  proxyName: string
  score: number
  grade: string
  status: ProxyTestOverallStatus
  passedCount: number
  warningCount: number
  failedCount: number
  outboundIp?: string
  outboundRegion?: string
  baseLatencyMs?: number
  testedAt: string
  items: ProxyTestItem[]
  message: string
}

interface HttpProbeResult {
  statusCode: number
  headers: Record<string, string | string[]>
  bodyText: string
  latencyMs: number
}

interface ProxyOutboundInfo {
  outboundIp?: string
  outboundRegion?: string
}

type OutboundProbeParser = 'ip-api' | 'ipwhois' | 'ipsb' | 'ipinfo' | 'ipify' | 'httpbin'

const probeTimeoutMs = 15000
export const manualProxyTestDeadlineMs = 25_000
const maxProxyProbeResponseBytes = 512 * 1024
export const proxyLatencyRefreshIntervalSeconds = 60
export const proxyLatencyRefreshBatchSize = 20
const outboundProbeTargets = [
  { url: 'http://ip-api.com/json/?lang=zh-CN', parser: 'ip-api' },
  { url: 'https://ipwho.is/', parser: 'ipwhois' },
  { url: 'https://api.ip.sb/geoip', parser: 'ipsb' },
  { url: 'https://ipinfo.io/json', parser: 'ipinfo' },
  { url: 'https://api.ipify.org?format=json', parser: 'ipify' },
  { url: 'http://httpbin.org/ip', parser: 'httpbin' }
] as const

export async function testProxyById(id: string, options: { persist?: boolean; deadlineMs?: number } = {}): Promise<ProxyTestReport | undefined> {
  const proxy = await getProxyTestConfigAsync(id)
  if (!proxy) return undefined
  return testProxy(proxy, {
    persist: options.persist ?? true,
    includeOutboundInfo: true,
    deadlineAtMs: proxyTestDeadlineAt(options.deadlineMs)
  })
}

export async function refreshProxyLatencyBatch(limit: number): Promise<void> {
  const proxies = await listEnabledProxyTestConfigsAsync(limit)
  for (const proxy of proxies) {
    try {
      await testProxy(proxy, { persist: true, includeOutboundInfo: false })
    } catch (error) {
      const message = error instanceof Error ? error.message : '代理检测失败'
      await persistProxyTestState(proxy.id, {
        testStatus: 'failed',
        lastTestMessage: message
      })
    }
  }
}

async function testProxy(proxy: ProxyProfileTestConfig, options: { persist: boolean; includeOutboundInfo: boolean; deadlineAtMs?: number }): Promise<ProxyTestReport> {
  const testedAt = new Date().toISOString()
  const enabledProviders = (await listProvidersAsync()).filter((provider) => provider.enabled)
  const outboundInfoPromise = options.includeOutboundInfo ? probeOutboundInfo(proxy, options.deadlineAtMs) : Promise.resolve(undefined)
  const providerItems: ProxyTestItem[] = []
  for (const provider of enabledProviders) {
    providerItems.push(await testProvider(proxy, provider, options.deadlineAtMs))
  }
  const outboundInfo = await outboundInfoPromise
  const baseItem = baseConnectivityItem(providerItems, enabledProviders.length)
  const items = [baseItem, ...providerItems]

  const summary = summarizeItems(items)
  const report: ProxyTestReport = {
    proxyId: proxy.id,
    proxyName: proxy.name,
    ...summary,
    outboundIp: outboundInfo?.outboundIp,
    outboundRegion: outboundInfo?.outboundRegion,
    baseLatencyMs: baseItem.latencyMs,
    testedAt,
    items,
    message: reportMessage(summary.status, summary.failedCount, summary.warningCount)
  }

  if (options.persist) {
    await persistProxyTestState(proxy.id, {
      testStatus: report.status,
      latencyMs: report.baseLatencyMs,
      outboundIp: options.includeOutboundInfo ? report.outboundIp : undefined,
      outboundRegion: options.includeOutboundInfo ? report.outboundRegion : undefined,
      lastTestMessage: report.message,
      lastTestedAt: testedAt
    })
  }

  return report
}

async function persistProxyTestState(
  proxyId: string,
  input: Parameters<typeof updateProxyTestState>[1]
): Promise<void> {
  const result = await requestBackgroundWorkerDbService({
    type: 'update_proxy_test_state',
    proxyId,
    input
  })
  if (result === undefined) {
    throw new Error('DB service 未返回代理检测状态写入结果')
  }
}

async function testProvider(proxy: ProxyProfileTestConfig, provider: ProviderDefinition, deadlineAtMs?: number): Promise<ProxyTestItem> {
  const targetUrl = provider.baseUrl
  if (proxyTestDeadlineReached(deadlineAtMs)) {
    return {
      name: provider.name,
      status: 'failed',
      message: '代理检测总耗时已达到上限',
      targetUrl
    }
  }
  try {
    const response = await requestWithProxy(targetUrl, proxy.proxyUrl, {
      timeoutMs: remainingProxyProbeTimeoutMs(deadlineAtMs)
    })
    const status = providerStatus(response.statusCode)
    return {
      name: provider.name,
      status,
      httpStatus: response.statusCode,
      latencyMs: response.latencyMs,
      message: providerMessage(status, response.statusCode),
      targetUrl
    }
  } catch (error) {
    return {
      name: provider.name,
      status: 'failed',
      message: errorMessage(error, '目标地址不可达'),
      targetUrl
    }
  }
}

async function probeOutboundInfo(proxy: ProxyProfileTestConfig, deadlineAtMs?: number): Promise<ProxyOutboundInfo | undefined> {
  for (const target of outboundProbeTargets) {
    if (proxyTestDeadlineReached(deadlineAtMs)) {
      return undefined
    }
    try {
      const response = await requestWithProxy(target.url, proxy.proxyUrl, {
        timeoutMs: remainingProxyProbeTimeoutMs(deadlineAtMs)
      })
      if (response.statusCode !== 200) {
        continue
      }
      const parsed = parseOutboundProbeResponse(target.parser, response.bodyText)
      if (parsed.outboundIp) {
        return parsed
      }
    } catch {
      // 出口信息是报告补充项，失败不影响供应商默认地址检测结果。
    }
  }
  return undefined
}

function parseOutboundProbeResponse(parser: OutboundProbeParser, bodyText: string): ProxyOutboundInfo {
  const result = JSON.parse(bodyText) as {
    success?: unknown
    status?: unknown
    message?: unknown
    query?: unknown
    ip?: unknown
    origin?: unknown
    country?: unknown
    country_code?: unknown
    countryCode?: unknown
    region?: unknown
    regionName?: unknown
    city?: unknown
  }

  if (parser === 'httpbin') {
    return {
      outboundIp: firstIp(result.origin)
    }
  }

  if (parser === 'ipify') {
    return {
      outboundIp: stringValue(result.ip)
    }
  }

  if (parser === 'ip-api') {
    if (String(result.status ?? '').toLowerCase() !== 'success') {
      throw new Error(typeof result.message === 'string' ? result.message : '出口信息探测失败')
    }
    return {
      outboundIp: stringValue(result.query),
      outboundRegion: regionText(result.country, result.countryCode, result.regionName, result.region, result.city)
    }
  }

  if (parser === 'ipwhois') {
    if (result.success === false) {
      throw new Error(typeof result.message === 'string' ? result.message : '出口信息探测失败')
    }
    return {
      outboundIp: stringValue(result.ip),
      outboundRegion: regionText(result.country, result.country_code, result.region, result.city)
    }
  }

  if (parser === 'ipsb') {
    return {
      outboundIp: stringValue(result.ip),
      outboundRegion: regionText(result.country, result.country_code, result.region, result.city)
    }
  }

  if (parser === 'ipinfo') {
    return {
      outboundIp: stringValue(result.ip),
      outboundRegion: regionText(undefined, result.country, result.region, result.city)
    }
  }

  return {}
}

function requestWithProxy(targetUrl: string, proxyUrl: string, options: { timeoutMs?: number } = {}): Promise<HttpProbeResult> {
  return new Promise((resolve, reject) => {
    const url = new URL(targetUrl)
    const requestFn = url.protocol === 'http:' ? httpRequest : httpsRequest
    const startedAt = Date.now()
    const timeoutMs = boundedProxyProbeTimeoutMs(options.timeoutMs)
    let settled = false
    const finish = (input: { statusCode: number; headers: IncomingHttpHeaders; body: BoundedBufferCollector }) => {
      if (settled) return
      settled = true
      resolve({
        statusCode: input.statusCode,
        headers: normalizeHeaders(input.headers),
        bodyText: input.body.text(),
        latencyMs: Date.now() - startedAt
      })
      request.destroy()
    }
    const request = requestFn(url, {
      method: 'GET',
      headers: {
        accept: 'application/json,text/plain,*/*',
        'user-agent': 'juhe-ai-proxy-test/0.1'
      },
      agent: createProxyAgent(proxyUrl),
      timeout: timeoutMs
    }, (response) => {
      const body = new BoundedBufferCollector(maxProxyProbeResponseBytes)
      response.on('data', (chunk: Buffer) => {
        body.append(chunk)
        if (body.truncated) {
          finish({ statusCode: response.statusCode ?? 0, headers: response.headers, body })
        }
      })
      response.on('end', () => {
        finish({ statusCode: response.statusCode ?? 0, headers: response.headers, body })
      })
    })
    request.on('error', (error) => {
      if (!settled) reject(error)
    })
    request.on('timeout', () => request.destroy(new Error('代理检测请求超时')))
    request.end()
  })
}

function proxyTestDeadlineAt(deadlineMs: number | undefined): number | undefined {
  if (deadlineMs === undefined) {
    return undefined
  }
  const normalized = Math.trunc(deadlineMs)
  return Number.isFinite(normalized) && normalized > 0 ? Date.now() + normalized : undefined
}

function proxyTestDeadlineReached(deadlineAtMs: number | undefined): boolean {
  return deadlineAtMs !== undefined && Date.now() >= deadlineAtMs
}

function remainingProxyProbeTimeoutMs(deadlineAtMs: number | undefined): number {
  if (deadlineAtMs === undefined) {
    return probeTimeoutMs
  }
  return Math.max(1, deadlineAtMs - Date.now())
}

function boundedProxyProbeTimeoutMs(timeoutMs: number | undefined): number {
  if (timeoutMs === undefined) {
    return probeTimeoutMs
  }
  const normalized = Math.trunc(timeoutMs)
  if (!Number.isFinite(normalized) || normalized <= 0) {
    return 1
  }
  return Math.max(1, Math.min(probeTimeoutMs, normalized))
}

function baseConnectivityItem(providerItems: ProxyTestItem[], providerCount: number): ProxyTestItem {
  if (providerCount <= 0) {
    return {
      name: '基础连通性',
      status: 'failed',
      message: '没有启用的供应商默认地址'
    }
  }
  const failedCount = providerItems.filter((item) => item.status === 'failed').length
  const reachableCount = providerItems.length - failedCount
  const latencyValues = providerItems
    .map((item) => item.latencyMs)
    .filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  const averageLatency = latencyValues.length
    ? Math.round(latencyValues.reduce((sum, value) => sum + value, 0) / latencyValues.length)
    : undefined
  return {
    name: '基础连通性',
    status: failedCount === 0 ? 'passed' : reachableCount > 0 ? 'warning' : 'failed',
    latencyMs: averageLatency,
    message: failedCount === 0
      ? '全部供应商默认地址可达'
      : reachableCount > 0
        ? `部分供应商默认地址可达（${reachableCount}/${providerCount}）`
        : '供应商默认地址全部不可达'
  }
}

function summarizeItems(items: ProxyTestItem[]): Pick<ProxyTestReport, 'score' | 'grade' | 'status' | 'passedCount' | 'warningCount' | 'failedCount'> {
  const passedCount = items.filter((item) => item.status === 'passed').length
  const warningCount = items.filter((item) => item.status === 'warning').length
  const failedCount = items.filter((item) => item.status === 'failed').length
  const score = Math.max(0, Math.round(100 - warningCount * 10 - failedCount * 35))
  const status: ProxyTestOverallStatus = failedCount > 0 ? 'failed' : warningCount > 0 ? 'warning' : items.length > 0 ? 'passed' : 'unknown'
  return {
    score,
    grade: score >= 90 ? 'A' : score >= 75 ? 'B' : score >= 60 ? 'C' : 'D',
    status,
    passedCount,
    warningCount,
    failedCount
  }
}

function providerStatus(statusCode: number): ProxyTestItemStatus {
  if (statusCode >= 200 && statusCode < 300) return 'passed'
  if (statusCode === 401 || statusCode === 403 || statusCode === 404 || statusCode === 405 || statusCode === 429) return 'warning'
  if (statusCode >= 300 && statusCode < 500) return 'warning'
  return 'failed'
}

function providerMessage(status: ProxyTestItemStatus, statusCode: number): string {
  if (status === 'passed') return `HTTP ${statusCode}`
  if (status === 'warning') return `HTTP ${statusCode}（目标可达，但鉴权或方法受限）`
  return `HTTP ${statusCode}（目标不可用或代理链路异常）`
}

function reportMessage(status: ProxyTestOverallStatus, failedCount: number, warningCount: number): string {
  if (status === 'passed') return '代理质量检测通过'
  if (status === 'warning') return `代理可用，存在 ${warningCount} 项告警`
  if (status === 'failed') return `代理检测存在 ${failedCount} 项失败`
  return '代理尚未检测'
}

function normalizeHeaders(headers: IncomingHttpHeaders): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  for (const [name, value] of Object.entries(headers)) {
    if (typeof value === 'string' || Array.isArray(value)) {
      output[name] = value
    }
  }
  return output
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function firstIp(value: unknown): string | undefined {
  return typeof value === 'string' ? stringValue(value.split(',')[0]) : undefined
}

function regionText(countryValue: unknown, countryCodeValue: unknown, ...fallbackValues: unknown[]): string | undefined {
  const countryName = countryDisplayName(countryValue, countryCodeValue)
  return countryName ?? fallbackValues.map(stringValue).find(Boolean)
}

function countryDisplayName(countryValue: unknown, countryCodeValue: unknown): string | undefined {
  const country = stringValue(countryValue)
  const countryCode = stringValue(countryCodeValue) ?? (country && /^[A-Z]{2}$/i.test(country) ? country : undefined)
  if (!countryCode) return country
  try {
    return new Intl.DisplayNames(['zh-CN'], { type: 'region' }).of(countryCode.toUpperCase()) ?? countryCode.toUpperCase()
  } catch {
    return country ?? countryCode.toUpperCase()
  }
}

import type { AgentOptions, ClientRequest, IncomingHttpHeaders, RequestOptions } from 'node:http'
import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'
import { connect as netConnect, isIP, type Socket } from 'node:net'
import { connect as tlsConnect, type ConnectionOptions as TlsConnectionOptions } from 'node:tls'

import { HttpsProxyAgent } from 'https-proxy-agent'

import type { ProviderDefinition } from '../../domain/types.js'
import { listProvidersAsync } from '../../storage/provider.repository.js'
import {
  getProxyTestConfigAsync,
  listEnabledProxyTestConfigsAsync,
  type ProxyTestStateUpdateInput,
  type ProxyProfileTestConfig
} from '../../storage/proxy.repository.js'
import { BoundedBufferCollector } from '../../shared/bounded-buffer.js'
import { requestBackgroundWorkerDbService } from '../background/background-ipc.js'
import { createProxyAgent } from '../openai-oauth/openai-oauth.service.js'

export type ProxyTestItemStatus = 'passed' | 'warning' | 'failed' | 'unknown'
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

export interface ProxyTestExecution {
  report: ProxyTestReport
  configUpdatedAt: string
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

class ProxyProbeTransportFailure extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ProxyProbeTransportFailure'
  }
}

class ProxyProbeUnknownFailure extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ProxyProbeUnknownFailure'
  }
}

type ProxyAgentConnectOptions = Parameters<HttpsProxyAgent<string>['connect']>[1]

interface ProxyConnectResponse {
  statusCode: number
  statusText: string
  headers: Record<string, string | string[]>
}

const proxyConnectHeaderMaxBytes = 64 * 1024

class BoundedProxyTestHttpsAgent extends HttpsProxyAgent<string> {
  constructor(
    proxyUrl: string,
    options: AgentOptions,
    private readonly signal: AbortSignal
  ) {
    super(proxyUrl, options)
  }

  override async connect(request: ClientRequest, options: ProxyAgentConnectOptions): Promise<Socket> {
    if (!options.host) {
      throw new ProxyProbeUnknownFailure('代理检测目标缺少 host')
    }
    if (this.signal.aborted) {
      throw new ProxyProbeTransportFailure('代理检测请求已达到绝对总超时')
    }

    const proxySocket = this.proxy.protocol === 'https:'
      ? tlsConnect(withTlsServername(this.connectOpts))
      : netConnect(this.connectOpts)
    const abort = () => proxySocket.destroy(new ProxyProbeTransportFailure('代理检测请求已达到绝对总超时'))
    this.signal.addEventListener('abort', abort, { once: true })
    proxySocket.once('close', () => {
      this.signal.removeEventListener('abort', abort)
    })

    try {
      const responsePromise = readProxyConnectResponse(proxySocket)
      proxySocket.write(proxyConnectRequestPayload(this, options))
      const connectResponse = await responsePromise
      request.emit('proxyConnect', connectResponse)
      this.emit('proxyConnect', connectResponse, request)
      if (connectResponse.statusCode !== 200) {
        throw new ProxyProbeTransportFailure(`代理 CONNECT 返回 HTTP ${connectResponse.statusCode}`)
      }

      request.once('socket', (socket) => socket.resume())
      if (!options.secureEndpoint) {
        return proxySocket
      }

      const {
        host: _host,
        path: _path,
        port: _port,
        secureEndpoint: _secureEndpoint,
        ...targetTlsOptions
      } = options
      return tlsConnect({
        ...withTlsServername(targetTlsOptions),
        socket: proxySocket
      })
    } catch (error) {
      proxySocket.destroy()
      throw error
    }
  }
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

export async function testProxyById(id: string, options: { persist?: boolean; deadlineMs?: number } = {}): Promise<ProxyTestExecution | undefined> {
  const proxy = await getProxyTestConfigAsync(id)
  if (!proxy) return undefined
  const report = await testProxy(proxy, {
    persist: options.persist ?? true,
    includeOutboundInfo: true,
    deadlineAtMs: proxyTestDeadlineAt(options.deadlineMs)
  })
  return {
    report,
    configUpdatedAt: proxy.configUpdatedAt
  }
}

export async function refreshProxyLatencyBatch(limit: number): Promise<void> {
  const proxies = await listEnabledProxyTestConfigsAsync(limit)
  for (const proxy of proxies) {
    const testedAt = new Date().toISOString()
    try {
      await testProxy(proxy, {
        persist: true,
        includeOutboundInfo: false,
        deadlineAtMs: Date.now() + manualProxyTestDeadlineMs,
        testedAt
      })
    } catch (error) {
      const message = error instanceof Error ? error.message : '代理检测失败'
      await persistProxyTestState(proxy.id, refreshFailureState(message, {
        expectedConfigUpdatedAt: proxy.configUpdatedAt,
        lastTestedAt: testedAt
      }))
    }
  }
}

async function testProxy(proxy: ProxyProfileTestConfig, options: { persist: boolean; includeOutboundInfo: boolean; deadlineAtMs?: number; testedAt?: string }): Promise<ProxyTestReport> {
  const testedAt = options.testedAt ?? new Date().toISOString()
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
      lastTestedAt: testedAt,
      expectedConfigUpdatedAt: proxy.configUpdatedAt
    })
  }

  return report
}

async function persistProxyTestState(
  proxyId: string,
  input: ProxyTestStateUpdateInput
): Promise<boolean> {
  const result = await requestBackgroundWorkerDbService({
    type: 'update_proxy_test_state',
    proxyId,
    input
  })
  if (!result) {
    throw new Error('DB service 未返回代理检测状态写入结果')
  }
  if (!result.updated) {
    return false
  }
  if (result.proxyStatus !== input.testStatus) {
    throw new Error('DB service 未确认代理检测状态写入成功')
  }
  return true
}

async function testProvider(
  proxy: Pick<ProxyProfileTestConfig, 'proxyUrl'>,
  provider: Pick<ProviderDefinition, 'name' | 'baseUrl'>,
  deadlineAtMs?: number
): Promise<ProxyTestItem> {
  const targetUrl = provider.baseUrl
  if (proxyTestDeadlineReached(deadlineAtMs)) {
    return {
      name: provider.name,
      status: 'unknown',
      message: '未发起目标请求：代理检测总耗时已达到上限',
      targetUrl
    }
  }
  try {
    const response = await requestWithProxy(targetUrl, proxy.proxyUrl, {
      timeoutMs: remainingProxyProbeTimeoutMs(deadlineAtMs)
    })
    return {
      name: provider.name,
      status: 'passed',
      httpStatus: response.statusCode,
      latencyMs: response.latencyMs,
      message: providerMessage(response.statusCode),
      targetUrl
    }
  } catch (error) {
    const transportFailure = error instanceof ProxyProbeTransportFailure
    return {
      name: provider.name,
      status: transportFailure ? 'failed' : 'unknown',
      message: transportFailure
        ? errorMessage(error, '代理传输失败')
        : `未形成真实代理检测请求：${errorMessage(error, '检测基础设施不可用')}`,
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

function createProxyTestAgent(proxyUrl: string, timeoutMs: number, signal: AbortSignal): ReturnType<typeof createProxyAgent> {
  const protocol = new URL(proxyUrl).protocol
  if (protocol === 'http:' || protocol === 'https:') {
    return new BoundedProxyTestHttpsAgent(proxyUrl, { timeout: timeoutMs }, signal)
  }
  return createProxyAgent(proxyUrl, { timeout: timeoutMs })
}

function proxyConnectRequestPayload(agent: BoundedProxyTestHttpsAgent, options: ProxyAgentConnectOptions): string {
  const headers = typeof agent.proxyHeaders === 'function'
    ? agent.proxyHeaders()
    : { ...agent.proxyHeaders }
  const host = options.host
  if (!host) throw new ProxyProbeUnknownFailure('代理检测目标缺少 host')
  const targetHost = isIP(host) === 6 ? `[${host}]` : host
  const targetAuthority = `${targetHost}:${options.port}`

  if (agent.proxy.username || agent.proxy.password) {
    const credentials = `${decodeURIComponent(agent.proxy.username)}:${decodeURIComponent(agent.proxy.password)}`
    headers['Proxy-Authorization'] = `Basic ${Buffer.from(credentials).toString('base64')}`
  }
  headers.Host = targetAuthority
  headers['Proxy-Connection'] ??= agent.keepAlive ? 'Keep-Alive' : 'close'

  let payload = `CONNECT ${targetAuthority} HTTP/1.1\r\n`
  for (const [name, rawValue] of Object.entries(headers)) {
    if (rawValue === undefined) continue
    const values = Array.isArray(rawValue) ? rawValue : [rawValue]
    for (const value of values) {
      payload += `${name}: ${String(value)}\r\n`
    }
  }
  return `${payload}\r\n`
}

function readProxyConnectResponse(socket: Socket): Promise<ProxyConnectResponse> {
  return new Promise((resolve, reject) => {
    let buffer = Buffer.alloc(0)
    let settled = false

    const cleanup = () => {
      socket.removeListener('readable', read)
      socket.removeListener('end', onEnd)
      socket.removeListener('error', onError)
      socket.removeListener('close', onClose)
    }
    const fail = (error: Error) => {
      if (settled) return
      settled = true
      cleanup()
      reject(error)
    }
    const finish = (response: ProxyConnectResponse) => {
      if (settled) return
      settled = true
      cleanup()
      resolve(response)
    }
    const read = () => {
      let chunk: Buffer | null
      while ((chunk = socket.read() as Buffer | null) !== null) {
        buffer = Buffer.concat([buffer, chunk], buffer.byteLength + chunk.byteLength)
        if (buffer.byteLength > proxyConnectHeaderMaxBytes) {
          fail(new ProxyProbeTransportFailure('代理 CONNECT 响应头超过 64KiB 上限'))
          return
        }
        const headerEndIndex = buffer.indexOf('\r\n\r\n')
        if (headerEndIndex < 0) continue
        const remaining = buffer.subarray(headerEndIndex + 4)
        if (remaining.byteLength > 0) socket.unshift(remaining)
        try {
          finish(parseProxyConnectHeaders(buffer.subarray(0, headerEndIndex).toString('latin1')))
        } catch (error) {
          fail(error instanceof Error ? error : new Error('代理 CONNECT 响应头无效'))
        }
        return
      }
      socket.once('readable', read)
    }
    const onEnd = () => fail(new ProxyProbeTransportFailure('代理连接在 CONNECT 响应前结束'))
    const onError = (error: Error) => fail(error)
    const onClose = () => fail(new ProxyProbeTransportFailure('代理连接在 CONNECT 响应前关闭'))

    socket.once('end', onEnd)
    socket.once('error', onError)
    socket.once('close', onClose)
    read()
  })
}

function parseProxyConnectHeaders(headerText: string): ProxyConnectResponse {
  const lines = headerText.split('\r\n')
  const statusLine = lines.shift() ?? ''
  const match = /^HTTP\/1\.[01]\s+(\d{3})(?:\s+(.*))?$/i.exec(statusLine)
  if (!match) {
    throw new ProxyProbeTransportFailure('代理 CONNECT 响应状态行无效')
  }

  const headers: Record<string, string | string[]> = {}
  for (const line of lines) {
    const separatorIndex = line.indexOf(':')
    if (separatorIndex <= 0) {
      throw new ProxyProbeTransportFailure('代理 CONNECT 响应头格式无效')
    }
    const name = line.slice(0, separatorIndex).trim().toLowerCase()
    const value = line.slice(separatorIndex + 1).trim()
    const existing = headers[name]
    headers[name] = existing === undefined
      ? value
      : Array.isArray(existing)
        ? [...existing, value]
        : [existing, value]
  }

  return {
    statusCode: Number(match[1]),
    statusText: match[2]?.trim() ?? '',
    headers
  }
}

function withTlsServername<T extends TlsConnectionOptions>(options: T): T {
  if (options.servername !== undefined || typeof options.host !== 'string' || isIP(options.host)) {
    return options
  }
  return { ...options, servername: options.host }
}

function requestWithProxy(targetUrl: string, proxyUrl: string, options: { timeoutMs?: number } = {}): Promise<HttpProbeResult> {
  const startedAt = Date.now()
  const timeoutMs = boundedProxyProbeTimeoutMs(options.timeoutMs)
  const connectionAbortController = new AbortController()
  let url: URL
  let requestFn: typeof httpRequest | typeof httpsRequest
  let agent: ReturnType<typeof createProxyAgent> | undefined
  let forwardProxyRequestOptions: RequestOptions | undefined
  try {
    url = new URL(targetUrl)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') {
      throw new Error(`不支持的目标协议：${url.protocol}`)
    }
    const parsedProxyUrl = new URL(proxyUrl)
    const useForwardProxy = url.protocol === 'http:' && (parsedProxyUrl.protocol === 'http:' || parsedProxyUrl.protocol === 'https:')
    if (useForwardProxy) {
      requestFn = parsedProxyUrl.protocol === 'https:' ? httpsRequest : httpRequest
      forwardProxyRequestOptions = forwardHttpProxyRequestOptions(url, parsedProxyUrl)
    } else {
      requestFn = url.protocol === 'http:' ? httpRequest : httpsRequest
      agent = createProxyTestAgent(proxyUrl, timeoutMs, connectionAbortController.signal)
    }
  } catch (error) {
    return Promise.reject(new ProxyProbeUnknownFailure(errorMessage(error, '代理检测配置无效')))
  }

  return new Promise((resolve, reject) => {
    let settled = false
    let request: ClientRequest | undefined
    let responseReceived = false
    let deadlineTimer: NodeJS.Timeout | undefined

    const cleanup = () => {
      if (deadlineTimer) {
        clearTimeout(deadlineTimer)
        deadlineTimer = undefined
      }
    }
    const finish = (input: { statusCode: number; headers: IncomingHttpHeaders; body: BoundedBufferCollector }) => {
      if (settled) return
      settled = true
      cleanup()
      resolve({
        statusCode: input.statusCode,
        headers: normalizeHeaders(input.headers),
        bodyText: input.body.text(),
        latencyMs: Date.now() - startedAt
      })
    }
    const fail = (error: ProxyProbeTransportFailure | ProxyProbeUnknownFailure) => {
      if (settled) return
      settled = true
      cleanup()
      connectionAbortController.abort()
      agent?.destroy()
      request?.destroy()
      reject(error)
    }

    try {
      const requestOptions: RequestOptions = forwardProxyRequestOptions ?? {
        method: 'GET',
        headers: {
          accept: 'application/json,text/plain,*/*',
          'user-agent': 'juhe-ai-proxy-test/0.1'
        },
        agent
      }
      request = forwardProxyRequestOptions
        ? requestFn(requestOptions, handleResponse)
        : requestFn(url, requestOptions, handleResponse)

      function handleResponse(response: import('node:http').IncomingMessage): void {
        responseReceived = true
        const body = new BoundedBufferCollector(maxProxyProbeResponseBytes)
        let responseEnded = false

        response.on('data', (chunk: Buffer) => {
          body.append(chunk)
        })
        response.once('aborted', () => {
          fail(new ProxyProbeTransportFailure('代理检测响应在完整结束前被中止'))
        })
        response.once('error', (error) => {
          fail(new ProxyProbeTransportFailure(`代理检测响应读取失败：${errorMessage(error, '未知读取错误')}`))
        })
        response.once('end', () => {
          responseEnded = true
          if (!response.complete) {
            fail(new ProxyProbeTransportFailure('代理检测响应 framing 不完整'))
            return
          }
          finish({ statusCode: response.statusCode ?? 0, headers: response.headers, body })
        })
        response.once('close', () => {
          if (!responseEnded) {
            fail(new ProxyProbeTransportFailure('代理检测响应在 end 前关闭'))
          }
        })
      }
    } catch (error) {
      fail(new ProxyProbeUnknownFailure(errorMessage(error, '代理检测请求无法创建')))
      return
    }

    request.once('error', (error) => {
      fail(new ProxyProbeTransportFailure(`代理检测连接失败：${errorMessage(error, '未知连接错误')}`))
    })
    request.once('close', () => {
      if (!responseReceived && !settled) {
        fail(new ProxyProbeTransportFailure('代理检测请求在收到响应前关闭'))
      }
    })

    deadlineTimer = setTimeout(() => {
      fail(new ProxyProbeTransportFailure('代理检测请求达到绝对总超时'))
    }, Math.max(1, timeoutMs - (Date.now() - startedAt)))

    try {
      request.end()
    } catch (error) {
      fail(new ProxyProbeUnknownFailure(errorMessage(error, '代理检测请求无法发出')))
    }
  })
}

function forwardHttpProxyRequestOptions(targetUrl: URL, proxyUrl: URL): RequestOptions {
  const absoluteTargetUrl = new URL(targetUrl.href)
  absoluteTargetUrl.hash = ''
  const headers: Record<string, string> = {
    accept: 'application/json,text/plain,*/*',
    'user-agent': 'juhe-ai-proxy-test/0.1',
    host: targetUrl.host,
    connection: 'close',
    'proxy-connection': 'close'
  }
  if (proxyUrl.username || proxyUrl.password) {
    const credentials = `${decodeURIComponent(proxyUrl.username)}:${decodeURIComponent(proxyUrl.password)}`
    headers['proxy-authorization'] = `Basic ${Buffer.from(credentials).toString('base64')}`
  }
  return {
    protocol: proxyUrl.protocol,
    hostname: proxyUrl.hostname,
    port: proxyUrl.port ? Number(proxyUrl.port) : undefined,
    method: 'GET',
    path: absoluteTargetUrl.href,
    headers,
    agent: false
  }
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
      status: 'unknown',
      message: '没有启用的供应商默认地址，未形成代理传输检测'
    }
  }
  const failedCount = providerItems.filter((item) => item.status === 'failed').length
  const unknownCount = providerItems.filter((item) => item.status === 'unknown').length
  const reachableCount = providerItems.filter((item) => item.status === 'passed').length
  const latencyValues = providerItems
    .map((item) => item.latencyMs)
    .filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  const averageLatency = latencyValues.length
    ? Math.round(latencyValues.reduce((sum, value) => sum + value, 0) / latencyValues.length)
    : undefined
  return {
    name: '基础连通性',
    status: failedCount === 0 && unknownCount === 0
      ? 'passed'
      : reachableCount > 0
        ? 'warning'
        : failedCount > 0
          ? 'failed'
          : 'unknown',
    latencyMs: averageLatency,
    message: failedCount === 0 && unknownCount === 0
      ? '全部供应商默认地址可达'
      : reachableCount > 0
        ? `部分供应商默认地址完成传输检测（${reachableCount}/${providerCount}）`
        : failedCount > 0
          ? '供应商默认地址全部发生传输失败'
          : '供应商默认地址均未形成真实传输检测'
  }
}

function summarizeItems(items: ProxyTestItem[]): Pick<ProxyTestReport, 'score' | 'grade' | 'status' | 'passedCount' | 'warningCount' | 'failedCount'> {
  const passedCount = items.filter((item) => item.status === 'passed').length
  const warningCount = items.filter((item) => item.status === 'warning').length
  const failedCount = items.filter((item) => item.status === 'failed').length
  const unknownCount = items.filter((item) => item.status === 'unknown').length
  const status: ProxyTestOverallStatus = failedCount > 0
    ? 'failed'
    : warningCount > 0 || (passedCount > 0 && unknownCount > 0)
      ? 'warning'
      : unknownCount > 0 || items.length === 0
        ? 'unknown'
        : 'passed'
  const score = status === 'unknown'
    ? 0
    : Math.max(0, Math.round(100 - warningCount * 10 - failedCount * 35))
  return {
    score,
    grade: score >= 90 ? 'A' : score >= 75 ? 'B' : score >= 60 ? 'C' : 'D',
    status,
    passedCount,
    warningCount,
    failedCount
  }
}

function providerMessage(statusCode: number): string {
  return `HTTP ${statusCode}（传输完整，状态码仅供诊断）`
}

function reportMessage(status: ProxyTestOverallStatus, failedCount: number, warningCount: number): string {
  if (status === 'passed') return '代理质量检测通过'
  if (status === 'warning') return `代理可用，存在 ${warningCount} 项告警`
  if (status === 'failed') return `代理检测存在 ${failedCount} 项失败`
  return '代理检测未形成有效传输尝试'
}

function refreshFailureState(
  message: string,
  fence: Pick<ProxyTestStateUpdateInput, 'expectedConfigUpdatedAt' | 'lastTestedAt'>
): ProxyTestStateUpdateInput {
  return {
    testStatus: 'unknown',
    latencyMs: null,
    lastTestMessage: message,
    ...fence
  }
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

export const proxyTestServiceTestHooks = {
  testTarget: async (input: {
    name: string
    targetUrl: string
    proxyUrl: string
    deadlineAtMs?: number
  }): Promise<ProxyTestItem> => await testProvider(
    { proxyUrl: input.proxyUrl },
    { name: input.name, baseUrl: input.targetUrl },
    input.deadlineAtMs
  ),
  requestTarget: async (input: {
    targetUrl: string
    proxyUrl: string
    timeoutMs?: number
  }): Promise<HttpProbeResult> => await requestWithProxy(input.targetUrl, input.proxyUrl, {
    timeoutMs: input.timeoutMs
  }),
  baseConnectivityItem,
  summarizeItems,
  refreshFailureState
}

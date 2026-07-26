import assert from 'node:assert/strict'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

type ProbeKind = 'health' | 'management'

interface ProbeConfig {
  baseUrl: string
  healthUrls: string[]
  managementPaths: string[]
  durationSeconds: number
  intervalMs: number
  requestTimeoutMs: number
  cookie?: string
  healthMaxMs: number
  healthP99Ms: number
  managementP95Ms: number
  managementP99Ms: number
  managementMaxMs: number
  minSampleCoverage: number
  maxScheduleDriftMs: number
  rawSampleLimit: number
  reportPath: string
}

interface ProbeTarget {
  kind: ProbeKind
  name: string
  url: string
}

interface ProbeSample {
  kind: ProbeKind
  name: string
  url: string
  scheduledAt: string
  startedAt: string
  status: number
  ok: boolean
  timeout: boolean
  ttfbMs: number
  totalMs: number
  responseBytes: number
  scheduleDriftMs: number
  error?: string
}

interface MetricSummary {
  min: number
  avg: number
  p50: number
  p95: number
  p99: number
  max: number
}

interface TargetReport {
  kind: ProbeKind
  name: string
  url: string
  count: number
  expectedCount: number
  sampleCoverage: number
  success: number
  errors: number
  timeouts: number
  statusCounts: Record<string, number>
  ttfbMs: MetricSummary
  totalMs: MetricSummary
  scheduleDriftMs: MetricSummary
}

interface ProbeReport {
  generatedAt: string
  startedAt: string
  finishedAt: string
  durationMs: number
  config: {
    baseUrl: string
    healthUrls: string[]
    managementPaths: string[]
    durationSeconds: number
    intervalMs: number
    requestTimeoutMs: number
    authMode: 'cookie' | 'auto_login_or_public'
    thresholds: {
      healthMaxMs: number
      healthP99Ms: number
      managementP95Ms: number
      managementP99Ms: number
      managementMaxMs: number
      minSampleCoverage: number
      maxScheduleDriftMs: number
    }
    rawSampleLimit: number
    reportPath: string
  }
  totals: {
    count: number
    expectedCount: number
    sampleCoverage: number
    success: number
    errors: number
    timeouts: number
    unavailableResponses: number
  }
  targets: TargetReport[]
  failures: ProbeSample[]
  sampleTimeline: ProbeSample[]
  sampleTimelineTruncated: boolean
  pass: boolean
  violations: string[]
}

const config = loadConfig()

try {
  const report = await runProbe(config)
  outputReport(report)
  printSummary(report)
  process.exit(report.pass ? 0 : 1)
} catch (error) {
  console.error(error instanceof Error ? error.stack ?? error.message : error)
  process.exit(1)
}

async function runProbe(input: ProbeConfig): Promise<ProbeReport> {
  const targets = buildTargets(input)
  const samples: ProbeSample[] = []
  const startedAt = new Date()
  const startedAtMs = performance.now()
  const endAtMs = startedAtMs + input.durationSeconds * 1000

  await Promise.all(targets.map((target) => runTargetLane(target, input, startedAtMs, endAtMs, samples)))

  const finishedAt = new Date()
  return buildReport(input, startedAt, finishedAt, performance.now() - startedAtMs, targets, samples)
}

async function runTargetLane(
  target: ProbeTarget,
  input: ProbeConfig,
  startedAtMs: number,
  endAtMs: number,
  samples: ProbeSample[]
): Promise<void> {
  let sequence = 0
  while (true) {
    const scheduledAtMs = startedAtMs + sequence * input.intervalMs
    if (scheduledAtMs >= endAtMs) return
    await sleep(Math.max(0, scheduledAtMs - performance.now()))
    const startedProbeAtMs = performance.now()
    const scheduledAt = new Date(Date.now() - (startedProbeAtMs - scheduledAtMs)).toISOString()
    const startedAt = new Date().toISOString()
    samples.push(await probeTarget(target, input, scheduledAt, startedAt, scheduledAtMs, startedProbeAtMs))
    const nextSequence = sequence + 1
    const firstFutureSequence = Math.floor((performance.now() - startedAtMs) / input.intervalMs) + 1
    sequence = Math.max(nextSequence, firstFutureSequence)
  }
}

async function probeTarget(
  target: ProbeTarget,
  input: ProbeConfig,
  scheduledAt: string,
  startedAt: string,
  scheduledAtMs: number,
  startedAtMs: number
): Promise<ProbeSample> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(new Error('probe timeout')), input.requestTimeoutMs)
  timeout.unref()
  let responseStatus = 0
  let responseTtfbMs: number | undefined
  try {
    const headers: Record<string, string> = {
      accept: 'application/json',
      'user-agent': 'juhe-ai-performance-control-plane-probe'
    }
    if (input.cookie) headers.cookie = input.cookie
    const response = await fetch(target.url, {
      headers,
      redirect: 'manual',
      signal: controller.signal
    })
    responseStatus = response.status
    responseTtfbMs = performance.now() - startedAtMs
    const body = await response.arrayBuffer()
    const totalMs = performance.now() - startedAtMs
    return {
      kind: target.kind,
      name: target.name,
      url: target.url,
      scheduledAt,
      startedAt,
      status: responseStatus,
      ok: response.status >= 200 && response.status < 300,
      timeout: false,
      ttfbMs: round(responseTtfbMs),
      totalMs: round(totalMs),
      responseBytes: body.byteLength,
      scheduleDriftMs: round(Math.max(0, startedAtMs - scheduledAtMs))
    }
  } catch (error) {
    const timedOut = controller.signal.aborted
    const totalMs = performance.now() - startedAtMs
    return {
      kind: target.kind,
      name: target.name,
      url: target.url,
      scheduledAt,
      startedAt,
      status: responseStatus,
      ok: false,
      timeout: timedOut,
      ttfbMs: round(responseTtfbMs ?? totalMs),
      totalMs: round(totalMs),
      responseBytes: 0,
      scheduleDriftMs: round(Math.max(0, startedAtMs - scheduledAtMs)),
      error: timedOut ? `请求超过 ${input.requestTimeoutMs}ms` : errorText(error)
    }
  } finally {
    clearTimeout(timeout)
  }
}

function buildTargets(input: ProbeConfig): ProbeTarget[] {
  const healthTargets = input.healthUrls.map((url, index) => ({
    kind: 'health' as const,
    name: `health-${index + 1}`,
    url
  }))
  const managementTargets = input.managementPaths.map((path, index) => ({
    kind: 'management' as const,
    name: `management-${index + 1}:${managementPathName(path)}`,
    url: new URL(path, `${input.baseUrl}/`).toString()
  }))
  return [...healthTargets, ...managementTargets]
}

function buildReport(
  input: ProbeConfig,
  startedAt: Date,
  finishedAt: Date,
  durationMs: number,
  targets: ProbeTarget[],
  samples: ProbeSample[]
): ProbeReport {
  const expectedPerTarget = expectedSampleCount(input.durationSeconds, input.intervalMs)
  const targetReports = targets.map((target) => summarizeTarget(
    target,
    samples.filter((sample) => sample.kind === target.kind && sample.url === target.url),
    expectedPerTarget
  ))
  const violations: string[] = []
  for (const target of targetReports) {
    if (target.count === 0) violations.push(`${target.name} 没有探针样本`)
    if (target.sampleCoverage < input.minSampleCoverage) {
      violations.push(`${target.name} 样本覆盖率 ${target.sampleCoverage} < ${input.minSampleCoverage}（${target.count}/${target.expectedCount}）`)
    }
    if (target.scheduleDriftMs.max > input.maxScheduleDriftMs) {
      violations.push(`${target.name} 调度 drift max ${target.scheduleDriftMs.max}ms > ${input.maxScheduleDriftMs}ms`)
    }
    if (target.errors > 0) violations.push(`${target.name} 存在 ${target.errors} 个失败请求`)
    if (target.timeouts > 0) violations.push(`${target.name} 存在 ${target.timeouts} 个超时请求`)
    if (target.kind === 'health') {
      if (target.totalMs.p99 > input.healthP99Ms) {
        violations.push(`${target.name} health P99 ${target.totalMs.p99}ms > ${input.healthP99Ms}ms`)
      }
      if (target.totalMs.max > input.healthMaxMs) {
        violations.push(`${target.name} health max ${target.totalMs.max}ms > ${input.healthMaxMs}ms`)
      }
    } else {
      if (target.totalMs.p95 > input.managementP95Ms) {
        violations.push(`${target.name} management P95 ${target.totalMs.p95}ms > ${input.managementP95Ms}ms`)
      }
      if (target.totalMs.p99 > input.managementP99Ms) {
        violations.push(`${target.name} management P99 ${target.totalMs.p99}ms > ${input.managementP99Ms}ms`)
      }
      if (target.totalMs.max > input.managementMaxMs) {
        violations.push(`${target.name} management max ${target.totalMs.max}ms > ${input.managementMaxMs}ms`)
      }
    }
  }
  const failures = samples.filter((sample) => !sample.ok)
  const unavailableResponses = samples.filter((sample) => sample.status === 503 || sample.status === 504).length
  const totalExpectedSamples = expectedPerTarget * targets.length
  const sampleTimeline = boundedSampleTimeline(samples, input.rawSampleLimit)
  if (unavailableResponses > 0) violations.push(`探针观察到 ${unavailableResponses} 个 HTTP 503/504`)

  return {
    generatedAt: new Date().toISOString(),
    startedAt: startedAt.toISOString(),
    finishedAt: finishedAt.toISOString(),
    durationMs: round(durationMs),
    config: {
      baseUrl: input.baseUrl,
      healthUrls: input.healthUrls,
      managementPaths: input.managementPaths,
      durationSeconds: input.durationSeconds,
      intervalMs: input.intervalMs,
      requestTimeoutMs: input.requestTimeoutMs,
      authMode: input.cookie ? 'cookie' : 'auto_login_or_public',
      thresholds: {
        healthMaxMs: input.healthMaxMs,
        healthP99Ms: input.healthP99Ms,
        managementP95Ms: input.managementP95Ms,
        managementP99Ms: input.managementP99Ms,
        managementMaxMs: input.managementMaxMs,
        minSampleCoverage: input.minSampleCoverage,
        maxScheduleDriftMs: input.maxScheduleDriftMs
      },
      rawSampleLimit: input.rawSampleLimit,
      reportPath: input.reportPath
    },
    totals: {
      count: samples.length,
      expectedCount: totalExpectedSamples,
      sampleCoverage: totalExpectedSamples > 0 ? round(samples.length / totalExpectedSamples) : 0,
      success: samples.filter((sample) => sample.ok).length,
      errors: failures.length,
      timeouts: samples.filter((sample) => sample.timeout).length,
      unavailableResponses
    },
    targets: targetReports,
    failures: failures.slice(0, 100),
    sampleTimeline,
    sampleTimelineTruncated: sampleTimeline.length < samples.length,
    pass: violations.length === 0,
    violations
  }
}

function summarizeTarget(target: ProbeTarget, samples: ProbeSample[], expectedCount: number): TargetReport {
  const statusCounts: Record<string, number> = {}
  for (const sample of samples) {
    const key = String(sample.status)
    statusCounts[key] = (statusCounts[key] ?? 0) + 1
  }
  return {
    kind: target.kind,
    name: target.name,
    url: target.url,
    count: samples.length,
    expectedCount,
    sampleCoverage: expectedCount > 0 ? round(samples.length / expectedCount) : 0,
    success: samples.filter((sample) => sample.ok).length,
    errors: samples.filter((sample) => !sample.ok).length,
    timeouts: samples.filter((sample) => sample.timeout).length,
    statusCounts,
    ttfbMs: metricSummary(samples.map((sample) => sample.ttfbMs)),
    totalMs: metricSummary(samples.map((sample) => sample.totalMs)),
    scheduleDriftMs: metricSummary(samples.map((sample) => sample.scheduleDriftMs))
  }
}

function outputReport(report: ProbeReport): void {
  mkdirSync(dirname(report.config.reportPath), { recursive: true })
  writeFileSync(report.config.reportPath, `${JSON.stringify(report, null, 2)}\n`, 'utf8')
}

function printSummary(report: ProbeReport): void {
  console.log(`performance-control-plane-probe ${report.pass ? 'passed' : 'failed'}`)
  console.log(`report=${report.config.reportPath}`)
  console.log(`requests=${report.totals.count}/${report.totals.expectedCount} coverage=${report.totals.sampleCoverage} success=${report.totals.success} errors=${report.totals.errors} timeouts=${report.totals.timeouts} unavailable=${report.totals.unavailableResponses}`)
  for (const target of report.targets) {
    console.log(`${target.name} total p95=${target.totalMs.p95}ms p99=${target.totalMs.p99}ms max=${target.totalMs.max}ms ttfbP99=${target.ttfbMs.p99}ms driftMax=${target.scheduleDriftMs.max}ms statuses=${JSON.stringify(target.statusCounts)}`)
  }
  if (report.violations.length > 0) console.log(`violations=${report.violations.join(' | ')}`)
}

function loadConfig(): ProbeConfig {
  const baseUrl = loopbackBaseUrl(envText('JUHE_AI_CONTROL_PROBE_BASE_URL', 'http://127.0.0.1:43099'))
  const defaultHealthUrls = [
    `${baseUrl}/__aisys__/health`,
    `${baseUrl}/__aisys__/api/health`
  ]
  const healthUrls = unique([
    ...urlList(process.env.JUHE_AI_CONTROL_PROBE_HEALTH_URLS, defaultHealthUrls),
    ...urlList(process.env.JUHE_AI_CONTROL_PROBE_GATEWAY_HEALTH_URLS, [])
  ]).map(loopbackUrl)
  assert.ok(healthUrls.length > 0, '至少需要配置一个 health URL')
  const managementPaths = unique(pathList(
    process.env.JUHE_AI_CONTROL_PROBE_MANAGEMENT_PATHS,
    [
      '/__aisys__/api/usage-records?page=1&pageSize=20&result=all&systemAccountId=sys_admin',
      '/__aisys__/api/stats/usage-overview',
      '/__aisys__/api/accounts?page=1&pageSize=20&status=all&sorts=priority:asc',
      '/__aisys__/api/audit-logs?page=1&pageSize=20',
      '/__aisys__/api/runtime-logs?page=1&pageSize=20'
    ]
  ).map(managementPath))
  assert.ok(managementPaths.length > 0, '至少需要配置一个 management path')
  const cookie = optionalCookie(process.env.JUHE_AI_CONTROL_PROBE_COOKIE)
  const timestamp = new Date().toISOString().replace(/[:.]/g, '-')
  return {
    baseUrl,
    healthUrls,
    managementPaths,
    durationSeconds: envInteger('JUHE_AI_CONTROL_PROBE_DURATION_SECONDS', 600, 1, 3600),
    intervalMs: envInteger('JUHE_AI_CONTROL_PROBE_INTERVAL_MS', 500, 100, 60_000),
    requestTimeoutMs: envInteger('JUHE_AI_CONTROL_PROBE_REQUEST_TIMEOUT_MS', 5000, 100, 120_000),
    ...(cookie ? { cookie } : {}),
    healthMaxMs: envNumber('JUHE_AI_CONTROL_PROBE_HEALTH_MAX_MS', 500, 1, 120_000),
    healthP99Ms: envNumber('JUHE_AI_CONTROL_PROBE_HEALTH_P99_MS', 250, 1, 120_000),
    managementP95Ms: envNumber('JUHE_AI_CONTROL_PROBE_MANAGEMENT_P95_MS', 500, 1, 120_000),
    managementP99Ms: envNumber('JUHE_AI_CONTROL_PROBE_MANAGEMENT_P99_MS', 1000, 1, 120_000),
    managementMaxMs: envNumber('JUHE_AI_CONTROL_PROBE_MANAGEMENT_MAX_MS', 2000, 1, 120_000),
    minSampleCoverage: envNumber('JUHE_AI_CONTROL_PROBE_MIN_SAMPLE_COVERAGE', 0.95, 0.01, 1),
    maxScheduleDriftMs: envNumber('JUHE_AI_CONTROL_PROBE_MAX_SCHEDULE_DRIFT_MS', 250, 0, 120_000),
    rawSampleLimit: envInteger('JUHE_AI_CONTROL_PROBE_RAW_SAMPLE_LIMIT', 200, 1, 10_000),
    reportPath: resolve(envText(
      'JUHE_AI_CONTROL_PROBE_REPORT_PATH',
      resolve('..', 'reports', `performance-control-plane-probe-${timestamp}.json`)
    ))
  }
}

function loopbackBaseUrl(value: string): string {
  const parsed = validatedLoopbackUrl(value)
  assert.ok(parsed.pathname === '/' || parsed.pathname === '', '探针 base URL 不允许携带路径')
  assert.equal(parsed.search, '', '探针 base URL 不允许携带查询参数')
  assert.equal(parsed.hash, '', '探针 base URL 不允许携带 fragment')
  return parsed.origin
}

function loopbackUrl(value: string): string {
  const parsed = validatedLoopbackUrl(value)
  assert.equal(parsed.hash, '', '探针 URL 不允许携带 fragment')
  return parsed.toString().replace(/\/$/, '')
}

function validatedLoopbackUrl(value: string): URL {
  const parsed = new URL(value)
  assert.ok(parsed.protocol === 'http:' || parsed.protocol === 'https:', '探针 URL 只允许 http/https')
  assert.ok(['127.0.0.1', 'localhost', '::1', '[::1]'].includes(parsed.hostname), '探针 URL 只允许回环地址')
  assert.equal(parsed.username, '', '探针 URL 不允许携带用户名')
  assert.equal(parsed.password, '', '探针 URL 不允许携带密码')
  return parsed
}

function managementPath(value: string): string {
  assert.ok(value.startsWith('/__aisys__/api/'), `management path 必须位于 /__aisys__/api/：${value}`)
  const parsed = new URL(value, 'http://127.0.0.1')
  assert.equal(parsed.origin, 'http://127.0.0.1', 'management path 必须为相对路径')
  return `${parsed.pathname}${parsed.search}`
}

function optionalCookie(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  if (!normalized) return undefined
  assert.ok(normalized.length <= 8192, '探针 cookie 过长')
  assert.doesNotMatch(normalized, /[\r\n]/, '探针 cookie 不允许换行')
  return normalized
}

function urlList(value: string | undefined, fallback: string[]): string[] {
  return textList(value, fallback)
}

function pathList(value: string | undefined, fallback: string[]): string[] {
  return textList(value, fallback)
}

function textList(value: string | undefined, fallback: string[]): string[] {
  if (!value?.trim()) return [...fallback]
  return value.split(',').map((item) => item.trim()).filter(Boolean)
}

function unique(values: string[]): string[] {
  return Array.from(new Set(values))
}

function managementPathName(path: string): string {
  const parsed = new URL(path, 'http://127.0.0.1')
  return parsed.pathname.replace('/__aisys__/api/', '').replaceAll('/', '-') || 'root'
}

function expectedSampleCount(durationSeconds: number, intervalMs: number): number {
  return Math.ceil((durationSeconds * 1000) / intervalMs)
}

function boundedSampleTimeline(samples: ProbeSample[], limit: number): ProbeSample[] {
  const timeline = [...samples].sort((left, right) => Date.parse(left.scheduledAt) - Date.parse(right.scheduledAt))
  if (timeline.length <= limit) return timeline
  if (limit === 1) return [timeline[timeline.length - 1]!]
  const selected: ProbeSample[] = []
  for (let index = 0; index < limit; index += 1) {
    const sourceIndex = Math.round((index * (timeline.length - 1)) / (limit - 1))
    selected.push(timeline[sourceIndex]!)
  }
  return selected
}

function metricSummary(values: number[]): MetricSummary {
  if (values.length === 0) return { min: 0, avg: 0, p50: 0, p95: 0, p99: 0, max: 0 }
  const sorted = [...values].sort((left, right) => left - right)
  return {
    min: round(sorted[0] ?? 0),
    avg: round(sorted.reduce((total, value) => total + value, 0) / sorted.length),
    p50: round(percentile(sorted, 0.50)),
    p95: round(percentile(sorted, 0.95)),
    p99: round(percentile(sorted, 0.99)),
    max: round(sorted[sorted.length - 1] ?? 0)
  }
}

function percentile(sorted: number[], ratio: number): number {
  if (sorted.length === 0) return 0
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * ratio) - 1))
  return sorted[index] ?? 0
}

function envText(name: string, fallback: string): string {
  return process.env[name]?.trim() || fallback
}

function envInteger(name: string, fallback: number, min: number, max: number): number {
  const parsed = Number(process.env[name])
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, Math.trunc(parsed)))
}

function envNumber(name: string, fallback: number, min: number, max: number): number {
  const parsed = Number(process.env[name])
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, parsed))
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function round(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.round(value * 100) / 100
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms))
}

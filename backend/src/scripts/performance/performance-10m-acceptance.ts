import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

interface ChildResult {
  code: number
  stdoutTail: string
  stderrTail: string
}

interface ResourceSample {
  sampledAt: string
  gatewayInstanceIds: string[]
  gatewayRequestCounts: Record<string, number>
  roles: string[]
  [key: string]: unknown
}

const confirmation = process.env.JUHE_AI_PERFORMANCE_ACCEPTANCE_CONFIRM?.trim()
assert.equal(
  confirmation,
  'external-3-gateway-independent-probe',
  '正式验收必须显式设置 JUHE_AI_PERFORMANCE_ACCEPTANCE_CONFIRM=external-3-gateway-independent-probe'
)

const ingressUrl = loopbackOrigin(requiredEnv('JUHE_AI_PERFORMANCE_ACCEPTANCE_GATEWAY_INGRESS_URL'))
const controlBaseUrl = loopbackOrigin(requiredEnv('JUHE_AI_PERFORMANCE_ACCEPTANCE_CONTROL_BASE_URL'))
const gatewayHealthUrls = commaList(requiredEnv('JUHE_AI_PERFORMANCE_ACCEPTANCE_GATEWAY_HEALTH_URLS')).map(loopbackUrl)
assert.equal(gatewayHealthUrls.length, 3, '正式验收必须配置恰好 3 个 Gateway 直连 health URL')
assert.equal(new Set(gatewayHealthUrls.map((url) => new URL(url).origin)).size, 3, '3 个 Gateway health URL 必须来自 3 个不同端口/origin')
assert.ok(gatewayHealthUrls.every((url) => url.endsWith('/__aisys__/health')), 'Gateway health URL 必须指向 /__aisys__/health')
assert.notEqual(ingressUrl, controlBaseUrl, 'Gateway ingress 与 control base URL 必须使用独立 origin')
assert.ok(gatewayHealthUrls.every((url) => new URL(url).origin !== ingressUrl), '正式负载必须走独立 ingress，不能把单 Gateway 直连端口冒充入口')
assert.ok(gatewayHealthUrls.every((url) => new URL(url).origin !== controlBaseUrl), 'Gateway 与 control 必须使用独立 origin')

const durationSeconds = envInteger('JUHE_AI_PERFORMANCE_ACCEPTANCE_DURATION_SECONDS', 600, 600, 3600)
const settleSeconds = envInteger('JUHE_AI_PERFORMANCE_ACCEPTANCE_SETTLE_SECONDS', 60, 10, 600)
const reportDirectory = resolve(requiredEnv('JUHE_AI_PERFORMANCE_ACCEPTANCE_REPORT_DIR'))
const resourceSamplesPath = resolve(requiredEnv('JUHE_AI_PERFORMANCE_ACCEPTANCE_RESOURCE_SAMPLES_PATH'))
mkdirSync(reportDirectory, { recursive: true })
const timestamp = new Date().toISOString().replace(/[:.]/g, '-')
const loadReportPath = resolve(reportDirectory, `performance-gateway-load-${timestamp}.json`)
const probeReportPath = resolve(reportDirectory, `performance-control-plane-probe-${timestamp}.json`)
const combinedReportPath = resolve(reportDirectory, `performance-10m-acceptance-${timestamp}.json`)
const resourceChecklistPath = resolve(reportDirectory, `performance-resource-collection-${timestamp}.txt`)
const acceptanceStartedAtMs = Date.now()

writeFileSync(resourceChecklistPath, resourceCollectionChecklist(resourceSamplesPath, durationSeconds), 'utf8')
console.log(`外部资源采集命令/格式清单：${resourceChecklistPath}`)
console.log('此入口不会伪装成 sysstat/ps/Redis/PostgreSQL 采集器；必须由独立 shell 在测试期间写入指定 JSONL。')

const commonEnv = { ...process.env }
const probeDurationSeconds = durationSeconds + settleSeconds + 30
const probePromise = runChild('src/scripts/performance/performance-control-plane-probe.ts', {
  ...commonEnv,
  JUHE_AI_CONTROL_PROBE_BASE_URL: controlBaseUrl,
  JUHE_AI_CONTROL_PROBE_GATEWAY_HEALTH_URLS: gatewayHealthUrls.join(','),
  JUHE_AI_CONTROL_PROBE_DURATION_SECONDS: String(probeDurationSeconds),
  JUHE_AI_CONTROL_PROBE_MIN_SAMPLE_COVERAGE: process.env.JUHE_AI_CONTROL_PROBE_MIN_SAMPLE_COVERAGE ?? '0.95',
  JUHE_AI_CONTROL_PROBE_MAX_SCHEDULE_DRIFT_MS: process.env.JUHE_AI_CONTROL_PROBE_MAX_SCHEDULE_DRIFT_MS ?? '250',
  JUHE_AI_CONTROL_PROBE_REPORT_PATH: probeReportPath
})

await sleep(1000)
const loadPromise = runChild('src/scripts/performance/performance-gateway-load-test.ts', {
  ...commonEnv,
  JUHE_AI_GATEWAY_LOAD_BASE_URL: ingressUrl,
  JUHE_AI_GATEWAY_LOAD_DURATION_SECONDS: String(durationSeconds),
  JUHE_AI_GATEWAY_LOAD_WARMUP_SECONDS: '0',
  JUHE_AI_GATEWAY_LOAD_SETTLE_SECONDS: String(settleSeconds),
  JUHE_AI_GATEWAY_LOAD_SCENARIOS: 'responses,chat,responses_stream',
  JUHE_AI_GATEWAY_LOAD_REQUEST_SHAPE: 'historical_responses',
  JUHE_AI_GATEWAY_LOAD_PROMPT_SIZE_PROFILE: 'historical',
  JUHE_AI_GATEWAY_LOAD_MAX_ERROR_RATE: '0',
  JUHE_AI_GATEWAY_LOAD_MAX_REDIS_PENDING: '0',
  JUHE_AI_GATEWAY_LOAD_ASSERT_ACCOUNT_CONCURRENCY: 'true',
  JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_P95_MS: process.env.JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_P95_MS ?? '500',
  JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_P99_MS: process.env.JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_P99_MS ?? '1000',
  JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_MAX_MS: process.env.JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_MAX_MS ?? '2000',
  JUHE_AI_GATEWAY_LOAD_MAX_SSE_TTFB_P99_MS: process.env.JUHE_AI_GATEWAY_LOAD_MAX_SSE_TTFB_P99_MS ?? '500',
  JUHE_AI_GATEWAY_LOAD_MAX_SSE_TTFB_MAX_MS: process.env.JUHE_AI_GATEWAY_LOAD_MAX_SSE_TTFB_MAX_MS ?? '1000',
  JUHE_AI_GATEWAY_LOAD_REPORT_PATH: loadReportPath
})

const [load, probe] = await Promise.all([loadPromise, probePromise])
let resourceAnalysis: Record<string, unknown>
let resourceError: string | undefined
try {
  resourceAnalysis = analyzeResourceSamples(resourceSamplesPath, durationSeconds, acceptanceStartedAtMs)
} catch (error) {
  resourceError = error instanceof Error ? error.message : String(error)
  resourceAnalysis = { pass: false, error: resourceError }
}
const pass = load.code === 0 && probe.code === 0 && resourceError === undefined
const report = {
  generatedAt: new Date().toISOString(),
  pass,
  topology: {
    ingressUrl,
    controlBaseUrl,
    gatewayHealthUrls,
    externalGatewayCount: 3,
    independentProbeProcess: true,
    singleServerFallbackAllowed: false
  },
  durationSeconds,
  settleSeconds,
  artifacts: { loadReportPath, probeReportPath, resourceSamplesPath, resourceChecklistPath },
  children: { load, probe },
  resourceAnalysis,
  violations: [
    ...(load.code === 0 ? [] : [`gateway load 子进程退出码 ${load.code}`]),
    ...(probe.code === 0 ? [] : [`control probe 子进程退出码 ${probe.code}`]),
    ...(resourceError ? [`外部资源证据无效：${resourceError}`] : [])
  ]
}
writeFileSync(combinedReportPath, `${JSON.stringify(report, null, 2)}\n`, 'utf8')
console.log(`10 分钟正式验收汇总：${combinedReportPath}`)
process.exit(pass ? 0 : 1)

function analyzeResourceSamples(path: string, minimumDurationSeconds: number, acceptanceStartedAtMs: number): Record<string, unknown> {
  assert.ok(existsSync(path), `缺少外部资源 JSONL：${path}`)
  assert.ok(statSync(path).size > 0, `外部资源 JSONL 为空：${path}`)
  const samples = readFileSync(path, 'utf8')
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line, index) => parseResourceSample(line, index + 1))
    .sort((left, right) => Date.parse(left.sampledAt) - Date.parse(right.sampledAt))
  assert.ok(samples.length >= Math.ceil(minimumDurationSeconds / 10), `外部资源样本不足：${samples.length}`)
  const firstAt = Date.parse(samples[0]!.sampledAt)
  const lastAt = Date.parse(samples[samples.length - 1]!.sampledAt)
  assert.ok(Number.isFinite(firstAt) && Number.isFinite(lastAt), '外部资源样本 sampledAt 无效')
  assert.ok(lastAt - firstAt >= (minimumDurationSeconds - 10) * 1000, '外部资源样本时间覆盖不足 10 分钟正式 load 窗口')
  assert.ok(firstAt >= acceptanceStartedAtMs - 10_000, '外部资源样本开始时间早于本轮验收，不能复用历史证据')
  assert.ok(firstAt <= acceptanceStartedAtMs + 10_000, '外部资源样本开始过晚，未覆盖正式验收启动窗口')
  assert.ok(lastAt >= acceptanceStartedAtMs + (minimumDurationSeconds - 10) * 1000, '外部资源样本结束过早，未覆盖正式 load 窗口')
  assert.ok(lastAt <= Date.now() + 10_000, '外部资源样本包含未来时间，证据无效')
  for (const [index, sample] of samples.entries()) {
    assert.equal(new Set(sample.gatewayInstanceIds).size, 3, `资源样本 ${index + 1} 未覆盖 3 个唯一 Gateway`)
    assertRoleCounts(sample.roles, index + 1)
  }
  const firstSample = samples[0]!
  const lastSample = samples[samples.length - 1]!
  for (const gatewayId of lastSample.gatewayInstanceIds) {
    const delta = Number(lastSample.gatewayRequestCounts[gatewayId] ?? 0) - Number(firstSample.gatewayRequestCounts[gatewayId] ?? 0)
    assert.ok(delta > 0, `Gateway ${gatewayId} 在 ingress 正式 load 期间没有请求计数增量`)
  }
  return {
    pass: true,
    sampleCount: samples.length,
    firstSampleAt: samples[0]!.sampledAt,
    lastSampleAt: samples[samples.length - 1]!.sampledAt,
    coverageSeconds: Math.round((lastAt - firstAt) / 1000),
    boundedSamples: boundedSamples(samples, 20)
  }
}

function parseResourceSample(line: string, lineNumber: number): ResourceSample {
  const value = JSON.parse(line) as Partial<ResourceSample>
  assert.equal(typeof value.sampledAt, 'string', `资源 JSONL 第 ${lineNumber} 行缺少 sampledAt`)
  assert.ok(Array.isArray(value.gatewayInstanceIds), `资源 JSONL 第 ${lineNumber} 行缺少 gatewayInstanceIds`)
  assert.ok(
    value.gatewayRequestCounts && typeof value.gatewayRequestCounts === 'object' && !Array.isArray(value.gatewayRequestCounts),
    `资源 JSONL 第 ${lineNumber} 行缺少 gatewayRequestCounts`
  )
  assert.ok(Array.isArray(value.roles), `资源 JSONL 第 ${lineNumber} 行缺少 roles`)
  return value as ResourceSample
}

function assertRoleCounts(roles: string[], sampleNumber: number): void {
  const counts = (prefix: string) => new Set(roles.filter((role) => role.startsWith(prefix))).size
  assert.ok(counts('gateway:') >= 3, `资源样本 ${sampleNumber} gateway 角色不足 3`)
  assert.ok(counts('usage-worker:') >= 2, `资源样本 ${sampleNumber} usage-worker 角色不足 2`)
  assert.ok(counts('log-worker:') >= 2, `资源样本 ${sampleNumber} log-worker 角色不足 2`)
  for (const prefix of ['control:', 'db-service:', 'stats-worker:', 'ops-worker:']) {
    assert.ok(counts(prefix) >= 1, `资源样本 ${sampleNumber} 缺少 ${prefix} 角色`)
  }
}

function boundedSamples(samples: ResourceSample[], limit: number): ResourceSample[] {
  if (samples.length <= limit) return samples
  return Array.from({ length: limit }, (_, index) => samples[Math.round((index * (samples.length - 1)) / (limit - 1))]!)
}

function runChild(script: string, env: NodeJS.ProcessEnv): Promise<ChildResult> {
  return new Promise((resolveChild, rejectChild) => {
    const child = spawn(process.execPath, ['--import', 'tsx', script], {
      cwd: resolve('.'),
      env,
      stdio: ['ignore', 'pipe', 'pipe'],
      windowsHide: true
    })
    let stdoutTail = ''
    let stderrTail = ''
    child.stdout.on('data', (chunk: Buffer) => {
      const text = chunk.toString('utf8')
      process.stdout.write(text)
      stdoutTail = boundedText(stdoutTail + text)
    })
    child.stderr.on('data', (chunk: Buffer) => {
      const text = chunk.toString('utf8')
      process.stderr.write(text)
      stderrTail = boundedText(stderrTail + text)
    })
    child.once('error', rejectChild)
    child.once('exit', (code) => resolveChild({ code: code ?? 1, stdoutTail, stderrTail }))
  })
}

function resourceCollectionChecklist(path: string, duration: number): string {
  return [
    '高性能 10 分钟验收外部资源采集清单',
    '',
    `目标 JSONL：${path}`,
    `覆盖窗口：至少 ${duration} 秒，建议每 5 秒一个样本。`,
    '必须由独立 shell/采集器完成；不要在 Gateway、mock upstream 或控制面探针进程内采样。',
    '可使用 macOS ps/top/vm_stat、Redis CLI、psql 或现有监控导出，但本脚本不会假装执行这些系统工具。',
    '每行必须是一个 JSON 对象，至少包含：',
    '{"sampledAt":"ISO-8601","gatewayInstanceIds":["gateway-1","gateway-2","gateway-3"],"gatewayRequestCounts":{"gateway-1":0,"gateway-2":0,"gateway-3":0},"roles":["gateway:gateway-1","gateway:gateway-2","gateway:gateway-3","control:control-1","db-service:db-service-1","usage-worker:1","usage-worker:2","log-worker:1","log-worker:2","stats-worker:1","ops-worker:1"],"cpu":{},"memory":{},"eventLoop":{},"redis":{},"postgres":{}}',
    '最后一个样本相对第一个样本，三个 gatewayRequestCounts 都必须有正增量，用于证明 ingress 真实分发到完整 3 Gateway。',
    '禁止把 Redis/PostgreSQL 密码、Cookie、API Key 或完整连接串写入 JSONL。',
    ''
  ].join('\n')
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  assert.ok(value, `缺少 ${name}`)
  return value
}

function loopbackOrigin(value: string): string {
  const parsed = validatedLoopbackUrl(value)
  assert.ok(parsed.pathname === '/' || parsed.pathname === '', 'base URL 不允许携带路径')
  assert.equal(parsed.search, '', 'base URL 不允许携带查询参数')
  return parsed.origin
}

function loopbackUrl(value: string): string {
  const parsed = validatedLoopbackUrl(value)
  assert.equal(parsed.hash, '', 'URL 不允许 fragment')
  return parsed.toString().replace(/\/$/, '')
}

function validatedLoopbackUrl(value: string): URL {
  const parsed = new URL(value)
  assert.ok(parsed.protocol === 'http:' || parsed.protocol === 'https:', '只允许 http/https URL')
  assert.ok(['127.0.0.1', 'localhost', '::1', '[::1]'].includes(parsed.hostname), '正式验收入口只允许回环地址')
  assert.equal(parsed.username, '', 'URL 不允许用户名')
  assert.equal(parsed.password, '', 'URL 不允许密码')
  return parsed
}

function commaList(value: string): string[] {
  return value.split(',').map((item) => item.trim()).filter(Boolean)
}

function envInteger(name: string, fallback: number, min: number, max: number): number {
  const value = Number(process.env[name] ?? fallback)
  assert.ok(Number.isInteger(value) && value >= min && value <= max, `${name} 必须是 ${min}-${max} 之间的整数`)
  return value
}

function boundedText(value: string): string {
  return value.length <= 20_000 ? value : value.slice(-20_000)
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms))
}

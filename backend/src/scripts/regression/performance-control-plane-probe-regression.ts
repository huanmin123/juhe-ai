import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

interface ProbeReport {
  config: { authMode: string }
  totals: { count: number; expectedCount: number; sampleCoverage: number; errors: number }
  targets: Array<{
    kind: string
    url: string
    count: number
    expectedCount: number
    sampleCoverage: number
    ttfbMs: { p99: number }
    totalMs: { p99: number }
  }>
  sampleTimeline: Array<{ ok: boolean; scheduledAt: string; startedAt: string }>
  sampleTimelineTruncated: boolean
  pass: boolean
  violations: string[]
}

const temporaryDirectory = mkdtempSync(join(tmpdir(), 'juhe-ai-control-probe-'))
let requiredCookie: string | undefined
let observedCookie = false
const server = http.createServer((req, res) => {
  if (requiredCookie) {
    observedCookie ||= req.headers.cookie === requiredCookie
    if (req.headers.cookie !== requiredCookie && req.url?.includes('/__aisys__/api/') && !req.url.endsWith('/health')) {
      res.writeHead(401, { 'content-type': 'application/json' })
      res.end('{"message":"unauthorized"}')
      return
    }
  }
  res.writeHead(200, { 'content-type': 'application/json' })
  res.write('{"data":')
  setTimeout(() => res.end('{"ok":true}}'), req.url?.startsWith('/gateway-1/') ? 350 : 5)
})

try {
  await listen(server)
  const address = server.address()
  assert.ok(address && typeof address === 'object')
  const baseUrl = `http://127.0.0.1:${address.port}`

  const autoLoginReportPath = resolve(temporaryDirectory, 'auto-login.json')
  await runProbe(baseUrl, autoLoginReportPath)
  const autoLoginReport = readReport(autoLoginReportPath)
  assert.equal(autoLoginReport.pass, true)
  assert.equal(autoLoginReport.config.authMode, 'auto_login_or_public')
  assert.equal(autoLoginReport.totals.errors, 0)
  assert.ok(autoLoginReport.totals.count >= 6)
  assert.ok(autoLoginReport.totals.expectedCount > autoLoginReport.totals.count)
  assert.ok(autoLoginReport.totals.sampleCoverage >= 0.5)
  assert.ok(autoLoginReport.sampleTimeline.length > 0)
  assert.ok(autoLoginReport.sampleTimeline.every((sample) => sample.ok))
  assert.ok(autoLoginReport.targets.some((target) => target.kind === 'health' && target.count > 0))
  assert.ok(autoLoginReport.targets.some((target) => target.kind === 'management' && target.count > 0))
  const slowHealthTarget = autoLoginReport.targets.find((target) => target.kind === 'health' && target.url.includes('/gateway-1/'))
  assert.ok(
    slowHealthTarget && slowHealthTarget.count <= 3,
    '慢 health lane 应跳过错过的固定节拍，不得在请求恢复后追赶堆积'
  )
  assert.ok(slowHealthTarget.ttfbMs.p99 < 100, 'TTFB 必须在收到响应头后记录，不得等到完整慢响应结束')
  assert.ok(slowHealthTarget.totalMs.p99 >= 300, 'total latency 必须覆盖完整响应体读取')

  requiredCookie = 'probe_session=probe-secret-cookie'
  const cookieReportPath = resolve(temporaryDirectory, 'cookie.json')
  await runProbe(baseUrl, cookieReportPath, requiredCookie)
  const reportText = readFileSync(cookieReportPath, 'utf8')
  const cookieReport = JSON.parse(reportText) as ProbeReport
  assert.equal(cookieReport.pass, true)
  assert.equal(cookieReport.config.authMode, 'cookie')
  assert.equal(cookieReport.totals.errors, 0)
  assert.equal(observedCookie, true)
  assert.doesNotMatch(reportText, /probe-secret-cookie/)
  assert.ok(cookieReport.targets.every((target) => target.totalMs.p99 < 1000))

  const coverageFailureReportPath = resolve(temporaryDirectory, 'coverage-failure.json')
  await assert.rejects(
    runProbe(baseUrl, coverageFailureReportPath, undefined, { minSampleCoverage: '0.95' }),
    /control-plane probe exited 1/
  )
  const coverageFailureReport = readReport(coverageFailureReportPath)
  assert.equal(coverageFailureReport.pass, false)
  assert.ok(coverageFailureReport.violations.some((violation) => violation.includes('样本覆盖率')))

  console.log('performance control-plane probe regression passed')
} finally {
  await closeServer(server)
  rmSync(temporaryDirectory, { recursive: true, force: true })
}

function runProbe(
  baseUrl: string,
  reportPath: string,
  cookie?: string,
  overrides: { minSampleCoverage?: string; maxScheduleDriftMs?: string } = {}
): Promise<void> {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(process.execPath, ['--import', 'tsx', 'src/scripts/performance/performance-control-plane-probe.ts'], {
      cwd: resolve('.'),
      env: {
        ...process.env,
        JUHE_AI_CONTROL_PROBE_BASE_URL: baseUrl,
        JUHE_AI_CONTROL_PROBE_HEALTH_URLS: `${baseUrl}/__aisys__/health,${baseUrl}/__aisys__/api/health`,
        JUHE_AI_CONTROL_PROBE_GATEWAY_HEALTH_URLS: `${baseUrl}/gateway-1/__aisys__/health`,
        JUHE_AI_CONTROL_PROBE_MANAGEMENT_PATHS: '/__aisys__/api/usage-records?page=1&pageSize=20&result=all,/__aisys__/api/stats/usage-overview',
        JUHE_AI_CONTROL_PROBE_DURATION_SECONDS: '1',
        JUHE_AI_CONTROL_PROBE_INTERVAL_MS: '200',
        JUHE_AI_CONTROL_PROBE_REQUEST_TIMEOUT_MS: '1000',
        JUHE_AI_CONTROL_PROBE_HEALTH_MAX_MS: '1000',
        JUHE_AI_CONTROL_PROBE_HEALTH_P99_MS: '1000',
        JUHE_AI_CONTROL_PROBE_MANAGEMENT_P95_MS: '1000',
        JUHE_AI_CONTROL_PROBE_MANAGEMENT_P99_MS: '1000',
        JUHE_AI_CONTROL_PROBE_MANAGEMENT_MAX_MS: '1000',
        JUHE_AI_CONTROL_PROBE_MIN_SAMPLE_COVERAGE: overrides.minSampleCoverage ?? '0.5',
        JUHE_AI_CONTROL_PROBE_MAX_SCHEDULE_DRIFT_MS: overrides.maxScheduleDriftMs ?? '1000',
        JUHE_AI_CONTROL_PROBE_RAW_SAMPLE_LIMIT: '4',
        JUHE_AI_CONTROL_PROBE_REPORT_PATH: reportPath,
        ...(cookie ? { JUHE_AI_CONTROL_PROBE_COOKIE: cookie } : {})
      },
      stdio: ['ignore', 'pipe', 'pipe'],
      windowsHide: true
    })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (chunk: Buffer) => { stdout = boundedOutput(stdout + chunk.toString('utf8')) })
    child.stderr.on('data', (chunk: Buffer) => { stderr = boundedOutput(stderr + chunk.toString('utf8')) })
    child.once('error', rejectRun)
    child.once('exit', (code) => {
      if (code === 0) {
        resolveRun()
        return
      }
      rejectRun(new Error(`control-plane probe exited ${code}\nstdout=${stdout}\nstderr=${stderr}`))
    })
  })
}

function readReport(path: string): ProbeReport {
  return JSON.parse(readFileSync(path, 'utf8')) as ProbeReport
}

function boundedOutput(value: string): string {
  return value.length <= 16_384 ? value : value.slice(-16_384)
}

function listen(target: http.Server): Promise<void> {
  return new Promise((resolveListen, rejectListen) => {
    target.once('error', rejectListen)
    target.listen(0, '127.0.0.1', () => resolveListen())
  })
}

function closeServer(target: http.Server): Promise<void> {
  return new Promise((resolveClose) => target.close(() => resolveClose()))
}

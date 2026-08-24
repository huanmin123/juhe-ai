import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  assertProxyLatencyReportMatchesProxy,
  parseProxyLatencyHandoverReport,
  proxyLatencyGoHandoverReady,
  proxyLatencyGoOwnerEnabled,
  proxyLatencyManualBridgeEnabled,
  proxyLatencyNodeOwnerEnabled,
  resolveProxyLatencyHandoverGate
} from '../../modules/background/proxy-latency-handover.js'

assert.equal(proxyLatencyGoOwnerEnabled({}), false)
assert.equal(proxyLatencyNodeOwnerEnabled({}), true)
assert.equal(proxyLatencyGoOwnerEnabled({ JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'GO' }), true)
assert.equal(proxyLatencyNodeOwnerEnabled({ JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'go' }), false)
assert.equal(proxyLatencyManualBridgeEnabled({ JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'go', JUHE_AI_PROXY_LATENCY_MANUAL_ENABLED: 'true' }), true)
assert.equal(proxyLatencyManualBridgeEnabled({ JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'go' }), false)
assert.equal(proxyLatencyGoHandoverReady({ JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'go' }), false)
assert.equal(proxyLatencyGoHandoverReady({
  JUHE_AI_PROXY_LATENCY_JOBS_OWNER: 'go',
  JUHE_AI_PROXY_LATENCY_MANUAL_ENABLED: 'true',
  JUHE_AI_PROXY_LATENCY_JOBS_COMMAND_WIRING_READY: 'true',
  JUHE_AI_PROXY_LATENCY_PROJECTION_READY: 'true',
  JUHE_AI_PROXY_LATENCY_MANUAL_BRIDGE_READY: 'true',
  JUHE_AI_PROXY_LATENCY_NODE_OWNER_DRAINED: 'true'
}), true)

assert.deepEqual(resolveProxyLatencyHandoverGate(), { enabled: false, reason: 'disabled_by_default' })
assert.deepEqual(resolveProxyLatencyHandoverGate({ enabled: true }), { enabled: false, reason: 'go_command_wiring_missing' })
assert.deepEqual(resolveProxyLatencyHandoverGate({ enabled: true, goCommandWiringReady: true }), { enabled: false, reason: 'go_projection_missing' })
assert.deepEqual(resolveProxyLatencyHandoverGate({ enabled: true, goCommandWiringReady: true, goProjectionReady: true }), { enabled: false, reason: 'go_manual_bridge_missing' })
assert.deepEqual(resolveProxyLatencyHandoverGate({ enabled: true, goCommandWiringReady: true, goProjectionReady: true, goManualBridgeReady: true }), { enabled: false, reason: 'node_owner_not_drained' })
assert.deepEqual(resolveProxyLatencyHandoverGate({ enabled: true, goCommandWiringReady: true, goProjectionReady: true, goManualBridgeReady: true, nodeOwnerDrained: true }), { enabled: true })

const report = parseProxyLatencyHandoverReport({
  schemaVersion: 1,
  job: 'proxy-latency',
  report: {
    proxyId: 'proxy-j3a',
    proxyName: 'J3a proxy',
    score: 90,
    grade: 'A',
    status: 'warning',
    passedCount: 1,
    warningCount: 1,
    failedCount: 0,
    baseLatencyMs: 42,
    testedAt: '2026-08-23T00:00:05.123456Z',
    items: [
      { name: '基础连通性', status: 'warning', message: '部分供应商默认地址完成传输检测（1/2）' },
      { name: 'OpenAI', status: 'passed', httpStatus: 200, latencyMs: 42, message: '代理目标检测完成', targetUrl: 'https://api.openai.com/v1' },
      { name: 'Gemini', status: 'unknown', message: 'deadline', targetUrl: 'https://generativelanguage.googleapis.com/v1' }
    ],
    message: '代理可用，存在 1 项告警'
  }
})
assert.equal(report.items[0]?.targetUrl, undefined, 'synthetic base item may omit targetUrl')
assert.equal(report.items[2]?.targetUrl, 'https://generativelanguage.googleapis.com/v1', 'provider unknown item must retain targetUrl')
assert.doesNotThrow(() => assertProxyLatencyReportMatchesProxy(report, 'proxy-j3a'))
assert.throws(() => assertProxyLatencyReportMatchesProxy(report, 'proxy-other'), /proxyId.*不匹配/u)
assert.throws(() => parseProxyLatencyHandoverReport({
  schemaVersion: 1,
  job: 'proxy-latency',
  report: { ...report, unexpected: true }
}), /字段不完整/u)

for (const [field, invalidValue] of [
  ['score', '90'],
  ['grade', 90],
  ['testedAt', 123],
  ['baseLatencyMs', '42']
] as const) {
  assert.throws(() => parseProxyLatencyHandoverReport({
    schemaVersion: 1,
    job: 'proxy-latency',
    report: { ...report, [field]: invalidValue }
  }), /(?:基础字段|baseLatencyMs).*无效/u, `${field} wrong type must fail closed`)
}
assert.throws(() => parseProxyLatencyHandoverReport({
  schemaVersion: 1,
  job: 'proxy-latency',
  report: { ...report, testedAt: '2026-08-23' }
}), /基础字段无效/u, 'testedAt must be strict RFC3339')
assert.throws(() => parseProxyLatencyHandoverReport({
  schemaVersion: 1,
  job: 'proxy-latency',
  report: { ...report, items: report.items.map((item, index) => index === 1 ? { ...item, targetUrl: 42 } : item) }
}), /targetUrl 无效/u, 'targetUrl wrong type must fail closed')

const goldenPath = resolve(import.meta.dirname, '../../../../backend-go/projects/jobs/internal/proxylatency/testdata/j3a-manual-report-golden.json')
const golden = JSON.parse(readFileSync(goldenPath, 'utf8')) as { schemaVersion: number; job: string; report: unknown }
assert.deepEqual(parseProxyLatencyHandoverReport(golden), golden.report, 'Node parser must accept the exact Go manual-report golden fixture')

const schedulerSource = readFileSync(resolve(import.meta.dirname, '../../modules/background/background-jobs.ts'), 'utf8')
assert.match(schedulerSource, /proxyLatencyNodeOwnerEnabled\(\)[\s\S]*proxy-latency-refresh/u, 'Node scheduler must be conditional on the owner gate')
assert.match(schedulerSource, /stopAndDrain\(10_000\)/u, 'Node scheduler must expose bounded stop-and-drain')
assert.match(schedulerSource, /activeCount/u, 'Node scheduler drain must report active count')
const workerSource = readFileSync(resolve(import.meta.dirname, '../../worker.ts'), 'utf8')
assert.match(workerSource, /stopBackgroundJobs\(\)/u, 'worker shutdown must drain background jobs')
const dbServiceSource = readFileSync(resolve(import.meta.dirname, '../../db-service.ts'), 'utf8')
assert.match(dbServiceSource, /startProxyLatencyJobsOutcomeProjectionRuntime\(\)/u, 'db-service must start J3a projector')
assert.match(dbServiceSource, /stopProxyLatencyJobsOutcomeProjectionRuntime\(\)/u, 'db-service must stop J3a projector')
assert.match(dbServiceSource, /JUHE_AI_PROXY_LATENCY_NODE_OWNER_DRAINED/u, 'Go owner must require explicit Node drain evidence')
const routeSource = readFileSync(resolve(import.meta.dirname, '../../modules/proxies/proxies.routes.ts'), 'utf8')
assert.match(routeSource, /proxyLatencyGoHandoverReady\(\)/u, 'manual route must fail closed until the full handoff gate is ready')
const outcomeRepositorySource = readFileSync(resolve(import.meta.dirname, '../../storage/proxy-latency-jobs-outcome.repository.ts'), 'utf8')
assert.match(outcomeRepositorySource, /to_char\(observed_at::timestamptz/u, 'PostgreSQL reader must preserve observed_at microseconds')
console.log('proxy latency handover regression passed')

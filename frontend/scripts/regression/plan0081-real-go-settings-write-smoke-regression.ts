import { strict as assert } from 'node:assert'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

import {
  formatRealGoSettingsWriteSmokeSummary,
  loadRealGoSettingsWriteSmokeConfig,
  realGoSettingsWriteSmokeEnv,
  runRealGoSettingsWriteSmoke,
  runRealGoSettingsWriteSmokeFromEnvironment,
  settingsWriteConfirmationForBaseUrl,
  type RealGoSettingsWriteSmokeEnvironment
} from '../smoke/plan0081-real-go-settings-write-smoke'

type Scenario = 'success' | 'patch-status' | 'missing-header' | 'invalid-json' | 'redirect' | 'disconnect' | 'restore-status'
interface RecordItem { method: string; pathname: string; body: unknown }

const cookie = 'juhe_ai_session=plan0081-settings-cookie-secret'
const settings: Record<string, unknown> = {
  systemMetricsHourlyRetentionDays: 20,
  usageRecordRetentionDays: 30,
  gatewayTextRawBodyLimitMegabytes: 20,
  timezone: 'Asia/Shanghai'
}
const records: RecordItem[] = []
let scenario: Scenario = 'success'
let patchNumber = 0

const server = createServer((req, res) => void handle(req, res))
await listen(server)
try {
  const baseUrl = serverBaseUrl(server)
  await gateTests(baseUrl)
  await successTest(baseUrl)
  await failureTests(baseUrl)
  assertNoLeak(formatRealGoSettingsWriteSmokeSummary({ settingsWriteChecked: true, settingsRestored: true }), baseUrl)
} finally { await close(server) }

console.log('PLAN-0081 real Go settings write smoke regression passed')

async function gateTests(baseUrl: string): Promise<void> {
  records.length = 0
  assert.throws(() => loadRealGoSettingsWriteSmokeConfig({}), /Missing required environment variable/)
  const valid = environment(baseUrl)
  for (const name of Object.values(realGoSettingsWriteSmokeEnv).slice(0, 4)) {
    const missing = { ...valid }; delete missing[name]
    await assert.rejects(runRealGoSettingsWriteSmokeFromEnvironment(missing, () => undefined), /Missing|required|must equal|confirmation/)
    assert.equal(records.length, 0)
  }
  for (const override of [
    { [realGoSettingsWriteSmokeEnv.allow]: '0' },
    { [realGoSettingsWriteSmokeEnv.allow]: 'true' },
    { [realGoSettingsWriteSmokeEnv.confirmation]: 'wrong' }
  ]) {
    await assert.rejects(runRealGoSettingsWriteSmokeFromEnvironment({ ...valid, ...override }, () => undefined))
    assert.equal(records.length, 0)
  }
}

async function successTest(baseUrl: string): Promise<void> {
  reset('success')
  const output: string[] = []
  const summary = await runRealGoSettingsWriteSmokeFromEnvironment(environment(baseUrl), message => output.push(message))
  assert.deepEqual(summary, { settingsWriteChecked: true, settingsRestored: true })
  assert.deepEqual(output, ['settingsWriteChecked=true settingsRestored=true'])
  assert.deepEqual(records.map(item => `${item.method} ${item.pathname}`), [
    'GET /__aisys__/api/settings', 'PATCH /__aisys__/api/settings',
    'GET /__aisys__/api/settings', 'GET /__aisys__/api/settings',
    'PATCH /__aisys__/api/settings', 'GET /__aisys__/api/settings'
  ])
  assert.deepEqual(records.filter(item => item.method === 'PATCH').map(item => item.body), [
    { systemMetricsHourlyRetentionDays: 19 }, { systemMetricsHourlyRetentionDays: 20 }
  ])
}

async function failureTests(baseUrl: string): Promise<void> {
  for (const [name, expected] of [
    ['patch-status', /settings PATCH failed with HTTP 500/],
    ['missing-header', /Cache-Control/],
    ['invalid-json', /settings response DTO/],
    ['redirect', /settings GET failed with HTTP 302/],
    ['disconnect', /settings PATCH request failed/]
  ] as const) {
    reset(name)
    await assert.rejects(runRealGoSettingsWriteSmokeFromEnvironment(environment(baseUrl), () => undefined), expected)
    assert.equal(settings.systemMetricsHourlyRetentionDays, 20, `${name} must restore the setting`)
  }
  reset('restore-status')
  await assert.rejects(runRealGoSettingsWriteSmokeFromEnvironment(environment(baseUrl), () => undefined), /settings PATCH failed with HTTP 500/)
  assert.equal(patchNumber, 2)
}

async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const url = new URL(req.url ?? '/', 'http://localhost')
  const method = req.method ?? 'GET'
  let body: unknown
  if (method === 'PATCH') body = await readJson(req)
  records.push({ method, pathname: url.pathname, body })
  if (method === 'GET') {
    if (scenario === 'redirect') return respond(res, 302, {}, { location: '/redirect' })
    return respond(res, 200, { data: { ...settings } })
  }
  if (scenario === 'disconnect') return res.destroy()
  patchNumber += 1
  if (scenario === 'patch-status' || (scenario === 'restore-status' && patchNumber === 2)) return respond(res, 500, { error: 'failed' })
  if (scenario === 'missing-header') return respond(res, 200, { data: { ...settings } }, undefined, false)
  if (scenario === 'invalid-json') return respondRaw(res, 200, '{invalid')
  const value = (body as Record<string, unknown>)?.systemMetricsHourlyRetentionDays
  assert.equal(Object.keys(body as object).join(','), 'systemMetricsHourlyRetentionDays')
  settings.systemMetricsHourlyRetentionDays = value
  return respond(res, 200, { data: { ...settings } })
}

function environment(baseUrl: string): RealGoSettingsWriteSmokeEnvironment {
  return {
    [realGoSettingsWriteSmokeEnv.baseUrl]: baseUrl,
    [realGoSettingsWriteSmokeEnv.cookie]: cookie,
    [realGoSettingsWriteSmokeEnv.allow]: '1',
    [realGoSettingsWriteSmokeEnv.confirmation]: settingsWriteConfirmationForBaseUrl(baseUrl),
    NODE_ENV: 'development'
  }
}
function reset(next: Scenario): void { scenario = next; patchNumber = 0; records.length = 0; settings.systemMetricsHourlyRetentionDays = 20 }
function respond(res: ServerResponse, status: number, value: unknown, extra?: Record<string, string>, includeHeader = true): void { res.statusCode = status; if (includeHeader) res.setHeader('Cache-Control', 'no-store'); for (const [key, value2] of Object.entries(extra ?? {})) res.setHeader(key, value2); res.end(JSON.stringify(value)) }
function respondRaw(res: ServerResponse, status: number, value: string): void { res.statusCode = status; res.setHeader('Cache-Control', 'no-store'); res.end(value) }
function readJson(req: IncomingMessage): Promise<unknown> { return new Promise((resolve, reject) => { let text = ''; req.on('data', chunk => { text += chunk }); req.on('end', () => { try { resolve(JSON.parse(text)) } catch { reject(new Error('invalid body')) } }); req.on('error', reject) }) }
function assertNoLeak(value: string, baseUrl: string): void { assert.equal(value.includes(cookie), false); assert.equal(value.includes(baseUrl), false) }
function serverBaseUrl(server: Server): string { const address = server.address() as AddressInfo; return `http://127.0.0.1:${address.port}` }
function listen(server: Server): Promise<void> { return new Promise((resolve, reject) => { server.listen(0, '127.0.0.1', () => resolve()); server.once('error', reject) }) }
function close(server: Server): Promise<void> { return new Promise(resolve => server.close(() => resolve())) }

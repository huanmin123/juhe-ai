import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { ProxyTestStateUpdateInput } from '../../storage/proxy.repository.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-proxy-persistence-fencing-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'proxy-persistence-fencing-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, invalidationModule, dbServiceHandlers] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../shared/gateway-cache-invalidation.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

function readRow(proxyId: string): { updated_at: string; test_status: string; last_tested_at: string | null; last_test_message: string | null } {
  return databaseModule.getBusinessDatabase()
    .prepare('SELECT updated_at, test_status, last_tested_at, last_test_message FROM proxy_profiles WHERE id = ?')
    .get(proxyId) as unknown as { updated_at: string; test_status: string; last_tested_at: string | null; last_test_message: string | null }
}

function stateInput(
  configUpdatedAt: string,
  lastTestedAt: string,
  input: Omit<ProxyTestStateUpdateInput, 'expectedConfigUpdatedAt' | 'lastTestedAt'>
): ProxyTestStateUpdateInput {
  return {
    ...input,
    expectedConfigUpdatedAt: configUpdatedAt,
    lastTestedAt
  }
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.ok(startIndex >= 0 && endIndex > startIndex, `无法截取源码区段：${start}`)
  return source.slice(startIndex, endIndex)
}

try {
  const created = repositories.createProxy({
    name: '代理持久化 fencing 回归',
    type: 'http',
    host: '127.0.0.1',
    port: 18_080,
    enabled: true
  }, access)
  const initialConfig = repositories.getProxyTestConfig(created.id)
  assert.ok(initialConfig?.configUpdatedAt, '代理测试配置必须携带内部 config revision')
  assert.equal(Object.prototype.hasOwnProperty.call(repositories.findProxy(created.id), 'configUpdatedAt'), false, '代理摘要不得暴露内部 config revision')

  const beforeDiagnostic = readRow(created.id)
  let invalidationCount = 0
  const unregisterInvalidator = invalidationModule.registerGatewayRuntimeCacheInvalidator(() => {
    invalidationCount += 1
  })
  const diagnosticAt = '2026-07-25T00:00:01.000Z'
  const diagnosticResult = repositories.updateProxyTestState(created.id, stateInput(initialConfig.configUpdatedAt, diagnosticAt, {
    testStatus: 'passed',
    latencyMs: 15,
    lastTestMessage: '诊断成功'
  }))
  unregisterInvalidator()
  assert.equal(diagnosticResult?.testStatus, 'passed', '诊断状态应成功写入')
  const afterDiagnostic = readRow(created.id)
  assert.equal(afterDiagnostic.updated_at, beforeDiagnostic.updated_at, '诊断写不得改变代理配置 updated_at')
  assert.equal(invalidationCount, 0, '诊断写不得触发 repository shared gateway invalidation')

  const staleConfig = repositories.getProxyTestConfig(created.id)
  assert.ok(staleConfig?.configUpdatedAt, 'stale probe 必须先读取 config revision')
  repositories.updateProxy(created.id, { host: '127.0.0.2' })
  const editedConfig = repositories.getProxyTestConfig(created.id)
  assert.ok(editedConfig?.configUpdatedAt && editedConfig.configUpdatedAt !== staleConfig.configUpdatedAt, '配置编辑必须推进 config revision')
  const staleResult = repositories.updateProxyTestState(created.id, stateInput(staleConfig.configUpdatedAt, '2026-07-25T00:00:03.000Z', {
    testStatus: 'failed',
    latencyMs: 99,
    lastTestMessage: '旧配置迟到结果'
  }))
  assert.equal(staleResult, undefined, '配置编辑期间的旧探针结果必须被 CAS 拒绝')
  assert.equal(repositories.findProxy(created.id)?.testStatus, 'unknown', '配置编辑重置后的状态不得被旧探针覆盖')

  const currentConfig = repositories.getProxyTestConfig(created.id)
  assert.ok(currentConfig?.configUpdatedAt, 'watermark probe 必须读取当前 config revision')
  const newerAt = new Date(Date.now() + 2_000).toISOString()
  const olderAt = new Date(Date.now() + 1_000).toISOString()
  const newerResult = repositories.updateProxyTestState(created.id, stateInput(currentConfig.configUpdatedAt, newerAt, {
    testStatus: 'passed',
    latencyMs: 20,
    lastTestMessage: '较晚开始的结果'
  }))
  assert.equal(newerResult?.testStatus, 'passed', '较晚开始的结果应先写入')
  const olderResult = repositories.updateProxyTestState(created.id, stateInput(currentConfig.configUpdatedAt, olderAt, {
    testStatus: 'failed',
    latencyMs: 120,
    lastTestMessage: '较早开始的迟到结果'
  }))
  assert.equal(olderResult, undefined, '较早 started/testedAt 的迟到结果必须被 watermark 拒绝')
  const afterWatermark = readRow(created.id)
  assert.equal(afterWatermark.test_status, 'passed', '迟到结果不得覆盖较新探针状态')
  assert.equal(afterWatermark.last_test_message, '较晚开始的结果', '迟到结果不得覆盖较新探针消息')

  const syntheticFutureRevision = '2099-01-01T00:00:00.000Z'
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE proxy_profiles SET updated_at = ? WHERE id = ?')
    .run(syntheticFutureRevision, created.id)
  const firstRevisionUpdate = repositories.updateProxy(created.id, { description: '同毫秒 revision 1' })
  const revisionOne = repositories.getProxyTestConfig(created.id)?.configUpdatedAt
  const secondRevisionUpdate = repositories.updateProxy(created.id, { description: '同毫秒 revision 2' })
  const revisionTwo = repositories.getProxyTestConfig(created.id)?.configUpdatedAt
  assert.ok(firstRevisionUpdate && secondRevisionUpdate && revisionOne && revisionTwo, '配置 revision 回归需要两次真实写入')
  assert.ok(Date.parse(revisionOne) > Date.parse(syntheticFutureRevision), '配置写必须在时钟落后时仍推进 revision')
  assert.ok(Date.parse(revisionTwo) > Date.parse(revisionOne), '连续配置写必须严格单调推进 revision')

  const handlerConfig = repositories.getProxyTestConfig(created.id)
  assert.ok(handlerConfig?.configUpdatedAt, 'DB service 状态写必须携带 config revision')
  invalidationCount = 0
  const unregisterHandlerInvalidator = invalidationModule.registerGatewayRuntimeCacheInvalidator(() => {
    invalidationCount += 1
  })
  const handlerResult = await dbServiceHandlers.handleDbServiceOperation({
    type: 'update_proxy_test_state',
    proxyId: created.id,
    input: stateInput(handlerConfig.configUpdatedAt, new Date(Date.now() + 3_000).toISOString(), {
      testStatus: 'warning',
      latencyMs: 22,
      lastTestMessage: 'DB service fencing 回归'
    })
  })
  unregisterHandlerInvalidator()
  assert.equal(handlerResult.updated, true, 'DB service 真正写入应返回 updated=true')
  assert.equal(handlerResult.proxyStatus, 'warning', 'DB service 真正写入必须核验状态')
  assert.equal(invalidationCount, 0, 'DB service 诊断状态写不得触发 shared invalidation')

  const proxyRepositorySource = readFileSync(new URL('../../storage/proxy.repository.ts', import.meta.url), 'utf8')
  const dbServiceHandlersSource = readFileSync(new URL('../../modules/db-service/db-service-handlers.ts', import.meta.url), 'utf8')
  const proxyTestSource = readFileSync(new URL('../../modules/proxies/proxy-test.service.ts', import.meta.url), 'utf8')
  const proxyRoutesSource = readFileSync(new URL('../../modules/proxies/proxies.routes.ts', import.meta.url), 'utf8')
  const syncStateBody = sourceBetween(proxyRepositorySource, 'export function updateProxyTestState(', 'export async function updateProxyTestStateAsync(')
  const asyncStateBody = sourceBetween(proxyRepositorySource, 'export async function updateProxyTestStateAsync(', 'export function deleteProxy(')
  assert.doesNotMatch(syncStateBody, /notifyGatewayRuntimeCacheInvalidation/, 'SQLite 诊断写不得通知 shared invalidation')
  assert.doesNotMatch(asyncStateBody, /notifyGatewayRuntimeCacheInvalidation/, 'Postgres 诊断写不得通知 shared invalidation')
  assert.doesNotMatch(syncStateBody, /SET[^`]*updated_at\s*=/, 'SQLite 诊断写 SET 不得包含 updated_at')
  assert.doesNotMatch(asyncStateBody, /SET[^`]*updated_at\s*=/, 'Postgres 诊断写 SET 不得包含 updated_at')
  assert.match(syncStateBody, /updated_at\s*=\s*\?[^\n]*\n\s*AND \(last_tested_at IS NULL OR last_tested_at <= \?\)/, 'SQLite 必须对 config revision 与 observation watermark 做对称 CAS')
  assert.match(asyncStateBody, /updated_at\s*=\s*\?[^\n]*\n\s*AND \(last_tested_at IS NULL OR last_tested_at <= \?\)/, 'Postgres 必须对 config revision 与 observation watermark 做对称 CAS')
  assert.match(proxyRepositorySource, /updated_at = CASE[\s\S]*updated_at >= \?[\s\S]*\+0\.001 seconds/, 'SQLite 配置 revision 必须单调递增')
  assert.match(proxyRepositorySource, /updated_at = GREATEST\(updated_at \+ INTERVAL '1 millisecond'/, 'Postgres 配置 revision 必须单调递增')

  const handlerCases = [...dbServiceHandlersSource.matchAll(/case 'update_proxy_test_state': \{([\s\S]*?)(?=\n    case ')/g)].map((match) => match[1])
  assert.equal(handlerCases.length, 2, 'DB service 必须同时维护 PG 与 SQLite 代理状态写分支')
  for (const handlerCase of handlerCases) {
    assert.doesNotMatch(handlerCase, /clearGatewayRuntimeCacheLocal/, '代理诊断状态写不得清理本地 gateway runtime cache')
  }
  assert.match(proxyTestSource, /expectedConfigUpdatedAt:\s*proxy\.configUpdatedAt/, '后台成功写回必须携带本次读取的 config revision')
  assert.match(proxyTestSource, /refreshFailureState\(message,\s*\{[\s\S]*expectedConfigUpdatedAt:\s*proxy\.configUpdatedAt/, '后台 refresh failure 写回必须携带本次读取的 config revision')
  assert.match(proxyRoutesSource, /expectedConfigUpdatedAt:\s*execution\.configUpdatedAt/, '手动 persist:false 写回必须携带本次实际测试 config revision')
  assert.match(proxyRoutesSource, /if \(after\.updated && after\.proxyStatus !== report\.status\)/, '手动写入成功时必须核验 DB service 返回状态')
  assert.match(proxyTestSource, /if \(!result\.updated\) \{[\s\S]*return false/, 'stale 写回必须作为正常 no-op，不得抛 DB service 未确认错误')

  console.log(JSON.stringify({
    message: '代理持久化 fencing 回归通过',
    staleConfigRejected: true,
    observationWatermarkRejected: true,
    diagnosticUpdatedAtStable: true,
    revisionMonotonic: true,
    handlerInvalidationCount: invalidationCount,
    postgresSqlStaticContract: true
  }))
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

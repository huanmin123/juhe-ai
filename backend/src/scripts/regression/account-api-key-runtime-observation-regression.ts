import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-runtime-observation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-runtime-observation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, rotation, runtimeStates] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js'),
  import('../../storage/account-api-key-runtime-state.repository.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({ name: 'Key 探测摘要回归分组', providerCode: 'gpt' }, access)
  const apiKeys = ['sk-runtime-observation-a', 'sk-runtime-observation-b']
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'Key 探测摘要回归账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    credentials: {
      api_key: apiKeys[0],
      api_keys: apiKeys,
      api_key_strategy: 'round_robin',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const gatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, { ignoreAvailability: true })
  assert(gatewayAccount, '应能读取测试网关账户')

  const entries = rotation.accountApiKeyEntries({ api_keys: apiKeys })
  assert.equal(entries.length, 2)
  const observedAt = '2026-07-20T00:30:00.000Z'
  const firstProbeAt = '2026-07-20T01:00:00.000Z'
  const secondProbeAt = '2026-07-20T01:10:00.000Z'
  const selected = entries.map((entry) => ({
    ...gatewayAccount,
    apiKey: entry.key,
    selectedApiKeyFingerprint: entry.fingerprint,
    selectedApiKeyIndex: entry.index
  }))

  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[1],
    status: 'rate_limited',
    statusCode: 429,
    errorCode: 'later_key_failure',
    errorMessage: '第二个 Key 失败',
    cooldownUntil: secondProbeAt,
    observedAt,
    traceId: 'trace-key-1'
  }).changed, true)
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'rate_limited',
    statusCode: 503,
    errorCode: 'stable_tie_winner',
    errorMessage: '第一个 Key 失败',
    cooldownUntil: firstProbeAt,
    observedAt,
    traceId: 'trace-key-0'
  }).changed, true)

  const summary = runtimeStates.loadAccountApiKeyRuntimeSummariesByAccountIds([account.id]).get(account.id)
  assert(summary, '应生成 Key 池运行态摘要')
  assert.equal(summary.lastFailureAt, observedAt, '摘要应选择最近的非空失败时间')
  assert.equal(summary.lastErrorCode, 'stable_tie_winner', '同一失败时间应按 key_index 稳定选择')
  assert.equal(summary.lastTraceId, 'trace-key-0', 'traceId 必须与选中的最近失败属于同一 observation')
  assert.equal(summary.nextProbeAt, firstProbeAt, '下次检查应选择不可用 Key 中最早的非空计划')

  const details = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)
  assert.equal(details?.[0]?.lastTraceId, 'trace-key-0', 'Key 明细应返回最近失败 traceId')

  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'temporary_unavailable',
    errorCode: 'stale_failure',
    errorMessage: '过期失败不得覆盖',
    observedAt: '2026-07-19T23:30:00.000Z',
    traceId: 'trace-stale'
  }).changed, false, '更早的失败 observation 不得覆盖新状态')
  const afterStaleFailure = runtimeStates.loadAccountApiKeyRuntimeSummariesByAccountIds([account.id]).get(account.id)
  assert.equal(afterStaleFailure?.lastTraceId, 'trace-key-0', '过期失败不得覆盖最近 traceId')
  assert.equal(afterStaleFailure?.lastErrorCode, 'stable_tie_winner', '过期失败不得覆盖最近错误')

  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(selected[0], {
    observedAt: '2026-07-20T00:40:00.000Z'
  }).changed, true)
  const restored = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]
  assert.equal(restored?.lastTraceId, undefined, '成功后必须清空旧失败 traceId')
  assert.equal(restored?.lastErrorCode, undefined, '成功后必须清空旧错误')

  const schemaColumns = databaseModule.getBusinessDatabase()
    .prepare("PRAGMA table_info('account_api_key_runtime_states')")
    .all() as Array<{ name: string }>
  assert(schemaColumns.some((column) => column.name === 'last_trace_id'), '当前 SQLite schema 必须包含 last_trace_id')

  console.log('账户内 API Key 探测摘要回归通过')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

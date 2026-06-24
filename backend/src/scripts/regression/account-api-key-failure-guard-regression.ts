import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-failure-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-failure-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  apiKeyRotation,
  apiKeyEffects,
  apiKeyFailureGuard
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js'),
  import('../../modules/gateway/runtime/account-api-key-effects.service.js'),
  import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({
    name: 'Key 失败保护回归分组',
    providerCode: 'gpt'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    name: 'Key 失败保护多 Key 账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    credentials: {
      api_key: 'sk-failure-guard-a',
      api_keys: ['sk-failure-guard-a', 'sk-failure-guard-b'],
      api_key_strategy: 'round_robin',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const gatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, { ignoreAvailability: true })
  assert(gatewayAccount, '应能读取测试网关账户')

  const selectedA = {
    ...gatewayAccount,
    apiKey: 'sk-failure-guard-a',
    selectedApiKeyFingerprint: apiKeyRotation.fingerprintAccountApiKey('sk-failure-guard-a'),
    selectedApiKeyIndex: 0
  }
  const selectedB = {
    ...gatewayAccount,
    apiKey: 'sk-failure-guard-b',
    selectedApiKeyFingerprint: apiKeyRotation.fingerprintAccountApiKey('sk-failure-guard-b'),
    selectedApiKeyIndex: 1
  }

  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  for (let index = 0; index < 8; index += 1) {
    apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
      status: 'temporary_unavailable',
      statusCode: 503,
      errorMessage: '同一来源高并发波动',
      trafficSource: 'gateway',
      clientIp: '198.51.100.10',
      apiKeyId: 'gateway-key-a',
      source: 'same_ip_regression'
    })
  }
  await delay(50)
  assert.equal(runtimeRows(account.id).length, 0, '同一 IP 连续失败不应写入全局 Key 运行态')
  assert.equal(
    apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(account.id).length,
    1,
    '同一 IP 失败仍应产生进程内短避让，避免当前进程持续打同一个 Key'
  )

  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, 'same_ip_recovered')
  assert.equal(
    apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(account.id).length,
    0,
    '真实成功应清理 Key 失败保护的本地短避让'
  )

  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  for (let index = 0; index < 4; index += 1) {
    apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
      status: 'temporary_unavailable',
      statusCode: 503,
      errorMessage: '第一来源失败',
      trafficSource: 'gateway',
      clientIp: '198.51.100.20',
      apiKeyId: 'gateway-key-a',
      source: 'storm_pending_regression'
    })
  }
  await delay(50)
  assert.equal(runtimeRows(account.id).length, 0, '未达到跨 IP 风暴阈值前不应写入全局 Key 运行态')

  apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'temporary_unavailable',
    statusCode: 503,
    errorMessage: '第二来源确认失败',
    trafficSource: 'gateway',
    clientIp: '198.51.100.21',
    apiKeyId: 'gateway-key-b',
    source: 'storm_confirmed_regression'
  })
  await delay(50)
  assert.equal(runtimeRows(account.id).length, 0, '达到跨 IP 风暴数量但观察时间不足时不应写入全局 Key 运行态')

  await delay(2100)
  apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'temporary_unavailable',
    statusCode: 503,
    errorMessage: '第二来源持续确认失败',
    trafficSource: 'gateway',
    clientIp: '198.51.100.21',
    apiKeyId: 'gateway-key-b',
    source: 'storm_confirmed_regression'
  })
  await waitFor(() => runtimeStatus(account.id, selectedA.selectedApiKeyFingerprint) === 'temporary_unavailable', 5000)

  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, 'storm_recovered')
  await waitFor(() => runtimeStatus(account.id, selectedA.selectedApiKeyFingerprint) === 'active', 5000)
  for (let index = 0; index < 4; index += 1) {
    apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
      status: 'temporary_unavailable',
      statusCode: 503,
      errorMessage: '成功后的第一来源失败',
      trafficSource: 'gateway',
      clientIp: '198.51.100.40',
      apiKeyId: 'gateway-key-a',
      source: 'recent_success_regression'
    })
  }
  await delay(2100)
  apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'temporary_unavailable',
    statusCode: 503,
    errorMessage: '成功后的第二来源失败',
    trafficSource: 'gateway',
    clientIp: '198.51.100.41',
    apiKeyId: 'gateway-key-b',
    source: 'recent_success_regression'
  })
  await delay(50)
  assert.equal(runtimeStatus(account.id, selectedA.selectedApiKeyFingerprint), 'active', '近期真实成功后不应因为短窗口失败再次写成全局不可用')

  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedB, {
    status: 'error',
    statusCode: 401,
    errorMessage: '错误策略确认 Key 失效',
    trafficSource: 'gateway',
    clientIp: '198.51.100.30',
    apiKeyId: 'gateway-key-a',
    source: 'policy_error_regression'
  })
  await waitFor(() => runtimeStatus(account.id, selectedB.selectedApiKeyFingerprint) === 'error', 5000)

  console.log('账户内 API Key 失败保护回归通过：同源高并发失败只本地短避让，跨 IP 持续失败或错误策略确认后才写全局 Key 状态')
} finally {
  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function runtimeRows(accountId: string): Array<{ key_fingerprint: string; status: string }> {
  return databaseModule.getBusinessDatabase()
    .prepare('SELECT key_fingerprint, status FROM account_api_key_runtime_states WHERE account_id = ? ORDER BY key_index ASC')
    .all(accountId) as Array<{ key_fingerprint: string; status: string }>
}

function runtimeStatus(accountId: string, keyFingerprint: string): string | undefined {
  return runtimeRows(accountId).find((row) => row.key_fingerprint === keyFingerprint)?.status
}

async function waitFor(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt <= timeoutMs) {
    if (predicate()) {
      return
    }
    await delay(20)
  }
  assert.fail(`等待条件超时 ${timeoutMs}ms`)
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-rotation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-rotation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, rotation] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({
    name: '账户内 Key 轮询回归分组',
    providerCode: 'gpt'
  }, access)

  const roundRobinAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户内多 Key 轮询',
    type: 'api_key',
    status: 'active',
    credentials: {
      api_keys: ['sk-rotation-a', 'sk-rotation-b', 'sk-rotation-c'],
      api_key_strategy: 'round_robin',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5']
  }, access)

  assert.equal(roundRobinAccount.credentials.api_key, 'sk-rotation-a', '多 Key 账户应保留首个 Key 作为主凭据')
  assert.deepEqual(roundRobinAccount.credentials.api_keys, ['sk-rotation-a', 'sk-rotation-b', 'sk-rotation-c'], '多个 Key 应保存在同一个账户凭据内')
  assert.equal(repositories.listAccounts(access, { page: 1, pageSize: 20 }).filter((account) => account.name === '账户内多 Key 轮询').length, 1, '多个上游 Key 不应展开成多个 AI 账户')
  assert.deepEqual(
    Array.from({ length: 5 }, () => rotation.selectAccountRuntimeApiKey({ accountId: roundRobinAccount.id, credentials: roundRobinAccount.credentials })),
    ['sk-rotation-a', 'sk-rotation-b', 'sk-rotation-c', 'sk-rotation-a', 'sk-rotation-b'],
    '轮询策略应在账户内多个 API Key 之间轮转'
  )

  const requestLocalCredentials = {
    api_keys: ['sk-local-a', 'sk-local-b', 'sk-local-c', 'sk-local-d', 'sk-local-e'],
    api_key_strategy: 'round_robin'
  }
  const requestLocalAccountId = `${roundRobinAccount.id}:request-local`
  const requestOneFirst = rotation.selectAccountRuntimeApiKeyEntry({
    accountId: requestLocalAccountId,
    credentials: requestLocalCredentials
  })
  const requestTwoFirst = rotation.selectAccountRuntimeApiKeyEntry({
    accountId: requestLocalAccountId,
    credentials: requestLocalCredentials
  })
  assert.equal(requestOneFirst?.key, 'sk-local-a')
  assert.equal(requestTwoFirst?.key, 'sk-local-b', '并发独立请求只应各推进一次全局轮换游标')

  const requestOneAttempted = new Set([requestOneFirst!.fingerprint])
  const requestOneSecond = rotation.selectAccountRuntimeApiKeyEntry({
    accountId: requestLocalAccountId,
    credentials: requestLocalCredentials,
    excludeFingerprints: requestOneAttempted,
    continueAfterFingerprint: requestOneFirst!.fingerprint
  })
  assert.equal(requestOneSecond?.key, 'sk-local-b', '请求内续选应从刚失败 Key 的池内后继开始')
  requestOneAttempted.add(requestOneSecond!.fingerprint)
  const requestOneThird = rotation.selectAccountRuntimeApiKeyEntry({
    accountId: requestLocalAccountId,
    credentials: requestLocalCredentials,
    excludeFingerprints: requestOneAttempted,
    continueAfterFingerprint: requestOneSecond!.fingerprint,
    runtimeStates: [
      {
        keyFingerprint: rotation.fingerprintAccountApiKey('sk-local-b'),
        status: 'temporary_unavailable'
      },
      {
        keyFingerprint: rotation.fingerprintAccountApiKey('sk-local-c'),
        status: 'temporary_unavailable'
      }
    ]
  })
  assert.equal(requestOneThird?.key, 'sk-local-d', '请求中途刚失败 Key 和后继 Key 运行态变化后仍应从原池位置继续，且不得重试已命中 Key')
  const requestThreeFirst = rotation.selectAccountRuntimeApiKeyEntry({
    accountId: requestLocalAccountId,
    credentials: requestLocalCredentials
  })
  assert.equal(requestThreeFirst?.key, 'sk-local-c', '请求内续选不得额外推进后续独立请求的全局轮换游标')

  const weightedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户内多 Key 权重',
    type: 'api_key',
    status: 'active',
    credentials: {
      api_keys: ['sk-weight-a', 'sk-weight-b'],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [3, 1],
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5']
  }, access)

  const weightedSequence = Array.from({ length: 8 }, () => rotation.selectAccountRuntimeApiKey({ accountId: weightedAccount.id, credentials: weightedAccount.credentials }))
  assert.equal(weightedSequence.filter((key) => key === 'sk-weight-a').length, 6, '权重 3 的 Key 在 8 次选择中应命中 6 次')
  assert.equal(weightedSequence.filter((key) => key === 'sk-weight-b').length, 2, '权重 1 的 Key 在 8 次选择中应命中 2 次')

  const weightedAvailableAfterIsolation = Array.from({ length: 3 }, () => rotation.selectAccountRuntimeApiKey({
    accountId: weightedAccount.id,
    credentials: weightedAccount.credentials,
    runtimeStates: [{
      keyFingerprint: rotation.fingerprintAccountApiKey('sk-weight-a'),
      status: 'temporary_unavailable',
      keyIndex: 0
    }]
  }))
  assert.deepEqual(
    weightedAvailableAfterIsolation,
    ['sk-weight-b', 'sk-weight-b', 'sk-weight-b'],
    '权重模式下不可用 Key 应从候选集中剔除，不能因为权重大继续被调度'
  )

  console.log('账户内 API Key 轮询回归通过：多个上游 Key 保存为单个 AI 账户，并按轮询或权重在账户内部选择')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

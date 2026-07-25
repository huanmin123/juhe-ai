import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import {
  authorizeAccountApiKeyPersistentMutation,
  authorizeAccountApiKeyPersistentMutationForTrafficSource,
  type AccountApiKeyPersistentMutationContext,
  type AccountApiKeyPersistentMutationKind
} from '../../modules/gateway/runtime/account-api-key-mutation-authority.js'

const mutations: AccountApiKeyPersistentMutationKind[] = ['failure', 'success', 'defer']
for (const mutation of mutations) {
  assert.equal(
    authorizeAccountApiKeyPersistentMutation(mutation, undefined).allowed,
    false,
    `缺少显式 authority 时不得执行 ${mutation} 持久状态写入`
  )
}

const userPolicyContext: AccountApiKeyPersistentMutationContext = {
  authority: 'explicit_user_policy',
  trafficSource: 'gateway'
}
assert.equal(authorizeAccountApiKeyPersistentMutation('failure', userPolicyContext).allowed, true)
assert.equal(authorizeAccountApiKeyPersistentMutation('success', userPolicyContext).allowed, false)
assert.equal(authorizeAccountApiKeyPersistentMutation('defer', userPolicyContext).allowed, false)
assert.deepEqual(
  authorizeAccountApiKeyPersistentMutationForTrafficSource('failure', 'manual_account_test', userPolicyContext),
  { allowed: false, reason: 'unauthorized_traffic_source' },
  '实际 trafficSource 必须与 mutation context 一致'
)

for (const trafficSource of ['manual_account_test', 'hybrid_scoring', 'hybrid_quality_scoring'] as const) {
  const forgedContexts = [
    { authority: 'explicit_user_policy', trafficSource },
    { authority: 'automatic_probe', trafficSource, probeOutcome: 'complete_success' }
  ] as unknown as AccountApiKeyPersistentMutationContext[]
  for (const context of forgedContexts) {
    for (const mutation of mutations) {
      assert.equal(
        authorizeAccountApiKeyPersistentMutation(mutation, context).allowed,
        false,
        `${trafficSource} 即使伪造 authority 也必须保持零状态副作用`
      )
    }
  }
}

const forgedGatewayProbeContext = {
  authority: 'automatic_probe',
  trafficSource: 'gateway',
  probeOutcome: 'complete_success'
} as unknown as AccountApiKeyPersistentMutationContext
for (const mutation of mutations) {
  assert.equal(
    authorizeAccountApiKeyPersistentMutation(mutation, forgedGatewayProbeContext).allowed,
    false,
    `gateway 不得伪造 automatic_probe 执行 ${mutation}`
  )
}

assert.deepEqual(
  authorizeAccountApiKeyPersistentMutation('success', {
    authority: 'forged_probe',
    trafficSource: 'cooldown_retest',
    probeOutcome: 'complete_success'
  } as unknown as AccountApiKeyPersistentMutationContext),
  { allowed: false, reason: 'invalid_authority' },
  '未知 authority 不得借合法 probe 来源与 outcome 绕过授权'
)

for (const trafficSource of ['account_health_check', 'runtime_recovery_probe', 'cooldown_retest'] as const) {
  const forgedPolicyContext = {
    authority: 'explicit_user_policy',
    trafficSource
  } as unknown as AccountApiKeyPersistentMutationContext
  for (const mutation of mutations) {
    assert.equal(
      authorizeAccountApiKeyPersistentMutation(mutation, forgedPolicyContext).allowed,
      false,
      `${trafficSource} 不得伪造 explicit_user_policy 执行 ${mutation}`
    )
  }
  const expectedByOutcome = {
    complete_success: 'success',
    upstream_failure: 'failure',
    framing_complete_neutral: 'defer',
    probe_task_failure: 'defer'
  } as const
  for (const [probeOutcome, expectedMutation] of Object.entries(expectedByOutcome)) {
    const context = {
      authority: 'automatic_probe',
      trafficSource,
      probeOutcome
    } as AccountApiKeyPersistentMutationContext
    for (const mutation of mutations) {
      assert.equal(
        authorizeAccountApiKeyPersistentMutation(mutation, context).allowed,
        mutation === expectedMutation,
        `${trafficSource}/${probeOutcome} 只能授权 ${expectedMutation}，不得执行 ${mutation}`
      )
    }
  }
}

console.log('账户内 API Key 持久状态授权矩阵回归通过')

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-mutation-authority-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-mutation-authority-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, apiKeyRotation, { handleDbServiceOperation }] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js'),
  import('../../modules/db-service/db-service-handlers.js')
])
const handleUncheckedDbServiceOperation = handleDbServiceOperation as unknown as (
  operation: unknown
) => Promise<{ changed: boolean; skippedReason?: string }>

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: 'Key 状态授权矩阵分组',
    providerCode: 'gpt'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'Key 状态授权矩阵账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    credentials: {
      api_key: 'sk-authority-a',
      api_keys: ['sk-authority-a', 'sk-authority-b'],
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const gatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, { ignoreAvailability: true })
  assert(gatewayAccount)
  const selected = {
    ...gatewayAccount,
    apiKey: 'sk-authority-a',
    selectedApiKeyFingerprint: apiKeyRotation.fingerprintAccountApiKey('sk-authority-a'),
    selectedApiKeyIndex: 0
  }

  for (const trafficSource of [undefined, 'manual_account_test', 'hybrid_scoring', 'hybrid_quality_scoring'] as const) {
    const result = await handleUncheckedDbServiceOperation({
      type: 'record_account_api_key_failure',
      account: selected,
      trafficSource,
      mutationContext: trafficSource === undefined
        ? undefined
        : {
            authority: 'explicit_user_policy',
            trafficSource
          },
      input: {
        status: 'error',
        observedAt: new Date().toISOString()
      }
    })
    assert.equal(result.changed, false, `${String(trafficSource)} 不得写 Key 失败状态`)
    assert.match(result.skippedReason ?? '', /^unauthorized_account_api_key_mutation:/)
    assert.equal(runtimeStatus(account.id, selected.selectedApiKeyFingerprint), undefined)
  }
  const forgedGatewayContext = await handleUncheckedDbServiceOperation({
    type: 'record_account_api_key_failure',
    account: selected,
    trafficSource: 'manual_account_test',
    mutationContext: userPolicyContext,
    input: {
      status: 'error',
      observedAt: new Date().toISOString()
    }
  })
  assert.equal(forgedGatewayContext.changed, false, '实际 manual 来源不得用伪造 gateway context 写 Key 状态')
  assert.match(forgedGatewayContext.skippedReason ?? '', /^unauthorized_account_api_key_mutation:/)
  assert.equal(runtimeStatus(account.id, selected.selectedApiKeyFingerprint), undefined)

  const policyFailure = await handleDbServiceOperation({
    type: 'record_account_api_key_failure',
    account: selected,
    trafficSource: 'gateway',
    mutationContext: userPolicyContext,
    input: {
      status: 'temporary_unavailable',
      observedAt: new Date().toISOString()
    }
  })
  assert.equal(policyFailure.changed, true, '用户显式规则必须能够写入配置指定的 Key 业务状态')
  assert.equal(runtimeStatus(account.id, selected.selectedApiKeyFingerprint), 'temporary_unavailable')

  for (const trafficSource of ['manual_account_test', 'hybrid_scoring', 'hybrid_quality_scoring'] as const) {
    const result = await handleUncheckedDbServiceOperation({
      type: 'record_account_api_key_success',
      account: selected,
      trafficSource,
      mutationContext: {
        authority: 'automatic_probe',
        trafficSource,
        probeOutcome: 'complete_success'
      },
      observedAt: new Date().toISOString()
    })
    assert.equal(result.changed, false, `${trafficSource} 不得把持久故障 Key 恢复为 active`)
    assert.match(result.skippedReason ?? '', /^unauthorized_account_api_key_mutation:/)
    assert.equal(runtimeStatus(account.id, selected.selectedApiKeyFingerprint), 'temporary_unavailable')
  }

  const probeSuccess = await handleDbServiceOperation({
    type: 'record_account_api_key_success',
    account: selected,
    trafficSource: 'cooldown_retest',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'complete_success'
    },
    observedAt: new Date(Date.now() + 1).toISOString()
  })
  assert.equal(probeSuccess.changed, true, 'Key 冷却复测的协议成功必须能够恢复对应 Key')
  assert.equal(runtimeStatus(account.id, selected.selectedApiKeyFingerprint), 'active')

  console.log('账户内 API Key DB 写入授权边界回归通过')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function runtimeStatus(accountId: string, keyFingerprint: string): string | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT status FROM account_api_key_runtime_states WHERE account_id = ? AND key_fingerprint = ? LIMIT 1')
    .get(accountId, keyFingerprint) as { status: string } | undefined
  return row?.status
}

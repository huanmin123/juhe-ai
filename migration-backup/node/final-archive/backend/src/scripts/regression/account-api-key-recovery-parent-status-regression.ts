import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { isAccountStatusEligibleForRecoveryProbe } from '../../storage/account-status.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-recovery-parent-status-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-recovery-parent-status-secret'
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
const dueAt = new Date(Date.now() - 1_000).toISOString()

try {
  const database = databaseModule.getBusinessDatabase()
  const group = repositories.createGroup({ name: '恢复父账户状态回归分组', providerCode: 'gpt' }, access)
  const accounts = new Map<string, string>()
  for (const status of ['temporary_unavailable', 'rate_limited', 'disabled', 'error'] as const) {
    const key = `sk-recovery-parent-${status}`
    const secondaryKey = `${key}-secondary`
    const account = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `恢复父账户状态 ${status}`,
      type: 'api_key',
      status: 'active',
      schedulable: true,
      supportedModels: ['gpt-5.5'],
      credentials: {
        api_key: key,
        api_keys: [key, secondaryKey],
        api_key_strategy: 'round_robin',
        base_url: 'https://api.openai.com/v1'
      },
      groupId: group.id
    }, access)
    database.prepare('UPDATE accounts SET status = ?, schedulable = 1, account_expires_at = NULL WHERE id = ?').run(status, account.id)
    const entry = rotation.accountApiKeyEntries({ api_keys: [key, secondaryKey] })[0]
    assert(entry)
    database.prepare(`
      INSERT INTO account_api_key_runtime_states (
        id, system_account_id, account_id, key_fingerprint, key_index,
        status, failure_count, consecutive_failures, success_count,
        cooldown_until, next_probe_at, probe_backoff_seconds,
        created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'temporary_unavailable', 1, 1, 0, ?, ?, 3, ?, ?)
    `).run(
      `recovery-parent-status-${status}`,
      access.systemAccountId,
      account.id,
      entry.fingerprint,
      entry.index,
      dueAt,
      dueAt,
      dueAt,
      dueAt
    )
    accounts.set(status, account.id)
  }

  const candidates = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(20)
  assert.equal(isAccountStatusEligibleForRecoveryProbe('active'), true)
  assert.equal(isAccountStatusEligibleForRecoveryProbe('temporary_unavailable'), true)
  assert.equal(isAccountStatusEligibleForRecoveryProbe('rate_limited'), true)
  assert.equal(isAccountStatusEligibleForRecoveryProbe('disabled'), false)
  assert.equal(isAccountStatusEligibleForRecoveryProbe('error'), false)
  assert.equal(isAccountStatusEligibleForRecoveryProbe('pending_test'), false)
  assert.equal(candidates.some((item) => item.accountId === accounts.get('temporary_unavailable')), true, 'temporary_unavailable 父账户的到期 Key 必须进入恢复探针候选')
  assert.equal(candidates.some((item) => item.accountId === accounts.get('rate_limited')), true, 'rate_limited 父账户的到期 Key 必须进入恢复探针候选')
  assert.equal(candidates.some((item) => item.accountId === accounts.get('disabled')), false, 'disabled 父账户不得进入恢复探针候选')
  assert.equal(candidates.some((item) => item.accountId === accounts.get('error')), false, 'error 父账户不得进入恢复探针候选')

  console.log('账户 API Key 恢复探针父账户状态回归通过')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-revalidate-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-revalidate-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [database, repositories, runtimeStates] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-runtime-state.repository.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: 'Key 池重新验证回归分组', providerCode: 'openai' }, access)
  const account = repositories.createAccount({
    providerCode: 'openai', providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: 'Key 池重新验证回归账户', type: 'api_key', status: 'active', schedulable: true,
    supportedModels: ['gpt-5.5'], groupId: group.id,
    credentials: { api_key: 'sk-a', api_keys: ['sk-a', 'sk-b', 'sk-c', 'sk-d', 'sk-e', 'sk-f'], base_url: 'https://api.openai.com/v1' }
  }, access)
  const db = database.getBusinessDatabase()
  db.prepare(`UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`).run(account.id)
  const keyFingerprints = accountApiKeyEntries({ api_keys: ['sk-a', 'sk-b', 'sk-c', 'sk-d', 'sk-e', 'sk-f'] }).map((entry) => entry.fingerprint)
  const now = new Date().toISOString()
  const future = new Date(Date.now() + 60_000).toISOString()
  const insert = db.prepare(`
    INSERT INTO account_api_key_runtime_states
      (id, system_account_id, account_id, key_fingerprint, key_index, status, failure_count, consecutive_failures,
       success_count, next_probe_at, probe_claimed_until, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?)
  `)
  insert.run('state-unverified', access.systemAccountId, account.id, keyFingerprints[0], 0, 'unverified', future, null, now, now)
  insert.run('state-temp', access.systemAccountId, account.id, keyFingerprints[1], 1, 'temporary_unavailable', future, null, now, now)
  insert.run('state-error', access.systemAccountId, account.id, keyFingerprints[2], 2, 'error', future, null, now, now)
  insert.run('state-active', access.systemAccountId, account.id, keyFingerprints[3], 3, 'active', future, null, now, now)
  insert.run('state-disabled', access.systemAccountId, account.id, keyFingerprints[4], 4, 'disabled', future, null, now, now)
  insert.run('state-leased', access.systemAccountId, account.id, keyFingerprints[5], 5, 'error', future, future, now, now)

  const revision = account.configRevision ?? 1
  const result = await runtimeStates.revalidateAccountApiKeyRuntimePoolAsync({ accountId: account.id, expectedConfigRevision: revision })
  assert.equal(result.changed, 3, '仅未 active/disabled 且无有效 lease 的三条 Key 应被标记到期')
  const rows = db.prepare(`SELECT key_fingerprint, status, next_probe_at, probe_claimed_until FROM account_api_key_runtime_states WHERE account_id = ? ORDER BY key_index`).all(account.id) as Array<{ key_fingerprint: string; status: string; next_probe_at: string | null; probe_claimed_until: string | null }>
  const byFingerprint = new Map(rows.map((row) => [row.key_fingerprint, row]))
  for (const key of keyFingerprints.slice(0, 3)) assert(Date.parse(byFingerprint.get(key)!.next_probe_at!) <= Date.now(), `${key} 应立即到期`)
  assert.equal(byFingerprint.get(keyFingerprints[3])!.next_probe_at, future, 'active Key 不得改变')
  assert.equal(byFingerprint.get(keyFingerprints[4])!.next_probe_at, future, 'disabled Key 必须保留')
  assert.equal(byFingerprint.get(keyFingerprints[5])!.next_probe_at, future, '有效 probe lease 不得抢占')

  const before = rows.map((row) => row.next_probe_at)
  const stale = await runtimeStates.revalidateAccountApiKeyRuntimePoolAsync({ accountId: account.id, expectedConfigRevision: revision + 1 })
  assert.equal(stale.changed, 0, '旧 revision 不得写入任何状态')
  const after = db.prepare(`SELECT next_probe_at FROM account_api_key_runtime_states WHERE account_id = ? ORDER BY key_index`).all(account.id) as Array<{ next_probe_at: string | null }>
  assert.deepEqual(after.map((row) => row.next_probe_at), rows.map((row) => row.next_probe_at), '旧 revision 不得改变已写入状态')

  db.prepare(`UPDATE accounts SET status = 'disabled' WHERE id = ?`).run(account.id)
  const disabledBefore = db.prepare(`SELECT next_probe_at FROM account_api_key_runtime_states WHERE account_id = ? ORDER BY key_index`).all(account.id) as Array<{ next_probe_at: string | null }>
  const disabled = await runtimeStates.revalidateAccountApiKeyRuntimePoolAsync({ accountId: account.id, expectedConfigRevision: revision })
  assert.equal(disabled.eligible, false, '停用账户不得重新验证 Key 池')
  assert.equal(disabled.reason, 'account_not_active')
  const disabledAfter = db.prepare(`SELECT next_probe_at FROM account_api_key_runtime_states WHERE account_id = ? ORDER BY key_index`).all(account.id) as Array<{ next_probe_at: string | null }>
  assert.deepEqual(disabledAfter, disabledBefore, '停用账户不得写入运行态')

  db.prepare(`UPDATE accounts SET status = 'active', schedulable = 0 WHERE id = ?`).run(account.id)
  const unschedulable = await runtimeStates.revalidateAccountApiKeyRuntimePoolAsync({ accountId: account.id, expectedConfigRevision: revision })
  assert.equal(unschedulable.eligible, false, '不可调度账户不得重新验证 Key 池')
  assert.equal(unschedulable.reason, 'account_unschedulable')

  db.prepare(`UPDATE accounts SET schedulable = 1 WHERE id = ?`).run(account.id)
  db.prepare(`UPDATE accounts SET status = 'disabled' WHERE id = ?`).run(account.id)
  const race = await runtimeStates.revalidateAccountApiKeyRuntimePoolAsync({ accountId: account.id, expectedConfigRevision: revision })
  assert.equal(race.eligible, false, '请求期间账户状态变化后不得伪成功')
  assert.equal(race.changed, 0)

  db.prepare(`UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`).run(account.id)
  db.prepare(`UPDATE account_api_key_runtime_states SET status = 'disabled', probe_claimed_until = NULL WHERE account_id = ?`).run(account.id)
  const noCandidate = await runtimeStates.revalidateAccountApiKeyRuntimePoolAsync({ accountId: account.id, expectedConfigRevision: revision })
  assert.equal(noCandidate.eligible, false, '没有可重新验证 Key 时不得伪成功')
  assert.equal(noCandidate.reason, 'no_revalidatable_key')
  assert.equal(noCandidate.changed, 0)

  const routeSource = await import('node:fs/promises').then((fs) => fs.readFile(new URL('../../modules/accounts/account-detail.routes.ts', import.meta.url), 'utf8'))
  const repositorySource = await import('node:fs/promises').then((fs) => fs.readFile(new URL('../../storage/account-api-key-runtime-state.repository.ts', import.meta.url), 'utf8'))
  assert.match(routeSource, /accessType === 'authorized'[\s\S]*status\(403\)/, '授权实例必须显式拒绝')
  assert.match(routeSource, /configRevision !== parsed\.data\.expectedConfigRevision[\s\S]*status\(409\)/, '旧 revision 必须显式返回冲突')
  assert.match(repositorySource, /const retry = database\.prepare\([\s\S]*EXISTS \([\s\S]*accounts\.status = 'active'[\s\S]*accounts\.schedulable = 1[\s\S]*const retried = Number\(retry\.changes/, 'SQLite 二次 CAS 更新必须实际检查 affected rows')
  assert.match(repositorySource, /const retry = await client\.execute\([\s\S]*accounts\.status = 'active'[\s\S]*accounts\.schedulable = 1[\s\S]*const retried = Number\(retry\.changes/, 'Postgres 二次 CAS 更新必须实际检查 affected rows')
  console.log('account-api-key-runtime-revalidate 回归通过')
} finally {
  database.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

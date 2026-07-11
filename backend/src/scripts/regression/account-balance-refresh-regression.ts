import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-balance-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-balance-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, balanceRepository, balanceQueryService] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-balance.repository.js'),
  import('../../modules/accounts/account-balance-query.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({ name: '余额回归分组', providerCode: 'gpt', enabled: true }, access)
  const create = (name: string, type = 'api_key', credentials: Record<string, unknown> = { api_key: `sk-${name}`, base_url: 'https://relay.example/v1' }) =>
    repositories.createAccount({
      providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name, type, credentials, groupId: group.id
    }, access)
  const dueA = create('due-a')
  const dueB = create('due-b')
  const disabled = create('disabled')
  const oauth = create('oauth', 'oauth', { access_token: 'oauth-token', refresh_token: 'refresh-token', base_url: 'https://relay.example/v1' })
  const multi = create('multi', 'api_key', { api_keys: ['sk-a', 'sk-b'], api_key: 'sk-a', base_url: 'https://relay.example/v1' })
  const future = create('future')
  const database = databaseModule.getBusinessDatabase()
  const dueAt = '2026-07-11T00:00:00.000Z'
  const futureAt = '2026-07-12T00:00:00.000Z'
  const configure = database.prepare(`UPDATE accounts SET status = ?, schedulable = 1, balance_query_enabled = 1, balance_query_config_json = ?, balance_query_next_refresh_at = ? WHERE id = ?`)
  configure.run('active', JSON.stringify({ adapter: 'sub2api', intervalMinutes: 5 }), dueAt, dueA.id)
  configure.run('active', JSON.stringify({ adapter: 'sub2api', intervalMinutes: 5 }), dueAt, dueB.id)
  configure.run('disabled', JSON.stringify({ adapter: 'sub2api', intervalMinutes: 5 }), dueAt, disabled.id)
  configure.run('active', JSON.stringify({ adapter: 'sub2api', intervalMinutes: 5 }), dueAt, oauth.id)
  configure.run('active', JSON.stringify({ adapter: 'sub2api', intervalMinutes: 5 }), dueAt, multi.id)
  configure.run('active', JSON.stringify({ adapter: 'sub2api', intervalMinutes: 5 }), futureAt, future.id)

  const due = balanceRepository.listAccountsDueForBalanceRefresh({ now: '2026-07-11T12:00:00.000Z', limit: 100 })
  assert.deepEqual(due.map((item: { id: string }) => item.id), [dueA.id, dueB.id].sort(), '只应领取到期的 active 物理单 API Key 账户')

  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: dueA.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '7.310000', lastAttemptAt: dueAt, lastSuccessAt: dueAt }
  })
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: dueA.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'failed', errorMessage: '上游鉴权失败（HTTP 401）', lastAttemptAt: dueAt }
  })
  const snapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([dueA.id]).get(dueA.id)
  assert.equal(snapshot?.status, 'failed')
  assert.equal(snapshot?.remainingUsd, undefined, '失败快照必须清除旧金额')
  assert.equal(snapshot?.errorMessage, '上游鉴权失败（HTTP 401）')

  const candidate = balanceRepository.listAccountsDueForBalanceRefresh({ now: '2026-07-11T12:00:00.000Z', limit: 1 })[0]
  let queryCount = 0
  const query = async () => {
    queryCount += 1
    await new Promise((resolve) => setTimeout(resolve, 30))
    return { status: 'fresh', remainingUsd: '9.990000', rawRemaining: '9.99', rawUnit: 'usd', basis: 'wallet' } as const
  }
  await Promise.all([
    balanceQueryService.refreshAccountBalanceCandidate(candidate, { query }),
    balanceQueryService.refreshAccountBalanceCandidate(candidate, { query })
  ])
  assert.equal(queryCount, 1, '同一账户并发刷新必须通过租约只查询一次上游')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('account balance refresh regression passed')

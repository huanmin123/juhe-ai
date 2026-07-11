import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
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

const balanceServiceSource = readFileSync(resolve('src/modules/accounts/account-balance-query.service.ts'), 'utf8')
const accountRoutesSource = readFileSync(resolve('src/modules/accounts/accounts.routes.ts'), 'utf8')
const repositoriesSource = readFileSync(resolve('src/storage/repositories.ts'), 'utf8')
assert.match(balanceServiceSource, /const balanceRefreshLeaseMs = 30_000/)
assert.ok(!accountRoutesSource.includes('saveAccountBalanceConfigurationAsync'), '账户路由不应在账户保存后进行第二次余额配置写入')
assert.match(repositoriesSource, /balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at/)

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
  const configured = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'configured', type: 'api_key', credentials: { api_key: 'sk-configured', base_url: 'https://relay.example/v1' },
    groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'sub2api', intervalMinutes: 10 }
  }, access)
  assert.equal(configured.balanceQueryEnabled, true)
  assert.deepEqual(configured.balanceQueryConfig, { adapter: 'sub2api', intervalMinutes: 10 })
  const database = databaseModule.getBusinessDatabase()
  const configuredRow = database.prepare(`SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(configured.id) as Record<string, unknown>
  assert.equal(configuredRow.balance_query_enabled, 1)
  assert.deepEqual(JSON.parse(String(configuredRow.balance_query_config_json)), { adapter: 'sub2api', intervalMinutes: 10 })
  assert.equal(typeof configuredRow.balance_query_next_refresh_at, 'string')
  const configuredDisabled = repositories.updateAccount(configured.id, {
    balanceQueryEnabled: false,
    balanceQueryConfig: { adapter: 'sub2api', intervalMinutes: 10 }
  }, access)
  assert.equal(configuredDisabled?.balanceQueryEnabled, false)
  assert.equal(database.prepare(`SELECT balance_query_enabled FROM accounts WHERE id = ?`).get(configured.id)?.balance_query_enabled, 0)
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

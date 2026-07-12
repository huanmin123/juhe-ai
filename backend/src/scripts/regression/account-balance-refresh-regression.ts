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
const balanceRoutesSource = readFileSync(resolve('src/modules/accounts/account-balance.routes.ts'), 'utf8')
const balanceRepositorySource = readFileSync(resolve('src/storage/account-balance.repository.ts'), 'utf8')
const accountRoutesSource = readFileSync(resolve('src/modules/accounts/accounts.routes.ts'), 'utf8')
const repositoriesSource = readFileSync(resolve('src/storage/repositories.ts'), 'utf8')
assert.match(balanceServiceSource, /const balanceRefreshLeaseMs = 30_000/)
assert.match(
  balanceServiceSource,
  /const requestTimeoutMs = 15_000/,
  '余额查询的全部内置适配器应共享 15 秒总 deadline'
)
assert.match(balanceRoutesSource, /post\('\/balance\/test-draft'/, '新增和编辑表单必须使用独立草稿余额测试接口')
assert.match(balanceRoutesSource, /prepareAccountDraftTestSnapshotAsync/, '草稿余额测试必须使用当前表单账户快照')
assert.match(balanceRoutesSource, /testAccountBalanceCandidate/, '草稿余额测试必须调用无持久化查询入口')
assert.ok(!balanceRoutesSource.includes('saveAccountBalanceConfigurationAsync'), '余额路由不能在查询时保存账户配置')
assert.ok(!balanceRoutesSource.includes('delete_account_balance_snapshot'), '余额测试不能删除或替换已保存快照')
assert.match(balanceRepositorySource, /balance_query_config_json::jsonb = \?::jsonb/, 'PostgreSQL 偏好条件更新必须按 JSON 语义比较配置')
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
  const autoDetect = create('auto-detect')
  const configured = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'configured', type: 'api_key', credentials: { api_key: 'sk-configured', base_url: 'https://relay.example/v1' },
    groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  assert.equal(configured.balanceQueryEnabled, true)
  assert.deepEqual(configured.balanceQueryConfig, { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' })
  const database = databaseModule.getBusinessDatabase()
  const configuredRow = database.prepare(`SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(configured.id) as Record<string, unknown>
  assert.equal(configuredRow.balance_query_enabled, 1)
  assert.deepEqual(JSON.parse(String(configuredRow.balance_query_config_json)), { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' })
  assert.equal(typeof configuredRow.balance_query_next_refresh_at, 'string')
  const configuredDisabled = repositories.updateAccount(configured.id, {
    balanceQueryEnabled: false,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  assert.equal(configuredDisabled?.balanceQueryEnabled, false)
  assert.equal(database.prepare(`SELECT balance_query_enabled FROM accounts WHERE id = ?`).get(configured.id)?.balance_query_enabled, 0)
  const dueAt = '2026-07-11T00:00:00.000Z'
  const futureAt = '2026-07-12T00:00:00.000Z'
  const configure = database.prepare(`UPDATE accounts SET status = ?, schedulable = 1, balance_query_enabled = 1, balance_query_config_json = ?, balance_query_next_refresh_at = ? WHERE id = ?`)
  const builtinConfig = JSON.stringify({ adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' })
  configure.run('active', builtinConfig, dueAt, dueA.id)
  configure.run('active', builtinConfig, dueAt, dueB.id)
  configure.run('disabled', builtinConfig, dueAt, disabled.id)
  configure.run('active', builtinConfig, dueAt, oauth.id)
  configure.run('active', builtinConfig, dueAt, multi.id)
  configure.run('active', builtinConfig, futureAt, future.id)
  database.prepare(`UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`).run(autoDetect.id)

  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: dueA.id,
    expectedConfigRevision: dueA.configRevision ?? 1,
    expectedConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' },
    nextConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextRefreshAt: dueAt
  }), true, '成功回退后应记录新的内置适配偏好')
  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: dueA.id,
    expectedConfigRevision: dueA.configRevision ?? 1,
    expectedConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' },
    nextConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'newapi' },
    nextRefreshAt: dueAt
  }), false, '旧配置快照不得覆盖已经更新的适配偏好')
  assert.equal((await balanceRepository.findAccountBalanceRefreshCandidateAsync(dueA.id))?.config.preferredBuiltinAdapter, 'user_balance')
  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: dueA.id,
    expectedConfigRevision: dueA.configRevision ?? 1,
    expectedConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextConfig: { adapter: 'builtin', intervalMinutes: 5 },
    nextRefreshAt: dueAt
  }), true, '全部内置适配失败后应清除旧偏好')
  assert.equal((await balanceRepository.findAccountBalanceRefreshCandidateAsync(dueA.id))?.config.preferredBuiltinAdapter, undefined)

  const autoDetectCandidate = await balanceRepository.findAccountBalanceDetectionCandidateAsync(autoDetect.id, autoDetect.configRevision ?? 1)
  assert.equal(autoDetectCandidate?.id, autoDetect.id)
  assert.equal(await balanceRepository.enableDetectedAccountBalanceQueryAsync({
    accountId: autoDetect.id,
    expectedConfigRevision: (autoDetect.configRevision ?? 1) + 1,
    config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextRefreshAt: futureAt
  }), false, '旧配置版本不得自动开启余额查询')
  assert.equal(await balanceRepository.enableDetectedAccountBalanceQueryAsync({
    accountId: autoDetect.id,
    expectedConfigRevision: autoDetect.configRevision ?? 1,
    config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextRefreshAt: futureAt
  }), true)
  assert.equal(await balanceRepository.enableDetectedAccountBalanceQueryAsync({
    accountId: autoDetect.id,
    expectedConfigRevision: autoDetect.configRevision ?? 1,
    config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' },
    nextRefreshAt: futureAt
  }), false, '已经开启后不得被自动探测覆盖')
  assert.deepEqual((await balanceRepository.findAccountBalanceRefreshCandidateAsync(autoDetect.id))?.config, {
    adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance'
  })

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
  const tested = await balanceQueryService.testAccountBalanceCandidate(candidate, {
    query: async () => ({ status: 'fresh', remainingUsd: '8.880000', rawRemaining: '8.88', rawUnit: 'usd', basis: 'wallet' })
  })
  assert.equal(tested.remainingUsd, '8.880000')
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([candidate.id]).get(candidate.id)?.remainingUsd,
    undefined,
    '草稿余额测试不得写入正式余额快照'
  )
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

  const staleCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(dueB.id)
  assert.ok(staleCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(staleCandidate, {
    query: async () => {
      database.prepare(`UPDATE accounts SET balance_query_enabled = 0, balance_query_next_refresh_at = NULL WHERE id = ?`).run(dueB.id)
      return { status: 'fresh', remainingUsd: '12.340000', rawRemaining: '12.34', rawUnit: 'usd', basis: 'wallet' }
    }
  })
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([dueB.id]).has(dueB.id),
    false,
    '查询期间关闭余额功能后，旧结果不得重新写入快照'
  )
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('account balance refresh regression passed')

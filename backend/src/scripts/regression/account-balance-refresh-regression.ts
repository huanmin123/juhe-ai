import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { UpstreamRequestTimeoutError } from '../../modules/gateway/upstream/request.js'

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
const balanceRefreshJobSource = readFileSync(resolve('src/modules/background/account-balance-refresh.job.ts'), 'utf8')
assert.match(balanceServiceSource, /const balanceRefreshLeaseMs = 30_000/)
assert.match(
  balanceServiceSource,
  /const requestTimeoutMs = 15_000/,
  '余额查询的全部内置适配器应共享 15 秒总 deadline'
)
assert.match(balanceRoutesSource, /post\('\/balance\/test-draft'/, '新增和编辑表单必须使用独立草稿余额测试接口')
assert.match(balanceRoutesSource, /prepareAccountDraftTestSnapshotAsync/, '草稿余额测试必须使用当前表单账户快照')
assert.match(balanceRoutesSource, /testAccountBalanceCandidate/, '草稿余额测试必须调用无持久化查询入口')
assert.match(balanceRoutesSource, /refreshAccountBalanceCandidate\(candidate, \{ mode: 'manual' \}\)/, '列表人工刷新必须使用独立 manual 模式')
assert.ok(!balanceRoutesSource.includes('saveAccountBalanceConfigurationAsync'), '余额路由不能在查询时保存账户配置')
assert.ok(!balanceRoutesSource.includes('delete_account_balance_snapshot'), '余额测试不能删除或替换已保存快照')
assert.match(balanceRepositorySource, /balance_query_config_json::jsonb = \?::jsonb/, 'PostgreSQL 偏好条件更新必须按 JSON 语义比较配置')
assert.ok(!accountRoutesSource.includes('saveAccountBalanceConfigurationAsync'), '账户路由不应在账户保存后进行第二次余额配置写入')
assert.match(repositoriesSource, /balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at/)
assert.match(accountRoutesSource, /balanceDecision\.autoDisabledForMultipleApiKeys/, '账户编辑路由必须接受 repository 之前的多 Key 自动关闭决策')
assert.match(accountRoutesSource, /const balanceIdentityChanged = !isDeepStrictEqual/, '账户保存必须按余额查询身份变化决定是否清理旧快照')
assert.match(accountRoutesSource, /if \(balanceIdentityChanged\) \{[\s\S]*cleanupAccountBalanceSnapshotAfterSave/, '只有真实余额身份变化才允许清理旧快照')
assert.match(balanceRepositorySource, /listAccountsNeedingBalanceRefreshRecoveryAsync/, '余额 worker 必须能自愈活动账户缺快照且无刷新计划的状态')
assert.match(balanceRepositorySource, /postgresBalanceRecoveryAfterId/, 'PostgreSQL 自愈候选也必须使用轮转游标，不能固定扫描最小 ID 前缀')
assert.match(balanceRefreshJobSource, /const recoveryBatchSize = 10/, '每轮余额刷新必须为缺失调度自愈保留固定小配额')
assert.match(balanceServiceSource, /loadCurrentGenerationBalanceSnapshot\(candidate\)/, '租约冲突与瞬时失败只能复用当前刷新代次的余额快照')
assert.doesNotMatch(balanceServiceSource, /loadAccountBalanceSnapshotsByAccountIdsAsync/, '余额刷新 fallback 不能绕过刷新代次直接读取快照金额')

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
  const transientFailure = create('transient-failure')
  const deterministicFailure = create('deterministic-failure')
  const manualRefresh = create('manual-refresh')
  const lifecycle = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'lifecycle', type: 'api_key', credentials: { api_key: 'sk-lifecycle', base_url: 'https://relay.example/v1' },
    groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5 }
  }, access)
  const recoverMissing = create('recover-missing')
  const recoverPaused = create('recover-paused')
  const recoverInactive = create('recover-inactive')
  const configured = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'configured', type: 'api_key', credentials: { api_key: 'sk-configured', base_url: 'https://relay.example/v1' },
    groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  assert.equal(configured.balanceQueryEnabled, true)
  assert.deepEqual(configured.balanceQueryConfig, { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' })
  const database = databaseModule.getBusinessDatabase()
  const recoveryConfig = JSON.stringify({ adapter: 'builtin', intervalMinutes: 5 })
  database.prepare(`
    UPDATE accounts
    SET status = ?, schedulable = ?, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `).run('active', 1, recoveryConfig, recoverMissing.id)
  database.prepare(`
    UPDATE accounts
    SET status = ?, schedulable = ?, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `).run('active', 1, recoveryConfig, recoverPaused.id)
  database.prepare(`
    UPDATE accounts
    SET status = ?, schedulable = ?, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `).run('disabled', 0, recoveryConfig, recoverInactive.id)
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: recoverPaused.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'unsupported', errorMessage: '当前配置未找到可用余额接口' }
  })
  assert.deepEqual(
    (await balanceRepository.listAccountsNeedingBalanceRefreshRecoveryAsync({ limit: 100 })).map((item: { id: string }) => item.id),
    [recoverMissing.id],
    '自愈只应领取活动且缺少当前快照的账户，unsupported 暂停和非活动账户不得反复查询'
  )
  database.prepare(`UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`).run(recoverInactive.id)
  assert.deepEqual(
    new Set((await balanceRepository.listAccountsNeedingBalanceRefreshRecoveryAsync({ limit: 100 })).map((item: { id: string }) => item.id)),
    new Set([recoverMissing.id, recoverInactive.id]),
    '非活动缺快照账户恢复活动后应进入自愈候选'
  )
  const pausedPrefix = Array.from({ length: 45 }, (_, index) => create(`recover-paused-prefix-${index}`))
  const recoverAfterPausedPrefix = create('recover-after-paused-prefix')
  const configureRecovery = database.prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `)
  for (const account of pausedPrefix) {
    configureRecovery.run(recoveryConfig, account.id)
    balanceRepository.replaceAccountBalanceSnapshot({
      accountId: account.id,
      systemAccountId: 'sys_admin',
      snapshot: { status: 'unsupported', errorMessage: '暂停回归夹具' }
    })
  }
  configureRecovery.run(recoveryConfig, recoverAfterPausedPrefix.id)
  assert.ok(
    (await balanceRepository.listAccountsNeedingBalanceRefreshRecoveryAsync({ limit: 10 }))
      .some((item: { id: string }) => item.id === recoverAfterPausedPrefix.id),
    'SQLite 自愈游标必须越过前缀暂停账户，不能固定扫描最前一页'
  )
  const unconfiguredMultiRow = database.prepare(`SELECT balance_query_enabled, balance_query_config_json FROM accounts WHERE id = ?`).get(multi.id) as Record<string, unknown>
  assert.equal(unconfiguredMultiRow.balance_query_enabled, 0)
  assert.deepEqual(JSON.parse(String(unconfiguredMultiRow.balance_query_config_json)), { adapter: 'builtin', intervalMinutes: 5 }, '多 Key 新建即使未提交余额配置，也必须写入已配置关闭标记')
  const configuredRow = database.prepare(`SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(configured.id) as Record<string, unknown>
  assert.equal(configuredRow.balance_query_enabled, 1)
  assert.deepEqual(JSON.parse(String(configuredRow.balance_query_config_json)), { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' })
  assert.equal(typeof configuredRow.balance_query_next_refresh_at, 'string')
  const lifecycleGeneration = '2026-07-11T06:00:00.000Z'
  databaseModule.getBusinessDatabase().prepare(`UPDATE accounts SET balance_query_next_refresh_at = ? WHERE id = ?`).run(lifecycleGeneration, lifecycle.id)
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: lifecycle.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '12.340000', lastAttemptAt: lifecycleGeneration, lastSuccessAt: lifecycleGeneration },
    nextRefreshAfter: lifecycleGeneration
  })
  const lifecycleSameValue = repositories.updateAccount(lifecycle.id, {
    name: 'lifecycle-renamed',
    balanceQueryEnabled: true,
    balanceQueryConfig: { intervalMinutes: 5, adapter: 'builtin' }
  }, access)
  assert.ok(lifecycleSameValue)
  assert.equal(lifecycleSameValue.balanceQueryNextRefreshAt, lifecycleGeneration, '同值全量表单和无关字段修改必须保留刷新代次')
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([lifecycle.id]).get(lifecycle.id)?.remainingUsd,
    '12.340000',
    '同值全量表单和无关字段修改必须保留旧快照'
  )
  const lifecycleConnectionChanged = repositories.updateAccount(lifecycle.id, {
    credentials: { api_key: 'sk-lifecycle-next', base_url: 'https://relay.example/v1' },
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5 }
  }, access)
  assert.ok(lifecycleConnectionChanged)
  assert.notEqual(lifecycleConnectionChanged.balanceQueryNextRefreshAt, lifecycleGeneration, 'Key 变化必须开启新的刷新代次')
  const configuredDisabled = repositories.updateAccount(configured.id, {
    balanceQueryEnabled: false,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  assert.equal(configuredDisabled?.balanceQueryEnabled, false)
  assert.equal(database.prepare(`SELECT balance_query_enabled FROM accounts WHERE id = ?`).get(configured.id)?.balance_query_enabled, 0)
  const multiKeyRequestedBalance = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'multi-key-requested-balance', type: 'api_key',
    credentials: { api_keys: ['sk-multi-a', 'sk-multi-b'], base_url: 'https://relay.example/v1' },
    groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 7, preferredBuiltinAdapter: 'user_balance' }
  }, access)
  assert.equal(multiKeyRequestedBalance.balanceQueryEnabled, false, '新建多 Key 即使请求开启余额也必须保存成功并自动关闭')
  const multiKeyRequestedBalanceRow = database.prepare(`
    SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
    FROM accounts WHERE id = ?
  `).get(multiKeyRequestedBalance.id) as Record<string, unknown>
  assert.equal(multiKeyRequestedBalanceRow.balance_query_enabled, 0)
  assert.equal(multiKeyRequestedBalanceRow.balance_query_next_refresh_at, null)
  assert.deepEqual(JSON.parse(String(multiKeyRequestedBalanceRow.balance_query_config_json)), {
    adapter: 'builtin', intervalMinutes: 7, preferredBuiltinAdapter: 'user_balance'
  }, '多 Key 自动关闭不能丢失余额查询配置')

  const singleToMulti = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'single-to-multi', type: 'api_key', credentials: { api_key: 'sk-single', base_url: 'https://relay.example/v1' },
    groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' } }
  }, access)
  const singleToMultiOldGeneration = '2000-01-01T00:00:00.000Z'
  database.prepare(`UPDATE accounts SET balance_query_next_refresh_at = ? WHERE id = ?`).run(singleToMultiOldGeneration, singleToMulti.id)
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: singleToMulti.id,
    systemAccountId: 'sys_admin',
    snapshot: {
      status: 'fresh',
      remainingUsd: '41.000000',
      lastAttemptAt: singleToMultiOldGeneration,
      lastSuccessAt: singleToMultiOldGeneration
    },
    nextRefreshAfter: singleToMultiOldGeneration
  })
  const singleToMultiRevision = singleToMulti.configRevision ?? 1
  const singleToMultiUpdated = repositories.updateAccount(singleToMulti.id, {
    credentials: { api_keys: ['sk-single', 'sk-second'], base_url: 'https://relay.example/v1' }
  }, access)
  assert.ok(singleToMultiUpdated)
  assert.equal(singleToMultiUpdated.balanceQueryEnabled, false, '只更新凭据的直接 repository 入口也必须中央关闭余额')
  const singleToMultiRow = database.prepare(`
    SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, config_revision
    FROM accounts WHERE id = ?
  `).get(singleToMulti.id) as Record<string, unknown>
  assert.equal(singleToMultiRow.balance_query_enabled, 0)
  assert.equal(singleToMultiRow.balance_query_next_refresh_at, null)
  assert.deepEqual(JSON.parse(String(singleToMultiRow.balance_query_config_json)), {
    adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' }
  }, '凭据更新触发自动关闭时必须原样保留既有余额配置')
  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: singleToMulti.id,
    expectedConfigRevision: singleToMultiRevision,
    expectedConfig: { adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' } },
    nextConfig: { adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' } },
    nextRefreshAt: new Date().toISOString()
  }), false, '多 Key 保存递增配置版本后，进行中的旧余额结果不得提交')
  const returnedToSingle = repositories.updateAccount(singleToMulti.id, {
    credentials: { api_key: 'sk-single', base_url: 'https://relay.example/v1' }
  }, access)
  assert.ok(returnedToSingle)
  assert.equal(database.prepare(`SELECT balance_query_enabled FROM accounts WHERE id = ?`).get(singleToMulti.id)?.balance_query_enabled, 0, '多 Key 改回单 Key 后不能自动恢复余额查询')
  database.prepare(`UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`).run(singleToMulti.id)
  assert.equal(
    await balanceRepository.findAccountBalanceDetectionCandidateAsync(singleToMulti.id, returnedToSingle.configRevision ?? Number(singleToMultiRow.config_revision) + 1),
    undefined,
    '保留的余额配置必须阻止单 Key 恢复后被首次探测重新开启'
  )
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([singleToMulti.id]).get(singleToMulti.id)?.remainingUsd,
    '41.000000',
    '模拟 stats 清理失败和进程重启时，旧 Key 余额快照应仍留在持久化层'
  )
  const resumedSingle = repositories.updateAccount(singleToMulti.id, {
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' } }
  }, access)
  assert.ok(resumedSingle)
  const resumedCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(singleToMulti.id)
  assert.ok(resumedCandidate)
  assert.notEqual(resumedCandidate.nextRefreshAt, singleToMultiOldGeneration, '重新启用必须产生与旧快照不同的刷新代次')
  let markResumedQueryStarted: (() => void) | undefined
  const resumedQueryStarted = new Promise<void>((resolve) => {
    markResumedQueryStarted = resolve
  })
  let releaseResumedQuery: (() => void) | undefined
  const resumedQueryGate = new Promise<void>((resolve) => {
    releaseResumedQuery = resolve
  })
  const resumedRefresh = balanceQueryService.refreshAccountBalanceCandidate(resumedCandidate, {
    query: async () => {
      markResumedQueryStarted?.()
      await resumedQueryGate
      throw new UpstreamRequestTimeoutError('恢复单 Key 后首次余额查询超时')
    }
  })
  await resumedQueryStarted
  let unexpectedLeaseFallbackQueryCount = 0
  const leaseFallback = await balanceQueryService.refreshAccountBalanceCandidate(resumedCandidate, {
    query: async () => {
      unexpectedLeaseFallbackQueryCount += 1
      return { status: 'fresh', remainingUsd: '100.000000' }
    }
  })
  assert.equal(unexpectedLeaseFallbackQueryCount, 0, '未获得余额租约时不能再次请求上游')
  assert.equal(leaseFallback.status, 'refreshing', '未获得租约且持久化快照代次失配时只能返回刷新中')
  assert.equal(leaseFallback.remainingUsd, undefined, '未获得租约时不能回传旧 Key 的余额')
  releaseResumedQuery?.()
  const resumedFailure = await resumedRefresh
  assert.equal(resumedFailure.status, 'pending', '恢复单 Key后的首次瞬时失败不能复制旧 Key 的成功状态')
  assert.equal(resumedFailure.remainingUsd, undefined, '恢复单 Key后的首次瞬时失败不能把旧余额洗成当前代次')
  const resumedFailureRecord = balanceRepository.loadAccountBalanceSnapshotRecordsByAccountIds([singleToMulti.id]).get(singleToMulti.id)
  assert.equal(resumedFailureRecord?.snapshot.status, 'pending')
  assert.equal(resumedFailureRecord?.snapshot.remainingUsd, undefined)
  assert.notEqual(resumedFailureRecord?.nextRefreshAfter, singleToMultiOldGeneration, '新失败快照必须写入当前刷新链的新代次')
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
  configure.run('active', builtinConfig, futureAt, transientFailure.id)
  configure.run('active', builtinConfig, futureAt, deterministicFailure.id)
  configure.run('active', builtinConfig, futureAt, manualRefresh.id)
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
  const fallbackAttempts: string[] = []
  const fallbackResult = await balanceQueryService.queryBuiltinAccountBalance(candidate, {
    queryAdapter: async (_candidate, adapter) => {
      fallbackAttempts.push(adapter)
      return adapter === 'user_balance'
        ? { status: 'fresh', remainingUsd: '4.790000', rawRemaining: '4.79', rawUnit: 'usd', basis: 'wallet' }
        : { status: 'unsupported', basis: 'api_key_quota' }
    }
  })
  assert.equal(fallbackResult.adapter, 'user_balance', '内置适配器返回 unsupported 后必须继续回退')
  assert.equal(fallbackResult.snapshot.remainingUsd, '4.790000')
  assert.deepEqual(fallbackAttempts, ['sub2api', 'newapi', 'litellm', 'user_balance'])
  const unsupportedResult = await balanceQueryService.queryBuiltinAccountBalance(candidate, {
    queryAdapter: async () => ({ status: 'unsupported', basis: 'api_key_quota' })
  })
  assert.equal(unsupportedResult.snapshot.status, 'unsupported', '全部内置适配器不支持时应返回可暂停的能力状态')
  let authenticationAttempts = 0
  await assert.rejects(
    balanceQueryService.queryBuiltinAccountBalance(candidate, {
      queryAdapter: async () => {
        authenticationAttempts += 1
        throw new Error('上游鉴权失败（HTTP 401）')
      }
    }),
    /上游鉴权失败/
  )
  assert.equal(authenticationAttempts, 1, '确定性鉴权错误不能继续尝试其他适配器')
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

  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: transientFailure.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '7.310000', lastAttemptAt: dueAt, lastSuccessAt: dueAt },
    nextRefreshAfter: futureAt
  })
  let transientCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(transientCandidate)
  const timeoutQuery = async () => {
    throw new UpstreamRequestTimeoutError('上游余额查询超时')
  }
  await balanceQueryService.refreshAccountBalanceCandidate(transientCandidate, { query: timeoutQuery })
  let transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.status, 'fresh', '第一次临时失败必须保留上次成功状态')
  assert.equal(transientSnapshot.remainingUsd, '7.310000', '第一次临时失败必须保留上次成功金额')
  assert.equal(transientSnapshot.consecutiveTransientFailures, 1)
  assertBalanceRetryDelay(database, transientFailure.id, transientSnapshot.lastAttemptAt, 5)
  transientCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(transientCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(transientCandidate, { query: timeoutQuery })
  transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.status, 'fresh', '第二次临时失败仍应保留上次成功状态')
  assert.equal(transientSnapshot.consecutiveTransientFailures, 2)
  assertBalanceRetryDelay(database, transientFailure.id, transientSnapshot.lastAttemptAt, 5)
  transientCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(transientCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(transientCandidate, { query: timeoutQuery })
  transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.status, 'failed', '连续第三次临时失败才应标记查询失败')
  assert.equal(transientSnapshot.remainingUsd, undefined, '连续第三次临时失败必须清除旧金额')
  assert.equal(transientSnapshot.consecutiveTransientFailures, 3)
  assertBalanceRetryDelay(database, transientFailure.id, transientSnapshot.lastAttemptAt, 5)
  transientCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(transientCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(transientCandidate, { query: timeoutQuery })
  transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.consecutiveTransientFailures, 3, '连续失败次数应在 3 封顶')
  assertBalanceRetryDelay(database, transientFailure.id, transientSnapshot.lastAttemptAt, 5)
  const recoveryCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(recoveryCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(recoveryCandidate, {
    query: async () => ({ status: 'fresh', remainingUsd: '8.880000', rawRemaining: '8.88', rawUnit: 'usd', basis: 'wallet' })
  })
  transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.status, 'fresh')
  assert.equal(transientSnapshot.remainingUsd, '8.880000')
  assert.equal(transientSnapshot.consecutiveTransientFailures, undefined, '成功后必须清零连续临时失败次数')

  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: deterministicFailure.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '5.550000', lastAttemptAt: dueAt, lastSuccessAt: dueAt }
  })
  const deterministicCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(deterministicFailure.id)
  assert.ok(deterministicCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(deterministicCandidate, {
    query: async () => { throw new Error('上游鉴权失败（HTTP 401）') }
  })
  const deterministicSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([deterministicFailure.id]).get(deterministicFailure.id)
  assert.ok(deterministicSnapshot)
  assert.equal(deterministicSnapshot.status, 'unsupported', '确定性鉴权错误必须暂停余额能力而不是反复轮询')
  assert.equal(deterministicSnapshot.remainingUsd, undefined)
  assert.equal(deterministicSnapshot.consecutiveTransientFailures, undefined)
  const deterministicRow = database.prepare(`SELECT balance_query_enabled, balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(deterministicFailure.id) as Record<string, unknown>
  assert.equal(deterministicRow.balance_query_enabled, 1, '能力暂停不能替用户关闭余额开关')
  assert.equal(deterministicRow.balance_query_next_refresh_at, null, '确定性不支持必须停止后台余额调度')
  const invalidBaseUrlResult = await balanceQueryService.refreshAccountBalanceCandidate({
    ...deterministicCandidate,
    credentials: { api_key: 'sk-invalid-base-url', base_url: 'not-a-valid-url' }
  })
  assert.equal(invalidBaseUrlResult.status, 'unsupported')
  assert.equal(invalidBaseUrlResult.errorMessage, '账户 Base URL 无效', '确定性本地配置错误应保留可读原因')
  const reactivated = repositories.updateAccount(deterministicFailure.id, {
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  assert.ok(reactivated)
  const reactivatedRow = database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(deterministicFailure.id) as { balance_query_next_refresh_at?: string | null }
  assert.ok(reactivatedRow.balance_query_next_refresh_at, '保存账户配置必须重新激活已暂停的余额调度')
  const reactivatedCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(deterministicFailure.id)
  assert.ok(reactivatedCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(reactivatedCandidate, { query: timeoutQuery })
  const snapshotAfterReactivationTimeout = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([deterministicFailure.id]).get(deterministicFailure.id)
  assert.equal(snapshotAfterReactivationTimeout?.status, 'pending', '暂停账户重新激活后的临时失败必须显示待重试，而不是继续显示已暂停')
  assert.equal(snapshotAfterReactivationTimeout?.consecutiveTransientFailures, 1)
  assertBalanceRetryDelay(database, deterministicFailure.id, snapshotAfterReactivationTimeout?.lastAttemptAt, 5)

  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: manualRefresh.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '6.660000', lastAttemptAt: dueAt, lastSuccessAt: dueAt }
  })
  const manualCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(manualRefresh.id)
  assert.ok(manualCandidate)
  const manualResult = await balanceQueryService.refreshAccountBalanceCandidate(manualCandidate, {
    mode: 'manual',
    query: timeoutQuery
  })
  assert.equal(manualResult.status, 'failed', '人工刷新失败必须立即返回本次错误')
  const storedAfterManualFailure = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([manualRefresh.id]).get(manualRefresh.id)
  assert.ok(storedAfterManualFailure)
  assert.equal(storedAfterManualFailure.status, 'failed', '人工刷新失败必须立即替换当前行状态')
  assert.equal(storedAfterManualFailure.remainingUsd, undefined, '人工刷新失败不能继续展示旧金额')
  const manualUnsupported = await balanceQueryService.refreshAccountBalanceCandidate(manualCandidate, {
    mode: 'manual',
    query: async () => ({ status: 'unsupported', basis: 'api_key_quota' })
  })
  assert.equal(manualUnsupported.status, 'unsupported', '人工刷新必须即时返回能力不支持状态')
  const storedAfterManualUnsupported = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([manualRefresh.id]).get(manualRefresh.id)
  assert.equal(storedAfterManualUnsupported?.status, 'unsupported', '人工能力探测失败必须保存失败语义')
  assert.equal(storedAfterManualUnsupported?.remainingUsd, undefined, '人工能力探测失败不能继续展示旧金额')
  assert.equal(
    database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(manualRefresh.id)?.balance_query_next_refresh_at,
    null,
    '人工能力不支持必须暂停自动刷新，避免后台重复查询'
  )
  const manualSuccessCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(manualRefresh.id)
  assert.ok(manualSuccessCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(manualSuccessCandidate, {
    mode: 'manual',
    query: async () => ({ status: 'fresh', remainingUsd: '9.010000', rawRemaining: '9.01', rawUnit: 'usd', basis: 'wallet' })
  })
  const storedAfterManualSuccess = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([manualRefresh.id]).get(manualRefresh.id)
  assert.ok(storedAfterManualSuccess)
  assert.equal(storedAfterManualSuccess.remainingUsd, '9.010000', '人工刷新成功必须保存新金额')
  assert.notEqual(
    database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(manualRefresh.id)?.balance_query_next_refresh_at,
    futureAt,
    '人工刷新成功必须推进自动刷新时间'
  )

  const staleCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(dueB.id)
  assert.ok(staleCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(staleCandidate, {
    query: async () => {
      const updated = repositories.updateAccount(dueB.id, {
        credentials: { api_keys: ['sk-due-b', 'sk-due-b-second'], base_url: 'https://relay.example/v1' }
      }, access)
      assert.ok(updated)
      assert.equal(updated.balanceQueryEnabled, false)
      return { status: 'fresh', remainingUsd: '12.340000', rawRemaining: '12.34', rawUnit: 'usd', basis: 'wallet' }
    }
  })
  const staleRow = database.prepare(`SELECT balance_query_enabled, balance_query_next_refresh_at, config_revision FROM accounts WHERE id = ?`).get(dueB.id) as Record<string, unknown>
  assert.equal(staleRow.balance_query_enabled, 0)
  assert.equal(staleRow.balance_query_next_refresh_at, null)
  assert.ok(Number(staleRow.config_revision) > staleCandidate.configRevision, '多 Key 保存必须递增配置版本，使在途查询结果失效')
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

function assertBalanceRetryDelay(
  database: ReturnType<typeof databaseModule.getBusinessDatabase>,
  accountId: string,
  lastAttemptAt: string | undefined,
  expectedMinutes: number
): void {
  assert.ok(lastAttemptAt)
  const row = database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(accountId) as { balance_query_next_refresh_at?: string | null }
  assert.ok(row.balance_query_next_refresh_at)
  const delayMs = Date.parse(row.balance_query_next_refresh_at) - Date.parse(lastAttemptAt)
  assert.ok(
    Math.abs(delayMs - expectedMinutes * 60_000) < 1_500,
    `余额临时失败应在约 ${expectedMinutes} 分钟后重试，实际 ${delayMs}ms`
  )
}

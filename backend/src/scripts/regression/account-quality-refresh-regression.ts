import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import type { AccountQualityRealtimeRefreshResult } from '../../storage/account-quality.repository.js'
import { minuteKey, usageStatsTimezone } from '../../storage/usage-stats-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-quality-refresh-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-quality-refresh.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-quality-refresh-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, accountQualityRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-quality.repository.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  assertSourceGuards()

  const statsDatabase = databaseModule.getStatsDatabase()
  const group = repositories.createGroup({
    name: '账号质量刷新回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '质量刷新回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-quality-refresh',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)
  const staleAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '质量刷新无新样本账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-quality-stale-refresh',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)
  const failureCandidateAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '质量刷新频繁失败候选账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-quality-frequent-failure',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)
  const batchAccounts = Array.from({ length: 5 }, (_, index) => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `质量刷新批量账户 ${index}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-quality-batch-${index}`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access))
  const businessDatabase = databaseModule.getBusinessDatabase()
  const activateAccount = businessDatabase.prepare(`
    UPDATE accounts
    SET status = 'active',
        schedulable = 1,
        last_error_code = NULL,
        last_error_message = NULL
    WHERE id = ?
  `)
  for (const activeAccount of [account, staleAccount, failureCandidateAccount, ...batchAccounts]) {
    activateAccount.run(activeAccount.id)
  }
  const nowDate = new Date()
  const now = nowDate.toISOString()
  const statMinute = minuteKey(nowDate, usageStatsTimezone())
  const inactiveAccountId = 'acct_quality_inactive_batch_cleanup'
  statsDatabase
    .prepare(`
      INSERT INTO account_quality_minute_stats (
        account_id, system_account_id, provider_code, stat_minute,
        request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
        last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
      ) VALUES (?, ?, ?, ?, 1, 0, 1, 0, 0, ?, NULL, ?, ?, ?)
    `)
    .run(account.id, 'sys_admin', 'gpt', statMinute, now, now, '质量刷新模拟错误', now)
  markAccountQualityDirty(account.id, now)
  statsDatabase
    .prepare(`
      INSERT INTO account_quality_minute_stats (
        account_id, system_account_id, provider_code, stat_minute,
        request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
        last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
      ) VALUES (?, ?, ?, ?, 5, 0, 5, 0, 0, ?, NULL, ?, ?, ?)
    `)
    .run(failureCandidateAccount.id, 'sys_admin', 'gpt', statMinute, now, now, '质量刷新模拟频繁错误', now)
  markAccountQualityDirty(failureCandidateAccount.id, now)
  for (const [index, batchAccount] of batchAccounts.entries()) {
    statsDatabase
      .prepare(`
        INSERT INTO account_quality_minute_stats (
          account_id, system_account_id, provider_code, stat_minute,
          request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
          last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
        ) VALUES (?, ?, ?, ?, 1, 1, 0, ?, 1, ?, ?, NULL, NULL, ?)
      `)
      .run(batchAccount.id, 'sys_admin', 'gpt', statMinute, 800 + index, now, now, now)
    markAccountQualityDirty(batchAccount.id, now)
  }
  for (let index = 0; index < 1205; index += 1) {
    const inactiveMinute = minuteKey(new Date(nowDate.getTime() - (60 + index) * 60 * 1000), usageStatsTimezone())
    statsDatabase
      .prepare(`
        INSERT INTO account_quality_minute_stats (
          account_id, system_account_id, provider_code, stat_minute,
          request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
          last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
        ) VALUES (?, 'sys_admin', 'gpt', ?, 1, 1, 0, 800, 1, ?, ?, NULL, NULL, ?)
      `)
      .run(inactiveAccountId, inactiveMinute, now, now, now)
  }
  statsDatabase
    .prepare(`
      INSERT INTO account_quality_scores (
        account_id, system_account_id, provider_code, quality_score, quality_state,
        recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
        recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
        window_started_at, window_ended_at, last_sample_at, updated_at
      ) VALUES (?, 'sys_admin', 'gpt', 1000, 'fresh', 1, 1, 0, 1, 1000, 1000, 1, ?, ?, ?, ?)
    `)
    .run(staleAccount.id, now, now, now, now)

  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  let qualityScoreUpsertPrepares = 0
  statsDatabase.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+account_quality_scores\b/i.test(sql)) {
      qualityScoreUpsertPrepares += 1
    }
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare

  let result: AccountQualityRealtimeRefreshResult
  try {
    result = accountQualityRepository.refreshAccountQualityFromUsage(10)
  } finally {
    statsDatabase.prepare = originalPrepare
  }
  assert.equal(result.refreshed, 2 + batchAccounts.length, '账号质量刷新应处理分钟桶样本')
  assert.equal(qualityScoreUpsertPrepares, 1, '账号质量刷新应复用 account_quality_scores upsert statement')
  assert.equal(inactiveQualityMinuteCount(inactiveAccountId), 205, '账号质量刷新应小批清理已失效账户分钟桶，剩余等待后续轮次')
  assert.equal(accountQualityDirtyCount(), 0, '账号质量刷新成功后应删除已消费的 dirty 账号窗口')
  const row = statsDatabase
    .prepare('SELECT quality_score, quality_state, recent_error_count, last_error_message FROM account_quality_scores WHERE account_id = ?')
    .get(account.id) as { quality_score?: number; quality_state?: string; recent_error_count?: number; last_error_message?: string } | undefined
  assert.equal(row?.quality_state, 'unknown', '只有失败样本时不能把账号质量标记为失败，避免请求形态错误污染调度')
  assert.equal(row?.recent_error_count, 1)
  assert.equal(row?.last_error_message, '质量刷新模拟错误')
  assert(row?.quality_score && row.quality_score >= 1_000_000, '没有成功首段样本时质量分应保持未知保守值')
  const listedAccount = repositories
    .listAccountsPage(access, { keyword: account.name, page: 1, pageSize: 10 })
    .items.find((item) => item.id === account.id)
  assert.equal(listedAccount?.status, 'active', '账号质量反馈不应写成账户持久状态')
  assert.equal(listedAccount?.effectiveAvailability.status, 'available', '账号质量反馈不应改变可用性筛选语义')
  assert.equal(listedAccount?.qualityRecentErrorCount, 1, '账户列表应返回近窗口失败数，供状态列细分正常状态')
  assert.equal(listedAccount?.qualityLastErrorMessage, '质量刷新模拟错误', '账户列表应返回最后质量错误，供状态 tooltip 解释')
  const failureCandidates = accountQualityRepository.listAccountQualityFailurePrecheckCandidates(10)
  const failureCandidate = failureCandidates.find((item) => item.accountId === failureCandidateAccount.id)
  assert(failureCandidate, '近窗口频繁失败账户应进入后台确认候选')
  assert.equal(failureCandidate.recentRequestCount, 5)
  assert.equal(failureCandidate.recentErrorCount, 5)
  assert.equal(failureCandidate.successRate, 0)
  assert.equal(
    failureCandidates.some((item) => item.accountId === account.id),
    false,
    '普通单次失败质量反馈不应进入后台确认候选'
  )
  assert.equal(
    repositories.findAccountSummary(failureCandidateAccount.id, access)?.status,
    'active',
    '账号质量刷新只生成确认候选，不直接写持久临时不可调用'
  )
  const staleRow = statsDatabase
    .prepare('SELECT quality_state, recent_request_count FROM account_quality_scores WHERE account_id = ?')
    .get(staleAccount.id) as { quality_state?: string; recent_request_count?: number } | undefined
  assert.equal(staleRow?.quality_state, 'stale', '活跃账户没有新质量样本时应标记为 stale')
  assert.equal(staleRow?.recent_request_count, 0)
  for (const batchAccount of batchAccounts) {
    const batchRow = statsDatabase
      .prepare('SELECT quality_state, recent_success_count FROM account_quality_scores WHERE account_id = ?')
      .get(batchAccount.id) as { quality_state?: string; recent_success_count?: number } | undefined
    assert.equal(batchRow?.quality_state, 'fresh', '批量质量样本账号应标记为 fresh')
    assert.equal(batchRow?.recent_success_count, 1)
  }

  console.log('账号质量刷新回归通过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function inactiveQualityMinuteCount(accountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_quality_minute_stats WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function markAccountQualityDirty(accountId: string, updatedAt: string): void {
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO account_quality_dirty_accounts (account_id, first_dirty_at, updated_at)
      VALUES (?, ?, ?)
      ON CONFLICT(account_id) DO UPDATE SET updated_at = excluded.updated_at
    `)
    .run(accountId, updatedAt, updatedAt)
}

function accountQualityDirtyCount(): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_quality_dirty_accounts')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function assertSourceGuards(): void {
  const source = readFileSync(resolve('src/storage/account-quality.repository.ts'), 'utf8')
  const schemaSource = readFileSync(resolve('src/storage/schema/stats-schema.ts'), 'utf8')
  const accountQualityWriterSource = readFileSync(resolve('src/storage/usage-stats-account-quality-writer.ts'), 'utf8')
  const failurePrecheckSource = readFileSync(resolve('src/modules/background/account-quality-failure-precheck.service.ts'), 'utf8')
  const gatewayAccountSideEffectsSource = readFileSync(resolve('src/modules/gateway/runtime/account-side-effects.service.ts'), 'utf8')
  assert.doesNotMatch(source, /SELECT id, system_account_id, provider_code FROM accounts'\)\s*\.all\(\)/, '账号质量刷新不应一次性加载全部账号元数据')
  assert.doesNotMatch(source, /SELECT \$\{accountQualitySelectColumns\(\)\} FROM account_quality_scores`\)\s*\.all\(\)/, '账号质量刷新不应一次性加载全部质量缓存')
  assert.doesNotMatch(source, /FROM account_quality_minute_stats quality_stats\s+WHERE quality_stats\.stat_minute >= \?\s+GROUP BY quality_stats\.account_id/i, '账号质量刷新不应按近窗口全量 GROUP BY 所有样本账号')
  assert.match(source, /account_quality_dirty_accounts INDEXED BY idx_account_quality_dirty_accounts_updated/, '账号质量刷新应先读取固定 dirty 账号窗口')
  assert.match(source, /listAccountQualityFailurePrecheckCandidates/, '账号质量刷新应暴露频繁失败确认候选查询')
  assert.match(source, /account_quality_scores INDEXED BY idx_account_quality_scores_failure_precheck/, '频繁失败确认候选查询应命中专用索引')
  assert.match(schemaSource, /idx_account_quality_scores_failure_precheck/, '统计库应为账号质量频繁失败确认候选提供索引')
  assert.match(schemaSource, /CREATE TABLE IF NOT EXISTS account_quality_dirty_accounts/, '统计库应保存账号质量 dirty 游标表')
  assert.match(schemaSource, /idx_account_quality_dirty_accounts_updated/, '账号质量 dirty 表应有更新时间窗口索引')
  assert.match(accountQualityWriterSource, /markAccountQualityDirty/, '用量统计写入账号质量分钟桶时应同步打 dirty 标记')
  assert.match(failurePrecheckSource, /loadAccountForTestViaDbService\(item\.accountId,\s*accountAccess\)/, '频繁失败确认应按质量样本所属系统账户上下文读取账户')
  assert.match(failurePrecheckSource, /requestBackgroundWorkerDbService\(\{\s*type:\s*'mark_account_test_temporary_unavailable'/, '频繁失败确认落库应通过 DB service 复用账户测试临时不可调用语义')
  assert.doesNotMatch(failurePrecheckSource, /\bmodel\s*:/, '频繁失败确认应交给统一账户测试模型解析器，不能复用失败请求模型')
  assert.match(failurePrecheckSource, /systemAccountId:\s*item\.systemAccountId/, '频繁失败确认应显式使用质量样本所属用户的个人默认模型作用域')
  assert.match(failurePrecheckSource, /trafficSource:\s*'runtime_recovery_probe'/, '频繁失败运行态确认探针应使用独立来源，避免和持久冷却复测混用')
  assert.doesNotMatch(failurePrecheckSource, /requestShape/, '频繁失败确认探针不能复用失败请求形态')
  assert.doesNotMatch(gatewayAccountSideEffectsSource, /\bmodel:\s*await preferredSystemAccountTestModelAsync/, '运行态恢复探针应交给统一账户测试模型解析器')
  assert.match(gatewayAccountSideEffectsSource, /systemAccountId:\s*state\.systemAccountId/, '运行态恢复探针应显式使用当前运行态用户作用域')
  assert.match(gatewayAccountSideEffectsSource, /trafficSource:\s*'runtime_recovery_probe'/, '运行态恢复探针应使用独立来源，避免和持久冷却复测混用')
  assert.match(gatewayAccountSideEffectsSource, /generation:\s*number/, '运行态恢复和事前确认状态必须带 generation，避免旧探针结果覆盖新状态')
  assert.doesNotMatch(gatewayAccountSideEffectsSource, /requestShape:/, '运行态恢复探针不能传入失败请求形态')
  assert.match(source, /loadQualityAccountMetadataByIds/, '账号质量刷新应按样本或固定候选账号批量补业务元数据')
  assert.match(source, /ORDER BY updated_at ASC, account_id ASC\s+LIMIT \?/, '账号质量缓存清理和 stale 推进必须按固定批次')
  assert.match(source, /temp_refreshed_quality_accounts/, '账号质量 stale 推进应避开本轮已刷新账号')
}

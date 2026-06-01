import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-list-stable-sort-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-list-stable-sort.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-list-stable-sort-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
let testGroupId = ''

try {
  const group = repositories.createGroup({
    name: '列表稳定排序分组',
    providerCode: 'openai'
  }, access)
  testGroupId = group.id
  const accounts = [
    createStableAccount('列表稳定排序-A', 'sk-list-stable-a', '2026-01-01T00:00:00.000Z'),
    createStableAccount('列表稳定排序-B', 'sk-list-stable-b', '2026-01-01T00:00:01.000Z'),
    createStableAccount('列表稳定排序-C', 'sk-list-stable-c', '2026-01-01T00:00:02.000Z')
  ]
  const expectedIds = accounts.map((account) => account.id)

  assert.deepEqual(listStableAccountIds(expectedIds), expectedIds, '同优先级账户初始列表顺序应按创建顺序稳定')

  repositories.updateAccount(accounts[1].id, { superPriorityEnabled: true }, access)
  assert.deepEqual(listStableAccountIds(expectedIds), expectedIds, '切换超级优先不应改变默认列表相对顺序')

  repositories.updateAccount(accounts[2].id, { fallbackEnabled: true }, access)
  assert.deepEqual(listStableAccountIds(expectedIds), expectedIds, '切换降级备用不应改变默认列表相对顺序')

  seedQualityScore(accounts[1].id, 900)
  seedQualityScore(accounts[2].id, 800)
  const qualityPage = repositories.listAccountsPage(access, {
    sorts: [{ field: 'qualityScore', order: 'desc' }],
    page: 1,
    pageSize: 2
  })
  assert.deepEqual(qualityPage.items.map((account) => account.id), [accounts[1].id, accounts[2].id], '质量分排序应由 SQL 分页返回当前页')
  assert.equal(qualityPage.total, 3, '质量分排序分页仍应返回完整筛选总数')

  console.log('AI 账户列表稳定排序回归通过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createStableAccount(name: string, apiKey: string, createdAt: string): { id: string } {
  const account = repositories.createAccount({
    providerCode: 'openai',
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    priority: 10,
    schedulable: true,
    groupId: testGroupId
  }, access)
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET created_at = ?, updated_at = ? WHERE id = ?')
    .run(createdAt, createdAt, account.id)
  return { id: account.id }
}

function listStableAccountIds(expectedIds: string[]): string[] {
  const expected = new Set(expectedIds)
  return repositories.listAccounts(access)
    .filter((account) => expected.has(account.id))
    .map((account) => account.id)
}

function seedQualityScore(accountId: string, qualityScore: number): void {
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO account_quality_scores (
        account_id, system_account_id, provider_code, quality_score, quality_state,
        recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
        recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
        window_started_at, window_ended_at, updated_at
      ) VALUES (?, 'sys_admin', 'openai', ?, 'healthy', 1, 1, 0, 1, 1000, 1000, 1, ?, ?, ?)
    `)
    .run(accountId, qualityScore, now, now, now)
}

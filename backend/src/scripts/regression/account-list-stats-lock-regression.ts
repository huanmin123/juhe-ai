import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-list-stats-lock-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-list-stats-lock-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '统计锁回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '统计锁回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-list-stats-lock',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)

  seedAccountUsage(account.id)
  const baselinePage = repositories.listAccountItemsPageReadOnly(access, { page: 1, pageSize: 20 })
  const baselineAccount = baselinePage.items.find((item) => item.id === account.id)
  assert.equal(baselineAccount?.usage.requestCount, 0, '账户基础列表不应同步读取账号累计用量')

  const statsDatabase = databaseModule.getStatsDatabase()
  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  const originalExec = statsDatabase.exec.bind(statsDatabase) as typeof statsDatabase.exec
  const busyTimeouts: number[] = []
  statsDatabase.exec = ((sql: string) => {
    const match = /PRAGMA\s+busy_timeout\s*=\s*(\d+)/i.exec(sql)
    if (match) {
      busyTimeouts.push(Number(match[1]))
    }
    return originalExec(sql)
  }) as typeof statsDatabase.exec
  statsDatabase.prepare = ((sql: string) => {
    if (isAccountListStatsLookupSql(sql)) {
      throw sqliteBusyError()
    }
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare

  let lockedPage: typeof baselinePage | undefined
  try {
    lockedPage = repositories.listAccountItemsPageReadOnly(access, { page: 1, pageSize: 20 })
  } finally {
    statsDatabase.prepare = originalPrepare
    statsDatabase.exec = originalExec
  }
  assert(lockedPage?.items.some((item) => item.id === account.id), '统计库忙锁不应阻塞不读取统计表的账户基础列表')
  assert.deepEqual(busyTimeouts, [], '账户基础列表不应为了累计用量修改统计库 busy_timeout')

  console.log('AI 账户列表统计锁回归通过：基础列表不读取累计统计，统计库 busy 不再阻塞首屏')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedAccountUsage(accountId: string): void {
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_stats_totals (
        system_account_id, scope_type, scope_id,
        request_count, input_tokens, output_tokens, total_cost_usd,
        last_used_at, updated_at
      )
      VALUES (?, 'account', ?, 7, 1200, 800, 0.0123, ?, ?)
    `)
    .run('sys_admin', accountId, now, now)
}

function isAccountListStatsLookupSql(sql: string): boolean {
  return /\bFROM\s+(usage_stats_totals|usage_stats_daily|account_usage_snapshots)\b/i.test(sql)
}

function sqliteBusyError(): Error {
  const error = new Error('database is locked') as Error & { errcode?: number; errstr?: string; code?: string }
  error.errcode = 5
  error.errstr = 'database is locked'
  error.code = 'SQLITE_BUSY'
  return error
}

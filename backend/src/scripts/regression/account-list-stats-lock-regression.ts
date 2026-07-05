import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

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
  const baselinePage = repositories.listAccountsPage(access, { page: 1, pageSize: 20 })
  const baselineAccount = baselinePage.items.find((item) => item.id === account.id)
  assert.equal(baselineAccount?.usage.requestCount, 7, '统计库未加锁时账户列表应正常展示账号总用量')

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

  const startedAt = performance.now()
  let lockedError: unknown
  try {
    repositories.listAccountsPage(access, { page: 1, pageSize: 20 })
  } catch (error) {
    lockedError = error
  } finally {
    statsDatabase.prepare = originalPrepare
    statsDatabase.exec = originalExec
  }
  const durationMs = performance.now() - startedAt

  assert(lockedError instanceof Error, '统计库忙锁时账户列表应明确失败，不能伪造空统计后成功返回')
  assert.match(lockedError.message, /SQLite 忙锁/, '统计库忙锁错误应说明统计装饰读取失败')
  assert.match(lockedError.message, /未返回伪造空统计/, '统计库忙锁错误应说明不会返回假空统计')
  assert.equal((lockedError as Error & { code?: string }).code, 'SQLITE_BUSY', '统计库忙锁错误应保留 SQLITE_BUSY 语义')
  assert(busyTimeouts.includes(60), '账户列表统计装饰读取应临时使用短 SQLite busy_timeout')
  assert(busyTimeouts.includes(5000), '账户列表统计装饰读取结束后应恢复默认 SQLite busy_timeout')
  assert(
    durationMs < 500,
    `统计库忙锁时账户列表应在短等待后失败，不能长时间阻塞，实际耗时 ${durationMs.toFixed(1)}ms`
  )

  console.log('AI 账户列表统计锁回归通过：统计库 busy 时列表短等待后明确失败，不返回伪造空统计')
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

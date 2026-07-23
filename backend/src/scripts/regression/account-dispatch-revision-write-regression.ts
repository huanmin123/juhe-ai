import assert from 'node:assert/strict'
import { mkdirSync, rmSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { advanceAccountCircuitDispatchRevisionInSqliteTransaction } from '../../storage/account-circuit-control-plane.repository.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-dispatch-revision-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'dispatch-revision-write-regression'
runtimeConfig.processRole = 'worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false

const repositories = await import('../../storage/repositories.js')
const { closeStorageDatabases, getBusinessDatabase } = await import('../../storage/database.js')

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const database = getBusinessDatabase()
const source = readFileSync(resolve('src/storage/repositories.ts'), 'utf8')

try {
  assert.match(source, /advanceAccountCircuitDispatchRevisionInSqliteTransaction\(database/, '同步账户写入必须在账户事务内推进 dispatch revision')
  assert.match(source, /advanceAccountCircuitDispatchRevisionInTransaction\(client/, '异步账户写入必须在 DatabaseClient 事务内推进 dispatch revision')

  const group = repositories.createGroup({
    name: 'dispatch revision write regression',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'dispatch revision write account',
    type: 'api_key',
    credentials: { api_key: 'sk-dispatch-revision-write', base_url: 'https://api.openai.com/v1' },
    groupId: group.id,
    status: 'disabled'
  }, access)

  const created = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(created.dispatch_revision, 2, '新建账户必须写入一次初始 dispatch revision 事件')
  assert.equal((database.prepare("SELECT COUNT(*) AS count FROM account_circuit_outbox WHERE account_id = ? AND event_type = 'dispatch_revision_changed'").get(account.id) as { count: number }).count, 1)

  repositories.updateAccount(account.id, { notes: '仅备注变化' }, access)
  const afterNotes = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterNotes.dispatch_revision, 2, '非调度字段更新不得推进 dispatch revision')

  repositories.updateAccount(account.id, { priority: 5 }, access)
  const afterPriority = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterPriority.dispatch_revision, 3, '优先级变化必须推进 dispatch revision')

  repositories.updateAccount(account.id, { credentials: { api_key: 'sk-dispatch-revision-write-2', base_url: 'https://api.openai.com/v1' } }, access)
  const afterCredentials = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterCredentials.dispatch_revision, 4, '凭据变化必须推进 dispatch revision')
  assert.equal((database.prepare("SELECT COUNT(*) AS count FROM account_circuit_outbox WHERE account_id = ? AND event_type = 'dispatch_revision_changed'").get(account.id) as { count: number }).count, 3)

  database.exec('BEGIN IMMEDIATE')
  try {
    database.prepare('UPDATE accounts SET priority = priority + 1 WHERE id = ?').run(account.id)
    advanceAccountCircuitDispatchRevisionInSqliteTransaction(database, {
      accountId: account.id,
      accountRuntimeKey: account.id,
      transitionId: 'rollback-dispatch-transition',
      nowMs: Date.now()
    })
    throw new Error('rollback sentinel')
  } catch (error) {
    database.exec('ROLLBACK')
    assert.equal((error as Error).message, 'rollback sentinel')
  }
  const afterRollback = database.prepare('SELECT dispatch_revision, priority FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number; priority: number }
  assert.equal(afterRollback.dispatch_revision, 4, '事务回滚不能留下单边 revision')
  assert.equal((database.prepare("SELECT COUNT(*) AS count FROM account_circuit_outbox WHERE account_id = ? AND event_type = 'dispatch_revision_changed'").get(account.id) as { count: number }).count, 3, '事务回滚不能留下单边 outbox')
} finally {
  closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('account dispatch revision write regression passed')

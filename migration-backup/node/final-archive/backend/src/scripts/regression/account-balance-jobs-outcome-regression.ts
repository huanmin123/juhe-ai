import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  closeAccountBalanceJobsStoreSource,
  createPostgresAccountBalanceJobsStoreSource,
  decodeAccountBalanceJobsOutcome,
  listAccountBalanceJobsOutcomes
} from '../../storage/account-balance-jobs-outcome.repository.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { advanceAccountBalanceProjectionCursorAsync, currentAccountBalanceProjectionCursorAsync } from '../../storage/account-balance-projection-cursor.repository.js'

const fixture = {
  outcome_id: 'account-balance-outcome-1', request_id: 'account-balance-request-1', account_id: 'acct-j2', system_account_id: 'sys-j2',
  input_version: 3, config_revision: 7, trigger: 'periodic', observed_at: '2026-08-19T00:00:00Z',
  expected_next_refresh_at: '2026-08-18T23:55:00Z', next_refresh_at: '2026-08-19T00:00:00Z',
  snapshot: { status: 'fresh', remainingUsd: '1.250000', rawUnit: 'usd', basis: 'wallet' }
}
const outcome = decodeAccountBalanceJobsOutcome(fixture, '2026-08-19T00:00:00.000Z')
assert.equal(outcome.accountId, 'acct-j2')
assert.equal(outcome.expectedNextRefreshAt, '2026-08-18T23:55:00.000Z')
assert.equal(
  decodeAccountBalanceJobsOutcome(fixture, '2026-08-19T00:00:00.123456Z').storageObservedAt,
  '2026-08-19T00:00:00.123456Z',
  'J2 source cursor must retain PostgreSQL microseconds'
)
assert.equal(decodeAccountBalanceJobsOutcome({ ...fixture, adapter: 'custom' }, outcome.storageObservedAt).adapter, 'custom')
assert.throws(() => decodeAccountBalanceJobsOutcome({ ...fixture, trigger: 'bad' }, outcome.storageObservedAt), /trigger 无效/u)
assert.throws(() => decodeAccountBalanceJobsOutcome({ ...fixture, system_account_id: '' }, outcome.storageObservedAt), /system_account_id 无效/u)
assert.throws(() => decodeAccountBalanceJobsOutcome({ ...fixture, secret: 'nope' }, outcome.storageObservedAt), /未知字段/u)

const require = createRequire(import.meta.url)
const DatabaseSync = require('node:sqlite').DatabaseSync as new (path: string) => { exec(sql: string): void; close(): void }
const tempDir = mkdtempSync(join(tmpdir(), 'juhe-ai-j2-outcome-'))
const sqlitePath = join(tempDir, 'jobs.sqlite')
try {
  const database = new DatabaseSync(sqlitePath)
  database.exec('CREATE TABLE account_balance_outcomes (outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL, account_id TEXT NOT NULL, input_version INTEGER NOT NULL, config_revision INTEGER NOT NULL, trigger TEXT NOT NULL, observed_at TEXT NOT NULL, payload TEXT NOT NULL, committed INTEGER NOT NULL)')
  const payload = JSON.stringify(fixture)
  const escaped = payload.replaceAll("'", "''")
  database.exec(`INSERT INTO account_balance_outcomes(outcome_id, request_id, account_id, input_version, config_revision, trigger, observed_at, payload, committed) VALUES ('account-balance-outcome-1', 'account-balance-request-1', 'acct-j2', 3, 7, 'periodic', '2026-08-19T00:00:00.000Z', '${escaped}', 1)`)
  database.exec(`INSERT INTO account_balance_outcomes(outcome_id, request_id, account_id, input_version, config_revision, trigger, observed_at, payload, committed) VALUES ('account-balance-outcome-stale', 'account-balance-request-stale', 'acct-j2', 3, 7, 'periodic', '2026-08-19T00:00:01.000Z', '${escaped}', 0)`)
  database.close()
  const sqliteOutcomes = await listAccountBalanceJobsOutcomes({ mode: 'sqlite', databasePath: sqlitePath }, { limit: 10 })
  assert.equal(sqliteOutcomes.length, 1)
  assert.equal(sqliteOutcomes[0]?.outcomeId, 'account-balance-outcome-1')
  assert.deepEqual((await listAccountBalanceJobsOutcomes({ mode: 'sqlite', databasePath: sqlitePath }, { after: { observedAt: '2026-08-19T00:00:00.000Z', outcomeId: 'account-balance-outcome-1' }, limit: 10 })), [])
  const cursorDatabase = new DatabaseSync(sqlitePath)
  cursorDatabase.exec('CREATE TABLE account_balance_projection_cursors (consumer_key TEXT PRIMARY KEY, observed_at TEXT, outcome_id TEXT, updated_at TEXT NOT NULL)')
  const cursorClient = createSqliteDatabaseClient(cursorDatabase as never)
  assert.equal(await currentAccountBalanceProjectionCursorAsync(cursorClient, 'j2-test'), undefined)
  assert.equal(await advanceAccountBalanceProjectionCursorAsync(cursorClient, 'j2-test', { observedAt: '2026-08-19T00:00:00.000Z', outcomeId: 'account-balance-outcome-1' }), true)
  assert.deepEqual(await currentAccountBalanceProjectionCursorAsync(cursorClient, 'j2-test'), { observedAt: '2026-08-19T00:00:00.000Z', outcomeId: 'account-balance-outcome-1' })
  cursorDatabase.close()
} finally {
  rmSync(tempDir, { recursive: true, force: true })
}

let postgresCursorReadSql = ''
const postgresCursorReader = {
  driver: 'postgres',
  dialect: { qualifyTable: (schema: string, name: string) => `${schema}.${name}` },
  one: async (sql: string) => {
    postgresCursorReadSql = sql
    return { observed_at: '2026-08-19T04:07:26.724123Z', outcome_id: 'account-balance-outcome-precision' }
  }
} as never
assert.deepEqual(
  await currentAccountBalanceProjectionCursorAsync(postgresCursorReader, 'j2-postgres-precision'),
  { observedAt: '2026-08-19T04:07:26.724123Z', outcomeId: 'account-balance-outcome-precision' }
)
assert.match(postgresCursorReadSql, /to_char\(observed_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS\.US"Z"'\)/u)

const postgresCursorWriter: any = {
  driver: 'postgres',
  dialect: { qualifyTable: (schema: string, name: string) => `${schema}.${name}` },
  transaction: async (operation: (client: unknown) => Promise<unknown>) => operation(postgresCursorWriter),
  one: async () => ({ observed_at: '2026-08-19T04:07:26.724455Z', outcome_id: 'account-balance-outcome-f1' }),
  execute: async () => { throw new Error('older microsecond cursor must not write') }
}
assert.equal(
  await advanceAccountBalanceProjectionCursorAsync(postgresCursorWriter, 'j2-postgres-precision', { observedAt: '2026-08-19T04:07:26.724170Z', outcomeId: 'account-balance-outcome-903' }),
  false,
  'a prior outcome in the same millisecond must remain behind the full-precision cursor'
)

let repairedCursorParams: readonly unknown[] | undefined
const legacyMillisecondCursor: any = {
  driver: 'postgres',
  dialect: { qualifyTable: (schema: string, name: string) => `${schema}.${name}` },
  transaction: async (operation: (client: unknown) => Promise<unknown>) => operation(legacyMillisecondCursor),
  one: async () => ({ observed_at: '2026-08-19T04:07:26.724000Z', outcome_id: 'account-balance-outcome-f1' }),
  execute: async (_sql: string, params: readonly unknown[]) => { repairedCursorParams = params; return { changes: 1 } }
}
assert.equal(
  await advanceAccountBalanceProjectionCursorAsync(legacyMillisecondCursor, 'j2-postgres-legacy', { observedAt: '2026-08-19T04:07:26.724170Z', outcomeId: 'account-balance-outcome-903' }),
  true,
  'a legacy millisecond cursor must advance through the first replayed microsecond outcome'
)
assert.equal(repairedCursorParams?.[0], '2026-08-19T04:07:26.724170Z')

// Simulate two replicas draining overlapping source windows. Replica A tries
// to write its older window after replica B has advanced the shared cursor.
let sharedCursor: { observedAt: string; outcomeId: string } | undefined = {
  observedAt: '2026-08-19T04:07:26.724000Z',
  outcomeId: 'account-balance-outcome-replica-base'
}
const contentionClient: any = {
  driver: 'postgres',
  dialect: { qualifyTable: (schema: string, name: string) => `${schema}.${name}` },
  transaction: async (operation: (client: unknown) => Promise<unknown>) => {
    const tx = {
      ...contentionClient,
      one: async () => sharedCursor ? { observed_at: sharedCursor.observedAt, outcome_id: sharedCursor.outcomeId } : undefined,
      execute: async (sql: string, params: readonly unknown[]) => {
        if (sql.startsWith('INSERT')) sharedCursor = { observedAt: String(params[1]), outcomeId: String(params[2]) }
        else if (sql.startsWith('UPDATE')) sharedCursor = { observedAt: String(params[0]), outcomeId: String(params[1]) }
        return { changes: 1 }
      }
    }
    return operation(tx)
  }
}
const replicaAWindow = { observedAt: '2026-08-19T04:07:26.724170Z', outcomeId: 'account-balance-outcome-replica-a' }
const replicaBWindow = { observedAt: '2026-08-19T04:07:26.724455Z', outcomeId: 'account-balance-outcome-replica-b' }
assert.equal(
  await advanceAccountBalanceProjectionCursorAsync(contentionClient, 'j2-double-replica', replicaBWindow),
  true,
  '副本 B 必须能够推进共享游标'
)
assert.equal(await advanceAccountBalanceProjectionCursorAsync(contentionClient, 'j2-double-replica', replicaAWindow), false, '副本 A 看到较新共享游标时必须报告 contention，而不是回退游标')
const runtimeSource = readFileSync(new URL('../../modules/background/account-balance-jobs-outcome-projection-runtime.service.ts', import.meta.url), 'utf8')
assert.match(runtimeSource, /account_balance_jobs_outcome_projection_cursor_contended/u, 'runtime 必须记录双副本 contention')
assert.doesNotMatch(runtimeSource, /J2 projection cursor 未前进/u, '双副本 contention 不得再被抛为失败')

const postgresOutcomeRow = {
  outcome_id: 'account-balance-outcome-1',
  account_id: 'acct-j2',
  input_version: 3,
  config_revision: 7,
  trigger: 'periodic',
  payload: fixture,
  storage_observed_at: '2026-08-19T00:00:00.000001Z'
}
const persistentPostgresSource = createPostgresAccountBalanceJobsStoreSource('postgres://reader@localhost/juhe_ai_test')
let persistentConnects = 0
let persistentEnds = 0
let persistentReleases = 0
const persistentQueries: string[] = []
const persistentClient = {
  query: async (sql: string) => {
    persistentQueries.push(sql)
    if (sql === 'SELECT current_user AS current_user') return { rows: [{ current_user: 'juhe_ai_j2_outcome_reader' }] }
    if (sql.startsWith('SELECT outcome_id')) return { rows: [postgresOutcomeRow] }
    return { rows: [] }
  },
  release: () => { persistentReleases++ }
}
persistentPostgresSource.pool = {
  connect: async () => {
    persistentConnects++
    return persistentClient as never
  },
  end: async () => { persistentEnds++ }
}
assert.equal((await listAccountBalanceJobsOutcomes(persistentPostgresSource, { limit: 1 })).length, 1)
assert.equal((await listAccountBalanceJobsOutcomes(persistentPostgresSource, { limit: 1 })).length, 1)
assert.equal(persistentConnects, 2, '每轮可复用 pool 中的连接，但仍应独立借还 client')
assert.equal(persistentEnds, 0, '成功轮询不得销毁 J2 outcome pool')
assert.equal(persistentReleases, 2, '每轮读取必须归还借出的 client')
assert.equal(persistentQueries.filter((sql) => sql === 'SELECT current_user AS current_user').length, 1, '每个新 pool 必须核验一次 J2 专用 reader 角色')
await closeAccountBalanceJobsStoreSource(persistentPostgresSource)
assert.equal(persistentEnds, 1, 'runtime 停止时必须关闭 J2 outcome pool')

const wrongRoleSource = createPostgresAccountBalanceJobsStoreSource('postgres://wrong-role@localhost/juhe_ai_test')
let wrongRoleEnds = 0
const wrongRoleClient = {
  query: async (sql: string) => sql === 'SELECT current_user AS current_user'
    ? { rows: [{ current_user: 'juhe_ai_j1_test_output' }] }
    : { rows: [] },
  release: () => undefined
}
wrongRoleSource.pool = {
  connect: async () => wrongRoleClient as never,
  end: async () => { wrongRoleEnds++ }
}
await assert.rejects(
  () => listAccountBalanceJobsOutcomes(wrongRoleSource, { limit: 1 }),
  /J2 outcome reader 必须使用专用数据库角色/u
)
assert.equal(wrongRoleEnds, 1, 'reader 角色误配时必须丢弃连接池，不能复用越权连接')
console.log('account balance jobs outcome regression passed')

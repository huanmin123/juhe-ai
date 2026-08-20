import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { convertQuestionPlaceholdersToPostgres, createPostgresDatabaseClient, createSqliteDatabaseClient, databaseTransactionDefinitelyRolledBack, postgresDialect, sqliteDialect } from '../../storage/database-client.js'
import type { PostgresPoolClient, PostgresQueryResult } from '../../storage/postgres-client.js'

async function testSqliteDatabaseClient(): Promise<void> {
  const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-dbclient-'))
  const database = new DatabaseSync(join(tempRoot, 'test.sqlite3'))
  try {
    const client = createSqliteDatabaseClient(database)
    assert.equal(client.driver, 'sqlite')
    assert.equal(client.dialect, sqliteDialect)
    assert.equal(client.dialect.placeholders(3), '?, ?, ?')
    assert.equal(client.dialect.bindPlaceholders(3), '?, ?, ?')

    await client.execute(`
      CREATE TABLE items (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        enabled INTEGER NOT NULL DEFAULT 0
      )
    `)
    await client.execute('INSERT INTO items (id, name, enabled) VALUES (?, ?, ?)', ['item_1', 'one', true])
    await client.execute(`
      INSERT INTO items (id, name, enabled) VALUES (?, ?, ?)
      ON CONFLICT(id) DO UPDATE SET name = excluded.name, enabled = excluded.enabled
    `, ['item_1', 'updated', false])

    const row = await client.one<{ id: string; name: string; enabled: number }>('SELECT id, name, enabled FROM items WHERE id = ?', ['item_1'])
    assert.equal(row?.id, 'item_1')
    assert.equal(row?.name, 'updated')
    assert.equal(row?.enabled, 0)

    await assert.rejects(
      client.transaction(async (tx) => {
        await tx.execute('INSERT INTO items (id, name) VALUES (?, ?)', ['rolled_back', 'rolled back'])
        throw new Error('rollback')
      }),
      /rollback/
    )
    assert.equal(await client.one('SELECT id FROM items WHERE id = ?', ['rolled_back']), undefined)

    await client.transaction(async (tx) => {
      await tx.execute('INSERT INTO items (id, name) VALUES (?, ?)', ['committed', 'committed'])
    })
    const committed = await client.one<{ id: string }>('SELECT id FROM items WHERE id = ?', ['committed'])
    assert.equal(committed?.id, 'committed')
  } finally {
    database.close()
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function testPostgresDialect(): Promise<void> {
  assert.equal(postgresDialect.placeholder(2), '$2')
  assert.equal(postgresDialect.placeholders(3, 2), '$2, $3, $4')
  assert.equal(postgresDialect.bindPlaceholders(3), '?, ?, ?')
  assert.equal(postgresDialect.qualifyTable('juhe_business', 'system_accounts'), '"juhe_business"."system_accounts"')
  assert.equal(
    convertQuestionPlaceholdersToPostgres("SELECT '?' AS literal, col FROM t WHERE a = ? AND b = ?"),
    "SELECT '?' AS literal, col FROM t WHERE a = $1 AND b = $2"
  )
  assert.equal(
    convertQuestionPlaceholdersToPostgres("SELECT ? -- ? stays comment\nFROM t WHERE note = '-- ?' AND b = ? /* ? stays block */"),
    "SELECT $1 -- ? stays comment\nFROM t WHERE note = '-- ?' AND b = $2 /* ? stays block */"
  )
  assert.deepEqual(
    postgresDialect.bind('SELECT * FROM demo WHERE a = ? AND b = ?', ['a', 'b']),
    { sql: 'SELECT * FROM demo WHERE a = $1 AND b = $2', params: ['a', 'b'] }
  )
}

async function testPostgresDatabaseClient(): Promise<void> {
  const pool = new FakePostgresPool()
  const client = createPostgresDatabaseClient(pool as unknown as Parameters<typeof createPostgresDatabaseClient>[0])

  pool.nextRows = [{ id: 'sys_admin' }]
  assert.deepEqual(await client.one<{ id: string }>('SELECT id FROM juhe_business.system_accounts WHERE id = ?', ['sys_admin']), { id: 'sys_admin' })
  assert.deepEqual(pool.queries[0], {
    sql: 'SELECT id FROM juhe_business.system_accounts WHERE id = $1',
    params: ['sys_admin']
  })

  pool.nextRowCount = 3
  assert.deepEqual(await client.execute('UPDATE demo SET name = ? WHERE id = ?', ['name', 'id']), { changes: 3 })
  assert.deepEqual(pool.queries[1], {
    sql: 'UPDATE demo SET name = $1 WHERE id = $2',
    params: ['name', 'id']
  })

  pool.nextRows = [{
    created_at: new Date('2026-07-21T04:05:06.789Z'),
    request_count: '10',
    totalBytes: '2048',
    metric_value: '1.5',
    totalCostUsd: '2.75',
    count: '3',
    rank: '4',
    dispatch_revision: '9',
    schema_version: '7',
    scope_id: '123',
    model: '456',
    protocol_version: '1'
  }]
  const normalizedRow = await client.one<Record<string, unknown>>('SELECT numeric fields FROM demo')
  assert.deepEqual(normalizedRow, {
    created_at: '2026-07-21T04:05:06.789Z',
    request_count: 10,
    totalBytes: 2048,
    metric_value: 1.5,
    totalCostUsd: 2.75,
    count: 3,
    rank: 4,
    dispatch_revision: 9,
    schema_version: 7,
    scope_id: '123',
    model: '456',
    protocol_version: '1'
  })

  pool.nextMultiResults = [
    { rows: [], rowCount: null },
    { rows: [], rowCount: 1 },
    { rows: [], rowCount: 2 }
  ]
  assert.deepEqual(await client.execute('SET search_path TO juhe_business, public;\nCREATE TABLE demo_multi (id text);\nCREATE INDEX demo_multi_idx ON demo_multi(id)'), { changes: 3 })

  await client.transaction(async (tx) => {
      await tx.execute('INSERT INTO demo (id, name) VALUES (?, ?)', ['id_1', 'name_1'])
      await Promise.all([
        tx.query('SELECT serial_one FROM demo'),
        tx.query('SELECT serial_two FROM demo'),
        tx.transaction(async (nestedTx) => Promise.all([
          nestedTx.query('SELECT serial_nested_one FROM demo'),
          nestedTx.query('SELECT serial_nested_two FROM demo')
        ]))
      ])
    })
  const committedConnection = assertConnection(pool.connection)
  assert.equal(committedConnection.queries[0]?.sql, 'BEGIN')
  assert.match(committedConnection.queries[1]?.sql ?? '', /^SET LOCAL statement_timeout/)
  assert.equal(committedConnection.queries[2]?.sql, 'INSERT INTO demo (id, name) VALUES ($1, $2)')
  assert.deepEqual(
    committedConnection.queries.slice(3, 7).map((query) => query.sql),
    [
      'SELECT serial_one FROM demo',
      'SELECT serial_two FROM demo',
      'SELECT serial_nested_one FROM demo',
      'SELECT serial_nested_two FROM demo'
    ],
    '同一个 PostgreSQL 事务连接上的 Promise.all / 嵌套事务查询必须按提交顺序串行执行'
  )
  assert.equal(committedConnection.queries[7]?.sql, 'COMMIT')
  assert.equal(committedConnection.maxConcurrentQueries, 1, '事务连接不得并发执行 client.query')
  assert.equal(committedConnection.releaseCount, 1)
  assert.equal(committedConnection.releaseError, undefined)

  pool.connection = undefined
  const operationError = new Error('shared transaction failure')
  await assert.rejects(
    client.transaction(async (tx) => {
      await tx.execute('INSERT INTO demo (id, name) VALUES (?, ?)', ['id_2', 'name_2'])
      throw operationError
    }),
    /shared transaction failure/
  )
  const rolledBackConnection = assertConnection(pool.connection)
  assert.equal(rolledBackConnection.queries[0]?.sql, 'BEGIN')
  assert.match(rolledBackConnection.queries[1]?.sql ?? '', /^SET LOCAL statement_timeout/)
  assert.equal(rolledBackConnection.queries[2]?.sql, 'INSERT INTO demo (id, name) VALUES ($1, $2)')
  assert.equal(rolledBackConnection.queries[3]?.sql, 'ROLLBACK')
  assert.equal(rolledBackConnection.releaseCount, 1)
  assert.equal(databaseTransactionDefinitelyRolledBack(operationError), true, '业务错误且 ROLLBACK 成功时应标记为明确回滚')

  pool.connection = undefined
  pool.nextConnectionQueryError = { sql: 'COMMIT', error: operationError }
  let commitError: unknown
  try {
    await client.transaction(async () => 'commit-outcome-unknown')
  } catch (error) {
    commitError = error
  }
  assert.equal(commitError, operationError)
  assert.equal(databaseTransactionDefinitelyRolledBack(commitError), false, 'COMMIT 已开始时结果不确定，禁止标记为明确回滚')
  assert.equal(assertConnection(pool.connection).queries.at(-1)?.sql, 'ROLLBACK')

  pool.connection = undefined
  let releaseBlockedOperation: (() => void) | undefined
  const blockedOperation = new Promise<void>((resolvePromise) => {
    releaseBlockedOperation = resolvePromise
  })
  const terminatedTransaction = client.transaction(async () => {
    await blockedOperation
    return 'should-not-commit'
  })
  await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
  const terminatedConnection = assertConnection(pool.connection)
  const terminationError = Object.assign(new Error('terminating connection due to idle-in-transaction timeout'), { code: '25P03' })
  const terminationAssertion = assert.rejects(terminatedTransaction, (error: unknown) => error === terminationError)
  terminatedConnection.emit('error', terminationError)
  await terminationAssertion
  releaseBlockedOperation?.()
  assert.equal(terminatedConnection.releaseCount, 1, '事务连接异步 error 必须由事务边界接管并释放，不能升级为进程 uncaughtException')
  assert.equal(terminatedConnection.releaseError, terminationError, '服务端终止的连接必须带 error 释放，禁止回收到 pool')
  assert.equal(terminatedConnection.listenerCount('error'), 0, '事务结束后不得遗留连接 error listener')
}

interface LoggedQuery {
  sql: string
  params: readonly unknown[]
}

class FakePostgresPool {
  queries: LoggedQuery[] = []
  connection?: FakePostgresConnection
  nextRows: Array<Record<string, unknown>> = []
  nextRowCount = 0
  nextMultiResults: PostgresQueryResult[] | undefined
  nextConnectionQueryError: { sql: string; error: Error } | undefined

  async query(sql: string, params: readonly unknown[] = []): Promise<PostgresQueryResult | PostgresQueryResult[]> {
    this.queries.push({ sql, params })
    if (this.nextMultiResults) {
      const results = this.nextMultiResults
      this.nextMultiResults = undefined
      return results
    }
    const rows = this.nextRows
    const rowCount = this.nextRowCount
    this.nextRows = []
    this.nextRowCount = 0
    return { rows, rowCount }
  }

  async connect(): Promise<PostgresPoolClient> {
    this.connection = new FakePostgresConnection(this.nextConnectionQueryError)
    this.nextConnectionQueryError = undefined
    return this.connection
  }
}

class FakePostgresConnection extends EventEmitter implements PostgresPoolClient {
  queries: LoggedQuery[] = []
  releaseCount = 0
  releaseError: Error | undefined
  maxConcurrentQueries = 0
  private activeQueries = 0

  constructor(private readonly queryError?: { sql: string; error: Error }) {
    super()
  }

  async query(sql: string, params: readonly unknown[] = []): Promise<PostgresQueryResult> {
    this.activeQueries += 1
    this.maxConcurrentQueries = Math.max(this.maxConcurrentQueries, this.activeQueries)
    try {
      this.queries.push({ sql, params })
      await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
      if (this.queryError?.sql === sql) throw this.queryError.error
      return { rows: [], rowCount: 1 }
    } finally {
      this.activeQueries -= 1
    }
  }

  release(error?: Error): void {
    this.releaseCount += 1
    this.releaseError = error
  }
}

function assertConnection(connection: FakePostgresConnection | undefined): FakePostgresConnection {
  assert.ok(connection, 'expected fake postgres connection')
  return connection
}

await testSqliteDatabaseClient()
await testPostgresDialect()
await testPostgresDatabaseClient()

console.log('database-client-regression passed')

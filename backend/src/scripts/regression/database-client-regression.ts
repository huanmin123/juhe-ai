import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { convertQuestionPlaceholdersToPostgres, createPostgresDatabaseClient, createSqliteDatabaseClient, postgresDialect, sqliteDialect } from '../../storage/database-client.js'
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
    request_count: '10',
    totalBytes: '2048',
    metric_value: '1.5',
    totalCostUsd: '2.75',
    count: '3',
    rank: '4',
    schema_version: '7',
    scope_id: '123',
    model: '456',
    protocol_version: '1'
  }]
  const normalizedRow = await client.one<Record<string, unknown>>('SELECT numeric fields FROM demo')
  assert.deepEqual(normalizedRow, {
    request_count: 10,
    totalBytes: 2048,
    metric_value: 1.5,
    totalCostUsd: 2.75,
    count: 3,
    rank: 4,
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
    })
  const committedConnection = assertConnection(pool.connection)
  assert.equal(committedConnection.queries[0]?.sql, 'BEGIN')
  assert.match(committedConnection.queries[1]?.sql ?? '', /^SET LOCAL statement_timeout/)
  assert.equal(committedConnection.queries[2]?.sql, 'INSERT INTO demo (id, name) VALUES ($1, $2)')
  assert.equal(committedConnection.queries[3]?.sql, 'COMMIT')
  assert.equal(committedConnection.releaseCount, 1)

  pool.connection = undefined
  await assert.rejects(
    client.transaction(async (tx) => {
      await tx.execute('INSERT INTO demo (id, name) VALUES (?, ?)', ['id_2', 'name_2'])
      throw new Error('tx failed')
    }),
    /tx failed/
  )
  const rolledBackConnection = assertConnection(pool.connection)
  assert.equal(rolledBackConnection.queries[0]?.sql, 'BEGIN')
  assert.match(rolledBackConnection.queries[1]?.sql ?? '', /^SET LOCAL statement_timeout/)
  assert.equal(rolledBackConnection.queries[2]?.sql, 'INSERT INTO demo (id, name) VALUES ($1, $2)')
  assert.equal(rolledBackConnection.queries[3]?.sql, 'ROLLBACK')
  assert.equal(rolledBackConnection.releaseCount, 1)
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
    this.connection = new FakePostgresConnection()
    return this.connection
  }
}

class FakePostgresConnection implements PostgresPoolClient {
  queries: LoggedQuery[] = []
  releaseCount = 0

  async query(sql: string, params: readonly unknown[] = []): Promise<PostgresQueryResult> {
    this.queries.push({ sql, params })
    return { rows: [], rowCount: 1 }
  }

  release(): void {
    this.releaseCount += 1
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

import assert from 'node:assert/strict'

import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import type { PostgresPoolClient, PostgresQueryResult } from '../../storage/postgres-client.js'

class RecordingConnection implements PostgresPoolClient {
  readonly queries: string[] = []
  released = false

  async query(sql: string): Promise<PostgresQueryResult> {
    this.queries.push(sql)
    return { rows: [{ value: 'connection' }], rowCount: 1 }
  }

  release(): void {
    this.released = true
  }
}

class RecordingPool {
  readonly queries: string[] = []
  readonly connections: RecordingConnection[] = []

  async query(sql: string): Promise<PostgresQueryResult> {
    this.queries.push(sql)
    return { rows: [{ value: 'pool' }], rowCount: 1 }
  }

  async connect(): Promise<PostgresPoolClient> {
    const connection = new RecordingConnection()
    this.connections.push(connection)
    return connection
  }
}

const pool = new RecordingPool()
const client = createPostgresDatabaseClient(pool)

assert.equal((await client.one<{ value: string }>('SELECT 1'))?.value, 'pool')
assert.deepEqual(await client.execute('UPDATE demo SET value = 1'), { changes: 1 })
assert.equal(pool.connections.length, 0, '普通 query/one/execute 不得借独占连接或开启隐式事务')
assert.deepEqual(pool.queries, ['SELECT 1', 'UPDATE demo SET value = 1'])

await client.transaction(async (tx) => {
  await tx.execute('UPDATE demo SET value = 2')
})
assert.equal(pool.connections.length, 1, '显式事务必须借独占连接')
const transactionQueries = pool.connections[0]?.queries ?? []
assert.equal(transactionQueries.length, 4)
assert.equal(transactionQueries[0], 'BEGIN')
assert.match(transactionQueries[1] ?? '', /^SET LOCAL statement_timeout/)
assert.match(transactionQueries[1] ?? '', /SET LOCAL lock_timeout/)
assert.match(transactionQueries[1] ?? '', /SET LOCAL idle_in_transaction_session_timeout/)
assert.equal(transactionQueries[2], 'UPDATE demo SET value = 2')
assert.equal(transactionQueries[3], 'COMMIT')
assert.equal(pool.connections[0]?.released, true)

console.log('POSTGRES_QUERY_TRANSACTION_BOUNDARY_OK')

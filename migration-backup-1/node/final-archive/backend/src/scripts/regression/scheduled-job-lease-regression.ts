import assert from 'node:assert/strict'

import { createPostgresDatabaseClient, postgresDialect, sqliteDialect, type DatabaseClient, type ExecuteResult } from '../../storage/database-client.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import {
  assertScheduledJobLease,
  releaseScheduledJobLease,
  renewScheduledJobLease,
  scheduledJobLeaseAdvisoryKey,
  scheduledJobLeaseKey,
  ScheduledJobLeaseLostError,
  tryAcquireScheduledJobLease,
  type ScheduledJobLeaseIdentity
} from '../../storage/scheduled-job-lease.repository.js'

async function main(): Promise<void> {
const leaseKey = scheduledJobLeaseKey('hourly-account-health', 'shard-3')
assert.equal(leaseKey, 'scheduled:hourly-account-health:shard-3')
assert.equal(scheduledJobLeaseKey('hourly-account-health', 'shard-3'), leaseKey)
assert.notEqual(scheduledJobLeaseAdvisoryKey(leaseKey), scheduledJobLeaseAdvisoryKey(`${leaseKey}:other`))
assert.match(scheduledJobLeaseAdvisoryKey(leaseKey), /^-?\d+$/)

const advisoryBusyClient = new FakeScheduledJobLeaseClient()
advisoryBusyClient.advisoryResults.push(false)
assert.deepEqual(await tryAcquireScheduledJobLease({
  jobName: 'hourly-account-health',
  shardKey: 'shard-3',
  ownerId: 'worker-busy',
  ttlMs: 60_000
}, advisoryBusyClient), { acquired: false, reason: 'advisory_busy', leaseKey })
assert.equal(advisoryBusyClient.insertCount, 0, 'advisory lock 未取得时不得写租约行')

const heldClient = new FakeScheduledJobLeaseClient()
heldClient.advisoryResults.push(true)
heldClient.acquireRows.push(undefined)
assert.deepEqual(await tryAcquireScheduledJobLease({
  jobName: 'hourly-account-health',
  shardKey: 'shard-3',
  ownerId: 'worker-held',
  ttlMs: 60_000
}, heldClient), { acquired: false, reason: 'lease_held', leaseKey })

const acquiredClient = new FakeScheduledJobLeaseClient()
acquiredClient.advisoryResults.push(true)
acquiredClient.acquireRows.push({
  lease_key: leaseKey,
  owner_id: 'worker-a',
  fencing_token: '41',
  lease_until: '2026-07-26T05:00:00.000Z'
})
const acquired = await tryAcquireScheduledJobLease({
  jobName: 'hourly-account-health',
  shardKey: 'shard-3',
  ownerId: 'worker-a',
  runId: 'run-a',
  ttlMs: 60_000
}, acquiredClient)
assert.equal(acquired.acquired, true)
assert.equal(acquired.acquired && acquired.lease.fencingToken, '41')
assert.equal(acquiredClient.transactionCount, 1, 'claim 必须在短事务内完成')
assert.match(acquiredClient.sql.join('\n'), /pg_try_advisory_xact_lock/)
assert.doesNotMatch(acquiredClient.sql.join('\n'), /pg_try_advisory_lock\s*\(/)
assert.match(acquiredClient.sql.join('\n'), /fencing_token = current\.fencing_token \+ 1/)

const lease = assertAcquiredLease(acquired)
acquiredClient.renewRows.push({
  lease_key: lease.leaseKey,
  owner_id: lease.ownerId,
  fencing_token: lease.fencingToken,
  lease_until: '2026-07-26T05:01:00.000Z'
})
const renewed = await renewScheduledJobLease(lease, 60_000, acquiredClient)
assert.equal(renewed?.leaseUntil, '2026-07-26T05:01:00.000Z')
assert.equal(acquiredClient.transactionCount, 2, 'renew 必须在带本地查询超时的短事务内完成')
await assertPostgresLeaseTransactionTimeoutBoundary(lease)

acquiredClient.releaseChanges.push(1)
assert.equal(await releaseScheduledJobLease(lease, acquiredClient), true)
assert.equal(acquiredClient.transactionCount, 3, 'release 必须在带本地查询超时的短事务内完成')
assert.match(acquiredClient.executeSql.at(-1) ?? '', /^\s*UPDATE\b/)
assert.doesNotMatch(acquiredClient.executeSql.at(-1) ?? '', /\bDELETE\b/)

acquiredClient.assertRows.push({ lease_key: lease.leaseKey })
await assertScheduledJobLease(acquiredClient, lease)
assert.equal(acquiredClient.transactionCount, 4, 'assert 必须在带本地查询超时的短事务内完成')
acquiredClient.assertRows.push(undefined)
await assert.rejects(
  assertScheduledJobLease(acquiredClient, lease),
  (error: unknown) => error instanceof ScheduledJobLeaseLostError && error.lease === lease
)
assert.equal(acquiredClient.transactionCount, 5, 'assert 失败也必须由短事务负责回滚并释放连接')

await assert.rejects(
  tryAcquireScheduledJobLease({
    jobName: 'hourly-account-health',
    ownerId: 'worker-invalid-ttl',
    ttlMs: 99
  }, acquiredClient),
  /ttlMs/
)

const sqliteClient = new FakeScheduledJobLeaseClient('sqlite')
await assert.rejects(
  tryAcquireScheduledJobLease({
    jobName: 'hourly-account-health',
    ownerId: 'sqlite-worker',
    ttlMs: 60_000
  }, sqliteClient),
  /只支持 PostgreSQL/
)

const schemaStatements = collectPostgresSchemaStatements()
const freshLeaseTable = schemaStatements.find((statement) => (
  statement.schemaName === 'juhe_stats'
  && /^CREATE TABLE IF NOT EXISTS background_job_leases\b/i.test(statement.sql)
))?.sql ?? ''
assert.match(freshLeaseTable, /fencing_token bigint NOT NULL DEFAULT 0/)
console.log('scheduled-job-lease-regression passed')
}

interface FakeLeaseRow {
  lease_key: string
  owner_id: string
  fencing_token: string | number | bigint
  lease_until: string
}

class FakeScheduledJobLeaseClient implements DatabaseClient {
  readonly driver: 'sqlite' | 'postgres'
  readonly dialect
  readonly sql: string[] = []
  readonly executeSql: string[] = []
  readonly advisoryResults: boolean[] = []
  readonly acquireRows: Array<FakeLeaseRow | undefined> = []
  readonly renewRows: Array<FakeLeaseRow | undefined> = []
  readonly assertRows: Array<{ lease_key: string } | undefined> = []
  readonly releaseChanges: number[] = []
  transactionCount = 0
  transactionDepth = 0
  insertCount = 0

  constructor(driver: 'sqlite' | 'postgres' = 'postgres') {
    this.driver = driver
    this.dialect = driver === 'postgres' ? postgresDialect : sqliteDialect
  }

  async query<T extends object = Record<string, unknown>>(): Promise<T[]> {
    throw new Error('unexpected query')
  }

  async one<T extends object = Record<string, unknown>>(sql: string): Promise<T | undefined> {
    this.assertLeaseQueryRunsInTransaction(sql)
    this.sql.push(sql)
    if (/pg_try_advisory_xact_lock/.test(sql)) {
      return { acquired: this.advisoryResults.shift() ?? false } as T
    }
    if (/\bINSERT INTO\b/.test(sql)) {
      this.insertCount += 1
      return this.acquireRows.shift() as T | undefined
    }
    if (/\bUPDATE\b/.test(sql) && /\bRETURNING\b/.test(sql)) {
      return this.renewRows.shift() as T | undefined
    }
    if (/\bSELECT lease_key\b/.test(sql)) {
      return this.assertRows.shift() as T | undefined
    }
    throw new Error(`unexpected one SQL: ${sql}`)
  }

  async execute(sql: string): Promise<ExecuteResult> {
    this.assertLeaseQueryRunsInTransaction(sql)
    this.executeSql.push(sql)
    return { changes: this.releaseChanges.shift() ?? 0 }
  }

  async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
    this.transactionCount += 1
    this.transactionDepth += 1
    try {
      return await operation(this)
    } finally {
      this.transactionDepth -= 1
    }
  }

  private assertLeaseQueryRunsInTransaction(sql: string): void {
    if (/background_job_leases|pg_try_advisory_xact_lock/.test(sql)) {
      assert.ok(this.transactionDepth > 0, `租约 SQL 不得绕过短事务：${sql}`)
    }
  }
}

function assertAcquiredLease(
  result: Awaited<ReturnType<typeof tryAcquireScheduledJobLease>>
): ScheduledJobLeaseIdentity {
  assert.equal(result.acquired, true)
  return result.lease
}

async function assertPostgresLeaseTransactionTimeoutBoundary(lease: ScheduledJobLeaseIdentity): Promise<void> {
  const sql: string[] = []
  let released = false
  const connection = {
    async query(text: string) {
      sql.push(text)
      if (/\bUPDATE\b/.test(text) && /\bRETURNING\b/.test(text)) {
        return {
          rows: [{
            lease_key: lease.leaseKey,
            owner_id: lease.ownerId,
            fencing_token: lease.fencingToken,
            lease_until: '2026-07-26T05:02:00.000Z'
          }],
          rowCount: 1
        }
      }
      return { rows: [], rowCount: 0 }
    },
    release() {
      released = true
    }
  }
  const client = createPostgresDatabaseClient({
    query: connection.query,
    async connect() {
      return connection
    }
  })

  const boundedRenewal = await renewScheduledJobLease(lease, 60_000, client)
  assert.equal(boundedRenewal?.leaseUntil, '2026-07-26T05:02:00.000Z')
  assert.equal(sql[0], 'BEGIN')
  assert.match(sql[1] ?? '', /SET LOCAL statement_timeout = \d+/)
  assert.match(sql[1] ?? '', /SET LOCAL lock_timeout = \d+/)
  assert.match(sql[1] ?? '', /SET LOCAL idle_in_transaction_session_timeout = \d+/)
  assert.match(sql[2] ?? '', /^\s*UPDATE\b/)
  assert.equal(sql.at(-1), 'COMMIT')
  assert.equal(released, true, '短事务结束后必须归还 pool connection')
}

await main()

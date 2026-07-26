import { strict as assert } from 'node:assert'

import { postgresDialect, type DatabaseClient, type ExecuteResult } from '../../storage/database-client.js'
import { insertSystemMetricsSampleBatchWithClientAsync } from '../../storage/system-metrics.repository.js'

let transactionCount = 0
const executedSql: string[] = []

const unsupportedQuery = async (): Promise<never> => {
  throw new Error('本回归不应执行读取查询')
}
const transactionClient: DatabaseClient = {
  driver: 'postgres',
  dialect: postgresDialect,
  query: unsupportedQuery,
  one: unsupportedQuery,
  async execute(sql: string): Promise<ExecuteResult> {
    executedSql.push(sql)
    return { changes: 1 }
  },
  async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
    return await operation(transactionClient)
  }
}
const rootClient: DatabaseClient = {
  ...transactionClient,
  async execute(): Promise<ExecuteResult> {
    throw new Error('批量写入不得在事务外执行 SQL')
  },
  async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
    transactionCount += 1
    return await operation(transactionClient)
  }
}

await insertSystemMetricsSampleBatchWithClientAsync(
  rootClient,
  {
    sampledAt: '2026-07-26T12:34:56.789Z',
    cpuPercent: 25,
    memoryUsedPercent: 50
  },
  [
    {
      sampledAt: '2026-07-26T12:34:56.789Z',
      processRole: 'server',
      processPid: 100,
      eventLoopLagMs: 5
    },
    {
      sampledAt: '2026-07-26T12:34:56.789Z',
      processRole: 'stats-worker',
      processPid: 101,
      processRssBytes: 1024
    },
    {
      sampledAt: '2026-07-26T12:34:56.789Z',
      processRole: 'ops-worker'
    }
  ],
  'UTC'
)

assert.equal(transactionCount, 1, '单轮系统指标与全部进程采样必须只开启一个 PostgreSQL 事务')
assert.equal(executedSql.length, 6, '一个系统采样与两个有效进程采样应各写明细和小时桶，空采样应跳过')
assert.equal(executedSql.filter((sql) => /system_metrics_samples/.test(sql)).length, 1, '系统指标明细应只写一次')
assert.equal(executedSql.filter((sql) => /system_metrics_hourly/.test(sql)).length, 1, '系统指标小时桶应只写一次')
assert.equal(executedSql.filter((sql) => /process_event_loop_samples/.test(sql)).length, 2, '有效进程采样明细应在同一事务内写入')
assert.equal(executedSql.filter((sql) => /process_event_loop_hourly/.test(sql)).length, 2, '有效进程小时桶应在同一事务内写入')

console.log('系统指标批量事务回归通过：单轮 PostgreSQL 仅一个短事务，空进程采样保持跳过')

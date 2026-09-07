import { strict as assert } from 'node:assert'

import type { ProcessEventLoopRole } from '../../shared/process-event-loop-monitor.js'
import { postgresDialect, type DatabaseClient, type ExecuteResult } from '../../storage/database-client.js'
import { insertSystemMetricsSampleBatchWithClientAsync } from '../../storage/system-metrics.repository.js'

let transactionCount = 0
const executedSql: string[] = []
const executedStatements: Array<{ sql: string; params: unknown[] }> = []

const unsupportedQuery = async (): Promise<never> => {
  throw new Error('本回归不应执行读取查询')
}
const transactionClient: DatabaseClient = {
  driver: 'postgres',
  dialect: postgresDialect,
  query: unsupportedQuery,
  one: unsupportedQuery,
  async execute(sql: string, params: unknown[] = []): Promise<ExecuteResult> {
    executedSql.push(sql)
    executedStatements.push({ sql, params })
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
const performanceProcessRoles = buildPerformanceProcessRoles(32, 32, 32)

await insertSystemMetricsSampleBatchWithClientAsync(
  rootClient,
  {
    sampledAt: '2026-07-26T12:34:56.789Z',
    cpuPercent: 25,
    memoryUsedPercent: 50
  },
  [
    ...performanceProcessRoles.map((processRole, index) => ({
      sampledAt: '2026-07-26T12:34:56.789Z',
      processRole,
      processPid: 100 + index,
      eventLoopLagMs: 5 + index,
      processRssBytes: 1024 + index
    })),
    {
      sampledAt: '2026-07-26T12:34:56.789Z',
      processRole: 'ops-worker'
    }
  ],
  'UTC'
)

assert.equal(transactionCount, 1, '单轮系统指标与全部进程采样必须只开启一个 PostgreSQL 事务')
assert.equal(performanceProcessRoles.length, 132, '合法最大 performance 拓扑必须按 132 个进程验证')
assert.equal(executedSql.length, 266, '一个系统采样与 132 个有效进程采样应各写明细和小时桶，空采样应跳过')
assert.equal(executedSql.filter((sql) => /system_metrics_samples/.test(sql)).length, 1, '系统指标明细应只写一次')
assert.equal(executedSql.filter((sql) => /system_metrics_hourly/.test(sql)).length, 1, '系统指标小时桶应只写一次')
assert.equal(executedSql.filter((sql) => /process_event_loop_samples/.test(sql)).length, 132, '132 个 performance 进程采样明细应在同一事务内写入')
assert.equal(executedSql.filter((sql) => /process_event_loop_hourly/.test(sql)).length, 132, '132 个 performance 进程小时桶应在同一事务内写入')
assert.deepEqual(
  executedStatements
    .filter(({ sql }) => /INSERT INTO .*process_event_loop_samples/.test(sql))
    .map(({ params }) => params[1]),
  performanceProcessRoles,
  'Gateway、DB service、control 和各 worker 的动态角色必须原样进入持久化参数'
)

console.log('系统指标批量事务回归通过：合法最大 132 个 performance 进程在单个 PostgreSQL 短事务内完整持久化')

function buildPerformanceProcessRoles(gatewayReplicas: number, usageReplicas: number, logReplicas: number): ProcessEventLoopRole[] {
  const roles: ProcessEventLoopRole[] = ['control:control-1', 'db-service:control-1']
  for (let replica = 1; replica <= gatewayReplicas; replica += 1) {
    roles.push(`gateway:gateway-${replica}`, `db-service:gateway-${replica}`)
  }
  for (let replica = 1; replica <= usageReplicas; replica += 1) roles.push(`usage-worker:${replica}`)
  for (let replica = 1; replica <= logReplicas; replica += 1) roles.push(`log-worker:${replica}`)
  roles.push('stats-worker:1', 'ops-worker:1')
  return roles
}

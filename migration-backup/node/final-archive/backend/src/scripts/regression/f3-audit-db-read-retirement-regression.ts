import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function source(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../${relativePath}`, import.meta.url)), 'utf8')
}

const workerSource = source('storage/sqlite-read-worker.ts')
const workerTypesSource = source('storage/sqlite-read-worker-pool.types.ts')
const dbServiceTypesSource = source('modules/db-service/db-service-types.ts')
const dbServiceHandlersSource = source('modules/db-service/db-service-handlers.ts')
const auditRoutesSource = source('modules/audit-logs/audit-logs.routes.ts')

const retiredOperations = [
  'list_audit_logs_read_only',
  'list_audit_logs_by_ids_read_only',
  'list_audit_error_groups_read_only',
  'list_audit_error_group_events_read_only',
  'get_audit_log_detail_read_only',
  'get_audit_log_detail_supplement_read_only',
  'get_audit_log_payload_read_only'
]

for (const [label, text] of [
  ['SQLite read worker', workerSource],
  ['SQLite read worker operation types', workerTypesSource],
  ['db-service protocol types', dbServiceTypesSource],
  ['db-service handlers', dbServiceHandlersSource]
] as const) {
  for (const operation of retiredOperations) {
    assert(!text.includes(operation), `${label} 不得保留已迁出的 F3 审计读取操作：${operation}`)
  }
  assert.doesNotMatch(text, /audit-log-(read|detail-supplement)\.repository/, `${label} 不得加载旧 Node 审计读取器`)
}

assert.doesNotMatch(workerTypesSource, /audit-logs\.repository/, 'SQLite read worker 类型不得从旧 Node 审计 repository 导入类型')
assert.doesNotMatch(auditRoutesSource, /requestSqliteReadWorker|db-service/, 'F3 审计路由不得经 Node DB-service/SQLite read worker 查询')
assert.match(auditRoutesSource, /audit-log-f3-query\.repository/, 'F3 审计路由必须使用独立 Go owner 只读 adapter')

console.log('F3 audit DB read retirement regression passed: Node db-service and SQLite read worker no longer serve audit reads.')

import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function source(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../${relativePath}`, import.meta.url)), 'utf8')
}

const datasetSchema = source('storage/schema/dataset-schema.ts')
const postgresSchema = source('storage/postgres-schema.ts')
const repositories = source('storage/repositories.ts')
const mockdataLogs = source('scripts/maintenance/mockdata/observability/logs.ts')
const f3QueryRepository = source('storage/audit-log-f3-query.repository.ts')

for (const table of [
  'audit_logs',
  'audit_log_attempts',
  'audit_payload_blobs',
  'audit_payload_refs',
  'audit_error_groups'
]) {
  assert.doesNotMatch(datasetSchema, new RegExp(`\\b${table}\\b`, 'i'), `Node SQLite schema 不得创建或迁移 F3 表：${table}`)
  assert.doesNotMatch(postgresSchema, new RegExp(`\\b${table}\\b`, 'i'), `Node PostgreSQL schema 不得创建或迁移 F3 表：${table}`)
}

for (const symbol of [
  'createAuditLogsBatch',
  'cleanupAuditLogsByRetention',
  'getAuditLogDetail',
  'listAuditLogs',
  "'./audit-logs.repository.js'"
]) {
  assert(!repositories.includes(symbol), `聚合 repositories 不得继续导出 Node F3 审计实现：${symbol}`)
}

assert.match(mockdataLogs, /function createAuditMockdata\(_records: UsageRecordSeed\[\]\): number \{\s*return 0\s*\}/, 'Node mockdata 必须显式跳过 F3 审计直写')
assert.match(mockdataLogs, /Go owner|Go 输入端点/, 'Node mockdata 必须说明审计样本由 Go input 契约注入')
assert.doesNotMatch(mockdataLogs, /createAuditLogsBatch|audit-logs\.repository|getDatasetDatabase/, 'Node mockdata 不得调用旧审计 writer 或数据集 DB')

assert.doesNotMatch(f3QueryRepository, /audit-logs\.repository|repositories\.js|getDatasetDatabase|applyDatasetSchema/, 'F3 只读 adapter 不得依赖 Node 审计 schema/writer')

console.log('F3 Node 审计 schema 退出回归通过：Node 不再建表、迁移、导出 writer 或由 mockdata 直写；读取仅通过独立 F3 adapter。')

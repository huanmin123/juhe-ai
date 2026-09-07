import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function source(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../${relativePath}`, import.meta.url)), 'utf8')
}

function excludesF3AuditOwnerSyntax(text: string, label: string): void {
  for (const token of [
    'CREATE TABLE IF NOT EXISTS audit_logs',
    'CREATE TABLE IF NOT EXISTS audit_log_attempts',
    'CREATE TABLE IF NOT EXISTS audit_payload_blobs',
    'CREATE TABLE IF NOT EXISTS audit_payload_refs',
    'CREATE TABLE IF NOT EXISTS audit_error_groups',
    'ALTER TABLE audit_logs',
    'ALTER TABLE audit_payload_refs',
    'CREATE INDEX IF NOT EXISTS idx_audit_',
    'createAuditLogsBatch',
    'cleanupAuditLogs',
    'cleanupAuditSuccessHotRetention',
    'cleanupUnreferencedAuditPayloadBlobs'
  ]) {
    assert(!text.includes(token), `${label} 仍保留 F3 Node owner 语义：${token}`)
  }
}

const datasetSchema = source('storage/schema/dataset-schema.ts')
const postgresSchema = source('storage/postgres-schema.ts')
const repositories = source('storage/repositories.ts')
const mockdataLogs = source('scripts/maintenance/mockdata/observability/logs.ts')

excludesF3AuditOwnerSyntax(datasetSchema, 'dataset SQLite schema')
excludesF3AuditOwnerSyntax(postgresSchema, 'PostgreSQL schema bootstrap')
excludesF3AuditOwnerSyntax(repositories, 'repositories barrel')
excludesF3AuditOwnerSyntax(mockdataLogs, 'mockdata observability logs')

assert.match(mockdataLogs, /F3 审计日志由 Go owner 接收并持久化/, 'mockdata 必须明确 F3 的 Go owner')
assert.match(mockdataLogs, /export function createAuditMockdata\(_records: UsageRecordSeed\[\]\): number \{\s+return 0\s+\}/, 'Node mockdata 不得再伪造或写入 F3 审计记录')

console.log('F3 Node audit owner exit regression passed: Node schema/bootstrap/barrel/mockdata no longer owns audit persistence or retention.')

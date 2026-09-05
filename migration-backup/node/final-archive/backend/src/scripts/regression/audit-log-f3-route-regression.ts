import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const routeSource = readFileSync(fileURLToPath(new URL('../../modules/audit-logs/audit-logs.routes.ts', import.meta.url)), 'utf8')
const runtimeSource = readFileSync(fileURLToPath(new URL('../../config/runtime.ts', import.meta.url)), 'utf8')

assert.match(routeSource, /createAuditLogF3QueryRepository/, 'audit routes must use the F3 query factory')
assert.match(routeSource, /getF3Repository\(\)[\s\S]{0,160}\.listAuditLogs/, 'list route must use F3 repository')
assert.match(routeSource, /getF3Repository\(\)[\s\S]{0,160}\.searchHot/, 'hot-search route must use F3 repository')
assert.match(routeSource, /getF3Repository\(\)[\s\S]{0,160}\.listAuditErrorGroups/, 'error-group route must use F3 repository')
assert.match(routeSource, /getF3Repository\(\)[\s\S]{0,160}\.listAuditErrorGroupEvents/, 'error-group events route must use F3 repository')
assert.match(routeSource, /getF3Repository\(\)[\s\S]{0,160}\.getAuditLogDetail/, 'detail route must use F3 repository')
assert.match(routeSource, /getF3Repository\(\)[\s\S]{0,160}\.getAuditLogPayload/, 'payload route must use F3 repository')
assert.doesNotMatch(routeSource, /repositories\.js|audit-log-hot-search-files|requestServerRuntimeSnapshot|readAuditLogSettings|audit-log-queue/, 'audit read routes must not import Node dataset/queue readers')
assert.match(routeSource, /getRuntime\(\)/, 'runtime route must expose F3 read-only runtime fields')
assert.match(runtimeSource, /defaultAuditLogF3DatabasePath/, 'runtime must define the dedicated F3 SQLite default')
assert.match(runtimeSource, /JUHE_AI_AUDIT_LOG_POSTGRES_SCHEMA/, 'runtime must expose the F3 PostgreSQL schema')
assert.match(runtimeSource, /JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY/, 'runtime must expose the dedicated F3 blob root')
assert.match(runtimeSource, /JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY/, 'runtime must expose the dedicated F3 hot-search root')

console.log('F3 Node audit read-route regression passed')

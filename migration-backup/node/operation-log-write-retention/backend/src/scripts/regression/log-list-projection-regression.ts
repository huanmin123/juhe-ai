import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const operationRead = readFileSync(resolve('src/storage/operation-log-read.repository.ts'), 'utf8')
const publicRead = readFileSync(resolve('src/storage/public-api-logs.repository.ts'), 'utf8')

assert.match(operationRead, /const operationLogDefaultPageSize = 20/)
assert.match(operationRead, /const operationLogMaxPageSize = 50/)
assert.match(publicRead, /const publicApiLogDefaultPageSize = 50/)
assert.match(publicRead, /const publicApiLogMaxPageSize = 100/)

const operationListSelect = operationRead.match(/function operationLogListSelectColumns\(alias: string\): string \{([\s\S]*?)\n\}/)?.[1] ?? ''
const publicListSelect = publicRead.match(/function publicApiLogListSelectColumns\(alias: string\): string \{([\s\S]*?)\n\}/)?.[1] ?? ''
assert(operationListSelect, 'operation log list projection must be explicit')
assert(publicListSelect, 'public API log list projection must be explicit')
for (const projection of [operationListSelect, publicListSelect]) {
  assert.doesNotMatch(projection, /\*|changes_json|metadata_json|request_data_json|response_data_json/,
    'log list projection must not select detail payload columns')
}
assert.match(operationRead, /SELECT ol\.\* FROM operation_logs ol WHERE/,
  'operation log detail must retain full payload lookup by ID')
assert.match(publicRead, /SELECT \* FROM public_api_logs WHERE id = \?/,
  'public API log internal full detail lookup must remain compatible')
const publicDetailSupplementSelect = publicRead.match(/function publicApiLogDetailSupplementSelectColumns\(alias: string\): string \{([\s\S]*?)\n\}/)?.[1] ?? ''
assert(publicDetailSupplementSelect, 'public API log management detail supplement projection must be explicit')
for (const duplicate of ['id', 'created_at', 'source_name', 'method', 'path', 'success', 'status_code', 'duration_ms', 'client_ip', 'trace_id']) {
  assert.doesNotMatch(publicDetailSupplementSelect, new RegExp(`['\"]${duplicate}['\"]`),
    `public API log detail supplement must not repeat list column ${duplicate}`)
}
assert.match(publicRead, /type: 'get_public_api_log_detail_supplement_read_only'/,
  'public API log detail supplement must have a dedicated SQLite read-worker operation')

console.log('operation/public API log list projection regression passed')

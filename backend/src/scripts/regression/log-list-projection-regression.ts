import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const operationRead = readFileSync(resolve('src/storage/operation-log-read.repository.ts'), 'utf8')
const publicRead = readFileSync(resolve('src/storage/public-api-logs.repository.ts'), 'utf8')

assert.match(operationRead, /const operationLogDefaultPageSize = 20/)
assert.match(operationRead, /const operationLogMaxPageSize = 50/)
assert.match(publicRead, /const publicApiLogDefaultPageSize = 50/)
assert.match(publicRead, /const publicApiLogMaxPageSize = 50/)

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
  'public API log detail must retain full payload lookup by ID')

console.log('operation/public API log list projection regression passed')

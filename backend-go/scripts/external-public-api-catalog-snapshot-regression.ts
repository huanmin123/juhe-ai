import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  getExternalPublicApiCatalog,
  type ExternalPublicApiDocItem,
  type ExternalPublicApiField
} from '../../backend/src/modules/external-integrations/external-public-api-catalog.js'
import { EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH } from './generate-external-public-api-catalog.js'

const expectedEndpoints = [
  { id: 'api-key-list', method: 'GET', path: '/__aipublic__/api-key/list', scope: 'juhe_ai_public:api_key_list:read' },
  { id: 'api-key-add', method: 'POST', path: '/__aipublic__/api-key/add', scope: 'juhe_ai_public:api_key_add:write' },
  { id: 'api-key-update', method: 'POST', path: '/__aipublic__/api-key/update', scope: 'juhe_ai_public:api_key_update:write' },
  { id: 'api-key-delete', method: 'POST', path: '/__aipublic__/api-key/del', scope: 'juhe_ai_public:api_key_delete:write' },
  { id: 'route-strategy-list', method: 'GET', path: '/__aipublic__/route-strategy/list', scope: 'juhe_ai_public:route_strategy_list:read' },
  { id: 'route-strategy-add', method: 'POST', path: '/__aipublic__/route-strategy/add', scope: 'juhe_ai_public:route_strategy_add:write' },
  { id: 'route-strategy-update', method: 'POST', path: '/__aipublic__/route-strategy/update', scope: 'juhe_ai_public:route_strategy_update:write' },
  { id: 'route-strategy-delete', method: 'POST', path: '/__aipublic__/route-strategy/del', scope: 'juhe_ai_public:route_strategy_delete:write' },
  { id: 'group-list', method: 'GET', path: '/__aipublic__/group/list', scope: 'juhe_ai_public:group_list:read' },
  { id: 'group-add', method: 'POST', path: '/__aipublic__/group/add', scope: 'juhe_ai_public:group_add:write' },
  { id: 'group-update', method: 'POST', path: '/__aipublic__/group/update', scope: 'juhe_ai_public:group_update:write' },
  { id: 'group-delete', method: 'POST', path: '/__aipublic__/group/del', scope: 'juhe_ai_public:group_delete:write' },
  { id: 'account-list', method: 'GET', path: '/__aipublic__/account/list', scope: 'juhe_ai_public:account_list:read' },
  { id: 'account-add', method: 'POST', path: '/__aipublic__/account/add', scope: 'juhe_ai_public:account_add:write' },
  { id: 'account-update', method: 'POST', path: '/__aipublic__/account/update', scope: 'juhe_ai_public:account_update:write' },
  { id: 'account-delete', method: 'POST', path: '/__aipublic__/account/del', scope: 'juhe_ai_public:account_delete:write' }
] as const

const currentCatalog = getExternalPublicApiCatalog()
const committedSnapshotText = readFileSync(EXTERNAL_PUBLIC_API_CATALOG_SNAPSHOT_PATH, 'utf8')
const committedCatalog: unknown = JSON.parse(committedSnapshotText)
const currentSnapshotText = `${JSON.stringify(currentCatalog, null, 2)}\n`
const currentSerializableCatalog: unknown = JSON.parse(currentSnapshotText)

assert.equal(
  committedSnapshotText,
  currentSnapshotText,
  'committed external public API catalog snapshot must match deterministic Node serialization'
)
assert.deepEqual(
  committedCatalog,
  currentSerializableCatalog,
  'committed external public API catalog snapshot must match the current Node structured catalog'
)

assert.equal(currentCatalog.basePath, '/__aipublic__', 'external public API basePath must remain fixed')
assert.equal(currentCatalog.authType, 'Bearer', 'external public API authType must remain Bearer')
assert.equal(currentCatalog.items.length, 16, 'external public API catalog must contain 16 items')
assert.deepEqual(
  currentCatalog.items.map(({ id, method, path, scope }) => ({ id, method, path, scope })),
  expectedEndpoints,
  'external public API endpoint ID, method, path, and scope metadata must remain complete'
)

assertUnique(currentCatalog.items.map((item) => item.id), 'endpoint IDs')
assertUnique(currentCatalog.items.map((item) => item.path), 'endpoint paths')
assertUnique(currentCatalog.items.map((item) => `${item.method} ${item.path}`), 'endpoint method/path pairs')
assertUnique(currentCatalog.items.map((item) => item.scope ?? ''), 'endpoint scopes')

for (const item of currentCatalog.items) {
  assert(item.name.trim().length > 0, `${item.id} must retain its display name`)
  assert(item.summary.trim().length > 0, `${item.id} must retain its summary`)
  assert.equal(item.status, 'available', `${item.id} must retain its availability status`)
  assert(item.path.startsWith(`${currentCatalog.basePath}/`), `${item.id} path must stay under basePath`)
  assert(item.scope && item.scope.trim().length > 0, `${item.id} must retain a non-empty scope`)
  assert(item.headers.length > 0, `${item.id} must retain request header documentation`)
  assert(Array.isArray(item.query), `${item.id} must retain query field documentation`)
  assert(item.responseFields.length > 0, `${item.id} must retain response field documentation`)

  for (const header of item.headers) {
    assert(header.name.trim().length > 0, `${item.id} contains a header without a name`)
    assert.equal(typeof header.required, 'boolean', `${item.id} header required flag must be retained`)
    assert(header.description.trim().length > 0, `${item.id} header description must be retained`)
    assert(header.example.trim().length > 0, `${item.id} header example must be retained`)
  }
  for (const field of [...item.query, ...item.responseFields]) {
    assertRichField(item.id, field)
  }

  const responseExample = asRecord(item.responseExample, `${item.id} responseExample`)
  asRecord(responseExample.data, `${item.id} responseExample.data`)

  if (item.method === 'GET') {
    assert(item.query.length > 0, `${item.id} GET documentation must retain query fields`)
    assert.equal(item.requestBody, undefined, `${item.id} GET documentation must not gain a request body`)
    continue
  }

  assert.equal(item.method, 'POST', `${item.id} must use a supported documented method`)
  assert(item.requestBody, `${item.id} POST documentation must retain its request body`)
  assert.equal(item.requestBody.contentType, 'application/json', `${item.id} request content type must remain JSON`)
  assert(item.requestBody.fields.length > 0, `${item.id} request body fields must not be empty`)
  for (const field of item.requestBody.fields) {
    assertRichField(item.id, field)
  }
  asRecord(item.requestBody.example, `${item.id} requestBody.example`)
}

const accountAdd = findItem('account-add')
assertFieldNames(
  accountAdd.requestBody?.fields ?? [],
  ['providerCode', 'providerProtocolProfileId', 'baseUrl', 'apiKey', 'supportedModels', 'concurrencyLimit', 'priority', 'availabilitySchedule'],
  'account-add request body'
)
const accountAddExample = asRecord(accountAdd.requestBody?.example, 'account-add requestBody.example')
assert.equal(accountAddExample.providerProtocolProfileId, 'profile_gpt_openai_v1')
assert.equal(accountAddExample.baseUrl, 'https://api.openai.com/v1')

const accountList = findItem('account-list')
assertFieldNames(
  accountList.responseFields,
  [
    'data.items[].providerProtocolProfileId',
    'data.items[].protocolCode',
    'data.items[].clientCompatibility',
    'data.items[].supportedModels',
    'data.items[].concurrencyLimit',
    'data.items[].priority'
  ],
  'account-list response fields'
)
const accountListData = asRecord(asRecord(accountList.responseExample, 'account-list responseExample').data, 'account-list responseExample.data')
assert(Array.isArray(accountListData.items) && accountListData.items.length > 0, 'account-list response example must retain account items')
const accountExample = asRecord(accountListData.items[0], 'account-list responseExample.data.items[0]')
assert.equal(accountExample.providerProtocolProfileId, 'profile_gpt_openai_v1')
assert.deepEqual(accountExample.supportedModels, ['gpt-5.5'])

const routeStrategyAdd = findItem('route-strategy-add')
assertFieldNames(routeStrategyAdd.requestBody?.fields ?? [], ['groupBindings', 'hybridRoutingConfig'], 'route-strategy-add request body')
const routeStrategyExample = asRecord(routeStrategyAdd.requestBody?.example, 'route-strategy-add requestBody.example')
assert(Array.isArray(routeStrategyExample.groupBindings) && routeStrategyExample.groupBindings.length > 0, 'route-strategy-add example must retain group bindings')

const apiKeyAdd = findItem('api-key-add')
assertFieldNames(apiKeyAdd.responseFields, ['data.apiKey.key', 'data.apiKey.routeStrategyId'], 'api-key-add response fields')
const apiKeyAddData = asRecord(asRecord(apiKeyAdd.responseExample, 'api-key-add responseExample').data, 'api-key-add responseExample.data')
const apiKeyExample = asRecord(apiKeyAddData.apiKey, 'api-key-add responseExample.data.apiKey')
assert.equal(apiKeyExample.key, 'juis_xxx_plain_once')

console.log('external public API catalog snapshot regression passed')

function findItem(id: string): ExternalPublicApiDocItem {
  const item = currentCatalog.items.find((candidate) => candidate.id === id)
  assert(item, `external public API catalog is missing ${id}`)
  return item
}

function assertUnique(values: string[], label: string): void {
  assert.equal(new Set(values).size, values.length, `${label} must be unique`)
  assert(values.every((value) => value.trim().length > 0), `${label} must not contain empty values`)
}

function assertRichField(itemId: string, field: ExternalPublicApiField): void {
  assert(field.name.trim().length > 0, `${itemId} contains a field without a name`)
  assert(field.type.trim().length > 0, `${itemId} field ${field.name} must retain its type`)
  assert.equal(typeof field.required, 'boolean', `${itemId} field ${field.name} must retain its required flag`)
  assert(field.description.trim().length > 0, `${itemId} field ${field.name} must retain its description`)
}

function assertFieldNames(fields: ExternalPublicApiField[], expectedNames: string[], label: string): void {
  const fieldNames = new Set(fields.map((field) => field.name))
  for (const expectedName of expectedNames) {
    assert(fieldNames.has(expectedName), `${label} must retain ${expectedName}`)
  }
}

function asRecord(value: unknown, label: string): Record<string, unknown> {
  assert(value !== null && typeof value === 'object' && !Array.isArray(value), `${label} must remain an object`)
  return value as Record<string, unknown>
}

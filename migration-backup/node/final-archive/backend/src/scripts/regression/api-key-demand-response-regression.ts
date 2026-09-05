import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const repositorySource = readFileSync(resolve('src/storage/api-key.repository.ts'), 'utf8')
const routesSource = readFileSync(resolve('src/modules/api-keys/api-keys.routes.ts'), 'utf8')
const frontendTypesSource = readFileSync(resolve('../frontend/src/types/domain/access.ts'), 'utf8')
const frontendApiSource = readFileSync(resolve('../frontend/src/api/domains/apiKeys.ts'), 'utf8')

assert.deepEqual(interfaceFields(repositorySource, 'ApiKeyCreateResult'), [
  'id',
  'key',
  'keyPrefix',
  'keySuffix',
  'revision'
], '创建和刷新结果只能包含复制密钥与列表协调所需字段')
assert.deepEqual(interfaceFields(repositorySource, 'ApiKeyPatchResult'), [
  'id',
  'revision',
  'changedFields',
  'rowPatch'
], 'PATCH 结果只能包含字段级 mutation 回执')
assert.deepEqual(interfaceFields(frontendTypesSource, 'CreatedApiKey'), [
  'id',
  'key',
  'keyPrefix',
  'keySuffix',
  'revision'
], '前端创建和刷新响应类型必须保持最小字段集')
assert.deepEqual(interfaceFields(frontendTypesSource, 'ApiKeyMutationResult'), [
  'id',
  'revision',
  'changedFields',
  'rowPatch'
], '前端 PATCH 响应类型不得接收后端审计上下文')

const refreshRouteSource = sourceBetween(
  routesSource,
  "apiKeysRouter.post('/:id/refresh-key'",
  "apiKeysRouter.post('/', mutationGuard("
)
assert.match(refreshRouteSource, /setNoStoreHeaders\(res\)[\s\S]*res\.json\(ok\(outcome\.result,/, '刷新密钥必须仅返回内部 outcome.result 并禁止缓存')
assert.doesNotMatch(refreshRouteSource, /res\.json\(ok\(outcome(?:,|\))/, '刷新密钥不得返回 owner、旧密钥标识或缓存失效元数据')

const createRouteSource = sourceBetween(
  routesSource,
  "apiKeysRouter.post('/', mutationGuard(",
  "apiKeysRouter.patch('/:id'"
)
assert.match(createRouteSource, /result:\s*\{\s*id: created\.id,\s*key: created\.key,\s*keyPrefix: created\.keyPrefix,\s*keySuffix: created\.keySuffix,\s*revision: created\.revision\s*\}/, '创建路由必须显式投影最小密钥回执')
assert.match(createRouteSource, /setNoStoreHeaders\(res\)[\s\S]*res\.status\(201\)\.json\(ok\(apiKey,/, '创建密钥响应必须禁止缓存')
assert.doesNotMatch(createRouteSource, /result:\s*created\b|result:\s*\{\s*\.\.\.created/, '创建路由不得直接返回完整仓储账户对象')

const patchRouteSource = sourceBetween(
  routesSource,
  "apiKeysRouter.patch('/:id'",
  "apiKeysRouter.delete('/:id'"
)
assert.match(patchRouteSource, /res\.json\(ok\(outcome\.result\)\)/, 'PATCH 路由必须剥离日志和缓存失效上下文后返回最小结果')
for (const internalField of ['ownerSystemAccountId', 'resourceName', 'before', 'after', 'validationCacheError']) {
  assert(!interfaceFields(frontendTypesSource, 'ApiKeyMutationResult').includes(internalField), `前端 PATCH 响应不得暴露 ${internalField}`)
}

assert.match(frontendApiSource, /create:[^\n]+unwrap<CreatedApiKey>/, '管理端创建 API 必须绑定最小创建回执')
assert.match(frontendApiSource, /update:[^\n]+unwrap<ApiKeyMutationResult>/, '管理端 PATCH API 必须绑定字段级 mutation 回执')
assert.match(frontendApiSource, /refreshKey:[^\n]+unwrap<CreatedApiKey>/, '管理端刷新 API 必须绑定最小密钥回执')
assert.match(frontendApiSource, /myApiKeysApi[\s\S]*create:[^\n]+unwrap<CreatedApiKey>/, '个人端创建 API 必须复用最小创建回执')
assert.match(frontendApiSource, /myApiKeysApi[\s\S]*update:[^\n]+unwrap<ApiKeyMutationResult>/, '个人端 PATCH API 必须复用字段级 mutation 回执')

console.log('API Key 响应边界回归通过：创建、刷新和 PATCH 仅返回页面消费字段，内部审计与缓存元数据不出后端')

function interfaceFields(source: string, name: string): string[] {
  const body = source.match(new RegExp(`export interface ${name} \\{([\\s\\S]*?)\\n\\}`))?.[1]
  assert(body, `缺少接口 ${name}`)
  return [...body.matchAll(/^\s*([A-Za-z][A-Za-z0-9]*)(?:\?)?:/gm)].map((match) => match[1]!)
}

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start + startMarker.length)
  assert(start >= 0 && end > start, `无法定位源码区间：${startMarker} -> ${endMarker}`)
  return source.slice(start, end)
}

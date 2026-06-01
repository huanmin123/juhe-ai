import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { backendRoot } from '../../config/runtime.js'

const serverSource = readFileSync(resolve(backendRoot, 'src/server.ts'), 'utf8')
const systemApiSource = readFileSync(resolve(backendRoot, 'src/modules/system-api/system-api-app.ts'), 'utf8')
const proxySource = readFileSync(resolve(backendRoot, 'src/modules/db-service/db-service-http-proxy.ts'), 'utf8')

const forbiddenServerImports = [
  'modules/accounts/',
  'modules/announcements/',
  'modules/api-keys/',
  'modules/audit-logs/audit-logs.routes',
  'modules/authorization-options/',
  'modules/authorizations/',
  'modules/auth/auth.middleware',
  'modules/auth/auth.routes',
  'modules/error-policies/',
  'modules/groups/',
  'modules/operation-logs/operation-logs.routes',
  'modules/openai-oauth/openai-oauth.routes',
  'modules/providers/',
  'modules/proxies/',
  'modules/runtime-logs/runtime-logs.routes',
  'modules/settings/settings.routes',
  'modules/stats/',
  'modules/system-accounts/',
  'modules/system-teams/',
  'modules/table-monitor/',
  'modules/usage-records/',
  'storage/'
]

for (const forbiddenImport of forbiddenServerImports) {
  assert.equal(
    serverSource.includes(forbiddenImport),
    false,
    `server.ts 不能直接导入管理 API 或 storage：${forbiddenImport}`
  )
}

assert.equal(serverSource.includes('requestDbService'), false, 'server.ts 不能通过 DB service IPC 直接处理管理 API')
assert.equal(serverSource.includes('express.json'), false, 'server.ts 不能为系统管理 API 解析 JSON body')
assert(serverSource.includes('createDbServiceHttpProxy'), 'server.ts 必须通过 DB service HTTP proxy 承接系统管理 API')
assert(systemApiSource.includes('listPublicGlobalSettings'), '公开全局设置应在 DB service system API 内直接读取')

const proxyIndex = serverSource.indexOf('app.use(systemApiPrefix, dbServiceHttpProxy)')
const publicProxyIndex = serverSource.indexOf('app.use(publicApiPrefix, dbServiceHttpProxy)')
const gatewayRawIndex = serverSource.indexOf('express.raw({ type: () => true')
assert(proxyIndex >= 0, 'server.ts 必须挂载 DB service HTTP proxy')
assert(publicProxyIndex >= 0, 'server.ts 必须通过 DB service HTTP proxy 承接公开系统 API')
assert(gatewayRawIndex >= 0, 'server.ts 必须保留网关 raw body 解析')
assert(proxyIndex < gatewayRawIndex, '系统管理 API 代理必须早于网关 raw body 解析，避免主进程消费管理 API 请求体')
assert(publicProxyIndex < gatewayRawIndex, '公开系统 API 代理必须早于网关 raw body 解析，避免主进程消费公开 API 请求体')
assert(systemApiSource.includes("systemApiJsonBodyLimit = '256kb'"), 'DB service system API JSON 请求体上限必须保持 256KB')
assert(proxySource.includes('dbServiceHttpProxyMaxInFlight'), 'DB service HTTP proxy 必须保留最大并发保护')
assert(proxySource.includes('dbServiceHttpProxyTimeoutMs'), 'DB service HTTP proxy 必须保留内部超时保护')

console.log('DB service system API 边界回归通过：主进程不直接挂载管理 API / storage，系统 API 由 DB service 承载且代理具备并发与超时边界')

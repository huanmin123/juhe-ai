import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { backendRoot } from '../../config/runtime.js'
import { dbServiceOperationPriority } from '../../modules/db-service/db-service-request-priority.js'

const serverSource = readFileSync(resolve(backendRoot, 'src/server.ts'), 'utf8')
const dbServiceSource = readFileSync(resolve(backendRoot, 'src/db-service.ts'), 'utf8')
const systemApiSource = readFileSync(resolve(backendRoot, 'src/modules/system-api/system-api-app.ts'), 'utf8')
const proxySource = readFileSync(resolve(backendRoot, 'src/modules/db-service/db-service-http-proxy.ts'), 'utf8')
const dbServicePrioritySource = readFileSync(resolve(backendRoot, 'src/modules/db-service/db-service-request-priority.ts'), 'utf8')
const dbServiceAccessModeSource = readFileSync(resolve(backendRoot, 'src/modules/db-service/db-service-operation-access-mode.ts'), 'utf8')

const forbiddenServerImports = [
  'modules/accounts/',
  'modules/announcements/',
  'modules/api-keys/',
  'modules/audit-logs/audit-logs.routes',
  'modules/authorization-options/',
  'modules/authorizations/',
  'modules/auth/auth.middleware',
  'modules/auth/auth.routes',
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
  'storage/repositories'
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
const gatewayRawIndex = serverSource.indexOf('  parseGatewayRawBody,')
assert(proxyIndex >= 0, 'server.ts 必须挂载 DB service HTTP proxy')
assert(publicProxyIndex >= 0, 'server.ts 必须通过 DB service HTTP proxy 承接公开系统 API')
assert(gatewayRawIndex >= 0, 'server.ts 必须保留网关 raw body 解析')
assert(proxyIndex < gatewayRawIndex, '系统管理 API 代理必须早于网关 raw body 解析，避免主进程消费管理 API 请求体')
assert(publicProxyIndex < gatewayRawIndex, '公开系统 API 代理必须早于网关 raw body 解析，避免主进程消费公开 API 请求体')
assert(systemApiSource.includes("systemApiJsonBodyLimit = '256kb'"), 'DB service system API JSON 请求体上限必须保持 256KB')
assert(systemApiSource.includes("chatSystemApiJsonBodyLimit = '24mb'"), 'AI 问答必须使用独立且有界的图片 JSON 请求体上限')
const chatJsonParserIndex = systemApiSource.indexOf('express.json({ limit: chatSystemApiJsonBodyLimit })')
const genericJsonParserIndex = systemApiSource.indexOf('app.use(systemApiPrefix, express.json({ limit: systemApiJsonBodyLimit })')
assert(chatJsonParserIndex >= 0, 'AI 问答必须挂载专用 JSON parser')
assert(chatJsonParserIndex < genericJsonParserIndex, 'AI 问答专用 parser 必须先于通用 256KB parser')
const chatAuthIndex = systemApiSource.indexOf("app.use(`${systemApiPrefix}/my-chat`, requireAuth")
const chatAdmissionIndex = systemApiSource.indexOf('systemApiDbServiceAdmissionControl, express.json({ limit: chatSystemApiJsonBodyLimit })')
assert(chatAuthIndex >= 0 && chatAuthIndex < chatJsonParserIndex, 'AI 问答必须先认证再读取大 JSON 请求体')
assert(chatAdmissionIndex >= 0 && chatAdmissionIndex < chatJsonParserIndex, 'AI 问答必须先经过准入控制再读取大 JSON 请求体')
assert(proxySource.includes('dbServiceHttpProxyMaxInFlight'), 'DB service HTTP proxy 必须保留最大并发保护')
assert(proxySource.includes('dbServiceHttpProxyTimeoutMs'), 'DB service HTTP proxy 必须保留内部超时保护')
assert(dbServiceSource.includes('shouldQueueDbServiceRequest'), 'DB service 父进程 IPC 请求必须先区分读写调度路径')
assert(dbServiceSource.includes('enqueueDbServiceRequest'), 'DB service 写入/维护 IPC 请求必须进入内部优先级队列')
assert(dbServiceSource.includes('dispatchDbServiceRequestImmediately'), 'DB service 读/runtime IPC 请求必须支持绕过写队列直接派发')
assert(dbServiceSource.includes('shiftNextDispatchableDbServiceRequest'), 'DB service 内部队列必须按优先级取下一个可派发请求')
assert(dbServiceSource.includes('yieldDbServiceRequestQueue'), 'DB service 内部队列每个请求后必须让出事件循环，避免后台 IPC 长时间压住 HTTP 管理请求')
assert(systemApiSource.includes('systemApiDbServiceAdmissionControl'), 'DB service system API 必须有内部在途请求保护，避免管理端慢查询压住 DB service')
const systemJsonParserIndex = systemApiSource.indexOf('app.use(systemApiPrefix, express.json')
const publicJsonParserIndex = systemApiSource.indexOf('app.use(publicApiPrefix, capturePublicApiLog, express.json')
assert.equal(systemApiSource.includes('systemApiReadOnlyMethodMiddleware'), false, '临时发布不得挂载 API 方法拦截门禁')
assert(publicJsonParserIndex >= 0, 'Public API 必须保留独立 JSON body parser 和认证链')
assert(dbServiceSource.includes('dbServiceRequestQueueMaxRequests'), 'DB service 子进程队列必须保留请求数上限')
assert(dbServiceSource.includes('dbServiceRequestQueueMaxBytes'), 'DB service 子进程队列必须保留字节上限')
assert(
  dbServiceSource.includes('dbServiceRequestPriorityForMessage')
    && dbServiceSource.includes('message.priority')
    && dbServiceSource.includes('normalizeDbServiceRequestPriority'),
  'DB service 子进程队列必须支持 IPC 显式优先级，确保后台 worker 请求可整体降级'
)
assert(
  dbServiceSource.includes('dbServiceHighDispatchesBeforeLow')
    && dbServiceSource.includes('dbServiceRequestPriorityOrder')
    && dbServiceSource.includes("return ['high', 'normal', 'low']")
    && dbServiceSource.includes("return ['low', 'high', 'normal']"),
  'DB service 内部队列必须保留 high 优先，同时通过 high dispatch 配额避免 low 长期饥饿'
)
assert(
  dbServiceSource.includes('shiftNextDispatchableDbServiceRequest')
    && dbServiceSource.includes('canShiftQueuedDbServiceRequest')
    && !dbServiceSource.includes('blockedByConcurrentLimit'),
  'DB service concurrent 写池满时必须跳过暂不可执行请求，不能队首阻塞普通管理操作'
)
assert(dbServicePrioritySource.includes('dbServiceOperationAccessMode'), 'DB service 优先级必须由 operation access mode 派生')
assert(dbServiceAccessModeSource.includes("cleanup_expired_system_sessions: 'maintenance'"), 'DB service 维护类操作必须登记为 maintenance access mode')
assert.equal(
  dbServiceOperationPriority({ type: 'cleanup_expired_system_sessions', expiredBefore: '2000-01-01T00:00:00.000Z', limit: 1 }),
  'low',
  '后台系统会话清理必须低优先级，不能压住管理操作'
)
assert.equal(
  dbServiceOperationPriority({ type: 'mark_account_exception', accountId: 'acct_priority_regression', errorCode: 'manual', reason: 'regression' }),
  'high',
  '用户可感知账号状态写入必须高优先级'
)

console.log('DB service system API 边界回归通过：主进程不直接挂载管理 API / storage，系统 API 由 DB service 承载且代理具备并发与超时边界')

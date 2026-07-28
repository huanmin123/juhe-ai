import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import express from 'express'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-user-reference-data-'))
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_CHAT_DATABASE_PATH = join(tempRoot, 'chat.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')
process.env.JUHE_AI_SECRET = 'user-reference-data-regression-secret'

const [
  { uiBootstrapRouter },
  { forceSelfAccessScope, requireAdmin },
  { withRequestAuthContext },
  repositories,
  databaseModule,
  databaseClientModule,
  { resolveSystemApiDbAccessMode }
] = await Promise.all([
  import('../../modules/ui-bootstrap/ui-bootstrap.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../modules/auth/request-context.js'),
  import('../../storage/repositories.js'),
  import('../../storage/database.js'),
  import('../../storage/database-client.js'),
  import('../../modules/system-api/system-api-db-access.js')
])

type ViewerKey = 'admin' | 'user-a' | 'user-b'
type UserReferenceData = import('../../domain/types.js').UserReferenceData

const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
let server: ReturnType<express.Express['listen']> | undefined

try {
  const admin = await createAccount('admin', 'admin')
  const userA = await createAccount('user-a', 'user')
  const userB = await createAccount('user-b', 'user')
  const viewers = new Map<ViewerKey, {
    systemAccountId: string
    username: string
    displayName: string
    role: 'admin' | 'user'
    mustChangePassword: boolean
    sessionId: string
  }>([
    ['admin', requestContext(admin, 'admin')],
    ['user-a', requestContext(userA, 'user')],
    ['user-b', requestContext(userB, 'user')]
  ])

  const app = express()
  app.use((req, _res, next) => {
    const viewer = viewers.get(String(req.headers['x-test-viewer']) as ViewerKey)
    withRequestAuthContext(viewer, next)
  })
  app.use('/__aisys__/api/my-ui-bootstrap', forceSelfAccessScope, uiBootstrapRouter)
  app.use('/__aisys__/api/ui-bootstrap', requireAdmin, uiBootstrapRouter)

  server = app.listen(0, '127.0.0.1')
  await new Promise<void>((resolve, reject) => {
    server?.once('listening', resolve)
    server?.once('error', reject)
  })
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('用户引用回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`

  const anonymousResponse = await fetch(`${baseUrl}/__aisys__/api/my-ui-bootstrap/options`)
  assert.equal(anonymousResponse.status, 401, '未登录请求不能读取默认资源引用')

  const userASelf = await getReferenceData(baseUrl, `/__aisys__/api/my-ui-bootstrap/options?systemAccountId=${encodeURIComponent(userB.id)}`, 'user-a')
  assert.equal(userASelf.systemAccountId, userA.id, 'self 路由必须忽略伪造的目标 owner')
  assertReferenceContract(userASelf)

  const adminSelf = await getReferenceData(baseUrl, `/__aisys__/api/my-ui-bootstrap/options?systemAccountId=${encodeURIComponent(userB.id)}`, 'admin')
  assert.equal(adminSelf.systemAccountId, admin.id, '管理员走 self 路由时也只能读取本人引用')

  const userAdminResponse = await request(baseUrl, `/__aisys__/api/ui-bootstrap/options?systemAccountId=${encodeURIComponent(userB.id)}`, 'user-a')
  assert.equal(userAdminResponse.status, 403, '普通用户不能访问管理目标引用接口')
  const missingTargetResponse = await request(baseUrl, '/__aisys__/api/ui-bootstrap/options', 'admin')
  assert.equal(missingTargetResponse.status, 400, '管理引用接口必须要求具体 owner')
  const allTargetResponse = await request(baseUrl, '/__aisys__/api/ui-bootstrap/options?systemAccountId=all', 'admin')
  assert.equal(allTargetResponse.status, 400, '管理引用接口不能把 all 解释为单 owner')
  const missingOwnerResponse = await request(baseUrl, '/__aisys__/api/ui-bootstrap/options?systemAccountId=missing-owner', 'admin')
  assert.equal(missingOwnerResponse.status, 404, '不存在的管理目标必须返回 404')

  const adminTarget = await getReferenceData(baseUrl, `/__aisys__/api/ui-bootstrap/options?systemAccountId=${encodeURIComponent(userB.id)}`, 'admin')
  assert.equal(adminTarget.systemAccountId, userB.id, '管理员目标引用必须使用明确指定的 owner')
  assertReferenceContract(adminTarget)

  const preferred = await repositories.findPreferredDefaultRouteStrategyReferenceAsync(userB.id)
  assert.deepEqual(preferred, adminTarget.preferredDefaultRouteStrategy, 'HTTP 首选路由必须复用共享 GPT resolver')
  assert.equal(preferred?.status, 'active', '首选路由必须是 active')

  databaseModule.getBusinessDatabase()
    .prepare('UPDATE route_strategies SET status = ? WHERE id = ?')
    .run('disabled', preferred?.id ?? '')
  assert.equal(await repositories.findPreferredDefaultRouteStrategyReferenceAsync(userB.id), undefined, 'GPT 默认路由停用后不得退化选择其他供应商')
  const disabledPreferred = await repositories.findUserReferenceDataForSystemAccountAsync(userB.id)
  assert.equal(disabledPreferred?.preferredDefaultRouteStrategy, undefined, 'bootstrap 不能返回不可调度的首选路由')

  assert.equal(resolveSystemApiDbAccessMode(requestFor('/__aisys__/api/my-ui-bootstrap/options'), '/__aisys__/api'), 'read')
  assert.equal(resolveSystemApiDbAccessMode(requestFor('/__aisys__/api/ui-bootstrap/options'), '/__aisys__/api'), 'read')

  const postgresSql: string[] = []
  const fakePostgresClient = {
    driver: 'postgres',
    dialect: databaseClientModule.postgresDialect,
    query: async (sql: string) => {
      postgresSql.push(sql)
      return [{
        system_account_id: 'pg-owner',
        provider_code: 'gpt',
        group_id: 'pg-group',
        group_name: '默认 GPT 分组',
        group_enabled: 1,
        route_strategy_id: 'pg-route',
        route_strategy_name: '默认 GPT 路由',
        route_strategy_mode: 'normal',
        route_strategy_status: 'active',
        route_binding_status: 'active'
      }]
    },
    one: async (sql: string) => {
      postgresSql.push(sql)
      return { id: 'pg-route', name: '默认 GPT 路由', mode: 'normal', status: 'active' }
    },
    execute: async () => ({ changes: 0 }),
    transaction: async () => {
      throw new Error('用户引用只读回归不应开启事务')
    }
  } as unknown as import('../../storage/database-client.js').DatabaseClient
  const postgresReference = await repositories.findUserReferenceDataForSystemAccountAsync('pg-owner', fakePostgresClient)
  assert.equal(postgresReference?.preferredDefaultRouteStrategy?.id, 'pg-route', 'PostgreSQL mapper 应保留 GPT 首选路由')
  assert.equal((await repositories.findPreferredDefaultRouteStrategyReferenceAsync('pg-owner', fakePostgresClient))?.id, 'pg-route')
  assert.equal((await repositories.findPreferredDefaultRouteStrategyReferenceAsync('pg-owner', fakePostgresClient, true))?.id, 'pg-route')
  assert.match(postgresSql.at(-1) ?? '', /FOR UPDATE OF route_strategies, route_strategy_groups, groups/, '事务内默认路由选择必须锁定关联行')
  assert.equal(postgresSql.every((sql) => sql.includes('"juhe_business".')), true, 'PostgreSQL 查询必须限定业务 schema')
  assert.equal(postgresSql.every((sql) => /is_default\s*=\s*1/.test(sql)), true, 'PostgreSQL 默认资源查询必须匹配最终 DDL 的 integer 标志位')
  assert.equal(postgresSql.every((sql) => !/is_default\s*=\s*TRUE/.test(sql)), true, 'PostgreSQL integer 标志位不得与 boolean TRUE 比较')

  const repositorySource = readFileSync(new URL('../../storage/user-reference-data.repository.ts', import.meta.url), 'utf8')
  assert.doesNotMatch(repositorySource, /AccountSummary|GroupSummary|RouteStrategySummary|todayUsage|permissions/i, '引用 repository 不能回退到宽摘要或运行态字段')
  assert.match(repositorySource, /function userReferenceTrueLiteral[\s\S]*return '1'/, '默认资源查询必须与 PostgreSQL/SQLite 最终 integer DDL 一致')

  console.log('user reference data regression passed')
} finally {
  if (server) {
    await new Promise<void>((resolve) => server?.close(() => resolve()))
  }
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function createAccount(label: string, role: 'admin' | 'user') {
  return repositories.createSystemAccountAsync({
    username: `reference_${label}_${suffix}`,
    displayName: `引用回归${label}${suffix}`,
    password: `Reference-${suffix}-Pwd!`,
    role,
    status: 'active',
    mustChangePassword: false,
    imageGenerationEnabled: false
  })
}

function requestContext(account: { id: string; username: string; displayName: string }, role: 'admin' | 'user') {
  return {
    systemAccountId: account.id,
    username: account.username,
    displayName: account.displayName,
    role,
    mustChangePassword: false,
    sessionId: `session-${account.id}`
  }
}

async function request(baseUrl: string, path: string, viewer: ViewerKey): Promise<Response> {
  return fetch(`${baseUrl}${path}`, { headers: { 'x-test-viewer': viewer } })
}

async function getReferenceData(baseUrl: string, path: string, viewer: ViewerKey): Promise<UserReferenceData> {
  const response = await request(baseUrl, path, viewer)
  assert.equal(response.status, 200, `${path} 应返回 200`)
  const body = await response.json() as { data: UserReferenceData }
  return body.data
}

function assertReferenceContract(value: UserReferenceData): void {
  assert.deepEqual(Object.keys(value).sort(), ['preferredDefaultRouteStrategy', 'providerDefaults', 'systemAccountId'])
  assert.equal(value.providerDefaults.length, 8, '每个新系统账户应返回八个默认供应商分组引用')
  for (const providerDefault of value.providerDefaults) {
    assert.deepEqual(Object.keys(providerDefault.defaultGroup).sort(), ['id', 'name'])
    const expectedKeys = providerDefault.defaultRouteStrategy
      ? ['defaultGroup', 'defaultRouteStrategy', 'providerCode']
      : ['defaultGroup', 'providerCode']
    assert.deepEqual(Object.keys(providerDefault).sort(), expectedKeys)
    if (providerDefault.defaultRouteStrategy) {
      assert.deepEqual(Object.keys(providerDefault.defaultRouteStrategy).sort(), ['id', 'mode', 'name', 'status'])
    }
  }
  assert.equal(value.providerDefaults.filter((item) => item.defaultRouteStrategy).length, 7, 'Hybrid 默认分组不应伪造默认路由')
  assert.deepEqual(Object.keys(value.preferredDefaultRouteStrategy ?? {}).sort(), ['id', 'mode', 'name', 'status'])
}

function requestFor(path: string) {
  return { method: 'GET', path, originalUrl: path }
}

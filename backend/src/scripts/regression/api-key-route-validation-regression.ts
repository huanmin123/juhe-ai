import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import cors from 'cors'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-route-validation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'api-key-route-validation.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-route-validation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { apiKeysRouter },
  { routeStrategiesRouter },
  { authRouter },
  { captchaAnswerForTest },
  { requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule
] = await Promise.all([
  import('../../modules/api-keys/api-keys.routes.js'),
  import('../../modules/route-strategies/route-strategies.routes.js'),
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/auth/captcha.service.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/api-keys', requireAdmin, apiKeysRouter)
app.use('/__aisys__/api/route-strategies', requireAdmin, routeStrategiesRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface RouteStrategyListItem {
  id: string
  status: string
  isDefault: boolean
  groupBindingPreview: Array<{
    providerCode?: string
  }>
}

interface RouteStrategyListResult {
  items: RouteStrategyListItem[]
}

interface ApiKeyCreateResult {
  id: string
  key: string
  keyPrefix: string
  keySuffix: string
  revision: string
}

interface ApiKeyListItem {
  id: string
  name: string
  description?: string
  keyPrefix: string
  keySuffix: string
  status: string
  routeStrategyId: string
  routeStrategyName?: string
  routeStrategyMode?: string
  expiresAt?: string
  availabilitySchedule?: {
    enabled?: boolean
    windows?: unknown[]
  }
  usage: {
    requestCount: number
    totalTokens: number
    totalCost: number
  }
  revision: string
}

interface ApiKeyListResult {
  items: ApiKeyListItem[]
}

interface ApiKeySecretResult {
  key: string
}

interface ApiKeyRefreshResult {
  id: string
  key: string
  keyPrefix: string
  keySuffix: string
  revision: string
}

interface ApiKeyPatchResult {
  id: string
  revision: string
  changedFields: string[]
  rowPatch: Record<string, unknown> & { revision: string }
}

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  try {
    appServer = app.listen(0, '127.0.0.1')
    await listen(appServer)
    const address = serverAddress(appServer)
    const baseUrl = `http://127.0.0.1:${address.port}`
    const adminCookie = await login(baseUrl)
    const preferredRouteStrategy = await findGptDefaultRouteStrategy(baseUrl, adminCookie)

    await assertBadRequestMessageIncludes(baseUrl, adminCookie, {
      name: '旧分组绑定字段回归 Key',
      routeStrategyId: preferredRouteStrategy.id,
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }]
    }, 'groupBindings')

    await assertBadRequestMessageIncludes(baseUrl, adminCookie, {
      name: '旧客户端画像字段回归 Key',
      routeStrategyId: preferredRouteStrategy.id,
      clientProfile: 'codex'
    }, 'clientProfile')

    await assertBadRequestMessageIncludes(baseUrl, adminCookie, {
      name: '旧显式混合规则字段回归 Key',
      routeStrategyId: preferredRouteStrategy.id,
      explicitHybridRouteRules: []
    }, 'explicitHybridRouteRules')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '不存在策略路由回归 Key',
      routeStrategyId: 'route_strategy_not_found'
    }, 'API Key 绑定的策略路由不存在或不属于当前用户')

    const validApiKey = await createApiKey(baseUrl, adminCookie, {
      name: '更新校验回归 Key'
    })
    let validSeed = await assertApiKeyListUsageContract(baseUrl, adminCookie, validApiKey.id)
    assert(validSeed.name === '更新校验回归 Key', '创建详情应由 GET 列表 seed 提供')
    assert(validSeed.routeStrategyId === preferredRouteStrategy.id, '省略 routeStrategyId 时应选择启用的 GPT 默认路由')
    assert(validSeed.routeStrategyMode === 'normal', 'API Key 列表应返回策略路由模式摘要')
    assert(validSeed.revision === validApiKey.revision, '创建回执 revision 应与列表 seed 一致')
    assert(!Object.prototype.hasOwnProperty.call(validSeed, 'groupBindings'), 'API Key 列表不应返回 groupBindings')
    assert(!Object.prototype.hasOwnProperty.call(validSeed, 'clientProfile'), 'API Key 列表不应返回 clientProfile')
    assert(!Object.prototype.hasOwnProperty.call(validSeed, 'explicitHybridRouteRules'), 'API Key 列表不应返回 explicitHybridRouteRules')

    const secretResult = await getEnvelope<ApiKeySecretResult>(baseUrl, `/__aisys__/api/api-keys/${validApiKey.id}/secret`, adminCookie)
    assertExactKeys(secretResult, ['key'], '复制完整密钥接口')
    assert(secretResult.key === validApiKey.key, '复制完整密钥接口应返回创建时的完整 API Key')
    assert(secretResult.key.startsWith(validApiKey.keyPrefix), '复制完整密钥接口返回值应匹配安全展示前缀')
    assert(secretResult.key.endsWith(validApiKey.keySuffix), '复制完整密钥接口返回值应匹配安全展示后缀')
    const refreshedApiKey = await postEnvelope<ApiKeyRefreshResult>(baseUrl, `/__aisys__/api/api-keys/${validApiKey.id}/refresh-key`, adminCookie, {})
    assertExactKeys(refreshedApiKey, ['id', 'key', 'keyPrefix', 'keySuffix', 'revision'], '刷新密钥接口')
    assert(refreshedApiKey.id === validApiKey.id, '刷新密钥应返回原 API Key id')
    assert(refreshedApiKey.revision !== validApiKey.revision, '刷新密钥应推进 revision')
    assert(refreshedApiKey.key !== validApiKey.key, '刷新密钥应返回新的完整 API Key')
    assert(refreshedApiKey.key.startsWith(refreshedApiKey.keyPrefix), '刷新密钥响应应匹配新的安全展示前缀')
    assert(refreshedApiKey.key.endsWith(refreshedApiKey.keySuffix), '刷新密钥响应应匹配新的安全展示后缀')
    const refreshedSecretResult = await getEnvelope<ApiKeySecretResult>(baseUrl, `/__aisys__/api/api-keys/${validApiKey.id}/secret`, adminCookie)
    assertExactKeys(refreshedSecretResult, ['key'], '刷新后的复制完整密钥接口')
    assert(refreshedSecretResult.key === refreshedApiKey.key, '复制完整密钥接口应返回刷新后的完整 API Key')

    validSeed = await getApiKeyListItem(baseUrl, adminCookie, validApiKey.id)
    assert(validSeed.revision === refreshedApiKey.revision, '刷新密钥回执 revision 应与列表 seed 一致')
    const disabledApiKey = await patchApiKey(baseUrl, adminCookie, validApiKey.id, validSeed.revision, {
      status: 'disabled'
    })
    assertPatchResult(disabledApiKey, validApiKey.id, ['status'], { status: 'disabled' })

    const noOpApiKey = await patchApiKey(baseUrl, adminCookie, validApiKey.id, disabledApiKey.revision, {
      status: 'disabled'
    })
    assertPatchResult(noOpApiKey, validApiKey.id, [], {})
    assert(noOpApiKey.revision === disabledApiKey.revision, 'no-op PATCH 不得推进 revision')

    const concurrentBaseRevision = noOpApiKey.revision
    const renamedApiKey = await patchApiKey(baseUrl, adminCookie, validApiKey.id, concurrentBaseRevision, {
      name: '更新校验回归 Key 已改名'
    })
    assertPatchResult(renamedApiKey, validApiKey.id, ['name'], { name: '更新校验回归 Key 已改名' })
    await assertPatchRevisionConflict(baseUrl, adminCookie, validApiKey.id, concurrentBaseRevision, {
      description: '冲突请求不应写入'
    }, renamedApiKey.revision)
    validSeed = await getApiKeyListItem(baseUrl, adminCookie, validApiKey.id)
    assert(validSeed.name === '更新校验回归 Key 已改名', 'revision 冲突不得覆盖先成功的字段')
    assert(validSeed.description !== '冲突请求不应写入', 'revision 冲突不得写入冲突请求字段')
    assert(validSeed.revision === renamedApiKey.revision, 'revision 冲突不得推进当前 revision')

    await assertPatchBadRequestMessage(baseUrl, adminCookie, validApiKey.id, renamedApiKey.revision, {
      routeStrategyId: 'route_strategy_not_found'
    }, 'API Key 绑定的策略路由不存在或不属于当前用户')

    const expiringApiKey = await createApiKey(baseUrl, adminCookie, {
      name: '清空过期时间回归 Key',
      expiresAt: '2099-06-01T00:01:00.000Z'
    })
    let expiringSeed = await getApiKeyListItem(baseUrl, adminCookie, expiringApiKey.id)
    assert(Boolean(expiringSeed.expiresAt), '创建 API Key 时应保存过期时间，并由 GET 列表返回')
    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '非法过期时间回归 Key',
      expiresAt: 'not-a-date'
    }, 'API Key 过期时间必须是有效时间字符串')
    await assertPatchBadRequestMessage(baseUrl, adminCookie, expiringApiKey.id, expiringSeed.revision, {
      expiresAt: 'not-a-date'
    }, 'API Key 过期时间必须是有效时间字符串')
    const clearedExpiringApiKey = await patchApiKey(baseUrl, adminCookie, expiringApiKey.id, expiringSeed.revision, {
      expiresAt: null
    })
    assertPatchResult(clearedExpiringApiKey, expiringApiKey.id, ['expiresAt'], { expiresAt: null })
    expiringSeed = await getApiKeyListItem(baseUrl, adminCookie, expiringApiKey.id)
    assert(!expiringSeed.expiresAt, '更新 API Key 时 expiresAt: null 应清空已有过期时间')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '非法时间计划回归 Key',
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        timezone: 'Asia/Shanghai',
        windows: [
          { daysOfWeek: [1], start: '22:00', end: '22:00' }
        ]
      }
    }, 'API Key 时间计划开始时间和停止时间不能相同')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '非法时间计划星期回归 Key',
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        timezone: 'Asia/Shanghai',
        windows: [
          { daysOfWeek: [1, 9], start: '22:00', end: '23:55' }
        ]
      }
    }, 'API Key 时间计划重复日期无效')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '缺失时间计划模式回归 Key',
      availabilitySchedule: {
        enabled: true,
        timezone: 'Asia/Shanghai',
        windows: [
          { daysOfWeek: [1], start: '22:00', end: '23:55' }
        ]
      }
    }, 'API Key 时间计划模式必须为 allow_windows')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '非法时间计划模式回归 Key',
      availabilitySchedule: {
        enabled: true,
        mode: 'legacy_windows',
        timezone: 'Asia/Shanghai',
        windows: [
          { daysOfWeek: [1], start: '22:00', end: '23:55' }
        ]
      }
    }, 'API Key 时间计划模式必须为 allow_windows')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '空时区时间计划回归 Key',
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        timezone: '',
        windows: [
          { daysOfWeek: [1], start: '22:00', end: '23:55' }
        ]
      }
    }, 'API Key 时间计划时区不能为空')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '空允许例外时间计划回归 Key',
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        timezone: 'Asia/Shanghai',
        windows: [
          { daysOfWeek: [1], start: '22:00', end: '23:55' }
        ],
        exceptions: [{ date: '2026-06-01', action: 'allow' }]
      }
    }, 'API Key 时间计划允许例外至少需要一个允许时段')

    const scheduleApiKey = await createApiKey(baseUrl, adminCookie, {
      name: '时间计划清空回归 Key',
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        windows: [
          { daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }
        ]
      }
    })
    let scheduleSeed = await getApiKeyListItem(baseUrl, adminCookie, scheduleApiKey.id)
    assert(scheduleSeed.availabilitySchedule?.enabled === true, '创建 API Key 应保存 availabilitySchedule 时间计划字段')
    assert(scheduleSeed.availabilitySchedule.windows?.length === 1, '时间计划字段应保存时段配置')
    const clearedScheduleApiKey = await patchApiKey(baseUrl, adminCookie, scheduleApiKey.id, scheduleSeed.revision, {
      availabilitySchedule: null
    })
    assertPatchResult(clearedScheduleApiKey, scheduleApiKey.id, ['availabilitySchedule'], { availabilitySchedule: null })
    scheduleSeed = await getApiKeyListItem(baseUrl, adminCookie, scheduleApiKey.id)
    assert(!scheduleSeed.availabilitySchedule, '更新 API Key 应支持 availabilitySchedule: null 清空时间计划')

    console.log('API Key HTTP 按需契约回归通过：创建回执、列表 seed、字段级 PATCH、revision 冲突、密钥读取及时间字段均符合预期')
  } finally {
    await closeServer(appServer)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function findGptDefaultRouteStrategy(baseUrl: string, cookie: string): Promise<RouteStrategyListItem> {
  const list = await getEnvelope<RouteStrategyListResult>(
    baseUrl,
    '/__aisys__/api/route-strategies?page=1&pageSize=100',
    cookie
  )
  const routeStrategy = list.items.find((item) => (
    item.isDefault
    && item.status === 'active'
    && item.groupBindingPreview.some((binding) => binding.providerCode === 'gpt')
  ))
  assert(routeStrategy, '回归夹具应提供绑定 GPT 默认分组的启用默认路由')
  return routeStrategy
}

async function createApiKey(baseUrl: string, cookie: string, body: Record<string, unknown>): Promise<ApiKeyCreateResult> {
  const created = await postEnvelope<ApiKeyCreateResult>(baseUrl, '/__aisys__/api/api-keys', cookie, body)
  assertExactKeys(created, ['id', 'key', 'keyPrefix', 'keySuffix', 'revision'], '创建 API Key 回执')
  assert(created.key.startsWith(created.keyPrefix), '创建回执完整密钥应匹配安全展示前缀')
  assert(created.key.endsWith(created.keySuffix), '创建回执完整密钥应匹配安全展示后缀')
  return created
}

async function assertBadRequestMessage(baseUrl: string, cookie: string, body: unknown, expectedMessage: string): Promise<void> {
  const response = await fetch(`${baseUrl}/__aisys__/api/api-keys`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert(response.status === 400, `创建 API Key 应返回 400，实际 HTTP ${response.status}: ${text}`)
  const parsed = JSON.parse(text) as { message?: string }
  assert(parsed.message === expectedMessage, `创建 API Key 错误文案异常：${parsed.message}`)
}

async function assertBadRequestMessageIncludes(baseUrl: string, cookie: string, body: unknown, expectedMessagePart: string): Promise<void> {
  const response = await fetch(`${baseUrl}/__aisys__/api/api-keys`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert(response.status === 400, `创建 API Key 应返回 400，实际 HTTP ${response.status}: ${text}`)
  const parsed = JSON.parse(text) as { message?: string }
  assert(parsed.message?.includes(expectedMessagePart), `创建 API Key 错误文案应包含 ${expectedMessagePart}，实际：${parsed.message}`)
}

async function assertPatchBadRequestMessage(
  baseUrl: string,
  cookie: string,
  id: string,
  expectedRevision: string,
  body: Record<string, unknown>,
  expectedMessage: string
): Promise<void> {
  const response = await fetch(`${baseUrl}/__aisys__/api/api-keys/${id}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ ...body, expectedRevision })
  })
  const text = await response.text()
  assert(response.status === 400, `更新 API Key 应返回 400，实际 HTTP ${response.status}: ${text}`)
  const parsed = JSON.parse(text) as { message?: string }
  assert(parsed.message === expectedMessage, `更新 API Key 错误文案异常：${parsed.message}`)
}

async function assertPatchRevisionConflict(
  baseUrl: string,
  cookie: string,
  id: string,
  staleRevision: string,
  body: Record<string, unknown>,
  expectedCurrentRevision: string
): Promise<void> {
  const response = await fetch(`${baseUrl}/__aisys__/api/api-keys/${id}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ ...body, expectedRevision: staleRevision })
  })
  const text = await response.text()
  assert(response.status === 409, `过期 revision 应返回 409，实际 HTTP ${response.status}: ${text}`)
  const parsed = JSON.parse(text) as { message?: string; currentRevision?: string }
  assertExactKeys(parsed, ['currentRevision', 'message'], 'revision 冲突响应')
  assert(parsed.currentRevision === expectedCurrentRevision, '409 应返回数据库当前 revision')
}

async function assertApiKeyListUsageContract(baseUrl: string, cookie: string, apiKeyId: string): Promise<ApiKeyListItem> {
  const listed = await getApiKeyListItem(baseUrl, cookie, apiKeyId)
  assertExactKeys(listed.usage, ['requestCount', 'totalCost', 'totalTokens'], 'API Key 列表 usage')
  assert(typeof listed.usage.requestCount === 'number', 'API Key 列表应同步返回累计用量')
  assert(typeof listed.usage.totalTokens === 'number', 'API Key 列表应同步返回累计 token')
  assert(typeof listed.usage.totalCost === 'number', 'API Key 列表应同步返回累计费用')
  assert(typeof listed.revision === 'string' && Boolean(listed.revision), 'API Key 列表应返回 PATCH revision')
  const removed = await fetch(`${baseUrl}/__aisys__/api/api-keys/usage?ids=${encodeURIComponent(apiKeyId)}`, { headers: { cookie } })
  assert(removed.status === 404, `独立 API Key 用量路由应删除，实际 HTTP ${removed.status}`)
  return listed
}

async function getApiKeyListItem(baseUrl: string, cookie: string, apiKeyId: string): Promise<ApiKeyListItem> {
  const list = await getEnvelope<ApiKeyListResult>(baseUrl, '/__aisys__/api/api-keys?page=1&pageSize=100', cookie)
  const listed = list.items.find((item) => item.id === apiKeyId)
  assert(listed, `API Key 列表缺少记录：${apiKeyId}`)
  return listed
}

async function patchApiKey(
  baseUrl: string,
  cookie: string,
  id: string,
  expectedRevision: string,
  changes: Record<string, unknown>
): Promise<ApiKeyPatchResult> {
  return patchEnvelope<ApiKeyPatchResult>(baseUrl, `/__aisys__/api/api-keys/${id}`, cookie, {
    ...changes,
    expectedRevision
  })
}

function assertPatchResult(
  result: ApiKeyPatchResult,
  expectedId: string,
  expectedChangedFields: string[],
  expectedRowValues: Record<string, unknown>
): void {
  assertExactKeys(result, ['changedFields', 'id', 'revision', 'rowPatch'], 'PATCH 回执')
  assert(result.id === expectedId, 'PATCH 回执 id 应匹配目标 API Key')
  assert(
    JSON.stringify(result.changedFields) === JSON.stringify(expectedChangedFields),
    `PATCH changedFields 异常：${JSON.stringify(result.changedFields)}`
  )
  assert(result.rowPatch.revision === result.revision, 'PATCH rowPatch revision 应与顶层 revision 一致')
  assertExactKeys(result.rowPatch, ['revision', ...Object.keys(expectedRowValues)], 'PATCH rowPatch')
  for (const [field, expectedValue] of Object.entries(expectedRowValues)) {
    assert(
      JSON.stringify(result.rowPatch[field]) === JSON.stringify(expectedValue),
      `PATCH rowPatch.${field} 异常：${JSON.stringify(result.rowPatch[field])}`
    )
  }
}

function assertExactKeys(value: unknown, expectedKeys: string[], label: string): void {
  assert(typeof value === 'object' && value !== null && !Array.isArray(value), `${label} 必须是对象`)
  const actual = Object.keys(value).sort()
  const expected = [...expectedKeys].sort()
  assert(
    JSON.stringify(actual) === JSON.stringify(expected),
    `${label} 字段应严格为 ${expected.join(', ')}，实际为 ${actual.join(', ')}`
  )
}

async function login(baseUrl: string): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string; image: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert(captchaCode, '测试夹具无法读取登录验证码')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      username: 'admin',
      password: 'admin',
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(response.ok, `登录失败：HTTP ${response.status} ${await response.text()}`)
  assert(cookie, '登录未返回会话 Cookie')
  const passwordResponse = await fetch(`${baseUrl}/__aisys__/api/auth/change-password`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ oldPassword: 'admin', newPassword: 'admin-regression-password' })
  })
  assert(passwordResponse.ok, `回归夹具修改初始密码失败：HTTP ${passwordResponse.status} ${await passwordResponse.text()}`)
  return cookie
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie?: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, cookie ? { headers: { cookie } } : undefined)
  return unwrapEnvelope<T>(response, path)
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return unwrapEnvelope<T>(response, path)
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return unwrapEnvelope<T>(response, path)
}

async function unwrapEnvelope<T>(response: Response, path: string): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\nAPI Key 路由校验回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})

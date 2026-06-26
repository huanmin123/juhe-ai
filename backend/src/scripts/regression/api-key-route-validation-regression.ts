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
  { authRouter },
  { captchaAnswerForTest },
  { requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  operationLogQueue
] = await Promise.all([
  import('../../modules/api-keys/api-keys.routes.js'),
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/auth/captcha.service.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/api-keys', requireAdmin, apiKeysRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface ApiKeySummary {
  id: string
  key: string
  keyPrefix: string
  keySuffix: string
  clientProfile?: string
  explicitHybridRouteRules?: Array<{
    id: string
    enabled: boolean
    sourceEndpointFamily: string
    upstreamEndpointFamily: string
    adapterMode: string
  }>
  expiresAt?: string
  availabilitySchedule?: {
    enabled?: boolean
    windows?: unknown[]
  }
}

interface ApiKeySecretResult {
  key: string
}

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  try {
    appServer = app.listen(0, '127.0.0.1')
    await listen(appServer)
    const address = serverAddress(appServer)
    const baseUrl = `http://127.0.0.1:${address.port}`
    const adminCookie = await login(baseUrl)

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '未绑定分组回归 Key'
    }, 'API Key 至少需要绑定一个分组')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '空绑定分组回归 Key',
      groupBindings: []
    }, 'API Key 至少需要绑定一个分组')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '空分组 ID 回归 Key',
      groupBindings: [{ groupId: '' }]
    }, 'API Key 分组无效')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '非法分组路由策略回归 Key',
      groupRouteStrategy: 'random_strategy',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }]
    }, '分组路由策略无效')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '非法分组权重回归 Key',
      groupRouteStrategy: 'weighted_round_robin',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin', weight: 101 }]
    }, '分组权重必须在 1-100 之间')

    const validApiKey = await postEnvelope<ApiKeySummary>(baseUrl, '/__aisys__/api/api-keys', adminCookie, {
      name: '更新校验回归 Key',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }]
    })
    const secretResult = await getEnvelope<ApiKeySecretResult>(baseUrl, `/__aisys__/api/api-keys/${validApiKey.id}/secret`, adminCookie)
    assert(secretResult.key === validApiKey.key, '复制完整密钥接口应返回创建时的完整 API Key')
    assert(secretResult.key.startsWith(validApiKey.keyPrefix), '复制完整密钥接口返回值应匹配安全展示前缀')
    assert(secretResult.key.endsWith(validApiKey.keySuffix), '复制完整密钥接口返回值应匹配安全展示后缀')
    const refreshedApiKey = await postEnvelope<ApiKeySummary>(baseUrl, `/__aisys__/api/api-keys/${validApiKey.id}/refresh-key`, adminCookie, {})
    assert(refreshedApiKey.key !== validApiKey.key, '刷新密钥应返回新的完整 API Key')
    assert(refreshedApiKey.key.startsWith(refreshedApiKey.keyPrefix), '刷新密钥响应应匹配新的安全展示前缀')
    assert(refreshedApiKey.key.endsWith(refreshedApiKey.keySuffix), '刷新密钥响应应匹配新的安全展示后缀')
    const refreshedSecretResult = await getEnvelope<ApiKeySecretResult>(baseUrl, `/__aisys__/api/api-keys/${validApiKey.id}/secret`, adminCookie)
    assert(refreshedSecretResult.key === refreshedApiKey.key, '复制完整密钥接口应返回刷新后的完整 API Key')
    await assertPatchBadRequestMessage(baseUrl, adminCookie, validApiKey.id, {
      groupRouteStrategy: 'random_strategy'
    }, '分组路由策略无效')
    await assertPatchBadRequestMessage(baseUrl, adminCookie, validApiKey.id, {
      groupRouteStrategy: 'weighted_round_robin',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin', weight: 0 }]
    }, '分组权重必须在 1-100 之间')
    const disabledApiKey = await patchEnvelope<ApiKeySummary & { status: string }>(baseUrl, `/__aisys__/api/api-keys/${validApiKey.id}`, adminCookie, {
      status: 'disabled'
    })
    assert(disabledApiKey.status === 'disabled', '仅更新 API Key 状态时不应要求重新提交分组绑定')

    const explicitHybridApiKey = await postEnvelope<ApiKeySummary>(baseUrl, '/__aisys__/api/api-keys', adminCookie, {
      name: '显式混合路由回归 Key',
      clientProfile: 'codex',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
      explicitHybridRouteRules: [
        {
          id: 'explicit_bridge_responses_to_messages',
          enabled: true,
          priority: 1,
          sourceClientProfile: 'codex',
          sourceEndpointFamily: 'responses',
          sourceModel: 'gpt-5.5',
          targetGroupId: 'grp_default_gpt_sys_admin',
          upstreamEndpointFamily: 'messages',
          upstreamModel: 'claude-opus-4-8',
          adapterMode: 'bridge'
        },
        {
          id: 'explicit_disabled_chat_alias',
          enabled: false,
          priority: 2,
          sourceClientProfile: 'auto',
          sourceEndpointFamily: 'chat_completions',
          sourceModel: 'alias-chat-model',
          targetGroupId: 'grp_default_gpt_sys_admin',
          upstreamEndpointFamily: 'chat_completions',
          upstreamModel: 'real-chat-model',
          adapterMode: 'direct'
        }
      ]
    })
    assert(explicitHybridApiKey.clientProfile === 'codex', 'API Key 应保存默认客户端画像')
    assert(explicitHybridApiKey.explicitHybridRouteRules?.length === 2, '显式混合路由应保留启用和禁用规则')
    assert(explicitHybridApiKey.explicitHybridRouteRules?.[0]?.upstreamEndpointFamily === 'messages', '显式混合路由应允许跨协议 bridge')
    assert(explicitHybridApiKey.explicitHybridRouteRules?.[1]?.enabled === false, '显式混合路由禁用规则保存后不应丢失')
    const clearedExplicitHybridApiKey = await patchEnvelope<ApiKeySummary>(baseUrl, `/__aisys__/api/api-keys/${explicitHybridApiKey.id}`, adminCookie, {
      explicitHybridRouteRules: []
    })
    assert(!clearedExplicitHybridApiKey.explicitHybridRouteRules?.length, '更新 API Key 应支持清空显式混合路由规则')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '显式混合路由直连跨协议非法 Key',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
      explicitHybridRouteRules: [
        {
          id: 'explicit_direct_responses_to_messages',
          enabled: true,
          priority: 1,
          sourceClientProfile: 'codex',
          sourceEndpointFamily: 'responses',
          targetGroupId: 'grp_default_gpt_sys_admin',
          upstreamEndpointFamily: 'messages',
          upstreamModel: 'claude-opus-4-8',
          adapterMode: 'direct'
        }
      ]
    }, '显式混合路由直连模式只能用于同协议模型别名')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '显式混合路由未绑定目标分组非法 Key',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
      explicitHybridRouteRules: [
        {
          id: 'explicit_unbound_target_group',
          enabled: true,
          priority: 1,
          sourceClientProfile: 'auto',
          sourceEndpointFamily: 'chat_completions',
          targetGroupId: 'grp_not_bound',
          upstreamEndpointFamily: 'chat_completions',
          upstreamModel: 'gpt-5.5',
          adapterMode: 'direct'
        }
      ]
    }, '显式混合路由目标分组必须是当前 API Key 已绑定且启用的分组：grp_not_bound')

    const expiringApiKey = await postEnvelope<ApiKeySummary>(baseUrl, '/__aisys__/api/api-keys', adminCookie, {
      name: '清空过期时间回归 Key',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
      expiresAt: '2099-06-01T00:01:00.000Z'
    })
    assert(Boolean(expiringApiKey.expiresAt), '创建 API Key 时应保存过期时间')
    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '非法过期时间回归 Key',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
      expiresAt: 'not-a-date'
    }, 'API Key 过期时间必须是有效时间字符串')
    await assertPatchBadRequestMessage(baseUrl, adminCookie, expiringApiKey.id, {
      expiresAt: 'not-a-date'
    }, 'API Key 过期时间必须是有效时间字符串')
    const clearedExpiringApiKey = await patchEnvelope<ApiKeySummary>(baseUrl, `/__aisys__/api/api-keys/${expiringApiKey.id}`, adminCookie, {
      expiresAt: null
    })
    assert(!clearedExpiringApiKey.expiresAt, '更新 API Key 时 expiresAt: null 应清空已有过期时间')

    await assertBadRequestMessage(baseUrl, adminCookie, {
      name: '非法时间计划回归 Key',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
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
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
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
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
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
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
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
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
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
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
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

    const scheduleApiKey = await postEnvelope<ApiKeySummary>(baseUrl, '/__aisys__/api/api-keys', adminCookie, {
      name: '时间计划清空回归 Key',
      groupBindings: [{ groupId: 'grp_default_gpt_sys_admin' }],
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        windows: [
          { daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }
        ]
      }
    })
    assert(scheduleApiKey.availabilitySchedule?.enabled === true, '创建 API Key 应保存 availabilitySchedule 时间计划字段')
    assert(scheduleApiKey.availabilitySchedule.windows?.length === 1, '时间计划字段应保存时段配置')
    const clearedScheduleApiKey = await patchEnvelope<ApiKeySummary>(baseUrl, `/__aisys__/api/api-keys/${scheduleApiKey.id}`, adminCookie, {
      availabilitySchedule: null
    })
    assert(!clearedScheduleApiKey.availabilitySchedule, '更新 API Key 应支持 availabilitySchedule: null 清空时间计划')

    console.log('API Key 路由校验回归通过：完整密钥复制、刷新密钥、创建/更新接口缺少分组、空分组绑定、空分组 ID、非法分组策略、非法权重、非法过期时间、非法时间计划模式、非法时段、非法星期、空时区、非法例外、时间计划清空和清空过期时间均符合预期')
  } finally {
    operationLogQueue.flushAllOperationLogQueue()
    await closeServer(appServer)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
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

async function assertPatchBadRequestMessage(baseUrl: string, cookie: string, id: string, body: unknown, expectedMessage: string): Promise<void> {
  const response = await fetch(`${baseUrl}/__aisys__/api/api-keys/${id}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert(response.status === 400, `更新 API Key 应返回 400，实际 HTTP ${response.status}: ${text}`)
  const parsed = JSON.parse(text) as { message?: string }
  assert(parsed.message === expectedMessage, `更新 API Key 错误文案异常：${parsed.message}`)
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



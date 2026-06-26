import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import http from 'node:http'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

if (process.env.JUHE_PERFORMANCE_SYSTEM_API_SMOKE_CHILD === 'postgres') {
  await runHttpSmoke()
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-performance-system-api-smoke-'))
try {
  process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
  process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
  process.env.JUHE_AI_CACHE_DRIVER = 'memory'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
  process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
  process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
  process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
  process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
  process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
  process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')
  process.env.JUHE_AI_SECRET = 'performance-system-api-smoke-secret'
  process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
  process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'

  await runHttpSmoke()

  if (process.env.JUHE_PERFORMANCE_SYSTEM_API_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_PERFORMANCE_SYSTEM_API_SMOKE_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_PERFORMANCE_SYSTEM_API_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_PERFORMANCE_SYSTEM_API_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_PERFORMANCE_SYSTEM_API_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('performance-system-api-smoke-regression passed')
} finally {
  await closeSqliteStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runHttpSmoke(): Promise<void> {
  const [
    { createSystemApiApp },
    { captchaAnswerForTest },
    { logger }
  ] = await Promise.all([
    import('../../modules/system-api/system-api-app.js'),
    import('../../modules/auth/captcha.service.js'),
    import('../../shared/logger.js')
  ])
  logger.level = 'silent'
  const label = process.env.JUHE_AI_DATABASE_DRIVER === 'postgres' ? 'postgres' : 'sqlite'
  const createdSystemAccountIds: string[] = []
  const createdGroupIds: string[] = []
  const createdAiAccountIds: string[] = []
  const createdApiKeyIds: string[] = []

  let server: http.Server | undefined
  try {
    console.log(`[performance-system-api-smoke:${label}] start app`)
    const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', trustProxy: true })
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`

    console.log(`[performance-system-api-smoke:${label}] settings/public`)
    const publicSettings = await getEnvelope<{ appName?: string; appIcon?: string }>(baseUrl, '/__aisys__/api/settings/public')
    assert.equal(publicSettings.appName, '聚合 AI', 'performance smoke 应能读取公开设置')
    assert.equal(publicSettings.appIcon, '/__aisys__/brand-icon.svg', 'performance smoke 应能读取公开图标')

    console.log(`[performance-system-api-smoke:${label}] captcha`)
    const captcha = await getEnvelope<{ captchaId: string }>(baseUrl, '/__aisys__/api/auth/captcha')
    const captchaCode = captchaAnswerForTest(captcha.captchaId)
    assert.ok(captchaCode, '测试夹具应能读取验证码答案')

    console.log(`[performance-system-api-smoke:${label}] login`)
    const loginResponse = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      signal: AbortSignal.timeout(10_000),
      body: JSON.stringify({
        username: 'admin',
        password: 'admin',
        captchaId: captcha.captchaId,
        captchaCode
      })
    })
    const loginText = await loginResponse.text()
    assert.equal(loginResponse.status, 200, `performance smoke 登录应成功，实际 HTTP ${loginResponse.status}: ${loginText}`)
    const cookie = loginResponse.headers.get('set-cookie')?.split(';')[0]
    assert.ok(cookie, 'performance smoke 登录应返回 session cookie')

    console.log(`[performance-system-api-smoke:${label}] auth/me`)
    const currentUser = await getEnvelope<{ username?: string; role?: string }>(baseUrl, '/__aisys__/api/auth/me', cookie)
    assert.equal(currentUser.username, 'admin', 'performance smoke 应能通过 session 读取当前用户')
    assert.equal(currentUser.role, 'super_admin', 'performance smoke 当前用户应为超级管理员')

    console.log(`[performance-system-api-smoke:${label}] settings/global`)
    const globalSettings = await getEnvelope<{ appName: string; appIcon: string }>(baseUrl, '/__aisys__/api/settings/global', cookie)
    assert.equal(globalSettings.appName, '聚合 AI', 'performance smoke 应能读取管理端全局设置')
    const nextAppName = `聚合 AI ${label} smoke`
    const updatedGlobalSettings = await patchEnvelope<{ appName: string; appIcon: string }>(baseUrl, '/__aisys__/api/settings/global', { appName: nextAppName }, cookie)
    assert.equal(updatedGlobalSettings.appName, nextAppName, 'performance smoke 应能更新全局设置')
    await patchEnvelope(baseUrl, '/__aisys__/api/settings/global', { appName: globalSettings.appName, appIcon: globalSettings.appIcon }, cookie)

    console.log(`[performance-system-api-smoke:${label}] settings`)
    const systemSettings = await getEnvelope<{ systemApiRateLimitIpReadPerMinute: number; systemApiRateLimitEnabled: boolean }>(baseUrl, '/__aisys__/api/settings', cookie)
    const nextReadLimit = systemSettings.systemApiRateLimitIpReadPerMinute + 1
    const updatedSystemSettings = await patchEnvelope<{ systemApiRateLimitIpReadPerMinute: number }>(baseUrl, '/__aisys__/api/settings', { systemApiRateLimitIpReadPerMinute: nextReadLimit }, cookie)
    assert.equal(updatedSystemSettings.systemApiRateLimitIpReadPerMinute, nextReadLimit, 'performance smoke 应能更新系统设置')
    await patchEnvelope(baseUrl, '/__aisys__/api/settings', { systemApiRateLimitIpReadPerMinute: systemSettings.systemApiRateLimitIpReadPerMinute }, cookie)

    console.log(`[performance-system-api-smoke:${label}] system-accounts`)
    const systemAccounts = await getEnvelope<{ items: Array<{ id: string; username: string; role: string }>; hasMore: boolean }>(baseUrl, '/__aisys__/api/system-accounts?page=1&pageSize=20', cookie)
    assert.ok(systemAccounts.items.some((account) => account.username === 'admin' && account.role === 'super_admin'), 'performance smoke 应能读取系统账户列表')
    const systemAccountOptions = await getEnvelope<Array<{ id: string; username: string }>>(baseUrl, '/__aisys__/api/system-accounts/options?ids=sys_admin', cookie)
    assert.deepEqual(systemAccountOptions.map((account) => account.id), ['sys_admin'], 'performance smoke 应能读取系统账户选项')
    const suffix = `${label}${Date.now()}${Math.random().toString(16).slice(2, 6)}`
    const createdAccount = await postEnvelope<{ id: string; username: string; status: string }>(baseUrl, '/__aisys__/api/system-accounts', {
      username: `smoke_sys_${suffix}`,
      displayName: `烟测系统账户${suffix}`,
      password: `Pwd${suffix}`,
      role: 'user',
      status: 'active',
      mustChangePassword: false
    }, cookie)
    createdSystemAccountIds.push(createdAccount.id)
    assert.equal(createdAccount.status, 'active', 'performance smoke 应能创建系统账户')
    const disabledAccount = await patchEnvelope<{ id: string; status: string }>(baseUrl, `/__aisys__/api/system-accounts/${createdAccount.id}`, {
      status: 'disabled',
      imageGenerationEnabled: true
    }, cookie)
    assert.equal(disabledAccount.status, 'disabled', 'performance smoke 应能更新系统账户')

    console.log(`[performance-system-api-smoke:${label}] providers`)
    const providers = await getEnvelope<Array<{ code: string; enabled: boolean }>>(baseUrl, '/__aisys__/api/providers', cookie)
    assert.ok(providers.some((provider) => provider.code === 'gpt' && provider.enabled), 'performance smoke 应能读取启用 GPT 供应商')

    console.log(`[performance-system-api-smoke:${label}] providers/options`)
    const providerOptions = await getEnvelope<Array<{ code: string }>>(baseUrl, '/__aisys__/api/providers/options', cookie)
    assert.ok(providerOptions.some((provider) => provider.code === 'gpt'), 'performance smoke 应能读取供应商选项')

    console.log(`[performance-system-api-smoke:${label}] groups`)
    const groups = await getEnvelope<{ items: Array<{ id: string; name: string; providerCode: string }>; hasMore: boolean }>(baseUrl, '/__aisys__/api/groups?page=1&pageSize=20', cookie)
    assert.ok(groups.items.some((group) => group.providerCode === 'gpt'), 'performance smoke 应能读取分组列表')
    const groupOptions = await getEnvelope<Array<{ id: string; providerCode: string }>>(baseUrl, '/__aisys__/api/groups/options?providerCode=gpt&limit=10', cookie)
    assert.ok(groupOptions.some((group) => group.providerCode === 'gpt'), 'performance smoke 应能读取分组选项')
    const groupSuffix = `${label}${Date.now()}${Math.random().toString(16).slice(2, 6)}`
    const createdGroup = await postEnvelope<{ id: string; name: string; groupType: string }>(baseUrl, '/__aisys__/api/groups', {
      name: `烟测分组${groupSuffix}`,
      providerCode: 'gpt',
      description: 'performance smoke group',
      enabled: true,
      groupType: 'high_concurrency',
      schedulingPolicy: {
        defaultSoftConcurrency: 10,
        maxQueueWaitMs: 30_000,
        clientIpConcurrencyLimit: 2,
        clientIpConcurrencyOverflowMode: 'queue',
        imageLaneMaxConcurrency: 1
      }
    }, cookie)
    createdGroupIds.push(createdGroup.id)
    assert.equal(createdGroup.groupType, 'high_concurrency', 'performance smoke 应能创建高并发分组')
    const updatedGroup = await patchEnvelope<{ id: string; name: string; enabled: boolean }>(baseUrl, `/__aisys__/api/groups/${createdGroup.id}`, {
      name: `烟测分组${groupSuffix}改`
    }, cookie)
    assert.equal(updatedGroup.name, `烟测分组${groupSuffix}改`, 'performance smoke 应能更新分组名称')

    console.log(`[performance-system-api-smoke:${label}] accounts`)
    const aiAccountSuffix = `${label}${Date.now()}${Math.random().toString(16).slice(2, 6)}`
    const smokeModel = 'gpt-5-mini'
    const createdAiAccount = await postEnvelope<{
      id: string
      name: string
      status: string
      boundGroupId?: string
      supportedModels?: string[]
      modelMappings?: Array<{ sourceModel: string; upstreamModel: string }>
    }>(baseUrl, '/__aisys__/api/accounts', {
      name: `烟测AI账户${aiAccountSuffix}`,
      providerCode: 'gpt',
      type: 'api_key',
      status: 'temporary_unavailable',
      groupId: createdGroup.id,
      credentials: {
        api_key: `sk-smoke-account-${aiAccountSuffix}`,
        base_url: 'https://example.invalid/v1'
      },
      supportedModels: [smokeModel],
      modelMappings: [{
        sourceModel: smokeModel,
        sourceEndpointFamily: 'responses',
        upstreamModel: smokeModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }],
      concurrencyLimit: 20
    }, cookie)
    createdAiAccountIds.push(createdAiAccount.id)
    assert.equal(createdAiAccount.status, 'temporary_unavailable', 'performance smoke 应能创建临时不可用 AI 账户')
    assert.equal(createdAiAccount.boundGroupId, createdGroup.id, 'performance smoke AI 账户应绑定目标分组')
    assert.deepEqual(createdAiAccount.supportedModels, [smokeModel], 'performance smoke AI 账户应保存支持模型')
    assert.equal(createdAiAccount.modelMappings?.[0]?.sourceModel, smokeModel, 'performance smoke AI 账户应保存模型映射')
    const aiAccountDetail = await getEnvelope<{
      id: string
      status: string
      boundGroupId?: string
      credentials?: Record<string, unknown>
      supportedModels?: string[]
      modelMappings?: Array<{ sourceModel: string; upstreamModel: string }>
    }>(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, cookie)
    assert.equal(aiAccountDetail.id, createdAiAccount.id, 'performance smoke 应能读取新建 AI 账户详情')
    assert.equal(aiAccountDetail.status, 'temporary_unavailable', 'performance smoke AI 账户详情应保留状态')
    assert.equal(aiAccountDetail.boundGroupId, createdGroup.id, 'performance smoke AI 账户详情应保留分组绑定')
    assert.equal(aiAccountDetail.credentials?.base_url, 'https://example.invalid/v1', 'performance smoke AI 账户详情应返回公开凭据字段')
    assert.deepEqual(aiAccountDetail.supportedModels, [smokeModel], 'performance smoke AI 账户详情应返回支持模型')
    assert.equal(aiAccountDetail.modelMappings?.[0]?.upstreamModel, smokeModel, 'performance smoke AI 账户详情应返回模型映射')
    const restoredAiAccount = await patchEnvelope<{ id: string; status: string; schedulable: boolean }>(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, {
      clearFailureState: true
    }, cookie)
    assert.equal(restoredAiAccount.status, 'active', 'performance smoke 应能恢复 AI 账户异常状态')
    assert.equal(restoredAiAccount.schedulable, true, 'performance smoke 恢复异常状态后账户应参与调度')
    const updatedAiAccountName = `${createdAiAccount.name}改`
    const updatedAiAccount = await patchEnvelope<{
      id: string
      name: string
      notes?: string
      priority: number
      boundGroupId?: string
      supportedModels?: string[]
      tags?: Array<{ name: string }>
      modelMappings?: Array<{ sourceModel: string; upstreamModel: string }>
    }>(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, {
      name: updatedAiAccountName,
      notes: 'performance smoke account updated',
      priority: 7,
      tags: ['烟测标签'],
      groupId: createdGroup.id,
      supportedModels: [smokeModel],
      modelMappings: [{
        sourceModel: smokeModel,
        sourceEndpointFamily: 'responses',
        upstreamModel: smokeModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }]
    }, cookie)
    assert.equal(updatedAiAccount.name, updatedAiAccountName, 'performance smoke 应能更新 AI 账户名称')
    assert.equal(updatedAiAccount.notes, 'performance smoke account updated', 'performance smoke 应能更新 AI 账户备注')
    assert.equal(updatedAiAccount.priority, 7, 'performance smoke 应能更新 AI 账户优先级')
    assert.equal(updatedAiAccount.boundGroupId, createdGroup.id, 'performance smoke 更新 AI 账户应保留分组绑定')
    assert.deepEqual(updatedAiAccount.supportedModels, [smokeModel], 'performance smoke 更新 AI 账户应保留支持模型')
    assert.ok(updatedAiAccount.tags?.some((tag) => tag.name === '烟测标签'), 'performance smoke 应能更新 AI 账户标签')
    const aiAccounts = await getEnvelope<{ items: Array<{ id: string; boundGroupId?: string; supportedModels?: string[] }> }>(
      baseUrl,
      `/__aisys__/api/accounts?keyword=${encodeURIComponent(updatedAiAccount.name)}&groupId=${encodeURIComponent(createdGroup.id)}&page=1&pageSize=20`,
      cookie
    )
    const listedAiAccount = aiAccounts.items.find((item) => item.id === createdAiAccount.id)
    assert.ok(listedAiAccount, 'performance smoke 应能在 AI 账户列表查回新建账户')
    assert.equal(listedAiAccount.boundGroupId, createdGroup.id, 'performance smoke AI 账户列表应保留分组绑定')
    assert.deepEqual(listedAiAccount.supportedModels, [smokeModel], 'performance smoke AI 账户列表应返回支持模型')
    const aiAccountOptions = await getEnvelope<Array<{ id: string; providerCode: string }>>(
      baseUrl,
      `/__aisys__/api/accounts/options?keyword=${encodeURIComponent(updatedAiAccount.name)}&groupId=${encodeURIComponent(createdGroup.id)}&limit=20`,
      cookie
    )
    assert.ok(aiAccountOptions.some((item) => item.id === createdAiAccount.id && item.providerCode === 'gpt'), 'performance smoke 应能在 AI 账户 options 查回新建账户')
    await deleteNoContent(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, cookie)
    await expectStatus(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, cookie, 404, 'performance smoke 删除 AI 账户后详情应不可见')

    console.log(`[performance-system-api-smoke:${label}] api-keys`)
    const apiKeySuffix = `${label}${Date.now()}${Math.random().toString(16).slice(2, 6)}`
    const createdApiKey = await postEnvelope<{ id: string; name: string; key: string; groupBindings: Array<{ groupId: string; weight: number }> }>(baseUrl, '/__aisys__/api/api-keys', {
      name: `烟测APIKey${apiKeySuffix}`,
      description: 'performance smoke api key',
      groupBindings: [{ groupId: createdGroup.id, priority: 1, weight: 10, status: 'active' }],
      status: 'active'
    }, cookie)
    createdApiKeyIds.push(createdApiKey.id)
    assert.ok(createdApiKey.key.startsWith('sk-'), 'performance smoke 应能创建 API Key 并返回一次性明文')
    assert.equal(createdApiKey.groupBindings[0]?.groupId, createdGroup.id, 'performance smoke API Key 应绑定目标分组')
    const apiKeys = await getEnvelope<{ items: Array<{ id: string; name: string }> }>(baseUrl, `/__aisys__/api/api-keys?keyword=${encodeURIComponent(`烟测APIKey${apiKeySuffix}`)}&page=1&pageSize=20`, cookie)
    assert.ok(apiKeys.items.some((item) => item.id === createdApiKey.id), 'performance smoke 应能读取 API Key 列表')
    const secret = await getEnvelope<{ key: string }>(baseUrl, `/__aisys__/api/api-keys/${createdApiKey.id}/secret`, cookie)
    assert.equal(secret.key, createdApiKey.key, 'performance smoke 应能读取 API Key 完整密钥')
    const updatedApiKey = await patchEnvelope<{ id: string; status: string; groupBindings: Array<{ weight: number }> }>(baseUrl, `/__aisys__/api/api-keys/${createdApiKey.id}`, {
      status: 'disabled',
      groupBindings: [{ groupId: createdGroup.id, priority: 1, weight: 20, status: 'active' }]
    }, cookie)
    assert.equal(updatedApiKey.status, 'disabled', 'performance smoke 应能更新 API Key')
    assert.equal(updatedApiKey.groupBindings[0]?.weight, 20, 'performance smoke 应能更新 API Key 分组权重')
    const refreshedApiKey = await postOkEnvelope<{ id: string; key: string }>(baseUrl, `/__aisys__/api/api-keys/${createdApiKey.id}/refresh-key`, {}, cookie)
    assert.ok(refreshedApiKey.key.startsWith('sk-'), 'performance smoke 应能刷新 API Key 密钥')
    assert.notEqual(refreshedApiKey.key, createdApiKey.key, 'performance smoke 刷新 API Key 应返回新密钥')
    await deleteNoContent(baseUrl, `/__aisys__/api/api-keys/${createdApiKey.id}`, cookie)
    createdApiKeyIds.splice(createdApiKeyIds.indexOf(createdApiKey.id), 1)

    const disabledGroup = await patchEnvelope<{ id: string; enabled: boolean }>(baseUrl, `/__aisys__/api/groups/${createdGroup.id}`, {
      enabled: false
    }, cookie)
    assert.equal(disabledGroup.enabled, false, 'performance smoke 应能更新分组启用状态')
    await deleteNoContent(baseUrl, `/__aisys__/api/groups/${createdGroup.id}`, cookie)
    createdGroupIds.splice(createdGroupIds.indexOf(createdGroup.id), 1)

    console.log(`[performance-system-api-smoke:${label}] logout`)
    const logoutResponse = await fetch(`${baseUrl}/__aisys__/api/auth/logout`, {
      method: 'POST',
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    assert.equal(logoutResponse.status, 200, `performance smoke 登出应成功，实际 HTTP ${logoutResponse.status}: ${await logoutResponse.text()}`)
  } finally {
    await closeServer(server)
    await cleanupCreatedApiKeys(createdApiKeyIds)
    await cleanupCreatedAiAccounts(createdAiAccountIds)
    await cleanupCreatedGroups(createdGroupIds)
    await cleanupCreatedSystemAccounts(createdSystemAccountIds)
  }
}

interface ApiEnvelope<T> {
  data: T
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie?: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    ...(cookie ? { headers: { cookie } } : {}),
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function patchEnvelope<T = unknown>(baseUrl: string, path: string, body: Record<string, unknown>, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: {
      cookie,
      'content-type': 'application/json'
    },
    signal: AbortSignal.timeout(10_000),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} PATCH 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function postEnvelope<T = unknown>(baseUrl: string, path: string, body: Record<string, unknown>, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      cookie,
      'content-type': 'application/json'
    },
    signal: AbortSignal.timeout(10_000),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 201, `${path} POST 应返回 HTTP 201，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function postOkEnvelope<T = unknown>(baseUrl: string, path: string, body: Record<string, unknown>, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      cookie,
      'content-type': 'application/json'
    },
    signal: AbortSignal.timeout(10_000),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} POST 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function deleteNoContent(baseUrl: string, path: string, cookie: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'DELETE',
    headers: { cookie },
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 204, `${path} DELETE 应返回 HTTP 204，实际 HTTP ${response.status}: ${text}`)
}

async function expectStatus(baseUrl: string, path: string, cookie: string, expectedStatus: number, message: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    headers: { cookie },
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, expectedStatus, `${message}，实际 HTTP ${response.status}: ${text}`)
}

async function cleanupCreatedApiKeys(apiKeyIds: string[]): Promise<void> {
  if (!apiKeyIds.length) {
    return
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of apiKeyIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."api_key_group_bindings" WHERE api_key_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."api_keys" WHERE id = ?', [id])
    }
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of apiKeyIds.splice(0)) {
    database.prepare('DELETE FROM api_key_group_bindings WHERE api_key_id = ?').run(id)
    database.prepare('DELETE FROM api_keys WHERE id = ?').run(id)
  }
}

async function cleanupCreatedGroups(groupIds: string[]): Promise<void> {
  if (!groupIds.length) {
    return
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of groupIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."api_key_group_bindings" WHERE group_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."groups" WHERE id = ?', [id])
    }
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of groupIds.splice(0)) {
    database.prepare('DELETE FROM api_key_group_bindings WHERE group_id = ?').run(id)
    database.prepare('DELETE FROM groups WHERE id = ?').run(id)
  }
}

async function cleanupCreatedAiAccounts(accountIds: string[]): Promise<void> {
  if (!accountIds.length) {
    return
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of accountIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."group_accounts" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_supported_models" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_model_mappings" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_tag_bindings" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_name_search_terms" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_name_search_documents" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_api_key_runtime_states" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."accounts" WHERE id = ?', [id])
    }
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of accountIds.splice(0)) {
    database.prepare('DELETE FROM group_accounts WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_supported_models WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_model_mappings WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_tag_bindings WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_name_search_terms WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_name_search_documents WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_api_key_runtime_states WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM accounts WHERE id = ?').run(id)
  }
}

async function cleanupCreatedSystemAccounts(systemAccountIds: string[]): Promise<void> {
  if (!systemAccountIds.length) {
    return
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { closePostgresPool, getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of systemAccountIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."groups" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."system_sessions" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."system_accounts" WHERE id = ?', [id])
    }
    await closePostgresPool()
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of systemAccountIds.splice(0)) {
    database.prepare('DELETE FROM groups WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM system_sessions WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM system_accounts WHERE id = ?').run(id)
  }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
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
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve())
  })
}

async function closeSqliteStorageDatabases(): Promise<void> {
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may fail before SQLite storage is imported.
  }
}

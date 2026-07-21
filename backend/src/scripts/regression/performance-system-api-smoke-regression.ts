import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import http from 'node:http'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { DatabaseClient } from '../../storage/database-client.js'

interface AuthorizationUsageSeed {
  authorizationId: string
  authorizationSourceId: string
  grantId: string
  statsSystemAccountId: string
  scopeType: string
  scopeId: string
  startDate: string
  endDate: string
}

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
  process.env.JUHE_AI_QUEUE_DRIVER = 'memory'
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

  const postgresSmokeEnv = performanceSystemApiPostgresSmokeEnv()
  if (postgresSmokeEnv) {
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
        JUHE_AI_LOG_FILE_ENABLED: 'true',
        JUHE_AI_LOG_DIR: join(tempRoot, 'postgres-logs'),
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_QUEUE_DRIVER: 'redis_stream',
        ...postgresSmokeEnv
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

function performanceSystemApiPostgresSmokeEnv(): Record<string, string> | undefined {
  const postgresUrl = process.env.JUHE_PERFORMANCE_SYSTEM_API_POSTGRES_URL?.trim()
  if (!postgresUrl) {
    return undefined
  }
  const required = {
    JUHE_AI_POSTGRES_URL: postgresUrl,
    JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_PERFORMANCE_SYSTEM_API_REDIS_CACHE_URL?.trim(),
    JUHE_AI_REDIS_STATE_URL: process.env.JUHE_PERFORMANCE_SYSTEM_API_REDIS_STATE_URL?.trim(),
    JUHE_AI_REDIS_QUEUE_URL: process.env.JUHE_PERFORMANCE_SYSTEM_API_REDIS_QUEUE_URL?.trim()
  }
  const missing = Object.entries(required)
    .filter(([, value]) => !value)
    .map(([key]) => key)
  if (missing.length > 0) {
    throw new Error(`performance-system-api-smoke PostgreSQL 子进程缺少必要环境变量：${missing.join(', ')}`)
  }
  return required as Record<string, string>
}

async function getEnvelopeWithHeaders<T>(baseUrl: string, path: string, cookie?: string): Promise<{ data: T; headers: Headers }> {
  const response = await fetch(`${baseUrl}${path}`, {
    ...(cookie ? { headers: { cookie } } : {}),
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  return {
    data: (JSON.parse(text) as ApiEnvelope<T>).data,
    headers: response.headers
  }
}

async function postCreatedEnvelopeWithHeaders<T = unknown>(
  baseUrl: string,
  path: string,
  body: Record<string, unknown>,
  cookie: string
): Promise<{ data: T; headers: Headers }> {
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
  return {
    data: (JSON.parse(text) as ApiEnvelope<T>).data,
    headers: response.headers
  }
}

async function postOkEnvelopeWithHeaders<T = unknown>(
  baseUrl: string,
  path: string,
  body: Record<string, unknown>,
  cookie: string
): Promise<{ data: T; headers: Headers }> {
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
  return {
    data: (JSON.parse(text) as ApiEnvelope<T>).data,
    headers: response.headers
  }
}

function assertNoStoreResponseHeaders(headers: Headers, label: string): void {
  assert.equal(headers.get('cache-control'), 'no-store', `${label} 应设置 Cache-Control: no-store`)
  assert.equal(headers.get('pragma'), 'no-cache', `${label} 应设置 Pragma: no-cache`)
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
  const createdRouteStrategyIds: string[] = []
  const createdCustomModelIds: string[] = []
  const createdAuthorizationUsageSeeds: AuthorizationUsageSeed[] = []
  const createdAuthorizationGrantIds: string[] = []

  let server: http.Server | undefined
  try {
    console.log(`[performance-system-api-smoke:${label}] start app`)
    const app = createSystemApiApp({
      systemApiPrefix: '/__aisys__/api',
      trustProxy: true,
      bypassSystemApiRateLimitForTest: true
    })
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
    const systemSettings = await getEnvelope<{ systemApiRateLimitIpReadPerMinute: number }>(baseUrl, '/__aisys__/api/settings', cookie)
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
    const providers = await getEnvelope<Array<{
      code: string
      enabled: boolean
      protocolProfiles?: Array<{ id: string; enabled: boolean }>
    }>>(baseUrl, '/__aisys__/api/providers', cookie)
    const gptProvider = providers.find((provider) => provider.code === 'gpt' && provider.enabled)
    assert.ok(gptProvider, 'performance smoke 应能读取启用 GPT 供应商')
    const gptProviderProfileId = gptProvider.protocolProfiles?.find((profile) => profile.enabled)?.id
    assert.ok(gptProviderProfileId, 'performance smoke 应能读取 GPT 默认协议档案')

    console.log(`[performance-system-api-smoke:${label}] providers/options`)
    const providerOptions = await getEnvelope<Array<{ code: string }>>(baseUrl, '/__aisys__/api/providers/options', cookie)
    assert.ok(providerOptions.some((provider) => provider.code === 'gpt'), 'performance smoke 应能读取供应商选项')

    console.log(`[performance-system-api-smoke:${label}] provider models`)
    const customModelTarget = await postEnvelope<{ id: string; model: string; providerCode: string; scope: string }>(baseUrl, '/__aisys__/api/providers/gpt/models', {
      model: `smoke-custom-target-${suffix}`,
      supportedApiProtocols: ['responses'],
      inputUsdPer1M: 1,
      outputUsdPer1M: 2,
      maxOutputTokens: 4096
    }, cookie)
    createdCustomModelIds.push(customModelTarget.id)
    assert.equal(customModelTarget.providerCode, 'gpt', 'performance smoke 自定义模型应归属目标供应商')
    assert.equal(customModelTarget.scope, 'personal', 'performance smoke 自定义模型应固定为个人模型')
    const customModelAlias = await postEnvelope<{ id: string; model: string; inputUsdPer1M?: number; outputUsdPer1M?: number }>(baseUrl, '/__aisys__/api/providers/gpt/models', {
      model: `smoke-custom-alias-${suffix}`,
      supportedApiProtocols: ['responses'],
      inputUsdPer1M: 1,
      outputUsdPer1M: 2
    }, cookie)
    createdCustomModelIds.push(customModelAlias.id)
    assert.equal(customModelAlias.inputUsdPer1M, 1, 'performance smoke 别名模型应保存自身输入价格')
    assert.equal(customModelAlias.outputUsdPer1M, 2, 'performance smoke 别名模型应保存自身输出价格')
    const customModels = await getEnvelope<Array<{ id?: string; model: string; scope: string }>>(baseUrl, '/__aisys__/api/providers/gpt/models?includeInactive=true&includeUnpriced=true', cookie)
    assert.ok(customModels.some((item) => item.id === customModelTarget.id && item.model === customModelTarget.model), 'performance smoke 应能在模型目录列表查回自定义目标模型')
    assert.ok(customModels.some((item) => item.id === customModelAlias.id && item.model === customModelAlias.model), 'performance smoke 应能在模型目录列表查回自定义别名模型')
    const updatedCustomModel = await patchEnvelope<{ id: string; maxOutputTokens?: number }>(baseUrl, `/__aisys__/api/providers/gpt/models/${customModelAlias.id}`, {
      maxOutputTokens: 2048
    }, cookie)
    assert.equal(updatedCustomModel.maxOutputTokens, 2048, 'performance smoke 应能更新自定义模型')
    await deleteNoContentEnvelope(baseUrl, `/__aisys__/api/providers/gpt/models/${customModelAlias.id}`, cookie)
    createdCustomModelIds.splice(createdCustomModelIds.indexOf(customModelAlias.id), 1)
    await deleteNoContentEnvelope(baseUrl, `/__aisys__/api/providers/gpt/models/${customModelTarget.id}`, cookie)
    createdCustomModelIds.splice(createdCustomModelIds.indexOf(customModelTarget.id), 1)

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

    console.log(`[performance-system-api-smoke:${label}] authorization options`)
    const granteeAccounts = await getEnvelope<Array<{ id: string; username: string }>>(baseUrl, '/__aisys__/api/authorization-options/grantee-accounts?ids=sys_admin', cookie)
    assert.deepEqual(granteeAccounts.map((account) => account.id), ['sys_admin'], 'performance smoke 应能读取授权被授权账号选项')
    const granteeTeams = await getEnvelope<Array<{ id: string; name: string }>>(baseUrl, '/__aisys__/api/authorization-options/grantee-teams?limit=5', cookie)
    assert.ok(Array.isArray(granteeTeams), 'performance smoke 应能读取授权被授权团队选项')
    const granteeGroups = await getEnvelope<Array<{ id: string; providerCode: string }>>(
      baseUrl,
      '/__aisys__/api/authorization-options/grantee-groups?granteeSystemAccountId=sys_admin&providerCode=gpt&preferDefault=true&limit=20',
      cookie
    )
    assert.ok(granteeGroups.some((group) => group.id === createdGroup.id && group.providerCode === 'gpt'), 'performance smoke 应能读取授权目标分组选项')

    console.log(`[performance-system-api-smoke:${label}] authorizations`)
    const authorizationsPage = await getEnvelope<{ items: Array<{ id: string }>; page: number; pageSize: number; hasMore: boolean }>(
      baseUrl,
      '/__aisys__/api/authorizations?status=all&page=1&pageSize=5',
      cookie
    )
    assert.equal(authorizationsPage.page, 1, 'performance smoke 应能读取统一授权列表分页')
    assert.equal(authorizationsPage.pageSize, 5, 'performance smoke 统一授权列表应保留 pageSize')
    const myAuthorizationsPage = await getEnvelope<{ items: Array<{ id: string }>; page: number }>(
      baseUrl,
      '/__aisys__/api/my-authorizations?status=all&page=1&pageSize=5',
      cookie
    )
    assert.equal(myAuthorizationsPage.page, 1, 'performance smoke 应能读取我的授权列表')

    console.log(`[performance-system-api-smoke:${label}] accounts`)
    const aiAccountSuffix = `${label}${Date.now()}${Math.random().toString(16).slice(2, 6)}`
    const smokeModel = 'gpt-5-mini'
    const smokeFallbackModel = 'gpt-5-nano'
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
      providerProtocolProfileId: gptProviderProfileId,
      type: 'api_key',
      status: 'disabled',
      groupId: createdGroup.id,
      credentials: {
        api_key: `sk-smoke-account-${aiAccountSuffix}`,
        base_url: 'https://example.invalid/v1'
      },
      supportedModels: [smokeModel],
      healthCheckModel: smokeModel,
      modelMappings: [{
        sourceModel: smokeFallbackModel,
        sourceEndpointFamily: 'chat_completions',
        upstreamModel: smokeModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }],
      concurrencyLimit: 20
    }, cookie)
    createdAiAccountIds.push(createdAiAccount.id)
    assert.equal(createdAiAccount.status, 'disabled', 'performance smoke 应能创建停用 AI 账户，避免依赖后台首次健康检查')
    assert.equal(createdAiAccount.boundGroupId, createdGroup.id, 'performance smoke AI 账户应绑定目标分组')
    assert.deepEqual(createdAiAccount.supportedModels, [smokeModel], 'performance smoke AI 账户应保存支持模型')
    assert.equal(createdAiAccount.modelMappings?.[0]?.sourceModel, smokeFallbackModel, 'performance smoke AI 账户应保存同协议模型别名映射')
    const temporarilyUnavailableAiAccount = await patchEnvelope<{ id: string; status: string }>(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, {
      status: 'temporary_unavailable'
    }, cookie)
    assert.equal(temporarilyUnavailableAiAccount.status, 'temporary_unavailable', 'performance smoke 应能把停用 AI 账户切换为临时不可用')
    const aiAccountBasicDetail = await getEnvelope<{
      id: string
      status: string
      boundGroupId?: string
      credentials?: Record<string, unknown>
      supportedModels?: string[]
      modelMappings?: Array<{ sourceModel: string; upstreamModel: string }>
    }>(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, cookie)
    assert.equal(aiAccountBasicDetail.id, createdAiAccount.id, 'performance smoke 应能读取新建 AI 账户基础详情')
    assert.equal(aiAccountBasicDetail.status, 'temporary_unavailable', 'performance smoke AI 账户基础详情应保留状态')
    assert.equal(aiAccountBasicDetail.boundGroupId, createdGroup.id, 'performance smoke AI 账户基础详情应保留分组绑定')
    assert.equal(Object.prototype.hasOwnProperty.call(aiAccountBasicDetail, 'credentials'), false, 'performance smoke AI 账户基础详情不应返回凭据')
    assert.equal(Object.prototype.hasOwnProperty.call(aiAccountBasicDetail, 'supportedModels'), false, 'performance smoke AI 账户基础详情不应返回支持模型')
    assert.equal(Object.prototype.hasOwnProperty.call(aiAccountBasicDetail, 'modelMappings'), false, 'performance smoke AI 账户基础详情不应返回模型映射')
    const aiAccountDetail = await getEnvelope<{
      id: string
      status: string
      boundGroupId?: string
      credentials?: Record<string, unknown>
      supportedModels?: string[]
      modelMappings?: Array<{ sourceModel: string; upstreamModel: string }>
    }>(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}/advanced`, cookie)
    assert.equal(aiAccountDetail.id, createdAiAccount.id, 'performance smoke 应能读取新建 AI 账户详情')
    assert.equal(aiAccountDetail.status, 'temporary_unavailable', 'performance smoke AI 账户详情应保留状态')
    assert.equal(aiAccountDetail.boundGroupId, createdGroup.id, 'performance smoke AI 账户详情应保留分组绑定')
    assert.equal(aiAccountDetail.credentials?.base_url, 'https://example.invalid/v1', 'performance smoke AI 账户详情应返回公开凭据字段')
    assert.deepEqual(aiAccountDetail.supportedModels, [smokeModel], 'performance smoke AI 账户详情应返回支持模型')
    assert.equal(aiAccountDetail.modelMappings?.[0]?.upstreamModel, smokeModel, 'performance smoke AI 账户详情应返回模型映射')
    const restoredAiAccount = await patchEnvelope<{ id: string; status: string; schedulable: boolean }>(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, {
      clearFailureState: true
    }, cookie)
    assert.equal(restoredAiAccount.status, 'active', 'performance smoke 应能恢复 AI 账户临时不可调用状态')
    assert.equal(restoredAiAccount.schedulable, true, 'performance smoke 恢复临时不可调用状态后账户应参与调度')
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
        sourceModel: smokeFallbackModel,
        sourceEndpointFamily: 'chat_completions',
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
    assert.equal(Object.prototype.hasOwnProperty.call(listedAiAccount, 'supportedModels'), false, 'performance smoke AI 账户列表不应返回编辑专用支持模型')
    const aiAccountOptions = await getEnvelope<Array<{ id: string; providerCode: string }>>(
      baseUrl,
      `/__aisys__/api/accounts/options?keyword=${encodeURIComponent(updatedAiAccount.name)}&groupId=${encodeURIComponent(createdGroup.id)}&limit=20`,
      cookie
    )
    assert.ok(aiAccountOptions.some((item) => item.id === createdAiAccount.id && item.providerCode === 'gpt'), 'performance smoke 应能在 AI 账户 options 查回新建账户')
    const activeUsageGrantee = await patchEnvelope<{ id: string; status: string }>(baseUrl, `/__aisys__/api/system-accounts/${createdAccount.id}`, {
      status: 'active'
    }, cookie)
    assert.equal(activeUsageGrantee.status, 'active', 'performance smoke 授权 usage 夹具应使用启用的被授权账号')
    const createdAccountGranteeGroups = await getEnvelope<Array<{ id: string; providerCode: string }>>(
      baseUrl,
      `/__aisys__/api/authorization-options/grantee-groups?granteeSystemAccountId=${encodeURIComponent(createdAccount.id)}&providerCode=gpt&preferDefault=true&limit=20`,
      cookie
    )
    const createdAccountTargetGroup = createdAccountGranteeGroups.find((group) => group.providerCode === 'gpt')
    assert.ok(createdAccountTargetGroup?.id, 'performance smoke 授权创建应能读取被授权账号目标分组')
    const createdAuthorization = await postEnvelope<{
      id: string
      status: string
      resourceId: string
      granteeSystemAccountId?: string
    }>(baseUrl, '/__aisys__/api/authorizations?systemAccountId=sys_admin', {
      resourceType: 'account',
      resourceId: createdAiAccount.id,
      granteeType: 'system_account',
      granteeId: createdAccount.id,
      targetGroupId: createdAccountTargetGroup.id,
      remark: 'performance smoke authorization create',
      limits: {
        daily: {
          enabled: true,
          limit: 1
        }
      }
    }, cookie)
    createdAuthorizationGrantIds.push(createdAuthorization.id)
    assert.equal(createdAuthorization.status, 'active', 'performance smoke 应能创建个人资源授权')
    assert.equal(createdAuthorization.resourceId, createdAiAccount.id, 'performance smoke 创建授权应指向目标 AI 账户')
    assert.equal(createdAuthorization.granteeSystemAccountId, createdAccount.id, 'performance smoke 创建授权应指向被授权账号')
    const authorizedInstanceId = await markCreatedAuthorizationInstanceUnavailable(createdAuthorization.id, createdAccount.id)
    const restoredAuthorizedAccount = await patchEnvelope<{ id: string; status: string; schedulable: boolean; accessType?: string }>(
      baseUrl,
      `/__aisys__/api/accounts/${authorizedInstanceId}/authorized-dispatch?systemAccountId=${encodeURIComponent(createdAccount.id)}`,
      { clearFailureState: true },
      cookie
    )
    assert.equal(restoredAuthorizedAccount.id, authorizedInstanceId, 'performance smoke 授权实例恢复应返回目标账户')
    assert.equal(restoredAuthorizedAccount.accessType, 'authorized', 'performance smoke 授权实例恢复应保持授权视图')
    assert.equal(restoredAuthorizedAccount.status, 'active', 'performance smoke 应能恢复授权实例异常状态')
    assert.equal(restoredAuthorizedAccount.schedulable, true, 'performance smoke 授权实例恢复后应参与调度')
    const revokedCreatedAuthorization = await deleteEnvelope<{ id: string; status: string }>(baseUrl, `/__aisys__/api/authorizations/${createdAuthorization.id}`, cookie)
    assert.equal(revokedCreatedAuthorization.status, 'revoked', 'performance smoke 新建授权应能回收')
    await cleanupCreatedAuthorizationGrants(createdAuthorizationGrantIds)
    const authorizationUsageSeed = await seedAuthorizationUsageDetail(createdAiAccount.id, createdAccount.id)
    createdAuthorizationUsageSeeds.push(authorizationUsageSeed)
    const authorizationUsage = await getEnvelope<{
      id: string
      usage: { requestCount: number; inputTokens: number; outputTokens: number; totalCost: number; lastUsedAt?: string }
      usageBySystemAccount: Array<{ systemAccountId: string; requestCount: number; rangeUsage: { requestCount: number } }>
      usageBySystemAccountPage: number
      usageBySystemAccountPageSize: number
      usageBySystemAccountHasMore: boolean
      usageRange?: { startDate: string; endDate: string }
    }>(
      baseUrl,
      `/__aisys__/api/authorizations/${authorizationUsageSeed.grantId}/usage?startDate=${authorizationUsageSeed.startDate}&endDate=${authorizationUsageSeed.endDate}&page=1&pageSize=5`,
      cookie
    )
    assert.equal(authorizationUsage.id, authorizationUsageSeed.grantId, 'performance smoke 授权 usage 详情应返回目标授权')
    assert.equal(authorizationUsage.usage.requestCount, 17, 'performance smoke 授权 usage 详情应读取统计范围窗口')
    assert.equal(authorizationUsage.usage.inputTokens, 170, 'performance smoke 授权 usage 详情应返回输入 token 汇总')
    assert.equal(authorizationUsage.usage.outputTokens, 34, 'performance smoke 授权 usage 详情应返回输出 token 汇总')
    assert.equal(authorizationUsage.usageBySystemAccount[0]?.systemAccountId, createdAccount.id, 'performance smoke 授权 usage 明细应返回被授权账号')
    assert.equal(authorizationUsage.usageBySystemAccount[0]?.rangeUsage.requestCount, 17, 'performance smoke 授权 usage 明细应复用范围窗口用量')
    assert.equal(authorizationUsage.usageBySystemAccountPage, 1, 'performance smoke 授权 usage 明细应返回页码')
    assert.equal(authorizationUsage.usageBySystemAccountPageSize, 5, 'performance smoke 授权 usage 明细应返回 pageSize')
    assert.equal(authorizationUsage.usageBySystemAccountHasMore, false, 'performance smoke 单个个人授权 usage 明细不应存在下一页')
    assert.equal(authorizationUsage.usageRange?.startDate, authorizationUsageSeed.startDate, 'performance smoke 授权 usage 应返回查询起始日期')
    const pausedAuthorization = await patchEnvelope<{ id: string; status: string }>(baseUrl, `/__aisys__/api/authorizations/${authorizationUsageSeed.grantId}`, {
      status: 'paused'
    }, cookie)
    assert.equal(pausedAuthorization.id, authorizationUsageSeed.grantId, 'performance smoke 应能更新已存在授权')
    assert.equal(pausedAuthorization.status, 'paused', 'performance smoke 授权更新后应暂停')
    const nextAuthorizationExpiresAt = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString()
    const expireUpdatedAuthorization = await patchEnvelope<{ id: string; status: string; expiresAt?: string | null; limits?: { hourly?: { hours?: number } } }>(
      baseUrl,
      `/__aisys__/api/authorizations/${authorizationUsageSeed.grantId}/expire`,
      {
        expiresAt: nextAuthorizationExpiresAt,
        limits: {
          hourly: {
            enabled: true,
            hours: 3,
            limit: 1
          }
        }
      },
      cookie
    )
    assert.equal(expireUpdatedAuthorization.status, 'paused', 'performance smoke 更新授权有效期不应改变暂停状态')
    assert.equal(expireUpdatedAuthorization.expiresAt, nextAuthorizationExpiresAt, 'performance smoke 应能更新授权有效期')
    assert.equal(expireUpdatedAuthorization.limits?.hourly?.hours, 3, 'performance smoke 应能更新授权小时额度窗口')
    const revokedAuthorization = await deleteEnvelope<{ id: string; status: string }>(baseUrl, `/__aisys__/api/authorizations/${authorizationUsageSeed.grantId}`, cookie)
    assert.equal(revokedAuthorization.id, authorizationUsageSeed.grantId, 'performance smoke 应能回收已存在授权')
    assert.equal(revokedAuthorization.status, 'revoked', 'performance smoke 授权回收后应为 revoked')
    await cleanupCreatedAuthorizationUsageSeeds(createdAuthorizationUsageSeeds)
    await deleteNoContent(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, cookie)
    await expectStatus(baseUrl, `/__aisys__/api/accounts/${createdAiAccount.id}`, cookie, 404, 'performance smoke 删除 AI 账户后详情应不可见')

    console.log(`[performance-system-api-smoke:${label}] api-keys`)
    const apiKeySuffix = `${label}${Date.now()}${Math.random().toString(16).slice(2, 6)}`
    const createdRouteStrategy = await postEnvelope<{ id: string; name: string; groupBindings: Array<{ groupId: string; weight: number }> }>(baseUrl, '/__aisys__/api/route-strategies', {
      name: `烟测普通路由${apiKeySuffix}`,
      description: 'performance smoke normal route strategy',
      mode: 'normal',
      groupBindings: [{ groupId: createdGroup.id, priority: 1, weight: 10, status: 'active' }],
      status: 'active'
    }, cookie)
    createdRouteStrategyIds.push(createdRouteStrategy.id)
    assert.equal(createdRouteStrategy.groupBindings[0]?.groupId, createdGroup.id, 'performance smoke 策略路由应绑定目标分组')
    const createdApiKeyResponse = await postCreatedEnvelopeWithHeaders<{ id: string; name: string; key: string; routeStrategyId: string }>(baseUrl, '/__aisys__/api/api-keys', {
      name: `烟测APIKey${apiKeySuffix}`,
      description: 'performance smoke api key',
      routeStrategyId: createdRouteStrategy.id,
      status: 'active'
    }, cookie)
    assertNoStoreResponseHeaders(createdApiKeyResponse.headers, 'API Key 创建响应')
    const createdApiKey = createdApiKeyResponse.data
    createdApiKeyIds.push(createdApiKey.id)
    assert.ok(createdApiKey.key.startsWith('sk-'), 'performance smoke 应能创建 API Key 并返回一次性明文')
    assert.equal(createdApiKey.routeStrategyId, createdRouteStrategy.id, 'performance smoke API Key 应绑定目标策略路由')
    const apiKeys = await getEnvelope<{ items: Array<{ id: string; name: string }> }>(baseUrl, `/__aisys__/api/api-keys?keyword=${encodeURIComponent(`烟测APIKey${apiKeySuffix}`)}&page=1&pageSize=20`, cookie)
    assert.ok(apiKeys.items.some((item) => item.id === createdApiKey.id), 'performance smoke 应能读取 API Key 列表')
    const secretResponse = await getEnvelopeWithHeaders<{ key: string }>(baseUrl, `/__aisys__/api/api-keys/${createdApiKey.id}/secret`, cookie)
    assertNoStoreResponseHeaders(secretResponse.headers, 'API Key secret 响应')
    const secret = secretResponse.data
    assert.equal(secret.key, createdApiKey.key, 'performance smoke 应能读取 API Key 完整密钥')
    const updatedRouteStrategy = await patchEnvelope<{ id: string; groupBindings: Array<{ weight: number }> }>(baseUrl, `/__aisys__/api/route-strategies/${createdRouteStrategy.id}`, {
      groupBindings: [{ groupId: createdGroup.id, priority: 1, weight: 20, status: 'active' }]
    }, cookie)
    assert.equal(updatedRouteStrategy.groupBindings[0]?.weight, 20, 'performance smoke 应能更新策略路由分组权重')
    const updatedApiKey = await patchEnvelope<{ id: string; status: string }>(baseUrl, `/__aisys__/api/api-keys/${createdApiKey.id}`, {
      status: 'disabled'
    }, cookie)
    assert.equal(updatedApiKey.status, 'disabled', 'performance smoke 应能更新 API Key')
    const refreshedApiKeyResponse = await postOkEnvelopeWithHeaders<{ id: string; key: string }>(baseUrl, `/__aisys__/api/api-keys/${createdApiKey.id}/refresh-key`, {}, cookie)
    assertNoStoreResponseHeaders(refreshedApiKeyResponse.headers, 'API Key 刷新响应')
    const refreshedApiKey = refreshedApiKeyResponse.data
    assert.ok(refreshedApiKey.key.startsWith('sk-'), 'performance smoke 应能刷新 API Key 密钥')
    assert.notEqual(refreshedApiKey.key, createdApiKey.key, 'performance smoke 刷新 API Key 应返回新密钥')
    await deleteNoContent(baseUrl, `/__aisys__/api/api-keys/${createdApiKey.id}`, cookie)
    createdApiKeyIds.splice(createdApiKeyIds.indexOf(createdApiKey.id), 1)
    await deleteNoContent(baseUrl, `/__aisys__/api/route-strategies/${createdRouteStrategy.id}`, cookie)
    createdRouteStrategyIds.splice(createdRouteStrategyIds.indexOf(createdRouteStrategy.id), 1)

    const hybridRouteStrategy = await postEnvelope<{
      id: string
      hybridRoutingConfig?: { scoringModel?: string; scoringTimeoutMs?: number; levelRoutes?: Array<{ targetModel: string }> }
    }>(baseUrl, '/__aisys__/api/route-strategies', {
      name: `烟测混合路由${apiKeySuffix}`,
      description: 'performance smoke hybrid route strategy',
      mode: 'hybrid_smart',
      groupBindings: [{ groupId: createdGroup.id, priority: 1, weight: 10, status: 'active' }],
      hybridRoutingConfig: {
        scoringModel: smokeModel,
        scoringContextMode: 'full_request',
        qualityPreference: 'balanced',
        scoringTimeoutMs: 10_000,
        scoringFallbackMaxLevel: 2,
        scoringCacheEnabled: true,
        scoringCacheTtlSeconds: 60,
        cacheAffinityEnabled: true,
        affinityTtlSeconds: 300,
        switchMinLevelDelta: 1,
        downgradeConsecutiveLowCount: 2,
        levelRoutes: [
          { minLevel: 1, maxLevel: 5, targetModel: smokeModel, enabled: true },
          { minLevel: 6, maxLevel: 10, targetModel: smokeFallbackModel, enabled: true }
        ],
        qualityInspection: {
          enabled: true,
          scoringModel: smokeModel,
          triggerMode: 'risk_based',
          maxTriggerLevel: 8,
          maxRetries: 1,
          failureAction: 'retry_same_model',
          unavailableAction: 'pass_through'
        }
      },
      status: 'active'
    }, cookie)
    createdRouteStrategyIds.push(hybridRouteStrategy.id)
    const hybridApiKey = await postEnvelope<{
      id: string
      routeStrategyId: string
      routeStrategyMode?: string
    }>(baseUrl, '/__aisys__/api/api-keys', {
      name: `烟测混合APIKey${apiKeySuffix}`,
      description: 'performance smoke hybrid api key',
      routeStrategyId: hybridRouteStrategy.id,
      status: 'active'
    }, cookie)
    createdApiKeyIds.push(hybridApiKey.id)
    assert.equal(hybridApiKey.routeStrategyId, hybridRouteStrategy.id, 'performance smoke 混合 API Key 应绑定混合策略路由')
    assert.equal(hybridApiKey.routeStrategyMode, 'hybrid_smart', 'performance smoke API Key 摘要应返回混合策略模式')
    assert.equal(hybridRouteStrategy.hybridRoutingConfig?.scoringModel, smokeModel, 'performance smoke 混合路由应通过模型目录校验并保存评分模型')
    assert.equal(hybridRouteStrategy.hybridRoutingConfig?.levelRoutes?.[0]?.targetModel, smokeModel, 'performance smoke 混合路由应保存等级目标模型')
    const updatedHybridRouteStrategy = await patchEnvelope<{ id: string; hybridRoutingConfig?: { scoringTimeoutMs?: number } }>(baseUrl, `/__aisys__/api/route-strategies/${hybridRouteStrategy.id}`, {
      hybridRoutingConfig: {
        ...hybridRouteStrategy.hybridRoutingConfig,
        scoringTimeoutMs: 12_000
      }
    }, cookie)
    assert.equal(updatedHybridRouteStrategy.hybridRoutingConfig?.scoringTimeoutMs, 12_000, 'performance smoke 应能更新混合路由配置')
    await deleteNoContent(baseUrl, `/__aisys__/api/api-keys/${hybridApiKey.id}`, cookie)
    createdApiKeyIds.splice(createdApiKeyIds.indexOf(hybridApiKey.id), 1)
    await deleteNoContent(baseUrl, `/__aisys__/api/route-strategies/${hybridRouteStrategy.id}`, cookie)
    createdRouteStrategyIds.splice(createdRouteStrategyIds.indexOf(hybridRouteStrategy.id), 1)

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
    await cleanupCreatedAuthorizationUsageSeeds(createdAuthorizationUsageSeeds)
    await cleanupCreatedAuthorizationGrants(createdAuthorizationGrantIds)
    await cleanupCreatedApiKeys(createdApiKeyIds)
    await cleanupCreatedRouteStrategies(createdRouteStrategyIds)
    await cleanupCreatedAiAccounts(createdAiAccountIds)
    await cleanupCreatedGroups(createdGroupIds)
    await cleanupCreatedCustomModels(createdCustomModelIds)
    await cleanupCreatedSystemAccounts(createdSystemAccountIds)
  }
}

interface ApiEnvelope<T> {
  data: T
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie?: string): Promise<T> {
  return (await getEnvelopeWithHeaders<T>(baseUrl, path, cookie)).data
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
  return (await postCreatedEnvelopeWithHeaders<T>(baseUrl, path, body, cookie)).data
}

async function postOkEnvelope<T = unknown>(baseUrl: string, path: string, body: Record<string, unknown>, cookie: string): Promise<T> {
  return (await postOkEnvelopeWithHeaders<T>(baseUrl, path, body, cookie)).data
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

async function deleteNoContentEnvelope(baseUrl: string, path: string, cookie: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'DELETE',
    headers: { cookie },
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} DELETE 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
}

async function deleteEnvelope<T = unknown>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'DELETE',
    headers: { cookie },
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} DELETE 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function expectStatus(baseUrl: string, path: string, cookie: string, expectedStatus: number, message: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    headers: { cookie },
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, expectedStatus, `${message}，实际 HTTP ${response.status}: ${text}`)
}

function smokeDateKey(date = new Date()): string {
  const parts = new Intl.DateTimeFormat('en-US', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit'
  }).formatToParts(date)
  const year = parts.find((part) => part.type === 'year')?.value
  const month = parts.find((part) => part.type === 'month')?.value
  const day = parts.find((part) => part.type === 'day')?.value
  assert.ok(year && month && day, 'performance smoke 应能生成统计日期键')
  return `${year}-${month}-${day}`
}

async function markCreatedAuthorizationInstanceUnavailable(grantId: string, granteeSystemAccountId: string): Promise<string> {
  const businessClient = await createBusinessDatabaseClient()
  const instance = await businessClient.one<{ id: string }>(`
    SELECT accounts.id
    FROM ${businessTable(businessClient, 'resource_authorization_grants')} grants
    INNER JOIN ${businessTable(businessClient, 'resource_authorizations')} authorizations
      ON authorizations.resource_type = grants.resource_type
      AND authorizations.resource_id = grants.resource_id
      AND authorizations.grantee_system_account_id = grants.grantee_system_account_id
    INNER JOIN ${businessTable(businessClient, 'accounts')} accounts
      ON accounts.authorization_instance_authorization_id = authorizations.id
      AND accounts.system_account_id = authorizations.grantee_system_account_id
      AND accounts.deleted_at IS NULL
    WHERE grants.id = ?
      AND grants.grantee_system_account_id = ?
    LIMIT 1
  `, [grantId, granteeSystemAccountId])
  assert.ok(instance?.id, 'performance smoke 授权创建后应生成授权实例账户')
  const now = new Date().toISOString()
  const cooldownUntil = new Date(Date.now() + 60_000).toISOString()
  await businessClient.execute(`
    UPDATE ${businessTable(businessClient, 'accounts')}
    SET status = 'temporary_unavailable',
        schedulable = 0,
        cooldown_until = ?,
        last_error_code = 'smoke_authorized_restore',
        last_error_message = 'performance smoke authorized restore',
        stream_failure_count = 1,
        stream_failure_window_started_at = ?,
        updated_at = ?
    WHERE id = ?
  `, [cooldownUntil, now, now, instance.id])
  return instance.id
}

async function seedAuthorizationUsageDetail(accountId: string, granteeSystemAccountId: string): Promise<AuthorizationUsageSeed> {
  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const now = new Date().toISOString()
  const usageDate = smokeDateKey()
  const authorizationId = `rauth_smoke_usage_${suffix}`
  const authorizationSourceId = `ras_smoke_usage_${suffix}`
  const grantId = `rag_smoke_usage_${suffix}`
  const businessClient = await createBusinessDatabaseClient()
  const statsClient = await createStatsDatabaseClient()

  await businessClient.execute(`
    INSERT INTO ${businessTable(businessClient, 'resource_authorizations')} (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
      scope, status, effective_source_type, effective_source_team_id, activated_at,
      last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
      revoked_by, revoked_at, revoked_reason, updated_at
    ) VALUES (?, 'account', ?, 'sys_admin', ?, 'use', 'active', 'manual', NULL, ?, ?, ?, NULL, NULL, 'sys_admin', ?, NULL, NULL, NULL, ?)
  `, [
    authorizationId,
    accountId,
    granteeSystemAccountId,
    now,
    now,
    'performance smoke authorization usage',
    now,
    now
  ])

  await businessClient.execute(`
    INSERT INTO ${businessTable(businessClient, 'resource_authorization_sources')} (
      id, authorization_id, source_type, source_team_id, status, activated_at,
      ended_at, ended_reason, created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, ?, 'manual', NULL, 'active', ?, NULL, NULL, 'sys_admin', ?, NULL, NULL, ?)
  `, [authorizationSourceId, authorizationId, now, now, now])

  await businessClient.execute(`
    INSERT INTO ${businessTable(businessClient, 'resource_authorization_grants')} (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
      grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
      limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, 'account', ?, 'sys_admin', 'system_account', ?, NULL, 'use', 'active', ?, NULL, NULL, 'sys_admin', ?, NULL, NULL, ?)
  `, [
    grantId,
    accountId,
    granteeSystemAccountId,
    'performance smoke authorization usage',
    now,
    now
  ])

  await statsClient.execute(`
    INSERT INTO ${statsTable(statsClient, 'usage_scope_range_windows')} (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, success_count, error_count, input_tokens, output_tokens,
      cache_read_tokens, cache_read_cost_usd, total_cost_usd, duration_ms_sum,
      duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, active_days, last_used_at, last_error_at, updated_at
    ) VALUES (?, 'account_authorization', ?, ?, ?, 17, 16, 1, 170, 34, 12, 0.0004, 0.017, 5100, 17, 700, 1200, 17, 200, 1, ?, NULL, ?)
  `, [
    granteeSystemAccountId,
    authorizationId,
    usageDate,
    usageDate,
    `${usageDate}T00:17:00.000Z`,
    now
  ])
  const selectedRuntimeAuthorization = await businessClient.one<{ id: string }>(`
    SELECT id
    FROM ${businessTable(businessClient, 'resource_authorizations')}
    WHERE resource_type = 'account'
      AND resource_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `, [accountId, granteeSystemAccountId])
  assert.equal(selectedRuntimeAuthorization?.id, authorizationId, 'performance smoke 授权 usage 夹具应能按详情查询条件命中运行态授权')
  const seededWindow = await statsClient.one<{ request_count: number | string }>(`
    SELECT request_count
    FROM ${statsTable(statsClient, 'usage_scope_range_windows')}
    WHERE system_account_id = ?
      AND scope_type = 'account_authorization'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
    LIMIT 1
  `, [granteeSystemAccountId, authorizationId, usageDate, usageDate])
  assert.equal(Number(seededWindow?.request_count ?? 0), 17, 'performance smoke 授权 usage 夹具应写入统计范围窗口')

  return {
    authorizationId,
    authorizationSourceId,
    grantId,
    statsSystemAccountId: granteeSystemAccountId,
    scopeType: 'account_authorization',
    scopeId: authorizationId,
    startDate: usageDate,
    endDate: usageDate
  }
}

async function cleanupCreatedAuthorizationUsageSeeds(seeds: AuthorizationUsageSeed[]): Promise<void> {
  if (!seeds.length) {
    return
  }
  const businessClient = await createBusinessDatabaseClient()
  const statsClient = await createStatsDatabaseClient()
  for (const seed of seeds.splice(0)) {
    await statsClient.execute(`
      DELETE FROM ${statsTable(statsClient, 'usage_scope_range_windows')}
      WHERE system_account_id = ?
        AND scope_type = ?
        AND scope_id = ?
        AND start_date = ?
        AND end_date = ?
    `, [seed.statsSystemAccountId, seed.scopeType, seed.scopeId, seed.startDate, seed.endDate])
    await businessClient.execute(`DELETE FROM ${businessTable(businessClient, 'resource_authorization_sources')} WHERE id = ?`, [seed.authorizationSourceId])
    await businessClient.execute(`DELETE FROM ${businessTable(businessClient, 'resource_authorization_grants')} WHERE id = ?`, [seed.grantId])
    await businessClient.execute(`DELETE FROM ${businessTable(businessClient, 'resource_authorizations')} WHERE id = ?`, [seed.authorizationId])
  }
}

async function cleanupCreatedAuthorizationGrants(grantIds: string[]): Promise<void> {
  if (!grantIds.length) {
    return
  }
  const businessClient = await createBusinessDatabaseClient()
  for (const grantId of grantIds.splice(0)) {
    const grant = await businessClient.one<{
      resource_type?: string
      resource_id?: string
      grantee_system_account_id?: string | null
    }>(`
      SELECT resource_type, resource_id, grantee_system_account_id
      FROM ${businessTable(businessClient, 'resource_authorization_grants')}
      WHERE id = ?
      LIMIT 1
    `, [grantId])
    const runtimeAuthorizations = grant?.resource_type && grant.resource_id && grant.grantee_system_account_id
      ? await businessClient.query<{ id: string }>(`
        SELECT id
        FROM ${businessTable(businessClient, 'resource_authorizations')}
        WHERE resource_type = ?
          AND resource_id = ?
          AND grantee_system_account_id = ?
      `, [grant.resource_type, grant.resource_id, grant.grantee_system_account_id])
      : []
    for (const authorization of runtimeAuthorizations) {
      await businessClient.execute(`DELETE FROM ${businessTable(businessClient, 'group_accounts')} WHERE account_authorization_id = ?`, [authorization.id])
      await businessClient.execute(`DELETE FROM ${businessTable(businessClient, 'accounts')} WHERE authorization_instance_authorization_id = ?`, [authorization.id])
      await businessClient.execute(`DELETE FROM ${businessTable(businessClient, 'resource_authorization_sources')} WHERE authorization_id = ?`, [authorization.id])
      await businessClient.execute(`DELETE FROM ${businessTable(businessClient, 'resource_authorizations')} WHERE id = ?`, [authorization.id])
    }
    await businessClient.execute(`DELETE FROM ${businessTable(businessClient, 'resource_authorization_grants')} WHERE id = ?`, [grantId])
  }
}

async function createBusinessDatabaseClient(): Promise<DatabaseClient> {
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  const [{ createSqliteDatabaseClient }, { getBusinessDatabase }] = await Promise.all([
    import('../../storage/database-client.js'),
    import('../../storage/database.js')
  ])
  return createSqliteDatabaseClient(getBusinessDatabase())
}

async function createStatsDatabaseClient(): Promise<DatabaseClient> {
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  const [{ createSqliteDatabaseClient }, { getStatsDatabase }] = await Promise.all([
    import('../../storage/database-client.js'),
    import('../../storage/database.js')
  ])
  return createSqliteDatabaseClient(getStatsDatabase())
}

function businessTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_stats', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

async function cleanupCreatedCustomModels(customModelIds: string[]): Promise<void> {
  if (!customModelIds.length) {
    return
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of customModelIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."custom_provider_models" WHERE id = ?', [id])
    }
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of customModelIds.splice(0)) {
    database.prepare('DELETE FROM custom_provider_models WHERE id = ?').run(id)
  }
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
      const routeRows = await client.query<{ route_strategy_id?: string }>('SELECT route_strategy_id FROM "juhe_business"."api_keys" WHERE id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."api_keys" WHERE id = ?', [id])
      for (const routeRow of routeRows) {
        if (!routeRow.route_strategy_id) continue
        await client.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE route_strategy_id = ?', [routeRow.route_strategy_id])
        await client.execute('DELETE FROM "juhe_business"."route_strategies" WHERE id = ?', [routeRow.route_strategy_id])
      }
    }
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of apiKeyIds.splice(0)) {
    const routeRows = database.prepare('SELECT route_strategy_id FROM api_keys WHERE id = ?').all(id) as Array<{ route_strategy_id?: string }>
    database.prepare('DELETE FROM api_keys WHERE id = ?').run(id)
    for (const routeRow of routeRows) {
      if (!routeRow.route_strategy_id) continue
      database.prepare('DELETE FROM route_strategy_groups WHERE route_strategy_id = ?').run(routeRow.route_strategy_id)
      database.prepare('DELETE FROM route_strategies WHERE id = ?').run(routeRow.route_strategy_id)
    }
  }
}

async function cleanupCreatedRouteStrategies(routeStrategyIds: string[]): Promise<void> {
  if (!routeStrategyIds.length) {
    return
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of routeStrategyIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE route_strategy_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."route_strategies" WHERE id = ?', [id])
    }
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of routeStrategyIds.splice(0)) {
    database.prepare('DELETE FROM route_strategy_groups WHERE route_strategy_id = ?').run(id)
    database.prepare('DELETE FROM route_strategies WHERE id = ?').run(id)
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
      await client.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE group_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."groups" WHERE id = ?', [id])
    }
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of groupIds.splice(0)) {
    database.prepare('DELETE FROM route_strategy_groups WHERE group_id = ?').run(id)
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
      await cleanupSystemAccountBusinessDependents(client, id)
      await client.execute('DELETE FROM "juhe_business"."groups" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."system_sessions" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."system_accounts" WHERE id = ?', [id])
    }
    await closePostgresPool()
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  const client = await createBusinessDatabaseClient()
  for (const id of systemAccountIds.splice(0)) {
    await cleanupSystemAccountBusinessDependents(client, id)
    database.prepare('DELETE FROM groups WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM system_sessions WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM system_accounts WHERE id = ?').run(id)
  }
}

async function cleanupSystemAccountBusinessDependents(client: DatabaseClient, systemAccountId: string): Promise<void> {
  const apiKeys = businessTable(client, 'api_keys')
  const routeStrategies = businessTable(client, 'route_strategies')
  const routeStrategyGroups = businessTable(client, 'route_strategy_groups')
  const groupAccounts = businessTable(client, 'group_accounts')
  const accounts = businessTable(client, 'accounts')
  const authorizations = businessTable(client, 'resource_authorizations')
  const authorizationSources = businessTable(client, 'resource_authorization_sources')
  const authorizationGrants = businessTable(client, 'resource_authorization_grants')
  const announcements = businessTable(client, 'announcements')

  await client.execute(`
    DELETE FROM ${apiKeys}
    WHERE system_account_id = ?
      OR route_strategy_id IN (
        SELECT id FROM ${routeStrategies} WHERE system_account_id = ?
      )
  `, [systemAccountId, systemAccountId])
  await client.execute(`DELETE FROM ${routeStrategyGroups} WHERE system_account_id = ?`, [systemAccountId])
  await client.execute(`DELETE FROM ${routeStrategies} WHERE system_account_id = ?`, [systemAccountId])

  await client.execute(`
    DELETE FROM ${groupAccounts}
    WHERE account_authorization_id IN (
      SELECT id FROM ${authorizations} WHERE grantee_system_account_id = ?
    )
  `, [systemAccountId])
  await client.execute(`
    DELETE FROM ${groupAccounts}
    WHERE account_id IN (
      SELECT id FROM ${accounts}
      WHERE system_account_id = ?
        AND authorization_instance_authorization_id IS NOT NULL
    )
  `, [systemAccountId])
  await deleteAuthorizationInstanceAccountRows(client, systemAccountId)
  await client.execute(`
    DELETE FROM ${accounts}
    WHERE system_account_id = ?
      AND authorization_instance_authorization_id IS NOT NULL
  `, [systemAccountId])
  await client.execute(`
    DELETE FROM ${authorizationSources}
    WHERE authorization_id IN (
      SELECT id FROM ${authorizations} WHERE grantee_system_account_id = ?
    )
  `, [systemAccountId])
  await client.execute(`DELETE FROM ${authorizations} WHERE grantee_system_account_id = ?`, [systemAccountId])
  await client.execute(`DELETE FROM ${authorizationGrants} WHERE grantee_system_account_id = ?`, [systemAccountId])
  await client.execute(`DELETE FROM ${announcements} WHERE created_by = ? OR updated_by = ?`, [systemAccountId, systemAccountId])
}

async function deleteAuthorizationInstanceAccountRows(client: DatabaseClient, systemAccountId: string): Promise<void> {
  const accounts = businessTable(client, 'accounts')
  const accountChildTables = [
    'account_supported_models',
    'account_model_mappings',
    'account_tag_bindings',
    'account_name_search_terms',
    'account_name_search_documents',
    'account_api_key_runtime_states'
  ]
  for (const tableName of accountChildTables) {
    await client.execute(`
      DELETE FROM ${businessTable(client, tableName)}
      WHERE account_id IN (
        SELECT id FROM ${accounts}
        WHERE system_account_id = ?
          AND authorization_instance_authorization_id IS NOT NULL
      )
    `, [systemAccountId])
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

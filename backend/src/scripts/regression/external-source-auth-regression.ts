import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import type { Server } from 'node:http'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Express } from 'express'

import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { AccountSummary } from '../../domain/types.js'

if (process.env.JUHE_AI_EXTERNAL_SOURCE_AUTH_DEMO_CHILD === '1') {
  await runChild()
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-external-source-auth-'))
try {
  const result = spawnSync(process.execPath, [
    '--import',
    'tsx',
    fileURLToPath(import.meta.url)
  ], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      JUHE_AI_EXTERNAL_SOURCE_AUTH_DEMO_CHILD: '1',
      JUHE_AI_PROCESS_ROLE: 'db-service',
      JUHE_AI_DATABASE_PATH: join(tempRoot, 'business.sqlite3'),
      JUHE_AI_DATASET_DATABASE_PATH: join(tempRoot, 'dataset.sqlite3'),
      JUHE_AI_STATS_DATABASE_PATH: join(tempRoot, 'stats.sqlite3'),
      JUHE_AI_USAGE_SHARD_ROOT: join(tempRoot, 'usage-shards'),
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    encoding: 'utf8'
  })

  if (result.status !== 0) {
    process.stdout.write(result.stdout)
    process.stderr.write(result.stderr)
    process.exit(result.status ?? 1)
  }
  process.stdout.write(result.stdout)
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runChild(): Promise<void> {
  const { createSystemApiApp } = await import('../../modules/system-api/system-api-app.js')
  const {
    createExternalIntegrationSourceToken,
    externalIntegrationAccountAddWriteScope,
    externalIntegrationAccountDeleteWriteScope,
    externalIntegrationAccountListReadScope,
    externalIntegrationAccountUpdateWriteScope,
    externalIntegrationApiKeyAddWriteScope,
    externalIntegrationApiKeyDeleteWriteScope,
    externalIntegrationApiKeyListReadScope,
    externalIntegrationApiKeyUpdateWriteScope,
    externalIntegrationGroupAddWriteScope,
    externalIntegrationGroupDeleteWriteScope,
    externalIntegrationGroupListReadScope,
    externalIntegrationGroupUpdateWriteScope,
    externalIntegrationRouteStrategyAddWriteScope,
    externalIntegrationRouteStrategyDeleteWriteScope,
    externalIntegrationRouteStrategyListReadScope,
    externalIntegrationRouteStrategyUpdateWriteScope,
    externalIntegrationScopeOptions,
    builtInExternalIntegrationTestSourceId,
    builtInExternalIntegrationTestTokenId,
    findExternalIntegrationSourceTokenSecret,
    upsertExternalIntegrationSource
  } = await import('../../storage/external-integration-source.repository.js')
  const {
    flushExternalIntegrationSourceLastUsedTouchesForTest
  } = await import('../../storage/external-integration-source-auth.repository.js')
  const {
    AccountConfigRevisionConflictError,
    createSession,
    findAccountSummary,
    findSystemAccountByUsername,
    updateAccount,
    updateAccountAsync,
    updateSystemAccount
  } = await import('../../storage/repositories.js')
  const { updatePublicWelfareAccount } = await import('../../modules/external-integrations/external-public-account-push.service.js')
  const { closeStorageDatabases, getBusinessDatabase } = await import('../../storage/database.js')
  const { closeSqliteReadWorkerPool } = await import('../../storage/sqlite-read-worker-pool.js')

  const groupReadToken = 'juis_valid_group_read_public_token_32_chars'
  const noScopeToken = 'juis_no_scope_external_source_token_32_chars'
  const disabledSourceToken = 'juis_disabled_source_public_token_32_chars'
  const limitedSourceToken = 'juis_limited_source_public_token_32_chars'
  const resourceToken = 'juis_valid_public_resource_crud_token_32_chars'
  const targetUsername = 'public_control_user'
  const sourceName = '公开资源维护来源'
  const resourceControlScopes = [
    externalIntegrationApiKeyListReadScope,
    externalIntegrationApiKeyAddWriteScope,
    externalIntegrationApiKeyUpdateWriteScope,
    externalIntegrationApiKeyDeleteWriteScope,
    externalIntegrationRouteStrategyListReadScope,
    externalIntegrationRouteStrategyAddWriteScope,
    externalIntegrationRouteStrategyUpdateWriteScope,
    externalIntegrationRouteStrategyDeleteWriteScope,
    externalIntegrationGroupListReadScope,
    externalIntegrationGroupAddWriteScope,
    externalIntegrationGroupUpdateWriteScope,
    externalIntegrationGroupDeleteWriteScope,
    externalIntegrationAccountListReadScope,
    externalIntegrationAccountAddWriteScope,
    externalIntegrationAccountUpdateWriteScope,
    externalIntegrationAccountDeleteWriteScope
  ]

  const readSource = upsertExternalIntegrationSource({
    name: sourceName,
    status: 'active',
    scopes: [externalIntegrationGroupListReadScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: readSource.id,
    name: 'group-read-token',
    token: groupReadToken,
    scopes: [externalIntegrationGroupListReadScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: readSource.id,
    name: 'no-scope-token',
    token: noScopeToken,
    scopes: []
  })
  const disabledSource = upsertExternalIntegrationSource({
    name: '已停用公开来源',
    status: 'disabled',
    scopes: [externalIntegrationGroupListReadScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: disabledSource.id,
    name: 'disabled-source-token',
    token: disabledSourceToken,
    scopes: [externalIntegrationGroupListReadScope]
  })
  const limitedSource = upsertExternalIntegrationSource({
    name: '限频公开来源',
    status: 'active',
    scopes: [externalIntegrationGroupListReadScope],
    rateLimits: [{ windowSeconds: 10, maxRequests: 2 }]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: limitedSource.id,
    name: 'limited-source-token',
    token: limitedSourceToken,
    scopes: [externalIntegrationGroupListReadScope]
  })
  const resourceSource = upsertExternalIntegrationSource({
    name: '公开资源 CRUD 来源',
    status: 'active',
    scopes: resourceControlScopes
  })
  createExternalIntegrationSourceToken({
    sourceRefId: resourceSource.id,
    name: 'resource-crud-token',
    token: resourceToken,
    scopes: resourceControlScopes
  })

  const builtInTokenSecret = findExternalIntegrationSourceTokenSecret(
    builtInExternalIntegrationTestSourceId,
    builtInExternalIntegrationTestTokenId
  )
  assert(builtInTokenSecret?.token, '内置测试 Token 应写入数据库并可用于回归测试')
  let builtInTestToken = builtInTokenSecret.token

  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', publicApiPrefix: '/__aipublic__' })
  const server = await listen(app)
  const address = server.address()
  assert(address && typeof address !== 'string', '测试 HTTP 服务地址无效')
  const baseUrl = `http://127.0.0.1:${address.port}`
  assert.equal(updateSystemAccount('sys_admin', { mustChangePassword: false })?.mustChangePassword, false, '回归准备：管理员应可访问管理 API')
  const adminCookie = `juhe_ai_session=${createSession('sys_admin', 1).token}`

  try {
    await assertPublicApiDocs(baseUrl, adminCookie, builtInTestToken)
    await assertBuiltInSourceManagement(baseUrl, adminCookie, builtInTestToken, builtInExternalIntegrationTestSourceId, externalIntegrationScopeOptions.map((item) => item.value).sort())
    builtInTestToken = await resetBuiltInToken(baseUrl, adminCookie, builtInTestToken)

    await assertRemovedPublicPaths(baseUrl, builtInTestToken)
    await assertSourceAuthBoundary(baseUrl, {
      groupReadToken,
      noScopeToken,
      disabledSourceToken,
      builtInTestToken,
      targetUsername
    })

    const publicGroup = await createPublicGroup(baseUrl, resourceToken, targetUsername)
    const groupReadSuccess = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=${targetUsername}`, {
      Authorization: `Bearer ${groupReadToken}`
    })
    assert.equal(groupReadSuccess.status, 200, `公开分组只读 token 应能成功调用：${JSON.stringify(groupReadSuccess.body)}`)
    assert(groupReadSuccess.body.data.items.some((item: any) => item.id === publicGroup.id), '公开分组只读 token 应返回新增分组')
    const routeStrategy = await createPublicRouteStrategy(baseUrl, resourceToken, targetUsername, publicGroup.id)
    const roundRobinRouteStrategy = await createPublicRouteStrategy(baseUrl, resourceToken, targetUsername, publicGroup.id, '公开轮询路由', 'round_robin')
    await assertPublicRouteStrategyCrud(baseUrl, resourceToken, targetUsername, publicGroup.id, routeStrategy.id)
    const apiKeyId = await assertPublicApiKeyCrud(baseUrl, resourceToken, targetUsername, routeStrategy.id, roundRobinRouteStrategy.id)
    const accountId = await assertPublicAccountCrud(baseUrl, resourceToken, targetUsername, {
      repositories: {
        AccountConfigRevisionConflictError,
        findAccountSummary,
        findSystemAccountByUsername,
        updateAccount,
        updateAccountAsync
      },
      service: { updatePublicWelfareAccount }
    })

    await assertDisabledTargetBoundary(baseUrl, resourceToken, targetUsername, {
      groupId: publicGroup.id,
      routeStrategyId: routeStrategy.id,
      apiKeyId,
      accountId
    }, { findSystemAccountByUsername, updateSystemAccount })

    await deletePublicApiKey(baseUrl, resourceToken, apiKeyId)
    await deletePublicAccount(baseUrl, resourceToken, accountId)
    await deletePublicRouteStrategy(baseUrl, resourceToken, roundRobinRouteStrategy.id)
    await deletePublicRouteStrategy(baseUrl, resourceToken, routeStrategy.id)
    await deletePublicGroup(baseUrl, resourceToken, publicGroup.id)

    await assertManagementSourceLifecycle(baseUrl, adminCookie, targetUsername, externalIntegrationGroupListReadScope)
    await assertRateLimit(baseUrl, limitedSourceToken)

    const protectedApi = await requestJson(baseUrl, '/__aisys__/api/accounts', {
      Authorization: `Bearer ${groupReadToken}`
    })
    assert.equal(protectedApi.status, 401, '外部来源 token 不能绕过后台登录态接口')

    await flushExternalIntegrationSourceLastUsedTouchesForTest()
    const lastUsedRow = getBusinessDatabase()
      .prepare(`
        SELECT tokens.last_used_at AS token_last_used_at, sources.last_used_at AS source_last_used_at
        FROM external_integration_source_tokens AS tokens
        JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
        WHERE sources.name = ? AND tokens.token_prefix = ?
      `)
      .get(sourceName, groupReadToken.slice(0, 8)) as { token_last_used_at?: string | null; source_last_used_at?: string | null } | undefined
    assert(lastUsedRow?.token_last_used_at, '成功鉴权后应低频记录 token 最近调用时间')
    assert(lastUsedRow?.source_last_used_at, '成功鉴权后应低频记录来源系统最近调用时间')
  } finally {
    await closeServer(server)
    await closeSqliteReadWorkerPool().catch(() => undefined)
    closeStorageDatabases()
  }

  console.log('外部来源系统鉴权和公开资源维护回归通过：公开前缀、Bearer token、测试 token mock、权限校验、停用来源、限频、后台登录边界、旧公开统计路径 404，以及 API Key / 路由策略 / 分组 / 账号增删改查均符合当前契约')
}

async function assertPublicApiDocs(baseUrl: string, adminCookie: string, builtInTestToken: string): Promise<void> {
  const apiDocs = await requestJson(baseUrl, '/__aisys__/api/external-integration-sources/api-docs', {
    Cookie: adminCookie
  })
  assert.equal(apiDocs.status, 200)
  const serialized = JSON.stringify(apiDocs.body.data)
  assert.equal(Object.prototype.hasOwnProperty.call(apiDocs.body.data, 'testToken'), false, '公开接口接入文档不能返回内置测试 Token 明文')
  assert.equal(serialized.includes(builtInTestToken), false, '公开接口接入文档不能包含内置测试 Token 明文')
  assert(apiDocs.body.data.items.every((item: any) => item.status === 'available'), '公开接口接入文档应把已落地接口标识为可用')
  assert.equal(serialized.includes("status\":\"mock"), false, '公开接口接入文档不应把已落地接口标识为 Mock 数据')
  const ids = apiDocs.body.data.items.map((item: any) => item.id)
  assert.deepEqual(ids, [
    'api-key-list',
    'api-key-add',
    'api-key-update',
    'api-key-delete',
    'route-strategy-list',
    'route-strategy-add',
    'route-strategy-update',
    'route-strategy-delete',
    'group-list',
    'group-add',
    'group-update',
    'group-delete',
    'account-list',
    'account-add',
    'account-update',
    'account-delete'
  ], '公开接口接入文档只应包含 API Key、路由策略、分组和账号 CRUD')
  for (const removed of ['ip-usage', 'account-usage', 'consumption-ranking', 'access-info', 'source-auth-demo']) {
    assert.equal(ids.includes(removed), false, `公开接口接入文档不应再包含 ${removed}`)
  }
}

async function assertBuiltInSourceManagement(baseUrl: string, adminCookie: string, builtInTestToken: string, builtInSourceId: string, expectedScopes: string[]): Promise<void> {
  const builtInList = await requestJson(baseUrl, '/__aisys__/api/external-integration-sources?pageSize=100', {
    Cookie: adminCookie
  })
  assert.equal(builtInList.status, 200)
  const builtInSource = builtInList.body.data.items.find((item: any) => item.id === builtInSourceId)
  assert(builtInSource, '外部来源授权列表应默认包含内置测试来源')
  assert.equal(builtInSource.isBuiltIn, true, '内置测试来源应带只读标识')
  assert.equal(builtInSource.tokenCount, 1, '外部来源列表应只对当前页批量回填 tokenCount')
  assert.equal(builtInSource.activeTokenCount, 1, '外部来源列表应只对当前页批量回填 activeTokenCount')
  assert.equal(builtInSource.primaryToken?.isBuiltIn, true, '外部来源列表应返回内置主 Token 摘要')
  assert.equal(builtInSource.primaryToken?.tokenPrefix, builtInTestToken.slice(0, 8), '主 Token 摘要应保留前缀')
  assert.equal(builtInSource.primaryToken?.tokenSuffix, builtInTestToken.slice(-8), '主 Token 摘要应保留后缀')
  for (const sensitiveField of ['token', 'tokenHash', 'tokenSecretEncrypted']) {
    assert.equal(Object.hasOwn(builtInSource.primaryToken ?? {}, sensitiveField), false, `主 Token 摘要不应返回 ${sensitiveField}`)
  }
  assert.deepEqual(builtInSource.scopes, expectedScopes, '内置测试来源应授权当前全部公开资源维护接口')
  assert.equal(builtInSource.scopes.some((scope: string) => scope.includes('usage') || scope.includes('ranking') || scope.includes('access_info') || scope.includes('source_auth_demo')), false, '内置测试来源不应再授权旧公开统计或 demo scope')

  const disabledBuiltIn = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInSourceId}`, {
    Cookie: adminCookie
  }, 'PATCH', { status: 'disabled' })
  assert.equal(disabledBuiltIn.status, 200, '内置测试来源应允许停用')
  const disabledBuiltInAuth = await requestJson(baseUrl, '/__aipublic__/group/list?targetUsername=huanmin', {
    Authorization: `Bearer ${builtInTestToken}`
  })
  assert.equal(disabledBuiltInAuth.status, 403, '内置测试来源停用后公开接口应拒绝调用')
  assert.equal(disabledBuiltInAuth.body.code, 'external_source_disabled')
  const enabledBuiltIn = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInSourceId}`, {
    Cookie: adminCookie
  }, 'PATCH', { status: 'active' })
  assert.equal(enabledBuiltIn.status, 200, '内置测试来源应允许重新启用')

  const builtInSourceEdit = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInSourceId}`, {
    Cookie: adminCookie
  }, 'PATCH', { name: '不应允许改名' })
  assert.equal(builtInSourceEdit.status, 400, '内置测试来源不应允许编辑基础信息')
  const builtInTokenCreate = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInSourceId}/tokens`, {
    Cookie: adminCookie
  }, 'POST', {
    name: '不应新增的 Token',
    status: 'active',
    scopes: [expectedScopes[0]]
  })
  assert.equal(builtInTokenCreate.status, 400, '内置测试来源不应允许新增 Token')
  const builtInDelete = await requestStatus(baseUrl, `/__aisys__/api/external-integration-sources/${builtInSourceId}`, {
    Cookie: adminCookie
  }, 'DELETE')
  assert.equal(builtInDelete, 400, '内置测试来源不应允许删除')
}

async function resetBuiltInToken(baseUrl: string, adminCookie: string, previousToken: string): Promise<string> {
  const resetBuiltIn = await requestJson(baseUrl, '/__aisys__/api/external-integration-sources/built-in-test-token/reset', {
    Cookie: adminCookie
  }, 'POST')
  assert.equal(resetBuiltIn.status, 200, '管理员应能重置内置测试 Token')
  const nextToken = resetBuiltIn.body.data.token.token as string
  assert(nextToken, '重置内置测试 Token 应一次性返回明文')
  assert.notEqual(nextToken, previousToken, '重置内置测试 Token 应生成新明文')
  const oldBuiltInTokenAuth = await requestJson(baseUrl, '/__aipublic__/group/list?targetUsername=huanmin', {
    Authorization: `Bearer ${previousToken}`
  })
  assert.equal(oldBuiltInTokenAuth.status, 401, '重置后旧内置测试 Token 应立即失效')
  return nextToken
}

async function assertRemovedPublicPaths(baseUrl: string, token: string): Promise<void> {
  for (const path of [
    '/__aipublic__/demo/source-auth',
    '/__aipublic__/ip/usage?range=today',
    '/__aipublic__/account/usage?range=today',
    '/__aipublic__/consumption/ranking?range=today',
    '/__aipublic__/access/info'
  ]) {
    const response = await requestJson(baseUrl, path, { Authorization: `Bearer ${token}` })
    assert.equal(response.status, 404, `旧公开接口应返回 404：${path}`)
  }
}

async function assertSourceAuthBoundary(baseUrl: string, input: {
  groupReadToken: string
  noScopeToken: string
  disabledSourceToken: string
  builtInTestToken: string
  targetUsername: string
}): Promise<void> {
  const missingToken = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=${input.targetUsername}`)
  assert.equal(missingToken.status, 401)
  assert.equal(missingToken.body.code, 'external_source_token_missing')
  const wrongToken = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=${input.targetUsername}`, {
    Authorization: 'Bearer juis_wrong_token'
  })
  assert.equal(wrongToken.status, 401)
  assert.equal(wrongToken.body.code, 'external_source_unauthorized')
  const noScope = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=${input.targetUsername}`, {
    Authorization: `Bearer ${input.noScopeToken}`
  })
  assert.equal(noScope.status, 403)
  assert.equal(noScope.body.code, 'external_source_scope_forbidden')
  const disabledSource = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=${input.targetUsername}`, {
    Authorization: `Bearer ${input.disabledSourceToken}`
  })
  assert.equal(disabledSource.status, 403)
  assert.equal(disabledSource.body.code, 'external_source_disabled')
  const testTokenSuccess = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=${input.targetUsername}`, {
    Authorization: `Bearer ${input.builtInTestToken}`
  })
  assert.equal(testTokenSuccess.status, 200)
  assert.equal(testTokenSuccess.body.data.source, 'mock')
}

async function createPublicGroup(baseUrl: string, token: string, targetUsername: string): Promise<{ id: string }> {
  const response = await requestJson(baseUrl, '/__aipublic__/group/add', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    targetUsername,
    targetDisplayName: '公开控制用户',
    name: '公开接口回归分组',
    providerCode: GPT_VENDOR_CODE
  })
  assert.equal(response.status, 201, `公开分组新增应成功：${JSON.stringify(response.body)}`)
  assert.equal(response.body.data.action, 'created')
  assert(response.body.data.group.id, '公开分组新增应返回分组 ID')
  return { id: response.body.data.group.id }
}

async function createPublicRouteStrategy(baseUrl: string, token: string, targetUsername: string, groupId: string, name = '公开默认路由', mode = 'normal'): Promise<{ id: string }> {
  const response = await requestJson(baseUrl, '/__aipublic__/route-strategy/add', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    targetUsername,
    name,
    mode,
    groupBindings: [{ groupId, priority: 1, weight: 100, status: 'active' }]
  })
  assert.equal(response.status, 201, `公开路由策略新增应成功：${JSON.stringify(response.body)}`)
  assert.equal(response.body.data.action, 'created')
  assert(response.body.data.routeStrategy.id, '公开路由策略新增应返回路由策略 ID')
  return { id: response.body.data.routeStrategy.id }
}

async function assertPublicRouteStrategyCrud(baseUrl: string, token: string, targetUsername: string, groupId: string, routeStrategyId: string): Promise<void> {
  const list = await requestJson(baseUrl, `/__aipublic__/route-strategy/list?targetUsername=${targetUsername}&mode=all`, {
    Authorization: `Bearer ${token}`
  })
  assert.equal(list.status, 200)
  assert(list.body.data.items.some((item: any) => item.id === routeStrategyId), '公开路由策略列表应返回新增路由策略')
  assert.equal(list.body.data.items.find((item: any) => item.id === routeStrategyId).groupBindings[0].groupId, groupId)

  const update = await requestJson(baseUrl, '/__aipublic__/route-strategy/update', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    routeStrategyId,
    name: '公开默认路由-更新',
    status: 'active'
  })
  assert.equal(update.status, 200, `公开账号修改应成功：${JSON.stringify(update.body)}`)
  assert.equal(update.body.data.action, 'updated')
  assert.equal(update.body.data.routeStrategy.name, '公开默认路由-更新')

  const missingUpdate = await requestJson(baseUrl, '/__aipublic__/route-strategy/update', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    targetUsername,
    routeStrategyId: 'rts_public_missing',
    status: 'disabled'
  })
  assert.equal(missingUpdate.status, 404, '公开路由策略修改找不到策略时应返回 404')
}

async function assertPublicApiKeyCrud(baseUrl: string, token: string, targetUsername: string, routeStrategyId: string, nextRouteStrategyId: string): Promise<string> {
  const add = await requestJson(baseUrl, '/__aipublic__/api-key/add', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    targetUsername,
    name: '公开接口回归 API Key',
    routeStrategyId,
    status: 'active'
  })
  assert.equal(add.status, 201, `公开 API Key 新增应成功：${JSON.stringify(add.body)}`)
  assert(add.body.data.apiKey.id, '公开 API Key 新增应返回 API Key ID')
  assert(add.body.data.apiKey.key, '公开 API Key 新增应一次性返回明文 key')
  const apiKeyId = add.body.data.apiKey.id as string

  const list = await requestJson(baseUrl, `/__aipublic__/api-key/list?targetUsername=${targetUsername}&routeStrategyId=${routeStrategyId}`, {
    Authorization: `Bearer ${token}`
  })
  assert.equal(list.status, 200)
  assert(list.body.data.items.some((item: any) => item.id === apiKeyId), '公开 API Key 列表应返回新增 Key')

  const update = await requestJson(baseUrl, '/__aipublic__/api-key/update', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    apiKeyId,
    routeStrategyId: nextRouteStrategyId,
    status: 'disabled'
  })
  assert.equal(update.status, 200, `公开账号修改应成功：${JSON.stringify(update.body)}`)
  assert.equal(update.body.data.apiKey.routeStrategyId, nextRouteStrategyId)
  assert.equal(update.body.data.apiKey.routeStrategyMode, 'round_robin')
  assert.equal(Object.prototype.hasOwnProperty.call(update.body.data.apiKey, 'key'), false, 'API Key 修改响应不应返回明文密钥')
  return apiKeyId
}

async function assertPublicAccountCrud(
  baseUrl: string,
  token: string,
  targetUsername: string,
  dependencies: {
    repositories: Pick<typeof import('../../storage/repositories.js'),
      | 'AccountConfigRevisionConflictError'
      | 'findAccountSummary'
      | 'findSystemAccountByUsername'
      | 'updateAccount'
      | 'updateAccountAsync'>
    service: Pick<typeof import('../../modules/external-integrations/external-public-account-push.service.js'), 'updatePublicWelfareAccount'>
  }
): Promise<string> {
  const add = await requestJson(baseUrl, '/__aipublic__/account/add', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    targetUsername,
    targetGroupName: '公开接口回归分组',
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '公开接口回归账号',
    type: 'api_key',
    baseUrl: 'https://push.example/v1',
    apiKey: 'sk-public-account-regression',
    status: 'disabled',
    supportedModels: ['gpt-5.6-sol']
  })
  assert.equal(add.status, 201, `公开账号新增应成功：${JSON.stringify(add.body)}`)
  assert(add.body.data.account.id, '公开账号新增应返回账号 ID')
  assert.equal(Object.prototype.hasOwnProperty.call(add.body.data.account, 'apiKey'), false, '账号新增响应不应回显上游 API Key')
  const accountId = add.body.data.account.id as string

  const list = await requestJson(baseUrl, `/__aipublic__/account/list?targetUsername=${targetUsername}&providerCode=${GPT_VENDOR_CODE}`, {
    Authorization: `Bearer ${token}`
  })
  assert.equal(list.status, 200)
  assert(list.body.data.items.some((item: any) => item.id === accountId), '公开账号列表应返回新增账号')

  const target = dependencies.repositories.findSystemAccountByUsername(targetUsername)
  assert(target, '公开账号新增应自动创建目标 owner')
  const access = { systemAccountId: target.id, role: 'user' as const }
  const existing = dependencies.repositories.findAccountSummary(accountId, access)
  assert(existing, '公开账号新增后应能按目标 owner 读取')
  const accountWithOverrides = dependencies.repositories.updateAccount(accountId, {
    credentials: {
      ...existing.credentials,
      service_tier_override: 'priority',
      reasoning_effort_override: 'low'
    }
  }, access)
  assert(accountWithOverrides, '回归夹具应能为公开 GPT 账户注入合法覆盖')

  const syncUpdate = dependencies.service.updatePublicWelfareAccount({
    accountId,
    apiKey: 'sk-public-account-regression-sync-compatible',
    supportedModels: ['gpt-5.6-sol', 'gpt-5.5-2026-04-23']
  })
  assert.deepEqual(syncUpdate.account.supportedModels, ['gpt-5.6-sol', 'gpt-5.5-2026-04-23'], '同步公开更新应接受支持覆盖的模型')
  const afterSyncUpdate = dependencies.repositories.findAccountSummary(accountId, access)
  assert(afterSyncUpdate, '同步公开更新后账户应仍存在')
  assert.equal(afterSyncUpdate.credentials.service_tier_override, 'priority', '同步公开更新必须保留服务等级覆盖')
  assert.equal(afterSyncUpdate.credentials.reasoning_effort_override, 'low', '同步公开更新必须保留思考级别覆盖')

  const asyncUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    accountId,
    apiKey: 'sk-public-account-regression-async-compatible',
    supportedModels: ['gpt-5.6-sol', 'gpt-5.5']
  })
  assert.equal(asyncUpdate.status, 200, `异步公开账号修改应成功：${JSON.stringify(asyncUpdate.body)}`)
  assert.deepEqual(asyncUpdate.body.data.account.supportedModels, ['gpt-5.6-sol', 'gpt-5.5'], '异步公开更新应接受支持覆盖的模型')
  const afterAsyncUpdate = dependencies.repositories.findAccountSummary(accountId, access)
  assert(afterAsyncUpdate, '异步公开更新后账户应仍存在')
  assert.equal(afterAsyncUpdate.credentials.service_tier_override, 'priority', '异步公开更新必须保留服务等级覆盖')
  assert.equal(afterAsyncUpdate.credentials.reasoning_effort_override, 'low', '异步公开更新必须保留思考级别覆盖')

  const staleConfigRevision = afterAsyncUpdate.configRevision
  assert.equal(typeof staleConfigRevision, 'number', 'SQLite stale revision 回归必须读取当前配置版本')
  const concurrentWinner = dependencies.repositories.updateAccount(accountId, {
    notes: 'SQLite revision 并发赢家'
  }, access)
  assert(concurrentWinner, 'SQLite stale revision 回归必须先提交并发赢家')
  const concurrentWinnerSnapshot = publicAccountMutationSnapshot(concurrentWinner)
  await assert.rejects(
    dependencies.repositories.updateAccountAsync(accountId, {
      notes: 'SQLite 陈旧写入不应落库'
    }, access, {
      expectedConfigRevision: staleConfigRevision
    }),
    (error: unknown) => error instanceof dependencies.repositories.AccountConfigRevisionConflictError,
    'SQLite updateAccountAsync 必须拒绝 stale expected revision'
  )
  const afterStaleRevisionRejection = dependencies.repositories.findAccountSummary(accountId, access)
  assert(afterStaleRevisionRejection, 'SQLite stale revision 拒绝后账户应仍存在')
  assert.deepEqual(
    publicAccountMutationSnapshot(afterStaleRevisionRejection),
    concurrentWinnerSnapshot,
    'SQLite stale revision 失败不得修改 revision、notes、credentials 或 models'
  )

  const stableSnapshot = concurrentWinnerSnapshot
  assert.throws(() => dependencies.service.updatePublicWelfareAccount({
    accountId,
    apiKey: 'sk-public-account-regression-sync-rejected',
    supportedModels: ['gpt-image-2']
  }), /所选支持模型中没有模型支持服务等级/, '同步公开更新必须拒绝不支持现有 GPT 覆盖的模型')
  const afterSyncRejection = dependencies.repositories.findAccountSummary(accountId, access)
  assert(afterSyncRejection, '同步公开更新拒绝后账户应仍存在')
  assert.deepEqual(publicAccountMutationSnapshot(afterSyncRejection), stableSnapshot, '同步校验失败后账户 models/credentials 必须原子不变')

  const missingCatalogUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
    Authorization: `Bearer ${token}`
  }, 'POST', {
    accountId,
    apiKey: 'sk-public-account-regression-async-rejected',
    supportedModels: ['gpt-public-catalog-missing']
  })
  assert.equal(missingCatalogUpdate.status, 400, `异步公开更新目录缺失模型应被拒绝：${JSON.stringify(missingCatalogUpdate.body)}`)
  assert.match(missingCatalogUpdate.body.message, /所选支持模型中没有模型支持服务等级/)
  const afterAsyncRejection = dependencies.repositories.findAccountSummary(accountId, access)
  assert(afterAsyncRejection, '异步公开更新拒绝后账户应仍存在')
  assert.deepEqual(publicAccountMutationSnapshot(afterAsyncRejection), stableSnapshot, '异步校验失败后账户 models/credentials 必须原子不变')
  return accountId
}

function publicAccountMutationSnapshot(account: AccountSummary): Pick<AccountSummary, 'configRevision' | 'credentials' | 'notes' | 'supportedModels'> {
  return {
    configRevision: account.configRevision,
    credentials: structuredClone(account.credentials),
    notes: account.notes,
    supportedModels: [...(account.supportedModels ?? [])]
  }
}

async function assertDisabledTargetBoundary(baseUrl: string, token: string, targetUsername: string, ids: {
  groupId: string
  routeStrategyId: string
  apiKeyId: string
  accountId: string
}, repositories: Pick<typeof import('../../storage/repositories.js'), 'findSystemAccountByUsername' | 'updateSystemAccount'>): Promise<void> {
  const target = repositories.findSystemAccountByUsername(targetUsername)
  assert(target, '公开控制面新增分组应自动创建目标用户')
  assert.equal(repositories.updateSystemAccount(target.id, { status: 'disabled' })?.status, 'disabled', '回归准备：公开控制面目标用户应可被停用')
  const probes: Array<[string, string, unknown | undefined]> = [
    ['GET', `/__aipublic__/group/list?targetUsername=${targetUsername}`, undefined],
    ['GET', `/__aipublic__/route-strategy/list?targetUsername=${targetUsername}`, undefined],
    ['GET', `/__aipublic__/api-key/list?targetUsername=${targetUsername}`, undefined],
    ['GET', `/__aipublic__/account/list?targetUsername=${targetUsername}`, undefined],
    ['POST', '/__aipublic__/group/update', { groupId: ids.groupId, name: '停用用户不应修改' }],
    ['POST', '/__aipublic__/route-strategy/update', { routeStrategyId: ids.routeStrategyId, status: 'disabled' }],
    ['POST', '/__aipublic__/api-key/update', { apiKeyId: ids.apiKeyId, status: 'disabled' }],
    ['POST', '/__aipublic__/account/update', { accountId: ids.accountId, status: 'disabled' }]
  ]
  for (const [method, path, body] of probes) {
    const response = await requestJson(baseUrl, path, { Authorization: `Bearer ${token}` }, method, body)
    assert.equal(response.status, 400, `目标用户停用后公开接口应被拒绝：${method} ${path}`)
    assert.match(response.body.message, /目标用户已停用/)
  }
  assert.equal(repositories.updateSystemAccount(target.id, { status: 'active' })?.status, 'active', '回归准备：公开控制面目标用户应可恢复启用')
}

async function deletePublicApiKey(baseUrl: string, token: string, apiKeyId: string): Promise<void> {
  const response = await requestJson(baseUrl, '/__aipublic__/api-key/del', { Authorization: `Bearer ${token}` }, 'POST', { apiKeyId })
  assert.equal(response.status, 200)
  assert.equal(response.body.data.action, 'deleted')
}

async function deletePublicAccount(baseUrl: string, token: string, accountId: string): Promise<void> {
  const response = await requestJson(baseUrl, '/__aipublic__/account/del', { Authorization: `Bearer ${token}` }, 'POST', { accountId })
  assert.equal(response.status, 200)
  assert.equal(response.body.data.action, 'deleted')
}

async function deletePublicRouteStrategy(baseUrl: string, token: string, routeStrategyId: string): Promise<void> {
  const response = await requestJson(baseUrl, '/__aipublic__/route-strategy/del', { Authorization: `Bearer ${token}` }, 'POST', { routeStrategyId })
  assert.equal(response.status, 200, `公开路由策略删除应成功：${JSON.stringify(response.body)}`)
  assert.equal(response.body.data.action, 'deleted')
}

async function deletePublicGroup(baseUrl: string, token: string, groupId: string): Promise<void> {
  const response = await requestJson(baseUrl, '/__aipublic__/group/del', { Authorization: `Bearer ${token}` }, 'POST', { groupId })
  assert.equal(response.status, 200)
  assert.equal(response.body.data.action, 'deleted')
}

async function assertManagementSourceLifecycle(baseUrl: string, adminCookie: string, targetUsername: string, groupListScope: string): Promise<void> {
  const managementCreatedSource = await requestJson(baseUrl, '/__aisys__/api/external-integration-sources', {
    Cookie: adminCookie
  }, 'POST', {
    name: '管理 API 删除回归来源',
    status: 'active',
    scopes: [groupListScope],
    rateLimits: [],
    expiresAt: null,
    notes: '用于覆盖公开接口授权新增和删除'
  })
  assert.equal(managementCreatedSource.status, 201, '管理员应能新增公开接口来源授权')
  const managementSourceId = managementCreatedSource.body.data.source.id as string
  const managementTokenId = managementCreatedSource.body.data.token.id as string
  const managementToken = managementCreatedSource.body.data.token.token as string
  const managementTokenSecret = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${managementSourceId}/tokens/${managementTokenId}/secret`, {
    Cookie: adminCookie
  })
  assert.equal(managementTokenSecret.status, 200, '管理员应能按单条 Token 复制完整明文')
  assert.equal(managementTokenSecret.body.data.token, managementToken, 'Token 复制接口应返回创建时的完整 Token')
  const publicCall = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=${targetUsername}`, {
    Authorization: `Bearer ${managementToken}`
  })
  assert.equal(publicCall.status, 200, '新增来源授权生成的 token 应可访问公开资源接口')
  const managementSourceDelete = await requestStatus(baseUrl, `/__aisys__/api/external-integration-sources/${managementSourceId}`, {
    Cookie: adminCookie
  }, 'DELETE')
  assert.equal(managementSourceDelete, 204, '管理员应能删除公开接口来源授权')
  const deletedSourceAuth = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=${targetUsername}`, {
    Authorization: `Bearer ${managementToken}`
  })
  assert.equal(deletedSourceAuth.status, 401, '删除来源授权后原 token 应立即失效')
}

async function assertRateLimit(baseUrl: string, token: string): Promise<void> {
  await requestJson(baseUrl, '/__aipublic__/group/list?targetUsername=huanmin', { Authorization: `Bearer ${token}` })
  await requestJson(baseUrl, '/__aipublic__/group/list?targetUsername=huanmin', { Authorization: `Bearer ${token}` })
  const rateLimited = await requestJson(baseUrl, '/__aipublic__/group/list?targetUsername=huanmin', { Authorization: `Bearer ${token}` })
  assert.equal(rateLimited.status, 429)
  assert.equal(rateLimited.body.code, 'external_source_rate_limited')
}

function listen(app: Express): Promise<Server> {
  return new Promise((resolve, reject) => {
    const server = app.listen(0, '127.0.0.1')
    server.once('error', reject)
    server.once('listening', () => {
      server.off('error', reject)
      resolve(server)
    })
  })
}

function closeServer(server: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error)
        return
      }
      resolve()
    })
  })
}

async function requestJson(
  baseUrl: string,
  path: string,
  headers: Record<string, string> = {},
  method = 'GET',
  body?: unknown
): Promise<{ status: number; body: any }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: body === undefined ? headers : { ...headers, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  const responseBody = await response.json()
  return {
    status: response.status,
    body: responseBody
  }
}

async function requestStatus(
  baseUrl: string,
  path: string,
  headers: Record<string, string> = {},
  method = 'GET',
  body?: unknown
): Promise<number> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: body === undefined ? headers : { ...headers, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  return response.status
}

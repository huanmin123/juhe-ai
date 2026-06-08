import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import type { Server } from 'node:http'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Express } from 'express'

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
    externalIntegrationAccessInfoReadScope,
    externalIntegrationAccountAddWriteScope,
    externalIntegrationAccountDeleteWriteScope,
    externalIntegrationAccountListReadScope,
    externalIntegrationAccountUpdateWriteScope,
    externalIntegrationAccountUsageReadScope,
    externalIntegrationApiKeyAddWriteScope,
    externalIntegrationApiKeyDeleteWriteScope,
    externalIntegrationApiKeyListReadScope,
    externalIntegrationApiKeyUpdateWriteScope,
    externalIntegrationConsumptionRankingReadScope,
    externalIntegrationGroupAddWriteScope,
    externalIntegrationGroupDeleteWriteScope,
    externalIntegrationGroupListReadScope,
    externalIntegrationGroupUpdateWriteScope,
    externalIntegrationIpUsageReadScope,
    externalIntegrationScopeOptions,
    externalIntegrationSourceAuthDemoScope,
    builtInExternalIntegrationTestSourceId,
    builtInExternalIntegrationTestTokenId,
    findExternalIntegrationSourceTokenSecret,
    upsertExternalIntegrationSource
  } = await import('../../storage/external-integration-source.repository.js')
  const {
    createSession,
    createAccount,
    createGroup,
    findSystemAccountByUsername,
    getOperationLogDetail,
    listOperationLogs,
    listAccounts,
    listGroupOptions,
    clearAccountFailureStateResult,
    updateSystemAccount
  } = await import('../../storage/repositories.js')
  const { closeStorageDatabases, getBusinessDatabase, getStatsDatabase } = await import('../../storage/database.js')
  const { decryptJson } = await import('../../storage/crypto.js')
  const clientIpStats = await import('../../storage/client-ip-stats.repository.js')
  const usageStatsHelpers = await import('../../storage/usage-stats-helpers.js')
  const readAccountCredentials = (accountId: string): Record<string, unknown> => {
    const row = getBusinessDatabase()
      .prepare('SELECT credentials_encrypted FROM accounts WHERE id = ?')
      .get(accountId) as { credentials_encrypted?: string } | undefined
    assert(row?.credentials_encrypted, `账号凭据不存在：${accountId}`)
    return decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  }

  const sourceName = 'juhe-ai公益站'
  const validToken = 'juis_valid_external_source_demo_token_32_chars'
  const noScopeToken = 'juis_no_scope_external_source_demo_token_32_chars'
  const disabledSourceToken = 'juis_disabled_source_demo_token_32_chars'
  const limitedSourceToken = 'juis_limited_source_demo_token_32_chars'
  const ipUsageToken = 'juis_valid_ip_usage_public_token_32_chars'
  const accountUsageToken = 'juis_valid_account_usage_public_token_32_chars'
  const consumptionRankingToken = 'juis_valid_consumption_ranking_public_token_32'
  const accessInfoToken = 'juis_valid_access_info_public_token_32_chars'
  const accountWriteToken = 'juis_valid_account_write_public_token_32_chars'
  const builtInTokenSecret = findExternalIntegrationSourceTokenSecret(
    builtInExternalIntegrationTestSourceId,
    builtInExternalIntegrationTestTokenId
  )
  assert(builtInTokenSecret?.token, '内置测试 Token 应写入数据库并可用于回归测试')
  let builtInTestToken = builtInTokenSecret.token
  let builtInSuccessfulPublicCallCount = 0

  const source = upsertExternalIntegrationSource({
    name: sourceName,
    status: 'active',
    scopes: [externalIntegrationSourceAuthDemoScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: 'valid-demo-token',
    token: validToken,
    scopes: [externalIntegrationSourceAuthDemoScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: 'no-scope-demo-token',
    token: noScopeToken,
    scopes: []
  })
  const disabledSource = upsertExternalIntegrationSource({
    name: '已停用来源',
    status: 'disabled',
    scopes: [externalIntegrationSourceAuthDemoScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: disabledSource.id,
    name: 'disabled-source-token',
    token: disabledSourceToken,
    scopes: [externalIntegrationSourceAuthDemoScope]
  })
  const limitedSource = upsertExternalIntegrationSource({
    name: '限频来源',
    status: 'active',
    scopes: [externalIntegrationSourceAuthDemoScope],
    rateLimits: [{ windowSeconds: 10, maxRequests: 2 }]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: limitedSource.id,
    name: 'limited-source-token',
    token: limitedSourceToken,
    scopes: [externalIntegrationSourceAuthDemoScope]
  })
  const ipUsageSource = upsertExternalIntegrationSource({
    name: 'IP 聚合来源',
    status: 'active',
    scopes: [externalIntegrationIpUsageReadScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: ipUsageSource.id,
    name: 'ip-usage-token',
    token: ipUsageToken,
    scopes: [externalIntegrationIpUsageReadScope]
  })
  const accountUsageSource = upsertExternalIntegrationSource({
    name: '账号聚合来源',
    status: 'active',
    scopes: [externalIntegrationAccountUsageReadScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: accountUsageSource.id,
    name: 'account-usage-token',
    token: accountUsageToken,
    scopes: [externalIntegrationAccountUsageReadScope]
  })
  const consumptionRankingSource = upsertExternalIntegrationSource({
    name: '消耗排行来源',
    status: 'active',
    scopes: [externalIntegrationConsumptionRankingReadScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: consumptionRankingSource.id,
    name: 'consumption-ranking-token',
    token: consumptionRankingToken,
    scopes: [externalIntegrationConsumptionRankingReadScope]
  })
  const accessInfoSource = upsertExternalIntegrationSource({
    name: '接入信息来源',
    status: 'active',
    scopes: [externalIntegrationAccessInfoReadScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: accessInfoSource.id,
    name: 'access-info-token',
    token: accessInfoToken,
    scopes: [externalIntegrationAccessInfoReadScope]
  })
  const resourceControlScopes = [
    externalIntegrationGroupListReadScope,
    externalIntegrationApiKeyListReadScope,
    externalIntegrationAccountListReadScope,
    externalIntegrationGroupAddWriteScope,
    externalIntegrationGroupUpdateWriteScope,
    externalIntegrationGroupDeleteWriteScope,
    externalIntegrationApiKeyAddWriteScope,
    externalIntegrationApiKeyUpdateWriteScope,
    externalIntegrationApiKeyDeleteWriteScope,
    externalIntegrationAccountAddWriteScope,
    externalIntegrationAccountUpdateWriteScope,
    externalIntegrationAccountDeleteWriteScope
  ]
  const accountWriteSource = upsertExternalIntegrationSource({
    name: '公开资源写入来源',
    status: 'active',
    scopes: resourceControlScopes
  })
  createExternalIntegrationSourceToken({
    sourceRefId: accountWriteSource.id,
    name: 'account-write-token',
    token: accountWriteToken,
    scopes: resourceControlScopes
  })
  seedClientIpUsageWindow(clientIpStats, usageStatsHelpers, getStatsDatabase)
  const seededUsageAccount = seedAccountUsageWindow(createAccount, createGroup, usageStatsHelpers, getStatsDatabase)

  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', publicApiPrefix: '/__aipublic__' })
  const server = await listen(app)
  const address = server.address()
  assert(address && typeof address !== 'string', '测试 HTTP 服务地址无效')
  const baseUrl = `http://127.0.0.1:${address.port}`
  assert.equal(updateSystemAccount('sys_admin', { mustChangePassword: false })?.mustChangePassword, false, '回归准备：管理员应可访问管理 API')
  const adminCookie = `juhe_ai_session=${createSession('sys_admin', 1).token}`

  try {
    const apiDocs = await requestJson(baseUrl, '/__aisys__/api/external-integration-sources/api-docs', {
      Cookie: adminCookie
    })
    assert.equal(apiDocs.status, 200)
    assert.equal(Object.prototype.hasOwnProperty.call(apiDocs.body.data, 'testToken'), false, '公开接口接入文档不能返回内置测试 Token 明文')
    assert.equal(Object.prototype.hasOwnProperty.call(apiDocs.body.data, 'testTokenName'), false, '公开接口接入文档不能返回测试 Token 字段')
    assert.equal(JSON.stringify(apiDocs.body.data).includes(builtInTestToken), false, '公开接口接入文档不能包含内置测试 Token 明文')
    assert(apiDocs.body.data.items.every((item: any) => item.status === 'mock'), '公开接口接入文档应统一标识为 Mock 数据接口')

    const builtInList = await requestJson(baseUrl, '/__aisys__/api/external-integration-sources?pageSize=100', {
      Cookie: adminCookie
    })
    assert.equal(builtInList.status, 200)
    const builtInSource = builtInList.body.data.items.find((item: any) => item.id === builtInExternalIntegrationTestSourceId)
    assert(builtInSource, '外部来源授权列表应默认包含内置测试来源')
    assert.equal(builtInSource.isBuiltIn, true, '内置测试来源应带只读标识')
    assert.equal(builtInSource.name, '内置测试来源')
    assert.deepEqual(builtInSource.scopes, externalIntegrationScopeOptions.map((item) => item.value).sort(), '内置测试来源应授权全部公开接口资源')
    assert.deepEqual(builtInSource.rateLimits, [{ windowSeconds: 60, maxRequests: 10 }], '内置测试来源应固定为 1 分钟 10 次')
    const builtInToken = builtInSource.tokens.find((token: any) => token.id === builtInExternalIntegrationTestTokenId)
    assert(builtInToken, '内置测试来源应默认包含内置测试 Token')
    assert.equal(builtInToken.isBuiltIn, true, '内置测试 Token 应带只读标识')
    assert.deepEqual(builtInToken.scopes, builtInSource.scopes, '内置测试 Token 应同步授权全部公开接口资源')

    const builtInSourceEdit = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInExternalIntegrationTestSourceId}`, {
      Cookie: adminCookie
    }, 'PATCH', {
      name: '不应允许改名'
    })
    assert.equal(builtInSourceEdit.status, 400, '内置测试来源不应允许编辑基础信息')

    const builtInTokenCreate = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInExternalIntegrationTestSourceId}/tokens`, {
      Cookie: adminCookie
    }, 'POST', {
      name: '不应新增的 Token',
      status: 'active',
      scopes: [externalIntegrationSourceAuthDemoScope]
    })
    assert.equal(builtInTokenCreate.status, 400, '内置测试来源不应允许新增 Token')

    const builtInTokenEdit = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInExternalIntegrationTestSourceId}/tokens/${builtInExternalIntegrationTestTokenId}`, {
      Cookie: adminCookie
    }, 'PATCH', {
      status: 'disabled'
    })
    assert.equal(builtInTokenEdit.status, 400, '内置测试 Token 不应允许直接编辑')

    const builtInDelete = await requestStatus(baseUrl, `/__aisys__/api/external-integration-sources/${builtInExternalIntegrationTestSourceId}`, {
      Cookie: adminCookie
    }, 'DELETE')
    assert.equal(builtInDelete, 400, '内置测试来源不应允许删除')

    const disabledBuiltIn = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInExternalIntegrationTestSourceId}`, {
      Cookie: adminCookie
    }, 'PATCH', {
      status: 'disabled'
    })
    assert.equal(disabledBuiltIn.status, 200, '内置测试来源应允许停用')
    assert.equal(disabledBuiltIn.body.data.status, 'disabled')
    const disabledBuiltInAuth = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${builtInTestToken}`
    })
    assert.equal(disabledBuiltInAuth.status, 403, '内置测试来源停用后公开接口应拒绝调用')
    assert.equal(disabledBuiltInAuth.body.code, 'external_source_disabled')

    const enabledBuiltIn = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${builtInExternalIntegrationTestSourceId}`, {
      Cookie: adminCookie
    }, 'PATCH', {
      status: 'active'
    })
    assert.equal(enabledBuiltIn.status, 200, '内置测试来源应允许重新启用')
    assert.equal(enabledBuiltIn.body.data.status, 'active')

    const previousBuiltInTestToken = builtInTestToken
    const resetBuiltIn = await requestJson(baseUrl, '/__aisys__/api/external-integration-sources/built-in-test-token/reset', {
      Cookie: adminCookie
    }, 'POST')
    assert.equal(resetBuiltIn.status, 200, '管理员应能重置内置测试 Token')
    assert(resetBuiltIn.body.data.token.token, '重置内置测试 Token 应一次性返回明文')
    assert.notEqual(resetBuiltIn.body.data.token.token, previousBuiltInTestToken, '重置内置测试 Token 应生成新明文')
    builtInTestToken = resetBuiltIn.body.data.token.token
    const oldBuiltInTokenAuth = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${previousBuiltInTestToken}`
    })
    assert.equal(oldBuiltInTokenAuth.status, 401, '重置后旧内置测试 Token 应立即失效')

    const missingToken = await requestJson(baseUrl, '/__aipublic__/demo/source-auth')
    assert.equal(missingToken.status, 401)
    assert.equal(missingToken.body.code, 'external_source_token_missing')

    const wrongToken = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: 'Bearer juis_wrong_token'
    })
    assert.equal(wrongToken.status, 401)
    assert.equal(wrongToken.body.code, 'external_source_unauthorized')

    const noScope = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${noScopeToken}`
    })
    assert.equal(noScope.status, 403)
    assert.equal(noScope.body.code, 'external_source_scope_forbidden')

    const disabledSource = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${disabledSourceToken}`
    })
    assert.equal(disabledSource.status, 403)
    assert.equal(disabledSource.body.code, 'external_source_disabled')

    const success = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${validToken}`
    })
    assert.equal(success.status, 200)
    assert.equal(success.body.data.sourceName, sourceName)
    assert.equal(success.body.data.tokenPrefix, validToken.slice(0, 8))
    assert.equal(Object.prototype.hasOwnProperty.call(success.body.data, 'token'), false, '成功响应不能返回明文 token')
    assert.equal(Object.prototype.hasOwnProperty.call(success.body.data, 'scopes'), false, '成功响应不应返回授权能力明细')

    const testTokenSuccess = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${builtInTestToken}`
    })
    assert.equal(testTokenSuccess.status, 200)
    builtInSuccessfulPublicCallCount += 1
    assert.equal(testTokenSuccess.body.data.sourceName, '内置测试来源')
    assert.equal(testTokenSuccess.body.data.mock, true)

    const removedMockRanking = await requestJson(baseUrl, '/__aipublic__/demo/mock-ranking?range=last7d&limit=3', {
      Authorization: `Bearer ${builtInTestToken}`
    })
    assert.equal(removedMockRanking.status, 404, '公益榜 Mock Demo 已移除')

    const ipUsageNoScope = await requestJson(baseUrl, '/__aipublic__/ip/usage?range=today&pageSize=5', {
      Authorization: `Bearer ${validToken}`
    })
    assert.equal(ipUsageNoScope.status, 403)
    assert.equal(ipUsageNoScope.body.code, 'external_source_scope_forbidden', 'IP 用量接口必须使用独立 scope')

    const mockIpUsage = await requestJson(baseUrl, '/__aipublic__/ip/usage?range=last7d&pageSize=2', {
      Authorization: `Bearer ${builtInTestToken}`
    })
    assert.equal(mockIpUsage.status, 200)
    builtInSuccessfulPublicCallCount += 1
    assert.equal(mockIpUsage.body.data.source, 'mock')
    assert.equal(mockIpUsage.body.data.items.length, 2)
    assert.equal(mockIpUsage.body.data.items[0].dimension, 'client_ip')
    assert.equal(Object.prototype.hasOwnProperty.call(mockIpUsage.body.data.items[0], 'ipHash'), false, '公开 IP 聚合不返回内部 hash')
    assert.equal(Object.prototype.hasOwnProperty.call(mockIpUsage.body.data.items[0], 'status'), false, '公开 IP 聚合不返回后台封禁状态')

    const ipUsage = await requestJson(baseUrl, '/__aipublic__/ip/usage?range=today&pageSize=5&sortField=requestCount&sortOrder=desc', {
      Authorization: `Bearer ${ipUsageToken}`
    })
    assert.equal(ipUsage.status, 200)
    assert.equal(ipUsage.body.data.source, 'stats')
    assert.equal(ipUsage.body.data.rangeReady, true)
    assert.equal(ipUsage.body.data.items[0].ip, '203.0.113.10')
    assert.equal(ipUsage.body.data.items[0].requestCount, 3)
    assert.equal(ipUsage.body.data.items[0].cacheReadTokens, 11)
    assert.equal(ipUsage.body.data.items[0].averageFirstTokenMs, 30)
    assert.equal(ipUsage.body.data.items[0].averageDurationMs, 240)
    assert.equal(ipUsage.body.data.items[0].maxDurationMs, 400)

    const ipUsageWithAccountUsageScope = await requestJson(baseUrl, '/__aipublic__/ip/usage?range=today&pageSize=5', {
      Authorization: `Bearer ${accountUsageToken}`
    })
    assert.equal(ipUsageWithAccountUsageScope.status, 403)
    assert.equal(ipUsageWithAccountUsageScope.body.code, 'external_source_scope_forbidden', 'IP 用量和账号用量必须是两个独立接口资源')

    const accountUsageNoScope = await requestJson(baseUrl, '/__aipublic__/account/usage?range=today&pageSize=5', {
      Authorization: `Bearer ${validToken}`
    })
    assert.equal(accountUsageNoScope.status, 403)
    assert.equal(accountUsageNoScope.body.code, 'external_source_scope_forbidden', '账号用量接口必须使用自己的接口 scope')

    const accountUsageWithIpScope = await requestJson(baseUrl, '/__aipublic__/account/usage?range=today&pageSize=5', {
      Authorization: `Bearer ${ipUsageToken}`
    })
    assert.equal(accountUsageWithIpScope.status, 403)
    assert.equal(accountUsageWithIpScope.body.code, 'external_source_scope_forbidden', '账号用量不能复用 IP 用量接口 scope')

    const mockAccountUsage = await requestJson(baseUrl, '/__aipublic__/account/usage?range=last7d&pageSize=2', {
      Authorization: `Bearer ${builtInTestToken}`
    })
    assert.equal(mockAccountUsage.status, 200)
    builtInSuccessfulPublicCallCount += 1
    assert.equal(mockAccountUsage.body.data.source, 'mock')
    assert.equal(mockAccountUsage.body.data.items.length, 2)
    assert.equal(mockAccountUsage.body.data.items[0].dimension, 'account')
    assert.equal(Object.prototype.hasOwnProperty.call(mockAccountUsage.body.data.items[0], 'credentials'), false, '公开账号聚合不返回上游凭据')

    const accountUsage = await requestJson(baseUrl, '/__aipublic__/account/usage?range=today&pageSize=5&sortField=totalTokens&sortOrder=desc', {
      Authorization: `Bearer ${accountUsageToken}`
    })
    assert.equal(accountUsage.status, 200)
    assert.equal(accountUsage.body.data.source, 'stats')
    assert.equal(accountUsage.body.data.rangeReady, true)
    assert.equal(accountUsage.body.data.items[0].accountId, seededUsageAccount.id)
    assert.equal(accountUsage.body.data.items[0].accountName, seededUsageAccount.name)
    assert.equal(accountUsage.body.data.items[0].totalTokens, 1500)
    assert.equal(accountUsage.body.data.items[0].successCount, 3)
    assert.equal(accountUsage.body.data.items[0].errorCount, 1)
    assert.equal(accountUsage.body.data.items[0].activeDays, 1)

    const customAccountUsage = await requestJson(baseUrl, '/__aipublic__/account/usage?startDate=2026-05-24&endDate=2026-05-30', {
      Authorization: `Bearer ${accountUsageToken}`
    })
    assert.equal(customAccountUsage.status, 400)
    assert.match(customAccountUsage.body.message, /暂不支持自定义日期范围/, '公开账号聚合接口不应接受未预生成的自定义窗口')

    const customIpUsage = await requestJson(baseUrl, '/__aipublic__/ip/usage?startDate=2026-05-24&endDate=2026-05-30', {
      Authorization: `Bearer ${ipUsageToken}`
    })
    assert.equal(customIpUsage.status, 400)
    assert.match(customIpUsage.body.message, /暂不支持自定义日期范围/, '公开 IP 聚合接口不应宣称并接受未预生成的自定义窗口')

    const consumptionRanking = await requestJson(baseUrl, '/__aipublic__/consumption/ranking?range=today&limit=1&metric=requestCount', {
      Authorization: `Bearer ${consumptionRankingToken}`
    })
    assert.equal(consumptionRanking.status, 200)
    assert.equal(consumptionRanking.body.data.dimension, 'client_ip')
    assert.equal(consumptionRanking.body.data.items[0].name, '203.0.113.10')
    assert.equal(consumptionRanking.body.data.items[0].metricValue, 3)

    const accessInfo = await requestJson(baseUrl, '/__aipublic__/access/info', {
      Authorization: `Bearer ${accessInfoToken}`
    })
    assert.equal(accessInfo.status, 200)
    assert.equal(accessInfo.body.data.dataDimension, 'client_ip')
    assert.deepEqual(accessInfo.body.data.supportedDimensions, ['client_ip', 'account'])
    assert.deepEqual(accessInfo.body.data.supportedRanges, ['today', 'last7d', 'last31d'], '接入信息只能声明后台已维护的固定窗口')
    const accessInfoEndpointKeys = accessInfo.body.data.endpoints
      .map((endpoint: any) => `${endpoint.method} ${endpoint.path}`)
      .sort()
    assert.deepEqual(accessInfoEndpointKeys, [
      'GET /__aipublic__/access/info',
      'GET /__aipublic__/account/list',
      'GET /__aipublic__/account/usage',
      'GET /__aipublic__/api-key/list',
      'GET /__aipublic__/consumption/ranking',
      'GET /__aipublic__/group/list',
      'GET /__aipublic__/ip/usage',
      'POST /__aipublic__/account/add',
      'POST /__aipublic__/account/del',
      'POST /__aipublic__/account/update',
      'POST /__aipublic__/api-key/add',
      'POST /__aipublic__/api-key/del',
      'POST /__aipublic__/api-key/update',
      'POST /__aipublic__/group/add',
      'POST /__aipublic__/group/del',
      'POST /__aipublic__/group/update'
    ].sort(), '接入信息应完整声明当前所有公开接口')
    assert(accessInfo.body.data.boundary.provides.includes('账号维度实际请求数、Token、缓存、成本、活跃天数和速度指标聚合'), '接入信息应声明账号实际用量事实')
    assert(accessInfo.body.data.boundary.provides.includes('分组、API Key 和账号的受控脱敏列表、新增、修改与删除入口'), '接入信息应声明资源列表和写入入口')
    assert(accessInfo.body.data.boundary.notProvided.includes('公益站用户维度排行榜快照'), '接入信息应明确公益站业务快照不由 sub2api-lite 提供')

    const accountAddNoScope = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${ipUsageToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      name: '公益站测试账号',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-001'
    })
    assert.equal(accountAddNoScope.status, 403)
    assert.equal(accountAddNoScope.body.code, 'external_source_scope_forbidden', '账号新增接口必须使用独立写入 scope')

    const accountDeleteNoScope = await requestJson(baseUrl, '/__aipublic__/account/del', {
      Authorization: `Bearer ${ipUsageToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      name: '公益站测试账号'
    })
    assert.equal(accountDeleteNoScope.status, 403)
    assert.equal(accountDeleteNoScope.body.code, 'external_source_scope_forbidden', '账号删除接口必须使用独立写入 scope')

    const mockAccountAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${builtInTestToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      name: '公益站测试账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-mock',
      supportedModels: ['gpt-5.5']
    })
    assert.equal(mockAccountAdd.status, 200)
    builtInSuccessfulPublicCallCount += 1
    assert.equal(mockAccountAdd.body.data.source, 'mock')
    assert.equal(Object.prototype.hasOwnProperty.call(mockAccountAdd.body.data.account, 'credentials'), false, '账号新增响应不能返回凭据')

    const mockAccountDelete = await requestJson(baseUrl, '/__aipublic__/account/del', {
      Authorization: `Bearer ${builtInTestToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: 'acc_mock_delete'
    })
    assert.equal(mockAccountDelete.status, 200)
    builtInSuccessfulPublicCallCount += 1
    assert.equal(mockAccountDelete.body.data.source, 'mock')
    assert.equal(mockAccountDelete.body.data.action, 'mock')
    assert.equal(Object.prototype.hasOwnProperty.call(mockAccountDelete.body.data.account, 'credentials'), false, '账号删除 mock 响应不能返回凭据')

    const illegalTypeAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'illegal_type_user',
      targetGroupName: '非法类型分组',
      providerCode: 'gpt',
      name: '非法 OAuth 新增账号',
      type: 'oauth',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-illegal-type'
    })
    assert.equal(illegalTypeAdd.status, 400)
    assert.match(illegalTypeAdd.body.message, /仅支持 API Key/, '账号新增不应接受 OAuth 或其他非 API Key 类型')
    assert.equal(findSystemAccountByUsername('illegal_type_user'), undefined, '非法类型在路由校验阶段不应创建目标用户')

    const invalidModelAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'invalid_model_user',
      targetGroupName: '无效模型分组',
      providerCode: 'gpt',
      name: '无效模型新增账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-invalid-model',
      supportedModels: ['definitely-not-a-real-model']
    })
    assert.equal(invalidModelAdd.status, 400)
    assert.match(invalidModelAdd.body.message, /账户支持模型不在供应商模型目录中/, '无效模型应由账号校验拒绝')
    assert.equal(findSystemAccountByUsername('invalid_model_user'), undefined, '账号创建失败后不应残留自动创建的目标用户')
    const invalidModelGroupResidue = getBusinessDatabase()
      .prepare("SELECT COUNT(*) AS total FROM groups WHERE name = '无效模型分组'")
      .get() as { total?: number } | undefined
    assert.equal(Number(invalidModelGroupResidue?.total ?? 0), 0, '账号创建失败后不应残留自动创建的目标分组')

    const legacyExternalIdAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      name: '旧外部登记字段账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-legacy-external-id',
      supportedModels: ['gpt-5.5'],
      externalId: 'account-registration:legacy'
    })
    assert.equal(legacyExternalIdAdd.status, 400, '公开账号新增不应再接收 externalId')

    const accountAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      name: '公益站测试账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-001',
      supportedModels: ['gpt-5.5'],
      status: 'active',
      availabilitySchedule: {
        enabled: true,
        timezone: 'UTC',
        mode: 'allow_windows',
        windows: [
          { daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }
        ]
      }
    })
    assert.equal(accountAdd.status, 201, `公开账号新增应成功，实际响应：${JSON.stringify(accountAdd.body)}`)
    assert.equal(accountAdd.body.data.source, 'stats')
    assert.equal(accountAdd.body.data.action, 'created')
    assert.equal(accountAdd.body.data.target.username, 'huanmin')
    assert.equal(accountAdd.body.data.target.groupName, '福利')
    assert.equal(accountAdd.body.data.target.created, true)
    assert.equal(accountAdd.body.data.target.groupCreated, true)
    assert.equal(accountAdd.body.data.account.name, '公益站测试账号')
    assert.equal(accountAdd.body.data.account.status, 'pending_test', '公开账号新增即使传 active 也应先落成待测试')
    assert.equal(accountAdd.body.data.account.availabilitySchedule?.enabled, true, '公开账号新增应写入并回显可用时段计划')
    assert.equal(Object.prototype.hasOwnProperty.call(accountAdd.body.data.account, 'credentials'), false, '正式新增响应不能返回凭据')

    const targetAccount = findSystemAccountByUsername('huanmin')
    assert(targetAccount, '账号新增应自动创建目标用户 huanmin')
    const targetAccess = { systemAccountId: targetAccount.id, role: 'user' as const }
    const welfareGroup = listGroupOptions(targetAccess, { keyword: '福利', providerCode: 'gpt' })
      .find((item) => item.name === '福利')
    assert(welfareGroup, '账号新增应自动创建福利分组')
    const addedAccount = listAccounts(targetAccess, { keyword: '公益站测试账号', providerCode: 'gpt', groupId: welfareGroup.id })
      .find((item) => item.name === '公益站测试账号')
    assert(addedAccount, '账号新增应把账号绑定到福利分组')
    assert.equal(addedAccount.boundGroupId, welfareGroup.id)
    assert.equal(addedAccount.availabilitySchedule?.enabled, true, '公开账号新增应持久化可用时段计划')
    const addLogs = listOperationLogs({
      module: 'external_integrations',
      action: 'account_add',
      resourceId: addedAccount.id,
      pageSize: 10
    })
    assert.equal(addLogs.items.length, 1, '正式账号新增应写入可追踪的操作日志')
    const addLogDetail = getOperationLogDetail(addLogs.items[0].id)
    assert(addLogDetail, '正式账号新增操作日志应可读取详情')
    assert.equal(addLogDetail.metadata?.sourceRefId, accountWriteSource.id, '操作日志详情应记录来源系统 ID')
    assert.equal(addLogDetail.metadata?.tokenPrefix, accountWriteToken.slice(0, 8), '操作日志详情应记录来源 token 前缀')
    assert.equal(readAccountCredentials(addedAccount.id).api_key, 'sk-public-push-regression-001', '公开账号新增应真实写入上游 API Key')

    const accountList = await requestJson(baseUrl, `/__aipublic__/account/list?targetUsername=huanmin&targetGroupName=${encodeURIComponent('福利')}&providerCode=gpt&pageSize=10`, {
      Authorization: `Bearer ${accountWriteToken}`
    })
    assert.equal(accountList.status, 200)
    assert.equal(accountList.body.data.source, 'stats')
    const listedAccount = accountList.body.data.items.find((item: any) => item.id === accountAdd.body.data.account.id)
    assert(listedAccount, '公开账号列表应能按目标用户和分组返回刚新增的账号')
    assert.equal(Object.prototype.hasOwnProperty.call(listedAccount, 'externalId'), false, '公开账号列表不应回显外部来源系统业务 ID')
    assert.equal(Object.prototype.hasOwnProperty.call(listedAccount, 'credentials'), false, '公开账号列表不能返回上游凭据')

    const accountKeyUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: accountAdd.body.data.account.id,
      name: '公益站测试账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v2',
      apiKey: 'sk-public-push-regression-rotated'
    })
    assert.equal(accountKeyUpdate.status, 200, `待测试账号修改凭据不应被隐式 active 状态拦截：${JSON.stringify(accountKeyUpdate.body)}`)
    assert.equal(accountKeyUpdate.body.data.action, 'updated')
    assert.equal(accountKeyUpdate.body.data.account.status, 'pending_test', '公开账号修改未提交 status 时应保留待测试状态')
    const rotatedCredentials = readAccountCredentials(addedAccount.id)
    assert.equal(rotatedCredentials.api_key, 'sk-public-push-regression-rotated', '公开账号修改应覆盖旧上游 API Key')
    assert.equal(rotatedCredentials.base_url, 'https://push.example/v2', '公开账号修改应覆盖 Base URL')
    const rotatedAccount = listAccounts(targetAccess, { keyword: '公益站测试账号', providerCode: 'gpt', groupId: welfareGroup.id })
      .find((item) => item.id === addedAccount.id)
    assert.deepEqual(rotatedAccount?.supportedModels, ['gpt-5.5'], '公开账号修改未提交 supportedModels 时应保留原模型限制')

    const activatedAccount = clearAccountFailureStateResult(addedAccount.id, targetAccess, { allowPendingTestRestore: true })
    assert.equal(activatedAccount.account?.status, 'active', '回归准备：待测试账号应通过测试成功入口恢复正常')

    const accountUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: accountAdd.body.data.account.id,
      name: '公益站测试账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v2',
      apiKey: 'sk-public-push-regression-rotated',
      supportedModels: ['gpt-5.5', 'gpt-5.4'],
      status: 'disabled'
    })
    assert.equal(accountUpdate.status, 200)
    assert.equal(accountUpdate.body.data.action, 'updated')
    assert.equal(accountUpdate.body.data.account.id, accountAdd.body.data.account.id)
    assert.equal(accountUpdate.body.data.account.status, 'disabled')
    assert.equal(accountUpdate.body.data.account.availabilitySchedule?.enabled, true, '公开账号修改未提交计划时不应清空既有可用时段计划')

    const accountRename = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: accountAdd.body.data.account.id,
      name: '公益站测试账号新版',
      type: 'api_key',
      baseUrl: 'https://push.example/v2',
      apiKey: 'sk-public-push-regression-rotated',
      supportedModels: ['gpt-5.5', 'gpt-5.4'],
      status: 'active',
      availabilitySchedule: null
    })
    assert.equal(accountRename.status, 200)
    assert.equal(accountRename.body.data.action, 'updated')
    assert.equal(accountRename.body.data.account.id, accountAdd.body.data.account.id)
    assert.equal(accountRename.body.data.account.name, '公益站测试账号新版')
    assert.equal(accountRename.body.data.account.status, 'active')
    assert.equal(accountRename.body.data.account.availabilitySchedule, undefined, '公开账号修改应支持 availabilitySchedule: null 清空计划')
    const renamedAccount = listAccounts(targetAccess, { keyword: '公益站测试账号新版', providerCode: 'gpt', groupId: welfareGroup.id })
      .find((item) => item.name === '公益站测试账号新版')
    assert.equal(renamedAccount?.id, addedAccount.id, '按 accountId 修改账号时应更新原账号，不能因名称变化创建新账号')

    const missingAccountUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: 'acc_public_missing_update',
      name: '不存在的账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-missing-update',
      status: 'active'
    })
    assert.equal(missingAccountUpdate.status, 404, '账号修改接口找不到账号时不应自动新增')

    const missingTargetAccountUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'missing_public_account_update_user',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: accountAdd.body.data.account.id,
      name: '目标用户不存在的账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-missing-target'
    })
    assert.equal(missingTargetAccountUpdate.status, 404, '账号修改接口目标用户不存在时不应自动创建用户')

    assert.equal(updateSystemAccount(targetAccount.id, { status: 'disabled' })?.status, 'disabled', '回归准备：目标用户 huanmin 应可被停用')
    const disabledTargetAccountList = await requestJson(baseUrl, `/__aipublic__/account/list?targetUsername=huanmin&targetGroupName=${encodeURIComponent('福利')}&providerCode=gpt`, {
      Authorization: `Bearer ${accountWriteToken}`
    })
    assert.equal(disabledTargetAccountList.status, 400, '目标用户停用后公开账号列表应被拒绝')
    assert.match(disabledTargetAccountList.body.message, /目标用户已停用/)

    const disabledTargetAccountAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      name: '停用用户不应新增的账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-disabled-target-add'
    })
    assert.equal(disabledTargetAccountAdd.status, 400, '目标用户停用后公开账号新增应被拒绝')
    assert.match(disabledTargetAccountAdd.body.message, /目标用户已停用/)

    const disabledTargetAccountUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: accountAdd.body.data.account.id,
      name: '停用用户不应修改的账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-disabled-target-update'
    })
    assert.equal(disabledTargetAccountUpdate.status, 400, '目标用户停用后公开账号修改应被拒绝')
    assert.match(disabledTargetAccountUpdate.body.message, /目标用户已停用/)

    const disabledTargetAccountDelete = await requestJson(baseUrl, '/__aipublic__/account/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: accountAdd.body.data.account.id
    })
    assert.equal(disabledTargetAccountDelete.status, 400, '目标用户停用后公开账号删除应被拒绝')
    assert.match(disabledTargetAccountDelete.body.message, /目标用户已停用/)
    assert.equal(updateSystemAccount(targetAccount.id, { status: 'active' })?.status, 'active', '回归准备：目标用户 huanmin 应可恢复启用')

    const accountDelete = await requestJson(baseUrl, '/__aipublic__/account/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: accountAdd.body.data.account.id
    })
    assert.equal(accountDelete.status, 200)
    assert.equal(accountDelete.body.data.source, 'stats')
    assert.equal(accountDelete.body.data.action, 'deleted')
    assert.equal(accountDelete.body.data.account.id, accountAdd.body.data.account.id)
    const removedAccount = listAccounts(targetAccess, { keyword: '公益站测试账号新版', providerCode: 'gpt', groupId: welfareGroup.id })
      .find((item) => item.name === '公益站测试账号新版')
    assert.equal(removedAccount, undefined, '公开账号删除接口应真实删除 sub2api-lite 账号，而不是禁用')
    const deleteLogs = listOperationLogs({
      module: 'external_integrations',
      action: 'account_delete',
      resourceId: addedAccount.id,
      pageSize: 10
    })
    assert.equal(deleteLogs.items.length, 1, '正式账号删除应写入可追踪的操作日志')

    const accountDeleteAgain = await requestJson(baseUrl, '/__aipublic__/account/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      accountId: accountAdd.body.data.account.id
    })
    assert.equal(accountDeleteAgain.status, 200)
    assert.equal(accountDeleteAgain.body.data.action, 'not_found', '重复删除应幂等成功，便于公益站删除本地记录')
    assert.equal(accountDeleteAgain.body.data.account, null)

    const newPathIpUsage = await requestJson(baseUrl, '/__aipublic__/ip/usage?range=today&pageSize=5', {
      Authorization: `Bearer ${ipUsageToken}`
    })
    assert.equal(newPathIpUsage.status, 200, 'IP 聚合新路径应可用')

    const publicGroupAdd = await requestJson(baseUrl, '/__aipublic__/group/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      name: '公开接口控制分组',
      providerCode: 'gpt',
      description: '公开接口控制面回归',
      enabled: true
    })
    assert.equal(publicGroupAdd.status, 201)
    assert.equal(publicGroupAdd.body.data.action, 'created')
    const publicGroupId = publicGroupAdd.body.data.group.id as string

    const publicGroupList = await requestJson(baseUrl, `/__aipublic__/group/list?targetUsername=public_control_user&providerCode=gpt&keyword=${encodeURIComponent('公开接口控制')}`, {
      Authorization: `Bearer ${accountWriteToken}`
    })
    assert.equal(publicGroupList.status, 200)
    assert(publicGroupList.body.data.items.some((item: any) => item.id === publicGroupId), '公开分组列表应返回刚新增的分组')

    const publicGroupUpdate = await requestJson(baseUrl, '/__aipublic__/group/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      groupId: publicGroupId,
      name: '公开接口控制分组新版',
      enabled: true
    })
    assert.equal(publicGroupUpdate.status, 200)
    assert.equal(publicGroupUpdate.body.data.action, 'updated')
    assert.equal(publicGroupUpdate.body.data.group.name, '公开接口控制分组新版')

    const missingPublicGroupUpdate = await requestJson(baseUrl, '/__aipublic__/group/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      groupId: 'grp_public_missing_update',
      name: '不存在的公开接口分组'
    })
    assert.equal(missingPublicGroupUpdate.status, 404, '公开分组修改找不到分组时应返回 404')
    assert.match(missingPublicGroupUpdate.body.message, /分组不存在/)

    const missingPublicGroupDelete = await requestJson(baseUrl, '/__aipublic__/group/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      groupId: 'grp_public_missing_delete'
    })
    assert.equal(missingPublicGroupDelete.status, 404, '公开分组删除找不到分组时应返回 404')
    assert.match(missingPublicGroupDelete.body.message, /分组不存在/)

    const missingTargetPublicApiKeyAdd = await requestJson(baseUrl, '/__aipublic__/api-key/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_api_key_missing_user',
      name: '目标用户不存在的公开 API Key',
      groupBindings: [{ groupId: publicGroupId }],
      status: 'active'
    })
    assert.equal(missingTargetPublicApiKeyAdd.status, 400, '公开 API Key 新增要求目标用户已存在')
    assert.match(missingTargetPublicApiKeyAdd.body.message, /目标用户不存在/)

    const publicApiKeyAdd = await requestJson(baseUrl, '/__aipublic__/api-key/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      name: '公开接口控制 Key',
      groupBindings: [{ groupId: publicGroupId }],
      status: 'active',
      availabilitySchedule: {
        enabled: true,
        mode: 'allow_windows',
        timezone: 'UTC',
        windows: [
          { daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }
        ]
      }
    })
    assert.equal(publicApiKeyAdd.status, 201)
    assert.equal(publicApiKeyAdd.body.data.action, 'created')
    assert.equal(typeof publicApiKeyAdd.body.data.apiKey.key, 'string', 'API Key 新增响应应只在创建时返回明文密钥')
    assert.equal(publicApiKeyAdd.body.data.apiKey.availabilitySchedule?.enabled, true, '公开 API Key 新增应写入并回显可用时段计划')
    const publicApiKeyId = publicApiKeyAdd.body.data.apiKey.id as string

    const publicApiKeyList = await requestJson(baseUrl, `/__aipublic__/api-key/list?targetUsername=public_control_user&groupId=${encodeURIComponent(publicGroupId)}`, {
      Authorization: `Bearer ${accountWriteToken}`
    })
    assert.equal(publicApiKeyList.status, 200)
    const listedApiKey = publicApiKeyList.body.data.items.find((item: any) => item.id === publicApiKeyId)
    assert(listedApiKey, '公开 API Key 列表应按绑定分组返回刚新增的 Key')
    assert.equal(Object.prototype.hasOwnProperty.call(listedApiKey, 'key'), false, '公开 API Key 列表不能返回明文密钥')

    const missingPublicApiKeyUpdate = await requestJson(baseUrl, '/__aipublic__/api-key/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      apiKeyId: 'key_public_missing_update',
      status: 'disabled'
    })
    assert.equal(missingPublicApiKeyUpdate.status, 404, '公开 API Key 修改找不到 Key 时应返回 404')
    assert.match(missingPublicApiKeyUpdate.body.message, /API Key 不存在/)

    const missingPublicApiKeyDelete = await requestJson(baseUrl, '/__aipublic__/api-key/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      apiKeyId: 'key_public_missing_delete'
    })
    assert.equal(missingPublicApiKeyDelete.status, 404, '公开 API Key 删除找不到 Key 时应返回 404')
    assert.match(missingPublicApiKeyDelete.body.message, /API Key 不存在/)

    const publicControlTarget = findSystemAccountByUsername('public_control_user')
    assert(publicControlTarget, '公开控制面新增分组应自动创建目标用户')
    assert.equal(updateSystemAccount(publicControlTarget.id, { status: 'disabled' })?.status, 'disabled', '回归准备：公开控制面目标用户应可被停用')

    const disabledPublicGroupList = await requestJson(baseUrl, '/__aipublic__/group/list?targetUsername=public_control_user', {
      Authorization: `Bearer ${accountWriteToken}`
    })
    assert.equal(disabledPublicGroupList.status, 400, '目标用户停用后公开分组列表应被拒绝')
    assert.match(disabledPublicGroupList.body.message, /目标用户已停用/)

    const disabledPublicApiKeyList = await requestJson(baseUrl, '/__aipublic__/api-key/list?targetUsername=public_control_user', {
      Authorization: `Bearer ${accountWriteToken}`
    })
    assert.equal(disabledPublicApiKeyList.status, 400, '目标用户停用后公开 API Key 列表应被拒绝')
    assert.match(disabledPublicApiKeyList.body.message, /目标用户已停用/)

    const disabledPublicAccountList = await requestJson(baseUrl, '/__aipublic__/account/list?targetUsername=public_control_user', {
      Authorization: `Bearer ${accountWriteToken}`
    })
    assert.equal(disabledPublicAccountList.status, 400, '目标用户停用后公开账号列表应被拒绝')
    assert.match(disabledPublicAccountList.body.message, /目标用户已停用/)

    const disabledPublicGroupAdd = await requestJson(baseUrl, '/__aipublic__/group/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      name: '停用用户不应新增的分组',
      providerCode: 'gpt'
    })
    assert.equal(disabledPublicGroupAdd.status, 400, '目标用户停用后公开分组新增应被拒绝')
    assert.match(disabledPublicGroupAdd.body.message, /目标用户已停用/)

    const disabledPublicGroupUpdate = await requestJson(baseUrl, '/__aipublic__/group/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      groupId: publicGroupId,
      name: '停用用户不应修改的分组'
    })
    assert.equal(disabledPublicGroupUpdate.status, 400, '目标用户停用后公开分组修改应被拒绝')
    assert.match(disabledPublicGroupUpdate.body.message, /目标用户已停用/)

    const disabledPublicGroupDelete = await requestJson(baseUrl, '/__aipublic__/group/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      groupId: publicGroupId
    })
    assert.equal(disabledPublicGroupDelete.status, 400, '目标用户停用后公开分组删除应被拒绝')
    assert.match(disabledPublicGroupDelete.body.message, /目标用户已停用/)

    const disabledPublicApiKeyAdd = await requestJson(baseUrl, '/__aipublic__/api-key/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      name: '停用用户不应新增的 API Key',
      groupBindings: [{ groupId: publicGroupId }],
      status: 'active'
    })
    assert.equal(disabledPublicApiKeyAdd.status, 400, '目标用户停用后公开 API Key 新增应被拒绝')
    assert.match(disabledPublicApiKeyAdd.body.message, /目标用户已停用/)

    const disabledPublicApiKeyUpdate = await requestJson(baseUrl, '/__aipublic__/api-key/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      apiKeyId: publicApiKeyId,
      status: 'disabled'
    })
    assert.equal(disabledPublicApiKeyUpdate.status, 400, '目标用户停用后公开 API Key 修改应被拒绝')
    assert.match(disabledPublicApiKeyUpdate.body.message, /目标用户已停用/)

    const disabledPublicApiKeyDelete = await requestJson(baseUrl, '/__aipublic__/api-key/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      apiKeyId: publicApiKeyId
    })
    assert.equal(disabledPublicApiKeyDelete.status, 400, '目标用户停用后公开 API Key 删除应被拒绝')
    assert.match(disabledPublicApiKeyDelete.body.message, /目标用户已停用/)
    assert.equal(updateSystemAccount(publicControlTarget.id, { status: 'active' })?.status, 'active', '回归准备：公开控制面目标用户应可恢复启用')

    const publicApiKeyUpdate = await requestJson(baseUrl, '/__aipublic__/api-key/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      apiKeyId: publicApiKeyId,
      status: 'disabled',
      groupBindings: [{ groupId: publicGroupId, priority: 1, weight: 1, status: 'active' }],
      groupRouteStrategy: 'round_robin',
      availabilitySchedule: null
    })
    assert.equal(publicApiKeyUpdate.status, 200)
    assert.equal(publicApiKeyUpdate.body.data.action, 'updated')
    assert.equal(Object.prototype.hasOwnProperty.call(publicApiKeyUpdate.body.data.apiKey, 'key'), false, 'API Key 修改响应不应返回明文密钥')
    assert.equal(publicApiKeyUpdate.body.data.apiKey.status, 'disabled')
    assert.equal(publicApiKeyUpdate.body.data.apiKey.groupRouteStrategy, 'round_robin', '公开 API Key 修改应支持 groupRouteStrategy')
    assert.equal(publicApiKeyUpdate.body.data.apiKey.groupBindings[0]?.groupId, publicGroupId, '公开 API Key 修改应支持 groupBindings 覆盖')
    assert.equal(publicApiKeyUpdate.body.data.apiKey.availabilitySchedule, undefined, '公开 API Key 修改应支持 availabilitySchedule: null 清空计划')

    const publicApiKeyDelete = await requestJson(baseUrl, '/__aipublic__/api-key/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      apiKeyId: publicApiKeyId
    })
    assert.equal(publicApiKeyDelete.status, 200)
    assert.equal(publicApiKeyDelete.body.data.action, 'deleted')

    const publicGroupDelete = await requestJson(baseUrl, '/__aipublic__/group/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      groupId: publicGroupId
    })
    assert.equal(publicGroupDelete.status, 200)
    assert.equal(publicGroupDelete.body.data.action, 'deleted')

    const managementCreatedSource = await requestJson(baseUrl, '/__aisys__/api/external-integration-sources', {
      Cookie: adminCookie
    }, 'POST', {
      name: '管理 API 删除回归来源',
      status: 'active',
      scopes: [externalIntegrationSourceAuthDemoScope],
      rateLimits: [],
      expiresAt: null,
      notes: '用于覆盖公开接口授权新增和删除'
    })
    assert.equal(managementCreatedSource.status, 201, '管理员应能新增公开接口来源授权')
    const managementSourceId = managementCreatedSource.body.data.source.id as string
    const managementTokenId = managementCreatedSource.body.data.token.id as string
    const managementToken = managementCreatedSource.body.data.token.token as string
    assert(managementSourceId, '新增公开接口来源授权应返回来源 ID')
    assert(managementTokenId, '新增公开接口来源授权应返回 token ID')
    assert(managementToken, '新增公开接口来源授权应一次性返回明文 token')

    const managementTokenSecret = await requestJson(baseUrl, `/__aisys__/api/external-integration-sources/${managementSourceId}/tokens/${managementTokenId}/secret`, {
      Cookie: adminCookie
    })
    assert.equal(managementTokenSecret.status, 200, '管理员应能按单条 Token 复制完整明文')
    assert.equal(managementTokenSecret.body.data.token, managementToken, 'Token 复制接口应返回创建时的完整 Token')

    const managementCreatedSourceAuth = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${managementToken}`
    })
    assert.equal(managementCreatedSourceAuth.status, 200, '新增来源授权生成的 token 应可访问公开接口')
    assert.equal(managementCreatedSourceAuth.body.data.sourceName, '管理 API 删除回归来源')

    const managementSourceDelete = await requestStatus(baseUrl, `/__aisys__/api/external-integration-sources/${managementSourceId}`, {
      Cookie: adminCookie
    }, 'DELETE')
    assert.equal(managementSourceDelete, 204, '管理员应能删除公开接口来源授权')

    const deletedSourceAuth = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${managementToken}`
    })
    assert.equal(deletedSourceAuth.status, 401, '删除来源授权后原 token 应立即失效')
    assert.equal(deletedSourceAuth.body.code, 'external_source_unauthorized')

    let builtInRateLimited: Awaited<ReturnType<typeof requestJson>> | undefined
    for (let index = 0; index < 11; index += 1) {
      const response = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
        Authorization: `Bearer ${builtInTestToken}`
      })
      if (response.status === 429) {
        builtInRateLimited = response
        break
      }
      assert.equal(response.status, 200, `内置测试 Token 连续限频探测第 ${index + 1} 次调用应在限频内或触发 429`)
    }
    assert(builtInRateLimited, `内置测试 Token 应在连续 11 次额外调用内触发 1 分钟 10 次限频，当前窗口前置成功调用约 ${builtInSuccessfulPublicCallCount} 次`)
    assert.equal(builtInRateLimited.body.code, 'external_source_rate_limited')

    await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${limitedSourceToken}`
    })
    await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${limitedSourceToken}`
    })
    const rateLimited = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${limitedSourceToken}`
    })
    assert.equal(rateLimited.status, 429)
    assert.equal(rateLimited.body.code, 'external_source_rate_limited')

    const protectedApi = await requestJson(baseUrl, '/__aisys__/api/accounts', {
      Authorization: `Bearer ${validToken}`
    })
    assert.equal(protectedApi.status, 401, '外部来源 token 不能绕过后台登录态接口')

    const lastUsedRow = getBusinessDatabase()
      .prepare(`
        SELECT tokens.last_used_at AS token_last_used_at, sources.last_used_at AS source_last_used_at
        FROM external_integration_source_tokens AS tokens
        JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
        WHERE sources.name = ? AND tokens.token_prefix = ?
      `)
      .get(sourceName, validToken.slice(0, 8)) as { token_last_used_at?: string | null; source_last_used_at?: string | null } | undefined
    assert(lastUsedRow?.token_last_used_at, '成功鉴权后应低频记录 token 最近调用时间')
    assert(lastUsedRow?.source_last_used_at, '成功鉴权后应低频记录来源系统最近调用时间')
  } finally {
    await closeServer(server)
    closeStorageDatabases()
  }

  console.log('外部来源系统鉴权和公开接口回归通过：公开前缀、Bearer token、测试 token mock、权限校验、停用来源、限频、后台登录边界、IP 聚合读取、账号新增/列表/修改/删除、分组和 API Key 新增/列表/修改/删除均符合预期')
}

function seedAccountUsageWindow(
  createAccount: typeof import('../../storage/repositories.js')['createAccount'],
  createGroup: typeof import('../../storage/repositories.js')['createGroup'],
  usageStatsHelpers: typeof import('../../storage/usage-stats-helpers.js'),
  getStatsDatabase: typeof import('../../storage/database.js')['getStatsDatabase']
): ReturnType<typeof createAccount> {
  const group = createGroup({
    providerCode: 'gpt',
    name: '公益站贡献统计分组',
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const account = createAccount({
    providerCode: 'gpt',
    name: '公益站贡献统计账号',
    type: 'api_key',
    groupId: group.id,
    credentials: {
      base_url: 'https://usage.example/v1',
      api_key: 'sk-public-account-usage-regression'
    }
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const today = usageStatsHelpers.dateKey(new Date(), usageStatsHelpers.usageStatsTimezone())
  const now = new Date().toISOString()
  const statsDatabase = getStatsDatabase()
  statsDatabase.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date,
      request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    ) VALUES (?, 'account', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    'global',
    account.id,
    today,
    4,
    3,
    1,
    1000,
    500,
    100,
    0.01,
    0.07,
    960,
    4,
    360,
    320,
    4,
    120,
    now,
    now,
    now
  )
  statsDatabase.prepare(`
    INSERT INTO usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
      last_used_at, last_error_at, updated_at
    ) VALUES (?, 'account', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    'global',
    account.id,
    today,
    today,
    4,
    3,
    1,
    1000,
    500,
    100,
    0.01,
    0.07,
    960,
    4,
    360,
    320,
    4,
    120,
    1,
    now,
    now,
    now
  )
  statsDatabase.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, lag_seconds, updated_at)
    VALUES ('global', '', 'usage_stats_aggregation', ?, 8, ?)
  `).run(now, now)
  return account
}

function seedClientIpUsageWindow(
  clientIpStats: typeof import('../../storage/client-ip-stats.repository.js'),
  usageStatsHelpers: typeof import('../../storage/usage-stats-helpers.js'),
  getStatsDatabase: typeof import('../../storage/database.js')['getStatsDatabase']
): void {
  const normalized = clientIpStats.normalizeClientIpForStats('203.0.113.10')
  assert(normalized, '测试 IP 应可规范化')
  const today = usageStatsHelpers.dateKey(new Date(), usageStatsHelpers.usageStatsTimezone())
  const now = new Date().toISOString()
  const statsDatabase = getStatsDatabase()
  statsDatabase.prepare(`
    INSERT INTO client_ip_registry (
      ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version,
      first_seen_at, last_seen_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    normalized.ipHash,
    normalized.bucketNo,
    normalized.aggregateIpKey,
    normalized.clientIp,
    normalized.ipVersion,
    now,
    now,
    now,
    now
  )
  statsDatabase.prepare(`
    INSERT INTO client_ip_usage_range_windows (
      ip_hash, start_date, end_date,
      request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
      first_token_ms_sum, first_token_ms_count, average_first_token_ms,
      active_days, last_used_at, last_error_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    normalized.ipHash,
    today,
    today,
    3,
    2,
    1,
    141,
    22,
    11,
    0.01,
    0.02,
    720,
    3,
    400,
    240,
    90,
    3,
    30,
    1,
    now,
    now,
    now
  )
  statsDatabase.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, updated_at)
    VALUES ('client_ip_range_window', ?, 'client_ip_range_window_refresh', ?, ?)
  `).run(`${today}:${today}`, now, now)
  statsDatabase.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, lag_seconds, updated_at)
    VALUES ('global', '', 'client_ip_stats_aggregation', ?, 12, ?)
  `).run(now, now)
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

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
    externalIntegrationAccountPushScope,
    externalIntegrationIpUsageReadScope,
    externalIntegrationSourceAuthDemoScope,
    externalIntegrationTestToken,
    upsertExternalIntegrationSource
  } = await import('../../storage/external-integration-source.repository.js')
  const {
    findSystemAccountByUsername,
    getOperationLogDetail,
    listOperationLogs,
    listAccounts,
    listGroupOptions
  } = await import('../../storage/repositories.js')
  const { closeStorageDatabases, getDatabase, getStatsDatabase } = await import('../../storage/database.js')
  const clientIpStats = await import('../../storage/client-ip-stats.repository.js')
  const usageStatsHelpers = await import('../../storage/usage-stats-helpers.js')

  const sourceName = 'juhe-ai公益站'
  const validToken = 'juis_valid_external_source_demo_token_32_chars'
  const noScopeToken = 'juis_no_scope_external_source_demo_token_32_chars'
  const disabledSourceToken = 'juis_disabled_source_demo_token_32_chars'
  const limitedSourceToken = 'juis_limited_source_demo_token_32_chars'
  const ipUsageToken = 'juis_valid_ip_usage_public_token_32_chars'
  const accountWriteToken = 'juis_valid_account_write_public_token_32_chars'

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
  const accountWriteSource = upsertExternalIntegrationSource({
    name: '公开资源写入来源',
    status: 'active',
    scopes: [externalIntegrationAccountPushScope]
  })
  createExternalIntegrationSourceToken({
    sourceRefId: accountWriteSource.id,
    name: 'account-write-token',
    token: accountWriteToken,
    scopes: [externalIntegrationAccountPushScope]
  })
  seedClientIpUsageWindow(clientIpStats, usageStatsHelpers, getStatsDatabase)

  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', publicApiPrefix: '/__aipublic__' })
  const server = await listen(app)
  const address = server.address()
  assert(address && typeof address !== 'string', '测试 HTTP 服务地址无效')
  const baseUrl = `http://127.0.0.1:${address.port}`

  try {
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
    assert.equal(success.body.data.tokenPrefix, validToken.slice(0, 12))
    assert.equal(Object.prototype.hasOwnProperty.call(success.body.data, 'token'), false, '成功响应不能返回明文 token')
    assert.equal(Object.prototype.hasOwnProperty.call(success.body.data, 'scopes'), false, '成功响应不应返回授权能力明细')

    const testTokenSuccess = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      Authorization: `Bearer ${externalIntegrationTestToken}`
    })
    assert.equal(testTokenSuccess.status, 200)
    assert.equal(testTokenSuccess.body.data.sourceName, '内置测试来源')
    assert.equal(testTokenSuccess.body.data.mock, true)

    const mockRanking = await requestJson(baseUrl, '/__aipublic__/demo/mock-ranking?range=last7d&limit=3', {
      Authorization: `Bearer ${externalIntegrationTestToken}`
    })
    assert.equal(mockRanking.status, 200)
    assert.equal(mockRanking.body.data.mock, true)
    assert.equal(mockRanking.body.data.testToken, true)
    assert.equal(mockRanking.body.data.items.length, 3)
    assert.equal(Object.prototype.hasOwnProperty.call(mockRanking.body.data, 'token'), false, 'mock 响应不能返回明文 token')

    const ipUsageNoScope = await requestJson(baseUrl, '/__aipublic__/ip/usage?range=today&pageSize=5', {
      Authorization: `Bearer ${validToken}`
    })
    assert.equal(ipUsageNoScope.status, 403)
    assert.equal(ipUsageNoScope.body.code, 'external_source_scope_forbidden', 'IP 用量接口必须使用独立 scope')

    const mockIpUsage = await requestJson(baseUrl, '/__aipublic__/ip/usage?range=last7d&limit=2', {
      Authorization: `Bearer ${externalIntegrationTestToken}`
    })
    assert.equal(mockIpUsage.status, 200)
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

    const customIpUsage = await requestJson(baseUrl, '/__aipublic__/ip/usage?startDate=2026-05-24&endDate=2026-05-30', {
      Authorization: `Bearer ${ipUsageToken}`
    })
    assert.equal(customIpUsage.status, 400)
    assert.match(customIpUsage.body.message, /暂不支持自定义日期范围/, '公开 IP 聚合接口不应宣称并接受未预生成的自定义窗口')

    const consumptionRanking = await requestJson(baseUrl, '/__aipublic__/consumption/ranking?range=today&limit=1&metric=requestCount', {
      Authorization: `Bearer ${ipUsageToken}`
    })
    assert.equal(consumptionRanking.status, 200)
    assert.equal(consumptionRanking.body.data.dimension, 'client_ip')
    assert.equal(consumptionRanking.body.data.items[0].name, '203.0.113.10')
    assert.equal(consumptionRanking.body.data.items[0].metricValue, 3)

    const accessInfo = await requestJson(baseUrl, '/__aipublic__/access/info', {
      Authorization: `Bearer ${ipUsageToken}`
    })
    assert.equal(accessInfo.status, 200)
    assert.equal(accessInfo.body.data.dataDimension, 'client_ip')
    assert.deepEqual(accessInfo.body.data.supportedRanges, ['today', 'last7d', 'last31d'], '接入信息只能声明后台已维护的固定窗口')
    assert(accessInfo.body.data.boundary.notProvided.includes('公益站用户维度排行榜快照'), '接入信息应明确公益站业务快照不由 sub2api-lite 提供')

    assert.equal(await requestStatus(baseUrl, '/__aipublic__/juhe-ai/ip-usage?range=today', {
      Authorization: `Bearer ${ipUsageToken}`
    }), 404, '旧 IP 聚合路径不应保留兼容入口')
    assert.equal(await requestStatus(baseUrl, '/__aipublic__/juhe-ai/consumption-ranking?range=today', {
      Authorization: `Bearer ${ipUsageToken}`
    }), 404, '旧消耗排行路径不应保留兼容入口')
    assert.equal(await requestStatus(baseUrl, '/__aipublic__/juhe-ai/access-info', {
      Authorization: `Bearer ${ipUsageToken}`
    }), 404, '旧接入信息路径不应保留兼容入口')
    assert.equal(await requestStatus(baseUrl, '/__aipublic__/juhe-ai/accounts', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      name: '旧路径账号',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-old-path-regression'
    }), 404, '旧账号写入路径不应保留兼容入口')
    assert.equal(await requestStatus(baseUrl, '/__aipublic__/juhe-ai/accounts', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'DELETE', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      name: '旧路径账号'
    }), 404, '旧账号删除路径不应保留兼容入口')

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
      Authorization: `Bearer ${externalIntegrationTestToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      name: '公益站测试账号',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-mock',
      supportedModels: ['gpt-5.5']
    })
    assert.equal(mockAccountAdd.status, 200)
    assert.equal(mockAccountAdd.body.data.source, 'mock')
    assert.equal(Object.prototype.hasOwnProperty.call(mockAccountAdd.body.data.account, 'credentials'), false, '账号新增响应不能返回凭据')

    const mockAccountDelete = await requestJson(baseUrl, '/__aipublic__/account/del', {
      Authorization: `Bearer ${externalIntegrationTestToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      accountId: 'acc_mock_delete',
      name: '公益站测试账号',
      externalId: 'account-registration:mock-delete'
    })
    assert.equal(mockAccountDelete.status, 200)
    assert.equal(mockAccountDelete.body.data.source, 'mock')
    assert.equal(mockAccountDelete.body.data.action, 'mock')
    assert.equal(Object.prototype.hasOwnProperty.call(mockAccountDelete.body.data.account, 'credentials'), false, '账号删除 mock 响应不能返回凭据')

    const illegalTypeAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'illegal_type_user',
      targetGroupName: '非法类型分组',
      providerCode: 'openai',
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
      providerCode: 'openai',
      name: '无效模型新增账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-invalid-model',
      supportedModels: ['definitely-not-a-real-model']
    })
    assert.equal(invalidModelAdd.status, 400)
    assert.match(invalidModelAdd.body.message, /账户支持模型不在供应商模型目录中/, '无效模型应由账号校验拒绝')
    assert.equal(findSystemAccountByUsername('invalid_model_user'), undefined, '账号创建失败后不应残留自动创建的目标用户')
    const invalidModelGroupResidue = getDatabase()
      .prepare("SELECT COUNT(*) AS total FROM groups WHERE name = '无效模型分组'")
      .get() as { total?: number } | undefined
    assert.equal(Number(invalidModelGroupResidue?.total ?? 0), 0, '账号创建失败后不应残留自动创建的目标分组')

    const businessDatabaseForAccountWrite = getDatabase()
    const originalAccountWritePrepare = businessDatabaseForAccountWrite.prepare.bind(businessDatabaseForAccountWrite) as typeof businessDatabaseForAccountWrite.prepare
    const accountNotesLookupSqls: string[] = []
    businessDatabaseForAccountWrite.prepare = ((sql: string) => {
      if (/\baccounts\.notes\s+LIKE\s+\?/i.test(sql)) {
        accountNotesLookupSqls.push(sql)
      }
      return originalAccountWritePrepare(sql)
    }) as typeof businessDatabaseForAccountWrite.prepare
    try {
      const longExternalId = 'account-registration:10012'
      const shortExternalId = 'account-registration:1001'
      const longExternalIdAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
        Authorization: `Bearer ${accountWriteToken}`
      }, 'POST', {
        targetUsername: 'external_id_collision_user',
        targetGroupName: '外部 ID 碰撞分组',
        providerCode: 'openai',
        name: '外部 ID 长前缀账号',
        type: 'api_key',
        baseUrl: 'https://push.example/v1',
        apiKey: 'sk-public-push-regression-long-external-id',
        supportedModels: ['gpt-5.5'],
        externalId: longExternalId
      })
      assert.equal(longExternalIdAdd.status, 201)

      const shortExternalIdAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
        Authorization: `Bearer ${accountWriteToken}`
      }, 'POST', {
        targetUsername: 'external_id_collision_user',
        targetGroupName: '外部 ID 碰撞分组',
        providerCode: 'openai',
        name: '外部 ID 短前缀账号',
        type: 'api_key',
        baseUrl: 'https://push.example/v1',
        apiKey: 'sk-public-push-regression-short-external-id',
        supportedModels: ['gpt-5.5'],
        externalId: shortExternalId
      })
      assert.equal(shortExternalIdAdd.status, 201, '前缀相同但不完全相同的 externalId 不应更新已有账号')
      assert.notEqual(shortExternalIdAdd.body.data.account.id, longExternalIdAdd.body.data.account.id, '不同 externalId 应绑定到不同账号')
    } finally {
      businessDatabaseForAccountWrite.prepare = originalAccountWritePrepare
    }
    assert.equal(accountNotesLookupSqls.length, 0, '账号新增 externalId 幂等匹配不应扫描 accounts.notes 长文本')

    const accountAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'openai',
      name: '公益站测试账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-001',
      supportedModels: ['gpt-5.5'],
      status: 'active',
      externalId: 'account-registration:1001'
    })
    assert.equal(accountAdd.status, 201)
    assert.equal(accountAdd.body.data.source, 'stats')
    assert.equal(accountAdd.body.data.action, 'created')
    assert.equal(accountAdd.body.data.target.username, 'huanmin')
    assert.equal(accountAdd.body.data.target.groupName, '福利')
    assert.equal(accountAdd.body.data.target.created, true)
    assert.equal(accountAdd.body.data.target.groupCreated, true)
    assert.equal(accountAdd.body.data.account.name, '公益站测试账号')
    assert.equal(Object.prototype.hasOwnProperty.call(accountAdd.body.data.account, 'credentials'), false, '正式新增响应不能返回凭据')

    const targetAccount = findSystemAccountByUsername('huanmin')
    assert(targetAccount, '账号新增应自动创建目标用户 huanmin')
    const targetAccess = { systemAccountId: targetAccount.id, role: 'user' as const }
    const welfareGroup = listGroupOptions(targetAccess, { keyword: '福利', providerCode: 'openai' })
      .find((item) => item.name === '福利')
    assert(welfareGroup, '账号新增应自动创建福利分组')
    const addedAccount = listAccounts(targetAccess, { keyword: '公益站测试账号', providerCode: 'openai', groupId: welfareGroup.id })
      .find((item) => item.name === '公益站测试账号')
    assert(addedAccount, '账号新增应把账号绑定到福利分组')
    assert.equal(addedAccount.boundGroupId, welfareGroup.id)
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
    assert.equal(addLogDetail.metadata?.tokenPrefix, accountWriteToken.slice(0, 12), '操作日志详情应记录来源 token 前缀')

    const accountUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'openai',
      name: '公益站测试账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-001',
      supportedModels: ['gpt-5.5', 'gpt-5.4'],
      status: 'disabled',
      externalId: 'account-registration:1001'
    })
    assert.equal(accountUpdate.status, 200)
    assert.equal(accountUpdate.body.data.action, 'updated')
    assert.equal(accountUpdate.body.data.account.id, accountAdd.body.data.account.id)
    assert.equal(accountUpdate.body.data.account.status, 'disabled')

    const accountRename = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'openai',
      name: '公益站测试账号新版',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-001',
      supportedModels: ['gpt-5.5', 'gpt-5.4'],
      status: 'active',
      externalId: 'account-registration:1001'
    })
    assert.equal(accountRename.status, 200)
    assert.equal(accountRename.body.data.action, 'updated')
    assert.equal(accountRename.body.data.account.id, accountAdd.body.data.account.id)
    assert.equal(accountRename.body.data.account.name, '公益站测试账号新版')
    assert.equal(accountRename.body.data.account.status, 'active')
    const renamedAccount = listAccounts(targetAccess, { keyword: '公益站测试账号新版', providerCode: 'openai', groupId: welfareGroup.id })
      .find((item) => item.name === '公益站测试账号新版')
    assert.equal(renamedAccount?.id, addedAccount.id, '同一 externalId 的账号改名应更新原账号，不能因凭据重复创建失败')

    const missingAccountUpdate = await requestJson(baseUrl, '/__aipublic__/account/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'openai',
      name: '不存在的账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-push-regression-missing-update',
      status: 'active',
      externalId: 'account-registration:not-found'
    })
    assert.equal(missingAccountUpdate.status, 404, '账号修改接口找不到账号时不应自动新增')

    const accountDelete = await requestJson(baseUrl, '/__aipublic__/account/del', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'openai',
      accountId: accountAdd.body.data.account.id,
      name: '公益站测试账号新版',
      externalId: 'account-registration:1001'
    })
    assert.equal(accountDelete.status, 200)
    assert.equal(accountDelete.body.data.source, 'stats')
    assert.equal(accountDelete.body.data.action, 'deleted')
    assert.equal(accountDelete.body.data.account.id, accountAdd.body.data.account.id)
    const removedAccount = listAccounts(targetAccess, { keyword: '公益站测试账号新版', providerCode: 'openai', groupId: welfareGroup.id })
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
      providerCode: 'openai',
      accountId: accountAdd.body.data.account.id,
      externalId: 'account-registration:1001'
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
      providerCode: 'openai',
      description: '公开接口控制面回归',
      enabled: true
    })
    assert.equal(publicGroupAdd.status, 201)
    assert.equal(publicGroupAdd.body.data.action, 'created')
    const publicGroupId = publicGroupAdd.body.data.group.id as string

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

    const publicApiKeyAdd = await requestJson(baseUrl, '/__aipublic__/api-key/add', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      name: '公开接口控制 Key',
      groupId: publicGroupId,
      status: 'active'
    })
    assert.equal(publicApiKeyAdd.status, 201)
    assert.equal(publicApiKeyAdd.body.data.action, 'created')
    assert.equal(typeof publicApiKeyAdd.body.data.apiKey.key, 'string', 'API Key 新增响应应只在创建时返回明文密钥')
    const publicApiKeyId = publicApiKeyAdd.body.data.apiKey.id as string

    const publicApiKeyUpdate = await requestJson(baseUrl, '/__aipublic__/api-key/update', {
      Authorization: `Bearer ${accountWriteToken}`
    }, 'POST', {
      targetUsername: 'public_control_user',
      apiKeyId: publicApiKeyId,
      status: 'disabled'
    })
    assert.equal(publicApiKeyUpdate.status, 200)
    assert.equal(publicApiKeyUpdate.body.data.action, 'updated')
    assert.equal(Object.prototype.hasOwnProperty.call(publicApiKeyUpdate.body.data.apiKey, 'key'), false, 'API Key 修改响应不应返回明文密钥')
    assert.equal(publicApiKeyUpdate.body.data.apiKey.status, 'disabled')

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

    const lastUsedRow = getDatabase()
      .prepare(`
        SELECT tokens.last_used_at AS token_last_used_at, sources.last_used_at AS source_last_used_at
        FROM external_integration_source_tokens AS tokens
        JOIN external_integration_sources AS sources ON sources.id = tokens.source_ref_id
        WHERE sources.name = ? AND tokens.token_prefix = ?
      `)
      .get(sourceName, validToken.slice(0, 12)) as { token_last_used_at?: string | null; source_last_used_at?: string | null } | undefined
    assert(lastUsedRow?.token_last_used_at, '成功鉴权后应低频记录 token 最近调用时间')
    assert(lastUsedRow?.source_last_used_at, '成功鉴权后应低频记录来源系统最近调用时间')
  } finally {
    await closeServer(server)
    closeStorageDatabases()
  }

  console.log('外部来源系统鉴权和公开接口回归通过：公开前缀、Bearer token、测试 token mock、权限校验、停用来源、限频、后台登录边界、IP 聚合读取、账号新增/修改/删除、分组和 API Key 新增/修改/删除均符合预期')
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

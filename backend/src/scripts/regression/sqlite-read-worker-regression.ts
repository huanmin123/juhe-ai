import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const tempRoot = resolve(tmpdir(), `juhe-ai-sqlite-read-worker-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context')
runtimeConfig.secret = 'sqlite-read-worker-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.workerRole = 'worker'
runtimeConfig.sqliteReadWorkerPoolSize = 2
runtimeConfig.sqliteReadWorkerQueueMaxItems = 8
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  readWorkerPool,
  providerDefaultTestModels,
  providerRepository,
  accountTestTasks,
  modelCatalogService,
  openAICompatibleFiles,
  openAICompatibleVectorStores,
  clientIpStats,
  dbServiceHandlers
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/provider-default-test-model.repository.js'),
  import('../../storage/provider.repository.js'),
  import('../../storage/account-test-tasks.repository.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../storage/openai-compatible-files.repository.js'),
  import('../../storage/openai-compatible-vector-stores.repository.js'),
  import('../../storage/client-ip-stats.repository.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: 'SQLite read worker 分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'SQLite read worker 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-sqlite-read-worker',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)
  const expiredAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'SQLite read worker 过期账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-sqlite-read-worker-expired',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)
  const routeStrategy = repositories.createRouteStrategy({
    name: 'SQLite read worker 路由策略',
    mode: 'normal',
    groupBindings: [{
      groupId: group.id,
      priority: 1,
      weight: 100,
      status: 'active'
    }]
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'SQLite read worker API Key',
    routeStrategyId: routeStrategy.id,
    status: 'active'
  }, access)
  const compatibleFile = openAICompatibleFiles.createOpenAICompatibleFile({
    id: 'file_sqlite_read_worker',
    systemAccountId: 'sys_admin',
    apiKeyId: apiKey.id,
    purpose: 'assistants',
    filename: 'sqlite-read-worker.txt',
    bytes: 42,
    mediaType: 'text/plain',
    storageKey: 'openai-compatible/sqlite-read-worker.txt',
    sha256: 'sqlite-read-worker-sha256'
  })
  const vectorStore = openAICompatibleVectorStores.createOpenAICompatibleVectorStore({
    id: 'vs_sqlite_read_worker',
    systemAccountId: 'sys_admin',
    apiKeyId: apiKey.id,
    name: 'SQLite read worker vector store',
    metadata: { regression: true }
  })
  const vectorStoreFile = openAICompatibleVectorStores.createOpenAICompatibleVectorStoreFile({
    vectorStoreId: vectorStore.id,
    fileId: compatibleFile.id,
    systemAccountId: 'sys_admin',
    apiKeyId: apiKey.id,
    status: 'completed',
    attributes: { topic: 'sqlite-read-worker' },
    chunks: [{
      contentText: 'SQLite read worker vector search needle',
      contentPreview: 'SQLite read worker vector search needle',
      tokenEstimate: 8,
      keywordIndexText: 'sqlite read worker vector search needle'
    }]
  })
  assert(vectorStoreFile, 'OpenAI-compatible vector store file seed 应创建成功')
  const proxy = repositories.createProxy({
    name: 'SQLite read worker 代理',
    description: 'read worker regression',
    type: 'http',
    host: '127.0.0.1',
    port: 7890,
    enabled: true
  }, access)
  const clientIpIdentity = clientIpStats.normalizeClientIpForStats('203.0.113.88')
  assert(clientIpIdentity, 'IP 策略测试应能生成 IPv4 identity')
  const nowForClientIp = new Date().toISOString()
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO client_ip_registry (
        ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version,
        first_seen_at, last_seen_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      clientIpIdentity.ipHash,
      clientIpIdentity.bucketNo,
      clientIpIdentity.aggregateIpKey,
      clientIpIdentity.clientIp,
      clientIpIdentity.ipVersion,
      nowForClientIp,
      nowForClientIp,
      nowForClientIp,
      nowForClientIp
    )
  const clientIpPolicy = clientIpStats.createClientIpPolicy({
    ipHash: clientIpIdentity.ipHash,
    reason: 'sqlite read worker regression',
    actorSystemAccountId: 'sys_admin'
  })
  repositories.updateAccountTags(account.id, ['SQLite read worker 标签'], access)
  const session = repositories.createSession('sys_admin')
  const runtimeLogId = 'rtlog_sqlite_read_worker'
  repositories.createRuntimeLogsBatch([{
    id: runtimeLogId,
    time: new Date().toISOString(),
    level: 'info',
    traceId: 'trace-sqlite-read-worker',
    event: 'sqlite_read_worker_regression',
    message: 'runtime keyword needle',
    rawJson: JSON.stringify({ event: 'sqlite_read_worker_regression', message: 'runtime keyword needle' })
  }])
  const expiredAt = new Date(Date.now() - 60_000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'temporary_unavailable',
          schedulable = 1,
          account_expires_at = ?,
          cooldown_until = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(expiredAt, expiredAt, expiredAt, expiredAccount.id)
  seedAccountUsage(account.id)

  assert.equal(readWorkerPool.sqliteReadWorkerPoolEnabled(), true, 'DB service + SQLite 应启用 read worker pool')
  const page = await repositories.listAccountsPageAsync(access, { page: 1, pageSize: 20 })
  const listed = page.items.find((item) => item.id === account.id)
  assert(listed, 'read worker 应返回真实账户列表')
  assert.equal(listed.usage.requestCount, 11, 'read worker 应返回真实统计装饰，不能伪造空统计')

  const expiredListed = page.items.find((item) => item.id === expiredAccount.id)
  assert(expiredListed, 'read worker 应返回过期账户的展示态')
  assert.equal(expiredListed.effectiveAvailability.available, false, '过期账户应通过展示态标记不可用')

  const expiredRow = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT status, schedulable, last_error_code
      FROM accounts
      WHERE id = ?
    `)
    .get(expiredAccount.id) as unknown as { status?: string; schedulable?: number; last_error_code?: string | null } | undefined
  assert.equal(expiredRow?.status, 'temporary_unavailable', '账户列表读不能把过期账号写成 disabled')
  assert.equal(expiredRow?.schedulable, 1, '账户列表读不能改写 schedulable')
  assert.notEqual(expiredRow?.last_error_code, 'account_expired', '账户列表读不能写入 account_expired')

  const userAccess = { systemAccountId: 'sys_admin', role: 'user' as const }
  const groupsPage = await repositories.listGroupsPageAsync(userAccess, { page: 1, pageSize: 20 })
  assert(groupsPage.items.some((item) => item.id === group.id), '分组列表 async 读应由 read worker 返回真实数据')
  const groupOptions = await repositories.listGroupOptionsAsync(userAccess, { limit: 20 })
  assert(groupOptions.some((item) => item.id === group.id), '分组选项 async 读应由 read worker 返回真实数据')
  const accountGroupOptions = await repositories.listAccountGroupOptionsAsync(userAccess, { limit: 20 })
  assert(accountGroupOptions.some((item) => item.id === group.id && item.accountIds.includes(account.id)), '账户分组选项应保留真实 accountIds')
  assert.equal((await repositories.findGroupSummaryAsync(group.id, userAccess))?.id, group.id, '分组详情 async 读应由 read worker 返回真实数据')
  const sessionReadHandledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const sessionRead = await repositories.findSessionByTokenAsync(session.token)
  assert.equal(sessionRead?.sessionId, session.sessionId, '管理端鉴权 session 读取应由 read worker 返回真实数据')
  assert(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs >= sessionReadHandledJobsBefore + 1,
    'SQLite DB service 下 findSessionByTokenAsync 必须进入 read worker，避免每个 GET 鉴权读卡住主连接'
  )
  const tagReadHandledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const tags = await repositories.listAccountTagsAsync(userAccess)
  assert(tags.some((item) => item.name === 'SQLite read worker 标签'), '账户标签列表 async 读应由 read worker 返回真实数据')
  assert(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs >= tagReadHandledJobsBefore + 1,
    'SQLite DB service 下 listAccountTagsAsync 必须进入 read worker，避免首屏标签查询卡住主连接'
  )
  assert.equal((await repositories.findAccountSummaryAsync(account.id, userAccess))?.id, account.id, '账户详情 async 读应由 read worker 返回真实数据')
  const accountForTestReadHandledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  assert.equal((await repositories.findAccountForTestAsync(account.id, userAccess))?.id, account.id, '账户高级详情凭据 async 读应由 read worker 返回真实数据')
  assert(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs >= accountForTestReadHandledJobsBefore + 1,
    'SQLite DB service 下 findAccountForTestAsync 必须进入 read worker，避免高级详情同步读凭据'
  )
  assert((await repositories.listAccountOptionsAsync(userAccess, { keyword: 'SQLite read worker', limit: 20 })).some((item) => item.id === account.id), '账户 options async 读应由 read worker 返回真实数据')
  assert.equal(await accountTestTasks.getAccountTestSessionAsync('missing_session', userAccess), undefined, '账户测试会话 async 读应由 read worker 返回真实空结果')
  assert.equal(await accountTestTasks.getAccountTestTaskAsync('missing_task', userAccess), undefined, '账户测试任务 async 读应由 read worker 返回真实空结果')
  assert.deepEqual(await accountTestTasks.listAccountTestTasksAsync([], userAccess), [], '账户测试任务批量 async 读应由 read worker 返回真实空列表')

  const apiKeysPage = await repositories.listApiKeysPageAsync(userAccess, { page: 1, pageSize: 20 })
  assert(apiKeysPage.items.some((item) => item.id === apiKey.id), 'API Key 列表 async 读应由 read worker 返回真实数据')
  assert((await repositories.listApiKeysAsync(userAccess, { pageSize: 20 })).some((item) => item.id === apiKey.id), 'API Key 非分页 async 读应由 read worker 返回真实数据')
  assert.equal((await repositories.findApiKeySummaryAsync(apiKey.id, userAccess))?.id, apiKey.id, 'API Key 详情 async 读应由 read worker 返回真实数据')
  assert.equal((await repositories.findApiKeySecretAsync(apiKey.id, userAccess))?.id, apiKey.id, 'API Key 密钥读取应由 read worker 返回真实数据')

  const routeStrategiesPage = await repositories.listRouteStrategiesPageAsync(userAccess, { page: 1, pageSize: 20 })
  assert(routeStrategiesPage.items.some((item) => item.id === routeStrategy.id), '策略路由列表 async 读应由 read worker 返回真实数据')
  const routeStrategyOptions = await repositories.listRouteStrategyOptionsAsync(userAccess, { limit: 20 })
  assert(routeStrategyOptions.some((item) => item.id === routeStrategy.id), '策略路由选项 async 读应由 read worker 返回真实数据')
  assert.equal((await repositories.findRouteStrategySummaryAsync(routeStrategy.id, userAccess))?.id, routeStrategy.id, '策略路由详情 async 读应由 read worker 返回真实数据')

  const proxiesPage = await repositories.listProxiesPageAsync({ page: 1, pageSize: 20 })
  assert(proxiesPage.items.some((item) => item.id === proxy.id), '代理列表 async 读应由 read worker 返回真实数据')
  assert((await repositories.listProxiesAsync()).some((item) => item.id === proxy.id), '代理非分页 async 读应由 read worker 返回真实数据')
  assert((await repositories.listProxyOptionsAsync({ limit: 20 })).some((item) => item.id === proxy.id), '代理选项 async 读应由 read worker 返回真实数据')
  assert.equal((await repositories.findProxyAsync(proxy.id))?.id, proxy.id, '代理详情 async 读应由 read worker 返回真实数据')

  const systemAccountsPage = await repositories.listSystemAccountsPageAsync({ page: 1, pageSize: 20 })
  assert(systemAccountsPage.items.some((item) => item.id === 'sys_admin'), '系统账户列表 async 读应由 read worker 返回真实数据')
  assert((await repositories.listSystemAccountOptionsAsync({ limit: 20 })).some((item) => item.id === 'sys_admin'), '系统账户选项 async 读应由 read worker 返回真实数据')
  assert.equal((await repositories.findSystemAccountByIdAsync('sys_admin'))?.id, 'sys_admin', '系统账户 ID async 读应由 read worker 返回真实数据')
  assert.equal((await repositories.findSystemAccountByUsernameAsync('admin'))?.id, 'sys_admin', '系统账户用户名 async 读应由 read worker 返回真实数据')
  assert((await repositories.listProvidersAsync()).some((item) => item.code === 'gpt'), '供应商列表 async 读应由 read worker 返回真实数据')
  assert((await providerRepository.listOpenAIProtocolProviderCodesAsync()).includes('gpt'), 'OpenAI 协议供应商 async 读应由 read worker 返回真实数据')
  assert((await providerRepository.listOpenAIProtocolProfileIdsAsync()).includes(GPT_OPENAI_V1_PROFILE_ID), 'OpenAI 协议档案 async 读应由 read worker 返回真实数据')
  assert.equal(await providerRepository.isOpenAIProtocolProviderCodeAsync('gpt'), true, '供应商协议判定 async 读应由 read worker 返回真实数据')
  assert.equal(typeof await providerRepository.findProviderDefaultTestModelAsync('gpt', 'sys_admin'), 'string', '供应商默认测试模型 async 读应由 read worker 返回真实数据')
  assert((await providerRepository.findProviderDefaultSupportedModelsAsync('gpt')).length > 0, '供应商默认模型池 async 读应由 read worker 返回真实数据')
  assert.equal((await providerRepository.findProviderProtocolProfileAsync(GPT_OPENAI_V1_PROFILE_ID))?.id, GPT_OPENAI_V1_PROFILE_ID, '供应商协议档案详情 async 读应由 read worker 返回真实数据')
  assert.equal((await providerRepository.defaultProviderProtocolProfileAsync('gpt'))?.id, GPT_OPENAI_V1_PROFILE_ID, '供应商默认协议档案 async 读应由 read worker 返回真实数据')
  assert.equal((await providerDefaultTestModels.listProviderDefaultTestModelPreferencesAsync('sys_admin', ['gpt'])).size, 0, '供应商默认测试模型偏好 async 读应支持 read worker 空结果')
  assert((await modelCatalogService.listProviderModelCatalogAsync({ providerCode: 'gpt', systemAccountId: 'sys_admin', includeUnpriced: true })).length > 0, '供应商模型目录 async 读应由 read worker 返回真实数据')
  assert.equal(typeof (await repositories.listGlobalSettingsAsync()).appName, 'string', '全局设置 async 读应由 read worker 返回真实数据')
  assert.equal(typeof (await repositories.getSettingsAsync()).defaultTemporaryUnschedulableMinutes, 'number', '系统设置 async 读应由 read worker 返回真实数据')
  assert((await repositories.listRuntimeLogsAsync({ keyword: 'needle', pageSize: 10 })).items.some((item) => item.id === runtimeLogId), '运行日志列表 async 读应由 read worker 返回真实关键词结果')
  assert.equal((await repositories.getRuntimeLogDetailAsync(runtimeLogId))?.id, runtimeLogId, '运行日志详情 async 读应由 read worker 返回真实数据')
  assert((await repositories.getRuntimeLogFacetsAsync()).totalIndexed >= 1, '运行日志 facets async 读应由 read worker 返回真实数据')

  const dbServiceReadHandledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  assert.equal((await dbServiceHandlers.handleDbServiceOperation({
    type: 'validate_gateway_api_key',
    key: apiKey.key
  }))?.id, apiKey.id, 'DB service API Key 校验 cache miss 应经 read worker 返回真实数据')
  const runtime = await dbServiceHandlers.handleDbServiceOperation({
    type: 'read_gateway_runtime',
    key: apiKey.key,
    skipDynamicRouteSelection: true
  })
  assert.equal(runtime.apiKey?.id, apiKey.id, 'DB service gateway runtime 静态读应经 read worker 返回真实 API Key')
  assert(runtime.accounts.some((item) => item.id === account.id), 'DB service gateway runtime 静态读应经 read worker 返回真实候选账号')
  assert.equal((await dbServiceHandlers.handleDbServiceOperation({
    type: 'resolve_group_usage_access',
    groupId: group.id,
    systemAccountId: 'sys_admin'
  }))?.providerCode, 'gpt', 'DB service 分组访问元数据应经 read worker 返回真实数据')
  assert((await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_accounts_for_group',
    groupId: group.id,
    systemAccountId: 'sys_admin'
  })).some((item) => item.id === account.id), 'DB service 分组账号候选应经 read worker 返回真实数据')
  assert((await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_accounts_for_group_result',
    groupId: group.id,
    systemAccountId: 'sys_admin'
  })).accounts.some((item) => item.id === account.id), 'DB service 分组账号候选结果应经 read worker 返回真实数据')
  assert.equal((await dbServiceHandlers.handleDbServiceOperation({
    type: 'find_openai_account_for_group',
    groupId: group.id,
    accountId: account.id,
    systemAccountId: 'sys_admin',
    ignoreAvailability: true
  }))?.id, account.id, 'DB service 分组单账号查询应经 read worker 返回真实数据')
  assert.equal((await dbServiceHandlers.handleDbServiceOperation({
    type: 'find_account_for_test',
    accountId: account.id,
    access
  }))?.id, account.id, 'DB service 账号测试详情应经 read worker 返回真实数据')
  assert.equal(await dbServiceHandlers.handleDbServiceOperation({
    type: 'find_openai_oauth_account_for_refresh',
    accountId: account.id
  }), undefined, 'DB service OAuth refresh 账号查询应经 read worker 返回真实空结果')
  assert((await dbServiceHandlers.handleDbServiceOperation({ type: 'list_active_client_ip_policies' })).some((item) => item.id === clientIpPolicy.id), 'DB service active IP 策略应经 read worker 返回真实列表')
  assert.equal((await clientIpStats.findActiveClientIpPolicyByHashAsync(clientIpIdentity.ipHash))?.id, clientIpPolicy.id, 'IP 策略 hash 精确 async 读应由 read worker 返回真实数据')
  assert((await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_active_response_inspection_policies',
    protocolCode: 'openai',
    providerCode: 'gpt'
  })).some((item) => item.defaultRule), 'DB service active 响应检查策略应经 read worker 返回默认策略')
  assert.deepEqual(await dbServiceHandlers.handleDbServiceOperation({
    type: 'check_authorization_quota',
    groupAuthorizationId: undefined,
    accountAuthorizationId: undefined
  }), { allowed: true }, 'DB service 授权 quota 单项读应经 read worker 返回允许结果')
  assert.deepEqual(await dbServiceHandlers.handleDbServiceOperation({
    type: 'check_authorization_quota_batch',
    groupAuthorizationId: undefined,
    accounts: [{ accountId: account.id }]
  }), [{ allowed: true }], 'DB service 授权 quota 批量读应经 read worker 返回允许结果')
  assert.equal(typeof (await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_public_global_settings'
  })).appName, 'string', '公开全局设置应经 DB service read worker 返回真实数据')
  const providerModelCatalog = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_provider_model_catalog',
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    includeInactive: false,
    includeUnpriced: true
  })
  assert(providerModelCatalog.length > 0, '供应商模型目录应经 DB service read worker 返回真实数据')
  const compatibleFiles = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_compatible_files',
    options: {
      systemAccountId: 'sys_admin',
      apiKeyId: apiKey.id,
      limit: 20
    }
  })
  assert(compatibleFiles.items.some((item) => item.id === compatibleFile.id), 'OpenAI-compatible files 列表应经 DB service read worker 返回真实数据')
  assert.equal((await dbServiceHandlers.handleDbServiceOperation({
    type: 'get_openai_compatible_file',
    fileId: compatibleFile.id,
    systemAccountId: 'sys_admin',
    apiKeyId: apiKey.id
  }))?.id, compatibleFile.id, 'OpenAI-compatible file 详情应经 DB service read worker 返回真实数据')
  const vectorStores = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_compatible_vector_stores',
    options: {
      systemAccountId: 'sys_admin',
      apiKeyId: apiKey.id,
      limit: 20
    }
  })
  assert(vectorStores.items.some((item) => item.id === vectorStore.id && item.fileCounts.completed === 1), 'OpenAI-compatible vector stores 列表应返回真实 fileCounts')
  assert.equal((await dbServiceHandlers.handleDbServiceOperation({
    type: 'get_openai_compatible_vector_store',
    vectorStoreId: vectorStore.id,
    systemAccountId: 'sys_admin',
    apiKeyId: apiKey.id
  }))?.id, vectorStore.id, 'OpenAI-compatible vector store 详情应经 DB service read worker 返回真实数据')
  const vectorStoreFiles = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_compatible_vector_store_files',
    options: {
      vectorStoreId: vectorStore.id,
      systemAccountId: 'sys_admin',
      apiKeyId: apiKey.id,
      limit: 20
    }
  })
  assert(vectorStoreFiles.items.some((item) => item.fileId === compatibleFile.id), 'OpenAI-compatible vector store files 列表应经 DB service read worker 返回真实数据')
  assert.equal((await dbServiceHandlers.handleDbServiceOperation({
    type: 'get_openai_compatible_vector_store_file',
    vectorStoreId: vectorStore.id,
    fileId: compatibleFile.id,
    systemAccountId: 'sys_admin',
    apiKeyId: apiKey.id
  }))?.file?.id, compatibleFile.id, 'OpenAI-compatible vector store file 详情应保留关联 file DTO')
  const searchResults = await dbServiceHandlers.handleDbServiceOperation({
    type: 'search_openai_compatible_vector_store',
    options: {
      vectorStoreId: vectorStore.id,
      systemAccountId: 'sys_admin',
      apiKeyId: apiKey.id,
      query: 'needle',
      maxNumResults: 5
    }
  })
  assert(searchResults.some((item) => item.fileId === compatibleFile.id && item.contentText.includes('needle')), 'OpenAI-compatible vector store search 应经 read worker 返回真实 chunk')
  const chunkResults = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_compatible_vector_store_file_chunks',
    vectorStoreId: vectorStore.id,
    fileId: compatibleFile.id,
    systemAccountId: 'sys_admin',
    apiKeyId: apiKey.id,
    limit: 5
  })
  assert(chunkResults.some((item) => item.fileId === compatibleFile.id && item.contentPreview.includes('needle')), 'OpenAI-compatible vector store chunks 应经 read worker 返回真实 chunk')
  const dbServiceReadHandledJobsDelta = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs - dbServiceReadHandledJobsBefore
  assert(
    dbServiceReadHandledJobsDelta >= 19,
    `DB service 剩余纯读应批量进入 read worker，实际新增 ${dbServiceReadHandledJobsDelta}`
  )

  const poolRuntime = readWorkerPool.getSqliteReadWorkerPoolRuntime()
  assert(poolRuntime.workerCount > 0, 'read worker pool 应创建子进程')
  assert(poolRuntime.handledJobs >= 60, '管理端 SQLite async 读应批量由 read worker 处理')

  console.log('SQLite read worker 回归通过：管理端账号/分组/API Key/策略/代理/系统账户/供应商/设置/模型目录/运行日志/网关运行时/OpenAI-compatible files/vector stores 读进入 query-only 子进程，返回真实数据且不触发隐藏写')
} finally {
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedAccountUsage(accountId: string): void {
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_stats_totals (
        system_account_id, scope_type, scope_id,
        request_count, input_tokens, output_tokens, total_cost_usd,
        last_used_at, updated_at
      )
      VALUES (?, 'account', ?, 11, 2100, 900, 0.021, ?, ?)
    `)
    .run('sys_admin', accountId, now, now)
}

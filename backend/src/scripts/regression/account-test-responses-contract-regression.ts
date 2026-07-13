import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-test-responses-contract-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-test-responses-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const seenResponsesPayloads: Record<string, unknown>[] = []
const providerDefaultHealthCheckModel = 'gpt-5.6-sol'

const [
  { resolveAccountTestModelAsync, testOpenAIAccount, testOpenAIAccountWithDiagnosticRetries },
  { accountManualTestOptionsAsync },
  { flushGatewayAccountSideEffects },
  { flushAllUsageRecordQueue, setDbServiceUsageRecordLocalWriteAllowedForTest },
  databaseModule,
  repositories,
  { createAccountTestTask },
  { upsertProviderDefaultHealthCheckModelPreferenceAsync },
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/accounts/account-test-options.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-test-tasks.repository.js'),
  import('../../storage/provider-default-health-check-model.repository.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

let mockOpenAIServer: http.Server | undefined

try {
  setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('账户测试 Responses 当前契约 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`

  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const access = { systemAccountId: admin.id, role: 'admin' as const }
  const group = repositories.createGroup({
    name: '账户测试 Responses 当前契约分组',
    providerCode: 'gpt'
  }, access)
  assert.throws(
    () => repositories.createAccount({
      providerCode: 'openai',
      name: '测试 OpenAI 兼容账户不接收客户端画像',
      type: 'api_key',
      clientCompatibility: 'codex_responses',
      credentials: { api_key: 'sk-openai-compatible-codex-default-modes', base_url: mockBaseUrl }
    }, access),
    /账户创建参数包含未知字段：clientCompatibility/,
    '账户创建请求不应接收客户端画像字段'
  )
  const oauthAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试 OAuth 固定 Codex 账户',
    type: 'oauth',
    groupId: group.id,
    credentials: {
      base_url: 'https://chatgpt.com/backend-api/codex',
      access_token: 'oauth-access-token',
      refresh_token: 'oauth-refresh-token',
      expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString()
    }
  }, access)
  assert.equal(oauthAccount.clientCompatibility, 'codex_responses', 'OpenAI OAuth 账户创建时应固定 Codex Responses 兼容')
  assert.throws(
    () => repositories.updateAccount(oauthAccount.id, {
      clientCompatibility: 'openai_standard'
    }, access),
    /账户更新参数包含未知字段：clientCompatibility/,
    '账户更新请求不应接收客户端画像字段'
  )
  const updatedOAuthAccount = repositories.findAccountSummary(oauthAccount.id, access)
  assert.equal(updatedOAuthAccount?.clientCompatibility, 'codex_responses', 'OpenAI OAuth 账户更新时不应被切回 OpenAI 标准兼容')
  const oauthTestTask = createAccountTestTask({
    account: updatedOAuthAccount!,
    access,
    diagnostics: 'full',
    model: 'gpt-5.5',
    testEndpointMode: 'responses_sse'
  })
  assert.equal(oauthTestTask.testEndpointMode, 'responses_sse', 'OpenAI OAuth 测试任务入队时应保留本次测试 endpoint mode')

  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试 Responses 当前契约账户',
    type: 'api_key',
    groupId: group.id,
    supportedModels: ['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini'],
    credentials: {
      api_key: 'sk-account-test-responses-contract',
      base_url: mockBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_sse']
    }
  }, access)
  assert.equal(account.clientCompatibility, 'codex_responses', 'GPT API Key 账户创建时默认应使用 Codex Responses 兼容')

  const fullAccountForManualTest = repositories.findAccountForTest(account.id, access)
  assert.equal(fullAccountForManualTest?.credentials.api_key, 'sk-account-test-responses-contract', '人工测试受控读取应取得完整保存凭据')
  const manualTestOptions = await accountManualTestOptionsAsync(fullAccountForManualTest!)
  assert.deepEqual(
    manualTestOptions.testEndpointModes,
    ['chat_sse', 'responses_sse'],
    '人工测试选项应按完整保存账户能力返回稳定顺序，不能依赖列表摘要或模型协议标签'
  )
  assert.equal(manualTestOptions.defaultTestEndpointMode, 'chat_sse', '人工测试默认请求形态应为稳定顺序第一项')
  assert.equal('credentials' in manualTestOptions, false, '人工测试选项不得暴露账户凭据')

  assert.equal(account.healthCheckModel, providerDefaultHealthCheckModel, '新账户应按协议档案系统默认值初始化检查模型')
  assert.equal(await resolveAccountTestModelAsync(account), providerDefaultHealthCheckModel, '系统复测应严格使用已保存的账户检查模型')
  repositories.updateAccountHealthCheckModel(account.id, 'gpt-5.4', access)
  const accountWithDefaultModel = repositories.findAccountSummary(account.id, access)
  assert.equal(accountWithDefaultModel?.healthCheckModel, 'gpt-5.4', '账户检查模型应按账户 ID 写入')
  assert.equal(await resolveAccountTestModelAsync(accountWithDefaultModel!), 'gpt-5.4', '账户检查模型应作为系统复测唯一默认模型')

  const isolatedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试账户默认模型隔离',
    type: 'api_key',
    groupId: group.id,
    supportedModels: ['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4'],
    credentials: { api_key: 'sk-account-test-default-model-isolated', base_url: mockBaseUrl }
  }, access)
  repositories.updateAccountHealthCheckModel(isolatedAccount.id, 'gpt-5.5', access)
  assert.equal(repositories.findAccountSummary(account.id, access)?.healthCheckModel, 'gpt-5.4', '账户 B 切换默认模型不能覆盖账户 A')
  assert.equal(repositories.findAccountSummary(isolatedAccount.id, access)?.healthCheckModel, 'gpt-5.5', '账户 B 应保存自己的检查模型')
  const rejectedHealthModel = repositories.updateAccountHealthCheckModel(account.id, 'gpt-5.6-terra', access)
  assert.equal(rejectedHealthModel?.healthCheckModel, 'gpt-5.4', '普通检查模型更新不能接受支持模型列表外的值')
  assert.equal(rejectedHealthModel?.supportedModels?.includes('gpt-5.6-terra'), false, '普通检查模型更新不能隐式扩张支持模型')
  const configuredHealthModel = repositories.updateAccountHealthCheckModel(account.id, 'gpt-5.6-terra', access, true)
  assert.equal(configuredHealthModel?.healthCheckModel, 'gpt-5.6-terra', '配置入口应保存新的账户检查模型')
  assert(configuredHealthModel?.supportedModels?.includes('gpt-5.6-terra'), '配置入口必须原子维护检查模型属于支持模型的不变量')
  repositories.updateAccountHealthCheckModel(account.id, 'gpt-5.4', access)

  const tested = await testOpenAIAccount(account, { model: 'gpt-5.5', testEndpointMode: 'responses_sse' })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()

  assert.equal(tested.success, true, `API Key 账户 Responses 测试不应发送 max_output_tokens：${tested.message}`)
  assert.equal(seenResponsesPayloads.length, 1, 'mock 上游应收到一次 Responses 测试请求')
  assert.equal(
    Object.prototype.hasOwnProperty.call(seenResponsesPayloads[0], 'max_output_tokens'),
    false,
    'API Key 账户 Responses 测试不应发送 max_output_tokens'
  )
  assert.equal(seenResponsesPayloads[0]?.model, 'gpt-5.5', '测试请求应保留显式模型')
  assert.equal(
    repositories.findAccountSummary(account.id, access)?.healthCheckModel,
    'gpt-5.4',
    '显式人工测试模型不能写回账户检查模型'
  )

  const accountDefaultModelTested = await testOpenAIAccount(accountWithDefaultModel!, { testEndpointMode: 'responses_sse' })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(accountDefaultModelTested.success, true, `账户检查模型测试应成功：${accountDefaultModelTested.message}`)
  assert.equal(accountDefaultModelTested.model, 'gpt-5.4', '未显式指定模型时应使用当前账户检查模型')
  assert.equal(seenResponsesPayloads.at(-1)?.model, 'gpt-5.4', '账户检查模型应写入上游请求')

  const defaultModelAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试 Responses 默认模型账户',
    type: 'api_key',
    groupId: group.id,
    supportedModels: ['gpt-5.6-sol', 'gpt-5.5'],
    credentials: { api_key: 'sk-account-test-default-model', base_url: mockBaseUrl }
  }, access)
  const defaultModelTested = await testOpenAIAccount(defaultModelAccount, { testEndpointMode: 'responses_sse' })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(defaultModelTested.success, true, `默认模型账户测试应成功：${defaultModelTested.message}`)
  assert.equal(defaultModelTested.model, providerDefaultHealthCheckModel, '未显式指定测试模型时，应使用新建时初始化的账户检查模型')
  assert.equal(seenResponsesPayloads.at(-1)?.model, providerDefaultHealthCheckModel, '系统复测上游请求应使用账户检查模型')

  const mappedSourceModel = 'gpt-5.5'
  const mappedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试 Responses 模型映射账户',
    type: 'api_key',
    groupId: group.id,
    supportedModels: [providerDefaultHealthCheckModel, mappedSourceModel],
    healthCheckModel: mappedSourceModel,
    modelMappings: [
      {
        sourceModel: 'gpt-5.4',
        sourceEndpointFamily: 'chat_completions',
        upstreamModel: providerDefaultHealthCheckModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      },
      {
        sourceModel: mappedSourceModel,
        sourceEndpointFamily: 'responses',
        upstreamModel: providerDefaultHealthCheckModel,
        upstreamEndpointFamily: 'responses',
        enabled: true
      }
    ],
    credentials: { api_key: 'sk-account-test-mapped-model', base_url: mockBaseUrl }
  }, access)
  assert.equal(
    await resolveAccountTestModelAsync(mappedAccount, { sourceFamilies: ['responses'] }),
    mappedSourceModel,
    '系统复测应严格使用账户检查模型，映射左侧模型可作为检查模型'
  )
  const mappedModelTested = await testOpenAIAccountWithDiagnosticRetries(mappedAccount, { testEndpointMode: 'responses_sse' })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(mappedModelTested.success, true, `模型映射账户测试应成功：${mappedModelTested.message}`)
  assert.equal(mappedModelTested.model, mappedSourceModel, '账户测试结果 model 应保留用户请求模型')
  assert.equal(mappedModelTested.upstreamModel, providerDefaultHealthCheckModel, '账户测试结果应返回实际上游模型')
  assert.equal(mappedModelTested.modelMappingApplied, true, '账户测试结果应明确标记模型映射已命中')
  assert.equal(mappedModelTested.sourceEndpointFamily, 'responses', '账户测试结果应返回映射来源协议族')
  assert.equal(mappedModelTested.upstreamEndpointFamily, 'responses', '账户测试结果应返回映射上游协议族')
  assert.equal(seenResponsesPayloads.at(-1)?.model, providerDefaultHealthCheckModel, '模型映射账户的真实上游请求应改写为映射右侧模型')

  const bindingUser = repositories.createSystemAccount({
    username: 'account_test_binding_user',
    displayName: '账户测试授权绑定用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const bindingUserAccess = { systemAccountId: bindingUser.id, role: 'user' as const }
  const bindingUserGroup = repositories.createGroup({
    name: '账户测试普通用户默认模型分组',
    providerCode: 'gpt'
  }, bindingUserAccess)
  await upsertProviderDefaultHealthCheckModelPreferenceAsync({
    systemAccountId: admin.id,
    providerCode: 'gpt',
    model: 'gpt-5.4-mini'
  })
  await upsertProviderDefaultHealthCheckModelPreferenceAsync({
    systemAccountId: bindingUser.id,
    providerCode: 'gpt',
    model: 'gpt-5.5'
  })
  assert.equal(
    await resolveAccountTestModelAsync({
      ...isolatedAccount,
      systemAccountId: bindingUser.id,
      ownerSystemAccountId: admin.id,
      bindingSystemAccountId: bindingUser.id,
      healthCheckModel: 'gpt-5.5'
    }),
    'gpt-5.5',
    '授权实例系统复测应严格使用该实例保存的账户检查模型'
  )

  await upsertProviderDefaultHealthCheckModelPreferenceAsync({
    systemAccountId: admin.id,
    providerCode: 'gpt',
    model: 'gpt-5.4-mini'
  })
  await assert.rejects(
    () => resolveAccountTestModelAsync(accountWithDefaultModel!, {
      systemAccountId: admin.id,
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-5.4-mini', 'gpt-5.5']
    }),
    /账户检查模型不在支持模型列表中/,
    '草稿支持模型移除账户检查模型时应直接报配置错误，不能动态回退个人或系统默认'
  )
  await upsertProviderDefaultHealthCheckModelPreferenceAsync({
    systemAccountId: bindingUser.id,
    providerCode: 'gpt',
    model: 'gpt-5.4-mini'
  })
  const userDefaultModelAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试 Responses 个人默认初始化检查模型账户',
    type: 'api_key',
    groupId: bindingUserGroup.id,
    supportedModels: ['gpt-5.6-sol', 'gpt-5.4-mini'],
    credentials: { api_key: 'sk-account-test-user-default-model', base_url: mockBaseUrl }
  }, bindingUserAccess)
  const fullUserDefaultModelAccount = repositories.findAccountForTest(userDefaultModelAccount.id, bindingUserAccess)
  assert(fullUserDefaultModelAccount, '普通用户个人默认初始化账户应能按受控测试路径读取')
  const userDefaultModelTested = await testOpenAIAccount(fullUserDefaultModelAccount, {
    systemAccountId: bindingUser.id,
    testEndpointMode: 'responses_sse'
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(userDefaultModelAccount.healthCheckModel, 'gpt-5.4-mini', '新建账户应使用当前用户个人默认初始化检查模型')
  assert.equal(userDefaultModelTested.success, true, `个人默认初始化的检查模型测试应成功：${userDefaultModelTested.message}`)
  assert.equal(userDefaultModelTested.model, 'gpt-5.4-mini', '系统复测应使用已初始化到账户上的检查模型')
  assert.equal(seenResponsesPayloads.at(-1)?.model, 'gpt-5.4-mini', '个人默认初始化后的账户检查模型应写入上游请求')

  const systemFallbackAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试显式系统默认检查模型账户',
    type: 'api_key',
    groupId: group.id,
    supportedModels: ['gpt-5.6-sol'],
    healthCheckModel: providerDefaultHealthCheckModel,
    credentials: { api_key: 'sk-account-test-system-default-fallback', base_url: mockBaseUrl }
  }, access)
  const systemFallbackTested = await testOpenAIAccount(systemFallbackAccount, {
    systemAccountId: admin.id,
    testEndpointMode: 'responses_sse'
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(systemFallbackTested.success, true, `系统默认回退账户测试应成功：${systemFallbackTested.message}`)
  assert.equal(systemFallbackAccount.healthCheckModel, providerDefaultHealthCheckModel, '显式检查模型应按账户支持模型保存')
  assert.equal(systemFallbackTested.model, providerDefaultHealthCheckModel, '系统复测应使用账户已保存的系统默认初始化值')

  console.log('账户测试 Responses 当前契约回归通过：人工显式模型不持久化，系统复测严格使用账户检查模型，初始化优先级和模型映射符合预期')
} finally {
  setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  await closeServer(mockOpenAIServer)
  await closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  try {
    rmSync(tempRoot, { recursive: true, force: true })
  } catch {
  }
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url?.split('?', 1)[0] !== '/v1/responses') {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }

    let requestBody = ''
    req.setEncoding('utf8')
    req.on('data', (chunk) => {
      requestBody += chunk
    })
    req.on('end', () => {
      const payload = parseJsonObject(requestBody)
      seenResponsesPayloads.push(payload)
      if (Object.prototype.hasOwnProperty.call(payload, 'max_output_tokens')) {
        res.writeHead(400, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { message: 'Unsupported parameter: max_output_tokens' } }))
        return
      }

      const completedEvent = {
        type: 'response.completed',
        response: {
          id: 'resp_contract',
          object: 'response',
          status: 'completed',
          output: [
            {
              type: 'message',
              content: [{ type: 'output_text', text: 'OK' }]
            }
          ],
          usage: {
            input_tokens: 1,
            output_tokens: 1,
            total_tokens: 2
          }
        }
      }
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
    })
  })
}

function parseJsonObject(requestBody: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(requestBody) as unknown
    if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
  }
  return {}
}

async function onceListening(listeningServer: http.Server): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: http.Server): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
}

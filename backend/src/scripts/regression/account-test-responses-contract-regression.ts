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
const providerDefaultTestModel = 'gpt-5.6-sol'

const [
  { resolveAccountTestModelAsync, testOpenAIAccount },
  { flushGatewayAccountSideEffects },
  { flushAllUsageRecordQueue, setDbServiceUsageRecordLocalWriteAllowedForTest },
  databaseModule,
  repositories,
  { createAccountTestTask },
  { upsertProviderDefaultTestModelPreferenceAsync },
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-test-tasks.repository.js'),
  import('../../storage/provider-default-test-model.repository.js'),
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
    credentials: { api_key: 'sk-account-test-responses-contract', base_url: mockBaseUrl }
  }, access)
  assert.equal(account.clientCompatibility, 'codex_responses', 'GPT API Key 账户创建时默认应使用 Codex Responses 兼容')

  assert.equal(await resolveAccountTestModelAsync(account), providerDefaultTestModel, '账户和用户都未配置时应使用账户协议档案系统默认模型')
  repositories.updateAccountDefaultTestModel(account.id, 'gpt-5.4', access)
  const accountWithDefaultModel = repositories.findAccountSummary(account.id, access)
  assert.equal(accountWithDefaultModel?.defaultTestModel, 'gpt-5.4', '账户默认测试模型应按账户 ID 写入')
  assert.equal(await resolveAccountTestModelAsync(accountWithDefaultModel!), 'gpt-5.4', '账户默认测试模型应优先于用户和系统默认')

  const isolatedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试账户默认模型隔离',
    type: 'api_key',
    groupId: group.id,
    supportedModels: ['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4'],
    credentials: { api_key: 'sk-account-test-default-model-isolated', base_url: mockBaseUrl }
  }, access)
  repositories.updateAccountDefaultTestModel(isolatedAccount.id, 'gpt-5.5', access)
  assert.equal(repositories.findAccountSummary(account.id, access)?.defaultTestModel, 'gpt-5.4', '账户 B 切换默认模型不能覆盖账户 A')
  assert.equal(repositories.findAccountSummary(isolatedAccount.id, access)?.defaultTestModel, 'gpt-5.5', '账户 B 应保存自己的默认测试模型')
  const firstEnsuredModel = repositories.updateAccountDefaultTestModel(account.id, 'gpt-5.6-terra', access, true)
  assert.equal(firstEnsuredModel?.defaultTestModel, 'gpt-5.6-terra', '编辑草稿成功模型应立即成为账户默认测试模型')
  assert(firstEnsuredModel?.supportedModels?.includes('gpt-5.6-terra'), '编辑草稿成功模型应原子追加到账户支持模型')
  const secondEnsuredModel = repositories.updateAccountDefaultTestModel(account.id, 'gpt-5.6-luna', access, true)
  assert(secondEnsuredModel?.supportedModels?.includes('gpt-5.6-terra'), '连续测试第二个新模型不能删除第一个成功模型')
  assert(secondEnsuredModel?.supportedModels?.includes('gpt-5.6-luna'), '连续测试第二个新模型应继续追加支持模型')
  assert(secondEnsuredModel?.supportedModels?.includes('gpt-5.4'), '追加草稿成功模型不能删除账户原有支持模型')
  assert.equal(secondEnsuredModel?.defaultTestModel, 'gpt-5.6-luna', '连续测试后应以最后成功模型作为账户默认')

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

  const accountDefaultModelTested = await testOpenAIAccount(accountWithDefaultModel!, { testEndpointMode: 'responses_sse' })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(accountDefaultModelTested.success, true, `账户默认模型测试应成功：${accountDefaultModelTested.message}`)
  assert.equal(accountDefaultModelTested.model, 'gpt-5.4', '未显式指定模型时应优先使用当前账户自己的默认测试模型')
  assert.equal(seenResponsesPayloads.at(-1)?.model, 'gpt-5.4', '账户默认测试模型应写入上游请求')

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
  assert.equal(defaultModelTested.model, providerDefaultTestModel, '未显式指定测试模型时，应使用供应商默认测试模型而不是最近真实请求模型')
  assert.equal(seenResponsesPayloads.at(-1)?.model, providerDefaultTestModel, '未显式指定测试模型时，上游请求应使用供应商默认测试模型')

  const bindingUser = repositories.createSystemAccount({
    username: 'account_test_binding_user',
    displayName: '账户测试授权绑定用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  await upsertProviderDefaultTestModelPreferenceAsync({
    systemAccountId: admin.id,
    providerCode: 'gpt',
    model: 'gpt-5.4-mini'
  })
  await upsertProviderDefaultTestModelPreferenceAsync({
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
      defaultTestModel: undefined
    }),
    'gpt-5.5',
    '授权账户没有账户偏好时应使用 binding 用户个人默认，不能读取来源 owner 的个人默认'
  )

  await upsertProviderDefaultTestModelPreferenceAsync({
    systemAccountId: admin.id,
    providerCode: 'gpt',
    model: 'gpt-5.4-mini'
  })
  assert.equal(
    await resolveAccountTestModelAsync(accountWithDefaultModel!, {
      systemAccountId: admin.id,
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-5.4-mini', 'gpt-5.5']
    }),
    'gpt-5.4-mini',
    '编辑草稿移除账户默认模型后，应按草稿支持模型回退当前用户默认，不能继续使用数据库中的旧账户默认'
  )
  const userDefaultModelAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试 Responses 用户默认测试模型账户',
    type: 'api_key',
    groupId: group.id,
    supportedModels: ['gpt-5.6-sol', 'gpt-5.4-mini'],
    credentials: { api_key: 'sk-account-test-user-default-model', base_url: mockBaseUrl }
  }, access)
  const userDefaultModelTested = await testOpenAIAccount(userDefaultModelAccount, {
    systemAccountId: admin.id,
    testEndpointMode: 'responses_sse'
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(userDefaultModelTested.success, true, `用户默认测试模型账户测试应成功：${userDefaultModelTested.message}`)
  assert.equal(userDefaultModelTested.model, 'gpt-5.4-mini', '未显式指定测试模型时，应优先使用当前用户默认测试模型偏好')
  assert.equal(seenResponsesPayloads.at(-1)?.model, 'gpt-5.4-mini', '用户默认测试模型偏好应写入上游测试请求')

  const systemFallbackAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '测试用户默认不受账户支持时回退系统默认',
    type: 'api_key',
    groupId: group.id,
    supportedModels: ['gpt-5.6-sol'],
    credentials: { api_key: 'sk-account-test-system-default-fallback', base_url: mockBaseUrl }
  }, access)
  const systemFallbackTested = await testOpenAIAccount(systemFallbackAccount, {
    systemAccountId: admin.id,
    testEndpointMode: 'responses_sse'
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(systemFallbackTested.success, true, `系统默认回退账户测试应成功：${systemFallbackTested.message}`)
  assert.equal(systemFallbackTested.model, providerDefaultTestModel, '用户默认不受账户支持时应回退账户协议档案系统默认')

  console.log('账户测试 Responses 当前契约回归通过：显式 testEndpointMode 生效，API Key 测试不发送 max_output_tokens')
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

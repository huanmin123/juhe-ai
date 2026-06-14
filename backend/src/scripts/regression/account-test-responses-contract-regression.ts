import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
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

const [
  { preferredSystemAccountTestModel, testOpenAIAccount },
  { flushGatewayAccountSideEffects },
  { flushAllUsageRecordQueue, setDbServiceUsageRecordLocalWriteAllowedForTest },
  databaseModule,
  repositories,
  { createAccountTestTask }
] = await Promise.all([
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-test-tasks.repository.js')
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
  const oauthAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '测试 OAuth 固定 Codex 账户',
    type: 'oauth',
    groupId: group.id,
    clientCompatibility: 'openai_standard',
    credentials: {
      base_url: 'https://chatgpt.com/backend-api/codex',
      access_token: 'oauth-access-token',
      refresh_token: 'oauth-refresh-token',
      expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString()
    }
  }, access)
  assert.equal(oauthAccount.clientCompatibility, 'codex_responses', 'OpenAI OAuth 账户创建时应固定 Codex Responses 兼容')
  const updatedOAuthAccount = repositories.updateAccount(oauthAccount.id, {
    clientCompatibility: 'openai_standard'
  }, access)
  assert.equal(updatedOAuthAccount?.clientCompatibility, 'codex_responses', 'OpenAI OAuth 账户更新时不应被切回 OpenAI 标准兼容')
  const oauthTestTask = createAccountTestTask({
    account: updatedOAuthAccount!,
    access,
    diagnostics: 'full',
    model: 'gpt-5.5',
    clientCompatibility: 'openai_standard'
  })
  assert.equal(oauthTestTask.clientCompatibility, 'codex_responses', 'OpenAI OAuth 测试任务入队时应固定 Codex Responses 兼容')

  const account = repositories.createAccount({
    providerCode: 'gpt',
    name: '测试 Responses 当前契约账户',
    type: 'api_key',
    groupId: group.id,
    credentials: { api_key: 'sk-account-test-responses-contract', base_url: mockBaseUrl }
  }, access)
  assert.equal(account.clientCompatibility, 'codex_responses', 'GPT API Key 账户创建时默认应使用 Codex Responses 兼容')

  assert.equal(preferredSystemAccountTestModel(account), 'gpt-5.5', '无手动成功测试模型时，系统复测应使用供应商默认测试模型')
  repositories.recordAccountSuccessfulTestModel(account.id, 'gpt-5.4', access)
  const accountWithSuccessfulModel = repositories.findAccountSummary(account.id, access)
  assert.equal(accountWithSuccessfulModel?.lastSuccessfulTestModel, 'gpt-5.4', '手动测试成功模型应写入账户')
  assert.equal(preferredSystemAccountTestModel(accountWithSuccessfulModel!), 'gpt-5.4', '系统复测应优先使用手动测试通过模型')

  const tested = await testOpenAIAccount(account, { model: 'gpt-5.5' })
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

  const defaultModelAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '测试 Responses 默认模型账户',
    type: 'api_key',
    groupId: group.id,
    credentials: { api_key: 'sk-account-test-default-model', base_url: mockBaseUrl }
  }, access)
  const defaultModelTested = await testOpenAIAccount(defaultModelAccount, {
    requestShape: {
      endpoint: '/v1/responses',
      model: 'gpt-5.4',
      stream: true,
      createdAt: new Date().toISOString()
    }
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(defaultModelTested.success, true, `默认模型账户测试应成功：${defaultModelTested.message}`)
  assert.equal(defaultModelTested.model, 'gpt-5.5', '未显式指定测试模型时，应使用供应商默认测试模型而不是最近真实请求模型')
  assert.equal(seenResponsesPayloads.at(-1)?.model, 'gpt-5.5', '未显式指定测试模型时，上游请求应使用供应商默认测试模型')

  console.log('账户测试 Responses 当前契约回归通过：API Key 测试不发送 max_output_tokens')
} finally {
  setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  await closeServer(mockOpenAIServer)
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

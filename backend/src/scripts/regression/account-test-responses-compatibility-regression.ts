import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-test-responses-compatibility-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-test-responses-compatibility-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const seenResponsesPayloads: Record<string, unknown>[] = []

const [
  { testOpenAIAccount },
  { flushGatewayAccountSideEffects },
  { flushAllUsageRecordQueue },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/gateway-account-side-effects.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

let mockOpenAIServer: http.Server | undefined

try {
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('账户测试 Responses 兼容性 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`

  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '测试 Responses 兼容性账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-test-responses-compatibility', base_url: mockBaseUrl }
  }, { systemAccountId: admin.id, role: 'admin' })

  const tested = await testOpenAIAccount(account, { model: 'gpt-5.5' })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()

  assert.equal(tested.success, true, `API Key 账户 Responses 测试应兼容不支持 max_output_tokens 的上游：${tested.message}`)
  assert.equal(seenResponsesPayloads.length, 1, 'mock 上游应收到一次 Responses 测试请求')
  assert.equal(
    Object.prototype.hasOwnProperty.call(seenResponsesPayloads[0], 'max_output_tokens'),
    false,
    'API Key 账户 Responses 测试不应发送 max_output_tokens'
  )
  assert.equal(seenResponsesPayloads[0]?.model, 'gpt-5.5', '测试请求应保留显式模型')

  console.log('账户测试 Responses 兼容性回归通过：API Key 测试不发送 max_output_tokens')
} finally {
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
          id: 'resp_compatibility',
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

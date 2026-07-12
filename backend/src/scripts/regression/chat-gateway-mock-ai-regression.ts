import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const projectRoot = resolve(backendRoot, '..')
const tempRoot = resolve(tmpdir(), `juhe-ai-chat-mock-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const upstreamAuthorizations: string[] = []
let upstream: http.Server | undefined
let backend: ChildProcess | undefined

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.chatDatabasePath = join(tempRoot, 'chat.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.secret = 'chat-mock-secret'
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')
const repositories = await import('../../storage/repositories.js')

try {
  upstream = createMockUpstream()
  upstream.listen(0, '127.0.0.1')
  await onceListening(upstream)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstream)}/v1`
  const access = { systemAccountId: 'sys_admin', role: 'user' as const }
  const group = repositories.createGroup({ name: 'AI 问答 Mock 分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'AI 问答 Mock 账户',
    type: 'api_key',
    credentials: { api_key: 'sk-chat-upstream', base_url: upstreamBaseUrl },
    groupId: group.id,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    status: 'active',
    schedulable: true
  }, access)
  assert(repositories.recordAccountHealthCheckSuccess(account.id, { intervalHours: 24, jitterMinutes: 0, failureThreshold: 3, statusCode: 200 }))
  const gatewayKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'AI 问答 Mock Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(gatewayKey.key)
  const session = repositories.createSession('sys_admin', 1)
  const cookie = `juhe_ai_session=${session.token}`
  databaseModule.closeStorageDatabases()

  const port = await freePort()
  const baseUrl = `http://127.0.0.1:${port}`
  backend = startBackend(port)
  await waitForReady(baseUrl, cookie, backend)

  const keys = await apiJson<{ data: Array<{ id: string }> }>(baseUrl, '/__aisys__/api/my-chat/api-keys', cookie)
  assert(keys.data.some((item) => item.id === gatewayKey.id), 'AI 问答应列出当前用户自己的可用 API Key')
  const created = await apiJson<{ data: { id: string } }>(baseUrl, '/__aisys__/api/my-chat/conversations', cookie, { apiKeyId: gatewayKey.id })
  const conversationId = created.data.id
  const models = await apiJson<{ data: string[] }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/models`, cookie)
  assert(models.data.includes('gpt-5.5'), 'AI 问答模型列表应来自绑定 Key 的真实网关 /v1/models')

  const streamResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ clientMessageId: 'mock-client-1', content: '请返回 Mock Markdown', model: 'gpt-5.5' })
  })
  const streamText = await streamResponse.text()
  assert.equal(streamResponse.status, 200, streamText)
  assert.match(streamText, /event: message\.started/)
  assert.match(streamText, /event: message\.delta/)
  assert.match(streamText, /"delta":"Mock "/)
  assert.match(streamText, /Markdown/)
  assert.match(streamText, /event: message\.completed/)
  assert.deepEqual(upstreamAuthorizations, ['Bearer sk-chat-upstream'], 'AI 问答必须经过网关并使用 AI 账户凭据访问上游')

  const stored = await apiJson<{ data: Array<{ role: string; status: string; contentText: string }> }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages`, cookie)
  assert.deepEqual(stored.data.map((item) => [item.role, item.status]), [['user', 'completed'], ['assistant', 'completed']])
  assert.match(stored.data[1].contentText, /\|项目\|结果\|/)
  assert.match(stored.data[1].contentText, /\$E=mc\^2\$/)
  assert.match(stored.data[1].contentText, /```mermaid/)
  console.log('AI 问答 Mock AI 全链路回归通过：登录态、Key 绑定、模型列表、现有网关、流式事件和消息终态均正确')
  if (process.env.JUHE_AI_CHAT_UI_KEEP_ALIVE === '1') {
    console.log(`CHAT_UI_URL=${baseUrl}/__aisys__/my-chat`)
    console.log('CHAT_UI_LOGIN=admin/admin')
    console.log(`CHAT_UI_COOKIE=${cookie}`)
    await new Promise<void>((resolveStop) => {
      process.once('SIGINT', resolveStop)
      process.once('SIGTERM', resolveStop)
    })
  }
} finally {
  await stopProcess(backend)
  await closeServer(upstream)
  databaseModule.closeStorageDatabases()
  await removeTempRoot(tempRoot)
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url !== '/v1/chat/completions') { res.writeHead(404).end(); return }
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      upstreamAuthorizations.push(String(req.headers.authorization ?? ''))
      const body = JSON.parse(Buffer.concat(chunks).toString('utf8')) as { stream?: boolean }
      assert.equal(body.stream, true)
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache' })
      res.write(`data: ${JSON.stringify({ id: 'chatcmpl-mock', object: 'chat.completion.chunk', choices: [{ index: 0, delta: { role: 'assistant', content: 'Mock ' }, finish_reason: null }] })}\n\n`)
      setTimeout(() => {
        const richMarkdown = '**Markdown**\n\n|项目|结果|\n|---|---|\n|流式|通过|\n\n公式：$E=mc^2$\n\n```mermaid\ngraph LR\nA-->B\n```'
        res.write(`data: ${JSON.stringify({ id: 'chatcmpl-mock', object: 'chat.completion.chunk', choices: [{ index: 0, delta: { content: richMarkdown }, finish_reason: null }] })}\n\n`)
        res.write('data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}\n\n')
        res.end('data: [DONE]\n\n')
      }, 20)
    })
  })
}

function startBackend(port: number): ChildProcess {
  return spawn('pnpm', ['--filter', 'juhe-ai-backend', 'exec', 'tsx', 'src/server.ts'], {
    cwd: projectRoot,
    env: { ...process.env, NODE_ENV: '', JUHE_AI_HOST: '127.0.0.1', JUHE_AI_PORT: String(port), JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1', JUHE_AI_DB_SERVICE_HTTP_PORT: '0', JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath, JUHE_AI_CHAT_DATABASE_PATH: runtimeConfig.chatDatabasePath, JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath, JUHE_AI_USAGE_CATALOG_DATABASE_PATH: runtimeConfig.usageCatalogDatabasePath, JUHE_AI_STATS_DATABASE_PATH: runtimeConfig.statsDatabasePath, JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot, JUHE_AI_CODEX_CONTEXT_ROOT: runtimeConfig.codexContextRoot, JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: runtimeConfig.codexContextStateShardRoot, JUHE_AI_SECRET: runtimeConfig.secret, JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS: 'true', JUHE_AI_LOG_LEVEL: 'warn', JUHE_AI_LOG_CONSOLE_ENABLED: 'false', JUHE_AI_LOG_FILE_ENABLED: 'false' },
    shell: process.platform === 'win32', stdio: ['ignore', 'pipe', 'pipe']
  })
}

async function apiJson<T>(baseUrl: string, path: string, cookie: string, body?: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { method: body === undefined ? 'GET' : 'POST', headers: { cookie, ...(body === undefined ? {} : { 'content-type': 'application/json' }) }, body: body === undefined ? undefined : JSON.stringify(body) })
  const text = await response.text()
  assert.equal(response.ok, true, `${path} HTTP ${response.status}: ${text}`)
  return JSON.parse(text) as T
}
async function waitForReady(baseUrl: string, cookie: string, child: ChildProcess): Promise<void> { const start = Date.now(); while (Date.now() - start < 30_000) { if (child.exitCode !== null) throw new Error(`临时后端退出：${child.exitCode}`); try { const response = await fetch(`${baseUrl}/__aisys__/api/auth/me`, { headers: { cookie } }); if (response.ok) return } catch {} await sleep(200) } throw new Error('临时后端等待超时') }
async function freePort(): Promise<number> { return new Promise((resolvePort, reject) => { const server = net.createServer(); server.once('error', reject); server.listen(0, '127.0.0.1', () => { const address = server.address(); if (!address || typeof address === 'string') { reject(new Error('无法分配端口')); return } const port = address.port; server.close((error) => error ? reject(error) : resolvePort(port)) }) }) }
function onceListening(server: http.Server): Promise<void> { return new Promise((resolveListen, reject) => { server.once('listening', resolveListen); server.once('error', reject) }) }
function serverPort(server: http.Server): number { const address = server.address(); if (!address || typeof address === 'string') throw new Error('Mock 端口不可用'); return address.port }
function sleep(ms: number): Promise<void> { return new Promise((resolveSleep) => setTimeout(resolveSleep, ms)) }
async function closeServer(server?: http.Server): Promise<void> { if (!server?.listening) return; await new Promise<void>((resolveClose) => server.close(() => resolveClose())) }
async function stopProcess(child?: ChildProcess): Promise<void> {
  if (!child || child.exitCode !== null) return
  if (process.platform === 'win32' && child.pid) {
    await new Promise<void>((resolveKill) => {
      const killer = spawn('taskkill', ['/pid', String(child.pid), '/t', '/f'], { stdio: 'ignore' })
      killer.once('error', () => resolveKill())
      killer.once('exit', () => resolveKill())
    })
  } else child.kill('SIGTERM')
  await Promise.race([new Promise<void>((resolveExit) => child.once('exit', () => resolveExit())), sleep(5000)])
  await sleep(500)
}
async function removeTempRoot(path: string): Promise<void> {
  for (let attempt = 0; attempt < 10; attempt += 1) {
    try { rmSync(path, { recursive: true, force: true }); return } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'EBUSY' || attempt === 9) throw error
      await sleep(300)
    }
  }
}

import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { buildChatSystemInstructions } from '../../modules/chat/chat-system-instructions.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const projectRoot = resolve(backendRoot, '..')
const tempRoot = resolve(tmpdir(), `juhe-ai-chat-mock-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const upstreamAuthorizations: string[] = []
const upstreamBodies: Array<Record<string, unknown>> = []
let upstream: http.Server | undefined
let backend: ChildProcess | undefined
const realCredentialFile = process.env.JUHE_AI_CHAT_REAL_CREDENTIAL_FILE?.trim()
const realCredential = realCredentialFile ? readRealCredential(realCredentialFile) : undefined
const testModel = process.env.JUHE_AI_CHAT_REAL_MODEL?.trim() || realCredential?.models.find((item) => item === 'gpt-5.5') || realCredential?.models[0] || 'gpt-5.5'
const chatOnlyTestModel = 'gpt-5.4-mini'

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
  if (!realCredential) {
    upstream = createMockUpstream()
    upstream.listen(0, '127.0.0.1')
    await onceListening(upstream)
  }
  const upstreamBaseUrl = realCredential?.baseUrl ?? `http://127.0.0.1:${serverPort(upstream!)}/v1`
  const access = { systemAccountId: 'sys_admin', role: 'user' as const }
  const group = repositories.createGroup({ name: 'AI 问答 Mock 分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'AI 问答 Mock 账户',
    type: 'api_key',
    credentials: { api_key: realCredential?.apiKey ?? 'sk-chat-upstream', base_url: upstreamBaseUrl },
    groupId: group.id,
    supportedModels: realCredential?.models ?? ['gpt-5.5'],
    healthCheckModel: testModel,
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
  if (Number(process.env.JUHE_AI_CHAT_UI_BULK_MESSAGES ?? 0) > 0) {
    await seedBulkChatMessages(gatewayKey.id, Math.min(5000, Number(process.env.JUHE_AI_CHAT_UI_BULK_MESSAGES)))
  }
  databaseModule.closeStorageDatabases()

  const port = await freePort()
  const baseUrl = `http://127.0.0.1:${port}`
  backend = startBackend(port)
  await waitForReady(baseUrl, cookie, backend)

  const keys = await apiJson<{ data: Array<{ id: string }> }>(baseUrl, '/__aisys__/api/my-chat/api-keys', cookie)
  assert(keys.data.some((item) => item.id === gatewayKey.id), 'AI 问答应列出当前用户自己的可用 API Key')
  const invalidCreate = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations`, { method: 'POST', headers: { cookie, 'content-type': 'application/json' }, body: '{}' })
  assert.equal(invalidCreate.status, 400, 'AI 问答参数校验失败必须返回 400 而不是全局 500')
  const created = await apiJson<{ data: { id: string } }>(baseUrl, '/__aisys__/api/my-chat/conversations', cookie, { apiKeyId: gatewayKey.id })
  const conversationId = created.data.id
  const models = await apiJson<{ data: Array<{ id: string }> }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/models`, cookie)
  assert(models.data.some((item) => item.id === testModel), 'AI 问答模型列表应来自绑定 Key 的真实网关 /v1/models')

  const streamResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ clientMessageId: 'mock-client-1', content: realCredential ? '请用一句中文回答：7天聊天记录保留策略的核心目的是什么？' : '请返回 Mock Markdown', model: testModel })
  })
  const streamText = await streamResponse.text()
  assert.equal(streamResponse.status, 200, streamText)
  assert.match(streamText, /event: message\.started/)
  assert.match(streamText, /event: message\.delta/)
  if (!realCredential) {
    assert.match(streamText, /event: tool\.started/)
    assert.match(streamText, /event: tool\.completed/)
    assert.match(streamText, /"delta":"Mock "/)
    assert.match(streamText, /Markdown/)
  }
  assert.match(streamText, /event: message\.completed/)
  const duplicateResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
    method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({
      clientMessageId: 'mock-client-1',
      content: '重复请求必须在协议与图片校验前命中',
      contentBlocks: [{ type: 'input_image', dataUrl: 'data:image/png;base64,abc' }],
      model: chatOnlyTestModel
    })
  })
  assert.equal(duplicateResponse.status, 409, '相同 clientMessageId 必须返回冲突且不再次调用模型')
  if (!realCredential) {
    const overBudgetResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({ clientMessageId: 'mock-client-over-budget', content: '很长'.repeat(20_000), model: testModel, contextWindowTokens: 16_000 })
    })
    const overBudgetText = await overBudgetResponse.text()
    const overBudgetPayload = parseOptionalJson(overBudgetText)
    const blockBudgetResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({
        clientMessageId: 'mock-client-block-over-budget',
        content: '短摘要',
        contentBlocks: [{ type: 'input_text', text: '很长'.repeat(20_000) }],
        model: testModel,
        contextWindowTokens: 16_000
      })
    })
    const blockBudgetPayload = parseOptionalJson(await blockBudgetResponse.text())
    const expectedInstructions = buildChatSystemInstructions({ toolsEnabled: true }).text
    const observedInstructions = upstreamBodies[0]?.instructions
    const observedTools = Array.isArray(upstreamBodies[0]?.tools) ? upstreamBodies[0].tools as Array<{ type?: string }> : []
    assert.deepEqual({
      instructionsMatch: observedInstructions === expectedInstructions,
      toolDisciplineCount: typeof observedInstructions === 'string' ? observedInstructions.match(/重复调用名称相同/g)?.length ?? 0 : 0,
      webSearchToolCount: observedTools.filter((tool) => tool.type === 'web_search').length,
      overBudgetStatus: overBudgetResponse.status,
      overBudgetCode: overBudgetPayload?.code,
      blockBudgetStatus: blockBudgetResponse.status,
      blockBudgetCode: blockBudgetPayload?.code,
      upstreamCalls: upstreamAuthorizations.length
    }, {
      instructionsMatch: true,
      toolDisciplineCount: 1,
      webSearchToolCount: 1,
      overBudgetStatus: 422,
      overBudgetCode: 'chat_input_exceeds_context',
      blockBudgetStatus: 422,
      blockBudgetCode: 'chat_input_exceeds_context',
      upstreamCalls: 1
    }, 'Responses 必须唯一注入工具提示；固定输入超预算必须在落轮次和请求上游前返回 422')
    assert.match(String(overBudgetPayload?.message ?? ''), /上下文窗口/)
    assert.match(String(blockBudgetPayload?.message ?? ''), /上下文窗口/)
    assert.deepEqual(upstreamAuthorizations, ['Bearer sk-chat-upstream'], 'AI 问答必须经过网关并使用 AI 账户凭据访问上游')
  }

  const stored = await apiJson<{ data: Array<{ id: string; turnId: string; sequenceNo: number; clientMessageId?: string; role: string; status: string; contentText: string; contentBlocks: Array<{ type: string; id?: string; status?: string; inputType?: string; order?: number }> }> }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages`, cookie)
  assert.deepEqual(stored.data.map((item) => [item.role, item.status]), [['user', 'completed'], ['assistant', 'completed']])
  if (!realCredential) {
    assert.deepEqual(stored.data[1].contentBlocks.map((block) => [block.type, block.id, block.status]), [['tool_call', 'search_1', 'completed']])
    assert.match(stored.data[1].contentText, /\|项目\|结果\|/)
    assert.match(stored.data[1].contentText, /\$E=mc\^2\$/)
    assert.match(stored.data[1].contentText, /```mermaid/)

    const outOfRangeCursor = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/messages?beforeSequenceNo=2147483648`, { headers: { cookie } })
    const outOfRangePayload = parseOptionalJson(await outOfRangeCursor.text())
    assert.deepEqual([outOfRangeCursor.status, outOfRangePayload?.code], [400, 'chat_invalid_request'], '消息游标超过 PostgreSQL int4 必须在 HTTP 边界拒绝')

    const upstreamCallsBeforeConflict = upstreamAuthorizations.length
    const longReplaceTurnId = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({ clientMessageId: 'mock-client-long-replace', replaceTurnId: 'x'.repeat(101), content: '过长替换参数', model: testModel })
    })
    const longReplacePayload = parseOptionalJson(await longReplaceTurnId.text())
    assert.deepEqual([longReplaceTurnId.status, longReplacePayload?.code, upstreamAuthorizations.length], [400, 'chat_invalid_request', upstreamCallsBeforeConflict], 'replaceTurnId 最多 100 字符且校验失败不能调用上游')
    const replaceConflict = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({ clientMessageId: 'mock-client-replace-conflict', replaceTurnId: 'turn_missing', content: '冲突草稿', contentBlocks: [{ type: 'input_text', text: '冲突草稿' }], model: testModel })
    })
    const replaceConflictPayload = parseOptionalJson(await replaceConflict.text())
    assert.deepEqual([replaceConflict.status, replaceConflictPayload?.code, upstreamAuthorizations.length], [409, 'chat_replace_conflict', upstreamCallsBeforeConflict], '替换冲突必须返回稳定机器码且不能调用上游')

    const originalTurnId = stored.data[0]!.turnId
    const originalSequenceNumbers = stored.data.map((item) => item.sequenceNo)
    const upstreamCallsBeforeReplace = upstreamAuthorizations.length
    const replaceResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({
        clientMessageId: 'mock-client-replace-success',
        replaceTurnId: originalTurnId,
        content: '修正后的 Markdown 问题',
        contentBlocks: [{ type: 'input_text', text: '修正后的 Markdown 问题' }],
        model: testModel
      })
    })
    const replaceStream = await replaceResponse.text()
    assert.equal(replaceResponse.status, 200, replaceStream)
    assert.match(replaceStream, /event: message\.started/)
    assert.match(replaceStream, /event: message\.completed/)
    assert.equal(upstreamAuthorizations.length, upstreamCallsBeforeReplace + 1, '成功替换只能调用一次上游')
    const replaced = await apiJson<typeof stored>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages`, cookie)
    assert.equal(replaced.data.length, 2, '替换后旧轮次必须完整消失')
    assert.equal(replaced.data.some((item) => item.turnId === originalTurnId), false)
    assert.deepEqual(replaced.data.map((item) => item.sequenceNo), originalSequenceNumbers, '新轮次必须复用旧轮次的两个序号')
    assert.equal(replaced.data[0]?.contentText, '修正后的 Markdown 问题')
    assert.deepEqual(replaced.data[0]?.contentBlocks, [{ type: 'input_marker', inputType: 'input_text', order: 0 }])

    const imageConversation = await apiJson<{ data: { id: string } }>(baseUrl, '/__aisys__/api/my-chat/conversations', cookie, { apiKeyId: gatewayKey.id })
    const imageDataUrl = 'data:image/png;base64,iVBORw0KGgo='
    const imageResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${imageConversation.data.id}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({
        clientMessageId: 'mock-client-image-markers',
        content: '图片前\n[图片]\n图片后',
        contentBlocks: [
          { type: 'input_text', text: '图片前' },
          { type: 'input_image', dataUrl: imageDataUrl },
          { type: 'input_text', text: '图片后' }
        ],
        model: testModel
      })
    })
    assert.equal(imageResponse.status, 200, await imageResponse.text())
    const imageStored = await apiJson<typeof stored>(baseUrl, `/__aisys__/api/my-chat/conversations/${imageConversation.data.id}/messages`, cookie)
    assert.deepEqual(imageStored.data[0]?.contentBlocks, [
      { type: 'input_marker', inputType: 'input_text', order: 0 },
      { type: 'input_marker', inputType: 'input_image', order: 1 },
      { type: 'input_marker', inputType: 'input_text', order: 2 }
    ], '图片轮次只保存顺序标记')
    assert.equal(JSON.stringify(imageStored.data[0]?.contentBlocks).includes(imageDataUrl), false, '图片 Data URL 不得落库')
  } else {
    assert(stored.data[1].contentText.trim().length > 0, '真实模型必须返回非空中文回答')
  }
  console.log(`AI 问答 ${realCredential ? '真实模型' : 'Mock AI'} 全链路回归通过：登录态、Key 绑定、模型列表、现有网关、流式事件和消息终态均正确`)
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

function readRealCredential(path: string): { baseUrl: string; apiKey: string; models: string[] } {
  const lines = readFileSync(path, 'utf8').split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  const baseUrl = lines.find((line) => /^https?:\/\//i.test(line))
  const apiKey = lines.find((line) => /^sk-/i.test(line))
  const models = [...new Set(lines.flatMap((line) => line.match(/gpt-[\w.-]+/gi) ?? []))]
  if (!baseUrl || !apiKey || models.length === 0) throw new Error('真实模型凭据文件缺少 Base URL、API Key 或模型列表')
  return { baseUrl: baseUrl.replace(/\/$/, ''), apiKey, models }
}

async function seedBulkChatMessages(apiKeyId: string, count: number): Promise<void> {
  const { createSqliteDatabaseClient } = await import('../../storage/database-client.js')
  const { createChatConversation } = await import('../../storage/chat.repository.js')
  const client = createSqliteDatabaseClient(databaseModule.getChatDatabase())
  const conversation = await createChatConversation(client, {
    id: 'chat_bulk_conversation', systemAccountId: 'sys_admin', apiKeyId, apiKeyNameSnapshot: 'AI 问答 Mock Key', now: '2099-01-01T00:00:00.000Z'
  })
  await client.transaction(async (tx) => {
    for (let index = 1; index <= count; index += 1) {
      await tx.execute(`
        INSERT INTO chat_messages (
          id, conversation_id, system_account_id, turn_id, sequence_no, client_message_id,
          role, status, content_text, content_bytes, model, created_at, completed_at, expires_at
        ) VALUES (?, ?, 'sys_admin', ?, ?, ?, ?, 'completed', ?, ?, 'gpt-5.5', '2099-01-01T00:00:00.000Z', '2099-01-01T00:00:00.000Z', '2099-01-08T00:00:00.000Z')
      `, [`bulk_${index}`, conversation.id, `bulk_turn_${Math.ceil(index / 2)}`, index, index % 2 === 1 ? `bulk_client_${index}` : null, index % 2 === 1 ? 'user' : 'assistant', `合成消息 ${index}`, Buffer.byteLength(`合成消息 ${index}`, 'utf8')])
    }
    await tx.execute(`UPDATE chat_conversations SET next_sequence_no = ?, title = '5000 条虚拟列表测试' WHERE id = ?`, [count + 1, conversation.id])
  })
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url !== '/v1/responses') { res.writeHead(404).end(); return }
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      upstreamAuthorizations.push(String(req.headers.authorization ?? ''))
      const body = JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown> & { stream?: boolean; tools?: Array<{ type?: string }> }
      upstreamBodies.push(body)
      assert.equal(body.stream, true)
      assert(body.tools?.some((tool) => tool.type === 'web_search'))
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache' })
      writeMockResponseEvent(res, 'response.output_item.added', { type: 'response.output_item.added', item: { id: 'search_1', type: 'web_search_call', status: 'in_progress' } })
      writeMockResponseEvent(res, 'response.output_item.done', { type: 'response.output_item.done', item: { id: 'search_1', type: 'web_search_call', status: 'completed' } })
      writeMockResponseEvent(res, 'response.output_text.delta', { type: 'response.output_text.delta', delta: 'Mock ' })
      setTimeout(() => {
        const richMarkdown = '**Markdown**\n\n|项目|结果|\n|---|---|\n|流式|通过|\n\n公式：$E=mc^2$\n\n```mermaid\ngraph LR\nA-->B\n```'
        writeMockResponseEvent(res, 'response.output_text.delta', { type: 'response.output_text.delta', delta: richMarkdown })
        writeMockResponseEvent(res, 'response.completed', { type: 'response.completed', response: { id: 'resp_mock', status: 'completed', usage: { input_tokens: 8, output_tokens: 4, total_tokens: 12 } } })
        res.end()
      }, 20)
    })
  })
}

function writeMockResponseEvent(res: http.ServerResponse, event: string, data: unknown): void {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
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
function parseOptionalJson(text: string): { code?: unknown; message?: unknown } | undefined { try { return JSON.parse(text) as { code?: unknown; message?: unknown } } catch { return undefined } }
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

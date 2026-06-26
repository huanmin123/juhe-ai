import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import type { RequestAuthContext } from '../../modules/auth/request-context.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-mcp-servers-route-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'mcp-servers-route-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  { forceSelfAccessScope, requireAdmin },
  { withRequestAuthContext },
  { mcpServersRouter },
  { requestContextMiddleware },
  repositories,
  mcpServerRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../modules/auth/request-context.js'),
  import('../../modules/openai-compatible-mcp/mcp-servers.routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/repositories.js'),
  import('../../storage/openai-compatible-mcp-server.repository.js')
])

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface McpServerSummary {
  id: string
  systemAccountId: string
  label: string
  serverUrl: string
  enabled: boolean
  allowedTools: string[]
  defaultApprovalPolicy: 'always' | 'never'
  allowRequestAuthorization: boolean
  authorization?: unknown
  authorizationRef?: string
}

interface McpServerListResult {
  items: McpServerSummary[]
  total: number
  page: number
  pageSize: number
}

interface McpToolSummary {
  toolName: string
  description?: string
  inputSchema: Record<string, unknown>
  annotations?: unknown
}

interface McpDiagnosticSummary {
  id: string
  status: 'succeeded' | 'failed'
  toolCount: number
  errorCode?: string
  errorMessage?: string
}

interface McpDiagnosticResult {
  diagnostic: McpDiagnosticSummary
  tools: McpToolSummary[]
}

interface McpToolsResult {
  server: McpServerSummary
  latestDiagnostic: McpDiagnosticSummary | null
  tools: McpToolSummary[]
}

const authContexts: Record<string, RequestAuthContext> = {
  admin: {
    systemAccountId: 'sys_admin',
    username: 'admin',
    displayName: '管理员',
    role: 'admin',
    mustChangePassword: false,
    sessionId: 'sess_admin'
  },
  userA: {
    systemAccountId: 'sys_user_a',
    username: 'user_a',
    displayName: '用户A',
    role: 'user',
    mustChangePassword: false,
    sessionId: 'sess_user_a'
  },
  userB: {
    systemAccountId: 'sys_user_b',
    username: 'user_b',
    displayName: '用户B',
    role: 'user',
    mustChangePassword: false,
    sessionId: 'sess_user_b'
  }
}

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use((req, _res, next) => {
  withRequestAuthContext(authContexts[firstHeaderValue(req.headers['x-test-user']) ?? ''], next)
})
app.use('/__aisys__/api/my-mcp-servers', forceSelfAccessScope, mcpServersRouter)
app.use('/__aisys__/api/mcp-servers', requireAdmin, mcpServersRouter)

let server: Server | undefined
let remoteMcpServer: Server | undefined
const remoteMcpHits: Array<{ method: string; authorization?: string; sessionId?: string }> = []

try {
  const seed = seedSystemAccounts()
  remoteMcpServer = createRemoteMcpMockServer(remoteMcpHits)
  remoteMcpServer.listen(0, '127.0.0.1')
  await onceListening(remoteMcpServer)
  const remoteAddress = remoteMcpServer.address()
  if (!remoteAddress || typeof remoteAddress === 'string') {
    throw new Error('远程 MCP mock server 地址不可用')
  }
  const remoteMcpUrl = `http://127.0.0.1:${remoteAddress.port}/mcp`

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('MCP server 路由回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const created = await postEnvelope<McpServerSummary>(
    baseUrl,
    '/__aisys__/api/my-mcp-servers',
    'userA',
    {
      label: 'db-mcp',
      serverUrl: 'https://mcp.example.test/runtime',
      description: '数据库 MCP server',
      allowedTools: ['echo', 'echo', 'search'],
      defaultApprovalPolicy: 'never',
      timeoutMs: 2000,
      maxRetries: 2,
      retryDelayMs: 10,
      maxBodyBytes: 65536,
      maxOutputBytes: 8192,
      allowRequestAuthorization: true,
      authorizationRef: 'vault:mcp/db-mcp'
    }
  )
  assert.equal(created.data.systemAccountId, seed.userAId, '用户侧创建应固定当前系统账户 scope')
  assert.deepEqual(created.data.allowedTools, ['echo', 'search'], 'allowedTools 应规范化去重')
  assert.equal(created.data.defaultApprovalPolicy, 'never', '默认审批策略应保存')
  assert.equal(created.data.allowRequestAuthorization, true, '允许请求授权开关应保存')
  assert.equal(created.data.authorization, undefined, 'MCP server 响应不应返回明文 authorization')
  assert.equal(created.data.authorizationRef, 'vault:mcp/db-mcp', 'MCP server 只返回授权引用')

  const duplicate = await postJson(baseUrl, '/__aisys__/api/my-mcp-servers', 'userA', {
    label: 'db-mcp',
    serverUrl: 'https://mcp.example.test/other'
  })
  assert.equal(duplicate.status, 409, '同一系统账户下 label 必须唯一')

  await postEnvelope<McpServerSummary>(
    baseUrl,
    `/__aisys__/api/mcp-servers?systemAccountId=${seed.userBId}`,
    'admin',
    {
      label: 'db-mcp',
      serverUrl: 'https://mcp.example.test/user-b',
      enabled: true
    }
  )

  const userAPage = await getEnvelope<McpServerListResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers?systemAccountId=${seed.userBId}&pageSize=10`,
    'userA'
  )
  assert.equal(userAPage.data.total, 1, '用户侧列表必须固定当前系统账户 scope')
  assert.equal(userAPage.data.items.every((item) => item.systemAccountId === seed.userAId), true, '用户侧列表不能被 systemAccountId 查询参数改写')
  assert.equal(userAPage.data.items[0]?.authorization, undefined, '列表不应回显明文 authorization')

  const adminFiltered = await getEnvelope<McpServerListResult>(
    baseUrl,
    `/__aisys__/api/mcp-servers?systemAccountId=${seed.userAId}&keyword=db-mcp&enabled=true`,
    'admin'
  )
  assert.equal(adminFiltered.data.total, 1, '管理侧应支持 systemAccountId、keyword、enabled 筛选')
  assert.equal(adminFiltered.data.items[0]?.id, created.data.id, '管理侧筛选应命中目标 server')

  const userBReadsUserA = await getJson(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${created.data.id}`,
    'userB'
  )
  assert.equal(userBReadsUserA.status, 404, '用户侧不能读取其他系统账户的 MCP server')

  const patched = await postPatchEnvelope<McpServerSummary>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${created.data.id}`,
    'userA',
    {
      enabled: false,
      allowedTools: ['echo'],
      defaultApprovalPolicy: 'always'
    }
  )
  assert.equal(patched.data.enabled, false, 'patch 应能禁用 DB MCP server')
  assert.deepEqual(patched.data.allowedTools, ['echo'], 'patch 应能更新 allowed tools')
  assert.equal(patched.data.defaultApprovalPolicy, 'always', 'patch 应能更新审批策略')
  assert.equal(mcpServerRepository.listRuntimeOpenAICompatibleMcpServers(seed.userAId).length, 0, '禁用的 DB MCP server 不应进入 runtime allowlist')
  assert.equal(
    mcpServerRepository.listOpenAICompatibleMcpServerLabelsForSystemAccount(seed.userAId).has('db-mcp'),
    true,
    '禁用的 DB MCP server label 也应压住同名环境变量 bootstrap'
  )

  const reenabled = await postPatchEnvelope<McpServerSummary>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${created.data.id}`,
    'userA',
    { enabled: true }
  )
  assert.equal(reenabled.data.enabled, true, 'patch 应能重新启用 DB MCP server')
  const runtimeServers = mcpServerRepository.listRuntimeOpenAICompatibleMcpServers(seed.userAId)
  assert.equal(runtimeServers.length, 1, '启用的 DB MCP server 应进入 runtime allowlist')
  assert.equal(runtimeServers[0]?.label, 'db-mcp')
  assert.equal(runtimeServers[0]?.serverUrl, 'https://mcp.example.test/runtime')
  assert.equal(runtimeServers[0]?.authorization, undefined, 'DB MCP runtime config 不应包含明文 authorization')
  assert.equal(runtimeServers[0]?.maxOutputBytes, 8192, 'DB MCP runtime config 应携带 per-server limit')

  const diagnosticServer = await postEnvelope<McpServerSummary>(
    baseUrl,
    '/__aisys__/api/my-mcp-servers',
    'userA',
    {
      label: 'diag-mcp',
      serverUrl: remoteMcpUrl,
      allowedTools: ['echo'],
      timeoutMs: 2000,
      maxRetries: 0,
      maxBodyBytes: 65536,
      allowRequestAuthorization: true
    }
  )
  remoteMcpHits.length = 0
  const diagnosticDetailBefore = await getEnvelope<McpServerSummary>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}`,
    'userA'
  )
  assert.equal(diagnosticDetailBefore.data.id, diagnosticServer.data.id, '普通详情应能读取诊断目标 server')
  const cacheBefore = await getEnvelope<McpToolsResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}/tools`,
    'userA'
  )
  assert.equal(cacheBefore.data.tools.length, 0, '未诊断前工具缓存应为空')
  assert.equal(remoteMcpHits.length, 0, '列表 / 详情 / 工具缓存读取不应触发远程 MCP')

  const diagnosis = await postOkEnvelope<McpDiagnosticResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}/diagnose`,
    'userA',
    { authorization: 'Bearer route-diagnostic-secret' }
  )
  assert.equal(diagnosis.data.diagnostic.status, 'succeeded', '手动诊断成功应写入 succeeded diagnostic')
  assert.equal(diagnosis.data.diagnostic.toolCount, 1, 'server allowlist 应过滤缓存工具数量')
  assert.deepEqual(diagnosis.data.tools.map((tool) => tool.toolName), ['echo'], '诊断成功应缓存允许的 tool schema')
  assert.deepEqual(remoteMcpHits.map((hit) => hit.method), ['initialize', 'notifications/initialized', 'tools/list'], '诊断只应执行 initialize 和 tools/list')
  assert.equal(remoteMcpHits.every((hit) => hit.authorization === 'Bearer route-diagnostic-secret'), true, '请求级 authorization 只应发送给远程 MCP')

  const cacheAfter = await getEnvelope<McpToolsResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}/tools`,
    'userA'
  )
  assert.equal(cacheAfter.data.latestDiagnostic?.status, 'succeeded', '缓存读取应返回最新诊断摘要')
  assert.deepEqual(cacheAfter.data.tools.map((tool) => tool.toolName), ['echo'], '缓存读取应返回本地 schema，不访问远端')

  await postPatchEnvelope<McpServerSummary>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}`,
    'userA',
    { serverUrl: `${remoteMcpUrl}-fail` }
  )
  const failedRefresh = await postOkEnvelope<McpDiagnosticResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}/diagnose`,
    'userA',
    {}
  )
  assert.equal(failedRefresh.data.diagnostic.status, 'failed', '同一 server 后续诊断失败应写入 failed diagnostic')
  const cacheAfterFailedRefresh = await getEnvelope<McpToolsResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}/tools`,
    'userA'
  )
  assert.equal(cacheAfterFailedRefresh.data.latestDiagnostic?.status, 'failed', '失败诊断应成为最新诊断摘要')
  assert.deepEqual(cacheAfterFailedRefresh.data.tools.map((tool) => tool.toolName), ['echo'], '失败诊断不应清空旧工具缓存')

  const userBDiagnoseUserA = await postJson(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}/diagnose`,
    'userB',
    {}
  )
  assert.equal(userBDiagnoseUserA.status, 404, '用户侧不能诊断其他系统账户 MCP server')

  remoteMcpHits.length = 0
  const failedDiagnosticServer = await postEnvelope<McpServerSummary>(
    baseUrl,
    '/__aisys__/api/my-mcp-servers',
    'userA',
    {
      label: 'diag-fail-mcp',
      serverUrl: `${remoteMcpUrl}-fail`,
      timeoutMs: 2000,
      maxRetries: 0,
      maxBodyBytes: 65536
    }
  )
  const failedDiagnosis = await postOkEnvelope<McpDiagnosticResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${failedDiagnosticServer.data.id}/diagnose`,
    'userA',
    {}
  )
  assert.equal(failedDiagnosis.data.diagnostic.status, 'failed', '远程 tools/list 失败应写入 failed diagnostic')
  assert.equal(failedDiagnosis.data.tools.length, 0, '失败诊断不应返回新工具缓存')
  assert.match(failedDiagnosis.data.diagnostic.errorCode ?? '', /openai_anthropic_bridge_mcp_proxy_/, '失败诊断应保留稳定错误码')

  const disabledDiagnosticServer = await postPatchEnvelope<McpServerSummary>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}`,
    'userA',
    { enabled: false }
  )
  assert.equal(disabledDiagnosticServer.data.enabled, false, '诊断目标应能被禁用')
  const disabledDiagnosis = await postJson(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${diagnosticServer.data.id}/diagnose`,
    'userA',
    { authorization: 'Bearer disabled-diagnostic-check' }
  )
  assert.equal(disabledDiagnosis.status, 400, '禁用 MCP server 不应执行诊断')

  const deleted = await postDeleteEnvelope<boolean>(
    baseUrl,
    `/__aisys__/api/my-mcp-servers/${created.data.id}`,
    'userA'
  )
  assert.equal(deleted.data, true, '用户侧应能删除自身 scope 内的 MCP server')
  assert.equal(
    mcpServerRepository.listRuntimeOpenAICompatibleMcpServers(seed.userAId).some((item) => item.label === 'db-mcp'),
    false,
    '删除后 DB MCP server 不应进入 runtime allowlist'
  )

  const nonAdminAdminRoute = await getJson(baseUrl, '/__aisys__/api/mcp-servers', 'userA')
  assert.equal(nonAdminAdminRoute.status, 403, '非管理员不能访问管理侧 MCP servers 路由')

  console.log('mcp-servers-route-regression: passed')
} finally {
  if (server) {
    await closeServer(server)
  }
  if (remoteMcpServer) {
    await closeServer(remoteMcpServer)
  }
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedSystemAccounts(): { userAId: string; userBId: string } {
  const userA = repositories.createSystemAccount({
    username: 'mcp_server_user_a',
    displayName: 'MCP服务器用户A',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'mcp_server_user_b',
    displayName: 'MCP服务器用户B',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  authContexts.userA.systemAccountId = userA.id
  authContexts.userB.systemAccountId = userB.id
  return { userAId: userA.id, userBId: userB.id }
}

async function getEnvelope<T>(baseUrl: string, path: string, authKey: string): Promise<ApiEnvelope<T>> {
  const response = await getJson<ApiEnvelope<T>>(baseUrl, path, authKey)
  assert.equal(response.status, 200, `${path} 应返回 200: ${JSON.stringify(response.body)}`)
  return response.body
}

async function postEnvelope<T>(
  baseUrl: string,
  path: string,
  authKey: string,
  body: Record<string, unknown>
): Promise<ApiEnvelope<T>> {
  const response = await postJson<ApiEnvelope<T>>(baseUrl, path, authKey, body)
  assert.equal(response.status, 201, `${path} 应返回 201: ${JSON.stringify(response.body)}`)
  return response.body
}

async function postOkEnvelope<T>(
  baseUrl: string,
  path: string,
  authKey: string,
  body: Record<string, unknown>
): Promise<ApiEnvelope<T>> {
  const response = await postJson<ApiEnvelope<T>>(baseUrl, path, authKey, body)
  assert.equal(response.status, 200, `${path} 应返回 200: ${JSON.stringify(response.body)}`)
  return response.body
}

async function postPatchEnvelope<T>(
  baseUrl: string,
  path: string,
  authKey: string,
  body: Record<string, unknown>
): Promise<ApiEnvelope<T>> {
  const response = await requestJson<ApiEnvelope<T>>(baseUrl, path, authKey, 'PATCH', body)
  assert.equal(response.status, 200, `${path} 应返回 200: ${JSON.stringify(response.body)}`)
  return response.body
}

async function postDeleteEnvelope<T>(baseUrl: string, path: string, authKey: string): Promise<ApiEnvelope<T>> {
  const response = await requestJson<ApiEnvelope<T>>(baseUrl, path, authKey, 'DELETE')
  assert.equal(response.status, 200, `${path} 应返回 200: ${JSON.stringify(response.body)}`)
  return response.body
}

async function getJson<T = unknown>(baseUrl: string, path: string, authKey: string): Promise<{ status: number; body: T }> {
  return requestJson<T>(baseUrl, path, authKey, 'GET')
}

async function postJson<T = unknown>(
  baseUrl: string,
  path: string,
  authKey: string,
  body: Record<string, unknown>
): Promise<{ status: number; body: T }> {
  return requestJson<T>(baseUrl, path, authKey, 'POST', body)
}

async function requestJson<T = unknown>(
  baseUrl: string,
  path: string,
  authKey: string,
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  body?: Record<string, unknown>
): Promise<{ status: number; body: T }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: {
      'content-type': 'application/json',
      'x-test-user': authKey
    },
    body: body ? JSON.stringify(body) : undefined
  })
  const text = await response.text()
  const parsed = (text ? JSON.parse(text) : {}) as T
  return { status: response.status, body: parsed }
}

function firstHeaderValue(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value
}

async function onceListening(server: Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolveListening) => {
    server.once('listening', resolveListening)
  })
}

async function closeServer(server: Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolveClose, rejectClose) => {
    server.close((error) => {
      if (error) {
        rejectClose(error)
        return
      }
      resolveClose()
    })
  })
}

function createRemoteMcpMockServer(hits: Array<{ method: string; authorization?: string; sessionId?: string }>): Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const body = safeParseJson(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown>
      const method = typeof body.method === 'string' ? body.method : ''
      hits.push({
        method,
        authorization: firstHeaderValue(req.headers.authorization),
        sessionId: firstHeaderValue(req.headers['mcp-session-id'])
      })
      if (req.url === '/mcp-fail') {
        res.writeHead(500, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: 'mock mcp failure' }))
        return
      }
      if (method === 'initialize') {
        res.writeHead(200, {
          'content-type': 'application/json',
          'mcp-session-id': 'mcp_session_route_diagnostic'
        })
        res.end(JSON.stringify({
          jsonrpc: '2.0',
          id: body.id,
          result: {
            protocolVersion: '2025-06-18',
            capabilities: {},
            serverInfo: { name: 'route-mcp', version: '1.0.0' }
          }
        }))
        return
      }
      if (method === 'notifications/initialized') {
        res.writeHead(202, { 'content-type': 'application/json' })
        res.end('{}')
        return
      }
      if (method === 'tools/list') {
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({
          jsonrpc: '2.0',
          id: body.id,
          result: {
            tools: [
              {
                name: 'echo',
                description: 'Echo input',
                input_schema: {
                  type: 'object',
                  properties: { query: { type: 'string' } },
                  required: ['query']
                },
                annotations: { readOnlyHint: true }
              },
              {
                name: 'admin_only',
                description: 'Filtered tool',
                input_schema: { type: 'object', properties: {} }
              }
            ]
          }
        }))
        return
      }
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ jsonrpc: '2.0', id: body.id, result: {} }))
    })
  })
}

function safeParseJson(text: string): unknown {
  try {
    return JSON.parse(text) as unknown
  } catch {
    return {}
  }
}

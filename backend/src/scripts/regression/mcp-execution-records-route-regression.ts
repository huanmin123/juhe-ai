import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import type { RequestAuthContext } from '../../modules/auth/request-context.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-mcp-execution-records-route-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'mcp-execution-records-route-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  { forceSelfAccessScope, requireAdmin },
  { withRequestAuthContext },
  { mcpExecutionRecordsRouter },
  { requestContextMiddleware },
  repositories,
  mcpApprovalRepository,
  mcpExecutionRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../modules/auth/request-context.js'),
  import('../../modules/openai-compatible-mcp/mcp-execution-records.routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/repositories.js'),
  import('../../storage/openai-compatible-mcp-approval.repository.js'),
  import('../../storage/openai-compatible-mcp-execution.repository.js')
])

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface McpExecutionRecordSummary {
  id: string
  systemAccountId: string
  apiKeyId: string
  groupId: string
  traceId?: string
  approvalRequestId?: string
  serverLabel: string
  toolName: string
  status: 'succeeded' | 'failed'
  outputDigest?: string
  outputBytes: number
  outputTruncated: boolean
  errorCode?: string
  errorMessage?: string
  omissionMetadata?: Record<string, unknown>
  outputText?: unknown
  outputBody?: unknown
  outputContent?: unknown
}

interface McpExecutionListResult {
  items: McpExecutionRecordSummary[]
  total: number
  page: number
  pageSize: number
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
    displayName: '用户 A',
    role: 'user',
    mustChangePassword: false,
    sessionId: 'sess_user_a'
  },
  userB: {
    systemAccountId: 'sys_user_b',
    username: 'user_b',
    displayName: '用户 B',
    role: 'user',
    mustChangePassword: false,
    sessionId: 'sess_user_b'
  }
}

const remoteOutputBodyMarker = 'MCP_REMOTE_OUTPUT_BODY_MUST_NOT_LEAK'
const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use((req, _res, next) => {
  withRequestAuthContext(authContexts[firstHeaderValue(req.headers['x-test-user']) ?? ''], next)
})
app.use('/__aisys__/api/my-mcp-execution-records', forceSelfAccessScope, mcpExecutionRecordsRouter)
app.use('/__aisys__/api/mcp-execution-records', requireAdmin, mcpExecutionRecordsRouter)

let server: Server | undefined

try {
  const records = seedExecutionRecords()

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('MCP 执行记录路由回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const adminPage = await getEnvelope<McpExecutionListResult>(
    baseUrl,
    '/__aisys__/api/mcp-execution-records?page=1&pageSize=2',
    'admin'
  )
  assert.equal(adminPage.data.total, 3, '管理侧应能看到全部 MCP execution records')
  assert.equal(adminPage.data.page, 1, '管理侧分页应返回当前页')
  assert.equal(adminPage.data.pageSize, 2, '管理侧分页应返回当前 pageSize')
  assert.equal(adminPage.data.items.length, 2, '管理侧分页应按 pageSize 裁剪')
  assertNoRemoteOutputBody(adminPage)

  const adminFiltered = await getEnvelope<McpExecutionListResult>(
    baseUrl,
    [
      '/__aisys__/api/mcp-execution-records?',
      `systemAccountId=${records.userA.systemAccountId}`,
      `&apiKeyId=${records.userA.apiKeyId}`,
      `&groupId=${records.userA.groupId}`,
      '&traceId=trace-a',
      `&approvalRequestId=${records.userA.approvalRequestId}`,
      '&serverLabel=real-mcp',
      '&toolName=echo',
      '&status=succeeded',
      '&startAt=2000-01-01T00%3A00%3A00.000Z',
      '&endAt=2999-01-01T00%3A00%3A00.000Z'
    ].join(''),
    'admin'
  )
  assert.equal(adminFiltered.data.total, 1, '管理侧应支持 scope、trace、approval、server、tool、status 和时间窗口组合筛选')
  assert.equal(adminFiltered.data.items[0]?.id, records.userASuccess.id, '管理侧组合筛选应命中目标记录')
  assertNoRemoteOutputBody(adminFiltered)

  const adminWrongScopeDetail = await getJson(
    baseUrl,
    `/__aisys__/api/mcp-execution-records/${records.userASuccess.id}?systemAccountId=${records.userB.systemAccountId}`,
    'admin'
  )
  assert.equal(adminWrongScopeDetail.status, 404, '管理侧详情读取应遵守 systemAccountId 筛选')

  const userAPage = await getEnvelope<McpExecutionListResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-execution-records?systemAccountId=${records.userB.systemAccountId}&pageSize=10`,
    'userA'
  )
  assert.equal(userAPage.data.total, 2, '用户侧查询必须固定当前系统账户 scope')
  assert.equal(userAPage.data.items.every((item) => item.systemAccountId === records.userA.systemAccountId), true, '用户侧查询不能被 systemAccountId 查询参数改写')
  assertNoRemoteOutputBody(userAPage)

  const userADetail = await getEnvelope<McpExecutionRecordSummary>(
    baseUrl,
    `/__aisys__/api/my-mcp-execution-records/${records.userASuccess.id}`,
    'userA'
  )
  assert.equal(userADetail.data.id, records.userASuccess.id, '用户侧应能读取自己 scope 内的详情摘要')
  assert.equal(userADetail.data.outputDigest, records.userASuccess.outputDigest, '详情应返回输出 digest 摘要')
  assertNoRemoteOutputBody(userADetail)

  const userBReadsUserA = await getJson(
    baseUrl,
    `/__aisys__/api/my-mcp-execution-records/${records.userASuccess.id}`,
    'userB'
  )
  assert.equal(userBReadsUserA.status, 404, '用户侧不能读取其他系统账户的 MCP execution record')

  const nonAdminAdminRoute = await getJson(baseUrl, '/__aisys__/api/mcp-execution-records', 'userA')
  assert.equal(nonAdminAdminRoute.status, 403, '非管理员不能访问管理侧 MCP execution records 路由')

  const noFutureRecords = await getEnvelope<McpExecutionListResult>(
    baseUrl,
    '/__aisys__/api/mcp-execution-records?startAt=2999-01-01T00%3A00%3A00.000Z',
    'admin'
  )
  assert.equal(noFutureRecords.data.total, 0, '时间窗口应进入 repository 查询条件')

  console.log('mcp-execution-records-route-regression: passed')
} finally {
  if (server) {
    await closeServer(server)
  }
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedExecutionRecords(): {
  userA: { systemAccountId: string; apiKeyId: string; groupId: string; approvalRequestId: string }
  userB: { systemAccountId: string; apiKeyId: string; groupId: string; approvalRequestId: string }
  userASuccess: McpExecutionRecordSummary
} {
  const userA = repositories.createSystemAccount({
    username: 'mcp_exec_user_a',
    displayName: 'MCP执行记录用户A',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'mcp_exec_user_b',
    displayName: 'MCP执行记录用户B',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  authContexts.userA.systemAccountId = userA.id
  authContexts.userB.systemAccountId = userB.id
  const userAAccess = { systemAccountId: userA.id, role: 'user' as const }
  const userBAccess = { systemAccountId: userB.id, role: 'user' as const }
  const userAGroup = repositories.createGroup({
    name: 'MCP 执行记录用户 A 分组',
    providerCode: 'gpt',
    enabled: true
  }, userAAccess)
  const userBGroup = repositories.createGroup({
    name: 'MCP 执行记录用户 B 分组',
    providerCode: 'gpt',
    enabled: true
  }, userBAccess)
  const userAApiKey = repositories.createApiKeyRecord({
    name: 'MCP 执行记录用户 A Key',
    groupBindings: [{ groupId: userAGroup.id, priority: 1, status: 'active' }]
  }, userAAccess)
  const userBApiKey = repositories.createApiKeyRecord({
    name: 'MCP 执行记录用户 B Key',
    groupBindings: [{ groupId: userBGroup.id, priority: 1, status: 'active' }]
  }, userBAccess)
  const userAApproval = mcpApprovalRepository.createOpenAICompatibleMcpApprovalRequest({
    scope: {
      systemAccountId: userA.id,
      apiKeyId: userAApiKey.id,
      groupId: userAGroup.id
    },
    serverLabel: 'real-mcp',
    serverUrl: 'https://mcp.example.test/a',
    toolName: 'echo',
    argumentsDigest: 'a'.repeat(64),
    argumentsPreview: '{"message":"hello"}',
    traceId: 'trace-a',
    ttlSeconds: 300
  })
  const userBApproval = mcpApprovalRepository.createOpenAICompatibleMcpApprovalRequest({
    scope: {
      systemAccountId: userB.id,
      apiKeyId: userBApiKey.id,
      groupId: userBGroup.id
    },
    serverLabel: 'other-mcp',
    serverUrl: 'https://mcp.example.test/b',
    toolName: 'search',
    argumentsDigest: 'd'.repeat(64),
    argumentsPreview: '{"query":"summary only"}',
    traceId: 'trace-b',
    ttlSeconds: 300
  })
  const userASuccess = mcpExecutionRepository.createOpenAICompatibleMcpExecutionRecord({
    scope: {
      systemAccountId: userA.id,
      apiKeyId: userAApiKey.id,
      groupId: userAGroup.id
    },
    traceId: 'trace-a',
    approvalRequestId: userAApproval.id,
    serverLabel: 'real-mcp',
    serverUrl: 'https://mcp.example.test/a',
    toolName: 'echo',
    argumentsDigest: 'a'.repeat(64),
    argumentsPreview: '{"message":"hello"}',
    status: 'succeeded',
    outputDigest: 'b'.repeat(64),
    outputBytes: 128,
    outputTruncated: false,
    omissionMetadata: { outputBody: 'omitted' },
    startedAt: '2026-06-25T00:00:00.000Z',
    finishedAt: '2026-06-25T00:00:00.120Z',
    durationMs: 120
  })
  mcpExecutionRepository.createOpenAICompatibleMcpExecutionRecord({
    scope: {
      systemAccountId: userA.id,
      apiKeyId: userAApiKey.id,
      groupId: userAGroup.id
    },
    traceId: 'trace-a-failed',
    serverLabel: 'real-mcp',
    serverUrl: 'https://mcp.example.test/a',
    toolName: 'fail',
    argumentsDigest: 'c'.repeat(64),
    argumentsPreview: '{"message":"fail"}',
    status: 'failed',
    outputBytes: 0,
    outputTruncated: false,
    errorCode: 'remote_tool_error',
    errorMessage: '远程 MCP 工具调用失败，正文已省略',
    startedAt: '2026-06-25T00:00:01.000Z',
    finishedAt: '2026-06-25T00:00:01.055Z',
    durationMs: 55
  })
  mcpExecutionRepository.createOpenAICompatibleMcpExecutionRecord({
    scope: {
      systemAccountId: userB.id,
      apiKeyId: userBApiKey.id,
      groupId: userBGroup.id
    },
    traceId: 'trace-b',
    approvalRequestId: userBApproval.id,
    serverLabel: 'other-mcp',
    serverUrl: 'https://mcp.example.test/b',
    toolName: 'search',
    argumentsDigest: 'd'.repeat(64),
    argumentsPreview: '{"query":"summary only"}',
    status: 'succeeded',
    outputDigest: 'e'.repeat(64),
    outputBytes: 4096,
    outputTruncated: true,
    omissionMetadata: { truncated: true, omittedBytes: 8192 },
    startedAt: '2026-06-25T00:00:02.000Z',
    finishedAt: '2026-06-25T00:00:02.320Z',
    durationMs: 320
  })
  return {
    userA: {
      systemAccountId: userA.id,
      apiKeyId: userAApiKey.id,
      groupId: userAGroup.id,
      approvalRequestId: userAApproval.id
    },
    userB: {
      systemAccountId: userB.id,
      apiKeyId: userBApiKey.id,
      groupId: userBGroup.id,
      approvalRequestId: userBApproval.id
    },
    userASuccess
  }
}

async function getEnvelope<T>(baseUrl: string, path: string, authKey: string): Promise<ApiEnvelope<T>> {
  const response = await getJson<ApiEnvelope<T>>(baseUrl, path, authKey)
  assert.equal(response.status, 200, `${path} 应返回 200: ${JSON.stringify(response.body)}`)
  return response.body
}

async function getJson<T = unknown>(baseUrl: string, path: string, authKey: string): Promise<{ status: number; body: T }> {
  const response = await fetch(`${baseUrl}${path}`, {
    headers: {
      'x-test-user': authKey
    }
  })
  const text = await response.text()
  const body = (text ? JSON.parse(text) : {}) as T
  return { status: response.status, body }
}

function assertNoRemoteOutputBody(payload: unknown): void {
  const serialized = JSON.stringify(payload)
  assert(!serialized.includes(remoteOutputBodyMarker), '查询接口不应返回远程 MCP 输出正文')
  const parsed = JSON.parse(serialized) as unknown
  assertNoForbiddenOutputFields(parsed)
}

function assertNoForbiddenOutputFields(value: unknown): void {
  if (!value || typeof value !== 'object') return
  if (Array.isArray(value)) {
    for (const item of value) {
      assertNoForbiddenOutputFields(item)
    }
    return
  }
  for (const [key, nestedValue] of Object.entries(value)) {
    assert.notEqual(key, 'outputText', '查询接口不应返回 outputText 正文字段')
    assert.notEqual(key, 'outputBody', '查询接口不应返回 outputBody 正文字段')
    assert.notEqual(key, 'outputContent', '查询接口不应返回 outputContent 正文字段')
    if (key !== 'omissionMetadata') {
      assertNoForbiddenOutputFields(nestedValue)
    }
  }
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

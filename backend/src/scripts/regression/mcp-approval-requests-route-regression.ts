import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import type { RequestAuthContext } from '../../modules/auth/request-context.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-mcp-approval-requests-route-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'mcp-approval-requests-route-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  { forceSelfAccessScope, requireAdmin },
  { withRequestAuthContext },
  { mcpApprovalRequestsRouter },
  { requestContextMiddleware },
  repositories,
  mcpApprovalRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../modules/auth/request-context.js'),
  import('../../modules/openai-compatible-mcp/mcp-approval-requests.routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/repositories.js'),
  import('../../storage/openai-compatible-mcp-approval.repository.js')
])

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface McpApprovalRequestSummary {
  id: string
  systemAccountId: string
  apiKeyId: string
  groupId: string
  traceId?: string
  serverLabel: string
  toolName: string
  argumentsDigest: string
  argumentsPreview: string
  status: 'pending' | 'approved' | 'rejected' | 'expired' | 'consumed'
  rejectReason?: string
}

interface McpApprovalListResult {
  items: McpApprovalRequestSummary[]
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
app.use('/__aisys__/api/my-mcp-approval-requests', forceSelfAccessScope, mcpApprovalRequestsRouter)
app.use('/__aisys__/api/mcp-approval-requests', requireAdmin, mcpApprovalRequestsRouter)

let server: Server | undefined

try {
  const seed = seedApprovalRequests()

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('MCP 审批路由回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const adminPage = await getEnvelope<McpApprovalListResult>(
    baseUrl,
    '/__aisys__/api/mcp-approval-requests?page=1&pageSize=2',
    'admin'
  )
  assert.equal(adminPage.data.total, 4, '管理侧应能看到全部 MCP approval requests')
  assert.equal(adminPage.data.items.length, 2, '管理侧分页应按 pageSize 裁剪')

  const adminFiltered = await getEnvelope<McpApprovalListResult>(
    baseUrl,
    [
      '/__aisys__/api/mcp-approval-requests?',
      `systemAccountId=${seed.userA.systemAccountId}`,
      `&apiKeyId=${seed.userA.apiKeyId}`,
      `&groupId=${seed.userA.groupId}`,
      '&traceId=trace-a-approve',
      '&serverLabel=real-mcp',
      '&toolName=echo',
      '&status=pending',
      '&startAt=2000-01-01T00%3A00%3A00.000Z',
      '&endAt=2999-01-01T00%3A00%3A00.000Z'
    ].join(''),
    'admin'
  )
  assert.equal(adminFiltered.data.total, 1, '管理侧应支持 scope、trace、server、tool、status 和时间窗口组合筛选')
  assert.equal(adminFiltered.data.items[0]?.id, seed.userA.approveId, '管理侧组合筛选应命中目标审批记录')

  const userAPage = await getEnvelope<McpApprovalListResult>(
    baseUrl,
    `/__aisys__/api/my-mcp-approval-requests?systemAccountId=${seed.userB.systemAccountId}&pageSize=10`,
    'userA'
  )
  assert.equal(userAPage.data.total, 3, '用户侧查询必须固定当前系统账户 scope')
  assert.equal(userAPage.data.items.every((item) => item.systemAccountId === seed.userA.systemAccountId), true, '用户侧查询不能被 systemAccountId 查询参数改写')

  const userBReadsUserA = await getJson(
    baseUrl,
    `/__aisys__/api/my-mcp-approval-requests/${seed.userA.approveId}`,
    'userB'
  )
  assert.equal(userBReadsUserA.status, 404, '用户侧不能读取其他系统账户的 MCP approval request')

  const approved = await postEnvelope<McpApprovalRequestSummary>(
    baseUrl,
    `/__aisys__/api/my-mcp-approval-requests/${seed.userA.approveId}/approve`,
    'userA'
  )
  assert.equal(approved.data.status, 'approved', '用户侧应能批准自身 scope 内的 pending approval')
  assert.equal(executionRecordCount(), 0, '人工 approve API 只能改状态，不应执行远程 MCP 或写 execution record')

  const repeatedApprove = await postJson(
    baseUrl,
    `/__aisys__/api/my-mcp-approval-requests/${seed.userA.approveId}/approve`,
    'userA'
  )
  assert.equal(repeatedApprove.status, 409, '已 approved 的 approval 不能重复批准')

  const rejected = await postEnvelope<McpApprovalRequestSummary>(
    baseUrl,
    `/__aisys__/api/my-mcp-approval-requests/${seed.userA.rejectId}/reject`,
    'userA',
    { rejectReason: '人工拒绝测试' }
  )
  assert.equal(rejected.data.status, 'rejected', '用户侧应能拒绝自身 scope 内的 pending approval')
  assert.equal(rejected.data.rejectReason, '人工拒绝测试', '拒绝原因应被保存')
  assert.equal(executionRecordCount(), 0, '人工 reject API 只能改状态，不应执行远程 MCP 或写 execution record')

  const adminWrongScopeApprove = await postJson(
    baseUrl,
    `/__aisys__/api/mcp-approval-requests/${seed.userA.expireId}/approve?systemAccountId=${seed.userB.systemAccountId}`,
    'admin'
  )
  assert.equal(adminWrongScopeApprove.status, 404, '管理侧 approve 也应遵守 systemAccountId 筛选')

  const expiredApprove = await postJson(
    baseUrl,
    `/__aisys__/api/my-mcp-approval-requests/${seed.userA.expireId}/approve`,
    'userA'
  )
  assert.equal(expiredApprove.status, 409, '已过期的 approval 不能通过人工 API 批准')

  const nonAdminAdminRoute = await getJson(baseUrl, '/__aisys__/api/mcp-approval-requests', 'userA')
  assert.equal(nonAdminAdminRoute.status, 403, '非管理员不能访问管理侧 MCP approval requests 路由')

  console.log('mcp-approval-requests-route-regression: passed')
} finally {
  if (server) {
    await closeServer(server)
  }
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedApprovalRequests(): {
  userA: {
    systemAccountId: string
    apiKeyId: string
    groupId: string
    approveId: string
    rejectId: string
    expireId: string
  }
  userB: { systemAccountId: string }
} {
  const userA = repositories.createSystemAccount({
    username: 'mcp_approval_user_a',
    displayName: 'MCP审批用户A',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'mcp_approval_user_b',
    displayName: 'MCP审批用户B',
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
    name: 'MCP 审批用户 A 分组',
    providerCode: 'gpt',
    enabled: true
  }, userAAccess)
  const userBGroup = repositories.createGroup({
    name: 'MCP 审批用户 B 分组',
    providerCode: 'gpt',
    enabled: true
  }, userBAccess)
  const userAApiKey = repositories.createApiKeyRecord({
    name: 'MCP 审批用户 A Key',
    groupBindings: [{ groupId: userAGroup.id, priority: 1, status: 'active' }]
  }, userAAccess)
  const userBApiKey = repositories.createApiKeyRecord({
    name: 'MCP 审批用户 B Key',
    groupBindings: [{ groupId: userBGroup.id, priority: 1, status: 'active' }]
  }, userBAccess)
  const approve = createApproval(userA.id, userAApiKey.id, userAGroup.id, {
    traceId: 'trace-a-approve',
    toolName: 'echo',
    argumentsDigest: 'a'.repeat(64),
    argumentsPreview: '{"message":"approve"}',
    ttlSeconds: 300
  })
  const reject = createApproval(userA.id, userAApiKey.id, userAGroup.id, {
    traceId: 'trace-a-reject',
    toolName: 'search',
    argumentsDigest: 'b'.repeat(64),
    argumentsPreview: '{"query":"reject"}',
    ttlSeconds: 300
  })
  const expired = createApproval(userA.id, userAApiKey.id, userAGroup.id, {
    traceId: 'trace-a-expired',
    toolName: 'expired',
    argumentsDigest: 'c'.repeat(64),
    argumentsPreview: '{"message":"expired"}',
    ttlSeconds: 1
  })
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE openai_compatible_mcp_approval_requests SET expires_at = ? WHERE id = ?')
    .run('2000-01-01T00:00:00.000Z', expired.id)
  createApproval(userB.id, userBApiKey.id, userBGroup.id, {
    traceId: 'trace-b',
    toolName: 'echo',
    argumentsDigest: 'd'.repeat(64),
    argumentsPreview: '{"message":"b"}',
    ttlSeconds: 300
  })
  return {
    userA: {
      systemAccountId: userA.id,
      apiKeyId: userAApiKey.id,
      groupId: userAGroup.id,
      approveId: approve.id,
      rejectId: reject.id,
      expireId: expired.id
    },
    userB: { systemAccountId: userB.id }
  }
}

function createApproval(
  systemAccountId: string,
  apiKeyId: string,
  groupId: string,
  input: {
    traceId: string
    toolName: string
    argumentsDigest: string
    argumentsPreview: string
    ttlSeconds: number
  }
): McpApprovalRequestSummary {
  return mcpApprovalRepository.createOpenAICompatibleMcpApprovalRequest({
    scope: { systemAccountId, apiKeyId, groupId },
    serverLabel: 'real-mcp',
    serverUrl: 'https://mcp.example.test/runtime',
    toolName: input.toolName,
    argumentsDigest: input.argumentsDigest,
    argumentsPreview: input.argumentsPreview,
    traceId: input.traceId,
    ttlSeconds: input.ttlSeconds
  })
}

function executionRecordCount(): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT COUNT(*) AS count FROM openai_compatible_mcp_execution_records')
    .get() as { count?: number } | undefined
  return row?.count ?? 0
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
  body?: Record<string, unknown>
): Promise<ApiEnvelope<T>> {
  const response = await postJson<ApiEnvelope<T>>(baseUrl, path, authKey, body)
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

async function postJson<T = unknown>(
  baseUrl: string,
  path: string,
  authKey: string,
  body?: Record<string, unknown>
): Promise<{ status: number; body: T }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'x-test-user': authKey
    },
    body: JSON.stringify(body ?? {})
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

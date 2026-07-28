import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-session-audit-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-session-audit-e2e-secret'
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.queueDriver = 'memory'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.auditLog.enabled = true
runtimeConfig.auditLog.successHotRetentionHours = 1
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  auditCaptureModule,
  auditQueueModule,
  clientStrategyModule,
  sessionIdentityModule
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/audit/capture.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/client-profiles/strategy.js'),
  import('../../modules/gateway/session-identity/index.js')
])

const systemAccountId = 'system-session-e2e'
const apiKeyId = 'api-key-session-e2e'
const groupId = 'group-session-e2e'
const codexSessionId = 'official-codex-session-e2e'
const codexTraces = ['trace-codex-session-e2e-1', 'trace-codex-session-e2e-2']
const claudeCodeSessionId = 'official-claude-code-session-e2e'
const claudeCodeTraces = ['trace-claude-session-e2e-1', 'trace-claude-session-e2e-2']

try {
  const sessionHeaderOnly = codexRequest(codexSessionId, undefined)
  const genericStrategy = clientStrategyModule.resolveOpenAIGatewayClientStrategy(sessionHeaderOnly, strategyIdentity())
  assert.equal(genericStrategy.clientProfile, 'generic_openai', 'session-id 不能单独把请求升级为 Codex 画像')
  assert.equal(
    sessionIdentityModule.resolveGatewaySessionIdentity(sessionHeaderOnly, sessionScope(genericStrategy.clientProfile)).status,
    'missing',
    'generic OpenAI 请求即使携带 session-id 也不能生成会话身份'
  )

  for (const [index, traceId] of codexTraces.entries()) {
    const request = codexRequest(codexSessionId, `turn-session-e2e-${index + 1}`)
    const strategy = clientStrategyModule.resolveOpenAIGatewayClientStrategy(request, strategyIdentity())
    assert.equal(strategy.clientProfile, 'codex', '合法 Codex turn metadata 应先建立 Codex 客户端画像')
    assert.equal(strategy.clientProfileSource, 'codex_turn_metadata')

    const identity = sessionIdentityModule.resolveGatewaySessionIdentity(request, sessionScope(strategy.clientProfile))
    assert.equal(identity.status, 'resolved')
    assert.equal(identity.sessionId, codexSessionId)
    assert.equal(identity.semanticNamespace, 'openai.codex.session')

    const capture = auditCaptureModule.createAuditCapture({
      req: request,
      traceId,
      startedAtMs: Date.now(),
      captureMode: 'metadata_only'
    })
    capture.bindContext({
      sessionId: identity.sessionId,
      sessionClientType: strategy.clientProfile,
      conversationKey: identity.conversationKey,
      systemAccountId,
      apiKeyId,
      groupId,
      accountId: 'account-session-e2e',
      providerCode: 'openai'
    })
    capture.finalize({ outcome: 'success', success: true, statusCode: 200 })
  }

  for (const traceId of claudeCodeTraces) {
    const request = claudeCodeRequest(claudeCodeSessionId)
    const strategy = clientStrategyModule.resolveOpenAIGatewayClientStrategy(request, {
      ...strategyIdentity(),
      endpoint: '/v1/messages',
      providerCode: 'anthropic',
      providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
      protocolCode: 'anthropic',
      protocolVersion: 'v1'
    })
    assert.equal(strategy.clientProfile, 'claude_code', 'Claude Code 官方多信号请求应建立客户端画像')
    assert.equal(strategy.clientProfileSource, 'claude_code_request_signature')

    const identity = sessionIdentityModule.resolveGatewaySessionIdentity(request, sessionScope(strategy.clientProfile))
    assert.equal(identity.status, 'resolved')
    assert.equal(identity.sessionId, claudeCodeSessionId)
    assert.equal(identity.semanticNamespace, 'anthropic.claude_code.session')

    const capture = auditCaptureModule.createAuditCapture({
      req: request,
      traceId,
      startedAtMs: Date.now(),
      captureMode: 'metadata_only'
    })
    capture.bindContext({
      sessionId: identity.sessionId,
      sessionClientType: strategy.clientProfile,
      conversationKey: identity.conversationKey,
      systemAccountId,
      apiKeyId,
      groupId,
      accountId: 'account-claude-session-e2e',
      providerCode: 'anthropic'
    })
    capture.finalize({ outcome: 'success', success: true, statusCode: 200 })
  }

  assert.equal(auditQueueModule.getAuditLogQueueRuntime().queueLength, 4, '四次官方客户端请求都应进入审计队列')
  auditQueueModule.flushAllAuditLogQueue()
  assert.equal(auditQueueModule.getAuditLogQueueRuntime().queueLength, 0, '审计队列应完整刷入隔离数据库')

  assertSessionAuditResult({
    sessionId: codexSessionId,
    sessionClientType: 'codex',
    traces: codexTraces
  })
  assertSessionAuditResult({
    sessionId: claudeCodeSessionId,
    sessionClientType: 'claude_code',
    traces: claudeCodeTraces
  })

  console.log('官方 Codex / Claude Code Header -> 画像 -> resolver -> AuditCapture -> 队列 -> DB -> 管理查询回归通过')
} finally {
  auditQueueModule.clearAuditLogQueueForTest()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function codexRequest(rawSessionId: string, turnId: string | undefined): Request {
  const headers: Record<string, string> = {
    accept: 'text/event-stream',
    'content-type': 'application/json',
    'session-id': rawSessionId,
    'user-agent': 'codex-cli/session-audit-e2e'
  }
  if (turnId) {
    headers['x-codex-turn-metadata'] = JSON.stringify({ turn_id: turnId })
  }
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    headers,
    body: { model: 'gpt-5.6-sol', stream: true, input: 'session audit regression' },
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    },
    get(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

function claudeCodeRequest(rawSessionId: string): Request {
  const headers: Record<string, string> = {
    accept: 'text/event-stream',
    'content-type': 'application/json',
    'user-agent': 'claude-cli/2.1.201 (external, sdk-cli)',
    'anthropic-beta': 'claude-code-20250219,interleaved-thinking-2025-05-14',
    'x-claude-code-session-id': rawSessionId
  }
  return {
    method: 'POST',
    originalUrl: '/v1/messages?beta=true',
    path: '/v1/messages',
    headers,
    body: { model: 'claude-sonnet-4-5', stream: true, max_tokens: 1024, messages: [] },
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    },
    get(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

function assertSessionAuditResult(input: {
  sessionId: string
  sessionClientType: 'codex' | 'claude_code'
  traces: string[]
}): void {
  const result = repositories.listAuditLogs({
    sessionId: input.sessionId,
    sessionClientType: input.sessionClientType,
    systemAccountId,
    apiKeyId
  })
  assert.equal(result.items.length, 2, '管理端按 session_id 查询应串联同一会话的全部请求')
  assert.deepEqual(result.items.map((item) => item.traceId).sort(), [...input.traces].sort())
  assert(result.items.every((item) => item.sessionId === input.sessionId))
  assert(result.items.every((item) => item.sessionClientType === input.sessionClientType))

  for (const item of result.items) {
    const detail = repositories.getAuditLogDetail(item.id)
    assert(detail, '管理端审计详情应可读取')
    assert.equal(detail.sessionId, input.sessionId)
    assert.equal(detail.sessionClientType, input.sessionClientType)
    assert.match(detail.conversationKey ?? '', /^conv_v1_[A-Za-z0-9_-]+$/)
  }
}

function strategyIdentity() {
  return {
    systemAccountId,
    apiKeyId,
    groupId,
    endpoint: '/v1/responses',
    providerCode: 'openai'
  }
}

function sessionScope(clientProfile: string) {
  return {
    clientProfile,
    systemAccountId,
    apiKeyId,
    hmacSecret: runtimeConfig.secret
  }
}

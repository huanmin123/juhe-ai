import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { createHash, randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { createServer as createHttpServer, type IncomingMessage, type ServerResponse } from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, isAbsolute, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { RuntimeConfig } from '../../config/runtime.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import {
  buildChatLongSessionFixture,
  buildSafeFixtureSummary,
  chatLongSessionArtifactMaxBytes,
  chatLongSessionArtifactQualityFailure,
  assertChatLongSessionScore,
  scoreChatLongSession,
  type ChatLongSessionResponse,
  type ChatLongSessionTurn
} from './chat-long-session-fixture.js'
import { createBoundedSseParser, resolveChatLongSessionMaxEventCount } from './chat-long-session-sse.js'
import { pickChildProcessBaseEnv, redactKnownSecrets, retryBusyCleanup, runIndependentCleanup, sanitizeErrorForDiagnostics } from './chat-long-session-runtime.js'
import { decideChatSubmissionRecovery } from './chat-long-session-recovery.js'
import { ChatLongSessionRunBudget } from './chat-long-session-budget.js'
import { extractSafeChatStreamFailure, type SafeChatStreamFailure } from './chat-long-session-failure.js'
import { runChatLongSessionTurnAttempts, type ChatLongSessionAttemptMetric } from './chat-long-session-attempts.js'
import { ChatLongSessionStreamProgress, type ChatLongSessionStreamIdleReason, type ChatLongSessionStreamProgressSnapshot } from './chat-long-session-stream-progress.js'
import { consumeReaderWithBoundedCancellation } from './chat-long-session-reader.js'
import {
  buildChatLongSessionAttemptIdentity,
  buildChatLongSessionResumePlan,
  chatLongSessionFixtureHash,
  chatLongSessionResumeCanonicalHash,
  type ChatLongSessionResumeMessage
} from './chat-long-session-checkpoint.js'
import { resolveChatLongSessionRunSecret } from './chat-long-session-run-identity.js'
import { withoutChatLongSessionAcceptanceObservability } from './chat-long-session-acceptance-snapshot.js'
import { shouldRemoveChatLongSessionTemp } from './chat-long-session-temp-retention.js'
import {
  buildChatLongSessionSemanticSeedPlan,
  chatLongSessionControlledSeedMaxTokens
} from './chat-long-session-semantic-seed.js'
import {
  busyCleanupTargetPath,
  classifyProcessCommand,
  collectWindowsBusyCleanupDiagnostic
} from './chat-long-session-cleanup-diagnostics.js'
import {
  assertTrackedProcessIdentitiesStopped,
  captureWindowsProcessTree,
  isTcpPortListening,
  killWindowsProcess,
  listWindowsProcessIdentities,
  mergeTrackedProcesses,
  selectTrackedProcessTree,
  startWindowsProcessTreeTracker,
  stopTrackedWindowsProcessTree,
  waitForChildProcessClose,
  type TrackedProcessIdentity,
  type WindowsProcessTreeTracker
} from './chat-long-session-process-tree.js'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const repoRoot = resolve(backendRoot, '..')
const fixture = buildChatLongSessionFixture()
let activeRunBudget: ChatLongSessionRunBudget | undefined
let activeDiagnosticSecrets: readonly string[] = []
const chatTurnHardDeadlineMs = 15 * 60 * 1000
const chatStreamEventIdleMs = 180 * 1000
const chatStreamProgressIdleMs = 300 * 1000

async function main(): Promise<void> {
  if (process.argv.includes('--module-initialization-check')) {
    console.log(JSON.stringify({ ok: true, mode: 'module-initialization-check' }))
  } else if (process.argv.includes('--dry-run')) {
    console.log(JSON.stringify({ ok: true, mode: 'fixture-preview', fixture: buildSafeFixtureSummary(fixture) }))
  } else if (process.argv.includes('--local-preflight')) {
    await runLocalPreflight()
  } else if (process.argv.includes('--offline-stream-recovery')) {
    await runOfflineStreamRecoveryRegression()
  } else {
    await runRealAcceptance(process.argv.includes('--real-probe'))
  }
}

interface RealCredential {
  baseUrl: string
  apiKey: string
  models: string[]
}

interface ChatModelOption {
  id: string
  supportedReasoningEfforts: string[]
  supportedServiceTiers: string[]
  supportedApiProtocols: string[]
  contextWindowTokens?: number
  maxInputTokens?: number
}

interface ChatModelListOption {
  id: string
  name: string
}

interface ContextStatus {
  usedTokens: number
  limitTokens?: number
  ratio: number
  state: string
  usageEstimated: boolean
  compactedThroughSequence: number
  revision: number
}

interface TurnMetric {
  turn: number
  traceId?: string
  firstDeltaMs: number | null
  totalMs: number
  eventCount: number
  status: number
  terminalEvent: string | null
  model: string
  modelSwitch: { result: 'applied' | 'skipped'; reason?: string }
  reasoning: { result: 'applied' | 'skipped'; value?: string; reason?: string }
  service: { result: 'applied' | 'skipped'; value?: string; reason?: string }
  context: ContextStatus
  usage: PersistedUsageMetric
  attempts: ChatLongSessionAttemptMetric[]
  errorCode?: string
}

interface PersistedUsageMetric {
  status: 'available' | 'unavailable'
  cacheStatus: 'available' | 'unavailable'
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  cacheWrite1hTokens?: number
  costUsd?: number
  firstTokenMs?: number
  durationMs?: number
  model?: string
  serviceTier?: string
}

interface LedgerSnapshot {
  messageCount: number
  idempotencyCount: number
  userTurnCount: number
  contextRevision: number
  checkpointCount: number
  assetCount: number
  contentBytes: number
  reservedBytes: number
  compactedThroughSequence: number
  nextSequenceNo: number
  completedAssistantCount: number
  distinctSequenceCount: number
  activeTurn: boolean
  contextState: string
}

interface AcceptanceSnapshot extends LedgerSnapshot {
  usageRecordEntryCount: number
  auditLogCount: number
  upstreamAttemptCount: number
  canonicalHash: string
}

interface ChatLongSessionCheckpoint {
  schemaVersion: 1
  fixtureHash: string
  conversationId: string
  accountId: string
  gatewayKeyId: string
  completedTurns: number
  canonicalHash: string
  turnMetrics: TurnMetric[]
}

async function runLocalPreflight(): Promise<void> {
  const preflightRoot = resolve(tmpdir(), `juhe-ai-chat-preflight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  const child = spawn('pnpm', ['test:chat-gateway-mock-ai'], {
    cwd: backendRoot,
    env: {
      ...pickChildProcessBaseEnv(process.env),
      ...buildHermeticJuheEnv(preflightRoot, 'server'),
      JUHE_AI_CHAT_REAL_CREDENTIAL_FILE: '', JUHE_AI_CHAT_REAL_BASE_URL: '', JUHE_AI_CHAT_REAL_API_KEY: '', JUHE_AI_CHAT_REAL_MODELS: '',
      JUHE_AI_CHAT_UI_KEEP_ALIVE: '0', JUHE_AI_LOG_CONSOLE_ENABLED: 'false', JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    shell: process.platform === 'win32',
    stdio: ['ignore', 'pipe', 'pipe']
  })
  const childProcessTree = process.platform === 'win32' && child.pid ? await captureWindowsProcessTree(child.pid) : []
  let output = ''
  const collect = (chunk: unknown): void => { output = `${output}${String(chunk)}`.slice(-128 * 1024) }
  child.stdout?.on('data', collect)
  child.stderr?.on('data', collect)
  const exitCode = await new Promise<number>((resolveExit, rejectExit) => {
    const timeout = setTimeout(() => resolveExit(-2), 300_000)
    child.once('error', (error) => { clearTimeout(timeout); rejectExit(error) })
    child.once('exit', (code) => { clearTimeout(timeout); resolveExit(code ?? -1) })
  })
  if (exitCode === -2) { await stopProcess(child, { tracked: childProcessTree }); throw new Error('chat_long_session_local_preflight_timeout') }
  assert.equal(exitCode, 0, `local preflight failed: ${redactKnownSecrets(output, [])}`)
  assert.match(output, /全链路回归通过/)
  console.log(JSON.stringify({ ok: true, mode: 'local-preflight', checks: ['temporary-sqlite', 'repository-seed', 'mock-sse', 'cleanup'], reportWritten: false }))
}

async function runRealAcceptance(realProbe: boolean): Promise<void> {
  const configuredResumeRoot = process.env.JUHE_AI_CHAT_REAL_RESUME_ROOT?.trim()
  const tempRoot = configuredResumeRoot
    ? resolve(configuredResumeRoot)
    : resolve(tmpdir(), `juhe-ai-chat-long-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  const resuming = Boolean(configuredResumeRoot)
  if (resuming && !existsSync(tempRoot)) throw new Error('chat_long_session_resume_root_not_found')
  if (!resuming) mkdirSync(tempRoot, { recursive: true })
  const credential = readRequiredCredential()
  const runSecret = resolveChatLongSessionRunSecret(tempRoot, {
    resuming,
    keyMaterial: credential.apiKey,
    allowLegacyPathDerivation: process.env.JUHE_AI_CHAT_REAL_ALLOW_LEGACY_PATH_SECRET === '1'
  })
  applyHermeticProcessEnv(tempRoot, runSecret)
  const { runtimeConfig } = await import('../../config/runtime.js')
  const { logger } = await import('../../shared/logger.js')
  const runAbortController = new AbortController()
  const runDeadline = Date.now() + 4 * 60 * 60 * 1000
  const runBudget = new ChatLongSessionRunBudget(runDeadline, runAbortController.signal)
  activeRunBudget = runBudget
  const keepTemp = process.env.JUHE_AI_CHAT_REAL_KEEP_TEMP === '1'
  const knownSecrets = [credential.apiKey, runtimeConfig.secret, credential.baseUrl]
  activeDiagnosticSecrets = knownSecrets
  let backend: ChildProcess | undefined
  let backendPort: number | undefined
  let backendProcessTree: TrackedProcessIdentity[] = []
  let backendProcessTracker: WindowsProcessTreeTracker | undefined
  let runnerReadWorkerIdentities: TrackedProcessIdentity[] = []
  let childDiagnostics = ''
  let completedReport: unknown
  let completedOutputPath = ''
  let completedSummary: Record<string, unknown> | undefined
  let executionSucceeded = false
  let acceptancePassed = false
  let reportWritten = false
  let cleanupHealthy = true
  let primaryError: Error | undefined
  let interruptedSignal: NodeJS.Signals | undefined
  let signalStopPromise: Promise<void> | undefined
  let signalStopError: unknown
  const handleSignal = (signal: NodeJS.Signals): void => {
    interruptedSignal = signal
    runAbortController.abort(new Error(`chat_long_session_interrupted_${signal}`))
    if (backend && !signalStopPromise) signalStopPromise = stopProcess(backend, { tracked: backendProcessTracker?.snapshot() ?? backendProcessTree, servicePort: backendPort }).catch((error) => { signalStopError = error })
  }
  const handleSigint = (): void => handleSignal('SIGINT')
  const handleSigterm = (): void => handleSignal('SIGTERM')
  process.once('SIGINT', handleSigint)
  process.once('SIGTERM', handleSigterm)
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.chatDatabasePath = join(tempRoot, 'chat.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
  runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
  runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
  runtimeConfig.chatAssetsRoot = join(tempRoot, 'chat-assets')
  runtimeConfig.secret = runSecret
  knownSecrets.push(runtimeConfig.secret)
  runtimeConfig.runtimeMode = 'standalone'
  runtimeConfig.databaseDriver = 'sqlite'
  runtimeConfig.cacheDriver = 'memory'
  runtimeConfig.runtimeStateDriver = 'memory'
  runtimeConfig.queueDriver = 'memory'
  runtimeConfig.postgres.url = undefined
  runtimeConfig.redis.cacheUrl = undefined
  runtimeConfig.redis.stateUrl = undefined
  runtimeConfig.redis.queueUrl = undefined
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.chat.retentionDays = 3
  runtimeConfig.chat.maxConversationsPerUser = 50
  runtimeConfig.chat.maxTurnsPerConversation = 50
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  logger.level = 'silent'

  const databaseModule = await import('../../storage/database.js')
  const repositories = await import('../../storage/repositories.js')
  const sqliteReadWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
  const { createSqliteDatabaseClient } = await import('../../storage/database-client.js')
  const chatRepository = await import('../../storage/chat.repository.js')
  const { compactChatContextOnce } = await import('../../modules/chat/chat-context-compaction.js')
  const access = { systemAccountId: 'sys_admin', role: 'user' as const }
  const checkpointPath = join(tempRoot, 'chat-long-session.checkpoint.json')

  try {
    const resumedCheckpoint = resuming ? readChatLongSessionCheckpoint(checkpointPath) : undefined
    if (resumedCheckpoint && resumedCheckpoint.fixtureHash !== chatLongSessionFixtureHash(fixture)) {
      throw new Error('chat_long_session_resume_fixture_hash_mismatch')
    }
    let accountId: string
    let gatewayKey: NonNullable<ReturnType<typeof repositories.findApiKeySecret>>
    if (resumedCheckpoint) {
      accountId = resumedCheckpoint.accountId
      const existingGatewayKey = repositories.findApiKeySecret(resumedCheckpoint.gatewayKeyId, access)
      if (!existingGatewayKey) throw new Error('chat_long_session_resume_gateway_key_not_found')
      gatewayKey = existingGatewayKey
    } else {
      const group = repositories.createGroup({ name: '长会话真实验收分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
      const account = repositories.createAccount({
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        name: '长会话真实验收账户',
        type: 'api_key',
        credentials: { api_key: credential.apiKey, base_url: credential.baseUrl },
        groupId: group.id,
        supportedModels: credential.models,
        healthCheckModel: credential.models[0],
        status: 'active',
        schedulable: true
      }, access)
      accountId = account.id
      assert(repositories.recordAccountHealthCheckSuccess(account.id, { intervalHours: 24, jitterMinutes: 0, failureThreshold: 3, statusCode: 200 }))
      gatewayKey = createApiKeyRecordWithRouteStrategy(repositories, {
        name: '长会话真实验收 Key',
        groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
        status: 'active'
      }, access)
    }
    assert(gatewayKey.key)
    const session = repositories.createSession(access.systemAccountId, 1)
    knownSecrets.push(gatewayKey.key, session.token)
    const cookie = `juhe_ai_session=${session.token}`
    databaseModule.closeStorageDatabases()

    const port = await freePort()
    const baseUrl = `http://127.0.0.1:${port}`
    backendPort = port
    backend = startBackend(port, tempRoot, runtimeConfig)
    if (process.platform === 'win32' && backend.pid) {
      backendProcessTree = await captureWindowsProcessTree(backend.pid)
      backendProcessTracker = startWindowsProcessTreeTracker({ rootPid: backend.pid, initial: backendProcessTree, pollIntervalMs: 250 })
      await backendProcessTracker.sample()
    }
    if (interruptedSignal) throw new Error(`chat_long_session_interrupted_${interruptedSignal}`)
    backend.stdout?.on('data', (chunk) => { childDiagnostics = boundedDiagnostics(childDiagnostics, String(chunk), knownSecrets, credential.baseUrl) })
    backend.stderr?.on('data', (chunk) => { childDiagnostics = boundedDiagnostics(childDiagnostics, String(chunk), knownSecrets, credential.baseUrl) })
    await waitForReady(baseUrl, cookie, backend)
    if (backendProcessTracker) {
      await backendProcessTracker.sample()
      backendProcessTree = backendProcessTracker.snapshot()
    } else if (process.platform === 'win32' && backend.pid) {
      backendProcessTree = mergeTrackedProcesses(backendProcessTree, await captureWindowsProcessTree(backend.pid))
    }

    const conversationId = resumedCheckpoint?.conversationId ?? (await apiJson<{ data: { id: string } }>(baseUrl, '/__aisys__/api/my-chat/conversations', cookie, { apiKeyId: gatewayKey.id })).data.id
    const models = (await apiJson<{ data: ChatModelListOption[] }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/models`, cookie)).data
    assert(models.length > 0, '真实会话没有可用模型')
    const available = await Promise.all(models.filter((model) => credential.models.includes(model.id)).map(async (model) => {
      const result = await apiJson<{ data: ChatModelOption }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/models/${encodeURIComponent(model.id)}`, cookie)
      return result.data
    }))
    assert(available.length > 0, '模型接口未返回凭据声明的支持模型')
    if (realProbe) {
      const turn = fixture[0]!
      const selectedModel = available[0]!
      const stream = await postChatStreamWithRecovery(baseUrl, cookie, conversationId, {
        clientMessageId: 'long-real-probe-01',
        content: turn.prompt,
        model: selectedModel.id
      }, 'chat-long-real-probe-01', runDeadline)
      assert.equal(stream.status, 200, `真实单轮 probe HTTP ${stream.status} ${stream.errorCode ?? ''}`)
      assert.equal(stream.terminalEvent, 'message.completed', `真实单轮 probe 缺少完成终态${formatSafeStreamFailure(stream.failure)}`)
      const probeQualityFailure = chatLongSessionArtifactQualityFailure({ responseMode: turn.responseMode, assistantOutput: stream.assistantOutput })
      assert(!probeQualityFailure, `真实单轮 probe artifact 质量失败${formatSafeStreamFailure(probeQualityFailure)}`)
      completedSummary = {
        ok: true,
        mode: 'real-probe',
        status: stream.status,
        terminalEvent: stream.terminalEvent,
        eventCount: stream.eventCount,
        firstDeltaMs: stream.firstDeltaMs,
        totalMs: stream.totalMs
      }
      executionSucceeded = true
    } else {
    let selectedModel = available[0]
    let reasoning: string | undefined
    let service: string | undefined
    const client = createSqliteDatabaseClient(databaseModule.getChatDatabase())
    const persistedMessages = resumedCheckpoint
      ? await chatRepository.listChatMessages(client, {
          conversationId,
          systemAccountId: access.systemAccountId,
          limit: 100,
          now: new Date().toISOString()
        })
      : []
    const resumeMessages: ChatLongSessionResumeMessage[] = persistedMessages.map((message) => ({
      id: message.id,
      turnId: message.turnId,
      sequenceNo: message.sequenceNo,
      role: message.role,
      status: message.status,
      contentText: message.contentText
    }))
    const resumePlan = buildChatLongSessionResumePlan(fixture, resumeMessages)
    const resumeInvocationId = resumedCheckpoint ? randomBytes(6).toString('hex') : undefined
    if (resumedCheckpoint) {
      assert.equal(resumedCheckpoint.completedTurns, resumePlan.lastCompletedTurn, '续跑检查点与数据库最后完成轮次不一致')
      assert.equal(resumedCheckpoint.canonicalHash, chatLongSessionResumeCanonicalHash(resumeMessages, resumePlan.lastCompletedTurn), '续跑检查点 canonical hash 不一致')
      assert.equal(resumedCheckpoint.turnMetrics.length, resumePlan.lastCompletedTurn, '续跑检查点指标数量不一致')
    }
    const turnMetrics: TurnMetric[] = resumedCheckpoint ? [...resumedCheckpoint.turnMetrics] : []
    if (resumedCheckpoint && await refreshUnavailableCheckpointUsage(turnMetrics, repositories, access)) {
      writeChatLongSessionCheckpoint(checkpointPath, { ...resumedCheckpoint, turnMetrics })
    }
    const assistantResponses: ChatLongSessionResponse[] = [...resumePlan.completedResponses]
    const usageRecords: PersistedUsageMetric[] = turnMetrics.map((metric) => metric.usage)
    if (!resumedCheckpoint) {
      writeChatLongSessionCheckpoint(checkpointPath, {
        schemaVersion: 1,
        fixtureHash: chatLongSessionFixtureHash(fixture),
        conversationId,
        accountId,
        gatewayKeyId: gatewayKey.id,
        completedTurns: 0,
        canonicalHash: chatLongSessionResumeCanonicalHash([], 0),
        turnMetrics: []
      })
    }

    for (const turn of fixture) {
      runBudget.assertActive(`main_turn_${turn.turn}`)
      let modelSwitch: TurnMetric['modelSwitch'] = { result: 'skipped', reason: 'not_requested' }
      if (turn.controls.model) {
        const alternate = alternateModel(available, selectedModel)
        modelSwitch = alternate.id === selectedModel.id ? { result: 'skipped', reason: 'capability_unavailable' } : { result: 'applied' }
        if (alternate.id !== selectedModel.id) { reasoning = undefined; service = undefined }
        selectedModel = alternate
      }
      let reasoningPlan = chooseControl(turn.controls.reasoning, selectedModel.supportedReasoningEfforts, reasoning)
      if (reasoningPlan.result === 'applied') reasoning = reasoningPlan.value
      let servicePlan = chooseControl(turn.controls.service, selectedModel.supportedServiceTiers, service)
      if (servicePlan.result === 'applied') service = servicePlan.value
      if (reasoning && !selectedModel.supportedReasoningEfforts.includes(reasoning)) { reasoning = undefined; reasoningPlan = { result: 'skipped', reason: 'capability_unavailable' } }
      if (service && !selectedModel.supportedServiceTiers.includes(service)) { service = undefined; servicePlan = { result: 'skipped', reason: 'capability_unavailable' } }
      if (turn.turn < resumePlan.nextTurn) continue
      const initialReplaceTurnId = turn.turn === resumePlan.nextTurn ? resumePlan.replaceTurnId : undefined
      const identityForAttempt = (attempt: number) => buildChatLongSessionAttemptIdentity(
        turn.turn,
        attempt,
        initialReplaceTurnId ? resumeInvocationId : undefined
      )
      const attemptOutcome = await runChatLongSessionTurnAttempts({
        maxAttempts: 3,
        sleep,
        submit: async ({ attempt, replaceTurnId }) => {
          const effectiveReplaceTurnId = replaceTurnId ?? initialReplaceTurnId
          const { clientMessageId, traceId } = identityForAttempt(attempt)
          const streamed = await postChatStreamWithRecovery(baseUrl, cookie, conversationId, {
            clientMessageId,
            content: turn.prompt,
            model: selectedModel.id,
            ...(reasoning ? { reasoningEffort: reasoning } : {}),
            ...(service ? { serviceTier: service } : {}),
            ...(effectiveReplaceTurnId ? { replaceTurnId: effectiveReplaceTurnId } : {})
          }, traceId, runDeadline)
          return applyChatLongSessionArtifactQualityGate(turn, streamed)
        },
        resolveAcceptedTurnId: async ({ attempt }) => {
          const submission = await apiJson<{ data: { state: 'not_found' | 'preparing' | 'accepted'; turnId?: string } }>(
            baseUrl,
            `/__aisys__/api/my-chat/conversations/${conversationId}/submissions/${identityForAttempt(attempt).clientMessageId}`,
            cookie
          )
          return submission.data.state === 'accepted' ? submission.data.turnId : undefined
        }
      })
      if (attemptOutcome.status === 'failed') {
        throw new Error(`第 ${turn.turn} 轮失败：${attemptOutcome.reason}${formatSafeStreamFailure(attemptOutcome.result.failure)}`)
      }
      const stream = attemptOutcome.result
      const traceId = identityForAttempt(attemptOutcome.attempts.length).traceId
      assert.equal(stream.status, 200, `第 ${turn.turn} 轮 HTTP ${stream.status} ${stream.errorCode ?? ''}`)
      assert.equal(stream.terminalEvent, 'message.completed', `第 ${turn.turn} 轮缺少完成终态${formatSafeStreamFailure(stream.failure)}`)
      assistantResponses.push({ turn: turn.turn, assistantOutput: stream.assistantOutput })
      const persistedUsage = await waitForPersistedUsage(repositories, access, traceId)
      usageRecords.push(persistedUsage)
      const context = (await apiJson<{ data: ContextStatus }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/context-status`, cookie)).data
      if (turn.memoryProbe || turn.turn === 50) {
        await apiJson(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages?beforeSequenceNo=${turn.turn * 2 + 1}&limit=6`, cookie)
      }
      turnMetrics.push({
        turn: turn.turn,
        traceId,
        firstDeltaMs: stream.firstDeltaMs,
        totalMs: stream.totalMs,
        eventCount: stream.eventCount,
        status: stream.status,
        terminalEvent: stream.terminalEvent,
        model: selectedModel.id,
        modelSwitch,
        reasoning: turn.controls.reasoning ? reasoningPlan : { result: reasoning ? 'applied' : 'skipped', ...(reasoning ? { value: reasoning } : { reason: 'not_requested' }) },
        service: turn.controls.service ? servicePlan : { result: service ? 'applied' : 'skipped', ...(service ? { value: service } : { reason: 'not_requested' }) },
        context,
        usage: persistedUsage,
        attempts: attemptOutcome.attempts,
        ...(stream.errorCode ? { errorCode: stream.errorCode } : {})
      })
      const checkpointMessages = (await chatRepository.listChatMessages(client, {
        conversationId,
        systemAccountId: access.systemAccountId,
        limit: 100,
        now: new Date().toISOString()
      })).map((message) => ({
        id: message.id,
        turnId: message.turnId,
        sequenceNo: message.sequenceNo,
        role: message.role,
        status: message.status,
        contentText: message.contentText
      }))
      writeChatLongSessionCheckpoint(checkpointPath, {
        schemaVersion: 1,
        fixtureHash: chatLongSessionFixtureHash(fixture),
        conversationId,
        accountId,
        gatewayKeyId: gatewayKey.id,
        completedTurns: turn.turn,
        canonicalHash: chatLongSessionResumeCanonicalHash(checkpointMessages, turn.turn),
        turnMetrics
      })
    }

    assert.equal(usageRecords.filter((item) => item.status === 'available').length, 50, '主会话必须形成 50 条成功持久化 usage')
    assert.equal(usageRecords.length, 50)
    const before51 = await waitForAcceptanceSnapshotStable(databaseModule, client, conversationId, access.systemAccountId, { minimumStableMs: 1_500, repositories, access })
    assert.equal(before51.messageCount, 100)
    assert.equal(before51.idempotencyCount, 50)
    assert.equal(before51.completedAssistantCount, 50)
    assert.equal(before51.userTurnCount, 50)
    assert.equal(before51.activeTurn, false)
    const turn51 = await postChatStream(baseUrl, cookie, conversationId, {
      clientMessageId: 'long-real-51-rejected', content: '这条请求必须在任何副作用前被拒绝。', model: selectedModel.id
    }, undefined, runDeadline)
    assert.deepEqual([turn51.status, turn51.errorCode], [409, 'chat_turn_limit_exceeded'])
    const after51 = await waitForAcceptanceSnapshotStable(databaseModule, client, conversationId, access.systemAccountId, { minimumStableMs: 2_000, repositories, access })
    assert.deepEqual(
      withoutChatLongSessionAcceptanceObservability(after51),
      withoutChatLongSessionAcceptanceObservability(before51),
      '第 51 次 HTTP 发送不得改变消息、幂等、轮次、上下文、checkpoint、资产或账本'
    )
    assert(after51.auditLogCount >= before51.auditLogCount, '异步审计总数不得倒退')

    await cleanupPreviousControlledConversations(client, chatRepository, access.systemAccountId, conversationId)
    const controlled = await seedControlledConversation({
      client,
      chatRepository,
      systemAccountId: access.systemAccountId,
      apiKeyId: gatewayKey.id,
      model: smallestContextModel(available)
    })
    let controlledContext = (await apiJson<{ data: ContextStatus }>(baseUrl, `/__aisys__/api/my-chat/conversations/${controlled.conversationId}/context-status`, cookie)).data
    const controlledStates = [controlledContext.state]
    const preCompactionFollowups = [
      '基于现有完整项目，说明最早锚点并继续保留最近代码，把 REQ-C43 加入根 html 唯一的 data-requirements 属性并实现打印页眉。',
      '继续同一项目，保留上一轮打印页眉，把 REQ-C44 加入根 html 唯一的 data-requirements 属性并实现紧凑密度模式。'
    ]
    const controlledResponses: Array<{ firstDeltaMs: number | null; totalMs: number; status: number }> = []
    for (let index = 0; index < preCompactionFollowups.length; index += 1) {
      const controlledTraceId = `chat-long-controlled-${index + 43}`
      const streamed = await runControlledChatTurnWithRetries({
        baseUrl,
        cookie,
        conversationId: controlled.conversationId,
        clientMessageIdBase: `controlled-http-${index + 43}`,
        traceIdBase: controlledTraceId,
        content: preCompactionFollowups[index]!,
        model: controlled.model.id,
        runDeadline
      })
      assert.equal(streamed.status, 200)
      controlledResponses.push({ firstDeltaMs: streamed.firstDeltaMs, totalMs: streamed.totalMs, status: streamed.status })
      controlledContext = (await apiJson<{ data: ContextStatus }>(baseUrl, `/__aisys__/api/my-chat/conversations/${controlled.conversationId}/context-status`, cookie)).data
      controlledStates.push(controlledContext.state)
    }
    const naturalCompactionBaseline = controlledContext
    const naturalObservation = controlled.naturalEligible
      ? await waitForNaturalCompaction(baseUrl, cookie, controlled.conversationId, naturalCompactionBaseline, controlledStates)
      : { context: naturalCompactionBaseline, observedCompactionChange: false }
    controlledContext = naturalObservation.context
    let compactionResult: { trigger: 'natural' | 'controlled'; status: string; reason?: string; sourceThroughSequence?: number }
    if (naturalObservation.observedCompactionChange && controlledContext.compactedThroughSequence > 0) {
      compactionResult = { trigger: 'natural', status: 'installed', sourceThroughSequence: controlledContext.compactedThroughSequence }
    } else {
      controlledContext = await waitForNoActiveNaturalCompaction(baseUrl, cookie, controlled.conversationId, controlledStates)
      const controlledHead = await snapshotLedger(client, controlled.conversationId, access.systemAccountId)
      assert.equal(controlledHead.activeTurn, false)
      assert.notEqual(controlledHead.contextState, 'compacting')
      assert.notEqual(controlledHead.contextState, 'compact_pending')
      runBudget.assertActive('controlled_compaction_before')
      const compactionRemainingMs = runBudget.remainingMs('controlled_compaction')
      const compacted = await compactChatContextOnce({
        client,
        conversationId: controlled.conversationId,
        systemAccountId: access.systemAccountId,
        apiKeySecret: gatewayKey.key,
        gatewayBaseUrl: baseUrl,
        model: controlled.model.id,
        protocol: controlled.model.supportedApiProtocols.includes('responses') ? 'responses' : 'chat_completions',
        effectiveContextLimitTokens: controlled.model.maxInputTokens ?? controlled.model.contextWindowTokens,
        signal: runBudget.signalFor(compactionRemainingMs, 'controlled_compaction'),
        requestTimeoutMs: compactionRemainingMs
      })
      runBudget.assertActive('controlled_compaction_after')
      compactionResult = { trigger: 'controlled', status: compacted.status, ...('reason' in compacted ? { reason: compacted.reason } : {}), ...('sourceThroughSequence' in compacted ? { sourceThroughSequence: compacted.sourceThroughSequence } : {}) }
      controlledContext = (await apiJson<{ data: ContextStatus }>(baseUrl, `/__aisys__/api/my-chat/conversations/${controlled.conversationId}/context-status`, cookie)).data
    }
    assert(controlledContext.compactedThroughSequence > 0, '受控会话未形成有效 checkpoint')
    const recall = await runControlledChatTurnWithRetries({
      baseUrl,
      cookie,
      conversationId: controlled.conversationId,
      clientMessageIdBase: 'controlled-http-45',
      traceIdBase: 'chat-long-controlled-45',
      content: '压缩完成后最终返回完整 HTML，并同时保留早期锚点 PROJECT-AURORA-FOUNDATION、最近版本标记 REQ-C42，以及压缩后新增需求 REQ-C43 和 REQ-C44。',
      model: controlled.model.id,
      runDeadline
    })
    assert.equal(recall.status, 200)
    assert.match(recall.assistantOutput, /PROJECT-AURORA-FOUNDATION/, '压缩后回答遗漏早期锚点')
    assert.match(recall.assistantOutput, /REQ-C42/, '压缩后回答遗漏最近预填项目版本')
    assert.match(recall.assistantOutput, /REQ-C43/, '压缩后回答遗漏压缩前新增需求')
    assert.match(recall.assistantOutput, /REQ-C44/, '压缩后回答遗漏压缩前最近需求')
    controlledResponses.push({ firstDeltaMs: recall.firstDeltaMs, totalMs: recall.totalMs, status: recall.status })
    controlledContext = await waitForContextSettled(baseUrl, cookie, controlled.conversationId, controlledStates)
    const controlledLedger = await snapshotLedger(client, controlled.conversationId, access.systemAccountId)
    assert.equal(controlledLedger.userTurnCount, controlled.seededTurns + preCompactionFollowups.length + 1)
    assert.equal(controlledLedger.messageCount, controlledLedger.userTurnCount * 2)
    assert.equal(controlledLedger.idempotencyCount, controlledLedger.userTurnCount)
    assert.equal(controlledLedger.nextSequenceNo, controlledLedger.messageCount + 1)
    assert.equal(controlledLedger.distinctSequenceCount, controlledLedger.messageCount)
    assert.equal(controlledLedger.completedAssistantCount, controlledLedger.userTurnCount)
    assert.equal(controlledLedger.reservedBytes, 0)

    const score = scoreChatLongSession(fixture, assistantResponses)
    let scoreFailure: Error | undefined
    try {
      assertChatLongSessionScore(score)
      acceptancePassed = true
    } catch (error) {
      scoreFailure = error instanceof Error ? error : new Error(String(error))
    }
    const report = {
      schemaVersion: 1,
      generatedAt: new Date().toISOString(),
      mode: 'real',
      fixture: buildSafeFixtureSummary(fixture),
      entities: { conversationHash: hashId(conversationId), controlledConversationHash: hashId(controlled.conversationId), accountHash: hashId(accountId) },
      acceptance: { passed: acceptancePassed, ...(scoreFailure ? { failureCode: scoreFailure.message } : {}) },
      score,
      turns: turnMetrics,
      turn51: { status: turn51.status, errorCode: turn51.errorCode, snapshotStable: true, before: before51 },
      controlled: {
        seededTurns: controlled.seededTurns,
        targetBytes: controlled.targetBytes,
        targetTokens: controlled.targetTokens,
        estimatedTokens: controlled.estimatedTokens,
        naturalEligible: controlled.naturalEligible,
        ...(controlled.controlledReason ? { controlledReason: controlled.controlledReason } : {}),
        ledger: controlledLedger,
        observedStates: [...new Set(controlledStates)],
        context: controlledContext,
        compaction: compactionResult,
        followups: controlledResponses
      },
      environmentObservation: environmentObservation(tempRoot, runtimeConfig)
    }
    const runHash = hashId(`${tempRoot}:${report.generatedAt}`).slice(0, 12)
    completedReport = report
    completedOutputPath = resolveOutputPath(runHash)
    completedSummary = { ok: acceptancePassed, outputPath: completedOutputPath, score: report.score, turnCount: turnMetrics.length, controlled: compactionResult.trigger }
    executionSucceeded = true
    if (scoreFailure) throw scoreFailure
    }
  } catch (error) {
    const message = redactText(error instanceof Error ? error.stack ?? error.message : String(error), knownSecrets, credential.baseUrl)
    const diagnostics = realProbe ? '' : redactText(childDiagnostics, knownSecrets, credential.baseUrl)
    primaryError = new Error(`${message}${diagnostics ? `\n临时后端诊断（已脱敏）：\n${diagnostics}` : ''}\n续跑目录：${tempRoot}`, {
      cause: sanitizeErrorForDiagnostics(error, [...knownSecrets, credential.baseUrl])
    })
  } finally {
    if (interruptedSignal && !primaryError) primaryError = new Error(`chat_long_session_interrupted_${interruptedSignal}`)
    try {
      await runIndependentCleanup(primaryError, [
        async () => {
          try {
            const stopErrors: unknown[] = []
            try { if (backendProcessTracker) backendProcessTree = await backendProcessTracker.stop() } catch (error) { stopErrors.push(error) }
            try { if (signalStopPromise) await signalStopPromise } catch (error) { stopErrors.push(error) }
            if (signalStopError) stopErrors.push(signalStopError)
            try { await stopProcess(backend, { tracked: backendProcessTree, servicePort: backendPort }) } catch (error) { stopErrors.push(error) }
            try { await assertTrackedProcessIdentitiesStopped(backendProcessTree) } catch (error) { stopErrors.push(error) }
            if (stopErrors.length) {
              throw new AggregateError(
                stopErrors.map((error) => sanitizeErrorForDiagnostics(error, [...knownSecrets, credential.baseUrl])),
                'chat_long_session_stop_failed'
              )
            }
          } catch (error) {
            cleanupHealthy = false
            throw error
          }
        },
        async () => {
          try {
            runnerReadWorkerIdentities = await captureRunnerLocalSqliteReadWorkers()
            await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
            await assertTrackedProcessIdentitiesStopped(runnerReadWorkerIdentities)
          } catch (error) {
            cleanupHealthy = false
            throw error
          }
        },
        async () => {
          try {
            databaseModule.closeStorageDatabases()
          } catch (error) {
            cleanupHealthy = false
            throw error
          }
        },
        async () => {
          if (!realProbe && executionSucceeded) {
            assert(completedReport && completedOutputPath)
            mkdirSync(dirname(completedOutputPath), { recursive: true })
            writeFileSync(completedOutputPath, `${JSON.stringify(completedReport, null, 2)}\n`, { encoding: 'utf8', flag: 'wx' })
            reportWritten = true
          }
        },
        async () => {
          if (shouldRemoveChatLongSessionTemp({
            keepTemp,
            executionSucceeded,
            acceptancePassed,
            realProbe,
            reportWritten,
            primaryError: Boolean(primaryError),
            cleanupHealthy
          })) {
            await removeTempRoot(tempRoot, [...backendProcessTree, ...runnerReadWorkerIdentities])
          }
        }
      ], [...knownSecrets, credential.baseUrl])
    } finally {
      process.removeListener('SIGINT', handleSigint)
      process.removeListener('SIGTERM', handleSigterm)
      activeRunBudget = undefined
      activeDiagnosticSecrets = []
    }
  }
  if (realProbe) {
    assert(completedSummary)
    console.log(JSON.stringify(completedSummary))
    return
  }
  assert(completedReport && completedOutputPath && completedSummary)
  assert(reportWritten)
  console.log(JSON.stringify(completedSummary))
}

async function cleanupPreviousControlledConversations(
  client: import('../../storage/database-client.js').DatabaseClient,
  chatRepository: typeof import('../../storage/chat.repository.js'),
  systemAccountId: string,
  mainConversationId: string
): Promise<void> {
  const rows = await client.query<{ id?: unknown }>(`
    SELECT id FROM chat_conversations
    WHERE system_account_id = ? AND id <> ?
    ORDER BY created_at ASC
  `, [systemAccountId, mainConversationId])
  for (const row of rows) {
    const conversationId = String(row.id ?? '')
    if (conversationId) await chatRepository.deleteChatConversation(client, conversationId, systemAccountId)
  }
}

async function runControlledChatTurnWithRetries(input: {
  baseUrl: string
  cookie: string
  conversationId: string
  clientMessageIdBase: string
  traceIdBase: string
  content: string
  model: string
  runDeadline: number
}): Promise<Awaited<ReturnType<typeof postChatStream>>> {
  const clientMessageIdForAttempt = (attempt: number) => attempt === 1
    ? input.clientMessageIdBase
    : `${input.clientMessageIdBase}-retry-${attempt}`
  const traceIdForAttempt = (attempt: number) => attempt === 1
    ? input.traceIdBase
    : `${input.traceIdBase}-retry-${attempt}`
  const outcome = await runChatLongSessionTurnAttempts({
    maxAttempts: 3,
    sleep,
    submit: ({ attempt, replaceTurnId }) => postChatStreamWithRecovery(input.baseUrl, input.cookie, input.conversationId, {
      clientMessageId: clientMessageIdForAttempt(attempt),
      content: input.content,
      model: input.model,
      ...(replaceTurnId ? { replaceTurnId } : {})
    }, traceIdForAttempt(attempt), input.runDeadline),
    resolveAcceptedTurnId: async ({ attempt }) => {
      const submission = await apiJson<{ data: { state: 'not_found' | 'preparing' | 'accepted'; turnId?: string } }>(
        input.baseUrl,
        `/__aisys__/api/my-chat/conversations/${input.conversationId}/submissions/${clientMessageIdForAttempt(attempt)}`,
        input.cookie
      )
      return submission.data.state === 'accepted' ? submission.data.turnId : undefined
    }
  })
  if (outcome.status === 'failed') {
    throw new Error(`受控会话失败：${outcome.reason}${formatSafeStreamFailure(outcome.result.failure)}`)
  }
  return outcome.result
}

async function seedControlledConversation(input: {
  client: import('../../storage/database-client.js').DatabaseClient
  chatRepository: typeof import('../../storage/chat.repository.js')
  systemAccountId: string
  apiKeyId: string
  model: ChatModelOption
}): Promise<{ conversationId: string; seededTurns: number; targetBytes: number; targetTokens: number; estimatedTokens: number; naturalEligible: boolean; controlledReason?: string; model: ChatModelOption }> {
  const seededTurns = 42
  const tokenLimit = input.model.maxInputTokens ?? input.model.contextWindowTokens
  const naturalTargetTokens = tokenLimit ? Math.ceil(tokenLimit * 0.72) : 0
  const targetTokens = Math.min(naturalTargetTokens, chatLongSessionControlledSeedMaxTokens)
  const seedPlan = buildChatLongSessionSemanticSeedPlan(
    seededTurns,
    targetTokens,
    (label) => activeRunBudget?.assertActive(label)
  )
  const now = new Date().toISOString()
  const conversation = await input.chatRepository.createChatConversation(input.client, {
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    apiKeyNameSnapshot: '长会话真实验收 Key',
    now,
    maxConversationsPerUser: 1000
  })
  for (let index = 1; index <= seededTurns; index += 1) {
    const accepted = await input.chatRepository.acceptChatTurn(input.client, {
      conversationId: conversation.id,
      systemAccountId: input.systemAccountId,
      clientMessageId: `controlled-seed-${String(index).padStart(2, '0')}`,
      userContent: `继续 Aurora Dashboard 第 ${index} 个连续版本；保留 PROJECT-AURORA-FOUNDATION，根 html 唯一的 data-requirements 属性累计加入 REQ-C${String(index).padStart(2, '0')}。`,
      model: input.model.id,
      now: new Date(Date.now() + index * 1000).toISOString(),
      storageQuotaBytes: 64 * 1024 * 1024,
      retentionDays: 7,
      maxTurnsPerConversation: 49
    })
    assert.equal(accepted.duplicate, false)
    const artifact = seedPlan.artifacts[index - 1]
    assert(Buffer.byteLength(artifact, 'utf8') < 192 * 1024)
    await input.chatRepository.completeChatTurn(input.client, {
      conversationId: conversation.id,
      systemAccountId: input.systemAccountId,
      turnId: accepted.turnId,
      assistantContent: artifact,
      finishReason: 'stop',
      traceId: `controlled-${index}`,
      now: new Date(Date.now() + index * 1000 + 500).toISOString()
    })
  }
  const ledger = await snapshotLedger(input.client, conversation.id, input.systemAccountId)
  assert.equal(ledger.messageCount, seededTurns * 2)
  assert.equal(ledger.idempotencyCount, seededTurns)
  assert.equal(ledger.userTurnCount, seededTurns)
  assert.equal(ledger.nextSequenceNo, seededTurns * 2 + 1)
  assert.equal(ledger.distinctSequenceCount, seededTurns * 2)
  assert.equal(ledger.completedAssistantCount, seededTurns)
  assert.equal(ledger.reservedBytes, 0)
  assert(ledger.contentBytes < 4 * 1024 * 1024)
  return {
    conversationId: conversation.id,
    seededTurns,
    targetBytes: seedPlan.totalBytes,
    targetTokens,
    estimatedTokens: seedPlan.totalTokens,
    naturalEligible: Boolean(tokenLimit) && seedPlan.totalTokens >= naturalTargetTokens,
    ...(!tokenLimit
      ? { controlledReason: 'effective_context_limit_unavailable' }
      : targetTokens < naturalTargetTokens
        ? { controlledReason: 'safe_seed_cap_below_72_percent' }
        : seedPlan.totalTokens >= naturalTargetTokens ? {} : { controlledReason: 'safe_seed_limits_below_72_percent' }),
    model: input.model
  }
}

async function snapshotLedger(client: import('../../storage/database-client.js').DatabaseClient, conversationId: string, systemAccountId: string): Promise<LedgerSnapshot> {
  const conversation = await client.one<Record<string, unknown>>('SELECT * FROM chat_conversations WHERE id = ? AND system_account_id = ?', [conversationId, systemAccountId])
  assert(conversation)
  const messages = await client.one<Record<string, unknown>>('SELECT COUNT(*) AS total, COALESCE(SUM(content_bytes), 0) AS bytes, COALESCE(SUM(storage_reserved_bytes), 0) AS reserved FROM chat_messages WHERE conversation_id = ? AND system_account_id = ?', [conversationId, systemAccountId])
  const messageIntegrity = await client.one<Record<string, unknown>>("SELECT COUNT(DISTINCT sequence_no) AS sequences, SUM(CASE WHEN role = 'assistant' AND status = 'completed' THEN 1 ELSE 0 END) AS completed_assistants FROM chat_messages WHERE conversation_id = ? AND system_account_id = ?", [conversationId, systemAccountId])
  const idempotency = await client.one<Record<string, unknown>>('SELECT COUNT(*) AS total FROM chat_message_idempotency WHERE conversation_id = ? AND system_account_id = ?', [conversationId, systemAccountId])
  const checkpoints = await client.one<Record<string, unknown>>('SELECT COUNT(*) AS total FROM chat_context_checkpoints WHERE conversation_id = ? AND system_account_id = ?', [conversationId, systemAccountId])
  const assets = await client.one<Record<string, unknown>>('SELECT COUNT(*) AS total FROM chat_assets WHERE conversation_id = ? AND system_account_id = ?', [conversationId, systemAccountId])
  return {
    messageCount: Number(messages?.total ?? 0),
    idempotencyCount: Number(idempotency?.total ?? 0),
    userTurnCount: Number(conversation.user_turn_count ?? 0),
    contextRevision: Number(conversation.context_revision ?? 0),
    checkpointCount: Number(checkpoints?.total ?? 0),
    assetCount: Number(assets?.total ?? 0),
    contentBytes: Number(messages?.bytes ?? 0),
    reservedBytes: Number(messages?.reserved ?? 0),
    compactedThroughSequence: Number(conversation.compacted_through_sequence ?? 0),
    nextSequenceNo: Number(conversation.next_sequence_no ?? 0),
    completedAssistantCount: Number(messageIntegrity?.completed_assistants ?? 0),
    distinctSequenceCount: Number(messageIntegrity?.sequences ?? 0),
    activeTurn: Boolean(conversation.active_turn_id),
    contextState: String(conversation.context_state ?? '')
  }
}

async function waitForAcceptanceSnapshotStable(
  databaseModule: typeof import('../../storage/database.js'),
  client: import('../../storage/database-client.js').DatabaseClient,
  conversationId: string,
  systemAccountId: string,
  options: {
    minimumStableMs: number
    repositories: typeof import('../../storage/repositories.js')
    access: { systemAccountId: string; role: 'user' }
  }
): Promise<AcceptanceSnapshot> {
  let previous: AcceptanceSnapshot | undefined
  let stableSince = Date.now()
  let consecutiveStable = 0
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    const ledger = await snapshotLedger(client, conversationId, systemAccountId)
    const usage = databaseModule.getUsageCatalogDatabase().prepare('SELECT COUNT(*) AS total FROM usage_record_shard_entries').get() as { total?: unknown } | undefined
    const audit = databaseModule.getDatasetDatabase().prepare('SELECT COUNT(*) AS total FROM audit_logs').get() as { total?: unknown } | undefined
    const attempts = databaseModule.getDatasetDatabase().prepare('SELECT COUNT(*) AS total FROM audit_log_attempts').get() as { total?: unknown } | undefined
    const persistedUsage = await options.repositories.listUsageRecordsAsync(options.access, { page: 1, pageSize: 100, trafficSource: 'gateway', result: 'all' })
    const usageWhitelist = persistedUsage.items.map((item) => ({
      traceHash: hashId(item.traceId), success: item.success, statusCode: item.statusCode, model: item.model,
      serviceTier: item.billedServiceTier, inputTokens: item.inputTokens, outputTokens: item.outputTokens,
      cacheReadTokens: item.cacheReadTokens, cacheWriteTokens: item.cacheWriteTokens, cacheWrite1hTokens: item.cacheWrite1hTokens,
      costUsd: item.costUsd, firstTokenMs: item.firstTokenMs, durationMs: item.durationMs
    })).sort((left, right) => left.traceHash.localeCompare(right.traceHash))
    const canonicalRowsHash = canonicalAcceptanceHash(databaseModule, conversationId, systemAccountId)
    const current: AcceptanceSnapshot = {
      ...ledger,
      usageRecordEntryCount: Number(usage?.total ?? 0),
      auditLogCount: Number(audit?.total ?? 0),
      upstreamAttemptCount: Number(attempts?.total ?? 0),
      canonicalHash: createHash('sha256').update(`${canonicalRowsHash}:${stableJson(usageWhitelist)}`).digest('hex')
    }
    if (previous && JSON.stringify(previous) === JSON.stringify(current)) consecutiveStable += 1
    else { consecutiveStable = 0; stableSince = Date.now() }
    if (consecutiveStable >= 3 && Date.now() - stableSince >= options.minimumStableMs) return current
    previous = current
    await sleep(250)
  }
  throw new Error('chat_long_session_acceptance_snapshot_not_stable')
}

function canonicalAcceptanceHash(databaseModule: typeof import('../../storage/database.js'), conversationId: string, systemAccountId: string): string {
  const chat = databaseModule.getChatDatabase()
  const catalog = databaseModule.getUsageCatalogDatabase()
  const snapshot = {
    conversation: chat.prepare('SELECT * FROM chat_conversations WHERE id = ? AND system_account_id = ?').all(conversationId, systemAccountId),
    messages: chat.prepare('SELECT * FROM chat_messages WHERE conversation_id = ? AND system_account_id = ? ORDER BY sequence_no, id').all(conversationId, systemAccountId),
    idempotency: chat.prepare('SELECT * FROM chat_message_idempotency WHERE conversation_id = ? AND system_account_id = ? ORDER BY client_message_id').all(conversationId, systemAccountId),
    checkpoints: chat.prepare('SELECT * FROM chat_context_checkpoints WHERE conversation_id = ? AND system_account_id = ? ORDER BY version, id').all(conversationId, systemAccountId),
    entries: chat.prepare('SELECT * FROM chat_context_entries WHERE conversation_id = ? ORDER BY checkpoint_id, sequence').all(conversationId),
    assets: chat.prepare('SELECT * FROM chat_assets WHERE conversation_id = ? AND system_account_id = ? ORDER BY id').all(conversationId, systemAccountId),
    storageWindows: chat.prepare('SELECT * FROM chat_user_storage_windows WHERE system_account_id = ? ORDER BY bucket_date').all(systemAccountId),
    usageCatalog: catalog.prepare('SELECT * FROM usage_record_shard_entries ORDER BY usage_id').all()
  }
  return createHash('sha256').update(stableJson(snapshot)).digest('hex')
}

function stableJson(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`
  if (value instanceof Uint8Array) return JSON.stringify({ bytesHash: createHash('sha256').update(value).digest('hex'), byteLength: value.byteLength })
  return `{${Object.entries(value as Record<string, unknown>).sort(([left], [right]) => left.localeCompare(right)).map(([key, child]) => `${JSON.stringify(key)}:${stableJson(child)}`).join(',')}}`
}

async function postChatStream(baseUrl: string, cookie: string, conversationId: string, body: Record<string, unknown>, traceId?: string, streamDeadline = Date.now() + chatTurnHardDeadlineMs): Promise<{
  status: number; firstDeltaMs: number | null; totalMs: number; eventCount: number; terminalEvent: string | null; assistantOutput: string; turnId?: string; errorCode?: string; failure?: SafeChatStreamFailure; progress?: ChatLongSessionStreamProgressSnapshot
}> {
  const startedAt = performance.now()
  const progress = new ChatLongSessionStreamProgress({ startedAt: Date.now(), eventIdleMs: chatStreamEventIdleMs, progressIdleMs: chatStreamProgressIdleMs })
  const headerDeadline = Math.min(streamDeadline, progress.nextDeadlineAt())
  let response: Response
  try {
    response = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST',
      headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream', ...(traceId ? { 'x-trace-id': traceId } : {}) },
      body: JSON.stringify(body),
      signal: activeRunBudget?.signalFor(Math.max(1, headerDeadline - Date.now()), 'chat_stream_headers') ?? AbortSignal.timeout(Math.max(1, headerDeadline - Date.now()))
    })
  } catch (error) {
    if (activeRunBudget?.signal.aborted) throw error
    const now = Date.now()
    if (now + 10 < headerDeadline) throw error
    throw new ChatLongSessionStreamDeadlineError({
      reason: streamDeadline <= progress.nextDeadlineAt() ? 'hard_deadline' : 'event_idle',
      progress: progress.snapshot(now, 0),
      firstDeltaMs: null,
      totalMs: elapsed(startedAt)
    })
  }
  if (!response.ok || !response.body) {
    const payload = await readBoundedText(response, 64 * 1024)
    const errorCode = parseErrorCode(payload) ?? `http_${response.status}`
    const failure = extractSafeChatStreamFailure('message.failed', {
      code: errorCode,
      message: `HTTP ${response.status}`
    }, activeDiagnosticSecrets)
    return { status: response.status, firstDeltaMs: null, totalMs: elapsed(startedAt), eventCount: 0, terminalEvent: null, assistantOutput: '', errorCode, failure }
  }
  const reader = response.body.getReader()
  let totalBytes = 0
  let firstDeltaMs: number | null = null
  let terminalEvent: string | null = null
  let turnId: string | undefined
  let failure: SafeChatStreamFailure | undefined
  let assistantOutput = ''
  const parser = createBoundedSseParser({
    maxEventCount: resolveChatLongSessionMaxEventCount(process.env.JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS),
    maxBufferChars: 2 * 1024 * 1024,
    onEvent: (event) => {
      if (event.name === 'message.started' && typeof event.data.turnId === 'string') turnId = event.data.turnId
      if (event.name === 'message.delta' && typeof event.data.delta === 'string') {
        if (firstDeltaMs === null) firstDeltaMs = elapsed(startedAt)
        assistantOutput += event.data.delta
        if (Buffer.byteLength(assistantOutput, 'utf8') > 4 * 1024 * 1024) throw new Error('chat_long_session_artifact_memory_limit_exceeded')
      }
      if (event.name === 'message.failed') {
        failure = extractSafeChatStreamFailure(event.name, event.data, [
          ...activeDiagnosticSecrets,
          typeof body.content === 'string' ? body.content : undefined,
          assistantOutput || undefined
        ])
      }
      if (event.name === 'message.completed' || event.name === 'message.failed' || event.name === 'message.canceled') terminalEvent = event.name
      progress.observe(event.name, event.data, Date.now(), Buffer.byteLength(assistantOutput, 'utf8'))
    }
  })
  try {
    await consumeReaderWithBoundedCancellation(reader, async () => {
    while (true) {
      const { done, value } = await readChatStreamChunk(reader, progress, streamDeadline)
      if (done) break
      totalBytes += value.byteLength
      if (totalBytes > 32 * 1024 * 1024) throw new Error('chat_long_session_sse_limit_exceeded')
      parser.push(value)
    }
    parser.finish()
    })
  } catch (error) {
    if (error instanceof ChatLongSessionStreamDeadlineError) {
      throw new ChatLongSessionStreamDeadlineError({
        reason: error.reason,
        progress: progress.snapshot(Date.now(), parser.eventCount),
        turnId,
        firstDeltaMs,
        totalMs: elapsed(startedAt)
      })
    }
    throw new ChatLongSessionStreamConsumptionError({
      failure: safeStreamConsumptionFailure(error),
      progress: progress.snapshot(Date.now(), parser.eventCount),
      turnId,
      firstDeltaMs,
      totalMs: elapsed(startedAt)
    })
  }
  return {
    status: response.status,
    firstDeltaMs,
    totalMs: elapsed(startedAt),
    eventCount: parser.eventCount,
    terminalEvent,
    assistantOutput,
    ...(turnId ? { turnId } : {}),
    ...(failure ? { failure, errorCode: failure.code } : {})
  }
}

type ChatLongSessionStreamDeadlineReason = ChatLongSessionStreamIdleReason | 'hard_deadline'

class ChatLongSessionStreamDeadlineError extends Error {
  readonly reason: ChatLongSessionStreamDeadlineReason
  readonly progress: ChatLongSessionStreamProgressSnapshot
  readonly turnId?: string
  readonly firstDeltaMs: number | null
  readonly totalMs: number

  constructor(input: { reason: ChatLongSessionStreamDeadlineReason; progress: ChatLongSessionStreamProgressSnapshot; turnId?: string; firstDeltaMs: number | null; totalMs: number }) {
    super(`chat_long_session_stream_${input.reason}`)
    this.reason = input.reason
    this.progress = input.progress
    this.turnId = input.turnId
    this.firstDeltaMs = input.firstDeltaMs
    this.totalMs = input.totalMs
  }
}

class ChatLongSessionStreamConsumptionError extends Error {
  readonly failure: SafeChatStreamFailure
  readonly progress: ChatLongSessionStreamProgressSnapshot
  readonly turnId?: string
  readonly firstDeltaMs: number | null
  readonly totalMs: number

  constructor(input: { failure: SafeChatStreamFailure; progress: ChatLongSessionStreamProgressSnapshot; turnId?: string; firstDeltaMs: number | null; totalMs: number }) {
    super(`chat_long_session_stream_consumption_failed:${input.failure.code}`)
    this.failure = input.failure
    this.progress = input.progress
    this.turnId = input.turnId
    this.firstDeltaMs = input.firstDeltaMs
    this.totalMs = input.totalMs
  }
}

function safeStreamConsumptionFailure(error: unknown): SafeChatStreamFailure {
  const message = error instanceof Error ? error.message : ''
  const localCode = /^chat_long_session_[a-z0-9_]+$/i.test(message) ? message.toLowerCase() : undefined
  return extractSafeChatStreamFailure('message.failed', localCode
    ? { code: localCode, message: 'local stream validation failed' }
    : { code: 'gateway_stream_connection_error', message: 'connection failed' }, activeDiagnosticSecrets)
}

async function readChatStreamChunk(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  progress: ChatLongSessionStreamProgress,
  hardDeadline: number
): Promise<ReadableStreamReadResult<Uint8Array>> {
  const now = Date.now()
  const idleReason = progress.expiredReason(now)
  if (idleReason) throw new ChatLongSessionStreamDeadlineError({ reason: idleReason, progress: progress.snapshot(now, 0), firstDeltaMs: null, totalMs: 0 })
  if (now >= hardDeadline) throw new ChatLongSessionStreamDeadlineError({ reason: 'hard_deadline', progress: progress.snapshot(now, 0), firstDeltaMs: null, totalMs: 0 })
  const nextIdleDeadline = progress.nextDeadlineAt()
  const deadline = Math.min(nextIdleDeadline, hardDeadline)
  const reason: ChatLongSessionStreamDeadlineReason = hardDeadline <= nextIdleDeadline
    ? 'hard_deadline'
    : (progress.expiredReason(deadline) ?? 'event_idle')
  return new Promise<ReadableStreamReadResult<Uint8Array>>((resolveRead, rejectRead) => {
    const timeout = setTimeout(() => rejectRead(new ChatLongSessionStreamDeadlineError({ reason, progress: progress.snapshot(Date.now(), 0), firstDeltaMs: null, totalMs: 0 })), Math.max(1, deadline - now))
    reader.read().then(
      (result) => { clearTimeout(timeout); resolveRead(result) },
      (error) => { clearTimeout(timeout); rejectRead(error) }
    )
  })
}

function formatSafeStreamFailure(failure: SafeChatStreamFailure | undefined): string {
  return failure ? `；${failure.type} ${failure.code}: ${failure.message}` : ''
}

async function postChatStreamWithRecovery(
  baseUrl: string,
  cookie: string,
  conversationId: string,
  body: Record<string, unknown> & { clientMessageId: string },
  traceId: string,
  runDeadline: number,
  streamRequest: typeof postChatStream = postChatStream
): ReturnType<typeof postChatStream> {
  const turnDeadline = Math.min(runDeadline, Date.now() + chatTurnHardDeadlineMs)
  let attempt = 0
  while (Date.now() < turnDeadline) {
    try {
      const streamed = await streamRequest(baseUrl, cookie, conversationId, body, traceId, turnDeadline)
      if (streamed.status === 409 && (streamed.errorCode === 'chat_message_already_exists' || streamed.errorCode === 'chat_message_in_progress')) {
        throw new Error(streamed.errorCode)
      }
      return streamed
    } catch (error) {
      if (error instanceof ChatLongSessionStreamDeadlineError) {
        return recoverAcceptedStreamFailure(baseUrl, cookie, conversationId, body.clientMessageId, {
          failure: extractSafeChatStreamFailure('message.failed', { code: `gateway_stream_${error.reason}_timeout`, message: 'upstream timed out' }, activeDiagnosticSecrets),
          progress: error.progress,
          turnId: error.turnId,
          firstDeltaMs: error.firstDeltaMs,
          totalMs: error.totalMs
        })
      }
      if (error instanceof ChatLongSessionStreamConsumptionError) {
        return recoverAcceptedStreamFailure(baseUrl, cookie, conversationId, body.clientMessageId, error)
      }
      while (Date.now() < turnDeadline) {
        let submission: { data: { state: 'not_found' | 'preparing' | 'accepted'; turnId?: string; assistantStatus?: string } }
        try {
          submission = await apiJson(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/submissions/${body.clientMessageId}`, cookie)
        } catch {
          await sleep(500)
          continue
        }
        const decision = decideChatSubmissionRecovery({ ...submission.data, attempt, timedOut: Date.now() >= turnDeadline })
        if (decision.action === 'retry') { attempt += 1; break }
        if (decision.action === 'wait') { await sleep(500); continue }
        if (decision.action === 'recover_completed') {
          let messages: { data: Array<{ clientMessageId?: string; role: string; contentText: string }> }
          try {
            messages = await apiJson(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages`, cookie)
          } catch {
            await sleep(500)
            continue
          }
          const userIndex = messages.data.findIndex((message) => message.clientMessageId === body.clientMessageId)
          const assistant = userIndex >= 0 ? messages.data.slice(userIndex + 1).find((message) => message.role === 'assistant') : undefined
          if (!assistant) throw new Error('chat_long_session_recovered_assistant_missing')
          return { status: 200, firstDeltaMs: null, totalMs: 0, eventCount: 0, terminalEvent: 'message.completed', assistantOutput: assistant.contentText }
        }
        if (decision.action === 'stop' && submission.data.turnId) {
          await apiJson(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/stop`, cookie, { turnId: submission.data.turnId, clientMessageId: body.clientMessageId })
        }
        throw new Error(`chat_long_session_recovery_${decision.action === 'fail' ? decision.reason : 'stopped'}: ${error instanceof Error ? error.name : 'network_error'}`)
      }
    }
  }
  throw new Error('chat_long_session_turn_deadline_exceeded')
}

function applyChatLongSessionArtifactQualityGate(
  turn: Pick<ChatLongSessionTurn, 'responseMode'>,
  streamed: Awaited<ReturnType<typeof postChatStream>>
): Awaited<ReturnType<typeof postChatStream>> {
  const qualityFailure = streamed.terminalEvent === 'message.completed'
    ? chatLongSessionArtifactQualityFailure({ responseMode: turn.responseMode, assistantOutput: streamed.assistantOutput })
    : undefined
  return qualityFailure
    ? { ...streamed, terminalEvent: 'message.failed', errorCode: qualityFailure.code, failure: qualityFailure }
    : streamed
}

async function recoverAcceptedStreamFailure(
  baseUrl: string,
  cookie: string,
  conversationId: string,
  clientMessageId: string,
  streamError: {
    failure: SafeChatStreamFailure
    progress: ChatLongSessionStreamProgressSnapshot
    turnId?: string
    firstDeltaMs: number | null
    totalMs: number
  }
): ReturnType<typeof postChatStream> {
  const recoveryDeadline = Date.now() + 30_000
  let stopRequested = false
  while (Date.now() < recoveryDeadline) {
    const submission = await apiJson<{ data: { state: 'not_found' | 'preparing' | 'accepted'; turnId?: string; assistantStatus?: string } }>(
      baseUrl,
      `/__aisys__/api/my-chat/conversations/${conversationId}/submissions/${clientMessageId}`,
      cookie
    )
    if (submission.data.state === 'accepted' && submission.data.assistantStatus === 'completed') {
      const messages = await apiJson<{ data: Array<{ clientMessageId?: string; role: string; contentText: string }> }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages`, cookie)
      const userIndex = messages.data.findIndex((message) => message.clientMessageId === clientMessageId)
      const assistant = userIndex >= 0 ? messages.data.slice(userIndex + 1).find((message) => message.role === 'assistant') : undefined
      if (!assistant) throw new Error('chat_long_session_recovered_assistant_missing')
      return { status: 200, firstDeltaMs: streamError.firstDeltaMs, totalMs: streamError.totalMs, eventCount: streamError.progress.eventCount, terminalEvent: 'message.completed', assistantOutput: assistant.contentText, progress: streamError.progress }
    }
    const turnId = submission.data.turnId ?? streamError.turnId
    if (submission.data.state === 'accepted' && submission.data.assistantStatus === 'streaming' && turnId && !stopRequested) {
      await apiJson(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/stop`, cookie, { turnId, clientMessageId })
      stopRequested = true
      await sleep(100)
      continue
    }
    if (submission.data.state === 'accepted' && (submission.data.assistantStatus === 'canceled' || submission.data.assistantStatus === 'failed') && turnId) {
      return {
        status: 200,
        firstDeltaMs: streamError.firstDeltaMs,
        totalMs: streamError.totalMs,
        eventCount: streamError.progress.eventCount,
        terminalEvent: submission.data.assistantStatus === 'canceled' ? 'message.canceled' : 'message.failed',
        assistantOutput: '',
        turnId,
        errorCode: streamError.failure.code,
        failure: streamError.failure,
        progress: streamError.progress
      }
    }
    if (submission.data.state === 'not_found') throw new Error(`chat_long_session_stream_${streamError.failure.code}_not_found`)
    await sleep(100)
  }
  throw new Error(`chat_long_session_stream_${streamError.failure.code}_recovery_timeout`)
}

async function runOfflineStreamRecoveryRegression(): Promise<void> {
  const submissions = new Map<string, { turnId: string; assistantStatus: 'streaming' | 'canceled' }>()
  const openStreams = new Set<ServerResponse>()
  let stopCount = 0
  const server = createHttpServer(async (request, response) => {
    try {
      const requestUrl = new URL(request.url ?? '/', 'http://127.0.0.1')
      if (request.method === 'POST' && requestUrl.pathname.endsWith('/stream')) {
        const body = await readOfflineRequestJson(request)
        const clientMessageId = String(body.clientMessageId ?? '')
        const turnId = `turn-${clientMessageId}`
        submissions.set(clientMessageId, { turnId, assistantStatus: 'streaming' })
        response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' })
        response.flushHeaders()
        response.write(`event: message.started\ndata: ${JSON.stringify({ turnId })}\n\n`)
        if (clientMessageId === 'offline-consumption') {
          response.end('event: message.delta\ndata: {bad}\n\n')
        } else {
          openStreams.add(response)
          response.once('close', () => openStreams.delete(response))
        }
        return
      }
      if (request.method === 'GET' && requestUrl.pathname.includes('/submissions/')) {
        const clientMessageId = decodeURIComponent(requestUrl.pathname.slice(requestUrl.pathname.lastIndexOf('/') + 1))
        const submission = submissions.get(clientMessageId)
        sendOfflineJson(response, submission
          ? { data: { state: 'accepted', turnId: submission.turnId, assistantStatus: submission.assistantStatus } }
          : { data: { state: 'not_found' } })
        return
      }
      if (request.method === 'POST' && requestUrl.pathname.endsWith('/stop')) {
        const body = await readOfflineRequestJson(request)
        const clientMessageId = String(body.clientMessageId ?? '')
        const submission = submissions.get(clientMessageId)
        if (submission) submission.assistantStatus = 'canceled'
        stopCount += 1
        sendOfflineJson(response, { data: { stopped: true } })
        return
      }
      sendOfflineJson(response, { code: 'not_found' }, 404)
    } catch {
      sendOfflineJson(response, { code: 'offline_fixture_failed' }, 500)
    }
  })
  const port = await new Promise<number>((resolvePort, rejectPort) => {
    server.once('error', rejectPort)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (!address || typeof address === 'string') { rejectPort(new Error('offline_stream_recovery_port_missing')); return }
      resolvePort(address.port)
    })
  })
  const baseUrl = `http://127.0.0.1:${port}`
  try {
    const oversizedArtifact = applyChatLongSessionArtifactQualityGate({ responseMode: 'artifact' }, {
      status: 200,
      firstDeltaMs: 1,
      totalMs: 2,
      eventCount: 3,
      terminalEvent: 'message.completed',
      assistantOutput: ''.padEnd(chatLongSessionArtifactMaxBytes + 1, 'x')
    })
    assert.equal(oversizedArtifact.terminalEvent, 'message.failed')
    assert.equal(oversizedArtifact.failure?.code, 'chat_long_session_artifact_too_large')
    const oversizedFailedPartial = applyChatLongSessionArtifactQualityGate({ responseMode: 'artifact' }, {
      status: 200,
      firstDeltaMs: 1,
      totalMs: 2,
      eventCount: 3,
      terminalEvent: 'message.failed',
      assistantOutput: ''.padEnd(chatLongSessionArtifactMaxBytes + 1, 'x'),
      failure: { type: 'message.failed', code: 'gateway_stream_connection_error', message: 'connection failed' },
      errorCode: 'gateway_stream_connection_error'
    })
    assert.equal(oversizedFailedPartial.failure?.code, 'gateway_stream_connection_error', 'failed partial output 不得被 artifact quality gate 改写')
    const consumption = await postChatStreamWithRecovery(baseUrl, 'offline-cookie', 'offline-conversation', {
      clientMessageId: 'offline-consumption',
      content: 'offline consumption recovery'
    }, 'offline-consumption-trace', Date.now() + 5_000)
    assert.equal(consumption.terminalEvent, 'message.canceled')
    assert.equal(consumption.turnId, 'turn-offline-consumption')
    assert.equal(consumption.failure?.code, 'chat_long_session_sse_json_invalid')

    submissions.set('offline-deadline', { turnId: 'turn-offline-deadline', assistantStatus: 'streaming' })
    const deadlineProgress = new ChatLongSessionStreamProgress({ startedAt: Date.now(), eventIdleMs: chatStreamEventIdleMs, progressIdleMs: chatStreamProgressIdleMs })
    const deadline = await postChatStreamWithRecovery(baseUrl, 'offline-cookie', 'offline-conversation', {
      clientMessageId: 'offline-deadline',
      content: 'offline deadline recovery'
    }, 'offline-deadline-trace', Date.now() + 5_000, async () => {
      throw new ChatLongSessionStreamDeadlineError({
        reason: 'hard_deadline',
        progress: deadlineProgress.snapshot(Date.now(), 0),
        turnId: 'turn-offline-deadline',
        firstDeltaMs: null,
        totalMs: 1
      })
    })
    assert.equal(deadline.terminalEvent, 'message.canceled')
    assert.equal(deadline.turnId, 'turn-offline-deadline')
    assert.equal(deadline.failure?.code, 'gateway_stream_hard_deadline_timeout')
    assert.equal(stopCount, 2)
    console.log(JSON.stringify({
      ok: true,
      mode: 'offline-stream-recovery',
      consumptionFailureCode: consumption.failure.code,
      deadlineFailureCode: deadline.failure.code,
      artifactFailureCode: oversizedArtifact.failure.code,
      stopCount
    }))
  } finally {
    for (const stream of openStreams) stream.destroy()
    await new Promise<void>((resolveClose, rejectClose) => server.close((error) => error ? rejectClose(error) : resolveClose()))
  }
}

async function readOfflineRequestJson(request: IncomingMessage): Promise<Record<string, unknown>> {
  let bytes = 0
  let text = ''
  for await (const chunk of request) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    bytes += buffer.byteLength
    if (bytes > 64 * 1024) throw new Error('offline_stream_recovery_request_too_large')
    text += buffer.toString('utf8')
  }
  const parsed = JSON.parse(text || '{}') as unknown
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('offline_stream_recovery_request_invalid')
  return parsed as Record<string, unknown>
}

function sendOfflineJson(response: ServerResponse, payload: unknown, status = 200): void {
  response.writeHead(status, { 'content-type': 'application/json' })
  response.end(JSON.stringify(payload))
}

async function waitForPersistedUsage(
  repositories: typeof import('../../storage/repositories.js'),
  access: { systemAccountId: string; role: 'user' },
  traceId: string
): Promise<PersistedUsageMetric> {
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    const result = await repositories.listUsageRecordsAsync(access, { traceId, page: 1, pageSize: 5, result: 'all' })
    const record = result.items.find((item) => item.traceId === traceId && item.trafficSource === 'gateway' && item.success)
    if (record) {
      return {
        status: 'available',
        cacheStatus: record.cacheReadTokens !== undefined || record.cacheWriteTokens !== undefined || record.cacheWrite1hTokens !== undefined ? 'available' : 'unavailable',
        ...numberField('inputTokens', record.inputTokens),
        ...numberField('outputTokens', record.outputTokens),
        ...numberField('cacheReadTokens', record.cacheReadTokens),
        ...numberField('cacheWriteTokens', record.cacheWriteTokens),
        ...numberField('cacheWrite1hTokens', record.cacheWrite1hTokens),
        ...numberField('costUsd', record.costUsd),
        ...numberField('firstTokenMs', record.firstTokenMs),
        ...numberField('durationMs', record.durationMs),
        ...(record.model ? { model: record.model } : {}),
        ...(record.billedServiceTier ? { serviceTier: record.billedServiceTier } : {})
      }
    }
    await sleep(250)
  }
  return { status: 'unavailable', cacheStatus: 'unavailable' }
}

async function refreshUnavailableCheckpointUsage(
  turnMetrics: TurnMetric[],
  repositories: typeof import('../../storage/repositories.js'),
  access: { systemAccountId: string; role: 'user' }
): Promise<boolean> {
  let changed = false
  for (const metric of turnMetrics) {
    if (metric.usage.status === 'available') continue
    if (!metric.traceId) throw new Error(`chat_long_session_checkpoint_usage_trace_missing_${metric.turn}`)
    const usage = await waitForPersistedUsage(repositories, access, metric.traceId)
    if (usage.status !== 'available') throw new Error(`chat_long_session_checkpoint_usage_unavailable_${metric.turn}`)
    metric.usage = usage
    changed = true
  }
  return changed
}

function numberField<K extends string>(key: K, value: number | undefined): Record<K, number> | Record<string, never> {
  return typeof value === 'number' && Number.isFinite(value) ? { [key]: value } as Record<K, number> : {}
}

function chooseControl(requested: 'alternate' | undefined, options: readonly string[], current?: string): { result: 'applied' | 'skipped'; value?: string; reason?: string } {
  if (!requested) return current ? { result: 'applied', value: current } : { result: 'skipped', reason: 'not_requested' }
  const alternate = options.find((option) => option !== current)
  return alternate ? { result: 'applied', value: alternate } : { result: 'skipped', reason: 'capability_unavailable' }
}

function alternateModel(models: readonly ChatModelOption[], current: ChatModelOption): ChatModelOption {
  return models.find((model) => model.id !== current.id) ?? current
}

function smallestContextModel(models: readonly ChatModelOption[]): ChatModelOption {
  return [...models].sort((left, right) => (left.maxInputTokens ?? left.contextWindowTokens ?? Number.MAX_SAFE_INTEGER) - (right.maxInputTokens ?? right.contextWindowTokens ?? Number.MAX_SAFE_INTEGER))[0]
}

async function waitForContextSettled(baseUrl: string, cookie: string, conversationId: string, states: string[]): Promise<ContextStatus> {
  const deadline = Date.now() + 180_000
  let latest: ContextStatus | undefined
  do {
    latest = (await apiJson<{ data: ContextStatus }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/context-status`, cookie)).data
    states.push(latest.state)
    if (latest.state === 'ready') return latest
    await sleep(500)
  } while (Date.now() < deadline)
  assert(latest)
  return latest
}

async function waitForNaturalCompaction(baseUrl: string, cookie: string, conversationId: string, initial: ContextStatus, states: string[]): Promise<{ context: ContextStatus; observedCompactionChange: boolean }> {
  const normalDeadline = Date.now() + 15_000
  const extendedCompactionDeadline = Date.now() + 180_000
  let latest = initial
  let observedCompactionChange = initial.state === 'compact_pending' || initial.state === 'compacting' || initial.compactedThroughSequence > 0
  while (Date.now() < (observedCompactionChange ? extendedCompactionDeadline : normalDeadline)) {
    latest = (await apiJson<{ data: ContextStatus }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/context-status`, cookie)).data
    states.push(latest.state)
    observedCompactionChange ||= latest.state === 'compact_pending' || latest.state === 'compacting' || latest.compactedThroughSequence > initial.compactedThroughSequence
    if (observedCompactionChange && latest.state === 'ready' && latest.compactedThroughSequence > 0) return { context: latest, observedCompactionChange }
    await sleep(500)
  }
  return { context: latest, observedCompactionChange }
}

async function waitForNoActiveNaturalCompaction(baseUrl: string, cookie: string, conversationId: string, states: string[]): Promise<ContextStatus> {
  const deadline = Date.now() + 180_000
  let latest = (await apiJson<{ data: ContextStatus }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/context-status`, cookie)).data
  while ((latest.state === 'compact_pending' || latest.state === 'compacting') && Date.now() < deadline) {
    states.push(latest.state)
    await sleep(500)
    latest = (await apiJson<{ data: ContextStatus }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/context-status`, cookie)).data
  }
  if (latest.state === 'compact_pending' || latest.state === 'compacting') throw new Error('chat_long_session_natural_compaction_still_active')
  return latest
}

function readRequiredCredential(): RealCredential {
  const path = process.env.JUHE_AI_CHAT_REAL_CREDENTIAL_FILE?.trim()
  const envBaseUrl = process.env.JUHE_AI_CHAT_REAL_BASE_URL?.trim()
  const envApiKey = process.env.JUHE_AI_CHAT_REAL_API_KEY?.trim()
  const envModels = process.env.JUHE_AI_CHAT_REAL_MODELS?.split(',').map((model) => model.trim()).filter(Boolean)
  if (path) return parseCredentialText(readFileSync(path, 'utf8'))
  if (envBaseUrl && envApiKey && envModels?.length) return { baseUrl: envBaseUrl.replace(/\/$/, ''), apiKey: envApiKey, models: [...new Set(envModels)] }
  throw new Error('真实长会话验收必须设置 JUHE_AI_CHAT_REAL_CREDENTIAL_FILE，或明确设置 JUHE_AI_CHAT_REAL_BASE_URL/JUHE_AI_CHAT_REAL_API_KEY/JUHE_AI_CHAT_REAL_MODELS')
}

function parseCredentialText(text: string): RealCredential {
  try {
    const json = JSON.parse(text) as Record<string, unknown>
    const baseUrl = String(json.baseURL ?? json.baseUrl ?? '').trim()
    const apiKey = String(json.apiKey ?? json.api_key ?? '').trim()
    const models = Array.isArray(json.models) ? json.models.map(String).map((model) => model.trim()).filter(Boolean) : []
    if (baseUrl && apiKey && models.length) return { baseUrl: baseUrl.replace(/\/$/, ''), apiKey, models: [...new Set(models)] }
  } catch {}
  const lines = text.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  const baseUrl = lines.find((line) => /^https?:\/\//i.test(line))
  const apiKey = lines.find((line) => /^(?:sk-|sess-|key-)/i.test(line))
  const models = [...new Set(lines.flatMap((line) => line.match(/(?:gpt-[\w.-]+|o\d[\w.-]*)/gi) ?? []))]
  if (!baseUrl || !apiKey || !models.length) throw new Error('真实模型凭据文件缺少 baseURL、API key 或支持模型')
  return { baseUrl: baseUrl.replace(/\/$/, ''), apiKey, models }
}

function startBackend(port: number, tempRoot: string, runtimeConfig: RuntimeConfig): ChildProcess {
  return spawn('pnpm', ['--filter', 'juhe-ai-backend', 'exec', 'tsx', 'src/server.ts'], {
    cwd: repoRoot,
    env: {
      ...pickChildProcessBaseEnv(process.env),
      ...buildHermeticJuheEnv(tempRoot, 'server', runtimeConfig.secret),
      NODE_ENV: '',
      JUHE_AI_ENV_FILE: '',
      DATABASE_URL: '',
      POSTGRES_URL: '',
      PGHOST: '',
      PGPORT: '',
      PGDATABASE: '',
      PGUSER: '',
      PGPASSWORD: '',
      REDIS_URL: '',
      REDIS_HOST: '',
      REDIS_PORT: '',
      JUHE_AI_HOST: '127.0.0.1', JUHE_AI_PORT: String(port),
      JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1', JUHE_AI_DB_SERVICE_HTTP_PORT: '0',
      JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath, JUHE_AI_CHAT_DATABASE_PATH: runtimeConfig.chatDatabasePath,
      JUHE_AI_CHAT_ASSETS_ROOT: runtimeConfig.chatAssetsRoot, JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath,
      JUHE_AI_USAGE_CATALOG_DATABASE_PATH: runtimeConfig.usageCatalogDatabasePath, JUHE_AI_STATS_DATABASE_PATH: runtimeConfig.statsDatabasePath,
      JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot, JUHE_AI_CODEX_CONTEXT_ROOT: runtimeConfig.codexContextRoot,
      JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: runtimeConfig.codexContextStateShardRoot, JUHE_AI_SECRET: runtimeConfig.secret,
      JUHE_AI_CHAT_STORAGE_QUOTA_BYTES: String(64 * 1024 * 1024), JUHE_AI_CHAT_RETENTION_DAYS: '3',
      JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER: '50', JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION: '50', JUHE_AI_LOG_DIR: join(tempRoot, 'logs'),
      JUHE_AI_LOG_LEVEL: 'warn', JUHE_AI_LOG_CONSOLE_ENABLED: 'false', JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    shell: process.platform === 'win32',
    stdio: ['ignore', 'pipe', 'pipe']
  })
}

async function apiJson<T = unknown>(baseUrl: string, path: string, cookie: string, body?: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { cookie, ...(body === undefined ? {} : { 'content-type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: activeRunBudget?.signalFor(30_000, `api_json:${path}`) ?? AbortSignal.timeout(30_000)
  })
  const text = await readBoundedText(response, 4 * 1024 * 1024)
  assert(response.ok, `${path} HTTP ${response.status} ${parseErrorCode(text) ?? 'request_failed'}`)
  return JSON.parse(text) as T
}

async function readBoundedText(response: Response, maxBytes: number): Promise<string> {
  if (!response.body) return ''
  const reader = response.body.getReader()
  return consumeReaderWithBoundedCancellation(reader, async () => {
    const decoder = new TextDecoder()
    let total = 0
    let text = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      total += value.byteLength
      if (total > maxBytes) throw new Error('chat_long_session_response_limit_exceeded')
      text += decoder.decode(value, { stream: true })
    }
    return text + decoder.decode()
  })
}

function parseErrorCode(text: string): string | undefined {
  try { const payload = JSON.parse(text) as { code?: unknown }; return typeof payload.code === 'string' ? payload.code : undefined } catch { return undefined }
}

function resolveOutputPath(runHash: string): string {
  const configured = process.env.JUHE_AI_CHAT_REAL_OUTPUT?.trim()
  if (configured) return isAbsolute(configured) ? configured : resolve(repoRoot, configured)
  const timestamp = new Date().toISOString().replace(/[:.]/g, '-').replace('T', '_').replace('Z', '')
  return resolve(repoRoot, 'reports', `chat-long-session-real-${timestamp}-${runHash}.json`)
}

function readChatLongSessionCheckpoint(path: string): ChatLongSessionCheckpoint {
  const size = statSync(path).size
  if (size <= 0 || size > 1024 * 1024) throw new Error('chat_long_session_resume_checkpoint_size_invalid')
  const parsed = JSON.parse(readFileSync(path, 'utf8')) as Partial<ChatLongSessionCheckpoint>
  if (
    parsed.schemaVersion !== 1
    || typeof parsed.fixtureHash !== 'string'
    || typeof parsed.conversationId !== 'string'
    || typeof parsed.accountId !== 'string'
    || typeof parsed.gatewayKeyId !== 'string'
    || !Number.isSafeInteger(parsed.completedTurns)
    || Number(parsed.completedTurns) < 0
    || Number(parsed.completedTurns) > fixture.length
    || typeof parsed.canonicalHash !== 'string'
    || !Array.isArray(parsed.turnMetrics)
  ) throw new Error('chat_long_session_resume_checkpoint_invalid')
  if (parsed.turnMetrics.length !== parsed.completedTurns) throw new Error('chat_long_session_resume_checkpoint_metric_count_invalid')
  assertSafeCheckpointPayload(parsed)
  return parsed as ChatLongSessionCheckpoint
}

function writeChatLongSessionCheckpoint(path: string, checkpoint: ChatLongSessionCheckpoint): void {
  assert.equal(checkpoint.turnMetrics.length, checkpoint.completedTurns)
  assertSafeCheckpointPayload(checkpoint)
  const nextPath = `${path}.${process.pid}.next`
  writeFileSync(nextPath, `${JSON.stringify(checkpoint, null, 2)}\n`, { encoding: 'utf8', flag: 'w' })
  renameSync(nextPath, path)
}

function assertSafeCheckpointPayload(value: unknown): void {
  const serialized = JSON.stringify(value)
  if (/"(?:prompt|assistantOutput|contentText|apiKey|secret|sessionToken)"\s*:/i.test(serialized)) {
    throw new Error('chat_long_session_checkpoint_contains_sensitive_content')
  }
}

function environmentObservation(tempRoot: string, runtimeConfig: RuntimeConfig): Record<string, number> {
  const result: Record<string, number> = {}
  for (const [name, path] of Object.entries({ businessDb: runtimeConfig.databasePath, chatDb: runtimeConfig.chatDatabasePath, chatWal: `${runtimeConfig.chatDatabasePath}-wal`, usageCatalogDb: runtimeConfig.usageCatalogDatabasePath })) {
    try { result[`${name}Bytes`] = statSync(path).size } catch { result[`${name}Bytes`] = 0 }
  }
  result.tempRootHash = Number.parseInt(hashId(tempRoot).slice(0, 12), 16)
  return result
}

function applyHermeticProcessEnv(tempRoot: string, runSecret: string): void {
  Object.assign(process.env, {
    ...buildHermeticJuheEnv(tempRoot, 'db-service', runSecret),
    NODE_ENV: '',
    JUHE_AI_ENV_FILE: '',
    JUHE_AI_RUNTIME_MODE: 'standalone',
    JUHE_AI_DATABASE_DRIVER: 'sqlite',
    JUHE_AI_CACHE_DRIVER: 'memory',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
    JUHE_AI_QUEUE_DRIVER: 'memory',
    JUHE_AI_POSTGRES_URL: '',
    JUHE_AI_REDIS_CACHE_URL: '',
    JUHE_AI_REDIS_STATE_URL: '',
    JUHE_AI_REDIS_QUEUE_URL: '',
    DATABASE_URL: '',
    POSTGRES_URL: '',
    PGHOST: '',
    PGPORT: '',
    PGDATABASE: '',
    PGUSER: '',
    PGPASSWORD: '',
    REDIS_URL: '',
    REDIS_HOST: '',
    REDIS_PORT: '',
    JUHE_AI_CHAT_RETENTION_DAYS: '3',
    JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER: '50',
    JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION: '50',
    JUHE_AI_LOG_DIR: join(tempRoot, 'logs'),
    JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
    JUHE_AI_LOG_FILE_ENABLED: 'false'
  })
}

function buildHermeticJuheEnv(tempRoot: string, processRole: 'server' | 'db-service', runSecret = `chat-long-${hashId(tempRoot)}`): NodeJS.ProcessEnv {
  return {
    NODE_ENV: '', JUHE_AI_ENV_FILE: '', JUHE_AI_DISABLE_BASE_ENV: 'true', JUHE_AI_RUNTIME_MODE: 'standalone', JUHE_AI_PROCESS_ROLE: processRole, JUHE_AI_WORKER_ROLE: 'worker',
    JUHE_AI_DATABASE_DRIVER: 'sqlite', JUHE_AI_CACHE_DRIVER: 'memory', JUHE_AI_RUNTIME_STATE_DRIVER: 'memory', JUHE_AI_QUEUE_DRIVER: 'memory',
    JUHE_AI_POSTGRES_URL: '', JUHE_AI_REDIS_CACHE_URL: '', JUHE_AI_REDIS_STATE_URL: '', JUHE_AI_REDIS_QUEUE_URL: '',
    JUHE_AI_CODEX_WEB_SEARCH_ENDPOINT: '', JUHE_AI_CODEX_WEB_SEARCH_API_KEY: '',
    JUHE_AI_IMAGE_GENERATION_PROVIDER_ENDPOINT: '', JUHE_AI_IMAGE_GENERATION_PROVIDER_API_KEY: '', JUHE_AI_IMAGE_GENERATION_PROVIDER_API: 'images',
    JUHE_AI_OAUTH_PROXY_URL: '', JUHE_AI_BACKEND_URL: '',
    JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENABLED: 'false', JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENDPOINT: '',
    JUHE_AI_HOSTED_TOOL_CODE_INTERPRETER_MODE: 'guidance', JUHE_AI_HOSTED_TOOL_COMPUTER_MODE: 'guidance',
    JUHE_AI_HOSTED_TOOL_SHELL_MODE: 'guidance', JUHE_AI_HOSTED_TOOL_SKILLS_MODE: 'guidance', JUHE_AI_HOSTED_TOOL_TOOL_SEARCH_MODE: 'guidance',
    JUHE_AI_DATABASE_PATH: join(tempRoot, 'business.sqlite3'), JUHE_AI_CHAT_DATABASE_PATH: join(tempRoot, 'chat.sqlite3'),
    JUHE_AI_DATASET_DATABASE_PATH: join(tempRoot, 'dataset.sqlite3'), JUHE_AI_USAGE_CATALOG_DATABASE_PATH: join(tempRoot, 'usage-catalog.sqlite3'),
    JUHE_AI_STATS_DATABASE_PATH: join(tempRoot, 'stats.sqlite3'), JUHE_AI_USAGE_SHARD_ROOT: join(tempRoot, 'usage-shards'),
    JUHE_AI_CODEX_CONTEXT_ROOT: join(tempRoot, 'codex-context'), JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: join(tempRoot, 'codex-context', 'state-shards'),
    JUHE_AI_CHAT_ASSETS_ROOT: join(tempRoot, 'chat-assets'), JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT: join(tempRoot, 'openai-files'),
    JUHE_AI_CODE_INTERPRETER_TEMP_ROOT: join(tempRoot, 'code-interpreter'), JUHE_AI_LOG_DIR: join(tempRoot, 'logs'),
    JUHE_AI_SECRET: runSecret, JUHE_AI_LOG_CONSOLE_ENABLED: 'false', JUHE_AI_LOG_FILE_ENABLED: 'false',
    JUHE_AI_USAGE_RECORD_WRITER_POOL_ENABLED: 'false', JUHE_AI_CODEX_CONTEXT_STATE_WRITER_POOL_ENABLED: 'false', JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE: '1',
    JUHE_AI_CHAT_RETENTION_DAYS: '3', JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER: '50', JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION: '50',
    JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS: '65536'
  }
}

function hashId(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function redactText(text: string, secrets: readonly string[], baseUrl: string): string {
  return redactKnownSecrets(text.replaceAll(baseUrl, '[REDACTED_BASE_URL]'), secrets)
}

function boundedDiagnostics(previous: string, next: string, secrets: readonly string[], baseUrl: string): string {
  return `${previous}${redactText(next, secrets, baseUrl)}`.slice(-32 * 1024)
}

function elapsed(startedAt: number): number {
  return Math.round(performance.now() - startedAt)
}

async function waitForReady(baseUrl: string, cookie: string, child: ChildProcess): Promise<void> {
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`临时后端退出：${child.exitCode}`)
    try { if ((await fetch(`${baseUrl}/__aisys__/api/auth/me`, { headers: { cookie }, signal: activeRunBudget?.signalFor(2_000, 'wait_for_ready') ?? AbortSignal.timeout(2_000) })).ok) return } catch (error) { if (activeRunBudget?.signal.aborted) throw error }
    await sleep(200)
  }
  throw new Error('临时后端等待超时')
}

async function freePort(): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const server = net.createServer()
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (!address || typeof address === 'string') { reject(new Error('无法分配端口')); return }
      server.close((error) => error ? reject(error) : resolvePort(address.port))
    })
  })
}

async function stopProcess(child?: ChildProcess, options: { tracked?: readonly TrackedProcessIdentity[]; servicePort?: number } = {}): Promise<void> {
  if (!child) return
  if (process.platform === 'win32' && child.pid) {
    await stopTrackedWindowsProcessTree({
      rootPid: child.pid,
      tracked: options.tracked ?? [],
      servicePort: options.servicePort,
      captureTree: captureWindowsProcessTree,
      taskkillTree: (rootPid) => new Promise<number>((resolveKill, rejectKill) => {
        const killer = spawn('taskkill', ['/pid', String(rootPid), '/t', '/f'], { stdio: 'ignore', windowsHide: true })
        killer.once('error', rejectKill)
        killer.once('exit', (code) => resolveKill(code ?? -1))
      }),
      listCurrentProcesses: listWindowsProcessIdentities,
      killPid: killWindowsProcess,
      isPortListening: isTcpPortListening
    })
    await waitForChildProcessClose(child, 5_000)
    return
  }
  if (child.exitCode !== null) return
  child.kill('SIGTERM')
  if (await waitForChildExit(child, 5_000)) return
  child.kill('SIGKILL')
  if (!await waitForChildExit(child, 5_000)) throw new Error('chat_long_session_child_stop_failed: sigkill_timeout')
}

async function waitForChildExit(child: ChildProcess, timeoutMs: number): Promise<boolean> {
  if (child.exitCode !== null) return true
  return new Promise<boolean>((resolveExit) => {
    const timeout = setTimeout(() => resolveExit(child.exitCode !== null), timeoutMs)
    child.once('exit', () => { clearTimeout(timeout); resolveExit(true) })
  })
}

async function removeTempRoot(path: string, tracked: readonly TrackedProcessIdentity[]): Promise<void> {
  await retryBusyCleanup(
    async () => { rmSync(path, { recursive: true, force: true }) },
    {
      maxAttempts: 8,
      baseDelayMs: 100,
      maxDelayMs: 2_000,
      sleep: plainSleep,
      onBusy: async ({ attempt, error }) => collectWindowsBusyCleanupDiagnostic({
        attempt,
        targetPath: busyCleanupTargetPath(error, path),
        tracked
      })
    }
  )
}

async function captureRunnerLocalSqliteReadWorkers(): Promise<TrackedProcessIdentity[]> {
  if (process.platform !== 'win32') return []
  const processes = await listWindowsProcessIdentities()
  return selectTrackedProcessTree(process.pid, processes)
    .filter((identity) => identity.pid !== process.pid && classifyProcessCommand(identity.commandLine) === 'sqlite-read-worker')
}

function sleep(ms: number): Promise<void> {
  return activeRunBudget?.sleep(ms) ?? plainSleep(ms)
}

function plainSleep(ms: number): Promise<void> {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms))
}

await main()

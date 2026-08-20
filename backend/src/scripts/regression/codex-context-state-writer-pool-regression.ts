import { strict as assert } from 'node:assert'
import { fork } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import {
  createCodexContextWriterStderrCapture,
  sanitizeCodexContextWriterDiagnostic
} from '../../storage/codex-context-state-writer-diagnostics.js'
import * as databaseModule from '../../storage/database.js'
import * as writerPool from '../../storage/codex-context-state-writer-pool.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-context-writer-pool-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.codexContextStateShardCount = 8
runtimeConfig.codexContextStateWriterPoolEnabled = true
runtimeConfig.codexContextStateWriterPoolSize = 4
runtimeConfig.codexContextStateWriterQueueMaxItems = 2000
runtimeConfig.secret = 'codex-context-writer-pool-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const boundary = {
  systemAccountId: 'sys_writer_pool',
  apiKeyId: 'apikey_writer_pool',
  groupId: 'group_writer_pool',
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}

const futureExpiresAt = '2999-01-01T00:00:00.000Z'
const expiredAt = '2000-01-01T00:00:00.000Z'
const cleanupNow = '2026-06-22T00:00:00.000Z'
const sharedStorageKey = 'sessions/shared/segments/2026062200.json.gz'

try {
  await runRegression()
  console.log('Responses bridge state writer pool 回归通过：精确 worker 数、异常补员、关闭回收、有界脱敏 stderr 和存储语义正常')
} finally {
  const livePidsBeforeClose = writerPool.getCodexContextStateWriterPoolRuntime().workerPids
  await writerPool.closeCodexContextStateWriterPool()
  assert.equal(writerPool.getCodexContextStateWriterPoolRuntime().workerCount, 0, 'close 后 runtime 不应遗留 worker')
  await waitFor(() => livePidsBeforeClose.every((pid) => !processExists(pid)), 'close 后真实 writer 子进程应全部退出')
  databaseModule.closeStorageDatabases()
  await removeTempRoot()
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (attempt === 4) throw error
      await delay(100 * (attempt + 1))
    }
  }
}

async function runRegression(): Promise<void> {
  const tails = await writeConcurrentResponseChains()
  const runtimeAfterWrites = writerPool.getCodexContextStateWriterPoolRuntime()
  assert.equal(runtimeAfterWrites.enabled, true, 'writer pool 应启用')
  assert(runtimeAfterWrites.workerCount > 0, 'writer pool 应按需创建 worker')
  assert(runtimeAfterWrites.handledJobs >= 64, 'writer pool 应执行并发写入任务')
  assert.equal(runtimeAfterWrites.failedJobs, 0, 'writer pool 不应产生失败任务')

  assertShardDistribution()
  await assertResponseChainRestores(tails)
  await assertCompactState()
  await assertCleanupBarrierAndStorageKeyProtection()
  await assertExactWorkerRecovery()
  assertBoundedSanitizedStderr()
  await assertRealWorkerFatalDiagnostic()

  const runtimeAfterCleanup = writerPool.getCodexContextStateWriterPoolRuntime()
  assert.equal(runtimeAfterCleanup.queueLength, 0, 'writer pool 队列应清空')
  assert.equal(runtimeAfterCleanup.activeJobs, 0, 'writer pool 不应遗留活动任务')
}

async function assertExactWorkerRecovery(): Promise<void> {
  const before = writerPool.getCodexContextStateWriterPoolRuntime()
  assert.equal(before.workerCount, 4, '首次请求后应精确创建配置的 4 个 writer')
  assert.equal(before.workerPids.length, 4, 'runtime 应暴露 4 个实时 writer PID')
  assert.equal(new Set(before.workerPids).size, 4, '实时 writer PID 不应重复')
  assert(before.workerPids.every(processExists), 'runtime 中的 writer PID 应真实存活')

  const killedPid = before.workerPids[0]
  assert(killedPid, '应存在可终止的 writer PID')
  process.kill(killedPid, 'SIGTERM')
  await waitFor(() => !writerPool.getCodexContextStateWriterPoolRuntime().workerPids.includes(killedPid), '被终止的 writer 应从 runtime 移除')

  await writerPool.requestCodexContextStateWriter({
    type: 'read_response_row',
    responseId: 'pool_resp_0_0'
  })
  const after = writerPool.getCodexContextStateWriterPoolRuntime()
  assert.equal(after.workerCount, 4, '下一次请求应只把 pool 补回目标 4 个 worker')
  assert.equal(after.workerPids.length, 4, '补员后实时 PID 数不能超过目标值')
  assert.equal(new Set(after.workerPids).size, 4, '补员后 PID 不应重复')
  assert.equal(after.workerPids.includes(killedPid), false, '补员后不能继续报告已退出 PID')
  assert(after.workerPids.every(processExists), '补员后的 writer PID 应真实存活')
}

function assertBoundedSanitizedStderr(): void {
  const secret = 'writer-secret-value-123456789'
  const bearer = 'bearer-secret-value-123456789'
  const postgresPassword = 'postgres-password-123456789'
  const redisToken = 'redis-token-123456789'
  const queryToken = 'query-token-123456789'
  const querySignature = 'query-signature-123456789'
  const queryCredential = 'query-credential-123456789'
  const awsCredential = 'aws-credential-123456789'
  const basicCredential = 'dXNlcjpiYXNpYy1zZWNyZXQtMTIzNDU2Nzg5'
  const proxyBasicCredential = 'cHJveHk6c2VjcmV0LTEyMzQ1Njc4OQ=='
  const tokenCredential = 'opaque-token-secret-123456789'
  const digestCredential = 'digest-secret-response-123456789'
  const apiKeyCredential = 'authorization-api-key-123456789'
  const capture = createCodexContextWriterStderrCapture({
    maxBytes: 8 * 1024,
    secrets: [secret]
  })
  capture.append(Buffer.from([
    `fatal api_key=${secret} Authorization: Bearer ${bearer}`,
    `postgresql://db_user:${postgresPassword}@db.internal:5432/app?sslmode=require&access_token=${queryToken}&application_name=juhe`,
    `redis://${redisToken}@redis.internal:6379/0?AUTH=${queryToken}&db=0`,
    `https://api.internal/v1/models?signature=${querySignature}&credential=${queryCredential}&normal=visible`,
    `https://s3.internal/object?X-Amz-Credential=${awsCredential}&X-Amz-Signature=${querySignature}&normal=visible`,
    `Authorization: Basic ${basicCredential}; status=kept`,
    `Proxy-Authorization=Basic ${proxyBasicCredential}; proxy_status=kept`,
    `authorization: Token ${tokenCredential}; token_status=kept`,
    `AUTHORIZATION: Digest username="user", response="${digestCredential}"; digest_status=kept`,
    `proxy_authorization: API-Key ${apiKeyCredential}; api_key_status=kept`
  ].join('\n')))
  const snapshot = capture.snapshot()
  assert.equal(snapshot.truncated, false, '未超过上限的 stderr 不应标记截断')
  assert(Buffer.byteLength(snapshot.summary, 'utf8') <= 8 * 1024, '脱敏摘要也不能超过 8KiB')
  assert.equal(snapshot.summary.includes(secret), false, 'stderr 摘要不能泄露显式 secret')
  assert.equal(snapshot.summary.includes(bearer), false, 'stderr 摘要不能泄露 Bearer token')
  assert.equal(snapshot.summary.includes(postgresPassword), false, 'stderr 摘要不能泄露 PostgreSQL userinfo 密码')
  assert.equal(snapshot.summary.includes(redisToken), false, 'stderr 摘要不能泄露 Redis 无密码 userinfo token')
  assert.equal(snapshot.summary.includes(queryToken), false, 'stderr 摘要不能泄露 query token/auth')
  assert.equal(snapshot.summary.includes(querySignature), false, 'stderr 摘要不能泄露 query signature')
  assert.equal(snapshot.summary.includes(queryCredential), false, 'stderr 摘要不能泄露 query credential')
  assert.equal(snapshot.summary.includes(awsCredential), false, 'stderr 摘要不能泄露 AWS credential')
  assert.equal(snapshot.summary.includes(basicCredential), false, 'stderr 摘要不能泄露 Basic Authorization 凭据')
  assert.equal(snapshot.summary.includes(proxyBasicCredential), false, 'stderr 摘要不能泄露 Proxy-Authorization 凭据')
  assert.equal(snapshot.summary.includes(tokenCredential), false, 'stderr 摘要不能泄露 Token Authorization 凭据')
  assert.equal(snapshot.summary.includes(digestCredential), false, 'stderr 摘要不能泄露 Digest Authorization 凭据')
  assert.equal(snapshot.summary.includes(apiKeyCredential), false, 'stderr 摘要不能泄露 API-Key Authorization 凭据')
  assert(snapshot.summary.includes('postgresql://[REDACTED]@db.internal:5432/app?sslmode=require&access_token=[REDACTED]&application_name=juhe'), 'PostgreSQL URI 应保留 scheme、host、path 和非敏感 query')
  assert(snapshot.summary.includes('redis://[REDACTED]@redis.internal:6379/0?AUTH=[REDACTED]&db=0'), 'Redis URI 应保留 scheme、host、path 和普通 query')
  assert(snapshot.summary.includes('https://api.internal/v1/models?signature=[REDACTED]&credential=[REDACTED]&normal=visible'), 'HTTPS credential query 脱敏不能破坏 host、path 和普通参数')
  assert(snapshot.summary.includes('https://s3.internal/object?X-Amz-Credential=[REDACTED]&X-Amz-Signature=[REDACTED]&normal=visible'), 'AWS Credential/Signature 脱敏应保留 host、path 和普通参数')
  assert(snapshot.summary.includes('Authorization: [REDACTED]; status=kept'), 'Authorization 脱敏应保留分号后的诊断字段')
  assert(snapshot.summary.includes('Proxy-Authorization=[REDACTED]; proxy_status=kept'), 'Proxy-Authorization 脱敏应保留分号后的诊断字段')
  assert(snapshot.summary.includes('authorization: [REDACTED]; token_status=kept'), 'Token Authorization 脱敏应保留分号后的诊断字段')
  assert(snapshot.summary.includes('AUTHORIZATION: [REDACTED]; digest_status=kept'), 'Digest Authorization 脱敏应覆盖逗号参数并保留后续字段')
  assert(snapshot.summary.includes('proxy_authorization: [REDACTED]; api_key_status=kept'), 'API-Key Proxy Authorization 脱敏应兼容大小写和下划线')
  assert(snapshot.summary.includes('[REDACTED]'), 'stderr 摘要应保留脱敏占位符')

  const jsonSecret = 'json-basic-secret-123456789'
  const sanitizedJson = sanitizeCodexContextWriterDiagnostic(JSON.stringify({
    authorization: `Basic ${jsonSecret}`,
    status: 'failed'
  }))
  assert.equal(sanitizedJson.includes(jsonSecret), false, 'JSON quoted Authorization 不能泄露凭据')
  assert.deepEqual(JSON.parse(sanitizedJson), {
    authorization: '[REDACTED]',
    status: 'failed'
  }, 'JSON quoted Authorization 脱敏后仍应保持合法 JSON 和相邻字段')

  const boundaryCapture = createCodexContextWriterStderrCapture({
    maxBytes: 8 * 1024,
    secrets: [secret]
  })
  boundaryCapture.append(Buffer.from(`${'x'.repeat(8 * 1024 - 8)}${secret}`))
  const boundary = boundaryCapture.snapshot()
  assert.equal(boundary.capturedBytes, 8 * 1024, 'stderr 原始捕获总量必须限制为 8KiB')
  assert.equal(boundary.truncated, true, '完整 secret 跨越捕获边界时应标记截断')
  assert(Buffer.byteLength(boundary.summary, 'utf8') <= 8 * 1024, '截断后的安全摘要不能超过 8KiB')
  assert.equal(boundary.summary.includes(secret.slice(0, 8)), false, '截断边界不能泄露 secret 前缀')
  assert(boundary.summary.includes('stderr') && boundary.summary.includes('truncated'), '跨边界时应返回固定安全摘要')
}

async function assertRealWorkerFatalDiagnostic(): Promise<void> {
  const secret = 'fatal-worker-secret-123456789'
  const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), 'codex-context-state-writer-fatal-fixture.ts')
  const child = fork(fixturePath, [], {
    execArgv: process.execArgv,
    env: {
      ...process.env,
      JUHE_AI_PROCESS_ROLE: 'db-service',
      JUHE_AI_SECRET: secret,
      JUHE_AI_TEST_CODEX_WRITER_FATAL_SECRET: secret
    },
    stdio: ['ignore', 'ignore', 'pipe', 'ipc']
  })
  const chunks: Buffer[] = []
  child.stderr?.on('data', (chunk: Buffer) => chunks.push(chunk))
  const exit = await new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolveExit, reject) => {
    child.once('error', reject)
    child.once('close', (code, signal) => resolveExit({ code, signal }))
  })
  const stderr = Buffer.concat(chunks)
  const text = stderr.toString('utf8')
  assert.equal(exit.code, 1, 'worker 未处理异常应以退出码 1 结束')
  assert.equal(exit.signal, null, 'worker fatal 退出不应依赖强杀信号')
  assert(stderr.length <= 8 * 1024, 'worker fatal stderr 必须保持在 8KiB 内')
  assert(text.includes('codex_context_state_writer_fatal'), 'worker fatal stderr 应包含稳定事件名')
  assert.equal(text.includes(secret), false, 'worker fatal stderr 不能泄露运行时 secret')
  assert(text.includes('[REDACTED]'), 'worker fatal stderr 应保留脱敏占位符')
  const parsed = JSON.parse(text.trim()) as { event?: unknown; summary?: unknown }
  assert.equal(parsed.event, 'codex_context_state_writer_fatal', '有界 fatal stderr 仍应是合法 JSON')
  assert.equal(typeof parsed.summary, 'string', '有界 fatal stderr 应保留字符串摘要')
}

async function waitFor(predicate: () => boolean, message: string, timeoutMs = 5000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) return
    await delay(25)
  }
  assert.fail(message)
}

function processExists(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

async function writeConcurrentResponseChains(): Promise<string[]> {
  const tails = await Promise.all(Array.from({ length: 16 }, async (_, sessionIndex) => {
    const sessionId = `pool_session_${sessionIndex}`
    let previousResponseId: string | undefined
    for (let responseIndex = 0; responseIndex < 4; responseIndex += 1) {
      const responseId = `pool_resp_${sessionIndex}_${responseIndex}`
      await saveResponse(responseId, sessionId, previousResponseId, futureExpiresAt, responseIndex)
      previousResponseId = responseId
    }
    return previousResponseId
  }))
  return tails.filter((value): value is string => Boolean(value))
}

async function assertResponseChainRestores(tails: string[]): Promise<void> {
  await Promise.all(tails.map(async (responseId) => {
    const result = await writerPool.readCodexContextResponseStateChainWithWriterPool({
      responseId,
      boundary,
      maxDepth: 8,
      now: cleanupNow,
      refreshExpiresAt: futureExpiresAt
    })
    assert.equal(result.outcome, 'found', `response chain ${responseId} 应可恢复`)
    if (result.outcome === 'found') {
      assert.equal(result.responses.length, 4, '每条 response chain 应保留 4 轮')
    }
  }))

  const mismatch = await writerPool.readCodexContextResponseStateChainWithWriterPool({
    responseId: tails[0] ?? '',
    boundary: { ...boundary, groupId: 'wrong_group' },
    maxDepth: 8,
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(mismatch.outcome, 'boundary_mismatch', '跨 group 的 previous_response_id 必须被拒绝')
}

async function assertCompactState(): Promise<void> {
  const compact = await writerPool.saveCodexContextCompactStateIndexWithWriterPool({
    compactId: 'cmp_writer_pool',
    sessionId: 'pool_session_0',
    sourceResponseId: 'pool_resp_0_3',
    summaryDigest: digestLike('c', 0),
    ...boundary,
    storageKey: 'sessions/compact/segments/2026062200.json.gz',
    storageOffsetBytes: 0,
    sha256: digestLike('d', 0),
    rawSizeBytes: 100,
    compressedSizeBytes: 80,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt: futureExpiresAt
  })
  assert.equal(compact.compactId, 'cmp_writer_pool')

  const found = await writerPool.readCodexContextCompactStateWithWriterPool({
    compactId: 'cmp_writer_pool',
    boundary,
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(found.outcome, 'found', 'compact state 应可读取')

  const mismatch = await writerPool.readCodexContextCompactStateWithWriterPool({
    compactId: 'cmp_writer_pool',
    boundary: { ...boundary, providerCode: 'glm' },
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(mismatch.outcome, 'boundary_mismatch', '跨供应商 compact state 必须被拒绝')
}

async function assertCleanupBarrierAndStorageKeyProtection(): Promise<void> {
  await saveResponse('active_shared_resp', 'active_shared_session', undefined, futureExpiresAt, 0, sharedStorageKey)

  for (let sessionIndex = 0; sessionIndex < 4; sessionIndex += 1) {
    let previousResponseId: string | undefined
    for (let responseIndex = 0; responseIndex < 3; responseIndex += 1) {
      const responseId = `expired_pool_resp_${sessionIndex}_${responseIndex}`
      await saveResponse(
        responseId,
        `expired_pool_session_${sessionIndex}`,
        previousResponseId,
        expiredAt,
        responseIndex,
        sessionIndex === 0 && responseIndex === 0 ? sharedStorageKey : undefined
      )
      previousResponseId = responseId
    }
  }

  const inFlightWrites = Promise.all(Array.from({ length: 12 }, async (_, index) => {
    await saveResponse(`barrier_future_resp_${index}`, `barrier_future_session_${index}`, undefined, futureExpiresAt, index)
  }))
  const cleanup = writerPool.cleanupExpiredCodexContextStatesWithWriterPool({
    expiredBefore: cleanupNow,
    limit: 1000
  })
  const [firstCleanupResult] = await Promise.all([cleanup, inFlightWrites])
  const cleanupResults = [firstCleanupResult]
  for (let index = 1; index < databaseModule.codexContextStateShardCount(); index += 1) {
    cleanupResults.push(await writerPool.cleanupExpiredCodexContextStatesWithWriterPool({
      expiredBefore: cleanupNow,
      limit: 1000
    }))
  }
  const cleanupResult = cleanupResults.reduce((total, result) => ({
    deletedSessions: total.deletedSessions + result.deletedSessions,
    deletedResponses: total.deletedResponses + result.deletedResponses,
    deletedCompacts: total.deletedCompacts + result.deletedCompacts,
    storageKeys: [...new Set([...total.storageKeys, ...result.storageKeys])],
    hasMore: total.hasMore || result.hasMore
  }), {
    deletedSessions: 0,
    deletedResponses: 0,
    deletedCompacts: 0,
    storageKeys: [] as string[],
    hasMore: false
  })

  assert.equal(cleanupResult.deletedResponses, 12, 'cleanup 应只删除过期 response')
  assert.equal(cleanupResult.deletedSessions, 4, 'cleanup 应只删除过期 session')
  assert.equal(cleanupResult.storageKeys.includes(sharedStorageKey), false, '仍被活跃 response 引用的 storageKey 不能返回删除')

  const settlement = await writerPool.settleCodexContextStorageCleanupWithWriterPool({
    succeededStorageKeys: cleanupResult.storageKeys,
    failures: [],
    now: cleanupNow
  })
  assert(settlement.acknowledged >= cleanupResult.storageKeys.length, 'writer pool 应确认已删除的 storage key 队列项')
  assert.equal(settlement.deferred, 0, '无失败文件时不应延后清理队列项')

  const activeShared = await writerPool.readCodexContextResponseStateChainWithWriterPool({
    responseId: 'active_shared_resp',
    boundary,
    maxDepth: 2,
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(activeShared.outcome, 'found', 'cleanup 不能误删活跃 shared storage response')

  const deletedExpired = await writerPool.readCodexContextResponseStateChainWithWriterPool({
    responseId: 'expired_pool_resp_0_2',
    boundary,
    maxDepth: 4,
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(deletedExpired.outcome, 'not_found', '过期 response 清理后应不可恢复')
}

function assertShardDistribution(): void {
  const counts = databaseModule.codexContextStateShardIndexes().map((shardIndex) => {
    const database = databaseModule.getCodexContextStateShardDatabase(shardIndex)
    const row = database.prepare('SELECT COUNT(*) AS count FROM codex_context_responses').get() as { count?: number | bigint } | undefined
    return Number(row?.count ?? 0)
  })
  assert(counts.reduce((sum, count) => sum + count, 0) >= 64, 'response rows 应写入 shard')
  assert(counts.filter((count) => count > 0).length > 1, 'response rows 应分散到多个 shard')
}

async function saveResponse(
  responseId: string,
  sessionId: string,
  previousResponseId: string | undefined,
  expiresAt: string,
  index: number,
  storageKey = `sessions/${sessionId}/segments/2026062200.json.gz`
): Promise<void> {
  await writerPool.saveCodexContextResponseStateIndexWithWriterPool({
    responseId,
    sessionId,
    previousResponseId,
    ...boundary,
    upstreamAccountId: 'acct_writer_pool',
    model: 'deepseek-v4-flash',
    upstreamModel: 'deepseek-v4-flash',
    storageKey,
    storageOffsetBytes: index * 128,
    sha256: digestLike('a', index + responseId.length),
    rawSizeBytes: 120 + index,
    compressedSizeBytes: 90 + index,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt
  })
}

function digestLike(seed: string, index: number): string {
  return `${seed}${index.toString(16).padStart(8, '0')}${seed.repeat(64)}`.slice(0, 64)
}

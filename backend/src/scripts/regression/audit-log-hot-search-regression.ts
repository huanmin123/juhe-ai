import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-audit-log-hot-search-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'audit-log-hot-search-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, hotSearchFiles, hotRetentionCleanup] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/audit-log-hot-search-files.js'),
  import('../../modules/background/audit-hot-retention-cleanup.service.js')
])

const hourMs = 60 * 60 * 1000
const nowMs = Date.now()
const uniqueKeyword = `hot-search-regression-${nowMs}`
const trimKeyword = `hot-search exact phrase ${nowMs}`
const trimKeywordInput = ` ${trimKeyword} `
const crossChunkFirstKeyword = `hot-search-cross-alpha-${nowMs}`
const crossChunkSecondKeyword = `hot-search-cross-beta-${nowMs}`
const newerTraceId = `trace-hot-search-newer-${nowMs}`
const olderTraceId = `trace-hot-search-older-${nowMs}`
const exactPhraseTraceId = `trace-hot-search-exact-phrase-${nowMs}`
const crossChunkTraceId = `trace-hot-search-cross-chunk-${nowMs}`
const unsampledTraceId = `trace-hot-trim-unsampled-${nowMs}`
const sampledTraceId = `trace-hot-trim-sampled-${nowMs}`
const failedTraceId = `trace-hot-trim-failed-${nowMs}`

try {
  assertAuditHotCleanupIsScheduledEveryMinute()

  await repositories.createAuditLogsBatchAsync([
    successAuditLog({
      id: 'audit_hot_search_older',
      traceId: olderTraceId,
      createdAt: new Date(nowMs - 60_000).toISOString(),
      sampleBucket: 9000,
      sampleReason: 'success_hot_full_retention',
      body: JSON.stringify({ keyword: uniqueKeyword, order: 'older', content: '最近1小时 rg 搜索应能命中正文内容' })
    }),
    successAuditLog({
      id: 'audit_hot_search_newer',
      traceId: newerTraceId,
      createdAt: new Date(nowMs - 5_000).toISOString(),
      sampleBucket: 9000,
      sampleReason: 'success_hot_full_retention',
      body: JSON.stringify({ keyword: uniqueKeyword, order: 'newer', content: '最近1小时 rg 搜索应按最新审计优先返回' })
    }),
    successAuditLog({
      id: 'audit_hot_search_exact_phrase',
      traceId: exactPhraseTraceId,
      createdAt: new Date(nowMs - 4_500).toISOString(),
      sampleBucket: 9000,
      sampleReason: 'success_hot_full_retention',
      body: JSON.stringify({ keyword: trimKeyword, content: '搜索输入只清理前后空格再匹配' })
    }),
    successAuditLog({
      id: 'audit_hot_search_cross_chunk',
      traceId: crossChunkTraceId,
      createdAt: new Date(nowMs - 4_000).toISOString(),
      sampleBucket: 9000,
      sampleReason: 'success_hot_full_retention',
      body: JSON.stringify({
        keyword: crossChunkFirstKeyword,
        content: `${'x'.repeat(13_000)} ${crossChunkSecondKeyword}`
      })
    })
  ])

  const searchResult = await hotSearchFiles.grepAuditHotSearchFiles({
    keywords: [uniqueKeyword],
    limit: 10
  })
  assert.equal(searchResult.available, true, searchResult.message ?? 'rg 热搜索应可用')
  assert.deepEqual(searchResult.auditLogIds.slice(0, 2), ['audit_hot_search_newer', 'audit_hot_search_older'], 'rg 热搜索应按审计创建时间倒序返回 ID')

  const searchRows = repositories.listAuditLogsByIds(searchResult.auditLogIds)
  assert.deepEqual(searchRows.slice(0, 2).map((item) => item.id), ['audit_hot_search_newer', 'audit_hot_search_older'], '热搜索命中 ID 回表后应保持 rg 结果顺序')

  const exactPhraseResult = await hotSearchFiles.grepAuditHotSearchFiles({
    keywords: [trimKeywordInput],
    limit: 10
  })
  assert.deepEqual(exactPhraseResult.keywords, [trimKeyword], '热搜索应只清理输入前后的空格')
  assert.deepEqual(exactPhraseResult.auditLogIds, ['audit_hot_search_exact_phrase'], '热搜索应保留内部空格并按完整关键字匹配')

  const nonSplitResult = await hotSearchFiles.grepAuditHotSearchFiles({
    keywords: [`${crossChunkFirstKeyword} ${crossChunkSecondKeyword}`],
    limit: 10
  })
  assert.deepEqual(nonSplitResult.auditLogIds, [], '热搜索不能把空格分隔的输入拆成多个关键词后误命中')

  const oldCreatedAt = new Date(nowMs - 3 * hourMs).toISOString()
  await repositories.createAuditLogsBatchAsync([
    successAuditLog({
      id: 'audit_hot_trim_unsampled',
      traceId: unsampledTraceId,
      createdAt: oldCreatedAt,
      sampleBucket: 9000,
      sampleReason: 'success_hot_full_retention',
      body: JSON.stringify({ keyword: `${uniqueKeyword}-trim`, retention: 'unsampled' })
    }),
    successAuditLog({
      id: 'audit_hot_trim_sampled',
      traceId: sampledTraceId,
      createdAt: oldCreatedAt,
      sampleBucket: 999,
      sampleReason: 'success_sample_0.1',
      body: JSON.stringify({ keyword: `${uniqueKeyword}-sampled`, retention: 'sampled' })
    }),
    failedAuditLog({
      id: 'audit_hot_trim_failed',
      traceId: failedTraceId,
      createdAt: oldCreatedAt,
      body: JSON.stringify({ keyword: `${uniqueKeyword}-failed`, retention: 'failed' })
    })
  ])

  const cleanupResult = await hotRetentionCleanup.cleanupExpiredAuditHotRetentionData(nowMs)
  assert.equal(cleanupResult.auditLogs, 1, '热窗口清理应只删除超过 1 小时且未命中 10% 采样的普通成功审计')
  assert(cleanupResult.auditPayloadBlobs >= 1, '热窗口清理应同步清理已无引用的 payload blob')
  assert(cleanupResult.auditHotSearchFiles >= 1, '热窗口清理应删除已完全超过热窗口的旧热搜索镜像文件')
  assert.equal(repositories.listAuditLogs({ traceId: unsampledTraceId }).total, 0, '未采样普通成功审计超过热窗口后应被删除')
  assert.equal(repositories.listAuditLogs({ traceId: sampledTraceId }).total, 1, '命中 10% 稳定采样的成功审计应继续保留')
  assert.equal(repositories.listAuditLogs({ traceId: failedTraceId }).total, 1, '失败审计不应被成功热窗口清理删除')

  console.log('审计热搜索与热保留清理回归通过：rg 可搜索最近 1 小时内容，超过热窗口后只删除未采样普通成功审计')
} finally {
  try {
    cleanupTemporaryAuditBlobs()
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function successAuditLog(input: {
  id: string
  traceId: string
  createdAt: string
  sampleBucket: number
  sampleReason: string
  body: string
}): AuditLogInput {
  return {
    id: input.id,
    traceId: input.traceId,
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    providerCode: 'gpt',
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.4-mini',
    auditOutcome: 'success',
    success: true,
    finalStatusCode: 200,
    sampleBucket: input.sampleBucket,
    sampleReason: input.sampleReason,
    captureStatus: 'complete',
    startedAt: input.createdAt,
    endedAt: input.createdAt,
    durationMs: 120,
    attempts: [],
    payloads: [
      {
        id: `${input.id}_request`,
        partType: 'client_request',
        sequenceIndex: 0,
        contentType: 'application/json',
        headers: { 'content-type': 'application/json' },
        body: input.body,
        createdAt: input.createdAt
      }
    ],
    createdAt: input.createdAt
  }
}

function failedAuditLog(input: {
  id: string
  traceId: string
  createdAt: string
  body: string
}): AuditLogInput {
  return {
    id: input.id,
    traceId: input.traceId,
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    providerCode: 'gpt',
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.4-mini',
    auditOutcome: 'upstream_failed',
    success: false,
    finalStatusCode: 429,
    errorPhase: 'upstream',
    errorCode: 'rate_limit_exceeded',
    errorMessage: 'Rate limit exceeded',
    sampleBucket: 9000,
    sampleReason: 'full_capture',
    captureStatus: 'complete',
    startedAt: input.createdAt,
    endedAt: input.createdAt,
    durationMs: 120,
    attempts: [],
    payloads: [
      {
        id: `${input.id}_error`,
        partType: 'gateway_error',
        sequenceIndex: 0,
        contentType: 'application/json',
        headers: { 'content-type': 'application/json' },
        body: input.body,
        createdAt: input.createdAt
      }
    ],
    createdAt: input.createdAt
  }
}

function cleanupTemporaryAuditBlobs(): void {
  try {
    const rows = databaseModule.getDatasetDatabase()
      .prepare('SELECT storage_key FROM audit_payload_blobs')
      .all() as Array<{ storage_key?: string }>
    for (const row of rows) {
      if (!row.storage_key) continue
      rmSync(resolve(backendRoot, 'data', 'audit', 'blobs', row.storage_key), { force: true })
    }
  } catch {
  }
}

function assertAuditHotCleanupIsScheduledEveryMinute(): void {
  const source = readFileSync(new URL('../../modules/background/background-jobs.ts', import.meta.url), 'utf8')
  assert(source.includes("name: 'audit-hot-retention-cleanup'"), '后台 worker 必须注册审计热窗口清理任务')
  assert(source.includes("name: 'audit-hot-retention-cleanup', intervalMs: minuteMs"), '审计热窗口清理任务必须按分钟级频率执行，不能只挂在 daily 保留清理里')
}

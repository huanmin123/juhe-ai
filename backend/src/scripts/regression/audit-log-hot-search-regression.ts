import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
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

  const legacyHotSearchBucket = new Date(Math.floor((nowMs - 2 * hourMs) / hourMs) * hourMs)
  const legacyHotSearchPath = join(
    tempRoot,
    'audit',
    'search-hot',
    `audit-hot-${legacyHotSearchBucket.toISOString().slice(0, 13).replace(/[-:T]/g, '')}.ndjson`
  )
  mkdirSync(join(tempRoot, 'audit', 'search-hot'), { recursive: true })
  writeFileSync(legacyHotSearchPath, `${JSON.stringify({
    auditLogId: 'legacy-hot-search-non-persisted',
    createdAt: legacyHotSearchBucket.toISOString(),
    traceId: 'legacy-hot-search-trace',
    text: 'legacy-hot-search-trace account_health_check POST /health'
  })}\n`, 'utf8')
  assert.equal(
    await hotSearchFiles.cleanupNonPersistedAuditHotSearchEntries({ maxFiles: 10, maxRunMs: 1000 }),
    1,
    '旧格式热搜索行应被来源清理任务处理'
  )
  assert.equal(readFileSync(legacyHotSearchPath, 'utf8'), '', '旧格式后台来源热搜索行应被移除')

  const pendingRecoveryBucket = new Date(Math.floor((nowMs - 4 * hourMs) / hourMs) * hourMs)
  const pendingRecoveryPath = join(
    tempRoot,
    'audit',
    'search-hot',
    `audit-hot-${pendingRecoveryBucket.toISOString().slice(0, 13).replace(/[-:T]/g, '')}.ndjson`
  )
  const pendingRecoveryPendingPath = `${pendingRecoveryPath}.pending-crashed-process`
  writeFileSync(pendingRecoveryPath, '', 'utf8')
  writeFileSync(pendingRecoveryPendingPath, `${JSON.stringify({
    auditLogId: 'recovered-after-process-crash',
    createdAt: pendingRecoveryBucket.toISOString(),
    trafficSource: 'gateway',
    text: 'recovered pending audit entry'
  })}\n`, 'utf8')
  await hotSearchFiles.cleanupNonPersistedAuditHotSearchEntries({ maxFiles: 10, maxRunMs: 1000 })
  assert.match(readFileSync(pendingRecoveryPath, 'utf8'), /recovered-after-process-crash/, '进程重启后 pending 热搜索行应恢复到主桶')
  assert.throws(() => readFileSync(pendingRecoveryPendingPath, 'utf8'), /ENOENT/, '已恢复的 pending 文件不应继续残留')

  const raceBucket = new Date(Math.floor((nowMs - 3 * hourMs) / hourMs) * hourMs)
  const racePath = join(
    tempRoot,
    'audit',
    'search-hot',
    `audit-hot-${raceBucket.toISOString().slice(0, 13).replace(/[-:T]/g, '')}.ndjson`
  )
  const racePrefix = Array.from({ length: 20_000 }, (_, index) => JSON.stringify({
    auditLogId: `race-existing-${index}`,
    createdAt: raceBucket.toISOString(),
    trafficSource: 'account_health_check',
    text: 'late cleanup race fixture'
  })).join('\n') + '\n'
  writeFileSync(racePath, racePrefix, 'utf8')
  const cleanupPromise = hotSearchFiles.cleanupNonPersistedAuditHotSearchEntries({ maxFiles: 10, maxRunMs: 1000 })
  await new Promise<void>((resolve) => setImmediate(resolve))
  hotSearchFiles.appendAuditHotSearchEntries([successAuditLog({
    id: 'audit_hot_search_late_append',
    traceId: `trace-hot-search-late-${nowMs}`,
    createdAt: raceBucket.toISOString(),
    sampleBucket: 999,
    sampleReason: 'success_sample_0.1',
    body: JSON.stringify({ keyword: 'late append must survive cleanup race' })
  })])
  await cleanupPromise
  assert.match(readFileSync(racePath, 'utf8'), /audit_hot_search_late_append/, '清理重写期间迟到追加不得被 rename 丢失')

  const scanBucketA = new Date(Math.floor((nowMs - 5 * hourMs) / hourMs) * hourMs)
  const scanBucketB = new Date(Math.floor((nowMs - 6 * hourMs) / hourMs) * hourMs)
  for (const bucket of [scanBucketA, scanBucketB]) {
    const path = join(tempRoot, 'audit', 'search-hot', `audit-hot-${bucket.toISOString().slice(0, 13).replace(/[-:T]/g, '')}.ndjson`)
    writeFileSync(path, `${JSON.stringify({ auditLogId: `scan-${bucket.getUTCHours()}`, createdAt: bucket.toISOString(), trafficSource: 'cooldown_retest', text: 'scan budget fixture' })}\n`, 'utf8')
  }
  const boundedCleanupCount = await hotSearchFiles.cleanupNonPersistedAuditHotSearchEntries({ maxFiles: 1, maxRunMs: 1000 })
  assert.equal(boundedCleanupCount, 1, '来源清理 maxFiles 应限制扫描文件数并最多处理一个文件')
  const remainingScanFiles = [scanBucketA, scanBucketB].filter((bucket) => {
    const path = join(tempRoot, 'audit', 'search-hot', `audit-hot-${bucket.toISOString().slice(0, 13).replace(/[-:T]/g, '')}.ndjson`)
    return readFileSync(path, 'utf8').trim().length > 0
  })
  assert.equal(remainingScanFiles.length, 1, '扫描预算耗尽后应留下未扫描文件供下一轮处理')

  const sourceBucket = new Date(Math.floor((nowMs - 7 * hourMs) / hourMs) * hourMs)
  const sourcePath = join(tempRoot, 'audit', 'search-hot', `audit-hot-${sourceBucket.toISOString().slice(0, 13).replace(/[-:T]/g, '')}.ndjson`)
  const sourceRows = ['gateway', 'manual_account_test', 'hybrid_scoring', 'hybrid_quality_scoring', 'account_health_check', 'runtime_recovery_probe', 'cooldown_retest']
    .map((trafficSource) => JSON.stringify({ auditLogId: `source-${trafficSource}`, createdAt: sourceBucket.toISOString(), trafficSource, text: trafficSource }))
    .join('\n') + '\n'
  writeFileSync(sourcePath, sourceRows, 'utf8')
  await hotSearchFiles.cleanupNonPersistedAuditHotSearchEntries({ maxFiles: 100, maxRunMs: 1000 })
  const retainedSources = readFileSync(sourcePath, 'utf8')
    .trim()
    .split('\n')
    .map((line) => JSON.parse(line).trafficSource)
  assert.deepEqual(retainedSources.sort(), ['gateway', 'hybrid_quality_scoring', 'hybrid_scoring', 'manual_account_test'], '热搜索清理必须保留四类持久化来源')

  await repositories.createAuditLogsBatchAsync([
    successAuditLog({
      id: 'audit_hot_search_older',
      traceId: olderTraceId,
      createdAt: new Date(nowMs - 60_000).toISOString(),
      sampleBucket: 999,
      sampleReason: 'success_sample_0.1',
      body: JSON.stringify({ keyword: uniqueKeyword, order: 'older', content: '最近1小时 rg 搜索应能命中正文内容' })
    }),
    successAuditLog({
      id: 'audit_hot_search_newer',
      traceId: newerTraceId,
      createdAt: new Date(nowMs - 5_000).toISOString(),
      sampleBucket: 999,
      sampleReason: 'success_sample_0.1',
      body: JSON.stringify({ keyword: uniqueKeyword, order: 'newer', content: '最近1小时 rg 搜索应按最新审计优先返回' })
    }),
    successAuditLog({
      id: 'audit_hot_search_exact_phrase',
      traceId: exactPhraseTraceId,
      createdAt: new Date(nowMs - 4_500).toISOString(),
      sampleBucket: 999,
      sampleReason: 'success_sample_0.1',
      body: JSON.stringify({ keyword: trimKeyword, content: '搜索输入只清理前后空格再匹配' })
    }),
    successAuditLog({
      id: 'audit_hot_search_cross_chunk',
      traceId: crossChunkTraceId,
      createdAt: new Date(nowMs - 4_000).toISOString(),
      sampleBucket: 999,
      sampleReason: 'success_sample_0.1',
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

  await repositories.createAuditLogsBatchAsync([
    successAuditLog({
      id: 'audit_hot_unsampled_success_body_omitted',
      traceId: `trace-hot-unsampled-body-omitted-${nowMs}`,
      createdAt: new Date(nowMs - 3_000).toISOString(),
      sampleBucket: 9000,
      sampleReason: 'success_hot_full_retention',
      body: JSON.stringify({ keyword: `${uniqueKeyword}-unsampled-hidden`, content: '普通成功热保留不应写入热搜索正文' })
    })
  ])
  const unsampledHiddenResult = await hotSearchFiles.grepAuditHotSearchFiles({
    keywords: [`${uniqueKeyword}-unsampled-hidden`],
    limit: 10
  })
  assert.deepEqual(unsampledHiddenResult.auditLogIds, [], '普通成功热保留日志只保留审计正文，不应再把正文写入热搜索镜像')

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
  assert.equal(cleanupResult.auditLogs, 1, '热窗口清理应只降级超过 1 小时且未命中 10% 采样的普通成功审计详情')
  assert(cleanupResult.auditPayloadBlobs >= 1, '热窗口清理应同步清理已无引用的 payload blob')
  assert(cleanupResult.auditHotSearchFiles >= 1, '热窗口清理应删除已完全超过热窗口的旧热搜索镜像文件')
  const unsampledAfterCleanup = repositories.listAuditLogs({ traceId: unsampledTraceId })
  assert.equal(unsampledAfterCleanup.total, 1, '未采样普通成功审计超过热窗口后应保留轻量 envelope')
  const unsampledDetailAfterCleanup = repositories.getAuditLogDetail(unsampledAfterCleanup.items[0]?.id ?? '')
  assert.equal(unsampledDetailAfterCleanup?.captureStatus, 'metadata_only', '未采样普通成功审计超过热窗口后应降级为 metadata_only')
  assert.equal(unsampledDetailAfterCleanup?.attemptCount, 0, '未采样普通成功审计降级后应清空 attempt 计数')
  assert.equal(unsampledDetailAfterCleanup?.payloadCount, 0, '未采样普通成功审计降级后应清空 payload 计数')
  assert.equal(unsampledDetailAfterCleanup?.rawPayloadBytes, 0, '未采样普通成功审计降级后应清空原始 payload 字节数')
  assert.equal(unsampledDetailAfterCleanup?.compressedPayloadBytes, 0, '未采样普通成功审计降级后应清空压缩 payload 字节数')
  assert.equal(unsampledDetailAfterCleanup?.attempts.length, 0, '未采样普通成功审计降级后应删除 attempt 详情')
  assert.equal(unsampledDetailAfterCleanup?.payloads.length, 0, '未采样普通成功审计降级后应删除 payload 详情')
  assert.equal(repositories.listAuditLogs({ traceId: sampledTraceId }).total, 1, '命中 10% 稳定采样的成功审计应继续保留')
  assert.equal(repositories.listAuditLogs({ traceId: failedTraceId }).total, 1, '失败审计不应被成功热窗口清理删除')

  console.log('审计热搜索与热保留清理回归通过：rg 可搜索最近 1 小时内容，超过热窗口后未采样普通成功审计仅保留 metadata_only envelope')
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
  const jobsSource = readFileSync(new URL('../../modules/background/background-jobs.ts', import.meta.url), 'utf8')
  const registrySource = readFileSync(new URL('../../modules/background/background-job-registry.entries.ts', import.meta.url), 'utf8')
  assert(jobsSource.includes("name: backgroundScheduledJobName('audit-hot-retention-cleanup')"), '后台 worker 必须注册审计热窗口清理任务')
  assert(
    jobsSource.includes("name: backgroundScheduledJobName('audit-hot-retention-cleanup'), intervalMs: minuteMs"),
    '审计热窗口清理任务必须按分钟级频率执行，不能只挂在 daily 保留清理里'
  )
  assert(registrySource.includes("jobName: 'audit-hot-retention-cleanup'"), '后台任务注册表必须声明审计热窗口清理任务')
  assert(registrySource.includes("defaultRole: 'ingest-worker'"), '审计热窗口清理任务必须归属 ingest-worker')
}

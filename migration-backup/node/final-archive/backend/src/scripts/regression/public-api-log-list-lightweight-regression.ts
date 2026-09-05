import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const workspace = mkdtempSync(join(tmpdir(), 'juhe-public-api-log-list-'))
const listKeys = [
  'id',
  'createdAt',
  'sourceName',
  'method',
  'path',
  'success',
  'statusCode',
  'durationMs',
  'clientIp',
  'traceId'
].sort()
const detailSupplementKeys = [
  'sourceRefId',
  'tokenId',
  'tokenName',
  'tokenPrefix',
  'isTestToken',
  'queryString',
  'userAgent',
  'requestSizeBytes',
  'responseSizeBytes',
  'requestCaptureStatus',
  'responseCaptureStatus',
  'errorCode',
  'errorMessage',
  'startedAt',
  'endedAt',
  'requestData',
  'responseData'
].sort()

try {
  const { runtimeConfig } = await import('../../config/runtime.js')
  runtimeConfig.databaseDriver = 'sqlite'
  runtimeConfig.databasePath = join(workspace, 'business.sqlite')
  runtimeConfig.datasetDatabasePath = join(workspace, 'dataset.sqlite')
  runtimeConfig.statsDatabasePath = join(workspace, 'stats.sqlite')
  runtimeConfig.secret = 'public-api-log-lightweight-secret'
  runtimeConfig.processRole = 'worker'

  const {
    createPublicApiLogsBatch,
    getPublicApiLogDetail,
    getPublicApiLogDetailSupplement,
    getPublicApiLogDetailSupplementAsync,
    listPublicApiLogs,
    listPublicApiLogsAsync
  } = await import('../../storage/public-api-logs.repository.js')

  const fixtures = Array.from({ length: 51 }, (_, index) => {
    const n = String(index + 1).padStart(2, '0')
    return {
      id: `public-api-log-light-${n}`,
      traceId: `trace-light-${n}`,
      sourceRefId: 'source-ref',
      sourceName: '公开源',
      tokenId: 'token-id',
      tokenName: 'token-name',
      tokenPrefix: 'sk-test',
      isTestToken: true,
      method: 'POST',
      path: '/v1/responses',
      queryString: 'foo=1',
      clientIp: '10.0.0.8',
      userAgent: 'public-api-list-wide-proof',
      statusCode: 200,
      success: true,
      durationMs: 12 + index,
      requestSizeBytes: 100,
      responseSizeBytes: 200,
      requestCaptureStatus: 'complete' as const,
      responseCaptureStatus: 'complete' as const,
      errorCode: undefined,
      errorMessage: undefined,
      requestData: { secret: 'request-detail' },
      responseData: { secret: 'response-detail' },
      startedAt: `2026-07-22T00:00:${n}.000Z`,
      endedAt: `2026-07-22T00:00:${n}.100Z`,
      createdAt: `2026-07-22T00:01:${n}.000Z`
    }
  })
  createPublicApiLogsBatch(fixtures)

  const defaultPage = listPublicApiLogs()
  assert.equal(defaultPage.pageSize, 50, '公开 API 日志默认 pageSize 必须是 50')
  assert.equal(defaultPage.items.length, 50, '默认第一页应返回 50 条')
  assert.equal(defaultPage.hasMore, true, '51 条数据默认第一页 hasMore 必须为 true')
  assert.deepEqual(Object.keys(defaultPage.items[0] ?? {}).sort(), listKeys, '列表必须只返回 10 个轻量字段')

  const widePage = listPublicApiLogs({ pageSize: 100 })
  assert.equal(widePage.pageSize, 100, '兼容既有调用：公开 API 日志最大 pageSize 必须保持 100')
  assert.equal(widePage.items.length, 51, '100 条窗口应一次返回当前 51 条轻量记录')

  const secondPage = listPublicApiLogs({ page: 2, pageSize: 50 })
  assert.equal(secondPage.items.length, 1, '第二页应只剩 1 条')
  const ids = [...defaultPage.items.map((item) => item.id), ...secondPage.items.map((item) => item.id)]
  assert.equal(new Set(ids).size, 51, '两页合计 51 且无重复')
  assert.deepEqual(ids, fixtures.map((item) => item.id).reverse(), '排序必须 created_at DESC, id DESC')

  const detail = getPublicApiLogDetail(fixtures[0]!.id)
  assert(detail)
  assert.equal(detail.userAgent, 'public-api-list-wide-proof')
  assert.equal(detail.tokenName, 'token-name')
  assert.equal(detail.requestData.secret, 'request-detail')
  assert.equal(detail.responseData.secret, 'response-detail')

  const supplement = getPublicApiLogDetailSupplement(fixtures[0]!.id)
  assert(supplement)
  assert.deepEqual(Object.keys(supplement).sort(), detailSupplementKeys, '详情补充 DTO 必须只返回列表缺失字段')
  assert.equal(supplement.userAgent, 'public-api-list-wide-proof')
  assert.equal(supplement.tokenName, 'token-name')
  assert.equal(supplement.requestData.secret, 'request-detail')
  assert.equal(supplement.responseData.secret, 'response-detail')
  for (const key of listKeys) {
    assert.equal(key in supplement, false, `详情补充 DTO 不得重复列表字段 ${key}`)
  }
  assert.equal(getPublicApiLogDetailSupplement('missing-public-api-log'), undefined, '不存在的日志补充读取应返回 undefined')

  runtimeConfig.processRole = 'db-service'
  const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
  const handledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const workerFirstPage = await listPublicApiLogsAsync({ page: 1, pageSize: 50, sourceRefId: 'source-ref' })
  const workerSecondPage = await listPublicApiLogsAsync({ page: 2, pageSize: 50, sourceRefId: 'source-ref' })
  assert.deepEqual(workerFirstPage, defaultPage, 'SQLite read worker 列表第一页必须与同步轻量投影一致')
  assert.deepEqual(workerSecondPage, secondPage, 'SQLite read worker 列表第二页必须与同步轻量投影一致')
  assert(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs >= handledJobsBefore + 2,
    '公开 API 日志分页列表必须进入 SQLite read worker'
  )
  const workerSupplement = await getPublicApiLogDetailSupplementAsync(fixtures[0]!.id)
  const serializableSupplement = Object.fromEntries(Object.entries(supplement).filter(([, value]) => value !== undefined))
  assert.deepEqual(workerSupplement, serializableSupplement, 'SQLite DB service 模式必须通过专用只读 worker 返回相同详情增量')

  const source = await import('node:fs').then(({ readFileSync }) => readFileSync(new URL('../../storage/public-api-logs.repository.ts', import.meta.url), 'utf8'))
  const projection = source.match(/function publicApiLogListSelectColumns[\s\S]*?\n\}/)?.[0]
    ?? source.match(/function publicApiLogSummarySelectColumns[\s\S]*?\n\}/)?.[0]
    ?? ''
  for (const forbidden of ['token_id', 'token_name', 'user_agent', 'request_data_json', 'response_data_json', 'request_size_bytes', 'error_message']) {
    assert.equal(projection.includes(forbidden), false, `列表 SQL 不得投影 ${forbidden}`)
  }
  const detailProjection = source.match(/function publicApiLogDetailSupplementSelectColumns[\s\S]*?\n\}/)?.[0] ?? ''
  assert(detailProjection, '详情补充 SQL 必须使用显式投影')
  for (const forbidden of [
    'id', 'created_at', 'source_name', 'method', 'path', 'client_ip', 'status_code',
    'success', 'duration_ms', 'trace_id'
  ]) {
    assert.equal(new RegExp(`['\"]${forbidden}['\"]`).test(detailProjection), false, `详情补充 SQL 不得重复列表列 ${forbidden}`)
  }
  for (const required of [
    'source_ref_id', 'token_id', 'token_name', 'query_string', 'user_agent',
    'request_size_bytes', 'response_size_bytes', 'request_capture_status',
    'response_capture_status', 'started_at', 'ended_at',
    'request_data_json', 'response_data_json'
  ]) {
    assert.equal(new RegExp(`['\"]${required}['\"]`).test(detailProjection), true, `详情补充 SQL 必须投影 ${required}`)
  }
  assert.match(source, /pal\.success = 1/, 'Node PostgreSQL 与 SQLite 的 success 均为整数列')
  assert.match(source, /pal\.success = 0/, 'Node PostgreSQL 与 SQLite 的 success 均为整数列')

  console.log('公开 API 日志轻量列表与详情增量回归通过')
} finally {
  const { closeSqliteReadWorkerPool } = await import('../../storage/sqlite-read-worker-pool.js')
  await closeSqliteReadWorkerPool()
  const { closeStorageDatabases } = await import('../../storage/database.js')
  closeStorageDatabases()
  rmSync(workspace, { recursive: true, force: true })
}

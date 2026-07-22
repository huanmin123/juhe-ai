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
    listPublicApiLogs
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

  const source = await import('node:fs').then(({ readFileSync }) => readFileSync(new URL('../../storage/public-api-logs.repository.ts', import.meta.url), 'utf8'))
  const projection = source.match(/function publicApiLogListSelectColumns[\s\S]*?\n\}/)?.[0]
    ?? source.match(/function publicApiLogSummarySelectColumns[\s\S]*?\n\}/)?.[0]
    ?? ''
  for (const forbidden of ['token_id', 'token_name', 'user_agent', 'request_data_json', 'response_data_json', 'request_size_bytes', 'error_message']) {
    assert.equal(projection.includes(forbidden), false, `列表 SQL 不得投影 ${forbidden}`)
  }
  assert.match(source, /pal\.success = true/, 'PostgreSQL 成功筛选必须使用 boolean true')
  assert.match(source, /pal\.success = false/, 'PostgreSQL 失败筛选必须使用 boolean false')
  assert.match(source, /pal\.success = 1/, 'SQLite 成功筛选必须保持整数 1')
  assert.match(source, /pal\.success = 0/, 'SQLite 失败筛选必须保持整数 0')

  console.log('公开 API 日志轻量列表回归通过')
} finally {
  const { closeStorageDatabases } = await import('../../storage/database.js')
  closeStorageDatabases()
  rmSync(workspace, { recursive: true, force: true })
}

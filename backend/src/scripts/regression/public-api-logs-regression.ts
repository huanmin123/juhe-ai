import assert from 'node:assert/strict'
import { request as httpRequest, type Server } from 'node:http'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Express } from 'express'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-public-api-logs-'))
Object.assign(process.env, {
  JUHE_AI_DATABASE_PATH: join(tempRoot, 'business.sqlite3'),
  JUHE_AI_DATASET_DATABASE_PATH: join(tempRoot, 'dataset.sqlite3'),
  JUHE_AI_STATS_DATABASE_PATH: join(tempRoot, 'stats.sqlite3'),
  JUHE_AI_USAGE_SHARD_ROOT: join(tempRoot, 'usage-shards'),
  JUHE_AI_PROCESS_ROLE: 'worker',
  JUHE_AI_WORKER_ROLE: 'ingest-worker',
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
  JUHE_AI_LOG_FILE_ENABLED: 'false'
})

const [
  { createSystemApiApp },
  {
    createPublicApiLogsBatch,
    createPublicApiLog,
    createSession,
    cleanupPublicApiLogsBefore,
    getPublicApiLogDetail,
    listPublicApiLogs,
    updateSystemAccount
  },
  {
    builtInExternalIntegrationTestSourceId,
    builtInExternalIntegrationTestTokenId,
    findExternalIntegrationSourceTokenSecret
  },
  { closeStorageDatabases, getDatasetDatabase },
  { logger },
  { enqueuePublicApiLog, flushPublicApiLogQueueForTest },
  { default: express },
  { requestContextMiddleware },
  { capturePublicApiLog }
] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/repositories.js'),
  import('../../storage/external-integration-source.repository.js'),
  import('../../storage/database.js'),
  import('../../shared/logger.js'),
  import('../../modules/public-api-logs/public-api-log-queue.service.js'),
  import('express'),
  import('../../shared/request-context.js'),
  import('../../modules/public-api-logs/public-api-log-capture.middleware.js')
])

logger.level = 'silent'
const builtInTokenSecret = findExternalIntegrationSourceTokenSecret(
  builtInExternalIntegrationTestSourceId,
  builtInExternalIntegrationTestTokenId
)
assert(builtInTokenSecret?.token, '内置测试 Token 应写入数据库并可用于公开接口日志回归')
const builtInTestToken = builtInTokenSecret.token

try {
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', publicApiPrefix: '/__aipublic__', trustProxy: 1 })
  const server = await listen(app)
  const address = server.address()
  assert(address && typeof address !== 'string', '测试 HTTP 服务地址无效')
  const baseUrl = `http://127.0.0.1:${address.port}`
  assert.equal(updateSystemAccount('sys_admin', { mustChangePassword: false })?.mustChangePassword, false, '回归准备：管理员应可访问管理 API')
  const adminCookie = `juhe_ai_session=${createSession('sys_admin', 1).token}`

  try {
    const missingToken = await requestJson(baseUrl, '/__aipublic__/group/list?targetUsername=huanmin', {
      'x-trace-id': 'trace-public-missing-token'
    })
    assert.equal(missingToken.status, 401)
    assert.equal(missingToken.body.code, 'external_source_token_missing')

    const missingTokenLog = singleLogByTraceId('trace-public-missing-token')
    assert.equal(missingTokenLog.statusCode, 401, '缺少 token 的公开接口请求应写入 401 日志')
    assert.equal(missingTokenLog.success, false)
    assert.equal(missingTokenLog.sourceName, undefined, '缺少 token 时不应伪造来源系统')

    const success = await requestJson(baseUrl, '/__aipublic__/group/list?targetUsername=huanmin&keyword=public-query-secret&providerCode=gpt', {
      Authorization: `Bearer ${builtInTestToken}`,
      'x-trace-id': 'trace-public-success',
      'x-forwarded-for': '198.51.100.250, 203.0.113.88'
    })
    assert.equal(success.status, 200)
    assert.equal(success.body.data.source, 'mock')

    const successLog = singleLogByTraceId('trace-public-success')
    assert.equal(successLog.statusCode, 200)
    assert.equal(successLog.success, true)
    assert.equal(successLog.sourceName, '内置测试来源')
    assert.equal(successLog.clientIp, '203.0.113.88', '公开接口日志应记录主进程转发后的真实客户端 IP，不能退回 DB service 本地地址')
    const successDetail = requiredDetail(successLog.id)
    assert.equal(successDetail.isTestToken, true)
    assert.equal(successDetail.queryString, 'targetUsername=huanmin&keyword=public-query-secret&providerCode=gpt', '公开接口日志 queryString 应保留查询参数原文')
    assert.equal((successDetail.requestData.query as Record<string, unknown>).keyword, 'public-query-secret', '请求快照 query 应保留 keyword 参数原文')
    assert(JSON.stringify(successDetail).includes('public-query-secret'), '公开接口日志详情应保存 query token 原文')

    const suffixPath = '/__aipublic__/group/list/compact'
    const suffixPathRequest = await requestJson(baseUrl, `${suffixPath}?targetUsername=huanmin&include=suffix`, {
      Authorization: `Bearer ${builtInTestToken}`,
      'x-trace-id': 'trace-public-path-suffix'
    })
    assert.equal(suffixPathRequest.status, 404)
    const suffixPathLog = singleLogByTraceId('trace-public-path-suffix')
    assert.equal(suffixPathLog.path, suffixPath, '公开接口日志列表应保留完整接口后缀路径')
    const suffixPathDetail = requiredDetail(suffixPathLog.id)
    assert.equal(suffixPathDetail.queryString, 'targetUsername=huanmin&include=suffix', '公开接口日志后缀路径 queryString 应单独保留')
    assert.equal(suffixPathDetail.path, suffixPath, '公开接口日志详情应保留完整接口后缀路径')
    assert.equal((suffixPathDetail.requestData as Record<string, unknown>).path, suffixPath, '公开接口日志请求摘要应保留完整接口后缀路径')
    assert.deepEqual(
      listPublicApiLogs({ path: suffixPath, pageSize: 10 }).items.map((item) => item.traceId),
      ['trace-public-path-suffix'],
      '公开接口日志 path 筛选应按完整后缀路径精确命中'
    )
    assert.deepEqual(
      listPublicApiLogs({ path: `GET ${suffixPath}?targetUsername=huanmin&include=suffix`, pageSize: 10 }).items.map((item) => item.traceId),
      ['trace-public-path-suffix'],
      '公开接口日志 path 筛选应兼容从接口列复制的 METHOD path?query 文本'
    )
    assert(
      !listPublicApiLogs({ path: '/__aipublic__/group/list', pageSize: 20 }).items.some((item) => item.traceId === 'trace-public-path-suffix'),
      '公开接口日志 path 筛选不应把后缀路径折叠到父路径'
    )

    const apiKeyAdd = await requestJson(baseUrl, '/__aipublic__/api-key/add', {
      Authorization: `Bearer ${builtInTestToken}`,
      'x-trace-id': 'trace-public-api-key-add'
    }, 'POST', {
      targetUsername: 'huanmin',
      name: '公开接口日志回归 Key',
      routeStrategyId: 'mock_route_strategy_public'
    })
    assert.equal(apiKeyAdd.status, 201)
    assert.equal(apiKeyAdd.body.data.apiKey.key, 'juis_mock_public_api_key', '测试 token 响应仍应按原业务返回一次性明文 Key')
    const apiKeyAddDetail = requiredDetail(singleLogByTraceId('trace-public-api-key-add').id)
    const apiKeySerialized = JSON.stringify(apiKeyAddDetail)
    assert(apiKeySerialized.includes('juis_mock_public_api_key'), '公开接口日志应保存 API Key 新增响应里的明文 key')
    assert.equal(apiKeySerialized.includes('[redacted]'), false, 'API Key 新增日志不应写入脱敏占位')

    const accountAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${builtInTestToken}`,
      'x-trace-id': 'trace-public-account-add'
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'gpt',
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name: '公开接口日志回归账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-log-regression-secret',
      supportedModels: ['gpt-5.5']
    })
    assert.equal(accountAdd.status, 200)
    const accountAddDetail = requiredDetail(singleLogByTraceId('trace-public-account-add').id)
    const accountSerialized = JSON.stringify(accountAddDetail)
    assert(accountSerialized.includes('sk-public-log-regression-secret'), '公开接口日志应保存账号写入请求里的上游 API Key')
    assert.equal(accountSerialized.includes('[redacted]'), false, '账号写入日志不应写入脱敏占位')

    const notFound = await requestJson(baseUrl, '/__aipublic__/not-found', {
      Authorization: `Bearer ${builtInTestToken}`,
      'x-trace-id': 'trace-public-not-found'
    })
    assert.equal(notFound.status, 404)
    assert.equal(singleLogByTraceId('trace-public-not-found').statusCode, 404, '公开前缀 404 也应写入公开接口日志')

    const malformedJson = await requestRaw(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${builtInTestToken}`,
      'Content-Type': 'application/json',
      'x-trace-id': 'trace-public-malformed-json'
    }, 'POST', '{"targetUsername":')
    assert.equal(malformedJson.status, 400)
    const malformedJsonDetail = requiredDetail(singleLogByTraceId('trace-public-malformed-json').id)
    assert.equal(malformedJsonDetail.requestCaptureStatus, 'dropped', '无效 JSON 请求体应标记为已丢弃')
    assert.equal((malformedJsonDetail.requestData.body as Record<string, unknown>).reason, 'request_body_parse_failed')

    const largeBody = JSON.stringify({ targetUsername: 'huanmin', note: 'x'.repeat(300 * 1024) })
    const tooLarge = await requestRaw(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${builtInTestToken}`,
      'Content-Type': 'application/json',
      'x-trace-id': 'trace-public-body-too-large'
    }, 'POST', largeBody)
    assert.equal(tooLarge.status, 413)
    const tooLargeDetail = requiredDetail(singleLogByTraceId('trace-public-body-too-large').id)
    assert.equal(tooLargeDetail.requestCaptureStatus, 'dropped', '超大请求体应标记为已丢弃')
    assert.equal((tooLargeDetail.requestData.body as Record<string, unknown>).reason, 'request_body_too_large')

    const adminList = await requestJson(baseUrl, '/__aisys__/api/public-api-logs?traceId=trace-public-&pageSize=10', {
      Cookie: adminCookie
    })
    assert.equal(adminList.status, 200)
    assert(adminList.body.data.items.length >= 5, '管理员公开接口日志列表应返回刚产生的公开调用记录')

    const detailResponse = await requestJson(baseUrl, `/__aisys__/api/public-api-logs/${successLog.id}`, {
      Cookie: adminCookie
    })
    assert.equal(detailResponse.status, 200)
    const detailSupplement = detailResponse.body.data as Record<string, unknown>
    const allowedDetailSupplementKeys = new Set([
      'sourceRefId', 'tokenId', 'tokenName', 'tokenPrefix', 'isTestToken', 'queryString', 'userAgent',
      'requestSizeBytes', 'responseSizeBytes', 'requestCaptureStatus', 'responseCaptureStatus',
      'errorCode', 'errorMessage', 'startedAt', 'endedAt', 'requestData', 'responseData'
    ])
    for (const key of Object.keys(detailSupplement)) {
      assert(allowedDetailSupplementKeys.has(key), `管理员详情增量响应包含非白名单字段：${key}`)
    }
    for (const duplicateKey of ['id', 'createdAt', 'sourceName', 'method', 'path', 'success', 'statusCode', 'durationMs', 'clientIp', 'traceId']) {
      assert.equal(duplicateKey in detailSupplement, false, `管理员详情增量响应不得重复列表字段：${duplicateKey}`)
    }
    assert.equal(typeof detailSupplement.requestData, 'object')
    assert.equal(typeof detailSupplement.responseData, 'object')
  } finally {
    await closeServer(server)
  }

  const closeApp = express()
  const slowRouteHit = deferred<void>()
  closeApp.use(requestContextMiddleware)
  closeApp.use('/__aipublic__', capturePublicApiLog)
  closeApp.use('/__aipublic__/slow-close', (req, _res) => {
    req.on('data', () => {})
    slowRouteHit.resolve()
  })
  const closeServerInstance = await listen(closeApp)
  const closeAddress = closeServerInstance.address()
  assert(closeAddress && typeof closeAddress !== 'string', '客户端断开测试 HTTP 服务地址无效')
  try {
    const request = httpRequest({
      hostname: '127.0.0.1',
      port: closeAddress.port,
      path: '/__aipublic__/slow-close',
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Content-Length': '1024',
        'x-trace-id': 'trace-public-client-closed'
      }
    })
    request.on('error', () => {})
    request.write('{"partial":')
    await slowRouteHit.promise
    request.destroy()
    const closedLog = await waitForSingleLogByTraceId('trace-public-client-closed')
    assert.equal(closedLog.statusCode, 499, '客户端提前断开应写入 499 公开接口日志')
    assert.equal(closedLog.success, false)
    assert.equal(requiredDetail(closedLog.id).errorCode, 'public_api_client_closed')
  } finally {
    await closeServer(closeServerInstance)
  }

  const now = Date.now()
  const retentionDays = 30
  const oneDayMs = 24 * 60 * 60 * 1000
  const oldLog = createPublicApiLog(publicApiLogFixture('publog_old_retention', new Date(now - (retentionDays + 1) * oneDayMs).toISOString()))
  const recentLog = createPublicApiLog(publicApiLogFixture('publog_recent_retention', new Date(now - retentionDays * oneDayMs + 5 * 60 * 1000).toISOString()))
  const cleanupResult = cleanupPublicApiLogsBefore(new Date(now - retentionDays * oneDayMs).toISOString(), 1000)
  assert(cleanupResult >= 1, '公开接口日志保留清理应删除超过 30 天的记录')
  assert.equal(getPublicApiLogDetail(oldLog.id), undefined, '超过 30 天的公开接口日志应被清理')
  assert(getPublicApiLogDetail(recentLog.id), '30 天内公开接口日志应保留')

  const batchCutoff = new Date().toISOString()
  for (let index = 0; index < 12; index += 1) {
    createPublicApiLog(publicApiLogFixture(`publog_bounded_${index}`, '2000-01-01T00:00:00.000Z'))
  }
  assert.equal(cleanupPublicApiLogsBefore(batchCutoff, 5), 5, '公开接口日志 repository 清理必须遵守传入 limit')
  assert.equal(listPublicApiLogs({ traceId: 'publog_bounded_', pageSize: 20 }).items.length, 7, '限定批量清理不能一次删除全部过期记录')

  const datasetDatabase = getDatasetDatabase()
  const originalDatasetPrepare = datasetDatabase.prepare.bind(datasetDatabase) as typeof datasetDatabase.prepare
  let publicApiLogInsertPrepareCount = 0
  datasetDatabase.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+public_api_logs\b/i.test(sql)) {
      publicApiLogInsertPrepareCount += 1
    }
    return originalDatasetPrepare(sql)
  }) as typeof datasetDatabase.prepare
  try {
    createPublicApiLogsBatch(Array.from({ length: 3 }, (_, index) => publicApiLogFixture(`publog_batch_insert_${index}`, new Date().toISOString())))
  } finally {
    datasetDatabase.prepare = originalDatasetPrepare
  }
  assert.equal(publicApiLogInsertPrepareCount, 1, '公开接口日志批量写入应复用单个 insert statement')
  assert.equal(listPublicApiLogs({ traceId: 'publog_batch_insert_', pageSize: 10 }).items.length, 3, '公开接口日志批量写入应写入完整批次')

  let simulatedQueueFailure = true
  datasetDatabase.prepare = ((sql: string) => {
    if (simulatedQueueFailure && /^\s*INSERT\s+INTO\s+public_api_logs\b/i.test(sql)) {
      simulatedQueueFailure = false
      throw new Error('模拟公开接口日志批量写入失败')
    }
    return originalDatasetPrepare(sql)
  }) as typeof datasetDatabase.prepare
  try {
    assert.equal(enqueuePublicApiLog(publicApiLogFixture('publog_queue_retry_retained', new Date().toISOString())), true, '公开接口日志应可入队')
    flushPublicApiLogQueueForTest()
    assert.equal(listPublicApiLogs({ traceId: 'publog_queue_retry_retained', pageSize: 10 }).items.length, 0, '公开接口日志批量写失败时不应落入部分记录')
  } finally {
    datasetDatabase.prepare = originalDatasetPrepare
  }
  flushPublicApiLogQueueForTest()
  assert.equal(listPublicApiLogs({ traceId: 'publog_queue_retry_retained', pageSize: 10 }).items.length, 1, '公开接口日志批量写失败后应保留队首批次并可重试成功')

  const publicApiLogRepositorySource = readFileSync(new URL('../../storage/public-api-logs.repository.ts', import.meta.url), 'utf8')
  assert.match(publicApiLogRepositorySource, /insertPublicApiLogsPostgres\(tx,\s*chunk\)/, 'PG 公开接口日志应走批量写入')
  assert.match(publicApiLogRepositorySource, /ON CONFLICT\(id\) DO NOTHING/, 'PG 公开接口日志应支持 Redis Stream 重投幂等')
  assert.match(publicApiLogRepositorySource, /textPrefixUpperBound\(text\)/, '公开接口日志前缀筛选不应使用固定 \\uffff 上界')
  assert.match(publicApiLogRepositorySource, /runtimeConfig\.databaseDriver === 'postgres' \? `\$\{column\} COLLATE "C"` : column/, 'PG 公开接口日志前缀筛选必须使用 C collation，和使用记录 traceId 搜索一致')

  console.log('公开接口日志回归通过：公开请求记录、管理员查询、原文日志和 30 天保留清理均符合预期')
} finally {
  try {
    closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function singleLogByTraceId(traceId: string): ReturnType<typeof listPublicApiLogs>['items'][number] {
  flushPublicApiLogQueueForTest()
  const result = listPublicApiLogs({ traceId, pageSize: 10 })
  assert.equal(result.items.length, 1, `应按 traceId 查到唯一公开接口日志：${traceId}`)
  return result.items[0]
}

async function waitForSingleLogByTraceId(traceId: string): Promise<ReturnType<typeof listPublicApiLogs>['items'][number]> {
  const deadline = Date.now() + 1000
  while (Date.now() < deadline) {
    flushPublicApiLogQueueForTest()
    const result = listPublicApiLogs({ traceId, pageSize: 10 })
    if (result.items.length === 1) {
      return result.items[0]
    }
    await sleep(20)
  }
  return singleLogByTraceId(traceId)
}

function requiredDetail(id: string): NonNullable<ReturnType<typeof getPublicApiLogDetail>> {
  const detail = getPublicApiLogDetail(id)
  assert(detail, `公开接口日志详情不存在：${id}`)
  return detail
}

function publicApiLogFixture(id: string, createdAt: string): Parameters<typeof createPublicApiLog>[0] {
  return {
    id,
    traceId: id,
    method: 'GET',
    path: '/__aipublic__/group/list',
    statusCode: 200,
    success: true,
    durationMs: 1,
    requestData: {},
    responseData: {},
    startedAt: createdAt,
    endedAt: createdAt,
    createdAt
  }
}

function listen(app: Express): Promise<Server> {
  return new Promise((resolve, reject) => {
    const server = app.listen(0, '127.0.0.1')
    server.once('error', reject)
    server.once('listening', () => {
      server.off('error', reject)
      resolve(server)
    })
  })
}

function closeServer(server: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error)
        return
      }
      resolve()
    })
  })
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (error: unknown) => void } {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function requestJson(
  baseUrl: string,
  path: string,
  headers: Record<string, string> = {},
  method = 'GET',
  body?: unknown
): Promise<{ status: number; body: any }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: body === undefined ? headers : { ...headers, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  return {
    status: response.status,
    body: await response.json()
  }
}

async function requestRaw(
  baseUrl: string,
  path: string,
  headers: Record<string, string> = {},
  method = 'GET',
  body?: string
): Promise<{ status: number; body: string }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers,
    body
  })
  return {
    status: response.status,
    body: await response.text()
  }
}

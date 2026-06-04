import assert from 'node:assert/strict'
import type { Server } from 'node:http'
import { mkdtempSync, rmSync } from 'node:fs'
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
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
  JUHE_AI_LOG_FILE_ENABLED: 'false'
})

const [
  { createSystemApiApp },
  {
    createPublicApiLog,
    createSession,
    getPublicApiLogDetail,
    listPublicApiLogs,
    updateSystemAccount
  },
  { externalIntegrationTestToken },
  { cleanupExpiredRetainedData },
  { closeStorageDatabases },
  { logger }
] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/repositories.js'),
  import('../../storage/external-integration-source.repository.js'),
  import('../../modules/background/data-retention-cleanup.service.js'),
  import('../../storage/database.js'),
  import('../../shared/logger.js')
])

logger.level = 'silent'

try {
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', publicApiPrefix: '/__aipublic__' })
  const server = await listen(app)
  const address = server.address()
  assert(address && typeof address !== 'string', '测试 HTTP 服务地址无效')
  const baseUrl = `http://127.0.0.1:${address.port}`
  assert.equal(updateSystemAccount('sys_admin', { mustChangePassword: false })?.mustChangePassword, false, '回归准备：管理员应可访问管理 API')
  const adminCookie = `juhe_ai_session=${createSession('sys_admin', 1).token}`

  try {
    const missingToken = await requestJson(baseUrl, '/__aipublic__/demo/source-auth', {
      'x-trace-id': 'trace-public-missing-token'
    })
    assert.equal(missingToken.status, 401)
    assert.equal(missingToken.body.code, 'external_source_token_missing')

    const missingTokenLog = singleLogByTraceId('trace-public-missing-token')
    assert.equal(missingTokenLog.statusCode, 401, '缺少 token 的公开接口请求应写入 401 日志')
    assert.equal(missingTokenLog.success, false)
    assert.equal(missingTokenLog.sourceName, undefined, '缺少 token 时不应伪造来源系统')

    const success = await requestJson(baseUrl, '/__aipublic__/demo/source-auth?token=public-query-secret&safe=ok', {
      Authorization: `Bearer ${externalIntegrationTestToken}`,
      'x-trace-id': 'trace-public-success'
    })
    assert.equal(success.status, 200)
    assert.equal(success.body.data.mock, true)

    const successLog = singleLogByTraceId('trace-public-success')
    assert.equal(successLog.statusCode, 200)
    assert.equal(successLog.success, true)
    assert.equal(successLog.isTestToken, true)
    assert.equal(successLog.sourceName, '内置测试来源')
    assert.equal(successLog.queryString, 'token=%5Bredacted%5D&safe=ok', '公开接口日志 queryString 应脱敏敏感参数')
    const successDetail = requiredDetail(successLog.id)
    assert.equal((successDetail.requestData.query as Record<string, unknown>).token, '[redacted]', '请求快照 query 应脱敏 token 参数')
    assert.equal(JSON.stringify(successDetail).includes('public-query-secret'), false, '公开接口日志详情不能保存 query token 原文')

    const apiKeyAdd = await requestJson(baseUrl, '/__aipublic__/api-key/add', {
      Authorization: `Bearer ${externalIntegrationTestToken}`,
      'x-trace-id': 'trace-public-api-key-add'
    }, 'POST', {
      targetUsername: 'huanmin',
      name: '公开接口日志回归 Key',
      groupBindings: [{ groupId: 'mock_group_public' }]
    })
    assert.equal(apiKeyAdd.status, 201)
    assert.equal(apiKeyAdd.body.data.apiKey.key, 'juis_mock_public_api_key', '测试 token 响应仍应按原业务返回一次性明文 Key')
    const apiKeyAddDetail = requiredDetail(singleLogByTraceId('trace-public-api-key-add').id)
    const apiKeySerialized = JSON.stringify(apiKeyAddDetail)
    assert.equal(apiKeySerialized.includes('juis_mock_public_api_key'), false, '公开接口日志不能保存 API Key 新增响应里的明文 key')
    assert(apiKeySerialized.includes('[redacted]'), 'API Key 新增日志应保留脱敏占位')

    const accountAdd = await requestJson(baseUrl, '/__aipublic__/account/add', {
      Authorization: `Bearer ${externalIntegrationTestToken}`,
      'x-trace-id': 'trace-public-account-add'
    }, 'POST', {
      targetUsername: 'huanmin',
      targetGroupName: '福利',
      providerCode: 'openai',
      name: '公开接口日志回归账号',
      type: 'api_key',
      baseUrl: 'https://push.example/v1',
      apiKey: 'sk-public-log-regression-secret',
      supportedModels: ['gpt-5.5']
    })
    assert.equal(accountAdd.status, 200)
    const accountAddDetail = requiredDetail(singleLogByTraceId('trace-public-account-add').id)
    const accountSerialized = JSON.stringify(accountAddDetail)
    assert.equal(accountSerialized.includes('sk-public-log-regression-secret'), false, '公开接口日志不能保存账号写入请求里的上游 API Key')
    assert(accountSerialized.includes('[redacted]'), '账号写入日志应保留脱敏占位')

    const notFound = await requestJson(baseUrl, '/__aipublic__/not-found', {
      Authorization: `Bearer ${externalIntegrationTestToken}`,
      'x-trace-id': 'trace-public-not-found'
    })
    assert.equal(notFound.status, 404)
    assert.equal(singleLogByTraceId('trace-public-not-found').statusCode, 404, '公开前缀 404 也应写入公开接口日志')

    const adminList = await requestJson(baseUrl, '/__aisys__/api/public-api-logs?traceId=trace-public-&pageSize=10', {
      Cookie: adminCookie
    })
    assert.equal(adminList.status, 200)
    assert(adminList.body.data.items.length >= 5, '管理员公开接口日志列表应返回刚产生的公开调用记录')

    const detailResponse = await requestJson(baseUrl, `/__aisys__/api/public-api-logs/${successLog.id}`, {
      Cookie: adminCookie
    })
    assert.equal(detailResponse.status, 200)
    assert.equal(detailResponse.body.data.id, successLog.id)
  } finally {
    await closeServer(server)
  }

  const oldLog = createPublicApiLog(publicApiLogFixture('publog_old_retention', '2000-01-01T00:00:00.000Z'))
  const recentLog = createPublicApiLog(publicApiLogFixture('publog_recent_retention', new Date().toISOString()))
  const cleanupResult = await cleanupExpiredRetainedData()
  assert(cleanupResult.publicApiLogs >= 1, '数据保留清理应删除 7 天前的公开接口日志')
  assert.equal(getPublicApiLogDetail(oldLog.id), undefined, '7 天前公开接口日志应被清理')
  assert(getPublicApiLogDetail(recentLog.id), '7 天内公开接口日志应保留')

  console.log('公开接口日志回归通过：公开请求记录、管理员查询、敏感字段脱敏和 7 天保留清理均符合预期')
} finally {
  try {
    closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function singleLogByTraceId(traceId: string): ReturnType<typeof listPublicApiLogs>['items'][number] {
  const result = listPublicApiLogs({ traceId, pageSize: 10 })
  assert.equal(result.items.length, 1, `应按 traceId 查到唯一公开接口日志：${traceId}`)
  return result.items[0]
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
    path: '/__aipublic__/demo/source-auth',
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

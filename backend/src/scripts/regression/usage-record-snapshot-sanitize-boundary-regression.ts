import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-snapshot-sanitize-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.databasePath = join(tempRoot, 'usage-snapshot-sanitize.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 1
runtimeConfig.secret = 'usage-snapshot-sanitize-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  usageRecordQueue,
  repositories,
  usageRecordShards,
  databaseModule
] = await Promise.all([
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js'),
  import('../../storage/database.js')
])

try {
  usageRecordQueue.clearUsageRecordQueueForTest()
  const snapshot = buildTrapSnapshot()
  usageRecordQueue.enqueueUsageRecordsLocal([{
    traceId: 'trace-usage-snapshot-sanitize-boundary',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    endpoint: 'POST /v1/responses',
    providerCode: 'gpt',
    success: true,
    stream: false,
    statusCode: 200,
    requestSnapshot: snapshot,
    createdAt: '2000-01-01T00:00:00.000Z'
  }])

  const runtime = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(runtime.queueLength, 1, '带大 snapshot 的 usage 记录应可入队')
  usageRecordQueue.clearUsageRecordQueueForTest()

  usageRecordQueue.enqueueUsageRecordsLocal([{
    traceId: 'trace-usage-complete-access-snapshot',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    accountId: '  acc_complete_snapshot  ',
    accountOwnerSystemAccountId: '  sys_admin  ',
    accountAccessType: 'owner',
    groupId: '  grp_complete_snapshot  ',
    groupOwnerSystemAccountId: '  sys_admin  ',
    groupAccessType: 'owner',
    accountAuthorizationSourceType: 'team',
    groupAuthorizationSourceType: 'team',
    endpoint: 'POST /v1/responses',
    success: true
  }])
  const normalizedCompleteScope = usageRecordQueue.peekPendingUsageRecordForTest()
  assert.equal(normalizedCompleteScope?.accountId, 'acc_complete_snapshot', '完整账户快照应保留并 trim accountId')
  assert.equal(normalizedCompleteScope?.accountOwnerSystemAccountId, 'sys_admin', '完整账户快照应保留并 trim owner')
  assert.equal(normalizedCompleteScope?.accountAccessType, 'owner', '完整账户快照应保留 accessType')
  assert.equal(normalizedCompleteScope?.groupId, 'grp_complete_snapshot', '完整分组快照应保留并 trim groupId')
  assert.equal(normalizedCompleteScope?.groupOwnerSystemAccountId, 'sys_admin', '完整分组快照应保留并 trim owner')
  assert.equal(normalizedCompleteScope?.groupAccessType, 'owner', '完整分组快照应保留 accessType')
  assert.equal(normalizedCompleteScope?.accountAuthorizationSourceType, undefined, '无账户授权 ID 时应剥离授权来源字段')
  assert.equal(normalizedCompleteScope?.groupAuthorizationSourceType, undefined, '无分组授权 ID 时应剥离授权来源字段')
  usageRecordQueue.clearUsageRecordQueueForTest()

  usageRecordQueue.enqueueUsageRecordsLocal([{
    traceId: 'trace-usage-incomplete-group-authorization',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    accountId: 'acc_group_authorized_without_group_auth',
    accountOwnerSystemAccountId: 'sys_owner',
    accountAccessType: 'group_authorized',
    groupId: 'grp_owner_without_authorization',
    groupOwnerSystemAccountId: 'sys_admin',
    groupAccessType: 'owner',
    endpoint: 'POST /v1/responses',
    success: true
  }])
  const normalizedIncompleteAuthorization = usageRecordQueue.peekPendingUsageRecordForTest()
  assert.equal(normalizedIncompleteAuthorization?.accountId, undefined, 'group_authorized 缺少完整分组授权时应剥离账户维度')
  assert.equal(normalizedIncompleteAuthorization?.groupId, 'grp_owner_without_authorization', '剥离异常账户维度时应保留独立合法分组维度')
  usageRecordQueue.clearUsageRecordQueueForTest()

  usageRecordQueue.enqueueUsageRecordsLocal([{
    traceId: 'trace-usage-team-source-without-team-id',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    accountId: 'acc_team-source-without-team-id',
    accountOwnerSystemAccountId: 'sys_owner',
    accountAccessType: 'account_authorized',
    accountAuthorizationId: 'auth_team-source-without-team-id',
    accountAuthorizationSourceType: 'team',
    endpoint: 'POST /v1/responses',
    success: true
  }])
  assert.equal(usageRecordQueue.peekPendingUsageRecordForTest()?.accountId, undefined, 'team 授权来源缺团队 ID 时应剥离账户维度')
  usageRecordQueue.clearUsageRecordQueueForTest()

  const createdAt = '2000-01-01T00:00:00.000Z'
  const recordId = usageRecordShards.generateUsageRecordId(createdAt, 'sanitize-test')
  usageRecordQueue.enqueueUsageRecordsLocal([{
    id: recordId,
    traceId: 'trace-usage-snapshot-sensitive-boundary',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: 'grp_sensitive_snapshot',
    groupAuthorizationId: 'auth_incomplete_group',
    groupAuthorizationSourceType: 'team',
    groupAuthorizationSourceTeamId: 'team_incomplete_group',
    accountId: 'acc_incomplete_usage_scope',
    accountAuthorizationId: 'auth_incomplete_account',
    accountAuthorizationSourceType: 'team',
    accountAuthorizationSourceTeamId: 'team_incomplete_account',
    endpoint: 'POST /v1/responses',
    providerCode: 'gpt',
    success: false,
    stream: false,
    statusCode: 502,
    errorMessage: 'top client_secret=top-usage-client-secret Authorization: Bearer sk-top-usage-secret-token',
    requestSnapshot: {
      method: 'POST',
      originalUrl: '/v1/responses?api_key=request-url-secret&safe=ok',
      headers: {
        authorization: 'Bearer request-header-secret'
      },
      bodyText: '{"client_secret":"request-body-client-secret","safe":"ok"}'
    },
    responseSnapshot: {
      upstreamUrl: 'https://url-user:url-password@example.com/v1/chat/completions?client_secret=response-url-secret&safe=ok',
      headers: {
        'set-cookie': 'session=response-cookie-secret'
      },
      bodyText: '{"error":{"message":"client_secret=response-body-client-secret Authorization: Bearer sk-response-body-secret-token"}}',
      errorMessage: 'id_token=response-error-id-token sk-response-error-secret-token'
    },
    createdAt
  }])
  const normalizedIncompleteScope = usageRecordQueue.peekPendingUsageRecordForTest()
  assert.equal(normalizedIncompleteScope?.groupId, undefined, '缺少分组归属快照时应剥离 groupId')
  assert.equal(normalizedIncompleteScope?.groupAuthorizationId, undefined, '剥离 groupId 时应同步剥离分组授权字段')
  assert.equal(normalizedIncompleteScope?.groupAuthorizationSourceType, undefined, '剥离 groupId 时应同步剥离分组授权来源类型')
  assert.equal(normalizedIncompleteScope?.groupAuthorizationSourceTeamId, undefined, '剥离 groupId 时应同步剥离分组授权团队')
  assert.equal(normalizedIncompleteScope?.accountId, undefined, '缺少账户归属快照时应剥离 accountId')
  assert.equal(normalizedIncompleteScope?.accountAuthorizationId, undefined, '剥离 accountId 时应同步剥离账户授权字段')
  assert.equal(normalizedIncompleteScope?.accountAuthorizationSourceType, undefined, '剥离 accountId 时应同步剥离账户授权来源类型')
  assert.equal(normalizedIncompleteScope?.accountAuthorizationSourceTeamId, undefined, '剥离 accountId 时应同步剥离账户授权团队')
  await usageRecordQueue.flushAllUsageRecordQueueAsync()
  const flushedRuntime = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(flushedRuntime.flushFailureCount, 0, '使用记录 flush 不应失败')
  assert.equal(flushedRuntime.queueLength, 0, '使用记录 flush 后队列应清空')
  const detail = repositories.getUsageRecordDetail(recordId)
  assert(detail, '应能读回写入的使用记录详情')
  const detailText = JSON.stringify(detail)
  assertAllPresent(detailText, [
    'top-usage-client-secret',
    'sk-top-usage-secret-token',
    'request-url-secret',
    'request-body-client-secret',
    'url-user',
    'url-password',
    'response-url-secret',
    'response-body-client-secret',
    'sk-response-body-secret-token',
    'response-error-id-token',
    'sk-response-error-secret-token'
  ], '使用记录落库内容应保留原文')
  assertAllAbsent(detailText, [
    'request-header-secret',
    'response-cookie-secret'
  ], '使用记录 header snapshot 不应保留敏感原文')
  assert(detailText.includes('[redacted]'), '使用记录 header snapshot 应写入脱敏占位')
  assert(String(detail.responseSnapshot?.upstreamUrl ?? '').includes('safe=ok'), 'URL 安全查询参数应保留')
  assert(String(detail.requestSnapshot?.bodyText ?? '').includes('"safe":"ok"'), 'bodyText 中安全字段应保留')

  console.log('使用记录 snapshot 原文边界回归通过：对象字段上限仍生效，URL 凭据、敏感字符串和顶层错误按原文落库')
} finally {
  usageRecordQueue.clearUsageRecordQueueForTest()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function buildTrapSnapshot(): Record<string, string> {
  const snapshot: Record<string, string> = {}
  for (let index = 0; index < 80; index += 1) {
    snapshot[`field_${index}`] = 'x'.repeat(1024)
  }
  Object.defineProperty(snapshot, 'field_80_trap', {
    enumerable: true,
    get() {
      throw new Error('usage snapshot 清洗不应读取超过字段上限后的属性')
    }
  })
  return snapshot
}

function assertAllPresent(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(text.includes(marker), `${message}：${marker}`)
  }
}

function assertAllAbsent(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(!text.includes(marker), `${message}：${marker}`)
  }
}

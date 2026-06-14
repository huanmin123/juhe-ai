import { join } from 'node:path'

import { backendRoot } from '../../config/runtime.js'
import type { AuditLogInput } from '../../storage/audit-logs.repository.js'
import {
  builtInExternalIntegrationTestSourceId,
  builtInExternalIntegrationTestTokenId,
  findExternalIntegrationSource
} from '../../storage/external-integration-source.repository.js'
import type { OperationLogInput } from '../../storage/operation-logs.repository.js'
import { createPublicApiLog } from '../../storage/public-api-logs.repository.js'
import * as repositories from '../../storage/repositories.js'
import { createRuntimeLogsBatch, type RuntimeLogIndexInput } from '../../storage/runtime-logs.repository.js'
import {
  chunks,
  dayMs,
  idPrefix,
  minuteMs,
  namePrefix,
  providerCode,
  pseudoRandom,
  publicApiLogErrorCode,
  publicApiLogErrorMessage,
  publicApiLogStatus,
  roundCost,
  tracePrefix,
  type CreatedMockdata,
  type MockdataOptions,
  type UsageRecordSeed
} from './mockdata-shared.js'

export function createAuditMockdata(records: UsageRecordSeed[]): void {
  const auditLogs: AuditLogInput[] = records
    .filter((_, index) => index % 4 === 0)
    .map((record, index) => {
      const startedAt = record.createdAt
      const endedAt = new Date(Date.parse(startedAt) + (record.durationMs ?? 200)).toISOString()
      const retrySuccess = record.success && index % 13 === 0
      const auditOutcome = record.success
        ? retrySuccess ? 'success_after_retry' : 'success'
        : record.stream ? 'stream_failed' : 'upstream_failed'
      const firstAttemptFailed = retrySuccess
      return {
        id: `${idPrefix}audit_${String(index + 1).padStart(5, '0')}`,
        traceId: record.traceId,
        systemAccountId: record.systemAccountId,
        apiKeyId: record.apiKeyId,
        groupId: record.groupId,
        accountId: record.accountId,
        providerCode: record.providerCode,
        method: record.endpoint?.split(' ')[0] ?? 'POST',
        path: record.endpoint?.split(' ')[1] ?? '/v1/responses',
        model: record.model,
        stream: record.stream,
        clientIp: record.clientIp,
        userAgent: 'mockdata-client/1.0',
        auditOutcome,
        success: record.success,
        finalStatusCode: record.statusCode,
        errorPhase: record.success ? undefined : 'upstream_response',
        errorCode: record.errorCode,
        errorMessage: record.errorMessage,
        sampleBucket: index % 100,
        sampleReason: record.success ? 'mockdata_success_sample' : 'mockdata_failure_sample',
        startedAt,
        endedAt,
        durationMs: record.durationMs,
        firstTokenMs: record.firstTokenMs,
        attempts: [
          ...(firstAttemptFailed ? [{
            id: `${idPrefix}audit_attempt_${String(index + 1).padStart(5, '0')}_1`,
            tempId: `attempt-${index}-1`,
            attemptIndex: 1,
            accountId: record.accountId,
            accountOwnerSystemAccountId: record.accountOwnerSystemAccountId,
            groupId: record.groupId,
            providerCode: record.providerCode,
            upstreamMethod: record.endpoint?.split(' ')[0] ?? 'POST',
            upstreamUrl: `https://api.openai.com${record.endpoint?.split(' ')[1] ?? '/v1/responses'}`,
            upstreamStatusCode: 503,
            success: false,
            errorPhase: 'upstream_response',
            errorCode: 'service_unavailable',
            errorMessage: 'Mockdata 首次上游尝试失败',
            startedAt,
            endedAt: new Date(Date.parse(startedAt) + 180).toISOString(),
            durationMs: 180
          }] : []),
          {
            id: `${idPrefix}audit_attempt_${String(index + 1).padStart(5, '0')}_${firstAttemptFailed ? '2' : '1'}`,
            tempId: `attempt-${index}-final`,
            attemptIndex: firstAttemptFailed ? 2 : 1,
            accountId: record.accountId,
            accountOwnerSystemAccountId: record.accountOwnerSystemAccountId,
            groupId: record.groupId,
            providerCode: record.providerCode,
            upstreamMethod: record.endpoint?.split(' ')[0] ?? 'POST',
            upstreamUrl: `https://api.openai.com${record.endpoint?.split(' ')[1] ?? '/v1/responses'}`,
            upstreamStatusCode: record.statusCode,
            success: record.success,
            errorPhase: record.success ? undefined : 'upstream_response',
            errorCode: record.errorCode,
            errorMessage: record.errorMessage,
            startedAt: firstAttemptFailed ? new Date(Date.parse(startedAt) + 220).toISOString() : startedAt,
            endedAt,
            durationMs: record.durationMs
          }
        ],
        payloads: [
          {
            id: `${idPrefix}audit_payload_${String(index + 1).padStart(5, '0')}_client`,
            attemptTempId: `attempt-${index}-final`,
            partType: 'client_request',
            sequenceIndex: 0,
            contentType: 'application/json',
            headers: {
              'content-type': 'application/json',
              'x-mockdata': 'true'
            },
            body: JSON.stringify(record.requestSnapshot ?? {})
          },
          {
            id: `${idPrefix}audit_payload_${String(index + 1).padStart(5, '0')}_response`,
            attemptTempId: `attempt-${index}-final`,
            partType: record.success ? 'upstream_response' : 'gateway_error',
            sequenceIndex: 1,
            contentType: 'application/json',
            body: JSON.stringify(record.responseSnapshot ?? {})
          }
        ],
        createdAt: record.createdAt
      }
    })

  for (const chunk of chunks(auditLogs, 200)) {
    repositories.createAuditLogsBatch(chunk)
  }
}

export function createPublicApiLogMockdata(created: CreatedMockdata, options: MockdataOptions): number {
  const endpoints = [
    { method: 'GET', path: '/__aipublic__/ip/usage', query: 'range=last_7_days&page=1&pageSize=20', scope: 'ip_usage' },
    { method: 'GET', path: '/__aipublic__/account/usage', query: 'range=last_30_days&page=1&pageSize=20', scope: 'account_usage' },
    { method: 'GET', path: '/__aipublic__/consumption/ranking', query: 'range=last_7_days&metric=cost', scope: 'ranking' },
    { method: 'GET', path: '/__aipublic__/access/info', query: '', scope: 'access_info' },
    { method: 'GET', path: '/__aipublic__/group/list', query: `targetUsername=${created.users.admin.username}&providerCode=gpt`, scope: 'group_list' },
    { method: 'GET', path: '/__aipublic__/api-key/list', query: `targetUsername=${created.users.admin.username}`, scope: 'api_key_list' },
    { method: 'GET', path: '/__aipublic__/account/list', query: `targetUsername=${created.users.admin.username}&providerCode=gpt`, scope: 'account_list' },
    { method: 'POST', path: '/__aipublic__/group/add', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/group/update', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/group/del', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/api-key/add', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/api-key/update', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/api-key/del', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/account/add', query: '', scope: 'account_write' },
    { method: 'POST', path: '/__aipublic__/account/update', query: '', scope: 'account_write' },
    { method: 'POST', path: '/__aipublic__/account/del', query: '', scope: 'account_write' }
  ]
  const perDay = Math.min(60, Math.max(12, Math.ceil(options.dailyRequests / 20)))
  const total = options.days * perDay
  const endAt = Date.now() - 20 * minuteMs
  const startAt = endAt - (options.days - 1) * dayMs
  const builtInTestSource = findExternalIntegrationSource(builtInExternalIntegrationTestSourceId)
  const builtInTestToken = builtInTestSource?.tokens.find((token) => token.id === builtInExternalIntegrationTestTokenId)
  for (let index = 0; index < total; index += 1) {
    const dayIndex = Math.floor(index / perDay)
    const indexInDay = index % perDay
    const endpoint = endpoints[index % endpoints.length]
    const startedAtMs = startAt + dayIndex * dayMs + Math.floor((indexInDay / perDay) * (dayMs - minuteMs))
    const durationMs = 40 + Math.floor(pseudoRandom(index, 70) * 1200)
    const status = publicApiLogStatus(index, endpoint.method)
    const success = status >= 200 && status < 300
    const useTestToken = index % 6 === 0
    const source = index % 5 === 0 ? created.externalSources.readonly : created.externalSources.primary
    const token = source.token
    createPublicApiLog({
      id: `${idPrefix}public_api_log_${String(index + 1).padStart(5, '0')}`,
      traceId: `${tracePrefix}public-api-${String(index + 1).padStart(5, '0')}`,
      sourceRefId: useTestToken ? builtInExternalIntegrationTestSourceId : source.source.id,
      sourceName: useTestToken ? builtInTestSource?.name ?? '内置测试来源' : source.source.name,
      tokenId: useTestToken ? builtInExternalIntegrationTestTokenId : token.id,
      tokenName: useTestToken ? '内置测试 Token' : token.name,
      tokenPrefix: useTestToken ? builtInTestToken?.tokenPrefix ?? 'juis_...' : token.tokenPrefix,
      isTestToken: useTestToken,
      method: endpoint.method,
      path: endpoint.path,
      queryString: endpoint.query || undefined,
      clientIp: `172.20.${index % 16}.${20 + (index % 180)}`,
      userAgent: index % 7 === 0 ? 'mockdata-public-bot/1.0' : 'mockdata-public-client/1.0',
      statusCode: status,
      success,
      durationMs,
      requestSizeBytes: endpoint.method === 'POST' ? 320 + (index % 2048) : 80 + (index % 512),
      responseSizeBytes: success ? 1200 + (index % 12000) : 220 + (index % 1200),
      requestCaptureStatus: index % 19 === 0 ? 'truncated' : endpoint.method === 'POST' ? 'complete' : 'empty',
      responseCaptureStatus: success ? (index % 23 === 0 ? 'truncated' : 'complete') : 'complete',
      requestData: publicApiLogRequestData(endpoint, created, index),
      responseData: publicApiLogResponseData(endpoint, success, index),
      errorCode: success ? undefined : publicApiLogErrorCode(status),
      errorMessage: success ? undefined : publicApiLogErrorMessage(status),
      startedAt: new Date(startedAtMs).toISOString(),
      endedAt: new Date(startedAtMs + durationMs).toISOString(),
      createdAt: new Date(startedAtMs + durationMs).toISOString()
    })
  }
  return total
}

export function createOperationMockdata(created: CreatedMockdata, usageRecords: UsageRecordSeed[]): void {
  const resources = [
    { module: 'accounts', resourceType: 'account', resourceId: created.accounts.primary.id, resourceName: created.accounts.primary.name, action: 'create', summary: '创建主力 API Key 账户' },
    { module: 'accounts', resourceType: 'account', resourceId: created.accounts.rateLimited.id, resourceName: created.accounts.rateLimited.name, action: 'cooldown', summary: '标记账户限流冷却' },
    { module: 'groups', resourceType: 'group', resourceId: created.groups.main.id, resourceName: created.groups.main.name, action: 'create', summary: '创建主力分组' },
    { module: 'api_keys', resourceType: 'api_key', resourceId: created.apiKeys.adminMain.id, resourceName: created.apiKeys.adminMain.name, action: 'create', summary: '创建主力本地网关 Key' },
    { module: 'authorizations', resourceType: 'authorization', resourceId: created.authorizations[0]?.id, resourceName: '研发用户分组授权', action: 'create', summary: '创建研发用户分组授权' },
    { module: 'system_teams', resourceType: 'system_team', resourceId: created.teams.devTeam.id, resourceName: created.teams.devTeam.name, action: 'update_members', summary: '维护研发团队成员' },
    { module: 'proxies', resourceType: 'proxy', resourceId: `${idPrefix}proxy`, resourceName: `${namePrefix}HTTP 代理`, action: 'test', summary: '完成代理连通性测试' },
    { module: 'announcements', resourceType: 'announcement', resourceId: `${idPrefix}announcement`, resourceName: `${namePrefix}系统维护公告`, action: 'publish', summary: '发布系统维护公告' },
    { module: 'settings', resourceType: 'system_settings', resourceId: 'sys_admin', resourceName: '系统设置', action: 'update', summary: '调整统计聚合和数据保留参数' },
    { module: 'usage_records', resourceType: 'usage_record', resourceId: usageRecords[0]?.id, resourceName: '使用记录', action: 'query', summary: '查询近 1 月使用记录' }
  ]
  const logs: OperationLogInput[] = []
  for (let index = 0; index < 90; index += 1) {
    const resource = resources[index % resources.length]
    const actor = index % 13 === 0 ? created.users.manager : index % 7 === 0 ? created.users.dev : index % 11 === 0 ? created.users.ops : created.users.admin
    const createdAt = new Date(Date.now() - Math.floor((index / 90) * 30 * dayMs)).toISOString()
    logs.push({
      id: `${idPrefix}operation_${String(index + 1).padStart(4, '0')}`,
      traceId: usageRecords[index * 5 % usageRecords.length]?.traceId,
      actorSystemAccountId: actor.id,
      actorUsername: actor.username,
      actorDisplayName: actor.displayName,
      actorRole: actor.role,
      operationScopeSystemAccountId: created.users.admin.id,
      mode: actor.id === created.users.admin.id ? 'self' : 'admin',
      module: resource.module,
      action: resource.action,
      operationKey: `mockdata.${resource.module}.${resource.action}`,
      resourceType: resource.resourceType,
      resourceId: resource.resourceId,
      resourceName: resource.resourceName,
      summary: `${namePrefix}${resource.summary}`,
      detailLevel: index % 5 === 0 ? 'summary' : 'full',
      visibilityScope: 'targeted',
      changes: [
        {
          field: 'status',
          label: '状态',
          before: index % 3 === 0 ? 'disabled' : 'draft',
          after: index % 3 === 0 ? 'active' : 'published'
        }
      ],
      metadata: {
        source: 'mockdata',
        batch: 'admin-full-business',
        index
      },
      method: index % 2 === 0 ? 'POST' : 'PATCH',
      path: `/__aisys__/api/mockdata/${resource.module}`,
      statusCode: index % 17 === 0 ? 409 : 200,
      clientIp: `10.30.0.${20 + index}`,
      userAgent: 'mockdata-admin/1.0',
      targets: [
        {
          targetType: resource.resourceType,
          targetId: resource.resourceId,
          targetName: resource.resourceName,
          targetOwnerSystemAccountId: created.users.admin.id,
          relation: 'primary'
        }
      ],
      viewers: [
        {
          systemAccountId: created.users.dev.id,
          visibilityReason: 'authorization_grantee',
          detailLevel: index % 3 === 0 ? 'summary' : 'full'
        },
        {
          systemAccountId: created.users.ops.id,
          visibilityReason: 'team_member',
          detailLevel: 'summary'
        }
      ],
      createdAt
    })
  }
  repositories.createOperationLogsBatch(logs)
}

export function createRuntimeLogMockdata(usageRecords: UsageRecordSeed[]): void {
  const recentRecords = usageRecords.slice(-240)
  const events = [
    'gateway_upstream_request_started',
    'gateway_upstream_response_received',
    'gateway_stream_finished_success',
    'gateway_upstream_attempt_failed',
    'background_usage_stats_aggregation_failed',
    'background_account_quality_refresh_completed',
    'db_service_started',
    'http_request_completed'
  ]
  const logs: RuntimeLogIndexInput[] = recentRecords.map((record, index) => {
    const level = record.success ? (index % 9 === 0 ? 'debug' : 'info') : (index % 5 === 0 ? 'error' : 'warn')
    const event = record.success ? events[index % 3] : events[3 + (index % 2)]
    const message = record.success
      ? `Mockdata 网关请求完成：${record.model}`
      : `Mockdata 网关请求失败：${record.errorCode}`
    return {
      id: `${idPrefix}runtime_${String(index + 1).padStart(4, '0')}`,
      logFile: join(backendRoot, 'logs', 'mockdata.log'),
      logOffset: index * 512,
      lineNumber: index + 1,
      time: new Date(Date.now() - Math.floor(((recentRecords.length - index) / recentRecords.length) * 3 * dayMs)).toISOString(),
      level,
      traceId: record.traceId,
      event,
      message,
      errorMessage: record.success ? undefined : record.errorMessage,
      rawJson: JSON.stringify({
        time: record.createdAt,
        level,
        event,
        traceId: record.traceId,
        message,
        mockdata: true,
        accountId: record.accountId,
        groupId: record.groupId,
        apiKeyId: record.apiKeyId
      }),
      createdAt: new Date(Date.now() - Math.floor(((recentRecords.length - index) / recentRecords.length) * 3 * dayMs)).toISOString()
    }
  })
  createRuntimeLogsBatch(logs)
}

function publicApiLogRequestData(
  endpoint: { method: string; path: string; scope: string },
  created: CreatedMockdata,
  index: number
): Record<string, unknown> {
  if (endpoint.method === 'GET') {
    return {
      query: endpoint.scope,
      page: 1 + (index % 5),
      pageSize: 20
    }
  }
  if (endpoint.path.includes('/group/')) {
    return {
      targetUsername: created.users.admin.username,
      providerCode,
      groupId: created.groups.main.id,
      name: `${namePrefix}公开接口分组 ${index % 9}`
    }
  }
  if (endpoint.path.includes('/api-key/')) {
    return {
      targetUsername: created.users.admin.username,
      apiKeyId: created.apiKeys.adminMain.id,
      groupBindings: [{ groupId: created.groups.main.id, priority: 1, status: 'active' }]
    }
  }
  return {
    targetUsername: created.users.admin.username,
    providerCode,
    accountId: created.accounts.primary.id,
    targetGroupName: `${namePrefix}公开接口账号分组`
  }
}

function publicApiLogResponseData(endpoint: { path: string; scope: string }, success: boolean, index: number): Record<string, unknown> {
  if (!success) {
    return {
      message: publicApiLogErrorMessage(publicApiLogStatus(index, endpoint.path)),
      code: publicApiLogErrorCode(publicApiLogStatus(index, endpoint.path))
    }
  }
  if (endpoint.path.includes('/ranking')) {
    return {
      source: 'stats',
      items: [
        { rank: 1, clientIp: `172.20.1.${20 + (index % 20)}`, totalCost: roundCost(4.2 + index / 1000) }
      ]
    }
  }
  if (endpoint.path.includes('/list')) {
    return {
      source: 'stats',
      page: 1,
      pageSize: 20,
      items: [{ id: `mockdata_public_item_${index}`, name: `${namePrefix}公开接口返回项` }]
    }
  }
  return {
    source: 'stats',
    action: endpoint.scope.includes('write') ? 'created' : 'read',
    mock: false
  }
}

import {
  builtInExternalIntegrationTestSourceId,
  builtInExternalIntegrationTestTokenId,
  findExternalIntegrationSource
} from '../../../../storage/external-integration-source.repository.js'
import type { OperationLogInput } from '../../../../storage/operation-logs.repository.js'
import { createPublicApiLog } from '../../../../storage/public-api-logs.repository.js'
import * as repositories from '../../../../storage/repositories.js'
import {
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
} from '../shared.js'

/**
 * F3 审计日志由 Go owner 接收并持久化。Node mockdata 不再直接写入审计表；
 * 需要审计样本时应通过 Go 输入端点注入。
 */
export function createAuditMockdata(_records: UsageRecordSeed[]): number {
  return 0
}

export function createPublicApiLogMockdata(created: CreatedMockdata, options: MockdataOptions): number {
  const endpoints = [
    { method: 'GET', path: '/__aipublic__/api-key/list', query: `targetUsername=${created.users.admin.username}`, scope: 'api_key_list' },
    { method: 'GET', path: '/__aipublic__/route-strategy/list', query: `targetUsername=${created.users.admin.username}&mode=all`, scope: 'route_strategy_list' },
    { method: 'GET', path: '/__aipublic__/group/list', query: `targetUsername=${created.users.admin.username}&providerCode=gpt`, scope: 'group_list' },
    { method: 'GET', path: '/__aipublic__/account/list', query: `targetUsername=${created.users.admin.username}&providerCode=gpt`, scope: 'account_list' },
    { method: 'POST', path: '/__aipublic__/api-key/add', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/api-key/update', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/api-key/del', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/route-strategy/add', query: '', scope: 'route_strategy_write' },
    { method: 'POST', path: '/__aipublic__/route-strategy/update', query: '', scope: 'route_strategy_write' },
    { method: 'POST', path: '/__aipublic__/route-strategy/del', query: '', scope: 'route_strategy_write' },
    { method: 'POST', path: '/__aipublic__/group/add', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/group/update', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/group/del', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/account/add', query: '', scope: 'account_write' },
    { method: 'POST', path: '/__aipublic__/account/update', query: '', scope: 'account_write' },
    { method: 'POST', path: '/__aipublic__/account/del', query: '', scope: 'account_write' }
  ]
  const perDay = Math.min(60, Math.max(12, Math.ceil(options.dailyRequests / 20)))
  const total = options.days * perDay
  const endAt = Date.now() - 20 * minuteMs
  const startAt = endAt - (options.days - 1) * dayMs
  const builtInTestSource = findExternalIntegrationSource(builtInExternalIntegrationTestSourceId)
  const builtInTestToken = builtInTestSource?.tokens?.find((token) => token.id === builtInExternalIntegrationTestTokenId)
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
      routeStrategyId: created.apiKeys.adminMain.routeStrategyId
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

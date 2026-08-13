import {
  builtInExternalIntegrationTestSourceId,
  builtInExternalIntegrationTestTokenId,
  findExternalIntegrationSource
} from '../../../../storage/external-integration-source.repository.js'
import { createPublicApiLog } from '../../../../storage/public-api-logs.repository.js'
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

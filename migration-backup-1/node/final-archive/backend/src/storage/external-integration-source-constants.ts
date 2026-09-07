import { randomBytes } from 'node:crypto'

import { hashSecret } from './crypto.js'

export const externalIntegrationGroupListReadScope = 'juhe_ai_public:group_list:read'
export const externalIntegrationRouteStrategyListReadScope = 'juhe_ai_public:route_strategy_list:read'
export const externalIntegrationApiKeyListReadScope = 'juhe_ai_public:api_key_list:read'
export const externalIntegrationAccountListReadScope = 'juhe_ai_public:account_list:read'
export const externalIntegrationGroupAddWriteScope = 'juhe_ai_public:group_add:write'
export const externalIntegrationGroupUpdateWriteScope = 'juhe_ai_public:group_update:write'
export const externalIntegrationGroupDeleteWriteScope = 'juhe_ai_public:group_delete:write'
export const externalIntegrationRouteStrategyAddWriteScope = 'juhe_ai_public:route_strategy_add:write'
export const externalIntegrationRouteStrategyUpdateWriteScope = 'juhe_ai_public:route_strategy_update:write'
export const externalIntegrationRouteStrategyDeleteWriteScope = 'juhe_ai_public:route_strategy_delete:write'
export const externalIntegrationApiKeyAddWriteScope = 'juhe_ai_public:api_key_add:write'
export const externalIntegrationApiKeyUpdateWriteScope = 'juhe_ai_public:api_key_update:write'
export const externalIntegrationApiKeyDeleteWriteScope = 'juhe_ai_public:api_key_delete:write'
export const externalIntegrationAccountAddWriteScope = 'juhe_ai_public:account_add:write'
export const externalIntegrationAccountUpdateWriteScope = 'juhe_ai_public:account_update:write'
export const externalIntegrationAccountDeleteWriteScope = 'juhe_ai_public:account_delete:write'

export const externalIntegrationScopeOptions = [
  { value: externalIntegrationApiKeyListReadScope, label: 'GET API Key 列表' },
  { value: externalIntegrationRouteStrategyListReadScope, label: 'GET 路由策略列表' },
  { value: externalIntegrationGroupListReadScope, label: 'GET 分组列表' },
  { value: externalIntegrationAccountListReadScope, label: 'GET 账号列表' },
  { value: externalIntegrationApiKeyAddWriteScope, label: 'POST API Key 新增' },
  { value: externalIntegrationApiKeyUpdateWriteScope, label: 'POST API Key 修改' },
  { value: externalIntegrationApiKeyDeleteWriteScope, label: 'POST API Key 删除' },
  { value: externalIntegrationRouteStrategyAddWriteScope, label: 'POST 路由策略新增' },
  { value: externalIntegrationRouteStrategyUpdateWriteScope, label: 'POST 路由策略修改' },
  { value: externalIntegrationRouteStrategyDeleteWriteScope, label: 'POST 路由策略删除' },
  { value: externalIntegrationGroupAddWriteScope, label: 'POST 分组新增' },
  { value: externalIntegrationGroupUpdateWriteScope, label: 'POST 分组修改' },
  { value: externalIntegrationGroupDeleteWriteScope, label: 'POST 分组删除' },
  { value: externalIntegrationAccountAddWriteScope, label: 'POST 账号新增' },
  { value: externalIntegrationAccountUpdateWriteScope, label: 'POST 账号修改' },
  { value: externalIntegrationAccountDeleteWriteScope, label: 'POST 账号删除' }
] as const

export const builtInExternalIntegrationTestSourceId = 'extsrc_builtin_test'
export const builtInExternalIntegrationTestTokenId = 'exttok_builtin_test'
export const builtInExternalIntegrationTestSourceName = '内置测试来源'
export const builtInExternalIntegrationTestTokenName = '内置测试 Token'
export const builtInExternalIntegrationTestRateLimits = [{ windowSeconds: 60, maxRequests: 10 }] as const
export const builtInExternalIntegrationTestTokenNotes = '系统内置测试 Token，只返回 mock 数据；可停用或重置，不支持编辑或删除。'

const generatedTokenPrefix = 'juis_'

export function isBuiltInExternalIntegrationTestSourceId(id: string | undefined): boolean {
  return id === builtInExternalIntegrationTestSourceId
}

export function isBuiltInExternalIntegrationTestTokenId(id: string | undefined): boolean {
  return id === builtInExternalIntegrationTestTokenId
}

export function createExternalIntegrationSourceTokenValue(): string {
  return `${generatedTokenPrefix}${randomBytes(32).toString('base64url')}`
}

export function hashExternalIntegrationSourceTokenValue(token: string): string {
  return hashSecret(`external-integration-source-token:${token}`)
}

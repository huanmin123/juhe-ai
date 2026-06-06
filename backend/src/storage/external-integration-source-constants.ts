import { randomBytes } from 'node:crypto'

import { hashSecret } from './crypto.js'

export const externalIntegrationSourceAuthDemoScope = 'external_integrations:source_auth_demo:read'
export const externalIntegrationIpUsageReadScope = 'juhe_ai_public:ip_usage:read'
export const externalIntegrationAccountUsageReadScope = 'juhe_ai_public:account_usage:read'
export const externalIntegrationConsumptionRankingReadScope = 'juhe_ai_public:consumption_ranking:read'
export const externalIntegrationAccessInfoReadScope = 'juhe_ai_public:access_info:read'
export const externalIntegrationGroupListReadScope = 'juhe_ai_public:group_list:read'
export const externalIntegrationApiKeyListReadScope = 'juhe_ai_public:api_key_list:read'
export const externalIntegrationAccountListReadScope = 'juhe_ai_public:account_list:read'
export const externalIntegrationGroupAddWriteScope = 'juhe_ai_public:group_add:write'
export const externalIntegrationGroupUpdateWriteScope = 'juhe_ai_public:group_update:write'
export const externalIntegrationGroupDeleteWriteScope = 'juhe_ai_public:group_delete:write'
export const externalIntegrationApiKeyAddWriteScope = 'juhe_ai_public:api_key_add:write'
export const externalIntegrationApiKeyUpdateWriteScope = 'juhe_ai_public:api_key_update:write'
export const externalIntegrationApiKeyDeleteWriteScope = 'juhe_ai_public:api_key_delete:write'
export const externalIntegrationAccountAddWriteScope = 'juhe_ai_public:account_add:write'
export const externalIntegrationAccountUpdateWriteScope = 'juhe_ai_public:account_update:write'
export const externalIntegrationAccountDeleteWriteScope = 'juhe_ai_public:account_delete:write'

export const externalIntegrationScopeOptions = [
  { value: externalIntegrationSourceAuthDemoScope, label: 'GET 来源鉴权 Demo' },
  { value: externalIntegrationIpUsageReadScope, label: 'GET IP 维度消费聚合' },
  { value: externalIntegrationAccountUsageReadScope, label: 'GET 账号维度实际消耗聚合' },
  { value: externalIntegrationConsumptionRankingReadScope, label: 'GET IP 维度消耗排行' },
  { value: externalIntegrationAccessInfoReadScope, label: 'GET 公益接入信息' },
  { value: externalIntegrationGroupListReadScope, label: 'GET 分组列表' },
  { value: externalIntegrationApiKeyListReadScope, label: 'GET API Key 列表' },
  { value: externalIntegrationAccountListReadScope, label: 'GET 账号列表' },
  { value: externalIntegrationGroupAddWriteScope, label: 'POST 分组新增' },
  { value: externalIntegrationGroupUpdateWriteScope, label: 'POST 分组修改' },
  { value: externalIntegrationGroupDeleteWriteScope, label: 'POST 分组删除' },
  { value: externalIntegrationApiKeyAddWriteScope, label: 'POST API Key 新增' },
  { value: externalIntegrationApiKeyUpdateWriteScope, label: 'POST API Key 修改' },
  { value: externalIntegrationApiKeyDeleteWriteScope, label: 'POST API Key 删除' },
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

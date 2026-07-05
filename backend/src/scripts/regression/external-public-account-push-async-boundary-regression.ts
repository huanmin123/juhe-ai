import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const routesSource = readFileSync(new URL('../../modules/external-integrations/external-integrations.routes.ts', import.meta.url), 'utf8')
const serviceSource = readFileSync(new URL('../../modules/external-integrations/external-public-account-push.service.ts', import.meta.url), 'utf8')
const routeStrategyServiceSource = readFileSync(new URL('../../modules/external-integrations/external-public-route-strategy.service.ts', import.meta.url), 'utf8')
const targetSource = readFileSync(new URL('../../modules/external-integrations/external-public-account-push.target.ts', import.meta.url), 'utf8')
const apiKeyCleanupSource = readFileSync(new URL('../../modules/api-keys/api-key-cleanup.service.ts', import.meta.url), 'utf8')

const requiredRouteTokens = [
  'createOperationLogAsync',
  'listPublicGroupsAsync',
  'listPublicRouteStrategiesAsync',
  'listPublicApiKeysAsync',
  'listPublicWelfareAccountsAsync',
  'addPublicGroupAsync',
  'addPublicWelfareAccountAsync',
  'addPublicRouteStrategyAsync',
  'updatePublicRouteStrategyAsync',
  'deletePublicRouteStrategyAsync',
  'addPublicApiKeyAsync',
  'updatePublicApiKeyAsync',
  'deletePublicApiKeyAsync',
  'updatePublicGroupAsync',
  'deletePublicGroupAsync',
  'updatePublicWelfareAccountAsync',
  'deletePublicWelfareAccountAsync',
  'await recordPublicWelfareAccountWriteOperation',
  'await recordPublicWelfareAccountDeleteOperation'
]

for (const token of requiredRouteTokens) {
  assert(routesSource.includes(token), `公开推送路由必须使用 async 边界：${token}`)
}

for (const token of [
  'listPublicGroups',
  'listPublicRouteStrategies',
  'listPublicApiKeys',
  'listPublicWelfareAccounts',
  'addPublicGroup',
  'addPublicWelfareAccount',
  'addPublicRouteStrategy',
  'updatePublicRouteStrategy',
  'deletePublicRouteStrategy',
  'addPublicApiKey',
  'updatePublicApiKey',
  'deletePublicApiKey',
  'updatePublicGroup',
  'deletePublicGroup',
  'updatePublicWelfareAccount',
  'deletePublicWelfareAccount'
]) {
  assert.doesNotMatch(routesSource, new RegExp(`\\b${token}\\(`), `公开推送路由不能直接调用同步服务：${token}`)
}
assert.doesNotMatch(routesSource, /\bcreateOperationLog\(/, '公开推送操作日志不能回退同步写入')

const requiredServiceTokens = [
  'export async function addPublicGroupAsync',
  'export async function addPublicWelfareAccountAsync',
  'export async function updatePublicGroupAsync',
  'export async function deletePublicGroupAsync',
  'export async function listPublicGroupsAsync',
  'export async function addPublicApiKeyAsync',
  'export async function updatePublicApiKeyAsync',
  'export async function deletePublicApiKeyAsync',
  'export async function listPublicApiKeysAsync',
  'export async function deletePublicWelfareAccountAsync',
  'export async function listPublicWelfareAccountsAsync',
  'createAccountAsync',
  'createApiKeyRecordAsync',
  'createGroupAsync',
  'deleteAccountWithRelatedCleanupAsync',
  'deleteApiKeyWithRelatedCleanupAsync',
  'deleteGroupAsync',
  'findAccountSummaryAsync',
  'findApiKeySummaryAsync',
  'findGroupSummaryAsync',
  'listAccountsPageAsync',
  'listApiKeysPageAsync',
  'listGroupsPageAsync',
  'listProvidersAsync',
  'updateAccountAsync',
  'updateApiKeyAsync',
  'updateGroupAsync',
  'findPublicAccountOwnerByIdAsync',
  'findPublicGroupOwnerByIdAsync',
  'findPublicApiKeyOwnerByIdAsync',
  'findTargetAccountByIdAsync',
  'findTargetAccountAsync',
  'resolvePublicAccountGroupFilterAsync',
  'juhe_business.accounts',
  'juhe_business.groups',
  'juhe_business.api_keys',
  'juhe_business.group_accounts'
]

for (const token of requiredServiceTokens) {
  assert(serviceSource.includes(token), `公开推送服务必须固定 async/PG 路径：${token}`)
}

for (const token of [
  'export async function listPublicRouteStrategiesAsync',
  'export async function addPublicRouteStrategyAsync',
  'export async function updatePublicRouteStrategyAsync',
  'export async function deletePublicRouteStrategyAsync',
  'createRouteStrategyAsync',
  'updateRouteStrategyAsync',
  'deleteRouteStrategyAsync',
  'findRouteStrategySummaryAsync',
  'listRouteStrategiesPageAsync',
  'requirePublicTargetAsync',
  'resolvePublicOwnedResourceTargetAsync'
]) {
  assert(routeStrategyServiceSource.includes(token), `公开路由策略服务必须固定 async/PG 路径：${token}`)
}

for (const token of [
  'ensureTargetSystemAccountAsync',
  'ensureTargetGroupAsync',
  'findPublicTargetAsync',
  'requirePublicTargetAsync',
  'resolvePublicOwnedResourceTargetAsync',
  'resolvePublicGroupAsync',
  'resolveAccountListGroupIdAsync',
  'findExistingTargetGroupAsync'
]) {
  assert(targetSource.includes(token), `公开推送目标解析必须提供 async helper：${token}`)
}

assert(apiKeyCleanupSource.includes("runtimeConfig.databaseDriver !== 'postgres'"), '仅单机模式 API Key 删除才登记本地 dataset 清理目标')
assert(!apiKeyCleanupSource.includes('postgres_record_cleanup_not_supported'), 'PG 模式 API Key 删除不能再返回固定清理跳过原因')
assert(apiKeyCleanupSource.includes('enqueueRecordMaintenanceJobWithResult(job)'), 'PG 模式 API Key 删除仍必须投递记录维护清理任务')

console.log('公开账号推送 async 边界回归通过：公开账号、分组、路由策略、API Key 与操作日志均固定 async/PG 路径')

import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  assertAccountGptRequestOverridesSupported,
  assertAccountGptRequestOverridesSupportedAsync
} from '../../modules/accounts/account-gpt-request-overrides.validation.js'
import {
  PublicAccountUpdateConflictError,
  rethrowStalePublicAccountUpdateValidationError,
  retryPublicAccountUpdateAfterConfigConflict
} from '../../modules/external-integrations/external-public-account-push.service.js'
import { AccountConfigRevisionConflictError } from '../../storage/repositories.js'

const routesSource = readFileSync(new URL('../../modules/external-integrations/external-integrations.routes.ts', import.meta.url), 'utf8')
const serviceSource = readFileSync(new URL('../../modules/external-integrations/external-public-account-push.service.ts', import.meta.url), 'utf8')
const repositoriesSource = readFileSync(new URL('../../storage/repositories.ts', import.meta.url), 'utf8')
const routeStrategyServiceSource = readFileSync(new URL('../../modules/external-integrations/external-public-route-strategy.service.ts', import.meta.url), 'utf8')
const targetSource = readFileSync(new URL('../../modules/external-integrations/external-public-account-push.target.ts', import.meta.url), 'utf8')
const apiKeyCleanupSource = readFileSync(new URL('../../modules/api-keys/api-key-cleanup.service.ts', import.meta.url), 'utf8')

const noGptOverridesInput = {
  providerCode: 'non_gpt_no_override_regression',
  accountType: 'api_key' as const,
  credentials: {},
  supportedModels: ['catalog-must-not-be-read'],
  systemAccountId: 'owner-must-not-be-read'
}
assert.doesNotThrow(
  () => assertAccountGptRequestOverridesSupported(noGptOverridesInput),
  '同步 GPT 覆盖 helper 在无覆盖时必须快速返回'
)
await assert.doesNotReject(
  () => assertAccountGptRequestOverridesSupportedAsync(noGptOverridesInput),
  '异步 GPT 覆盖 helper 在无覆盖时必须快速返回'
)

let retryAttempts = 0
const retryResult = await retryPublicAccountUpdateAfterConfigConflict(async () => {
  retryAttempts += 1
  if (retryAttempts < 3) {
    throw new AccountConfigRevisionConflictError('account-retry-regression', retryAttempts)
  }
  return 'updated'
})
assert.equal(retryResult, 'updated', '公开账号异步修改应在 revision 冲突后重新执行完整更新')
assert.equal(retryAttempts, 3, '公开账号异步修改最多第三次成功')

let exhaustedRetryAttempts = 0
await assert.rejects(
  retryPublicAccountUpdateAfterConfigConflict(async () => {
    exhaustedRetryAttempts += 1
    throw new AccountConfigRevisionConflictError('account-conflict-regression', exhaustedRetryAttempts)
  }),
  (error: unknown) => error instanceof PublicAccountUpdateConflictError
    && error.revisionConflict instanceof AccountConfigRevisionConflictError,
  '连续 revision 冲突必须有界终止并返回明确错误'
)
assert.equal(exhaustedRetryAttempts, 3, '连续 revision 冲突只能尝试三次')

const staleValidationError = new Error('基于陈旧 existing 的校验错误')
await assert.rejects(
  rethrowStalePublicAccountUpdateValidationError(staleValidationError, {
    accountId: 'account-stale-validation-regression',
    expectedConfigRevision: 4,
    readCurrentConfigRevision: async () => 5
  }),
  (error: unknown) => error instanceof AccountConfigRevisionConflictError
    && error.expectedConfigRevision === 4
    && error.actualConfigRevision === 5,
  '基于陈旧 existing 的校验错误必须转换为 revision 冲突以触发完整重试'
)

await assert.rejects(
  rethrowStalePublicAccountUpdateValidationError(new Error('校验期间账号被删除或不可见'), {
    accountId: 'account-missing-during-validation-regression',
    expectedConfigRevision: 6,
    readCurrentConfigRevision: async () => undefined
  }),
  (error: unknown) => error instanceof AccountConfigRevisionConflictError
    && error.expectedConfigRevision === 6
    && error.actualConfigRevision === undefined,
  '校验期间账号被删除或不可见也必须转换为 stale revision 冲突'
)

const directRevisionConflict = new AccountConfigRevisionConflictError('account-direct-conflict-regression', 7, 8)
let directConflictRevisionReads = 0
await assert.rejects(
  rethrowStalePublicAccountUpdateValidationError(directRevisionConflict, {
    accountId: directRevisionConflict.accountId,
    expectedConfigRevision: directRevisionConflict.expectedConfigRevision,
    readCurrentConfigRevision: async () => {
      directConflictRevisionReads += 1
      return directRevisionConflict.actualConfigRevision
    }
  }),
  (error: unknown) => error === directRevisionConflict,
  '专用 revision 冲突必须直接重抛'
)
assert.equal(directConflictRevisionReads, 0, '专用 revision 冲突不得重复读取 summary')

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
  'PublicAccountUpdateConflictError',
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
  'createAccountInClientAsync',
  'createApiKeyRecordAsync',
  'createGroupInClientAsync',
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
  'assertAccountGptRequestOverridesSupported',
  'assertAccountGptRequestOverridesSupportedAsync',
  'finalPublicAccountGptOverrideValidationInput',
  'updateAccountAsync',
  'updateApiKeyAsync',
  'updateGroupAsync',
  'findPublicAccountOwnerByIdAsync',
  'findPublicGroupOwnerByIdAsync',
  'findPublicApiKeyOwnerByIdAsync',
  'findTargetAccountByIdAsync',
  'findTargetAccountAsync',
  'ensureTargetSystemAccountInClientAsync',
  'ensureTargetGroupInClientAsync',
  'findExistingTargetGroupInClientAsync',
  'resolvePublicAccountGroupFilterAsync',
  'juhe_business.accounts',
  'juhe_business.groups',
  'juhe_business.api_keys',
  'juhe_business.group_accounts'
]

for (const token of requiredServiceTokens) {
  assert(serviceSource.includes(token), `公开推送服务必须固定 async/PG 路径：${token}`)
}

assert.match(
  serviceSource,
  /async function writePublicWelfareAccountAsync[\s\S]*?client\.transaction\(async \(tx\)[\s\S]*?ensureTargetSystemAccountInClientAsync\(tx[\s\S]*?ensureTargetGroupInClientAsync\(tx[\s\S]*?createAccountInClientAsync\(tx/,
  '公开账号新增 PG 路径必须在同一个事务内创建目标用户、目标分组和账号'
)
assert.match(
  serviceSource,
  /export async function addPublicGroupAsync[\s\S]*?client\.transaction\(async \(tx\)[\s\S]*?ensureTargetSystemAccountInClientAsync\(tx[\s\S]*?findExistingTargetGroupInClientAsync\(tx[\s\S]*?createGroupInClientAsync\(tx/,
  '公开分组新增 PG 路径必须在同一个事务内创建目标用户和分组'
)
assert.match(
  serviceSource,
  /function updatePublicWelfareAccountById\([\s\S]*?const payload = accountPartialUpdateInputForPush\(input, existing\)[\s\S]*?assertAccountGptRequestOverridesSupported\([\s\S]*?finalPublicAccountGptOverrideValidationInput\(existing, payload, target\.account\.id\)[\s\S]*?const updated = updateAccount\(existing\.id, payload, access\)/,
  '公开账号同步修改必须使用目标 owner 校验最终 GPT 覆盖，并在校验通过后才写入账户'
)
assert.match(
  serviceSource,
  /async function updatePublicWelfareAccountByIdAsync\([\s\S]*?retryPublicAccountUpdateAfterConfigConflict\(async \(\) => \{[\s\S]*?findAccountSummaryAsync\(accountId, access\)[\s\S]*?preparePublicAccountUpdateAttemptAsync\([\s\S]*?updateAccountAsync\(existing\.id, payload, access, \{[\s\S]*?expectedConfigRevision[\s\S]*?\}\)/,
  '公开账号异步修改每次重试都必须重新读取、重建和校验，并把目标 revision 传给 repository'
)
assert.match(
  serviceSource,
  /async function preparePublicAccountUpdateAttemptAsync\([\s\S]*?const expectedConfigRevision = existing\.configRevision[\s\S]*?accountPartialUpdateInputForPush\(input, existing\)[\s\S]*?assertAccountGptRequestOverridesSupportedAsync\([\s\S]*?catch \(error\)[\s\S]*?rethrowStalePublicAccountUpdateValidationError/,
  '公开账号异步准备失败必须在返回业务校验错误前检查 existing revision 是否陈旧'
)
assert.match(
  serviceSource,
  /function rethrowStalePublicAccountUpdateValidationError[\s\S]*?error instanceof AccountConfigRevisionConflictError[\s\S]*?throw error[\s\S]*?const actualConfigRevision = await input\.readCurrentConfigRevision\(\)[\s\S]*?if \(actualConfigRevision !== input\.expectedConfigRevision\)[\s\S]*?throw new AccountConfigRevisionConflictError/,
  '专用 revision 冲突必须直接重抛，summary revision 变化或账号消失都必须转换为冲突'
)
assert.doesNotMatch(
  serviceSource,
  /actualConfigRevision !== undefined && actualConfigRevision !== input\.expectedConfigRevision/,
  '账号删除或不可见返回 undefined 时不能回退旧校验错误'
)
assert.match(
  serviceSource,
  /function finalPublicAccountGptOverrideValidationInput\([\s\S]*?\{ \.\.\.existing, \.\.\.payload \}[\s\S]*?credentials: finalAccount\.credentials[\s\S]*?supportedModels: Array\.isArray\(finalAccount\.supportedModels\)/,
  '公开账号 GPT 覆盖校验必须从 existing 与最终 payload 合并态读取 credentials 和 supportedModels'
)
assert.match(
  serviceSource,
  /const publicAccountUpdateMaxAttempts = 3[\s\S]*?function retryPublicAccountUpdateAfterConfigConflict[\s\S]*?error instanceof AccountConfigRevisionConflictError[\s\S]*?attempt === publicAccountUpdateMaxAttempts[\s\S]*?throw new PublicAccountUpdateConflictError\(error\)/,
  '公开账号 revision 冲突重试必须固定为三次上限，并在耗尽后转换为公开领域冲突'
)

assert.match(
  routesSource,
  /'\/account\/update'[\s\S]*?error instanceof PublicAccountUpdateConflictError[\s\S]*?res\.status\(409\)\.json\(badRequest\(error\.message\)\)/,
  '公开账号并发冲突必须按专用错误类型明确返回 HTTP 409'
)

assert.match(
  repositoriesSource,
  /export interface UpdateAccountAsyncOptions[\s\S]*?expectedConfigRevision\?: number[\s\S]*?export async function updateAccountAsync\([\s\S]*?options\?: UpdateAccountAsyncOptions/,
  '异步账户更新必须提供可选 expectedConfigRevision options，保持旧调用兼容'
)
assert.match(
  repositoriesSource,
  /export async function updateAccountAsync\([\s\S]*?const expectedConfigRevision = options\?\.expectedConfigRevision[\s\S]*?账户配置版本无效[\s\S]*?runtimeConfig\.databaseDriver !== 'postgres'/,
  '异步账户更新必须在 driver 分支前统一校验 expected revision options'
)
assert.match(
  repositoriesSource,
  /runtimeConfig\.databaseDriver !== 'postgres'[\s\S]*?expectedConfigRevision === undefined[\s\S]*?return updateAccount\(id, input, access\)[\s\S]*?runInDatabaseTransaction\(\(\) => \{[\s\S]*?findAccountSummary\(id, access\)[\s\S]*?currentConfigRevision !== expectedConfigRevision[\s\S]*?AccountConfigRevisionConflictError[\s\S]*?return updateAccount\(id, input, access\)/,
  'SQLite 有 expected revision 时必须在同一同步事务中检查 current 并调用 updateAccount，无 expected 保持旧路径'
)
assert.match(
  repositoriesSource,
  /const currentConfigRevision = current\.configRevision \?\? 1[\s\S]*?currentConfigRevision !== expectedConfigRevision[\s\S]*?throw new AccountConfigRevisionConflictError/,
  'PG 异步账户更新必须在读取 current 后立即校验 expected revision'
)
assert.match(
  repositoriesSource,
  /const expectedConfigRevisionClause =[\s\S]*?' AND config_revision = \?'[\s\S]*?UPDATE \$\{accountWriteTable\(tx, 'accounts'\)\}[\s\S]*?\$\{expectedConfigRevisionClause\}[\s\S]*?expectedConfigRevision === undefined \? \[\] : \[expectedConfigRevision\]/,
  'PG 主账户 UPDATE 必须把 expected revision 作为精确 WHERE 条件和绑定参数'
)
assert.match(
  repositoriesSource,
  /if \(result\.changes !== 1\)[\s\S]*?throw new AccountConfigRevisionConflictError\(id, expectedConfigRevision\)[\s\S]*?if \(!updated\) return undefined[\s\S]*?refreshGroupAccountStatsAfterWriteAsync/,
  'PG revision 条件未命中必须抛冲突，普通零命中必须返回 undefined，不能继续伪成功副作用'
)

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
  'findExistingTargetGroupAsync',
  'ensureTargetSystemAccountInClientAsync',
  'ensureTargetGroupInClientAsync',
  'findExistingTargetGroupInClientAsync'
]) {
  assert(targetSource.includes(token), `公开推送目标解析必须提供 async helper：${token}`)
}

assert(apiKeyCleanupSource.includes("runtimeConfig.databaseDriver !== 'postgres'"), '仅单机模式 API Key 删除才登记本地 dataset 清理目标')
assert(!apiKeyCleanupSource.includes('postgres_record_cleanup_not_supported'), 'PG 模式 API Key 删除不能再返回固定清理跳过原因')
assert(apiKeyCleanupSource.includes('enqueueRecordMaintenanceJobWithResult(job)'), 'PG 模式 API Key 删除仍必须投递记录维护清理任务')

console.log('公开账号推送 async 边界回归通过：公开账号、分组、路由策略、API Key 与操作日志均固定 async/PG 路径')

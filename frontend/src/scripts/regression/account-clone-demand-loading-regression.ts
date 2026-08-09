import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountCloneContext } from '../../types/domain'
import { buildAccountCloneFormLoad } from '../../views/accounts/accountEditFormLoaders'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import { buildAccountSavePayload } from '../../views/accounts/accountSavePayload'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../../..')
const editFormSource = readSource('src/views/accounts/useAccountEditForm.ts')
const accountApiSource = readSource('src/api/domains/accounts.ts')
const accountTypesSource = readSource('src/types/domain/accounts.ts')
const loaderSource = readSource('src/views/accounts/accountEditFormLoaders.ts')
const openCloneSource = sourceBetween(editFormSource, 'async function openClone(', 'async function loadAccountDetailForForm(')
const basicLoaderSource = sourceBetween(loaderSource, 'function buildAccountBasicFormPatch', 'export function buildAccountCloneFormLoad')
const cloneLoaderSource = sourceBetween(loaderSource, 'export function buildAccountCloneFormLoad', 'function accountClientCompatibilityForForm')
const cloneContextTypeSource = sourceBetween(
  accountTypesSource,
  'export interface AccountCloneContext',
  'export interface AccountCloneCredentialOptions'
)
const cloneOptionsTypeSource = sourceBetween(
  accountTypesSource,
  'export interface AccountCloneCredentialOptions',
  'export interface AccountMutationResult'
)

assert.match(
  openCloneSource,
  /isManagementView\.value\s*\?\s*await api\.accounts\.cloneContext\([\s\S]*:\s*await api\.myAccounts\.cloneContext\(/,
  '管理侧和用户侧克隆都必须只调用专用 clone-context'
)
assert.doesNotMatch(
  openCloneSource,
  /Promise\.all|editBasicDetail|advancedDetail|loadAccountDetailForForm|loadAccountOptions|loadGroupOptions|loadAccountTagOptions|loadCurrentProviderModelOptions|apiKeyRuntime/,
  '克隆首开不得拼接详情或预取下拉与运行态'
)
assert.match(accountApiSource, /cloneContext:[\s\S]*\/accounts\/\$\{id\}\/clone-context/)
assert.match(accountApiSource, /cloneContext:[\s\S]*\/my-accounts\/\$\{id\}\/clone-context/)
assert.match(
  openCloneSource,
  /sourceAccount\.boundGroupId\s*\?\?\s*options\.groupIdForAccount\(account\.id\)/,
  '克隆上下文未返回分组时必须回退到来源账户列表分组'
)
assert.match(cloneContextTypeSource, /credentialOptions: AccountCloneCredentialOptions/)
assert.match(cloneContextTypeSource, /status: AccountStatus/)
assert.match(cloneContextTypeSource, /balanceQueryEnabled: boolean/)
assert.match(cloneContextTypeSource, /balanceQueryConfig\?: AccountBalanceQueryConfig/)
assert.doesNotMatch(cloneContextTypeSource, /\bcredentials\b|usage|runtime|permissions|authorizationSources/)
assert.doesNotMatch(
  cloneOptionsTypeSource,
  /access_token|refresh_token|client_secret|password/,
  '克隆凭据选项类型不得容纳建号秘密'
)
assert.match(loaderSource, /const credentials = account\.credentialOptions/)
assert.match(basicLoaderSource, /apiKeyStrategy:\s*parseAccountApiKeyStrategy\(credentials\)/, '编辑 loader 必须使用统一 API Key 策略解析')
assert.match(cloneLoaderSource, /apiKeyStrategy:\s*parseAccountApiKeyStrategy\(credentials\)/, '克隆 loader 必须使用统一 API Key 策略解析')
assert.equal((loaderSource.match(/function parseAccountApiKeyStrategy\(/g) ?? []).length, 1, 'API Key 策略解析函数只能定义一处')

const defaults = defaultAccountForm('gpt', 'api_key', FALLBACK_PROVIDERS)
const context: AccountCloneContext = {
  id: 'account-clone-source',
  configRevision: 7,
  providerCode: 'gpt',
  providerProtocolProfileId: defaults.providerProtocolProfileId,
  protocolCode: 'openai',
  protocolVersion: 'v1',
  name: '克隆来源账户',
  notes: '只复制表单配置',
  type: 'api_key',
  status: 'active',
  credentialOptions: {
    api_key_count: 2,
    api_key_strategy: 'weighted_round_robin',
    api_key_weights: [3, 7],
    base_url: 'https://api.openai.com/v1',
    supported_endpoint_modes: ['responses_json', 'responses_sse'],
    service_tier_override: 'priority',
    reasoning_effort_override: 'high',
    client_id: 'clone-client-id',
    quota_project_id: 'clone-quota-project',
    oauth_type: 'ai_studio',
    tier_id: 'aistudio_paid',
    project_id: 'clone-project-id'
  },
  concurrencyLimit: 12,
  priority: 3,
  superPriorityEnabled: false,
  fallbackEnabled: true,
  clientCompatibility: 'codex_responses',
  supportedModels: ['gpt-5.4'],
  tags: [{ id: 'tag-clone', name: '生产' }],
  healthCheckModel: 'gpt-5.4',
  healthCheckEndpointMode: 'responses_sse',
  boundGroupId: 'group-clone-source',
  boundGroupName: '克隆来源分组',
  modelMappings: [],
  temporaryUnavailableContinuousProbeEnabled: true,
  balanceQueryEnabled: true,
  balanceQueryConfig: {
    adapter: 'custom',
    intervalMinutes: 8,
    custom: {
      path: '/balance',
      remainingPointer: '/data/remaining',
      divisor: '100'
    }
  }
}
const loaded = buildAccountCloneFormLoad({
  account: context,
  defaults,
  selectedGroup: { id: context.boundGroupId!, name: context.boundGroupName! }
})
assert.equal(loaded.patch.baseUrl, 'https://api.openai.com/v1')
assert.equal(loaded.patch.apiKey, '')
assert.deepEqual(loaded.patch.apiKeys, ['', ''])
assert.equal(loaded.patch.apiKeyStrategy, 'weighted_round_robin')
assert.deepEqual(loaded.patch.apiKeyWeights, [3, 7])
assert.equal(loaded.patch.accessToken, '')
assert.equal(loaded.patch.refreshToken, '')
assert.equal(loaded.patch.googleClientId, 'clone-client-id')
assert.equal(loaded.patch.googleClientSecret, '')
assert.equal(loaded.patch.googleQuotaProjectId, 'clone-quota-project')
assert.equal(loaded.patch.oauthType, 'ai_studio')
assert.equal(loaded.patch.tierId, 'aistudio_paid')
assert.equal(loaded.patch.projectId, 'clone-project-id')
assert.equal(loaded.patch.status, 'active')
assert.equal(loaded.patch.serviceTierOverride, 'priority')
assert.equal(loaded.patch.reasoningEffortOverride, 'high')
assert.equal(loaded.patch.balanceQueryEnabled, true)
assert.equal(loaded.patch.balanceQueryAdapter, 'custom')
assert.equal(loaded.patch.balanceQueryIntervalMinutes, 8)
assert.equal(loaded.patch.balanceQueryCustomPath, '/balance')
assert.equal(loaded.patch.balanceQueryRemainingPointer, '/data/remaining')
assert.equal(loaded.patch.balanceQueryDivisor, '100')
assert.equal(loaded.patch.groupId, 'group-clone-source')
assert.deepEqual(loaded.patch.group, { id: 'group-clone-source', name: '克隆来源分组' })
loaded.patch.apiKeys = ['clone-key-a', 'clone-key-b']
const savePayload = buildAccountSavePayload({
  accounts: [],
  form: loaded.patch,
  errorPolicyRules: loaded.errorPolicyRules,
  responseInspectionRules: loaded.responseInspectionRules
})
assert.equal(savePayload.groupId, 'group-clone-source', '克隆保存请求必须携带来源分组')
assert.equal(savePayload.status, 'active', '克隆保存请求必须携带来源状态')
assert.equal(savePayload.credentials.api_key_strategy, 'weighted_round_robin', '克隆保存请求必须携带 API Key 池策略')
assert.deepEqual(savePayload.credentials.api_key_weights, [3, 7], '克隆保存请求必须携带 API Key 池权重')
assert.equal(savePayload.balanceQueryEnabled, false, '多个 API Key 的余额查询必须遵守现有自动停用规则')

const roundRobinContext: AccountCloneContext = {
  ...context,
  id: 'account-clone-round-robin-source',
  credentialOptions: {
    ...context.credentialOptions,
    api_key_strategy: 'round_robin'
  }
}
const roundRobinLoaded = buildAccountCloneFormLoad({
  account: roundRobinContext,
  defaults,
  selectedGroup: { id: context.boundGroupId!, name: context.boundGroupName! }
})
assert.equal(roundRobinLoaded.patch.apiKeyStrategy, 'round_robin', '克隆表单必须回显轮询策略')
roundRobinLoaded.patch.apiKeys = ['round-robin-key-a', 'round-robin-key-b']
const roundRobinSavePayload = buildAccountSavePayload({
  accounts: [],
  form: roundRobinLoaded.patch,
  errorPolicyRules: roundRobinLoaded.errorPolicyRules,
  responseInspectionRules: roundRobinLoaded.responseInspectionRules
})
assert.equal(roundRobinSavePayload.credentials.api_key_strategy, 'round_robin', '克隆保存请求必须保留轮询策略')

console.log('账户克隆按需加载回归通过：单 clone-context 请求、窄 credentialOptions、建号秘密不回填')

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start + startMarker.length)
  assert(start >= 0 && end > start, `无法定位源码片段: ${startMarker}`)
  return source.slice(start, end)
}

import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountCloneContext } from '../../types/domain'
import { buildAccountCloneFormLoad } from '../../views/accounts/accountEditFormLoaders'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../../..')
const editFormSource = readSource('src/views/accounts/useAccountEditForm.ts')
const accountApiSource = readSource('src/api/domains/accounts.ts')
const accountTypesSource = readSource('src/types/domain/accounts.ts')
const loaderSource = readSource('src/views/accounts/accountEditFormLoaders.ts')
const openCloneSource = sourceBetween(editFormSource, 'async function openClone(', 'async function loadAccountDetailForForm(')
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
assert.match(cloneContextTypeSource, /credentialOptions: AccountCloneCredentialOptions/)
assert.doesNotMatch(cloneContextTypeSource, /\bcredentials\b|balanceQuery|usage|runtime|permissions|authorizationSources/)
assert.doesNotMatch(
  cloneOptionsTypeSource,
  /api_key|api_keys|access_token|refresh_token|client_id|client_secret|quota_project_id|project_id|tier_id|password/,
  '克隆凭据选项类型不得容纳建号秘密'
)
assert.match(loaderSource, /const credentials = account\.credentialOptions/)

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
  credentialOptions: {
    base_url: 'https://api.openai.com/v1',
    supported_endpoint_modes: ['responses_json', 'responses_sse'],
    service_tier_override: 'priority',
    reasoning_effort_override: 'high'
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
  modelMappings: [],
  temporaryUnavailableContinuousProbeEnabled: true
}
const loaded = buildAccountCloneFormLoad({ account: context, defaults })
assert.equal(loaded.patch.baseUrl, 'https://api.openai.com/v1')
assert.equal(loaded.patch.apiKey, '')
assert.deepEqual(loaded.patch.apiKeys, [''])
assert.equal(loaded.patch.accessToken, '')
assert.equal(loaded.patch.refreshToken, '')
assert.equal(loaded.patch.googleClientId, '')
assert.equal(loaded.patch.googleClientSecret, '')
assert.equal(loaded.patch.googleQuotaProjectId, '')
assert.equal(loaded.patch.serviceTierOverride, 'priority')
assert.equal(loaded.patch.reasoningEffortOverride, 'high')

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

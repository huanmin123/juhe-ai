import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountAdvancedDetail, AccountCloneContext, AccountEditBasicDetail } from '../../src/types/domain'
import {
  buildAccountBasicEditFormLoad,
  buildAccountCloneFormLoad,
  buildAccountEditFormLoad
} from '../../src/views/accounts/accountEditFormLoaders'
import { defaultAccountForm } from '../../src/views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../src/views/accounts/accountOptions'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const modalSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/AccountEditModal.vue'), 'utf8')
const loaderSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/accountEditFormLoaders.ts'), 'utf8')
const defaults = defaultAccountForm('gpt', 'api_key', FALLBACK_PROVIDERS)
const credentials = {
  base_url: 'https://api.openai.com/v1',
  api_key: 'sk-redacted',
  supported_endpoint_modes: ['responses_json', 'responses_sse'],
  codex_responses_safe_repair_enabled: false,
  codex_responses_strict_intercept_enabled: true
}
const account: AccountEditBasicDetail = {
  id: 'account-codex-response-guard-edit',
  configRevision: 1,
  ownerSystemAccountId: 'sys_admin',
  providerCode: 'gpt',
  providerProtocolProfileId: defaults.providerProtocolProfileId,
  protocolCode: 'openai',
  protocolVersion: 'v1',
  name: 'Codex Responses 编辑回填回归',
  type: 'api_key',
  credentials,
  status: 'active',
  concurrencyLimit: 20,
  priority: 0,
  superPriorityEnabled: false,
  fallbackEnabled: false,
  clientCompatibility: 'codex_responses',
  supportedModels: ['gpt-5.4'],
  tags: [],
  healthCheckModel: 'gpt-5.4',
  healthCheckEndpointMode: 'responses_sse'
}
const advanced: AccountAdvancedDetail = {
  id: account.id,
  configRevision: account.configRevision,
  accessType: 'owner',
  credentials,
  modelMappings: [],
  temporaryUnavailableContinuousProbeEnabled: true,
  balanceQueryEnabled: false
}
const cloneContext: AccountCloneContext = {
  id: account.id,
  configRevision: account.configRevision,
  providerCode: account.providerCode,
  providerProtocolProfileId: account.providerProtocolProfileId,
  protocolCode: account.protocolCode,
  protocolVersion: account.protocolVersion,
  name: account.name,
  type: account.type,
  credentialOptions: {
    base_url: credentials.base_url,
    supported_endpoint_modes: ['responses_json', 'responses_sse'],
    codex_responses_safe_repair_enabled: credentials.codex_responses_safe_repair_enabled,
    codex_responses_strict_intercept_enabled: credentials.codex_responses_strict_intercept_enabled
  },
  concurrencyLimit: account.concurrencyLimit,
  priority: account.priority,
  superPriorityEnabled: account.superPriorityEnabled,
  fallbackEnabled: account.fallbackEnabled,
  clientCompatibility: account.clientCompatibility,
  supportedModels: account.supportedModels,
  tags: account.tags,
  healthCheckModel: account.healthCheckModel,
  healthCheckEndpointMode: account.healthCheckEndpointMode,
  modelMappings: [],
  temporaryUnavailableContinuousProbeEnabled: true
}

const basicLoaded = buildAccountBasicEditFormLoad({ account, credentials, defaults })
const loaded = buildAccountEditFormLoad({ account, advanced, credentials, defaults })
const cloned = buildAccountCloneFormLoad({ account: cloneContext, defaults })

assert.equal(basicLoaded.patch.clientCompatibility, 'codex_responses', '基础详情加载必须保留账户持久化的兼容模式')
for (const [label, patch] of [['编辑', loaded.patch], ['克隆', cloned.patch]] as const) {
  assert.equal(patch.clientCompatibility, 'codex_responses', `${label}必须保留账户持久化的 Codex Responses 兼容模式`)
  assert.equal(patch.codexResponsesSafeRepairEnabled, false, `${label}必须回填安全修复开关`)
  assert.equal(patch.codexResponsesStrictInterceptEnabled, true, `${label}必须回填严格拦截开关`)
}
assert.match(modalSource, /v-if="form\.clientCompatibility === 'codex_responses'"[\s\S]*Codex Responses 响应防护/, 'Codex Responses 账户必须显示响应防护区')
assert.match(loaderSource, /function accountClientCompatibilityForForm[\s\S]*return account\.clientCompatibility/, '编辑回填不得重新推导并覆盖持久化兼容模式')

console.log('Codex Responses 响应防护编辑回归通过')

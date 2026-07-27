import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountSummary, AccountUsageSummary } from '../../src/types/domain'
import { buildAccountEditFormLoad } from '../../src/views/accounts/accountEditFormLoaders'
import { defaultAccountForm } from '../../src/views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../src/views/accounts/accountOptions'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const modalSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/AccountEditModal.vue'), 'utf8')
const loaderSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/accountEditFormLoaders.ts'), 'utf8')
const zeroUsage: AccountUsageSummary = {
  requestCount: 0,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheReadCost: 0,
  cacheWriteTokens: 0,
  cacheWrite1hTokens: 0,
  cacheWriteCost: 0,
  thinkingTokens: 0,
  inputImageTokens: 0,
  outputImageTokens: 0,
  totalTokens: 0,
  totalCost: 0
}
const defaults = defaultAccountForm('gpt', 'api_key', FALLBACK_PROVIDERS)
const credentials = {
  base_url: 'https://api.openai.com/v1',
  api_key: 'sk-redacted',
  supported_endpoint_modes: ['responses_json', 'responses_sse'],
  codex_responses_safe_repair_enabled: false,
  codex_responses_strict_intercept_enabled: true
}
const account: AccountSummary = {
  id: 'account-codex-response-guard-edit',
  providerCode: 'gpt',
  providerProtocolProfileId: defaults.providerProtocolProfileId,
  name: 'Codex Responses 编辑回填回归',
  type: 'api_key',
  credentials,
  status: 'active',
  concurrencyLimit: 20,
  currentConcurrency: 0,
  priority: 0,
  superPriorityEnabled: false,
  fallbackEnabled: false,
  clientCompatibility: 'codex_responses',
  supportedModels: ['gpt-5.4'],
  healthCheckModel: 'gpt-5.4',
  healthCheckEndpointMode: 'responses_sse',
  schedulable: true,
  todayUsage: zeroUsage,
  usage: zeroUsage
}

const loaded = buildAccountEditFormLoad({ account, credentials, defaults })
assert.equal(loaded.patch.clientCompatibility, 'codex_responses', '编辑必须保留账户持久化的 Codex Responses 兼容模式')
assert.equal(loaded.patch.codexResponsesSafeRepairEnabled, false, '编辑必须回填默认安全修复开关')
assert.equal(loaded.patch.codexResponsesStrictInterceptEnabled, true, '编辑必须回填严格拦截开关')
assert.match(
  modalSource,
  /v-if="form\.clientCompatibility === 'codex_responses'"[\s\S]*Codex Responses 响应防护/,
  '编辑表单识别到 Codex Responses 兼容模式时必须展示响应防护区'
)
assert.match(
  loaderSource,
  /function accountClientCompatibilityForForm[\s\S]*return account\.clientCompatibility/,
  '编辑回填不得再用测试兼容性默认值覆盖账户持久化事实'
)

console.log('Codex Responses 响应防护编辑回归通过：兼容模式与两个防护开关均可见并准确回填')

import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountSummary, AccountUsageSummary, ProviderDefinition, ProviderModelPricing } from '../../src/types/domain'
import {
  buildTestModelOptions,
  defaultTestModelForAccountSelection
} from '../../src/views/accounts/accountDerivedState'
import { defaultAccountForm } from '../../src/views/accounts/accountFormDefaults'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const accountTestModalPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModal.ts')
const accountTestModelsPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModels.ts')
const accountsViewPath = resolve(frontendRoot, 'src/views/accounts/AccountsView.vue')
const accountListDataPath = resolve(frontendRoot, 'src/views/accounts/useAccountListData.ts')

const accountTestModalSource = readFileSync(accountTestModalPath, 'utf8')
const accountTestModelsSource = readFileSync(accountTestModelsPath, 'utf8')
const accountsViewSource = readFileSync(accountsViewPath, 'utf8')
const accountListDataSource = readFileSync(accountListDataPath, 'utf8')

assertIncludes(accountTestModalSource, "import { useAccountTestModels } from './useAccountTestModels'", '账户测试弹窗应通过模型 composable 获取测试模型能力')
assertIncludes(accountTestModelsSource, 'export function useAccountTestModels', '模型 composable 应导出 useAccountTestModels')
assertIncludes(accountTestModelsSource, 'api.providers.models(requestProviderCode, input.accountScopeParams.value)', '模型 composable 应按当前账户作用域加载供应商模型列表')
assertIncludes(accountTestModelsSource, 'api.providers.setDefaultTestModel(providerCode, normalizedModel, input.accountScopeParams.value)', '模型 composable 应按当前账户作用域持久化手动选择的默认测试模型')
assertIncludes(accountTestModelsSource, 'buildTestModelOptions', '模型 composable 应负责构建测试模型选项')
assertIncludes(accountTestModelsSource, 'providerDefaultTestModelForAccountSelection', '模型 composable 应负责供应商默认测试模型推导')
assertIncludes(accountTestModelsSource, 'nextTestModel', '模型 composable 应负责测试模型回落选择')
assertIncludes(accountTestModelsSource, 'providerModelsProviderCode.value === providerCode', '模型 composable 应按供应商校验缓存归属')
assertIncludes(accountTestModelsSource, 'if (!providerCode)', '模型 composable 应在没有唯一供应商时停止加载模型目录')
assertIncludes(accountTestModelsSource, 'testTargetProviderCode.value === providerCode', '模型 composable 应按当前测试目标校验请求是否仍有效')
assertIncludes(accountTestModalSource, 'function updateAccountTestModel(model: string)', '账户测试弹窗应区分用户手动切换模型和程序默认赋值')
assertIncludes(accountTestModalSource, 'void saveDefaultTestModel(normalizedModel)', '用户手动切换测试模型后应持久化个人默认')
assertIncludes(accountsViewSource, '@update:model="updateAccountTestModel"', '账户页应把测试模型选择事件交给持久化处理')
assertNotIncludes(accountsViewSource, 'v-model:model="testForm.model"', '账户页不应继续仅通过临时 v-model 保存测试模型')
assertIncludes(accountListDataSource, 'api.providers.options(systemAccountId ? { systemAccountId } : undefined)', '账户页应按当前系统账户作用域加载 provider 默认测试模型')
assertIncludes(accountListDataSource, 'loadAccountOptions(systemAccountId, Boolean(_loadOptions?.forceOptions))', '账户页刷新数据时应同步刷新 provider 选项')

assertNotIncludes(accountTestModalSource, 'api.providers.models', '账户测试弹窗不应直接加载供应商模型列表')
assertNotIncludes(accountTestModalSource, 'ProviderModelPricing', '账户测试弹窗不应持有供应商模型列表类型')
assertNotIncludes(accountTestModalSource, 'providerModelsProviderCode', '账户测试弹窗不应持有供应商模型缓存归属状态')
assertNotIncludes(accountTestModalSource, 'buildTestModelOptions', '账户测试弹窗不应直接构建测试模型选项')
assertNotIncludes(accountTestModalSource, 'providerDefaultTestModelForAccountSelection', '账户测试弹窗不应直接推导供应商默认测试模型')
assertNotIncludes(accountTestModalSource, 'isGatewaySupportedTestSelection', '账户测试弹窗不应直接判断测试目标协议兼容')
assertNotIncludes(accountTestModalSource, 'nextTestModel', '账户测试弹窗不应直接处理测试模型回落')
assertNotIncludes(accountTestModalSource, 'GPT_VENDOR_CODE', '账户测试弹窗不应直接持有 OpenAI 默认供应商回落')
assertNotIncludes(accountTestModelsSource, 'GPT_VENDOR_CODE', '模型 composable 不应直接持有 GPT 供应商常量')
assertNotIncludes(accountTestModelsSource, 'preferredDefaultProviderCode', '模型 composable 不应在混合供应商选择时回落到默认供应商模型目录')

assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('gpt-5.4-mini'),
    providerModel('gpt-5.5')
  ], accountFixture(), 'gpt-5.4-mini')),
  ['gpt-5.4-mini', 'gpt-5.5'],
  '未限制模型的账户测试下拉应合并供应商默认模型和模型目录'
)

const limitedAccount = accountFixture({
  supportedModels: ['gpt-5.5', 'gpt-5.4']
})
assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('gpt-5.4-mini'),
    providerModel('gpt-5.5'),
    providerModel('gpt-4.1')
  ], limitedAccount, 'gpt-5.4-mini')),
  ['gpt-5.5', 'gpt-5.4'],
  '已限制模型的账户测试下拉只能展示账户 supportedModels'
)
assert.equal(
  defaultTestModelForAccountSelection(limitedAccount, 'gpt-5.4-mini'),
  'gpt-5.5',
  '已限制模型且供应商默认模型不在限制内时，应默认选第一个受限模型'
)

const limitedAccountWithAllowedDefault = accountFixture({
  supportedModels: ['gpt-5.4', 'gpt-5.5']
})
assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('gpt-5.4-mini'),
    providerModel('gpt-4.1')
  ], limitedAccountWithAllowedDefault, 'gpt-5.5')),
  ['gpt-5.5', 'gpt-5.4'],
  '已限制模型且供应商默认模型在限制内时，下拉应仍只展示受限模型并优先默认模型'
)
assert.equal(
  defaultTestModelForAccountSelection(limitedAccountWithAllowedDefault, 'gpt-5.5'),
  'gpt-5.5',
  '供应商默认模型在账户限制内时可作为默认测试模型'
)

assertDeepEqual(
  defaultAccountForm('gpt', 'api_key', [
    providerFixture({
      defaultTestModel: 'gpt-personal-default',
      defaultSupportedModels: ['gpt-system-default', 'gpt-personal-default']
    })
  ]).supportedModels,
  ['gpt-personal-default', 'gpt-system-default'],
  '新建账户默认支持模型应优先包含当前用户的默认测试模型并去重'
)

assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('gpt-5.4-mini'),
    providerModel('gpt-5.5')
  ], [
    accountFixture({ id: 'acct_batch_limited_a', supportedModels: ['gpt-5.5', 'gpt-5.4'] }),
    accountFixture({ id: 'acct_batch_limited_b', supportedModels: ['gpt-5.5', 'gpt-4.1'] }),
    accountFixture({ id: 'acct_batch_unrestricted' })
  ], 'gpt-5.4-mini')),
  ['gpt-5.5'],
  '批量测试包含模型限制账户时，下拉应只展示所有受限账户共同支持的模型'
)

console.log('账户测试模型 composable 回归通过：模型加载、缓存归属、默认模型与弹窗流程边界保持分离')

function assertIncludes(source: string, expected: string, message: string): void {
  if (!source.includes(expected)) {
    throw new Error(`${message}，未找到 ${expected}`)
  }
}

function assertNotIncludes(source: string, unexpected: string, message: string): void {
  if (source.includes(unexpected)) {
    throw new Error(`${message}，不应包含 ${unexpected}`)
  }
}

function accountFixture(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'acct_test_models',
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '测试模型账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    ...overrides
  }
}

function providerModel(model: string): ProviderModelPricing {
  return {
    providerCode: 'gpt',
    model,
    source: 'built-in',
    scope: 'built_in',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: false
  }
}

function providerFixture(overrides: Partial<ProviderDefinition> = {}): ProviderDefinition {
  return {
    id: 'provider_gpt',
    code: 'gpt',
    name: 'OpenAI',
    enabled: true,
    defaultProtocolProfileId: 'gpt-openai-v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: 'https://api.openai.com/v1',
    defaultTestModel: 'gpt-system-default',
    defaultSupportedModels: ['gpt-system-default'],
    accountTypes: ['api_key'],
    capabilities: [],
    protocolProfiles: [{
      id: 'gpt-openai-v1',
      providerCode: 'gpt',
      name: 'OpenAI v1',
      enabled: true,
      protocolCode: 'openai',
      protocolVersion: 'v1',
      baseUrl: 'https://api.openai.com/v1',
      defaultTestModel: 'gpt-system-default',
      accountTypes: ['api_key'],
      capabilities: [],
      endpointFamilies: []
    }],
    ...overrides
  }
}

function emptyUsage(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function optionValues(options: Array<{ value: string }>): string[] {
  return options.map((option) => option.value)
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualJson = JSON.stringify(actual)
  const expectedJson = JSON.stringify(expected)
  if (actualJson !== expectedJson) {
    throw new Error(`${message}，实际 ${actualJson}，预期 ${expectedJson}`)
  }
}

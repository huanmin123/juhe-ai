import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountOptionSummary } from '../../src/types/domain'
import {
  canRunModelCheckForAccount,
  canSelectModelCheckAccount,
  canSelectTrustedModelCheckAccount,
  modelCheckModelsForAccount
} from '../../src/views/model-checks/modelCheckProviderCapabilities'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const accountOptionsSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/useModelCheckAccountOptions.ts'), 'utf8')
const capabilitySource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/modelCheckProviderCapabilities.ts'), 'utf8')

const gptOpenAIAccount = accountFixture({
  id: 'acct_model_check_gpt_openai',
  providerCode: 'gpt',
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
const openAICompatibleAccount = accountFixture({
  id: 'acct_model_check_openai_compatible',
  providerCode: 'openai',
  providerProtocolProfileId: 'profile_openai_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
const anthropicAccount = accountFixture({
  id: 'acct_model_check_anthropic',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1'
})
const deepSeekAccount = accountFixture({
  id: 'acct_model_check_deepseek',
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
const glmAccount = accountFixture({
  id: 'acct_model_check_glm',
  providerCode: 'glm',
  providerProtocolProfileId: 'profile_glm_general_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
const geminiAccount = accountFixture({
  id: 'acct_model_check_gemini',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
})
const secondAnthropicAccount = accountFixture({
  id: 'acct_model_check_anthropic_trusted',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1'
})
const unnamedOpenAIAccount = accountFixture({
  id: 'acct_model_check_unnamed',
  providerCode: 'openai',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  name: '   '
})

assert.equal(canRunModelCheckForAccount(gptOpenAIAccount), true, 'GPT 的 OpenAI v1 账户仍应可进入模型检测')
assert.equal(canRunModelCheckForAccount(openAICompatibleAccount), true, 'OpenAI-compatible 的 OpenAI v1 账户应可进入模型检测')
assert.equal(canRunModelCheckForAccount(deepSeekAccount), true, 'DeepSeek OpenAI Chat 账户应可进入模型检测')
assert.equal(canRunModelCheckForAccount(anthropicAccount), true, 'Anthropic 原生账户应可进入模型检测')
assert.equal(canRunModelCheckForAccount(glmAccount), true, 'GLM OpenAI Chat 账户应可进入模型检测')
assert.equal(canRunModelCheckForAccount(geminiAccount), true, 'Gemini native 账户应可进入模型检测')
assert.equal(canSelectModelCheckAccount(openAICompatibleAccount), true, 'OpenAI-compatible 有名称账户应可被选择')
assert.equal(canSelectModelCheckAccount(openAICompatibleAccount, { excludedAccountId: openAICompatibleAccount.id }), false, '可信对比账户不能选择当前检测目标')
assert.equal(canSelectModelCheckAccount(unnamedOpenAIAccount), false, '无名称账户不应出现在模型检测选项中')
assert.deepEqual(modelCheckModelsForAccount(gptOpenAIAccount), ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.4'], 'GPT 模型检测必须使用当前完整 GPT 模型 ID')
assert.deepEqual(modelCheckModelsForAccount(anthropicAccount), ['claude-opus-4-8', 'claude-opus-4-7'], 'Anthropic 模型检测必须使用完整 Claude 模型 ID')
assert.deepEqual(modelCheckModelsForAccount(glmAccount), ['glm-5.2', 'glm-5.1'], 'GLM 模型检测必须使用完整 GLM 模型 ID')
assert.deepEqual(modelCheckModelsForAccount(deepSeekAccount), ['deepseek-v4-flash', 'deepseek-v4-pro'], 'DeepSeek 模型检测必须使用完整 DeepSeek 模型 ID')
assert.deepEqual(modelCheckModelsForAccount(geminiAccount), ['gemini-3.5-flash', 'gemini-3.1-pro-preview'], 'Gemini 模型检测必须使用完整 Gemini 模型 ID')
assert.equal(canSelectTrustedModelCheckAccount(secondAnthropicAccount, {
  targetAccount: anthropicAccount,
  model: 'claude-opus-4-8',
  excludedAccountId: anthropicAccount.id
}), true, '同供应商同 profile 同模型的 Anthropic 账户应可作为可信对比')
assert.equal(canSelectTrustedModelCheckAccount(gptOpenAIAccount, {
  targetAccount: anthropicAccount,
  model: 'claude-opus-4-8',
  excludedAccountId: anthropicAccount.id
}), false, '可信对比账户不能跨供应商或跨协议 profile 选择')

assert.match(accountOptionsSource, /from '\.\/modelCheckProviderCapabilities'/, '模型检测账户选项应通过能力 helper 过滤')
assert.doesNotMatch(accountOptionsSource, /isGptVendorCode|GPT_VENDOR_CODE/, '模型检测账户选项不应再绑定 GPT 供应商名')
assert.doesNotMatch(accountOptionsSource, /isOpenAIProtocolProfile/, '模型检测账户选项不应内联协议判断')
assert.match(capabilitySource, /gpt-5\.6-sol/, '能力 helper 必须包含 GPT-5.6 Sol 完整模型 ID')
assert.match(capabilitySource, /claude-opus-4-8/, '能力 helper 必须包含 Anthropic 完整模型 ID')
assert.match(capabilitySource, /glm-5\.2/, '能力 helper 必须包含 GLM 完整模型 ID')
assert.match(capabilitySource, /deepseek-v4-flash/, '能力 helper 必须包含 DeepSeek 完整模型 ID')
assert.match(capabilitySource, /gemini-3\.5-flash/, '能力 helper 必须包含 Gemini 完整模型 ID')

console.log('模型检测供应商能力回归通过：多供应商账户按完整模型 ID 和 provider profile 进入检测')

function accountFixture(overrides: Partial<AccountOptionSummary> = {}): AccountOptionSummary {
  return {
    id: 'acct_model_check',
    systemAccountId: 'sys_admin',
    systemAccountName: '系统账户',
    ownerSystemAccountId: 'sys_admin',
    ownerSystemAccountName: '系统账户',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '模型检测账户',
    type: 'api_key',
    status: 'active',
    accessType: 'owner',
    accountAuthorizationId: undefined,
    authorizationStatus: undefined,
    authorizationExpiresAt: undefined,
    accountExpiresAt: undefined,
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canAuthorize: true,
      canViewCredentials: true,
      canBindToApiKey: true
    },
    ...overrides
  }
}

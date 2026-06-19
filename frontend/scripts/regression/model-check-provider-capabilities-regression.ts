import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountOptionSummary } from '../../src/types/domain'
import {
  canRunModelCheckForAccount,
  canSelectModelCheckAccount
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
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
const anthropicAccount = accountFixture({
  id: 'acct_model_check_anthropic',
  providerCode: 'anthropic',
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
assert.equal(canRunModelCheckForAccount(anthropicAccount), false, 'Anthropic 原生账户不应进入当前 OpenAI v1 模型检测')
assert.equal(canSelectModelCheckAccount(openAICompatibleAccount), true, 'OpenAI-compatible 有名称账户应可被选择')
assert.equal(canSelectModelCheckAccount(openAICompatibleAccount, { excludedAccountId: openAICompatibleAccount.id }), false, '可信对比账户不能选择当前检测目标')
assert.equal(canSelectModelCheckAccount(unnamedOpenAIAccount), false, '无名称账户不应出现在模型检测选项中')

assert.match(accountOptionsSource, /from '\.\/modelCheckProviderCapabilities'/, '模型检测账户选项应通过能力 helper 过滤')
assert.doesNotMatch(accountOptionsSource, /isGptVendorCode|GPT_VENDOR_CODE/, '模型检测账户选项不应再绑定 GPT 供应商名')
assert.doesNotMatch(accountOptionsSource, /isOpenAIProtocolProfile/, '模型检测账户选项不应内联协议判断')
assert.match(capabilitySource, /isOpenAIProtocolProfile/, '当前模型检测能力 helper 应独立表达 OpenAI v1 协议边界')

console.log('模型检测供应商能力回归通过：OpenAI-compatible 可进入 OpenAI v1 检测，Anthropic 原生仍隔离')

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


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
const schedulesModalSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/ModelQualitySchedulesModal.vue'), 'utf8')
const modelChecksViewSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/ModelChecksView.vue'), 'utf8')
const qualityConfigSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/ModelQualityConfigPopover.vue'), 'utf8')

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
assert.deepEqual(modelCheckModelsForAccount({ ...gptOpenAIAccount, modelCheckModels: ['gpt-5.4'] }), ['gpt-5.4'], '账户模型限制必须覆盖供应商通用模型列表')
assert.deepEqual(modelCheckModelsForAccount({ ...gptOpenAIAccount, modelCheckModels: [] }), [], '没有可用检测模型的账户不得回退到供应商通用模型列表')
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
assert.match(accountOptionsSource, /purpose: 'run'/, '运行下拉必须请求专用 run purpose options')
assert.match(accountOptionsSource, /purpose: 'history'/, '历史筛选必须请求专用 history purpose options')
assert.match(accountOptionsSource, /selectedIds(?:\s*:\s*|\s*\n)/, '模型检测选项请求必须携带已选 ID 以避免搜索窗口丢失当前值')
assert.match(accountOptionsSource, /systemAccountId === input\.modelCheckScopeParams\.value\?\.systemAccountId/, '模型检测选项不得让旧身份请求覆盖新身份状态')
assert.doesNotMatch(accountOptionsSource, /isGptVendorCode|GPT_VENDOR_CODE/, '模型检测账户选项不应再绑定 GPT 供应商名')
assert.doesNotMatch(accountOptionsSource, /isOpenAIProtocolProfile/, '模型检测账户选项不应内联协议判断')
assert.match(capabilitySource, /gpt-5\.6-sol/, '能力 helper 必须包含 GPT-5.6 Sol 完整模型 ID')
assert.match(capabilitySource, /claude-opus-4-8/, '能力 helper 必须包含 Anthropic 完整模型 ID')
assert.match(capabilitySource, /glm-5\.2/, '能力 helper 必须包含 GLM 完整模型 ID')
assert.match(capabilitySource, /deepseek-v4-flash/, '能力 helper 必须包含 DeepSeek 完整模型 ID')
assert.match(capabilitySource, /gemini-3\.5-flash/, '能力 helper 必须包含 Gemini 完整模型 ID')
assert.match(capabilitySource, /account\?\.modelCheckModels/, '能力 helper 必须优先使用后台返回的账户级可用检测模型')
assert.match(schedulesModalSource, /<a-form\s+:model="form"/, '定时检查表单必须绑定 model，确保提交事件实际触发')
assert.match(schedulesModalSource, /selectedModelOptions/, '定时检查模型必须随账户模型能力联动')
assert.match(schedulesModalSource, /props\.resetToken/, '定时检查保存或删除成功后必须清除旧 revision 编辑态')
assert.match(schedulesModalSource, /class="schedule-form-actions"/, '定时检查操作按钮必须使用独立布局容器')
assert.match(schedulesModalSource, /grid-column:\s*1\s*\/\s*-1/, '定时检查操作按钮必须跨列收口，避免编辑态按钮溢出弹窗')
assert.match(schedulesModalSource, /align-items:\s*start/, '定时检查表单项必须顶部对齐，避免单项校验提示压低同排字段')
assert.match(schedulesModalSource, /ant-form-item-control\)\s*\{\s*min-height:/, '定时检查桌面表单必须为校验提示预留稳定空间')
assert.match(schedulesModalSource, /v-model:value="form\.profile"/, '每条定时计划必须独立绑定快速或深度检测模式')
assert.match(schedulesModalSource, /v-model:value="form\.penaltyThreshold"/, '每条定时计划必须独立绑定处罚阈值')
assert.match(schedulesModalSource, /v-model:value="form\.penaltyAction"/, '每条定时计划必须独立绑定处罚方式')
assert.match(schedulesModalSource, /form\.penaltyAction === 'quality_isolate'/, '定时计划仅在质量隔离时显示独立恢复周期')
assert.match(schedulesModalSource, /检测配置和处理规则彼此独立/, '定时计划必须明确不继承外部手动配置')
assert.match(qualityConfigSource, /v-if="form\.penaltyAction === 'quality_isolate'"/, '只有质量隔离处罚才应显示恢复周期')
assert.match(qualityConfigSource, /耗时更长且消耗更多 Token/, '深度检测说明必须明确耗时和 Token 成本')
assert.match(qualityConfigSource, /关闭后仅记录检测结果与健康状态，不修改账户/, '手动处罚说明必须明确关闭后的行为边界')
assert.match(qualityConfigSource, /仅用于页面手动检查；定时计划使用各自独立配置/, '外部质量配置必须明确只作用于手动检查')
assert.match(modelChecksViewSource, /modelCheckModels:\s*\[\.\.\.item\.modelCheckModels\]/, '定时检查账户选项必须保留后台返回的账户级模型能力')
assert.match(modelChecksViewSource, /scheduleFormResetToken\.value \+= 1/, '定时检查成功写入后必须推进表单重置代次')

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

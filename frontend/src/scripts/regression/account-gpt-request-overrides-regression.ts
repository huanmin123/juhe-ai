import { strict as assert } from 'node:assert'

import {
  accountGptRequestOverrideCapabilities,
  accountGptRequestOverridesForForm,
  availableAccountGptReasoningEffortOptions,
  availableAccountGptServiceTierOptions
} from '../../views/accounts/accountGptRequestOverrides'
import { buildAccountCredentials } from '../../views/accounts/accountCredentials'
import { buildAccountDraftTestPayload } from '../../views/accounts/accountDraftTestPayload'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import {
  buildOAuthCreateCommonPayload,
  validateAccountSaveForm
} from '../../views/accounts/accountSavePayload'
import {
  buildCustomModelPayload,
  createCustomModelFormFromPricing,
  emptyCustomModelForm
} from '../../views/providers/customProviderModelForm'
import { formatModelRequestCapabilities } from '../../views/providers/providerModelFormatters'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import type {
  ProviderModelPricing,
  ProviderModelReasoningEffort
} from '../../types/domain'
import type { AccountFormModel } from '../../views/accounts/accountFormTypes'
import type { AccountModelSelectOption } from '../../views/accounts/accountEditFormPayload'

const modelOptions: AccountModelSelectOption[] = [
  {
    label: 'gpt-5.6-sol',
    value: 'gpt-5.6-sol',
    supportedApiProtocols: ['responses', 'chat_completions'],
    supportedServiceTiers: ['priority', 'flex'],
    supportedReasoningEfforts: ['low', 'medium', 'high', 'max'],
    defaultReasoningEffort: 'high'
  },
  {
    label: 'gpt-5.6-terra',
    value: 'gpt-5.6-terra',
    supportedApiProtocols: ['responses', 'chat_completions'],
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['medium', 'high', 'xhigh', 'max'],
    defaultReasoningEffort: 'high'
  }
]

const apiKeyCapabilities = accountGptRequestOverrideCapabilities({
  accountType: 'api_key',
  modelOptions,
  supportedModels: ['gpt-5.6-sol', 'gpt-5.6-terra']
})
assert.deepEqual(apiKeyCapabilities.serviceTiers, ['priority'], '服务等级必须取全部支持模型的精确交集')
assert.deepEqual(apiKeyCapabilities.reasoningEfforts, ['medium', 'high', 'max'], '思考级别必须取全部支持模型的精确交集')
assert.deepEqual(
  availableAccountGptServiceTierOptions(apiKeyCapabilities).map((option) => option.value),
  ['', 'default', 'priority'],
  '存在共同非标准服务等级时才应展示显式 default'
)
assert.deepEqual(
  availableAccountGptReasoningEffortOptions(apiKeyCapabilities).map((option) => option.value),
  ['', 'medium', 'high', 'max'],
  '账户思考级别选项不得包含模型交集之外的值'
)

const unknownCapabilities = accountGptRequestOverrideCapabilities({
  accountType: 'api_key',
  modelOptions,
  supportedModels: ['gpt-5.6-sol', 'gpt-unknown']
})
assert.deepEqual(unknownCapabilities, { serviceTiers: [], reasoningEfforts: [] }, '任一支持模型未知时对应能力交集必须为空')

const oauthFlexOnlyCapabilities = accountGptRequestOverrideCapabilities({
  accountType: 'oauth',
  modelOptions: [{
    label: 'gpt-flex-only',
    value: 'gpt-flex-only',
    supportedServiceTiers: ['flex'],
    supportedReasoningEfforts: ['high']
  }],
  supportedModels: ['gpt-flex-only']
})
assert.deepEqual(oauthFlexOnlyCapabilities.serviceTiers, [], 'OAuth 必须从可用服务等级中移除 Flex')
assert.deepEqual(
  availableAccountGptServiceTierOptions(oauthFlexOnlyCapabilities).map((option) => option.value),
  [''],
  'OAuth 过滤 Flex 后没有共同非标准等级时也不能展示 default'
)

const apiKeyForm = gptForm('api_key')
apiKeyForm.serviceTierOverride = 'priority'
apiKeyForm.reasoningEffortOverride = 'high'
const apiKeyCredentials = buildAccountCredentials({
  errorPolicyRules: [],
  responseInspectionRules: [],
  form: apiKeyForm
})
assert.equal(apiKeyCredentials.service_tier_override, 'priority', 'API Key credentials 必须保存 snake_case 服务等级覆盖')
assert.equal(apiKeyCredentials.reasoning_effort_override, 'high', 'API Key credentials 必须保存 snake_case 思考级别覆盖')
assert.deepEqual(
  accountGptRequestOverridesForForm('gpt', apiKeyCredentials),
  { serviceTierOverride: 'priority', reasoningEffortOverride: 'high' },
  '编辑和克隆加载必须恢复已保存 GPT 覆盖'
)

const draftPayload = buildAccountDraftTestPayload({
  accounts: [],
  form: apiKeyForm,
  errorPolicyRules: [],
  responseInspectionRules: [],
  mappingUpstreamModelOptions: modelOptions,
  providers: FALLBACK_PROVIDERS
})
assert.equal(draftPayload.credentials.service_tier_override, 'priority', '草稿测试必须保留服务等级覆盖')
assert.equal(draftPayload.credentials.reasoning_effort_override, 'high', '草稿测试必须保留思考级别覆盖')

const oauthForm = gptForm('oauth')
oauthForm.oauthMode = 'refresh_token'
oauthForm.refreshToken = 'refresh-regression'
oauthForm.serviceTierOverride = 'priority'
oauthForm.reasoningEffortOverride = 'max'
const oauthPayload = buildOAuthCreateCommonPayload({
  accounts: [],
  form: oauthForm,
  errorPolicyRules: [],
  responseInspectionRules: []
})
assert.equal(oauthPayload.credentialsPatch?.service_tier_override, 'priority', 'OAuth credentialsPatch 必须保留服务等级覆盖')
assert.equal(oauthPayload.credentialsPatch?.reasoning_effort_override, 'max', 'OAuth credentialsPatch 必须保留思考级别覆盖')

oauthForm.serviceTierOverride = 'flex'
assert.equal(
  validateAccountSaveForm({
    form: oauthForm,
    hasAuthSession: true,
    errorPolicyRules: [],
    responseInspectionRules: [],
    mappingUpstreamModelOptions: modelOptions,
    providers: FALLBACK_PROVIDERS
  }),
  'GPT OAuth 账户不支持 Flex 服务等级',
  'OAuth 保存前必须稳定拒绝 Flex'
)

const customModelForm = {
  ...emptyCustomModelForm,
  model: 'gpt-custom-capabilities',
  supportedServiceTiers: ['priority', 'priority', 'flex'],
  supportedReasoningEfforts: ['high', 'ultra', 'max'] as unknown as ProviderModelReasoningEffort[],
  defaultReasoningEffort: 'ultra' as ProviderModelReasoningEffort
}
const customPayload = buildCustomModelPayload(customModelForm, 'text', { includeGptCapabilities: true })
assert.deepEqual(customPayload?.supportedServiceTiers, ['priority', 'flex'], '自定义模型服务等级应去重并限制 wire 枚举')
assert.deepEqual(customPayload?.supportedReasoningEfforts, ['high', 'max'], '自定义模型 wire 思考级别必须过滤 Ultra')
assert.equal(customPayload?.defaultReasoningEffort, null, '默认思考级别不在已支持 wire 级别中时必须清空')
const imageCustomPayload = buildCustomModelPayload(customModelForm, 'image', { includeGptCapabilities: true })
assert.deepEqual(imageCustomPayload?.supportedServiceTiers, [], 'GPT 非文本自定义模型保存时必须清空服务等级能力')
assert.deepEqual(imageCustomPayload?.supportedReasoningEfforts, [], 'GPT 非文本自定义模型保存时必须清空思考能力')
assert.equal(imageCustomPayload?.defaultReasoningEffort, null, 'GPT 非文本自定义模型保存时必须清空默认思考级别')

const catalogRecord = providerModel({
  supportedServiceTiers: ['priority'],
  supportedReasoningEfforts: ['high', 'max'],
  defaultReasoningEffort: 'high',
  codexSupportedReasoningLevels: ['high', 'ultra'],
  codexDefaultReasoningLevel: 'ultra',
  codexMultiAgentVersion: 'v2'
})
const loadedCustomForm = createCustomModelFormFromPricing(catalogRecord, [])
assert.deepEqual(loadedCustomForm.supportedServiceTiers, ['priority'], '自定义模型编辑必须恢复 wire 服务等级能力')
assert.deepEqual(loadedCustomForm.supportedReasoningEfforts, ['high', 'max'], '自定义模型编辑必须恢复 wire 思考能力')
assert.equal(loadedCustomForm.defaultReasoningEffort, 'high', '自定义模型编辑必须恢复 wire 默认思考级别')
assert.match(formatModelRequestCapabilities(catalogRecord), /Codex High \/ Ultra/, '模型目录可以展示 Codex Ultra 能力')
assert.doesNotMatch(
  availableAccountGptReasoningEffortOptions(apiKeyCapabilities).map((option) => option.value).join(','),
  /ultra/,
  '账户请求覆盖永远不能显示 Ultra'
)

console.log('GPT 请求覆盖前端回归通过：精确交集、OAuth 限制、持久化和自定义模型能力均符合契约')

function gptForm(type: 'api_key' | 'oauth'): AccountFormModel {
  const form = defaultAccountForm('gpt', type, FALLBACK_PROVIDERS)
  form.name = 'GPT 覆盖回归账户'
  form.groupId = 'grp_gpt_override'
  form.group = { id: 'grp_gpt_override', name: 'GPT 覆盖回归分组' }
  form.supportedModels = ['gpt-5.6-sol']
  form.healthCheckModel = 'gpt-5.6-sol'
  form.apiKeys = type === 'api_key' ? ['sk-regression-gpt-override'] : ['']
  form.apiKey = type === 'api_key' ? 'sk-regression-gpt-override' : ''
  return form
}

function providerModel(overrides: Partial<ProviderModelPricing> = {}): ProviderModelPricing {
  return {
    providerCode: 'gpt',
    model: 'gpt-custom-capabilities',
    source: 'custom-personal',
    scope: 'personal',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: true,
    supportedApiProtocols: ['responses', 'chat_completions'],
    ...overrides
  }
}

import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { buildProviderModelColumns } from '../../views/providers/providerModelTableState'

import {
  accountGptRequestOverrideCapabilities,
  accountGptRequestOverridesForForm,
  availableAccountGptReasoningEffortOptions,
  availableAccountGptServiceTierOptions,
  isAccountRequestOverrideProviderSupported
} from '../../views/accounts/accountGptRequestOverrides'
import { buildAccountCredentials } from '../../views/accounts/accountCredentials'
import { buildAccountDraftTestPayload } from '../../views/accounts/accountDraftTestPayload'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import {
  buildOAuthCreateCommonPayload,
  validateAccountSaveForm
} from '../../views/accounts/accountSavePayload'
import {
  applyConfigurationTemplateToCustomModelForm,
  availableCustomModelStatusOptions,
  buildCustomModelCapabilityOptions,
  buildCustomModelPayload,
  clearCustomModelPricesOutsideCategory,
  createCustomModelFormFromPricing,
  emptyCustomModelForm,
  reconcileCustomModelServiceTierPrices
} from '../../views/providers/customProviderModelForm'
import {
  customModelPriceFields,
  customModelTierPriceFields,
  formatModelReasoningCapabilities,
  formatModelRequestCapabilities,
  formatTokens
} from '../../views/providers/providerModelFormatters'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import type {
  ProviderModelPricing,
  ProviderModelReasoningEffort
} from '../../types/domain'
import type { AccountFormModel } from '../../views/accounts/accountFormTypes'
import type { AccountModelSelectOption } from '../../views/accounts/accountEditFormPayload'

const emptyTextColumnKeys = buildProviderModelColumns('text', []).map((column) => column.key)
assert.equal(emptyTextColumnKeys.some((key) => String(key).startsWith('catalogDisplay:')), false, '空目录不能生成无事实依据的动态列')
const declaredTextColumnKeys = buildProviderModelColumns('text', [providerModel({
  catalogDisplay: [
    { key: 'tier_priority', label: 'Priority', items: [{ key: 'input', label: '输入', format: 'usd_per_1m_tokens', value: 2 }] },
    { key: 'reasoning', label: 'Reasoning', items: [{ key: 'levels', label: '级别', format: 'text', value: 'high / max' }] }
  ]
})]).map((column) => column.key)
assert(declaredTextColumnKeys.includes('catalogDisplay:tier_priority'), '模型目录必须按后端 descriptor 展示服务档位')
assert(declaredTextColumnKeys.includes('catalogDisplay:reasoning'), '模型目录必须按后端 descriptor 展示思考能力')
assert.equal(declaredTextColumnKeys.includes('serviceTiers'), false, '模型目录不能保留固定服务等级列')
assert.equal(declaredTextColumnKeys.includes('reasoningEfforts'), false, '模型目录不能保留固定思考级别列')
assert.equal(formatTokens(131_072), '128K', '模型容量只显示 K，不追加精确括号数字')
assert.equal(formatTokens(1_048_576), '1M', '模型容量只显示 M，不追加精确括号数字')
const modelCatalogModalSource = readFileSync(new URL('../../views/providers/ProviderModelCatalogModal.vue', import.meta.url), 'utf8')
assert.match(modelCatalogModalSource, /modelCatalogDisplaySections\(record\)/, '移动端模型目录必须循环当前模型的动态 section')
assert.doesNotMatch(modelCatalogModalSource, /column\.key === '(?:serviceTiers|reasoningEfforts|prices|cacheWrite|context)'/, '模型目录不能继续渲染固定计费或能力列')
assert.doesNotMatch(modelCatalogModalSource, /codexSupportedReasoningLevels/, '模型目录思考列只能消费 API reasoning effort')
assert.doesNotMatch(modelCatalogModalSource, /codexMultiAgentVersion/, '模型目录思考列不能展示 Codex 多代理能力')
const requestOverridesSectionSource = readFileSync(new URL('../../views/accounts/AccountGptRequestOverridesSection.vue', import.meta.url), 'utf8')
const batchEditModalSource = readFileSync(new URL('../../views/accounts/AccountBatchEditModal.vue', import.meta.url), 'utf8')
assert.match(requestOverridesSectionSource, /v-if="requestOverridesSupported"/, '支持账户覆盖的供应商必须保留配置区')
assert.doesNotMatch(requestOverridesSectionSource, /serviceTierOptions\.length \|\| reasoningEffortOptions\.length/, '单账户配置区不能因能力为空整体隐藏')
assert.doesNotMatch(requestOverridesSectionSource, /<a-form-item v-if=/, '服务等级和思考级别字段不能按选项数量分别隐藏')
assert.doesNotMatch(requestOverridesSectionSource, /watch\(/, '模型能力变化不能静默清空已保存覆盖')
assert.match(
  requestOverridesSectionSource,
  /requestOverridesSupported[\s\S]+serviceTierOverride \|\| props\.form\.reasoningEffortOverride/,
  'Interactions-only 账户已有覆盖时必须保留清除入口'
)
assert.match(requestOverridesSectionSource, /当前已选模型未声明可用服务等级/, '服务等级无能力时必须显示中文原因')
assert.match(requestOverridesSectionSource, /当前已选模型未声明可用思考级别/, '思考级别无能力时必须显示中文原因')
assert.match(batchEditModalSource, /v-if="requestOverridesSupported"/, '批量编辑必须为支持供应商保留请求覆盖字段')
assert.doesNotMatch(batchEditModalSource, /serviceTierOptions\.length \|\| reasoningEffortOptions\.length/, '批量编辑不能因能力为空整体隐藏请求覆盖字段')
assert.equal(isAccountRequestOverrideProviderSupported('gpt'), true, 'GPT 账户必须显示请求覆盖配置区')
assert.equal(isAccountRequestOverrideProviderSupported('openai'), true, 'OpenAI-compatible 账户必须显示请求覆盖配置区')
assert.equal(isAccountRequestOverrideProviderSupported('anthropic'), true, 'Anthropic 账户必须显示请求覆盖配置区')
assert.equal(isAccountRequestOverrideProviderSupported('gemini'), true, 'Gemini 账户必须显示已实现的思考级别覆盖配置区')
assert.equal(
  isAccountRequestOverrideProviderSupported('gemini', ['interactions_json', 'interactions_sse']),
  false,
  'Interactions-only Gemini 账户不得显示 GenerateContent 请求覆盖配置区'
)
assert.equal(isAccountRequestOverrideProviderSupported('deepseek'), false, '没有账户覆盖 wire 映射的供应商不能显示无效配置区')

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
assert.deepEqual(apiKeyCapabilities.serviceTiers, ['priority', 'flex'], '配置页服务等级必须取账户支持模型目录能力合集')
assert.deepEqual(apiKeyCapabilities.reasoningEfforts, ['low', 'medium', 'high', 'max', 'xhigh'], '配置页思考级别必须按模型顺序取能力合集并去重')
assert.deepEqual(
  availableAccountGptServiceTierOptions(apiKeyCapabilities).map((option) => option.value),
  ['', 'default', 'priority', 'flex'],
  '只要至少一个已选模型支持，非标准服务等级就应开放覆盖配置'
)
assert.deepEqual(
  availableAccountGptReasoningEffortOptions(apiKeyCapabilities).map((option) => option.value),
  ['', 'low', 'medium', 'high', 'max', 'xhigh'],
  '账户思考级别选项必须包含全部已选模型能力的合集'
)

const unknownCapabilities = accountGptRequestOverrideCapabilities({
  accountType: 'api_key',
  modelOptions,
  supportedModels: ['gpt-5.6-sol', 'gpt-unknown']
})
assert.deepEqual(
  unknownCapabilities,
  { serviceTiers: ['priority', 'flex'], reasoningEfforts: ['low', 'medium', 'high', 'max'] },
  '能力未知模型不贡献选项，也不能清空其他已知模型能力'
)

const geminiCapabilities = accountGptRequestOverrideCapabilities({
  providerCode: 'gemini',
  accountType: 'api_key',
  modelOptions: [{
    label: 'gemini-test',
    value: 'gemini-test',
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['low', 'high']
  }],
  supportedModels: ['gemini-test']
})
assert.deepEqual(geminiCapabilities.serviceTiers, ['priority'], 'Gemini 服务等级控件必须读取官方 service_tier 能力')
assert.deepEqual(geminiCapabilities.reasoningEfforts, ['low', 'high'], 'Gemini thinking level 有明确映射时应显示思考级别控件')
assert.deepEqual(accountGptRequestOverrideCapabilities({
  providerCode: 'gemini',
  accountType: 'google_oauth',
  modelOptions: geminiCapabilities ? [{
    label: 'gemini-test', value: 'gemini-test', supportedServiceTiers: ['priority'], supportedReasoningEfforts: ['low', 'high']
  }] : [],
  supportedModels: ['gemini-test'],
  supportedEndpointModes: ['interactions_json', 'interactions_sse']
}), { serviceTiers: [], reasoningEfforts: [] }, 'Interactions-only Gemini 账户不能暴露不会生效的 GenerateContent 覆盖能力')

const deepSeekCapabilities = accountGptRequestOverrideCapabilities({
  providerCode: 'deepseek',
  accountType: 'api_key',
  modelOptions: [{
    label: 'deepseek-test',
    value: 'deepseek-test',
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['high']
  }],
  supportedModels: ['deepseek-test']
})
assert.deepEqual(
  deepSeekCapabilities,
  { serviceTiers: [], reasoningEfforts: [] },
  'DeepSeek 没有账户覆盖 driver 时不能因手工目录声明而显示无效控件'
)

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
assert.deepEqual(oauthFlexOnlyCapabilities.serviceTiers, ['flex'], 'OAuth 必须与 API Key 一样使用模型目录声明的 Flex 能力')
assert.deepEqual(
  availableAccountGptServiceTierOptions(oauthFlexOnlyCapabilities).map((option) => option.value),
  ['', 'default', 'flex'],
  'OAuth Flex 模型必须提供默认和 Flex 覆盖选项'
)

const emptyCapabilities = accountGptRequestOverrideCapabilities({
  accountType: 'api_key',
  modelOptions: [{
    label: 'gpt-empty-capabilities',
    value: 'gpt-empty-capabilities',
    supportedServiceTiers: [],
    supportedReasoningEfforts: []
  }],
  supportedModels: ['gpt-empty-capabilities']
})
assert.deepEqual(
  availableAccountGptServiceTierOptions(emptyCapabilities).map((option) => option.value),
  [''],
  '服务等级能力为空时仍必须提供不覆盖选项，供页面保留禁用字段'
)
assert.deepEqual(
  availableAccountGptReasoningEffortOptions(emptyCapabilities).map((option) => option.value),
  [''],
  '思考级别能力为空时仍必须提供不覆盖选项，供页面保留禁用字段'
)
const staleServiceTierOptions = availableAccountGptServiceTierOptions(emptyCapabilities, 'flex')
assert.deepEqual(staleServiceTierOptions.map((option) => option.value), ['', 'flex'], '失效服务等级配置必须保留为当前配置选项并允许用户清除')
assert.equal(staleServiceTierOptions.find((option) => option.value === 'flex')?.disabled, true, '失效服务等级不能伪装成当前可用能力')
const staleReasoningOptions = availableAccountGptReasoningEffortOptions(emptyCapabilities, 'max')
assert.deepEqual(staleReasoningOptions.map((option) => option.value), ['', 'max'], '失效思考级别配置必须保留为当前配置选项并允许用户清除')
assert.equal(staleReasoningOptions.find((option) => option.value === 'max')?.disabled, true, '失效思考级别不能伪装成当前可用能力')

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
  undefined,
  'OAuth 保存前必须按模型目录能力接受 Flex'
)

const customModelForm = {
  ...emptyCustomModelForm,
  model: 'gpt-custom-capabilities',
  supportedServiceTiers: ['priority', 'priority', 'flex'],
  supportedReasoningEfforts: ['none', 'high', 'ultra', 'max'] as unknown as ProviderModelReasoningEffort[]
}
const customPayload = buildCustomModelPayload(customModelForm, 'text', { includeRequestCapabilities: true })
assert.deepEqual(customPayload?.supportedServiceTiers, ['priority', 'flex'], '自定义模型服务等级应去重并限制 wire 枚举')
assert.equal(Object.hasOwn(customPayload ?? {}, 'catalogVisible'), false, '自定义模型不再提交发布到模型接口字段')
assert.deepEqual(customPayload?.supportedReasoningEfforts, ['high', 'ultra', 'max'], 'none 表示关闭思考，不应作为思考能力级别提交')
assert.equal(customPayload?.defaultReasoningEffort, null, '未选择默认思考级别时应显式清空')
customModelForm.defaultReasoningEffort = 'high'
assert.equal(buildCustomModelPayload(customModelForm, 'text', { includeRequestCapabilities: true })?.defaultReasoningEffort, null, '自定义模型即使表单残留默认值也必须清空，不能替上游做客户端决策')
const ordinaryUserPayload = buildCustomModelPayload(customModelForm, 'text', {
  includeRequestCapabilities: true,
  includePrices: false
})
for (const priceField of [
  'inputUsdPer1M', 'outputUsdPer1M', 'cachedInputUsdPer1M', 'cacheWriteUsdPer1M', 'cacheWrite1hUsdPer1M',
  'serviceTierPrices', 'imageInputUsdPer1M', 'imageOutputUsdPer1M', 'audioInputUsdPer1M', 'audioOutputUsdPer1M',
  'outputUsdPerImage'
]) {
  assert.equal(Object.prototype.hasOwnProperty.call(ordinaryUserPayload, priceField), false, `普通用户 payload 必须省略价格字段 ${priceField}`)
}
customModelForm.serviceTierPrices = { priority: { inputUsdPer1M: 2 }, orphan: { inputUsdPer1M: 9 } }
reconcileCustomModelServiceTierPrices(customModelForm)
assert.deepEqual(Object.keys(customModelForm.serviceTierPrices), ['priority', 'flex'], '动态档位价格行必须只保留当前支持档位并补齐缺失行')
applyConfigurationTemplateToCustomModelForm(customModelForm, [providerModel({
  id: 'template-pricing',
  model: 'template-pricing',
  supportedServiceTiers: ['priority', 'flex'],
  supportedReasoningEfforts: ['low', 'high'],
  defaultReasoningEffort: 'high',
  serviceTierPrices: { priority: { inputUsdPer1M: 3 } }
})], 'template-pricing')
assert.equal(customModelForm.defaultReasoningEffort, undefined, '配置模板不能把内置默认思考级别继承到新自定义模型')
assert.deepEqual(Object.keys(customModelForm.serviceTierPrices), ['priority', 'flex'], '价格模板应用后必须按当前档位重建价格行')
assert.deepEqual(customModelForm.serviceTierPrices.flex, {}, '模板缺少的当前档位必须补空行，避免渲染访问 undefined')
assert.equal(
  buildCustomModelCapabilityOptions('gpt', [], ['none', 'low']).reasoningEfforts.some((option) => option.value === 'none'),
  false,
  '新增自定义模型的思考能力选项不得包含 none'
)
assert.deepEqual(
  customModelPriceFields('anthropic', 'text').map((field) => [field.key, field.label]),
  [
    ['inputUsdPer1M', '输入'],
    ['cachedInputUsdPer1M', '缓存读'],
    ['cacheWriteUsdPer1M', '5m 缓存写入'],
    ['cacheWrite1hUsdPer1M', '1h 缓存写入'],
    ['outputUsdPer1M', '输出']
  ],
  'Anthropic 自定义模型必须使用厂商自己的缓存计费字段'
)
assert.deepEqual(
  customModelPriceFields('gemini', 'text').map((field) => field.key),
  ['inputUsdPer1M', 'cachedInputUsdPer1M', 'cacheStorageUsdPer1MPerHour', 'audioInputUsdPer1M', 'outputUsdPer1M'],
  'Gemini 文本模型必须补充缓存存储和音频输入价格'
)
assert.deepEqual(
  customModelTierPriceFields('xai').map((field) => field.key),
  ['inputUsdPer1M', 'cachedInputUsdPer1M', 'outputUsdPer1M'],
  'xAI 服务档位不能显示未公布的缓存写入字段'
)
clearCustomModelPricesOutsideCategory(customModelForm, 'image')
assert.deepEqual(customModelForm.serviceTierPrices, {}, '切换到非文本模型必须清空服务档位价格')
assert(availableCustomModelStatusOptions(false, 'active').some((option) => option.value === 'active'), '普通用户编辑原 active 模型时必须稳定保留 active 选项')
assert.equal(availableCustomModelStatusOptions(false, 'draft').some((option) => option.value === 'active'), true, '普通用户创建或编辑自有模型时可以启用')
const imageCustomPayload = buildCustomModelPayload(customModelForm, 'image', { includeRequestCapabilities: true })
assert.deepEqual(imageCustomPayload?.supportedServiceTiers, [], 'GPT 非文本自定义模型保存时必须清空服务等级能力')
assert.deepEqual(imageCustomPayload?.supportedReasoningEfforts, [], 'GPT 非文本自定义模型保存时必须清空思考能力')
assert.equal(imageCustomPayload?.defaultReasoningEffort, null, '非文本模型必须显式清空默认思考级别')

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
assert.equal(loadedCustomForm.defaultReasoningEffort, 'high', '自定义模型编辑表单必须恢复默认思考级别')
assert.doesNotMatch(formatModelRequestCapabilities(catalogRecord), /Codex|Ultra|多代理/, '通用模型目录格式化只展示 API 请求能力')
assert.doesNotMatch(
  formatModelReasoningCapabilities(catalogRecord),
  /服务等级|Priority|Flex/,
  '移动端思考级别不能重复展示服务等级'
)
assert.doesNotMatch(
  availableAccountGptReasoningEffortOptions(apiKeyCapabilities).map((option) => option.value).join(','),
  /ultra/,
  '账户请求覆盖永远不能显示 Ultra'
)

console.log('GPT 请求覆盖前端回归通过：模型能力合集、OAuth Flex、常显控件、失效配置保留和持久化均符合契约')

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

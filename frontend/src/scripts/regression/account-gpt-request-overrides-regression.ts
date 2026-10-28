import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  accountGptRequestOverrideCapabilities,
  accountGptRequestOverridesForForm,
  availableAccountGptReasoningEffortOptions,
  availableAccountGptServiceTierOptions,
  isAccountRequestOverrideProviderSupported
} from '../../views/accounts/accountGptRequestOverrides'
import { accountRequestOverrideFamilyOf } from '../../views/accounts/accountRequestOverrideVocab'
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
  formatTokens,
  modelPriceSlots
} from '../../views/providers/providerModelFormatters'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import type {
  ProviderModelPricing,
  ProviderModelReasoningEffort
} from '../../types/domain'
import type { AccountFormModel } from '../../views/accounts/accountFormTypes'
import type { AccountModelSelectOption } from '../../views/accounts/accountEditFormPayload'

assert.deepEqual(
  modelPriceSlots(providerModel({
    inputUsdPer1M: 1.25,
    outputUsdPer1M: 2.5,
    cachedInputUsdPer1M: 0.1
  })),
  [
    { label: '输入', value: '1.25', unit: '/1M tokens' },
    { label: '输出', value: '2.5', unit: '/1M tokens' },
    { label: '缓存读', value: '0.1', unit: '/1M tokens' }
  ],
  'text 模型卡片必须按候选序列展示输入/输出/缓存读三槽'
)
assert.deepEqual(
  modelPriceSlots(providerModel({ mode: 'audio', ttsInputUsdPer1MChars: 20 })),
  [{ label: '语音合成', value: '20', unit: '/1M 字符' }],
  '音频目录行必须命中字符价槽位，不得因固定三槽隐藏价格区'
)
assert.deepEqual(
  modelPriceSlots(providerModel({ mode: 'video', videoOutputUsdPerSecond: 0.5 })),
  [{ label: '视频（每秒）', value: '0.5', unit: '/秒' }],
  '视频目录行必须命中秒价槽位'
)
assert.deepEqual(modelPriceSlots(providerModel()), [], '全部直价字段为空才允许返回空槽位')
assert.equal(modelPriceSlots(providerModel({ inputUsdPer1M: 0 }))[0]?.value, '0', '显式 0 是真实价，必须保留槽位')
assert.deepEqual(
  modelPriceSlots(providerModel({
    inputUsdPer1M: 2,
    outputUsdPer1M: 2,
    catalogDisplay: [
      { key: 'token_pricing', label: 'Token 计费', items: [{ key: 'token', label: 'Token', format: 'usd_per_1m_tokens', value: 2 }] }
    ]
  })),
  [{ label: 'Token', value: '2', unit: '/1M tokens' }],
  '后端投影呈单 Token 条目时卡片必须合并为单槽 Token'
)
assert.deepEqual(
  modelPriceSlots(providerModel({ inputUsdPer1M: 2, outputUsdPer1M: 2 })).map((slot) => slot.label),
  ['输入', '输出'],
  '无单 Token 投影的同值行必须保持输入/输出两槽，不做字段级全局合并'
)
assert.equal(formatTokens(131_072), '128K', '模型容量只显示 K，不追加精确括号数字')
assert.equal(formatTokens(1_048_576), '1M', '模型容量只显示 M，不追加精确括号数字')
// 模型目录卡片化形态已随模型广场退役平移：源码断言目标从 ProviderModelCatalogModal.vue 换为 ModelPlazaView.vue
// （模型广场页与供应商目录退役设计 §5），四条契约（动态 section 循环、槽位接线、不回退固定列、不消费 codex* 字段）原样保留。
const plazaViewSource = readFileSync(new URL('../../views/providers/ModelPlazaView.vue', import.meta.url), 'utf8')
// 模型广场页与供应商目录退役设计 + 卡片化 §6.2：抽屉「计费价格」为一张价格表，
// 动态 section 经 modelPriceTable 合并配对、非价格分组走 modelInfoItems。
assert.match(plazaViewSource, /modelPriceTable\(record\)/, '详情抽屉「计费价格」必须按一张价格表渲染当前模型的动态 section')
assert.match(plazaViewSource, /modelInfoItems\(record\)/, '非价格分组必须经「基础信息」网格渲染当前模型的动态 section')
assert.match(plazaViewSource, /modelPriceSlots\(/, '模型目录卡片必须接线 modelPriceSlots 槽位契约')
assert.doesNotMatch(plazaViewSource, /column\.key === '(?:serviceTiers|reasoningEfforts|prices|cacheWrite|context)'/, '模型目录不能继续渲染固定计费或能力列')
assert.doesNotMatch(plazaViewSource, /codexSupportedReasoningLevels/, '模型目录思考列只能消费 API reasoning effort')
assert.doesNotMatch(plazaViewSource, /codexMultiAgentVersion/, '模型目录思考列不能展示 Codex 多代理能力')
const requestOverridesSectionSource = readFileSync(new URL('../../views/accounts/AccountGptRequestOverridesSection.vue', import.meta.url), 'utf8')
const batchEditModalSource = readFileSync(new URL('../../views/accounts/AccountBatchEditModal.vue', import.meta.url), 'utf8')
const accountSavePayloadSource = readFileSync(new URL('../../views/accounts/accountSavePayload.ts', import.meta.url), 'utf8')

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

// 单账户配置区：区块按供应商级能力显示，字段按族集 + 声明能力分别显示
assert.match(requestOverridesSectionSource, /v-if="requestOverridesSupported"/, '支持账户覆盖的供应商必须保留配置区')
assert.doesNotMatch(requestOverridesSectionSource, /serviceTierOptions\.length \|\| reasoningEffortOptions\.length/, '单账户配置区不能因能力为空整体隐藏')
assert.match(requestOverridesSectionSource, /<a-form-item v-if="serviceTierFieldVisible"/, '服务等级字段必须按供应商级声明能力显示')
assert.match(requestOverridesSectionSource, /<a-form-item v-if="reasoningEffortFieldVisible"/, '思考级别字段必须按供应商级声明能力显示')
assert.doesNotMatch(requestOverridesSectionSource, /watch\(/, '模型能力变化不能静默清空已保存覆盖')
assert.doesNotMatch(
  requestOverridesSectionSource,
  /当前服务等级配置不受已选模型支持|当前思考级别配置不受已选模型支持|当前已选模型未声明可用服务等级|当前已选模型未声明可用思考级别/,
  '按已选模型判定的警告族必须整体删除'
)
assert.match(
  requestOverridesSectionSource,
  /requestOverridesSupported = computed\(\(\) => isAccountRequestOverrideProviderSupported\([\s\S]*&& \(serviceTierFieldVisible\.value \|\| reasoningEffortFieldVisible\.value\)\)/,
  'Interactions-only Gemini 守卫必须绝对生效，且区块按字段可见性显示'
)
assert.doesNotMatch(
  requestOverridesSectionSource,
  /\|\| Boolean\(props\.form\.serviceTierOverride/,
  'Interactions-only Gemini 不再因存量覆盖值绕过守卫显示（该协议面覆盖不会生效）'
)
assert.doesNotMatch(
  requestOverridesSectionSource,
  /:disabled="readonly \|\| modelsLoading/,
  '下拉不再因能力加载或能力为空而禁用（族集非空即始终可选）'
)
// 批量编辑：字段按供应商级能力显示，能力计算不再传 accountType、不再按已选模型
assert.match(batchEditModalSource, /v-if="requestOverridesSupported"/, '批量编辑必须为支持供应商保留请求覆盖字段')
assert.match(batchEditModalSource, /v-if="serviceTierOverrideFieldVisible"/, '批量编辑服务等级字段必须按供应商级声明能力显示')
assert.match(batchEditModalSource, /v-if="reasoningEffortOverrideFieldVisible"/, '批量编辑思考级别字段必须按供应商级声明能力显示')
assert.doesNotMatch(batchEditModalSource, /当前已选模型未声明可用服务等级|当前已选模型未声明可用思考级别/, '批量编辑不得保留按已选模型判定的说明')
assert.doesNotMatch(batchEditModalSource, /accountType: homogeneousAccount\.value\?\.type/, '批量编辑能力计算不得再传 accountType')
assert.match(batchEditModalSource, /protocolCode: homogeneousAccount\.value\?\.protocolCode/, '批量编辑能力计算必须携带协议码用于族判定')
assert.doesNotMatch(batchEditModalSource, /supportedModels: effectiveBatchModels\.value/, '批量编辑能力计算不得再依赖已选模型')
// 保存前能力校验删除：值域由后端族集校验负责
assert.doesNotMatch(accountSavePayloadSource, /validateAccountGptRequestOverrides/, '保存前覆盖能力校验函数必须删除')
assert.doesNotMatch(accountSavePayloadSource, /所选支持模型中没有模型支持/, '保存前不得再按已选模型拦截覆盖字段')
assert.equal(isAccountRequestOverrideProviderSupported('gpt'), true, 'GPT 账户必须显示请求覆盖配置区')
assert.equal(isAccountRequestOverrideProviderSupported('openai'), true, 'OpenAI-compatible 账户必须显示请求覆盖配置区')
assert.equal(isAccountRequestOverrideProviderSupported('anthropic'), true, 'Anthropic 账户必须显示请求覆盖配置区（服务等级字段按空族集隐藏）')
assert.equal(isAccountRequestOverrideProviderSupported('gemini'), true, 'Gemini 账户必须显示已实现的思考级别覆盖配置区')
assert.equal(
  isAccountRequestOverrideProviderSupported('gemini', ['interactions_json', 'interactions_sse']),
  false,
  'Interactions-only Gemini 账户不得显示 GenerateContent 请求覆盖配置区'
)
assert.equal(isAccountRequestOverrideProviderSupported('deepseek'), true, '目录声明能力的供应商必须显示请求覆盖配置区')
assert.equal(isAccountRequestOverrideProviderSupported('glm'), true, '目录声明能力的供应商必须显示请求覆盖配置区')
assert.equal(isAccountRequestOverrideProviderSupported('xai'), true, '目录声明能力的供应商必须显示请求覆盖配置区')
assert.equal(isAccountRequestOverrideProviderSupported('volcengine'), true, '目录声明能力的供应商必须显示请求覆盖配置区')

// 族集全集（前端镜像，单一真源为后端 requestoverride 包）：不随 modelOptions 变化
const openaiCapabilities = accountGptRequestOverrideCapabilities({ providerCode: 'gpt', modelOptions: [] })
assert.deepEqual(
  openaiCapabilities.reasoningEfforts,
  ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'],
  'openai 族思考级别全集 = none,minimal,low,medium,high,xhigh,max（不含 ultra）'
)
assert.deepEqual(openaiCapabilities.serviceTiers, ['default', 'priority', 'flex'], 'openai 族服务等级全集 = default/priority/flex')
assert.equal(openaiCapabilities.family, 'openai', '未知/默认供应商回落 openai 族')
assert.deepEqual(
  accountGptRequestOverrideCapabilities({ providerCode: 'deepseek', modelOptions: [] }).reasoningEfforts,
  ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'],
  'deepseek 账户思考级别选项 = 不覆盖 + openai 族全集，不再按已选模型过滤'
)
assert.equal(
  accountGptRequestOverrideCapabilities({ providerCode: 'not-a-known-provider', modelOptions: [] }).family,
  'openai',
  '未知供应商回落 openai 族'
)
const anthropicCapabilities = accountGptRequestOverrideCapabilities({ providerCode: 'anthropic', modelOptions: [] })
assert.deepEqual(anthropicCapabilities.reasoningEfforts, ['low', 'medium', 'high', 'xhigh', 'max'], 'anthropic 族思考级别最低档为 low')
assert.deepEqual(anthropicCapabilities.serviceTiers, [], 'anthropic 族不提供服务等级选项')
assert.deepEqual(
  availableAccountGptServiceTierOptions(anthropicCapabilities).map((option) => option.value),
  [''],
  'anthropic 服务等级族集为空：只有不覆盖选项，UI 按声明能力隐藏该字段'
)
const geminiCapabilities = accountGptRequestOverrideCapabilities({ providerCode: 'gemini', modelOptions: [] })
assert.deepEqual(geminiCapabilities.reasoningEfforts, ['minimal', 'low', 'medium', 'high'], 'gemini 族思考级别最高档为 high')
assert.deepEqual(geminiCapabilities.serviceTiers, ['default', 'priority', 'flex'], 'gemini 族服务等级全集与 openai 族一致')
// FamilyOf 镜像：协议族优先，协议码取不到时回落供应商族
assert.equal(accountRequestOverrideFamilyOf('gpt', 'anthropic_messages'), 'anthropic', '协议族判定协议码优先')
assert.equal(accountRequestOverrideFamilyOf('anthropic', 'openai'), 'openai', '协议码 openai 覆盖供应商回落')
assert.equal(accountRequestOverrideFamilyOf('deepseek', ''), 'openai', '协议码取不到时按供应商码回落')
assert.equal(accountRequestOverrideFamilyOf('deepseek'), 'openai', '协议码缺省时按供应商码回落')
// 供应商级声明能力只决定字段显示，不收窄值域
const declaredFlags = accountGptRequestOverrideCapabilities({
  providerCode: 'gpt',
  modelOptions: [{ value: 'm1', supportedReasoningEfforts: ['high'] }]
})
assert.equal(declaredFlags.declaredReasoningEfforts, true, '存在声明思考级别的模型时供应商级声明为真')
assert.equal(declaredFlags.declaredServiceTiers, false, '无模型声明服务档位时供应商级声明为假')
assert.deepEqual(
  declaredFlags.reasoningEfforts,
  ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'],
  '目录声明只影响显示，不收窄协议族值域'
)
// Interactions-only Gemini：能力与守卫同时归零
assert.deepEqual(
  accountGptRequestOverrideCapabilities({
    providerCode: 'gemini',
    modelOptions: [{ value: 'gemini-test', supportedServiceTiers: ['priority'], supportedReasoningEfforts: ['low', 'high'] }],
    supportedEndpointModes: ['interactions_json', 'interactions_sse']
  }),
  { family: 'gemini', serviceTiers: [], reasoningEfforts: [], declaredServiceTiers: false, declaredReasoningEfforts: false },
  'Interactions-only Gemini 账户不能暴露不会生效的 GenerateContent 覆盖能力'
)

// 下拉选项：族集全集 + 不覆盖；存量值在族集内时不出现在末尾
assert.deepEqual(
  availableAccountGptServiceTierOptions(openaiCapabilities).map((option) => option.value),
  ['', 'default', 'priority', 'flex'],
  'openai 族服务等级选项 = 不覆盖 + default/priority/flex'
)
assert.deepEqual(
  availableAccountGptReasoningEffortOptions(openaiCapabilities).map((option) => option.value),
  ['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'],
  'openai 族思考级别选项 = 不覆盖 + 族全集'
)
assert.doesNotMatch(
  availableAccountGptReasoningEffortOptions(openaiCapabilities).map((option) => option.value).join(','),
  /ultra/,
  '账户请求覆盖永远不能显示 Ultra'
)
// 存量值不在族集内：保留为「当前配置」禁用项，供用户改选或清除
const staleServiceTierOptions = availableAccountGptServiceTierOptions(openaiCapabilities, 'auto')
assert.deepEqual(
  staleServiceTierOptions.map((option) => option.value),
  ['', 'default', 'priority', 'flex', 'auto'],
  '族集外服务等级存量值必须保留为当前配置选项'
)
assert.equal(staleServiceTierOptions.find((option) => option.value === 'auto')?.disabled, true, '族集外服务等级存量值不能伪装成可用能力')
assert.match(
  staleServiceTierOptions.find((option) => option.value === 'auto')?.label ?? '',
  /不在当前协议族值域内/,
  '族集外存量值必须以「当前配置：X（不在当前协议族值域内）」呈现'
)
const staleReasoningOptions = availableAccountGptReasoningEffortOptions(openaiCapabilities, 'ultra')
assert.deepEqual(
  staleReasoningOptions.map((option) => option.value),
  ['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'],
  '族集外思考级别存量值（如 ultra）必须保留为当前配置选项'
)
assert.equal(staleReasoningOptions.find((option) => option.value === 'ultra')?.disabled, true, '族集外思考级别存量值不能伪装成可用能力')
assert.deepEqual(
  availableAccountGptServiceTierOptions(openaiCapabilities, 'priority').map((option) => option.value),
  ['', 'default', 'priority', 'flex'],
  '族集内存量值不再追加「当前配置」项'
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

// 保存校验（2026-10-18 简化定案）：前端不再做覆盖字段能力/值域校验，
// 族内值与族外存量值都放行，值域由后端族集校验负责
oauthForm.serviceTierOverride = 'flex'
oauthForm.reasoningEffortOverride = 'max'
// 国外 OAuth 供应商（gpt 等）的 refresh_token 表单必须绑定出海代理后才能保存，与后端创建拦截一致
oauthForm.proxyProfileId = 'proxy-overseas-regression'
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
  'OAuth 保存必须接受族内 Flex/Max 覆盖'
)
oauthForm.serviceTierOverride = 'auto'
oauthForm.reasoningEffortOverride = 'ultra'
assert.equal(
  validateAccountSaveForm({
    form: oauthForm,
    hasAuthSession: true,
    errorPolicyRules: [],
    responseInspectionRules: [],
    mappingUpstreamModelOptions: [],
    providers: FALLBACK_PROVIDERS
  }),
  undefined,
  '族集外存量覆盖值不再阻止前端保存（值域由后端族集校验负责）'
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

console.log('GPT 请求覆盖前端回归通过：协议族全集选项、供应商级显示、Interactions-only 守卫、存量值保留和持久化均符合契约')

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

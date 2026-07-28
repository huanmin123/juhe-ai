import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  applyConfigurationTemplateToCustomModelForm,
  availableCustomModelStatusOptions,
  buildCustomModelCapabilityOptions,
  buildCustomModelMutationPatch,
  buildCustomModelPayload,
  canManageModelPricesForView,
  emptyCustomModelForm,
  availableCustomModelModeOptions
} from '../../src/views/providers/customProviderModelForm'
import {
  apiProtocolOptions,
  defaultProtocolsForProviderModelCategory,
  formatModelCatalogDisplayValue,
  formatTokens
} from '../../src/views/providers/providerModelFormatters'
import { buildConfigurationTemplateOptions } from '../../src/views/providers/providerModelTableState'
import type { ProviderDefinition, ProviderModelPricing } from '../../src/types/domain'

const template = providerModel({
  id: 'provider_model_gpt_5_6_sol',
  model: 'gpt-5.6-sol',
  mode: 'text',
  supportedApiProtocols: ['responses', 'chat_completions'],
  supportedServiceTiers: ['priority', 'flex'],
  supportedReasoningEfforts: ['none', 'low', 'medium', 'high', 'xhigh', 'max'],
  defaultReasoningEffort: 'high',
  releaseDate: '2026-06-26',
  shutdownDate: '2027-06-26',
  contextWindowTokens: 1_050_000,
  maxInputTokens: 922_000,
  maxOutputTokens: 128_000,
  inputUsdPer1M: 5,
  outputUsdPer1M: 30,
  cachedInputUsdPer1M: 0.5,
  cacheWriteUsdPer1M: 6.25,
  serviceTierPrices: {
    priority: { inputUsdPer1M: 10, outputUsdPer1M: 60 },
    flex: { inputUsdPer1M: 2.5, outputUsdPer1M: 15 }
  }
})

const options = buildConfigurationTemplateOptions([template], '', 'text')
assert.deepEqual(options, [{ value: template.id, label: 'gpt-5.6-sol（内置）' }], '配置模板应直接使用已有模型 ID')
const personalTemplate = providerModel({ id: 'personal-template', model: 'personal-template', scope: 'personal' })
assert.equal(
  buildConfigurationTemplateOptions([template, personalTemplate], '', 'text', 'global').some((item) => item.value === personalTemplate.id),
  false,
  '管理员创建全局模型时不得保留个人配置模板'
)
for (const protocol of ['generate_content', 'stream_generate_content', 'count_tokens', 'embed_content']) {
  assert(apiProtocolOptions.some((item) => item.value === protocol), `Gemini 原生协议 ${protocol} 必须出现在接口协议选项`)
}
const geminiProvider = {
  id: 'provider_gemini', code: 'gemini', name: 'Gemini', enabled: true,
  defaultProtocolProfileId: 'profile_gemini_native_v1beta', protocolCode: 'gemini_native', protocolVersion: 'v1beta',
  baseUrl: 'https://generativelanguage.googleapis.com/v1beta', defaultHealthCheckModel: 'gemini-3.5-flash',
  defaultSupportedModels: ['gemini-3.5-flash'], accountTypes: ['api_key'], capabilities: [],
  protocolProfiles: [{
    id: 'profile_gemini_native_v1beta', providerCode: 'gemini', name: 'Gemini Native', enabled: true,
    protocolCode: 'gemini_native', protocolVersion: 'v1beta', baseUrl: 'https://generativelanguage.googleapis.com/v1beta',
    defaultHealthCheckModel: 'gemini-3.5-flash', accountTypes: ['api_key'], capabilities: [],
    endpointFamilies: ['generate_content', 'stream_generate_content', 'count_tokens', 'embed_content'].map((code) => ({ code, name: code }))
  }]
} as ProviderDefinition
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(geminiProvider, 'text'),
  ['generate_content', 'stream_generate_content', 'count_tokens', 'embed_content'],
  'Gemini 文本模型必须从原生协议档案生成默认协议，不能回退到 OpenAI Chat/Responses'
)

const form = { ...emptyCustomModelForm, model: 'my-gpt-model', serviceTierPrices: {} }
applyConfigurationTemplateToCustomModelForm(form, [template], template.id)
assert.equal(form.configurationTemplateId, template.id, '表单应记录本次复制来源供服务端可信继承价格')
assert.equal(form.mode, 'text')
assert.deepEqual(form.supportedApiProtocols, ['responses', 'chat_completions'])
assert.deepEqual(form.supportedServiceTiers, ['priority', 'flex'])
assert.deepEqual(form.supportedReasoningEfforts, ['low', 'medium', 'high', 'xhigh', 'max'])
assert.equal(form.defaultReasoningEffort, undefined, '配置模板不得给新增自定义模型复制默认思考级别')
assert.equal(form.releaseDate, '2026-06-26', '配置模板必须复制发布时间')
assert.equal(form.shutdownDate, '2027-06-26', '配置模板必须复制停用时间')
assert.equal(form.contextWindowTokens, 1_050_000)
assert.equal(form.maxInputTokens, 922_000)
assert.equal(form.maxOutputTokens, 128_000)
assert.equal(form.inputUsdPer1M, 5)
assert.deepEqual(form.serviceTierPrices, template.serviceTierPrices)

const userPayload = buildCustomModelPayload(form, 'text', { includeRequestCapabilities: true, includePrices: false })
assert.equal(userPayload?.configurationTemplateId, template.id, '普通用户请求应提交配置模板 ID')
assert.equal('inputUsdPer1M' in (userPayload ?? {}), false, '普通用户请求仍不得提交价格')
assert.equal(userPayload?.defaultReasoningEffort, null, '创建契约必须明确由上游决定默认思考级别')
const mutationPatch = buildCustomModelMutationPatch(
  { ...userPayload, maxOutputTokens: 128_000 },
  { ...userPayload, maxOutputTokens: 64_000 }
)
assert.deepEqual(mutationPatch, { maxOutputTokens: 64_000 }, '编辑模型必须只提交实际变化字段')
assert.deepEqual(
  buildCustomModelMutationPatch(userPayload ?? {}, structuredClone(userPayload ?? {})),
  {},
  '编辑模型没有变化时必须生成空 PATCH'
)
const providersViewSource = readFileSync(new URL('../../src/views/providers/ProvidersView.vue', import.meta.url), 'utf8')
assert.match(
  providersViewSource,
  /function buildCurrentCustomModelPayload\(\)[\s\S]+?includeDefaultReasoningEffort: false/,
  '自定义模型编辑提交也必须固定清空默认思考级别'
)
assert(availableCustomModelStatusOptions(false).some((option) => option.value === 'active'), '普通用户新增模型必须可以选择启用')

const capabilityOptions = buildCustomModelCapabilityOptions('gpt', [], [])
assert(capabilityOptions.serviceTiers.some((option) => option.value === 'priority'), '没有现存模型时仍必须可选 Priority 服务等级')
assert(capabilityOptions.serviceTiers.some((option) => option.value === 'flex'), '没有现存模型时仍必须可选 Flex 服务等级')
assert.equal(capabilityOptions.reasoningEfforts.some((option) => option.value === 'none'), false, '关闭思考不得伪装成 reasoning effort 选项')
assert(capabilityOptions.reasoningEfforts.some((option) => option.value === 'max'), '没有现存模型时仍必须可选 Max 思考级别')
const deepSeekCapabilityOptions = buildCustomModelCapabilityOptions('deepseek', [], [])
assert.deepEqual(deepSeekCapabilityOptions.serviceTiers, [], '不能把 GPT Priority/Flex 候选注入 DeepSeek')
assert.deepEqual(deepSeekCapabilityOptions.reasoningEfforts, [], '不能把 GPT reasoning effort 候选注入 DeepSeek')
assert.deepEqual(availableCustomModelModeOptions('gpt', []).map((option) => option.value), ['text', 'image'], 'GPT 新目录只允许创建当前支持的文本与图像模型')
assert.deepEqual(availableCustomModelModeOptions('deepseek', []).map((option) => option.value), ['text'], 'DeepSeek 不能显示未支持的图像或音频用途')
assert.deepEqual(
  availableCustomModelModeOptions('gemini', [providerModel({ providerCode: 'gemini', model: 'gemini-image', mode: 'image' })]).map((option) => option.value),
  ['text', 'image'],
  '其他供应商用途必须来自自身目录事实'
)
assert.equal(canManageModelPricesForView(true, true), true, '管理员管理视图可以维护价格')
assert.equal(canManageModelPricesForView(false, true), true, '管理员进入我的模型时应能维护自己个人模型的价格')
assert.equal(canManageModelPricesForView(false, false), true, '普通用户应能维护自己个人模型的价格')

assert.equal(formatModelCatalogDisplayValue({ key: 'contextWindowTokens', label: '总上下文', format: 'tokens', value: template.contextWindowTokens ?? 0 }), '1.05M', '动态目录应格式化总上下文容量')
assert.equal(formatModelCatalogDisplayValue({ key: 'maxInputTokens', label: '最大输入', format: 'tokens', value: template.maxInputTokens ?? 0 }), '922K', '动态目录应独立格式化最大输入容量')
assert.equal(formatTokens(template.maxOutputTokens), '128K', '最大输出应独立展示')
assert.equal(formatModelCatalogDisplayValue({ key: 'contextWindowTokens', label: '总上下文', format: 'tokens', value: 200_000 }), '200K', '动态目录容量项必须只格式化服务端明确提供的值')

console.log('自定义模型配置复制回归通过')

function providerModel(overrides: Partial<ProviderModelPricing> = {}): ProviderModelPricing {
  return {
    providerCode: 'gpt',
    model: 'gpt-5.6-sol',
    source: 'built-in',
    scope: 'built_in',
    status: 'active',
    supportsPromptCaching: true,
    supportsServiceTier: true,
    ...overrides
  }
}

import { strict as assert } from 'node:assert'

import {
  applyConfigurationTemplateToCustomModelForm,
  availableCustomModelStatusOptions,
  buildCustomModelCapabilityOptions,
  buildCustomModelPayload,
  canManageModelPricesForView,
  emptyCustomModelForm,
  availableCustomModelModeOptions
} from '../../src/views/providers/customProviderModelForm'
import {
  apiProtocolOptions,
  defaultProtocolsForProviderModelCategory,
  formatModelContextTokens,
  formatModelInputTokens,
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
assert.deepEqual(form.supportedReasoningEfforts, ['none', 'low', 'medium', 'high', 'xhigh', 'max'])
assert.equal(form.defaultReasoningEffort, 'high', '配置模板必须复制默认思考级别')
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
assert.equal(userPayload?.defaultReasoningEffort, 'high', '创建契约必须提交模板复制的默认思考级别')
assert(availableCustomModelStatusOptions(false).some((option) => option.value === 'active'), '普通用户新增模型必须可以选择启用')

const capabilityOptions = buildCustomModelCapabilityOptions('gpt', [], [])
assert(capabilityOptions.serviceTiers.some((option) => option.value === 'priority'), '没有现存模型时仍必须可选 Priority 服务等级')
assert(capabilityOptions.serviceTiers.some((option) => option.value === 'flex'), '没有现存模型时仍必须可选 Flex 服务等级')
assert(capabilityOptions.reasoningEfforts.some((option) => option.value === 'none'), '没有现存模型时仍必须可选关闭思考')
assert(capabilityOptions.reasoningEfforts.some((option) => option.value === 'max'), '没有现存模型时仍必须可选 Max 思考级别')
const deepSeekCapabilityOptions = buildCustomModelCapabilityOptions('deepseek', [], [])
assert.deepEqual(deepSeekCapabilityOptions.serviceTiers, [], '不能把 GPT Priority/Flex 候选注入 DeepSeek')
assert.deepEqual(deepSeekCapabilityOptions.reasoningEfforts, [], '不能把 GPT reasoning effort 候选注入 DeepSeek')
assert.deepEqual(availableCustomModelModeOptions('gpt', []).map((option) => option.value), ['text', 'image', 'audio'], 'GPT 新目录必须可创建三类已支持模型')
assert.deepEqual(availableCustomModelModeOptions('deepseek', []).map((option) => option.value), ['text'], 'DeepSeek 不能显示未支持的图像或音频用途')
assert.deepEqual(
  availableCustomModelModeOptions('gemini', [providerModel({ providerCode: 'gemini', model: 'gemini-image', mode: 'image' })]).map((option) => option.value),
  ['text', 'image'],
  '其他供应商用途必须来自自身目录事实'
)
assert.equal(canManageModelPricesForView(true, true), true, '管理员管理视图可以维护价格')
assert.equal(canManageModelPricesForView(false, true), false, '管理员进入我的模型时必须与普通用户保持一致，不能显示价格维护')
assert.equal(canManageModelPricesForView(true, false), false, '普通用户不能维护价格')

assert.equal(formatModelContextTokens(template), '1.05M', '总上下文应读取 contextWindowTokens')
assert.equal(formatModelInputTokens(template), '922K', '最大输入应只读取 maxInputTokens')
assert.equal(formatTokens(template.maxOutputTokens), '128K', '最大输出应独立展示')
assert.equal(formatModelInputTokens(providerModel({ contextWindowTokens: 200_000 })), '-', '未公布最大输入时不得拿总上下文回退')

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

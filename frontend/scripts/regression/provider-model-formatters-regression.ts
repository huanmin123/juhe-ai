import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  categoryFromModeOrModel,
  defaultProtocolsForModelCategory,
  defaultProtocolsForProviderModelCategory,
  formatModelCatalogDisplayValue,
  formatModelCategory,
  formatModelReasoningCapabilities,
  formatModelServiceTierCapabilities,
  formatTokens,
  getModelCategory,
  modelCatalogDisplaySections
} from '../../src/views/providers/providerModelFormatters'
import { buildProviderModelColumns } from '../../src/views/providers/providerModelTableState'
import type { ProviderDefinition, ProviderModelPricing } from '../../src/types/domain'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const formatterSource = readFileSync(resolve(frontendRoot, 'src/views/providers/providerModelFormatters.ts'), 'utf8')
const categoryRulesSource = readFileSync(resolve(frontendRoot, 'src/views/providers/providerModelCategoryRules.ts'), 'utf8')
const catalogModalSource = readFileSync(resolve(frontendRoot, 'src/views/providers/ProviderModelCatalogModal.vue'), 'utf8')
const providersViewSource = readFileSync(resolve(frontendRoot, 'src/views/providers/ProvidersView.vue'), 'utf8')
const providerTableConfigSource = readFileSync(resolve(frontendRoot, 'src/views/providers/providerTableConfig.ts'), 'utf8')

assert.equal(categoryFromModeOrModel('image_generation', 'custom-model'), 'image', '图片 mode 应识别为图像模型')
assert.equal(categoryFromModeOrModel(undefined, 'gpt-image-2'), 'image', 'gpt-image 模型名应识别为图像模型')
assert.equal(categoryFromModeOrModel(undefined, 'dall-e-3'), 'image', 'dall-e 模型名应识别为图像模型')
assert.equal(categoryFromModeOrModel('audio_speech', 'custom-model'), 'text', '已下线音频分类不应生成独立音频 Tab')
assert.equal(categoryFromModeOrModel(undefined, 'whisper-large-v3'), 'text', '已下线音频模型名不应生成独立音频 Tab')
assert.equal(categoryFromModeOrModel(undefined, 'tts-1-hd'), 'text', '已下线音频模型名不应生成独立音频 Tab')
assert.equal(categoryFromModeOrModel(undefined, 'claude-opus-4-5'), 'text', 'Claude 模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'deepseek-v4-flash'), 'text', 'DeepSeek 官方模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'deepseek-ai-v4-pro'), 'text', 'DeepSeek 上游别名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'gpt-5.5'), 'text', 'GPT 模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'o3-pro'), 'text', 'o 系列模型名应识别为文本模型')
assert.doesNotMatch(categoryRulesSource, /audio:\s*'音频'/, '模型目录不能保留音频 Tab')
assert.doesNotMatch(formatterSource, /label:\s*'音频',\s*value:\s*'audio'/, '新增自定义模型不能保留音频用途入口')

assert.equal(getModelCategory(providerModel({ model: 'gpt-image-2' })), 'image', 'getModelCategory 应继续使用拆分后的分类规则')
assert.equal(formatModelCategory(providerModel({ model: 'claude-haiku-4-5' })), '对话 / 编码', 'formatModelCategory 文案不应变化')
assert.equal(formatModelServiceTierCapabilities(providerModel()), '仅标准', '未声明额外服务等级时应明确表示仅标准档')
assert.equal(formatModelReasoningCapabilities(providerModel()), '不支持', '未声明思考能力时应明确表示不支持')
assert.equal(
  formatModelReasoningCapabilities(providerModel({ supportedReasoningEfforts: ['low', 'high'] })),
  'Low / High',
  '模型目录只声明客户端可选的思考级别'
)
assert.equal(
  formatModelReasoningCapabilities(providerModel({ supportedReasoningEfforts: ['low', 'high'], defaultReasoningEffort: 'high' })),
  'Low / High',
  '模型目录不能把上游元数据标成客户端默认思考级别'
)
assert.equal(formatTokens(1_048_576), '1M', '二进制 1M Token 容量应使用简洁单位')
assert.equal(formatTokens(65_536), '64K', '二进制 64K Token 容量应使用简洁单位')
assert.equal(formatTokens(131_072), '128K', '二进制 128K Token 容量应使用简洁单位')
assert.equal(formatTokens(128_000), '128K', '十进制 128K Token 容量应保持官方单位')
assert.equal(
  formatModelCatalogDisplayValue({ key: 'input', label: '输入', format: 'usd_per_1m_tokens', value: 1.5 }),
  '$1.5 / 1M tokens',
  'Token 价格 descriptor 必须自带明确计价单位'
)
assert.equal(
  formatModelCatalogDisplayValue({ key: 'storage', label: '存储', format: 'usd_per_1m_token_hour', value: 4.5 }),
  '$4.5 / 1M tokens·小时',
  'Gemini token-hour 价格必须保留时间维度'
)
assert.equal(
  formatModelCatalogDisplayValue({ key: 'multiplier', label: '输入倍率', format: 'multiplier', value: 2 }),
  '2x',
  '长上下文倍率必须按倍率展示，不能伪装成金额'
)
const descriptorModel = providerModel({
  catalogDisplay: [
    { key: 'standard', label: '标准计费', items: [{ key: 'input', label: '输入', format: 'usd_per_1m_tokens', value: 1.5 }] },
    { key: 'empty', label: '空 section', items: [] },
    { key: 'capacity', label: '容量', items: [{ key: 'context', label: '上下文', format: 'tokens', value: 1_048_576 }] }
  ]
})
assert.deepEqual(modelCatalogDisplaySections(descriptorModel).map((section) => section.key), ['standard', 'capacity'], '目录 presenter 必须忽略空 section')
assert.deepEqual(
  buildProviderModelColumns('text', [providerModel(), descriptorModel])
    .filter((column) => 'catalogDisplaySectionKey' in column)
    .map((column) => column.key),
  ['catalogDisplay:standard', 'catalogDisplay:capacity'],
  '桌面目录列必须取当前模型 descriptor section 的有序并集'
)
assert.deepEqual(defaultProtocolsForModelCategory('image'), ['images'], '图片模型默认协议不应变化')
assert.deepEqual(defaultProtocolsForModelCategory('text'), ['responses', 'chat_completions'], '文本模型默认协议不应变化')
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(providerFixture({
    code: 'anthropic',
    protocolCode: 'anthropic',
    protocolVersion: 'v1',
    endpointFamilies: ['messages', 'models', 'message_token_counting']
  }), 'text'),
  ['messages', 'message_token_counting'],
  'Anthropic 文本自定义模型默认协议应来自当前供应商协议档案'
)
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(providerFixture({
    code: 'deepseek',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    endpointFamilies: ['chat_completions']
  }), 'text'),
  ['chat_completions'],
  'DeepSeek 文本自定义模型默认协议应只来自 Chat Completions 档案'
)
const multiProfileDeepSeekProvider = providerFixture({
  code: 'deepseek',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  endpointFamilies: ['chat_completions']
})
multiProfileDeepSeekProvider.protocolProfiles.push({
  ...multiProfileDeepSeekProvider.protocolProfiles[0]!,
  id: 'profile_deepseek_anthropic_v1',
  name: 'DeepSeek Anthropic v1',
  protocolCode: 'anthropic',
  endpointFamilies: [{ code: 'messages', name: 'messages' }]
})
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(multiProfileDeepSeekProvider, 'text'),
  ['chat_completions'],
  'DeepSeek 空目录回退不能把备用 Anthropic Messages 档案合并进新模型默认协议'
)
const multiProfileGlmProvider = providerFixture({
  code: 'glm',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  endpointFamilies: ['chat_completions']
})
multiProfileGlmProvider.protocolProfiles.push({
  ...multiProfileGlmProvider.protocolProfiles[0]!,
  id: 'profile_glm_coding_anthropic_v1',
  name: 'GLM Coding Anthropic v1',
  protocolCode: 'anthropic',
  endpointFamilies: [{ code: 'messages', name: 'messages' }]
})
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(multiProfileGlmProvider, 'text'),
  ['chat_completions'],
  'GLM 空目录回退不能把 Coding Anthropic Messages 档案合并进新模型默认协议'
)
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(providerFixture({
    code: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    endpointFamilies: ['responses', 'chat_completions']
  }), 'text'),
  ['responses', 'chat_completions'],
  'OpenAI v1 文本自定义模型默认协议应保持 Responses / Chat'
)
const lightweightAnthropicProvider = providerFixture({
  code: 'anthropic',
  protocolCode: 'anthropic',
  protocolVersion: 'v1',
  endpointFamilies: []
})
lightweightAnthropicProvider.protocolProfiles = []
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(lightweightAnthropicProvider, 'text'),
  ['messages', 'message_token_counting'],
  '轻量供应商列表必须仅凭真实 protocolCode 推导 Anthropic 默认协议，不能回退 OpenAI'
)
const lightweightGeminiProvider = providerFixture({
  code: 'gemini',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta',
  endpointFamilies: []
})
lightweightGeminiProvider.protocolProfiles = []
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(lightweightGeminiProvider, 'text'),
  ['generate_content', 'stream_generate_content', 'count_tokens', 'embed_content', 'interactions'],
  '轻量供应商列表必须仅凭真实 protocolCode 推导 Gemini 默认协议，不能回退 OpenAI'
)
assert.match(formatterSource, /from '\.\/providerModelCategoryRules'/, 'providerModelFormatters 应从分类规则文件读取模型类别能力')
assert.doesNotMatch(formatterSource, /gpt-image|dall-e|whisper|startsWith\('gpt-'|startsWith\('claude-'/, 'providerModelFormatters 不应继续内联模型名前缀分类规则')
assert.match(categoryRulesSource, /modelNameCategoryRules/, '模型名前缀分类规则应集中在 providerModelCategoryRules')
assert.match(catalogModalSource, /isDefaultHealthCheckModel\(record\).*默认检查/s, '个人模型目录应在当前默认模型名称旁保留默认检查标签')
assert.doesNotMatch(catalogModalSource, /我的默认检查模型|column\.key === 'defaultTest'/, '个人模型目录不应重复增加顶部说明或独立默认检查列')
assert.doesNotMatch(providersViewSource, /ensureActiveProviderDetail|activeProviderDetail\.value|api\.providers\.detail/, '新增或编辑模型必须直接使用目录行与已加载目录能力，不得预取 provider 详情')
assert.match(providersViewSource, /defaultProtocolsForCurrentProviderCategory[\s\S]*providerModels\.value/, '新增模型协议默认值必须优先复用已加载目录能力')
assert.match(providersViewSource, /defaultProtocolsForProviderModelCategory\(activeProvider\.value \?\? undefined, category\)/, '空模型目录必须复用轻量供应商列表中的协议常量')
assert.match(providerTableConfigSource, /const selfProviderColumns = \[[\s\S]*默认检查模型[\s\S]*defaultHealthCheckModel/, '普通用户模型目录列表应展示个人默认检查模型列')
assert.match(providersViewSource, /<div class="mobile-list-meta-item mobile-list-meta-wide">\s*<span>默认检查模型<\/span>/, '普通用户移动端模型目录列表应展示个人默认检查模型')
assert.match(providersViewSource, /const canManageModelPrices = computed\(\(\) => canManageModelPricesForView\(isManagementView\.value, authState\.isAdmin\.value\)\)/, '新增和编辑必须使用统一的价格维护判定')
assert.match(providersViewSource, /availableCustomModelModeOptions/, '新增模型用途必须按当前供应商能力生成')
assert.match(providersViewSource, /label="服务等级"[\s\S]*:mode="customModelCapabilitySelectMode"[\s\S]*:options="customModelServiceTierOptions"/, '服务等级必须使用当前供应商的选择模式和已知选项')
assert.match(providersViewSource, /label="思考能力"[\s\S]*:mode="customModelCapabilitySelectMode"[\s\S]*:options="customModelReasoningEffortOptions"/, '思考能力必须使用当前供应商的选择模式和已知选项')
assert.match(providersViewSource, /v-if="isManagementView && !editingBuiltInModel" label="作用域"/, '内置模型价格编辑不能伪装成个人模型作用域')
assert.doesNotMatch(providersViewSource, /:disabled="editingBuiltInModel"/, '管理员编辑内置模型时状态、用途、协议、服务等级、思考和 token 上限不能被锁死')
assert.doesNotMatch(providersViewSource, /编辑模型价格/, '内置模型编辑已不是仅价格编辑')
assert.match(catalogModalSource, /column\.catalogDisplaySectionKey/, '桌面模型目录必须渲染动态 descriptor 列')
assert.match(catalogModalSource, /v-for="section in modelCatalogDisplaySections\(record\)"/, '移动端模型目录必须只循环当前模型的非空 section')
assert.doesNotMatch(catalogModalSource, /column\.key === '(?:serviceTiers|reasoningEfforts|prices|cacheWrite|imageTokenPrice|audioTokenPrice|imageUnitPrice|context)'/, '模型目录不能保留按模型类别硬编码的计费、能力和容量列')
assert.doesNotMatch(catalogModalSource, />仅标准<|>不支持<|>不适用<|>暂无价格</, '动态目录不能用无意义负面占位填充缺失 section')
assert.doesNotMatch(formatterSource, /按上下文推导|官方未单独公布|官方未公布/, '容量与计费格式化不能伪造推导值或堆叠缺失文案')
assert.match(catalogModalSource, /:row-key="modelRowKey"/, '聚合模型目录必须使用稳定复合键，不能只用可能跨供应商重复的模型名')

console.log('供应商模型 formatter 回归通过：模型类别规则已拆分，现有分类和默认协议行为保持不变')

function providerModel(overrides: Partial<ProviderModelPricing> = {}): ProviderModelPricing {
  return {
    providerCode: 'gpt',
    model: 'gpt-5.5',
    source: 'built-in',
    scope: 'built_in',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: false,
    ...overrides
  }
}

function providerFixture(input: {
  code: string
  protocolCode: string
  protocolVersion: string
  endpointFamilies: string[]
}): ProviderDefinition {
  const profileId = `profile_${input.code}_${input.protocolCode}_${input.protocolVersion}`
  return {
    id: input.code,
    code: input.code,
    name: input.code,
    enabled: true,
    defaultProtocolProfileId: profileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    baseUrl: 'https://example.com/v1',
    defaultHealthCheckModel: '',
    accountTypes: ['api_key'],
    capabilities: [],
    protocolProfiles: [
      {
        id: profileId,
        providerCode: input.code,
        name: profileId,
        enabled: true,
        protocolCode: input.protocolCode,
        protocolVersion: input.protocolVersion,
        baseUrl: 'https://example.com/v1',
        defaultHealthCheckModel: '',
        accountTypes: ['api_key'],
        capabilities: [],
        endpointFamilies: input.endpointFamilies.map((code) => ({ code, name: code }))
      }
    ]
  }
}

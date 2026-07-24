import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  categoryFromModeOrModel,
  defaultProtocolsForModelCategory,
  defaultProtocolsForProviderModelCategory,
  formatModelCategory,
  formatModelCacheCostSummary,
  formatModelCapacitySummary,
  formatModelReasoningCapabilities,
  formatModelServiceTierCapabilities,
  formatPrice,
  formatTokens,
  getModelCategory
} from '../../src/views/providers/providerModelFormatters'
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
assert.equal(categoryFromModeOrModel('audio_speech', 'custom-model'), 'audio', '音频 mode 应识别为音频模型')
assert.equal(categoryFromModeOrModel(undefined, 'whisper-large-v3'), 'audio', 'whisper 模型名应识别为音频模型')
assert.equal(categoryFromModeOrModel(undefined, 'tts-1-hd'), 'audio', 'tts 模型名应识别为音频模型')
assert.equal(categoryFromModeOrModel(undefined, 'claude-opus-4-5'), 'text', 'Claude 模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'deepseek-v4-flash'), 'text', 'DeepSeek 官方模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'deepseek-ai-v4-pro'), 'text', 'DeepSeek 上游别名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'gpt-5.5'), 'text', 'GPT 模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'o3-pro'), 'text', 'o 系列模型名应识别为文本模型')

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
assert.equal(formatPrice(undefined), '—', '缺失价格应使用紧凑占位，不重复渲染未公布文案')
assert.equal(formatTokens(1_048_576), '1M（1,048,576）', '二进制 1M Token 容量应兼顾易读单位和精确值')
assert.equal(formatTokens(65_536), '64K（65,536）', '二进制 64K Token 容量应保留精确值')
assert.equal(formatTokens(131_072), '128K（131,072）', '二进制 128K Token 容量应保留精确值')
assert.equal(formatTokens(128_000), '128K', '十进制 128K Token 容量应保持官方单位')
assert.equal(
  formatModelCapacitySummary(providerModel({ contextWindowTokens: 200_000, maxOutputTokens: 128_000 })),
  '上下文 200K · 最大输出 128K',
  '厂商未公布最大输入时只展示已有容量事实，不得按上下文推导'
)
assert.equal(
  formatModelCacheCostSummary(providerModel({ cachedInputUsdPer1M: 0.2, supportsPromptCaching: true })),
  '无独立费用',
  '自动缓存只有命中价格时不应显示缺失写入费'
)
assert.equal(
  formatModelCacheCostSummary(providerModel({ cacheStorageUsdPer1MPerHour: 4.5, supportsPromptCaching: true })),
  '存储/小时 $4.5',
  '按时长计费的缓存存储必须与一次性写入价格分开展示'
)
assert.deepEqual(defaultProtocolsForModelCategory('image'), ['images'], '图片模型默认协议不应变化')
assert.deepEqual(defaultProtocolsForModelCategory('audio'), ['audio'], '音频模型默认协议不应变化')
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
assert.match(formatterSource, /from '\.\/providerModelCategoryRules'/, 'providerModelFormatters 应从分类规则文件读取模型类别能力')
assert.doesNotMatch(formatterSource, /gpt-image|dall-e|whisper|startsWith\('gpt-'|startsWith\('claude-'/, 'providerModelFormatters 不应继续内联模型名前缀分类规则')
assert.match(categoryRulesSource, /modelNameCategoryRules/, '模型名前缀分类规则应集中在 providerModelCategoryRules')
assert.match(catalogModalSource, /isDefaultHealthCheckModel\(record\).*默认检查/s, '个人模型目录应在当前默认模型名称旁保留默认检查标签')
assert.doesNotMatch(catalogModalSource, /我的默认检查模型|column\.key === 'defaultTest'/, '个人模型目录不应重复增加顶部说明或独立默认检查列')
assert.match(providersViewSource, /force:\s*force\s*\|\|\s*!isManagementView\.value/, '普通用户每次打开模型目录时应强制重新读取个人 provider 默认值')
assert.match(providerTableConfigSource, /const selfProviderColumns = \[[\s\S]*默认检查模型[\s\S]*defaultHealthCheckModel/, '普通用户模型目录列表应展示个人默认检查模型列')
assert.match(providersViewSource, /<div class="mobile-list-meta-item mobile-list-meta-wide">\s*<span>默认检查模型<\/span>/, '普通用户移动端模型目录列表应展示个人默认检查模型')
assert.match(providersViewSource, /const canManageModelPrices = computed\(\(\) => canManageModelPricesForView\(isManagementView\.value, authState\.isAdmin\.value\)\)/, '新增和编辑必须使用统一的价格维护判定')
assert.match(providersViewSource, /availableCustomModelModeOptions/, '新增模型用途必须按当前供应商能力生成')
assert.match(providersViewSource, /label="服务等级"[\s\S]*mode="multiple"/, '服务等级必须限制为当前供应商已知选项')
assert.match(providersViewSource, /label="思考级别"[\s\S]*mode="multiple"/, '思考级别必须限制为当前供应商已知选项')
assert.match(providersViewSource, /v-if="isManagementView && !editingBuiltInModel" label="作用域"/, '内置模型价格编辑不能伪装成个人模型作用域')
assert.doesNotMatch(providersViewSource, /:disabled="editingBuiltInModel"/, '管理员编辑内置模型时状态、用途、协议、服务等级、思考和 token 上限不能被锁死')
assert.doesNotMatch(providersViewSource, /编辑模型价格/, '内置模型编辑已不是仅价格编辑')
assert.match(catalogModalSource, /serviceTierPrices/, '模型目录必须展示服务等级价格明细')
assert.doesNotMatch(catalogModalSource, /默认由上游决定|（默认）/, '模型目录思考级别只展示客户端可选能力，不展示默认语义')
assert.match(catalogModalSource, /tier-price-metrics/, '服务等级价格必须使用紧凑指标布局')
assert.match(catalogModalSource, /暂无价格/, '缺失服务等级价格应合并为简洁空态')
assert.match(catalogModalSource, /prices\.cacheWriteUsdPer1M/, '桌面档位价格必须展示缓存写入')
assert.match(catalogModalSource, /prices\.cacheWrite1hUsdPer1M/, '桌面档位价格必须展示 1h 缓存写入')
assert.match(catalogModalSource, /缓存附加费/, '桌面与移动端必须使用统一的缓存附加费语义')
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

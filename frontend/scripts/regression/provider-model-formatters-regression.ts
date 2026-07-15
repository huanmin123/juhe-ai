import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  categoryFromModeOrModel,
  defaultProtocolsForModelCategory,
  defaultProtocolsForProviderModelCategory,
  formatModelCategory,
  formatModelReasoningCapabilities,
  formatModelServiceTierCapabilities,
  formatPrice,
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
  'Low / High；默认：上游决定',
  '支持思考但没有正式默认值时必须明确由上游决定，不能看起来像漏字段'
)
assert.equal(
  formatModelReasoningCapabilities(providerModel({ supportedReasoningEfforts: ['low', 'high'], defaultReasoningEffort: 'high' })),
  'Low / High（默认）',
  '思考级别必须标明默认值'
)
assert.equal(formatPrice(undefined), '官方未公布', '缺失价格不能显示成含义不明的短横线')
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
assert.match(providersViewSource, /: await api\.providers\.options\(\)/, '普通用户每次打开模型目录时应重新读取个人 provider 默认值')
assert.match(providerTableConfigSource, /const selfProviderColumns = \[[\s\S]*默认检查模型[\s\S]*defaultHealthCheckModel/, '普通用户模型目录列表应展示个人默认检查模型列')
assert.match(providersViewSource, /<div class="mobile-list-meta-item mobile-list-meta-wide">\s*<span>默认检查模型<\/span>/, '普通用户移动端模型目录列表应展示个人默认检查模型')
assert.match(providersViewSource, /const canManageModelPrices = computed\(\(\) => canManageModelPricesForView\(isManagementView\.value, authState\.isAdmin\.value\)\)/, '价格维护权限必须同时受管理视图和管理员身份约束')
assert.match(providersViewSource, /customModelModeOptions = modelModeOptions/, '新增模型用途不能依赖已有目录记录')
assert.match(providersViewSource, /label="服务等级"[\s\S]*mode="tags"/, '服务等级必须允许录入首个供应商原生值')
assert.match(providersViewSource, /label="思考级别"[\s\S]*mode="tags"/, '思考级别必须允许录入首个供应商原生值')
assert.match(providersViewSource, /v-if="isManagementView && !editingBuiltInModel" label="作用域"/, '内置模型价格编辑不能伪装成个人模型作用域')
assert.match(catalogModalSource, /serviceTierPrices/, '模型目录必须展示服务等级价格明细')
assert.match(catalogModalSource, /缓存写入/, '移动端模型目录必须展示缓存写入价格')
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

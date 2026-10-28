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
import * as providerModelTableState from '../../src/views/providers/providerModelTableState'
import type { ProviderDefinition, ProviderModelPricing } from '../../src/types/domain'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const formatterSource = readFileSync(resolve(frontendRoot, 'src/views/providers/providerModelFormatters.ts'), 'utf8')
const categoryRulesSource = readFileSync(resolve(frontendRoot, 'src/views/providers/providerModelCategoryRules.ts'), 'utf8')
// 模型广场页与供应商目录退役设计 §5：卡片/抽屉形态锚点从 ProviderModelCatalogModal.vue（已删除）
// 换为 ModelPlazaView.vue；表单接线锚点从 ProvidersView.vue 换为 CustomModelFormModal.vue。
const plazaViewSource = readFileSync(resolve(frontendRoot, 'src/views/providers/ModelPlazaView.vue'), 'utf8')
const customFormModalSource = readFileSync(resolve(frontendRoot, 'src/views/providers/CustomModelFormModal.vue'), 'utf8')

assert.equal(categoryFromModeOrModel('image_generation', 'custom-model'), 'image', '图片 mode 应识别为图像模型')
assert.equal(categoryFromModeOrModel(undefined, 'gpt-image-2'), 'image', 'gpt-image 模型名应识别为图像模型')
assert.equal(categoryFromModeOrModel(undefined, 'dall-e-3'), 'image', 'dall-e 模型名应识别为图像模型')
assert.equal(categoryFromModeOrModel('audio_speech', 'custom-model'), 'audio', '音频 mode 应识别为音频模型')
assert.equal(categoryFromModeOrModel(undefined, 'whisper-large-v3'), 'text', '音频模型名不单独推断分类，需依赖目录 mode')
assert.equal(categoryFromModeOrModel(undefined, 'tts-1-hd'), 'text', '音频模型名不单独推断分类，需依赖目录 mode')
assert.equal(categoryFromModeOrModel(undefined, 'claude-opus-4-5'), 'text', 'Claude 模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'deepseek-v4-flash'), 'text', 'DeepSeek 官方模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'deepseek-ai-v4-pro'), 'text', 'DeepSeek 上游别名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'gpt-5.5'), 'text', 'GPT 模型名应识别为文本模型')
assert.equal(categoryFromModeOrModel(undefined, 'o3-pro'), 'text', 'o 系列模型名应识别为文本模型')
assert.match(categoryRulesSource, /audio:\s*'音频'/, '模型目录必须保留音频 Tab')
assert.match(formatterSource, /label:\s*'音频',\s*value:\s*'audio'/, '新增自定义模型必须保留音频用途入口')

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
assert.equal(typeof providerModelTableState.buildModelCategoryTabs, 'function', '用途分类 Tabs 构造必须保留')
assert.equal(typeof providerModelTableState.filterProviderModelsByKeyword, 'function', '关键词过滤必须保留')
assert.equal(typeof providerModelTableState.buildConfigurationTemplateOptions, 'function', '配置模板选项构造必须保留')
assert.equal('baseModelColumns' in providerModelTableState, false, '表格基列已随模型目录卡片化移除')
assert.equal('buildProviderModelColumns' in providerModelTableState, false, '动态目录列构造已随模型目录卡片化移除')
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
const wireLightweightAnthropicProvider = {
  ...lightweightAnthropicProvider,
  protocolProfiles: undefined as never
}
assert.deepEqual(
  defaultProtocolsForProviderModelCategory(wireLightweightAnthropicProvider, 'text'),
  ['messages', 'message_token_counting'],
  '轻量供应商 wire DTO 省略 protocolProfiles 时仍必须使用 protocolCode 常量，不能触发无交互详情请求'
)
assert.match(formatterSource, /from '\.\/providerModelCategoryRules'/, 'providerModelFormatters 应从分类规则文件读取模型类别能力')
assert.doesNotMatch(formatterSource, /gpt-image|dall-e|whisper|startsWith\('gpt-'|startsWith\('claude-'/, 'providerModelFormatters 不应继续内联模型名前缀分类规则')
assert.match(categoryRulesSource, /modelNameCategoryRules/, '模型名前缀分类规则应集中在 providerModelCategoryRules')
assert.doesNotMatch(plazaViewSource, />默认检查<|isDefaultHealthCheckModel\(/, '内置来源与默认检查属内部机制，卡面与抽屉不得渲染')
assert.match(plazaViewSource, /v-if="item\.scope !== 'built_in'"/, '范围徽标仅自定义模型（全局/个人）显示，内置不展示')
assert.match(plazaViewSource, /v-if="record\.scope !== 'built_in'"/, '抽屉 Hero 的范围徽标同样仅自定义模型显示')
assert.doesNotMatch(plazaViewSource, /我的默认检查模型|column\.key === 'defaultTest'/, '个人模型目录不应重复增加顶部说明或独立默认检查列')
assert.match(customFormModalSource, /defaultProtocolsForCurrentProviderCategory[\s\S]*props\.models/, '新增模型协议默认值必须优先复用已加载目录能力')
assert.match(customFormModalSource, /defaultProtocolsForProviderModelCategory\(providerDefinition, category\)/, '空模型目录必须复用轻量供应商列表中的协议常量')
assert.match(customFormModalSource, /const canManageModelPrices = computed\(\(\) => canManageModelPricesForView\(props\.isManagementView, authState\.isAdmin\.value\)\)/, '新增和编辑必须使用统一的价格维护判定')
assert.match(customFormModalSource, /availableCustomModelModeOptions/, '新增模型用途必须按当前供应商能力生成')
assert.match(customFormModalSource, /label="服务等级"[\s\S]*:mode="customModelCapabilitySelectMode"[\s\S]*:options="customModelServiceTierOptions"/, '服务等级必须使用当前供应商的选择模式和已知选项')
assert.match(customFormModalSource, /label="思考能力"[\s\S]*:mode="customModelCapabilitySelectMode"[\s\S]*:options="customModelReasoningEffortOptions"/, '思考能力必须使用当前供应商的选择模式和已知选项')
assert.match(customFormModalSource, /v-if="isManagementView && !editingBuiltInModel" label="作用域"/, '内置模型价格编辑不能伪装成个人模型作用域')
assert.doesNotMatch(customFormModalSource, /:disabled="editingBuiltInModel"/, '管理员编辑内置模型时状态、用途、协议、服务等级、思考和 token 上限不能被锁死')
assert.doesNotMatch(customFormModalSource, /编辑模型价格/, '内置模型编辑已不是仅价格编辑')
assert.match(plazaViewSource, /repeat\(auto-fill, minmax\(280px, 1fr\)\)/, '模型目录必须使用 auto-fill 卡片网格承载模型列表')
assert.match(plazaViewSource, /modelPriceSlots\(/, '模型目录卡片必须按候选序列接线价格槽位')
// 抽屉「计费价格」一张表 + 「基础信息」网格（§6.2/§6.3，对齐 portal 形态）：
// 模板字面调用 modelPriceTable(record) / modelInfoItems(record)（不得包 computed 别名，否则断言失效）。
assert.match(plazaViewSource, /modelPriceTable\(record\)/, '详情抽屉「计费价格」必须按一张价格表渲染当前模型')
assert.match(plazaViewSource, /modelInfoItems\(record\)/, '非价格分组必须经「基础信息」网格渲染')
assert.match(plazaViewSource, /<table class="model-drawer-table">/, '计费价格必须渲染为表格')
assert.match(plazaViewSource, /table-layout: fixed/, '价格表必须 fixed 布局锚定列宽')
assert.match(plazaViewSource, /width: 38%/, '价格表首列必须锚定 38% 宽，禁止被数值列挤压塌陷成竖排单字')
// 列语义对齐官方「正常价 / 高峰价」：单列模型「正常价」，DeepSeek 两列「正常价 | 高峰价」。
// 列头文案的单一来源在 modelPriceTable（模板只渲染 column.label），锚点打 formatters 源码。
assert.match(formatterSource, /label: '正常价'/, '价格表必须以「正常价」为基准列头')
assert.match(formatterSource, /label: '高峰价'/, '存在高峰档（DeepSeek）时价格表必须追加「高峰价」列')
assert.match(plazaViewSource, /高峰时段为工作日 09:00–12:00、14:00–18:00（北京时间）；时段计费未启用，当前所有请求按高峰价结算。/, '高峰列下方必须附官方时段口径与计费说明')
assert.match(plazaViewSource, /基础信息/, '非价格分组必须落在「基础信息」区块')
// §6.3 追加裁决：接入事实并入基础信息网格（接口协议整行宽项；工具矩阵属 AI 问答搜索绑定内部机制，不展示），无独立「接入信息」区块，「模型模式」不展示。
assert.match(plazaViewSource, /model-drawer-info-item--wide/, '接口协议必须以整行宽项并入基础信息网格')
assert.doesNotMatch(plazaViewSource, />工具矩阵<|model-drawer-tools|modelToolsByProtocol\(/, '工具矩阵属内部机制数据，抽屉不得渲染')
assert.match(plazaViewSource, /hasModelAccessInfo\(record\)/, '基础信息区块必须带接入事实存在性门控')
assert.doesNotMatch(plazaViewSource, /接入信息|模型模式/, '抽屉不得再出现独立「接入信息」区块或「模型模式」行')
// §6.4：banned 分组在任何渲染路径都不得出现（formatters 函数层已过滤，模板/注释不得复活字面）。
assert.doesNotMatch(plazaViewSource, /currency_conversion|source_disclosure/, 'banned 分组不得出现在广场页任何渲染路径')
assert.doesNotMatch(plazaViewSource, /column\.key === '(?:serviceTiers|reasoningEfforts|prices|cacheWrite|imageTokenPrice|audioTokenPrice|imageUnitPrice|context)'/, '模型目录不能保留按模型类别硬编码的计费、能力和容量列')
assert.doesNotMatch(plazaViewSource, />仅标准<|>不支持<|>不适用<|>暂无价格</, '动态目录不能用无意义负面占位填充缺失 section')
assert.doesNotMatch(formatterSource, /按上下文推导|官方未单独公布|官方未公布/, '容量与计费格式化不能伪造推导值或堆叠缺失文案')
assert.match(plazaViewSource, /<a-drawer/, '模型目录详情必须迁移到右侧抽屉承载完整分组')
assert.match(plazaViewSource, /a-pagination/, '模型目录必须保留显式分页')
assert.match(plazaViewSource, /:key="modelRowKey\(item\)"/, '卡片列表必须使用稳定复合键，不能只用可能跨供应商重复的模型名')

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

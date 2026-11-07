import { readFileSync, readdirSync } from 'node:fs'

import {
  accountModelMappingEndpointFamilyProtocol,
  accountModelMappingSourceModelOptions,
  accountModelMappingUpstreamModelOptions,
  filterAccountModelMappingOptionsByEndpointFamily,
  type AccountModelMappingModelOption
} from '../../views/accounts/accountModelMappingModelOptions'
import {
  accountModelMappingEndpointFamilyText,
  accountModelMappingProtocolOptions,
  accountModelMappingProtocolRules,
  accountModelMappingProtocolValidationMessage,
  accountModelMappingTargetForSource,
  defaultAccountModelMappingProtocol,
  hasAccountModelMappingUpstreamEndpointFamilyCapability,
  isAccountModelMappingProtocolAllowed,
  isAccountModelMappingProtocolOptionAllowed,
  type AccountModelMappingUpstreamEndpointFamily
} from '../../views/accounts/accountModelMappingProtocolMatrix'
import { accountEndpointModeOptions } from '../../views/accounts/accountEndpointModes'

const accountEditModalSource = readFileSync(new URL('../../views/accounts/AccountEditModal.vue', import.meta.url), 'utf8')
const accountsViewSource = readFileSync(new URL('../../views/accounts/AccountsView.vue', import.meta.url), 'utf8')
const accountApiKeySectionSource = readFileSync(new URL('../../views/accounts/AccountApiKeySection.vue', import.meta.url), 'utf8')
const accountEditFormSource = readFileSync(new URL('../../views/accounts/useAccountEditForm.ts', import.meta.url), 'utf8')
const accountSavePayloadSource = readFileSync(new URL('../../views/accounts/accountSavePayload.ts', import.meta.url), 'utf8')
const accountStrategySectionSource = readFileSync(new URL('../../views/accounts/AccountStrategySection.vue', import.meta.url), 'utf8')
// 在线帮助自 9268cb4df 起重构为 Markdown 文档站（public/help/docs/**，render-help-docs 渲染管线的唯一人工维护层）；
// 账户配置语义（模型映射 / 接口能力）迁入 admin 篇，消费语义在 user 篇，断言以各篇全部 md 合集为源。
function readHelpDocsSource(section: 'user' | 'admin'): string {
  const dir = new URL(`../../../public/help/docs/${section}/`, import.meta.url)
  return readdirSync(dir)
    .filter((file) => file.endsWith('.md'))
    .sort()
    .map((file) => readFileSync(new URL(file, dir), 'utf8'))
    .join('\n')
}
const userHelpSource = readHelpDocsSource('user')
const adminHelpSource = readHelpDocsSource('admin')
const publicHelpSource = `${userHelpSource}\n${adminHelpSource}`

// 跨供应商客户端模型候选池：池本身是全供应商全量目录，按映射行协议过滤后供下拉。
const options: AccountModelMappingModelOption[] = [
  { label: 'gpt-chat-only', value: 'gpt-chat-only', supportedApiProtocols: ['chat_completions'] },
  { label: 'gpt-responses-only', value: 'gpt-responses-only', supportedApiProtocols: ['responses'] },
  { label: 'gpt-dual', value: 'gpt-dual', supportedApiProtocols: ['chat_completions', 'responses'] },
  { label: 'claude-messages', value: 'claude-messages', supportedApiProtocols: ['messages', 'message_token_counting'] },
  { label: 'gemini-native', value: 'gemini-native', supportedApiProtocols: ['generate_content', 'stream_generate_content'] },
  { label: 'sora-video', value: 'sora-video', supportedApiProtocols: ['video'] },
  { label: 'gpt-tts', value: 'gpt-tts', supportedApiProtocols: ['audio_speech'] },
  { label: 'unknown-legacy', value: 'unknown-legacy' }
]

// 客户端模型候选（跨协议退役契约）：source 可跨供应商，但必须支持所选协议；
// 未声明协议的目录行（含空协议数组）视为不支持，不进入候选。
assertDeepEqual(
  values(accountModelMappingSourceModelOptions({ sourceEndpointFamily: 'responses', options })),
  ['gpt-responses-only', 'gpt-dual'],
  '客户端模型候选必须按所选协议过滤跨供应商目录'
)
assertDeepEqual(
  values(accountModelMappingSourceModelOptions({ sourceEndpointFamily: 'chat_completions', options })),
  ['gpt-chat-only', 'gpt-dual'],
  'Chat 协议的客户端模型候选只能包含声明支持 Chat 的模型'
)
assertDeepEqual(
  values(accountModelMappingSourceModelOptions({ sourceEndpointFamily: 'video_generation', options })),
  ['sora-video'],
  '媒体族客户端模型候选按目录 video 协议 token 过滤（跨供应商 source）'
)
assertDeepEqual(
  values(accountModelMappingUpstreamModelOptions(options, 'responses')),
  ['gpt-responses-only', 'gpt-dual'],
  '上游模型必须按上游协议过滤'
)

assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'chat_completions')),
  ['gpt-chat-only', 'gpt-dual'],
  'Chat Completions 只能展示声明支持 Chat 的模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'responses')),
  ['gpt-responses-only', 'gpt-dual'],
  'Responses 只能展示声明支持 Responses 的模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'messages')),
  ['claude-messages'],
  'Messages 只能展示 Anthropic Messages 模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'generate_content')),
  ['gemini-native'],
  'Gemini GenerateContent 只能展示 Gemini native 模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'stream_generate_content')),
  ['gemini-native'],
  'Gemini StreamGenerateContent 只能展示支持流式 Gemini native 的模型'
)
assertEqual(accountModelMappingEndpointFamilyProtocol('chat_completions'), 'chat_completions', 'Chat 协议映射错误')
assertEqual(accountModelMappingEndpointFamilyProtocol('responses'), 'responses', 'Responses 协议映射错误')
assertEqual(accountModelMappingEndpointFamilyProtocol('messages'), 'messages', 'Messages 协议映射错误')
// M4b 媒体族端点族码与目录协议 token 不同名，必须经换算后过滤。
assertEqual(accountModelMappingEndpointFamilyProtocol('video_generation'), 'video', '视频生成族协议 token 换算错误')
assertEqual(accountModelMappingEndpointFamilyProtocol('tts'), 'audio_speech', '语音合成族协议 token 换算错误')
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'video_generation')),
  ['sora-video'],
  '视频生成只能展示声明 video 协议 token 的媒体模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'tts')),
  ['gpt-tts'],
  '语音合成只能展示声明 audio_speech 协议 token 的媒体模型'
)
assertIncludes(accountEditModalSource, 'for (const item of props.form.supportedModels)', '账号模型别名右侧下拉只能从账户支持模型构建')
assertNotIncludes(accountEditModalSource, 'buildAccountModelMappingUpstreamOptions', '账号模型别名右侧下拉不应合并整个供应商模型目录')
assertIncludes(accountApiKeySectionSource, 'mode="multiple"', '支持模型必须只能从模型目录多选')
assertNotIncludes(accountApiKeySectionSource, 'mode="tags"', '支持模型不得开放任意模型 ID 输入')
assertMatch(accountEditFormSource, /function selectProvider[\s\S]*?resetForm\(providerCode, ''\)/, '切换供应商必须重置为该供应商的默认表单')
assertNotIncludes(
  accountEditFormSource.slice(
    accountEditFormSource.indexOf('function selectProvider'),
    accountEditFormSource.indexOf('function selectAccountType')
  ),
  'loadCurrentProviderModelOptions()',
  '切换供应商的同步重置逻辑不得内联加载模型目录（目录加载统一由 useAccountEditForm 的供应商 watch 触发）'
)
assertIncludes(accountEditFormSource, 'async function loadCurrentProviderModelOptions', '账户表单必须保留模型控件交互时的按需加载入口')
assertMatch(
  accountsViewSource,
  /function handleAccountModelOptionsOpen\(open: boolean\)[\s\S]*?if \(!open\) \{[\s\S]*?clearAccountModelOptionsSearchTimer\(\)/,
  '关闭模型下拉必须清理尚未触发的搜索请求'
)
assertMatch(
  accountsViewSource,
  /watch\(modalOpen, \(open\) => \{[\s\S]*?clearAccountModelOptionsSearchTimer\(\)[\s\S]*?cancelAccountModelCatalogSync\(\)/,
  '关闭账户弹窗必须同时失效搜索定时器和模型目录同步'
)
assertIncludes(accountsViewSource, '@refresh-models="refreshAccountModelCatalog"', '账户表单必须保留用户点击的上游模型同步入口')
assertNotIncludes(accountsViewSource, 'shouldAutoRefreshAccountModelCatalog', '新增 API Key 草稿不得自动同步上游模型目录')
assertNotIncludes(accountsViewSource, 'scheduleAutomaticAccountModelCatalogSync', '新增 API Key 草稿不得建立模型目录自动同步定时器')
assertNotIncludes(accountsViewSource, 'automaticModelCatalogAttemptedRequestKeys', '手动同步不得维护自动同步尝试状态')
assertMatch(
  accountsViewSource,
  /async function refreshAccountModelCatalog\(\): Promise<void> \{[\s\S]*?const requestKey = modelCatalogDiscoveryRequestKey\(payload\)[\s\S]*?requestKey !== currentModelCatalogDiscoveryRequestKey\(\)/,
  '手动目录同步必须在响应返回时确认草稿没有变更'
)
assertIncludes(accountsViewSource, "message.error(extractApiErrorMessage(error, '同步上游模型失败'))", '手动同步失败必须保留可观察错误提示')
assertIncludes(
  accountEditModalSource,
  "credentialItem('supported_endpoint_modes', '上游接口能力'",
  '账户详情必须把 supported_endpoint_modes 展示为上游接口能力'
)
assertIncludes(accountStrategySectionSource, 'label="上游接口能力"', '账户表单必须使用上游接口能力标签')
assertIncludes(accountStrategySectionSource, 'placeholder="客户端模型"', '账户表单左侧必须明确使用客户端模型文案')
assertIncludes(accountStrategySectionSource, 'placeholder="上游模型"', '账户表单右侧必须明确使用上游模型文案')
assertIncludes(accountStrategySectionSource, '真实上游支持的接口形态', '账户表单提示必须解释真实上游能力语义')
assertNotIncludes(accountStrategySectionSource, '接口能力限制', '账户表单不得继续展示旧接口能力限制文案')
assertNotIncludes(accountStrategySectionSource, '可承接的接口形态', '账户表单不得把上游能力描述成客户端可承接请求')
assertNotIncludes(publicHelpSource, '接口能力限制', '帮助不得继续展示接口能力限制旧文案')
assertNotIncludes(publicHelpSource, '账号可承接的请求形态', '帮助不得展示派生的可承接请求形态')
assertIncludes(adminHelpSource, '接口能力 / 模型别名', '帮助必须展示上游接口能力字段（模型别名配置说明）')

// ---- 单协议下拉（2026-11-07 跨协议模型映射退役，批次 E 前端契约） ----

// 单协议选项 = 协议等价 pair：7 个选项，value 即 source 端点族，target 由选项表派生。
assertEqual(accountModelMappingProtocolOptions.length, 7, '单协议下拉必须恰好提供 7 个协议等价选项')
for (const option of accountModelMappingProtocolOptions) {
  assertEqual(option.value, option.source, `协议选项 ${option.value} 的下拉值必须是 source 端点族`)
  assertEqual(
    accountModelMappingTargetForSource(option.source),
    option.upstream,
    `协议选项 ${option.value} 的 target 派生必须与选项表一致`
  )
}
assertEqual(accountModelMappingTargetForSource('stream_generate_content'), 'generate_content', 'Gemini StreamGenerateContent 选项的 target 必须归一为 generate_content')
assertEqual(accountModelMappingEndpointFamilyText('stream_generate_content'), 'Gemini StreamGenerateContent', 'Gemini 流式族文案')

const openAIProfile = { protocolCode: 'openai', protocolVersion: 'v1' }
const anthropicProfile = { protocolCode: 'anthropic', protocolVersion: 'v1' }
const geminiNativeProfile = {
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
}
const geminiOpenAIChatProfile = {
  providerProtocolProfileId: 'profile_gemini_openai_chat_v1beta',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}

// 协议选项按 profile + 账户 endpoint modes 过滤；默认协议按账户支持模式选择。
assertDefaultMappingProtocol(
  { providerProfile: openAIProfile, supportedEndpointModes: ['responses_json'] },
  'responses',
  'responses',
  'Responses-only OpenAI 新增映射'
)
assertDefaultMappingProtocol(
  { providerProfile: anthropicProfile, supportedEndpointModes: ['messages_json'] },
  'messages',
  'messages',
  'Anthropic 新增映射'
)
assertDefaultMappingProtocol(
  { providerProfile: geminiNativeProfile, supportedEndpointModes: ['generate_content_sse'] },
  'generate_content',
  'generate_content',
  'Gemini native 新增映射（GenerateContent 优先于 StreamGenerateContent）'
)

// Chat-only 账户：协议下拉只有 Chat 选项，默认协议是 Chat（不再默认 responses → chat bridge）。
const chatOnlyOptions = accountModelMappingProtocolOptions.filter((option) => (
  isAccountModelMappingProtocolOptionAllowed(option, { providerProfile: openAIProfile, supportedEndpointModes: ['chat_json', 'chat_sse'] })
))
assertDeepEqual(
  chatOnlyOptions.map((option) => option.value),
  ['chat_completions'],
  'Chat-only 账户的协议下拉必须只有 Chat Completions'
)
assertDefaultMappingProtocol(
  { providerProfile: openAIProfile, supportedEndpointModes: ['chat_json', 'chat_sse'] },
  'chat_completions',
  'chat_completions',
  'Chat-only 账户新增映射'
)
// 媒体族选项按账户媒体端点模式放行。
assertDeepEqual(
  accountModelMappingProtocolOptions.filter((option) => (
    isAccountModelMappingProtocolOptionAllowed(option, { providerProfile: openAIProfile, supportedEndpointModes: ['video_create', 'audio_speech'] })
  )).map((option) => option.value),
  ['video_generation', 'tts'],
  '仅启用媒体端点模式的账户只出现媒体族协议选项'
)

// Gemini OpenAI Chat 档案：bridge 退役后只允许 Chat 同协议映射。
assertDeepEqual(
  accountModelMappingProtocolOptions.filter((option) => (
    isAccountModelMappingProtocolOptionAllowed(option, { providerProfile: geminiOpenAIChatProfile, supportedEndpointModes: ['chat_json'] })
  )).map((option) => option.value),
  ['chat_completions'],
  'Gemini OpenAI Chat 档案的协议下拉必须只有 Chat Completions'
)
assertMatch(
  accountModelMappingProtocolValidationMessage({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true,
    context: { providerProfile: geminiOpenAIChatProfile, supportedEndpointModes: ['chat_json', 'chat_sse'] }
  }) ?? '',
  /Gemini OpenAI Chat.*Chat Completions 同协议映射/,
  'Gemini OpenAI Chat 档案不得再提供 Responses 到 Chat bridge（文案与结构门同步退役）'
)

// 跨协议方向整体退役：矩阵外组合一律按协议一致口径拒绝（含 responses -> chat）。
assertMatch(
  accountModelMappingProtocolValidationMessage({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true,
    context: { providerProfile: openAIProfile, supportedEndpointModes: ['chat_json', 'chat_sse'] }
  }) ?? '',
  /模型映射客户端协议与上游协议必须一致/,
  '退役的 Responses -> Chat 跨协议映射必须被结构门拒绝'
)
assertEqual(
  isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'responses',
    enabled: true,
    context: { providerProfile: openAIProfile, supportedEndpointModes: ['responses_json'] }
  }),
  true,
  'Responses 同族改名映射在启用 Responses 上游能力时可用'
)
assertEqual(
  isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'stream_generate_content',
    upstreamEndpointFamily: 'generate_content',
    enabled: true,
    context: { providerProfile: geminiNativeProfile, supportedEndpointModes: ['generate_content_sse'] }
  }),
  true,
  'Gemini 流式归一映射在启用 GenerateContent SSE 上游能力时可用'
)
assertMatch(
  accountModelMappingProtocolValidationMessage({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'messages',
    enabled: true,
    context: { providerProfile: anthropicProfile, supportedEndpointModes: ['message_token_counting'] }
  }) ?? '',
  /Messages.*上游接口能力/,
  '前端 Messages 目标族不能把 token-counting 当作请求能力'
)
// M4b 媒体映射（普通 OpenAI 档案同族改写）。
assertEqual(
  isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'tts',
    upstreamEndpointFamily: 'tts',
    enabled: true,
    context: { providerProfile: openAIProfile, supportedEndpointModes: ['audio_speech'] }
  }),
  true,
  'OpenAI 档案媒体同族映射在启用对应媒体端点模式时必须可用'
)
assertEqual(
  accountModelMappingProtocolValidationMessage({
    sourceEndpointFamily: 'video_generation',
    upstreamEndpointFamily: 'video_generation',
    enabled: true,
    context: { providerProfile: anthropicProfile, supportedEndpointModes: ['messages_json'] }
  }),
  '当前供应商协议不支持媒体模型映射账号别名',
  '非 OpenAI 档案不得声明媒体映射族'
)

// 单账户策略区源码契约：一个协议下拉派生 source/target；协议重置 watcher 删除。
assertIncludes(accountSavePayloadSource, 'enabled: item.enabled', '前端保存校验必须把映射启停状态传给统一协议矩阵')
assertIncludes(
  accountStrategySectionSource,
  'v-model:value="mapping.sourceEndpointFamily"',
  '映射行必须只保留一个协议下拉（绑定 source 端点族）'
)
assertIncludes(
  accountStrategySectionSource,
  '@change="handleMappingProtocolChange(mapping)"',
  '修改协议必须触发 source/target 同步写入'
)
assertIncludes(
  accountStrategySectionSource,
  'mapping.upstreamEndpointFamily = accountModelMappingTargetForSource(mapping.sourceEndpointFamily)',
  '修改协议时必须按选项表派生并写入上游端点族'
)
assertNotIncludes(accountStrategySectionSource, 'upstreamEndpointFamilyOptions', '上游协议下拉必须删除，不得保留第二个协议控件')
assertNotIncludes(accountStrategySectionSource, 'shouldResetAccountModelMappingUpstreamEndpointFamily', '协议重置 watcher 必须删除（单下拉不再需要结构回写）')
assertNotIncludes(accountStrategySectionSource, 'isAccountModelMappingSourceEndpointFamilyAllowed', '来源协议双下拉时代的结构过滤入口必须删除')
assertIncludes(accountStrategySectionSource, ':options="protocolOptions"', '协议下拉必须使用协议等价选项表')
assertIncludes(accountStrategySectionSource, 'isAccountModelMappingProtocolOptionAllowed(option, modelMappingProtocolContext())', '协议选项必须按协议档案与账户 endpoint modes 过滤')
assertIncludes(accountStrategySectionSource, 'defaultAccountModelMappingProtocol(modelMappingProtocolContext())', '新增映射必须使用共享默认协议')
assertNotIncludes(accountStrategySectionSource, "sourceEndpointFamily: OPENAI_CHAT_COMPLETIONS_FAMILY", '主编辑器新增映射不得硬编码 Chat 来源族')
assertIncludes(
  accountStrategySectionSource,
  'accountModelMappingSourceModelOptions({',
  '客户端模型候选必须经跨供应商池的协议过滤入口'
)
assertIncludes(
  accountStrategySectionSource,
  'options: props.mappingSourceModelOptions',
  '客户端模型候选必须来自跨供应商全量目录'
)
assertIncludes(
  accountStrategySectionSource,
  'accountModelMappingUpstreamModelOptions(props.mappingUpstreamModelOptions, upstreamEndpointFamily)',
  '上游模型候选仍必须从账户支持模型按目标协议过滤'
)
assertNotIncludes(accountStrategySectionSource, "mapping.sourceModel = ''", '模型目录异步刷新不得静默清空来源模型')
assertNotIncludes(accountStrategySectionSource, "mapping.upstreamModel = ''", '模型目录异步刷新不得静默清空上游模型')
assertNotIncludes(accountStrategySectionSource, 'sourceModelOptionsFingerprint', '协议结构 watcher 不应订阅异步模型目录指纹')
assertEqual(
  accountStrategySectionSource.indexOf('label="上游接口能力"') < accountStrategySectionSource.indexOf('label="账号模型别名"'),
  true,
  '上游接口能力必须展示在账号模型别名之前'
)

// useAccountEditForm 契约：跨供应商池（不传 providerCode/protocol）+ 主动加载 + 就绪标志。
assertIncludes(
  accountEditFormSource,
  'mappingSourceModelOptionsReady',
  '映射 source 候选池必须维护就绪标志，供保存校验阻止未就绪提交'
)
assertNotIncludes(accountEditFormSource, "protocol: 'openai'", '映射 source 池不得按 openai 协议切片（跨供应商池不传 protocol）')
assertNotIncludes(accountEditFormSource, "protocol: 'anthropic'", '映射 source 池不得按 anthropic 协议切片')
assertNotIncludes(accountEditFormSource, "protocol: 'gemini'", '映射 source 池不得按 gemini 协议切片')
assertNotIncludes(accountEditFormSource, 'mappingAnthropicSourceModelOptions', '按协议拆分的三个 source 池实例必须删除')
assertIncludes(
  accountEditFormSource,
  'void loadMappingSourceModelOptions()',
  '打开弹窗/装载编辑详情时必须主动加载映射 source 候选池'
)
assertIncludes(
  accountsViewSource,
  'function handleMappingModelOptionsOpen(open: boolean)',
  '映射 source 下拉展开事件不得再携带协议参数（单池跨供应商）'
)
// 只读展示单协议；历史行仍可显示两族。
assertIncludes(accountEditModalSource, 'readonlyMappingProtocolText', '授权只读展示必须使用单协议投影')
assertNotIncludes(accountEditModalSource, 'function endpointFamilyText', '账户编辑弹窗内的重复端点族文案函数必须删除（统一走矩阵文案）')

const accountBatchModalSource = readFileSync(new URL('../../views/accounts/AccountBatchEditModal.vue', import.meta.url), 'utf8')
assertIncludes(accountBatchModalSource, 'placeholder="映射协议"', '批量编辑必须使用单协议下拉')
assertNotIncludes(accountBatchModalSource, 'placeholder="上游协议"', '批量编辑不得保留上游协议下拉')
assertNotIncludes(accountBatchModalSource, 'disabled: !isAccountModelMappingProtocolAllowed', '批量编辑协议选项不得保留 disabled 写法（批量没有协议 watcher）')
assertIncludes(accountBatchModalSource, 'mappingSourcePoolReady', '批量编辑必须维护跨供应商 source 池就绪标志')

const accountSavePayloadTestSource = readFileSync(new URL('../../views/accounts/accountSavePayload.test.ts', import.meta.url), 'utf8')
assertIncludes(accountSavePayloadTestSource, 'ACCOUNT_MODEL_MAPPING_SOURCE_POOL_UNREADY_MESSAGE', '保存校验必须覆盖池未就绪阻止保存用例')
assertIncludes(accountSavePayloadTestSource, '不支持 Chat Completions 协议', '保存校验必须覆盖跨供应商 source 协议不符被拒用例')

assertIncludes(adminHelpSource, '只声明真实上游能处理的协议', '帮助必须解释上游接口能力边界')
assertIncludes(adminHelpSource, '别名两端都受协议档案和支持模型限制', '帮助必须解释模型映射两端受支持模型与上游能力约束')

// T103 / BUG-0342 根因治理：账号模型别名 family pair 合法集合的四方一致性
// golden 断言。唯一事实源 golden 是
// backend-go/projects/gateway/internal/accounts/testdata/entry_mapping_pairs.json，
// Go 侧断言在同目录 entry_mapping_pairs_golden_test.go（写侧矩阵全等 + 运行时
// admissible 一致）；本段守护前端 protocolRules 清单与 golden 的 pair 集合
// 全等。targetModes 是 pair 级可执行形态（§5.3），前端下拉只到 family 级能力门，
// 因此 mode 侧断言的可验证子集是：golden 的每个形态都在前端端点模式词表内，
// 且前端该目标族能力门认可的形态与 golden 交集非空（family 级与 pair 级的粒度
// 差不是矩阵漂移；mode 级联动校验由后端 §5.3 保存校验承担）。
type EntryMappingPairsGoldenFile = {
  pairs: Array<{ pair: string, designDocRow: number, targetModes: string[], frontendRuleSets: string[] }>
}
const entryMappingPairsGolden = JSON.parse(
  readFileSync(
    new URL('../../../../backend-go/projects/gateway/internal/accounts/testdata/entry_mapping_pairs.json', import.meta.url),
    'utf8'
  )
) as EntryMappingPairsGoldenFile

const goldenPairModes = new Map<string, string[]>()
for (const item of entryMappingPairsGolden.pairs) {
  if (goldenPairModes.has(item.pair)) {
    throw new Error(`golden pair 重复：${item.pair}`)
  }
  goldenPairModes.set(item.pair, item.targetModes)
}

const ordinaryProtocolPairs = new Set(accountModelMappingProtocolRules.map((rule) => `${rule.source} -> ${rule.upstream}`))
assertSetEquals(
  goldenPairModes,
  ordinaryProtocolPairs,
  '前端协议矩阵与 golden 的 pair 集合'
)

// frontendRuleSets 标注锁定清单成员关系：任何一条 pair 单独增删（而不是
// 清单与 golden 一起演化）都转红。
for (const item of entryMappingPairsGolden.pairs) {
  const expected = ordinaryProtocolPairs.has(item.pair) ? ['ordinary'] : []
  assertDeepEqual(
    [...item.frontendRuleSets].sort(),
    expected,
    `golden pair ${item.pair}（设计 §5.2 #${item.designDocRow}）的 frontendRuleSets 标注与前端清单实际成员关系`
  )
}

const knownEndpointModes = new Set(accountEndpointModeOptions.map((option) => option.value))
for (const [pair, modes] of goldenPairModes) {
  const upstream = pair.split(' -> ')[1] as AccountModelMappingUpstreamEndpointFamily
  const unknownModes = modes.filter((mode) => !knownEndpointModes.has(mode))
  if (unknownModes.length > 0) {
    throw new Error(`golden pair ${pair} 的 targetModes 含前端端点模式词表之外的形态：${unknownModes.join('、')}`)
  }
  const frontendFamilyModes = [...knownEndpointModes].filter((mode) => (
    hasAccountModelMappingUpstreamEndpointFamilyCapability(upstream, [mode])
  ))
  const sharedModes = modes.filter((mode) => frontendFamilyModes.includes(mode))
  if (sharedModes.length === 0) {
    throw new Error(
      `前端 ${upstream} 目标族能力门认可的形态（${frontendFamilyModes.join('、') || '无'}）`
      + `与 golden pair ${pair} 的允许形态（${modes.join('、')}）交集为空`
    )
  }
}

console.log('账号模型别名协议模型选项回归通过')

function values(options: AccountModelMappingModelOption[]): string[] {
  return options.map((option) => option.value)
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (!Object.is(actual, expected)) {
    throw new Error(`${message}，实际 ${String(actual)}，预期 ${String(expected)}`)
  }
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualJson = JSON.stringify(actual)
  const expectedJson = JSON.stringify(expected)
  if (actualJson !== expectedJson) {
    throw new Error(`${message}，实际 ${actualJson}，预期 ${expectedJson}`)
  }
}

function assertSetEquals(expected: Map<string, unknown>, actual: Set<string>, label: string): void {
  const missingInActual = [...expected.keys()].filter((item) => !actual.has(item))
  const unexpectedInActual = [...actual].filter((item) => !expected.has(item))
  if (missingInActual.length > 0 || unexpectedInActual.length > 0) {
    throw new Error(
      `${label}不一致`
      + (missingInActual.length > 0 ? `，实际缺失：${missingInActual.join('、')}` : '')
      + (unexpectedInActual.length > 0 ? `，实际多出：${unexpectedInActual.join('、')}` : '')
    )
  }
}

function assertIncludes(source: string, expected: string, message: string): void {
  if (!source.includes(expected)) {
    throw new Error(`${message}，未找到 ${expected}`)
  }
}

function assertNotIncludes(source: string, unexpected: string, message: string): void {
  if (source.includes(unexpected)) {
    throw new Error(`${message}，不应包含 ${unexpected}`)
  }
}

function assertMatch(actual: string, expected: RegExp, message: string): void {
  if (!expected.test(actual)) {
    throw new Error(`${message}，实际 ${actual}，预期匹配 ${expected}`)
  }
}

function assertDefaultMappingProtocol(
  context: Parameters<typeof defaultAccountModelMappingProtocol>[0],
  expectedSource: ReturnType<typeof defaultAccountModelMappingProtocol>['source'],
  expectedUpstream: ReturnType<typeof defaultAccountModelMappingProtocol>['upstream'],
  message: string
): void {
  const option = defaultAccountModelMappingProtocol(context)
  assertEqual(option.source, expectedSource, `${message}默认协议来源族错误`)
  assertEqual(option.upstream, expectedUpstream, `${message}默认协议目标族错误`)
}

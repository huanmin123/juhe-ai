import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  accountHealthCheckEndpointModeOptions,
  defaultAccountHealthCheckEndpointMode
} from '../../views/accounts/accountHealthCheckEndpointMode'
import { defaultHealthCheckEndpointModeForCapabilityDomain } from '../../views/accounts/accountCapabilityDomains'
import { accountTestEndpointModesForAccount, accountTestEndpointModesForModel } from '../../views/accounts/accountEndpointModes'
import { providerModelsForProtocolProfile } from '../../views/accounts/accountEditFormPayload'

// 流式优先（2026-10-19 定案）：缺省首选一律取已启用的 _sse 形态，无 _sse 才回 _json。
assert.equal(defaultAccountHealthCheckEndpointMode('gpt', 'profile_gpt_openai_v1', ['chat_json', 'responses_sse']), 'responses_sse')
assert.equal(defaultAccountHealthCheckEndpointMode('anthropic', 'profile_anthropic_anthropic_v1', ['messages_json']), 'messages_json')
assert.equal(defaultAccountHealthCheckEndpointMode('gemini', 'profile_gemini_native_v1beta', ['generate_content_json']), 'generate_content_json')
assert.equal(defaultAccountHealthCheckEndpointMode('gemini', 'profile_gemini_native_v1beta', ['generate_content_sse', 'count_tokens']), 'generate_content_sse')
assert.equal(defaultAccountHealthCheckEndpointMode('gpt', 'profile_gpt_openai_v1', ['chat_json']), 'chat_json')
assert.equal(defaultAccountHealthCheckEndpointMode('anthropic', 'profile_anthropic_anthropic_v1', ['messages_sse', 'message_token_counting']), 'messages_sse')
assert.equal(defaultAccountHealthCheckEndpointMode('gemini', 'profile_gemini_native_v1beta', ['generate_content_sse', 'count_tokens']), 'generate_content_sse')
assert.deepEqual(accountHealthCheckEndpointModeOptions(['responses_sse', 'chat_json', 'responses_json']), [
  { label: 'Chat Completions（JSON）', value: 'chat_json' },
  { label: 'Responses API（JSON）', value: 'responses_json' },
  { label: 'Responses API（Streaming）', value: 'responses_sse' }
])
assert.deepEqual(accountHealthCheckEndpointModeOptions([
  'messages_sse',
  'message_token_counting',
  'generate_content_sse',
  'count_tokens',
  'embed_content'
]), [
  { label: 'Messages API（Streaming）', value: 'messages_sse' },
  { label: 'Messages Token Counting', value: 'message_token_counting' },
  { label: 'GenerateContent（Streaming）', value: 'generate_content_sse' },
  { label: 'Count Tokens', value: 'count_tokens' },
  { label: 'Embed Content', value: 'embed_content' }
], '2026-11-21 连接测试收口后，输入型 mode 是合法检查形态记录值，必须可展示')
// 健康检查选项必须与后端 AccountHealthCheckEndpointModes 白名单同集：媒体任务
// 查询/内容/取消与工具型 mode 都不是自动探针执行形态，但都是连接测试账户的合法
// 记录值；audio_job_create/realtime_session 同属记录型白名单值，必须可展示与选择。
assert.deepEqual(accountHealthCheckEndpointModeOptions([
  'images_json', 'audio_speech', 'audio_transcription_json', 'audio_job_create',
  'video_create', 'video_get', 'video_content', 'video_cancel', 'realtime_session',
  'chat_json', 'chat_sse', 'responses_json', 'responses_sse',
  'messages_json', 'messages_sse',
  'generate_content_json', 'generate_content_sse',
  'interactions_json', 'interactions_sse',
  'embeddings_json', 'rerank_json'
]).map((option) => option.value), [
  'images_json', 'audio_speech', 'audio_transcription_json', 'audio_job_create',
  'video_create', 'video_get', 'video_content', 'video_cancel', 'realtime_session',
  'chat_json', 'chat_sse', 'responses_json', 'responses_sse',
  'messages_json', 'messages_sse',
  'generate_content_json', 'generate_content_sse',
  'interactions_json', 'interactions_sse',
  'embeddings_json', 'rerank_json'
], '健康检查选项必须与后端 27 值白名单对齐')
assert.equal(
  defaultAccountHealthCheckEndpointMode('qwen', 'profile_qwen_openai_v1', ['audio_job_create']),
  'audio_job_create',
  '仅声明长音频任务的 qwen 账户必须默认落到可保存的 audio_job_create'
)
assert.equal(
  defaultAccountHealthCheckEndpointMode('qwen', 'profile_qwen_openai_v1', ['realtime_session']),
  'realtime_session',
  '仅声明 realtime 的账户必须默认落到可保存的 realtime_session'
)
// 2026-11-21 五域退役（AI账户上游接口能力设计 §5）：五域不再声明上游接口能力，
// 检查形态按能力域常量默认落值，与后端 DefaultHealthCheckEndpointModeForDomain 对齐。
assert.deepEqual(
  (['chat', 'image', 'audio', 'video', 'embedding', 'rerank'] as const).map(
    (capabilityDomain) => defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain })
  ),
  ['chat_sse', 'images_json', 'audio_speech', 'video_create', 'embeddings_json', 'rerank_json'],
  '检查形态域默认必须与后端 DefaultHealthCheckEndpointModeForDomain 六域映射同值'
)
const draftEndpointModes = accountTestEndpointModesForAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  healthCheckEndpointMode: 'responses_sse',
  credentials: {
    supported_endpoint_modes: ['chat_sse', 'responses_sse', 'chat_json', 'responses_json']
  }
})
assert.equal(draftEndpointModes[0], 'responses_sse', '草稿账户默认测试必须优先使用保存的精确请求形态')
assert(draftEndpointModes.includes('responses_json'), '人工测试仍应保留显式选择其他已启用请求形态的能力')

const accountTestModelsSource = readFileSync(
  new URL('../../views/accounts/useAccountTestModels.ts', import.meta.url),
  'utf8'
)
const updateSelectableTestModelSource = accountTestModelsSource.slice(
  accountTestModelsSource.indexOf('function updateSelectableTestModel'),
  accountTestModelsSource.indexOf('function resetTestModels')
)
assert.match(
  updateSelectableTestModelSource,
  /applyTestEndpointModes\(option\.testEndpointModes, false\)/,
  '切换模型必须直接同步服务端给出的请求形态'
)
assert.match(
  updateSelectableTestModelSource,
  /option\.testEndpointModes/,
  '模型目录选项必须预载请求形态'
)
assert.doesNotMatch(
  accountTestModelsSource,
  /testModelCapabilities\(/,
  '浏览器端不得二次读取当前模型能力'
)
assert.doesNotMatch(
  accountTestModelsSource,
  /accountTestEndpointModesForModelOption/,
  '模型列表不得再承载已拆分的模型能力逻辑'
)

const imageEndpointModes = accountTestEndpointModesForModel({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  credentials: { supported_endpoint_modes: ['responses_json', 'responses_sse'] }
}, undefined, { supportedApiProtocols: ['images'] })
assert.deepEqual(imageEndpointModes, ['images_json'], '纯图片模型必须直接收口到 Images API')
// v8-C 对话域纯粹化：providerModelsForProtocolProfile 不再为 gpt api_key 追加
// 'images'——对话账户的模型选项（域 mode 过滤后的协议层）必须排除 image-only 模型；
// 图像域账户的候选由域 mode 过滤 + 媒体域 token 过滤承担，不经此协议层放行。
assert.deepEqual(providerModelsForProtocolProfile([
  { label: 'gpt-image-2', value: 'gpt-image-2', supportedApiProtocols: ['images'] },
  { label: 'gpt-5.2', value: 'gpt-5.2', supportedApiProtocols: ['responses'] }
], {
  id: 'profile_gpt_openai_v1',
  providerCode: 'gpt',
  name: 'GPT / OpenAI v1',
  enabled: true,
  protocolCode: 'openai',
  protocolVersion: 'v1',
  baseUrl: 'https://api.openai.com/v1',
  defaultHealthCheckModel: '',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['responses', 'chat'],
  endpointFamilies: [{ code: 'responses', name: 'Responses' }]
}).map((option) => option.value), ['gpt-5.2'], 'GPT 模型选项必须排除 image-only 模型（对话域无 images lane）')

const healthCheckFieldSource = readFileSync(
  new URL('../../views/accounts/AccountHealthCheckModelField.vue', import.meta.url),
  'utf8'
)
assert.match(healthCheckFieldSource, /accountTestEndpointModesForModel/, '账户编辑检查协议必须联合模型目录能力计算')
assert.match(healthCheckFieldSource, /v-if="imageOnlyModel"[\s\S]*value="images_json"/, '纯图片模型必须显示只读 Images API')
assert.match(healthCheckFieldSource, /props\.form\.healthCheckEndpointMode\s*=\s*'images_json'/, '纯图片模型必须把表单检查请求形态规范化为 Images API')

const providerModelOptionsSource = readFileSync(
  new URL('../../views/accounts/useAccountProviderModelOptions.ts', import.meta.url),
  'utf8'
)
assert.match(providerModelOptionsSource, /supportedApiProtocols: item\.supportedApiProtocols/, '账户模型选项必须直接保留后端返回的目录协议')
assert.doesNotMatch(providerModelOptionsSource, /modelCapabilities\(/, '账户模型加载不得再逐模型请求能力接口')

for (const relativePath of [
  '../../views/accounts/accountBatchEditForm.ts',
  '../../views/accounts/accountSavePayload.ts'
]) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.doesNotMatch(source, /检查协议必须选择.*非流式 JSON/, `${relativePath} 不得把健康检查限制为非流式 JSON`)
  assert.match(source, /检查请求形态必须选择.*JSON 或流式/, `${relativePath} 必须说明 JSON 和流式能力都可选择`)
}

const savePayloadSource = readFileSync(
  new URL('../../views/accounts/accountSavePayload.ts', import.meta.url),
  'utf8'
)
assert.match(savePayloadSource, /form\.healthCheckEndpointMode\s*!==\s*'images_json'/, '保存校验必须允许由模型目录证实的 Images API 形态')

console.log('前端 AI 账户健康检查请求形态回归通过')

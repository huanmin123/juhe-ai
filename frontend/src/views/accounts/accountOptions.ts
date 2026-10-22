import type { AccountStatus, ProviderDefinition } from '@/types/domain'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY,
  ANTHROPIC_MESSAGES_FAMILY,
  ANTHROPIC_MODELS_FAMILY,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_COUNT_TOKENS_FAMILY,
  GEMINI_EMBED_CONTENT_FAMILY,
  GEMINI_INTERACTIONS_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  GEMINI_PROVIDER_CODE,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  XAI_OPENAI_V1_PROFILE_ID,
  XAI_PROVIDER_CODE,
  MINIMAX_OPENAI_V1_PROFILE_ID,
  MINIMAX_PROVIDER_CODE,
  OPENAI_AUDIO_TRANSCRIPTION_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY,
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY,
  QWEN_OPENAI_V1_PROFILE_ID,
  QWEN_PROVIDER_CODE,
  VOLCENGINE_OPENAI_V1_PROFILE_ID,
  VOLCENGINE_PROVIDER_CODE
} from '@/shared/providerProtocol'

export { GPT_VENDOR_CODE }

export const DEFAULT_OPENAI_SUPPORTED_MODELS = ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini', 'gpt-image-2']
export const DEFAULT_GPT_SUPPORTED_MODELS = [...DEFAULT_OPENAI_SUPPORTED_MODELS]
export const DEFAULT_XAI_SUPPORTED_MODELS = ['grok-4.6', 'grok-4.5']
export const DEFAULT_ANTHROPIC_SUPPORTED_MODELS = ['claude-fable-5-1', 'claude-opus-5', 'claude-sonnet-5', 'claude-haiku-4-5']
export const DEFAULT_GEMINI_SUPPORTED_MODELS = ['gemini-3.7-flash', 'gemini-3.5-flash', 'gemini-3.1-pro-preview', 'gemini-2.5-pro', 'gemini-2.5-flash']
export const DEFAULT_DEEPSEEK_SUPPORTED_MODELS = ['deepseek-v4-flash', 'deepseek-v4-pro']
export const DEFAULT_GLM_SUPPORTED_MODELS = ['glm-5.3', 'glm-5.2', 'glm-5.1', 'glm-5', 'glm-5-turbo', 'glm-4.7-flashx', 'glm-4.7-flash']
export const DEFAULT_HYBRID_SUPPORTED_MODELS = ['gpt-5.6-sol', 'claude-opus-5', 'gemini-3.7-flash', 'deepseek-v4-flash', 'glm-5.3']

export const OPENAI_COMPATIBLE_PROVIDER: ProviderDefinition = {
  id: OPENAI_COMPATIBLE_PROVIDER_CODE,
  code: OPENAI_COMPATIBLE_PROVIDER_CODE,
  name: 'OpenAI 兼容',
  enabled: true,
  defaultProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultHealthCheckModel: 'gpt-5.5',
  defaultSupportedModels: DEFAULT_OPENAI_SUPPORTED_MODELS,
  accountTypes: ['api_key'],
  capabilities: ['responses', 'chat', 'passthrough'],
  protocolProfiles: [
    {
      id: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      name: 'OpenAI 兼容 / OpenAI v1',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.openai.com/v1',
      defaultHealthCheckModel: 'gpt-5.5',
      accountTypes: ['api_key'],
      capabilities: ['responses', 'chat', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_RESPONSES_FAMILY, name: 'Responses' },
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    }
  ]
}

export const GPT_PROVIDER: ProviderDefinition = {
  id: GPT_VENDOR_CODE,
  code: GPT_VENDOR_CODE,
  name: 'GPT',
  parentCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  enabled: true,
  defaultProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultHealthCheckModel: '',
  defaultSupportedModels: DEFAULT_GPT_SUPPORTED_MODELS,
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['responses', 'chat'],
  protocolProfiles: [
    {
      id: GPT_OPENAI_V1_PROFILE_ID,
      providerCode: GPT_VENDOR_CODE,
      name: 'GPT / OpenAI v1',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.openai.com/v1',
      defaultHealthCheckModel: '',
      accountTypes: ['oauth', 'api_key'],
      capabilities: ['responses', 'chat'],
      endpointFamilies: [
        { code: OPENAI_RESPONSES_FAMILY, name: 'Responses' },
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    }
  ]
}

export const XAI_PROVIDER: ProviderDefinition = {
  id: XAI_PROVIDER_CODE,
  code: XAI_PROVIDER_CODE,
  name: 'xAI / Grok',
  parentCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  enabled: true,
  defaultProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.x.ai/v1',
  defaultHealthCheckModel: 'grok-4.6',
  defaultSupportedModels: DEFAULT_XAI_SUPPORTED_MODELS,
  accountTypes: ['api_key', 'oauth'],
  capabilities: ['responses', 'chat', 'passthrough'],
  protocolProfiles: [
    {
      id: XAI_OPENAI_V1_PROFILE_ID,
      providerCode: XAI_PROVIDER_CODE,
      name: 'xAI / OpenAI v1',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.x.ai/v1',
      defaultHealthCheckModel: 'grok-4.6',
      accountTypes: ['api_key', 'oauth'],
      capabilities: ['responses', 'chat', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' },
        { code: OPENAI_RESPONSES_FAMILY, name: 'Responses' }
      ]
    }
  ]
}

export const ANTHROPIC_PROVIDER: ProviderDefinition = {
  id: ANTHROPIC_PROVIDER_CODE,
  code: ANTHROPIC_PROVIDER_CODE,
  name: 'Anthropic',
  enabled: true,
  defaultProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  baseUrl: 'https://api.anthropic.com/v1',
  defaultHealthCheckModel: 'claude-opus-5',
  defaultSupportedModels: DEFAULT_ANTHROPIC_SUPPORTED_MODELS,
  accountTypes: ['api_key', 'oauth'],
  capabilities: ['messages', 'models', 'count_tokens', 'passthrough'],
  protocolProfiles: [
    {
      id: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      providerCode: ANTHROPIC_PROVIDER_CODE,
      name: 'Anthropic / Anthropic v1',
      enabled: true,
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
      baseUrl: 'https://api.anthropic.com/v1',
      defaultHealthCheckModel: 'claude-opus-5',
      accountTypes: ['api_key', 'oauth'],
      capabilities: ['messages', 'models', 'count_tokens', 'passthrough'],
      endpointFamilies: [
        { code: ANTHROPIC_MESSAGES_FAMILY, name: 'Messages' },
        { code: ANTHROPIC_MODELS_FAMILY, name: 'Models' },
        { code: ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY, name: 'Message Token Counting' }
      ]
    }
  ]
}

export const GEMINI_PROVIDER: ProviderDefinition = {
  id: GEMINI_PROVIDER_CODE,
  code: GEMINI_PROVIDER_CODE,
  name: 'Google Gemini',
  enabled: true,
  defaultProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
  protocolCode: GEMINI_PROTOCOL_CODE,
  protocolVersion: GEMINI_PROTOCOL_VERSION,
  baseUrl: 'https://generativelanguage.googleapis.com',
  defaultHealthCheckModel: 'gemini-3.7-flash',
  defaultSupportedModels: DEFAULT_GEMINI_SUPPORTED_MODELS,
  accountTypes: ['api_key', 'google_oauth'],
  capabilities: ['generate_content', 'count_tokens', 'embed_content', 'interactions', 'passthrough'],
  protocolProfiles: [
    {
      id: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      providerCode: GEMINI_PROVIDER_CODE,
      name: 'Google Gemini / Gemini v1beta',
      enabled: true,
      protocolCode: GEMINI_PROTOCOL_CODE,
      protocolVersion: GEMINI_PROTOCOL_VERSION,
      baseUrl: 'https://generativelanguage.googleapis.com',
      defaultHealthCheckModel: 'gemini-3.7-flash',
      accountTypes: ['api_key', 'google_oauth'],
      capabilities: ['generate_content', 'count_tokens', 'embed_content', 'interactions', 'passthrough'],
      endpointFamilies: [
        { code: GEMINI_GENERATE_CONTENT_FAMILY, name: 'Generate Content' },
        { code: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, name: 'Stream Generate Content' },
        { code: GEMINI_COUNT_TOKENS_FAMILY, name: 'Count Tokens' },
        { code: GEMINI_EMBED_CONTENT_FAMILY, name: 'Embed Content' },
        { code: GEMINI_INTERACTIONS_FAMILY, name: 'Interactions' }
      ]
    },
    {
      id: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
      providerCode: GEMINI_PROVIDER_CODE,
      name: 'Google Gemini / OpenAI Chat',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://generativelanguage.googleapis.com/v1beta/openai',
      defaultHealthCheckModel: 'gemini-3.7-flash',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    }
  ]
}

export const HYBRID_PROVIDER: ProviderDefinition = {
  id: HYBRID_PROVIDER_CODE,
  code: HYBRID_PROVIDER_CODE,
  name: '混合供应商',
  enabled: true,
  defaultProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: '',
  defaultHealthCheckModel: '',
  defaultSupportedModels: DEFAULT_HYBRID_SUPPORTED_MODELS,
  accountTypes: ['api_key'],
  capabilities: ['chat', 'messages', 'generate_content', 'stream_generate_content', 'bridge'],
  protocolProfiles: [
    {
      id: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
      providerCode: HYBRID_PROVIDER_CODE,
      name: '混合供应商',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: '',
      defaultHealthCheckModel: '',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'messages', 'generate_content', 'stream_generate_content', 'bridge'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' },
        { code: ANTHROPIC_MESSAGES_FAMILY, name: 'Messages' },
        { code: GEMINI_GENERATE_CONTENT_FAMILY, name: 'Generate Content' },
        { code: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, name: 'Stream Generate Content' }
      ]
    }
  ]
}

export const DEEPSEEK_PROVIDER: ProviderDefinition = {
  id: DEEPSEEK_PROVIDER_CODE,
  code: DEEPSEEK_PROVIDER_CODE,
  name: 'DeepSeek',
  enabled: true,
  defaultProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.deepseek.com',
  defaultHealthCheckModel: 'deepseek-v4-flash',
  defaultSupportedModels: DEFAULT_DEEPSEEK_SUPPORTED_MODELS,
  accountTypes: ['api_key'],
  capabilities: ['chat', 'responses', 'messages', 'passthrough'],
  protocolProfiles: [
    {
      id: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      providerCode: DEEPSEEK_PROVIDER_CODE,
      name: 'DeepSeek / OpenAI v1',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.deepseek.com',
      defaultHealthCheckModel: 'deepseek-v4-flash',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'responses', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' },
        { code: OPENAI_RESPONSES_FAMILY, name: 'Responses' }
      ]
    },
    {
      id: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
      providerCode: DEEPSEEK_PROVIDER_CODE,
      name: 'DeepSeek / Anthropic v1',
      enabled: true,
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
      baseUrl: 'https://api.deepseek.com/anthropic',
      defaultHealthCheckModel: 'deepseek-v4-flash',
      accountTypes: ['api_key'],
      capabilities: ['messages', 'models', 'passthrough'],
      endpointFamilies: [
        { code: ANTHROPIC_MESSAGES_FAMILY, name: 'Messages' },
        { code: ANTHROPIC_MODELS_FAMILY, name: 'Models' }
      ]
    }
  ]
}

export const GLM_PROVIDER: ProviderDefinition = {
  id: GLM_PROVIDER_CODE,
  code: GLM_PROVIDER_CODE,
  name: '智谱 GLM',
  enabled: true,
  defaultProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://open.bigmodel.cn/api/paas/v4/',
  defaultHealthCheckModel: 'glm-5.3',
  defaultSupportedModels: DEFAULT_GLM_SUPPORTED_MODELS,
  accountTypes: ['api_key'],
  capabilities: ['chat', 'messages', 'passthrough'],
  protocolProfiles: [
    {
      id: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      providerCode: GLM_PROVIDER_CODE,
      name: '智谱 GLM 通用 / OpenAI Chat',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://open.bigmodel.cn/api/paas/v4/',
      defaultHealthCheckModel: 'glm-5.3',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'messages', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    },
    {
      id: GLM_CODING_OPENAI_V1_PROFILE_ID,
      providerCode: GLM_PROVIDER_CODE,
      name: '智谱 GLM Coding / OpenAI Chat',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
      defaultHealthCheckModel: 'glm-5.3',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    },
    {
      id: GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
      providerCode: GLM_PROVIDER_CODE,
      name: '智谱 GLM Coding / Anthropic v1',
      enabled: true,
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
      baseUrl: 'https://open.bigmodel.cn/api/anthropic',
      defaultHealthCheckModel: 'glm-5.3',
      accountTypes: ['api_key'],
      capabilities: ['messages', 'models', 'passthrough'],
      endpointFamilies: [
        { code: ANTHROPIC_MESSAGES_FAMILY, name: 'Messages' },
        { code: ANTHROPIC_MODELS_FAMILY, name: 'Models' }
      ]
    }
  ]
}

// 媒体域三家供应商不在对话域 options 清单，也没有旧协议档案可读（B4 退役后
// `/providers/definitions` 的 protocolProfiles 恒为空）；媒体表单按
// `protocolProfiles[].endpointFamilies` 过滤供应商与推导可勾选 modes，缺本地
// 回退会整批静默消失（T47）。档案 id 与后端 accountscore registry 的 preset
// 对应关系见 shared/providerProtocol.ts 的 CONNECTION_PRESET_BY_PROFILE_ID。
const MINIMAX_PROVIDER: ProviderDefinition = {
  id: MINIMAX_PROVIDER_CODE,
  code: MINIMAX_PROVIDER_CODE,
  name: 'MiniMax',
  enabled: true,
  defaultProtocolProfileId: MINIMAX_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.minimax.chat',
  defaultHealthCheckModel: 'MiniMax-M3',
  defaultSupportedModels: ['MiniMax-M3', 'MiniMax-H3', 'image-01', 'speech-2.8-hd'],
  accountTypes: ['api_key'],
  capabilities: ['video', 'audio'],
  protocolProfiles: [
    {
      id: MINIMAX_OPENAI_V1_PROFILE_ID,
      providerCode: MINIMAX_PROVIDER_CODE,
      name: 'MiniMax / 媒体',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.minimax.chat',
      defaultHealthCheckModel: 'MiniMax-M3',
      accountTypes: ['api_key'],
      capabilities: ['video', 'audio'],
      endpointFamilies: [
        { code: OPENAI_VIDEO_GENERATION_FAMILY, name: '视频生成' },
        { code: OPENAI_TTS_FAMILY, name: '语音合成' }
      ]
    }
  ]
}

const VOLCENGINE_PROVIDER: ProviderDefinition = {
  id: VOLCENGINE_PROVIDER_CODE,
  code: VOLCENGINE_PROVIDER_CODE,
  name: '火山引擎',
  enabled: true,
  defaultProtocolProfileId: VOLCENGINE_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://ark.cn-beijing.volces.com',
  defaultHealthCheckModel: 'doubao-seed-2-1-pro-260915',
  defaultSupportedModels: ['doubao-seed-2-1-pro-260915', 'doubao-seedance-2-5-260628', 'doubao-seedream-5-0-260128', 'seed-tts-2.0'],
  accountTypes: ['api_key'],
  capabilities: ['chat', 'video', 'audio', 'image'],
  protocolProfiles: [
    {
      id: VOLCENGINE_OPENAI_V1_PROFILE_ID,
      providerCode: VOLCENGINE_PROVIDER_CODE,
      name: '火山引擎 Ark / OpenAI 兼容',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://ark.cn-beijing.volces.com',
      defaultHealthCheckModel: 'doubao-seed-2-1-pro-260915',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'video', 'audio', 'image'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' },
        { code: OPENAI_VIDEO_GENERATION_FAMILY, name: '视频生成' },
        { code: OPENAI_TTS_FAMILY, name: '语音合成' },
        { code: OPENAI_AUDIO_TRANSCRIPTION_FAMILY, name: '音频转写' }
      ]
    }
  ]
}

const QWEN_PROVIDER: ProviderDefinition = {
  id: QWEN_PROVIDER_CODE,
  code: QWEN_PROVIDER_CODE,
  name: '通义千问',
  enabled: true,
  defaultProtocolProfileId: QWEN_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
  defaultHealthCheckModel: 'qwen3.8-max',
  defaultSupportedModels: ['qwen3.8-max', 'wan3.0-video', 'qwen3-tts-flash', 'qwen-image-3.0'],
  accountTypes: ['api_key'],
  capabilities: ['chat', 'video', 'audio'],
  protocolProfiles: [
    {
      id: QWEN_OPENAI_V1_PROFILE_ID,
      providerCode: QWEN_PROVIDER_CODE,
      name: '通义千问 DashScope / OpenAI 兼容',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
      defaultHealthCheckModel: 'qwen3.8-max',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'video', 'audio'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' },
        { code: OPENAI_VIDEO_GENERATION_FAMILY, name: '视频生成' },
        { code: OPENAI_AUDIO_TRANSCRIPTION_FAMILY, name: '音频转写' }
      ]
    }
  ]
}

export const FALLBACK_PROVIDERS: ProviderDefinition[] = [GPT_PROVIDER, XAI_PROVIDER, GLM_PROVIDER, DEEPSEEK_PROVIDER, HYBRID_PROVIDER, OPENAI_COMPATIBLE_PROVIDER, ANTHROPIC_PROVIDER, GEMINI_PROVIDER, MINIMAX_PROVIDER, VOLCENGINE_PROVIDER, QWEN_PROVIDER]

export const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20
export const ACCOUNT_PAGE_SIZE = 20

export const statusOptions: Array<{ label: string; value: AccountStatus }> = [
  { label: '可调度', value: 'active' },
  { label: '待检查', value: 'pending_test' },
  { label: '停用', value: 'disabled' },
  { label: '异常', value: 'error' },
  { label: '限流中', value: 'rate_limited' },
  { label: '临时不可调用', value: 'temporary_unavailable' },
  { label: '质量隔离', value: 'quality_isolated' }
]

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY,
  ANTHROPIC_MESSAGES_FAMILY,
  ANTHROPIC_MODELS_FAMILY,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE,
  GEMINI_COUNT_TOKENS_FAMILY,
  GEMINI_EMBED_CONTENT_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_MODELS_FAMILY,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  GEMINI_PROVIDER_CODE,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY
} from '../domain/provider-protocol.js'

export const DEFAULT_GPT_GROUP = {
  id: 'grp_default_gpt_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 GPT 分组',
  providerCode: GPT_VENDOR_CODE,
  description: ''
} as const

export const DEFAULT_OPENAI_COMPATIBLE_GROUP = {
  id: 'grp_default_openai_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 OpenAI 兼容分组',
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  description: ''
} as const

export const DEFAULT_ANTHROPIC_GROUP = {
  id: 'grp_default_anthropic_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 Anthropic 分组',
  providerCode: ANTHROPIC_PROVIDER_CODE,
  description: ''
} as const

export const DEFAULT_GEMINI_GROUP = {
  id: 'grp_default_gemini_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 Gemini 分组',
  providerCode: GEMINI_PROVIDER_CODE,
  description: ''
} as const

export const DEFAULT_DEEPSEEK_GROUP = {
  id: 'grp_default_deepseek_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 DeepSeek 分组',
  providerCode: DEEPSEEK_PROVIDER_CODE,
  description: ''
} as const

export const DEFAULT_GLM_GROUP = {
  id: 'grp_default_glm_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认 GLM 分组',
  providerCode: GLM_PROVIDER_CODE,
  description: ''
} as const

export const DEFAULT_HYBRID_OPENAI_CHAT_GROUP = {
  id: 'grp_default_hybrid_openai_chat_sys_admin',
  systemAccountId: 'sys_admin',
  name: '默认混合供应商分组',
  providerCode: HYBRID_PROVIDER_CODE,
  description: '混合供应商账户保存真实上游凭据和 Base URL，允许账户内配置跨协议入口映射'
} as const

export const DEFAULT_BUILT_IN_GROUPS = [
  DEFAULT_OPENAI_COMPATIBLE_GROUP,
  DEFAULT_GPT_GROUP,
  DEFAULT_DEEPSEEK_GROUP,
  DEFAULT_ANTHROPIC_GROUP,
  DEFAULT_GEMINI_GROUP,
  DEFAULT_GLM_GROUP,
  DEFAULT_HYBRID_OPENAI_CHAT_GROUP
] as const

export const DEFAULT_GLOBAL_SETTINGS = [
  ['appName', '聚合 AI'],
  ['appIcon', '/__aisys__/brand-icon.svg']
] as const

export const DEFAULT_OPENAI_SUPPORTED_MODELS = ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini', 'gpt-image-2'] as const
export const DEFAULT_ANTHROPIC_SUPPORTED_MODELS = ['claude-opus-4-8', 'claude-sonnet-4-6', 'claude-haiku-4-5'] as const
export const DEFAULT_GEMINI_SUPPORTED_MODELS = ['gemini-3.5-flash', 'gemini-3.1-pro-preview', 'gemini-2.5-pro', 'gemini-2.5-flash'] as const
export const DEFAULT_DEEPSEEK_SUPPORTED_MODELS = ['deepseek-v4-flash', 'deepseek-v4-pro'] as const
export const DEFAULT_GLM_SUPPORTED_MODELS = ['glm-5.2', 'glm-5.1', 'glm-5', 'glm-5-turbo', 'glm-4.7-flashx', 'glm-4.7-flash'] as const
export const DEFAULT_HYBRID_SUPPORTED_MODELS = ['gpt-5.6-sol', 'claude-opus-4-8', 'gemini-3.5-flash', 'deepseek-v4-flash', 'glm-5.2'] as const

export const GPT_PROVIDER_SEED = {
  id: GPT_VENDOR_CODE,
  code: GPT_VENDOR_CODE,
  name: 'GPT',
  parentCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  description: 'GPT 官方供应商，继承通用 OpenAI-compatible 能力，并启用 OAuth、Codex Responses 等 GPT 专属能力',
  enabled: 1,
  defaultSupportedModels: DEFAULT_OPENAI_SUPPORTED_MODELS
} as const

export const OPENAI_COMPATIBLE_PROVIDER_SEED = {
  id: OPENAI_COMPATIBLE_PROVIDER_CODE,
  code: OPENAI_COMPATIBLE_PROVIDER_CODE,
  name: 'OpenAI 兼容',
  parentCode: null,
  description: '通用 OpenAI-compatible 供应商，用于接入兼容 OpenAI v1 协议的上游服务，默认只提供 API Key 透传能力',
  enabled: 1,
  defaultSupportedModels: DEFAULT_OPENAI_SUPPORTED_MODELS
} as const

export const ANTHROPIC_PROVIDER_SEED = {
  id: ANTHROPIC_PROVIDER_CODE,
  code: ANTHROPIC_PROVIDER_CODE,
  name: 'Anthropic',
  parentCode: null,
  description: 'Anthropic 官方供应商，当前支持官方 API Key 与 Anthropic Messages 原生协议直连',
  enabled: 1,
  defaultSupportedModels: DEFAULT_ANTHROPIC_SUPPORTED_MODELS
} as const

export const GEMINI_PROVIDER_SEED = {
  id: GEMINI_PROVIDER_CODE,
  code: GEMINI_PROVIDER_CODE,
  name: 'Gemini',
  parentCode: null,
  description: 'Google Gemini 官方供应商，默认使用 Gemini v1beta 原生协议；Codex / OpenAI 客户端通过 Gemini OpenAI Chat 兼容档案接入',
  enabled: 1,
  defaultSupportedModels: DEFAULT_GEMINI_SUPPORTED_MODELS
} as const

export const DEEPSEEK_PROVIDER_SEED = {
  id: DEEPSEEK_PROVIDER_CODE,
  code: DEEPSEEK_PROVIDER_CODE,
  name: 'DeepSeek',
  parentCode: null,
  description: 'DeepSeek 官方供应商，支持 OpenAI-compatible v1 Chat Completions 直连，也支持 Anthropic v1 Messages 档案兼容 Claude Code',
  enabled: 1,
  defaultSupportedModels: DEFAULT_DEEPSEEK_SUPPORTED_MODELS
} as const

export const GLM_PROVIDER_SEED = {
  id: GLM_PROVIDER_CODE,
  code: GLM_PROVIDER_CODE,
  name: '智谱 GLM',
  parentCode: null,
  description: '智谱 GLM 官方供应商，支持通用 GLM API Key、GLM Coding Plan OpenAI Chat 档案，以及 GLM Coding Anthropic v1 Messages 档案',
  enabled: 1,
  defaultSupportedModels: DEFAULT_GLM_SUPPORTED_MODELS
} as const

export const HYBRID_PROVIDER_SEED = {
  id: HYBRID_PROVIDER_CODE,
  code: HYBRID_PROVIDER_CODE,
  name: '混合供应商',
  parentCode: null,
  description: '混合供应商账户用于创建真实上游账户，并在账户内配置允许的下游协议入口和上游模型映射；不指向其他账户、分组或 API Key',
  enabled: 1,
  defaultSupportedModels: DEFAULT_HYBRID_SUPPORTED_MODELS
} as const

export const DEFAULT_PROVIDER_SEEDS = [
  OPENAI_COMPATIBLE_PROVIDER_SEED,
  GPT_PROVIDER_SEED,
  DEEPSEEK_PROVIDER_SEED,
  ANTHROPIC_PROVIDER_SEED,
  GEMINI_PROVIDER_SEED,
  GLM_PROVIDER_SEED,
  HYBRID_PROVIDER_SEED
] as const

export const OPENAI_PROTOCOL_SEED = {
  id: `${OPENAI_PROTOCOL_CODE}_${OPENAI_PROTOCOL_VERSION}`,
  code: OPENAI_PROTOCOL_CODE,
  version: OPENAI_PROTOCOL_VERSION,
  name: 'OpenAI v1',
  description: 'OpenAI-compatible v1 协议；接口族包含 Chat Completions 与 Responses',
  enabled: 1
} as const

export const ANTHROPIC_PROTOCOL_SEED = {
  id: `${ANTHROPIC_PROTOCOL_CODE}_${ANTHROPIC_PROTOCOL_VERSION}`,
  code: ANTHROPIC_PROTOCOL_CODE,
  version: ANTHROPIC_PROTOCOL_VERSION,
  name: 'Anthropic v1',
  description: 'Anthropic 官方 v1 协议；接口族包含 Messages、Models 与 Message Token Counting',
  enabled: 1
} as const

export const GEMINI_PROTOCOL_SEED = {
  id: `${GEMINI_PROTOCOL_CODE}_${GEMINI_PROTOCOL_VERSION}`,
  code: GEMINI_PROTOCOL_CODE,
  version: GEMINI_PROTOCOL_VERSION,
  name: 'Gemini v1beta',
  description: 'Google Gemini v1beta 原生协议；接口族包含 Models、generateContent、streamGenerateContent、countTokens 与 embedContent',
  enabled: 1
} as const

export const DEFAULT_PROTOCOL_SEEDS = [
  OPENAI_PROTOCOL_SEED,
  ANTHROPIC_PROTOCOL_SEED,
  GEMINI_PROTOCOL_SEED
] as const

export const OPENAI_PROTOCOL_ENDPOINT_FAMILY_SEEDS = [
  {
    id: `${OPENAI_PROTOCOL_CODE}_${OPENAI_PROTOCOL_VERSION}_${OPENAI_CHAT_COMPLETIONS_FAMILY}`,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    code: OPENAI_CHAT_COMPLETIONS_FAMILY,
    name: 'Chat Completions',
    description: 'OpenAI v1 /chat/completions 接口族',
    enabled: 1
  },
  {
    id: `${OPENAI_PROTOCOL_CODE}_${OPENAI_PROTOCOL_VERSION}_${OPENAI_RESPONSES_FAMILY}`,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    code: OPENAI_RESPONSES_FAMILY,
    name: 'Responses',
    description: 'OpenAI v1 /responses 接口族',
    enabled: 1
  }
] as const

export const ANTHROPIC_PROTOCOL_ENDPOINT_FAMILY_SEEDS = [
  {
    id: `${ANTHROPIC_PROTOCOL_CODE}_${ANTHROPIC_PROTOCOL_VERSION}_${ANTHROPIC_MESSAGES_FAMILY}`,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    code: ANTHROPIC_MESSAGES_FAMILY,
    name: 'Messages',
    description: 'Anthropic v1 /messages 接口族',
    enabled: 1
  },
  {
    id: `${ANTHROPIC_PROTOCOL_CODE}_${ANTHROPIC_PROTOCOL_VERSION}_${ANTHROPIC_MODELS_FAMILY}`,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    code: ANTHROPIC_MODELS_FAMILY,
    name: 'Models',
    description: 'Anthropic v1 /models 接口族',
    enabled: 1
  },
  {
    id: `${ANTHROPIC_PROTOCOL_CODE}_${ANTHROPIC_PROTOCOL_VERSION}_${ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY}`,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    code: ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY,
    name: 'Message Token Counting',
    description: 'Anthropic v1 /messages/count_tokens 接口族',
    enabled: 1
  }
] as const

export const GEMINI_PROTOCOL_ENDPOINT_FAMILY_SEEDS = [
  {
    id: `${GEMINI_PROTOCOL_CODE}_${GEMINI_PROTOCOL_VERSION}_${GEMINI_MODELS_FAMILY}`,
    protocolCode: GEMINI_PROTOCOL_CODE,
    protocolVersion: GEMINI_PROTOCOL_VERSION,
    code: GEMINI_MODELS_FAMILY,
    name: 'Models',
    description: 'Gemini v1beta /models 接口族',
    enabled: 1
  },
  {
    id: `${GEMINI_PROTOCOL_CODE}_${GEMINI_PROTOCOL_VERSION}_${GEMINI_GENERATE_CONTENT_FAMILY}`,
    protocolCode: GEMINI_PROTOCOL_CODE,
    protocolVersion: GEMINI_PROTOCOL_VERSION,
    code: GEMINI_GENERATE_CONTENT_FAMILY,
    name: 'generateContent',
    description: 'Gemini v1beta :generateContent 接口族',
    enabled: 1
  },
  {
    id: `${GEMINI_PROTOCOL_CODE}_${GEMINI_PROTOCOL_VERSION}_${GEMINI_STREAM_GENERATE_CONTENT_FAMILY}`,
    protocolCode: GEMINI_PROTOCOL_CODE,
    protocolVersion: GEMINI_PROTOCOL_VERSION,
    code: GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
    name: 'streamGenerateContent',
    description: 'Gemini v1beta :streamGenerateContent SSE 接口族',
    enabled: 1
  },
  {
    id: `${GEMINI_PROTOCOL_CODE}_${GEMINI_PROTOCOL_VERSION}_${GEMINI_COUNT_TOKENS_FAMILY}`,
    protocolCode: GEMINI_PROTOCOL_CODE,
    protocolVersion: GEMINI_PROTOCOL_VERSION,
    code: GEMINI_COUNT_TOKENS_FAMILY,
    name: 'countTokens',
    description: 'Gemini v1beta :countTokens 接口族',
    enabled: 1
  },
  {
    id: `${GEMINI_PROTOCOL_CODE}_${GEMINI_PROTOCOL_VERSION}_${GEMINI_EMBED_CONTENT_FAMILY}`,
    protocolCode: GEMINI_PROTOCOL_CODE,
    protocolVersion: GEMINI_PROTOCOL_VERSION,
    code: GEMINI_EMBED_CONTENT_FAMILY,
    name: 'embedContent',
    description: 'Gemini v1beta :embedContent 接口族',
    enabled: 1
  }
] as const

export const DEFAULT_PROTOCOL_ENDPOINT_FAMILY_SEEDS = [
  ...OPENAI_PROTOCOL_ENDPOINT_FAMILY_SEEDS,
  ...ANTHROPIC_PROTOCOL_ENDPOINT_FAMILY_SEEDS,
  ...GEMINI_PROTOCOL_ENDPOINT_FAMILY_SEEDS
] as const

export const GPT_OPENAI_V1_PROFILE_SEED = {
  id: GPT_OPENAI_V1_PROFILE_ID,
  providerCode: GPT_VENDOR_CODE,
  name: 'GPT / OpenAI v1',
  description: 'GPT 供应商的 OpenAI v1 协议档案，支持 OAuth 与 API Key 两种账户接入方式',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultHealthCheckModel: 'gpt-5.6-sol',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['responses', 'chat'],
  endpointFamilies: [OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_RESPONSES_FAMILY]
} as const

export const OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_SEED = {
  id: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  name: 'OpenAI 兼容 / OpenAI v1',
  description: '通用 OpenAI-compatible 供应商的 OpenAI v1 协议档案，仅承载 API Key 透传、模型目录和通用协议策略',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultHealthCheckModel: 'gpt-5.6-sol',
  accountTypes: ['api_key'],
  capabilities: ['responses', 'chat', 'passthrough'],
  endpointFamilies: [OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_RESPONSES_FAMILY]
} as const

export const ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED = {
  id: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  providerCode: ANTHROPIC_PROVIDER_CODE,
  name: 'Anthropic / Anthropic v1',
  description: 'Anthropic 官方 API Key 协议档案，仅承载 x-api-key、anthropic-version 与 Messages 原生协议',
  enabled: 1,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  baseUrl: 'https://api.anthropic.com/v1',
  defaultHealthCheckModel: 'claude-opus-4-8',
  accountTypes: ['api_key'],
  capabilities: ['messages', 'models', 'count_tokens', 'passthrough'],
  endpointFamilies: [ANTHROPIC_MESSAGES_FAMILY, ANTHROPIC_MODELS_FAMILY, ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY]
} as const

export const GEMINI_NATIVE_V1BETA_PROFILE_SEED = {
  id: GEMINI_NATIVE_V1BETA_PROFILE_ID,
  providerCode: GEMINI_PROVIDER_CODE,
  name: 'Gemini / Gemini v1beta',
  description: 'Gemini 官方 API Key 协议档案，承载 x-goog-api-key 与 Gemini v1beta 原生协议直连',
  enabled: 1,
  protocolCode: GEMINI_PROTOCOL_CODE,
  protocolVersion: GEMINI_PROTOCOL_VERSION,
  baseUrl: 'https://generativelanguage.googleapis.com',
  defaultHealthCheckModel: 'gemini-3.5-flash',
  accountTypes: ['api_key'],
  capabilities: ['generate_content', 'stream_generate_content', 'count_tokens', 'embed_content', 'models', 'passthrough'],
  endpointFamilies: [
    GEMINI_MODELS_FAMILY,
    GEMINI_GENERATE_CONTENT_FAMILY,
    GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
    GEMINI_COUNT_TOKENS_FAMILY,
    GEMINI_EMBED_CONTENT_FAMILY
  ]
} as const

export const GEMINI_OPENAI_CHAT_V1BETA_PROFILE_SEED = {
  id: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  providerCode: GEMINI_PROVIDER_CODE,
  name: 'Gemini / OpenAI Chat',
  description: 'Gemini 官方 OpenAI Chat Completions 兼容档案，仅用于 OpenAI Chat 直连和 Codex Responses 显式模型映射，不承载 Gemini 原生协议',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://generativelanguage.googleapis.com/v1beta/openai',
  defaultHealthCheckModel: 'gemini-3.5-flash',
  accountTypes: ['api_key'],
  capabilities: ['chat', 'passthrough'],
  endpointFamilies: [OPENAI_CHAT_COMPLETIONS_FAMILY]
} as const

export const DEEPSEEK_OPENAI_V1_PROFILE_SEED = {
  id: DEEPSEEK_OPENAI_V1_PROFILE_ID,
  providerCode: DEEPSEEK_PROVIDER_CODE,
  name: 'DeepSeek / OpenAI v1',
  description: 'DeepSeek 供应商的 OpenAI-compatible v1 协议档案，承载 API Key、Chat Completions、DeepSeek 响应扩展字段与 Codex Responses 桥接',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.deepseek.com',
  defaultHealthCheckModel: 'deepseek-v4-flash',
  accountTypes: ['api_key'],
  capabilities: ['chat', 'passthrough'],
  endpointFamilies: [OPENAI_CHAT_COMPLETIONS_FAMILY]
} as const

export const DEEPSEEK_ANTHROPIC_V1_PROFILE_SEED = {
  id: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  providerCode: DEEPSEEK_PROVIDER_CODE,
  name: 'DeepSeek / Anthropic v1',
  description: 'DeepSeek 供应商的 Anthropic v1 Messages 协议档案，承载 Claude Code 使用的 /v1/messages 与 /v1/models 直连',
  enabled: 1,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  baseUrl: 'https://api.deepseek.com/anthropic',
  defaultHealthCheckModel: 'deepseek-v4-flash',
  accountTypes: ['api_key'],
  capabilities: ['messages', 'models', 'passthrough'],
  endpointFamilies: [ANTHROPIC_MESSAGES_FAMILY, ANTHROPIC_MODELS_FAMILY]
} as const

export const GLM_GENERAL_OPENAI_V1_PROFILE_SEED = {
  id: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  providerCode: GLM_PROVIDER_CODE,
  name: '智谱 GLM 通用 / OpenAI Chat',
  description: '智谱通用 GLM API Key 协议档案，使用智谱 OpenAI Chat Completions 兼容端点',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://open.bigmodel.cn/api/paas/v4/',
  defaultHealthCheckModel: 'glm-5.2',
  accountTypes: ['api_key'],
  capabilities: ['chat', 'passthrough'],
  endpointFamilies: [OPENAI_CHAT_COMPLETIONS_FAMILY]
} as const

export const GLM_CODING_OPENAI_V1_PROFILE_SEED = {
  id: GLM_CODING_OPENAI_V1_PROFILE_ID,
  providerCode: GLM_PROVIDER_CODE,
  name: '智谱 GLM Coding / OpenAI Chat',
  description: '智谱 GLM Coding Plan Key 协议档案，使用 Coding Plan OpenAI Chat Completions 兼容端点',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
  defaultHealthCheckModel: 'glm-5.2',
  accountTypes: ['api_key'],
  capabilities: ['chat', 'passthrough'],
  endpointFamilies: [OPENAI_CHAT_COMPLETIONS_FAMILY]
} as const

export const GLM_CODING_ANTHROPIC_V1_PROFILE_SEED = {
  id: GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  providerCode: GLM_PROVIDER_CODE,
  name: '智谱 GLM Coding / Anthropic v1',
  description: '智谱 GLM Coding Plan Key 的 Anthropic v1 Messages 协议档案，面向 Anthropic Messages 客户端直连',
  enabled: 1,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  baseUrl: 'https://open.bigmodel.cn/api/anthropic',
  defaultHealthCheckModel: 'glm-5.2',
  accountTypes: ['api_key'],
  capabilities: ['messages', 'models', 'passthrough'],
  endpointFamilies: [ANTHROPIC_MESSAGES_FAMILY, ANTHROPIC_MODELS_FAMILY]
} as const

export const HYBRID_OPENAI_CHAT_V1_PROFILE_SEED = {
  id: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  providerCode: HYBRID_PROVIDER_CODE,
  name: '混合供应商',
  description: '混合供应商通用 API Key 档案；真实上游 Base URL 和目标协议由账户模型映射显式声明',
  enabled: 1,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: '',
  defaultHealthCheckModel: '',
  accountTypes: ['api_key'],
  capabilities: ['chat', 'responses', 'messages', 'generate_content', 'stream_generate_content', 'bridge'],
  endpointFamilies: [
    OPENAI_CHAT_COMPLETIONS_FAMILY,
    OPENAI_RESPONSES_FAMILY,
    ANTHROPIC_MESSAGES_FAMILY,
    GEMINI_GENERATE_CONTENT_FAMILY,
    GEMINI_STREAM_GENERATE_CONTENT_FAMILY
  ]
} as const

export const HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_SEED = {
  id: HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
  providerCode: HYBRID_PROVIDER_CODE,
  name: '混合供应商 Anthropic Messages',
  description: '混合供应商 Anthropic Messages API Key 档案；下游协议由账户模型映射显式声明',
  enabled: 1,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  baseUrl: '',
  defaultHealthCheckModel: '',
  accountTypes: ['api_key'],
  capabilities: ['messages', 'bridge'],
  endpointFamilies: [
    ANTHROPIC_MESSAGES_FAMILY
  ]
} as const

export const DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS = [
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_SEED,
  GPT_OPENAI_V1_PROFILE_SEED,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_SEED,
  DEEPSEEK_OPENAI_V1_PROFILE_SEED,
  ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_SEED,
  GEMINI_NATIVE_V1BETA_PROFILE_SEED,
  GLM_CODING_OPENAI_V1_PROFILE_SEED,
  GLM_CODING_ANTHROPIC_V1_PROFILE_SEED,
  GLM_GENERAL_OPENAI_V1_PROFILE_SEED,
  HYBRID_OPENAI_CHAT_V1_PROFILE_SEED,
  HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_SEED
] as const

export const DEFAULT_SYSTEM_SETTINGS = [
  ['gatewayTextRawBodyLimitMegabytes', 16],
  ['gptPriorityPriceMultiplier', 2],
  ['gptFlexPriceMultiplier', 0.5],
  ['systemApiRateLimitIpReadPerMinute', 600],
  ['systemApiRateLimitIpReadBurstPer10Seconds', 120],
  ['systemApiRateLimitIpWritePerMinute', 180],
  ['systemApiRateLimitIpWriteBurstPer10Seconds', 40],
  ['systemApiRateLimitUserReadPerMinute', 300],
  ['systemApiRateLimitUserWritePerMinute', 120],
  ['defaultTemporaryUnschedulableMinutes', 2],
  ['temporaryUnschedulableRetryIntervalSeconds', 3],
  ['temporaryUnschedulableRetryAttempts', 3],
  ['streamRequestTimeoutSeconds', 120],
  ['streamIdleTimeoutSeconds', 30],
  ['streamClientTotalWaitTimeoutSeconds', 270],
  ['streamMaxLifetimeSeconds', 1800],
  ['streamFailureThresholdCount', 3],
  ['streamFailureThresholdWindowMinutes', 5],
  ['operationLogRetentionDays', 365],
  ['operationLogMaxChangesPerRecord', 100],
  ['statsAggregationIntervalSeconds', 60],
  ['statsAggregationBatchSize', 2000],
  ['statsAggregationMaxBatchesPerRun', 5],
  ['usageHotWindowRefreshIntervalSeconds', 600],
  ['groupAccountStatsRefreshIntervalSeconds', 60],
  ['systemMetricsSampleIntervalSeconds', 30],
  ['tableMonitorMaxTablesPerRun', 4],
  ['accountQualityRefreshIntervalSeconds', 600],
  ['accountQualityWindowMinutes', 10],
  ['accountTestTaskConcurrency', 100],
  ['accountHealthCheckIntervalHours', 12],
  ['accountHealthCheckJitterMinutes', 120],
  ['accountHealthCheckBatchSize', 20],
  ['accountHealthCheckFailureThreshold', 3],
  ['cooldownAccountRetestIntervalSeconds', 3],
  ['cooldownAccountRetestBatchSize', 10],
  ['cooldownAccountRetestMaxBackoffHours', 12],
  ['cooldownAccountRetestLongTermIntervalHours', 1],
  ['oauthAccessTokenRefreshIntervalSeconds', 60],
  ['oauthAccessTokenRefreshLeadSeconds', 300],
  ['oauthAccessTokenRefreshBatchSize', 20],
  ['oauthAccessTokenRefreshRetryBackoffSeconds', 300],
  ['modelCheckRetentionDays', 30],
  ['runtimeLogIndexRetentionDays', 14],
  ['publicApiLogRetentionDays', 30],
  ['usageRecordRetentionDays', 30],
  ['usageStatsTimezone', Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'],
  ['usageStatsMinuteRetentionHours', 48],
  ['usageStatsHourlyRetentionDays', 60],
  ['usageStatsDailyRetentionDays', 400],
  ['usageStatsWeeklyRetentionWeeks', 104],
  ['usageStatsMonthlyRetentionMonths', 24],
  ['usageRankSnapshotRetentionDays', 30],
  ['systemMetricsRetentionDays', 7],
  ['systemMetricsHourlyRetentionDays', 30]
] as const

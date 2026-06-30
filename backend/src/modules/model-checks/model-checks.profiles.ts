import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  normalizeProviderToken,
  type ProviderProtocolProfileDefinition
} from '../../domain/provider-protocol.js'

export type ModelCheckProbeProtocol = 'openai_responses' | 'openai_chat' | 'anthropic_messages' | 'gemini_native'

export interface ModelCheckProtocolProfile {
  id: 'openai_responses_strong' | 'openai_chat_strong' | 'anthropic_messages_strong' | 'gemini_native_strong'
  protocol: ModelCheckProbeProtocol
  protocolLabel: string
  providerCode: string
  providerProtocolProfileIds: readonly string[]
  models: readonly string[]
  defaultModel: string
}

const gptModels = ['gpt-5.5', 'gpt-5.4'] as const
const anthropicModels = ['claude-opus-4-8', 'claude-opus-4-7'] as const
const glmModels = ['glm-5.2', 'glm-5.1'] as const
const deepSeekModels = ['deepseek-v4-flash', 'deepseek-v4-pro'] as const
const geminiModels = ['gemini-3.5-flash', 'gemini-3.1-pro-preview'] as const

export const modelCheckProtocolProfiles: readonly ModelCheckProtocolProfile[] = [
  {
    id: 'openai_responses_strong',
    protocol: 'openai_responses',
    protocolLabel: 'OpenAI Responses',
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileIds: [GPT_OPENAI_V1_PROFILE_ID],
    models: gptModels,
    defaultModel: gptModels[0]
  },
  {
    id: 'openai_responses_strong',
    protocol: 'openai_responses',
    protocolLabel: 'OpenAI Responses',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileIds: [OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID],
    models: gptModels,
    defaultModel: gptModels[0]
  },
  {
    id: 'openai_chat_strong',
    protocol: 'openai_chat',
    protocolLabel: 'OpenAI Chat Completions',
    providerCode: DEEPSEEK_PROVIDER_CODE,
    providerProtocolProfileIds: [DEEPSEEK_OPENAI_V1_PROFILE_ID],
    models: deepSeekModels,
    defaultModel: deepSeekModels[0]
  },
  {
    id: 'anthropic_messages_strong',
    protocol: 'anthropic_messages',
    protocolLabel: 'Anthropic Messages',
    providerCode: DEEPSEEK_PROVIDER_CODE,
    providerProtocolProfileIds: [DEEPSEEK_ANTHROPIC_V1_PROFILE_ID],
    models: deepSeekModels,
    defaultModel: deepSeekModels[0]
  },
  {
    id: 'openai_chat_strong',
    protocol: 'openai_chat',
    protocolLabel: 'OpenAI Chat Completions',
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileIds: [GLM_GENERAL_OPENAI_V1_PROFILE_ID, GLM_CODING_OPENAI_V1_PROFILE_ID],
    models: glmModels,
    defaultModel: glmModels[0]
  },
  {
    id: 'anthropic_messages_strong',
    protocol: 'anthropic_messages',
    protocolLabel: 'Anthropic Messages',
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileIds: [GLM_CODING_ANTHROPIC_V1_PROFILE_ID],
    models: glmModels,
    defaultModel: glmModels[0]
  },
  {
    id: 'anthropic_messages_strong',
    protocol: 'anthropic_messages',
    protocolLabel: 'Anthropic Messages',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileIds: [ANTHROPIC_ANTHROPIC_V1_PROFILE_ID],
    models: anthropicModels,
    defaultModel: anthropicModels[0]
  },
  {
    id: 'gemini_native_strong',
    protocol: 'gemini_native',
    protocolLabel: 'Gemini native v1beta',
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileIds: [GEMINI_NATIVE_V1BETA_PROFILE_ID],
    models: geminiModels,
    defaultModel: geminiModels[0]
  },
  {
    id: 'openai_chat_strong',
    protocol: 'openai_chat',
    protocolLabel: 'OpenAI Chat Completions',
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileIds: [GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID],
    models: geminiModels,
    defaultModel: geminiModels[0]
  }
] as const

export const supportedModels = uniqueStrings(modelCheckProtocolProfiles.flatMap((profile) => profile.models))
export const supportedModelSet = new Set<string>(supportedModels)
export type SupportedModel = string

export const defaultModel = modelCheckProtocolProfiles[0]?.defaultModel ?? 'gpt-5.5'
export const defaultProfile = 'full'
export const probeSetVersion = 'multi-provider-model-check-v1-strong-retry'
export const distributionSampleCount = 5
export const modelCheckSupportedProtocolLabel = 'OpenAI Responses / OpenAI Chat Completions / Anthropic Messages / Gemini native'

export function normalizeModelCheckModel(value: unknown): string | undefined {
  const text = typeof value === 'string' ? value.trim() : ''
  return supportedModelSet.has(text) ? text : undefined
}

export function findModelCheckProfileForAccount(
  account: ProviderProtocolProfileDefinition | undefined
): ModelCheckProtocolProfile | undefined {
  if (!account) return undefined
  const providerCode = normalizeProviderToken(account.providerCode)
  const profileId = normalizeProviderToken(account.providerProtocolProfileId)
  return modelCheckProtocolProfiles.find((profile) => (
    normalizeProviderToken(profile.providerCode) === providerCode
    && profile.providerProtocolProfileIds.some((item) => normalizeProviderToken(item) === profileId)
  ))
}

export function findModelCheckProfileForAccountModel(
  account: ProviderProtocolProfileDefinition | undefined,
  model: string
): ModelCheckProtocolProfile | undefined {
  const profile = findModelCheckProfileForAccount(account)
  return profile?.models.includes(model) ? profile : undefined
}

export function isModelCheckSupportedAccountProfile(account: ProviderProtocolProfileDefinition | undefined): boolean {
  return Boolean(findModelCheckProfileForAccount(account))
}

export function pairedModelForProfile(profile: ModelCheckProtocolProfile, model: string): string | undefined {
  return profile.models.find((item) => item !== model)
}

export function modelCheckModelsForAccount(account: ProviderProtocolProfileDefinition | undefined): readonly string[] {
  return findModelCheckProfileForAccount(account)?.models ?? []
}

export function sameModelCheckComparisonProfile(
  left: ProviderProtocolProfileDefinition | undefined,
  right: ProviderProtocolProfileDefinition | undefined
): boolean {
  const leftProviderCode = normalizeProviderToken(left?.providerCode)
  const rightProviderCode = normalizeProviderToken(right?.providerCode)
  const leftProfileId = normalizeProviderToken(left?.providerProtocolProfileId)
  const rightProfileId = normalizeProviderToken(right?.providerProtocolProfileId)
  return Boolean(leftProviderCode && rightProviderCode && leftProviderCode === rightProviderCode && leftProfileId && leftProfileId === rightProfileId)
}

function uniqueStrings(values: readonly string[]): string[] {
  return Array.from(new Set(values))
}

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
  normalizeProviderToken
} from '@/shared/providerProtocol'
import type { AccountOptionSummary } from '@/types/domain'

export type ModelCheckAccountProfile = Pick<
  AccountOptionSummary,
  'id' | 'name' | 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion'
> & { modelCheckModels?: string[] }

interface ModelCheckAccountProfileRule {
  providerCode: string
  providerProtocolProfileIds: readonly string[]
  models: readonly string[]
}

const gptModels = ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.4'] as const
const anthropicModels = ['claude-opus-5', 'claude-opus-4-8'] as const
const glmModels = ['glm-5.2', 'glm-5.1'] as const
const deepSeekModels = ['deepseek-v4-flash', 'deepseek-v4-pro'] as const
const geminiModels = ['gemini-3.5-flash', 'gemini-3.1-pro-preview'] as const

const modelCheckAccountProfileRules: readonly ModelCheckAccountProfileRule[] = [
  { providerCode: GPT_VENDOR_CODE, providerProtocolProfileIds: [GPT_OPENAI_V1_PROFILE_ID], models: gptModels },
  { providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE, providerProtocolProfileIds: [OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID], models: gptModels },
  { providerCode: DEEPSEEK_PROVIDER_CODE, providerProtocolProfileIds: [DEEPSEEK_OPENAI_V1_PROFILE_ID, DEEPSEEK_ANTHROPIC_V1_PROFILE_ID], models: deepSeekModels },
  { providerCode: GLM_PROVIDER_CODE, providerProtocolProfileIds: [GLM_GENERAL_OPENAI_V1_PROFILE_ID, GLM_CODING_OPENAI_V1_PROFILE_ID, GLM_CODING_ANTHROPIC_V1_PROFILE_ID], models: glmModels },
  { providerCode: ANTHROPIC_PROVIDER_CODE, providerProtocolProfileIds: [ANTHROPIC_ANTHROPIC_V1_PROFILE_ID], models: anthropicModels },
  { providerCode: GEMINI_PROVIDER_CODE, providerProtocolProfileIds: [GEMINI_NATIVE_V1BETA_PROFILE_ID, GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID], models: geminiModels }
]

export function canRunModelCheckForAccount(account: ModelCheckAccountProfile | undefined): boolean {
  return Boolean(modelCheckRuleForAccount(account))
}

export function canSelectModelCheckAccount(
  account: ModelCheckAccountProfile,
  options: { excludedAccountId?: string } = {}
): boolean {
  if (!canRunModelCheckForAccount(account)) return false
  if (options.excludedAccountId && account.id === options.excludedAccountId) return false
  return Boolean(account.name.trim())
}

export function modelCheckModelsForAccount(account: ModelCheckAccountProfile | undefined): string[] {
  if (account?.modelCheckModels) return [...account.modelCheckModels]
  return [...(modelCheckRuleForAccount(account)?.models ?? [])]
}

export interface ModelCheckRunModelOption {
  label: string
  value: string
}

/**
 * 合并检测目标候选 = 目录可检模型 ∪ 账户支持模型。
 * 目录内候选保持 options.supportedModels 的顺序与 label；目录外的账户支持
 * 模型按账户返回顺序追加，label 直接使用模型 ID。历史筛选下拉不走此合并。
 */
export function mergeModelCheckRunModelOptions(
  supportedModels: ReadonlyArray<{ label: string; value: string }>,
  accountModels: readonly string[]
): ModelCheckRunModelOption[] {
  const catalog = supportedModels.filter((item) => accountModels.includes(item.value))
  const catalogValues = new Set(catalog.map((item) => item.value))
  const extra = accountModels.filter((model) => !catalogValues.has(model))
  return [
    ...catalog.map((item) => ({ label: item.label, value: item.value })),
    ...extra.map((model) => ({ label: model, value: model }))
  ]
}

export function canUseModelCheckModelForAccount(account: ModelCheckAccountProfile | undefined, model: string | undefined): boolean {
  const normalizedModel = model?.trim()
  if (!normalizedModel) return false
  return modelCheckModelsForAccount(account).includes(normalizedModel)
}

export function canSelectTrustedModelCheckAccount(
  account: ModelCheckAccountProfile,
  options: { targetAccount?: ModelCheckAccountProfile; model?: string; excludedAccountId?: string } = {}
): boolean {
  if (!canSelectModelCheckAccount(account, { excludedAccountId: options.excludedAccountId })) return false
  if (!options.targetAccount) return true
  if (!sameModelCheckAccountProfile(account, options.targetAccount)) return false
  // 候选的精确可检模型未知（账户候选列表不携带 modelCheckModels，目录外模型
  // 只在按账户定点查询时返回）时不做模型维度预筛：目标模型可能来自账户支持
  // 模型，静态目录表会误拒实际可用的候选。选定后会定点补拉并清除不兼容项，
  // 发起运行时后端也会对对比账户按模型/协议强校验。
  if (account.modelCheckModels === undefined) return true
  return canUseModelCheckModelForAccount(account, options.model)
}

export function sameModelCheckAccountProfile(left: ModelCheckAccountProfile | undefined, right: ModelCheckAccountProfile | undefined): boolean {
  const leftProviderCode = normalizeProviderToken(left?.providerCode)
  const rightProviderCode = normalizeProviderToken(right?.providerCode)
  const leftProfileId = normalizeProviderToken(left?.providerProtocolProfileId)
  const rightProfileId = normalizeProviderToken(right?.providerProtocolProfileId)
  return Boolean(leftProviderCode && rightProviderCode && leftProviderCode === rightProviderCode && leftProfileId && leftProfileId === rightProfileId)
}

function modelCheckRuleForAccount(account: ModelCheckAccountProfile | undefined): ModelCheckAccountProfileRule | undefined {
  if (!account) return undefined
  const providerCode = normalizeProviderToken(account.providerCode)
  const profileId = normalizeProviderToken(account.providerProtocolProfileId)
  return modelCheckAccountProfileRules.find((rule) => (
    normalizeProviderToken(rule.providerCode) === providerCode
    && rule.providerProtocolProfileIds.some((item) => normalizeProviderToken(item) === profileId)
  ))
}

import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountType, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  XAI_OPENAI_V1_PROFILE_ID,
  isHybridProviderCode,
  isDeepSeekProviderCode,
  isGeminiProviderCode,
  isGlmProviderCode,
  isXaiProviderCode
} from '@/shared/providerProtocol'

import {
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle as buildAccountTypeTitle
} from './accountBasicFormatters'

type ProviderDisplaySource = Pick<ProviderDefinition, 'code' | 'name'>

export interface AccountTypeChoice {
  value: string
  type: AccountType
  providerCode: string
  providerProtocolProfileId: string
  label: string
  description: string
  tag: string
}

interface AccountEditModalTitleOptions {
  cloningSourceId?: string
  editingAuthorizedAccount: boolean
  editingId?: string
  editingSystemAccountLabel?: string
  providerCode?: string
  providerProtocolProfileId?: string
  providers: ProviderDisplaySource[]
  targetSystemAccountLabel?: string
  type: AccountType
  typeTitle?: string
}

export function accountTypeSortWeight(type: AccountType): number {
  if (type === 'api_key') return 0
  if (type === 'oauth') return 1
  return 2
}

export function accountEditProviderName(providerCode: string | undefined, providers: ProviderDisplaySource[]): string {
  return providerDisplayName(providerCode, providers)
}

export function accountEditAccountTypeTitle(providerCode: string, type: AccountType, providers: ProviderDisplaySource[]): string {
  return buildAccountTypeTitle(accountEditProviderName(providerCode, providers), type)
}

export function accountTypeChoiceValue(providerProtocolProfileId: string, type: AccountType): string {
  return `${providerProtocolProfileId}:${type}`
}

export function accountEditCreateModalTitle(baseTitle: string, targetLabel?: string): string {
  return targetLabel ? `${baseTitle}（${targetLabel}）` : baseTitle
}

export function accountEditEditingModalTitle(baseTitle: string, accountLabel?: string): string {
  return accountLabel ? `${baseTitle}（系统账户：${accountLabel}）` : baseTitle
}

export function accountTypeChoicesForProfile(
  profile: ProviderProtocolProfileDefinition | undefined,
  providerCode: string,
  providers: ProviderDisplaySource[]
): AccountTypeChoice[] {
  const indexedChoices = [...(profile?.accountTypes ?? [])]
    .map((type) => ({
      value: accountTypeChoiceValue(profile?.id ?? '', type),
      type,
      providerCode,
      providerProtocolProfileId: profile?.id ?? '',
      label: accountTypeChoiceLabel(providerCode, profile?.id, type, providers),
      description: accountTypeDescription(providerCode, type, profile?.id),
      tag: accountTypeChoiceTag(providerCode, profile?.id, type),
      index: 0
    }))
  return indexedChoices
    .sort((left, right) => accountTypeSortWeight(left.type) - accountTypeSortWeight(right.type))
    .map(({ index: _index, ...choice }) => choice)
}

export function accountTypeChoicesForProvider(
  provider: ProviderDefinition | undefined,
  providers: ProviderDisplaySource[]
): AccountTypeChoice[] {
  if (!provider) return []
  if (isHybridProviderCode(provider.code)) {
    const profile = provider.protocolProfiles.find((item) => item.id === provider.defaultProtocolProfileId)
      ?? provider.protocolProfiles.find((item) => item.enabled)
      ?? provider.protocolProfiles[0]
    return [{
      value: accountTypeChoiceValue(profile?.id ?? provider.defaultProtocolProfileId ?? '', 'api_key'),
      type: 'api_key',
      providerCode: provider.code,
      providerProtocolProfileId: profile?.id ?? provider.defaultProtocolProfileId ?? '',
      label: '混合供应商',
      description: '混合供应商账户保存自己的真实上游凭据和 Base URL，跨协议入口通过账号模型映射配置。',
      tag: '混合供应商'
    }]
  }
  const profiles = provider.protocolProfiles.length
    ? provider.protocolProfiles.filter((profile) => profile.enabled)
    : []
  const sourceProfiles = profiles.length
    ? profiles
    : [{
      id: provider.defaultProtocolProfileId,
      providerCode: provider.code,
      name: provider.name,
      enabled: provider.enabled,
      protocolCode: provider.protocolCode,
      protocolVersion: provider.protocolVersion,
      baseUrl: provider.baseUrl,
      defaultHealthCheckModel: provider.defaultHealthCheckModel,
      accountTypes: provider.accountTypes,
      capabilities: provider.capabilities,
      endpointFamilies: []
    }]
  return sourceProfiles
    .flatMap((profile, profileIndex) => [...(profile.accountTypes ?? [])].map((type, typeIndex) => ({
      value: accountTypeChoiceValue(profile.id, type),
      type,
      providerCode: provider.code,
      providerProtocolProfileId: profile.id,
      label: accountTypeChoiceLabel(provider.code, profile.id, type, providers),
      description: accountTypeDescription(provider.code, type, profile.id),
      tag: accountTypeChoiceTag(provider.code, profile.id, type),
      profileIndex,
      typeIndex
    })))
    .sort((left, right) => accountTypeSortWeight(left.type) - accountTypeSortWeight(right.type)
      || left.profileIndex - right.profileIndex
      || left.typeIndex - right.typeIndex
      || left.label.localeCompare(right.label, 'zh-CN'))
    .map(({ profileIndex: _profileIndex, typeIndex: _typeIndex, ...choice }) => choice)
}

export function accountEditModalTitle(options: AccountEditModalTitleOptions): string {
  if (options.editingAuthorizedAccount) {
    return accountEditEditingModalTitle('编辑授权账户', options.editingSystemAccountLabel)
  }
  if (options.editingId) {
    return accountEditEditingModalTitle('编辑账户', options.editingSystemAccountLabel)
  }
  if (options.cloningSourceId) return '克隆账户'
  if (!options.providerCode) {
    return accountEditCreateModalTitle('添加账户', options.targetSystemAccountLabel)
  }
  if (!options.providerProtocolProfileId) {
    return accountEditCreateModalTitle(`添加 ${accountEditProviderName(options.providerCode, options.providers)} 账户`, options.targetSystemAccountLabel)
  }
  if (!options.type) {
    return accountEditCreateModalTitle(`添加 ${accountEditProviderName(options.providerCode, options.providers)} 账户`, options.targetSystemAccountLabel)
  }
  if (isHybridProviderCode(options.providerCode)) {
    return accountEditCreateModalTitle('添加 混合供应商账户', options.targetSystemAccountLabel)
  }
  return accountEditCreateModalTitle(
    `添加 ${options.typeTitle || accountEditAccountTypeTitle(options.providerCode, options.type, options.providers)} 账户`,
    options.targetSystemAccountLabel
  )
}

function accountTypeChoiceLabel(
  providerCode: string,
  providerProtocolProfileId: string | undefined,
  type: AccountType,
  providers: ProviderDisplaySource[]
): string {
  if (isDeepSeekProviderCode(providerCode) && type === 'api_key') {
    if (providerProtocolProfileId === DEEPSEEK_ANTHROPIC_V1_PROFILE_ID) return 'DeepSeek Claude Code API Key'
    if (providerProtocolProfileId === DEEPSEEK_OPENAI_V1_PROFILE_ID) return 'DeepSeek OpenAI-compatible API Key'
  }
  if (isGlmProviderCode(providerCode) && type === 'api_key') {
    if (providerProtocolProfileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID) return 'GLM Coding Anthropic Key'
    if (providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID) return 'GLM Coding Plan Key'
    if (providerProtocolProfileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID) return '通用 GLM API Key'
  }
  if (isGeminiProviderCode(providerCode) && type === 'api_key') {
    if (providerProtocolProfileId === GEMINI_NATIVE_V1BETA_PROFILE_ID) return 'Gemini 原生 API Key'
    if (providerProtocolProfileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID) return 'Gemini OpenAI Chat API Key'
  }
  if (isGeminiProviderCode(providerCode) && type === 'google_oauth') return 'Gemini Google OAuth'
  if (isXaiProviderCode(providerCode) && type === 'oauth') return 'Grok OAuth'
  if (isXaiProviderCode(providerCode) && type === 'api_key' && providerProtocolProfileId === XAI_OPENAI_V1_PROFILE_ID) {
    return 'xAI 官方 API Key'
  }
  return accountEditAccountTypeTitle(providerCode, type, providers)
}

function accountTypeChoiceTag(
  providerCode: string,
  providerProtocolProfileId: string | undefined,
  type: AccountType
): string {
  if (isDeepSeekProviderCode(providerCode) && type === 'api_key') {
    if (providerProtocolProfileId === DEEPSEEK_ANTHROPIC_V1_PROFILE_ID) return 'Claude Code'
    if (providerProtocolProfileId === DEEPSEEK_OPENAI_V1_PROFILE_ID) return 'OpenAI'
  }
  if (isGlmProviderCode(providerCode) && type === 'api_key') {
    if (providerProtocolProfileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID) return 'Anthropic'
    if (providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID) return 'Coding'
    if (providerProtocolProfileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID) return '通用'
  }
  if (isGeminiProviderCode(providerCode) && type === 'api_key') {
    if (providerProtocolProfileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID) return 'OpenAI Chat'
    return 'Gemini API'
  }
  if (isGeminiProviderCode(providerCode) && type === 'google_oauth') return 'Google OAuth'
  if (isXaiProviderCode(providerCode) && type === 'oauth') return 'Grok OAuth'
  if (isXaiProviderCode(providerCode) && type === 'api_key' && providerProtocolProfileId === XAI_OPENAI_V1_PROFILE_ID) {
    return 'OpenAI v1'
  }
  return accountTypeText(type)
}

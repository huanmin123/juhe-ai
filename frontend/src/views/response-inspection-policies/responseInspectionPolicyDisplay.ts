import { providerDisplayName } from '@/shared/providerDisplay'
import type {
  ProviderDefinition,
  ResponseInspectionPolicyAction,
  ResponseInspectionPolicySummary
} from '@/types/domain'
import { responseInspectionActionLabel } from './responseInspectionActionTemplates'
import {
  responseInspectionAccountCompatibilityOptions,
  responseInspectionClientProfileOptions,
  responseInspectionListText,
  responseInspectionScopedListSummary
} from './responseInspectionPolicyForm'

type ProviderDisplaySource = Pick<ProviderDefinition, 'code' | 'name'>

export function responseInspectionPolicyActionText(action: ResponseInspectionPolicyAction): string {
  return responseInspectionActionLabel(action)
}

export function responseInspectionPolicyScopeText(policy: Pick<ResponseInspectionPolicySummary, 'scopeType'>): string {
  return policy.scopeType === 'provider' ? '供应商层' : '协议层'
}

export function responseInspectionPolicyProtocolText(protocolCode: string): string {
  if (protocolCode === 'openai') return 'OpenAI v1'
  return protocolCode || '-'
}

export function responseInspectionPolicyProviderText(providerCode?: string, providers: ProviderDisplaySource[] = []): string {
  if (!providerCode) return '-'
  return providerDisplayName(providerCode, providers)
}

export function responseInspectionPolicyClientProfileText(values?: ResponseInspectionPolicySummary['match']['clientProfiles']): string {
  return responseInspectionListText(values?.map(responseInspectionPolicyClientProfileLabel))
}

export function responseInspectionPolicyAccountCompatibilityText(values?: ResponseInspectionPolicySummary['match']['accountClientCompatibilities']): string {
  return responseInspectionListText(values?.map(responseInspectionPolicyAccountCompatibilityLabel))
}

export function responseInspectionPolicyMatchSummary(policy: ResponseInspectionPolicySummary): string {
  const match = policy.match
  const parts = [
    responseInspectionScopedListSummary('客户端画像', match.clientProfiles?.map(responseInspectionPolicyClientProfileLabel)),
    responseInspectionScopedListSummary('账号兼容', match.accountClientCompatibilities?.map(responseInspectionPolicyAccountCompatibilityLabel)),
    responseInspectionScopedListSummary('输出包含', match.outputTextIncludes),
    responseInspectionScopedListSummary('输出排除', match.outputTextExcludes),
    responseInspectionScopedListSummary('code', match.errorCodes),
    responseInspectionScopedListSummary('type', match.errorTypes),
    responseInspectionScopedListSummary('错误消息', match.errorMessageIncludes),
    responseInspectionScopedListSummary('完成原因', match.finishReasons),
    responseInspectionScopedListSummary('SSE 原文', match.rawTextIncludes),
    responseInspectionScopedListSummary('JSON路径', match.jsonPathsExists)
  ].filter(Boolean)
  return parts.length ? parts.join('；') : '-'
}

function responseInspectionPolicyClientProfileLabel(value: string): string {
  return responseInspectionClientProfileOptions.find((option) => option.value === value)?.label ?? value
}

function responseInspectionPolicyAccountCompatibilityLabel(value: string): string {
  return responseInspectionAccountCompatibilityOptions.find((option) => option.value === value)?.label ?? value
}

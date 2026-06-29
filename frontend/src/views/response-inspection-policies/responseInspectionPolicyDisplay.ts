import { providerDisplayName } from '@/shared/providerDisplay'
import type {
  ProviderDefinition,
  ResponseInspectionPolicyAction,
  ResponseInspectionPolicySummary
} from '@/types/domain'
import { responseInspectionActionLabel } from './responseInspectionActionTemplates'
import {
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
  if (protocolCode === 'anthropic') return 'Anthropic v1'
  if (protocolCode === 'gemini') return 'Gemini v1beta'
  return protocolCode || '-'
}

export function responseInspectionPolicyProviderText(providerCode?: string, providers: ProviderDisplaySource[] = []): string {
  if (!providerCode) return '-'
  return providerDisplayName(providerCode, providers)
}

export function responseInspectionPolicyClientProfileText(values?: ResponseInspectionPolicySummary['match']['clientProfiles']): string {
  return responseInspectionListText(values?.map(responseInspectionPolicyClientProfileLabel))
}

export function responseInspectionPolicyMatchSummary(policy: ResponseInspectionPolicySummary): string {
  const match = policy.match
  const parts = [
    responseInspectionScopedListSummary('请求客户端', match.clientProfiles?.map(responseInspectionPolicyClientProfileLabel)),
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

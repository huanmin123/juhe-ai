import type {
  ResponseInspectionPolicyAction,
  ResponseInspectionPolicyOverview
} from '@/types/domain'
import { responseInspectionActionLabel } from './responseInspectionActionTemplates'

export function responseInspectionPolicyActionText(action: ResponseInspectionPolicyAction): string {
  return responseInspectionActionLabel(action)
}

export function responseInspectionPolicyScopeText(policy: Pick<ResponseInspectionPolicyOverview, 'scopeType'>): string {
  return policy.scopeType === 'provider' ? '供应商层' : '协议层'
}

export function responseInspectionPolicyProtocolText(protocolCode: string): string {
  if (protocolCode === 'openai') return 'OpenAI v1'
  if (protocolCode === 'anthropic') return 'Anthropic v1'
  if (protocolCode === 'gemini') return 'Gemini v1beta'
  return protocolCode || '-'
}

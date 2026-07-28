import { providerDisplayName } from '@/shared/providerDisplay'
import {
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  GPT_VENDOR_CODE
} from '@/shared/providerProtocol'
import type {
  ResponseInspectionPolicyProtocolCode,
  ResponseInspectionPolicyProviderOption
} from '@/types/domain'

export const DEFAULT_RESPONSE_INSPECTION_PROVIDER_CODES: Record<ResponseInspectionPolicyProtocolCode, string> = {
  openai: GPT_VENDOR_CODE,
  anthropic: ANTHROPIC_PROVIDER_CODE,
  gemini: GEMINI_PROVIDER_CODE
}

export interface ResponseInspectionProviderSelectOption {
  label: string
  value: string
}

export function defaultResponseInspectionProviderCode(
  options: ResponseInspectionPolicyProviderOption[],
  protocolCode: ResponseInspectionPolicyProtocolCode,
  optionsReady = false
): string {
  const matchingOptions = options.filter((option) => option.protocolCode === protocolCode)
  const localDefault = DEFAULT_RESPONSE_INSPECTION_PROVIDER_CODES[protocolCode]
  return matchingOptions.find((option) => option.code === localDefault)?.code
    ?? matchingOptions[0]?.code
    ?? (optionsReady ? '' : localDefault)
}

export function responseInspectionProviderSelectOptions(
  options: ResponseInspectionPolicyProviderOption[],
  protocolCode: ResponseInspectionPolicyProtocolCode,
  selected?: { code?: string; name?: string }
): ResponseInspectionProviderSelectOption[] {
  const result = options
    .filter((option) => option.protocolCode === protocolCode)
    .map((option) => ({ label: option.name, value: option.code }))
  const selectedCode = selected?.code?.trim()
  if (!selectedCode || result.some((option) => option.value === selectedCode)) return result
  const fallbackName = providerDisplayName(selectedCode)
  result.push({
    label: selected?.name?.trim() || (fallbackName === '未知供应商' ? selectedCode : fallbackName),
    value: selectedCode
  })
  return result
}

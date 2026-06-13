import type { ResponseInspectionPolicyAction } from '@/types/domain'

export interface ResponseInspectionMatchFormFields {
  outputTextIncludes: string
  outputTextExcludes: string
  errorCodes: string
  errorTypes: string
  errorMessageIncludes: string
  finishReasons: string
  jsonPathsExists: string
  rawTextIncludes: string
}

export type ResponseInspectionMatchFieldKey = keyof ResponseInspectionMatchFormFields

export type ResponseInspectionMatchPayload = Partial<Record<ResponseInspectionMatchFieldKey, string[]>>

export const responseInspectionActionValues: ResponseInspectionPolicyAction[] = [
  'observe',
  'drop_event',
  'retry_no_avoidance',
  'retry_next_account',
  'avoid_account_ttl',
  'avoid_upstream_bucket_ttl'
]

export const responseInspectionPositiveMatchFieldKeys = [
  'outputTextIncludes',
  'errorCodes',
  'errorTypes',
  'errorMessageIncludes',
  'finishReasons',
  'jsonPathsExists',
  'rawTextIncludes'
] as const satisfies readonly ResponseInspectionMatchFieldKey[]

export const responseInspectionMatchFieldDefinitions: readonly { key: ResponseInspectionMatchFieldKey; label: string }[] = [
  { key: 'outputTextIncludes', label: '输出文本包含' },
  { key: 'outputTextExcludes', label: '输出文本排除' },
  { key: 'errorCodes', label: 'error.code' },
  { key: 'errorTypes', label: 'error.type' },
  { key: 'errorMessageIncludes', label: '错误消息包含' },
  { key: 'finishReasons', label: '完成原因 / 状态' },
  { key: 'jsonPathsExists', label: 'JSON字段路径存在' },
  { key: 'rawTextIncludes', label: 'SSE 事件原文包含' }
]

const listSeparators = /[,，]/
const unsupportedListSeparators = /[;；\r\n]/

export function splitResponseInspectionList(value: unknown): string[] {
  if (Array.isArray(value)) {
    return uniqueTrimmedStrings(value)
  }
  if (typeof value !== 'string') {
    return value == null ? [] : uniqueTrimmedStrings([value])
  }
  return uniqueTrimmedStrings(value.split(listSeparators))
}

export function optionalResponseInspectionList(value: unknown): string[] | undefined {
  const items = splitResponseInspectionList(value)
  return items.length ? items : undefined
}

export function formatResponseInspectionList(values?: string[]): string {
  return values?.length ? values.join(', ') : ''
}

export function responseInspectionListText(values?: string[]): string {
  return values?.length ? values.join(', ') : '-'
}

export function hasUnsupportedResponseInspectionListSeparators(value: unknown): boolean {
  const values = Array.isArray(value) ? value : [value]
  return values.some((item) => typeof item === 'string' && unsupportedListSeparators.test(item))
}

export function responseInspectionMatchFieldEntries(
  form: ResponseInspectionMatchFormFields
): Array<{ key: ResponseInspectionMatchFieldKey; label: string; value: string }> {
  return responseInspectionMatchFieldDefinitions.map((field) => ({
    ...field,
    value: form[field.key]
  }))
}

export function validateResponseInspectionMatchFields(
  form: ResponseInspectionMatchFormFields,
  options: { messagePrefix?: string } = {}
): string | undefined {
  for (const field of responseInspectionMatchFieldEntries(form)) {
    const label = `${options.messagePrefix ?? ''}${field.label}`
    if (hasUnsupportedResponseInspectionListSeparators(field.value)) {
      return `${label}只能用英文逗号或中文逗号分隔`
    }
    const items = splitResponseInspectionList(field.value)
    if (items.length > 50) return `${label}不能超过 50 项`
    if (items.some((item) => item.length > 200)) return `${label}单项不能超过 200 个字符`
  }
  return undefined
}

export function hasPositiveResponseInspectionMatcher(form: ResponseInspectionMatchFormFields): boolean {
  return responseInspectionPositiveMatchFieldKeys.some((key) => splitResponseInspectionList(form[key]).length > 0)
}

export function buildResponseInspectionMatchPayload(form: ResponseInspectionMatchFormFields): ResponseInspectionMatchPayload {
  const payload: ResponseInspectionMatchPayload = {}
  for (const field of responseInspectionMatchFieldDefinitions) {
    const items = splitResponseInspectionList(form[field.key])
    if (items.length > 0) {
      payload[field.key] = items
    }
  }
  return payload
}

export function responseInspectionFieldSummary(label: string, value: unknown, limit = 2): string {
  const items = splitResponseInspectionList(value)
  return items.length ? `${label}: ${items.slice(0, limit).join(', ')}${items.length > limit ? ` 等 ${items.length} 项` : ''}` : ''
}

export function responseInspectionScopedListSummary(label: string, values?: string[], limit = 3): string {
  if (!values?.length) return ''
  return `${label}: ${values.slice(0, limit).join(', ')}${values.length > limit ? ` 等 ${values.length} 项` : ''}`
}

export function normalizeResponseInspectionAction(value: unknown): ResponseInspectionPolicyAction | undefined {
  return responseInspectionActionValues.includes(value as ResponseInspectionPolicyAction)
    ? value as ResponseInspectionPolicyAction
    : undefined
}

export function requireResponseInspectionAction(value: unknown): ResponseInspectionPolicyAction {
  const action = normalizeResponseInspectionAction(value)
  if (!action) throw new Error('响应检查处置动作无效')
  return action
}

function uniqueTrimmedStrings(values: unknown[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const text = String(value).trim()
    if (!text || seen.has(text)) continue
    seen.add(text)
    output.push(text)
  }
  return output
}

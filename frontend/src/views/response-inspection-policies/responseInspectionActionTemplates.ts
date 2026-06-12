import type { ResponseInspectionPolicyAction } from '@/types/domain'

export interface ResponseInspectionActionTemplate {
  action: ResponseInspectionPolicyAction
  label: string
  description: string
  runtimeAvoidance: boolean
}

export const responseInspectionActionTemplates: ResponseInspectionActionTemplate[] = [
  {
    action: 'observe',
    label: '先观察命中',
    description: '命中后只写日志，不拦截、不重试；适合先确认规则会命中哪些响应。',
    runtimeAvoidance: false
  },
  {
    action: 'drop_event',
    label: '只丢弃命中事件',
    description: '只对 SSE 生效：丢掉这一条命中事件，后续事件继续转发；JSON 响应命中时不使用。',
    runtimeAvoidance: false
  },
  {
    action: 'retry_no_avoidance',
    label: '重试但不避让账号',
    description: '命中后在可行时重新请求一次，但不避让当前账号；重试时仍可能选到它。',
    runtimeAvoidance: false
  },
  {
    action: 'retry_next_account',
    label: '本次重试避开当前账号',
    description: '命中后重试，并且这次重试不再选当前账号；后续请求仍可使用它。',
    runtimeAvoidance: false
  },
  {
    action: 'avoid_account_ttl',
    label: '短期避让当前账号',
    description: '命中后按系统临时不可调用策略短期避让当前账号，并在可行时重试。',
    runtimeAvoidance: true
  },
  {
    action: 'avoid_upstream_bucket_ttl',
    label: '短期避让上游桶',
    description: '命中后按系统临时不可调用策略避让同代理、同 baseUrl 或同供应商协议档案桶，并重试。',
    runtimeAvoidance: true
  }
]

export const responseInspectionActionTemplateOptions = responseInspectionActionTemplates.map((template) => ({
  label: template.label,
  value: template.action
}))

export function responseInspectionActionTemplateByAction(action: ResponseInspectionPolicyAction): ResponseInspectionActionTemplate {
  return responseInspectionActionTemplates.find((template) => template.action === action) ?? responseInspectionActionTemplates[0]
}

export function responseInspectionActionLabel(action: ResponseInspectionPolicyAction): string {
  return responseInspectionActionTemplateByAction(action).label
}

export function responseInspectionActionDescription(action: ResponseInspectionPolicyAction): string {
  return responseInspectionActionTemplateByAction(action).description
}

export function responseInspectionActionUsesRuntimeAvoidance(action: ResponseInspectionPolicyAction): boolean {
  return responseInspectionActionTemplateByAction(action).runtimeAvoidance
}

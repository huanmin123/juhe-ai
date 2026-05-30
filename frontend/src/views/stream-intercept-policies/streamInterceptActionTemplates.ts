import type { StreamInterceptPolicyAction } from '@/types/domain'

export interface StreamInterceptActionTemplate {
  action: StreamInterceptPolicyAction
  label: string
  description: string
  ttlRequired: boolean
}

export const defaultAvoidanceTtlSeconds = 300

export const streamInterceptActionTemplates: StreamInterceptActionTemplate[] = [
  {
    action: 'observe',
    label: '先观察命中',
    description: '只记录命中，不改变下游响应，适合新规则先看误杀范围。',
    ttlRequired: false
  },
  {
    action: 'drop_event',
    label: '只丢弃命中事件',
    description: '只移除单个污染事件，继续读取后续流，不触发重试。',
    ttlRequired: false
  },
  {
    action: 'fail_stream',
    label: '结束当前流',
    description: '把当前流改写为普通失败事件，不触发重试或账号避让。',
    ttlRequired: false
  },
  {
    action: 'retry_no_avoidance',
    label: '重试但不避让账号',
    description: '当前结果不可接受，但不改变账号候选；适合弱证据失败。',
    ttlRequired: false
  },
  {
    action: 'retry_next_account',
    label: '本次重试避开当前账号',
    description: '只在本次服务端重试中排除当前账号，不影响后续请求。',
    ttlRequired: false
  },
  {
    action: 'avoid_account_ttl',
    label: '短期避让当前账号',
    description: '当前账号短时间不再参与候选，并触发可行的重试。',
    ttlRequired: true
  },
  {
    action: 'avoid_upstream_bucket_ttl',
    label: '短期避让上游桶',
    description: '短时间避让同代理、baseUrl 或供应商桶，并触发可行的重试。',
    ttlRequired: true
  }
]

export const streamInterceptActionTemplateOptions = streamInterceptActionTemplates.map((template) => ({
  label: template.label,
  value: template.action
}))

export function streamInterceptActionTemplateByAction(action: StreamInterceptPolicyAction): StreamInterceptActionTemplate {
  return streamInterceptActionTemplates.find((template) => template.action === action) ?? streamInterceptActionTemplates[0]
}

export function streamInterceptActionLabel(action: StreamInterceptPolicyAction): string {
  return streamInterceptActionTemplateByAction(action).label
}

export function streamInterceptActionDescription(action: StreamInterceptPolicyAction): string {
  return streamInterceptActionTemplateByAction(action).description
}

export function streamInterceptActionUsesTtl(action: StreamInterceptPolicyAction): boolean {
  return streamInterceptActionTemplateByAction(action).ttlRequired
}

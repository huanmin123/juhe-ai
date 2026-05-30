import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'

export const streamInterceptExecutionModeOptions = [
  { label: '拦截', value: 'intercept' },
  { label: '试运行', value: 'dry_run' }
]

export const streamInterceptDataHandlingOptions = [
  { label: '丢弃命中事件', value: 'discard_event' },
  { label: '丢弃当前流', value: 'discard_stream' },
  { label: '替换为失败事件', value: 'replace_with_failure' }
]

export const streamInterceptAccountSwitchOptions = [
  { label: '不切号', value: 'none' },
  { label: '本次请求切下一个账号', value: 'request_next_account' },
  { label: '切号并短期避让当前账号', value: 'avoid_account_ttl' },
  { label: '切号并短期避让上游桶', value: 'avoid_upstream_bucket_ttl' }
]

export const streamInterceptAccountStateOptions = [
  { label: '不修改', value: 'none' },
  { label: '仅运行态避让', value: 'runtime_avoidance' }
]

export function createBlankAccountStreamInterceptRule(priority = 100): AccountStreamInterceptRuleForm {
  return {
    enabled: true,
    name: '中转流污染拦截',
    priority,
    executionMode: 'intercept',
    eventTypes: '',
    dataTypes: '',
    errorCodes: '',
    errorTypes: '',
    textIncludes: '',
    textExcludes: '',
    jsonPathsExists: '',
    dataHandling: 'discard_stream',
    retryEnabled: true,
    accountSwitch: 'avoid_account_ttl',
    accountState: 'runtime_avoidance',
    avoidanceTtlSeconds: 300,
    notes: ''
  }
}

export function nextStreamInterceptRulePriority(rules: AccountStreamInterceptRuleForm[]): number {
  const max = Math.max(0, ...rules.map((rule) => Number(rule.priority ?? 0)).filter(Number.isFinite))
  return Math.min(9999, max + 10 || 100)
}

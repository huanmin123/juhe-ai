import type { GroupSchedulingPolicy, GroupSummary, GroupType } from '@/types/domain'

// 普通分组可选调度策略（普通分组可选调度策略设计 §2.2）：策略列 NULL 等价于
// 两项都取默认值；非空对象必须同时包含两个键，缺键、未知键、越界或非整数一律
// 无效，不做静默补默认。
export const defaultGroupSchedulingPolicy: Required<GroupSchedulingPolicy> = {
  defaultSoftConcurrency: -1,
  clientIpConcurrencyLimit: 0
}

export const schedulingPolicyValueMax = 1_000_000

export function cloneGroupSchedulingPolicy(source?: GroupSchedulingPolicy | null): Required<GroupSchedulingPolicy> {
  if (source === undefined || source === null) {
    return { ...defaultGroupSchedulingPolicy }
  }
  if (typeof source !== 'object' || Array.isArray(source)) {
    throw new Error('分组调度策略无效')
  }
  const input = source as Record<string, unknown>
  assertGroupSchedulingPolicyKeys(input)
  return {
    defaultSoftConcurrency: normalizeGroupSchedulingInteger(input.defaultSoftConcurrency, 'defaultSoftConcurrency', -1, schedulingPolicyValueMax, { forbidden: [0] }),
    clientIpConcurrencyLimit: normalizeGroupSchedulingInteger(input.clientIpConcurrencyLimit, 'clientIpConcurrencyLimit', 0, schedulingPolicyValueMax)
  }
}

export function groupTypeText(_groupType?: GroupType): string {
  return '个人'
}

export function groupTypeColor(_groupType?: GroupType): string {
  return 'blue'
}

// groupKind 与 groupType 正交：超级分组只拥有成员关系，不能渲染成「个人」类型标签
// （超级分组设计 §3.1）。
export function groupKindText(group: Pick<GroupSummary, 'groupType' | 'groupKind'>): string {
  return group.groupKind === 'super' ? '超级分组' : groupTypeText(group.groupType)
}

export function groupKindColor(group: Pick<GroupSummary, 'groupType' | 'groupKind'>): string {
  return group.groupKind === 'super' ? 'geekblue' : groupTypeColor(group.groupType)
}

// 摘要文案按契约（普通分组可选调度策略设计 §2.3）：两项都默认显示「未启用可选
// 调度策略」；阈值启用显示「单账户排队阈值 N」；IP 限制启用显示「单 IP 并发 N（超限拒绝）」。
export function groupPolicySummary(group: Pick<GroupSummary, 'groupType' | 'groupKind'> & Partial<Pick<GroupSummary, 'schedulingPolicy'>>): string {
  if (group.groupKind === 'super') {
    return '超级分组按成员顺序展开成员分组的账号候选'
  }
  if (!group.schedulingPolicy) return '未启用可选调度策略'
  let policy: Required<GroupSchedulingPolicy>
  try {
    policy = cloneGroupSchedulingPolicy(group.schedulingPolicy)
  } catch {
    return '可选调度策略数据异常，请编辑后重新保存'
  }
  const parts: string[] = []
  if (policy.defaultSoftConcurrency > 0) {
    parts.push(`单账户排队阈值 ${policy.defaultSoftConcurrency}`)
  }
  if (policy.clientIpConcurrencyLimit > 0) {
    parts.push(`单 IP 并发 ${policy.clientIpConcurrencyLimit}（超限拒绝）`)
  }
  return parts.length ? parts.join('，') : '未启用可选调度策略'
}

function assertGroupSchedulingPolicyKeys(input: Record<string, unknown>): void {
  const allowed = new Set<string>(Object.keys(defaultGroupSchedulingPolicy))
  const unknownKeys = Object.keys(input).filter((key) => !allowed.has(key))
  if (unknownKeys.length) {
    throw new Error(`分组调度策略包含未知字段：${unknownKeys.join('、')}`)
  }
  const missingKeys = [...allowed].filter((key) => input[key] === undefined || input[key] === null)
  if (missingKeys.length) {
    throw new Error(`分组调度策略缺少字段：${missingKeys.join('、')}`)
  }
}

function normalizeGroupSchedulingInteger(
  value: unknown,
  key: string,
  min: number,
  max: number,
  options: { forbidden?: number[] } = {}
): number {
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`分组调度策略 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`分组调度策略 ${key} 必须在 ${min}-${max} 之间`)
  }
  if (options.forbidden?.includes(value)) {
    throw new Error(`分组调度策略 ${key} 不能为 ${value}`)
  }
  return value
}

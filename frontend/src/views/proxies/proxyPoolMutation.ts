import type { ProxyPoolCreatePayload, ProxyPoolMemberPayload, ProxyPoolPatchPayload } from '@/api/contracts'
import type { ProxyPoolDetail, ProxyPoolMemberDetail, ProxyPoolStrategy } from '@/types/domain'

// 代理池编辑器表单状态与载荷构造（代理池设计 §8.1/§9.1）：
// - PATCH 必须带 expectedUpdatedAt；members 出现即完整替换成员集合；
// - 权重 1..100；priority 由行顺序推导（0..n-1），由后端归一；
// - 与代理档案编辑一致采用基线差量：无变化不发请求。

export const POOL_MEMBER_WEIGHT_MIN = 1
export const POOL_MEMBER_WEIGHT_MAX = 100

export interface ProxyPoolMemberFormRow {
  proxyProfileId: string
  name: string
  type: string
  enabled: boolean
  testStatus: string
  lastTestedAt?: string
  latencyMs?: number
  poolHealthStatus: string
  poolHealthFailureStreak: number
  poolHealthCheckedAt?: string
  weight: number
  memberEnabled: boolean
  leaseCount: number
  assignable: boolean
}

export interface ProxyPoolFormState {
  name: string
  description: string
  strategy: ProxyPoolStrategy
  enabled: boolean
  members: ProxyPoolMemberFormRow[]
}

export function defaultProxyPoolForm(): ProxyPoolFormState {
  return {
    name: '',
    description: '',
    strategy: 'round_robin',
    enabled: true,
    members: []
  }
}

export function proxyPoolFormFromDetail(detail: ProxyPoolDetail): ProxyPoolFormState {
  return {
    name: detail.name,
    description: detail.description ?? '',
    strategy: detail.strategy,
    enabled: detail.enabled,
    members: (detail.members ?? []).map(memberRowFromDetail)
  }
}

export function memberRowFromDetail(member: ProxyPoolMemberDetail): ProxyPoolMemberFormRow {
  return {
    proxyProfileId: member.proxyProfileId,
    name: member.name,
    type: member.type,
    enabled: member.enabled,
    testStatus: member.testStatus,
    lastTestedAt: member.lastTestedAt,
    latencyMs: member.latencyMs,
    poolHealthStatus: member.poolHealthStatus,
    poolHealthFailureStreak: member.poolHealthFailureStreak,
    poolHealthCheckedAt: member.poolHealthCheckedAt,
    weight: clampMemberWeight(member.weight),
    memberEnabled: member.memberEnabled,
    leaseCount: member.leaseCount,
    assignable: member.assignable
  }
}

export function clampMemberWeight(value: number | undefined): number {
  const normalized = Math.trunc(Number(value ?? POOL_MEMBER_WEIGHT_MIN))
  if (!Number.isFinite(normalized)) return POOL_MEMBER_WEIGHT_MIN
  return Math.min(POOL_MEMBER_WEIGHT_MAX, Math.max(POOL_MEMBER_WEIGHT_MIN, normalized))
}

export function validateProxyPoolForm(form: ProxyPoolFormState): string | undefined {
  if (!form.name.trim()) return '请填写代理池名称'
  if (form.members.length > 50) return '代理池成员数量超出上限（最多 50 个）'
  const seen = new Set<string>()
  for (const member of form.members) {
    if (!member.proxyProfileId) return '代理池成员无效，请移除后重新添加'
    if (seen.has(member.proxyProfileId)) return '代理池成员重复，请移除重复行'
    seen.add(member.proxyProfileId)
  }
  const invalidWeight = form.members.findIndex((member) => !Number.isInteger(member.weight)
    || member.weight < POOL_MEMBER_WEIGHT_MIN
    || member.weight > POOL_MEMBER_WEIGHT_MAX)
  if (invalidWeight >= 0) return `第 ${invalidWeight + 1} 个成员的权重必须是 ${POOL_MEMBER_WEIGHT_MIN}..${POOL_MEMBER_WEIGHT_MAX} 的整数`
  return undefined
}

export function buildProxyPoolCreatePayload(form: ProxyPoolFormState): ProxyPoolCreatePayload {
  return {
    name: form.name.trim(),
    ...(form.description.trim() ? { description: form.description.trim() } : {}),
    strategy: form.strategy,
    enabled: form.enabled,
    members: memberPayloads(form)
  }
}

export function buildProxyPoolPatchPayload(
  baseline: ProxyPoolFormState,
  form: ProxyPoolFormState,
  expectedUpdatedAt: string
): ProxyPoolPatchPayload {
  const patch: ProxyPoolPatchPayload = { expectedUpdatedAt }
  if (form.name.trim() !== baseline.name.trim()) patch.name = form.name.trim()
  if (normalizedNullableText(baseline.description) !== normalizedNullableText(form.description)) {
    patch.description = normalizedNullableText(form.description)
  }
  if (form.strategy !== baseline.strategy) patch.strategy = form.strategy
  if (form.enabled !== baseline.enabled) patch.enabled = form.enabled
  if (!sameMembers(baseline.members, form.members)) patch.members = memberPayloads(form)
  return patch
}

export function hasProxyPoolPatchChanges(baseline: ProxyPoolFormState, form: ProxyPoolFormState): boolean {
  return Object.keys(buildProxyPoolPatchPayload(baseline, form, 'x')).filter((key) => key !== 'expectedUpdatedAt').length > 0
}

function normalizedNullableText(value: string): string | null {
  const text = value.trim()
  return text || null
}

export function sameMembers(
  left: ProxyPoolMemberFormRow[],
  right: ProxyPoolMemberFormRow[]
): boolean {
  if (left.length !== right.length) return false
  return left.every((item, index) => {
    const other = right[index]
    return item.proxyProfileId === other.proxyProfileId
      && clampMemberWeight(item.weight) === clampMemberWeight(other.weight)
      && item.memberEnabled === other.memberEnabled
  })
}

function memberPayloads(form: ProxyPoolFormState): ProxyPoolMemberPayload[] {
  return form.members.map((member, index) => ({
    proxyProfileId: member.proxyProfileId,
    weight: clampMemberWeight(member.weight),
    memberEnabled: member.memberEnabled
  }))
}

/** 行顺序即 priority（§8.1 priority 归一由后端完成）：上移/下移只改变数组顺序。 */
export function movePoolMember(members: ProxyPoolMemberFormRow[], index: number, offset: -1 | 1): ProxyPoolMemberFormRow[] {
  const target = index + offset
  if (index < 0 || index >= members.length || target < 0 || target >= members.length) return members
  const next = [...members]
  const [moved] = next.splice(index, 1)
  next.splice(target, 0, moved)
  return next
}

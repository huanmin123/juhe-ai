import { proxySelectOptionLabel, rememberProxyLabel, rememberProxyPoolLabel } from '@/shared/proxyLabelCache'
import type { AccountListItem, ProxyProfileOptionSummary } from '@/types/domain'

export function accountProxyDisplay(
  account: AccountListItem,
  fallback?: ProxyProfileOptionSummary
): ProxyProfileOptionSummary | undefined {
  if (account.proxyProfileName !== undefined || account.proxyProfileType !== undefined || account.proxyProfileEnabled !== undefined) {
    return {
      id: account.proxyProfileId ?? '',
      name: account.proxyProfileName ?? '',
      type: account.proxyProfileType ?? '',
      enabled: account.proxyProfileEnabled ?? true
    }
  }
  if (account.proxyProfileUnavailable) return undefined
  return fallback
}

/** 账户列表代理展示四态（代理池设计 §8.5/§9.3）：normal / pending（待分配或待切换）/ error（无可用成员）。 */
export type AccountProxyPresentationTone = 'normal' | 'pending' | 'error'

export interface AccountProxyPresentation {
  /** 'profile' = 直接代理（保持既有展示）；'pool' = 代理池绑定四态。 */
  kind: 'profile' | 'pool'
  /** 列表/移动端 tag 主文案；undefined 表示不使用代理（直连）。 */
  text?: string
  tooltip: string
  tone: AccountProxyPresentationTone
  unavailable: boolean
  /** 配置了代理但展示字段缺失（已删除或当前不可见）：保留原「代理已配置」橙色提示。 */
  unknown?: boolean
}

const POOL_NO_MEMBER_TEXT = '无可用成员'

/**
 * 池绑定展示规则（§9.3）：
 * - 有租约成员且成员可用：`池名 · 成员名`（normal）；
 * - 无租约且池有可分配成员：`池名 · 待分配`（pending，proxyProfileUnavailable=false）；
 * - 有租约但成员不可用且仍有可分配：`池名 · 当前成员不可用（待切换）`（pending）；
 * - 池停用或无任何可分配成员（或成员不可用且无替代）：`池名 · 无可用成员`（error，标红）。
 * 用 proxyPoolAssignableMemberCount + 租约成员可用性区分，不只靠 proxyProfileUnavailable。
 */
export function accountProxyPresentation(account: AccountListItem, fallback?: ProxyProfileOptionSummary): AccountProxyPresentation {
  if (account.proxyTargetType === 'pool' || (!account.proxyProfileId && account.proxyPoolId)) {
    return poolPresentation(account)
  }
  const display = accountProxyDisplay(account, fallback)
  const unavailable = account.proxyProfileUnavailable === true || display?.enabled === false
  if (!account.proxyProfileId && !display) {
    return { kind: 'profile', tooltip: '', tone: 'normal', unavailable: false }
  }
  const known = Boolean(display)
  return {
    kind: 'profile',
    text: display?.name || '代理已配置',
    tooltip: profileTooltip(account, display),
    tone: unavailable ? 'error' : 'normal',
    unavailable,
    unknown: !known
  }
}

function poolPresentation(account: AccountListItem): AccountProxyPresentation {
  const poolName = account.proxyPoolName ?? account.proxyPoolId ?? '代理池'
  const assignable = account.proxyPoolAssignableMemberCount ?? 0
  const poolDisabled = account.proxyPoolEnabled === false
  const hasMember = Boolean(account.proxyMemberProfileId)
  const memberUnavailable = hasMember
    && (account.proxyMemberProfileEnabled === false || account.proxyProfileUnavailable === true)
  const unavailable = account.proxyProfileUnavailable === true || poolDisabled
  const baseTooltip = account.proxyProfileErrorMessage?.trim()

  // 无租约且池有可分配成员：待分配（下次请求解析时分配）。
  if (!hasMember && !unavailable && assignable > 0) {
    return {
      kind: 'pool',
      text: `${poolName} · 待分配`,
      tooltip: baseTooltip || '代理池尚未分配成员：下次请求解析时自动分配。',
      tone: 'pending',
      unavailable: false
    }
  }
  // 有租约成员但成员不可用，池内仍有可分配成员：待切换（下次请求重新分配）。
  if (hasMember && memberUnavailable && assignable > 0 && !poolDisabled) {
    const memberName = account.proxyMemberProfileName ?? account.proxyMemberProfileId ?? ''
    return {
      kind: 'pool',
      text: `${poolName} · 当前成员不可用（待切换）`,
      tooltip: baseTooltip || `当前成员 ${memberName} 不可用：下次请求会在池内重新分配（还有 ${assignable} 个可分配成员）。`,
      tone: 'pending',
      unavailable: true
    }
  }
  // 池停用、无任何可分配成员，或成员不可用且无替代：无可用成员（标红）。
  if (unavailable || memberUnavailable) {
    return {
      kind: 'pool',
      text: `${poolName} · ${POOL_NO_MEMBER_TEXT}`,
      tooltip: baseTooltip || (poolDisabled
        ? '代理池已停用：请启用代理池或改绑其他代理。'
        : '代理池没有可分配成员：请先在代理池中添加并启用成员。'),
      tone: 'error',
      unavailable: true
    }
  }
  // 正常：池名 · 成员名（成员健康；即使其余成员不可分配，当前租约仍有效）。
  const memberName = account.proxyMemberProfileName ?? account.proxyMemberProfileId ?? ''
  return {
    kind: 'pool',
    text: memberName ? `${poolName} · ${memberName}` : poolName,
    tooltip: baseTooltip || memberTooltip(account, memberName, assignable),
    tone: 'normal',
    unavailable: false
  }
}

function memberTooltip(account: AccountListItem, memberName: string, assignable: number): string {
  const parts = [
    memberName && account.proxyMemberProfileType
      ? `${memberName}（${account.proxyMemberProfileType}）`
      : memberName || '',
    `代理池：${account.proxyPoolName ?? account.proxyPoolId ?? ''}，剩余可分配成员 ${assignable} 个`
  ].filter(Boolean)
  return parts.join('\n')
}

function profileTooltip(account: AccountListItem, display?: ProxyProfileOptionSummary): string {
  if (account.proxyProfileErrorMessage) return account.proxyProfileErrorMessage
  if (account.proxyProfileUnavailable) return '代理不可用，请到代理管理确认配置'
  if (display?.enabled === false) return '代理已停用，请启用代理或更换账户代理'
  if (display) return `${display.name}（${display.type}）`
  return '代理配置不存在或当前不可见'
}

/**
 * 打开编辑弹窗前用列表行的代理展示字段回填标签缓存（kind-aware）。
 *
 * 代理候选按需加载（页面初始化不预取），冷会话直接打开编辑时下拉没有候选：
 * - 直接代理按候选同款标签格式（名称（类型，已停用））写入 proxies 缓存；
 * - 池绑定按池选项格式（池名（N 个成员，M 个可分配））写入 proxyPools 独立缓存，
 *   ProxySelect 回填 `pool:<id>` 选中项时以池名为稳定展示，不把复合值原样显示。
 * 名称缺失（代理/池已删除或当前不可见）不写入，避免把空名称拼成括号后缀。
 */
export function rememberAccountProxyLabel(account: AccountListItem): void {
  const display = accountProxyDisplay(account)
  if (display?.id && display.name.trim()) {
    rememberProxyLabel(display.id, proxySelectOptionLabel(display))
  }
  // 池绑定回填池名（§9.2）：列表行无 memberCount，先回填纯池名；下拉候选加载后
  // 由 proxyPoolOptionLabel 的完整格式（N 个成员，M 个可分配）替换。
  if (account.proxyPoolId && account.proxyPoolName?.trim()) {
    rememberProxyPoolLabel(account.proxyPoolId, account.proxyPoolName.trim())
  }
}

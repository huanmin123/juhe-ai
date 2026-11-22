import type { GroupOptionSummary } from '@/types/domain'

export interface ClaimTargetGroupOption {
  label: string
  value: string
}

export interface ClaimTargetGroupOptionsQuery {
  purpose: 'account'
  manageableOnly: true
  groupKind: 'standard'
  limit: number
}

/**
 * 领取/换绑目标分组候选的 options 查询参数。purpose=account 必带：缺省
 * purpose 会被后端归一为 select，只投影 {id,name}（无 groupKind/enabled/
 * accessType），候选过滤字段全部缺失、恒为空；manageableOnly 让 limit 名额
 * 不被授权分组/超级分组占用，groupKind=standard 由后端先行过滤（前端
 * claimTargetGroupOptions 保留同口径过滤作防御）。
 */
export function claimTargetGroupOptionsQuery(): ClaimTargetGroupOptionsQuery {
  return { purpose: 'account', manageableOnly: true, groupKind: 'standard', limit: 50 }
}

/**
 * 领取/换绑账户授权时的目标分组候选：只保留接收方自己的、已启用
 * （enabled !== false）、非授权绑入（accessType !== 'authorized'）的普通分组
 * （groupKind === 'standard'）。授权绑入的分组归属原授权方，不能再作为
 * 领取目标；超级分组不是账户绑入目标。缺过滤字段的行（如 select purpose
 * 投影）一律丢弃。
 */
export function claimTargetGroupOptions(groups: GroupOptionSummary[]): ClaimTargetGroupOption[] {
  return groups
    .filter((group) => group.accessType !== 'authorized' && group.enabled !== false && group.groupKind === 'standard')
    .map((group) => ({ label: group.name, value: group.id }))
}

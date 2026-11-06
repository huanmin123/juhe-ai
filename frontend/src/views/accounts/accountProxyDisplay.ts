import { proxySelectOptionLabel, rememberProxyLabel } from '@/shared/proxyLabelCache'
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

/**
 * 打开编辑弹窗前用列表行的代理展示字段回填标签缓存。
 *
 * 代理候选按需加载（页面初始化不预取），冷会话直接打开编辑时下拉没有候选，
 * `CachedSelect` 只能用裸 proxy id 占位；列表行已携带 `proxyProfileName/Type/Enabled`，
 * 按与候选相同的标签格式（名称（类型，已停用））写入缓存后，编辑框以名称为稳定展示。
 * 名称缺失（代理已删除或当前不可见）不写入，避免把空名称拼成「（类型）」。
 */
export function rememberAccountProxyLabel(account: AccountListItem): void {
  const display = accountProxyDisplay(account)
  if (!display?.id || !display.name.trim()) return
  rememberProxyLabel(display.id, proxySelectOptionLabel(display))
}

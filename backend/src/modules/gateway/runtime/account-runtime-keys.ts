export type SuppressibleGatewayAccount = {
  id: string
  accessType?: 'owner' | 'authorized'
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  bindingSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  boundGroupId?: string
  accountAuthorizationId?: string
  credentialSourceAccountId?: string
}

export interface GatewayAccountRuntimeClearTarget {
  accountId: string
  authorizedBinding?: {
    systemAccountId?: string
    groupId?: string
    accountAuthorizationId?: string
  }
}

export function gatewayAccountRuntimeKey(account: SuppressibleGatewayAccount | string): string {
  if (typeof account === 'string') {
    return account
  }
  if (account.accountAccessType === 'account_authorized' || account.accessType === 'authorized') {
    const systemAccountId = account.bindingSystemAccountId ?? ''
    const groupId = account.boundGroupId ?? ''
    const authorizationId = account.accountAuthorizationId ?? ''
    if (systemAccountId && groupId && authorizationId) {
      return `${account.id}:authorized:${systemAccountId}:${groupId}:${authorizationId}`
    }
    throw new Error('授权账户运行态键缺少绑定上下文')
  }
  return account.id
}

export function gatewayAccountId(account: SuppressibleGatewayAccount | string): string {
  return typeof account === 'string' ? account : account.id
}

export function runtimeAccountIdFromKey(runtimeKey: string): string {
  return runtimeKey.split(':', 1)[0] || runtimeKey
}

export function gatewayAccountRuntimeClearKeys(account: GatewayAccountRuntimeClearTarget | SuppressibleGatewayAccount | string): string[] {
  if (typeof account === 'string') {
    return account.trim() ? [account.trim()] : []
  }
  const isClearTarget = 'accountId' in account
  const accountId = (isClearTarget ? account.accountId : account.id)?.trim()
  if (!accountId) {
    return []
  }
  const keys = new Set<string>([accountId])
  const authorizedBinding = isClearTarget
      ? account.authorizedBinding
      : account.accountAccessType === 'account_authorized' || account.accessType === 'authorized'
        ? {
          systemAccountId: account.bindingSystemAccountId,
          groupId: account.boundGroupId,
          accountAuthorizationId: account.accountAuthorizationId
        }
      : undefined
  const systemAccountId = authorizedBinding?.systemAccountId
  const groupId = authorizedBinding?.groupId
  const authorizationId = authorizedBinding?.accountAuthorizationId
  if (systemAccountId?.trim() && groupId?.trim() && authorizationId?.trim()) {
    keys.add(`${accountId}:authorized:${systemAccountId.trim()}:${groupId.trim()}:${authorizationId.trim()}`)
  }
  return [...keys]
}

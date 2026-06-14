import type { AccountImportItem, AccountImportProxyItem, AccountImportSummary } from './account-import.service.js'

type AccountImportPlanAction = 'create' | 'reuse' | 'skip' | 'failed'

export interface AccountImportGroupCreatePlan {
  providerCode: string
  providerProtocolProfileId: string
  name: string
}

export type AccountImportGroupCreateMap = Map<string, AccountImportGroupCreatePlan>

export interface AccountImportDuplicateCandidate {
  source: {
    index: number
    name: string
  }
  item: {
    action: AccountImportPlanAction
    messages: string[]
  }
}

export function markDuplicateAccountImportItems(accounts: AccountImportDuplicateCandidate[], skipDuplicates: boolean): void {
  const seenName = new Map<string, number>()
  for (const account of accounts) {
    if (account.item.action === 'failed') continue
    const nameKey = account.source.name.trim().toLowerCase()
    const duplicatedByName = seenName.get(nameKey)
    if (duplicatedByName) {
      account.item.action = skipDuplicates ? 'skip' : 'failed'
      account.item.messages.push(`与第 ${duplicatedByName} 条账户名称重复`)
    } else {
      seenName.set(nameKey, account.source.index)
    }
  }
}

export function buildAccountImportSummary(
  accounts: AccountImportItem[],
  proxies: AccountImportProxyItem[],
  groupsToCreate: ReadonlyMap<string, AccountImportGroupCreatePlan>
): AccountImportSummary {
  const groupRefs = new Set<string>()
  for (const item of accounts) {
    if (item.action === 'failed') continue
    if (item.groupId) {
      groupRefs.add(`id:${item.groupId}`)
    } else if (item.groupName) {
      groupRefs.add(accountImportGroupKey(item.providerProtocolProfileId ?? '', item.groupName))
    }
  }
  return {
    accounts: {
      total: accounts.length,
      create: accounts.filter((item) => item.action === 'create').length,
      skip: accounts.filter((item) => item.action === 'skip').length,
      failed: accounts.filter((item) => item.action === 'failed').length
    },
    proxies: {
      total: proxies.length,
      create: proxies.filter((item) => item.action === 'create').length,
      reuse: proxies.filter((item) => item.action === 'reuse').length,
      skip: proxies.filter((item) => item.action === 'skip').length,
      failed: proxies.filter((item) => item.action === 'failed').length
    },
    groups: {
      create: groupsToCreate.size,
      reuse: Math.max(0, groupRefs.size - groupsToCreate.size),
      failed: 0
    }
  }
}

export function accountImportGroupKey(providerProtocolProfileId: string, name: string): string {
  return `${providerProtocolProfileId.trim().toLowerCase()}:${name.trim().toLowerCase()}`
}

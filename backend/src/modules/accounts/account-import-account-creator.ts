import { type AccessScope } from '../../storage/access-scope.js'
import { createAccount } from '../../storage/repositories.js'
import { errorMessage } from './account-import-field-parser.js'
import { accountImportGroupKey } from './account-import-plan.js'
import {
  buildAccountImportCreatePayload,
  type AccountImportCreatePayloadAccount
} from './account-import-account-payload.js'
import type { AccountImportItem, AccountImportSummary } from './account-import.service.js'

export interface AccountImportAccountCreatePlan {
  options: {
    skipDuplicates: boolean
  }
  result: {
    summary: AccountImportSummary
  }
  accounts: AccountImportAccountCreatePlanItem[]
  groupIdsByKey: Map<string, string>
}

export interface AccountImportAccountCreatePlanItem {
  source: AccountImportCreatePayloadAccount & {
    groupId?: string
    groupName?: string
    proxyRef?: string
  }
  item: AccountImportItem
  groupId?: string
  proxyProfileId?: string
}

export function createPlannedImportAccounts(plan: AccountImportAccountCreatePlan, access: AccessScope): void {
  for (const account of plan.accounts) {
    if (account.item.action !== 'create') continue
    const groupId = account.groupId ?? groupIdForImportAccount(plan, account.source)
    const proxyProfileId = account.proxyProfileId
    try {
      const accountInput = buildAccountImportCreatePayload(account.source, {
        groupId,
        proxyProfileId
      })
      const created = createAccount(accountInput, access)
      account.item.accountId = created.id
      account.item.messages = [created.status === 'pending_test' ? '已创建账户，需测试通过后参与调度' : '已创建账户']
    } catch (error) {
      if (isDuplicateAccountError(error) && plan.options.skipDuplicates) {
        account.item.action = 'skip'
        account.item.messages = [errorMessage(error)]
        plan.result.summary.accounts.create -= 1
        plan.result.summary.accounts.skip += 1
        continue
      }
      account.item.action = 'failed'
      account.item.messages = [errorMessage(error)]
      plan.result.summary.accounts.create -= 1
      plan.result.summary.accounts.failed += 1
    }
  }
}

function groupIdForImportAccount(
  plan: AccountImportAccountCreatePlan,
  account: AccountImportAccountCreatePlanItem['source']
): string | undefined {
  if (account.groupId) return account.groupId
  if (!account.groupName) return undefined
  if (!account.providerProtocolProfileId) return undefined
  return plan.groupIdsByKey.get(accountImportGroupKey(account.providerProtocolProfileId, account.groupName))
}

function isDuplicateAccountError(error: unknown): boolean {
  return error instanceof Error && error.message.includes('账户名称已存在')
}

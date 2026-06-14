import { type AccessScope } from '../../storage/access-scope.js'
import {
  createPlannedImportGroups,
  createPlannedImportProxies,
  failAccountsWithUnresolvedImportProxy
} from './account-import-resource-creator.js'
import { createPlannedImportAccounts } from './account-import-account-creator.js'
import { type AccountImportGroupCreateMap } from './account-import-plan.js'
import { type AccountImportAccountPlan } from './account-import-account-plan.js'
import { type AccountImportProxyPlan } from './account-import-proxy-plan.js'
import { type AccountImportResult } from './account-import.service.js'

export interface AccountImportExecutionPlan {
  result: AccountImportResult
  accounts: AccountImportAccountPlan[]
  proxies: AccountImportProxyPlan[]
  groupIdsByKey: Map<string, string>
  groupNamesToCreate: AccountImportGroupCreateMap
  options: {
    skipDuplicates: boolean
  }
}

export function executeAccountImportPlan(plan: AccountImportExecutionPlan, access: AccessScope): AccountImportResult {
  const createdProxyIds = createPlannedImportProxies(plan, access)
  for (const [ref, proxyId] of createdProxyIds) {
    for (const account of plan.accounts) {
      if (account.source.proxyRef === ref && !account.proxyProfileId) {
        account.proxyProfileId = proxyId
      }
    }
  }
  failAccountsWithUnresolvedImportProxy(plan)

  createPlannedImportGroups(plan, access)
  createPlannedImportAccounts(plan, access)

  plan.result.imported = true
  plan.result.canImport = false
  return plan.result
}

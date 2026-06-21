import { type AccountType } from '../../domain/types.js'
import { type AccessScope } from '../../storage/access-scope.js'
import { listProviders } from '../../storage/repositories.js'
import {
  accountImportGroupKey,
  buildAccountImportSummary,
  markDuplicateAccountImportItems,
  type AccountImportGroupCreateMap,
  type AccountImportGroupCreatePlan
} from './account-import-plan.js'
import {
  importTargetSystemAccountId,
  type AccountImportResourceContext
} from './account-import-resource-resolver.js'
import { type AccountImportProviderContext } from './account-import-provider-resolver.js'
import { planImportProxies, type AccountImportProxyPlan } from './account-import-proxy-plan.js'
import { planImportAccount, type AccountImportAccountPlan } from './account-import-account-plan.js'
import { validateAccountImportRoot } from './account-import-root-validation.js'
import { executeAccountImportPlan } from './account-import-executor.js'

export const accountImportProtocolType = 'juhe-ai-account-import'
export const accountImportProtocolVersion = 1
export const accountImportMaxAccounts = 50
export const accountImportMaxProxies = 20

type ImportAction = 'create' | 'reuse' | 'skip' | 'failed'

export interface AccountImportOptions {
  createMissingGroups?: boolean
  createMissingProxies?: boolean
  skipDuplicates?: boolean
}
export interface AccountImportSummary {
  accounts: {
    total: number
    create: number
    skip: number
    failed: number
  }
  proxies: {
    total: number
    create: number
    reuse: number
    skip: number
    failed: number
  }
  groups: {
    create: number
    reuse: number
    failed: number
  }
}

export interface AccountImportItem {
  index: number
  ref?: string
  name?: string
  providerCode?: string
  providerProtocolProfileId?: string
  connectionType?: string
  protocolCode?: string
  protocolVersion?: string
  accountType?: AccountType
  groupName?: string
  groupId?: string
  proxyRef?: string
  action: ImportAction
  messages: string[]
  warnings: string[]
  accountId?: string
}

export interface AccountImportProxyItem {
  index: number
  ref?: string
  name?: string
  action: ImportAction
  messages: string[]
  warnings: string[]
  proxyProfileId?: string
}

export interface AccountImportResult {
  type: typeof accountImportProtocolType
  version: typeof accountImportProtocolVersion
  mode: 'preview' | 'import'
  canImport: boolean
  imported: boolean
  summary: AccountImportSummary
  accounts: AccountImportItem[]
  proxies: AccountImportProxyItem[]
  messages: string[]
}

interface ImportPlan {
  result: AccountImportResult
  accounts: AccountImportAccountPlan[]
  proxies: AccountImportProxyPlan[]
  groupIdsByKey: Map<string, string>
  groupNamesToCreate: AccountImportGroupCreateMap
  options: Required<AccountImportOptions>
  access?: AccessScope
}

interface ImportContext extends AccountImportResourceContext, AccountImportProviderContext {
  targetSystemAccountId?: string
}

export function previewAccountImport(data: unknown, options: AccountImportOptions = {}, access?: AccessScope): AccountImportResult {
  return buildImportPlan(data, options, access).result
}

export function executeAccountImport(data: unknown, options: AccountImportOptions = {}, access: AccessScope): AccountImportResult {
  const plan = buildImportPlan(data, options, access)
  const result = plan.result
  result.mode = 'import'
  if (!result.canImport) {
    return result
  }

  return executeAccountImportPlan(plan, access)
}

export function defaultAccountImportOptions(options: AccountImportOptions = {}): Required<AccountImportOptions> {
  return {
    createMissingGroups: options.createMissingGroups !== false,
    createMissingProxies: options.createMissingProxies !== false,
    skipDuplicates: options.skipDuplicates !== false
  }
}

function buildImportPlan(data: unknown, rawOptions: AccountImportOptions, access?: AccessScope): ImportPlan {
  const options = defaultAccountImportOptions(rawOptions)
  const providers = listProviders()
  const context: ImportContext = {
    access,
    options,
    targetSystemAccountId: importTargetSystemAccountId(access),
    providerByCode: new Map(providers.map((provider) => [provider.code, provider])),
    groupLookup: new Map(),
    proxyLookup: new Map()
  }
  const result = emptyResult('preview')
  const root = validateAccountImportRoot(data, result.messages, {
    maxAccounts: accountImportMaxAccounts,
    maxProxies: accountImportMaxProxies,
    protocolType: accountImportProtocolType,
    protocolVersion: accountImportProtocolVersion
  })
  if (!root.success) {
    return emptyPlan(result)
  }

  const { proxyPlans, proxyByRef } = planImportProxies(root.rawProxies, context)

  const groupIdsByKey = new Map<string, string>()
  const groupNamesToCreate = new Map<string, AccountImportGroupCreatePlan>()
  const accounts = root.rawAccounts.map((item, index) => planImportAccount(item, index + 1, context, proxyByRef, groupIdsByKey, groupNamesToCreate))
  markDuplicateAccountImportItems(accounts, context.options.skipDuplicates)

  result.proxies = proxyPlans.map((plan) => plan.item)
  result.accounts = accounts.map((plan) => plan.item)
  result.summary = buildAccountImportSummary(result.accounts, result.proxies, groupNamesToCreate)
  result.canImport = result.summary.accounts.failed === 0
    && result.summary.proxies.failed === 0
    && result.summary.accounts.create > 0

  return {
    result,
    accounts,
    proxies: proxyPlans,
    groupIdsByKey,
    groupNamesToCreate,
    options,
    access
  }
}

function emptyPlan(result: AccountImportResult): ImportPlan {
  return {
    result,
    accounts: [],
    proxies: [],
    groupIdsByKey: new Map(),
    groupNamesToCreate: new Map(),
    options: defaultAccountImportOptions()
  }
}

function emptyResult(mode: AccountImportResult['mode']): AccountImportResult {
  return {
    type: accountImportProtocolType,
    version: accountImportProtocolVersion,
    mode,
    canImport: false,
    imported: false,
    summary: {
      accounts: { total: 0, create: 0, skip: 0, failed: 0 },
      proxies: { total: 0, create: 0, reuse: 0, skip: 0, failed: 0 },
      groups: { create: 0, reuse: 0, failed: 0 }
    },
    accounts: [],
    proxies: [],
    messages: []
  }
}

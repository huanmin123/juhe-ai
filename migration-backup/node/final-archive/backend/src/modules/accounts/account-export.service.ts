import type { AccountAvailabilitySchedule, AccountHealthCheckEndpointMode, AccountModelMapping, AccountSummary, AccountType } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  findAccountSummaryAsync,
  getProxyTestConfig,
  getProxyTestConfigAsync,
  listAccounts,
  type ProxyProfileTestConfig
} from '../../storage/repositories.js'
import {
  accountImportProtocolType,
  accountImportProtocolVersion
} from './account-import.service.js'

type AccountExportStatus = 'active' | 'pending_test' | 'disabled'

export const accountExportMaxAccounts = 500

const accountExportAsyncReadConcurrency = 10

export interface AccountExportOptions {
  accountIds: string[]
  matchedAccounts?: number
  truncated?: boolean
}

export interface AccountExportProxy {
  ref: string
  name: string
  type: string
  host: string
  port: number
  username?: string
  password?: string
  description?: string
  enabled: boolean
}

export interface AccountExportAccount {
  ref: string
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  type: AccountType
  status: AccountExportStatus
  groupId?: string
  groupName?: string
  proxyRef?: string
  concurrencyLimit?: number
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  supportedModels?: string[]
  healthCheckModel?: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  temporaryUnavailableContinuousProbeEnabled?: boolean
  modelMappings?: AccountModelMapping[]
  tags?: string[]
  accountExpiresAt?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  credentials: Record<string, unknown>
  notes?: string
}

export interface AccountExportDocument {
  type: typeof accountImportProtocolType
  version: typeof accountImportProtocolVersion
  proxies?: AccountExportProxy[]
  accounts: AccountExportAccount[]
}

export interface AccountExportResult {
  document: AccountExportDocument
  summary: {
    accounts: number
    proxies: number
    skippedAccounts: number
    matchedAccounts?: number
    truncated?: boolean
  }
}

const apiKeyExportCredentialKeys = [
  'api_key',
  'api_keys',
  'api_key_strategy',
  'api_key_weights',
  'base_url',
  'supported_endpoint_modes',
  'service_tier_override',
  'reasoning_effort_override',
  'error_handling_rules',
  'response_inspection_rules',
  'quota_recovery_policy'
]

const oauthExportCredentialKeys = [
  'refresh_token',
  'access_token',
  'expires_at',
  'client_id',
  'id_token',
  'base_url',
  'supported_endpoint_modes',
  'service_tier_override',
  'reasoning_effort_override',
  'account_id',
  'email',
  'chatgpt_user_id',
  'plan_type',
  'error_handling_rules',
  'response_inspection_rules',
  'quota_recovery_policy'
]

export function exportAccountsAsImportDocument(options: AccountExportOptions, access: AccessScope): AccountExportResult {
  const accountIds = normalizeExportAccountIds(options.accountIds)
  const accountsById = new Map(
    listAccounts(access, {
      ids: accountIds,
      page: 1,
      pageSize: accountExportMaxAccounts
    })
      .filter(isExportableOwnerAccount)
      .map((account) => [account.id, account])
  )
  const accounts = accountIds.map((id) => accountsById.get(id)).filter((account): account is AccountSummary => Boolean(account))
  if (!accounts.length) {
    throw new Error('没有可导出的自有 AI 账户')
  }

  const proxies: AccountExportProxy[] = []
  const proxyRefsById = new Map<string, string>()
  const exportedAccounts = accounts.map((account) => exportAccount(account, proxyRefsById, proxies))
  const document: AccountExportDocument = {
    type: accountImportProtocolType,
    version: accountImportProtocolVersion,
    ...(proxies.length ? { proxies } : {}),
    accounts: exportedAccounts
  }
  return {
    document,
    summary: {
      accounts: exportedAccounts.length,
      proxies: proxies.length,
      skippedAccounts: Math.max(0, accountIds.length - exportedAccounts.length),
      ...(typeof options.matchedAccounts === 'number' ? { matchedAccounts: options.matchedAccounts } : {}),
      ...(typeof options.truncated === 'boolean' ? { truncated: options.truncated } : {})
    }
  }
}

export async function exportAccountsAsImportDocumentAsync(options: AccountExportOptions, access: AccessScope): Promise<AccountExportResult> {
  const accountIds = normalizeExportAccountIds(options.accountIds)
  const loadedAccounts = await mapWithConcurrency(
    accountIds,
    accountExportAsyncReadConcurrency,
    (id) => findAccountSummaryAsync(id, access)
  )
  const accountsById = new Map(
    loadedAccounts
      .filter((account): account is AccountSummary => Boolean(account))
      .filter(isExportableOwnerAccount)
      .map((account) => [account.id, account])
  )
  const accounts = accountIds.map((id) => accountsById.get(id)).filter((account): account is AccountSummary => Boolean(account))
  if (!accounts.length) {
    throw new Error('没有可导出的自有 AI 账户')
  }

  const proxies: AccountExportProxy[] = []
  const proxyRefsById = new Map<string, string>()
  const exportedAccounts: AccountExportAccount[] = []
  // Proxy refs are shared document state. Serialize materialization to keep each ref unique.
  for (const account of accounts) {
    exportedAccounts.push(await exportAccountAsync(account, proxyRefsById, proxies))
  }
  const document: AccountExportDocument = {
    type: accountImportProtocolType,
    version: accountImportProtocolVersion,
    ...(proxies.length ? { proxies } : {}),
    accounts: exportedAccounts
  }
  return {
    document,
    summary: {
      accounts: exportedAccounts.length,
      proxies: proxies.length,
      skippedAccounts: Math.max(0, accountIds.length - exportedAccounts.length),
      ...(typeof options.matchedAccounts === 'number' ? { matchedAccounts: options.matchedAccounts } : {}),
      ...(typeof options.truncated === 'boolean' ? { truncated: options.truncated } : {})
    }
  }
}

async function mapWithConcurrency<T, U>(
  values: readonly T[],
  concurrency: number,
  callback: (value: T) => Promise<U>
): Promise<U[]> {
  const output = new Array<U>(values.length)
  let nextIndex = 0
  const workerCount = Math.min(Math.max(1, concurrency), values.length)
  await Promise.all(Array.from({ length: workerCount }, async () => {
    while (nextIndex < values.length) {
      const index = nextIndex
      nextIndex += 1
      output[index] = await callback(values[index])
    }
  }))
  return output
}

function normalizeExportAccountIds(values: string[]): string[] {
  const ids = [...new Set(values.map((value) => typeof value === 'string' ? value.trim() : '').filter(Boolean))]
  if (!ids.length) {
    throw new Error('请选择要导出的 AI 账户')
  }
  if (ids.length > accountExportMaxAccounts) {
    throw new Error(`单次最多导出 ${accountExportMaxAccounts} 个 AI 账户`)
  }
  return ids
}

function isExportableOwnerAccount(account: AccountSummary): boolean {
  return account.accessType !== 'authorized'
    && account.permissions?.canViewCredentials !== false
    && account.permissions?.canEdit !== false
}

function exportAccount(account: AccountSummary, proxyRefsById: Map<string, string>, proxies: AccountExportProxy[]): AccountExportAccount {
  return buildExportAccount(account, exportProxyRef(account.proxyProfileId, proxyRefsById, proxies))
}

async function exportAccountAsync(account: AccountSummary, proxyRefsById: Map<string, string>, proxies: AccountExportProxy[]): Promise<AccountExportAccount> {
  return buildExportAccount(account, await exportProxyRefAsync(account.proxyProfileId, proxyRefsById, proxies))
}

function buildExportAccount(account: AccountSummary, proxyRef: string | undefined): AccountExportAccount {
  const status = exportAccountStatus(account)
  const output: AccountExportAccount = {
    ref: account.id,
    name: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    type: account.type,
    status,
    healthCheckEndpointMode: account.healthCheckEndpointMode,
    credentials: exportCredentials(account.type, account.credentials)
  }
  if (account.boundGroupName) {
    output.groupName = account.boundGroupName
  } else if (account.boundGroupId) {
    output.groupId = account.boundGroupId
  }
  if (proxyRef) output.proxyRef = proxyRef
  if (Number.isInteger(account.concurrencyLimit) && account.concurrencyLimit > 0) output.concurrencyLimit = account.concurrencyLimit
  if (Number.isInteger(account.priority) && account.priority >= 0) output.priority = account.priority
  if (status === 'active') {
    if (account.superPriorityEnabled) output.superPriorityEnabled = true
    if (account.fallbackEnabled) output.fallbackEnabled = true
  }
  if (account.supportedModels?.length) output.supportedModels = [...account.supportedModels]
  if (account.healthCheckModel) output.healthCheckModel = account.healthCheckModel
  if (account.temporaryUnavailableContinuousProbeEnabled === false) output.temporaryUnavailableContinuousProbeEnabled = false
  if (account.modelMappings?.length) output.modelMappings = account.modelMappings.map((item) => ({ ...item }))
  if (account.tags?.length) output.tags = account.tags.map((tag) => tag.name).filter(Boolean)
  if (account.accountExpiresAt) output.accountExpiresAt = account.accountExpiresAt
  if (account.availabilitySchedule) output.availabilitySchedule = account.availabilitySchedule
  if (account.notes) output.notes = account.notes
  return output
}

function exportAccountStatus(account: AccountSummary): AccountExportStatus {
  if (account.status === 'pending_test') return 'pending_test'
  return account.status === 'active' && account.schedulable !== false ? 'active' : 'disabled'
}

function exportCredentials(accountType: AccountType, credentials: Record<string, unknown>): Record<string, unknown> {
  const keys = accountType === 'api_key'
    ? apiKeyExportCredentialKeys
    : accountType === 'oauth'
      ? oauthExportCredentialKeys
      : Object.keys(credentials)
  const output: Record<string, unknown> = {}
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(credentials, key) && credentials[key] !== undefined) {
      output[key] = credentials[key]
    }
  }
  return output
}

function exportProxyRef(proxyProfileId: string | undefined, proxyRefsById: Map<string, string>, proxies: AccountExportProxy[]): string | undefined {
  if (!proxyProfileId) return undefined
  const existingRef = proxyRefsById.get(proxyProfileId)
  if (existingRef) return existingRef
  const proxy = exportableProxy(proxyProfileId)
  if (!proxy) return undefined
  const ref = `proxy-${proxyProfileId}`
  proxyRefsById.set(proxyProfileId, ref)
  proxies.push(exportProxy(proxy, ref))
  return ref
}

function exportableProxy(proxyProfileId: string): ProxyProfileTestConfig | undefined {
  try {
    const proxy = getProxyTestConfig(proxyProfileId)
    return proxy?.enabled ? proxy : undefined
  } catch {
    return undefined
  }
}

async function exportProxyRefAsync(proxyProfileId: string | undefined, proxyRefsById: Map<string, string>, proxies: AccountExportProxy[]): Promise<string | undefined> {
  if (!proxyProfileId) return undefined
  const existingRef = proxyRefsById.get(proxyProfileId)
  if (existingRef) return existingRef
  const proxy = await exportableProxyAsync(proxyProfileId)
  if (!proxy) return undefined
  const ref = `proxy-${proxyProfileId}`
  proxyRefsById.set(proxyProfileId, ref)
  proxies.push(exportProxy(proxy, ref))
  return ref
}

async function exportableProxyAsync(proxyProfileId: string): Promise<ProxyProfileTestConfig | undefined> {
  try {
    const proxy = await getProxyTestConfigAsync(proxyProfileId)
    return proxy?.enabled ? proxy : undefined
  } catch {
    return undefined
  }
}

function exportProxy(proxy: ProxyProfileTestConfig, ref: string): AccountExportProxy {
  const credentials = proxyCredentials(proxy.proxyUrl)
  return {
    ref,
    name: proxy.name,
    type: proxy.type,
    host: proxy.host,
    port: proxy.port,
    ...(proxy.username ? { username: proxy.username } : {}),
    ...(credentials.password ? { password: credentials.password } : {}),
    ...(proxy.description ? { description: proxy.description } : {}),
    enabled: true
  }
}

function proxyCredentials(proxyUrl: string): { password?: string } {
  try {
    const url = new URL(proxyUrl)
    return {
      password: url.password ? decodeURIComponent(url.password) : undefined
    }
  } catch {
    return {}
  }
}

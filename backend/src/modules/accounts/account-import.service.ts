import type { AccountAvailabilitySchedule, AccountType, ProviderDefinition } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountAvailabilityScheduleFromRequest } from '../../storage/account-availability-schedule.js'
import {
  DuplicateAccountCredentialError,
  createAccount,
  createGroup,
  createProxy,
  findGroupSummary,
  findProxy,
  listGroupOptions,
  listProviders,
  listProxyOptions,
  type GroupOptionSummary,
  type ProxyProfileOptionSummary,
  type ProxyProfileSummary
} from '../../storage/repositories.js'

export const accountImportProtocolType = 'juhe-ai-account-import'
export const accountImportProtocolVersion = 1

const maxImportAccounts = 500
const maxImportProxies = 200
const defaultBaseUrl = 'https://api.openai.com/v1'

type ImportAction = 'create' | 'reuse' | 'skip' | 'failed'
type AccountImportStatus = 'active' | 'disabled'

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

interface ImportDefaults {
  providerCode: string
  type: AccountType
  status: AccountImportStatus
  baseUrl: string
  groupId?: string
  groupName?: string
  proxyRef?: string
  proxyProfileId?: string
  concurrencyLimit?: number
  priority?: number
  accountExpiresAt?: string
  availabilitySchedule?: AccountAvailabilitySchedule
}

interface NormalizedImportAccount {
  index: number
  ref?: string
  name: string
  providerCode: string
  type: AccountType
  status: AccountImportStatus
  credentials: Record<string, unknown>
  groupId?: string
  groupName?: string
  proxyRef?: string
  proxyProfileId?: string
  concurrencyLimit?: number
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  supportedModels?: string[]
  accountExpiresAt?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  notes?: string
  messages: string[]
  warnings: string[]
}

interface NormalizedImportProxy {
  index: number
  ref: string
  name: string
  type: 'http' | 'https' | 'socks5' | 'socks5h'
  host: string
  port: number
  username?: string
  password?: string
  description?: string
  enabled: boolean
  messages: string[]
  warnings: string[]
}

interface AccountPlan {
  source: NormalizedImportAccount
  item: AccountImportItem
  groupId?: string
  proxyProfileId?: string
}

interface ProxyPlan {
  source: NormalizedImportProxy
  item: AccountImportProxyItem
  proxyProfileId?: string
}

interface ImportPlan {
  result: AccountImportResult
  accounts: AccountPlan[]
  proxies: ProxyPlan[]
  groupIdsByKey: Map<string, string>
  groupNamesToCreate: Map<string, { providerCode: string; name: string }>
  options: Required<AccountImportOptions>
}

interface ImportContext {
  access?: AccessScope
  options: Required<AccountImportOptions>
  providers: ProviderDefinition[]
  providerByCode: Map<string, ProviderDefinition>
  groupLookup: Map<string, GroupOptionSummary | undefined>
  proxyLookup: Map<string, ProxyProfileOptionSummary | undefined>
}

export function previewAccountImport(data: unknown, options: AccountImportOptions = {}, access?: AccessScope): AccountImportResult {
  return buildImportPlan(data, options, access).result
}

export function executeAccountImport(data: unknown, options: AccountImportOptions = {}, access?: AccessScope): AccountImportResult {
  const plan = buildImportPlan(data, options, access)
  const result = plan.result
  result.mode = 'import'
  if (!result.canImport) {
    return result
  }

  const createdProxyIds = createPlannedProxies(plan)
  for (const [ref, proxyId] of createdProxyIds) {
    for (const account of plan.accounts) {
      if (account.source.proxyRef === ref && !account.proxyProfileId) {
        account.proxyProfileId = proxyId
      }
    }
  }
  failAccountsWithUnresolvedProxy(plan)

  createPlannedGroups(plan, access)
  for (const account of plan.accounts) {
    if (account.item.action !== 'create') continue
    const groupId = account.groupId ?? groupIdForAccount(plan, account.source)
    const proxyProfileId = account.proxyProfileId
    try {
      const created = createAccount({
        providerCode: account.source.providerCode,
        name: account.source.name,
        type: account.source.type,
        status: account.source.status,
        credentials: account.source.credentials,
        groupId,
        proxyProfileId,
        concurrencyLimit: account.source.concurrencyLimit,
        priority: account.source.priority,
        superPriorityEnabled: account.source.superPriorityEnabled,
        fallbackEnabled: account.source.fallbackEnabled,
        supportedModels: account.source.supportedModels,
        accountExpiresAt: account.source.accountExpiresAt,
        availabilitySchedule: account.source.availabilitySchedule,
        notes: account.source.notes
      }, access)
      account.item.accountId = created.id
      account.item.messages = ['已创建账户']
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

  result.imported = true
  result.canImport = false
  return result
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
    providers,
    providerByCode: new Map(providers.map((provider) => [provider.code, provider])),
    groupLookup: new Map(),
    proxyLookup: new Map()
  }
  const result = emptyResult('preview')
  if (!isRecord(data)) {
    result.messages.push('导入内容必须是 JSON 对象')
    return emptyPlan(result)
  }
  if (data.type !== accountImportProtocolType) {
    result.messages.push(`type 必须是 ${accountImportProtocolType}`)
  }
  if (data.version !== accountImportProtocolVersion) {
    result.messages.push(`version 必须是 ${accountImportProtocolVersion}`)
  }
  if (result.messages.length > 0) {
    return emptyPlan(result)
  }

  const defaults = normalizeDefaults(data.defaults, context, result.messages)
  if (result.messages.length > 0) {
    return emptyPlan(result)
  }
  const rawProxies = Array.isArray(data.proxies) ? data.proxies : []
  const rawAccounts = Array.isArray(data.accounts) ? data.accounts : undefined
  if (!rawAccounts || rawAccounts.length === 0) {
    result.messages.push('accounts 至少需要 1 条账户')
    return emptyPlan(result)
  }
  if (rawAccounts.length > maxImportAccounts) {
    result.messages.push(`accounts 单次最多导入 ${maxImportAccounts} 条`)
    return emptyPlan(result)
  }
  if (rawProxies.length > maxImportProxies) {
    result.messages.push(`proxies 单次最多导入 ${maxImportProxies} 条`)
    return emptyPlan(result)
  }

  const proxyPlans = rawProxies.map((item, index) => planProxy(item, index + 1, context))
  const proxyByRef = new Map<string, ProxyPlan>()
  for (const proxy of proxyPlans) {
    if (proxy.source.ref && !proxyByRef.has(proxy.source.ref)) {
      proxyByRef.set(proxy.source.ref, proxy)
    } else if (proxy.source.ref) {
      proxy.item.action = 'failed'
      proxy.item.messages.push(`代理 ref 重复：${proxy.source.ref}`)
    }
  }

  const groupIdsByKey = new Map<string, string>()
  const groupNamesToCreate = new Map<string, { providerCode: string; name: string }>()
  const accounts = rawAccounts.map((item, index) => planAccount(item, index + 1, defaults, context, proxyByRef, groupIdsByKey, groupNamesToCreate))
  markDuplicateAccounts(accounts, context.options.skipDuplicates)

  result.proxies = proxyPlans.map((plan) => plan.item)
  result.accounts = accounts.map((plan) => plan.item)
  result.summary = buildSummary(result.accounts, result.proxies, groupNamesToCreate)
  result.canImport = result.summary.accounts.failed === 0
    && result.summary.proxies.failed === 0
    && result.summary.accounts.create > 0

  return {
    result,
    accounts,
    proxies: proxyPlans,
    groupIdsByKey,
    groupNamesToCreate,
    options
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

function normalizeDefaults(value: unknown, context: ImportContext, messages: string[]): ImportDefaults {
  const record = isRecord(value) ? value : {}
  const providerCode = text(record.providerCode) || 'openai'
  const provider = context.providerByCode.get(providerCode)
  return {
    providerCode,
    type: text(record.type) || 'api_key',
    status: normalizeStatus(record.status) ?? 'active',
    baseUrl: text(record.baseUrl) || provider?.baseUrl || defaultBaseUrl,
    groupId: text(record.groupId),
    groupName: text(record.groupName),
    proxyRef: text(record.proxyRef),
    proxyProfileId: text(record.proxyProfileId),
    concurrencyLimit: positiveInteger(record.concurrencyLimit),
    priority: integer(record.priority),
    accountExpiresAt: dateTimeString(record.accountExpiresAt),
    availabilitySchedule: normalizeImportAvailabilitySchedule(importAvailabilityScheduleInput(record).value, messages)
  }
}

function planProxy(value: unknown, index: number, context: ImportContext): ProxyPlan {
  const item: AccountImportProxyItem = { index, action: 'create', messages: [], warnings: [] }
  const source: NormalizedImportProxy = {
    index,
    ref: '',
    name: '',
    type: 'socks5h',
    host: '',
    port: 0,
    enabled: true,
    messages: item.messages,
    warnings: item.warnings
  }
  if (!isRecord(value)) {
    item.action = 'failed'
    item.messages.push('代理配置必须是对象')
    return { source, item }
  }
  source.ref = text(value.ref)
  source.name = text(value.name)
  const rawProxyType = text(value.type)
  source.type = normalizeProxyType(value.type)
  source.host = text(value.host)
  source.port = positiveInteger(value.port) ?? 0
  source.username = text(value.username)
  source.password = text(value.password)
  source.description = text(value.description)
  source.enabled = normalizeProxyEnabled(value.enabled)
  item.ref = source.ref
  item.name = source.name

  if (!source.ref) item.messages.push('代理 ref 不能为空')
  if (!source.name) item.messages.push('代理名称不能为空')
  if (rawProxyType && !isProxyType(rawProxyType)) item.messages.push(`代理 type 不支持：${rawProxyType}`)
  if (!source.host) item.messages.push('代理 host 不能为空')
  if (source.port < 1 || source.port > 65535) item.messages.push('代理 port 必须是 1 到 65535 的整数')
  if (!context.options.createMissingProxies) {
    item.action = 'skip'
    item.warnings.push('当前导入选项未启用代理创建')
  } else if (context.access?.role !== 'admin') {
    item.action = 'failed'
    item.messages.push('用户侧导入不能创建代理，请由管理员先创建代理')
  } else {
    const existing = findProxyOptionByName(source.name, context)
    if (existing) {
      item.action = 'reuse'
      item.proxyProfileId = existing.id
    }
  }
  if (item.messages.length > 0) {
    item.action = 'failed'
  }
  return {
    source,
    item,
    proxyProfileId: item.proxyProfileId
  }
}

function planAccount(
  value: unknown,
  index: number,
  defaults: ImportDefaults,
  context: ImportContext,
  proxyByRef: Map<string, ProxyPlan>,
  groupIdsByKey: Map<string, string>,
  groupNamesToCreate: Map<string, { providerCode: string; name: string }>
): AccountPlan {
  const item: AccountImportItem = { index, action: 'create', messages: [], warnings: [] }
  const source: NormalizedImportAccount = {
    index,
    name: '',
    providerCode: defaults.providerCode,
    type: defaults.type,
    status: defaults.status,
    credentials: {},
    groupId: defaults.groupId,
    groupName: defaults.groupName,
    proxyRef: defaults.proxyRef,
    proxyProfileId: defaults.proxyProfileId,
    concurrencyLimit: defaults.concurrencyLimit,
    priority: defaults.priority,
    accountExpiresAt: defaults.accountExpiresAt,
    messages: item.messages,
    warnings: item.warnings
  }
  if (!isRecord(value)) {
    item.action = 'failed'
    item.messages.push('账户配置必须是对象')
    return { source, item }
  }
  source.ref = text(value.ref)
  source.name = text(value.name)
  source.providerCode = text(value.providerCode) || defaults.providerCode
  source.type = text(value.type) || defaults.type
  const rawStatus = text(value.status)
  source.status = normalizeStatus(value.status) ?? defaults.status
  source.groupId = text(value.groupId) || defaults.groupId
  source.groupName = text(value.groupName) || defaults.groupName
  source.proxyRef = text(value.proxyRef) || defaults.proxyRef
  source.proxyProfileId = text(value.proxyProfileId) || defaults.proxyProfileId
  source.concurrencyLimit = positiveInteger(value.concurrencyLimit) ?? defaults.concurrencyLimit
  source.priority = integer(value.priority) ?? defaults.priority
  source.superPriorityEnabled = booleanValue(value.superPriorityEnabled)
  source.fallbackEnabled = booleanValue(value.fallbackEnabled)
  source.supportedModels = stringArray(value.supportedModels)
  const rawAccountExpiresAt = text(value.accountExpiresAt)
  source.accountExpiresAt = dateTimeString(value.accountExpiresAt) ?? defaults.accountExpiresAt
  const availabilityScheduleInput = importAvailabilityScheduleInput(value)
  source.availabilitySchedule = availabilityScheduleInput.present
    ? normalizeImportAvailabilitySchedule(availabilityScheduleInput.value, item.messages)
    : defaults.availabilitySchedule
  source.notes = text(value.notes)
  source.credentials = normalizeCredentials(value.credentials, defaults, source.providerCode)

  item.ref = source.ref
  item.name = source.name
  item.providerCode = source.providerCode
  item.accountType = source.type
  item.groupName = source.groupName
  item.groupId = source.groupId
  item.proxyRef = source.proxyRef

  if (rawStatus && !normalizeStatus(rawStatus)) {
    item.messages.push(`账户状态不支持：${rawStatus}`)
  }
  if (rawAccountExpiresAt && !dateTimeString(rawAccountExpiresAt)) {
    item.messages.push('accountExpiresAt 必须是有效时间字符串')
  }
  validateAccountBasics(source, context)
  const groupId = resolveAccountGroup(source, context, groupIdsByKey, groupNamesToCreate, item)
  const proxyProfileId = resolveAccountProxy(source, proxyByRef, item)
  if (item.messages.length > 0) {
    item.action = 'failed'
  }
  return {
    source,
    item,
    groupId,
    proxyProfileId
  }
}

function validateAccountBasics(account: NormalizedImportAccount, context: ImportContext): void {
  if (!account.name) account.messages.push('账户名称不能为空')
  const provider = context.providerByCode.get(account.providerCode)
  if (!provider) {
    account.messages.push(`不支持的供应商：${account.providerCode}`)
  } else if (!provider.enabled) {
    account.messages.push(`供应商已停用：${account.providerCode}`)
  } else if (!provider.accountTypes.includes(account.type)) {
    account.messages.push(`供应商 ${account.providerCode} 不支持账户类型 ${account.type}`)
  }
  if (account.status !== 'active' && account.status !== 'disabled') {
    account.messages.push('账户状态仅支持 active 或 disabled')
  }
  if (account.type === 'api_key' && !text(account.credentials.api_key)) {
    account.messages.push('API Key 账户必须填写密钥')
  }
  if (account.type === 'oauth' && !text(account.credentials.refresh_token) && !text(account.credentials.access_token)) {
    account.messages.push('OAuth 账户必须填写刷新令牌或访问令牌')
  }
  if (account.concurrencyLimit !== undefined && account.concurrencyLimit < 1) {
    account.messages.push('concurrencyLimit 必须大于 0')
  }
  if (account.accountExpiresAt && !dateTimeString(account.accountExpiresAt)) {
    account.messages.push('accountExpiresAt 必须是有效时间字符串')
  }
}

function importAvailabilityScheduleInput(record: Record<string, unknown>): { present: boolean; value: unknown } {
  if (Object.prototype.hasOwnProperty.call(record, 'availabilitySchedule')) {
    return { present: true, value: record.availabilitySchedule }
  }
  return { present: false, value: undefined }
}

function normalizeImportAvailabilitySchedule(value: unknown, messages: string[]): AccountAvailabilitySchedule | undefined {
  if (value === undefined) return undefined
  try {
    return accountAvailabilityScheduleFromRequest({ availabilitySchedule: value })
  } catch (error) {
    messages.push(errorMessage(error))
    return undefined
  }
}

function resolveAccountGroup(
  account: NormalizedImportAccount,
  context: ImportContext,
  groupIdsByKey: Map<string, string>,
  groupNamesToCreate: Map<string, { providerCode: string; name: string }>,
  item: AccountImportItem
): string | undefined {
  if (account.groupId && account.groupName) {
    item.warnings.push('同时填写 groupId 和 groupName 时优先使用 groupId')
  }
  if (account.groupId) {
    const group = findGroupSummary(account.groupId, context.access)
    if (!group) {
      item.messages.push(`分组不存在或无权使用：${account.groupId}`)
      return undefined
    }
    if (group.providerCode !== account.providerCode) {
      item.messages.push(`分组供应商与账户供应商不一致：${group.name}`)
      return undefined
    }
    return group.id
  }
  if (!account.groupName) {
    return undefined
  }
  const key = groupKey(account.providerCode, account.groupName)
  const existingGroupId = groupIdsByKey.get(key)
  if (existingGroupId) return existingGroupId
  const group = findGroupOptionByName(account.providerCode, account.groupName, context)
  if (group) {
    groupIdsByKey.set(key, group.id)
    return group.id
  }
  if (!context.options.createMissingGroups) {
    item.messages.push(`分组不存在：${account.groupName}`)
    return undefined
  }
  groupNamesToCreate.set(key, { providerCode: account.providerCode, name: account.groupName })
  return undefined
}

function resolveAccountProxy(
  account: NormalizedImportAccount,
  proxyByRef: Map<string, ProxyPlan>,
  item: AccountImportItem
): string | undefined {
  if (account.proxyRef && account.proxyProfileId) {
    item.messages.push('proxyRef 和 proxyProfileId 只能填写一个')
    return undefined
  }
  if (account.proxyProfileId) {
    const proxy = findProxy(account.proxyProfileId)
    if (!proxy) {
      item.messages.push(`代理不存在：${account.proxyProfileId}`)
      return undefined
    }
    if (!proxy.enabled) {
      item.messages.push(`代理已停用：${proxy.name}`)
      return undefined
    }
    return proxy.id
  }
  if (!account.proxyRef) {
    return undefined
  }
  const plannedProxy = proxyByRef.get(account.proxyRef)
  if (plannedProxy) {
    if (plannedProxy.item.action === 'failed') {
      item.messages.push(`代理引用不可用：${account.proxyRef}`)
    }
    if (plannedProxy.item.action === 'skip') {
      item.messages.push(`代理引用未创建：${account.proxyRef}`)
    }
    return plannedProxy.proxyProfileId
  }
  const proxy = findProxy(account.proxyRef)
  if (!proxy) {
    item.messages.push(`代理引用不存在：${account.proxyRef}`)
    return undefined
  }
  if (!proxy.enabled) {
    item.messages.push(`代理已停用：${proxy.name}`)
    return undefined
  }
  return proxy.id
}

function markDuplicateAccounts(accounts: AccountPlan[], skipDuplicates: boolean): void {
  const seenName = new Map<string, number>()
  const seenCredential = new Map<string, number>()
  for (const account of accounts) {
    if (account.item.action === 'failed') continue
    const nameKey = `${account.source.providerCode}:${account.source.name.trim().toLowerCase()}`
    const credentialKey = accountCredentialKey(account.source)
    const duplicatedByName = seenName.get(nameKey)
    const duplicatedByCredential = credentialKey ? seenCredential.get(credentialKey) : undefined
    if (duplicatedByName || duplicatedByCredential) {
      account.item.action = skipDuplicates ? 'skip' : 'failed'
      account.item.messages.push(duplicatedByName
        ? `与第 ${duplicatedByName} 条账户名称重复`
        : `与第 ${duplicatedByCredential} 条账户凭据重复`)
    } else {
      seenName.set(nameKey, account.source.index)
      if (credentialKey) seenCredential.set(credentialKey, account.source.index)
    }
  }
}

function accountCredentialKey(account: NormalizedImportAccount): string | undefined {
  const secret = account.type === 'oauth'
    ? text(account.credentials.refresh_token) || text(account.credentials.access_token)
    : text(account.credentials.api_key)
  if (!secret) return undefined
  return [
    account.providerCode,
    account.type,
    text(account.credentials.base_url) || defaultBaseUrl,
    secret
  ].join('|')
}

function createPlannedProxies(plan: ImportPlan): Map<string, string> {
  const created = new Map<string, string>()
  for (const proxy of plan.proxies) {
    if (proxy.item.action === 'reuse' && proxy.proxyProfileId) {
      created.set(proxy.source.ref, proxy.proxyProfileId)
      continue
    }
    if (proxy.item.action !== 'create') continue
    try {
      const createdProxy = createProxy({
        name: proxy.source.name,
        description: proxy.source.description,
        type: proxy.source.type,
        host: proxy.source.host,
        port: proxy.source.port,
        username: proxy.source.username,
        password: proxy.source.password,
        enabled: proxy.source.enabled
      })
      proxy.proxyProfileId = createdProxy.id
      proxy.item.proxyProfileId = createdProxy.id
      proxy.item.messages = ['已创建代理']
      created.set(proxy.source.ref, createdProxy.id)
    } catch (error) {
      const existing = findProxyByName(proxy.source.name)
      if (existing) {
        proxy.item.action = 'reuse'
        proxy.item.proxyProfileId = existing.id
        proxy.item.messages = ['代理名称已存在，已复用现有代理']
        proxy.item.warnings.push(errorMessage(error))
        proxy.proxyProfileId = existing.id
        created.set(proxy.source.ref, existing.id)
        plan.result.summary.proxies.create -= 1
        plan.result.summary.proxies.reuse += 1
        continue
      }
      proxy.item.action = 'failed'
      proxy.item.messages = [errorMessage(error)]
      plan.result.summary.proxies.create -= 1
      plan.result.summary.proxies.failed += 1
    }
  }
  return created
}

function failAccountsWithUnresolvedProxy(plan: ImportPlan): void {
  const proxyByRef = new Map(plan.proxies.map((proxy) => [proxy.source.ref, proxy]))
  for (const account of plan.accounts) {
    if (account.item.action !== 'create' || !account.source.proxyRef || account.proxyProfileId) continue
    const proxy = proxyByRef.get(account.source.proxyRef)
    if (!proxy) continue
    account.item.action = 'failed'
    account.item.messages = [`代理创建失败，账户未导入：${account.source.proxyRef}`]
    plan.result.summary.accounts.create -= 1
    plan.result.summary.accounts.failed += 1
  }
}

function createPlannedGroups(plan: ImportPlan, access?: AccessScope): void {
  for (const [key, group] of plan.groupNamesToCreate) {
    try {
      const created = createGroup({
        providerCode: group.providerCode,
        name: group.name,
        description: '由账户导入自动创建'
      }, access)
      plan.groupIdsByKey.set(key, created.id)
    } catch (error) {
      const existing = findGroupOptionByName(group.providerCode, group.name, {
        access,
        options: defaultAccountImportOptions(),
        providers: listProviders(),
        providerByCode: new Map(listProviders().map((provider) => [provider.code, provider])),
        groupLookup: new Map(),
        proxyLookup: new Map()
      })
      if (existing) {
        plan.groupIdsByKey.set(key, existing.id)
        plan.result.summary.groups.create -= 1
        plan.result.summary.groups.reuse += 1
        continue
      }
      plan.result.summary.groups.create -= 1
      plan.result.summary.groups.failed += 1
      for (const account of plan.accounts) {
        if (account.source.groupName && groupKey(account.source.providerCode, account.source.groupName) === key && account.item.action === 'create') {
          account.item.action = 'failed'
          account.item.messages = [errorMessage(error)]
          plan.result.summary.accounts.create -= 1
          plan.result.summary.accounts.failed += 1
        }
      }
    }
  }
}

function groupIdForAccount(plan: ImportPlan, account: NormalizedImportAccount): string | undefined {
  if (account.groupId) return account.groupId
  if (!account.groupName) return undefined
  return plan.groupIdsByKey.get(groupKey(account.providerCode, account.groupName))
}

function buildSummary(
  accounts: AccountImportItem[],
  proxies: AccountImportProxyItem[],
  groupsToCreate: Map<string, { providerCode: string; name: string }>
): AccountImportSummary {
  const groupRefs = new Set<string>()
  for (const item of accounts) {
    if (item.action === 'failed') continue
    if (item.groupId) {
      groupRefs.add(`id:${item.groupId}`)
    } else if (item.groupName) {
      groupRefs.add(groupKey(item.providerCode ?? '', item.groupName))
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

function normalizeCredentials(value: unknown, defaults: ImportDefaults, providerCode: string): Record<string, unknown> {
  const input = isRecord(value) ? value : {}
  const output: Record<string, unknown> = {}
  for (const [key, rawValue] of Object.entries(input)) {
    if (!key.trim() || rawValue === undefined || rawValue === null || rawValue === '') continue
    output[key.trim()] = typeof rawValue === 'string' ? rawValue.trim() : rawValue
  }
  if (!text(output.base_url)) {
    const providerBaseUrl = providerCode === defaults.providerCode ? defaults.baseUrl : undefined
    output.base_url = providerBaseUrl || defaultBaseUrl
  }
  return output
}

function findGroupOptionByName(providerCode: string, name: string, context: ImportContext): GroupOptionSummary | undefined {
  const key = groupKey(providerCode, name)
  if (context.groupLookup.has(key)) {
    return context.groupLookup.get(key)
  }
  const normalized = name.trim().toLowerCase()
  const group = listGroupOptions(context.access, {
    providerCode,
    keyword: name,
    manageableOnly: true,
    limit: 50
  }).find((item) => item.name.trim().toLowerCase() === normalized)
  context.groupLookup.set(key, group)
  return group
}

function findProxyOptionByName(name: string, context: ImportContext): ProxyProfileOptionSummary | undefined {
  const key = name.trim().toLowerCase()
  if (context.proxyLookup.has(key)) {
    return context.proxyLookup.get(key)
  }
  const proxy = listProxyOptions({ keyword: name, limit: 50 }).find((item) => item.name.trim().toLowerCase() === key)
  context.proxyLookup.set(key, proxy)
  return proxy
}

function findProxyByName(name: string): ProxyProfileSummary | undefined {
  const option = listProxyOptions({ keyword: name, limit: 50 }).find((item) => item.name.trim().toLowerCase() === name.trim().toLowerCase())
  return option ? findProxy(option.id) : undefined
}

function groupKey(providerCode: string, name: string): string {
  return `${providerCode.trim().toLowerCase()}:${name.trim().toLowerCase()}`
}

function normalizeStatus(value: unknown): AccountImportStatus | undefined {
  const input = text(value)
  if (input === 'active' || input === 'disabled') return input
  return undefined
}

function normalizeProxyType(value: unknown): NormalizedImportProxy['type'] {
  const input = text(value)
  return isProxyType(input) ? input : 'socks5h'
}

function isProxyType(value: string): value is NormalizedImportProxy['type'] {
  return value === 'http' || value === 'https' || value === 'socks5' || value === 'socks5h'
}

function normalizeProxyEnabled(value: unknown): boolean {
  if (typeof value === 'boolean') return value
  const input = text(value)
  if (!input) return true
  return input !== 'disabled' && input !== 'inactive'
}

function stringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const items = value.map((item) => text(item)).filter(Boolean)
  return items.length ? [...new Set(items)] : undefined
}

function positiveInteger(value: unknown): number | undefined {
  const number = integer(value)
  return number !== undefined && number > 0 ? number : undefined
}

function integer(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isInteger(value)) return value
  if (typeof value !== 'string' || !value.trim()) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) ? parsed : undefined
}

function booleanValue(value: unknown): boolean | undefined {
  return typeof value === 'boolean' ? value : undefined
}

function dateTimeString(value: unknown): string | undefined {
  const input = text(value)
  if (!input) return undefined
  return Number.isFinite(Date.parse(input)) ? input : undefined
}

function isDuplicateAccountError(error: unknown): boolean {
  return error instanceof DuplicateAccountCredentialError
    || (error instanceof Error && error.message.includes('账户名称已存在'))
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

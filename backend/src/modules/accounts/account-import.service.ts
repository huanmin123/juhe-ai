import { isAdminRole, type AccountAvailabilitySchedule, type AccountType, type ProviderDefinition } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountAvailabilityScheduleFromRequest } from '../../storage/account-availability-schedule.js'
import {
  createAccount,
  createGroup,
  createProxy,
  findGroupSummary,
  findProxy,
  listGroupOptions,
  listProviders,
  listProxyOptions,
  normalizeAccountCredentialsForWrite,
  type GroupOptionSummary,
  type ProxyProfileOptionSummary,
  type ProxyProfileSummary
} from '../../storage/repositories.js'
import { optionalServerDateTimeIso } from '../../storage/value-utils.js'

export const accountImportProtocolType = 'juhe-ai-account-import'
export const accountImportProtocolVersion = 1
export const accountImportMaxAccounts = 50
export const accountImportMaxProxies = 20

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
  access?: AccessScope
}

interface ImportContext {
  access?: AccessScope
  options: Required<AccountImportOptions>
  providers: ProviderDefinition[]
  providerByCode: Map<string, ProviderDefinition>
  groupLookup: Map<string, GroupOptionSummary | undefined>
  proxyLookup: Map<string, ProxyProfileOptionSummary | undefined>
}

const importRootKeys = new Set(['type', 'version', 'proxies', 'accounts'])
const importProxyKeys = new Set([
  'ref',
  'name',
  'type',
  'host',
  'port',
  'username',
  'password',
  'description',
  'enabled'
])
const importAccountKeys = new Set([
  'ref',
  'name',
  'providerCode',
  'type',
  'status',
  'credentials',
  'groupId',
  'groupName',
  'proxyRef',
  'proxyProfileId',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'supportedModels',
  'accountExpiresAt',
  'availabilitySchedule',
  'notes'
])

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

  const createdProxyIds = createPlannedProxies(plan, access)
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
      const accountInput: Record<string, unknown> = {
        providerCode: account.source.providerCode,
        name: account.source.name,
        type: account.source.type,
        status: account.source.status,
        credentials: account.source.credentials
      }
      if (groupId !== undefined) accountInput.groupId = groupId
      if (proxyProfileId !== undefined) accountInput.proxyProfileId = proxyProfileId
      if (account.source.concurrencyLimit !== undefined) accountInput.concurrencyLimit = account.source.concurrencyLimit
      if (account.source.priority !== undefined) accountInput.priority = account.source.priority
      if (account.source.superPriorityEnabled !== undefined) accountInput.superPriorityEnabled = account.source.superPriorityEnabled
      if (account.source.fallbackEnabled !== undefined) accountInput.fallbackEnabled = account.source.fallbackEnabled
      if (account.source.supportedModels !== undefined) accountInput.supportedModels = account.source.supportedModels
      if (account.source.accountExpiresAt !== undefined) accountInput.accountExpiresAt = account.source.accountExpiresAt
      if (account.source.availabilitySchedule !== undefined) accountInput.availabilitySchedule = account.source.availabilitySchedule
      if (account.source.notes !== undefined) accountInput.notes = account.source.notes
      const created = createAccount(accountInput, access)
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
  appendUnknownFieldMessages(data, importRootKeys, '导入内容', result.messages)
  if (data.type !== accountImportProtocolType) {
    result.messages.push(`type 必须是 ${accountImportProtocolType}`)
  }
  if (data.version !== accountImportProtocolVersion) {
    result.messages.push(`version 必须是 ${accountImportProtocolVersion}`)
  }
  if (result.messages.length > 0) {
    return emptyPlan(result)
  }

  const rawProxies = Array.isArray(data.proxies) ? data.proxies : []
  if (hasOwnField(data, 'proxies') && !Array.isArray(data.proxies)) {
    result.messages.push('proxies 必须是数组')
  }
  const rawAccounts = Array.isArray(data.accounts) ? data.accounts : undefined
  if (hasOwnField(data, 'accounts') && !Array.isArray(data.accounts)) {
    result.messages.push('accounts 必须是数组')
  }
  if (result.messages.length > 0) {
    return emptyPlan(result)
  }
  if (!rawAccounts || rawAccounts.length === 0) {
    result.messages.push('accounts 至少需要 1 条账户')
    return emptyPlan(result)
  }
  if (rawAccounts.length > accountImportMaxAccounts) {
    result.messages.push(`accounts 单次最多导入 ${accountImportMaxAccounts} 条`)
    return emptyPlan(result)
  }
  if (rawProxies.length > accountImportMaxProxies) {
    result.messages.push(`proxies 单次最多导入 ${accountImportMaxProxies} 条`)
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
  const accounts = rawAccounts.map((item, index) => planAccount(item, index + 1, context, proxyByRef, groupIdsByKey, groupNamesToCreate))
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
  appendUnknownFieldMessages(value, importProxyKeys, '代理配置', item.messages)
  source.ref = optionalTextField(value, 'ref', '代理 ref', item.messages) ?? ''
  source.name = optionalTextField(value, 'name', '代理名称', item.messages) ?? ''
  const proxyTypeInput = optionalTextField(value, 'type', '代理 type', item.messages)
  if (proxyTypeInput) {
    try {
      source.type = normalizeProxyType(proxyTypeInput)
    } catch (error) {
      item.messages.push(errorMessage(error))
    }
  }
  source.host = optionalTextField(value, 'host', '代理 host', item.messages) ?? ''
  source.port = optionalPositiveIntegerField(value, 'port', '代理 port', item.messages) ?? 0
  source.username = optionalTextField(value, 'username', '代理 username', item.messages)
  source.password = optionalTextField(value, 'password', '代理 password', item.messages)
  source.description = optionalTextField(value, 'description', '代理 description', item.messages)
  const proxyEnabled = optionalBooleanField(value, 'enabled', '代理 enabled', item.messages)
  if (proxyEnabled !== undefined) {
    source.enabled = proxyEnabled
  }
  item.ref = source.ref
  item.name = source.name

  if (!source.ref) item.messages.push('代理 ref 不能为空')
  if (!source.name) item.messages.push('代理名称不能为空')
  if (!proxyTypeInput) item.messages.push('代理 type 不能为空')
  if (!source.host) item.messages.push('代理 host 不能为空')
  if (source.port < 1 || source.port > 65535) item.messages.push('代理 port 必须是 1 到 65535 的整数')
  if (!context.options.createMissingProxies) {
    item.action = 'skip'
    item.warnings.push('当前导入选项未启用代理创建')
  } else if (!isAdminRole(context.access?.role)) {
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
  context: ImportContext,
  proxyByRef: Map<string, ProxyPlan>,
  groupIdsByKey: Map<string, string>,
  groupNamesToCreate: Map<string, { providerCode: string; name: string }>
): AccountPlan {
  const item: AccountImportItem = { index, action: 'create', messages: [], warnings: [] }
  const source: NormalizedImportAccount = {
    index,
    name: '',
    providerCode: '',
    type: 'api_key',
    status: 'active',
    credentials: {},
    messages: item.messages,
    warnings: item.warnings
  }
  if (!isRecord(value)) {
    item.action = 'failed'
    item.messages.push('账户配置必须是对象')
    return { source, item }
  }
  appendUnknownFieldMessages(value, importAccountKeys, '账户配置', item.messages)
  source.ref = optionalTextField(value, 'ref', '账户 ref', item.messages)
  source.name = optionalTextField(value, 'name', '账户名称', item.messages) ?? ''
  source.providerCode = optionalTextField(value, 'providerCode', '账户 providerCode', item.messages) ?? ''
  if (!source.providerCode) {
    item.messages.push('账户 providerCode 不能为空')
  }
  const typeInput = optionalTextField(value, 'type', '账户 type', item.messages)
  if (typeInput) {
    source.type = typeInput
  } else {
    item.messages.push('账户 type 不能为空')
  }
  const rawStatus = optionalTextField(value, 'status', '账户 status', item.messages)
  if (rawStatus !== undefined) {
    const normalizedStatus = normalizeStatus(rawStatus)
    if (normalizedStatus) {
      source.status = normalizedStatus
    } else {
      item.messages.push(`账户状态不支持：${rawStatus}`)
    }
  } else {
    item.messages.push('账户 status 不能为空')
  }
  source.groupId = optionalTextField(value, 'groupId', '账户 groupId', item.messages)
  source.groupName = optionalTextField(value, 'groupName', '账户 groupName', item.messages)
  source.proxyRef = optionalTextField(value, 'proxyRef', '账户 proxyRef', item.messages)
  source.proxyProfileId = optionalTextField(value, 'proxyProfileId', '账户 proxyProfileId', item.messages)
  source.concurrencyLimit = optionalPositiveIntegerField(value, 'concurrencyLimit', '账户 concurrencyLimit', item.messages)
  source.priority = optionalNonNegativeIntegerField(value, 'priority', '账户 priority', item.messages)
  source.superPriorityEnabled = optionalBooleanField(value, 'superPriorityEnabled', '账户 superPriorityEnabled', item.messages)
  source.fallbackEnabled = optionalBooleanField(value, 'fallbackEnabled', '账户 fallbackEnabled', item.messages)
  source.supportedModels = optionalStringArrayField(value, 'supportedModels', '账户 supportedModels', item.messages)
  source.accountExpiresAt = optionalDateTimeField(value, 'accountExpiresAt', '账户 accountExpiresAt', item.messages)
  const availabilityScheduleInput = importAvailabilityScheduleInput(value)
  source.availabilitySchedule = availabilityScheduleInput.present
    ? normalizeImportAvailabilitySchedule(availabilityScheduleInput.value, item.messages)
    : undefined
  source.notes = optionalTextField(value, 'notes', '账户 notes', item.messages)
  try {
    source.credentials = normalizeAccountCredentialsForWrite(source.type, value.credentials)
  } catch (error) {
    item.messages.push(errorMessage(error))
  }

  item.ref = source.ref
  item.name = source.name
  item.providerCode = source.providerCode
  item.accountType = source.type
  item.groupName = source.groupName
  item.groupId = source.groupId
  item.proxyRef = source.proxyRef

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
  if (account.concurrencyLimit !== undefined && account.concurrencyLimit < 1) {
    account.messages.push('concurrencyLimit 必须大于 0')
  }
  if (account.accountExpiresAt && !optionalServerDateTimeIso(account.accountExpiresAt)) {
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
    item.messages.push('账户 groupId 或 groupName 必填')
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

function createPlannedProxies(plan: ImportPlan, access: AccessScope): Map<string, string> {
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
      }, access)
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

function createPlannedGroups(plan: ImportPlan, access: AccessScope): void {
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
  if (!input) {
    throw new Error('代理 type 不能为空')
  }
  if (!isProxyType(input)) {
    throw new Error(`代理 type 不支持：${input}`)
  }
  return input
}

function isProxyType(value: string): value is NormalizedImportProxy['type'] {
  return value === 'http' || value === 'https' || value === 'socks5' || value === 'socks5h'
}

function isDuplicateAccountError(error: unknown): boolean {
  return error instanceof Error && error.message.includes('账户名称已存在')
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function hasOwnField(record: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(record, key)
}

function appendUnknownFieldMessages(
  record: Record<string, unknown>,
  allowedKeys: ReadonlySet<string>,
  label: string,
  messages: string[]
): void {
  const unknownKeys = Object.keys(record).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    messages.push(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

function optionalTextField(record: Record<string, unknown>, key: string, label: string, messages: string[]): string | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (typeof value !== 'string') {
    messages.push(`${label}必须是字符串`)
    return undefined
  }
  const input = value.trim()
  return input || undefined
}

function optionalBooleanField(record: Record<string, unknown>, key: string, label: string, messages: string[]): boolean | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (typeof value !== 'boolean') {
    messages.push(`${label}必须是布尔值`)
    return undefined
  }
  return value
}

function optionalIntegerField(record: Record<string, unknown>, key: string, label: string, messages: string[]): number | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    messages.push(`${label}必须是整数`)
    return undefined
  }
  return value
}

function optionalPositiveIntegerField(record: Record<string, unknown>, key: string, label: string, messages: string[]): number | undefined {
  const value = optionalIntegerField(record, key, label, messages)
  if (value === undefined) return undefined
  if (value <= 0) {
    messages.push(`${label}必须是大于 0 的整数`)
    return undefined
  }
  return value
}

function optionalNonNegativeIntegerField(record: Record<string, unknown>, key: string, label: string, messages: string[]): number | undefined {
  const value = optionalIntegerField(record, key, label, messages)
  if (value === undefined) return undefined
  if (value < 0) {
    messages.push(`${label}必须是大于等于 0 的整数`)
    return undefined
  }
  return value
}

function optionalStringArrayField(record: Record<string, unknown>, key: string, label: string, messages: string[]): string[] | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (!Array.isArray(value)) {
    messages.push(`${label}必须是非空字符串数组`)
    return undefined
  }
  const items: string[] = []
  for (const item of value) {
    if (typeof item !== 'string' || !item.trim()) {
      messages.push(`${label}必须是非空字符串数组`)
      return undefined
    }
    items.push(item.trim())
  }
  return items
}

function optionalDateTimeField(record: Record<string, unknown>, key: string, label: string, messages: string[]): string | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (typeof value !== 'string' || !value.trim()) {
    messages.push(`${label}必须是有效时间字符串`)
    return undefined
  }
  const normalized = optionalServerDateTimeIso(value)
  if (!normalized) {
    messages.push(`${label}必须是有效时间字符串`)
    return undefined
  }
  return normalized
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

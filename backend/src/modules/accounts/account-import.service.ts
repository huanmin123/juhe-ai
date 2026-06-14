import { type AccountAvailabilitySchedule, type AccountModelMapping, type AccountType } from '../../domain/types.js'
import { type AccessScope } from '../../storage/access-scope.js'
import {
  createAccount,
  createGroup,
  createProxy,
  listProviders,
  normalizeAccountCredentialsForWrite,
  normalizeAccountModelMappingsForProvider,
  normalizeAccountSupportedModelsForProvider
} from '../../storage/repositories.js'
import {
  appendUnknownFieldMessages,
  errorMessage,
  hasOwnField,
  importAccountKeys,
  importAvailabilityScheduleInput,
  importProxyKeys,
  importRootKeys,
  isRecord,
  normalizeImportAvailabilitySchedule,
  normalizeProxyType,
  normalizeStatus,
  optionalAccountTagsField,
  optionalBooleanField,
  optionalDateTimeField,
  optionalModelMappingsField,
  optionalNonNegativeIntegerField,
  optionalPositiveIntegerField,
  optionalStringArrayField,
  optionalTextField,
  type AccountImportProxyType,
  type AccountImportStatus
} from './account-import-field-parser.js'
import {
  accountImportGroupKey,
  buildAccountImportSummary,
  markDuplicateAccountImportItems,
  type AccountImportGroupCreateMap,
  type AccountImportGroupCreatePlan
} from './account-import-plan.js'
import {
  canCreateImportProxy,
  findGroupOptionByName,
  findProxyByName,
  findProxyOptionByName,
  importTargetSystemAccountId,
  resolveAccountGroup,
  resolveAccountProxy,
  type AccountImportProxyReferencePlan,
  type AccountImportResourceContext
} from './account-import-resource-resolver.js'
import {
  validateImportAccountProviderAndBasics,
  type AccountImportProviderContext
} from './account-import-provider-resolver.js'

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

interface NormalizedImportAccount {
  index: number
  ref?: string
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
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
  modelMappings?: AccountModelMapping[]
  tags?: string[]
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
  type: AccountImportProxyType
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
        providerProtocolProfileId: account.source.providerProtocolProfileId,
        name: account.source.name,
        type: account.source.type,
        status: accountImportCreateStatus(account.source.status),
        credentials: account.source.credentials
      }
      if (groupId !== undefined) accountInput.groupId = groupId
      if (proxyProfileId !== undefined) accountInput.proxyProfileId = proxyProfileId
      if (account.source.concurrencyLimit !== undefined) accountInput.concurrencyLimit = account.source.concurrencyLimit
      if (account.source.priority !== undefined) accountInput.priority = account.source.priority
      if (account.source.superPriorityEnabled !== undefined) accountInput.superPriorityEnabled = account.source.superPriorityEnabled
      if (account.source.fallbackEnabled !== undefined) accountInput.fallbackEnabled = account.source.fallbackEnabled
      if (account.source.supportedModels !== undefined) accountInput.supportedModels = account.source.supportedModels
      if (account.source.modelMappings !== undefined) accountInput.modelMappings = account.source.modelMappings
      if (account.source.tags !== undefined) accountInput.tags = account.source.tags
      if (account.source.accountExpiresAt !== undefined) accountInput.accountExpiresAt = account.source.accountExpiresAt
      if (account.source.availabilitySchedule !== undefined) accountInput.availabilitySchedule = account.source.availabilitySchedule
      if (account.source.notes !== undefined) accountInput.notes = account.source.notes
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
    targetSystemAccountId: importTargetSystemAccountId(access),
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
  const proxyByRef = new Map<string, AccountImportProxyReferencePlan>()
  for (const proxy of proxyPlans) {
    if (proxy.source.ref && !proxyByRef.has(proxy.source.ref)) {
      proxyByRef.set(proxy.source.ref, proxy)
    } else if (proxy.source.ref) {
      proxy.item.action = 'failed'
      proxy.item.messages.push(`代理 ref 重复：${proxy.source.ref}`)
    }
  }

  const groupIdsByKey = new Map<string, string>()
  const groupNamesToCreate = new Map<string, AccountImportGroupCreatePlan>()
  const accounts = rawAccounts.map((item, index) => planAccount(item, index + 1, context, proxyByRef, groupIdsByKey, groupNamesToCreate))
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
  const existing = findProxyOptionByName(source.name, context)
  if (existing) {
    item.action = 'reuse'
    item.proxyProfileId = existing.id
  } else if (!canCreateImportProxy(context, item)) {
    item.action = item.messages.length > 0 ? 'failed' : 'skip'
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
  proxyByRef: Map<string, AccountImportProxyReferencePlan>,
  groupIdsByKey: Map<string, string>,
  groupNamesToCreate: AccountImportGroupCreateMap
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
  source.providerProtocolProfileId = optionalTextField(value, 'providerProtocolProfileId', '账户 providerProtocolProfileId', item.messages)
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
  source.modelMappings = optionalModelMappingsField(value, 'modelMappings', '账户 modelMappings', item.messages)
  source.tags = optionalAccountTagsField(value, 'tags', '账户 tags', item.messages)
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
  item.groupName = source.groupName
  item.groupId = source.groupId
  item.proxyRef = source.proxyRef

  validateImportAccountProviderAndBasics(source, context)
  item.providerProtocolProfileId = source.providerProtocolProfileId
  item.protocolCode = source.protocolCode
  item.protocolVersion = source.protocolVersion
  item.accountType = source.type
  validateAccountModelCatalogFields(source, context)
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

function validateAccountModelCatalogFields(account: NormalizedImportAccount, context: ImportContext): void {
  if (!account.providerCode || !context.providerByCode.has(account.providerCode) || !context.targetSystemAccountId) {
    return
  }
  try {
    account.supportedModels = normalizeAccountSupportedModelsForProvider(account.supportedModels, account.providerCode, context.targetSystemAccountId)
    account.modelMappings = normalizeAccountModelMappingsForProvider(account.modelMappings, account.providerCode, context.targetSystemAccountId)
  } catch (error) {
    account.messages.push(errorMessage(error))
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
        providerProtocolProfileId: group.providerProtocolProfileId,
        name: group.name,
        description: '由账户导入自动创建'
      }, access)
      plan.groupIdsByKey.set(key, created.id)
    } catch (error) {
      const existing = findGroupOptionByName(group.providerCode, group.providerProtocolProfileId, group.name, {
        access,
        options: defaultAccountImportOptions(),
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
        if (account.source.groupName && account.source.providerProtocolProfileId && accountImportGroupKey(account.source.providerProtocolProfileId, account.source.groupName) === key && account.item.action === 'create') {
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
  if (!account.providerProtocolProfileId) return undefined
  return plan.groupIdsByKey.get(accountImportGroupKey(account.providerProtocolProfileId, account.groupName))
}

function accountImportCreateStatus(status: AccountImportStatus): AccountImportStatus {
  return status === 'active' ? 'pending_test' : status
}

function isDuplicateAccountError(error: unknown): boolean {
  return error instanceof Error && error.message.includes('账户名称已存在')
}

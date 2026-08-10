import { parse as parseYaml } from 'yaml'

import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { assertSafeUpstreamBaseUrl } from '../../shared/upstream-url-policy.js'

export const accountImportSourceModes = ['native', 'sub2api', 'newapi', 'cpa', 'oneapi'] as const
export type AccountImportSourceMode = typeof accountImportSourceModes[number]

export interface AccountImportSourceSummary {
  mode: AccountImportSourceMode
  records: number
  accepted: number
  skipped: number
  ignoredFields: number
  messages: string[]
}

export interface AccountImportSourceAdaptation {
  data: unknown
  source: AccountImportSourceSummary
}

const maxImportedAccounts = 50
const maxImportedProxies = 20
const defaultOpenAIBaseUrl = 'https://api.openai.com/v1'

interface AdapterState {
  source: AccountImportSourceSummary
  accounts: Record<string, unknown>[]
  proxies: Record<string, unknown>[]
  proxyRefByKey: Map<string, string>
  unavailableProxyKeys: Set<string>
}

export function adaptAccountImportSource(data: unknown, mode: AccountImportSourceMode): AccountImportSourceAdaptation {
  if (mode === 'native') {
    return {
      data,
      source: emptySourceSummary(mode)
    }
  }

  const state = createAdapterState(mode)
  try {
    if (mode === 'sub2api') {
      adaptSub2Api(data, state)
    } else if (mode === 'newapi' || mode === 'oneapi') {
      adaptChannelSource(data, mode, state)
    } else {
      adaptCliProxyApi(data, state)
    }
  } catch (error) {
    addSourceMessage(state, error instanceof Error ? error.message : '来源数据解析失败')
  }

  if (state.source.records === 0 && state.source.messages.length === 0) {
    addSourceMessage(state, '未识别到可导入的来源账户记录')
  }
  if (state.source.skipped > sourceMessageRecordLimit) {
    addSourceMessage(state, `另有 ${state.source.skipped - sourceMessageRecordLimit} 条来源记录未逐项展开`)
  }

  return {
    data: {
      type: 'juhe-ai-account-import',
      version: 1,
      proxies: state.proxies,
      accounts: state.accounts
    },
    source: state.source
  }
}

function emptySourceSummary(mode: AccountImportSourceMode): AccountImportSourceSummary {
  return {
    mode,
    records: 0,
    accepted: 0,
    skipped: 0,
    ignoredFields: 0,
    messages: []
  }
}

function createAdapterState(mode: AccountImportSourceMode): AdapterState {
  return {
    source: emptySourceSummary(mode),
    accounts: [],
    proxies: [],
    proxyRefByKey: new Map(),
    unavailableProxyKeys: new Set()
  }
}

function adaptSub2Api(input: unknown, state: AdapterState): void {
  const root = requireSourceRecord(parseJsonInput(input, 'Sub2API'))
  const data = isRecord(root.data) ? root.data : root
  countIgnoredRecordKeys(data, new Set(['type', 'version', 'exported_at', 'proxies', 'accounts', 'skipped_shadows']), state)

  const proxies = arrayValue(data.proxies)
  for (let index = 0; index < proxies.length; index += 1) {
    adaptSub2ApiProxy(proxies[index], index + 1, state)
  }

  const accounts = arrayValue(data.accounts)
  if (!accounts.length) {
    addSourceMessage(state, 'Sub2API 数据未包含 accounts 数组')
    return
  }
  for (let index = 0; index < accounts.length; index += 1) {
    adaptSub2ApiAccount(accounts[index], index + 1, state)
  }
}

function adaptSub2ApiProxy(value: unknown, index: number, state: AdapterState): void {
  if (!isRecord(value)) {
    state.source.ignoredFields += 1
    return
  }
  countIgnoredRecordKeys(value, new Set([
    'proxy_key', 'name', 'protocol', 'host', 'port', 'username', 'password', 'status'
  ]), state)
  const proxyKey = text(value.proxy_key)
  const protocol = normalizeProxyType(value.protocol)
  const host = text(value.host)
  const port = positiveInteger(value.port)
  const active = normalizeSourceStatus(value.status) !== 'disabled'
  if (!proxyKey || !protocol || !host || !port || !active) {
    if (proxyKey) state.unavailableProxyKeys.add(proxyKey)
    return
  }
  if (state.proxies.length >= maxImportedProxies) {
    state.unavailableProxyKeys.add(proxyKey)
    return
  }
  const ref = `sub2api-proxy-${index}`
  state.proxyRefByKey.set(proxyKey, ref)
  state.proxies.push(compactRecord({
    ref,
    name: text(value.name) || `Sub2API 代理 ${index}`,
    type: protocol,
    host,
    port,
    username: text(value.username) || undefined,
    password: text(value.password) || undefined,
    enabled: true
  }))
}

function adaptSub2ApiAccount(value: unknown, index: number, state: AdapterState): void {
  state.source.records += 1
  if (!isRecord(value)) {
    skipSourceRecord(state, index, '账户不是对象')
    return
  }
  countIgnoredRecordKeys(value, new Set([
    'name', 'notes', 'platform', 'type', 'credentials', 'proxy_key', 'concurrency', 'priority', 'expires_at'
  ]), state)
  if (!isOpenAiSourcePlatform(value.platform)) {
    skipSourceRecord(state, index, '只支持 OpenAI 平台账户')
    return
  }
  const sourceType = normalizeSub2ApiAccountType(value.type)
  if (!sourceType) {
    skipSourceRecord(state, index, '账户类型不是可导入的 API Key 或 OAuth')
    return
  }
  const credentials = isRecord(value.credentials) ? value.credentials : undefined
  if (!credentials) {
    skipSourceRecord(state, index, '缺少 credentials')
    return
  }
  const proxyRef = resolveSub2ApiProxyRef(value.proxy_key, index, state)
  if (proxyRef === null) return
  const account = sourceType === 'api_key'
    ? adaptApiKeyAccount({
        credentials,
        name: text(value.name) || `Sub2API API Key ${index}`,
        groupName: 'Sub2API 导入',
        status: 'active',
        proxyRef,
        concurrencyLimit: positiveInteger(value.concurrency),
        priority: nonNegativeInteger(value.priority),
        notes: text(value.notes) || undefined,
        accountExpiresAt: isoDate(value.expires_at)
      }, state)
    : adaptOAuthAccount({
        credentials,
        name: text(value.name) || `Sub2API OAuth ${index}`,
        groupName: 'Sub2API 导入',
        status: 'active',
        proxyRef,
        concurrencyLimit: positiveInteger(value.concurrency),
        priority: nonNegativeInteger(value.priority),
        notes: text(value.notes) || undefined,
        accountExpiresAt: isoDate(value.expires_at)
      }, state)
  if (!account) {
    skipSourceRecord(state, index, sourceType === 'api_key' ? '缺少可用 API Key 或 Base URL' : '缺少可用 OAuth 凭据或 Base URL')
    return
  }
  acceptAccount(state, index, account)
}

function resolveSub2ApiProxyRef(value: unknown, index: number, state: AdapterState): string | undefined | null {
  const proxyKey = text(value)
  if (!proxyKey) return undefined
  const proxyRef = state.proxyRefByKey.get(proxyKey)
  if (proxyRef) return proxyRef
  if (state.unavailableProxyKeys.has(proxyKey)) {
    skipSourceRecord(state, index, '引用的来源代理不可用或超过本次导入上限')
    return null
  }
  skipSourceRecord(state, index, '引用的来源代理不存在')
  return null
}

function adaptChannelSource(input: unknown, mode: 'newapi' | 'oneapi', state: AdapterState): void {
  const records = extractChannelRecords(parseJsonInput(input, mode === 'newapi' ? 'NewAPI' : 'One-API'))
  if (!records.length) {
    addSourceMessage(state, `${sourceLabel(mode)} 数据未包含 Channel 记录`)
    return
  }
  for (let index = 0; index < records.length; index += 1) {
    state.source.records += 1
    const record = records[index]
    if (!isRecord(record)) {
      skipSourceRecord(state, index + 1, 'Channel 不是对象')
      continue
    }
    countIgnoredRecordKeys(record, new Set(['id', 'type', 'key', 'base_url', 'name', 'group', 'status']), state)
    if (!isOpenAiChannel(record.type, mode)) {
      skipSourceRecord(state, index + 1, 'Channel 不是该来源定义的 OpenAI 类型')
      continue
    }
    const apiKeys = apiKeyList(record.key)
    if (!apiKeys.length || apiKeys.length > 10) {
      skipSourceRecord(state, index + 1, !apiKeys.length ? 'Channel 缺少 API Key' : 'Channel API Key 数量超过 10 条')
      continue
    }
    const baseUrl = safeBaseUrl(text(record.base_url) || defaultOpenAIBaseUrl, state)
    if (!baseUrl) {
      skipSourceRecord(state, index + 1, 'Channel Base URL 不符合上游地址策略')
      continue
    }
    const account = buildApiKeyAccount({
      apiKeys,
      baseUrl,
      name: text(record.name) || `${sourceLabel(mode)} Channel ${index + 1}`,
      groupName: sourceGroupName(record.group, `${sourceLabel(mode)} 导入`),
      status: normalizeChannelStatus(record.status),
      notes: undefined
    })
    acceptAccount(state, index + 1, account)
  }
}

function adaptCliProxyApi(input: unknown, state: AdapterState): void {
  const parsed = parseCpaInput(input)
  const root = requireSourceRecord(parsed)
  const sourceType = firstText(
    root.type,
    isRecord(root.metadata) ? root.metadata.type : undefined,
    root.provider
  )?.toLowerCase()
  if (sourceType === 'codex') {
    adaptCpaCodexAuthFile(root, state)
    return
  }

  countIgnoredRecordKeys(root, new Set(['codex-api-key', 'codex_api_key', 'openai-compatibility', 'openai_compatibility']), state)
  const codexEntries = arrayValue(root['codex-api-key'] ?? root.codex_api_key)
  for (let index = 0; index < codexEntries.length; index += 1) {
    adaptCpaApiKeyEntry(codexEntries[index], {
      index: index + 1,
      label: 'CPA Codex API Key',
      groupName: 'CPA 导入'
    }, state)
  }

  const providers = arrayValue(root['openai-compatibility'] ?? root.openai_compatibility)
  for (let providerIndex = 0; providerIndex < providers.length; providerIndex += 1) {
    const provider = providers[providerIndex]
    if (!isRecord(provider)) {
      state.source.ignoredFields += 1
      continue
    }
    countIgnoredRecordKeys(provider, new Set(['name', 'base-url', 'base_url', 'api-key-entries', 'api_key_entries']), state)
    const providerName = text(provider.name) || `CPA OpenAI Provider ${providerIndex + 1}`
    const providerBaseUrl = firstText(provider['base-url'], provider.base_url)
    const entries = arrayValue(provider['api-key-entries'] ?? provider.api_key_entries)
    for (let entryIndex = 0; entryIndex < entries.length; entryIndex += 1) {
      adaptCpaApiKeyEntry(entries[entryIndex], {
        index: entryIndex + 1,
        label: providerName,
        groupName: 'CPA 导入',
        baseUrl: providerBaseUrl
      }, state)
    }
  }

  if (state.source.records === 0) {
    addSourceMessage(state, 'CPA 配置未包含 codex-api-key 或 openai-compatibility API Key')
  }
}

function adaptCpaApiKeyEntry(
  value: unknown,
  input: { index: number, label: string, groupName: string, baseUrl?: string },
  state: AdapterState
): void {
  state.source.records += 1
  const record = isRecord(value) ? value : undefined
  if (record) {
    countIgnoredRecordKeys(record, new Set(['api-key', 'api_key', 'key', 'base-url', 'base_url']), state)
  }
  const apiKeys = apiKeyList(record ? record['api-key'] ?? record.api_key ?? record.key : value)
  if (!apiKeys.length || apiKeys.length > 10) {
    skipSourceRecord(state, input.index, !apiKeys.length ? '缺少 API Key' : 'API Key 数量超过 10 条')
    return
  }
  const baseUrl = safeBaseUrl(firstText(record?.['base-url'], record?.base_url, input.baseUrl) || defaultOpenAIBaseUrl, state)
  if (!baseUrl) {
    skipSourceRecord(state, input.index, 'API Key Base URL 不符合上游地址策略')
    return
  }
  acceptAccount(state, input.index, buildApiKeyAccount({
    apiKeys,
    baseUrl,
    name: `${input.label} ${input.index}`,
    groupName: input.groupName,
    status: 'active'
  }))
}

function adaptCpaCodexAuthFile(root: Record<string, unknown>, state: AdapterState): void {
  state.source.records += 1
  const tokens = isRecord(root.token_data)
    ? { ...root.token_data, ...root }
    : root
  countIgnoredRecordKeys(root, new Set([
    'type', 'provider', 'metadata', 'token_data', 'access_token', 'accessToken', 'refresh_token', 'refreshToken',
    'id_token', 'idToken', 'email', 'account_id', 'accountId', 'chatgpt_account_id', 'expires_at', 'expiresAt', 'name', 'id'
  ]), state)
  const credentials = oauthCredentials(tokens, state)
  if (!credentials) {
    skipSourceRecord(state, 1, 'Codex auth-file 缺少可用 OAuth 凭据')
    return
  }
  const name = firstText(tokens.email, root.email, root.name, root.id) || 'CPA Codex OAuth'
  acceptAccount(state, 1, compactRecord({
    name,
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'oauth',
    status: 'active',
    groupName: 'CPA 导入',
    credentials
  }))
}

function adaptApiKeyAccount(input: {
  credentials: Record<string, unknown>
  name: string
  groupName: string
  status: 'active' | 'disabled'
  proxyRef?: string
  concurrencyLimit?: number
  priority?: number
  notes?: string
  accountExpiresAt?: string
}, state: AdapterState): Record<string, unknown> | undefined {
  countIgnoredRecordKeys(input.credentials, new Set(['api_key', 'api_keys', 'key', 'base_url']), state)
  const apiKeys = apiKeyList(input.credentials.api_key ?? input.credentials.api_keys ?? input.credentials.key)
  if (!apiKeys.length || apiKeys.length > 10) return undefined
  const baseUrl = safeBaseUrl(text(input.credentials.base_url) || defaultOpenAIBaseUrl, state)
  if (!baseUrl) return undefined
  return buildApiKeyAccount({
    apiKeys,
    baseUrl,
    name: input.name,
    groupName: input.groupName,
    status: input.status,
    proxyRef: input.proxyRef,
    concurrencyLimit: input.concurrencyLimit,
    priority: input.priority,
    notes: input.notes,
    accountExpiresAt: input.accountExpiresAt
  })
}

function adaptOAuthAccount(input: {
  credentials: Record<string, unknown>
  name: string
  groupName: string
  status: 'active' | 'disabled'
  proxyRef?: string
  concurrencyLimit?: number
  priority?: number
  notes?: string
  accountExpiresAt?: string
}, state: AdapterState): Record<string, unknown> | undefined {
  const credentials = oauthCredentials(input.credentials, state)
  if (!credentials) return undefined
  return compactRecord({
    name: input.name,
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'oauth',
    status: input.status,
    groupName: input.groupName,
    proxyRef: input.proxyRef,
    concurrencyLimit: input.concurrencyLimit,
    priority: input.priority,
    accountExpiresAt: input.accountExpiresAt,
    notes: input.notes,
    credentials
  })
}

function buildApiKeyAccount(input: {
  apiKeys: string[]
  baseUrl: string
  name: string
  groupName: string
  status: 'active' | 'disabled'
  proxyRef?: string
  concurrencyLimit?: number
  priority?: number
  notes?: string
  accountExpiresAt?: string
}): Record<string, unknown> {
  return compactRecord({
    name: input.name,
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: input.status,
    groupName: input.groupName,
    proxyRef: input.proxyRef,
    concurrencyLimit: input.concurrencyLimit,
    priority: input.priority,
    accountExpiresAt: input.accountExpiresAt,
    notes: input.notes,
    credentials: compactRecord({
      api_key: input.apiKeys[0],
      api_keys: input.apiKeys.length > 1 ? input.apiKeys : undefined,
      api_key_strategy: input.apiKeys.length > 1 ? 'failover' : undefined,
      base_url: input.baseUrl
    })
  })
}

function oauthCredentials(value: Record<string, unknown>, state: AdapterState): Record<string, unknown> | undefined {
  countIgnoredRecordKeys(value, new Set([
    'access_token', 'accessToken', 'refresh_token', 'refreshToken', 'expires_at', 'expiresAt', 'client_id', 'clientId',
    'id_token', 'idToken', 'token_type', 'tokenType', 'scope', 'email', 'account_id', 'accountId', 'chatgpt_account_id',
    'chatgptAccountId', 'chatgpt_user_id', 'chatgptUserId', 'plan_type', 'planType', 'organization_id', 'organizationId', 'base_url'
  ]), state)
  const refreshToken = firstText(value.refresh_token, value.refreshToken)
  let accessToken = firstText(value.access_token, value.accessToken)
  const accountId = firstText(value.account_id, value.accountId, value.chatgpt_account_id, value.chatgptAccountId)
  if (!refreshToken && !accessToken) return undefined
  if (accessToken && !accountId && refreshToken) {
    accessToken = undefined
    state.source.ignoredFields += 1
  }
  if (accessToken && !accountId) return undefined
  const baseUrl = safeBaseUrl(text(value.base_url) || defaultOpenAIBaseUrl, state)
  if (!baseUrl) return undefined
  return compactRecord({
    refresh_token: refreshToken,
    access_token: accessToken,
    expires_at: isoDate(value.expires_at ?? value.expiresAt),
    client_id: firstText(value.client_id, value.clientId),
    id_token: firstText(value.id_token, value.idToken),
    token_type: firstText(value.token_type, value.tokenType),
    scope: text(value.scope) || undefined,
    email: text(value.email) || undefined,
    account_id: accountId,
    organization_id: firstText(value.organization_id, value.organizationId),
    chatgpt_user_id: firstText(value.chatgpt_user_id, value.chatgptUserId),
    plan_type: firstText(value.plan_type, value.planType),
    base_url: baseUrl
  })
}

function acceptAccount(state: AdapterState, index: number, account: Record<string, unknown>): void {
  if (state.accounts.length >= maxImportedAccounts) {
    skipSourceRecord(state, index, `超过单次最多 ${maxImportedAccounts} 条账户的限制`)
    return
  }
  state.accounts.push(account)
  state.source.accepted += 1
}

const sourceMessageRecordLimit = 8

function skipSourceRecord(state: AdapterState, index: number, reason: string): void {
  state.source.skipped += 1
  if (state.source.skipped <= sourceMessageRecordLimit) {
    addSourceMessage(state, `第 ${index} 条来源记录已跳过：${reason}`)
  }
}

function addSourceMessage(state: AdapterState, message: string): void {
  if (!state.source.messages.includes(message)) state.source.messages.push(message)
}

function parseJsonInput(value: unknown, label: string): unknown {
  if (typeof value !== 'string') return value
  try {
    return JSON.parse(value)
  } catch {
    throw new Error(`${label} 导入内容必须是 JSON`)
  }
}

function parseCpaInput(value: unknown): unknown {
  if (typeof value !== 'string') return value
  try {
    return parseYaml(value)
  } catch {
    throw new Error('CPA 导入内容必须是有效 YAML 或 JSON')
  }
}

function requireSourceRecord(value: unknown): Record<string, unknown> {
  if (!isRecord(value)) throw new Error('来源导入内容必须是对象')
  return value
}

function extractChannelRecords(value: unknown): unknown[] {
  if (Array.isArray(value)) return value
  if (!isRecord(value)) return []
  if (Array.isArray(value.items)) return value.items
  if (Array.isArray(value.channels)) return value.channels
  if (isRecord(value.data)) return extractChannelRecords(value.data)
  if (Array.isArray(value.data)) return value.data
  return [value]
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function firstText(...values: unknown[]): string | undefined {
  for (const value of values) {
    const output = text(value)
    if (output) return output
  }
  return undefined
}

function positiveInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : undefined
}

function nonNegativeInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : undefined
}

function compactRecord(value: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined))
}

function safeBaseUrl(value: string, state: AdapterState): string | undefined {
  try {
    assertSafeUpstreamBaseUrl(value)
    return value
  } catch {
    state.source.ignoredFields += 1
    return undefined
  }
}

function countIgnoredRecordKeys(record: Record<string, unknown>, acceptedKeys: ReadonlySet<string>, state: AdapterState): void {
  for (const key of Object.keys(record)) {
    if (!acceptedKeys.has(key)) state.source.ignoredFields += 1
  }
}

function normalizeProxyType(value: unknown): 'http' | 'https' | 'socks5' | 'socks5h' | undefined {
  const normalized = text(value).toLowerCase()
  if (normalized === 'http' || normalized === 'https' || normalized === 'socks5' || normalized === 'socks5h') return normalized
  return undefined
}

function normalizeSub2ApiAccountType(value: unknown): 'api_key' | 'oauth' | undefined {
  const normalized = text(value).toLowerCase()
  if (normalized === 'apikey' || normalized === 'api_key') return 'api_key'
  return normalized === 'oauth' ? 'oauth' : undefined
}

function isOpenAiSourcePlatform(value: unknown): boolean {
  const normalized = text(value).toLowerCase()
  return normalized === 'openai' || normalized === 'gpt' || normalized === 'chatgpt'
}

function isOpenAiChannel(value: unknown, mode: 'newapi' | 'oneapi'): boolean {
  if (value === 1) return true
  const normalized = text(value).toLowerCase().replace(/[ _-]+/g, '')
  return normalized === 'openai' || normalized === 'openaicompatible'
}

function normalizeSourceStatus(value: unknown): 'active' | 'disabled' {
  if (value === 0 || value === false) return 'disabled'
  const normalized = text(value).toLowerCase()
  return normalized === 'disabled' || normalized === 'inactive' || normalized === 'banned' ? 'disabled' : 'active'
}

function normalizeChannelStatus(value: unknown): 'active' | 'disabled' {
  if (value === 1) return 'active'
  const normalized = text(value).toLowerCase()
  return normalized === '1' || normalized === 'active' || normalized === 'enabled' ? 'active' : 'disabled'
}

function sourceGroupName(value: unknown, fallback: string): string {
  const normalized = text(value)
  if (!normalized || /[,;\n\r]/.test(normalized)) return fallback
  return normalized
}

function isoDate(value: unknown): string | undefined {
  if (typeof value === 'string' && value.trim()) return value.trim()
  if (typeof value !== 'number' || !Number.isFinite(value)) return undefined
  const timestamp = value > 10_000_000_000 ? value : value * 1000
  const date = new Date(timestamp)
  return Number.isNaN(date.valueOf()) ? undefined : date.toISOString()
}

function apiKeyList(value: unknown): string[] {
  const input = Array.isArray(value) ? value : [value]
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of input) {
    if (typeof item !== 'string') continue
    const raw = item.trim()
    if (!raw) continue
    if (isMaskedApiKey(raw)) continue
    if (raw.startsWith('[')) {
      try {
        for (const nested of apiKeyList(JSON.parse(raw))) addApiKey(output, seen, nested)
        continue
      } catch {
      }
    }
    for (const key of raw.split(/\r?\n/)) addApiKey(output, seen, key)
  }
  return output
}

function isMaskedApiKey(value: string): boolean {
  const normalized = value.toLowerCase()
  return normalized.includes('***')
    || normalized.includes('…')
    || normalized.includes('...')
    || normalized === '<redacted>'
    || normalized === '[redacted]'
    || normalized === 'masked'
}

function addApiKey(output: string[], seen: Set<string>, value: string): void {
  const key = value.trim()
  if (!key || seen.has(key)) return
  seen.add(key)
  output.push(key)
}

function sourceLabel(mode: 'newapi' | 'oneapi'): string {
  return mode === 'newapi' ? 'NewAPI' : 'One-API'
}

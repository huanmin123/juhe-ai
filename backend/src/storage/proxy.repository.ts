import { decryptJson, encryptJson } from './crypto.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { chunkValues, normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { optionalString } from './value-utils.js'
import { currentSystemAccountId, type AccessScope } from './access-scope.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

const proxyTypeValues = ['http', 'https', 'socks5', 'socks5h'] as const
const proxyTestStatusValues = ['unknown', 'passed', 'warning', 'failed'] as const
const proxyInputKeys = new Set(['name', 'description', 'type', 'host', 'port', 'username', 'password', 'enabled'])
const proxyUsagePreviewLimit = 3
const proxyUsageWindowLimit = proxyUsagePreviewLimit + 1
const businessSchemaName = 'juhe_business'
export type ProxyProfileTestStatus = typeof proxyTestStatusValues[number]

interface ProxyRow {
  id: string
  name: string
  description: string | null
  type: string
  host: string
  port: number
  username: string | null
  password_encrypted?: string | null
  enabled: number
  test_status: string
  latency_ms?: number | null
  outbound_ip?: string | null
  outbound_region?: string | null
  last_test_message?: string | null
  last_tested_at: string | null
}

export interface ProxyProfileSummary {
  id: string
  name: string
  description?: string
  type: string
  host: string
  port: number
  username?: string
  enabled: boolean
  testStatus: ProxyProfileTestStatus
  latencyMs?: number
  outboundIp?: string
  outboundRegion?: string
  lastTestMessage?: string
  lastTestedAt?: string
}

export type ProxyProfileOptionSummary = Pick<ProxyProfileSummary, 'id' | 'name' | 'type' | 'enabled'>

export interface ProxyProfileListOptions {
  page?: number
  pageSize?: number
  keyword?: string
}

export interface ProxyProfileOptionListOptions {
  keyword?: string
  limit?: number
}

export interface ProxyProfileListResult {
  items: ProxyProfileSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface ProxyProfileTestConfig extends ProxyProfileSummary {
  proxyUrl: string
}

export interface ProxyProfileUrlResolution {
  proxyUrl?: string
  unavailable?: boolean
  errorMessage?: string
}

export class ProxyInUseError extends Error {
  constructor(readonly accountCount: number, readonly accountNames: string[], readonly accountCountIsLowerBound = false) {
    const names = accountNames.length > 0
      ? `：${accountNames.join('、')}${accountCountIsLowerBound || accountCount > accountNames.length ? ' 等' : ''}`
      : ''
    const countText = accountCountIsLowerBound ? `至少 ${accountCount}` : String(accountCount)
    super(`这个代理仍被 ${countText} 个账户使用，请先在账户管理中解绑或改绑后再删除${names}`)
    this.name = 'ProxyInUseError'
  }
}

export class ProxyProfileUnavailableError extends Error {
  constructor(readonly proxyProfileId: string) {
    super('代理不存在或已停用，请选择一个已启用的代理')
    this.name = 'ProxyProfileUnavailableError'
  }
}

export function listProxies(): ProxyProfileSummary[] {
  return listProxiesReadOnly()
}

export function listProxiesReadOnly(): ProxyProfileSummary[] {
  return queryProxies().items
}

export async function listProxiesAsync(): Promise<ProxyProfileSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_proxies_read_only'
      })
    }
    return listProxiesReadOnly()
  }
  return (await queryProxiesAsync()).items
}

export function listProxiesPage(options: ProxyProfileListOptions = {}): ProxyProfileListResult {
  return listProxiesPageReadOnly(options)
}

export function listProxiesPageReadOnly(options: ProxyProfileListOptions = {}): ProxyProfileListResult {
  return queryProxies(options, true)
}

export async function listProxiesPageAsync(options: ProxyProfileListOptions = {}): Promise<ProxyProfileListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_proxies_page_read_only',
        options
      })
    }
    return listProxiesPageReadOnly(options)
  }
  return await queryProxiesAsync(options, true)
}

export function findProxy(id: string): ProxyProfileSummary | undefined {
  return findProxyReadOnly(id)
}

export function findProxyReadOnly(id: string): ProxyProfileSummary | undefined {
  const row = getBusinessDatabase().prepare(`SELECT ${proxySummarySelectColumns()} FROM proxy_profiles WHERE id = ?`).get(id) as unknown as ProxyRow | undefined
  return row ? proxySummaryFromRow(row) : undefined
}

export async function findProxyAsync(id: string): Promise<ProxyProfileSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'find_proxy_read_only',
        id
      })
    }
    return findProxyReadOnly(id)
  }
  const client = await getProxyDatabaseClient()
  const row = await client.one<ProxyRow>(`SELECT ${proxySummarySelectColumns()} FROM ${proxyProfilesTable(client)} WHERE id = ?`, [id])
  return row ? proxySummaryFromRow(row) : undefined
}

export function listProxyOptions(options: ProxyProfileOptionListOptions = {}): ProxyProfileOptionSummary[] {
  return listProxyOptionsReadOnly(options)
}

export function listProxyOptionsReadOnly(options: ProxyProfileOptionListOptions = {}): ProxyProfileOptionSummary[] {
  const keywordFilter = buildProxyKeywordFilter(options.keyword)
  const safeLimit = typeof options.limit === 'number' && Number.isInteger(options.limit)
    ? Math.min(50, Math.max(1, options.limit))
    : 50
  const rows = getBusinessDatabase()
    .prepare(`SELECT id, name, type, enabled FROM proxy_profiles WHERE enabled = 1${keywordFilter.clause ? ` AND ${keywordFilter.clause}` : ''} ORDER BY name ASC, updated_at DESC, id ASC LIMIT ?`)
    .all(...keywordFilter.params, safeLimit) as unknown as Array<Pick<ProxyRow, 'id' | 'name' | 'type' | 'enabled'>>
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    type: row.type,
    enabled: row.enabled === 1
  }))
}

export async function listProxyOptionsAsync(options: ProxyProfileOptionListOptions = {}): Promise<ProxyProfileOptionSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_proxy_options_read_only',
        options
      })
    }
    return listProxyOptionsReadOnly(options)
  }
  const client = await getProxyDatabaseClient()
  const keywordFilter = buildProxyKeywordFilterAsync(options.keyword)
  const safeLimit = typeof options.limit === 'number' && Number.isInteger(options.limit)
    ? Math.min(50, Math.max(1, options.limit))
    : 50
  const rows = await client.query<Array<Pick<ProxyRow, 'id' | 'name' | 'type' | 'enabled'>>[number]>(
    `SELECT id, name, type, enabled
     FROM ${proxyProfilesTable(client)}
     WHERE enabled = 1${keywordFilter.clause ? ` AND ${keywordFilter.clause}` : ''}
     ORDER BY name ASC, updated_at DESC, id ASC
     LIMIT ?`,
    [...keywordFilter.params, safeLimit]
  )
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    type: row.type,
    enabled: row.enabled === 1
  }))
}

function queryProxies(options: ProxyProfileListOptions = {}, paged = false): ProxyProfileListResult {
  const normalized = normalizeProxyListOptions(options)
  const keywordFilter = buildProxyKeywordFilter(normalized.keyword)
  const whereClause = keywordFilter.clause ? `WHERE ${keywordFilter.clause}` : ''
  const pageClause = paged ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = paged ? [normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize] : []
  const rows = getBusinessDatabase()
    .prepare(`SELECT ${proxySummarySelectColumns()} FROM proxy_profiles ${whereClause} ORDER BY updated_at DESC, id DESC${pageClause}`)
    .all(...keywordFilter.params, ...pageParams) as unknown as ProxyRow[]
  const pageRows = paged ? takePageRows(rows, normalized.pageSize) : { rows, hasMore: false }
  const items = pageRows.rows.map(proxySummaryFromRow)
  return {
    items,
    total: paged ? pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore) : items.length,
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

async function queryProxiesAsync(options: ProxyProfileListOptions = {}, paged = false): Promise<ProxyProfileListResult> {
  const normalized = normalizeProxyListOptions(options)
  const keywordFilter = buildProxyKeywordFilterAsync(normalized.keyword)
  const whereClause = keywordFilter.clause ? `WHERE ${keywordFilter.clause}` : ''
  const pageClause = paged ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = paged ? [normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize] : []
  const client = await getProxyDatabaseClient()
  const rows = await client.query<ProxyRow>(
    `SELECT ${proxySummarySelectColumns()}
     FROM ${proxyProfilesTable(client)}
     ${whereClause}
     ORDER BY updated_at DESC, id DESC${pageClause}`,
    [...keywordFilter.params, ...pageParams]
  )
  const pageRows = paged ? takePageRows(rows, normalized.pageSize) : { rows, hasMore: false }
  const items = pageRows.rows.map(proxySummaryFromRow)
  return {
    items,
    total: paged ? pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore) : items.length,
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

function normalizeProxyListOptions(options: ProxyProfileListOptions): Required<Pick<ProxyProfileListOptions, 'page' | 'pageSize'>> & Pick<ProxyProfileListOptions, 'keyword'> {
  const rawPageSize = options.pageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(200, Math.max(1, rawPageSize))
    : 20
  const page = normalizeListPage(options.page, pageSize)
  return {
    page,
    pageSize,
    keyword: optionalString(options.keyword)
  }
}

function buildProxyKeywordFilter(keyword?: string): { clause: string; params: string[] } {
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  return {
    clause: '(name >= ? AND name < ?)',
    params: [text, textPrefixUpperBound(text)]
  }
}

function buildProxyKeywordFilterAsync(keyword?: string): { clause: string; params: string[] } {
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  return {
    clause: '(name COLLATE "C" >= ? AND name COLLATE "C" < ? AND starts_with(name, ?))',
    params: [text, textPrefixUpperBound(text), text]
  }
}

function textPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\u{10ffff}`
}

function proxySummaryFromRow(row: ProxyRow): ProxyProfileSummary {
  return {
    id: row.id,
    name: row.name,
    description: row.description ?? undefined,
    type: row.type,
    host: row.host,
    port: row.port,
    username: row.username ?? undefined,
    enabled: row.enabled === 1,
    testStatus: normalizeProxyTestStatus(row.test_status),
    latencyMs: row.latency_ms ?? undefined,
    outboundIp: row.outbound_ip ?? undefined,
    outboundRegion: row.outbound_region ?? undefined,
    lastTestMessage: row.last_test_message ?? undefined,
    lastTestedAt: row.last_tested_at ?? undefined
  }
}

function proxySummarySelectColumns(): string {
  return [
    'id',
    'name',
    'description',
    'type',
    'host',
    'port',
    'username',
    'enabled',
    'test_status',
    'latency_ms',
    'outbound_ip',
    'outbound_region',
    'last_test_message',
    'last_tested_at'
  ].join(', ')
}

function proxyTestConfigSelectColumns(): string {
  return [
    'id',
    'name',
    'description',
    'type',
    'host',
    'port',
    'username',
    'password_encrypted',
    'enabled',
    'test_status',
    'latency_ms',
    'outbound_ip',
    'outbound_region',
    'last_test_message',
    'last_tested_at'
  ].join(', ')
}

export function createProxy(input: Record<string, unknown>, access: AccessScope): ProxyProfileSummary {
  assertKnownInputKeys(input, proxyInputKeys, '代理')
  const now = nowIso()
  const systemAccountId = currentSystemAccountId(access)
  const hasPasswordInput = Object.prototype.hasOwnProperty.call(input, 'password')
  const password = normalizeProxyPassword(input.password, hasPasswordInput)
  const proxy: ProxyProfileSummary = {
    id: newId('proxy'),
    name: normalizedRequiredProxyName(input.name),
    description: normalizeOptionalText(input.description, '代理描述'),
    type: normalizedProxyType(input.type),
    host: normalizedRequiredProxyHost(input.host),
    port: normalizedProxyPort(input.port),
    username: normalizeOptionalText(input.username, '代理用户名'),
    enabled: normalizeOptionalBoolean(input.enabled, true, '代理启用状态'),
    testStatus: 'unknown',
    latencyMs: undefined,
    outboundIp: undefined,
    outboundRegion: undefined,
    lastTestMessage: undefined
  }
  try {
    getBusinessDatabase()
      .prepare(`
        INSERT INTO proxy_profiles (id, system_account_id, name, description, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(proxy.id, systemAccountId, proxy.name, proxy.description ?? null, proxy.type, proxy.host, proxy.port, proxy.username ?? null, password ? encryptJson({ password }) : null, proxy.enabled ? 1 : 0, proxy.testStatus, now, now)
  } catch (error) {
    if (isDuplicateProxyNameError(error)) {
      throw new Error(`代理名称已存在：${proxy.name}`)
    }
    throw error
  }
  notifyGatewayRuntimeCacheInvalidation('proxy_created')
  return proxy
}

export async function createProxyAsync(input: Record<string, unknown>, access: AccessScope): Promise<ProxyProfileSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createProxy(input, access)
  }
  assertKnownInputKeys(input, proxyInputKeys, '代理')
  const now = nowIso()
  const systemAccountId = currentSystemAccountId(access)
  const hasPasswordInput = Object.prototype.hasOwnProperty.call(input, 'password')
  const password = normalizeProxyPassword(input.password, hasPasswordInput)
  const proxy: ProxyProfileSummary = {
    id: newId('proxy'),
    name: normalizedRequiredProxyName(input.name),
    description: normalizeOptionalText(input.description, '代理描述'),
    type: normalizedProxyType(input.type),
    host: normalizedRequiredProxyHost(input.host),
    port: normalizedProxyPort(input.port),
    username: normalizeOptionalText(input.username, '代理用户名'),
    enabled: normalizeOptionalBoolean(input.enabled, true, '代理启用状态'),
    testStatus: 'unknown',
    latencyMs: undefined,
    outboundIp: undefined,
    outboundRegion: undefined,
    lastTestMessage: undefined
  }
  const client = await getProxyDatabaseClient()
  try {
    await client.execute(`
      INSERT INTO ${proxyProfilesTable(client)} (id, system_account_id, name, description, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [proxy.id, systemAccountId, proxy.name, proxy.description ?? null, proxy.type, proxy.host, proxy.port, proxy.username ?? null, password ? encryptJson({ password }) : null, proxy.enabled ? 1 : 0, proxy.testStatus, now, now])
  } catch (error) {
    if (isDuplicateProxyNameError(error)) {
      throw new Error(`代理名称已存在：${proxy.name}`)
    }
    throw error
  }
  notifyGatewayRuntimeCacheInvalidation('proxy_created')
  return proxy
}

export function updateProxy(id: string, input: Record<string, unknown>): ProxyProfileSummary | undefined {
  assertKnownInputKeys(input, proxyInputKeys, '代理')
  const current = findProxy(id)
  if (!current) {
    return undefined
  }
  const currentSecret = getBusinessDatabase()
    .prepare('SELECT password_encrypted FROM proxy_profiles WHERE id = ?')
    .get(id) as unknown as { password_encrypted?: string | null } | undefined
  const hasPasswordInput = Object.prototype.hasOwnProperty.call(input, 'password')
  const nextPassword = normalizeProxyPassword(input.password, hasPasswordInput)
  const shouldUpdatePassword = hasPasswordInput
  const hasUsernameInput = Object.prototype.hasOwnProperty.call(input, 'username')
  const next: ProxyProfileSummary = {
    ...current,
    name: Object.prototype.hasOwnProperty.call(input, 'name') ? normalizedRequiredProxyName(input.name) : current.name,
    description: input.description === undefined ? current.description : normalizeOptionalText(input.description, '代理描述'),
    type: Object.prototype.hasOwnProperty.call(input, 'type') ? normalizedProxyType(input.type) : current.type,
    host: Object.prototype.hasOwnProperty.call(input, 'host') ? normalizedRequiredProxyHost(input.host) : current.host,
    port: Object.prototype.hasOwnProperty.call(input, 'port') ? normalizedProxyPort(input.port) : current.port,
    username: hasUsernameInput ? normalizeOptionalText(input.username, '代理用户名') : current.username,
    enabled: input.enabled === undefined ? current.enabled : normalizeOptionalBoolean(input.enabled, true, '代理启用状态')
  }
  const shouldResetTestState = next.type !== current.type ||
    next.host !== current.host ||
    next.port !== current.port ||
    next.username !== current.username ||
    shouldUpdatePassword
  const nextPasswordEncrypted = shouldUpdatePassword
    ? encryptJson({ password: nextPassword })
    : currentSecret?.password_encrypted ?? null
  try {
    getBusinessDatabase()
      .prepare(`
        UPDATE proxy_profiles
        SET name = ?, description = ?, type = ?, host = ?, port = ?, username = ?, password_encrypted = ?, enabled = ?,
          test_status = ?, latency_ms = ?, outbound_ip = ?, outbound_region = ?, last_test_message = ?, last_tested_at = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(
        next.name,
        next.description ?? null,
        next.type,
        next.host,
        next.port,
        next.username ?? null,
        nextPasswordEncrypted,
        next.enabled ? 1 : 0,
        shouldResetTestState ? 'unknown' : next.testStatus,
        shouldResetTestState ? null : next.latencyMs ?? null,
        shouldResetTestState ? null : next.outboundIp ?? null,
        shouldResetTestState ? null : next.outboundRegion ?? null,
        shouldResetTestState ? null : next.lastTestMessage ?? null,
        shouldResetTestState ? null : next.lastTestedAt ?? null,
        nowIso(),
        id
      )
  } catch (error) {
    if (isDuplicateProxyNameError(error)) {
      throw new Error(`代理名称已存在：${next.name}`)
    }
    throw error
  }
  notifyGatewayRuntimeCacheInvalidation('proxy_updated')
  return findProxy(id) ?? next
}

export async function updateProxyAsync(id: string, input: Record<string, unknown>): Promise<ProxyProfileSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateProxy(id, input)
  }
  assertKnownInputKeys(input, proxyInputKeys, '代理')
  const current = await findProxyAsync(id)
  if (!current) {
    return undefined
  }
  const client = await getProxyDatabaseClient()
  const currentSecret = await client.one<{ password_encrypted?: string | null }>(`SELECT password_encrypted FROM ${proxyProfilesTable(client)} WHERE id = ?`, [id])
  const hasPasswordInput = Object.prototype.hasOwnProperty.call(input, 'password')
  const nextPassword = normalizeProxyPassword(input.password, hasPasswordInput)
  const shouldUpdatePassword = hasPasswordInput
  const hasUsernameInput = Object.prototype.hasOwnProperty.call(input, 'username')
  const next: ProxyProfileSummary = {
    ...current,
    name: Object.prototype.hasOwnProperty.call(input, 'name') ? normalizedRequiredProxyName(input.name) : current.name,
    description: input.description === undefined ? current.description : normalizeOptionalText(input.description, '代理描述'),
    type: Object.prototype.hasOwnProperty.call(input, 'type') ? normalizedProxyType(input.type) : current.type,
    host: Object.prototype.hasOwnProperty.call(input, 'host') ? normalizedRequiredProxyHost(input.host) : current.host,
    port: Object.prototype.hasOwnProperty.call(input, 'port') ? normalizedProxyPort(input.port) : current.port,
    username: hasUsernameInput ? normalizeOptionalText(input.username, '代理用户名') : current.username,
    enabled: input.enabled === undefined ? current.enabled : normalizeOptionalBoolean(input.enabled, true, '代理启用状态')
  }
  const shouldResetTestState = next.type !== current.type ||
    next.host !== current.host ||
    next.port !== current.port ||
    next.username !== current.username ||
    shouldUpdatePassword
  const nextPasswordEncrypted = shouldUpdatePassword
    ? encryptJson({ password: nextPassword })
    : currentSecret?.password_encrypted ?? null
  try {
    await client.execute(`
      UPDATE ${proxyProfilesTable(client)}
      SET name = ?, description = ?, type = ?, host = ?, port = ?, username = ?, password_encrypted = ?, enabled = ?,
        test_status = ?, latency_ms = ?, outbound_ip = ?, outbound_region = ?, last_test_message = ?, last_tested_at = ?, updated_at = ?
      WHERE id = ?
    `, [
      next.name,
      next.description ?? null,
      next.type,
      next.host,
      next.port,
      next.username ?? null,
      nextPasswordEncrypted,
      next.enabled ? 1 : 0,
      shouldResetTestState ? 'unknown' : next.testStatus,
      shouldResetTestState ? null : next.latencyMs ?? null,
      shouldResetTestState ? null : next.outboundIp ?? null,
      shouldResetTestState ? null : next.outboundRegion ?? null,
      shouldResetTestState ? null : next.lastTestMessage ?? null,
      shouldResetTestState ? null : next.lastTestedAt ?? null,
      nowIso(),
      id
    ])
  } catch (error) {
    if (isDuplicateProxyNameError(error)) {
      throw new Error(`代理名称已存在：${next.name}`)
    }
    throw error
  }
  notifyGatewayRuntimeCacheInvalidation('proxy_updated')
  return await findProxyAsync(id) ?? next
}

export function getProxyTestConfig(id: string): ProxyProfileTestConfig | undefined {
  const row = getBusinessDatabase().prepare(`SELECT ${proxyTestConfigSelectColumns()} FROM proxy_profiles WHERE id = ?`).get(id) as unknown as ProxyRow | undefined
  return row ? { ...proxySummaryFromRow(row), proxyUrl: proxyUrlFromRow(row) } : undefined
}

export async function getProxyTestConfigAsync(id: string): Promise<ProxyProfileTestConfig | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getProxyTestConfig(id)
  }
  const client = await getProxyDatabaseClient()
  const row = await client.one<ProxyRow>(`SELECT ${proxyTestConfigSelectColumns()} FROM ${proxyProfilesTable(client)} WHERE id = ?`, [id])
  return row ? { ...proxySummaryFromRow(row), proxyUrl: proxyUrlFromRow(row) } : undefined
}

export function listEnabledProxyTestConfigs(limit = 20): ProxyProfileTestConfig[] {
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${proxyTestConfigSelectColumns()}
      FROM proxy_profiles
      WHERE enabled = 1
      ORDER BY (last_tested_at IS NOT NULL) ASC, last_tested_at ASC, updated_at DESC, id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as unknown as ProxyRow[]
  return rows.map((row) => ({ ...proxySummaryFromRow(row), proxyUrl: proxyUrlFromRow(row) }))
}

export async function listEnabledProxyTestConfigsAsync(limit = 20): Promise<ProxyProfileTestConfig[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listEnabledProxyTestConfigs(limit)
  }
  const client = await getProxyDatabaseClient()
  const rows = await client.query<ProxyRow>(`
    SELECT ${proxyTestConfigSelectColumns()}
    FROM ${proxyProfilesTable(client)}
    WHERE enabled = 1
    ORDER BY (last_tested_at IS NOT NULL) ASC, last_tested_at ASC, updated_at DESC, id ASC
    LIMIT ?
  `, [Math.max(1, Math.trunc(limit))])
  return rows.map((row) => ({ ...proxySummaryFromRow(row), proxyUrl: proxyUrlFromRow(row) }))
}

export function updateProxyTestState(
  id: string,
  input: { testStatus: string; latencyMs?: number | null; outboundIp?: string | null; outboundRegion?: string | null; lastTestMessage?: string | null; lastTestedAt?: string }
): ProxyProfileSummary | undefined {
  const testedAt = input.lastTestedAt ?? nowIso()
  const testStatus = normalizeProxyTestStatus(input.testStatus)
  const latencyMs = normalizeProxyTestLatencyMs(input.latencyMs)
  const outboundIp = input.outboundIp === undefined ? undefined : normalizeProxyTestText(input.outboundIp, '代理出口 IP')
  const outboundRegion = input.outboundRegion === undefined ? undefined : normalizeProxyTestText(input.outboundRegion, '代理出口地区')
  const outboundUpdateSql = [
    outboundIp !== undefined ? 'outbound_ip = ?' : '',
    outboundRegion !== undefined ? 'outbound_region = ?' : ''
  ].filter(Boolean).join(', ')
  const sql = `
      UPDATE proxy_profiles
      SET test_status = ?, latency_ms = ?, ${outboundUpdateSql ? `${outboundUpdateSql}, ` : ''}last_test_message = ?, last_tested_at = ?, updated_at = ?
      WHERE id = ?
    `
  const params = [
    testStatus,
    latencyMs,
    ...(outboundIp !== undefined ? [outboundIp] : []),
    ...(outboundRegion !== undefined ? [outboundRegion] : []),
    normalizeProxyTestText(input.lastTestMessage, '代理检测消息'),
    testedAt,
    nowIso(),
    id
  ]
  getBusinessDatabase()
    .prepare(sql)
    .run(...params)
  notifyGatewayRuntimeCacheInvalidation('proxy_test_state_updated')
  return findProxy(id)
}

export async function updateProxyTestStateAsync(
  id: string,
  input: { testStatus: string; latencyMs?: number | null; outboundIp?: string | null; outboundRegion?: string | null; lastTestMessage?: string | null; lastTestedAt?: string }
): Promise<ProxyProfileSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateProxyTestState(id, input)
  }
  const testedAt = input.lastTestedAt ?? nowIso()
  const testStatus = normalizeProxyTestStatus(input.testStatus)
  const latencyMs = normalizeProxyTestLatencyMs(input.latencyMs)
  const outboundIp = input.outboundIp === undefined ? undefined : normalizeProxyTestText(input.outboundIp, '代理出口 IP')
  const outboundRegion = input.outboundRegion === undefined ? undefined : normalizeProxyTestText(input.outboundRegion, '代理出口地区')
  const outboundUpdateSql = [
    outboundIp !== undefined ? 'outbound_ip = ?' : '',
    outboundRegion !== undefined ? 'outbound_region = ?' : ''
  ].filter(Boolean).join(', ')
  const client = await getProxyDatabaseClient()
  const sql = `
    UPDATE ${proxyProfilesTable(client)}
    SET test_status = ?, latency_ms = ?, ${outboundUpdateSql ? `${outboundUpdateSql}, ` : ''}last_test_message = ?, last_tested_at = ?, updated_at = ?
    WHERE id = ?
  `
  const params = [
    testStatus,
    latencyMs,
    ...(outboundIp !== undefined ? [outboundIp] : []),
    ...(outboundRegion !== undefined ? [outboundRegion] : []),
    normalizeProxyTestText(input.lastTestMessage, '代理检测消息'),
    testedAt,
    nowIso(),
    id
  ]
  await client.execute(sql, params)
  notifyGatewayRuntimeCacheInvalidation('proxy_test_state_updated')
  return await findProxyAsync(id)
}

export function deleteProxy(id: string): boolean {
  const usage = proxyUsageSummary(id)
  if (usage.accountCount > 0) {
    throw new ProxyInUseError(usage.accountCount, usage.accountNames, usage.accountCountIsLowerBound)
  }
  const result = getBusinessDatabase().prepare('DELETE FROM proxy_profiles WHERE id = ?').run(id)
  if (Number(result.changes ?? 0) > 0) {
    notifyGatewayRuntimeCacheInvalidation('proxy_deleted')
  }
  return result.changes > 0
}

export async function deleteProxyAsync(id: string): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return deleteProxy(id)
  }
  const usage = await proxyUsageSummaryAsync(id)
  if (usage.accountCount > 0) {
    throw new ProxyInUseError(usage.accountCount, usage.accountNames, usage.accountCountIsLowerBound)
  }
  const client = await getProxyDatabaseClient()
  const result = await client.execute(`DELETE FROM ${proxyProfilesTable(client)} WHERE id = ?`, [id])
  if (Number(result.changes ?? 0) > 0) {
    notifyGatewayRuntimeCacheInvalidation('proxy_deleted')
  }
  return result.changes > 0
}

function proxyUsageSummary(id: string): { accountCount: number; accountCountIsLowerBound: boolean; accountNames: string[] } {
  const rows = getBusinessDatabase()
    .prepare('SELECT id, name FROM accounts WHERE proxy_profile_id = ? AND deleted_at IS NULL ORDER BY id ASC LIMIT ?')
    .all(id, proxyUsageWindowLimit) as unknown as Array<{ id?: string; name?: string }>
  const accountCountIsLowerBound = rows.length >= proxyUsageWindowLimit
  const accountCount = rows.length
  return {
    accountCount,
    accountCountIsLowerBound,
    accountNames: rows
      .slice(0, proxyUsagePreviewLimit)
      .map((item) => item.name)
      .filter((name): name is string => Boolean(name))
  }
}

async function proxyUsageSummaryAsync(id: string): Promise<{ accountCount: number; accountCountIsLowerBound: boolean; accountNames: string[] }> {
  const client = await getProxyDatabaseClient()
  const rows = await client.query<Array<{ id?: string; name?: string }>[number]>(
    `SELECT id, name
     FROM ${client.dialect.qualifyTable(businessSchemaName, 'accounts')}
     WHERE proxy_profile_id = ? AND deleted_at IS NULL
     ORDER BY id ASC
     LIMIT ?`,
    [id, proxyUsageWindowLimit]
  )
  const accountCountIsLowerBound = rows.length >= proxyUsageWindowLimit
  const accountCount = rows.length
  return {
    accountCount,
    accountCountIsLowerBound,
    accountNames: rows
      .slice(0, proxyUsagePreviewLimit)
      .map((item) => item.name)
      .filter((name): name is string => Boolean(name))
  }
}

export function resolveProxyUrlForProfile(proxyProfileId?: string | null): string | undefined {
  return proxyUrlForProfile(proxyProfileId)
}

export async function resolveProxyUrlForProfileAsync(proxyProfileId?: string | null): Promise<string | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return resolveProxyUrlForProfile(proxyProfileId)
  }
  return await proxyUrlForProfileAsync(proxyProfileId)
}

export function resolveProxyUrlForProfileForSystemAccount(proxyProfileId: string | undefined | null, _systemAccountId: string): string | undefined {
  return proxyUrlForProfile(proxyProfileId)
}

export async function resolveProxyUrlForProfileForSystemAccountAsync(proxyProfileId: string | undefined | null, _systemAccountId: string): Promise<string | undefined> {
  return await resolveProxyUrlForProfileAsync(proxyProfileId)
}

export function resolveProxyUrlsForProfiles(proxyProfileIds: string[]): Map<string, ProxyProfileUrlResolution> {
  const ids = [...new Set(proxyProfileIds.map((id) => id.trim()).filter(Boolean))]
  const output = new Map<string, ProxyProfileUrlResolution>()
  if (!ids.length) return output

  const rows: Array<Pick<ProxyRow, 'id' | 'type' | 'host' | 'port' | 'username' | 'password_encrypted' | 'enabled'>> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`SELECT id, type, host, port, username, password_encrypted, enabled FROM proxy_profiles WHERE id IN (${sqlPlaceholders(chunk.length)})`)
      .all(...chunk) as unknown as Array<Pick<ProxyRow, 'id' | 'type' | 'host' | 'port' | 'username' | 'password_encrypted' | 'enabled'>>)
  }
  const rowsById = new Map(rows.map((row) => [row.id, row]))
  for (const id of ids) {
    const row = rowsById.get(id)
    if (!row || row.enabled !== 1) {
      output.set(id, { unavailable: true, errorMessage: new ProxyProfileUnavailableError(id).message })
      continue
    }
    try {
      output.set(id, { proxyUrl: proxyUrlFromRow(row) })
    } catch {
      output.set(id, { unavailable: true, errorMessage: '代理凭据不可解密，请检查代理配置' })
    }
  }
  return output
}

export async function resolveProxyUrlsForProfilesAsync(proxyProfileIds: string[]): Promise<Map<string, ProxyProfileUrlResolution>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return resolveProxyUrlsForProfiles(proxyProfileIds)
  }
  const ids = [...new Set(proxyProfileIds.map((id) => id.trim()).filter(Boolean))]
  const output = new Map<string, ProxyProfileUrlResolution>()
  if (!ids.length) return output

  const client = await getProxyDatabaseClient()
  const rows: Array<Pick<ProxyRow, 'id' | 'type' | 'host' | 'port' | 'username' | 'password_encrypted' | 'enabled'>> = []
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...await client.query<Pick<ProxyRow, 'id' | 'type' | 'host' | 'port' | 'username' | 'password_encrypted' | 'enabled'>>(`
      SELECT id, type, host, port, username, password_encrypted, enabled
      FROM ${proxyProfilesTable(client)}
      WHERE id IN (${chunk.map(() => '?').join(', ')})
    `, chunk))
  }
  const rowsById = new Map(rows.map((row) => [row.id, row]))
  for (const id of ids) {
    const row = rowsById.get(id)
    if (!row || row.enabled !== 1) {
      output.set(id, { unavailable: true, errorMessage: new ProxyProfileUnavailableError(id).message })
      continue
    }
    try {
      output.set(id, { proxyUrl: proxyUrlFromRow(row) })
    } catch {
      output.set(id, { unavailable: true, errorMessage: '代理凭据不可解密，请检查代理配置' })
    }
  }
  return output
}

export function resolveEnabledProxyProfileId(proxyProfileId?: string | null): string | undefined {
  if (!proxyProfileId) return undefined
  const row = getBusinessDatabase()
    .prepare('SELECT id, enabled FROM proxy_profiles WHERE id = ?')
    .get(proxyProfileId) as unknown as Pick<ProxyRow, 'id' | 'enabled'> | undefined
  if (!row || row.enabled !== 1) {
    throw new ProxyProfileUnavailableError(proxyProfileId)
  }
  return row.id
}

export async function resolveEnabledProxyProfileIdAsync(proxyProfileId?: string | null): Promise<string | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return resolveEnabledProxyProfileId(proxyProfileId)
  }
  if (!proxyProfileId) return undefined
  const client = await getProxyDatabaseClient()
  const row = await client.one<Pick<ProxyRow, 'id' | 'enabled'>>(`SELECT id, enabled FROM ${proxyProfilesTable(client)} WHERE id = ?`, [proxyProfileId])
  if (!row || row.enabled !== 1) {
    throw new ProxyProfileUnavailableError(proxyProfileId)
  }
  return row.id
}

function proxyUrlForProfile(proxyProfileId?: string | null): string | undefined {
  if (!proxyProfileId) return undefined
  const row = getBusinessDatabase()
    .prepare('SELECT type, host, port, username, password_encrypted, enabled FROM proxy_profiles WHERE id = ?')
    .get(proxyProfileId) as unknown as ProxyRow | undefined
  if (!row || row.enabled !== 1) {
    throw new ProxyProfileUnavailableError(proxyProfileId)
  }
  return proxyUrlFromRow(row)
}

async function proxyUrlForProfileAsync(proxyProfileId?: string | null): Promise<string | undefined> {
  if (!proxyProfileId) return undefined
  const client = await getProxyDatabaseClient()
  const row = await client.one<ProxyRow>(`
    SELECT type, host, port, username, password_encrypted, enabled
    FROM ${proxyProfilesTable(client)}
    WHERE id = ?
  `, [proxyProfileId])
  if (!row || row.enabled !== 1) {
    throw new ProxyProfileUnavailableError(proxyProfileId)
  }
  return proxyUrlFromRow(row)
}

async function getProxyDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function proxyProfilesTable(client: DatabaseClient): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, 'proxy_profiles')
    : client.dialect.quoteIdentifier('proxy_profiles')
}

function proxyUrlFromRow(row: Pick<ProxyRow, 'type' | 'host' | 'port' | 'username' | 'password_encrypted'>): string {
  const protocol = row.type === 'socks5h' ? 'socks5h' : row.type === 'socks5' ? 'socks5h' : row.type
  const password = proxyPassword(row)
  const credentials = row.username
    ? `${encodeURIComponent(row.username)}${password ? `:${encodeURIComponent(password)}` : ''}@`
    : ''
  return `${protocol}://${credentials}${row.host}:${row.port}`
}

function proxyPassword(row: Pick<ProxyRow, 'password_encrypted'>): string | undefined {
  if (!row.password_encrypted) return undefined
  const decrypted = decryptJson<{ password?: unknown }>(row.password_encrypted)
  return typeof decrypted.password === 'string' ? decrypted.password : undefined
}

function normalizedRequiredProxyName(value: unknown): string {
  if (typeof value === 'string' && value.trim()) return value.trim()
  throw new Error('代理名称不能为空')
}

function normalizedRequiredProxyHost(value: unknown): string {
  if (typeof value === 'string' && value.trim()) return value.trim()
  throw new Error('代理主机不能为空')
}

function normalizedProxyType(value: unknown): string {
  if (typeof value === 'string' && proxyTypeValues.includes(value as typeof proxyTypeValues[number])) {
    return value
  }
  throw new Error('代理类型无效')
}

function normalizedProxyPort(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 65535) {
    throw new Error('代理端口必须是 1-65535 的整数')
  }
  return value
}

function normalizeOptionalText(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null) {
    return undefined
  }
  if (typeof value !== 'string') {
    throw new Error(`${label}必须是字符串`)
  }
  const text = value.trim()
  return text || undefined
}

function normalizeOptionalBoolean(value: unknown, fallback: boolean, label: string): boolean {
  if (value === undefined) return fallback
  if (typeof value !== 'boolean') {
    throw new Error(`${label}必须是布尔值`)
  }
  return value
}

function normalizeProxyPassword(value: unknown, requiredWhenPresent: boolean): string | undefined {
  if (value === undefined) {
    if (requiredWhenPresent) {
      throw new Error('代理密码不能为空')
    }
    return undefined
  }
  if (typeof value !== 'string') {
    throw new Error('代理密码必须是字符串')
  }
  if (value.trim().length === 0 && requiredWhenPresent) {
    throw new Error('代理密码不能为空')
  }
  return value
}

function normalizeProxyTestStatus(value: string): ProxyProfileTestStatus {
  if (proxyTestStatusValues.includes(value as ProxyProfileTestStatus)) {
    return value as ProxyProfileTestStatus
  }
  throw new Error('代理检测状态无效')
}

function normalizeProxyTestLatencyMs(value: unknown): number | null {
  if (value === undefined || value === null) return null
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value) || value < 0) {
    throw new Error('代理检测延迟必须是非负整数')
  }
  return value
}

function normalizeProxyTestText(value: unknown, label: string): string | null {
  if (value === undefined || value === null) return null
  if (typeof value !== 'string') {
    throw new Error(`${label}必须是字符串`)
  }
  const text = value.trim()
  return text || null
}

function assertKnownInputKeys(input: Record<string, unknown>, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

function isDuplicateProxyNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_proxy_profiles_name_unique')
    || error.message.includes('idx_proxy_profiles_name_unique_lower')
}

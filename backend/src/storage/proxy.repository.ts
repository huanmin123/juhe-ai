import { decryptJson, encryptJson } from './crypto.js'
import { getDatabase, newId, nowIso } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { optionalNullableString, optionalString } from './value-utils.js'

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
  testStatus: string
  latencyMs?: number
  outboundIp?: string
  outboundRegion?: string
  lastTestMessage?: string
  lastTestedAt?: string
}

export type ProxyProfileOptionSummary = Pick<ProxyProfileSummary, 'id' | 'name' | 'type' | 'enabled'>

export interface ProxyProfileTestConfig extends ProxyProfileSummary {
  proxyUrl: string
}

export interface ProxyProfileUrlResolution {
  proxyUrl?: string
  unavailable?: boolean
  errorMessage?: string
}

export class ProxyInUseError extends Error {
  constructor(readonly accountCount: number, readonly accountNames: string[]) {
    const names = accountNames.length > 0 ? `：${accountNames.join('、')}${accountCount > accountNames.length ? ' 等' : ''}` : ''
    super(`这个代理仍被 ${accountCount} 个账户使用，请先在账户管理中解绑或改绑后再删除${names}`)
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
  const rows = getDatabase().prepare('SELECT * FROM proxy_profiles ORDER BY updated_at DESC, id DESC').all() as unknown as ProxyRow[]
  return rows.map(proxySummaryFromRow)
}

export function findProxy(id: string): ProxyProfileSummary | undefined {
  const row = getDatabase().prepare('SELECT * FROM proxy_profiles WHERE id = ?').get(id) as unknown as ProxyRow | undefined
  return row ? proxySummaryFromRow(row) : undefined
}

export function listProxyOptions(): ProxyProfileOptionSummary[] {
  const rows = getDatabase()
    .prepare('SELECT id, name, type, enabled FROM proxy_profiles WHERE enabled = 1 ORDER BY name ASC, updated_at DESC, id ASC')
    .all() as unknown as Array<Pick<ProxyRow, 'id' | 'name' | 'type' | 'enabled'>>
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    type: row.type,
    enabled: row.enabled === 1
  }))
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
    testStatus: row.test_status,
    latencyMs: row.latency_ms ?? undefined,
    outboundIp: row.outbound_ip ?? undefined,
    outboundRegion: row.outbound_region ?? undefined,
    lastTestMessage: row.last_test_message ?? undefined,
    lastTestedAt: row.last_tested_at ?? undefined
  }
}

export function createProxy(input: Record<string, unknown>): ProxyProfileSummary {
  const now = nowIso()
  const proxy: ProxyProfileSummary = {
    id: newId('proxy'),
    name: normalizedProxyName(input.name, '未命名代理'),
    description: optionalString(input.description),
    type: String(input.type ?? 'socks5h'),
    host: String(input.host ?? ''),
    port: Number(input.port ?? 0),
    username: optionalNullableString(input.username) ?? undefined,
    enabled: input.enabled !== false,
    testStatus: 'unknown',
    latencyMs: undefined,
    outboundIp: undefined,
    outboundRegion: undefined,
    lastTestMessage: undefined
  }
  assertProxyNameAvailable(proxy.name)
  try {
    getDatabase()
      .prepare(`
        INSERT INTO proxy_profiles (id, name, description, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(proxy.id, proxy.name, proxy.description ?? null, proxy.type, proxy.host, proxy.port, proxy.username ?? null, input.password ? encryptJson({ password: input.password }) : null, proxy.enabled ? 1 : 0, proxy.testStatus, now, now)
  } catch (error) {
    if (isDuplicateProxyNameError(error)) {
      throw new Error(`代理名称已存在：${proxy.name}`)
    }
    throw error
  }
  return proxy
}

export function updateProxy(id: string, input: Record<string, unknown>): ProxyProfileSummary | undefined {
  const current = findProxy(id)
  if (!current) {
    return undefined
  }
  const currentSecret = getDatabase()
    .prepare('SELECT password_encrypted FROM proxy_profiles WHERE id = ?')
    .get(id) as unknown as { password_encrypted?: string | null } | undefined
  const shouldUpdatePassword = typeof input.password === 'string' && input.password.trim() !== ''
  const hasUsernameInput = Object.prototype.hasOwnProperty.call(input, 'username')
  const next: ProxyProfileSummary = {
    ...current,
    name: typeof input.name === 'string' && input.name.trim() ? input.name.trim() : current.name,
    description: input.description === undefined ? current.description : optionalNullableString(input.description) ?? undefined,
    type: typeof input.type === 'string' ? input.type : current.type,
    host: typeof input.host === 'string' ? input.host : current.host,
    port: Number(input.port ?? current.port),
    username: hasUsernameInput ? optionalNullableString(input.username) ?? undefined : current.username,
    enabled: typeof input.enabled === 'boolean' ? input.enabled : current.enabled
  }
  const shouldResetTestState = next.type !== current.type ||
    next.host !== current.host ||
    next.port !== current.port ||
    next.username !== current.username ||
    shouldUpdatePassword
  const nextPasswordEncrypted = shouldUpdatePassword
    ? encryptJson({ password: String(input.password) })
    : currentSecret?.password_encrypted ?? null
  assertProxyNameAvailable(next.name, id)
  try {
    getDatabase()
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
  return findProxy(id) ?? next
}

export function getProxyTestConfig(id: string): ProxyProfileTestConfig | undefined {
  const row = getDatabase().prepare('SELECT * FROM proxy_profiles WHERE id = ?').get(id) as unknown as ProxyRow | undefined
  return row ? { ...proxySummaryFromRow(row), proxyUrl: proxyUrlFromRow(row) } : undefined
}

export function listEnabledProxyTestConfigs(limit = 20): ProxyProfileTestConfig[] {
  const rows = getDatabase()
    .prepare(`
      SELECT *
      FROM proxy_profiles
      WHERE enabled = 1
      ORDER BY last_tested_at IS NOT NULL ASC, last_tested_at ASC, updated_at DESC, id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as unknown as ProxyRow[]
  return rows.map((row) => ({ ...proxySummaryFromRow(row), proxyUrl: proxyUrlFromRow(row) }))
}

export function updateProxyTestState(
  id: string,
  input: { testStatus: string; latencyMs?: number; outboundIp?: string | null; outboundRegion?: string | null; lastTestMessage?: string; lastTestedAt?: string }
): ProxyProfileSummary | undefined {
  const testedAt = input.lastTestedAt ?? nowIso()
  const latencyMs = typeof input.latencyMs === 'number' && Number.isFinite(input.latencyMs)
    ? Math.max(0, Math.trunc(input.latencyMs))
    : null
  const outboundIp = input.outboundIp === undefined ? undefined : optionalString(input.outboundIp) ?? null
  const outboundRegion = input.outboundRegion === undefined ? undefined : optionalString(input.outboundRegion) ?? null
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
    input.testStatus,
    latencyMs,
    ...(outboundIp !== undefined ? [outboundIp] : []),
    ...(outboundRegion !== undefined ? [outboundRegion] : []),
    optionalString(input.lastTestMessage) ?? null,
    testedAt,
    nowIso(),
    id
  ]
  getDatabase()
    .prepare(sql)
    .run(...params)
  return findProxy(id)
}

export function deleteProxy(id: string): boolean {
  const usage = proxyUsageSummary(id)
  if (usage.accountCount > 0) {
    throw new ProxyInUseError(usage.accountCount, usage.accountNames)
  }
  const result = getDatabase().prepare('DELETE FROM proxy_profiles WHERE id = ?').run(id)
  return result.changes > 0
}

function proxyUsageSummary(id: string): { accountCount: number; accountNames: string[] } {
  const row = getDatabase()
    .prepare('SELECT COUNT(*) AS account_count FROM accounts WHERE proxy_profile_id = ?')
    .get(id) as unknown as { account_count?: number } | undefined
  const accountCount = Number(row?.account_count ?? 0)
  if (accountCount <= 0) {
    return { accountCount: 0, accountNames: [] }
  }
  const rows = getDatabase()
    .prepare('SELECT name FROM accounts WHERE proxy_profile_id = ? ORDER BY name ASC, id ASC LIMIT 3')
    .all(id) as unknown as Array<{ name?: string }>
  return {
    accountCount,
    accountNames: rows.map((item) => item.name).filter((name): name is string => Boolean(name))
  }
}

export function resolveProxyUrlForProfile(proxyProfileId?: string | null): string | undefined {
  return proxyUrlForProfile(proxyProfileId)
}

export function resolveProxyUrlForProfileForSystemAccount(proxyProfileId: string | undefined | null, _systemAccountId: string): string | undefined {
  return proxyUrlForProfile(proxyProfileId)
}

export function resolveProxyUrlsForProfiles(proxyProfileIds: string[]): Map<string, ProxyProfileUrlResolution> {
  const ids = [...new Set(proxyProfileIds.map((id) => id.trim()).filter(Boolean))]
  const output = new Map<string, ProxyProfileUrlResolution>()
  if (!ids.length) return output

  const rows: Array<Pick<ProxyRow, 'id' | 'type' | 'host' | 'port' | 'username' | 'password_encrypted' | 'enabled'>> = []
  const database = getDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`SELECT id, type, host, port, username, password_encrypted, enabled FROM proxy_profiles WHERE id IN (${sqlPlaceholders(chunk.length)})`)
      .all(...chunk) as unknown as Array<Pick<ProxyRow, 'id' | 'type' | 'host' | 'port' | 'username' | 'password_encrypted' | 'enabled'>>)
  }
  const rowsById = new Map(rows.map((row) => [row.id, row]))
  for (const id of ids) {
    const row = rowsById.get(id)
    output.set(id, row && row.enabled === 1
      ? { proxyUrl: proxyUrlFromRow(row) }
      : { unavailable: true, errorMessage: new ProxyProfileUnavailableError(id).message })
  }
  return output
}

export function resolveEnabledProxyProfileId(proxyProfileId?: string | null): string | undefined {
  if (!proxyProfileId) return undefined
  const row = getDatabase()
    .prepare('SELECT id, enabled FROM proxy_profiles WHERE id = ?')
    .get(proxyProfileId) as unknown as Pick<ProxyRow, 'id' | 'enabled'> | undefined
  if (!row || row.enabled !== 1) {
    throw new ProxyProfileUnavailableError(proxyProfileId)
  }
  return row.id
}

function proxyUrlForProfile(proxyProfileId?: string | null): string | undefined {
  if (!proxyProfileId) return undefined
  const row = getDatabase()
    .prepare('SELECT type, host, port, username, password_encrypted, enabled FROM proxy_profiles WHERE id = ?')
    .get(proxyProfileId) as unknown as ProxyRow | undefined
  if (!row || row.enabled !== 1) {
    throw new ProxyProfileUnavailableError(proxyProfileId)
  }
  return proxyUrlFromRow(row)
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

function normalizedProxyName(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

function assertProxyNameAvailable(name: string, excludeId?: string): void {
  const params: string[] = [name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getDatabase()
    .prepare(`SELECT id FROM proxy_profiles WHERE lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`代理名称已存在：${name}`)
  }
}

function isDuplicateProxyNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_proxy_profiles_name_unique_lower')
}

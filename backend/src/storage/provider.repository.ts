import {
  isAdminRole,
  type AccountType,
  type ProviderCode,
  type ProviderDefinition,
  type ProviderProtocolProfileDefinition,
  type ProtocolEndpointFamilyDefinition,
  type SystemAccountRole
} from '../domain/types.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
} from '../domain/provider-protocol.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { runtimeConfig } from '../config/runtime.js'
import { parseJsonArray } from './value-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import {
  findProviderDefaultHealthCheckModelPreference,
  findProviderDefaultHealthCheckModelPreferenceAsync
} from './provider-default-health-check-model.repository.js'
import {
  findProviderSystemDefaultHealthCheckModel,
  findProviderSystemDefaultHealthCheckModelAsync
} from './provider-system-default-health-check-model.repository.js'
import type { ProviderListItem } from '../domain/types.js'

interface ProviderRow {
  id: string
  code: ProviderCode
  name: string
  parent_code: ProviderCode | null
  description: string | null
  enabled: number | boolean
  default_supported_models_json: string | null
}

interface ProviderProtocolProfileRow {
  id: string
  provider_code: ProviderCode
  name: string
  description: string | null
  enabled: number | boolean
  protocol_code: string
  protocol_version: string
  base_url: string
  default_health_check_model: string
  account_types_json: string
  capabilities_json: string
}

interface ProviderProfileFamilyRow {
  profile_id: string
  family_code: string
  name: string
  description: string | null
}

interface SystemAccountRoleRow {
  role: SystemAccountRole
}

const maxProviderDefinitions = 50
const maxProviderProtocolProfiles = 200
const businessSchemaName = 'juhe_business'

export function listProviderListItems(): ProviderListItem[] {
  const rows = getBusinessDatabase().prepare(`
    SELECT p.id, p.code, p.name, p.parent_code, p.description, p.enabled,
      p.default_supported_models_json,
      ppp.protocol_code, ppp.base_url, ppp.default_health_check_model, ppp.account_types_json, ppp.capabilities_json
    FROM providers p
    LEFT JOIN provider_protocol_profiles ppp ON ppp.id = (
      SELECT candidate.id FROM provider_protocol_profiles candidate
      WHERE candidate.provider_code = p.code
      ORDER BY candidate.enabled DESC,
        CASE WHEN candidate.id IN (?, ?) THEN 0 ELSE 1 END,
        candidate.updated_at DESC, candidate.id ASC LIMIT 1
    )
    ORDER BY p.name ASC, p.code ASC LIMIT ?
  `).all(GEMINI_NATIVE_V1BETA_PROFILE_ID, GLM_CODING_OPENAI_V1_PROFILE_ID, maxProviderDefinitions) as unknown as ProviderListRow[]
  return rows.map(mapProviderListRow)
}

export async function listProviderListItemsAsync(): Promise<ProviderListItem[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') return listProviderListItems()
  const client = await getProviderDatabaseClient()
  const providers = providerTable(client, 'providers')
  const profiles = providerTable(client, 'provider_protocol_profiles')
  const rows = await client.query<ProviderListRow>(`
    SELECT p.id, p.code, p.name, p.parent_code, p.description, p.enabled,
      p.default_supported_models_json,
      ppp.protocol_code, ppp.base_url, ppp.default_health_check_model, ppp.account_types_json, ppp.capabilities_json
    FROM ${providers} p
    LEFT JOIN ${profiles} ppp ON ppp.id = (
      SELECT candidate.id FROM ${profiles} candidate
      WHERE candidate.provider_code = p.code
      ORDER BY candidate.enabled DESC,
        CASE WHEN candidate.id IN (?, ?) THEN 0 ELSE 1 END,
        candidate.updated_at DESC, candidate.id ASC LIMIT 1
    )
    ORDER BY p.name ASC, p.code ASC LIMIT ?
  `, [GEMINI_NATIVE_V1BETA_PROFILE_ID, GLM_CODING_OPENAI_V1_PROFILE_ID, maxProviderDefinitions])
  return rows.map(mapProviderListRow)
}

interface ProviderListRow {
  id: string; code: ProviderCode; name: string; parent_code: ProviderCode | null; description: string | null; enabled: number | boolean
  default_supported_models_json: string | null; base_url: string | null; default_health_check_model: string | null
  protocol_code: string | null; account_types_json: string | null; capabilities_json: string | null
}

export interface ProviderOptionRecord {
  id: string
  code: ProviderCode
  name: string
  enabled: boolean
}

interface ProviderOptionRow {
  id: string
  code: ProviderCode
  name: string
  enabled: number | boolean
}

export async function listEnabledProviderOptionsAsync(): Promise<ProviderOptionRecord[]> {
  const client = await getProviderDatabaseClient()
  const rows = await client.query<ProviderOptionRow>(`
    SELECT id, code, name, enabled
    FROM ${providerTable(client, 'providers')}
    WHERE ${providerEnabledPredicate(client, 'enabled')}
    ORDER BY name ASC, code ASC
    LIMIT ?
  `, [maxProviderDefinitions])
  return rows.map(mapProviderOptionRow)
}

export async function findProviderOptionByCodeAsync(providerCode: string): Promise<ProviderOptionRecord | undefined> {
  const code = providerCode.trim()
  if (!code) return undefined
  const client = await getProviderDatabaseClient()
  const row = await client.one<ProviderOptionRow>(`
    SELECT id, code, name, enabled
    FROM ${providerTable(client, 'providers')}
    WHERE code = ?
    LIMIT 1
  `, [code])
  return row ? mapProviderOptionRow(row) : undefined
}

function mapProviderOptionRow(row: ProviderOptionRow): ProviderOptionRecord {
  return { id: row.id, code: row.code, name: row.name, enabled: Number(row.enabled) === 1 }
}

function mapProviderListRow(row: ProviderListRow): ProviderListItem {
  return {
    id: row.id, code: row.code, name: row.name, parentCode: row.parent_code ?? undefined,
    description: row.description ?? undefined, enabled: Number(row.enabled) === 1,
    protocolCode: row.protocol_code ?? '',
    baseUrl: row.base_url ?? '', defaultHealthCheckModel: row.default_health_check_model ?? '',
    defaultSupportedModels: providerDefaultSupportedModels(row.default_supported_models_json),
    accountTypes: parseJsonArray(row.account_types_json ?? '') as AccountType[], capabilities: parseJsonArray(row.capabilities_json ?? '')
  }
}

export function listProviders(): ProviderDefinition[] {
  return listProvidersReadOnly()
}

export function listProvidersReadOnly(providerCode?: string): ProviderDefinition[] {
  const code = providerCode?.trim()
  const where = code ? 'WHERE code = ?' : ''
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, code, name, parent_code, description, enabled, default_supported_models_json
      FROM providers
      ${where}
      ORDER BY name ASC, code ASC
      LIMIT ?
    `)
    .all(...(code ? [code, maxProviderDefinitions] : [maxProviderDefinitions])) as unknown as ProviderRow[]
  const profilesByProvider = providerProtocolProfilesByProviderCode(rows.map((row) => row.code))
  return rows.map((row) => ({
    id: row.id,
    code: row.code,
    name: row.name,
    parentCode: row.parent_code ?? undefined,
    description: row.description ?? undefined,
    enabled: isProviderEnabled(row.enabled),
    defaultSupportedModels: providerDefaultSupportedModels(row.default_supported_models_json),
    ...providerDefaultProfileFields(profilesByProvider.get(row.code) ?? []),
    protocolProfiles: profilesByProvider.get(row.code) ?? []
  }))
}

export async function listProvidersAsync(providerCode?: string): Promise<ProviderDefinition[]> {
  const code = providerCode?.trim()
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (!code && sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_providers_read_only'
      })
    }
    return listProvidersReadOnly(code)
  }
  const client = await getProviderDatabaseClient()
  const rows = await client.query<ProviderRow>(`
    SELECT id, code, name, parent_code, description, enabled, default_supported_models_json
    FROM ${providerTable(client, 'providers')}
    ${code ? 'WHERE code = ?' : ''}
    ORDER BY name ASC, code ASC
    LIMIT ?
  `, code ? [code, maxProviderDefinitions] : [maxProviderDefinitions])
  const profilesByProvider = await providerProtocolProfilesByProviderCodeAsync(client, rows.map((row) => row.code))
  return rows.map((row) => ({
    id: row.id,
    code: row.code,
    name: row.name,
    parentCode: row.parent_code ?? undefined,
    description: row.description ?? undefined,
    enabled: isProviderEnabled(row.enabled),
    defaultSupportedModels: providerDefaultSupportedModels(row.default_supported_models_json),
    ...providerDefaultProfileFields(profilesByProvider.get(row.code) ?? []),
    protocolProfiles: profilesByProvider.get(row.code) ?? []
  }))
}

export function listOpenAIProtocolProviderCodes(): ProviderCode[] {
  return listProtocolProviderCodes(OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION)
}

export async function listOpenAIProtocolProviderCodesAsync(): Promise<ProviderCode[]> {
  return await listProtocolProviderCodesAsync(OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION)
}

export function listAnthropicProtocolProviderCodes(): ProviderCode[] {
  return listProtocolProviderCodes(ANTHROPIC_PROTOCOL_CODE, ANTHROPIC_PROTOCOL_VERSION)
}

export async function listAnthropicProtocolProviderCodesAsync(): Promise<ProviderCode[]> {
  return await listProtocolProviderCodesAsync(ANTHROPIC_PROTOCOL_CODE, ANTHROPIC_PROTOCOL_VERSION)
}

export function listGeminiProtocolProviderCodes(): ProviderCode[] {
  return listProtocolProviderCodes(GEMINI_PROTOCOL_CODE, GEMINI_PROTOCOL_VERSION)
}

export async function listGeminiProtocolProviderCodesAsync(): Promise<ProviderCode[]> {
  return await listProtocolProviderCodesAsync(GEMINI_PROTOCOL_CODE, GEMINI_PROTOCOL_VERSION)
}

export function listProtocolProviderCodes(protocolCode: string, protocolVersion: string): ProviderCode[] {
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT code
      FROM provider_protocol_profiles
      INNER JOIN providers ON providers.code = provider_protocol_profiles.provider_code
      WHERE providers.enabled = 1
        AND provider_protocol_profiles.enabled = 1
        AND protocol_code = ?
        AND protocol_version = ?
      ORDER BY code ASC
      LIMIT ?
    `)
    .all(protocolCode, protocolVersion, maxProviderDefinitions) as unknown as Array<{ code: ProviderCode }>
  return rows.map((row) => row.code)
}

async function listProtocolProviderCodesAsync(protocolCode: string, protocolVersion: string): Promise<ProviderCode[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_protocol_provider_codes_read_only',
      protocolCode,
      protocolVersion
    })
  }
  const client = await getProviderDatabaseClient()
  const profilesTable = providerTable(client, 'provider_protocol_profiles')
  const providersTable = providerTable(client, 'providers')
  const rows = await client.query<{ code: ProviderCode }>(`
    SELECT p.code
    FROM ${profilesTable} ppp
    INNER JOIN ${providersTable} p
      ON p.code = ppp.provider_code
    WHERE ${providerEnabledPredicate(client, 'p.enabled')}
      AND ${providerEnabledPredicate(client, 'ppp.enabled')}
      AND ppp.protocol_code = ?
      AND ppp.protocol_version = ?
    ORDER BY p.code ASC
    LIMIT ?
  `, [protocolCode, protocolVersion, maxProviderDefinitions])
  return rows.map((row) => row.code)
}

export function listOpenAIProtocolProfileIds(): string[] {
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT provider_protocol_profiles.id
      FROM provider_protocol_profiles
      INNER JOIN providers ON providers.code = provider_protocol_profiles.provider_code
      WHERE providers.enabled = 1
        AND provider_protocol_profiles.enabled = 1
        AND protocol_code = ?
        AND protocol_version = ?
      ORDER BY provider_protocol_profiles.id ASC
      LIMIT ?
    `)
    .all(OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION, maxProviderProtocolProfiles) as unknown as Array<{ id: string }>
  return rows.map((row) => row.id)
}

export async function listOpenAIProtocolProfileIdsAsync(): Promise<string[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_openai_protocol_profile_ids_read_only'
    })
  }
  const client = await getProviderDatabaseClient()
  const profilesTable = providerTable(client, 'provider_protocol_profiles')
  const providersTable = providerTable(client, 'providers')
  const rows = await client.query<{ id: string }>(`
    SELECT ppp.id
    FROM ${profilesTable} ppp
    INNER JOIN ${providersTable} p
      ON p.code = ppp.provider_code
    WHERE ${providerEnabledPredicate(client, 'p.enabled')}
      AND ${providerEnabledPredicate(client, 'ppp.enabled')}
      AND ppp.protocol_code = ?
      AND ppp.protocol_version = ?
    ORDER BY ppp.id ASC
    LIMIT ?
  `, [OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION, maxProviderProtocolProfiles])
  return rows.map((row) => row.id)
}

export function isOpenAIProtocolProviderCode(providerCode: string): boolean {
  return isProtocolProviderCode(providerCode, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION)
}

export async function isOpenAIProtocolProviderCodeAsync(providerCode: string): Promise<boolean> {
  return await isProtocolProviderCodeAsync(providerCode, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION)
}

export function isProtocolProviderCode(providerCode: string, protocolCode: string, protocolVersion?: string): boolean {
  const code = providerCode.trim()
  if (!code) return false
  const normalizedProtocolCode = protocolCode.trim()
  const normalizedProtocolVersion = protocolVersion?.trim()
  if (!normalizedProtocolCode) return false
  const versionClause = normalizedProtocolVersion
    ? 'AND protocol_version = ?'
    : ''
  const params = normalizedProtocolVersion
    ? [code, normalizedProtocolCode, normalizedProtocolVersion]
    : [code, normalizedProtocolCode]
  const row = getBusinessDatabase()
    .prepare(`
      SELECT 1
      FROM provider_protocol_profiles
      INNER JOIN providers ON providers.code = provider_protocol_profiles.provider_code
      WHERE provider_protocol_profiles.provider_code = ?
        AND providers.enabled = 1
        AND provider_protocol_profiles.enabled = 1
        AND protocol_code = ?
        ${versionClause}
      LIMIT 1
    `)
    .get(...params) as unknown
  return Boolean(row)
}

export async function isProtocolProviderCodeAsync(providerCode: string, protocolCode: string, protocolVersion?: string): Promise<boolean> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'is_protocol_provider_code_read_only',
      providerCode,
      protocolCode,
      protocolVersion
    })
  }
  const code = providerCode.trim()
  if (!code) return false
  const normalizedProtocolCode = protocolCode.trim()
  const normalizedProtocolVersion = protocolVersion?.trim()
  if (!normalizedProtocolCode) return false
  const versionClause = normalizedProtocolVersion
    ? 'AND ppp.protocol_version = ?'
    : ''
  const params = normalizedProtocolVersion
    ? [code, normalizedProtocolCode, normalizedProtocolVersion]
    : [code, normalizedProtocolCode]
  const client = await getProviderDatabaseClient()
  const profilesTable = providerTable(client, 'provider_protocol_profiles')
  const providersTable = providerTable(client, 'providers')
  const row = await client.one(`
    SELECT 1
    FROM ${profilesTable} ppp
    INNER JOIN ${providersTable} p
      ON p.code = ppp.provider_code
    WHERE ppp.provider_code = ?
      AND ${providerEnabledPredicate(client, 'p.enabled')}
      AND ${providerEnabledPredicate(client, 'ppp.enabled')}
      AND ppp.protocol_code = ?
      ${versionClause}
    LIMIT 1
  `, params)
  return Boolean(row)
}

export function findProviderDefaultHealthCheckModel(providerCode: string, systemAccountId?: string): string | undefined {
  const code = providerCode.trim()
  if (!code) return undefined
  if (shouldReadProviderDefaultHealthCheckModelPreference(systemAccountId)) {
    const preference = findProviderDefaultHealthCheckModelPreference(code, systemAccountId)
    if (preference) return preference
  }
  const systemDefault = findProviderSystemDefaultHealthCheckModel(code)
  if (systemDefault) return systemDefault
  const row = getBusinessDatabase()
    .prepare(`
      SELECT provider_protocol_profiles.default_health_check_model
      FROM provider_protocol_profiles
      INNER JOIN providers ON providers.code = provider_protocol_profiles.provider_code
      WHERE provider_protocol_profiles.provider_code = ?
        AND providers.enabled = 1
        AND provider_protocol_profiles.enabled = 1
      ORDER BY provider_protocol_profiles.updated_at DESC, provider_protocol_profiles.id ASC
      LIMIT 1
    `)
    .get(code) as unknown as { default_health_check_model?: string | null } | undefined
  const model = row?.default_health_check_model?.trim()
  return model || undefined
}

export async function findProviderDefaultHealthCheckModelAsync(providerCode: string, systemAccountId?: string): Promise<string | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_provider_default_health_check_model_read_only',
      providerCode,
      systemAccountId
    })
  }
  const code = providerCode.trim()
  if (!code) return undefined
  if (await shouldReadProviderDefaultHealthCheckModelPreferenceAsync(systemAccountId)) {
    const preference = await findProviderDefaultHealthCheckModelPreferenceAsync(code, systemAccountId)
    if (preference) return preference
  }
  const systemDefault = await findProviderSystemDefaultHealthCheckModelAsync(code)
  if (systemDefault) return systemDefault
  const client = await getProviderDatabaseClient()
  const profilesTable = providerTable(client, 'provider_protocol_profiles')
  const providersTable = providerTable(client, 'providers')
  const row = await client.one<{ default_health_check_model?: string | null }>(`
    SELECT ppp.default_health_check_model
    FROM ${profilesTable} ppp
    INNER JOIN ${providersTable} p
      ON p.code = ppp.provider_code
    WHERE ppp.provider_code = ?
      AND ${providerEnabledPredicate(client, 'p.enabled')}
      AND ${providerEnabledPredicate(client, 'ppp.enabled')}
    ORDER BY ppp.updated_at DESC, ppp.id ASC
    LIMIT 1
  `, [code])
  const model = row?.default_health_check_model?.trim()
  return model || undefined
}

function shouldReadProviderDefaultHealthCheckModelPreference(systemAccountId?: string): boolean {
  const id = systemAccountId?.trim()
  if (!id) return true
  const row = getBusinessDatabase()
    .prepare(`
      SELECT role
      FROM system_accounts
      WHERE id = ?
      LIMIT 1
    `)
    .get(id) as unknown as SystemAccountRoleRow | undefined
  return !isAdminRole(row?.role)
}

async function shouldReadProviderDefaultHealthCheckModelPreferenceAsync(systemAccountId?: string): Promise<boolean> {
  const id = systemAccountId?.trim()
  if (!id) return true
  const client = await getProviderDatabaseClient()
  const row = await client.one<SystemAccountRoleRow>(`
    SELECT role
    FROM ${providerTable(client, 'system_accounts')}
    WHERE id = ?
    LIMIT 1
  `, [id])
  return !isAdminRole(row?.role)
}

export function findProviderDefaultSupportedModels(providerCode: string): string[] {
  const code = providerCode.trim()
  if (!code) return []
  const row = getBusinessDatabase()
    .prepare(`
      SELECT default_supported_models_json
      FROM providers
      WHERE code = ?
        AND enabled = 1
      LIMIT 1
    `)
    .get(code) as unknown as Pick<ProviderRow, 'default_supported_models_json'> | undefined
  return providerDefaultSupportedModels(row?.default_supported_models_json)
}

export async function findProviderDefaultSupportedModelsAsync(providerCode: string): Promise<string[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_provider_default_supported_models_read_only',
      providerCode
    })
  }
  const code = providerCode.trim()
  if (!code) return []
  const client = await getProviderDatabaseClient()
  const row = await client.one<Pick<ProviderRow, 'default_supported_models_json'>>(`
    SELECT default_supported_models_json
    FROM ${providerTable(client, 'providers')}
    WHERE code = ?
      AND ${providerEnabledPredicate(client, 'enabled')}
    LIMIT 1
  `, [code])
  return providerDefaultSupportedModels(row?.default_supported_models_json)
}

export function findProviderProtocolProfile(profileId: string): ProviderProtocolProfileDefinition | undefined {
  const id = profileId.trim()
  if (!id) return undefined
  return listProviderProtocolProfiles().find((profile) => profile.id === id)
}

export async function findProviderProtocolProfileAsync(profileId: string): Promise<ProviderProtocolProfileDefinition | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_provider_protocol_profile_read_only',
      profileId
    })
  }
  const id = profileId.trim()
  if (!id) return undefined
  return (await listProviderProtocolProfilesAsync()).find((profile) => profile.id === id)
}

export function defaultProviderProtocolProfile(providerCode: string): ProviderProtocolProfileDefinition | undefined {
  const code = providerCode.trim()
  if (!code) return undefined
  return preferredDefaultProtocolProfile(listProviderProtocolProfiles([code]).filter((profile) => profile.providerCode === code))
}

export async function defaultProviderProtocolProfileAsync(providerCode: string): Promise<ProviderProtocolProfileDefinition | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'default_provider_protocol_profile_read_only',
      providerCode
    })
  }
  const code = providerCode.trim()
  if (!code) return undefined
  return preferredDefaultProtocolProfile((await listProviderProtocolProfilesAsync([code])).filter((profile) => profile.providerCode === code))
}

export function requireEnabledProviderProtocolProfile(providerCode: string, profileIdInput: unknown): ProviderProtocolProfileDefinition {
  const provider = requireEnabledProvider(providerCode)
  const profileId = typeof profileIdInput === 'string' && profileIdInput.trim()
    ? profileIdInput.trim()
    : ''
  if (!profileId) {
    throw new Error('供应商协议档案不能为空')
  }
  const profile = findProviderProtocolProfile(profileId)
  if (!profile || profile.providerCode !== providerCode) {
    throw new Error(`供应商协议档案无效：${profileId || providerCode}`)
  }
  if (!profile.enabled) {
    throw new Error(`供应商协议档案已停用：${profile.name}`)
  }
  return profile
}

export async function requireEnabledProviderProtocolProfileAsync(providerCode: string, profileIdInput: unknown): Promise<ProviderProtocolProfileDefinition> {
  return requireEnabledProviderProtocolProfileInClientAsync(await getProviderDatabaseClient(), providerCode, profileIdInput)
}

export async function requireEnabledProviderProtocolProfileInClientAsync(
  client: DatabaseClient,
  providerCode: string,
  profileIdInput: unknown
): Promise<ProviderProtocolProfileDefinition> {
  const profileId = typeof profileIdInput === 'string' && profileIdInput.trim()
    ? profileIdInput.trim()
    : ''
  const row = await client.one<ProviderProtocolProfileRow & { provider_enabled: number | boolean | string }>(`
    SELECT ppp.id, ppp.provider_code, ppp.name, ppp.description, ppp.enabled,
      ppp.protocol_code, ppp.protocol_version, ppp.base_url,
      ppp.default_health_check_model, ppp.account_types_json, ppp.capabilities_json,
      p.enabled AS provider_enabled
    FROM ${providerTable(client, 'providers')} p
    LEFT JOIN ${providerTable(client, 'provider_protocol_profiles')} ppp
      ON ppp.provider_code = p.code
      AND ppp.id = ?
    WHERE p.code = ?
    LIMIT 1
  `, [profileId, providerCode])
  if (!row) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (Number(row.provider_enabled) !== 1) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  if (!profileId) {
    throw new Error('供应商协议档案不能为空')
  }
  if (!row.id || row.provider_code !== providerCode) {
    throw new Error(`供应商协议档案无效：${profileId || providerCode}`)
  }
  if (Number(row.enabled) !== 1) {
    throw new Error(`供应商协议档案已停用：${row.name}`)
  }
  const families = await providerEndpointFamiliesByProfileIdAsync(client, [profileId])
  return {
    id: row.id,
    providerCode: row.provider_code,
    name: row.name,
    description: row.description ?? undefined,
    enabled: true,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    baseUrl: row.base_url,
    defaultHealthCheckModel: row.default_health_check_model,
    accountTypes: parseJsonArray(row.account_types_json) as AccountType[],
    capabilities: parseJsonArray(row.capabilities_json),
    endpointFamilies: families.get(profileId) ?? []
  }
}

function listProviderProtocolProfiles(providerCodes?: string[]): ProviderProtocolProfileDefinition[] {
  const normalizedProviderCodes = [...new Set((providerCodes ?? []).map((code) => code.trim()).filter(Boolean))]
  const providerFilter = normalizedProviderCodes.length
    ? `WHERE provider_code IN (${normalizedProviderCodes.map(() => '?').join(', ')})`
    : ''
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, provider_code, name, description, enabled, protocol_code, protocol_version,
        base_url, default_health_check_model, account_types_json, capabilities_json
      FROM provider_protocol_profiles
      ${providerFilter}
      ORDER BY provider_code ASC, updated_at DESC, id ASC
      LIMIT ?
    `)
    .all(...normalizedProviderCodes, maxProviderProtocolProfiles) as unknown as ProviderProtocolProfileRow[]
  const familiesByProfileId = providerEndpointFamiliesByProfileId(rows.map((row) => row.id))
  return rows.map((row) => ({
    id: row.id,
    providerCode: row.provider_code,
    name: row.name,
    description: row.description ?? undefined,
    enabled: isProviderEnabled(row.enabled),
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    baseUrl: row.base_url,
    defaultHealthCheckModel: row.default_health_check_model,
    accountTypes: parseJsonArray(row.account_types_json) as AccountType[],
    capabilities: parseJsonArray(row.capabilities_json),
    endpointFamilies: familiesByProfileId.get(row.id) ?? []
  }))
}

async function listProviderProtocolProfilesAsync(providerCodes?: string[], clientInput?: DatabaseClient): Promise<ProviderProtocolProfileDefinition[]> {
  const client = clientInput ?? await getProviderDatabaseClient()
  const normalizedProviderCodes = [...new Set((providerCodes ?? []).map((code) => code.trim()).filter(Boolean))]
  const providerFilter = normalizedProviderCodes.length
    ? `WHERE ppp.provider_code IN (${client.dialect.bindPlaceholders(normalizedProviderCodes.length)})`
    : ''
  const profilesTable = providerTable(client, 'provider_protocol_profiles')
  const rows = await client.query<ProviderProtocolProfileRow>(`
    SELECT ppp.id, ppp.provider_code, ppp.name, ppp.description, ppp.enabled, ppp.protocol_code, ppp.protocol_version,
      ppp.base_url, ppp.default_health_check_model, ppp.account_types_json, ppp.capabilities_json
    FROM ${profilesTable} ppp
    ${providerFilter}
    ORDER BY ppp.provider_code ASC, ppp.updated_at DESC, ppp.id ASC
    LIMIT ?
  `, [...normalizedProviderCodes, maxProviderProtocolProfiles])
  const familiesByProfileId = await providerEndpointFamiliesByProfileIdAsync(client, rows.map((row) => row.id))
  return rows.map((row) => ({
    id: row.id,
    providerCode: row.provider_code,
    name: row.name,
    description: row.description ?? undefined,
    enabled: isProviderEnabled(row.enabled),
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    baseUrl: row.base_url,
    defaultHealthCheckModel: row.default_health_check_model,
    accountTypes: parseJsonArray(row.account_types_json) as AccountType[],
    capabilities: parseJsonArray(row.capabilities_json),
    endpointFamilies: familiesByProfileId.get(row.id) ?? []
  }))
}

function requireEnabledProvider(providerCode: string): ProviderDefinition {
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  return provider
}

function providerProtocolProfilesByProviderCode(providerCodes: ProviderCode[]): Map<ProviderCode, ProviderProtocolProfileDefinition[]> {
  const profiles = listProviderProtocolProfiles(providerCodes)
  const result = new Map<ProviderCode, ProviderProtocolProfileDefinition[]>()
  for (const profile of profiles) {
    const items = result.get(profile.providerCode) ?? []
    items.push(profile)
    result.set(profile.providerCode, items)
  }
  return result
}

async function providerProtocolProfilesByProviderCodeAsync(client: DatabaseClient, providerCodes: ProviderCode[]): Promise<Map<ProviderCode, ProviderProtocolProfileDefinition[]>> {
  const profiles = await listProviderProtocolProfilesAsync(providerCodes, client)
  const result = new Map<ProviderCode, ProviderProtocolProfileDefinition[]>()
  for (const profile of profiles) {
    const items = result.get(profile.providerCode) ?? []
    items.push(profile)
    result.set(profile.providerCode, items)
  }
  return result
}

function providerEndpointFamiliesByProfileId(profileIds: string[]): Map<string, ProtocolEndpointFamilyDefinition[]> {
  const ids = [...new Set(profileIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map()
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT provider_protocol_profile_families.profile_id,
        protocol_endpoint_families.family_code,
        protocol_endpoint_families.name,
        protocol_endpoint_families.description
      FROM provider_protocol_profile_families
      INNER JOIN provider_protocol_profiles
        ON provider_protocol_profiles.id = provider_protocol_profile_families.profile_id
      INNER JOIN protocol_endpoint_families
        ON protocol_endpoint_families.protocol_code = provider_protocol_profiles.protocol_code
        AND protocol_endpoint_families.protocol_version = provider_protocol_profiles.protocol_version
        AND protocol_endpoint_families.family_code = provider_protocol_profile_families.family_code
      WHERE provider_protocol_profile_families.profile_id IN (${ids.map(() => '?').join(', ')})
        AND provider_protocol_profile_families.enabled = 1
        AND protocol_endpoint_families.enabled = 1
      ORDER BY provider_protocol_profile_families.profile_id ASC, protocol_endpoint_families.family_code ASC
    `)
    .all(...ids) as unknown as ProviderProfileFamilyRow[]
  const result = new Map<string, ProtocolEndpointFamilyDefinition[]>()
  for (const row of rows) {
    const items = result.get(row.profile_id) ?? []
    items.push({
      code: row.family_code,
      name: row.name,
      description: row.description ?? undefined
    })
    result.set(row.profile_id, items)
  }
  return result
}

async function providerEndpointFamiliesByProfileIdAsync(client: DatabaseClient, profileIds: string[]): Promise<Map<string, ProtocolEndpointFamilyDefinition[]>> {
  const ids = [...new Set(profileIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map()
  const profileFamiliesTable = providerTable(client, 'provider_protocol_profile_families')
  const profilesTable = providerTable(client, 'provider_protocol_profiles')
  const familiesTable = providerTable(client, 'protocol_endpoint_families')
  const rows = await client.query<ProviderProfileFamilyRow>(`
    SELECT ppf.profile_id,
      f.family_code,
      f.name,
      f.description
    FROM ${profileFamiliesTable} ppf
    INNER JOIN ${profilesTable} ppp
      ON ppp.id = ppf.profile_id
    INNER JOIN ${familiesTable} f
      ON f.protocol_code = ppp.protocol_code
      AND f.protocol_version = ppp.protocol_version
      AND f.family_code = ppf.family_code
    WHERE ppf.profile_id IN (${client.dialect.bindPlaceholders(ids.length)})
      AND ${providerEnabledPredicate(client, 'ppf.enabled')}
      AND ${providerEnabledPredicate(client, 'f.enabled')}
    ORDER BY ppf.profile_id ASC, f.family_code ASC
  `, ids)
  const result = new Map<string, ProtocolEndpointFamilyDefinition[]>()
  for (const row of rows) {
    const items = result.get(row.profile_id) ?? []
    items.push({
      code: row.family_code,
      name: row.name,
      description: row.description ?? undefined
    })
    result.set(row.profile_id, items)
  }
  return result
}

async function getProviderDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function providerTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function providerEnabledPredicate(client: DatabaseClient, column: string): string {
  return `${column} = ${client.driver === 'postgres' ? 'TRUE' : '1'}`
}

function isProviderEnabled(value: number | boolean): boolean {
  return value === true || value === 1
}

function providerDefaultProfileFields(profiles: ProviderProtocolProfileDefinition[]): Omit<ProviderDefinition, 'id' | 'code' | 'name' | 'parentCode' | 'description' | 'enabled' | 'defaultSupportedModels' | 'protocolProfiles'> {
  const defaultProfile = preferredDefaultProtocolProfile(profiles)
  if (!defaultProfile) {
    return {
      defaultProtocolProfileId: '',
      protocolCode: '',
      protocolVersion: '',
      baseUrl: '',
      defaultHealthCheckModel: '',
      accountTypes: [],
      capabilities: []
    }
  }
  return {
    defaultProtocolProfileId: defaultProfile.id,
    protocolCode: defaultProfile.protocolCode,
    protocolVersion: defaultProfile.protocolVersion,
    baseUrl: defaultProfile.baseUrl,
    defaultHealthCheckModel: defaultProfile.defaultHealthCheckModel,
    accountTypes: defaultProfile.accountTypes,
    capabilities: defaultProfile.capabilities
  }
}

function providerDefaultSupportedModels(value: unknown): string[] {
  if (typeof value !== 'string' || !value.trim()) return []
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of parseJsonArray(value)) {
    const model = item.trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    output.push(model)
  }
  return output
}

function preferredDefaultProtocolProfile(profiles: ProviderProtocolProfileDefinition[]): ProviderProtocolProfileDefinition | undefined {
  const enabledProfiles = profiles.filter((profile) => profile.enabled)
  const candidates = enabledProfiles.length ? enabledProfiles : profiles
  const geminiNativeProfile = candidates.find((profile) => (
    profile.providerCode === GEMINI_PROVIDER_CODE
    && profile.id === GEMINI_NATIVE_V1BETA_PROFILE_ID
  ))
  const glmCodingPlanProfile = candidates.find((profile) => (
    profile.providerCode === GLM_PROVIDER_CODE
    && profile.id === GLM_CODING_OPENAI_V1_PROFILE_ID
  ))
  return geminiNativeProfile ?? glmCodingPlanProfile ?? candidates[0]
}

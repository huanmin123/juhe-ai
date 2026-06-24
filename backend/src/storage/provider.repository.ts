import type { AccountType, ProviderCode, ProviderDefinition, ProviderProtocolProfileDefinition, ProtocolEndpointFamilyDefinition } from '../domain/types.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
} from '../domain/provider-protocol.js'
import { getBusinessDatabase } from './database.js'
import { parseJsonArray } from './value-utils.js'

interface ProviderRow {
  id: string
  code: ProviderCode
  name: string
  parent_code: ProviderCode | null
  description: string | null
  enabled: number
}

interface ProviderProtocolProfileRow {
  id: string
  provider_code: ProviderCode
  name: string
  description: string | null
  enabled: number
  protocol_code: string
  protocol_version: string
  base_url: string
  default_test_model: string
  account_types_json: string
  capabilities_json: string
}

interface ProviderProfileFamilyRow {
  profile_id: string
  family_code: string
  name: string
  description: string | null
}

const maxProviderDefinitions = 50
const maxProviderProtocolProfiles = 200

export function listProviders(): ProviderDefinition[] {
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, code, name, parent_code, description, enabled
      FROM providers
      ORDER BY name ASC, code ASC
      LIMIT ?
    `)
    .all(maxProviderDefinitions) as unknown as ProviderRow[]
  const profilesByProvider = providerProtocolProfilesByProviderCode(rows.map((row) => row.code))
  return rows.map((row) => ({
    id: row.id,
    code: row.code,
    name: row.name,
    parentCode: row.parent_code ?? undefined,
    description: row.description ?? undefined,
    enabled: row.enabled === 1,
    ...providerDefaultProfileFields(profilesByProvider.get(row.code) ?? []),
    protocolProfiles: profilesByProvider.get(row.code) ?? []
  }))
}

export function listOpenAIProtocolProviderCodes(): ProviderCode[] {
  return listProtocolProviderCodes(OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION)
}

export function listAnthropicProtocolProviderCodes(): ProviderCode[] {
  return listProtocolProviderCodes(ANTHROPIC_PROTOCOL_CODE, ANTHROPIC_PROTOCOL_VERSION)
}

function listProtocolProviderCodes(protocolCode: string, protocolVersion: string): ProviderCode[] {
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

export function isOpenAIProtocolProviderCode(providerCode: string): boolean {
  return isProtocolProviderCode(providerCode, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION)
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

export function findProviderDefaultTestModel(providerCode: string): string | undefined {
  const code = providerCode.trim()
  if (!code) return undefined
  const row = getBusinessDatabase()
    .prepare(`
      SELECT provider_protocol_profiles.default_test_model
      FROM provider_protocol_profiles
      INNER JOIN providers ON providers.code = provider_protocol_profiles.provider_code
      WHERE provider_protocol_profiles.provider_code = ?
        AND providers.enabled = 1
        AND provider_protocol_profiles.enabled = 1
      ORDER BY provider_protocol_profiles.updated_at DESC, provider_protocol_profiles.id ASC
      LIMIT 1
    `)
    .get(code) as unknown as { default_test_model?: string | null } | undefined
  const model = row?.default_test_model?.trim()
  return model || undefined
}

export function findProviderProtocolProfile(profileId: string): ProviderProtocolProfileDefinition | undefined {
  const id = profileId.trim()
  if (!id) return undefined
  return listProviderProtocolProfiles().find((profile) => profile.id === id)
}

export function defaultProviderProtocolProfile(providerCode: string): ProviderProtocolProfileDefinition | undefined {
  const code = providerCode.trim()
  if (!code) return undefined
  return listProviderProtocolProfiles([code]).find((profile) => profile.providerCode === code)
}

export function requireEnabledProviderProtocolProfile(providerCode: string, profileIdInput: unknown): ProviderProtocolProfileDefinition {
  const provider = requireEnabledProvider(providerCode)
  const profileId = typeof profileIdInput === 'string' && profileIdInput.trim()
    ? profileIdInput.trim()
    : provider.defaultProtocolProfileId
  const profile = profileId ? findProviderProtocolProfile(profileId) : defaultProviderProtocolProfile(providerCode)
  if (!profile || profile.providerCode !== providerCode) {
    throw new Error(`供应商协议档案无效：${profileId || providerCode}`)
  }
  if (!profile.enabled) {
    throw new Error(`供应商协议档案已停用：${profile.name}`)
  }
  return profile
}

function listProviderProtocolProfiles(providerCodes?: string[]): ProviderProtocolProfileDefinition[] {
  const normalizedProviderCodes = [...new Set((providerCodes ?? []).map((code) => code.trim()).filter(Boolean))]
  const providerFilter = normalizedProviderCodes.length
    ? `WHERE provider_code IN (${normalizedProviderCodes.map(() => '?').join(', ')})`
    : ''
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, provider_code, name, description, enabled, protocol_code, protocol_version,
        base_url, default_test_model, account_types_json, capabilities_json
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
    enabled: row.enabled === 1,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    baseUrl: row.base_url,
    defaultTestModel: row.default_test_model,
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

function providerDefaultProfileFields(profiles: ProviderProtocolProfileDefinition[]): Omit<ProviderDefinition, 'id' | 'code' | 'name' | 'description' | 'enabled' | 'protocolProfiles'> {
  const defaultProfile = profiles.find((profile) => profile.enabled) ?? profiles[0]
  if (!defaultProfile) {
    return {
      defaultProtocolProfileId: '',
      protocolCode: '',
      protocolVersion: '',
      baseUrl: '',
      defaultTestModel: '',
      accountTypes: [],
      capabilities: []
    }
  }
  return {
    defaultProtocolProfileId: defaultProfile.id,
    protocolCode: defaultProfile.protocolCode,
    protocolVersion: defaultProfile.protocolVersion,
    baseUrl: defaultProfile.baseUrl,
    defaultTestModel: defaultProfile.defaultTestModel,
    accountTypes: defaultProfile.accountTypes,
    capabilities: defaultProfile.capabilities
  }
}

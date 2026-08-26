import type {
  GroupSchedulingPolicy,
  GroupSummary,
  GroupType,
  ProviderCode
} from '../../../../domain/types.js'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
} from '../../../../domain/provider-protocol.js'
import {
  groupSchedulingPolicyJson,
  normalizeGroupType,
  parseGroupSchedulingPolicyJson
} from '../../../../domain/group-scheduling.js'
import {
  currentSystemAccountId,
  includeSystemAccountFields,
  manageableSystemAccountId,
  type AccessScope
} from '../../../../storage/access-scope.js'
import { invalidateGatewayRuntimeAfterBusinessWrite } from '../../../../storage/account-runtime-mutation-helpers.js'
import { getBusinessDatabase, newId, nowIso } from '../../../../storage/database.js'
import { emptyGroupAccountStats } from '../../../../storage/group-account-stats.mapper.js'
import { invalidateGroupLookupCache } from '../../../../storage/repository-lookups.js'
import { providerCode as defaultProviderCode } from '../shared.js'

interface MockGroupInput {
  name: string
  providerCode?: ProviderCode
  description?: string
  enabled?: boolean
  isDefault?: boolean
  groupType?: GroupType
  schedulingPolicy?: GroupSchedulingPolicy
}

interface GroupRow {
  id: string
  system_account_id: string
  name: string
  provider_code: ProviderCode
  description: string | null
  enabled: number
  is_default: number
  group_type: GroupType | null
  scheduling_policy_json: string | null
}

let groupProtocolColumnsEnabled: boolean | undefined

export function createMockGroup(input: MockGroupInput, access?: AccessScope): GroupSummary {
  const systemAccountId = manageableSystemAccountId(access) ?? currentSystemAccountId(access)
  return insertMockGroup(input, systemAccountId, access)
}

export function ensureMockDefaultGptGroup(systemAccountId: string): GroupSummary {
  const existing = getBusinessDatabase()
    .prepare(`
      SELECT id, system_account_id, name, provider_code, description, enabled, is_default, group_type, scheduling_policy_json
      FROM groups
      WHERE system_account_id = ? AND provider_code = ? AND is_default = 1
      ORDER BY updated_at DESC, id ASC
      LIMIT 1
    `)
    .get(systemAccountId, defaultProviderCode) as unknown as GroupRow | undefined
  if (existing) {
    return groupSummaryFromRow(existing)
  }
  return insertMockGroup({
    name: '默认 GPT 分组',
    providerCode: defaultProviderCode,
    description: '',
    enabled: true,
    isDefault: true
  }, systemAccountId)
}

function insertMockGroup(input: MockGroupInput, systemAccountId: string, access?: AccessScope): GroupSummary {
  const now = nowIso()
  const id = newId('grp')
  const providerCode = input.providerCode ?? defaultProviderCode
  const groupType = normalizeGroupType(input.groupType)
  const schedulingPolicyJson = groupSchedulingPolicyJson(input.schedulingPolicy, groupType)
  const group: GroupSummary = {
    id,
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    ownerSystemAccountId: systemAccountId,
    name: input.name,
    providerCode,
    description: input.description,
    enabled: input.enabled ?? true,
    isDefault: input.isDefault ?? false,
    groupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(schedulingPolicyJson, groupType),
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  if (localGroupsRequireProtocolColumns()) {
    getBusinessDatabase()
      .prepare(`
        INSERT INTO groups (
          id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
          description, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        group.id,
        systemAccountId,
        group.name,
        group.providerCode,
        protocolProfileIdForGroup(group.providerCode),
        OPENAI_PROTOCOL_CODE,
        OPENAI_PROTOCOL_VERSION,
        group.description ?? null,
        group.enabled ? 1 : 0,
        group.isDefault ? 1 : 0,
        group.groupType,
        schedulingPolicyJson,
        now,
        now
      )
  } else {
    getBusinessDatabase()
      .prepare(`
        INSERT INTO groups (
          id, system_account_id, name, provider_code,
          description, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        group.id,
        systemAccountId,
        group.name,
        group.providerCode,
        group.description ?? null,
        group.enabled ? 1 : 0,
        group.isDefault ? 1 : 0,
        group.groupType,
        schedulingPolicyJson,
        now,
        now
      )
  }
  invalidateGroupLookupCache(group.id)
  invalidateGatewayRuntimeAfterBusinessWrite('mockdata_group_created')
  return group
}

function protocolProfileIdForGroup(providerCode: ProviderCode): string {
  if (providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE) {
    return OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
  }
  return GPT_OPENAI_V1_PROFILE_ID
}

function localGroupsRequireProtocolColumns(): boolean {
  if (groupProtocolColumnsEnabled !== undefined) {
    return groupProtocolColumnsEnabled
  }
  const rows = getBusinessDatabase()
    .prepare('PRAGMA table_info(groups)')
    .all() as unknown as Array<{ name?: string }>
  const columns = new Set(rows.map((row) => row.name).filter(Boolean))
  groupProtocolColumnsEnabled = columns.has('provider_protocol_profile_id')
    && columns.has('protocol_code')
    && columns.has('protocol_version')
  return groupProtocolColumnsEnabled
}

function groupSummaryFromRow(row: GroupRow): GroupSummary {
  const groupType = normalizeGroupType(row.group_type)
  return {
    id: row.id,
    ownerSystemAccountId: row.system_account_id,
    name: row.name,
    providerCode: row.provider_code,
    description: row.description ?? undefined,
    enabled: row.enabled === 1,
    isDefault: row.is_default === 1,
    groupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(row.scheduling_policy_json, groupType),
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
}

import { randomBytes } from 'node:crypto'

import type { AccountStatus, AccountSummary, GroupSummary, SystemAccountSummary } from '../../domain/types.js'
import {
  createAccount,
  createGroup,
  createSystemAccount,
  findAccountSummary,
  findGroupSummary,
  findSystemAccountByUsername,
  listAccounts,
  listGroupOptions,
  listProviders,
  updateAccount
} from '../../storage/repositories.js'
import { getDatabase, newId, nowIso, runInDatabaseTransaction } from '../../storage/database.js'

export interface PublicAccountPushInput {
  targetUsername: string
  targetDisplayName?: string
  targetGroupName: string
  providerCode?: string
  name: string
  type?: string
  baseUrl: string
  apiKey: string
  supportedModels?: string[]
  status?: 'active' | 'disabled'
  concurrencyLimit?: number
  priority?: number
  notes?: string
  externalId?: string
}

export interface PublicAccountPushResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'updated' | 'mock'
  target: {
    username: string
    displayName: string
    systemAccountId: string
    created: boolean
    groupId: string
    groupName: string
    groupCreated: boolean
  }
  account: {
    id: string
    name: string
    providerCode: string
    type: string
    status: AccountStatus
    supportedModels?: string[]
    boundGroupId?: string
    boundGroupName?: string
    schedulable: boolean
  }
  externalId?: string
}

type ResolvedTarget = {
  account: SystemAccountSummary
  created: boolean
}

type ResolvedGroup = {
  group: GroupSummary
  created: boolean
}

export function pushPublicWelfareAccount(input: PublicAccountPushInput): PublicAccountPushResponse {
  const providerCode = normalizedText(input.providerCode) || 'openai'
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  assertSupportedPushAccountType(input.type, provider.accountTypes)

  return runInDatabaseTransaction(() => {
    const target = ensureTargetSystemAccount(input)
    if (target.account.status !== 'active') {
      throw new Error(`目标用户已停用：${target.account.username}`)
    }

    const access = { systemAccountId: target.account.id, role: 'user' as const }
    const targetGroup = ensureTargetGroup({
      access,
      providerCode,
      groupName: input.targetGroupName
    })
    const existing = findTargetAccountByExternalId({
      access,
      providerCode,
      groupId: targetGroup.group.id,
      externalId: input.externalId
    }) ?? findTargetAccount({
      access,
      providerCode,
      groupId: targetGroup.group.id,
      name: input.name
    })
    const accountInput = accountInputForPush(input, providerCode, targetGroup.group.id)
    const account = existing
      ? updateAccount(existing.id, accountInput, access) ?? existing
      : createAccount(accountInput, access)
    upsertExternalAccountPushRecord({
      systemAccountId: target.account.id,
      providerCode,
      groupId: targetGroup.group.id,
      externalId: input.externalId,
      accountId: account.id
    })

    return {
      source: 'stats',
      generatedAt: new Date().toISOString(),
      action: existing ? 'updated' : 'created',
      target: {
        username: target.account.username,
        displayName: target.account.displayName,
        systemAccountId: target.account.id,
        created: target.created,
        groupId: targetGroup.group.id,
        groupName: targetGroup.group.name,
        groupCreated: targetGroup.created
      },
      account: sanitizeAccount(account),
      externalId: normalizedText(input.externalId)
    }
  }, getDatabase())
}

export function mockPublicWelfareAccountPush(input: PublicAccountPushInput): PublicAccountPushResponse {
  const generatedAt = new Date().toISOString()
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'openai'
  return {
    source: 'mock',
    generatedAt,
    action: 'mock',
    target: {
      username,
      displayName: normalizedText(input.targetDisplayName) || username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false,
      groupId: 'mock_group_welfare',
      groupName,
      groupCreated: false
    },
    account: {
      id: 'mock_account_public_welfare',
      name: input.name,
      providerCode,
      type: 'api_key',
      status: input.status === 'disabled' ? 'disabled' : 'active',
      supportedModels: normalizedStringList(input.supportedModels),
      boundGroupId: 'mock_group_welfare',
      boundGroupName: groupName,
      schedulable: input.status !== 'disabled'
    },
    externalId: normalizedText(input.externalId)
  }
}

function ensureTargetSystemAccount(input: PublicAccountPushInput): ResolvedTarget {
  const username = normalizedText(input.targetUsername)
  if (!username) {
    throw new Error('目标用户不能为空')
  }
  const existing = findSystemAccountByUsername(username)
  if (existing) {
    return { account: existing, created: false }
  }

  const displayName = normalizedText(input.targetDisplayName) || username
  const account = createSystemAccount({
    username,
    displayName,
    description: '由公益站公开接口推送自动创建',
    password: randomBytes(18).toString('base64url'),
    role: 'user',
    status: 'active',
    mustChangePassword: true
  })
  return { account, created: true }
}

function ensureTargetGroup(input: {
  access: { systemAccountId: string; role: 'user' }
  providerCode: string
  groupName: string
}): ResolvedGroup {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = listGroupOptions(input.access, {
    keyword: groupName,
    providerCode: input.providerCode,
    limit: 20
  }).find((item) => item.providerCode === input.providerCode && sameText(item.name, groupName))

  if (existing) {
    const group = findGroupSummary(existing.id, input.access)
    if (!group) {
      throw new Error('目标分组不存在')
    }
    return {
      group,
      created: false
    }
  }

  return {
    group: createGroup({
      name: groupName,
      providerCode: input.providerCode,
      description: '由公益站公开接口推送自动创建',
      enabled: true,
      groupType: 'personal'
    }, input.access),
    created: true
  }
}

function findTargetAccountByExternalId(input: {
  access: { systemAccountId: string; role: 'user' }
  providerCode: string
  groupId: string
  externalId?: string
}): AccountSummary | undefined {
  const externalId = normalizedText(input.externalId)
  if (!externalId) {
    return undefined
  }
  const row = getDatabase().prepare(`
    SELECT push_records.account_id AS id
    FROM external_integration_account_push_records push_records
    INNER JOIN accounts ON accounts.id = push_records.account_id
    INNER JOIN group_accounts ON group_accounts.account_id = accounts.id
      AND group_accounts.group_id = push_records.group_id
    WHERE push_records.system_account_id = ?
      AND push_records.provider_code = ?
      AND push_records.group_id = ?
      AND push_records.external_id = ?
      AND accounts.system_account_id = push_records.system_account_id
      AND accounts.provider_code = push_records.provider_code
    LIMIT 1
  `).get(
    input.access.systemAccountId,
    input.providerCode,
    input.groupId,
    externalId
  ) as { id?: string } | undefined
  return row?.id ? findAccountSummary(row.id, input.access) : undefined
}

function upsertExternalAccountPushRecord(input: {
  systemAccountId: string
  providerCode: string
  groupId: string
  externalId?: string
  accountId: string
}): void {
  const externalId = normalizedText(input.externalId)
  if (!externalId) return
  const now = nowIso()
  getDatabase().prepare(`
    INSERT INTO external_integration_account_push_records (
      id, system_account_id, provider_code, group_id, external_id, account_id, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, provider_code, group_id, external_id)
    DO UPDATE SET account_id = excluded.account_id, updated_at = excluded.updated_at
  `).run(
    newId('eapush'),
    input.systemAccountId,
    input.providerCode,
    input.groupId,
    externalId,
    input.accountId,
    now,
    now
  )
}

function findTargetAccount(input: {
  access: { systemAccountId: string; role: 'user' }
  providerCode: string
  groupId: string
  name: string
}): AccountSummary | undefined {
  const name = normalizedText(input.name)
  if (!name) {
    throw new Error('账户名称不能为空')
  }
  return listAccounts(input.access, {
    keyword: name,
    providerCode: input.providerCode,
    groupId: input.groupId,
    limit: 20
  }).find((item) => item.providerCode === input.providerCode && sameText(item.name, name))
}

function accountInputForPush(input: PublicAccountPushInput, providerCode: string, groupId: string): Record<string, unknown> {
  const name = normalizedText(input.name)
  const baseUrl = normalizedText(input.baseUrl)
  const apiKey = normalizedText(input.apiKey)
  if (!name) {
    throw new Error('账户名称不能为空')
  }
  if (!baseUrl) {
    throw new Error('Base URL 不能为空')
  }
  if (!apiKey) {
    throw new Error('API Key 不能为空')
  }

  return {
    providerCode,
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: baseUrl
    },
    supportedModels: normalizedStringList(input.supportedModels),
    status: input.status === 'disabled' ? 'disabled' : 'active',
    concurrencyLimit: boundedInteger(input.concurrencyLimit, 1, 100_000),
    priority: boundedInteger(input.priority, 0, 100_000) ?? 0,
    schedulable: input.status !== 'disabled',
    groupId,
    notes: pushNotes(input)
  }
}

function assertSupportedPushAccountType(value: unknown, providerAccountTypes: readonly string[]): void {
  const type = normalizedText(value) || 'api_key'
  if (type !== 'api_key') {
    throw new Error('公益账号推送仅支持 API Key 账户')
  }
  if (!providerAccountTypes.includes('api_key')) {
    throw new Error('当前供应商不支持 API Key 账户')
  }
}

function sanitizeAccount(account: AccountSummary): PublicAccountPushResponse['account'] {
  return {
    id: account.id,
    name: account.name,
    providerCode: account.providerCode,
    type: account.type,
    status: account.status,
    supportedModels: account.supportedModels,
    boundGroupId: account.boundGroupId,
    boundGroupName: account.boundGroupName,
    schedulable: account.schedulable
  }
}

function pushNotes(input: PublicAccountPushInput): string | undefined {
  const parts = [
    normalizedText(input.notes),
    normalizedText(input.externalId) ? `externalId=${normalizedText(input.externalId)}` : undefined
  ].filter((item): item is string => Boolean(item))
  return parts.length ? parts.join('\n') : undefined
}

function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizedStringList(values: readonly string[] | undefined): string[] | undefined {
  const normalized = [...new Set((values ?? []).map((item) => item.trim()).filter(Boolean))]
  return normalized.length ? normalized : undefined
}

function boundedInteger(value: unknown, min: number, max: number): number | undefined {
  const number = Math.trunc(Number(value))
  if (!Number.isFinite(number)) {
    return undefined
  }
  return Math.min(max, Math.max(min, number))
}

function sameText(left: string, right: string): boolean {
  return left.trim().toLowerCase() === right.trim().toLowerCase()
}

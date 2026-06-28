import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import {
  accountNameSearchQueryTerms,
  buildAccountNameSearchTerms,
  escapeAccountNameSearchLike,
  normalizeAccountNameSearchText
} from '../../storage/account-name-search.repository.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  deleteAccountAsync,
  deleteGroupAsync,
  listAccountsPageAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'AI 账户列表 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `account_list_pg_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
const plannerAccountIds: string[] = []
const plannerSystemAccountIds: string[] = []
const tagName = `list-pg-${marker}`
const plannerOwnerCount = 24
const plannerAccountsPerOwner = 24
const plannerOwnerListAccountCount = 768

try {
  await cleanupSmokeRows()

  const matchedGroup = await createGroupAsync({
    name: `AI 账户列表 PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(matchedGroup.id)

  const otherGroup = await createGroupAsync({
    name: `AI 账户列表 PG smoke 其他分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(otherGroup.id)

  const keyword = `账户列表 PG 检索目标 ${marker}`
  const matchedByName = await createSmokeAccount({
    name: keyword,
    groupId: matchedGroup.id,
    tags: [tagName],
    status: 'active'
  })
  const tagId = matchedByName.tags?.find((tag) => tag.name === tagName)?.id
  assert(tagId, 'PG 创建账户后应返回可用于列表筛选的标签 ID')

  const matchedByPrefix = await createSmokeAccount({
    name: `${keyword} 扩展`,
    groupId: matchedGroup.id,
    status: 'active'
  })
  const middleNameOnly = await createSmokeAccount({
    name: `普通 ${keyword} 中间`,
    groupId: matchedGroup.id,
    status: 'active'
  })
  const notesOnly = await createSmokeAccount({
    name: `备注字段账户 ${marker}`,
    groupId: matchedGroup.id,
    notes: `${keyword} 只出现在备注`,
    status: 'active'
  })
  const otherGroupOnly = await createSmokeAccount({
    name: `其他分组账户 ${marker}`,
    groupId: otherGroup.id,
    status: 'active'
  })
  const disabledAccount = await createSmokeAccount({
    name: `停用账户 ${marker}`,
    groupId: matchedGroup.id,
    status: 'disabled'
  })
  const unschedulableAccount = await createSmokeAccount({
    name: `禁用调度账户 ${marker}`,
    groupId: matchedGroup.id,
    schedulable: false,
    status: 'active'
  })
  const wildcardLiteral = await createSmokeAccount({
    name: `percent%literal ${marker}`,
    groupId: matchedGroup.id,
    status: 'active'
  })
  const wildcardNeighbor = await createSmokeAccount({
    name: `percentXliteral ${marker}`,
    groupId: matchedGroup.id,
    status: 'active'
  })

  const keywordResult = await listAccountsPageAsync(access, { keyword, page: 1, pageSize: 50 })
  const keywordIds = keywordResult.items.map((item) => item.id)
  assert(keywordIds.includes(matchedByName.id), 'PG AI 账户列表 keyword 应命中名称精确值')
  assert(keywordIds.includes(matchedByPrefix.id), 'PG AI 账户列表 keyword 应命中名称前缀值')
  assert(keywordIds.includes(middleNameOnly.id), 'PG AI 账户列表 keyword 应命中名称中间包含值')
  assert(!keywordIds.includes(notesOnly.id), 'PG AI 账户列表 keyword 不应扫描备注字段命中')
  assert.deepEqual(keywordResult.items.find((item) => item.id === matchedByName.id)?.credentials, {}, 'PG AI 账户列表不应返回完整凭据')

  const wildcardResult = await listAccountsPageAsync(access, { keyword: `percent%literal ${marker}`, page: 1, pageSize: 50 })
  const wildcardIds = wildcardResult.items.map((item) => item.id)
  assert(wildcardIds.includes(wildcardLiteral.id), 'PG AI 账户列表应把 % 当作字面量处理')
  assert(!wildcardIds.includes(wildcardNeighbor.id), 'PG AI 账户列表不应把用户输入的 % 当作 LIKE 通配符')

  const groupResult = await listAccountsPageAsync(access, { groupId: matchedGroup.id, status: 'active', page: 1, pageSize: 50 })
  const groupIds = groupResult.items.map((item) => item.id)
  assert(groupIds.includes(matchedByName.id), 'PG AI 账户列表 groupId 筛选应命中绑定分组账户')
  assert(!groupIds.includes(otherGroupOnly.id), 'PG AI 账户列表 groupId 筛选不应混入其他分组账户')
  assert(!groupIds.includes(disabledAccount.id), 'PG AI 账户列表 active 状态筛选不应返回停用账户')

  const tagResult = await listAccountsPageAsync(access, { tagIds: [tagId], page: 1, pageSize: 50 })
  const tagIds = tagResult.items.map((item) => item.id)
  assert(tagIds.includes(matchedByName.id), 'PG AI 账户列表 tagIds 筛选应命中绑定标签账户')
  assert(!tagIds.includes(matchedByPrefix.id), 'PG AI 账户列表 tagIds 筛选不应混入未绑定标签账户')

  const schedulableResult = await listAccountsPageAsync(access, { schedulable: 'enabled', page: 1, pageSize: 50 })
  const schedulableIds = schedulableResult.items.map((item) => item.id)
  assert(schedulableIds.includes(matchedByName.id), 'PG AI 账户列表 schedulable=enabled 应命中可调度账户')
  assert(!schedulableIds.includes(unschedulableAccount.id), 'PG AI 账户列表 schedulable=enabled 不应返回禁用调度账户')

  await seedAccountListPlannerRows(keyword)
  await assertAccountListIndexedPlans(access.systemAccountId, matchedGroup.id, tagId, keyword)

  console.log(JSON.stringify({
    message: 'AI 账户列表 PG smoke 通过',
    groupId: matchedGroup.id,
    matchedAccountId: matchedByName.id,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function createSmokeAccount(input: {
  name: string
  groupId: string
  notes?: string
  tags?: string[]
  status: 'active' | 'disabled'
  schedulable?: boolean
}) {
  const createInput: Record<string, unknown> = {
    providerCode: 'gpt',
    name: input.name,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-list-pg-smoke-${marker}-${createdAccountIds.length}`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: input.groupId,
    notes: input.notes,
    tags: input.tags,
    supportedModels: ['gpt-4o-mini'],
    status: input.status
  }
  if (input.schedulable !== undefined) {
    createInput.schedulable = input.schedulable
  }
  const account = await createAccountAsync(createInput, access)
  createdAccountIds.push(account.id)
  return account
}

async function assertAccountListIndexedPlans(
  systemAccountId: string,
  groupId: string,
  tagId: string,
  keyword: string
): Promise<void> {
  const normalizedKeyword = normalizeAccountNameSearchText(keyword)
  const terms = accountNameSearchQueryTerms(keyword)
  assert(terms.length > 0, 'PG AI 账户列表名称包含 explain 需要搜索词项')
  const systemAccountParamIndex = 1
  const firstTermParamIndex = 2
  const containsParamIndex = terms.length + 2
  const termCountParamIndex = terms.length + 3
  const termPlaceholdersAfterOwner = terms.map((_, index) => `$${firstTermParamIndex + index}`).join(', ')

  await assertIndexedPlan(
    'AI 账户默认列表 PG 查询',
    `
      SELECT accounts.id
      FROM juhe_business.accounts accounts
      WHERE accounts.system_account_id = $1
        AND accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NULL
      ORDER BY accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
      LIMIT 50
    `,
    [systemAccountId],
    ['idx_accounts_owner_list_order']
  )
  await assertIndexedPlan(
    'AI 账户名称精确 PG 查询',
    `
      SELECT accounts.id
      FROM juhe_business.accounts accounts
      WHERE accounts.system_account_id = $1
        AND accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NULL
        AND lower(accounts.name) = lower($2)
      LIMIT 20
    `,
    [systemAccountId, keyword],
    ['idx_accounts_owner_name_lower_lookup', 'idx_accounts_system_account_name_lookup']
  )
  await assertIndexedPlan(
    'AI 账户名称包含候选 PG 查询',
    `
      WITH candidate_terms AS MATERIALIZED (
        SELECT search.account_id, search.term
        FROM juhe_business.account_name_search_terms search
        WHERE search.system_account_id = $${systemAccountParamIndex}
          AND search.term IN (${termPlaceholdersAfterOwner})
      )
      SELECT candidate_terms.account_id
      FROM candidate_terms
      INNER JOIN juhe_business.account_name_search_documents documents
        ON documents.account_id = candidate_terms.account_id
      WHERE documents.normalized_name LIKE $${containsParamIndex} ESCAPE '\\'
      GROUP BY candidate_terms.account_id
      HAVING COUNT(DISTINCT candidate_terms.term) = $${termCountParamIndex}
    `,
    [systemAccountId, ...terms, `%${escapeAccountNameSearchLike(normalizedKeyword)}%`, terms.length],
    ['idx_account_name_search_terms_owner_term']
  )
  await assertIndexedPlan(
    'AI 账户分组筛选 PG 查询',
    `
      SELECT accounts.id
      FROM juhe_business.accounts accounts
      WHERE accounts.system_account_id = $1
        AND accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NULL
        AND accounts.id IN (
          SELECT group_filter.account_id
          FROM juhe_business.group_accounts group_filter
          WHERE group_filter.system_account_id = $1
            AND group_filter.group_id = $2
            AND group_filter.enabled = 1
        )
      ORDER BY accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
      LIMIT 50
    `,
    [systemAccountId, groupId],
    ['idx_group_accounts_owner_group_enabled', 'idx_group_accounts_group_enabled']
  )
  await assertIndexedPlan(
    'AI 账户标签筛选 PG 查询',
    `
      SELECT accounts.id
      FROM juhe_business.accounts accounts
      WHERE accounts.system_account_id = $1
        AND accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NULL
        AND accounts.id IN (
          SELECT tag_filter.account_id
          FROM juhe_business.account_tag_bindings tag_filter
          WHERE tag_filter.system_account_id = $1
            AND tag_filter.tag_id = $2
        )
      ORDER BY accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
      LIMIT 50
    `,
    [systemAccountId, tagId],
    ['idx_account_tag_bindings_tag_owner']
  )
}

async function seedAccountListPlannerRows(keyword: string): Promise<void> {
  const pool = await getPostgresPool()
  const profileResult = await pool.query(`
    SELECT id, provider_code, protocol_code, protocol_version
    FROM juhe_business.provider_protocol_profiles
    WHERE provider_code = 'gpt'
      AND enabled = 1
    ORDER BY updated_at DESC, id ASC
    LIMIT 1
  `)
  const profile = profileResult.rows[0] as {
    id: string
    provider_code: string
    protocol_code: string
    protocol_version: string
  } | undefined
  assert(profile, 'PG AI 账户列表 planner 种子需要可用 GPT 协议档案')

  const now = new Date().toISOString()
  const ownerIds = Array.from({ length: plannerOwnerCount }, (_, index) => `${marker}_planner_owner_${index}`)
  const usernames = ownerIds.map((_, index) => `${marker}_planner_user_${index}`)
  const displayNames = ownerIds.map((_, index) => `AI 账户列表 planner 租户 ${marker} ${index}`)
  plannerSystemAccountIds.push(...ownerIds)

  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, role, status, password_hash, created_at, updated_at
    )
    SELECT *
    FROM UNNEST($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[])
    ON CONFLICT (id) DO NOTHING
  `, [
    ownerIds,
    usernames,
    displayNames,
    ownerIds.map(() => 'user'),
    ownerIds.map(() => 'active'),
    ownerIds.map(() => `planner-password-${marker}`),
    ownerIds.map(() => now),
    ownerIds.map(() => now)
  ])

  const accountIds: string[] = []
  const accountOwnerIds: string[] = []
  const accountNames: string[] = []
  for (const [ownerIndex, ownerId] of ownerIds.entries()) {
    for (let accountIndex = 0; accountIndex < plannerAccountsPerOwner; accountIndex += 1) {
      accountIds.push(`${marker}_planner_acc_${ownerIndex}_${accountIndex}`)
      accountOwnerIds.push(ownerId)
      accountNames.push(`${keyword} planner ${marker} ${ownerIndex}-${accountIndex}`)
    }
  }
  for (let accountIndex = 0; accountIndex < plannerOwnerListAccountCount; accountIndex += 1) {
    accountIds.push(`${marker}_planner_admin_acc_${accountIndex}`)
    accountOwnerIds.push(access.systemAccountId)
    accountNames.push(`planner-list-${marker}-${accountIndex}`)
  }
  plannerAccountIds.push(...accountIds)

  await pool.query(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credentials_encrypted, credential_mask, created_at, updated_at
    )
    SELECT *
    FROM UNNEST(
      $1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[],
      $7::text[], $8::text[], $9::text[], $10::text[], $11::text[], $12::text[], $13::text[]
    )
    ON CONFLICT (id) DO NOTHING
  `, [
    accountIds,
    accountOwnerIds,
    accountIds.map(() => profile.provider_code),
    accountIds.map(() => profile.id),
    accountIds.map(() => profile.protocol_code),
    accountIds.map(() => profile.protocol_version),
    accountNames,
    accountIds.map(() => 'api_key'),
    accountIds.map(() => 'active'),
    accountIds.map(() => '{}'),
    accountIds.map(() => ''),
    accountIds.map(() => now),
    accountIds.map(() => now)
  ])

  const normalizedNames = accountNames.map((name) => normalizeAccountNameSearchText(name))
  await pool.query(`
    INSERT INTO juhe_business.account_name_search_documents (
      account_id, system_account_id, normalized_name, updated_at
    )
    SELECT *
    FROM UNNEST($1::text[], $2::text[], $3::text[], $4::text[])
    ON CONFLICT (account_id) DO UPDATE
    SET system_account_id = EXCLUDED.system_account_id,
        normalized_name = EXCLUDED.normalized_name,
        updated_at = EXCLUDED.updated_at
  `, [
    accountIds,
    accountOwnerIds,
    normalizedNames,
    accountIds.map(() => now)
  ])

  const termAccountIds: string[] = []
  const termOwnerIds: string[] = []
  const termValues: string[] = []
  for (const [index, name] of accountNames.entries()) {
    for (const term of buildAccountNameSearchTerms(name)) {
      termAccountIds.push(accountIds[index])
      termOwnerIds.push(accountOwnerIds[index])
      termValues.push(term)
    }
  }
  await pool.query(`
    INSERT INTO juhe_business.account_name_search_terms (
      account_id, system_account_id, term, created_at
    )
    SELECT *
    FROM UNNEST($1::text[], $2::text[], $3::text[], $4::text[])
    ON CONFLICT (account_id, term) DO NOTHING
  `, [
    termAccountIds,
    termOwnerIds,
    termValues,
    termAccountIds.map(() => now)
  ])

  await pool.query('ANALYZE juhe_business.accounts')
  await pool.query('ANALYZE juhe_business.account_name_search_terms')
  await pool.query('ANALYZE juhe_business.account_name_search_documents')
}

async function assertIndexedPlan(label: string, sql: string, params: unknown[], expectedIndexes: string[]): Promise<void> {
  const pool = await getPostgresPool()
  const connection = await pool.connect()
  try {
    await connection.query('BEGIN')
    await connection.query('SET LOCAL enable_seqscan = off')
    const planResult = await connection.query(`EXPLAIN (COSTS OFF) ${sql}`, params)
    await connection.query('ROLLBACK')
    const plan = planResult.rows
      .map((row: Record<string, unknown>) => String(row['QUERY PLAN'] ?? ''))
      .filter(Boolean)
      .join('\n')
    assert(!/\bSeq Scan\b/i.test(plan), `${label} 不应退化为 Seq Scan，实际计划：${plan}`)
    assert(
      expectedIndexes.some((indexName) => plan.includes(indexName)),
      `${label} 应命中索引 ${expectedIndexes.join(' / ')}，实际计划：${plan}`
    )
  } catch (error) {
    await connection.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  for (const accountId of createdAccountIds) {
    await deleteAccountAsync(accountId, access).catch(() => false)
  }
  for (const groupId of createdGroupIds) {
    await deleteGroupAsync(groupId, access).catch(() => undefined)
  }
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [plannerAccountIds])
  await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [plannerAccountIds])
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[]) OR position($2 in name) > 0', [plannerAccountIds, `${marker}_planner`])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[]) OR position($2 in id) > 0', [plannerSystemAccountIds, `${marker}_planner_owner`])
  await pool.query(`
    DELETE FROM juhe_business.account_tag_bindings
    WHERE account_id = ANY($1::text[])
       OR tag_id IN (
        SELECT id
        FROM juhe_business.account_tags
        WHERE system_account_id = $2
          AND name = $3
      )
  `, [createdAccountIds, access.systemAccountId, tagName])
  await pool.query('DELETE FROM juhe_business.account_tags WHERE system_account_id = $1 AND name = $2', [access.systemAccountId, tagName])
  await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [createdAccountIds])
  await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [createdAccountIds])
  await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[]) OR group_id = ANY($2::text[])', [createdAccountIds, createdGroupIds])
  await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [createdGroupIds]).catch(() => undefined)
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[]) OR position($2 in name) > 0', [createdAccountIds, marker])
  await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[]) OR position($2 in name) > 0', [createdGroupIds, marker])
}

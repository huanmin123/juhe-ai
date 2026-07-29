import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  addSystemTeamMembersAsync,
  createGroupAsync,
  createResourceAuthorizationAsync,
  createSystemTeamAsync,
  findSystemTeamDetailAsync,
  listSystemTeamsPageAsync,
  removeSystemTeamMemberAsync,
  updateSystemTeamAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '系统团队 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `system_team_pg_smoke_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const keyword = `系统团队PGSmoke_${marker}`
const adminAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const memberIds = [`sys_${marker}_member_1`, `sys_${marker}_member_2`]
const createdTeamIds: string[] = []
const createdGroupIds: string[] = []

try {
  await cleanupSmokeRows()
  await seedSystemAccounts()

  const team = await createSystemTeamAsync({ name: `${keyword}_主团队`, status: 'active' }, adminAccess)
  createdTeamIds.push(team.id)
  const teamWithFirstMember = await addSystemTeamMembersAsync(team.id, { systemAccountIds: [memberIds[0]] }, adminAccess)
  const firstMemberRow = teamWithFirstMember?.members?.find((member) => member.systemAccountId === memberIds[0])
  assert(firstMemberRow?.id, 'PG 系统团队添加成员后应返回成员 ID')

  const listed = await listSystemTeamsPageAsync(adminAccess, { keyword, page: 1, pageSize: 10 })
  assert.ok(listed.items.some((item) => item.id === team.id), 'PG 系统团队列表关键词应返回刚创建的团队')
  assert.equal(listed.items.find((item) => item.id === team.id)?.updatedAt, team.updatedAt, 'PG 系统团队列表必须携带编辑 CAS 版本')
  const scopedListed = await listSystemTeamsPageAsync({ systemAccountId: memberIds[0], role: 'user' }, { keyword, page: 1, pageSize: 10 })
  assert.deepEqual(scopedListed.items.map((item) => item.id), [team.id], 'PG 系统团队成员作用域列表应只返回成员所在团队')
  const detail = await findSystemTeamDetailAsync(team.id, adminAccess)
  assert.deepEqual(Object.keys(detail ?? {}).sort(), ['createdAt', 'description', 'id', 'memberCount', 'members', 'name', 'status'], 'PG 系统团队详情只应返回弹窗字段')
  assert.deepEqual(Object.keys(detail?.members[0] ?? {}).sort(), ['id', 'joinedAt', 'systemAccountId', 'systemAccountName'], 'PG 系统团队成员 DTO 只应返回四个字段')

  const group = await createGroupAsync({
    name: `系统团队 PG smoke 授权分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, adminAccess)
  createdGroupIds.push(group.id)

  await createResourceAuthorizationAsync({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'team',
    granteeId: team.id
  }, adminAccess)
  await assertRuntimeAuthorization(group.id, memberIds[0], 'active', team.id)

  const teamWithSecondMember = await addSystemTeamMembersAsync(team.id, { systemAccountIds: [memberIds[1]] }, adminAccess)
  assert.ok(teamWithSecondMember?.members?.some((member) => member.systemAccountId === memberIds[1]), 'PG 系统团队新增成员应返回团队摘要')
  await assertRuntimeAuthorization(group.id, memberIds[1], 'active', team.id)

  const afterRemove = await removeSystemTeamMemberAsync(team.id, firstMemberRow.id, adminAccess)
  assert.ok(afterRemove?.members?.every((member) => member.systemAccountId !== memberIds[0]), 'PG 系统团队移除成员后摘要不应包含该成员')
  await assertRuntimeAuthorization(group.id, memberIds[0], 'revoked', null)

  const disabled = await updateSystemTeamAsync(team.id, { status: 'disabled', expectedUpdatedAt: team.updatedAt }, adminAccess)
  assert.equal(disabled.status, 'updated', 'PG 系统团队应可停用')
  assert.equal(disabled.status === 'updated' ? disabled.result.rowPatch.status : undefined, 'disabled', 'PG 系统团队停用应返回状态行补丁')
  await assertRuntimeAuthorization(group.id, memberIds[1], 'revoked', null)

  assert.equal(disabled.status, 'updated')
  const reactivated = await updateSystemTeamAsync(team.id, { status: 'active', expectedUpdatedAt: disabled.result.updatedAt }, adminAccess)
  assert.equal(reactivated.status, 'updated', 'PG 系统团队应可重新启用')
  assert.equal(reactivated.status === 'updated' ? reactivated.result.rowPatch.status : undefined, 'active', 'PG 系统团队重新启用应返回状态行补丁')
  await assertRuntimeAuthorization(group.id, memberIds[1], 'active', team.id)

  assert.equal(reactivated.status, 'updated')
  const noOp = await updateSystemTeamAsync(team.id, { status: 'active', expectedUpdatedAt: reactivated.result.updatedAt }, adminAccess)
  assert.equal(noOp.status, 'noop', 'PG 同值团队 PATCH 必须成为 no-op')
  assert.equal(noOp.status === 'noop' ? noOp.result.updatedAt : undefined, reactivated.result.updatedAt, 'PG no-op 不得推进版本')
  const stale = await updateSystemTeamAsync(team.id, { description: '过期版本不得覆盖', expectedUpdatedAt: disabled.result.updatedAt }, adminAccess)
  assert.equal(stale.status, 'conflict', 'PG 过期版本必须返回 CAS 冲突')

  await assertSystemTeamIndexedPlans(keyword, team.id)

  console.log(JSON.stringify({
    message: '系统团队 PG smoke 通过',
    teamId: team.id,
    groupId: group.id,
    changedFields: reactivated.status === 'updated' ? reactivated.result.changedFields : [],
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function seedSystemAccounts(): Promise<void> {
  const pool = await getPostgresPool()
  const now = new Date().toISOString()
  for (const [index, id] of memberIds.entries()) {
    await pool.query(`
      INSERT INTO juhe_business.system_accounts (
        id, username, display_name, role, status, password_hash,
        must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES ($1, $2, $3, 'user', 'active', 'pg-smoke-password-hash', 0, 0, $4, $4)
      ON CONFLICT(id) DO UPDATE SET
        status = 'active',
        updated_at = excluded.updated_at
    `, [id, `${marker}_${index}`, `系统团队 PG smoke 成员 ${index}`, now])
  }
}

async function assertRuntimeAuthorization(resourceId: string, granteeSystemAccountId: string, status: 'active' | 'revoked', expectedTeamId: string | null): Promise<void> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT status, effective_source_type, effective_source_team_id
    FROM juhe_business.resource_authorizations
    WHERE resource_type = 'group'
      AND resource_id = $1
      AND grantee_system_account_id = $2
    LIMIT 1
  `, [resourceId, granteeSystemAccountId])
  const row = result.rows[0] as { status?: string; effective_source_type?: string | null; effective_source_team_id?: string | null } | undefined
  assert.equal(row?.status, status, `PG 系统团队授权运行态应为 ${status}`)
  assert.equal(row?.effective_source_team_id ?? null, expectedTeamId, 'PG 系统团队授权运行态来源团队应正确')
  if (status === 'active') {
    assert.equal(row?.effective_source_type, 'team', 'PG 系统团队授权运行态应标记为团队来源')
  }
}

async function assertSystemTeamIndexedPlans(keywordValue: string, teamId: string): Promise<void> {
  await assertIndexedPlan(
    '系统团队列表排序 PG 查询',
    `
      SELECT id, name, status
      FROM juhe_business.system_teams
      ORDER BY status ASC, updated_at DESC, name ASC, id ASC
      LIMIT 10
    `,
    [],
    ['idx_system_teams_list_order']
  )
  await assertIndexedPlan(
    '系统团队名称前缀 PG 查询',
    `
      SELECT id
      FROM juhe_business.system_teams
      WHERE name COLLATE "C" >= $1
        AND name COLLATE "C" < $2
        AND starts_with(name, $1)
      ORDER BY name COLLATE "C" ASC, id ASC
      LIMIT 10
    `,
    [keywordValue, systemTeamTextPrefixUpperBound(keywordValue)],
    ['idx_system_teams_name_c_lookup']
  )
  await assertIndexedPlan(
    '系统团队成员窗口 PG 查询',
    `
      SELECT *
      FROM (
        SELECT system_team_members.id, system_team_members.team_id, system_team_members.system_account_id,
          ROW_NUMBER() OVER (
            PARTITION BY system_team_members.team_id
            ORDER BY system_team_members.status ASC, system_team_members.joined_at ASC, system_team_members.id ASC
          ) AS team_member_rank
        FROM juhe_business.system_team_members system_team_members
        WHERE system_team_members.team_id IN ($1)
          AND system_team_members.status = 'active'
      ) ranked_team_members
      WHERE team_member_rank <= 1000
    `,
    [teamId],
    ['idx_system_team_members_team_status_joined']
  )
  await assertIndexedPlan(
    '系统团队成员作用域 PG 查询',
    `
      SELECT team_id
      FROM juhe_business.system_team_members
      WHERE system_account_id = $1
        AND status = 'active'
      ORDER BY system_account_id ASC, status ASC
      LIMIT 10
    `,
    [memberIds[1]],
    ['idx_system_team_members_account', 'idx_system_team_members_active_unique']
  )
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

function systemTeamTextPrefixUpperBound(value: string): string {
  for (let index = value.length - 1; index >= 0; index -= 1) {
    const code = value.charCodeAt(index)
    if (code < 0xffff) {
      return `${value.slice(0, index)}${String.fromCharCode(code + 1)}`
    }
  }
  return `${value}\uffff`
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.resource_authorization_sources WHERE source_team_id = ANY($1::text[])', [createdTeamIds])
  await pool.query('DELETE FROM juhe_business.resource_authorization_grants WHERE grantee_team_id = ANY($1::text[])', [createdTeamIds])
  await pool.query('DELETE FROM juhe_business.resource_authorizations WHERE resource_id = ANY($1::text[]) OR grantee_system_account_id = ANY($2::text[])', [createdGroupIds, memberIds])
  await pool.query('DELETE FROM juhe_business.system_team_members WHERE team_id = ANY($1::text[]) OR system_account_id = ANY($2::text[])', [createdTeamIds, memberIds])
  await pool.query('DELETE FROM juhe_business.system_teams WHERE id = ANY($1::text[])', [createdTeamIds])
  await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])', [memberIds])
}

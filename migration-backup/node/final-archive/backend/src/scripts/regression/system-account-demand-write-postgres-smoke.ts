import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  listSystemAccountsPageAsync,
  patchSystemAccountManagementAsync,
  SystemAccountManagementPatchConflictError
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '系统账户 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `sys_account_demand_pg_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const systemAccountId = `sys_${marker}`

try {
  const pool = await getPostgresPool()
  const initialUpdatedAt = '2026-07-29T01:02:03.123456Z'
  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, description, role, status, password_hash,
      must_change_password, image_generation_enabled, request_limits_json,
      created_at, updated_at
    ) VALUES ($1, $2, $3, $4, 'user', 'disabled', 'pg-smoke-password-hash', 1, 0, NULL, $5, $5)
  `, [systemAccountId, marker, `系统账户 PG 按需写 ${marker}`, '初始说明', initialUpdatedAt])

  const listed = (await listSystemAccountsPageAsync({ keyword: marker, page: 1, pageSize: 20 })).items.find((item) => item.id === systemAccountId)
  assert(listed, 'PG 列表必须返回微秒版本夹具')
  assert.equal(listed.editVersion, initialUpdatedAt, 'PG 列表不得丢失 updated_at 微秒精度')

  const noOp = await patchSystemAccountManagementAsync(systemAccountId, { status: 'disabled' }, '2026-07-29T01:02:03.1234560Z')
  assert.equal(noOp?.kind, 'no_op', 'PG disabled 同值 PATCH 必须是 no-op')
  assert.equal(noOp?.result.updatedAt, initialUpdatedAt, 'PG no-op 不得推进版本')

  const updated = await patchSystemAccountManagementAsync(systemAccountId, { description: 'PG 只改说明' }, listed.editVersion)
  assert.equal(updated?.kind, 'updated', 'PG 说明 PATCH 应实际更新')
  assert.deepEqual(updated?.changes.map((change) => change.field), ['description'], 'PG 单字段 PATCH 不得派生其他字段')
  assert.deepEqual(Object.keys(updated?.result ?? {}).sort(), ['description', 'id', 'updatedAt'], 'PG PATCH 必须返回最小回执')

  const rowResult = await pool.query(`
    SELECT description, status, updated_at
    FROM juhe_business.system_accounts
    WHERE id = $1
  `, [systemAccountId])
  const row = rowResult.rows[0] as { description: string; status: string; updated_at: string }
  assert.equal(row.description, 'PG 只改说明')
  assert.equal(row.status, 'disabled', 'PG 说明 PATCH 不得覆盖状态')
  assert.equal(revisionTokensEqual(row.updated_at, updated?.result.updatedAt ?? ''), true, 'PG 数据库微秒版本应与毫秒回执等价')

  await assert.rejects(
    patchSystemAccountManagementAsync(systemAccountId, { description: '过期覆盖' }, initialUpdatedAt),
    SystemAccountManagementPatchConflictError,
    'PG 过期 PATCH 必须由 CAS 拒绝'
  )

  console.log(JSON.stringify({
    message: '系统账户 PG 按需写 smoke 通过',
    systemAccountId,
    changedFields: updated?.changes.map((change) => change.field) ?? []
  }))
} finally {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.system_sessions WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = $1', [systemAccountId])
  await closeRedisClients()
  await closePostgresPool()
}

function revisionTokensEqual(left: string, right: string): boolean {
  return normalizedRevisionToken(left) === normalizedRevisionToken(right)
}

function normalizedRevisionToken(value: string): string {
  const match = /^(.*?)(?:\.(\d+))?Z$/i.exec(value)
  if (!match) return value
  const fraction = (match[2] ?? '').replace(/0+$/, '')
  return `${match[1]}${fraction ? `.${fraction}` : ''}Z`
}

import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import {
  createExternalIntegrationSourceAuthorizationAsync,
  deleteExternalIntegrationSourceAsync,
  externalIntegrationAccessInfoReadScope,
  externalIntegrationAccountAddWriteScope,
  externalIntegrationSourceAuthDemoScope,
  findExternalIntegrationSourceAsync,
  findExternalIntegrationSourceTokenSecretAsync,
  listExternalIntegrationSourcesAsync,
  updateExternalIntegrationSourceAsync,
  updateExternalIntegrationSourceTokenAsync,
  validateExternalIntegrationSourceTokenAsync
} from '../../storage/external-integration-source.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '外部来源系统 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `external_source_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2, 8)}`
const sourceName = `外部来源 PG smoke ${marker}`
let sourceId: string | undefined
let tokenId: string | undefined

try {
  await cleanupSmokeRows()
  const created = await createExternalIntegrationSourceAuthorizationAsync({
    name: sourceName,
    status: 'active',
    scopes: [externalIntegrationSourceAuthDemoScope, externalIntegrationAccessInfoReadScope],
    rateLimits: [{ windowSeconds: 60, maxRequests: 100 }],
    notes: 'pg smoke'
  })
  sourceId = created.source.id
  tokenId = created.token.id

  const listed = await listExternalIntegrationSourcesAsync({
    keyword: sourceName,
    status: 'active',
    page: 1,
    pageSize: 10
  })
  assert.equal(listed.items.length, 1, 'PG 外部来源系统列表应按名称前缀命中临时来源')
  assert.equal(listed.items[0]?.tokenCount, 1, 'PG 外部来源系统列表应批量回填 tokenCount')
  assert.equal(listed.items[0]?.activeTokenCount, 1, 'PG 外部来源系统列表应批量回填 activeTokenCount')

  const secret = await findExternalIntegrationSourceTokenSecretAsync(sourceId, tokenId)
  assert.equal(secret?.token, created.token.token, 'PG 外部来源系统 token secret 应可解密完整 token')

  const authOk = await validateExternalIntegrationSourceTokenAsync({
    token: created.token.token,
    requiredScope: externalIntegrationSourceAuthDemoScope
  })
  assert.equal(authOk.ok, true, 'PG 外部来源系统 token 应通过授权 scope 校验')
  assert.equal(authOk.ok ? authOk.context.sourceRefId : undefined, sourceId, 'PG 外部来源系统鉴权上下文应返回 sourceRefId')

  const authForbidden = await validateExternalIntegrationSourceTokenAsync({
    token: created.token.token,
    requiredScope: externalIntegrationAccountAddWriteScope
  })
  assert.equal(authForbidden.ok, false, 'PG 外部来源系统 token scope 不足应拒绝')
  assert.equal(authForbidden.ok ? undefined : authForbidden.code, 'external_source_scope_forbidden', 'PG 外部来源系统 token scope 不足应返回固定错误码')

  const token = await updateExternalIntegrationSourceTokenAsync(sourceId, tokenId, {
    name: `外部来源 PG smoke token ${marker}`,
    scopes: [externalIntegrationSourceAuthDemoScope]
  })
  assert.equal(token?.name, `外部来源 PG smoke token ${marker}`, 'PG 外部来源系统 token 应可更新名称')
  assert.deepEqual(token?.scopes, [externalIntegrationSourceAuthDemoScope], 'PG 外部来源系统 token 应可更新 scopes')

  const disabled = await updateExternalIntegrationSourceAsync(sourceId, { status: 'disabled' })
  assert.equal(disabled?.status, 'disabled', 'PG 外部来源系统应可禁用')
  assert.equal(disabled?.tokens[0]?.status, 'disabled', 'PG 外部来源系统禁用后应同步禁用非 revoked token')

  const authDisabled = await validateExternalIntegrationSourceTokenAsync({
    token: created.token.token,
    requiredScope: externalIntegrationSourceAuthDemoScope
  })
  assert.equal(authDisabled.ok, false, 'PG 外部来源系统禁用后 token 应拒绝')
  assert.equal(authDisabled.ok ? undefined : authDisabled.code, 'external_source_disabled', 'PG 外部来源系统禁用后应返回固定错误码')

  assert.equal(await deleteExternalIntegrationSourceAsync(sourceId), true, 'PG 外部来源系统应可删除')
  const afterDelete = await findExternalIntegrationSourceAsync(sourceId)
  assert.equal(afterDelete, undefined, 'PG 外部来源系统删除后应不可读取')
  sourceId = undefined
  tokenId = undefined

  console.log(JSON.stringify({
    message: '外部来源系统 PG smoke 通过',
    listed: listed.items.length,
    sourceDeleted: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  if (sourceId) {
    await deleteExternalIntegrationSourceAsync(sourceId).catch(() => false)
  }
  await pool.query('DELETE FROM juhe_business.external_integration_source_tokens WHERE source_ref_id IN (SELECT id FROM juhe_business.external_integration_sources WHERE name LIKE $1)', [`%${marker}%`])
  await pool.query('DELETE FROM juhe_business.external_integration_sources WHERE name LIKE $1', [`%${marker}%`])
}

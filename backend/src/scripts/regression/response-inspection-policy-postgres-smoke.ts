import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import {
  createResponseInspectionPolicyAsync,
  deleteResponseInspectionPolicyAsync,
  listActiveResponseInspectionPoliciesForGatewayAsync,
  listResponseInspectionPoliciesAsync,
  updateResponseInspectionPolicyAsync
} from '../../storage/response-inspection-policy.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { closeRedisClients } from '../../shared/redis-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '响应检查策略 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `response_policy_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const createdPolicyIds: string[] = []

try {
  const created = await createResponseInspectionPolicyAsync({
    name: `响应检查策略 PG smoke ${marker}`,
    enabled: true,
    priority: 777,
    scopeType: 'protocol',
    protocolCode: OPENAI_PROTOCOL_CODE,
    match: {
      errorCodes: [`${marker}_protocol_error`]
    },
    action: 'observe',
    notes: 'response inspection policy postgres smoke'
  })
  createdPolicyIds.push(created.id)

  const listed = await listResponseInspectionPoliciesAsync()
  assert.ok(listed.policies.some((policy) => policy.id === created.id), 'PG list 应返回刚创建的管理端响应检查策略')

  const activeProtocolPolicies = await listActiveResponseInspectionPoliciesForGatewayAsync({
    protocolCode: OPENAI_PROTOCOL_CODE
  })
  assert.ok(activeProtocolPolicies.some((policy) => policy.id === created.id), 'PG 网关 active 查询应返回启用的 protocol 策略')

  const providerScoped = await updateResponseInspectionPolicyAsync(created.id, {
    name: `响应检查策略 PG smoke provider ${marker}`,
    enabled: true,
    priority: 778,
    scopeType: 'provider',
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: GPT_VENDOR_CODE,
    match: {
      errorMessageIncludes: [`${marker} provider updated`]
    },
    action: 'retry_no_avoidance',
    notes: null
  })
  assert.ok(providerScoped, 'PG update 应返回更新后的响应检查策略')
  assert.equal(providerScoped.providerCode, GPT_VENDOR_CODE, 'PG provider scoped 更新应保留供应商编码')
  assert.equal(providerScoped.notes, undefined, 'PG update 应支持清空备注')

  const activeProviderPolicies = await listActiveResponseInspectionPoliciesForGatewayAsync({
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: GPT_VENDOR_CODE
  })
  assert.ok(activeProviderPolicies.some((policy) => policy.id === created.id && policy.providerCode === GPT_VENDOR_CODE), 'PG 网关 active 查询应返回启用的 provider 策略')

  const activeProtocolAfterProviderUpdate = await listActiveResponseInspectionPoliciesForGatewayAsync({
    protocolCode: OPENAI_PROTOCOL_CODE
  })
  assert.equal(activeProtocolAfterProviderUpdate.some((policy) => policy.id === created.id), false, 'PG provider scoped 策略不应泄漏到纯 protocol active 查询')

  const disabled = await updateResponseInspectionPolicyAsync(created.id, {
    name: providerScoped.name,
    enabled: false,
    priority: providerScoped.priority,
    scopeType: 'provider',
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: GPT_VENDOR_CODE,
    match: providerScoped.match,
    action: providerScoped.action
  })
  assert.ok(disabled, 'PG disable update 应返回更新后的响应检查策略')
  assert.equal(disabled.enabled, false, 'PG disable update 应写回 enabled=false')

  const activeAfterDisable = await listActiveResponseInspectionPoliciesForGatewayAsync({
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: GPT_VENDOR_CODE
  })
  assert.equal(activeAfterDisable.some((policy) => policy.id === created.id), false, 'PG disabled provider 策略不应进入网关 active 查询')

  await assertActivePolicyExplainUsesIndex()

  const deleted = await deleteResponseInspectionPolicyAsync(created.id)
  assert.equal(deleted, true, 'PG delete 应返回已删除')
  const listedAfterDelete = await listResponseInspectionPoliciesAsync()
  assert.equal(listedAfterDelete.policies.some((policy) => policy.id === created.id), false, 'PG delete 后 list 不应再返回策略')

  console.log(JSON.stringify({
    message: '响应检查策略 PG smoke 通过',
    createdPolicyId: created.id,
    activeProtocolChecked: true,
    activeProviderChecked: true,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function assertActivePolicyExplainUsesIndex(): Promise<void> {
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('SET LOCAL enable_seqscan = off')
    const result = await client.query(
      `EXPLAIN (COSTS OFF)
       SELECT *
       FROM juhe_business.response_inspection_policies
       WHERE enabled = 1
         AND protocol_code = $1
         AND (
           (scope_type = 'protocol' AND provider_code IS NULL)
           OR (scope_type = 'provider' AND provider_code = $2)
         )
       ORDER BY CASE scope_type WHEN 'provider' THEN 0 ELSE 1 END ASC, priority ASC, updated_at DESC, id ASC
       LIMIT 100`,
      [OPENAI_PROTOCOL_CODE, GPT_VENDOR_CODE]
    )
    const plan = result.rows.map((row) => String(row['QUERY PLAN'] ?? '')).join('\n')
    assert.match(plan, /idx_response_inspection_policies_(scope|protocol|enabled)_priority/, 'PG active 响应检查策略查询应使用响应检查策略索引')
    assert.doesNotMatch(plan, /\bSeq Scan\b/, 'PG active 响应检查策略查询不应出现 Seq Scan')
  } finally {
    await client.query('ROLLBACK').catch(() => undefined)
    client.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  if (createdPolicyIds.length === 0) return
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.response_inspection_policies WHERE id = ANY($1::text[])', [createdPolicyIds])
}

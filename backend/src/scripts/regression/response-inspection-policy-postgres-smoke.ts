import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import {
  createResponseInspectionPolicyAsync,
  deleteResponseInspectionPolicyAsync,
  getResponseInspectionPolicyDetailAsync,
  listActiveResponseInspectionPoliciesForGatewayAsync,
  listResponseInspectionPolicyProviderOptionsAsync,
  listResponseInspectionPoliciesAsync,
  patchResponseInspectionPolicyAsync
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
  const createdOverview = listed.policies.find((policy) => policy.id === created.id)
  assert.ok(createdOverview, 'PG list 应返回刚创建的管理端响应检查策略')
  assertOverviewShape(createdOverview)
  for (const defaultRule of listed.defaultRules) assertOverviewShape(defaultRule)

  const createdDetail = await getResponseInspectionPolicyDetailAsync(created.id)
  assert.ok(createdDetail, 'PG detail 应返回刚创建的管理端响应检查策略')
  assert.deepEqual(createdDetail.match, { errorCodes: [`${marker}_protocol_error`] }, 'PG detail 必须保留完整 matcher')
  assert.equal(createdDetail.notes, 'response inspection policy postgres smoke', 'PG detail 必须保留备注')
  assert.equal(typeof createdDetail.updatedAt, 'string', 'PG detail 必须携带 CAS 版本')

  const defaultDetail = await getResponseInspectionPolicyDetailAsync(listed.defaultRules[0]?.id ?? '')
  assert.ok(defaultDetail, 'PG detail 必须支持系统默认规则')
  assert(defaultDetail.match && Object.keys(defaultDetail.match).length > 0, 'PG 默认规则 detail 必须包含 matcher')

  const providerOptions = await listResponseInspectionPolicyProviderOptionsAsync({ protocolCode: OPENAI_PROTOCOL_CODE, scopeType: 'provider' })
  assert(providerOptions.length > 0, 'PG provider options 必须返回启用的受支持供应商')
  for (const option of providerOptions) {
    assert.deepEqual(Object.keys(option).sort(), ['code', 'name'], 'PG provider option 只能返回 code/name')
  }
  assert.equal(new Set(providerOptions.map((option) => `${option.code}\u0000${option.protocolCode}`)).size, providerOptions.length, 'PG provider options 必须去重')

  const activeProtocolPolicies = await listActiveResponseInspectionPoliciesForGatewayAsync({
    protocolCode: OPENAI_PROTOCOL_CODE
  })
  assert.ok(activeProtocolPolicies.some((policy) => policy.id === created.id), 'PG 网关 active 查询应返回启用的 protocol 策略')

  const providerOutcome = await patchResponseInspectionPolicyAsync(created.id, {
    name: `响应检查策略 PG smoke provider ${marker}`,
    priority: 778,
    scopeType: 'provider',
    providerCode: GPT_VENDOR_CODE,
    match: {
      errorMessageIncludes: [`${marker} provider updated`]
    },
    action: 'retry_no_avoidance',
    notes: null
  }, createdDetail.updatedAt as string)
  assert.equal(providerOutcome.status, 'updated', 'PG PATCH 应返回更新结果')
  assert(providerOutcome.status === 'updated')
  const providerScoped = providerOutcome.policy
  assert.equal(providerScoped.providerCode, GPT_VENDOR_CODE, 'PG provider scoped 更新应保留供应商编码')
  assert.equal(providerScoped.notes, undefined, 'PG update 应支持清空备注')

  const noop = await patchResponseInspectionPolicyAsync(created.id, { priority: 778 }, providerScoped.updatedAt as string)
  assert.equal(noop.status, 'noop', 'PG 同值 PATCH 必须零写')
  assert(noop.status === 'noop')
  assert.equal(noop.policy.updatedAt, providerScoped.updatedAt, 'PG no-op 不得推进版本')
  assert.equal((await patchResponseInspectionPolicyAsync(created.id, { enabled: false }, createdDetail.updatedAt as string)).status, 'conflict', 'PG 旧版本 PATCH 必须冲突')

  const activeProviderPolicies = await listActiveResponseInspectionPoliciesForGatewayAsync({
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: GPT_VENDOR_CODE
  })
  assert.ok(activeProviderPolicies.some((policy) => policy.id === created.id && policy.providerCode === GPT_VENDOR_CODE), 'PG 网关 active 查询应返回启用的 provider 策略')

  const activeProtocolAfterProviderUpdate = await listActiveResponseInspectionPoliciesForGatewayAsync({
    protocolCode: OPENAI_PROTOCOL_CODE
  })
  assert.equal(activeProtocolAfterProviderUpdate.some((policy) => policy.id === created.id), false, 'PG provider scoped 策略不应泄漏到纯 protocol active 查询')

  const disabledOutcome = await patchResponseInspectionPolicyAsync(created.id, { enabled: false }, providerScoped.updatedAt as string)
  assert.equal(disabledOutcome.status, 'updated', 'PG disable PATCH 应返回更新结果')
  assert(disabledOutcome.status === 'updated')
  assert.equal(disabledOutcome.policy.enabled, false, 'PG disable update 应写回 enabled=false')

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
  assert.equal(await getResponseInspectionPolicyDetailAsync(created.id), undefined, 'PG delete 后 detail 应返回 undefined，供 HTTP 映射 404')

  console.log(JSON.stringify({
    message: '响应检查策略 PG smoke 通过',
    createdPolicyId: created.id,
    activeProtocolChecked: true,
    activeProviderChecked: true,
    overviewDetailOptionsChecked: true,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

function assertOverviewShape(value: object): void {
  const policy = value as Record<string, unknown>
  const allowed = new Set([
    'id', 'defaultRule', 'editable', 'name', 'enabled', 'priority', 'scopeType', 'protocolCode',
    'providerCode', 'providerName', 'action', 'updatedAt'
  ])
  for (const key of Object.keys(policy)) assert(allowed.has(key), `PG overview 出现非白名单字段：${key}`)
  for (const key of ['match', 'notes', 'createdAt']) assert.equal(Object.hasOwn(policy, key), false, `PG overview 禁止返回 ${key}`)
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

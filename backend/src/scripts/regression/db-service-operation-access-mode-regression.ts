import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  dbServiceOperationAccessMode,
  dbServiceOperationAccessModeByType
} from '../../modules/db-service/db-service-operation-access-mode.js'
import { dbServiceOperationPriority } from '../../modules/db-service/db-service-request-priority.js'
import type { DbServiceOperation } from '../../modules/db-service/db-service-types.js'

const regressionDir = dirname(fileURLToPath(import.meta.url))
const backendSrcDir = resolve(regressionDir, '../..')
const dbServiceTypesSource = readFileSync(resolve(backendSrcDir, 'modules/db-service/db-service-types.ts'), 'utf8')
const dbServiceAccessModeSource = readFileSync(resolve(backendSrcDir, 'modules/db-service/db-service-operation-access-mode.ts'), 'utf8')
const dbServiceSource = readFileSync(resolve(backendSrcDir, 'db-service.ts'), 'utf8')
const dbServiceHandlersSource = readFileSync(resolve(backendSrcDir, 'modules/db-service/db-service-handlers.ts'), 'utf8')

const operationTypes = extractDbServiceOperationTypes(dbServiceTypesSource)
const classifiedTypes = Object.keys(dbServiceOperationAccessModeByType).sort()

assert.deepEqual(
  classifiedTypes,
  operationTypes,
  '每个 DbServiceOperation type 都必须显式维护 access mode 分类'
)

const huygensReadOperationTypes = [
  'list_public_global_settings',
  'validate_gateway_api_key',
  'read_gateway_settings',
  'resolve_group_usage_access',
  'list_openai_accounts_for_group',
  'list_openai_accounts_for_group_result',
  'find_openai_account_for_group',
  'list_recoverable_unavailable_openai_accounts_for_group',
  'read_gateway_runtime',
  'list_openai_compatible_files',
  'get_openai_compatible_file',
  'list_openai_compatible_vector_stores',
  'get_openai_compatible_vector_store',
  'list_openai_compatible_vector_store_files',
  'get_openai_compatible_vector_store_file',
  'search_openai_compatible_vector_store',
  'list_openai_compatible_vector_store_file_chunks',
  'list_provider_model_catalog',
  'check_api_key_quota',
  'read_api_key_quota_costs',
  'check_authorization_quota',
  'check_authorization_quota_batch',
  'find_openai_oauth_account_for_refresh',
  'find_account_for_test',
  'is_account_test_task_cancel_requested',
  'read_account_test_task_cancel_message',
  'list_active_client_ip_policies',
  'list_active_response_inspection_policies',
  'list_runtime_logs',
  'get_runtime_log_detail',
  'get_runtime_log_facets',
  'read_codex_context_response_chain',
  'read_codex_context_compact_state'
] as const

for (const type of huygensReadOperationTypes) {
  const operation = { type } as DbServiceOperation
  assert.equal(dbServiceOperationAccessMode(operation), 'read', `${type} 必须归类为 read`)
  assert.notEqual(dbServiceOperationPriority(operation), 'low', `${type} 不能被维护任务低优先级覆盖`)
}

for (const type of [
  'create_openai_compatible_file',
  'delete_openai_compatible_vector_store',
  'update_openai_oauth_credentials',
  'persist_openai_codex_usage_headers',
  'apply_account_error_handling',
  'record_account_api_key_failure',
  'record_account_api_key_success',
  'record_account_stream_failure',
  'clear_account_stream_failure_state',
  'mark_account_temporary_unavailable',
  'update_proxy_test_state',
  'save_codex_context_response_state',
  'save_codex_context_compact_state',
  'mark_account_test_task_running',
  'record_client_ip_policy_hits'
] as const) {
  assert.equal(dbServiceOperationAccessMode({ type } as DbServiceOperation), 'write', `${type} 必须归类为 write`)
}

for (const type of [
  'mark_all_group_account_stats_dirty',
  'delete_group_account_stats_dirty_rows',
  'update_group_account_stats_all_cursor',
  'sync_api_key_availability_schedule_statuses',
  'sync_account_availability_schedule_statuses',
  'expire_due_resource_authorizations',
  'cleanup_expired_deleted_accounts',
  'cleanup_expired_system_sessions',
  'list_accounts_due_for_health_check',
  'find_account_for_health_check',
  'list_accounts_due_for_cooldown_retest',
  'find_account_for_cooldown_retest',
  'cleanup_expired_codex_context_states',
  'account_test_task_maintenance'
] as const) {
  const operation = { type } as DbServiceOperation
  assert.equal(dbServiceOperationAccessMode(operation), 'maintenance', `${type} 必须归类为 maintenance`)
  assert.equal(dbServiceOperationPriority(operation), 'low', `${type} 必须保持低优先级`)
}

for (const type of ['status', 'clear_gateway_runtime_cache'] as const) {
  assert.equal(dbServiceOperationAccessMode({ type } as DbServiceOperation), 'runtime', `${type} 必须归类为 runtime`)
}

assert.ok(!dbServiceSource.includes('postgresConcurrentDbServiceOperationTypes'), 'PG 模式不能继续使用硬编码并发白名单')
assert.match(
  dbServiceAccessModeSource,
  /function shouldQueueDbServiceOperationForDriver[\s\S]+databaseDriver === 'postgres'[\s\S]+return false[\s\S]+isDbServiceWriteQueueOperation\(operation\)/,
  'DB service 入队决策 helper 必须确保 PG 不走全局队列，SQLite 只让写入/维护进入队列'
)
assert.match(
  dbServiceSource,
  /function shouldQueueDbServiceRequest[\s\S]+shouldQueueDbServiceOperationForDriver\(message\.operation, runtimeConfig\.databaseDriver\)/,
  'DB service 真实调度必须调用可模拟测试的入队决策 helper'
)
assert.match(
  dbServiceSource,
  /if \(shouldQueueDbServiceRequest\(message\)\)[\s\S]+enqueueDbServiceRequest\(message\)[\s\S]+dispatchDbServiceRequestImmediately\(message\)/,
  'DB service 父进程请求必须先区分是否入队，不入队的读/runtime 请求直接派发'
)
assertDbServiceHandlerDispatchBoundary(operationTypes)

console.log('DB service operation access mode 回归通过：操作分类完整，读写维护调度骨架保持分离')

function extractDbServiceOperationTypes(source: string): string[] {
  const start = source.indexOf('export type DbServiceOperation =')
  const end = source.indexOf('export type DbServiceOperationResult')
  assert.ok(start >= 0 && end > start, '无法定位 DbServiceOperation 联合类型源码')
  const operationSource = source.slice(start, end)
  return Array.from(new Set([...operationSource.matchAll(/type: '([^']+)'/g)].map((match) => match[1])))
    .sort()
}

function assertDbServiceHandlerDispatchBoundary(expectedOperationTypes: string[]): void {
  const dispatchBody = functionBody(dbServiceHandlersSource, 'handleDbServiceOperationDispatch')
  const dispatchCases = extractSwitchCaseBodies(dispatchBody)
  assert.deepEqual(
    [...dispatchCases.keys()].sort(),
    expectedOperationTypes,
    '每个 DbServiceOperation type 都必须在 handleDbServiceOperationDispatch 中显式维护 PG/SQLite 分发 case'
  )

  const localRuntimeOnlySyncCases = new Set(['clear_gateway_runtime_cache'])
  for (const [type, body] of dispatchCases) {
    const syncFallbackIndex = body.indexOf('handleDbServiceOperationSync(operation)')
    if (syncFallbackIndex < 0) continue
    if (localRuntimeOnlySyncCases.has(type)) continue
    const postgresBranchIndex = body.indexOf("runtimeConfig.databaseDriver === 'postgres'")
    assert.ok(
      postgresBranchIndex >= 0 && postgresBranchIndex < syncFallbackIndex,
      `${type} 在调用 handleDbServiceOperationSync 前必须先接入 PostgreSQL async 分支，禁止 PG 回落同步 SQLite`
    )
  }

  assert.match(
    dispatchBody,
    /default:[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*PostgreSQL DB service operation 未接入 async driver[\s\S]*handleDbServiceOperationSync\(operation\)/,
    'DB service 未接入的新 operation 在 PG 下必须 fail-fast，不能默认落到同步 dispatcher'
  )
}

function extractSwitchCaseBodies(source: string): Map<string, string> {
  const matches = [...source.matchAll(/case '([^']+)':/g)]
  const output = new Map<string, string>()
  for (let index = 0; index < matches.length; index += 1) {
    const match = matches[index]
    const next = matches[index + 1]
    const start = match.index ?? 0
    const end = next?.index ?? source.indexOf('default:', start)
    assert.ok(end > start, `无法解析 DB service case：${match[1]}`)
    output.set(match[1], source.slice(start, end))
  }
  return output
}

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert.ok(start >= 0, `缺少函数 ${functionName}`)
  const openBrace = sourceText.indexOf('{', start)
  assert.ok(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) return sourceText.slice(openBrace, index + 1)
    }
  }
  throw new Error(`函数 ${functionName} 函数体解析失败`)
}

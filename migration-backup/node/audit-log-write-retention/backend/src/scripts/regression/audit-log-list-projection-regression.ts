import assert from 'node:assert/strict'

import { auditLogListSelectColumns } from '../../storage/audit-log-list-query.js'
import { auditLogListItemFromRow } from '../../storage/audit-log-mappers.js'

const expectedColumns = [
  'id', 'trace_id', 'session_id', 'session_client_type',
  'traffic_source', 'system_account_id', 'api_key_id',
  'group_id', 'account_id', 'method', 'path', 'model', 'upstream_model',
  'model_mapping_applied', 'stream', 'audit_outcome', 'success',
  'final_status_code', 'duration_ms', 'http_duration_ms', 'created_at'
]

assert.deepEqual(
  auditLogListSelectColumns('al').split(', ').map((column) => column.replace(/^al\./, '')),
  expectedColumns,
  '审计列表 SQL 只能读取页面首屏需要的列'
)

const item = auditLogListItemFromRow({
  id: 'audit-1',
  trace_id: 'trace-1',
  session_id: 'session-1',
  session_client_type: 'codex',
  traffic_source: 'gateway',
  system_account_id: 'system-1',
  api_key_id: 'key-1',
  api_key_name: 'Key 1',
  group_id: 'group-1',
  group_name: 'Group 1',
  account_id: 'account-1',
  account_name: 'Account 1',
  method: 'POST',
  path: '/v1/responses',
  model: 'gpt-5',
  upstream_model: 'gpt-5-upstream',
  model_mapping_applied: 1,
  stream: 1,
  audit_outcome: 'success',
  success: 1,
  final_status_code: 200,
  duration_ms: 15,
  http_duration_ms: 18,
  created_at: '2026-07-23T00:00:00.000Z',
  pricing_model: 'must-not-leak',
  sample_reason: 'must-not-leak',
  raw_payload_bytes: 1024,
  error_message: 'must-not-leak'
}, new Map([['system-1', 'System 1']]))

assert.deepEqual(Object.keys(item), [
  'id', 'traceId', 'sessionId', 'sessionClientType',
  'trafficSource', 'systemAccountId', 'systemAccountName',
  'apiKeyId', 'apiKeyName', 'groupId', 'groupName', 'accountId', 'accountName',
  'method', 'path', 'model', 'upstreamModel', 'modelMappingApplied', 'stream',
  'auditOutcome', 'success', 'finalStatusCode', 'durationMs', 'httpDurationMs',
  'createdAt'
], '审计列表 DTO 不得携带详情、payload 或采样字段')

console.log('审计日志列表投影回归通过：SQL 与 DTO 均严格限制为页面消费字段')

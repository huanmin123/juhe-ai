import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { publicAccountRuntimeAvailability } from '../../domain/account-runtime-availability-public.js'
import type { AccountRuntimeAvailability, AccountSummary } from '../../domain/types.js'
import {
  projectAccountListItem,
  sanitizeAccountBasicDetailResponse,
  sanitizeAccountRuntimeAvailabilityResponse
} from '../../modules/accounts/account-response-sanitizer.js'

const unsafeRuntime: AccountRuntimeAvailability = {
  status: 'precheck_pending',
  reason: '等待确认',
  since: '2026-07-23T00:00:00.000Z',
  failureCount: 8,
  distinctClientIpCount: 3,
  distinctApiKeyCount: 2,
  precheckAttemptCount: 4,
  localFailureCount: 3,
  probePresentation: {
    schedule: { state: 'running' },
    recoveryAt: '2026-07-23T00:02:00.000Z',
    recoveryAtKind: 'policy_ttl_expiry'
  }
}

const expectedPublicRuntime = {
  status: 'precheck_pending',
  reason: '等待确认',
  since: '2026-07-23T00:00:00.000Z',
  probePresentation: { schedule: { state: 'running' } }
}

assert.deepEqual(
  publicAccountRuntimeAvailability(unsafeRuntime),
  expectedPublicRuntime,
  '状态快照公开投影不得返回内部计数或恢复控制字段'
)

const account = {
  id: 'account-runtime-redaction',
  runtimeAvailability: unsafeRuntime,
  credentials: {},
  supportedModels: [],
  modelMappings: [],
  apiKeyRuntimeDetails: [],
  usage: {},
  oauthUsage: undefined,
  authorizationSources: [],
  authorizationCount: 0,
  authorizationTeamCount: 0,
  authorizationUsageAvailable: false
} as unknown as AccountSummary

assert.deepEqual(
  projectAccountListItem(account).runtimeAvailability,
  expectedPublicRuntime,
  '账户列表投影不得返回运行态内部字段'
)
assert.deepEqual(
  sanitizeAccountBasicDetailResponse(account).runtimeAvailability,
  expectedPublicRuntime,
  '账户详情投影不得返回运行态内部字段'
)
assert.deepEqual(
  sanitizeAccountRuntimeAvailabilityResponse({ runtimeAvailability: unsafeRuntime }).runtimeAvailability,
  expectedPublicRuntime,
  '高级详情的运行态专用投影不得返回内部字段'
)

for (const [label, value] of [
  ['list', projectAccountListItem(account).runtimeAvailability],
  ['detail', sanitizeAccountBasicDetailResponse(account).runtimeAvailability],
  ['status-snapshot', publicAccountRuntimeAvailability(unsafeRuntime)]
] as const) {
  const encoded = JSON.stringify(value)
  for (const field of [
    'failureCount',
    'distinctClientIpCount',
    'distinctApiKeyCount',
    'precheckAttemptCount',
    'localFailureCount',
    'until',
    'leaseId',
    'leasePurpose',
    'leaseUntilMs',
    'recoveryAt',
    'recoveryAtKind'
  ]) {
    assert.equal(encoded.includes(`"${field}"`), false, `${label} 不得返回 ${field}`)
  }
}

const statusSnapshotSource = readFileSync(resolve('src/modules/accounts/account-status-snapshot.service.ts'), 'utf8')
assert.match(
  statusSnapshotSource,
  /publicAccountRuntimeAvailability\(runtime\.values\[runtimeKey\]\)/,
  '状态快照必须在写入响应前执行公开运行态投影'
)
const runtimeSnapshotSource = readFileSync(resolve('src/modules/gateway/runtime/runtime-snapshot.service.ts'), 'utf8')
assert.match(
  runtimeSnapshotSource,
  /runtimeAvailability:\s*publicAccountRuntimeAvailability\(runtimeStatus\)/,
  '列表和详情的运行态合并必须先执行公开投影'
)

console.log('账户运行态响应脱敏回归通过：list/detail/status-snapshot 均只返回公开字段')

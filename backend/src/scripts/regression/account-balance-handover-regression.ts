import assert from 'node:assert/strict'

import type { AccountBalanceRefreshCandidate } from '../../storage/account-balance.repository.js'
import {
  parseAccountBalanceHandoverResult,
  prepareAccountBalanceHandoverInput,
  projectAccountBalanceHandoverResult,
  resolveAccountBalanceHandoverGate,
  accountBalanceGoOwnerEnabled,
  accountBalanceNodeOwnerEnabled,
  sameAccountBalanceJobsPostgresStore
} from '../../modules/background/account-balance-handover.js'

assert.equal(accountBalanceGoOwnerEnabled({}), false, 'J2 默认不得切到 Go owner')
assert.equal(accountBalanceNodeOwnerEnabled({}), true, 'J2 默认仍由 Node owner')
assert.equal(accountBalanceGoOwnerEnabled({ JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER: 'go' }), true, '显式 go owner 才能切换')
assert.equal(accountBalanceNodeOwnerEnabled({ JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER: 'go' }), false, '显式 go owner 必须停止 Node owner')
assert.equal(accountBalanceGoOwnerEnabled({ JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER: 'GO' }), true, 'owner gate 必须大小写无关')
assert.equal(sameAccountBalanceJobsPostgresStore('postgres://jobs-writer@db.example:5432/juhe_jobs?sslmode=require', 'postgres://projector-reader@DB.EXAMPLE/juhe_jobs'), true, 'jobs writer 与 projector 可使用不同凭据但必须同库')
assert.equal(sameAccountBalanceJobsPostgresStore('postgres://jobs@db.example/juhe_jobs_a', 'postgres://reader@db.example/juhe_jobs_b'), false, '不同 jobs DB 不得通过 handover gate')
assert.equal(sameAccountBalanceJobsPostgresStore('postgres://writer@db.example', 'postgres://reader@db.example/'), false, '未显式数据库名会由驱动回退到不同角色默认库，handover 必须拒绝')
assert.equal(sameAccountBalanceJobsPostgresStore('postgres://writer@db.example/juhe_jobs?dbname=other_jobs', 'postgres://reader@db.example/juhe_jobs'), false, '驱动对 dbname query override 的解析不一致，handover 必须拒绝')
assert.equal(sameAccountBalanceJobsPostgresStore('postgres://writer@db.example/juhe_jobs?database=other_jobs', 'postgres://reader@db.example/juhe_jobs'), false, '驱动对 database query override 的解析不一致，handover 必须拒绝')

const gate = {
  enabled: true,
  goCommandWiringReady: true,
  goInputResultReady: true,
  goProjectionReady: true,
  nodeOwnerDrained: true
} as const

const candidate: AccountBalanceRefreshCandidate = {
  id: 'acct-j2-handover',
  systemAccountId: 'sys-j2-handover',
  configRevision: 7,
  credentials: {
    base_url: 'https://relay.example.test/v1',
    api_key: 'sk-j2-handover-secret'
  },
  config: { adapter: 'builtin', intervalMinutes: 5 },
  nextRefreshAt: '2026-08-18T00:00:00+08:00',
  proxyProfileId: 'proxy-j2'
}

assert.deepEqual(resolveAccountBalanceHandoverGate(), {
  enabled: false,
  reason: 'disabled_by_default'
}, 'J2 handover must remain off by default')
assert.deepEqual(resolveAccountBalanceHandoverGate({ enabled: true }), {
  enabled: false,
  reason: 'go_command_wiring_missing'
}, 'enabling without Go command wiring must fail closed')

const disabled = prepareAccountBalanceHandoverInput(candidate)
assert.deepEqual(disabled, { enabled: false, reason: 'disabled_by_default' })

const prepared = prepareAccountBalanceHandoverInput(candidate, {
  gate,
  now: new Date('2026-08-18T00:00:00.000Z'),
  deadlineAt: new Date('2026-08-18T00:00:20.000Z'),
  expiresAt: new Date('2026-08-18T00:05:00.000Z')
})
assert.equal(prepared.enabled, true)
if (!prepared.enabled) throw new Error('J2 input should be enabled in the complete fixture gate')
assert.equal(prepared.prepared.input.baseUrl, 'https://relay.example.test')
assert.equal(prepared.prepared.input.nextRefreshAt, '2026-08-17T16:00:00.000Z')
assert.equal(prepared.prepared.input.credential.kind, 'api_key')
assert.doesNotMatch(prepared.prepared.body.toString('utf8'), /sk-j2-handover-secret/u, 'J2 input body must not contain plaintext API Key')

const result = parseAccountBalanceHandoverResult(JSON.stringify({
  schemaVersion: 1,
  job: 'account-balance-refresh',
  result: {
    schemaVersion: 1,
    job: 'account-balance-refresh',
    accountId: candidate.id,
    systemAccountId: candidate.systemAccountId,
    configRevision: candidate.configRevision,
    expectedNextRefreshAt: '2026-08-17T16:00:00Z',
    nextRefreshAfter: '2026-08-18T00:05:00Z',
    outcome: 'refreshed',
    committed: true,
    snapshot: {
      status: 'fresh',
      remainingUsd: '7.310000',
      rawRemaining: '7.310000',
      rawUnit: 'usd',
      basis: 'wallet'
    }
  }
}))
const projected = projectAccountBalanceHandoverResult(result, {
  accountId: candidate.id,
  systemAccountId: candidate.systemAccountId,
  configRevision: candidate.configRevision,
  nextRefreshAt: candidate.nextRefreshAt
}, gate)
assert.equal(projected.projected, true)
if (!projected.projected) throw new Error('J2 result should project in the complete fixture gate')
assert.equal(projected.projection.snapshot.remainingUsd, '7.310000')

const stale = projectAccountBalanceHandoverResult(result, {
  accountId: candidate.id,
  systemAccountId: candidate.systemAccountId,
  configRevision: candidate.configRevision,
  nextRefreshAt: '2026-08-18T00:01:00Z'
}, gate)
assert.deepEqual(stale, { projected: false, reason: 'next_refresh_fence_mismatch' })

assert.throws(() => parseAccountBalanceHandoverResult(JSON.stringify({
  schemaVersion: 1,
  job: 'account-balance-refresh',
  result: { ...result, secret: 'should-not-be-accepted' }
})), /未知或缺失字段/u, 'J2 result must reject unknown fields')

assert.throws(() => prepareAccountBalanceHandoverInput({
  ...candidate,
  credentials: { ...candidate.credentials, api_keys: ['sk-one', 'sk-two'] }
}, { gate }), /一个有效的 API Key/u, 'J2 input must reject multi-Key candidates')

console.log('account balance handover regression passed')

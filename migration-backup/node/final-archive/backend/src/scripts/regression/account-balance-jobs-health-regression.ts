import assert from 'node:assert/strict'

import { accountBalanceJobsOutcomeProjectionRuntimeFresh } from '../../modules/background/account-balance-jobs-outcome-projection-runtime.service.js'
import { accountBalanceGoOwnerHealth, resolveSystemApiHealth } from '../../modules/system-api/system-api-app.js'
import { resolveRuntimeReadiness } from '../../shared/runtime-readiness.js'

assert.equal(accountBalanceJobsOutcomeProjectionRuntimeFresh(true, true, 10_001, 10_000), true, '最近成功的 drain 必须让 projector ready')
assert.equal(accountBalanceJobsOutcomeProjectionRuntimeFresh(true, true, 10_000, 10_000), false, '挂起 drain 超过新鲜度截止时间必须让 projector 降级')
assert.equal(accountBalanceJobsOutcomeProjectionRuntimeFresh(false, true, 10_001, 10_000), false, '停止中的 projector 不能 ready')

assert.deepEqual(await accountBalanceGoOwnerHealth({}), { enabled: false, ready: true }, '非 Go owner 不应把 J2 加入 DB-service health')

const goOwnerEnv = {
  JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER: 'go',
  JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_URL: 'http://127.0.0.1:3305/account-balance/manual'
}

assert.deepEqual(await accountBalanceGoOwnerHealth(goOwnerEnv, {
  projectorReady: () => false,
  fetch: async () => { throw new Error('projector 未启动时不得探测 Go jobs') }
}), { enabled: true, ready: false, projectorReady: false }, 'Go owner 且 projector 未启动必须拒绝 DB-service health')
assert.deepEqual(resolveSystemApiHealth({ enabled: true, ready: false, projectorReady: false }, { enabled: false, ready: true }), {
  statusCode: 200,
  status: 'degraded',
  service: 'juhe-ai-db-service',
  accountBalance: { enabled: true, ready: false, projectorReady: false },
  proxyLatency: { enabled: false, ready: true }
}, 'J2 transient health 只能在 DB-service body 中降级，不得把 Node readiness 变成 503')

const ready = await accountBalanceGoOwnerHealth(goOwnerEnv, {
  projectorReady: () => true,
  fetch: async (url) => {
    assert.equal(String(url), 'http://127.0.0.1:3305/health', 'J2 health 必须从 manual bridge origin 读取 Go jobs health')
    return new Response(JSON.stringify({ ready: true, accountBalanceEnabled: true, accountBalanceReady: true }), { status: 200 })
  }
})
assert.deepEqual(ready, { enabled: true, ready: true, projectorReady: true }, 'Go jobs 与 projector 都 ready 时 DB-service health 必须通过')
assert.equal(resolveSystemApiHealth(ready, { enabled: false, ready: true }).statusCode, 200, 'J2 ready health 仍应返回 200')

const standby = await accountBalanceGoOwnerHealth({
  ...goOwnerEnv,
  JUHE_AI_BLUE_GREEN_OWNER_MODE: 'standby'
}, {
  projectorReady: () => true,
  fetch: async () => new Response(JSON.stringify({
    ready: false,
    ownerMode: 'standby',
    accountBalanceEnabled: false,
    accountBalanceReady: false
  }), { status: 200 })
})
assert.deepEqual(standby, { enabled: true, ready: true, projectorReady: true, ownerMode: 'standby' as const }, 'standby 不持有 J2 owner 时，健康检查必须以进程可达和投影新鲜度判定')
assert.equal(resolveSystemApiHealth(standby, { enabled: false, ready: true }).status, 'ok', '正常 standby 不得被误报为 degraded')

const standbyWithWrongMode = await accountBalanceGoOwnerHealth({
  ...goOwnerEnv,
  JUHE_AI_BLUE_GREEN_OWNER_MODE: 'standby'
}, {
  projectorReady: () => true,
  fetch: async () => new Response(JSON.stringify({
    ready: true,
    ownerMode: 'active',
    accountBalanceEnabled: true,
    accountBalanceReady: true
  }), { status: 200 })
})
assert.equal(standbyWithWrongMode.ready, false, 'standby 探测到错误 ownerMode 时必须拒绝 ready')

assert.deepEqual(resolveRuntimeReadiness({
  dbServiceReady: true,
  workerTopologyReady: true,
  topologyGatesHealth: true
}), {
  statusCode: 200,
  status: 'ok',
  dbServiceReady: true,
  workerTopologyReady: true,
  blockers: []
}, 'Node runtime 与 worker topology 正常时 readiness 必须通过')
assert.deepEqual(resolveRuntimeReadiness({
  dbServiceReady: true,
  workerTopologyReady: false,
  topologyGatesHealth: true
}), {
  statusCode: 503,
  status: 'starting',
  dbServiceReady: true,
  workerTopologyReady: false,
  blockers: ['worker_topology_not_ready']
}, '真实 worker topology 未就绪仍必须阻断 Node readiness')
assert.deepEqual(resolveRuntimeReadiness({
  dbServiceReady: false,
  workerTopologyReady: true,
  topologyGatesHealth: false
}), {
  statusCode: 503,
  status: 'starting',
  dbServiceReady: false,
  workerTopologyReady: true,
  blockers: ['db_service_unavailable']
}, '强制 DB service failure 仍必须阻断 Node readiness')

console.log('account balance jobs health regression passed')

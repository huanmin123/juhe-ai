import assert from 'node:assert/strict'

import { accountBalanceJobsOutcomeProjectionRuntimeFresh } from '../../modules/background/account-balance-jobs-outcome-projection-runtime.service.js'
import { accountBalanceGoOwnerHealth } from '../../modules/system-api/system-api-app.js'

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

const ready = await accountBalanceGoOwnerHealth(goOwnerEnv, {
  projectorReady: () => true,
  fetch: async (url) => {
    assert.equal(String(url), 'http://127.0.0.1:3305/health', 'J2 health 必须从 manual bridge origin 读取 Go jobs health')
    return new Response(JSON.stringify({ ready: true, accountBalanceEnabled: true, accountBalanceReady: true }), { status: 200 })
  }
})
assert.deepEqual(ready, { enabled: true, ready: true, projectorReady: true }, 'Go jobs 与 projector 都 ready 时 DB-service health 必须通过')

console.log('account balance jobs health regression passed')

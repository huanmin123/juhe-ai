import assert from 'node:assert/strict'
import { mkdirSync, rmSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import { accountCircuitScopeKey } from '../../modules/gateway/runtime/account-circuit-store.js'
import { GatewayAccountCircuitService } from '../../modules/gateway/runtime/account-circuit.service.js'
import {
  advanceAccountCircuitDispatchRevisionFamilyInSqliteTransaction,
  compareAndSetAccountCircuitIncident,
  listAccountCircuitIncidentsForRebuild
} from '../../storage/account-circuit-control-plane.repository.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-dispatch-revision-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'dispatch-revision-write-regression'
runtimeConfig.processRole = 'worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false

const repositories = await import('../../storage/repositories.js')
const { closeStorageDatabases, getBusinessDatabase } = await import('../../storage/database.js')

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const database = getBusinessDatabase()
const source = readFileSync(resolve('src/storage/repositories.ts'), 'utf8')
const circuitControlPlaneSource = readFileSync(resolve('src/storage/account-circuit-control-plane.repository.ts'), 'utf8')

try {
  assert.match(source, /advanceAccountCircuitDispatchRevisionFamilyInSqliteTransaction\(database/, '同步账户写入必须在账户事务内推进 owner/authorized dispatch revision family')
  assert.match(source, /advanceAccountCircuitDispatchRevisionFamilyInTransaction\((client|tx)/, '异步账户写入必须在 DatabaseClient 事务内推进 owner/authorized dispatch revision family')
  assert.match(circuitControlPlaneSource, /lockedRootId !== rootId[\s\S]{0,180}账户授权关系在锁定期间发生变化/, 'family root 锁定后必须复读 source pointer，防止并发改写造成错误 family')
  assert.match(circuitControlPlaneSource, /const lockedInput = input\.accountId === familyRootId[\s\S]{0,220}lockAccountDispatchRevision\(client, input\.accountId\)/, 'family revision 必须先锁 parent，再锁触发账户并复读 source pointer')
  assert.match(circuitControlPlaneSource, /lockedRootId !== familyRootId[\s\S]{0,180}账户授权关系在锁定期间发生变化/, 'family revision source pointer 改写必须 fail-closed')

  const group = repositories.createGroup({
    name: 'dispatch revision write regression',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'dispatch revision write account',
    type: 'api_key',
    credentials: { api_key: 'sk-dispatch-revision-write', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-4o'],
    groupId: group.id,
    status: 'disabled'
  }, access)
  const grantee = repositories.createSystemAccount({
    username: `dispatch_revision_grantee_${Date.now()}`,
    displayName: 'dispatch-revision-grantee',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: 'dispatch revision authorized group',
    providerCode: 'gpt',
    enabled: true
  }, granteeAccess)
  const authorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: 'dispatch revision family regression'
  }, access)
  const authorizedInstance = database.prepare(`
    SELECT id, dispatch_revision
    FROM accounts
    WHERE authorization_instance_source_account_id = ?
      AND system_account_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `).get(account.id, grantee.id) as unknown as { id: string; dispatch_revision: number } | undefined
  assert(authorizedInstance?.id, '回归必须创建授权实例')
  assert.equal(authorizedInstance.dispatch_revision, 1, '新授权实例应从独立 revision 起点开始')

  const oldOwnerScopeKey = `account:${account.id}`
  const authorizedRuntimeKey = `${authorizedInstance.id}:authorized:${grantee.id}:${granteeGroup.id}:${authorization.id}`
  const oldAuthorizedScopeKey = `account:${authorizedRuntimeKey}`
  for (const incident of [
    { accountId: account.id, runtimeKey: account.id, scopeKey: oldOwnerScopeKey, revision: 2 },
    { accountId: authorizedInstance.id, runtimeKey: authorizedRuntimeKey, scopeKey: oldAuthorizedScopeKey, revision: 1 }
  ]) {
    const persisted = await compareAndSetAccountCircuitIncident({
      accountId: incident.accountId,
      accountRuntimeKey: incident.runtimeKey,
      circuitScopeKey: incident.scopeKey,
      scopeKind: 'account',
      incidentId: `incident:${incident.scopeKey}`,
      state: 'OPEN',
      generation: 1,
      dispatchRevision: incident.revision,
      expectedLedgerRevision: null,
      transitionId: `open:${incident.accountId}`,
      nextTransitionAtMs: Date.now() + 60_000,
      openUntilMs: Date.now() + 60_000,
      backoffLevel: 1,
      recoveringSuccesses: 0,
      upstreamAttemptObserved: true
    })
    assert.equal(persisted.status, 'applied')
  }
  assert.equal((await listAccountCircuitIncidentsForRebuild({ limit: 20 })).items.length, 2, 'revision 变化前 owner/authorized incident 应可重建')

  const created = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(created.dispatch_revision, 2, '新建账户必须写入一次初始 dispatch revision 事件')
  assert.equal((database.prepare("SELECT COUNT(*) AS count FROM account_circuit_outbox WHERE account_id = ? AND event_type = 'dispatch_revision_changed'").get(account.id) as { count: number }).count, 1)

  repositories.updateAccount(account.id, { notes: '仅备注变化' }, access)
  const afterNotes = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterNotes.dispatch_revision, 2, '非调度字段更新不得推进 dispatch revision')

  repositories.updateAccount(account.id, {
    credentials: {
      ...account.credentials,
      error_handling_rules: [{
        enabled: true,
        name: '用户显式策略',
        priority: 1,
        action: 'retry_next',
        status_codes: [429]
      }]
    }
  }, access)
  const afterExplicitPolicy = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterExplicitPolicy.dispatch_revision, 2, '用户显式错误策略变化不得复活 OPEN 传输电路')

  repositories.updateAccount(account.id, { priority: 5 }, access)
  const afterPriority = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterPriority.dispatch_revision, 2, '优先级变化不得清空传输故障电路')
  assert.equal((database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision, 1, '来源优先级变化不得推进授权实例电路 revision')

  repositories.updateAccount(account.id, { concurrencyLimit: 9 }, access)
  const afterConcurrency = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterConcurrency.dispatch_revision, 2, '并发上限变化不得清空传输故障电路')
  assert.equal((await listAccountCircuitIncidentsForRebuild({ limit: 20 })).items.length, 2, '无关调度配置变化后 owner/authorized OPEN 必须仍可重建')

  repositories.updateAccount(account.id, { credentials: { api_key: 'sk-dispatch-revision-write-2', base_url: 'https://api.openai.com/v1' } }, access)
  const afterCredentials = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterCredentials.dispatch_revision, 3, '凭据变化必须推进电路 owner revision')
  assert.equal((database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision, 2, '来源凭据变化必须 fence 授权实例旧运行态')
  assert.equal((await listAccountCircuitIncidentsForRebuild({ limit: 20 })).items.length, 0, '连接身份 revision 变化后旧 owner/authorized incident 不得被 rebuild 复活')

  const proxy = repositories.createProxy({
    name: 'dispatch revision proxy',
    type: 'http',
    host: '127.0.0.1',
    port: 17890,
    enabled: true
  }, access)
  repositories.updateAccount(account.id, { proxyProfileId: proxy.id }, access)
  assert.equal((database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }).dispatch_revision, 4, '代理绑定变化必须创建新电路代')
  assert.equal((database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision, 3, '来源代理变化必须 fence 授权实例旧运行态')

  assert.equal((database.prepare("SELECT COUNT(*) AS count FROM account_circuit_outbox WHERE account_id = ? AND event_type = 'dispatch_revision_changed'").get(account.id) as { count: number }).count, 3)
  assert.equal((database.prepare("SELECT COUNT(*) AS count FROM account_circuit_outbox WHERE account_id = ? AND account_runtime_key = ? AND event_type = 'dispatch_revision_changed'").get(authorizedInstance.id, authorizedInstance.id) as { count: number }).count, 2, '授权实例必须获得自己的裸 ID revision outbox')

  const genericRestoreOwnerRevision = (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }).dispatch_revision
  const genericRestoreAuthorizedRevision = (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision
  for (const incident of [
    { accountId: account.id, runtimeKey: account.id, scopeKey: `account:${account.id}:generic-restore`, revision: genericRestoreOwnerRevision },
    { accountId: authorizedInstance.id, runtimeKey: authorizedRuntimeKey, scopeKey: `account:${authorizedRuntimeKey}:generic-restore`, revision: genericRestoreAuthorizedRevision }
  ]) {
    const persisted = await compareAndSetAccountCircuitIncident({
      accountId: incident.accountId,
      accountRuntimeKey: incident.runtimeKey,
      circuitScopeKey: incident.scopeKey,
      scopeKind: 'account',
      incidentId: `incident:${incident.scopeKey}`,
      state: 'OPEN',
      generation: 9,
      dispatchRevision: incident.revision,
      expectedLedgerRevision: null,
      transitionId: `open:${incident.scopeKey}`,
      nextTransitionAtMs: Date.now() + 60_000,
      openUntilMs: Date.now() + 60_000,
      backoffLevel: 1,
      recoveringSuccesses: 0,
      upstreamAttemptObserved: true
    })
    assert.equal(persisted.status, 'applied')
  }
  repositories.updateAccount(account.id, { status: 'disabled' }, access)
  const disabledOwnerRevision = (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }).dispatch_revision
  assert.equal(disabledOwnerRevision, genericRestoreOwnerRevision, '停用账户本身不应提前推进 dispatch revision')
  repositories.updateAccount(account.id, { status: 'active' }, access)
  assert.equal(
    (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }).dispatch_revision,
    genericRestoreOwnerRevision + 1,
    '公共账户更新 disabled -> active 必须推进 dispatch revision'
  )
  assert.equal(
    (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision,
    genericRestoreAuthorizedRevision + 1,
    '公共账户更新恢复 owner 时必须同时 fence 授权实例'
  )
  assert.equal((await listAccountCircuitIncidentsForRebuild({ limit: 20 })).items.some((item) => item.incidentId.includes('generic-restore')), false, '公共恢复后的旧 OPEN incident 不得 rebuild')

  database.prepare(`
    UPDATE accounts
    SET status = 'temporary_unavailable', schedulable = 0,
        cooldown_until = ?, last_error_code = 'temporary_unavailable', last_error_message = '回归故障'
    WHERE id = ?
  `).run(new Date(Date.now() + 60_000).toISOString(), account.id)
  const beforeRestore = database.prepare('SELECT config_revision, dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { config_revision: number; dispatch_revision: number }
  await compareAndSetAccountCircuitIncident({
    accountId: account.id,
    accountRuntimeKey: account.id,
    circuitScopeKey: oldOwnerScopeKey,
    scopeKind: 'account',
    incidentId: 'incident:restore-open',
    state: 'OPEN',
    generation: 2,
    dispatchRevision: beforeRestore.dispatch_revision,
    expectedLedgerRevision: null,
    transitionId: 'open:restore',
    nextTransitionAtMs: Date.now() + 60_000,
    openUntilMs: Date.now() + 60_000,
    backoffLevel: 1,
    recoveringSuccesses: 0,
    upstreamAttemptObserved: true
  })
  repositories.clearAccountFailureState(account.id, access)
  const afterRestore = database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }
  assert.equal(afterRestore.dispatch_revision, beforeRestore.dispatch_revision + 1, '临时不可用恢复必须推进 dispatch revision')
  assert.equal(
    (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision,
    5,
    '来源账户恢复必须同时 fence 授权实例旧运行态'
  )
  assert.equal((await listAccountCircuitIncidentsForRebuild({ limit: 20 })).items.some((item) => item.incidentId === 'incident:restore-open'), false, '恢复后的旧 OPEN incident 不得 rebuild')
  const recoveryAccount: UpstreamAccount = {
    id: account.id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: '恢复 circuit regression',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-4o'],
    modelMappings: [],
    healthCheckModel: 'gpt-4o',
    healthCheckEndpointMode: 'responses_json',
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-dispatch-revision-write',
    apiKeys: ['sk-dispatch-revision-write'],
    streamFailureCount: 0,
    credentials: { api_key: 'sk-dispatch-revision-write' },
    dispatchRevision: afterRestore.dispatch_revision
  }
  const recoveryStore = new MemoryAccountCircuitStore({ capacity: 4, now: () => 10_000 })
  const recoveryScope = { kind: 'account' as const, accountRuntimeKey: account.id }
  await recoveryStore.restore({
    scope: recoveryScope,
    scopeKey: accountCircuitScopeKey(recoveryScope),
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: String(beforeRestore.dispatch_revision),
    transitionId: 'restore-old-open',
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    openedAtMs: 10_000,
    retryAtMs: 70_000,
    updatedAtMs: 10_000
  })
  const recoveryDecision = await new GatewayAccountCircuitService(recoveryStore, { now: () => 10_000 }).prepareAttempt({
    account: recoveryAccount,
    requestLane: 'text',
    model: 'gpt-4o',
    confirmationLeaseDurationMs: 30_000,
    confirmationFailuresRequired: 1
  })
  assert.equal(recoveryDecision.outcome, 'dispatchable', '恢复后的新 revision 必须 fence 旧 OPEN circuit')

  database.exec('BEGIN IMMEDIATE')
  try {
    database.prepare('UPDATE accounts SET priority = priority + 1 WHERE id = ?').run(account.id)
    advanceAccountCircuitDispatchRevisionFamilyInSqliteTransaction(database, {
      accountId: account.id,
      accountRuntimeKey: account.id,
      transitionId: 'rollback-dispatch-transition',
      nowMs: Date.now()
    })
    throw new Error('rollback sentinel')
  } catch (error) {
    database.exec('ROLLBACK')
    assert.equal((error as Error).message, 'rollback sentinel')
  }
  const afterRollback = database.prepare('SELECT dispatch_revision, priority FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number; priority: number }
  assert.equal(afterRollback.dispatch_revision, 6, '事务回滚不能留下单边 revision')
  assert.equal((database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision, 5, 'family 事务回滚不能留下授权实例单边 revision')
  assert.equal((database.prepare("SELECT COUNT(*) AS count FROM account_circuit_outbox WHERE account_id = ? AND event_type = 'dispatch_revision_changed'").get(account.id) as { count: number }).count, 5, '事务回滚不能留下单边 outbox')

  const beforeChildFamily = {
    owner: (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }).dispatch_revision,
    child: (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision
  }
  const childFamilyResult = advanceAccountCircuitDispatchRevisionFamilyInSqliteTransaction(database, {
    accountId: authorizedInstance.id,
    accountRuntimeKey: authorizedInstance.id,
    transitionId: 'authorized-child-family-transition',
    nowMs: Date.now()
  })
  assert.equal(childFamilyResult.length, 2, '从授权实例触发 family revision 时必须覆盖来源账户与全部授权实例')
  assert.equal(
    (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { dispatch_revision: number }).dispatch_revision,
    beforeChildFamily.owner + 1,
    '授权实例恢复触发 family revision 时必须推进来源账户'
  )
  assert.equal(
    (database.prepare('SELECT dispatch_revision FROM accounts WHERE id = ?').get(authorizedInstance.id) as { dispatch_revision: number }).dispatch_revision,
    beforeChildFamily.child + 1,
    '授权实例恢复触发 family revision 时必须推进自身'
  )
} finally {
  closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('account dispatch revision write regression passed')

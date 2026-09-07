import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-dispatch-demand-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-dispatch-demand-write-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, requestSchemas] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/accounts/account-request.schemas.js')
])

try {
  const routeSource = readFileSync(new URL('../../modules/accounts/account-authorized-dispatch.routes.ts', import.meta.url), 'utf8')
  const repositorySource = readFileSync(new URL('../../storage/account-authorized-dispatch.repository.ts', import.meta.url), 'utf8')
  assert.doesNotMatch(routeSource, /findAccountSummary|applyServerAccountRuntimeToAccount|sanitizeAccountResponse/, '授权调度 mutation route 不得回查或裁剪宽 AccountSummary')
  assert.doesNotMatch(repositorySource, /findAccountSummary|AccountSummary/, '授权调度 repository 不得依赖宽 AccountSummary')
  assert.match(routeSource, /configRevision: account\.configRevision[\s\S]*changedFields: account\.changedFields[\s\S]*patch: account\.patch/, '授权调度 route 必须只返回可合并 mutation DTO')

  assert.equal(requestSchemas.authorizedAccountDispatchSchema.safeParse({ expectedConfigRevision: 1 }).success, false, '空授权调度命令必须拒绝')
  assert.equal(requestSchemas.authorizedAccountDispatchSchema.safeParse({ expectedConfigRevision: 1, clearFailureState: false }).success, false, 'clearFailureState=false 不能伪装成写命令')
  assert.equal(requestSchemas.authorizedAccountDispatchSchema.safeParse({ expectedConfigRevision: 1, priority: 0 }).success, true, '单字段授权调度命令必须允许')

  const owner = repositories.createSystemAccount({
    username: 'authorized_dispatch_demand_owner',
    displayName: '授权调度减负所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorized_dispatch_demand_grantee',
    displayName: '授权调度减负被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({ name: '授权调度减负来源分组', providerCode: 'gpt' }, ownerAccess)
  const granteeGroup = repositories.listGroups(granteeAccess).find((group) => group.providerCode === 'gpt' && group.isDefault)
  assert(granteeGroup, '被授权人默认 GPT 分组不存在')
  const sourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '授权调度减负来源账户',
    type: 'api_key',
    supportedModels: ['gpt-5.5'],
    credentials: { api_key: 'sk-authorized-dispatch-demand', base_url: 'https://api.openai.com/v1' },
    groupId: ownerGroup.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id
  }, ownerAccess)
  const authorizedAccount = repositories.listAccounts(granteeAccess)
    .find((account) => account.authorizationInstanceSourceAccountId === sourceAccount.id)
  assert(authorizedAccount, '授权实例账户不存在')
  assert.equal(authorizedAccount.boundGroupId, granteeGroup.id, '授权实例必须绑定被授权人自己的分组')
  const initialConfigRevision = authorizedAccount.configRevision
  assert(initialConfigRevision !== undefined, '授权实例列表必须返回并发版本')

  const database = databaseModule.getBusinessDatabase()
  const authorizedInstanceRow = database.prepare(`
    SELECT id
    FROM accounts
    WHERE authorization_instance_source_account_id = ?
      AND system_account_id = ?
      AND deleted_at IS NULL
  `).get(sourceAccount.id, grantee.id) as unknown as { id: string } | undefined
  assert(authorizedInstanceRow, '授权实例物理账户行不存在')
  assert.equal(authorizedInstanceRow.id, authorizedAccount.id, '授权实例列表 ID 必须对应物理账户行')
  const priorityCapture = await captureSql(database, () => repositories.updateAuthorizedAccountBindingDispatchAsync(
    authorizedAccount.id,
    { expectedConfigRevision: initialConfigRevision, priority: 7 },
    granteeAccess
  ))
  assert(priorityCapture.value, '优先级 mutation 必须返回结果')
  assert.deepEqual(priorityCapture.value.patch, { priority: 7 }, '优先级 mutation 只返回实际变化的列表字段')
  assert.equal(priorityCapture.value.configRevision, initialConfigRevision + 1, '真实变化必须推进授权实例版本')
  const priorityDml = businessMutationCalls(priorityCapture.calls)
  assert.equal(priorityDml.filter((call) => /UPDATE\s+"?accounts"?/i.test(call.sql)).length, 1, '绑定字段变化只允许一次账户 CAS revision UPDATE')
  assert.equal(priorityDml.filter((call) => /UPDATE\s+"?group_accounts"?/i.test(call.sql)).length, 1, '优先级变化只允许一次绑定 UPDATE')
  const bindingUpdate = priorityDml.find((call) => /UPDATE\s+"?group_accounts"?/i.test(call.sql))
  assert(bindingUpdate, '必须捕获绑定 UPDATE')
  assert.match(bindingUpdate.sql, /"?local_priority"?\s*=\s*\?/, '优先级 mutation 必须更新 local_priority')
  assert.doesNotMatch(bindingUpdate.sql, /"?local_super_priority_enabled"?\s*=|"?local_fallback_enabled"?\s*=/, '优先级 mutation 不得重写其他绑定字段')

  const noOpCapture = await captureSql(database, () => repositories.updateAuthorizedAccountBindingDispatchAsync(
    authorizedAccount.id,
    { expectedConfigRevision: priorityCapture.value!.configRevision, priority: 7 },
    granteeAccess
  ))
  assert(noOpCapture.value, '同值 mutation 必须返回当前版本')
  assert.equal(noOpCapture.value.configRevision, priorityCapture.value.configRevision, '同值 mutation 不得推进版本')
  assert.deepEqual(noOpCapture.value.changedFields, [], '同值 mutation 必须返回空 changedFields')
  assert.deepEqual(businessMutationCalls(noOpCapture.calls), [], '同值 mutation 必须为 0 DML')

  await assert.rejects(() => repositories.updateAuthorizedAccountBindingDispatchAsync(
    authorizedAccount.id,
    { expectedConfigRevision: initialConfigRevision, priority: 8 },
    granteeAccess
  ), (error: unknown) => error instanceof repositories.AuthorizedAccountDispatchRevisionConflictError, '旧版本 mutation 必须触发 CAS 冲突')

  const disabled = await repositories.updateAuthorizedAccountBindingDispatchAsync(
    authorizedAccount.id,
    { expectedConfigRevision: priorityCapture.value.configRevision, status: 'disabled' },
    granteeAccess
  )
  assert(disabled, '停用授权实例必须返回 mutation')
  assert.deepEqual(disabled.patch, { status: 'disabled', schedulable: false }, '停用 mutation 只返回状态和联动调度字段')
  const ownerSourceAfterDisable = repositories.listAccounts(ownerAccess).find((account) => account.id === sourceAccount.id)
  assert.equal(ownerSourceAfterDisable?.status, 'active', '停用授权实例不得覆盖来源账户状态')

  database.prepare(`
    UPDATE accounts
    SET account_expires_at = ?
    WHERE id = ?
  `).run(new Date(Date.now() - 60_000).toISOString(), authorizedInstanceRow.id)
  const expiredAuthorizedInstance = database.prepare(`
    SELECT account_expires_at
    FROM accounts
    WHERE id = ?
  `).get(authorizedInstanceRow.id) as unknown as { account_expires_at: string | null } | undefined
  assert(expiredAuthorizedInstance?.account_expires_at, '授权实例回归造数必须写入到期时间')
  await assert.rejects(() => repositories.updateAuthorizedAccountBindingDispatchAsync(
    authorizedAccount.id,
    { expectedConfigRevision: disabled.configRevision, clearFailureState: true },
    granteeAccess
  ), /授权账户已到期/, '已过期授权账户不得通过异常恢复重新进入调度')
  database.prepare(`
    UPDATE accounts
    SET account_expires_at = NULL
    WHERE id = ?
  `).run(authorizedInstanceRow.id)
  database.prepare(`
    UPDATE accounts
    SET account_expires_at = ?
    WHERE id = ?
  `).run(new Date(Date.now() - 60_000).toISOString(), sourceAccount.id)
  await assert.rejects(() => repositories.updateAuthorizedAccountBindingDispatchAsync(
    authorizedAccount.id,
    { expectedConfigRevision: disabled.configRevision, status: 'active' },
    granteeAccess
  ), /授权方原账户已到期/, '来源账户已过期时不得重新启用授权实例')
  database.prepare(`
    UPDATE accounts
    SET account_expires_at = NULL
    WHERE id = ?
  `).run(sourceAccount.id)
  const stillDisabled = repositories.listAccounts(granteeAccess)
    .find((account) => account.id === authorizedAccount.id)
  assert.equal(stillDisabled?.status, 'disabled', '拒绝过期恢复不得改变授权实例状态')
  assert.equal(stillDisabled?.configRevision, disabled.configRevision, '拒绝过期恢复不得推进授权实例版本')

  const restored = await repositories.updateAuthorizedAccountBindingDispatchAsync(
    authorizedAccount.id,
    { expectedConfigRevision: disabled.configRevision, clearFailureState: true },
    granteeAccess
  )
  assert(restored, '恢复授权实例必须返回 mutation')
  assert.equal(restored.patch.status, 'active', '恢复 mutation 必须返回实际状态变化')
  assert.equal(restored.patch.schedulable, true, '恢复 mutation 必须返回实际调度变化')

  console.log('授权账户调度按需写回归通过：严格命令、最小 DTO、字段级 UPDATE、0 DML no-op 与 CAS 均符合契约')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

interface SqlCall {
  method: 'get' | 'all' | 'run' | 'exec'
  sql: string
  params: SQLInputValue[]
}

async function captureSql<T>(database: DatabaseSync, action: () => Promise<T>): Promise<{ value: T; calls: SqlCall[] }> {
  const calls: SqlCall[] = []
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const originalExec = database.exec.bind(database) as typeof database.exec
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    const originalRun = statement.run.bind(statement) as typeof statement.run
    statement.get = ((...params: SQLInputValue[]) => {
      calls.push({ method: 'get', sql, params })
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      calls.push({ method: 'all', sql, params })
      return originalAll(...params)
    }) as typeof statement.all
    statement.run = ((...params: SQLInputValue[]) => {
      calls.push({ method: 'run', sql, params })
      return originalRun(...params)
    }) as typeof statement.run
    return statement
  }) as typeof database.prepare
  database.exec = ((sql: string) => {
    calls.push({ method: 'exec', sql, params: [] })
    return originalExec(sql)
  }) as typeof database.exec
  try {
    return { value: await action(), calls }
  } finally {
    database.prepare = originalPrepare
    database.exec = originalExec
  }
}

function businessMutationCalls(calls: SqlCall[]): SqlCall[] {
  return calls.filter((call) => (
    (call.method === 'run' || call.method === 'exec')
    && /\b(?:INSERT(?:\s+OR\s+\w+)?\s+INTO|UPDATE|DELETE\s+FROM|REPLACE\s+INTO)\b/i.test(call.sql)
  ))
}

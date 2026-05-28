import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-return-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorization-return-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { authorizationsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/authorizations/authorizations.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-authorizations', forceSelfAccessScope, authorizationsRouter)
app.use('/__aisys__/api/authorizations', requireAdmin, authorizationsRouter)

let server: ReturnType<typeof app.listen> | undefined

try {
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('授权归还回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`
  const seed = seedData()

  const accountAuthorizationId = repositories
    .listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((account) => account.id === seed.ownerAccountId)?.accountAuthorizationId
  assert(accountAuthorizationId, '被授权账户应带运行态授权 ID')
  await returnOk(baseUrl, `/__aisys__/api/my-authorizations/${accountAuthorizationId}/return`, seed.granteeCookie)
  assert.equal(
    repositories.listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const }).some((account) => account.id === seed.ownerAccountId),
    false,
    '被授权用户归还账户授权后不应继续看到该授权账户'
  )
  assert.equal(
    repositories.listAccounts({ systemAccountId: seed.ownerId, role: 'user' as const }).some((account) => account.id === seed.ownerAccountId),
    true,
    '被授权用户归还账户授权不应删除授权方原账户'
  )
  const ownerAccess = { systemAccountId: seed.ownerId, role: 'user' as const }
  const granteeAccess = { systemAccountId: seed.granteeId, role: 'user' as const }
  const returnedAccountGrant = repositories
    .listResourceAuthorizations({}, ownerAccess)
    .find((authorization) => authorization.resourceType === 'account' && authorization.resourceId === seed.ownerAccountId && authorization.granteeSystemAccountId === seed.granteeId)
  assert.equal(returnedAccountGrant?.status, 'returned', '被授权用户归还后，授权方授权列表仍应保留已归还记录')
  const restoredAccountGrant = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: seed.ownerAccountId,
    granteeType: 'system_account',
    granteeId: seed.granteeId,
    remark: '授权账户归还后重新授权'
  }, ownerAccess)
  assert.equal(restoredAccountGrant.id, returnedAccountGrant?.id, '归还后重新授权应复用原授权业务记录')
  const restoredRuntimeAuthorizationId = repositories
    .listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((account) => account.id === seed.ownerAccountId)?.accountAuthorizationId
  assert.equal(restoredRuntimeAuthorizationId, accountAuthorizationId, '归还后重新授权应复用原用户级授权 ID')
  const pausedAccountGrant = repositories.updateResourceAuthorization(restoredAccountGrant.id, { status: 'paused' }, ownerAccess)
  assert.equal(pausedAccountGrant?.status, 'paused', '授权方暂停后授权状态应为 paused')
  const pausedAccount = repositories
    .listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((account) => account.id === seed.ownerAccountId)
  assert.equal(pausedAccount?.authorizationStatus, 'paused', '暂停授权后被授权账户仍应可见但标记为暂停')
  repositories.updateResourceAuthorization(restoredAccountGrant.id, { status: 'active' }, ownerAccess)
  const inboundAccountGrant = repositories
    .listResourceAuthorizations({ direction: 'inbound', status: 'all' }, granteeAccess)
    .find((authorization) => authorization.resourceType === 'account' && authorization.resourceId === seed.ownerAccountId && authorization.granteeSystemAccountId === seed.granteeId)
  assert.equal(inboundAccountGrant?.id, restoredAccountGrant.id, '个人授权列表应返回授权业务记录 ID')
  await returnOk(baseUrl, `/__aisys__/api/my-authorizations/${inboundAccountGrant.id}/return`, seed.granteeCookie)
  assert.equal(
    repositories.listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const }).some((account) => account.id === seed.ownerAccountId),
    false,
    '被授权用户通过个人授权列表归还后不应继续看到该授权账户'
  )
  const returnedAccountGrantByListId = repositories
    .listResourceAuthorizations({}, ownerAccess)
    .find((authorization) => authorization.resourceType === 'account' && authorization.resourceId === seed.ownerAccountId && authorization.granteeSystemAccountId === seed.granteeId)
  assert.equal(returnedAccountGrantByListId?.status, 'returned', '个人授权列表归还后，授权方授权列表仍应保留已归还记录')

  const groupAuthorizationId = repositories
    .listGroups({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((group) => group.id === seed.ownerGroupId)?.groupAuthorizationId
  assert(groupAuthorizationId, '被授权分组应带运行态授权 ID')
  await returnOk(baseUrl, `/__aisys__/api/my-authorizations/${groupAuthorizationId}/return`, seed.granteeCookie)
  assert.equal(
    repositories.listGroups({ systemAccountId: seed.granteeId, role: 'user' as const }).some((group) => group.id === seed.ownerGroupId),
    false,
    '被授权用户归还分组授权后不应继续看到该授权分组'
  )
  assert.equal(
    repositories.listGroups({ systemAccountId: seed.ownerId, role: 'user' as const }).some((group) => group.id === seed.ownerGroupId),
    true,
    '被授权用户归还分组授权不应删除授权方原分组'
  )

  const adminManagedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '管理员代归还授权账户',
    type: 'api_key',
    credentials: { api_key: 'sk-admin-authorization-return' }
  }, { systemAccountId: seed.ownerId, role: 'user' as const })
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: adminManagedAccount.id,
    granteeType: 'system_account',
    granteeId: seed.granteeId,
    remark: '管理员代归还授权使用权'
  }, { systemAccountId: seed.ownerId, role: 'user' as const })
  const adminManagedAuthorizationId = repositories
    .listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((account) => account.id === adminManagedAccount.id)?.accountAuthorizationId
  assert(adminManagedAuthorizationId, '管理员代归还前应能看到被授权账户')
  await returnOk(
    baseUrl,
    `/__aisys__/api/authorizations/${adminManagedAuthorizationId}/return?systemAccountId=${seed.granteeId}`,
    seed.adminCookie
  )
  assert.equal(
    repositories.listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const }).some((account) => account.id === adminManagedAccount.id),
    false,
    '管理员按用户作用域归还授权使用权后，该用户不应继续看到授权账户'
  )

  console.log('授权归还回归通过：被授权人可归还账户/分组授权使用权，且不删除授权方原资源')
} finally {
  await closeServer(server)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData() {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const owner = repositories.createSystemAccount({
    username: 'authorization_return_owner',
    displayName: '授权归还所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_return_grantee',
    displayName: '授权归还被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({
    name: '授权归还分组',
    providerCode: 'openai'
  }, ownerAccess)
  const ownerAccount = repositories.createAccount({
    providerCode: 'openai',
    groupId: ownerGroup.id,
    name: '授权归还账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorization-return' }
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '授权账户归还回归'
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: ownerGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '授权分组归还回归'
  }, ownerAccess)
  return {
    adminCookie: sessionCookie(admin.id),
    granteeCookie: sessionCookie(grantee.id),
    granteeId: grantee.id,
    ownerAccountId: ownerAccount.id,
    ownerGroupId: ownerGroup.id,
    ownerId: owner.id
  }
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function returnOk(baseUrl: string, path: string, cookie: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, { method: 'DELETE', headers: { cookie } })
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${await response.text()}`)
  }
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => {
      listeningServer.closeAllConnections?.()
      resolvePromise()
    }, 1000)
    listeningServer.close((error) => {
      clearTimeout(timeout)
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
    listeningServer.closeIdleConnections?.()
  })
}

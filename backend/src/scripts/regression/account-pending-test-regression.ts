import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import type { AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { submitAccountTestAndWait } from '../shared/account-test-task-client.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-pending-test-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-pending-test.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-pending-test-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  accountImport,
  accountExport,
  accountTestTasks,
  { accountsRouter },
  { forceSelfAccessScope, requireAuth },
  { requestContextMiddleware },
  { flushAllUsageRecordQueue },
  { flushAllOperationLogQueue }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/accounts/account-import.service.js'),
  import('../../modules/accounts/account-export.service.js'),
  import('../../storage/account-test-tasks.repository.js'),
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)

let appServer: ReturnType<typeof app.listen> | undefined
let mockOpenAIServer: http.Server | undefined
const mockOpenAIRequests: Array<{ authorization?: string; body: Record<string, unknown> }> = []

try {
  const owner = repositories.createSystemAccount({
    username: 'account_pending_test_owner',
    displayName: '待测试账户回归用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: '待测试账户回归分组',
    providerCode: 'gpt'
  }, access)

  const pending = repositories.createAccount({
    providerCode: 'gpt',
    name: '默认创建待测试账户',
    type: 'api_key',
    credentials: { api_key: 'sk-pending-default', base_url: 'https://api.openai.com/v1' },
    groupId: group.id
  }, access)
  assert.equal(pending.status, 'pending_test', '新建账户默认应为待测试')
  assert.equal(pending.schedulable, false, '待测试账户默认不得参与调度')
  assert.match(pending.lastErrorMessage ?? '', /测试通过/, '待测试账户应记录需测试通过的提示')
  assert.equal(
    repositories.listOpenAIAccountsForGroup(group.id, owner.id).some((account) => account.id === pending.id),
    false,
    '待测试账户不应进入网关调度候选'
  )
  assert.equal(
    repositories.findOpenAIAccountForGroup(group.id, pending.id, owner.id),
    undefined,
    '待测试账户即使指定账号也不应作为可用网关账号返回'
  )
  assert.equal(
    repositories.markAccountTestTemporaryUnavailable(pending, '模拟测试失败', access),
    undefined,
    '待测试账户测试失败不应被改写为临时不可调用'
  )
  assert.equal(
    repositories.clearAccountFailureState(pending.id, access)?.status,
    'pending_test',
    '普通恢复入口不应激活待测试账户'
  )
  assert.throws(
    () => repositories.updateAccount(pending.id, { status: 'disabled' }, access),
    /待测试账户需手动测试通过/,
    '待测试账户不应先停用再启用绕过测试'
  )

  const restored = repositories.clearAccountFailureStateResult(pending.id, access, { allowPendingTestRestore: true })
  assert.equal(restored.changed, true, '测试成功路径应允许激活待测试账户')
  assert.equal(restored.account?.status, 'active', '测试成功路径应把待测试账户改为正常')
  assert.equal(restored.account?.schedulable, true, '测试成功路径应恢复调度')
  assert.equal(
    repositories.listOpenAIAccountsForGroup(group.id, owner.id).some((account) => account.id === pending.id),
    true,
    '测试成功后账户应进入网关调度候选'
  )

  const importResult = accountImport.executeAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入 active 转待测试账户',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'active',
        groupId: group.id,
        credentials: { api_key: 'sk-import-active-to-pending', base_url: 'https://api.openai.com/v1' }
      }
    ]
  }, {}, access)
  assert.equal(importResult.summary.accounts.create, 1, '导入回归账户应创建成功')
  const importedId = importResult.accounts[0]?.accountId
  assert(importedId, '导入结果应返回账户 ID')
  const imported = repositories.findAccountSummary(importedId, access)
  assert.equal(imported?.status, 'pending_test', '导入 active 账户应落库为待测试')
  assert.equal(imported?.schedulable, false, '导入后待测试账户不得参与调度')

  const exportResult = accountExport.exportAccountsAsImportDocument({ accountIds: [importedId] }, access)
  assert.equal(exportResult.document.accounts[0]?.status, 'pending_test', '导出应保留待测试状态')

  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('待测试账户真实 mock 上游地址不可用')
  }

  appServer = app.listen(0, '127.0.0.1')
  await onceListening(appServer)
  const appAddress = appServer.address()
  if (!appAddress || typeof appAddress === 'string') {
    throw new Error('待测试账户创建路由回归服务地址不可用')
  }
  await assertRouteCreateActivation({
    baseUrl: `http://127.0.0.1:${appAddress.port}`,
    cookie: sessionCookie(owner.id),
    groupId: group.id,
    mockBaseUrl: `http://127.0.0.1:${mockAddress.port}`,
    ownerSystemAccountId: owner.id
  })

  console.log('待测试账户创建与调度保护回归通过：默认隔离、恢复防绕过、测试成功激活、编辑快照测试使用当前表单、导入导出状态一致，真实 mock 上游测试后才允许激活')
} finally {
  await closeServer(appServer)
  await closeServer(mockOpenAIServer)
  try {
    flushAllUsageRecordQueue()
    flushAllOperationLogQueue()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

interface RouteAccountCreatePayload {
  providerCode: 'gpt'
  name: string
  type: 'api_key'
  credentials: Record<string, unknown>
  groupId: string
  clientCompatibility?: AccountSummary['clientCompatibility']
  status?: 'active' | 'pending_test'
  activationTestTaskId?: string
}

interface ApiEnvelope<T> {
  data?: T
  message?: string
}

async function assertRouteCreateActivation(input: {
  baseUrl: string
  cookie: string
  groupId: string
  mockBaseUrl: string
  ownerSystemAccountId: string
}): Promise<void> {
  const withoutTask = routeAccountPayload(input.groupId, '路由直接正常无测试账户', 'sk-route-active-without-task')
  const withoutTaskResponse = await postJson<AccountSummary>(input.baseUrl, '/__aisys__/api/my-accounts', input.cookie, {
    ...withoutTask,
    status: 'active'
  })
  assert.equal(withoutTaskResponse.status, 400, '未携带成功草稿测试任务时不应允许创建正常账户')
  assert.match(responseMessage(withoutTaskResponse.body), /草稿测试/, '未测试直接正常创建应提示需要草稿测试')

  const realFailedDraftPayload = routeAccountPayload(input.groupId, '真实失败草稿测试账户', 'sk-real-fail-draft-test', input.mockBaseUrl)
  const failedDraftTask = await submitDraftAccountTestAndWait(input.baseUrl, input.cookie, realFailedDraftPayload)
  assert.equal(failedDraftTask.result?.success, false, '真实 mock 上游失败时，草稿测试结果应失败')
  assert.equal(failedDraftTask.result?.statusCode, 401, '真实 mock 上游失败应保留上游 HTTP 状态')
  const realFailedCreateResponse = await postJson<AccountSummary>(input.baseUrl, '/__aisys__/api/my-accounts', input.cookie, {
    ...realFailedDraftPayload,
    status: 'active',
    activationTestTaskId: failedDraftTask.id
  })
  assert.equal(realFailedCreateResponse.status, 400, '真实失败草稿测试任务不应允许创建正常账户')

  const realSuccessDraftPayload = routeAccountPayload(input.groupId, '真实成功草稿测试账户', 'sk-real-success-draft-test', input.mockBaseUrl)
  const successDraftTask = await submitDraftAccountTestAndWait(input.baseUrl, input.cookie, realSuccessDraftPayload)
  assert.equal(successDraftTask.result?.success, true, `真实 mock 上游草稿测试应成功：${successDraftTask.result?.message ?? ''}`)
  const realSuccessCreateResponse = await postJson<AccountSummary>(input.baseUrl, '/__aisys__/api/my-accounts', input.cookie, {
    ...realSuccessDraftPayload,
    status: 'active',
    activationTestTaskId: successDraftTask.id
  })
  assert.equal(realSuccessCreateResponse.status, 201, `真实成功草稿测试应允许创建正常账户：${responseMessage(realSuccessCreateResponse.body)}`)
  assert.equal(realSuccessCreateResponse.body.data?.status, 'active', '真实成功草稿测试创建的账户应为正常状态')
  assert.equal(realSuccessCreateResponse.body.data?.schedulable, true, '真实成功草稿测试创建的账户应参与调度')

  const realPendingPayload = routeAccountPayload(input.groupId, '真实手动测试激活账户', 'sk-real-manual-test-activate', input.mockBaseUrl)
  const realPendingCreateResponse = await postJson<AccountSummary>(input.baseUrl, '/__aisys__/api/my-accounts', input.cookie, realPendingPayload)
  assert.equal(realPendingCreateResponse.status, 201, `未携带草稿测试时应创建为待测试账户：${responseMessage(realPendingCreateResponse.body)}`)
  const realPendingAccount = realPendingCreateResponse.body.data
  assert(realPendingAccount?.id, '待测试账户创建结果应返回账户 ID')
  assert.equal(realPendingAccount.status, 'pending_test', '未携带草稿测试的新账户应为待测试')
  assert.equal(realPendingAccount.schedulable, false, '未携带草稿测试的新账户不应参与调度')
  assert.equal(
    repositories.listOpenAIAccountsForGroup(input.groupId, input.ownerSystemAccountId).some((account) => account.id === realPendingAccount.id),
    false,
    '待测试账户在真实手动测试前不应进入网关调度候选'
  )
  const manualTestResult = await submitAccountTestAndWait<AccountTestResult>({
    baseUrl: input.baseUrl,
    path: `/__aisys__/api/my-accounts/${realPendingAccount.id}/test`,
    cookie: input.cookie,
    body: { model: 'gpt-5.5' }
  })
  assert.equal(manualTestResult.success, true, `待测试账户真实手动测试应通过：${manualTestResult.message}`)
  assert.equal(manualTestResult.accountStatusChanged, true, '待测试账户测试成功后应报告状态变化')
  assert.equal(manualTestResult.accountStatus, 'active', '待测试账户测试成功后结果状态应为正常')
  const manualRestored = repositories.findAccountSummary(realPendingAccount.id, { systemAccountId: input.ownerSystemAccountId, role: 'user' })
  assert.equal(manualRestored?.status, 'active', '待测试账户真实手动测试通过后应恢复正常')
  assert.equal(manualRestored?.schedulable, true, '待测试账户真实手动测试通过后应参与调度')
  assert.equal(
    repositories.listOpenAIAccountsForGroup(input.groupId, input.ownerSystemAccountId).some((account) => account.id === realPendingAccount.id),
    true,
    '待测试账户真实手动测试通过后应进入网关调度候选'
  )

  const editSnapshotPayload = routeAccountPayload(input.groupId, '编辑弹框快照测试账户', 'sk-edit-saved-should-not-be-used', input.mockBaseUrl)
  const editSnapshotCreateResponse = await postJson<AccountSummary>(input.baseUrl, '/__aisys__/api/my-accounts', input.cookie, editSnapshotPayload)
  assert.equal(editSnapshotCreateResponse.status, 201, `编辑快照回归账户应创建为待测试：${responseMessage(editSnapshotCreateResponse.body)}`)
  const editSnapshotAccount = editSnapshotCreateResponse.body.data
  assert(editSnapshotAccount?.id, '编辑快照回归账户应返回账户 ID')
  assert.equal(editSnapshotAccount.status, 'pending_test', '编辑快照回归账户初始应为待测试')
  const beforeEditSnapshotRequestCount = mockOpenAIRequests.length
  const editSnapshotResult = await submitAccountTestAndWait<AccountTestResult>({
    baseUrl: input.baseUrl,
    path: `/__aisys__/api/my-accounts/${editSnapshotAccount.id}/test`,
    cookie: input.cookie,
    body: {
      model: 'gpt-5.5',
      account: routeAccountPayload(input.groupId, '编辑弹框快照测试账户（未保存）', 'sk-edit-current-input-test', input.mockBaseUrl)
    }
  })
  assert.equal(editSnapshotResult.success, true, `编辑弹框快照测试应通过：${editSnapshotResult.message}`)
  assert.equal(editSnapshotResult.accountStatusChanged, true, '编辑弹框快照测试成功后应报告状态变化')
  assert.equal(editSnapshotResult.accountStatus, 'active', '编辑弹框快照测试成功后结果状态应为正常')
  const editSnapshotRestored = repositories.findAccountSummary(editSnapshotAccount.id, { systemAccountId: input.ownerSystemAccountId, role: 'user' })
  assert.equal(editSnapshotRestored?.status, 'active', '编辑弹框快照测试通过后应直接恢复已保存账户状态')
  const editSnapshotRequests = mockOpenAIRequests.slice(beforeEditSnapshotRequestCount)
  assert(
    editSnapshotRequests.some((request) => request.authorization?.includes('sk-edit-current-input-test')),
    '编辑弹框快照测试应使用当前表单 API Key'
  )
  assert(
    editSnapshotRequests.every((request) => !request.authorization?.includes('sk-edit-saved-should-not-be-used')),
    '编辑弹框快照测试不应使用已保存但被表单覆盖的 API Key'
  )

  const failedTaskPayload = routeAccountPayload(input.groupId, '路由失败测试任务账户', 'sk-route-failed-task')
  const failedTaskId = createDraftActivationTask({
    payload: failedTaskPayload,
    ownerSystemAccountId: input.ownerSystemAccountId,
    success: false
  })
  const failedTaskResponse = await postJson<AccountSummary>(input.baseUrl, '/__aisys__/api/my-accounts', input.cookie, {
    ...failedTaskPayload,
    status: 'active',
    activationTestTaskId: failedTaskId
  })
  assert.equal(failedTaskResponse.status, 400, '失败草稿测试任务不应允许创建正常账户')
  assert.match(responseMessage(failedTaskResponse.body), /尚未成功/, '失败草稿测试任务应提示不能直接正常创建')

  const originalPayload = routeAccountPayload(input.groupId, '路由错配原始账户', 'sk-route-mismatch-task')
  const mismatchTaskId = createDraftActivationTask({
    payload: originalPayload,
    ownerSystemAccountId: input.ownerSystemAccountId,
    success: true
  })
  const changedPayload = routeAccountPayload(input.groupId, '路由错配变更账户', 'sk-route-mismatch-task')
  const mismatchResponse = await postJson<AccountSummary>(input.baseUrl, '/__aisys__/api/my-accounts', input.cookie, {
    ...changedPayload,
    status: 'active',
    activationTestTaskId: mismatchTaskId
  })
  assert.equal(mismatchResponse.status, 400, '草稿测试后修改账户内容不应允许创建正常账户')
  assert.match(responseMessage(mismatchResponse.body), /内容已变化/, '草稿测试内容变化应提示重新测试')

  const successPayload = routeAccountPayload(input.groupId, '路由测试通过账户', 'sk-route-success-task')
  const successTaskId = createDraftActivationTask({
    payload: successPayload,
    ownerSystemAccountId: input.ownerSystemAccountId,
    success: true
  })
  const successResponse = await postJson<AccountSummary>(input.baseUrl, '/__aisys__/api/my-accounts', input.cookie, {
    ...successPayload,
    status: 'active',
    activationTestTaskId: successTaskId
  })
  assert.equal(successResponse.status, 201, `成功且内容一致的草稿测试应允许创建正常账户：${responseMessage(successResponse.body)}`)
  assert.equal(successResponse.body.data?.status, 'active', '成功草稿测试创建的账户应为正常状态')
  assert.equal(successResponse.body.data?.schedulable, true, '成功草稿测试创建的账户应参与调度')
  assert.equal(
    repositories.listOpenAIAccountsForGroup(input.groupId, input.ownerSystemAccountId).some((account) => account.id === successResponse.body.data?.id),
    true,
    '成功草稿测试创建的正常账户应进入网关调度候选'
  )
  assert(
    mockOpenAIRequests.some((request) => request.authorization?.includes('sk-real-success-draft-test')),
    '真实成功草稿测试应命中 mock OpenAI 上游'
  )
  assert(
    mockOpenAIRequests.some((request) => request.authorization?.includes('sk-real-manual-test-activate')),
    '待测试账户手动测试应命中 mock OpenAI 上游'
  )
  assert(
    mockOpenAIRequests.some((request) => request.authorization?.includes('sk-edit-current-input-test')),
    '编辑快照测试应命中 mock OpenAI 上游'
  )
}

function routeAccountPayload(groupId: string, name: string, apiKey: string, baseUrl = 'https://api.openai.com/v1'): RouteAccountCreatePayload {
  return {
    providerCode: 'gpt',
    name,
    type: 'api_key',
    credentials: { api_key: apiKey, base_url: baseUrl },
    groupId
  }
}

function createDraftActivationTask(input: {
  payload: RouteAccountCreatePayload
  ownerSystemAccountId: string
  success: boolean
}): string {
  const draftAccount = draftActivationSnapshot(input.payload, input.ownerSystemAccountId)
  const task = accountTestTasks.createAccountTestTask({
    account: draftAccountSummary(draftAccount),
    access: { systemAccountId: input.ownerSystemAccountId, role: 'user' as const },
    diagnostics: 'full',
    draftAccount
  })
  assert(accountTestTasks.markAccountTestTaskRunning(task.id), '草稿测试任务应能进入运行中')
  const result: AccountTestResult = {
    accountId: task.accountId,
    accountName: task.accountName,
    providerCode: task.providerCode,
    type: task.type,
    success: input.success,
    statusCode: input.success ? 200 : 401,
    message: input.success ? '草稿测试成功' : '草稿测试失败'
  }
  assert(accountTestTasks.completeAccountTestTask(task.id, result), '草稿测试任务应能完成')
  return task.id
}

function draftActivationSnapshot(payload: RouteAccountCreatePayload, ownerSystemAccountId: string): AccountTestDraftSnapshot {
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    payload.providerCode,
    payload.type,
    payload.clientCompatibility,
    'openai_standard',
    { protocolCode: OPENAI_PROTOCOL_CODE, protocolVersion: OPENAI_PROTOCOL_VERSION }
  )
  return {
    id: `acctdraft_${payload.name}`,
    ownerSystemAccountId,
    groupId: payload.groupId,
    groupName: '待测试账户回归分组',
    providerCode: payload.providerCode,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    name: payload.name,
    type: payload.type,
    credentials: repositories.normalizeAccountCredentialsForWrite(payload.type, payload.credentials),
    concurrencyLimit: 20,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility,
    supportedModels: [],
    modelMappings: repositories.normalizeAccountModelMappingsForProvider([], payload.providerCode, ownerSystemAccountId) ?? []
  }
}

function draftAccountSummary(draft: AccountTestDraftSnapshot): AccountSummary {
  const usage = emptyUsageSummary()
  return {
    id: draft.id,
    systemAccountId: draft.ownerSystemAccountId,
    ownerSystemAccountId: draft.ownerSystemAccountId,
    providerCode: draft.providerCode,
    name: draft.name,
    type: draft.type,
    credentials: draft.credentials,
    status: 'active',
    concurrencyLimit: draft.concurrencyLimit,
    currentConcurrency: 0,
    priority: draft.priority,
    superPriorityEnabled: draft.superPriorityEnabled,
    fallbackEnabled: draft.fallbackEnabled,
    clientCompatibility: draft.clientCompatibility,
    supportedModels: draft.supportedModels,
    modelMappings: draft.modelMappings,
    schedulable: true,
    availabilityScheduleActive: true,
    todayUsage: usage,
    usage,
    accessType: 'owner',
    boundGroupId: draft.groupId,
    boundGroupName: draft.groupName,
    groupBindStatus: 'bound',
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canAuthorize: false,
      canViewCredentials: true,
      canManageAccounts: true,
      canBindToApiKey: true
    },
    effectiveAvailability: {
      available: true,
      status: 'available',
      label: '草稿测试',
      color: 'blue'
    }
  }
}

function emptyUsageSummary(): AccountSummary['usage'] {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

async function postJson<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<{ status: number; body: ApiEnvelope<T> }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      cookie
    },
    body: JSON.stringify(body)
  })
  return {
    status: response.status,
    body: await response.json() as ApiEnvelope<T>
  }
}

interface AccountTestTask<T = AccountTestResult> {
  id: string
  status: 'queued' | 'running' | 'success' | 'failed' | 'canceled'
  message?: string
  result?: T
}

async function submitDraftAccountTestAndWait(baseUrl: string, cookie: string, account: RouteAccountCreatePayload): Promise<AccountTestTask<AccountTestResult>> {
  const response = await postJson<AccountTestTask<AccountTestResult>>(baseUrl, '/__aisys__/api/my-accounts/test-draft', cookie, {
    account,
    model: 'gpt-5.5'
  })
  assert.equal(response.status, 202, `草稿测试任务应成功入队：${responseMessage(response.body)}`)
  const task = response.body.data
  assert(task?.id, '草稿测试任务应返回任务 ID')
  return waitForAccountTestTask(baseUrl, cookie, task.id)
}

async function waitForAccountTestTask(baseUrl: string, cookie: string, taskId: string): Promise<AccountTestTask<AccountTestResult>> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 30_000) {
    const tasks = await getEnvelope<Array<AccountTestTask<AccountTestResult>>>(
      baseUrl,
      `/__aisys__/api/my-accounts/test-tasks?ids=${encodeURIComponent(taskId)}`,
      cookie
    )
    const task = tasks.find((item) => item.id === taskId)
    assert(task, `草稿测试任务 ${taskId} 应可查询`)
    if (task.status === 'success' || task.status === 'failed') {
      assert(task.result, `草稿测试任务 ${taskId} 已结束但没有结果`)
      return task
    }
    if (task.status === 'canceled') {
      throw new Error(`草稿测试任务 ${taskId} 已取消：${task.message ?? ''}`)
    }
    await sleep(100)
  }
  throw new Error(`草稿测试任务 ${taskId} 等待超时`)
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data as T
}

function responseMessage(body: ApiEnvelope<unknown>): string {
  return typeof body.message === 'string' ? body.message : ''
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
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

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url?.split('?', 1)[0] !== '/v1/responses') {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }

    let requestBody = ''
    req.setEncoding('utf8')
    req.on('data', (chunk) => {
      requestBody += chunk
    })
    req.on('end', () => {
      const authorization = typeof req.headers.authorization === 'string' ? req.headers.authorization : undefined
      const payload = parseJsonObject(requestBody)
      mockOpenAIRequests.push({ authorization, body: payload })
      if (authorization?.includes('sk-real-fail-draft-test')) {
        res.writeHead(401, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { code: 'invalid_api_key', message: 'mock invalid api key' } }))
        return
      }
      if (authorization?.includes('sk-edit-saved-should-not-be-used')) {
        res.writeHead(401, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { code: 'saved_api_key_used', message: 'saved api key should not be used' } }))
        return
      }
      const completedEvent = {
        type: 'response.completed',
        response: {
          id: 'resp_pending_test_mock',
          object: 'response',
          status: 'completed',
          output: [
            {
              type: 'message',
              content: [{ type: 'output_text', text: 'OK' }]
            }
          ],
          usage: {
            input_tokens: 1,
            output_tokens: 1,
            total_tokens: 2
          }
        }
      }
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
    })
  })
}

function parseJsonObject(requestBody: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(requestBody) as unknown
    if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
  }
  return {}
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

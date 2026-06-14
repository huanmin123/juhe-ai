import { Router } from 'express'

import { isAdminRole, type AccountStatus, type AccountSummary } from '../../domain/types.js'
import { isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import { badRequest, ok } from '../../shared/http.js'
import { ProxyProfileUnavailableError, accountTestUnavailableMessage, clearAccountFailureState, createAccount, deleteAccountWithRelatedCleanup, findAccountForTest, findAccountSummary, findGroupSummary, listProviders, migrateAccountTraffic, returnAccountAuthorizationInstanceForGrantee, setAccountGroup, updateAccount, updateAccountTags, updateAuthorizedAccountBindingDispatch } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { applyServerAccountRuntimeToAccount } from '../gateway/runtime/runtime-snapshot.service.js'
import { migrateOpenAIAccountSessionAffinity } from '../gateway/runtime/session-affinity.service.js'
import { diffSafeFields, operationMode, ownerTarget, recordOperationLog, resolveOperationOwner, runLoggedOperation, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'
import {
  createAccountTestTask,
  failAccountTestTask,
} from '../../storage/account-test-tasks.repository.js'
import {
  accountCreateStatusFromActivationTest,
  prepareAccountDraftTestSnapshot,
  savedAccountDraftTestSnapshot
} from './account-draft-test.service.js'
import { accountImportMaxAccounts } from './account-import.service.js'
import { accountErrorPolicyValidationMessage, validateAccountCredentialsErrorHandlingRules } from './account-error-policy-validation.js'
import {
  accountCreateSchema,
  accountDraftTestSchema,
  accountGroupSchema,
  accountTagsUpdateSchema,
  accountTestSchema,
  accountTrafficMigrationSchema,
  accountUpdateSchema,
  authorizedAccountDispatchSchema
} from './account-request.schemas.js'
import { accountResponseInspectionPolicyValidationMessage, validateAccountCredentialsResponseInspectionRules } from './account-response-inspection-policy-validation.js'
import { sanitizeAccountResponse, sanitizeAccountTrafficMigrationResponse } from './account-response-sanitizer.js'
import { dispatchAccountTestTasks } from './account-test-task-queue.service.js'
import { accountCredentialFingerprint, credentialsRecordValue, mergeAccountCredentialsForUpdate } from './account-credential-update.js'
import { accountExportRequestSchema, exportAccountsForRequest } from './account-export-request.js'
import { registerAccountTestSessionRoutes } from './account-test-session.routes.js'
import { registerAccountTestStatusRoutes } from './account-test-status.routes.js'
import { registerAccountListRoutes } from './account-list.routes.js'
import { registerAccountImportRoutes } from './account-import.routes.js'
import { registerAccountTagsRoutes } from './account-tags.routes.js'

export const accountsRouter = Router()

registerAccountListRoutes(accountsRouter)
registerAccountTagsRoutes(accountsRouter)

accountsRouter.post('/export', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const parsed = accountExportRequestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(`账户导出参数无效，单次最多导出 ${accountImportMaxAccounts} 个账户`))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  if (!requestAccess) {
    res.status(401).json(badRequest('缺少系统账户上下文'))
    return
  }
  try {
    const result = exportAccountsForRequest(parsed.data, requestAccess)
    const ownerSystemAccountId = resolveOperationOwner(undefined, requestAccess)
    const matchedText = typeof result.summary.matchedAccounts === 'number' ? `，匹配 ${result.summary.matchedAccounts} 条` : ''
    const truncatedText = result.summary.truncated ? `，仅处理前 ${accountImportMaxAccounts} 条` : ''
    recordOperationLog({
      operationScopeSystemAccountId: ownerSystemAccountId,
      mode: operationMode(requestAccess),
      module: 'accounts',
      action: 'export',
      operationKey: 'accounts.export',
      resourceType: 'account',
      resourceName: 'AI 账户导出',
      summary: `导出 AI 账户：${result.summary.accounts} 个账户，${result.summary.proxies} 个代理${matchedText}${truncatedText}`,
      visibilityScope: isAdminRole(requestAccess.role) ? 'admin_only' : 'targeted',
      changes: [
        safeChange('accountExported', '导出账户数', undefined, result.summary.accounts),
        safeChange('proxyExported', '导出代理数', undefined, result.summary.proxies),
        safeChange('accountSkipped', '跳过账户数', undefined, result.summary.skippedAccounts),
        ...(typeof result.summary.matchedAccounts === 'number'
          ? [safeChange('accountMatched', '匹配账户数', undefined, result.summary.matchedAccounts)]
          : []),
        ...(result.summary.truncated
          ? [safeChange('accountExportTruncated', '导出结果截断', false, true)]
          : [])
      ],
      ...(!isAdminRole(requestAccess.role) ? { viewers: viewer(ownerSystemAccountId, 'resource_owner') } : {})
    }, req)
    res.json(ok(result))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '导出账户失败'))
  }
})

registerAccountTestSessionRoutes(accountsRouter)

accountsRouter.post('/test-draft', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  if (!requestAccess) {
    res.status(403).json({ message: '缺少系统账户上下文' })
    return
  }
  const parsed = accountDraftTestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '账户草稿测试参数无效'))
    return
  }
  try {
    const preparedDraft = prepareAccountDraftTestSnapshot({
      accountInput: parsed.data.account,
      requestAccess
    })
    const { prompt: _ignoredPrompt, testSessionId, ...testOptions } = parsed.data
    const task = createAccountTestTask({
      account: preparedDraft.account,
      access: requestAccess,
      diagnostics: 'full',
      sessionId: testSessionId,
      model: testOptions.model,
      clientCompatibility: testOptions.clientCompatibility,
      draftAccount: preparedDraft.draftAccount
    })
    if (!dispatchAccountTestTasks([task.id])) {
      failAccountTestTask(task.id, '后台 worker 暂不可用，账号草稿测试任务未能投递')
      res.status(503).json({ message: '后台 worker 暂不可用，账号草稿测试任务未能投递' })
      return
    }
    res.status(202).json(ok(task))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建账户草稿测试任务失败'))
  }
})

registerAccountTestStatusRoutes(accountsRouter)
registerAccountImportRoutes(accountsRouter)

accountsRouter.get('/:id', async (req, res, next) => {
  try {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const visibleAccount = findAccountSummary(req.params.id, requestAccess)
    if (!visibleAccount) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    if (visibleAccount.accessType === 'authorized') {
      const hydratedAccount = await applyServerAccountRuntimeToAccount(visibleAccount)
      res.json(ok(sanitizeAccountResponse(hydratedAccount)))
      return
    }
    if (visibleAccount.permissions?.canViewCredentials === false || visibleAccount.permissions?.canEdit === false) {
      res.status(403).json({ message: '无权查看账户凭据' })
      return
    }
    const account = findAccountForTest(req.params.id, requestAccess)
    if (!account) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    const hydratedAccount = await applyServerAccountRuntimeToAccount(account)
    res.json(ok(hydratedAccount))
  } catch (error) {
    next(error)
  }
})

accountsRouter.post('/', mutationGuard({
  operationKey: 'accounts.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    type: normalizedText(bodyField(req, 'type')),
    name: normalizedText(bodyField(req, 'name')),
    credential: accountCredentialFingerprint(bodyField(req, 'credentials')),
    status: normalizedText(bodyField(req, 'status')),
    activationTestTaskId: normalizedText(bodyField(req, 'activationTestTaskId'))
  })
}), (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountCreateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('账户参数无效'))
    return
  }
  const errorPolicyValidationMessage = accountErrorPolicyValidationMessage(validateAccountCredentialsErrorHandlingRules(parsed.data.credentials))
  if (errorPolicyValidationMessage) {
    res.status(400).json(badRequest(errorPolicyValidationMessage))
    return
  }
  const responseInspectionValidationMessage = accountResponseInspectionPolicyValidationMessage(validateAccountCredentialsResponseInspectionRules(parsed.data.credentials))
  if (responseInspectionValidationMessage) {
    res.status(400).json(badRequest(responseInspectionValidationMessage))
    return
  }

  const providerCode = parsed.data.providerCode
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    res.status(400).json(badRequest(`不支持的供应商：${providerCode}`))
    return
  }
  if (!provider.enabled) {
    res.status(400).json(badRequest(`供应商已停用：${providerCode}`))
    return
  }
  const groupId = typeof parsed.data.groupId === 'string' && parsed.data.groupId ? parsed.data.groupId : undefined
  let group: ReturnType<typeof findGroupSummary> | undefined
  if (groupId) {
    group = findGroupSummary(groupId, requestAccess)
    if (!group || group.providerCode !== providerCode) {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
  }
  const providerProtocolProfileId = parsed.data.providerProtocolProfileId ?? group?.providerProtocolProfileId ?? provider.defaultProtocolProfileId
  const providerProfile = provider.protocolProfiles.find((item) => item.id === providerProtocolProfileId)
  if (!providerProfile || !providerProfile.accountTypes.includes(parsed.data.type)) {
    res.status(400).json(badRequest(`供应商协议档案不支持账户类型：${parsed.data.type}`))
    return
  }
  if (group && group.providerProtocolProfileId !== providerProfile.id) {
    res.status(400).json(badRequest('账户分组协议档案无效'))
    return
  }

  let createStatus: AccountStatus
  try {
    createStatus = accountCreateStatusFromActivationTest({
      account: { ...parsed.data, providerProtocolProfileId: providerProfile.id },
      providerBaseUrl: providerProfile.baseUrl,
      providerProtocolProfileId: providerProfile.id,
      protocolCode: providerProfile.protocolCode,
      protocolVersion: providerProfile.protocolVersion,
      group,
      requestAccess
    })
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '账户创建测试状态无效'))
    return
  }

  try {
    const account = runLoggedOperation(() => {
      const { activationTestTaskId: _activationTestTaskId, ...accountCreateInput } = parsed.data
      const account = createAccount({
        ...accountCreateInput,
        providerCode,
        providerProtocolProfileId: providerProfile.id,
        status: createStatus
      }, requestAccess)
      const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'create',
          operationKey: 'accounts.create',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: `创建 AI 账户：${account.name}`,
          changes: [
            safeChange('name', '名称', undefined, account.name),
            safeChange('providerCode', '供应商', undefined, account.providerCode),
            safeChange('providerProtocolProfileId', '协议档案', undefined, account.providerProtocolProfileId),
            safeChange('type', '账户类型', undefined, account.type),
            safeChange('status', '状态', undefined, account.status),
            safeChange('clientCompatibility', '客户端兼容', undefined, account.clientCompatibility),
            safeChange('credentials', '凭据', undefined, parsed.data.credentials),
            safeChange('supportedModels', '支持模型', undefined, account.supportedModels),
            safeChange('modelMappings', '模型映射', undefined, account.modelMappings),
            safeChange('tags', '标签', undefined, account.tags),
            safeChange('groupId', '绑定分组', undefined, account.boundGroupId),
            safeChange('proxyProfileId', '代理', undefined, account.proxyProfileId),
            safeChange('accountExpiresAt', '过期时间', undefined, account.accountExpiresAt),
            safeChange('availabilitySchedule', '时间计划', undefined, account.availabilitySchedule),
            safeChange('notes', '备注', undefined, account.notes)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.status(201).json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    const message = error instanceof Error ? error.message : '账户参数无效'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

accountsRouter.post('/:id/group', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountGroupSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('绑定分组参数无效'))
    return
  }

  const before = findAccountForTest(req.params.id, requestAccess)
  try {
    const account = runLoggedOperation(() => {
      const account = setAccountGroup(req.params.id, parsed.data.groupId, requestAccess)
      if (!account) {
        throw new Error('账户不存在、授权已失效或分组不可用')
      }
      const ownerSystemAccountId = authorizedLocalOperationOwner(account, requestAccess)
        ?? resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'bind_group',
          operationKey: 'accounts.bind_group',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: `绑定账户分组：${account.name}`,
          changes: [
            safeChange('groupId', '绑定分组', before?.boundGroupId, account.boundGroupId)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '绑定账户分组失败'))
  }
})

accountsRouter.post('/:id/traffic-migration', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountTrafficMigrationSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('迁移流量参数无效'))
    return
  }

  try {
    let affinityResult = { migratedSessionCount: 0 }
    const migration = runLoggedOperation(() => {
      const migration = migrateAccountTraffic({
        sourceAccountId: req.params.id,
        targetAccountId: parsed.data.targetAccountId,
        sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
      }, requestAccess)
      if (!migration) {
        throw new Error('账户不存在或无权迁移')
      }
      const ownerSystemAccountId = authorizedLocalOperationOwner(migration.sourceAccount, requestAccess)
        ?? resolveOperationOwner(migration.sourceAccount as unknown as Record<string, unknown>, requestAccess)
      return {
        result: migration,
        afterCommit: () => {
          const affinityScope = authorizedMigrationAffinityScope(migration.sourceAccount, requestAccess)
          affinityResult = migrateOpenAIAccountSessionAffinity(req.params.id, parsed.data.targetAccountId, affinityScope)
        },
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'traffic_migration',
          operationKey: 'accounts.traffic_migration',
          resourceType: 'account',
          resourceId: migration.sourceAccount.id,
          resourceName: migration.sourceAccount.name,
          summary: `迁移账户流量：${migration.sourceAccount.name} -> ${migration.targetAccount.name}`,
          changes: [
            safeChange('targetAccountId', '目标账户', undefined, migration.targetAccount.name),
            safeChange('sourceStatus', '源账户状态', undefined, parsed.data.sourceStatus ?? 'temporary_unavailable')
          ],
          targets: [
            {
              targetType: 'account',
              targetId: migration.targetAccount.id,
              targetName: migration.targetAccount.name,
              targetOwnerSystemAccountId: resolveOperationOwner(migration.targetAccount as unknown as Record<string, unknown>, requestAccess),
              relation: 'affected'
            }
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(sanitizeAccountTrafficMigrationResponse({
      ...migration,
      ...affinityResult,
      sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
    })))
  } catch (error) {
    if (error instanceof Error && error.message === '账户不存在或无权迁移') {
      res.status(404).json({ message: '账户不存在或无权迁移' })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '迁移流量失败'))
  }
})

function authorizedLocalOperationOwner(account: AccountSummary, access?: RequestAccessScope): string | undefined {
  return account.accessType === 'authorized' ? effectiveRequestSystemAccountId(access) : undefined
}

async function clearAccountGatewayRuntimeAfterRestore(account: AccountSummary, access?: RequestAccessScope): Promise<void> {
  const systemAccountId = account.accessType === 'authorized'
    ? account.bindingSystemAccountId ?? effectiveRequestSystemAccountId(access)
    : undefined
  await clearServerAccountRuntimeAvailability({
    accountId: account.id,
    authorizedBinding: account.accessType === 'authorized' && systemAccountId && account.boundGroupId && account.accountAuthorizationId
      ? {
          systemAccountId,
          groupId: account.boundGroupId,
          accountAuthorizationId: account.accountAuthorizationId
        }
      : undefined
  }).catch(() => undefined)
}

function authorizedMigrationAffinityScope(account: AccountSummary, access?: RequestAccessScope): { systemAccountId: string; groupId: string } | undefined {
  const systemAccountId = effectiveRequestSystemAccountId(access)
  return account.accessType === 'authorized' && account.boundGroupId && systemAccountId
    ? { systemAccountId, groupId: account.boundGroupId }
    : undefined
}

function effectiveRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}

accountsRouter.patch('/:id/authorized-dispatch', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = authorizedAccountDispatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('授权账户调度参数无效'))
    return
  }
  try {
    const account = runLoggedOperation(() => {
      const account = updateAuthorizedAccountBindingDispatch(req.params.id, parsed.data, requestAccess)
      if (!account) {
        throw new Error('授权账户不存在或尚未绑定分组')
      }
      const ownerSystemAccountId = effectiveRequestSystemAccountId(requestAccess)
      return {
        result: account,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'authorized_dispatch',
          operationKey: 'accounts.authorized_dispatch',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: `调整授权账户使用设置：${account.name}`,
          changes: [
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'status') ? [safeChange('status', '实例状态', undefined, parsed.data.status)] : []),
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'priority') ? [safeChange('priority', '分组内优先级', undefined, parsed.data.priority)] : []),
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'superPriorityEnabled') ? [safeChange('superPriorityEnabled', '分组内超级优先', undefined, parsed.data.superPriorityEnabled)] : []),
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'fallbackEnabled') ? [safeChange('fallbackEnabled', '分组内降级备用', undefined, parsed.data.fallbackEnabled)] : []),
            ...(parsed.data.clearFailureState === true ? [safeChange('clearFailureState', '恢复实例异常状态', false, true)] : [])
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    if (!account) {
      res.status(404).json({ message: '授权账户不存在或尚未绑定分组' })
      return
    }
    if (parsed.data.clearFailureState === true || parsed.data.status === 'active') {
      await clearAccountGatewayRuntimeAfterRestore(account, requestAccess)
    }
    res.json(ok(sanitizeAccountResponse(await applyServerAccountRuntimeToAccount(account))))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新授权账户调度设置失败'))
  }
})

accountsRouter.patch('/:id/tags', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountTagsUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '账户标签参数无效'))
    return
  }
  const before = findAccountSummary(req.params.id, requestAccess)
  if (!before) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  try {
    const account = runLoggedOperation(() => {
      const tags = updateAccountTags(req.params.id, parsed.data.tags, requestAccess)
      if (!tags) {
        throw new Error('账户不存在')
      }
      const account = findAccountSummary(req.params.id, requestAccess)
      if (!account) {
        throw new Error('账户不存在')
      }
      const ownerSystemAccountId = authorizedLocalOperationOwner(account, requestAccess)
        ?? resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: { ...account, tags },
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'update_tags',
          operationKey: 'accounts.update_tags',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: `更新账户标签：${account.name}`,
          changes: [
            safeChange('tags', '标签', before?.tags, tags)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
    if (error instanceof Error && error.message === '账户不存在') {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新账户标签失败'))
  }
})

accountsRouter.patch('/:id', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '账户更新参数无效'))
    return
  }
  const body = parsed.data as Record<string, unknown>
  const { groupId: requestedGroupId, clearFailureState: requestedClearFailureState, ...accountUpdateInput } = parsed.data
  const existingAccount = findAccountForTest(req.params.id, requestAccess)
  if (!existingAccount) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  if (requestedClearFailureState === true && existingAccount.status === 'pending_test') {
    res.status(400).json(badRequest('待测试账户需手动测试通过后才能参与调度'))
    return
  }
  const hasGroupId = Object.prototype.hasOwnProperty.call(body, 'groupId')
  const groupIdToBind = typeof requestedGroupId === 'string' ? requestedGroupId : undefined
  if (hasGroupId && !groupIdToBind) {
    res.status(400).json(badRequest('账户分组不能为空'))
    return
  }
  if (hasGroupId) {
    const group = findGroupSummary(groupIdToBind as string, requestAccess)
    if (!group || group.providerCode !== existingAccount.providerCode) {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
  }
  const errorPolicyValidationMessage = accountErrorPolicyValidationMessage(validateAccountCredentialsErrorHandlingRules(body.credentials))
  if (errorPolicyValidationMessage) {
    res.status(400).json(badRequest(errorPolicyValidationMessage))
    return
  }
  const responseInspectionValidationMessage = accountResponseInspectionPolicyValidationMessage(validateAccountCredentialsResponseInspectionRules(body.credentials))
  if (responseInspectionValidationMessage) {
    res.status(400).json(badRequest(responseInspectionValidationMessage))
    return
  }
  const requestedCredentials = credentialsRecordValue(body.credentials)
  if (Object.prototype.hasOwnProperty.call(body, 'credentials') && requestedCredentials) {
    accountUpdateInput.credentials = mergeAccountCredentialsForUpdate(existingAccount, requestedCredentials)
  }
  try {
    const account = runLoggedOperation(() => {
      if (requestedClearFailureState === true) {
        const restoredAccount = clearAccountFailureState(req.params.id, requestAccess)
        if (!restoredAccount) {
          throw new Error('账户不存在')
        }
      }
      let account = updateAccount(req.params.id, accountUpdateInput, requestAccess)
      if (!account) {
        throw new Error('账户不存在')
      }
      if (hasGroupId) {
        const nextAccount = setAccountGroup(account.id, groupIdToBind as string, requestAccess)
        if (!nextAccount) {
          throw new Error('账户分组无效')
        }
        account = nextAccount
      }
      const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: requestedClearFailureState === true ? 'restore' : 'update',
          operationKey: requestedClearFailureState === true ? 'accounts.restore' : 'accounts.update',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: requestedClearFailureState === true ? `恢复 AI 账户：${account.name}` : `更新 AI 账户：${account.name}`,
          changes: [
            ...diffSafeFields(existingAccount as unknown as Record<string, unknown>, account as unknown as Record<string, unknown>, {
              name: '名称',
              notes: '备注',
              credentials: '凭据',
              status: '状态',
              concurrencyLimit: '并发限制',
              priority: '优先级',
              superPriorityEnabled: '超级优先',
              fallbackEnabled: '降级备用',
              clientCompatibility: '客户端兼容',
              supportedModels: '支持模型',
              modelMappings: '模型映射',
              tags: '标签',
              proxyProfileId: '代理',
              schedulable: '参与调度',
              accountExpiresAt: '过期时间',
              availabilitySchedule: '时间计划',
              boundGroupId: '绑定分组',
              cooldownUntil: '冷却结束时间',
              lastErrorCode: '异常类型',
              lastErrorMessage: '错误信息'
            }),
            ...(requestedClearFailureState === true ? [safeChange('clearFailureState', '恢复异常状态', false, true)] : [])
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    if (requestedClearFailureState === true || body.status === 'active') {
      await clearAccountGatewayRuntimeAfterRestore(account, requestAccess)
    }
    res.json(ok(sanitizeAccountResponse(await applyServerAccountRuntimeToAccount(account))))
  } catch (error) {
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    if (error instanceof Error && error.message === '账户分组无效') {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
    if (error instanceof Error && error.message === '账户不存在') {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    const message = error instanceof Error ? error.message : '更新账户失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

accountsRouter.post('/:id/test', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  if (!requestAccess) {
    res.status(403).json({ message: '缺少系统账户上下文' })
    return
  }
  const parsed = accountTestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('账户测试参数无效'))
    return
  }
  const account = findAccountForTest(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  if (!isOpenAIProtocolProfile(account)) {
    res.status(400).json({ message: '当前仅支持测试 OpenAI 协议账户' })
    return
  }
  const unavailableMessage = accountTestUnavailableMessage(account)
  if (unavailableMessage) {
    res.status(400).json({ message: unavailableMessage })
    return
  }

  try {
    const diagnostics = isAdminRole(requestAccess?.role) || account.accessType !== 'authorized' ? 'full' : 'limited'
    const testRequest = parsed.data ?? {}
    const { prompt: _ignoredPrompt, account: accountSnapshot, testSessionId, ...testOptions } = testRequest
    const draftAccount = accountSnapshot
      ? savedAccountDraftTestSnapshot(account, accountSnapshot, requestAccess)
      : undefined
    const task = createAccountTestTask({
      account,
      access: requestAccess,
      diagnostics,
      sessionId: testSessionId,
      model: testOptions.model,
      clientCompatibility: testOptions.clientCompatibility ?? draftAccount?.clientCompatibility,
      draftAccount
    })
    if (!dispatchAccountTestTasks([task.id])) {
      failAccountTestTask(task.id, '后台 worker 暂不可用，账号测试任务未能投递')
      res.status(503).json({ message: '后台 worker 暂不可用，账号测试任务未能投递' })
      return
    }
    res.status(202).json(ok(task))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建账户测试任务失败'))
  }
})

accountsRouter.post('/:id/return-authorization', mutationGuard({
  operationKey: 'accounts.return_authorization',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    accountId: normalizedText(req.params.id),
    grantee: normalizedText(queryField(req, 'systemAccountId'))
  })
}), (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const before = findAccountSummary(req.params.id, requestAccess)
  try {
    runLoggedOperation(() => {
      const authorization = returnAccountAuthorizationInstanceForGrantee(req.params.id, requestAccess)
      if (!authorization) {
        throw new Error('授权账户不存在或不可归还')
      }
      const resourceName = before?.name ?? authorization.resource_id
      return {
        result: true,
        log: {
          operationScopeSystemAccountId: authorization.grantee_system_account_id,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'return',
          operationKey: 'accounts.return_authorization',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName,
          summary: `归还授权账户：${resourceName}`,
          changes: [safeChange('returned', '归还授权账户', false, true)],
          targets: [
            ownerTarget({
              targetType: authorization.resource_type,
              targetId: authorization.resource_id,
              ownerSystemAccountId: authorization.resource_owner_system_account_id,
              relation: 'owner'
            }),
            ownerTarget({
              targetType: 'system_account',
              targetId: authorization.grantee_system_account_id,
              ownerSystemAccountId: authorization.grantee_system_account_id,
              relation: 'grantee'
            })
          ],
          viewers: viewers(
            viewer(authorization.resource_owner_system_account_id, 'authorization_owner'),
            viewer(authorization.grantee_system_account_id, 'authorization_grantee')
          )
        }
      }
    }, req)
    res.status(204).send()
  } catch (error) {
    if (error instanceof Error && error.message === '授权账户不存在或不可归还') {
      res.status(404).json({ message: '授权账户不存在或不可归还' })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '归还授权账户失败'))
  }
})

accountsRouter.delete('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const before = findAccountSummary(req.params.id, requestAccess)
  const ownerSystemAccountId = resolveOperationOwner(before as unknown as Record<string, unknown> | undefined, requestAccess)
  try {
    runLoggedOperation(() => {
      const deleteResult = deleteAccountWithRelatedCleanup(req.params.id, requestAccess)
      if (!deleteResult.deleted) {
        throw new Error('账户不存在')
      }
      return {
        result: true,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'delete',
          operationKey: 'accounts.delete',
          resourceType: 'account',
          resourceId: req.params.id,
          resourceName: before?.name ?? req.params.id,
          summary: `删除 AI 账户：${before?.name ?? req.params.id}`,
          changes: [safeChange('deleted', '删除状态', false, true)],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
  } catch (error) {
    if (error instanceof Error && error.message === '账户不存在') {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    if (error instanceof Error && error.message === '授权账户请使用归还操作') {
      res.status(400).json(badRequest('授权账户请使用归还操作'))
      return
    }
    throw error
  }
  res.status(204).send()
})

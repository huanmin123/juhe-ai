import { Router } from 'express'

import { type AccountStatus, type AccountSummary } from '../../domain/types.js'
import { resolveProviderProtocolProfileIdFromConnectionType } from '../../domain/provider-connection-type.js'
import { badRequest, ok } from '../../shared/http.js'
import { ProxyProfileUnavailableError, clearAccountFailureStateAsync, createAccountAsync, findAccountForTestAsync, findGroupSummaryAsync, listProvidersAsync, setAccountGroupAsync, updateAccountAsync } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { applyServerAccountRuntimeToAccount } from '../gateway/runtime/runtime-snapshot.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import {
  createAccountTestTask,
  failAccountTestTask,
} from '../../storage/account-test-tasks.repository.js'
import {
  accountCreateStatusFromActivationTest,
  prepareAccountDraftTestSnapshot
} from './account-draft-test.service.js'
import { accountErrorPolicyValidationMessage, validateAccountCredentialsErrorHandlingRules } from './account-error-policy-validation.js'
import {
  accountCreateSchema,
  accountDraftTestSchema,
  accountUpdateSchema
} from './account-request.schemas.js'
import { accountResponseInspectionPolicyValidationMessage, validateAccountCredentialsResponseInspectionRules } from './account-response-inspection-policy-validation.js'
import { sanitizeAccountResponse } from './account-response-sanitizer.js'
import { dispatchAccountTestTasks } from './account-test-task-queue.service.js'
import { accountCredentialFingerprint, credentialsRecordValue, mergeAccountCredentialsForUpdate } from './account-credential-update.js'
import { registerAccountExportRoutes } from './account-export.routes.js'
import { registerAccountTestSessionRoutes } from './account-test-session.routes.js'
import { registerAccountTestStatusRoutes } from './account-test-status.routes.js'
import { registerAccountListRoutes } from './account-list.routes.js'
import { registerAccountImportRoutes } from './account-import.routes.js'
import { registerAccountTagsRoutes } from './account-tags.routes.js'
import { registerAccountAuthorizationReturnRoutes } from './account-authorization-return.routes.js'
import { registerAccountTrafficMigrationRoutes } from './account-traffic-migration.routes.js'
import { registerAccountAuthorizedDispatchRoutes } from './account-authorized-dispatch.routes.js'
import { registerAccountGroupBindingRoutes } from './account-group-binding.routes.js'
import { registerAccountDeleteRoutes } from './account-delete.routes.js'
import { registerAccountDetailRoutes } from './account-detail.routes.js'
import { registerAccountTestDispatchRoutes } from './account-test-dispatch.routes.js'

export const accountsRouter = Router()

registerAccountListRoutes(accountsRouter)
registerAccountTagsRoutes(accountsRouter)
registerAccountExportRoutes(accountsRouter)

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
registerAccountTrafficMigrationRoutes(accountsRouter)
registerAccountGroupBindingRoutes(accountsRouter)

registerAccountDetailRoutes(accountsRouter)

accountsRouter.post('/', mutationGuard({
  operationKey: 'accounts.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    connectionType: normalizedText(bodyField(req, 'connectionType')),
    type: normalizedText(bodyField(req, 'type')),
    name: normalizedText(bodyField(req, 'name')),
    credential: accountCredentialFingerprint(bodyField(req, 'credentials')),
    status: normalizedText(bodyField(req, 'status')),
    activationTestTaskId: normalizedText(bodyField(req, 'activationTestTaskId'))
  })
}), async (req, res) => {
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
  const provider = (await listProvidersAsync()).find((item) => item.code === providerCode)
  if (!provider) {
    res.status(400).json(badRequest(`不支持的供应商：${providerCode}`))
    return
  }
  if (!provider.enabled) {
    res.status(400).json(badRequest(`供应商已停用：${providerCode}`))
    return
  }
  const groupId = typeof parsed.data.groupId === 'string' && parsed.data.groupId ? parsed.data.groupId : undefined
  let group: Awaited<ReturnType<typeof findGroupSummaryAsync>> | undefined
  if (groupId) {
    group = await findGroupSummaryAsync(groupId, requestAccess)
    if (!group || group.providerCode !== providerCode) {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
  }
  let providerProtocolProfileId: string | undefined
  try {
    providerProtocolProfileId = resolveProviderProtocolProfileIdFromConnectionType({
      providerCode,
      providerProtocolProfileId: parsed.data.providerProtocolProfileId,
      connectionType: parsed.data.connectionType
    }) ?? group?.providerProtocolProfileId ?? provider.defaultProtocolProfileId
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '账户接入类型无效'))
    return
  }
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
    const account = await runLoggedOperationAsync(async () => {
      const { activationTestTaskId: _activationTestTaskId, connectionType: _connectionType, ...accountCreateInput } = parsed.data
      const account = await createAccountAsync({
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
            safeChange('availabilityScheduleActive', '时间计划派生状态', undefined, account.availabilityScheduleActive),
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

function effectiveRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}

registerAccountAuthorizedDispatchRoutes(accountsRouter)

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
  const existingAccount = await findAccountForTestAsync(req.params.id, requestAccess)
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
  if (hasGroupId && existingAccount.boundGroupId !== groupIdToBind) {
    const group = await findGroupSummaryAsync(groupIdToBind as string, requestAccess)
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
  const hasAccountUpdateInput = Object.keys(accountUpdateInput).length > 0
  const canUseExistingWithoutAccountUpdate = existingAccount.accessType !== 'authorized' && !existingAccount.accountAuthorizationId
  try {
    const account = await runLoggedOperationAsync(async () => {
      let account: AccountSummary | undefined
      if (requestedClearFailureState === true) {
        const restoredAccount = await clearAccountFailureStateAsync(req.params.id, requestAccess)
        if (!restoredAccount) {
          throw new Error('账户不存在')
        }
        account = restoredAccount
      }
      if (hasAccountUpdateInput || !canUseExistingWithoutAccountUpdate) {
        account = await updateAccountAsync(req.params.id, accountUpdateInput, requestAccess)
        if (!account) {
          throw new Error('账户不存在')
        }
      } else if (!account) {
        account = existingAccount
      }
      if (hasGroupId && account.boundGroupId !== groupIdToBind) {
        const nextAccount = await setAccountGroupAsync(account.id, groupIdToBind as string, requestAccess)
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
              availabilityScheduleActive: '时间计划派生状态',
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

registerAccountTestDispatchRoutes(accountsRouter)

registerAccountAuthorizationReturnRoutes(accountsRouter)
registerAccountDeleteRoutes(accountsRouter)

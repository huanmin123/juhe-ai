import { Router } from 'express'
import { isDeepStrictEqual } from 'node:util'

import { type AccountSummary } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { ProxyProfileUnavailableError, clearAccountFailureStateAsync, createAccountAsync, findAccountForTestAsync, findGroupSummaryAsync, listProvidersAsync, setAccountGroupAsync, updateAccountAsync } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { applyServerAccountRuntimeToAccount } from '../gateway/runtime/runtime-snapshot.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import {
  createAccountTestTaskAsync,
  failAccountTestTaskAsync
} from '../../storage/account-test-tasks.repository.js'
import { prepareAccountDraftTestSnapshotAsync } from './account-draft-test.service.js'
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
import { accountBalanceQueryIdentity, normalizeAccountBalanceConfig, validateAccountBalanceCapability } from './account-balance-config.js'
import {
  loadAccountBalanceConfigurationsByAccountIdsAsync,
} from '../../storage/account-balance.repository.js'
import { registerAccountExportRoutes } from './account-export.routes.js'
import { registerAccountTestSessionRoutes } from './account-test-session.routes.js'
import { resolveAccountManualTestSelectionAsync } from './account-test-options.service.js'
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
import { registerAccountBatchEditRoutes } from './account-batch-edit.routes.js'
import { registerAccountBalanceRoutes } from './account-balance.routes.js'
import { assertAccountGptRequestOverridesSupportedAsync } from './account-gpt-request-overrides.validation.js'
import {
  accountUpdateNeedsImmediateHealthCheck,
  dispatchAccountHealthCheck,
  dispatchPendingAccountHealthCheck
} from './account-health-check-dispatch.service.js'
import { cleanupAccountBalanceSnapshotAfterSave } from './account-balance-snapshot-cleanup.service.js'
import { registerAccountForceActivateRoutes } from './account-force-activate.routes.js'
import { registerAccountStatusSnapshotRoutes } from './account-status-snapshot.routes.js'

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
    const preparedDraft = await prepareAccountDraftTestSnapshotAsync({
      accountInput: parsed.data.account,
      requestAccess
    })
    const { prompt: _ignoredPrompt, testSessionId, ...testOptions } = parsed.data
    const selection = await resolveAccountManualTestSelectionAsync(
      preparedDraft.account,
      preparedDraft.account.healthCheckModel,
      testOptions.testEndpointMode ?? preparedDraft.account.healthCheckEndpointMode
    )
    const task = await createAccountTestTaskAsync({
      account: preparedDraft.account,
      access: requestAccess,
      diagnostics: 'full',
      sessionId: testSessionId,
      model: selection.model,
      testEndpointMode: selection.testEndpointMode,
      draftAccount: preparedDraft.draftAccount
    })
    if (!dispatchAccountTestTasks([task.id])) {
      await failAccountTestTaskAsync(task.id, '后台 worker 暂不可用，账号草稿测试任务未能投递')
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
registerAccountBatchEditRoutes(accountsRouter)
registerAccountBalanceRoutes(accountsRouter)
registerAccountForceActivateRoutes(accountsRouter)
registerAccountStatusSnapshotRoutes(accountsRouter)

registerAccountDetailRoutes(accountsRouter)

accountsRouter.post('/', mutationGuard({
  operationKey: 'accounts.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    providerProtocolProfileId: normalizedText(bodyField(req, 'providerProtocolProfileId')),
    type: normalizedText(bodyField(req, 'type')),
    name: normalizedText(bodyField(req, 'name')),
    credential: accountCredentialFingerprint(bodyField(req, 'credentials')),
    status: normalizedText(bodyField(req, 'status'))
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
  let balanceQueryEnabled = parsed.data.balanceQueryEnabled ?? false
  const balanceQueryConfig = parsed.data.balanceQueryConfig
    ? normalizeAccountBalanceConfig(parsed.data.balanceQueryConfig)
    : undefined
  try {
    balanceQueryEnabled = validateAccountBalanceCapability(
      { type: parsed.data.type, credentials: parsed.data.credentials },
      balanceQueryEnabled
    ).enabled
    if (balanceQueryEnabled && !balanceQueryConfig) throw new Error('开启上游余额查询时必须选择查询类型')
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '余额查询配置无效'))
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
  const providerProfile = provider.protocolProfiles.find((item) => item.id === parsed.data.providerProtocolProfileId)
  if (!providerProfile || !providerProfile.accountTypes.includes(parsed.data.type)) {
    res.status(400).json(badRequest(`供应商协议档案不支持账户类型：${parsed.data.type}`))
    return
  }
  try {
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode,
      accountType: parsed.data.type,
      credentials: parsed.data.credentials,
      supportedModels: parsed.data.supportedModels ?? provider.defaultSupportedModels,
      systemAccountId: effectiveRequestSystemAccountId(requestAccess)
    })
    const account = await runLoggedOperationAsync(async () => {
      const account = await createAccountAsync({
        ...parsed.data,
        balanceQueryEnabled,
        balanceQueryConfig,
        providerCode,
        providerProtocolProfileId: providerProfile.id,
        status: parsed.data.status === 'disabled' ? 'disabled' : 'pending_test'
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
            safeChange('serviceTierOverride', '服务等级覆盖', undefined, parsed.data.credentials?.service_tier_override),
            safeChange('reasoningEffortOverride', '思考级别覆盖', undefined, parsed.data.credentials?.reasoning_effort_override),
            safeChange('supportedModels', '支持模型', undefined, account.supportedModels),
            safeChange('healthCheckModel', '检查模型', undefined, account.healthCheckModel),
            safeChange('temporaryUnavailableContinuousProbeEnabled', '持续恢复探活', undefined, account.temporaryUnavailableContinuousProbeEnabled),
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
    dispatchPendingAccountHealthCheck(account)
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

function isApiKeyCredentialChanged(account: AccountSummary, credentials: unknown): boolean {
  if (account.type !== 'api_key') return false
  const requestedCredentials = credentialsRecordValue(credentials)
  if (!requestedCredentials) return false
  return accountCredentialFingerprint(requestedCredentials) !== accountCredentialFingerprint(account.credentials)
}

function isAuthorizedAccountUpdateTarget(account: AccountSummary): boolean {
  return account.accessType === 'authorized' || Boolean(account.accountAuthorizationId || account.authorizationInstanceSourceAccountId)
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
  const {
    groupId: requestedGroupId,
    clearFailureState: requestedClearFailureState,
    balanceQueryEnabled: requestedBalanceQueryEnabled,
    balanceQueryConfig: requestedBalanceQueryConfig,
    ...accountUpdateInput
  } = parsed.data
  const existingAccount = await findAccountForTestAsync(req.params.id, requestAccess)
  if (!existingAccount) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  if (
    requestedClearFailureState === true
    && existingAccount.status === 'pending_test'
    && !isPendingHealthCheckFailure(existingAccount)
  ) {
    res.status(400).json(badRequest('账户正在等待首次后台健康检查，无需重新检查'))
    return
  }
  if (isAuthorizedAccountUpdateTarget(existingAccount) && Object.prototype.hasOwnProperty.call(body, 'concurrencyLimit')) {
    res.status(400).json(badRequest('授权账户并发上限由来源账户控制，不能在被授权账户上修改'))
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
  const canUseExistingWithoutAccountUpdate = existingAccount.accessType !== 'authorized' && !existingAccount.accountAuthorizationId
  try {
    const nextCredentials = credentialsRecordValue(accountUpdateInput.credentials) ?? existingAccount.credentials
    const currentBalance = (await loadAccountBalanceConfigurationsByAccountIdsAsync([existingAccount.id])).get(existingAccount.id)
    const requestedNextBalanceEnabled = requestedBalanceQueryEnabled ?? currentBalance?.enabled ?? false
    const nextBalanceConfig = requestedBalanceQueryConfig
      ? normalizeAccountBalanceConfig(requestedBalanceQueryConfig)
      : currentBalance?.config
    const balanceDecision = validateAccountBalanceCapability({
      type: existingAccount.type,
      credentials: nextCredentials,
      accountAuthorizationId: existingAccount.accountAuthorizationId,
      authorizationInstanceAuthorizationId: existingAccount.authorizationInstanceSourceAccountId,
      accessType: existingAccount.accessType
    }, requestedNextBalanceEnabled)
    const nextBalanceEnabled = balanceDecision.enabled
    if (nextBalanceEnabled && !nextBalanceConfig) throw new Error('开启上游余额查询时必须选择查询类型')
    if (requestedBalanceQueryEnabled !== undefined || requestedBalanceQueryConfig !== undefined || balanceDecision.autoDisabledForMultipleApiKeys) {
      Object.assign(accountUpdateInput, {
        balanceQueryEnabled: nextBalanceEnabled,
        ...(nextBalanceConfig ? { balanceQueryConfig: nextBalanceConfig } : {})
      })
    }
    const hasAccountUpdateInput = Object.keys(accountUpdateInput).length > 0
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode: existingAccount.providerCode,
      accountType: existingAccount.type,
      credentials: nextCredentials,
      supportedModels: Array.isArray(accountUpdateInput.supportedModels)
        ? accountUpdateInput.supportedModels as string[]
        : existingAccount.supportedModels ?? [],
      systemAccountId: existingAccount.ownerSystemAccountId ?? effectiveRequestSystemAccountId(requestAccess)
    })
    const account = await runLoggedOperationAsync(async () => {
      let account: AccountSummary | undefined
      if (requestedClearFailureState === true) {
        const restoredAccount = await clearAccountFailureStateAsync(req.params.id, requestAccess, {
          allowPendingTestRestore: existingAccount.status === 'pending_test',
          allowExplicitPolicyRestore: true
        })
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
      const finalBalance = (await loadAccountBalanceConfigurationsByAccountIdsAsync([account.id])).get(account.id)
      const balanceIdentityChanged = !isDeepStrictEqual(
        accountBalanceQueryIdentity({
          enabled: currentBalance?.enabled === true,
          config: currentBalance?.config,
          providerCode: existingAccount.providerCode,
          accountType: existingAccount.type,
          credentials: existingAccount.credentials,
          proxyProfileId: existingAccount.proxyProfileId
        }),
        accountBalanceQueryIdentity({
          enabled: finalBalance?.enabled === true,
          config: finalBalance?.config,
          providerCode: account.providerCode,
          accountType: account.type,
          credentials: account.credentials,
          proxyProfileId: account.proxyProfileId
        })
      )
      if (balanceIdentityChanged) {
        cleanupAccountBalanceSnapshotAfterSave({
          accountId: account.id,
          configRevision: account.configRevision ?? 1,
          reason: balanceDecision.autoDisabledForMultipleApiKeys
            ? 'multiple_api_keys'
            : 'balance_configuration_changed'
        })
      }
      if (finalBalance) {
        account = {
          ...account,
          balanceQueryEnabled: finalBalance.enabled,
          balanceQueryConfig: finalBalance.config,
          balanceQueryNextRefreshAt: finalBalance.nextRefreshAt
        }
      }
      const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: requestedClearFailureState === true ? 'restore' : 'update',
          operationKey: requestedClearFailureState === true
            ? existingAccount.status === 'pending_test' ? 'accounts.recheck' : 'accounts.restore'
            : 'accounts.update',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: requestedClearFailureState === true
            ? existingAccount.status === 'pending_test' ? `重新检查 AI 账户：${account.name}` : `异常恢复 AI 账户：${account.name}`
            : `更新 AI 账户：${account.name}`,
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
              healthCheckModel: '检查模型',
              healthCheckEndpointMode: '检查协议',
              temporaryUnavailableContinuousProbeEnabled: '持续恢复探活',
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
            safeChange(
              'serviceTierOverride',
              '服务等级覆盖',
              existingAccount.credentials.service_tier_override,
              account.credentials.service_tier_override
            ),
            safeChange(
              'reasoningEffortOverride',
              '思考级别覆盖',
              existingAccount.credentials.reasoning_effort_override,
              account.credentials.reasoning_effort_override
            ),
            ...(requestedClearFailureState === true ? [safeChange(
              'clearFailureState',
              existingAccount.status === 'pending_test' ? '重新检查' : '异常恢复',
              false,
              true
            )] : [])
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    if (requestedClearFailureState === true || body.status === 'active') {
      await clearAccountGatewayRuntimeAfterRestore(account, requestAccess)
    }
    if (requestedClearFailureState === true && account.status === 'pending_test') {
      dispatchAccountHealthCheck(account.id, 'activation')
    } else if (accountUpdateNeedsImmediateHealthCheck(accountUpdateInput)) {
      dispatchAccountHealthCheck(account.id, 'configuration')
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

function isPendingHealthCheckFailure(account: Pick<AccountSummary, 'status' | 'lastHealthCheckAt' | 'lastHealthCheckErrorCode' | 'lastHealthCheckErrorMessage'>): boolean {
  return account.status === 'pending_test'
    && Boolean(account.lastHealthCheckAt)
    && Boolean(account.lastHealthCheckErrorCode || account.lastHealthCheckErrorMessage)
}

registerAccountTestDispatchRoutes(accountsRouter)

registerAccountAuthorizationReturnRoutes(accountsRouter)
registerAccountDeleteRoutes(accountsRouter)

import { Router } from 'express'

import { type AccountSummary } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { ProxyProfileUnavailableError, createAccountAsync } from '../../storage/repositories.js'
import {
  AccountManagementPatchRevisionConflictError,
  patchAccountManagementAsync,
  type AccountManagementPatchResult
} from '../../storage/account-management-patch.repository.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import {
  createAccountTestTaskAsync,
  failAccountTestTaskAsync
} from '../../storage/account-test-tasks.repository.js'
import { prepareAccountDraftTestSnapshotAsync, prepareAccountModelCatalogDiscoverySnapshotAsync } from './account-draft-test.service.js'
import { accountErrorPolicyValidationMessage, validateAccountCredentialsErrorHandlingRules } from './account-error-policy-validation.js'
import {
  accountCreateSchema,
  accountDraftTestSchema,
  accountModelCatalogRefreshSchema,
  accountUpdateSchema
} from './account-request.schemas.js'
import { accountResponseInspectionPolicyValidationMessage, validateAccountCredentialsResponseInspectionRules } from './account-response-inspection-policy-validation.js'
import { sanitizeAccountResponse } from './account-response-sanitizer.js'
import { dispatchAccountTestTasks } from './account-test-task-queue.service.js'
import { accountCredentialFingerprint, credentialsRecordValue } from './account-credential-update.js'
import { normalizeAccountBalanceConfig, validateAccountBalanceCapability } from './account-balance-config.js'
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
  dispatchAccountHealthCheck,
  dispatchInitialAccountHealthCheck
} from './account-health-check-dispatch.service.js'
import { cleanupAccountBalanceSnapshotAfterSave } from './account-balance-snapshot-cleanup.service.js'
import { registerAccountForceActivateRoutes } from './account-force-activate.routes.js'
import { refreshAccountDraftModelCatalogAsync } from './account-model-catalog-refresh.service.js'
import { accountCreationStatusInput } from './account-creation-status.js'

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

accountsRouter.post('/model-catalog/refresh', async (req, res) => {
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
  const parsed = accountModelCatalogRefreshSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '模型目录同步参数无效'))
    return
  }
  const clientAbortController = new AbortController()
  req.once('aborted', () => {
    clientAbortController.abort()
  })
  res.once('close', () => {
    if (!res.writableEnded) clientAbortController.abort()
  })
  try {
    const preparedDraft = await prepareAccountModelCatalogDiscoverySnapshotAsync({
      accountInput: parsed.data.account,
      requestAccess
    })
    if (clientAbortController.signal.aborted) return
    const result = await refreshAccountDraftModelCatalogAsync({
      ...preparedDraft,
      signal: clientAbortController.signal
    })
    if (clientAbortController.signal.aborted || res.writableEnded) return
    res.json(ok(result))
  } catch (error) {
    if (clientAbortController.signal.aborted || res.writableEnded) return
    res.status(400).json(badRequest(error instanceof Error ? error.message : '获取上游模型目录失败'))
  }
})

registerAccountTestStatusRoutes(accountsRouter)
registerAccountImportRoutes(accountsRouter)
registerAccountTrafficMigrationRoutes(accountsRouter)
registerAccountGroupBindingRoutes(accountsRouter)
registerAccountBatchEditRoutes(accountsRouter)
registerAccountBalanceRoutes(accountsRouter)
registerAccountForceActivateRoutes(accountsRouter)
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
    status: accountCreationStatusInput(bodyField(req, 'status')).status
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
    const issue = parsed.error.issues[0]
    const issuePath = issue?.path.length ? `（${issue.path.join('.')}）` : ''
    res.status(400).json(badRequest(`账户参数无效${issuePath}${issue?.message ? `：${issue.message}` : ''}`))
    return
  }
  const creationStatus = accountCreationStatusInput(parsed.data.status)
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
  try {
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode,
      accountType: parsed.data.type,
      credentials: parsed.data.credentials,
      supportedModels: parsed.data.supportedModels ?? [],
      systemAccountId: effectiveRequestSystemAccountId(requestAccess)
    })
    const account = await runLoggedOperationAsync(async () => {
      const account = await createAccountAsync({
        ...parsed.data,
        balanceQueryEnabled,
        balanceQueryConfig,
        providerCode,
        providerProtocolProfileId: parsed.data.providerProtocolProfileId,
        ...creationStatus
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
    dispatchInitialAccountHealthCheck(account)
    res.status(201).json(ok({
      id: account.id,
      status: account.status
    }))
  } catch (error) {
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    const message = error instanceof Error ? error.message : '账户参数无效'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

async function clearAccountGatewayRuntimeAfterRestore(
  account: Pick<AccountManagementPatchResult, 'id' | 'authorizedBinding'>
): Promise<void> {
  await clearServerAccountRuntimeAvailability({
    accountId: account.id,
    authorizedBinding: account.authorizedBinding
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
  try {
    const account = await runLoggedOperationAsync(async () => {
      const patched = await patchAccountManagementAsync(req.params.id, parsed.data, requestAccess)
      if (!patched) throw new Error('账户不存在')
      const restoring = parsed.data.clearFailureState === true
      const ownerSystemAccountId = patched.ownerSystemAccountId
      return {
        result: patched,
        log: patched.changedFields.length > 0 ? {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: restoring ? 'restore' : 'update',
          operationKey: restoring
            ? patched.previousStatus === 'pending_test' ? 'accounts.recheck' : 'accounts.restore'
            : 'accounts.update',
          resourceType: 'account',
          resourceId: patched.id,
          resourceName: patched.name,
          summary: restoring
            ? patched.previousStatus === 'pending_test' ? `重新检查 AI 账户：${patched.name}` : `异常恢复 AI 账户：${patched.name}`
            : `更新 AI 账户：${patched.name}`,
          changes: patched.changes.map((change) => safeChange(
            change.field,
            accountPatchChangeLabel(change.field),
            change.before,
            change.after
          )),
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        } : undefined
      }
    }, req)
    if (account.balanceIdentityChanged) {
      cleanupAccountBalanceSnapshotAfterSave({
        accountId: account.id,
        configRevision: account.configRevision,
        reason: account.balanceAutoDisabledForMultipleApiKeys
          ? 'multiple_api_keys'
          : 'balance_configuration_changed'
      })
    }
    if (account.runtimeRestoreRequired) {
      await clearAccountGatewayRuntimeAfterRestore(account)
    }
    if (account.healthCheckRequired && account.healthCheckReason) {
      dispatchAccountHealthCheck(account.id, account.healthCheckReason)
    }
    res.json(ok({
      id: account.id,
      configRevision: account.configRevision,
      changedFields: account.changedFields,
      ...(account.authorizationInstancesAffected ? { authorizationInstancesAffected: true } : {})
    }))
  } catch (error) {
    if (error instanceof AccountManagementPatchRevisionConflictError) {
      res.status(409).json(badRequest('账户配置已被其他操作更新，请刷新后重试'))
      return
    }
    if (error instanceof ProxyProfileUnavailableError || (error instanceof Error && error.name === 'ProxyProfileUnavailableError')) {
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

function accountPatchChangeLabel(field: string): string {
  const credentialField = field.startsWith('credentials.')
  if (credentialField) return '凭据'
  return ({
    name: '名称',
    notes: '备注',
    credentials: '凭据',
    status: '状态',
    runtimeState: '运行状态',
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
    groupId: '绑定分组',
    balanceQueryEnabled: '余额查询',
    balanceQueryConfig: '余额查询配置',
    clearFailureState: '异常恢复'
  } as Record<string, string>)[field] ?? field
}

registerAccountTestDispatchRoutes(accountsRouter)

registerAccountAuthorizationReturnRoutes(accountsRouter)
registerAccountDeleteRoutes(accountsRouter)

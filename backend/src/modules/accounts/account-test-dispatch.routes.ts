import type { Router } from 'express'

import { isAdminRole } from '../../domain/types.js'
import { isGatewaySupportedProtocolProfile } from '../../domain/provider-protocol.js'
import { badRequest, ok } from '../../shared/http.js'
import { accountTestUnavailableMessage, findAccountForTestAsync } from '../../storage/repositories.js'
import {
  findAccountManualTestCapabilitiesContextAsync,
  findAccountManualTestOptionsContextAsync
} from '../../storage/account-manual-test-context.repository.js'
import {
  createAccountTestTaskAsync,
  failAccountTestTaskAsync,
} from '../../storage/account-test-tasks.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { accountTestSchema } from './account-request.schemas.js'
import { savedAccountDraftTestSnapshotAsync } from './account-draft-test.service.js'
import { dispatchAccountTestTasks } from './account-test-task-queue.service.js'
import {
  accountManualTestCapabilitiesContextFromDraft,
  accountManualTestModelCapabilitiesAsync,
  accountManualTestOptionsAsync,
  normalizeAccountManualTestOptionsQuery,
  resolveAccountManualTestSelectionAsync
} from './account-test-options.service.js'

export function registerAccountTestDispatchRoutes(router: Router): void {
  router.get('/:id/test-options', async (req, res) => {
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
    const account = await findAccountManualTestOptionsContextAsync(req.params.id, requestAccess)
    if (!account) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    try {
      const optionQuery = normalizeAccountManualTestOptionsQuery(req.query as Record<string, unknown>)
      res.json(ok(await accountManualTestOptionsAsync(account, optionQuery)))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '测试模型加载失败'))
    }
  })

  router.get('/:id/test-options/models/:modelId', async (req, res) => {
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
    const account = await findAccountManualTestCapabilitiesContextAsync(req.params.id, req.params.modelId, requestAccess)
    if (!account) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    try {
      res.json(ok(await accountManualTestModelCapabilitiesAsync(account, req.params.modelId)))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '测试模型能力加载失败'))
    }
  })

  router.post('/:id/test', async (req, res) => {
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
    const account = await findAccountForTestAsync(req.params.id, requestAccess)
    if (!account) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    if (!isGatewaySupportedProtocolProfile(account)) {
      res.status(400).json({ message: '当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户' })
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
        ? await savedAccountDraftTestSnapshotAsync(account, accountSnapshot, requestAccess)
        : undefined
      const selection = draftAccount
        ? await resolveAccountManualTestSelectionAsync(
            accountManualTestCapabilitiesContextFromDraft(draftAccount),
            draftAccount.healthCheckModel,
            testOptions.testEndpointMode ?? draftAccount.healthCheckEndpointMode
          )
        : await resolveAccountManualTestSelectionAsync(account, testOptions.model, testOptions.testEndpointMode)
      const task = await createAccountTestTaskAsync({
        account,
        access: requestAccess,
        diagnostics,
        sessionId: testSessionId,
        model: selection.model,
        testEndpointMode: selection.testEndpointMode,
        draftAccount
      })
      if (!(await dispatchAccountTestTasks([task.id]))) {
        await failAccountTestTaskAsync(task.id, '后台 worker 暂不可用，账号测试任务未能投递')
        res.status(503).json({ message: '后台 worker 暂不可用，账号测试任务未能投递' })
        return
      }
      res.status(202).json(ok(task))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '创建账户测试任务失败'))
    }
  })
}

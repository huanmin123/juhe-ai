import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import {
  cancelAccountTestSession,
  cancelAccountTestTask,
  createAccountTestSession,
  heartbeatAccountTestSession
} from '../../storage/account-test-tasks.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { dispatchAccountTestCancel } from './account-test-task-queue.service.js'

export function registerAccountTestSessionRoutes(router: Router): void {
  router.post('/test-sessions', (req, res) => {
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
    try {
      res.status(201).json(ok(createAccountTestSession(requestAccess)))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '创建账户测试会话失败'))
    }
  })

  router.post('/test-sessions/:sessionId/heartbeat', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const session = heartbeatAccountTestSession(req.params.sessionId, requestAccess)
    if (!session) {
      res.status(404).json({ message: '账户测试会话不存在' })
      return
    }
    res.json(ok(session))
  })

  router.post('/test-sessions/:sessionId/cancel', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const result = cancelAccountTestSession(req.params.sessionId, requestAccess)
    if (!result) {
      res.status(404).json({ message: '账户测试会话不存在' })
      return
    }
    for (const taskId of result.taskIds) {
      dispatchAccountTestCancel(taskId)
    }
    res.json(ok(result.session))
  })

  router.post('/test-tasks/:taskId/cancel', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const task = cancelAccountTestTask(req.params.taskId, requestAccess)
    if (!task) {
      res.status(404).json({ message: '账户测试任务不存在' })
      return
    }
    dispatchAccountTestCancel(task.id)
    res.json(ok(task))
  })
}

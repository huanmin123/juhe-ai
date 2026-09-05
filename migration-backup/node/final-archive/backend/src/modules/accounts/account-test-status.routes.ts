import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { queryTextList } from '../../shared/query-values.js'
import {
  getAccountTestSessionAsync,
  getAccountTestSessionDetailAsync,
  getAccountTestTaskAsync,
  listAccountTestTasksAsync
} from '../../storage/account-test-tasks.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'

export function registerAccountTestStatusRoutes(router: Router): void {
  router.get('/test-tasks', async (req, res, next) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      const taskIds = queryTextList(req.query.ids, 200)
      const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
      res.json(ok(await listAccountTestTasksAsync(taskIds, requestAccess)))
    } catch (error) {
      next(error)
    }
  })

  router.get('/test-sessions/:sessionId', async (req, res, next) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
      const session = await getAccountTestSessionAsync(req.params.sessionId, requestAccess)
      if (!session) {
        res.status(404).json({ message: '账户测试会话不存在' })
        return
      }
      res.json(ok(session))
    } catch (error) {
      next(error)
    }
  })

  router.get('/test-sessions/:sessionId/tasks', async (req, res, next) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
      const detail = await getAccountTestSessionDetailAsync(req.params.sessionId, requestAccess)
      if (!detail) {
        res.status(404).json({ message: '账户测试会话不存在' })
        return
      }
      res.json(ok(detail.tasks))
    } catch (error) {
      next(error)
    }
  })

  router.get('/test-tasks/:taskId', async (req, res, next) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
      const task = await getAccountTestTaskAsync(req.params.taskId, requestAccess)
      if (!task) {
        res.status(404).json({ message: '账户测试任务不存在' })
        return
      }
      res.json(ok(task))
    } catch (error) {
      next(error)
    }
  })
}

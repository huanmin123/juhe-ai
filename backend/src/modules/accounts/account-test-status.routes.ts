import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { queryTextList } from '../../shared/query-values.js'
import {
  getAccountTestSession,
  getAccountTestTask,
  listAccountTestTasks
} from '../../storage/account-test-tasks.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'

export function registerAccountTestStatusRoutes(router: Router): void {
  router.get('/test-tasks', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const taskIds = queryTextList(req.query.ids, 200)
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    res.json(ok(listAccountTestTasks(taskIds, requestAccess)))
  })

  router.get('/test-sessions/:sessionId', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const session = getAccountTestSession(req.params.sessionId, requestAccess)
    if (!session) {
      res.status(404).json({ message: '账户测试会话不存在' })
      return
    }
    res.json(ok(session))
  })

  router.get('/test-tasks/:taskId', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const task = getAccountTestTask(req.params.taskId, requestAccess)
    if (!task) {
      res.status(404).json({ message: '账户测试任务不存在' })
      return
    }
    res.json(ok(task))
  })
}

import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import {
  getOperationLogDetailAsync,
  getOperationLogDetailForViewerAsync,
  listOperationLogsAsync,
  listOperationLogsForViewerAsync,
} from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { parseOperationLogListOptions } from './operation-log-list-options.js'

export const operationLogsRouter = Router()
export const myOperationLogsRouter = Router()

myOperationLogsRouter.get('/', async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const result = await listOperationLogsForViewerAsync(context.systemAccountId, parseOperationLogListOptions(req.query, false))
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

myOperationLogsRouter.get('/:id', async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const detail = await getOperationLogDetailForViewerAsync(req.params.id, context.systemAccountId)
    if (!detail) {
      sendNotFound(res, '操作日志不存在')
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

operationLogsRouter.get('/', requireAdmin, async (req, res, next) => {
  try {
    const result = await listOperationLogsAsync(parseOperationLogListOptions(req.query, true))
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

operationLogsRouter.get('/:id', requireAdmin, async (req, res, next) => {
  try {
    const detail = await getOperationLogDetailAsync(req.params.id)
    if (!detail) {
      sendNotFound(res, '操作日志不存在')
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

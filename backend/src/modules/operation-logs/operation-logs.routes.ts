import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { getOperationLogDetailFromGo, listOperationLogsFromGo } from './operation-log-go-input.service.js'
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
    const result = await listOperationLogsFromGo(parseOperationLogListOptions(req.query, false), context.systemAccountId)
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
    const supplement = await getOperationLogDetailFromGo(req.params.id, context.systemAccountId)
    if (!supplement) {
      sendNotFound(res, '操作日志不存在')
      return
    }
    res.json(ok(supplement))
  } catch (error) {
    next(error)
  }
})

operationLogsRouter.get('/', requireAdmin, async (req, res, next) => {
  try {
    const result = await listOperationLogsFromGo(parseOperationLogListOptions(req.query, true))
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

operationLogsRouter.get('/:id', requireAdmin, async (req, res, next) => {
  try {
    const supplement = await getOperationLogDetailFromGo(req.params.id)
    if (!supplement) {
      sendNotFound(res, '操作日志不存在')
      return
    }
    res.json(ok(supplement))
  } catch (error) {
    next(error)
  }
})

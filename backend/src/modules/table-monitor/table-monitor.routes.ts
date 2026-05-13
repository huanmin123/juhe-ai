import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { getTableStorageOverview, listTableStorageHistory, type MonitoredDatabaseRole } from '../../storage/table-monitor.repository.js'

export const tableMonitorRouter = Router()

const overviewQuerySchema = z.object({
  startAt: z.string().trim().optional(),
  endAt: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(1000).optional()
})

const historyQuerySchema = z.object({
  databaseRole: z.enum(['business', 'records']),
  tableName: z.string().trim().min(1),
  startAt: z.string().trim().optional(),
  endAt: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(10000).optional()
})

tableMonitorRouter.get('/overview', (req, res) => {
  const parsed = overviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '表监控参数无效')))
    return
  }
  res.json(ok(getTableStorageOverview({
    startAt: parsed.data.startAt,
    endAt: parsed.data.endAt,
    limit: parsed.data.limit
  })))
})

tableMonitorRouter.get('/history', (req, res) => {
  const parsed = historyQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '表监控历史参数无效')))
    return
  }
  res.json(ok(listTableStorageHistory({
    databaseRole: parsed.data.databaseRole as MonitoredDatabaseRole,
    tableName: parsed.data.tableName,
    startAt: parsed.data.startAt,
    endAt: parsed.data.endAt,
    limit: parsed.data.limit
  })))
})

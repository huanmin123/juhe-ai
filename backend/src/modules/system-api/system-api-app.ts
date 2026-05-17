import express, { type NextFunction, type Request, type Response } from 'express'

import { accountsRouter } from '../accounts/accounts.routes.js'
import { announcementsRouter } from '../announcements/announcements.routes.js'
import { apiKeysRouter } from '../api-keys/api-keys.routes.js'
import { auditLogsRouter } from '../audit-logs/audit-logs.routes.js'
import { authorizationOptionsRouter } from '../authorization-options/authorization-options.routes.js'
import { authorizationsRouter } from '../authorizations/authorizations.routes.js'
import { forceSelfAccessScope, requireAdmin, requireAuth } from '../auth/auth.middleware.js'
import { authRouter } from '../auth/auth.routes.js'
import { errorPoliciesRouter } from '../error-policies/error-policies.routes.js'
import { featureRulesRouter } from '../feature-rules/feature-rules.routes.js'
import { groupsRouter } from '../groups/groups.routes.js'
import { myOperationLogsRouter, operationLogsRouter } from '../operation-logs/operation-logs.routes.js'
import { openAIOAuthRouter } from '../openai-oauth/openai-oauth.routes.js'
import { providersRouter } from '../providers/providers.routes.js'
import { proxiesRouter } from '../proxies/proxies.routes.js'
import { runtimeLogsRouter } from '../runtime-logs/runtime-logs.routes.js'
import { settingsRouter } from '../settings/settings.routes.js'
import { statsRouter } from '../stats/stats.routes.js'
import { systemAccountsRouter } from '../system-accounts/system-accounts.routes.js'
import { myTeamsRouter, systemTeamsRouter } from '../system-teams/system-teams.routes.js'
import { tableMonitorRouter } from '../table-monitor/table-monitor.routes.js'
import { usageRecordsRouter } from '../usage-records/usage-records.routes.js'
import { ok } from '../../shared/http.js'
import { getRequestLogger, requestContextMiddleware, sanitizeUrlForLog } from '../../shared/request-context.js'
import { listPublicGlobalSettings } from '../../storage/repositories.js'

export interface SystemApiAppOptions {
  systemApiPrefix: string
}

type BodyParserError = Error & {
  status?: number
  statusCode?: number
  type?: string
}

export function createSystemApiApp(options: SystemApiAppOptions): express.Express {
  const app = express()
  const { systemApiPrefix } = options

  app.use(requestContextMiddleware)
  app.use(systemApiPrefix, express.json({ limit: '2mb' }), handleJsonBodyError)

  app.get(`${systemApiPrefix}/health`, (_req, res) => {
    res.json({ status: 'ok', service: 'juhe-ai-db-service' })
  })

  app.use(`${systemApiPrefix}/auth`, authRouter)
  app.get(`${systemApiPrefix}/settings/public`, (_req, res) => {
    res.json(ok(listPublicGlobalSettings()))
  })

  app.use(systemApiPrefix, requireAuth)
  app.use(`${systemApiPrefix}/announcements`, announcementsRouter)
  app.use(`${systemApiPrefix}/my-accounts`, forceSelfAccessScope, accountsRouter)
  app.use(`${systemApiPrefix}/my-groups`, forceSelfAccessScope, groupsRouter)
  app.use(`${systemApiPrefix}/my-api-keys`, forceSelfAccessScope, apiKeysRouter)
  app.use(`${systemApiPrefix}/my-authorization-options`, forceSelfAccessScope, authorizationOptionsRouter)
  app.use(`${systemApiPrefix}/my-authorizations`, forceSelfAccessScope, authorizationsRouter)
  app.use(`${systemApiPrefix}/my-openai-oauth`, forceSelfAccessScope, openAIOAuthRouter)
  app.use(`${systemApiPrefix}/my-usage-records`, forceSelfAccessScope, usageRecordsRouter)
  app.use(`${systemApiPrefix}/my-stats`, forceSelfAccessScope, statsRouter)
  app.use(`${systemApiPrefix}/my-operation-logs`, forceSelfAccessScope, myOperationLogsRouter)
  app.use(`${systemApiPrefix}/providers`, requireAdmin, providersRouter)
  app.use(`${systemApiPrefix}/error-policies`, errorPoliciesRouter)
  app.use(`${systemApiPrefix}/feature-rules`, requireAdmin, featureRulesRouter)
  app.use(`${systemApiPrefix}/accounts`, requireAdmin, accountsRouter)
  app.use(`${systemApiPrefix}/groups`, requireAdmin, groupsRouter)
  app.use(`${systemApiPrefix}/api-keys`, requireAdmin, apiKeysRouter)
  app.use(`${systemApiPrefix}/authorization-options`, requireAdmin, authorizationOptionsRouter)
  app.use(`${systemApiPrefix}/authorizations`, requireAdmin, authorizationsRouter)
  app.use(`${systemApiPrefix}/openai-oauth`, requireAdmin, openAIOAuthRouter)
  app.use(`${systemApiPrefix}/proxies`, proxiesRouter)
  app.use(`${systemApiPrefix}/usage-records`, requireAdmin, usageRecordsRouter)
  app.use(`${systemApiPrefix}/operation-logs`, requireAdmin, operationLogsRouter)
  app.use(`${systemApiPrefix}/audit-logs`, requireAdmin, auditLogsRouter)
  app.use(`${systemApiPrefix}/runtime-logs`, requireAdmin, runtimeLogsRouter)
  app.use(`${systemApiPrefix}/stats`, requireAdmin, statsRouter)
  app.use(`${systemApiPrefix}/table-monitor`, requireAdmin, tableMonitorRouter)
  app.use(`${systemApiPrefix}/settings`, settingsRouter)
  app.use(`${systemApiPrefix}/system-accounts`, systemAccountsRouter)
  app.use(`${systemApiPrefix}/my-teams`, forceSelfAccessScope, myTeamsRouter)
  app.use(`${systemApiPrefix}/system-teams`, systemTeamsRouter)

  app.use(systemApiPrefix, (_req, res) => {
    res.status(404).json({ message: '资源不存在' })
  })

  app.use(handleSystemApiError)

  return app
}

function handleJsonBodyError(error: BodyParserError, req: Request, res: Response, next: NextFunction): void {
  const statusCode = Number.isInteger(error.statusCode)
    ? Number(error.statusCode)
    : Number.isInteger(error.status)
      ? Number(error.status)
      : 400

  if (!error.type && statusCode < 400) {
    next(error)
    return
  }

  getRequestLogger().warn({
    event: 'system_api_json_body_rejected',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    statusCode,
    errorType: error.type
  }, '系统 API 请求体被拒绝')
  res.status(statusCode >= 400 && statusCode < 600 ? statusCode : 400).json({
    message: statusCode === 413 ? '请求体过大' : '请求体无效'
  })
}

function handleSystemApiError(error: unknown, req: Request, res: Response, _next: NextFunction): void {
  getRequestLogger().error({
    event: 'system_api_unhandled_error',
    err: error instanceof Error ? error : undefined,
    errorMessage: error instanceof Error ? undefined : String(error),
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl)
  }, '系统 API 未处理错误')

  if (res.headersSent) {
    res.end()
    return
  }

  res.status(500).json({ message: '服务器内部错误' })
}

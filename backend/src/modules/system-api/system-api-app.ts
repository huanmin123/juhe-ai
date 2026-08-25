import express, { type NextFunction, type Request, type Response } from 'express'

import { accountsRouter } from '../accounts/accounts.routes.js'
import { announcementsRouter } from '../announcements/announcements.routes.js'
import { apiKeysRouter } from '../api-keys/api-keys.routes.js'
import { auditLogsRouter } from '../audit-logs/audit-logs.routes.js'
import { authorizationOptionsRouter } from '../authorization-options/authorization-options.routes.js'
import { authorizationsRouter } from '../authorizations/authorizations.routes.js'
import { forceSelfAccessScope, requireAdmin, requireAuth } from '../auth/auth.middleware.js'
import { authRouter } from '../auth/auth.routes.js'
import { externalIntegrationsRouter } from '../external-integrations/external-integrations.routes.js'
import { externalIntegrationSourcesRouter } from '../external-integrations/external-integration-sources.routes.js'
import { delegatedApiRouter } from '../delegated-api/delegated-api.routes.js'
import { groupsRouter } from '../groups/groups.routes.js'
import { chatRouter } from '../chat/chat.routes.js'
import { ipStatsRouter } from '../ip-stats/ip-stats.routes.js'
import { modelChecksRouter } from '../model-checks/model-checks.routes.js'
import { myOperationLogsRouter, operationLogsRouter } from '../operation-logs/operation-logs.routes.js'
import { anthropicOAuthRouter } from '../anthropic-oauth/anthropic-oauth.routes.js'
import { geminiOAuthRouter } from '../gemini-oauth/gemini-oauth.routes.js'
import { grokOAuthRouter } from '../grok-oauth/grok-oauth.routes.js'
import { openAIOAuthRouter } from '../openai-oauth/openai-oauth.routes.js'
import { oauthManagementRouter, oauthPublicRouter } from '../oidc-provider/oidc-provider.routes.js'
import { providersRouter } from '../providers/providers.routes.js'
import { proxiesRouter } from '../proxies/proxies.routes.js'
import { capturePublicApiLog } from '../public-api-logs/public-api-log-capture.middleware.js'
import { publicApiLogsRouter } from '../public-api-logs/public-api-logs.routes.js'
import { runtimeLogsRouter } from '../runtime-logs/runtime-logs.routes.js'
import { routeStrategiesRouter } from '../route-strategies/route-strategies.routes.js'
import { settingsRouter } from '../settings/settings.routes.js'
import { statsRouter } from '../stats/stats.routes.js'
import { responseInspectionPoliciesRouter } from '../response-inspection-policies/response-inspection-policies.routes.js'
import { systemAccountsRouter } from '../system-accounts/system-accounts.routes.js'
import { myTeamsRouter, systemTeamsRouter } from '../system-teams/system-teams.routes.js'
import { tableMonitorRouter } from '../table-monitor/table-monitor.routes.js'
import { usageRecordsRouter } from '../usage-records/usage-records.routes.js'
import { uiBootstrapRouter } from '../ui-bootstrap/ui-bootstrap.routes.js'
import { ok } from '../../shared/http.js'
import { getRequestLogger, requestContextMiddleware, sanitizeUrlForLog } from '../../shared/request-context.js'
import { listPublicGlobalSettingsAsync } from '../../storage/repositories.js'
import { systemApiAuthenticatedRateLimit, systemApiIpRateLimit } from './system-api-rate-limit.middleware.js'
import {
  controlReadReplicaPrimaryOnlyRequestGuard,
  controlReadReplicaProxy,
  controlReadReplicaRequestGuard
} from './control-read-replica-proxy.js'
import { createHttpCompressionMiddleware } from '../../shared/http-compression.js'
import { systemErrorMessageLocalizationMiddleware } from '../../shared/system-error-message.js'
import {
  systemApiDbAccessModeMiddleware,
  systemApiDbServiceAdmissionControl,
  systemApiDbServiceMaxInFlight
} from './system-api-db-access.js'
import { accountBalanceGoOwnerEnabled } from '../background/account-balance-handover.js'
import { accountBalanceJobsOutcomeProjectionRuntimeReady } from '../background/account-balance-jobs-outcome-projection-runtime.service.js'
import { parseLoopbackHttpUrl } from '../../shared/loopback-http.js'

export interface SystemApiAppOptions {
  systemApiPrefix: string
  publicApiPrefix?: string
  trustProxy?: boolean | number
  bypassSystemApiRateLimitForTest?: boolean
  accountBalanceHealth?: () => Promise<SystemApiDependencyHealth>
  proxyLatencyHealth?: () => Promise<SystemApiDependencyHealth>
}

type BodyParserError = Error & {
  status?: number
  statusCode?: number
  type?: string
}

export const systemApiJsonBodyLimit = '256kb'
export const chatSystemApiJsonBodyLimit = '24mb'
export { systemApiDbServiceAdmissionControl, systemApiDbServiceMaxInFlight }

export interface SystemApiDependencyHealth {
  enabled: boolean
  ready: boolean
  projectorReady?: boolean
}

export interface SystemApiHealthResponse {
  statusCode: 200
  status: 'ok' | 'degraded'
  service: 'juhe-ai-db-service'
  accountBalance: SystemApiDependencyHealth
  proxyLatency: SystemApiDependencyHealth
}

/**
 * The DB service health endpoint reports account-level dependency degradation
 * without turning it into the Node process readiness signal. A dead or
 * unreachable DB service still fails at the parent proxy boundary (503/504).
 */
export function resolveSystemApiHealth(
  accountBalance: SystemApiDependencyHealth,
  proxyLatency: SystemApiDependencyHealth
): SystemApiHealthResponse {
  return {
    statusCode: 200,
    status: accountBalance.ready && proxyLatency.ready ? 'ok' : 'degraded',
    service: 'juhe-ai-db-service',
    accountBalance,
    proxyLatency
  }
}

export function createSystemApiApp(options: SystemApiAppOptions): express.Express {
  const app = express()
  const { systemApiPrefix } = options
  const publicApiPrefix = options.publicApiPrefix ?? '/__aipublic__'
  app.set('etag', false)
  if (options.trustProxy !== undefined) {
    app.set('trust proxy', options.trustProxy)
  }

  app.use(requestContextMiddleware)
  app.use(systemErrorMessageLocalizationMiddleware)
  app.use(createHttpCompressionMiddleware())
  app.use(systemApiPrefix, noStoreSystemApiResponse)
  app.use(systemApiPrefix, systemApiDbAccessModeMiddleware(systemApiPrefix))
  app.use(systemApiPrefix, controlReadReplicaRequestGuard)
  if (!options.bypassSystemApiRateLimitForTest) {
    app.use(systemApiPrefix, systemApiIpRateLimit)
    app.use(`${systemApiPrefix}/my-chat`, requireAuth, systemApiAuthenticatedRateLimit, systemApiDbServiceAdmissionControl, express.json({ limit: chatSystemApiJsonBodyLimit }), handleJsonBodyError, forceSelfAccessScope, chatRouter)
  } else {
    app.use(`${systemApiPrefix}/my-chat`, requireAuth, systemApiDbServiceAdmissionControl, express.json({ limit: chatSystemApiJsonBodyLimit }), handleJsonBodyError, forceSelfAccessScope, chatRouter)
  }
  app.use(systemApiPrefix, express.json({ limit: systemApiJsonBodyLimit }), handleJsonBodyError)
  app.use(publicApiPrefix, controlReadReplicaPrimaryOnlyRequestGuard)
  app.use(publicApiPrefix, capturePublicApiLog, express.json({ limit: systemApiJsonBodyLimit }), handleJsonBodyError)
  app.use(publicApiPrefix, systemApiDbAccessModeMiddleware(publicApiPrefix), systemApiDbServiceAdmissionControl)
  app.use('/__aidelegated__/v1', noStoreSystemApiResponse, controlReadReplicaPrimaryOnlyRequestGuard, express.json({ limit: systemApiJsonBodyLimit }), handleJsonBodyError)
  app.use('/__aidelegated__/v1', systemApiDbAccessModeMiddleware('/__aidelegated__/v1'), systemApiDbServiceAdmissionControl, delegatedApiRouter)

  app.get(`${systemApiPrefix}/health`, async (_req, res) => {
    const [accountBalance, proxyLatency] = await Promise.all([
      (options.accountBalanceHealth ?? accountBalanceGoOwnerHealth)(),
      (options.proxyLatencyHealth ?? proxyLatencyGoOwnerHealth)()
    ])
    const health = resolveSystemApiHealth(accountBalance, proxyLatency)
    res.status(health.statusCode).json({ ...health, checkedAt: new Date().toISOString() })
  })

  app.use('/.well-known', controlReadReplicaPrimaryOnlyRequestGuard)
  app.use('/oauth', controlReadReplicaPrimaryOnlyRequestGuard)
  app.use(systemApiDbServiceAdmissionControl, oauthPublicRouter)

  app.use(`${systemApiPrefix}/auth`, systemApiDbServiceAdmissionControl, authRouter)
  app.get(`${systemApiPrefix}/settings/public`, async (_req, res, next) => {
    try {
      res.json(ok(await listPublicGlobalSettingsAsync()))
    } catch (error) {
      next(error)
    }
  })
  app.use(publicApiPrefix, externalIntegrationsRouter)

  app.use(systemApiPrefix, requireAuth)
  if (!options.bypassSystemApiRateLimitForTest) {
    app.use(systemApiPrefix, systemApiAuthenticatedRateLimit)
  }
  app.use(systemApiPrefix, systemApiDbServiceAdmissionControl)
  app.use(systemApiPrefix, controlReadReplicaProxy)
  app.use(`${systemApiPrefix}/announcements`, announcementsRouter)
  app.use(`${systemApiPrefix}/my-accounts`, forceSelfAccessScope, accountsRouter)
  app.use(`${systemApiPrefix}/my-groups`, forceSelfAccessScope, groupsRouter)
  app.use(`${systemApiPrefix}/my-route-strategies`, forceSelfAccessScope, routeStrategiesRouter)
  app.use(`${systemApiPrefix}/my-api-keys`, forceSelfAccessScope, apiKeysRouter)
  app.use(`${systemApiPrefix}/my-ui-bootstrap`, forceSelfAccessScope, uiBootstrapRouter)
  app.use(`${systemApiPrefix}/my-authorization-options`, forceSelfAccessScope, authorizationOptionsRouter)
  app.use(`${systemApiPrefix}/my-authorizations`, forceSelfAccessScope, authorizationsRouter)
  app.use(`${systemApiPrefix}/my-anthropic-oauth`, forceSelfAccessScope, anthropicOAuthRouter)
  app.use(`${systemApiPrefix}/my-gemini-oauth`, forceSelfAccessScope, geminiOAuthRouter)
  app.use(`${systemApiPrefix}/my-grok-oauth`, forceSelfAccessScope, grokOAuthRouter)
  app.use(`${systemApiPrefix}/my-openai-oauth`, forceSelfAccessScope, openAIOAuthRouter)
  app.use(`${systemApiPrefix}/my-usage-records`, forceSelfAccessScope, usageRecordsRouter)
  app.use(`${systemApiPrefix}/my-model-checks`, forceSelfAccessScope, modelChecksRouter)
  app.use(`${systemApiPrefix}/my-stats`, forceSelfAccessScope, statsRouter)
  app.use(`${systemApiPrefix}/my-operation-logs`, forceSelfAccessScope, myOperationLogsRouter)
  app.use(`${systemApiPrefix}/providers`, providersRouter)
  app.use(`${systemApiPrefix}/response-inspection-policies`, requireAdmin, responseInspectionPoliciesRouter)
  app.use(`${systemApiPrefix}/accounts`, requireAdmin, accountsRouter)
  app.use(`${systemApiPrefix}/groups`, requireAdmin, groupsRouter)
  app.use(`${systemApiPrefix}/route-strategies`, requireAdmin, routeStrategiesRouter)
  app.use(`${systemApiPrefix}/api-keys`, requireAdmin, apiKeysRouter)
  app.use(`${systemApiPrefix}/ui-bootstrap`, requireAdmin, uiBootstrapRouter)
  app.use(`${systemApiPrefix}/authorization-options`, requireAdmin, authorizationOptionsRouter)
  app.use(`${systemApiPrefix}/authorizations`, requireAdmin, authorizationsRouter)
  app.use(`${systemApiPrefix}/anthropic-oauth`, requireAdmin, anthropicOAuthRouter)
  app.use(`${systemApiPrefix}/gemini-oauth`, requireAdmin, geminiOAuthRouter)
  app.use(`${systemApiPrefix}/grok-oauth`, requireAdmin, grokOAuthRouter)
  app.use(`${systemApiPrefix}/openai-oauth`, requireAdmin, openAIOAuthRouter)
  app.use(`${systemApiPrefix}/proxies`, proxiesRouter)
  app.use(`${systemApiPrefix}/usage-records`, requireAdmin, usageRecordsRouter)
  app.use(`${systemApiPrefix}/model-checks`, requireAdmin, modelChecksRouter)
  app.use(`${systemApiPrefix}/operation-logs`, requireAdmin, operationLogsRouter)
  app.use(`${systemApiPrefix}/public-api-logs`, requireAdmin, publicApiLogsRouter)
  app.use(`${systemApiPrefix}/audit-logs`, requireAdmin, auditLogsRouter)
  app.use(`${systemApiPrefix}/runtime-logs`, requireAdmin, runtimeLogsRouter)
  app.use(`${systemApiPrefix}/stats`, requireAdmin, statsRouter)
  app.use(`${systemApiPrefix}/ip-stats`, requireAdmin, ipStatsRouter)
  app.use(`${systemApiPrefix}/external-integration-sources`, requireAdmin, externalIntegrationSourcesRouter)
  app.use(`${systemApiPrefix}/oauth`, requireAdmin, oauthManagementRouter)
  app.use(`${systemApiPrefix}/table-monitor`, requireAdmin, tableMonitorRouter)
  app.use(`${systemApiPrefix}/settings`, settingsRouter)
  app.use(`${systemApiPrefix}/system-accounts`, systemAccountsRouter)
  app.use(`${systemApiPrefix}/my-teams`, forceSelfAccessScope, myTeamsRouter)
  app.use(`${systemApiPrefix}/system-teams`, systemTeamsRouter)

  app.use(systemApiPrefix, (_req, res) => {
    res.status(404).json({ message: '资源不存在' })
  })

  app.use(publicApiPrefix, (_req, res) => {
    res.status(404).json({ message: '资源不存在' })
  })

  app.use(handleSystemApiError)

  return app
}

export async function accountBalanceGoOwnerHealth(
  env: NodeJS.ProcessEnv = process.env,
  dependencies: { projectorReady?: () => boolean; fetch?: typeof fetch } = {}
): Promise<{ enabled: boolean; ready: boolean; projectorReady?: boolean }> {
  if (!accountBalanceGoOwnerEnabled(env)) return { enabled: false, ready: true }
  const endpoint = env.JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_URL?.trim()
  const projectorReady = (dependencies.projectorReady ?? accountBalanceJobsOutcomeProjectionRuntimeReady)()
  if (!endpoint || !projectorReady) return { enabled: true, ready: false, projectorReady }
  try {
    const healthUrl = new URL('/health', endpoint)
    const response = await (dependencies.fetch ?? fetch)(healthUrl, { signal: AbortSignal.timeout(2_000) })
    const payload: unknown = await response.json()
    const health = payload && typeof payload === 'object' ? payload as Record<string, unknown> : undefined
    const ready = response.ok && health?.ready === true && health.accountBalanceEnabled === true && health.accountBalanceReady === true
    return { enabled: true, ready, projectorReady }
  } catch {
    return { enabled: true, ready: false, projectorReady }
  }
}

/**
 * J3a exposes only a one-way Node -> Go health observation. The Go jobs
 * process owns probe execution and projection, and never calls Node from this
 * path. When the explicit Go owner switch is absent this dependency is not
 * enabled; once present, every required readiness field must be true.
 */
export async function proxyLatencyGoOwnerHealth(
  env: NodeJS.ProcessEnv = process.env,
  dependencies: { fetch?: typeof fetch } = {}
): Promise<SystemApiDependencyHealth> {
  if (env.JUHE_AI_PROXY_LATENCY_JOBS_OWNER?.trim().toLowerCase() !== 'go') return { enabled: false, ready: true }
  const endpoint = env.JUHE_AI_PROXY_LATENCY_JOBS_HTTP_URL?.trim()
  if (!endpoint) return { enabled: true, ready: false }
  try {
    const healthUrl = new URL('/health', parseLoopbackHttpUrl(endpoint, 'J3a Go health observer endpoint'))
    const response = await (dependencies.fetch ?? fetch)(healthUrl, { signal: AbortSignal.timeout(2_000) })
    const payload: unknown = await response.json()
    const health = payload && typeof payload === 'object' ? payload as Record<string, unknown> : undefined
    const ready = response.ok
      && health?.ready === true
      && health.proxyLatencyEnabled === true
      && health.proxyLatencyReady === true
      && health.proxyLatencyOwnerHeld === true
    return { enabled: true, ready }
  } catch {
    return { enabled: true, ready: false }
  }
}

function noStoreSystemApiResponse(_req: Request, res: Response, next: NextFunction): void {
  res.setHeader('Cache-Control', 'no-store')
  next()
}

function handleJsonBodyError(error: BodyParserError, req: Request, res: Response, next: NextFunction): void {
  if (!error || typeof error !== 'object' || typeof error.type !== 'string') {
    next(error)
    return
  }
  const statusCode = Number.isInteger(error.statusCode)
    ? Number(error.statusCode)
    : Number.isInteger(error.status)
      ? Number(error.status)
      : 400

  res.locals.publicApiRequestBodyRejected = {
    statusCode,
    errorType: error.type
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

  const statusCode = error && typeof error === 'object' && Number.isInteger((error as { statusCode?: unknown }).statusCode)
    ? Number((error as { statusCode: number }).statusCode)
    : 500
  if (statusCode >= 400 && statusCode < 500) {
    res.status(statusCode).json({ message: error instanceof Error ? error.message : '请求参数无效' })
    return
  }
  res.status(500).json({ message: '服务器内部错误' })
}

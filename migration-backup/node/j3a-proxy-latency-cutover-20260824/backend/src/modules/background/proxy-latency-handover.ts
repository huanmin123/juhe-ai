import { createCipheriv, createHash, randomBytes } from 'node:crypto'
import type { ProxyProfileTestConfig } from '../../storage/proxy.repository.js'
import { listProvidersAsync } from '../../storage/provider.repository.js'
import { parseRfc3339Instant } from '../../shared/rfc3339.js'
import type { ProxyTestReport } from '../proxies/proxy-test.service.js'

export const proxyLatencyHandoverSchemaVersion = 1 as const
export const proxyLatencyHandoverJobName = 'proxy-latency' as const

export function proxyLatencyGoOwnerEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return env.JUHE_AI_PROXY_LATENCY_JOBS_OWNER?.trim().toLowerCase() === 'go'
}

/** Node remains the default owner until every Go-owner gate is explicit. */
export function proxyLatencyNodeOwnerEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return !proxyLatencyGoOwnerEnabled(env)
}

export function proxyLatencyManualBridgeEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return proxyLatencyGoOwnerEnabled(env)
    && env.JUHE_AI_PROXY_LATENCY_MANUAL_ENABLED?.trim().toLowerCase() === 'true'
}

/**
 * The public Node route must not select Go merely because the owner string was
 * changed.  All four handoff facts are required before the route is allowed
 * to cross the process boundary; otherwise a partial cutover would silently
 * remove the Node executor without a ready projector/bridge.
 */
export function proxyLatencyGoHandoverReady(env: NodeJS.ProcessEnv = process.env): boolean {
  return resolveProxyLatencyHandoverGate({
    enabled: proxyLatencyGoOwnerEnabled(env),
    goCommandWiringReady: env.JUHE_AI_PROXY_LATENCY_JOBS_COMMAND_WIRING_READY?.trim().toLowerCase() === 'true',
    goProjectionReady: env.JUHE_AI_PROXY_LATENCY_PROJECTION_READY?.trim().toLowerCase() === 'true',
    goManualBridgeReady: proxyLatencyManualBridgeEnabled(env)
      && env.JUHE_AI_PROXY_LATENCY_MANUAL_BRIDGE_READY?.trim().toLowerCase() === 'true',
    nodeOwnerDrained: env.JUHE_AI_PROXY_LATENCY_NODE_OWNER_DRAINED?.trim().toLowerCase() === 'true'
  }).enabled
}

export interface ProxyLatencyHandoverGateInput {
  enabled?: boolean
  goCommandWiringReady?: boolean
  goProjectionReady?: boolean
  goManualBridgeReady?: boolean
  nodeOwnerDrained?: boolean
}

export type ProxyLatencyHandoverGateReason =
  | 'disabled_by_default'
  | 'go_command_wiring_missing'
  | 'go_projection_missing'
  | 'go_manual_bridge_missing'
  | 'node_owner_not_drained'

export interface ProxyLatencyHandoverGate {
  enabled: boolean
  reason?: ProxyLatencyHandoverGateReason
}

/**
 * Resolve the complete J3a owner gate. Every fact is intentionally explicit;
 * a missing handoff fact never falls back to the legacy Node/Go path.
 */
export function resolveProxyLatencyHandoverGate(
  input: ProxyLatencyHandoverGateInput = {}
): ProxyLatencyHandoverGate {
  if (input.enabled !== true) return { enabled: false, reason: 'disabled_by_default' }
  if (input.goCommandWiringReady !== true) return { enabled: false, reason: 'go_command_wiring_missing' }
  if (input.goProjectionReady !== true) return { enabled: false, reason: 'go_projection_missing' }
  if (input.goManualBridgeReady !== true) return { enabled: false, reason: 'go_manual_bridge_missing' }
  if (input.nodeOwnerDrained !== true) return { enabled: false, reason: 'node_owner_not_drained' }
  return { enabled: true }
}

export async function runProxyLatencyManualViaGo(
  proxy: ProxyProfileTestConfig,
  options: { signal?: AbortSignal; timeoutMs?: number } = {}
): Promise<ProxyTestReport> {
  if (!proxyLatencyGoHandoverReady()) throw new Error('J3a Go owner handoff gate 未完成')
  const endpoint = process.env.JUHE_AI_PROXY_LATENCY_JOBS_HTTP_URL?.trim()
  const secret = process.env.JUHE_AI_PROXY_LATENCY_MANUAL_HTTP_SECRET?.trim()
  const credentialSecret = process.env.JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET?.trim()
  if (!endpoint || !secret || secret.length < 32 || !credentialSecret) throw new Error('J3a Go manual bridge 配置不完整')
  const providers = (await listProvidersAsync()).filter((provider) => provider.enabled)
  const proxyUrl = new URL(proxy.proxyUrl)
  const input = {
    schema_version: 1,
    proxy_id: proxy.id,
    proxy_name: proxy.name,
    config_revision: proxy.configUpdatedAt,
    proxy_type: proxy.type,
    proxy_host: proxy.host,
    proxy_port: proxy.port,
    ...(proxy.username ? { proxy_username: proxy.username } : {}),
    ...(proxyUrl.password ? { proxy_password: { kind: 'proxy_password', ciphertext: encryptProxyPassword(decodeURIComponent(proxyUrl.password), credentialSecret) } } : {}),
    targets: providers.map((provider) => ({ provider: provider.code, profile_id: provider.code, name: provider.name, url: provider.baseUrl })),
    deadline_ms: Math.min(25_000, Math.max(1_000, options.timeoutMs ?? 25_000))
  }
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(new Error('J3a Go manual bridge deadline exceeded')), input.deadline_ms + 1_000)
  const onAbort = (): void => controller.abort(options.signal?.reason ?? new Error('J3a Go manual bridge canceled'))
  options.signal?.addEventListener('abort', onAbort, { once: true })
  try {
    const response = await fetch(new URL('/proxy-latency/manual', endpoint), {
      method: 'POST',
      headers: { authorization: `Bearer ${secret}`, 'content-type': 'application/json' },
      body: JSON.stringify({ input }),
      signal: controller.signal
    })
    const text = await response.text()
    if (!response.ok) throw new Error(`Go manual bridge HTTP ${response.status}: ${text.slice(0, 2_000)}`)
    const report = parseProxyLatencyHandoverReport(JSON.parse(text))
    assertProxyLatencyReportMatchesProxy(report, proxy.id)
    return report
  } finally {
    clearTimeout(timeout)
    options.signal?.removeEventListener('abort', onAbort)
  }
}

function encryptProxyPassword(password: string, secret: string): string {
  const iv = randomBytes(12)
  const cipher = createCipheriv('aes-256-gcm', createHash('sha256').update(secret).digest(), iv)
  const encrypted = Buffer.concat([cipher.update(Buffer.from(JSON.stringify({ password }), 'utf8')), cipher.final()])
  return `v1:${iv.toString('base64url')}:${cipher.getAuthTag().toString('base64url')}:${encrypted.toString('base64url')}`
}

export function parseProxyLatencyHandoverReport(value: unknown): ProxyTestReport {
  const envelope = record(value, 'J3a Go result')
  exact(envelope, ['schemaVersion', 'job', 'report'])
  if (envelope.schemaVersion !== 1 || envelope.job !== proxyLatencyHandoverJobName) throw new Error('J3a Go result schema/job 不匹配')
  const report = record(envelope.report, 'J3a Go report')
  exact(report, ['proxyId', 'proxyName', 'score', 'grade', 'status', 'passedCount', 'warningCount', 'failedCount', 'testedAt', 'items', 'message'], ['outboundIp', 'outboundRegion', 'baseLatencyMs'])
  if (typeof report.proxyId !== 'string' || typeof report.proxyName !== 'string' || !Number.isSafeInteger(report.score) || report.score < 0 || report.score > 100 || typeof report.grade !== 'string' || !['A', 'B', 'C', 'D'].includes(report.grade) || typeof report.testedAt !== 'string' || !parseRfc3339Instant(report.testedAt) || typeof report.message !== 'string') throw new Error('J3a Go report 基础字段无效')
  if (report.baseLatencyMs !== undefined && (!Number.isSafeInteger(report.baseLatencyMs) || report.baseLatencyMs < 0)) throw new Error('J3a Go report baseLatencyMs 无效')
  if (!['passed', 'warning', 'failed', 'unknown'].includes(String(report.status))) throw new Error('J3a Go report status 无效')
  for (const field of ['passedCount', 'warningCount', 'failedCount']) if (!Number.isSafeInteger(report[field]) || Number(report[field]) < 0) throw new Error(`J3a Go report ${field} 无效`)
  if (!Array.isArray(report.items)) throw new Error('J3a Go report items 无效')
  const items = report.items.map((item) => {
    const row = record(item, 'J3a Go report item')
    exact(row, ['name', 'status', 'message'], ['httpStatus', 'latencyMs', 'targetUrl'])
    if (typeof row.name !== 'string' || typeof row.message !== 'string' || !['passed', 'warning', 'failed', 'unknown'].includes(String(row.status))) throw new Error('J3a Go report item 无效')
    if (row.httpStatus !== undefined && (!Number.isSafeInteger(row.httpStatus) || Number(row.httpStatus) < 100 || Number(row.httpStatus) > 599)) throw new Error('J3a Go report item httpStatus 无效')
    if (row.latencyMs !== undefined && (!Number.isSafeInteger(row.latencyMs) || Number(row.latencyMs) < 0)) throw new Error('J3a Go report item latencyMs 无效')
    if (row.targetUrl !== undefined && typeof row.targetUrl !== 'string') throw new Error('J3a Go report item targetUrl 无效')
    return row as unknown as ProxyTestReport['items'][number]
  })
  return {
    proxyId: report.proxyId,
    proxyName: report.proxyName,
    score: report.score,
    grade: report.grade,
    status: report.status as ProxyTestReport['status'],
    passedCount: report.passedCount as number,
    warningCount: report.warningCount as number,
    failedCount: report.failedCount as number,
    ...(typeof report.outboundIp === 'string' ? { outboundIp: report.outboundIp } : {}),
    ...(typeof report.outboundRegion === 'string' ? { outboundRegion: report.outboundRegion } : {}),
    ...(typeof report.baseLatencyMs === 'number' ? { baseLatencyMs: report.baseLatencyMs } : {}),
    testedAt: report.testedAt,
    items,
    message: report.message
  }
}

export function assertProxyLatencyReportMatchesProxy(
  report: Pick<ProxyTestReport, 'proxyId'>,
  proxyId: string
): void {
  if (report.proxyId !== proxyId) throw new Error('J3a Go report proxyId 与请求代理不匹配')
}

function record(value: unknown, label: string): Record<string, any> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label} 必须是对象`)
  return value as Record<string, any>
}
function exact(value: Record<string, unknown>, required: string[], optional: string[] = []): void {
  const allowed = new Set([...required, ...optional])
  if (required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) || Object.keys(value).some((key) => !allowed.has(key))) throw new Error('J3a Go result 字段不完整')
}

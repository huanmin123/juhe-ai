import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

interface BenchmarkConfig {
  baseUrl: string
  profile: BenchmarkProfile
  accessScope: BenchmarkAccessScope
  iterations: number
  concurrency: number
  requestDelayMs: number
  warmupIterations: number
  requestTimeoutMs: number
  maxAllowedP95Ms: number
  maxAllowedErrorRate: number
  reportPath: string
  authHeader?: string
  cookie?: string
}

type BenchmarkProfile = 'core' | 'broad' | 'long-read'
type BenchmarkAccessScope = 'self' | 'admin' | 'all'
type EndpointAccess = 'public' | 'auth' | 'self' | 'admin' | 'shared'

interface EndpointSpec {
  name: string
  path: string
  profile: BenchmarkProfile
  access: EndpointAccess
  requiresAuth?: boolean
  dynamicFrom?: DynamicEndpointSource
  expectedStatuses?: number[]
}

interface DynamicEndpointSource {
  sourceName: string
  idField?: string
  buildPath: (value: string) => string
}

interface ProbeResult {
  endpoint: string
  path: string
  status: number
  ok: boolean
  latencyMs: number
  bytes: number
  cacheControl?: string
  serverTiming?: string
  error?: string
}

interface EndpointReport {
  endpoint: string
  path: string
  count: number
  ok: number
  errors: number
  p50Ms: number
  p95Ms: number
  p99Ms: number
  maxMs: number
  bytesAvg: number
  statuses: Record<string, number>
  serverTiming: Record<string, ServerTimingSummary>
}

interface ServerTimingSummary {
  count: number
  p50Ms: number
  p95Ms: number
  maxMs: number
}

interface BenchmarkReport {
  startedAt: string
  finishedAt: string
  durationMs: number
  config: Omit<BenchmarkConfig, 'cookie' | 'authHeader'>
  endpoints: Array<{ name: string; path: string; profile: BenchmarkProfile; access: EndpointAccess }>
  totalRequests: number
  okRequests: number
  errorRequests: number
  errorRate: number
  overall: EndpointReport
  endpointReports: EndpointReport[]
  failures: ProbeResult[]
  pass: boolean
  violations: string[]
}

interface ApiEnvelope {
  data?: unknown
}

const config = loadConfig()

try {
  const report = await runBenchmark(config)
  outputReport(report)
  printSummary(report)
  process.exit(report.pass ? 0 : 1)
} catch (error) {
  console.error(error instanceof Error ? error.stack ?? error.message : error)
  process.exit(1)
}

async function runBenchmark(input: BenchmarkConfig): Promise<BenchmarkReport> {
  const startedAt = new Date()
  const startedAtMs = performance.now()
  const endpoints = await resolveEndpoints(input)

  for (let index = 0; index < input.warmupIterations; index += 1) {
    await runEndpointBatch(input, endpoints)
  }

  const probes: ProbeResult[] = []
  const requestQueue = endpoints.flatMap((endpoint) =>
    Array.from({ length: input.iterations }, () => endpoint)
  )
  let nextIndex = 0
  await Promise.all(Array.from({ length: input.concurrency }, async () => {
    while (nextIndex < requestQueue.length) {
      const endpoint = requestQueue[nextIndex]
      nextIndex += 1
      if (!endpoint) continue
      probes.push(await requestEndpoint(input, endpoint))
      if (input.requestDelayMs > 0) {
        await delay(input.requestDelayMs)
      }
    }
  }))

  const finishedAt = new Date()
  const durationMs = performance.now() - startedAtMs
  return buildReport(input, startedAt, finishedAt, durationMs, endpoints, probes)
}

async function resolveEndpoints(input: BenchmarkConfig): Promise<EndpointSpec[]> {
  const staticEndpoints = endpointCatalog().filter((endpoint) =>
    profileIncludes(input.profile, endpoint.profile) && accessScopeIncludes(input.accessScope, endpoint.access)
  )
  const resolved = [...staticEndpoints]
  const dynamicSources = staticEndpoints.filter((endpoint) => !endpoint.dynamicFrom)
  const sourceData = new Map<string, unknown>()

  for (const endpoint of dynamicSources) {
    if (!dynamicSourceNames().has(endpoint.name)) continue
    const result = await requestJson(input, endpoint)
    if (result.ok) {
      sourceData.set(endpoint.name, result.data)
    }
  }

  for (const endpoint of staticEndpoints.filter((item) => item.dynamicFrom)) {
    const source = endpoint.dynamicFrom
    if (!source) continue
    const data = sourceData.get(source.sourceName)
    const value = firstFieldValue(data, source.idField ?? 'id')
    if (!value) continue
    resolved.push({
      ...endpoint,
      path: source.buildPath(value),
      dynamicFrom: undefined
    })
  }

  return uniqueEndpoints(resolved.filter((endpoint) => !endpoint.dynamicFrom))
}

function endpointCatalog(): EndpointSpec[] {
  return [
    { name: 'GET health', path: '/__aisys__/api/health', profile: 'core', access: 'public', expectedStatuses: [200, 404] },
    { name: 'GET settings/public', path: '/__aisys__/api/settings/public', profile: 'core', access: 'public' },
    { name: 'GET auth/me', path: '/__aisys__/api/auth/me', profile: 'core', access: 'auth', requiresAuth: true },
    { name: 'GET providers/options', path: '/__aisys__/api/providers/options', profile: 'core', access: 'shared', requiresAuth: true },
    { name: 'GET providers/models/options', path: '/__aisys__/api/providers/models/options?protocol=openai', profile: 'broad', access: 'shared', requiresAuth: true },
    { name: 'GET providers/gpt/models', path: '/__aisys__/api/providers/gpt/models?includeInactive=true&includeUnpriced=true', profile: 'broad', access: 'shared', requiresAuth: true },
    { name: 'GET proxies/options', path: '/__aisys__/api/proxies/options?limit=50', profile: 'core', access: 'shared', requiresAuth: true },

    { name: 'GET my-accounts', path: '/__aisys__/api/my-accounts?page=1&pageSize=20&status=all&sorts=priority:asc', profile: 'core', access: 'self', requiresAuth: true },
    { name: 'GET my-accounts/options', path: '/__aisys__/api/my-accounts/options?limit=50', profile: 'core', access: 'self', requiresAuth: true },
    { name: 'GET my-accounts/tags', path: '/__aisys__/api/my-accounts/tags', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-accounts/test-tasks', path: '/__aisys__/api/my-accounts/test-tasks?ids=missing_task', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-account detail', path: '', profile: 'core', access: 'self', requiresAuth: true, dynamicFrom: { sourceName: 'GET my-accounts', buildPath: (id) => `/__aisys__/api/my-accounts/${encodeURIComponent(id)}` } },
    { name: 'GET my-account advanced', path: '', profile: 'broad', access: 'self', requiresAuth: true, dynamicFrom: { sourceName: 'GET my-accounts', buildPath: (id) => `/__aisys__/api/my-accounts/${encodeURIComponent(id)}/advanced` } },
    { name: 'GET my-account edit-basic', path: '', profile: 'broad', access: 'self', requiresAuth: true, dynamicFrom: { sourceName: 'GET my-accounts', buildPath: (id) => `/__aisys__/api/my-accounts/${encodeURIComponent(id)}/edit-basic` } },
    { name: 'GET my-groups', path: '/__aisys__/api/my-groups?page=1&pageSize=20', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-groups/options', path: '/__aisys__/api/my-groups/options?limit=50', profile: 'core', access: 'self', requiresAuth: true },
    { name: 'GET my-groups/account-options', path: '/__aisys__/api/my-groups/account-options?limit=50', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-group detail', path: '', profile: 'broad', access: 'self', requiresAuth: true, dynamicFrom: { sourceName: 'GET my-groups', buildPath: (id) => `/__aisys__/api/my-groups/${encodeURIComponent(id)}` } },
    { name: 'GET my-route-strategies', path: '/__aisys__/api/my-route-strategies?page=1&pageSize=20&mode=all&status=all', profile: 'core', access: 'self', requiresAuth: true },
    { name: 'GET my-route-strategies/options', path: '/__aisys__/api/my-route-strategies/options?limit=50&manageableOnly=true', profile: 'core', access: 'self', requiresAuth: true },
    { name: 'GET my-route-strategy detail', path: '', profile: 'core', access: 'self', requiresAuth: true, dynamicFrom: { sourceName: 'GET my-route-strategies', buildPath: (id) => `/__aisys__/api/my-route-strategies/${encodeURIComponent(id)}` } },
    { name: 'GET my-api-keys', path: '/__aisys__/api/my-api-keys?page=1&pageSize=20&status=all', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-authorizations', path: '/__aisys__/api/my-authorizations?status=all&page=1&pageSize=20', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-authorization detail', path: '', profile: 'broad', access: 'self', requiresAuth: true, dynamicFrom: { sourceName: 'GET my-authorizations', buildPath: (id) => `/__aisys__/api/my-authorizations/${encodeURIComponent(id)}` } },
    { name: 'GET my-authorization usage', path: '', profile: 'broad', access: 'self', requiresAuth: true, dynamicFrom: { sourceName: 'GET my-authorizations', buildPath: (id) => `/__aisys__/api/my-authorizations/${encodeURIComponent(id)}/usage` } },
    { name: 'GET my-authorization-options/grantee-accounts', path: '/__aisys__/api/my-authorization-options/grantee-accounts?limit=20', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-authorization-options/grantee-teams', path: '/__aisys__/api/my-authorization-options/grantee-teams?limit=20', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-authorization-options/grantee-groups', path: '', profile: 'broad', access: 'self', requiresAuth: true, dynamicFrom: { sourceName: 'GET auth/me', buildPath: (id) => `/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=${encodeURIComponent(id)}&limit=20&providerCode=gpt&preferDefault=true` } },
    { name: 'GET my-usage-records', path: '/__aisys__/api/my-usage-records?page=1&pageSize=20&result=all', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-stats/usage-overview', path: '/__aisys__/api/my-stats/usage-overview', profile: 'core', access: 'self', requiresAuth: true },
    { name: 'GET my-stats/usage-window', path: '/__aisys__/api/my-stats/usage-window', profile: 'core', access: 'self', requiresAuth: true },
    { name: 'GET my-stats/ai-performance', path: '/__aisys__/api/my-stats/ai-performance', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-stats/ai-performance/accounts', path: '/__aisys__/api/my-stats/ai-performance/accounts?limit=20', profile: 'core', access: 'self', requiresAuth: true },
    { name: 'GET my-stats/account-usage', path: '/__aisys__/api/my-stats/account-usage?page=1&pageSize=20', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-operation-logs', path: '/__aisys__/api/my-operation-logs?page=1&pageSize=20', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-model-checks/options', path: '/__aisys__/api/my-model-checks/options', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-model-checks/runs', path: '/__aisys__/api/my-model-checks/runs?page=1&pageSize=20', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET my-teams', path: '/__aisys__/api/my-teams?page=1&pageSize=20', profile: 'broad', access: 'self', requiresAuth: true },
    { name: 'GET announcements/public', path: '/__aisys__/api/announcements/public?limit=20', profile: 'broad', access: 'self', requiresAuth: true },

    { name: 'GET settings/global', path: '/__aisys__/api/settings/global', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET settings', path: '/__aisys__/api/settings', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET providers', path: '/__aisys__/api/providers', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET response-inspection-policies', path: '/__aisys__/api/response-inspection-policies', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET accounts', path: '/__aisys__/api/accounts?page=1&pageSize=20&status=all&sorts=priority:asc', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET accounts/options', path: '/__aisys__/api/accounts/options?limit=50', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET accounts/tags', path: '/__aisys__/api/accounts/tags', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET accounts/test-tasks', path: '/__aisys__/api/accounts/test-tasks?ids=missing_task', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET account detail', path: '', profile: 'core', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET accounts', buildPath: (id) => `/__aisys__/api/accounts/${encodeURIComponent(id)}` } },
    { name: 'GET account advanced', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET accounts', buildPath: (id) => `/__aisys__/api/accounts/${encodeURIComponent(id)}/advanced` } },
    { name: 'GET account edit-basic', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET accounts', buildPath: (id) => `/__aisys__/api/accounts/${encodeURIComponent(id)}/edit-basic` } },
    { name: 'GET groups', path: '/__aisys__/api/groups?page=1&pageSize=20', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET groups/options', path: '/__aisys__/api/groups/options?limit=50', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET groups/account-options', path: '/__aisys__/api/groups/account-options?limit=50', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET group detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET groups', buildPath: (id) => `/__aisys__/api/groups/${encodeURIComponent(id)}` } },
    { name: 'GET route-strategies', path: '/__aisys__/api/route-strategies?page=1&pageSize=20&mode=all&status=all', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET route-strategies/options', path: '/__aisys__/api/route-strategies/options?limit=50&manageableOnly=true', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET route-strategy detail', path: '', profile: 'core', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET route-strategies', buildPath: (id) => `/__aisys__/api/route-strategies/${encodeURIComponent(id)}` } },
    { name: 'GET api-keys', path: '/__aisys__/api/api-keys?page=1&pageSize=20&status=all', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET authorizations', path: '/__aisys__/api/authorizations?status=all&page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET authorization detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET authorizations', buildPath: (id) => `/__aisys__/api/authorizations/${encodeURIComponent(id)}` } },
    { name: 'GET authorization usage', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET authorizations', buildPath: (id) => `/__aisys__/api/authorizations/${encodeURIComponent(id)}/usage` } },
    { name: 'GET authorization-options/grantee-accounts', path: '/__aisys__/api/authorization-options/grantee-accounts?limit=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET authorization-options/grantee-teams', path: '/__aisys__/api/authorization-options/grantee-teams?limit=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET authorization-options/grantee-groups', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET auth/me', buildPath: (id) => `/__aisys__/api/authorization-options/grantee-groups?granteeSystemAccountId=${encodeURIComponent(id)}&limit=20&providerCode=gpt&preferDefault=true` } },
    { name: 'GET usage-records', path: '/__aisys__/api/usage-records?page=1&pageSize=20&result=all', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET stats/usage-overview', path: '/__aisys__/api/stats/usage-overview', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET stats/usage-window', path: '/__aisys__/api/stats/usage-window', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET stats/ai-performance', path: '/__aisys__/api/stats/ai-performance', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET stats/ai-performance/accounts', path: '/__aisys__/api/stats/ai-performance/accounts?limit=20', profile: 'core', access: 'admin', requiresAuth: true },
    { name: 'GET stats/account-usage', path: '/__aisys__/api/stats/account-usage?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET stats/system-metrics', path: '/__aisys__/api/stats/system-metrics', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET operation-logs', path: '/__aisys__/api/operation-logs?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET operation-log detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET operation-logs', buildPath: (id) => `/__aisys__/api/operation-logs/${encodeURIComponent(id)}` } },
    { name: 'GET public-api-logs', path: '/__aisys__/api/public-api-logs?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET public-api-log detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET public-api-logs', buildPath: (id) => `/__aisys__/api/public-api-logs/${encodeURIComponent(id)}` } },
    { name: 'GET audit-logs', path: '/__aisys__/api/audit-logs?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET audit-logs/runtime', path: '/__aisys__/api/audit-logs/runtime?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET audit-logs/error-groups', path: '/__aisys__/api/audit-logs/error-groups?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET audit-log detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET audit-logs', buildPath: (id) => `/__aisys__/api/audit-logs/${encodeURIComponent(id)}` } },
    { name: 'GET audit-error-group events', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET audit-logs/error-groups', buildPath: (id) => `/__aisys__/api/audit-logs/error-groups/${encodeURIComponent(id)}/events?page=1&pageSize=20` } },
    { name: 'GET runtime-logs', path: '/__aisys__/api/runtime-logs?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET runtime-logs/facets', path: '/__aisys__/api/runtime-logs/facets', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET runtime-log detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET runtime-logs', buildPath: (id) => `/__aisys__/api/runtime-logs/${encodeURIComponent(id)}` } },
    { name: 'GET model-checks/options', path: '/__aisys__/api/model-checks/options', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET model-checks/runs', path: '/__aisys__/api/model-checks/runs?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET model-check run detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET model-checks/runs', buildPath: (id) => `/__aisys__/api/model-checks/runs/${encodeURIComponent(id)}` } },
    { name: 'GET ip-stats', path: '/__aisys__/api/ip-stats?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET ip-stats detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET ip-stats', idField: 'ipHash', buildPath: (id) => `/__aisys__/api/ip-stats/${encodeURIComponent(id)}/detail` } },
    { name: 'GET proxies', path: '/__aisys__/api/proxies?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET system-accounts', path: '/__aisys__/api/system-accounts?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET system-accounts/options', path: '/__aisys__/api/system-accounts/options?limit=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET system-teams', path: '/__aisys__/api/system-teams?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET system-team detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET system-teams', buildPath: (id) => `/__aisys__/api/system-teams/${encodeURIComponent(id)}` } },
    { name: 'GET external-integration-sources/scopes', path: '/__aisys__/api/external-integration-sources/scopes', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET external-integration-sources/api-docs', path: '/__aisys__/api/external-integration-sources/api-docs', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET external-integration-sources', path: '/__aisys__/api/external-integration-sources?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET external-integration-source detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET external-integration-sources', buildPath: (id) => `/__aisys__/api/external-integration-sources/${encodeURIComponent(id)}` } },
    { name: 'GET table-monitor/overview', path: '/__aisys__/api/table-monitor/overview', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET table-monitor/history', path: '/__aisys__/api/table-monitor/history?databaseRole=business&tableName=system_accounts&limit=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET table-monitor/database-history', path: '/__aisys__/api/table-monitor/database-history?limit=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET announcements', path: '/__aisys__/api/announcements?page=1&pageSize=20', profile: 'broad', access: 'admin', requiresAuth: true },
    { name: 'GET announcement detail', path: '', profile: 'broad', access: 'admin', requiresAuth: true, dynamicFrom: { sourceName: 'GET announcements', buildPath: (id) => `/__aisys__/api/announcements/${encodeURIComponent(id)}` } },
    { name: 'GET runtime-logs/grep', path: '/__aisys__/api/runtime-logs/grep?keyword=error&limit=20', profile: 'long-read', access: 'admin', requiresAuth: true },
    { name: 'GET audit-logs/search-hot', path: '/__aisys__/api/audit-logs/search-hot?keyword=error&limit=20', profile: 'long-read', access: 'admin', requiresAuth: true }
  ]
}

function profileIncludes(selected: BenchmarkProfile, endpointProfile: BenchmarkProfile): boolean {
  if (selected === 'long-read') return true
  if (selected === 'broad') return endpointProfile === 'core' || endpointProfile === 'broad'
  return endpointProfile === 'core'
}

function accessScopeIncludes(scope: BenchmarkAccessScope, access: EndpointAccess): boolean {
  if (scope === 'all') return true
  if (access === 'public' || access === 'auth' || access === 'shared') return true
  if (scope === 'admin') return access === 'admin' || access === 'self'
  return access === 'self'
}

function dynamicSourceNames(): Set<string> {
  return new Set(endpointCatalog().flatMap((endpoint) => endpoint.dynamicFrom?.sourceName ?? []))
}

async function runEndpointBatch(input: BenchmarkConfig, endpoints: EndpointSpec[]): Promise<void> {
  for (const endpoint of endpoints) {
    await requestEndpoint(input, endpoint)
  }
}

async function requestJson(input: BenchmarkConfig, endpoint: EndpointSpec): Promise<{ ok: boolean; data?: unknown }> {
  const result = await fetchEndpoint(input, endpoint)
  if (!result.ok || !result.text) return { ok: false }
  try {
    const parsed = JSON.parse(result.text) as ApiEnvelope
    return { ok: true, data: parsed.data }
  } catch {
    return { ok: false }
  }
}

async function requestEndpoint(input: BenchmarkConfig, endpoint: EndpointSpec): Promise<ProbeResult> {
  const startedAt = performance.now()
  try {
    const result = await fetchEndpoint(input, endpoint)
    const expectedStatuses = endpoint.expectedStatuses ?? [200]
    const ok = expectedStatuses.includes(result.status)
    return {
      endpoint: endpoint.name,
      path: endpoint.path,
      status: result.status,
      ok,
      latencyMs: round(performance.now() - startedAt),
      bytes: result.bytes,
      cacheControl: result.headers.get('cache-control') ?? undefined,
      serverTiming: result.headers.get('server-timing') ?? undefined,
      ...(ok ? {} : { error: `Unexpected HTTP ${result.status}` })
    }
  } catch (error) {
    return {
      endpoint: endpoint.name,
      path: endpoint.path,
      status: 0,
      ok: false,
      latencyMs: round(performance.now() - startedAt),
      bytes: 0,
      error: error instanceof Error ? error.message : String(error)
    }
  }
}

async function fetchEndpoint(
  input: BenchmarkConfig,
  endpoint: EndpointSpec
): Promise<{ status: number; headers: Headers; text: string; bytes: number; ok: boolean }> {
  const headers: Record<string, string> = {
    accept: 'application/json'
  }
  if (input.cookie) {
    headers.cookie = input.cookie
  }
  if (input.authHeader) {
    headers.authorization = input.authHeader
  }
  if (endpoint.requiresAuth && !input.cookie && !input.authHeader) {
    throw new Error(`Endpoint requires auth but no cookie/auth header configured: ${endpoint.name}`)
  }
  const response = await fetch(`${input.baseUrl}${endpoint.path}`, {
    headers,
    signal: AbortSignal.timeout(input.requestTimeoutMs)
  })
  const text = await response.text()
  return {
    status: response.status,
    headers: response.headers,
    text,
    bytes: Buffer.byteLength(text),
    ok: response.ok
  }
}

function buildReport(
  input: BenchmarkConfig,
  startedAt: Date,
  finishedAt: Date,
  durationMs: number,
  endpoints: EndpointSpec[],
  probes: ProbeResult[]
): BenchmarkReport {
  const endpointReports = Array.from(new Set(probes.map((probe) => probe.endpoint)))
    .map((endpoint) => summarizeEndpoint(endpoint, probes.filter((probe) => probe.endpoint === endpoint)))
    .sort((left, right) => right.p95Ms - left.p95Ms)
  const overall = summarizeEndpoint('overall', probes)
  const totalRequests = probes.length
  const errorRequests = probes.filter((probe) => !probe.ok).length
  const errorRate = totalRequests ? errorRequests / totalRequests : 0
  const violations = collectViolations(input, overall, errorRate)
  return {
    startedAt: startedAt.toISOString(),
    finishedAt: finishedAt.toISOString(),
    durationMs: round(durationMs),
    config: {
      baseUrl: input.baseUrl,
      profile: input.profile,
      accessScope: input.accessScope,
      iterations: input.iterations,
      concurrency: input.concurrency,
      requestDelayMs: input.requestDelayMs,
      warmupIterations: input.warmupIterations,
      requestTimeoutMs: input.requestTimeoutMs,
      maxAllowedP95Ms: input.maxAllowedP95Ms,
      maxAllowedErrorRate: input.maxAllowedErrorRate,
      reportPath: input.reportPath
    },
    endpoints: endpoints.map((endpoint) => ({ name: endpoint.name, path: endpoint.path, profile: endpoint.profile, access: endpoint.access })),
    totalRequests,
    okRequests: totalRequests - errorRequests,
    errorRequests,
    errorRate: round(errorRate),
    overall,
    endpointReports,
    failures: probes.filter((probe) => !probe.ok),
    pass: violations.length === 0,
    violations
  }
}

function summarizeEndpoint(endpoint: string, probes: ProbeResult[]): EndpointReport {
  const latencies = probes.map((probe) => probe.latencyMs).sort((left, right) => left - right)
  const statuses: Record<string, number> = {}
  for (const probe of probes) {
    const key = String(probe.status)
    statuses[key] = (statuses[key] ?? 0) + 1
  }
  return {
    endpoint,
    path: probes[0]?.path ?? '',
    count: probes.length,
    ok: probes.filter((probe) => probe.ok).length,
    errors: probes.filter((probe) => !probe.ok).length,
    p50Ms: percentile(latencies, 0.50),
    p95Ms: percentile(latencies, 0.95),
    p99Ms: percentile(latencies, 0.99),
    maxMs: percentile(latencies, 1),
    bytesAvg: round(probes.reduce((sum, probe) => sum + probe.bytes, 0) / Math.max(probes.length, 1)),
    statuses,
    serverTiming: summarizeServerTiming(probes)
  }
}

function summarizeServerTiming(probes: ProbeResult[]): Record<string, ServerTimingSummary> {
  const byMetric = new Map<string, number[]>()
  for (const probe of probes) {
    for (const metric of parseServerTiming(probe.serverTiming)) {
      const samples = byMetric.get(metric.name) ?? []
      samples.push(metric.durationMs)
      byMetric.set(metric.name, samples)
    }
  }
  const summary: Record<string, ServerTimingSummary> = {}
  for (const [name, samples] of byMetric) {
    const sorted = samples.sort((left, right) => left - right)
    summary[name] = {
      count: sorted.length,
      p50Ms: percentile(sorted, 0.50),
      p95Ms: percentile(sorted, 0.95),
      maxMs: percentile(sorted, 1)
    }
  }
  return summary
}

function parseServerTiming(value: string | undefined): Array<{ name: string; durationMs: number }> {
  if (!value) return []
  return value.split(',').flatMap((item) => {
    const [rawName, ...parts] = item.trim().split(';')
    const name = rawName?.trim()
    const dur = parts.map((part) => part.trim()).find((part) => part.startsWith('dur='))?.slice(4)
    const durationMs = Number(dur)
    if (!name || !Number.isFinite(durationMs)) return []
    return [{ name, durationMs }]
  })
}

function collectViolations(input: BenchmarkConfig, overall: EndpointReport, errorRate: number): string[] {
  const violations: string[] = []
  if (errorRate > input.maxAllowedErrorRate) {
    violations.push(`HTTP error rate ${round(errorRate * 100)}% > ${round(input.maxAllowedErrorRate * 100)}%`)
  }
  if (overall.p95Ms > input.maxAllowedP95Ms) {
    violations.push(`Overall p95 ${overall.p95Ms}ms > ${input.maxAllowedP95Ms}ms`)
  }
  return violations
}

function firstFieldValue(data: unknown, field: string): string | undefined {
  const queue: unknown[] = [data]
  const seen = new Set<unknown>()
  while (queue.length) {
    const item = queue.shift()
    if (!item || seen.has(item)) continue
    seen.add(item)
    if (Array.isArray(item)) {
      queue.push(...item.slice(0, 20))
      continue
    }
    if (typeof item === 'object') {
      const record = item as Record<string, unknown>
      const value = record[field]
      if (typeof value === 'string' && value.trim()) {
        return value
      }
      if (Array.isArray(record.items)) {
        queue.push(record.items)
      }
      if (Array.isArray(record.data)) {
        queue.push(record.data)
      }
    }
  }
  return undefined
}

function uniqueEndpoints(endpoints: EndpointSpec[]): EndpointSpec[] {
  const seen = new Set<string>()
  return endpoints.filter((endpoint) => {
    const key = `${endpoint.name}\n${endpoint.path}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function outputReport(report: BenchmarkReport): void {
  mkdirSync(dirname(report.config.reportPath), { recursive: true })
  writeFileSync(report.config.reportPath, `${JSON.stringify(report, null, 2)}\n`, 'utf8')
}

function printSummary(report: BenchmarkReport): void {
  console.log(`production-system-api-read-benchmark ${report.pass ? 'passed' : 'failed'}`)
  console.log(`report=${report.config.reportPath}`)
  console.log(`requests=${report.totalRequests} ok=${report.okRequests} errors=${report.errorRequests} p95=${report.overall.p95Ms}ms max=${report.overall.maxMs}ms`)
  for (const item of report.endpointReports.slice(0, 15)) {
    console.log(`${item.endpoint} p50=${item.p50Ms}ms p95=${item.p95Ms}ms max=${item.maxMs}ms statuses=${JSON.stringify(item.statuses)}`)
  }
  if (report.violations.length) {
    console.log(`violations=${report.violations.join(' | ')}`)
  }
}

function loadConfig(): BenchmarkConfig {
  const baseUrl = normalizeBaseUrl(process.env.JUHE_PRODUCTION_API_BENCHMARK_BASE_URL)
    ?? normalizeBaseUrl(argValue('--base-url'))
    ?? 'https://aijh.huanmin.top'
  const timestamp = new Date().toISOString().replace(/[:.]/g, '-')
  const reportPath = resolve(
    process.env.JUHE_PRODUCTION_API_BENCHMARK_REPORT_PATH
      ?? argValue('--report')
      ?? `../reports/production-system-api-read-benchmark-${timestamp}.json`
  )
  return {
    baseUrl,
    profile: profileValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_PROFILE ?? argValue('--profile')),
    accessScope: accessScopeValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_ACCESS_SCOPE ?? argValue('--access-scope')),
    iterations: intValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_ITERATIONS ?? argValue('--iterations'), 3, 1, 100),
    concurrency: intValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_CONCURRENCY ?? argValue('--concurrency'), 4, 1, 32),
    requestDelayMs: intValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_REQUEST_DELAY_MS ?? argValue('--request-delay-ms'), 0, 0, 60_000),
    warmupIterations: intValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_WARMUP_ITERATIONS ?? argValue('--warmup'), 1, 0, 10),
    requestTimeoutMs: intValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_TIMEOUT_MS ?? argValue('--timeout-ms'), 15_000, 500, 120_000),
    maxAllowedP95Ms: numberValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_MAX_P95_MS ?? argValue('--max-p95-ms'), 2500, 1, 120_000),
    maxAllowedErrorRate: numberValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_MAX_ERROR_RATE ?? argValue('--max-error-rate'), 0, 0, 1),
    reportPath,
    cookie: textValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_COOKIE),
    authHeader: textValue(process.env.JUHE_PRODUCTION_API_BENCHMARK_AUTH_HEADER)
  }
}

function argValue(name: string): string | undefined {
  const inline = process.argv.find((arg) => arg.startsWith(`${name}=`))
  if (inline) return inline.slice(name.length + 1)
  const index = process.argv.indexOf(name)
  if (index >= 0) return process.argv[index + 1]
  return undefined
}

function profileValue(value: string | undefined): BenchmarkProfile {
  if (value === 'core' || value === 'broad' || value === 'long-read') return value
  return 'broad'
}

function accessScopeValue(value: string | undefined): BenchmarkAccessScope {
  if (value === 'self' || value === 'admin' || value === 'all') return value
  return 'self'
}

function intValue(value: string | undefined, fallback: number, min: number, max: number): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, Math.trunc(parsed)))
}

function numberValue(value: string | undefined, fallback: number, min: number, max: number): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, parsed))
}

function textValue(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  return normalized || undefined
}

function normalizeBaseUrl(value: string | undefined): string | undefined {
  const normalized = value?.trim().replace(/\/+$/, '')
  return normalized || undefined
}

function percentile(sortedSamples: number[], value: number): number {
  if (!sortedSamples.length) return 0
  const index = Math.min(sortedSamples.length - 1, Math.max(0, Math.ceil(sortedSamples.length * value) - 1))
  return round(sortedSamples[index] ?? 0)
}

function round(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.round(value * 100) / 100
}

function delay(ms: number): Promise<void> {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, ms))
}

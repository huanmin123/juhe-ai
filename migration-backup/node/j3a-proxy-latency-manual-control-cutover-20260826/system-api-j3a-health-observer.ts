// 从 backend/src/modules/system-api/system-api-app.ts 删除的 J3a Node→Go 健康探测。
// 归档仅用于追溯，禁止重新接回运行路径。
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

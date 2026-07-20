import { normalizeFrontendBuildId } from '@/shared/frontendBuildId'

export type FrontendBuildStatus = 'changed' | 'same' | 'unknown'

interface LoadRemoteFrontendBuildIdOptions {
  baseUrl?: string
  fetcher?: typeof fetch
  now?: () => number
  timeoutMs?: number
}

export async function loadRemoteFrontendBuildId(
  options: LoadRemoteFrontendBuildIdOptions = {}
): Promise<string | undefined> {
  const baseUrl = options.baseUrl ?? import.meta.env.BASE_URL
  const normalizedBaseUrl = baseUrl.endsWith('/') ? baseUrl : `${baseUrl}/`
  const now = options.now ?? Date.now
  const fetcher = options.fetcher ?? fetch
  const timeoutMs = options.timeoutMs ?? 1500

  try {
    const response = await fetcher(`${normalizedBaseUrl}build-info.json?t=${now()}`, {
      cache: 'no-store',
      signal: AbortSignal.timeout(timeoutMs)
    })
    if (!response.ok) return undefined
    const payload = await response.json() as { buildId?: unknown }
    return normalizeFrontendBuildId(payload.buildId)
  } catch {
    return undefined
  }
}

export async function classifyFrontendBuild(
  currentBuildId: unknown,
  loadRemoteBuildId: () => Promise<unknown>
): Promise<FrontendBuildStatus> {
  const normalizedCurrentBuildId = normalizeFrontendBuildId(currentBuildId)
  if (!normalizedCurrentBuildId) return 'unknown'

  try {
    const remoteBuildId = normalizeFrontendBuildId(await loadRemoteBuildId())
    if (!remoteBuildId) return 'unknown'
    return remoteBuildId === normalizedCurrentBuildId ? 'same' : 'changed'
  } catch {
    return 'unknown'
  }
}

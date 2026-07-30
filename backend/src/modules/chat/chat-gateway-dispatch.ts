import { runtimeConfig } from '../../config/runtime.js'
import { listInternalGatewayEndpoints } from '../gateway/runtime/internal-gateway-registry.js'

export type ChatGatewayDispatch = (path: string, init: RequestInit) => Promise<Response>

export class ChatGatewayUnavailableError extends Error {
  constructor() {
    super('当前没有可用的内部 Gateway，请稍后重试')
    this.name = 'ChatGatewayUnavailableError'
  }
}

let nextGatewayOffset = 0
let listGatewayEndpointsForTest: typeof listInternalGatewayEndpoints | undefined
let endpointCache: { endpoints: Awaited<ReturnType<typeof listInternalGatewayEndpoints>>; expiresAtMs: number } | undefined
let endpointRefreshPromise: Promise<Awaited<ReturnType<typeof listInternalGatewayEndpoints>>> | undefined
const endpointCacheTtlMs = 1_000
const activeRequestsByOrigin = new Map<string, number>()
const failedGatewayCooldownMs = 5_000
const failedGatewayOrigins = new Map<string, number>()

export const dispatchChatGatewayRequest: ChatGatewayDispatch = async (path, init) => {
  const { origin, release } = await reserveChatGatewayRequest()
  try {
    const response = await fetch(`${origin}${normalizePath(path)}`, init)
    return trackGatewayResponse(response, release)
  } catch (error) {
    release()
    if (!isAbortError(error)) markGatewayTransportFailure(origin)
    throw error
  }
}

export async function resolveChatGatewayOrigin(): Promise<string> {
  const endpoints = await resolveChatGatewayEndpoints()
  return selectChatGatewayOrigin(endpoints)
}

async function reserveChatGatewayRequest(): Promise<{ origin: string; release: () => void }> {
  const endpoints = await resolveChatGatewayEndpoints()
  const origin = selectChatGatewayOrigin(endpoints)
  return { origin, release: reserveGatewayRequest(origin) }
}

async function resolveChatGatewayEndpoints(): Promise<Awaited<ReturnType<typeof listInternalGatewayEndpoints>>> {
  if (!requiresDiscoveredGateway()) {
    return [{ instanceId: 'local', origin: `http://127.0.0.1:${runtimeConfig.port}` }]
  }
  let endpoints = cachedEndpoints()
  try {
    if (!endpoints) {
      endpoints = await refreshCachedEndpoints()
    }
  } catch {
    throw new ChatGatewayUnavailableError()
  }
  if (!endpoints.length) throw new ChatGatewayUnavailableError()
  return endpoints
}

function selectChatGatewayOrigin(endpoints: Awaited<ReturnType<typeof listInternalGatewayEndpoints>>): string {
  const nowMs = Date.now()
  const availableEndpoints = endpoints.filter((endpoint) => (failedGatewayOrigins.get(endpoint.origin) ?? 0) <= nowMs)
  const eligibleEndpoints = availableEndpoints.length ? availableEndpoints : endpoints
  const lowestInFlight = Math.min(...eligibleEndpoints.map((endpoint) => activeRequestsByOrigin.get(endpoint.origin) ?? 0))
  const candidates = eligibleEndpoints.filter((endpoint) => (activeRequestsByOrigin.get(endpoint.origin) ?? 0) === lowestInFlight)
  const endpoint = candidates[nextGatewayOffset % candidates.length]
  nextGatewayOffset = (nextGatewayOffset + 1) % Number.MAX_SAFE_INTEGER
  return endpoint.origin
}

export function setChatGatewayEndpointResolverForTest(
  resolver?: typeof listInternalGatewayEndpoints
): void {
  listGatewayEndpointsForTest = resolver
  nextGatewayOffset = 0
  endpointCache = undefined
  endpointRefreshPromise = undefined
  activeRequestsByOrigin.clear()
  failedGatewayOrigins.clear()
}

function markGatewayTransportFailure(origin: string): void {
  failedGatewayOrigins.set(origin, Date.now() + failedGatewayCooldownMs)
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}

function cachedEndpoints(): Awaited<ReturnType<typeof listInternalGatewayEndpoints>> | undefined {
  if (!endpointCache || endpointCache.expiresAtMs <= Date.now()) return undefined
  return endpointCache.endpoints
}

async function refreshCachedEndpoints(): Promise<Awaited<ReturnType<typeof listInternalGatewayEndpoints>>> {
  if (endpointRefreshPromise) return await endpointRefreshPromise
  const refreshPromise = (async () => {
    try {
      const endpoints = await (listGatewayEndpointsForTest ?? listInternalGatewayEndpoints)()
      endpointCache = { endpoints, expiresAtMs: Date.now() + endpointCacheTtlMs }
      return endpoints
    } finally {
      endpointRefreshPromise = undefined
    }
  })()
  endpointRefreshPromise = refreshPromise
  return await refreshPromise
}

function requiresDiscoveredGateway(): boolean {
  return runtimeConfig.runtimeMode === 'performance'
    && runtimeConfig.performanceNodeRole === 'control'
    && runtimeConfig.processRole === 'db-service'
}

function normalizePath(path: string): string {
  const normalized = path.trim()
  if (!normalized.startsWith('/')) throw new Error('内部 Gateway 路径必须以 / 开头')
  return normalized
}

function reserveGatewayRequest(origin: string): () => void {
  activeRequestsByOrigin.set(origin, (activeRequestsByOrigin.get(origin) ?? 0) + 1)
  let released = false
  return () => {
    if (released) return
    released = true
    const next = (activeRequestsByOrigin.get(origin) ?? 1) - 1
    if (next > 0) activeRequestsByOrigin.set(origin, next)
    else activeRequestsByOrigin.delete(origin)
  }
}

function trackGatewayResponse(response: Response, release: () => void): Response {
  if (!response.body) {
    release()
    return response
  }
  const reader = response.body.getReader()
  const body = new ReadableStream<Uint8Array>({
    async pull(controller) {
      try {
        const chunk = await reader.read()
        if (chunk.done) {
          release()
          controller.close()
          return
        }
        controller.enqueue(chunk.value)
      } catch (error) {
        release()
        controller.error(error)
      }
    },
    async cancel(reason) {
      try {
        await reader.cancel(reason)
      } finally {
        release()
      }
    }
  })
  return new Response(body, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers
  })
}

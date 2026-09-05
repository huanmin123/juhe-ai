import type { GatewaySessionIdentityRequest } from './types.js'

export function normalizedGatewaySessionRequestPath(request: GatewaySessionIdentityRequest): string {
  const endpoint = request.originalUrl || request.path || ''
  const path = endpoint.split('?', 1)[0]?.trim().toLowerCase() ?? ''
  if (!path) return '/'
  const normalized = path.replace(/\/+$/, '').replace(/^\/v1(?=\/|internal:|$)/, '')
  if (!normalized) return '/'
  return normalized.startsWith('/') ? normalized : `/${normalized}`
}

export function gatewaySessionHeaderValues(request: GatewaySessionIdentityRequest, name: string): string[] {
  const normalizedName = name.toLowerCase()
  const distinct = request.headersDistinct?.[normalizedName]
    ?? Object.entries(request.headersDistinct ?? {}).find(([key]) => key.toLowerCase() === normalizedName)?.[1]
  if (distinct?.length) {
    return distinct.map((value) => String(value))
  }
  const rawValues: string[] = []
  for (let index = 0; index + 1 < (request.rawHeaders?.length ?? 0); index += 2) {
    if (request.rawHeaders?.[index]?.toLowerCase() === normalizedName) {
      rawValues.push(String(request.rawHeaders[index + 1]))
    }
  }
  if (rawValues.length) return rawValues
  const direct = request.headers?.[normalizedName]
    ?? Object.entries(request.headers ?? {}).find(([key]) => key.toLowerCase() === normalizedName)?.[1]
  if (Array.isArray(direct)) {
    return direct.map((value) => String(value))
  }
  if (typeof direct === 'string') {
    return [direct]
  }
  const value = request.header?.(name)
  return typeof value === 'string' ? [value] : []
}

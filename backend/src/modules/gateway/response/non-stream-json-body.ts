export type GatewayNonStreamJsonBody =
  | { status: 'valid'; value: unknown }
  | { status: 'empty' | 'not_json' | 'invalid' }

export const gatewayNonStreamJsonBodyReceiver = Symbol('gatewayNonStreamJsonBodyReceiver')

export interface GatewayNonStreamJsonBodyReceiver {
  [gatewayNonStreamJsonBodyReceiver]?: (body: GatewayNonStreamJsonBody) => void
}

export function publishGatewayNonStreamJsonBody(
  response: object,
  body: GatewayNonStreamJsonBody | undefined
): void {
  if (body) {
    const receiver = response as GatewayNonStreamJsonBodyReceiver
    receiver[gatewayNonStreamJsonBodyReceiver]?.(body)
  }
}

export function parseGatewayNonStreamJsonBody(
  bodyText: string | undefined,
  headers?: Headers
): GatewayNonStreamJsonBody {
  const trimmed = bodyText?.trim()
  if (!trimmed) return { status: 'empty' }

  const contentType = headers?.get('content-type')?.toLowerCase() ?? ''
  if (!contentType.includes('json') && trimmed[0] !== '{' && trimmed[0] !== '[') {
    return { status: 'not_json' }
  }

  try {
    return { status: 'valid', value: JSON.parse(trimmed) as unknown }
  } catch {
    return { status: 'invalid' }
  }
}

export function gatewayNonStreamJsonBodyFromValue(value: unknown): GatewayNonStreamJsonBody {
  return { status: 'valid', value }
}

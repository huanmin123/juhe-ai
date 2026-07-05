export class GatewayFirstByteTimeoutError extends Error {
  readonly code = 'first_byte_timeout'

  constructor(
    message: string,
    readonly timeoutMs: number
  ) {
    super(message)
    this.name = 'GatewayFirstByteTimeoutError'
  }
}

export function isGatewayFirstByteTimeoutError(error: unknown): error is GatewayFirstByteTimeoutError {
  return error instanceof GatewayFirstByteTimeoutError
}

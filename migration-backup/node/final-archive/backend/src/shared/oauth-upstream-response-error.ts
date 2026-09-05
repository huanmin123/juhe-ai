export class OAuthUpstreamResponseError extends Error {
  constructor(message: string, readonly statusCode?: number) {
    super(message)
    this.name = 'OAuthUpstreamResponseError'
  }
}

export function isOAuthUpstreamResponseError(error: unknown): error is OAuthUpstreamResponseError {
  return error instanceof OAuthUpstreamResponseError
}

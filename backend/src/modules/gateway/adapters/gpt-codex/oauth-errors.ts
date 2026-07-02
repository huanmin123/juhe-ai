export class OpenAIOAuthCodexAdapterError extends Error {
  readonly statusCode: number
  readonly type: string
  readonly code: string
  readonly accountScoped: boolean

  constructor(
    message: string,
    code = 'invalid_openai_oauth_codex_request',
    options: { statusCode?: number; type?: string; accountScoped?: boolean } = {}
  ) {
    super(message)
    this.code = code
    this.statusCode = options.statusCode ?? 400
    this.type = options.type ?? 'invalid_request_error'
    this.accountScoped = options.accountScoped === true
  }
}

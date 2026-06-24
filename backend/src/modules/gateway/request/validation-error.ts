export class GatewayRequestValidationError extends Error {
  readonly statusCode: number
  readonly type: string
  readonly code: string
  readonly accountScoped: boolean

  constructor(
    message: string,
    code = 'invalid_gateway_request',
    options: { statusCode?: number; type?: string; accountScoped?: boolean } = {}
  ) {
    super(message)
    this.code = code
    this.statusCode = options.statusCode ?? 400
    this.type = options.type ?? 'invalid_request_error'
    this.accountScoped = options.accountScoped === true
  }
}

export type GatewayAgentGuidanceProtocol = 'chat_completions' | 'responses'

export class GatewayAgentGuidanceResponse extends Error {
  readonly statusCode = 200
  readonly type = 'agent_guidance'
  readonly code: string
  readonly accountScoped = false
  readonly protocol: GatewayAgentGuidanceProtocol
  readonly stream: boolean
  readonly model: string

  constructor(input: {
    message: string
    code: string
    protocol: GatewayAgentGuidanceProtocol
    stream: boolean
    model: string
  }) {
    super(input.message)
    this.code = input.code
    this.protocol = input.protocol
    this.stream = input.stream
    this.model = input.model
  }
}

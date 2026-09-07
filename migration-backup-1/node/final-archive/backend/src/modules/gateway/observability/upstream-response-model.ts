import { StringDecoder } from 'node:string_decoder'

export type UpstreamResponseModelProtocol = 'openai' | 'anthropic' | 'gemini'

export interface UpstreamResponseModelObservation {
  readonly protocol: UpstreamResponseModelProtocol
  readonly model?: string
  readonly conflict: boolean
  observe(chunk: Uint8Array): void
  finish(): void
}

interface UpstreamResponseModelObserverOptions {
  protocol: UpstreamResponseModelProtocol
  sse: boolean
}

const maxObservedModelLength = 200
const maxSseEventBytes = 256 * 1024
const maxJsonResponseBytes = 1024 * 1024

export function createUpstreamResponseModelObservation(
  options: UpstreamResponseModelObserverOptions
): UpstreamResponseModelObservation {
  return new RawUpstreamResponseModelObserver(options)
}

export function observeUpstreamResponseModelBody(
  body: AsyncIterable<Uint8Array>,
  observation: UpstreamResponseModelObservation
): AsyncIterable<Uint8Array> {
  return {
    async *[Symbol.asyncIterator](): AsyncGenerator<Uint8Array> {
      for await (const chunk of body) {
        observation.observe(chunk)
        yield chunk
      }
      observation.finish()
    }
  }
}

export function upstreamResponseModelProtocolForRequest(input: {
  headers: Headers
  upstreamUrl: string
  providerCode?: string
  protocolCode?: string
}): UpstreamResponseModelProtocol {
  const protocolCode = input.protocolCode?.trim().toLowerCase() ?? ''
  if (protocolCode.includes('anthropic')) return 'anthropic'
  if (protocolCode.includes('gemini')) return 'gemini'
  if (protocolCode.includes('openai')) return 'openai'
  if (input.headers.has('anthropic-version')) return 'anthropic'
  if (isGoogleOpenAICompatibleUpstreamUrl(input.upstreamUrl)) return 'openai'
  if (
    input.headers.has('x-goog-api-key')
    || input.headers.has('x-goog-user-project')
    || isGeminiNativeUpstreamUrl(input.upstreamUrl)
  ) {
    return 'gemini'
  }
  const providerCode = input.providerCode?.trim().toLowerCase() ?? ''
  if (providerCode === 'anthropic') return 'anthropic'
  if (providerCode === 'gemini') return 'gemini'
  return 'openai'
}

class RawUpstreamResponseModelObserver implements UpstreamResponseModelObservation {
  readonly protocol: UpstreamResponseModelProtocol
  private readonly sse: boolean
  private readonly decoder = new StringDecoder('utf8')
  private firstModel?: string
  private terminalModel?: string
  private completed = false
  private pendingLine = ''
  private eventName?: string
  private dataLines: string[] = []
  private dataBytes = 0
  private eventOversized = false
  private jsonChunks: Buffer[] = []
  private jsonBytes = 0
  private jsonOversized = false
  conflict = false

  constructor(options: UpstreamResponseModelObserverOptions) {
    this.protocol = options.protocol
    this.sse = options.sse
  }

  get model(): string | undefined {
    return this.terminalModel ?? this.firstModel
  }

  observe(chunk: Uint8Array): void {
    if (this.completed || chunk.byteLength === 0) return
    try {
      if (this.sse) {
        this.observeSseText(this.decoder.write(Buffer.from(chunk)))
        return
      }
      this.observeJsonChunk(chunk)
    } catch {
      // Response-model audit is observational. Parsing must not affect forwarding.
    }
  }

  finish(): void {
    if (this.completed) return
    this.completed = true
    try {
      if (this.sse) {
        const trailing = this.decoder.end()
        if (trailing) this.observeSseText(trailing)
        if (this.pendingLine) {
          this.consumeSseLine(this.pendingLine)
          this.pendingLine = ''
        }
        this.consumeSseEvent()
        return
      }
      this.finishJsonObservation()
    } catch {
      // The payload remains available to the existing response handling path.
    } finally {
      this.jsonChunks = []
    }
  }

  private observeJsonChunk(chunk: Uint8Array): void {
    if (this.jsonOversized) return
    this.jsonBytes += chunk.byteLength
    if (this.jsonBytes > maxJsonResponseBytes) {
      this.jsonOversized = true
      this.jsonChunks = []
      return
    }
    this.jsonChunks.push(Buffer.from(chunk))
  }

  private finishJsonObservation(): void {
    if (this.jsonOversized || this.jsonChunks.length === 0) return
    this.observeJsonPayload(Buffer.concat(this.jsonChunks))
  }

  private observeSseText(text: string): void {
    if (!text) return
    this.pendingLine += text
    while (true) {
      const lineBreak = this.pendingLine.indexOf('\n')
      if (lineBreak < 0) {
        if (Buffer.byteLength(this.pendingLine, 'utf8') > maxSseEventBytes) {
          this.pendingLine = ''
          this.resetSseEvent(true)
        }
        return
      }
      const line = this.pendingLine.slice(0, lineBreak).replace(/\r$/, '')
      this.pendingLine = this.pendingLine.slice(lineBreak + 1)
      this.consumeSseLine(line)
    }
  }

  private consumeSseLine(line: string): void {
    if (line.length === 0) {
      this.consumeSseEvent()
      return
    }
    if (line.startsWith('event:')) {
      this.eventName = line.slice('event:'.length).trim()
      return
    }
    if (!line.startsWith('data:') || this.eventOversized) return
    const data = line.slice('data:'.length).replace(/^\s/, '')
    this.dataBytes += Buffer.byteLength(data, 'utf8')
    if (this.dataBytes > maxSseEventBytes) {
      this.resetSseEvent(true)
      return
    }
    this.dataLines.push(data)
  }

  private consumeSseEvent(): void {
    if (!this.eventOversized && this.dataLines.length > 0) {
      this.observeJsonPayload(Buffer.from(this.dataLines.join('\n'), 'utf8'), this.eventName)
    }
    this.resetSseEvent(false)
  }

  private resetSseEvent(oversized: boolean): void {
    this.eventName = undefined
    this.dataLines = []
    this.dataBytes = 0
    this.eventOversized = oversized
  }

  private observeJsonPayload(payload: Buffer, eventName?: string): void {
    let value: unknown
    try {
      value = JSON.parse(payload.toString('utf8'))
    } catch {
      return
    }
    if (!isRecord(value)) return
    const model = this.modelFromPayload(value)
    if (!model) return
    this.observeModel(model, this.isTerminalEvent(value, eventName))
  }

  private modelFromPayload(value: Record<string, unknown>): string | undefined {
    if (this.protocol === 'anthropic') {
      return modelText(recordValue(value.message)?.model) ?? modelText(value.model)
    }
    if (this.protocol === 'gemini') {
      return modelText(value.modelVersion) ?? modelText(recordValue(value.response)?.modelVersion)
    }
    return modelText(recordValue(value.response)?.model) ?? modelText(value.model)
  }

  private isTerminalEvent(value: Record<string, unknown>, eventName?: string): boolean {
    if (this.protocol === 'gemini') return true
    if (this.protocol !== 'openai') return false
    const eventType = eventName?.trim() || modelText(value.type)
    return eventType === 'response.completed'
      || eventType === 'response.done'
      || eventType === 'response.failed'
      || eventType === 'response.incomplete'
      || eventType === 'response.cancelled'
      || eventType === 'response.canceled'
  }

  private observeModel(model: string, terminal: boolean): void {
    const current = this.model
    if (current && current !== model) {
      this.conflict = true
    }
    if (terminal) {
      this.terminalModel = model
      return
    }
    if (!this.firstModel) this.firstModel = model
  }
}

function modelText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const model = value.trim()
  return model && Array.from(model).length <= maxObservedModelLength ? model : undefined
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return isRecord(value) ? value : undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isGoogleOpenAICompatibleUpstreamUrl(value: string): boolean {
  try {
    const url = new URL(value)
    const hostname = url.hostname.toLowerCase()
    if (hostname !== 'generativelanguage.googleapis.com' && !hostname.endsWith('.googleapis.com')) {
      return false
    }
    return /(^|\/)openai(?:\/|$)/i.test(url.pathname)
  } catch {
    return false
  }
}

function isGeminiNativeUpstreamUrl(value: string): boolean {
  try {
    const hostname = new URL(value).hostname.toLowerCase()
    return hostname === 'generativelanguage.googleapis.com'
      || (hostname.endsWith('.googleapis.com') && hostname.includes('cloudcode'))
  } catch {
    return false
  }
}

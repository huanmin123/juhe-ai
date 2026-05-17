import type { Request } from 'express'

export const gatewayJsonBodyLargeWarningBytes = 2 * 1024 * 1024

export type GatewayJsonBodyParseStatus =
  | 'empty'
  | 'not_json'
  | 'parsed'
  | 'deferred_large_json'
  | 'invalid_json'

export interface GatewayRequestBodyState {
  rawBodyBytes: number
  contentType: string
  isJson: boolean
  jsonParseStatus: GatewayJsonBodyParseStatus
  jsonParseWarningBytes: number
  model?: string
  stream?: boolean
}

export type GatewayRawBodyRequest = Request & {
  rawBody?: Buffer
  gatewayRequestBody?: GatewayRequestBodyState
}

export function isGatewayJsonContentType(contentType: unknown): boolean {
  return String(contentType ?? '').toLowerCase().includes('json')
}

export function createGatewayRequestBodyState(input: {
  rawBody: Buffer
  contentType: unknown
  jsonParseStatus: GatewayJsonBodyParseStatus
  parsedBody?: unknown
  model?: string
  stream?: boolean
}): GatewayRequestBodyState {
  const contentType = String(input.contentType ?? '')
  const parsedBody = typeof input.parsedBody === 'object' && input.parsedBody !== null
    ? input.parsedBody as Record<string, unknown>
    : undefined
  return {
    rawBodyBytes: input.rawBody.length,
    contentType,
    isJson: isGatewayJsonContentType(contentType),
    jsonParseStatus: input.jsonParseStatus,
    jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
    model: input.model ?? (typeof parsedBody?.model === 'string' ? parsedBody.model : undefined),
    stream: input.stream ?? (typeof parsedBody?.stream === 'boolean' ? parsedBody.stream : undefined)
  }
}

export function getGatewayRequestBodyState(req: Request): GatewayRequestBodyState | undefined {
  return (req as GatewayRawBodyRequest).gatewayRequestBody
}

export function buildGatewayRequestBodySummary(req: Request): Record<string, unknown> | undefined {
  const state = getGatewayRequestBodyState(req)
  if (!state || state.rawBodyBytes <= state.jsonParseWarningBytes) {
    return undefined
  }
  return {
    _gatewayBody: {
      rawBodyBytes: state.rawBodyBytes,
      contentType: state.contentType,
      jsonParseStatus: state.jsonParseStatus,
      jsonParseWarningBytes: state.jsonParseWarningBytes,
      model: state.model ?? (typeof req.body?.model === 'string' ? req.body.model : undefined),
      stream: state.stream ?? (typeof req.body?.stream === 'boolean' ? req.body.stream : undefined)
    }
  }
}

export function extractGatewayJsonBodyMetadata(rawBody: Buffer): { model?: string; stream?: boolean } {
  const text = rawBody.toString('utf8', 0, Math.min(rawBody.length, gatewayLargeJsonMetadataPrefixBytes))
  let index = skipJsonWhitespace(text, 0)
  if (text[index] !== '{') {
    return {}
  }
  index += 1

  let model: string | undefined
  let stream: boolean | undefined
  while (index < text.length && (model === undefined || stream === undefined)) {
    index = skipJsonWhitespace(text, index)
    if (text[index] === ',') {
      index += 1
      continue
    }
    if (text[index] === '}') {
      break
    }

    const key = readJsonStringToken(text, index)
    if (!key) {
      break
    }
    index = skipJsonWhitespace(text, key.nextIndex)
    if (text[index] !== ':') {
      break
    }
    index = skipJsonWhitespace(text, index + 1)

    if (key.value === 'model') {
      const value = readJsonStringToken(text, index)
      if (value) {
        model = value.value
        index = value.nextIndex
        continue
      }
    } else if (key.value === 'stream') {
      if (text.startsWith('true', index)) {
        stream = true
        index += 4
        continue
      }
      if (text.startsWith('false', index)) {
        stream = false
        index += 5
        continue
      }
    }

    index = skipJsonValue(text, index)
  }

  return { model, stream }
}

function skipJsonWhitespace(text: string, index: number): number {
  while (index < text.length && /\s/.test(text[index])) {
    index += 1
  }
  return index
}

function readJsonStringToken(text: string, index: number): { value: string; nextIndex: number } | undefined {
  if (text[index] !== '"') {
    return undefined
  }
  let escaped = false
  for (let cursor = index + 1; cursor < text.length; cursor += 1) {
    const char = text[cursor]
    if (escaped) {
      escaped = false
      continue
    }
    if (char === '\\') {
      escaped = true
      continue
    }
    if (char === '"') {
      try {
        const value = JSON.parse(text.slice(index, cursor + 1)) as unknown
        return typeof value === 'string' ? { value, nextIndex: cursor + 1 } : undefined
      } catch {
        return undefined
      }
    }
  }
  return undefined
}

function skipJsonValue(text: string, index: number): number {
  index = skipJsonWhitespace(text, index)
  if (text[index] === '"') {
    return readJsonStringToken(text, index)?.nextIndex ?? text.length
  }
  if (text[index] !== '{' && text[index] !== '[') {
    while (index < text.length && text[index] !== ',' && text[index] !== '}') {
      index += 1
    }
    return index
  }

  const stack = [text[index]]
  let escaped = false
  let inString = false
  for (let cursor = index + 1; cursor < text.length; cursor += 1) {
    const char = text[cursor]
    if (inString) {
      if (escaped) {
        escaped = false
      } else if (char === '\\') {
        escaped = true
      } else if (char === '"') {
        inString = false
      }
      continue
    }
    if (char === '"') {
      inString = true
      continue
    }
    if (char === '{' || char === '[') {
      stack.push(char)
      continue
    }
    if (char === '}' || char === ']') {
      stack.pop()
      if (stack.length === 0) {
        return cursor + 1
      }
    }
  }
  return text.length
}

const gatewayLargeJsonMetadataPrefixBytes = 256 * 1024

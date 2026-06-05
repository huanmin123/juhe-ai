import type { Request } from 'express'

export const gatewayJsonBodyLargeWarningBytes = 2 * 1024 * 1024
export const gatewayJsonBodyInlineParseMaxBytes = 256 * 1024
export const gatewayRawBodyHardLimitBytes = 8 * 1024 * 1024
export const gatewayRawBodyHardLimit = '8mb'

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
  imageGeneration?: boolean
  imageGenerationForced?: boolean
}

export type GatewayRawBodyRequest = Request & {
  rawBody?: Buffer
  gatewayRequestBody?: GatewayRequestBodyState
  gatewayParsedJsonBodyAvailable?: boolean
  gatewayParsedJsonBody?: unknown
  gatewayUpstreamBodyCache?: {
    passthrough?: { body: Buffer | undefined }
  }
}

export interface GatewayImageGenerationToolDowngradeResult {
  downgraded: boolean
  removedToolCount: number
  reason?:
    | 'auto_image_generation_tool_removed'
    | 'not_json_object'
    | 'no_auto_image_generation_tool'
    | 'forced_image_generation_tool'
    | 'invalid_json'
    | 'image_endpoint_or_model'
    | 'json_worker_overloaded'
}

interface ImageGenerationToolInspection {
  imageToolCount: number
  nonImageToolCount: number
  forcedImageGeneration: boolean
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
  imageGeneration?: boolean
  imageGenerationForced?: boolean
}): GatewayRequestBodyState {
  const contentType = String(input.contentType ?? '')
  const parsedBody = typeof input.parsedBody === 'object' && input.parsedBody !== null
    ? input.parsedBody as Record<string, unknown>
    : undefined
  const imageInspection = parsedBody ? inspectImageGenerationTools(parsedBody) : undefined
  return {
    rawBodyBytes: input.rawBody.length,
    contentType,
    isJson: isGatewayJsonContentType(contentType),
    jsonParseStatus: input.jsonParseStatus,
    jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
    model: input.model ?? (typeof parsedBody?.model === 'string' ? parsedBody.model : undefined),
    stream: input.stream ?? (typeof parsedBody?.stream === 'boolean' ? parsedBody.stream : undefined),
    imageGeneration: input.imageGeneration ?? (
      imageInspection ? imageInspection.imageToolCount > 0 || imageInspection.forcedImageGeneration : false
    ),
    imageGenerationForced: input.imageGenerationForced ?? imageInspection?.forcedImageGeneration ?? false
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
      stream: state.stream ?? (typeof req.body?.stream === 'boolean' ? req.body.stream : undefined),
      imageGeneration: state.imageGeneration,
      imageGenerationForced: state.imageGenerationForced
    }
  }
}

export function requestBodyHasImageGenerationHint(value: unknown): boolean {
  const inspection = inspectImageGenerationTools(value)
  return inspection.imageToolCount > 0 || inspection.forcedImageGeneration
}

export function requestBodyForcesImageGeneration(value: unknown): boolean {
  return inspectImageGenerationTools(value).forcedImageGeneration
}

export function gatewayRequestBodyForcesImageGeneration(req: Request): boolean {
  const state = getGatewayRequestBodyState(req)
  return Boolean(state?.imageGenerationForced || requestBodyForcesImageGeneration(req.body))
}

export function downgradeGatewayAutoImageGenerationTool(req: Request): GatewayImageGenerationToolDowngradeResult {
  const body = gatewayJsonObjectBody(req)
  if (!body) {
    return { downgraded: false, removedToolCount: 0, reason: 'not_json_object' }
  }

  const inspection = inspectImageGenerationTools(body)
  if (inspection.forcedImageGeneration) {
    return { downgraded: false, removedToolCount: 0, reason: 'forced_image_generation_tool' }
  }
  if (inspection.imageToolCount <= 0) {
    return { downgraded: false, removedToolCount: 0, reason: 'no_auto_image_generation_tool' }
  }

  const nextBody = { ...body }
  const removedToolCount = removeImageGenerationToolDefinitions(nextBody)
  if (removedToolCount <= 0) {
    return { downgraded: false, removedToolCount: 0, reason: 'no_auto_image_generation_tool' }
  }

  replaceGatewayJsonBody(req, nextBody)
  return { downgraded: true, removedToolCount, reason: 'auto_image_generation_tool_removed' }
}

function inspectImageGenerationTools(value: unknown): ImageGenerationToolInspection {
  const result: ImageGenerationToolInspection = {
    imageToolCount: 0,
    nonImageToolCount: 0,
    forcedImageGeneration: false
  }
  if (typeof value !== 'object' || value === null) {
    return result
  }
  const body = value as Record<string, unknown>
  if (body.type === 'image_generation') {
    result.forcedImageGeneration = true
  }
  collectToolDefinitionCounts(body.tools, result)
  collectToolDefinitionCounts(toolChoiceTools(body.tool_choice), result)
  if (toolChoiceForcesImageGeneration(body.tool_choice)) {
    result.forcedImageGeneration = true
  }
  if (
    body.tool_choice === 'required'
    && result.imageToolCount > 0
    && result.nonImageToolCount === 0
  ) {
    result.forcedImageGeneration = true
  }
  return result
}

function collectToolDefinitionCounts(value: unknown, result: ImageGenerationToolInspection, depth = 0): void {
  if (depth > 4 || value === null || value === undefined) {
    return
  }
  if (typeof value === 'string') {
    if (value === 'image_generation') {
      result.imageToolCount += 1
    } else if (value.trim()) {
      result.nonImageToolCount += 1
    }
    return
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      collectToolDefinitionCounts(item, result, depth + 1)
    }
    return
  }
  if (typeof value !== 'object') {
    return
  }
  const object = value as Record<string, unknown>
  const type = object.type
  if (type === 'image_generation') {
    result.imageToolCount += 1
    return
  }
  if (typeof type === 'string' && type.trim()) {
    result.nonImageToolCount += 1
  }
}

function toolChoiceForcesImageGeneration(value: unknown): boolean {
  if (value === 'image_generation') {
    return true
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const object = value as Record<string, unknown>
  return object.type === 'image_generation'
}

function toolChoiceTools(value: unknown): unknown {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined
  }
  return (value as Record<string, unknown>).tools
}

function removeImageGenerationToolDefinitions(body: Record<string, unknown>): number {
  let removedToolCount = removeImageGenerationToolArray(body, 'tools')
  if (typeof body.tool_choice === 'object' && body.tool_choice !== null && !Array.isArray(body.tool_choice)) {
    const toolChoice = { ...(body.tool_choice as Record<string, unknown>) }
    const removedChoiceTools = removeImageGenerationToolArray(toolChoice, 'tools')
    if (removedChoiceTools > 0) {
      body.tool_choice = toolChoice
      removedToolCount += removedChoiceTools
    }
  }
  return removedToolCount
}

function removeImageGenerationToolArray(owner: Record<string, unknown>, key: string): number {
  if (!Array.isArray(owner[key])) {
    return 0
  }
  const tools = owner[key] as unknown[]
  const nextTools = tools.filter((tool) => !isImageGenerationToolDefinition(tool))
  const removedToolCount = tools.length - nextTools.length
  if (removedToolCount <= 0) {
    return 0
  }
  if (nextTools.length > 0) {
    owner[key] = nextTools
  } else {
    delete owner[key]
  }
  return removedToolCount
}

function isImageGenerationToolDefinition(value: unknown): boolean {
  if (value === 'image_generation') {
    return true
  }
  return typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && (value as Record<string, unknown>).type === 'image_generation'
}

function gatewayJsonObjectBody(req: Request): Record<string, unknown> | undefined {
  const request = req as GatewayRawBodyRequest
  const body = request.body !== undefined
    ? request.body
    : request.gatewayParsedJsonBodyAvailable
      ? request.gatewayParsedJsonBody
      : undefined
  if (typeof body === 'object' && body !== null && !Array.isArray(body)) {
    return body as Record<string, unknown>
  }
  return undefined
}

function replaceGatewayJsonBody(req: Request, body: Record<string, unknown>): void {
  const request = req as GatewayRawBodyRequest
  const previousState = getGatewayRequestBodyState(req)
  const contentType = previousState?.contentType ?? String(req.headers['content-type'] ?? 'application/json')
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  request.rawBody = rawBody
  request.body = body
  request.gatewayParsedJsonBodyAvailable = true
  request.gatewayParsedJsonBody = body
  request.gatewayUpstreamBodyCache = undefined
  request.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody,
    contentType,
    jsonParseStatus: previousState?.jsonParseStatus ?? 'parsed',
    parsedBody: body
  })
}

export interface GatewayJsonBodyMetadata {
  model?: string
  stream?: boolean
  imageGeneration?: boolean
  imageGenerationForced?: boolean
  invalidJson?: boolean
}

export function extractGatewayJsonBodyMetadata(rawBody: Buffer): GatewayJsonBodyMetadata {
  const metadata: GatewayJsonBodyMetadata = {}
  const inspection: ImageGenerationToolInspection = {
    imageToolCount: 0,
    nonImageToolCount: 0,
    forcedImageGeneration: false
  }
  let toolChoiceRequired = false
  let index = skipJsonWhitespace(rawBody, 0)
  if (rawBody[index] !== jsonObjectOpenByte) {
    return metadata
  }
  index += 1
  let closed = false

  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      index += 1
      closed = true
      break
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }

    const key = readJsonStringToken(rawBody, index)
    if (!key) {
      metadata.invalidJson = true
      break
    }
    index = skipJsonWhitespace(rawBody, key.nextIndex)
    if (rawBody[index] !== jsonColonByte) {
      metadata.invalidJson = true
      break
    }
    index = skipJsonWhitespace(rawBody, index + 1)

    if (key.value === 'model') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        metadata.model = value.value
        index = value.nextIndex
      } else {
        const skipped = skipJsonValue(rawBody, index)
        metadata.invalidJson = metadata.invalidJson || !skipped.ok
        index = skipped.nextIndex
      }
    } else if (key.value === 'stream') {
      const booleanValue = readJsonBoolean(rawBody, index)
      if (booleanValue) {
        metadata.stream = booleanValue.value
        index = booleanValue.nextIndex
      } else {
        const skipped = skipJsonValue(rawBody, index)
        metadata.invalidJson = metadata.invalidJson || !skipped.ok
        index = skipped.nextIndex
      }
    } else if (key.value === 'type') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        if (value.value === 'image_generation') {
          inspection.forcedImageGeneration = true
        }
        index = value.nextIndex
      } else {
        const skipped = skipJsonValue(rawBody, index)
        metadata.invalidJson = metadata.invalidJson || !skipped.ok
        index = skipped.nextIndex
      }
    } else if (key.value === 'tools') {
      const result = inspectJsonToolDefinitions(rawBody, index)
      mergeImageGenerationToolInspection(inspection, result.inspection)
      index = result.nextIndex
    } else if (key.value === 'tool_choice') {
      const result = inspectJsonToolChoice(rawBody, index)
      mergeImageGenerationToolInspection(inspection, result.inspection)
      if (result.required) {
        toolChoiceRequired = true
      }
      index = result.nextIndex
    } else {
      const skipped = skipJsonValue(rawBody, index)
      metadata.invalidJson = metadata.invalidJson || !skipped.ok
      index = skipped.nextIndex
    }
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    if (rawBody[index] === jsonObjectCloseByte) {
      index += 1
      closed = true
      break
    }
    if (index >= rawBody.length) {
      metadata.invalidJson = true
      break
    }
    metadata.invalidJson = true
    break
  }

  if (rawBody.length > 0 && !closed && !metadata.invalidJson) {
    metadata.invalidJson = true
  }
  if (closed && skipJsonWhitespace(rawBody, index) < rawBody.length) {
    metadata.invalidJson = true
  }

  if (
    toolChoiceRequired
    && inspection.imageToolCount > 0
    && inspection.nonImageToolCount === 0
  ) {
    inspection.forcedImageGeneration = true
  }
  metadata.imageGeneration = inspection.imageToolCount > 0 || inspection.forcedImageGeneration
  metadata.imageGenerationForced = inspection.forcedImageGeneration
  return metadata
}

interface JsonReadResult {
  nextIndex: number
  ok: boolean
}

interface JsonToolInspectionReadResult {
  nextIndex: number
  inspection: ImageGenerationToolInspection
  required?: boolean
}

function createEmptyImageGenerationToolInspection(): ImageGenerationToolInspection {
  return {
    imageToolCount: 0,
    nonImageToolCount: 0,
    forcedImageGeneration: false
  }
}

function mergeImageGenerationToolInspection(
  target: ImageGenerationToolInspection,
  source: ImageGenerationToolInspection
): void {
  target.imageToolCount += source.imageToolCount
  target.nonImageToolCount += source.nonImageToolCount
  target.forcedImageGeneration = target.forcedImageGeneration || source.forcedImageGeneration
}

function inspectJsonToolDefinitions(rawBody: Buffer, index: number, depth = 0): JsonToolInspectionReadResult {
  const inspection = createEmptyImageGenerationToolInspection()
  index = skipJsonWhitespace(rawBody, index)
  if (depth > 4) {
    return { nextIndex: skipJsonValue(rawBody, index).nextIndex, inspection }
  }
  if (rawBody[index] === jsonStringByte) {
    const value = readJsonStringToken(rawBody, index)
    if (!value) {
      return { nextIndex: rawBody.length, inspection }
    }
    countToolType(value.value, inspection)
    return { nextIndex: value.nextIndex, inspection }
  }
  if (rawBody[index] === jsonArrayOpenByte) {
    index += 1
    while (index < rawBody.length) {
      index = skipJsonWhitespace(rawBody, index)
      if (rawBody[index] === jsonArrayCloseByte) {
        return { nextIndex: index + 1, inspection }
      }
      if (rawBody[index] === jsonCommaByte) {
        index += 1
        continue
      }
      const result = inspectJsonToolDefinitions(rawBody, index, depth + 1)
      mergeImageGenerationToolInspection(inspection, result.inspection)
      index = result.nextIndex
    }
    return { nextIndex: rawBody.length, inspection }
  }
  if (rawBody[index] === jsonObjectOpenByte) {
    return inspectJsonToolObject(rawBody, index)
  }
  return { nextIndex: skipJsonValue(rawBody, index).nextIndex, inspection }
}

function inspectJsonToolObject(rawBody: Buffer, index: number): JsonToolInspectionReadResult {
  const inspection = createEmptyImageGenerationToolInspection()
  index += 1
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      return { nextIndex: index + 1, inspection }
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    const key = readJsonStringToken(rawBody, index)
    if (!key) {
      return { nextIndex: rawBody.length, inspection }
    }
    index = skipJsonWhitespace(rawBody, key.nextIndex)
    if (rawBody[index] !== jsonColonByte) {
      return { nextIndex: rawBody.length, inspection }
    }
    index = skipJsonWhitespace(rawBody, index + 1)
    if (key.value === 'type') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        countToolType(value.value, inspection)
        index = value.nextIndex
        continue
      }
    }
    index = skipJsonValue(rawBody, index).nextIndex
  }
  return { nextIndex: rawBody.length, inspection }
}

function inspectJsonToolChoice(rawBody: Buffer, index: number): JsonToolInspectionReadResult {
  const inspection = createEmptyImageGenerationToolInspection()
  index = skipJsonWhitespace(rawBody, index)
  if (rawBody[index] === jsonStringByte) {
    const value = readJsonStringToken(rawBody, index)
    if (!value) {
      return { nextIndex: rawBody.length, inspection }
    }
    if (value.value === 'image_generation') {
      inspection.forcedImageGeneration = true
    }
    return { nextIndex: value.nextIndex, inspection, required: value.value === 'required' }
  }
  if (rawBody[index] !== jsonObjectOpenByte) {
    return { nextIndex: skipJsonValue(rawBody, index).nextIndex, inspection }
  }

  index += 1
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      return { nextIndex: index + 1, inspection }
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    const key = readJsonStringToken(rawBody, index)
    if (!key) {
      return { nextIndex: rawBody.length, inspection }
    }
    index = skipJsonWhitespace(rawBody, key.nextIndex)
    if (rawBody[index] !== jsonColonByte) {
      return { nextIndex: rawBody.length, inspection }
    }
    index = skipJsonWhitespace(rawBody, index + 1)
    if (key.value === 'type') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        if (value.value === 'image_generation') {
          inspection.forcedImageGeneration = true
        }
        index = value.nextIndex
        continue
      }
    } else if (key.value === 'tools') {
      const result = inspectJsonToolDefinitions(rawBody, index)
      mergeImageGenerationToolInspection(inspection, result.inspection)
      index = result.nextIndex
      continue
    }
    index = skipJsonValue(rawBody, index).nextIndex
  }
  return { nextIndex: rawBody.length, inspection }
}

function countToolType(value: string, inspection: ImageGenerationToolInspection): void {
  if (value === 'image_generation') {
    inspection.imageToolCount += 1
  } else if (value.trim()) {
    inspection.nonImageToolCount += 1
  }
}

function skipJsonWhitespace(rawBody: Buffer, index: number): number {
  while (index < rawBody.length && isJsonWhitespaceByte(rawBody[index] ?? 0)) {
    index += 1
  }
  return index
}

function readJsonStringToken(rawBody: Buffer, index: number): { value: string; nextIndex: number } | undefined {
  const nextIndex = skipJsonString(rawBody, index)
  if (nextIndex === undefined) {
    return undefined
  }
  try {
    const value = JSON.parse(rawBody.toString('utf8', index, nextIndex)) as unknown
    return typeof value === 'string' ? { value, nextIndex } : undefined
  } catch {
    return undefined
  }
}

function readJsonBoolean(rawBody: Buffer, index: number): { value: boolean; nextIndex: number } | undefined {
  if (rawBody.subarray(index, index + 4).equals(jsonTrueBuffer)) {
    return { value: true, nextIndex: index + 4 }
  }
  if (rawBody.subarray(index, index + 5).equals(jsonFalseBuffer)) {
    return { value: false, nextIndex: index + 5 }
  }
  return undefined
}

function skipJsonValue(rawBody: Buffer, index: number): JsonReadResult {
  index = skipJsonWhitespace(rawBody, index)
  const firstByte = rawBody[index]
  if (firstByte === jsonStringByte) {
    const nextIndex = skipJsonString(rawBody, index)
    return { nextIndex: nextIndex ?? rawBody.length, ok: nextIndex !== undefined }
  }
  if (firstByte !== jsonObjectOpenByte && firstByte !== jsonArrayOpenByte) {
    const startIndex = index
    while (
      index < rawBody.length
      && rawBody[index] !== jsonCommaByte
      && rawBody[index] !== jsonObjectCloseByte
      && rawBody[index] !== jsonArrayCloseByte
    ) {
      index += 1
    }
    return {
      nextIndex: index,
      ok: isValidJsonPrimitive(rawBody, startIndex, index)
    }
  }

  const stack = [firstByte]
  for (let cursor = index + 1; cursor < rawBody.length; cursor += 1) {
    const byte = rawBody[cursor]
    if (byte === jsonStringByte) {
      const nextIndex = skipJsonString(rawBody, cursor)
      if (nextIndex === undefined) {
        return { nextIndex: rawBody.length, ok: false }
      }
      cursor = nextIndex - 1
      continue
    }
    if (byte === jsonObjectOpenByte || byte === jsonArrayOpenByte) {
      stack.push(byte)
      continue
    }
    if (byte === jsonObjectCloseByte || byte === jsonArrayCloseByte) {
      const previous = stack.pop()
      if (
        (byte === jsonObjectCloseByte && previous !== jsonObjectOpenByte)
        || (byte === jsonArrayCloseByte && previous !== jsonArrayOpenByte)
      ) {
        return { nextIndex: rawBody.length, ok: false }
      }
      if (stack.length === 0) {
        return { nextIndex: cursor + 1, ok: true }
      }
    }
  }
  return { nextIndex: rawBody.length, ok: false }
}

function skipJsonString(rawBody: Buffer, index: number): number | undefined {
  if (rawBody[index] !== jsonStringByte) {
    return undefined
  }
  let escaped = false
  for (let cursor = index + 1; cursor < rawBody.length; cursor += 1) {
    const byte = rawBody[cursor]
    if (escaped) {
      escaped = false
      continue
    }
    if (byte === jsonEscapeByte) {
      escaped = true
      continue
    }
    if (byte === jsonStringByte) {
      return cursor + 1
    }
  }
  return undefined
}

function isJsonWhitespaceByte(byte: number): boolean {
  return byte === 0x20 || byte === 0x0a || byte === 0x0d || byte === 0x09
}

function isValidJsonPrimitive(rawBody: Buffer, startIndex: number, endIndex: number): boolean {
  const start = skipJsonWhitespace(rawBody, startIndex)
  const end = trimJsonWhitespaceEnd(rawBody, start, endIndex)
  return jsonLiteralEquals(rawBody, start, end, jsonNullBuffer)
    || jsonLiteralEquals(rawBody, start, end, jsonTrueBuffer)
    || jsonLiteralEquals(rawBody, start, end, jsonFalseBuffer)
    || isValidJsonNumber(rawBody, start, end)
}

function trimJsonWhitespaceEnd(rawBody: Buffer, startIndex: number, endIndex: number): number {
  let index = endIndex
  while (index > startIndex && isJsonWhitespaceByte(rawBody[index - 1] ?? 0)) {
    index -= 1
  }
  return index
}

function jsonLiteralEquals(rawBody: Buffer, startIndex: number, endIndex: number, literal: Buffer): boolean {
  return endIndex - startIndex === literal.length
    && rawBody.subarray(startIndex, endIndex).equals(literal)
}

function isValidJsonNumber(rawBody: Buffer, startIndex: number, endIndex: number): boolean {
  let index = startIndex
  if (index >= endIndex) return false
  if (rawBody[index] === jsonMinusByte) {
    index += 1
  }
  if (index >= endIndex) return false

  const integerFirstByte = rawBody[index]
  if (integerFirstByte === jsonZeroByte) {
    index += 1
    if (isJsonDigitByte(rawBody[index] ?? -1)) {
      return false
    }
  } else if (isJsonOneToNineDigitByte(integerFirstByte ?? -1)) {
    index += 1
    while (index < endIndex && isJsonDigitByte(rawBody[index] ?? -1)) {
      index += 1
    }
  } else {
    return false
  }

  if (rawBody[index] === jsonDotByte) {
    index += 1
    const fractionStart = index
    while (index < endIndex && isJsonDigitByte(rawBody[index] ?? -1)) {
      index += 1
    }
    if (index === fractionStart) {
      return false
    }
  }

  const exponentByte = rawBody[index]
  if (exponentByte === jsonLowerEByte || exponentByte === jsonUpperEByte) {
    index += 1
    if (rawBody[index] === jsonPlusByte || rawBody[index] === jsonMinusByte) {
      index += 1
    }
    const exponentStart = index
    while (index < endIndex && isJsonDigitByte(rawBody[index] ?? -1)) {
      index += 1
    }
    if (index === exponentStart) {
      return false
    }
  }

  return index === endIndex
}

function isJsonDigitByte(byte: number): boolean {
  return byte >= jsonZeroByte && byte <= jsonNineByte
}

function isJsonOneToNineDigitByte(byte: number): boolean {
  return byte >= jsonOneByte && byte <= jsonNineByte
}

const jsonStringByte = 0x22
const jsonEscapeByte = 0x5c
const jsonCommaByte = 0x2c
const jsonColonByte = 0x3a
const jsonMinusByte = 0x2d
const jsonPlusByte = 0x2b
const jsonDotByte = 0x2e
const jsonZeroByte = 0x30
const jsonOneByte = 0x31
const jsonNineByte = 0x39
const jsonLowerEByte = 0x65
const jsonUpperEByte = 0x45
const jsonObjectOpenByte = 0x7b
const jsonObjectCloseByte = 0x7d
const jsonArrayOpenByte = 0x5b
const jsonArrayCloseByte = 0x5d
const jsonNullBuffer = Buffer.from('null')
const jsonTrueBuffer = Buffer.from('true')
const jsonFalseBuffer = Buffer.from('false')

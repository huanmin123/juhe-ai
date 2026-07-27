import { normalizeUsageReasoningEffort, type UsageReasoningEffort } from '../usage/reasoning-effort.js'
import { normalizeOptionalUsageServiceTier, type UsageServiceTier } from '../usage/service-tier.js'

export interface GatewayJsonBodyMetadata {
  model?: string
  stream?: boolean
  serviceTier?: UsageServiceTier
  reasoningEffort?: UsageReasoningEffort
  maxOutputTokens?: number
  imageGeneration?: boolean
  imageGenerationForced?: boolean
  compactionTrigger?: boolean
  invalidJson?: boolean
}

interface ImageGenerationToolInspection {
  imageToolCount: number
  nonImageToolCount: number
  forcedImageGeneration: boolean
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

interface JsonCompactionInputReadResult extends JsonReadResult {
  found: boolean
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
    } else if (key.value === 'service_tier') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        metadata.serviceTier = normalizeOptionalUsageServiceTier(value.value)
        index = value.nextIndex
      } else {
        const skipped = skipJsonValue(rawBody, index)
        metadata.invalidJson = metadata.invalidJson || !skipped.ok
        index = skipped.nextIndex
      }
    } else if (key.value === 'reasoning_effort') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        metadata.reasoningEffort = metadata.reasoningEffort ?? normalizeUsageReasoningEffort(value.value)
        index = value.nextIndex
      } else {
        const skipped = skipJsonValue(rawBody, index)
        metadata.invalidJson = metadata.invalidJson || !skipped.ok
        index = skipped.nextIndex
      }
    } else if (key.value === 'reasoning') {
      const result = readJsonObjectStringProperty(rawBody, index, 'effort')
      metadata.reasoningEffort = normalizeUsageReasoningEffort(result.value) ?? metadata.reasoningEffort
      metadata.invalidJson = metadata.invalidJson || !result.ok
      index = result.nextIndex
    } else if (key.value === 'output_config') {
      const result = readJsonObjectStringProperty(rawBody, index, 'effort')
      metadata.reasoningEffort = normalizeUsageReasoningEffort(result.value) ?? metadata.reasoningEffort
      metadata.invalidJson = metadata.invalidJson || !result.ok
      index = result.nextIndex
    } else if (key.value === 'generationConfig') {
      const result = readJsonObjectNestedStringProperty(rawBody, index, ['thinkingConfig', 'thinkingLevel'])
      metadata.reasoningEffort = normalizeUsageReasoningEffort(result.value) ?? metadata.reasoningEffort
      metadata.invalidJson = metadata.invalidJson || !result.ok
      index = result.nextIndex
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
    } else if (key.value === 'max_output_tokens' || key.value === 'max_tokens') {
      const numberValue = readJsonNonNegativeInteger(rawBody, index)
      if (numberValue) {
        metadata.maxOutputTokens = metadata.maxOutputTokens === undefined
          ? numberValue.value
          : Math.max(metadata.maxOutputTokens, numberValue.value)
        index = numberValue.nextIndex
      } else {
        const skipped = skipJsonValue(rawBody, index)
        metadata.invalidJson = metadata.invalidJson || !skipped.ok
        index = skipped.nextIndex
      }
    } else if (key.value === 'input') {
      const result = inspectJsonCompactionInput(rawBody, index)
      metadata.compactionTrigger = result.found
      metadata.invalidJson = metadata.invalidJson || !result.ok
      index = result.nextIndex
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
  metadata.compactionTrigger = !metadata.invalidJson && metadata.compactionTrigger === true
  return metadata
}

function inspectJsonCompactionInput(rawBody: Buffer, index: number): JsonCompactionInputReadResult {
  index = skipJsonWhitespace(rawBody, index)
  if (rawBody[index] !== jsonArrayOpenByte) {
    const skipped = skipJsonValue(rawBody, index)
    return { ...skipped, found: false }
  }
  index += 1
  let found = false
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonArrayCloseByte) {
      return { nextIndex: index + 1, ok: true, found }
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    const item = inspectJsonCompactionInputItem(rawBody, index)
    if (!item.ok) return { ...item, found }
    found = found || item.found
    index = item.nextIndex
  }
  return { nextIndex: rawBody.length, ok: false, found }
}

function inspectJsonCompactionInputItem(rawBody: Buffer, index: number): JsonCompactionInputReadResult {
  index = skipJsonWhitespace(rawBody, index)
  if (rawBody[index] !== jsonObjectOpenByte) {
    const skipped = skipJsonValue(rawBody, index)
    return { ...skipped, found: false }
  }
  index += 1
  let found = false
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      return { nextIndex: index + 1, ok: true, found }
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    const key = readJsonStringToken(rawBody, index)
    if (!key) return { nextIndex: rawBody.length, ok: false, found }
    index = skipJsonWhitespace(rawBody, key.nextIndex)
    if (rawBody[index] !== jsonColonByte) return { nextIndex: rawBody.length, ok: false, found }
    index = skipJsonWhitespace(rawBody, index + 1)
    if (key.value === 'type') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        found = value.value === 'compaction_trigger'
        index = value.nextIndex
        continue
      }
    }
    const skipped = skipJsonValue(rawBody, index)
    if (!skipped.ok) return { ...skipped, found }
    index = skipped.nextIndex
  }
  return { nextIndex: rawBody.length, ok: false, found }
}

function readJsonObjectStringProperty(
  rawBody: Buffer,
  index: number,
  propertyName: string
): { nextIndex: number; value?: string; ok: boolean } {
  return readJsonObjectNestedStringProperty(rawBody, index, [propertyName])
}

function readJsonObjectNestedStringProperty(
  rawBody: Buffer,
  index: number,
  propertyPath: readonly string[]
): { nextIndex: number; value?: string; ok: boolean } {
  index = skipJsonWhitespace(rawBody, index)
  if (!propertyPath.length || rawBody[index] !== jsonObjectOpenByte) {
    const skipped = skipJsonValue(rawBody, index)
    return { nextIndex: skipped.nextIndex, ok: skipped.ok }
  }

  index += 1
  let value: string | undefined
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      return { nextIndex: index + 1, value, ok: true }
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    const key = readJsonStringToken(rawBody, index)
    if (!key) return { nextIndex: rawBody.length, value, ok: false }
    index = skipJsonWhitespace(rawBody, key.nextIndex)
    if (rawBody[index] !== jsonColonByte) return { nextIndex: rawBody.length, value, ok: false }
    index = skipJsonWhitespace(rawBody, index + 1)
    if (key.value === propertyPath[0]) {
      if (propertyPath.length === 1) {
        const propertyValue = readJsonStringToken(rawBody, index)
        if (propertyValue) {
          value = propertyValue.value
          index = propertyValue.nextIndex
          continue
        }
      } else {
        const nested = readJsonObjectNestedStringProperty(rawBody, index, propertyPath.slice(1))
        if (!nested.ok) return { nextIndex: nested.nextIndex, value, ok: false }
        value = nested.value ?? value
        index = nested.nextIndex
        continue
      }
    }
    const skipped = skipJsonValue(rawBody, index)
    if (!skipped.ok) return { nextIndex: skipped.nextIndex, value, ok: false }
    index = skipped.nextIndex
  }
  return { nextIndex: rawBody.length, value, ok: false }
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

function readJsonNonNegativeInteger(rawBody: Buffer, index: number): { value: number; nextIndex: number } | undefined {
  const result = skipJsonValue(rawBody, index)
  if (!result.ok) return undefined
  const text = rawBody.toString('utf8', index, result.nextIndex).trim()
  const value = Number(text)
  return Number.isSafeInteger(value) && value >= 0
    ? { value, nextIndex: result.nextIndex }
    : undefined
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

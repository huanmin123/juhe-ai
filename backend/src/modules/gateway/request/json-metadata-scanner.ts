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
  strictOutputRequirement?: boolean
  invalidJson?: boolean
}

interface GenerationConfigInspection {
  nextIndex: number
  reasoningEffort?: UsageReasoningEffort
  imageOutput: boolean
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

export function extractGatewayJsonBodyMetadata(rawBody: Buffer): GatewayJsonBodyMetadata {
  const metadata: GatewayJsonBodyMetadata = {}
  const inspection: ImageGenerationToolInspection = {
    imageToolCount: 0,
    nonImageToolCount: 0,
    forcedImageGeneration: false
  }
  let toolChoiceRequired = false
  let imageOutput = false
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
    } else if (key.value === 'generationConfig' || key.value === 'generation_config') {
      const result = inspectGenerationConfig(rawBody, index)
      metadata.reasoningEffort = result.reasoningEffort ?? metadata.reasoningEffort
      imageOutput = imageOutput || result.imageOutput
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
      metadata.strictOutputRequirement = true
      const result = inspectJsonToolDefinitions(rawBody, index)
      mergeImageGenerationToolInspection(inspection, result.inspection)
      index = result.nextIndex
    } else if (key.value === 'tool_choice') {
      metadata.strictOutputRequirement = true
      const result = inspectJsonToolChoice(rawBody, index)
      mergeImageGenerationToolInspection(inspection, result.inspection)
      if (result.required) {
        toolChoiceRequired = true
      }
      index = result.nextIndex
    } else if (key.value === 'response_format') {
      metadata.strictOutputRequirement = true
      const skipped = skipJsonValue(rawBody, index)
      metadata.invalidJson = metadata.invalidJson || !skipped.ok
      index = skipped.nextIndex
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
  metadata.imageGeneration = imageOutput || inspection.imageToolCount > 0 || inspection.forcedImageGeneration
  metadata.imageGenerationForced = inspection.forcedImageGeneration
  metadata.invalidJson = metadata.invalidJson || !isValidJsonDocument(rawBody)
  return metadata
}

function inspectGenerationConfig(rawBody: Buffer, index: number): GenerationConfigInspection {
  index = skipJsonWhitespace(rawBody, index)
  if (rawBody[index] !== jsonObjectOpenByte) {
    return { nextIndex: skipJsonValue(rawBody, index).nextIndex, imageOutput: false }
  }

  index += 1
  let reasoningEffort: UsageReasoningEffort | undefined
  let imageOutput = false
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      return { nextIndex: index + 1, reasoningEffort, imageOutput }
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    const key = readJsonStringToken(rawBody, index)
    if (!key) return { nextIndex: rawBody.length, reasoningEffort, imageOutput }
    index = skipJsonWhitespace(rawBody, key.nextIndex)
    if (rawBody[index] !== jsonColonByte) {
      return { nextIndex: rawBody.length, reasoningEffort, imageOutput }
    }
    index = skipJsonWhitespace(rawBody, index + 1)
    if (key.value === 'thinkingConfig' || key.value === 'thinking_config') {
      const result = readJsonObjectStringProperty(rawBody, index, key.value === 'thinkingConfig' ? 'thinkingLevel' : 'thinking_level')
      reasoningEffort = normalizeUsageReasoningEffort(result.value) ?? reasoningEffort
      index = result.nextIndex
      continue
    }
    if (key.value === 'responseModalities' || key.value === 'response_modalities') {
      const result = inspectJsonStringArrayForImage(rawBody, index)
      imageOutput = imageOutput || result.imageOutput
      index = result.nextIndex
      continue
    }
    if (key.value === 'responseMimeType' || key.value === 'response_mime_type') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        imageOutput = imageOutput || /^image\//i.test(value.value.trim())
        index = value.nextIndex
        continue
      }
    }
    index = skipJsonValue(rawBody, index).nextIndex
  }
  return { nextIndex: rawBody.length, reasoningEffort, imageOutput }
}

function inspectJsonStringArrayForImage(
  rawBody: Buffer,
  index: number
): { nextIndex: number; imageOutput: boolean } {
  index = skipJsonWhitespace(rawBody, index)
  if (rawBody[index] !== jsonArrayOpenByte) {
    return { nextIndex: skipJsonValue(rawBody, index).nextIndex, imageOutput: false }
  }
  index += 1
  let imageOutput = false
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonArrayCloseByte) {
      return { nextIndex: index + 1, imageOutput }
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    const value = readJsonStringToken(rawBody, index)
    if (value) {
      imageOutput = imageOutput || value.value.trim().toLowerCase() === 'image'
      index = value.nextIndex
      continue
    }
    index = skipJsonValue(rawBody, index).nextIndex
  }
  return { nextIndex: rawBody.length, imageOutput }
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

type JsonContainerFrame =
  | { kind: 'object'; state: 'first_key_or_end' | 'key' | 'colon' | 'value' | 'comma_or_end' }
  | { kind: 'array'; state: 'first_value_or_end' | 'value' | 'comma_or_end' }

function isValidJsonDocument(rawBody: Buffer): boolean {
  const stack: JsonContainerFrame[] = []
  let index = skipJsonWhitespace(rawBody, 0)
  let rootComplete = false

  const consumeValue = (): boolean => {
    index = skipJsonWhitespace(rawBody, index)
    const byte = rawBody[index]
    if (byte === jsonObjectOpenByte) {
      index += 1
      stack.push({ kind: 'object', state: 'first_key_or_end' })
      return true
    }
    if (byte === jsonArrayOpenByte) {
      index += 1
      stack.push({ kind: 'array', state: 'first_value_or_end' })
      return true
    }
    if (byte === jsonStringByte) {
      const nextIndex = skipValidJsonString(rawBody, index)
      if (nextIndex === undefined) return false
      index = nextIndex
      return true
    }
    const literalEnd = validJsonLiteralEnd(rawBody, index)
    if (literalEnd !== undefined) {
      index = literalEnd
      return true
    }
    const numberEnd = validJsonNumberEnd(rawBody, index)
    if (numberEnd === undefined) return false
    index = numberEnd
    return true
  }

  if (!consumeValue()) return false
  if (stack.length === 0) rootComplete = true

  while (!rootComplete) {
    index = skipJsonWhitespace(rawBody, index)
    const frame = stack[stack.length - 1]
    if (!frame) return false

    if (frame.kind === 'object') {
      if (frame.state === 'first_key_or_end' && rawBody[index] === jsonObjectCloseByte) {
        index += 1
        stack.pop()
      } else if (frame.state === 'first_key_or_end' || frame.state === 'key') {
        const nextIndex = skipValidJsonString(rawBody, index)
        if (nextIndex === undefined) return false
        index = nextIndex
        frame.state = 'colon'
        continue
      } else if (frame.state === 'colon') {
        if (rawBody[index] !== jsonColonByte) return false
        index += 1
        frame.state = 'value'
        continue
      } else if (frame.state === 'value') {
        frame.state = 'comma_or_end'
        const depth = stack.length
        if (!consumeValue()) return false
        if (stack.length === depth) continue
        continue
      } else if (rawBody[index] === jsonCommaByte) {
        index += 1
        frame.state = 'key'
        continue
      } else if (rawBody[index] === jsonObjectCloseByte) {
        index += 1
        stack.pop()
      } else {
        return false
      }
    } else if (frame.state === 'first_value_or_end' && rawBody[index] === jsonArrayCloseByte) {
      index += 1
      stack.pop()
    } else if (frame.state === 'first_value_or_end' || frame.state === 'value') {
      frame.state = 'comma_or_end'
      const depth = stack.length
      if (!consumeValue()) return false
      if (stack.length === depth) continue
      continue
    } else if (rawBody[index] === jsonCommaByte) {
      index += 1
      frame.state = 'value'
      continue
    } else if (rawBody[index] === jsonArrayCloseByte) {
      index += 1
      stack.pop()
    } else {
      return false
    }

    if (stack.length === 0) {
      rootComplete = true
    }
  }

  return skipJsonWhitespace(rawBody, index) === rawBody.length
}

function skipValidJsonString(rawBody: Buffer, index: number): number | undefined {
  if (rawBody[index] !== jsonStringByte) return undefined
  for (let cursor = index + 1; cursor < rawBody.length; cursor += 1) {
    const byte = rawBody[cursor]
    if (byte === jsonStringByte) return cursor + 1
    if (byte <= 0x1f) return undefined
    if (byte !== jsonEscapeByte) continue
    cursor += 1
    const escaped = rawBody[cursor]
    if (
      escaped === jsonStringByte
      || escaped === jsonEscapeByte
      || escaped === jsonSlashByte
      || escaped === jsonLowerBByte
      || escaped === jsonLowerFByte
      || escaped === jsonLowerNByte
      || escaped === jsonLowerRByte
      || escaped === jsonLowerTByte
    ) {
      continue
    }
    if (escaped !== jsonLowerUByte || cursor + 4 >= rawBody.length) return undefined
    for (let offset = 1; offset <= 4; offset += 1) {
      if (!isJsonHexByte(rawBody[cursor + offset] ?? -1)) return undefined
    }
    cursor += 4
  }
  return undefined
}

function validJsonLiteralEnd(rawBody: Buffer, index: number): number | undefined {
  if (rawBody.subarray(index, index + jsonNullBuffer.length).equals(jsonNullBuffer)) {
    return index + jsonNullBuffer.length
  }
  if (rawBody.subarray(index, index + jsonTrueBuffer.length).equals(jsonTrueBuffer)) {
    return index + jsonTrueBuffer.length
  }
  if (rawBody.subarray(index, index + jsonFalseBuffer.length).equals(jsonFalseBuffer)) {
    return index + jsonFalseBuffer.length
  }
  return undefined
}

function validJsonNumberEnd(rawBody: Buffer, startIndex: number): number | undefined {
  let index = startIndex
  if (rawBody[index] === jsonMinusByte) index += 1
  if (rawBody[index] === jsonZeroByte) {
    index += 1
    if (isJsonDigitByte(rawBody[index] ?? -1)) return undefined
  } else if (isJsonOneToNineDigitByte(rawBody[index] ?? -1)) {
    index += 1
    while (isJsonDigitByte(rawBody[index] ?? -1)) index += 1
  } else {
    return undefined
  }
  if (rawBody[index] === jsonDotByte) {
    index += 1
    const fractionStart = index
    while (isJsonDigitByte(rawBody[index] ?? -1)) index += 1
    if (index === fractionStart) return undefined
  }
  if (rawBody[index] === jsonLowerEByte || rawBody[index] === jsonUpperEByte) {
    index += 1
    if (rawBody[index] === jsonPlusByte || rawBody[index] === jsonMinusByte) index += 1
    const exponentStart = index
    while (isJsonDigitByte(rawBody[index] ?? -1)) index += 1
    if (index === exponentStart) return undefined
  }
  return index
}

function isJsonHexByte(byte: number): boolean {
  return isJsonDigitByte(byte)
    || (byte >= 0x41 && byte <= 0x46)
    || (byte >= 0x61 && byte <= 0x66)
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
const jsonSlashByte = 0x2f
const jsonLowerBByte = 0x62
const jsonLowerFByte = 0x66
const jsonLowerNByte = 0x6e
const jsonLowerRByte = 0x72
const jsonLowerTByte = 0x74
const jsonLowerUByte = 0x75
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

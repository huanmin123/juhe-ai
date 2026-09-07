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
  codexCompactionTrigger?: boolean
  invalidJson?: boolean
}

interface GenerationConfigInspection {
  nextIndex: number
  isObject: boolean
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
  let codexCompactionTrigger = false
  let reasoningObjectEffort: UsageReasoningEffort | undefined
  let reasoningFieldEffort: UsageReasoningEffort | undefined
  let outputConfigEffort: UsageReasoningEffort | undefined
  let camelGenerationConfig: GenerationConfigInspection | undefined
  let snakeGenerationConfig: GenerationConfigInspection | undefined
  let maxOutputTokens: number | undefined
  let maxTokens: number | undefined
  let topLevelTypeForcesImage = false
  let toolsInspection = createEmptyImageGenerationToolInspection()
  let toolChoiceInspection = createEmptyImageGenerationToolInspection()
  let toolChoiceRequired = false
  let responseFormatStrict = false
  let toolsStrict = false
  let toolChoiceStrict = false
  const validJson = isValidJsonDocument(rawBody, {
    onCodexCompactionTrigger: () => {
      codexCompactionTrigger = true
    },
    onTopLevelProperty: (key, index) => {
      if (key === 'model') {
        const value = readJsonStringToken(rawBody, index)
        metadata.model = value?.value
      } else if (key === 'service_tier') {
        const value = readJsonStringToken(rawBody, index)
        metadata.serviceTier = value ? normalizeOptionalUsageServiceTier(value.value) : undefined
      } else if (key === 'reasoning_effort') {
        const value = readJsonStringToken(rawBody, index)
        reasoningFieldEffort = normalizeUsageReasoningEffort(value?.value)
      } else if (key === 'reasoning') {
        const result = readJsonObjectStringProperty(rawBody, index, 'effort')
        reasoningObjectEffort = normalizeUsageReasoningEffort(result.value)
      } else if (key === 'output_config') {
        const result = readJsonObjectStringProperty(rawBody, index, 'effort')
        outputConfigEffort = normalizeUsageReasoningEffort(result.value)
      } else if (key === 'generationConfig') {
        camelGenerationConfig = inspectGenerationConfig(rawBody, index)
      } else if (key === 'generation_config') {
        snakeGenerationConfig = inspectGenerationConfig(rawBody, index)
      } else if (key === 'stream') {
        metadata.stream = readJsonBoolean(rawBody, index)?.value
      } else if (key === 'max_output_tokens') {
        maxOutputTokens = readJsonNonNegativeInteger(rawBody, index)?.value
      } else if (key === 'max_tokens') {
        maxTokens = readJsonNonNegativeInteger(rawBody, index)?.value
      } else if (key === 'type') {
        topLevelTypeForcesImage = readJsonStringToken(rawBody, index)?.value === 'image_generation'
      } else if (key === 'tools') {
        toolsStrict = jsonValueIsTruthy(rawBody, index)
        toolsInspection = inspectJsonToolDefinitions(rawBody, index).inspection
      } else if (key === 'tool_choice') {
        toolChoiceStrict = jsonValueIsTruthy(rawBody, index)
        const result = inspectJsonToolChoice(rawBody, index)
        toolChoiceInspection = result.inspection
        toolChoiceRequired = result.required === true
      } else if (key === 'response_format') {
        responseFormatStrict = jsonValueIsTruthy(rawBody, index)
      }
    }
  })

  if (!validJson) {
    metadata.invalidJson = true
    return metadata
  }

  const generationConfig = camelGenerationConfig?.isObject
    ? camelGenerationConfig
    : snakeGenerationConfig?.isObject
      ? snakeGenerationConfig
      : undefined
  const inspection = createEmptyImageGenerationToolInspection()
  mergeImageGenerationToolInspection(inspection, toolsInspection)
  mergeImageGenerationToolInspection(inspection, toolChoiceInspection)
  inspection.forcedImageGeneration = inspection.forcedImageGeneration || topLevelTypeForcesImage
  if (
    toolChoiceRequired
    && inspection.imageToolCount > 0
    && inspection.nonImageToolCount === 0
  ) {
    inspection.forcedImageGeneration = true
  }

  metadata.reasoningEffort = reasoningObjectEffort
    ?? reasoningFieldEffort
    ?? outputConfigEffort
    ?? generationConfig?.reasoningEffort
  const tokenLimits = [maxOutputTokens, maxTokens]
    .filter((value): value is number => value !== undefined)
  metadata.maxOutputTokens = tokenLimits.length > 0 ? Math.max(...tokenLimits) : undefined
  metadata.imageGeneration = generationConfig?.imageOutput === true
    || inspection.imageToolCount > 0
    || inspection.forcedImageGeneration
  metadata.imageGenerationForced = inspection.forcedImageGeneration
  metadata.strictOutputRequirement = responseFormatStrict || toolsStrict || toolChoiceStrict
  metadata.codexCompactionTrigger = codexCompactionTrigger
  return metadata
}

function inspectGenerationConfig(rawBody: Buffer, index: number): GenerationConfigInspection {
  index = skipJsonWhitespace(rawBody, index)
  if (rawBody[index] !== jsonObjectOpenByte) {
    return { nextIndex: skipJsonValue(rawBody, index).nextIndex, isObject: false, imageOutput: false }
  }

  index += 1
  let camelThinkingIsObject = false
  let camelThinkingEffort: UsageReasoningEffort | undefined
  let snakeThinkingIsObject = false
  let snakeThinkingEffort: UsageReasoningEffort | undefined
  let camelModalitiesDefined = false
  let camelModalitiesImage = false
  let snakeModalitiesImage = false
  let camelMimeDefined = false
  let camelMimeImage = false
  let snakeMimeImage = false
  const result = (nextIndex: number): GenerationConfigInspection => ({
    nextIndex,
    isObject: true,
    reasoningEffort: camelThinkingIsObject ? camelThinkingEffort : snakeThinkingIsObject ? snakeThinkingEffort : undefined,
    imageOutput: (camelModalitiesDefined ? camelModalitiesImage : snakeModalitiesImage)
      || (camelMimeDefined ? camelMimeImage : snakeMimeImage)
  })
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      return result(index + 1)
    }
    if (rawBody[index] === jsonCommaByte) {
      index += 1
      continue
    }
    const key = readJsonStringToken(rawBody, index)
    if (!key) return result(rawBody.length)
    index = skipJsonWhitespace(rawBody, key.nextIndex)
    if (rawBody[index] !== jsonColonByte) {
      return result(rawBody.length)
    }
    index = skipJsonWhitespace(rawBody, index + 1)
    if (key.value === 'thinkingConfig') {
      camelThinkingIsObject = rawBody[index] === jsonObjectOpenByte
      const nested = readJsonObjectStringProperty(rawBody, index, 'thinkingLevel')
      camelThinkingEffort = normalizeUsageReasoningEffort(nested.value)
      index = nested.nextIndex
      continue
    }
    if (key.value === 'thinking_config') {
      snakeThinkingIsObject = rawBody[index] === jsonObjectOpenByte
      const nested = readJsonObjectStringProperty(rawBody, index, 'thinking_level')
      snakeThinkingEffort = normalizeUsageReasoningEffort(nested.value)
      index = nested.nextIndex
      continue
    }
    if (key.value === 'responseModalities') {
      camelModalitiesDefined = !jsonValueIsNull(rawBody, index)
      const modalities = inspectJsonStringArrayForImage(rawBody, index)
      camelModalitiesImage = modalities.imageOutput
      index = modalities.nextIndex
      continue
    }
    if (key.value === 'response_modalities') {
      const modalities = inspectJsonStringArrayForImage(rawBody, index)
      snakeModalitiesImage = modalities.imageOutput
      index = modalities.nextIndex
      continue
    }
    if (key.value === 'responseMimeType') {
      camelMimeDefined = !jsonValueIsNull(rawBody, index)
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        camelMimeImage = /^image\//i.test(value.value.trim())
        index = value.nextIndex
        continue
      }
      camelMimeImage = false
    } else if (key.value === 'response_mime_type') {
      const value = readJsonStringToken(rawBody, index)
      if (value) {
        snakeMimeImage = /^image\//i.test(value.value.trim())
        index = value.nextIndex
        continue
      }
      snakeMimeImage = false
    }
    index = skipJsonValue(rawBody, index).nextIndex
  }
  return result(rawBody.length)
}

function jsonValueIsNull(rawBody: Buffer, index: number): boolean {
  index = skipJsonWhitespace(rawBody, index)
  return rawBody.subarray(index, index + jsonNullBuffer.length).equals(jsonNullBuffer)
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
        value = undefined
      } else {
        const nested = readJsonObjectNestedStringProperty(rawBody, index, propertyPath.slice(1))
        if (!nested.ok) return { nextIndex: nested.nextIndex, value, ok: false }
        value = nested.value
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
  let toolType: string | undefined
  index += 1
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      if (toolType !== undefined) countToolType(toolType, inspection)
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
        toolType = value.value
        index = value.nextIndex
        continue
      }
      toolType = undefined
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
  let choiceType: string | undefined
  let nestedTools = createEmptyImageGenerationToolInspection()
  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === jsonObjectCloseByte) {
      if (choiceType === 'image_generation') inspection.forcedImageGeneration = true
      mergeImageGenerationToolInspection(inspection, nestedTools)
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
        choiceType = value.value
        index = value.nextIndex
        continue
      }
      choiceType = undefined
    } else if (key.value === 'tools') {
      const result = inspectJsonToolDefinitions(rawBody, index)
      nestedTools = result.inspection
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
  return decodeJsonStringToken(rawBody, index, nextIndex)
}

function decodeJsonStringToken(
  rawBody: Buffer,
  index: number,
  nextIndex: number
): { value: string; nextIndex: number } | undefined {
  if (rawBody.subarray(index + 1, nextIndex - 1).indexOf(jsonEscapeByte) === -1) {
    return { value: rawBody.toString('utf8', index + 1, nextIndex - 1), nextIndex }
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

function jsonValueIsTruthy(rawBody: Buffer, index: number): boolean {
  index = skipJsonWhitespace(rawBody, index)
  const byte = rawBody[index]
  if (byte === jsonObjectOpenByte || byte === jsonArrayOpenByte) return true
  if (byte === jsonStringByte) return Boolean(readJsonStringToken(rawBody, index)?.value)
  if (rawBody.subarray(index, index + 4).equals(jsonTrueBuffer)) return true
  if (
    rawBody.subarray(index, index + 4).equals(jsonNullBuffer)
    || rawBody.subarray(index, index + 5).equals(jsonFalseBuffer)
  ) {
    return false
  }
  const skipped = skipJsonValue(rawBody, index)
  if (!skipped.ok) return false
  const value = Number(rawBody.toString('utf8', index, skipped.nextIndex).trim())
  return !Number.isNaN(value) && value !== 0
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

  const stack = new CompactJsonFrameStack()
  stack.push(firstByte)
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

let gatewayJsonMetadataScannerStackObserverForTest: ((capacityBytes: number) => void) | undefined

export function setGatewayJsonMetadataScannerStackObserverForTest(
  observer: ((capacityBytes: number) => void) | undefined
): void {
  gatewayJsonMetadataScannerStackObserverForTest = observer
}

class CompactJsonFrameStack {
  private frames = new Uint8Array(256)
  length = 0

  constructor() {
    gatewayJsonMetadataScannerStackObserverForTest?.(this.frames.byteLength)
  }

  push(frame: number): void {
    if (this.length >= this.frames.length) {
      const expanded = new Uint8Array(this.frames.length * 2)
      expanded.set(this.frames)
      this.frames = expanded
      gatewayJsonMetadataScannerStackObserverForTest?.(this.frames.byteLength)
    }
    this.frames[this.length] = frame
    this.length += 1
  }

  peek(): number | undefined {
    return this.length > 0 ? this.frames[this.length - 1] : undefined
  }

  replaceTop(frame: number): boolean {
    if (this.length === 0) return false
    this.frames[this.length - 1] = frame
    return true
  }

  pop(): number | undefined {
    if (this.length === 0) return undefined
    this.length -= 1
    return this.frames[this.length]
  }
}

const jsonFrameStateMask = 0x07
const jsonFrameTypeIsCompactionFlag = 0x08
const jsonFrameKeyIsTypeFlag = 0x10
const jsonObjectFirstKeyOrEndState = 0
const jsonObjectKeyState = 1
const jsonObjectColonState = 2
const jsonObjectValueState = 3
const jsonObjectCommaOrEndState = 4
const jsonArrayFirstValueOrEndState = 5
const jsonArrayValueState = 6
const jsonArrayCommaOrEndState = 7

function jsonFrameState(frame: number): number {
  return frame & jsonFrameStateMask
}

function jsonFrameWithState(frame: number, state: number): number {
  return (frame & ~jsonFrameStateMask) | state
}

function isJsonObjectFrame(frame: number): boolean {
  return jsonFrameState(frame) <= jsonObjectCommaOrEndState
}

function isValidJsonDocument(
  rawBody: Buffer,
  callbacks: {
    onCodexCompactionTrigger: () => void
    onTopLevelProperty: (key: string, startIndex: number, endIndex: number) => void
  }
): boolean {
  const stack = new CompactJsonFrameStack()
  let index = skipJsonWhitespace(rawBody, 0)
  let rootComplete = false
  let currentTopLevelKey: string | undefined
  let pendingTopLevelProperty: { key: string; startIndex: number } | undefined

  const consumeValue = (): boolean => {
    index = skipJsonWhitespace(rawBody, index)
    const byte = rawBody[index]
    if (byte === jsonObjectOpenByte) {
      index += 1
      stack.push(jsonObjectFirstKeyOrEndState)
      return true
    }
    if (byte === jsonArrayOpenByte) {
      index += 1
      stack.push(jsonArrayFirstValueOrEndState)
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

  const closeCurrentFrame = (): boolean => {
    const frame = stack.pop()
    if (frame === undefined) return false
    if (isJsonObjectFrame(frame) && (frame & jsonFrameTypeIsCompactionFlag) !== 0) {
      callbacks.onCodexCompactionTrigger()
    }
    if (pendingTopLevelProperty && stack.length === 1) {
      callbacks.onTopLevelProperty(pendingTopLevelProperty.key, pendingTopLevelProperty.startIndex, index)
      pendingTopLevelProperty = undefined
    }
    return true
  }

  if (!consumeValue()) return false
  if (stack.length === 0) rootComplete = true

  while (!rootComplete) {
    index = skipJsonWhitespace(rawBody, index)
    const frame = stack.peek()
    if (frame === undefined) return false
    const state = jsonFrameState(frame)

    if (isJsonObjectFrame(frame)) {
      if (state === jsonObjectFirstKeyOrEndState && rawBody[index] === jsonObjectCloseByte) {
        index += 1
        if (!closeCurrentFrame()) return false
      } else if (state === jsonObjectFirstKeyOrEndState || state === jsonObjectKeyState) {
        const nextIndex = skipValidJsonString(rawBody, index)
        if (nextIndex === undefined) return false
        const key = decodeJsonInspectionKey(rawBody, index, nextIndex, stack.length === 1)
        index = nextIndex
        if (stack.length === 1) currentTopLevelKey = key
        let nextFrame = jsonFrameWithState(frame, jsonObjectColonState) & ~jsonFrameKeyIsTypeFlag
        if (key === 'type') nextFrame |= jsonFrameKeyIsTypeFlag
        if (!stack.replaceTop(nextFrame)) return false
        continue
      } else if (state === jsonObjectColonState) {
        if (rawBody[index] !== jsonColonByte) return false
        index += 1
        if (!stack.replaceTop(jsonFrameWithState(frame, jsonObjectValueState))) return false
        continue
      } else if (state === jsonObjectValueState) {
        const key = stack.length === 1
          ? currentTopLevelKey
          : (frame & jsonFrameKeyIsTypeFlag) !== 0
            ? 'type'
            : undefined
        const valueStartIndex = skipJsonWhitespace(rawBody, index)
        let nextFrame = jsonFrameWithState(frame, jsonObjectCommaOrEndState) & ~jsonFrameKeyIsTypeFlag
        if ((frame & jsonFrameKeyIsTypeFlag) !== 0) {
          const value = readJsonStringToken(rawBody, index)
          nextFrame = value?.value === 'compaction_trigger'
            ? nextFrame | jsonFrameTypeIsCompactionFlag
            : nextFrame & ~jsonFrameTypeIsCompactionFlag
        }
        if (stack.length === 1) currentTopLevelKey = undefined
        if (!stack.replaceTop(nextFrame)) return false
        const depth = stack.length
        if (!consumeValue()) return false
        if (depth === 1 && key !== undefined) {
          if (stack.length === depth) {
            callbacks.onTopLevelProperty(key, valueStartIndex, index)
          } else {
            pendingTopLevelProperty = { key, startIndex: valueStartIndex }
          }
        }
        if (stack.length === depth) continue
        continue
      } else if (rawBody[index] === jsonCommaByte) {
        index += 1
        if (!stack.replaceTop(jsonFrameWithState(frame, jsonObjectKeyState))) return false
        continue
      } else if (rawBody[index] === jsonObjectCloseByte) {
        index += 1
        if (!closeCurrentFrame()) return false
      } else {
        return false
      }
    } else if (state === jsonArrayFirstValueOrEndState && rawBody[index] === jsonArrayCloseByte) {
      index += 1
      if (!closeCurrentFrame()) return false
    } else if (state === jsonArrayFirstValueOrEndState || state === jsonArrayValueState) {
      if (!stack.replaceTop(jsonFrameWithState(frame, jsonArrayCommaOrEndState))) return false
      const depth = stack.length
      if (!consumeValue()) return false
      if (stack.length === depth) continue
      continue
    } else if (rawBody[index] === jsonCommaByte) {
      index += 1
      if (!stack.replaceTop(jsonFrameWithState(frame, jsonArrayValueState))) return false
      continue
    } else if (rawBody[index] === jsonArrayCloseByte) {
      index += 1
      if (!closeCurrentFrame()) return false
    } else {
      return false
    }

    if (stack.length === 0) {
      rootComplete = true
    }
  }

  return skipJsonWhitespace(rawBody, index) === rawBody.length
}

function decodeJsonInspectionKey(
  rawBody: Buffer,
  index: number,
  nextIndex: number,
  topLevel: boolean
): string | undefined {
  const content = rawBody.subarray(index + 1, nextIndex - 1)
  if (content.indexOf(jsonEscapeByte) === -1) {
    if (content.equals(jsonTypeKeyBuffer)) return 'type'
    if (!topLevel) return undefined
    for (const candidate of topLevelMetadataKeyBuffers) {
      if (content.equals(candidate.buffer)) return candidate.key
    }
    return undefined
  }
  const decoded = decodeJsonStringToken(rawBody, index, nextIndex)?.value
  if (decoded === 'type') return decoded
  return topLevel && topLevelMetadataKeys.has(decoded ?? '') ? decoded : undefined
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
const jsonTypeKeyBuffer = Buffer.from('type')
const topLevelMetadataKeyBuffers = [
  'model',
  'service_tier',
  'reasoning_effort',
  'reasoning',
  'output_config',
  'generationConfig',
  'generation_config',
  'stream',
  'max_output_tokens',
  'max_tokens',
  'tools',
  'tool_choice',
  'response_format'
].map((key) => ({ key, buffer: Buffer.from(key) }))
const topLevelMetadataKeys = new Set(topLevelMetadataKeyBuffers.map(({ key }) => key))

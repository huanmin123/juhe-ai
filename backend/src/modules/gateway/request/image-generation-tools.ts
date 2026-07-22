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

export interface ImageGenerationToolInspection {
  imageToolCount: number
  nonImageToolCount: number
  forcedImageGeneration: boolean
}

interface GatewayAutoImageGenerationToolDowngradeResult extends GatewayImageGenerationToolDowngradeResult {
  body?: Record<string, unknown>
}

export function requestBodyHasImageGenerationHint(value: unknown): boolean {
  const inspection = inspectImageGenerationTools(value)
  return inspection.imageToolCount > 0 || inspection.forcedImageGeneration
}

export function requestBodyForcesImageGeneration(value: unknown): boolean {
  return inspectImageGenerationTools(value).forcedImageGeneration
}

export function inspectImageGenerationTools(value: unknown): ImageGenerationToolInspection {
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

export function downgradeAutoImageGenerationToolsInBody(
  body: Record<string, unknown> | undefined
): GatewayAutoImageGenerationToolDowngradeResult {
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

  return {
    downgraded: true,
    removedToolCount,
    reason: 'auto_image_generation_tool_removed',
    body: nextBody
  }
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

export type AssistantProcessStatus = 'started' | 'completed' | 'failed' | 'canceled'
export type AssistantToolStatus = AssistantProcessStatus | 'updated'
export type AssistantTimelineTerminalStatus = Exclude<AssistantProcessStatus, 'started'>

export type AssistantJsonValue = null | boolean | number | string | AssistantJsonValue[] | AssistantJsonObject
export interface AssistantJsonObject { [key: string]: AssistantJsonValue }

export interface AssistantOutputTextBlock {
  readonly type: 'output_text'
  readonly blockId: string
  readonly order: number
  text: string
}

export interface AssistantReasoningBlock {
  readonly type: 'reasoning'
  readonly blockId: string
  readonly order: number
  text: string
  status: AssistantProcessStatus
}

export interface AssistantToolCallBlock {
  readonly type: 'tool_call'
  readonly blockId: string
  readonly order: number
  readonly callId: string
  readonly toolType: string
  status: AssistantToolStatus
  item?: AssistantJsonObject
}

export interface AssistantOutputImageBlock {
  readonly type: 'output_image'
  readonly blockId: string
  readonly order: number
  readonly assetId: string
  status: AssistantProcessStatus
  mimeType?: string
  width?: number
  height?: number
  revisedPrompt?: string
}

export type AssistantContentBlock = AssistantOutputTextBlock | AssistantReasoningBlock | AssistantToolCallBlock | AssistantOutputImageBlock

export interface AssistantTimelineSnapshot {
  status: AssistantProcessStatus
  contentText: string
  contentBlocks: AssistantContentBlock[]
}

export interface AssistantToolStartInput {
  callId: string
  toolType: string
  item?: AssistantJsonObject
}

export interface AssistantToolUpdateInput {
  callId: string
  status: AssistantToolStatus
  item?: AssistantJsonObject
}

export interface AssistantOutputImageStartInput {
  assetId: string
  mimeType?: string
  width?: number
  height?: number
  revisedPrompt?: string
}

export interface AssistantOutputImageUpdateInput extends AssistantOutputImageStartInput {
  status: AssistantProcessStatus
}

export class AssistantTimeline {
  private readonly blocks: AssistantContentBlock[] = []
  private status: AssistantProcessStatus = 'started'

  appendText(text: string): AssistantOutputTextBlock | undefined {
    this.ensureMutable()
    if (text.length === 0) return undefined
    const last = this.blocks.at(-1)
    if (last?.type === 'output_text') {
      last.text += text
      return cloneBlock(last) as AssistantOutputTextBlock
    }
    const block: AssistantOutputTextBlock = {
      type: 'output_text',
      blockId: this.nextBlockId(),
      order: this.blocks.length + 1,
      text
    }
    this.blocks.push(block)
    return cloneBlock(block) as AssistantOutputTextBlock
  }

  appendReasoning(text: string): AssistantReasoningBlock | undefined {
    this.ensureMutable()
    if (text.length === 0) return undefined
    const last = this.blocks.at(-1)
    if (last?.type === 'reasoning' && last.status === 'started') {
      last.text += text
      return cloneBlock(last) as AssistantReasoningBlock
    }
    const block: AssistantReasoningBlock = {
      type: 'reasoning',
      blockId: this.nextBlockId(),
      order: this.blocks.length + 1,
      text,
      status: 'started'
    }
    this.blocks.push(block)
    return cloneBlock(block) as AssistantReasoningBlock
  }

  startTool(input: AssistantToolStartInput): AssistantToolCallBlock {
    this.ensureMutable()
    const callId = requireIdentifier(input.callId, 'callId')
    const toolType = requireIdentifier(input.toolType, 'toolType')
    const nextItem = input.item === undefined ? undefined : cloneJsonObject(input.item)
    const existing = this.blocks.find((block): block is AssistantToolCallBlock => block.type === 'tool_call' && block.callId === callId)
    if (existing) {
      if (existing.toolType !== toolType) throw new Error(`助手工具类型不一致: ${callId}`)
      if (isTerminalToolStatus(existing.status)) return cloneBlock(existing) as AssistantToolCallBlock
      if (nextItem !== undefined && !existing.item) existing.item = nextItem
      return cloneBlock(existing) as AssistantToolCallBlock
    }
    const block: AssistantToolCallBlock = {
      type: 'tool_call',
      blockId: this.nextBlockId(),
      order: this.blocks.length + 1,
      callId,
      toolType,
      status: 'started'
    }
    if (nextItem !== undefined) block.item = nextItem
    this.blocks.push(block)
    return cloneBlock(block) as AssistantToolCallBlock
  }

  updateTool(input: AssistantToolUpdateInput): AssistantToolCallBlock {
    this.ensureMutable()
    const callId = requireIdentifier(input.callId, 'callId')
    const block = this.blocks.find((candidate): candidate is AssistantToolCallBlock => candidate.type === 'tool_call' && candidate.callId === callId)
    if (!block) throw new Error(`未知的助手工具调用: ${callId}`)
    const nextItem = input.item === undefined ? undefined : cloneJsonObject(input.item)
    if (isTerminalToolStatus(block.status)) return cloneBlock(block) as AssistantToolCallBlock
    if (input.status !== 'started' || block.status === 'started') block.status = input.status
    if (nextItem !== undefined) block.item = nextItem
    return cloneBlock(block) as AssistantToolCallBlock
  }

  startImage(input: AssistantOutputImageStartInput): AssistantOutputImageBlock {
    this.ensureMutable()
    const assetId = requireIdentifier(input.assetId, 'assetId')
    const existing = this.blocks.find((block): block is AssistantOutputImageBlock => block.type === 'output_image' && block.assetId === assetId)
    if (existing) return cloneBlock(existing) as AssistantOutputImageBlock
    const block: AssistantOutputImageBlock = {
      type: 'output_image',
      blockId: this.nextBlockId(),
      order: this.blocks.length + 1,
      assetId,
      status: 'started',
      ...(input.mimeType ? { mimeType: input.mimeType } : {}),
      ...(input.width ? { width: input.width } : {}),
      ...(input.height ? { height: input.height } : {}),
      ...(input.revisedPrompt ? { revisedPrompt: input.revisedPrompt } : {})
    }
    this.blocks.push(block)
    return cloneBlock(block) as AssistantOutputImageBlock
  }

  updateImage(input: AssistantOutputImageUpdateInput): AssistantOutputImageBlock {
    this.ensureMutable()
    const assetId = requireIdentifier(input.assetId, 'assetId')
    const block = this.blocks.find((candidate): candidate is AssistantOutputImageBlock => candidate.type === 'output_image' && candidate.assetId === assetId)
    if (!block) {
      const created = this.startImage(input)
      if (input.status === 'started') return created
      return this.updateImage(input)
    }
    if (isTerminalProcessStatus(block.status)) return cloneBlock(block) as AssistantOutputImageBlock
    block.status = input.status
    if (input.mimeType) block.mimeType = input.mimeType
    if (input.width) block.width = input.width
    if (input.height) block.height = input.height
    if (input.revisedPrompt) block.revisedPrompt = input.revisedPrompt
    return cloneBlock(block) as AssistantOutputImageBlock
  }

  completeBlock(blockId: string): AssistantContentBlock {
    this.ensureMutable()
    const block = this.blocks.find((candidate) => candidate.blockId === blockId)
    if (!block) throw new Error(`未知的助手内容块: ${blockId}`)
    if (block.type === 'reasoning' && isActiveProcessStatus(block.status)) block.status = 'completed'
    if (block.type === 'tool_call' && isActiveToolStatus(block.status)) block.status = 'completed'
    return cloneBlock(block)
  }

  finalize(status: AssistantTimelineTerminalStatus): AssistantTimelineSnapshot {
    if (this.status !== 'started') return this.snapshot()
    for (const block of this.blocks) {
      if (block.type === 'reasoning' && isActiveProcessStatus(block.status)) block.status = status
      if (block.type === 'tool_call' && isActiveToolStatus(block.status)) block.status = status
      if (block.type === 'output_image' && isActiveProcessStatus(block.status)) block.status = status
    }
    this.status = status
    return this.snapshot()
  }

  snapshot(): AssistantTimelineSnapshot {
    const contentBlocks = this.blocks.map((block) => cloneBlock(block))
    return {
      status: this.status,
      contentText: contentBlocks
        .filter((block): block is AssistantOutputTextBlock => block.type === 'output_text')
        .sort((left, right) => left.order - right.order)
        .map((block) => block.text)
        .join(''),
      contentBlocks
    }
  }

  private nextBlockId(): string {
    return `assistant_block_${this.blocks.length + 1}`
  }

  private ensureMutable(): void {
    if (this.status !== 'started') throw new Error('助手时间线已进入终态')
  }
}

function isActiveProcessStatus(status: AssistantProcessStatus): status is 'started' {
  return status === 'started'
}

function isActiveToolStatus(status: AssistantToolStatus): status is 'started' | 'updated' {
  return status === 'started' || status === 'updated'
}

function isTerminalProcessStatus(status: AssistantProcessStatus): status is 'completed' | 'failed' | 'canceled' {
  return status === 'completed' || status === 'failed' || status === 'canceled'
}

function isTerminalToolStatus(status: AssistantToolStatus): status is 'completed' | 'failed' | 'canceled' {
  return status === 'completed' || status === 'failed' || status === 'canceled'
}

function requireIdentifier(value: string, name: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`助手工具 ${name} 不能为空`)
  return value
}

function cloneBlock(block: AssistantContentBlock): AssistantContentBlock {
  if (block.type === 'output_text') return { ...block }
  if (block.type === 'reasoning') return { ...block }
  if (block.type === 'output_image') return { ...block }
  return block.item === undefined ? { ...block } : { ...block, item: cloneJsonObject(block.item) }
}

function cloneJsonObject(value: AssistantJsonObject): AssistantJsonObject {
  try {
    const cloned = cloneJsonValue(value, new Set<object>())
    if (!cloned || typeof cloned !== 'object' || Array.isArray(cloned)) throw new TypeError('item must be an object')
    return cloned
  } catch (error) {
    throw new Error('助手工具 item 不可序列化为严格 JSON', { cause: error })
  }
}

function cloneJsonValue(value: unknown, ancestors: Set<object>): AssistantJsonValue {
  if (value === null || typeof value === 'boolean' || typeof value === 'string') return value
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new TypeError('JSON number must be finite')
    return value
  }
  if (typeof value !== 'object') throw new TypeError(`unsupported JSON value: ${typeof value}`)
  if (ancestors.has(value)) throw new TypeError('circular JSON value')
  ancestors.add(value)
  try {
    if (Array.isArray(value)) return cloneJsonArray(value, ancestors)
    const prototype = Object.getPrototypeOf(value)
    if (prototype !== Object.prototype && prototype !== null) throw new TypeError('JSON object must be plain')
    const cloned: AssistantJsonObject = {}
    for (const key of Reflect.ownKeys(value)) {
      if (typeof key !== 'string') throw new TypeError('JSON object cannot contain symbol keys')
      const descriptor = Object.getOwnPropertyDescriptor(value, key)
      if (!descriptor?.enumerable || !('value' in descriptor)) throw new TypeError('JSON object properties must be enumerable values')
      Object.defineProperty(cloned, key, {
        configurable: true,
        enumerable: true,
        value: cloneJsonValue(descriptor.value, ancestors),
        writable: true
      })
    }
    return cloned
  } finally {
    ancestors.delete(value)
  }
}

function cloneJsonArray(value: unknown[], ancestors: Set<object>): AssistantJsonValue[] {
  const allowedKeys = new Set<PropertyKey>(['length'])
  for (let index = 0; index < value.length; index += 1) allowedKeys.add(String(index))
  for (const key of Reflect.ownKeys(value)) {
    if (!allowedKeys.has(key)) throw new TypeError('JSON array cannot contain holes or extra properties')
  }
  const cloned: AssistantJsonValue[] = []
  for (let index = 0; index < value.length; index += 1) {
    const descriptor = Object.getOwnPropertyDescriptor(value, String(index))
    if (!descriptor?.enumerable || !('value' in descriptor)) throw new TypeError('JSON array cannot contain holes or accessors')
    cloned.push(cloneJsonValue(descriptor.value, ancestors))
  }
  return cloned
}

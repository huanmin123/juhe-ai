import {
  type ChatInternalToolDefinition,
  type ChatToolExecutionContext,
  type ChatToolExecutionResult,
  type ChatToolRuntimeEnvironment
} from './contracts.js'
import {
  ChatToolSchemaError,
  compileChatToolSchema,
  validateChatToolArguments,
  type CompiledChatToolSchema
} from './schema.js'

interface RegisteredChatTool {
  definition: ChatInternalToolDefinition
  schema: CompiledChatToolSchema
}

export class ChatInternalToolRegistry {
  private readonly toolsByName = new Map<string, RegisteredChatTool>()
  private readonly toolIds = new Set<string>()

  constructor(private readonly options: {
    environment: ChatToolRuntimeEnvironment
    internalToolsEnabled: boolean
    imageGenerationEnabled?: boolean
  }) {}

  register(definition: ChatInternalToolDefinition): void {
    assertToolDefinition(definition)
    if (this.toolsByName.has(definition.modelName) || this.toolIds.has(definition.id)) {
      throw new Error(`内部工具重复注册：${definition.modelName}`)
    }
    const schema = compileChatToolSchema(definition.inputSchema)
    this.toolsByName.set(definition.modelName, { definition, schema })
    this.toolIds.add(definition.id)
  }

  resolve(input: { functionCalling: boolean }): ChatInternalToolDefinition[] {
    if (!input.functionCalling) return []
    return [...this.toolsByName.values()]
      .map((item) => item.definition)
      .filter((definition) => this.isAvailable(definition))
  }

  validateArguments(toolName: string, argumentsJson: string): Record<string, unknown> {
    const registered = this.requireAvailable(toolName)
    return validateChatToolArguments(registered.schema, argumentsJson, registered.definition.limits.maxArgumentBytes)
  }

  normalizeArguments(toolName: string, argumentsJson: string): { input: Record<string, unknown>; normalizedJson: string } {
    const input = this.validateArguments(toolName, argumentsJson)
    return { input, normalizedJson: stableJson(input) }
  }

  definition(toolName: string): ChatInternalToolDefinition {
    return this.requireAvailable(toolName).definition
  }

  async execute(input: {
    toolName: string
    argumentsJson: string
    context: ChatToolExecutionContext
  }): Promise<ChatToolExecutionResult> {
    const registered = this.requireAvailable(input.toolName)
    const parsed = validateChatToolArguments(
      registered.schema,
      input.argumentsJson,
      registered.definition.limits.maxArgumentBytes
    )
    throwIfAborted(input.context.signal)
    const result = await executeWithLimits(
      (signal) => registered.definition.execute(parsed, { ...input.context, signal }),
      input.context.signal,
      registered.definition.limits.timeoutMs
    )
    assertResult(result, registered.definition.limits.maxResultBytes)
    return result
  }

  private requireAvailable(toolName: string): RegisteredChatTool {
    const normalized = normalizedToolName(toolName)
    const registered = this.toolsByName.get(normalized)
    if (!registered || !this.isAvailable(registered.definition)) {
      throw new ChatInternalToolError('tool_not_available', `tool_not_available: ${normalized || 'unknown'}`)
    }
    return registered
  }

  private isAvailable(definition: ChatInternalToolDefinition): boolean {
    const environments = definition.availability.environments
    if (environments && !environments.includes(this.options.environment)) return false
    if (definition.availability.requiresInternalToolsEnabled && !this.options.internalToolsEnabled) return false
    if (definition.availability.requiresImageGenerationEnabled && !this.options.imageGenerationEnabled) return false
    if (this.options.environment === 'production' && definition.id === 'diagnostic.echo') return false
    return true
  }
}

export class ChatInternalToolError extends Error {
  constructor(public readonly code: string, message: string) {
    super(message)
    this.name = 'ChatInternalToolError'
  }
}

function assertToolDefinition(definition: ChatInternalToolDefinition): void {
  if (!/^[a-z][a-z0-9._-]{1,79}$/u.test(definition.id)) throw new Error('内部工具 id 无效')
  if (!/^[a-z][a-z0-9_]{1,63}$/u.test(definition.modelName)) throw new Error('内部工具 modelName 无效')
  if (!definition.version.trim() || !definition.title.trim() || !definition.description.trim()) throw new Error('内部工具元数据不完整')
  if (definition.executionOwner !== 'application') throw new Error('内部工具 executionOwner 必须是 application')
  for (const [name, value] of Object.entries(definition.limits)) {
    if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`内部工具限制 ${name} 必须是正整数`)
  }
}

async function executeWithLimits<T>(operation: (signal: AbortSignal) => Promise<T>, parentSignal: AbortSignal, timeoutMs: number): Promise<T> {
  const timeoutController = new AbortController()
  const timeout = setTimeout(() => {
    timeoutController.abort(new ChatInternalToolError('tool_timeout', `工具执行超过 ${timeoutMs}ms 超时`))
  }, timeoutMs)
  const signal = AbortSignal.any([parentSignal, timeoutController.signal])
  if (signal.aborted) throw abortReason(signal)
  try {
    return await new Promise<T>((resolve, reject) => {
      const onAbort = () => reject(abortReason(signal))
      signal.addEventListener('abort', onAbort, { once: true })
      operation(signal).then(resolve, reject).finally(() => signal.removeEventListener('abort', onAbort)).catch(() => undefined)
    })
  } finally {
    clearTimeout(timeout)
  }
}

function abortReason(signal: AbortSignal): Error {
  if (signal.reason instanceof Error) return signal.reason
  return new DOMException('工具执行已取消', 'AbortError')
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw abortReason(signal)
}

function assertResult(result: ChatToolExecutionResult, maxResultBytes: number): void {
  if (!result || typeof result.modelOutput !== 'string') throw new Error('内部工具结果缺少 modelOutput')
  if (Buffer.byteLength(result.modelOutput, 'utf8') > maxResultBytes) {
    throw new ChatInternalToolError('tool_result_too_large', `工具结果超过 ${maxResultBytes} 字节上限`)
  }
  if (result.publicResult !== undefined) {
    const bytes = Buffer.byteLength(JSON.stringify(result.publicResult), 'utf8')
    if (bytes > maxResultBytes) throw new ChatInternalToolError('tool_result_too_large', `工具公开结果超过 ${maxResultBytes} 字节上限`)
  }
}

function normalizedToolName(value: string): string {
  return typeof value === 'string' ? value.trim() : ''
}

function stableJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`
  if (value && typeof value === 'object') {
    return `{${Object.entries(value as Record<string, unknown>)
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([key, item]) => `${JSON.stringify(key)}:${stableJson(item)}`)
      .join(',')}}`
  }
  return JSON.stringify(value)
}

export { ChatToolSchemaError }

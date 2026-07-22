import {
  type ChatInternalToolDefinition,
  type ChatToolCall,
  type ChatToolExecutionContext,
  type ChatToolExecutionEvent,
  type ChatToolExecutionOutput
} from './contracts.js'
import { ChatInternalToolError, ChatInternalToolRegistry } from './registry.js'
import { ChatToolSchemaError } from './schema.js'
import {
  buildChatToolContinuation,
  compileChatInternalTools,
  type ChatToolProtocol
} from './protocol.js'

export interface ChatToolModelTurn {
  content: string
  finishReason?: string
  continuationItems: unknown[]
  toolCalls: ChatToolCall[]
  inputTokens?: number
  outputTokens?: number
}

export interface ChatToolModelRequest {
  protocol: ChatToolProtocol
  round: number
  tools: Array<Record<string, unknown>>
  continuation: readonly unknown[]
}

export interface ChatToolOrchestratorResult {
  content: string
  finishReason?: string
  modelRounds: number
  toolCalls: number
  inputTokens?: number
  outputTokens?: number
}

export class ChatInternalToolOrchestrator {
  private readonly seenResults = new Map<string, ChatToolExecutionOutput>()
  private totalToolCalls = 0
  private imageCalls = 0
  private correctableFailures = 0

  constructor(private readonly options: {
    registry: ChatInternalToolRegistry
    tools: readonly ChatInternalToolDefinition[]
    context: ChatToolExecutionContext
    limits: { maxModelRounds: number; maxToolCalls: number; maxImageCalls: number }
    publish?: (event: ChatToolExecutionEvent) => void
  }) {}

  async run(input: {
    protocol: ChatToolProtocol
    invokeModel: (request: ChatToolModelRequest) => Promise<ChatToolModelTurn>
  }): Promise<ChatToolOrchestratorResult> {
    let continuation: unknown[] = []
    let inputTokens: number | undefined
    let outputTokens: number | undefined
    for (let round = 1; ; round += 1) {
      throwIfAborted(this.options.context.signal)
      if (round > this.options.limits.maxModelRounds) {
        throw new Error(`模型请求轮次超过 ${this.options.limits.maxModelRounds}`)
      }
      const turn = await input.invokeModel({
        protocol: input.protocol,
        round,
        tools: compileChatInternalTools(input.protocol, this.options.tools),
        continuation
      })
      inputTokens = turn.inputTokens ?? inputTokens
      outputTokens = turn.outputTokens ?? outputTokens
      if (!turn.toolCalls.length) {
        return {
          content: turn.content,
          finishReason: turn.finishReason,
          modelRounds: round,
          toolCalls: this.totalToolCalls,
          inputTokens,
          outputTokens
        }
      }
      if (round >= this.options.limits.maxModelRounds) {
        throw new Error(`工具循环达到模型请求轮次上限 ${this.options.limits.maxModelRounds}`)
      }
      const outputs = await this.executeCalls(turn.toolCalls)
      continuation = [
        ...continuation,
        ...buildChatToolContinuation(input.protocol, turn.continuationItems, outputs)
      ]
    }
  }

  private async executeCalls(calls: readonly ChatToolCall[]): Promise<ChatToolExecutionOutput[]> {
    const ordered = [...calls].sort((left, right) => left.sourceOrder - right.sourceOrder)
    const outputs: ChatToolExecutionOutput[] = []
    for (const call of ordered) {
      throwIfAborted(this.options.context.signal)
      const eventBase = { callId: call.callId, toolName: call.toolName, executionOwner: 'application' as const }
      this.publish({ status: 'started', ...eventBase })
      try {
        this.totalToolCalls += 1
        if (this.totalToolCalls > this.options.limits.maxToolCalls) {
          throw new ChatInternalToolError('tool_call_limit_exceeded', `工具调用次数超过 ${this.options.limits.maxToolCalls}`)
        }
        if (call.toolName === 'generate_image') {
          this.imageCalls += 1
          if (this.imageCalls > this.options.limits.maxImageCalls) {
            throw new ChatInternalToolError('image_tool_call_limit_exceeded', `单轮图片生成调用次数超过 ${this.options.limits.maxImageCalls}`)
          }
        }
        const definition = this.options.registry.definition(call.toolName)
        const { normalizedJson } = this.options.registry.normalizeArguments(call.toolName, call.argumentsJson)
        const cacheKey = `${definition.modelName}@${definition.version}:${normalizedJson}`
        const cached = definition.duplicatePolicy === 'reuse_exact' ? this.seenResults.get(cacheKey) : undefined
        if (cached) {
          const reused = { ...cached, callId: call.callId, reused: true }
          outputs.push(reused)
          this.publish({ status: 'completed', callId: call.callId, toolName: definition.modelName, executionOwner: definition.executionOwner, publicResult: reused.publicResult, reused: true })
          continue
        }
        const result = await this.options.registry.execute({
          toolName: call.toolName,
          argumentsJson: normalizedJson,
          context: this.options.context
        })
        const output = { callId: call.callId, toolName: definition.modelName, modelOutput: result.modelOutput, publicResult: result.publicResult, reused: false }
        this.seenResults.set(cacheKey, output)
        outputs.push(output)
        this.publish({ status: 'completed', callId: call.callId, toolName: definition.modelName, executionOwner: definition.executionOwner, publicResult: result.publicResult, reused: false })
      } catch (error) {
        const canceled = this.options.context.signal.aborted || isAbortError(error)
        const errorCode = canceled ? 'canceled' : chatToolErrorCode(error)
        this.publish({ status: canceled ? 'canceled' : 'failed', ...eventBase, errorCode })
        if (canceled) throw error
        if (this.correctableFailures >= 1) throw error
        this.correctableFailures += 1
        outputs.push({
          callId: call.callId,
          toolName: call.toolName,
          modelOutput: JSON.stringify({ ok: false, error: { code: errorCode, message: chatToolErrorMessage(errorCode) } }),
          reused: false
        })
      }
    }
    return outputs
  }

  private publish(event: ChatToolExecutionEvent): void {
    this.options.publish?.(event)
  }
}

function throwIfAborted(signal: AbortSignal): void {
  if (!signal.aborted) return
  throw signal.reason instanceof Error ? signal.reason : new DOMException('工具执行已取消', 'AbortError')
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
    || error instanceof Error && error.name === 'AbortError'
}

function chatToolErrorCode(error: unknown): string {
  if (error instanceof ChatInternalToolError || error instanceof ChatToolSchemaError) return error.code
  return 'tool_execution_failed'
}

function chatToolErrorMessage(code: string): string {
  return ({
    tool_not_available: '请求的工具当前不可用',
    tool_arguments_too_large: '工具参数超过允许上限',
    tool_arguments_invalid_json: '工具参数不是有效 JSON',
    tool_arguments_invalid: '工具参数不符合要求',
    tool_timeout: '工具执行超时',
    tool_result_too_large: '工具结果超过允许上限',
    tool_call_limit_exceeded: '本轮工具调用次数已达到上限',
    image_tool_call_limit_exceeded: '本轮图片生成次数已达到上限'
  } as Record<string, string>)[code] ?? '工具执行失败'
}

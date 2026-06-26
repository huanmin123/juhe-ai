import { runtimeConfig, type HostedToolRuntimeMode } from '../../../../config/runtime.js'
import type { AccountModelMappingSourceEndpointFamily } from '../../../../domain/types.js'

export type OpenAIHostedToolRuntimeType =
  | 'code_interpreter'
  | 'computer'
  | 'mcp'
  | 'shell'
  | 'skills'
  | 'tool_search'

export interface OpenAIHostedToolRuntimeDecision {
  toolType: OpenAIHostedToolRuntimeType
  mode: HostedToolRuntimeMode
  compatibilityDetail: string
  sourceEndpointFamily?: AccountModelMappingSourceEndpointFamily
}

export function resolveOpenAIHostedToolRuntimeDecision(input: {
  toolType: string
  sourceEndpointFamily?: AccountModelMappingSourceEndpointFamily
}): OpenAIHostedToolRuntimeDecision | undefined {
  const toolType = normalizeOpenAIHostedToolRuntimeType(input.toolType)
  if (!toolType) return undefined
  return {
    toolType,
    mode: openAIHostedToolRuntimeMode(toolType),
    compatibilityDetail: runtimeCompatibilityDetailForTool(toolType),
    sourceEndpointFamily: input.sourceEndpointFamily
  }
}

export function openAIHostedToolRuntimeCompatibilityDetail(type: string): string | undefined {
  const toolType = normalizeOpenAIHostedToolRuntimeType(type)
  if (!toolType) return undefined
  return runtimeCompatibilityDetailForTool(toolType)
}

export function normalizeOpenAIHostedToolRuntimeType(type: string): OpenAIHostedToolRuntimeType | undefined {
  if (type === 'container') return 'code_interpreter'
  if (
    type === 'code_interpreter'
    || type === 'computer'
    || type === 'mcp'
    || type === 'shell'
    || type === 'skills'
    || type === 'tool_search'
  ) {
    return type
  }
  return undefined
}

function openAIHostedToolRuntimeMode(toolType: OpenAIHostedToolRuntimeType): HostedToolRuntimeMode {
  if (toolType === 'code_interpreter') return runtimeConfig.hostedToolRuntimes.codeInterpreter
  if (toolType === 'computer') return runtimeConfig.hostedToolRuntimes.computer
  if (toolType === 'mcp') return 'guidance'
  if (toolType === 'shell') return runtimeConfig.hostedToolRuntimes.shell
  if (toolType === 'skills') return runtimeConfig.hostedToolRuntimes.skills
  return runtimeConfig.hostedToolRuntimes.toolSearch
}

function runtimeCompatibilityDetailForTool(toolType: OpenAIHostedToolRuntimeType): string {
  if (toolType === 'code_interpreter') {
    return '需要 Anthropic code execution 能力或网关本地安全沙箱；当前未启用执行器'
  }
  if (toolType === 'computer') {
    return '需要 Anthropic computer use 或网关本地 computer adapter；当前未启用执行器'
  }
  if (toolType === 'mcp') {
    return 'MCP 不在网关服务端执行；请使用客户端本地 MCP，或切换到原生支持该 MCP 能力的上游'
  }
  if (toolType === 'shell' || toolType === 'skills' || toolType === 'tool_search') {
    return '需要调用方本地工具运行时；当前不能由 Anthropic Messages 字段转换凭空执行'
  }
  return '需要先在高兼容能力矩阵中定义映射、模拟或 agent guidance 策略'
}

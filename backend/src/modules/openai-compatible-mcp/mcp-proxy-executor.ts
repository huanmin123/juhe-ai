import { createHash } from 'node:crypto'

import { runtimeConfig, type McpProxyServerRuntimeConfig } from '../../config/runtime.js'
import { stableJsonStringify } from '../../storage/audit-log-stable-json.js'
import {
  consumeOpenAICompatibleMcpApprovalRequest,
  createOpenAICompatibleMcpApprovalRequest,
  resolveOpenAICompatibleMcpApprovalResponse,
  type OpenAICompatibleMcpApprovalScope
} from '../../storage/openai-compatible-mcp-approval.repository.js'
import { createOpenAICompatibleMcpExecutionRecord } from '../../storage/openai-compatible-mcp-execution.repository.js'
import {
  createOpenAICompatibleMcpServerDiagnostic,
  listOpenAICompatibleMcpServerLabelsForSystemAccount,
  listRuntimeOpenAICompatibleMcpServers,
  replaceOpenAICompatibleMcpToolCache,
  type OpenAICompatibleMcpServerDiagnosticRecord,
  type OpenAICompatibleMcpServerRecord,
  type OpenAICompatibleMcpToolCacheRecord
} from '../../storage/openai-compatible-mcp-server.repository.js'
import { getRequestContext } from '../../shared/request-context.js'
import { GatewayLocalProtocolResponse, GatewayRequestValidationError } from '../gateway/request/validation-error.js'
import type {
  OpenAIToAnthropicMcpProxyExecutor,
  OpenAIToAnthropicMcpProxyInput,
  OpenAIToAnthropicMcpProxyPreparedServer,
  OpenAIToAnthropicMcpProxyToolCallResult,
  OpenAIToAnthropicMcpProxyToolDefinition
} from '../providers/drivers/_shared/openai-anthropic-bridge.js'

type JsonRecord = Record<string, unknown>

interface McpToolDefinition {
  name: string
  description?: string
  input_schema: JsonRecord
  annotations: unknown
}

interface McpPreparedContext {
  server: McpProxyServerRuntimeConfig
  authorization?: string
  sessionId?: string
  legacySse?: McpLegacySseSession
}

interface McpJsonRpcResult {
  result: unknown
  sessionId?: string
}

class RetryableMcpProxyError extends GatewayRequestValidationError {}

class McpProxyHttpError extends GatewayRequestValidationError {
  readonly httpStatus: number

  constructor(
    message: string,
    code: string,
    options: { statusCode?: number; type?: string },
    httpStatus: number
  ) {
    super(message, code, options)
    this.httpStatus = httpStatus
  }
}

interface McpLegacySseSession {
  endpointUrl: string
  reader: ReadableStreamDefaultReader<Uint8Array>
  decoder: TextDecoder
  abortController: AbortController
  buffer: string
  bytesRead: number
  closed: boolean
  sessionId?: string
}

export interface OpenAICompatibleMcpServerDiagnosticResult {
  diagnostic: OpenAICompatibleMcpServerDiagnosticRecord
  tools: OpenAICompatibleMcpToolCacheRecord[]
}

export function openAICompatibleMcpProxyExecutorForGatewayRequest(): OpenAIToAnthropicMcpProxyExecutor | undefined {
  const servers = runtimeMcpProxyServersForRequest()
  if (!servers.length) return undefined
  return {
    async prepare(input) {
      return prepareOpenAICompatibleMcpProxy(input, servers)
    },
    async callTool(input) {
      return callOpenAICompatibleMcpProxyTool(input.prepared, input.toolName, input.arguments, {
        signal: input.signal
      })
    },
    close(prepared) {
      closeMcpLegacySseSession(mcpPreparedContext(prepared).legacySse)
    },
    async run(input) {
      return runOpenAICompatibleMcpProxy(input, servers)
    }
  }
}

export async function diagnoseOpenAICompatibleMcpServer(input: {
  server: OpenAICompatibleMcpServerRecord
  authorization?: string
  signal?: AbortSignal
}): Promise<OpenAICompatibleMcpServerDiagnosticResult> {
  const runtimeServer = mcpServerRuntimeConfigFromRecord(input.server)
  const authorization = input.authorization && input.server.allowRequestAuthorization
    ? input.authorization
    : undefined
  const startedAtMs = Date.now()
  const startedAt = new Date(startedAtMs).toISOString()
  let initialized: { sessionId?: string; legacySse?: McpLegacySseSession } | undefined
  try {
    initialized = await initializeMcpServer(runtimeServer, authorization, input.signal)
    const listed = await requestMcpJsonRpc(runtimeServer, {
      method: 'tools/list',
      params: {},
      authorization,
      sessionId: initialized.sessionId,
      legacySse: initialized.legacySse,
      signal: input.signal
    })
    const tools = filteredMcpTools({}, runtimeServer, listed.result)
    const checkedAt = new Date().toISOString()
    const cachedTools = replaceOpenAICompatibleMcpToolCache({
      serverId: input.server.id,
      systemAccountId: input.server.systemAccountId,
      serverLabel: input.server.label,
      serverUrl: input.server.serverUrl,
      tools: tools.map((tool) => ({
        name: tool.name,
        description: tool.description,
        inputSchema: tool.input_schema,
        annotations: tool.annotations
      })),
      checkedAt
    })
    return {
      diagnostic: createMcpServerDiagnosticRecord(input.server, {
        startedAt,
        startedAtMs,
        status: 'succeeded',
        toolCount: cachedTools.length
      }),
      tools: cachedTools
    }
  } catch (error) {
    const diagnostic = createMcpServerDiagnosticRecord(input.server, {
      startedAt,
      startedAtMs,
      status: 'failed',
      error
    })
    return {
      diagnostic,
      tools: []
    }
  } finally {
    closeMcpLegacySseSession(initialized?.legacySse)
  }
}

function runtimeMcpProxyServersForRequest(): McpProxyServerRuntimeConfig[] {
  const systemAccountId = getRequestContext()?.systemAccountId
  const databaseServers = listRuntimeOpenAICompatibleMcpServers(systemAccountId)
  const servers = [...databaseServers]
  const labels = listOpenAICompatibleMcpServerLabelsForSystemAccount(systemAccountId)
  for (const server of runtimeConfig.mcpProxy.servers) {
    if (!server.enabled || labels.has(server.label)) continue
    servers.push(server)
    labels.add(server.label)
  }
  return servers
}

async function runOpenAICompatibleMcpProxy(
  input: OpenAIToAnthropicMcpProxyInput,
  servers: McpProxyServerRuntimeConfig[]
): Promise<GatewayLocalProtocolResponse> {
  const prepared = await prepareOpenAICompatibleMcpProxy(input, servers)
  const context = mcpPreparedContext(prepared)
  try {
    const payload = await responsesMcpProxyResponsePayload(input, prepared, context)
    return new GatewayLocalProtocolResponse({
      code: 'openai_anthropic_bridge_mcp_proxy_runtime',
      message: 'Responses MCP proxy runtime completed',
      body: input.stream ? responsesMcpProxySse(payload) : JSON.stringify(payload.response),
      contentType: input.stream
        ? 'text/event-stream; charset=utf-8'
        : 'application/json; charset=utf-8'
    })
  } finally {
    closeMcpLegacySseSession(context.legacySse)
  }
}

async function prepareOpenAICompatibleMcpProxy(
  input: Omit<OpenAIToAnthropicMcpProxyInput, 'model' | 'stream'>,
  servers: McpProxyServerRuntimeConfig[]
): Promise<OpenAIToAnthropicMcpProxyPreparedServer> {
  const server = resolveMcpProxyServer(input.tool, servers)
  const authorization = mcpProxyAuthorization(input.tool, server)
  let initialized: { sessionId?: string; legacySse?: McpLegacySseSession } | undefined
  try {
    initialized = await initializeMcpServer(server, authorization, input.signal)
    const listed = await requestMcpJsonRpc(server, {
      method: 'tools/list',
      params: {},
      authorization,
      sessionId: initialized.sessionId,
      legacySse: initialized.legacySse,
      signal: input.signal
    })
    const tools = filteredMcpTools(input.tool, server, listed.result)
    return {
      serverLabel: server.label,
      serverUrl: server.serverUrl,
      tools: tools.map(toBridgeMcpToolDefinition),
      context: {
        server,
        authorization,
        sessionId: initialized.sessionId,
        legacySse: initialized.legacySse
      } satisfies McpPreparedContext
    }
  } catch (error) {
    closeMcpLegacySseSession(initialized?.legacySse)
    throw error
  }
}

async function callOpenAICompatibleMcpProxyTool(
  prepared: OpenAIToAnthropicMcpProxyPreparedServer,
  toolName: string,
  toolArguments: JsonRecord,
  options: {
    signal?: AbortSignal
    approvalRequestId?: string
  } = {}
): Promise<OpenAIToAnthropicMcpProxyToolCallResult> {
  const context = mcpPreparedContext(prepared)
  const startedAtMs = Date.now()
  const startedAt = new Date(startedAtMs).toISOString()
  const argumentsDigest = mcpApprovalArgumentsDigest(prepared.serverLabel, prepared.serverUrl, toolName, toolArguments)
  const argumentsPreview = mcpApprovalArgumentsPreview(toolArguments)
  try {
    const callResult = await requestMcpJsonRpc(context.server, {
      method: 'tools/call',
      params: {
        name: toolName,
        arguments: toolArguments
      },
      authorization: context.authorization,
      sessionId: context.sessionId,
      legacySse: context.legacySse,
      signal: options.signal
    })
    const output = limitedMcpOutput(callResult.result, context.server)
    const executionRecord = recordMcpExecution({
      approvalRequestId: options.approvalRequestId,
      prepared,
      toolName,
      argumentsDigest,
      argumentsPreview,
      startedAt,
      startedAtMs,
      status: 'succeeded',
      output
    })
    return {
      outputText: output.text,
      truncated: output.truncated,
      metadata: output.truncated
        ? {
            execution_record_id: executionRecord.id,
            output_limit_bytes: mcpProxyMaxOutputBytes(context.server)
          }
        : {
            execution_record_id: executionRecord.id
          }
    }
  } catch (error) {
    recordMcpExecution({
      approvalRequestId: options.approvalRequestId,
      prepared,
      toolName,
      argumentsDigest,
      argumentsPreview,
      startedAt,
      startedAtMs,
      status: 'failed',
      error
    })
    throw error
  }
}

function mcpPreparedContext(prepared: OpenAIToAnthropicMcpProxyPreparedServer): McpPreparedContext {
  const context = prepared.context as McpPreparedContext | undefined
  if (!context?.server) {
    throw new GatewayRequestValidationError(
      'MCP proxy prepared context missing server',
      'openai_anthropic_bridge_mcp_proxy_invalid_context',
      { statusCode: 500, type: 'server_error' }
    )
  }
  return context
}

function toBridgeMcpToolDefinition(tool: McpToolDefinition): OpenAIToAnthropicMcpProxyToolDefinition {
  return {
    name: tool.name,
    description: tool.description,
    inputSchema: tool.input_schema,
    annotations: tool.annotations
  }
}

async function initializeMcpServer(
  server: McpProxyServerRuntimeConfig,
  authorization: string | undefined,
  signal: AbortSignal | undefined
): Promise<{ sessionId?: string; legacySse?: McpLegacySseSession }> {
  let legacySse: McpLegacySseSession | undefined
  const initializeInput = {
    method: 'initialize',
    params: {
      protocolVersion: '2025-06-18',
      capabilities: {},
      clientInfo: {
        name: 'juhe-ai',
        version: '0.1.0'
      }
    },
    authorization,
    signal
  }
  let initialized: McpJsonRpcResult
  try {
    initialized = await requestMcpJsonRpc(server, initializeInput)
  } catch (error) {
    if (!shouldFallbackToLegacySse(error)) throw error
    legacySse = await openMcpLegacySseSession(server, { authorization, signal })
    try {
      initialized = await requestMcpJsonRpc(server, {
        ...initializeInput,
        legacySse
      })
    } catch (legacyError) {
      closeMcpLegacySseSession(legacySse)
      throw legacyError
    }
  }
  if (initialized.sessionId || legacySse) {
    await notifyMcpJsonRpc(server, {
      method: 'notifications/initialized',
      params: {},
      authorization,
      sessionId: initialized.sessionId,
      legacySse,
      signal
    })
  }
  return { sessionId: initialized.sessionId, legacySse }
}

async function responsesMcpProxyResponsePayload(
  input: OpenAIToAnthropicMcpProxyInput,
  prepared: OpenAIToAnthropicMcpProxyPreparedServer,
  context: McpPreparedContext
): Promise<{ response: JsonRecord; items: JsonRecord[]; text: string }> {
  const suffix = `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  const responseId = `resp_mcp_proxy_${suffix}`
  const createdAt = Math.floor(Date.now() / 1000)
  const listToolsItem: JsonRecord = {
    id: `mcpl_${safeIdSegment(responseId)}`,
    type: 'mcp_list_tools',
    server_label: prepared.serverLabel,
    tools: prepared.tools.map((tool) => ({
      annotations: tool.annotations ?? null,
      description: tool.description ?? '',
      input_schema: tool.inputSchema,
      name: tool.name
    }))
  }
  const items: JsonRecord[] = [listToolsItem]
  let text = ''
  const selectedTool = prepared.tools[0]
  if (!selectedTool) {
    text = 'MCP proxy found no allowed tools.'
    items.push(responsesMcpProxyMessageItem(responseId, text))
  } else {
    const toolArguments = mcpProxyToolArguments(input.body, selectedTool)
    const requiresApproval = mcpRequiresApproval(input.tool, selectedTool.name, context.server)
    const approval = mcpApprovalResponseFromResponsesInput(input.body.input)
    if (requiresApproval && approval) {
      const approvalDecision = resolveMcpApprovalDecision({
        approval,
        prepared,
        toolName: selectedTool.name,
        toolArguments
      })
      if (!approvalDecision.approved) {
        text = 'MCP proxy tool call was not approved.'
        items.push(responsesMcpProxyMessageItem(responseId, text))
      } else {
        const consumed = consumeOpenAICompatibleMcpApprovalRequest(approvalDecision.approvalRequestId)
        if (consumed?.status !== 'consumed') {
          throw new GatewayRequestValidationError(
            'MCP approval_request_id 状态无法消费，已拒绝执行远程工具',
            'openai_anthropic_bridge_mcp_approval_not_pending',
            { statusCode: 400, type: 'invalid_request_error' }
          )
        }
        try {
          const callResult = await callOpenAICompatibleMcpProxyTool(prepared, selectedTool.name, toolArguments, {
            approvalRequestId: approvalDecision.approvalRequestId,
            signal: input.signal
          })
          items.push(responsesMcpProxyCallItem(
            responseId,
            prepared.serverLabel,
            selectedTool.name,
            toolArguments,
            callResult,
            approvalDecision.approvalRequestId
          ))
          text = 'MCP proxy completed remote tool call.'
        } catch (error) {
          items.push(responsesMcpProxyFailedCallItem(
            responseId,
            prepared.serverLabel,
            selectedTool.name,
            toolArguments,
            error,
            approvalDecision.approvalRequestId
          ))
          text = 'MCP proxy tool call failed.'
        }
        items.push(responsesMcpProxyMessageItem(responseId, text))
      }
    } else if (approval?.approved === false) {
      text = 'MCP proxy tool call was not approved.'
      items.push(responsesMcpProxyMessageItem(responseId, text))
    } else if (requiresApproval) {
      const approvalRequest = createOpenAICompatibleMcpApprovalRequest({
        scope: currentMcpApprovalScope(),
        serverLabel: prepared.serverLabel,
        serverUrl: prepared.serverUrl,
        toolName: selectedTool.name,
        argumentsDigest: mcpApprovalArgumentsDigest(prepared.serverLabel, prepared.serverUrl, selectedTool.name, toolArguments),
        argumentsPreview: mcpApprovalArgumentsPreview(toolArguments),
        traceId: getRequestContext()?.traceId,
        ttlSeconds: runtimeConfig.mcpProxy.approvalTtlSeconds
      })
      items.push(responsesMcpProxyApprovalRequestItem(prepared.serverLabel, selectedTool.name, toolArguments, approvalRequest.id))
    } else {
      try {
        const callResult = await callOpenAICompatibleMcpProxyTool(prepared, selectedTool.name, toolArguments, {
          signal: input.signal
        })
        items.push(responsesMcpProxyCallItem(
          responseId,
          prepared.serverLabel,
          selectedTool.name,
          toolArguments,
          callResult
        ))
        text = 'MCP proxy completed remote tool call.'
      } catch (error) {
        items.push(responsesMcpProxyFailedCallItem(
          responseId,
          prepared.serverLabel,
          selectedTool.name,
          toolArguments,
          error
        ))
        text = 'MCP proxy tool call failed.'
      }
      items.push(responsesMcpProxyMessageItem(responseId, text))
    }
  }

  return {
    response: {
      id: responseId,
      object: 'response',
      created_at: createdAt,
      status: 'completed',
      completed_at: createdAt,
      error: null,
      incomplete_details: null,
      instructions: null,
      max_output_tokens: null,
      model: input.model,
      output: items,
      output_text: text,
      parallel_tool_calls: false,
      previous_response_id: stringValue(input.body.previous_response_id) ?? null,
      reasoning: {
        effort: objectValue(input.body.reasoning)?.effort ?? null,
        summary: objectValue(input.body.reasoning)?.summary ?? null
      },
      store: false,
      temperature: null,
      text: { format: { type: 'text' } },
      tool_choice: 'auto',
      tools: [responsesMcpProxyToolSnapshot(input.tool, context.server)],
      top_p: null,
      truncation: 'disabled',
      usage: zeroResponsesUsage(),
      user: null,
      metadata: {
        gateway_runtime: 'local_runtime',
        gateway_tool: 'mcp'
      }
    },
    items,
    text
  }
}

function resolveMcpProxyServer(tool: JsonRecord, servers: McpProxyServerRuntimeConfig[]): McpProxyServerRuntimeConfig {
  const serverLabel = stringValue(tool.server_label)
  const serverUrl = stringValue(tool.server_url)
  const server = servers.find((item) => item.label === serverLabel && item.serverUrl === serverUrl)
  if (!server) {
    throw new GatewayRequestValidationError(
      `MCP server ${serverLabel ?? '<missing_label>'} 未命中本地 allowlist`,
      'openai_anthropic_bridge_mcp_server_not_allowed',
      { statusCode: 403, type: 'permission_error' }
    )
  }
  return server
}

function mcpProxyAuthorization(tool: JsonRecord, server: McpProxyServerRuntimeConfig): string | undefined {
  if (server.authorization) return server.authorization
  const requestAuthorization = stringValue(tool.authorization)
  if (requestAuthorization && server.allowRequestAuthorization) return requestAuthorization
  return undefined
}

function filteredMcpTools(
  tool: JsonRecord,
  server: McpProxyServerRuntimeConfig,
  listed: unknown
): McpToolDefinition[] {
  const serverAllowed = new Set(server.allowedTools)
  const requestAllowed = new Set(stringArrayValue(tool.allowed_tools))
  return normalizeMcpToolDefinitions(listed)
    .filter((definition) => !serverAllowed.size || serverAllowed.has(definition.name))
    .filter((definition) => !requestAllowed.size || requestAllowed.has(definition.name))
}

function normalizeMcpToolDefinitions(listed: unknown): McpToolDefinition[] {
  const result = objectValue(listed)
  const rawTools = Array.isArray(result?.tools) ? result.tools : []
  return rawTools
    .map(normalizeMcpToolDefinition)
    .filter((definition): definition is McpToolDefinition => Boolean(definition))
}

function normalizeMcpToolDefinition(value: unknown): McpToolDefinition | undefined {
  const tool = objectValue(value)
  const name = stringValue(tool?.name)
  if (!tool || !name) return undefined
  return {
    name,
    description: stringValue(tool.description),
    input_schema: objectValue(tool.input_schema) ?? objectValue(tool.inputSchema) ?? {
      type: 'object',
      properties: {}
    },
    annotations: tool.annotations ?? null
  }
}

function mcpProxyToolArguments(body: JsonRecord, tool: { inputSchema: JsonRecord }): JsonRecord {
  const schema = objectValue(tool.inputSchema)
  const properties = objectValue(schema?.properties)
  const required = stringArrayValue(schema?.required)
  const userText = responsesInputText(body.input) ?? ''
  const output: JsonRecord = {}
  for (const key of required) {
    const property = objectValue(properties?.[key])
    const type = stringValue(property?.type)
    if (type === 'string') {
      output[key] = userText || 'juhe-ai mcp proxy runtime'
    }
  }
  if (Object.keys(output).length > 0) return output
  if (properties?.query || properties?.input) {
    output[properties.query ? 'query' : 'input'] = userText || 'juhe-ai mcp proxy runtime'
  }
  return output
}

async function requestMcpJsonRpc(
  server: McpProxyServerRuntimeConfig,
  input: {
    method: string
    params?: JsonRecord
    authorization?: string
    sessionId?: string
    legacySse?: McpLegacySseSession
    signal?: AbortSignal
  }
): Promise<McpJsonRpcResult> {
  const id = `mcp_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  const parsed = await postMcpJsonRpc(server, {
    body: {
      jsonrpc: '2.0',
      id,
      method: input.method,
      params: input.params ?? {}
    },
    authorization: input.authorization,
    sessionId: input.sessionId,
    legacySse: input.legacySse,
    signal: input.signal
  })
  const record = objectValue(parsed.body)
  if (!record) {
    throw invalidMcpResponse(`MCP ${input.method} 响应不是 JSON-RPC 对象`)
  }
  const error = objectValue(record.error)
  if (error) {
    throw new GatewayRequestValidationError(
      stringValue(error.message) ?? `MCP ${input.method} 返回 JSON-RPC error`,
      stringValue(error.code) ?? 'openai_anthropic_bridge_mcp_proxy_jsonrpc_error',
      { statusCode: 502, type: 'upstream_error' }
    )
  }
  return {
    result: record.result,
    sessionId: parsed.sessionId ?? input.sessionId
  }
}

async function notifyMcpJsonRpc(
  server: McpProxyServerRuntimeConfig,
  input: {
    method: string
    params?: JsonRecord
    authorization?: string
    sessionId?: string
    legacySse?: McpLegacySseSession
    signal?: AbortSignal
  }
): Promise<void> {
  await postMcpJsonRpc(server, {
    body: {
      jsonrpc: '2.0',
      method: input.method,
      params: input.params ?? {}
    },
    authorization: input.authorization,
    sessionId: input.sessionId,
    legacySse: input.legacySse,
    signal: input.signal
  })
}

async function postMcpJsonRpc(
  server: McpProxyServerRuntimeConfig,
  input: {
    body: JsonRecord
    authorization?: string
    sessionId?: string
    legacySse?: McpLegacySseSession
    signal?: AbortSignal
  }
): Promise<{ body: unknown; sessionId?: string }> {
  if (input.legacySse) {
    return await postMcpLegacySseJsonRpc(server, input.legacySse, input)
  }
  const bodyText = JSON.stringify(input.body)
  const maxAttempts = Math.max(1, mcpProxyMaxRetries(server) + 1)
  let lastError: GatewayRequestValidationError | undefined
  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    try {
      return await postMcpJsonRpcAttempt(server, input, bodyText)
    } catch (error) {
      if (!(error instanceof GatewayRequestValidationError)) throw error
      lastError = error
      if (!(error instanceof RetryableMcpProxyError) || attempt >= maxAttempts || input.signal?.aborted) {
        throw error
      }
      await waitMcpProxyRetryDelay(server, input.signal)
    }
  }
  throw lastError ?? invalidMcpResponse('MCP JSON-RPC 请求未返回结果')
}

async function postMcpJsonRpcAttempt(
  server: McpProxyServerRuntimeConfig,
  input: {
    body: JsonRecord
    authorization?: string
    sessionId?: string
    signal?: AbortSignal
  },
  bodyText: string
): Promise<{ body: unknown; sessionId?: string }> {
  const headers = new Headers()
  headers.set('accept', 'application/json, text/event-stream')
  headers.set('content-type', 'application/json')
  headers.set('mcp-protocol-version', '2025-06-18')
  if (input.sessionId) headers.set('mcp-session-id', input.sessionId)
  if (input.authorization) headers.set('authorization', input.authorization)
  const timeoutController = new AbortController()
  const timeout = setTimeout(() => timeoutController.abort(), mcpProxyTimeoutMs(server))
  const requestSignal = input.signal
    ? AbortSignal.any([input.signal, timeoutController.signal])
    : timeoutController.signal
  try {
    const response = await fetch(server.serverUrl, {
      method: 'POST',
      headers,
      body: bodyText,
      signal: requestSignal,
      redirect: 'manual'
    })
    if (isMcpRedirectStatus(response.status)) {
      throw new GatewayRequestValidationError(
        `MCP server ${server.label} 返回 HTTP ${response.status} 重定向，已按 allowlist 策略拒绝：${mcpRedirectLocationSummary(response.headers.get('location'))}`,
        'openai_anthropic_bridge_mcp_proxy_redirect_blocked',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    if (!response.ok) {
      const text = await readResponseTextWithLimit(response, mcpProxyMaxBodyBytes(server))
      if (isRetryableMcpHttpStatus(response.status)) {
        throw new RetryableMcpProxyError(
          `MCP server ${server.label} 返回 HTTP ${response.status}${text ? `: ${truncateForError(text)}` : ''}`,
          'openai_anthropic_bridge_mcp_proxy_http_error',
          { statusCode: response.status >= 400 && response.status < 500 ? 400 : 502, type: 'upstream_error' }
        )
      }
      throw new McpProxyHttpError(
        `MCP server ${server.label} 返回 HTTP ${response.status}${text ? `: ${truncateForError(text)}` : ''}`,
        'openai_anthropic_bridge_mcp_proxy_http_error',
        { statusCode: response.status >= 400 && response.status < 500 ? 400 : 502, type: 'upstream_error' },
        response.status
      )
    }
    const text = await readResponseTextWithLimit(response, mcpProxyMaxBodyBytes(server))
    return {
      body: responseIsSse(response) ? parseMcpSseResponse(text) : safeParseJson(text),
      sessionId: response.headers.get('mcp-session-id') ?? undefined
    }
  } catch (error) {
    if (error instanceof GatewayRequestValidationError) throw error
    if (timeoutController.signal.aborted) {
      throw new RetryableMcpProxyError(
        `MCP server ${server.label} 请求超时`,
        'openai_anthropic_bridge_mcp_proxy_timeout',
        { statusCode: 504, type: 'upstream_error' }
      )
    }
    throw new RetryableMcpProxyError(
      error instanceof Error ? `MCP server ${server.label} 请求失败：${truncateForError(error.message)}` : `MCP server ${server.label} 请求失败`,
      'openai_anthropic_bridge_mcp_proxy_request_failed',
      { statusCode: 502, type: 'upstream_error' }
    )
  } finally {
    clearTimeout(timeout)
  }
}

function shouldFallbackToLegacySse(error: unknown): boolean {
  return error instanceof McpProxyHttpError && [404, 405, 410, 415].includes(error.httpStatus)
}

async function openMcpLegacySseSession(
  server: McpProxyServerRuntimeConfig,
  input: {
    authorization?: string
    signal?: AbortSignal
  }
): Promise<McpLegacySseSession> {
  const headers = new Headers()
  headers.set('accept', 'text/event-stream')
  headers.set('mcp-protocol-version', '2025-06-18')
  if (input.authorization) headers.set('authorization', input.authorization)
  const sessionAbortController = new AbortController()
  const timeoutController = new AbortController()
  const timeout = setTimeout(() => timeoutController.abort(), mcpProxyTimeoutMs(server))
  const requestSignal = input.signal
    ? AbortSignal.any([input.signal, sessionAbortController.signal, timeoutController.signal])
    : AbortSignal.any([sessionAbortController.signal, timeoutController.signal])
  let session: McpLegacySseSession | undefined
  try {
    const response = await fetch(server.serverUrl, {
      method: 'GET',
      headers,
      signal: requestSignal,
      redirect: 'manual'
    })
    if (isMcpRedirectStatus(response.status)) {
      throw new GatewayRequestValidationError(
        `MCP legacy SSE server ${server.label} 返回 HTTP ${response.status} 重定向，已按 allowlist 策略拒绝：${mcpRedirectLocationSummary(response.headers.get('location'))}`,
        'openai_anthropic_bridge_mcp_proxy_redirect_blocked',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    if (!response.ok) {
      const text = await readResponseTextWithLimit(response, mcpProxyMaxBodyBytes(server))
      throw new McpProxyHttpError(
        `MCP legacy SSE server ${server.label} 返回 HTTP ${response.status}${text ? `: ${truncateForError(text)}` : ''}`,
        'openai_anthropic_bridge_mcp_proxy_http_error',
        { statusCode: response.status >= 400 && response.status < 500 ? 400 : 502, type: 'upstream_error' },
        response.status
      )
    }
    if (!responseIsSse(response)) {
      throw new GatewayRequestValidationError(
        `MCP legacy SSE server ${server.label} 未返回 text/event-stream`,
        'openai_anthropic_bridge_mcp_proxy_sse_invalid_response',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    if (!response.body) {
      throw new GatewayRequestValidationError(
        `MCP legacy SSE server ${server.label} 缺少响应流`,
        'openai_anthropic_bridge_mcp_proxy_sse_missing_body',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    session = {
      endpointUrl: '',
      reader: response.body.getReader(),
      decoder: new TextDecoder(),
      abortController: sessionAbortController,
      buffer: '',
      bytesRead: 0,
      closed: false
    }
    while (true) {
      const frame = await readMcpLegacySseFrame(server, session)
      if (!frame) {
        throw new GatewayRequestValidationError(
          `MCP legacy SSE server ${server.label} 在 endpoint 事件前关闭连接`,
          'openai_anthropic_bridge_mcp_proxy_sse_endpoint_missing',
          { statusCode: 502, type: 'upstream_error' }
        )
      }
      if (frame.event !== 'endpoint') continue
      session.endpointUrl = resolveMcpLegacySseEndpointUrl(server, frame.data)
      return session
    }
  } catch (error) {
    closeMcpLegacySseSession(session)
    if (error instanceof GatewayRequestValidationError) throw error
    if (timeoutController.signal.aborted) {
      throw new RetryableMcpProxyError(
        `MCP legacy SSE server ${server.label} 等待 endpoint 超时`,
        'openai_anthropic_bridge_mcp_proxy_sse_timeout',
        { statusCode: 504, type: 'upstream_error' }
      )
    }
    throw new RetryableMcpProxyError(
      error instanceof Error ? `MCP legacy SSE server ${server.label} 请求失败：${truncateForError(error.message)}` : `MCP legacy SSE server ${server.label} 请求失败`,
      'openai_anthropic_bridge_mcp_proxy_request_failed',
      { statusCode: 502, type: 'upstream_error' }
    )
  } finally {
    clearTimeout(timeout)
  }
}

async function postMcpLegacySseJsonRpc(
  server: McpProxyServerRuntimeConfig,
  session: McpLegacySseSession,
  input: {
    body: JsonRecord
    authorization?: string
    sessionId?: string
    signal?: AbortSignal
  }
): Promise<{ body: unknown; sessionId?: string }> {
  if (session.closed || !session.endpointUrl) {
    throw new GatewayRequestValidationError(
      `MCP legacy SSE server ${server.label} 会话已关闭`,
      'openai_anthropic_bridge_mcp_proxy_sse_closed',
      { statusCode: 502, type: 'upstream_error' }
    )
  }
  const requestId = stringValue(input.body.id)
  const response = await postMcpLegacySseEndpoint(server, session, input)
  const responseSessionId = response.headers.get('mcp-session-id') ?? input.sessionId ?? session.sessionId
  if (responseSessionId) session.sessionId = responseSessionId
  await response.body?.cancel().catch(() => undefined)
  if (!requestId) {
    return { body: {}, sessionId: session.sessionId }
  }
  const body = await readMcpLegacySseJsonRpcResponse(server, session, requestId, input.signal)
  return { body, sessionId: session.sessionId }
}

async function postMcpLegacySseEndpoint(
  server: McpProxyServerRuntimeConfig,
  session: McpLegacySseSession,
  input: {
    body: JsonRecord
    authorization?: string
    sessionId?: string
    signal?: AbortSignal
  }
): Promise<Response> {
  const headers = new Headers()
  headers.set('accept', 'application/json, text/event-stream')
  headers.set('content-type', 'application/json')
  headers.set('mcp-protocol-version', '2025-06-18')
  const sessionId = input.sessionId ?? session.sessionId
  if (sessionId) headers.set('mcp-session-id', sessionId)
  if (input.authorization) headers.set('authorization', input.authorization)
  const timeoutController = new AbortController()
  const timeout = setTimeout(() => timeoutController.abort(), mcpProxyTimeoutMs(server))
  const requestSignal = input.signal
    ? AbortSignal.any([input.signal, session.abortController.signal, timeoutController.signal])
    : AbortSignal.any([session.abortController.signal, timeoutController.signal])
  try {
    const response = await fetch(session.endpointUrl, {
      method: 'POST',
      headers,
      body: JSON.stringify(input.body),
      signal: requestSignal,
      redirect: 'manual'
    })
    if (isMcpRedirectStatus(response.status)) {
      throw new GatewayRequestValidationError(
        `MCP legacy SSE endpoint ${server.label} 返回 HTTP ${response.status} 重定向，已按 allowlist 策略拒绝：${mcpRedirectLocationSummary(response.headers.get('location'))}`,
        'openai_anthropic_bridge_mcp_proxy_redirect_blocked',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    if (!response.ok) {
      const text = await readResponseTextWithLimit(response, mcpProxyMaxBodyBytes(server))
      throw new McpProxyHttpError(
        `MCP legacy SSE endpoint ${server.label} 返回 HTTP ${response.status}${text ? `: ${truncateForError(text)}` : ''}`,
        'openai_anthropic_bridge_mcp_proxy_http_error',
        { statusCode: response.status >= 400 && response.status < 500 ? 400 : 502, type: 'upstream_error' },
        response.status
      )
    }
    return response
  } catch (error) {
    if (error instanceof GatewayRequestValidationError) throw error
    if (timeoutController.signal.aborted) {
      closeMcpLegacySseSession(session)
      throw new RetryableMcpProxyError(
        `MCP legacy SSE endpoint ${server.label} 请求超时`,
        'openai_anthropic_bridge_mcp_proxy_timeout',
        { statusCode: 504, type: 'upstream_error' }
      )
    }
    closeMcpLegacySseSession(session)
    throw new RetryableMcpProxyError(
      error instanceof Error ? `MCP legacy SSE endpoint ${server.label} 请求失败：${truncateForError(error.message)}` : `MCP legacy SSE endpoint ${server.label} 请求失败`,
      'openai_anthropic_bridge_mcp_proxy_request_failed',
      { statusCode: 502, type: 'upstream_error' }
    )
  } finally {
    clearTimeout(timeout)
  }
}

async function readMcpLegacySseJsonRpcResponse(
  server: McpProxyServerRuntimeConfig,
  session: McpLegacySseSession,
  requestId: string,
  signal?: AbortSignal
): Promise<unknown> {
  const timeout = setTimeout(() => session.abortController.abort(), mcpProxyTimeoutMs(server))
  const abortSession = () => session.abortController.abort()
  signal?.addEventListener('abort', abortSession, { once: true })
  try {
    while (true) {
      const frame = await readMcpLegacySseFrame(server, session)
      if (!frame) {
        throw new GatewayRequestValidationError(
          `MCP legacy SSE server ${server.label} 在返回 JSON-RPC response 前关闭连接`,
          'openai_anthropic_bridge_mcp_proxy_sse_response_missing',
          { statusCode: 502, type: 'upstream_error' }
        )
      }
      if (frame.data === '[DONE]') continue
      if (frame.event && frame.event !== 'message') continue
      const parsed = safeParseJson(frame.data)
      const record = objectValue(parsed)
      if (!record || record.id !== requestId) continue
      return record
    }
  } catch (error) {
    if (error instanceof GatewayRequestValidationError) throw error
    if (session.abortController.signal.aborted) {
      throw new RetryableMcpProxyError(
        `MCP legacy SSE server ${server.label} 等待 JSON-RPC response 超时或被取消`,
        'openai_anthropic_bridge_mcp_proxy_sse_timeout',
        { statusCode: 504, type: 'upstream_error' }
      )
    }
    throw new RetryableMcpProxyError(
      error instanceof Error ? `MCP legacy SSE server ${server.label} 读取响应失败：${truncateForError(error.message)}` : `MCP legacy SSE server ${server.label} 读取响应失败`,
      'openai_anthropic_bridge_mcp_proxy_request_failed',
      { statusCode: 502, type: 'upstream_error' }
    )
  } finally {
    clearTimeout(timeout)
    signal?.removeEventListener('abort', abortSession)
  }
}

async function readMcpLegacySseFrame(
  server: McpProxyServerRuntimeConfig,
  session: McpLegacySseSession
): Promise<{ event?: string; data: string } | undefined> {
  while (true) {
    const boundary = findSseFrameBoundary(session.buffer)
    if (boundary) {
      const frameText = session.buffer.slice(0, boundary.index)
      session.buffer = session.buffer.slice(boundary.index + boundary.length)
      const frame = parseSseFrame(frameText)
      if (frame) return frame
      continue
    }
    const { done, value } = await session.reader.read()
    if (done) return undefined
    if (!value) continue
    session.bytesRead += value.byteLength
    if (session.bytesRead > mcpProxyMaxBodyBytes(server)) {
      closeMcpLegacySseSession(session)
      throw new GatewayRequestValidationError(
        'MCP legacy SSE 响应体超过读取上限',
        'openai_anthropic_bridge_mcp_proxy_response_too_large',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    session.buffer += session.decoder.decode(value, { stream: true })
  }
}

function findSseFrameBoundary(buffer: string): { index: number; length: number } | undefined {
  const lfIndex = buffer.indexOf('\n\n')
  const crlfIndex = buffer.indexOf('\r\n\r\n')
  if (lfIndex < 0 && crlfIndex < 0) return undefined
  if (lfIndex >= 0 && (crlfIndex < 0 || lfIndex < crlfIndex)) {
    return { index: lfIndex, length: 2 }
  }
  return { index: crlfIndex, length: 4 }
}

function resolveMcpLegacySseEndpointUrl(server: McpProxyServerRuntimeConfig, value: string): string {
  const rawEndpoint = value.trim()
  if (!rawEndpoint) {
    throw new GatewayRequestValidationError(
      `MCP legacy SSE server ${server.label} 返回空 endpoint`,
      'openai_anthropic_bridge_mcp_proxy_sse_endpoint_missing',
      { statusCode: 502, type: 'upstream_error' }
    )
  }
  const baseUrl = new URL(server.serverUrl)
  const endpointUrl = new URL(rawEndpoint, baseUrl)
  if (endpointUrl.origin !== baseUrl.origin) {
    throw new GatewayRequestValidationError(
      `MCP legacy SSE server ${server.label} 返回跨域 endpoint，已拒绝：${mcpLegacyEndpointSummary(endpointUrl)}`,
      'openai_anthropic_bridge_mcp_proxy_sse_endpoint_not_allowed',
      { statusCode: 502, type: 'upstream_error' }
    )
  }
  return endpointUrl.toString()
}

function mcpLegacyEndpointSummary(endpointUrl: URL): string {
  return truncateForError(`${endpointUrl.protocol}//${endpointUrl.host}${endpointUrl.pathname || '/'}`)
}

function closeMcpLegacySseSession(session?: McpLegacySseSession): void {
  if (!session || session.closed) return
  session.closed = true
  session.abortController.abort()
  void session.reader.cancel().catch(() => undefined)
}

function isRetryableMcpHttpStatus(status: number): boolean {
  return status === 408 || status === 425 || status === 429 || (status >= 500 && status <= 599)
}

function isMcpRedirectStatus(status: number): boolean {
  return status >= 300 && status <= 399
}

function mcpRedirectLocationSummary(location: string | null): string {
  if (!location) return '<missing>'
  try {
    const parsed = new URL(location, 'https://mcp.local')
    const isAbsolute = /^[A-Za-z][A-Za-z0-9+.-]*:/.test(location)
    const target = isAbsolute
      ? `${parsed.protocol}//${parsed.host}${parsed.pathname}`
      : parsed.pathname || '/'
    return truncateForError(target)
  } catch {
    return truncateForError(location.split(/[?#]/, 1)[0] ?? '<invalid>')
  }
}

async function waitMcpProxyRetryDelay(server: McpProxyServerRuntimeConfig, signal?: AbortSignal): Promise<void> {
  const delayMs = mcpProxyRetryDelayMs(server)
  if (delayMs <= 0 || signal?.aborted) return
  await new Promise<void>((resolve) => {
    let timeout: ReturnType<typeof setTimeout> | undefined
    const finish = () => {
      if (timeout) clearTimeout(timeout)
      signal?.removeEventListener('abort', finish)
      resolve()
    }
    timeout = setTimeout(finish, delayMs)
    signal?.addEventListener('abort', finish, { once: true })
  })
}

function mcpProxyTimeoutMs(server: McpProxyServerRuntimeConfig): number {
  return normalizedServerNumber(server.timeoutMs, runtimeConfig.mcpProxy.timeoutMs)
}

function mcpProxyMaxRetries(server: McpProxyServerRuntimeConfig): number {
  return normalizedServerNumber(server.maxRetries, runtimeConfig.mcpProxy.maxRetries)
}

function mcpProxyRetryDelayMs(server: McpProxyServerRuntimeConfig): number {
  return normalizedServerNumber(server.retryDelayMs, runtimeConfig.mcpProxy.retryDelayMs)
}

function mcpProxyMaxBodyBytes(server: McpProxyServerRuntimeConfig): number {
  return normalizedServerNumber(server.maxBodyBytes, runtimeConfig.mcpProxy.maxBodyBytes)
}

function mcpProxyMaxOutputBytes(server: McpProxyServerRuntimeConfig): number {
  return normalizedServerNumber(server.maxOutputBytes, runtimeConfig.mcpProxy.maxOutputBytes)
}

function normalizedServerNumber(value: number | undefined, fallback: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value ?? fallback)) : fallback
}

function parseMcpSseResponse(text: string): unknown {
  for (const frame of text.split(/\r?\n\r?\n/)) {
    const parsedFrame = parseSseFrame(frame)
    if (!parsedFrame || parsedFrame.data === '[DONE]') continue
    return safeParseJson(parsedFrame.data)
  }
  return {}
}

function parseSseFrame(frame: string): { event?: string; data: string } | undefined {
  const dataLines: string[] = []
  let event: string | undefined
  for (const rawLine of frame.split(/\r?\n/)) {
    if (!rawLine || rawLine.startsWith(':')) continue
    const colonIndex = rawLine.indexOf(':')
    const field = colonIndex >= 0 ? rawLine.slice(0, colonIndex) : rawLine
    const rawValue = colonIndex >= 0 ? rawLine.slice(colonIndex + 1) : ''
    const value = rawValue.startsWith(' ') ? rawValue.slice(1) : rawValue
    if (field === 'event') event = value
    if (field === 'data') dataLines.push(value)
  }
  if (!dataLines.length) return undefined
  return { event, data: dataLines.join('\n') }
}

function responseIsSse(response: Response): boolean {
  return (response.headers.get('content-type') ?? '').toLowerCase().includes('text/event-stream')
}

async function readResponseTextWithLimit(response: Response, maxBodyBytes: number): Promise<string> {
  if (!response.body) return await response.text()
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let total = 0
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    if (!value) continue
    total += value.byteLength
    if (total > maxBodyBytes) {
      await reader.cancel()
      throw new GatewayRequestValidationError(
        'MCP server 响应体超过读取上限',
        'openai_anthropic_bridge_mcp_proxy_response_too_large',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    chunks.push(value)
  }
  return Buffer.concat(chunks.map((chunk) => Buffer.from(chunk))).toString('utf8')
}

function responsesMcpProxyCallItem(
  responseId: string,
  serverLabel: string,
  toolName: string,
  toolArguments: JsonRecord,
  result: OpenAIToAnthropicMcpProxyToolCallResult,
  approvalRequestId?: string
): JsonRecord {
  const item: JsonRecord = {
    id: `mcp_${safeIdSegment(responseId)}`,
    type: 'mcp_call',
    approval_request_id: approvalRequestId ?? null,
    arguments: JSON.stringify(toolArguments),
    error: null,
    name: toolName,
    output: result.outputText,
    server_label: serverLabel
  }
  if (result.truncated || result.metadata) {
    item.metadata = {
      ...(result.metadata ?? {}),
      ...(result.truncated ? { output_truncated: true } : {})
    }
  }
  return item
}

function responsesMcpProxyFailedCallItem(
  responseId: string,
  serverLabel: string,
  toolName: string,
  toolArguments: JsonRecord,
  error: unknown,
  approvalRequestId?: string
): JsonRecord {
  return {
    id: `mcp_${safeIdSegment(responseId)}`,
    type: 'mcp_call',
    approval_request_id: approvalRequestId ?? null,
    arguments: JSON.stringify(toolArguments),
    error: mcpProxyToolCallErrorObject(error),
    name: toolName,
    output: null,
    server_label: serverLabel
  }
}

function mcpProxyToolCallErrorObject(error: unknown): JsonRecord {
  const code = error instanceof GatewayRequestValidationError
    ? error.code
    : 'openai_anthropic_bridge_mcp_proxy_tool_call_failed'
  const type = error instanceof GatewayRequestValidationError
    ? error.type
    : 'upstream_error'
  return {
    message: code,
    type,
    code
  }
}

function limitedMcpOutput(value: unknown, server: McpProxyServerRuntimeConfig): { text: string; truncated: boolean } {
  const raw = mcpOutputText(value)
  const buffer = Buffer.from(raw, 'utf8')
  const maxOutputBytes = mcpProxyMaxOutputBytes(server)
  if (buffer.byteLength <= maxOutputBytes) {
    return { text: raw, truncated: false }
  }
  const limited = buffer.subarray(0, maxOutputBytes).toString('utf8')
  return {
    text: `${limited}\n[truncated by juhe-ai MCP proxy output limit]`,
    truncated: true
  }
}

function recordMcpExecution(input: {
  approvalRequestId?: string
  prepared: OpenAIToAnthropicMcpProxyPreparedServer
  toolName: string
  argumentsDigest: string
  argumentsPreview: string
  startedAt: string
  startedAtMs: number
  status: 'succeeded' | 'failed'
  output?: { text: string; truncated: boolean }
  error?: unknown
}): { id: string } {
  const finishedAtMs = Date.now()
  const outputBytes = input.output ? Buffer.byteLength(input.output.text, 'utf8') : 0
  const error = input.error instanceof GatewayRequestValidationError ? input.error : undefined
  const errorCode = input.status === 'failed'
    ? error?.code ?? 'openai_anthropic_bridge_mcp_proxy_tool_call_failed'
    : undefined
  const record = createOpenAICompatibleMcpExecutionRecord({
    scope: currentMcpApprovalScope(),
    traceId: getRequestContext()?.traceId,
    approvalRequestId: input.approvalRequestId,
    serverLabel: input.prepared.serverLabel,
    serverUrl: input.prepared.serverUrl,
    toolName: input.toolName,
    argumentsDigest: input.argumentsDigest,
    argumentsPreview: input.argumentsPreview,
    status: input.status,
    outputDigest: input.output ? sha256Hex(input.output.text) : undefined,
    outputBytes,
    outputTruncated: input.output?.truncated === true,
    errorCode,
    errorMessage: errorCode,
    omissionMetadata: input.output?.truncated
      ? { output_truncated: true, output_limit_bytes: mcpProxyMaxOutputBytes(mcpPreparedContext(input.prepared).server) }
      : undefined,
    startedAt: input.startedAt,
    finishedAt: new Date(finishedAtMs).toISOString(),
    durationMs: finishedAtMs - input.startedAtMs
  })
  return { id: record.id }
}

function createMcpServerDiagnosticRecord(
  server: OpenAICompatibleMcpServerRecord,
  input: {
    startedAt: string
    startedAtMs: number
    status: 'succeeded' | 'failed'
    toolCount?: number
    error?: unknown
  }
): OpenAICompatibleMcpServerDiagnosticRecord {
  const finishedAtMs = Date.now()
  const error = input.error instanceof GatewayRequestValidationError ? input.error : undefined
  const errorCode = input.status === 'failed'
    ? error?.code ?? 'openai_anthropic_bridge_mcp_proxy_diagnostic_failed'
    : undefined
  return createOpenAICompatibleMcpServerDiagnostic({
    serverId: server.id,
    systemAccountId: server.systemAccountId,
    serverLabel: server.label,
    serverUrl: server.serverUrl,
    status: input.status,
    toolCount: input.toolCount ?? 0,
    errorCode,
    errorMessage: input.status === 'failed' ? errorCode : undefined,
    omissionMetadata: input.status === 'failed'
      ? { error_message_omitted: true }
      : undefined,
    startedAt: input.startedAt,
    finishedAt: new Date(finishedAtMs).toISOString(),
    durationMs: finishedAtMs - input.startedAtMs
  })
}

function mcpServerRuntimeConfigFromRecord(record: OpenAICompatibleMcpServerRecord): McpProxyServerRuntimeConfig {
  return {
    label: record.label,
    serverUrl: record.serverUrl,
    enabled: record.enabled,
    allowedTools: record.allowedTools,
    defaultApprovalPolicy: record.defaultApprovalPolicy,
    timeoutMs: record.timeoutMs,
    maxRetries: record.maxRetries,
    retryDelayMs: record.retryDelayMs,
    maxBodyBytes: record.maxBodyBytes,
    maxOutputBytes: record.maxOutputBytes,
    allowRequestAuthorization: record.allowRequestAuthorization
  }
}

function mcpOutputText(value: unknown): string {
  const record = objectValue(value)
  const content = Array.isArray(record?.content) ? record.content : []
  const texts = content.map((item) => {
    const part = objectValue(item)
    return part?.type === 'text' ? stringValue(part.text) : undefined
  }).filter((item): item is string => Boolean(item))
  if (texts.length) return texts.join('\n')
  return JSON.stringify(value ?? null)
}

function responsesMcpProxyApprovalRequestItem(
  serverLabel: string,
  toolName: string,
  toolArguments: JsonRecord,
  approvalRequestId: string
): JsonRecord {
  return {
    id: approvalRequestId,
    type: 'mcp_approval_request',
    arguments: JSON.stringify(toolArguments),
    name: toolName,
    server_label: serverLabel
  }
}

function responsesMcpProxyMessageItem(responseId: string, text: string): JsonRecord {
  return {
    id: `msg_${safeIdSegment(responseId)}`,
    type: 'message',
    status: 'completed',
    role: 'assistant',
    content: [{
      type: 'output_text',
      text,
      annotations: []
    }]
  }
}

function responsesMcpProxyToolSnapshot(tool: JsonRecord, server: McpProxyServerRuntimeConfig): JsonRecord {
  const snapshot: JsonRecord = {
    type: 'mcp',
    server_label: server.label,
    server_url: server.serverUrl
  }
  const serverDescription = stringValue(tool.server_description)
  if (serverDescription) snapshot.server_description = serverDescription
  const allowedTools = stringArrayValue(tool.allowed_tools)
  if (allowedTools.length) snapshot.allowed_tools = allowedTools
  if (tool.require_approval !== undefined) snapshot.require_approval = tool.require_approval
  if (tool.defer_loading === true) snapshot.defer_loading = true
  return snapshot
}

function responsesMcpProxySse(input: { response: JsonRecord; items: JsonRecord[]; text: string }): string {
  const response = input.response
  const inProgressResponse: JsonRecord = {
    ...response,
    status: 'in_progress',
    completed_at: null,
    output: [],
    output_text: '',
    usage: null
  }
  const output: string[] = [
    sse('response.created', {
      type: 'response.created',
      response: inProgressResponse
    }),
    sse('response.in_progress', {
      type: 'response.in_progress',
      response: inProgressResponse
    })
  ]

  for (const [index, item] of input.items.entries()) {
    output.push(sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: index,
      item: mcpProxyInProgressItem(item)
    }))
    if (item.type === 'message') {
      const itemId = stringValue(item.id) ?? ''
      const content = Array.isArray(item.content) ? item.content.filter(isPlainObject) : []
      const part = content[0] ?? { type: 'output_text', text: '', annotations: [] }
      const text = stringValue(part.text) ?? ''
      output.push(
        sse('response.content_part.added', {
          type: 'response.content_part.added',
          item_id: itemId,
          output_index: index,
          content_index: 0,
          part: { type: 'output_text', text: '', annotations: [] }
        }),
        sse('response.output_text.delta', {
          type: 'response.output_text.delta',
          item_id: itemId,
          output_index: index,
          content_index: 0,
          delta: text
        }),
        sse('response.output_text.done', {
          type: 'response.output_text.done',
          item_id: itemId,
          output_index: index,
          content_index: 0,
          text
        }),
        sse('response.content_part.done', {
          type: 'response.content_part.done',
          item_id: itemId,
          output_index: index,
          content_index: 0,
          part
        })
      )
    }
    if (item.type === 'mcp_call') {
      output.push(...responsesMcpCallLifecycleSse(response, item, index))
    }
    output.push(sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: index,
      item
    }))
  }

  output.push(sse('response.completed', {
    type: 'response.completed',
    response
  }))
  return output.join('')
}

function mcpProxyInProgressItem(item: JsonRecord): JsonRecord {
  if (item.type !== 'message') return item
  return {
    ...item,
    status: 'in_progress',
    content: []
  }
}

function responsesMcpCallLifecycleSse(response: JsonRecord, item: JsonRecord, outputIndex: number): string[] {
  const responseId = stringValue(response.id) ?? ''
  const itemId = stringValue(item.id) ?? ''
  const argumentsText = typeof item.arguments === 'string' ? item.arguments : ''
  const output: string[] = []
  if (argumentsText) {
    output.push(
      sse('response.mcp_call_arguments.delta', {
        type: 'response.mcp_call_arguments.delta',
        response_id: responseId,
        item_id: itemId,
        output_index: outputIndex,
        delta: argumentsText
      }),
      sse('response.mcp_call_arguments.done', {
        type: 'response.mcp_call_arguments.done',
        response_id: responseId,
        item_id: itemId,
        output_index: outputIndex,
        arguments: argumentsText
      })
    )
  }
  output.push(sse('response.mcp_call.in_progress', {
    type: 'response.mcp_call.in_progress',
    response_id: responseId,
    item_id: itemId,
    output_index: outputIndex
  }))
  const error = objectValue(item.error)
  if (error) {
    output.push(sse('response.mcp_call.failed', {
      type: 'response.mcp_call.failed',
      response_id: responseId,
      item_id: itemId,
      output_index: outputIndex,
      error
    }))
  }
  return output
}

function mcpRequiresApproval(tool: JsonRecord, toolName: string, server?: McpProxyServerRuntimeConfig): boolean {
  if (server?.defaultApprovalPolicy === 'always') return true
  const requireApproval = tool.require_approval
  if (requireApproval === 'never') return false
  if (requireApproval === 'always') return true
  const requireApprovalObject = objectValue(requireApproval)
  const never = objectValue(requireApprovalObject?.never)
  if (stringArrayValue(never?.tool_names).includes(toolName)) return false
  if (server?.defaultApprovalPolicy === 'never') return false
  return true
}

function resolveMcpApprovalDecision(input: {
  approval: { approved: boolean; approvalRequestId?: string; rejectReason?: string }
  prepared: OpenAIToAnthropicMcpProxyPreparedServer
  toolName: string
  toolArguments: JsonRecord
}): { approved: boolean; approvalRequestId: string } {
  const approvalRequestId = input.approval.approvalRequestId
  if (!approvalRequestId) {
    throw new GatewayRequestValidationError(
      'MCP approval_response 缺少 approval_request_id，已拒绝执行远程工具',
      'openai_anthropic_bridge_mcp_approval_missing_id',
      { statusCode: 400, type: 'invalid_request_error' }
    )
  }
  const decision = resolveOpenAICompatibleMcpApprovalResponse({
    approvalRequestId,
    scope: currentMcpApprovalScope(),
    serverLabel: input.prepared.serverLabel,
    serverUrl: input.prepared.serverUrl,
    toolName: input.toolName,
    argumentsDigest: mcpApprovalArgumentsDigest(input.prepared.serverLabel, input.prepared.serverUrl, input.toolName, input.toolArguments),
    approved: input.approval.approved,
    rejectReason: input.approval.rejectReason
  })
  if (decision.ok) {
    return { approved: decision.approved, approvalRequestId }
  }
  if (decision.reason === 'scope_mismatch') {
    throw new GatewayRequestValidationError(
      'MCP approval_request_id 不属于当前 API Key / 分组，已拒绝执行远程工具',
      'openai_anthropic_bridge_mcp_approval_scope_mismatch',
      { statusCode: 403, type: 'permission_error' }
    )
  }
  if (decision.reason === 'expired') {
    throw new GatewayRequestValidationError(
      'MCP approval_request_id 已过期，已拒绝执行远程工具',
      'openai_anthropic_bridge_mcp_approval_expired',
      { statusCode: 400, type: 'invalid_request_error' }
    )
  }
  if (decision.reason === 'not_pending') {
    throw new GatewayRequestValidationError(
      'MCP approval_request_id 已被处理，不能重复执行远程工具',
      'openai_anthropic_bridge_mcp_approval_not_pending',
      { statusCode: 400, type: 'invalid_request_error' }
    )
  }
  const mismatch = decision.reason === 'target_mismatch' || decision.reason === 'arguments_mismatch'
  throw new GatewayRequestValidationError(
    mismatch
      ? 'MCP approval_request_id 与当前 server/tool/arguments 不匹配，已拒绝执行远程工具'
      : 'MCP approval_request_id 不存在，已拒绝执行远程工具',
    mismatch
      ? 'openai_anthropic_bridge_mcp_approval_mismatch'
      : 'openai_anthropic_bridge_mcp_approval_not_found',
    { statusCode: 400, type: 'invalid_request_error' }
  )
}

function currentMcpApprovalScope(): OpenAICompatibleMcpApprovalScope {
  const context = getRequestContext()
  if (context?.systemAccountId && context.apiKeyId && context.groupId) {
    return {
      systemAccountId: context.systemAccountId,
      apiKeyId: context.apiKeyId,
      groupId: context.groupId
    }
  }
  throw new GatewayRequestValidationError(
    'MCP approval 需要当前 API Key / 分组请求上下文，已拒绝执行远程工具',
    'openai_anthropic_bridge_mcp_approval_scope_missing',
    { statusCode: 503, type: 'service_unavailable' }
  )
}

function mcpApprovalArgumentsDigest(
  serverLabel: string,
  serverUrl: string,
  toolName: string,
  toolArguments: JsonRecord
): string {
  return sha256Hex(stableJsonStringify({
      arguments: toolArguments,
      server_label: serverLabel,
      server_url: serverUrl,
      tool_name: toolName
    }))
}

function mcpApprovalArgumentsPreview(toolArguments: JsonRecord): string {
  const text = stableJsonStringify(toolArguments)
  return text.length > 1000 ? `${text.slice(0, 1000)}...` : text
}

function sha256Hex(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function mcpApprovalResponseFromResponsesInput(value: unknown): { approved: boolean; approvalRequestId?: string; rejectReason?: string } | undefined {
  const items = Array.isArray(value)
    ? value
    : isPlainObject(value) ? [value] : []
  for (let index = items.length - 1; index >= 0; index -= 1) {
    const item = items[index]
    if (!isPlainObject(item)) continue
    if (stringValue(item.type) !== 'mcp_approval_response') continue
    return {
      approved: item.approve === true,
      approvalRequestId: stringValue(item.approval_request_id),
      rejectReason: stringValue(item.reason)
    }
  }
  return undefined
}

function responsesInputText(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  const items = Array.isArray(value)
    ? value
    : isPlainObject(value) ? [value] : []
  const texts: string[] = []
  for (const item of items) {
    if (!isPlainObject(item)) continue
    const content = Array.isArray(item.content) ? item.content : []
    for (const part of content) {
      const record = objectValue(part)
      if (!record) continue
      if (record.type === 'input_text' || record.type === 'output_text') {
        const text = stringValue(record.text)
        if (text) texts.push(text)
      }
    }
  }
  return texts.join('\n') || undefined
}

function zeroResponsesUsage(): JsonRecord {
  return {
    input_tokens: 0,
    input_tokens_details: { cached_tokens: 0 },
    output_tokens: 0,
    output_tokens_details: { reasoning_tokens: 0 },
    total_tokens: 0
  }
}

function invalidMcpResponse(message: string): GatewayRequestValidationError {
  return new GatewayRequestValidationError(
    message,
    'openai_anthropic_bridge_mcp_proxy_invalid_response',
    { statusCode: 502, type: 'upstream_error' }
  )
}

function sse(event: string, data: JsonRecord): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
}

function safeParseJson(text: string): unknown {
  if (!text) return {}
  try {
    return JSON.parse(text) as unknown
  } catch {
    return {}
  }
}

function truncateForError(text: string): string {
  const normalized = text.replace(/\s+/g, ' ').trim()
  return normalized.length > 200 ? `${normalized.slice(0, 200)}...` : normalized
}

function safeIdSegment(value: string): string {
  const normalized = value.replace(/[^A-Za-z0-9_-]/g, '_')
  return normalized.slice(0, 96) || 'mcp'
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function stringArrayValue(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string' && item.trim().length > 0).map((item) => item.trim())
}

import { Router, type NextFunction, type Response as ExpressResponse } from 'express'
import { z } from 'zod'

import { runtimeConfig } from '../../config/runtime.js'
import { ok } from '../../shared/http.js'
import { getTraceId } from '../../shared/request-context.js'
import {
  acceptChatTurn,
  cancelChatTurn,
  ChatConflictError,
  completeChatTurn,
  createChatConversation,
  deleteChatConversation,
  failChatTurn,
  findChatTurnByClientMessageId,
  getChatConversation,
  listChatContextMessages,
  listChatConversations,
  listChatMessages,
  updateChatConversation,
  type ChatMessageContentBlock
} from '../../storage/chat.repository.js'
import { getChatDatabaseClient } from '../../storage/chat-client.js'
import { findApiKeySecretAsync, listApiKeysAsync } from '../../storage/repositories.js'
import { validateGatewayApiKeyAsync } from '../../storage/gateway-api-key.repository.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { listCachedOpenAIAccountsForGroupAsync } from '../gateway/runtime/runtime-cache.service.js'
import { collectOpenAIChatSse } from './chat-gateway-sse.js'
import { ChatContextBudgetError, resolveEffectiveChatContextWindowTokens, trimChatContextToBudget, validateFixedChatInputBudget } from './chat-context-budget.js'
import { collectChatResponsesSse } from './chat-responses-sse.js'
import { buildChatTransportRequest, resolveChatBudgetContent, resolveChatSupportedProtocols, selectChatTransport } from './chat-transport.js'
import { buildChatModelOptions, chatReasoningEfforts, chatServiceTiers } from './chat-model-options.js'
import { buildChatSystemInstructions } from './chat-system-instructions.js'
import { initializeAcceptedChatTurn } from './chat-turn-initialization.js'
import { listProviderModelCatalogAsync } from '../model-pricing/model-catalog.service.js'
import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'

export const chatRouter = Router()

const messageBodySchema = z.object({
  clientMessageId: z.string().trim().min(1).max(100),
  replaceTurnId: z.string().trim().min(1).max(100).optional(),
  content: z.string().trim().min(1, '请输入消息').max(196_608, '消息内容过长'),
  contentBlocks: z.array(z.object({ type: z.enum(['input_text', 'input_image']), text: z.string().max(196_608, '文本块内容过长').optional(), dataUrl: z.string().max(14 * 1024 * 1024).optional() })).max(8).optional(),
  model: z.string().trim().min(1, '请选择模型').max(200),
  reasoningEffort: z.enum(chatReasoningEfforts).optional(),
  serviceTier: z.enum(chatServiceTiers).optional(),
  contextWindowTokens: z.number().int().min(16_000).max(2_000_000).optional()
}).strict()
const messagesQuerySchema = z.object({
  beforeSequenceNo: z.preprocess(queryScalar, z.coerce.number().int().min(1).max(2_147_483_647).optional()),
  limit: z.preprocess(queryScalar, z.coerce.number().int().min(1).max(100).default(50))
}).strict()
const createConversationSchema = z.object({ apiKeyId: z.string().trim().min(1, '请选择 API Key') }).strict()
const updateConversationSchema = z.object({
  title: z.string().trim().min(1, '请输入会话标题').max(60, '会话标题最多 60 个字符').optional(),
  isPinned: z.boolean().optional()
}).strict().refine((value) => value.title !== undefined || value.isPinned !== undefined, '没有可更新的会话字段')
const activeStreams = new Map<string, { ownerId: string; turnId: string; controller: AbortController }>()
const maxMessageBytes = 192 * 1024
const storageQuotaBytes = 2 * 1024 * 1024 * 1024

class ChatRequestError extends Error {
  constructor(public readonly code: 'chat_image_not_supported', message: string) {
    super(message)
    this.name = 'ChatRequestError'
  }
}

chatRouter.get('/api-keys', async (_req, res, next) => {
  try {
    const auth = requireChatAuth()
    const keys = await listApiKeysAsync({ systemAccountId: auth.systemAccountId, role: 'user' })
    res.json(ok(keys.filter((key) => key.status === 'active').map((key) => ({ id: key.id, name: key.name, status: key.status }))))
  } catch (error) { next(error) }
})

chatRouter.get('/conversations', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const client = await getChatDatabaseClient()
    res.json(ok(await listChatConversations(client, {
      systemAccountId: auth.systemAccountId,
      beforeLastMessageAt: textQuery(req.query.beforeLastMessageAt),
      beforeId: textQuery(req.query.beforeId),
      limit: integerQuery(req.query.limit, 30, 1, 50)
    })))
  } catch (error) { next(error) }
})

chatRouter.post('/conversations', async (req, res, next) => {
  try {
    const body = createConversationSchema.parse(req.body)
    const auth = requireChatAuth()
    const apiKey = await requireOwnedApiKey(body.apiKeyId, auth.systemAccountId)
    const client = await getChatDatabaseClient()
    res.status(201).json(ok(await createChatConversation(client, {
      systemAccountId: auth.systemAccountId,
      apiKeyId: apiKey.id,
      apiKeyNameSnapshot: apiKey.name,
      now: new Date().toISOString()
    })))
  } catch (error) { handleChatRouteError(error, res, next) }
})

chatRouter.get('/conversations/:conversationId/messages', async (req, res, next) => {
  try {
    const query = messagesQuerySchema.parse(req.query)
    const auth = requireChatAuth()
    const client = await getChatDatabaseClient()
    res.json(ok(await listChatMessages(client, {
      conversationId: req.params.conversationId,
      systemAccountId: auth.systemAccountId,
      beforeSequenceNo: query.beforeSequenceNo,
      limit: query.limit,
      now: new Date().toISOString()
    })))
  } catch (error) { handleChatRouteError(error, res, next) }
})

chatRouter.get('/conversations/:conversationId', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const conversation = await getChatConversation(await getChatDatabaseClient(), req.params.conversationId, auth.systemAccountId)
    if (!conversation) { res.status(404).json({ message: '会话不存在' }); return }
    res.json(ok(conversation))
  } catch (error) { next(error) }
})

chatRouter.patch('/conversations/:conversationId', async (req, res, next) => {
  try {
    const body = updateConversationSchema.parse(req.body)
    const auth = requireChatAuth()
    const conversation = await updateChatConversation(await getChatDatabaseClient(), {
      conversationId: req.params.conversationId,
      systemAccountId: auth.systemAccountId,
      title: body.title,
      isPinned: body.isPinned,
      now: new Date().toISOString()
    })
    if (!conversation) { res.status(404).json({ message: '会话不存在' }); return }
    res.json(ok(conversation))
  } catch (error) { handleChatRouteError(error, res, next) }
})

chatRouter.get('/conversations/:conversationId/models', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const conversation = await requireOwnedConversation(req.params.conversationId, auth.systemAccountId)
    const apiKey = await requireOwnedApiKey(conversation.apiKeyId, auth.systemAccountId)
    const response = await fetch(gatewayUrl('/v1/models'), { headers: { authorization: `Bearer ${apiKey.key}` } })
    const payload = await boundedJson(response)
    if (!response.ok) throw new Error(upstreamMessage(payload, `模型列表请求失败（HTTP ${response.status}）`))
    const data = Array.isArray((payload as { data?: unknown }).data) ? (payload as { data: Array<{ id?: unknown }> }).data : []
    const modelIds = data.map((item) => String(item.id ?? '')).filter(Boolean)
    const catalog = await listProviderModelCatalogAsync({
      providerCode: GPT_VENDOR_CODE,
      systemAccountId: auth.systemAccountId,
      includeUnpriced: true
    })
    res.json(ok(buildChatModelOptions(modelIds, catalog)))
  } catch (error) { next(error) }
})

chatRouter.post('/conversations/:conversationId/stream', async (req, res, next) => {
  let accepted: Awaited<ReturnType<typeof acceptChatTurn>> | undefined
  let ownerId = ''
  let controller: AbortController | undefined
  let responseClosed = false
  let partialContent = ''
  const contentBlocks: ChatMessageContentBlock[] = []
  try {
    const body = messageBodySchema.parse(req.body)
    if (Buffer.byteLength(body.content, 'utf8') > maxMessageBytes) {
      res.status(413).json({ message: '消息内容超过 192 KiB 上限' })
      return
    }
    const auth = requireChatAuth()
    ownerId = auth.systemAccountId
    const conversation = await requireOwnedConversation(req.params.conversationId, ownerId)
    const client = await getChatDatabaseClient()
    const existingTurn = await findChatTurnByClientMessageId(client, {
      conversationId: conversation.id,
      systemAccountId: ownerId,
      clientMessageId: body.clientMessageId
    })
    if (existingTurn) {
      res.status(409).json({ message: '该消息已提交，请刷新会话', code: 'chat_message_already_exists' })
      return
    }
    const apiKey = await requireOwnedApiKey(conversation.apiKeyId, ownerId)
    const apiKeySecret = String(apiKey.key)
    const gatewayKey = await validateGatewayApiKeyAsync(apiKeySecret)
    const supportedProtocols = await resolveChatSupportedProtocols({
      groupIds: gatewayKey?.group_bindings?.map((binding) => binding.group_id) ?? [],
      model: body.model,
      loadAccounts: (groupId, model, endpointFamily) => listCachedOpenAIAccountsForGroupAsync(groupId, ownerId, {
        requestedModel: model,
        requestedEndpointFamily: endpointFamily
      })
    })
    const protocol = selectChatTransport({ supportedProtocols, toolsEnabled: true })
    const toolsEnabled = protocol === 'responses'
    const imageCount = body.contentBlocks?.filter((block) => block.type === 'input_image').length ?? 0
    if (protocol === 'chat_completions' && imageCount > 0) {
      throw new ChatRequestError('chat_image_not_supported', '当前路由不支持图片输入，请切换到支持 Responses 的账户或移除图片')
    }
    const catalog = await listProviderModelCatalogAsync({
      providerCode: GPT_VENDOR_CODE,
      systemAccountId: ownerId,
      includeUnpriced: true
    })
    const catalogItem = catalog.find((item) => item.model === body.model)
    const contextWindowTokens = resolveEffectiveChatContextWindowTokens({
      clientContextWindowTokens: body.contextWindowTokens,
      serverContextWindowTokens: catalogItem?.contextWindowTokens ?? catalogItem?.maxInputTokens
    })
    const systemInstructions = buildChatSystemInstructions({ toolsEnabled })
    const fixedBudgetInput = {
      currentUserContent: resolveChatBudgetContent({ protocol, currentContent: body.content, currentBlocks: body.contentBlocks }),
      instructions: systemInstructions.text,
      toolsEnabled,
      imageCount,
      contextWindowTokens
    }
    validateFixedChatInputBudget(fixedBudgetInput)
    accepted = await acceptChatTurn(client, {
      conversationId: conversation.id,
      systemAccountId: ownerId,
      clientMessageId: body.clientMessageId,
      userContent: body.content,
      contentBlocks: body.contentBlocks,
      model: body.model,
      now: new Date().toISOString(),
      storageQuotaBytes,
      replaceTurnId: body.replaceTurnId
    })
    if (accepted.duplicate) {
      res.status(409).json({ message: '该消息已提交，请刷新会话', code: 'chat_message_already_exists' })
      return
    }
    const acceptedTurn = accepted
    const transport = await initializeAcceptedChatTurn({
      initialize: async () => {
        const storedContext = await listChatContextMessages(client, {
          conversationId: conversation.id,
          systemAccountId: ownerId,
          limitTurns: 64,
          now: new Date().toISOString()
        })
        const context = trimChatContextToBudget({ history: storedContext, ...fixedBudgetInput })
        return buildChatTransportRequest({
          protocol,
          instructions: systemInstructions.text,
          model: body.model,
          history: context,
          currentContent: body.content,
          currentBlocks: body.contentBlocks,
          toolsEnabled,
          reasoningEffort: body.reasoningEffort,
          serviceTier: body.serviceTier
        })
      },
      failAcceptedTurn: async () => {
        await failChatTurn(client, {
          conversationId: conversation.id,
          systemAccountId: ownerId,
          turnId: acceptedTurn.turnId,
          assistantContent: '',
          contentBlocks: [],
          errorCode: 'chat_initialization_failed',
          traceId: getTraceId(),
          now: new Date().toISOString()
        })
      }
    })
    controller = new AbortController()
    activeStreams.set(conversation.id, { ownerId, turnId: accepted.turnId, controller })
    res.status(200)
    res.setHeader('Content-Type', 'text/event-stream; charset=utf-8')
    res.setHeader('Cache-Control', 'no-cache, no-transform')
    res.setHeader('X-Accel-Buffering', 'no')
    res.flushHeaders()
    writeSse(res, 'message.started', { turnId: accepted.turnId, userMessage: accepted.userMessage, assistantMessage: accepted.assistantMessage })
    res.on('close', () => { responseClosed = true; controller?.abort() })
    const heartbeat = setInterval(() => { if (!res.writableEnded) res.write(': heartbeat\n\n') }, 15_000)
    heartbeat.unref()
    try {
      const upstream = await fetch(gatewayUrl(transport.path), {
        method: 'POST',
        headers: { authorization: `Bearer ${apiKeySecret}`, 'content-type': 'application/json', accept: 'text/event-stream' },
        body: JSON.stringify(transport.body),
        signal: controller.signal
      })
      if (!upstream.ok || !upstream.body) {
        const payload = await boundedJson(upstream)
        throw new Error(upstreamMessage(payload, `模型请求失败（HTTP ${upstream.status}）`))
      }
      const result = protocol === 'responses'
        ? await collectChatResponsesSse(readableStreamChunks(upstream.body), (event) => {
            if (event.type === 'text_delta') {
              partialContent += event.delta
              if (!res.writableEnded) writeSse(res, 'message.delta', { messageId: accepted?.assistantMessage.id, delta: event.delta })
            } else if (event.type === 'reasoning_delta') {
              const existing = contentBlocks.find((block): block is Extract<ChatMessageContentBlock, { type: 'reasoning' }> => block.type === 'reasoning')
              if (existing) existing.text += event.delta
              else contentBlocks.push({ type: 'reasoning', text: event.delta })
              if (!res.writableEnded) writeSse(res, 'reasoning.delta', { messageId: accepted?.assistantMessage.id, delta: event.delta })
            } else if (event.type === 'tool_started' || event.type === 'tool_updated' || event.type === 'tool_completed') {
              projectToolEvent(contentBlocks, event.type, event.item)
              if (!res.writableEnded) writeSse(res, event.type.replace('_', '.'), { messageId: accepted?.assistantMessage.id, item: event.item })
            } else if (event.type === 'failed') {
              throw new Error(upstreamMessage(event.error, '模型工具调用失败'))
            }
          }, maxMessageBytes)
        : await collectOpenAIChatSse(readableStreamChunks(upstream.body), maxMessageBytes, (delta) => {
            partialContent += delta
            if (!res.writableEnded) writeSse(res, 'message.delta', { messageId: accepted?.assistantMessage.id, delta })
          })
      await completeChatTurn(client, {
        conversationId: conversation.id, systemAccountId: ownerId, turnId: accepted.turnId,
        assistantContent: result.content, contentBlocks, finishReason: 'finishReason' in result ? result.finishReason ?? 'stop' : 'stop', traceId: getTraceId() ?? '', now: new Date().toISOString()
      })
      if (!res.writableEnded) writeSse(res, 'message.completed', { messageId: accepted.assistantMessage.id, finishReason: 'finishReason' in result ? result.finishReason ?? 'stop' : 'stop', traceId: getTraceId() })
    } catch (error) {
      const canceled = controller.signal.aborted
      if (canceled) {
        await cancelChatTurn(client, { conversationId: conversation.id, systemAccountId: ownerId, turnId: accepted.turnId, assistantContent: partialContent, contentBlocks, traceId: getTraceId(), now: new Date().toISOString() })
      } else {
        await failChatTurn(client, { conversationId: conversation.id, systemAccountId: ownerId, turnId: accepted.turnId, assistantContent: partialContent, contentBlocks, errorCode: 'gateway_stream_failed', traceId: getTraceId(), now: new Date().toISOString() })
        if (!res.writableEnded) writeSse(res, 'message.failed', { messageId: accepted.assistantMessage.id, code: 'gateway_stream_failed', message: error instanceof Error ? error.message : '模型请求失败' })
      }
    } finally {
      clearInterval(heartbeat)
      activeStreams.delete(conversation.id)
      if (!res.writableEnded && !responseClosed) res.end()
    }
  } catch (error) {
    if (res.headersSent) {
      if (!res.writableEnded) res.end()
      return
    }
    if (error instanceof ChatConflictError) {
      res.status(409).json({ message: error.message, code: error.code })
      return
    }
    if (error instanceof ChatContextBudgetError) {
      res.status(422).json({ message: error.message, code: error.code })
      return
    }
    if (error instanceof ChatRequestError) {
      res.status(422).json({ message: error.message, code: error.code })
      return
    }
    handleChatRouteError(error, res, next)
  }
})

chatRouter.post('/conversations/:conversationId/stop', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const active = activeStreams.get(req.params.conversationId)
    if (!active || active.ownerId !== auth.systemAccountId) {
      res.status(404).json({ message: '当前没有正在生成的回答' })
      return
    }
    active.controller.abort()
    res.status(202).json(ok({ stopped: true }))
  } catch (error) { next(error) }
})

chatRouter.delete('/conversations/:conversationId', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const deleted = await deleteChatConversation(await getChatDatabaseClient(), req.params.conversationId, auth.systemAccountId)
    if (!deleted) { res.status(404).json({ message: '会话不存在' }); return }
    res.status(204).end()
  } catch (error) { next(error) }
})

async function requireOwnedConversation(conversationId: string, ownerId: string) {
  const conversation = await getChatConversation(await getChatDatabaseClient(), conversationId, ownerId)
  if (!conversation) throw new Error('会话不存在')
  return conversation
}

async function requireOwnedApiKey(apiKeyId: string | undefined, ownerId: string) {
  if (!apiKeyId) throw new Error('会话绑定的 API Key 已删除')
  const key = await findApiKeySecretAsync(apiKeyId, { systemAccountId: ownerId, role: 'user' })
  if (!key?.key || key.status !== 'active') throw new Error('API Key 不存在或不可用')
  return key
}

function requireChatAuth() {
  const auth = getRequestAuthContext()
  if (!auth) throw new Error('请先登录')
  return auth
}

function handleChatRouteError(error: unknown, res: ExpressResponse, next: NextFunction): void {
  if (error instanceof z.ZodError) {
    res.status(400).json({ message: error.issues[0]?.message || '请求参数无效', code: 'chat_invalid_request' })
    return
  }
  next(error)
}

function gatewayUrl(path: string): string { return `http://127.0.0.1:${runtimeConfig.port}${path}` }
function queryScalar(value: unknown): unknown { return Array.isArray(value) ? value[0] : value === '' ? undefined : value }
function textQuery(value: unknown): string | undefined { const raw = Array.isArray(value) ? value[0] : value; return typeof raw === 'string' && raw.trim() ? raw.trim() : undefined }
function optionalIntegerQuery(value: unknown): number | undefined { const text = textQuery(value); if (!text) return undefined; const result = Number(text); return Number.isInteger(result) && result > 0 ? result : undefined }
function integerQuery(value: unknown, fallback: number, min: number, max: number): number { const result = optionalIntegerQuery(value); return result === undefined ? fallback : Math.max(min, Math.min(max, result)) }
function writeSse(res: import('express').Response, event: string, data: unknown): void { res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`) }
async function* readableStreamChunks(stream: ReadableStream<Uint8Array>): AsyncGenerator<Uint8Array> { const reader = stream.getReader(); try { while (true) { const next = await reader.read(); if (next.done) return; yield next.value } } finally { reader.releaseLock() } }
async function boundedJson(response: Response): Promise<unknown> { const text = (await response.text()).slice(0, 64 * 1024); try { return JSON.parse(text) } catch { return { message: text } } }
function upstreamMessage(payload: unknown, fallback: string): string { if (payload && typeof payload === 'object') { const item = payload as { message?: unknown; error?: { message?: unknown } }; if (typeof item.error?.message === 'string') return item.error.message; if (typeof item.message === 'string') return item.message } return fallback }

function projectToolEvent(blocks: ChatMessageContentBlock[], eventType: 'tool_started' | 'tool_updated' | 'tool_completed', item: Record<string, unknown>): void {
  const id = String(item.id ?? item.call_id ?? `tool_${blocks.filter((block) => block.type === 'tool_call').length + 1}`)
  const status = eventType === 'tool_started' ? 'started' : eventType === 'tool_updated' ? 'updated' : 'completed'
  const existing = blocks.find((block): block is Extract<ChatMessageContentBlock, { type: 'tool_call' }> => block.type === 'tool_call' && block.id === id)
  if (existing) { existing.status = status; existing.toolType = String(item.type ?? existing.toolType); existing.item = item; return }
  blocks.push({ type: 'tool_call', id, toolType: String(item.type ?? 'tool'), status, item })
}

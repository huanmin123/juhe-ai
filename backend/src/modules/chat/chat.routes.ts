import { Router, type NextFunction, type Response as ExpressResponse } from 'express'
import { z } from 'zod'

import { runtimeConfig } from '../../config/runtime.js'
import { ok } from '../../shared/http.js'
import { getTraceId } from '../../shared/request-context.js'
import {
  acceptChatTurn,
  assertChatTurnReplaceable,
  cancelActiveChatTurnIfMatches,
  cancelChatTurn,
  ChatConflictError,
  completeChatTurn,
  createChatConversation,
  deleteChatConversation,
  failChatTurn,
  findChatTurnByClientMessageId,
  getChatConversation,
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
import { ChatContextBudgetError, estimateChatInputTokens, validateFixedChatInputBudget } from './chat-context-budget.js'
import { collectChatResponsesSse } from './chat-responses-sse.js'
import { buildChatTransportRequest, resolveChatBudgetContent, resolveChatSupportedProtocols, selectChatTransport } from './chat-transport.js'
import { buildChatModelOptions, ChatModelCapabilityError, chatReasoningEfforts, chatServiceTiers, resolveChatModelRequestOptions, type ChatModelOption } from './chat-model-options.js'
import { buildChatSystemInstructions } from './chat-system-instructions.js'
import {
  beginActiveChatAcceptance,
  cancelActiveChatPreparation,
  claimActiveChatPreparation,
  deleteActiveChatPreparationIfMatches,
  deleteActiveChatStreamIfMatches,
  hasActiveChatPreparation,
  type ActiveChatPreparation
} from './chat-active-streams.js'
import { sanitizeChatContentBlocksForPersistence } from './chat-content-blocks.js'
import { readChatJsonResponse } from './chat-bounded-json.js'
import { listProviderModelCatalogAsync, type ProviderModelCatalogItem } from '../model-pricing/model-catalog.service.js'
import { GPT_VENDOR_CODE, normalizeProviderToken } from '../../domain/provider-protocol.js'
import { chatAssetApiMetadata, claimUncommittedChatAssetForDeletion, completeChatAssetDeletion, getChatAsset, releaseChatAssetDeletionClaim } from '../../storage/chat-assets.repository.js'
import { openChatAssetObject, removeChatAssetObject } from '../../storage/chat-asset-storage.js'
import { ChatAssetUploadError, uploadChatAsset } from './chat-asset-upload.js'
import { ChatAssetInputError, resolveChatAssetInput } from './chat-asset-input.js'
import { getChatContextHead, recordChatContextUsage } from '../../storage/chat-context.repository.js'
import { loadChatTransportHistory, ChatModelContextError } from './chat-model-context.js'
import { compactChatContextOnce, scheduleChatContextCompaction } from './chat-context-compaction.js'
import { scheduleChatImageObservations, waitForChatImageObservations } from './chat-image-observation.js'
import { countChatTextTokens } from './chat-token-count.js'
import { buildChatPromptCacheKey } from './chat-prompt-cache.js'

export const chatRouter = Router()

const messageContentBlocksSchema = z.array(z.discriminatedUnion('type', [
  z.object({ type: z.literal('input_text'), text: z.string().max(196_608, '文本块内容过长') }).strict(),
  z.object({ type: z.literal('input_image'), assetId: z.string().trim().min(1, '图片资产 ID 不能为空').max(120) }).strict()
])).max(9).refine(
  (blocks) => blocks.filter((block) => block.type === 'input_image').length <= 4,
  '最多粘贴 4 张图片'
).refine(
  (blocks) => {
    const ids = blocks.flatMap((block) => block.type === 'input_image' ? [block.assetId] : [])
    return new Set(ids).size === ids.length
  },
  '同一张图片不能重复引用'
)
const messageBodySchema = z.object({
  clientMessageId: z.string().trim().min(1).max(100),
  replaceTurnId: z.string().trim().min(1).max(100).optional(),
  content: z.string().trim().min(1, '请输入消息').max(196_608, '消息内容过长'),
  contentBlocks: messageContentBlocksSchema.optional(),
  model: z.string().trim().min(1, '请选择模型').max(200),
  reasoningEffort: z.enum(chatReasoningEfforts).optional(),
  serviceTier: z.enum(chatServiceTiers).optional()
}).strict()
const messagesQuerySchema = z.object({
  beforeSequenceNo: z.preprocess(queryScalar, z.coerce.number().int().min(1).max(2_147_483_647).optional()),
  limit: z.preprocess(queryScalar, z.coerce.number().int().min(1).max(100).default(50))
}).strict()
const submissionParamsSchema = z.object({
  conversationId: z.string().trim().min(1).max(120),
  clientMessageId: z.string().trim().min(1).max(100)
}).strict()
const stopBodySchema = z.object({
  turnId: z.string().trim().min(1).max(100).optional(),
  clientMessageId: z.string().trim().min(1).max(100).optional()
}).strict().refine((value) => value.turnId !== undefined || value.clientMessageId !== undefined, '缺少要停止的消息或轮次')
const createConversationSchema = z.object({ apiKeyId: z.string().trim().min(1, '请选择 API Key') }).strict()
const updateConversationSchema = z.object({
  title: z.string().trim().min(1, '请输入会话标题').max(60, '会话标题最多 60 个字符').optional(),
  isPinned: z.boolean().optional()
}).strict().refine((value) => value.title !== undefined || value.isPinned !== undefined, '没有可更新的会话字段')
const activeStreams = new Map<string, { ownerId: string; turnId: string; controller: AbortController }>()
const activePreparations = new Map<string, ActiveChatPreparation>()
const maxMessageBytes = 192 * 1024
const maxInternalChatRequestBytes = 15 * 1024 * 1024
const storageQuotaBytes = 2 * 1024 * 1024 * 1024

class ChatRequestError extends Error {
  constructor(public readonly code: 'chat_image_not_supported' | 'chat_request_body_too_large', message: string) {
    super(message)
    this.name = 'ChatRequestError'
  }
}

class ChatPreparationCanceledError extends Error {
  constructor() {
    super('消息准备已取消')
    this.name = 'ChatPreparationCanceledError'
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
    const conversations = await listChatConversations(client, {
      systemAccountId: auth.systemAccountId,
      beforeIsPinned: optionalBooleanQuery(req.query.beforeIsPinned),
      beforeLastMessageAt: textQuery(req.query.beforeLastMessageAt),
      beforeId: textQuery(req.query.beforeId),
      limit: integerQuery(req.query.limit, 30, 1, 50)
    })
    res.json(ok(conversations.map(chatConversationResponse)))
  } catch (error) { next(error) }
})

chatRouter.post('/conversations', async (req, res, next) => {
  try {
    const body = createConversationSchema.parse(req.body)
    const auth = requireChatAuth()
    const apiKey = await requireOwnedApiKey(body.apiKeyId, auth.systemAccountId)
    const client = await getChatDatabaseClient()
    const conversation = await createChatConversation(client, {
      systemAccountId: auth.systemAccountId,
      apiKeyId: apiKey.id,
      apiKeyNameSnapshot: apiKey.name,
      now: new Date().toISOString(),
      maxConversationsPerUser: runtimeConfig.chat.maxConversationsPerUser
    })
    res.status(201).json(ok(chatConversationResponse(conversation)))
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

chatRouter.get('/conversations/:conversationId/submissions/:clientMessageId', async (req, res, next) => {
  try {
    const params = submissionParamsSchema.parse(req.params)
    const auth = requireChatAuth()
    const client = await getChatDatabaseClient()
    const conversation = await getChatConversation(client, params.conversationId, auth.systemAccountId)
    if (!conversation) {
      res.status(404).json({ message: '会话不存在', code: 'chat_conversation_not_found' })
      return
    }
    const accepted = await findChatTurnByClientMessageId(client, {
      conversationId: conversation.id,
      systemAccountId: auth.systemAccountId,
      clientMessageId: params.clientMessageId
    })
    if (accepted) {
      res.json(ok({ state: 'accepted', turnId: accepted.turnId, assistantStatus: accepted.assistantStatus }))
      return
    }
    const preparing = hasActiveChatPreparation(activePreparations, {
      conversationId: conversation.id,
      ownerId: auth.systemAccountId,
      clientMessageId: params.clientMessageId
    })
    res.json(ok({ state: preparing ? 'preparing' : 'not_found' }))
  } catch (error) { handleChatRouteError(error, res, next) }
})

chatRouter.get('/conversations/:conversationId/context-status', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const head = await getChatContextHead(await getChatDatabaseClient(), {
      conversationId: req.params.conversationId,
      systemAccountId: auth.systemAccountId
    })
    if (!head) { res.status(404).json({ message: '会话不存在' }); return }
    const usedTokens = head.activeContextTokens ?? 0
    const limitTokens = head.effectiveContextLimitTokens
    res.json(ok({
      usedTokens,
      limitTokens,
      ratio: limitTokens ? Math.min(1, usedTokens / limitTokens) : 0,
      state: head.contextState,
      usageEstimated: head.usageEstimated,
      compactedThroughSequence: head.compactedThroughSequence,
      revision: head.contextRevision
    }))
  } catch (error) { next(error) }
})

chatRouter.get('/conversations/:conversationId', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const conversation = await getChatConversation(await getChatDatabaseClient(), req.params.conversationId, auth.systemAccountId)
    if (!conversation) { res.status(404).json({ message: '会话不存在' }); return }
    res.json(ok(chatConversationResponse(conversation)))
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
    res.json(ok(chatConversationResponse(conversation)))
  } catch (error) { handleChatRouteError(error, res, next) }
})

chatRouter.post('/conversations/:conversationId/assets', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const conversation = await requireOwnedConversation(req.params.conversationId, auth.systemAccountId)
    const asset = await uploadChatAsset({
      req,
      client: await getChatDatabaseClient(),
      systemAccountId: auth.systemAccountId,
      conversationId: conversation.id,
      now: new Date().toISOString(),
      retentionDays: runtimeConfig.chat.retentionDays
    })
    res.status(201).json(ok(chatAssetApiMetadata(asset)))
  } catch (error) { handleChatRouteError(error, res, next) }
})

chatRouter.get('/conversations/:conversationId/assets/:assetId/content', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const conversation = await requireOwnedConversation(req.params.conversationId, auth.systemAccountId)
    const asset = await getChatAsset(await getChatDatabaseClient(), {
      assetId: req.params.assetId,
      systemAccountId: auth.systemAccountId,
      conversationId: conversation.id,
      now: new Date().toISOString()
    })
    if (!asset || asset.processingStatus !== 'ready' || !asset.storageKey || !asset.processedMimeType) {
      res.status(404).json({ message: '图片不存在或已过期' })
      return
    }
    const object = await openChatAssetObject(asset.storageKey)
    res.status(200)
    res.setHeader('Content-Type', asset.processedMimeType)
    res.setHeader('Content-Length', String(object.bytes))
    res.setHeader('Cache-Control', 'private, max-age=3600')
    res.setHeader('X-Content-Type-Options', 'nosniff')
    object.stream.once('error', (error) => {
      if (!res.headersSent) next(error)
      else res.destroy(error)
    })
    res.once('close', () => object.stream.destroy())
    object.stream.pipe(res)
  } catch (error) { next(error) }
})

chatRouter.delete('/conversations/:conversationId/assets/:assetId', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const conversation = await requireOwnedConversation(req.params.conversationId, auth.systemAccountId)
    const client = await getChatDatabaseClient()
    const now = new Date().toISOString()
    const claim = await claimUncommittedChatAssetForDeletion(client, {
      assetId: req.params.assetId,
      systemAccountId: auth.systemAccountId,
      conversationId: conversation.id,
      now
    })
    if (!claim) {
      res.status(409).json({ message: '图片已发送、已删除或当前不能清理', code: 'chat_asset_not_deletable' })
      return
    }
    try {
      await removeChatAssetObject(claim.asset.storageKey)
      if (!await completeChatAssetDeletion(client, { assetId: claim.asset.id, claimId: claim.claimId })) {
        throw new Error('聊天图片删除认领已变化')
      }
      res.status(204).end()
    } catch (error) {
      await releaseChatAssetDeletionClaim(client, {
        assetId: claim.asset.id,
        claimId: claim.claimId,
        errorCode: error instanceof Error ? error.name || 'chat_asset_delete_failed' : 'chat_asset_delete_failed',
        retryAt: new Date(Date.parse(now) + 60_000).toISOString(),
        now
      }).catch(() => false)
      throw error
    }
  } catch (error) { next(error) }
})

chatRouter.get('/conversations/:conversationId/models', async (req, res, next) => {
  try {
    const auth = requireChatAuth()
    const conversation = await requireOwnedConversation(req.params.conversationId, auth.systemAccountId)
    const apiKey = await requireOwnedApiKey(conversation.apiKeyId, auth.systemAccountId)
    const response = await fetch(gatewayUrl('/v1/models'), { headers: { authorization: `Bearer ${apiKey.key}` } })
    const payload = await readChatJsonResponse(response, 4 * 1024 * 1024)
    if (!response.ok) throw new Error(upstreamMessage(payload, `模型列表请求失败（HTTP ${response.status}）`))
    const data = Array.isArray((payload as { data?: unknown }).data) ? (payload as { data: Array<{ id?: unknown }> }).data : []
    const modelIds = data.map((item) => String(item.id ?? '')).filter(Boolean)
    const gatewayKey = await validateGatewayApiKeyAsync(String(apiKey.key))
    const groupIds = gatewayKey?.group_bindings?.map((binding) => binding.group_id) ?? []
    const catalog = await listChatModelCatalog({
      groupIds,
      systemAccountId: auth.systemAccountId
    })
    const modelOptions = await resolveRouteChatModelOptions({
      modelOptions: buildChatModelOptions(modelIds, catalog),
      groupIds,
      systemAccountId: auth.systemAccountId
    })
    res.json(ok(modelOptions))
  } catch (error) { next(error) }
})

chatRouter.post('/conversations/:conversationId/stream', async (req, res, next) => {
  let accepted: Awaited<ReturnType<typeof acceptChatTurn>> | undefined
  let ownerId = ''
  let controller: AbortController | undefined
  let responseClosed = false
  let partialContent = ''
  let preparationConversationId = ''
  let preparationClaim: ActiveChatPreparation | undefined
  const contentBlocks: ChatMessageContentBlock[] = []
  res.once('close', () => {
    responseClosed = true
    controller?.abort()
  })
  try {
    const body = messageBodySchema.parse(req.body)
    const traceId = getTraceId()
    if (Buffer.byteLength(body.content, 'utf8') > maxMessageBytes) {
      res.status(413).json({ message: '消息内容超过 192 KiB 上限' })
      return
    }
    const auth = requireChatAuth()
    ownerId = auth.systemAccountId
    const client = await getChatDatabaseClient()
    if (responseClosed || req.aborted || res.destroyed) return
    const conversation = await getChatConversation(client, req.params.conversationId, ownerId)
    if (responseClosed || req.aborted || res.destroyed) return
    if (!conversation) {
      res.status(404).json({ message: '会话不存在', code: 'chat_conversation_not_found' })
      return
    }
    const existingTurn = await findChatTurnByClientMessageId(client, {
      conversationId: conversation.id,
      systemAccountId: ownerId,
      clientMessageId: body.clientMessageId
    })
    if (responseClosed || req.aborted || res.destroyed) return
    if (existingTurn) {
      res.status(409).json({ message: '该消息已提交，请刷新会话', code: 'chat_message_already_exists' })
      return
    }
    if (!body.replaceTurnId && conversation.userTurnCount >= runtimeConfig.chat.maxTurnsPerConversation) {
      throw new ChatConflictError('chat_turn_limit_exceeded')
    }
    if (conversation.activeTurnId) {
      throw new ChatConflictError(body.replaceTurnId ? 'chat_replace_conflict' : 'chat_message_in_progress')
    }
    preparationConversationId = conversation.id
    preparationClaim = claimActiveChatPreparation(activePreparations, {
      conversationId: preparationConversationId,
      ownerId,
      clientMessageId: body.clientMessageId
    })
    if (!preparationClaim) throw new ChatConflictError(body.replaceTurnId ? 'chat_replace_conflict' : 'chat_message_in_progress')
    controller = preparationClaim.controller
    if (responseClosed || req.aborted || res.destroyed) controller.abort()
    assertChatPreparationActive(preparationClaim)
    if (body.replaceTurnId) {
      await assertChatTurnReplaceable(client, {
        conversationId: conversation.id,
        systemAccountId: ownerId,
        replaceTurnId: body.replaceTurnId,
        now: new Date().toISOString()
      })
    }
    assertChatPreparationActive(preparationClaim)
    const apiKey = await requireOwnedApiKey(conversation.apiKeyId, ownerId)
    const apiKeySecret = String(apiKey.key)
    const gatewayKey = await validateGatewayApiKeyAsync(apiKeySecret)
    assertChatPreparationActive(preparationClaim)
    const accountSupportedProtocols = await resolveChatSupportedProtocols({
      groupIds: gatewayKey?.group_bindings?.map((binding) => binding.group_id) ?? [],
      model: body.model,
      loadAccounts: (groupId, model, endpointFamily) => listCachedOpenAIAccountsForGroupAsync(groupId, ownerId, {
        requestedModel: model,
        requestedEndpointFamily: endpointFamily
      })
    })
    assertChatPreparationActive(preparationClaim)
    if (!accountSupportedProtocols.length) {
      throw new ChatModelCapabilityError('当前 API Key 没有可用于该模型的对话路由，请切换模型或检查账户映射')
    }
    const imageCount = body.contentBlocks?.filter((block) => block.type === 'input_image').length ?? 0
    const catalog = await listChatModelCatalog({
      groupIds: gatewayKey?.group_bindings?.map((binding) => binding.group_id) ?? [],
      systemAccountId: ownerId,
      requestedModel: body.model
    })
    assertChatPreparationActive(preparationClaim)
    const modelOption = buildChatModelOptions([body.model], catalog)[0]
    if (!modelOption) throw new ChatModelCapabilityError('当前模型能力信息不可用，请刷新模型列表')
    const supportsWebSearch = modelOption.supportedTools.includes('web_search')
    const protocol = selectChatTransport({
      supportedProtocols: accountSupportedProtocols,
      preferResponses: supportsWebSearch || imageCount > 0
    })
    const toolsEnabled = protocol === 'responses' && supportsWebSearch
    if (imageCount > 0 && (!modelOption.inputModalities.includes('image') || protocol !== 'responses')) {
      throw new ChatRequestError('chat_image_not_supported', '当前模型或路由不支持图片输入，请切换模型或移除图片')
    }
    const resolvedInput = await resolveChatAssetInput({
      client,
      blocks: body.contentBlocks,
      systemAccountId: ownerId,
      conversationId: conversation.id,
      now: new Date().toISOString()
    })
    assertChatPreparationActive(preparationClaim)
    const modelRequestOptions = resolveChatModelRequestOptions(modelOption, {
      reasoningEffort: body.reasoningEffort,
      serviceTier: body.serviceTier
    })
    const promptCacheKey = modelOption.supportsPromptCaching
      ? buildChatPromptCacheKey({
          systemAccountId: ownerId,
          apiKeyId: apiKey.id,
          conversationId: conversation.id
        })
      : undefined
    const systemInstructions = buildChatSystemInstructions({ toolsEnabled })
    const fixedBudgetInput = {
      currentUserContent: resolveChatBudgetContent({ protocol, currentContent: body.content, currentBlocks: resolvedInput.blocks }),
      instructions: systemInstructions.text,
      toolsEnabled,
      imageTokenEstimate: resolvedInput.imageTokenEstimate,
      maxInputTokens: modelRequestOptions.maxInputTokens
    }
    validateFixedChatInputBudget(fixedBudgetInput)
    const compactionInput = {
      client,
      conversationId: conversation.id,
      systemAccountId: ownerId,
      apiKeySecret,
      gatewayBaseUrl: gatewayUrl(''),
      model: body.model,
      protocol,
      effectiveContextLimitTokens: modelRequestOptions.maxInputTokens
    }
    let contextCompacted = false
    let preparedContext
    try {
      preparedContext = await loadChatTransportHistory({
        client,
        conversationId: conversation.id,
        systemAccountId: ownerId,
        protocol,
        now: new Date().toISOString(),
        excludeTurnId: body.replaceTurnId
      })
      assertChatPreparationActive(preparationClaim)
    } catch (error) {
      if (!(error instanceof ChatModelContextError) || error.reason !== 'load_limit') throw error
      const compacted = await waitForChatPreparation(compactChatContextOnce(compactionInput), preparationClaim)
      assertChatPreparationActive(preparationClaim)
      if (compacted.status !== 'installed') throw error
      contextCompacted = true
      preparedContext = await loadChatTransportHistory({
        client,
        conversationId: conversation.id,
        systemAccountId: ownerId,
        protocol,
        now: new Date().toISOString(),
        excludeTurnId: body.replaceTurnId
      })
    }
    if (preparedContext.unresolvedAssetIds.length) {
      scheduleChatImageObservations({
        client,
        targets: preparedContext.unresolvedAssets,
        conversationId: conversation.id,
        systemAccountId: ownerId,
        apiKeySecret,
        gatewayBaseUrl: gatewayUrl(''),
        model: body.model,
        userContent: '补全此前对话中的图片语义说明',
        assistantContent: ''
      })
      await waitForChatPreparation(waitForChatImageObservations(preparedContext.unresolvedAssetIds, 1_000), preparationClaim)
      assertChatPreparationActive(preparationClaim)
      preparedContext = await loadChatTransportHistory({
        client,
        conversationId: conversation.id,
        systemAccountId: ownerId,
        protocol,
        now: new Date().toISOString(),
        excludeTurnId: body.replaceTurnId
      })
      if (preparedContext.unresolvedAssetIds.length) {
        throw new ChatModelContextError('历史图片语义说明仍在生成，请稍后重试', 'image_pending')
      }
    }
    let estimatedRequestTokens = estimateChatInputTokens({ history: preparedContext.history, ...fixedBudgetInput })
    const effectiveContextLimitTokens = modelRequestOptions.maxInputTokens
    if (effectiveContextLimitTokens && estimatedRequestTokens / effectiveContextLimitTokens >= 0.85) {
      const compacted = await waitForChatPreparation(compactChatContextOnce(compactionInput), preparationClaim)
      assertChatPreparationActive(preparationClaim)
      if (compacted.status === 'installed') {
        contextCompacted = true
        preparedContext = await loadChatTransportHistory({
          client,
          conversationId: conversation.id,
          systemAccountId: ownerId,
          protocol,
          now: new Date().toISOString(),
          excludeTurnId: body.replaceTurnId
        })
        estimatedRequestTokens = estimateChatInputTokens({ history: preparedContext.history, ...fixedBudgetInput })
      }
    }
    if (effectiveContextLimitTokens && estimatedRequestTokens > effectiveContextLimitTokens) throw new ChatContextBudgetError()
    let transport = buildChatTransportRequest({
      protocol,
      instructions: systemInstructions.text,
      model: body.model,
      history: preparedContext.history,
      currentContent: body.content,
      currentBlocks: resolvedInput.blocks,
      toolsEnabled,
      reasoningEffort: modelRequestOptions.reasoningEffort,
      serviceTier: modelRequestOptions.serviceTier,
      promptCacheKey
    })
    let serializedTransportBody = JSON.stringify(transport.body)
    if (Buffer.byteLength(serializedTransportBody, 'utf8') > maxInternalChatRequestBytes && !contextCompacted && preparedContext.history.length) {
      const compacted = await waitForChatPreparation(compactChatContextOnce(compactionInput), preparationClaim)
      assertChatPreparationActive(preparationClaim)
      if (compacted.status === 'installed') {
        contextCompacted = true
        preparedContext = await loadChatTransportHistory({
          client,
          conversationId: conversation.id,
          systemAccountId: ownerId,
          protocol,
          now: new Date().toISOString(),
          excludeTurnId: body.replaceTurnId
        })
        estimatedRequestTokens = estimateChatInputTokens({ history: preparedContext.history, ...fixedBudgetInput })
        if (effectiveContextLimitTokens && estimatedRequestTokens > effectiveContextLimitTokens) throw new ChatContextBudgetError()
        transport = buildChatTransportRequest({
          protocol,
          instructions: systemInstructions.text,
          model: body.model,
          history: preparedContext.history,
          currentContent: body.content,
          currentBlocks: resolvedInput.blocks,
          toolsEnabled,
          reasoningEffort: modelRequestOptions.reasoningEffort,
          serviceTier: modelRequestOptions.serviceTier,
          promptCacheKey
        })
        serializedTransportBody = JSON.stringify(transport.body)
      }
    }
    if (Buffer.byteLength(serializedTransportBody, 'utf8') > maxInternalChatRequestBytes) {
      throw new ChatRequestError('chat_request_body_too_large', '本轮模型请求体仍超过安全上限，请减少图片或等待图片说明完成后重试')
    }
    if (!beginActiveChatAcceptance(activePreparations, conversation.id, preparationClaim.token)) throw new ChatPreparationCanceledError()
    accepted = await acceptChatTurn(client, {
      conversationId: conversation.id,
      systemAccountId: ownerId,
      clientMessageId: body.clientMessageId,
      userContent: body.content,
      contentBlocks: body.contentBlocks,
      model: body.model,
      now: new Date().toISOString(),
      storageQuotaBytes,
      retentionDays: runtimeConfig.chat.retentionDays,
      maxTurnsPerConversation: runtimeConfig.chat.maxTurnsPerConversation,
      replaceTurnId: body.replaceTurnId
    })
    if (accepted.duplicate) {
      res.status(409).json({ message: '该消息已提交，请刷新会话', code: 'chat_message_already_exists' })
      return
    }
    activeStreams.set(conversation.id, { ownerId, turnId: accepted.turnId, controller })
    let heartbeat: ReturnType<typeof setInterval> | undefined
    try {
      if (controller.signal.aborted || responseClosed || req.aborted || res.destroyed) throw new ChatPreparationCanceledError()
      const acceptedContextHead = await getChatContextHead(client, {
        conversationId: conversation.id,
        systemAccountId: ownerId
      })
      if (!acceptedContextHead) throw new Error('聊天上下文状态不存在')
      if (controller.signal.aborted || responseClosed || req.aborted || res.destroyed) throw new ChatPreparationCanceledError()
      res.status(200)
      res.setHeader('Content-Type', 'text/event-stream; charset=utf-8')
      res.setHeader('Cache-Control', 'no-cache, no-transform')
      res.setHeader('X-Accel-Buffering', 'no')
      res.flushHeaders()
      writeSse(res, 'message.started', { turnId: accepted.turnId, userMessage: accepted.userMessage, assistantMessage: accepted.assistantMessage })
      heartbeat = setInterval(() => { if (!res.writableEnded) res.write(': heartbeat\n\n') }, 15_000)
      heartbeat.unref()
      const upstream = await fetch(gatewayUrl(transport.path), {
        method: 'POST',
        headers: {
          authorization: `Bearer ${apiKeySecret}`,
          'content-type': 'application/json',
          accept: 'text/event-stream',
          ...(traceId ? { 'x-trace-id': traceId } : {})
        },
        body: serializedTransportBody,
        signal: controller.signal
      })
      if (!upstream.ok || !upstream.body) {
        const payload = await readChatJsonResponse(upstream, 64 * 1024)
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
          }, maxMessageBytes, runtimeConfig.chat.upstreamSseMaxEvents)
        : await collectOpenAIChatSse(readableStreamChunks(upstream.body), maxMessageBytes, (delta) => {
            partialContent += delta
            if (!res.writableEnded) writeSse(res, 'message.delta', { messageId: accepted?.assistantMessage.id, delta })
          }, runtimeConfig.chat.upstreamSseMaxEvents)
      const finishReason = 'finishReason' in result && typeof result.finishReason === 'string' ? result.finishReason : 'stop'
      if (controller.signal.aborted) throw new ChatPreparationCanceledError()
      await completeChatTurn(client, {
        conversationId: conversation.id, systemAccountId: ownerId, turnId: accepted.turnId,
        assistantContent: result.content, contentBlocks: sanitizeChatContentBlocksForPersistence(contentBlocks), finishReason, traceId: traceId ?? '', now: new Date().toISOString()
      })
      deleteActiveChatStreamIfMatches(activeStreams, conversation.id, accepted.turnId)
      const upstreamUsageAvailable = result.inputTokens !== undefined
      const activeContextTokens = upstreamUsageAvailable
        ? result.inputTokens! + (result.outputTokens ?? countChatTextTokens(result.content))
        : estimatedRequestTokens + countChatTextTokens(result.content) + 12
      await recordChatContextUsage(client, {
        conversationId: conversation.id,
        systemAccountId: ownerId,
        expectedContextRevision: acceptedContextHead.contextRevision,
        activeContextTokens,
        effectiveContextLimitTokens,
        usageEstimated: !upstreamUsageAvailable,
        now: new Date().toISOString()
      }).catch(() => false)
      if (resolvedInput.assetIds.length) {
        const observationTurnId = accepted.turnId
        const observationMessageId = accepted.userMessage.id
        scheduleChatImageObservations({
          client,
          targets: resolvedInput.assetIds.map((assetId) => ({
            assetId,
            expectedTurnId: observationTurnId,
            expectedMessageId: observationMessageId
          })),
          conversationId: conversation.id,
          systemAccountId: ownerId,
          apiKeySecret,
          gatewayBaseUrl: gatewayUrl(''),
          model: body.model,
          userContent: body.content,
          assistantContent: result.content
        })
      }
      if (effectiveContextLimitTokens && activeContextTokens / effectiveContextLimitTokens >= 0.7) {
        scheduleChatContextCompaction({
          client,
          conversationId: conversation.id,
          systemAccountId: ownerId,
          apiKeySecret,
          gatewayBaseUrl: gatewayUrl(''),
          model: body.model,
          protocol,
          effectiveContextLimitTokens
        })
      }
      if (!res.writableEnded) writeSse(res, 'message.completed', { messageId: accepted.assistantMessage.id, finishReason, traceId })
    } catch (error) {
      const canceled = controller.signal.aborted || error instanceof ChatPreparationCanceledError
      const persistedContentBlocks = sanitizeChatContentBlocksForPersistence(contentBlocks)
      try {
        if (canceled) {
          await cancelChatTurn(client, { conversationId: conversation.id, systemAccountId: ownerId, turnId: accepted.turnId, assistantContent: partialContent, contentBlocks: persistedContentBlocks, traceId, now: new Date().toISOString() })
        } else {
          await failChatTurn(client, { conversationId: conversation.id, systemAccountId: ownerId, turnId: accepted.turnId, assistantContent: partialContent, contentBlocks: persistedContentBlocks, errorCode: 'gateway_stream_failed', traceId, now: new Date().toISOString() })
        }
      } catch (finalizeError) {
        const authoritative = await findChatTurnByClientMessageId(client, {
          conversationId: conversation.id,
          systemAccountId: ownerId,
          clientMessageId: body.clientMessageId
        }).catch(() => undefined)
        if (!authoritative || authoritative.turnId !== accepted.turnId || authoritative.assistantStatus === 'streaming') throw finalizeError
      }
      if (!res.headersSent) {
        if (canceled) {
          if (!res.writableEnded && !responseClosed) res.status(499).json({ message: '消息准备已取消', code: 'chat_preparation_canceled' })
        } else {
          throw error
        }
      } else if (!canceled && !res.writableEnded) {
        writeSse(res, 'message.failed', { messageId: accepted.assistantMessage.id, code: 'gateway_stream_failed', message: error instanceof Error ? error.message : '模型请求失败' })
      }
    } finally {
      if (heartbeat) clearInterval(heartbeat)
      deleteActiveChatStreamIfMatches(activeStreams, conversation.id, accepted.turnId)
      if (!res.writableEnded && !responseClosed) res.end()
    }
  } catch (error) {
    if (res.headersSent) {
      if (!res.writableEnded) res.end()
      return
    }
    if (error instanceof ChatPreparationCanceledError || (!accepted && controller?.signal.aborted)) {
      if (!res.writableEnded && !responseClosed) res.status(499).json({ message: '消息准备已取消', code: 'chat_preparation_canceled' })
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
    if (error instanceof ChatAssetInputError) {
      res.status(422).json({ message: error.message, code: error.code })
      return
    }
    if (error instanceof ChatModelCapabilityError) {
      res.status(422).json({ message: error.message, code: error.code })
      return
    }
    if (error instanceof ChatModelContextError) {
      res.status(422).json({ message: error.message, code: error.code })
      return
    }
    handleChatRouteError(error, res, next)
  } finally {
    if (preparationClaim) deleteActiveChatPreparationIfMatches(activePreparations, preparationConversationId, preparationClaim.token)
  }
})

chatRouter.post('/conversations/:conversationId/stop', async (req, res, next) => {
  try {
    const body = stopBodySchema.parse(req.body)
    const auth = requireChatAuth()
    const client = await getChatDatabaseClient()
    const conversation = await getChatConversation(client, req.params.conversationId, auth.systemAccountId)
    if (!conversation) {
      res.status(404).json({ message: '会话不存在', code: 'chat_conversation_not_found' })
      return
    }
    let expectedTurnId = body.turnId
    if (body.clientMessageId) {
      const accepted = await findChatTurnByClientMessageId(client, {
        conversationId: conversation.id,
        systemAccountId: auth.systemAccountId,
        clientMessageId: body.clientMessageId
      })
      if (accepted) {
        if (expectedTurnId && expectedTurnId !== accepted.turnId) {
          res.status(409).json({ message: '要停止的轮次已变化', code: 'chat_turn_mismatch' })
          return
        }
        expectedTurnId = accepted.turnId
      } else if (!expectedTurnId) {
        const preparationPhase = cancelActiveChatPreparation(activePreparations, {
          conversationId: conversation.id,
          ownerId: auth.systemAccountId,
          clientMessageId: body.clientMessageId
        })
        if (preparationPhase) {
          res.status(202).json(ok({ stopped: true, preparationPhase }))
          return
        }
        res.status(404).json({ message: '当前没有匹配的准备或生成任务', code: 'chat_generation_not_found' })
        return
      }
    }
    if (!expectedTurnId) {
      res.status(404).json({ message: '当前没有匹配的生成任务', code: 'chat_generation_not_found' })
      return
    }
    const active = activeStreams.get(conversation.id)
    if (active?.ownerId === auth.systemAccountId && active.turnId === expectedTurnId) {
      active.controller.abort()
      res.status(202).json(ok({ stopped: true, turnId: expectedTurnId }))
      return
    }
    const result = await cancelActiveChatTurnIfMatches(client, {
      conversationId: conversation.id,
      systemAccountId: auth.systemAccountId,
      expectedTurnId,
      now: new Date().toISOString()
    })
    if (result.state === 'canceled' || result.state === 'already_terminal') {
      res.status(202).json(ok({ stopped: true, turnId: expectedTurnId, state: result.state, assistantStatus: result.assistantStatus }))
      return
    }
    if (result.state === 'turn_mismatch') {
      res.status(409).json({ message: '要停止的轮次已变化', code: 'chat_turn_mismatch' })
      return
    }
    res.status(404).json({ message: '当前没有匹配的生成任务', code: 'chat_generation_not_found' })
  } catch (error) { handleChatRouteError(error, res, next) }
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

function assertChatPreparationActive(preparation: ActiveChatPreparation): void {
  if (preparation.controller.signal.aborted) throw new ChatPreparationCanceledError()
}

function waitForChatPreparation<T>(operation: Promise<T>, preparation: ActiveChatPreparation): Promise<T> {
  assertChatPreparationActive(preparation)
  return new Promise<T>((resolve, reject) => {
    const abort = () => reject(new ChatPreparationCanceledError())
    preparation.controller.signal.addEventListener('abort', abort, { once: true })
    operation.then(resolve, reject).finally(() => preparation.controller.signal.removeEventListener('abort', abort))
  })
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
  if (error instanceof ChatAssetUploadError) {
    res.status(error.statusCode).json({ message: error.message, code: error.code })
    return
  }
  if (error instanceof ChatConflictError) {
    res.status(409).json({ message: error.message, code: error.code })
    return
  }
  next(error)
}

function chatConversationResponse(conversation: Awaited<ReturnType<typeof getChatConversation>> extends infer T ? Exclude<T, undefined> : never) {
  return { ...conversation, userTurnLimit: runtimeConfig.chat.maxTurnsPerConversation }
}

function gatewayUrl(path: string): string { return `http://127.0.0.1:${runtimeConfig.port}${path}` }
async function listChatModelCatalog(input: { groupIds: readonly string[]; systemAccountId: string; requestedModel?: string }): Promise<ProviderModelCatalogItem[]> {
  const accountLists = await Promise.all([...new Set(input.groupIds.filter(Boolean))].map((groupId) => (
    listCachedOpenAIAccountsForGroupAsync(groupId, input.systemAccountId, { requestedModel: input.requestedModel })
  )))
  const accounts = accountLists.flat()
  const providerCodes = [...new Set(accounts.map((account) => normalizeProviderToken(account.providerCode)).filter((code): code is string => Boolean(code)))]
  const catalogs = await Promise.all(providerCodes.map(async (providerCode) => {
    const items = await listProviderModelCatalogAsync({
      providerCode,
      systemAccountId: input.systemAccountId,
      includeUnpriced: true
    })
    const providerAccounts = accounts.filter((account) => normalizeProviderToken(account.providerCode) === providerCode)
    return items
      .filter((item) => normalizeProviderToken(item.providerCode) === providerCode)
      .map((item) => constrainCatalogItemForAccountTypes(item, providerCode, providerAccounts.map((account) => account.type)))
  }))
  return catalogs.flat()
}

async function resolveRouteChatModelOptions(input: {
  modelOptions: readonly ChatModelOption[]
  groupIds: readonly string[]
  systemAccountId: string
}): Promise<ChatModelOption[]> {
  const result: ChatModelOption[] = []
  const batchSize = 8
  for (let offset = 0; offset < input.modelOptions.length; offset += batchSize) {
    const batch = await Promise.all(input.modelOptions.slice(offset, offset + batchSize).map(async (modelOption) => {
      const supportedApiProtocols = await resolveChatSupportedProtocols({
        groupIds: input.groupIds,
        model: modelOption.id,
        loadAccounts: (groupId, model, endpointFamily) => listCachedOpenAIAccountsForGroupAsync(groupId, input.systemAccountId, {
          requestedModel: model,
          requestedEndpointFamily: endpointFamily
        })
      })
      return supportedApiProtocols.length ? { ...modelOption, supportedApiProtocols } : undefined
    }))
    result.push(...batch.filter((item): item is NonNullable<typeof item> => Boolean(item)))
  }
  return result
}

function constrainCatalogItemForAccountTypes(
  item: ProviderModelCatalogItem,
  providerCode: string,
  accountTypes: readonly string[]
): ProviderModelCatalogItem {
  if (providerCode !== GPT_VENDOR_CODE || !accountTypes.includes('oauth')) return item
  const supportedServiceTiers = item.supportedServiceTiers.filter((tier) => tier === 'priority')
  return {
    ...item,
    supportedServiceTiers,
    supportsServiceTier: supportedServiceTiers.length > 0
  }
}
function queryScalar(value: unknown): unknown { return Array.isArray(value) ? value[0] : value === '' ? undefined : value }
function textQuery(value: unknown): string | undefined { const raw = Array.isArray(value) ? value[0] : value; return typeof raw === 'string' && raw.trim() ? raw.trim() : undefined }
function optionalIntegerQuery(value: unknown): number | undefined { const text = textQuery(value); if (!text) return undefined; const result = Number(text); return Number.isInteger(result) && result > 0 ? result : undefined }
function optionalBooleanQuery(value: unknown): boolean | undefined { const text = textQuery(value)?.toLowerCase(); if (text === 'true' || text === '1') return true; if (text === 'false' || text === '0') return false; return undefined }
function integerQuery(value: unknown, fallback: number, min: number, max: number): number { const result = optionalIntegerQuery(value); return result === undefined ? fallback : Math.max(min, Math.min(max, result)) }
function writeSse(res: import('express').Response, event: string, data: unknown): void { res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`) }
async function* readableStreamChunks(stream: ReadableStream<Uint8Array>): AsyncGenerator<Uint8Array> { const reader = stream.getReader(); try { while (true) { const next = await reader.read(); if (next.done) return; yield next.value } } finally { reader.releaseLock() } }
function upstreamMessage(payload: unknown, fallback: string): string { if (payload && typeof payload === 'object') { const item = payload as { message?: unknown; error?: { message?: unknown } }; if (typeof item.error?.message === 'string') return item.error.message; if (typeof item.message === 'string') return item.message } return fallback }

function projectToolEvent(blocks: ChatMessageContentBlock[], eventType: 'tool_started' | 'tool_updated' | 'tool_completed', item: Record<string, unknown>): void {
  const id = String(item.id ?? item.call_id ?? `tool_${blocks.filter((block) => block.type === 'tool_call').length + 1}`)
  const status = eventType === 'tool_started' ? 'started' : eventType === 'tool_updated' ? 'updated' : 'completed'
  const existing = blocks.find((block): block is Extract<ChatMessageContentBlock, { type: 'tool_call' }> => block.type === 'tool_call' && block.id === id)
  if (existing) { existing.status = status; existing.toolType = String(item.type ?? existing.toolType); existing.item = item; return }
  blocks.push({ type: 'tool_call', id, toolType: String(item.type ?? 'tool'), status, item })
}

<template>
  <section class="chat-workspace">
    <aside v-if="!mobile" class="conversation-panel">
      <ConversationPane />
    </aside>
    <a-drawer v-else v-model:open="conversationDrawerOpen" title="对话记录" placement="left" :width="300" :body-style="{ padding: 0 }">
      <ConversationPane @selected="conversationDrawerOpen = false" />
    </a-drawer>

    <main class="chat-main">
      <template v-if="selectedConversation">
        <ChatMessageList
          ref="messageList"
          :messages="messages"
          :loading="messagesLoading"
          :editable-message-id="generating || submissionBlocked ? undefined : editableUserMessageId"
          :editing-turn-id="editingTurn?.turnId"
          @near-top="loadOlderMessages"
          @jump-visibility="showJumpToBottom = $event"
          @edit-message="beginTurnEdit"
        />
        <footer class="composer-shell">
          <a-tooltip v-if="showJumpToBottom" title="回到底部"><a-button class="jump-bottom-button" shape="circle" aria-label="回到底部" @click="messageList?.scrollToBottom()"><ArrowDownOutlined /></a-button></a-tooltip>
          <div v-if="pendingConfirmation" class="submission-confirmation-bar">
            <span>正在确认上一条消息是否已提交，确认前不会重复发送</span>
            <a-button type="link" size="small" :loading="confirmingSubmission" @click="retryPendingConfirmation">重新确认</a-button>
          </div>
          <div v-else-if="editingTurn" class="turn-editing-bar">
            <span>正在修改最近一轮消息</span>
            <a-button type="link" size="small" :disabled="editingTurn.phase === 'submitting'" @click="cancelTurnEdit">取消编辑</a-button>
          </div>
          <AIComposer
            ref="composer"
            v-model="selectedModel"
            v-model:reasoning-effort="selectedReasoningEffort"
            v-model:service-tier="selectedServiceTier"
            v-model:context-window-tokens="selectedContextWindowTokens"
            :disabled="generating || submissionBlocked"
            :model-options="models"
            :models-loading="modelsLoading"
            :show-conversation-button="mobile"
            @open-conversations="conversationDrawerOpen = true"
            @submit="handleComposerSubmit"
            @stop="stopGeneration"
          />
        </footer>
      </template>
      <div v-else class="chat-start-state">
        <MessageOutlined />
        <strong>新建对话后开始提问</strong>
        <a-button type="primary" :disabled="!apiKeys.length" @click="openCreateDialog"><PlusOutlined />新建对话</a-button>
      </div>
    </main>

    <a-modal v-model:open="createDialogOpen" title="新建对话" ok-text="创建" cancel-text="取消" :confirm-loading="creating" :ok-button-props="{ disabled: !newApiKeyId }" @ok="createConversation">
      <a-form layout="vertical"><a-form-item label="API Key" required><a-select v-model:value="newApiKeyId" :options="apiKeyOptions" placeholder="选择自己的 API Key" /></a-form-item></a-form>
    </a-modal>

    <div v-if="conversationMenu" class="conversation-context-menu" :style="{ left: `${conversationMenu.x}px`, top: `${conversationMenu.y}px` }" @click.stop>
      <button type="button" @click="openRenameDialog(conversationMenu.item)">重命名</button>
      <button type="button" @click="togglePinned(conversationMenu.item)">{{ conversationMenu.item.isPinned ? '取消置顶' : '置顶' }}</button>
      <button type="button" @click="openDetails(conversationMenu.item)">详情</button>
      <button type="button" class="is-danger" @click="openDeleteDialog(conversationMenu.item)">删除</button>
    </div>

    <a-modal v-model:open="renameDialogOpen" title="重命名会话" ok-text="保存" cancel-text="取消" :confirm-loading="conversationUpdating" @ok="renameConversation">
      <a-input v-model:value="renameTitle" :maxlength="60" placeholder="输入会话标题" @press-enter="renameConversation" />
    </a-modal>
    <a-modal v-model:open="detailsDialogOpen" title="会话详情" :closable="false">
      <a-descriptions v-if="detailConversation" :column="1" size="small" bordered>
        <a-descriptions-item label="标题">{{ detailConversation.title }}</a-descriptions-item>
        <a-descriptions-item label="API Key">{{ detailConversation.apiKeyNameSnapshot }}</a-descriptions-item>
        <a-descriptions-item label="最近模型">{{ detailConversation.lastModel || '未使用' }}</a-descriptions-item>
        <a-descriptions-item label="状态">{{ detailConversation.activeTurnId ? '生成中' : '空闲' }}</a-descriptions-item>
        <a-descriptions-item label="置顶">{{ detailConversation.isPinned ? '是' : '否' }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDetailTime(detailConversation.createdAt) }}</a-descriptions-item>
        <a-descriptions-item label="更新时间">{{ formatDetailTime(detailConversation.updatedAt) }}</a-descriptions-item>
      </a-descriptions>
      <template #footer><a-button @click="detailsDialogOpen = false">关闭</a-button></template>
    </a-modal>
    <a-modal v-model:open="deleteDialogOpen" title="删除会话" ok-text="删除" cancel-text="取消" ok-type="danger" :confirm-loading="conversationUpdating" @ok="confirmDeleteConversation">
      删除后聊天记录无法恢复，确定删除“{{ pendingConversation?.title }}”吗？
    </a-modal>
  </section>
</template>

<script setup lang="ts">
import { ArrowDownOutlined, MessageOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, defineComponent, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { chatApi, ChatStreamHttpError, streamChatMessage } from '@/api/domains/chat'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ChatApiKeyOption, ChatConversation, ChatMessage, ChatModelOption, ChatReasoningEffort, ChatServiceTier } from '@/types/domain/chat'
import { applyChatStreamEvent } from './chatStream'
import { beginLatestTurnEdit, isDefinitiveChatHttpRejection, resolveChatReconciliationNotice, resolveChatSubmitFailure } from './chatTurnEditing'
import { applyChatReconciliationIfActive, reconcileChatSubmission, type ChatSubmissionReconciliation } from './chatTurnReconciliation'
import { createChatConversationSummaryRefresher, mergeChatConversationSummary } from './chatConversationSummary'
import { isCurrentChatConversationLoad } from './chatConversationLoad'
import { stopActiveChatGeneration } from './chatStopGeneration'
import ChatMessageList from './ChatMessageList.vue'
import AIComposer from './composer/AIComposer.vue'
import type { ChatInputBlock } from './composer/chatComposerDocument'
import type { JSONContent } from '@tiptap/core'

interface ChatTurnEditingState {
  conversationId: string
  turnId: string
  userMessageId: string
  assistantMessageId: string
  content: string
  displacedDraft: JSONContent
  phase: 'editing' | 'submitting'
}

interface ChatRequestContext {
  readonly conversationId: string
  readonly clientMessageId: string
  readonly replaceTurnId?: string
  readonly snapshot: JSONContent
}

interface PendingSubmissionConfirmation {
  readonly request: ChatRequestContext
  readonly streamStarted: boolean
  readonly startedTurnId?: string
  readonly silent: boolean
  readonly errorMessage: string
}

const conversations = ref<ChatConversation[]>([])
const conversationCursor = ref<ChatConversation>()
const conversationsLoadingMore = ref(false)
const hasMoreConversations = ref(false)
const apiKeys = ref<ChatApiKeyOption[]>([])
const selectedConversationId = ref<string>()
const messages = ref<ChatMessage[]>([])
const models = ref<ChatModelOption[]>([])
const selectedModel = ref<string>()
const selectedReasoningEffort = ref<ChatReasoningEffort | ''>('')
const selectedServiceTier = ref<ChatServiceTier | ''>('')
const selectedContextWindowTokens = ref(0)
const messagesLoading = ref(false)
const olderMessagesLoading = ref(false)
const hasOlderMessages = ref(false)
const modelsLoading = ref(false)
const creating = ref(false)
const generating = ref(false)
const stopping = ref(false)
const confirmingSubmission = ref(false)
const pendingConfirmation = ref<PendingSubmissionConfirmation>()
const createDialogOpen = ref(false)
const conversationDrawerOpen = ref(false)
const newApiKeyId = ref<string>()
const mobile = ref(false)
const conversationMenu = ref<{ item: ChatConversation; x: number; y: number }>()
const renameDialogOpen = ref(false)
const detailsDialogOpen = ref(false)
const deleteDialogOpen = ref(false)
const conversationUpdating = ref(false)
const showJumpToBottom = ref(false)
const pendingConversation = ref<ChatConversation>()
const detailConversation = ref<ChatConversation>()
const renameTitle = ref('')
const messageList = ref<InstanceType<typeof ChatMessageList>>()
const composer = ref<InstanceType<typeof AIComposer>>()
const editingTurn = ref<ChatTurnEditingState>()
let streamController: AbortController | undefined
let activeSendSettled: Promise<void> | undefined
let pendingConfirmationTimer: number | undefined
let conversationLoadEpoch = 0
let disposed = false

const selectedConversation = computed(() => conversations.value.find((item) => item.id === selectedConversationId.value))
const apiKeyOptions = computed(() => apiKeys.value.map((item) => ({ label: item.name, value: item.id })))
const editableUserMessageId = computed(() => {
  const candidate = messages.value[messages.value.length - 2]
  return candidate && beginLatestTurnEdit(messages.value, candidate.id)?.userMessageId
})
const submissionBlocked = computed(() => stopping.value || Boolean(pendingConfirmation.value))
const refreshConversationSummary = createChatConversationSummaryRefresher({
  load: chatApi.getConversation,
  apply: (item) => {
    if (disposed) return
    replaceConversation(mergeChatConversationSummary(conversations.value.find((current) => current.id === item.id), item))
    sortConversations()
  }
})

const ConversationPane = defineComponent({
  emits: ['selected'],
  setup(_props, { emit }) {
    return () => h('div', { class: 'conversation-pane-inner' }, [
      h('div', { class: 'conversation-pane-toolbar' }, [h('strong', '对话'), h('button', { class: 'conversation-new-button', type: 'button', disabled: !apiKeys.value.length, onClick: openCreateDialog }, [h(PlusOutlined), ' 新建'])]),
      conversations.value.length
        ? h('div', { class: 'conversation-list' }, [
            ...conversations.value.map((item) => h('button', { class: ['conversation-item', { active: item.id === selectedConversationId.value }], type: 'button', title: item.title, onContextmenu: (event: MouseEvent) => openConversationMenu(event, item), onClick: () => { void selectConversation(item.id); emit('selected') } }, item.title)),
            ...(hasMoreConversations.value ? [h('button', { class: 'conversation-load-more', type: 'button', disabled: conversationsLoadingMore.value, onClick: () => { void loadMoreConversations() } }, conversationsLoadingMore.value ? '正在加载' : '加载更多')] : [])
          ])
        : h('div', { class: 'conversation-list-empty' }, '暂无对话')
    ])
  }
})

async function loadInitial(): Promise<void> {
  try {
    const [keyItems, conversationItems] = await Promise.all([chatApi.listApiKeys(), chatApi.listConversations({ limit: 50 })])
    apiKeys.value = keyItems
    conversations.value = conversationItems
    conversationCursor.value = conversationItems.at(-1)
    hasMoreConversations.value = conversationItems.length === 50
    if (conversationItems[0]) await selectConversation(conversationItems[0].id)
  } catch (error) { message.error(extractApiErrorMessage(error, '加载 AI 问答失败')) }
}
async function loadMoreConversations(): Promise<void> {
  const last = conversationCursor.value
  if (!last || !hasMoreConversations.value || conversationsLoadingMore.value) return
  conversationsLoadingMore.value = true
  try {
    const items = await chatApi.listConversations({ beforeIsPinned: last.isPinned, beforeLastMessageAt: last.lastMessageAt, beforeId: last.id, limit: 50 })
    const knownIds = new Set(conversations.value.map((item) => item.id))
    conversations.value.push(...items.filter((item) => !knownIds.has(item.id)))
    conversationCursor.value = items.at(-1) ?? last
    hasMoreConversations.value = items.length === 50
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载更多会话失败'))
  } finally {
    conversationsLoadingMore.value = false
  }
}
async function selectConversation(id: string): Promise<void> {
  if (generating.value || submissionBlocked.value || selectedConversationId.value === id) return
  await cancelTurnEdit()
  const loadEpoch = ++conversationLoadEpoch
  selectedConversationId.value = id
  messages.value = []
  models.value = []
  selectedModel.value = undefined
  hasOlderMessages.value = false
  olderMessagesLoading.value = false
  showJumpToBottom.value = false
  messagesLoading.value = true
  modelsLoading.value = true
  try {
    const [messageItems, modelItems] = await Promise.all([chatApi.listMessages(id, { limit: 100 }), chatApi.listModels(id)])
    if (!isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) return
    messages.value = messageItems
    hasOlderMessages.value = messageItems.length === 100
    models.value = modelItems
    const conversation = conversations.value.find((item) => item.id === id)
    selectedModel.value = conversation?.lastModel && modelItems.some((item) => item.id === conversation.lastModel) ? conversation.lastModel : modelItems[0]?.id
  } catch (error) {
    if (isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) {
      message.error(extractApiErrorMessage(error, '加载对话失败'))
    }
  } finally {
    if (isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) {
      messagesLoading.value = false
      modelsLoading.value = false
    }
  }
}
async function loadOlderMessages(): Promise<void> {
  const id = selectedConversationId.value
  const loadEpoch = conversationLoadEpoch
  const first = messages.value[0]
  if (!id || !first || !hasOlderMessages.value || olderMessagesLoading.value || messagesLoading.value) return
  olderMessagesLoading.value = true
  const anchor = messageList.value?.captureScrollAnchor()
  try {
    const older = await chatApi.listMessages(id, { beforeSequenceNo: first.sequenceNo, limit: 100 })
    if (!isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) return
    messages.value = [...older, ...messages.value]
    hasOlderMessages.value = older.length === 100
    if (anchor) await messageList.value?.restoreScrollAnchor(anchor)
  } catch (error) {
    if (isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) {
      message.error(extractApiErrorMessage(error, '加载更早消息失败'))
    }
  } finally {
    if (isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) {
      olderMessagesLoading.value = false
    }
  }
}
function openCreateDialog(): void { newApiKeyId.value = apiKeys.value[0]?.id; createDialogOpen.value = true }
async function createConversation(): Promise<void> {
  if (!newApiKeyId.value) return
  creating.value = true
  try { const item = await chatApi.createConversation(newApiKeyId.value); conversations.value.unshift(item); createDialogOpen.value = false; await selectConversation(item.id) }
  catch (error) { message.error(extractApiErrorMessage(error, '创建对话失败')) }
  finally { creating.value = false }
}
async function sendMessage(content: string, snapshot: JSONContent, blocks: ChatInputBlock[]): Promise<void> {
  const conversation = selectedConversation.value
  const model = selectedModel.value
  if (!conversation || !content.trim() || !model || generating.value || submissionBlocked.value) return
  const activeEdit = editingTurn.value?.conversationId === conversation.id ? editingTurn.value : undefined
  if (activeEdit) activeEdit.phase = 'submitting'
  const requestContext: ChatRequestContext = Object.freeze({
    conversationId: conversation.id,
    clientMessageId: crypto.randomUUID(),
    replaceTurnId: activeEdit?.turnId,
    snapshot: cloneDocument(snapshot)
  })
  generating.value = true
  messageList.value?.scrollToBottom()
  const controller = new AbortController()
  streamController = controller
  let streamStarted = false
  let streamTerminal = false
  let startedTurnId: string | undefined
  try {
    await streamChatMessage({
      conversationId: requestContext.conversationId,
      clientMessageId: requestContext.clientMessageId,
      replaceTurnId: requestContext.replaceTurnId,
      content,
      contentBlocks: blocks.map((block) => block.type === 'input_image' ? { type: block.type, dataUrl: block.dataUrl } : { type: block.type, text: block.text }),
      model,
      reasoningEffort: selectedReasoningEffort.value || undefined,
      serviceTier: selectedServiceTier.value || undefined,
      contextWindowTokens: selectedContextWindowTokens.value || undefined,
      signal: controller.signal,
      onEvent: (event) => {
        if (selectedConversationId.value !== requestContext.conversationId) return
        if (event.type === 'message.started') {
          streamStarted = true
          startedTurnId = event.data.turnId
          applyChatStreamEvent(messages.value, event, { replaceTurnId: requestContext.replaceTurnId })
          finishAcceptedTurnEdit(requestContext)
          void refreshConversationSummary(requestContext.conversationId).catch(() => undefined)
        } else {
          applyChatStreamEvent(messages.value, event)
        }
        if (event.type === 'message.completed' || event.type === 'message.failed') streamTerminal = true
        if (event.type === 'message.failed') message.error(event.data.message)
      }
    })
    if (!streamStarted) throw new Error('模型响应缺少开始事件，请刷新后确认消息状态')
    if (!streamTerminal) throw new Error('模型流已中断，正在确认消息终态')
    await refreshConversationSummary(requestContext.conversationId)
  } catch (error) {
    await handleSubmitFailure(error, requestContext, streamStarted, startedTurnId, controller.signal.aborted)
  } finally {
    generating.value = false
    if (streamController === controller) streamController = undefined
  }
}
async function stopGeneration(): Promise<void> {
  const id = selectedConversationId.value
  if (!id || stopping.value) return
  const controller = streamController
  const sendSettled = activeSendSettled
  stopping.value = true
  try {
    await stopActiveChatGeneration({ controller, sendSettled, stop: () => chatApi.stop(id) })
  } finally {
    stopping.value = false
  }
}
async function refreshMessages(conversationId = selectedConversationId.value): Promise<ChatMessage[]> {
  if (!conversationId) return []
  const latest = await chatApi.listMessages(conversationId, { limit: 100 })
  if (!disposed && selectedConversationId.value === conversationId) {
    messages.value = latest
    hasOlderMessages.value = latest.length === 100
  }
  return latest
}
async function removeConversation(id: string): Promise<void> { try { await chatApi.deleteConversation(id); conversations.value = conversations.value.filter((item) => item.id !== id); if (selectedConversationId.value === id) { selectedConversationId.value = undefined; messages.value = []; const next = conversations.value[0]; if (next) await selectConversation(next.id) } } catch (error) { message.error(extractApiErrorMessage(error, '删除对话失败')) } }
function handleComposerSubmit(payload: { blocks: ChatInputBlock[]; snapshot: JSONContent }): void {
  if (generating.value || submissionBlocked.value) { composer.value?.restore(payload.snapshot); return }
  if (!selectedConversation.value || !selectedModel.value || modelsLoading.value) {
    composer.value?.restore(payload.snapshot)
    message.warning(modelsLoading.value ? '模型仍在加载，请稍后发送' : '当前没有可用模型')
    return
  }
  const content = payload.blocks.map((item) => item.type === 'input_image' ? '[图片]' : item.text).join('\n')
  const sendSettled = sendMessage(content, payload.snapshot, payload.blocks)
  activeSendSettled = sendSettled
  void sendSettled.finally(() => { if (activeSendSettled === sendSettled) activeSendSettled = undefined })
}
function beginTurnEdit(messageItem: ChatMessage): void {
  if (generating.value || submissionBlocked.value || editingTurn.value) return
  const candidate = beginLatestTurnEdit(messages.value, messageItem.id)
  const editor = composer.value
  if (!candidate || !editor || candidate.conversationId !== selectedConversationId.value) return
  editingTurn.value = {
    ...candidate,
    displacedDraft: editor.getSnapshot(),
    phase: 'editing'
  }
  editor.setText(candidate.content)
  editor.focus()
}
async function cancelTurnEdit(): Promise<void> {
  const current = editingTurn.value
  if (!current || current.phase === 'submitting') return
  editingTurn.value = undefined
  composer.value?.restore(current.displacedDraft)
}
function finishAcceptedTurnEdit(request: ChatRequestContext): void {
  const current = editingTurn.value
  if (!request.replaceTurnId || current?.turnId !== request.replaceTurnId) return
  editingTurn.value = undefined
  composer.value?.restore(current.displacedDraft)
}
async function handleSubmitFailure(error: unknown, request: ChatRequestContext, streamStarted: boolean, startedTurnId: string | undefined, silent: boolean): Promise<void> {
  const replaceConflict = error instanceof ChatStreamHttpError && error.code === 'chat_replace_conflict'
  const errorMessage = extractApiErrorMessage(error, '发送失败')
  await applyChatReconciliationIfActive({
    reconcile: () => reconcileChatSubmission({
      clientMessageId: request.clientMessageId,
      acceptedTurnId: startedTurnId,
      confirmPendingAcceptance: !(error instanceof ChatStreamHttpError) || error.code === 'chat_message_already_exists',
      listMessages: () => refreshMessages(request.conversationId),
      stop: async () => { await chatApi.stop(request.conversationId) }
    }),
    isDisposed: () => disposed,
    apply: async (reconciliation) => {
      if (!reconciliation.confirmed && !replaceConflict && !isDefinitiveChatRejection(error)) {
        enterPendingConfirmation({ request, streamStarted, startedTurnId, silent, errorMessage })
        return
      }
      await applySubmissionOutcome({ request, streamStarted, silent, errorMessage, replaceConflict, reconciliation })
    }
  })
}
async function applySubmissionOutcome(input: {
  request: ChatRequestContext
  streamStarted: boolean
  silent: boolean
  errorMessage: string
  replaceConflict: boolean
  reconciliation: ChatSubmissionReconciliation
}): Promise<void> {
  if (disposed) return
  const accepted = input.streamStarted || input.reconciliation.accepted
  if (input.reconciliation.accepted) {
    try { await refreshConversationSummary(input.request.conversationId) } catch {}
    if (disposed) return
  }
  const resolution = resolveChatSubmitFailure({ streamStarted: input.streamStarted, accepted, confirmed: true, replaceConflict: input.replaceConflict })
  if (resolution.clearEditing) {
    if (accepted && !input.replaceConflict) finishAcceptedTurnEdit(input.request)
    else editingTurn.value = undefined
  } else {
    const currentEdit = editingTurn.value
    if (currentEdit && currentEdit.turnId === input.request.replaceTurnId) currentEdit.phase = 'editing'
  }
  if (resolution.restoreSubmittedDraft) composer.value?.restore(input.request.snapshot)
  if (input.replaceConflict) {
    message.warning('最近一轮已变化，已保留当前草稿，请重新确认后发送')
    return
  }
  const notice = resolveChatReconciliationNotice({ accepted, assistantStatus: input.reconciliation.terminal ? input.reconciliation.assistantStatus : 'streaming', silent: input.silent })
  if (notice === 'none') return
  if (notice === 'pending') message.warning('消息已提交，后台仍在结束生成，请稍后刷新会话')
  else if (notice === 'stopped') message.warning('连接已中断，本轮生成已停止')
  else if (notice === 'failed') message.error('消息已提交，但模型生成失败')
  else message.error(input.errorMessage)
}
function enterPendingConfirmation(input: PendingSubmissionConfirmation): void {
  if (disposed) return
  pendingConfirmation.value = Object.freeze(input)
  message.warning('暂时无法确认消息是否已提交，已暂停新的发送并将在后台重试')
  schedulePendingConfirmation()
}
function schedulePendingConfirmation(): void {
  if (pendingConfirmationTimer !== undefined) window.clearTimeout(pendingConfirmationTimer)
  if (disposed || !pendingConfirmation.value) return
  pendingConfirmationTimer = window.setTimeout(() => { pendingConfirmationTimer = undefined; void retryPendingConfirmation() }, 1_200)
}
async function retryPendingConfirmation(): Promise<void> {
  const pending = pendingConfirmation.value
  if (disposed || !pending || confirmingSubmission.value) return
  if (pendingConfirmationTimer !== undefined) { window.clearTimeout(pendingConfirmationTimer); pendingConfirmationTimer = undefined }
  confirmingSubmission.value = true
  try {
    const reconciliation = await reconcileChatSubmission({
      clientMessageId: pending.request.clientMessageId,
      acceptedTurnId: pending.startedTurnId,
      confirmPendingAcceptance: true,
      listMessages: () => refreshMessages(pending.request.conversationId),
      stop: async () => { await chatApi.stop(pending.request.conversationId) },
      maxAttempts: 4
    })
    if (disposed || pendingConfirmation.value !== pending) return
    if (!reconciliation.confirmed) { schedulePendingConfirmation(); return }
    pendingConfirmation.value = undefined
    await applySubmissionOutcome({
      request: pending.request,
      streamStarted: pending.streamStarted,
      silent: pending.silent,
      errorMessage: pending.errorMessage,
      replaceConflict: false,
      reconciliation
    })
  } finally {
    if (!disposed) confirmingSubmission.value = false
  }
}
function isDefinitiveChatRejection(error: unknown): boolean {
  if (!(error instanceof ChatStreamHttpError)) return false
  return isDefinitiveChatHttpRejection({ status: error.status, code: error.code })
}
function cloneDocument(document: JSONContent): JSONContent { return JSON.parse(JSON.stringify(document)) as JSONContent }
function updateMobile(): void { mobile.value = window.innerWidth <= 820 }
function openConversationMenu(event: MouseEvent, item: ChatConversation): void { event.preventDefault(); conversationMenu.value = { item, x: Math.min(event.clientX, window.innerWidth - 150), y: Math.min(event.clientY, window.innerHeight - 160) } }
function closeConversationMenu(): void { conversationMenu.value = undefined }
function openRenameDialog(item: ChatConversation): void { pendingConversation.value = item; renameTitle.value = item.title; renameDialogOpen.value = true; closeConversationMenu() }
function openDetails(item: ChatConversation): void { detailConversation.value = item; detailsDialogOpen.value = true; closeConversationMenu() }
function openDeleteDialog(item: ChatConversation): void { pendingConversation.value = item; deleteDialogOpen.value = true; closeConversationMenu() }
async function renameConversation(): Promise<void> { const item = pendingConversation.value; const title = renameTitle.value.trim(); if (!item || !title || conversationUpdating.value) return; conversationUpdating.value = true; try { replaceConversation(await chatApi.updateConversation(item.id, { title })); renameDialogOpen.value = false } catch (error) { message.error(extractApiErrorMessage(error, '重命名失败')) } finally { conversationUpdating.value = false } }
async function togglePinned(item: ChatConversation): Promise<void> { closeConversationMenu(); try { replaceConversation(await chatApi.updateConversation(item.id, { isPinned: !item.isPinned })); sortConversations() } catch (error) { message.error(extractApiErrorMessage(error, '更新置顶状态失败')) } }
async function confirmDeleteConversation(): Promise<void> { const item = pendingConversation.value; if (!item || conversationUpdating.value) return; conversationUpdating.value = true; try { await removeConversation(item.id); deleteDialogOpen.value = false } finally { conversationUpdating.value = false } }
function replaceConversation(next: ChatConversation): void { const index = conversations.value.findIndex((item) => item.id === next.id); if (index >= 0) conversations.value[index] = next; if (detailConversation.value?.id === next.id) detailConversation.value = next }
function sortConversations(): void { conversations.value.sort((left, right) => Number(right.isPinned) - Number(left.isPinned) || Date.parse(right.lastMessageAt) - Date.parse(left.lastMessageAt) || right.id.localeCompare(left.id)) }
function formatDetailTime(value: string): string { const date = new Date(value); return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString('zh-CN', { hour12: false }) }
function resetModelControls(): void { const model = models.value.find((item) => item.id === selectedModel.value); selectedReasoningEffort.value = model?.defaultReasoningEffort ?? ''; selectedServiceTier.value = ''; selectedContextWindowTokens.value = 0 }
watch(selectedModel, resetModelControls)
onMounted(() => { updateMobile(); window.addEventListener('resize', updateMobile); window.addEventListener('click', closeConversationMenu); window.addEventListener('blur', closeConversationMenu); void loadInitial() })
onBeforeUnmount(() => { disposed = true; conversationLoadEpoch += 1; window.removeEventListener('resize', updateMobile); window.removeEventListener('click', closeConversationMenu); window.removeEventListener('blur', closeConversationMenu); if (pendingConfirmationTimer !== undefined) { window.clearTimeout(pendingConfirmationTimer); pendingConfirmationTimer = undefined }; streamController?.abort() })
</script>

<style scoped>
.chat-workspace { height: 100vh; height: 100dvh; min-height: 520px; display: grid; grid-template-columns: 260px minmax(0, 1fr); overflow: hidden; background: #fff; border: 0; border-radius: 0; }
.conversation-panel { min-width: 0; border-right: 1px solid #e2e8f0; background: #f8fafc; }
.chat-main { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
.composer-shell { position: relative; padding: 12px clamp(12px, 3vw, 28px) 14px; border-top: 1px solid #e2e8f0; background: #fff; }
.turn-editing-bar { display: flex; align-items: center; justify-content: space-between; min-height: 30px; padding: 0 4px 4px; color: #64748b; font-size: 12px; }
.submission-confirmation-bar { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 34px; padding: 0 4px 4px; color: #b45309; font-size: 12px; }
.jump-bottom-button { position: absolute; z-index: 4; top: -46px; left: 50%; color: #475569; background: rgba(255, 255, 255, .96); border-color: #d9e0e8; box-shadow: 0 4px 14px rgba(15, 23, 42, .14); transform: translateX(-50%); }
.chat-start-state { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 14px; color: #64748b; }
.chat-start-state > :deep(.anticon) { font-size: 36px; color: #94a3b8; }
.chat-start-state strong { color: #334155; font-size: 16px; }
:deep(.conversation-pane-inner) { height: 100%; display: flex; flex-direction: column; }
:deep(.conversation-pane-toolbar) { height: 58px; display: flex; align-items: center; justify-content: space-between; padding: 0 12px 0 16px; border-bottom: 1px solid #e2e8f0; }
:deep(.conversation-pane-toolbar strong) { color: #172033; font-size: 15px; }
:deep(.conversation-new-button) { height: 30px; display: inline-flex; align-items: center; padding: 0 9px; color: #1677ff; background: #fff; border: 1px solid #b9d7ff; border-radius: 6px; cursor: pointer; }
:deep(.conversation-new-button:disabled) { color: #94a3b8; border-color: #e2e8f0; cursor: not-allowed; }
:deep(.conversation-list) { flex: 1; min-height: 0; overflow-y: auto; padding: 8px; }
:deep(.conversation-item) { width: 100%; height: 38px; display: block; overflow: hidden; margin-bottom: 3px; padding: 0 10px; color: #273449; font-size: 13px; line-height: 36px; text-align: left; text-overflow: ellipsis; white-space: nowrap; background: transparent; border: 1px solid transparent; border-radius: 6px; cursor: pointer; }
:deep(.conversation-item:hover) { background: #fff; border-color: #e2e8f0; }
:deep(.conversation-item.active) { background: #eaf3ff; border-color: #b9d7ff; }
:deep(.conversation-load-more) { width: 100%; height: 34px; color: #64748b; background: transparent; border: 0; cursor: pointer; }
:deep(.conversation-load-more:hover) { color: #1677ff; }
:deep(.conversation-load-more:disabled) { color: #94a3b8; cursor: wait; }
:deep(.conversation-list-empty) { padding: 32px 12px; color: #94a3b8; text-align: center; }
.conversation-context-menu { position: fixed; z-index: 1100; width: 136px; padding: 5px; background: #fff; border: 1px solid #e2e8f0; border-radius: 7px; box-shadow: 0 10px 26px rgba(15, 23, 42, .16); }
.conversation-context-menu button { width: 100%; display: block; padding: 7px 9px; color: #334155; text-align: left; background: transparent; border: 0; border-radius: 5px; cursor: pointer; }
.conversation-context-menu button:hover { background: #f1f5f9; }
.conversation-context-menu button.is-danger { color: #dc2626; }
@media (max-width: 991px) and (min-width: 821px) { :deep(.conversation-pane-toolbar) { padding-left: 56px; } }
@media (max-width: 820px) { .chat-workspace { height: 100vh; height: 100dvh; min-height: 440px; grid-template-columns: minmax(0, 1fr); } .composer-shell { padding: 9px; } :deep(.message-virtual-space) { margin-top: 52px; } :deep(.message-loading), :deep(.message-empty) { padding-top: 52px; } }
</style>

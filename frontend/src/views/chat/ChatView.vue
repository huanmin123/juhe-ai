<template>
  <section class="chat-workspace">
    <aside v-if="!mobile" class="conversation-panel">
      <ConversationPane />
    </aside>
    <a-drawer v-else v-model:open="conversationDrawerOpen" title="对话记录" placement="left" :width="300" :body-style="{ padding: 0 }" @after-open-change="handleConversationDrawerAfterOpenChange">
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
            <a-button type="link" size="small" :loading="confirmingSubmission" @click="retryPendingConfirmation(true)">重新确认</a-button>
          </div>
          <div v-else-if="conversationForbidden" class="submission-confirmation-bar">
            <span>当前会话权限已失效，请重新登录或刷新权限后重试</span>
          </div>
          <div v-else-if="editingTurn" class="turn-editing-bar">
            <span>正在修改最近一轮消息</span>
            <a-button type="link" size="small" :disabled="editingTurn.phase === 'submitting'" @click="cancelTurnEdit">取消编辑</a-button>
          </div>
          <div v-else-if="turnLimitReached" class="turn-limit-bar">
            <span>{{ turnLimitMessage }}</span>
            <a-button type="link" size="small" :loading="creating" @click="createConversation">新建对话</a-button>
          </div>
          <AIComposer
            ref="composer"
            v-model="selectedModel"
            v-model:reasoning-effort="selectedReasoningEffort"
            v-model:service-tier="selectedServiceTier"
            :conversation-id="selectedConversation.id"
            :context-status="contextStatus"
            :disabled="generating || submissionBlocked"
            :stoppable="generating || Boolean(pendingConfirmation)"
            :turn-limit-reached="turnLimitReached && !editingTurn"
            :turn-limit-message="turnLimitMessage"
            :image-input-supported="Boolean(selectedModelOption?.inputModalities.includes('image') && selectedModelOption.supportedApiProtocols.includes('responses'))"
            :model-options="models"
            :models-loading="modelsLoading"
            :show-conversation-button="mobile"
            @open-conversations="conversationDrawerOpen = true"
            @models-open="refreshSelectedModelsIfExpired"
            @submit="handleComposerSubmit"
            @stop="stopGeneration"
          />
        </footer>
      </template>
      <div v-else class="chat-start-state">
        <MessageOutlined />
        <strong>新建对话后开始提问</strong>
        <a-button type="primary" :loading="creating" @click="createConversation"><PlusOutlined />新建对话</a-button>
      </div>
    </main>

    <div v-if="conversationMenu" id="conversation-actions-menu" ref="conversationMenuElement" class="conversation-context-menu" role="menu" :style="{ left: `${conversationMenu.x}px`, top: `${conversationMenu.y}px` }" @click.stop @keydown="handleConversationMenuKeyDown">
      <button type="button" role="menuitem" tabindex="-1" @click="openRenameDialog(conversationMenu.item)">重命名</button>
      <button type="button" role="menuitem" tabindex="-1" @click="togglePinned(conversationMenu.item)">{{ conversationMenu.item.isPinned ? '取消置顶' : '置顶' }}</button>
      <button type="button" role="menuitem" tabindex="-1" @click="openDetails(conversationMenu.item)">详情</button>
      <button type="button" role="menuitem" tabindex="-1" class="is-danger" @click="openDeleteDialog(conversationMenu.item)">删除</button>
    </div>

    <a-modal v-model:open="renameDialogOpen" title="重命名会话" ok-text="保存" cancel-text="取消" :confirm-loading="conversationUpdating" @ok="renameConversation">
      <a-input v-model:value="renameTitle" :maxlength="60" placeholder="输入会话标题" @press-enter="renameConversation" />
    </a-modal>
    <a-modal v-model:open="detailsDialogOpen" title="会话详情" :closable="false">
      <a-descriptions v-if="detailConversation" :column="1" size="small" bordered>
        <a-descriptions-item label="会话 ID">
          <span class="conversation-detail-id">
            <code>{{ detailConversation.id }}</code>
            <a-tooltip title="复制会话 ID">
              <a-button type="text" size="small" aria-label="复制会话 ID" @click="copyTextToClipboard(detailConversation.id, '会话 ID 已复制')">
                <template #icon><CopyOutlined /></template>
              </a-button>
            </a-tooltip>
          </span>
        </a-descriptions-item>
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
import { ArrowDownOutlined, CopyOutlined, MessageOutlined, MoreOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, defineComponent, h, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { chatApi, ChatStreamHttpError } from '@/api/domains/chat'
import { authState } from '@/composables/useAuth'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { ChatContextStatus, ChatConversation, ChatConversationSyncHead, ChatMessage, ChatModelOption, ChatReasoningEffort, ChatServiceTier } from '@/types/domain/chat'
import { beginLatestTurnEdit, isDefinitiveChatHttpRejection, resolveChatReconciliationNotice, resolveChatSubmitFailure } from './chatTurnEditing'
import {
  applyChatReconciliationIfActive,
  reconcileChatPendingSubmissionRecovery,
  reconcileChatSubmission,
  type ChatPendingConversationAvailability,
  type ChatSubmissionReconciliation
} from './chatTurnReconciliation'
import { createChatConversationSummaryRefresher, mergeChatConversationSummary } from './chatConversationSummary'
import { canSubmitChatTurn, chatTurnLimitMessage, isChatTurnLimitReached, markChatConversationTurnLimitReached } from './chatTurnLimit'
import { isCurrentChatConversationLoad, startChatConversationLoad } from './chatConversationLoad'
import { resolveChatStopTarget, stopActiveChatGeneration } from './chatStopGeneration'
import {
  clearChatPendingSubmission,
  readChatPendingSubmission,
  writeChatPendingSubmission,
  type ChatPendingSubmission
} from './chatPendingSubmissionStorage'
import ChatMessageList from './ChatMessageList.vue'
import AIComposer from './composer/AIComposer.vue'
import type { ChatInputBlock } from './composer/chatComposerDocument'
import { defaultChatReasoningEffort, defaultChatServiceTier, normalizeChatModelControls } from './composer/chatModelControls'
import type { JSONContent } from '@tiptap/core'
import { clampChatFloatingMenuPosition, resolveChatVisualViewportBounds } from './chatViewport'
import { chatGenerationRuntime, type RunningTurn } from './chatGenerationRuntime'
import { getDefaultChatLocalCache } from './chatLocalCache'
import { ChatCacheBroadcast } from './chatCacheBroadcast'
import { createDefaultChatConversationSyncDependencies, hasOlderChatMessages, projectChatMessagesWithRuntime, restoreChatActiveTurnFromSync, synchronizeChatConversation } from './chatConversationSync'
import { ChatRuntimeReconciliationScheduler } from './chatRuntimeReconciliation'
import { applyDeletedChatConversation, ChatModelLoadCoordinator, ChatSingleFlightCoordinator } from './chatConversationPerformance'

interface ChatTurnEditingState {
  conversationId: string
  turnId: string
  userMessageId: string
  assistantMessageId: string
  content: string
  contentBlocks: ChatInputBlock[]
  displacedDraft: JSONContent
  phase: 'editing' | 'submitting'
}

interface ChatRequestContext {
  readonly systemAccountId: string
  readonly conversationId: string
  readonly clientMessageId: string
  readonly replaceTurnId?: string
  readonly snapshot: JSONContent
}

type PendingSubmissionConfirmation = ChatPendingSubmission

interface ActiveChatStopTarget {
  readonly request: ChatRequestContext
  turnId?: string
}

const conversations = ref<ChatConversation[]>([])
const conversationCursor = ref<ChatConversation>()
const conversationsLoadingMore = ref(false)
const hasMoreConversations = ref(false)
const selectedConversationId = ref<string>()
const messages = ref<ChatMessage[]>([])
const models = ref<ChatModelOption[]>([])
const selectedModel = ref<string>()
const selectedReasoningEffort = ref<ChatReasoningEffort | ''>('')
const selectedServiceTier = ref<ChatServiceTier | ''>('')
const contextStatus = ref<ChatContextStatus>()
const messagesLoading = ref(false)
const olderMessagesLoading = ref(false)
const hasOlderMessages = ref(false)
const modelsLoading = ref(false)
const creating = ref(false)
const generating = ref(false)
const conversationAccessEpoch = ref(0)
const stopping = ref(false)
const confirmingSubmission = ref(false)
const pendingConfirmation = ref<PendingSubmissionConfirmation>()
const conversationDrawerOpen = ref(false)
const pendingCreateAfterDrawerClose = ref(false)
const mobile = ref(false)
const conversationMenu = ref<{ item: ChatConversation; x: number; y: number }>()
const conversationMenuElement = ref<HTMLElement>()
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
let activeStopTarget: ActiveChatStopTarget | undefined
let pendingConfirmationTimer: number | undefined
let pendingConfirmationRetryCount = 0
let contextStatusTimer: number | undefined
let conversationLoadEpoch = 0
let disposed = false
let pageActive = false
let initialLoaded = false
let runtimeUnsubscribe: (() => void) | undefined
let broadcastUnsubscribe: (() => void) | undefined
let subscribedRuntimeConversationId: string | undefined
let lastRuntimeAcceptedKey: string | undefined
let lastRuntimeTerminalKey: string | undefined
let conversationMenuTrigger: HTMLElement | undefined

const localCache = getDefaultChatLocalCache()
const modelLoadCoordinator = new ChatModelLoadCoordinator<ChatModelOption>({
  load: ({ conversationId }, signal) => chatApi.listModels(conversationId, { signal })
})
const contextStatusCoordinator = new ChatSingleFlightCoordinator<ChatContextStatus>()
const syncDependencies = createDefaultChatConversationSyncDependencies({
  pendingConversationIds: () => pendingConfirmation.value ? new Set([pendingConfirmation.value.request.conversationId]) : new Set()
})
const cacheBroadcast = new ChatCacheBroadcast()
const runtimeReconciliationScheduler = new ChatRuntimeReconciliationScheduler()

const selectedConversation = computed(() => conversations.value.find((item) => item.id === selectedConversationId.value))
const conversationForbidden = computed(() => {
  conversationAccessEpoch.value
  return Boolean(selectedConversation.value && chatGenerationRuntime.isConversationBlocked(selectedConversation.value.systemAccountId, selectedConversation.value.id))
})
const turnLimitReached = computed(() => Boolean(selectedConversation.value && isChatTurnLimitReached(selectedConversation.value.userTurnCount, selectedConversation.value.userTurnLimit)))
const turnLimitMessage = computed(() => chatTurnLimitMessage(selectedConversation.value?.userTurnLimit ?? 0))
const selectedModelOption = computed(() => models.value.find((item) => item.id === selectedModel.value))
const editableUserMessageId = computed(() => {
  const candidate = messages.value[messages.value.length - 2]
  return candidate && beginLatestTurnEdit(messages.value, candidate.id)?.userMessageId
})
const submissionBlocked = computed(() => stopping.value || confirmingSubmission.value || Boolean(pendingConfirmation.value) || conversationForbidden.value)
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
      h('div', { class: 'conversation-pane-toolbar' }, [h('strong', '对话'), h('button', { class: 'conversation-new-button', type: 'button', disabled: creating.value, onClick: createConversationFromPane }, [h(PlusOutlined), ' 新建'])]),
      conversations.value.length
        ? h('div', { class: 'conversation-list' }, [
            ...conversations.value.map((item) => h('div', {
              class: ['conversation-item', { active: item.id === selectedConversationId.value }],
              onContextmenu: (event: MouseEvent) => openConversationMenu(event, item)
            }, [
              h('button', {
                class: 'conversation-item-select',
                type: 'button',
                title: item.title,
                onClick: async () => { if (await selectConversation(item.id)) emit('selected') }
              }, item.title),
              mobile.value ? h('button', {
                class: 'conversation-more-button',
                type: 'button',
                title: '更多操作',
                'aria-label': `更多会话操作：${item.title}`,
                'aria-haspopup': 'menu',
                'aria-controls': 'conversation-actions-menu',
                'aria-expanded': conversationMenu.value?.item.id === item.id,
                onClick: (event: MouseEvent) => openConversationMenuFromButton(event, item)
              }, [h(MoreOutlined)]) : undefined
            ])),
            ...(hasMoreConversations.value ? [h('button', { class: 'conversation-load-more', type: 'button', disabled: conversationsLoadingMore.value, onClick: () => { void loadMoreConversations() } }, conversationsLoadingMore.value ? '正在加载' : '加载更多')] : [])
          ])
        : h('div', { class: 'conversation-list-empty' }, '暂无对话')
    ])
  }
})

async function loadInitial(): Promise<void> {
  try {
    const conversationItems = await chatApi.listConversations({ limit: 50 })
    conversations.value = conversationItems
    conversationCursor.value = conversationItems.at(-1)
    hasMoreConversations.value = conversationItems.length === 50
    const storedPending = readStoredPendingConfirmation()
    if (storedPending) {
      restorePendingConfirmation(storedPending)
      const availability = await ensurePendingConversationAvailability(storedPending)
      if (availability === 'not_found') {
        clearPendingConfirmation(storedPending.request.systemAccountId)
        message.warning('原会话已不可用，未确认草稿无法恢复')
      } else if (!disposed) {
        if (availability === 'ready') message.info('正在继续确认上一条消息的提交状态')
        else message.error('暂时无法加载待确认会话，将继续后台重试')
        schedulePendingConfirmation()
      }
    } else if (conversationItems[0]) {
      await selectConversation(conversationItems[0].id)
    }
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
async function selectConversation(id: string, options: {
  allowPendingRecovery?: boolean
  forceReload?: boolean
  refreshModels?: boolean
  silentLoadError?: boolean
} = {}): Promise<boolean> {
  if (selectedConversationId.value === id && !options.forceReload) return true
  const previousConversation = selectedConversation.value
  const nextConversation = conversations.value.find((item) => item.id === id)
  const previousModelCacheKey = previousConversation ? previousConversation.apiKeyId ?? previousConversation.id : undefined
  const nextModelCacheKey = nextConversation ? nextConversation.apiKeyId ?? nextConversation.id : undefined
  if (previousModelCacheKey !== nextModelCacheKey) modelLoadCoordinator.cancel(previousModelCacheKey)
  await cancelTurnEdit()
  const loadEpoch = ++conversationLoadEpoch
  selectedConversationId.value = id
  messages.value = []
  models.value = []
  selectedModel.value = undefined
  contextStatus.value = undefined
  hasOlderMessages.value = false
  olderMessagesLoading.value = false
  showJumpToBottom.value = false
  messagesLoading.value = true
  modelsLoading.value = true
  try {
    subscribeSelectedRuntime()
    const conversation = conversations.value.find((item) => item.id === id)
    if (!conversation) throw new Error('会话不存在')
    if (authState.currentUser.value?.id !== conversation.systemAccountId) return false
    let loadedSyncHead: ChatConversationSyncHead | undefined
    const modelRequest = { apiKeyId: conversation.apiKeyId ?? conversation.id, conversationId: id }
    const cachedModels = modelLoadCoordinator.peek(modelRequest.apiKeyId)
    const conversationLoad = startChatConversationLoad({
      loadMessages: async () => {
        const blockedBeforeSync = chatGenerationRuntime.isConversationBlocked(conversation.systemAccountId, id)
        const cached = blockedBeforeSync ? undefined : await localCache.readConversation(conversation.systemAccountId, id)
        if (cached?.ok && cached.value?.messages.length) {
          if (isCurrentConversationAccount(conversation.systemAccountId, id, loadEpoch)) {
            messages.value = cached.value.messages
            hasOlderMessages.value = hasOlderChatMessages(cached.value.messages)
            messagesLoading.value = false
            await nextTick()
            messageList.value?.scrollToBottom()
          }
        }
        const synchronized = await synchronizeChatConversation({
          systemAccountId: conversation.systemAccountId,
          conversationId: id,
          dependencies: syncDependencies,
          projectMessages: (items, syncHead) => projectRuntimeMessages(conversation.systemAccountId, id, items, syncHead)
        })
        if (!isCurrentConversationAccount(conversation.systemAccountId, id, loadEpoch)) return []
        if (synchronized.state === 'not_found') {
          conversations.value = conversations.value.filter((item) => item.id !== id)
          return []
        }
        if (synchronized.state === 'forbidden') {
          chatGenerationRuntime.blockConversation(conversation.systemAccountId, id)
          conversationAccessEpoch.value += 1
          models.value = []
          selectedModel.value = undefined
          return []
        }
        if (synchronized.state === 'superseded') return messages.value
        loadedSyncHead = synchronized.syncHead
        chatGenerationRuntime.allowConversation(conversation.systemAccountId, id)
        conversationAccessEpoch.value += 1
        replaceConversation({ ...conversation, messageRevision: synchronized.messageRevision, activeTurnId: synchronized.syncHead.activeTurn?.turnId })
        cacheBroadcast.publish({ systemAccountId: conversation.systemAccountId, conversationId: id, messageRevision: synchronized.messageRevision })
        if (synchronized.syncHead.activeTurn) {
          const active = synchronized.syncHead.activeTurn
          const user = synchronized.messages.find((item) => item.turnId === active.turnId && item.role === 'user')
          const pending = pendingConfirmation.value
          if (pending?.request.conversationId === id && (!user?.clientMessageId || pending.request.clientMessageId === user.clientMessageId)) {
            activeStopTarget = { request: pending.request, turnId: active.turnId }
          }
          if (!isCurrentConversationAccount(conversation.systemAccountId, id, loadEpoch)) return synchronized.messages
          attachActiveTurnFromSync(conversation, synchronized.syncHead, synchronized.messages, user?.clientMessageId, synchronized.projectionEventVersion)
        }
        return synchronized.messages
      },
      loadModels: () => (options.refreshModels
        ? modelLoadCoordinator.refresh(modelRequest)
        : cachedModels
          ? Promise.resolve(cachedModels)
          : modelLoadCoordinator.load(modelRequest)).then((items) => [...items])
    })
    const messageItems = await conversationLoad.messages
    if (!isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) return false
    messages.value = messageItems
    if (loadedSyncHead) messages.value = projectRuntimeMessages(conversation.systemAccountId, id, messageItems, loadedSyncHead).messages
    hasOlderMessages.value = hasOlderChatMessages(messageItems)
    void refreshContextStatus(id)
    void conversationLoad.models.then((modelResult) => {
      if (!isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) return
      if (modelResult.ok) {
        if (chatGenerationRuntime.isConversationBlocked(conversation.systemAccountId, id)) {
          models.value = []
          selectedModel.value = undefined
          modelsLoading.value = false
          return
        }
        models.value = modelResult.value
        const latestConversation = conversations.value.find((item) => item.id === id)
        selectedModel.value = latestConversation?.lastModel && modelResult.value.some((item) => item.id === latestConversation.lastModel)
          ? latestConversation.lastModel
          : modelResult.value[0]?.id
      } else if (!options.silentLoadError) {
        message.error(extractApiErrorMessage(modelResult.error, '加载可用模型失败'))
      }
      modelsLoading.value = false
    }).catch(() => {
      if (isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) modelsLoading.value = false
    })
    return true
  } catch (error) {
    if (isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) {
      if (!options.silentLoadError) message.error(extractApiErrorMessage(error, '加载对话失败'))
      modelsLoading.value = false
    }
    return false
  } finally {
    if (isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) {
      messagesLoading.value = false
      await nextTick()
      if (isCurrentChatConversationLoad({ conversationId: id, selectedConversationId: selectedConversationId.value, epoch: loadEpoch, currentEpoch: conversationLoadEpoch, disposed })) {
        messageList.value?.scrollToBottom()
      }
    }
  }
}
async function ensurePendingConversationAvailability(pending: PendingSubmissionConfirmation): Promise<ChatPendingConversationAvailability> {
  let conversation = conversations.value.find((item) => item.id === pending.request.conversationId)
  if (!conversation) {
    try {
      const loaded = await chatApi.getConversation(pending.request.conversationId)
      if (loaded.systemAccountId !== pending.request.systemAccountId) return 'retry'
      conversation = loaded
      conversations.value.push(loaded)
      sortConversations()
    } catch (error) {
      return isNotFoundResponse(error) ? 'not_found' : 'retry'
    }
  }
  const selected = await selectConversation(conversation.id, {
    allowPendingRecovery: true,
    forceReload: true,
    silentLoadError: true
  })
  return selected ? 'ready' : 'retry'
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
    hasOlderMessages.value = hasOlderChatMessages(messages.value, older.length)
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
function createConversationFromPane(): void {
  if (mobile.value && conversationDrawerOpen.value) {
    pendingCreateAfterDrawerClose.value = true
    conversationDrawerOpen.value = false
    return
  }
  pendingCreateAfterDrawerClose.value = false
  void createConversation()
}
function handleConversationDrawerAfterOpenChange(open: boolean): void {
  if (open || !pendingCreateAfterDrawerClose.value) return
  pendingCreateAfterDrawerClose.value = false
  void createConversation()
}
async function createConversation(): Promise<void> {
  if (creating.value) return
  const selectionEpochAtStart = conversationLoadEpoch
  const selectedConversationIdAtStart = selectedConversationId.value
  creating.value = true
  try {
    const item = await chatApi.createConversation()
    conversations.value.unshift(item)
    if (
      pageActive
      && conversationLoadEpoch === selectionEpochAtStart
      && selectedConversationId.value === selectedConversationIdAtStart
      && await selectConversation(item.id, { refreshModels: true })
    ) conversationDrawerOpen.value = false
  }
  catch (error) { message.error(extractApiErrorMessage(error, '创建对话失败')) }
  finally { creating.value = false }
}
async function sendMessage(content: string, snapshot: JSONContent, blocks: ChatInputBlock[]): Promise<void> {
  const conversation = selectedConversation.value
  const model = selectedModel.value
  if (conversation && chatGenerationRuntime.isConversationBlocked(conversation.systemAccountId, conversation.id)) {
    composer.value?.restore(snapshot)
    message.error('当前会话权限已失效，请重新登录或刷新权限后重试')
    return
  }
  if (!conversation || !content.trim() || !model || generating.value || submissionBlocked.value) return
  const activeEdit = editingTurn.value?.conversationId === conversation.id ? editingTurn.value : undefined
  if (!canSubmitChatTurn({
    userTurnCount: conversation.userTurnCount,
    userTurnLimit: conversation.userTurnLimit,
    replaceTurnId: activeEdit?.turnId
  })) {
    composer.value?.restore(snapshot)
    message.warning(chatTurnLimitMessage(conversation.userTurnLimit))
    return
  }
  const requestContext: ChatRequestContext = Object.freeze({
    systemAccountId: conversation.systemAccountId,
    conversationId: conversation.id,
    clientMessageId: crypto.randomUUID(),
    replaceTurnId: activeEdit?.turnId,
    snapshot: cloneDocument(snapshot)
  })
  if (!writeStoredPendingConfirmation({
    request: requestContext,
    streamStarted: false,
    silent: false,
    errorMessage: '发送连接中断，正在确认消息状态'
  })) {
    composer.value?.restore(requestContext.snapshot)
    message.error('浏览器无法保存消息提交状态，本次消息未发送，请检查浏览器存储权限或空间后重试')
    return
  }
  if (activeEdit) activeEdit.phase = 'submitting'
  generating.value = true
  messageList.value?.scrollToBottom()
  const stopTarget: ActiveChatStopTarget = { request: requestContext }
  activeStopTarget = stopTarget
  subscribeSelectedRuntime()
  try {
    chatGenerationRuntime.start({
      systemAccountId: requestContext.systemAccountId,
      conversationId: requestContext.conversationId,
      clientMessageId: requestContext.clientMessageId,
      replaceTurnId: requestContext.replaceTurnId,
      content,
      contentBlocks: blocks.map((block) => block.type === 'input_image' ? { type: block.type, assetId: block.assetId } : { type: block.type, text: block.text }),
      model,
      reasoningEffort: selectedReasoningEffort.value || undefined,
      serviceTier: selectedServiceTier.value || undefined
    })
  } catch (error) {
    generating.value = false
    activeStopTarget = undefined
    clearPendingConfirmation(requestContext.systemAccountId)
    const currentEdit = editingTurn.value
    if (currentEdit && currentEdit.turnId === requestContext.replaceTurnId) currentEdit.phase = 'editing'
    composer.value?.restore(requestContext.snapshot)
    const runtimeUnavailable = error instanceof Error && /runtime (?:conversation blocked|account inactive)/i.test(error.message)
    message.error(runtimeUnavailable ? '当前会话权限或登录状态已变化，请重新进入会话后重试' : extractApiErrorMessage(error, '发送失败，请稍后重试'))
  }
}
async function stopGeneration(): Promise<void> {
  const id = selectedConversationId.value
  const active = activeStopTarget
  const pending = pendingConfirmation.value
  const target = resolveChatStopTarget({
    selectedConversationId: id,
    active: active ? {
      conversationId: active.request.conversationId,
      clientMessageId: active.request.clientMessageId,
      turnId: active.turnId
    } : undefined,
    pending: pending ? {
      conversationId: pending.request.conversationId,
      clientMessageId: pending.request.clientMessageId,
      turnId: pending.startedTurnId
    } : undefined
  })
  if (!id || !target || stopping.value) return
  stopping.value = true
  try {
    const conversation = selectedConversation.value
    const stoppedByRuntime = conversation && target.turnId
      ? await chatGenerationRuntime.stop(conversation.systemAccountId, id, { clientMessageId: target.clientMessageId, turnId: target.turnId })
      : false
    if (!stoppedByRuntime) await stopActiveChatGeneration({ stop: () => chatApi.stop(id, { clientMessageId: target.clientMessageId, turnId: target.turnId }) })
    if (pendingConfirmation.value?.request.clientMessageId === target.clientMessageId) await retryPendingConfirmation(true)
  } finally {
    stopping.value = false
  }
}
async function refreshMessages(conversationId = selectedConversationId.value): Promise<ChatMessage[]> {
  if (!conversationId) return []
  const latest = await chatApi.listMessages(conversationId, { limit: 100 })
  if (!disposed && selectedConversationId.value === conversationId) {
    messages.value = latest
    hasOlderMessages.value = hasOlderChatMessages(latest)
  }
  return latest
}
function subscribeSelectedRuntime(): void {
  runtimeUnsubscribe?.()
  runtimeUnsubscribe = undefined
  subscribedRuntimeConversationId = undefined
  if (!pageActive) return
  const conversation = selectedConversation.value
  if (!conversation || authState.currentUser.value?.id !== conversation.systemAccountId) {
    generating.value = false
    return
  }
  subscribedRuntimeConversationId = conversation.id
  runtimeUnsubscribe = chatGenerationRuntime.subscribe(conversation.systemAccountId, conversation.id, applyRuntimeTurn)
}
function applyRuntimeTurn(turn: RunningTurn | undefined): void {
  const conversation = selectedConversation.value
  if (!conversation || authState.currentUser.value?.id !== conversation.systemAccountId || subscribedRuntimeConversationId !== conversation.id) return
  generating.value = turn?.status === 'preparing' || turn?.status === 'running'
  if (!turn) {
    activeStopTarget = undefined
    runtimeReconciliationScheduler.clearConversation(conversation.systemAccountId, conversation.id)
    return
  }
  if (!turn.reconciliationReason) runtimeReconciliationScheduler.clear(turn)
  let active = activeStopTarget
  if (turn.turnId && turn.clientMessageId && (!active || active.request.conversationId !== turn.conversationId || active.request.clientMessageId !== turn.clientMessageId)) {
    active = {
      request: {
        systemAccountId: turn.systemAccountId,
        conversationId: turn.conversationId,
        clientMessageId: turn.clientMessageId,
        snapshot: { type: 'doc', content: [] }
      },
      turnId: turn.turnId
    }
    activeStopTarget = active
  }
  if (turn.turnId && active?.request.clientMessageId === turn.clientMessageId) {
    active.turnId = turn.turnId
    const acceptedKey = `${turn.conversationId}:${turn.turnId}`
    if (lastRuntimeAcceptedKey !== acceptedKey) {
      lastRuntimeAcceptedKey = acceptedKey
      finishAcceptedTurnEdit(active.request)
      composer.value?.releaseSubmittedAssets()
      clearPendingConfirmation(active.request.systemAccountId)
      void refreshConversationFromSync(turn.conversationId)
      void refreshConversationSummary(turn.conversationId).catch(() => undefined)
    }
  }
  const projection = turn.projection as ChatMessage
  if (projection.id && projection.sequenceNo > 0) {
    const index = messages.value.findIndex((item) => item.id === projection.id)
    if (index >= 0) messages.value[index] = cloneMessage(projection)
    else messages.value.push(cloneMessage(projection))
    messages.value.sort((left, right) => left.sequenceNo - right.sequenceNo)
  }
  if (turn.status === 'completed' || turn.status === 'failed' || turn.status === 'canceled' || turn.reconciliationReason) {
    const terminalKey = `${turn.conversationId}:${turn.turnId ?? turn.clientMessageId}:${turn.status}:${turn.reconciliationReason ?? ''}`
    if (lastRuntimeTerminalKey !== terminalKey) {
      lastRuntimeTerminalKey = terminalKey
      if (!turn.reconciliationReason) void refreshConversationFromSync(turn.conversationId)
      else requestRuntimeReconciliationSync(turn)
      void refreshContextStatus(turn.conversationId)
      if (turn.status === 'failed') message.error(turn.error?.message || '模型生成失败')
      if (turn.status !== 'running' && turn.status !== 'preparing') activeStopTarget = undefined
    }
  }
  if (turn.status === 'failed' && !turn.turnId && active?.request.clientMessageId === turn.clientMessageId) {
    if (turn.error?.status === 403) {
      chatGenerationRuntime.blockConversation(turn.systemAccountId, turn.conversationId)
      conversationAccessEpoch.value += 1
      messages.value = []
      models.value = []
      selectedModel.value = undefined
    }
    const failure = turn.error?.status
      ? new ChatStreamHttpError(turn.error.status, turn.error.code, turn.error.message || '发送失败')
      : new Error(turn.error?.message || '发送失败')
    const failedRequest = active.request
    void (async () => {
      try {
        await handleSubmitFailure(failure, failedRequest, false, undefined, false)
      } finally {
        generating.value = false
        activeStopTarget = undefined
        clearPendingConfirmation(failedRequest.systemAccountId)
        const currentEdit = editingTurn.value
        if (currentEdit && currentEdit.turnId === failedRequest.replaceTurnId) currentEdit.phase = 'editing'
        composer.value?.restore(failedRequest.snapshot)
      }
    })()
  }
}
async function refreshConversationFromSync(conversationId: string): Promise<void> {
  const conversation = conversations.value.find((item) => item.id === conversationId)
  if (!conversation || authState.currentUser.value?.id !== conversation.systemAccountId) return
  try {
    const result = await synchronizeChatConversation({
      systemAccountId: conversation.systemAccountId,
      conversationId,
      dependencies: syncDependencies,
      projectMessages: (items, syncHead) => projectRuntimeMessages(conversation.systemAccountId, conversationId, items, syncHead)
    })
    if (authState.currentUser.value?.id !== conversation.systemAccountId || !conversations.value.some((item) => item.id === conversationId && item.systemAccountId === conversation.systemAccountId)) return
    if (result.state === 'not_found') {
      conversations.value = conversations.value.filter((item) => item.id !== conversationId)
      if (selectedConversationId.value === conversationId) messages.value = []
      return
    }
    if (result.state === 'forbidden') {
      chatGenerationRuntime.blockConversation(conversation.systemAccountId, conversationId)
      conversationAccessEpoch.value += 1
      if (selectedConversationId.value === conversationId) {
        messages.value = []
        models.value = []
        selectedModel.value = undefined
      }
      return
    }
    if (result.state === 'superseded' || result.messageRevision < conversation.messageRevision) return
    chatGenerationRuntime.allowConversation(conversation.systemAccountId, conversationId)
    conversationAccessEpoch.value += 1
    replaceConversation({ ...conversation, messageRevision: result.messageRevision, activeTurnId: result.syncHead.activeTurn?.turnId })
    if (!disposed && selectedConversationId.value === conversationId) {
      messages.value = projectRuntimeMessages(conversation.systemAccountId, conversationId, result.messages, result.syncHead).messages
      hasOlderMessages.value = hasOlderChatMessages(result.messages)
    }
    if (result.syncHead.activeTurn) {
      const active = result.syncHead.activeTurn
      const user = result.messages.find((item) => item.turnId === active.turnId && item.role === 'user')
      attachActiveTurnFromSync(conversation, result.syncHead, result.messages, user?.clientMessageId, result.projectionEventVersion)
    } else {
      const running = chatGenerationRuntime.get(conversation.systemAccountId, conversationId)
      if (running?.turnId) chatGenerationRuntime.forget(conversation.systemAccountId, conversationId, running.turnId)
      if (selectedConversationId.value === conversationId) generating.value = false
    }
    cacheBroadcast.publish({ systemAccountId: conversation.systemAccountId, conversationId, messageRevision: result.messageRevision })
  } catch (error) {
    if (!disposed && selectedConversationId.value === conversationId) console.error('[chat-sync]', error)
  }
}
function cloneMessage(value: ChatMessage): ChatMessage {
  return JSON.parse(JSON.stringify(value)) as ChatMessage
}
function requestRuntimeReconciliationSync(turn: RunningTurn): void {
  if (!runtimeReconciliationScheduler.begin(turn)) return
  void refreshConversationFromSync(turn.conversationId).finally(() => {
    runtimeReconciliationScheduler.complete(turn, chatGenerationRuntime.get(turn.systemAccountId, turn.conversationId))
  })
}
function isCurrentConversationAccount(systemAccountId: string, conversationId: string, epoch: number): boolean {
  return authState.currentUser.value?.id === systemAccountId
    && isCurrentChatConversationLoad({ conversationId, selectedConversationId: selectedConversationId.value, epoch, currentEpoch: conversationLoadEpoch, disposed })
}
function projectRuntimeMessages(
  systemAccountId: string,
  conversationId: string,
  values: readonly ChatMessage[],
  syncHead: { activeTurn?: { turnId: string; assistantMessageId: string } }
): { messages: ChatMessage[]; eventVersion?: number; status?: ChatMessage['status']; turnId?: string; assistantMessageId?: string } {
  const running = chatGenerationRuntime.get(systemAccountId, conversationId)
  return {
    messages: projectChatMessagesWithRuntime({ messages: values, activeTurn: syncHead.activeTurn, runtimeTurn: running }),
    eventVersion: running?.eventVersion,
    status: running?.projection.status,
    turnId: running?.turnId,
    assistantMessageId: running?.assistantMessageId
  }
}
function attachActiveTurnFromSync(
  conversation: ChatConversation,
  syncHead: ChatConversationSyncHead,
  synchronizedMessages: readonly ChatMessage[],
  clientMessageId?: string,
  projectionEventVersion?: number
): void {
  const active = syncHead.activeTurn
  if (!active) return
  const runtimeBeforeAttach = chatGenerationRuntime.get(conversation.systemAccountId, conversation.id)
  if (runtimeBeforeAttach?.turnId === active.turnId && (runtimeBeforeAttach.status === 'completed' || runtimeBeforeAttach.status === 'failed' || runtimeBeforeAttach.status === 'canceled')) return
  restoreChatActiveTurnFromSync({
    systemAccountId: conversation.systemAccountId,
    syncHead,
    messages: synchronizedMessages,
    clientMessageId,
    projectionEventVersion,
    attach: (input) => chatGenerationRuntime.attach(input)
  })
}
async function removeConversation(id: string): Promise<void> {
  const conversation = conversations.value.find((item) => item.id === id)
  try {
    await chatApi.deleteConversation(id)
  } catch (error) {
    if (!isNotFoundResponse(error)) {
      message.error(extractApiErrorMessage(error, '删除对话失败'))
      return
    }
  }

  const deleted = applyDeletedChatConversation({
    conversations: conversations.value,
    selectedConversationId: selectedConversationId.value,
    deletedConversationId: id
  })
  conversations.value = deleted.conversations
  selectedConversationId.value = deleted.selectedConversationId
  if (!deleted.selectedConversationId) {
    messages.value = []
    models.value = []
    selectedModel.value = undefined
    contextStatus.value = undefined
  }
  if (conversation) void localCache.deleteConversation(conversation.systemAccountId, id).catch(() => undefined)
  if (deleted.nextConversationId) void selectConversation(deleted.nextConversationId)
}
async function refreshSelectedModelsIfExpired(): Promise<void> {
  const conversation = selectedConversation.value
  if (!conversation) return
  const request = { apiKeyId: conversation.apiKeyId ?? conversation.id, conversationId: conversation.id }
  modelsLoading.value = true
  try {
    const items = [...await modelLoadCoordinator.refreshIfExpired(request)]
    if (selectedConversationId.value !== conversation.id) return
    models.value = items
    if (!selectedModel.value || !items.some((item) => item.id === selectedModel.value)) {
      selectedModel.value = conversation.lastModel && items.some((item) => item.id === conversation.lastModel)
        ? conversation.lastModel
        : items[0]?.id
    }
    normalizeCurrentModelControls()
  } catch (error) {
    if (selectedConversationId.value === conversation.id) message.error(extractApiErrorMessage(error, '刷新可用模型失败'))
  } finally {
    if (selectedConversationId.value === conversation.id) modelsLoading.value = false
  }
}
function handleComposerSubmit(payload: { blocks: ChatInputBlock[]; snapshot: JSONContent }): void {
  if (generating.value || submissionBlocked.value) { composer.value?.restore(payload.snapshot); return }
  if (!selectedConversation.value || !selectedModel.value || modelsLoading.value) {
    composer.value?.restore(payload.snapshot)
    message.warning(modelsLoading.value ? '模型仍在加载，请稍后发送' : '当前没有可用模型')
    return
  }
  const content = payload.blocks.map((item) => item.type === 'input_image' ? '[图片]' : item.text).join('\n')
  void sendMessage(content, payload.snapshot, payload.blocks)
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
  editor.setBlocks(candidate.contentBlocks)
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
  const turnLimitExceeded = error instanceof ChatStreamHttpError && error.code === 'chat_turn_limit_exceeded'
  const errorMessage = extractApiErrorMessage(error, '发送失败')
  if (replaceConflict || isDefinitiveChatRejection(error)) {
    if (replaceConflict) { try { await refreshMessages(request.conversationId) } catch {} }
    await applySubmissionOutcome({
      request,
      streamStarted,
      silent,
      errorMessage,
      replaceConflict,
      reconciliation: { messages: messages.value, confirmed: true, accepted: false, terminal: false }
    })
    if (turnLimitExceeded && !disposed) {
      const conversation = conversations.value.find((item) => item.id === request.conversationId)
      if (conversation) replaceConversation(markChatConversationTurnLimitReached(conversation))
      void refreshConversationSummary(request.conversationId).catch(() => undefined)
    }
    if (!disposed) clearPendingConfirmation(request.systemAccountId)
    return
  }
  await applyChatReconciliationIfActive({
    reconcile: () => reconcileChatSubmission({
      initialAcceptedTurnId: streamStarted ? startedTurnId : undefined,
      initialAssistantStatus: streamStarted && startedTurnId ? 'streaming' : undefined,
      getSubmissionStatus: () => chatApi.getSubmissionStatus(request.conversationId, request.clientMessageId),
      listMessages: () => refreshMessages(request.conversationId),
      stop: async (turnId) => { await chatApi.stop(request.conversationId, { clientMessageId: request.clientMessageId, turnId }) }
    }),
    isDisposed: () => disposed,
    apply: async (reconciliation) => {
      if (!reconciliation.confirmed || (reconciliation.accepted && !reconciliation.terminal)) {
        if (reconciliation.accepted) {
          finishAcceptedTurnEdit(request)
          composer.value?.releaseSubmittedAssets()
        }
        enterPendingConfirmation({
          request,
          streamStarted: streamStarted || reconciliation.accepted,
          startedTurnId: reconciliation.turnId ?? startedTurnId,
          acceptedAssistantStatus: reconciliation.assistantStatus ?? (streamStarted && startedTurnId ? 'streaming' : undefined),
          silent,
          errorMessage
        })
        return
      }
      await applySubmissionOutcome({
        request,
        streamStarted,
        silent,
        errorMessage: reconciliation.lookupError ? extractApiErrorMessage(reconciliation.lookupError, errorMessage) : errorMessage,
        replaceConflict,
        reconciliation
      })
      if (!disposed) clearPendingConfirmation(request.systemAccountId)
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
  else if (accepted) composer.value?.releaseSubmittedAssets()
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
  setPendingConfirmation(input)
  pendingConfirmationRetryCount = 0
  message.warning('暂时无法确认消息是否已提交，已暂停新的发送并将在后台重试')
  schedulePendingConfirmation()
}
function schedulePendingConfirmation(): void {
  if (pendingConfirmationTimer !== undefined) window.clearTimeout(pendingConfirmationTimer)
  if (disposed || !pendingConfirmation.value) return
  const delay = Math.min(15_000, 1_200 * 2 ** Math.min(pendingConfirmationRetryCount, 4))
  pendingConfirmationTimer = window.setTimeout(() => { pendingConfirmationTimer = undefined; void retryPendingConfirmation() }, delay)
}
async function retryPendingConfirmation(manual = false): Promise<void> {
  const pending = pendingConfirmation.value
  if (disposed || !pending || confirmingSubmission.value) return
  if (pendingConfirmationTimer !== undefined) { window.clearTimeout(pendingConfirmationTimer); pendingConfirmationTimer = undefined }
  if (manual) pendingConfirmationRetryCount = 0
  confirmingSubmission.value = true
  try {
    const recovery = await reconcileChatPendingSubmissionRecovery({
      pending,
      ensureConversation: () => ensurePendingConversationAvailability(pending),
      reconcile: (initial) => reconcileChatSubmission({
        ...initial,
        getSubmissionStatus: () => chatApi.getSubmissionStatus(pending.request.conversationId, pending.request.clientMessageId),
        listMessages: () => refreshMessages(pending.request.conversationId),
        stop: async (turnId) => { await chatApi.stop(pending.request.conversationId, { clientMessageId: pending.request.clientMessageId, turnId }) },
        maxAttempts: 4
      })
    })
    if (disposed || pendingConfirmation.value !== pending) return
    if (recovery.action === 'missing') {
      clearPendingConfirmation(pending.request.systemAccountId)
      message.warning('原会话已不可用，未确认草稿无法恢复')
      return
    }
    if (recovery.action === 'retry') {
      pendingConfirmationRetryCount += 1
      setPendingConfirmation(recovery.pending)
      schedulePendingConfirmation()
      return
    }
    const reconciliation = recovery.reconciliation
    if (!reconciliation) throw new Error('待确认恢复缺少提交状态')
    await applySubmissionOutcome({
      request: pending.request,
      streamStarted: recovery.pending.streamStarted,
      silent: pending.silent,
      errorMessage: reconciliation.lookupError ? extractApiErrorMessage(reconciliation.lookupError, pending.errorMessage) : pending.errorMessage,
      replaceConflict: false,
      reconciliation
    })
    if (!disposed && pendingConfirmation.value === pending) clearPendingConfirmation(pending.request.systemAccountId)
  } catch (error) {
    if (!disposed && pendingConfirmation.value === pending) {
      pendingConfirmationRetryCount += 1
      schedulePendingConfirmation()
      if (manual) message.error(extractApiErrorMessage(error, '确认消息状态失败，将继续后台重试'))
    }
  } finally {
    if (!disposed) confirmingSubmission.value = false
  }
}
function isDefinitiveChatRejection(error: unknown): boolean {
  if (!(error instanceof ChatStreamHttpError)) return false
  return isDefinitiveChatHttpRejection({ status: error.status, code: error.code })
}
function isNotFoundResponse(error: unknown): boolean {
  const candidate = error as { status?: unknown; response?: { status?: unknown } } | undefined
  return (candidate?.response?.status ?? candidate?.status) === 404
}
function setPendingConfirmation(input: PendingSubmissionConfirmation): void {
  const next = Object.freeze(input)
  pendingConfirmation.value = next
  if (!writeStoredPendingConfirmation(next)) message.warning('浏览器暂时无法更新待确认状态，当前页面仍会继续确认')
}
function restorePendingConfirmation(input: PendingSubmissionConfirmation): void {
  pendingConfirmation.value = Object.freeze(input)
}
function writeStoredPendingConfirmation(input: PendingSubmissionConfirmation): boolean {
  try {
    return writeChatPendingSubmission(window.sessionStorage, input)
  } catch {
    return false
  }
}
function clearPendingConfirmation(systemAccountId = pendingConfirmation.value?.request.systemAccountId ?? authState.currentUser.value?.id): void {
  pendingConfirmation.value = undefined
  pendingConfirmationRetryCount = 0
  if (systemAccountId) {
    try { clearChatPendingSubmission(window.sessionStorage, systemAccountId) } catch {}
  }
}
function readStoredPendingConfirmation(): PendingSubmissionConfirmation | undefined {
  const systemAccountId = authState.currentUser.value?.id
  if (!systemAccountId) return undefined
  let stored: ChatPendingSubmission | undefined
  try { stored = readChatPendingSubmission(window.sessionStorage, systemAccountId) } catch { return undefined }
  return stored ? Object.freeze({ ...stored, request: Object.freeze(stored.request) }) : undefined
}
function cloneDocument(document: JSONContent): JSONContent { return JSON.parse(JSON.stringify(document)) as JSONContent }
async function refreshContextStatus(conversationId = selectedConversationId.value): Promise<void> {
  if (!conversationId) return
  try {
    const next = await contextStatusCoordinator.load(conversationId, () => chatApi.getContextStatus(conversationId))
    if (!disposed && selectedConversationId.value === conversationId) contextStatus.value = next
  } catch {
    if (!disposed && selectedConversationId.value === conversationId) contextStatus.value = undefined
  }
}
function updateMobile(): void { mobile.value = window.innerWidth <= 820 }
function currentVisualViewportBounds() {
  return resolveChatVisualViewportBounds({
    offsetLeft: window.visualViewport?.offsetLeft,
    offsetTop: window.visualViewport?.offsetTop,
    width: window.visualViewport?.width,
    height: window.visualViewport?.height,
    innerWidth: window.innerWidth,
    innerHeight: window.innerHeight
  })
}
function showConversationMenu(item: ChatConversation, preferredX: number, preferredY: number, trigger: HTMLElement): void {
  const position = clampChatFloatingMenuPosition({
    preferredX,
    preferredY,
    menuWidth: 136,
    menuHeight: 188,
    viewport: currentVisualViewportBounds(),
    padding: 8
  })
  conversationMenuTrigger = trigger
  conversationMenu.value = { item, ...position }
  void nextTick(focusConversationMenu)
}
function focusConversationMenu(): void {
  conversationMenuElement.value?.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
}
function openConversationMenu(event: MouseEvent, item: ChatConversation): void {
  event.preventDefault()
  const row = event.currentTarget as HTMLElement
  const trigger = row.querySelector<HTMLElement>('.conversation-item-select') ?? row
  showConversationMenu(item, event.clientX, event.clientY, trigger)
}
function openConversationMenuFromButton(event: MouseEvent, item: ChatConversation): void {
  event.preventDefault()
  event.stopPropagation()
  const bounds = (event.currentTarget as HTMLElement).getBoundingClientRect()
  showConversationMenu(item, bounds.right - 136, bounds.bottom + 4, event.currentTarget as HTMLElement)
}
function handleConversationMenuKeyDown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    closeConversationMenu(true)
    return
  }
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
  const items = [...(conversationMenuElement.value?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])]
  if (!items.length) return
  event.preventDefault()
  const currentIndex = Math.max(0, items.indexOf(document.activeElement as HTMLElement))
  const offset = event.key === 'ArrowDown' ? 1 : -1
  items[(currentIndex + offset + items.length) % items.length]?.focus()
}
function closeConversationMenu(restoreFocus = false): void {
  const trigger = conversationMenuTrigger
  conversationMenu.value = undefined
  conversationMenuTrigger = undefined
  if (restoreFocus) void nextTick(() => trigger?.focus())
}
function handleWindowConversationMenuDismiss(): void { closeConversationMenu() }
function openRenameDialog(item: ChatConversation): void { pendingConversation.value = item; renameTitle.value = item.title; renameDialogOpen.value = true; closeConversationMenu() }
function openDetails(item: ChatConversation): void { detailConversation.value = item; detailsDialogOpen.value = true; closeConversationMenu() }
function openDeleteDialog(item: ChatConversation): void { pendingConversation.value = item; deleteDialogOpen.value = true; closeConversationMenu() }
async function renameConversation(): Promise<void> { const item = pendingConversation.value; const title = renameTitle.value.trim(); if (!item || !title || conversationUpdating.value) return; conversationUpdating.value = true; try { replaceConversation(await chatApi.updateConversation(item.id, { title })); renameDialogOpen.value = false } catch (error) { message.error(extractApiErrorMessage(error, '重命名失败')) } finally { conversationUpdating.value = false } }
async function togglePinned(item: ChatConversation): Promise<void> { closeConversationMenu(); try { replaceConversation(await chatApi.updateConversation(item.id, { isPinned: !item.isPinned })); sortConversations() } catch (error) { message.error(extractApiErrorMessage(error, '更新置顶状态失败')) } }
async function confirmDeleteConversation(): Promise<void> { const item = pendingConversation.value; if (!item || conversationUpdating.value) return; conversationUpdating.value = true; try { await removeConversation(item.id); deleteDialogOpen.value = false } finally { conversationUpdating.value = false } }
function replaceConversation(next: ChatConversation): void { const index = conversations.value.findIndex((item) => item.id === next.id); if (index >= 0) conversations.value[index] = next; if (detailConversation.value?.id === next.id) detailConversation.value = next }
function sortConversations(): void { conversations.value.sort((left, right) => Number(right.isPinned) - Number(left.isPinned) || Date.parse(right.lastMessageAt) - Date.parse(left.lastMessageAt) || right.id.localeCompare(left.id)) }
function formatDetailTime(value: string): string { const date = new Date(value); return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString('zh-CN', { hour12: false }) }
function resetModelControls(): void { selectedReasoningEffort.value = defaultChatReasoningEffort(selectedModelOption.value); selectedServiceTier.value = defaultChatServiceTier(selectedModelOption.value) }
function normalizeCurrentModelControls(): void {
  const normalized = normalizeChatModelControls({
    model: selectedModelOption.value,
    reasoningEffort: selectedReasoningEffort.value,
    serviceTier: selectedServiceTier.value
  })
  selectedReasoningEffort.value = normalized.reasoningEffort
  selectedServiceTier.value = normalized.serviceTier
}
watch(selectedModel, resetModelControls)
function activateChatPage(): void {
  if (pageActive || disposed) return
  pageActive = true
  updateMobile()
  window.addEventListener('resize', updateMobile)
  window.addEventListener('click', handleWindowConversationMenuDismiss)
  window.addEventListener('blur', handleWindowConversationMenuDismiss)
  contextStatusTimer = window.setInterval(() => {
    void refreshContextStatus()
    const conversation = selectedConversation.value
    const runtime = conversation && chatGenerationRuntime.get(conversation.systemAccountId, conversation.id)
    if (conversation && runtime?.reconciliationReason) requestRuntimeReconciliationSync(runtime)
  }, 5_000)
  subscribeSelectedRuntime()
  broadcastUnsubscribe = cacheBroadcast.subscribe((payload) => {
    const conversation = selectedConversation.value
    if (!conversation || payload.systemAccountId !== conversation.systemAccountId || payload.conversationId !== conversation.id || payload.messageRevision <= conversation.messageRevision) return
    void refreshConversationFromSync(conversation.id)
  })
  if (pendingConfirmation.value) schedulePendingConfirmation()
  if (initialLoaded && selectedConversationId.value) void refreshConversationFromSync(selectedConversationId.value)
}
function deactivateChatPage(): void {
  if (!pageActive) return
  pageActive = false
  window.removeEventListener('resize', updateMobile)
  window.removeEventListener('click', handleWindowConversationMenuDismiss)
  window.removeEventListener('blur', handleWindowConversationMenuDismiss)
  runtimeUnsubscribe?.()
  runtimeUnsubscribe = undefined
  subscribedRuntimeConversationId = undefined
  broadcastUnsubscribe?.()
  broadcastUnsubscribe = undefined
  if (pendingConfirmationTimer !== undefined) { window.clearTimeout(pendingConfirmationTimer); pendingConfirmationTimer = undefined }
  if (contextStatusTimer !== undefined) { window.clearInterval(contextStatusTimer); contextStatusTimer = undefined }
  closeConversationMenu()
}
onMounted(() => {
  activateChatPage()
  void loadInitial().finally(() => { initialLoaded = true })
})
onActivated(activateChatPage)
onDeactivated(deactivateChatPage)
onBeforeUnmount(() => {
  disposed = true
  conversationLoadEpoch += 1
  deactivateChatPage()
  cacheBroadcast.close()
})
</script>

<style scoped>
.chat-workspace { height: var(--app-visual-viewport-height, 100dvh); min-height: 0; display: grid; grid-template-columns: 260px minmax(0, 1fr); overflow: hidden; background: #fff; border: 0; border-radius: 0; }
.conversation-panel { min-width: 0; border-right: 1px solid #e2e8f0; background: #f8fafc; }
.chat-main { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
.composer-shell { position: relative; padding: 12px clamp(12px, 3vw, 28px) 14px; border-top: 1px solid #e2e8f0; background: #fff; }
.turn-editing-bar { display: flex; align-items: center; justify-content: space-between; min-height: 30px; padding: 0 4px 4px; color: #64748b; font-size: 12px; }
.turn-limit-bar { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 30px; padding: 0 4px 4px; color: #64748b; font-size: 12px; }
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
:deep(.conversation-item) { width: 100%; height: 38px; display: flex; align-items: stretch; overflow: hidden; margin-bottom: 3px; color: #273449; font-size: 13px; background: transparent; border: 1px solid transparent; border-radius: 6px; }
:deep(.conversation-item-select) { min-width: 0; flex: 1; overflow: hidden; padding: 0 10px; color: inherit; font: inherit; line-height: 36px; text-align: left; text-overflow: ellipsis; white-space: nowrap; background: transparent; border: 0; cursor: pointer; }
:deep(.conversation-more-button) { width: 38px; flex: 0 0 38px; display: inline-flex; align-items: center; justify-content: center; padding: 0; color: #64748b; background: transparent; border: 0; cursor: pointer; }
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
.conversation-detail-id { display: flex; align-items: center; gap: 6px; min-width: 0; }
.conversation-detail-id code { flex: 1; min-width: 0; color: #334155; overflow-wrap: anywhere; }
@media (max-width: 991px) and (min-width: 821px) { :deep(.conversation-pane-toolbar) { padding-left: 56px; } }
@media (max-width: 820px) { .chat-workspace { grid-template-columns: minmax(0, 1fr); } .composer-shell { padding: 9px calc(9px + env(safe-area-inset-right)) calc(9px + env(safe-area-inset-bottom)) calc(9px + env(safe-area-inset-left)); } }
@media (pointer: coarse) {
  :deep(.conversation-new-button), :deep(.conversation-item), :deep(.conversation-item-select), :deep(.conversation-load-more), :deep(.conversation-more-button) { min-height: 44px; }
  :deep(.conversation-item) { height: 44px; }
  :deep(.conversation-item-select) { line-height: 42px; }
  :deep(.conversation-more-button) { width: 44px; flex-basis: 44px; }
  .conversation-context-menu button { min-height: 44px; }
  .jump-bottom-button { min-width: 44px; height: 44px; }
  .turn-editing-bar :deep(.ant-btn) { min-width: 44px; min-height: 44px; }
  .turn-limit-bar :deep(.ant-btn) { min-width: 44px; min-height: 44px; }
  .submission-confirmation-bar :deep(.ant-btn) { min-width: 44px; min-height: 44px; }
}
</style>

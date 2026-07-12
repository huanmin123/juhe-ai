<template>
  <section class="chat-workspace">
    <aside v-if="!mobile" class="conversation-panel">
      <ConversationPane />
    </aside>
    <a-drawer v-else v-model:open="conversationDrawerOpen" title="对话记录" placement="left" :width="300" :body-style="{ padding: 0 }">
      <ConversationPane @selected="conversationDrawerOpen = false" />
    </a-drawer>

    <main class="chat-main">
      <header class="chat-toolbar">
        <a-tooltip v-if="mobile" title="对话记录"><a-button type="text" aria-label="对话记录" @click="conversationDrawerOpen = true"><MenuOutlined /></a-button></a-tooltip>
        <div class="chat-title-block">
          <strong>{{ selectedConversation?.title || 'AI 问答' }}</strong>
          <span v-if="selectedConversation">{{ selectedConversation.apiKeyNameSnapshot }}</span>
        </div>
        <a-select
          v-model:value="selectedModel"
          class="model-select"
          :options="modelOptions"
          :loading="modelsLoading"
          :disabled="!selectedConversation || generating"
          placeholder="选择模型"
          show-search
        />
        <a-popconfirm v-if="selectedConversation" title="确定删除这个对话吗？" ok-text="删除" cancel-text="取消" @confirm="removeConversation">
          <a-tooltip title="删除对话"><a-button type="text" danger aria-label="删除对话"><DeleteOutlined /></a-button></a-tooltip>
        </a-popconfirm>
      </header>

      <template v-if="selectedConversation">
        <ChatMessageList ref="messageList" :messages="messages" :loading="messagesLoading" @near-top="loadOlderMessages" />
        <footer class="composer-shell">
          <div class="composer">
            <a-textarea v-model:value="draft" :auto-size="{ minRows: 1, maxRows: 6 }" :maxlength="196608" placeholder="输入消息" :disabled="generating" @keydown="handleComposerKeydown" />
            <a-tooltip v-if="generating" title="停止生成"><a-button class="send-button" danger aria-label="停止生成" @click="stopGeneration"><StopOutlined /></a-button></a-tooltip>
            <a-tooltip v-else title="发送"><a-button class="send-button" type="primary" aria-label="发送" :disabled="!canSend" @click="sendMessage"><SendOutlined /></a-button></a-tooltip>
          </div>
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
  </section>
</template>

<script setup lang="ts">
import { DeleteOutlined, MenuOutlined, MessageOutlined, PlusOutlined, SendOutlined, StopOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, defineComponent, h, onBeforeUnmount, onMounted, ref } from 'vue'
import { chatApi, streamChatMessage } from '@/api/domains/chat'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ChatApiKeyOption, ChatConversation, ChatMessage, ChatStreamEvent } from '@/types/domain/chat'
import { applyChatStreamEvent } from './chatStream'
import ChatMessageList from './ChatMessageList.vue'

const conversations = ref<ChatConversation[]>([])
const apiKeys = ref<ChatApiKeyOption[]>([])
const selectedConversationId = ref<string>()
const messages = ref<ChatMessage[]>([])
const models = ref<string[]>([])
const selectedModel = ref<string>()
const draft = ref('')
const messagesLoading = ref(false)
const olderMessagesLoading = ref(false)
const hasOlderMessages = ref(false)
const modelsLoading = ref(false)
const creating = ref(false)
const generating = ref(false)
const createDialogOpen = ref(false)
const conversationDrawerOpen = ref(false)
const newApiKeyId = ref<string>()
const mobile = ref(false)
const messageList = ref<InstanceType<typeof ChatMessageList>>()
let streamController: AbortController | undefined

const selectedConversation = computed(() => conversations.value.find((item) => item.id === selectedConversationId.value))
const apiKeyOptions = computed(() => apiKeys.value.map((item) => ({ label: item.name, value: item.id })))
const modelOptions = computed(() => models.value.map((item) => ({ label: item, value: item })))
const canSend = computed(() => Boolean(selectedConversation.value && selectedModel.value && draft.value.trim()))

const ConversationPane = defineComponent({
  emits: ['selected'],
  setup(_props, { emit }) {
    return () => h('div', { class: 'conversation-pane-inner' }, [
      h('div', { class: 'conversation-pane-toolbar' }, [h('strong', '对话'), h('button', { class: 'conversation-new-button', type: 'button', disabled: !apiKeys.value.length, onClick: openCreateDialog }, [h(PlusOutlined), ' 新建'])]),
      conversations.value.length
        ? h('div', { class: 'conversation-list' }, conversations.value.map((item) => h('button', { class: ['conversation-item', { active: item.id === selectedConversationId.value }], type: 'button', onClick: () => { void selectConversation(item.id); emit('selected') } }, [h('span', { class: 'conversation-item-title' }, item.title), h('span', { class: 'conversation-item-meta' }, formatConversationTime(item.lastMessageAt))])))
        : h('div', { class: 'conversation-list-empty' }, '暂无对话')
    ])
  }
})

async function loadInitial(): Promise<void> {
  try {
    const [keyItems, conversationItems] = await Promise.all([chatApi.listApiKeys(), chatApi.listConversations({ limit: 50 })])
    apiKeys.value = keyItems
    conversations.value = conversationItems
    if (conversationItems[0]) await selectConversation(conversationItems[0].id)
  } catch (error) { message.error(extractApiErrorMessage(error, '加载 AI 问答失败')) }
}
async function selectConversation(id: string): Promise<void> {
  if (generating.value || selectedConversationId.value === id) return
  selectedConversationId.value = id
  messagesLoading.value = true
  modelsLoading.value = true
  try {
    const [messageItems, modelItems] = await Promise.all([chatApi.listMessages(id, { limit: 100 }), chatApi.listModels(id)])
    messages.value = messageItems
    hasOlderMessages.value = messageItems.length === 100
    models.value = modelItems
    const conversation = conversations.value.find((item) => item.id === id)
    selectedModel.value = conversation?.lastModel && modelItems.includes(conversation.lastModel) ? conversation.lastModel : modelItems[0]
  } catch (error) { message.error(extractApiErrorMessage(error, '加载对话失败')) }
  finally { messagesLoading.value = false; modelsLoading.value = false }
}
async function loadOlderMessages(): Promise<void> {
  const id = selectedConversationId.value
  const first = messages.value[0]
  if (!id || !first || !hasOlderMessages.value || olderMessagesLoading.value || messagesLoading.value) return
  olderMessagesLoading.value = true
  const anchor = messageList.value?.captureScrollAnchor()
  try {
    const older = await chatApi.listMessages(id, { beforeSequenceNo: first.sequenceNo, limit: 100 })
    messages.value = [...older, ...messages.value]
    hasOlderMessages.value = older.length === 100
    if (anchor) await messageList.value?.restoreScrollAnchor(anchor)
  } catch (error) { message.error(extractApiErrorMessage(error, '加载更早消息失败')) }
  finally { olderMessagesLoading.value = false }
}
function openCreateDialog(): void { newApiKeyId.value = apiKeys.value[0]?.id; createDialogOpen.value = true }
async function createConversation(): Promise<void> {
  if (!newApiKeyId.value) return
  creating.value = true
  try { const item = await chatApi.createConversation(newApiKeyId.value); conversations.value.unshift(item); createDialogOpen.value = false; await selectConversation(item.id) }
  catch (error) { message.error(extractApiErrorMessage(error, '创建对话失败')) }
  finally { creating.value = false }
}
async function sendMessage(): Promise<void> {
  const conversation = selectedConversation.value
  const content = draft.value.trim()
  const model = selectedModel.value
  if (!conversation || !content || !model || generating.value) return
  generating.value = true
  draft.value = ''
  streamController = new AbortController()
  try {
    await streamChatMessage({ conversationId: conversation.id, clientMessageId: crypto.randomUUID(), content, model, signal: streamController.signal, onEvent: handleStreamEvent })
    const current = conversations.value.find((item) => item.id === conversation.id)
    if (current) { current.lastModel = model; current.lastMessageAt = new Date().toISOString(); const first = messages.value.find((item) => item.role === 'user'); if (current.title === '新对话' && first) current.title = first.contentText.slice(0, 60) }
  } catch (error) { if (!streamController.signal.aborted) message.error(extractApiErrorMessage(error, '发送失败')) }
  finally { generating.value = false; streamController = undefined }
}
function handleStreamEvent(event: ChatStreamEvent): void { applyChatStreamEvent(messages.value, event); if (event.type === 'message.failed') message.error(event.data.message); messageList.value?.scrollToBottom() }
async function stopGeneration(): Promise<void> { const id = selectedConversationId.value; if (!id) return; try { await chatApi.stop(id) } catch {} finally { streamController?.abort(); generating.value = false; await refreshMessages() } }
async function refreshMessages(): Promise<void> { const id = selectedConversationId.value; if (id) { messages.value = await chatApi.listMessages(id, { limit: 100 }); hasOlderMessages.value = messages.value.length === 100 } }
async function removeConversation(): Promise<void> { const id = selectedConversationId.value; if (!id) return; try { await chatApi.deleteConversation(id); conversations.value = conversations.value.filter((item) => item.id !== id); selectedConversationId.value = undefined; messages.value = []; const next = conversations.value[0]; if (next) await selectConversation(next.id) } catch (error) { message.error(extractApiErrorMessage(error, '删除对话失败')) } }
function handleComposerKeydown(event: KeyboardEvent): void { if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); void sendMessage() } }
function updateMobile(): void { mobile.value = window.innerWidth <= 820 }
function formatConversationTime(value: string): string { const date = new Date(value); return Number.isNaN(date.getTime()) ? '' : date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' }) }
onMounted(() => { updateMobile(); window.addEventListener('resize', updateMobile); void loadInitial() })
onBeforeUnmount(() => { window.removeEventListener('resize', updateMobile); streamController?.abort() })
</script>

<style scoped>
.chat-workspace { height: calc(100vh - 154px); min-height: 520px; display: grid; grid-template-columns: 260px minmax(0, 1fr); overflow: hidden; background: #fff; border: 1px solid #e2e8f0; border-radius: 8px; }
.conversation-panel { min-width: 0; border-right: 1px solid #e2e8f0; background: #f8fafc; }
.chat-main { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
.chat-toolbar { height: 58px; flex: 0 0 58px; display: flex; align-items: center; gap: 8px; padding: 0 14px 0 18px; border-bottom: 1px solid #e2e8f0; }
.chat-title-block { min-width: 0; flex: 1; display: flex; flex-direction: column; }
.chat-title-block strong { overflow: hidden; color: #172033; font-size: 15px; line-height: 22px; text-overflow: ellipsis; white-space: nowrap; }
.chat-title-block span { overflow: hidden; color: #64748b; font-size: 12px; line-height: 18px; text-overflow: ellipsis; white-space: nowrap; }
.model-select { width: min(260px, 34vw); }
.composer-shell { padding: 12px clamp(12px, 3vw, 28px) 14px; border-top: 1px solid #e2e8f0; background: #fff; }
.composer { display: grid; grid-template-columns: minmax(0, 1fr) 38px; align-items: end; gap: 8px; padding: 8px; border: 1px solid #cbd5e1; border-radius: 8px; box-shadow: 0 2px 8px rgba(15, 23, 42, 0.05); }
.composer:focus-within { border-color: #1677ff; box-shadow: 0 0 0 2px rgba(22, 119, 255, 0.1); }
.composer :deep(textarea) { padding: 5px 4px; border: 0; box-shadow: none !important; resize: none; }
.send-button { width: 38px; height: 38px; padding: 0; }
.chat-start-state { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 14px; color: #64748b; }
.chat-start-state > :deep(.anticon) { font-size: 36px; color: #94a3b8; }
.chat-start-state strong { color: #334155; font-size: 16px; }
:deep(.conversation-pane-inner) { height: 100%; display: flex; flex-direction: column; }
:deep(.conversation-pane-toolbar) { height: 58px; display: flex; align-items: center; justify-content: space-between; padding: 0 12px 0 16px; border-bottom: 1px solid #e2e8f0; }
:deep(.conversation-pane-toolbar strong) { color: #172033; font-size: 15px; }
:deep(.conversation-new-button) { height: 30px; display: inline-flex; align-items: center; padding: 0 9px; color: #1677ff; background: #fff; border: 1px solid #b9d7ff; border-radius: 6px; cursor: pointer; }
:deep(.conversation-new-button:disabled) { color: #94a3b8; border-color: #e2e8f0; cursor: not-allowed; }
:deep(.conversation-list) { flex: 1; min-height: 0; overflow-y: auto; padding: 8px; }
:deep(.conversation-item) { width: 100%; min-height: 54px; display: flex; flex-direction: column; align-items: stretch; justify-content: center; gap: 2px; margin-bottom: 4px; padding: 8px 10px; text-align: left; background: transparent; border: 1px solid transparent; border-radius: 6px; cursor: pointer; }
:deep(.conversation-item:hover) { background: #fff; border-color: #e2e8f0; }
:deep(.conversation-item.active) { background: #eaf3ff; border-color: #b9d7ff; }
:deep(.conversation-item-title) { overflow: hidden; color: #273449; font-size: 13px; line-height: 20px; text-overflow: ellipsis; white-space: nowrap; }
:deep(.conversation-item-meta) { color: #8492a6; font-size: 11px; line-height: 16px; }
:deep(.conversation-list-empty) { padding: 32px 12px; color: #94a3b8; text-align: center; }
@media (max-width: 820px) { .chat-workspace { height: calc(100vh - 140px); min-height: 440px; grid-template-columns: minmax(0, 1fr); border-right: 0; border-left: 0; border-radius: 0; } .chat-toolbar { padding: 0 8px; } .model-select { width: 42vw; min-width: 120px; } .composer-shell { padding: 9px; } }
</style>

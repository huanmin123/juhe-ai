<template>
  <a-modal
    :open="open"
    title="新建对话"
    ok-text="创建"
    cancel-text="取消"
    width="440px"
    :ok-button-props="{ disabled: !selectedObjectId }"
    :confirm-loading="creating"
    @update:open="handleOpenUpdate"
    @ok="createConversation"
  >
    <div class="chat-create-conversation-form">
      <div class="chat-create-conversation-field">
        <span class="chat-create-conversation-label">绑定模式</span>
        <a-segmented v-model:value="bindMode" :options="bindModeOptions" block @change="handleBindModeChange" />
      </div>
      <div class="chat-create-conversation-field">
        <span class="chat-create-conversation-label">{{ objectLabel }}</span>
        <a-select
          v-if="bindMode === 'api_key'"
          v-model:value="selectedObjectId"
          :options="apiKeyOptions"
          :loading="optionsLoading.api_key"
          placeholder="请选择 API Key"
          show-search
          option-filter-prop="label"
          @dropdown-visible-change="handleDropdownVisibleChange"
        />
        <a-select
          v-else-if="bindMode === 'group'"
          v-model:value="selectedObjectId"
          :options="groupOptions"
          :loading="optionsLoading.group"
          placeholder="请选择分组"
          show-search
          option-filter-prop="label"
          @dropdown-visible-change="handleDropdownVisibleChange"
        />
        <a-select
          v-else
          v-model:value="selectedObjectId"
          :options="accountOptions"
          :loading="optionsLoading.account"
          placeholder="请选择账户"
          show-search
          option-filter-prop="label"
          @dropdown-visible-change="handleDropdownVisibleChange"
        />
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import { chatApi, type ChatConversationCreatePayload } from '@/api/domains/chat'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ChatConversation, ChatConversationBindMode } from '@/types/domain/chat'

interface ChatCreateOption {
  label: string
  value: string
}

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'created', conversation: ChatConversation): void
}>()

const bindModeOptions: Array<{ label: string; value: ChatConversationBindMode }> = [
  { label: 'API Key', value: 'api_key' },
  { label: '分组', value: 'group' },
  { label: '账户', value: 'account' }
]

const bindMode = ref<ChatConversationBindMode>('api_key')
const selectedObjectId = ref<string>()
const creating = ref(false)
const apiKeyOptions = ref<ChatCreateOption[]>([])
const groupOptions = ref<ChatCreateOption[]>([])
const accountOptions = ref<ChatCreateOption[]>([])
const optionsLoading = ref<Record<ChatConversationBindMode, boolean>>({ api_key: false, group: false, account: false })
const loadedKinds = new Set<ChatConversationBindMode>()

const objectLabel = computed(() => bindMode.value === 'api_key' ? 'API Key' : bindMode.value === 'group' ? '分组' : '账户')

watch(() => props.open, (open) => {
  if (!open) return
  // 不记忆上次选择：每次打开都回到默认模式，并清空已选对象与已加载的下拉数据。
  bindMode.value = 'api_key'
  selectedObjectId.value = undefined
  creating.value = false
  loadedKinds.clear()
  apiKeyOptions.value = []
  groupOptions.value = []
  accountOptions.value = []
  optionsLoading.value = { api_key: false, group: false, account: false }
})

function handleOpenUpdate(value: boolean): void {
  emit('update:open', value)
}
function handleBindModeChange(): void {
  // 切换模式清空已选对象，避免把上一模式的 ID 带进创建请求。
  selectedObjectId.value = undefined
}
function handleDropdownVisibleChange(open: boolean): void {
  if (open) void ensureOptionsLoaded(bindMode.value)
}
async function ensureOptionsLoaded(kind: ChatConversationBindMode): Promise<void> {
  if (loadedKinds.has(kind)) return
  loadedKinds.add(kind)
  optionsLoading.value[kind] = true
  try {
    if (kind === 'api_key') {
      const result = await api.myApiKeys.list({ status: 'active' })
      apiKeyOptions.value = result.items
        .filter((item) => !item.expiresAt || new Date(item.expiresAt).getTime() > Date.now())
        .map((item) => ({ label: item.name, value: item.id }))
    } else if (kind === 'group') {
      const items = await api.groups.options({ purpose: 'select', limit: 50 })
      groupOptions.value = items.map((item) => ({ label: item.name, value: item.id }))
    } else {
      const items = await api.accounts.options({ status: 'active', limit: 50 })
      accountOptions.value = items.map((item) => ({ label: item.name, value: item.id }))
    }
  } catch (error) {
    // 失败后允许下次展开重试。
    loadedKinds.delete(kind)
    message.error(extractApiErrorMessage(error, kind === 'api_key' ? '加载 API Key 列表失败' : kind === 'group' ? '加载分组列表失败' : '加载账户列表失败'))
  } finally {
    optionsLoading.value[kind] = false
  }
}
async function createConversation(): Promise<void> {
  if (creating.value || !selectedObjectId.value) return
  creating.value = true
  try {
    // 三种模式字段互斥：只携带当前模式对应的绑定对象 ID。
    const payload: ChatConversationCreatePayload = { bindMode: bindMode.value }
    if (bindMode.value === 'api_key') payload.apiKeyId = selectedObjectId.value
    else if (bindMode.value === 'group') payload.groupId = selectedObjectId.value
    else payload.accountId = selectedObjectId.value
    const conversation = await chatApi.createConversation(payload)
    emit('update:open', false)
    emit('created', conversation)
  } catch (error) {
    message.error(extractApiErrorMessage(error, '创建对话失败'))
  } finally {
    creating.value = false
  }
}
</script>

<style scoped>
.chat-create-conversation-form { display: grid; gap: 16px; padding: 4px 0; }
.chat-create-conversation-field { display: grid; gap: 8px; }
.chat-create-conversation-label { color: #334155; font-size: 13px; }
</style>

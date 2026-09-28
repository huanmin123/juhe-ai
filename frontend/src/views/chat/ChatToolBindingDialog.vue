<template>
  <a-modal
    :open="open"
    :title="`设置${toolTitle}绑定`"
    ok-text="保存"
    cancel-text="取消"
    :confirm-loading="saving"
    :mask-closable="false"
    @ok="save"
    @cancel="emit('close')"
  >
    <a-spin v-if="loading" size="small" />
    <template v-else-if="status">
      <p class="chat-tool-binding-intro">{{ introText }}</p>
      <p v-if="status.invalidReason" class="chat-tool-binding-warning">{{ status.invalidReason }}</p>
      <a-radio-group v-model:value="selectedKey" class="chat-tool-binding-options">
        <a-radio class="chat-tool-binding-unbound" value="">
          <span>不绑定（需要时由会话引导设置）</span>
        </a-radio>
        <a-radio v-for="candidate in status.candidates ?? []" :key="candidateKey(candidate)" :value="candidateKey(candidate)">
          <span>{{ candidate.accountName }} · {{ candidate.modelName }}</span>
        </a-radio>
      </a-radio-group>
      <p v-if="!status.candidates?.length" class="chat-tool-binding-warning">
        当前没有可用的绑定候选：需要账户可用且支持该能力的模型。
      </p>
    </template>
    <p v-else class="chat-tool-binding-warning">绑定状态暂时无法读取，请关闭后重试。</p>
  </a-modal>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, ref, watch } from 'vue'
import { chatApi } from '@/api/domains/chat'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ChatConversation, ChatConversationToolCapability, ChatToolBindingCandidate } from '@/types/domain/chat'

// 模型工具的会话级绑定设置弹窗（工具体系设计 §8）：web_search 绑「账户+模型」
// 二元组；generate_image 绑账户（模型列沿用 defaultImageModel，选定的候选模型
// 与当前默认图像模型不同时随请求一并更新，保证绑定立即生效）。
const props = defineProps<{
  open: boolean
  conversation: ChatConversation
  toolId: string
}>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'saved', conversation: ChatConversation): void
}>()

const toolTitles: Record<string, string> = { web_search: '网页搜索', generate_image: '图片生成' }
const loading = ref(false)
const saving = ref(false)
const status = ref<ChatConversationToolCapability>()
const selectedKey = ref('')

const toolTitle = computed(() => toolTitles[props.toolId] ?? props.toolId)
const introText = computed(() => props.toolId === 'generate_image'
  ? '图片生成由绑定的账户执行；模型跟随「默认图像模型」。'
  : '网页搜索由绑定的账户和模型执行，可与对话使用不同的账户。')

function candidateKey(candidate: ChatToolBindingCandidate): string {
  return `${candidate.accountId}@${candidate.modelId}`
}

watch(() => props.open, (open) => {
  if (!open) return
  selectedKey.value = ''
  status.value = undefined
  void loadStatus()
})

async function loadStatus(): Promise<void> {
  loading.value = true
  try {
    const payload = await chatApi.getToolBindings(props.conversation.id)
    const found = payload.tools.find((tool) => tool.id === props.toolId)
    status.value = found
    if (found?.binding) selectedKey.value = candidateKey(found.binding)
  } catch (error) {
    message.error(extractApiErrorMessage(error, '绑定状态加载失败'))
  } finally {
    loading.value = false
  }
}

async function save(): Promise<void> {
  const current = status.value
  if (!current || saving.value) return
  const candidate = (current.candidates ?? []).find((item) => candidateKey(item) === selectedKey.value)
  saving.value = true
  try {
    let updated: ChatConversation
    if (props.toolId === 'generate_image') {
      // 绑定校验只看账户，但生效模型 = defaultImageModel：选定候选模型不同时
      // 一并切换，否则绑定会立即显示失效（契约 §8.2 联动）。
      updated = await chatApi.updateConversation(props.conversation.id, {
        imageBinding: candidate ? { accountId: candidate.accountId } : null,
        ...(candidate && candidate.modelId !== props.conversation.defaultImageModel
          ? { defaultImageModel: candidate.modelId as ChatConversation['defaultImageModel'] }
          : {})
      })
    } else {
      updated = await chatApi.updateConversation(props.conversation.id, {
        searchBinding: candidate ? { accountId: candidate.accountId, modelId: candidate.modelId } : null
      })
    }
    emit('saved', updated)
    message.success(`${toolTitle.value}绑定已更新`)
    emit('close')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '绑定保存失败'))
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.chat-tool-binding-intro { margin: 0 0 10px; color: #64748b; font-size: 12px; }
.chat-tool-binding-warning { margin: 0 0 10px; color: #b45309; font-size: 12px; }
.chat-tool-binding-options { display: grid; gap: 8px; width: 100%; max-height: 300px; overflow-y: auto; }
.chat-tool-binding-unbound span { color: #64748b; }
</style>

<template>
  <a-modal
    :open="open"
    :title="dialogTitle"
    :width="'min(92vw, 720px)'"
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
      <a-input
        v-if="status.candidates?.length"
        v-model:value="filterText"
        class="chat-tool-binding-filter"
        size="small"
        allow-clear
        placeholder="过滤账户或模型"
      />
      <a-radio-group v-model:value="selectedKey" class="chat-tool-binding-options">
        <a-radio class="chat-tool-binding-unbound" value="">
          <span>不绑定（需要时由会话引导设置）</span>
        </a-radio>
        <a-radio v-for="candidate in visibleCandidates" :key="candidateKey(candidate)" :value="candidateKey(candidate)">
          <span>{{ candidate.accountName }} · {{ candidate.modelName }}</span>
        </a-radio>
      </a-radio-group>
      <p v-if="!status.candidates?.length" class="chat-tool-binding-warning">
        当前没有可用的绑定候选：需要账户可用且支持该能力的模型。
      </p>
      <p v-else-if="!visibleCandidates.length" class="chat-tool-binding-warning">
        没有匹配「{{ filterText.trim() }}」的候选，请调整过滤词。
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
import type { ChatConversation, ChatConversationToolCapability, ChatConversationToolCapabilities, ChatImageModel, ChatToolBindingCandidate } from '@/types/domain/chat'

// 模型工具的绑定设置弹窗（工具体系设计 §8/§10.6/§10.7）：web_search 绑「账户+
// 模型」二元组；generate_image 绑账户（模型列沿用 defaultImageModel，选定的
// 候选模型与当前生效默认图像模型不同时随请求一并更新，保证绑定立即生效）。
// mode='conversation'（默认）读写会话级绑定；mode='user' 为全局模式，读写用户
// 级默认偏好端点（GET/PATCH /my-chat/tool-preferences），保存只改用户默认、
// 不触碰任何会话。
const props = defineProps<{
  open: boolean
  conversation?: ChatConversation
  toolId: string
  mode?: 'conversation' | 'user'
}>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'saved', conversation?: ChatConversation): void
}>()

const toolTitles: Record<string, string> = { web_search: '网页搜索', generate_image: '图片生成' }
const loading = ref(false)
const saving = ref(false)
const status = ref<ChatConversationToolCapability>()
const selectedKey = ref('')
const filterText = ref('')
// 全局默认置顶（工具体系设计 §10.4，2026-10-02 起）：打开时拉用户级默认偏好，
// 命中该工具默认绑定组合的候选置顶，其余保持接口顺序（sort 稳定排序）；
// 无偏好或偏好拉取失败时保持接口顺序。
const defaultTopCandidateKey = ref<string | undefined>()
// user 模式生图保存的联动基准：GET 偏好返回的 generate_image 生效模型（偏好行
// default_image_model）；选定候选模型与之不同时随 PATCH 附带 defaultImageModel。
const effectiveImageModel = ref<string>()

const mode = computed(() => props.mode ?? 'conversation')
const toolTitle = computed(() => toolTitles[props.toolId] ?? props.toolId)
const dialogTitle = computed(() => mode.value === 'user' ? `设置${toolTitle.value}全局默认绑定` : `设置${toolTitle.value}绑定`)
const introText = computed(() => {
  const globalSuffix = '；新会话自动继承，会话内修改会同步为默认。'
  if (props.toolId === 'generate_image') {
    return mode.value === 'user'
      ? `图片生成全局默认由绑定的账户执行，模型跟随「默认图像模型」${globalSuffix}`
      : '图片生成由绑定的账户执行；模型跟随「默认图像模型」。'
  }
  return mode.value === 'user'
    ? `网页搜索全局默认由绑定的账户和模型执行，可与对话使用不同的账户${globalSuffix}`
    : '网页搜索由绑定的账户和模型执行，可与对话使用不同的账户。'
})
// 输入过滤（§10.4）：按账户名/模型名模糊包含匹配，大小写不敏感。
const visibleCandidates = computed(() => {
  const candidates = status.value?.candidates ?? []
  const query = filterText.value.trim().toLowerCase()
  const filtered = query
    ? candidates.filter((item) => item.accountName.toLowerCase().includes(query) || item.modelName.toLowerCase().includes(query))
    : [...candidates]
  const top = defaultTopCandidateKey.value
  if (!top) return filtered
  return [...filtered].sort((left, right) => Number(candidateKey(left) !== top) - Number(candidateKey(right) !== top))
})

function candidateKey(candidate: ChatToolBindingCandidate): string {
  return `${candidate.accountId}@${candidate.modelId}`
}

// 从偏好 payload 提取排序基准与生图生效默认模型：generate_image 的生效模型取
// 偏好行 default_image_model（工具体系设计 §8.5）。
function applyToolPreferences(preferences: ChatConversationToolCapabilities | undefined): void {
  const preferredTool = preferences?.tools.find((tool) => tool.id === props.toolId)
  defaultTopCandidateKey.value = preferredTool?.binding ? candidateKey(preferredTool.binding) : undefined
  if (props.toolId === 'generate_image') {
    effectiveImageModel.value = preferredTool?.binding?.modelId
  }
}

watch(() => props.open, (open) => {
  if (!open) return
  selectedKey.value = ''
  filterText.value = ''
  defaultTopCandidateKey.value = undefined
  effectiveImageModel.value = undefined
  status.value = undefined
  void loadStatus()
})

async function loadStatus(): Promise<void> {
  loading.value = true
  try {
    if (mode.value === 'user') {
      // user 模式：GET 偏好本身就是数据源（状态与排序同源，单请求）。
      const preferences = await chatApi.getToolPreferences()
      applyToolPreferences(preferences)
      const found = preferences.tools.find((tool) => tool.id === props.toolId)
      status.value = found
      if (found?.binding) selectedKey.value = candidateKey(found.binding)
    } else {
      // conversation 模式：状态来自 tool-bindings，排序基准额外拉一次用户默认
      // 偏好（仅排序增强，失败不阻断弹窗，按「无偏好」处理）。
      const [bindings, preferences] = await Promise.all([
        chatApi.getToolBindings(props.conversation!.id),
        chatApi.getToolPreferences().catch(() => undefined)
      ])
      applyToolPreferences(preferences)
      const found = bindings.tools.find((tool) => tool.id === props.toolId)
      status.value = found
      if (found?.binding) selectedKey.value = candidateKey(found.binding)
    }
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
    if (mode.value === 'user') {
      // 全局模式：PATCH 偏好端点，只改用户默认、不触碰任何会话（§8.6）。
      // 生图绑定校验只看账户，但生效模型 = 偏好行 defaultImageModel：选定候选
      // 模型不同时一并切换，否则默认会立即显示失效（契约 §8.6 联动）。
      await chatApi.updateToolPreferences(props.toolId === 'generate_image'
        ? {
            imageBinding: candidate ? { accountId: candidate.accountId } : null,
            ...(candidate && candidate.modelId !== effectiveImageModel.value
              ? { defaultImageModel: candidate.modelId as ChatImageModel }
              : {})
          }
        : { searchBinding: candidate ? { accountId: candidate.accountId, modelId: candidate.modelId } : null })
      emit('saved')
      // 成功提示由 ChatView 按 mode 分支统一发出（user 模式不触碰会话详情）。
      emit('close')
      return
    }
    let updated: ChatConversation
    if (props.toolId === 'generate_image') {
      // 绑定校验只看账户，但生效模型 = defaultImageModel：选定候选模型不同时
      // 一并切换，否则绑定会立即显示失效（契约 §8.2 联动）。
      updated = await chatApi.updateConversation(props.conversation!.id, {
        imageBinding: candidate ? { accountId: candidate.accountId } : null,
        ...(candidate && candidate.modelId !== props.conversation!.defaultImageModel
          ? { defaultImageModel: candidate.modelId as ChatConversation['defaultImageModel'] }
          : {})
      })
    } else {
      updated = await chatApi.updateConversation(props.conversation!.id, {
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
.chat-tool-binding-intro { margin: 0 0 10px; color: var(--juhe-muted); font-size: 12px; }
.chat-tool-binding-warning { margin: 0 0 10px; color: var(--juhe-warn); font-size: 12px; }
.chat-tool-binding-filter { margin-bottom: 10px; }
.chat-tool-binding-options { display: grid; gap: 8px; width: 100%; max-height: 300px; overflow-y: auto; }
.chat-tool-binding-unbound span { color: var(--juhe-muted); }
</style>

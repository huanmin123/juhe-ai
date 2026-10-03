<template>
  <a-modal
    :open="open"
    title="工具模型绑定"
    :width="'min(92vw, 720px)'"
    ok-text="保存"
    cancel-text="取消"
    :confirm-loading="saving"
    :ok-button-props="okButtonProps"
    :mask-closable="false"
    @ok="save"
    @cancel="emit('close')"
  >
    <a-spin v-if="loading" size="small" />
    <template v-else-if="sections.length">
      <p class="chat-tool-binding-intro">{{ introText }}</p>
      <section v-for="section in sections" :key="section.id" class="chat-tool-binding-section">
        <div class="chat-tool-binding-section-heading">
          <strong>{{ section.title }}</strong>
          <a-tag :color="sectionStateColor(section)">{{ sectionStateText(section) }}</a-tag>
          <span class="chat-tool-binding-current">当前生效：{{ section.currentText }}</span>
        </div>
        <p v-if="section.invalidReason" class="chat-tool-binding-warning">{{ section.invalidReason }}</p>
        <template v-if="section.candidates.length">
          <div class="chat-tool-binding-row">
            <span class="chat-tool-binding-row-label">账户</span>
            <a-select
              class="chat-tool-binding-select"
              :value="selectedAccountId(section)"
              :options="accountOptions(section)"
              show-search
              :filter-option="filterSelectOption"
              aria-label="绑定账户"
              @update:value="onAccountChange(section, $event)"
            />
          </div>
          <div class="chat-tool-binding-row">
            <span class="chat-tool-binding-row-label">模型</span>
            <a-select
              class="chat-tool-binding-select"
              :value="selectedModelId(section)"
              :options="modelOptions(section)"
              :disabled="!selectedAccountId(section)"
              show-search
              :filter-option="filterSelectOption"
              aria-label="绑定模型"
              placeholder="先选择账户"
              @update:value="onModelChange(section, $event)"
            />
          </div>
        </template>
        <p v-else class="chat-tool-binding-warning">
          当前没有可用的绑定候选：需要账户可用且支持该能力的模型。
        </p>
      </section>
    </template>
    <p v-else class="chat-tool-binding-warning">绑定状态暂时无法读取，请关闭后重试。</p>
  </a-modal>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, ref, watch } from 'vue'
import { chatApi } from '@/api/domains/chat'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ChatImageModel, ChatToolBindingCandidate, ChatToolPreferencesPatch } from '@/types/domain/chat'

// 工具模型绑定统一弹窗（工具体系设计 §10.6/§10.7，2026-10-03 收敛；2026-10-04
// 两级选择器形态）：纯全局语义，读写用户级默认偏好端点（GET/PATCH
// /my-chat/tool-preferences），一次展示偏好响应中全部模型工具各一区。每区为
// 「账户 → 模型」两级下拉（账户去重、模型跟随所选账户过滤，下拉自带搜索），
// 区标题右侧明示当前生效值；「不绑定」即账户下拉空值。保存 = 一个 PATCH 携带
// 全部键；当前会话的立即跟随由 ChatView 在 saved 回调中完成。
const props = defineProps<{
  open: boolean
}>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'saved', payload: ChatToolPreferencesPatch): void
}>()

const toolTitles: Record<string, string> = { web_search: '网页搜索', generate_image: '图片生成' }
const loading = ref(false)
const saving = ref(false)
// 偏好行当前生效的默认图像模型（来自 generate_image binding.modelId）；未绑定
// 账户时偏好响应不含该值，保存省略 defaultImageModel 即保持现值。
const effectiveImageModel = ref<ChatImageModel>()

interface ChatToolBindingSection {
  id: string
  title: string
  bound: boolean
  valid: boolean
  invalidReason: string
  currentText: string
  candidates: ChatToolBindingCandidate[]
  selectedKey: string
}
const sections = ref<ChatToolBindingSection[]>([])

const introText = '为网页搜索与图片生成设置全局默认绑定「账户 + 模型」；新会话自动继承，保存后当前会话立即同步。'

function candidateKey(candidate: ChatToolBindingCandidate): string {
  return `${candidate.accountId}@${candidate.modelId}`
}

function selectedAccountId(section: ChatToolBindingSection): string {
  return section.selectedKey ? section.selectedKey.slice(0, section.selectedKey.indexOf('@')) : ''
}

function selectedModelId(section: ChatToolBindingSection): string {
  return section.selectedKey ? section.selectedKey.slice(section.selectedKey.indexOf('@') + 1) : ''
}

// 账户下拉：空值即「不绑定」；账户去重保持接口顺序。
function accountOptions(section: ChatToolBindingSection): Array<{ value: string; label: string }> {
  const seen = new Map<string, string>()
  for (const candidate of section.candidates) {
    if (!seen.has(candidate.accountId)) seen.set(candidate.accountId, candidate.accountName)
  }
  return [{ value: '', label: '不绑定' }, ...Array.from(seen, ([value, label]) => ({ value, label }))]
}

// 模型下拉：只列当前所选账户在候选内的模型；未选账户时为空且禁用。
function modelOptions(section: ChatToolBindingSection): Array<{ value: string; label: string }> {
  const accountId = selectedAccountId(section)
  return section.candidates
    .filter((candidate) => candidate.accountId === accountId)
    .map((candidate) => ({ value: candidate.modelId, label: candidate.modelName || candidate.modelId }))
}

function onAccountChange(section: ChatToolBindingSection, accountId: string | number): void {
  const id = String(accountId)
  if (!id) {
    section.selectedKey = ''
    return
  }
  // 换账户时尽量保留同模型（该账户存在同名模型才保留），否则取该账户首项。
  const sameModel = section.candidates.find((candidate) => candidate.accountId === id && candidate.modelId === selectedModelId(section))
  const next = sameModel ?? section.candidates.find((candidate) => candidate.accountId === id)
  section.selectedKey = next ? candidateKey(next) : ''
}

function onModelChange(section: ChatToolBindingSection, modelId: string | number): void {
  const accountId = selectedAccountId(section)
  section.selectedKey = accountId ? `${accountId}@${String(modelId)}` : ''
}

function filterSelectOption(input: string, option: { label?: string }): boolean {
  return String(option?.label ?? '').toLowerCase().includes(input.trim().toLowerCase())
}

function sectionStateText(section: ChatToolBindingSection): string {
  if (!section.bound) return '未设置'
  return section.valid ? '已绑定' : '已失效'
}
function sectionStateColor(section: ChatToolBindingSection): string {
  if (!section.bound) return 'default'
  return section.valid ? 'success' : 'warning'
}

watch(() => props.open, (open) => {
  if (!open) return
  sections.value = []
  effectiveImageModel.value = undefined
  void loadSections()
})

async function loadSections(): Promise<void> {
  loading.value = true
  try {
    // 偏好端点本身就是数据源（状态、候选与当前绑定同源，单请求）。
    const preferences = await chatApi.getToolPreferences()
    effectiveImageModel.value = preferences.tools.find((tool) => tool.id === 'generate_image')?.binding?.modelId as ChatImageModel | undefined
    sections.value = preferences.tools
      .filter((tool) => tool.kind === 'model')
      .map((tool) => ({
        id: tool.id,
        title: toolTitles[tool.id] ?? tool.id,
        bound: Boolean(tool.bound),
        valid: Boolean(tool.valid),
        invalidReason: tool.invalidReason ?? '',
        currentText: tool.bound && tool.binding
          ? (tool.binding.modelId ? `${tool.binding.accountName} · ${tool.binding.modelId}` : tool.binding.accountName)
          : '未设置',
        candidates: tool.candidates ?? [],
        selectedKey: tool.binding ? candidateKey(tool.binding) : ''
      }))
  } catch (error) {
    message.error(extractApiErrorMessage(error, '绑定状态加载失败'))
  } finally {
    loading.value = false
    initialPayloadJson.value = JSON.stringify(buildPayload())
  }
}

function sectionCandidate(toolId: string): ChatToolBindingCandidate | undefined {
  const section = sections.value.find((item) => item.id === toolId)
  return section?.candidates.find((item) => candidateKey(item) === section.selectedKey)
}

// UI 最终值的唯一构造点：保存与「未变更禁用保存」共用同一形态，保证 dirty
// 判定与实际提交的 payload 恒一致。
function buildPayload(): ChatToolPreferencesPatch {
  const searchCandidate = sectionCandidate('web_search')
  const imageCandidate = sectionCandidate('generate_image')
  const imageModel = imageCandidate ? imageCandidate.modelId as ChatImageModel : effectiveImageModel.value
  return {
    searchBinding: searchCandidate ? { accountId: searchCandidate.accountId, modelId: searchCandidate.modelId } : null,
    imageBinding: imageCandidate ? { accountId: imageCandidate.accountId } : null,
    ...(imageModel ? { defaultImageModel: imageModel } : {})
  }
}

// 打开加载完成时的 payload 快照；选项未偏离该快照时禁用保存，避免"点了保存
// 却毫无变化"的无效提交。
const initialPayloadJson = ref('')
const dirty = computed(() => sections.value.length > 0 && JSON.stringify(buildPayload()) !== initialPayloadJson.value)
const okButtonProps = computed(() => ({ disabled: !dirty.value }))

async function save(): Promise<void> {
  if (saving.value || !dirty.value) return
  const payload = buildPayload()
  saving.value = true
  try {
    await chatApi.updateToolPreferences(payload)
    emit('saved', payload)
    emit('close')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '绑定保存失败'))
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.chat-tool-binding-intro { margin: 0 0 12px; color: var(--juhe-muted); font-size: 12px; }
.chat-tool-binding-warning { margin: 0 0 10px; color: var(--juhe-warn); font-size: 12px; }
.chat-tool-binding-section { margin-bottom: 16px; padding-bottom: 4px; border-bottom: 1px solid var(--juhe-border); }
.chat-tool-binding-section:last-child { border-bottom: 0; }
.chat-tool-binding-section-heading { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.chat-tool-binding-section-heading strong { color: var(--juhe-fg-soft); font-size: 13px; }
.chat-tool-binding-current { margin-left: auto; color: var(--juhe-muted); font-size: 12px; }
.chat-tool-binding-row { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.chat-tool-binding-row-label { flex: 0 0 2.5em; color: var(--juhe-muted); font-size: 12px; }
.chat-tool-binding-select { flex: 1 1 auto; }
</style>

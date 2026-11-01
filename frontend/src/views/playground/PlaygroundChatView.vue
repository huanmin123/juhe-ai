<template>
  <a-card class="page-card responsive-page-card">
    <div class="playground-chat-layout">
      <aside class="playground-side">
        <a-form layout="vertical">
          <a-form-item label="账户" required>
            <a-select
              v-model:value="accountId"
              :options="accountOptions"
              :loading="accountsLoading"
              allow-clear
              show-search
              option-filter-prop="label"
              placeholder="选择要试玩的账户"
              @change="handleAccountChange"
            />
          </a-form-item>
          <a-form-item label="模型" required>
            <a-select
              v-model:value="modelId"
              :options="modelOptions"
              :disabled="!accountId || !modelOptions.length"
              show-search
              option-filter-prop="label"
              :placeholder="accountId ? '选择 chat 模型' : '先选择账户'"
            />
            <div v-if="currentModel?.functionCalling" class="playground-model-hint">该模型声明 function_calling，后端默认注入 fetch_url 工具</div>
          </a-form-item>
          <a-form-item label="System 提示词（可选）">
            <a-textarea
              v-model:value="system"
              :rows="3"
              placeholder="设定助手的行为与语气"
            />
          </a-form-item>
          <a-form-item :label="`温度 ${temperature.toFixed(1)}`">
            <a-slider v-model:value="temperature" :min="0" :max="2" :step="0.1" />
          </a-form-item>
          <a-form-item label="最大生成长度（可选）">
            <a-input-number
              v-model:value="maxTokens"
              :min="1"
              placeholder="默认不限制"
              style="width: 100%"
            />
          </a-form-item>
          <a-form-item>
            <div class="playground-inline-row">
              <a-tooltip title="演练场对话固定流式输出（后端仅支持 stream: true）">
                <span class="playground-toggle-label">流式输出</span>
              </a-tooltip>
              <a-switch v-model:checked="stream" disabled />
            </div>
          </a-form-item>
          <a-collapse ghost class="playground-advanced-collapse">
            <a-collapse-panel key="advanced" header="高级参数">
              <a-form-item :label="`核采样 Top P ${topP.toFixed(2)}`">
                <a-slider v-model:value="topP" :min="0" :max="1" :step="0.05" />
              </a-form-item>
              <a-form-item :label="`频率惩罚 ${frequencyPenalty.toFixed(1)}`">
                <a-slider v-model:value="frequencyPenalty" :min="-2" :max="2" :step="0.1" />
              </a-form-item>
              <a-form-item :label="`存在惩罚 ${presencePenalty.toFixed(1)}`">
                <a-slider v-model:value="presencePenalty" :min="-2" :max="2" :step="0.1" />
              </a-form-item>
            </a-collapse-panel>
          </a-collapse>
        </a-form>
      </aside>

      <section class="playground-conversation">
        <div ref="messageListEl" class="playground-messages">
          <a-empty
            v-if="!messages.length"
            class="playground-empty"
            description="选择账户与模型,发送第一条消息开始试玩"
          />
          <template v-for="(item, index) in messages" :key="index">
            <div v-if="item.role === 'user'" class="playground-message-user">
              <div class="playground-bubble playground-bubble-user">{{ item.content }}</div>
            </div>
            <div v-else class="playground-message-assistant">
              <div v-if="item.tools.length" class="playground-tool-timeline">
                <div v-for="tool in item.tools" :key="tool.callId" class="playground-tool-step">
                  <span class="playground-tool-dot" :class="`is-${tool.status}`" />
                  <div class="playground-tool-body">
                    <div class="playground-tool-title">
                      fetch_url（{{ tool.name }}）
                      <span v-if="tool.status === 'running'" class="playground-tool-state">执行中…</span>
                      <span v-else-if="tool.status === 'ok'" class="playground-tool-state is-ok">完成</span>
                      <span v-else class="playground-tool-state is-failed">失败</span>
                    </div>
                    <div v-if="tool.argsText" class="playground-tool-args">{{ tool.argsText }}</div>
                    <div v-if="tool.summary" class="playground-tool-summary">{{ tool.summary }}</div>
                  </div>
                </div>
              </div>
              <div v-if="item.content" class="playground-bubble playground-bubble-assistant">
                <PlaygroundMarkdown :content="item.content" />
              </div>
              <div v-if="item.streaming && !item.content" class="playground-streaming-hint">生成中…</div>
            </div>
          </template>
          <a-alert
            v-if="streamError"
            class="playground-error-bar"
            type="error"
            show-icon
            :message="streamError.message"
            :description="streamError.code ? `错误码：${streamError.code}` : undefined"
          />
        </div>
        <div class="playground-composer">
          <div class="playground-composer-box">
            <a-textarea
              v-model:value="draft"
              class="playground-composer-input"
              :rows="3"
              :bordered="false"
              placeholder="输入消息"
              @keydown="handleComposerKeydown"
            />
            <div class="playground-composer-bar">
              <div class="playground-composer-tools">
                <a-popconfirm
                  title="清空当前对话？消息只存在本页内存中，清空后不可恢复。"
                  ok-text="清空"
                  :ok-button-props="{ danger: true }"
                  cancel-text="取消"
                  @confirm="clearConversation"
                >
                  <a-tooltip title="清空对话">
                    <button type="button" class="playground-composer-tool" :disabled="!messages.length || sending">
                      <DeleteOutlined />
                    </button>
                  </a-tooltip>
                </a-popconfirm>
                <span class="playground-composer-hint">Enter 发送，Shift + Enter 换行</span>
              </div>
              <a-button
                class="playground-composer-send"
                type="primary"
                shape="circle"
                :loading="sending"
                :disabled="!canSend"
                @click="send"
              >
                <template #icon>
                  <ArrowUpOutlined />
                </template>
              </a-button>
            </div>
          </div>
        </div>
      </section>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { ArrowUpOutlined, DeleteOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { PlaygroundStreamHttpError } from '@/api/domains/playground'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { PlaygroundChatMessage, PlaygroundStreamEvent } from '@/types/domain/playground'
import PlaygroundMarkdown from './PlaygroundMarkdown.vue'
import { usePlaygroundCatalog } from './usePlaygroundCatalog'

// 对话试玩（演练场设计 §4.1）：一次性对话——消息只存在于本页内存（无本地持久化、
// 无会话落库、无断线续传），刷新即空；SSE delta 实时透出即打字机效果。

interface PlaygroundToolStep {
  callId: string
  name: string
  argsText?: string
  status: 'running' | 'ok' | 'failed'
  summary?: string
}

interface PlaygroundChatItem {
  role: 'user' | 'assistant'
  content: string
  tools: PlaygroundToolStep[]
  usage?: { promptTokens: number; completionTokens: number; totalTokens: number }
  rounds?: number
  streaming?: boolean
}

interface PlaygroundStreamError {
  code?: string
  message: string
}

const {
  accountsLoading,
  accountId,
  modelId,
  currentModel,
  accountOptions,
  modelOptions,
  handleAccountChange,
  loadCatalog
} = usePlaygroundCatalog('chat')

const system = ref('')
const temperature = ref(0.7)
const maxTokens = ref<number>()
const stream = ref(true)
const topP = ref(1)
const frequencyPenalty = ref(0)
const presencePenalty = ref(0)

const messages = ref<PlaygroundChatItem[]>([])
const draft = ref('')
const streamError = ref<PlaygroundStreamError>()
const sending = ref(false)
const messageListEl = ref<HTMLElement>()

const canSend = computed(() => Boolean(draft.value.trim() && accountId.value && modelId.value && !sending.value))

onMounted(() => {
  void loadCatalog()
})

function handleComposerKeydown(event: KeyboardEvent): void {
  // Enter 发送,Shift+Enter 换行(2026-11-01 用户裁决);Ctrl/Cmd+Enter 保留同义发送。
  if (event.key !== 'Enter') return
  if (event.shiftKey) return
  event.preventDefault()
  void send()
}

async function send(): Promise<void> {
  if (sending.value) return
  const content = draft.value.trim()
  if (!content) {
    message.warning('请输入要发送的内容')
    return
  }
  if (!accountId.value || !modelId.value) {
    message.warning('请先选择账户与模型')
    return
  }
  // 全量上下文由客户端携带（服务端零状态）：只回放可见的 user/assistant 正文。
  const history: PlaygroundChatMessage[] = []
  for (const item of messages.value) {
    if (item.role === 'user') history.push({ role: 'user', content: item.content })
    else if (item.content) history.push({ role: 'assistant', content: item.content })
  }
  history.push({ role: 'user', content })

  messages.value.push({ role: 'user', content, tools: [] })
  draft.value = ''
  const assistant: PlaygroundChatItem = { role: 'assistant', content: '', tools: [], streaming: true }
  messages.value.push(assistant)
  streamError.value = undefined
  sending.value = true
  scrollToBottom()
  try {
    await api.playground.chatStream({
      accountId: accountId.value,
      modelId: modelId.value,
      messages: history,
      system: system.value.trim() || undefined,
      temperature: temperature.value,
      maxTokens: maxTokens.value ?? null,
      stream: true,
      topP: topP.value,
      frequencyPenalty: frequencyPenalty.value,
      presencePenalty: presencePenalty.value,
      onEvent: (event: PlaygroundStreamEvent) => applyStreamEvent(assistant, event)
    })
  } catch (error) {
    console.error(error)
    streamError.value = error instanceof PlaygroundStreamHttpError
      ? { code: error.code, message: error.message }
      : { message: extractApiErrorMessage(error, '对话请求失败，请稍后重试') }
  } finally {
    assistant.streaming = false
    sending.value = false
    scrollToBottom()
  }
}

function applyStreamEvent(item: PlaygroundChatItem, event: PlaygroundStreamEvent): void {
  switch (event.type) {
    case 'round':
      item.rounds = event.round
      break
    case 'delta':
      item.content += event.text
      scrollToBottom()
      break
    case 'tool_start': {
      // 后端 wire 的 args 是模型产出的 arguments JSON 字符串（sse.go Args 原文）：
      // 解析成功按对象字段规整展示，解析失败原样展示字符串（不静默丢弃原文）。
      let argsText: string | undefined
      if (event.args) {
        try {
          argsText = JSON.stringify(JSON.parse(event.args))
        } catch {
          argsText = event.args
        }
      }
      item.tools.push({
        callId: event.callId,
        name: event.name,
        argsText,
        status: 'running'
      })
      scrollToBottom()
      break
    }
    case 'tool_end': {
      const step = [...item.tools].reverse().find((tool) => tool.callId === event.callId)
      if (step) {
        step.status = event.ok ? 'ok' : 'failed'
        step.summary = event.summary
      }
      scrollToBottom()
      break
    }
    case 'usage':
      item.usage = {
        promptTokens: event.promptTokens,
        completionTokens: event.completionTokens,
        totalTokens: event.totalTokens
      }
      break
    case 'done':
      item.rounds = event.rounds
      break
    case 'error':
      streamError.value = { code: event.code, message: event.message }
      break
  }
}

function clearConversation(): void {
  messages.value = []
  streamError.value = undefined
}

function scrollToBottom(): void {
  void nextTick(() => {
    const el = messageListEl.value
    if (el) el.scrollTop = el.scrollHeight
  })
}
</script>

<style scoped>
/* 卡片体（responsive-page-card）是固定视口高的 flex 容器：布局撑满剩余空间，
   左栏与消息区各自内部滚动（对照参考站 playgroud 布局口径）。 */
.playground-chat-layout {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: 300px minmax(0, 1fr);
  grid-template-rows: minmax(0, 1fr);
  gap: 24px;
}

.playground-side {
  min-width: 0;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding-right: 4px;
}

/* 左栏节奏：表单项间距与 label 间距沿用 antd vertical 默认（与参考站一致），
   仅弱化最大生成长度数字输入的边框存在感。 */
.playground-side :deep(.ant-input-number) {
  border-color: var(--juhe-border);
  background: var(--juhe-surface-soft);
}

.playground-side :deep(.ant-input-number:hover) {
  border-color: var(--juhe-border-strong);
}

.playground-model-hint {
  margin-top: 6px;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 1.5;
}

.playground-toggle-label {
  cursor: help;
}

.playground-inline-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.playground-advanced-collapse {
  margin-bottom: 12px;
}

.playground-conversation {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}

/* 消息区直接铺在卡片面上（无独立边框/底色），flex 撑满右栏，空态随之垂直居中 */
.playground-messages {
  flex: 1;
  min-height: 0;
  padding: 4px 4px 12px;
  overflow-y: auto;
  overscroll-behavior: contain;
}

.playground-empty {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}

.playground-message-user {
  display: flex;
  justify-content: flex-end;
  margin: 0 4px 12px;
}

.playground-message-assistant {
  margin: 0 4px 12px;
}

.playground-bubble {
  max-width: 76%;
  padding: 8px 12px;
  border-radius: 10px;
  font-size: 14px;
  line-height: 1.7;
  overflow-wrap: anywhere;
}

.playground-bubble-user {
  background: var(--juhe-accent-soft);
  white-space: pre-wrap;
}

.playground-bubble-assistant {
  max-width: 100%;
  /* AI 回复无背景（2026-11-01 用户裁决）：与页面同底色，去气泡底与圆角。 */
  background: transparent;
  border-radius: 0;
  padding: 0;
}

.playground-streaming-hint {
  margin-top: 6px;
  color: var(--juhe-faint);
  font-size: 12px;
}

/* 工具时间线：浅色小卡片，弱于正文气泡存在感 */
.playground-tool-timeline {
  max-width: 76%;
  margin-bottom: 8px;
  padding: 8px 10px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: var(--juhe-surface-soft);
}

.playground-tool-step {
  display: flex;
  gap: 8px;
  align-items: flex-start;
}

.playground-tool-dot {
  flex: none;
  width: 8px;
  height: 8px;
  margin-top: 6px;
  border-radius: 50%;
  background: var(--juhe-faint);
}

.playground-tool-dot.is-running {
  background: var(--juhe-warn);
}

.playground-tool-dot.is-ok {
  background: var(--juhe-ok);
}

.playground-tool-dot.is-failed {
  background: var(--juhe-danger);
}

.playground-tool-body {
  min-width: 0;
  font-size: 12px;
  line-height: 1.6;
}

.playground-tool-title {
  color: var(--juhe-fg-soft);
  font-weight: 600;
}

.playground-tool-state {
  margin-left: 6px;
  color: var(--juhe-faint);
  font-weight: 400;
}

.playground-tool-state.is-ok {
  color: var(--juhe-ok);
}

.playground-tool-state.is-failed {
  color: var(--juhe-danger);
}

.playground-tool-args {
  margin-top: 2px;
  color: var(--juhe-muted);
  word-break: break-all;
}

.playground-tool-summary {
  margin-top: 2px;
  color: var(--juhe-muted);
}

.playground-error-bar {
  margin: 12px 4px 0;
}

.playground-composer {
  margin-top: 12px;
}

/* 一体化输入区：textarea 与工具行同处一个圆角容器，容器统一承担边框与聚焦态 */
.playground-composer-box {
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius);
  background: var(--juhe-surface);
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
}

.playground-composer-box:focus-within {
  border-color: var(--juhe-accent-mid);
  box-shadow: 0 0 0 3px rgba(83, 105, 107, 0.08);
}

.playground-composer-box .playground-composer-input {
  display: block;
  width: 100%;
  padding: 10px 12px 2px;
  background: transparent;
  border: none;
  box-shadow: none;
  font-size: 14px;
  line-height: 1.6;
  resize: none;
}

.playground-composer-box .playground-composer-input:focus,
.playground-composer-box .playground-composer-input.ant-input-focused {
  border: none;
  box-shadow: none;
}

.playground-composer-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 8px 8px 12px;
}

.playground-composer-tools {
  display: flex;
  align-items: center;
  gap: 10px;
}

.playground-composer-tool {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--juhe-muted);
  font-size: 15px;
  cursor: pointer;
  transition: background 0.15s ease, color 0.15s ease;
}

.playground-composer-tool:hover:not(:disabled) {
  background: var(--juhe-accent-soft);
  color: var(--juhe-fg);
}

.playground-composer-tool:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.playground-composer-hint {
  font-size: 12px;
  color: var(--juhe-faint);
}

/* 压缩 antd 默认 40px 圆形按钮到工具行尺度 */
.playground-composer-send {
  width: 34px !important;
  height: 34px !important;
  min-width: 34px !important;
  padding: 0 !important;
}

.playground-composer-send :deep(.anticon) {
  font-size: 16px;
}

@media (max-width: 992px) {
  .playground-chat-layout {
    grid-template-columns: 1fr;
    grid-template-rows: none;
    overflow-y: auto;
  }

  .playground-side {
    overflow: visible;
  }

  .playground-messages {
    flex: none;
    height: 380px;
  }

  .playground-bubble,
  .playground-tool-timeline {
    max-width: 100%;
  }
}
</style>

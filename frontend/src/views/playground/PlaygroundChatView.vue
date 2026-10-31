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
          <a-form-item required>
            <template #label>
              <span class="playground-model-label">
                模型
                <a-tag v-if="currentModel?.functionCalling" class="playground-tool-tag" title="该模型声明 function_calling，后端默认注入 fetch_url 工具">工具:fetch_url</a-tag>
              </span>
            </template>
            <a-select
              v-model:value="modelId"
              :options="modelOptions"
              :disabled="!accountId || !modelOptions.length"
              show-search
              option-filter-prop="label"
              :placeholder="accountId ? '选择 chat 模型' : '先选择账户'"
            />
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
              <span>流式输出</span>
              <a-switch v-model:checked="stream" disabled />
            </div>
            <div class="playground-field-hint">演练场对话固定流式输出（后端仅支持 stream: true）</div>
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
          <a-popconfirm
            title="清空当前对话？消息只存在本页内存中，清空后不可恢复。"
            ok-text="清空"
            :ok-button-props="{ danger: true }"
            cancel-text="取消"
            @confirm="clearConversation"
          >
            <a-button danger block :disabled="sending">清空对话</a-button>
          </a-popconfirm>
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
              <div v-if="item.usage || item.rounds" class="playground-usage-row">
                <span v-if="item.usage">输入 {{ item.usage.promptTokens }} · 输出 {{ item.usage.completionTokens }} · 共 {{ item.usage.totalTokens }} tokens</span>
                <span v-if="item.rounds">{{ item.rounds }} 轮派发</span>
              </div>
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
          <a-textarea
            v-model:value="draft"
            :rows="3"
            placeholder="输入消息，Ctrl+Enter 发送，Enter 换行"
            @keydown="handleComposerKeydown"
          />
          <div class="playground-composer-footer">
            <span class="playground-field-hint">消息只保存在当前页面内存中，刷新或清空后即消失</span>
            <a-button type="primary" :loading="sending" :disabled="!canSend" @click="send">发送</a-button>
          </div>
        </div>
      </section>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
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
  if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
    event.preventDefault()
    void send()
  }
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
.playground-chat-layout {
  display: grid;
  grid-template-columns: 320px minmax(0, 1fr);
  gap: 24px;
}

.playground-side {
  min-width: 0;
}

.playground-model-label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.playground-tool-tag {
  margin-inline-end: 0;
  font-size: 11px;
  line-height: 18px;
}

.playground-inline-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.playground-field-hint {
  margin-top: 4px;
  color: var(--juhe-faint);
  font-size: 12px;
}

.playground-advanced-collapse {
  margin-bottom: 12px;
}

.playground-conversation {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.playground-messages {
  height: 480px;
  padding: 4px 2px 12px;
  overflow-y: auto;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: var(--juhe-rail);
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
  margin: 12px 12px 0;
}

.playground-message-assistant {
  margin: 12px 12px 0;
}

.playground-bubble {
  max-width: 84%;
  padding: 9px 13px;
  border-radius: 10px;
  font-size: 14px;
  line-height: 1.7;
  overflow-wrap: anywhere;
}

.playground-bubble-user {
  background: var(--juhe-accent-soft);
  border: 1px solid var(--juhe-border);
  white-space: pre-wrap;
}

.playground-bubble-assistant {
  max-width: 100%;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
}

.playground-streaming-hint {
  margin-top: 6px;
  color: var(--juhe-faint);
  font-size: 12px;
}

.playground-tool-timeline {
  max-width: 100%;
  margin-bottom: 8px;
  padding: 8px 10px;
  border: 1px dashed var(--juhe-border-strong);
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

.playground-usage-row {
  display: flex;
  gap: 12px;
  margin-top: 6px;
  color: var(--juhe-faint);
  font-size: 12px;
}

.playground-error-bar {
  margin: 12px 12px 0;
}

.playground-composer {
  margin-top: 12px;
}

.playground-composer-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 8px;
}

.playground-composer-footer .playground-field-hint {
  margin-top: 0;
}

@media (max-width: 992px) {
  .playground-chat-layout {
    grid-template-columns: 1fr;
  }

  .playground-messages {
    height: 380px;
  }

  .playground-bubble {
    max-width: 100%;
  }
}
</style>

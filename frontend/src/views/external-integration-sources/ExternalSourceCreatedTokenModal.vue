<template>
  <a-modal
    :open="open"
    title="来源授权已创建"
    width="600px"
    :footer="null"
    :mask-closable="false"
    @cancel="emit('close')"
  >
    <div class="created-token-guide">
      <a-alert
        class="created-token-alert"
        type="success"
        show-icon
        message="生产 Token 已生成"
        description="请复制后保存到外部系统后端；后续可在列表按权限复制完整 Token，不要放进前端包或公开文档。"
      />
      <div class="created-token-guide-section">
        <span class="created-token-step-title">1. 复制 Base URL</span>
        <div class="created-token-copy-row">
          <span class="created-token-label">Base URL</span>
          <code class="created-token-value">{{ publicApiBaseUrl }}</code>
          <a-button type="text" size="small" @click="copyPublicApiBaseUrl">
            <template #icon><copy-outlined /></template>
            复制
          </a-button>
        </div>
      </div>
      <div class="created-token-guide-section">
        <span class="created-token-step-title">2. 保存生产 Token</span>
        <a-input-group compact class="created-token-input">
          <a-input :value="token" readonly />
          <a-button type="primary" @click="copyCreatedToken">复制</a-button>
        </a-input-group>
      </div>
      <div class="created-token-guide-section">
        <span class="created-token-step-title">3. 配置请求头</span>
        <pre class="created-token-code">{{ createdTokenAuthHeader }}</pre>
        <a-button size="small" @click="copyCreatedTokenAuthHeader">
          <template #icon><copy-outlined /></template>
          复制认证头
        </a-button>
      </div>
      <div class="created-token-actions">
        <a-button @click="emit('open-docs')">查看接入文档</a-button>
        <a-button type="primary" @click="emit('close')">我已保存</a-button>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CopyOutlined } from '@ant-design/icons-vue'

import { copyTextToClipboard } from '@/shared/clipboard'

const props = defineProps<{
  open: boolean
  publicApiBaseUrl: string
  token: string
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'open-docs'): void
}>()

const createdTokenAuthHeader = computed(() => `Authorization: Bearer ${props.token || '<source_token>'}`)

function copyCreatedToken(): void {
  void copyTextToClipboard(props.token, 'Token 已复制')
}

function copyCreatedTokenAuthHeader(): void {
  void copyTextToClipboard(createdTokenAuthHeader.value, '认证头已复制')
}

function copyPublicApiBaseUrl(): void {
  void copyTextToClipboard(props.publicApiBaseUrl, 'Base URL 已复制')
}
</script>

<style scoped>
.created-token-alert {
  margin-bottom: 0;
}

.created-token-guide {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.created-token-guide-section {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 10px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 12px;
  background: #fbfdff;
}

.created-token-step-title {
  color: #0f172a;
  font-size: 14px;
  font-weight: 700;
}

.created-token-copy-row {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.created-token-label {
  flex: none;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
}

.created-token-value {
  min-width: 0;
  flex: 1;
  padding: 4px 10px;
  overflow: hidden;
  color: #0f766e;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 6px;
  background: #ecfeff;
}

.created-token-input {
  display: flex;
  margin-top: 2px;
}

.created-token-input :deep(.ant-input) {
  flex: 1;
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', Menlo, monospace;
}

.created-token-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.created-token-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 720px) {
  .created-token-copy-row,
  .created-token-actions {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>

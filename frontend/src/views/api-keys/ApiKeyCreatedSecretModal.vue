<template>
  <a-modal v-model:open="open" :title="title" width="720px" :footer="null">
    <div class="created-key-guide">
      <a-alert class="created-key-alert" :message="message" type="success" show-icon />

      <div class="created-key-step">
        <span class="created-key-step-index">1</span>
        <div class="created-key-step-body">
          <div class="created-key-step-header">
            <strong>复制 Base URL</strong>
            <span>填到 OpenAI 兼容客户端的 Base URL 配置项。</span>
          </div>
          <div class="created-key-copy-row">
            <span class="created-key-label">Base URL</span>
            <code class="created-key-value">{{ gatewayBaseUrl }}</code>
            <a-button class="created-key-copy-button" type="text" size="small" @click="$emit('copy-gateway-base-url')">
              <template #icon><copy-outlined /></template>
              复制
            </a-button>
          </div>
        </div>
      </div>

      <div class="created-key-step">
        <span class="created-key-step-index">2</span>
        <div class="created-key-step-body">
          <div class="created-key-step-header">
            <strong>保存完整 API Key</strong>
            <span>客户端鉴权只填写密钥本身，不需要手动加 Bearer 前缀。</span>
          </div>
          <a-input-group compact class="created-key-secret">
            <a-input :value="apiKey" readonly style="width: calc(100% - 96px)" />
            <a-button type="primary" @click="$emit('copy-api-key')">
              <template #icon><copy-outlined /></template>
              复制
            </a-button>
          </a-input-group>
        </div>
      </div>

      <div class="created-key-step">
        <span class="created-key-step-index">3</span>
        <div class="created-key-step-body">
          <div class="created-key-step-header">
            <strong>发送最小 HTTP 请求</strong>
            <span>已按 {{ minimalHttpRequestPlatformLabel }} 生成；用完整密钥请求模型列表，确认本地网关可用。</span>
          </div>
          <pre class="created-key-code">{{ minimalHttpRequestExample }}</pre>
          <div class="created-key-actions">
            <a-button @click="$emit('copy-minimal-http-request')">
              <template #icon><copy-outlined /></template>
              复制请求示例
            </a-button>
          </div>
        </div>
      </div>

      <a-alert
        class="created-key-tip"
        type="info"
        show-icon
        message="如果连接失败，请先检查 API Key 是否启用、是否绑定可用分组，以及分组内账号是否正常。"
      />
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'

defineProps<{
  apiKey: string
  gatewayBaseUrl: string
  message: string
  minimalHttpRequestExample: string
  minimalHttpRequestPlatformLabel: string
  title: string
}>()

defineEmits<{
  'copy-api-key': []
  'copy-gateway-base-url': []
  'copy-minimal-http-request': []
}>()

const open = defineModel<boolean>('open', { required: true })
</script>

<style scoped>
.created-key-guide {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.created-key-alert,
.created-key-tip {
  border-radius: 8px;
}

.created-key-step {
  display: grid;
  grid-template-columns: 30px minmax(0, 1fr);
  gap: 12px;
  padding: 14px;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  background: #fbfdff;
}

.created-key-step-index {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  color: #1677ff;
  font-size: 14px;
  font-weight: 700;
  border-radius: 50%;
  background: #e6f4ff;
}

.created-key-step-body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 0;
}

.created-key-step-header {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.created-key-step-header strong {
  color: #0f172a;
  font-size: 15px;
}

.created-key-step-header span {
  color: #64748b;
  font-size: 13px;
  line-height: 1.6;
}

.created-key-copy-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.created-key-secret {
  width: 100%;
}

.created-key-label {
  flex: none;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
}

.created-key-value {
  min-width: 0;
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

.created-key-copy-button {
  flex: none;
}

.created-key-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.7;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.8);
}

.created-key-actions {
  display: flex;
  justify-content: flex-end;
}

@media (max-width: 640px) {
  .created-key-step {
    grid-template-columns: 1fr;
  }

  .created-key-copy-row,
  .created-key-actions {
    align-items: stretch;
    flex-direction: column;
  }

  .created-key-value {
    white-space: normal;
    word-break: break-all;
  }
}
</style>

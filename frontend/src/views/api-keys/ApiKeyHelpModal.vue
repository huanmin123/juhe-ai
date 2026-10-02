<template>
  <a-modal v-model:open="open" title="API Key 接入帮助" width="560px" :footer="null">
    <div class="gateway-help-content">
      <div class="gateway-help-section">
        <span class="gateway-step-title">1. 复制 Base URL</span>
        <div class="gateway-url-row">
          <span class="gateway-url-label">Base URL</span>
          <span class="gateway-url-value">{{ gatewayBaseUrl }}</span>
          <a-button class="gateway-copy-button" type="text" size="small" @click="$emit('copy-base-url')">
            <template #icon><copy-outlined /></template>
            复制
          </a-button>
        </div>
      </div>
      <div class="gateway-help-section">
        <span class="gateway-step-title">2. 复制 API Key</span>
        <span>列表显示前8位和后8位用于识别，可通过复制按钮复制完整密钥。</span>
      </div>
      <div class="gateway-help-section">
        <span class="gateway-step-title">3. 填到客户端</span>
        <pre class="gateway-code">{{ gatewayClientExample }}</pre>
      </div>
      <a-alert class="gateway-help-note" type="info" show-icon message="Responses 是连续会话入口；/chat/completions 按上游公开接口能力处理。统计、会话亲和和缓存不按 OAuth / API Key 类型拆分。" />
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'

defineProps<{
  gatewayBaseUrl: string
  gatewayClientExample: string
}>()

defineEmits<{
  'copy-base-url': []
}>()

const open = defineModel<boolean>('open', { required: true })
</script>

<style scoped>
.gateway-help-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.gateway-help-section {
  padding: 14px;
  border: 1px solid var(--juhe-border);
  border-radius: 12px;
  background: var(--juhe-surface-soft);
}

.gateway-help-note {
  border-radius: 8px;
}

.gateway-step-title {
  color: var(--juhe-fg);
  font-size: 15px;
  font-weight: 700;
}

.gateway-url-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.gateway-url-label {
  flex: none;
  color: var(--juhe-muted);
  font-size: 12px;
  font-weight: 600;
}

.gateway-url-value {
  min-width: 0;
  padding: 4px 10px;
  overflow: hidden;
  color: var(--juhe-ok);
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 6px;
  background: var(--juhe-ok-soft);
}

.gateway-copy-button {
  flex: none;
}

.gateway-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  color: var(--juhe-fg-soft);
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  border: 1px solid var(--juhe-border);
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.8);
}
</style>

<template>
  <a-modal v-model:open="open" :title="title" width="640px" :footer="null">
    <a-alert :message="message" type="info" show-icon />
    <div class="created-key-base-url">
      <span class="created-key-label">Base URL</span>
      <span class="created-key-value">{{ gatewayBaseUrl }}</span>
      <a-button type="link" size="small" @click="$emit('copy-gateway-base-url')">复制</a-button>
    </div>
    <a-input-group compact class="created-key">
      <a-input :value="apiKey" readonly style="width: calc(100% - 88px)" />
      <a-button type="primary" @click="$emit('copy-api-key')">复制</a-button>
    </a-input-group>
  </a-modal>
</template>

<script setup lang="ts">
defineProps<{
  apiKey: string
  gatewayBaseUrl: string
  message: string
  title: string
}>()

defineEmits<{
  'copy-api-key': []
  'copy-gateway-base-url': []
}>()

const open = defineModel<boolean>('open', { required: true })
</script>

<style scoped>
.created-key {
  margin-top: 16px;
}

.created-key-base-url {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  margin-top: 16px;
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
</style>

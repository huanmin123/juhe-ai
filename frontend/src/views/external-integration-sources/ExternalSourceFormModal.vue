<template>
  <a-modal
    :open="open"
    :title="editing ? '编辑来源授权' : '新增授权'"
    width="760px"
    :confirm-loading="saving"
    ok-text="保存"
    cancel-text="取消"
    :ok-button-props="{ disabled: saving }"
    @ok="emit('save')"
    @update:open="emit('update:open', $event)"
  >
    <a-form layout="vertical">
      <a-form-item label="授权名称" required>
        <a-input v-model:value="form.name" placeholder="例如 公益站生产授权" />
      </a-form-item>
      <a-form-item label="状态">
        <a-select v-model:value="form.status" :options="externalSourceStatusOptions" />
      </a-form-item>
      <a-form-item label="接口资源授权">
        <a-select v-model:value="form.scopes" mode="multiple" :options="scopeOptions" placeholder="选择允许调用的公开接口" />
      </a-form-item>
      <a-form-item label="到期时间">
        <a-date-picker v-model:value="form.expiresAt" class="full-control" show-time allow-clear />
      </a-form-item>
      <a-form-item label="限频规则">
        <div class="rate-limit-list">
          <div v-for="(rule, index) in form.rateLimits" :key="index" class="rate-limit-row">
            <a-input-number v-model:value="rule.windowSeconds" :min="1" :max="86400" :precision="0" addon-after="秒内" />
            <a-input-number v-model:value="rule.maxRequests" :min="1" :max="100000" :precision="0" addon-after="次" />
            <a-button danger @click="emit('remove-rate-limit', index)">删除</a-button>
          </div>
          <a-button @click="emit('add-rate-limit')">新增限频规则</a-button>
          <span v-if="!form.rateLimits.length" class="muted-cell">默认不限制。</span>
        </div>
      </a-form-item>
      <a-form-item label="备注">
        <a-textarea v-model:value="form.notes" :rows="3" :maxlength="500" show-count />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import type { ExternalIntegrationScopeOption } from '@/types/domain'
import { externalSourceStatusOptions, type ExternalSourceForm } from './externalSourceFormModel'

defineProps<{
  editing: boolean
  form: ExternalSourceForm
  open: boolean
  saving: boolean
  scopeOptions: ExternalIntegrationScopeOption[]
}>()

const emit = defineEmits<{
  (event: 'add-rate-limit'): void
  (event: 'remove-rate-limit', index: number): void
  (event: 'save'): void
  (event: 'update:open', value: boolean): void
}>()
</script>

<style scoped>
.full-control {
  width: 100%;
}

.rate-limit-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.rate-limit-row {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) minmax(120px, 1fr) auto;
  gap: 8px;
}

@media (max-width: 720px) {
  .rate-limit-row {
    grid-template-columns: 1fr;
  }
}
</style>

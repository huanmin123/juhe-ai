<template>
  <div class="quota-limit-grid">
    <div class="quota-limit-item quota-limit-hourly">
      <a-switch v-model:checked="model.hourly.enabled" />
      <span class="quota-limit-title">n 小时额度</span>
      <a-input-number v-model:value="model.hourly.hours" :min="1" :max="720" addon-after="小时" :disabled="!model.hourly.enabled" class="quota-hours-input" />
      <a-input-number v-model:value="model.hourly.limit" :min="1" addon-after="次请求" :disabled="!model.hourly.enabled" class="quota-limit-input" />
    </div>
    <div v-for="item in quotaLimitItems" :key="item.key" class="quota-limit-item">
      <a-switch v-model:checked="model[item.key].enabled" />
      <span class="quota-limit-title">{{ item.label }}</span>
      <a-input-number v-model:value="model[item.key].limit" :min="1" addon-after="次请求" :disabled="!model[item.key].enabled" class="quota-limit-input" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { quotaLimitItems, type RequestQuotaFormModel } from './requestQuotaForm'

defineProps<{
  model: RequestQuotaFormModel
}>()
</script>

<style scoped>
.quota-limit-grid {
  display: grid;
  gap: 10px;
}

.quota-limit-item {
  display: grid;
  grid-template-columns: auto minmax(150px, 1fr) minmax(160px, 220px);
  gap: 10px;
  align-items: center;
  padding: 10px 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fbfdff;
}

.quota-limit-hourly {
  grid-template-columns: auto minmax(110px, 1fr) minmax(110px, 140px) minmax(160px, 220px);
}

.quota-limit-title {
  min-width: 0;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.quota-limit-input,
.quota-hours-input {
  width: 100%;
}

@media (max-width: 640px) {
  .quota-limit-item,
  .quota-limit-hourly {
    grid-template-columns: auto minmax(0, 1fr);
  }

  .quota-limit-input,
  .quota-hours-input {
    grid-column: 1 / -1;
  }
}
</style>

<template>
  <section v-if="visible" class="account-config-section">
    <div class="account-config-section-title">上游余额查询</div>
    <a-form-item label="启用查询">
      <a-switch v-model:checked="form.balanceQueryEnabled" :disabled="readonly" />
    </a-form-item>
    <template v-if="form.balanceQueryEnabled">
      <div class="balance-query-grid">
        <a-form-item label="查询类型">
          <a-select v-model:value="form.balanceQueryAdapter" :disabled="readonly" :options="adapterOptions" />
        </a-form-item>
        <a-form-item label="刷新周期">
          <a-input-number v-model:value="form.balanceQueryIntervalMinutes" :disabled="readonly" :min="1" :max="10" :precision="0" addon-after="分钟" />
        </a-form-item>
      </div>
      <template v-if="form.balanceQueryAdapter === 'custom'">
        <a-form-item label="接口路径">
          <a-input v-model:value="form.balanceQueryCustomPath" :disabled="readonly" placeholder="/api/balance" />
        </a-form-item>
        <div class="balance-query-grid">
          <a-form-item label="余额字段">
            <a-input v-model:value="form.balanceQueryRemainingPointer" :disabled="readonly" placeholder="/data/remaining" />
          </a-form-item>
          <a-form-item label="金额除数">
            <a-input v-model:value="form.balanceQueryDivisor" :disabled="readonly" placeholder="1" />
          </a-form-item>
          <a-form-item label="总额字段">
            <a-input v-model:value="form.balanceQueryTotalPointer" :disabled="readonly" placeholder="/data/total" />
          </a-form-item>
          <a-form-item label="已用字段">
            <a-input v-model:value="form.balanceQueryUsedPointer" :disabled="readonly" placeholder="/data/used" />
          </a-form-item>
        </div>
      </template>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { AccountFormModel } from './accountFormTypes'

const props = defineProps<{ form: AccountFormModel; readonly?: boolean }>()

const visible = computed(() => props.form.type === 'api_key' && props.form.apiKeys.map((item) => item.trim()).filter(Boolean).length === 1)
const adapterOptions = [
  { label: 'Sub2API', value: 'sub2api' },
  { label: 'New API', value: 'newapi' },
  { label: 'LiteLLM', value: 'litellm' },
  { label: '自定义接口', value: 'custom' }
]
</script>

<style scoped>
.account-config-section {
  padding-top: 4px;
}

.account-config-section-title {
  margin-bottom: 12px;
  color: #1f2937;
  font-weight: 600;
}

.balance-query-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

@media (max-width: 720px) {
  .balance-query-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

<template>
  <section v-if="visible" class="account-config-section">
    <div class="balance-query-header">
      <div class="account-config-section-title">
        <span>上游余额查询</span>
        <a-tooltip title="保存后由后台按刷新周期更新；仅验证当前配置，不会保存。">
          <QuestionCircleOutlined class="balance-query-help" />
        </a-tooltip>
      </div>
      <a-switch v-model:checked="form.balanceQueryEnabled" :disabled="readonly" />
    </div>
    <template v-if="form.balanceQueryEnabled">
      <div class="balance-query-grid">
        <a-form-item label="查询类型">
          <a-select v-model:value="form.balanceQueryAdapter" :disabled="readonly" :options="adapterOptions" />
        </a-form-item>
        <a-form-item label="刷新周期">
          <div class="balance-query-refresh-control">
            <a-input-number class="balance-query-interval-input" v-model:value="form.balanceQueryIntervalMinutes" :disabled="readonly" :min="1" :max="10" :precision="0" addon-after="分钟" />
            <a-button
              class="balance-query-query-button"
              html-type="button"
              :disabled="readonly || !canQuery"
              :loading="queryLoading"
              @click="emit('query')"
            >
              测试查询
            </a-button>
          </div>
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
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import type { AccountFormModel } from './accountFormTypes'
import { normalizedAccountApiKeys } from './accountCredentials'

const props = defineProps<{
  canQuery?: boolean
  form: AccountFormModel
  queryLoading?: boolean
  readonly?: boolean
}>()
const emit = defineEmits<{ (event: 'query'): void }>()

const visible = computed(() => props.form.type === 'api_key' && normalizedAccountApiKeys(props.form).length >= 1)
const adapterOptions = [
  { label: '内置适配', value: 'builtin' },
  { label: '自定义接口', value: 'custom' }
]
</script>

<style scoped>
.account-config-section {
  padding-top: 4px;
}

.account-config-section-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: #1f2937;
  font-weight: 600;
}

.balance-query-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 32px;
  margin-bottom: 12px;
}

.balance-query-help {
  color: #94a3b8;
  cursor: help;
  font-size: 13px;
}

.balance-query-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

.balance-query-refresh-control {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 8px;
  width: 100%;
}

.balance-query-refresh-control :deep(.ant-input-number-group-wrapper) {
  width: 100%;
}

.balance-query-interval-input {
  width: 100%;
}

@media (max-width: 720px) {
  .balance-query-grid {
    grid-template-columns: minmax(0, 1fr);
  }

}

@media (max-width: 480px) {
  .balance-query-refresh-control {
    grid-template-columns: minmax(0, 1fr);
  }

  .balance-query-query-button {
    justify-self: end;
  }
}
</style>

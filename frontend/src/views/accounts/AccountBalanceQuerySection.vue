<template>
  <section v-if="visible" class="account-config-section">
    <div class="balance-query-header">
      <div class="account-config-section-title">
        <span>上游余额查询</span>
        <a-tooltip title="保存后由后台按刷新周期更新；测试查询只验证当前配置，不会保存。">
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

      <div class="balance-query-test">
        <span>仅验证当前配置，不会保存</span>
        <a-button :disabled="readonly || !canQuery" :loading="queryLoading" @click="emit('query')">
          测试查询
        </a-button>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import type { AccountFormModel } from './accountFormTypes'

const props = defineProps<{
  canQuery?: boolean
  form: AccountFormModel
  queryLoading?: boolean
  readonly?: boolean
}>()
const emit = defineEmits<{ (event: 'query'): void }>()

const visible = computed(() => props.form.type === 'api_key' && props.form.apiKeys.map((item) => item.trim()).filter(Boolean).length === 1)
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

.balance-query-test {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 0;
  border-top: 1px solid #eef2f7;
}

.balance-query-test > span {
  color: #64748b;
  font-size: 12px;
}

@media (max-width: 720px) {
  .balance-query-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .balance-query-test {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>

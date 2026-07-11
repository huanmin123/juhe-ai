<template>
  <section v-if="visible" class="account-config-section">
    <div class="balance-query-header">
      <div class="account-config-section-title">
        <span>上游余额查询</span>
        <a-tooltip title="通过上游接口查询当前 API Key 的可用余额。开启后后台会按刷新周期更新，也可在保存账户后手动查询。">
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
        <div class="balance-query-test-copy">
          <strong>查询验证</strong>
          <span>{{ canQuery ? '使用当前已保存的余额配置请求上游。' : '新账户保存后可验证余额查询。' }}</span>
        </div>
        <a-button :disabled="readonly || !canQuery" :loading="queryLoading" @click="emit('query')">
          立即查询
        </a-button>
      </div>

      <a-alert
        v-if="queryResult"
        class="balance-query-result"
        :type="queryResult.tone === 'failed' ? 'error' : queryResult.tone === 'fresh' || queryResult.tone === 'unlimited' ? 'success' : 'info'"
        show-icon
        :message="`查询结果：${queryResult.text}`"
        :description="queryResultDescription"
      />
    </template>
  </section>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import type { AccountBalanceSnapshot } from '@/types/domain'
import { formatAccountBalance } from './accountBalanceQuery'
import type { AccountFormModel } from './accountFormTypes'

const props = defineProps<{
  canQuery?: boolean
  form: AccountFormModel
  queryLoading?: boolean
  querySnapshot?: AccountBalanceSnapshot
  readonly?: boolean
}>()
const emit = defineEmits<{ (event: 'query'): void }>()

const visible = computed(() => props.form.type === 'api_key' && props.form.apiKeys.map((item) => item.trim()).filter(Boolean).length === 1)
const queryResult = computed(() => props.querySnapshot ? formatAccountBalance(props.querySnapshot) : undefined)
const queryResultDescription = computed(() => {
  if (!props.querySnapshot) return undefined
  if (props.querySnapshot.status === 'failed') return props.querySnapshot.errorMessage || '上游余额查询失败'
  const attemptedAt = props.querySnapshot.lastAttemptAt
  return attemptedAt ? `查询时间：${formatDateTime(attemptedAt)}` : undefined
})
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

.balance-query-test-copy {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.balance-query-test-copy span {
  color: #64748b;
  font-size: 12px;
}

.balance-query-result {
  margin-top: 4px;
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

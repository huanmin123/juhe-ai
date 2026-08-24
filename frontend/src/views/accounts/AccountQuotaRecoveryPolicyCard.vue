<template>
  <section class="form-section quota-recovery-policy-card">
    <div class="quota-recovery-header">
      <div>
        <div class="quota-recovery-title">额度不足恢复策略</div>
        <div class="quota-recovery-help">
          {{ props.accountType === 'api_key'
            ? '支持该语义的 API Key 供应商明确返回 reset_at / Retry-After 时优先；以下策略用于没有明确恢复时间的额度不足响应。'
            : 'OAuth / Google OAuth 按下方账户策略恢复；API Key 的 reset_at / Retry-After 字段不适用于 OAuth。' }}
        </div>
      </div>
      <a-tag color="blue">{{ accountTypeLabel }}</a-tag>
    </div>

    <a-form-item label="恢复模式">
      <a-select v-model:value="schedule.reset_strategy" :disabled="readonly" style="width: 220px">
        <a-select-option value="duration">固定时长后恢复探测</a-select-option>
        <a-select-option value="daily">每日时间点恢复</a-select-option>
        <a-select-option value="weekly">每周时间点恢复</a-select-option>
      </a-select>
    </a-form-item>

    <a-form-item v-if="schedule.reset_strategy === 'duration'" label="恢复间隔（分钟）">
      <a-input-number v-model:value="schedule.duration_minutes" :disabled="readonly" :min="30" :max="10080" :precision="0" />
      <span class="quota-recovery-inline-help">建议 60；jitter_minutes固定15、实际0–15 分钟稳定错峰</span>
    </a-form-item>
    <a-form-item v-else-if="schedule.reset_strategy === 'daily'" label="每日恢复时间">
      <a-input-number v-model:value="schedule.daily_reset_hour" :disabled="readonly" :min="0" :max="23" :precision="0" />
      <span class="quota-recovery-inline-help">{{ schedule.timezone }} {{ String(schedule.daily_reset_hour).padStart(2, '0') }}:00，jitter_minutes固定15、实际0–15 分钟稳定错峰</span>
    </a-form-item>
    <a-form-item v-else label="每周恢复时间">
      <a-space>
        <a-select v-model:value="schedule.weekly_reset_day" :disabled="readonly" style="width: 120px">
          <a-select-option v-for="item in weekdays" :key="item.value" :value="item.value">{{ item.label }}</a-select-option>
        </a-select>
        <a-input-number v-model:value="schedule.weekly_reset_hour" :disabled="readonly" :min="0" :max="23" :precision="0" />
      </a-space>
    </a-form-item>

    <a-form-item label="时区">
      <a-input v-model:value="schedule.timezone" :disabled="readonly" placeholder="UTC 或 IANA 时区，例如 Asia/Shanghai" />
    </a-form-item>
    <a-form-item label="错峰范围（分钟）">
      <a-input-number v-model:value="schedule.jitter_minutes" disabled :min="15" :max="15" :precision="0" />
      <span class="quota-recovery-inline-help">jitter_minutes固定15、实际0–15 分钟稳定错峰，账户不能关闭或修改</span>
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import {
  defaultAccountQuotaRecoverySchedule,
  ensureAccountQuotaRecoverySchedule,
  type AccountQuotaRecoveryPolicyForm
} from './accountQuotaRecoveryPolicyTypes'

const props = withDefaults(defineProps<{
  accountType: string
  readonly?: boolean
}>(), { readonly: false })
const policy = defineModel<AccountQuotaRecoveryPolicyForm | undefined>('policy', { default: undefined })

function ensureSchedule() {
  if (!policy.value) policy.value = {}
  return ensureAccountQuotaRecoverySchedule(policy.value, props.accountType)
}
function scheduleKey() {
  return props.accountType === 'api_key' ? 'api_key' : props.accountType === 'google_oauth' ? 'google_oauth' : 'oauth'
}
const schedule = computed(() => policy.value?.[scheduleKey()] ?? defaultAccountQuotaRecoverySchedule(props.accountType))
const accountTypeLabel = computed(() => props.accountType === 'api_key' ? 'API Key' : props.accountType === 'google_oauth' ? 'Google OAuth' : 'OAuth')
const weekdays = [
  { value: 0, label: '周日' },
  { value: 1, label: '周一' },
  { value: 2, label: '周二' },
  { value: 3, label: '周三' },
  { value: 4, label: '周四' },
  { value: 5, label: '周五' },
  { value: 6, label: '周六' }
]

watch([policy, () => props.accountType], () => {
  ensureSchedule()
}, { immediate: true })
</script>

<style scoped>
.quota-recovery-policy-card {
  padding: 14px 16px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #fafafa;
}
.quota-recovery-header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}
.quota-recovery-title { font-weight: 600; color: #1f2937; }
.quota-recovery-help, .quota-recovery-inline-help { color: #6b7280; font-size: 12px; }
.quota-recovery-inline-help { margin-left: 10px; }
</style>

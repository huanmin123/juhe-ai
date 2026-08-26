<template>
  <div class="quota-recovery-policy-fields">
    <a-form-item label="恢复策略">
      <a-select :value="schedule.reset_strategy" :disabled="readonly" :options="accountErrorRecoveryStrategyOptions" style="width: 220px" @update:value="updateSchedule('reset_strategy', $event)" />
    </a-form-item>

    <a-form-item v-if="schedule.reset_strategy === 'duration'" label="恢复小时数">
      <a-input-number :value="Math.max(1, Math.round(schedule.duration_minutes / 60))" :disabled="readonly" :min="1" :max="720" :precision="0" @update:value="updateDurationHours" />
    </a-form-item>
    <a-form-item v-if="schedule.reset_strategy === 'daily'" label="每天恢复时间">
      <a-select :value="schedule.daily_reset_hour" :disabled="readonly" :options="accountErrorHourOptions" style="width: 180px" @update:value="updateSchedule('daily_reset_hour', $event)" />
    </a-form-item>
    <a-form-item v-if="schedule.reset_strategy === 'weekly'" label="每周恢复日">
      <a-select :value="schedule.weekly_reset_day" :disabled="readonly" :options="accountErrorWeekdayOptions" style="width: 180px" @update:value="updateSchedule('weekly_reset_day', $event)" />
    </a-form-item>
    <a-form-item v-if="schedule.reset_strategy === 'weekly'" label="每周恢复时间">
      <a-select :value="schedule.weekly_reset_hour" :disabled="readonly" :options="accountErrorHourOptions" style="width: 180px" @update:value="updateSchedule('weekly_reset_hour', $event)" />
    </a-form-item>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import {
  defaultAccountQuotaRecoverySchedule,
  type AccountQuotaRecoveryPolicyForm,
  type AccountQuotaRecoveryScheduleForm
} from './accountQuotaRecoveryPolicyTypes'
import { accountErrorHourOptions, accountErrorRecoveryStrategyOptions, accountErrorWeekdayOptions } from './accountErrorPolicyTypes'

const props = withDefaults(defineProps<{
  accountType: string
  effectiveQuotaRecoveryPolicy?: AccountQuotaRecoveryPolicyForm
  readonly?: boolean
}>(), { readonly: false, effectiveQuotaRecoveryPolicy: undefined })
const policy = defineModel<AccountQuotaRecoveryPolicyForm | undefined>('policy', { default: undefined })

function scheduleKey() {
  return props.accountType === 'api_key' ? 'api_key' : props.accountType === 'google_oauth' ? 'google_oauth' : 'oauth'
}
const effectiveSchedule = computed(() => props.effectiveQuotaRecoveryPolicy?.[scheduleKey()] ?? defaultAccountQuotaRecoverySchedule(props.accountType))
const schedule = computed(() => policy.value?.[scheduleKey()] ?? effectiveSchedule.value)

function ensureSchedule(): AccountQuotaRecoveryScheduleForm {
  const key = scheduleKey()
  if (!policy.value) policy.value = {}
  const current = policy.value[key]
  if (current) return current

  // The first edit must fork only the currently edited account type from the
  // displayed effective (global or inherited) schedule. Other account types
  // remain absent and therefore continue to inherit their own defaults.
  const created = { ...schedule.value, jitter_minutes: 15 }
  policy.value[key] = created
  return created
}

function updateSchedule<K extends keyof AccountQuotaRecoveryScheduleForm>(
  key: K,
  value: AccountQuotaRecoveryScheduleForm[K] | null
) {
  if (value === null) return
  ensureSchedule()[key] = value
}

function updateDurationHours(value: number | null) {
  if (value === null) return
  updateSchedule('duration_minutes', value * 60)
}

</script>

<style scoped>
</style>

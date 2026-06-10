<template>
  <TimeScheduleSection
    :form="form"
    :readonly="readonly"
    label="时间计划"
    readonly-label="来源时间计划"
    row-key-prefix="account_schedule_window"
  />
</template>

<script setup lang="ts">
import TimeScheduleSection from '@/views/shared/TimeScheduleSection.vue'

import type { AccountFormModel } from './accountFormTypes'

defineProps<{
  form: AccountFormModel
  readonly?: boolean
}>()
</script>

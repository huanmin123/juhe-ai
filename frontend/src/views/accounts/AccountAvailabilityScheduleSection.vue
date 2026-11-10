<template>
  <div class="account-availability-schedule">
    <TimeScheduleSection
      :form="form"
      :readonly="readonly"
      label="时间计划"
      readonly-label="来源时间计划"
      :help-message="scheduleHelpMessage"
      row-key-prefix="account_schedule_window"
      :peak-presets="peakPresets"
    />
    <!-- 时间计划闸门（时间计划闸门与账户状态解耦设计 §6）：时段外只暂停调度，不改写账户状态。 -->
    <p class="schedule-gate-note">{{ scheduleGateNote }}</p>
  </div>
</template>

<script setup lang="ts">
import TimeScheduleSection from '@/views/shared/TimeScheduleSection.vue'

import type { TimeSchedulePreset } from '@/views/shared/timeSchedule'
import type { AccountFormModel } from './accountFormTypes'

const scheduleGateNote = '时段外账户仍为正常状态，仅暂停调度；人工停用不会被计划自动恢复。'
const scheduleHelpMessage = `开启后只在配置的星期和时间段内参与调度；${scheduleGateNote}`

defineProps<{
  form: AccountFormModel
  readonly?: boolean
  /** 当前供应商的高峰快捷预设（来源：供应商目录 peakWindows），空 = 无高峰口径。 */
  peakPresets?: TimeSchedulePreset[]
}>()
</script>

<style scoped>
.account-availability-schedule {
  display: grid;
  gap: 8px;
}

.schedule-gate-note {
  margin: 0;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 1.6;
}
</style>

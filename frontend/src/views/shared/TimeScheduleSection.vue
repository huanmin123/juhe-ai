<template>
  <section class="time-schedule-section" :class="{ 'time-schedule-section-bordered': bordered }">
    <div class="schedule-toggle-row">
      <span class="schedule-toggle-label">
        <span>{{ readonly ? readonlyLabel : label }}</span>
        <a-tooltip :title="scheduleTooltipTitle">
          <QuestionCircleOutlined class="help-icon" />
        </a-tooltip>
      </span>
      <a-switch v-model:checked="form.availabilitySchedule.enabled" :disabled="readonly" />
    </div>
    <div v-if="form.availabilitySchedule.enabled" class="schedule-config">
      <div class="schedule-window-list">
        <div v-for="(window, index) in form.availabilitySchedule.windows" :key="window.key" class="schedule-window-row">
          <a-select
            v-model:value="window.daysOfWeek"
            mode="multiple"
            class="schedule-days-select"
            max-tag-count="responsive"
            :options="weekdayOptions"
            :disabled="readonly"
            placeholder="重复日期"
          />
          <a-time-picker v-model:value="window.start" format="HH:mm" value-format="HH:mm" class="schedule-time-picker" :disabled="readonly" placeholder="开始" />
          <a-time-picker v-model:value="window.end" format="HH:mm" value-format="HH:mm" class="schedule-time-picker" :disabled="readonly" placeholder="结束" />
          <a-tooltip title="移除">
            <a-button type="text" size="small" danger :disabled="readonly || form.availabilitySchedule.windows.length <= 1" @click="removeScheduleWindow(index)">
              <template #icon><DeleteOutlined /></template>
            </a-button>
          </a-tooltip>
        </div>
        <a-button v-if="!readonly" type="dashed" block @click="addScheduleWindow">
          <template #icon><PlusOutlined /></template>
          添加时段
        </a-button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { DeleteOutlined, PlusOutlined, QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import { createTimeScheduleWindowFormRow, weekdayOptions, type TimeScheduleForm } from './timeSchedule'

const props = withDefaults(defineProps<{
  form: { availabilitySchedule: TimeScheduleForm }
  readonly?: boolean
  label?: string
  readonlyLabel?: string
  helpMessage?: string
  rowKeyPrefix?: string
  bordered?: boolean
}>(), {
  label: '时间计划',
  readonlyLabel: '来源时间计划',
  rowKeyPrefix: 'time_schedule_window',
  bordered: true
})
const scheduleTooltipTitle = computed(() => props.helpMessage || '开启后只在配置的星期和时间段内参与调度；未命中时间段时会暂时跳过。')

function addScheduleWindow(): void {
  if (props.readonly) return
  props.form.availabilitySchedule.windows.push(createTimeScheduleWindowFormRow({ keyPrefix: props.rowKeyPrefix }))
}

function removeScheduleWindow(index: number): void {
  if (props.readonly) return
  if (props.form.availabilitySchedule.windows.length <= 1) return
  props.form.availabilitySchedule.windows.splice(index, 1)
}
</script>

<style scoped>
.time-schedule-section {
  width: 100%;
  background: transparent;
}

.time-schedule-section-bordered {
  padding: 0 0 16px;
  border-bottom: 1px solid #eef2f7;
}

.schedule-toggle-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
}

.schedule-toggle-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.help-icon {
  color: #94a3b8;
  cursor: help;
  font-size: 14px;
}

.help-icon:hover {
  color: #1677ff;
}

.schedule-config,
.schedule-window-list {
  display: grid;
  gap: 10px;
}

.schedule-config {
  margin-top: 12px;
}

.schedule-window-row {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) 112px 112px 32px;
  gap: 8px;
  align-items: start;
}

.schedule-days-select,
.schedule-time-picker {
  min-width: 0;
}

@media (max-width: 640px) {
  .schedule-window-row {
    grid-template-columns: minmax(0, 1fr) minmax(96px, 1fr);
  }

  .schedule-days-select {
    grid-column: 1 / -1;
  }

  .schedule-window-row > .ant-btn {
    grid-column: 2;
    justify-self: start;
  }
}
</style>

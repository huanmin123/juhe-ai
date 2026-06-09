<template>
  <section class="form-section">
    <div class="schedule-toggle-row">
      <span class="schedule-toggle-label">{{ readonly ? '来源可用时段计划' : '可用时段计划' }}</span>
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
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue'

import type { AccountFormModel } from './accountFormTypes'
import { createAccountScheduleWindowFormRow, weekdayOptions } from './accountAvailabilitySchedule'

const props = defineProps<{
  form: AccountFormModel
  readonly?: boolean
}>()

function addScheduleWindow(): void {
  if (props.readonly) return
  props.form.availabilitySchedule.windows.push(createAccountScheduleWindowFormRow())
}

function removeScheduleWindow(index: number): void {
  if (props.readonly) return
  if (props.form.availabilitySchedule.windows.length <= 1) return
  props.form.availabilitySchedule.windows.splice(index, 1)
}
</script>

<style scoped>
.form-section {
  padding: 0 0 16px;
  border-bottom: 1px solid #eef2f7;
  background: transparent;
}

.schedule-toggle-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
}

.schedule-toggle-label {
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.schedule-config,
.schedule-window-list {
  display: grid;
  gap: 10px;
}

.schedule-config {
  margin-top: 12px;
}

.schedule-help {
  font-size: 12px;
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

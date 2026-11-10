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
      <div v-if="!readonly" class="schedule-quick-row">
        <span class="schedule-quick-label">快捷预设</span>
        <template v-if="presets.length">
          <a-popconfirm
            v-for="preset in presets"
            :key="preset.key"
            :title="preset.invertOnApply
              ? `将按该供应商高峰定义（${preset.windows.map((w) => `${w.start}-${w.end}`).join('、')}）反向生成非高峰时段，覆盖当前 ${form.availabilitySchedule.windows.length} 个时段，确定？`
              : `将覆盖当前 ${form.availabilitySchedule.windows.length} 个时段为：${preset.description}，确定？`"
            ok-text="覆盖"
            cancel-text="取消"
            @confirm="applySchedulePreset(preset)"
          >
            <a-tag class="schedule-quick-tag" :title="preset.description">{{ preset.label }}</a-tag>
          </a-popconfirm>
        </template>
        <span v-else-if="peakPresetsDefined" class="schedule-quick-empty">当前供应商未配置高峰时段</span>
        <a-popconfirm
          title="把当前时段视为高峰并取反生成其余全部时间，将覆盖现有时段，确定？"
          ok-text="生成反向"
          cancel-text="取消"
          @confirm="invertScheduleWindows"
        >
          <a-button size="small" type="link" class="schedule-quick-invert">反向当前时段</a-button>
        </a-popconfirm>
      </div>
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

import { message } from '@/lib/antd'

import {
  createTimeScheduleWindowFormRow,
  invertTimeScheduleWindows,
  weekdayOptions,
  type TimeScheduleForm,
  type TimeSchedulePreset
} from './timeSchedule'

const props = withDefaults(defineProps<{
  form: { availabilitySchedule: TimeScheduleForm }
  readonly?: boolean
  label?: string
  readonlyLabel?: string
  helpMessage?: string
  rowKeyPrefix?: string
  bordered?: boolean
  /**
   * 供应商高峰快捷预设（动态数据源：调用方从 /providers/definitions 的
   * peakWindows 转换传入，见 peakWindowsToTimeSchedulePreset）。空数组 =
   * 当前供应商无高峰口径，只展示「反向当前时段」通用按钮；undefined =
   * 供应商未知（未选择），连占位也不显示。
   */
  peakPresets?: TimeSchedulePreset[]
}>(), {
  label: '时间计划',
  readonlyLabel: '来源时间计划',
  rowKeyPrefix: 'time_schedule_window',
  bordered: true
})

const presets = computed(() => props.peakPresets ?? [])
// undefined = 供应商未知（未选择），与「无高峰口径」的占位提示区分。
const peakPresetsDefined = computed(() => props.peakPresets !== undefined)
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

// 快捷预设：按预设定义覆盖时段并按供应商口径修正计划时区；
// invertOnApply 预设（高峰定义）先反向生成非高峰调度时段，反向为空则不覆盖。
function applySchedulePreset(preset: TimeSchedulePreset): void {
  if (props.readonly) return
  const scheduleWindows = preset.invertOnApply
    ? invertTimeScheduleWindows(preset.windows)
    : preset.windows
  if (!scheduleWindows.length) {
    message.warning('该预设的高峰覆盖全天，没有可生成的调度时段')
    return
  }
  props.form.availabilitySchedule.timezone = preset.timezone
  props.form.availabilitySchedule.windows = scheduleWindows.map((window) => createTimeScheduleWindowFormRow({
    daysOfWeek: window.daysOfWeek,
    start: window.start,
    end: window.end,
    keyPrefix: props.rowKeyPrefix
  }))
  message.success(`已应用预设：${preset.label}`)
}

// 反向生成：把当前时段视为高峰，覆盖为其补集（跨午夜拆段、零头丢弃）。
function invertScheduleWindows(): void {
  if (props.readonly) return
  const inverted = invertTimeScheduleWindows(props.form.availabilitySchedule.windows
    .filter((window) => window.start && window.end)
    .map((window) => ({ daysOfWeek: window.daysOfWeek, start: window.start as string, end: window.end as string })))
  if (!inverted.length) {
    message.warning('当前时段已覆盖全天，没有可生成的反向时段')
    return
  }
  props.form.availabilitySchedule.windows = inverted.map((window) => createTimeScheduleWindowFormRow({
    daysOfWeek: window.daysOfWeek,
    start: window.start,
    end: window.end,
    keyPrefix: props.rowKeyPrefix
  }))
  message.success(`已生成 ${inverted.length} 个反向时段`)
}
</script>

<style scoped>
.time-schedule-section {
  width: 100%;
  background: transparent;
}

.time-schedule-section-bordered {
  padding: 0 0 16px;
  border-bottom: 1px solid var(--juhe-border);
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
  color: var(--juhe-fg-soft);
  font-size: 13px;
  font-weight: 600;
}

.help-icon {
  color: var(--juhe-faint);
  cursor: help;
  font-size: 14px;
}

.help-icon:hover {
  color: var(--juhe-accent);
}

.schedule-config,
.schedule-window-list {
  display: grid;
  gap: 10px;
}

.schedule-config {
  margin-top: 12px;
}

.schedule-quick-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.schedule-quick-label {
  color: var(--juhe-faint);
  font-size: 12px;
}

.schedule-quick-tag {
  cursor: pointer;
  margin: 0;
}

.schedule-quick-empty {
  color: var(--juhe-faint);
  font-size: 12px;
}

.schedule-quick-invert {
  padding: 0;
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

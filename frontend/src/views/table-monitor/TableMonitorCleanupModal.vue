<template>
  <a-modal
    v-model:open="open"
    title="清理非业务数据"
    width="560px"
    ok-text="提交清理"
    cancel-text="取消"
    :confirm-loading="submitting"
    :ok-button-props="{ danger: true, disabled: submitting || !cutoffAt }"
    @ok="emit('submit')"
  >
    <a-alert
      show-icon
      type="warning"
      message="硬清理业务库之外的历史数据"
      description="系统会提交后台任务，按所选截止时间清理数据集目录库、统计结果库中具备时间列的全部非业务表，以及 usage shard 和审计 payload 外部文件；业务库不会清理。删除后 SQLite 文件大小不会立即变小，释放出的空闲页会留在库内供后续新增数据复用；只有需要归还磁盘时，才需要停服执行 VACUUM。"
    />
    <a-form class="cleanup-form" layout="vertical">
      <a-form-item label="清理这个时间之前的非业务数据" required>
        <a-date-picker
          v-model:value="cutoffAt"
          class="cleanup-date-picker"
          format="YYYY-MM-DD HH:mm:ss"
          show-time
          :disabled="submitting"
          :disabled-date="disabledCleanupDate"
          :disabled-time="disabledCleanupTime"
        />
      </a-form-item>
    </a-form>
    <a-alert
      v-if="result"
      class="cleanup-result"
      show-icon
      :type="cleanupResultType"
      :message="cleanupResultMessage"
      :description="cleanupResultDescription"
    />
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'

import { formatDateTime } from '@/shared/formatters'
import type { NonBusinessDataCleanupResult } from '@/types/domain'

import { formatInteger } from './tableMonitorDisplay'

type CleanupResultType = 'success' | 'info' | 'warning'

const open = defineModel<boolean>('open', { required: true })
const cutoffAt = defineModel<Dayjs | undefined>('cutoffAt', { required: true })

const props = defineProps<{
  submitting: boolean
  result?: NonBusinessDataCleanupResult
}>()

const emit = defineEmits<{
  submit: []
}>()

const cleanupResultType = computed<CleanupResultType>(() => {
  if (!props.result) return 'info'
  if (props.result.queued) return 'info'
  if (props.result.blockedReason) return 'warning'
  return props.result.deletedRows > 0 ? 'success' : 'info'
})

const cleanupResultMessage = computed(() => {
  const result = props.result
  if (!result) return ''
  if (result.queued) {
    return '后台清理任务已提交'
  }
  if (result.blockedReason) return '本次未清理'
  return result.deletedRows > 0
    ? `已清理 ${formatInteger(result.deletedRows)} 行非业务数据`
    : '没有可清理的非业务数据'
})

const cleanupResultDescription = computed(() => {
  const result = props.result
  if (!result) return ''
  if (result.blockedReason) return result.blockedReason
  if (result.queued) {
    const details = [
      `截止时间：${formatDateTime(result.cutoffAt)}`,
      result.submittedAt ? `提交时间：${formatDateTime(result.submittedAt)}` : undefined,
      result.jobId ? `任务：${result.jobId}` : undefined,
      'worker 会在后台分批清理，稍后刷新表监控可查看数据集目录库、统计结果库和 usage shard 变化。'
    ].filter((item): item is string => Boolean(item))
    return details.join('；')
  }
  const details = [
    `截止时间：${formatDateTime(result.cutoffAt)}`,
    result.hasMore ? '本次达到批量上限，仍有可清理记录，可再次执行。' : '当前截止时间前没有更多待清理记录。'
  ].filter((item): item is string => Boolean(item))
  return details.join('；')
})

function disabledCleanupDate(current: Dayjs) {
  return current.isAfter(latestAllowedCleanupCutoff(), 'day')
}

function disabledCleanupTime(current?: Dayjs | null) {
  const latestAllowed = latestAllowedCleanupCutoff()
  if (!current?.isSame(latestAllowed, 'day')) {
    return {}
  }
  return {
    disabledHours: () => range(latestAllowed.hour() + 1, 24),
    disabledMinutes: (selectedHour: number) => selectedHour === latestAllowed.hour() ? range(latestAllowed.minute() + 1, 60) : [],
    disabledSeconds: (selectedHour: number, selectedMinute: number) => (
      selectedHour === latestAllowed.hour() && selectedMinute === latestAllowed.minute()
        ? range(latestAllowed.second() + 1, 60)
        : []
    )
  }
}

function latestAllowedCleanupCutoff() {
  return dayjs()
}

function range(start: number, end: number) {
  const output: number[] = []
  for (let value = Math.max(0, start); value < end; value += 1) {
    output.push(value)
  }
  return output
}
</script>

<style scoped>
.cleanup-form {
  margin-top: 16px;
}

.cleanup-date-picker {
  width: 100%;
}

.cleanup-result {
  margin-top: 12px;
}
</style>

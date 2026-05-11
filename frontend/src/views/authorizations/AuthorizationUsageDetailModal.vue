<template>
  <a-modal v-model:open="open" :title="title" width="1040px" :footer="null">
    <template v-if="authorization">
      <div class="usage-toolbar">
        <a-range-picker
          v-model:value="localDateRange"
          :allow-clear="false"
          :disabled="loading"
          :disabled-date="disabledDate"
          class="usage-range-picker"
          format="YYYY-MM-DD"
          @calendar-change="handleCalendarChange"
          @change="emitRangeChange"
          @open-change="handleDateRangeOpenChange"
        />
        <a-segmented v-model:value="localGroupBy" :disabled="loading" :options="groupByOptions" @change="emitRangeChange" />
        <a-button :loading="loading" @click="emitRangeChange">刷新</a-button>
      </div>
      <a-alert
        class="usage-alert"
        type="info"
        show-icon
        :message="`${rangeLabel}授权总计（不含归属人自己消耗）：${usageSummaryText(authorization.usage)}`"
      />
      <div class="usage-section-title">{{ groupByText }}消耗</div>
      <ResponsiveDataList size="small" :columns="usageBucketColumns" :data-source="usageBuckets" row-key="bucketKey" :pagination="false" :table-scroll-enabled="false" :lock-body-scroll="false">
        <template #emptyText>
          <a-empty description="暂无用量趋势" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'period'">
            {{ bucketPeriodText(record) }}
          </template>
          <template v-else-if="column.key === 'usage'">
            {{ usageSummaryText(record) }}
          </template>
          <template v-else-if="column.key === 'lastUsedAt'">
            {{ formatDateTime(record.lastUsedAt) }}
          </template>
        </template>
        <template #card="{ record }">
          <article class="usage-detail-card">
            <strong>{{ bucketPeriodText(record) }}</strong>
            <span>{{ usageSummaryText(record) }}</span>
            <span>最近使用：{{ formatDateTime(record.lastUsedAt) }}</span>
          </article>
        </template>
      </ResponsiveDataList>
      <div v-if="teamUsageSummaries.length" class="usage-team-section">
        <div class="usage-section-title">团队来源范围消耗</div>
        <div class="usage-team-cards">
          <article v-for="summary in teamUsageSummaries" :key="summary.teamId" class="usage-team-card">
            <div class="usage-team-card-head">
              <span class="usage-team-card-title">{{ summary.teamName }}</span>
              <a-tag color="gold">团队来源</a-tag>
            </div>
            <strong class="usage-team-card-summary">{{ usageSummaryText(summary.usage) }}</strong>
            <span class="usage-team-card-meta">成员 {{ summary.memberCount }} 人</span>
          </article>
        </div>
        <div class="usage-section-title usage-subsection-title">团队来源成员范围消耗</div>
        <ResponsiveDataList size="small" :columns="teamUsageColumns" :data-source="teamUsageRows" row-key="key" :pagination="false" :table-scroll-enabled="false" :lock-body-scroll="false">
          <template #emptyText>
            <a-empty description="暂无团队成员用量" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'teamName'">
              {{ record.teamName }}
            </template>
            <template v-else-if="column.key === 'memberName'">
              {{ record.systemAccountName || '未命名成员' }}
            </template>
            <template v-else-if="column.key === 'usage'">
              {{ usageSummaryText(record.usage) }}
            </template>
          </template>
          <template #card="{ record }">
            <article class="usage-detail-card">
              <strong>{{ record.systemAccountName || '未命名成员' }}</strong>
              <span>团队：{{ record.teamName }}</span>
              <span>{{ usageSummaryText(record.usage) }}</span>
            </article>
          </template>
        </ResponsiveDataList>
      </div>
      <div class="usage-section-title">每系统账户范围消耗</div>
      <ResponsiveDataList size="small" :columns="usageDetailColumns" :data-source="usageDetails" row-key="systemAccountId" :pagination="false" :table-scroll-enabled="false" :lock-body-scroll="false">
        <template #emptyText>
          <a-empty description="暂无用量明细" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            {{ record.systemAccountName || '未知账户' }}
          </template>
          <template v-else-if="column.key === 'usage'">
            {{ usageSummaryText(record.rangeUsage ?? record) }}
          </template>
          <template v-else-if="column.key === 'lastUsedAt'">
            {{ formatDateTime(record.lastUsedAt) }}
          </template>
        </template>
        <template #card="{ record }">
          <article class="usage-detail-card">
            <strong>{{ record.systemAccountName || '未知账户' }}</strong>
            <span>{{ usageSummaryText(record.rangeUsage ?? record) }}</span>
            <span>最近使用：{{ formatDateTime(record.lastUsedAt) }}</span>
          </article>
        </template>
      </ResponsiveDataList>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import dayjs, { type Dayjs } from 'dayjs'
import { computed, ref, watch } from 'vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import type { AuthorizationUsageBucket, AuthorizationUsageGroupBy, AuthorizationUserUsageDetail, ResourceAuthorizationSummary } from '@/types/domain'
import { formatDateTime, usageSummaryText, type TeamUsageSummary } from './authorizationFormatters'

type TeamUsageRow = TeamUsageSummary['members'][number]
type DateRangeValue = [Dayjs, Dayjs]

const MAX_RANGE_DAYS = 31

const groupByOptions: Array<{ label: string; value: AuthorizationUsageGroupBy }> = [
  { label: '按日', value: 'day' },
  { label: '按周', value: 'week' }
]
const usageBucketColumns = [
  { title: '时间', key: 'period', width: 220 },
  { title: '用量', key: 'usage', width: 220 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 }
]

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  authorization?: ResourceAuthorizationSummary
  groupBy: AuthorizationUsageGroupBy
  loading: boolean
  range: DateRangeValue
  teamUsageColumns: Array<Record<string, unknown>>
  teamUsageRows: TeamUsageRow[]
  teamUsageSummaries: TeamUsageSummary[]
  usageDetailColumns: Array<Record<string, unknown>>
  usageDetails: AuthorizationUserUsageDetail[]
}>()

const emit = defineEmits<{
  (event: 'range-change', payload: { range: DateRangeValue; groupBy: AuthorizationUsageGroupBy }): void
}>()

const localDateRange = ref<DateRangeValue>(props.range)
const calendarRange = ref<Array<Dayjs | null>>([])
const localGroupBy = ref<AuthorizationUsageGroupBy>(props.groupBy)

const title = computed(() => props.authorization
  ? `用量明细：${props.authorization.resourceName || props.authorization.resourceId}`
  : '用量明细')
const usageBuckets = computed<AuthorizationUsageBucket[]>(() => props.authorization?.usageBuckets ?? props.usageDetails[0]?.usageBuckets ?? [])
const groupByText = computed(() => localGroupBy.value === 'week' ? '每周' : '每日')
const rangeLabel = computed(() => {
  const range = props.authorization?.usageRange
  if (range) {
    return range.startDate === range.endDate ? `${formatDateLabel(range.startDate)} ` : `${formatDateLabel(range.startDate)} 至 ${formatDateLabel(range.endDate)} `
  }
  const [start, end] = localDateRange.value
  return start.isSame(end, 'day') ? `${start.format('M月D日')} ` : `${start.format('M月D日')} 至 ${end.format('M月D日')} `
})

watch(() => props.range, (value) => {
  localDateRange.value = value
}, { deep: true })

watch(() => props.groupBy, (value) => {
  localGroupBy.value = value
})

function handleCalendarChange(value: Array<Dayjs | null>) {
  calendarRange.value = value
}

function handleDateRangeOpenChange(opened: boolean) {
  if (!opened) {
    calendarRange.value = []
  }
}

function disabledDate(current: Dayjs) {
  if (!current) return false
  if (current.isAfter(dayjs(), 'day')) return true
  const anchor = calendarRange.value[0] ?? calendarRange.value[1]
  if (!anchor) return false
  return Math.abs(current.startOf('day').diff(anchor.startOf('day'), 'day')) > MAX_RANGE_DAYS - 1
}

function emitRangeChange() {
  emit('range-change', {
    range: normalizedDateRange(localDateRange.value),
    groupBy: localGroupBy.value
  })
}

function normalizedDateRange(value: DateRangeValue): DateRangeValue {
  const today = dayjs().startOf('day')
  let start = value[0].startOf('day')
  let end = value[1].startOf('day')
  if (end.isAfter(today, 'day')) {
    end = today
  }
  if (start.isAfter(end, 'day')) {
    start = end
  }
  if (end.diff(start, 'day') > MAX_RANGE_DAYS - 1) {
    start = end.subtract(MAX_RANGE_DAYS - 1, 'day')
  }
  return [start, end]
}

function bucketPeriodText(bucket: AuthorizationUsageBucket): string {
  if (bucket.startDate === bucket.endDate) {
    return formatDateLabel(bucket.startDate)
  }
  return `${formatDateLabel(bucket.startDate)} 至 ${formatDateLabel(bucket.endDate)}`
}

function formatDateLabel(value: string): string {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return value
  const [year, month, day] = value.split('-').map((part) => Number(part))
  const parsed = dayjs(new Date(year, month - 1, day)).startOf('day')
  if (parsed.year() !== year || parsed.month() !== month - 1 || parsed.date() !== day) return value
  return parsed.isValid() ? parsed.format('M月D日') : value
}
</script>

<style scoped>
.usage-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  margin-bottom: 12px;
}

.usage-range-picker {
  width: 260px;
}

.usage-alert {
  margin-bottom: 12px;
}

.usage-team-section {
  display: grid;
  gap: 12px;
  margin: 16px 0;
}

.usage-section-title {
  margin: 12px 0 8px;
  color: #0f172a;
  font-size: 14px;
  font-weight: 700;
}

.usage-subsection-title {
  margin-top: -2px;
}

.usage-team-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
}

.usage-team-card {
  display: grid;
  gap: 8px;
  padding: 14px;
  border: 1px solid #e8edf5;
  border-radius: 8px;
  background: #fffdf5;
}

.usage-team-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.usage-team-card-title {
  color: #0f172a;
  font-weight: 700;
}

.usage-team-card-summary {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 14px;
}

.usage-team-card-meta {
  color: #64748b;
  font-size: 12px;
}

.usage-detail-card {
  display: grid;
  gap: 6px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
  color: #64748b;
  font-size: 12px;
}

.usage-detail-card strong {
  color: #0f172a;
  font-size: 13px;
}

@media (max-width: 640px) {
  .usage-toolbar {
    align-items: stretch;
  }

  .usage-range-picker {
    width: 100%;
  }
}
</style>

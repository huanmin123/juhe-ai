<template>
  <ResponsiveListToolbar
    :show-search="false"
    filter-title="筛选授权"
    :active-filter-count="activeFilterCount"
    :refresh-loading="loading"
    @reset="$emit('reset')"
    @refresh="$emit('refresh')"
    >
    <template #inline-filters>
      <a-segmented v-if="!isManagementView" v-model:value="filters.direction" class="direction-filter responsive-list-inline-filter" :options="directionOptions" @change="$emit('refresh')" />
      <a-range-picker
        v-model:value="usageDateRangeValue"
        :allow-clear="false"
        :disabled="loading"
        :disabled-date="disabledUsageDate"
        class="usage-range-picker responsive-list-inline-filter"
        format="YYYY-MM-DD"
        @calendar-change="handleUsageCalendarChange"
        @change="handleUsageDateRangeChange"
        @open-change="handleUsageDateRangeOpenChange"
      />
      <a-select v-if="!isManagementView" v-model:value="filters.sourceType" class="filter-select responsive-list-inline-filter" :options="sourceOptions" @change="$emit('refresh')" />
      <a-select v-if="isManagementView" v-model:value="filters.resourceType" class="filter-select responsive-list-inline-filter" :options="resourceTypeOptions" @change="$emit('resource-type-change')" />
      <a-select
        v-if="isManagementView"
        v-model:value="filters.resourceId"
        show-search
        allow-clear
        option-filter-prop="label"
        class="filter-select filter-resource responsive-list-inline-filter"
        :options="resourceOptions"
        :disabled="filters.resourceType === 'all'"
        :placeholder="filters.resourceType === 'all' ? '先选择资源类型' : '筛选资源'"
        @change="$emit('refresh')"
      />
      <SystemPrincipalSelect
        v-if="isManagementView"
        v-model:value="filters.teamId"
        :teams="teams"
        :active-only="false"
        allow-clear
        class="filter-select responsive-list-inline-filter"
        placeholder="筛选授权来源"
        scope="team"
        @change="$emit('refresh')"
      />
      <SystemPrincipalSelect
        v-if="isManagementView"
        v-model:value="filters.granteeSystemAccountId"
        :accounts="users"
        :active-only="false"
        allow-clear
        class="filter-select filter-user responsive-list-inline-filter"
        placeholder="筛选被授权用户"
        @change="$emit('refresh')"
      />
    </template>
    <template #actions>
      <a-button @click="$emit('help')">
        <template #icon><question-circle-outlined /></template>
        授权帮助
      </a-button>
      <a-button type="primary" @click="$emit('create')">新增授权</a-button>
    </template>
    <template #filters>
      <label v-if="!isManagementView" class="mobile-filter-field">
        <span>授权方向</span>
        <a-segmented v-model:value="filters.direction" :options="directionOptions" @change="$emit('refresh')" />
      </label>
      <label class="mobile-filter-field">
        <span>用量时间</span>
        <a-range-picker
          v-model:value="usageDateRangeValue"
          :allow-clear="false"
          :disabled="loading"
          :disabled-date="disabledUsageDate"
          format="YYYY-MM-DD"
          @calendar-change="handleUsageCalendarChange"
          @change="handleUsageDateRangeChange"
          @open-change="handleUsageDateRangeOpenChange"
        />
      </label>
      <label v-if="!isManagementView" class="mobile-filter-field">
        <span>授权对象</span>
        <a-select v-model:value="filters.sourceType" :options="sourceOptions" @change="$emit('refresh')" />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>资源类型</span>
        <a-select v-model:value="filters.resourceType" :options="resourceTypeOptions" @change="$emit('resource-type-change')" />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>资源</span>
        <a-select
          v-model:value="filters.resourceId"
          show-search
          allow-clear
          option-filter-prop="label"
          :options="resourceOptions"
          :disabled="filters.resourceType === 'all'"
          :placeholder="filters.resourceType === 'all' ? '先选择资源类型' : '筛选资源'"
          @change="$emit('refresh')"
        />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>授权来源</span>
        <SystemPrincipalSelect v-model:value="filters.teamId" :teams="teams" :active-only="false" allow-clear scope="team" placeholder="筛选授权来源" @change="$emit('refresh')" />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>被授权用户</span>
        <SystemPrincipalSelect v-model:value="filters.granteeSystemAccountId" :accounts="users" :active-only="false" allow-clear placeholder="筛选被授权用户" @change="$emit('refresh')" />
      </label>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import dayjs, { type Dayjs } from 'dayjs'
import { computed, ref } from 'vue'

import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { SystemAccountPrincipalSummary, SystemTeamSummary } from '@/types/domain'
import type { AuthorizationDirectionFilter, AuthorizationFilterResourceType, AuthorizationSourceFilter } from './authorizationTableColumns'

type DateRangeValue = [Dayjs, Dayjs]
const MAX_USAGE_RANGE_DAYS = 31

const props = defineProps<{
  filters: {
    direction: AuthorizationDirectionFilter
    sourceType: AuthorizationSourceFilter
    resourceType: AuthorizationFilterResourceType
    resourceId?: string
    teamId?: string
    granteeSystemAccountId?: string
    usageDateRange: [string, string]
  }
  isManagementView: boolean
  directionOptions: Array<{ label: string; value: AuthorizationDirectionFilter }>
  sourceOptions: Array<{ label: string; value: AuthorizationSourceFilter }>
  resourceTypeOptions: Array<{ label: string; value: AuthorizationFilterResourceType }>
  resourceOptions: Array<{ label: string; value: string }>
  teams: SystemTeamSummary[]
  users: SystemAccountPrincipalSummary[]
  activeFilterCount: number
  loading: boolean
}>()

const emit = defineEmits<{
  (event: 'create'): void
  (event: 'help'): void
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'resource-type-change'): void
}>()

const usageCalendarRange = ref<Array<Dayjs | null>>([])
const usageDateRangeValue = computed<DateRangeValue>({
  get() {
    const [start, end] = props.filters.usageDateRange
    return [dayjs(start).startOf('day'), dayjs(end).startOf('day')]
  },
  set(value) {
    props.filters.usageDateRange = normalizeUsageDateRange(value).map((date) => date.format('YYYY-MM-DD')) as [string, string]
  }
})

function handleUsageCalendarChange(value: Array<Dayjs | null>) {
  usageCalendarRange.value = value
}

function handleUsageDateRangeOpenChange(opened: boolean) {
  if (!opened) {
    usageCalendarRange.value = []
  }
}

function handleUsageDateRangeChange() {
  usageDateRangeValue.value = usageDateRangeValue.value
  emit('refresh')
}

function disabledUsageDate(current: Dayjs) {
  if (!current) return false
  if (current.isAfter(dayjs(), 'day')) return true
  const anchor = usageCalendarRange.value[0] ?? usageCalendarRange.value[1]
  if (!anchor) return false
  return Math.abs(current.startOf('day').diff(anchor.startOf('day'), 'day')) > MAX_USAGE_RANGE_DAYS - 1
}

function normalizeUsageDateRange(value: DateRangeValue): DateRangeValue {
  const today = dayjs().startOf('day')
  let start = value[0].startOf('day')
  let end = value[1].startOf('day')
  if (end.isAfter(today, 'day')) {
    end = today
  }
  if (start.isAfter(end, 'day')) {
    start = end
  }
  if (end.diff(start, 'day') > MAX_USAGE_RANGE_DAYS - 1) {
    start = end.subtract(MAX_USAGE_RANGE_DAYS - 1, 'day')
  }
  return [start, end]
}
</script>

<style scoped>
.filter-select {
  min-width: 140px;
}

.direction-filter {
  width: max-content;
}

.filter-resource,
.filter-user {
  min-width: 220px;
}

.usage-range-picker {
  width: 240px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}
</style>

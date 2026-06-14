<template>
  <ResponsiveListToolbar
    v-if="viewMode === 'index'"
    v-model:keyword="traceIdFilterModel"
    search-placeholder="搜索 traceId"
    filter-title="日志筛选"
    :active-filter-count="activeFilterCount"
    :advanced-filter-count="advancedFilterCount"
    :refresh-loading="loading"
    @refresh="emit('refreshIndex')"
    @reset="emit('resetIndex')"
    @search="emit('applyIndex')"
  >
    <template #inline-filters>
      <a-select v-model:value="levelFilterModel" class="toolbar-select log-level-filter responsive-list-inline-filter" :options="levelOptions" @change="emit('applyIndex')" />
      <a-select
        v-model:value="eventFilterModel"
        allow-clear
        show-search
        class="toolbar-select runtime-event-filter responsive-list-inline-filter"
        placeholder="事件"
        :options="eventOptions"
        :filter-option="filterEventOption"
        @change="emit('applyIndex')"
      />
    </template>
    <template #advanced-filters>
      <a-form layout="vertical" class="advanced-filter-form">
        <a-form-item label="关键字">
          <a-input v-model:value="keywordFilterModel" allow-clear placeholder="模糊匹配消息列" @press-enter="emit('applyIndex')" />
        </a-form-item>
        <a-form-item label="索引时间范围">
          <a-range-picker
            v-model:value="indexTimeRangeModel"
            allow-clear
            show-time
            class="drawer-range-picker"
            :disabled-date="disabledIndexDate"
            :placeholder="['索引开始时间', '索引结束时间']"
            @change="emit('indexRangeChange')"
          />
        </a-form-item>
      </a-form>
    </template>
    <template #actions>
      <TableColumnManager
        :columns="runtimeLogColumns"
        :settings="indexColumnSettings"
        :required-keys="['message']"
        @reset="emit('resetIndexColumnSettings')"
        @update:settings="emit('update:indexColumnSettings', $event)"
      />
      <a-segmented v-model:value="viewModeModel" class="log-mode-segmented" :options="viewModeOptions" @change="handleModeChange" />
    </template>
    <template #filters>
      <a-form layout="vertical">
        <a-form-item label="级别">
          <a-select v-model:value="levelFilterModel" :options="levelOptions" />
        </a-form-item>
        <a-form-item label="事件">
          <a-select v-model:value="eventFilterModel" allow-clear show-search :options="eventOptions" :filter-option="filterEventOption" placeholder="选择或输入事件" />
        </a-form-item>
        <a-form-item label="关键字">
          <a-input v-model:value="keywordFilterModel" allow-clear placeholder="模糊匹配消息列" />
        </a-form-item>
        <a-form-item label="索引时间范围">
          <a-range-picker
            v-model:value="indexTimeRangeModel"
            allow-clear
            show-time
            class="drawer-range-picker"
            :disabled-date="disabledIndexDate"
            :placeholder="['索引开始时间', '索引结束时间']"
            @change="emit('indexRangeChange')"
          />
        </a-form-item>
      </a-form>
    </template>
  </ResponsiveListToolbar>

  <ResponsiveListToolbar
    v-else
    v-model:keyword="grepKeywordFilterModel"
    search-placeholder="后端 rg 搜索任意关键字，空格分隔表示同时命中"
    filter-title="grep 文件范围"
    :active-filter-count="grepActiveFilterCount"
    :refresh-loading="loading"
    @refresh="emit('searchGrep')"
    @reset="emit('resetGrep')"
    @search="emit('searchGrep')"
  >
    <template #inline-filters>
      <a-range-picker
        v-model:value="grepTimeRangeModel"
        :allow-clear="false"
        class="toolbar-select grep-time-range responsive-list-inline-filter"
        show-time
        :title="grepRangeLimitText"
        :disabled-date="disabledGrepDate"
        :placeholder="['文件开始时间', '文件结束时间']"
        @change="emit('grepRangeChange')"
      />
    </template>
    <template #actions>
      <TableColumnManager
        :columns="runtimeLogColumns"
        :settings="grepColumnSettings"
        :required-keys="['message']"
        @reset="emit('resetGrepColumnSettings')"
        @update:settings="emit('update:grepColumnSettings', $event)"
      />
      <a-segmented v-model:value="viewModeModel" class="log-mode-segmented" :options="viewModeOptions" @change="handleModeChange" />
    </template>
    <template #filters>
      <a-form layout="vertical">
        <a-form-item label="文件时间范围">
          <a-range-picker
            v-model:value="grepTimeRangeModel"
            :allow-clear="false"
            show-time
            class="drawer-range-picker"
            :title="grepRangeLimitText"
            :disabled-date="disabledGrepDate"
            :placeholder="['文件开始时间', '文件结束时间']"
            @change="emit('grepRangeChange')"
          />
        </a-form-item>
      </a-form>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Dayjs } from 'dayjs'

import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import type { TableColumnSetting } from '@/components/tableColumnSettings'
import type { RuntimeLogLevel } from '@/types/domain'
import type { RuntimeLogEventOption } from './runtimeLogFacets'
import type { RuntimeLogTimeRangeValue } from './runtimeLogTimeRanges'

type RuntimeLogViewMode = 'index' | 'grep'
type RuntimeLogLevelFilter = RuntimeLogLevel | 'all'
type RuntimeLogOption<T extends string = string> = {
  label: string
  value: T
}

const props = defineProps<{
  activeFilterCount: number
  advancedFilterCount: number
  disabledGrepDate: (current: Dayjs) => boolean
  disabledIndexDate: (current: Dayjs) => boolean
  eventFilter?: string
  eventOptions: RuntimeLogEventOption[]
  filterEventOption: (input: string, option?: { label?: string; rawEvent?: string; value?: string }) => boolean
  grepActiveFilterCount: number
  grepColumnSettings: TableColumnSetting[]
  grepKeywordFilter: string
  grepRangeLimitText: string
  grepTimeRange?: [Dayjs, Dayjs]
  indexColumnSettings: TableColumnSetting[]
  indexTimeRange: RuntimeLogTimeRangeValue
  keywordFilter: string
  levelFilter: RuntimeLogLevelFilter
  levelOptions: RuntimeLogOption[]
  loading: boolean
  runtimeLogColumns: Array<Record<string, any>>
  traceIdFilter: string
  viewMode: RuntimeLogViewMode
  viewModeOptions: RuntimeLogOption[]
}>()

const emit = defineEmits<{
  (event: 'applyIndex'): void
  (event: 'grepRangeChange'): void
  (event: 'indexRangeChange'): void
  (event: 'modeChange', value: string | number): void
  (event: 'refreshIndex'): void
  (event: 'resetGrep'): void
  (event: 'resetGrepColumnSettings'): void
  (event: 'resetIndex'): void
  (event: 'resetIndexColumnSettings'): void
  (event: 'searchGrep'): void
  (event: 'update:eventFilter', value?: string): void
  (event: 'update:grepColumnSettings', value: TableColumnSetting[]): void
  (event: 'update:grepKeywordFilter', value: string): void
  (event: 'update:grepTimeRange', value?: [Dayjs, Dayjs]): void
  (event: 'update:indexColumnSettings', value: TableColumnSetting[]): void
  (event: 'update:indexTimeRange', value: RuntimeLogTimeRangeValue): void
  (event: 'update:keywordFilter', value: string): void
  (event: 'update:levelFilter', value: RuntimeLogLevelFilter): void
  (event: 'update:traceIdFilter', value: string): void
  (event: 'update:viewMode', value: RuntimeLogViewMode): void
}>()

const traceIdFilterModel = computed({
  get: () => props.traceIdFilter,
  set: (value: string) => emit('update:traceIdFilter', value)
})
const grepKeywordFilterModel = computed({
  get: () => props.grepKeywordFilter,
  set: (value: string) => emit('update:grepKeywordFilter', value)
})
const levelFilterModel = computed({
  get: () => props.levelFilter,
  set: (value: RuntimeLogLevelFilter) => emit('update:levelFilter', value)
})
const eventFilterModel = computed({
  get: () => props.eventFilter,
  set: (value?: string) => emit('update:eventFilter', value)
})
const keywordFilterModel = computed({
  get: () => props.keywordFilter,
  set: (value: string) => emit('update:keywordFilter', value)
})
const indexTimeRangeModel = computed({
  get: () => props.indexTimeRange,
  set: (value: RuntimeLogTimeRangeValue) => emit('update:indexTimeRange', value)
})
const grepTimeRangeModel = computed({
  get: () => props.grepTimeRange,
  set: (value?: [Dayjs, Dayjs]) => emit('update:grepTimeRange', value)
})
const viewModeModel = computed({
  get: () => props.viewMode,
  set: (value: RuntimeLogViewMode) => emit('update:viewMode', value)
})

function handleModeChange(value: string | number): void {
  emit('modeChange', value)
}
</script>

<style scoped>
.log-mode-segmented {
  flex: none;
}

.runtime-event-filter {
  width: 210px;
}

.log-level-filter {
  width: 108px;
}

.grep-time-range {
  width: 380px;
}

.drawer-range-picker {
  width: 100%;
}

.advanced-filter-form :deep(.ant-input),
.advanced-filter-form :deep(.ant-picker) {
  width: 100%;
}
</style>

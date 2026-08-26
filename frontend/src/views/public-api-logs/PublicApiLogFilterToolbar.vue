<template>
  <ResponsiveListToolbar
    v-model:keyword="traceIdModel"
    search-placeholder="搜索 traceId"
    filter-title="公开接口筛选"
    :active-filter-count="activeFilterCount"
    :advanced-filter-count="advancedFilterCount"
    :refresh-loading="loading"
    @refresh="emit('refresh')"
    @reset="emit('reset')"
    @search="emit('search')"
  >
    <template #inline-filters>
      <a-select v-model:value="resultModel" class="toolbar-select result-filter responsive-list-inline-filter" :options="resultOptions" @change="emit('search')" />
    </template>
    <template #advanced-filters>
      <a-form layout="vertical" class="advanced-filter-form">
        <a-form-item label="来源系统 ID">
          <a-input v-model:value="sourceRefIdModel" allow-clear placeholder="extsrc_xxx" @press-enter="emit('search')" />
        </a-form-item>
        <a-form-item label="接口路径">
          <a-input v-model:value="pathModel" allow-clear placeholder="/__aipublic__/api-key/list" @press-enter="emit('search')" />
        </a-form-item>
        <a-form-item label="状态码">
          <a-input v-model:value="statusCodeModel" allow-clear placeholder="200 / 401 / 500" @press-enter="emit('search')" />
        </a-form-item>
        <a-form-item label="客户端 IP">
          <a-input v-model:value="clientIpModel" allow-clear placeholder="203.0.113." @press-enter="emit('search')" />
        </a-form-item>
        <a-form-item label="调用时间范围">
          <a-range-picker
            v-model:value="timeRangeModel"
            allow-clear
            show-time
            class="drawer-range-picker"
            :placeholder="['开始时间', '结束时间']"
            @change="emit('search')"
          />
        </a-form-item>
      </a-form>
    </template>
    <template #filters>
      <a-form layout="vertical">
        <a-form-item label="结果">
          <a-select v-model:value="resultModel" :options="resultOptions" />
        </a-form-item>
        <a-form-item label="来源系统 ID">
          <a-input v-model:value="sourceRefIdModel" allow-clear placeholder="extsrc_xxx" />
        </a-form-item>
        <a-form-item label="接口路径">
          <a-input v-model:value="pathModel" allow-clear placeholder="/__aipublic__/api-key/list" />
        </a-form-item>
        <a-form-item label="状态码">
          <a-input v-model:value="statusCodeModel" allow-clear placeholder="200 / 401 / 500" />
        </a-form-item>
        <a-form-item label="客户端 IP">
          <a-input v-model:value="clientIpModel" allow-clear placeholder="203.0.113." />
        </a-form-item>
        <a-form-item label="调用时间范围">
          <a-range-picker
            v-model:value="timeRangeModel"
            allow-clear
            show-time
            class="drawer-range-picker"
            :placeholder="['开始时间', '结束时间']"
          />
        </a-form-item>
      </a-form>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { PublicApiLogResultFilter } from '@/types/domain'
import type { PublicApiLogTimeRangeValue } from './publicApiLogFormatters'

const props = defineProps<{
  activeFilterCount: number
  advancedFilterCount: number
  clientIpFilter: string
  loading: boolean
  pathFilter: string
  resultFilter: PublicApiLogResultFilter
  resultOptions: Array<{ label: string; value: PublicApiLogResultFilter }>
  sourceRefIdFilter: string
  statusCodeFilter: string
  timeRange: PublicApiLogTimeRangeValue
  traceIdFilter: string
}>()

const emit = defineEmits<{
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'search'): void
  (event: 'update:clientIpFilter', value: string): void
  (event: 'update:pathFilter', value: string): void
  (event: 'update:resultFilter', value: PublicApiLogResultFilter): void
  (event: 'update:sourceRefIdFilter', value: string): void
  (event: 'update:statusCodeFilter', value: string): void
  (event: 'update:timeRange', value: PublicApiLogTimeRangeValue): void
  (event: 'update:traceIdFilter', value: string): void
}>()

const traceIdModel = computed({
  get: () => props.traceIdFilter,
  set: (value: string) => emit('update:traceIdFilter', value)
})
const resultModel = computed({
  get: () => props.resultFilter,
  set: (value: PublicApiLogResultFilter) => emit('update:resultFilter', value)
})
const sourceRefIdModel = computed({
  get: () => props.sourceRefIdFilter,
  set: (value: string) => emit('update:sourceRefIdFilter', value)
})
const pathModel = computed({
  get: () => props.pathFilter,
  set: (value: string) => emit('update:pathFilter', value)
})
const statusCodeModel = computed({
  get: () => props.statusCodeFilter,
  set: (value: string) => emit('update:statusCodeFilter', value)
})
const clientIpModel = computed({
  get: () => props.clientIpFilter,
  set: (value: string) => emit('update:clientIpFilter', value)
})
const timeRangeModel = computed({
  get: () => props.timeRange,
  set: (value: PublicApiLogTimeRangeValue) => emit('update:timeRange', value)
})
</script>

<style scoped>
.result-filter {
  width: 112px;
}

.drawer-range-picker {
  width: 100%;
}

.advanced-filter-form :deep(.ant-input),
.advanced-filter-form :deep(.ant-picker) {
  width: 100%;
}
</style>

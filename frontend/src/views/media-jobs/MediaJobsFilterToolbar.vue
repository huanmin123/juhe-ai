<template>
  <ResponsiveListToolbar
    :show-search="false"
    filter-title="媒体任务筛选"
    :active-filter-count="activeFilterCount"
    :advanced-filter-count="0"
    :refresh-loading="loading"
    @refresh="emit('refresh')"
    @reset="emit('reset')"
    @search="emit('search')"
  >
    <template #inline-filters>
      <a-select
        v-model:value="kindModel"
        class="toolbar-select kind-filter responsive-list-inline-filter"
        :options="kindOptions"
        @change="emit('search')"
      />
      <a-select
        v-model:value="statusModel"
        class="toolbar-select status-filter responsive-list-inline-filter"
        :options="statusOptions"
        @change="emit('search')"
      />
    </template>
    <template #filters>
      <a-form layout="vertical">
        <a-form-item label="类型">
          <a-select v-model:value="kindModel" class="full-width-select" :options="kindOptions" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="statusModel" class="full-width-select" :options="statusOptions" />
        </a-form-item>
      </a-form>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { MediaJobKindFilter, MediaJobStatusFilter } from './mediaJobsOptions'

const props = defineProps<{
  activeFilterCount: number
  kindFilter: MediaJobKindFilter
  loading: boolean
  statusFilter: MediaJobStatusFilter
  kindOptions: Array<{ label: string; value: MediaJobKindFilter }>
  statusOptions: Array<{ label: string; value: MediaJobStatusFilter }>
}>()

const emit = defineEmits<{
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'search'): void
  (event: 'update:kindFilter', value: MediaJobKindFilter): void
  (event: 'update:statusFilter', value: MediaJobStatusFilter): void
}>()

const kindModel = computed({
  get: () => props.kindFilter,
  set: (value: MediaJobKindFilter) => emit('update:kindFilter', value)
})
const statusModel = computed({
  get: () => props.statusFilter,
  set: (value: MediaJobStatusFilter) => emit('update:statusFilter', value)
})
</script>

<style scoped>
.kind-filter {
  width: 128px;
}

.status-filter {
  width: 112px;
}

.full-width-select {
  width: 100%;
}
</style>
